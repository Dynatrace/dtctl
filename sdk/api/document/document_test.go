package document

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/dynatrace-oss/dtctl/sdk/httpclient"
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
	mux.HandleFunc("/platform/document/v1/documents", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		resp := DocumentList{
			Documents: []DocumentMetadata{
				{ID: "doc-1", Name: "Dashboard 1", Type: "dashboard", Version: 1},
				{ID: "doc-2", Name: "Notebook 1", Type: "notebook", Version: 2},
			},
			TotalCount: 2,
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	})

	h := NewHandler(newTestClient(t, mux))
	result, err := h.List(context.Background(), DocumentFilters{})
	if err != nil {
		t.Fatalf("List() error: %v", err)
	}
	if len(result.Documents) != 2 {
		t.Errorf("got %d documents, want 2", len(result.Documents))
	}
	if result.TotalCount != 2 {
		t.Errorf("TotalCount = %d, want 2", result.TotalCount)
	}
}

func TestList_Paginated(t *testing.T) {
	callCount := 0
	mux := http.NewServeMux()
	mux.HandleFunc("/platform/document/v1/documents", func(w http.ResponseWriter, r *http.Request) {
		callCount++
		pageKey := r.URL.Query().Get("page-key")

		// Document API: page-size IS allowed with page-key
		var resp DocumentList
		switch pageKey {
		case "":
			// First page
			if ps := r.URL.Query().Get("page-size"); ps != "2" {
				t.Errorf("first page: page-size = %q, want %q", ps, "2")
			}
			resp = DocumentList{
				Documents: []DocumentMetadata{
					{ID: "doc-1", Name: "Doc 1", Version: 1},
					{ID: "doc-2", Name: "Doc 2", Version: 1},
				},
				TotalCount:  3,
				NextPageKey: "page2token",
			}
		case "page2token":
			// Second page — page-size should still be present (Document API style)
			if ps := r.URL.Query().Get("page-size"); ps != "2" {
				t.Errorf("second page: page-size = %q, want %q (Document API sends page-size on every request)", ps, "2")
			}
			resp = DocumentList{
				Documents: []DocumentMetadata{
					{ID: "doc-3", Name: "Doc 3", Version: 1},
				},
				TotalCount: 3,
			}
		default:
			w.WriteHeader(http.StatusBadRequest)
			fmt.Fprintf(w, `{"error":{"message":"unknown page key"}}`)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	})

	h := NewHandler(newTestClient(t, mux))
	result, err := h.List(context.Background(), DocumentFilters{ChunkSize: 2})
	if err != nil {
		t.Fatalf("List() error: %v", err)
	}
	if len(result.Documents) != 3 {
		t.Errorf("got %d documents, want 3", len(result.Documents))
	}
	if callCount != 2 {
		t.Errorf("API called %d times, want 2", callCount)
	}
}

func TestGetMetadata(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/platform/document/v1/documents/doc-123/metadata", func(w http.ResponseWriter, r *http.Request) {
		resp := DocumentMetadata{
			ID:      "doc-123",
			Name:    "My Dashboard",
			Type:    "dashboard",
			Version: 3,
			Owner:   "user-1",
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	})

	h := NewHandler(newTestClient(t, mux))
	result, err := h.GetMetadata(context.Background(), "doc-123")
	if err != nil {
		t.Fatalf("GetMetadata() error: %v", err)
	}
	if result.ID != "doc-123" {
		t.Errorf("ID = %q, want %q", result.ID, "doc-123")
	}
	if result.Name != "My Dashboard" {
		t.Errorf("Name = %q, want %q", result.Name, "My Dashboard")
	}
}

func TestDelete(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/platform/document/v1/documents/doc-123", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodDelete {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		if v := r.URL.Query().Get("optimistic-locking-version"); v != "5" {
			t.Errorf("optimistic-locking-version = %q, want %q", v, "5")
		}
		w.WriteHeader(http.StatusNoContent)
	})

	h := NewHandler(newTestClient(t, mux))
	err := h.Delete(context.Background(), "doc-123", 5)
	if err != nil {
		t.Fatalf("Delete() error: %v", err)
	}
}

func TestCreateEnvironmentShare(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/platform/document/v1/environment-shares", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		resp := EnvironmentShare{
			ID:         "share-1",
			DocumentID: "doc-123",
			Access:     []string{"read"},
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		json.NewEncoder(w).Encode(resp)
	})

	h := NewHandler(newTestClient(t, mux))
	result, err := h.CreateEnvironmentShare(context.Background(), CreateEnvironmentShareRequest{
		DocumentID: "doc-123",
		Access:     "read",
	})
	if err != nil {
		t.Fatalf("CreateEnvironmentShare() error: %v", err)
	}
	if result.ID != "share-1" {
		t.Errorf("ID = %q, want %q", result.ID, "share-1")
	}
}

func TestCreateEnvironmentShare_Conflict(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/platform/document/v1/environment-shares", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusConflict)
		fmt.Fprintf(w, `{"error":{"code":409,"message":"share already exists"}}`)
	})

	h := NewHandler(newTestClient(t, mux))
	_, err := h.CreateEnvironmentShare(context.Background(), CreateEnvironmentShareRequest{
		DocumentID: "doc-123",
		Access:     "read",
	})
	if err == nil {
		t.Fatal("CreateEnvironmentShare() expected error for 409")
	}
	if !errors.Is(err, ErrShareConflict) {
		t.Errorf("expected ErrShareConflict, got: %v", err)
	}
}

func TestListEnvironmentShares(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/platform/document/v1/environment-shares", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		resp := EnvironmentShareList{
			Shares: []EnvironmentShare{
				{ID: "share-1", DocumentID: "doc-123", Access: []string{"read"}},
			},
			TotalCount: 1,
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	})

	h := NewHandler(newTestClient(t, mux))
	result, err := h.ListEnvironmentShares(context.Background(), "doc-123")
	if err != nil {
		t.Fatalf("ListEnvironmentShares() error: %v", err)
	}
	if len(result.Shares) != 1 {
		t.Errorf("got %d shares, want 1", len(result.Shares))
	}
}

func TestListDirectShares(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/platform/document/v1/direct-shares", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		// The API returns the array under the kebab-case "direct-shares" key, and access as
		// an array, both matching environment-shares.
		_, _ = w.Write([]byte(`{"direct-shares":[{"id":"share-1","documentId":"doc-123","access":["read"]}],"totalCount":1}`))
	})

	h := NewHandler(newTestClient(t, mux))
	result, err := h.ListDirectShares(context.Background(), "doc-123")
	if err != nil {
		t.Fatalf("ListDirectShares() error: %v", err)
	}
	if len(result.Shares) != 1 {
		t.Fatalf("got %d shares, want 1", len(result.Shares))
	}
	if result.Shares[0].ID != "share-1" {
		t.Errorf("got share ID %q, want share-1", result.Shares[0].ID)
	}
	if !result.Shares[0].ExactAccess("read") {
		t.Errorf("got access %v, want exactly read", result.Shares[0].Access)
	}
}

func TestListDirectShares_Paginated(t *testing.T) {
	callCount := 0
	mux := http.NewServeMux()
	mux.HandleFunc("/platform/document/v1/direct-shares", func(w http.ResponseWriter, r *http.Request) {
		callCount++
		if f := r.URL.Query().Get("filter"); f != "documentId=='doc-123'" {
			t.Errorf("filter = %q, want the document filter on every page", f)
		}
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Query().Get("page-key") {
		case "":
			_, _ = w.Write([]byte(`{"direct-shares":[{"id":"share-1","documentId":"doc-123","access":["read"]}],` +
				`"totalCount":2,"nextPageKey":"page2token"}`))
		case "page2token":
			_, _ = w.Write([]byte(`{"direct-shares":[{"id":"share-2","documentId":"doc-123","access":["read"]}],` +
				`"totalCount":2}`))
		default:
			w.WriteHeader(http.StatusBadRequest)
			fmt.Fprintf(w, `{"error":{"message":"unknown page key"}}`)
		}
	})

	h := NewHandler(newTestClient(t, mux))
	result, err := h.ListDirectShares(context.Background(), "doc-123")
	if err != nil {
		t.Fatalf("ListDirectShares() error: %v", err)
	}
	if len(result.Shares) != 2 {
		t.Errorf("got %d shares, want 2 - later pages are dropped", len(result.Shares))
	}
	if result.TotalCount != 2 {
		t.Errorf("TotalCount = %d, want 2", result.TotalCount)
	}
	if callCount != 2 {
		t.Errorf("API called %d times, want 2", callCount)
	}
}

func TestGetEnvironmentShare(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/platform/document/v1/environment-shares/share-1", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"share-1","documentId":"doc-123","access":["read"],"claimCount":2}`))
	})

	h := NewHandler(newTestClient(t, mux))
	result, err := h.GetEnvironmentShare(context.Background(), "share-1")
	if err != nil {
		t.Fatalf("GetEnvironmentShare() error: %v", err)
	}
	if result.DocumentID != "doc-123" {
		t.Errorf("DocumentID = %q, want doc-123", result.DocumentID)
	}
	if result.ClaimCount != 2 {
		t.Errorf("ClaimCount = %d, want 2", result.ClaimCount)
	}
}

func TestGetEnvironmentShare_NotFound(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/platform/document/v1/environment-shares/missing", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		fmt.Fprintf(w, `{"error":{"code":404,"message":"not found"}}`)
	})

	h := NewHandler(newTestClient(t, mux))
	_, err := h.GetEnvironmentShare(context.Background(), "missing")
	if err == nil {
		t.Fatal("GetEnvironmentShare() expected error for 404")
	}
	if !errors.Is(err, httpclient.ErrNotFound) {
		t.Errorf("expected ErrNotFound, got: %v", err)
	}
}

func TestClaimEnvironmentShare(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/platform/document/v1/environment-shares/share-1/claim", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPut {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"documentId":"doc-123","documentType":"dashboard","access":["read","write"]}`))
	})

	h := NewHandler(newTestClient(t, mux))
	result, err := h.ClaimEnvironmentShare(context.Background(), "share-1")
	if err != nil {
		t.Fatalf("ClaimEnvironmentShare() error: %v", err)
	}
	if result.DocumentID != "doc-123" || result.DocumentType != "dashboard" {
		t.Errorf("got %+v, want doc-123/dashboard", result)
	}
	if len(result.Access) != 2 {
		t.Errorf("Access = %v, want read+write", result.Access)
	}
}

func TestClaimEnvironmentShare_EscapesShareID(t *testing.T) {
	var gotPath string
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.EscapedPath()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"documentId":"d","documentType":"notebook","access":["read"]}`))
	})

	h := NewHandler(newTestClient(t, mux))
	if _, err := h.ClaimEnvironmentShare(context.Background(), "a/b?c"); err != nil {
		t.Fatalf("ClaimEnvironmentShare() error: %v", err)
	}
	if want := "/platform/document/v1/environment-shares/a%2Fb%3Fc/claim"; gotPath != want {
		t.Errorf("path = %q, want %q", gotPath, want)
	}
}

func TestClaimEnvironmentShare_NotFound(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/platform/document/v1/environment-shares/missing/claim", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"error":{"code":404,"message":"not found"}}`))
	})

	h := NewHandler(newTestClient(t, mux))
	_, err := h.ClaimEnvironmentShare(context.Background(), "missing")
	if !errors.Is(err, httpclient.ErrNotFound) {
		t.Errorf("expected ErrNotFound, got: %v", err)
	}
}

func TestDeleteEnvironmentShare(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/platform/document/v1/environment-shares/share-1", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodDelete {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})

	h := NewHandler(newTestClient(t, mux))
	err := h.DeleteEnvironmentShare(context.Background(), "share-1")
	if err != nil {
		t.Fatalf("DeleteEnvironmentShare() error: %v", err)
	}
}

// TestDocumentVisibility pins the action each visibility setter calls: a
// bodiless POST to :makePublic / :makePrivate, with no locking version. The
// deprecated SetDocumentPublic goes through :makePublic too.
func TestDocumentVisibility(t *testing.T) {
	tests := []struct {
		name       string
		set        func(h *Handler) error
		wantAction string
	}{
		{"public", func(h *Handler) error { return h.MakeDocumentPublic(context.Background(), "doc-1") }, "makePublic"},
		{"private", func(h *Handler) error { return h.MakeDocumentPrivate(context.Background(), "doc-1") }, "makePrivate"},
		{"deprecated SetDocumentPublic", func(h *Handler) error { return h.SetDocumentPublic(context.Background(), "doc-1", 7) }, "makePublic"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var calls []string
			c := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls = append(calls, r.Method+" "+r.URL.Path+"?"+r.URL.RawQuery)
				w.WriteHeader(http.StatusNoContent)
			}))
			if err := tt.set(NewHandler(c)); err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			want := "POST /platform/document/v1/documents/doc-1:" + tt.wantAction + "?"
			if len(calls) != 1 || calls[0] != want {
				t.Errorf("calls = %q, want [%q]", calls, want)
			}
		})
	}

	t.Run("error", func(t *testing.T) {
		c := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusForbidden)
		}))
		if err := NewHandler(c).MakeDocumentPrivate(context.Background(), "doc-1"); err == nil {
			t.Error("expected an error for a 403")
		}
	})
}

// TestDirectShareSendNotification pins the one lever the API offers against
// notifying recipients: send-notification=false is sent only when asked for, on
// both calls that add recipients, and the API default is otherwise left alone.
func TestDirectShareSendNotification(t *testing.T) {
	tests := []struct {
		name     string
		suppress bool
		want     string
	}{
		{name: "default leaves the API default", suppress: false, want: ""},
		{name: "suppress sends false", suppress: true, want: "false"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := map[string]string{}
			mux := http.NewServeMux()
			mux.HandleFunc("/platform/document/v1/direct-shares", func(w http.ResponseWriter, r *http.Request) {
				got["create"] = r.URL.Query().Get("send-notification")
				var body map[string]any
				_ = json.NewDecoder(r.Body).Decode(&body)
				if _, leaked := body["SuppressNotification"]; leaked {
					t.Error("SuppressNotification leaked into the request body")
				}
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusCreated)
				_, _ = w.Write([]byte(`{"id":"share-1","documentId":"doc-123","access":["read"]}`))
			})
			mux.HandleFunc("/platform/document/v1/direct-shares/share-1/recipients/add", func(w http.ResponseWriter, r *http.Request) {
				got["add"] = r.URL.Query().Get("send-notification")
				w.WriteHeader(http.StatusNoContent)
			})

			h := NewHandler(newTestClient(t, mux))
			recipients := []SsoEntity{{ID: "group-1", Type: "group"}}
			if _, err := h.CreateDirectShare(context.Background(), CreateDirectShareRequest{
				DocumentID:           "doc-123",
				Access:               "read",
				Recipients:           recipients,
				SuppressNotification: tt.suppress,
			}); err != nil {
				t.Fatalf("CreateDirectShare() error: %v", err)
			}
			if err := h.AddDirectShareRecipientsWithOptions(context.Background(), "share-1", recipients,
				AddDirectShareRecipientsOptions{SuppressNotification: tt.suppress}); err != nil {
				t.Fatalf("AddDirectShareRecipientsWithOptions() error: %v", err)
			}

			for _, call := range []string{"create", "add"} {
				if got[call] != tt.want {
					t.Errorf("%s: send-notification = %q, want %q", call, got[call], tt.want)
				}
			}
		})
	}
}
