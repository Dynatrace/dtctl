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

// Without a scope file there is no entry to find and no file to name: the
// answer says that the repository has none, and how to get one.
func TestRepoScopeNamedEntryWithoutAFile(t *testing.T) {
	const want = `repo scope "ledger" not found: this repository has no ` + reposcope.FileName + ` ('dtctl repo-scope set' writes one)`
	for _, args := range [][]string{
		{"repo-scope", "describe", "ledger"}, {"repo-scope", "delete", "ledger"},
		{"query", "fetch logs", "--repo-scope", "ledger"}, {"verify", "query", "fetch logs", "--repo-scope", "ledger"},
		{"create", "breakpoint", "A.java:1", "--repo-scope", "ledger"},
	} {
		newLiveDebuggerStub(t, "readwrite-all")
		optInRepoScope(t)
		chdirTestRepo(t, "")
		code, _, stderr := runRepoScoped(args, RunOptions{})
		require.Equal(t, client.ExitNotFoundError, code, "%q", args)
		require.Equal(t, "Error: "+want+"\n", strings.SplitAfter(stderr, "\n")[0], "%q", args)

		code, stdout, _ := runRepoScoped(append(args, "--agent"), RunOptions{})
		require.Equal(t, client.ExitNotFoundError, code, "%q", args)
		detail := agentError(t, stdout)
		require.Equal(t, "not_found", detail.Code)
		require.Equal(t, want, detail.Message, "%q", args)
		require.Equal(t, []string{repoScopeDiscoverHint}, detail.Suggestions, "%q", args)
	}
}

// A word after the group that is no subcommand is answered by the group: the
// root's advice for the word (a synonym of one of its verbs) is not for here.
func TestRepoScopeUnknownSubcommandIsTheGroups(t *testing.T) {
	localRepoScopeConfig(t)
	chdirTestRepo(t, "")
	for _, word := range []string{"search", "show", "sett"} {
		code, _, stderr := runRepoScoped([]string{"repo-scope", word}, RunOptions{})
		require.NotZero(t, code)
		require.Contains(t, stderr, `unknown command "`+word+`"`, word)
		require.NotContains(t, stderr, "dtctl query", word)
		require.NotContains(t, stderr, "dtctl get", word)
	}
	_, _, stderr := runRepoScoped([]string{"repo-scope", "sett"}, RunOptions{})
	require.Contains(t, stderr, "set")
}
