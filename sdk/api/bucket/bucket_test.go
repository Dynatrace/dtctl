package bucket

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
	mux.HandleFunc("/platform/storage/management/v1/bucket-definitions", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		resp := BucketList{
			Buckets: []Bucket{
				{BucketName: "default_logs", Table: "logs", DisplayName: "Default Logs", Status: "active", RetentionDays: 35, Version: 1},
				{BucketName: "default_metrics", Table: "metrics", DisplayName: "Default Metrics", Status: "active", RetentionDays: 462, Version: 2},
			},
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	})

	h := NewHandler(newTestClient(t, mux))
	result, err := h.List(context.Background())
	if err != nil {
		t.Fatalf("List() error: %v", err)
	}
	if len(result.Buckets) != 2 {
		t.Errorf("got %d buckets, want 2", len(result.Buckets))
	}
	if result.Buckets[0].BucketName != "default_logs" {
		t.Errorf("BucketName = %q, want %q", result.Buckets[0].BucketName, "default_logs")
	}
}

func TestGet(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/platform/storage/management/v1/bucket-definitions/default_logs", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		resp := Bucket{BucketName: "default_logs", Table: "logs", DisplayName: "Default Logs", Status: "active", RetentionDays: 35, Version: 1}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	})

	h := NewHandler(newTestClient(t, mux))
	result, err := h.Get(context.Background(), "default_logs")
	if err != nil {
		t.Fatalf("Get() error: %v", err)
	}
	if result.BucketName != "default_logs" {
		t.Errorf("BucketName = %q, want %q", result.BucketName, "default_logs")
	}
	if result.RetentionDays != 35 {
		t.Errorf("RetentionDays = %d, want 35", result.RetentionDays)
	}
}

func TestGet_NotFound(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/platform/storage/management/v1/bucket-definitions/missing", func(w http.ResponseWriter, r *http.Request) {
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
	mux.HandleFunc("/platform/storage/management/v1/bucket-definitions", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		var req BucketCreate
		json.NewDecoder(r.Body).Decode(&req)
		resp := Bucket{BucketName: req.BucketName, Table: req.Table, DisplayName: req.DisplayName, Status: "creating", RetentionDays: req.RetentionDays, Version: 1}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		json.NewEncoder(w).Encode(resp)
	})

	h := NewHandler(newTestClient(t, mux))
	result, err := h.Create(context.Background(), BucketCreate{BucketName: "my_bucket", Table: "logs", DisplayName: "My Bucket", RetentionDays: 30})
	if err != nil {
		t.Fatalf("Create() error: %v", err)
	}
	if result.BucketName != "my_bucket" {
		t.Errorf("BucketName = %q, want %q", result.BucketName, "my_bucket")
	}
	if result.Status != "creating" {
		t.Errorf("Status = %q, want %q", result.Status, "creating")
	}
}

func TestDelete(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/platform/storage/management/v1/bucket-definitions/my_bucket", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodDelete {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})

	h := NewHandler(newTestClient(t, mux))
	err := h.Delete(context.Background(), "my_bucket")
	if err != nil {
		t.Fatalf("Delete() error: %v", err)
	}
}

func TestDelete_ServerError(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/platform/storage/management/v1/bucket-definitions/my_bucket", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		fmt.Fprintf(w, `{"error":{"message":"internal error"}}`)
	})

	h := NewHandler(newTestClient(t, mux))
	err := h.Delete(context.Background(), "my_bucket")
	if err == nil {
		t.Fatal("Delete() expected error for 500")
	}
}
