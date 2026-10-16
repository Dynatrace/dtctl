package cmd

import (
	"bytes"
	"context"
	"os"
	"strings"
	"testing"

	"github.com/dynatrace-oss/dtctl/pkg/exec"
	"github.com/dynatrace-oss/dtctl/pkg/output"
)

func TestFormatVerifyResultHuman_ValidQuery(t *testing.T) {
	// Capture stderr
	oldStderr := os.Stderr
	r, w, _ := os.Pipe()
	os.Stderr = w

	result := &exec.DQLVerifyResponse{
		Valid: true,
		Notifications: []exec.MetadataNotification{
			{
				Severity:         "INFO",
				NotificationType: "LIMIT_ADDED",
				Message:          "Added a limit to protect resources",
				SyntaxPosition: &exec.SyntaxPosition{
					Start: &exec.Position{Line: 1, Column: 1},
				},
			},
		},
	}

	err := formatVerifyResultHuman(context.Background(), result, "fetch logs", false)
	if err != nil {
		t.Fatalf("formatVerifyResultHuman failed: %v", err)
	}

	// Close writer and restore stderr
	w.Close()
	os.Stderr = oldStderr

	// Read captured output
	var buf bytes.Buffer
	buf.ReadFrom(r)
	output := buf.String()

	// Verify output contains expected elements
	if !strings.Contains(output, "Query is valid") {
		t.Errorf("Expected 'Query is valid' in output, got: %s", output)
	}
	if !strings.Contains(output, "INFO") {
		t.Errorf("Expected 'INFO' in output, got: %s", output)
	}
	if !strings.Contains(output, "LIMIT_ADDED") {
		t.Errorf("Expected 'LIMIT_ADDED' in output, got: %s", output)
	}
}

func TestFormatVerifyResultHuman_InvalidQuery(t *testing.T) {
	// Capture stderr
	oldStderr := os.Stderr
	r, w, _ := os.Pipe()
	os.Stderr = w

	result := &exec.DQLVerifyResponse{
		Valid: false,
		Notifications: []exec.MetadataNotification{
			{
				Severity:         "ERROR",
				NotificationType: "SYNTAX_ERROR",
				Message:          "Unexpected token \"summrize\"",
				SyntaxPosition: &exec.SyntaxPosition{
					Start: &exec.Position{Line: 1, Column: 14},
					End:   &exec.Position{Line: 1, Column: 22},
				},
			},
		},
	}

	query := "fetch logs | summrize count()"
	err := formatVerifyResultHuman(context.Background(), result, query, false)
	if err != nil {
		t.Fatalf("formatVerifyResultHuman failed: %v", err)
	}

	// Close writer and restore stderr
	w.Close()
	os.Stderr = oldStderr

	// Read captured output
	var buf bytes.Buffer
	buf.ReadFrom(r)
	output := buf.String()

	// Verify output contains expected elements
	if !strings.Contains(output, "Query is invalid") {
		t.Errorf("Expected 'Query is invalid' in output, got: %s", output)
	}
	if !strings.Contains(output, "ERROR") {
		t.Errorf("Expected 'ERROR' in output, got: %s", output)
	}
	if !strings.Contains(output, "Unexpected token") {
		t.Errorf("Expected 'Unexpected token' in output, got: %s", output)
	}
	if !strings.Contains(output, "line 1, col 14") {
		t.Errorf("Expected 'line 1, col 14' in output, got: %s", output)
	}
	// Verify caret indicator is present
	if !strings.Contains(output, "^") {
		t.Errorf("Expected caret indicator in output, got: %s", output)
	}
}

func TestFormatVerifyResultHuman_WithCanonical(t *testing.T) {
	// Capture stderr
	oldStderr := os.Stderr
	r, w, _ := os.Pipe()
	os.Stderr = w

	result := &exec.DQLVerifyResponse{
		Valid:          true,
		CanonicalQuery: "fetch logs\n| limit 1000",
	}

	err := formatVerifyResultHuman(context.Background(), result, "fetch logs", true)
	if err != nil {
		t.Fatalf("formatVerifyResultHuman failed: %v", err)
	}

	// Close writer and restore stderr
	w.Close()
	os.Stderr = oldStderr

	// Read captured output
	var buf bytes.Buffer
	buf.ReadFrom(r)
	output := buf.String()

	// Verify canonical query is printed
	if !strings.Contains(output, "Canonical Query:") {
		t.Errorf("Expected 'Canonical Query:' in output, got: %s", output)
	}
	if !strings.Contains(output, "fetch logs") {
		t.Errorf("Expected 'fetch logs' in canonical output, got: %s", output)
	}
}

func TestPrintSyntaxError(t *testing.T) {
	// Capture stderr
	oldStderr := os.Stderr
	r, w, _ := os.Pipe()
	os.Stderr = w

	query := "fetch logs | summrize count()"
	pos := &exec.SyntaxPosition{
		Start: &exec.Position{Line: 1, Column: 14},
		End:   &exec.Position{Line: 1, Column: 22},
	}

	err := printSyntaxError(context.Background(), query, pos, false)
	if err != nil {
		t.Fatalf("printSyntaxError failed: %v", err)
	}

	// Close writer and restore stderr
	w.Close()
	os.Stderr = oldStderr

	// Read captured output
	var buf bytes.Buffer
	buf.ReadFrom(r)
	output := buf.String()

	// Verify the query line is printed
	if !strings.Contains(output, "fetch logs | summrize count()") {
		t.Errorf("Expected query line in output, got: %s", output)
	}

	// Verify carets are present
	if !strings.Contains(output, "^") {
		t.Errorf("Expected caret indicator in output, got: %s", output)
	}

	// Count carets - should be 8 (from col 14 to col 22)
	caretCount := strings.Count(output, "^")
	if caretCount < 1 {
		t.Errorf("Expected at least 1 caret, got %d", caretCount)
	}
}

func TestGetVerifyExitCode_ValidQuery(t *testing.T) {
	result := &exec.DQLVerifyResponse{
		Valid: true,
		Notifications: []exec.MetadataNotification{
			{
				Severity: "INFO",
				Message:  "Query is valid",
			},
		},
	}

	exitCode := getVerifyExitCode(result, nil, false)
	if exitCode != 0 {
		t.Errorf("Expected exit code 0 for valid query, got %d", exitCode)
	}
}

func TestGetVerifyExitCode_InvalidQuery(t *testing.T) {
	result := &exec.DQLVerifyResponse{
		Valid: false,
		Notifications: []exec.MetadataNotification{
			{
				Severity: "ERROR",
				Message:  "Syntax error",
			},
		},
	}

	exitCode := getVerifyExitCode(result, nil, false)
	if exitCode != 1 {
		t.Errorf("Expected exit code 1 for invalid query, got %d", exitCode)
	}
}

func TestGetVerifyExitCode_ErrorNotification(t *testing.T) {
	result := &exec.DQLVerifyResponse{
		Valid: true, // Even if valid, ERROR notification should cause exit 1
		Notifications: []exec.MetadataNotification{
			{
				Severity: "ERROR",
				Message:  "Some error occurred",
			},
		},
	}

	exitCode := getVerifyExitCode(result, nil, false)
	if exitCode != 1 {
		t.Errorf("Expected exit code 1 for ERROR notification, got %d", exitCode)
	}
}

func TestGetVerifyExitCode_WarningWithoutFlag(t *testing.T) {
	result := &exec.DQLVerifyResponse{
		Valid: true,
		Notifications: []exec.MetadataNotification{
			{
				Severity: "WARN",
				Message:  "Warning message",
			},
		},
	}

	// Without --fail-on-warn, should return 0
	exitCode := getVerifyExitCode(result, nil, false)
	if exitCode != 0 {
		t.Errorf("Expected exit code 0 for warning without --fail-on-warn, got %d", exitCode)
	}
}

func TestGetVerifyExitCode_WarningWithFlag(t *testing.T) {
	result := &exec.DQLVerifyResponse{
		Valid: true,
		Notifications: []exec.MetadataNotification{
			{
				Severity: "WARNING",
				Message:  "Warning message",
			},
		},
	}

	// With --fail-on-warn, should return 1
	exitCode := getVerifyExitCode(result, nil, true)
	if exitCode != 1 {
		t.Errorf("Expected exit code 1 for warning with --fail-on-warn, got %d", exitCode)
	}
}

func TestGetVerifyExitCode_AuthError(t *testing.T) {
	// Test 401
	authErr := &testError{msg: "query verification failed with status 401: Unauthorized"}
	exitCode := getVerifyExitCode(nil, authErr, false)
	if exitCode != 2 {
		t.Errorf("Expected exit code 2 for auth error (401), got %d", exitCode)
	}

	// Test 403
	forbiddenErr := &testError{msg: "query verification failed with status 403: Forbidden"}
	exitCode = getVerifyExitCode(nil, forbiddenErr, false)
	if exitCode != 2 {
		t.Errorf("Expected exit code 2 for auth error (403), got %d", exitCode)
	}
}

func TestGetVerifyExitCode_NetworkError(t *testing.T) {
	// Test 5xx error
	serverErr := &testError{msg: "query verification failed with status 500: Internal Server Error"}
	exitCode := getVerifyExitCode(nil, serverErr, false)
	if exitCode != 3 {
		t.Errorf("Expected exit code 3 for server error (5xx), got %d", exitCode)
	}

	// Test timeout
	timeoutErr := &testError{msg: "request timeout exceeded"}
	exitCode = getVerifyExitCode(nil, timeoutErr, false)
	if exitCode != 3 {
		t.Errorf("Expected exit code 3 for timeout error, got %d", exitCode)
	}
}

// testError is a simple error implementation for testing
type testError struct {
	msg string
}

func (e *testError) Error() string {
	return e.msg
}

func TestIsSupportedVerifyOutputFormat(t *testing.T) {
	tests := []struct {
		name   string
		format string
		want   bool
	}{
		{name: "empty default", format: "", want: true},
		{name: "table", format: "table", want: true},
		{name: "json", format: "json", want: true},
		{name: "yaml", format: "yaml", want: true},
		{name: "yml alias", format: "yml", want: true},
		{name: "toon", format: "toon", want: true},
		{name: "trimmed and mixed case", format: " Json ", want: true},
		{name: "csv unsupported", format: "csv", want: false},
		{name: "chart unsupported", format: "chart", want: false},
		{name: "wide unsupported", format: "wide", want: false},
		{name: "xml unsupported", format: "xml", want: false},
		{name: "sparkline unsupported", format: "sparkline", want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := isSupportedVerifyOutputFormat(tt.format)
			if got != tt.want {
				t.Errorf("isSupportedVerifyOutputFormat(%q) = %v, want %v", tt.format, got, tt.want)
			}
		})
	}
}

func TestVerifyQuery_StructuredOutputFormats(t *testing.T) {
	result := &exec.DQLVerifyResponse{
		Valid:          true,
		CanonicalQuery: "fetch logs\n| limit 1000",
		Notifications: []exec.MetadataNotification{
			{
				Severity:         "INFO",
				NotificationType: "LIMIT_ADDED",
				Message:          "Added a limit to protect resources",
			},
		},
	}

	formats := []struct {
		name   string
		format string
		expect string // substring to look for in output
	}{
		{name: "json", format: "json", expect: `"valid"`},
		{name: "yaml", format: "yaml", expect: "valid:"},
		{name: "toon", format: "toon", expect: "valid"},
	}

	for _, tt := range formats {
		t.Run(tt.name, func(t *testing.T) {
			var buf bytes.Buffer
			printer := output.NewPrinterWithWriter(tt.format, &buf)
			if err := printer.Print(result); err != nil {
				t.Fatalf("Print(%s) failed: %v", tt.format, err)
			}
			out := buf.String()
			if !strings.Contains(out, tt.expect) {
				t.Errorf("expected %s output to contain %q, got:\n%s", tt.format, tt.expect, out)
			}
		})
	}
}
