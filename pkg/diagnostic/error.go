package diagnostic

import (
	"fmt"
	"strings"

	"github.com/Dynatrace/dtctl/pkg/client"
)

// Error represents an enhanced diagnostic error with contextual help
type Error struct {
	Operation   string   // e.g., "get workflows", "apply dashboard"
	StatusCode  int      // HTTP status code
	Message     string   // Error message from API
	RequestID   string   // Request ID for support escalation
	Suggestions []string // Actionable troubleshooting suggestions
	Err         error    // Underlying error
}

// Error implements the error interface
func (e *Error) Error() string {
	var sb strings.Builder

	// Basic error info
	if e.Operation != "" {
		sb.WriteString(fmt.Sprintf("Failed to %s", e.Operation))
	} else {
		sb.WriteString("Operation failed")
	}

	if e.StatusCode > 0 {
		sb.WriteString(fmt.Sprintf(" (HTTP %d)", e.StatusCode))
	}

	sb.WriteString(": ")

	if e.Message != "" {
		sb.WriteString(e.Message)
	} else if e.Err != nil {
		sb.WriteString(e.Err.Error())
	}

	// Add request ID if available
	if e.RequestID != "" {
		sb.WriteString(fmt.Sprintf("\nRequest ID: %s", e.RequestID))
	}

	// Add suggestions if available and not in plain mode
	if len(e.Suggestions) > 0 {
		sb.WriteString("\n\nTroubleshooting suggestions:")
		for _, suggestion := range e.Suggestions {
			sb.WriteString("\n  • " + suggestion)
		}
	}

	return sb.String()
}

// Unwrap returns the underlying error for errors.Is/As compatibility
func (e *Error) Unwrap() error {
	return e.Err
}

// ExitCode returns the appropriate exit code for this error
func (e *Error) ExitCode() int {
	return client.ExitCodeForStatus(e.StatusCode)
}

// SuggestionsForStatusCode returns troubleshooting suggestions for an HTTP status
// code. Exported so the agent envelope can carry the same advice for a bare SDK
// HTTP failure that a diagnostic.Error carries for a wrapped one.
func SuggestionsForStatusCode(statusCode int) []string {
	switch statusCode {
	case 401:
		return []string{
			"Token may be expired or invalid. Run 'dtctl config get-context' to check your configuration",
			"Verify your API token has not been revoked in the Dynatrace console",
			"Try refreshing your authentication with 'dtctl context set' and a new token",
		}
	case 403:
		return []string{
			"Insufficient permissions. Check that your API token has the required scopes",
			"Re-run the same command with --check-scopes to verify the scopes it requires",
			"Token scopes and IAM policy permissions are separate gates — if --check-scopes reports \"ok\", or \"unknown\" because the token is not introspectable, the tenant's IAM policy is what denies the request",
			"View current context and safety level: 'dtctl config get-context'",
			"If using a 'readonly' context, switch to a context with write permissions",
			"Review required token scopes in the documentation",
		}
	case 404:
		return []string{
			"Resource not found. Verify the resource name or ID is correct",
			"List available resources: 'dtctl get <resource-type>'",
			"Check if the resource was deleted or renamed",
		}
	case 429:
		return []string{
			"Rate limit exceeded. dtctl will automatically retry with exponential backoff",
			"If rate limits persist, consider reducing concurrency or spacing out requests",
			"Check the rate limit headers in debug mode: 'dtctl --debug <command>'",
		}
	case 500, 502, 503, 504:
		return []string{
			"Dynatrace API server error. This is usually temporary",
			"Check Dynatrace platform status at your environment's status page",
			"dtctl will automatically retry the request (3 attempts)",
			"If the issue persists, contact Dynatrace support with the Request ID above",
		}
	case 400:
		return []string{
			"Invalid request. Check that the resource definition is valid",
			"Validate your YAML/JSON file: 'dtctl apply --dry-run -f <file>'",
			"Review the error message for specific validation failures",
		}
	case 409:
		return []string{
			"Resource conflict. The resource may already exist or be in use",
			"For updates, use 'dtctl apply' instead of 'dtctl create'",
			"Check if another process is modifying the same resource",
		}
	default:
		if statusCode >= 500 {
			return []string{
				"Server error. This is usually temporary",
				"dtctl will automatically retry the request",
				"Contact support if the issue persists, providing the Request ID above",
			}
		}
		return nil
	}
}

// AddSuggestion adds a custom suggestion to the error
func (e *Error) AddSuggestion(suggestion string) *Error {
	if suggestion != "" {
		e.Suggestions = append(e.Suggestions, suggestion)
	}
	return e
}

// WithRequestID adds a request ID to the error
func (e *Error) WithRequestID(requestID string) *Error {
	e.RequestID = requestID
	return e
}

// WithStatusCode sets the status code (useful when wrapping non-API errors)
func (e *Error) WithStatusCode(statusCode int) *Error {
	e.StatusCode = statusCode
	if len(e.Suggestions) == 0 {
		e.Suggestions = SuggestionsForStatusCode(statusCode)
	}
	return e
}
