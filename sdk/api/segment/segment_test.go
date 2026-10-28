package segment

import (
	"context"
	"encoding/json"
	"errors"
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
	mux.HandleFunc("/platform/storage/filter-segments/v1/filter-segments", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		resp := FilterSegmentList{
			FilterSegments: []FilterSegment{
				{UID: "uid-1", Name: "Segment A", IsPublic: true, Version: 1},
				{UID: "uid-2", Name: "Segment B", IsPublic: false, Version: 2},
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
	if len(result.FilterSegments) != 2 {
		t.Errorf("got %d segments, want 2", len(result.FilterSegments))
	}
	if result.TotalCount != 2 {
		t.Errorf("TotalCount = %d, want 2", result.TotalCount)
	}
}

func TestGet(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/platform/storage/filter-segments/v1/filter-segments/uid-1", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		resp := FilterSegment{UID: "uid-1", Name: "Segment A", IsPublic: true, Version: 1}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	})

	h := NewHandler(newTestClient(t, mux))
	result, err := h.Get(context.Background(), "uid-1")
	if err != nil {
		t.Fatalf("Get() error: %v", err)
	}
	if result.UID != "uid-1" {
		t.Errorf("UID = %q, want %q", result.UID, "uid-1")
	}
	if result.Name != "Segment A" {
		t.Errorf("Name = %q, want %q", result.Name, "Segment A")
	}
}

func TestGet_NotFound(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/platform/storage/filter-segments/v1/filter-segments/missing", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		fmt.Fprintf(w, `{"error":{"message":"not found"}}`)
	})

	h := NewHandler(newTestClient(t, mux))
	_, err := h.Get(context.Background(), "missing")
	if err == nil {
		t.Fatal("Get() expected error for 404")
	}
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("expected ErrNotFound, got: %v", err)
	}
}

func TestCreate(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/platform/storage/filter-segments/v1/filter-segments", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		resp := FilterSegment{UID: "uid-new", Name: "New Segment", IsPublic: true, Version: 1}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		json.NewEncoder(w).Encode(resp)
	})

	h := NewHandler(newTestClient(t, mux))
	data := []byte(`{"name":"New Segment","isPublic":true}`)
	result, err := h.Create(context.Background(), data)
	if err != nil {
		t.Fatalf("Create() error: %v", err)
	}
	if result.UID != "uid-new" {
		t.Errorf("UID = %q, want %q", result.UID, "uid-new")
	}
}

func TestDelete(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/platform/storage/filter-segments/v1/filter-segments/uid-1", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodDelete {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})

	h := NewHandler(newTestClient(t, mux))
	err := h.Delete(context.Background(), "uid-1")
	if err != nil {
		t.Fatalf("Delete() error: %v", err)
	}
}

func TestCreate_ServerError(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/platform/storage/filter-segments/v1/filter-segments", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		fmt.Fprintf(w, `{"error":{"message":"internal error"}}`)
	})

	h := NewHandler(newTestClient(t, mux))
	_, err := h.Create(context.Background(), []byte(`{}`))
	if err == nil {
		t.Fatal("Create() expected error for 500")
	}
}
