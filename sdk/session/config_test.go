package session

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/adrg/xdg"
)

// mockKeyring is an in-memory keyringBackend for tests that need no OS keyring.
type mockKeyring struct{ data map[string]string }

func newMockKeyring() *mockKeyring           { return &mockKeyring{data: make(map[string]string)} }
func (m *mockKeyring) Available() bool       { return true }
func (m *mockKeyring) Set(n, v string) error { m.data[n] = v; return nil }
func (m *mockKeyring) Delete(n string) error { delete(m.data, n); return nil }
func (m *mockKeyring) Get(n string) (string, error) {
	v, ok := m.data[n]
	if !ok {
		return "", fmt.Errorf("token %q not found in keyring", n)
	}
	return v, nil
}

// refusingKeyring is available but rejects every delete, simulating a locked or
// otherwise uncooperative keyring.
type refusingKeyring struct{}

func (r *refusingKeyring) Available() bool { return true }
func (r *refusingKeyring) Get(string) (string, error) {
	return "", fmt.Errorf("keyring locked")
}
func (r *refusingKeyring) Set(string, string) error { return fmt.Errorf("keyring locked") }
func (r *refusingKeyring) Delete(string) error      { return fmt.Errorf("keyring locked") }

// unavailableKeyring simulates an environment without OS keyring (headless/WSL).
type unavailableKeyring struct{}

func (u *unavailableKeyring) Available() bool { return false }
func (u *unavailableKeyring) Get(string) (string, error) {
	return "", fmt.Errorf("keyring unavailable")
}
func (u *unavailableKeyring) Set(string, string) error { return fmt.Errorf("keyring unavailable") }
func (u *unavailableKeyring) Delete(string) error      { return fmt.Errorf("keyring unavailable") }

func TestNewConfig(t *testing.T) {
	cfg := NewConfig()

	if cfg.APIVersion != "v1" {
		t.Errorf("APIVersion = %v, want v1", cfg.APIVersion)
	}
	if cfg.Kind != "Config" {
		t.Errorf("Kind = %v, want Config", cfg.Kind)
	}
	if len(cfg.Contexts) != 0 {
		t.Errorf("Contexts should be empty, got %d", len(cfg.Contexts))
	}
	if len(cfg.Tokens) != 0 {
		t.Errorf("Tokens should be empty, got %d", len(cfg.Tokens))
	}
	if cfg.Preferences.Output != "table" {
		t.Errorf("Preferences.Output = %v, want table", cfg.Preferences.Output)
	}
	if cfg.Preferences.Editor != "vim" {
		t.Errorf("Preferences.Editor = %v, want vim", cfg.Preferences.Editor)
	}
}

func TestConfig_SetContext(t *testing.T) {
	cfg := NewConfig()

	// Add new context
	cfg.SetContext("dev", "https://dev.dynatrace.com", "dev-token")

	if len(cfg.Contexts) != 1 {
		t.Fatalf("Expected 1 context, got %d", len(cfg.Contexts))
	}
	if cfg.Contexts[0].Name != "dev" {
		t.Errorf("Context name = %v, want dev", cfg.Contexts[0].Name)
	}
	if cfg.Contexts[0].Context.Environment != "https://dev.dynatrace.com" {
		t.Errorf("Environment = %v, want https://dev.dynatrace.com", cfg.Contexts[0].Context.Environment)
	}

	// Update existing context
	cfg.SetContext("dev", "https://dev2.dynatrace.com", "")

	if len(cfg.Contexts) != 1 {
		t.Fatalf("Expected 1 context after update, got %d", len(cfg.Contexts))
	}
	if cfg.Contexts[0].Context.Environment != "https://dev2.dynatrace.com" {
		t.Errorf("Updated environment = %v, want https://dev2.dynatrace.com", cfg.Contexts[0].Context.Environment)
	}
	// Token should remain unchanged when empty string passed
	if cfg.Contexts[0].Context.TokenRef != "dev-token" {
		t.Errorf("TokenRef should remain dev-token, got %v", cfg.Contexts[0].Context.TokenRef)
	}
}

func TestConfig_SetContext_NormalizesEnvironmentScheme(t *testing.T) {
	cfg := NewConfig()

	// A bare host without a scheme must be stored with https:// so that later
	// queries don't fail with "unsupported protocol scheme".
	cfg.SetContext("dev", "abc12345.apps.dynatrace.com", "dev-token")
	if got := cfg.Contexts[0].Context.Environment; got != "https://abc12345.apps.dynatrace.com" {
		t.Errorf("Environment = %v, want https://abc12345.apps.dynatrace.com", got)
	}

	// An explicit scheme is preserved as-is.
	cfg.SetContext("local", "http://127.0.0.1:8080", "local-token")
	if got := cfg.Contexts[1].Context.Environment; got != "http://127.0.0.1:8080" {
		t.Errorf("Environment = %v, want http://127.0.0.1:8080", got)
	}
}

func TestConfig_SetToken(t *testing.T) {
	cfg := NewConfig()

	// Add new token
	err := cfg.SetToken("my-token", "secret-value")
	if err != nil {
		t.Fatalf("SetToken() error = %v", err)
	}

	if len(cfg.Tokens) != 1 {
		t.Fatalf("Expected 1 token, got %d", len(cfg.Tokens))
	}
	if cfg.Tokens[0].Name != "my-token" {
		t.Errorf("Token name = %v, want my-token", cfg.Tokens[0].Name)
	}
	// Token may be empty if keyring is available (stored there instead)
	if !IsKeyringAvailable() && cfg.Tokens[0].Token != "secret-value" {
		t.Errorf("Token value = %v, want secret-value", cfg.Tokens[0].Token)
	}

	// Update existing token
	err = cfg.SetToken("my-token", "new-secret")
	if err != nil {
		t.Fatalf("SetToken() update error = %v", err)
	}

	if len(cfg.Tokens) != 1 {
		t.Fatalf("Expected 1 token after update, got %d", len(cfg.Tokens))
	}
}

func TestConfig_GetToken(t *testing.T) {
	cfg := NewConfig()
	_ = cfg.SetToken("existing", "token-value")

	tests := []struct {
		name     string
		tokenRef string
		want     string
		wantErr  bool
	}{
		{
			name:     "existing token",
			tokenRef: "existing",
			want:     "token-value",
			wantErr:  false,
		},
		{
			name:     "non-existing token",
			tokenRef: "missing",
			want:     "",
			wantErr:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := cfg.GetToken(tt.tokenRef)
			if (err != nil) != tt.wantErr {
				t.Errorf("GetToken() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if got != tt.want {
				t.Errorf("GetToken() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestConfig_oauthKeyringNames(t *testing.T) {
	cfg := NewConfig()
	cfg.SetContext("prod", "https://abc123.apps.dynatrace.com", "shared-token")
	cfg.SetContext("dev", "https://dev456.dev.apps.dynatracelabs.com", "shared-token")

	got := cfg.oauthKeyringNames("shared-token")
	want := []string{
		"oauth:prod:shared-token",
		"oauth:dev:shared-token",
		"oauth:hard:shared-token",
	}

	if len(got) != len(want) {
		t.Fatalf("oauthKeyringNames() len = %d, want %d (got=%v)", len(got), len(want), got)
	}

	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("oauthKeyringNames()[%d] = %q, want %q (all=%v)", i, got[i], want[i], got)
		}
	}
}

func TestOAuthEnvironmentFromURL(t *testing.T) {
	tests := []struct {
		name string
		url  string
		want string
	}{
		{name: "prod", url: "https://abc123.apps.dynatrace.com", want: "prod"},
		{name: "dev", url: "https://abc.dev.apps.dynatracelabs.com", want: "dev"},
		{name: "hard", url: "https://abc.sprint.apps.dynatracelabs.com", want: "hard"},
		{name: "unknown", url: "https://example.com", want: ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := oauthEnvironmentFromURL(tt.url); got != tt.want {
				t.Errorf("oauthEnvironmentFromURL() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestConfig_MustGetToken(t *testing.T) {
	cfg := NewConfig()
	_ = cfg.SetToken("existing", "token-value")

	// Existing token
	if got := cfg.MustGetToken("existing"); got != "token-value" {
		t.Errorf("MustGetToken() = %v, want token-value", got)
	}

	// Non-existing token returns empty string
	if got := cfg.MustGetToken("missing"); got != "" {
		t.Errorf("MustGetToken() for missing = %v, want empty", got)
	}
}

func TestConfig_CurrentContextObj(t *testing.T) {
	cfg := NewConfig()
	cfg.SetContext("prod", "https://prod.dynatrace.com", "prod-token")

	// No current context set
	_, err := cfg.CurrentContextObj()
	if err == nil {
		t.Error("Expected error when no current context set")
	}

	// Set current context
	cfg.CurrentContext = "prod"
	ctx, err := cfg.CurrentContextObj()
	if err != nil {
		t.Fatalf("CurrentContextObj() error = %v", err)
	}
	if ctx.Environment != "https://prod.dynatrace.com" {
		t.Errorf("Environment = %v, want https://prod.dynatrace.com", ctx.Environment)
	}

	// Non-existing current context
	cfg.CurrentContext = "nonexistent"
	_, err = cfg.CurrentContextObj()
	if err == nil {
		t.Error("Expected error for non-existing current context")
	}
}

// TestConfig_CurrentContextObj_AliasesConfig guards #543: the returned pointer
// must alias the entry in cfg.Contexts, not a loop copy, so a write through it
// changes the config instead of being silently dropped.
func TestConfig_CurrentContextObj_AliasesConfig(t *testing.T) {
	cfg := NewConfig()
	cfg.SetContext("dev", "https://dev.example.invalid", "dev-token")
	cfg.SetContext("prod", "https://prod.example.invalid", "prod-token")
	cfg.CurrentContext = "prod"

	ctx, err := cfg.CurrentContextObj()
	if err != nil {
		t.Fatalf("CurrentContextObj() error = %v", err)
	}
	ctx.Description = "edited"
	ctx.NoDeprecated = true

	got, err := cfg.GetContext("prod")
	if err != nil {
		t.Fatalf("GetContext(prod) error = %v", err)
	}
	if got.Context.Description != "edited" || !got.Context.NoDeprecated {
		t.Errorf("write through CurrentContextObj was lost: got %+v", got.Context)
	}
	if dev, _ := cfg.GetContext("dev"); dev.Context.Description != "" || dev.Context.NoDeprecated {
		t.Errorf("write through CurrentContextObj leaked into another context: %+v", dev.Context)
	}

	// GetContext must alias the config the same way.
	got.Context.Environment = "https://edited.example.invalid"
	if again, _ := cfg.CurrentContextObj(); again.Environment != "https://edited.example.invalid" {
		t.Errorf("write through GetContext was lost: Environment = %q", again.Environment)
	}
}

func TestConfig_SaveAndLoad(t *testing.T) {
	// Create temp directory
	tmpDir, err := os.MkdirTemp("", "dtctl-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer func() {
		_ = os.RemoveAll(tmpDir)
	}()

	configPath := filepath.Join(tmpDir, "config")

	// Create and save config
	cfg := NewConfig()
	cfg.SetContext("test", "https://test.dynatrace.com", "test-token")
	_ = cfg.SetToken("test-token", "secret123")
	cfg.CurrentContext = "test"

	if err := cfg.SaveTo(configPath); err != nil {
		t.Fatalf("SaveTo() error = %v", err)
	}

	// Verify file permissions (Unix-like systems only)
	if runtime.GOOS != "windows" {
		info, err := os.Stat(configPath)
		if err != nil {
			t.Fatalf("Failed to stat config file: %v", err)
		}
		if info.Mode().Perm() != 0600 {
			t.Errorf("Config file permissions = %v, want 0600", info.Mode().Perm())
		}
	}

	// Load config
	loaded, err := LoadFrom(configPath)
	if err != nil {
		t.Fatalf("LoadFrom() error = %v", err)
	}

	if loaded.CurrentContext != "test" {
		t.Errorf("Loaded CurrentContext = %v, want test", loaded.CurrentContext)
	}
	if len(loaded.Contexts) != 1 {
		t.Fatalf("Loaded contexts count = %d, want 1", len(loaded.Contexts))
	}
	if loaded.Contexts[0].Context.Environment != "https://test.dynatrace.com" {
		t.Errorf("Loaded environment = %v", loaded.Contexts[0].Context.Environment)
	}
}

func TestLoadFrom_NotFound(t *testing.T) {
	_, err := LoadFrom("/nonexistent/path/config")
	if err == nil {
		t.Error("Expected error for non-existent config file")
	}
}

func TestLoadFrom_InvalidYAML(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "dtctl-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer func() {
		_ = os.RemoveAll(tmpDir)
	}()

	configPath := filepath.Join(tmpDir, "config")
	if err := os.WriteFile(configPath, []byte("invalid: yaml: content: ["), 0600); err != nil {
		t.Fatalf("Failed to write invalid config: %v", err)
	}

	_, err = LoadFrom(configPath)
	if err == nil {
		t.Error("Expected error for invalid YAML")
	}
}

func TestConfigDir(t *testing.T) {
	dir := ConfigDir()
	if dir == "" {
		t.Error("ConfigDir() returned empty string")
	}
}

func TestCacheDir(t *testing.T) {
	dir := CacheDir()
	if dir == "" {
		t.Error("CacheDir() returned empty string")
	}
}

func TestDataDir(t *testing.T) {
	dir := DataDir()
	if dir == "" {
		t.Error("DataDir() returned empty string")
	}
}

func TestConfig_MultipleContexts(t *testing.T) {
	cfg := NewConfig()

	cfg.SetContext("dev", "https://dev.dt.com", "dev-token")
	cfg.SetContext("staging", "https://staging.dt.com", "staging-token")
	cfg.SetContext("prod", "https://prod.dt.com", "prod-token")

	if len(cfg.Contexts) != 3 {
		t.Errorf("Expected 3 contexts, got %d", len(cfg.Contexts))
	}

	// Switch contexts
	cfg.CurrentContext = "staging"
	ctx, err := cfg.CurrentContextObj()
	if err != nil {
		t.Fatalf("CurrentContextObj() error = %v", err)
	}
	if ctx.Environment != "https://staging.dt.com" {
		t.Errorf("Wrong context environment: %v", ctx.Environment)
	}
}

func TestConfig_Save(t *testing.T) {
	// Create temp directory
	tmpDir := t.TempDir()

	// Override XDG for this test
	t.Setenv("XDG_CONFIG_HOME", tmpDir)
	xdg.Reload()
	defer xdg.Reload()

	cfg := NewConfig()
	cfg.SetContext("test", "https://test.dt.com", "token")

	// Save should work (creates directory if needed)
	err := cfg.Save()
	if err != nil {
		t.Errorf("Save() error = %v", err)
	}
}

func TestConfig_Load(t *testing.T) {
	// Create temp directory with config
	tmpDir, err := os.MkdirTemp("", "dtctl-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	// Create config directory and file
	configDir := filepath.Join(tmpDir, "dtctl")
	if err := os.MkdirAll(configDir, 0700); err != nil {
		t.Fatalf("Failed to create config dir: %v", err)
	}

	configContent := `apiVersion: v1
kind: Config
current-context: test
contexts:
  - name: test
    context:
      environment: https://test.dt.com
      token-ref: test-token
tokens:
  - name: test-token
    token: secret123
`
	configPath := filepath.Join(configDir, "config")
	if err := os.WriteFile(configPath, []byte(configContent), 0600); err != nil {
		t.Fatalf("Failed to write config: %v", err)
	}

	// Use LoadFrom directly instead of Load() to avoid XDG caching issues
	cfg, err := LoadFrom(configPath)
	if err != nil {
		t.Fatalf("LoadFrom() error = %v", err)
	}

	if cfg.CurrentContext != "test" {
		t.Errorf("CurrentContext = %v, want test", cfg.CurrentContext)
	}
}

func TestDefaultConfigPath(t *testing.T) {
	path := DefaultConfigPath()
	if path == "" {
		t.Error("DefaultConfigPath() returned empty string")
	}
}

func TestSaveTo_CreateDirectory(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "dtctl-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	// Path with non-existent subdirectory
	configPath := filepath.Join(tmpDir, "subdir", "nested", "config")

	cfg := NewConfig()
	err = cfg.SaveTo(configPath)
	if err != nil {
		t.Fatalf("SaveTo() error = %v", err)
	}

	// Verify directory was created with correct permissions (Unix-like systems only)
	if runtime.GOOS != "windows" {
		dirInfo, err := os.Stat(filepath.Dir(configPath))
		if err != nil {
			t.Fatalf("Failed to stat directory: %v", err)
		}
		if dirInfo.Mode().Perm() != 0700 {
			t.Errorf("Directory permissions = %v, want 0700", dirInfo.Mode().Perm())
		}
	}
}

// Safety Level Tests

func TestSafetyLevel_IsValid(t *testing.T) {
	tests := []struct {
		level SafetyLevel
		valid bool
	}{
		{SafetyLevelReadOnly, true},
		{SafetyLevelReadWriteMine, true},
		{SafetyLevelReadWriteAll, true},
		{SafetyLevelDangerouslyUnrestricted, true},
		{"", true}, // Empty is valid (uses default)
		{"invalid", false},
		{"read-only", false},  // Old name, no longer valid
		{"read-write", false}, // Old name, no longer valid
	}

	for _, tt := range tests {
		t.Run(string(tt.level), func(t *testing.T) {
			if got := tt.level.IsValid(); got != tt.valid {
				t.Errorf("SafetyLevel(%q).IsValid() = %v, want %v", tt.level, got, tt.valid)
			}
		})
	}
}

func TestSafetyLevel_String(t *testing.T) {
	tests := []struct {
		level SafetyLevel
		want  string
	}{
		{SafetyLevelReadOnly, "readonly"},
		{SafetyLevelReadWriteMine, "readwrite-mine"},
		{SafetyLevelReadWriteAll, "readwrite-all"},
		{SafetyLevelDangerouslyUnrestricted, "dangerously-unrestricted"},
		{"", "readwrite-all"}, // Empty returns default
	}

	for _, tt := range tests {
		t.Run(string(tt.level), func(t *testing.T) {
			if got := tt.level.String(); got != tt.want {
				t.Errorf("SafetyLevel(%q).String() = %v, want %v", tt.level, got, tt.want)
			}
		})
	}
}

func TestValidSafetyLevels(t *testing.T) {
	levels := ValidSafetyLevels()

	if len(levels) != 4 {
		t.Errorf("ValidSafetyLevels() returned %d levels, want 4", len(levels))
	}

	// Verify all returned levels are valid
	for _, level := range levels {
		if !level.IsValid() {
			t.Errorf("ValidSafetyLevels() returned invalid level: %s", level)
		}
	}

	// Verify expected levels are present
	expected := map[SafetyLevel]bool{
		SafetyLevelReadOnly:                false,
		SafetyLevelReadWriteMine:           false,
		SafetyLevelReadWriteAll:            false,
		SafetyLevelDangerouslyUnrestricted: false,
	}
	for _, level := range levels {
		expected[level] = true
	}
	for level, found := range expected {
		if !found {
			t.Errorf("ValidSafetyLevels() missing level: %s", level)
		}
	}
}

func TestContext_GetEffectiveSafetyLevel(t *testing.T) {
	tests := []struct {
		name     string
		level    SafetyLevel
		expected SafetyLevel
	}{
		{"explicit readonly", SafetyLevelReadOnly, SafetyLevelReadOnly},
		{"explicit readwrite-mine", SafetyLevelReadWriteMine, SafetyLevelReadWriteMine},
		{"explicit readwrite-all", SafetyLevelReadWriteAll, SafetyLevelReadWriteAll},
		{"explicit unrestricted", SafetyLevelDangerouslyUnrestricted, SafetyLevelDangerouslyUnrestricted},
		{"empty defaults to readwrite-all", "", SafetyLevelReadWriteAll},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := &Context{
				Environment: "https://test.dt.com",
				SafetyLevel: tt.level,
			}
			if got := ctx.GetEffectiveSafetyLevel(); got != tt.expected {
				t.Errorf("GetEffectiveSafetyLevel() = %v, want %v", got, tt.expected)
			}
		})
	}
}

func TestConfig_SetContextWithOptions(t *testing.T) {
	cfg := NewConfig()

	opts := &ContextOptions{
		SafetyLevel: SafetyLevelReadOnly,
		Description: "Production read-only access",
	}

	cfg.SetContextWithOptions("prod", "https://prod.dt.com", "prod-token", opts)

	if len(cfg.Contexts) != 1 {
		t.Fatalf("Expected 1 context, got %d", len(cfg.Contexts))
	}

	ctx := cfg.Contexts[0].Context
	if ctx.SafetyLevel != SafetyLevelReadOnly {
		t.Errorf("SafetyLevel = %v, want %v", ctx.SafetyLevel, SafetyLevelReadOnly)
	}
	if ctx.Description != "Production read-only access" {
		t.Errorf("Description = %v, want 'Production read-only access'", ctx.Description)
	}

	// Update with new options
	opts2 := &ContextOptions{
		SafetyLevel: SafetyLevelReadWriteAll,
	}
	cfg.SetContextWithOptions("prod", "https://prod.dt.com", "", opts2)

	if len(cfg.Contexts) != 1 {
		t.Fatalf("Expected 1 context after update, got %d", len(cfg.Contexts))
	}

	ctx = cfg.Contexts[0].Context
	if ctx.SafetyLevel != SafetyLevelReadWriteAll {
		t.Errorf("Updated SafetyLevel = %v, want %v", ctx.SafetyLevel, SafetyLevelReadWriteAll)
	}
	// Description should remain unchanged when not provided in update
	if ctx.Description != "Production read-only access" {
		t.Errorf("Description should remain unchanged, got %v", ctx.Description)
	}
}

func TestConfig_SetContextWithOptions_UsageTracking(t *testing.T) {
	cfg := NewConfig()
	on, off := true, false

	cfg.SetContextWithOptions("prod", "https://prod.dt.com", "tok", nil)
	cfg.CurrentContext = "prod"
	if cfg.UsageTrackingEnabled() {
		t.Fatal("usage tracking must be off by default")
	}

	cfg.SetContextWithOptions("prod", "https://prod.dt.com", "", &ContextOptions{UsageTracking: &on})
	if !cfg.UsageTrackingEnabled() {
		t.Fatal("UsageTracking=&true must enable tracking")
	}

	// An update that does not mention tracking must leave it alone.
	cfg.SetContextWithOptions("prod", "https://prod.dt.com", "", &ContextOptions{Description: "x"})
	if !cfg.UsageTrackingEnabled() {
		t.Fatal("an unrelated update must not switch tracking off")
	}

	cfg.SetContextWithOptions("prod", "https://prod.dt.com", "", &ContextOptions{UsageTracking: &off})
	if cfg.UsageTrackingEnabled() {
		t.Fatal("UsageTracking=&false must disable tracking")
	}

	// A new context can be created with tracking on.
	cfg.SetContextWithOptions("dev", "https://dev.dt.com", "tok", &ContextOptions{UsageTracking: &on})
	cfg.CurrentContext = "dev"
	if !cfg.UsageTrackingEnabled() {
		t.Fatal("new context created with tracking on must report enabled")
	}

	cfg.CurrentContext = "missing"
	if cfg.UsageTrackingEnabled() {
		t.Fatal("an unresolvable current context must report tracking off")
	}
}

func TestConfig_SetContextWithOptions_NilOpts(t *testing.T) {
	cfg := NewConfig()

	// SetContextWithOptions with nil opts should work like SetContext
	cfg.SetContextWithOptions("test", "https://test.dt.com", "test-token", nil)

	if len(cfg.Contexts) != 1 {
		t.Fatalf("Expected 1 context, got %d", len(cfg.Contexts))
	}

	ctx := cfg.Contexts[0].Context
	if ctx.SafetyLevel != "" {
		t.Errorf("SafetyLevel should be empty (use default), got %v", ctx.SafetyLevel)
	}
	if ctx.GetEffectiveSafetyLevel() != SafetyLevelReadWriteAll {
		t.Errorf("GetEffectiveSafetyLevel() = %v, want %v", ctx.GetEffectiveSafetyLevel(), SafetyLevelReadWriteAll)
	}
}

func TestFindLocalConfig(t *testing.T) {
	// Create a temp directory hierarchy
	tmpDir, err := os.MkdirTemp("", "dtctl-local-config-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	// Create nested directory structure: tmpDir/project/subdir/nested
	projectDir := filepath.Join(tmpDir, "project")
	subDir := filepath.Join(projectDir, "subdir")
	nestedDir := filepath.Join(subDir, "nested")
	if err := os.MkdirAll(nestedDir, 0755); err != nil {
		t.Fatalf("Failed to create nested dirs: %v", err)
	}

	// Test 1: No local config exists
	result := findLocalConfigFrom(nestedDir)
	if result != "" {
		t.Errorf("findLocalConfigFrom() should return empty when no config exists, got %q", result)
	}

	// Test 2: Create .dtctl.yaml in project root
	localConfigPath := filepath.Join(projectDir, LocalConfigName)
	configContent := `apiVersion: v1
kind: Config
current-context: local-test
contexts:
  - name: local-test
    context:
      environment: https://local.dt.com
      token-ref: local-token
`
	if err := os.WriteFile(localConfigPath, []byte(configContent), 0600); err != nil {
		t.Fatalf("Failed to write local config: %v", err)
	}

	// Test 3: Find config from project dir
	result = findLocalConfigFrom(projectDir)
	if result != localConfigPath {
		t.Errorf("findLocalConfigFrom(projectDir) = %q, want %q", result, localConfigPath)
	}

	// Test 4: Find config from nested subdir (walks up to project dir)
	result = findLocalConfigFrom(nestedDir)
	if result != localConfigPath {
		t.Errorf("findLocalConfigFrom(nestedDir) = %q, want %q", result, localConfigPath)
	}

	// Test 5: Config in subdir takes precedence
	subConfigPath := filepath.Join(subDir, LocalConfigName)
	if err := os.WriteFile(subConfigPath, []byte(configContent), 0600); err != nil {
		t.Fatalf("Failed to write subdir config: %v", err)
	}

	result = findLocalConfigFrom(nestedDir)
	if result != subConfigPath {
		t.Errorf("findLocalConfigFrom(nestedDir) with subdir config = %q, want %q", result, subConfigPath)
	}

	// Test 6: Starting from root should not find config
	result = findLocalConfigFrom("/")
	if result != "" {
		t.Errorf("findLocalConfigFrom('/') should return empty, got %q", result)
	}
}

func TestLocalConfigName(t *testing.T) {
	t.Parallel()
	if LocalConfigName != ".dtctl.yaml" {
		t.Errorf("LocalConfigName = %q, want .dtctl.yaml", LocalConfigName)
	}
}

func TestFindLocalConfig_Integration(t *testing.T) {
	// NOT parallel: os.Chdir is process-global and races with other tests
	// Create a temp directory hierarchy
	tmpDir, err := os.MkdirTemp("", "dtctl-find-local-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	// Create nested directory structure
	projectDir := filepath.Join(tmpDir, "project")
	subDir := filepath.Join(projectDir, "subdir")
	if err := os.MkdirAll(subDir, 0755); err != nil {
		t.Fatalf("Failed to create nested dirs: %v", err)
	}

	// Create local config in project root
	localConfigPath := filepath.Join(projectDir, LocalConfigName)
	configContent := `apiVersion: v1
kind: Config
current-context: local-test
`
	if err := os.WriteFile(localConfigPath, []byte(configContent), 0600); err != nil {
		t.Fatalf("Failed to write local config: %v", err)
	}

	// Save current working directory
	origWd, err := os.Getwd()
	if err != nil {
		t.Fatalf("Failed to get working directory: %v", err)
	}
	defer func() {
		_ = os.Chdir(origWd)
	}()

	// Change to subdirectory and test FindLocalConfig
	if err := os.Chdir(subDir); err != nil {
		t.Fatalf("Failed to change directory: %v", err)
	}

	result := FindLocalConfig()
	// Use filepath.EvalSymlinks to handle /var vs /private/var on macOS
	expectedPath, _ := filepath.EvalSymlinks(localConfigPath)
	actualPath, _ := filepath.EvalSymlinks(result)
	if actualPath != expectedPath {
		t.Errorf("FindLocalConfig() from subdir = %q, want %q", result, localConfigPath)
	}

	// Change to project root and test
	if err := os.Chdir(projectDir); err != nil {
		t.Fatalf("Failed to change to project dir: %v", err)
	}

	result = FindLocalConfig()
	actualPath, _ = filepath.EvalSymlinks(result)
	if actualPath != expectedPath {
		t.Errorf("FindLocalConfig() from project dir = %q, want %q", result, localConfigPath)
	}
}

func TestLoad_LocalConfigPrecedence(t *testing.T) {
	// NOT parallel: os.Chdir is process-global and races with other tests
	// This test verifies the Load() function logic by checking directory changes
	// We can't fully test XDG env var changes due to library caching,
	// but we can verify local config detection works

	tmpDir, err := os.MkdirTemp("", "dtctl-load-precedence-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	// Create local config
	localConfigPath := filepath.Join(tmpDir, LocalConfigName)
	localContent := `apiVersion: v1
kind: Config
current-context: local-ctx
contexts:
  - name: local-ctx
    context:
      environment: https://local.dt.com
      token-ref: local-token
`
	if err := os.WriteFile(localConfigPath, []byte(localContent), 0600); err != nil {
		t.Fatalf("Failed to write local config: %v", err)
	}

	// Save current working directory
	origWd, err := os.Getwd()
	if err != nil {
		t.Fatalf("Failed to get working directory: %v", err)
	}
	defer func() {
		_ = os.Chdir(origWd)
	}()

	// Change to directory with local config
	if err := os.Chdir(tmpDir); err != nil {
		t.Fatalf("Failed to change directory: %v", err)
	}

	// Load should find local config
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	if cfg.CurrentContext != "local-ctx" {
		t.Errorf("Load() returned CurrentContext = %q, want 'local-ctx' (should find local config)", cfg.CurrentContext)
	}
}

// TestLoad_LocalConfigIgnoresExecKeys verifies the AI-36 hardening: an
// auto-discovered local .dtctl.yaml must never have its code-execution keys
// (shell aliases or apply hooks) honored. The values stay in the struct so
// edit/save commands round-trip the file, but the honoring points
// (GetPreApplyHook/GetPostApplyHook, and resolveAlias which checks IsLocal)
// must treat them as absent. Load flags that such keys are being ignored.
func TestLoad_LocalConfigIgnoresExecKeys(t *testing.T) {
	// NOT parallel: os.Chdir is process-global and races with other tests.
	tmpDir, err := os.MkdirTemp("", "dtctl-ignore-exec-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	localConfigPath := filepath.Join(tmpDir, LocalConfigName)
	localContent := `apiVersion: v1
kind: Config
current-context: local-ctx
contexts:
  - name: local-ctx
    context:
      environment: https://local.dt.com
      token-ref: local-token
      hooks:
        pre-apply: "bash -c 'id > /tmp/pwned'"
        post-apply: "curl -s https://evil.example"
preferences:
  hooks:
    pre-apply: "evil-global-pre"
    post-apply: "evil-global-post"
aliases:
  version: "!echo PWNED > /tmp/pwned"
  wf: "get workflows"
`
	if err := os.WriteFile(localConfigPath, []byte(localContent), 0600); err != nil {
		t.Fatalf("Failed to write local config: %v", err)
	}

	origWd, err := os.Getwd()
	if err != nil {
		t.Fatalf("Failed to get working directory: %v", err)
	}
	defer func() { _ = os.Chdir(origWd) }()

	if err := os.Chdir(tmpDir); err != nil {
		t.Fatalf("Failed to change directory: %v", err)
	}

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	// Non-executable config is still honored.
	if cfg.CurrentContext != "local-ctx" {
		t.Errorf("CurrentContext = %q, want local-ctx", cfg.CurrentContext)
	}

	// Code-execution keys are NOT honored: the effective hooks resolve to
	// empty because the config is local.
	if cfg.GetPreApplyHook() != "" {
		t.Errorf("GetPreApplyHook() = %q, want empty (local hooks not honored)", cfg.GetPreApplyHook())
	}
	if cfg.GetPostApplyHook() != "" {
		t.Errorf("GetPostApplyHook() = %q, want empty (local hooks not honored)", cfg.GetPostApplyHook())
	}

	// ...but the raw values remain in the struct so a load-modify-save by a
	// config-management command does not silently destroy the user's file.
	if len(cfg.Aliases) != 2 {
		t.Errorf("Aliases = %v, want both retained for round-trip safety", cfg.Aliases)
	}
	if cfg.Preferences.Hooks.PreApply != "evil-global-pre" {
		t.Errorf("Preferences.Hooks.PreApply = %q, want retained for round-trip", cfg.Preferences.Hooks.PreApply)
	}

	// Metadata for the caller-facing warning.
	if !cfg.IsLocal() {
		t.Error("IsLocal() = false, want true")
	}
	if !cfg.IgnoredExecKeys() {
		t.Error("IgnoredExecKeys() = false, want true")
	}
	gotPath, _ := filepath.EvalSymlinks(cfg.LocalConfigPath())
	wantPath, _ := filepath.EvalSymlinks(localConfigPath)
	if gotPath != wantPath {
		t.Errorf("LocalConfigPath() = %q, want %q", gotPath, wantPath)
	}
}

// TestConfig_GetEffectiveSafetyLevel_LocalClampsToGlobal verifies that a local
// .dtctl.yaml cannot escalate its safety level beyond what the global config
// permits for the token-ref it borrows. The clamp is keyed by token-ref, the
// same binding GetToken enforces, so it holds even when the local context is
// named differently from the global one.
func TestConfig_GetEffectiveSafetyLevel_LocalClampsToGlobal(t *testing.T) {
	// NOT parallel: os.Chdir and XDG_CONFIG_HOME are process-global.
	cases := []struct {
		name         string
		globalLevel  string
		localCtxName string
		localRef     string
		localLevel   string
		wantLevel    SafetyLevel
	}{
		{
			name:         "escalation blocked",
			globalLevel:  "readonly",
			localCtxName: "myctx",
			localRef:     "prod-token",
			localLevel:   "dangerously-unrestricted",
			wantLevel:    SafetyLevelReadOnly,
		},
		{
			// The reviewer's case: the local config satisfies the origin
			// binding (same token-ref, same host) but renames the context.
			// Keying the clamp by context name let this escalate.
			name:         "escalation blocked when context name differs",
			globalLevel:  "readonly",
			localCtxName: "project-local",
			localRef:     "prod-token",
			localLevel:   "dangerously-unrestricted",
			wantLevel:    SafetyLevelReadOnly,
		},
		{
			name:         "local stricter than global is kept",
			globalLevel:  "dangerously-unrestricted",
			localCtxName: "myctx",
			localRef:     "prod-token",
			localLevel:   "readonly",
			wantLevel:    SafetyLevelReadOnly,
		},
		{
			// An omitted global safety-level still clamps: it resolves to the
			// readwrite-all default, not to "unbounded".
			name:         "omitted global level clamps to the default",
			globalLevel:  "",
			localCtxName: "myctx",
			localRef:     "prod-token",
			localLevel:   "dangerously-unrestricted",
			wantLevel:    SafetyLevelReadWriteAll,
		},
		{
			// An unbound token-ref has no trust anchor, so it gets no more
			// than the default. GetToken refuses it as well.
			name:         "unbound token-ref clamps to the default",
			globalLevel:  "readonly",
			localCtxName: "myctx",
			localRef:     "unbound-token",
			localLevel:   "dangerously-unrestricted",
			wantLevel:    SafetyLevelReadWriteAll,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tmpDir := t.TempDir()
			projectDir := t.TempDir()

			xdgDir := filepath.Join(tmpDir, "xdg")
			globalDir := filepath.Join(xdgDir, "dtctl")
			if err := os.MkdirAll(globalDir, 0700); err != nil {
				t.Fatalf("mkdir: %v", err)
			}

			globalCfg := `apiVersion: v1
kind: Config
current-context: myctx
contexts:
  - name: myctx
    context:
      environment: https://global.dt.com
      token-ref: prod-token
`
			if tc.globalLevel != "" {
				globalCfg += "      safety-level: " + tc.globalLevel + "\n"
			}
			if err := os.WriteFile(filepath.Join(globalDir, "config"), []byte(globalCfg), 0600); err != nil {
				t.Fatalf("write global config: %v", err)
			}

			t.Setenv("XDG_CONFIG_HOME", xdgDir)
			xdg.Reload()
			defer xdg.Reload()

			localCfg := `apiVersion: v1
kind: Config
current-context: ` + tc.localCtxName + `
contexts:
  - name: ` + tc.localCtxName + `
    context:
      environment: https://global.dt.com
      token-ref: ` + tc.localRef + `
      safety-level: ` + tc.localLevel + `
`
			if err := os.WriteFile(filepath.Join(projectDir, LocalConfigName), []byte(localCfg), 0600); err != nil {
				t.Fatalf("write local config: %v", err)
			}

			origWd, _ := os.Getwd()
			defer func() { _ = os.Chdir(origWd) }()
			if err := os.Chdir(projectDir); err != nil {
				t.Fatalf("chdir: %v", err)
			}

			cfg, err := Load()
			if err != nil {
				t.Fatalf("Load() error: %v", err)
			}

			got := cfg.GetEffectiveSafetyLevel()
			if got != tc.wantLevel {
				t.Errorf("GetEffectiveSafetyLevel() = %q, want %q", got, tc.wantLevel)
			}
		})
	}
}

// TestBuildGlobalBindings_SharedTokenRefTakesStrictestLevelAndAllHosts pins the
// multi-binding case: when several global contexts share one token-ref, the
// clamp ceiling must not depend on YAML ordering, and every bound host must
// stay acceptable to the origin check.
func TestBuildGlobalBindings_SharedTokenRefTakesStrictestLevelAndAllHosts(t *testing.T) {
	// NOT parallel: XDG_CONFIG_HOME is process-global.
	for _, order := range []struct {
		name  string
		first string
	}{
		{name: "permissive context first", first: "readwrite-all"},
		{name: "restrictive context first", first: "readonly"},
	} {
		t.Run(order.name, func(t *testing.T) {
			second := "readonly"
			if order.first == "readonly" {
				second = "readwrite-all"
			}

			xdgDir := filepath.Join(t.TempDir(), "xdg")
			globalDir := filepath.Join(xdgDir, "dtctl")
			if err := os.MkdirAll(globalDir, 0700); err != nil {
				t.Fatalf("mkdir: %v", err)
			}
			globalCfg := `apiVersion: v1
kind: Config
current-context: a
contexts:
  - name: a
    context:
      environment: https://one.dt.com
      token-ref: shared
      safety-level: ` + order.first + `
  - name: b
    context:
      environment: https://two.dt.com
      token-ref: shared
      safety-level: ` + second + `
`
			if err := os.WriteFile(filepath.Join(globalDir, "config"), []byte(globalCfg), 0600); err != nil {
				t.Fatalf("write global config: %v", err)
			}
			t.Setenv("XDG_CONFIG_HOME", xdgDir)
			xdg.Reload()
			defer xdg.Reload()

			binding, ok := buildGlobalBindings()["shared"]
			if !ok {
				t.Fatal("token-ref \"shared\" has no binding")
			}
			if binding.level != SafetyLevelReadOnly {
				t.Errorf("level = %q, want %q (strictest of the two)", binding.level, SafetyLevelReadOnly)
			}
			for _, host := range []string{"one.dt.com", "two.dt.com"} {
				if !binding.allows(host) {
					t.Errorf("allows(%q) = false, want true; hosts = %v", host, binding.hosts)
				}
			}
		})
	}
}

// TestGetToken_UnboundLocalTokenRefIsRefused is the premise the "unbound
// token-ref" clamp case relies on: such a context cannot reach an API at all.
func TestGetToken_UnboundLocalTokenRefIsRefused(t *testing.T) {
	cfg := newLocalConfig(t, "https://abc.apps.dynatrace.com", "unbound-token",
		map[string]string{"other-token": "abc.apps.dynatrace.com"})

	if _, err := cfg.GetToken("unbound-token"); err == nil {
		t.Fatal("GetToken() succeeded for an unbound token-ref, want an error")
	} else if !strings.Contains(err.Error(), "not bound to any environment") {
		t.Errorf("GetToken() error = %v, want a not-bound error", err)
	}
}

// TestConfig_GetEffectiveSafetyLevel_ClampFollowsContextOverride verifies the
// clamp is resolved from the context in effect, not from the one named in the
// local file at load time — --context / DTCTL_CONTEXT rewrite CurrentContext
// after Load().
func TestConfig_GetEffectiveSafetyLevel_ClampFollowsContextOverride(t *testing.T) {
	// NOT parallel: os.Chdir and XDG_CONFIG_HOME are process-global.
	tmpDir := t.TempDir()
	projectDir := t.TempDir()

	xdgDir := filepath.Join(tmpDir, "xdg")
	globalDir := filepath.Join(xdgDir, "dtctl")
	if err := os.MkdirAll(globalDir, 0700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	globalCfg := `apiVersion: v1
kind: Config
current-context: safe
contexts:
  - name: safe
    context:
      environment: https://global.dt.com
      token-ref: prod-token
      safety-level: readonly
`
	if err := os.WriteFile(filepath.Join(globalDir, "config"), []byte(globalCfg), 0600); err != nil {
		t.Fatalf("write global config: %v", err)
	}

	t.Setenv("XDG_CONFIG_HOME", xdgDir)
	xdg.Reload()
	defer xdg.Reload()

	// The first context carries no token-ref; the override target borrows the
	// globally bound one and tries to escalate.
	localCfg := `apiVersion: v1
kind: Config
current-context: unbound
contexts:
  - name: unbound
    context:
      environment: https://global.dt.com
      safety-level: readwrite-all
  - name: escalated
    context:
      environment: https://global.dt.com
      token-ref: prod-token
      safety-level: dangerously-unrestricted
`
	if err := os.WriteFile(filepath.Join(projectDir, LocalConfigName), []byte(localCfg), 0600); err != nil {
		t.Fatalf("write local config: %v", err)
	}

	origWd, _ := os.Getwd()
	defer func() { _ = os.Chdir(origWd) }()
	if err := os.Chdir(projectDir); err != nil {
		t.Fatalf("chdir: %v", err)
	}

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}

	cfg.CurrentContext = "escalated"
	if got := cfg.GetEffectiveSafetyLevel(); got != SafetyLevelReadOnly {
		t.Errorf("GetEffectiveSafetyLevel() after override = %q, want %q", got, SafetyLevelReadOnly)
	}
}

// TestLoad_LocalConfigRoundTripPreservesExecKeys is the regression guard for
// the data-loss bug: loading a local config (which ignores exec keys) and
// saving it back — as `dtctl config set`, `alias set`, `migrate-tokens`, etc.
// do — must NOT wipe the user's own aliases or hooks from their file.
func TestLoad_LocalConfigRoundTripPreservesExecKeys(t *testing.T) {
	// NOT parallel: os.Chdir is process-global and races with other tests.
	tmpDir, err := os.MkdirTemp("", "dtctl-roundtrip-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	localConfigPath := filepath.Join(tmpDir, LocalConfigName)
	localContent := `apiVersion: v1
kind: Config
current-context: local-ctx
contexts:
  - name: local-ctx
    context:
      environment: https://local.dt.com
      token-ref: local-token
      hooks:
        pre-apply: "echo ctx-hook"
preferences:
  hooks:
    pre-apply: "echo global-hook"
aliases:
  wf: "get workflows"
  prod: "get workflows --context prod"
`
	if err := os.WriteFile(localConfigPath, []byte(localContent), 0600); err != nil {
		t.Fatalf("Failed to write local config: %v", err)
	}

	origWd, err := os.Getwd()
	if err != nil {
		t.Fatalf("Failed to get working directory: %v", err)
	}
	defer func() { _ = os.Chdir(origWd) }()
	if err := os.Chdir(tmpDir); err != nil {
		t.Fatalf("Failed to change directory: %v", err)
	}

	// Simulate an unrelated config-management write: load, mutate something
	// else, save back to the same local file.
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	cfg.Preferences.Editor = "vim"
	if err := cfg.SaveTo(localConfigPath); err != nil {
		t.Fatalf("SaveTo() error = %v", err)
	}

	// Reload raw from disk and confirm nothing was destroyed.
	raw, err := LoadFromWithoutExpansion(localConfigPath)
	if err != nil {
		t.Fatalf("LoadFromWithoutExpansion() error = %v", err)
	}
	if len(raw.Aliases) != 2 {
		t.Errorf("aliases after round-trip = %v, want both preserved", raw.Aliases)
	}
	if raw.Preferences.Hooks.PreApply != "echo global-hook" {
		t.Errorf("global pre-apply hook after round-trip = %q, want preserved", raw.Preferences.Hooks.PreApply)
	}
	if len(raw.Contexts) != 1 || raw.Contexts[0].Context.Hooks.PreApply != "echo ctx-hook" {
		t.Errorf("context pre-apply hook after round-trip not preserved: %+v", raw.Contexts)
	}
}

// TestLoad_LocalConfigNoExecKeys verifies that a clean local config (no aliases
// or hooks) loads without being flagged as carrying ignored exec keys.
func TestLoad_LocalConfigNoExecKeys(t *testing.T) {
	// NOT parallel: os.Chdir is process-global and races with other tests.
	tmpDir, err := os.MkdirTemp("", "dtctl-no-exec-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	localConfigPath := filepath.Join(tmpDir, LocalConfigName)
	localContent := `apiVersion: v1
kind: Config
current-context: local-ctx
contexts:
  - name: local-ctx
    context:
      environment: https://local.dt.com
      token-ref: local-token
`
	if err := os.WriteFile(localConfigPath, []byte(localContent), 0600); err != nil {
		t.Fatalf("Failed to write local config: %v", err)
	}

	origWd, err := os.Getwd()
	if err != nil {
		t.Fatalf("Failed to get working directory: %v", err)
	}
	defer func() { _ = os.Chdir(origWd) }()

	if err := os.Chdir(tmpDir); err != nil {
		t.Fatalf("Failed to change directory: %v", err)
	}

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	if !cfg.IsLocal() {
		t.Error("IsLocal() = false, want true")
	}
	if cfg.IgnoredExecKeys() {
		t.Error("IgnoredExecKeys() = true, want false (no exec keys present)")
	}
}

// TestLoadFrom_ExplicitConfigKeepsExecKeys verifies that an explicit config
// path (e.g. --config) is trusted and honors aliases/hooks; only
// auto-discovered local configs have their exec keys ignored.
func TestLoadFrom_ExplicitConfigKeepsExecKeys(t *testing.T) {
	t.Parallel()
	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "explicit-config.yaml")
	content := `apiVersion: v1
kind: Config
aliases:
  wf: "get workflows"
preferences:
  hooks:
    pre-apply: "bash validate.sh"
`
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		t.Fatalf("Failed to write config: %v", err)
	}

	cfg, err := LoadFrom(path)
	if err != nil {
		t.Fatalf("LoadFrom() error = %v", err)
	}

	if cfg.IsLocal() {
		t.Error("IsLocal() = true, want false for explicit config path")
	}
	if cfg.IgnoredExecKeys() {
		t.Error("IgnoredExecKeys() = true, want false for explicit config path")
	}
	if _, ok := cfg.GetAlias("wf"); !ok {
		t.Error("GetAlias(wf) missing; explicit config must retain aliases")
	}
	if cfg.Preferences.Hooks.PreApply != "bash validate.sh" {
		t.Errorf("PreApply hook = %q, want retained", cfg.Preferences.Hooks.PreApply)
	}
}

// TestLoad_EnvConfigIsTrusted verifies that a config named by DTCTL_CONFIG is
// treated as an explicit, trusted config (like --config): its aliases and apply
// hooks are honored and it is not flagged as local. This is the kb-run /
// prepared-workspace use case where an agent runs `dtctl apply` without a flag.
func TestLoad_EnvConfigIsTrusted(t *testing.T) {
	// NOT parallel: os.Chdir + os.Setenv are process-global.
	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "workspace-config.yaml")
	content := `apiVersion: v1
kind: Config
current-context: ws-ctx
contexts:
  - name: ws-ctx
    context:
      environment: https://ws.dt.com
      token-ref: ws-token
preferences:
  hooks:
    pre-apply: "bash validate.sh"
    post-apply: "echo done"
aliases:
  wf: "get workflows"
`
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		t.Fatalf("Failed to write config: %v", err)
	}

	// Run from an empty dir so no local .dtctl.yaml can be auto-discovered.
	emptyDir := t.TempDir()
	origWd, err := os.Getwd()
	if err != nil {
		t.Fatalf("Failed to get working directory: %v", err)
	}
	defer func() { _ = os.Chdir(origWd) }()
	if err := os.Chdir(emptyDir); err != nil {
		t.Fatalf("Failed to change directory: %v", err)
	}

	t.Setenv(EnvConfig, path)

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	if cfg.IsLocal() {
		t.Error("IsLocal() = true, want false for DTCTL_CONFIG-named config")
	}
	if cfg.IgnoredExecKeys() {
		t.Error("IgnoredExecKeys() = true, want false for trusted env config")
	}
	if cfg.GetPreApplyHook() != "bash validate.sh" {
		t.Errorf("GetPreApplyHook() = %q, want honored", cfg.GetPreApplyHook())
	}
	if cfg.GetPostApplyHook() != "echo done" {
		t.Errorf("GetPostApplyHook() = %q, want honored", cfg.GetPostApplyHook())
	}
	if _, ok := cfg.GetAlias("wf"); !ok {
		t.Error("GetAlias(wf) missing; env config must honor aliases")
	}
}

// TestLoad_EnvConfigShadowsLocalDiscovery verifies that when DTCTL_CONFIG is
// set, auto-discovery is skipped entirely: a closer (untrusted) .dtctl.yaml in
// the working directory can never shadow or downgrade the explicitly named
// config. This is the core security property distinguishing DTCTL_CONFIG from a
// blanket "trust discovered configs" switch.
func TestLoad_EnvConfigShadowsLocalDiscovery(t *testing.T) {
	// NOT parallel: os.Chdir + os.Setenv are process-global.
	trustedDir := t.TempDir()
	trustedPath := filepath.Join(trustedDir, "trusted.yaml")
	trusted := `apiVersion: v1
kind: Config
current-context: trusted-ctx
contexts:
  - name: trusted-ctx
    context:
      environment: https://trusted.dt.com
      token-ref: t
preferences:
  hooks:
    pre-apply: "echo trusted-hook"
`
	if err := os.WriteFile(trustedPath, []byte(trusted), 0600); err != nil {
		t.Fatalf("Failed to write trusted config: %v", err)
	}

	// A hostile local config sitting in the working directory.
	workDir := t.TempDir()
	localPath := filepath.Join(workDir, LocalConfigName)
	local := `apiVersion: v1
kind: Config
current-context: evil-ctx
contexts:
  - name: evil-ctx
    context:
      environment: https://evil.dt.com
      token-ref: t
preferences:
  hooks:
    pre-apply: "bash -c 'id > /tmp/pwned'"
`
	if err := os.WriteFile(localPath, []byte(local), 0600); err != nil {
		t.Fatalf("Failed to write local config: %v", err)
	}

	origWd, err := os.Getwd()
	if err != nil {
		t.Fatalf("Failed to get working directory: %v", err)
	}
	defer func() { _ = os.Chdir(origWd) }()
	if err := os.Chdir(workDir); err != nil {
		t.Fatalf("Failed to change directory: %v", err)
	}

	t.Setenv(EnvConfig, trustedPath)

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	if cfg.IsLocal() {
		t.Error("IsLocal() = true, want false: DTCTL_CONFIG must bypass discovery")
	}
	if cfg.CurrentContext != "trusted-ctx" {
		t.Errorf("CurrentContext = %q, want trusted-ctx (local config must not be loaded)", cfg.CurrentContext)
	}
	if got := cfg.GetPreApplyHook(); got != "echo trusted-hook" {
		t.Errorf("GetPreApplyHook() = %q, want the trusted hook (never the local one)", got)
	}
}

// TestLoad_EnvConfigMissingFileErrors verifies that DTCTL_CONFIG pointing at a
// nonexistent file fails closed rather than silently falling back to
// (untrusted) auto-discovery, which would be a surprising security downgrade.
func TestLoad_EnvConfigMissingFileErrors(t *testing.T) {
	// NOT parallel: os.Setenv is process-global.
	missing := filepath.Join(t.TempDir(), "does-not-exist.yaml")
	t.Setenv(EnvConfig, missing)

	if _, err := Load(); err == nil {
		t.Error("Load() error = nil, want error for missing DTCTL_CONFIG file")
	}
}

func TestConfig_DeleteContext(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name        string
		setup       func() *Config
		contextName string
		wantErr     bool
		wantCount   int
	}{
		{
			name: "delete existing context",
			setup: func() *Config {
				cfg := NewConfig()
				cfg.SetContext("dev", "https://dev.dt.com", "dev-token")
				cfg.SetContext("prod", "https://prod.dt.com", "prod-token")
				return cfg
			},
			contextName: "dev",
			wantErr:     false,
			wantCount:   1,
		},
		{
			name: "delete non-existing context",
			setup: func() *Config {
				cfg := NewConfig()
				cfg.SetContext("dev", "https://dev.dt.com", "dev-token")
				return cfg
			},
			contextName: "nonexistent",
			wantErr:     true,
			wantCount:   1,
		},
		{
			name: "delete only context",
			setup: func() *Config {
				cfg := NewConfig()
				cfg.SetContext("only", "https://only.dt.com", "only-token")
				return cfg
			},
			contextName: "only",
			wantErr:     false,
			wantCount:   0,
		},
		{
			name:        "delete from empty config",
			setup:       NewConfig,
			contextName: "any",
			wantErr:     true,
			wantCount:   0,
		},
		{
			name: "delete middle context",
			setup: func() *Config {
				cfg := NewConfig()
				cfg.SetContext("first", "https://first.dt.com", "first-token")
				cfg.SetContext("middle", "https://middle.dt.com", "middle-token")
				cfg.SetContext("last", "https://last.dt.com", "last-token")
				return cfg
			},
			contextName: "middle",
			wantErr:     false,
			wantCount:   2,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			cfg := tt.setup()
			err := cfg.DeleteContext(tt.contextName)

			if (err != nil) != tt.wantErr {
				t.Errorf("DeleteContext() error = %v, wantErr %v", err, tt.wantErr)
				return
			}

			if len(cfg.Contexts) != tt.wantCount {
				t.Errorf("After DeleteContext(), context count = %d, want %d", len(cfg.Contexts), tt.wantCount)
			}

			// Verify the deleted context is actually gone
			if !tt.wantErr {
				for _, nc := range cfg.Contexts {
					if nc.Name == tt.contextName {
						t.Errorf("Context %q should have been deleted but still exists", tt.contextName)
					}
				}
			}
		})
	}
}

func TestLoadFrom_EdgeCases(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name        string
		fileContent string
		wantErr     bool
		errContains string
	}{
		{
			name:        "empty file",
			fileContent: "",
			wantErr:     false, // YAML can unmarshal empty file
		},
		{
			name:        "invalid YAML syntax",
			fileContent: "invalid: yaml: [unclosed",
			wantErr:     true,
			errContains: "failed to parse config file",
		},
		{
			name: "minimal valid config",
			fileContent: `apiVersion: v1
kind: Config`,
			wantErr: false,
		},
		{
			name:        "config with tabs",
			fileContent: "apiVersion: v1\nkind: Config\ncurrent-context:\ttest",
			wantErr:     false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			tmpDir, err := os.MkdirTemp("", "dtctl-loadfrom-*")
			if err != nil {
				t.Fatalf("Failed to create temp dir: %v", err)
			}
			defer os.RemoveAll(tmpDir)

			configPath := filepath.Join(tmpDir, "config")
			if err := os.WriteFile(configPath, []byte(tt.fileContent), 0600); err != nil {
				t.Fatalf("Failed to write config: %v", err)
			}

			_, err = LoadFrom(configPath)
			if (err != nil) != tt.wantErr {
				t.Errorf("LoadFrom() error = %v, wantErr %v", err, tt.wantErr)
			}

			if tt.wantErr && tt.errContains != "" {
				if err == nil || !strings.Contains(err.Error(), tt.errContains) {
					t.Errorf("LoadFrom() error = %v, want error containing %q", err, tt.errContains)
				}
			}
		})
	}
}

func TestLoadFrom_ReadError(t *testing.T) {
	t.Parallel()
	// Test error when reading a directory instead of a file
	tmpDir, err := os.MkdirTemp("", "dtctl-readerror-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	_, err = LoadFrom(tmpDir)
	if err == nil {
		t.Error("LoadFrom() on directory should return error")
	}
	if !strings.Contains(err.Error(), "failed to read config file") {
		t.Errorf("LoadFrom() error = %v, want error containing 'failed to read config file'", err)
	}
}

func TestConfig_GetToken_KeyringFallback(t *testing.T) {
	t.Parallel()
	cfg := NewConfig()

	// Add token with empty value (simulating keyring migration)
	cfg.Tokens = append(cfg.Tokens, NamedToken{
		Name:  "migrated-token",
		Token: "", // Empty = stored in keyring
	})

	// If keyring is not available, should get specific error
	_, err := cfg.GetToken("migrated-token")
	if err == nil {
		// Either keyring is available and returned token, or should have error
		t.Log("Keyring available, token retrieved successfully")
	} else if !strings.Contains(err.Error(), "not found in keyring") {
		t.Errorf("GetToken() error = %v, want error about keyring", err)
	}
}

func TestConfig_SetContextWithOptions_EmptyTokenRef(t *testing.T) {
	t.Parallel()
	cfg := NewConfig()

	// Create context with initial token
	cfg.SetContextWithOptions("test", "https://test.dt.com", "initial-token", nil)

	if cfg.Contexts[0].Context.TokenRef != "initial-token" {
		t.Errorf("Initial TokenRef = %v, want 'initial-token'", cfg.Contexts[0].Context.TokenRef)
	}

	// Update with empty token ref (should keep existing)
	opts := &ContextOptions{
		Description: "Updated description",
	}
	cfg.SetContextWithOptions("test", "https://test2.dt.com", "", opts)

	if cfg.Contexts[0].Context.TokenRef != "initial-token" {
		t.Errorf("After update with empty tokenRef, TokenRef = %v, want 'initial-token'", cfg.Contexts[0].Context.TokenRef)
	}
	if cfg.Contexts[0].Context.Environment != "https://test2.dt.com" {
		t.Errorf("Environment not updated = %v", cfg.Contexts[0].Context.Environment)
	}
}

func TestConfig_SetContextWithOptions_PartialUpdate(t *testing.T) {
	t.Parallel()
	cfg := NewConfig()

	// Create context with all fields
	opts := &ContextOptions{
		SafetyLevel: SafetyLevelReadOnly,
		Description: "Initial description",
	}
	cfg.SetContextWithOptions("prod", "https://prod.dt.com", "prod-token", opts)

	// Update only safety level
	opts2 := &ContextOptions{
		SafetyLevel: SafetyLevelReadWriteAll,
	}
	cfg.SetContextWithOptions("prod", "https://prod2.dt.com", "", opts2)

	ctx := cfg.Contexts[0].Context
	if ctx.SafetyLevel != SafetyLevelReadWriteAll {
		t.Errorf("SafetyLevel = %v, want %v", ctx.SafetyLevel, SafetyLevelReadWriteAll)
	}
	if ctx.Description != "Initial description" {
		t.Errorf("Description changed unexpectedly to %v", ctx.Description)
	}

	// Update only description
	opts3 := &ContextOptions{
		Description: "New description",
	}
	cfg.SetContextWithOptions("prod", "https://prod3.dt.com", "", opts3)

	ctx = cfg.Contexts[0].Context
	if ctx.SafetyLevel != SafetyLevelReadWriteAll {
		t.Errorf("SafetyLevel changed unexpectedly to %v", ctx.SafetyLevel)
	}
	if ctx.Description != "New description" {
		t.Errorf("Description = %v, want 'New description'", ctx.Description)
	}
}

func TestSaveTo_MarshalError(t *testing.T) {
	t.Parallel()
	// This is difficult to test as yaml.Marshal rarely fails with valid Go structs
	// We test the directory creation error path instead
	cfg := NewConfig()

	// Try to save to a path where we can't create the directory
	// Using root directory should fail on most systems without sudo
	err := cfg.SaveTo("/root/impossible/path/config")
	if err == nil {
		t.Log("Warning: Expected permission error when saving to /root, but succeeded")
	} else if !strings.Contains(err.Error(), "failed to create config directory") &&
		!strings.Contains(err.Error(), "failed to write config file") {
		t.Errorf("SaveTo() error = %v, want error about directory creation or file write", err)
	}
}

func TestConfig_SetToken_UpdateExisting(t *testing.T) {
	t.Parallel()
	cfg := NewConfig()

	// Add initial token
	if err := cfg.SetToken("my-token", "initial-value"); err != nil {
		t.Fatalf("SetToken() error = %v", err)
	}

	initialCount := len(cfg.Tokens)

	// Update existing token
	if err := cfg.SetToken("my-token", "updated-value"); err != nil {
		t.Fatalf("SetToken() update error = %v", err)
	}

	// Should not add a new token entry
	if len(cfg.Tokens) != initialCount {
		t.Errorf("SetToken() added duplicate, count = %d, want %d", len(cfg.Tokens), initialCount)
	}

	// Find the token
	found := false
	for _, nt := range cfg.Tokens {
		if nt.Name == "my-token" {
			found = true
			// If keyring not available, should have the new value
			if !IsKeyringAvailable() && nt.Token != "updated-value" {
				t.Errorf("Token value = %q, want 'updated-value'", nt.Token)
			}
			break
		}
	}

	if !found {
		t.Error("Updated token not found in config")
	}
}

func TestConfig_SetToken_InvalidatesOAuthCache(t *testing.T) {
	t.Parallel()

	kr := newMockKeyring()
	// Seed cached OAuth tokens for three environments.
	kr.data["oauth:prod:rotated-token"] = `{"access_token":"old-access","refresh_token":"stale-refresh"}`
	kr.data["oauth:dev:rotated-token"] = `{"access_token":"old-dev","refresh_token":"stale-dev"}`
	kr.data["oauth:hard:rotated-token"] = `{"access_token":"old-hard","refresh_token":"stale-hard"}`

	cfg := NewConfig()
	if err := cfg.setTokenWithKeyring("rotated-token", "brand-new-platform-token", kr, nil); err != nil {
		t.Fatalf("setTokenWithKeyring() error = %v", err)
	}

	// All cached OAuth entries must be gone.
	for _, key := range []string{"oauth:prod:rotated-token", "oauth:dev:rotated-token", "oauth:hard:rotated-token"} {
		if _, ok := kr.data[key]; ok {
			t.Errorf("OAuth cache entry %q still exists after setTokenWithKeyring", key)
		}
	}

	// The new platform token must be stored.
	if got := kr.data["rotated-token"]; got != "brand-new-platform-token" {
		t.Errorf("platform token = %q, want %q", got, "brand-new-platform-token")
	}

	// Config entry should be empty (reference only, value lives in keyring).
	if len(cfg.Tokens) != 1 || cfg.Tokens[0].Token != "" {
		t.Errorf("config token should be empty reference, got %+v", cfg.Tokens)
	}
}

func TestConfig_SetToken_InvalidatesOAuthCache_DynamicEnv(t *testing.T) {
	t.Parallel()

	kr := newMockKeyring()
	// Seed a cached OAuth token for the "prod" environment.
	kr.data["oauth:prod:dyn-token"] = `{"refresh_token":"stale"}`

	cfg := NewConfig()
	// Add a context whose URL maps to "prod" so oauthKeyringNames discovers it dynamically.
	cfg.Contexts = []NamedContext{
		{Name: "my-env", Context: Context{
			Environment: "https://abc12345.apps.dynatrace.com",
			TokenRef:    "dyn-token",
		}},
	}

	if err := cfg.setTokenWithKeyring("dyn-token", "new-value", kr, nil); err != nil {
		t.Fatalf("setTokenWithKeyring() error = %v", err)
	}

	if _, ok := kr.data["oauth:prod:dyn-token"]; ok {
		t.Error("dynamically-discovered OAuth cache entry still exists after setTokenWithKeyring")
	}
}

func TestConfig_SetToken_InvalidatesOAuthFileCache(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	fileStore := NewOAuthFileStoreWithDir(dir)

	// Seed file-based OAuth cache entries.
	for _, key := range []string{"oauth:prod:file-tok", "oauth:dev:file-tok", "oauth:hard:file-tok"} {
		if err := fileStore.SetToken(key, `{"refresh_token":"stale"}`); err != nil {
			t.Fatalf("seed file store %s: %v", key, err)
		}
	}

	// Use a mock keyring that reports unavailable so the file-store path is
	// the only cache backend (simulates headless/WSL).
	kr := &unavailableKeyring{}

	cfg := NewConfig()
	if err := cfg.setTokenWithKeyring("file-tok", "new-platform-token", kr, fileStore); err != nil {
		t.Fatalf("setTokenWithKeyring() error = %v", err)
	}

	// All file-based OAuth entries must be gone.
	for _, key := range []string{"oauth:prod:file-tok", "oauth:dev:file-tok", "oauth:hard:file-tok"} {
		if tok, err := fileStore.GetToken(key); err == nil {
			t.Errorf("file OAuth cache entry %q still exists: %s", key, tok)
		}
	}

	// Token should be stored in config (keyring unavailable).
	if len(cfg.Tokens) != 1 || cfg.Tokens[0].Token != "new-platform-token" {
		t.Errorf("config token = %+v, want token in config (keyring unavailable)", cfg.Tokens)
	}
}

func TestConfig_SetToken_InvalidatesBothKeyringAndFileCache(t *testing.T) {
	t.Parallel()

	kr := newMockKeyring()
	kr.data["oauth:prod:both-tok"] = `{"refresh_token":"kr-stale"}`

	dir := t.TempDir()
	fileStore := NewOAuthFileStoreWithDir(dir)
	if err := fileStore.SetToken("oauth:prod:both-tok", `{"refresh_token":"file-stale"}`); err != nil {
		t.Fatalf("seed file store: %v", err)
	}

	cfg := NewConfig()
	if err := cfg.setTokenWithKeyring("both-tok", "fresh-token", kr, fileStore); err != nil {
		t.Fatalf("setTokenWithKeyring() error = %v", err)
	}

	// Keyring OAuth cache must be cleared.
	if _, ok := kr.data["oauth:prod:both-tok"]; ok {
		t.Error("keyring OAuth cache entry still exists")
	}

	// File OAuth cache must be cleared.
	if tok, err := fileStore.GetToken("oauth:prod:both-tok"); err == nil {
		t.Errorf("file OAuth cache entry still exists: %s", tok)
	}

	// Platform token stored in keyring.
	if got := kr.data["both-tok"]; got != "fresh-token" {
		t.Errorf("keyring platform token = %q, want %q", got, "fresh-token")
	}
}

func TestLoadFrom_EnvironmentVariableExpansion(t *testing.T) {
	tests := []struct {
		name          string
		configContent string
		envVars       map[string]string
		wantEnv       string
		wantToken     string
		wantSafetyLvl string
		wantErr       bool
	}{
		{
			name: "expand single environment variable",
			configContent: `apiVersion: dtctl.io/v1
kind: Config
current-context: test
contexts:
  - name: test
    context:
      environment: ${TEST_ENV_URL}
      token-ref: test-token
      safety-level: readonly
tokens:
  - name: test-token
    token: test-value`,
			envVars: map[string]string{
				"TEST_ENV_URL": "https://test.dynatrace.com",
			},
			wantEnv:       "https://test.dynatrace.com",
			wantToken:     "test-value",
			wantSafetyLvl: "readonly",
		},
		{
			name: "expand multiple environment variables",
			configContent: `apiVersion: dtctl.io/v1
kind: Config
current-context: test
contexts:
  - name: test
    context:
      environment: ${DT_ENVIRONMENT_URL}
      token-ref: test-token
      safety-level: ${DT_SAFETY_LEVEL}
tokens:
  - name: test-token
    token: ${DT_API_TOKEN}`,
			envVars: map[string]string{
				"DT_ENVIRONMENT_URL": "https://abc123.apps.dynatrace.com",
				"DT_API_TOKEN":       "dt0s16.SECRET_TOKEN",
				"DT_SAFETY_LEVEL":    "readwrite-all",
			},
			wantEnv:       "https://abc123.apps.dynatrace.com",
			wantToken:     "dt0s16.SECRET_TOKEN",
			wantSafetyLvl: "readwrite-all",
		},
		{
			name: "undefined environment variable becomes empty",
			configContent: `apiVersion: dtctl.io/v1
kind: Config
current-context: test
contexts:
  - name: test
    context:
      environment: ${UNDEFINED_VAR}
      token-ref: test-token
tokens:
  - name: test-token
    token: static-value`,
			envVars:       map[string]string{},
			wantEnv:       "",
			wantToken:     "static-value",
			wantSafetyLvl: "readwrite-all", // default
		},
		{
			name: "mixed static and dynamic values",
			configContent: `apiVersion: dtctl.io/v1
kind: Config
current-context: test
contexts:
  - name: test
    context:
      environment: https://static.dynatrace.com
      token-ref: test-token
      safety-level: readonly
tokens:
  - name: test-token
    token: ${DT_API_TOKEN}`,
			envVars: map[string]string{
				"DT_API_TOKEN": "dt0s16.DYNAMIC_TOKEN",
			},
			wantEnv:       "https://static.dynatrace.com",
			wantToken:     "dt0s16.DYNAMIC_TOKEN",
			wantSafetyLvl: "readonly",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Set up environment variables
			for k, v := range tt.envVars {
				os.Setenv(k, v)
				defer os.Unsetenv(k)
			}

			// Create temporary config file
			tmpDir := t.TempDir()
			configPath := filepath.Join(tmpDir, "config")
			if err := os.WriteFile(configPath, []byte(tt.configContent), 0600); err != nil {
				t.Fatalf("Failed to write test config: %v", err)
			}

			// Load config
			cfg, err := LoadFrom(configPath)
			if (err != nil) != tt.wantErr {
				t.Errorf("LoadFrom() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if tt.wantErr {
				return
			}

			// Verify environment variable was expanded
			if len(cfg.Contexts) == 0 {
				t.Fatal("Expected at least one context")
			}
			if got := cfg.Contexts[0].Context.Environment; got != tt.wantEnv {
				t.Errorf("Environment = %q, want %q", got, tt.wantEnv)
			}

			// Verify token was expanded
			if len(cfg.Tokens) > 0 {
				if got := cfg.Tokens[0].Token; got != tt.wantToken {
					t.Errorf("Token = %q, want %q", got, tt.wantToken)
				}
			}

			// Verify safety level
			if got := cfg.Contexts[0].Context.SafetyLevel.String(); got != tt.wantSafetyLvl {
				t.Errorf("SafetyLevel = %q, want %q", got, tt.wantSafetyLvl)
			}
		})
	}
}

func TestGetPreApplyHook_ContextOverridesGlobal(t *testing.T) {
	cfg := &Config{
		Preferences: Preferences{
			Hooks: Hooks{PreApply: "global-hook"},
		},
		CurrentContext: "prod",
		Contexts: []NamedContext{{
			Name: "prod",
			Context: Context{
				Environment: "https://prod.example.invalid",
				TokenRef:    "prod-token",
				Hooks:       Hooks{PreApply: "prod-hook"},
			},
		}},
	}
	got := cfg.GetPreApplyHook()
	if got != "prod-hook" {
		t.Errorf("GetPreApplyHook() = %q, want %q", got, "prod-hook")
	}
}

func TestGetPreApplyHook_FallsBackToGlobal(t *testing.T) {
	cfg := &Config{
		Preferences: Preferences{
			Hooks: Hooks{PreApply: "global-hook"},
		},
		CurrentContext: "dev",
		Contexts: []NamedContext{{
			Name: "dev",
			Context: Context{
				Environment: "https://dev.example.invalid",
				TokenRef:    "dev-token",
			},
		}},
	}
	got := cfg.GetPreApplyHook()
	if got != "global-hook" {
		t.Errorf("GetPreApplyHook() = %q, want %q", got, "global-hook")
	}
}

func TestGetPreApplyHook_NoneDisablesGlobal(t *testing.T) {
	cfg := &Config{
		Preferences: Preferences{
			Hooks: Hooks{PreApply: "global-hook"},
		},
		CurrentContext: "dev",
		Contexts: []NamedContext{{
			Name: "dev",
			Context: Context{
				Environment: "https://dev.example.invalid",
				TokenRef:    "dev-token",
				Hooks:       Hooks{PreApply: "none"},
			},
		}},
	}
	got := cfg.GetPreApplyHook()
	if got != "" {
		t.Errorf("GetPreApplyHook() = %q, want empty (none should disable global hook)", got)
	}
}

func TestGetPreApplyHook_NoHookConfigured(t *testing.T) {
	cfg := &Config{
		CurrentContext: "dev",
		Contexts: []NamedContext{{
			Name: "dev",
			Context: Context{
				Environment: "https://dev.example.invalid",
				TokenRef:    "dev-token",
			},
		}},
	}
	got := cfg.GetPreApplyHook()
	if got != "" {
		t.Errorf("GetPreApplyHook() = %q, want empty", got)
	}
}

func TestGetPreApplyHook_NoCurrentContext(t *testing.T) {
	// When no current context is set, fall back to global
	cfg := &Config{
		Preferences: Preferences{
			Hooks: Hooks{PreApply: "global-hook"},
		},
	}
	got := cfg.GetPreApplyHook()
	if got != "global-hook" {
		t.Errorf("GetPreApplyHook() = %q, want %q", got, "global-hook")
	}
}

func TestGetPreApplyHook_CurrentContextPointsToNonexistent(t *testing.T) {
	// current-context points to a name that doesn't exist in contexts[]
	// Should fall back to global hook
	cfg := &Config{
		Preferences: Preferences{
			Hooks: Hooks{PreApply: "global-hook"},
		},
		CurrentContext: "nonexistent",
		Contexts:       []NamedContext{}, // empty — "nonexistent" won't be found
	}
	got := cfg.GetPreApplyHook()
	if got != "global-hook" {
		t.Errorf("GetPreApplyHook() = %q, want %q (should fall back to global)", got, "global-hook")
	}
}

func TestGetPreApplyHook_MultipleContextsOnlyOneHasHook(t *testing.T) {
	cfg := &Config{
		Preferences: Preferences{
			Hooks: Hooks{PreApply: "global-hook"},
		},
		CurrentContext: "staging",
		Contexts: []NamedContext{
			{
				Name: "dev",
				Context: Context{
					Environment: "https://dev.example.invalid",
					TokenRef:    "dev-token",
					Hooks:       Hooks{PreApply: "dev-hook"},
				},
			},
			{
				Name: "staging",
				Context: Context{
					Environment: "https://staging.example.invalid",
					TokenRef:    "staging-token",
					// No hook — should fall back to global
				},
			},
			{
				Name: "prod",
				Context: Context{
					Environment: "https://prod.example.invalid",
					TokenRef:    "prod-token",
					Hooks:       Hooks{PreApply: "prod-hook"},
				},
			},
		},
	}
	got := cfg.GetPreApplyHook()
	if got != "global-hook" {
		t.Errorf("GetPreApplyHook() = %q, want %q (staging has no hook, should use global)", got, "global-hook")
	}

	// Switch to dev context — should use dev-hook
	cfg.CurrentContext = "dev"
	got = cfg.GetPreApplyHook()
	if got != "dev-hook" {
		t.Errorf("GetPreApplyHook() = %q, want %q (dev context has hook)", got, "dev-hook")
	}

	// Switch to prod context — should use prod-hook
	cfg.CurrentContext = "prod"
	got = cfg.GetPreApplyHook()
	if got != "prod-hook" {
		t.Errorf("GetPreApplyHook() = %q, want %q (prod context has hook)", got, "prod-hook")
	}
}

func TestHooks_YAMLRoundTrip(t *testing.T) {
	cfg := &Config{
		APIVersion:     "v1",
		Kind:           "Config",
		CurrentContext: "test",
		Preferences: Preferences{
			Output: "table",
			Editor: "vim",
			Hooks:  Hooks{PreApply: "global-validate.sh"},
		},
		Contexts: []NamedContext{
			{
				Name: "test",
				Context: Context{
					Environment: "https://test.example.invalid",
					TokenRef:    "test-token",
					Hooks:       Hooks{PreApply: "context-validate.sh"},
				},
			},
		},
		Tokens: []NamedToken{
			{Name: "test-token", Token: "secret"},
		},
	}

	// Save to temp file
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config")
	if err := cfg.SaveTo(configPath); err != nil {
		t.Fatalf("SaveTo() error = %v", err)
	}

	// Load back
	loaded, err := LoadFrom(configPath)
	if err != nil {
		t.Fatalf("LoadFrom() error = %v", err)
	}

	// Verify global hook survived
	if loaded.Preferences.Hooks.PreApply != "global-validate.sh" {
		t.Errorf("Preferences.Hooks.PreApply = %q, want %q", loaded.Preferences.Hooks.PreApply, "global-validate.sh")
	}

	// Verify context hook survived
	if len(loaded.Contexts) != 1 {
		t.Fatalf("Contexts count = %d, want 1", len(loaded.Contexts))
	}
	if loaded.Contexts[0].Context.Hooks.PreApply != "context-validate.sh" {
		t.Errorf("Context.Hooks.PreApply = %q, want %q", loaded.Contexts[0].Context.Hooks.PreApply, "context-validate.sh")
	}

	// Verify GetPreApplyHook returns context hook (precedence)
	got := loaded.GetPreApplyHook()
	if got != "context-validate.sh" {
		t.Errorf("GetPreApplyHook() = %q, want %q", got, "context-validate.sh")
	}
}

func TestHooks_YAMLRoundTrip_NoneValue(t *testing.T) {
	cfg := &Config{
		APIVersion:     "v1",
		Kind:           "Config",
		CurrentContext: "test",
		Preferences: Preferences{
			Output: "table",
			Hooks:  Hooks{PreApply: "global-hook"},
		},
		Contexts: []NamedContext{
			{
				Name: "test",
				Context: Context{
					Environment: "https://test.example.invalid",
					TokenRef:    "test-token",
					Hooks:       Hooks{PreApply: "none"},
				},
			},
		},
		Tokens: []NamedToken{
			{Name: "test-token", Token: "secret"},
		},
	}

	// Save and reload
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config")
	if err := cfg.SaveTo(configPath); err != nil {
		t.Fatalf("SaveTo() error = %v", err)
	}
	loaded, err := LoadFrom(configPath)
	if err != nil {
		t.Fatalf("LoadFrom() error = %v", err)
	}

	// "none" should survive round-trip and disable global hook
	got := loaded.GetPreApplyHook()
	if got != "" {
		t.Errorf("GetPreApplyHook() = %q, want empty (none should disable global hook after round-trip)", got)
	}
}

func TestHooks_YAMLRoundTrip_EmptyHooks(t *testing.T) {
	// Config with no hooks at all should round-trip cleanly
	cfg := &Config{
		APIVersion:     "v1",
		Kind:           "Config",
		CurrentContext: "test",
		Preferences:    Preferences{Output: "table"},
		Contexts: []NamedContext{
			{
				Name: "test",
				Context: Context{
					Environment: "https://test.example.invalid",
					TokenRef:    "test-token",
				},
			},
		},
		Tokens: []NamedToken{
			{Name: "test-token", Token: "secret"},
		},
	}

	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config")
	if err := cfg.SaveTo(configPath); err != nil {
		t.Fatalf("SaveTo() error = %v", err)
	}
	loaded, err := LoadFrom(configPath)
	if err != nil {
		t.Fatalf("LoadFrom() error = %v", err)
	}

	got := loaded.GetPreApplyHook()
	if got != "" {
		t.Errorf("GetPreApplyHook() = %q, want empty (no hooks configured)", got)
	}
}

func TestGetPostApplyHook_ContextOverridesGlobal(t *testing.T) {
	cfg := &Config{
		Preferences: Preferences{
			Hooks: Hooks{PostApply: "global-post"},
		},
		CurrentContext: "prod",
		Contexts: []NamedContext{{
			Name: "prod",
			Context: Context{
				Environment: "https://prod.example.invalid",
				TokenRef:    "prod-token",
				Hooks:       Hooks{PostApply: "prod-post"},
			},
		}},
	}
	if got := cfg.GetPostApplyHook(); got != "prod-post" {
		t.Errorf("GetPostApplyHook() = %q, want %q", got, "prod-post")
	}
}

func TestGetPostApplyHook_FallsBackToGlobal(t *testing.T) {
	cfg := &Config{
		Preferences: Preferences{
			Hooks: Hooks{PostApply: "global-post"},
		},
		CurrentContext: "dev",
		Contexts: []NamedContext{{
			Name: "dev",
			Context: Context{
				Environment: "https://dev.example.invalid",
				TokenRef:    "dev-token",
			},
		}},
	}
	if got := cfg.GetPostApplyHook(); got != "global-post" {
		t.Errorf("GetPostApplyHook() = %q, want %q", got, "global-post")
	}
}

func TestGetPostApplyHook_NoneDisablesGlobal(t *testing.T) {
	cfg := &Config{
		Preferences: Preferences{
			Hooks: Hooks{PostApply: "global-post"},
		},
		CurrentContext: "dev",
		Contexts: []NamedContext{{
			Name: "dev",
			Context: Context{
				Environment: "https://dev.example.invalid",
				TokenRef:    "dev-token",
				Hooks:       Hooks{PostApply: "none"},
			},
		}},
	}
	if got := cfg.GetPostApplyHook(); got != "" {
		t.Errorf("GetPostApplyHook() = %q, want empty (none should disable)", got)
	}
}

func TestGetPostApplyHook_NoHookConfigured(t *testing.T) {
	cfg := &Config{
		CurrentContext: "dev",
		Contexts: []NamedContext{{
			Name: "dev",
			Context: Context{
				Environment: "https://dev.example.invalid",
				TokenRef:    "dev-token",
			},
		}},
	}
	if got := cfg.GetPostApplyHook(); got != "" {
		t.Errorf("GetPostApplyHook() = %q, want empty", got)
	}
}

func TestExpandEnvPreservingShellParams(t *testing.T) {
	t.Setenv("DTCTL_TEST_FOO", "expanded-foo")
	t.Setenv("DTCTL_TEST_BAR", "expanded-bar")

	tests := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "real env var ${VAR} is expanded",
			in:   "${DTCTL_TEST_FOO}/x",
			want: "expanded-foo/x",
		},
		{
			name: "real env var bare $VAR is expanded",
			in:   "$DTCTL_TEST_FOO/x",
			want: "expanded-foo/x",
		},
		{
			name: "shell positional $1 is preserved verbatim",
			in:   `bash validate.sh "$1" "$2"`,
			want: `bash validate.sh "$1" "$2"`,
		},
		{
			name: "two-digit positional ${10} is preserved",
			in:   `echo "${10}"`,
			want: `echo "${10}"`,
		},
		{
			name: "shell special $@ is preserved",
			in:   `forward "$@"`,
			want: `forward "$@"`,
		},
		{
			name: "shell special $? $$ $! are preserved",
			in:   `echo $? $$ $!`,
			want: `echo $? $$ $!`,
		},
		{
			name: "unset env var falls back to empty (matches os.ExpandEnv)",
			in:   "prefix-${DTCTL_TEST_DEFINITELY_UNSET_XYZ}-suffix",
			want: "prefix--suffix",
		},
		{
			name: "mixed real env + positional in same string",
			in:   `${DTCTL_TEST_FOO}/run.sh "$1" --bar="${DTCTL_TEST_BAR}"`,
			want: `expanded-foo/run.sh "$1" --bar="expanded-bar"`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := expandEnvPreservingShellParams(tt.in); got != tt.want {
				t.Errorf("expandEnvPreservingShellParams(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestConfig_PruneEmptyEnvironments(t *testing.T) {
	tests := []struct {
		name        string
		contexts    []NamedContext
		keepContext string
		wantNames   []string
	}{
		{
			name: "removes context with empty environment",
			contexts: []NamedContext{
				{Name: "placeholder", Context: Context{Environment: ""}},
				{Name: "real", Context: Context{Environment: "https://abc12345.apps.dynatrace.com/"}},
			},
			keepContext: "real",
			wantNames:   []string{"real"},
		},
		{
			name: "keeps all contexts with real environments",
			contexts: []NamedContext{
				{Name: "prod", Context: Context{Environment: "https://prod.apps.dynatrace.com/"}},
				{Name: "dev", Context: Context{Environment: "https://dev.apps.dynatracelabs.com/"}},
			},
			keepContext: "prod",
			wantNames:   []string{"prod", "dev"},
		},
		{
			name: "removes multiple placeholder contexts",
			contexts: []NamedContext{
				{Name: "my-environment", Context: Context{Environment: ""}},
				{Name: "another-placeholder", Context: Context{Environment: ""}},
				{Name: "real", Context: Context{Environment: "https://abc12345.apps.dynatrace.com/"}},
			},
			keepContext: "real",
			wantNames:   []string{"real"},
		},
		{
			name: "keepContext is never removed even with empty environment",
			contexts: []NamedContext{
				{Name: "new-ctx", Context: Context{Environment: ""}},
				{Name: "other", Context: Context{Environment: ""}},
			},
			keepContext: "new-ctx",
			wantNames:   []string{"new-ctx"},
		},
		{
			name:        "empty config is a no-op",
			contexts:    []NamedContext{},
			keepContext: "anything",
			wantNames:   []string{},
		},
		{
			name: "single non-empty keepContext is preserved",
			contexts: []NamedContext{
				{Name: "prod", Context: Context{Environment: "https://abc12345.apps.dynatrace.com/"}},
			},
			keepContext: "prod",
			wantNames:   []string{"prod"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := NewConfig()
			cfg.Contexts = tt.contexts

			placeholderNames := make(map[string]bool)
			for _, nc := range tt.contexts {
				if nc.Context.Environment == "" {
					placeholderNames[nc.Name] = true
				}
			}
			cfg.PruneEmptyEnvironments(tt.keepContext, placeholderNames)

			if len(cfg.Contexts) != len(tt.wantNames) {
				t.Fatalf("after prune: got %d contexts, want %d; names: %v",
					len(cfg.Contexts), len(tt.wantNames), contextNames(cfg.Contexts))
			}
			for i, want := range tt.wantNames {
				if cfg.Contexts[i].Name != want {
					t.Errorf("contexts[%d].Name = %q, want %q", i, cfg.Contexts[i].Name, want)
				}
			}
		})
	}
}

func TestContext_AccountUUIDRoundTrip(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")

	cfg := NewConfig()
	cfg.CurrentContext = "prod"
	cfg.Contexts = []NamedContext{
		{
			Name: "prod",
			Context: Context{
				Environment: "https://abc12345.apps.dynatrace.com",
				TokenRef:    "prod-token",
				AccountUUID: "aaaabbbb-cccc-dddd-eeee-ffffaaaabbbb",
			},
		},
	}
	if err := cfg.SaveTo(path); err != nil {
		t.Fatalf("SaveTo: %v", err)
	}

	loaded, err := LoadFrom(path)
	if err != nil {
		t.Fatalf("LoadFrom: %v", err)
	}
	if len(loaded.Contexts) != 1 {
		t.Fatalf("want 1 context, got %d", len(loaded.Contexts))
	}
	got := loaded.Contexts[0].Context.AccountUUID
	want := cfg.Contexts[0].Context.AccountUUID
	if got != want {
		t.Errorf("AccountUUID = %q, want %q", got, want)
	}
}

func contextNames(contexts []NamedContext) []string {
	names := make([]string, len(contexts))
	for i, nc := range contexts {
		names[i] = nc.Name
	}
	return names
}

// TestLoad_LocalConfigDoesNotExpandEnv verifies that env-var references in
// auto-discovered local configs are left unexpanded (H1-1/H1-2 hardening).
func TestLoad_LocalConfigDoesNotExpandEnv(t *testing.T) {
	// NOT parallel: os.Chdir is process-global.
	t.Setenv("PLANTED_SECRET", "secret-val")

	tmpDir := t.TempDir()
	localCfg := `apiVersion: v1
kind: Config
current-context: local-ctx
contexts:
  - name: local-ctx
    context:
      environment: https://evil.example/$PLANTED_SECRET
      token-ref: tok
tokens:
  - name: tok
    token: $PLANTED_SECRET
`
	if err := os.WriteFile(filepath.Join(tmpDir, LocalConfigName), []byte(localCfg), 0600); err != nil {
		t.Fatalf("write local config: %v", err)
	}

	origWd, _ := os.Getwd()
	defer func() { _ = os.Chdir(origWd) }()
	if err := os.Chdir(tmpDir); err != nil {
		t.Fatalf("chdir: %v", err)
	}

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}

	ctx, err := cfg.CurrentContextObj()
	if err != nil {
		t.Fatalf("CurrentContextObj: %v", err)
	}
	if !strings.Contains(ctx.Environment, "$PLANTED_SECRET") {
		t.Errorf("Environment = %q; want literal $PLANTED_SECRET (not expanded)", ctx.Environment)
	}
	if !strings.Contains(cfg.Tokens[0].Token, "$PLANTED_SECRET") {
		t.Errorf("Token = %q; want literal $PLANTED_SECRET (not expanded)", cfg.Tokens[0].Token)
	}
	if !cfg.IgnoredEnvRefs() {
		t.Error("IgnoredEnvRefs() = false; want true")
	}
}

// TestGetToken_LocalConfigIgnoresInlineToken verifies that an inline token
// defined in an auto-discovered local config is rejected (H1-4 hardening).
func TestGetToken_LocalConfigIgnoresInlineToken(t *testing.T) {
	// NOT parallel: os.Chdir is process-global.
	tmpDir := t.TempDir()
	localCfg := `apiVersion: v1
kind: Config
current-context: local-ctx
contexts:
  - name: local-ctx
    context:
      environment: https://abc.apps.dynatrace.com
      token-ref: my-token
tokens:
  - name: my-token
    token: dt0s08.SECRETTOKEN
`
	if err := os.WriteFile(filepath.Join(tmpDir, LocalConfigName), []byte(localCfg), 0600); err != nil {
		t.Fatalf("write local config: %v", err)
	}

	origWd, _ := os.Getwd()
	defer func() { _ = os.Chdir(origWd) }()
	if err := os.Chdir(tmpDir); err != nil {
		t.Fatalf("chdir: %v", err)
	}

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}

	_, err = cfg.GetToken("my-token")
	if err == nil {
		t.Fatal("GetToken() returned nil error; want error about inline token")
	}
	if !strings.Contains(err.Error(), "inline token") {
		t.Errorf("GetToken() error = %q; want message about inline token", err.Error())
	}
}

// TestGetToken_LocalConfigRejectsForeignOrigin verifies that a local config
// cannot redirect a stored credential to a different host (H1-5 hardening).
func TestGetToken_LocalConfigRejectsForeignOrigin(t *testing.T) {
	// NOT parallel: os.Chdir and XDG_CONFIG_HOME are process-global.
	tmpDir := t.TempDir()

	// Global config: token-ref "prod" bound to abc.apps.dynatrace.com.
	globalDir := filepath.Join(tmpDir, "xdg", "dtctl")
	if err := os.MkdirAll(globalDir, 0700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	globalCfg := `apiVersion: v1
kind: Config
current-context: prod
contexts:
  - name: prod
    context:
      environment: https://abc.apps.dynatrace.com
      token-ref: prod
`
	if err := os.WriteFile(filepath.Join(globalDir, "config"), []byte(globalCfg), 0600); err != nil {
		t.Fatalf("write global config: %v", err)
	}

	t.Setenv("XDG_CONFIG_HOME", filepath.Join(tmpDir, "xdg"))
	xdg.Reload()
	defer xdg.Reload()

	// Local config: same token-ref "prod" but pointing at evil.example.
	localCfg := `apiVersion: v1
kind: Config
current-context: local
contexts:
  - name: local
    context:
      environment: https://evil.example
      token-ref: prod
`
	if err := os.WriteFile(filepath.Join(tmpDir, LocalConfigName), []byte(localCfg), 0600); err != nil {
		t.Fatalf("write local config: %v", err)
	}

	origWd, _ := os.Getwd()
	defer func() { _ = os.Chdir(origWd) }()
	if err := os.Chdir(tmpDir); err != nil {
		t.Fatalf("chdir: %v", err)
	}

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}

	_, err = cfg.GetToken("prod")
	if err == nil {
		t.Fatal("GetToken() returned nil error; want error about foreign origin")
	}
	if !strings.Contains(err.Error(), "redirects token") && !strings.Contains(err.Error(), "foreign") && !strings.Contains(err.Error(), "evil.example") {
		t.Errorf("GetToken() error = %q; want message about origin mismatch", err.Error())
	}
}

// TestGetToken_LocalConfigMatchingOriginStillResolves verifies that a local
// config pointing to the same host as the global binding is not rejected by
// the origin check (H1-5 hardening — the happy path).
func TestGetToken_LocalConfigMatchingOriginStillResolves(t *testing.T) {
	// NOT parallel: os.Chdir and XDG_CONFIG_HOME are process-global.
	tmpDir := t.TempDir()

	// Global config: token-ref "prod" bound to abc.apps.dynatrace.com.
	globalDir := filepath.Join(tmpDir, "xdg", "dtctl")
	if err := os.MkdirAll(globalDir, 0700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	globalCfg := `apiVersion: v1
kind: Config
current-context: prod
contexts:
  - name: prod
    context:
      environment: https://abc.apps.dynatrace.com
      token-ref: prod
`
	if err := os.WriteFile(filepath.Join(globalDir, "config"), []byte(globalCfg), 0600); err != nil {
		t.Fatalf("write global config: %v", err)
	}

	t.Setenv("XDG_CONFIG_HOME", filepath.Join(tmpDir, "xdg"))
	xdg.Reload()
	defer xdg.Reload()

	// Local config: same token-ref "prod" pointing at the SAME host.
	localCfg := `apiVersion: v1
kind: Config
current-context: local
contexts:
  - name: local
    context:
      environment: https://abc.apps.dynatrace.com
      token-ref: prod
`
	if err := os.WriteFile(filepath.Join(tmpDir, LocalConfigName), []byte(localCfg), 0600); err != nil {
		t.Fatalf("write local config: %v", err)
	}

	origWd, _ := os.Getwd()
	defer func() { _ = os.Chdir(origWd) }()
	if err := os.Chdir(tmpDir); err != nil {
		t.Fatalf("chdir: %v", err)
	}

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}

	_, err = cfg.GetToken("prod")
	// Origin check passes; the error (if any) should be about the token not
	// being found in the keyring or file store — not about origin binding.
	if err != nil {
		if strings.Contains(err.Error(), "redirects token") || strings.Contains(err.Error(), "not bound") {
			t.Errorf("GetToken() returned origin error %q; origin should have been accepted", err.Error())
		}
		// Any other error (keyring unavailable, token not found) is expected.
	}
}

// TestLoadFrom_ExplicitConfigStillExpandsEnv verifies that a config loaded via
// DTCTL_CONFIG still has env-var expansion applied (only auto-discovered local
// configs skip expansion — H1-1 hardening).
func TestLoadFrom_ExplicitConfigStillExpandsEnv(t *testing.T) {
	t.Setenv("DT_TEST_ENV_URL", "https://explicit.apps.dynatrace.com")

	tmpDir := t.TempDir()
	cfgContent := `apiVersion: v1
kind: Config
current-context: explicit
contexts:
  - name: explicit
    context:
      environment: $DT_TEST_ENV_URL
      token-ref: tok
`
	cfgPath := filepath.Join(tmpDir, "explicit-config.yaml")
	if err := os.WriteFile(cfgPath, []byte(cfgContent), 0600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	t.Setenv(EnvConfig, cfgPath)

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}
	ctx, err := cfg.CurrentContextObj()
	if err != nil {
		t.Fatalf("CurrentContextObj: %v", err)
	}
	if ctx.Environment != "https://explicit.apps.dynatrace.com" {
		t.Errorf("Environment = %q; want expanded value", ctx.Environment)
	}
	if cfg.IgnoredEnvRefs() {
		t.Error("IgnoredEnvRefs() = true; explicit config should not set this flag")
	}
}

// writeOriginBindingFixture sets up the documented per-project layout: a global
// config binding tokenRef to globalEnv, and an auto-discovered .dtctl.yaml in
// the (chdir'd) working directory whose environment is localEnv. It returns the
// loaded local config.
func writeOriginBindingFixture(t *testing.T, tokenRef, globalEnv, localEnv string) *Config {
	t.Helper()
	// NOT parallel-safe: os.Chdir and XDG_CONFIG_HOME are process-global.
	tmpDir := t.TempDir()

	globalDir := filepath.Join(tmpDir, "xdg", "dtctl")
	if err := os.MkdirAll(globalDir, 0700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	globalCfg := fmt.Sprintf(`apiVersion: v1
kind: Config
current-context: global
contexts:
  - name: global
    context:
      environment: %s
      token-ref: %s
`, globalEnv, tokenRef)
	if err := os.WriteFile(filepath.Join(globalDir, "config"), []byte(globalCfg), 0600); err != nil {
		t.Fatalf("write global config: %v", err)
	}

	t.Setenv("XDG_CONFIG_HOME", filepath.Join(tmpDir, "xdg"))
	xdg.Reload()
	t.Cleanup(xdg.Reload)

	localCfg := fmt.Sprintf(`apiVersion: v1
kind: Config
current-context: local
contexts:
  - name: local
    context:
      environment: %q
      token-ref: %s
`, localEnv, tokenRef)
	if err := os.WriteFile(filepath.Join(tmpDir, LocalConfigName), []byte(localCfg), 0600); err != nil {
		t.Fatalf("write local config: %v", err)
	}

	origWd, _ := os.Getwd()
	t.Cleanup(func() { _ = os.Chdir(origWd) })
	if err := os.Chdir(tmpDir); err != nil {
		t.Fatalf("chdir: %v", err)
	}

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}
	if !cfg.IsLocal() {
		t.Fatal("fixture did not produce a local config")
	}
	return cfg
}

// TestGetToken_LocalConfigEnvVarEnvironmentSatisfiesOriginBinding covers the
// workflow `dtctl config init` generates and QUICK_START.md documents: a
// committed .dtctl.yaml names its environment through ${VAR}, and the token-ref
// is bound to that same host in the global config.
//
// Regression test: the origin-binding check used to compare the *unexpanded*
// literal, so the resolved host came out empty and the documented setup failed
// with `redirects token ... to ""` even though the hosts matched.
func TestGetToken_LocalConfigEnvVarEnvironmentSatisfiesOriginBinding(t *testing.T) {
	const host = "https://abc12345.apps.dynatrace.com"
	t.Setenv("DT_TEST_ENVIRONMENT_URL", host)

	cfg := writeOriginBindingFixture(t, "dev-oauth", host, "${DT_TEST_ENVIRONMENT_URL}")

	_, err := cfg.GetToken("dev-oauth")
	// No credential is stored, so "not found" is the expected outcome; what
	// must not happen is rejection on origin grounds.
	if err != nil && (strings.Contains(err.Error(), "redirects token") ||
		strings.Contains(err.Error(), "not bound")) {
		t.Errorf("GetToken() rejected the documented ${VAR} setup: %v", err)
	}
}

// TestGetToken_LocalConfigEnvVarCannotRedirectOrigin is the other half: making
// the environment an env-var reference must not become a way around the origin
// binding when the expansion resolves to a different host.
func TestGetToken_LocalConfigEnvVarCannotRedirectOrigin(t *testing.T) {
	t.Setenv("DT_TEST_ENVIRONMENT_URL", "https://zzz99999.apps.dynatrace.com")

	cfg := writeOriginBindingFixture(t, "dev-oauth",
		"https://abc12345.apps.dynatrace.com", "${DT_TEST_ENVIRONMENT_URL}")

	_, err := cfg.GetToken("dev-oauth")
	if err == nil || !strings.Contains(err.Error(), "redirects token") {
		t.Fatalf("GetToken() error = %v, want rejection for redirecting the token", err)
	}
	if !strings.Contains(err.Error(), "zzz99999.apps.dynatrace.com") {
		t.Errorf("error should name the resolved host it refused, got: %v", err)
	}
}

// TestResolvedEnvironment_OnlyExpandsForLocalConfigs pins the asymmetry that
// caused the regression: expansion is a local-config affordance, and a trusted
// config's value is returned untouched (LoadFrom already expanded it).
func TestResolveLocalEnvironments_AdoptsOnlyValidOrigins(t *testing.T) {
	t.Setenv("DT_TEST_ENVIRONMENT_URL", "https://abc12345.apps.dynatrace.com")
	t.Setenv("PLANTED_SECRET", "secret-val")

	// A non-local config is never expanded at all.
	global := NewConfig()
	global.Contexts = []NamedContext{{Name: "g", Context: Context{Environment: "${DT_TEST_ENVIRONMENT_URL}"}}}
	global.resolveLocalEnvironments()
	if got := global.Contexts[0].Context.Environment; got != "${DT_TEST_ENVIRONMENT_URL}" {
		t.Errorf("non-local environment = %q, want it untouched", got)
	}

	tests := []struct {
		name string
		raw  string
		want string
	}{
		{
			name: "valid bare origin is adopted",
			raw:  "${DT_TEST_ENVIRONMENT_URL}",
			want: "https://abc12345.apps.dynatrace.com",
		},
		{
			// The expanded form must exist nowhere: readers that do not
			// validate (doctor HEADs the URL as written) would otherwise ship
			// the secret to the attacker's host.
			name: "secret smuggled into a path is not adopted",
			raw:  "https://abc12345.apps.dynatrace.com/$PLANTED_SECRET",
			want: "https://abc12345.apps.dynatrace.com/$PLANTED_SECRET",
		},
		{
			name: "secret smuggled into a hostname is not adopted",
			raw:  "https://$PLANTED_SECRET.apps.dynatrace.com",
			want: "https://$PLANTED_SECRET.apps.dynatrace.com",
		},
		{
			name: "foreign host is not adopted",
			raw:  "https://evil.example/$PLANTED_SECRET",
			want: "https://evil.example/$PLANTED_SECRET",
		},
		{
			name: "unset variable is not adopted",
			raw:  "${DT_TEST_UNSET_URL}",
			want: "${DT_TEST_UNSET_URL}",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := newLocalConfig(t, tt.raw, "ref", nil)
			if got := cfg.Contexts[0].Context.Environment; got != tt.want {
				t.Errorf("environment = %q, want %q", got, tt.want)
			}
			unresolved := tt.raw == tt.want
			if got := cfg.EnvironmentUnresolved("default"); got != unresolved {
				t.Errorf("EnvironmentUnresolved() = %v, want %v", got, unresolved)
			}
		})
	}
}

// localConfigWithEnvVarURL writes a global config binding token-ref "prod" to
// abc12345.apps.dynatrace.com and a local .dtctl.yaml that names the same host
// through ${DT_TEST_ENVIRONMENT_URL} — the shape `dtctl config init` writes.
// It chdirs into the project dir and returns it.
func localConfigWithEnvVarURL(t *testing.T) string {
	t.Helper()
	tmpDir := t.TempDir()

	globalDir := filepath.Join(tmpDir, "xdg", "dtctl")
	if err := os.MkdirAll(globalDir, 0700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	globalCfg := `apiVersion: v1
kind: Config
current-context: prod
contexts:
  - name: prod
    context:
      environment: https://abc12345.apps.dynatrace.com
      token-ref: prod
`
	if err := os.WriteFile(filepath.Join(globalDir, "config"), []byte(globalCfg), 0600); err != nil {
		t.Fatalf("write global config: %v", err)
	}
	// Registered before t.Setenv so it runs *after* the env var is restored;
	// reloading while it still points at the temp dir caches a path that
	// t.TempDir is about to delete.
	t.Cleanup(xdg.Reload)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(tmpDir, "xdg"))
	xdg.Reload()

	localCfg := `apiVersion: v1
kind: Config
current-context: local
contexts:
  - name: local
    context:
      environment: ${DT_TEST_ENVIRONMENT_URL}
      token-ref: prod
`
	if err := os.WriteFile(filepath.Join(tmpDir, LocalConfigName), []byte(localCfg), 0600); err != nil {
		t.Fatalf("write local config: %v", err)
	}
	t.Setenv("DT_TEST_ENVIRONMENT_URL", "https://abc12345.apps.dynatrace.com")

	origWd, _ := os.Getwd()
	t.Cleanup(func() { _ = os.Chdir(origWd) })
	if err := os.Chdir(tmpDir); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	return tmpDir
}

// TestLoad_LocalEnvironmentIsResolvedForPlainReaders pins that Load resolves a
// local ${VAR} environment in place. Most readers of an environment URL take
// Context.Environment directly (doctor's URL and connectivity checks, the
// live-debugger handlers, spill's tenant id, account environment detection);
// resolving only at selected call sites left those reporting the literal
// "${DT_TEST_ENVIRONMENT_URL}".
func TestLoad_LocalEnvironmentIsResolvedForPlainReaders(t *testing.T) {
	// NOT parallel: os.Chdir and XDG_CONFIG_HOME are process-global.
	localConfigWithEnvVarURL(t)

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}

	const want = "https://abc12345.apps.dynatrace.com"
	if got := cfg.Contexts[0].Context.Environment; got != want {
		t.Errorf("Contexts[0].Context.Environment = %q, want %q", got, want)
	}
	ctx, err := cfg.CurrentContextObj()
	if err != nil {
		t.Fatalf("CurrentContextObj() error: %v", err)
	}
	if got := ctx.Environment; got != want {
		t.Errorf("CurrentContextObj().Environment = %q, want %q", got, want)
	}
	// The raw reference is kept so SaveTo can write it back unexpanded.
	if got := cfg.rawEnvironments["local"]; got != "${DT_TEST_ENVIRONMENT_URL}" {
		t.Errorf("rawEnvironments[\"local\"] = %q, want the raw reference", got)
	}
}

// TestSaveTo_PreservesLocalEnvironmentTemplate verifies that editing a
// committed .dtctl.yaml does not bake the editing developer's expansion into
// it. Any config-management command (ctx use, config set, alias add, login)
// load-modify-saves the discovered file, so resolving at load must not leak
// into the write path.
func TestSaveTo_PreservesLocalEnvironmentTemplate(t *testing.T) {
	// NOT parallel: os.Chdir and XDG_CONFIG_HOME are process-global.
	tmpDir := localConfigWithEnvVarURL(t)
	path := filepath.Join(tmpDir, LocalConfigName)

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}
	// An unrelated mutation, as `dtctl config set output json` would make.
	cfg.Preferences.Output = "json"
	if err := cfg.SaveTo(path); err != nil {
		t.Fatalf("SaveTo() error: %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if !strings.Contains(string(data), "${DT_TEST_ENVIRONMENT_URL}") {
		t.Errorf("SaveTo() dropped the env-var reference; file is:\n%s", data)
	}
	if strings.Contains(string(data), "abc12345.apps.dynatrace.com") {
		t.Errorf("SaveTo() baked the expansion into the file; file is:\n%s", data)
	}
}

// TestSaveTo_KeepsExplicitEnvironmentOverride verifies the other half: an
// environment changed after load (config set-context --environment) is written
// as given rather than reverted to the template it replaced.
func TestSaveTo_KeepsExplicitEnvironmentOverride(t *testing.T) {
	// NOT parallel: os.Chdir and XDG_CONFIG_HOME are process-global.
	tmpDir := localConfigWithEnvVarURL(t)
	path := filepath.Join(tmpDir, LocalConfigName)

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}
	const explicit = "https://zzz99999.apps.dynatrace.com"
	cfg.Contexts[0].Context.Environment = explicit
	if err := cfg.SaveTo(path); err != nil {
		t.Fatalf("SaveTo() error: %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if !strings.Contains(string(data), explicit) {
		t.Errorf("SaveTo() lost the explicit environment; file is:\n%s", data)
	}
	if strings.Contains(string(data), "${DT_TEST_ENVIRONMENT_URL}") {
		t.Errorf("SaveTo() reverted an explicit environment to the template; file is:\n%s", data)
	}
}

func TestConfig_DeleteToken_ClearsEntireFanOut(t *testing.T) {
	t.Parallel()

	kr := newMockKeyring()
	// The plain platform token, the per-environment OAuth caches, and the scope
	// companion each hold live material and must all be cleared.
	kr.data["incident-token"] = "platform-secret"
	kr.data["oauth:prod:incident-token"] = `{"refresh_token":"prod-refresh"}`
	kr.data["oauth:prod:incident-token:scopes"] = "storage:logs:read"
	kr.data["oauth:dev:incident-token"] = `{"refresh_token":"dev-refresh"}`
	kr.data["oauth:hard:incident-token"] = `{"refresh_token":"hard-refresh"}`
	// An unrelated credential must survive: teardown is scoped to one token ref.
	kr.data["other-token"] = "keep-me"

	cfg := NewConfig()
	cfg.Tokens = []NamedToken{{Name: "incident-token"}, {Name: "other-token"}}

	if err := cfg.deleteTokenWithKeyring("incident-token", kr, NewOAuthFileStoreWithDir(t.TempDir())); err != nil {
		t.Fatalf("deleteTokenWithKeyring() error = %v", err)
	}

	for key := range kr.data {
		if strings.Contains(key, "incident-token") {
			t.Errorf("keyring entry %q still exists after DeleteToken", key)
		}
	}
	if got := kr.data["other-token"]; got != "keep-me" {
		t.Errorf("unrelated credential = %q, want %q", got, "keep-me")
	}

	if len(cfg.Tokens) != 1 || cfg.Tokens[0].Name != "other-token" {
		t.Errorf("config tokens = %+v, want only other-token", cfg.Tokens)
	}
}

func TestConfig_DeleteToken_ClearsFileStore(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	fileStore := NewOAuthFileStoreWithDir(dir)

	// Both backends are swept regardless of which one is active, because
	// GetToken reads from either: a credential written to the file store and
	// deleted only from the keyring would still resolve afterwards.
	keys := []string{"file-cred", "oauth:prod:file-cred", "oauth:prod:file-cred:scopes"}
	for _, key := range keys {
		if err := fileStore.SetToken(key, `{"refresh_token":"stale"}`); err != nil {
			t.Fatalf("seeding %q: %v", key, err)
		}
	}

	cfg := NewConfig()
	cfg.Tokens = []NamedToken{{Name: "file-cred"}}

	if err := cfg.deleteTokenWithKeyring("file-cred", &unavailableKeyring{}, fileStore); err != nil {
		t.Fatalf("deleteTokenWithKeyring() error = %v", err)
	}

	for _, key := range keys {
		if _, err := fileStore.GetToken(key); err == nil {
			t.Errorf("file-store entry %q still exists after DeleteToken", key)
		}
	}
}

func TestConfig_DeleteToken_SweepsEnvironmentsWithoutAContext(t *testing.T) {
	t.Parallel()

	kr := newMockKeyring()
	for _, key := range []string{"oauth:prod:orphan", "oauth:dev:orphan", "oauth:hard:orphan"} {
		kr.data[key] = `{"refresh_token":"stale"}`
	}

	// No context references "orphan" — the credential's context may already be
	// gone, or may never have existed. The sweep must still be complete, which
	// it is because oauthKeyringNames enumerates prod/dev/hard unconditionally
	// and oauthEnvironmentFromURL cannot return anything outside that set. This
	// is why deletion imposes no ordering against context removal.
	cfg := NewConfig()
	cfg.Tokens = []NamedToken{{Name: "orphan"}}

	if err := cfg.deleteTokenWithKeyring("orphan", kr, NewOAuthFileStoreWithDir(t.TempDir())); err != nil {
		t.Fatalf("deleteTokenWithKeyring() error = %v", err)
	}

	if len(kr.data) != 0 {
		t.Errorf("keyring still holds %v after DeleteToken with no context present", kr.data)
	}
}

func TestConfig_DeleteToken_SweepsLegacyOAuthEntry(t *testing.T) {
	t.Parallel()

	kr := newMockKeyring()
	// Pre-environment format written by older dtctl versions. GetToken no
	// longer resolves it, but it still holds a refresh token, so teardown must
	// remove it rather than leave an unreachable secret on the machine.
	kr.data["oauth:legacy-cred"] = `{"refresh_token":"ancient"}`
	kr.data["oauth:legacy-cred:scopes"] = "storage:logs:read"

	cfg := NewConfig()
	cfg.Tokens = []NamedToken{{Name: "legacy-cred"}}

	if err := cfg.deleteTokenWithKeyring("legacy-cred", kr, NewOAuthFileStoreWithDir(t.TempDir())); err != nil {
		t.Fatalf("deleteTokenWithKeyring() error = %v", err)
	}

	if len(kr.data) != 0 {
		t.Errorf("legacy OAuth entries survived DeleteToken: %v", kr.data)
	}
}

func TestConfig_DeleteToken_IsIdempotent(t *testing.T) {
	t.Parallel()

	cfg := NewConfig()
	// A credential that is already gone is not an error, so a teardown step can
	// be retried (or run twice) without failing the caller.
	if err := cfg.deleteTokenWithKeyring("never-existed", newMockKeyring(), NewOAuthFileStoreWithDir(t.TempDir())); err != nil {
		t.Errorf("DeleteToken() for missing credential = %v, want nil", err)
	}
}

func TestConfig_DeleteToken_KeepsConfigEntryWhenStoreFails(t *testing.T) {
	t.Parallel()

	cfg := NewConfig()
	cfg.Tokens = []NamedToken{{Name: "stuck-cred"}}

	// A keyring that reports itself available but refuses deletes: the error must
	// surface, and the config entry must survive so the delete stays retryable
	// by name rather than leaving an unreachable secret behind.
	if err := cfg.deleteTokenWithKeyring("stuck-cred", &refusingKeyring{}, NewOAuthFileStoreWithDir(t.TempDir())); err == nil {
		t.Fatal("DeleteToken() error = nil, want error when the keyring refuses the delete")
	}

	if len(cfg.Tokens) != 1 || cfg.Tokens[0].Name != "stuck-cred" {
		t.Errorf("config tokens = %+v, want stuck-cred retained for retry", cfg.Tokens)
	}
}

func TestConfig_DeleteToken_RejectsEmptyName(t *testing.T) {
	t.Parallel()

	cfg := NewConfig()
	// An empty name would otherwise expand into the bare "oauth:<env>:" prefixes.
	if err := cfg.deleteTokenWithKeyring("", newMockKeyring(), nil); err == nil {
		t.Error("DeleteToken(\"\") error = nil, want error")
	}
}

func TestConfig_EffectiveQueryLimits(t *testing.T) {
	// No block anywhere: every field stays zero so callers fall through to the
	// server defaults, i.e. the pre-feature behavior.
	cfg := &Config{CurrentContext: "dev", Contexts: []NamedContext{{Name: "dev"}}}
	if got := cfg.EffectiveQueryLimits(); !got.IsZero() {
		t.Errorf("empty config = %+v, want zero", got)
	}

	// Global only.
	cfg.QueryLimits = QueryLimits{ScanLimitGbytes: 500, MaxResultRecords: 5000}
	got := cfg.EffectiveQueryLimits()
	if got.ScanLimitGbytes != 500 || got.MaxResultRecords != 5000 {
		t.Errorf("global = %+v, want scan 500 / records 5000", got)
	}

	// Context overrides one field; the rest must be inherited, not reset.
	cfg.Contexts[0].Context.QueryLimits = &QueryLimits{ScanLimitGbytes: 50}
	got = cfg.EffectiveQueryLimits()
	if got.ScanLimitGbytes != 50 {
		t.Errorf("context override scan = %g, want 50", got.ScanLimitGbytes)
	}
	if got.MaxResultRecords != 5000 {
		t.Errorf("inherited records = %d, want 5000", got.MaxResultRecords)
	}

	// A context that sets a zero field does not clear the global value: zero
	// means "unset" throughout the chain.
	cfg.Contexts[0].Context.QueryLimits = &QueryLimits{MaxResultBytes: 1024}
	got = cfg.EffectiveQueryLimits()
	if got.ScanLimitGbytes != 500 || got.MaxResultBytes != 1024 {
		t.Errorf("partial override = %+v, want scan 500 / bytes 1024", got)
	}
}

// With no usable current context, the global block still applies rather than
// erroring out — `dtctl query` against an explicit --environment has no context.
func TestConfig_EffectiveQueryLimits_NoCurrentContext(t *testing.T) {
	cfg := &Config{QueryLimits: QueryLimits{ScanLimitGbytes: 500}}
	if got := cfg.EffectiveQueryLimits(); got.ScanLimitGbytes != 500 {
		t.Errorf("no current context = %+v, want scan 500", got)
	}

	cfg.CurrentContext = "missing"
	if got := cfg.EffectiveQueryLimits(); got.ScanLimitGbytes != 500 {
		t.Errorf("unknown current context = %+v, want scan 500", got)
	}
}

// The block must survive a save/load cycle with its documented key spellings,
// since plugins read the file directly.
func TestConfig_QueryLimitsRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config")
	cfg := NewConfig()
	cfg.QueryLimits = QueryLimits{ScanLimitGbytes: 500, MaxResultRecords: 5000}
	cfg.SetContext("prod", "https://abc.example.invalid", "prod-token")
	cfg.CurrentContext = "prod"
	for i := range cfg.Contexts {
		if cfg.Contexts[i].Name == "prod" {
			cfg.Contexts[i].Context.QueryLimits = &QueryLimits{SamplingRatio: 100}
		}
	}
	if err := cfg.SaveTo(path); err != nil {
		t.Fatalf("SaveTo: %v", err)
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"query-limits:", "scan-limit-gbytes: 500", "max-result-records: 5000", "sampling-ratio: 100"} {
		if !strings.Contains(string(raw), want) {
			t.Errorf("saved config missing %q:\n%s", want, raw)
		}
	}

	loaded, err := LoadFrom(path)
	if err != nil {
		t.Fatalf("LoadFrom: %v", err)
	}
	if loaded.QueryLimits != cfg.QueryLimits {
		t.Errorf("global round-trip = %+v, want %+v", loaded.QueryLimits, cfg.QueryLimits)
	}
	ctx, err := loaded.CurrentContextObj()
	if err != nil {
		t.Fatal(err)
	}
	if ctx.QueryLimits == nil || ctx.QueryLimits.SamplingRatio != 100 {
		t.Errorf("context round-trip = %+v, want sampling 100", ctx.QueryLimits)
	}
}

func TestQueryLimits_Validate(t *testing.T) {
	if err := (QueryLimits{ScanLimitGbytes: 500, MaxResultRecords: 5000, MaxResultBytes: 1024, SamplingRatio: 100}).Validate(); err != nil {
		t.Errorf("valid block: error = %v, want nil", err)
	}
	// Zero everywhere is the "defer to the next layer" case, not an error.
	if err := (QueryLimits{}).Validate(); err != nil {
		t.Errorf("empty block: error = %v, want nil", err)
	}
	for name, lim := range map[string]QueryLimits{
		"scan-limit-gbytes":  {ScanLimitGbytes: -1},
		"max-result-records": {MaxResultRecords: -1},
		"max-result-bytes":   {MaxResultBytes: -1},
		"sampling-ratio":     {SamplingRatio: -0.5},
	} {
		err := lim.Validate()
		if err == nil {
			t.Errorf("negative %s: error = nil, want rejection", name)
			continue
		}
		if !strings.Contains(err.Error(), name) {
			t.Errorf("negative %s: error = %q, want it to name the key", name, err)
		}
	}
}

// TestConfig_UsageTrackingOn_LocalNeedsGlobalOptIn: an auto-discovered local
// .dtctl.yaml cannot turn the usage ledger on. Its context is tracked only when
// the global context binding the same token-ref and host opted in.
func TestConfig_UsageTrackingOn_LocalNeedsGlobalOptIn(t *testing.T) {
	// NOT parallel: os.Chdir and XDG_CONFIG_HOME are process-global.
	cases := []struct {
		name          string
		globalTracked bool
		localTracked  bool
		localEnv      string
		localRef      string
		wantOn        bool
		wantIgnored   bool
	}{
		{name: "local on, global off is ignored", localTracked: true, localEnv: "https://global.dt.com", localRef: "prod-token", wantIgnored: true},
		{name: "global on is inherited", globalTracked: true, localEnv: "https://global.dt.com", localRef: "prod-token", wantOn: true},
		{name: "both on", globalTracked: true, localTracked: true, localEnv: "https://global.dt.com", localRef: "prod-token", wantOn: true},
		{name: "global on for another host", globalTracked: true, localTracked: true, localEnv: "https://other.dt.com", localRef: "prod-token", wantIgnored: true},
		{name: "unbound token-ref", globalTracked: true, localTracked: true, localEnv: "https://global.dt.com", localRef: "unbound-token", wantIgnored: true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			xdgDir := filepath.Join(t.TempDir(), "xdg")
			projectDir := t.TempDir()
			globalDir := filepath.Join(xdgDir, "dtctl")
			if err := os.MkdirAll(globalDir, 0700); err != nil {
				t.Fatalf("mkdir: %v", err)
			}

			globalCfg := `apiVersion: v1
kind: Config
current-context: myctx
contexts:
  - name: myctx
    context:
      environment: https://global.dt.com
      token-ref: prod-token
`
			if tc.globalTracked {
				globalCfg += "      usage-tracking: true\n"
			}
			if err := os.WriteFile(filepath.Join(globalDir, "config"), []byte(globalCfg), 0600); err != nil {
				t.Fatalf("write global config: %v", err)
			}

			t.Setenv("XDG_CONFIG_HOME", xdgDir)
			xdg.Reload()
			defer xdg.Reload()

			localCfg := `apiVersion: v1
kind: Config
current-context: project
contexts:
  - name: project
    context:
      environment: ` + tc.localEnv + `
      token-ref: ` + tc.localRef + `
`
			if tc.localTracked {
				localCfg += "      usage-tracking: true\n"
			}
			if err := os.WriteFile(filepath.Join(projectDir, LocalConfigName), []byte(localCfg), 0600); err != nil {
				t.Fatalf("write local config: %v", err)
			}

			origWd, _ := os.Getwd()
			defer func() { _ = os.Chdir(origWd) }()
			if err := os.Chdir(projectDir); err != nil {
				t.Fatalf("chdir: %v", err)
			}

			cfg, err := Load()
			if err != nil {
				t.Fatalf("Load() error: %v", err)
			}
			if !cfg.IsLocal() {
				t.Fatal("expected the local config to be loaded")
			}
			if got := cfg.UsageTrackingEnabled(); got != tc.wantOn {
				t.Errorf("UsageTrackingEnabled() = %v, want %v", got, tc.wantOn)
			}
			if got := cfg.IgnoredUsageTracking("project"); got != tc.wantIgnored {
				t.Errorf("IgnoredUsageTracking() = %v, want %v", got, tc.wantIgnored)
			}
		})
	}
}
