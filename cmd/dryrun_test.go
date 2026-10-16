package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// withAgentMode sets agentMode for one test and restores it.
func withAgentMode(t *testing.T, on bool) {
	t.Helper()
	orig := agentMode(context.Background())
	gFlags.agentMode = on
	t.Cleanup(func() { gFlags.agentMode = orig })
}

func TestDryRunReport_HumanOutputIsPlainLines(t *testing.T) {
	withAgentMode(t, false)
	cmd, _, err := rootCmd.Find([]string{"create", "bucket"})
	require.NoError(t, err)

	out := captureScopeStdout(t, func() {
		require.NoError(t, newDryRunReport(cmd).
			Linef("Dry run: would create bucket").
			Field("Name", "%s", "my_bucket").
			Field("Retention", "%d days", 35).
			Payload([]byte(`{"bucketName":"my_bucket"}`)).
			Print())
	})

	// One line per entry, in order, and nothing else: no JSON, and the payload
	// stays out of the human rendering unless a line asked for it.
	require.Equal(t, "Dry run: would create bucket\nName: my_bucket\nRetention: 35 days\n", out)
}

func TestDryRunReport_AgentOutputIsAnEnvelope(t *testing.T) {
	withAgentMode(t, true)
	cmd, _, err := rootCmd.Find([]string{"create", "bucket"})
	require.NoError(t, err)

	out := captureScopeStdout(t, func() {
		require.NoError(t, newDryRunReport(cmd).
			Linef("Dry run: would create bucket").
			Field("Display Name", "%s", "My Bucket").
			Detail("table", "%s", "logs").
			Payload([]byte(`{"bucketName":"my_bucket"}`)).
			Print())
	})

	var resp struct {
		OK     bool `json:"ok"`
		Result struct {
			DryRun   bool              `json:"dry_run"`
			Verb     string            `json:"verb"`
			Resource string            `json:"resource"`
			Details  map[string]string `json:"details"`
			Payload  json.RawMessage   `json:"payload"`
			Message  string            `json:"message"`
		} `json:"result"`
		Context struct {
			Verb     string `json:"verb"`
			Resource string `json:"resource"`
		} `json:"context"`
	}
	require.NoError(t, json.Unmarshal([]byte(out), &resp), "agent output must be a single JSON document")

	require.True(t, resp.OK, "a dry run succeeded; it is not an error")
	require.True(t, resp.Result.DryRun)
	require.Equal(t, "create", resp.Result.Verb)
	require.Equal(t, "bucket", resp.Result.Resource)
	require.Equal(t, "create", resp.Context.Verb)
	require.Equal(t, "bucket", resp.Context.Resource)

	// Field labels become snake_case keys; Detail contributes without a line.
	require.Equal(t, map[string]string{"display_name": "My Bucket", "table": "logs"}, resp.Result.Details)

	// The payload is JSON, not a string, so it can be diffed against the real request.
	require.JSONEq(t, `{"bucketName":"my_bucket"}`, string(resp.Result.Payload))

	// The human text is preserved verbatim, so nothing the prose says is lost.
	require.Equal(t, "Dry run: would create bucket\nDisplay Name: My Bucket", resp.Result.Message)

	// And it goes out compact, like every other envelope on a non-terminal
	// stdout. Print() routes through output.EncodeEnvelope for exactly this: a
	// local encoder with SetIndent would have spent a third more tokens on
	// whitespace for the audience the envelope exists to serve.
	require.NotContains(t, out, "\n  ", "a piped envelope must be compact, not indented")
	require.Equal(t, 1, strings.Count(out, "\n"), "compact JSON is one line plus its terminator")
}

func TestDryRunReport_InvalidPayloadIsDropped(t *testing.T) {
	withAgentMode(t, true)
	cmd, _, err := rootCmd.Find([]string{"create", "workflow"})
	require.NoError(t, err)

	out := captureScopeStdout(t, func() {
		require.NoError(t, newDryRunReport(cmd).
			Linef("Dry run: would create workflow").
			Payload([]byte("not json")).
			Print())
	})

	// An unparseable payload must not make the envelope unparseable.
	var resp map[string]interface{}
	require.NoError(t, json.Unmarshal([]byte(out), &resp))
	result, ok := resp["result"].(map[string]interface{})
	require.True(t, ok)
	require.NotContains(t, result, "payload")
}

func TestDetailKey(t *testing.T) {
	for label, want := range map[string]string{
		"Name":                     "name",
		"Display Name":             "display_name",
		"Lookup Field":             "lookup_field",
		"Config ID":                "config_id",
		"File Size":                "file_size",
		"Referenced by context(s)": "referenced_by_contexts",
	} {
		require.Equal(t, want, detailKey(label), "label %q", label)
	}
}

// dryRunBranch is the code a dry run executes: the body of `if dryRun { ... }`,
// or the else of `if !dryRun { ... } else { ... }`.
type dryRunBranch struct {
	file  string
	line  int
	block *ast.BlockStmt
}

// dryRunBranches finds every dry-run branch in the non-test files of cmd/, and
// indexes the package's top-level functions so a branch can be followed into
// the helpers it calls.
func dryRunBranches(t *testing.T) ([]dryRunBranch, map[string]*ast.FuncDecl, *token.FileSet) {
	t.Helper()
	entries, err := os.ReadDir(".")
	require.NoError(t, err)

	fset := token.NewFileSet()
	funcs := map[string]*ast.FuncDecl{}
	var branches []dryRunBranch
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		file, perr := parser.ParseFile(fset, filepath.Clean(name), nil, 0)
		require.NoError(t, perr)

		for _, decl := range file.Decls {
			if fn, ok := decl.(*ast.FuncDecl); ok && fn.Recv == nil && fn.Body != nil {
				funcs[fn.Name.Name] = fn
			}
		}
		ast.Inspect(file, func(n ast.Node) bool {
			ifStmt, ok := n.(*ast.IfStmt)
			if !ok || !mentionsDryRun(ifStmt.Cond) {
				return true
			}
			block := ifStmt.Body
			switch {
			case isNotDryRun(ifStmt.Cond):
				// `if !dryRun { real work }`: the dry run is the else, if any.
				block, _ = ifStmt.Else.(*ast.BlockStmt)
			case negatesDryRun(ifStmt.Cond):
				// `if x && !dryRun { real work }`: neither branch is the dry run.
				block = nil
			}
			if block != nil {
				branches = append(branches, dryRunBranch{name, fset.Position(ifStmt.Pos()).Line, block})
			}
			return true
		})
	}
	return branches, funcs, fset
}

// dryRunReach is what a dry-run branch does, including in the package helpers
// it calls (followed transitively, each at most once).
type dryRunReach struct {
	stdoutPrints []string // "file:line callee"
	infoPrints   []string // "file:line" of output.PrintInfo
	agentAware   bool     // builds a report, an agent printer, or reads agentMode
}

func reachOf(block *ast.BlockStmt, funcs map[string]*ast.FuncDecl, fset *token.FileSet) dryRunReach {
	var r dryRunReach
	visited := map[string]bool{}
	var walk func(ast.Node)
	walk = func(node ast.Node) {
		ast.Inspect(node, func(n ast.Node) bool {
			switch n := n.(type) {
			case *ast.Ident:
				if n.Name == "agentMode" {
					r.agentAware = true
				}
			case *ast.CallExpr:
				pos := fset.Position(n.Pos())
				where := fmt.Sprintf("%s:%d", filepath.Base(pos.Filename), pos.Line)
				if callee := stdoutPrintCallee(n); callee != "" {
					r.stdoutPrints = append(r.stdoutPrints, where+" "+callee)
				}
				if isQualifiedCall(n, "output", "PrintInfo") {
					r.infoPrints = append(r.infoPrints, where)
				}
				if ident, ok := n.Fun.(*ast.Ident); ok {
					switch ident.Name {
					case "newDryRunReport", "deleteDryRun", "enrichAgent", "NewPrinter":
						r.agentAware = true
					}
					if fn, ok := funcs[ident.Name]; ok && !visited[ident.Name] {
						visited[ident.Name] = true
						walk(fn.Body)
					}
				}
			}
			return true
		})
	}
	walk(block)
	return r
}

// TestDryRunBranchesDoNotPrintToStdout guards the invariant dryRunReport
// established: a dry-run branch that prints with fmt.Print* puts prose on
// stdout, which in agent mode is the stream the caller decodes as JSON — so the
// dry run became the one outcome an agent could not read, while errors from the
// same command arrived correctly enveloped.
//
// The scan is lexical but follows calls: it finds the dry-run branches and the
// print calls inside them and inside every package helper they reach, so a
// branch that delegates its printing (exec api's printAPIDryRun did) is held to
// the same rule as one that prints inline.
func TestDryRunBranchesDoNotPrintToStdout(t *testing.T) {
	branches, funcs, fset := dryRunBranches(t)
	require.NotEmpty(t, branches, "the scan found no dry-run branches; it is not looking where the code is")

	for _, b := range branches {
		for _, p := range reachOf(b.block, funcs, fset).stdoutPrints {
			t.Errorf("%s:%d: dry-run branch reaches %s — build a dryRunReport and return its Print() so agent mode gets an envelope",
				b.file, b.line, p)
		}
	}
}

// TestDryRunBranchesPutThePlanInTheResult catches the quieter form of the same
// failure (#514): a dry run that writes its plan only with output.PrintInfo.
// That is stderr, so stdout stays clean — but in agent mode it stays *empty*:
// the agent is told nothing about what would have happened, and the prose it
// would need is on the stream it does not parse.
//
// A branch may use PrintInfo as long as it also renders for agent mode: through
// a dryRunReport (OnStderr keeps the human lines on stderr), or by checking
// agentMode / building an agent printer itself.
func TestDryRunBranchesPutThePlanInTheResult(t *testing.T) {
	branches, funcs, fset := dryRunBranches(t)
	for _, b := range branches {
		reach := reachOf(b.block, funcs, fset)
		if len(reach.infoPrints) > 0 && !reach.agentAware {
			t.Errorf("%s:%d: dry-run branch writes its plan only with output.PrintInfo (%s), so agent mode gets no result — "+
				"build a dryRunReport (OnStderr() keeps the human lines on stderr) and return its Print()",
				b.file, b.line, strings.Join(reach.infoPrints, ", "))
		}
	}
}

// isNotDryRun reports whether a condition is exactly `!dryRun`.
func isNotDryRun(expr ast.Expr) bool {
	unary, ok := expr.(*ast.UnaryExpr)
	if !ok || unary.Op != token.NOT {
		return false
	}
	ident, ok := unary.X.(*ast.Ident)
	return ok && ident.Name == "dryRun"
}

// negatesDryRun reports whether a condition contains `!dryRun` anywhere.
func negatesDryRun(expr ast.Expr) bool {
	found := false
	ast.Inspect(expr, func(n ast.Node) bool {
		if e, ok := n.(ast.Expr); ok && isNotDryRun(e) {
			found = true
		}
		return !found
	})
	return found
}

// isQualifiedCall reports whether call is pkg.name(...).
func isQualifiedCall(call *ast.CallExpr, pkg, name string) bool {
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok || sel.Sel.Name != name {
		return false
	}
	ident, ok := sel.X.(*ast.Ident)
	return ok && ident.Name == pkg
}

// mentionsDryRun reports whether an expression reads the dryRun flag.
func mentionsDryRun(expr ast.Expr) bool {
	found := false
	ast.Inspect(expr, func(n ast.Node) bool {
		if ident, ok := n.(*ast.Ident); ok && ident.Name == "dryRun" {
			found = true
		}
		return !found
	})
	return found
}

// stdoutPrintCallee names the call if it writes to stdout, and returns "" if it
// does not. Writes to stderr (output.PrintInfo, output.PrintWarning) are fine:
// stdout is the stream an agent decodes, and diagnostics have always gone to
// stderr.
func stdoutPrintCallee(call *ast.CallExpr) string {
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return ""
	}
	pkg, ok := sel.X.(*ast.Ident)
	if !ok {
		return ""
	}
	qualified := pkg.Name + "." + sel.Sel.Name
	switch qualified {
	case "fmt.Print", "fmt.Printf", "fmt.Println", "output.DescribeKV":
		return qualified
	case "fmt.Fprint", "fmt.Fprintf", "fmt.Fprintln":
		if len(call.Args) > 0 && isOsStdout(call.Args[0]) {
			return qualified
		}
	}
	return ""
}

// isOsStdout reports whether an expression is os.Stdout.
func isOsStdout(expr ast.Expr) bool {
	sel, ok := expr.(*ast.SelectorExpr)
	if !ok || sel.Sel.Name != "Stdout" {
		return false
	}
	pkg, ok := sel.X.(*ast.Ident)
	return ok && pkg.Name == "os"
}
