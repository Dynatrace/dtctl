package cmd

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// The Live Debugger commands that change something, each on every path that
// reaches a different safety check.
var mutatingBreakpointCommands = []struct {
	name string
	argv []string
}{
	{"update the filters", []string{"update", "breakpoint", "--filters", "k8s.namespace.name:web"}},
	{"update the filters from an entry", []string{"update", "breakpoint", "--repo-scope", "ledger"}},
	{"disable a breakpoint", []string{"update", "breakpoint", "dtctl-rule-0001", "--enabled=false"}},
	{"change a condition", []string{"update", "breakpoint", "dtctl-rule-0001", "--condition", "a==1"}},
	{"delete by id", []string{"delete", "breakpoint", "dtctl-rule-0001"}},
	{"delete by location", []string{"delete", "breakpoint", "A.java:1"}},
	{"delete all", []string{"delete", "breakpoint", "--all"}},
}

// A context that may not update or delete refuses every such breakpoint
// command before anything is sent, a read included. readwrite-mine is such a
// context too: nothing says whose a workspace's breakpoints are.
func TestBreakpoint_MutationsNeedTheirSafetyLevel(t *testing.T) {
	for _, level := range []string{"readonly", "readwrite-mine"} {
		for _, tc := range mutatingBreakpointCommands {
			t.Run(level+"/"+tc.name, func(t *testing.T) {
				srv := newLiveDebuggerStub(t, level)
				srv.rules = []interface{}{ruleAt("A.java", 1, "")}
				chdirTestRepo(t, breakpointScope)

				code, stdout, _ := runRepoScoped(append(tc.argv, "--agent", "--yes"), RunOptions{})
				require.NotZero(t, code, stdout)
				require.Equal(t, "safety_blocked", agentError(t, stdout).Code)
				require.Empty(t, srv.ops(), "a refused command sends nothing")
			})
		}
	}
}

// The same commands run where the level allows them, so the refusals above
// are the safety check's and not a command that fails anyway.
func TestBreakpoint_MutationsRunWhereAllowed(t *testing.T) {
	for _, tc := range mutatingBreakpointCommands {
		t.Run(tc.name, func(t *testing.T) {
			srv := newLiveDebuggerStub(t, "readwrite-all")
			srv.rules = []interface{}{ruleAt("A.java", 1, "")}
			chdirTestRepo(t, breakpointScope)

			code, stdout, stderr := runRepoScoped(append(tc.argv, "--plain", "--yes"), RunOptions{})
			require.Zero(t, code, "stdout: %s\nstderr: %s", stdout, stderr)
			require.Len(t, srv.mutations(), 1, srv.ops())
		})
	}
}

// A dry run of update needs no safety level and sends nothing.
func TestUpdateBreakpoint_DryRunSendsNothing(t *testing.T) {
	for _, tc := range []struct {
		name string
		argv []string
		want string
	}{
		{"one breakpoint", []string{"update", "breakpoint", "dtctl-rule-0001", "--enabled=false"}, "Dry run: would update breakpoint dtctl-rule-0001 (enabled=false)"},
		{"the filters", []string{"update", "breakpoint", "--filters", "k8s.namespace.name:web"}, "'s filters (k8s.namespace.name:web)"},
		{"the filters from an entry", []string{"update", "breakpoint", "--repo-scope", "ledger"}, "'s filters from repo scope ledger (service.name:ledger)"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := newLiveDebuggerStub(t, "readonly")
			chdirTestRepo(t, breakpointScope)

			code, stdout, stderr := runRepoScoped(append(tc.argv, "--plain", "--dry-run"), RunOptions{})
			require.Zero(t, code, stderr)
			require.Contains(t, breakpointMessage(t, stdout), tc.want)
			require.Empty(t, srv.ops())
		})
	}
}

// A confirmation that is declined changes nothing: neither the filters nor,
// on create, the breakpoint that was to follow them.
func TestBreakpoint_DeclinedFilterChangeSendsNoWrite(t *testing.T) {
	for _, tc := range []struct {
		name string
		argv []string
	}{
		{"create with --filters", []string{"create", "breakpoint", "A.java:1", "--filters", "k8s.namespace.name:web"}},
		{"create with --repo-scope", []string{"create", "breakpoint", "A.java:1", "--repo-scope", "ledger"}},
		{"create by default", []string{"create", "breakpoint", "A.java:1"}},
		{"update with --filters", []string{"update", "breakpoint", "--filters", "k8s.namespace.name:web"}},
		{"update with --repo-scope", []string{"update", "breakpoint", "--repo-scope", "ledger"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := newLiveDebuggerStub(t, "readwrite-all")
			srv.activeRules = 1
			chdirTestRepo(t, breakpointScope)

			code, stdout, stderr := runRepoScoped(append(tc.argv, "--no-agent"), RunOptions{Stdin: strings.NewReader("n\n")})
			require.Zero(t, code, stderr)
			require.Contains(t, stdout, "for 1 active breakpoint. Continue?")
			require.Contains(t, stdout, "Cancelled")
			require.Empty(t, srv.mutations())
		})
	}
}

// --repo-scope names the workspace on the commands that set no filters from
// it, wherever they run: each asks for <repo>/<name> and works in it.
func TestBreakpoint_RepoScopeNamesTheWorkspaceOfEveryCommand(t *testing.T) {
	for _, tc := range []struct {
		name string
		argv []string
		ops  []string
	}{
		{"delete", []string{"delete", "breakpoint", "A.java:1", "--yes"}, []string{"GetOrCreateWorkspaceV2", "GetWorkspaceRules", "DeleteRule"}},
		{"delete all", []string{"delete", "breakpoint", "--all", "--yes"}, []string{"GetOrCreateWorkspaceV2", "GetWorkspaceRules", "DeleteAllRulesFromWorkspace"}},
		{"describe", []string{"describe", "breakpoint", "A.java:1"}, []string{"GetOrCreateWorkspaceV2", "GetWorkspaceRules", "GetRuleStatusBreakdown"}},
		{"get snapshots", []string{"get", "snapshots", "A.java:1"}, []string{"GetOrCreateWorkspaceV2", "GetWorkspaceRules"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := newLiveDebuggerStub(t, "readwrite-all")
			srv.rules = []interface{}{ruleAt("A.java", 1, "")}
			root := chdirTestRepo(t, breakpointScope)
			require.NoError(t, os.WriteFile(filepath.Join(root, ".git", "config"),
				[]byte("[remote \"origin\"]\n\turl = git@example.invalid:shop/storefront.git\n"), 0o644))
			chdirSubdir(t, filepath.Join("services", "ledger"))

			code, stdout, stderr := runRepoScoped(append(tc.argv, "--plain", "--repo-scope", "regions"), RunOptions{})
			require.Zero(t, code, "stdout: %s\nstderr: %s", stdout, stderr)
			require.Equal(t, tc.ops, srv.ops())
			require.Equal(t, []string{"storefront/regions"}, srv.projectPaths())
			require.Equal(t, []string{"storefront/regions"}, srv.workedIn())

			srv.reset()
			code, stdout, _ = runRepoScoped(append(tc.argv, "--agent", "--repo-scope", "nope"), RunOptions{})
			require.NotZero(t, code)
			require.Equal(t, "not_found", agentError(t, stdout).Code)
			require.Empty(t, srv.ops())
		})
	}
}

// Creating a breakpoint is a create: a context that may create but not
// update gets its breakpoint in a workspace whose filters stay as they are,
// and a context that may only read gets nothing.
func TestCreateBreakpoint_NeedsOnlyTheCreateLevel(t *testing.T) {
	srv := newLiveDebuggerStub(t, "readwrite-mine", webSetJSON)
	chdirTestRepo(t, "")

	code, stdout, stderr := runRepoScoped([]string{"create", "breakpoint", "A.java:1", "--plain"}, RunOptions{})
	require.Zero(t, code, "stdout: %s\nstderr: %s", stdout, stderr)
	require.Equal(t, []string{"GetOrCreateWorkspaceV2", "CreateRule"}, srv.ops())

	srv = newLiveDebuggerStub(t, "readonly", webSetJSON)
	code, stdout, _ = runRepoScoped([]string{"create", "breakpoint", "A.java:1", "--agent"}, RunOptions{})
	require.NotZero(t, code)
	require.Equal(t, "safety_blocked", agentError(t, stdout).Code)
	require.Empty(t, srv.ops())
}

// Setting a workspace's filters is an update, whichever command does it:
// they re-scope every breakpoint in the workspace. A context that may not
// update refuses a create that would set them, as it refuses update
// breakpoint --filters, before the entry is read or anything is sent.
func TestCreateBreakpoint_SettingFiltersNeedsTheUpdateLevel(t *testing.T) {
	for _, level := range []string{"readonly", "readwrite-mine"} {
		for _, tc := range []struct {
			name string
			args []string
			// reads are the requests a refused run has sent.
			reads []string
		}{
			{"--filters", []string{"--filters", "k8s.namespace.name:web"}, []string{}},
			{"--repo-scope", []string{"--repo-scope", "split"}, []string{}},
			{"the default", nil, []string{"GetOrCreateWorkspaceV2"}},
		} {
			for _, yes := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/%s/yes=%t", level, tc.name, yes), func(t *testing.T) {
					if level == "readonly" && tc.args == nil {
						t.Skip("refused as a create, see TestCreateBreakpoint_NeedsOnlyTheCreateLevel")
					}
					srv := newLiveDebuggerStub(t, level)
					chdirTestRepo(t, breakpointScope)
					argv := append([]string{"create", "breakpoint", "A.java:1", "--agent"}, tc.args...)
					if yes {
						argv = append(argv, "--yes")
					}

					code, stdout, stderr := runRepoScoped(argv, RunOptions{})
					require.NotZero(t, code, stdout)
					require.Equal(t, "safety_blocked", agentError(t, stdout).Code)
					require.Equal(t, tc.reads, append([]string{}, srv.ops()...))
					require.Empty(t, stderr, "a refused run has said nothing about the entry")
				})
			}
		}
	}
}

// A dry run of update previews a change in one context's environment, so a
// context that does not exist fails it as it fails the real run.
func TestUpdateBreakpoint_DryRunNeedsItsContext(t *testing.T) {
	for _, argv := range [][]string{
		{"update", "breakpoint", "--filters", "k8s.namespace.name:web"},
		{"update", "breakpoint", "dtctl-rule-0001", "--enabled=false"},
	} {
		srv := newLiveDebuggerStub(t, "readwrite-all")
		chdirTestRepo(t, breakpointScope)

		code, stdout, _ := runRepoScoped(append(argv, "--agent", "--dry-run", "--context", "nope"), RunOptions{})
		require.NotZero(t, code, stdout)
		require.Contains(t, agentError(t, stdout).Message, `"nope"`)
		require.Empty(t, srv.ops())
	}
}
