package cmd

import (
	"net/http"
	"os"
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

// A directory inside a checkout of its own never reads this repository's
// file: the walk up from it stops at the inner .git. An entry for it would be
// committed and cover no query.
func TestRepoScopeSetRefusesAPathInANestedCheckout(t *testing.T) {
	localRepoScopeConfig(t)
	repo := chdirTestRepo(t, "")
	require.NoError(t, os.MkdirAll(filepath.Join(repo, "vendor", "sub", "inner"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(repo, "vendor", "sub", ".git"), []byte("gitdir: ../../.git/modules/sub\n"), 0o644))
	for _, dir := range []string{"vendor/sub/inner", "vendor/sub"} {
		code, stdout, _ := runRepoScoped([]string{"repo-scope", "set", "sub", "--service-name", "sub", "--path", dir, "--agent"}, RunOptions{})
		require.Equal(t, client.ExitUsageError, code, dir)
		detail := agentError(t, stdout)
		require.Equal(t, `--path: "`+dir+`" is in the checkout at "vendor/sub", which has a .git of its own, so no query run there reads this repository's `+reposcope.FileName, detail.Message)
		require.Equal(t, []string{"run 'dtctl repo-scope set' inside vendor/sub to link that checkout"}, detail.Suggestions)
		require.NoFileExists(t, filepath.Join(repo, reposcope.FileName))
	}
	// The directory above the inner checkout is this repository's.
	code, _, stderr := runRepoScoped([]string{"repo-scope", "set", "vendor", "--service-name", "vendor", "--path", "vendor"}, RunOptions{})
	require.Zero(t, code, stderr)
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

// A scope file that does not load is named the way the repository names it,
// by every command and in every mode: its path on this machine, the home
// directory in it, is nobody else's to read in an envelope or a log.
func TestRepoScopeBrokenFileIsNamedWithoutItsPath(t *testing.T) {
	broken := "environments:\n  " + repoScopeTestEnvHost + ":\n    - name: Checkout\n      service-names: [checkout]\n"
	for _, args := range [][]string{
		{"repo-scope", "list"}, {"repo-scope", "set", "x", "--service-name", "x"}, {"repo-scope", "delete", "checkout"},
		{"repo-scope", "describe", "checkout"}, {"repo-scope", "current"}, {"repo-scope", "describe"},
		{"query", "fetch logs", "--repo-scope", "checkout"}, {"query", "fetch logs"},
	} {
		for _, mode := range [][]string{nil, {"--agent"}} {
			newDQLRecorder(t, "readonly", answer(http.StatusOK, oneRecord))
			writeIsolatedConfig(t, safetyLevelConfig(repoScopeTestEnvironment, "readonly"))
			repo := chdirTestRepo(t, broken)
			_, stdout, stderr := runRepoScoped(append(args, mode...), RunOptions{})
			require.NotContains(t, stdout+stderr, filepath.Dir(repo), "%q %q", args, mode)
			require.Contains(t, stdout+stderr, reposcope.FileName+": environments[", "%q %q", args, mode)
		}
	}

	// A save that is refused names it the same way.
	localRepoScopeConfig(t)
	repo := chdirTestRepo(t, "kind: RepoScope\nenvironments:\n  "+repoScopeTestEnvHost+":\n    - name: checkout\n      service-names: [checkout]\n")
	code, stdout, _ := runRepoScoped([]string{"repo-scope", "set", "checkout", "--service-name", "x", "--agent"}, RunOptions{})
	require.Equal(t, client.ExitUsageError, code)
	require.True(t, strings.HasPrefix(agentError(t, stdout).Message, reposcope.FileName+`: unknown key "kind" on line 1`), stdout)
	require.NotContains(t, stdout, filepath.Dir(repo))
}

// What an agent is told to run after a set speaks about the entry it saved,
// in the environment it saved it for, from whatever directory.
func TestRepoScopeSetSuggestsCommandsForTheEntryItSaved(t *testing.T) {
	localRepoScopeConfig(t)
	chdirTestRepo(t, "")
	chdirSubdir(t, "elsewhere")
	code, stdout, stderr := runRepoScoped([]string{"repo-scope", "set", "ledger", "--service-name", "ledger", "--agent"}, RunOptions{})
	require.Zero(t, code, stderr)
	require.Equal(t, []string{
		"dtctl repo-scope describe ledger --context c",
		"dtctl query 'fetch logs | limit 20' --repo-scope ledger --context c",
	}, agentEnvelope(t, stdout).Context.Suggestions)
}

// list prints rows, or the entries in a format that holds a value. A format
// that draws a chart, or none at all, is refused before anything is printed.
func TestRepoScopeListRefusesAFormatItCannotPrint(t *testing.T) {
	localRepoScopeConfig(t)
	chdirTestRepo(t, monorepoScope)
	for _, format := range []string{"chart", "bogus", "sparkline"} {
		code, stdout, stderr := runRepoScoped([]string{"repo-scope", "list", "-o", format}, RunOptions{})
		require.Equal(t, client.ExitUsageError, code, format)
		require.Empty(t, stdout, format)
		require.Contains(t, stderr, "repo-scope list prints entries; use -o table, -o wide, -o csv, -o json, -o jsonl, -o yaml or -o toon", format)
	}
	for _, format := range []string{"table", "wide", "csv", "json", "jsonl", "yaml", "yml", "toon"} {
		code, stdout, stderr := runRepoScoped([]string{"repo-scope", "list", "-o", format}, RunOptions{})
		require.Zero(t, code, "%s: %s", format, stderr)
		require.Contains(t, stdout, "ledger", format)
	}
}

// -o auto is the format a terminal gets when none is named.
func TestRepoScopeCurrentTakesAutoAsTheTerminalForm(t *testing.T) {
	localRepoScopeConfig(t)
	chdirTestRepo(t, monorepoScope)
	chdirSubdir(t, "services/checkout")
	for _, verb := range []string{"current", "describe"} {
		_, plain, _ := runRepoScoped([]string{"repo-scope", verb}, RunOptions{})
		code, stdout, stderr := runRepoScoped([]string{"repo-scope", verb, "-o", "auto"}, RunOptions{})
		require.Zero(t, code, stderr)
		require.Equal(t, plain, stdout)
	}
}

// The envelope of a delete says, like the terminal does, that the file went
// with its last entry.
func TestRepoScopeDeleteSaysWhenTheFileWasRemoved(t *testing.T) {
	localRepoScopeConfig(t)
	repo := chdirTestRepo(t, monorepoScope)
	var deleted repoScopeDeleted
	_, stdout, _ := runRepoScoped([]string{"repo-scope", "delete", "ledger", "--agent"}, RunOptions{})
	agentResult(t, stdout, &deleted)
	require.Equal(t, repoScopeDeleted{Deleted: true, Name: "ledger", File: reposcope.FileName, Environment: repoScopeTestEnvHost}, deleted)
	require.Contains(t, stdout, `"fileRemoved":false`)
	_, stdout, _ = runRepoScoped([]string{"repo-scope", "delete", "checkout", "--agent"}, RunOptions{})
	agentResult(t, stdout, &deleted)
	require.True(t, deleted.FileRemoved)
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
