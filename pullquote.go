package main

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha1"
	"errors"
	"flag"
	"fmt"
	"hash"
	"io"
	"io/ioutil"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"unicode"
	"unicode/utf8"
)

var (
	logger   = log.New(os.Stderr, "", 0)
	debug, _ = strconv.ParseBool(os.Getenv("DEBUG"))
)

func main() {
	if debug {
		logger = log.New(os.Stderr, "", log.LstdFlags|log.Lshortfile)
	}

	checkMode := flag.Bool("check", false, "whether to run in check mode")
	walk := flag.Bool("walk", false, "whether to automatically discover all targets")

	flag.Parse()

	// add in stdin if present
	var r io.Reader
	if stat, _ := os.Stdin.Stat(); stat != nil && stat.Mode()&os.ModeCharDevice == 0 {
		r = os.Stdin
	}

	err := func() error {
		ctx, cncl := signalCtx()
		defer cncl()
		return run(ctx, flag.Args(), r, *walk, *checkMode)
	}()

	switch {
	case errors.Is(err, errCheckMode):
		logger.Println(`msg="changes detected"`)
		os.Exit(2)

	case err != nil:
		logger.Fatalf("err=%q", err)

	case *checkMode:
		logger.Println(`msg="no changes detected"`)
	}
}

func run(ctx context.Context, fns []string, r io.Reader, walk, checkMode bool) error {
	ctx, cncl := context.WithCancel(ctx)
	defer cncl()

	fileC, errC := listFiles(ctx, fns, r, walk)

	var (
		wg               sync.WaitGroup
		listErr, procErr error
	)

	wg.Add(1)
	go func() {
		defer wg.Done()

		select {
		case listErr = <-errC:
			if listErr != nil {
				cncl()
			}
		case <-ctx.Done():
		}
	}()

	wg.Add(1)
	go func() {
		defer wg.Done()
		if procErr = processFiles(ctx, checkMode, fileC); procErr != nil {
			cncl()
		}
	}()

	wg.Wait()

	// prefer non-contextual errors for reporting if present
	var retErr error
	for _, e := range [...]error{listErr, procErr} {
		if e != nil && !errors.Is(e, context.Canceled) {
			retErr = e
			break
		}
	}
	if retErr == nil {
		retErr = ctx.Err()
	}

	// log if failed for other reasons
	if listErr != nil && errors.Unwrap(retErr) != errors.Unwrap(listErr) {
		logger.Printf(`msg="listing files failed" err=%q`, listErr)
	}
	if procErr != nil && errors.Unwrap(retErr) != errors.Unwrap(procErr) {
		logger.Printf(`msg="processing files failed" err=%q`, procErr)
	}
	return retErr
}

func signalCtx() (context.Context, context.CancelFunc) {
	ctx, cncl := context.WithCancel(context.Background())
	{
		signals := make(chan os.Signal, 1)
		signal.Notify(signals, os.Interrupt)
		go func() {
			select {
			case <-ctx.Done():
			case <-signals:
				cncl()
			}
		}()
	}
	return ctx, cncl
}

var errCheckMode = errors.New("files changed")

func processFiles(ctx context.Context, checkMode bool, fns <-chan string) error {
	tmpDir, err := ioutil.TempDir("", "pullquote")
	if err != nil {
		return fmt.Errorf("unable to open temp directory: %w", err)
	}
	defer func() {
		_ = os.RemoveAll(tmpDir)
	}()

	type result struct {
		fn     string
		tempFn string
		err    error
	}

	processCtx, processCncl := context.WithCancel(ctx)
	defer processCncl()

	var (
		resultCh = make(chan result, 1)
		inFlight int

		// err msging equipment
		otherErrs int
		errFn     string

		// moves to make
		moves [][2]string
	)

	// we eschew the need for a waitgroup here by just tracking the number in flight
	for fns != nil || inFlight > 0 {
		select {

		case <-ctx.Done():
			err = ctx.Err()
			fns = nil

		case fn, ok := <-fns:
			if !ok {
				fns = nil
				break
			}
			inFlight++
			go func(fn string) {
				tempFn, err := processFile(processCtx, tmpDir, fn)
				select {
				case resultCh <- result{fn, tempFn, err}:
				case <-processCtx.Done():
				}
			}(fn)

		case res := <-resultCh:
			inFlight--

			switch {

			case err == nil && res.err == nil: // happy path
				if res.tempFn != "" {
					moves = append(moves, [2]string{res.tempFn, res.fn})
				}

			case res.err != nil && !errors.Is(res.err, context.Canceled): // ignore canceled ctx for per-file reporting
				logger.Printf("file=%q err=%q", res.fn, res.err)
				otherErrs++
				if err == nil || res.fn < errFn {
					err, errFn = res.err, res.fn // take the "minimum" for deterministic results
				}
			}
		}
	}
	if err != nil {
		if otherErrs > 0 {
			return fmt.Errorf("%v failed (along with %v others): %w", errFn, otherErrs, err)
		}
		return fmt.Errorf("%v failed: %w", errFn, err)
	}
	if checkMode && len(moves) > 0 {
		return errCheckMode
	}
	for _, m := range moves {
		if err := os.Rename(m[0], m[1]); err != nil {
			return fmt.Errorf("os.Rename(%v, %v): %w", m[0], m[1], err)
		}
	}
	logger.Printf(`msg="processing complete" files_updated=%d`, len(moves))
	return nil
}

func listFiles(ctx context.Context, fns []string, r io.Reader, walk bool) (<-chan string, <-chan error) {
	var (
		errC   = make(chan error, 1)
		merged = make(chan string, len(fns)+1)
	)

	wd, err := os.Getwd()
	if err != nil {
		errC <- err // can't block
		close(merged)
		close(errC)
		return merged, errC
	}

	ctx, cncl := context.WithCancel(ctx)

	var (
		wg              sync.WaitGroup
		scanned, walked chan string
	)
	if r != nil {
		scanned = make(chan string)
		wg.Add(1)
		go func() {
			defer wg.Done()

			scanner := bufio.NewScanner(r)
			for scanner.Scan() {
				select {
				case scanned <- scanner.Text():
				case <-ctx.Done():
				}
			}
			if err := scanner.Err(); err != nil {
				cncl()
				select {
				case errC <- err:
				default:
				}
			}
			close(scanned)
		}()
	}

	if walk {
		walked = make(chan string)
		wg.Add(1)
		go func() {
			defer wg.Done()
			err = filepath.Walk(wd, func(path string, info os.FileInfo, err error) error {
				switch {
				case err != nil:
					return err
				case info.IsDir():
					// skip hidden dirs and conventionally excluded go dirs
					if name := info.Name(); name != "." && (strings.HasPrefix(name, ".") || name == "testdata") {
						return filepath.SkipDir
					}
					return nil
				case strings.ToLower(filepath.Ext(path)) != ".md":
					return nil
				default:
					select {
					case <-ctx.Done():
						return ctx.Err()
					case walked <- path:
						return nil
					}
				}
			})
			if err != nil {
				cncl()
				select {
				case errC <- err:
				default:
				}
			}
			close(walked)
		}()
	}

	go func() {
		defer cncl()

		seen := make(map[string]struct{})

		submit := func(path string) {
			if _, ok := seen[path]; ok {
				return // dupe
			}
			select {
			case merged <- path:
				seen[path] = struct{}{}
			case <-ctx.Done():
			}
		}

		standardize := func(path string) string {
			if !filepath.IsAbs(path) {
				path = filepath.Join(wd, path)
			}
			return filepath.Clean(path)
		}

		for _, fn := range fns {
			submit(standardize(fn))
		}

	SelectLoop:
		for scanned != nil || walked != nil {
			select {
			case <-ctx.Done():
				break SelectLoop
			case s, ok := <-scanned:
				if !ok {
					scanned = nil
					break
				}
				submit(standardize(s))
			case s, ok := <-walked:
				if !ok {
					walked = nil
					break
				}
				// no need to call submit here -- guaranted to be clean
				submit(s)
			}
		}
		wg.Wait()
		close(merged)
		close(errC)
	}()

	return merged, errC
}

var msgKey = func() interface{} { // lawl
	type ctxKey struct{}
	return ctxKey{}
}()

func addLogCtx(ctx context.Context, format string, args ...interface{}) context.Context {
	var b strings.Builder
	if msg, ok := ctx.Value(msgKey).(string); ok {
		b.WriteString(msg)
		if r, _ := utf8.DecodeLastRuneInString(msg); !unicode.IsSpace(r) { // zero len safe
			b.WriteByte(' ')
		}
	}
	_, _ = fmt.Fprintf(&b, format, args...)
	return context.WithValue(ctx, msgKey, b.String())
}

func ctxLogf(ctx context.Context, format string, args ...interface{}) {
	var b strings.Builder
	_, _ = fmt.Fprintf(&b, format, args...)

	if msg, ok := ctx.Value(msgKey).(string); ok {
		if r, _ := utf8.DecodeLastRuneInString(b.String()); !unicode.IsSpace(r) { // zero len safe
			b.WriteByte(' ')
		}
		b.WriteString(msg)
	}

	_ = logger.Output(2, b.String())
}

func processFile(ctx context.Context, tmpDir, fn string) (string, error) {
	ctx = addLogCtx(ctx, "filename=%q", fn)

	f, err := os.Open(fn)
	if err != nil {
		return "", fmt.Errorf("os.Open(%v): %w", fn, err)
	}
	defer func() {
		if cErr := f.Close(); cErr != nil && err != nil {
			err = cErr
		}
	}()

	pqs, err := readPullQuotes(ctx, f)
	if err != nil {
		return "", fmt.Errorf("readPullQuotes %v: %w", fn, err)
	}
	if debug {
		ctxLogf(ctx, "total_pullquotes=%v", len(pqs))
	}
	if len(pqs) == 0 {
		return "", nil
	}

	dir := filepath.Dir(fn)
	for _, pq := range pqs {
		if pq.src != "" {
			pq.src = filepath.Join(dir, pq.src)
		}
		if pq.objPath != "" && (strings.HasPrefix(pq.objPath, "./") || strings.Contains(pq.objPath, ".go")) {
			pq.objPath = filepath.Join(dir, pq.objPath)
		}
	}

	expanded, err := expandPullQuotes(ctx, pqs)
	if err != nil {
		return "", fmt.Errorf("expandedPullQuotes: %w", err)
	}

	o, err := ioutil.TempFile(tmpDir, "")
	if err != nil {
		return "", fmt.Errorf("unable to open tmp file: %w", err)
	}
	defer func() {
		_ = o.Close()
	}()
	if err := func() error {
		if _, err := f.Seek(0, io.SeekStart); err != nil {
			return fmt.Errorf("f.seek 0: %w", err)
		}
		w := bufio.NewWriter(o)
		if err := applyPullQuotes(pqs, expanded, f, w); err != nil {
			return fmt.Errorf("failed applying pull quotes: %w", err)
		}

		if err := w.Flush(); err != nil {
			return fmt.Errorf("couldn't flush: %w", err)
		}
		return nil
	}(); err != nil {
		return "", err
	}

	changed, err := filesChanged(f, o)
	switch {
	case err != nil:
		ctxLogf(ctx, `msg="detecting file change" err=%q`, err)
		return o.Name(), nil
	case changed:
		ctxLogf(ctx, `msg="change detected"`)
		return o.Name(), nil
	default:
		if debug {
			ctxLogf(ctx, `msg="no change detected"`)
		}
		return "", nil
	}
}

var hashPool = sync.Pool{
	New: func() interface{} {
		return sha1.New()
	},
}

func filesChanged(a, b *os.File) (bool, error) {
	hA, hB := hashPool.Get().(hash.Hash), hashPool.Get().(hash.Hash)
	defer func() {
		hashPool.Put(hA)
		hashPool.Put(hB)
	}()
	bA, err := calcHash(hA, a)
	if err != nil {
		return false, err
	}
	bB, err := calcHash(hB, b)
	if err != nil {
		return false, err
	}
	return !bytes.Equal(bA, bB), nil
}

func calcHash(h hash.Hash, f *os.File) ([]byte, error) {
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return nil, err
	}

	h.Reset()
	if _, err := io.Copy(h, f); err != nil {
		return nil, err
	}

	return h.Sum(nil), nil
}

func newlineIncludingScanner(r io.Reader) *bufio.Scanner {
	scanner := bufio.NewScanner(r)
	scanner.Split(func(data []byte, atEOF bool) (advance int, token []byte, err error) {
		if atEOF && len(data) == 0 {
			return 0, nil, nil
		}
		if i := bytes.IndexByte(data, '\n'); i >= 0 {
			// We have a full newline-terminated line.
			return i + 1, data[:i+1], nil
		}
		// If we're at EOF, we have a final, non-terminated line. Return it.
		if atEOF {
			return len(data), data, nil
		}
		// Request more data.
		return 0, nil, nil
	})
	return scanner
}

type readerAtSeeker interface {
	io.ReaderAt
	io.ReadSeeker
}

func applyPullQuotes(pqs []*pullQuote, expanded []*expanded, r readerAtSeeker, w io.Writer) (err error) {
	write := func(s string) {
		if err != nil {
			return
		}
		_, err = w.Write([]byte(s))
	}

	writeCodeFence := func(data, lang string) {
		if err != nil {
			return
		}
		format := "\n```%s\n%s\n```\n"
		if strings.HasPrefix(data, "```") || strings.Contains(data, "\n```") {
			format = "\n~~~%s\n%s\n~~~\n"
		}
		_, err = fmt.Fprintf(w, format, lang, data)
	}

	// every pq has a start offset and, optionally, and end index
	readThrough := 0
	for i, pq := range pqs {
		exp := expanded[i]

		if _, err = io.Copy(w, io.NewSectionReader(r, int64(readThrough), int64(pq.startIdx-readThrough))); err != nil {
			break
		}
		readThrough = pq.startIdx

		switch pq.fmt {
		case fmtExample:
			if len(exp.Parts) != 2 {
				writeCodeFence(exp.String, pq.lang)
				break
			}
			write("\n**Code**:")
			writeCodeFence(exp.Parts[0], pq.lang)
			write("**Output**:")
			writeCodeFence(exp.Parts[1], "")
		case fmtCodeFence:
			writeCodeFence(exp.String, pq.lang)
		case fmtBlockQuote:
			write("\n> ")
			write(strings.Replace(exp.String, "\n", "\n> ", -1) + "\n")
		default:
			write("\n" + exp.String + "\n")
		}

		if pq.endIdx == idxNoEnd { // add an end tag
			write("<!-- /" + pq.originalTag + "quote -->")
		} else {
			readThrough = pq.endIdx // skip any intervening content -- we have rewritten it
		}
	}
	if err != nil {
		return err
	}

	if _, err = r.Seek(int64(readThrough), io.SeekStart); err != nil {
		return err
	}
	_, err = io.Copy(w, r)

	return err
}

const idxNoEnd = -1

func readPullQuotes(ctx context.Context, r io.Reader) ([]*pullQuote, error) {
	var pqs []*pullQuote

	comments := htmlCommentScanner(r)
	for comments.Scan() {
		b := comments.Bytes()

		ctx := addLogCtx(ctx, "start=%v end=%v comment=%q", comments.start, comments.end, string(b))

		toks := tokenizingScanner(bytes.NewReader(b[len("<!--") : len(b)-len("-->")]))
		toks.Scan()

		var tt string
		switch t := toks.Text(); t {
		case "pullquote":
			tt = "pull"
		case "goquote":
			tt = "go"
		case "jsonquote":
			tt = "json"
		case "/pullquote", "/goquote", "/jsonquote":
			if l := len(pqs) - 1; l >= 0 && pqs[l].endIdx == idxNoEnd && strings.HasPrefix(t, "/"+pqs[l].originalTag) {
				pqs[l].endIdx = comments.start
				if debug {
					ctxLogf(ctx, `msg="found pullquote end" pq=%q`, pqs[l])
				}
				continue
			}
			return nil, fmt.Errorf("unexpected %v at offset %v: %q", t, comments.start, string(b))
		default:
			if debug {
				ctxLogf(ctx, `msg="unsupported comment tag"`)
			}
			continue
		}

		pq := pullQuote{originalTag: tt, startIdx: comments.end, endIdx: idxNoEnd}
		seen, err := setOptions(&pq, toks, tt)
		if err != nil {
			return nil, fmt.Errorf("parsing pullquote at offset %v: %w", comments.start, err)
		}
		if err := validate(&pq, seen); err != nil {
			return nil, fmt.Errorf("validating pullquote at offset %v: %w", comments.start, err)
		}
		if debug {
			ctxLogf(ctx, `msg="found pullquote" pq=%q`, &pq)
		}
		pqs = append(pqs, &pq)
	}
	if err := comments.Err(); err != nil {
		return nil, err
	}
	return pqs, nil
}

type expanded struct {
	String string
	Parts  []string
}

// doing it w/o hash maps for s&gs
func expandPullQuotes(ctx context.Context, pqs []*pullQuote) ([]*expanded, error) {
	results := make([]*expanded, len(pqs))

	var buf []*pullQuote

	for _, strategy := range []struct {
		quoteType string
		expander  func(context.Context, []*pullQuote) ([]*expanded, error)
	}{
		{"go", expandGoQuotes},
		{"json", expandJSONQuotes},
	} {
		for i, pq := range pqs {
			if results[i] != nil {
				continue
			}
			if pq.quoteType == strategy.quoteType {
				buf = append(buf, pq)
			}
		}

		if len(buf) > 0 {
			expanded, err := strategy.expander(ctx, buf)
			if err != nil {
				return nil, err
			}
			for j, cur := 0, 0; j < len(pqs) && cur < len(buf); j++ {
				if pqs[j] == buf[cur] {
					results[j] = expanded[cur]
					cur++
				}
			}
			buf = buf[:0]
		}
	}

	for i, pq := range pqs {
		if results[i] != nil {
			continue
		}

		for j := i; j < len(pqs); j++ {
			if pqs[j].src == pq.src {
				buf = append(buf, pqs[j])
			}
		}

		found, err := expandSrcPullQuotes(ctx, buf)
		if err != nil {
			return nil, err
		}

		for j, cur := i, 0; j < len(pqs); j++ {
			if pqs[j].src == pq.src {
				results[j] = found[cur]
				cur++
			}
		}

		buf = buf[:0]
	}

	return results, nil
}

func expandSrcPullQuotes(_ context.Context, pqs []*pullQuote) ([]*expanded, error) {
	f, err := os.Open(pqs[0].src)
	if err != nil {
		return nil, err
	}
	defer func() {
		_ = f.Close()
	}()

	type state struct {
		*pullQuote
		*bytes.Buffer
		result            *expanded
		endMatchRemaining int
	}

	states := make([]*state, 0, len(pqs))
	for _, pq := range pqs {
		endCountRem := 1
		if pq.endCount != 0 {
			endCountRem = pq.endCount
		}
		states = append(states, &state{pq, nil, nil, endCountRem})
	}

	{
		scanner := newlineIncludingScanner(f)
		for scanner.Scan() {
			txt := scanner.Text()
			for _, s := range states {
				if s.result != nil {
					continue
				}
				if s.Buffer == nil {
					if !s.start.MatchString(txt) {
						continue
					}
					s.Buffer = new(bytes.Buffer) // init buffer
				}
				s.Buffer.WriteString(txt)
				if s.end.MatchString(txt) {
					s.endMatchRemaining--
					if s.endMatchRemaining == 0 {
						s.result = &expanded{String: strings.TrimRight(s.Buffer.String(), "\r\n")}
						s.Buffer = nil
						continue
					}
				}
			}
		}
		if err := scanner.Err(); err != nil {
			return nil, err
		}
	}

	results := make([]*expanded, 0, len(states))
	for _, s := range states {
		if s.result != nil {
			results = append(results, s.result)
			continue
		}
		if s.Buffer != nil {
			return nil, fmt.Errorf("never matched end: %q", s.end)
		}
		return nil, fmt.Errorf("never matched start: %q", s.end)
	}

	return results, nil
}

const (
	// keyNoReformat disables realigning go tabs for the snippet
	keyNoReformat = "noreformat"

	// keyGoPath sets the path to a go expression or statement to print; can also be specified via goquote tag
	keyGoPath = "gopath"
	// keyIncludeGroup includes the whole group declaration, not just the single named statement
	keyIncludeGroup = "includegroup"

	// keyJSONPath sets the path to a JSON object to print; can also be specified via jsonquote tag
	keyJSONPath = "jsonpath"

	// keySrc specifies the file from which to take a pullquote
	keySrc = "src"
	// keyStart specifies a pattern for the line on which a pullquote begins
	keyStart = "start"
	// keyEnd specifies a pattern for the line on which a pullquote ends
	keyEnd = "end"
	// keyEndCount specifies the number of times the `end` pattern should match before ending the quote; default 1
	keyEndCount = "endcount"

	// keyFmt specifies a format -- can be `none`, `blockquote`, or `codefence`; for goquote, defaults to codefence.
	keyFmt = "fmt"
	// keyLang specifies the language highlighting to be used with a codefence.
	keyLang = "lang"

	// fmtCodeFence specifies that the snippet should be rendered within a "codefence" -- i.e. ```
	fmtCodeFence = "codefence"
	// fmtCodeFence specifies that the snippet should be rendered as a blockquote
	fmtBlockQuote = "blockquote"
	// fmtNone can be used to explicitly unset default formats
	fmtNone = "none"
	// fmtExample indicates that the code should be rendered like a godoc example
	fmtExample = "example"
)

var (
	keysCommonOptional    = [...]string{keyFmt, keyLang}
	keysGoQuoteValid      = [...]string{keyGoPath, keyNoReformat, keyIncludeGroup}
	keysJSONQuoteValid    = [...]string{keyJSONPath, keyNoReformat}
	keysPullQuoteOptional = [...]string{keyEndCount}
	keysPullQuoteRequired = [...]string{keySrc, keyStart, keyEnd}
	validFmts             = map[string]bool{
		fmtBlockQuote: true,
		fmtCodeFence:  true,
		fmtExample:    true,
		fmtNone:       true,
	}
)

type pullQuote struct {
	originalTag, quoteType string

	src        string
	start, end *regexp.Regexp
	endCount   int
	fmt, lang  string

	objPath, jsonPath string

	flags uint

	startIdx, endIdx int
}

// String returns a representation of the PQ for debugging; it is _not_ a valid serialization.
func (pq *pullQuote) String() string {
	var b strings.Builder
	_, _ = fmt.Fprintf(&b, "<!-- %vquote", pq.originalTag)

	switch pq.quoteType {
	case "go":
		if pq.originalTag == "go" {
			_, _ = fmt.Fprintf(&b, " %q", pq.objPath)
		} else {
			_, _ = fmt.Fprintf(&b, " gopath=%q", pq.objPath)
		}
	case "json":
		if pq.originalTag == "json" {
			_, _ = fmt.Fprintf(&b, " %q", pq.jsonPath)
		} else {
			_, _ = fmt.Fprintf(&b, " jsonpath=%q", pq.jsonPath)
		}
	}

	for _, t := range []struct {
		key string
		val interface{}
	}{
		{"startIdx", pq.startIdx},
		{"endIdx", pq.endIdx},
		{keySrc, pq.src},
		{keyStart, pq.start},
		{keyEnd, pq.end},
		{keyEndCount, pq.endCount},
		{keyFmt, pq.fmt},
		{keyLang, pq.lang},
		{keyIncludeGroup, pq.flags&includeGroup != 0},
		{keyNoReformat, pq.flags&noRealignTabs != 0},
	} {
		switch v := t.val.(type) {
		case bool:
			if v {
				_, _ = fmt.Fprintf(&b, " %v", t.key)
			}
			continue
		case string:
			if v != "" {
				_, _ = fmt.Fprintf(&b, " %v=%q", t.key, v)
			}
		case int:
			if v != 0 {
				_, _ = fmt.Fprintf(&b, " %v=%d", t.key, v)
			}
		case *regexp.Regexp:
			if v != nil {
				_, _ = fmt.Fprintf(&b, " %v=%q", t.key, v)
			}
		default:
			_, _ = fmt.Fprintf(&b, " %v=UNKNOWN(%v)", t.key, v)
		}
	}

	_, _ = io.WriteString(&b, " -->")

	return b.String()
}

const (
	_ = 1 << iota
	noRealignTabs
	includeGroup
)

type scanner interface {
	Scan() bool
	Text() string
	Err() error
}

func setOptions(pq *pullQuote, toks scanner, tagType string) (map[string]struct{}, error) {
	b := builder{pq: pq, seen: make(map[string]struct{})}

	// our expressions require maximum three "tokens"
	window := make([]string, 0, 3)

	switch tagType {
	case "go":
		window = append(window, keyGoPath, "=")
	case "json":
		window = append(window, keyJSONPath, "=")
	}

	for toks.Scan() && b.err == nil {
		window = append(window, toks.Text())
		switch len(window) {
		case 2:
			if window[1] != "=" { // one off key
				b.set(window[0], "", false)
				window[0] = window[1]
				window = window[:1]
			}
		case 3: // ["key", "=", "value"]
			b.set(window[0], window[2], true)
			window = window[:0]
		}
	}
	if b.err == nil {
		b.err = toks.Err()
	}
	switch len(window) { // check remainders
	case 1:
		b.set(window[0], "", false)
	case 2:
		b.set(window[0], "", false)
		b.set(window[1], "", false)
	}
	return b.seen, b.err
}

func validate(pq *pullQuote, seen map[string]struct{}) error {
	if pq.fmt != "" && !validFmts[pq.fmt] {
		return errors.New("fmt must be example, codefence, blockquote, or none")
	}

	for _, s := range keysCommonOptional {
		delete(seen, s)
	}

	if pq.quoteType == "json" {
		if pq.fmt == "" {
			pq.fmt = fmtCodeFence
			pq.lang = "json"
		}

		for _, s := range keysJSONQuoteValid {
			delete(seen, s)
		}

		delete(seen, keyJSONPath)
		if err := checkRemaining(seen); err != nil {
			return fmt.Errorf("jsonquote: %w", err)
		}
		return nil
	}

	if pq.quoteType == "go" {
		if pq.fmt == "" {
			pq.fmt = fmtCodeFence
			pq.lang = "go"
			if strings.Contains(pq.objPath, "#Example") { // likely example test
				pq.fmt = fmtExample
			}
		}

		for _, s := range keysGoQuoteValid {
			delete(seen, s)
		}

		if err := checkRemaining(seen); err != nil {
			return fmt.Errorf("goquote: %w", err)
		}
		return nil
	}

	for _, s := range keysPullQuoteOptional {
		delete(seen, s)
	}

	for _, s := range keysPullQuoteRequired {
		if _, ok := seen[s]; !ok {
			return fmt.Errorf("%q cannot be unset", s)
		}
		delete(seen, s)
	}

	if err := checkRemaining(seen); err != nil {
		return fmt.Errorf("pullquote: %w", err)
	}
	return nil
}

func checkRemaining(m map[string]struct{}) error {
	if len(m) == 0 {
		return nil
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return fmt.Errorf("invalid keys: %v", strings.Join(keys, ", "))
}

type builder struct {
	pq   *pullQuote
	err  error
	seen map[string]struct{}
}

func (b *builder) vSetTest(k string, want, got bool) bool {
	if b.err == nil && want != got {
		b.err = fmt.Errorf("%q requires value", k)
		if !want {
			b.err = fmt.Errorf("%q does not take a value", k)
		}
	}
	return b.err == nil
}

func (b *builder) set(k, v string, vSet bool) {
	if b.err != nil {
		return
	}
	if _, ok := b.seen[k]; ok {
		b.err = fmt.Errorf("key %v already seen", k)
		return
	}

	b.seen[k] = struct{}{}

	switch k {
	case keyIncludeGroup:
		b.vSetTest(keyIncludeGroup, false, vSet)
		b.pq.flags |= includeGroup
	case keyNoReformat:
		b.vSetTest(keyNoReformat, false, vSet)
		b.pq.flags |= noRealignTabs
	case keySrc:
		b.vSetTest(keySrc, true, vSet)
		b.pq.src = v
	case keyStart:
		if b.vSetTest(keyStart, true, vSet) {
			if b.pq.start, b.err = regexp.Compile(v); b.err != nil {
				b.err = fmt.Errorf("invalid start %q: %w", v, b.err)
			}
		}
	case keyEnd:
		if b.vSetTest(keyEnd, true, vSet) {
			if b.pq.end, b.err = regexp.Compile(v); b.err != nil {
				b.err = fmt.Errorf("invalid end %q: %w", v, b.err)
			}
		}
	case keyEndCount:
		if b.vSetTest(keyEndCount, true, vSet) {
			if b.pq.endCount, b.err = strconv.Atoi(v); b.err != nil {
				b.err = fmt.Errorf("invalid endcount %q: %w", v, b.err)
			}
		}
	case keyFmt:
		b.pq.fmt = v
	case keyLang:
		b.pq.lang = v
	case keyGoPath:
		b.pq.objPath = v
		b.pq.quoteType = "go"
	case keyJSONPath:
		b.pq.objPath = v
		b.pq.quoteType = "json"
	default:
		if vSet {
			b.err = fmt.Errorf("unknown key %q with value %q", k, v)
			break
		}
		b.err = fmt.Errorf("unknown key %q", k)
	}
}

var errTokUnterminated = errors.New("unterminated token")

type trackingScanner struct {
	*bufio.Scanner
	start int
	end   int
}

func tokenizingScanner(r io.Reader) *trackingScanner {
	unescape := func(buf []byte) []byte {
		var (
			cur     int
			escaped bool
			quote   rune
		)
		for i, width := 0, 0; i < len(buf); i += width {
			var r rune
			r, width = utf8.DecodeRune(buf[i:])
			if !escaped {
				switch {
				case r == '\\':
					escaped = true
					continue
				case quote != 0:
					if r == quote {
						quote = 0
						continue
					}
				case r == '\'' || r == '"':
					quote = r
					continue
				}
			}
			escaped = false
			copy(buf[cur:cur+width], buf[i:i+width])
			cur += width
		}
		return buf[:cur]
	}

	var toks trackingScanner
	toks.Scanner = bufio.NewScanner(r)
	toks.Scanner.Split(func(data []byte, atEOF bool) (advance int, _ []byte, _ error) {
		defer func() { toks.end += advance }()

		// Skip leading spaces.
		start := 0
		for width := 0; start < len(data); start += width {
			var r rune
			r, width = utf8.DecodeRune(data[start:])
			if !unicode.IsSpace(r) {
				break
			}
		}

		toks.start = toks.end + start

		var (
			quote   rune
			escaped bool
		)
		// Scan until unquoted space or equals, marking end of word.
		for width, i := 0, start; i < len(data); i += width {
			var r rune
			r, width = utf8.DecodeRune(data[i:])
			switch {
			case escaped:
				escaped = false

			case r == '\\':
				escaped = true

			case quote != 0:
				if r == quote {
					quote = 0
				}

			case r == '\'' || r == '"':
				quote = r

			case r == '=':
				if i == start { // just `=`
					return i + width, data[start : i+width], nil
				}
				return i, unescape(data[start:i]), nil

			case unicode.IsSpace(r):
				return i, unescape(data[start:i]), nil
			}
		}
		if atEOF && len(data) > start {
			if quote != 0 || escaped {
				return len(data), data[start:], errTokUnterminated
			}
			return len(data), unescape(data[start:]), nil
		}
		// Request more data.
		return start, nil, nil
	})
	return &toks
}

func htmlCommentScanner(r io.Reader) *trackingScanner {
	detectCodeFence := func(data []byte) (int, int) {
		tickStart := bytes.Index(data, []byte("\n```"))
		tildeStart := bytes.Index(data, []byte("\n~~~"))

		var (
			start int
			delim []byte
		)
		switch {
		case tickStart == -1 && tildeStart == -1:
			return -1, -1
		case tildeStart == -1:
			start, delim = tickStart, []byte("\n```")
		case tickStart == -1, tildeStart < tickStart:
			start, delim = tildeStart, []byte("\n~~~")
		default:
			start, delim = tickStart, []byte("\n```")
		}

		end := bytes.Index(data[start+len(delim):], delim)
		if end == -1 {
			return start, -1
		}
		return start, start + end + len(delim)*2
	}

	detectComment := func(data []byte) (int, int) {
		if end := bytes.Index(data, []byte("-->")); end > 0 {
			start := bytes.LastIndex(data[:end], []byte("<!--"))
			return start, end + len("-->")
		}
		return bytes.Index(data, []byte("<!--")), -1
	}

	var html trackingScanner
	html.Scanner = bufio.NewScanner(r)
	html.Split(func(data []byte, atEOF bool) (advance int, token []byte, err error) {
		defer func() { html.end += advance }()

		// all indices, slices should be interpreted relative to i
		i := 0

		cfStart, cfEnd := detectCodeFence(data[i:]) // should always be interpreted relative to i
		for cfStart != -1 && cfEnd != -1 {          // complete codefence in front of us; let's process it
			if cmStart, cmEnd := detectComment(data[i : i+cfStart]); cmStart != -1 && cmEnd != -1 {
				html.start = html.end + cmStart + i
				return i + cmEnd, data[i+cmStart : i+cmEnd], nil
			}
			// jump past this codefence and continue
			i += cfEnd
			cfStart, cfEnd = detectCodeFence(data[i:])
		}

		{
			searchRange := data[i:]
			if cfStart != -1 {
				searchRange = data[i : i+cfStart]
			}

			if cmStart, cmEnd := detectComment(searchRange); cmStart != -1 && cmEnd != -1 {
				html.start = html.end + cmStart + i
				return cmEnd + i, data[cmStart+i : cmEnd+i], nil
			}
		}

		if cfStart == -1 { // no codefence ahead but still couldn't find comment -- jump to end of data
			return len(data), nil, nil
		}

		// codefence start ahead with no comment intervening, but no end present -- jump to start of codefence
		return i + cfStart, nil, nil
	})
	return &html
}
