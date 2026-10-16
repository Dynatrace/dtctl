package cmd

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// cloudMutatingFiles are the hyperscaler command files whose RunE bodies call a
// handler's Create/Update directly from flags, rather than going through
// pkg/apply (which has its own dry-run path).
var cloudMutatingFiles = []string{
	"create_aws.go", "create_azure.go", "create_gcp.go",
	"update_aws.go", "update_azure.go", "update_gcp.go",
}

// TestCloudCommandsHonorDryRun pins that every hyperscaler create/update
// command checks the global dryRun flag before it mutates.
//
// --dry-run is a persistent root flag, so cobra accepts it on every command
// whether or not the command reads it. All six of these files used to ignore
// it: `dtctl create aws monitoring --dry-run` resolved the connection, fetched
// the extension version and then POSTed for real. The safety checker still
// gated the operation, so a readonly context was safe — but on a readwrite
// context a flag whose whole purpose is "change nothing" created a
// configuration. Nothing in a type signature prevents the next command from
// reintroducing that, hence this guard.
func TestCloudCommandsHonorDryRun(t *testing.T) {
	for _, name := range cloudMutatingFiles {
		t.Run(name, func(t *testing.T) {
			fset := token.NewFileSet()
			file, err := parser.ParseFile(fset, filepath.Clean(name), nil, 0)
			require.NoError(t, err)

			var checked int
			ast.Inspect(file, func(n ast.Node) bool {
				fn, ok := n.(*ast.FuncLit)
				if !ok {
					return true
				}
				for _, call := range mutatingCalls(fn.Body) {
					checked++
					guard := firstDryRunGuard(fn.Body)
					require.NotZero(t, guard,
						"%s:%d calls %s but its enclosing func never reads dryRun() — "+
							"add an `if dryRun() { ... return nil }` block before the call",
						name, fset.Position(call.Pos()).Line, callName(call))
					require.Less(t, guard, call.Pos(),
						"%s:%d calls %s before the dryRun() check at line %d — "+
							"the guard must come first or --dry-run still mutates",
						name, fset.Position(call.Pos()).Line, callName(call),
						fset.Position(guard).Line)
				}
				return true
			})
			require.NotZero(t, checked,
				"%s has no handler Create/Update calls — if the command moved, update cloudMutatingFiles", name)
		})
	}
}

// mutatingCalls finds calls of the form <something>.Create(...) / .Update(...).
func mutatingCalls(body *ast.BlockStmt) []*ast.CallExpr {
	var found []*ast.CallExpr
	ast.Inspect(body, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		if sel, ok := call.Fun.(*ast.SelectorExpr); ok {
			if sel.Sel.Name == "Create" || sel.Sel.Name == "Update" {
				found = append(found, call)
			}
		}
		return true
	})
	return found
}

// firstDryRunGuard returns the position of the earliest `dryRun` reference in
// the body, or token.NoPos when the body never reads it.
func firstDryRunGuard(body *ast.BlockStmt) token.Pos {
	first := token.NoPos
	ast.Inspect(body, func(n ast.Node) bool {
		ident, ok := n.(*ast.Ident)
		if !ok || ident.Name != "dryRun" {
			return true
		}
		if first == token.NoPos || ident.Pos() < first {
			first = ident.Pos()
		}
		return true
	})
	return first
}

func callName(call *ast.CallExpr) string {
	if sel, ok := call.Fun.(*ast.SelectorExpr); ok {
		if recv, ok := sel.X.(*ast.Ident); ok {
			return recv.Name + "." + sel.Sel.Name
		}
		return sel.Sel.Name
	}
	return "call"
}
