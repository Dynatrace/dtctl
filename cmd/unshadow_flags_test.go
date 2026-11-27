package cmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"

	"github.com/Dynatrace/dtctl/cmd/testutil"
	"github.com/Dynatrace/dtctl/pkg/config"
)

func resetGlobalOutputFlag(t *testing.T) {
	t.Helper()
	t.Cleanup(func() {
		if f := rootCmd.PersistentFlags().Lookup("output"); f != nil {
			_ = f.Value.Set(f.DefValue)
			f.Changed = false
		}
		testutil.ResetCommandFlags(diffCmd)
	})
}

func TestDiffHonoursGlobalOutputJSON(t *testing.T) {
	dir := t.TempDir()
	a, b := filepath.Join(dir, "a.yaml"), filepath.Join(dir, "b.yaml")
	require.NoError(t, os.WriteFile(a, []byte("name: one\nvalue: 1\n"), 0o600))
	require.NoError(t, os.WriteFile(b, []byte("name: one\nvalue: 2\n"), 0o600))
	resetGlobalOutputFlag(t)

	var runErr error
	out := captureStdout(t, func() {
		rootCmd.SetArgs([]string{"diff", "-f", a, "-f", b, "-o", "json"})
		runErr = rootCmd.Execute()
	})
	var silent *silentExitError
	require.ErrorAs(t, runErr, &silent)
	require.Contains(t, out, `"op"`, "-o json must print the JSON patch, got: %s", out)
}

func TestDiffRejectsUnsupportedGlobalOutput(t *testing.T) {
	dir := t.TempDir()
	a := filepath.Join(dir, "a.yaml")
	require.NoError(t, os.WriteFile(a, []byte("name: one\n"), 0o600))
	resetGlobalOutputFlag(t)

	rootCmd.SetArgs([]string{"diff", "-f", a, "-f", a, "-o", "yaml"})
	err := rootCmd.Execute()
	require.Error(t, err)
	require.Contains(t, err.Error(), "supported: json")
}

func TestConfigInitReadsContextFromEnv(t *testing.T) {
	tmp := t.TempDir()
	orig, err := os.Getwd()
	require.NoError(t, err)
	require.NoError(t, os.Chdir(tmp))
	t.Cleanup(func() { _ = os.Chdir(orig); testutil.ResetCommandFlags(configInitCmd) })
	t.Setenv("DTCTL_CONTEXT", "from-env")

	rootCmd.SetArgs([]string{"config", "init"})
	require.NoError(t, rootCmd.Execute())

	data, err := os.ReadFile(filepath.Join(tmp, config.LocalConfigName))
	require.NoError(t, err)
	var cfg config.Config
	require.NoError(t, yaml.Unmarshal(data, &cfg))
	require.Equal(t, "from-env", cfg.CurrentContext)
}

func TestAuthLoginContextFromEnvWithoutConfig(t *testing.T) {
	resetAuthLoginFlags(t)
	t.Setenv("DTCTL_CONTEXT", "fresh")
	t.Setenv("DTCTL_DISABLE_KEYRING", "1")
	gFlags.cfgFile = filepath.Join(t.TempDir(), "missing.yaml")
	t.Cleanup(func() { gFlags.cfgFile = "" })

	rootCmd.SetArgs([]string{"auth", "login", "--environment", "https://abc12345.apps.dynatrace.com"})
	err := rootCmd.Execute()
	// Login itself cannot finish in a unit test; it must get past context
	// resolution, which is what failed before.
	if err != nil {
		require.False(t, strings.Contains(err.Error(), "--context and --environment are required"), err.Error())
	}
}
