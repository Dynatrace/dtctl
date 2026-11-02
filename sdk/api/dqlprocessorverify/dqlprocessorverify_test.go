package dqlprocessorverify

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
		var req DQLProcessorVerifyRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if req.Script != "fieldsAdd foo = 1" {
			t.Errorf("script = %q, want %q", req.Script, "fieldsAdd foo = 1")
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"valid":true,"notifications":[]}`))
	})

	h := NewHandler(newTestClient(t, mux))
	result, err := h.Verify(context.Background(), DQLProcessorVerifyRequest{
		Script: "fieldsAdd foo = 1",
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
	mux.HandleFunc(basePath, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"valid": false,
			"notifications": [{
				"severity": "ERROR",
				"message": "syntax error near '!'",
				"syntaxPosition": {
					"start": {"line": 1, "column": 1, "index": 0},
					"end":   {"line": 1, "column": 1, "index": 0}
				}
			}]
		}`))
	})

	h := NewHandler(newTestClient(t, mux))
	result, err := h.Verify(context.Background(), DQLProcessorVerifyRequest{Script: "!invalid"})
	if err != nil {
		t.Fatalf("Verify() error: %v", err)
	}
	if result.Valid {
		t.Errorf("Valid = true, want false")
	}
	if len(result.Notifications) != 1 {
		t.Fatalf("len(Notifications) = %d, want 1", len(result.Notifications))
	}
}

func TestVerify_WithConfigID(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc(basePath, func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}

		// Pin the exact wire key: the DQL processor verify endpoint uses
		// "configurationId", NOT "configId" (the preview endpoint's field).
		// Decoding into the request struct alone would not catch a wrong json
		// tag, since the client marshals with the same struct.
		var raw map[string]json.RawMessage
		if err := json.Unmarshal(body, &raw); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if _, ok := raw["configurationId"]; !ok {
			t.Errorf("request body missing \"configurationId\" key; got %v", keysOf(raw))
		}
		if _, ok := raw["configId"]; ok {
			t.Error("request body must not use \"configId\" for the DQL processor endpoint")
		}

		var req DQLProcessorVerifyRequest
		if err := json.Unmarshal(body, &req); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if req.ConfigurationID != "logs" {
			t.Errorf("configurationId = %q, want %q", req.ConfigurationID, "logs")
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"valid":true}`))
	})

	h := NewHandler(newTestClient(t, mux))
	_, err := h.Verify(context.Background(), DQLProcessorVerifyRequest{
		Script:          "fieldsAdd x = 1",
		ConfigurationID: "logs",
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
	if _, err := h.Verify(context.Background(), DQLProcessorVerifyRequest{Script: "test"}); err == nil {
		t.Fatal("Verify() expected error for 500")
	}
}
