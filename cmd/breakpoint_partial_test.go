package cmd

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// A create that set the workspace's filters and then failed to create the
// breakpoint has changed something, and says exactly what: the workspace,
// its filters before and after, that nothing was rolled back, and the
// commands that show and restore them. Nothing is rolled back for it: the
// restoring write could fail too, and would re-scope the workspace again.
func TestCreateBreakpoint_SaysWhatItChangedWhenTheCreateFails(t *testing.T) {
	const cause = "live debugger graphql returned errors: rule limit reached"
	for _, tc := range []struct {
		name    string
		seed    []string
		args    []string
		message string // formatted by replacing WS with the workspace
		suggest []string
	}{
		{"--filters over one set", []string{webSetJSON}, []string{"--filters", "k8s.namespace.name:payments"},
			"workspace WS's filters were changed from k8s.namespace.name:web to k8s.namespace.name:payments, then creating the breakpoint at A.java:1 failed: " + cause,
			[]string{
				"the filters were not rolled back: every breakpoint in workspace WS is scoped by them now",
				"to see the workspace and its filters: dtctl get breakpoints -v",
				"to restore the previous filters: dtctl update breakpoint --filters k8s.namespace.name:web",
				"to try again once the cause is fixed: dtctl create breakpoint A.java:1",
			}},
		{"--filters over none", nil, []string{"--filters", "k8s.namespace.name:payments"},
			"workspace WS's filters were set to k8s.namespace.name:payments (it had none), then creating the breakpoint at A.java:1 failed: " + cause,
			[]string{
				"the filters were not rolled back: every breakpoint in workspace WS is scoped by them now",
				"to see the workspace and its filters: dtctl get breakpoints -v",
				"the workspace had no filters before, and no dtctl command clears them",
				"to try again once the cause is fixed: dtctl create breakpoint A.java:1",
			}},
		{"--filters over several sets", []string{splitWorkloadSet, splitServiceSet}, []string{"--filters", "k8s.namespace.name:payments"},
			"workspace WS's filters were changed from k8s.namespace.name:payments,k8s.workload.name:split; service.name:split to k8s.namespace.name:payments, then creating the breakpoint at A.java:1 failed: " + cause,
			[]string{
				"the filters were not rolled back: every breakpoint in workspace WS is scoped by them now",
				"to see the workspace and its filters: dtctl get breakpoints -v",
				"the previous filters were several sets, which --filters cannot spell: set them again from the repo scope entry that renders them, with dtctl update breakpoint --repo-scope <name>",
				"to try again once the cause is fixed: dtctl create breakpoint A.java:1",
			}},
		{"--repo-scope over one set", []string{webSetJSON}, []string{"--repo-scope", "ledger"},
			"workspace WS's filters were changed from k8s.namespace.name:web to service.name:ledger (repo scope ledger), then creating the breakpoint at A.java:1 failed: " + cause,
			[]string{
				"the filters were not rolled back: every breakpoint in workspace WS is scoped by them now",
				"to see the workspace and its filters: dtctl get breakpoints -v --repo-scope ledger",
				"the previous filters are in this message; no single command restores them in a workspace --repo-scope named",
				"to try again once the cause is fixed: dtctl create breakpoint A.java:1 --repo-scope ledger",
			}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// run is the command against a fresh stub in a fresh checkout.
			run := func(mode ...string) (srv *liveDebuggerStub, ws, stdout, stderr string) {
				srv = newLiveDebuggerStub(t, "readwrite-all", tc.seed...)
				srv.fail["CreateRule"] = "rule limit reached"
				ws = filepath.Base(chdirTestRepo(t, breakpointScope)) + "/checkout"
				if tc.args[0] == "--repo-scope" {
					ws = filepath.Dir(ws) + "/" + tc.args[1]
				}
				code, stdout, stderr := runRepoScoped(append(append([]string{"create", "breakpoint", "A.java:1"}, mode...), tc.args...), RunOptions{})
				require.NotZero(t, code)
				return srv, ws, stdout, stderr
			}

			srv, ws, stdout, _ := run("--agent")
			in := func(s string) string { return strings.ReplaceAll(s, "WS", ws) }
			detail := agentError(t, stdout)
			require.Equal(t, in(tc.message), detail.Message)
			for i, s := range tc.suggest {
				require.Equal(t, in(s), detail.Suggestions[i])
			}
			require.Len(t, detail.Suggestions, len(tc.suggest))
			require.Equal(t, []string{"GetOrCreateWorkspaceV2", "UpdateWorkspaceV2", "CreateRule"}, srv.ops(), "nothing is rolled back")

			// A person gets the same, in the error.
			_, ws, stdout, stderr := run("--no-agent", "--yes")
			require.Contains(t, stdout+stderr, in(tc.message+"\n  "+strings.Join(tc.suggest, "\n  ")))
		})
	}
}

// A create that changed no filters fails with the cause alone.
func TestCreateBreakpoint_AFailedCreateThatChangedNothingSaysOnlyWhy(t *testing.T) {
	srv := newLiveDebuggerStub(t, "readwrite-all", webSetJSON)
	srv.fail["CreateRule"] = "rule limit reached"
	chdirTestRepo(t, "")

	code, stdout, _ := runRepoScoped([]string{"create", "breakpoint", "A.java:1", "--agent"}, RunOptions{})
	require.NotZero(t, code)
	require.Equal(t, "live debugger graphql returned errors: rule limit reached", agentError(t, stdout).Message)
}
