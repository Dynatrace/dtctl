package awsconnection

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Dynatrace/dtctl/pkg/client"
)

func newHandler(t *testing.T, fn http.HandlerFunc) (*Handler, *httptest.Server) {
	t.Helper()
	server := httptest.NewServer(fn)
	c, err := client.NewForTesting(server.URL, "test-token")
	if err != nil {
		server.Close()
		t.Fatalf("client.New() error = %v", err)
	}
	c.HTTP().SetRetryCount(0)
	return NewHandler(c), server
}

// TestListPaginationStitchesPages verifies that List follows nextPageKey
// across multiple Settings 2.0 pages and that page 2+ requests obey the
// Settings API constraint (no pageSize/schemaIds/scopes alongside nextPageKey).
func TestListPaginationStitchesPages(t *testing.T) {
	pages := []ListResponse{
		{Items: []AWSConnection{{ObjectID: "a", Value: Value{Name: "a", Type: TypeRoleBased}}}, NextPageKey: "key-2"},
		{Items: []AWSConnection{{ObjectID: "b", Value: Value{Name: "b", Type: TypeRoleBased}}}, NextPageKey: "key-3"},
		{Items: []AWSConnection{{ObjectID: "c", Value: Value{Name: "c", Type: TypeRoleBased}}}},
	}
	calls := 0
	h, server := newHandler(t, func(w http.ResponseWriter, r *http.Request) {
		// AGENTS.md required constraint guard: Settings API rejects ALL of
		// pageSize/schemaIds/scopes/fields when nextPageKey is present.
		if r.URL.Query().Get("nextPageKey") != "" {
			for _, p := range []string{"pageSize", "schemaIds", "scopes", "fields"} {
				if r.URL.Query().Get(p) != "" {
					w.WriteHeader(http.StatusBadRequest)
					fmt.Fprintf(w, `{"error":{"code":400,"message":"%s must not be combined with nextPageKey"}}`, p)
					return
				}
			}
		} else {
			if got := r.URL.Query().Get("schemaIds"); got != SchemaID {
				t.Errorf("expected schemaIds=%q on first page, got %q", SchemaID, got)
			}
			// Without the projection the list returns objectId + value only,
			// and the export loses the schemaId/scope apply needs (#509).
			if got := r.URL.Query().Get("fields"); got != listFields {
				t.Errorf("expected fields=%q on first page, got %q", listFields, got)
			}
		}

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(pages[calls])
		calls++
	})
	defer server.Close()

	items, err := h.List()
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if calls != 3 {
		t.Fatalf("expected 3 page requests, got %d", calls)
	}
	if len(items) != 3 {
		t.Fatalf("expected 3 items, got %d (%+v)", len(items), items)
	}
	for i, want := range []string{"a", "b", "c"} {
		if items[i].Name != want {
			t.Errorf("item[%d].Name = %q, want %q", i, items[i].Name, want)
		}
	}
}

// TestUpdateDoesNotSendIfMatch verifies the update PUT does not send the
// (incorrect) If-Match: schemaVersion header. Settings 2.0 does not use
// HTTP ETag for optimistic locking; schemaVersion is the schema semver.
func TestUpdateDoesNotSendIfMatch(t *testing.T) {
	gotIfMatch := "sentinel"
	h, server := newHandler(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.Method {
		case http.MethodGet:
			_ = json.NewEncoder(w).Encode(AWSConnection{ObjectID: "obj-1", SchemaVersion: "1.0.27", Value: Value{Name: "n", Type: TypeRoleBased, AwsRoleBasedAuthentication: &AwsRoleBasedAuthenticationConfig{RoleArn: "", Consumers: []string{DefaultConsumer}}}})
		case http.MethodPut:
			gotIfMatch = r.Header.Get("If-Match")
			_ = json.NewEncoder(w).Encode(map[string]any{})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	})
	defer server.Close()

	_, err := h.Update("obj-1", Value{Name: "n", Type: TypeRoleBased, AwsRoleBasedAuthentication: &AwsRoleBasedAuthenticationConfig{RoleArn: "", Consumers: []string{DefaultConsumer}}})
	if err != nil {
		t.Fatalf("Update() error = %v", err)
	}
	if gotIfMatch != "" {
		t.Fatalf("If-Match header should be unset, got %q", gotIfMatch)
	}
}

// TestListPropagatesHTTPError ensures non-2xx page responses surface as errors.
func TestListPropagatesHTTPError(t *testing.T) {
	h, server := newHandler(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error":{"code":500,"message":"boom"}}`))
	})
	defer server.Close()

	_, err := h.List()
	if err == nil || !strings.Contains(err.Error(), "failed to list aws_connections") {
		t.Fatalf("List() err = %v, want failed-to-list error", err)
	}
}
