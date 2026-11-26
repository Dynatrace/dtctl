package cmd

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Dynatrace/dtctl/cmd/testutil"
	"github.com/Dynatrace/dtctl/pkg/client"
)

// current and describe have a view for a terminal and a value for a program,
// and nothing else: a format that would print neither is refused by name
// before it prints the value's address or a row of struct fields.
func TestRepoScopeStatusFormats(t *testing.T) {
	localRepoScopeConfig(t)
	chdirTestRepo(t, monorepoScope)
	chdirSubdir(t, "services/checkout")
	for _, verb := range []string{"current", "describe"} {
		code, table, stderr := runRepoScoped([]string{"repo-scope", verb}, RunOptions{})
		require.Zero(t, code, stderr)
		code, wide, stderr := runRepoScoped([]string{"repo-scope", verb, "-o", "wide"}, RunOptions{})
		require.Zero(t, code, stderr)
		require.Equal(t, table, wide, "%s has one view for a terminal", verb)

		for _, format := range []string{"csv", "chart", "jsonl"} {
			code, stdout, stderr := runRepoScoped([]string{"repo-scope", verb, "-o", format}, RunOptions{})
			require.Equal(t, client.ExitUsageError, code, "%s -o %s", verb, format)
			require.Empty(t, stdout)
			require.Contains(t, stderr, `-o `+format+`: repo-scope `+verb+` prints one entry, not rows; use -o table, -o json, -o yaml or -o toon`)
		}
		for _, format := range []string{"json", "yaml", "toon"} {
			code, stdout, stderr := runRepoScoped([]string{"repo-scope", verb, "-o", format}, RunOptions{})
			require.Zero(t, code, stderr)
			require.Contains(t, stdout, "selected", "%s -o %s prints the value", verb, format)
		}
	}
}

// list prints its rows in every format that prints rows.
func TestRepoScopeListFormats(t *testing.T) {
	localRepoScopeConfig(t)
	chdirTestRepo(t, monorepoScope)
	code, table, stderr := runRepoScoped([]string{"repo-scope", "list"}, RunOptions{})
	require.Zero(t, code, stderr)
	code, wide, stderr := runRepoScoped([]string{"repo-scope", "list", "-o", "wide"}, RunOptions{})
	require.Zero(t, code, stderr)
	require.Equal(t, table, wide)
	code, csv, stderr := runRepoScoped([]string{"repo-scope", "list", "-o", "csv"}, RunOptions{})
	require.Zero(t, code, stderr)
	testutil.AssertGolden(t, "repo-scope/list-csv", csv)
	// The group without a subcommand is list.
	code, group, stderr := runRepoScoped([]string{"repo-scope", "-o", "csv"}, RunOptions{})
	require.Zero(t, code, stderr)
	require.Equal(t, csv, group)
}

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
