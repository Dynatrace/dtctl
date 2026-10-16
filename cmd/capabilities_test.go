package cmd

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestSetCapabilitiesRoundTrip(t *testing.T) {
	prev := SetCapabilities(Capabilities{})
	require.Equal(t, AllCapabilities(), prev, "CLI default must grant every capability")
	require.Equal(t, Capabilities{}, caps)

	restored := SetCapabilities(prev)
	require.Equal(t, Capabilities{}, restored, "SetCapabilities must return the value it replaced")
	require.Equal(t, AllCapabilities(), caps)
}

func TestLaunchEditorCapabilityDisabled(t *testing.T) {
	prev := SetCapabilities(Capabilities{})
	t.Cleanup(func() { SetCapabilities(prev) })

	err := launchEditor(context.Background(), "vim", filepath.Join(t.TempDir(), "x.yaml"))
	var capErr *CapabilityError
	require.ErrorAs(t, err, &capErr)
	require.Contains(t, capErr.Error(), "interactive editing")
	require.Contains(t, capErr.Error(), "not available in this environment")
}

func TestPluginDispatchCapabilityDisabled(t *testing.T) {
	prev := SetCapabilities(Capabilities{})
	t.Cleanup(func() { SetCapabilities(prev) })

	// With the capability off the gate must reject before any PATH lookup,
	// so even a name that would resolve to a plugin falls through to the
	// normal unknown-command error path.
	code, handled := tryPluginDispatch(context.Background(), []string{"definitely-not-a-builtin"})
	require.False(t, handled, "dispatch must decline when PluginDispatch is off")
	require.Zero(t, code)
}

func TestErrorToDetailCapabilityError(t *testing.T) {
	err := &CapabilityError{Feature: "apply hooks"}
	detail := errorToDetail(context.Background(), err)
	require.Equal(t, "capability_disabled", detail.Code)
	require.Equal(t, err.Error(), detail.Message)

	// Wrapped errors must still map (errors.As semantics).
	detail = errorToDetail(context.Background(), errors.Join(errors.New("outer"), err))
	require.Equal(t, "capability_disabled", detail.Code)
}

// TestSubprocessSpawnsConfinedToGateways is the E3 guard: every subprocess
// spawn in cmd/ must live in one of the known capability-gated gateway files.
// A spawn appearing anywhere else is a new escape hatch that bypasses the
// Capabilities model and must either move behind an existing gateway or get
// its own capability + gate before landing.
func TestSubprocessSpawnsConfinedToGateways(t *testing.T) {
	// file → the capability that gates its spawn(s).
	gateways := map[string]string{
		"alias_resolve.go":        "ShellAliases (gated at the executeArgs() call site in root.go)",
		"edit.go":                 "Editor (gated in launchEditor)",
		"exec_forward_unix.go":    "PluginDispatch (only called from tryPluginDispatch)",
		"exec_forward_windows.go": "PluginDispatch (only called from tryPluginDispatch)",
		"open_intent.go":          "BrowserOpen (gated in openBrowser)",
	}

	entries, err := os.ReadDir(".")
	require.NoError(t, err)

	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		src, err := os.ReadFile(filepath.Clean(name))
		require.NoError(t, err)

		spawns := strings.Count(string(src), "exec.Command(") +
			strings.Count(string(src), "syscall.Exec(") +
			strings.Count(string(src), "exec.CommandContext(")
		if spawns == 0 {
			continue
		}
		require.Contains(t, gateways, name,
			"%s spawns a subprocess outside the capability gateways — gate it behind cmd.Capabilities", name)
	}
}
