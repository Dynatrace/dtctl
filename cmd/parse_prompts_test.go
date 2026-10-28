package cmd

import (
	"context"
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/require"
)

// TestPromptCallSitesAreKnown pins the places cmd/ asks for confirmation on
// stdin. A command that asks reads stdin, so Parse has to report it as
// RequiresInput (markPrompts); a new call site fails here until the command
// that reaches it is declared and the site is listed.
func TestPromptCallSitesAreKnown(t *testing.T) {
	known := map[string]string{
		"auth.go:authConfirmFunc":                              "auth login's consent prompt: asks only when stdin is a terminal file, which a service never has, and auth is blocked in the engine",
		"usage.go:runUsageReset":                               "usage reset --yes",
		"create_breakpoints.go:newCreateBreakpointCmd":         "--yes",
		"update_breakpoints.go:newUpdateBreakpointCmd":         "--yes",
		"delete_breakpoints.go:runDeleteAllBreakpointsWithOps": "delete breakpoint --yes",
		"delete_breakpoints.go:runDeleteBreakpointRowsWithOps": "delete breakpoint --yes",
		"get_anomalydetector.go:newDeleteAnomalyDetectorCmd":   "--yes",
		"get_apps.go:newDeleteAppCmd":                          "--yes",
		"get_buckets.go:newDeleteBucketCmd":                    "--yes",
		"get_documents.go:newDeleteDashboardCmd":               "--yes",
		"get_documents.go:newDeleteNotebookCmd":                "--yes",
		"get_documents.go:newDeleteTrashCmd":                   "--yes",
		"get_documents.go:newDeleteDocumentCmd":                "--yes",
		"get_edgeconnects.go:newDeleteEdgeConnectCmd":          "--yes",
		"get_lookups.go:newDeleteLookupCmd":                    "--yes",
		"get_notifications.go:newDeleteNotificationCmd":        "--yes",
		"get_scheduling_rules.go:newDeleteSchedulingRuleCmd":   "--yes",
		"get_segments.go:newDeleteSegmentCmd":                  "--yes",
		"get_settings.go:newDeleteSettingsCmd":                 "--yes",
		"get_slos.go:newDeleteSLOCmd":                          "--yes",
		"get_workflows.go:newDeleteWorkflowCmd":                "--yes",
		"restore.go:newRestoreWorkflowCmd":                     "--force",
		"restore.go:newRestoreDashboardCmd":                    "--force",
		"restore.go:newRestoreNotebookCmd":                     "--force",
		"restore.go:newRestoreDocumentCmd":                     "--force",
		"restore.go:newRestoreTrashCmd":                        "--force",
	}
	files, err := filepath.Glob("*.go")
	require.NoError(t, err)
	fset := token.NewFileSet()
	found := map[string]bool{}
	for _, name := range files {
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, name, nil, 0)
		require.NoError(t, err)
		for _, decl := range f.Decls {
			ast.Inspect(decl, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				sel, ok := call.Fun.(*ast.SelectorExpr)
				if !ok || !strings.HasPrefix(sel.Sel.Name, "Confirm") {
					return true
				}
				if id, ok := sel.X.(*ast.Ident); !ok || id.Name != "prompt" {
					return true
				}
				site := name + ":" + declName(decl, call.Pos())
				found[site] = true
				if _, ok := known[site]; !ok {
					t.Errorf("%q: %s asks for confirmation", site, fset.Position(call.Pos()))
				}
				return true
			})
		}
	}
	for site := range known {
		if !found[site] {
			t.Errorf("%s no longer asks for confirmation: remove its entry", site)
		}
	}
}

// declName names the function, or the var of a var block, that holds pos.
func declName(decl ast.Decl, pos token.Pos) string {
	switch d := decl.(type) {
	case *ast.FuncDecl:
		return d.Name.Name
	case *ast.GenDecl:
		for _, spec := range d.Specs {
			if v, ok := spec.(*ast.ValueSpec); ok && len(v.Names) > 0 && v.Pos() <= pos && pos < v.End() {
				return v.Names[0].Name
			}
		}
	}
	return ""
}

// TestPromptingCommandsAreDeclared pins which commands carry markPrompts, so
// a rename or a dropped declaration cannot quietly make Parse report that a
// command which asks for confirmation needs no input.
func TestPromptingCommandsAreDeclared(t *testing.T) {
	want := []string{
		"dtctl create breakpoint",
		"dtctl delete anomaly-detector",
		"dtctl delete app",
		"dtctl delete breakpoint",
		"dtctl delete bucket",
		"dtctl delete dashboard",
		"dtctl delete document",
		"dtctl delete edgeconnect",
		"dtctl delete lookup",
		"dtctl delete notebook",
		"dtctl delete notification",
		"dtctl delete scheduling-rule",
		"dtctl delete segment",
		"dtctl delete settings",
		"dtctl delete slo",
		"dtctl delete trash",
		"dtctl delete workflow",
		"dtctl restore dashboard",
		"dtctl restore document",
		"dtctl restore notebook",
		"dtctl restore trash",
		"dtctl restore workflow",
		"dtctl update breakpoint",
		"dtctl usage reset",
	}
	var got []string
	var walk func(c *cobra.Command)
	walk = func(c *cobra.Command) {
		if c.Annotations[annotationPrompts] != "" {
			got = append(got, c.CommandPath())
		}
		for _, sub := range c.Commands() {
			walk(sub)
		}
	}
	walk(newCommandTree(withInvocation(context.Background(), &invocation{concurrent: true})).root)
	slices.Sort(got)
	require.Equal(t, want, got)
}

// TestParseKeepsAGroupsDeclaredRefusal: a group whose own RunE refuses an
// unknown subcommand (skills) is refused by Parse as Run refuses it, not
// recorded as a run.
func TestParseKeepsAGroupsDeclaredRefusal(t *testing.T) {
	for _, argv := range [][]string{{"skills", "bogus"}, {"skills"}} {
		_, err := Parse(argv, ParseOptions{})
		var pe *ParseError
		require.ErrorAs(t, err, &pe, "%v", argv)
	}
	res, err := Parse([]string{"skills", "status"}, ParseOptions{})
	require.NoError(t, err)
	require.Equal(t, "skills", res.Verb)
}
