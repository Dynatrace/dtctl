package diagnostic

import (
	"errors"
	"strings"
	"testing"

	"github.com/Dynatrace/dtctl/pkg/client"
	"github.com/Dynatrace/dtctl/sdk/httpclient"
)

func TestError_Error(t *testing.T) {
	tests := []struct {
		name     string
		err      *Error
		contains []string // Strings that should be in the output
	}{
		{
			name: "basic error with operation and status code",
			err: &Error{
				Operation:  "get workflows",
				StatusCode: 404,
				Message:    "workflow not found",
			},
			contains: []string{
				"Failed to get workflows",
				"HTTP 404",
				"workflow not found",
			},
		},
		{
			name: "error with request ID",
			err: &Error{
				Operation:  "create dashboard",
				StatusCode: 500,
				Message:    "internal server error",
				RequestID:  "abc-123-def",
			},
			contains: []string{
				"Failed to create dashboard",
				"HTTP 500",
				"internal server error",
				"Request ID: abc-123-def",
			},
		},
		{
			name: "error with suggestions",
			err: &Error{
				Operation:  "delete slo",
				StatusCode: 403,
				Message:    "forbidden",
				Suggestions: []string{
					"Check permissions",
					"Verify token scopes",
				},
			},
			contains: []string{
				"Failed to delete slo",
				"HTTP 403",
				"forbidden",
				"Troubleshooting suggestions:",
				"Check permissions",
				"Verify token scopes",
			},
		},
		{
			name: "error without operation",
			err: &Error{
				StatusCode: 401,
				Message:    "unauthorized",
			},
			contains: []string{
				"Operation failed",
				"HTTP 401",
				"unauthorized",
			},
		},
		{
			name: "error with underlying error but no message",
			err: &Error{
				Operation: "query",
				Err:       errors.New("connection refused"),
			},
			contains: []string{
				"Failed to query",
				"connection refused",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.err.Error()

			for _, want := range tt.contains {
				if !strings.Contains(got, want) {
					t.Errorf("Error() output missing expected string\ngot: %s\nwant substring: %s", got, want)
				}
			}
		})
	}
}

func TestError_ExitCode(t *testing.T) {
	tests := []struct {
		name       string
		statusCode int
		want       int
	}{
		{"401 unauthorized", 401, client.ExitAuthError},
		{"403 forbidden", 403, client.ExitPermissionError},
		{"404 not found", 404, client.ExitNotFoundError},
		{"400 bad request", 400, client.ExitError},
		{"500 server error", 500, client.ExitError},
		{"0 no status code", 0, client.ExitError},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := &Error{StatusCode: tt.statusCode}
			if got := err.ExitCode(); got != tt.want {
				t.Errorf("ExitCode() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestSuggestionsForStatusCode(t *testing.T) {
	tests := []struct {
		name         string
		statusCode   int
		wantCount    int    // Minimum number of suggestions
		wantContains string // At least one suggestion should contain this
	}{
		{
			name:         "401 has auth suggestions",
			statusCode:   401,
			wantCount:    1,
			wantContains: "token",
		},
		{
			name:         "403 has permission suggestions",
			statusCode:   403,
			wantCount:    1,
			wantContains: "permission",
		},
		{
			name:         "404 has not found suggestions",
			statusCode:   404,
			wantCount:    1,
			wantContains: "not found",
		},
		{
			name:         "429 has rate limit suggestions",
			statusCode:   429,
			wantCount:    1,
			wantContains: "rate limit",
		},
		{
			name:         "500 has server error suggestions",
			statusCode:   500,
			wantCount:    1,
			wantContains: "server error",
		},
		{
			name:         "502 has server error suggestions",
			statusCode:   502,
			wantCount:    1,
			wantContains: "server error",
		},
		{
			name:         "400 has validation suggestions",
			statusCode:   400,
			wantCount:    1,
			wantContains: "request",
		},
		{
			name:         "409 has conflict suggestions",
			statusCode:   409,
			wantCount:    1,
			wantContains: "conflict",
		},
		{
			name:       "200 has no suggestions",
			statusCode: 200,
			wantCount:  0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := SuggestionsForStatusCode(tt.statusCode)

			if len(got) < tt.wantCount {
				t.Errorf("SuggestionsForStatusCode(%d) returned %d suggestions, want at least %d",
					tt.statusCode, len(got), tt.wantCount)
			}

			if tt.wantContains != "" {
				found := false
				combined := strings.ToLower(strings.Join(got, " "))
				if strings.Contains(combined, strings.ToLower(tt.wantContains)) {
					found = true
				}
				if !found {
					t.Errorf("SuggestionsForStatusCode(%d) suggestions don't contain %q\ngot: %v",
						tt.statusCode, tt.wantContains, got)
				}
			}
		})
	}
}

func TestError_AddSuggestion(t *testing.T) {
	err := &Error{
		Operation: "test",
		Message:   "error",
	}

	// Add first suggestion
	err.AddSuggestion("first suggestion")
	if len(err.Suggestions) != 1 {
		t.Errorf("Expected 1 suggestion, got %d", len(err.Suggestions))
	}
	if err.Suggestions[0] != "first suggestion" {
		t.Errorf("Suggestion = %q, want %q", err.Suggestions[0], "first suggestion")
	}

	// Add second suggestion
	err.AddSuggestion("second suggestion")
	if len(err.Suggestions) != 2 {
		t.Errorf("Expected 2 suggestions, got %d", len(err.Suggestions))
	}

	// Empty suggestion should be ignored
	err.AddSuggestion("")
	if len(err.Suggestions) != 2 {
		t.Errorf("Empty suggestion should not be added, got %d suggestions", len(err.Suggestions))
	}
}

func TestError_WithRequestID(t *testing.T) {
	err := &Error{Operation: "test"}
	err.WithRequestID("req-123")

	if err.RequestID != "req-123" {
		t.Errorf("RequestID = %q, want %q", err.RequestID, "req-123")
	}
}

func TestError_WithStatusCode(t *testing.T) {
	err := &Error{Operation: "test"}
	err.WithStatusCode(404)

	if err.StatusCode != 404 {
		t.Errorf("StatusCode = %d, want %d", err.StatusCode, 404)
	}

	// Should add suggestions for 404
	if len(err.Suggestions) == 0 {
		t.Error("Expected suggestions to be added for 404 status code")
	}
}

func TestError_Unwrap(t *testing.T) {
	originalErr := errors.New("original error")
	err := &Error{
		Operation: "test",
		Err:       originalErr,
	}

	if unwrapped := err.Unwrap(); unwrapped != originalErr {
		t.Errorf("Unwrap() = %v, want %v", unwrapped, originalErr)
	}
}

func TestError_ChainedWrapping(t *testing.T) {
	// Test that errors.Is and errors.As work with our wrapped errors
	originalErr := httpclient.NewAPIError(404, "not found", "")
	wrappedErr := &Error{Operation: "get workflow", StatusCode: 404, Err: originalErr}

	// Should be able to unwrap to find the original error
	if !errors.Is(wrappedErr, originalErr) {
		t.Error("errors.Is should find the original error")
	}

	var apiErr *httpclient.APIError
	if !errors.As(wrappedErr, &apiErr) {
		t.Error("errors.As should extract APIError from wrapped error")
	}
	if apiErr.StatusCode != 404 {
		t.Errorf("Extracted APIError StatusCode = %d, want 404", apiErr.StatusCode)
	}
}
