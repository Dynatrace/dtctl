package cmd

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/adrg/xdg"
	"github.com/spf13/pflag"
	"github.com/spf13/viper"

	"github.com/dynatrace-oss/dtctl/pkg/auth"
	"github.com/dynatrace-oss/dtctl/pkg/config"
	"github.com/dynatrace-oss/dtctl/pkg/diagnostic"
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

// resetAuthLoginFlags resets all auth login command flags to their default values.
// This is needed because cobra/pflag retains flag values across Execute() calls when
// the global rootCmd is reused in tests — a flag not present in the args of a new
// Execute() call keeps the value set by the previous call.
func resetAuthLoginFlags(t *testing.T) {
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
		prodURL = "https://irc65933.apps.dynatrace.com/"
		hardURL = "https://eva38390.sprint.apps.dynatracelabs.com/"
		prodTok = "prod1sfm-oauth"
		hardTok = "hardsfm-oauth"
	)

	makeCfg := func() *config.Config {
		cfg := config.NewConfig()
		cfg.SetContext("prod1sfm", prodURL, prodTok)
		cfg.SetContext("hardsfm", hardURL, hardTok)
		cfg.CurrentContext = "prod1sfm"
		return cfg
	}

	tests := []struct {
		name        string
		contextName string
		environment string
		tokenName   string
		currentCtx  string // override CurrentContext ("" means use default "prod1sfm")
		wantContext string
		wantEnv     string
		wantToken   string
		wantErr     bool
	}{
		{
			name:        "no flags uses current context",
			wantContext: "prod1sfm",
			wantEnv:     prodURL,
			wantToken:   prodTok,
		},
		{
			// Regression: before the fix, this would return prodURL for environment.
			name:        "explicit context uses named context URL not current context URL",
			contextName: "hardsfm",
			wantContext: "hardsfm",
			wantEnv:     hardURL,
			wantToken:   hardTok,
		},
		{
			name:        "explicit context and explicit environment uses provided values",
			contextName: "hardsfm",
			environment: "https://custom.example.invalid/",
			wantContext: "hardsfm",
			wantEnv:     "https://custom.example.invalid/",
			wantToken:   hardTok, // token still resolved from named context
		},
		{
			name:        "all flags explicit skips lookup entirely",
			contextName: "hardsfm",
			environment: "https://custom.example.invalid/",
			tokenName:   "my-explicit-token",
			wantContext: "hardsfm",
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
// Config: prod1sfm (active, prod URL) + hardsfm (sprint URL)
// Command: auth login --context hardsfm   (no --environment)
// Expected: hardsfm's sprint URL is used, command fails at keyring gate (not URL resolution).
func TestAuthLogin_ContextOnly_UsesNamedContextURL(t *testing.T) {
	viper.Reset()
	resetAuthLoginFlags(t)
	t.Setenv("DTCTL_DISABLE_KEYRING", "1")

	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config.yaml")

	cfg := config.NewConfig()
	cfg.SetContext("prod1sfm", "https://irc65933.apps.dynatrace.com/", "prod1sfm-oauth")
	cfg.SetContext("hardsfm", "https://eva38390.sprint.apps.dynatracelabs.com/", "hardsfm-oauth")
	cfg.CurrentContext = "prod1sfm"

	if err := cfg.SaveTo(configPath); err != nil {
		t.Fatalf("failed to save config: %v", err)
	}
	gFlags.cfgFile = configPath
	defer func() { gFlags.cfgFile = "" }()

	rootCmd.SetArgs([]string{"auth", "login", "--context", "hardsfm"})
	err := rootCmd.Execute()

	// The command must NOT fail with a context/environment validation error —
	// that would indicate hardsfm's URL was not resolved from config.
	if err != nil && strings.Contains(err.Error(), "--context and --environment are required") {
		t.Errorf("expected hardsfm's URL to be resolved from config, got: %v", err)
	}
	if err != nil && strings.Contains(err.Error(), "not found in config") {
		t.Errorf("expected hardsfm to be found in config, got: %v", err)
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
	t.Setenv("XDG_DATA_HOME", t.TempDir()) // keep the file token store off the real one

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
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	xdg.Reload()
	t.Cleanup(xdg.Reload)
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
				"--safety-level readonly gates dtctl itself; it does not narrow the token",
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
			t.Setenv("XDG_DATA_HOME", t.TempDir())
			xdg.Reload()
			t.Cleanup(xdg.Reload)
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
	t.Setenv("XDG_DATA_HOME", dataHome)
	xdg.Reload()
	t.Cleanup(xdg.Reload)

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
