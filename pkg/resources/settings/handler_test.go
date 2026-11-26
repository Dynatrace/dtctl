package settings

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Dynatrace/dtctl/pkg/client"
)

// newTestHandler creates a handler backed by a test server running the given handler func.
// Returns the Handler and a cleanup function.
func newTestHandler(t *testing.T, mux *http.ServeMux) (*Handler, func()) {
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

// --- ListSchemas ---

func TestListSchemas_Success(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/platform/classic/environment-api/v2/settings/schemas", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("expected GET, got %s", r.Method)
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(SchemaList{
			Items: []Schema{
				{SchemaID: "builtin:alerting.profile", DisplayName: "Alerting Profile", Version: "1.0"},
				{SchemaID: "builtin:anomaly-detection", DisplayName: "Anomaly Detection", Version: "2.0"},
			},
			TotalCount: 2,
		})
	})
	h, cleanup := newTestHandler(t, mux)
	defer cleanup()

	result, err := h.ListSchemas()
	if err != nil {
		t.Fatalf("ListSchemas() error = %v", err)
	}
	if len(result.Items) != 2 {
		t.Errorf("expected 2 schemas, got %d", len(result.Items))
	}
	if result.Items[0].SchemaID != "builtin:alerting.profile" {
		t.Errorf("unexpected first schema ID: %q", result.Items[0].SchemaID)
	}
}

func TestListSchemas_ServerError(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/platform/classic/environment-api/v2/settings/schemas", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		fmt.Fprint(w, "server error")
	})
	h, cleanup := newTestHandler(t, mux)
	defer cleanup()

	_, err := h.ListSchemas()
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

// --- GetSchema ---

func TestGetSchema_Success(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/platform/classic/environment-api/v2/settings/schemas/builtin:alerting.profile", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"schemaId": "builtin:alerting.profile",
			"version":  "1.0",
		})
	})
	h, cleanup := newTestHandler(t, mux)
	defer cleanup()

	result, err := h.GetSchema("builtin:alerting.profile")
	if err != nil {
		t.Fatalf("GetSchema() error = %v", err)
	}
	if result["schemaId"] != "builtin:alerting.profile" {
		t.Errorf("unexpected schemaId: %v", result["schemaId"])
	}
}

func TestGetSchema_NotFound(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/platform/classic/environment-api/v2/settings/schemas/unknown", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	})
	h, cleanup := newTestHandler(t, mux)
	defer cleanup()

	_, err := h.GetSchema("unknown")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

func TestGetSchema_ServerError(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/platform/classic/environment-api/v2/settings/schemas/bad", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	})
	h, cleanup := newTestHandler(t, mux)
	defer cleanup()

	_, err := h.GetSchema("bad")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

// --- ListObjects ---

func TestListObjects_Success(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/platform/classic/environment-api/v2/settings/objects", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(SettingsObjectsList{
			Items: []SettingsObject{
				{ObjectID: "obj1", SchemaID: "builtin:alerting.profile", Scope: "environment", Summary: "Default"},
			},
			TotalCount: 1,
		})
	})
	h, cleanup := newTestHandler(t, mux)
	defer cleanup()

	result, err := h.ListObjects("builtin:alerting.profile", "", 0)
	if err != nil {
		t.Fatalf("ListObjects() error = %v", err)
	}
	if len(result.Items) != 1 {
		t.Errorf("expected 1 item, got %d", len(result.Items))
	}
}

func TestListObjects_Pagination(t *testing.T) {
	callCount := 0
	mux := http.NewServeMux()
	mux.HandleFunc("/platform/classic/environment-api/v2/settings/objects", func(w http.ResponseWriter, r *http.Request) {
		callCount++
		w.Header().Set("Content-Type", "application/json")

		// Simulate Settings API constraint: pageSize, schemaIds, scopes, and fields
		// must NOT be combined with nextPageKey (all are embedded in the page token).
		if r.URL.Query().Get("nextPageKey") != "" {
			for _, param := range []string{"pageSize", "schemaIds", "scopes", "fields"} {
				if r.URL.Query().Get(param) != "" {
					t.Errorf("%s must not be sent with nextPageKey", param)
					w.WriteHeader(http.StatusBadRequest)
					fmt.Fprintf(w, `{"error":{"code":400,"message":"Constraints violated.","constraintViolations":[{"path":"%s","message":"must not be used in combination with nextPageKey query parameter."}]}}`, param)
					return
				}
			}
			// Second page: no more pages
			json.NewEncoder(w).Encode(SettingsObjectsList{
				Items:      []SettingsObject{{ObjectID: "obj2", Summary: "Second"}},
				TotalCount: 2,
			})
		} else {
			// First page: fields must be present
			if r.URL.Query().Get("fields") == "" {
				t.Error("fields must be sent on first page request")
			}
			json.NewEncoder(w).Encode(SettingsObjectsList{
				Items:       []SettingsObject{{ObjectID: "obj1", Summary: "First"}},
				TotalCount:  2,
				NextPageKey: "page2",
			})
		}
	})
	h, cleanup := newTestHandler(t, mux)
	defer cleanup()

	result, err := h.ListObjects("", "", 10) // chunkSize>0 enables pagination
	if err != nil {
		t.Fatalf("ListObjects() error = %v", err)
	}
	if len(result.Items) != 2 {
		t.Errorf("expected 2 items across pages, got %d", len(result.Items))
	}
	if callCount != 2 {
		t.Errorf("expected 2 API calls, got %d", callCount)
	}
}

func TestListObjects_ScopePopulatedFromAPI(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/platform/classic/environment-api/v2/settings/objects", func(w http.ResponseWriter, r *http.Request) {
		// Verify fields param is present so scope is requested
		if r.URL.Query().Get("fields") == "" {
			t.Error("fields query param not sent — scope will not be included in response")
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(SettingsObjectsList{
			Items: []SettingsObject{
				{ObjectID: "obj-app", SchemaID: "builtin:rum.web.name", Scope: "APPLICATION-00A21C71EC30483F", Summary: "My App"},
				{ObjectID: "obj-svc", SchemaID: "builtin:anomaly-detection.services", Scope: "SERVICE-000118600941601B", Summary: "My Service"},
				{ObjectID: "obj-env", SchemaID: "builtin:alerting.profile", Scope: "environment", Summary: "Default"},
			},
			TotalCount: 3,
		})
	})
	h, cleanup := newTestHandler(t, mux)
	defer cleanup()

	result, err := h.ListObjects("", "", 0)
	if err != nil {
		t.Fatalf("ListObjects() error = %v", err)
	}

	cases := []struct {
		idx           int
		wantScopeType string
		wantScopeID   string
	}{
		{0, "APPLICATION", "00A21C71EC30483F"},
		{1, "SERVICE", "000118600941601B"},
		{2, "environment", ""},
	}
	for _, tc := range cases {
		item := result.Items[tc.idx]
		if item.ScopeType != tc.wantScopeType {
			t.Errorf("item[%d] ScopeType = %q, want %q", tc.idx, item.ScopeType, tc.wantScopeType)
		}
		if item.ScopeID != tc.wantScopeID {
			t.Errorf("item[%d] ScopeID = %q, want %q", tc.idx, item.ScopeID, tc.wantScopeID)
		}
	}
}

func TestListObjects_PaginationWithFilters(t *testing.T) {
	callCount := 0
	mux := http.NewServeMux()
	mux.HandleFunc("/platform/classic/environment-api/v2/settings/objects", func(w http.ResponseWriter, r *http.Request) {
		callCount++
		w.Header().Set("Content-Type", "application/json")

		// Simulate Settings API constraint: pageSize, schemaIds, scopes, and fields
		// must NOT be combined with nextPageKey (all are embedded in the page token).
		if r.URL.Query().Get("nextPageKey") != "" {
			for _, param := range []string{"pageSize", "schemaIds", "scopes", "fields"} {
				if r.URL.Query().Get(param) != "" {
					t.Errorf("%s must not be sent with nextPageKey", param)
					w.WriteHeader(http.StatusBadRequest)
					fmt.Fprintf(w, `{"error":{"code":400,"message":"Constraints violated."}}`)
					return
				}
			}
			// Second page: no more pages
			json.NewEncoder(w).Encode(SettingsObjectsList{
				Items:      []SettingsObject{{ObjectID: "obj2", SchemaID: "builtin:alerting.profile", Scope: "environment", Summary: "Second"}},
				TotalCount: 2,
			})
		} else {
			// First page: verify filter params are sent
			if r.URL.Query().Get("schemaIds") != "builtin:alerting.profile" {
				t.Errorf("expected schemaIds on first request, got %q", r.URL.Query().Get("schemaIds"))
			}
			if r.URL.Query().Get("scopes") != "environment" {
				t.Errorf("expected scopes on first request, got %q", r.URL.Query().Get("scopes"))
			}
			json.NewEncoder(w).Encode(SettingsObjectsList{
				Items:       []SettingsObject{{ObjectID: "obj1", SchemaID: "builtin:alerting.profile", Scope: "environment", Summary: "First"}},
				TotalCount:  2,
				NextPageKey: "page2token",
			})
		}
	})
	h, cleanup := newTestHandler(t, mux)
	defer cleanup()

	result, err := h.ListObjects("builtin:alerting.profile", "environment", 10)
	if err != nil {
		t.Fatalf("ListObjects() error = %v", err)
	}
	if len(result.Items) != 2 {
		t.Errorf("expected 2 items across pages, got %d", len(result.Items))
	}
	if callCount != 2 {
		t.Errorf("expected 2 API calls, got %d", callCount)
	}
}

func TestListObjects_NotFound(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/platform/classic/environment-api/v2/settings/objects", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	})
	h, cleanup := newTestHandler(t, mux)
	defer cleanup()

	_, err := h.ListObjects("unknown-schema", "", 0)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

// --- getByObjectID (via Get) ---

func TestGet_ByObjectID_Success(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/platform/classic/environment-api/v2/settings/objects/my-object-id", func(w http.ResponseWriter, r *http.Request) {
		// Verify fields param is present so scope is included in the response
		if r.URL.Query().Get("fields") == "" {
			t.Error("fields query param not sent — scope will not be included in response")
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(SettingsObject{
			ObjectID: "my-object-id",
			SchemaID: "builtin:alerting.profile",
			Scope:    "environment",
			Summary:  "My alert profile",
		})
	})
	h, cleanup := newTestHandler(t, mux)
	defer cleanup()

	obj, err := h.Get("my-object-id")
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	if obj.ObjectID != "my-object-id" {
		t.Errorf("expected objectID 'my-object-id', got %q", obj.ObjectID)
	}
}

func TestGet_ByObjectID_NotFound(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/platform/classic/environment-api/v2/settings/objects/missing", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	})
	h, cleanup := newTestHandler(t, mux)
	defer cleanup()

	_, err := h.Get("missing")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

func TestGet_ByObjectID_Forbidden(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/platform/classic/environment-api/v2/settings/objects/locked", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	})
	h, cleanup := newTestHandler(t, mux)
	defer cleanup()

	_, err := h.Get("locked")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

// --- Create ---

func TestCreate_Success(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/platform/classic/environment-api/v2/settings/objects", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("expected POST, got %s", r.Method)
		}
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode([]SettingsObjectResponse{{ObjectID: "new-obj-id"}})
	})
	h, cleanup := newTestHandler(t, mux)
	defer cleanup()

	resp, err := h.Create(SettingsObjectCreate{
		SchemaID: "builtin:alerting.profile",
		Scope:    "environment",
		Value:    map[string]any{"name": "My Profile"},
	})
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	if resp.ObjectID != "new-obj-id" {
		t.Errorf("expected objectID 'new-obj-id', got %q", resp.ObjectID)
	}
}

func TestCreate_BadRequest(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/platform/classic/environment-api/v2/settings/objects", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		fmt.Fprint(w, "invalid schema")
	})
	h, cleanup := newTestHandler(t, mux)
	defer cleanup()

	_, err := h.Create(SettingsObjectCreate{SchemaID: "bad", Scope: "environment", Value: map[string]any{}})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

func TestCreate_Forbidden(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/platform/classic/environment-api/v2/settings/objects", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	})
	h, cleanup := newTestHandler(t, mux)
	defer cleanup()

	_, err := h.Create(SettingsObjectCreate{SchemaID: "s", Scope: "env", Value: map[string]any{}})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

func TestCreate_NotFound(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/platform/classic/environment-api/v2/settings/objects", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	})
	h, cleanup := newTestHandler(t, mux)
	defer cleanup()

	_, err := h.Create(SettingsObjectCreate{SchemaID: "s", Scope: "env", Value: map[string]any{}})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

func TestCreate_Conflict(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/platform/classic/environment-api/v2/settings/objects", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusConflict)
	})
	h, cleanup := newTestHandler(t, mux)
	defer cleanup()

	_, err := h.Create(SettingsObjectCreate{SchemaID: "s", Scope: "env", Value: map[string]any{}})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

func TestCreate_EmptyResponse(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/platform/classic/environment-api/v2/settings/objects", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, "[]")
	})
	h, cleanup := newTestHandler(t, mux)
	defer cleanup()

	_, err := h.Create(SettingsObjectCreate{SchemaID: "s", Scope: "env", Value: map[string]any{}})
	if err == nil {
		t.Fatal("expected error for empty response, got nil")
	}
}

func TestCreate_ResponseWithError(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/platform/classic/environment-api/v2/settings/objects", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode([]SettingsObjectResponse{{
			Error: &struct {
				Code    int    `json:"code"`
				Message string `json:"message"`
			}{Code: 400, Message: "validation error"},
		}})
	})
	h, cleanup := newTestHandler(t, mux)
	defer cleanup()

	_, err := h.Create(SettingsObjectCreate{SchemaID: "s", Scope: "env", Value: map[string]any{}})
	if err == nil {
		t.Fatal("expected error from error in response, got nil")
	}
}

// --- Delete ---

func TestDelete_Success(t *testing.T) {
	mux := http.NewServeMux()
	// First GET to resolve objectID
	mux.HandleFunc("/platform/classic/environment-api/v2/settings/objects/obj-to-delete", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(SettingsObject{ObjectID: "obj-to-delete", SchemaVersion: "1"})
		} else if r.Method == http.MethodDelete {
			w.WriteHeader(http.StatusNoContent)
		}
	})
	h, cleanup := newTestHandler(t, mux)
	defer cleanup()

	err := h.Delete("obj-to-delete")
	if err != nil {
		t.Fatalf("Delete() error = %v", err)
	}
}

func TestDelete_NotFound(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/platform/classic/environment-api/v2/settings/objects/missing", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	})
	h, cleanup := newTestHandler(t, mux)
	defer cleanup()

	err := h.Delete("missing")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

// --- ValidateCreate ---

func TestValidateCreate_Success(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/platform/classic/environment-api/v2/settings/objects", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("validateOnly") != "true" {
			t.Error("expected validateOnly=true query param")
		}
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, "[]")
	})
	h, cleanup := newTestHandler(t, mux)
	defer cleanup()

	err := h.ValidateCreate(SettingsObjectCreate{SchemaID: "s", Scope: "env", Value: map[string]any{}})
	if err != nil {
		t.Fatalf("ValidateCreate() error = %v", err)
	}
}

func TestValidateCreate_ValidationFailed(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/platform/classic/environment-api/v2/settings/objects", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		fmt.Fprint(w, "value missing required field")
	})
	h, cleanup := newTestHandler(t, mux)
	defer cleanup()

	err := h.ValidateCreate(SettingsObjectCreate{SchemaID: "s", Scope: "env", Value: map[string]any{}})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

// --- GetRaw ---

func TestGetRaw_Success(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/platform/classic/environment-api/v2/settings/objects/raw-id", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"objectId":"raw-id","value":{"key":"val"}}`)
	})
	h, cleanup := newTestHandler(t, mux)
	defer cleanup()

	raw, err := h.GetRaw("raw-id")
	if err != nil {
		t.Fatalf("GetRaw() error = %v", err)
	}
	if len(raw) == 0 {
		t.Error("expected non-empty raw response")
	}
}

func TestGetRaw_NotFound(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/platform/classic/environment-api/v2/settings/objects/nope", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	})
	h, cleanup := newTestHandler(t, mux)
	defer cleanup()

	_, err := h.GetRaw("nope")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}
