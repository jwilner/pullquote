[![Tests](https://github.com/jwilner/pullquote/workflows/tests/badge.svg)](https://github.com/jwilner/pullquote/workflows/)

# pullquote

A simple documentation tool that keeps quotes or snippets in your docs up-to-date. Intended to be wired into CI so you never have to update your snippets again.

## Example

Given a piece of code to document like
```go
// fooBar is a very fine func
func fooBar() {
    fmt.Println("Cool!")
}
```

- Insert a `pullquote` tag in your doc:
```md
Check out my example function:

<!-- pullquote gopath=.#fooBar -->
<!-- /pullquote -->

Neat, huh?
```

- Run `pullquote` on  the doc
```shell
pullquote doc.md
```

- `pullquote` adds the snippet between the quotes.
~~~md
Check out my example function:

<!-- pullquote gopath=.#fooBar -->
```go
// fooBar is a very fine func
func fooBar() {
    fmt.Println("Cool!")
}
```
<!-- /pullquote -->

Neat, huh?
~~~

That's it.

## Options:

There are two mutually exclusive groups of pullquote options:

`gopath` group:

- `gopath` (required)

    The path to a package or file and a symbol within in the format `PATH_TO_PACKAGE_OR_FILE#SYMBOL`. Non-local packages are supported; e.g. `errors#New`.

`src` group:

- `src` (required)

    Specifies the file from which to pull the quote.


- `start` (required)

    A pattern or substring specifying the line on which to begin. Matches the first occurrence of the pattern.

- `end` (required)

    A pattern or substring specifying the line on which to end. Matches the first occurrence of the pattern after the start, or, if `endcount` is specified, the nth occurrence.

- `fmt`
    - `codefence` markdown code fence formatting, optionally with a language if `lang` specified
    - `blockquote` markdown block quote formatting

- `endcount`

    Specifies the number of times to match the end pattern before closing the pull quote.

- `lang`

    Specifies the language, if any, with which to highlight the `codefence`.
