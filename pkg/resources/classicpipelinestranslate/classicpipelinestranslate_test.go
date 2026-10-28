package classicpipelinestranslate

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Dynatrace/dtctl/pkg/client"
)

func newTestHandler(t *testing.T, handler http.HandlerFunc) *Handler {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	c, err := client.NewForTesting(server.URL, "test-token")
	if err != nil {
		t.Fatalf("client.NewForTesting: %v", err)
	}
	return NewHandler(c)
}

func TestIsValidConfiguration(t *testing.T) {
	tests := []struct {
		scope string
		want  bool
	}{
		{"logs", true},
		{"bizevents", true},
		{"", false},
		{"metrics", false},
		{"Logs", false},
	}
	for _, tt := range tests {
		if got := IsValidConfiguration(tt.scope); got != tt.want {
			t.Errorf("IsValidConfiguration(%q) = %v, want %v", tt.scope, got, tt.want)
		}
	}
}

func TestTranslate_DecodesValueStructurally(t *testing.T) {
	h := newTestHandler(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"value":{"id":"pipe-1","processors":[{"type":"fieldsAdd"}]},"withWarning":true}`))
	})

	result, err := h.Translate(TranslateOptions{Configuration: "logs"})
	if err != nil {
		t.Fatalf("Translate() error: %v", err)
	}
	if !result.WithWarning {
		t.Errorf("WithWarning = false, want true")
	}
	// Value must be decoded into a generic structure (not a raw/escaped string)
	// so YAML/JSON printers render it structurally.
	doc, ok := result.Value.(map[string]any)
	if !ok {
		t.Fatalf("Value type = %T, want map[string]any", result.Value)
	}
	if doc["id"] != "pipe-1" {
		t.Errorf("Value[id] = %v, want %q", doc["id"], "pipe-1")
	}
}

func TestTranslate_NullValue(t *testing.T) {
	// A null/absent pipeline document is forwarded as a nil value, not an error.
	h := newTestHandler(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"value":null,"withWarning":false}`))
	})

	result, err := h.Translate(TranslateOptions{Configuration: "logs"})
	if err != nil {
		t.Fatalf("Translate() error: %v", err)
	}
	if result.Value != nil {
		t.Errorf("Value = %v, want nil", result.Value)
	}
}

func TestTranslate_PropagatesError(t *testing.T) {
	h := newTestHandler(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"error":{"message":"forbidden"}}`))
	})

	if _, err := h.Translate(TranslateOptions{Configuration: "logs"}); err == nil {
		t.Fatal("Translate() expected error for 403")
	}
}
