package cmd

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Dynatrace/dtctl/pkg/client"
)

// A name the file does not define exits as any resource that does not exist,
// and a value or a file that is refused as any other usage error, so a shell
// caller reads the same verdict the envelope's code gives.
func TestRepoScopeExitCodes(t *testing.T) {
	localRepoScopeConfig(t)
	repo := chdirTestRepo(t, monorepoScope)
	before := readScopeFile(t, repo)
	for _, tc := range []struct {
		args []string
		exit int
		code string
	}{
		{[]string{"describe", "missing"}, client.ExitNotFoundError, "not_found"},
		{[]string{"delete", "missing"}, client.ExitNotFoundError, "not_found"},
		{[]string{"set", "x", "--service", "BAD"}, client.ExitUsageError, "validation_error"},
		{[]string{"set", "x", "--workload", "w"}, client.ExitUsageError, "validation_error"},
		{[]string{"set", "x", "--namespace", "n"}, client.ExitUsageError, "validation_error"},
		{[]string{"set", "x", "--namespace", "a", "--workload", "w", "--namespace", "b"}, client.ExitUsageError, "validation_error"},
	} {
		code, stdout, _ := runRepoScoped(append([]string{"repo-scope", "--agent"}, tc.args...), RunOptions{})
		require.Equal(t, tc.exit, code, tc.args)
		require.Equal(t, tc.code, agentError(t, stdout).Code, tc.args)
		require.Equal(t, before, readScopeFile(t, repo))
	}
	_, stdout, _ := runRepoScoped([]string{"repo-scope", "set", "x", "--workload", "w", "--agent"}, RunOptions{})
	require.Equal(t, "repo-scope set: --workload names a workload of a namespace; pass --namespace with it", agentError(t, stdout).Message)
}

// A misspelt subcommand is answered from the group's own subcommands.
func TestRepoScopeUnknownSubcommand(t *testing.T) {
	localRepoScopeConfig(t)
	chdirTestRepo(t, monorepoScope)
	for typed, meant := range map[string]string{"lst": "list", "curent": "current", "discovr": "discover"} {
		code, stdout, stderr := runRepoScoped([]string{"repo-scope", typed}, RunOptions{})
		require.Equal(t, client.ExitUsageError, code)
		require.Empty(t, stdout)
		require.Contains(t, stderr, `unknown command "`+typed+`", did you mean "`+meant+`"?`)

		code, stdout, _ = runRepoScoped([]string{"repo-scope", typed, "--agent"}, RunOptions{})
		require.Equal(t, client.ExitUsageError, code)
		require.Equal(t, "unknown_command", agentError(t, stdout).Code)
	}
}
