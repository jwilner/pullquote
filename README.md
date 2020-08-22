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

<!-- pullquote src=file.go start="// fooBar" end=^} codefence=go -->
<!-- /pullquote -->

Neat, huh?
```

- Run `pullquote` on  the doc
```shell
pullquote doc.md
```

- `pullquote` adds the snippet between the quotes.
```md
Check out my example function:

<!-- pullquote src=file.go start="// fooBar" end=^} codefence=go -->
```go
// fooBar is a very fine func
func fooBar() {
    fmt.Println("Cool!")
}
\`\`\`
<!-- /pullquote -->

Neat, huh?
```

That's it.
