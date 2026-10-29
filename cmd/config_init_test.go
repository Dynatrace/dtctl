package cmd

import (
	"os"
	"path/filepath"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/Dynatrace/dtctl/pkg/config"
)

func TestConfigInitCmd(t *testing.T) {
	tests := []struct {
		name         string
		args         []string
		existingFile bool
		wantErr      bool
		wantContext  string
	}{
		{
			name:         "create new config",
			args:         []string{},
			existingFile: false,
			wantErr:      false,
			wantContext:  "my-environment",
		},
		{
			name:         "create with custom context",
			args:         []string{"--context", "production"},
			existingFile: false,
			wantErr:      false,
			wantContext:  "production",
		},
		{
			name:         "fail on existing file without force",
			args:         []string{},
			existingFile: true,
			wantErr:      true,
		},
		{
			name:         "overwrite with force flag",
			args:         []string{"--force"},
			existingFile: true,
			wantErr:      false,
			wantContext:  "my-environment",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tmpDir := t.TempDir()
			origDir, err := os.Getwd()
			if err != nil {
				t.Fatal(err)
			}
			defer os.Chdir(origDir)

			if err := os.Chdir(tmpDir); err != nil {
				t.Fatal(err)
			}

			if tt.existingFile {
				if err := os.WriteFile(config.LocalConfigName, []byte("existing"), 0600); err != nil {
					t.Fatal(err)
				}
			}

			contextName := ""
			force := false
			for i := 0; i < len(tt.args); i++ {
				if tt.args[i] == "--context" && i+1 < len(tt.args) {
					contextName = tt.args[i+1]
					i++
				} else if tt.args[i] == "--force" {
					force = true
				}
			}

			configPath := config.LocalConfigName
			if _, err := os.Stat(configPath); err == nil {
				if !force {
					if !tt.wantErr {
						t.Error("Expected error when file exists without --force")
					}
					return
				}
			}

			template := createLocalConfigTemplate(contextName)

			data, marshalErr := yaml.Marshal(template)
			if marshalErr != nil {
				t.Fatalf("Failed to marshal config template: %v", marshalErr)
			}

			if err := os.WriteFile(configPath, data, 0600); err != nil {
				t.Fatalf("Failed to write %s: %v", configPath, err)
			}

			if tt.wantErr {
				return
			}

			if _, err := os.Stat(config.LocalConfigName); os.IsNotExist(err) {
				t.Error("Expected .dtctl.yaml to be created")
				return
			}

			fileData, err := os.ReadFile(config.LocalConfigName)
			if err != nil {
				t.Fatalf("Failed to read created file: %v", err)
			}

			var cfg config.Config
			if err := yaml.Unmarshal(fileData, &cfg); err != nil {
				t.Fatalf("Failed to parse created config: %v", err)
			}

			if cfg.APIVersion != "dtctl.io/v1" {
				t.Errorf("APIVersion = %q, want dtctl.io/v1", cfg.APIVersion)
			}
			if cfg.Kind != "Config" {
				t.Errorf("Kind = %q, want Config", cfg.Kind)
			}
			if cfg.CurrentContext != tt.wantContext {
				t.Errorf("CurrentContext = %q, want %q", cfg.CurrentContext, tt.wantContext)
			}

			if len(cfg.Contexts) != 1 {
				t.Fatalf("Expected 1 context, got %d", len(cfg.Contexts))
			}
			if cfg.Contexts[0].Name != tt.wantContext {
				t.Errorf("Context name = %q, want %q", cfg.Contexts[0].Name, tt.wantContext)
			}

			// Template uses env-var syntax for the URL; it is expanded at runtime
			// by NewClientFromConfig (after Dynatrace URL validation).
			env := cfg.Contexts[0].Context.Environment
			if env == "" {
				t.Error("Environment must not be empty")
			}

			// Template must not include inline tokens — auto-discovered local configs reject them.
			if len(cfg.Tokens) != 0 {
				t.Errorf("Expected no inline tokens in template, got %d", len(cfg.Tokens))
			}
		})
	}
}

func TestCreateLocalConfigTemplate(t *testing.T) {
	tests := []struct {
		name        string
		contextName string
		wantContext string
	}{
		{
			name:        "default context name",
			contextName: "",
			wantContext: "my-environment",
		},
		{
			name:        "custom context name",
			contextName: "production",
			wantContext: "production",
		},
		{
			name:        "dev context",
			contextName: "dev",
			wantContext: "dev",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := createLocalConfigTemplate(tt.contextName)

			if cfg.APIVersion != "dtctl.io/v1" {
				t.Errorf("APIVersion = %q, want dtctl.io/v1", cfg.APIVersion)
			}
			if cfg.Kind != "Config" {
				t.Errorf("Kind = %q, want Config", cfg.Kind)
			}
			if cfg.CurrentContext != tt.wantContext {
				t.Errorf("CurrentContext = %q, want %q", cfg.CurrentContext, tt.wantContext)
			}

			if len(cfg.Contexts) != 1 {
				t.Fatalf("Expected 1 context, got %d", len(cfg.Contexts))
			}
			ctx := cfg.Contexts[0]
			if ctx.Name != tt.wantContext {
				t.Errorf("Context name = %q, want %q", ctx.Name, tt.wantContext)
			}

			// Template uses env-var syntax; runtime expansion is handled by NewClientFromConfig.
			if ctx.Context.Environment == "" {
				t.Error("Environment must not be empty")
			}

			if ctx.Context.TokenRef != "my-token" {
				t.Errorf("TokenRef = %q, want my-token", ctx.Context.TokenRef)
			}
			if ctx.Context.SafetyLevel != config.SafetyLevelReadWriteAll {
				t.Errorf("SafetyLevel = %q, want %q", ctx.Context.SafetyLevel, config.SafetyLevelReadWriteAll)
			}

			// No inline tokens — local configs reject them.
			if len(cfg.Tokens) != 0 {
				t.Errorf("Template must not include inline tokens, got %d", len(cfg.Tokens))
			}

			if cfg.Preferences.Output != "table" {
				t.Errorf("Preferences.Output = %q, want table", cfg.Preferences.Output)
			}
		})
	}
}

func TestConfigInitCmd_Integration(t *testing.T) {
	tmpDir := t.TempDir()
	origDir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	defer os.Chdir(origDir)

	if err := os.Chdir(tmpDir); err != nil {
		t.Fatal(err)
	}

	template := createLocalConfigTemplate("test-env")
	data, err := yaml.Marshal(template)
	if err != nil {
		t.Fatalf("Failed to marshal template: %v", err)
	}

	if err := os.WriteFile(config.LocalConfigName, data, 0600); err != nil {
		t.Fatalf("Failed to write config: %v", err)
	}

	// Load via the explicit (trusted) path — the template uses ${DT_ENVIRONMENT_URL},
	// which is expanded when loading from a trusted path.
	t.Setenv("DT_ENVIRONMENT_URL", "https://test.dynatrace.com")
	cfg, err := config.LoadFrom(filepath.Join(tmpDir, config.LocalConfigName))
	if err != nil {
		t.Fatalf("Failed to load config: %v", err)
	}

	if len(cfg.Contexts) != 1 {
		t.Fatalf("Expected 1 context, got %d", len(cfg.Contexts))
	}

	env := cfg.Contexts[0].Context.Environment
	if env == "" {
		t.Error("Environment must not be empty after load")
	}
	// The placeholder must not contain unresolved env-var references.
	if env == "${DT_ENVIRONMENT_URL}" || env == "$DT_ENVIRONMENT_URL" {
		t.Errorf("Environment = %q: unexpanded env-var reference in template", env)
	}

	// No tokens section in the template.
	if len(cfg.Tokens) != 0 {
		t.Errorf("Expected no inline tokens, got %d", len(cfg.Tokens))
	}
}
