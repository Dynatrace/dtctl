package cmd

import (
	"slices"
	"testing"

	"github.com/stretchr/testify/require"
)

// An empty result for a breakpoint that is disabled for a reason, such as a
// hit limit, says where its earlier snapshots are, on stderr and in the
// agent envelope; an enabled one gets no hint, whether or not it was ever
// re-versioned, and neither does a result that has a row.
func TestGetSnapshots_ReversionedBreakpoint(t *testing.T) {
	const hint = `0 snapshots under the breakpoint's current ID (HIT_LIMIT_REACHED); earlier snapshots, if any, are under its previous IDs: dtctl query 'fetch application.snapshots | summarize count(), by:{breakpoint.id}' --no-repo-scope --default-timeframe-start now-7d`
	for _, tc := range []struct {
		disableReason string
		want          string
	}{
		{"HIT_LIMIT_REACHED", hint},
		{"", ""},
	} {
		srv := newLiveDebuggerStub(t, "readonly")
		srv.rules = []interface{}{ruleAt("WebProxy.java", 42, tc.disableReason)}
		t.Chdir(t.TempDir())
		argv := []string{"get", "snapshots", "WebProxy.java:42", "--default-timeframe-start", "now-7d"}

		code, _, stderr := runRepoScoped(append(argv, "--plain"), RunOptions{})
		require.Zero(t, code, stderr)
		code, stdout, _ := runRepoScoped(append(argv, "--agent"), RunOptions{})
		require.Zero(t, code, stdout)
		suggestions := agentEnvelope(t, stdout).Context.Suggestions
		if tc.want == "" {
			require.NotContains(t, stderr, "current ID")
			require.NotContains(t, suggestions, hint)
			continue
		}
		require.Contains(t, stderr, tc.want+"\n")
		require.Equal(t, tc.want, suggestions[0])

		srv.records = `{"breakpoint.id":"abc12345"}`
		code, stdout, stderr = runRepoScoped(append(argv, "--agent"), RunOptions{})
		require.Zero(t, code, stdout)
		require.NotContains(t, stdout+stderr, "current ID", "a result with a row has nothing to explain")
	}
}

// The query get snapshots sends selects the breakpoint by its immutable id,
// padded to the width a snapshot carries it in, and nothing else; --limit is
// appended to it.
func TestGetSnapshots_QueriesTheBreakpointsOwnSnapshots(t *testing.T) {
	const query = `fetch application.snapshots | filter breakpoint.id == toUid("000000000000000000000000abc12345")`
	for _, tc := range []struct {
		args []string
		want string
	}{
		{nil, query},
		{[]string{"--limit", "5"}, query + " | limit 5"},
	} {
		srv := newLiveDebuggerStub(t, "readonly")
		srv.rules = []interface{}{ruleAt("A.java", 1, "")}
		t.Chdir(t.TempDir())

		code, stdout, stderr := runRepoScoped(slices.Concat([]string{"get", "snapshots", "A.java:1", "--plain"}, tc.args), RunOptions{})
		require.Zero(t, code, "stdout: %s\nstderr: %s", stdout, stderr)
		require.Equal(t, []string{tc.want}, srv.sentQueries())
	}
}

// A flag value that cannot be used is refused before anything is read: the
// first request a breakpoint command sends finds its workspace, and creates
// one where there is none.
func TestGetSnapshots_ValidatesItsFlagsBeforeAnyRequest(t *testing.T) {
	for _, tc := range []struct{ flag, want string }{
		{"--decode-snapshots=bogus", `unsupported --decode-snapshots value "bogus" (use "simplified" or "full")`},
		{"--metadata=bogus", "bogus"},
	} {
		srv := newLiveDebuggerStub(t, "readonly")
		srv.rules = []interface{}{ruleAt("A.java", 1, "")}
		t.Chdir(t.TempDir())

		code, stdout, stderr := runRepoScoped([]string{"get", "snapshots", "A.java:1", tc.flag, "--agent"}, RunOptions{})
		require.NotZero(t, code)
		require.Contains(t, agentError(t, stdout).Message, tc.want)
		require.Empty(t, srv.ops())
		require.Empty(t, srv.sentQueries())
		require.Empty(t, stderr)
	}
}
