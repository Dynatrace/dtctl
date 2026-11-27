package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Dynatrace/dtctl/pkg/exec"
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
				"to see the workspace and its filters: dtctl --context c get breakpoints -v",
				"to restore the previous filters: dtctl --context c update breakpoint --filters k8s.namespace.name:web",
				"to try again once the cause is fixed: dtctl --context c create breakpoint A.java:1",
			}},
		{"--filters over none", nil, []string{"--filters", "k8s.namespace.name:payments"},
			"workspace WS's filters were set to k8s.namespace.name:payments (it had none), then creating the breakpoint at A.java:1 failed: " + cause,
			[]string{
				"the filters were not rolled back: every breakpoint in workspace WS is scoped by them now",
				"to see the workspace and its filters: dtctl --context c get breakpoints -v",
				"the workspace had no filters before, and no dtctl command clears them",
				"to try again once the cause is fixed: dtctl --context c create breakpoint A.java:1",
			}},
		{"--filters over several sets", []string{splitWorkloadSet, splitServiceSet}, []string{"--filters", "k8s.namespace.name:payments"},
			"workspace WS's filters were changed from k8s.namespace.name:payments,k8s.workload.name:split; service.name:split to k8s.namespace.name:payments, then creating the breakpoint at A.java:1 failed: " + cause,
			[]string{
				"the filters were not rolled back: every breakpoint in workspace WS is scoped by them now",
				"to see the workspace and its filters: dtctl --context c get breakpoints -v",
				"the previous filters were several sets, which --filters cannot spell, and they are not the ones repo scope checkout renders: they are in this message, and no single command restores them",
				"to try again once the cause is fixed: dtctl --context c create breakpoint A.java:1",
			}},
		{"--repo-scope over one set", []string{webSetJSON}, []string{"--repo-scope", "ledger"},
			"workspace WS's filters were changed from k8s.namespace.name:web to service.name:ledger (repo scope ledger), then creating the breakpoint at A.java:1 failed: " + cause,
			[]string{
				"the filters were not rolled back: every breakpoint in workspace WS is scoped by them now",
				"to see the workspace and its filters: dtctl --context c get breakpoints -v --repo-scope ledger",
				"the previous filters are in this message; no single command restores them in a workspace --repo-scope named",
				"to try again once the cause is fixed: dtctl --context c create breakpoint A.java:1 --repo-scope ledger",
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
			// After them, only what was said about the entry's filters.
			notes := []string{}
			if tc.args[0] == "--repo-scope" {
				notes = []string{"repo scope ledger sets service.name, which is not verified as a workspace label: a filter set a workspace does not honour selects nothing, and a breakpoint under it is created and never hits; check with dtctl describe breakpoint <id>"}
			}
			require.Equal(t, notes, detail.Suggestions[len(tc.suggest):])
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

// twoContextConfig is a config whose current context, prod, is not the one a
// test runs with: staging. Both are stubs, so a line that loses its context
// is seen arriving at the wrong one.
func twoContextConfig(staging, prod *liveDebuggerStub) string {
	return fmt.Sprintf(`current-context: prod
contexts:
  - name: prod
    context:
      environment: %s
      token-ref: t
      safety-level: readwrite-all
  - name: staging
    context:
      environment: %s
      token-ref: t
      safety-level: readwrite-all
tokens:
  - name: t
    token: dt0c01.EXAMPLE
`, prod.URL, staging.URL)
}

// suggestedCommand is the dtctl line a suggestion ends with.
func suggestedCommand(t *testing.T, suggestion string) string {
	t.Helper()
	_, line, ok := strings.Cut(suggestion, ": dtctl ")
	require.True(t, ok, "no command in %q", suggestion)
	return "dtctl " + line
}

// runSuggested runs a suggested line as a shell would hand it over, with
// whatever else the caller's shell would carry, and returns its exit code.
func runSuggested(t *testing.T, line string, extra ...string) int {
	t.Helper()
	words := shellWords(t, line)
	require.Equal(t, "dtctl", words[0], line)
	code, _, _ := runRepoScoped(append(words[1:], extra...), RunOptions{})
	return code
}

// The lines a failed create suggests are run later, in a shell whose current
// context may be another one, and the same repository names the same
// workspace in every environment: each line carries the context, and the
// config --config named, of the run that printed it, every value as one
// shell word. Run as printed, each reaches the environment and the workspace
// the create changed, and no other.
func TestCreateBreakpoint_RecoveryLinesKeepTheContext(t *testing.T) {
	const orderSet = `{"filters":[],"labels":[{"field":"service.name","values":["Order Service"]}]}`
	for _, tc := range []struct {
		name string
		// withConfig names the config with --config, and takes it out of
		// the environment before the lines are run.
		withConfig bool
		// dir is where the create runs, below the repository root.
		dir, entry string
		seed       []string
		args       []string
		// restore is the restoring line after the selectors, "" when the
		// message offers none.
		restore string
		retry   string
	}{
		{name: "--context", entry: "checkout", seed: []string{orderSet}, args: []string{"--filters", "k8s.namespace.name:payments"},
			restore: "update breakpoint --filters 'service.name:Order Service'", retry: "create breakpoint 'My File.java:1'"},
		{name: "--config", withConfig: true, entry: "checkout", seed: []string{orderSet}, args: []string{"--filters", "k8s.namespace.name:payments"},
			restore: "update breakpoint --filters 'service.name:Order Service'", retry: "create breakpoint 'My File.java:1'"},
		{name: "several sets the covering entry renders", dir: filepath.Join("services", "split"), entry: "split",
			seed: []string{splitServiceSet, splitWorkloadSet}, args: []string{"--filters", "k8s.namespace.name:payments"},
			restore: "update breakpoint --repo-scope split", retry: "create breakpoint 'My File.java:1'"},
		{name: "--repo-scope", entry: "ledger", seed: []string{orderSet}, args: []string{"--repo-scope", "ledger"},
			retry: "create breakpoint 'My File.java:1' --repo-scope ledger"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			prod := newLiveDebuggerStub(t, "readwrite-all", tc.seed...)
			staging := newLiveDebuggerStub(t, "readwrite-all", tc.seed...)
			staging.fail["CreateRule"] = "rule limit reached"
			writeIsolatedConfig(t, twoContextConfig(staging, prod))
			ws := filepath.Base(chdirTestRepo(t, breakpointScope)) + "/" + tc.entry
			if tc.dir != "" {
				chdirSubdir(t, tc.dir)
			}

			selectors := "--context staging"
			argv := []string{"--context", "staging"}
			if tc.withConfig {
				path := os.Getenv("DTCTL_CONFIG")
				t.Setenv("DTCTL_CONFIG", filepath.Join(t.TempDir(), "absent.yaml"))
				argv = append(argv, "--config", path)
				selectors = "--config " + exec.ShellQuote(path) + " " + selectors
			}
			argv = append(argv, "create", "breakpoint", "My File.java:1", "--agent")
			code, stdout, _ := runRepoScoped(append(argv, tc.args...), RunOptions{})
			require.NotZero(t, code)
			suggestions := agentError(t, stdout).Suggestions
			require.GreaterOrEqual(t, len(suggestions), 4)
			require.Equal(t, []string{ws}, staging.workedIn())
			sent := staging.sentFilterSets(t)

			named := ""
			if tc.args[0] == "--repo-scope" {
				named = " --repo-scope " + tc.args[1]
			}
			require.Equal(t, "dtctl "+selectors+" get breakpoints -v"+named, suggestedCommand(t, suggestions[1]))
			require.Equal(t, "dtctl "+selectors+" "+tc.retry, suggestedCommand(t, suggestions[3]))
			if tc.restore == "" {
				require.NotContains(t, suggestions[2], "dtctl ")
			} else {
				require.Equal(t, "to restore the previous filters: dtctl "+selectors+" "+tc.restore, suggestions[2])
			}

			// Each line, run as printed in a shell that selects nothing.
			delete(staging.fail, "CreateRule")
			staging.reset()
			require.Zero(t, runSuggested(t, suggestedCommand(t, suggestions[1]), "--agent"))
			require.Equal(t, []string{ws}, staging.projectPaths(), "the see line shows the workspace that was changed")

			staging.reset()
			require.Zero(t, runSuggested(t, suggestedCommand(t, suggestions[3]), "--agent"))
			require.Equal(t, []string{ws}, staging.workedIn(), "the retry creates in the workspace that was changed")
			require.Equal(t, "CreateRule", staging.mutations()[len(staging.mutations())-1])

			if tc.restore != "" {
				staging.reset()
				require.Zero(t, runSuggested(t, suggestedCommand(t, suggestions[2]), "--agent", "--yes"))
				require.Equal(t, []string{"UpdateWorkspaceV2"}, staging.mutations())
				require.Equal(t, []string{ws}, staging.workedIn(), "the restore writes to the workspace that was changed")
				require.NotEqual(t, sent, staging.sentFilterSets(t))
				require.ElementsMatch(t, decodeFilterSets(t, filterSetsJSON(tc.seed...)), decodeFilterSets(t, staging.sentFilterSets(t)), "the restore sends the previous filters")
			}
			require.Empty(t, prod.ops(), "no line reaches the config's current context")
		})
	}
}

// decodeFilterSets decodes a JSON list of filter sets for comparison.
func decodeFilterSets(t *testing.T, raw string) []interface{} {
	t.Helper()
	var sets []interface{}
	require.NoError(t, json.Unmarshal([]byte(raw), &sets))
	return sets
}

// Several previous sets are restored by the entry the workspace is named
// after only when that entry renders exactly them; otherwise no line is
// offered, and never one with a placeholder for a name.
func TestCreateBreakpoint_OffersNoRestoreItCannotSpell(t *testing.T) {
	srv := newLiveDebuggerStub(t, "readwrite-all", splitWorkloadSet, splitServiceSet)
	srv.fail["CreateRule"] = "rule limit reached"
	chdirTestRepo(t, "")

	code, stdout, _ := runRepoScoped([]string{"create", "breakpoint", "A.java:1", "--filters", "k8s.namespace.name:payments", "--agent"}, RunOptions{})
	require.NotZero(t, code)
	require.Equal(t, "the previous filters were several sets, which --filters cannot spell: they are in this message, and no single command restores them",
		agentError(t, stdout).Suggestions[2])
}

// A session has one environment and no context its caller could name, so
// the lines of a create that ran in one carry no selector.
func TestCreateBreakpoint_RecoveryLinesNameNoContextInASession(t *testing.T) {
	srv := newLiveDebuggerStub(t, "readwrite-all", webSetJSON)
	srv.fail["CreateRule"] = "rule limit reached"

	code, stdout, _ := runRepoScoped([]string{"create", "breakpoint", "A.java:1", "--filters", "k8s.namespace.name:payments", "--agent"},
		RunOptions{Session: &Session{EnvironmentURL: srv.URL, Token: "t", SafetyLevel: "readwrite-all", MinStability: "experimental"}})
	require.NotZero(t, code)
	suggestions := agentError(t, stdout).Suggestions
	require.Len(t, suggestions, 4)
	require.Equal(t, "dtctl get breakpoints -v", suggestedCommand(t, suggestions[1]))
	require.Equal(t, "dtctl update breakpoint --filters k8s.namespace.name:web", suggestedCommand(t, suggestions[2]))
	require.Equal(t, "dtctl create breakpoint A.java:1", suggestedCommand(t, suggestions[3]))
}

// A set without a value selects by nothing: beside one real set it does not
// make the previous filters "several sets", and the one set is restored.
func TestCreateBreakpoint_AnEmptySetIsNotAPreviousFilter(t *testing.T) {
	srv := newLiveDebuggerStub(t, "readwrite-all", webSetJSON, `{"filters":[],"labels":[]}`)
	srv.fail["CreateRule"] = "rule limit reached"
	chdirTestRepo(t, "")

	code, stdout, _ := runRepoScoped([]string{"create", "breakpoint", "A.java:1", "--filters", "k8s.namespace.name:payments", "--agent"}, RunOptions{})
	require.NotZero(t, code)
	require.Equal(t, "to restore the previous filters: dtctl --context c update breakpoint --filters k8s.namespace.name:web", agentError(t, stdout).Suggestions[2])
}

// A workspace's filters are whatever the environment returns. A value with a
// control character is printed with U+FFFD in its place, in the error and in
// the hint, and no restore line is offered: the line would not set the value
// the workspace held.
func TestCreateBreakpoint_PrintsNoControlCharacterOfAWorkspaceFilter(t *testing.T) {
	const escaped = `{"filters":[],"labels":[{"field":"service.name","values":["svc\u001b[31mRED"]}]}`
	const shown = "service.name:svc�[31mRED"

	srv := newLiveDebuggerStub(t, "readwrite-all", escaped)
	srv.fail["CreateRule"] = "rule limit reached"
	chdirTestRepo(t, breakpointScope)
	code, stdout, stderr := runRepoScoped([]string{"create", "breakpoint", "A.java:1", "--filters", "k8s.namespace.name:payments", "--no-agent", "--yes"}, RunOptions{})
	require.NotZero(t, code)
	require.NotContains(t, stdout+stderr, "\x1b")
	require.Contains(t, stderr, "filters were changed from "+shown+" to ")
	require.Contains(t, stderr, "the previous filters are in this message; --filters cannot spell them")
	require.NotContains(t, stderr, "to restore the previous filters")

	newLiveDebuggerStub(t, "readwrite-all", escaped)
	code, stdout, stderr = runRepoScoped([]string{"create", "breakpoint", "A.java:1", "--no-agent"}, RunOptions{})
	require.Zero(t, code, stderr)
	require.NotContains(t, stdout+stderr, "\x1b")
	require.Contains(t, stderr, "filters ("+shown+") are not the ones repo scope checkout")
}

// What the filters set from an entry leave out, and carry unverified, was
// said before the write. When the create then fails the agent's envelope is
// an error, which has no context.warnings, so the lines close its
// suggestions: the filters they describe are set.
func TestCreateBreakpoint_AFailedCreateKeepsTheEntrysNotes(t *testing.T) {
	srv := newLiveDebuggerStub(t, "readwrite-all", webSetJSON)
	srv.fail["CreateRule"] = "rule limit reached"
	chdirTestRepo(t, breakpointScope)

	code, stdout, _ := runRepoScoped([]string{"create", "breakpoint", "A.java:1", "--repo-scope", "split", "--agent"}, RunOptions{})
	require.NotZero(t, code)
	suggestions := agentError(t, stdout).Suggestions
	require.Len(t, suggestions, 6)
	require.Equal(t, "repo scope split's service ids are not sent: Live Debugger does not honour dt.entity.service as a workspace label", suggestions[4])
	require.True(t, strings.HasPrefix(suggestions[5], "repo scope split sets service.name, which is not verified as a workspace label"), suggestions[5])
}
