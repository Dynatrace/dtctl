package azureconnection

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Dynatrace/dtctl/pkg/client"
)

func TestValueString_MasksSecret(t *testing.T) {
	tests := []struct {
		name          string
		value         Value
		wantSubstr    string
		notWantSubstr string
	}{
		{
			name: "masks non-empty client secret",
			value: Value{
				Name: "test-connection",
				Type: "client-secret",
				ClientSecret: &ClientSecretCredential{
					ApplicationID: "app-123",
					DirectoryID:   "dir-456",
					ClientSecret:  "super-secret-value",
					Consumers:     []string{"consumer1"},
				},
			},
			wantSubstr:    "secret=[REDACTED]",
			notWantSubstr: "super-secret-value",
		},
		{
			name: "shows empty string for empty secret",
			value: Value{
				Name: "test-connection",
				Type: "client-secret",
				ClientSecret: &ClientSecretCredential{
					ApplicationID: "app-123",
					DirectoryID:   "dir-456",
					ClientSecret:  "",
					Consumers:     []string{"consumer1"},
				},
			},
			wantSubstr:    "secret=",
			notWantSubstr: "[REDACTED]",
		},
		{
			name: "federated identity credential without secret",
			value: Value{
				Name: "test-connection",
				Type: "federated-identity",
				FederatedIdentityCredential: &FederatedIdentityCredential{
					Consumers: []string{"consumer1", "consumer2"},
				},
			},
			wantSubstr:    "name=test-connection type=federated-identity",
			notWantSubstr: "secret=",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.value.String()
			if !strings.Contains(got, tt.wantSubstr) {
				t.Fatalf("Value.String() = %v, want substring %v", got, tt.wantSubstr)
			}
			if tt.notWantSubstr != "" && strings.Contains(got, tt.notWantSubstr) {
				t.Fatalf("Value.String() = %v, should not contain %v", got, tt.notWantSubstr)
			}
		})
	}
}

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
				_ = json.NewEncoder(w).Encode(ListResponse{Items: []AzureConnection{{ObjectID: "obj-1", Value: Value{Name: "conn-a", Type: "clientSecret"}}}})
				return
			}
			_ = json.NewEncoder(w).Encode(AzureConnection{ObjectID: "obj-1", SchemaVersion: "7", Value: Value{Name: "conn-a", Type: "clientSecret"}})
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
	if item.Name != "conn-a" || item.Type != "clientSecret" {
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

	notFound, err := h.FindByNameAndType("conn-a", "federatedIdentityCredential")
	if err != nil {
		t.Fatalf("FindByNameAndType() unexpected error = %v", err)
	}
	if notFound != nil {
		t.Fatalf("FindByNameAndType() expected nil, got %+v", notFound)
	}

	if err := h.Delete("obj-1"); err != nil {
		t.Fatalf("Delete() error = %v", err)
	}
}

func TestHandlerCreateSuccessAndStatuses(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		postSeen := false
		h, server := newHandler(t, func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			switch r.Method {
			case http.MethodPost:
				postSeen = true
				var body []AzureConnectionCreate
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Fatalf("decode POST body: %v", err)
				}
				if body[0].SchemaID != SchemaID || body[0].Scope != "environment" {
					t.Fatalf("defaults not applied: %+v", body[0])
				}
				_, _ = w.Write([]byte(`[{"objectId":"obj-created"}]`))
			case http.MethodGet:
				_ = json.NewEncoder(w).Encode(AzureConnection{ObjectID: "obj-created", Value: Value{Name: "created", Type: "clientSecret"}})
			default:
				w.WriteHeader(http.StatusNotFound)
			}
		})
		defer server.Close()

		created, err := h.Create(AzureConnectionCreate{Value: Value{Name: "created", Type: "clientSecret"}})
		if err != nil {
			t.Fatalf("Create() error = %v", err)
		}
		if !postSeen || created.ObjectID != "obj-created" {
			t.Fatalf("unexpected result postSeen=%v created=%+v", postSeen, created)
		}
	})

	t.Run("status mapping", func(t *testing.T) {
		tests := []struct {
			status  int
			wantErr string
		}{
			{status: 400, wantErr: "invalid azure_connection"},
			// 403 now surfaces the server's reason instead of swallowing it (#476).
			{status: 403, wantErr: "create azure_connection (HTTP 403): boom"},
			{status: 404, wantErr: fmt.Sprintf("schema %q not found", SchemaID)},
			{status: 409, wantErr: "already exists"},
			{status: 500, wantErr: "failed to create azure_connection: status 500"},
		}
		for _, tc := range tests {
			t.Run(fmt.Sprintf("status_%d", tc.status), func(t *testing.T) {
				h, server := newHandler(t, func(w http.ResponseWriter, r *http.Request) {
					w.Header().Set("Content-Type", "application/json")
					if r.Method == http.MethodPost {
						w.WriteHeader(tc.status)
						_, _ = w.Write([]byte("boom"))
						return
					}
					w.WriteHeader(http.StatusNotFound)
				})
				defer server.Close()

				_, err := h.Create(AzureConnectionCreate{Value: Value{Name: "x", Type: "clientSecret"}})
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("Create() err = %v, want contains %q", err, tc.wantErr)
				}
			})
		}
	})

	t.Run("response parse variants", func(t *testing.T) {
		tests := []struct {
			body    string
			wantErr string
		}{
			{body: "not-json", wantErr: "failed to parse create response"},
			{body: "[]", wantErr: "no items returned in create response"},
			{body: `[{"objectId":"x","error":{"code":400,"message":"bad"}}]`, wantErr: "create failed: bad"},
		}
		for _, tc := range tests {
			h, server := newHandler(t, func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				if r.Method == http.MethodPost {
					_, _ = w.Write([]byte(tc.body))
					return
				}
				w.WriteHeader(http.StatusNotFound)
			})
			_, err := h.Create(AzureConnectionCreate{Value: Value{Name: "x", Type: "clientSecret"}})
			server.Close()
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("Create() err = %v, want contains %q", err, tc.wantErr)
			}
		}
	})
}

func TestHandlerUpdateSuccessAndStatuses(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		getCalls := 0
		h, server := newHandler(t, func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			switch r.Method {
			case http.MethodGet:
				getCalls++
				if getCalls == 1 {
					_ = json.NewEncoder(w).Encode(AzureConnection{ObjectID: "obj-1", SchemaVersion: "9", Value: Value{Name: "old", Type: "clientSecret"}})
					return
				}
				_ = json.NewEncoder(w).Encode(AzureConnection{ObjectID: "obj-1", SchemaVersion: "10", Value: Value{Name: "new", Type: "clientSecret"}})
			case http.MethodPut:
				if got := r.Header.Get("If-Match"); got != "9" {
					t.Fatalf("If-Match = %q, want 9", got)
				}
				w.WriteHeader(http.StatusOK)
			default:
				w.WriteHeader(http.StatusNotFound)
			}
		})
		defer server.Close()

		updated, err := h.Update("obj-1", Value{Name: "new", Type: "clientSecret"})
		if err != nil {
			t.Fatalf("Update() error = %v", err)
		}
		if updated.Value.Name != "new" {
			t.Fatalf("Update() returned unexpected object: %+v", updated)
		}
	})

	t.Run("status mapping", func(t *testing.T) {
		tests := []struct {
			status  int
			wantErr string
		}{
			{status: 400, wantErr: "invalid azure_connection"},
			{status: 403, wantErr: "update azure_connection \"obj-1\" (HTTP 403): boom"},
			{status: 404, wantErr: "azure_connection \"obj-1\" not found"},
			{status: 409, wantErr: "version conflict"},
			{status: 412, wantErr: "version conflict"},
			{status: 500, wantErr: "failed to update azure_connection: status 500"},
		}
		for _, tc := range tests {
			h, server := newHandler(t, func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				switch r.Method {
				case http.MethodGet:
					_ = json.NewEncoder(w).Encode(AzureConnection{ObjectID: "obj-1", SchemaVersion: "1", Value: Value{Name: "x", Type: "clientSecret"}})
				case http.MethodPut:
					w.WriteHeader(tc.status)
					_, _ = w.Write([]byte("boom"))
				default:
					w.WriteHeader(http.StatusNotFound)
				}
			})
			_, err := h.Update("obj-1", Value{Name: "x", Type: "clientSecret"})
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
		{Items: []AzureConnection{{ObjectID: "a", Value: Value{Name: "a", Type: "clientSecret"}}}, NextPageKey: "k2"},
		{Items: []AzureConnection{{ObjectID: "b", Value: Value{Name: "b", Type: "clientSecret"}}}, NextPageKey: "k3"},
		{Items: []AzureConnection{{ObjectID: "c", Value: Value{Name: "c", Type: "clientSecret"}}}},
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
