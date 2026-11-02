package slo

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
	mux.HandleFunc("/platform/slo/v1/slos", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		resp := SLOList{
			SLOs: []SLO{
				{ID: "slo-1", Name: "Availability", Version: "1"},
				{ID: "slo-2", Name: "Latency", Version: "1"},
			},
			TotalCount: 2,
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	})

	h := NewHandler(newTestClient(t, mux))
	result, err := h.List(context.Background(), "", 0)
	if err != nil {
		t.Fatalf("List() error: %v", err)
	}
	if len(result.SLOs) != 2 {
		t.Errorf("got %d SLOs, want 2", len(result.SLOs))
	}
	if result.TotalCount != 2 {
		t.Errorf("TotalCount = %d, want 2", result.TotalCount)
	}
}

func TestList_Paginated(t *testing.T) {
	callCount := 0
	mux := http.NewServeMux()
	mux.HandleFunc("/platform/slo/v1/slos", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		// Simulate API constraint: page-size must not be combined with page-key
		if r.URL.Query().Get("page-size") != "" && r.URL.Query().Get("page-key") != "" {
			w.WriteHeader(http.StatusBadRequest)
			w.Write([]byte(`{"error":{"code":400,"message":"Constraints violated."}}`))
			return
		}

		callCount++
		var resp SLOList
		if callCount == 1 {
			resp = SLOList{
				SLOs:        []SLO{{ID: "slo-1", Name: "Availability"}},
				TotalCount:  2,
				NextPageKey: "page-2-token",
			}
		} else {
			resp = SLOList{
				SLOs:       []SLO{{ID: "slo-2", Name: "Latency"}},
				TotalCount: 2,
			}
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	})

	h := NewHandler(newTestClient(t, mux))
	result, err := h.List(context.Background(), "", 1)
	if err != nil {
		t.Fatalf("List() error: %v", err)
	}
	if len(result.SLOs) != 2 {
		t.Errorf("got %d SLOs, want 2", len(result.SLOs))
	}
	if callCount != 2 {
		t.Errorf("expected 2 API calls, got %d", callCount)
	}
}

func TestGet(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/platform/slo/v1/slos/slo-1", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		resp := SLO{ID: "slo-1", Name: "Availability", Description: "Service availability", Version: "1"}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	})

	h := NewHandler(newTestClient(t, mux))
	result, err := h.Get(context.Background(), "slo-1")
	if err != nil {
		t.Fatalf("Get() error: %v", err)
	}
	if result.ID != "slo-1" {
		t.Errorf("ID = %q, want %q", result.ID, "slo-1")
	}
	if result.Name != "Availability" {
		t.Errorf("Name = %q, want %q", result.Name, "Availability")
	}
}

func TestGet_NotFound(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/platform/slo/v1/slos/missing", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		fmt.Fprintf(w, `{"error":{"message":"not found"}}`)
	})

	h := NewHandler(newTestClient(t, mux))
	_, err := h.Get(context.Background(), "missing")
	if err == nil {
		t.Fatal("Get() expected error for 404")
	}
}

func TestCreate(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/platform/slo/v1/slos", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		resp := SLO{ID: "slo-new", Name: "New SLO", Version: "1"}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		json.NewEncoder(w).Encode(resp)
	})

	h := NewHandler(newTestClient(t, mux))
	result, err := h.Create(context.Background(), []byte(`{"name":"New SLO"}`))
	if err != nil {
		t.Fatalf("Create() error: %v", err)
	}
	if result.ID != "slo-new" {
		t.Errorf("ID = %q, want %q", result.ID, "slo-new")
	}
}

func TestDelete(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/platform/slo/v1/slos/slo-1", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodDelete {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		if r.URL.Query().Get("optimistic-locking-version") != "3" {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})

	h := NewHandler(newTestClient(t, mux))
	err := h.Delete(context.Background(), "slo-1", "3")
	if err != nil {
		t.Fatalf("Delete() error: %v", err)
	}
}

func TestList_ServerError(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/platform/slo/v1/slos", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		fmt.Fprintf(w, `{"error":{"message":"internal error"}}`)
	})

	h := NewHandler(newTestClient(t, mux))
	_, err := h.List(context.Background(), "", 0)
	if err == nil {
		t.Fatal("List() expected error for 500")
	}
}

// TestDelete_EscapesID covers #492: characters that are special in a URL must
// stay inside the ID segment instead of cutting the path or adding query
// parameters.
func TestDelete_EscapesID(t *testing.T) {
	for _, id := range []string{"slo-1#x", "slo-1?x=1", "slo-1/other", "a b"} {
		t.Run(id, func(t *testing.T) {
			var gotPath, gotQuery string
			h := NewHandler(newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				gotPath, gotQuery = r.URL.EscapedPath(), r.URL.RawQuery
				w.WriteHeader(http.StatusNoContent)
			})))

			if err := h.Delete(context.Background(), id, "1"); err != nil {
				t.Fatalf("Delete() error: %v", err)
			}
			if want := "/platform/slo/v1/slos/" + httpclient.PathSegment(id); gotPath != want {
				t.Errorf("path = %q, want %q", gotPath, want)
			}
			if gotQuery != "optimistic-locking-version=1" {
				t.Errorf("query = %q, want only the locking version", gotQuery)
			}
		})
	}
}

func TestDelete_RejectsDotAndEmptyID(t *testing.T) {
	for _, id := range []string{"", ".", ".."} {
		t.Run(fmt.Sprintf("%q", id), func(t *testing.T) {
			called := false
			h := NewHandler(newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				called = true
				w.WriteHeader(http.StatusNoContent)
			})))

			if err := h.Delete(context.Background(), id, "1"); err == nil {
				t.Fatal("Delete() error = nil, want an invalid path error")
			}
			if called {
				t.Error("the request reached the server")
			}
		})
	}
}
