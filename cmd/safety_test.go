package cmd

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/viper"

	"github.com/dynatrace-oss/dtctl/pkg/config"
	"github.com/dynatrace-oss/dtctl/pkg/safety"
)

// TestNewSafetyChecker tests the NewSafetyChecker function
func TestNewSafetyChecker(t *testing.T) {
	tests := []struct {
		name            string
		safetyLevel     config.SafetyLevel
		wantSafetyLevel config.SafetyLevel
	}{
		{
			name:            "readonly context",
			safetyLevel:     config.SafetyLevelReadOnly,
			wantSafetyLevel: config.SafetyLevelReadOnly,
		},
		{
			name:            "readwrite-all context",
			safetyLevel:     config.SafetyLevelReadWriteAll,
			wantSafetyLevel: config.SafetyLevelReadWriteAll,
		},
		{
			name:            "dangerously-unrestricted",
			safetyLevel:     config.SafetyLevelDangerouslyUnrestricted,
			wantSafetyLevel: config.SafetyLevelDangerouslyUnrestricted,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Setup test config
			tmpDir := t.TempDir()
			configPath := filepath.Join(tmpDir, "config.yaml")

			cfg := config.NewConfig()
			cfg.SetContextWithOptions("test", "https://test.dt.com", "test-token", &config.ContextOptions{
				SafetyLevel: tt.safetyLevel,
			})
			cfg.CurrentContext = "test"

			if err := cfg.SaveTo(configPath); err != nil {
				t.Fatalf("failed to save config: %v", err)
			}

			// Setup cmd state
			origCfgFile := cfgFile(context.Background())
			defer func() {
				gFlags.cfgFile = origCfgFile
			}()

			viper.Reset()
			gFlags.cfgFile = configPath

			// Load config
			loadedCfg, err := LoadConfig()
			if err != nil {
				t.Fatalf("LoadConfig() error = %v", err)
			}

			// Create safety checker
			checker, err := NewSafetyChecker(loadedCfg)
			if err != nil {
				t.Fatalf("NewSafetyChecker() error = %v", err)
			}

			if checker.SafetyLevel() != tt.wantSafetyLevel {
				t.Errorf("SafetyLevel() = %v, want %v", checker.SafetyLevel(), tt.wantSafetyLevel)
			}
		})
	}
}

// TestSafetyChecker_ReadonlyBlocksOperations tests that readonly context blocks mutating operations
func TestSafetyChecker_ReadonlyBlocksOperations(t *testing.T) {
	// Setup test config with readonly context
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config.yaml")

	cfg := config.NewConfig()
	cfg.SetContextWithOptions("readonly-prod", "https://prod.dt.com", "prod-token", &config.ContextOptions{
		SafetyLevel: config.SafetyLevelReadOnly,
	})
	cfg.CurrentContext = "readonly-prod"

	if err := cfg.SaveTo(configPath); err != nil {
		t.Fatalf("failed to save config: %v", err)
	}

	// Setup cmd state
	origCfgFile := cfgFile(context.Background())
	defer func() {
		gFlags.cfgFile = origCfgFile
	}()

	viper.Reset()
	gFlags.cfgFile = configPath

	loadedCfg, err := LoadConfig()
	if err != nil {
		t.Fatalf("LoadConfig() error = %v", err)
	}

	checker, err := NewSafetyChecker(loadedCfg)
	if err != nil {
		t.Fatalf("NewSafetyChecker() error = %v", err)
	}

	// Test all mutating operations are blocked
	mutatingOperations := []struct {
		op   safety.Operation
		name string
	}{
		{safety.OperationCreate, "create"},
		{safety.OperationUpdate, "update"},
		{safety.OperationDelete, "delete"},
		{safety.OperationDeleteBucket, "delete-bucket"},
	}

	for _, tt := range mutatingOperations {
		t.Run(tt.name, func(t *testing.T) {
			err := checker.CheckError(tt.op, safety.OwnershipUnknown)
			if err == nil {
				t.Errorf("CheckError(%s) should return error in readonly context", tt.op)
			}
			if !strings.Contains(err.Error(), "readonly-prod") {
				t.Errorf("Error should mention context name, got: %s", err.Error())
			}
			if !strings.Contains(err.Error(), "readonly") {
				t.Errorf("Error should mention safety level, got: %s", err.Error())
			}
		})
	}

	// Read operations should be allowed
	t.Run("read allowed", func(t *testing.T) {
		err := checker.CheckError(safety.OperationRead, safety.OwnershipUnknown)
		if err != nil {
			t.Errorf("CheckError(read) should not return error in readonly context, got: %v", err)
		}
	})
}

// TestSafetyChecker_ReadWriteMineBlocksSharedAndUnknown tests readwrite-mine blocks shared and unknown ownership
func TestSafetyChecker_ReadWriteMineBlocksSharedAndUnknown(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config.yaml")

	cfg := config.NewConfig()
	cfg.SetContextWithOptions("dev", "https://dev.dt.com", "dev-token", &config.ContextOptions{
		SafetyLevel: config.SafetyLevelReadWriteMine,
	})
	cfg.CurrentContext = "dev"

	if err := cfg.SaveTo(configPath); err != nil {
		t.Fatalf("failed to save config: %v", err)
	}

	origCfgFile := cfgFile(context.Background())
	defer func() {
		gFlags.cfgFile = origCfgFile
	}()

	viper.Reset()
	gFlags.cfgFile = configPath

	loadedCfg, err := LoadConfig()
	if err != nil {
		t.Fatalf("LoadConfig() error = %v", err)
	}

	checker, err := NewSafetyChecker(loadedCfg)
	if err != nil {
		t.Fatalf("NewSafetyChecker() error = %v", err)
	}

	// Create should be allowed (ownership doesn't matter for create)
	t.Run("create allowed", func(t *testing.T) {
		err := checker.CheckError(safety.OperationCreate, safety.OwnershipUnknown)
		if err != nil {
			t.Errorf("Create should be allowed, got: %v", err)
		}
	})

	// Update own should be allowed
	t.Run("update own allowed", func(t *testing.T) {
		err := checker.CheckError(safety.OperationUpdate, safety.OwnershipOwn)
		if err != nil {
			t.Errorf("Update own should be allowed, got: %v", err)
		}
	})

	// Update unknown should be blocked (safer default - assume shared)
	t.Run("update unknown blocked", func(t *testing.T) {
		err := checker.CheckError(safety.OperationUpdate, safety.OwnershipUnknown)
		if err == nil {
			t.Error("Update with unknown ownership should be blocked")
		}
	})

	// Update shared should be blocked
	t.Run("update shared blocked", func(t *testing.T) {
		err := checker.CheckError(safety.OperationUpdate, safety.OwnershipShared)
		if err == nil {
			t.Error("Update shared should be blocked")
		}
	})

	// Delete shared should be blocked
	t.Run("delete shared blocked", func(t *testing.T) {
		err := checker.CheckError(safety.OperationDelete, safety.OwnershipShared)
		if err == nil {
			t.Error("Delete shared should be blocked")
		}
	})

	// Delete bucket should always be blocked
	t.Run("delete bucket blocked", func(t *testing.T) {
		err := checker.CheckError(safety.OperationDeleteBucket, safety.OwnershipUnknown)
		if err == nil {
			t.Error("Delete bucket should be blocked in readwrite-mine")
		}
	})
}

// TestSafetyChecker_ReadWriteAllBlocksBucket tests readwrite-all only blocks bucket deletion
func TestSafetyChecker_ReadWriteAllBlocksBucket(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config.yaml")

	cfg := config.NewConfig()
	cfg.SetContextWithOptions("staging", "https://staging.dt.com", "staging-token", &config.ContextOptions{
		SafetyLevel: config.SafetyLevelReadWriteAll,
	})
	cfg.CurrentContext = "staging"

	if err := cfg.SaveTo(configPath); err != nil {
		t.Fatalf("failed to save config: %v", err)
	}

	origCfgFile := cfgFile(context.Background())
	defer func() {
		gFlags.cfgFile = origCfgFile
	}()

	viper.Reset()
	gFlags.cfgFile = configPath

	loadedCfg, err := LoadConfig()
	if err != nil {
		t.Fatalf("LoadConfig() error = %v", err)
	}

	checker, err := NewSafetyChecker(loadedCfg)
	if err != nil {
		t.Fatalf("NewSafetyChecker() error = %v", err)
	}

	// All operations except bucket delete should be allowed
	allowedOps := []struct {
		op        safety.Operation
		ownership safety.ResourceOwnership
		name      string
	}{
		{safety.OperationRead, safety.OwnershipUnknown, "read"},
		{safety.OperationCreate, safety.OwnershipUnknown, "create"},
		{safety.OperationUpdate, safety.OwnershipOwn, "update own"},
		{safety.OperationUpdate, safety.OwnershipShared, "update shared"},
		{safety.OperationDelete, safety.OwnershipOwn, "delete own"},
		{safety.OperationDelete, safety.OwnershipShared, "delete shared"},
	}

	for _, tt := range allowedOps {
		t.Run(tt.name+" allowed", func(t *testing.T) {
			err := checker.CheckError(tt.op, tt.ownership)
			if err != nil {
				t.Errorf("%s should be allowed, got: %v", tt.name, err)
			}
		})
	}

	// Only bucket delete should be blocked
	t.Run("delete bucket blocked", func(t *testing.T) {
		err := checker.CheckError(safety.OperationDeleteBucket, safety.OwnershipUnknown)
		if err == nil {
			t.Error("Delete bucket should be blocked in readwrite-all")
		}
		if !strings.Contains(err.Error(), "dangerously-unrestricted") {
			t.Errorf("Error should suggest dangerously-unrestricted, got: %s", err.Error())
		}
	})
}

// TestSafetyChecker_DangerouslyUnrestrictedAllowsAll tests unrestricted allows everything
func TestSafetyChecker_DangerouslyUnrestrictedAllowsAll(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config.yaml")

	cfg := config.NewConfig()
	cfg.SetContextWithOptions("dev-full", "https://dev.dt.com", "dev-token", &config.ContextOptions{
		SafetyLevel: config.SafetyLevelDangerouslyUnrestricted,
	})
	cfg.CurrentContext = "dev-full"

	if err := cfg.SaveTo(configPath); err != nil {
		t.Fatalf("failed to save config: %v", err)
	}

	origCfgFile := cfgFile(context.Background())
	defer func() {
		gFlags.cfgFile = origCfgFile
	}()

	viper.Reset()
	gFlags.cfgFile = configPath

	loadedCfg, err := LoadConfig()
	if err != nil {
		t.Fatalf("LoadConfig() error = %v", err)
	}

	checker, err := NewSafetyChecker(loadedCfg)
	if err != nil {
		t.Fatalf("NewSafetyChecker() error = %v", err)
	}

	// All operations should be allowed including bucket delete
	allOps := []struct {
		op        safety.Operation
		ownership safety.ResourceOwnership
		name      string
	}{
		{safety.OperationRead, safety.OwnershipUnknown, "read"},
		{safety.OperationCreate, safety.OwnershipUnknown, "create"},
		{safety.OperationUpdate, safety.OwnershipOwn, "update own"},
		{safety.OperationUpdate, safety.OwnershipShared, "update shared"},
		{safety.OperationDelete, safety.OwnershipOwn, "delete own"},
		{safety.OperationDelete, safety.OwnershipShared, "delete shared"},
		{safety.OperationDeleteBucket, safety.OwnershipUnknown, "delete bucket"},
	}

	for _, tt := range allOps {
		t.Run(tt.name, func(t *testing.T) {
			err := checker.CheckError(tt.op, tt.ownership)
			if err != nil {
				t.Errorf("%s should be allowed in dangerously-unrestricted, got: %v", tt.name, err)
			}
		})
	}
}

// TestDefaultSafetyLevel tests that default safety level is applied correctly
func TestDefaultSafetyLevel(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config.yaml")

	// Create config WITHOUT explicit safety level
	cfg := config.NewConfig()
	cfg.SetContext("test", "https://test.dt.com", "test-token")
	cfg.CurrentContext = "test"
	// Note: Not setting SafetyLevel - should default

	if err := cfg.SaveTo(configPath); err != nil {
		t.Fatalf("failed to save config: %v", err)
	}

	origCfgFile := cfgFile(context.Background())
	defer func() {
		gFlags.cfgFile = origCfgFile
	}()

	viper.Reset()
	gFlags.cfgFile = configPath

	loadedCfg, err := LoadConfig()
	if err != nil {
		t.Fatalf("LoadConfig() error = %v", err)
	}

	checker, err := NewSafetyChecker(loadedCfg)
	if err != nil {
		t.Fatalf("NewSafetyChecker() error = %v", err)
	}

	// Default should be readwrite-all
	if checker.SafetyLevel() != config.SafetyLevelReadWriteAll {
		t.Errorf("Default safety level should be readwrite-all, got: %s", checker.SafetyLevel())
	}

	// Verify default behavior: create/update/delete allowed, bucket delete blocked
	t.Run("create allowed with default", func(t *testing.T) {
		if err := checker.CheckError(safety.OperationCreate, safety.OwnershipUnknown); err != nil {
			t.Errorf("Create should be allowed with default level, got: %v", err)
		}
	})

	t.Run("delete bucket blocked with default", func(t *testing.T) {
		if err := checker.CheckError(safety.OperationDeleteBucket, safety.OwnershipUnknown); err == nil {
			t.Error("Delete bucket should be blocked with default level")
		}
	})
}

// TestSafetyErrorMessages verifies error messages are helpful
func TestSafetyErrorMessages(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config.yaml")

	cfg := config.NewConfig()
	cfg.SetContextWithOptions("production", "https://prod.dt.com", "prod-token", &config.ContextOptions{
		SafetyLevel: config.SafetyLevelReadOnly,
	})
	cfg.CurrentContext = "production"

	if err := cfg.SaveTo(configPath); err != nil {
		t.Fatalf("failed to save config: %v", err)
	}

	origCfgFile := cfgFile(context.Background())
	defer func() {
		gFlags.cfgFile = origCfgFile
	}()

	viper.Reset()
	gFlags.cfgFile = configPath

	loadedCfg, err := LoadConfig()
	if err != nil {
		t.Fatalf("LoadConfig() error = %v", err)
	}

	checker, err := NewSafetyChecker(loadedCfg)
	if err != nil {
		t.Fatalf("NewSafetyChecker() error = %v", err)
	}

	err = checker.CheckError(safety.OperationDelete, safety.OwnershipUnknown)
	if err == nil {
		t.Fatal("Expected error for delete in readonly context")
	}

	errStr := err.Error()

	// Verify error contains helpful information
	requiredParts := []string{
		"production", // Context name
		"readonly",   // Safety level
	}

	for _, part := range requiredParts {
		if !strings.Contains(errStr, part) {
			t.Errorf("Error message should contain %q, got: %s", part, errStr)
		}
	}
}
