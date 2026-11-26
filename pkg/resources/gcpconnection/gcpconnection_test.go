package gcpconnection

import (
	"encoding/json"
	"errors"
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

func TestHandlerGetListFindDelete(t *testing.T) {
	h, server := newHandler(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.Method {
		case http.MethodGet:
			if r.URL.Query().Get("schemaIds") != "" {
				_ = json.NewEncoder(w).Encode(ListResponse{Items: []GCPConnection{{ObjectID: "obj-1", Value: Value{Name: "conn-a", Type: "serviceAccountImpersonation", ServiceAccountImpersonation: &ServiceAccountImpersonation{ServiceAccountID: "sa@test"}}}}})
				return
			}
			_ = json.NewEncoder(w).Encode(GCPConnection{ObjectID: "obj-1", SchemaVersion: "7", Value: Value{Name: "conn-a", Type: "serviceAccountImpersonation", ServiceAccountImpersonation: &ServiceAccountImpersonation{ServiceAccountID: "sa@test"}}})
		case http.MethodDelete:
			w.WriteHeader(http.StatusNoContent)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	})
	defer server.Close()

	item, err := h.Get("obj-1")
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	if item.Name != "conn-a" || item.ServiceAccountID != "sa@test" {
		t.Fatalf("flattened fields not set: %+v", item)
	}

	items, err := h.List()
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if len(items) != 1 || items[0].Name != "conn-a" {
		t.Fatalf("List() result unexpected: %+v", items)
	}

	found, err := h.FindByName("conn-a")
	if err != nil {
		t.Fatalf("FindByName() error = %v", err)
	}
	if found.ObjectID != "obj-1" {
		t.Fatalf("FindByName() ObjectID = %q, want obj-1", found.ObjectID)
	}

	if err := h.Delete("obj-1"); err != nil {
		t.Fatalf("Delete() error = %v", err)
	}
}

func TestEnsureDynatracePrincipal(t *testing.T) {
	t.Run("get principal not found sentinel", func(t *testing.T) {
		h, server := newHandler(t, func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			if r.Method == http.MethodGet && r.URL.Query().Get("schemaIds") == PrincipalSchemaID {
				_ = json.NewEncoder(w).Encode(ListResponse{Items: []GCPConnection{}})
				return
			}
			w.WriteHeader(http.StatusNotFound)
		})
		defer server.Close()

		_, err := h.GetDynatracePrincipal()
		if err == nil {
			t.Fatalf("GetDynatracePrincipal() expected error, got nil")
		}
		if !errors.Is(err, ErrPrincipalNotFound) {
			t.Fatalf("GetDynatracePrincipal() expected ErrPrincipalNotFound, got %v", err)
		}
	})

	t.Run("already exists", func(t *testing.T) {
		h, server := newHandler(t, func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			if r.URL.Query().Get("schemaIds") == PrincipalSchemaID {
				_ = json.NewEncoder(w).Encode(ListResponse{Items: []GCPConnection{{
					ObjectID: "principal-1",
					Value:    Value{Principal: "dt-has@test"},
				}}})
				return
			}
			w.WriteHeader(http.StatusNotFound)
		})
		defer server.Close()

		if err := h.EnsureDynatracePrincipal(); err != nil {
			t.Fatalf("EnsureDynatracePrincipal() error = %v", err)
		}
	})

	t.Run("create when missing", func(t *testing.T) {
		postSeen := false
		principalCreated := false
		h, server := newHandler(t, func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			if r.Method == http.MethodGet && r.URL.Query().Get("schemaIds") == PrincipalSchemaID {
				if !principalCreated {
					_ = json.NewEncoder(w).Encode(ListResponse{Items: []GCPConnection{}})
					return
				}
				_ = json.NewEncoder(w).Encode(ListResponse{Items: []GCPConnection{{
					ObjectID: "created",
					Value: Value{
						Principal: "dt-has@test",
					},
				}}})
				return
			}
			if r.Method == http.MethodPost {
				if r.URL.Query().Get("schemaIds") != PrincipalSchemaID {
					t.Fatalf("expected schemaIds query param %q, got %q", PrincipalSchemaID, r.URL.Query().Get("schemaIds"))
				}

				var payload []map[string]any
				if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
					t.Fatalf("failed to decode payload: %v", err)
				}
				if len(payload) != 1 {
					t.Fatalf("unexpected payload size: %d", len(payload))
				}
				value, ok := payload[0]["value"].(map[string]any)
				if !ok {
					t.Fatalf("expected map value payload, got %#v", payload[0]["value"])
				}
				if len(value) != 0 {
					t.Fatalf("expected empty value payload for principal bootstrap, got %#v", value)
				}

				postSeen = true
				principalCreated = true
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte(`[{"objectId":"created"}]`))
				return
			}
			w.WriteHeader(http.StatusNotFound)
		})
		defer server.Close()

		principal, err := h.EnsureDynatracePrincipalWithResult()
		if err != nil {
			t.Fatalf("EnsureDynatracePrincipalWithResult() error = %v", err)
		}
		if principal.Principal != "dt-has@test" {
			t.Fatalf("unexpected principal value: %#v", principal.Principal)
		}
		if !postSeen {
			t.Fatalf("expected principal creation POST")
		}
	})
}

func TestHandlerCreateUpdateStatusMapping(t *testing.T) {
	t.Run("create status mapping", func(t *testing.T) {
		tests := []struct {
			status  int
			wantErr string
		}{
			{status: 400, wantErr: "invalid gcp_connection"},
			// 403 now surfaces the server's reason instead of swallowing it (#476).
			{status: 403, wantErr: "create gcp_connection (HTTP 403): boom"},
			{status: 404, wantErr: fmt.Sprintf("schema %q not found", SchemaID)},
			{status: 409, wantErr: "already exists"},
			{status: 500, wantErr: "failed to create gcp_connection: status 500"},
		}
		for _, tc := range tests {
			h, server := newHandler(t, func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				if r.Method == http.MethodPost {
					w.WriteHeader(tc.status)
					_, _ = w.Write([]byte("boom"))
					return
				}
				w.WriteHeader(http.StatusNotFound)
			})
			_, err := h.Create(GCPConnectionCreate{Value: Value{Name: "x", Type: "serviceAccountImpersonation"}})
			server.Close()
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("Create() err = %v, want contains %q", err, tc.wantErr)
			}
		}
	})

	t.Run("update status mapping", func(t *testing.T) {
		tests := []struct {
			status  int
			wantErr string
		}{
			{status: 400, wantErr: "invalid gcp_connection"},
			{status: 403, wantErr: "update gcp_connection \"obj-1\" (HTTP 403): boom"},
			{status: 404, wantErr: "gcp_connection \"obj-1\" not found"},
			{status: 409, wantErr: "version conflict"},
			{status: 412, wantErr: "version conflict"},
			{status: 500, wantErr: "failed to update gcp_connection: status 500"},
		}
		for _, tc := range tests {
			h, server := newHandler(t, func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				switch r.Method {
				case http.MethodGet:
					_ = json.NewEncoder(w).Encode(GCPConnection{ObjectID: "obj-1", SchemaVersion: "1", Value: Value{Name: "x", Type: "serviceAccountImpersonation"}})
				case http.MethodPut:
					w.WriteHeader(tc.status)
					_, _ = w.Write([]byte("boom"))
				default:
					w.WriteHeader(http.StatusNotFound)
				}
			})
			_, err := h.Update("obj-1", Value{Name: "x", Type: "serviceAccountImpersonation"})
			server.Close()
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("Update() err = %v, want contains %q", err, tc.wantErr)
			}
		}
	})
}

// TestListPaginationStitchesPages verifies the Settings 2.0 pagination loop.
func TestListPaginationStitchesPages(t *testing.T) {
	pages := []ListResponse{
		{Items: []GCPConnection{{ObjectID: "a", Value: Value{Name: "a", Type: "serviceAccountImpersonation"}}}, NextPageKey: "k2"},
		{Items: []GCPConnection{{ObjectID: "b", Value: Value{Name: "b", Type: "serviceAccountImpersonation"}}}, NextPageKey: "k3"},
		{Items: []GCPConnection{{ObjectID: "c", Value: Value{Name: "c", Type: "serviceAccountImpersonation"}}}},
	}
	calls := 0
	h, server := newHandler(t, func(w http.ResponseWriter, r *http.Request) {
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
		t.Fatalf("expected 3 items, got %d", len(items))
	}
}
