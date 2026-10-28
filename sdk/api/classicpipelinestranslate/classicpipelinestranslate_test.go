package classicpipelinestranslate

import (
	"context"
	"encoding/json"
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

func TestTranslate_Success(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc(basePath, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		if got := r.URL.Query().Get("configuration"); got != "logs" {
			t.Errorf("configuration = %q, want %q", got, "logs")
		}
		w.Header().Set("Content-Type", "application/json")
		// value is an arbitrary opaque pipeline document.
		_, _ = w.Write([]byte(`{"value":{"id":"pipe-1","processors":[{"type":"fieldsAdd"}]},"withWarning":false}`))
	})

	h := NewHandler(newTestClient(t, mux))
	result, err := h.Translate(context.Background(), TranslateOptions{Configuration: "logs"})
	if err != nil {
		t.Fatalf("Translate() error: %v", err)
	}
	if result.WithWarning {
		t.Errorf("WithWarning = true, want false")
	}
	// Value is forwarded verbatim as raw JSON.
	var doc map[string]any
	if err := json.Unmarshal(result.Value, &doc); err != nil {
		t.Fatalf("value not valid JSON: %v", err)
	}
	if doc["id"] != "pipe-1" {
		t.Errorf("value.id = %v, want %q", doc["id"], "pipe-1")
	}
}

func TestTranslate_WithWarning(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc(basePath, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"value":{"id":"pipe-2"},"withWarning":true}`))
	})

	h := NewHandler(newTestClient(t, mux))
	result, err := h.Translate(context.Background(), TranslateOptions{Configuration: "bizevents"})
	if err != nil {
		t.Fatalf("Translate() error: %v", err)
	}
	if !result.WithWarning {
		t.Errorf("WithWarning = false, want true")
	}
}

func TestTranslate_SendsAllQueryParamsExplicitly(t *testing.T) {
	// The three booleans must be sent on every request (so a caller can
	// override server defaults), and the scope must be forwarded verbatim.
	mux := http.NewServeMux()
	mux.HandleFunc(basePath, func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		checks := map[string]string{
			"configuration":              "bizevents",
			"includeSampleData":          "true",
			"skipDisabledRules":          "false",
			"skipBuiltinProcessingRules": "true",
		}
		for param, want := range checks {
			if got := q.Get(param); got != want {
				t.Errorf("query %q = %q, want %q", param, got, want)
			}
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"value":{},"withWarning":false}`))
	})

	h := NewHandler(newTestClient(t, mux))
	_, err := h.Translate(context.Background(), TranslateOptions{
		Configuration:              "bizevents",
		IncludeSampleData:          true,
		SkipDisabledRules:          false,
		SkipBuiltinProcessingRules: true,
	})
	if err != nil {
		t.Fatalf("Translate() error: %v", err)
	}
}

func TestTranslate_DefaultBoolsSentExplicitly(t *testing.T) {
	// With a zero-value options struct, the booleans should still be present
	// as "false" rather than omitted.
	mux := http.NewServeMux()
	mux.HandleFunc(basePath, func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		for _, param := range []string{"includeSampleData", "skipDisabledRules", "skipBuiltinProcessingRules"} {
			if !q.Has(param) {
				t.Errorf("query param %q not sent; want it present", param)
			}
			if got := q.Get(param); got != "false" {
				t.Errorf("query %q = %q, want %q", param, got, "false")
			}
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"value":{},"withWarning":false}`))
	})

	h := NewHandler(newTestClient(t, mux))
	if _, err := h.Translate(context.Background(), TranslateOptions{Configuration: "logs"}); err != nil {
		t.Fatalf("Translate() error: %v", err)
	}
}

func TestTranslate_EmptyConfiguration(t *testing.T) {
	// No HTTP call should be made for an empty configuration.
	mux := http.NewServeMux()
	mux.HandleFunc(basePath, func(w http.ResponseWriter, _ *http.Request) {
		t.Error("endpoint should not be called with an empty configuration")
		w.WriteHeader(http.StatusOK)
	})

	h := NewHandler(newTestClient(t, mux))
	if _, err := h.Translate(context.Background(), TranslateOptions{}); err == nil {
		t.Fatal("Translate() expected error for empty configuration")
	}
}

func TestTranslate_ServerError(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc(basePath, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error":{"message":"internal error"}}`))
	})

	h := NewHandler(newTestClient(t, mux))
	if _, err := h.Translate(context.Background(), TranslateOptions{Configuration: "logs"}); err == nil {
		t.Fatal("Translate() expected error for 500")
	}
}

func TestTranslate_NotFound(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc(basePath, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"error":{"message":"not found"}}`))
	})

	h := NewHandler(newTestClient(t, mux))
	if _, err := h.Translate(context.Background(), TranslateOptions{Configuration: "logs"}); err == nil {
		t.Fatal("Translate() expected error for 404")
	}
}

func TestTranslate_InvalidJSON(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc(basePath, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`not json`))
	})

	h := NewHandler(newTestClient(t, mux))
	if _, err := h.Translate(context.Background(), TranslateOptions{Configuration: "logs"}); err == nil {
		t.Fatal("Translate() expected parse error for malformed body")
	}
}
