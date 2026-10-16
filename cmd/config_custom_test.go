package cmd

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/adrg/xdg"
	"github.com/spf13/viper"

	"github.com/dynatrace-oss/dtctl/pkg/config"
)

func TestConfigFlagRespected(t *testing.T) {
	// 1. Setup separate directories for "default" config and "custom" config
	tmpDir := t.TempDir()
	defaultConfigDir := filepath.Join(tmpDir, "default")
	if err := os.MkdirAll(defaultConfigDir, 0700); err != nil {
		t.Fatalf("failed to create default config dir: %v", err)
	}

	customConfigFile := filepath.Join(tmpDir, "custom", "custom-config.yaml")
	if err := os.MkdirAll(filepath.Dir(customConfigFile), 0700); err != nil {
		t.Fatalf("failed to create custom config dir: %v", err)
	}

	// Mock XDG_CONFIG_HOME to point to our temp default dir
	// This ensures valid Load() calls would go here if --config is ignored
	t.Setenv("XDG_CONFIG_HOME", defaultConfigDir)
	xdg.Reload()
	defer xdg.Reload()

	// Save original cfgFile value and restore after test
	originalCfgFile := cfgFile(context.Background())
	defer func() { gFlags.cfgFile = originalCfgFile }()

	// 2. Set the global cfgFile variable (simulating --config flag)
	gFlags.cfgFile = customConfigFile

	// 3. Run config set-context command
	// validation: should create file at customConfigFile
	args := []string{"test-ctx"}
	cmd := configSetContextCmd

	// Reset flags to avoid interference
	if err := cmd.Flags().Set("environment", "https://example.com"); err != nil {
		t.Fatalf("failed to set environment flag: %v", err)
	}
	if err := cmd.Flags().Set("token-ref", "my-token"); err != nil {
		t.Fatalf("failed to set token-ref flag: %v", err)
	}
	if err := cmd.Flags().Set("safety-level", "readonly"); err != nil {
		t.Fatalf("failed to set safety-level flag: %v", err)
	}

	err := cmd.RunE(cmd, args)
	if err != nil {
		t.Fatalf("Command failed: %v", err)
	}

	// 4. Verify custom config file was created
	if _, err := os.Stat(customConfigFile); os.IsNotExist(err) {
		t.Errorf("Custom config file was NOT created at %s", customConfigFile)
	}

	// 5. Verify default config was NOT created/touched
	defaultConfigPath := filepath.Join(defaultConfigDir, "dtctl", "config")
	if _, err := os.Stat(defaultConfigPath); err == nil {
		t.Errorf("Default config file SHOULD NOT exist at %s", defaultConfigPath)
	}

	// 6. Verify content of custom config
	cfg, err := config.LoadFrom(customConfigFile)
	if err != nil {
		t.Fatalf("Failed to load custom config: %v", err)
	}

	if cfg.CurrentContext != "test-ctx" {
		t.Errorf("Expected current-context 'test-ctx', got '%s'", cfg.CurrentContext)
	}

	// 7. Verify we can read it back using view command
	// Reset Viper to ensure it doesn't hold old state
	viper.Reset()

	// Capture stdout
	// (Simulated by just running the command and ensuring no error_
	viewErr := configViewCmd.RunE(configViewCmd, []string{})
	if viewErr != nil {
		t.Errorf("View command failed with custom config: %v", viewErr)
	}
}

// TestEnvConfigSaveRoundTrips verifies that when DTCTL_CONFIG names an explicit
// config, a config-mutating command writes back to that same file rather than
// the global config or an auto-discovered local .dtctl.yaml. This guards the
// load/save symmetry: loadConfigRaw reads DTCTL_CONFIG, so saveConfig must
// target it too, otherwise the mutation would silently clobber a different file.
func TestEnvConfigSaveRoundTrips(t *testing.T) {
	tmpDir := t.TempDir()

	// A global config location that must stay untouched.
	defaultConfigDir := filepath.Join(tmpDir, "default")
	if err := os.MkdirAll(defaultConfigDir, 0700); err != nil {
		t.Fatalf("failed to create default config dir: %v", err)
	}
	t.Setenv("XDG_CONFIG_HOME", defaultConfigDir)
	xdg.Reload()
	defer xdg.Reload()

	// The explicit, trusted config named via DTCTL_CONFIG.
	envConfigPath := filepath.Join(tmpDir, "workspace", "ws-config.yaml")
	if err := os.MkdirAll(filepath.Dir(envConfigPath), 0700); err != nil {
		t.Fatalf("failed to create workspace dir: %v", err)
	}
	seed := `apiVersion: v1
kind: Config
current-context: seed-ctx
contexts:
  - name: seed-ctx
    context:
      environment: https://seed.example.com
      token-ref: seed-token
`
	if err := os.WriteFile(envConfigPath, []byte(seed), 0600); err != nil {
		t.Fatalf("failed to write env config: %v", err)
	}

	// Ensure the --config flag is not in play; only DTCTL_CONFIG.
	originalCfgFile := cfgFile(context.Background())
	defer func() { gFlags.cfgFile = originalCfgFile }()
	gFlags.cfgFile = ""

	// Run from an empty dir so no local .dtctl.yaml can be auto-discovered.
	emptyDir := filepath.Join(tmpDir, "empty")
	if err := os.MkdirAll(emptyDir, 0700); err != nil {
		t.Fatalf("failed to create empty dir: %v", err)
	}
	origWd, err := os.Getwd()
	if err != nil {
		t.Fatalf("failed to get working directory: %v", err)
	}
	defer func() { _ = os.Chdir(origWd) }()
	if err := os.Chdir(emptyDir); err != nil {
		t.Fatalf("failed to change directory: %v", err)
	}

	t.Setenv(config.EnvConfig, envConfigPath)
	viper.Reset()
	defer viper.Reset()

	// Mutate the config via set-context.
	cmd := configSetContextCmd
	if err := cmd.Flags().Set("environment", "https://updated.example.com"); err != nil {
		t.Fatalf("failed to set environment flag: %v", err)
	}
	if err := cmd.Flags().Set("token-ref", "new-token"); err != nil {
		t.Fatalf("failed to set token-ref flag: %v", err)
	}
	if err := cmd.Flags().Set("safety-level", "readonly"); err != nil {
		t.Fatalf("failed to set safety-level flag: %v", err)
	}
	if err := cmd.RunE(cmd, []string{"new-ctx"}); err != nil {
		t.Fatalf("set-context failed: %v", err)
	}

	// The mutation must have landed in the DTCTL_CONFIG file, preserving the seed.
	cfg, err := config.LoadFrom(envConfigPath)
	if err != nil {
		t.Fatalf("failed to reload env config: %v", err)
	}
	if _, err := cfg.GetContext("new-ctx"); err != nil {
		t.Error("new-ctx missing from DTCTL_CONFIG file; save did not target it")
	}
	if _, err := cfg.GetContext("seed-ctx"); err != nil {
		t.Error("seed-ctx missing; save clobbered the existing DTCTL_CONFIG content instead of round-tripping")
	}

	// The global config must never have been created.
	globalPath := filepath.Join(defaultConfigDir, "dtctl", "config")
	if _, err := os.Stat(globalPath); err == nil {
		t.Errorf("global config was written at %s; save ignored DTCTL_CONFIG", globalPath)
	}
}

// TestConfigCommandsRespectCustomPath tests that all config commands respect the --config flag
func TestConfigCommandsRespectCustomPath(t *testing.T) {
	tmpDir := t.TempDir()
	customConfigPath := filepath.Join(tmpDir, "custom-config.yaml")

	originalCfgFile := cfgFile(context.Background())
	defer func() { gFlags.cfgFile = originalCfgFile }()
	gFlags.cfgFile = customConfigPath

	t.Run("use-context modifies custom path", func(t *testing.T) {
		// First create two contexts
		_ = configSetContextCmd.Flags().Set("environment", "https://first.example.com")
		_ = configSetContextCmd.Flags().Set("token-ref", "first-token")
		defer func() {
			clearFlag(configSetContextCmd, "environment")
			_ = configSetContextCmd.Flags().Set("token-ref", "")
		}()

		if err := configSetContextCmd.RunE(configSetContextCmd, []string{"first-ctx"}); err != nil {
			t.Fatalf("failed to create first context: %v", err)
		}

		_ = configSetContextCmd.Flags().Set("environment", "https://second.example.com")
		_ = configSetContextCmd.Flags().Set("token-ref", "second-token")

		if err := configSetContextCmd.RunE(configSetContextCmd, []string{"second-ctx"}); err != nil {
			t.Fatalf("failed to create second context: %v", err)
		}

		// Now switch to the second context
		if err := configUseContextCmd.RunE(configUseContextCmd, []string{"second-ctx"}); err != nil {
			t.Fatalf("failed to use-context: %v", err)
		}

		// Verify the change was written to custom path
		cfg, err := config.LoadFrom(customConfigPath)
		if err != nil {
			t.Fatalf("failed to load config: %v", err)
		}

		if cfg.CurrentContext != "second-ctx" {
			t.Errorf("expected current context 'second-ctx', got %q", cfg.CurrentContext)
		}
	})

	t.Run("config set modifies custom path", func(t *testing.T) {
		if err := configSetCmd.RunE(configSetCmd, []string{"preferences.editor", "emacs"}); err != nil {
			t.Fatalf("failed to set preference: %v", err)
		}

		// Verify the change was written to custom path
		cfg, err := config.LoadFrom(customConfigPath)
		if err != nil {
			t.Fatalf("failed to load config: %v", err)
		}

		if cfg.Preferences.Editor != "emacs" {
			t.Errorf("expected editor 'emacs', got %q", cfg.Preferences.Editor)
		}
	})

	t.Run("delete-context modifies custom path", func(t *testing.T) {
		if err := configDeleteContextCmd.RunE(configDeleteContextCmd, []string{"first-ctx"}); err != nil {
			t.Fatalf("failed to delete context: %v", err)
		}

		// Verify the change was written to custom path
		cfg, err := config.LoadFrom(customConfigPath)
		if err != nil {
			t.Fatalf("failed to load config: %v", err)
		}

		// Should only have one context left (second-ctx)
		if len(cfg.Contexts) != 1 {
			t.Errorf("expected 1 context, got %d", len(cfg.Contexts))
		}

		if cfg.Contexts[0].Name != "second-ctx" {
			t.Errorf("expected remaining context 'second-ctx', got %q", cfg.Contexts[0].Name)
		}
	})

	t.Run("get-contexts reads from custom path", func(t *testing.T) {
		// Should not error when reading from custom config
		if err := configGetContextsCmd.RunE(configGetContextsCmd, nil); err != nil {
			t.Fatalf("failed to get-contexts: %v", err)
		}
	})

	t.Run("current-context reads from custom path", func(t *testing.T) {
		if err := configCurrentContextCmd.RunE(configCurrentContextCmd, nil); err != nil {
			t.Fatalf("failed to get current-context: %v", err)
		}
	})

	t.Run("describe-context reads from custom path", func(t *testing.T) {
		if err := configDescribeContextCmd.RunE(configDescribeContextCmd, []string{"second-ctx"}); err != nil {
			t.Fatalf("failed to describe-context: %v", err)
		}
	})
}

// TestConfigMultipleCustomPaths tests isolation between different config files
func TestConfigMultipleCustomPaths(t *testing.T) {
	tmpDir := t.TempDir()
	config1Path := filepath.Join(tmpDir, "config1.yaml")
	config2Path := filepath.Join(tmpDir, "config2.yaml")

	originalCfgFile := cfgFile(context.Background())
	defer func() { gFlags.cfgFile = originalCfgFile }()

	// Create first config
	gFlags.cfgFile = config1Path
	_ = configSetContextCmd.Flags().Set("environment", "https://config1.example.com")
	_ = configSetContextCmd.Flags().Set("token-ref", "config1-token")
	defer func() {
		clearFlag(configSetContextCmd, "environment")
		_ = configSetContextCmd.Flags().Set("token-ref", "")
	}()

	if err := configSetContextCmd.RunE(configSetContextCmd, []string{"config1-ctx"}); err != nil {
		t.Fatalf("failed to create config1: %v", err)
	}

	// Create second config
	gFlags.cfgFile = config2Path
	_ = configSetContextCmd.Flags().Set("environment", "https://config2.example.com")
	_ = configSetContextCmd.Flags().Set("token-ref", "config2-token")

	if err := configSetContextCmd.RunE(configSetContextCmd, []string{"config2-ctx"}); err != nil {
		t.Fatalf("failed to create config2: %v", err)
	}

	// Verify config1 still has its own context
	cfg1, err := config.LoadFrom(config1Path)
	if err != nil {
		t.Fatalf("failed to load config1: %v", err)
	}
	if len(cfg1.Contexts) != 1 {
		t.Errorf("config1: expected 1 context, got %d", len(cfg1.Contexts))
	}
	if cfg1.Contexts[0].Name != "config1-ctx" {
		t.Errorf("config1: expected context 'config1-ctx', got %q", cfg1.Contexts[0].Name)
	}

	// Verify config2 has its own context
	cfg2, err := config.LoadFrom(config2Path)
	if err != nil {
		t.Fatalf("failed to load config2: %v", err)
	}
	if len(cfg2.Contexts) != 1 {
		t.Errorf("config2: expected 1 context, got %d", len(cfg2.Contexts))
	}
	if cfg2.Contexts[0].Name != "config2-ctx" {
		t.Errorf("config2: expected context 'config2-ctx', got %q", cfg2.Contexts[0].Name)
	}
}

// TestConfigSetCredentialsWithCustomPath tests set-credentials respects --config
func TestConfigSetCredentialsWithCustomPath(t *testing.T) {
	tmpDir := t.TempDir()
	customConfigPath := filepath.Join(tmpDir, "custom-config.yaml")

	originalCfgFile := cfgFile(context.Background())
	defer func() { gFlags.cfgFile = originalCfgFile }()
	gFlags.cfgFile = customConfigPath

	// Create initial config
	_ = configSetContextCmd.Flags().Set("environment", "https://test.example.com")
	_ = configSetContextCmd.Flags().Set("token-ref", "test-token")
	defer func() {
		clearFlag(configSetContextCmd, "environment")
		_ = configSetContextCmd.Flags().Set("token-ref", "")
	}()

	if err := configSetContextCmd.RunE(configSetContextCmd, []string{"test-ctx"}); err != nil {
		t.Fatalf("failed to create context: %v", err)
	}

	// Set credentials
	_ = configSetCredentialsCmd.Flags().Set("token", "secret-token-value")
	defer func() {
		_ = configSetCredentialsCmd.Flags().Set("token", "")
	}()

	if err := configSetCredentialsCmd.RunE(configSetCredentialsCmd, []string{"test-token"}); err != nil {
		t.Fatalf("failed to set credentials: %v", err)
	}

	// Verify credentials were saved to custom config
	cfg, err := config.LoadFrom(customConfigPath)
	if err != nil {
		t.Fatalf("failed to load config: %v", err)
	}

	// Check if token is in config (if keyring is not available) or referenced
	found := false
	for _, nt := range cfg.Tokens {
		if nt.Name == "test-token" {
			found = true
			break
		}
	}

	if !found {
		t.Error("token reference not found in custom config")
	}
}
