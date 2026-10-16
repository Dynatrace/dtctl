package cmd

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/adrg/xdg"

	"github.com/dynatrace-oss/dtctl/pkg/config"
)

// setupCtxTestConfig creates a temp config with contexts for testing.
func setupCtxTestConfig(t *testing.T) string {
	t.Helper()

	tmpDir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", tmpDir)
	xdg.Reload()
	t.Cleanup(func() { xdg.Reload() })

	cfg := config.NewConfig()
	cfg.SetContext("dev", "https://dev.example.com", "dev-token")
	cfg.SetContext("prod", "https://prod.example.com", "prod-token")
	cfg.CurrentContext = "dev"

	if err := cfg.Save(); err != nil {
		t.Fatalf("failed to save test config: %v", err)
	}

	return tmpDir
}

func TestCtxListContexts(t *testing.T) {
	setupCtxTestConfig(t)

	// Save and restore cfgFile
	originalCfgFile := cfgFile(context.Background())
	defer func() { gFlags.cfgFile = originalCfgFile }()
	gFlags.cfgFile = ""

	err := ctxCmd.RunE(ctxCmd, []string{})
	if err != nil {
		t.Fatalf("ctx (list) failed: %v", err)
	}
}

func TestCtxSwitchContext(t *testing.T) {
	setupCtxTestConfig(t)

	originalCfgFile := cfgFile(context.Background())
	defer func() { gFlags.cfgFile = originalCfgFile }()
	gFlags.cfgFile = ""

	// Switch to prod
	err := ctxCmd.RunE(ctxCmd, []string{"prod"})
	if err != nil {
		t.Fatalf("ctx prod failed: %v", err)
	}

	// Verify switch happened
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("failed to load config: %v", err)
	}
	if cfg.CurrentContext != "prod" {
		t.Errorf("expected current context 'prod', got %q", cfg.CurrentContext)
	}
}

func TestCtxSwitchNonExistent(t *testing.T) {
	setupCtxTestConfig(t)

	originalCfgFile := cfgFile(context.Background())
	defer func() { gFlags.cfgFile = originalCfgFile }()
	gFlags.cfgFile = ""

	err := ctxCmd.RunE(ctxCmd, []string{"nonexistent"})
	if err == nil {
		t.Fatal("expected error for non-existent context, got nil")
	}
	if !strings.Contains(err.Error(), "not found") {
		t.Errorf("expected error to contain 'not found', got %q", err.Error())
	}
}

func TestCtxCurrentCmd(t *testing.T) {
	setupCtxTestConfig(t)

	originalCfgFile := cfgFile(context.Background())
	defer func() { gFlags.cfgFile = originalCfgFile }()
	gFlags.cfgFile = ""

	err := ctxCurrentCmd.RunE(ctxCurrentCmd, []string{})
	if err != nil {
		t.Fatalf("ctx current failed: %v", err)
	}
}

func TestCtxDescribeCmd(t *testing.T) {
	setupCtxTestConfig(t)

	originalCfgFile := cfgFile(context.Background())
	defer func() { gFlags.cfgFile = originalCfgFile }()
	gFlags.cfgFile = ""

	t.Run("describe existing context", func(t *testing.T) {
		err := ctxDescribeCmd.RunE(ctxDescribeCmd, []string{"dev"})
		if err != nil {
			t.Fatalf("ctx describe dev failed: %v", err)
		}
	})

	t.Run("describe non-existent context", func(t *testing.T) {
		err := ctxDescribeCmd.RunE(ctxDescribeCmd, []string{"nonexistent"})
		if err == nil {
			t.Fatal("expected error for non-existent context, got nil")
		}
		if !strings.Contains(err.Error(), "not found") {
			t.Errorf("expected error to contain 'not found', got %q", err.Error())
		}
	})
}

func TestCtxSetCmd(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config")

	originalCfgFile := cfgFile(context.Background())
	defer func() { gFlags.cfgFile = originalCfgFile }()
	gFlags.cfgFile = configPath

	t.Run("create new context", func(t *testing.T) {
		_ = ctxSetCmd.Flags().Set("environment", "https://staging.example.com")
		_ = ctxSetCmd.Flags().Set("token-ref", "staging-token")
		_ = ctxSetCmd.Flags().Set("safety-level", "readonly")
		_ = ctxSetCmd.Flags().Set("description", "Staging environment")
		defer func() {
			clearFlag(ctxSetCmd, "environment")
			_ = ctxSetCmd.Flags().Set("token-ref", "")
			_ = ctxSetCmd.Flags().Set("safety-level", "")
			_ = ctxSetCmd.Flags().Set("description", "")
		}()

		err := ctxSetCmd.RunE(ctxSetCmd, []string{"staging"})
		if err != nil {
			t.Fatalf("ctx set staging failed: %v", err)
		}

		// Verify context was created and is now current
		cfg, err := config.LoadFrom(configPath)
		if err != nil {
			t.Fatalf("failed to load config: %v", err)
		}
		if len(cfg.Contexts) != 1 {
			t.Fatalf("expected 1 context, got %d", len(cfg.Contexts))
		}
		if cfg.Contexts[0].Name != "staging" {
			t.Errorf("expected context 'staging', got %q", cfg.Contexts[0].Name)
		}
		if cfg.Contexts[0].Context.Environment != "https://staging.example.com" {
			t.Errorf("expected environment 'https://staging.example.com', got %q", cfg.Contexts[0].Context.Environment)
		}
		if cfg.Contexts[0].Context.SafetyLevel != config.SafetyLevelReadOnly {
			t.Errorf("expected safety level 'readonly', got %q", cfg.Contexts[0].Context.SafetyLevel)
		}
		if cfg.CurrentContext != "staging" {
			t.Errorf("expected current context 'staging', got %q", cfg.CurrentContext)
		}
	})

	t.Run("update existing context activates it", func(t *testing.T) {
		// Create two contexts with staging as current
		initCfg := config.NewConfig()
		initCfg.SetContext("staging", "https://staging.example.com", "staging-token")
		initCfg.SetContext("prod", "https://prod.example.com", "prod-token")
		initCfg.CurrentContext = "staging"
		if err := initCfg.SaveTo(configPath); err != nil {
			t.Fatalf("failed to save initial config: %v", err)
		}

		// Update prod (not the current context) — this should switch to it
		_ = ctxSetCmd.Flags().Set("environment", "https://prod.example.com")
		defer func() { clearFlag(ctxSetCmd, "environment") }()

		err := ctxSetCmd.RunE(ctxSetCmd, []string{"prod"})
		if err != nil {
			t.Fatalf("ctx set prod failed: %v", err)
		}

		cfg, err := config.LoadFrom(configPath)
		if err != nil {
			t.Fatalf("failed to load config: %v", err)
		}
		if cfg.CurrentContext != "prod" {
			t.Errorf("expected current context 'prod' after update, got %q", cfg.CurrentContext)
		}
	})

	t.Run("create without environment fails", func(t *testing.T) {
		clearFlag(ctxSetCmd, "environment")
		_ = ctxSetCmd.Flags().Set("token-ref", "")
		_ = ctxSetCmd.Flags().Set("safety-level", "")
		_ = ctxSetCmd.Flags().Set("description", "")

		// Clean config file to ensure no existing context
		cleanCfg := config.NewConfig()
		if err := cleanCfg.SaveTo(configPath); err != nil {
			t.Fatalf("failed to save clean config: %v", err)
		}

		err := ctxSetCmd.RunE(ctxSetCmd, []string{"new-ctx"})
		if err == nil {
			t.Fatal("expected error when creating context without environment")
		}
		if !strings.Contains(err.Error(), "--environment") {
			t.Errorf("expected error to mention --environment, got %q", err.Error())
		}
	})

	t.Run("invalid safety level", func(t *testing.T) {
		_ = ctxSetCmd.Flags().Set("environment", "https://test.example.com")
		_ = ctxSetCmd.Flags().Set("safety-level", "invalid-level")
		defer func() {
			clearFlag(ctxSetCmd, "environment")
			_ = ctxSetCmd.Flags().Set("safety-level", "")
		}()

		err := ctxSetCmd.RunE(ctxSetCmd, []string{"bad-ctx"})
		if err == nil {
			t.Fatal("expected error for invalid safety level")
		}
		if !strings.Contains(err.Error(), "invalid safety level") {
			t.Errorf("expected error about invalid safety level, got %q", err.Error())
		}
	})
}

func TestCtxDeleteCmd(t *testing.T) {
	setupCtxTestConfig(t)

	originalCfgFile := cfgFile(context.Background())
	defer func() { gFlags.cfgFile = originalCfgFile }()
	gFlags.cfgFile = ""

	t.Run("delete non-current context", func(t *testing.T) {
		err := ctxDeleteCmd.RunE(ctxDeleteCmd, []string{"prod"})
		if err != nil {
			t.Fatalf("ctx delete prod failed: %v", err)
		}

		cfg, err := config.Load()
		if err != nil {
			t.Fatalf("failed to load config: %v", err)
		}
		if len(cfg.Contexts) != 1 {
			t.Errorf("expected 1 context, got %d", len(cfg.Contexts))
		}
		// Current should still be dev
		if cfg.CurrentContext != "dev" {
			t.Errorf("expected current context 'dev', got %q", cfg.CurrentContext)
		}
	})

	t.Run("delete current context clears current-context", func(t *testing.T) {
		err := ctxDeleteCmd.RunE(ctxDeleteCmd, []string{"dev"})
		if err != nil {
			t.Fatalf("ctx delete dev failed: %v", err)
		}

		cfg, err := config.Load()
		if err != nil {
			t.Fatalf("failed to load config: %v", err)
		}
		if cfg.CurrentContext != "" {
			t.Errorf("expected current context to be cleared, got %q", cfg.CurrentContext)
		}
	})

	t.Run("delete non-existent context", func(t *testing.T) {
		err := ctxDeleteCmd.RunE(ctxDeleteCmd, []string{"nonexistent"})
		if err == nil {
			t.Fatal("expected error for non-existent context")
		}
		if !strings.Contains(err.Error(), "not found") {
			t.Errorf("expected error to contain 'not found', got %q", err.Error())
		}
	})
}

func TestCtxTokenCmd(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", tmpDir)
	xdg.Reload()
	t.Cleanup(func() { xdg.Reload() })

	// Build a config with two contexts and inline token values.
	cfg := config.NewConfig()
	cfg.SetContext("dev", "https://dev.example.com", "dev-token")
	cfg.SetContext("prod", "https://prod.example.com", "prod-token")
	cfg.CurrentContext = "dev"
	if err := cfg.SetToken("dev-token", "dt0c01.DEV_TOKEN_VALUE"); err != nil {
		t.Fatalf("SetToken dev-token: %v", err)
	}
	if err := cfg.SetToken("prod-token", "dt0c01.PROD_TOKEN_VALUE"); err != nil {
		t.Fatalf("SetToken prod-token: %v", err)
	}
	if err := cfg.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}

	originalCfgFile := cfgFile(context.Background())
	defer func() { gFlags.cfgFile = originalCfgFile }()
	gFlags.cfgFile = ""

	captureStdout := func(t *testing.T, fn func()) string {
		t.Helper()
		r, w, err := os.Pipe()
		if err != nil {
			t.Fatalf("os.Pipe: %v", err)
		}
		origStdout := os.Stdout
		os.Stdout = w
		fn()
		_ = w.Close()
		os.Stdout = origStdout
		data, err := io.ReadAll(r)
		if err != nil {
			t.Fatalf("ReadAll: %v", err)
		}
		return strings.TrimSpace(string(data))
	}

	t.Run("implicit current context prints dev token", func(t *testing.T) {
		got := captureStdout(t, func() {
			if err := ctxTokenCmd.RunE(ctxTokenCmd, []string{}); err != nil {
				t.Fatalf("ctxTokenCmd: %v", err)
			}
		})
		if got != "dt0c01.DEV_TOKEN_VALUE" {
			t.Errorf("got %q, want %q", got, "dt0c01.DEV_TOKEN_VALUE")
		}
	})

	t.Run("explicit context name prints that context's token", func(t *testing.T) {
		got := captureStdout(t, func() {
			if err := ctxTokenCmd.RunE(ctxTokenCmd, []string{"prod"}); err != nil {
				t.Fatalf("ctxTokenCmd prod: %v", err)
			}
		})
		if got != "dt0c01.PROD_TOKEN_VALUE" {
			t.Errorf("got %q, want %q", got, "dt0c01.PROD_TOKEN_VALUE")
		}
	})

	t.Run("non-existent context returns error", func(t *testing.T) {
		err := ctxTokenCmd.RunE(ctxTokenCmd, []string{"nonexistent"})
		if err == nil {
			t.Fatal("expected error for non-existent context, got nil")
		}
		if !strings.Contains(err.Error(), "not found") {
			t.Errorf("expected 'not found' in error, got %q", err.Error())
		}
	})
}

func TestCtxWithCustomConfigPath(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "custom-config.yaml")

	originalCfgFile := cfgFile(context.Background())
	defer func() { gFlags.cfgFile = originalCfgFile }()
	gFlags.cfgFile = configPath

	// Create config using ctx set
	_ = ctxSetCmd.Flags().Set("environment", "https://test.example.com")
	_ = ctxSetCmd.Flags().Set("token-ref", "test-token")
	defer func() {
		clearFlag(ctxSetCmd, "environment")
		_ = ctxSetCmd.Flags().Set("token-ref", "")
	}()

	if err := ctxSetCmd.RunE(ctxSetCmd, []string{"test"}); err != nil {
		t.Fatalf("ctx set failed: %v", err)
	}

	// Verify it was written to custom path
	if _, err := os.Stat(configPath); os.IsNotExist(err) {
		t.Errorf("config file was NOT created at %s", configPath)
	}

	cfg, err := config.LoadFrom(configPath)
	if err != nil {
		t.Fatalf("failed to load config from custom path: %v", err)
	}
	if len(cfg.Contexts) != 1 || cfg.Contexts[0].Name != "test" {
		t.Errorf("expected context 'test' in custom config, got %+v", cfg.Contexts)
	}

	// Switch using ctx
	_ = ctxSetCmd.Flags().Set("environment", "https://other.example.com")
	if err := ctxSetCmd.RunE(ctxSetCmd, []string{"other"}); err != nil {
		t.Fatalf("ctx set other failed: %v", err)
	}

	if err := ctxCmd.RunE(ctxCmd, []string{"test"}); err != nil {
		t.Fatalf("ctx switch failed: %v", err)
	}

	cfg, err = config.LoadFrom(configPath)
	if err != nil {
		t.Fatalf("failed to reload config: %v", err)
	}
	if cfg.CurrentContext != "test" {
		t.Errorf("expected current context 'test', got %q", cfg.CurrentContext)
	}
}
