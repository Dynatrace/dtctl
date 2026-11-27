package httpclient

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/go-resty/resty/v2"
)

// CheckResponse inspects a resty response and returns a structured [*APIError]
// if the HTTP status code indicates an error (4xx/5xx). Returns nil for
// successful responses.
//
// It attempts to parse the Dynatrace error response body for a human-readable
// message. If parsing fails, the raw response body is kept in Details (never
// in the message or in Error()).
func CheckResponse(resp *resty.Response) error {
	if resp == nil || !resp.IsError() {
		return nil
	}

	msg := resp.Status()
	details := ""

	// Try to extract message from Dynatrace error envelope.
	// Common shapes: {"error":{"message":"...","details":{...}}} or {"message":"..."}
	var envelope struct {
		Error *struct {
			Message string          `json:"message"`
			Details json.RawMessage `json:"details"`
		} `json:"error"`
		Message string `json:"message"`
	}
	const maxDetails = 1024
	if err := json.Unmarshal(resp.Body(), &envelope); err == nil {
		if envelope.Error != nil && envelope.Error.Message != "" {
			msg = envelope.Error.Message
			// The top-level message is often generic (e.g. "Constraints
			// violated."); the actionable specifics live in "details".
			details = formatErrorDetails(envelope.Error.Details)
			if len(details) > maxDetails {
				details = details[:maxDetails] + "... (truncated)"
			}
		} else if envelope.Message != "" {
			msg = envelope.Message
		}
	} else {
		// If we can't parse JSON, include raw body as details (truncated to 1KB).
		if body := resp.String(); body != "" {
			if len(body) > maxDetails {
				body = body[:maxDetails] + "... (truncated)"
			}
			details = body
		}
	}

	return NewAPIError(resp.StatusCode(), msg, details)
}

// formatErrorDetails renders the "details" of a Dynatrace platform error
// envelope into a readable string. The actionable specifics that "details"
// carries would otherwise be dropped behind a generic top-level message.
//
// A plain string is returned verbatim; a well-formed constraintViolations array
// is joined as "path: message; ..." entries. For non-standard shapes the raw
// JSON bytes are returned as-is. Returns "" when empty.
func formatErrorDetails(raw json.RawMessage) string {
	if len(raw) == 0 || string(raw) == "null" {
		return ""
	}

	// Convention allows details to be a plain string (e.g. an overload message).
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return strings.TrimSpace(s)
	}

	// Convention: details.constraintViolations is an array of objects, each with
	// a message and optional path; errorCode is an optional string giving more
	// detail than the HTTP status. Render those when present and well-formed.
	var obj struct {
		ErrorCode            string `json:"errorCode"`
		ConstraintViolations []struct {
			Path    string `json:"path"`
			Message string `json:"message"`
		} `json:"constraintViolations"`
	}
	if err := json.Unmarshal(raw, &obj); err == nil {
		violations := make([]string, 0, len(obj.ConstraintViolations))
		for _, cv := range obj.ConstraintViolations {
			if cv.Message == "" {
				continue
			}
			if cv.Path != "" {
				violations = append(violations, fmt.Sprintf("%s: %s", cv.Path, cv.Message))
			} else {
				violations = append(violations, cv.Message)
			}
		}
		if len(violations) > 0 {
			parts := violations
			if obj.ErrorCode != "" {
				parts = append([]string{fmt.Sprintf("errorCode: %s", obj.ErrorCode)}, violations...)
			}
			return strings.Join(parts, "; ")
		}
	}

	// Non-standard or malformed: dump the raw JSON so nothing is hidden.
	return strings.TrimSpace(string(raw))
}
