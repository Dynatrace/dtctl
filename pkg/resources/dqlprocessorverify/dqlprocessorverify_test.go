package dqlprocessorverify

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Dynatrace/dtctl/pkg/client"
)

const verifyPath = "/platform/openpipeline/v1/dqlProcessor/verify"

// newTestHandler wires a Handler to a mock server serving the given response body
// at the DQL processor verify endpoint. It records the last request body when
// capture is non-nil so callers can assert the forwarded options.
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
		Script:          `fieldsAdd severity = "INFO"`,
		ConfigurationID: "logs",
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
	// The options must be forwarded into the SDK request body.
	if !strings.Contains(string(body), `"configurationId"`) {
		t.Errorf("request body missing configurationId: %s", body)
	}
}

func TestHandler_Verify_Invalid(t *testing.T) {
	resp := `{
		"valid": false,
		"notifications": [{
			"severity": "ERROR",
			"message": "There's no command ` + "`parsee`" + `.",
			"syntaxPosition": {
				"start": {"line": 1, "column": 1, "index": 0},
				"end":   {"line": 1, "column": 6, "index": 5}
			}
		}]
	}`
	h := newTestHandler(t, http.StatusOK, resp, nil)

	got, err := h.Verify(VerifyOptions{Script: "parsee content"})
	if err != nil {
		t.Fatalf("Verify() error = %v", err)
	}
	if got.Valid {
		t.Errorf("Valid = true, want false")
	}
	if len(got.Notifications) != 1 {
		t.Fatalf("len(Notifications) = %d, want 1", len(got.Notifications))
	}
	if !strings.HasPrefix(got.Summary, "ERROR (1:1-1:6):") {
		t.Errorf("Summary = %q, want ERROR position prefix", got.Summary)
	}
}

func TestHandler_Verify_ServerError(t *testing.T) {
	h := newTestHandler(t, http.StatusInternalServerError, `{"error":{"message":"boom"}}`, nil)
	if _, err := h.Verify(VerifyOptions{Script: "fieldsAdd x = 1"}); err == nil {
		t.Fatal("Verify() expected error for 500, got nil")
	}
}
