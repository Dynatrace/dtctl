package cmd

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
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
		}{
			{"--filters", []string{"--filters", "k8s.namespace.name:web"}},
			{"--repo-scope", []string{"--repo-scope", "split"}},
		} {
			for _, yes := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/%s/yes=%t", level, tc.name, yes), func(t *testing.T) {
					srv := newLiveDebuggerStub(t, level)
					chdirTestRepo(t, breakpointScope)
					argv := append([]string{"create", "breakpoint", "A.java:1", "--agent"}, tc.args...)
					if yes {
						argv = append(argv, "--yes")
					}

					code, stdout, stderr := runRepoScoped(argv, RunOptions{})
					require.NotZero(t, code, stdout)
					require.Equal(t, "safety_blocked", agentError(t, stdout).Code)
					require.Empty(t, srv.ops())
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

// A dry run names the workspace the real run would work in, which the
// context's environment decides. A context that does not exist, or one
// without an environment, fails it as it fails the real run, rather than
// previewing the working directory's workspace: with a scope file, without
// one, and with one that does not load, whose refusal it must not skip.
func TestBreakpoint_DryRunNeedsItsEnvironment(t *testing.T) {
	for _, argv := range [][]string{
		{"create", "breakpoint", "A.java:1"},
		{"create", "breakpoint", "A.java:1", "--filters", "k8s.namespace.name:web"},
		{"update", "breakpoint", "--filters", "k8s.namespace.name:web"},
		{"update", "breakpoint", "dtctl-rule-0001", "--enabled=false"},
	} {
		for name, scope := range map[string]string{"linked": breakpointScope, "no file": "", "broken file": brokenScope} {
			t.Run(strings.Join(argv, " ")+"/"+name, func(t *testing.T) {
				srv := newLiveDebuggerStub(t, "readwrite-all")
				chdirTestRepo(t, scope)

				code, stdout, _ := runRepoScoped(append(slices.Clone(argv), "--agent", "--dry-run", "--context", "nope"), RunOptions{})
				require.NotZero(t, code, stdout)
				require.Contains(t, agentError(t, stdout).Message, `context "nope" not found`)

				writeIsolatedConfig(t, safetyLevelConfig(`""`, "readwrite-all"))
				code, stdout, _ = runRepoScoped(append(slices.Clone(argv), "--agent", "--dry-run"), RunOptions{})
				require.NotZero(t, code, stdout)
				require.Equal(t, `context "c" has no environment URL, and a Live Debugger workspace is one environment's`, agentError(t, stdout).Message)
				require.Empty(t, srv.ops())
			})
		}
	}
}

// Without any config a create's dry run still says what it would do, naming
// no workspace: a preview never newly requires a config.
func TestCreateBreakpoint_DryRunNeedsNoConfig(t *testing.T) {
	writeIsolatedConfig(t, "")
	t.Setenv("DTCTL_CONFIG", filepath.Join(t.TempDir(), "absent.yaml"))
	chdirTestRepo(t, breakpointScope)

	code, stdout, stderr := runRepoScoped([]string{"create", "breakpoint", "A.java:1", "--plain", "--dry-run"}, RunOptions{})
	require.Zero(t, code, stderr)
	require.Equal(t, "Dry run: would create breakpoint at A.java:1", breakpointMessage(t, stdout))
}

// "--enabled false" with no identifier before it leaves the flag bare and
// "false" as the only argument, where the breakpoint identifier goes. A lone
// argument that reads as a boolean and stands after the flag is that
// mistake and names no breakpoint, so nothing is sent.
//
// An identifier is whatever the rule was created with, so "1" or "t" can be
// one. Before the flag it is the identifier and is never refused. After it,
// "--enabled 1" and "--enabled=true 1" cannot be told from the mistake once
// the flags are parsed, so both are refused and the message says where such
// an identifier goes.
func TestUpdateBreakpoint_EnabledValueIsNotAnIdentifier(t *testing.T) {
	for _, argv := range [][]string{
		{"update", "breakpoint", "--enabled", "false"},
		{"update", "breakpoint", "--repo-scope", "checkout", "--enabled", "false"},
		{"update", "breakpoint", "--enabled", "TRUE"},
		{"update", "breakpoint", "--enabled", "1"},
		{"update", "breakpoint", "--enabled", "--condition", "a==1", "f"},
		{"update", "breakpoint", "--enabled=true", "1"},
		// Two arguments after the flag: the first is where its value would
		// be, and reading it as the identifier would enable a breakpoint
		// called "false" for a caller who meant to disable breakpoint 1.
		{"update", "breakpoint", "--enabled", "false", "1"},
	} {
		t.Run(strings.Join(argv[2:], " "), func(t *testing.T) {
			srv := newLiveDebuggerStub(t, "readwrite-all")
			chdirTestRepo(t, breakpointScope)

			code, stdout, _ := runRepoScoped(append(argv, "--agent"), RunOptions{})
			require.Equal(t, 2, code, stdout)
			detail := agentError(t, stdout)
			require.Equal(t, "validation_error", detail.Code)
			token := argv[len(argv)-1]
			if token == "1" && argv[len(argv)-2] == "false" {
				token = "false"
			}
			value, err := strconv.ParseBool(token)
			require.NoError(t, err)
			require.Equal(t, fmt.Sprintf(`%q follows --enabled, where it reads as the flag's value, so no breakpoint is named: name it first, as in dtctl update breakpoint <id|filename:line> --enabled=%t; a breakpoint whose identifier is %q goes before the flag as well`,
				token, value, token), detail.Message)
			require.Empty(t, srv.ops())
		})
	}

	// The spellings that name a breakpoint still do, an identifier that
	// reads as a boolean among them.
	for _, tc := range []struct {
		argv    []string
		id      string
		enabled bool
	}{
		{[]string{"update", "breakpoint", "dtctl-rule-0001", "--enabled", "false"}, "dtctl-rule-0001", false},
		{[]string{"update", "breakpoint", "dtctl-rule-0001", "--enabled=false"}, "dtctl-rule-0001", false},
		{[]string{"update", "breakpoint", "dtctl-rule-0001", "--enabled"}, "dtctl-rule-0001", true},
		{[]string{"update", "breakpoint", "1", "--enabled"}, "1", true},
		{[]string{"update", "breakpoint", "t", "--enabled=false"}, "t", false},
		{[]string{"update", "breakpoint", "true", "--enabled", "false"}, "true", false},
		{[]string{"update", "breakpoint", "0", "--condition", "a==1", "--enabled"}, "0", true},
	} {
		t.Run(strings.Join(tc.argv[2:], " "), func(t *testing.T) {
			srv := newLiveDebuggerStub(t, "readwrite-all")
			srv.rules = []interface{}{ruleAt("A.java", 1, "")}
			chdirTestRepo(t, breakpointScope)
			code, stdout, stderr := runRepoScoped(append(tc.argv, "--plain", "--dry-run"), RunOptions{})
			require.Zero(t, code, "stdout: %s\nstderr: %s", stdout, stderr)
			require.Contains(t, breakpointMessage(t, stdout), fmt.Sprintf("Dry run: would update breakpoint %s (", tc.id))
			require.Contains(t, breakpointMessage(t, stdout), fmt.Sprintf("enabled=%t)", tc.enabled))
		})
	}
}

// What --enabled sends is what was typed: the rule named, and disabled
// exactly when the value is false. A value written with "=" is the flag's
// own, so the one argument beside it is the identifier wherever it stands,
// one that reads as a boolean included.
func TestUpdateBreakpoint_EnabledSendsWhatWasTyped(t *testing.T) {
	for _, tc := range []struct {
		argv     []string
		id       string
		disabled bool
	}{
		{[]string{"dtctl-rule-0001", "--enabled", "false"}, "dtctl-rule-0001", true},
		{[]string{"dtctl-rule-0001", "--enabled=false"}, "dtctl-rule-0001", true},
		{[]string{"dtctl-rule-0001", "--enabled"}, "dtctl-rule-0001", false},
		{[]string{"dtctl-rule-0001", "--enabled", "true"}, "dtctl-rule-0001", false},
		{[]string{"dtctl-rule-0001", "--enabled", "--", "false"}, "dtctl-rule-0001", true},
		{[]string{"--enabled=false", "1"}, "1", true},
		{[]string{"--enabled=0", "true"}, "true", true},
		{[]string{"1", "--enabled=false"}, "1", true},
		{[]string{"--enabled", "dtctl-rule-0001"}, "dtctl-rule-0001", false},
	} {
		t.Run(strings.Join(tc.argv, " "), func(t *testing.T) {
			srv := newLiveDebuggerStub(t, "readwrite-all")
			srv.rules = []interface{}{ruleAt("A.java", 1, "")}
			chdirTestRepo(t, breakpointScope)

			code, stdout, stderr := runRepoScoped(slices.Concat([]string{"--plain", "update", "breakpoint"}, tc.argv), RunOptions{})
			require.Zero(t, code, "stdout: %s\nstderr: %s", stdout, stderr)
			require.Equal(t, []string{"EnableOrDisableRules"}, srv.mutations())
			sent := srv.variablesOf("EnableOrDisableRules")[0]
			require.Equal(t, []interface{}{tc.id}, sent["rulesIds"])
			require.Equal(t, tc.disabled, sent["isDisabled"])
			require.Contains(t, breakpointMessage(t, stdout), fmt.Sprintf("Updated breakpoint %s (enabled=%t)", tc.id, !tc.disabled))
		})
	}
}

// A second argument is the value of --enabled only where the documented
// spelling puts it: after a bare flag that follows the one identifier.
// Anywhere else it is one argument too many, and nothing is sent: read as
// the value, it would override the one the flag carries, or enable what the
// caller meant to disable.
func TestUpdateBreakpoint_EnabledTakesNoStrayValue(t *testing.T) {
	for _, tc := range []struct {
		argv []string
		want string
	}{
		{[]string{"dtctl-rule-0001", "1", "--enabled=false"}, `--enabled=false carries its value, so one breakpoint identifier is expected beside it, and 2 arguments were given`},
		{[]string{"dtctl-rule-0001", "--enabled=false", "true"}, `--enabled=false carries its value, so one breakpoint identifier is expected beside it, and 2 arguments were given`},
		{[]string{"dtctl-rule-0001", "--enabled=bogus", "true"}, `invalid boolean value "bogus" for --enabled`},
		{[]string{"--enabled=bogus", "1"}, `invalid boolean value "bogus" for --enabled`},
		{[]string{"dtctl-rule-0001", "0", "--enabled"}, `2 arguments were given, and --enabled takes a value only right after the one breakpoint identifier, as in dtctl update breakpoint <id|filename:line> --enabled false`},
		{[]string{"--enabled", "dtctl-rule-0001", "false"}, `2 arguments were given, and --enabled takes a value only right after the one breakpoint identifier, as in dtctl update breakpoint <id|filename:line> --enabled false`},
	} {
		t.Run(strings.Join(tc.argv, " "), func(t *testing.T) {
			srv := newLiveDebuggerStub(t, "readwrite-all")
			srv.rules = []interface{}{ruleAt("A.java", 1, "")}
			chdirTestRepo(t, breakpointScope)

			code, stdout, _ := runRepoScoped(slices.Concat([]string{"update", "breakpoint"}, tc.argv, []string{"--agent"}), RunOptions{})
			require.Equal(t, 2, code, stdout)
			require.Equal(t, "validation_error", agentError(t, stdout).Code)
			require.Equal(t, tc.want, agentError(t, stdout).Message)
			require.Empty(t, srv.ops())
		})
	}
}

// --filters with --enabled is refused for what it is, two changes in one
// call, whatever follows the flag.
func TestUpdateBreakpoint_FiltersWithEnabledIsTwoChanges(t *testing.T) {
	srv := newLiveDebuggerStub(t, "readwrite-all")
	chdirTestRepo(t, breakpointScope)

	code, stdout, _ := runRepoScoped([]string{"update", "breakpoint", "--filters", "k8s.namespace.name:web", "--enabled", "false", "--agent"}, RunOptions{})
	require.NotZero(t, code)
	require.Equal(t, "--filters sets the workspace's filters, shared by every breakpoint in it; change --condition, --log-message, or --enabled in a separate call", agentError(t, stdout).Message)
	require.Empty(t, srv.ops())
}

// A dry run of delete has to read what it would delete, so it sends the two
// reads get breakpoints sends and nothing else, and needs no safety level.
// The first of them is the one request that finds a workspace, and creates
// an empty one where there is none: the user guide says so.
func TestDeleteBreakpoint_DryRunOnlyReads(t *testing.T) {
	for _, argv := range [][]string{
		{"delete", "breakpoint", "--all"},
		{"delete", "breakpoint", "A.java:1"},
		{"delete", "breakpoint", "dtctl-rule-0001"},
	} {
		t.Run(argv[2], func(t *testing.T) {
			srv := newLiveDebuggerStub(t, "readonly")
			srv.rules = []interface{}{ruleAt("A.java", 1, "")}
			chdirTestRepo(t, breakpointScope)

			code, stdout, stderr := runRepoScoped(append(argv, "--plain", "--dry-run", "--yes"), RunOptions{})
			require.Zero(t, code, "stdout: %s\nstderr: %s", stdout, stderr)
			require.Contains(t, breakpointMessage(t, stdout), "Dry run: would delete")
			require.Equal(t, []string{"GetOrCreateWorkspaceV2", "GetWorkspaceRules"}, srv.ops())
			require.Empty(t, srv.mutations())
		})
	}
}

// A deletion that is declined deletes nothing, for all the breakpoints of a
// workspace, for the several at one location, and for one.
func TestDeleteBreakpoint_DeclinedDeletesNothing(t *testing.T) {
	for _, tc := range []struct {
		name   string
		argv   []string
		prompt string
	}{
		{"all", []string{"delete", "breakpoint", "--all"}, "Delete ALL 2 breakpoint(s) in workspace "},
		{"several at one location", []string{"delete", "breakpoint", "A.java:1"}, "Delete 2 breakpoint(s) at A.java:1?"},
		{"one", []string{"delete", "breakpoint", "dtctl-rule-0001"}, "You are about to delete the following breakpoint:"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := newLiveDebuggerStub(t, "readwrite-all")
			srv.rules = []interface{}{ruleAt("A.java", 1, ""), ruleAt("A.java", 1, "")}
			chdirTestRepo(t, breakpointScope)

			code, stdout, stderr := runRepoScoped(append(tc.argv, "--no-agent"), RunOptions{Stdin: strings.NewReader("n\n")})
			require.Zero(t, code, stderr)
			require.Contains(t, stdout, tc.prompt)
			require.Contains(t, stdout, "Deletion cancelled")
			require.Empty(t, srv.mutations())
		})
	}
}
