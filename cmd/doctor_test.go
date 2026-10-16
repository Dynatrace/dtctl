package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/dynatrace-oss/dtctl/pkg/config"
)

func TestDoctorCheckVersionAlwaysPasses(t *testing.T) {
	results := runDoctorChecks(context.Background())
	if len(results) == 0 {
		t.Fatal("expected at least one result (version check)")
	}
	if results[0].Name != "dtctl version" {
		t.Errorf("expected first check to be 'dtctl version', got %q", results[0].Name)
	}
	if results[0].Status != "ok" {
		t.Errorf("expected version check to pass, got status %q", results[0].Status)
	}
}

func TestDoctorNoConfig(t *testing.T) {
	tmpDir := t.TempDir()

	originalCfgFile := cfgFile(context.Background())
	defer func() { gFlags.cfgFile = originalCfgFile }()
	// Point to a non-existent config
	gFlags.cfgFile = filepath.Join(tmpDir, "nonexistent", "config")

	results := runDoctorChecks(context.Background())

	// Should have version (ok) and config (fail), then stop
	if len(results) < 2 {
		t.Fatalf("expected at least 2 results, got %d", len(results))
	}
	if results[1].Name != "Configuration" {
		t.Errorf("expected second check to be 'Configuration', got %q", results[1].Name)
	}
	if results[1].Status != "fail" {
		t.Errorf("expected config check to fail, got %q", results[1].Status)
	}
	// Should stop after config failure
	if len(results) > 2 {
		t.Errorf("expected checks to stop after config failure, got %d results", len(results))
	}
}

func TestDoctorNoCurrentContext(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config")

	originalCfgFile := cfgFile(context.Background())
	defer func() { gFlags.cfgFile = originalCfgFile }()
	gFlags.cfgFile = configPath

	// Create config with no current context
	cfg := config.NewConfig()
	cfg.SetContext("dev", "https://dev.example.com", "dev-token")
	cfg.CurrentContext = "" // Explicitly no current context
	if err := cfg.SaveTo(configPath); err != nil {
		t.Fatalf("failed to save config: %v", err)
	}

	results := runDoctorChecks(context.Background())

	// Should have version, config, then context (fail)
	found := false
	for _, r := range results {
		if r.Name == "Current context" {
			found = true
			if r.Status != "fail" {
				t.Errorf("expected context check to fail, got %q", r.Status)
			}
			if !strings.Contains(r.Detail, "no current context") {
				t.Errorf("expected detail about no current context, got %q", r.Detail)
			}
		}
	}
	if !found {
		t.Error("expected 'Current context' check in results")
	}
}

func TestDoctorValidConfigNoToken(t *testing.T) {
	// Disable the OS keyring so the token lookup can't pick up a "dev-token"
	// entry left in the shared keychain by a sibling test (macOS/Windows CI use
	// a real keychain across the whole package run); otherwise the token check
	// reports "ok" instead of the expected "fail".
	t.Setenv("DTCTL_DISABLE_KEYRING", "1")

	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config")

	originalCfgFile := cfgFile(context.Background())
	defer func() { gFlags.cfgFile = originalCfgFile }()
	gFlags.cfgFile = configPath

	// Create config with context but no token
	cfg := config.NewConfig()
	cfg.SetContext("dev", "https://dev.example.com", "dev-token")
	cfg.CurrentContext = "dev"
	// Don't set a token - it will fail at token retrieval
	if err := cfg.SaveTo(configPath); err != nil {
		t.Fatalf("failed to save config: %v", err)
	}

	results := runDoctorChecks(context.Background())

	// Should have version, config, context, then token (fail)
	found := false
	for _, r := range results {
		if r.Name == "Token" {
			found = true
			if r.Status != "fail" {
				t.Errorf("expected token check to fail, got %q", r.Status)
			}
		}
	}
	if !found {
		t.Error("expected 'Token' check in results")
	}
}

func TestDoctorFullPassingFlow(t *testing.T) {
	// Set up mock server that handles HEAD and user metadata
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodHead:
			w.WriteHeader(http.StatusOK)
		case r.URL.Path == "/platform/metadata/v1/user":
			resp := map[string]interface{}{
				"userId":       "test-user-id",
				"emailAddress": "test@example.com",
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(resp)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config")

	originalCfgFile := cfgFile(context.Background())
	defer func() { gFlags.cfgFile = originalCfgFile }()
	gFlags.cfgFile = configPath

	cfg := config.NewConfig()
	cfg.SetContext("test", server.URL, "test-token")
	if err := cfg.SetToken("test-token", "dt0c01.ST.test-token-value.test-secret"); err != nil {
		t.Fatalf("failed to set token: %v", err)
	}
	cfg.CurrentContext = "test"
	if err := cfg.SaveTo(configPath); err != nil {
		t.Fatalf("failed to save config: %v", err)
	}

	results := runDoctorChecks(context.Background())

	// Should have at least version, config, context, token, connectivity
	if len(results) < 5 {
		t.Fatalf("expected at least 5 results, got %d", len(results))
	}

	// Check early checks pass
	for _, r := range results {
		switch r.Name {
		case "dtctl version":
			if r.Status != "ok" {
				t.Errorf("version check: expected ok, got %q: %s", r.Status, r.Detail)
			}
		case "Configuration":
			if r.Status != "ok" {
				t.Errorf("config check: expected ok, got %q: %s", r.Status, r.Detail)
			}
		case "Current context":
			if r.Status != "ok" {
				t.Errorf("context check: expected ok, got %q: %s", r.Status, r.Detail)
			}
		case "Token":
			if r.Status != "ok" {
				t.Errorf("token check: expected ok, got %q: %s", r.Status, r.Detail)
			}
		case "Connectivity":
			if r.Status != "ok" {
				t.Errorf("connectivity check: expected ok, got %q: %s", r.Status, r.Detail)
			}
		}
	}
}

func TestDoctorConnectivityFailure(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config")

	originalCfgFile := cfgFile(context.Background())
	defer func() { gFlags.cfgFile = originalCfgFile }()
	gFlags.cfgFile = configPath

	// Start a server then immediately close it so the port refuses connections fast
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	unreachableURL := srv.URL
	srv.Close()

	cfg := config.NewConfig()
	cfg.SetContext("test", unreachableURL, "test-token")
	if err := cfg.SetToken("test-token", "dt0c01.ST.test-token-value.test-secret"); err != nil {
		t.Fatalf("failed to set token: %v", err)
	}
	cfg.CurrentContext = "test"
	if err := cfg.SaveTo(configPath); err != nil {
		t.Fatalf("failed to save config: %v", err)
	}

	// Use a very short timeout so the test doesn't wait on any OS-level delay
	fastClient := &http.Client{Timeout: 100 * time.Millisecond}
	results := runDoctorChecksWithClient(context.Background(), fastClient)

	found := false
	for _, r := range results {
		if r.Name == "Connectivity" {
			found = true
			if r.Status != "fail" {
				t.Errorf("expected connectivity check to fail, got %q", r.Status)
			}
			if !strings.Contains(r.Detail, "cannot reach") {
				t.Errorf("expected detail about unreachable, got %q", r.Detail)
			}
		}
	}
	if !found {
		t.Error("expected 'Connectivity' check in results")
	}
}

func TestDoctorCmdReturnsErrorOnFailure(t *testing.T) {
	tmpDir := t.TempDir()

	originalCfgFile := cfgFile(context.Background())
	defer func() { gFlags.cfgFile = originalCfgFile }()
	gFlags.cfgFile = filepath.Join(tmpDir, "nonexistent", "config")

	err := doctorCmd.RunE(doctorCmd, []string{})
	if err == nil {
		t.Fatal("expected doctor command to return error when checks fail")
	}
	if !strings.Contains(err.Error(), "failed") {
		t.Errorf("expected error to mention failure, got %q", err.Error())
	}
}

func TestPrintDoctorResults(t *testing.T) {
	// Just verify it doesn't panic
	results := []checkResult{
		{Name: "Test", Status: "ok", Detail: "all good"},
		{Name: "Test2", Status: "warn", Detail: "warning"},
		{Name: "Test3", Status: "fail", Detail: "failure"},
	}
	printDoctorResults(context.Background(), results)
}

func TestDoctorURLValidation(t *testing.T) {
	tests := []struct {
		name       string
		envURL     string
		wantStatus string // "ok" or "warn"
		wantInMsg  string // substring in Detail, if warn
	}{
		{
			name:       "correct apps URL",
			envURL:     "https://abc12345.apps.dynatrace.com",
			wantStatus: "ok",
		},
		{
			name:       "live.dynatrace.com URL",
			envURL:     "https://abc12345.live.dynatrace.com",
			wantStatus: "warn",
			wantInMsg:  "live.dynatrace.com",
		},
		{
			name:       "dev without apps",
			envURL:     "https://deve2e.dev.dynatracelabs.com",
			wantStatus: "warn",
			wantInMsg:  "dev.dynatracelabs.com",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Set up a mock server for connectivity
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusOK)
			}))
			defer server.Close()

			tmpDir := t.TempDir()
			configPath := filepath.Join(tmpDir, "config")

			originalCfgFile := cfgFile(context.Background())
			defer func() { gFlags.cfgFile = originalCfgFile }()
			gFlags.cfgFile = configPath

			cfg := config.NewConfig()
			cfg.SetContext("test", tt.envURL, "test-token")
			if err := cfg.SetToken("test-token", "dt0c01.ST.test-token-value.test-secret"); err != nil {
				t.Fatalf("failed to set token: %v", err)
			}
			cfg.CurrentContext = "test"
			if err := cfg.SaveTo(configPath); err != nil {
				t.Fatalf("failed to save config: %v", err)
			}

			fastClient := &http.Client{Timeout: 100 * time.Millisecond}
			results := runDoctorChecksWithClient(context.Background(), fastClient)

			found := false
			for _, r := range results {
				if r.Name == "Environment URL" {
					found = true
					if r.Status != tt.wantStatus {
						t.Errorf("expected status %q, got %q: %s", tt.wantStatus, r.Status, r.Detail)
					}
					if tt.wantInMsg != "" && !strings.Contains(r.Detail, tt.wantInMsg) {
						t.Errorf("expected detail to contain %q, got %q", tt.wantInMsg, r.Detail)
					}
				}
			}
			if !found {
				t.Error("expected 'Environment URL' check in results")
				for _, r := range results {
					t.Logf("  %s: %s: %s", r.Name, r.Status, r.Detail)
				}
			}
		})
	}
}

// TestDoctorPlatformToken403 verifies that when the platform metadata API
// returns 403 because a platform token (dt0s16.*) cannot be granted
// iam:users:read, the doctor authentication check is reported as "warn"
// (with an explanation) rather than "fail". Regression test for
// https://github.com/dynatrace-oss/dtctl/issues/190
func TestDoctorPlatformToken403(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodHead:
			w.WriteHeader(http.StatusOK)
		case r.URL.Path == "/platform/metadata/v1/user":
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte(`{"error":{"code":403,"message":"forbidden"}}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config")

	originalCfgFile := cfgFile(context.Background())
	defer func() { gFlags.cfgFile = originalCfgFile }()
	gFlags.cfgFile = configPath

	cfg := config.NewConfig()
	cfg.SetContext("test", server.URL, "platform-token")
	if err := cfg.SetToken("platform-token", "dt0s16.PLATFORM.test-token-value"); err != nil {
		t.Fatalf("failed to set token: %v", err)
	}
	cfg.CurrentContext = "test"
	if err := cfg.SaveTo(configPath); err != nil {
		t.Fatalf("failed to save config: %v", err)
	}

	results := runDoctorChecks(context.Background())

	found := false
	for _, r := range results {
		if r.Name == "Authentication" {
			found = true
			if r.Status != "warn" {
				t.Errorf("expected authentication status 'warn' for platform token + 403, got %q: %s", r.Status, r.Detail)
			}
			if !strings.Contains(r.Detail, "platform token") {
				t.Errorf("expected detail to mention 'platform token', got %q", r.Detail)
			}
		}
	}
	if !found {
		t.Error("expected 'Authentication' check in results")
		for _, r := range results {
			t.Logf("  %s: %s: %s", r.Name, r.Status, r.Detail)
		}
	}

	// The overall command should not return an error: warn-only checks must
	// not be promoted to a failure.
	for _, r := range results {
		if r.Status == "fail" {
			t.Errorf("unexpected fail check for platform token + 403: %s: %s", r.Name, r.Detail)
		}
	}
}

// TestDoctorFileTokenStorage verifies that when keyring is unavailable but
// DTCTL_TOKEN_STORAGE=file is set, the doctor check reports "ok" for
// token storage instead of "warn".
func TestDoctorFileTokenStorage(t *testing.T) {
	t.Setenv("DTCTL_DISABLE_KEYRING", "1")
	t.Setenv(config.EnvTokenStorage, "file")

	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config")

	originalCfgFile := cfgFile(context.Background())
	defer func() { gFlags.cfgFile = originalCfgFile }()
	gFlags.cfgFile = configPath

	cfg := config.NewConfig()
	cfg.SetContext("test", "https://test.apps.dynatrace.com", "test-token")
	if err := cfg.SetToken("test-token", "dt0c01.ST.test-token-value.test-secret"); err != nil {
		t.Fatalf("failed to set token: %v", err)
	}
	cfg.CurrentContext = "test"
	if err := cfg.SaveTo(configPath); err != nil {
		t.Fatalf("failed to save config: %v", err)
	}

	results := runDoctorChecks(context.Background())

	found := false
	for _, r := range results {
		if r.Name == "Token storage" {
			found = true
			if r.Status != "ok" {
				t.Errorf("expected token storage status 'ok' with file storage, got %q: %s", r.Status, r.Detail)
			}
			if !strings.Contains(r.Detail, "file-based") {
				t.Errorf("expected detail to mention 'file-based', got %q", r.Detail)
			}
		}
	}
	if !found {
		t.Error("expected 'Token storage' check in results")
		for _, r := range results {
			t.Logf("  %s: %s: %s", r.Name, r.Status, r.Detail)
		}
	}
}

func TestDoctorKeyringCheck(t *testing.T) {
	t.Setenv("DTCTL_DISABLE_KEYRING", "1")

	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config")

	originalCfgFile := cfgFile(context.Background())
	defer func() { gFlags.cfgFile = originalCfgFile }()
	gFlags.cfgFile = configPath

	cfg := config.NewConfig()
	cfg.SetContext("test", "https://test.apps.dynatrace.com", "test-token")
	if err := cfg.SetToken("test-token", "dt0c01.ST.test-token-value.test-secret"); err != nil {
		t.Fatalf("failed to set token: %v", err)
	}
	cfg.CurrentContext = "test"
	if err := cfg.SaveTo(configPath); err != nil {
		t.Fatalf("failed to save config: %v", err)
	}

	results := runDoctorChecks(context.Background())

	found := false
	for _, r := range results {
		if r.Name == "Token storage" {
			found = true
			if r.Status != "warn" {
				t.Errorf("expected token storage status 'warn' (disabled via env), got %q", r.Status)
			}
			if !strings.Contains(r.Detail, config.EnvDisableKeyring) {
				t.Errorf("expected token storage detail to mention %s, got %q", config.EnvDisableKeyring, r.Detail)
			}
		}
	}
	if !found {
		t.Error("expected 'Token storage' check in results")
		for _, r := range results {
			t.Logf("  %s: %s: %s", r.Name, r.Status, r.Detail)
		}
	}
}

// TestDoctorKeyringCollectionRecoverySuggestion verifies that when the keyring
// error contains the collection-unlock message, the doctor output includes the
// recovery suggestion telling the user to run 'dtctl auth login'.
func TestDoctorKeyringCollectionRecoverySuggestion(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config")

	originalCfgFile := cfgFile(context.Background())
	defer func() { gFlags.cfgFile = originalCfgFile }()
	gFlags.cfgFile = configPath

	cfg := config.NewConfig()
	cfg.SetContext("test", "https://test.apps.dynatrace.com", "test-token")
	if err := cfg.SetToken("test-token", "dt0c01.ST.test-token-value.test-secret"); err != nil {
		t.Fatalf("failed to set token: %v", err)
	}
	cfg.CurrentContext = "test"
	if err := cfg.SaveTo(configPath); err != nil {
		t.Fatalf("failed to save config: %v", err)
	}

	// Override checkKeyringFunc to return an error that contains the
	// collection-unlock message.
	origFunc := checkKeyringFunc
	defer func() { checkKeyringFunc = origFunc }()
	checkKeyringFunc = func() error {
		return fmt.Errorf("keyring probe failed: %s", config.ErrMsgCollectionUnlock)
	}

	results := runDoctorChecks(context.Background())

	found := false
	for _, r := range results {
		if r.Name == "Token storage" {
			found = true
			if r.Status != "warn" {
				t.Errorf("expected token storage status 'warn', got %q", r.Status)
			}
			if !strings.Contains(r.Detail, "dtctl auth login") {
				t.Errorf("expected recovery suggestion mentioning 'dtctl auth login', got %q", r.Detail)
			}
		}
	}
	if !found {
		t.Error("expected 'Token storage' check in results")
		for _, r := range results {
			t.Logf("  %s: %s: %s", r.Name, r.Status, r.Detail)
		}
	}
}
