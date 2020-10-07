package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
)

func expandJSONQuotes(_ context.Context, pqs []*pullQuote) ([]*expanded, error) {
	exp := make([]*expanded, 0, len(pqs))
	for _, pq := range pqs {
		parts := strings.SplitN(pq.objPath, "#", 2)
		pat, sym := parts[0], parts[1]

		s, err := func() (string, error) {
			f, err := os.Open(pat)
			if err != nil {
				return "", err
			}
			return parse(f, sym, pq.flags&noRealignTabs != 0)
		}()
		if err != nil {
			return nil, err
		}
		exp = append(exp, &expanded{String: s})
	}
	return exp, nil
}

func parse(r io.Reader, jsonPath string, noRealign bool) (string, error) {
	parts := strings.Split(jsonPath, "/")
	if len(parts) != 0 && parts[0] == "" {
		parts = parts[1:]
	}

	for dec := json.NewDecoder(r); ; {
		if len(parts) == 0 {
			var val json.RawMessage
			if err := dec.Decode(&val); err != nil {
				return "", fmt.Errorf("dec.Decode: %w", err)
			}
			if noRealign {
				return string(val), nil
			}
			var buf bytes.Buffer
			if err := json.Indent(&buf, val, "", "  "); err != nil {
				return "", err
			}
			return buf.String(), nil
		}

		tok, err := dec.Token()
		if err != nil {
			return "", fmt.Errorf("dec.Token: %w", err)
		}

		switch tok {
		case json.Delim('{'):
			err := func() error {
				for dec.More() {
					var (
						key  json.Token
						keyS string
						ok   bool
					)
					if key, err = dec.Token(); err != nil {
						return fmt.Errorf("parse key: %w", err)
					}
					if keyS, ok = key.(string); !ok {
						return fmt.Errorf("expected string, got %v", key)
					}
					if parts[0] == keyS {
						parts = parts[1:]
						return nil
					}
					// discard value
					var j json.RawMessage
					if err = dec.Decode(&j); err != nil {
						return fmt.Errorf("parse value: %w", err)
					}
				}
				return errors.New("no such value")
			}()
			if err != nil {
				return "", fmt.Errorf("couldn't parse obj: %w", err)
			}
		case json.Delim('['):
			idx, err := strconv.Atoi(parts[0])
			if err != nil {
				return "", err
			}

			err = func() error {
				for i := 0; i <= idx && dec.More(); i++ {
					if i == idx {
						parts = parts[1:]
						return nil
					}
					// discard value
					var j json.RawMessage
					_ = dec.Decode(&j)
				}
				return errors.New("no such value")
			}()
			if err != nil {
				return "", fmt.Errorf("couldn't parse array: %w", err)
			}
		}
	}
}
