//go:build integration
// +build integration

package e2e

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Dynatrace/dtctl/test/integration"
)

// The Live Debugger surface is classified experimental, which makes it the
// first real command family the stability floor governs. test/integration
// covers the floor's mechanics against an unroutable environment; this file
// covers the half that only a real tenant can answer: that the tier is a
// contract statement and not a functional change — the commands still reach
// the live API at the default floor — and that a stable floor refuses them
// *before* the request rather than after.
//
// Everything here is read-only. `get breakpoints` resolves the Live Debugger
// workspace for the current directory, which is idempotent (get-or-create) and
// the only write anywhere in this file; no breakpoint is ever created, because
// a breakpoint on a shared tenant is live instrumentation on someone else's
// process.

// liveDebuggerEnv returns the tenant URL and token the integration suite is
// configured with, skipping when the suite is not configured at all. It reads
// the variables directly rather than through SetupIntegration because these
// tests drive the CLI binary, which needs a config file rather than a client.
func liveDebuggerEnv(t *testing.T) (string, string) {
	t.Helper()
	envURL := os.Getenv("DTCTL_INTEGRATION_ENV")
	if envURL == "" {
		t.Skip("Skipping integration test: DTCTL_INTEGRATION_ENV not set")
	}
	token := os.Getenv("DTCTL_INTEGRATION_TOKEN")
	if token == "" {
		t.Skip("Skipping integration test: DTCTL_INTEGRATION_TOKEN not set")
	}
	return envURL, token
}

// TestLiveDebuggerExperimentalAgainstTenant pins the tier's real-world effect
// on the Live Debugger commands: reachable at the default floor, withheld
// under a stable one, restored by a per-command exception.
func TestLiveDebuggerExperimentalAgainstTenant(t *testing.T) {
	if testing.Short() {
		t.Skip("builds the dtctl binary; skipped in -short mode")
	}
	envURL, token := liveDebuggerEnv(t)
	exe := integration.BuildDtctl(t)

	newConfig := func(floor string, exceptions []string) string {
		return integration.WriteCLIConfig(t, integration.CLIConfig{
			Environment:         envURL,
			Token:               token,
			MinStability:        floor,
			StabilityExceptions: exceptions,
		})
	}

	t.Run("reaches the live API at the default floor", func(t *testing.T) {
		cfg := newConfig("", nil)
		code, stdout, stderr := integration.RunDtctl(t, exe, cfg, nil, "get", "breakpoints", "--agent")
		out := stdout + stderr

		// The classification must not have made the command unusable. A
		// tenant-side refusal (Live Debugger not licensed or not enabled) is a
		// legitimate outcome and not what this test is about; a stability
		// block is a regression.
		require.NotContains(t, out, "stability_blocked",
			"the default floor admits experimental surface; got: %s", out)
		if code != 0 {
			t.Skipf("Live Debugger unavailable on this tenant, tier still verified: %s", out)
		}

		var envelope struct {
			OK bool `json:"ok"`
		}
		require.NoError(t, json.Unmarshal([]byte(stdout), &envelope), stdout)
		require.True(t, envelope.OK, "expected a successful envelope: %s", stdout)
	})

	t.Run("a stable floor refuses before the request", func(t *testing.T) {
		cfg := newConfig("stable", nil)
		code, stdout, stderr := integration.RunDtctl(t, exe, cfg, nil, "get", "breakpoints", "--agent")
		require.NotZero(t, code, "%s%s", stdout, stderr)

		var envelope struct {
			OK    bool `json:"ok"`
			Error struct {
				Code    string `json:"code"`
				Message string `json:"message"`
			} `json:"error"`
		}
		require.NoError(t, json.Unmarshal([]byte(stdout), &envelope), stdout)
		require.False(t, envelope.OK)
		require.Equal(t, "stability_blocked", envelope.Error.Code)

		// The point of masking at registration rather than inside the handler:
		// with real credentials in hand, the refusal still carries no tenant
		// data and no auth outcome, because no request was made.
		out := stdout + stderr
		require.NotContains(t, out, "401")
		require.NotContains(t, out, "403")
		require.NotContains(t, strings.ToLower(out), "workspace")
	})

	t.Run("a per-command exception restores it against the tenant", func(t *testing.T) {
		cfg := newConfig("stable", []string{"get breakpoints"})
		_, stdout, stderr := integration.RunDtctl(t, exe, cfg, nil, "get", "breakpoints", "--agent")
		out := stdout + stderr
		require.NotContains(t, out, "stability_blocked",
			"the exception must admit the command: %s", out)

		// Its siblings are not admitted with it, even holding valid
		// credentials for all of them.
		_, stdout, stderr = integration.RunDtctl(t, exe, cfg, nil, "get", "snapshots", "Example.java:1", "--agent")
		require.Contains(t, stdout+stderr, "stability_blocked",
			"an exception is per-command, not per-family")
	})
}

// TestQueryDecodeSnapshotsAgainstTenant covers the per-flag half of the
// classification on a live tenant: `query` is stable and keeps working, while
// its Live Debugger decoding flag is experimental and follows the floor.
func TestQueryDecodeSnapshotsAgainstTenant(t *testing.T) {
	if testing.Short() {
		t.Skip("builds the dtctl binary; skipped in -short mode")
	}
	envURL, token := liveDebuggerEnv(t)
	exe := integration.BuildDtctl(t)

	// A read-only query that any tenant answers, so the assertion is about the
	// flag rather than about the data.
	const dql = "fetch logs, from:-5m | limit 1"

	newConfig := func(floor string, exceptions []string) string {
		return integration.WriteCLIConfig(t, integration.CLIConfig{
			Environment:         envURL,
			Token:               token,
			MinStability:        floor,
			StabilityExceptions: exceptions,
		})
	}

	t.Run("usable at the default floor", func(t *testing.T) {
		cfg := newConfig("", nil)
		code, stdout, stderr := integration.RunDtctl(t, exe, cfg, nil,
			"query", dql, "--decode-snapshots", "--agent")
		out := stdout + stderr
		require.NotContains(t, out, "stability_blocked", out)
		require.Zero(t, code, "the flag must still work on a real query: %s", out)

		var envelope struct {
			OK bool `json:"ok"`
		}
		require.NoError(t, json.Unmarshal([]byte(stdout), &envelope), stdout)
		require.True(t, envelope.OK, stdout)
	})

	t.Run("withheld under a stable floor while the query still runs", func(t *testing.T) {
		cfg := newConfig("stable", nil)

		code, stdout, stderr := integration.RunDtctl(t, exe, cfg, nil,
			"query", dql, "--decode-snapshots", "--agent")
		require.NotZero(t, code)
		require.Contains(t, stdout+stderr, "stability_blocked")
		require.Contains(t, stdout+stderr, "--decode-snapshots")

		// The command carrying the flag is stable, so the same floor must
		// leave it fully functional against the tenant. A floor that took out
		// `query` along with its experimental flag would be unusable.
		code, stdout, stderr = integration.RunDtctl(t, exe, cfg, nil, "query", dql, "--agent")
		require.Zero(t, code, "query is stable and must still run: %s%s", stdout, stderr)
	})

	t.Run("a per-flag exception restores it", func(t *testing.T) {
		cfg := newConfig("stable", []string{"query --decode-snapshots"})
		code, stdout, stderr := integration.RunDtctl(t, exe, cfg, nil,
			"query", dql, "--decode-snapshots", "--agent")
		out := stdout + stderr
		require.NotContains(t, out, "stability_blocked", out)
		require.Zero(t, code, out)
	})
}
