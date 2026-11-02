package hub

import (
	"context"
	"encoding/json"
	"fmt"
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

func TestListExtensions(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/platform/hub/v1/catalog/extensions", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}

		// Simulate API constraint: page-size must not be combined with page-key
		if r.URL.Query().Get("page-size") != "" && r.URL.Query().Get("page-key") != "" {
			w.WriteHeader(http.StatusBadRequest)
			fmt.Fprintf(w, `{"error":{"code":400,"message":"Constraints violated."}}`)
			return
		}

		resp := HubExtensionList{
			Items: []HubExtension{
				{ID: "com.dynatrace.extension.host", Name: "Host Monitoring", Description: "Monitor hosts"},
				{ID: "com.dynatrace.extension.jmx", Name: "JMX Monitoring", Description: "Monitor JMX"},
			},
			TotalCount: 2,
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	})

	h := NewHandler(newTestClient(t, mux))
	result, err := h.ListExtensions(context.Background(), "", 0)
	if err != nil {
		t.Fatalf("ListExtensions() error: %v", err)
	}
	if len(result.Items) != 2 {
		t.Errorf("got %d extensions, want 2", len(result.Items))
	}
	if result.TotalCount != 2 {
		t.Errorf("TotalCount = %d, want 2", result.TotalCount)
	}
}

func TestGetExtension(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/platform/hub/v1/catalog/extensions/com.dynatrace.extension.host", func(w http.ResponseWriter, r *http.Request) {
		resp := HubExtension{
			ID:          "com.dynatrace.extension.host",
			Name:        "Host Monitoring",
			Description: "Monitor hosts",
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	})

	h := NewHandler(newTestClient(t, mux))
	result, err := h.GetExtension(context.Background(), "com.dynatrace.extension.host")
	if err != nil {
		t.Fatalf("GetExtension() error: %v", err)
	}
	if result.ID != "com.dynatrace.extension.host" {
		t.Errorf("ID = %q, want %q", result.ID, "com.dynatrace.extension.host")
	}
	if result.Name != "Host Monitoring" {
		t.Errorf("Name = %q, want %q", result.Name, "Host Monitoring")
	}
}

func TestGetExtension_NotFound(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/platform/hub/v1/catalog/extensions/nonexistent", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		fmt.Fprintf(w, `{"error":{"code":404,"message":"not found"}}`)
	})

	h := NewHandler(newTestClient(t, mux))
	_, err := h.GetExtension(context.Background(), "nonexistent")
	if err == nil {
		t.Fatal("GetExtension() expected error for 404")
	}
}

func TestListExtensionReleases(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/platform/hub/v1/catalog/extensions/com.dynatrace.extension.host/releases", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}

		// Simulate API constraint: page-size must not be combined with page-key
		if r.URL.Query().Get("page-size") != "" && r.URL.Query().Get("page-key") != "" {
			w.WriteHeader(http.StatusBadRequest)
			fmt.Fprintf(w, `{"error":{"code":400,"message":"Constraints violated."}}`)
			return
		}

		// The releases endpoint returns items under the "releases" key.
		resp := HubExtensionReleaseList{
			Items: []HubExtensionRelease{
				{Version: "2.0.0", ReleaseDate: "2024-01-01"},
				{Version: "1.9.0", ReleaseDate: "2023-06-01"},
			},
			TotalCount: 2,
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	})

	h := NewHandler(newTestClient(t, mux))
	result, err := h.ListExtensionReleases(context.Background(), "com.dynatrace.extension.host", 0)
	if err != nil {
		t.Fatalf("ListExtensionReleases() error: %v", err)
	}
	if len(result.Items) != 2 {
		t.Errorf("got %d releases, want 2", len(result.Items))
	}
	if result.Items[0].Version != "2.0.0" {
		t.Errorf("Items[0].Version = %q, want %q", result.Items[0].Version, "2.0.0")
	}
}
