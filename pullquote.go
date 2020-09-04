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

	if err := applyPullQuotes(pqs, expanded, f, o); err != nil {
		return "", fmt.Errorf("failed applying pull quotes: %w", err)
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

func applyPullQuotes(pqs []*pullQuote, expanded []*expanded, r io.Reader, w io.Writer) error {
	applier := applier{w, nil}
	scanner := bufio.NewScanner(r)
	for i := 0; scanner.Scan() && applier.err == nil; i++ {
		var pq *pullQuote
		if len(pqs) > 0 {
			pq = pqs[0]
		}

		switch {
		case pq == nil || i <= pq.startIdx:
			applier.writeWithNewLine(scanner.Bytes())

		case i == pq.endIdx:
			exp := expanded[0]

			switch pq.fmt {
			case fmtExample:
				if len(exp.Parts) == 2 {
					applier.writeWithNewLine([]byte("Code:"))
					applier.writeCodeFence([]byte(exp.Parts[0]), pq.lang)
					applier.writeWithNewLine([]byte("Output:"))
					applier.writeCodeFence([]byte(exp.Parts[1]), "")
					break
				}
				// we couldn't parse the example -- treat it like a standard codefence
				applier.writeCodeFence([]byte(exp.String), pq.lang)
			case fmtCodeFence:
				applier.writeCodeFence([]byte(exp.String), pq.lang)
			case fmtBlockQuote:
				applier.write([]byte{'>', ' '})
				applier.writeWithNewLine([]byte(strings.Replace(exp.String, "\n", "\n> ", -1)))
			default: // include fmtNone
				applier.writeWithNewLine([]byte(exp.String))
			}
			applier.writeWithNewLine(scanner.Bytes())

			pqs = pqs[1:]
			expanded = expanded[1:]
		}
	}
	if applier.err != nil {
		return applier.err
	}
	return scanner.Err()
}

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
			}
			continue
		}

		var err error
		if current, err = parseLine(line); err != nil {
			return nil, fmt.Errorf("parsing line %v: %w", i+1, err)
		}
		if current != nil {
			current.startIdx = i
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("scanning failed after %v lines(s): %w", i+1, err)
	}
	if current != nil {
		return nil, fmt.Errorf("unfinished pullquote begun on line %d", current.startIdx+1)
	}
	return patterns, nil
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
		scanner := bufio.NewScanner(f)
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
						s.result = &expanded{String: s.Buffer.String()}
						s.Buffer = nil
						continue
					}
				}
				s.Buffer.WriteByte('\n')
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

func parseLine(line string) (*pullQuote, error) {
	groups := regexpWrapper.FindStringSubmatch(line)
	if len(groups) != 3 {
		return nil, nil
	}

	var (
		pq               = pullQuote{tagType: groups[1]}
		options          = []rune(groups[2])
		keyIdxs, valIdxs = [2]int{-1, -1}, [2]int{-1, -1}
		escaped          bool
		seen             = make(map[string]struct{})
	)

	const skipKey = -2
	if pq.tagType == "go" {
		keyIdxs = [2]int{skipKey, skipKey}
	}

	for i, r := range options {
		last := i == len(options)-1

		switch {
		case keyIdxs[0] == -1:
			if unicode.IsLetter(r) {
				keyIdxs[0] = i
			}
			if !last {
				continue
			}
			// both start and end
			fallthrough

		case keyIdxs[1] == -1:
			if r == '=' {
				keyIdxs[1] = i
				continue
			}
			if unicode.IsSpace(r) {
				keyIdxs[1] = i
			} else if last {
				keyIdxs[1] = len(options)
			} else {
				continue
			}

			// valueless key
			key := string(options[keyIdxs[0]:keyIdxs[1]])
			if _, ok := seen[key]; ok {
				return nil, fmt.Errorf("%v provided more than once", key)
			}

			switch key {
			case keyNoRealign:
				seen[key] = struct{}{}
				pq.goPrintFlags |= noRealignTabs
				keyIdxs = [2]int{-1, -1}
			case keyIncludeGroup:
				seen[key] = struct{}{}
				pq.goPrintFlags |= includeGroup
				keyIdxs = [2]int{-1, -1}
			}

		case valIdxs[0] == -1:
			valIdxs[0] = i
			if !last {
				continue
			}
			// both start and end
			fallthrough
		case valIdxs[1] == -1:
			if options[valIdxs[0]] == '"' {
				if r == '\\' {
					escaped = !escaped
					continue
				}
				if r != '"' || escaped {
					escaped = false
					continue
				}
				valIdxs[0]++

				// remove escaping in the current buffer
				var (
					cur    = valIdxs[0]
					curEsc bool
				)
				for j := valIdxs[0]; j < i; j++ {
					if options[j] == '\\' && !curEsc {
						curEsc = true
						continue
					}
					options[cur] = options[j]
					cur++
					curEsc = false
				}
				valIdxs[1] = cur
			} else if last {
				valIdxs[1] = len(options)
			} else if !unicode.IsSpace(r) {
				continue
			} else {
				valIdxs[1] = i
			}

			var key string
			if keyIdxs[0] == skipKey {
				key = keyGoPath
			} else {
				key = string(options[keyIdxs[0]:keyIdxs[1]])
			}

			if _, ok := seen[key]; ok {
				return nil, fmt.Errorf("%v provided more than once", key)
			}
			seen[key] = struct{}{}

			val := string(options[valIdxs[0]:valIdxs[1]])
			switch key {
			case keySrc:
				pq.src = val
			case keyStart:
				var err error
				if pq.start, err = regexp.Compile(val); err != nil {
					return nil, fmt.Errorf("invalid start %q: %w", val, err)
				}
			case keyEnd:
				var err error
				if pq.end, err = regexp.Compile(val); err != nil {
					return nil, fmt.Errorf("invalid end %q: %w", val, err)
				}
			case keyEndCount:
				var err error
				if pq.endCount, err = strconv.Atoi(val); err != nil {
					return nil, fmt.Errorf("invalid endcount %q: %w", val, err)
				}
			case keyFmt:
				pq.fmt = val
			case keyLang:
				pq.lang = val
			case keyGoPath:
				pq.goPath = val
			default:
				return nil, fmt.Errorf("unknown key %q with value %q", key, val)
			}

			keyIdxs[0], keyIdxs[1] = -1, -1
			valIdxs[0], valIdxs[1] = -1, -1
			escaped = false
		}
	}

	if escaped {
		return nil, errors.New("unclosed escape expression")
	}
	if valIdxs[0] != -1 {
		return nil, fmt.Errorf("unclosed value expression: %q", string(options[valIdxs[0]:]))
	}
	if keyIdxs[1] != -1 {
		return nil, fmt.Errorf("no value given for %q", string(options[keyIdxs[0]:keyIdxs[1]]))
	}
	if keyIdxs[0] != -1 {
		return nil, fmt.Errorf("unclosed key expression: %q", string(options))
	}

	if pq.fmt != "" && !validFmts[pq.fmt] {
		return nil, errors.New("fmt must be codefence, blockquote, or none")
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

		if len(seen) > 0 {
			return nil, fmt.Errorf("invalid keys for goquote: %v", strings.Join(sortedKeys(seen), ", "))
		}

		return &pq, nil
	}

	for _, s := range keysPullQuoteOptional {
		delete(seen, s)
	}

	for _, s := range keysPullQuoteRequired {
		if _, ok := seen[s]; !ok {
			return nil, fmt.Errorf("%q cannot be unset", s)
		}
		delete(seen, s)
	}

	if len(seen) > 0 {
		return nil, fmt.Errorf("invalid keys for pullquote: %v", strings.Join(sortedKeys(seen), ", "))
	}

	return &pq, nil
}

func sortedKeys(m map[string]struct{}) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

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

type expanded struct {
	String string
	Parts  []string
}
