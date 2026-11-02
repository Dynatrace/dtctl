package settings

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

func TestListSchemas(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/platform/classic/environment-api/v2/settings/schemas", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		resp := SchemaList{
			Items: []Schema{
				{SchemaID: "builtin:alerting.profile", DisplayName: "Alerting profile"},
				{SchemaID: "builtin:anomaly-detection", DisplayName: "Anomaly detection"},
			},
			TotalCount: 2,
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	})

	h := NewHandler(newTestClient(t, mux))
	result, err := h.ListSchemas(context.Background())
	if err != nil {
		t.Fatalf("ListSchemas() error: %v", err)
	}
	if len(result.Items) != 2 {
		t.Errorf("got %d schemas, want 2", len(result.Items))
	}
	if result.Items[0].SchemaID != "builtin:alerting.profile" {
		t.Errorf("SchemaID = %q, want %q", result.Items[0].SchemaID, "builtin:alerting.profile")
	}
}

func TestListObjects_Paginated(t *testing.T) {
	callCount := 0
	mux := http.NewServeMux()
	mux.HandleFunc("/platform/classic/environment-api/v2/settings/objects", func(w http.ResponseWriter, r *http.Request) {
		callCount++

		// Simulate Settings API constraint: pageSize, schemaIds, scopes, and fields
		// must NOT be combined with nextPageKey (all are embedded in the page token).
		if r.URL.Query().Get("nextPageKey") != "" {
			for _, param := range []string{"pageSize", "schemaIds", "scopes", "fields"} {
				if r.URL.Query().Get(param) != "" {
					w.WriteHeader(http.StatusBadRequest)
					fmt.Fprintf(w, `{"error":{"code":400,"message":"Constraints violated."}}`)
					return
				}
			}
		}

		nextPageKey := r.URL.Query().Get("nextPageKey")
		var resp SettingsObjectsList
		switch nextPageKey {
		case "":
			// First page
			resp = SettingsObjectsList{
				Items: []SettingsObject{
					{ObjectID: "obj-1", SchemaID: "builtin:alerting.profile", Scope: "environment"},
				},
				TotalCount:  2,
				NextPageKey: "page2token",
			}
		case "page2token":
			// Second page — only nextPageKey, no other params
			resp = SettingsObjectsList{
				Items: []SettingsObject{
					{ObjectID: "obj-2", SchemaID: "builtin:alerting.profile", Scope: "environment"},
				},
				TotalCount: 2,
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
	result, err := h.ListObjects(context.Background(), "builtin:alerting.profile", "environment", 10)
	if err != nil {
		t.Fatalf("ListObjects() error: %v", err)
	}
	if len(result.Items) != 2 {
		t.Errorf("got %d items, want 2", len(result.Items))
	}
	if callCount != 2 {
		t.Errorf("API called %d times, want 2", callCount)
	}
}

func TestGet(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/platform/classic/environment-api/v2/settings/objects/obj-123", func(w http.ResponseWriter, r *http.Request) {
		resp := SettingsObject{
			ObjectID:      "obj-123",
			SchemaID:      "builtin:alerting.profile",
			SchemaVersion: "1.0.0",
			Scope:         "environment",
			Value:         map[string]any{"name": "Default"},
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	})

	h := NewHandler(newTestClient(t, mux))
	result, err := h.Get(context.Background(), "obj-123")
	if err != nil {
		t.Fatalf("Get() error: %v", err)
	}
	if result.ObjectID != "obj-123" {
		t.Errorf("ObjectID = %q, want %q", result.ObjectID, "obj-123")
	}
	if result.SchemaID != "builtin:alerting.profile" {
		t.Errorf("SchemaID = %q, want %q", result.SchemaID, "builtin:alerting.profile")
	}
}

func TestCreate(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/platform/classic/environment-api/v2/settings/objects", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		resp := []SettingsObjectResponse{
			{ObjectID: "obj-new", Code: 200},
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	})

	h := NewHandler(newTestClient(t, mux))
	result, err := h.Create(context.Background(), SettingsObjectCreate{
		SchemaID: "builtin:alerting.profile",
		Scope:    "environment",
		Value:    map[string]any{"name": "New Profile"},
	})
	if err != nil {
		t.Fatalf("Create() error: %v", err)
	}
	if result.ObjectID != "obj-new" {
		t.Errorf("ObjectID = %q, want %q", result.ObjectID, "obj-new")
	}
}

func TestDelete(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/platform/classic/environment-api/v2/settings/objects/obj-123", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodDelete {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		if ifMatch := r.Header.Get("If-Match"); ifMatch != "1.0.0" {
			t.Errorf("If-Match = %q, want %q", ifMatch, "1.0.0")
		}
		w.WriteHeader(http.StatusNoContent)
	})

	h := NewHandler(newTestClient(t, mux))
	err := h.Delete(context.Background(), "obj-123", "1.0.0")
	if err != nil {
		t.Fatalf("Delete() error: %v", err)
	}
}
