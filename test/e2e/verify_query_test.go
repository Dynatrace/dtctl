//go:build integration
// +build integration

package e2e

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/Dynatrace/dtctl/pkg/exec"
	"github.com/Dynatrace/dtctl/test/integration"
)

func TestQueryVerify_ValidQuery(t *testing.T) {
	env := integration.SetupIntegration(t)
	defer env.Cleanup.Cleanup(t)

	executor := exec.NewDQLExecutor(env.Client)

	tests := []struct {
		name    string
		query   string
		wantErr bool
	}{
		{
			name:    "simple fetch logs query",
			query:   "fetch logs | limit 10",
			wantErr: false,
		},
		{
			name:    "fetch logs with filter",
			query:   "fetch logs | filter status == \"ERROR\" | limit 10",
			wantErr: false,
		},
		{
			name:    "fetch events query",
			query:   "fetch events | limit 10",
			wantErr: false,
		},
		{
			// Use a core Grail data object with a fields projection. Classic
			// dt.entity.* / smartscapeNodes data objects are not available in
			// Grail-only tenants (the API rejects them as UNKNOWN_DATA_OBJECT),
			// so they cannot be used in a query that must verify as valid.
			name:    "fetch spans with fields query",
			query:   "fetch spans | fields timestamp, span.name | limit 10",
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := executor.VerifyQuery(tt.query, exec.DQLVerifyOptions{})
			if (err != nil) != tt.wantErr {
				t.Errorf("VerifyQuery() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if err != nil {
				t.Logf("✓ Got expected error: %v", err)
				return
			}

			// Verify result structure
			if result == nil {
				t.Error("VerifyQuery() returned nil result")
				return
			}

			// Verify query is valid
			if !result.Valid {
				t.Errorf("Expected valid query, got valid=%v", result.Valid)
			}

			t.Logf("✓ Query verified successfully (valid=%v)", result.Valid)
		})
	}
}

func TestQueryVerify_InvalidQuery(t *testing.T) {
	env := integration.SetupIntegration(t)
	defer env.Cleanup.Cleanup(t)

	executor := exec.NewDQLExecutor(env.Client)

	tests := []struct {
		name           string
		query          string
		expectValid    bool
		expectError    bool   // API error vs invalid query
		expectSeverity string // ERROR notification
	}{
		{
			name:           "invalid syntax",
			query:          "fetch logs | invalid_command",
			expectValid:    false,
			expectError:    false,
			expectSeverity: "ERROR",
		},
		{
			name:           "missing pipe",
			query:          "fetch logs filter status == \"ERROR\"",
			expectValid:    false,
			expectError:    false,
			expectSeverity: "ERROR",
		},
		{
			name:           "empty query",
			query:          "",
			expectValid:    false,
			expectError:    false,
			expectSeverity: "ERROR",
		},
		{
			name:           "invalid filter syntax",
			query:          "fetch logs | filter status ==",
			expectValid:    false,
			expectError:    false,
			expectSeverity: "ERROR",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := executor.VerifyQuery(tt.query, exec.DQLVerifyOptions{})

			if tt.expectError {
				if err == nil {
					t.Error("Expected API error, got nil")
				} else {
					t.Logf("✓ Got expected API error: %v", err)
				}
				return
			}

			if err != nil {
				t.Errorf("Unexpected error: %v", err)
				return
			}

			if result == nil {
				t.Error("VerifyQuery() returned nil result")
				return
			}

			// Verify query is invalid
			if result.Valid != tt.expectValid {
				t.Errorf("Expected valid=%v, got valid=%v", tt.expectValid, result.Valid)
			}

			// Verify error notifications
			if tt.expectSeverity != "" {
				foundSeverity := false
				for _, notification := range result.Notifications {
					if notification.Severity == tt.expectSeverity {
						foundSeverity = true
						t.Logf("✓ Found expected severity: %s - %s", notification.Severity, notification.Message)
						break
					}
				}
				if !foundSeverity {
					t.Errorf("Expected notification with severity %s, but not found", tt.expectSeverity)
				}
			}

			t.Logf("✓ Invalid query rejected (valid=%v, notifications=%d)", result.Valid, len(result.Notifications))
		})
	}
}

func TestQueryVerify_FileInput(t *testing.T) {
	env := integration.SetupIntegration(t)
	defer env.Cleanup.Cleanup(t)

	executor := exec.NewDQLExecutor(env.Client)

	// Create a temporary directory for test files
	tmpDir, err := os.MkdirTemp("", "dtctl-query-verify-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	tests := []struct {
		name        string
		content     string
		expectValid bool
	}{
		{
			name:        "valid query from file",
			content:     "fetch logs | limit 10",
			expectValid: true,
		},
		{
			name: "multiline query from file",
			content: `fetch logs
| filter status == "ERROR"
| limit 10`,
			expectValid: true,
		},
		{
			name:        "invalid query from file",
			content:     "fetch logs | invalid_command",
			expectValid: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Write query to file
			queryFile := filepath.Join(tmpDir, "query.dql")
			if err := os.WriteFile(queryFile, []byte(tt.content), 0644); err != nil {
				t.Fatalf("Failed to write query file: %v", err)
			}

			// Read query from file
			queryBytes, err := os.ReadFile(queryFile)
			if err != nil {
				t.Fatalf("Failed to read query file: %v", err)
			}
			query := string(queryBytes)

			// Verify query
			result, err := executor.VerifyQuery(query, exec.DQLVerifyOptions{})
			if err != nil {
				t.Fatalf("VerifyQuery() error = %v", err)
			}

			if result == nil {
				t.Error("VerifyQuery() returned nil result")
				return
			}

			if result.Valid != tt.expectValid {
				t.Errorf("Expected valid=%v, got valid=%v", tt.expectValid, result.Valid)
			}

			t.Logf("✓ Query from file verified (valid=%v)", result.Valid)
		})
	}
}

func TestQueryVerify_StdinInput(t *testing.T) {
	env := integration.SetupIntegration(t)
	defer env.Cleanup.Cleanup(t)

	executor := exec.NewDQLExecutor(env.Client)

	tests := []struct {
		name        string
		stdin       string
		expectValid bool
	}{
		{
			name:        "valid query from stdin",
			stdin:       "fetch logs | limit 10",
			expectValid: true,
		},
		{
			name: "multiline query from stdin",
			stdin: `fetch logs
| filter status == "ERROR"
| limit 10`,
			expectValid: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Simulate reading from stdin
			query := tt.stdin

			// Verify query
			result, err := executor.VerifyQuery(query, exec.DQLVerifyOptions{})
			if err != nil {
				t.Fatalf("VerifyQuery() error = %v", err)
			}

			if result == nil {
				t.Error("VerifyQuery() returned nil result")
				return
			}

			if result.Valid != tt.expectValid {
				t.Errorf("Expected valid=%v, got valid=%v", tt.expectValid, result.Valid)
			}

			t.Logf("✓ Query from stdin verified (valid=%v)", result.Valid)
		})
	}
}

func TestQueryVerify_TemplateVariables(t *testing.T) {
	env := integration.SetupIntegration(t)
	defer env.Cleanup.Cleanup(t)

	executor := exec.NewDQLExecutor(env.Client)

	// Note: Template rendering is handled in the command layer (cmd/query.go)
	// This test verifies that the executor can handle pre-rendered queries
	tests := []struct {
		name          string
		queryTemplate string
		renderedQuery string
		expectValid   bool
	}{
		{
			name:          "query with variable placeholder",
			queryTemplate: "fetch logs | filter status == {{.status}} | limit {{.limit}}",
			renderedQuery: "fetch logs | filter status == \"ERROR\" | limit 10",
			expectValid:   true,
		},
		{
			// Templating the data object itself. Avoids dt.entity.* so the
			// rendered query verifies as valid on Grail-only tenants too.
			name:          "query with data object variable",
			queryTemplate: "fetch {{.dataObject}} | fields timestamp | limit 10",
			renderedQuery: "fetch spans | fields timestamp | limit 10",
			expectValid:   true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Verify the rendered query (template rendering happens in cmd layer)
			result, err := executor.VerifyQuery(tt.renderedQuery, exec.DQLVerifyOptions{})
			if err != nil {
				t.Fatalf("VerifyQuery() error = %v", err)
			}

			if result == nil {
				t.Error("VerifyQuery() returned nil result")
				return
			}

			if result.Valid != tt.expectValid {
				t.Errorf("Expected valid=%v, got valid=%v", tt.expectValid, result.Valid)
			}

			t.Logf("✓ Template-rendered query verified (valid=%v)", result.Valid)
		})
	}
}

func TestQueryVerify_CanonicalFlag(t *testing.T) {
	env := integration.SetupIntegration(t)
	defer env.Cleanup.Cleanup(t)

	executor := exec.NewDQLExecutor(env.Client)

	tests := []struct {
		name              string
		query             string
		generateCanonical bool
		expectCanonical   bool
	}{
		{
			name:              "simple query with canonical",
			query:             "fetch logs | limit 10",
			generateCanonical: true,
			expectCanonical:   true,
		},
		{
			name:              "simple query without canonical",
			query:             "fetch logs | limit 10",
			generateCanonical: false,
			expectCanonical:   false,
		},
		{
			name:              "complex query with canonical",
			query:             "fetch logs | filter status == \"ERROR\" | summarize count() | limit 10",
			generateCanonical: true,
			expectCanonical:   true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			opts := exec.DQLVerifyOptions{
				GenerateCanonicalQuery: tt.generateCanonical,
			}

			result, err := executor.VerifyQuery(tt.query, opts)
			if err != nil {
				t.Fatalf("VerifyQuery() error = %v", err)
			}

			if result == nil {
				t.Error("VerifyQuery() returned nil result")
				return
			}

			if !result.Valid {
				t.Errorf("Expected valid query, got valid=%v", result.Valid)
			}

			if tt.expectCanonical {
				if result.CanonicalQuery == "" {
					t.Error("Expected canonical query, but got empty string")
				} else {
					t.Logf("✓ Canonical query generated: %s", result.CanonicalQuery)
				}
			} else {
				if result.CanonicalQuery != "" {
					t.Logf("Note: Canonical query returned even though not requested: %s", result.CanonicalQuery)
				} else {
					t.Logf("✓ No canonical query (as expected)")
				}
			}
		})
	}
}

func TestQueryVerify_TimezoneLocale(t *testing.T) {
	env := integration.SetupIntegration(t)
	defer env.Cleanup.Cleanup(t)

	executor := exec.NewDQLExecutor(env.Client)

	tests := []struct {
		name        string
		query       string
		timezone    string
		locale      string
		expectValid bool
	}{
		{
			name:        "query with UTC timezone",
			query:       "fetch logs | limit 10",
			timezone:    "UTC",
			locale:      "",
			expectValid: true,
		},
		{
			name:        "query with Europe/Paris timezone",
			query:       "fetch logs | limit 10",
			timezone:    "Europe/Paris",
			locale:      "",
			expectValid: true,
		},
		{
			name:        "query with en_US locale",
			query:       "fetch logs | limit 10",
			timezone:    "",
			locale:      "en_US",
			expectValid: true,
		},
		{
			name:        "query with timezone and locale",
			query:       "fetch logs | limit 10",
			timezone:    "Europe/Paris",
			locale:      "fr_FR",
			expectValid: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			opts := exec.DQLVerifyOptions{
				Timezone: tt.timezone,
				Locale:   tt.locale,
			}

			result, err := executor.VerifyQuery(tt.query, opts)
			if err != nil {
				t.Fatalf("VerifyQuery() error = %v", err)
			}

			if result == nil {
				t.Error("VerifyQuery() returned nil result")
				return
			}

			if result.Valid != tt.expectValid {
				t.Errorf("Expected valid=%v, got valid=%v", tt.expectValid, result.Valid)
			}

			t.Logf("✓ Query verified with timezone=%s, locale=%s (valid=%v)", tt.timezone, tt.locale, result.Valid)
		})
	}
}

func TestQueryVerify_FailOnWarn(t *testing.T) {
	env := integration.SetupIntegration(t)
	defer env.Cleanup.Cleanup(t)

	executor := exec.NewDQLExecutor(env.Client)

	tests := []struct {
		name          string
		query         string
		expectValid   bool
		expectWarning bool
	}{
		{
			name:          "simple query without warnings",
			query:         "fetch logs | limit 10",
			expectValid:   true,
			expectWarning: false,
		},
		{
			name:          "query with potential warning",
			query:         "fetch logs | limit 10",
			expectValid:   true,
			expectWarning: false, // May vary by environment
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := executor.VerifyQuery(tt.query, exec.DQLVerifyOptions{})
			if err != nil {
				t.Fatalf("VerifyQuery() error = %v", err)
			}

			if result == nil {
				t.Error("VerifyQuery() returned nil result")
				return
			}

			if result.Valid != tt.expectValid {
				t.Errorf("Expected valid=%v, got valid=%v", tt.expectValid, result.Valid)
			}

			// Check for warnings
			hasWarning := false
			for _, notification := range result.Notifications {
				if notification.Severity == "WARN" || notification.Severity == "WARNING" {
					hasWarning = true
					t.Logf("Warning found: %s - %s", notification.NotificationType, notification.Message)
				}
			}

			if tt.expectWarning && !hasWarning {
				t.Log("Note: Expected warning but none found (may vary by environment)")
			}

			t.Logf("✓ Query verified (valid=%v, warnings=%v)", result.Valid, hasWarning)
		})
	}
}

func TestQueryVerify_OutputFormats(t *testing.T) {
	env := integration.SetupIntegration(t)
	defer env.Cleanup.Cleanup(t)

	executor := exec.NewDQLExecutor(env.Client)

	query := "fetch logs | limit 10"

	tests := []struct {
		name       string
		outputFunc func(*exec.DQLVerifyResponse) error
	}{
		{
			name: "JSON output format",
			outputFunc: func(result *exec.DQLVerifyResponse) error {
				data, err := json.MarshalIndent(result, "", "  ")
				if err != nil {
					return err
				}
				// Verify it's valid JSON
				var verify map[string]interface{}
				if err := json.Unmarshal(data, &verify); err != nil {
					return err
				}
				return nil
			},
		},
		{
			name: "YAML output format",
			outputFunc: func(result *exec.DQLVerifyResponse) error {
				data, err := yaml.Marshal(result)
				if err != nil {
					return err
				}
				// Verify it's valid YAML
				var verify map[string]interface{}
				if err := yaml.Unmarshal(data, &verify); err != nil {
					return err
				}
				return nil
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := executor.VerifyQuery(query, exec.DQLVerifyOptions{})
			if err != nil {
				t.Fatalf("VerifyQuery() error = %v", err)
			}

			if result == nil {
				t.Error("VerifyQuery() returned nil result")
				return
			}

			// Test output formatting
			if err := tt.outputFunc(result); err != nil {
				t.Errorf("Output formatting failed: %v", err)
				return
			}

			t.Logf("✓ Output format validated successfully")
		})
	}
}

func TestQueryVerify_ExitCodes(t *testing.T) {
	env := integration.SetupIntegration(t)
	defer env.Cleanup.Cleanup(t)

	executor := exec.NewDQLExecutor(env.Client)

	tests := []struct {
		name         string
		query        string
		expectValid  bool
		expectError  bool
		expectedCode int // 0 = success, 1 = invalid query/error, 2 = auth error, 3 = network error
	}{
		{
			name:         "valid query should exit 0",
			query:        "fetch logs | limit 10",
			expectValid:  true,
			expectError:  false,
			expectedCode: 0,
		},
		{
			name:         "invalid query should exit 1",
			query:        "fetch logs | invalid_command",
			expectValid:  false,
			expectError:  false,
			expectedCode: 1,
		},
		{
			name:         "empty query should exit 1",
			query:        "",
			expectValid:  false,
			expectError:  false,
			expectedCode: 1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := executor.VerifyQuery(tt.query, exec.DQLVerifyOptions{})

			// Determine exit code based on result
			exitCode := 0
			if err != nil {
				// API error
				switch {
				case strings.Contains(err.Error(), "status 401") || strings.Contains(err.Error(), "status 403"):
					exitCode = 2
				case strings.Contains(err.Error(), "status 5") || strings.Contains(err.Error(), "timeout"):
					exitCode = 3
				default:
					exitCode = 1
				}
			} else if result != nil {
				if !result.Valid {
					exitCode = 1
				} else {
					// Check for ERROR notifications
					for _, notification := range result.Notifications {
						if notification.Severity == "ERROR" {
							exitCode = 1
							break
						}
					}
				}
			}

			if exitCode != tt.expectedCode {
				t.Errorf("Expected exit code %d, got %d", tt.expectedCode, exitCode)
			}

			t.Logf("✓ Exit code verified: %d (valid=%v, error=%v)", exitCode, result != nil && result.Valid, err != nil)
		})
	}
}

func TestQueryVerify_OutputSymbols(t *testing.T) {
	env := integration.SetupIntegration(t)
	defer env.Cleanup.Cleanup(t)

	executor := exec.NewDQLExecutor(env.Client)

	tests := []struct {
		name         string
		query        string
		expectValid  bool
		expectSymbol string // "✔" for valid, "✖" for invalid
	}{
		{
			name:         "valid query shows check mark",
			query:        "fetch logs | limit 10",
			expectValid:  true,
			expectSymbol: "✔",
		},
		{
			name:         "invalid query shows X mark",
			query:        "fetch logs | invalid_command",
			expectValid:  false,
			expectSymbol: "✖",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := executor.VerifyQuery(tt.query, exec.DQLVerifyOptions{})
			if err != nil {
				t.Fatalf("VerifyQuery() error = %v", err)
			}

			if result == nil {
				t.Error("VerifyQuery() returned nil result")
				return
			}

			if result.Valid != tt.expectValid {
				t.Errorf("Expected valid=%v, got valid=%v", tt.expectValid, result.Valid)
			}

			// Human-readable output would contain these symbols
			// This is handled in cmd/query.go's formatVerifyResultHuman function
			symbol := "✖"
			if result.Valid {
				symbol = "✔"
			}

			if symbol != tt.expectSymbol {
				t.Errorf("Expected symbol %s, got %s", tt.expectSymbol, symbol)
			}

			t.Logf("✓ Output symbol verified: %s (valid=%v)", symbol, result.Valid)
		})
	}
}

func TestQueryVerify_NotificationTypes(t *testing.T) {
	env := integration.SetupIntegration(t)
	defer env.Cleanup.Cleanup(t)

	executor := exec.NewDQLExecutor(env.Client)

	tests := []struct {
		name        string
		query       string
		expectValid bool
	}{
		{
			name:        "valid query",
			query:       "fetch logs | limit 10",
			expectValid: true,
		},
		{
			name:        "invalid query with syntax error",
			query:       "fetch logs | invalid_command",
			expectValid: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := executor.VerifyQuery(tt.query, exec.DQLVerifyOptions{})
			if err != nil {
				t.Fatalf("VerifyQuery() error = %v", err)
			}

			if result == nil {
				t.Error("VerifyQuery() returned nil result")
				return
			}

			if result.Valid != tt.expectValid {
				t.Errorf("Expected valid=%v, got valid=%v", tt.expectValid, result.Valid)
			}

			// Log all notifications
			for _, notification := range result.Notifications {
				t.Logf("Notification: [%s] %s - %s",
					notification.Severity,
					notification.NotificationType,
					notification.Message)

				// Verify notification structure
				if notification.Severity == "" {
					t.Error("Notification missing severity")
				}
				if notification.Message == "" {
					t.Error("Notification missing message")
				}
			}

			t.Logf("✓ Query verification complete (valid=%v, notifications=%d)", result.Valid, len(result.Notifications))
		})
	}
}
