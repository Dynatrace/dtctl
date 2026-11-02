package edgeconnect

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

func TestList(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/platform/app-engine/edge-connect/v1/edge-connects", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		resp := EdgeConnectList{
			EdgeConnects: []EdgeConnect{
				{ID: "ec-1", Name: "edge-1", HostPatterns: []string{"*.example.com"}},
				{ID: "ec-2", Name: "edge-2"},
			},
			TotalCount: 2,
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	})

	h := NewHandler(newTestClient(t, mux))
	result, err := h.List(context.Background())
	if err != nil {
		t.Fatalf("List() error: %v", err)
	}
	if len(result.EdgeConnects) != 2 {
		t.Errorf("got %d edge connects, want 2", len(result.EdgeConnects))
	}
}

func TestGet(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/platform/app-engine/edge-connect/v1/edge-connects/ec-1", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		resp := EdgeConnect{ID: "ec-1", Name: "edge-1"}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	})

	h := NewHandler(newTestClient(t, mux))
	result, err := h.Get(context.Background(), "ec-1")
	if err != nil {
		t.Fatalf("Get() error: %v", err)
	}
	if result.ID != "ec-1" {
		t.Errorf("ID = %q, want %q", result.ID, "ec-1")
	}
}

func TestGet_NotFound(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/platform/app-engine/edge-connect/v1/edge-connects/ec-missing", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		fmt.Fprintf(w, `{"error":{"message":"not found"}}`)
	})

	h := NewHandler(newTestClient(t, mux))
	_, err := h.Get(context.Background(), "ec-missing")
	if err == nil {
		t.Fatal("Get() expected error for 404")
	}
}

func TestCreate(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/platform/app-engine/edge-connect/v1/edge-connects", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		resp := EdgeConnect{ID: "ec-new", Name: "new-edge"}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		json.NewEncoder(w).Encode(resp)
	})

	h := NewHandler(newTestClient(t, mux))
	result, err := h.Create(context.Background(), EdgeConnect{Name: "new-edge"})
	if err != nil {
		t.Fatalf("Create() error: %v", err)
	}
	if result.ID != "ec-new" {
		t.Errorf("ID = %q, want %q", result.ID, "ec-new")
	}
}

func TestDelete(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/platform/app-engine/edge-connect/v1/edge-connects/ec-1", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodDelete {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})

	h := NewHandler(newTestClient(t, mux))
	err := h.Delete(context.Background(), "ec-1")
	if err != nil {
		t.Fatalf("Delete() error: %v", err)
	}
}
