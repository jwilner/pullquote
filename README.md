[![Tests](https://github.com/jwilner/pullquote/workflows/tests/badge.svg)](https://github.com/jwilner/pullquote/workflows/)

# pullquote

A simple documentation tool that keeps quotes or snippets in your docs up-to-date. Intended to be wired into CI so you never have to update your snippets again.

## Example

Given a piece of code to document like:
<!-- goquote testdata/test_run/gopath#fooBar -->
```go
// fooBar does some stuff
func fooBar() {
	// OK COOL
}
```
<!-- /goquote -->

- Insert a `pullquote` tag in your doc:
<!-- pullquote src=testdata/test_run/gopath/README.md start=hello end=bye fmt=codefence lang=md -->
```md
hello
<!-- goquote .#fooBar -->
<!-- /goquote -->
bye
```
<!-- /pullquote -->

- Run `pullquote` on  the doc
```shell
pullquote doc.md
```

- `pullquote` adds the snippet between the quotes.
<!-- pullquote src=testdata/test_run/gopath/README.expected.md start=hello end=bye fmt=codefence lang=md -->
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

That's it.

## Options:

<!-- goquote .#keySrc includegroup -->
```go
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
)
```
<!-- /goquote -->
<!-- goquote .#keysCommonOptional includegroup -->
```go
var (
	keysCommonOptional    = [...]string{keyFmt, keyLang}
	keysGoquoteValid      = [...]string{keyGoPath, keyNoRealign, keyIncludeGroup}
	keysPullQuoteOptional = [...]string{keyEndCount}
	keysPullQuoteRequired = [...]string{keySrc, keyStart, keyEnd}
	validFmts             = map[string]bool{fmtCodeFence: true, fmtBlockQuote: true, fmtNone: true}
)
```
<!-- /goquote -->
