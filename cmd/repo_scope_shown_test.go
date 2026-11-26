package cmd

import (
	"path/filepath"
	"strings"
	"testing"
	"unicode"

	"github.com/stretchr/testify/require"
)

// expectTerminalSafe fails when text holds a control character other than
// the line feeds that end its lines.
func expectTerminalSafe(t *testing.T, text string, args []string) {
	t.Helper()
	if i := strings.IndexFunc(text, func(r rune) bool { return r != '\n' && unicode.IsControl(r) }); i >= 0 {
		line := text[strings.LastIndexByte(text[:i], '\n')+1:]
		line, _, _ = strings.Cut(line, "\n")
		t.Errorf("%q prints a control character: %q", args, line)
	}
}

// Outside a repository the refusal names no path: the message travels in an
// agent envelope and in logs, and the path is this machine's.
func TestRepoScopeOutsideARepositoryNamesNoPath(t *testing.T) {
	const want = "repo scope: the working directory is not inside a git repository: a repo scope lives in .dtctl-repo-scope.yaml at the repository root"
	for _, args := range [][]string{
		{"query", "fetch logs", "--repo-scope", "x"}, {"verify", "query", "fetch logs", "--repo-scope", "x"},
		{"repo-scope", "list"}, {"repo-scope", "discover"}, {"repo-scope", "set", "x", "--service-name", "x"}, {"repo-scope", "delete", "x"},
		{"create", "breakpoint", "A.java:1", "--repo-scope", "x"},
	} {
		newLiveDebuggerStub(t, "readwrite-all")
		dir := t.TempDir()
		t.Chdir(dir)
		code, stdout, stderr := runRepoScoped(args, RunOptions{})
		require.NotZero(t, code, "%q", args)
		require.Equal(t, "Error: "+want+"\n", strings.SplitAfter(stderr, "\n")[0], "%q", args)
		require.NotContains(t, stdout+stderr, filepath.Base(dir), "%q", args)

		code, stdout, _ = runRepoScoped(append(args, "--agent"), RunOptions{})
		require.NotZero(t, code, "%q", args)
		require.Equal(t, want, agentError(t, stdout).Message, "%q", args)
		require.NotContains(t, stdout, filepath.Base(dir), "%q", args)
	}
}
