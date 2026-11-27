package cmd

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/adrg/xdg"
	"github.com/spf13/pflag"
	"github.com/spf13/viper"

	"github.com/Dynatrace/dtctl/pkg/auth"
	"github.com/Dynatrace/dtctl/pkg/client"
	"github.com/Dynatrace/dtctl/pkg/config"
	"github.com/Dynatrace/dtctl/pkg/diagnostic"
)

// setupAuthTestConfig creates a temporary config with the given context and returns the path.
func setupAuthTestConfig(t *testing.T, contextName, environment, tokenRef string) string {
	t.Helper()
	t.Setenv("DTCTL_DISABLE_KEYRING", "1")
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config.yaml")

	cfg := config.NewConfig()
	cfg.SetContext(contextName, environment, tokenRef)
	cfg.CurrentContext = contextName

	if err := cfg.SaveTo(configPath); err != nil {
		t.Fatalf("failed to save test config: %v", err)
	}
	return configPath
}

// setXDGHome points one XDG base directory (XDG_DATA_HOME, XDG_CONFIG_HOME) at
// dir for the rest of the test and makes the xdg package follow.
func setXDGHome(t *testing.T, key, dir string) {
	t.Helper()
	// Registered before t.Setenv so that it runs after the variable is
	// restored. The other way round the reload still sees dir, and xdg keeps
	// that deleted path for every later test in the package.
	t.Cleanup(xdg.Reload)
	t.Setenv(key, dir)
	xdg.Reload()
}

// TestSetXDGHomeRestoresThePaths: a test that moves an XDG directory must hand
// the next test the paths it found.
func TestSetXDGHomeRestoresThePaths(t *testing.T) {
	xdg.Reload()
	dataHome, configHome := xdg.DataHome, xdg.ConfigHome

	var dataDir, configDir string
	t.Run("moved", func(t *testing.T) {
		dataDir, configDir = t.TempDir(), t.TempDir()
		setXDGHome(t, "XDG_CONFIG_HOME", configDir)
		setXDGHome(t, "XDG_DATA_HOME", dataDir)
		if xdg.DataHome != dataDir || xdg.ConfigHome != configDir {
			t.Fatalf("xdg.DataHome %q, xdg.ConfigHome %q; want %q and %q", xdg.DataHome, xdg.ConfigHome, dataDir, configDir)
		}
	})

	if xdg.DataHome != dataHome {
		t.Errorf("xdg.DataHome is %q after the test, want %q", xdg.DataHome, dataHome)
	}
	if xdg.ConfigHome != configHome {
		t.Errorf("xdg.ConfigHome is %q after the test, want %q", xdg.ConfigHome, configHome)
	}
}

// resetAuthLoginFlags resets all auth login command flags to their default values.
// This is needed because cobra/pflag retains flag values across Execute() calls when
// the global rootCmd is reused in tests — a flag not present in the args of a new
// Execute() call keeps the value set by the previous call.
//
// It resets them again when the test is done: the test that runs next may be
// one that does not reset them itself, and would log in with this test's
// --context.
func resetAuthLoginFlags(t *testing.T) {
	t.Helper()
	clearAuthLoginFlags(t)
	t.Cleanup(func() { clearAuthLoginFlags(t) })
}

// clearAuthLoginFlags is the reset resetAuthLoginFlags does twice.
func clearAuthLoginFlags(t *testing.T) {
	t.Helper()
	for _, name := range []string{"context", "environment", "token-name", "timeout", "safety-level", "client-id", "client-secret", "account-urn", "scopes"} {
		if f := authLoginCmd.Flags().Lookup(name); f != nil {
			// Set appends for slice flags rather than replacing, so reset those
			// through the interface that clears them.
			if sv, ok := f.Value.(pflag.SliceValue); ok {
				if err := sv.Replace(nil); err != nil {
					t.Logf("warning: could not reset slice flag %q: %v", name, err)
				}
				f.Changed = false
				continue
			}
			// A flag that rejects an empty value refuses Set(""), so it
			// resets through its own Reset.
			if rv, ok := f.Value.(interface{ Reset() }); ok {
				rv.Reset()
				f.Changed = false
				continue
			}
			if err := f.Value.Set(f.DefValue); err != nil {
				t.Logf("warning: could not reset flag %q: %v", name, err)
			}
			f.Changed = false
		}
	}
}

// TestAuthLogin_FlagValidation checks that the login command fails correctly when
// neither flags nor a current context are available.
func TestAuthLogin_FlagValidation(t *testing.T) {
	tests := []struct {
		name        string
		args        []string
		setupConfig bool // whether to write a config with a current context
		wantErrSub  string
		wantHint    string
	}{
		{
			name:        "no flags no config errors helpfully",
			args:        []string{"auth", "login"},
			setupConfig: false,
			wantErrSub:  "--context and --environment are required",
			wantHint:    "dtctl ctx",
		},
		{
			name:        "no flags empty current context errors helpfully",
			args:        []string{"auth", "login"},
			setupConfig: true, // config exists but CurrentContext is empty (handled below)
			wantErrSub:  "--context and --environment are required when no current context is set",
			wantHint:    "dtctl ctx",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			viper.Reset()
			resetAuthLoginFlags(t)

			if tt.name == "no flags no config errors helpfully" {
				// Point to a non-existent config file
				tmpDir := t.TempDir()
				gFlags.cfgFile = filepath.Join(tmpDir, "nonexistent.yaml")
			} else {
				// Config with no current context set
				tmpDir := t.TempDir()
				configPath := filepath.Join(tmpDir, "config.yaml")
				cfg := config.NewConfig()
				// Deliberately leave cfg.CurrentContext empty
				if err := cfg.SaveTo(configPath); err != nil {
					t.Fatalf("failed to save config: %v", err)
				}
				gFlags.cfgFile = configPath
			}

			rootCmd.SetArgs(tt.args)
			err := rootCmd.Execute()

			if err == nil {
				t.Fatal("expected error but got nil")
			}
			if !strings.Contains(err.Error(), tt.wantErrSub) {
				t.Errorf("expected error containing %q, got: %v", tt.wantErrSub, err)
			}
			if !strings.Contains(err.Error(), tt.wantHint) {
				t.Errorf("expected error to contain hint %q, got: %v", tt.wantHint, err)
			}

			// Reset
			gFlags.cfgFile = ""
		})
	}
}

// TestAuthLogin_CurrentContextFallback verifies that the login command derives
// context name, environment URL and token name from the active context when no
// flags are provided.  The test stops before the actual OAuth flow (keyring
// unavailable) but checks the context resolution logic produces the expected
// error – i.e. it gets past flag validation.
func TestAuthLogin_CurrentContextFallback(t *testing.T) {
	viper.Reset()
	resetAuthLoginFlags(t)

	const (
		ctxName  = "my-context"
		envURL   = "https://abc12345.apps.dynatrace.com"
		tokenRef = "my-context-oauth"
	)

	configPath := setupAuthTestConfig(t, ctxName, envURL, tokenRef)
	gFlags.cfgFile = configPath
	defer func() { gFlags.cfgFile = "" }()

	// Execute with no flags – should pass flag validation and reach the
	// keyring check (which fails in a test environment).
	rootCmd.SetArgs([]string{"auth", "login"})
	err := rootCmd.Execute()

	// We expect the command to fail, but NOT because of missing flags.
	// It should fail later (keyring unavailable or similar infrastructure error).
	if err == nil {
		// Unlikely in a unit test environment without a real keyring/browser,
		// but not a failure of the logic we are testing.
		return
	}

	if strings.Contains(err.Error(), "--context and --environment are required") {
		t.Errorf("expected current-context fallback to work, but got flag validation error: %v", err)
	}

	// The error should reference the token storage env var or the keyring disable env var
	// as a hint for how to resolve the issue.
	errMsg := err.Error()
	hasStorageHint := strings.Contains(errMsg, config.EnvTokenStorage) || strings.Contains(errMsg, config.EnvDisableKeyring)
	if strings.Contains(errMsg, "keyring") && !hasStorageHint {
		t.Errorf("expected error to include storage hint (%s or %s), got: %v", config.EnvTokenStorage, config.EnvDisableKeyring, err)
	}
}

// TestAuthLogin_PartialFlags verifies that supplying only --context (without
// --environment) causes the missing value to be filled from the current context.
func TestAuthLogin_PartialFlags_EnvironmentFromContext(t *testing.T) {
	viper.Reset()
	resetAuthLoginFlags(t)

	const (
		ctxName  = "my-context"
		envURL   = "https://abc12345.apps.dynatrace.com"
		tokenRef = "my-context-oauth"
	)

	configPath := setupAuthTestConfig(t, ctxName, envURL, tokenRef)
	gFlags.cfgFile = configPath
	defer func() { gFlags.cfgFile = "" }()

	// --context is the active context, so environment resolution uses its own URL.
	rootCmd.SetArgs([]string{"auth", "login", "--context", ctxName})
	err := rootCmd.Execute()

	if err != nil && strings.Contains(err.Error(), "--context and --environment are required") {
		t.Errorf("expected environment to be filled from named context, got: %v", err)
	}
}

// TestAuthLogin_KeyringRecovery verifies that the auth login command
// attempts to create a keyring collection when CheckKeyring returns an
// error containing ErrMsgCollectionUnlock, and that a successful recovery
// allows the flow to continue past the keyring gate.
func TestAuthLogin_KeyringRecovery(t *testing.T) {
	viper.Reset()
	resetAuthLoginFlags(t)

	const (
		ctxName = "recover-ctx"
		envURL  = "https://abc12345.apps.dynatrace.com"
	)

	configPath := setupAuthTestConfig(t, ctxName, envURL, ctxName+"-oauth")
	gFlags.cfgFile = configPath
	defer func() { gFlags.cfgFile = "" }()

	// Track whether EnsureKeyringCollection was called.
	ensureCalled := false

	// First call to CheckKeyring returns the unlock error; after recovery
	// it returns nil (simulating a fixed keyring).
	calls := 0
	origCheck := authCheckKeyringFunc
	origEnsure := authEnsureKeyringFunc
	defer func() {
		authCheckKeyringFunc = origCheck
		authEnsureKeyringFunc = origEnsure
	}()

	authCheckKeyringFunc = func() error {
		calls++
		if calls == 1 {
			return fmt.Errorf("keyring probe failed: %s", config.ErrMsgCollectionUnlock)
		}
		return nil // recovered
	}
	authEnsureKeyringFunc = func(_ context.Context) error {
		ensureCalled = true
		return nil
	}

	// Past the keyring gate the command starts the browser flow. Stub it, or
	// the test opens the developer's browser against the real SSO and blocks
	// until the flow's 5-minute timeout.
	browserFlowCalled := false
	origFlow := authBrowserFlowFunc
	defer func() { authBrowserFlowFunc = origFlow }()
	authBrowserFlowFunc = func(_ context.Context, _ *auth.OAuthFlow) (*auth.TokenSet, error) {
		browserFlowCalled = true
		return nil, errors.New("browser flow stubbed in test")
	}

	rootCmd.SetArgs([]string{"auth", "login", "--context", ctxName, "--environment", envURL})
	err := rootCmd.Execute()

	if !ensureCalled {
		t.Fatal("expected EnsureKeyringCollection to be called during recovery")
	}

	// After recovery the command should proceed past the keyring gate.
	// It will eventually fail further along (no real keyring for token
	// storage, or OAuth infrastructure issues), but not with the initial
	// keyring gate error about requiring a working keyring.
	if err != nil && strings.Contains(err.Error(), "OAuth login requires a working system keyring") {
		t.Errorf("expected recovery to succeed and proceed past keyring gate, got: %v", err)
	}
	if !browserFlowCalled {
		t.Errorf("expected auth login to reach the browser flow after keyring recovery, got: %v", err)
	}
}

// TestFinalizeLoginConfig verifies that after a successful login the config is
// updated correctly and any template placeholder contexts (empty environment)
// are pruned, except for the context that was just logged into.
func TestFinalizeLoginConfig(t *testing.T) {
	tests := []struct {
		name          string
		setupContexts []struct{ name, env, tok string }
		// placeholderNames overrides which context names are treated as prunable
		// placeholders. nil means: derive from setupContexts where env == "".
		// Set explicitly to map[string]bool{} to simulate a context whose env is
		// empty only because its env var was unset at load time (not a true placeholder).
		placeholderNames map[string]bool
		loginContext     string
		loginEnv         string
		loginToken       string
		wantContexts     []string
		wantCurrent      string
	}{
		{
			name: "prunes my-environment placeholder after login",
			setupContexts: []struct{ name, env, tok string }{
				{"my-environment", "", "my-token"},
			},
			loginContext: "dev",
			loginEnv:     "https://abc12345.apps.dynatrace.com/",
			loginToken:   "dev-oauth",
			wantContexts: []string{"dev"},
			wantCurrent:  "dev",
		},
		{
			name: "prunes multiple placeholder contexts",
			setupContexts: []struct{ name, env, tok string }{
				{"my-environment", "", "my-token"},
				{"staging", "", "staging-token"},
				{"prod", "https://prod.apps.dynatrace.com/", "prod-oauth"},
			},
			loginContext: "dev",
			loginEnv:     "https://dev.apps.dynatracelabs.com/",
			loginToken:   "dev-oauth",
			wantContexts: []string{"prod", "dev"},
			wantCurrent:  "dev",
		},
		{
			name: "keeps all contexts with real environments",
			setupContexts: []struct{ name, env, tok string }{
				{"prod", "https://prod.apps.dynatrace.com/", "prod-oauth"},
				{"staging", "https://staging.apps.dynatrace.com/", "staging-oauth"},
			},
			loginContext: "dev",
			loginEnv:     "https://dev.apps.dynatracelabs.com/",
			loginToken:   "dev-oauth",
			wantContexts: []string{"prod", "staging", "dev"},
			wantCurrent:  "dev",
		},
		{
			name:          "no existing contexts — fresh config",
			setupContexts: []struct{ name, env, tok string }{},
			loginContext:  "dev",
			loginEnv:      "https://abc12345.apps.dynatrace.com/",
			loginToken:    "dev-oauth",
			wantContexts:  []string{"dev"},
			wantCurrent:   "dev",
		},
		{
			// Simulates: config has `environment: ${CI_DT_URL}` and CI_DT_URL is
			// unset at login time. os.ExpandEnv produces "" so ci-env IS treated
			// as a placeholder and pruned after login.
			name: "prunes context backed by unset env var",
			setupContexts: []struct{ name, env, tok string }{
				{"ci-env", "", "ci-token"}, // expanded from ${CI_DT_URL} which is unset
			},
			placeholderNames: map[string]bool{"ci-env": true}, // ci-env IS a placeholder: ExpandEnv("${CI_DT_URL}") == ""
			loginContext:     "dev",
			loginEnv:         "https://abc12345.apps.dynatrace.com/",
			loginToken:       "dev-oauth",
			wantContexts:     []string{"dev"},
			wantCurrent:      "dev",
		},
		{
			// Simulates: config has `environment: ${CI_DT_URL}` and CI_DT_URL IS
			// set to a real URL at login time. os.ExpandEnv produces a non-empty URL
			// so ci-env is NOT a placeholder and must be preserved.
			name: "preserves context backed by set env var",
			setupContexts: []struct{ name, env, tok string }{
				{"ci-env", "https://ci.apps.dynatrace.com/", "ci-token"}, // expanded from ${CI_DT_URL} which IS set
			},
			placeholderNames: map[string]bool{}, // ci-env is NOT a placeholder: ExpandEnv("${CI_DT_URL}") != ""
			loginContext:     "dev",
			loginEnv:         "https://abc12345.apps.dynatrace.com/",
			loginToken:       "dev-oauth",
			wantContexts:     []string{"ci-env", "dev"},
			wantCurrent:      "dev",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := config.NewConfig()
			for _, c := range tt.setupContexts {
				cfg.SetContext(c.name, c.env, c.tok)
			}

			placeholderNames := tt.placeholderNames
			if placeholderNames == nil {
				placeholderNames = make(map[string]bool)
				for _, c := range tt.setupContexts {
					if c.env == "" {
						placeholderNames[c.name] = true
					}
				}
			}

			finalizeLoginConfig(cfg, tt.loginContext, tt.loginEnv, tt.loginToken, config.SafetyLevelReadWriteAll, placeholderNames)

			if cfg.CurrentContext != tt.wantCurrent {
				t.Errorf("CurrentContext = %q, want %q", cfg.CurrentContext, tt.wantCurrent)
			}
			if len(cfg.Contexts) != len(tt.wantContexts) {
				got := make([]string, len(cfg.Contexts))
				for i, nc := range cfg.Contexts {
					got[i] = nc.Name
				}
				t.Fatalf("context count = %d %v, want %d %v", len(cfg.Contexts), got, len(tt.wantContexts), tt.wantContexts)
			}
			for i, want := range tt.wantContexts {
				if cfg.Contexts[i].Name != want {
					t.Errorf("contexts[%d].Name = %q, want %q", i, cfg.Contexts[i].Name, want)
				}
			}
			// The login context must always be present with the correct environment.
			nc, err := cfg.GetContext(tt.loginContext)
			if err != nil {
				t.Fatalf("login context %q not found after finalize: %v", tt.loginContext, err)
			}
			if nc.Context.Environment != tt.loginEnv {
				t.Errorf("login context environment = %q, want %q", nc.Context.Environment, tt.loginEnv)
			}
		})
	}
}

// TestResolveLoginContext tests the resolveLoginContext helper that determines
// contextName, environment, and tokenName from an existing config when not all
// values are supplied as explicit CLI flags.
func TestResolveLoginContext(t *testing.T) {
	const (
		prodURL = "https://abc12345.apps.dynatrace.com/"
		hardURL = "https://xyz67890.sprint.apps.dynatracelabs.com/"
		prodTok = "prod-env-oauth"
		hardTok = "sprint-env-oauth"
	)

	makeCfg := func() *config.Config {
		cfg := config.NewConfig()
		cfg.SetContext("prod-env", prodURL, prodTok)
		cfg.SetContext("sprint-env", hardURL, hardTok)
		cfg.CurrentContext = "prod-env"
		return cfg
	}

	tests := []struct {
		name        string
		contextName string
		environment string
		tokenName   string
		currentCtx  string // override CurrentContext ("" means use default "prod-env")
		wantContext string
		wantEnv     string
		wantToken   string
		wantErr     bool
	}{
		{
			name:        "no flags uses current context",
			wantContext: "prod-env",
			wantEnv:     prodURL,
			wantToken:   prodTok,
		},
		{
			// Regression: before the fix, this would return prodURL for environment.
			name:        "explicit context uses named context URL not current context URL",
			contextName: "sprint-env",
			wantContext: "sprint-env",
			wantEnv:     hardURL,
			wantToken:   hardTok,
		},
		{
			name:        "explicit context and explicit environment uses provided values",
			contextName: "sprint-env",
			environment: "https://custom.example.invalid/",
			wantContext: "sprint-env",
			wantEnv:     "https://custom.example.invalid/",
			wantToken:   hardTok, // token still resolved from named context
		},
		{
			name:        "all flags explicit skips lookup entirely",
			contextName: "sprint-env",
			environment: "https://custom.example.invalid/",
			tokenName:   "my-explicit-token",
			wantContext: "sprint-env",
			wantEnv:     "https://custom.example.invalid/",
			wantToken:   "my-explicit-token",
		},
		{
			name:        "explicit context not in config returns empty environment",
			contextName: "new-context",
			wantContext: "new-context",
			wantEnv:     "", // caller must check and error
			wantToken:   "",
		},
		{
			name:       "no flags no current context returns error",
			currentCtx: "none", // sentinel: clear CurrentContext
			wantErr:    true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := makeCfg()
			if tt.currentCtx == "none" {
				cfg.CurrentContext = ""
			}

			gotCtx, gotEnv, gotToken, err := resolveLoginContext(cfg, tt.contextName, tt.environment, tt.tokenName)

			if tt.wantErr {
				if err == nil {
					t.Fatal("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if gotCtx != tt.wantContext {
				t.Errorf("context = %q, want %q", gotCtx, tt.wantContext)
			}
			if gotEnv != tt.wantEnv {
				t.Errorf("environment = %q, want %q", gotEnv, tt.wantEnv)
			}
			if gotToken != tt.wantToken {
				t.Errorf("tokenName = %q, want %q", gotToken, tt.wantToken)
			}
		})
	}
}

// TestAuthLogin_ContextOnly_UsesNamedContextURL is the integration-level regression
// test for the bug where dtctl auth login --context <non-active-context> would
// silently overwrite the target context's environment URL with the active context's URL.
//
// Config: prod-env (active, prod URL) + sprint-env (sprint URL)
// Command: auth login --context sprint-env   (no --environment)
// Expected: sprint-env's sprint URL is used, command fails at keyring gate (not URL resolution).
func TestAuthLogin_ContextOnly_UsesNamedContextURL(t *testing.T) {
	viper.Reset()
	resetAuthLoginFlags(t)
	t.Setenv("DTCTL_DISABLE_KEYRING", "1")

	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config.yaml")

	cfg := config.NewConfig()
	cfg.SetContext("prod-env", "https://abc12345.apps.dynatrace.com/", "prod-env-oauth")
	cfg.SetContext("sprint-env", "https://xyz67890.sprint.apps.dynatracelabs.com/", "sprint-env-oauth")
	cfg.CurrentContext = "prod-env"

	if err := cfg.SaveTo(configPath); err != nil {
		t.Fatalf("failed to save config: %v", err)
	}
	gFlags.cfgFile = configPath
	defer func() { gFlags.cfgFile = "" }()

	rootCmd.SetArgs([]string{"auth", "login", "--context", "sprint-env"})
	err := rootCmd.Execute()

	// The command must NOT fail with a context/environment validation error —
	// that would indicate sprint-env's URL was not resolved from config.
	if err != nil && strings.Contains(err.Error(), "--context and --environment are required") {
		t.Errorf("expected sprint-env's URL to be resolved from config, got: %v", err)
	}
	if err != nil && strings.Contains(err.Error(), "not found in config") {
		t.Errorf("expected sprint-env to be found in config, got: %v", err)
	}
	// The command should fail at the keyring gate (disabled in tests), not earlier.
	if err != nil && !strings.Contains(err.Error(), "keyring") {
		t.Errorf("expected keyring error after successful URL resolution, got: %v", err)
	}
}

// TestAuthLogin_NewContext_RequiresEnvironment verifies that when --context names
// a context that does not exist in config yet, --environment must be supplied.
func TestAuthLogin_NewContext_RequiresEnvironment(t *testing.T) {
	viper.Reset()
	resetAuthLoginFlags(t)
	t.Setenv("DTCTL_DISABLE_KEYRING", "1")

	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config.yaml")

	cfg := config.NewConfig()
	cfg.SetContext("existing", "https://abc12345.apps.dynatrace.com/", "existing-oauth")
	cfg.CurrentContext = "existing"

	if err := cfg.SaveTo(configPath); err != nil {
		t.Fatalf("failed to save config: %v", err)
	}
	gFlags.cfgFile = configPath
	defer func() { gFlags.cfgFile = "" }()

	rootCmd.SetArgs([]string{"auth", "login", "--context", "brand-new-context"})
	err := rootCmd.Execute()

	if err == nil {
		t.Fatal("expected error for new context without --environment, got nil")
	}
	if !strings.Contains(err.Error(), "--environment is required") {
		t.Errorf("expected --environment required error, got: %v", err)
	}
}

// TestAuthLogin_KeyringRecoveryFailure verifies that when
// EnsureKeyringCollection fails, the command returns an actionable
// diagnostic error with suggestions including file-based storage.
func TestAuthLogin_KeyringRecoveryFailure(t *testing.T) {
	viper.Reset()
	resetAuthLoginFlags(t)

	const (
		ctxName = "fail-ctx"
		envURL  = "https://abc12345.apps.dynatrace.com"
	)

	configPath := setupAuthTestConfig(t, ctxName, envURL, ctxName+"-oauth")
	gFlags.cfgFile = configPath
	defer func() { gFlags.cfgFile = "" }()

	origCheck := authCheckKeyringFunc
	origEnsure := authEnsureKeyringFunc
	defer func() {
		authCheckKeyringFunc = origCheck
		authEnsureKeyringFunc = origEnsure
	}()

	// CheckKeyring always fails with the unlock error.
	authCheckKeyringFunc = func() error {
		return fmt.Errorf("keyring probe failed: %s", config.ErrMsgCollectionUnlock)
	}
	// EnsureKeyringCollection fails too.
	authEnsureKeyringFunc = func(_ context.Context) error {
		return fmt.Errorf("D-Bus connection refused")
	}

	rootCmd.SetArgs([]string{"auth", "login", "--context", ctxName, "--environment", envURL})
	err := rootCmd.Execute()

	if err == nil {
		t.Fatal("expected error when keyring recovery fails")
	}
	if !strings.Contains(err.Error(), "keyring") {
		t.Errorf("expected keyring-related error, got: %v", err)
	}
	// Should suggest file-based storage as the primary alternative
	if !strings.Contains(err.Error(), config.EnvTokenStorage) {
		t.Errorf("expected suggestion about %s, got: %v", config.EnvTokenStorage, err)
	}
}

// TestAuthLogin_FileStorage_PassesKeyringGate verifies that when the keyring
// is unavailable but DTCTL_TOKEN_STORAGE=file is set, auth login proceeds
// past the keyring gate (it will fail later at the actual OAuth flow, but
// the keyring gate itself should not block).
func TestAuthLogin_FileStorage_PassesKeyringGate(t *testing.T) {
	viper.Reset()
	resetAuthLoginFlags(t)
	t.Setenv("DTCTL_DISABLE_KEYRING", "1")
	t.Setenv(config.EnvTokenStorage, "file")

	const (
		ctxName = "file-ctx"
		envURL  = "https://abc12345.apps.dynatrace.com"
	)

	configPath := setupAuthTestConfig(t, ctxName, envURL, ctxName+"-oauth")
	gFlags.cfgFile = configPath
	defer func() { gFlags.cfgFile = "" }()

	// Override keyring check to always fail (deterministic across OSes)
	origCheck := authCheckKeyringFunc
	defer func() { authCheckKeyringFunc = origCheck }()
	authCheckKeyringFunc = func() error {
		return fmt.Errorf("keyring disabled via %s environment variable", config.EnvDisableKeyring)
	}

	// Use a very short timeout so the OAuth flow fails quickly instead of hanging
	rootCmd.SetArgs([]string{"auth", "login", "--context", ctxName, "--environment", envURL, "--timeout", "1s"})
	err := rootCmd.Execute()

	// The command should NOT fail at the keyring gate.
	// It will fail further along (OAuth timeout or no browser),
	// but the error should NOT be about requiring a keyring.
	if err != nil {
		errMsg := err.Error()
		if strings.Contains(errMsg, "requires a token storage backend") {
			t.Errorf("expected file storage to pass keyring gate, got blocking error: %v", err)
		}
		if strings.Contains(errMsg, "requires a working system keyring") {
			t.Errorf("expected file storage to pass keyring gate, got old-style keyring error: %v", err)
		}
	}
}

// TestAuthLogin_KeyringRecovery_WithFileStorage verifies that the keyring
// gate in auth login produces a warning (not an error) when keyring is
// unavailable but DTCTL_DISABLE_KEYRING is set together with DTCTL_TOKEN_STORAGE=file.
func TestAuthLogin_KeyringRecovery_WithFileStorage(t *testing.T) {
	viper.Reset()
	resetAuthLoginFlags(t)
	t.Setenv("DTCTL_DISABLE_KEYRING", "1")
	t.Setenv(config.EnvTokenStorage, "file")

	const (
		ctxName = "file-recovery-ctx"
		envURL  = "https://abc12345.apps.dynatrace.com"
	)

	configPath := setupAuthTestConfig(t, ctxName, envURL, ctxName+"-oauth")
	gFlags.cfgFile = configPath
	defer func() { gFlags.cfgFile = "" }()

	// Override keyring check to always fail (simulates headless Linux)
	origCheck := authCheckKeyringFunc
	defer func() { authCheckKeyringFunc = origCheck }()
	authCheckKeyringFunc = func() error {
		return fmt.Errorf("keyring disabled via %s environment variable", config.EnvDisableKeyring)
	}

	// Use a very short timeout so the OAuth flow fails quickly instead of hanging
	rootCmd.SetArgs([]string{"auth", "login", "--context", ctxName, "--environment", envURL, "--timeout", "1s"})
	err := rootCmd.Execute()

	// Should NOT fail at the keyring gate — file storage should let it through.
	if err != nil && strings.Contains(err.Error(), "requires a token storage backend") {
		t.Errorf("expected file storage to bypass keyring gate, got: %v", err)
	}
}

// TestAuthLogout_RemoveContext_DoesNotPersistEnvOverride guards the
// session-locality contract of DTCTL_CONTEXT (see LoadConfig): a logout that
// rewrites the config file must persist the file's own current-context, not
// the in-memory env override.
func TestAuthLogout_RemoveContext_DoesNotPersistEnvOverride(t *testing.T) {
	viper.Reset()
	t.Setenv("DTCTL_DISABLE_KEYRING", "1")
	t.Setenv(config.EnvTokenStorage, "file")
	// The logout deletes "doomed-oauth" from the file token store, which must
	// be this test's own directory. Setting the variable alone does not move
	// it: the xdg package keeps the paths it read earlier.
	dataHome := t.TempDir()
	setXDGHome(t, "XDG_DATA_HOME", dataHome)
	if dir := config.DataDir(); !strings.HasPrefix(dir, dataHome) {
		t.Fatalf("the file token store is in %s, outside the test's directory %s", dir, dataHome)
	}

	configPath := filepath.Join(t.TempDir(), "config.yaml")
	cfg := config.NewConfig()
	cfg.SetContext("doomed", "https://doomed.example.invalid", "doomed-oauth")
	cfg.SetContext("other", "https://other.example.invalid", "other-oauth")
	cfg.CurrentContext = "doomed"
	if err := cfg.SaveTo(configPath); err != nil {
		t.Fatalf("save test config: %v", err)
	}

	gFlags.cfgFile = configPath
	defer func() { gFlags.cfgFile = "" }()
	t.Setenv("DTCTL_CONTEXT", "other") // session-local override, must never be written

	rootCmd.SetArgs([]string{"auth", "logout", "doomed", "--remove-context"})
	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("auth logout: %v", err)
	}

	saved, err := config.LoadFrom(configPath)
	if err != nil {
		t.Fatalf("reload config: %v", err)
	}
	if saved.CurrentContext == "other" {
		t.Fatal("DTCTL_CONTEXT env override was persisted to the config file")
	}
	if saved.CurrentContext != "" {
		t.Errorf("current-context = %q, want cleared (the deleted context was current in the file)", saved.CurrentContext)
	}
	if _, err := saved.GetContext("doomed"); err == nil {
		t.Error("context 'doomed' should have been removed")
	}
}

// TestAuthLogin_ClientCredentialsValidation verifies that the non-interactive
// client credentials grant is selected by either flags or environment
// variables, and that supplying only half of the credential pair is reported
// as a usage error rather than silently falling back to the browser flow.
func TestAuthLogin_ClientCredentialsValidation(t *testing.T) {
	const (
		ctxName = "cc-ctx"
		envURL  = "https://abc12345.apps.dynatrace.com"
	)

	tests := []struct {
		name string
		args []string
		env  map[string]string
	}{
		{
			name: "client id flag without secret",
			args: []string{"--client-id", "dt0s02.EXAMPLE"},
		},
		{
			name: "client secret flag without id",
			args: []string{"--client-secret", "dt0s02.EXAMPLE.SECRET"},
		},
		{
			name: "client id from environment without secret",
			env:  map[string]string{envLoginClientID: "dt0s02.EXAMPLE"},
		},
		{
			name: "client secret from environment without id",
			env:  map[string]string{envLoginClientSecret: "dt0s02.EXAMPLE.SECRET"},
		},
		{
			name: "flag id with the environment secret explicitly empty",
			args: []string{"--client-id", "dt0s02.EXAMPLE"},
			env:  map[string]string{envLoginClientSecret: ""},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			viper.Reset()
			resetAuthLoginFlags(t)
			// Clear both variables so the host environment cannot influence the
			// result, then apply only what the case under test needs.
			t.Setenv(envLoginClientID, "")
			t.Setenv(envLoginClientSecret, "")
			for k, v := range tt.env {
				t.Setenv(k, v)
			}

			gFlags.cfgFile = setupAuthTestConfig(t, ctxName, envURL, ctxName+"-oauth")
			defer func() { gFlags.cfgFile = "" }()

			args := append([]string{"auth", "login", "--context", ctxName, "--environment", envURL}, tt.args...)
			rootCmd.SetArgs(args)
			err := rootCmd.Execute()

			if err == nil {
				t.Fatal("expected an error when only one client credential is supplied, got nil")
			}
			const want = "requires both a client ID and a client secret"
			if !strings.Contains(err.Error(), want) {
				t.Errorf("expected error containing %q, got: %v", want, err)
			}
		})
	}
}

// TestAuthLogin_ClientCredentials_SkipsBrowserFlow verifies that supplying a
// complete credential pair takes the non-interactive path: the grant is
// invoked with the supplied parameters and the interactive browser flow,
// which cannot complete without a user, is never started.
func TestAuthLogin_ClientCredentials_SkipsBrowserFlow(t *testing.T) {
	viper.Reset()
	resetAuthLoginFlags(t)
	t.Setenv("DTCTL_DISABLE_KEYRING", "1")
	t.Setenv(config.EnvTokenStorage, "file")
	// The command really does store the minted token, so keep the file store
	// off the developer's own ~/.local/share/dtctl.
	setXDGHome(t, "XDG_DATA_HOME", t.TempDir())
	t.Setenv(envLoginClientID, "dt0s02.EXAMPLE")
	t.Setenv(envLoginClientSecret, "dt0s02.EXAMPLE.SECRET")
	t.Setenv(envLoginAccountURN, "urn:dtaccount:00000000-0000-0000-0000-000000000000")

	const (
		ctxName = "cc-nonint-ctx"
		envURL  = "https://abc12345.apps.dynatrace.com"
	)

	gFlags.cfgFile = setupAuthTestConfig(t, ctxName, envURL, ctxName+"-oauth")
	defer func() { gFlags.cfgFile = "" }()

	origCheck := authCheckKeyringFunc
	defer func() { authCheckKeyringFunc = origCheck }()
	authCheckKeyringFunc = func() error {
		return fmt.Errorf("keyring disabled via %s environment variable", config.EnvDisableKeyring)
	}

	var gotID, gotSecret, gotResource string
	var gotScopes []string
	called := false

	origGrant := authClientCredentialsFunc
	defer func() { authClientCredentialsFunc = origGrant }()
	var gotDeadline time.Time
	authClientCredentialsFunc = func(ctx context.Context, _ *auth.OAuthFlow, clientID, clientSecret, resource string, scopes []string) (*auth.TokenSet, error) {
		called = true
		gotID, gotSecret, gotResource, gotScopes = clientID, clientSecret, resource, scopes
		gotDeadline, _ = ctx.Deadline()
		return &auth.TokenSet{
			AccessToken: "test-access-token",
			TokenType:   "Bearer",
			ExpiresIn:   300,
			ExpiresAt:   time.Now().Add(5 * time.Minute),
		}, nil
	}

	rootCmd.SetArgs([]string{
		"auth", "login", "--context", ctxName, "--environment", envURL,
		"--timeout", "90s", "--scopes", "storage:logs:read,storage:buckets:read",
	})
	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("expected the non-interactive login to succeed, got: %v", err)
	}

	if !called {
		t.Fatal("expected the client credentials grant to be used, but it was never called")
	}
	if gotID != "dt0s02.EXAMPLE" {
		t.Errorf("client ID = %q, want %q", gotID, "dt0s02.EXAMPLE")
	}
	if gotSecret != "dt0s02.EXAMPLE.SECRET" {
		t.Errorf("client secret = %q, want %q", gotSecret, "dt0s02.EXAMPLE.SECRET")
	}
	if gotResource != "urn:dtaccount:00000000-0000-0000-0000-000000000000" {
		t.Errorf("resource = %q, want the account URN from the environment", gotResource)
	}
	if len(gotScopes) != 2 || gotScopes[0] != "storage:logs:read" || gotScopes[1] != "storage:buckets:read" {
		t.Errorf("scopes = %v, want the two scopes passed via --scopes", gotScopes)
	}
	// --timeout must bound the token request too, not just the browser flow.
	if gotDeadline.IsZero() {
		t.Fatal("the grant was called with a context that carries no deadline; --timeout was dropped")
	}
	if remaining := time.Until(gotDeadline); remaining <= 0 || remaining > 90*time.Second {
		t.Errorf("context deadline is %v away, want it derived from --timeout 90s", remaining)
	}
}

// TestAuthLogin_ClientCredentials_ReportsTokenAuthority covers the reporting
// around the safety level. The browser flow requests the safety level's scope
// set at the IdP, so there the stored safety level and the token's authority
// move together; this grant requests only --scopes, so without them the token
// keeps the OAuth client's full granted set while the context still records
// the chosen level. The command has to say so.
func TestAuthLogin_ClientCredentials_ReportsTokenAuthority(t *testing.T) {
	const envURL = "https://abc12345.apps.dynatrace.com"

	tests := []struct {
		name        string
		args        []string
		grantedFrom string
		wantStderr  []string
		notStderr   []string
	}{
		{
			name:        "no scopes with a narrowing safety level warns",
			args:        []string{"--safety-level", "readonly"},
			grantedFrom: "storage:logs:read storage:buckets:read",
			wantStderr: []string{
				"the token carries every scope the OAuth client was granted",
				"The safety level readonly gates dtctl itself; it does not narrow the token",
				// The endpoint may return fewer scopes than requested, so the
				// token's real scopes are what gets reported.
				"Granted scopes: storage:logs:read storage:buckets:read",
			},
		},
		{
			name:        "no scopes with dangerously-unrestricted does not warn about narrowing",
			args:        []string{"--safety-level", "dangerously-unrestricted"},
			grantedFrom: "storage:logs:read",
			wantStderr:  []string{"the token carries every scope the OAuth client was granted"},
			notStderr:   []string{"does not narrow the token"},
		},
		{
			name:        "explicit scopes suppress the warning",
			args:        []string{"--safety-level", "readonly", "--scopes", "storage:logs:read"},
			grantedFrom: "storage:logs:read",
			notStderr: []string{
				"the token carries every scope the OAuth client was granted",
				"does not narrow the token",
			},
		},
	}

	for i, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			viper.Reset()
			resetAuthLoginFlags(t)
			t.Setenv("DTCTL_DISABLE_KEYRING", "1")
			t.Setenv(config.EnvTokenStorage, "file")
			setXDGHome(t, "XDG_DATA_HOME", t.TempDir())
			t.Setenv(envLoginClientID, "dt0s02.EXAMPLE")
			t.Setenv(envLoginClientSecret, "dt0s02.EXAMPLE.SECRET")
			t.Setenv(envLoginAccountURN, "")

			ctxName := fmt.Sprintf("cc-report-ctx-%d", i)
			gFlags.cfgFile = setupAuthTestConfig(t, ctxName, envURL, ctxName+"-oauth")
			defer func() { gFlags.cfgFile = "" }()

			origCheck := authCheckKeyringFunc
			defer func() { authCheckKeyringFunc = origCheck }()
			authCheckKeyringFunc = func() error {
				return fmt.Errorf("keyring disabled via %s environment variable", config.EnvDisableKeyring)
			}

			origGrant := authClientCredentialsFunc
			defer func() { authClientCredentialsFunc = origGrant }()
			authClientCredentialsFunc = func(context.Context, *auth.OAuthFlow, string, string, string, []string) (*auth.TokenSet, error) {
				return &auth.TokenSet{
					AccessToken: "test-access-token",
					TokenType:   "Bearer",
					ExpiresIn:   300,
					Scope:       tt.grantedFrom,
					ExpiresAt:   time.Now().Add(5 * time.Minute),
				}, nil
			}

			args := append([]string{"auth", "login", "--context", ctxName, "--environment", envURL}, tt.args...)
			stderr := captureAuthStderr(t, func() {
				rootCmd.SetArgs(args)
				if err := rootCmd.Execute(); err != nil {
					t.Errorf("auth login: %v", err)
				}
			})

			for _, want := range tt.wantStderr {
				if !strings.Contains(stderr, want) {
					t.Errorf("stderr missing %q, got:\n%s", want, stderr)
				}
			}
			for _, unwanted := range tt.notStderr {
				if strings.Contains(stderr, unwanted) {
					t.Errorf("stderr unexpectedly contains %q, got:\n%s", unwanted, stderr)
				}
			}
		})
	}
}

// TestAuthRefresh_NoRefreshToken_PointsAtLogin covers the dead end a client
// credentials token creates: it carries no refresh token by design, so
// `auth refresh` can never succeed for it and has to say what will.
func TestAuthRefresh_NoRefreshToken_PointsAtLogin(t *testing.T) {
	viper.Reset()
	t.Setenv("DTCTL_DISABLE_KEYRING", "1")
	t.Setenv(config.EnvTokenStorage, "file")
	dataHome := t.TempDir()
	setXDGHome(t, "XDG_DATA_HOME", dataHome)

	const (
		ctxName   = "cc-refresh-ctx"
		envURL    = "https://abc12345.apps.dynatrace.com"
		tokenName = ctxName + "-oauth"
	)

	gFlags.cfgFile = setupAuthTestConfig(t, ctxName, envURL, tokenName)
	defer func() { gFlags.cfgFile = "" }()

	// Store a token set shaped like one from the client credentials grant:
	// an access token and no refresh token.
	tokenManager, err := auth.NewTokenManager(auth.OAuthConfigFromEnvironmentURL(envURL))
	if err != nil {
		t.Fatalf("new token manager: %v", err)
	}
	if err := tokenManager.SaveToken(tokenName, &auth.TokenSet{
		AccessToken: "test-access-token",
		TokenType:   "Bearer",
		ExpiresIn:   300,
		ExpiresAt:   time.Now().Add(5 * time.Minute),
	}); err != nil {
		t.Fatalf("save token: %v", err)
	}

	rootCmd.SetArgs([]string{"auth", "refresh", ctxName})
	err = rootCmd.Execute()
	if err == nil {
		t.Fatal("expected auth refresh to fail for a token with no refresh token")
	}

	var diagErr *diagnostic.Error
	if !errors.As(err, &diagErr) {
		t.Fatalf("expected a diagnostic.Error carrying suggestions, got %T: %v", err, err)
	}
	if !errors.Is(err, auth.ErrNoRefreshToken) {
		t.Errorf("expected the error to wrap auth.ErrNoRefreshToken, got: %v", err)
	}
	joined := strings.Join(diagErr.Suggestions, "\n")
	if !strings.Contains(joined, "dtctl auth login --context "+ctxName) {
		t.Errorf("expected a suggestion to re-run auth login for %q, got:\n%s", ctxName, joined)
	}
}

// captureAuthStderr runs fn while capturing everything written to os.Stderr,
// which is where output.Print* sends progress and warnings.
func captureAuthStderr(t *testing.T, fn func()) string {
	t.Helper()
	orig := os.Stderr
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	os.Stderr = w
	done := make(chan string, 1)
	go func() {
		var buf bytes.Buffer
		_, _ = io.Copy(&buf, r)
		done <- buf.String()
	}()
	fn()
	_ = w.Close()
	os.Stderr = orig
	return <-done
}

// TestAuthLogin_ClientCredentials_PartialInputDoesNotFallBackToBrowser covers
// the parameters that only make sense for the client credentials grant. Left
// out of the trigger they would reach the browser flow, which on a CI runner
// is an unexplained hang until --timeout expires rather than an error.
func TestAuthLogin_ClientCredentials_PartialInputDoesNotFallBackToBrowser(t *testing.T) {
	const (
		ctxName = "cc-partial-ctx"
		envURL  = "https://abc12345.apps.dynatrace.com"
	)

	tests := []struct {
		name     string
		args     []string
		env      map[string]string
		wantHint string
	}{
		{
			name:     "account urn from environment with no credentials",
			env:      map[string]string{envLoginAccountURN: "urn:dtaccount:00000000-0000-0000-0000-000000000000"},
			wantHint: "neither was supplied",
		},
		{
			name:     "account urn flag with no credentials",
			args:     []string{"--account-urn", "urn:dtaccount:00000000-0000-0000-0000-000000000000"},
			wantHint: "neither was supplied",
		},
		{
			name:     "scopes with no credentials",
			args:     []string{"--scopes", "storage:logs:read"},
			wantHint: "neither was supplied",
		},
		{
			name:     "account urn with only a client id",
			args:     []string{"--account-urn", "urn:dtaccount:00000000-0000-0000-0000-000000000000", "--client-id", "dt0s02.EXAMPLE"},
			wantHint: "the client secret is missing",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			viper.Reset()
			resetAuthLoginFlags(t)
			for _, name := range []string{envLoginClientID, envLoginClientSecret, envLoginAccountURN} {
				t.Setenv(name, "")
			}
			for k, v := range tt.env {
				t.Setenv(k, v)
			}

			// If the trigger regressed, the browser flow would start here; a
			// tiny timeout keeps the failure fast instead of hanging the suite.
			origGrant := authClientCredentialsFunc
			defer func() { authClientCredentialsFunc = origGrant }()
			authClientCredentialsFunc = func(context.Context, *auth.OAuthFlow, string, string, string, []string) (*auth.TokenSet, error) {
				t.Fatal("the grant must not run without a complete credential pair")
				return nil, nil
			}

			gFlags.cfgFile = setupAuthTestConfig(t, ctxName, envURL, ctxName+"-oauth")
			defer func() { gFlags.cfgFile = "" }()

			args := append([]string{"auth", "login", "--context", ctxName, "--environment", envURL, "--timeout", "1ns"}, tt.args...)
			rootCmd.SetArgs(args)
			err := rootCmd.Execute()

			if err == nil {
				t.Fatal("expected a usage error, got nil")
			}
			if !strings.Contains(err.Error(), "requires both a client ID and a client secret") {
				t.Errorf("expected the incomplete-pair error, got: %v", err)
			}
			if !strings.Contains(err.Error(), tt.wantHint) {
				t.Errorf("expected the error to name the missing half (%q), got: %v", tt.wantHint, err)
			}
		})
	}
}

// TestAuthLogin_KeepsTheContextsSafetyLevel: logging in again is how an
// expired session is renewed, so it must not change what the context may do.
// The flag's default applies to a context that does not exist yet; an
// existing one keeps its level unless --safety-level is given.
func TestAuthLogin_KeepsTheContextsSafetyLevel(t *testing.T) {
	const envURL = "https://abc12345.apps.dynatrace.com"

	tests := []struct {
		name     string
		existing config.SafetyLevel // "" = the context does not exist yet
		args     []string
		want     config.SafetyLevel
	}{
		{"existing readonly context, no flag", config.SafetyLevelReadOnly, nil, config.SafetyLevelReadOnly},
		{"existing readonly context, flag raises it", config.SafetyLevelReadOnly, []string{"--safety-level", "readwrite-mine"}, config.SafetyLevelReadWriteMine},
		{"existing context, flag set to the default", config.SafetyLevelReadOnly, []string{"--safety-level", string(config.DefaultSafetyLevel)}, config.DefaultSafetyLevel},
		{"new context, no flag", "", nil, config.DefaultSafetyLevel},
		{"new context, flag", "", []string{"--safety-level", "readonly"}, config.SafetyLevelReadOnly},
	}

	for i, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			viper.Reset()
			resetAuthLoginFlags(t)
			t.Setenv("DTCTL_DISABLE_KEYRING", "1")
			t.Setenv(config.EnvTokenStorage, "file")
			setXDGHome(t, "XDG_DATA_HOME", t.TempDir())
			t.Setenv(envLoginClientID, "dt0s02.EXAMPLE")
			t.Setenv(envLoginClientSecret, "dt0s02.EXAMPLE.SECRET")
			t.Setenv(envLoginAccountURN, "")

			ctxName := fmt.Sprintf("keep-level-ctx-%d", i)
			configPath := setupAuthTestConfig(t, "other", envURL, "other-oauth")
			if tt.existing != "" {
				cfg, err := config.LoadFrom(configPath)
				if err != nil {
					t.Fatalf("load config: %v", err)
				}
				cfg.SetContextWithOptions(ctxName, envURL, ctxName+"-oauth", &config.ContextOptions{SafetyLevel: tt.existing})
				if err := cfg.SaveTo(configPath); err != nil {
					t.Fatalf("save config: %v", err)
				}
			}
			gFlags.cfgFile = configPath
			defer func() { gFlags.cfgFile = "" }()

			origCheck := authCheckKeyringFunc
			defer func() { authCheckKeyringFunc = origCheck }()
			authCheckKeyringFunc = func() error {
				return fmt.Errorf("keyring disabled via %s environment variable", config.EnvDisableKeyring)
			}

			origGrant := authClientCredentialsFunc
			defer func() { authClientCredentialsFunc = origGrant }()
			authClientCredentialsFunc = func(context.Context, *auth.OAuthFlow, string, string, string, []string) (*auth.TokenSet, error) {
				return &auth.TokenSet{
					AccessToken: "test-access-token",
					TokenType:   "Bearer",
					ExpiresIn:   300,
					ExpiresAt:   time.Now().Add(5 * time.Minute),
				}, nil
			}

			args := append([]string{"auth", "login", "--context", ctxName, "--environment", envURL}, tt.args...)
			captureAuthStderr(t, func() {
				rootCmd.SetArgs(args)
				if err := rootCmd.Execute(); err != nil {
					t.Errorf("auth login: %v", err)
				}
			})

			saved, err := config.LoadFrom(configPath)
			if err != nil {
				t.Fatalf("load saved config: %v", err)
			}
			nc, err := saved.GetContext(ctxName)
			if err != nil {
				t.Fatalf("context not saved: %v", err)
			}
			if nc.Context.SafetyLevel != tt.want {
				t.Errorf("safety level = %q, want %q", nc.Context.SafetyLevel, tt.want)
			}
		})
	}
}

// TestLoginSafetyLevels covers the two levels a login works with: the one it
// saves to the context and the one whose scopes it requests.
func TestLoginSafetyLevels(t *testing.T) {
	const envURL = "https://abc12345.apps.dynatrace.com"
	withContext := func(level config.SafetyLevel) *config.Config {
		cfg := config.NewConfig()
		cfg.SetContextWithOptions("ctx", envURL, "ctx-oauth", &config.ContextOptions{SafetyLevel: level})
		return cfg
	}

	tests := []struct {
		name          string
		cfg           *config.Config
		flagLevel     config.SafetyLevel
		flagGiven     bool
		wantSaved     config.SafetyLevel
		wantRequested config.SafetyLevel
	}{
		{"new context takes the default", config.NewConfig(), config.DefaultSafetyLevel, false, config.DefaultSafetyLevel, config.DefaultSafetyLevel},
		{"new context takes the flag", config.NewConfig(), config.SafetyLevelReadOnly, true, config.SafetyLevelReadOnly, config.SafetyLevelReadOnly},
		{"readonly is kept", withContext(config.SafetyLevelReadOnly), config.DefaultSafetyLevel, false, config.SafetyLevelReadOnly, config.SafetyLevelReadOnly},
		{"readwrite-mine is kept", withContext(config.SafetyLevelReadWriteMine), config.DefaultSafetyLevel, false, config.SafetyLevelReadWriteMine, config.SafetyLevelReadWriteMine},
		{"dangerously-unrestricted is kept", withContext(config.SafetyLevelDangerouslyUnrestricted), config.DefaultSafetyLevel, false, config.SafetyLevelDangerouslyUnrestricted, config.SafetyLevelDangerouslyUnrestricted},
		{"the flag replaces a stored level", withContext(config.SafetyLevelReadOnly), config.SafetyLevelReadWriteMine, true, config.SafetyLevelReadWriteMine, config.SafetyLevelReadWriteMine},
		{"the flag set to the default still counts", withContext(config.SafetyLevelReadOnly), config.DefaultSafetyLevel, true, config.DefaultSafetyLevel, config.DefaultSafetyLevel},
		{"a context with no level is left without one", withContext(""), config.DefaultSafetyLevel, false, "", config.DefaultSafetyLevel},
		{"the flag gives a context with no level one", withContext(""), config.SafetyLevelReadOnly, true, config.SafetyLevelReadOnly, config.SafetyLevelReadOnly},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := loginSafetyLevels(tt.cfg, nil, "ctx", "ctx-oauth", tt.flagLevel, tt.flagGiven)
			if err != nil {
				t.Fatalf("loginSafetyLevels: %v", err)
			}
			if got.saved != tt.wantSaved || got.requested != tt.wantRequested {
				t.Errorf("saved %q, requested %q; want saved %q, requested %q", got.saved, got.requested, tt.wantSaved, tt.wantRequested)
			}
			// Outside a project config nothing clamps the declared level.
			if got.declared != tt.wantRequested {
				t.Errorf("declared %q, want %q", got.declared, tt.wantRequested)
			}
		})
	}

	t.Run("an invalid stored level is refused", func(t *testing.T) {
		cfg := withContext("read-only")
		_, err := loginSafetyLevels(cfg, nil, "ctx", "ctx-oauth", config.DefaultSafetyLevel, false)
		var diagErr *diagnostic.Error
		if !errors.As(err, &diagErr) {
			t.Fatalf("err = %T %v, want a diagnostic.Error", err, err)
		}
		if diagErr.Operation != "auth login" {
			t.Errorf("operation = %q, want %q", diagErr.Operation, "auth login")
		}
		if want := `context "ctx" stores the safety level "read-only", which is not a valid value (valid values: readonly, readwrite-mine, readwrite-all, dangerously-unrestricted)`; diagErr.Message != want {
			t.Errorf("message = %q\nwant      %q", diagErr.Message, want)
		}
		wantSuggestions := []string{
			"Give the context a valid level, then log in again: dtctl ctx set ctx --safety-level <level>",
			"Or set it with the login: dtctl auth login --context ctx --safety-level <level>",
		}
		if !slices.Equal(diagErr.Suggestions, wantSuggestions) {
			t.Errorf("suggestions = %q, want %q", diagErr.Suggestions, wantSuggestions)
		}
		// The flag is the way out, so it must still work.
		got, err := loginSafetyLevels(cfg, nil, "ctx", "ctx-oauth", config.SafetyLevelReadOnly, true)
		if err != nil || got.saved != config.SafetyLevelReadOnly || got.requested != config.SafetyLevelReadOnly {
			t.Errorf("with the flag: saved %q, requested %q, err %v", got.saved, got.requested, err)
		}
	})
}

// TestLoginSafetyLevels_RefusalHasOneAgentCode: the agent-mode code of the
// refusal must not depend on what the context is called. As a plain error it
// was classified by its text, and the text quotes the context's name.
func TestLoginSafetyLevels_RefusalHasOneAgentCode(t *testing.T) {
	for _, name := range []string{"viewer", "timeout-env", "my-config", "no context here"} {
		cfg := config.NewConfig()
		cfg.SetContextWithOptions(name, "https://abc12345.apps.dynatrace.com", "t-oauth", &config.ContextOptions{SafetyLevel: "read-only"})
		_, err := loginSafetyLevels(cfg, nil, name, "t-oauth", config.DefaultSafetyLevel, false)
		if err == nil {
			t.Fatalf("context %q: no error", name)
		}
		detail := errorToDetail(context.Background(), err)
		if detail.Code != "error" {
			t.Errorf("context %q: code %q, want %q", name, detail.Code, "error")
		}
		if len(detail.Suggestions) != 2 {
			t.Errorf("context %q: %d suggestions, want 2: %q", name, len(detail.Suggestions), detail.Suggestions)
		}
	}
}

// TestLoginSafetyLevels_ProjectConfigCannotRaiseTheScopes: a .dtctl.yaml found
// in the working directory declares its own level, and dtctl enforces no more
// than the global config binds to the token. The login keeps what the file
// says and requests scopes for what is enforced.
func TestLoginSafetyLevels_ProjectConfigCannotRaiseTheScopes(t *testing.T) {
	// Not parallel: the working directory and XDG_CONFIG_HOME are process-wide.
	const envURL = "https://abc12345.apps.dynatrace.com"
	xdgDir := t.TempDir()
	globalDir := filepath.Join(xdgDir, "dtctl")
	if err := os.MkdirAll(globalDir, 0o700); err != nil {
		t.Fatal(err)
	}
	global := "apiVersion: v1\nkind: Config\ncurrent-context: prod\ncontexts:\n  - name: prod\n    context:\n      environment: " + envURL + "\n      token-ref: prod-oauth\n      safety-level: readonly\n"
	if err := os.WriteFile(filepath.Join(globalDir, "config"), []byte(global), 0o600); err != nil {
		t.Fatal(err)
	}
	setXDGHome(t, "XDG_CONFIG_HOME", xdgDir)
	// A DTCTL_CONFIG in the developer's shell would short-circuit discovery.
	t.Setenv(config.EnvConfig, "")

	projectDir := t.TempDir()
	local := "apiVersion: v1\nkind: Config\ncurrent-context: project\ncontexts:\n  - name: project\n    context:\n      environment: " + envURL + "\n      token-ref: prod-oauth\n      safety-level: dangerously-unrestricted\n"
	if err := os.WriteFile(filepath.Join(projectDir, ".dtctl.yaml"), []byte(local), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Chdir(projectDir)

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if !cfg.IsLocal() {
		t.Fatal("the project config was not picked up")
	}

	levels, err := loginSafetyLevels(cfg, globalContextsFor(cfg), "project", "prod-oauth", config.DefaultSafetyLevel, false)
	if err != nil {
		t.Fatalf("loginSafetyLevels: %v", err)
	}
	if levels.saved != config.SafetyLevelDangerouslyUnrestricted {
		t.Errorf("saved %q: the login must not rewrite what the file declares", levels.saved)
	}
	if levels.requested != config.SafetyLevelReadOnly {
		t.Errorf("requested %q, want readonly: the global config binds this token to readonly", levels.requested)
	}

	// Through the command: the login is configured with the enforced level,
	// says why it differs from the declared one, and leaves the file's level.
	viper.Reset()
	resetAuthLoginFlags(t)
	t.Setenv("DTCTL_DISABLE_KEYRING", "1")
	t.Setenv(config.EnvTokenStorage, "file")
	setXDGHome(t, "XDG_DATA_HOME", t.TempDir())
	t.Setenv(envLoginClientID, "dt0s02.EXAMPLE")
	t.Setenv(envLoginClientSecret, "dt0s02.EXAMPLE.SECRET")
	t.Setenv(envLoginAccountURN, "")
	origCheck := authCheckKeyringFunc
	t.Cleanup(func() { authCheckKeyringFunc = origCheck })
	authCheckKeyringFunc = func() error {
		return fmt.Errorf("keyring disabled via %s environment variable", config.EnvDisableKeyring)
	}
	origGrant := authClientCredentialsFunc
	t.Cleanup(func() { authClientCredentialsFunc = origGrant })
	authClientCredentialsFunc = func(context.Context, *auth.OAuthFlow, string, string, string, []string) (*auth.TokenSet, error) {
		return &auth.TokenSet{AccessToken: "test-access-token", TokenType: "Bearer", ExpiresIn: 300, ExpiresAt: time.Now().Add(5 * time.Minute)}, nil
	}

	var runErr error
	stderr := captureAuthStderr(t, func() {
		rootCmd.SetArgs([]string{"auth", "login", "--context", "project"})
		runErr = rootCmd.Execute()
	})
	if runErr != nil {
		t.Fatalf("auth login: %v\n%s", runErr, stderr)
	}
	for _, want := range []string{
		"Safety level: readonly",
		// The enforced level is the one that does not narrow the token. No
		// --safety-level was given, so the line must not show one.
		"The safety level readonly gates dtctl itself; it does not narrow the token.",
	} {
		if !strings.Contains(stderr, want) {
			t.Errorf("stderr lacks %q:\n%s", want, stderr)
		}
	}
	// This is the client credentials grant: the token carries the OAuth
	// client's scopes, so nothing may say the login requests a level's.
	if strings.Contains(stderr, "so the login requests") {
		t.Errorf("stderr says which scopes the login requests:\n%s", stderr)
	}
	if strings.Contains(stderr, "--safety-level readonly") {
		t.Errorf("stderr shows a flag that was not given:\n%s", stderr)
	}
	after, err := config.Load()
	if err != nil {
		t.Fatalf("load after login: %v", err)
	}
	nc, err := after.GetContext("project")
	if err != nil {
		t.Fatalf("context missing after login: %v", err)
	}
	if nc.Context.SafetyLevel != config.SafetyLevelDangerouslyUnrestricted {
		t.Errorf("the file's level became %q", nc.Context.SafetyLevel)
	}
}

// TestContextsNarrowedByLogin: a login replaces the stored token, so the
// contexts that share it and whose own login would request a scope this one
// does not are named before it happens.
func TestContextsNarrowedByLogin(t *testing.T) {
	const (
		envURL     = "https://abc12345.apps.dynatrace.com"
		otherURL   = "https://xyz67890.apps.dynatrace.com"
		devTierURL = "https://abc12345.dev.apps.dynatracelabs.com"
	)
	cfg := config.NewConfig()
	add := func(name, url, token string, level config.SafetyLevel) {
		cfg.SetContextWithOptions(name, url, token, &config.ContextOptions{SafetyLevel: level})
	}
	add("ro", envURL, "shared", config.SafetyLevelReadOnly)
	add("writer", envURL, "shared", config.SafetyLevelReadWriteAll)
	add("admin", envURL, "shared", config.SafetyLevelDangerouslyUnrestricted)
	add("mine", envURL, "shared", config.SafetyLevelReadWriteMine)
	add("unset", envURL, "shared", "")
	// Not a level: dtctl enforces the default for it, so it counts as one.
	add("typo", envURL, "shared", "read-only")
	// Another environment of the same tier shares the stored token. It is
	// named for that, whatever its level, and not among those that lose scopes.
	add("neighbour", otherURL, "shared", config.SafetyLevelReadWriteAll)
	// Tokens are stored per tier, so this one is not replaced.
	add("dev", devTierURL, "shared", config.SafetyLevelDangerouslyUnrestricted)
	add("elsewhere", envURL, "other", config.SafetyLevelDangerouslyUnrestricted)

	tests := []struct {
		context   string
		requested config.SafetyLevel
		want      []string
	}{
		{"ro", config.SafetyLevelReadOnly, []string{`"admin"`, `"mine"`, `"typo"`, `"unset"`, `"writer"`}},
		// readonly requests six read scopes that readwrite-mine does not.
		{"mine", config.SafetyLevelReadWriteMine, []string{`"admin"`, `"ro"`, `"typo"`, `"unset"`, `"writer"`}},
		{"writer", config.SafetyLevelReadWriteAll, []string{`"admin"`}},
		{"admin", config.SafetyLevelDangerouslyUnrestricted, nil},
		// The context that logs in is never its own peer, also when the login
		// requests less than its stored level.
		{"admin", config.SafetyLevelReadOnly, []string{`"mine"`, `"typo"`, `"unset"`, `"writer"`}},
	}
	for _, tt := range tests {
		peers := contextsSharingToken(cfg, nil, tt.context, "shared", envURL)
		got := peerLabels(contextsNarrowedByLogin(peers, auth.GetScopesForSafetyLevel(tt.requested)))
		if !slices.Equal(got, tt.want) {
			t.Errorf("login to %s at %s: got %v, want %v", tt.context, tt.requested, got, tt.want)
		}
		if got, want := peerLabels(contextsOnOtherEnvironments(peers)), []string{`"neighbour"`}; !slices.Equal(got, want) {
			t.Errorf("login to %s: on other environments %v, want %v", tt.context, got, want)
		}
	}
	// Seen from the neighbour's environment, every other context is elsewhere.
	fromNeighbour := contextsSharingToken(cfg, nil, "neighbour", "shared", otherURL)
	if got, want := peerLabels(contextsOnOtherEnvironments(fromNeighbour)), []string{`"admin"`, `"mine"`, `"ro"`, `"typo"`, `"unset"`, `"writer"`}; !slices.Equal(got, want) {
		t.Errorf("login to neighbour: on other environments %v, want %v", got, want)
	}
	if got := contextsNarrowedByLogin(fromNeighbour, auth.GetScopesForSafetyLevel(config.SafetyLevelReadOnly)); len(got) != 0 {
		t.Errorf("login to neighbour: narrowed %v, want none on its own environment", got)
	}
	// The host decides, not the spelling of the URL.
	spelled := contextsSharingToken(cfg, nil, "ro", "shared", "https://ABC12345.apps.dynatrace.com/")
	if got, want := peerLabels(contextsOnOtherEnvironments(spelled)), []string{`"neighbour"`}; !slices.Equal(got, want) {
		t.Errorf("login with another spelling of the URL: on other environments %v, want %v", got, want)
	}
	if got := contextsSharingToken(cfg, nil, "ro", "ro-oauth", envURL); got != nil {
		t.Errorf("a token nobody else uses: got %v", got)
	}
	if got := contextsSharingToken(cfg, nil, "fresh", "shared", devTierURL); len(got) != 1 || got[0].label != `"dev"` {
		t.Errorf("a login in the dev tier: got %v, want only \"dev\"", got)
	}
}

// TestWarnSharedTokenHints pins what the hint offers for each mix of contexts
// in a project directory, where the two ways out differ from elsewhere.
func TestWarnSharedTokenHints(t *testing.T) {
	const (
		envURL    = "https://abc12345.apps.dynatrace.com"
		viaConfig = "A context of the global config is logged in to from outside this directory, or with --config "
		boundOnly = "A token name of its own (--token-name) works here only once a context in the global config binds that name to this environment."
	)
	project := tokenPeer{label: `"b"`}
	global := tokenPeer{label: `"prod" (global config)`, global: true}

	tests := []struct {
		name     string
		narrowed []tokenPeer
		path     string
		want     string
		unwanted string
	}{
		{"a project context only", []tokenPeer{project}, "/home/u/.config/dtctl/config",
			"Log in to it afterwards to restore its scopes. " + boundOnly, viaConfig},
		{"a global context", []tokenPeer{global}, "/home/u/.config/dtctl/config",
			"Log in to it afterwards to restore its scopes. " + viaConfig + "/home/u/.config/dtctl/config. " + boundOnly, ""},
		{"both, and a path with a space", []tokenPeer{project, global}, "/home/u/Application Support/dtctl/config",
			"Afterwards, log in to the context whose scopes you need (a login restores only those of its own context). " + viaConfig + "'/home/u/Application Support/dtctl/config'. " + boundOnly, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var b strings.Builder
			warnSharedToken(&b, "prod-oauth", envURL, tt.narrowed, nil, true, tt.path)
			if !strings.Contains(b.String(), tt.want) {
				t.Errorf("output lacks %q:\n%s", tt.want, b.String())
			}
			if tt.unwanted != "" && strings.Contains(b.String(), tt.unwanted) {
				t.Errorf("output contains %q:\n%s", tt.unwanted, b.String())
			}
		})
	}

	t.Run("nothing shared, nothing said", func(t *testing.T) {
		var b strings.Builder
		warnSharedToken(&b, "prod-oauth", envURL, nil, nil, true, "/x")
		if b.Len() != 0 {
			t.Errorf("output: %s", b.String())
		}
	})
}

// TestJoinQuotedNames pins how a list of contexts reads in a sentence.
func TestJoinQuotedNames(t *testing.T) {
	tests := []struct {
		names []string
		want  string
	}{
		{[]string{`"a"`}, `"a"`},
		{[]string{`"a"`, `"b"`}, `"a" and "b"`},
		{[]string{`"a"`, `"b"`, `"c"`}, `"a", "b" and "c"`},
	}
	for _, tt := range tests {
		if got := joinNames(tt.names); got != tt.want {
			t.Errorf("joinNames(%v) = %q, want %q", tt.names, got, tt.want)
		}
	}
}

// TestAuthLogin_StoredLevelThroughTheCommand runs the command for the two
// cases a renewed login has to say something about: a token another context
// relies on, and a stored level that is not a level.
func TestAuthLogin_StoredLevelThroughTheCommand(t *testing.T) {
	const envURL = "https://abc12345.apps.dynatrace.com"

	run := func(t *testing.T, stored config.SafetyLevel) (stderr string, granted bool, saved config.SafetyLevel, runErr error) {
		t.Helper()
		viper.Reset()
		resetAuthLoginFlags(t)
		t.Setenv(config.EnvTokenStorage, "file")
		setXDGHome(t, "XDG_DATA_HOME", t.TempDir())
		t.Setenv(envLoginClientID, "dt0s02.EXAMPLE")
		t.Setenv(envLoginClientSecret, "dt0s02.EXAMPLE.SECRET")
		t.Setenv(envLoginAccountURN, "")

		// "writer" and "viewer" share one token; "viewer" stores the level under test.
		configPath := setupAuthTestConfig(t, "writer", envURL, "shared-oauth")
		cfg, err := config.LoadFrom(configPath)
		if err != nil {
			t.Fatalf("load config: %v", err)
		}
		cfg.SetContextWithOptions("writer", envURL, "shared-oauth", &config.ContextOptions{SafetyLevel: config.SafetyLevelReadWriteAll})
		cfg.SetContextWithOptions("viewer", envURL, "shared-oauth", &config.ContextOptions{SafetyLevel: stored})
		if err := cfg.SaveTo(configPath); err != nil {
			t.Fatalf("save config: %v", err)
		}
		gFlags.cfgFile = configPath
		t.Cleanup(func() { gFlags.cfgFile = "" })

		origCheck := authCheckKeyringFunc
		t.Cleanup(func() { authCheckKeyringFunc = origCheck })
		authCheckKeyringFunc = func() error {
			return fmt.Errorf("keyring disabled via %s environment variable", config.EnvDisableKeyring)
		}
		origGrant := authClientCredentialsFunc
		t.Cleanup(func() { authClientCredentialsFunc = origGrant })
		authClientCredentialsFunc = func(context.Context, *auth.OAuthFlow, string, string, string, []string) (*auth.TokenSet, error) {
			granted = true
			return &auth.TokenSet{AccessToken: "test-access-token", TokenType: "Bearer", ExpiresIn: 300, ExpiresAt: time.Now().Add(5 * time.Minute)}, nil
		}

		stderr = captureAuthStderr(t, func() {
			// No --environment: the context supplies it, and its token name
			// with it, which is how a session is renewed.
			rootCmd.SetArgs([]string{"auth", "login", "--context", "viewer"})
			runErr = rootCmd.Execute()
		})
		after, err := config.LoadFrom(configPath)
		if err != nil {
			t.Fatalf("load saved config: %v", err)
		}
		nc, err := after.GetContext("viewer")
		if err != nil {
			t.Fatalf("context missing: %v", err)
		}
		return stderr, granted, nc.Context.SafetyLevel, runErr
	}

	t.Run("a shared token is named before it is replaced", func(t *testing.T) {
		stderr, granted, saved, err := run(t, config.SafetyLevelReadOnly)
		if err != nil || !granted {
			t.Fatalf("login: err %v, granted %v", err, granted)
		}
		if saved != config.SafetyLevelReadOnly {
			t.Errorf("saved level %q, want readonly", saved)
		}
		for _, want := range []string{
			"Safety level: readonly",
			`Token "shared-oauth" is also used by "writer". This login replaces it with the OAuth client's token.`,
		} {
			if !strings.Contains(stderr, want) {
				t.Errorf("stderr lacks %q:\n%s", want, stderr)
			}
		}
		// The client credentials grant does not request the level's scopes,
		// so the login cannot be said to narrow the token to them.
		for _, unwanted := range []string{"readonly token", "requests readonly scopes", "loses", "--token-name"} {
			if strings.Contains(stderr, unwanted) {
				t.Errorf("stderr contains %q:\n%s", unwanted, stderr)
			}
		}
	})

	t.Run("an invalid stored level stops the login before a token is requested", func(t *testing.T) {
		_, granted, saved, err := run(t, "read-only")
		if err == nil || !strings.Contains(err.Error(), `context "viewer" stores the safety level "read-only", which is not a valid value`) {
			t.Errorf("err = %v, want the invalid-level error", err)
		}
		if granted {
			t.Error("a token was requested")
		}
		if saved != "read-only" {
			t.Errorf("the config was rewritten: level %q", saved)
		}
	})
}

// errBrowserLoginStubbed is what the stubbed browser step returns, so a test
// can tell that the command got that far and no further.
var errBrowserLoginStubbed = errors.New("browser login stubbed in test")

// runBrowserLogin runs `auth login` on the browser path with nothing behind
// it: the keyring check passes, the OAuth configuration is captured where the
// command hands it to the flow, and the browser step fails before anything
// could reach the identity provider. It returns what the command printed, the
// captured configuration (nil if the command stopped earlier) and its error.
func runBrowserLogin(t *testing.T, args ...string) (stderr string, oauthConfig *auth.OAuthConfig, runErr error) {
	t.Helper()
	viper.Reset()
	resetAuthLoginFlags(t)
	// Any of these would select the client credentials grant.
	t.Setenv(envLoginClientID, "")
	t.Setenv(envLoginClientSecret, "")
	t.Setenv(envLoginAccountURN, "")

	origCheck := authCheckKeyringFunc
	t.Cleanup(func() { authCheckKeyringFunc = origCheck })
	authCheckKeyringFunc = func() error { return nil }

	origNewFlow := authNewOAuthFlowFunc
	t.Cleanup(func() { authNewOAuthFlowFunc = origNewFlow })
	authNewOAuthFlowFunc = func(c *auth.OAuthConfig) (*auth.OAuthFlow, error) {
		oauthConfig = c
		return origNewFlow(c)
	}

	origBrowser := authBrowserFlowFunc
	t.Cleanup(func() { authBrowserFlowFunc = origBrowser })
	authBrowserFlowFunc = func(context.Context, *auth.OAuthFlow) (*auth.TokenSet, error) {
		return nil, errBrowserLoginStubbed
	}

	stderr = captureAuthStderr(t, func() {
		rootCmd.SetArgs(append([]string{"auth", "login"}, args...))
		runErr = rootCmd.Execute()
	})
	return stderr, oauthConfig, runErr
}

// TestAuthLogin_BrowserFlowRequestsTheKeptLevelsScopes reads the scope list a
// browser login is about to request. Renewing the session of an existing
// context must ask for that context's level, in both directions.
func TestAuthLogin_BrowserFlowRequestsTheKeptLevelsScopes(t *testing.T) {
	const envURL = "https://abc12345.apps.dynatrace.com"

	tests := []struct {
		stored config.SafetyLevel
		// One scope that tells the levels apart, and whether it is requested.
		writeDocuments  bool
		truncateBuckets bool
	}{
		{config.SafetyLevelReadOnly, false, false},
		{config.SafetyLevelReadWriteAll, true, false},
		{config.SafetyLevelDangerouslyUnrestricted, true, true},
	}
	for _, tt := range tests {
		t.Run(string(tt.stored), func(t *testing.T) {
			configPath := setupAuthTestConfig(t, "other", envURL, "other-oauth")
			cfg, err := config.LoadFrom(configPath)
			if err != nil {
				t.Fatalf("load config: %v", err)
			}
			cfg.SetContextWithOptions("kept", envURL, "kept-oauth", &config.ContextOptions{SafetyLevel: tt.stored})
			if err := cfg.SaveTo(configPath); err != nil {
				t.Fatalf("save config: %v", err)
			}
			gFlags.cfgFile = configPath
			t.Cleanup(func() { gFlags.cfgFile = "" })

			stderr, oauthConfig, runErr := runBrowserLogin(t, "--context", "kept")
			if !errors.Is(runErr, errBrowserLoginStubbed) {
				t.Fatalf("auth login: %v, want it to reach the browser step\n%s", runErr, stderr)
			}
			if oauthConfig == nil {
				t.Fatal("the command built no OAuth flow")
			}
			if want := auth.GetScopesForSafetyLevel(tt.stored); !slices.Equal(oauthConfig.Scopes, want) {
				t.Errorf("requested scopes:\n%v\nwant those of %s:\n%v", oauthConfig.Scopes, tt.stored, want)
			}
			if got := slices.Contains(oauthConfig.Scopes, "document:documents:write"); got != tt.writeDocuments {
				t.Errorf("document:documents:write requested = %v, want %v", got, tt.writeDocuments)
			}
			if got := slices.Contains(oauthConfig.Scopes, "storage:bucket-definitions:truncate"); got != tt.truncateBuckets {
				t.Errorf("storage:bucket-definitions:truncate requested = %v, want %v", got, tt.truncateBuckets)
			}
			for _, want := range []string{
				"Safety level: " + string(tt.stored),
				"Requesting OAuth scopes for safety level " + string(tt.stored) + "...",
			} {
				if !strings.Contains(stderr, want) {
					t.Errorf("stderr lacks %q:\n%s", want, stderr)
				}
			}
		})
	}
}

// runClientCredentialsLogin runs `auth login` with the client credentials
// grant stubbed to succeed and the tokens going to a file store in a temp
// directory. It returns what the command printed, its error and whether a
// token was requested.
func runClientCredentialsLogin(t *testing.T, args ...string) (stderr string, runErr error, granted bool) {
	t.Helper()
	stderr, grant, runErr := runClientCredentialsGrant(t, args...)
	return stderr, runErr, grant != nil
}

// runClientCredentialsGrant is runClientCredentialsLogin for a test that reads
// what the command asked the grant for: the scopes it passed, in a pointer
// that is nil when no token was requested.
func runClientCredentialsGrant(t *testing.T, args ...string) (stderr string, grantScopes *[]string, runErr error) {
	t.Helper()
	viper.Reset()
	resetAuthLoginFlags(t)
	t.Setenv(config.EnvTokenStorage, "file")
	setXDGHome(t, "XDG_DATA_HOME", t.TempDir())
	t.Setenv(envLoginClientID, "dt0s02.EXAMPLE")
	t.Setenv(envLoginClientSecret, "dt0s02.EXAMPLE.SECRET")
	t.Setenv(envLoginAccountURN, "")

	origCheck := authCheckKeyringFunc
	t.Cleanup(func() { authCheckKeyringFunc = origCheck })
	authCheckKeyringFunc = func() error {
		return fmt.Errorf("keyring disabled via %s environment variable", config.EnvDisableKeyring)
	}
	origGrant := authClientCredentialsFunc
	t.Cleanup(func() { authClientCredentialsFunc = origGrant })
	authClientCredentialsFunc = func(_ context.Context, _ *auth.OAuthFlow, _, _, _ string, scopes []string) (*auth.TokenSet, error) {
		grantScopes = &scopes
		return &auth.TokenSet{AccessToken: "test-access-token", TokenType: "Bearer", ExpiresIn: 300, ExpiresAt: time.Now().Add(5 * time.Minute)}, nil
	}

	stderr = captureAuthStderr(t, func() {
		rootCmd.SetArgs(append([]string{"auth", "login"}, args...))
		runErr = rootCmd.Execute()
	})
	return stderr, grantScopes, runErr
}

// TestAuthLogin_ClientCredentialsScopesDoNotFollowTheLevel: the grant gets
// --scopes as given, or none, also in a project directory where a browser
// login would leave scopes out. Nothing about left-out scopes may be printed.
func TestAuthLogin_ClientCredentialsScopesDoNotFollowTheLevel(t *testing.T) {
	const envURL = "https://abc12345.apps.dynatrace.com"
	global := loginConfigYAML("prod", loginTestContext{"prod", envURL, "prod-oauth", config.SafetyLevelReadWriteMine})
	project := loginConfigYAML("project", loginTestContext{"project", envURL, "prod-oauth", config.SafetyLevelReadOnly})

	tests := []struct {
		name string
		args []string
		want []string
	}{
		// One of the six scopes a browser login leaves out here, and a write scope.
		{"--scopes arrives unchanged", []string{"--scopes", "storage:user.events:read,document:documents:write"}, []string{"storage:user.events:read", "document:documents:write"}},
		{"no --scopes requests none", nil, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			setupLoginProject(t, global, project)
			stderr, grantScopes, runErr := runClientCredentialsGrant(t, append([]string{"--context", "project"}, tt.args...)...)
			if runErr != nil || grantScopes == nil {
				t.Fatalf("login: err %v, granted %v\n%s", runErr, grantScopes != nil, stderr)
			}
			if !slices.Equal(*grantScopes, tt.want) {
				t.Errorf("scopes passed to the grant: %q, want %q", *grantScopes, tt.want)
			}
			if want := "Safety level: readonly\n"; !strings.Contains(stderr, want) {
				t.Errorf("stderr lacks %q:\n%s", want, stderr)
			}
			for _, unwanted := range []string{"Left out", "requests only scopes", "so the login requests"} {
				if strings.Contains(stderr, unwanted) {
					t.Errorf("stderr contains %q:\n%s", unwanted, stderr)
				}
			}
		})
	}
}

// TestAuthLogin_BothFlagsKeepTheContextsTokenName: --token-name "defaults to
// existing token name", and that holds when --context and --environment are
// both given for a context that already exists.
func TestAuthLogin_BothFlagsKeepTheContextsTokenName(t *testing.T) {
	const envURL = "https://abc12345.apps.dynatrace.com"
	configPath := setupAuthTestConfig(t, "viewer", envURL, "team-oauth")
	gFlags.cfgFile = configPath
	t.Cleanup(func() { gFlags.cfgFile = "" })

	stderr, err, granted := runClientCredentialsLogin(t, "--context", "viewer", "--environment", envURL)
	if err != nil || !granted {
		t.Fatalf("login: err %v, granted %v\n%s", err, granted, stderr)
	}
	if want := "as 'team-oauth'"; !strings.Contains(stderr, want) {
		t.Errorf("stderr lacks %q:\n%s", want, stderr)
	}
	after, err := config.LoadFrom(configPath)
	if err != nil {
		t.Fatalf("load saved config: %v", err)
	}
	nc, err := after.GetContext("viewer")
	if err != nil {
		t.Fatalf("context missing: %v", err)
	}
	if nc.Context.TokenRef != "team-oauth" {
		t.Errorf("token-ref = %q, want %q", nc.Context.TokenRef, "team-oauth")
	}

	// A context that does not exist yet still gets <context>-oauth.
	stderr, err, _ = runClientCredentialsLogin(t, "--context", "fresh", "--environment", envURL)
	if err != nil {
		t.Fatalf("login to a new context: %v\n%s", err, stderr)
	}
	if want := "as 'fresh-oauth'"; !strings.Contains(stderr, want) {
		t.Errorf("stderr lacks %q:\n%s", want, stderr)
	}
}

// TestAuthLogin_EmptySafetyLevelIsRejected: an explicitly empty --safety-level
// (an unset shell variable) used to count as given and mean readwrite-all,
// which raised a readonly context.
func TestAuthLogin_EmptySafetyLevelIsRejected(t *testing.T) {
	const envURL = "https://abc12345.apps.dynatrace.com"
	configPath := setupAuthTestConfig(t, "other", envURL, "other-oauth")
	cfg, err := config.LoadFrom(configPath)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	cfg.SetContextWithOptions("viewer", envURL, "viewer-oauth", &config.ContextOptions{SafetyLevel: config.SafetyLevelReadOnly})
	if err := cfg.SaveTo(configPath); err != nil {
		t.Fatalf("save config: %v", err)
	}
	gFlags.cfgFile = configPath
	t.Cleanup(func() { gFlags.cfgFile = "" })

	stderr, runErr, granted := runClientCredentialsLogin(t, "--context", "viewer", "--safety-level", "")
	if !errors.Is(runErr, errEmptyFlagValue) {
		t.Fatalf("err = %v, want the empty flag value error\n%s", runErr, stderr)
	}
	if !strings.Contains(runErr.Error(), "safety-level") {
		t.Errorf("err = %q, want it to name --safety-level", runErr)
	}
	if granted {
		t.Error("a token was requested")
	}
	after, err := config.LoadFrom(configPath)
	if err != nil {
		t.Fatalf("load config after: %v", err)
	}
	nc, err := after.GetContext("viewer")
	if err != nil {
		t.Fatalf("context missing: %v", err)
	}
	if nc.Context.SafetyLevel != config.SafetyLevelReadOnly {
		t.Errorf("safety level = %q, want readonly", nc.Context.SafetyLevel)
	}
}

// TestAuthLogin_InvalidSafetyLevelFlagIsInvalidInput: a value that is not a
// level is refused with the valid ones, as invalid input: the usage exit code
// and one agent-mode code, the same as an empty value of the flag. As a plain
// error it exited 1 and was classified by its text, which quotes the value.
func TestAuthLogin_InvalidSafetyLevelFlagIsInvalidInput(t *testing.T) {
	const envURL = "https://abc12345.apps.dynatrace.com"
	for _, value := range []string{"bogus", "timeout", "my-config", "no context"} {
		t.Run(value, func(t *testing.T) {
			configPath := setupAuthTestConfig(t, "viewer", envURL, "viewer-oauth")
			gFlags.cfgFile = configPath
			t.Cleanup(func() { gFlags.cfgFile = "" })

			stderr, runErr, granted := runClientCredentialsLogin(t, "--context", "viewer", "--safety-level", value)
			if granted {
				t.Errorf("a token was requested\n%s", stderr)
			}
			if runErr == nil {
				t.Fatalf("no error\n%s", stderr)
			}
			if want := fmt.Sprintf("invalid safety level %q (valid values: readonly, readwrite-mine, readwrite-all, dangerously-unrestricted)", value); runErr.Error() != want {
				t.Errorf("error = %q\nwant    %q", runErr, want)
			}
			if detail := errorToDetail(context.Background(), runErr); detail.Code != "validation_error" {
				t.Errorf("code %q, want %q", detail.Code, "validation_error")
			}
			if got := exitCodeForError(runErr); got != client.ExitUsageError {
				t.Errorf("exit code %d, want %d", got, client.ExitUsageError)
			}
		})
	}
}

// TestShellWord: a name pasted into a suggested command must stay one
// argument, and an ordinary name must stay as it is.
func TestShellWord(t *testing.T) {
	tests := []struct{ in, want string }{
		{"prod", "prod"},
		{"team-a_1.eu@corp:x", "team-a_1.eu@corp:x"},
		{"my ctx", "'my ctx'"},
		{"a$b", "'a$b'"},
		{`a"b`, `'a"b'`},
		{"it's", `'it'\''s'`},
		{"a;b", "'a;b'"},
		{"", "''"},
	}
	for _, tt := range tests {
		if got := shellWord(tt.in); got != tt.want {
			t.Errorf("shellWord(%q) = %s, want %s", tt.in, got, tt.want)
		}
	}
}

// TestAuthLogin_RepairCommandsQuoteTheContextName: the refusals of an invalid
// level suggest commands that take the context's name, which may hold a space.
func TestAuthLogin_RepairCommandsQuoteTheContextName(t *testing.T) {
	const envURL = "https://abc12345.apps.dynatrace.com"

	t.Run("the context's own level", func(t *testing.T) {
		cfg := config.NewConfig()
		cfg.SetContextWithOptions("my ctx", envURL, "t-oauth", &config.ContextOptions{SafetyLevel: "read-only"})
		_, err := loginSafetyLevels(cfg, nil, "my ctx", "t-oauth", config.DefaultSafetyLevel, false)
		var diagErr *diagnostic.Error
		if !errors.As(err, &diagErr) {
			t.Fatalf("err = %T %v, want a diagnostic.Error", err, err)
		}
		want := []string{
			"Give the context a valid level, then log in again: dtctl ctx set 'my ctx' --safety-level <level>",
			"Or set it with the login: dtctl auth login --context 'my ctx' --safety-level <level>",
		}
		if !slices.Equal(diagErr.Suggestions, want) {
			t.Errorf("suggestions = %q, want %q", diagErr.Suggestions, want)
		}
	})

	t.Run("a global context's level", func(t *testing.T) {
		cfg := config.NewConfig()
		global := []config.NamedContext{{Name: "my prod", Context: config.Context{Environment: envURL, TokenRef: "t-oauth", SafetyLevel: "read-only"}}}
		_, err := loginSafetyLevels(cfg, global, "project", "t-oauth", config.DefaultSafetyLevel, false)
		var diagErr *diagnostic.Error
		if !errors.As(err, &diagErr) {
			t.Fatalf("err = %T %v, want a diagnostic.Error", err, err)
		}
		want := []string{"Give the global context a valid level: dtctl ctx set 'my prod' --global --safety-level <level>"}
		if !slices.Equal(diagErr.Suggestions, want) {
			t.Errorf("suggestions = %q, want %q", diagErr.Suggestions, want)
		}
	})
}

// loginTestContext is one context of a config file written by loginConfigYAML.
type loginTestContext struct {
	name, environment, tokenRef string
	level                       config.SafetyLevel // "" writes no safety-level key
}

// loginConfigYAML renders a config file with the given contexts.
func loginConfigYAML(current string, contexts ...loginTestContext) string {
	var b strings.Builder
	fmt.Fprintf(&b, "apiVersion: v1\nkind: Config\ncurrent-context: %s\ncontexts:\n", current)
	for _, c := range contexts {
		fmt.Fprintf(&b, "  - name: %s\n    context:\n      environment: %s\n      token-ref: %s\n", c.name, c.environment, c.tokenRef)
		if c.level != "" {
			fmt.Fprintf(&b, "      safety-level: %s\n", c.level)
		}
	}
	return b.String()
}

// setupLoginProject puts the test in a project directory: global becomes the
// global config and local the .dtctl.yaml that auto-discovery finds. It
// returns the path of the .dtctl.yaml.
func setupLoginProject(t *testing.T, global, local string) string {
	t.Helper()
	// Not parallel: the working directory and XDG_CONFIG_HOME are process-wide.
	xdgDir := t.TempDir()
	globalDir := filepath.Join(xdgDir, "dtctl")
	if err := os.MkdirAll(globalDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(globalDir, "config"), []byte(global), 0o600); err != nil {
		t.Fatal(err)
	}
	setXDGHome(t, "XDG_CONFIG_HOME", xdgDir)
	// A DTCTL_CONFIG in the developer's shell would short-circuit discovery.
	t.Setenv(config.EnvConfig, "")

	projectDir := t.TempDir()
	localPath := filepath.Join(projectDir, ".dtctl.yaml")
	if err := os.WriteFile(localPath, []byte(local), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Chdir(projectDir)
	return localPath
}

// TestAuthLogin_ProjectConfigRequestsNoMoreThanTheTokensBinding: in a project
// directory dtctl enforces no more than the global config binds to a token.
// The login requests scopes for that level whichever way the level and the
// token name reach it: from the context, from a flag or from the default.
func TestAuthLogin_ProjectConfigRequestsNoMoreThanTheTokensBinding(t *testing.T) {
	const envURL = "https://abc12345.apps.dynatrace.com"
	global := loginConfigYAML("prod", loginTestContext{"prod", envURL, "prod-oauth", config.SafetyLevelReadOnly})

	tests := []struct {
		name string
		// global replaces the global config of the other rows when set.
		global    string
		project   loginTestContext
		args      []string
		wantLevel config.SafetyLevel
		// wantNotice is the line that explains the lower level; "" = no such line.
		wantNotice string
	}{
		{
			name:       "the binding of the token the login writes, not of the one the context stores",
			project:    loginTestContext{"project", envURL, "mine-oauth", config.SafetyLevelDangerouslyUnrestricted},
			args:       []string{"--context", "project", "--token-name", "prod-oauth"},
			wantLevel:  config.SafetyLevelReadOnly,
			wantNotice: `The context declares dangerously-unrestricted; the global config allows token "prod-oauth" readonly, so the login requests readonly scopes.`,
		},
		{
			name:       "a new context",
			project:    loginTestContext{"project", envURL, "prod-oauth", config.SafetyLevelReadOnly},
			args:       []string{"--context", "fresh", "--environment", envURL, "--token-name", "prod-oauth"},
			wantLevel:  config.SafetyLevelReadOnly,
			wantNotice: `The default level is readwrite-all; the global config allows token "prod-oauth" readonly, so the login requests readonly scopes.`,
		},
		{
			name:       "--safety-level",
			project:    loginTestContext{"project", envURL, "prod-oauth", config.SafetyLevelReadOnly},
			args:       []string{"--context", "project", "--safety-level", "dangerously-unrestricted"},
			wantLevel:  config.SafetyLevelReadOnly,
			wantNotice: `--safety-level dangerously-unrestricted was given; the global config allows token "prod-oauth" readonly, so the login requests readonly scopes.`,
		},
		{
			name:       "a context that stores no level",
			project:    loginTestContext{"project", envURL, "prod-oauth", ""},
			args:       []string{"--context", "project"},
			wantLevel:  config.SafetyLevelReadOnly,
			wantNotice: `The default level is readwrite-all; the global config allows token "prod-oauth" readonly, so the login requests readonly scopes.`,
		},
		{
			name:       "a token the global config does not bind",
			project:    loginTestContext{"project", envURL, "mine-oauth", config.SafetyLevelDangerouslyUnrestricted},
			args:       []string{"--context", "project"},
			wantLevel:  config.SafetyLevelReadWriteAll,
			wantNotice: `The context declares dangerously-unrestricted; the global config binds no context to token "mine-oauth", which limits a project config to readwrite-all, so the login requests readwrite-all scopes.`,
		},
		{
			// The sdk binds a token to hosts; an environment without one binds nothing.
			name:       "a global context whose environment has no host binds nothing",
			global:     loginConfigYAML("prod", loginTestContext{"prod", "not-a-url", "mine-oauth", config.SafetyLevelDangerouslyUnrestricted}),
			project:    loginTestContext{"project", envURL, "mine-oauth", config.SafetyLevelDangerouslyUnrestricted},
			args:       []string{"--context", "project"},
			wantLevel:  config.SafetyLevelReadWriteAll,
			wantNotice: `The context declares dangerously-unrestricted; the global config binds no context to token "mine-oauth", which limits a project config to readwrite-all, so the login requests readwrite-all scopes.`,
		},
		{
			name:      "a level within the binding needs no explanation",
			project:   loginTestContext{"project", envURL, "prod-oauth", config.SafetyLevelReadOnly},
			args:      []string{"--context", "project"},
			wantLevel: config.SafetyLevelReadOnly,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			globalConfig := global
			if tt.global != "" {
				globalConfig = tt.global
			}
			setupLoginProject(t, globalConfig, loginConfigYAML("project", tt.project))

			stderr, oauthConfig, runErr := runBrowserLogin(t, tt.args...)
			if !errors.Is(runErr, errBrowserLoginStubbed) {
				t.Fatalf("auth login: %v, want it to reach the browser step\n%s", runErr, stderr)
			}
			if want := "Safety level: " + string(tt.wantLevel) + "\n"; !strings.Contains(stderr, want) {
				t.Errorf("stderr lacks %q:\n%s", want, stderr)
			}
			if want := auth.GetScopesForSafetyLevel(tt.wantLevel); !slices.Equal(oauthConfig.Scopes, want) {
				t.Errorf("requested scopes:\n%v\nwant those of %s:\n%v", oauthConfig.Scopes, tt.wantLevel, want)
			}
			if got, want := slices.Contains(oauthConfig.Scopes, "document:documents:write"), tt.wantLevel != config.SafetyLevelReadOnly; got != want {
				t.Errorf("document:documents:write requested = %v, want %v", got, want)
			}
			if tt.wantNotice == "" {
				if strings.Contains(stderr, "so the login requests") {
					t.Errorf("stderr explains a lower level that was not applied:\n%s", stderr)
				}
			} else if !strings.Contains(stderr, tt.wantNotice) {
				t.Errorf("stderr lacks %q:\n%s", tt.wantNotice, stderr)
			}
		})
	}
}

// TestAuthLogin_ContextWithoutALevelIsNotGivenOne: a login to a context that
// stores no level must not write one. In a project directory the written
// default would also claim a level dtctl does not enforce there.
func TestAuthLogin_ContextWithoutALevelIsNotGivenOne(t *testing.T) {
	const envURL = "https://abc12345.apps.dynatrace.com"
	global := loginConfigYAML("prod", loginTestContext{"prod", envURL, "prod-oauth", config.SafetyLevelReadOnly})
	localPath := setupLoginProject(t, global, loginConfigYAML("project", loginTestContext{"project", envURL, "prod-oauth", ""}))

	// Save the file once so that it is in the form dtctl writes.
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if err := cfg.SaveTo(localPath); err != nil {
		t.Fatalf("save: %v", err)
	}
	before, err := os.ReadFile(localPath)
	if err != nil {
		t.Fatal(err)
	}

	stderr, runErr, granted := runClientCredentialsLogin(t, "--context", "project")
	if runErr != nil || !granted {
		t.Fatalf("login: err %v, granted %v\n%s", runErr, granted, stderr)
	}
	if want := "Safety level: readonly\n"; !strings.Contains(stderr, want) {
		t.Errorf("stderr lacks %q:\n%s", want, stderr)
	}
	if strings.Contains(stderr, "The context declares") {
		t.Errorf("stderr says the context declares a level; its file has none:\n%s", stderr)
	}
	after, err := os.ReadFile(localPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(before) {
		t.Errorf("the login changed the project file.\nbefore:\n%s\nafter:\n%s", before, after)
	}
}

// TestAuthLogin_CurrentContextKeepsItsLevel: a plain `dtctl auth login` names
// no context, and the current one must keep its level all the same. That is
// the case the kept level exists for: renewing an expired session.
func TestAuthLogin_CurrentContextKeepsItsLevel(t *testing.T) {
	const envURL = "https://abc12345.apps.dynatrace.com"
	setup := func(t *testing.T) string {
		t.Helper()
		configPath := setupAuthTestConfig(t, "viewer", envURL, "viewer-oauth")
		cfg, err := config.LoadFrom(configPath)
		if err != nil {
			t.Fatalf("load config: %v", err)
		}
		cfg.SetContextWithOptions("viewer", envURL, "viewer-oauth", &config.ContextOptions{SafetyLevel: config.SafetyLevelReadOnly})
		if err := cfg.SaveTo(configPath); err != nil {
			t.Fatalf("save config: %v", err)
		}
		gFlags.cfgFile = configPath
		t.Cleanup(func() { gFlags.cfgFile = "" })
		return configPath
	}

	t.Run("the browser login requests its scopes", func(t *testing.T) {
		setup(t)
		stderr, oauthConfig, runErr := runBrowserLogin(t)
		if !errors.Is(runErr, errBrowserLoginStubbed) {
			t.Fatalf("auth login: %v, want it to reach the browser step\n%s", runErr, stderr)
		}
		if want := auth.GetScopesForSafetyLevel(config.SafetyLevelReadOnly); !slices.Equal(oauthConfig.Scopes, want) {
			t.Errorf("%d scopes requested, want the %d of readonly", len(oauthConfig.Scopes), len(want))
		}
		if want := "Safety level: readonly\n"; !strings.Contains(stderr, want) {
			t.Errorf("stderr lacks %q:\n%s", want, stderr)
		}
	})

	t.Run("the saved context keeps it", func(t *testing.T) {
		configPath := setup(t)
		stderr, runErr, granted := runClientCredentialsLogin(t)
		if runErr != nil || !granted {
			t.Fatalf("login: err %v, granted %v\n%s", runErr, granted, stderr)
		}
		after, err := config.LoadFrom(configPath)
		if err != nil {
			t.Fatalf("load saved config: %v", err)
		}
		nc, err := after.GetContext("viewer")
		if err != nil {
			t.Fatalf("context missing: %v", err)
		}
		if nc.Context.SafetyLevel != config.SafetyLevelReadOnly {
			t.Errorf("safety level = %q, want readonly", nc.Context.SafetyLevel)
		}
	})
}

// TestAuthLogin_ContextWithoutALevelOutsideAProject: a context that stores no
// level is still saved by a login, here with a new environment, and still
// without a level. And since nothing clamps it outside a project directory,
// the login has no lower level to explain.
func TestAuthLogin_ContextWithoutALevelOutsideAProject(t *testing.T) {
	const (
		envURL = "https://abc12345.apps.dynatrace.com"
		newURL = "https://xyz67890.apps.dynatrace.com"
	)
	setup := func(t *testing.T) string {
		t.Helper()
		// setupAuthTestConfig writes the context without a level.
		configPath := setupAuthTestConfig(t, "plain", envURL, "plain-oauth")
		gFlags.cfgFile = configPath
		t.Cleanup(func() { gFlags.cfgFile = "" })
		return configPath
	}

	t.Run("the login saves the context, without a level", func(t *testing.T) {
		configPath := setup(t)
		stderr, runErr, granted := runClientCredentialsLogin(t, "--context", "plain", "--environment", newURL)
		if runErr != nil || !granted {
			t.Fatalf("login: err %v, granted %v\n%s", runErr, granted, stderr)
		}
		raw, err := os.ReadFile(configPath)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(raw), newURL) || strings.Contains(string(raw), envURL) {
			t.Errorf("the saved context is not on %s:\n%s", newURL, raw)
		}
		if strings.Contains(string(raw), "safety-level") {
			t.Errorf("the login wrote a safety level:\n%s", raw)
		}
	})

	t.Run("the browser login explains no lower level", func(t *testing.T) {
		setup(t)
		stderr, oauthConfig, runErr := runBrowserLogin(t, "--context", "plain")
		if !errors.Is(runErr, errBrowserLoginStubbed) {
			t.Fatalf("auth login: %v, want it to reach the browser step\n%s", runErr, stderr)
		}
		if want := auth.GetScopesForSafetyLevel(config.DefaultSafetyLevel); !slices.Equal(oauthConfig.Scopes, want) {
			t.Errorf("%d scopes requested, want the %d of the default level", len(oauthConfig.Scopes), len(want))
		}
		if strings.Contains(stderr, "so the login requests") {
			t.Errorf("stderr explains a lower level that was not applied:\n%s", stderr)
		}
	})
}

// TestAuthLogin_NewProjectContextIsNotGivenTheDefault: a login that creates a
// context in a project directory without --safety-level must not write the
// default into .dtctl.yaml. Nobody chose it, and dtctl enforces the level of
// the token's global binding there, so the file would declare more than holds.
func TestAuthLogin_NewProjectContextIsNotGivenTheDefault(t *testing.T) {
	const envURL = "https://abc12345.apps.dynatrace.com"
	global := loginConfigYAML("prod", loginTestContext{"prod", envURL, "prod-oauth", config.SafetyLevelReadOnly})
	localPath := setupLoginProject(t, global, loginConfigYAML("project", loginTestContext{"project", envURL, "prod-oauth", config.SafetyLevelReadOnly}))

	stderr, runErr, granted := runClientCredentialsLogin(t, "--context", "fresh", "--environment", envURL, "--token-name", "prod-oauth")
	if runErr != nil || !granted {
		t.Fatalf("login: err %v, granted %v\n%s", runErr, granted, stderr)
	}
	if want := "Safety level: readonly\n"; !strings.Contains(stderr, want) {
		t.Errorf("stderr lacks %q:\n%s", want, stderr)
	}
	after, err := config.LoadFrom(localPath)
	if err != nil {
		t.Fatalf("load the project file: %v", err)
	}
	nc, err := after.GetContext("fresh")
	if err != nil {
		t.Fatalf("context not saved: %v", err)
	}
	if nc.Context.SafetyLevel != "" {
		t.Errorf("the project file gives the new context the level %q, want none", nc.Context.SafetyLevel)
	}
	raw, err := os.ReadFile(localPath)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Count(string(raw), "safety-level:"); got != 1 {
		t.Errorf("the project file has %d safety-level keys, want only the one of \"project\":\n%s", got, raw)
	}

	// The next login finds a context without a level, not one that declares
	// the default.
	stderr, _, runErr = runBrowserLogin(t, "--context", "fresh")
	if !errors.Is(runErr, errBrowserLoginStubbed) {
		t.Fatalf("auth login: %v, want it to reach the browser step\n%s", runErr, stderr)
	}
	if want := `The default level is readwrite-all; the global config allows token "prod-oauth" readonly, so the login requests readonly scopes.`; !strings.Contains(stderr, want) {
		t.Errorf("stderr lacks %q:\n%s", want, stderr)
	}

	t.Run("--safety-level is saved", func(t *testing.T) {
		stderr, runErr, _ := runClientCredentialsLogin(t, "--context", "pinned", "--environment", envURL, "--token-name", "prod-oauth", "--safety-level", "readonly")
		if runErr != nil {
			t.Fatalf("login: %v\n%s", runErr, stderr)
		}
		after, err := config.LoadFrom(localPath)
		if err != nil {
			t.Fatalf("load the project file: %v", err)
		}
		nc, err := after.GetContext("pinned")
		if err != nil {
			t.Fatalf("context not saved: %v", err)
		}
		if nc.Context.SafetyLevel != config.SafetyLevelReadOnly {
			t.Errorf("level %q, want readonly", nc.Context.SafetyLevel)
		}
	})
}

// TestAuthLogin_InvalidLevelInTheGlobalBindingIsRefused: in a project
// directory a global context that binds the login's token to a level that is
// not a level stops the login before a token is requested. dtctl ranks such a
// value with readwrite-all, so a clamp alone lets every project level at or
// below that through and makes the outcome of a tie depend on the order of
// the global contexts.
func TestAuthLogin_InvalidLevelInTheGlobalBindingIsRefused(t *testing.T) {
	const envURL = "https://abc12345.apps.dynatrace.com"
	invalid := loginTestContext{"prod", envURL, "prod-oauth", "read-only"}
	writer := loginTestContext{"writer", envURL, "prod-oauth", config.SafetyLevelReadWriteAll}
	viewer := loginTestContext{"viewer", envURL, "prod-oauth", config.SafetyLevelReadOnly}

	tests := []struct {
		name    string
		global  []loginTestContext
		project config.SafetyLevel
	}{
		{"project readonly", []loginTestContext{invalid}, config.SafetyLevelReadOnly},
		{"project readwrite-mine", []loginTestContext{invalid}, config.SafetyLevelReadWriteMine},
		{"project readwrite-all", []loginTestContext{invalid}, config.SafetyLevelReadWriteAll},
		{"project without a level", []loginTestContext{invalid}, ""},
		{"project dangerously-unrestricted", []loginTestContext{invalid}, config.SafetyLevelDangerouslyUnrestricted},
		// The invalid value ranks with readwrite-all: neither order may win.
		{"listed before a readwrite-all context", []loginTestContext{invalid, writer}, config.SafetyLevelDangerouslyUnrestricted},
		{"listed after a readwrite-all context", []loginTestContext{writer, invalid}, config.SafetyLevelDangerouslyUnrestricted},
		// A lower, valid level in another context is the ceiling and still
		// does not make the typo acceptable.
		{"next to a readonly context", []loginTestContext{viewer, invalid}, config.SafetyLevelReadOnly},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			setupLoginProject(t, loginConfigYAML("prod", tt.global...),
				loginConfigYAML("project", loginTestContext{"project", envURL, "prod-oauth", tt.project}))

			stderr, runErr, granted := runClientCredentialsLogin(t, "--context", "project")
			if granted {
				t.Errorf("a token was requested\n%s", stderr)
			}
			var diagErr *diagnostic.Error
			if !errors.As(runErr, &diagErr) {
				t.Fatalf("err = %T %v, want a diagnostic.Error", runErr, runErr)
			}
			if want := `global context "prod" binds token "prod-oauth" to the safety level "read-only", which is not a valid value (valid values: readonly, readwrite-mine, readwrite-all, dangerously-unrestricted)`; diagErr.Message != want {
				t.Errorf("message = %q\nwant      %q", diagErr.Message, want)
			}
			if diagErr.Operation != "auth login" {
				t.Errorf("operation = %q, want %q", diagErr.Operation, "auth login")
			}
			wantSuggestions := []string{
				"Give the global context a valid level: dtctl ctx set prod --global --safety-level <level>",
			}
			if !slices.Equal(diagErr.Suggestions, wantSuggestions) {
				t.Errorf("suggestions = %q, want %q", diagErr.Suggestions, wantSuggestions)
			}
			if strings.Contains(stderr, "Safety level:") {
				t.Errorf("the login printed a level:\n%s", stderr)
			}
		})
	}

	t.Run("every invalid context gets its repair command", func(t *testing.T) {
		setupLoginProject(t,
			loginConfigYAML("prod", invalid, writer, loginTestContext{"stage", envURL, "prod-oauth", "rw"}),
			loginConfigYAML("project", loginTestContext{"project", envURL, "prod-oauth", config.SafetyLevelReadOnly}))

		_, runErr, _ := runClientCredentialsLogin(t, "--context", "project")
		var diagErr *diagnostic.Error
		if !errors.As(runErr, &diagErr) {
			t.Fatalf("err = %T %v, want a diagnostic.Error", runErr, runErr)
		}
		wantSuggestions := []string{
			"Give the global context a valid level: dtctl ctx set prod --global --safety-level <level>",
			"Give the global context a valid level: dtctl ctx set stage --global --safety-level <level>",
		}
		if !slices.Equal(diagErr.Suggestions, wantSuggestions) {
			t.Errorf("suggestions = %q, want %q", diagErr.Suggestions, wantSuggestions)
		}
	})

	t.Run("a context that binds nothing is not read", func(t *testing.T) {
		// Another token's context, and one whose environment has no host:
		// neither is part of the binding dtctl enforces for prod-oauth.
		setupLoginProject(t,
			loginConfigYAML("prod", viewer,
				loginTestContext{"elsewhere", envURL, "other-oauth", "read-only"},
				loginTestContext{"broken", "not-a-url", "prod-oauth", "read-only"}),
			loginConfigYAML("project", loginTestContext{"project", envURL, "prod-oauth", config.SafetyLevelReadOnly}))

		stderr, runErr, granted := runClientCredentialsLogin(t, "--context", "project")
		if runErr != nil || !granted {
			t.Fatalf("login: err %v, granted %v\n%s", runErr, granted, stderr)
		}
	})
}

// TestAuthLogin_BrowserFlowNamesTheContextsItNarrows: the browser login
// requests the scopes of one level and replaces the stored token, so a context
// that shares the token and needs a scope outside that set is named before
// the browser opens.
func TestAuthLogin_BrowserFlowNamesTheContextsItNarrows(t *testing.T) {
	const (
		envURL     = "https://abc12345.apps.dynatrace.com"
		devTierURL = "https://abc12345.dev.apps.dynatracelabs.com"
	)

	t.Run("one context, by scope set and not by rank", func(t *testing.T) {
		// readonly ranks below readwrite-mine and still requests six read
		// scopes readwrite-mine does not, so it is the one that loses.
		configPath := setupAuthTestConfig(t, "other", envURL, "other-oauth")
		cfg, err := config.LoadFrom(configPath)
		if err != nil {
			t.Fatalf("load config: %v", err)
		}
		cfg.SetContextWithOptions("mine", envURL, "shared-oauth", &config.ContextOptions{SafetyLevel: config.SafetyLevelReadWriteMine})
		cfg.SetContextWithOptions("ro", envURL, "shared-oauth", &config.ContextOptions{SafetyLevel: config.SafetyLevelReadOnly})
		cfg.SetContextWithOptions("dev", devTierURL, "shared-oauth", &config.ContextOptions{SafetyLevel: config.SafetyLevelDangerouslyUnrestricted})
		if err := cfg.SaveTo(configPath); err != nil {
			t.Fatalf("save config: %v", err)
		}
		gFlags.cfgFile = configPath
		t.Cleanup(func() { gFlags.cfgFile = "" })

		stderr, _, runErr := runBrowserLogin(t, "--context", "mine")
		if !errors.Is(runErr, errBrowserLoginStubbed) {
			t.Fatalf("auth login: %v, want it to reach the browser step\n%s", runErr, stderr)
		}
		for _, want := range []string{
			`Token "shared-oauth" is also used by "ro", whose safety level requests scopes this login does not. The login replaces the token, so that context loses them.`,
			"Log in to it afterwards to restore its scopes, or give this context its own token with --token-name.",
		} {
			if !strings.Contains(stderr, want) {
				t.Errorf("stderr lacks %q:\n%s", want, stderr)
			}
		}
		// The dev tier keeps its own token under the same name.
		if strings.Contains(stderr, `"dev"`) {
			t.Errorf("stderr names a context of another tier:\n%s", stderr)
		}
	})

	t.Run("several contexts", func(t *testing.T) {
		configPath := setupAuthTestConfig(t, "viewer", envURL, "shared-oauth")
		cfg, err := config.LoadFrom(configPath)
		if err != nil {
			t.Fatalf("load config: %v", err)
		}
		cfg.SetContextWithOptions("viewer", envURL, "shared-oauth", &config.ContextOptions{SafetyLevel: config.SafetyLevelReadOnly})
		cfg.SetContextWithOptions("writer", envURL, "shared-oauth", &config.ContextOptions{SafetyLevel: config.SafetyLevelReadWriteAll})
		cfg.SetContextWithOptions("admin", envURL, "shared-oauth", &config.ContextOptions{SafetyLevel: config.SafetyLevelDangerouslyUnrestricted})
		if err := cfg.SaveTo(configPath); err != nil {
			t.Fatalf("save config: %v", err)
		}
		gFlags.cfgFile = configPath
		t.Cleanup(func() { gFlags.cfgFile = "" })

		stderr, _, runErr := runBrowserLogin(t, "--context", "viewer")
		if !errors.Is(runErr, errBrowserLoginStubbed) {
			t.Fatalf("auth login: %v, want it to reach the browser step\n%s", runErr, stderr)
		}
		for _, want := range []string{
			`Token "shared-oauth" is also used by "admin" and "writer", whose safety levels request scopes this login does not. The login replaces the token, so those contexts lose them.`,
			// A login to "writer" would leave "admin" ten scopes short.
			"Afterwards, log in to the context whose scopes you need (a login restores only those of its own context), or give this context its own token with --token-name.",
		} {
			if !strings.Contains(stderr, want) {
				t.Errorf("stderr lacks %q:\n%s", want, stderr)
			}
		}
	})

	t.Run("a global context that shares the token with a project context", func(t *testing.T) {
		// Two global contexts bind the token; the more restrictive one sets
		// the ceiling, so the login requests readonly scopes whatever the
		// project file declares, and the global readwrite-all context loses.
		global := loginConfigYAML("prod",
			loginTestContext{"prod", envURL, "prod-oauth", config.SafetyLevelReadOnly},
			loginTestContext{"writer", envURL, "prod-oauth", config.SafetyLevelReadWriteAll})
		setupLoginProject(t, global, loginConfigYAML("project", loginTestContext{"project", envURL, "prod-oauth", config.SafetyLevelDangerouslyUnrestricted}))

		stderr, _, runErr := runBrowserLogin(t, "--context", "project")
		if !errors.Is(runErr, errBrowserLoginStubbed) {
			t.Fatalf("auth login: %v, want it to reach the browser step\n%s", runErr, stderr)
		}
		for _, want := range []string{
			`Token "prod-oauth" is also used by "writer" (global config), whose safety level requests scopes this login does not. The login replaces the token, so that context loses them.`,
			"Log in to it afterwards to restore its scopes. A context of the global config is logged in to from outside this directory, or with --config " + config.DefaultConfigPath() + ". A token name of its own (--token-name) works here only once a context in the global config binds that name to this environment.",
		} {
			if !strings.Contains(stderr, want) {
				t.Errorf("stderr lacks %q:\n%s", want, stderr)
			}
		}

		// The advised login works from this directory: it reaches the
		// browser step for the global context, with that context's scopes.
		t.Cleanup(func() { gFlags.cfgFile = "" })
		stderr, oauthConfig, runErr := runBrowserLogin(t, "--config", config.DefaultConfigPath(), "--context", "writer")
		if !errors.Is(runErr, errBrowserLoginStubbed) {
			t.Fatalf("the advised login: %v, want it to reach the browser step\n%s", runErr, stderr)
		}
		if want := auth.GetScopesForSafetyLevel(config.SafetyLevelReadWriteAll); !slices.Equal(oauthConfig.Scopes, want) {
			t.Errorf("the advised login requests %d scopes, want the %d of readwrite-all", len(oauthConfig.Scopes), len(want))
		}
		// Without --config the same login cannot find the context.
		gFlags.cfgFile = ""
		if stderr, _, runErr := runBrowserLogin(t, "--context", "writer"); runErr == nil || errors.Is(runErr, errBrowserLoginStubbed) {
			t.Errorf("a login to the global context from the project directory: err %v\n%s", runErr, stderr)
		}
	})

	t.Run("a context whose own login would request no more is not named", func(t *testing.T) {
		// Both project contexts are readonly under a readwrite-mine binding,
		// so a login to either requests the same 38 scopes: "b" loses
		// nothing and could restore nothing. The global context can.
		global := loginConfigYAML("prod", loginTestContext{"prod", envURL, "prod-oauth", config.SafetyLevelReadWriteMine})
		setupLoginProject(t, global, loginConfigYAML("a",
			loginTestContext{"a", envURL, "prod-oauth", config.SafetyLevelReadOnly},
			loginTestContext{"b", envURL, "prod-oauth", config.SafetyLevelReadOnly}))

		stderr, oauthConfig, runErr := runBrowserLogin(t, "--context", "a")
		if !errors.Is(runErr, errBrowserLoginStubbed) {
			t.Fatalf("auth login: %v, want it to reach the browser step\n%s", runErr, stderr)
		}
		if got := len(oauthConfig.Scopes); got != 38 {
			t.Fatalf("%d scopes requested, want 38", got)
		}
		for _, want := range []string{
			`Token "prod-oauth" is also used by "prod" (global config), whose safety level requests scopes this login does not. The login replaces the token, so that context loses them.`,
			"Log in to it afterwards to restore its scopes.",
		} {
			if !strings.Contains(stderr, want) {
				t.Errorf("stderr lacks %q:\n%s", want, stderr)
			}
		}
		if strings.Contains(stderr, `"b"`) {
			t.Errorf("stderr names a context that loses nothing:\n%s", stderr)
		}
	})

	// Tokens are stored per OAuth tier and name, so a context on another
	// environment of the tier uses the stored token the login replaces. The
	// new token is requested for the login's environment. Whether the other
	// environment accepts it is not something dtctl knows, so the context is
	// named whatever the levels are.
	const otherURL = "https://xyz67890.apps.dynatrace.com"
	sharedAcrossEnvironments := func(t *testing.T, yURL string, yLevel config.SafetyLevel) {
		t.Helper()
		configPath := setupAuthTestConfig(t, "x", envURL, "shared")
		cfg, err := config.LoadFrom(configPath)
		if err != nil {
			t.Fatalf("load config: %v", err)
		}
		cfg.SetContextWithOptions("x", envURL, "shared", &config.ContextOptions{SafetyLevel: config.SafetyLevelReadWriteAll})
		cfg.SetContextWithOptions("y", yURL, "shared", &config.ContextOptions{SafetyLevel: yLevel})
		if err := cfg.SaveTo(configPath); err != nil {
			t.Fatalf("save config: %v", err)
		}
		gFlags.cfgFile = configPath
		t.Cleanup(func() { gFlags.cfgFile = "" })
	}
	const (
		otherEnvironmentWarning = `Token "shared" is also used by "y", which is on another environment. The login replaces the token with one requested for `
		ownTokenHint            = "Give this context its own token with --token-name to leave the stored one as it is."
	)

	t.Run("a context on another environment, same level", func(t *testing.T) {
		sharedAcrossEnvironments(t, otherURL, config.SafetyLevelReadWriteAll)

		stderr, _, runErr := runBrowserLogin(t, "--context", "x")
		if !errors.Is(runErr, errBrowserLoginStubbed) {
			t.Fatalf("auth login: %v, want it to reach the browser step\n%s", runErr, stderr)
		}
		for _, want := range []string{otherEnvironmentWarning + envURL + ".", ownTokenHint} {
			if !strings.Contains(stderr, want) {
				t.Errorf("stderr lacks %q:\n%s", want, stderr)
			}
		}
		if strings.Contains(stderr, "lose") {
			t.Errorf("stderr says a context loses scopes; the levels are equal:\n%s", stderr)
		}
	})

	t.Run("a context moved to another environment keeps its token name", func(t *testing.T) {
		// Both contexts are on one environment until the login moves "x":
		// --context with --environment keeps the token name "y" uses too.
		sharedAcrossEnvironments(t, envURL, config.SafetyLevelReadWriteAll)

		stderr, oauthConfig, runErr := runBrowserLogin(t, "--context", "x", "--environment", otherURL)
		if !errors.Is(runErr, errBrowserLoginStubbed) {
			t.Fatalf("auth login: %v, want it to reach the browser step\n%s", runErr, stderr)
		}
		if oauthConfig.EnvironmentURL != otherURL {
			t.Fatalf("the login is for %q, want %q", oauthConfig.EnvironmentURL, otherURL)
		}
		for _, want := range []string{otherEnvironmentWarning + otherURL + ".", ownTokenHint} {
			if !strings.Contains(stderr, want) {
				t.Errorf("stderr lacks %q:\n%s", want, stderr)
			}
		}
	})

	t.Run("a context on another environment is named once, with those that lose scopes", func(t *testing.T) {
		sharedAcrossEnvironments(t, otherURL, config.SafetyLevelDangerouslyUnrestricted)
		cfg, err := config.LoadFrom(gFlags.cfgFile)
		if err != nil {
			t.Fatalf("load config: %v", err)
		}
		cfg.SetContextWithOptions("admin", envURL, "shared", &config.ContextOptions{SafetyLevel: config.SafetyLevelDangerouslyUnrestricted})
		if err := cfg.SaveTo(gFlags.cfgFile); err != nil {
			t.Fatalf("save config: %v", err)
		}

		stderr, _, runErr := runBrowserLogin(t, "--context", "x")
		if !errors.Is(runErr, errBrowserLoginStubbed) {
			t.Fatalf("auth login: %v, want it to reach the browser step\n%s", runErr, stderr)
		}
		for _, want := range []string{
			`Token "shared" is also used by "admin", whose safety level requests scopes this login does not. The login replaces the token, so that context loses them.`,
			otherEnvironmentWarning + envURL + ".",
			"Log in to it afterwards to restore its scopes, or give this context its own token with --token-name.",
		} {
			if !strings.Contains(stderr, want) {
				t.Errorf("stderr lacks %q:\n%s", want, stderr)
			}
		}
		if got := strings.Count(stderr, `"y"`); got != 1 {
			t.Errorf("\"y\" is named %d times, want once:\n%s", got, stderr)
		}
		if got := strings.Count(stderr, "--token-name"); got != 1 {
			t.Errorf("--token-name is suggested %d times, want once:\n%s", got, stderr)
		}
	})

	t.Run("in a project directory, a global context on another environment", func(t *testing.T) {
		// The global config binds the token on two environments at one
		// level, so no context loses a scope and the one elsewhere is named.
		global := loginConfigYAML("prod",
			loginTestContext{"prod", envURL, "prod-oauth", config.SafetyLevelReadOnly},
			loginTestContext{"second", otherURL, "prod-oauth", config.SafetyLevelReadOnly})
		setupLoginProject(t, global, loginConfigYAML("project", loginTestContext{"project", envURL, "prod-oauth", config.SafetyLevelReadOnly}))

		stderr, _, runErr := runBrowserLogin(t, "--context", "project")
		if !errors.Is(runErr, errBrowserLoginStubbed) {
			t.Fatalf("auth login: %v, want it to reach the browser step\n%s", runErr, stderr)
		}
		for _, want := range []string{
			`Token "prod-oauth" is also used by "second" (global config), which is on another environment. The login replaces the token with one requested for ` + envURL + ".",
			"A token name of its own (--token-name) works here only once a context in the global config binds that name to this environment.",
		} {
			if !strings.Contains(stderr, want) {
				t.Errorf("stderr lacks %q:\n%s", want, stderr)
			}
		}
		for _, unwanted := range []string{"lose", "Give this context its own token", "Log in to"} {
			if strings.Contains(stderr, unwanted) {
				t.Errorf("stderr contains %q:\n%s", unwanted, stderr)
			}
		}
	})

	t.Run("several contexts on other environments", func(t *testing.T) {
		sharedAcrossEnvironments(t, otherURL, config.SafetyLevelReadOnly)
		cfg, err := config.LoadFrom(gFlags.cfgFile)
		if err != nil {
			t.Fatalf("load config: %v", err)
		}
		cfg.SetContextWithOptions("z", "https://def24680.apps.dynatrace.com", "shared", &config.ContextOptions{SafetyLevel: config.SafetyLevelReadOnly})
		if err := cfg.SaveTo(gFlags.cfgFile); err != nil {
			t.Fatalf("save config: %v", err)
		}

		stderr, _, runErr := runBrowserLogin(t, "--context", "x")
		if !errors.Is(runErr, errBrowserLoginStubbed) {
			t.Fatalf("auth login: %v, want it to reach the browser step\n%s", runErr, stderr)
		}
		if want := `Token "shared" is also used by "y" and "z", which are on other environments. The login replaces the token with one requested for ` + envURL + "."; !strings.Contains(stderr, want) {
			t.Errorf("stderr lacks %q:\n%s", want, stderr)
		}
	})

	t.Run("the client credentials login names a context on another environment too", func(t *testing.T) {
		sharedAcrossEnvironments(t, otherURL, config.SafetyLevelReadWriteAll)

		stderr, runErr, granted := runClientCredentialsLogin(t, "--context", "x")
		if runErr != nil || !granted {
			t.Fatalf("login: err %v, granted %v\n%s", runErr, granted, stderr)
		}
		if want := `Token "shared" is also used by "y". This login replaces it with the OAuth client's token.`; !strings.Contains(stderr, want) {
			t.Errorf("stderr lacks %q:\n%s", want, stderr)
		}
	})

	t.Run("nothing to say when no context loses a scope", func(t *testing.T) {
		configPath := setupAuthTestConfig(t, "viewer", envURL, "shared-oauth")
		cfg, err := config.LoadFrom(configPath)
		if err != nil {
			t.Fatalf("load config: %v", err)
		}
		cfg.SetContextWithOptions("viewer", envURL, "shared-oauth", &config.ContextOptions{SafetyLevel: config.SafetyLevelReadOnly})
		cfg.SetContextWithOptions("writer", envURL, "shared-oauth", &config.ContextOptions{SafetyLevel: config.SafetyLevelReadWriteAll})
		if err := cfg.SaveTo(configPath); err != nil {
			t.Fatalf("save config: %v", err)
		}
		gFlags.cfgFile = configPath
		t.Cleanup(func() { gFlags.cfgFile = "" })

		stderr, _, runErr := runBrowserLogin(t, "--context", "writer")
		if !errors.Is(runErr, errBrowserLoginStubbed) {
			t.Fatalf("auth login: %v, want it to reach the browser step\n%s", runErr, stderr)
		}
		if strings.Contains(stderr, "is also used by") {
			t.Errorf("stderr warns about a shared token nobody loses a scope of:\n%s", stderr)
		}
	})
}

// TestAuthLogin_ProjectConfigRequestsOnlyScopesTheBindingRequests: the scope
// sets of the levels are not nested. readonly requests six read scopes that
// readwrite-mine does not, so a rank alone lets a project context obtain
// scopes its token's owner never asked for. In a project directory the login
// requests the scopes of the enforced level that the global binding requests
// too. A context clamped to the binding's own level loses nothing.
func TestAuthLogin_ProjectConfigRequestsOnlyScopesTheBindingRequests(t *testing.T) {
	const envURL = "https://abc12345.apps.dynatrace.com"
	onlyReadonly := []string{
		"storage:application.snapshots:read",
		"storage:user.events:read",
		"storage:user.sessions:read",
		"storage:user.replays:read",
		"storage:fieldsets:read",
		"storage:fieldset-definitions:read",
	}
	const leftOut = `The login requests only scopes that the global binding of token "prod-oauth" (readwrite-mine) requests too. Left out: storage:application.snapshots:read, storage:user.events:read, storage:user.sessions:read, storage:user.replays:read, storage:fieldsets:read, storage:fieldset-definitions:read.`

	prod := func(level config.SafetyLevel) loginTestContext {
		return loginTestContext{"prod", envURL, "prod-oauth", level}
	}
	mine := loginTestContext{"mine", envURL, "prod-oauth", config.SafetyLevelReadWriteMine}

	tests := []struct {
		name string
		// global are the global contexts, all bound to the login's token.
		global     []loginTestContext
		declared   config.SafetyLevel
		wantScopes int
		wantSix    bool // whether the six read scopes are requested
		// wantNarrowed is the global context named as losing scopes.
		wantNarrowed string
	}{
		// Enforced readonly, bound readwrite-mine: the binding lacks the six.
		{"readonly under a readwrite-mine binding", []loginTestContext{prod(config.SafetyLevelReadWriteMine)}, config.SafetyLevelReadOnly, 38, false, `"prod" (global config)`},
		// Enforced readonly, bound readonly: the binding requests all of it.
		{"readwrite-mine under a readonly binding", []loginTestContext{prod(config.SafetyLevelReadOnly)}, config.SafetyLevelReadWriteMine, 44, true, ""},
		// Two global contexts bind the token: the most restrictive level by
		// rank is the binding, in either order. A second, readonly context
		// therefore raises a readonly project login from 38 scopes to 44,
		// the 44 that global context requests for the same stored token.
		{"readonly and readwrite-mine bind the token", []loginTestContext{prod(config.SafetyLevelReadOnly), mine}, config.SafetyLevelReadOnly, 44, true, `"mine" (global config)`},
		{"readwrite-mine and readonly bind the token", []loginTestContext{mine, prod(config.SafetyLevelReadOnly)}, config.SafetyLevelReadOnly, 44, true, `"mine" (global config)`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			global := loginConfigYAML("prod", tt.global...)
			setupLoginProject(t, global, loginConfigYAML("project", loginTestContext{"project", envURL, "prod-oauth", tt.declared}))

			stderr, oauthConfig, runErr := runBrowserLogin(t, "--context", "project")
			if !errors.Is(runErr, errBrowserLoginStubbed) {
				t.Fatalf("auth login: %v, want it to reach the browser step\n%s", runErr, stderr)
			}
			// Either way dtctl enforces readonly, the lower of the two.
			if want := "Safety level: readonly\n"; !strings.Contains(stderr, want) {
				t.Errorf("stderr lacks %q:\n%s", want, stderr)
			}
			if got := len(oauthConfig.Scopes); got != tt.wantScopes {
				t.Errorf("%d scopes requested, want %d", got, tt.wantScopes)
			}
			for _, s := range onlyReadonly {
				if got := slices.Contains(oauthConfig.Scopes, s); got != tt.wantSix {
					t.Errorf("%s requested = %v, want %v", s, got, tt.wantSix)
				}
			}
			for _, s := range oauthConfig.Scopes {
				if !slices.Contains(auth.GetScopesForSafetyLevel(config.SafetyLevelReadOnly), s) {
					t.Errorf("%s is requested; readonly does not request it", s)
				}
			}
			if got := strings.Contains(stderr, leftOut); got == tt.wantSix {
				t.Errorf("left-out line printed = %v, want %v:\n%s", got, !tt.wantSix, stderr)
			}
			if tt.wantSix && strings.Contains(stderr, "Left out") {
				t.Errorf("stderr reports scopes left out:\n%s", stderr)
			}
			// A global readwrite-mine context needs write scopes a readonly
			// login does not request; a global readonly one loses nothing.
			if tt.wantNarrowed == "" {
				if strings.Contains(stderr, "is also used by") {
					t.Errorf("stderr warns about a shared token nobody loses a scope of:\n%s", stderr)
				}
			} else if want := `Token "prod-oauth" is also used by ` + tt.wantNarrowed + ", whose safety level requests scopes this login does not."; !strings.Contains(stderr, want) {
				t.Errorf("stderr lacks %q:\n%s", want, stderr)
			}
		})
	}

	// A context at the level of its token's binding requests that level's
	// whole set, up to the 90 scopes of an unrestricted one.
	for _, level := range []config.SafetyLevel{config.SafetyLevelReadWriteMine, config.SafetyLevelReadWriteAll, config.SafetyLevelDangerouslyUnrestricted} {
		t.Run(string(level)+" under a binding of the same level", func(t *testing.T) {
			setupLoginProject(t, loginConfigYAML("prod", prod(level)),
				loginConfigYAML("project", loginTestContext{"project", envURL, "prod-oauth", level}))

			stderr, oauthConfig, runErr := runBrowserLogin(t, "--context", "project")
			if !errors.Is(runErr, errBrowserLoginStubbed) {
				t.Fatalf("auth login: %v, want it to reach the browser step\n%s", runErr, stderr)
			}
			if want := auth.GetScopesForSafetyLevel(level); !slices.Equal(oauthConfig.Scopes, want) {
				t.Errorf("%d scopes requested, want the %d of %s", len(oauthConfig.Scopes), len(want), level)
			}
			for _, unwanted := range []string{"Left out", "so the login requests", "is also used by"} {
				if strings.Contains(stderr, unwanted) {
					t.Errorf("stderr contains %q:\n%s", unwanted, stderr)
				}
			}
		})
	}

	t.Run("outside a project directory a readonly login requests all of readonly", func(t *testing.T) {
		configPath := setupAuthTestConfig(t, "other", envURL, "other-oauth")
		cfg, err := config.LoadFrom(configPath)
		if err != nil {
			t.Fatalf("load config: %v", err)
		}
		cfg.SetContextWithOptions("ro", envURL, "ro-oauth", &config.ContextOptions{SafetyLevel: config.SafetyLevelReadOnly})
		if err := cfg.SaveTo(configPath); err != nil {
			t.Fatalf("save config: %v", err)
		}
		gFlags.cfgFile = configPath
		t.Cleanup(func() { gFlags.cfgFile = "" })

		stderr, oauthConfig, runErr := runBrowserLogin(t, "--context", "ro")
		if !errors.Is(runErr, errBrowserLoginStubbed) {
			t.Fatalf("auth login: %v, want it to reach the browser step\n%s", runErr, stderr)
		}
		if got, want := len(oauthConfig.Scopes), 44; got != want {
			t.Errorf("%d scopes requested, want %d", got, want)
		}
		if strings.Contains(stderr, "Left out") {
			t.Errorf("stderr reports scopes left out:\n%s", stderr)
		}
	})
}
