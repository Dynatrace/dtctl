package cmd

import (
	"bytes"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Dynatrace/dtctl/pkg/client"
)

// TestRunBlockedCommand_Guard: a blocked top-level command must fail with the
// signposted "not supported" message carrying the caller's reason — not run,
// and not degrade to a generic unknown-command error.
func TestRunBlockedCommand_Guard(t *testing.T) {
	isolatedConfig(t, "contexts: []\n")

	var stdout, stderr bytes.Buffer
	code := Run([]string{"config"}, RunOptions{
		BlockedCommands: map[string]string{"config": "it manages host state"},
		Stdout:          &stdout,
		Stderr:          &stderr,
	})
	require.Equal(t, client.ExitUsageError, code)
	require.Contains(t, stderr.String(), `command "config" is not supported in this environment`)
	require.Contains(t, stderr.String(), "it manages host state",
		"the caller's reason must reach the user")
}

// TestRunBlockedCommand_Subtree: blocking a top-level command blocks its whole
// subtree, and the error names the full command path.
func TestRunBlockedCommand_Subtree(t *testing.T) {
	isolatedConfig(t, "contexts: []\n")

	var stderr bytes.Buffer
	code := Run([]string{"config", "view"}, RunOptions{
		BlockedCommands: map[string]string{"config": "host config"},
		Stderr:          &stderr,
	})
	require.NotZero(t, code)
	require.Contains(t, stderr.String(), `command "config view" is not supported`)
}

// TestRunBlockedCommand_ArgAndFlagShapeDoesNotLeak: the guard must win over
// Cobra's arg validation and flag parsing — a wrong arg count or unknown flag
// on a blocked command still yields the block, not a shape error.
func TestRunBlockedCommand_ArgAndFlagShapeDoesNotLeak(t *testing.T) {
	isolatedConfig(t, "contexts: []\n")

	var stderr bytes.Buffer
	code := Run([]string{"config", "--bogus-flag", "too", "many", "args"}, RunOptions{
		BlockedCommands: map[string]string{"config": "host state"},
		Stderr:          &stderr,
	})
	require.NotZero(t, code)
	require.Contains(t, stderr.String(), "is not supported in this environment")
	require.NotContains(t, stderr.String(), "unknown flag")
}

// TestRunBlockedCommand_AgentEnvelope: with --agent the block renders as a
// structured envelope on stdout with the stable code. The flag never reaches
// Cobra parsing (the mask disables it), so this exercises the raw-argv
// fallback in executeArgs.
func TestRunBlockedCommand_AgentEnvelope(t *testing.T) {
	isolatedConfig(t, "contexts: []\n")

	var stdout bytes.Buffer
	code := Run([]string{"config", "--agent"}, RunOptions{
		BlockedCommands: map[string]string{"config": "it manages host state"},
		Stdout:          &stdout,
	})
	require.Equal(t, client.ExitUsageError, code)
	require.Contains(t, stdout.String(), `"code":"unsupported_in_service"`)
	require.Contains(t, stdout.String(), `"ok":false`)
}

// TestRunBlockedCommand_HiddenFromCatalog: a blocked command disappears from
// the `dtctl commands` catalog — an agent bootstrapping from the catalog must
// not be steered toward commands it cannot run.
func TestRunBlockedCommand_HiddenFromCatalog(t *testing.T) {
	isolatedConfig(t, "contexts: []\n")

	var withBlock bytes.Buffer
	code := Run([]string{"commands"}, RunOptions{
		BlockedCommands: map[string]string{"doctor": "host diagnostics"},
		Stdout:          &withBlock,
	})
	require.Zero(t, code)
	require.NotContains(t, withBlock.String(), "doctor")
	require.Contains(t, withBlock.String(), "get", "unblocked commands stay listed")

	// The next unrestricted run sees the full surface again (pristine restore).
	var without bytes.Buffer
	code = Run([]string{"commands"}, RunOptions{Stdout: &without})
	require.Zero(t, code)
	require.Contains(t, without.String(), "doctor")
}

// TestRunBlockedCommand_NextRunUnaffected: the mask is strictly per-invocation
// — the same command runs normally when the next caller doesn't block it.
func TestRunBlockedCommand_NextRunUnaffected(t *testing.T) {
	isolatedConfig(t, "contexts: []\n")

	var stderr bytes.Buffer
	code := Run([]string{"config"}, RunOptions{
		BlockedCommands: map[string]string{"config": "host state"},
		Stderr:          &stderr,
	})
	require.NotZero(t, code)

	var stdout bytes.Buffer
	code = Run([]string{"config", "--help"}, RunOptions{Stdout: &stdout})
	require.Zero(t, code, "config must work again without the block")
	require.True(t, strings.Contains(stdout.String(), "config"),
		"help for the unblocked command must render")
}
