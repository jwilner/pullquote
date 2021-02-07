package main

import (
	"fmt"
	"github.com/antlr/antlr4/runtime/Go/antlr"
	"github.com/jwilner/pullquote/python/parser"
	"io"
	"log"
	"os"
	"strings"
)

func main() {
	if len(os.Args) != 3 {
		log.Fatalf("USAGE: %v FILENAME OBJECT", os.Args[0])
	}
	if err := run(os.Stdout, os.Args[1], os.Args[2]); err != nil {
		log.Fatal(err)
	}
}

func run(w io.Writer, fileName, nodeName string) error {
	fileNode, err := parseFile(fileName)
	if err != nil {
		return err
	}
	node := findByName(fileNode, nodeName)
	if node == nil {
		return fmt.Errorf("unable to find node %v in %v", nodeName, fileName)
	}
	return printNode(w, node)
}

func parseFile(name string) (*parser.File_inputContext, error) {
	input, err := antlr.NewFileStream(name)
	if err != nil {
		return nil, err
	}
	p := parser.NewPython3Parser(
		antlr.NewCommonTokenStream(
			parser.NewPython3Lexer(input),
			0,
		),
	)
	p.RemoveErrorListeners() // don't log if the file is malformed
	p.BuildParseTrees = true
	// there's only one implementation of this interface 🙄
	return p.File_input().(*parser.File_inputContext), nil
}

func findByName(tree antlr.Tree, name string) antlr.ParserRuleContext {
	if tree == nil {
		return nil
	}

	switch t := tree.(type) {
	case *parser.FuncdefContext:
		if t.NAME().GetText() == name {
			return t
		}
	case *parser.ClassdefContext:
		if t.NAME().GetText() == name {
			return t
		}
		prefix := t.NAME().GetText()+"."
		if strings.HasPrefix(name, prefix) {
			name = name[len(prefix):] // e.g. FooBar.__init__ -> __init__
		}
	}

	for _, c := range tree.GetChildren() {
		if r := findByName(c, name); r != nil {
			return r
		}
	}

	return nil
}

func printNode(w io.Writer, ctx antlr.ParserRuleContext) error {
	start, stop := ctx.GetStart(), ctx.GetStop()

	f, err := os.Open(start.GetInputStream().GetSourceName())
	if err != nil {
		return err
	}
	defer func() {
		_ = f.Close()
	}()

	startOffset := start.GetStart()

	if _, err := f.Seek(int64(startOffset), io.SeekStart); err != nil {
		return err
	}
	if _, err := io.CopyN(w, f, int64(stop.GetStop()-startOffset)); err != nil {
		return err
	}
	_, err = w.Write([]byte{'\n'})
	return err
}
