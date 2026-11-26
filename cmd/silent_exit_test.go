package cmd

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Dynatrace/dtctl/cmd/testutil"
)

// TestNoOsExitOutsideExecute is the E2 guard: command bodies must return a
// *silentExitError (or a regular error) instead of calling os.Exit, so the
// command tree stays embeddable — an in-process caller (engine, serve) must
// never have its process terminated by a command. Only Execute() in root.go
// may translate the code into os.Exit.
func TestNoOsExitOutsideExecute(t *testing.T) {
	entries, err := os.ReadDir(".")
	require.NoError(t, err)

	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		src, err := os.ReadFile(filepath.Clean(name))
		require.NoError(t, err)

		count := strings.Count(string(src), "os.Exit(")
		if name == "root.go" {
			require.Equal(t, 1, count,
				"root.go must contain exactly one os.Exit (in Execute); found %d", count)
			continue
		}
		require.Zero(t, count,
			"%s calls os.Exit — return a *silentExitError instead (see silentExitError doc)", name)
	}
}

func TestDiffExitCodeViaSilentError(t *testing.T) {
	dir := t.TempDir()
	a := filepath.Join(dir, "a.yaml")
	b := filepath.Join(dir, "b.yaml")
	require.NoError(t, os.WriteFile(a, []byte("name: one\nvalue: 1\n"), 0o600))
	require.NoError(t, os.WriteFile(b, []byte("name: one\nvalue: 2\n"), 0o600))

	t.Cleanup(func() { testutil.ResetCommandFlags(diffCmd) })

	// Differences found: diff(1) semantics — silent exit code 1, no message.
	rootCmd.SetArgs([]string{"diff", "-f", a, "-f", b})
	err := rootCmd.Execute()
	var silent *silentExitError
	require.ErrorAs(t, err, &silent, "diff with changes must return *silentExitError")
	require.Equal(t, ExitCodeHasDiff, silent.code)
	require.Empty(t, silent.Error(), "silent exit errors must not print")

	// Identical inputs: success, no error.
	testutil.ResetCommandFlags(diffCmd)
	rootCmd.SetArgs([]string{"diff", "-f", a, "-f", a})
	require.NoError(t, rootCmd.Execute())
}

func TestExitCodeForErrorMapsSilentExit(t *testing.T) {
	err := &silentExitError{code: 42, reason: "test"}
	require.Equal(t, 42, exitCodeForError(err))
	// Wrapped silent errors must still be found.
	wrapped := errors.Join(err)
	require.Equal(t, 42, exitCodeForError(wrapped))
}
