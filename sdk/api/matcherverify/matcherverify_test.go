package matcherverify

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Dynatrace/dtctl/sdk/httpclient"
)

func newTestClient(t *testing.T, handler http.Handler) *httpclient.Client {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	c, err := httpclient.New(srv.URL, httpclient.WithToken("dt0c01.test"))
	if err != nil {
		t.Fatalf("httpclient.New: %v", err)
	}
	return c
}

// keysOf returns the top-level keys of a decoded JSON object, for diagnostics.
func keysOf(m map[string]json.RawMessage) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	return keys
}

func TestVerify_Valid(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc(basePath, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		var req MatcherVerifyRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if req.Query != "matchesValue(content, \"error\")" {
			t.Errorf("query = %q, want %q", req.Query, "matchesValue(content, \"error\")")
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"valid":true,"notifications":[]}`))
	})

	h := NewHandler(newTestClient(t, mux))
	result, err := h.Verify(context.Background(), MatcherVerifyRequest{
		Query: "matchesValue(content, \"error\")",
	})
	if err != nil {
		t.Fatalf("Verify() error: %v", err)
	}
	if !result.Valid {
		t.Errorf("Valid = false, want true")
	}
}

func TestVerify_Invalid(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc(basePath, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"valid": false,
			"notifications": [{
				"severity": "ERROR",
				"message": "unexpected token",
				"syntaxPosition": {
					"start": {"line": 1, "column": 1, "index": 0},
					"end":   {"line": 1, "column": 6, "index": 5}
				}
			}]
		}`))
	})

	h := NewHandler(newTestClient(t, mux))
	result, err := h.Verify(context.Background(), MatcherVerifyRequest{Query: "!@#$"})
	if err != nil {
		t.Fatalf("Verify() error: %v", err)
	}
	if result.Valid {
		t.Errorf("Valid = true, want false")
	}
	if len(result.Notifications) != 1 {
		t.Fatalf("len(Notifications) = %d, want 1", len(result.Notifications))
	}
	n := result.Notifications[0]
	if n.Severity != "ERROR" {
		t.Errorf("severity = %q, want ERROR", n.Severity)
	}
	if n.SyntaxPosition == nil {
		t.Fatal("SyntaxPosition is nil, want non-nil")
	}
	if n.SyntaxPosition.Start.Line != 1 {
		t.Errorf("start.line = %d, want 1", n.SyntaxPosition.Start.Line)
	}
}

func TestVerify_WithConfigAndContext(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc(basePath, func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}

		// Pin the exact wire key: the matcher endpoint uses "configurationId",
		// NOT "configId" (which is the preview endpoint's field). Decoding into
		// the request struct alone would not catch a wrong json tag, since the
		// client marshals with the same struct — so assert the raw key here.
		var raw map[string]json.RawMessage
		if err := json.Unmarshal(body, &raw); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if _, ok := raw["configurationId"]; !ok {
			t.Errorf("request body missing \"configurationId\" key; got %v", keysOf(raw))
		}
		if _, ok := raw["configId"]; ok {
			t.Error("request body must not use \"configId\" for the matcher endpoint")
		}

		var req MatcherVerifyRequest
		if err := json.Unmarshal(body, &req); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if req.ConfigurationID != "logs" {
			t.Errorf("configurationId = %q, want %q", req.ConfigurationID, "logs")
		}
		if req.Context != "ROUTING_RULE" {
			t.Errorf("context = %q, want %q", req.Context, "ROUTING_RULE")
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"valid":true}`))
	})

	h := NewHandler(newTestClient(t, mux))
	_, err := h.Verify(context.Background(), MatcherVerifyRequest{
		Query:           "matchesValue(content, \"test\")",
		ConfigurationID: "logs",
		Context:         "ROUTING_RULE",
	})
	if err != nil {
		t.Fatalf("Verify() error: %v", err)
	}
}

func TestVerify_ServerError(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc(basePath, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error":{"message":"internal error"}}`))
	})

	h := NewHandler(newTestClient(t, mux))
	if _, err := h.Verify(context.Background(), MatcherVerifyRequest{Query: "test"}); err == nil {
		t.Fatal("Verify() expected error for 500")
	}
}
