package cmd

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Dynatrace/dtctl/pkg/client"
	"github.com/Dynatrace/dtctl/pkg/reposcope"
)

// A refusal of set is an envelope when the run is in agent mode, and the
// line a terminal reads when it is not: whether a value on the command line
// looks like a mode flag, or switches one off, does not decide it.
func TestRepoScopeSetRefusalFollowsTheModeTheRunIsIn(t *testing.T) {
	localRepoScopeConfig(t)
	chdirTestRepo(t, "")
	for _, args := range [][]string{
		{"repo-scope", "set", "x", "--workload", "w", "--service-name", "-A"},
		{"repo-scope", "set", "x", "--service", "BAD", "--agent=false"},
		{"repo-scope", "set", "x", "--service", "BAD", "--plain=false"},
		{"repo-scope", "set", "x", "--namespace", "a", "--namespace", "b", "--workload", "w"},
	} {
		code, stdout, stderr := runRepoScoped(args, RunOptions{})
		require.Equal(t, client.ExitUsageError, code, "%q", args)
		require.Empty(t, stdout, "%q", args)
		require.True(t, strings.HasPrefix(stderr, "Error: "), "%q: %s", args, stderr)
	}
	// The command is named once.
	_, _, stderr := runRepoScoped([]string{"repo-scope", "set", "x", "--namespace", "a", "--namespace", "b", "--workload", "w"}, RunOptions{})
	require.Equal(t, "Error: repo-scope set: --namespace takes one value, for every --workload of the command\n", strings.SplitAfter(stderr, "\n")[0])
}

// --path names a directory below the root. The root itself is the entry
// without one, and the advice is about the flag, not about the file's key.
func TestRepoScopeSetRefusesTheRootAsAPath(t *testing.T) {
	localRepoScopeConfig(t)
	repo := chdirTestRepo(t, "")
	code, stdout, _ := runRepoScoped([]string{"repo-scope", "set", "all", "--service-name", "all", "--path", ".", "--agent"}, RunOptions{})
	require.Equal(t, client.ExitUsageError, code)
	detail := agentError(t, stdout)
	require.Equal(t, `--path: "." is the repository root, which an entry without a path covers`, detail.Message)
	require.Equal(t, []string{"leave --path out for an entry that covers the whole repository"}, detail.Suggestions)
	require.NoFileExists(t, filepath.Join(repo, reposcope.FileName))
}
