package notification

import (
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

func TestListEventNotifications(t *testing.T) {
	tests := []struct {
		name             string
		notificationType string
		statusCode       int
		responseBody     string
		expectError      bool
		errorContains    string
		validate         func(*testing.T, *EventNotificationList)
	}{
		{
			name:             "successful list without filter",
			notificationType: "",
			statusCode:       200,
			responseBody: `{
				"results": [
					{
						"id": "notif-1",
						"notificationType": "workflow",
						"enabled": true,
						"appId": "app-123"
					},
					{
						"id": "notif-2",
						"notificationType": "email",
						"enabled": false,
						"appId": "app-456"
					}
				],
				"count": 2
			}`,
			expectError: false,
			validate: func(t *testing.T, result *EventNotificationList) {
				if result.Count != 2 {
					t.Errorf("expected count 2, got %d", result.Count)
				}
				if len(result.Results) != 2 {
					t.Errorf("expected 2 results, got %d", len(result.Results))
				}
				if result.Results[0].ID != "notif-1" {
					t.Errorf("expected first ID 'notif-1', got %q", result.Results[0].ID)
				}
			},
		},
		{
			name:             "successful list with filter",
			notificationType: "workflow",
			statusCode:       200,
			responseBody: `{
				"results": [
					{
						"id": "notif-1",
						"notificationType": "workflow",
						"enabled": true
					}
				],
				"count": 1
			}`,
			expectError: false,
			validate: func(t *testing.T, result *EventNotificationList) {
				if result.Count != 1 {
					t.Errorf("expected count 1, got %d", result.Count)
				}
				if result.Results[0].NotificationType != "workflow" {
					t.Errorf("expected type 'workflow', got %q", result.Results[0].NotificationType)
				}
			},
		},
		{
			name:          "server error",
			statusCode:    500,
			responseBody:  "internal server error",
			expectError:   true,
			errorContains: "API error (500)",
		},
		{
			name:          "forbidden",
			statusCode:    403,
			responseBody:  "access denied",
			expectError:   true,
			errorContains: "API error (403)",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/platform/notification/v2/event-notifications" {
					t.Errorf("expected path '/platform/notification/v2/event-notifications', got %q", r.URL.Path)
				}
				if tt.notificationType != "" {
					if r.URL.Query().Get("notificationType") != tt.notificationType {
						t.Errorf("expected notificationType query param %q, got %q", tt.notificationType, r.URL.Query().Get("notificationType"))
					}
				}
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(tt.statusCode)
				w.Write([]byte(tt.responseBody))
			}))
			defer server.Close()

			c, err := client.NewForTesting(server.URL, "test-token")
			if err != nil {
				t.Fatalf("failed to create client: %v", err)
			}
			h := NewHandler(c)

			result, err := h.ListEventNotifications(tt.notificationType)

			if tt.expectError {
				if err == nil {
					t.Error("expected error, got nil")
				} else if tt.errorContains != "" && !strings.Contains(err.Error(), tt.errorContains) {
					t.Errorf("expected error containing %q, got %q", tt.errorContains, err.Error())
				}
			} else {
				if err != nil {
					t.Errorf("unexpected error: %v", err)
				}
				if tt.validate != nil {
					tt.validate(t, result)
				}
			}
		})
	}
}

func TestGetEventNotification(t *testing.T) {
	tests := []struct {
		name          string
		id            string
		statusCode    int
		responseBody  string
		expectError   bool
		errorContains string
		validate      func(*testing.T, *EventNotification)
	}{
		{
			name:       "successful get",
			id:         "notif-123",
			statusCode: 200,
			responseBody: `{
				"id": "notif-123",
				"notificationType": "workflow",
				"enabled": true,
				"appId": "app-123",
				"owner": "user@example.com"
			}`,
			expectError: false,
			validate: func(t *testing.T, notif *EventNotification) {
				if notif.ID != "notif-123" {
					t.Errorf("expected ID 'notif-123', got %q", notif.ID)
				}
				if notif.NotificationType != "workflow" {
					t.Errorf("expected type 'workflow', got %q", notif.NotificationType)
				}
			},
		},
		{
			name:          "notification not found",
			id:            "non-existent",
			statusCode:    404,
			responseBody:  "not found",
			expectError:   true,
			errorContains: "not found",
		},
		{
			name:          "server error",
			id:            "notif-123",
			statusCode:    500,
			responseBody:  "internal error",
			expectError:   true,
			errorContains: "API error (500)",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				expectedPath := "/platform/notification/v2/event-notifications/" + tt.id
				if r.URL.Path != expectedPath {
					t.Errorf("expected path %q, got %q", expectedPath, r.URL.Path)
				}
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(tt.statusCode)
				w.Write([]byte(tt.responseBody))
			}))
			defer server.Close()

			c, err := client.NewForTesting(server.URL, "test-token")
			if err != nil {
				t.Fatalf("failed to create client: %v", err)
			}
			h := NewHandler(c)

			result, err := h.GetEventNotification(tt.id)

			if tt.expectError {
				if err == nil {
					t.Error("expected error, got nil")
				} else if tt.errorContains != "" && !strings.Contains(err.Error(), tt.errorContains) {
					t.Errorf("expected error containing %q, got %q", tt.errorContains, err.Error())
				}
			} else {
				if err != nil {
					t.Errorf("unexpected error: %v", err)
				}
				if tt.validate != nil {
					tt.validate(t, result)
				}
			}
		})
	}
}

func TestCreateEventNotification(t *testing.T) {
	tests := []struct {
		name          string
		data          []byte
		statusCode    int
		responseBody  string
		expectError   bool
		errorContains string
		validate      func(*testing.T, *EventNotification)
	}{
		{
			name:       "successful create",
			data:       []byte(`{"notificationType":"workflow","enabled":true}`),
			statusCode: 201,
			responseBody: `{
				"id": "notif-new",
				"notificationType": "workflow",
				"enabled": true
			}`,
			expectError: false,
			validate: func(t *testing.T, notif *EventNotification) {
				if notif.ID != "notif-new" {
					t.Errorf("expected ID 'notif-new', got %q", notif.ID)
				}
			},
		},
		{
			name:          "invalid configuration",
			data:          []byte(`{}`),
			statusCode:    400,
			responseBody:  "invalid configuration",
			expectError:   true,
			errorContains: "invalid configuration",
		},
		{
			name:          "access denied",
			data:          []byte(`{"notificationType":"workflow"}`),
			statusCode:    403,
			responseBody:  "access denied",
			expectError:   true,
			errorContains: "access denied",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != "POST" {
					t.Errorf("expected POST method, got %s", r.Method)
				}
				if r.URL.Path != "/platform/notification/v2/event-notifications" {
					t.Errorf("expected path '/platform/notification/v2/event-notifications', got %q", r.URL.Path)
				}
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(tt.statusCode)
				w.Write([]byte(tt.responseBody))
			}))
			defer server.Close()

			c, err := client.NewForTesting(server.URL, "test-token")
			if err != nil {
				t.Fatalf("failed to create client: %v", err)
			}
			h := NewHandler(c)

			result, err := h.CreateEventNotification(tt.data)

			if tt.expectError {
				if err == nil {
					t.Error("expected error, got nil")
				} else if tt.errorContains != "" && !strings.Contains(err.Error(), tt.errorContains) {
					t.Errorf("expected error containing %q, got %q", tt.errorContains, err.Error())
				}
			} else {
				if err != nil {
					t.Errorf("unexpected error: %v", err)
				}
				if tt.validate != nil {
					tt.validate(t, result)
				}
			}
		})
	}
}

func TestDeleteEventNotification(t *testing.T) {
	tests := []struct {
		name          string
		id            string
		statusCode    int
		responseBody  string
		expectError   bool
		errorContains string
	}{
		{
			name:        "successful delete",
			id:          "notif-123",
			statusCode:  204,
			expectError: false,
		},
		{
			name:          "notification not found",
			id:            "non-existent",
			statusCode:    404,
			responseBody:  "not found",
			expectError:   true,
			errorContains: "not found",
		},
		{
			name:          "server error",
			id:            "notif-123",
			statusCode:    500,
			responseBody:  "internal error",
			expectError:   true,
			errorContains: "API error (500)",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != "DELETE" {
					t.Errorf("expected DELETE method, got %s", r.Method)
				}
				expectedPath := "/platform/notification/v2/event-notifications/" + tt.id
				if r.URL.Path != expectedPath {
					t.Errorf("expected path %q, got %q", expectedPath, r.URL.Path)
				}
				w.WriteHeader(tt.statusCode)
				w.Write([]byte(tt.responseBody))
			}))
			defer server.Close()

			c, err := client.NewForTesting(server.URL, "test-token")
			if err != nil {
				t.Fatalf("failed to create client: %v", err)
			}
			h := NewHandler(c)

			err = h.DeleteEventNotification(tt.id)

			if tt.expectError {
				if err == nil {
					t.Error("expected error, got nil")
				} else if tt.errorContains != "" && !strings.Contains(err.Error(), tt.errorContains) {
					t.Errorf("expected error containing %q, got %q", tt.errorContains, err.Error())
				}
			} else {
				if err != nil {
					t.Errorf("unexpected error: %v", err)
				}
			}
		})
	}
}

func TestListResourceNotifications(t *testing.T) {
	tests := []struct {
		name             string
		notificationType string
		resourceID       string
		statusCode       int
		responseBody     string
		expectError      bool
		errorContains    string
		validate         func(*testing.T, *ResourceNotificationList)
	}{
		{
			name:             "successful list without filter",
			notificationType: "",
			resourceID:       "",
			statusCode:       200,
			responseBody: `{
				"results": [
					{
						"id": "rn-1",
						"notificationType": "resource-changed",
						"resourceId": "resource-123",
						"appId": "app-123"
					}
				],
				"count": 1
			}`,
			expectError: false,
			validate: func(t *testing.T, result *ResourceNotificationList) {
				if result.Count != 1 {
					t.Errorf("expected count 1, got %d", result.Count)
				}
				if result.Results[0].ID != "rn-1" {
					t.Errorf("expected ID 'rn-1', got %q", result.Results[0].ID)
				}
			},
		},
		{
			name:             "successful list with filters",
			notificationType: "resource-changed",
			resourceID:       "resource-123",
			statusCode:       200,
			responseBody:     `{"results":[],"count":0}`,
			expectError:      false,
			validate: func(t *testing.T, result *ResourceNotificationList) {
				if result.Count != 0 {
					t.Errorf("expected count 0, got %d", result.Count)
				}
			},
		},
		{
			name:          "server error",
			statusCode:    500,
			responseBody:  "internal server error",
			expectError:   true,
			errorContains: "API error (500)",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/platform/notification/v2/resource-notifications" {
					t.Errorf("expected path '/platform/notification/v2/resource-notifications', got %q", r.URL.Path)
				}
				if tt.notificationType != "" {
					if r.URL.Query().Get("notificationType") != tt.notificationType {
						t.Errorf("expected notificationType %q, got %q", tt.notificationType, r.URL.Query().Get("notificationType"))
					}
				}
				if tt.resourceID != "" {
					if r.URL.Query().Get("resourceId") != tt.resourceID {
						t.Errorf("expected resourceId %q, got %q", tt.resourceID, r.URL.Query().Get("resourceId"))
					}
				}
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(tt.statusCode)
				w.Write([]byte(tt.responseBody))
			}))
			defer server.Close()

			c, err := client.NewForTesting(server.URL, "test-token")
			if err != nil {
				t.Fatalf("failed to create client: %v", err)
			}
			h := NewHandler(c)

			result, err := h.ListResourceNotifications(tt.notificationType, tt.resourceID)

			if tt.expectError {
				if err == nil {
					t.Error("expected error, got nil")
				} else if tt.errorContains != "" && !strings.Contains(err.Error(), tt.errorContains) {
					t.Errorf("expected error containing %q, got %q", tt.errorContains, err.Error())
				}
			} else {
				if err != nil {
					t.Errorf("unexpected error: %v", err)
				}
				if tt.validate != nil {
					tt.validate(t, result)
				}
			}
		})
	}
}

func TestGetResourceNotification(t *testing.T) {
	tests := []struct {
		name          string
		id            string
		statusCode    int
		responseBody  string
		expectError   bool
		errorContains string
		validate      func(*testing.T, *ResourceNotification)
	}{
		{
			name:       "successful get",
			id:         "rn-123",
			statusCode: 200,
			responseBody: `{
				"id": "rn-123",
				"notificationType": "resource-changed",
				"resourceId": "resource-456",
				"appId": "app-789"
			}`,
			expectError: false,
			validate: func(t *testing.T, notif *ResourceNotification) {
				if notif.ID != "rn-123" {
					t.Errorf("expected ID 'rn-123', got %q", notif.ID)
				}
				if notif.ResourceID != "resource-456" {
					t.Errorf("expected ResourceID 'resource-456', got %q", notif.ResourceID)
				}
			},
		},
		{
			name:          "notification not found",
			id:            "non-existent",
			statusCode:    404,
			responseBody:  "not found",
			expectError:   true,
			errorContains: "not found",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				expectedPath := "/platform/notification/v2/resource-notifications/" + tt.id
				if r.URL.Path != expectedPath {
					t.Errorf("expected path %q, got %q", expectedPath, r.URL.Path)
				}
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(tt.statusCode)
				w.Write([]byte(tt.responseBody))
			}))
			defer server.Close()

			c, err := client.NewForTesting(server.URL, "test-token")
			if err != nil {
				t.Fatalf("failed to create client: %v", err)
			}
			h := NewHandler(c)

			result, err := h.GetResourceNotification(tt.id)

			if tt.expectError {
				if err == nil {
					t.Error("expected error, got nil")
				} else if tt.errorContains != "" && !strings.Contains(err.Error(), tt.errorContains) {
					t.Errorf("expected error containing %q, got %q", tt.errorContains, err.Error())
				}
			} else {
				if err != nil {
					t.Errorf("unexpected error: %v", err)
				}
				if tt.validate != nil {
					tt.validate(t, result)
				}
			}
		})
	}
}

func TestDeleteResourceNotification(t *testing.T) {
	tests := []struct {
		name          string
		id            string
		statusCode    int
		responseBody  string
		expectError   bool
		errorContains string
	}{
		{
			name:        "successful delete",
			id:          "rn-123",
			statusCode:  204,
			expectError: false,
		},
		{
			name:          "notification not found",
			id:            "non-existent",
			statusCode:    404,
			responseBody:  "not found",
			expectError:   true,
			errorContains: "not found",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != "DELETE" {
					t.Errorf("expected DELETE method, got %s", r.Method)
				}
				expectedPath := "/platform/notification/v2/resource-notifications/" + tt.id
				if r.URL.Path != expectedPath {
					t.Errorf("expected path %q, got %q", expectedPath, r.URL.Path)
				}
				w.WriteHeader(tt.statusCode)
				w.Write([]byte(tt.responseBody))
			}))
			defer server.Close()

			c, err := client.NewForTesting(server.URL, "test-token")
			if err != nil {
				t.Fatalf("failed to create client: %v", err)
			}
			h := NewHandler(c)

			err = h.DeleteResourceNotification(tt.id)

			if tt.expectError {
				if err == nil {
					t.Error("expected error, got nil")
				} else if tt.errorContains != "" && !strings.Contains(err.Error(), tt.errorContains) {
					t.Errorf("expected error containing %q, got %q", tt.errorContains, err.Error())
				}
			} else {
				if err != nil {
					t.Errorf("unexpected error: %v", err)
				}
			}
		})
	}
}
