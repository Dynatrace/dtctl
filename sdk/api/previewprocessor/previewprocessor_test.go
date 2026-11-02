package previewprocessor

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
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

// sampleEnvelope mirrors the real request shape: a processor definition (with
// its sample data embedded under sampleData) wrapped with the config scope.
var sampleEnvelope = json.RawMessage(`{
	"processor": {"type": "dql", "matcher": "true", "dqlScript": "fieldsAdd foo = 1", "sampleData": "{\"content\":\"hello\"}"},
	"configId": "logs"
}`)

func TestPreview_Success(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc(basePath, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		// The SDK must forward the envelope verbatim (it is a oneOf union the
		// SDK deliberately does not interpret) so that fields built upstream —
		// e.g. the top-level "configId" — reach the wire unchanged.
		body, err := io.ReadAll(r.Body)
		if err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if !bytes.Equal(body, sampleEnvelope) {
			t.Errorf("request body not forwarded verbatim:\n got: %s\nwant: %s", body, sampleEnvelope)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"results": [
				{"matched": true, "record": {"content": "hello", "foo": 1}},
				{"matched": false, "record": {"content": "world"}}
			]
		}`))
	})

	h := NewHandler(newTestClient(t, mux))
	result, err := h.Preview(context.Background(), sampleEnvelope)
	if err != nil {
		t.Fatalf("Preview() error: %v", err)
	}
	if len(result.Results) != 2 {
		t.Fatalf("len(Results) = %d, want 2", len(result.Results))
	}
	if !result.Results[0].Matched {
		t.Errorf("Results[0].Matched = false, want true")
	}
	if result.Results[1].Matched {
		t.Errorf("Results[1].Matched = true, want false")
	}
}

func TestPreview_NotFound(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc(basePath, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"error":{"message":"not found"}}`))
	})

	h := NewHandler(newTestClient(t, mux))
	_, err := h.Preview(context.Background(), sampleEnvelope)
	if err == nil {
		t.Fatal("Preview() expected error for 404")
	}
	if msg := err.Error(); !strings.Contains(msg, "not found") {
		t.Errorf("expected not-found message, got: %v", err)
	}
}

func TestPreview_Timeout(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc(basePath, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusRequestTimeout)
	})

	h := NewHandler(newTestClient(t, mux))
	_, err := h.Preview(context.Background(), sampleEnvelope)
	if err == nil {
		t.Fatal("Preview() expected error for 408")
	}
	if msg := err.Error(); !strings.Contains(msg, "timed out") {
		t.Errorf("expected timeout message, got: %v", err)
	}
}

func TestPreview_TooManyRequests(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc(basePath, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
	})

	h := NewHandler(newTestClient(t, mux))
	_, err := h.Preview(context.Background(), sampleEnvelope)
	if err == nil {
		t.Fatal("Preview() expected error for 429")
	}
	if msg := err.Error(); !strings.Contains(msg, "concurrent") {
		t.Errorf("expected concurrency message, got: %v", err)
	}
}

func TestPreview_ServerError(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc(basePath, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error":{"message":"internal error"}}`))
	})

	h := NewHandler(newTestClient(t, mux))
	if _, err := h.Preview(context.Background(), sampleEnvelope); err == nil {
		t.Fatal("Preview() expected error for 500")
	}
}
