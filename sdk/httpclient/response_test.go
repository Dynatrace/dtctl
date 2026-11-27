package httpclient

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/go-resty/resty/v2"
)

// respFor performs a request against a server that returns the given status and
// body, yielding a *resty.Response for CheckResponse to inspect.
func respFor(t *testing.T, status int, body string) *resty.Response {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)

	resp, err := resty.New().R().Get(srv.URL)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	return resp
}

func TestCheckResponse(t *testing.T) {
	tests := []struct {
		name     string
		status   int
		body     string
		wantMsg  string
		wantDetl string
	}{
		{
			name:    "success returns nil",
			status:  200,
			body:    `{}`,
			wantMsg: "",
		},

		// --- Responses that follow the platform error convention: formatted nicely. ---
		{
			name:    "error message only",
			status:  404,
			body:    `{"error":{"message":"not found"}}`,
			wantMsg: "not found",
		},
		{
			// Convention: validation failures live in details.constraintViolations
			// (with an optional errorCode) and must not be dropped behind the
			// generic top-level message.
			name:     "constraint violations are surfaced",
			status:   400,
			body:     `{"error":{"code":400,"message":"Constraints violated.","details":{"errorCode":"InvalidPaginationToken","constraintViolations":[{"path":"detectionRules[0].filterConfig.pattern","message":"may not be null","parameterLocation":"PAYLOAD_BODY"}]}}}`,
			wantMsg:  "Constraints violated.",
			wantDetl: "errorCode: InvalidPaginationToken; detectionRules[0].filterConfig.pattern: may not be null",
		},
		{
			name:     "multiple constraint violations are surfaced",
			status:   400,
			body:     `{"error":{"code":400,"message":"Constraints violated.","details":{"constraintViolations":[{"path":"name","message":"may not be null"},{"path":"tasks[0].position.y","message":"must be greater than or equal to 1"},{"message":"at least one task is required"}]}}}`,
			wantMsg:  "Constraints violated.",
			wantDetl: "name: may not be null; tasks[0].position.y: must be greater than or equal to 1; at least one task is required",
		},
		{
			// Convention also allows details to be a plain string.
			name:     "string details are surfaced",
			status:   503,
			body:     `{"error":{"code":503,"message":"service is overloaded","details":"service is busy, good luck next time!"}}`,
			wantMsg:  "service is overloaded",
			wantDetl: "service is busy, good luck next time!",
		},

		// --- Responses that deviate from the convention: degrade gracefully, never drop. ---
		{
			// Some services key validation messages by field rather than using
			// constraintViolations. We don't guess at the shape — dump the raw
			// JSON so nothing is hidden.
			name:     "non-standard keyed details are dumped as json",
			status:   400,
			body:     `{"error":{"message":"Invalid request.","code":400,"details":{"tasks":["noop -> position -> y: Input should be greater than or equal to 1"]}}}`,
			wantMsg:  "Invalid request.",
			wantDetl: `{"tasks":["noop -> position -> y: Input should be greater than or equal to 1"]}`,
		},
		{
			// A details object with no constraintViolations is dumped as raw JSON
			// rather than interpreted.
			name:     "non-violation object details are dumped as json",
			status:   403,
			body:     `{"error":{"code":403,"message":"Insufficient permissions.","details":{"missingScopes":["document:documents:read","state:app-states:write"]}}}`,
			wantMsg:  "Insufficient permissions.",
			wantDetl: `{"missingScopes":["document:documents:read","state:app-states:write"]}`,
		},
		{
			// Body isn't the error envelope at all: keep it verbatim as details.
			name:    "unparseable body kept as details",
			status:  400,
			body:    `not json`,
			wantMsg: "400 Bad Request",
		},
		{
			name:    "null details yield no detail",
			status:  400,
			body:    `{"error":{"message":"Invalid request.","details":null}}`,
			wantMsg: "Invalid request.",
		},

		// --- Truncation: details must never bloat error strings beyond 1 KB. ---
		{
			// A plain-string details value longer than 1 KB must be truncated.
			name:    "large string details are truncated",
			status:  503,
			body:    `{"error":{"message":"overloaded","details":"` + strings.Repeat("x", 2000) + `"}}`,
			wantMsg: "overloaded",
			// Details field must be capped at 1024 chars + truncation marker.
			wantDetl: strings.Repeat("x", 1024) + "... (truncated)",
		},
		{
			// A non-standard details object whose raw JSON exceeds 1 KB must be truncated.
			name:    "large raw-json details are truncated",
			status:  400,
			body:    `{"error":{"message":"bad","details":{"items":["` + strings.Repeat("y", 2000) + `"]}}}`,
			wantMsg: "bad",
			// Raw JSON fallback also subject to 1 KB cap.
			wantDetl: (`{"items":["` + strings.Repeat("y", 2000) + `"]}`)[:1024] + "... (truncated)",
		},
		{
			// An unparseable body longer than 1 KB must also be truncated.
			name:     "large unparseable body is truncated",
			status:   500,
			body:     strings.Repeat("z", 2000),
			wantMsg:  "500 Internal Server Error",
			wantDetl: strings.Repeat("z", 1024) + "... (truncated)",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := CheckResponse(respFor(t, tt.status, tt.body))
			if tt.wantMsg == "" {
				if err != nil {
					t.Fatalf("expected nil, got %v", err)
				}
				return
			}
			apiErr, ok := err.(*APIError)
			if !ok {
				t.Fatalf("expected *APIError, got %T: %v", err, err)
			}
			if apiErr.Message != tt.wantMsg {
				t.Errorf("Message = %q, want %q", apiErr.Message, tt.wantMsg)
			}
			if tt.wantDetl != "" && apiErr.Details != tt.wantDetl {
				t.Errorf("Details = %q, want %q", apiErr.Details, tt.wantDetl)
			}
			if tt.name == "null details yield no detail" && apiErr.Details != tt.body {
				t.Errorf("Details = %q, want the raw body", apiErr.Details)
			}
			if tt.name == "unparseable body kept as details" && !strings.Contains(apiErr.Details, "not json") {
				t.Errorf("Details = %q, want to contain raw body", apiErr.Details)
			}
		})
	}
}

// The raw body (here carrying a secret-looking value) must reach callers only
// through Details, never through Error().
func TestCheckResponse_ErrorOmitsBody(t *testing.T) {
	const secret = "token-dt0c01.SECRET"
	for name, body := range map[string]string{
		"unparseable body":    "upstream said: " + secret,
		"envelope details":    `{"error":{"message":"Invalid request.","details":"` + secret + `"}}`,
		"constraint violated": `{"error":{"message":"Constraints violated.","details":{"constraintViolations":[{"path":"a","message":"` + secret + `"}]}}}`,
	} {
		t.Run(name, func(t *testing.T) {
			err := CheckResponse(respFor(t, 400, body))
			apiErr, ok := err.(*APIError)
			if !ok {
				t.Fatalf("expected *APIError, got %T", err)
			}
			if strings.Contains(err.Error(), secret) {
				t.Errorf("Error() leaks the body: %q", err.Error())
			}
			if !strings.Contains(apiErr.Details, secret) {
				t.Errorf("Details = %q, want the body", apiErr.Details)
			}
		})
	}
}

func TestCheckResponse_SiblingConstraintViolations(t *testing.T) {
	body := `{"error":{"code":400,"message":"Constraints violated.","constraintViolations":[{"path":"scope","message":"is required"},{"message":"bad value"}]}}`
	apiErr := CheckResponse(respFor(t, 400, body)).(*APIError)
	if apiErr.Message != "Constraints violated." {
		t.Errorf("Message = %q", apiErr.Message)
	}
	if apiErr.Details != "scope: is required; bad value" {
		t.Errorf("Details = %q", apiErr.Details)
	}
	if strings.Contains(apiErr.Error(), "scope") {
		t.Errorf("Error() leaks details: %q", apiErr.Error())
	}
}

func TestCheckResponse_RawBodyKeptWhenNothingParsed(t *testing.T) {
	body := `{"error":{"message":"Nope.","extra":{"a":1}}}`
	apiErr := CheckResponse(respFor(t, 400, body)).(*APIError)
	if apiErr.Details != body {
		t.Errorf("Details = %q, want raw body", apiErr.Details)
	}
	long := strings.Repeat("y", 3000)
	apiErr = CheckResponse(respFor(t, 500, `{"error":{"message":"m","extra":"`+long+`"}}`)).(*APIError)
	if len(apiErr.Details) > MaxBodyDetails+len("... (truncated)") {
		t.Errorf("Details not capped: %d bytes", len(apiErr.Details))
	}
}

func TestTruncateBodyAndErrorText(t *testing.T) {
	if got := TruncateBody("short"); got != "short" {
		t.Errorf("got %q", got)
	}
	got := TruncateBody(strings.Repeat("é", 800)) // 1600 bytes, multi-byte
	if !strings.HasSuffix(got, "... (truncated)") || !utf8.ValidString(got) {
		t.Errorf("bad truncation: valid=%v", utf8.ValidString(got))
	}
	e := fmt.Errorf("op: %w", NewAPIError(400, "bad", "the body"))
	if ErrorText(e) != "op: API error (400): bad - the body" {
		t.Errorf("ErrorText = %q", ErrorText(e))
	}
	if ErrorText(errors.New("x")) != "x" || ErrorText(nil) != "" {
		t.Error("plain errors must pass through")
	}
	if a := NewAPIErrorFromBody(502, strings.Repeat("z", 2000)); a.Message != "Bad Gateway" || len(a.Details) > MaxBodyDetails+20 {
		t.Errorf("NewAPIErrorFromBody = %+v", a)
	}
}
