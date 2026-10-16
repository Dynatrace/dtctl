package cmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/adrg/xdg"
	"github.com/spf13/cobra"

	"github.com/dynatrace-oss/dtctl/pkg/config"
)

func TestConfigSetCmd(t *testing.T) {
	// Create a temporary directory for the config file
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config")

	// Set the config path
	t.Setenv("XDG_CONFIG_HOME", tmpDir)
	xdg.Reload()
	defer xdg.Reload()

	tests := []struct {
		name      string
		key       string
		value     string
		wantError bool
		validate  func(t *testing.T, cfg *config.Config)
	}{
		{
			name:      "set editor preference",
			key:       "preferences.editor",
			value:     "vim",
			wantError: false,
			validate: func(t *testing.T, cfg *config.Config) {
				if cfg.Preferences.Editor != "vim" {
					t.Errorf("expected editor to be 'vim', got %q", cfg.Preferences.Editor)
				}
			},
		},
		{
			name:      "set editor to micro",
			key:       "preferences.editor",
			value:     "micro",
			wantError: false,
			validate: func(t *testing.T, cfg *config.Config) {
				if cfg.Preferences.Editor != "micro" {
					t.Errorf("expected editor to be 'micro', got %q", cfg.Preferences.Editor)
				}
			},
		},
		{
			name:      "unknown key",
			key:       "unknown.key",
			value:     "value",
			wantError: true,
			validate:  nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Clean up for fresh test
			_ = os.Remove(configPath)

			// Execute the RunE function directly with args
			err := configSetCmd.RunE(configSetCmd, []string{tt.key, tt.value})

			if tt.wantError {
				if err == nil {
					t.Error("expected error but got none")
				}
				return
			}

			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			// Validate the result
			if tt.validate != nil {
				cfg, err := config.Load()
				if err != nil {
					t.Fatalf("failed to load config: %v", err)
				}
				tt.validate(t, cfg)
			}
		})
	}
}

func TestConfigDeleteContextCmd(t *testing.T) {
	// Create a temporary directory for the config file
	tmpDir := t.TempDir()

	// Set the config path
	t.Setenv("XDG_CONFIG_HOME", tmpDir)
	xdg.Reload()
	defer xdg.Reload()

	tests := []struct {
		name         string
		setupConfig  func() *config.Config
		contextName  string
		wantError    bool
		errorContain string
		validate     func(t *testing.T, cfg *config.Config)
	}{
		{
			name: "delete existing context",
			setupConfig: func() *config.Config {
				cfg := config.NewConfig()
				cfg.SetContext("dev", "https://dev.dynatrace.com", "dev-token")
				cfg.SetContext("prod", "https://prod.dynatrace.com", "prod-token")
				cfg.CurrentContext = "dev"
				return cfg
			},
			contextName: "prod",
			wantError:   false,
			validate: func(t *testing.T, cfg *config.Config) {
				// Should only have one context left
				if len(cfg.Contexts) != 1 {
					t.Errorf("expected 1 context, got %d", len(cfg.Contexts))
				}
				// The remaining context should be dev
				if cfg.Contexts[0].Name != "dev" {
					t.Errorf("expected remaining context to be 'dev', got %q", cfg.Contexts[0].Name)
				}
				// Current context should still be dev
				if cfg.CurrentContext != "dev" {
					t.Errorf("expected current context to be 'dev', got %q", cfg.CurrentContext)
				}
			},
		},
		{
			name: "delete current context clears current-context",
			setupConfig: func() *config.Config {
				cfg := config.NewConfig()
				cfg.SetContext("dev", "https://dev.dynatrace.com", "dev-token")
				cfg.SetContext("prod", "https://prod.dynatrace.com", "prod-token")
				cfg.CurrentContext = "dev"
				return cfg
			},
			contextName: "dev",
			wantError:   false,
			validate: func(t *testing.T, cfg *config.Config) {
				// Should only have one context left
				if len(cfg.Contexts) != 1 {
					t.Errorf("expected 1 context, got %d", len(cfg.Contexts))
				}
				// Current context should be cleared
				if cfg.CurrentContext != "" {
					t.Errorf("expected current context to be cleared, got %q", cfg.CurrentContext)
				}
			},
		},
		{
			name: "delete non-existent context",
			setupConfig: func() *config.Config {
				cfg := config.NewConfig()
				cfg.SetContext("dev", "https://dev.dynatrace.com", "dev-token")
				cfg.CurrentContext = "dev"
				return cfg
			},
			contextName:  "nonexistent",
			wantError:    true,
			errorContain: "not found",
		},
		{
			name: "delete only context",
			setupConfig: func() *config.Config {
				cfg := config.NewConfig()
				cfg.SetContext("only", "https://only.dynatrace.com", "only-token")
				cfg.CurrentContext = "only"
				return cfg
			},
			contextName: "only",
			wantError:   false,
			validate: func(t *testing.T, cfg *config.Config) {
				// Should have no contexts left
				if len(cfg.Contexts) != 0 {
					t.Errorf("expected 0 contexts, got %d", len(cfg.Contexts))
				}
				// Current context should be cleared
				if cfg.CurrentContext != "" {
					t.Errorf("expected current context to be cleared, got %q", cfg.CurrentContext)
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Setup config
			cfg := tt.setupConfig()
			if err := cfg.Save(); err != nil {
				t.Fatalf("failed to save config: %v", err)
			}

			// Execute the RunE function directly with args
			err := configDeleteContextCmd.RunE(configDeleteContextCmd, []string{tt.contextName})

			if tt.wantError {
				if err == nil {
					t.Error("expected error but got none")
				}
				if tt.errorContain != "" && !strings.Contains(err.Error(), tt.errorContain) {
					t.Errorf("expected error to contain %q, got %q", tt.errorContain, err.Error())
				}
				return
			}

			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			// Validate the result
			if tt.validate != nil {
				cfg, err := config.Load()
				if err != nil {
					t.Fatalf("failed to load config: %v", err)
				}
				tt.validate(t, cfg)
			}
		})
	}
}

func TestConfigDeleteCredentialsCmd(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", tmpDir)
	xdg.Reload()
	defer xdg.Reload()

	cfg := config.NewConfig()
	cfg.SetContext("dev", "https://dev.dynatrace.com", "dev-token")
	if err := cfg.SetToken("dev-token", "secret-value"); err != nil {
		t.Fatalf("failed to seed credential: %v", err)
	}
	if err := cfg.SetToken("spare-token", "other-value"); err != nil {
		t.Fatalf("failed to seed credential: %v", err)
	}
	if err := cfg.Save(); err != nil {
		t.Fatalf("failed to save config: %v", err)
	}

	// Deleting a credential a context still names must succeed: the context is
	// simply unusable until a new credential is stored. Refusing would leave
	// the caller with no supported way to remove the secret.
	if err := deleteCredentials(mustFindCmd("config", "delete-credentials"), "dev-token"); err != nil {
		t.Fatalf("deleteCredentials() error = %v", err)
	}

	reloaded, err := config.Load()
	if err != nil {
		t.Fatalf("failed to load config: %v", err)
	}

	for _, nt := range reloaded.Tokens {
		if nt.Name == "dev-token" {
			t.Error("credential 'dev-token' still present in config after delete")
		}
	}
	if len(reloaded.Tokens) != 1 || reloaded.Tokens[0].Name != "spare-token" {
		t.Errorf("tokens = %+v, want only spare-token", reloaded.Tokens)
	}
	// The context itself is untouched — the two are deleted independently.
	if len(reloaded.Contexts) != 1 || reloaded.Contexts[0].Name != "dev" {
		t.Errorf("contexts = %+v, want dev retained", reloaded.Contexts)
	}
}

func TestConfigDeleteCredentialsCmd_Idempotent(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", tmpDir)
	xdg.Reload()
	defer xdg.Reload()

	if err := config.NewConfig().Save(); err != nil {
		t.Fatalf("failed to save config: %v", err)
	}

	// A teardown step must be safe to re-run after a partial or repeated cleanup.
	if err := deleteCredentials(mustFindCmd("config", "delete-credentials"), "never-stored"); err != nil {
		t.Errorf("deleteCredentials() for missing credential = %v, want nil", err)
	}
}

func TestDeleteContext_DeleteCredentials(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", tmpDir)
	xdg.Reload()
	defer xdg.Reload()

	cfg := config.NewConfig()
	cfg.SetContext("incident", "https://abc12345.apps.dynatrace.com", "incident-token")
	cfg.CurrentContext = "incident"
	if err := cfg.SetToken("incident-token", "secret-value"); err != nil {
		t.Fatalf("failed to seed credential: %v", err)
	}
	if err := cfg.Save(); err != nil {
		t.Fatalf("failed to save config: %v", err)
	}

	if err := deleteContext(mustFindCmd("config", "delete-context"), "incident", true); err != nil {
		t.Fatalf("deleteContext() error = %v", err)
	}

	reloaded, err := config.Load()
	if err != nil {
		t.Fatalf("failed to load config: %v", err)
	}

	if len(reloaded.Contexts) != 0 {
		t.Errorf("contexts = %+v, want none", reloaded.Contexts)
	}
	if len(reloaded.Tokens) != 0 {
		t.Errorf("tokens = %+v, want none — the credential must not outlive the context", reloaded.Tokens)
	}
}

func TestDeleteContext_KeepsCredentialByDefault(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", tmpDir)
	xdg.Reload()
	defer xdg.Reload()

	cfg := config.NewConfig()
	cfg.SetContext("incident", "https://abc12345.apps.dynatrace.com", "incident-token")
	if err := cfg.SetToken("incident-token", "secret-value"); err != nil {
		t.Fatalf("failed to seed credential: %v", err)
	}
	if err := cfg.Save(); err != nil {
		t.Fatalf("failed to save config: %v", err)
	}

	if err := deleteContext(mustFindCmd("config", "delete-context"), "incident", false); err != nil {
		t.Fatalf("deleteContext() error = %v", err)
	}

	reloaded, err := config.Load()
	if err != nil {
		t.Fatalf("failed to load config: %v", err)
	}

	// Default is opt-out so a shared token ref is never removed by surprise;
	// the command output names delete-credentials to finish the job.
	if len(reloaded.Tokens) != 1 || reloaded.Tokens[0].Name != "incident-token" {
		t.Errorf("tokens = %+v, want incident-token retained", reloaded.Tokens)
	}
}

func TestDeleteContext_RefusesSharedCredential(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", tmpDir)
	xdg.Reload()
	defer xdg.Reload()

	cfg := config.NewConfig()
	cfg.SetContext("incident", "https://abc12345.apps.dynatrace.com", "shared-token")
	cfg.SetContext("routine", "https://abc12345.apps.dynatrace.com", "shared-token")
	if err := cfg.SetToken("shared-token", "secret-value"); err != nil {
		t.Fatalf("failed to seed credential: %v", err)
	}
	if err := cfg.Save(); err != nil {
		t.Fatalf("failed to save config: %v", err)
	}

	// Deleting one context must not silently break the other.
	err := deleteContext(mustFindCmd("config", "delete-context"), "incident", true)
	if err == nil {
		t.Fatal("deleteContext() error = nil, want refusal for a shared credential")
	}
	if !strings.Contains(err.Error(), "shared") {
		t.Errorf("error = %q, want it to mention the credential is shared", err.Error())
	}

	reloaded, loadErr := config.Load()
	if loadErr != nil {
		t.Fatalf("failed to load config: %v", loadErr)
	}
	// The refusal must be a no-op: nothing half-deleted.
	if len(reloaded.Contexts) != 2 {
		t.Errorf("contexts = %+v, want both retained after refusal", reloaded.Contexts)
	}
	if len(reloaded.Tokens) != 1 {
		t.Errorf("tokens = %+v, want the shared credential retained", reloaded.Tokens)
	}
}

// TestCredentialTeardownPreservesEnvTemplates pins the write rule from
// CONFIG_CONTRACT.md: a command that rewrites the config file must load it
// without expanding ${VAR}. Loading with expansion and saving resolves every
// template and persists the resolved values — so removing one credential would
// write another credential's secret into the file in plaintext, in a file the
// docs describe as safe to commit.
func TestCredentialTeardownPreservesEnvTemplates(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", tmpDir)
	xdg.Reload()
	defer xdg.Reload()

	t.Setenv("DT_TEST_ENV_URL", "https://abc12345.apps.dynatrace.com")
	t.Setenv("DT_TEST_SECRET", "dt0s16.NOT-A-REAL-TOKEN.VALUE")

	configPath := filepath.Join(tmpDir, "dtctl", "config")
	if err := os.MkdirAll(filepath.Dir(configPath), 0o755); err != nil {
		t.Fatalf("failed to create config dir: %v", err)
	}
	const templated = `apiVersion: v1
kind: Config
current-context: templated
contexts:
    - name: templated
      context:
        environment: ${DT_TEST_ENV_URL}
        token-ref: templated-token
    - name: doomed
      context:
        environment: https://def67890.apps.dynatrace.com
        token-ref: doomed-token
tokens:
    - name: templated-token
      token: ${DT_TEST_SECRET}
    - name: doomed-token
      token: plain-value
`
	if err := os.WriteFile(configPath, []byte(templated), 0o600); err != nil {
		t.Fatalf("failed to write config: %v", err)
	}

	cases := []struct {
		name string
		run  func() error
	}{
		{"delete-credentials", func() error { return deleteCredentials(mustFindCmd("config", "delete-credentials"), "doomed-token") }},
		{"delete-context --delete-credentials", func() error { return deleteContext(mustFindCmd("config", "delete-context"), "doomed", true) }},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := os.WriteFile(configPath, []byte(templated), 0o600); err != nil {
				t.Fatalf("failed to reset config: %v", err)
			}

			if err := tc.run(); err != nil {
				t.Fatalf("%s error = %v", tc.name, err)
			}

			data, err := os.ReadFile(configPath)
			if err != nil {
				t.Fatalf("failed to read config: %v", err)
			}
			got := string(data)

			if strings.Contains(got, "dt0s16.NOT-A-REAL-TOKEN.VALUE") {
				t.Error("the other credential's secret was expanded into the config file in plaintext")
			}
			if !strings.Contains(got, "${DT_TEST_SECRET}") {
				t.Error("${DT_TEST_SECRET} template did not survive the rewrite")
			}
			if !strings.Contains(got, "${DT_TEST_ENV_URL}") {
				t.Error("${DT_TEST_ENV_URL} template did not survive the rewrite")
			}
			// The intended removal must still have happened. Checked against the
			// parsed token list, not the raw text: a surviving context
			// legitimately still names "doomed-token" in its token-ref.
			parsed, err := config.LoadFromWithoutExpansion(configPath)
			if err != nil {
				t.Fatalf("failed to parse config: %v", err)
			}
			for _, nt := range parsed.Tokens {
				if nt.Name == "doomed-token" {
					t.Error("doomed-token was not removed from the token list")
				}
			}
			// The untouched credential must survive, still as a template.
			if len(parsed.Tokens) != 1 || parsed.Tokens[0].Name != "templated-token" {
				t.Errorf("tokens = %+v, want only templated-token", parsed.Tokens)
			}
		})
	}
}

func TestCredentialTeardownHonorsDryRun(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", tmpDir)
	xdg.Reload()
	defer xdg.Reload()

	gFlags.dryRun = true
	defer func() { gFlags.dryRun = false }()

	cases := []struct {
		name string
		run  func() error
	}{
		{"delete-credentials", func() error { return deleteCredentials(mustFindCmd("config", "delete-credentials"), "keep-token") }},
		{"delete-context", func() error { return deleteContext(mustFindCmd("config", "delete-context"), "keep-ctx", true) }},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := config.NewConfig()
			cfg.SetContext("keep-ctx", "https://abc12345.apps.dynatrace.com", "keep-token")
			if err := cfg.SetToken("keep-token", "secret-value"); err != nil {
				t.Fatalf("failed to seed credential: %v", err)
			}
			if err := cfg.Save(); err != nil {
				t.Fatalf("failed to save config: %v", err)
			}

			if err := tc.run(); err != nil {
				t.Fatalf("%s error = %v", tc.name, err)
			}

			// A --dry-run that destroys the credential is the worst kind of
			// surprise in a teardown script, so nothing may change.
			reloaded, err := config.Load()
			if err != nil {
				t.Fatalf("failed to load config: %v", err)
			}
			if len(reloaded.Tokens) != 1 || reloaded.Tokens[0].Name != "keep-token" {
				t.Errorf("tokens = %+v, want keep-token untouched", reloaded.Tokens)
			}
			if len(reloaded.Contexts) != 1 || reloaded.Contexts[0].Name != "keep-ctx" {
				t.Errorf("contexts = %+v, want keep-ctx untouched", reloaded.Contexts)
			}
		})
	}
}

// TestDeleteCredentialsFlagIsWiredOnBothCommands guards the flag name itself.
// Both delete paths read --delete-credentials via Flags().GetBool, which returns
// false for an unregistered name rather than erroring — so a rename or a typo on
// either command would silently stop deleting credentials while still reporting
// success, which is the exact failure this feature exists to prevent.
func TestDeleteCredentialsFlagIsWiredOnBothCommands(t *testing.T) {
	for _, cmd := range []*cobra.Command{configDeleteContextCmd, ctxDeleteCmd} {
		if cmd.Flags().Lookup("delete-credentials") == nil {
			t.Errorf("%q does not register --delete-credentials", cmd.CommandPath())
		}
	}

	// delete-credentials must stay reachable under the name the skill and docs use.
	if configDeleteCredentialsCmd.Name() != "delete-credentials" {
		t.Errorf("credential delete command renamed to %q; docs and the shipped skill name 'delete-credentials'",
			configDeleteCredentialsCmd.Name())
	}
}

// TestDryRunPreviewMatchesRealRun pins that a preview does not promise a
// deletion the real run refuses. The shared-credential check must therefore run
// before the dry-run return, not after it.
func TestDryRunPreviewMatchesRealRun(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", tmpDir)
	xdg.Reload()
	defer xdg.Reload()

	cfg := config.NewConfig()
	cfg.SetContext("a", "https://abc12345.apps.dynatrace.com", "shared-token")
	cfg.SetContext("b", "https://abc12345.apps.dynatrace.com", "shared-token")
	if err := cfg.SetToken("shared-token", "secret-value"); err != nil {
		t.Fatalf("failed to seed credential: %v", err)
	}
	if err := cfg.Save(); err != nil {
		t.Fatalf("failed to save config: %v", err)
	}

	gFlags.dryRun = true
	defer func() { gFlags.dryRun = false }()

	if err := deleteContext(mustFindCmd("config", "delete-context"), "a", true); err == nil {
		t.Error("dry run reported success for a shared credential the real run refuses")
	}
}

// mustFindCmd resolves a real command out of the command tree. The delete
// helpers take the *cobra.Command they were invoked from, because a dry run
// reads the verb and resource for its envelope off it; a test must therefore
// hand them the same command production does, not a synthetic one.
func mustFindCmd(path ...string) *cobra.Command {
	c, _, err := rootCmd.Find(path)
	if err != nil {
		panic(err)
	}
	return c
}
