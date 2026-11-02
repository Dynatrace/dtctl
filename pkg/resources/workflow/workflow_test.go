package workflow

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Dynatrace/dtctl/pkg/client"
)

func stringPtr(v string) *string { return &v }

func intPtr(v int) *int { return &v }

// TestHandler_List_DefaultFieldProjection verifies the CLI handler applies the
// listFields projection (a display concern owned by this layer, not the SDK).
func TestHandler_List_DefaultFieldProjection(t *testing.T) {
	var gotFields string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotFields = r.URL.Query().Get("fields")
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(WorkflowList{Count: 0, Results: []Workflow{}})
	}))
	defer server.Close()

	c, err := client.NewForTesting(server.URL, "test-token")
	if err != nil {
		t.Fatalf("client.NewForTesting() error = %v", err)
	}
	c.HTTP().SetRetryCount(0)

	if _, err := NewHandler(c).List(WorkflowFilters{}, 0, 0); err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if gotFields != listFields {
		t.Errorf("fields query param = %q, want %q", gotFields, listFields)
	}
}

func TestHandler_List(t *testing.T) {
	tests := []struct {
		name       string
		filters    WorkflowFilters
		wantOwner  string
		statusCode int
		response   WorkflowList
		wantErr    bool
		wantCount  int
	}{
		{
			name:       "list all workflows",
			filters:    WorkflowFilters{},
			wantOwner:  "",
			statusCode: http.StatusOK,
			response: WorkflowList{
				Count: 2,
				Results: []Workflow{
					{ID: "wf1", Title: "Workflow 1", Owner: "user-123"},
					{ID: "wf2", Title: "Workflow 2", Owner: "user-456"},
				},
			},
			wantErr:   false,
			wantCount: 2,
		},
		{
			name:       "list with owner filter (--mine)",
			filters:    WorkflowFilters{Owner: "user-123"},
			wantOwner:  "user-123",
			statusCode: http.StatusOK,
			response: WorkflowList{
				Count: 1,
				Results: []Workflow{
					{ID: "wf1", Title: "Workflow 1", Owner: "user-123"},
				},
			},
			wantErr:   false,
			wantCount: 1,
		},
		{
			name:       "server error",
			filters:    WorkflowFilters{},
			statusCode: http.StatusInternalServerError,
			response:   WorkflowList{},
			wantErr:    true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				// Verify the request path
				if r.URL.Path != "/platform/automation/v1/workflows" {
					t.Errorf("unexpected path: %s", r.URL.Path)
				}

				// Verify the owner query parameter if expected
				gotOwner := r.URL.Query().Get("owner")
				if gotOwner != tt.wantOwner {
					t.Errorf("owner query param = %q, want %q", gotOwner, tt.wantOwner)
				}

				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(tt.statusCode)
				_ = json.NewEncoder(w).Encode(tt.response)
			}))
			defer server.Close()

			c, err := client.NewForTesting(server.URL, "test-token")
			if err != nil {
				t.Fatalf("client.New() error = %v", err)
			}
			c.HTTP().SetRetryCount(0)

			handler := NewHandler(c)
			list, err := handler.List(tt.filters, 0, 0)

			if (err != nil) != tt.wantErr {
				t.Errorf("List() error = %v, wantErr %v", err, tt.wantErr)
				return
			}

			if !tt.wantErr {
				if list == nil {
					t.Fatal("List() returned nil")
				}
				if len(list.Results) != tt.wantCount {
					t.Errorf("List() returned %d workflows, want %d", len(list.Results), tt.wantCount)
				}
			}
		})
	}
}

func TestHandler_Get(t *testing.T) {
	tests := []struct {
		name       string
		id         string
		statusCode int
		response   Workflow
		wantErr    bool
	}{
		{
			name:       "get existing workflow",
			id:         "wf-123",
			statusCode: http.StatusOK,
			response: Workflow{
				ID:                   "wf-123",
				Title:                "Test Workflow",
				IsDeployed:           true,
				Description:          "A test workflow",
				Actor:                "user-123",
				Owner:                "user-123",
				OwnerType:            "USER",
				Private:              false,
				SchemaVersion:        4,
				Trigger:              map[string]interface{}{"manual": true},
				Result:               stringPtr("{{ result('task1') }}"),
				Type:                 "STANDARD",
				Input:                map[string]interface{}{"env": "prod"},
				HourlyExecutionLimit: intPtr(1000),
				Guide:                stringPtr("# Workflow Guide\n\nUse this workflow for testing."),
			},
			wantErr: false,
		},
		{
			name:       "workflow not found",
			id:         "wf-nonexistent",
			statusCode: http.StatusNotFound,
			response:   Workflow{},
			wantErr:    true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				expectedPath := "/platform/automation/v1/workflows/" + tt.id
				if r.URL.Path != expectedPath {
					t.Errorf("unexpected path: got %s, want %s", r.URL.Path, expectedPath)
				}

				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(tt.statusCode)
				_ = json.NewEncoder(w).Encode(tt.response)
			}))
			defer server.Close()

			c, err := client.NewForTesting(server.URL, "test-token")
			if err != nil {
				t.Fatalf("client.New() error = %v", err)
			}
			c.HTTP().SetRetryCount(0)

			handler := NewHandler(c)
			wf, err := handler.Get(tt.id)

			if (err != nil) != tt.wantErr {
				t.Errorf("Get() error = %v, wantErr %v", err, tt.wantErr)
				return
			}

			if !tt.wantErr {
				if wf == nil {
					t.Fatal("Get() returned nil")
				}
				if wf.ID != tt.response.ID {
					t.Errorf("Get() ID = %v, want %v", wf.ID, tt.response.ID)
				}
				if wf.Title != tt.response.Title {
					t.Errorf("Get() Title = %v, want %v", wf.Title, tt.response.Title)
				}
				if wf.IsDeployed != tt.response.IsDeployed {
					t.Errorf("Get() IsDeployed = %v, want %v", wf.IsDeployed, tt.response.IsDeployed)
				}
				if wf.Description != tt.response.Description {
					t.Errorf("Get() Description = %v, want %v", wf.Description, tt.response.Description)
				}
				if wf.Actor != tt.response.Actor {
					t.Errorf("Get() Actor = %v, want %v", wf.Actor, tt.response.Actor)
				}
				if wf.OwnerType != tt.response.OwnerType {
					t.Errorf("Get() OwnerType = %v, want %v", wf.OwnerType, tt.response.OwnerType)
				}
				if wf.Private != tt.response.Private {
					t.Errorf("Get() Private = %v, want %v", wf.Private, tt.response.Private)
				}
				if wf.SchemaVersion != tt.response.SchemaVersion {
					t.Errorf("Get() SchemaVersion = %v, want %v", wf.SchemaVersion, tt.response.SchemaVersion)
				}
				if got := wf.Trigger["manual"]; got != tt.response.Trigger["manual"] {
					t.Errorf("Get() Trigger[manual] = %v, want %v", got, tt.response.Trigger["manual"])
				}
				if wf.Type != tt.response.Type {
					t.Errorf("Get() Type = %v, want %v", wf.Type, tt.response.Type)
				}
				if got := wf.Input["env"]; got != tt.response.Input["env"] {
					t.Errorf("Get() Input[env] = %v, want %v", got, tt.response.Input["env"])
				}
				if wf.Result == nil || tt.response.Result == nil || *wf.Result != *tt.response.Result {
					t.Errorf("Get() Result = %v, want %v", wf.Result, tt.response.Result)
				}
				if wf.HourlyExecutionLimit == nil || tt.response.HourlyExecutionLimit == nil || *wf.HourlyExecutionLimit != *tt.response.HourlyExecutionLimit {
					t.Errorf("Get() HourlyExecutionLimit = %v, want %v", wf.HourlyExecutionLimit, tt.response.HourlyExecutionLimit)
				}
				if wf.Guide == nil || tt.response.Guide == nil || *wf.Guide != *tt.response.Guide {
					t.Errorf("Get() Guide = %v, want %v", wf.Guide, tt.response.Guide)
				}
			}
		})
	}
}

func TestWorkflowJSONRoundTripPreservesDefinitionFields(t *testing.T) {
	wf := Workflow{
		ID:                   "wf-123",
		Title:                "Test Workflow",
		IsDeployed:           true,
		Description:          "A test workflow",
		Actor:                "user-123",
		Owner:                "user-123",
		OwnerType:            "USER",
		Private:              false,
		SchemaVersion:        4,
		Trigger:              map[string]interface{}{"manual": true},
		Result:               stringPtr("{{ result('task1') }}"),
		Type:                 "STANDARD",
		Input:                map[string]interface{}{"env": map[string]interface{}{"type": "string"}},
		HourlyExecutionLimit: intPtr(1000),
		Guide:                stringPtr("# Workflow Guide\n\nUse this workflow for testing."),
	}

	data, err := json.Marshal(wf)
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}

	var roundTrip Workflow
	if err := json.Unmarshal(data, &roundTrip); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}

	if roundTrip.SchemaVersion != wf.SchemaVersion {
		t.Errorf("SchemaVersion = %d, want %d", roundTrip.SchemaVersion, wf.SchemaVersion)
	}
	if roundTrip.Type != wf.Type {
		t.Errorf("Type = %q, want %q", roundTrip.Type, wf.Type)
	}
	inputField, ok := roundTrip.Input["env"].(map[string]interface{})
	if !ok {
		t.Fatalf("Input[env] type = %T, want map[string]interface{}", roundTrip.Input["env"])
	}
	if inputField["type"] != "string" {
		t.Errorf("Input[env][type] = %#v, want %q", inputField["type"], "string")
	}
	if roundTrip.Result == nil || *roundTrip.Result != *wf.Result {
		t.Errorf("Result = %v, want %v", roundTrip.Result, wf.Result)
	}
	if roundTrip.HourlyExecutionLimit == nil || *roundTrip.HourlyExecutionLimit != *wf.HourlyExecutionLimit {
		t.Errorf("HourlyExecutionLimit = %v, want %v", roundTrip.HourlyExecutionLimit, wf.HourlyExecutionLimit)
	}
	if roundTrip.Guide == nil || *roundTrip.Guide != *wf.Guide {
		t.Errorf("Guide = %v, want %v", roundTrip.Guide, wf.Guide)
	}
}

func TestWorkflowFilters(t *testing.T) {
	// Test that WorkflowFilters struct has the expected fields
	filters := WorkflowFilters{
		Owner: "user-123",
	}

	if filters.Owner != "user-123" {
		t.Errorf("WorkflowFilters.Owner = %v, want user-123", filters.Owner)
	}

	// Test empty filters
	emptyFilters := WorkflowFilters{}
	if emptyFilters.Owner != "" {
		t.Errorf("Empty WorkflowFilters.Owner should be empty, got %v", emptyFilters.Owner)
	}
}

func TestHandler_Delete(t *testing.T) {
	tests := []struct {
		name       string
		id         string
		statusCode int
		wantErr    bool
	}{
		{
			name:       "delete existing workflow",
			id:         "wf-123",
			statusCode: http.StatusNoContent,
			wantErr:    false,
		},
		{
			name:       "delete non-existent workflow (404)",
			id:         "wf-nonexistent",
			statusCode: http.StatusNotFound,
			wantErr:    true,
		},
		{
			name:       "access denied (403)",
			id:         "wf-forbidden",
			statusCode: http.StatusForbidden,
			wantErr:    true,
		},
		{
			name:       "server error (500)",
			id:         "wf-error",
			statusCode: http.StatusInternalServerError,
			wantErr:    true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				expectedPath := "/platform/automation/v1/workflows/" + tt.id
				if r.URL.Path != expectedPath {
					t.Errorf("unexpected path: got %s, want %s", r.URL.Path, expectedPath)
				}
				if r.Method != http.MethodDelete {
					t.Errorf("unexpected method: got %s, want DELETE", r.Method)
				}

				w.WriteHeader(tt.statusCode)
			}))
			defer server.Close()

			c, err := client.NewForTesting(server.URL, "test-token")
			if err != nil {
				t.Fatalf("client.New() error = %v", err)
			}
			c.HTTP().SetRetryCount(0)

			handler := NewHandler(c)
			err = handler.Delete(tt.id)

			if (err != nil) != tt.wantErr {
				t.Errorf("Delete() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
		})
	}
}

func TestHandler_GetRaw(t *testing.T) {
	tests := []struct {
		name         string
		id           string
		statusCode   int
		response     string
		wantErr      bool
		wantRawEqual string
	}{
		{
			name:         "get raw workflow",
			id:           "wf-123",
			statusCode:   http.StatusOK,
			response:     `{"id":"wf-123","title":"Test"}`,
			wantErr:      false,
			wantRawEqual: `{"id":"wf-123","title":"Test"}`,
		},
		{
			name:       "workflow not found (404)",
			id:         "wf-nonexistent",
			statusCode: http.StatusNotFound,
			response:   `{"error":"not found"}`,
			wantErr:    true,
		},
		{
			name:       "access denied (403)",
			id:         "wf-forbidden",
			statusCode: http.StatusForbidden,
			response:   `{"error":"forbidden"}`,
			wantErr:    true,
		},
		{
			name:       "server error (500)",
			id:         "wf-error",
			statusCode: http.StatusInternalServerError,
			response:   `{"error":"internal error"}`,
			wantErr:    true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				expectedPath := "/platform/automation/v1/workflows/" + tt.id
				if r.URL.Path != expectedPath {
					t.Errorf("unexpected path: got %s, want %s", r.URL.Path, expectedPath)
				}

				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(tt.statusCode)
				w.Write([]byte(tt.response))
			}))
			defer server.Close()

			c, err := client.NewForTesting(server.URL, "test-token")
			if err != nil {
				t.Fatalf("client.New() error = %v", err)
			}
			c.HTTP().SetRetryCount(0)

			handler := NewHandler(c)
			rawData, err := handler.GetRaw(tt.id)

			if (err != nil) != tt.wantErr {
				t.Errorf("GetRaw() error = %v, wantErr %v", err, tt.wantErr)
				return
			}

			if !tt.wantErr {
				if string(rawData) != tt.wantRawEqual {
					t.Errorf("GetRaw() = %q, want %q", string(rawData), tt.wantRawEqual)
				}
			}
		})
	}
}

func TestHandler_Update(t *testing.T) {
	tests := []struct {
		name       string
		id         string
		data       []byte
		statusCode int
		response   Workflow
		wantErr    bool
	}{
		{
			name:       "update existing workflow",
			id:         "wf-123",
			data:       []byte(`{"title":"Updated Workflow"}`),
			statusCode: http.StatusOK,
			response: Workflow{
				ID:    "wf-123",
				Title: "Updated Workflow",
			},
			wantErr: false,
		},
		{
			name:       "update non-existent workflow (404)",
			id:         "wf-nonexistent",
			data:       []byte(`{"title":"Test"}`),
			statusCode: http.StatusNotFound,
			wantErr:    true,
		},
		{
			name:       "access denied (403)",
			id:         "wf-forbidden",
			data:       []byte(`{"title":"Test"}`),
			statusCode: http.StatusForbidden,
			wantErr:    true,
		},
		{
			name:       "invalid data (400)",
			id:         "wf-123",
			data:       []byte(`{"invalid":"data"}`),
			statusCode: http.StatusBadRequest,
			wantErr:    true,
		},
		{
			name:       "server error (500)",
			id:         "wf-error",
			data:       []byte(`{"title":"Test"}`),
			statusCode: http.StatusInternalServerError,
			wantErr:    true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				expectedPath := "/platform/automation/v1/workflows/" + tt.id
				if r.URL.Path != expectedPath {
					t.Errorf("unexpected path: got %s, want %s", r.URL.Path, expectedPath)
				}
				if r.Method != http.MethodPut {
					t.Errorf("unexpected method: got %s, want PUT", r.Method)
				}

				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(tt.statusCode)
				_ = json.NewEncoder(w).Encode(tt.response)
			}))
			defer server.Close()

			c, err := client.NewForTesting(server.URL, "test-token")
			if err != nil {
				t.Fatalf("client.New() error = %v", err)
			}
			c.HTTP().SetRetryCount(0)

			handler := NewHandler(c)
			wf, err := handler.Update(tt.id, tt.data)

			if (err != nil) != tt.wantErr {
				t.Errorf("Update() error = %v, wantErr %v", err, tt.wantErr)
				return
			}

			if !tt.wantErr {
				if wf == nil {
					t.Fatal("Update() returned nil")
				}
				if wf.ID != tt.response.ID {
					t.Errorf("Update() ID = %v, want %v", wf.ID, tt.response.ID)
				}
				if wf.Title != tt.response.Title {
					t.Errorf("Update() Title = %v, want %v", wf.Title, tt.response.Title)
				}
			}
		})
	}
}

func TestHandler_Create(t *testing.T) {
	tests := []struct {
		name       string
		data       []byte
		statusCode int
		response   Workflow
		wantErr    bool
	}{
		{
			name:       "create new workflow",
			data:       []byte(`{"title":"New Workflow"}`),
			statusCode: http.StatusCreated,
			response: Workflow{
				ID:    "wf-new-123",
				Title: "New Workflow",
			},
			wantErr: false,
		},
		{
			name:       "invalid data (400)",
			data:       []byte(`{"invalid":"data"}`),
			statusCode: http.StatusBadRequest,
			wantErr:    true,
		},
		{
			name:       "access denied (403)",
			data:       []byte(`{"title":"Test"}`),
			statusCode: http.StatusForbidden,
			wantErr:    true,
		},
		{
			name:       "server error (500)",
			data:       []byte(`{"title":"Test"}`),
			statusCode: http.StatusInternalServerError,
			wantErr:    true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				expectedPath := "/platform/automation/v1/workflows"
				if r.URL.Path != expectedPath {
					t.Errorf("unexpected path: got %s, want %s", r.URL.Path, expectedPath)
				}
				if r.Method != http.MethodPost {
					t.Errorf("unexpected method: got %s, want POST", r.Method)
				}

				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(tt.statusCode)
				_ = json.NewEncoder(w).Encode(tt.response)
			}))
			defer server.Close()

			c, err := client.NewForTesting(server.URL, "test-token")
			if err != nil {
				t.Fatalf("client.New() error = %v", err)
			}
			c.HTTP().SetRetryCount(0)

			handler := NewHandler(c)
			wf, err := handler.Create(tt.data)

			if (err != nil) != tt.wantErr {
				t.Errorf("Create() error = %v, wantErr %v", err, tt.wantErr)
				return
			}

			if !tt.wantErr {
				if wf == nil {
					t.Fatal("Create() returned nil")
				}
				if wf.ID != tt.response.ID {
					t.Errorf("Create() ID = %v, want %v", wf.ID, tt.response.ID)
				}
				if wf.Title != tt.response.Title {
					t.Errorf("Create() Title = %v, want %v", wf.Title, tt.response.Title)
				}
			}
		})
	}
}

func TestHandler_ListHistory(t *testing.T) {
	tests := []struct {
		name       string
		workflowID string
		statusCode int
		response   HistoryList
		wantErr    bool
		errMsg     string
		wantCount  int
	}{
		{
			name:       "list history for workflow",
			workflowID: "wf-123",
			statusCode: http.StatusOK,
			response: HistoryList{
				Count: 2,
				Results: []HistoryRecord{
					{Version: 2, User: "user-123", DateCreated: "2024-01-02T00:00:00Z"},
					{Version: 1, User: "user-456", DateCreated: "2024-01-01T00:00:00Z"},
				},
			},
			wantErr:   false,
			wantCount: 2,
		},
		{
			name:       "workflow not found (404)",
			workflowID: "wf-nonexistent",
			statusCode: http.StatusNotFound,
			wantErr:    true,
			errMsg:     "list workflow history: API error (404): 404 Not Found",
		},
		{
			name:       "access denied (403)",
			workflowID: "wf-forbidden",
			statusCode: http.StatusForbidden,
			wantErr:    true,
			errMsg:     "list workflow history: API error (403): 403 Forbidden",
		},
		{
			name:       "server error (500)",
			workflowID: "wf-error",
			statusCode: http.StatusInternalServerError,
			wantErr:    true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				expectedPath := "/platform/automation/v1/workflows/" + tt.workflowID + "/history"
				if r.URL.Path != expectedPath {
					t.Errorf("unexpected path: got %s, want %s", r.URL.Path, expectedPath)
				}

				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(tt.statusCode)
				_ = json.NewEncoder(w).Encode(tt.response)
			}))
			defer server.Close()

			c, err := client.NewForTesting(server.URL, "test-token")
			if err != nil {
				t.Fatalf("client.New() error = %v", err)
			}
			c.HTTP().SetRetryCount(0)

			handler := NewHandler(c)
			history, err := handler.ListHistory(tt.workflowID)

			if (err != nil) != tt.wantErr {
				t.Errorf("ListHistory() error = %v, wantErr %v", err, tt.wantErr)
				return
			}

			if tt.wantErr && tt.errMsg != "" {
				if err.Error() != tt.errMsg {
					t.Errorf("ListHistory() error = %q, want %q", err.Error(), tt.errMsg)
				}
			}

			if !tt.wantErr {
				if history == nil {
					t.Fatal("ListHistory() returned nil")
				}
				if len(history.Results) != tt.wantCount {
					t.Errorf("ListHistory() returned %d records, want %d", len(history.Results), tt.wantCount)
				}
			}
		})
	}
}

func TestHandler_GetHistoryRecord(t *testing.T) {
	tests := []struct {
		name       string
		workflowID string
		version    int
		statusCode int
		response   Workflow
		wantErr    bool
		errMsg     string
	}{
		{
			name:       "get history record",
			workflowID: "wf-123",
			version:    2,
			statusCode: http.StatusOK,
			response: Workflow{
				ID:    "wf-123",
				Title: "Workflow v2",
			},
			wantErr: false,
		},
		{
			name:       "workflow or version not found (404)",
			workflowID: "wf-nonexistent",
			version:    1,
			statusCode: http.StatusNotFound,
			wantErr:    true,
			errMsg:     "get workflow history record: API error (404): 404 Not Found",
		},
		{
			name:       "access denied (403)",
			workflowID: "wf-forbidden",
			version:    1,
			statusCode: http.StatusForbidden,
			wantErr:    true,
			errMsg:     "get workflow history record: API error (403): 403 Forbidden",
		},
		{
			name:       "server error (500)",
			workflowID: "wf-error",
			version:    1,
			statusCode: http.StatusInternalServerError,
			wantErr:    true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				expectedPath := "/platform/automation/v1/workflows/" + tt.workflowID + "/history/2"
				if tt.version == 1 {
					expectedPath = "/platform/automation/v1/workflows/" + tt.workflowID + "/history/1"
				}
				if r.URL.Path != expectedPath {
					t.Errorf("unexpected path: got %s, want %s", r.URL.Path, expectedPath)
				}

				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(tt.statusCode)
				_ = json.NewEncoder(w).Encode(tt.response)
			}))
			defer server.Close()

			c, err := client.NewForTesting(server.URL, "test-token")
			if err != nil {
				t.Fatalf("client.New() error = %v", err)
			}
			c.HTTP().SetRetryCount(0)

			handler := NewHandler(c)
			wf, err := handler.GetHistoryRecord(tt.workflowID, tt.version)

			if (err != nil) != tt.wantErr {
				t.Errorf("GetHistoryRecord() error = %v, wantErr %v", err, tt.wantErr)
				return
			}

			if tt.wantErr && tt.errMsg != "" {
				if err.Error() != tt.errMsg {
					t.Errorf("GetHistoryRecord() error = %q, want %q", err.Error(), tt.errMsg)
				}
			}

			if !tt.wantErr {
				if wf == nil {
					t.Fatal("GetHistoryRecord() returned nil")
				}
				if wf.ID != tt.response.ID {
					t.Errorf("GetHistoryRecord() ID = %v, want %v", wf.ID, tt.response.ID)
				}
			}
		})
	}
}

func TestHandler_RestoreHistory(t *testing.T) {
	tests := []struct {
		name       string
		workflowID string
		version    int
		statusCode int
		response   Workflow
		wantErr    bool
		errMsg     string
	}{
		{
			name:       "restore workflow to version",
			workflowID: "wf-123",
			version:    2,
			statusCode: http.StatusOK,
			response: Workflow{
				ID:    "wf-123",
				Title: "Restored Workflow",
			},
			wantErr: false,
		},
		{
			name:       "workflow or version not found (404)",
			workflowID: "wf-nonexistent",
			version:    1,
			statusCode: http.StatusNotFound,
			wantErr:    true,
			errMsg:     "restore workflow: API error (404): 404 Not Found",
		},
		{
			name:       "access denied (403)",
			workflowID: "wf-forbidden",
			version:    1,
			statusCode: http.StatusForbidden,
			wantErr:    true,
			errMsg:     "restore workflow: API error (403): 403 Forbidden",
		},
		{
			name:       "server error (500)",
			workflowID: "wf-error",
			version:    1,
			statusCode: http.StatusInternalServerError,
			wantErr:    true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				expectedPath := "/platform/automation/v1/workflows/" + tt.workflowID + "/history/2/restore"
				if tt.version == 1 {
					expectedPath = "/platform/automation/v1/workflows/" + tt.workflowID + "/history/1/restore"
				}
				if r.URL.Path != expectedPath {
					t.Errorf("unexpected path: got %s, want %s", r.URL.Path, expectedPath)
				}
				if r.Method != http.MethodPost {
					t.Errorf("unexpected method: got %s, want POST", r.Method)
				}

				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(tt.statusCode)
				_ = json.NewEncoder(w).Encode(tt.response)
			}))
			defer server.Close()

			c, err := client.NewForTesting(server.URL, "test-token")
			if err != nil {
				t.Fatalf("client.New() error = %v", err)
			}
			c.HTTP().SetRetryCount(0)

			handler := NewHandler(c)
			wf, err := handler.RestoreHistory(tt.workflowID, tt.version)

			if (err != nil) != tt.wantErr {
				t.Errorf("RestoreHistory() error = %v, wantErr %v", err, tt.wantErr)
				return
			}

			if tt.wantErr && tt.errMsg != "" {
				if err.Error() != tt.errMsg {
					t.Errorf("RestoreHistory() error = %q, want %q", err.Error(), tt.errMsg)
				}
			}

			if !tt.wantErr {
				if wf == nil {
					t.Fatal("RestoreHistory() returned nil")
				}
				if wf.ID != tt.response.ID {
					t.Errorf("RestoreHistory() ID = %v, want %v", wf.ID, tt.response.ID)
				}
			}
		})
	}
}
