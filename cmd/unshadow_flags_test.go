package cmd

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"

	"github.com/Dynatrace/dtctl/cmd/testutil"
	"github.com/Dynatrace/dtctl/pkg/auth"
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
	// Isolate from the host: no stored-credential or client-credentials env, so
	// the interactive path is the one under test, and that path is stubbed.
	for _, name := range []string{
		config.EnvTokenStorage, config.EnvDisableKeyring, "DTCTL_TOKEN",
		envLoginClientID, envLoginClientSecret, envLoginAccountURN,
	} {
		t.Setenv(name, "")
	}
	t.Setenv("DTCTL_CONTEXT", "fresh")

	origCheck, origBrowser := authCheckKeyringFunc, authBrowserFlowFunc
	t.Cleanup(func() { authCheckKeyringFunc, authBrowserFlowFunc = origCheck, origBrowser })
	authCheckKeyringFunc = func() error { return nil }
	errStub := errors.New("stub: browser flow reached")
	authBrowserFlowFunc = func(context.Context, *auth.OAuthFlow) (*auth.TokenSet, error) {
		return nil, errStub
	}

	gFlags.cfgFile = filepath.Join(t.TempDir(), "missing.yaml")
	t.Cleanup(func() { gFlags.cfgFile = "" })

	rootCmd.SetArgs([]string{"auth", "login", "--environment", "https://abc12345.apps.dynatrace.com"})
	err := rootCmd.Execute()
	// Context resolution succeeded without any config: login got as far as the
	// (stubbed) browser flow.
	require.ErrorIs(t, err, errStub)
}

func TestDiffRejectsJSONOutputWithOtherFormats(t *testing.T) {
	dir := t.TempDir()
	a := filepath.Join(dir, "a.yaml")
	require.NoError(t, os.WriteFile(a, []byte("name: one\n"), 0o600))
	for _, flag := range []string{"--side-by-side", "--semantic"} {
		t.Run(flag, func(t *testing.T) {
			resetGlobalOutputFlag(t)
			rootCmd.SetArgs([]string{"diff", "-f", a, "-f", a, "-o", "json", flag})
			err := rootCmd.Execute()
			require.Error(t, err)
			require.Contains(t, err.Error(), "cannot be combined")
		})
	}
}

func TestDiffRejectsJSONOutputWithConflictingFormat(t *testing.T) {
	dir := t.TempDir()
	a := filepath.Join(dir, "a.yaml")
	require.NoError(t, os.WriteFile(a, []byte("name: one\n"), 0o600))
	for _, format := range []string{"unified", "side-by-side", "semantic"} {
		t.Run(format, func(t *testing.T) {
			resetGlobalOutputFlag(t)
			rootCmd.SetArgs([]string{"diff", "-f", a, "-f", a, "-o", "json", "--format", format})
			err := rootCmd.Execute()
			require.Error(t, err)
			require.Contains(t, err.Error(), "--format json-patch")
		})
	}
	t.Run("json-patch agrees", func(t *testing.T) {
		resetGlobalOutputFlag(t)
		rootCmd.SetArgs([]string{"diff", "-f", a, "-f", a, "-o", "json", "--format", "json-patch"})
		require.NoError(t, rootCmd.Execute())
	})
}

func TestDiffRejectsJSONPatchWithIgnoreOrder(t *testing.T) {
	dir := t.TempDir()
	a := filepath.Join(dir, "a.yaml")
	require.NoError(t, os.WriteFile(a, []byte("name: one\n"), 0o600))
	for _, args := range [][]string{
		{"-o", "json"},
		{"--format", "json-patch"},
	} {
		t.Run(args[0], func(t *testing.T) {
			resetGlobalOutputFlag(t)
			rootCmd.SetArgs(append([]string{"diff", "-f", a, "-f", a, "--ignore-order"}, args...))
			err := rootCmd.Execute()
			require.Error(t, err)
			require.Contains(t, err.Error(), "--ignore-order")
		})
	}
}
