package cmd

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/require"

	"github.com/Dynatrace/dtctl/pkg/config"
	"github.com/Dynatrace/dtctl/pkg/reposcope"
)

func TestRepoScopeAgentSetAndHumanDeleteLastEntry(t *testing.T) {
	localRepoScopeConfig(t)
	repo := chdirTestRepo(t, "")
	code, stdout, stderr := runRepoScoped([]string{"repo-scope", "set", "checkout", "--service-name", "checkout", "--agent"}, RunOptions{})
	require.Zero(t, code, stderr)
	var status reposcope.Status
	response := agentResult(t, stdout, &status)
	require.True(t, status.Linked)
	require.Equal(t, "checkout", status.Entry.Name)
	require.Equal(t, "set", response.Context.Verb)
	require.Contains(t, response.Context.Suggestions, "dtctl repo-scope current")
	require.Equal(t, status.Entry, &loadScopeFile(t, repo).Entries(repoScopeTestEnvHost)[0])
	code, stdout, stderr = runRepoScoped([]string{"repo-scope", "delete", "checkout"}, RunOptions{})
	require.Zero(t, code, stderr)
	require.Empty(t, stdout)
	require.Contains(t, stderr, "held no other entries and was removed")
	require.NoFileExists(t, filepath.Join(repo, reposcope.FileName))
}

func TestRepoScopeUnlinkedAgentSuggestions(t *testing.T) {
	for _, tc := range []struct {
		name, file  string
		code        reposcope.StatusCode
		suggestions []string
		outside     bool
	}{
		{name: "outside", outside: true, code: reposcope.StatusNotARepository, suggestions: []string{repoScopeCheckoutHint}},
		{name: "no file", code: reposcope.StatusNoFile, suggestions: []string{repoScopeDiscoverHint}},
		{name: "uncovered directory", file: monorepoScope, code: reposcope.StatusNoEntry,
			suggestions: []string{"no entry covers this directory; pass --repo-scope <name> to use one of: checkout, ledger"}},
		{name: "another environment", file: "environments:\n  other.example.invalid:\n    - name: checkout\n      service-names: [checkout]\n",
			code: reposcope.StatusNoEntryForEnvironment, suggestions: []string{repoScopeDiscoverHint}},
		// The file is there: the next step is to correct it, with the advice
		// its own error gives, and not to discover what it already says.
		{name: "a file with a misspelt key", file: "environments:\n  " + repoScopeTestEnvHost + ":\n    - name: checkout\n      service_names: [checkout]\n",
			code: reposcope.StatusInvalidFile, suggestions: []string{
				"correct .dtctl-repo-scope.yaml: nothing in it is applied until it loads",
				"correct the key, or upgrade dtctl if a newer one wrote it"}},
		{name: "a file with a value of the wrong format", file: "environments:\n  " + repoScopeTestEnvHost + ":\n    - name: checkout\n      services: [checkout]\n",
			code: reposcope.StatusInvalidFile, suggestions: []string{"correct .dtctl-repo-scope.yaml: nothing in it is applied until it loads"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			localRepoScopeConfig(t)
			if tc.outside {
				t.Chdir(t.TempDir())
			} else {
				chdirTestRepo(t, tc.file)
			}
			code, stdout, stderr := runRepoScoped([]string{"repo-scope", "current", "--agent"}, RunOptions{})
			require.Zero(t, code, stderr)
			var status reposcope.Status
			response := agentResult(t, stdout, &status)
			require.False(t, status.Linked)
			require.Equal(t, tc.code, status.Code)
			require.Equal(t, tc.suggestions, response.Context.Suggestions)
		})
	}
}

// What an agent is told next depends on how the entry was chosen. Only the
// entry in effect in the working directory narrows the queries run there; one
// described by name may cover another directory, and is used by naming it.
func TestRepoScopeLinkedAgentSuggestions(t *testing.T) {
	localRepoScopeConfig(t)
	chdirTestRepo(t, monorepoScope)
	chdirSubdir(t, "services/checkout")
	for _, tc := range []struct {
		args       []string
		selected   reposcope.Selection
		suggestion string
	}{
		{[]string{"current"}, reposcope.SelectedByDirectory,
			`'dtctl query' narrows fetch logs and fetch spans to "checkout" here; add --no-repo-scope to run a query unscoped`},
		{[]string{"describe", "ledger"}, reposcope.SelectedByName,
			`pass --repo-scope ledger to 'dtctl query' to narrow fetch logs and fetch spans to this entry from any directory`},
		{[]string{"describe", "checkout"}, reposcope.SelectedByName,
			`pass --repo-scope checkout to 'dtctl query' to narrow fetch logs and fetch spans to this entry from any directory`},
	} {
		code, stdout, stderr := runRepoScoped(append([]string{"repo-scope", "--agent"}, tc.args...), RunOptions{})
		require.Zero(t, code, stderr)
		var status reposcope.Status
		response := agentResult(t, stdout, &status)
		require.Equal(t, reposcope.StatusLinked, status.Code)
		require.Equal(t, tc.selected, status.Selected)
		require.Equal(t, []string{tc.suggestion}, response.Context.Suggestions)
	}
}

func TestRepoScopeListOtherOrEmptyEnvironments(t *testing.T) {
	for _, tc := range []struct{ name, file, reason string }{
		{"empty", "environments: {}\n", reposcope.FileName + " defines no environment"},
		{"other", "environments:\n  other.example.invalid:\n    - name: checkout\n      service-names: [checkout]\n", reposcope.FileName + " has no entry for " + repoScopeTestEnvHost + " (it defines: other.example.invalid)"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			localRepoScopeConfig(t)
			chdirTestRepo(t, tc.file)
			for _, flags := range [][]string{{}, {"--agent"}} {
				code, stdout, stderr := runRepoScoped(append([]string{"repo-scope", "list"}, flags...), RunOptions{})
				require.Zero(t, code, stderr)
				if len(flags) == 0 {
					require.Contains(t, stderr, tc.reason)
				} else {
					require.Equal(t, []string{tc.reason, repoScopeDiscoverHint}, agentEnvelope(t, stdout).Context.Suggestions)
				}
			}
		})
	}
}

func TestRepoScopeCompletion(t *testing.T) {
	localRepoScopeConfig(t)
	repo := chdirTestRepo(t, monorepoScope)
	cmd := newRepoScopeSetCmd()
	names, directive := completeRepoScopeNames(cmd, nil, "")
	require.Equal(t, []string{"checkout", "ledger"}, names)
	require.Equal(t, cobra.ShellCompDirectiveNoFileComp, directive)
	names, directive = completeRepoScopeNames(cmd, []string{"checkout"}, "")
	require.Empty(t, names)
	require.Equal(t, cobra.ShellCompDirectiveNoFileComp, directive)
	require.NoError(t, os.WriteFile(filepath.Join(repo, reposcope.FileName), []byte("environments: ["), 0o644))
	names, directive = completeRepoScopeNames(cmd, nil, "")
	require.Empty(t, names)
	require.Equal(t, cobra.ShellCompDirectiveNoFileComp, directive)
}

func TestRepoScopeInvalidContext(t *testing.T) {
	for _, cfg := range []*config.Config{
		{},
		{CurrentContext: "missing"},
		{CurrentContext: "empty", Contexts: []config.NamedContext{{Name: "empty"}}},
	} {
		scope, err := openRepoScope(t.Context(), cfg)
		require.Error(t, err)
		require.Nil(t, scope)
	}
}

func TestRepoScopeBrokenConfig(t *testing.T) {
	chdirTestRepo(t, "")
	cfg := filepath.Join(t.TempDir(), "broken.yaml")
	require.NoError(t, os.WriteFile(cfg, []byte("contexts: ["), 0o600))
	for _, args := range [][]string{
		{"repo-scope", "list"}, {"repo-scope", "current"}, {"repo-scope", "discover"},
	} {
		code, stdout, stderr := runRepoScoped(append(args, "--config", cfg), RunOptions{})
		require.NotZero(t, code)
		require.Contains(t, stdout+stderr, "config")
	}
}

func TestRepoScopeDeletePreservesUnknownKeys(t *testing.T) {
	localRepoScopeConfig(t)
	repo := chdirTestRepo(t, "future-key: keep\n"+monorepoScope)
	before := readScopeFile(t, repo)
	code, stdout, _ := runRepoScoped([]string{"repo-scope", "delete", "checkout", "--agent"}, RunOptions{})
	require.NotZero(t, code)
	require.Equal(t, "validation_error", agentError(t, stdout).Code)
	require.Equal(t, before, readScopeFile(t, repo))
}

func TestRepoScopeDiscoverDryRunWithoutNames(t *testing.T) {
	localRepoScopeConfig(t)
	repo := chdirTestRepo(t, "")
	code, stdout, stderr := runRepoScoped([]string{"repo-scope", "discover", "--dry-run"}, RunOptions{})
	require.Zero(t, code, stderr)
	require.Contains(t, stdout, "no usable name found")
	require.Contains(t, stdout, repoScopeNarrowHint)
	require.NoFileExists(t, filepath.Join(repo, reposcope.FileName))
}

// The scope file comes with a clone, and its paths are printed in the
// answers about it. An escape sequence in one must never reach the terminal.
func TestRepoScopeNeverPrintsAnEscapeSequence(t *testing.T) {
	localRepoScopeConfig(t)
	chdirTestRepo(t, "environments:\n  "+repoScopeTestEnvHost+":\n    - name: checkout\n      path: \"svc\\e[31mRED\"\n      service-names: [checkout]\n")
	for _, args := range [][]string{{"current"}, {"describe"}, {"list"}, {"describe", "checkout"}} {
		_, stdout, stderr := runRepoScoped(append([]string{"repo-scope"}, args...), RunOptions{})
		require.NotContains(t, stdout+stderr, "\x1b", args)
		require.Contains(t, stdout+stderr, `path "svc\x1b[31mRED" must not contain control or non-printing characters`, args)
	}
}
