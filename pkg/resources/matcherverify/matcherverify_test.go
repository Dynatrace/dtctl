package matcherverify

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Dynatrace/dtctl/pkg/client"
	sdkmatcher "github.com/Dynatrace/dtctl/sdk/api/matcherverify"
)

const verifyPath = "/platform/openpipeline/v1/matcher/verify"

// newTestHandler wires a Handler to a mock server serving the given response body
// at the matcher verify endpoint. It fails the test if the request method is not
// POST, and records the last request body for assertions.
func newTestHandler(t *testing.T, status int, body string, capture *[]byte) *Handler {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != verifyPath {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		if r.Method != http.MethodPost {
			t.Errorf("method = %s, want POST", r.Method)
		}
		if capture != nil {
			b, _ := io.ReadAll(r.Body)
			*capture = b
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	c, err := client.NewForTesting(srv.URL, "test-token")
	if err != nil {
		t.Fatalf("client.NewForTesting: %v", err)
	}
	return NewHandler(c)
}

func TestHandler_Verify_Valid(t *testing.T) {
	var body []byte
	h := newTestHandler(t, http.StatusOK, `{"valid":true,"notifications":[]}`, &body)

	got, err := h.Verify(VerifyOptions{
		Query:           `matchesValue(content, "error")`,
		ConfigurationID: "logs",
		Context:         "ROUTING_RULE",
	})
	if err != nil {
		t.Fatalf("Verify() error = %v", err)
	}
	if !got.Valid {
		t.Errorf("Valid = false, want true")
	}
	if len(got.Notifications) != 0 {
		t.Errorf("Notifications = %v, want empty", got.Notifications)
	}
	if got.Summary != "" {
		t.Errorf("Summary = %q, want empty", got.Summary)
	}
	// The options must be forwarded to the SDK request body.
	if !containsAll(string(body), `"query"`, `"configurationId"`, `"context"`) {
		t.Errorf("request body missing forwarded options: %s", body)
	}
}

func TestHandler_Verify_Invalid(t *testing.T) {
	resp := `{
		"valid": false,
		"notifications": [{
			"severity": "ERROR",
			"message": "unexpected token",
			"syntaxPosition": {
				"start": {"line": 1, "column": 1, "index": 0},
				"end":   {"line": 1, "column": 6, "index": 5}
			}
		}]
	}`
	h := newTestHandler(t, http.StatusOK, resp, nil)

	got, err := h.Verify(VerifyOptions{Query: "!@#$"})
	if err != nil {
		t.Fatalf("Verify() error = %v", err)
	}
	if got.Valid {
		t.Errorf("Valid = true, want false")
	}
	if len(got.Notifications) != 1 {
		t.Fatalf("len(Notifications) = %d, want 1", len(got.Notifications))
	}
	if got.Summary != "ERROR (1:1-1:6): unexpected token" {
		t.Errorf("Summary = %q", got.Summary)
	}
}

func TestHandler_Verify_ServerError(t *testing.T) {
	h := newTestHandler(t, http.StatusInternalServerError, `{"error":{"message":"boom"}}`, nil)
	if _, err := h.Verify(VerifyOptions{Query: "test"}); err == nil {
		t.Fatal("Verify() expected error for 500, got nil")
	}
}

// containsAll reports whether s contains every substring in subs.
func containsAll(s string, subs ...string) bool {
	for _, sub := range subs {
		if !strings.Contains(s, sub) {
			return false
		}
	}
	return true
}

func TestFromSDKVerifyResponse(t *testing.T) {
	sdkResp := &sdkmatcher.VerifyResponse{
		Valid: false,
		Notifications: []sdkmatcher.MetadataNotification{
			{
				Severity: "ERROR",
				Message:  "There's no command `parsee`.",
				SyntaxPosition: &sdkmatcher.SyntaxRange{
					Start: sdkmatcher.SyntaxPosition{Line: 1, Column: 1, Index: 0},
					End:   sdkmatcher.SyntaxPosition{Line: 1, Column: 6, Index: 5},
				},
			},
			{Severity: "WARN", Message: "no position"},
		},
	}

	got := FromSDKVerifyResponse(sdkResp)

	if got.Valid {
		t.Errorf("Valid = true, want false")
	}
	if len(got.Notifications) != 2 {
		t.Fatalf("Notifications len = %d, want 2", len(got.Notifications))
	}

	// Structured notifications are preserved, including the position range.
	first := got.Notifications[0]
	if first.SyntaxPosition == nil {
		t.Fatal("Notifications[0].SyntaxPosition = nil, want populated")
	}
	if first.SyntaxPosition.Start.Column != 1 || first.SyntaxPosition.End.Column != 6 {
		t.Errorf("position = %+v, want start col 1 / end col 6", first.SyntaxPosition)
	}
	// A notification without a position keeps SyntaxPosition nil.
	if got.Notifications[1].SyntaxPosition != nil {
		t.Errorf("Notifications[1].SyntaxPosition = %+v, want nil", got.Notifications[1].SyntaxPosition)
	}

	// Summary flattens both notifications, one per line.
	want := "ERROR (1:1-1:6): There's no command `parsee`.\nWARN: no position"
	if got.Summary != want {
		t.Errorf("Summary = %q, want %q", got.Summary, want)
	}
}

func TestFromSDKVerifyResponse_Valid(t *testing.T) {
	got := FromSDKVerifyResponse(&sdkmatcher.VerifyResponse{Valid: true})
	if !got.Valid {
		t.Errorf("Valid = false, want true")
	}
	if len(got.Notifications) != 0 {
		t.Errorf("Notifications = %v, want empty", got.Notifications)
	}
	if got.Summary != "" {
		t.Errorf("Summary = %q, want empty", got.Summary)
	}
}

func TestFormatNotifications(t *testing.T) {
	tests := []struct {
		name  string
		input []sdkmatcher.MetadataNotification
		want  string
	}{
		{
			name:  "empty",
			input: nil,
			want:  "",
		},
		{
			name:  "without position",
			input: []sdkmatcher.MetadataNotification{{Severity: "WARN", Message: "heads up"}},
			want:  "WARN: heads up",
		},
		{
			name: "with position",
			input: []sdkmatcher.MetadataNotification{{
				Severity: "ERROR",
				Message:  "bad",
				SyntaxPosition: &sdkmatcher.SyntaxRange{
					Start: sdkmatcher.SyntaxPosition{Line: 2, Column: 3},
					End:   sdkmatcher.SyntaxPosition{Line: 2, Column: 7},
				},
			}},
			want: "ERROR (2:3-2:7): bad",
		},
		{
			name: "multiple joined by newline",
			input: []sdkmatcher.MetadataNotification{
				{Severity: "INFO", Message: "one"},
				{Severity: "INFO", Message: "two"},
			},
			want: "INFO: one\nINFO: two",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := FormatNotifications(tt.input); got != tt.want {
				t.Errorf("FormatNotifications() = %q, want %q", got, tt.want)
			}
		})
	}
}
