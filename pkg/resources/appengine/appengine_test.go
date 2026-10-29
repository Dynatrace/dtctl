package appengine

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Dynatrace/dtctl/pkg/client"
)

func TestHandler_ListApps(t *testing.T) {
	tests := []struct {
		name       string
		statusCode int
		response   AppList
		wantErr    bool
		wantCount  int
	}{
		{
			name:       "list apps successfully",
			statusCode: http.StatusOK,
			response: AppList{
				Apps: []App{
					{ID: "dynatrace.automations", Name: "Automations", Version: "1.0.0"},
					{ID: "dynatrace.slack", Name: "Slack", Version: "2.1.0"},
				},
			},
			wantErr:   false,
			wantCount: 2,
		},
		{
			name:       "empty app list",
			statusCode: http.StatusOK,
			response:   AppList{Apps: []App{}},
			wantErr:    false,
			wantCount:  0,
		},
		{
			name:       "server error",
			statusCode: http.StatusInternalServerError,
			response:   AppList{},
			wantErr:    true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/platform/app-engine/registry/v1/apps" {
					t.Errorf("unexpected path: %s", r.URL.Path)
				}
				if r.URL.Query().Get("add-fields") != "isBuiltin,manifest,resourceStatus.subResourceTypes" {
					t.Errorf("missing add-fields query param")
				}

				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(tt.statusCode)
				_ = json.NewEncoder(w).Encode(tt.response)
			}))
			defer server.Close()

			c, err := client.NewForTesting(server.URL, "test-token")
			if err != nil {
				t.Fatalf("failed to create client: %v", err)
			}

			handler := NewHandler(c)
			result, err := handler.ListApps()

			if (err != nil) != tt.wantErr {
				t.Errorf("ListApps() error = %v, wantErr %v", err, tt.wantErr)
				return
			}

			if !tt.wantErr && len(result.Apps) != tt.wantCount {
				t.Errorf("ListApps() got %d apps, want %d", len(result.Apps), tt.wantCount)
			}
		})
	}
}

func TestHandler_GetApp(t *testing.T) {
	tests := []struct {
		name       string
		appID      string
		statusCode int
		response   App
		wantErr    bool
	}{
		{
			name:       "get app successfully",
			appID:      "dynatrace.automations",
			statusCode: http.StatusOK,
			response: App{
				ID:      "dynatrace.automations",
				Name:    "Automations",
				Version: "1.0.0",
			},
			wantErr: false,
		},
		{
			name:       "app not found",
			appID:      "nonexistent.app",
			statusCode: http.StatusNotFound,
			wantErr:    true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(tt.statusCode)
				if tt.statusCode == http.StatusOK {
					_ = json.NewEncoder(w).Encode(tt.response)
				}
			}))
			defer server.Close()

			c, err := client.NewForTesting(server.URL, "test-token")
			if err != nil {
				t.Fatalf("failed to create client: %v", err)
			}

			handler := NewHandler(c)
			result, err := handler.GetApp(tt.appID)

			if (err != nil) != tt.wantErr {
				t.Errorf("GetApp() error = %v, wantErr %v", err, tt.wantErr)
				return
			}

			if !tt.wantErr && result.ID != tt.appID {
				t.Errorf("GetApp() got ID %s, want %s", result.ID, tt.appID)
			}
		})
	}
}

func TestHandler_GetApp_ModificationInfo(t *testing.T) {
	// Use a raw JSON body (not a marshaled App) so the test actually exercises
	// the JSON tags on ModificationInfo — the registry returns createdAt/lastModifiedAt.
	const body = `{
		"id": "dynatrace.automations",
		"name": "Workflows",
		"version": "1.3253.0",
		"modificationInfo": {
			"createdBy": "system",
			"createdAt": "2023-02-23T10:55:12.553Z",
			"lastModifiedBy": "system",
			"lastModifiedAt": "2026-07-03T09:11:53.765Z"
		}
	}`

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(body))
	}))
	defer server.Close()

	c, err := client.NewForTesting(server.URL, "test-token")
	if err != nil {
		t.Fatalf("failed to create client: %v", err)
	}

	app, err := NewHandler(c).GetApp("dynatrace.automations")
	if err != nil {
		t.Fatalf("GetApp() error = %v", err)
	}

	if app.ModificationInfo == nil {
		t.Fatal("GetApp() ModificationInfo = nil, want populated")
	}
	if got, want := app.ModificationInfo.CreatedAt, "2023-02-23T10:55:12.553Z"; got != want {
		t.Errorf("CreatedAt = %q, want %q", got, want)
	}
	if got, want := app.ModificationInfo.LastModifiedAt, "2026-07-03T09:11:53.765Z"; got != want {
		t.Errorf("LastModifiedAt = %q, want %q", got, want)
	}
	if got, want := app.ModificationInfo.LastModifiedBy, "system"; got != want {
		t.Errorf("LastModifiedBy = %q, want %q", got, want)
	}
}

func TestContains(t *testing.T) {
	tests := []struct {
		name  string
		slice []string
		item  string
		want  bool
	}{
		{
			name:  "item exists",
			slice: []string{"FUNCTIONS", "SETTINGS"},
			item:  "FUNCTIONS",
			want:  true,
		},
		{
			name:  "item does not exist",
			slice: []string{"SETTINGS"},
			item:  "FUNCTIONS",
			want:  false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := contains(tt.slice, tt.item); got != tt.want {
				t.Errorf("contains() = %v, want %v", got, tt.want)
			}
		})
	}
}
