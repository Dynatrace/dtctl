package hub

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Dynatrace/dtctl/pkg/client"
	"github.com/Dynatrace/dtctl/sdk/httpclient"
)

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func newTestClient(t *testing.T, url string) *client.Client {
	t.Helper()
	c, err := client.NewForTesting(url, "test-token")
	if err != nil {
		t.Fatalf("failed to create test client: %v", err)
	}
	return c
}

func writeJSON(w http.ResponseWriter, code int, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

// paginationGuard rejects page-size combined with page-key (PaginationDefault constraint)
func paginationGuard(t *testing.T, w http.ResponseWriter, r *http.Request) bool {
	t.Helper()
	if r.URL.Query().Get("page-key") != "" && r.URL.Query().Get("page-size") != "" {
		t.Error("page-size must not be sent with page-key")
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "Constraints violated."})
		return true
	}
	return false
}

// ---------------------------------------------------------------------------
// NewHandler
// ---------------------------------------------------------------------------

func TestNewHandler(t *testing.T) {
	c := newTestClient(t, "https://test.example.invalid")
	h := NewHandler(c)
	if h == nil || h.sdk == nil {
		t.Fatal("NewHandler() returned nil or has nil sdk")
	}
}

// ---------------------------------------------------------------------------
// ListExtensions
// ---------------------------------------------------------------------------

func TestListExtensions(t *testing.T) {
	tests := []struct {
		name      string
		chunkSize int64
		pages     []HubExtensionList
		validate  func(*testing.T, *HubExtensionList)
	}{
		{
			name:      "single page no chunking",
			chunkSize: 0,
			pages: []HubExtensionList{
				{
					TotalCount: 2,
					Items: []HubExtension{
						{ID: "ext-001", Name: "Extension One", Type: "EXTENSION_2"},
						{ID: "ext-002", Name: "Extension Two", Type: "EXTENSION_2"},
					},
				},
			},
			validate: func(t *testing.T, r *HubExtensionList) {
				if len(r.Items) != 2 {
					t.Errorf("expected 2 items, got %d", len(r.Items))
				}
			},
		},
		{
			name:      "paginated across two pages",
			chunkSize: 50,
			pages: []HubExtensionList{
				{
					TotalCount:  3,
					NextPageKey: "page2key",
					Items: []HubExtension{
						{ID: "ext-001", Name: "Extension One"},
						{ID: "ext-002", Name: "Extension Two"},
					},
				},
				{
					TotalCount: 3,
					Items: []HubExtension{
						{ID: "ext-003", Name: "Extension Three"},
					},
				},
			},
			validate: func(t *testing.T, r *HubExtensionList) {
				if len(r.Items) != 3 {
					t.Errorf("expected 3 items across pages, got %d", len(r.Items))
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pageIdx := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/platform/hub/v1/catalog/extensions" {
					t.Errorf("unexpected path: %s", r.URL.Path)
					w.WriteHeader(http.StatusNotFound)
					return
				}
				if paginationGuard(t, w, r) {
					return
				}
				if pageIdx >= len(tt.pages) {
					t.Error("more requests than expected pages")
					w.WriteHeader(http.StatusInternalServerError)
					return
				}
				writeJSON(w, http.StatusOK, tt.pages[pageIdx])
				pageIdx++
			}))
			defer server.Close()

			h := NewHandler(newTestClient(t, server.URL))
			result, err := h.ListExtensions("", tt.chunkSize)
			if err != nil {
				t.Fatalf("ListExtensions() unexpected error: %v", err)
			}
			tt.validate(t, result)
		})
	}
}

func TestListExtensions_ClientSideFilter(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if paginationGuard(t, w, r) {
			return
		}
		writeJSON(w, http.StatusOK, HubExtensionList{
			TotalCount: 3,
			Items: []HubExtension{
				{ID: "com.dynatrace.extension.kafka", Name: "Apache Kafka", Type: "EXTENSION_2", Description: "Kafka monitoring"},
				{ID: "com.dynatrace.extension.jmx", Name: "JMX Extension", Type: "EXTENSION_2", Description: "JMX monitoring"},
				{ID: "com.dynatrace.extension.redis", Name: "Redis", Type: "EXTENSION_2", Description: "Redis cache monitoring"},
			},
		})
	}))
	defer server.Close()

	h := NewHandler(newTestClient(t, server.URL))

	// matches by name (case-insensitive)
	r, err := h.ListExtensions("kafka", 0)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(r.Items) != 1 || r.Items[0].ID != "com.dynatrace.extension.kafka" {
		t.Errorf("expected 1 kafka result, got %d items", len(r.Items))
	}

	// matches by description substring
	r, err = h.ListExtensions("cache", 0)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(r.Items) != 1 || r.Items[0].ID != "com.dynatrace.extension.redis" {
		t.Errorf("expected 1 redis result by description, got %d items", len(r.Items))
	}

	// no matches
	r, err = h.ListExtensions("zzznomatch", 0)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(r.Items) != 0 {
		t.Errorf("expected 0 results, got %d", len(r.Items))
	}

	// empty filter returns all
	r, err = h.ListExtensions("", 0)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(r.Items) != 3 {
		t.Errorf("expected 3 results with no filter, got %d", len(r.Items))
	}
}

// ---------------------------------------------------------------------------
// GetExtension
// ---------------------------------------------------------------------------

func TestGetExtension(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/platform/hub/v1/catalog/extensions/ext-001" {
			t.Errorf("unexpected path: %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
			return
		}
		writeJSON(w, http.StatusOK, HubExtension{
			ID:   "ext-001",
			Name: "Extension One",
			Type: "EXTENSION_2",
		})
	}))
	defer server.Close()

	h := NewHandler(newTestClient(t, server.URL))
	result, err := h.GetExtension("ext-001")
	if err != nil {
		t.Fatalf("GetExtension() unexpected error: %v", err)
	}
	if result.ID != "ext-001" {
		t.Errorf("expected ID ext-001, got %q", result.ID)
	}
	if result.Type != "EXTENSION_2" {
		t.Errorf("expected type EXTENSION_2, got %q", result.Type)
	}
}

func TestGetExtension_NotFound(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "not found"})
	}))
	defer server.Close()

	h := NewHandler(newTestClient(t, server.URL))
	_, err := h.GetExtension("missing")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

// ---------------------------------------------------------------------------
// ListExtensionReleases
// ---------------------------------------------------------------------------

func TestListExtensionReleases(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/platform/hub/v1/catalog/extensions/ext-001/releases" {
			t.Errorf("unexpected path: %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if paginationGuard(t, w, r) {
			return
		}
		// The releases endpoint uses "releases" as the JSON key for the items array.
		writeJSON(w, http.StatusOK, struct {
			Releases   []HubExtensionRelease `json:"releases"`
			TotalCount int                   `json:"totalCount"`
		}{
			TotalCount: 2,
			Releases: []HubExtensionRelease{
				{Version: "1.0.1", ReleaseDate: "2024-12-01"},
				{Version: "1.0.0", ReleaseDate: "2024-10-10"},
			},
		})
	}))
	defer server.Close()

	h := NewHandler(newTestClient(t, server.URL))
	result, err := h.ListExtensionReleases("ext-001", 0)
	if err != nil {
		t.Fatalf("ListExtensionReleases() unexpected error: %v", err)
	}
	if len(result.Items) != 2 {
		t.Errorf("expected 2 releases, got %d", len(result.Items))
	}
}

// ---------------------------------------------------------------------------
// URL escaping
// ---------------------------------------------------------------------------

func TestGetExtension_URLEscaping(t *testing.T) {
	// Extension IDs like "com.dynatrace.extension.host-monitoring" contain dots
	// which should be safely escaped in the URL path.
	extID := "com.dynatrace.extension.host-monitoring"

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		expectedPath := "/platform/hub/v1/catalog/extensions/" + httpclient.PathSegment(extID)
		if r.URL.RawPath != "" {
			// When the path contains encoded characters, RawPath is set
			if r.URL.RawPath != expectedPath {
				t.Errorf("expected raw path %q, got %q", expectedPath, r.URL.RawPath)
				w.WriteHeader(http.StatusNotFound)
				return
			}
		}
		writeJSON(w, http.StatusOK, HubExtension{
			ID:   extID,
			Name: "Host Monitoring",
			Type: "EXTENSION_2",
		})
	}))
	defer server.Close()

	h := NewHandler(newTestClient(t, server.URL))
	result, err := h.GetExtension(extID)
	if err != nil {
		t.Fatalf("GetExtension() unexpected error: %v", err)
	}
	if result.ID != extID {
		t.Errorf("expected ID %q, got %q", extID, result.ID)
	}
}

func TestListExtensionReleases_URLEscaping(t *testing.T) {
	extID := "com.dynatrace.extension.host-monitoring"

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		expectedPath := "/platform/hub/v1/catalog/extensions/" + httpclient.PathSegment(extID) + "/releases"
		if r.URL.RawPath != "" {
			if r.URL.RawPath != expectedPath {
				t.Errorf("expected raw path %q, got %q", expectedPath, r.URL.RawPath)
				w.WriteHeader(http.StatusNotFound)
				return
			}
		}
		if paginationGuard(t, w, r) {
			return
		}
		// The releases endpoint uses "releases" as the JSON key for the items array.
		writeJSON(w, http.StatusOK, struct {
			Releases   []HubExtensionRelease `json:"releases"`
			TotalCount int                   `json:"totalCount"`
		}{
			TotalCount: 1,
			Releases: []HubExtensionRelease{
				{Version: "1.0.0", ReleaseDate: "2024-12-01"},
			},
		})
	}))
	defer server.Close()

	h := NewHandler(newTestClient(t, server.URL))
	result, err := h.ListExtensionReleases(extID, 0)
	if err != nil {
		t.Fatalf("ListExtensionReleases() unexpected error: %v", err)
	}
	if len(result.Items) != 1 {
		t.Errorf("expected 1 release, got %d", len(result.Items))
	}
}
