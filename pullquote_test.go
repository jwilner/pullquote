package main

import (
	"bytes"
	"context"
	"io"
	"io/ioutil"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

var reg = regexp.MustCompile

func Test_run(t *testing.T) {
	entries, err := ioutil.ReadDir("testdata/test_run")
	if err != nil {
		t.Skipf("testdata/test_run not usable: %v", err)
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		dataDir, err := filepath.Abs(filepath.Join("testdata/test_run", e.Name()))
		if err != nil {
			t.Fatalf("abs: %v", err)
		}
		t.Run(e.Name(), func(t *testing.T) {
			tDir := changeTmpDir(t)
			defer tDir.Close()

			var (
				inFiles, expectedFiles []string
				golden                 bool
			)
			if err := filepath.Walk(dataDir, func(path string, info os.FileInfo, err error) error {
				switch {
				case err != nil, info.IsDir():
					return err
				case info.Name() == "GOLDEN":
					golden = true
					return nil
				case strings.HasSuffix(path, ".expected.md"):
					expectedFiles = append(expectedFiles, path)
					inFiles = append(inFiles, strings.Replace(path[len(dataDir)+1:], ".expected", "", -1))
					return nil
				default:
					rel := path[len(dataDir)+1:]
					return copyFile(path, rel)
				}
			}); err != nil {
				t.Fatalf("unable to copy: %v", err)
			}

			if err := run(context.Background(), inFiles); err != nil {
				t.Fatal(err)
			}

			for i := 0; i < len(expectedFiles); i++ {
				expected, err := ioutil.ReadFile(expectedFiles[i])
				if err != nil {
					t.Fatal(err)
				}
				in, err := ioutil.ReadFile(inFiles[i])
				if err != nil {
					t.Fatal(err)
				}
				if !bytes.Equal(expected, in) {
					if golden {
						if err := ioutil.WriteFile(expectedFiles[i], in, 0o644); err != nil {
							t.Fatal(err)
						}
						return
					}
					t.Fatalf("outputs did not match\nwanted:\n\n%v\n\ngot:\n\n%v", string(expected), string(in))
				}
			}
		})
	}
}

func Test_processFile(t *testing.T) {

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
		{
			"gopath",
			[][2]string{
				{
					"my/README.md",
					`
hello
<!-- goquote ./#fooBar -->
<!-- /goquote -->
bye
`,
				},
				{
					"my/local.go",
					`package main

func fooBar() {
	// OK COOL
}
`,
				},
			},
			"my/README.md",
			`
hello
<!-- goquote ./#fooBar -->
` + "```" + `go
func fooBar() {
	// OK COOL
}
` + "```" + `
<!-- /goquote -->
bye
`,
			"",
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			d := changeTmpDir(t)
			defer d.Close()

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
			"<!-- pullquote src=hi start=a end=b -->",
			&pullQuote{tagType: "pull", src: "hi", start: reg("a"), end: reg("b")},
			"",
		},
		{
			"quoted src",
			`<!-- pullquote src="hi" start=a end=b -->`,
			&pullQuote{tagType: "pull", src: "hi", start: reg("a"), end: reg("b")},
			"",
		},
		{
			"escaped src",
			`<!-- pullquote src="hi\\" start=a end=b -->`,
			&pullQuote{tagType: "pull", src: `hi\`, start: reg("a"), end: reg("b")},
			"",
		},
		{
			"escaped quote src",
			`<!-- pullquote src="h \"" start=a end=b -->`,
			&pullQuote{tagType: "pull", src: `h "`, start: reg("a"), end: reg("b")},
			"",
		},
		{
			"escaped quote src middle",
			`<!-- pullquote src="h\"here" start=a end=b -->`,
			&pullQuote{tagType: "pull", src: `h"here`, start: reg("a"), end: reg("b")},
			"",
		},
		{
			"escaped quote src middle multi backslash",
			`<!-- pullquote src="h\\\"here" start=a end=b -->`,
			&pullQuote{tagType: "pull", src: `h\"here`, start: reg("a"), end: reg("b")},
			"",
		},
		{
			"start",
			`<!-- pullquote src="here" start=hi end=b -->`,
			&pullQuote{tagType: "pull", src: `here`, start: reg("hi"), end: reg("b")},
			"",
		},
		{
			"here end",
			`<!-- pullquote src="here.go" start="hi" end=bye -->`,
			&pullQuote{tagType: "pull", src: `here.go`, start: reg("hi"), end: reg("bye")},
			"",
		},
		{
			"no quotes",
			`<!-- pullquote src=here.go start=hi end=bye fmt=codefence -->`,
			&pullQuote{tagType: "pull", src: `here.go`, start: reg("hi"), end: reg("bye"), fmt: "codefence"},
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
			`no value given for "src"`,
		},
		{
			"unclosed escape",
			`<!-- pullquote src="\ -->`,
			nil,
			`unclosed escape expression`,
		},
		{
			"goquote",
			`<!-- goquote .#Foo -->`,
			&pullQuote{tagType: "go", goPath: ".#Foo", fmt: "codefence", lang: "go"},
			"",
		},
		{
			"goquote quoted",
			`<!-- goquote ".#Foo" -->`,
			&pullQuote{tagType: "go", goPath: ".#Foo", fmt: "codefence", lang: "go"},
			"",
		},
		{
			"goquote flag norealign",
			`<!-- goquote .#Foo norealign -->`,
			&pullQuote{tagType: "go", goPath: ".#Foo", fmt: "codefence", lang: "go", goPrintFlags: noRealignTabs},
			"",
		},
		{
			"goquote example",
			`<!-- goquote .#ExampleFooBar norealign -->`,
			&pullQuote{tagType: "go", goPath: ".#ExampleFooBar", fmt: "example", lang: "go"},
			"",
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
				{tagType: "pull", src: "here.go", start: reg("hi"), end: reg("bye")},
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
				{tagType: "pull", src: "here.go", start: reg("hi"), end: reg("bye")},
				{tagType: "pull", src: "here1.go", start: reg("hi1"), end: reg("bye1")},
			},
			"",
		},
		{
			"skip codefence",
			`
` + "```go" + `
~~~
<!-- pullquote src=here.go start=hi end=bye -->
<!-- /pullquote -->
~~~
` + "```" + `
<!-- pullquote src=here1.go start=hi1 end=bye1 -->
<!-- /pullquote -->
`,
			[]*pullQuote{
				{tagType: "pull", src: "here1.go", start: reg("hi1"), end: reg("bye1")},
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
			"parsing line 2: \"end\" cannot be unset",
		},
		{
			"missing start",
			`
<!-- pullquote src=here.go end=hi -->
`,
			nil,
			"parsing line 2: \"start\" cannot be unset",
		},
		{
			"missing src",
			`
<!-- pullquote start=here.go end=hi -->
`,
			nil,
			"parsing line 2: \"src\" cannot be unset",
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
			"endCount",
			"my/path.go",
			[][2]string{{"my/local.go",
				`
func fooBar() {
	// OK COOL
}

func fooBaz() {
	// ok
}
`}},
			[]*pullQuote{
				{src: "local.go", start: reg(`func fooBar\(\) {`), end: reg(`}`), endCount: 2},
			},
			[]string{"func fooBar() {\n\t// OK COOL\n}\n\nfunc fooBaz() {\n\t// ok\n}"},
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
		{
			"func with doc comment",
			"my/path.go",
			[][2]string{{"my/local.go",
				`package main

// doc comment
func fooBar() {
	// OK COOL
	fmt.Println("nice")
}
`}},
			[]*pullQuote{
				{goPath: "local.go#fooBar"},
			},
			[]string{"// doc comment\nfunc fooBar() {\n\t// OK COOL\n\tfmt.Println(\"nice\")\n}"},
			"",
		},
		{
			"type decl",
			"my/path.go",
			[][2]string{{"my/local.go",
				`package main

type (
	// Foo does some stuff
	// and other stuff
	Foo struct {
		// floating inline
		A int // trailing inline
		// Also this
	}
	// Bar does some other stuff
	Bar struct {
		B int
	}
)
`}},
			[]*pullQuote{
				{goPath: "local.go#Foo"},
			},

			[]string{
				`// Foo does some stuff
// and other stuff
Foo struct {
	// floating inline
	A int // trailing inline
	// Also this
}`,
			},
			"",
		},
		{
			"const decl",
			"my/path.go",
			[][2]string{{"my/local.go",
				`package main

const (
	// Foo does some important things
	Foo = iota
	Bar
)

`}},
			[]*pullQuote{
				{goPath: "local.go#Foo"},
			},
			[]string{
				`// Foo does some important things
Foo = iota`,
			},
			"",
		},
		{
			"const decl include groups",
			"my/path.go",
			[][2]string{{"my/local.go",
				`package main

// a bunch of great stuff
const (
	// Foo does some important things
	Foo = iota
	Bar
)

`}},
			[]*pullQuote{
				{goPath: "local.go#Foo", goPrintFlags: includeGroup},
			},
			[]string{
				`// a bunch of great stuff
const (
	// Foo does some important things
	Foo = iota
	Bar
)`,
			},
			"",
		},
		{
			"third party",
			"my/path.go",
			[][2]string{},
			[]*pullQuote{
				{goPath: "errors#New"},
			},
			[]string{"// New returns an error that formats as the given text.\n// Each call to New returns a distinct error value even if the text is identical.\nfunc New(text string) error {\n\treturn &errorString{text}\n}"},
			"",
		},
		{
			"random var",
			"my/path.go",
			[][2]string{{"my/local.go",
				`package main

// doc comment
func fooBar() {
	// OK COOL
	a := 23
	fmt.Println(a)
}
`}},
			[]*pullQuote{
				{goPath: "local.go#a"},
			},
			[]string{"a := 23"},
			"",
		},
		{
			"zero var",
			"my/path.go",
			[][2]string{{"my/local.go",
				`package main

// blah means nothing
var blah int
`}},
			[]*pullQuote{
				{goPath: "local.go#blah"},
			},
			[]string{
				`// blah means nothing
var blah int`,
			},
			"",
		},
		{
			"inline const",
			"my/path.go",
			[][2]string{{"my/local.go",
				`package main

func fooBar() {
	// const blah
	const a int = 23
}
`}},
			[]*pullQuote{
				{goPath: "./#a"},
			},
			[]string{
				`// const blah
const a int = 23`,
			},
			"",
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			d := changeTmpDir(t)
			defer d.Close()

			for _, f := range c.files {
				writeFile(t, f[0], f[1])
			}
			for _, pq := range c.pqs {
				if pq.src != "" {
					pq.src = filepath.Join(filepath.Dir(c.fn), pq.src)
				}
				if pq.goPath != "" && strings.HasPrefix(pq.goPath, "./") || strings.Contains(pq.goPath, ".go") {
					pq.goPath = filepath.Join(filepath.Dir(c.fn), pq.goPath)
				}
			}
			res, err := expandPullQuotes(context.Background(), c.pqs)
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
				if res[i].String != c.expected[i] {
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
	tmpDir, err := ioutil.TempDir("", strings.Replace(t.Name(), "/", "_", -1))
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
	for _, comp := range []struct {
		l, r interface{}
	}{
		{expected.goPath, got.goPath},
		{expected.src, got.src},
		{expected.fmt, got.fmt},
		{expected.lang, got.lang},
		{expected.tagType, got.tagType},

		{expected.endCount, got.endCount},

		{expected.start, got.start},
		{expected.end, got.end},
	} {
		switch v := comp.l.(type) {
		case string:
			if v != comp.r.(string) {
				t.Fatalf("wanted %q but got %q", comp.l, comp.r)
			}
		case int:
			if v != comp.r.(int) {
				t.Fatalf("wanted %v but got %v", comp.l, comp.r)
			}
		case *regexp.Regexp:
			compareRegexps(t, v, comp.r.(*regexp.Regexp))
		default:
			panic("unknown type")
		}
	}

	if expected.goPath != got.goPath {
		t.Fatalf("wanted %q but got %q", expected.goPath, got.goPath)
	}
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

func copyFile(src, dst string) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}

	f, err := os.Open(src)
	if err != nil {
		return err
	}
	defer func() {
		_ = f.Close()
	}()

	g, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer func() {
		_ = g.Close()
	}()

	_, err = io.Copy(g, f)
	return err
}
