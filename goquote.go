package main

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"regexp"
	"sort"
	"strings"

	"golang.org/x/tools/go/packages"
)

const parseMode = parser.ParseComments

func parseFile(fSet *token.FileSet, pat string) ([]*ast.File, error) {
	file, err := parser.ParseFile(fSet, pat, nil, parseMode)
	if err != nil {
		return nil, err
	}
	return []*ast.File{file}, nil
}

func parsePackage(ctx context.Context, fSet *token.FileSet, pat string) ([]*ast.File, error) {
	pkgs, err := packages.Load(&packages.Config{
		Mode: packages.NeedSyntax |
			packages.NeedTypes |
			packages.NeedImports |
			packages.NeedFiles |
			packages.NeedName,
		Context: ctx,
		Fset:    fSet,
		Tests:   true,
	}, pat)
	if err != nil {
		return nil, err
	}
	var syntax []*ast.File
	for _, pkg := range pkgs {
		syntax = append(syntax, pkg.Syntax...)
	}
	return syntax, nil
}

func parseDir(fSet *token.FileSet, pat string) ([]*ast.File, error) {
	pkgs, err := parser.ParseDir(fSet, pat, nil, parseMode)
	if err != nil {
		return nil, err
	}

	var numFiles int
	keys := make([]string, 0, len(pkgs))
	for k, p := range pkgs {
		keys = append(keys, k)
		numFiles += len(p.Files)
	}
	sort.Strings(keys) // sort so that `blah` comes before `blah_test`

	files := make([]*ast.File, 0, numFiles)
	for _, k := range keys {
		for _, f := range pkgs[k].Files {
			files = append(files, f)
		}
	}

	return files, nil
}

func expandGoQuotes(ctx context.Context, pqs []*pullQuote) ([]*expanded, error) {
	res := make([]*expanded, 0, len(pqs))
	for _, pq := range pqs {
		fSet := token.NewFileSet()

		parts := strings.SplitN(pq.goPath, "#", 2)
		pat, sym := parts[0], parts[1]

		var (
			files []*ast.File
			err   error
		)
		if strings.HasSuffix(pat, ".go") {
			files, err = parseFile(fSet, pat)
		} else if files, err = parsePackage(ctx, fSet, pat); err == nil && len(files) == 0 {
			files, err = parseDir(fSet, pat)
		}

		s, err := sprintNodeWithName(fSet, files, sym, pq.goPrintFlags, pq.fmt == fmtExample)
		if err != nil {
			return nil, fmt.Errorf("error within %v: %w", pat, err)
		}
		res = append(res, s)
	}

	return res, nil
}

func sprintNodeWithName(fSet *token.FileSet, files []*ast.File, name string, flags goPrintFlag, example bool) (*expanded, error) {
	for _, f := range files {
		var (
			found []byte
			parts [][]byte
			err   error
		)
		ast.Inspect(f, func(node ast.Node) bool {
			if found != nil || err != nil {
				return false
			}
			switch x := node.(type) {
			case *ast.AssignStmt:
				if x.Tok != token.DEFINE {
					break
				}
				for _, lhs := range x.Lhs {
					if ident, ok := lhs.(*ast.Ident); ok {
						if ident.Name == name {
							found, err = renderNode(fSet, nil, x)
							return false
						}
					}
				}
			case *ast.GenDecl:
				switch x.Tok {
				case token.CONST, token.VAR:
					for _, s := range x.Specs {
						s := s.(*ast.ValueSpec)
						for _, n := range s.Names {
							if n.Name == name {
								if flags&includeGroup != 0 || x.Lparen == token.NoPos {
									found, err = renderNode(fSet, x.Doc, x)
									return false
								}
								found, err = renderNode(fSet, s.Doc, s)
								return false
							}
						}
					}
				case token.TYPE:
					for _, s := range x.Specs {
						s := s.(*ast.TypeSpec)
						if s.Name.Name == name {
							if flags&includeGroup != 0 || x.Lparen == 0 {
								found, err = renderNode(fSet, x.Doc, x)
								return false
							}
							found, err = renderNode(fSet, s.Doc, s)
							return false
						}
					}
				}
			case *ast.FuncDecl:
				if x.Name.Name != name {
					break
				}
				found, err = renderNode(fSet, x.Doc, x)
				if err != nil {
					return false
				}
				if example {
					parts, err = parseExampleTest(found)
				}
				return false
			}
			return true
		})
		if err != nil {
			return nil, err
		}
		if found == nil {
			continue
		}
		if flags&noRealignTabs == 0 {
			found = realignTabs(found)
		}
		exp := &expanded{String: string(found)}
		for _, p := range parts {
			exp.Parts = append(exp.Parts, string(p))
		}
		return exp, nil
	}
	return nil, fmt.Errorf("couldn't find %q", name)
}

var (
	regexpOutputComment = regexp.MustCompile(`^\s*//\s*Output:\s*$`)
	regexpCommentPrefix = regexp.MustCompile(`^\s*//\s?(.*)$`)
)

func parseExampleTest(f []byte) (res [][]byte, err error) {
	var (
		sawDecl     bool
		buf         bytes.Buffer
		sawOutput   bool
		firstPrefix = -1
		write       = func(b []byte) {
			if err != nil {
				return
			}
			if buf.Len() != 0 {
				buf.WriteByte('\n')
			}
			_, err = buf.Write(b)
		}
	)

	s := bufio.NewScanner(bytes.NewReader(f))
	for s.Scan() && err == nil {
		if !sawDecl {
			// check if decl
			sawDecl = strings.HasPrefix(s.Text(), "func ")
			continue
		}
		if sawOutput {
			matches := regexpCommentPrefix.FindSubmatch(s.Bytes())
			if len(matches) == 2 {
				write(matches[1])
			}
			continue
		}
		if regexpOutputComment.MatchString(s.Text()) {
			cp := make([]byte, buf.Len())
			copy(cp, buf.Bytes())
			res = append(res, cp)

			buf.Reset()
			sawOutput = true
			continue
		}
		// in the function body
		l := s.Bytes()

		if firstPrefix == -1 {
			firstPrefix = 0
			for _, b := range l {
				if b != '\t' {
					break
				}
				firstPrefix++
			}
		}

		start := 0
		for start < firstPrefix && start < len(l) && l[start] == '\t' {
			start++
		}

		write(l[start:])
	}
	if err == nil {
		err = s.Err()
	}
	if err != nil {
		return nil, err
	}
	return append(res, buf.Bytes()), nil
}

func realignTabs(found []byte) []byte {
	expectedInset := 1
	if len(found) >= 2 && found[0] == '/' && found[1] == '/' {
		expectedInset = 0
	}

	tabsToRemove := -1
	for _, b := range found {
		if tabsToRemove == -1 {
			if b == '\n' { // start counting at first newline
				tabsToRemove = 0
			}
			continue
		}
		if b == '\t' {
			tabsToRemove++
			continue
		}
		break
	}

	tabsToRemove -= expectedInset
	if tabsToRemove <= 0 {
		return found
	}

	var tabsSeen, cur int
	for _, b := range found {
		if tabsSeen == -1 {
			if b == '\n' {
				tabsSeen = 0
			}
		} else if b == '\t' {
			tabsSeen++
			if tabsSeen <= tabsToRemove {
				continue
			}
		} else {
			tabsSeen = -1
		}
		found[cur] = b
		cur++
	}
	return found[:cur]
}

func renderNode(fSet *token.FileSet, doc *ast.CommentGroup, node ast.Node) ([]byte, error) {
	sPos := node.Pos()
	if doc != nil {
		sPos = doc.Pos()
	}

	pos, end := fSet.PositionFor(sPos, false), fSet.PositionFor(node.End(), false)
	if !pos.IsValid() || !end.IsValid() {
		panic("invalid node for fSet passed")
	}

	buf := make([]byte, end.Offset-pos.Offset)
	f, err := os.Open(pos.Filename)
	if err != nil {
		return nil, err
	}
	defer func() {
		if cErr := f.Close(); cErr != nil && err == nil {
			err = cErr
		}
	}()
	_, err = f.ReadAt(buf, int64(pos.Offset))
	return buf, err
}
