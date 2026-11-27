package httpclient

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"unicode/utf8"

	"github.com/go-resty/resty/v2"
)

// CheckResponse inspects a resty response and returns a structured [*APIError]
// if the HTTP status code indicates an error (4xx/5xx). Returns nil for
// successful responses.
//
// It attempts to parse the Dynatrace error response body for a human-readable
// message. Whenever no further details can be parsed, the raw response body
// (capped, see [TruncateBody]) is kept in Details; it is never part of the
// message or of Error().
func CheckResponse(resp *resty.Response) error {
	if resp == nil || !resp.IsError() {
		return nil
	}

	return ParseAPIError(resp.StatusCode(), resp.Status(), resp.Body())
}

// ParseAPIError builds an [*APIError] from a raw error response body: the
// Dynatrace error envelope supplies the message and details, and fallbackMsg
// is used when none is found. When nothing is parsed, the capped body is kept
// in Details. It is what [CheckResponse] uses, for callers that hold only the
// status and body.
func ParseAPIError(statusCode int, fallbackMsg string, body []byte) *APIError {
	msg := fallbackMsg
	details := ""

	// Try to extract message from Dynatrace error envelope.
	// Common shapes: {"error":{"message":"...","details":{...}}} or {"message":"..."}.
	// Settings validation failures put the violations next to the message:
	// {"error":{"message":"Constraints violated.","constraintViolations":[...]}}.
	var envelope struct {
		Error *struct {
			Message              string          `json:"message"`
			Details              json.RawMessage `json:"details"`
			ConstraintViolations json.RawMessage `json:"constraintViolations"`
		} `json:"error"`
		Message string `json:"message"`
	}
	if err := json.Unmarshal(body, &envelope); err == nil {
		if envelope.Error != nil && envelope.Error.Message != "" {
			msg = envelope.Error.Message
			// The top-level message is often generic (e.g. "Constraints
			// violated."); the actionable specifics live in "details" or in
			// a sibling "constraintViolations".
			details = formatErrorDetails(envelope.Error.Details)
			if len(envelope.Error.ConstraintViolations) > 0 {
				if cv := formatErrorDetails(json.RawMessage(`{"constraintViolations":` + string(envelope.Error.ConstraintViolations) + `}`)); cv != "" {
					if details != "" {
						details += "; "
					}
					details += cv
				}
			}
		} else if envelope.Message != "" {
			msg = envelope.Message
		}
	}
	details = TruncateBody(details)
	// Nothing parsed out: keep the raw body (capped) so no explanation is lost.
	if details == "" {
		details = TruncateBody(strings.TrimSpace(string(body)))
	}

	return NewAPIError(statusCode, msg, details)
}

// formatErrorDetails renders the "details" of a Dynatrace platform error
// envelope into a readable string. The actionable specifics that "details"
// carries would otherwise be dropped behind a generic top-level message.
//
// A plain string is returned verbatim; a well-formed constraintViolations array
// is joined as "path: message; ..." entries. For non-standard shapes the raw
// JSON bytes are returned as-is. Returns "" when empty.
func formatErrorDetails(raw json.RawMessage) string {
	if t := strings.TrimSpace(string(raw)); t == "" || t == "null" || t == "{}" {
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

// MaxBodyDetails caps how much of a response body is carried in
// [APIError.Details].
const MaxBodyDetails = 1024

// TruncateBody caps s at [MaxBodyDetails] bytes (without splitting a rune) and
// marks the cut. Use it for any response body that goes into [APIError.Details].
func TruncateBody(s string) string {
	if len(s) <= MaxBodyDetails {
		return s
	}
	cut := MaxBodyDetails
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + "... (truncated)"
}

// NewAPIErrorFromBody builds an [*APIError] for a response whose body could not
// be (or was not) parsed into a message: the status text is the message and the
// capped body is the details.
func NewAPIErrorFromBody(statusCode int, body string) *APIError {
	return NewAPIError(statusCode, http.StatusText(statusCode), TruncateBody(body))
}

// ErrorText renders err together with the response body of the first
// [*APIError] in its chain, which Error() deliberately leaves out. It is the
// opt-in for user-facing output (CLI messages, hints that match on body text).
func ErrorText(err error) string {
	if err == nil {
		return ""
	}
	text := err.Error()
	var apiErr *APIError
	if errors.As(err, &apiErr) && apiErr.Details != "" && !strings.Contains(text, apiErr.Details) {
		return text + " - " + apiErr.Details
	}
	return text
}
