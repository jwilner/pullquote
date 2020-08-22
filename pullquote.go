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
	"unicode"
)

func main() {
	ctx, cncl := context.WithCancel(context.Background())
	defer cncl()

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

	if err := run(ctx, os.Args[1:]); err != nil {
		log.Fatal(err)
	}
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
			return err
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

	expanded, err := expandPullQuotes(fn, pqs)
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

func applyPullQuotes(pqs []*pullQuote, expanded []string, r io.Reader, w io.Writer) error {
	writeWithNewLine := func(l []byte) error {
		if _, err := w.Write(l); err != nil {
			return err
		}
		_, err := w.Write([]byte{'\n'})
		return err
	}

	scanner := bufio.NewScanner(r)
	for i := 0; scanner.Scan(); i++ {
		switch {
		case len(pqs) == 0, i <= pqs[0].startIdx:
			if err := writeWithNewLine(scanner.Bytes()); err != nil {
				return err
			}

		case i == pqs[0].endIdx:
			if pqs[0].codeFenceEnabled {
				if err := writeWithNewLine(append([]byte{'`', '`', '`'}, []byte(pqs[0].codeFenceLang)...)); err != nil {
					return err
				}
			}
			if err := writeWithNewLine([]byte(expanded[0])); err != nil {
				return err
			}
			if pqs[0].codeFenceEnabled {
				if err := writeWithNewLine([]byte{'`', '`', '`'}); err != nil {
					return err
				}
			}
			if err := writeWithNewLine(scanner.Bytes()); err != nil {
				return err
			}
			pqs = pqs[1:]
			expanded = expanded[1:]
		}
	}
	return scanner.Err()
}

func readPullQuotes(r io.Reader) ([]*pullQuote, error) {
	var (
		patterns []*pullQuote
		current  *pullQuote
		scanner  = bufio.NewScanner(r)
		i        int
	)
	for ; scanner.Scan(); i++ {
		if current != nil {
			if regexpWrapperEnd.MatchString(scanner.Text()) {
				current.endIdx = i
				patterns = append(patterns, current)
				current = nil
			}
			continue
		}

		var err error
		if current, err = parseLine(scanner.Text()); err != nil {
			return nil, fmt.Errorf("parsing line %v: %w", i+1, err)
		}
		if current != nil {
			if err = validate(current); err != nil {
				return nil, fmt.Errorf("invalid pull quote on line %v: %w", i+1, err)
			}
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

func expandPullQuotes(fn string, pqs []*pullQuote) ([]string, error) {
	results := make([]string, len(pqs))

	var buf []*pullQuote
	for i, pq := range pqs {
		if results[i] != "" {
			continue
		}

		for j := i; j < len(pqs); j++ {
			if pqs[j].src == pq.src {
				buf = append(buf, pqs[j])
			}
		}

		found, err := expandGroupedPullQuotes(fn, buf)
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

func expandGroupedPullQuotes(fn string, pqs []*pullQuote) ([]string, error) {
	f, err := os.Open(filepath.Join(filepath.Dir(fn), pqs[0].src))
	if err != nil {
		return nil, err
	}
	defer func() {
		_ = f.Close()
	}()

	type state struct {
		*pullQuote
		*bytes.Buffer
		result string
	}

	states := make([]*state, 0, len(pqs))
	for _, pq := range pqs {
		states = append(states, &state{pq, nil, ""})
	}

	{
		scanner := bufio.NewScanner(f)
		for scanner.Scan() {
			txt := scanner.Text()
			for _, s := range states {
				if s.result != "" {
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
					s.result = s.Buffer.String()
					s.Buffer = nil
					continue
				}
				s.Buffer.WriteByte('\n')
			}
		}
		if err := scanner.Err(); err != nil {
			return nil, err
		}
	}

	results := make([]string, 0, len(states))
	for _, s := range states {
		if s.result != "" {
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
	regexpWrapper    = regexp.MustCompile(`^\s*<!--\s*pullquote\s*(.*?)\s*-->\s*$`)
	regexpWrapperEnd = regexp.MustCompile(`^\s*<!--\s*/pullquote\s*-->\s*$`)
)

func parseLine(line string) (*pullQuote, error) {
	groups := regexpWrapper.FindStringSubmatch(line)
	if len(groups) != 2 {
		return nil, nil
	}

	var (
		options          = []rune(groups[1])
		keyIdxs, valIdxs = [2]int{-1, -1}, [2]int{-1, -1}
		escaped          bool
		pat              pullQuote
	)

	for i, r := range options {
		switch {
		case keyIdxs[0] == -1:
			if unicode.IsLetter(r) {
				keyIdxs[0] = i
			}

		case keyIdxs[1] == -1:
			if r == '=' {
				keyIdxs[1] = i
			}

		case valIdxs[0] == -1:
			valIdxs[0] = i

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
			} else if i == len(options)-1 {
				valIdxs[1] = len(options)
			} else if !unicode.IsSpace(r) {
				continue
			} else {
				valIdxs[1] = i
			}

			key := string(options[keyIdxs[0]:keyIdxs[1]])
			val := string(options[valIdxs[0]:valIdxs[1]])
			switch key {
			case "src":
				pat.src = val
			case "start":
				var err error
				if pat.start, err = regexp.Compile(val); err != nil {
					return nil, fmt.Errorf("invalid start %q: %w", val, err)
				}
			case "end":
				var err error
				if pat.end, err = regexp.Compile(val); err != nil {
					return nil, fmt.Errorf("invalid end %q: %w", val, err)
				}
			case "codefence":
				pat.codeFenceLang = val
				pat.codeFenceEnabled = true
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
	return &pat, nil
}

func validate(pq *pullQuote) error {
	switch {
	case pq.src == "":
		return errors.New("src cannot be unset")
	case pq.start == nil:
		return errors.New("start cannot be unset")
	case pq.end == nil:
		return errors.New("end cannot be unset")
	default:
		return nil
	}
}

type pullQuote struct {
	src              string
	start, end       *regexp.Regexp
	startIdx, endIdx int
	codeFenceLang    string
	codeFenceEnabled bool
}
