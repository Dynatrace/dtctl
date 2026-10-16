package document

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/dynatrace-oss/dtctl/pkg/client"
)

func newDocTestHandler(t *testing.T, mux *http.ServeMux) (*Handler, func()) {
	t.Helper()
	srv := httptest.NewServer(mux)
	c, err := client.NewForTesting(srv.URL, "test-token")
	if err != nil {
		t.Fatalf("failed to create client: %v", err)
	}
	return NewHandler(c), srv.Close
}

// --- NewHandler ---

func TestNewHandler(t *testing.T) {
	c, err := client.NewForTesting("https://test.example.invalid", "tok")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	h := NewHandler(c)
	if h == nil || h.sdk == nil {
		t.Fatal("NewHandler returned nil")
	}
}

// --- List ---

func TestList_Success(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/platform/document/v1/documents", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(DocumentList{
			Documents: []DocumentMetadata{
				{ID: "doc-1", Name: "My Dashboard", Type: "dashboard"},
				{ID: "doc-2", Name: "My Notebook", Type: "notebook"},
			},
			TotalCount: 2,
		})
	})
	h, cleanup := newDocTestHandler(t, mux)
	defer cleanup()

	result, err := h.List(DocumentFilters{})
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if len(result.Documents) != 2 {
		t.Errorf("expected 2 documents, got %d", len(result.Documents))
	}
}

func TestList_WithFilters(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/platform/document/v1/documents", func(w http.ResponseWriter, r *http.Request) {
		// Verify filter is passed
		filter := r.URL.Query().Get("filter")
		if filter == "" {
			t.Error("expected filter query param")
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(DocumentList{Documents: []DocumentMetadata{{ID: "doc-1", Type: "dashboard"}}, TotalCount: 1})
	})
	h, cleanup := newDocTestHandler(t, mux)
	defer cleanup()

	_, err := h.List(DocumentFilters{Type: "dashboard"})
	if err != nil {
		t.Fatalf("List() with filter error = %v", err)
	}
}

func TestList_RawFilterPassthrough(t *testing.T) {
	rawFilter := "originAppId exists and type in ('dashboard','notebook')"
	mux := http.NewServeMux()
	mux.HandleFunc("/platform/document/v1/documents", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("filter") != rawFilter {
			t.Errorf("expected filter %q sent verbatim, got %q", rawFilter, r.URL.Query().Get("filter"))
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(DocumentList{TotalCount: 0})
	})
	h, cleanup := newDocTestHandler(t, mux)
	defer cleanup()

	// Type/Name/Owner are ignored when Filter is set
	_, err := h.List(DocumentFilters{
		Filter: rawFilter,
		Type:   "dashboard",
		Name:   "ignored",
		Owner:  "alice",
	})
	if err != nil {
		t.Fatalf("List() with raw filter error = %v", err)
	}
}

func TestList_SortAddFieldsAdminAccess(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/platform/document/v1/documents", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("sort") != "name,-modificationInfo.lastModifiedTime" {
			t.Errorf("expected sort param, got %q", r.URL.Query().Get("sort"))
		}
		if r.URL.Query().Get("add-fields") != "originExtensionId,labels,shareInfo.isShared" {
			t.Errorf("expected add-fields joined comma-separated, got %q", r.URL.Query().Get("add-fields"))
		}
		if r.URL.Query().Get("admin-access") != "true" {
			t.Errorf("expected admin-access=true, got %q", r.URL.Query().Get("admin-access"))
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(DocumentList{TotalCount: 0})
	})
	h, cleanup := newDocTestHandler(t, mux)
	defer cleanup()

	_, err := h.List(DocumentFilters{
		Sort:        "name,-modificationInfo.lastModifiedTime",
		AddFields:   []string{"originExtensionId", "labels", "shareInfo.isShared"},
		AdminAccess: true,
	})
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
}

func TestList_OmitsUnsetExtraQueryParams(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/platform/document/v1/documents", func(w http.ResponseWriter, r *http.Request) {
		for _, p := range []string{"sort", "add-fields", "admin-access"} {
			if r.URL.Query().Has(p) {
				t.Errorf("expected %q not sent when unset, got %q", p, r.URL.Query().Get(p))
			}
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(DocumentList{TotalCount: 0})
	})
	h, cleanup := newDocTestHandler(t, mux)
	defer cleanup()

	if _, err := h.List(DocumentFilters{Type: "dashboard"}); err != nil {
		t.Fatalf("List() error = %v", err)
	}
}

func TestDocumentMetadata_AddFieldsRoundTrip(t *testing.T) {
	body := []byte(`{
		"id": "doc-1",
		"name": "test",
		"type": "dashboard",
		"version": 1,
		"originAppId": "cloud-monitoring",
		"originExtensionId": "ext-id",
		"labels": ["a","b"],
		"shareInfo": {"isShared": true, "isSharedWithCurrentUser": false},
		"userContext": {"lastAccessedTime": "2026-04-29T10:00:00Z"}
	}`)
	var m DocumentMetadata
	if err := json.Unmarshal(body, &m); err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}
	if m.OriginAppID != "cloud-monitoring" {
		t.Errorf("expected OriginAppID %q, got %q", "cloud-monitoring", m.OriginAppID)
	}
	if m.OriginExtensionID != "ext-id" {
		t.Errorf("expected OriginExtensionID %q, got %q", "ext-id", m.OriginExtensionID)
	}
	if len(m.Labels) != 2 || m.Labels[0] != "a" || m.Labels[1] != "b" {
		t.Errorf("expected Labels [a b], got %v", m.Labels)
	}
	if m.ShareInfo == nil || !m.ShareInfo.IsShared {
		t.Errorf("expected ShareInfo.IsShared=true, got %+v", m.ShareInfo)
	}
	if m.UserContext == nil || m.UserContext.LastAccessedTime.IsZero() {
		t.Errorf("expected UserContext.LastAccessedTime set, got %+v", m.UserContext)
	}
}

func TestList_ServerError(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/platform/document/v1/documents", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	})
	h, cleanup := newDocTestHandler(t, mux)
	defer cleanup()

	_, err := h.List(DocumentFilters{})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

func TestList_Pagination(t *testing.T) {
	pageIndex := 0
	pages := []DocumentList{
		{
			Documents: []DocumentMetadata{
				{ID: "doc-1", Name: "Dashboard 1", Type: "dashboard"},
				{ID: "doc-2", Name: "Dashboard 2", Type: "dashboard"},
			},
			TotalCount:  3,
			NextPageKey: "page2",
		},
		{
			Documents: []DocumentMetadata{
				{ID: "doc-3", Name: "Dashboard 3", Type: "dashboard"},
			},
			TotalCount: 3,
		},
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/platform/document/v1/documents", func(w http.ResponseWriter, r *http.Request) {
		// Verify filter is sent on every request (page tokens do NOT preserve it)
		expectedFilter := "type=='dashboard'"
		if r.URL.Query().Get("filter") != expectedFilter {
			t.Errorf("expected filter %q on every request, got %q", expectedFilter, r.URL.Query().Get("filter"))
		}

		// Verify page-size is sent on every request (Document API accepts it with page-key)
		if r.URL.Query().Get("page-size") == "" {
			t.Error("page-size must be sent on every request")
		}

		if pageIndex >= len(pages) {
			t.Error("received more requests than expected pages")
			w.WriteHeader(http.StatusInternalServerError)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(pages[pageIndex])
		pageIndex++
	})
	h, cleanup := newDocTestHandler(t, mux)
	defer cleanup()

	result, err := h.List(DocumentFilters{ChunkSize: 10, Type: "dashboard"})
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if len(result.Documents) != 3 {
		t.Errorf("expected 3 documents across pages, got %d", len(result.Documents))
	}
	if result.TotalCount != 3 {
		t.Errorf("expected TotalCount 3, got %d", result.TotalCount)
	}
}

// --- GetMetadata ---

func TestGetMetadata_Success(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/platform/document/v1/documents/doc-123/metadata", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(DocumentMetadata{
			ID:   "doc-123",
			Name: "My Dashboard",
			Type: "dashboard",
		})
	})
	h, cleanup := newDocTestHandler(t, mux)
	defer cleanup()

	meta, err := h.GetMetadata("doc-123")
	if err != nil {
		t.Fatalf("GetMetadata() error = %v", err)
	}
	if meta.ID != "doc-123" {
		t.Errorf("expected ID 'doc-123', got %q", meta.ID)
	}
}

func TestGetMetadata_NotFound(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/platform/document/v1/documents/missing/metadata", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	})
	h, cleanup := newDocTestHandler(t, mux)
	defer cleanup()

	_, err := h.GetMetadata("missing")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

func TestGetMetadata_Forbidden(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/platform/document/v1/documents/locked/metadata", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	})
	h, cleanup := newDocTestHandler(t, mux)
	defer cleanup()

	_, err := h.GetMetadata("locked")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

// --- Delete ---

func TestDelete_Success(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/platform/document/v1/documents/doc-del", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodDelete {
			t.Errorf("expected DELETE, got %s", r.Method)
		}
		if r.URL.Query().Get("optimistic-locking-version") == "" {
			t.Error("expected optimistic-locking-version query param")
		}
		w.WriteHeader(http.StatusNoContent)
	})
	h, cleanup := newDocTestHandler(t, mux)
	defer cleanup()

	err := h.Delete("doc-del", 3)
	if err != nil {
		t.Fatalf("Delete() error = %v", err)
	}
}

func TestDelete_NotFound(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/platform/document/v1/documents/gone", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	})
	h, cleanup := newDocTestHandler(t, mux)
	defer cleanup()

	err := h.Delete("gone", 1)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

func TestDelete_Conflict(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/platform/document/v1/documents/stale", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusConflict)
	})
	h, cleanup := newDocTestHandler(t, mux)
	defer cleanup()

	err := h.Delete("stale", 1)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

// --- Create ---

func TestCreate_MissingName(t *testing.T) {
	h, cleanup := newDocTestHandler(t, http.NewServeMux())
	defer cleanup()

	_, err := h.Create(CreateRequest{Type: "dashboard", Content: []byte("{}")})
	if err == nil {
		t.Fatal("expected error for missing name")
	}
}

func TestCreate_MissingType(t *testing.T) {
	h, cleanup := newDocTestHandler(t, http.NewServeMux())
	defer cleanup()

	_, err := h.Create(CreateRequest{Name: "My Doc", Content: []byte("{}")})
	if err == nil {
		t.Fatal("expected error for missing type")
	}
}

func TestCreate_MissingContent(t *testing.T) {
	h, cleanup := newDocTestHandler(t, http.NewServeMux())
	defer cleanup()

	_, err := h.Create(CreateRequest{Name: "My Doc", Type: "dashboard"})
	if err == nil {
		t.Fatal("expected error for missing content")
	}
}

func TestCreate_Success(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/platform/document/v1/documents", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("expected POST, got %s", r.Method)
		}
		w.Header().Set("Content-Type", "multipart/form-data; boundary=boundary")
		// Return a multipart response with metadata and content parts
		boundary := "test-boundary"
		w.Header().Set("Content-Type", fmt.Sprintf("multipart/form-data; boundary=%s", boundary))
		w.WriteHeader(http.StatusOK)
		fmt.Fprintf(w, "--%s\r\nContent-Disposition: form-data; name=\"metadata\"\r\nContent-Type: application/json\r\n\r\n{\"id\":\"new-doc-1\",\"name\":\"My Dashboard\",\"type\":\"dashboard\",\"version\":1}\r\n--%s--\r\n", boundary, boundary)
	})
	h, cleanup := newDocTestHandler(t, mux)
	defer cleanup()

	doc, err := h.Create(CreateRequest{
		Name:    "My Dashboard",
		Type:    "dashboard",
		Content: []byte(`{"tiles":[]}`),
	})
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	if doc == nil {
		t.Fatal("expected document, got nil")
	}
}

func TestCreate_ServerError(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/platform/document/v1/documents", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	})
	h, cleanup := newDocTestHandler(t, mux)
	defer cleanup()

	_, err := h.Create(CreateRequest{Name: "Doc", Type: "dashboard", Content: []byte("{}")})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

// --- EnvironmentShare ---

func TestCreateEnvironmentShare(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/platform/document/v1/environment-shares", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("expected POST, got %s", r.Method)
		}
		var body CreateEnvironmentShareRequest
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode body: %v", err)
		}
		if body.DocumentID != "doc-1" || body.Access != "read" {
			t.Errorf("unexpected body: %+v", body)
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(EnvironmentShare{ID: "share-1", DocumentID: "doc-1", Access: []string{"read"}})
	})
	h, cleanup := newDocTestHandler(t, mux)
	defer cleanup()

	got, err := h.CreateEnvironmentShare(CreateEnvironmentShareRequest{DocumentID: "doc-1", Access: "read"})
	if err != nil {
		t.Fatalf("CreateEnvironmentShare: %v", err)
	}
	if got.ID != "share-1" || len(got.Access) != 1 || got.Access[0] != "read" {
		t.Errorf("unexpected result: %+v", got)
	}
}

func TestListEnvironmentShares_FiltersByDocumentID(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/platform/document/v1/environment-shares", func(w http.ResponseWriter, r *http.Request) {
		filter := r.URL.Query().Get("filter")
		if filter != "documentId=='doc-1'" {
			t.Errorf("unexpected filter: %q", filter)
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(EnvironmentShareList{
			Shares:     []EnvironmentShare{{ID: "s1", DocumentID: "doc-1", Access: []string{"read"}}},
			TotalCount: 1,
		})
	})
	h, cleanup := newDocTestHandler(t, mux)
	defer cleanup()

	got, err := h.ListEnvironmentShares("doc-1")
	if err != nil {
		t.Fatalf("ListEnvironmentShares: %v", err)
	}
	if len(got.Shares) != 1 || got.Shares[0].ID != "s1" {
		t.Errorf("unexpected result: %+v", got)
	}
}

func TestClaimEnvironmentShare(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/platform/document/v1/environment-shares/share-1/claim", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPut {
			t.Errorf("expected PUT, got %s", r.Method)
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"documentId":"doc-1","documentType":"notebook","access":["read"]}`)
	})
	h, cleanup := newDocTestHandler(t, mux)
	defer cleanup()

	got, err := h.ClaimEnvironmentShare("share-1")
	if err != nil {
		t.Fatalf("ClaimEnvironmentShare: %v", err)
	}
	if got.DocumentID != "doc-1" || got.DocumentType != "notebook" {
		t.Errorf("unexpected result: %+v", got)
	}
}

func TestParseShareRef(t *testing.T) {
	tests := []struct {
		in, id, host string
		wantErr      bool
	}{
		{in: "abc-123", id: "abc-123"},
		{in: " abc-123\n", id: "abc-123"},
		{in: "https://env.apps.example.invalid/ui/document/v0/#share=abc-123", id: "abc-123", host: "env.apps.example.invalid"},
		{in: "https://env.apps.example.invalid/ui/document/v0/#share=abc-123&x=1", id: "abc-123", host: "env.apps.example.invalid"},
		{in: "https://ENV.apps.example.invalid:443/ui/document/v0/#share=abc-123", id: "abc-123", host: "env.apps.example.invalid"},
		{in: "", wantErr: true},
		{in: "https://env.apps.example.invalid/#share=", wantErr: true},
		{in: "a/b", wantErr: true},
	}
	for _, tt := range tests {
		id, host, err := ParseShareRef(tt.in)
		if (err != nil) != tt.wantErr {
			t.Errorf("ParseShareRef(%q) err = %v, wantErr %v", tt.in, err, tt.wantErr)
			continue
		}
		if id != tt.id || host != tt.host {
			t.Errorf("ParseShareRef(%q) = %q, %q; want %q, %q", tt.in, id, host, tt.id, tt.host)
		}
	}
}

// TestSetPrivate: each direction is one bodiless POST to its action, with no
// metadata read and no locking version.
func TestSetPrivate(t *testing.T) {
	for _, tt := range []struct {
		private bool
		want    string
	}{{false, "POST /platform/document/v1/documents/doc-1:makePublic"}, {true, "POST /platform/document/v1/documents/doc-1:makePrivate"}} {
		var calls []string
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			calls = append(calls, r.Method+" "+r.URL.Path)
			w.WriteHeader(http.StatusNoContent)
		}))
		c, err := client.NewForTesting(srv.URL, "test-token")
		if err != nil {
			t.Fatalf("failed to create client: %v", err)
		}
		if err := NewHandler(c).SetPrivate("doc-1", tt.private); err != nil {
			t.Fatalf("SetPrivate(%v): %v", tt.private, err)
		}
		srv.Close()
		if len(calls) != 1 || calls[0] != tt.want {
			t.Errorf("SetPrivate(%v) calls = %q, want [%q]", tt.private, calls, tt.want)
		}
	}
}

func TestEnsureEnvironmentLink_Handles409Race(t *testing.T) {
	listCalls := 0
	mux := http.NewServeMux()
	mux.HandleFunc("/platform/document/v1/environment-shares", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodGet {
			listCalls++
			if listCalls == 1 {
				// First list: empty (simulates race — another process hasn't created yet)
				json.NewEncoder(w).Encode(EnvironmentShareList{Shares: nil, TotalCount: 0})
			} else {
				// Second list (after 409): share now exists from the other process
				json.NewEncoder(w).Encode(EnvironmentShareList{
					Shares:     []EnvironmentShare{{ID: "s-race", DocumentID: "doc-1", Access: []string{"read"}}},
					TotalCount: 1,
				})
			}
			return
		}
		if r.Method == http.MethodPost {
			// Simulate conflict from concurrent create
			w.WriteHeader(http.StatusConflict)
			fmt.Fprintf(w, `{"error":{"message":"an environment share already exists for document \"doc-1\""}}`)
		}
	})
	h, cleanup := newDocTestHandler(t, mux)
	defer cleanup()

	res, err := h.EnsureEnvironmentLink("doc-1", "read")
	if err != nil {
		t.Fatalf("EnsureEnvironmentLink should recover from 409 race: %v", err)
	}
	if got := res.Share; got.ID != "s-race" {
		t.Errorf("expected recovered share s-race, got %+v", got)
	}
	if listCalls != 2 {
		t.Errorf("expected 2 list calls (initial + re-list after 409), got %d", listCalls)
	}
}

// TestEnsureEnvironmentLink_409WithoutMatch: a 409 that the re-list does
// not explain (no share at the level) is returned, not retried, and a share at
// another level is never deleted.
func TestEnsureEnvironmentLink_409WithoutMatch(t *testing.T) {
	createCalls, deleteCalls := 0, 0
	mux := http.NewServeMux()
	mux.HandleFunc("/platform/document/v1/environment-shares", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodGet {
			json.NewEncoder(w).Encode(EnvironmentShareList{
				Shares:     []EnvironmentShare{{ID: "s-r", DocumentID: "doc-1", Access: []string{"read"}}},
				TotalCount: 1,
			})
			return
		}
		createCalls++
		w.WriteHeader(http.StatusConflict)
		fmt.Fprintf(w, `{"error":{"message":"conflict"}}`)
	})
	mux.HandleFunc("/platform/document/v1/environment-shares/", func(w http.ResponseWriter, r *http.Request) {
		deleteCalls++
		w.WriteHeader(http.StatusNoContent)
	})
	h, cleanup := newDocTestHandler(t, mux)
	defer cleanup()

	if _, err := h.EnsureEnvironmentLink("doc-1", "read-write"); err == nil {
		t.Fatal("expected the 409 to be returned")
	}
	if createCalls != 1 || deleteCalls != 0 {
		t.Errorf("creates = %d, deletes = %d; want 1 and 0", createCalls, deleteCalls)
	}
}

// envLinkServer mocks the environment-share endpoints and records every
// mutating call ("POST <access>", "DELETE <id>") in order; any call on the
// document itself is recorded too, so a test can pin that none is made. A
// DELETE of failDelete answers 500.
func envLinkServer(t *testing.T, shares []EnvironmentShare, failDelete string) (*Handler, *[]string) {
	t.Helper()
	var calls []string
	mux := http.NewServeMux()
	mux.HandleFunc("/platform/document/v1/environment-shares", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodPost {
			var body map[string]string
			_ = json.NewDecoder(r.Body).Decode(&body)
			calls = append(calls, "POST "+body["access"])
			w.WriteHeader(http.StatusCreated)
			json.NewEncoder(w).Encode(EnvironmentShare{ID: "s-new", DocumentID: "doc-1", Access: []string{"read", "write"}})
			return
		}
		json.NewEncoder(w).Encode(EnvironmentShareList{Shares: shares, TotalCount: len(shares)})
	})
	mux.HandleFunc("/platform/document/v1/environment-shares/", func(w http.ResponseWriter, r *http.Request) {
		id := strings.TrimPrefix(r.URL.Path, "/platform/document/v1/environment-shares/")
		calls = append(calls, r.Method+" "+id)
		if id == failDelete {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("/platform/document/v1/documents/", func(w http.ResponseWriter, r *http.Request) {
		calls = append(calls, r.Method+" "+r.URL.Path)
		w.WriteHeader(http.StatusNoContent)
	})
	h, cleanup := newDocTestHandler(t, mux)
	t.Cleanup(cleanup)
	return h, &calls
}

// TestEnsureEnvironmentLink: a link never touches isPrivate, a share at the
// level is reused, and a share at another level is left alone: a document has
// at most one per level, so the requested one is created next to it.
func TestEnsureEnvironmentLink(t *testing.T) {
	readWrite := []EnvironmentShare{{ID: "s-rw", DocumentID: "doc-1", Access: []string{"read", "write"}}}
	tests := []struct {
		name        string
		shares      []EnvironmentShare
		access      string
		wantCalls   string
		wantShare   string
		wantCreated bool
	}{
		{name: "creates a share when there is none", access: "read",
			wantCalls: "POST read", wantShare: "s-new", wantCreated: true},
		{name: "reuses a share at the same level", shares: readWrite, access: "read-write",
			wantShare: "s-rw"},
		{name: "creates one next to a share at another level", shares: readWrite, access: "read",
			wantCalls: "POST read", wantShare: "s-new", wantCreated: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h, calls := envLinkServer(t, tt.shares, "")

			res, err := h.EnsureEnvironmentLink("doc-1", tt.access)
			if err != nil {
				t.Fatalf("EnsureEnvironmentLink: %v", err)
			}
			if got := strings.Join(*calls, "; "); got != tt.wantCalls {
				t.Errorf("calls = [%s], want [%s]", got, tt.wantCalls)
			}
			if res.Share.ID != tt.wantShare || res.Created != tt.wantCreated {
				t.Errorf("share = %s (created %v), want %s (created %v)",
					res.Share.ID, res.Created, tt.wantShare, tt.wantCreated)
			}
		})
	}
}

// TestRemoveEnvironmentLinks: deleting links never touches isPrivate, and an
// --access filter deletes only shares at exactly that level.
func TestRemoveEnvironmentLinks(t *testing.T) {
	shares := []EnvironmentShare{
		{ID: "s-r", DocumentID: "doc-1", Access: []string{"read"}},
		{ID: "s-rw", DocumentID: "doc-1", Access: []string{"read", "write"}},
	}
	tests := []struct {
		name      string
		shares    []EnvironmentShare
		access    string
		wantCalls string
		wantN     int
	}{
		{name: "deletes every share", shares: shares, wantCalls: "DELETE s-r; DELETE s-rw", wantN: 2},
		{name: "access filter deletes only the exact match", shares: shares, access: "read-write",
			wantCalls: "DELETE s-rw", wantN: 1},
		{name: "a filter that matches nothing changes nothing", shares: shares[1:], access: "read"},
		{name: "no share, nothing to do"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h, calls := envLinkServer(t, tt.shares, "")

			n, err := h.RemoveEnvironmentLinks("doc-1", tt.access)
			if err != nil {
				t.Fatalf("RemoveEnvironmentLinks: %v", err)
			}
			if got := strings.Join(*calls, "; "); got != tt.wantCalls {
				t.Errorf("calls = [%s], want [%s]", got, tt.wantCalls)
			}
			if n != tt.wantN {
				t.Errorf("deleted = %d, want %d", n, tt.wantN)
			}
		})
	}
}

// TestRemoveEnvironmentLinks_FailurePartway: the error says how many shares
// were deleted before the failure.
func TestRemoveEnvironmentLinks_FailurePartway(t *testing.T) {
	h, _ := envLinkServer(t, []EnvironmentShare{
		{ID: "s1", DocumentID: "doc-1", Access: []string{"read"}},
		{ID: "s2", DocumentID: "doc-1", Access: []string{"read"}},
	}, "s2")

	n, err := h.RemoveEnvironmentLinks("doc-1", "")
	if err == nil {
		t.Fatal("expected an error")
	}
	for _, want := range []string{"s2", "1 of 2"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not mention %q", err, want)
		}
	}
	if n != 1 {
		t.Errorf("deleted = %d, want 1", n)
	}
}

// --- documentListItemToDocument / ConvertToDocuments ---

func TestConvertToDocuments(t *testing.T) {
	list := &DocumentList{
		Documents: []DocumentMetadata{
			{ID: "d1", Name: "Dashboard 1", Type: "dashboard", Version: 1},
			{ID: "d2", Name: "Notebook 2", Type: "notebook", Version: 2},
		},
		TotalCount: 2,
	}
	docs := ConvertToDocuments(list)
	if len(docs) != 2 {
		t.Fatalf("expected 2 docs, got %d", len(docs))
	}
	if docs[0].ID != "d1" || docs[1].ID != "d2" {
		t.Errorf("unexpected documents: %v", docs)
	}
}

// TestConvertToDocuments_PreservesAddFields verifies that fields populated
// via DocumentFilters.AddFields (originAppId, originExtensionId, labels,
// shareInfo, userContext) survive the DocumentMetadata -> Document
// conversion. Without this, --add-fields output is silently stripped before
// the printer ever sees it.
func TestConvertToDocuments_PreservesAddFields(t *testing.T) {
	share := &ShareInfo{IsShared: true, IsSharedWithCurrentUser: true}
	userCtx := &UserContext{}
	list := &DocumentList{
		Documents: []DocumentMetadata{
			{
				ID:                "d1",
				Name:              "Dashboard with extras",
				Type:              "dashboard",
				Version:           1,
				OriginAppID:       "dynatrace.app",
				OriginExtensionID: "ext-123",
				Labels:            []string{"prod", "team-a"},
				ShareInfo:         share,
				UserContext:       userCtx,
			},
		},
	}

	docs := ConvertToDocuments(list)
	if len(docs) != 1 {
		t.Fatalf("expected 1 doc, got %d", len(docs))
	}
	d := docs[0]
	if d.OriginAppID != "dynatrace.app" {
		t.Errorf("OriginAppID not preserved: got %q", d.OriginAppID)
	}
	if d.OriginExtensionID != "ext-123" {
		t.Errorf("OriginExtensionID not preserved: got %q", d.OriginExtensionID)
	}
	if len(d.Labels) != 2 || d.Labels[0] != "prod" || d.Labels[1] != "team-a" {
		t.Errorf("Labels not preserved: got %v", d.Labels)
	}
	if d.ShareInfo == nil || !d.ShareInfo.IsShared || !d.ShareInfo.IsSharedWithCurrentUser {
		t.Errorf("ShareInfo not preserved: got %+v", d.ShareInfo)
	}
	if d.UserContext != userCtx {
		t.Errorf("UserContext pointer not preserved")
	}
}

// TestDocument_JSONMarshal_OmitsEmptyAddFields ensures the new optional
// fields are absent from JSON output when not populated, so default
// `dtctl get dashboards -o json` payload shape is unchanged.
func TestDocument_JSONMarshal_OmitsEmptyAddFields(t *testing.T) {
	d := Document{ID: "d1", Name: "Plain", Type: "dashboard", Version: 1}
	b, err := json.Marshal(d)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	out := string(b)
	for _, key := range []string{"originAppId", "originExtensionId", "labels", "shareInfo", "userContext"} {
		if strings.Contains(out, key) {
			t.Errorf("expected %q absent from JSON without --add-fields, got: %s", key, out)
		}
	}
}

// TestDocument_JSONMarshal_IncludesPopulatedAddFields ensures the new
// optional fields ARE present in JSON output when populated, which is the
// observable behavior of `dtctl get dashboards --add-fields ... -o json`.
func TestDocument_JSONMarshal_IncludesPopulatedAddFields(t *testing.T) {
	d := Document{
		ID:                "d1",
		Name:              "With extras",
		Type:              "dashboard",
		Version:           1,
		OriginExtensionID: "ext-123",
		Labels:            []string{"prod"},
		ShareInfo:         &ShareInfo{IsShared: true},
	}
	b, err := json.Marshal(d)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	out := string(b)
	for _, key := range []string{`"originExtensionId":"ext-123"`, `"labels":["prod"]`, `"shareInfo":{"isShared":true}`} {
		if !strings.Contains(out, key) {
			t.Errorf("expected JSON to contain %s; got: %s", key, out)
		}
	}
}

// TestDocument_YAMLMarshal_OmitsEmptyAddFields ensures the custom MarshalYAML
// keeps default `-o yaml` payloads minimal when --add-fields is not used.
func TestDocument_YAMLMarshal_OmitsEmptyAddFields(t *testing.T) {
	d := Document{ID: "d1", Name: "Plain", Type: "dashboard", Version: 1}
	b, err := yaml.Marshal(d)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	out := string(b)
	for _, key := range []string{"originAppId", "originExtensionId", "labels", "shareInfo", "userContext"} {
		if strings.Contains(out, key) {
			t.Errorf("expected %q absent from YAML without --add-fields, got: %s", key, out)
		}
	}
}

// TestDocument_YAMLMarshal_IncludesPopulatedAddFields locks in the YAML half
// of the --add-fields fix: the custom MarshalYAML had been built around an
// explicit map and silently dropped the new optional fields, even when
// populated. Without this assertion, --add-fields would regress to broken
// YAML output while JSON kept working.
func TestDocument_YAMLMarshal_IncludesPopulatedAddFields(t *testing.T) {
	d := Document{
		ID:                "d1",
		Name:              "With extras",
		Type:              "dashboard",
		Version:           1,
		OriginAppID:       "dynatrace.app",
		OriginExtensionID: "ext-123",
		Labels:            []string{"prod", "team-a"},
		ShareInfo:         &ShareInfo{IsShared: true, IsSharedWithCurrentUser: true},
		UserContext:       &UserContext{},
	}
	b, err := yaml.Marshal(d)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	out := string(b)
	for _, key := range []string{
		"originAppId: dynatrace.app",
		"originExtensionId: ext-123",
		"labels:",
		"- prod",
		"- team-a",
		"shareInfo:",
		"isShared: true",
		"isSharedWithCurrentUser: true",
		"userContext:",
	} {
		if !strings.Contains(out, key) {
			t.Errorf("expected YAML to contain %q; got:\n%s", key, out)
		}
	}
}
