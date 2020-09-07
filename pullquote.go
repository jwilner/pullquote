package main

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
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
	"unicode"
	"unicode/utf8"
)

func main() {
	fns := make([]string, len(os.Args)-1)
	copy(fns, os.Args[1:])

	// add in stdin if present
	if stat, _ := os.Stdin.Stat(); stat != nil && stat.Mode()&os.ModeCharDevice == 0 {
		scanner := bufio.NewScanner(os.Stdin)
		for scanner.Scan() {
			fns = append(fns, scanner.Text())
		}
		if err := scanner.Err(); err != nil {
			log.Fatal(err)
		}
	}

	ctx, cncl := signalCtx()
	defer cncl()
	if err := run(ctx, fns); err != nil {
		cncl()
		log.Fatal(err)
	}
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

func run(ctx context.Context, fns []string) error {
	if len(fns) == 0 {
		return errors.New("no files provided")
	}

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

	var resultCh = make(chan result, len(fns))
	for _, fn := range fns {
		go func(fn string) {
			tempFn, err := processFile(ctx, tmpDir, fn)
			resultCh <- result{fn, tempFn, err}
		}(fn)
	}

	var (
		errFn  string
		others int
		moves  [][2]string
	)
	for i, res := len(fns)-1, <-resultCh; i >= 0; i-- {
		if err != nil {
			if res.err != nil {
				others++
			}
			continue
		}
		if res.err != nil {
			err = res.err
			errFn = res.fn
			continue
		}
		if res.tempFn == "" {
			continue
		}
		moves = append(moves, [2]string{res.tempFn, res.fn})
	}
	if err != nil {
		if others > 0 {
			return fmt.Errorf("%v failed (along with %v others): %w", errFn, others, err)
		}
		return fmt.Errorf("%v failed: %w", errFn, err)
	}
	for _, m := range moves {
		if err := os.Rename(m[0], m[1]); err != nil {
			return fmt.Errorf("os.Rename(%v, %v): %w", m[0], m[1], err)
		}
	}
	return nil
}

func processFile(ctx context.Context, tmpDir, fn string) (string, error) {
	f, err := os.Open(fn)
	if err != nil {
		return "", fmt.Errorf("os.Open(%v): %w", fn, err)
	}
	defer func() {
		if cErr := f.Close(); cErr != nil && err != nil {
			err = cErr
		}
	}()

	pqs, err := readPullQuotes(f)
	if err != nil {
		return "", fmt.Errorf("readPullQuotes %v: %w", fn, err)
	}
	if len(pqs) == 0 {
		return "", nil
	}

	dir := filepath.Dir(fn)
	for _, pq := range pqs {
		if pq.src != "" {
			pq.src = filepath.Join(dir, pq.src)
		}
		if pq.goPath != "" && (strings.HasPrefix(pq.goPath, "./") || strings.Contains(pq.goPath, ".go")) {
			pq.goPath = filepath.Join(dir, pq.goPath)
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

	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return ",", fmt.Errorf("f.seek 0: %w", err)
	}

	w := bufio.NewWriter(o)
	if err := applyPullQuotes(pqs, expanded, f, w); err != nil {
		return "", fmt.Errorf("failed applying pull quotes: %w", err)
	}
	if err := w.Flush(); err != nil {
		return "", fmt.Errorf("couldn't flush: %w", err)
	}

	return o.Name(), nil
}

type applier struct {
	w   io.Writer
	err error
}

func (a *applier) write(l []byte) {
	if a.err != nil {
		return
	}
	_, a.err = a.w.Write(l)
}

func (a *applier) writeWithNewLine(l []byte) {
	a.write(l)
	a.write([]byte{'\n'})
}

func (a *applier) writeCodeFence(data []byte, lang string) {
	codeFenceLiteral := []byte("```")
	if bytes.HasPrefix(data, codeFenceLiteral) || bytes.Contains(data, []byte("\n```")) {
		codeFenceLiteral = []byte("~~~")
	}

	a.writeWithNewLine(append(codeFenceLiteral, []byte(lang)...))
	a.writeWithNewLine(data)
	a.writeWithNewLine(codeFenceLiteral)
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

func applyPullQuotes(pqs []*pullQuote, expanded []*expanded, r io.Reader, w io.Writer) error {
	applier := applier{w, nil}
	scanner := newlineIncludingScanner(r)
	for i := 0; scanner.Scan() && applier.err == nil; i++ {
		var pq *pullQuote
		if len(pqs) > 0 {
			pq = pqs[0]
		}

		switch {
		case pq == nil || i < pq.startIdx:
			applier.write(scanner.Bytes())

		case i == pq.startIdx:
			applier.write(scanner.Bytes())

			exp := expanded[0]

			switch pq.fmt {
			case fmtExample:
				if len(exp.Parts) != 2 {
					// we couldn't parse the example -- treat it like a standard codefence
					applier.writeCodeFence([]byte(exp.String), pq.lang)
					break
				}
				applier.writeWithNewLine([]byte("Code:"))
				applier.writeCodeFence([]byte(exp.Parts[0]), pq.lang)
				applier.writeWithNewLine([]byte("Output:"))
				applier.writeCodeFence([]byte(exp.Parts[1]), "")
			case fmtCodeFence:
				applier.writeCodeFence([]byte(exp.String), pq.lang)
			case fmtBlockQuote:
				applier.write([]byte{'>', ' '})
				applier.writeWithNewLine([]byte(strings.Replace(exp.String, "\n", "\n> ", -1)))
			default: // include fmtNone
				applier.writeWithNewLine([]byte(exp.String))
			}

			if pq.endIdx == idxNoEnd {
				// add in the end tag
				applier.writeWithNewLine([]byte(fmt.Sprintf("<!-- /%vquote -->", pq.tagType)))

				pqs = pqs[1:]
				expanded = expanded[1:]
			}

		case i == pq.endIdx:
			applier.write(scanner.Bytes())

			pqs = pqs[1:]
			expanded = expanded[1:]
		}
	}
	if applier.err != nil {
		return applier.err
	}
	return scanner.Err()
}

const idxNoEnd = -1

func readPullQuotes(r io.Reader) ([]*pullQuote, error) {
	var (
		patterns  []*pullQuote
		current   *pullQuote
		scanner   = bufio.NewScanner(r)
		i         int
		codefence string
	)
	for ; scanner.Scan(); i++ {
		line := scanner.Text()

		if codefence != "" {
			if strings.HasPrefix(line, codefence) {
				codefence = ""
			}
			continue
		}

		if strings.HasPrefix(line, "```") || strings.HasPrefix(line, "~~~") {
			codefence = line[:3]
			continue
		}

		if current != nil {
			if match := regexpWrapperEnd.FindStringSubmatch(line); len(match) == 2 {
				if match[1] != current.tagType {
					return nil, fmt.Errorf("wanted %vquote end but got %vquote end", current.tagType, match[1])
				}

				current.endIdx = i
				patterns = append(patterns, current)
				current = nil
				continue
			}
		}

		next, err := parseLine(line)
		if err != nil {
			return nil, fmt.Errorf("parsing line %v: %w", i+1, err)
		}
		if next == nil {
			continue
		}
		next.startIdx = i
		if current != nil {
			current.endIdx = idxNoEnd
			patterns = append(patterns, current)
		}
		current = next
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("scanning failed after %v lines(s): %w", i+1, err)
	}
	if current != nil {
		current.endIdx = idxNoEnd
		patterns = append(patterns, current)
	}
	return patterns, nil
}

type expanded struct {
	String string
	Parts  []string
}

func expandPullQuotes(ctx context.Context, pqs []*pullQuote) ([]*expanded, error) {
	results := make([]*expanded, len(pqs))

	var buf []*pullQuote

	// handle go quotes first
	for _, pq := range pqs {
		if pq.goPath != "" {
			buf = append(buf, pq)
		}
	}
	if len(buf) > 0 {
		expanded, err := expandGoQuotes(ctx, buf)
		if err != nil {
			return nil, err
		}
		for j, cur := 0, 0; j < len(pqs); j++ {
			if pqs[j] == buf[cur] {
				results[j] = expanded[cur]
				cur++
			}
		}
		buf = buf[:0]
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

		found, err := expandSrcPullQuotes(buf)
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

func expandSrcPullQuotes(pqs []*pullQuote) ([]*expanded, error) {
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

var (
	regexpWrapper    = regexp.MustCompile(`^\s*<!--\s*(pull|go)quote\s*(.*?)\s*-->\s*$`)
	regexpWrapperEnd = regexp.MustCompile(`^\s*<!--\s*/(pull|go)quote\s*-->\s*$`)
)

const (
	// keyGoPath sets the path to a go expression or statement to print; can also be specified via goquote tag
	keyGoPath = "gopath"
	// keyNoRealign disables realigning go tabs for the snippet
	keyNoRealign = "norealign"
	// keyIncludeGroup includes the whole group declaration, not just the single named statement
	keyIncludeGroup = "includegroup"

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
	keysGoquoteValid      = [...]string{keyGoPath, keyNoRealign, keyIncludeGroup}
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
	src        string
	start, end *regexp.Regexp
	endCount   int
	fmt, lang  string

	goPath       string
	goPrintFlags goPrintFlag

	startIdx, endIdx int

	tagType string
}

type goPrintFlag uint

const (
	_ goPrintFlag = 1 << iota
	noRealignTabs
	includeGroup
)

func setOptions(pq *pullQuote, options string) (map[string]struct{}, error) {
	b := builder{pq: pq, seen: make(map[string]struct{})}

	// our expressions require maximum three "tokens"
	window := make([]string, 0, 3)

	if pq.tagType == "go" { // goquote tagtype is equivalent to an initial `gopath=`
		window = append(window, keyGoPath, "=")
	}

	toks := tokenizingScanner(strings.NewReader(options))
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

func parseLine(line string) (*pullQuote, error) {
	groups := regexpWrapper.FindStringSubmatch(line)
	if len(groups) != 3 {
		return nil, nil
	}

	pq := pullQuote{tagType: groups[1]}

	seen, err := setOptions(&pq, groups[2])
	if err != nil {
		return nil, err
	}

	return &pq, validate(&pq, seen)
}

func validate(pq *pullQuote, seen map[string]struct{}) error {
	if pq.fmt != "" && !validFmts[pq.fmt] {
		return errors.New("fmt must be codefence, blockquote, or none")
	}

	for _, s := range keysCommonOptional {
		delete(seen, s)
	}

	if pq.goPath != "" {
		if pq.fmt == "" {
			pq.fmt = fmtCodeFence
			pq.lang = "go"
			if strings.Contains(pq.goPath, "#Example") {
				pq.fmt = fmtExample
			}
		}

		for _, s := range keysGoquoteValid {
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
		b.pq.goPrintFlags |= includeGroup
	case keyNoRealign:
		b.vSetTest(keyNoRealign, false, vSet)
		b.pq.goPrintFlags |= noRealignTabs
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
		b.pq.goPath = v
	default:
		if vSet {
			b.err = fmt.Errorf("unknown key %q with value %q", k, v)
			break
		}
		b.err = fmt.Errorf("unknown key %q", k)
	}
}

var errTokUnterminated = errors.New("unterminated token")

func tokenizingScanner(r io.Reader) *bufio.Scanner {
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

	s := bufio.NewScanner(r)
	s.Split(func(data []byte, atEOF bool) (int, []byte, error) {
		// Skip leading spaces.
		start := 0
		for width := 0; start < len(data); start += width {
			var r rune
			r, width = utf8.DecodeRune(data[start:])
			if !unicode.IsSpace(r) {
				break
			}
		}
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
				return i + width, unescape(data[start:i]), nil
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
	return s
}
