package slo

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Dynatrace/dtctl/pkg/client"
)

func TestNewHandler(t *testing.T) {
	c, err := client.New("https://test.dynatrace.com", "test-token")
	if err != nil {
		t.Fatalf("failed to create client: %v", err)
	}
	h := NewHandler(c)

	if h == nil {
		t.Fatal("NewHandler() returned nil")
	}
	if h.sdk == nil {
		t.Error("Handler.sdk is nil")
	}
}

func TestList(t *testing.T) {
	tests := []struct {
		name          string
		chunkSize     int64
		filter        string
		pages         []SLOList
		expectError   bool
		errorContains string
		validate      func(*testing.T, *SLOList)
	}{
		{
			name:      "successful list single page",
			chunkSize: 0, // No chunking, return first page only
			pages: []SLOList{
				{
					TotalCount: 2,
					SLOs: []SLO{
						{
							ID:          "slo-1",
							Name:        "Service Availability",
							Description: "Uptime > 99.9%",
						},
						{
							ID:          "slo-2",
							Name:        "Response Time",
							Description: "P95 < 500ms",
						},
					},
				},
			},
			expectError: false,
			validate: func(t *testing.T, result *SLOList) {
				if len(result.SLOs) != 2 {
					t.Errorf("expected 2 SLOs, got %d", len(result.SLOs))
				}
				if result.TotalCount != 2 {
					t.Errorf("expected TotalCount 2, got %d", result.TotalCount)
				}
			},
		},
		{
			name:      "paginated list with chunking",
			chunkSize: 10,
			pages: []SLOList{
				{
					TotalCount:  3,
					NextPageKey: "page2",
					SLOs: []SLO{
						{ID: "slo-1", Name: "SLO 1"},
						{ID: "slo-2", Name: "SLO 2"},
					},
				},
				{
					TotalCount:  3,
					NextPageKey: "",
					SLOs: []SLO{
						{ID: "slo-3", Name: "SLO 3"},
					},
				},
			},
			expectError: false,
			validate: func(t *testing.T, result *SLOList) {
				if len(result.SLOs) != 3 {
					t.Errorf("expected 3 SLOs across pages, got %d", len(result.SLOs))
				}
				if result.TotalCount != 3 {
					t.Errorf("expected TotalCount 3, got %d", result.TotalCount)
				}
			},
		},
		{
			name:      "empty list",
			chunkSize: 0,
			pages: []SLOList{
				{
					TotalCount: 0,
					SLOs:       []SLO{},
				},
			},
			expectError: false,
			validate: func(t *testing.T, result *SLOList) {
				if len(result.SLOs) != 0 {
					t.Errorf("expected 0 SLOs, got %d", len(result.SLOs))
				}
			},
		},
		{
			name:      "with filter",
			chunkSize: 0,
			filter:    "name contains 'test'",
			pages: []SLOList{
				{
					TotalCount: 1,
					SLOs: []SLO{
						{ID: "slo-test", Name: "Test SLO"},
					},
				},
			},
			expectError: false,
			validate: func(t *testing.T, result *SLOList) {
				if len(result.SLOs) != 1 {
					t.Errorf("expected 1 filtered SLO, got %d", len(result.SLOs))
				}
			},
		},
		{
			name:      "paginated list with filter",
			chunkSize: 10,
			filter:    "name contains 'test'",
			pages: []SLOList{
				{
					TotalCount:  2,
					NextPageKey: "page2",
					SLOs: []SLO{
						{ID: "slo-1", Name: "Test SLO 1"},
					},
				},
				{
					TotalCount: 2,
					SLOs: []SLO{
						{ID: "slo-2", Name: "Test SLO 2"},
					},
				},
			},
			expectError: false,
			validate: func(t *testing.T, result *SLOList) {
				if len(result.SLOs) != 2 {
					t.Errorf("expected 2 SLOs across pages, got %d", len(result.SLOs))
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pageIndex := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/platform/slo/v1/slos" {
					t.Errorf("unexpected path: %s", r.URL.Path)
					w.WriteHeader(http.StatusNotFound)
					return
				}

				// Simulate API constraint: page-size must not be combined with page-key
				if r.URL.Query().Get("page-key") != "" {
					if r.URL.Query().Get("page-size") != "" {
						t.Error("page-size must not be sent with page-key")
						w.WriteHeader(http.StatusBadRequest)
						return
					}
				}

				// Verify filter is sent on every request (page tokens may not preserve it)
				if tt.filter != "" {
					filter := r.URL.Query().Get("filter")
					if filter != tt.filter {
						t.Errorf("expected filter %q on every request, got %q", tt.filter, filter)
					}
				}

				if pageIndex >= len(tt.pages) {
					t.Error("received more requests than expected pages")
					w.WriteHeader(http.StatusInternalServerError)
					return
				}

				w.Header().Set("Content-Type", "application/json")
				json.NewEncoder(w).Encode(tt.pages[pageIndex])
				pageIndex++
			}))
			defer server.Close()

			c, err := client.NewForTesting(server.URL, "test-token")
			if err != nil {
				t.Fatalf("failed to create client: %v", err)
			}
			handler := NewHandler(c)

			result, err := handler.List(tt.filter, tt.chunkSize)

			if (err != nil) != tt.expectError {
				t.Errorf("List() error = %v, expectError %v", err, tt.expectError)
				return
			}

			if tt.expectError && tt.errorContains != "" {
				if !strings.Contains(err.Error(), tt.errorContains) {
					t.Errorf("expected error to contain %q, got %q", tt.errorContains, err.Error())
				}
			}

			if !tt.expectError && tt.validate != nil {
				tt.validate(t, result)
			}
		})
	}
}

func TestGet(t *testing.T) {
	tests := []struct {
		name          string
		sloID         string
		statusCode    int
		responseBody  interface{}
		expectError   bool
		errorContains string
	}{
		{
			name:       "successful get",
			sloID:      "slo-123",
			statusCode: 200,
			responseBody: SLO{
				ID:          "slo-123",
				Name:        "Service Availability",
				Description: "Uptime > 99.9%",
				Version:     "1",
			},
			expectError: false,
		},
		{
			name:          "SLO not found",
			sloID:         "nonexistent",
			statusCode:    404,
			responseBody:  map[string]string{"error": "Not found"},
			expectError:   true,
			errorContains: "Not found",
		},
		{
			name:          "access denied",
			sloID:         "slo-forbidden",
			statusCode:    403,
			responseBody:  map[string]string{"error": "Forbidden"},
			expectError:   true,
			errorContains: "API error (403)",
		},
		{
			name:          "server error",
			sloID:         "slo-error",
			statusCode:    500,
			responseBody:  map[string]string{"error": "Internal error"},
			expectError:   true,
			errorContains: "API error (500)",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				expectedPath := "/platform/slo/v1/slos/" + tt.sloID
				if r.URL.Path != expectedPath {
					t.Errorf("unexpected path: %s, want %s", r.URL.Path, expectedPath)
					w.WriteHeader(http.StatusNotFound)
					return
				}

				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(tt.statusCode)
				json.NewEncoder(w).Encode(tt.responseBody)
			}))
			defer server.Close()

			c, err := client.NewForTesting(server.URL, "test-token")
			if err != nil {
				t.Fatalf("failed to create client: %v", err)
			}
			handler := NewHandler(c)

			result, err := handler.Get(tt.sloID)

			if (err != nil) != tt.expectError {
				t.Errorf("Get() error = %v, expectError %v", err, tt.expectError)
				return
			}

			if tt.expectError {
				if tt.errorContains != "" && !strings.Contains(err.Error(), tt.errorContains) {
					t.Errorf("expected error to contain %q, got %q", tt.errorContains, err.Error())
				}
			} else {
				if result == nil {
					t.Fatal("Get() returned nil result without error")
				}
				if result.ID != tt.sloID {
					t.Errorf("expected ID %q, got %q", tt.sloID, result.ID)
				}
			}
		})
	}
}

func TestCreate(t *testing.T) {
	tests := []struct {
		name          string
		inputData     map[string]interface{}
		statusCode    int
		responseBody  interface{}
		expectError   bool
		errorContains string
	}{
		{
			name: "successful create",
			inputData: map[string]interface{}{
				"name":        "New SLO",
				"description": "Test SLO",
			},
			statusCode: 201,
			responseBody: SLO{
				ID:          "slo-new",
				Name:        "New SLO",
				Description: "Test SLO",
				Version:     "1",
			},
			expectError: false,
		},
		{
			name: "invalid configuration",
			inputData: map[string]interface{}{
				"name": "", // Invalid empty name
			},
			statusCode:    400,
			responseBody:  map[string]string{"error": "Invalid configuration"},
			expectError:   true,
			errorContains: "API error (400)",
		},
		{
			name: "access denied",
			inputData: map[string]interface{}{
				"name": "SLO",
			},
			statusCode:    403,
			responseBody:  map[string]string{"error": "Forbidden"},
			expectError:   true,
			errorContains: "API error (403)",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/platform/slo/v1/slos" {
					t.Errorf("unexpected path: %s", r.URL.Path)
					w.WriteHeader(http.StatusNotFound)
					return
				}
				if r.Method != http.MethodPost {
					t.Errorf("expected POST, got %s", r.Method)
				}

				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(tt.statusCode)
				json.NewEncoder(w).Encode(tt.responseBody)
			}))
			defer server.Close()

			c, err := client.NewForTesting(server.URL, "test-token")
			if err != nil {
				t.Fatalf("failed to create client: %v", err)
			}
			handler := NewHandler(c)

			data, _ := json.Marshal(tt.inputData)
			result, err := handler.Create(data)

			if (err != nil) != tt.expectError {
				t.Errorf("Create() error = %v, expectError %v", err, tt.expectError)
				return
			}

			if tt.expectError {
				if tt.errorContains != "" && !strings.Contains(err.Error(), tt.errorContains) {
					t.Errorf("expected error to contain %q, got %q", tt.errorContains, err.Error())
				}
			} else {
				if result == nil {
					t.Error("Create() returned nil result")
				}
			}
		})
	}
}

func TestUpdate(t *testing.T) {
	tests := []struct {
		name          string
		sloID         string
		version       string
		statusCode    int
		expectError   bool
		errorContains string
	}{
		{
			name:        "successful update",
			sloID:       "slo-123",
			version:     "1",
			statusCode:  200,
			expectError: false,
		},
		{
			name:          "invalid configuration",
			sloID:         "slo-123",
			version:       "1",
			statusCode:    400,
			expectError:   true,
			errorContains: "API error (400)",
		},
		{
			name:          "access denied",
			sloID:         "slo-forbidden",
			version:       "1",
			statusCode:    403,
			expectError:   true,
			errorContains: "API error (403)",
		},
		{
			name:          "not found",
			sloID:         "nonexistent",
			version:       "1",
			statusCode:    404,
			expectError:   true,
			errorContains: "API error (404)",
		},
		{
			name:          "version conflict",
			sloID:         "slo-123",
			version:       "1",
			statusCode:    409,
			expectError:   true,
			errorContains: "API error (409)",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				expectedPath := "/platform/slo/v1/slos/" + tt.sloID
				if r.URL.Path != expectedPath {
					t.Errorf("unexpected path: %s", r.URL.Path)
					w.WriteHeader(http.StatusNotFound)
					return
				}
				if r.Method != http.MethodPut {
					t.Errorf("expected PUT, got %s", r.Method)
				}

				// Verify version parameter
				version := r.URL.Query().Get("optimistic-locking-version")
				if version != tt.version {
					t.Errorf("expected version %q, got %q", tt.version, version)
				}

				w.WriteHeader(tt.statusCode)
			}))
			defer server.Close()

			c, err := client.NewForTesting(server.URL, "test-token")
			if err != nil {
				t.Fatalf("failed to create client: %v", err)
			}
			handler := NewHandler(c)

			data := []byte(`{"name":"Updated SLO"}`)
			err = handler.Update(tt.sloID, tt.version, data)

			if (err != nil) != tt.expectError {
				t.Errorf("Update() error = %v, expectError %v", err, tt.expectError)
				return
			}

			if tt.expectError && tt.errorContains != "" {
				if !strings.Contains(err.Error(), tt.errorContains) {
					t.Errorf("expected error to contain %q, got %q", tt.errorContains, err.Error())
				}
			}
		})
	}
}

func TestDelete(t *testing.T) {
	tests := []struct {
		name          string
		sloID         string
		version       string
		statusCode    int
		expectError   bool
		errorContains string
	}{
		{
			name:        "successful delete",
			sloID:       "slo-123",
			version:     "1",
			statusCode:  204,
			expectError: false,
		},
		{
			name:          "access denied",
			sloID:         "slo-forbidden",
			version:       "1",
			statusCode:    403,
			expectError:   true,
			errorContains: "API error (403)",
		},
		{
			name:          "not found",
			sloID:         "nonexistent",
			version:       "1",
			statusCode:    404,
			expectError:   true,
			errorContains: "API error (404)",
		},
		{
			name:          "version conflict",
			sloID:         "slo-123",
			version:       "1",
			statusCode:    409,
			expectError:   true,
			errorContains: "API error (409)",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				expectedPath := "/platform/slo/v1/slos/" + tt.sloID
				if r.URL.Path != expectedPath {
					t.Errorf("unexpected path: %s", r.URL.Path)
					w.WriteHeader(http.StatusNotFound)
					return
				}
				if r.Method != http.MethodDelete {
					t.Errorf("expected DELETE, got %s", r.Method)
				}

				// Verify version parameter
				version := r.URL.Query().Get("optimistic-locking-version")
				if version != tt.version {
					t.Errorf("expected version %q, got %q", tt.version, version)
				}

				w.WriteHeader(tt.statusCode)
			}))
			defer server.Close()

			c, err := client.NewForTesting(server.URL, "test-token")
			if err != nil {
				t.Fatalf("failed to create client: %v", err)
			}
			handler := NewHandler(c)

			err = handler.Delete(tt.sloID, tt.version)

			if (err != nil) != tt.expectError {
				t.Errorf("Delete() error = %v, expectError %v", err, tt.expectError)
				return
			}

			if tt.expectError && tt.errorContains != "" {
				if !strings.Contains(err.Error(), tt.errorContains) {
					t.Errorf("expected error to contain %q, got %q", tt.errorContains, err.Error())
				}
			}
		})
	}
}

func TestListTemplates(t *testing.T) {
	tests := []struct {
		name          string
		filter        string
		statusCode    int
		responseBody  interface{}
		expectError   bool
		errorContains string
	}{
		{
			name:       "successful list templates",
			filter:     "",
			statusCode: 200,
			responseBody: TemplateList{
				TotalCount: 2,
				Items: []Template{
					{
						ID:          "template-1",
						Name:        "Availability Template",
						BuiltIn:     true,
						Description: "Service availability",
					},
					{
						ID:          "template-2",
						Name:        "Performance Template",
						BuiltIn:     false,
						Description: "Performance metrics",
					},
				},
			},
			expectError: false,
		},
		{
			name:       "with filter",
			filter:     "builtIn == true",
			statusCode: 200,
			responseBody: TemplateList{
				TotalCount: 1,
				Items: []Template{
					{
						ID:      "template-1",
						Name:    "Availability Template",
						BuiltIn: true,
					},
				},
			},
			expectError: false,
		},
		{
			name:          "server error",
			filter:        "",
			statusCode:    500,
			responseBody:  map[string]string{"error": "Internal error"},
			expectError:   true,
			errorContains: "API error (500)",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/platform/slo/v1/objective-templates" {
					t.Errorf("unexpected path: %s", r.URL.Path)
					w.WriteHeader(http.StatusNotFound)
					return
				}

				// Verify filter parameter if provided
				if tt.filter != "" {
					filter := r.URL.Query().Get("filter")
					if filter != tt.filter {
						t.Errorf("expected filter %q, got %q", tt.filter, filter)
					}
				}

				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(tt.statusCode)
				json.NewEncoder(w).Encode(tt.responseBody)
			}))
			defer server.Close()

			c, err := client.NewForTesting(server.URL, "test-token")
			if err != nil {
				t.Fatalf("failed to create client: %v", err)
			}
			handler := NewHandler(c)

			result, err := handler.ListTemplates(tt.filter)

			if (err != nil) != tt.expectError {
				t.Errorf("ListTemplates() error = %v, expectError %v", err, tt.expectError)
				return
			}

			if tt.expectError {
				if tt.errorContains != "" && !strings.Contains(err.Error(), tt.errorContains) {
					t.Errorf("expected error to contain %q, got %q", tt.errorContains, err.Error())
				}
			} else {
				if result == nil {
					t.Error("ListTemplates() returned nil")
				}
			}
		})
	}
}

func TestGetTemplate(t *testing.T) {
	tests := []struct {
		name          string
		templateID    string
		statusCode    int
		responseBody  interface{}
		expectError   bool
		errorContains string
	}{
		{
			name:       "successful get template",
			templateID: "template-123",
			statusCode: 200,
			responseBody: Template{
				ID:          "template-123",
				Name:        "Availability Template",
				Description: "Service availability",
				BuiltIn:     true,
			},
			expectError: false,
		},
		{
			name:          "template not found",
			templateID:    "nonexistent",
			statusCode:    404,
			responseBody:  map[string]string{"error": "Not found"},
			expectError:   true,
			errorContains: "Not found",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				expectedPath := "/platform/slo/v1/objective-templates/" + tt.templateID
				if r.URL.Path != expectedPath {
					t.Errorf("unexpected path: %s", r.URL.Path)
					w.WriteHeader(http.StatusNotFound)
					return
				}

				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(tt.statusCode)
				json.NewEncoder(w).Encode(tt.responseBody)
			}))
			defer server.Close()

			c, err := client.NewForTesting(server.URL, "test-token")
			if err != nil {
				t.Fatalf("failed to create client: %v", err)
			}
			handler := NewHandler(c)

			result, err := handler.GetTemplate(tt.templateID)

			if (err != nil) != tt.expectError {
				t.Errorf("GetTemplate() error = %v, expectError %v", err, tt.expectError)
				return
			}

			if tt.expectError {
				if tt.errorContains != "" && !strings.Contains(err.Error(), tt.errorContains) {
					t.Errorf("expected error to contain %q, got %q", tt.errorContains, err.Error())
				}
			} else {
				if result == nil {
					t.Fatal("GetTemplate() returned nil result without error")
				}
				if result.ID != tt.templateID {
					t.Errorf("expected ID %q, got %q", tt.templateID, result.ID)
				}
			}
		})
	}
}

func TestEvaluate(t *testing.T) {
	tests := []struct {
		name          string
		sloID         string
		statusCode    int
		responseBody  interface{}
		expectError   bool
		errorContains string
	}{
		{
			name:       "successful evaluation start",
			sloID:      "slo-123",
			statusCode: 200,
			responseBody: EvaluationResponse{
				EvaluationToken: "token-abc-123",
				TTLSeconds:      3600,
			},
			expectError: false,
		},
		{
			name:          "SLO not found",
			sloID:         "nonexistent",
			statusCode:    404,
			responseBody:  map[string]string{"error": "Not found"},
			expectError:   true,
			errorContains: "Not found",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/platform/slo/v1/slos/evaluation:start" {
					t.Errorf("unexpected path: %s", r.URL.Path)
					w.WriteHeader(http.StatusNotFound)
					return
				}
				if r.Method != http.MethodPost {
					t.Errorf("expected POST, got %s", r.Method)
				}

				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(tt.statusCode)
				json.NewEncoder(w).Encode(tt.responseBody)
			}))
			defer server.Close()

			c, err := client.NewForTesting(server.URL, "test-token")
			if err != nil {
				t.Fatalf("failed to create client: %v", err)
			}
			handler := NewHandler(c)

			result, err := handler.Evaluate(tt.sloID)

			if (err != nil) != tt.expectError {
				t.Errorf("Evaluate() error = %v, expectError %v", err, tt.expectError)
				return
			}

			if tt.expectError {
				if tt.errorContains != "" && !strings.Contains(err.Error(), tt.errorContains) {
					t.Errorf("expected error to contain %q, got %q", tt.errorContains, err.Error())
				}
			} else {
				if result == nil {
					t.Error("Evaluate() returned nil")
				}
			}
		})
	}
}

func TestPollEvaluation(t *testing.T) {
	tests := []struct {
		name          string
		token         string
		timeoutMs     int
		statusCode    int
		responseBody  interface{}
		expectError   bool
		errorContains string
	}{
		{
			name:       "successful poll",
			token:      "token-abc-123",
			timeoutMs:  5000,
			statusCode: 200,
			responseBody: EvaluationResponse{
				EvaluationToken: "token-abc-123",
				EvaluationResults: []EvaluationResult{
					{
						Criteria: "availability",
						Status:   "success",
						Value:    floatPtr(99.95),
					},
				},
			},
			expectError: false,
		},
		{
			name:          "token expired",
			token:         "expired-token",
			timeoutMs:     0,
			statusCode:    410,
			responseBody:  map[string]string{"error": "Token expired"},
			expectError:   true,
			errorContains: "expired",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/platform/slo/v1/slos/evaluation:poll" {
					t.Errorf("unexpected path: %s", r.URL.Path)
					w.WriteHeader(http.StatusNotFound)
					return
				}

				// Verify token parameter
				token := r.URL.Query().Get("evaluation-token")
				if token != tt.token {
					t.Errorf("expected token %q, got %q", tt.token, token)
				}

				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(tt.statusCode)
				json.NewEncoder(w).Encode(tt.responseBody)
			}))
			defer server.Close()

			c, err := client.NewForTesting(server.URL, "test-token")
			if err != nil {
				t.Fatalf("failed to create client: %v", err)
			}
			handler := NewHandler(c)

			result, err := handler.PollEvaluation(tt.token, tt.timeoutMs)

			if (err != nil) != tt.expectError {
				t.Errorf("PollEvaluation() error = %v, expectError %v", err, tt.expectError)
				return
			}

			if tt.expectError {
				if tt.errorContains != "" && !strings.Contains(err.Error(), tt.errorContains) {
					t.Errorf("expected error to contain %q, got %q", tt.errorContains, err.Error())
				}
			} else {
				if result == nil {
					t.Error("PollEvaluation() returned nil")
				}
			}
		})
	}
}

func TestGetRaw(t *testing.T) {
	tests := []struct {
		name          string
		sloID         string
		statusCode    int
		responseBody  string
		expectError   bool
		errorContains string
		validateJSON  bool
	}{
		{
			name:         "successful get raw",
			sloID:        "slo-123",
			statusCode:   200,
			responseBody: `{"id":"slo-123","name":"Test SLO","version":"1"}`,
			expectError:  false,
			validateJSON: true,
		},
		{
			name:          "SLO not found",
			sloID:         "nonexistent",
			statusCode:    404,
			responseBody:  `{"error":"Not found"}`,
			expectError:   true,
			errorContains: "Not found",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				expectedPath := "/platform/slo/v1/slos/" + tt.sloID
				if r.URL.Path != expectedPath {
					t.Errorf("unexpected path: %s", r.URL.Path)
					w.WriteHeader(http.StatusNotFound)
					return
				}

				w.WriteHeader(tt.statusCode)
				w.Header().Set("Content-Type", "application/json")
				w.Write([]byte(tt.responseBody))
			}))
			defer server.Close()

			c, err := client.NewForTesting(server.URL, "test-token")
			if err != nil {
				t.Fatalf("failed to create client: %v", err)
			}
			handler := NewHandler(c)

			result, err := handler.GetRaw(tt.sloID)

			if (err != nil) != tt.expectError {
				t.Errorf("GetRaw() error = %v, expectError %v", err, tt.expectError)
				return
			}

			if tt.expectError {
				if tt.errorContains != "" && !strings.Contains(err.Error(), tt.errorContains) {
					t.Errorf("expected error to contain %q, got %q", tt.errorContains, err.Error())
				}
			} else {
				if result == nil {
					t.Error("GetRaw() returned nil")
				}
				// Verify it's valid JSON with pretty formatting
				if tt.validateJSON {
					var data interface{}
					if err := json.Unmarshal(result, &data); err != nil {
						t.Errorf("GetRaw() returned invalid JSON: %v", err)
					}
					// Check for pretty printing (should have indentation)
					if !strings.Contains(string(result), "  ") {
						t.Error("GetRaw() should return pretty-printed JSON with indentation")
					}
				}
			}
		})
	}
}

// floatPtr returns a pointer to a float64
func floatPtr(f float64) *float64 {
	return &f
}
