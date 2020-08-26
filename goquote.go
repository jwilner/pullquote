package main

import (
	"context"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"golang.org/x/tools/go/packages"
	"os"
	"sort"
	"strings"
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
	}, pat)
	if err != nil {
		return nil, err
	}
	if len(pkgs) > 0 {
		return pkgs[0].Syntax, nil
	}
	return nil, nil
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

func expandGoQuotes(ctx context.Context, pqs []*pullQuote) ([]string, error) {
	res := make([]string, 0, len(pqs))
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

		s, err := sprintNodeWithName(fSet, files, sym, pq.goPrintFlags)
		if err != nil {
			return nil, fmt.Errorf("error within %v: %w", pat, err)
		}
		res = append(res, s)
	}

	return res, nil
}

func sprintNodeWithName(fSet *token.FileSet, files []*ast.File, name string, flags goPrintFlag) (string, error) {
	for _, f := range files {
		var (
			found []byte
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
				return false
			}
			return true
		})
		if err != nil {
			return "", err
		}
		if found == nil {
			continue
		}
		if flags&noRealignTabs == 0 {
			found = realignTabs(found)
		}
		return string(found), nil
	}
	return "", fmt.Errorf("couldn't find %q", name)
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
