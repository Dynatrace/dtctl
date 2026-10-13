package cmd

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// An empty result for a breakpoint that was updated or hit its limit says
// where its earlier snapshots are, on stderr and in the agent envelope; one
// that has not been hit yet has no earlier ID to point at.
func TestGetSnapshots_ReversionedBreakpoint(t *testing.T) {
	const hint = `0 snapshots under the breakpoint's current ID (HIT_LIMIT_REACHED); earlier ones are under its previous IDs: dtctl query 'fetch application.snapshots | summarize count(), by:{breakpoint.id}' --default-timeframe-start now-7d`
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
	}
}
