package cmd

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestReservedSessionHeadersCoverDtctlsOwn keeps reservedSessionHeaders in
// step with the code. It reads every non-test Go file of both modules and
// requires that each header name dtctl sets on a request as a literal is
// reserved, so a session header can never collide with it.
//
// It recognizes the forms dtctl spells them in: resty's SetHeader, Set and
// Add on a request's Header, and a map literal handed to WithHeaders or
// SetHeaders. A name built at run time is not covered; neither are the trace
// context headers OpenTelemetry writes, which the list names explicitly.
func TestReservedSessionHeadersCoverDtctlsOwn(t *testing.T) {
	found := requestHeaderLiterals(t, "..")

	// The scan must see the headers it is known to find, or a pass proves
	// nothing.
	for _, known := range []string{"Content-Type", "Dt-Client-Context", "If-Match", "User-Agent"} {
		require.Contains(t, found, known, "the scan no longer finds a header dtctl is known to set")
	}
	for name, where := range found {
		_, reserved := reservedSessionHeaders[name]
		require.True(t, reserved, "%s sets the %s header; add it to reservedSessionHeaders", where, name)
	}
}

// requestHeaderLiterals maps each header name set as a literal in the
// non-test Go files under root, normalized, to the first place it is set.
func requestHeaderLiterals(t *testing.T, root string) map[string]string {
	t.Helper()
	skip := map[string]bool{".git": true, "docs": true, "test": true, "testdata": true, "dist": true, "node_modules": true}
	found := map[string]string{}
	record := func(fset *token.FileSet, lit ast.Expr) {
		basic, ok := lit.(*ast.BasicLit)
		if !ok || basic.Kind != token.STRING {
			return
		}
		name, err := strconv.Unquote(basic.Value)
		if err != nil || name == "" {
			return
		}
		name = normalizeHeaderName(name)
		if _, seen := found[name]; !seen {
			found[name] = fset.Position(basic.Pos()).String()
		}
	}

	require.NoError(t, filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			if skip[info.Name()] {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		fset := token.NewFileSet()
		file, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
		if err != nil {
			return err
		}
		ast.Inspect(file, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok || len(call.Args) == 0 {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			switch sel.Sel.Name {
			case "SetHeader":
				record(fset, call.Args[0])
			case "Set", "Add":
				// req.Header.Set, not w.Header().Set: a response writer's
				// headers are not sent on a request.
				if recv, ok := sel.X.(*ast.SelectorExpr); ok && recv.Sel.Name == "Header" {
					record(fset, call.Args[0])
				}
			case "WithHeaders", "SetHeaders":
				if lit, ok := call.Args[0].(*ast.CompositeLit); ok {
					for _, elt := range lit.Elts {
						if kv, ok := elt.(*ast.KeyValueExpr); ok {
							record(fset, kv.Key)
						}
					}
				}
			}
			return true
		})
		return nil
	}))
	return found
}
