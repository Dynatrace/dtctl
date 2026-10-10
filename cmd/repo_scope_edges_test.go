package cmd

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/require"

	"github.com/dynatrace-oss/dtctl/pkg/config"
	"github.com/dynatrace-oss/dtctl/pkg/reposcope"
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
		name, file, suggestion string
		outside                bool
	}{
		{name: "outside", outside: true, suggestion: repoScopeCheckoutHint},
		{name: "no file", suggestion: repoScopeDiscoverHint},
		{name: "uncovered directory", file: monorepoScope, suggestion: "no entry covers this directory; pass --repo-scope <name> to use one of: checkout, ledger"},
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
			require.Equal(t, []string{tc.suggestion}, response.Context.Suggestions)
		})
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
