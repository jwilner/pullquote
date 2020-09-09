package main

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"io/ioutil"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

var reg = regexp.MustCompile

func Test_processFiles(t *testing.T) {
	slCh := func(sl []string) <-chan string {
		ch := make(chan string, len(sl))
		for _, s := range sl {
			ch<-s
		}
		close(ch)
		return ch
	}

	entries, err := ioutil.ReadDir("testdata/test_processFiles")
	if err != nil {
		t.Skipf("testdata/test_processFiles not usable: %v", err)
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		if e.Name() != "multiple" {
			continue
		}
		dataDir, err := filepath.Abs(filepath.Join("testdata/test_processFiles", e.Name()))
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

			checkEqual := func(t *testing.T) {
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
			}

			t.Run("first pass", func(t *testing.T) {
				if err := processFiles(context.Background(), false, slCh(inFiles)); err != nil {
					t.Fatal(err)
				}
				checkEqual(t)
			})

			if t.Failed() {
				t.SkipNow()
			}

			t.Run("idempotent", func(t *testing.T) {
				if err := processFiles(context.Background(), false, slCh(inFiles)); err != nil {
					t.Fatal(err)
				}
				checkEqual(t)
			})
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
				t.Fatalf("wanted:\n%q\ngot:\n%q", c.expected, got)
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
			fmt.Errorf("parsing pullquote at offset 0: %w", errTokUnterminated).Error(),
		},
		{
			"unclosed key",
			`<!-- pullquote src -->`,
			nil,
			`parsing pullquote at offset 0: "src" requires value`,
		},
		{
			"unclosed escape",
			`<!-- pullquote src="\ -->`,
			nil,
			fmt.Errorf("parsing pullquote at offset 0: %w", errTokUnterminated).Error(),
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
			&pullQuote{
				tagType:      "go",
				goPath:       ".#ExampleFooBar",
				fmt:          "example",
				lang:         "go",
				goPrintFlags: noRealignTabs,
			},
			"",
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			pq, err := readPullQuotes(context.Background(), strings.NewReader(c.line))

			var errS string
			if err != nil {
				errS = err.Error()
			}
			if errS != c.err {
				t.Fatalf("wanted %q but got %q", c.err, err)
			} else if err != nil {
				return
			}

			comparePQ(t, "", c.line, c.pq, pq[0])
		})
	}
}

func Test_readPullQuotes(t *testing.T) {
	type testCase struct {
		name, contents string
		pqs            []*pullQuote
		err            string
	}
	cases := []testCase{
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
<!-- pullquote src=here1.go start=hi1 end=bye1 --><!-- /pullquote -->
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
			[]*pullQuote{
				{
					tagType:  "pull",
					src:      "here.go",
					start:    reg("hi"),
					end:      reg("bye"),
					startIdx: 48,
					endIdx:   idxNoEnd,
				},
			},
			"",
		},
		{
			"missing end",
			`
<!-- pullquote src=here.go start=hi -->
`,
			nil,
			"validating pullquote at offset 1: \"end\" cannot be unset",
		},
		{
			"missing start",
			`
<!-- pullquote src=here.go end=hi -->
`,
			nil,
			"validating pullquote at offset 1: \"start\" cannot be unset",
		},
		{
			"missing src",
			`
<!-- pullquote start=here.go end=hi -->
`,
			nil,
			"validating pullquote at offset 1: \"src\" cannot be unset",
		},
		{
			"markdown comment",
			`
<!-- pullquote src=README.md start=hello end=bye fmt=codefence lang=md -->
` + "```" + `md
hello
<!-- goquote .#fooBar -->
bye
` + "```" + `
<!-- /pullquote -->
`,
			[]*pullQuote{
				{
					src:     "README.md",
					start:   reg("hello"),
					end:     reg("bye"),
					fmt:     "codefence",
					lang:    "md",
					tagType: "pull",
				},
			},
			"",
		},
		{
			"from readme",
			`hello
<!-- goquote .#ExampleFooBar -->
Code:
` + "```" + `go
FooBar(i)
` + "```" + `
Output:
` + "```" + `
FooBarRan 0
` + "```" + `
<!-- /goquote -->
bye
`,
			[]*pullQuote{{tagType: "go", goPath: ".#ExampleFooBar", fmt: fmtExample, lang: "go"}},
			"",
		},
	}

	if readMe := loadReadMe(t); readMe != "" {
		cases = append(cases, testCase{
			name:     "README.md",
			contents: readMe,
			pqs: []*pullQuote{
				{goPath: "testdata/test_processFiles/gopath#fooBar", fmt: "codefence", lang: "go", tagType: "go"},
				{
					src:     "testdata/test_processFiles/gopath/README.md",
					fmt:     "codefence",
					lang:    "md",
					tagType: "pull",
					start:   reg("hello"),
					end:     reg("bye"),
				},
				{
					src:     "testdata/test_processFiles/gopath/README.expected.md",
					fmt:     "codefence",
					lang:    "md",
					tagType: "pull",
					start:   reg("hello"),
					end:     reg("bye"),
				},
				{goPath: ".#keySrc", fmt: "codefence", lang: "go", tagType: "go", goPrintFlags: includeGroup},
				{
					goPath:       ".#keysCommonOptional",
					fmt:          "codefence",
					lang:         "go",
					tagType:      "go",
					goPrintFlags: includeGroup,
				},
			},
		})
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			pqs, err := readPullQuotes(context.Background(), strings.NewReader(c.contents))
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
				t.Fatalf("expected %d pqs but got %d", len(c.pqs), len(pqs))
			}

			for i := 0; i < len(pqs); i++ {
				comparePQ(t, strconv.Itoa(i), c.contents, c.pqs[i], pqs[i])
			}
		})
	}
}

func loadReadMe(t *testing.T) string {
	f, err := os.Open("README.md")
	if os.IsNotExist(err) {
		t.Logf("README.md does not exist; not running test")
		return ""
	}
	if err != nil {
		t.Fatalf("unable to load README.md: %v", err)
	}
	defer func() {
		_ = f.Close()
	}()

	b, err := ioutil.ReadAll(f)
	if err != nil {
		t.Fatalf("Unable to read all: %v", err)
	}
	return string(b)
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
					t.Errorf("wanted at %d:\n%q\ngot:\n%q", i, c.expected[i], res[i].String)
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

func comparePQ(t *testing.T, label string, src string, expected, got *pullQuote) {
	type check struct {
		name string
		l, r interface{}
	}
	checks := []check{
		{"goPath", expected.goPath, got.goPath},
		{"src", expected.src, got.src},
		{"fmt", expected.fmt, got.fmt},
		{"lang", expected.lang, got.lang},
		{"tagType", expected.tagType, got.tagType},

		{"endCount", expected.endCount, got.endCount},
		{"start", expected.start, got.start},
		{"end", expected.end, got.end},

		{"goPrintFlags", int(expected.goPrintFlags), int(got.goPrintFlags)},
	}

	if expected.startIdx != 0 || expected.endIdx != 0 {
		checks = append(checks, []check{
			{"startIdx", expected.startIdx, got.startIdx},
			{"endIdx", expected.endIdx, got.endIdx},
		}...)
	}

	for _, comp := range checks {
		switch v := comp.l.(type) {
		case string:
			if v != comp.r.(string) {
				t.Errorf("%v.%v: wanted %q but got %q", label, comp.name, comp.l, comp.r)
			}
		case int:
			if v != comp.r.(int) {
				t.Errorf("%v.%v:  wanted %v but got %v", label, comp.name, comp.l, comp.r)
			}
		case *regexp.Regexp:
			var expS, gotS string
			if v != nil {
				expS = v.String()
			}
			if r := comp.r.(*regexp.Regexp); r != nil {
				gotS = r.String()
			}
			if expS != gotS {
				t.Errorf("%v.%v: wanted %q but got %q", label, comp.name, expS, gotS)
			}
		default:
			panic("unknown type")
		}
	}

	if src != "" && !(expected.startIdx != 0 || expected.endIdx != 0) {
		src = src[:got.startIdx]
		src = src[strings.LastIndex(src, "<!--"):]

		pqs, err := readPullQuotes(context.Background(), strings.NewReader(src))
		if err != nil {
			t.Errorf("unexpected error while loading pqs for comparison: %v", err)
			return
		}
		if len(pqs) == 0 {
			t.Errorf("expected at least one pullquote at provided offset")
			return
		}
		pqs[0].startIdx, pqs[0].endIdx = 0, 0
		comparePQ(t, label+".reloaded", "", pqs[0], got)
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

type pos struct {
	str string
	// if start end are provided, check they match offsets returned; otherwise, check str matches offsets returned.
	start, end int
}

func Test_tokenizingScanner(t *testing.T) {
	for _, tt := range []struct {
		name string
		val  string
		res  []pos
	}{
		{
			"whitespace stripped",
			"  abc  ",
			[]pos{{"abc", 2, 5}},
		},
		{
			"quoted",
			`  "abc ="  `,
			[]pos{{"abc =", 2, 9}},
		},
		{
			"escaped quote",
			`  "abc \""  `,
			[]pos{{`abc "`, 2, 10}},
		},
		{
			"equals in the middle",
			`  "abc \""=23  `,
			[]pos{{`abc "`, 2, 10}, {`=`, 10, 11}, {`23`, 11, 13}},
		},
		{
			"double escape",
			`  "abc \\"=23  `,
			[]pos{{`abc \`, 2, 10}, {`=`, 10, 11}, {`23`, 11, 13}},
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			runScannerTest(t, tokenizingScanner(strings.NewReader(tt.val)), tt.val, tt.res)
		})
	}
}

func Test_htmlCommentScanner(t *testing.T) {
	type testCase struct {
		name string
		val  string
		res  []pos
	}
	cases := []testCase{
		{
			"finds",
			`abcdef<!--1234567890-->ghj`,
			[]pos{{str: "<!--1234567890-->"}},
		},
		{
			"finds",
			`abcdef<!--1234567890-->ghj<!--ok-->`,
			[]pos{{str: "<!--1234567890-->"}, {str: "<!--ok-->"}},
		},
		{
			"nothing between",
			`a<!---->b`,
			[]pos{{str: "<!---->"}},
		},
		{
			"unfinished start",
			`abcdef<!--`,
			nil,
		},
		{
			"unfinished end",
			`abcdef<!----`,
			nil,
		},
		{
			"markdown comment",
			`
<!-- pullquote src=README.md start=hello end=bye fmt=codefence lang=md -->
` + "```" + `md
			hello
			<!-- goquote .#fooBar -->
			bye
` + "```" + `
<!-- /pullquote -->
`,
			[]pos{
				{str: "<!-- pullquote src=README.md start=hello end=bye fmt=codefence lang=md -->"},
				{str: "<!-- /pullquote -->"},
			},
		},
		{
			"example",
			`hello
<!-- goquote .#ExampleFooBar -->
Code:
` + "```" + `go
FooBar(i)
` + "```" + `
Output:
` + "```" + `
FooBarRan 0
` + "```" + `
<!-- /goquote -->
bye
`,
			[]pos{
				{str: "<!-- goquote .#ExampleFooBar -->"},
				{str: "<!-- /goquote -->"},
			},
		},
	}
	if readMe := loadReadMe(t); readMe != "" {
		cases = append(cases, testCase{
			"README.md",
			readMe,
			[]pos{
				{str: "<!-- goquote testdata/test_processFiles/gopath#fooBar -->"},
				{str: "<!-- /goquote -->"},
				{str: "<!-- pullquote src=testdata/test_processFiles/gopath/README.md start=hello end=bye fmt=codefence lang=md -->"},
				{str: "<!-- /pullquote -->"},
				{str: "<!-- pullquote src=testdata/test_processFiles/gopath/README.expected.md start=hello end=bye fmt=codefence lang=md -->"},
				{str: "<!-- /pullquote -->"},
				{str: "<!-- goquote .#keySrc includegroup -->"},
				{str: "<!-- /goquote -->"},
				{str: "<!-- goquote .#keysCommonOptional includegroup -->"},
				{str: "<!-- /goquote -->"},
			},
		})
	}

	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			runScannerTest(t, htmlCommentScanner(strings.NewReader(tt.val)), tt.val, tt.res)
		})
	}
}

func runScannerTest(t *testing.T, sc *trackingScanner, val string, expected []pos) {
	var res []pos
	for sc.Scan() {
		res = append(res, pos{sc.Text(), sc.start, sc.end})
	}
	if err := sc.Err(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	for i, r := range expected {
		if i >= len(res) {
			t.Errorf("token %d: wanted %v but missing", i, r)
			continue
		}
		if r.str != res[i].str {
			t.Errorf("token %d: wanted %v but got %v", i, r.str, res[i].str)
		}
		if r.start != 0 || r.end != 0 {
			if r.start != res[i].start {
				t.Errorf("token start %d: wanted %v but got %v", i, r.start, res[i].start)
			}
			if r.end != res[i].end {
				t.Errorf("token end %d: wanted %v but got %v", i, r.end, res[i].end)
			}
			continue
		}
		if val[res[i].start:res[i].end] != res[i].str {
			t.Errorf("expected returned indices [%d, %d) to match output but got %v", res[i].start, res[i].end,
				res[i].str)
		}
	}
	for i, r := range res {
		if i >= len(expected) {
			t.Errorf("token %d: got unexpected %v", i, r)
			continue
		}
	}
}

func Test_filesChanged(t *testing.T) {
	td := changeTmpDir(t)
	defer td.Close()

	var fs []*os.File
	defer func() {
		for _, f := range fs {
			_ = f.Close()
		}
	}()
	writeTmp := func(data string) *os.File {
		fA, err := ioutil.TempFile(td.tmpDir, "")
		if err != nil {
			t.Fatal(err)
		}
		fs = append(fs, fA)

		if _, err = fA.WriteString(data); err != nil {
			t.Fatal(err)
		}

		return fA
	}

	a := writeTmp("abcdefghijklmonp")
	b := writeTmp("abcdefghijklmonp")
	c := writeTmp("abcdefghijklmon") // different

	t.Run("identity", func(t *testing.T) {
		changed, err := filesChanged(a, a)
		if err != nil {
			t.Fatalf("unexpected failure: %v", err)
		}
		if changed {
			t.Fatal("Expected same file to be equal to itself")
		}
	})

	t.Run("same contents", func(t *testing.T) {
		changed, err := filesChanged(a, b)
		if err != nil {
			t.Fatalf("unexpected failure: %v", err)
		}
		if changed {
			t.Fatal("Expected same contexts to be equal")
		}
	})

	t.Run("different contents", func(t *testing.T) {
		changed, err := filesChanged(a, c)
		if err != nil {
			t.Fatalf("unexpected failure: %v", err)
		}
		if !changed {
			t.Fatal("Expected different contents to be unequal")
		}
	})
}
