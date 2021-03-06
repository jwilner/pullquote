[![Tests](https://github.com/jwilner/pullquote/workflows/tests/badge.svg)](https://github.com/jwilner/pullquote/workflows/)
[![Lint](https://github.com/jwilner/pullquote/workflows/lint/badge.svg)](https://github.com/jwilner/pullquote/actions?query=workflow%3Alint+branch%3Amain)
[![GoDoc](https://godoc.org/github.com/jwilner/pullquote?status.svg)](https://godoc.org/github.com/jwilner/pullquote)


# pullquote

A simple documentation tool that keeps quotes or snippets in your docs up-to-date. Intended to be wired into CI so you never have to update your snippets again.

Executable as either a binary or a GitHub Action.

## Example

Given a piece of code to document like:
<!-- goquote testdata/test_processFiles/gopath#fooBar -->
```go
// fooBar does some stuff
func fooBar() {
	// OK COOL
}
```
<!-- /goquote -->

- Insert a `pullquote` comment in your doc (markdown ignores html-style comments):
<!-- pullquote src=testdata/test_processFiles/gopath/README.md start=hello end=bye fmt=codefence lang=md -->
```md
hello
<!-- goquote .#fooBar -->
bye
```
<!-- /pullquote -->

- Run `pullquote` on  the doc
```shell
pullquote doc.md
```

- `pullquote` adds in the snippet styled the way you expect.
<!-- pullquote src=testdata/test_processFiles/gopath/README.expected.md start=hello end=bye fmt=codefence lang=md -->
~~~md
hello
<!-- goquote .#fooBar -->
```go
// fooBar does some stuff
func fooBar() {
	// OK COOL
}
```
<!-- /goquote -->
bye
~~~
<!-- /pullquote -->

`pullquote` understands all `go list` style paths and can pull in source code from anywhere, including third-party projects.

It also does JSON!
<!-- pullquote src=testdata/test_processFiles/jsonpath/README.expected.md start=hello end=bye fmt=codefence lang=md -->
~~~md
hello
<!-- jsonquote foo.json#/foo/0/1/bar -->
```json
[
  {
    "beep": "boop",
    "boop": "beep"
  }
]
```
<!-- /jsonquote -->
bye
~~~
<!-- /pullquote -->

## Usage

### Command line

`pullquote` supports two flags:

- `-walk` discovers all `*.md` files itself, rather than accepting over the CLI.
- `-check` will error if any files are not up-to-date, leaving the filesystem unmodified.

Specific files can be passed to `pullquote` either as args or over `stdin` (convenient for e.g. piping from `find`).

### GitHub Action

`pullquote` comes pre-packaged as a GitHub Action. In this mode, it uses `-walk` by default.

It can be paired with [peter-evans/create-pull-request](https://github.com/peter-evans/create-pull-request) to create PRs whenever snippets have fallen out of date. See [.github/workflows/pullquote.yaml](.github/workflows/pullquote.yaml) for an example.

## Options:

<!-- goquote .#keySrc includegroup -->
```go
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
```
<!-- /goquote -->
<!-- goquote .#keysCommonOptional includegroup -->
```go
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
```
<!-- /goquote -->
