package main

import (
	"context"
	"io"
	"io/ioutil"
	"os"
	"path"
	"regexp"
	"strings"
	"testing"
)

var reg = regexp.MustCompile

func Test_run(t *testing.T) {
	d := changeTmpDir(t)
	defer d.Close()

	for _, c := range []struct {
		name     string
		files    [][2]string
		fns      []string
		expected []string
		errS     string
	}{
		{
			"inserts",
			[][2]string{
				{
					"my/path.go",
					`
hello
<!-- pullquote src=local.go start="func fooBar\\(\\) {" end="}" -->
<!-- /pullquote -->
bye
`,
				},
				{
					"my/local.go",
					`
func fooBar() {
	// OK COOL
}
`,
				},
			},
			[]string{"my/path.go"},
			[]string{
				`
hello
<!-- pullquote src=local.go start="func fooBar\\(\\) {" end="}" -->
func fooBar() {
	// OK COOL
}
<!-- /pullquote -->
bye
`,
			},
			"",
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			for _, f := range c.files {
				writeFile(t, f[0], f[1])
			}
			var errS string
			if err := run(context.Background(), c.fns); err != nil {
				errS = err.Error()
			}
			if errS != c.errS {
				t.Fatalf("Expected %q but got %q", c.errS, errS)
			} else if errS != "" {
				return
			}

			for i, fn := range c.fns {
				b, err := ioutil.ReadFile(fn)
				if err != nil {
					t.Fatalf("failed reading %d %v: %v", i, fn, err)
				}
				if s := string(b); s != c.expected[i] {
					t.Fatalf("wanted %q but got %q", c.expected[i], s)
				}
			}
		})
	}
}

func Test_processFile(t *testing.T) {
	d := changeTmpDir(t)
	defer d.Close()

	for _, c := range []struct {
		name                 string
		files                [][2]string
		input, expected, err string
	}{
		{
			"inserts",
			[][2]string{
				{
					"my/path.go",
					`
hello
<!-- pullquote src=local.go start="func fooBar\\(\\) {" end="}" -->
<!-- /pullquote -->
bye
`,
				},
				{
					"my/local.go",
					`
func fooBar() {
	// OK COOL
}
`,
				},
			},
			"my/path.go",
			`
hello
<!-- pullquote src=local.go start="func fooBar\\(\\) {" end="}" -->
func fooBar() {
	// OK COOL
}
<!-- /pullquote -->
bye
`,
			"",
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			for _, f := range c.files {
				writeFile(t, f[0], f[1])
			}

			s, err := processFile(context.Background(), d.tmpDir, c.input)
			var errS string
			if err != nil {
				errS = err.Error()
			}
			if errS != c.err {
				t.Fatalf("wanted %q but got %q", c.err, errS)
			} else if err != nil {
				return
			}

			b, err := ioutil.ReadFile(s)
			if err != nil {
				t.Fatal(err)
			}
			got := string(b)
			if got != c.expected {
				t.Fatalf("wanted %q but got %q", c.expected, got)
			}
		})
	}
}

func Test_parseLine(t *testing.T) {
	for _, c := range []struct {
		name, line string
		pq         *pullQuote
		err        string
	}{
		{
			"unquoted src",
			"<!-- pullquote src=hi -->",
			&pullQuote{src: "hi"},
			"",
		},
		{
			"quoted src",
			`<!-- pullquote src="hi" -->`,
			&pullQuote{src: "hi"},
			"",
		},
		{
			"escaped src",
			`<!-- pullquote src="hi\\" -->`,
			&pullQuote{src: `hi\`},
			"",
		},
		{
			"escaped quote src",
			`<!-- pullquote src="h \"" -->`,
			&pullQuote{src: `h "`},
			"",
		},
		{
			"escaped quote src middle",
			`<!-- pullquote src="h\"here" -->`,
			&pullQuote{src: `h"here`},
			"",
		},
		{
			"escaped quote src middle multi backslash",
			`<!-- pullquote src="h\\\"here" -->`,
			&pullQuote{src: `h\"here`},
			"",
		},
		{
			"start",
			`<!-- pullquote src="here" start=hi -->`,
			&pullQuote{src: `here`, start: reg("hi")},
			"",
		},
		{
			"here end",
			`<!-- pullquote src="here.go" start="hi" end=bye -->`,
			&pullQuote{src: `here.go`, start: reg("hi"), end: reg("bye")},
			"",
		},
		{
			"no quotes",
			`<!-- pullquote src=here.go start=hi end=bye -->`,
			&pullQuote{src: `here.go`, start: reg("hi"), end: reg("bye")},
			"",
		},
		{
			"unclosed quotes",
			`<!-- pullquote src="hi -->`,
			nil,
			`unclosed value expression: "\"hi"`,
		},
		{
			"unclosed key",
			`<!-- pullquote src -->`,
			nil,
			`unclosed key expression: "src"`,
		},
		{
			"unclosed escape",
			`<!-- pullquote src="\ -->`,
			nil,
			`unclosed escape expression`,
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			pq, err := parseLine(c.line)

			var errS string
			if err != nil {
				errS = err.Error()
			}
			if errS != c.err {
				t.Fatalf("wanted %q but got %q", c.err, err)
			} else if err != nil {
				return
			}

			comparePQ(t, c.pq, pq)
		})
	}
}

func Test_readPatterns(t *testing.T) {
	for _, c := range []struct {
		name, contents string
		pqs            []*pullQuote
		err            string
	}{
		{
			"empty",
			``,
			nil,
			"",
		},
		{
			"valid single",
			`
<!-- pullquote src=here.go start=hi end=bye -->
<!-- /pullquote -->
`,
			[]*pullQuote{
				{src: "here.go", start: reg("hi"), end: reg("bye")},
			},
			"",
		},
		{
			"valid multi",
			`
<!-- pullquote src=here.go start=hi end=bye -->
<!-- /pullquote -->
<!-- pullquote src=here1.go start=hi1 end=bye1 -->
<!-- /pullquote -->
`,
			[]*pullQuote{
				{src: "here.go", start: reg("hi"), end: reg("bye")},
				{src: "here1.go", start: reg("hi1"), end: reg("bye1")},
			},
			"",
		},
		{
			"unfinished",
			`
<!-- pullquote src=here.go start=hi end=bye -->
`,
			nil,
			"unfinished pullquote begun on line 2",
		},
		{
			"missing end",
			`
<!-- pullquote src=here.go start=hi -->
`,
			nil,
			"invalid pull quote on line 2: end cannot be unset",
		},
		{
			"missing start",
			`
<!-- pullquote src=here.go end=hi -->
`,
			nil,
			"invalid pull quote on line 2: start cannot be unset",
		},
		{
			"missing src",
			`
<!-- pullquote start=here.go end=hi -->
`,
			nil,
			"invalid pull quote on line 2: src cannot be unset",
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			r := strings.NewReader(c.contents)
			pqs, err := readPullQuotes(r)

			var errS string
			if err != nil {
				errS = err.Error()
			}
			if c.err != errS {
				t.Fatalf("wanted %q but got %q", c.err, errS)
			} else if err != nil {
				return
			}

			if len(pqs) != len(c.pqs) {
				t.Fatalf("wanted %d pqs but got %d", len(c.pqs), len(pqs))
			}

			for i := 0; i < len(pqs); i++ {
				comparePQ(t, c.pqs[i], pqs[i])
			}
		})
	}
}

func Test_expandPullQuotes(t *testing.T) {
	d := changeTmpDir(t)
	defer d.Close()

	for _, c := range []struct {
		name, fn string
		files    [][2]string
		pqs      []*pullQuote
		expected []string
		err      string
	}{
		{
			"single",
			"my/path.go",
			[][2]string{{"my/local.go",
				`
func fooBar() {
	// OK COOL
}
`}},
			[]*pullQuote{
				{src: "local.go", start: reg(`func fooBar\(\) {`), end: reg(`}`)},
			},
			[]string{"func fooBar() {\n\t// OK COOL\n}"},
			"",
		},
		{
			"two serially",
			"my/path.go",
			[][2]string{{"my/local.go",
				`
func fooBar() {
	// OK COOL
}

func baz() {
	// also good
}
`}},
			[]*pullQuote{
				{src: "local.go", start: reg(`func baz\(\) {`), end: reg(`}`)},
				{src: "local.go", start: reg(`func fooBar\(\) {`), end: reg(`}`)},
			},
			[]string{
				"func baz() {\n\t// also good\n}",
				"func fooBar() {\n\t// OK COOL\n}",
			},
			"",
		},
		{
			"overlap",
			"my/path.go",
			[][2]string{{"my/local.go",
				`
func fooBar() {
	// OK COOL
}
`}},
			[]*pullQuote{
				{src: "local.go", start: reg(`OK`), end: reg(`COOL`)},
				{src: "local.go", start: reg(`func fooBar\(\) {`), end: reg(`}`)},
			},
			[]string{
				"\t// OK COOL",
				"func fooBar() {\n\t// OK COOL\n}",
			},
			"",
		},
		{
			"multipath",
			"my/path.go",
			[][2]string{
				{
					"my/local.go",
					`
func fooBar() {
	// OK COOL
}
`,
				},
				{
					"my/other.go",
					`
func fooBaz() {
	// OK COOL
}
`,
				},
			},
			[]*pullQuote{
				{src: "local.go", start: reg(`func fooBar\(\) {`), end: reg(`}`)},
				{src: "other.go", start: reg(`func fooBaz\(\) {`), end: reg(`}`)},
				{src: "other.go", start: reg(`OK COOL`), end: reg(`OK COOL`)},
			},
			[]string{
				"func fooBar() {\n\t// OK COOL\n}",
				"func fooBaz() {\n\t// OK COOL\n}",
				"\t// OK COOL",
			},
			"",
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			for _, f := range c.files {
				writeFile(t, f[0], f[1])
			}
			res, err := expandPullQuotes(c.fn, c.pqs)
			var errS string
			if err != nil {
				errS = err.Error()
			}
			if errS != c.err {
				t.Fatalf("expected %q but got %q", c.err, errS)
			} else if err != nil {
				return
			}

			if len(res) != len(c.expected) {
				t.Fatalf("wanted %v matches but got %v", len(c.expected), len(res))
			}
			for i := range res {
				if res[i] != c.expected[i] {
					t.Errorf("wanted %q at %d but got %q", c.expected[i], i, res[i])
				}
			}
		})
	}
}

func writeFile(t *testing.T, fileLoc, val string) {
	if err := os.MkdirAll(path.Dir(fileLoc), 0o755); err != nil {
		t.Fatal(err)
	}
	func() {
		f, err := os.Create(fileLoc)
		if err != nil {
			t.Fatal(err)
		}
		defer func() {
			_ = f.Close()
		}()
		if _, err := io.WriteString(f, val); err != nil {
			t.Fatal(err)
		}
	}()
}

type testDir struct {
	tmpDir, origWd string
}

func (t *testDir) Close() {
	_ = os.Chdir(t.origWd)
	_ = os.RemoveAll(t.tmpDir)
}

func changeTmpDir(t *testing.T) *testDir {
	tmpDir, err := ioutil.TempDir("", t.Name())
	if err != nil {
		t.Fatal(err)
	}

	wd, err := os.Getwd()
	if err != nil {
		_ = os.RemoveAll(tmpDir)
		t.Fatal(err)
	}

	if err = os.Chdir(tmpDir); err != nil {
		_ = os.RemoveAll(tmpDir)
		t.Fatal(err)
	}

	return &testDir{tmpDir, wd}
}

func comparePQ(t *testing.T, expected, got *pullQuote) {
	if expected.src != got.src {
		t.Fatalf("wanted %q but got %q", expected.src, got.src)
	}
	compareRegexps(t, expected.start, got.start)
	compareRegexps(t, expected.end, got.end)
}

func compareRegexps(t *testing.T, expected, got *regexp.Regexp) {
	var expS, gotS string
	if expected != nil {
		expS = expected.String()
	}
	if got != nil {
		gotS = got.String()
	}
	if expS != gotS {
		t.Fatalf("wanted %q but got %q", expS, gotS)
	}
}
