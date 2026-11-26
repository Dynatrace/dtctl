package edgeconnect

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
		statusCode    int
		responseBody  interface{}
		expectError   bool
		errorContains string
		validate      func(*testing.T, *EdgeConnectList)
	}{
		{
			name:       "successful list",
			statusCode: 200,
			responseBody: EdgeConnectList{
				EdgeConnects: []EdgeConnect{
					{
						ID:           "ec-123",
						Name:         "Production EdgeConnect",
						HostPatterns: []string{"*.example.com"},
					},
					{
						ID:           "ec-456",
						Name:         "Staging EdgeConnect",
						HostPatterns: []string{"*.staging.example.com"},
					},
				},
				TotalCount: 2,
				PageSize:   100,
			},
			expectError: false,
			validate: func(t *testing.T, result *EdgeConnectList) {
				if result.TotalCount != 2 {
					t.Errorf("expected 2 EdgeConnects, got %d", result.TotalCount)
				}
				if len(result.EdgeConnects) != 2 {
					t.Errorf("expected 2 EdgeConnects in list, got %d", len(result.EdgeConnects))
				}
				if result.EdgeConnects[0].ID != "ec-123" {
					t.Errorf("expected first EdgeConnect ID 'ec-123', got %q", result.EdgeConnects[0].ID)
				}
			},
		},
		{
			name:       "empty list",
			statusCode: 200,
			responseBody: EdgeConnectList{
				EdgeConnects: []EdgeConnect{},
				TotalCount:   0,
				PageSize:     100,
			},
			expectError: false,
			validate: func(t *testing.T, result *EdgeConnectList) {
				if result.TotalCount != 0 {
					t.Errorf("expected 0 EdgeConnects, got %d", result.TotalCount)
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
				if r.URL.Path != "/platform/app-engine/edge-connect/v1/edge-connects" {
					t.Errorf("expected path '/platform/app-engine/edge-connect/v1/edge-connects', got %q", r.URL.Path)
				}
				if r.URL.Query().Get("add-fields") != "modificationInfo,metadata" {
					t.Errorf("expected add-fields query param")
				}
				w.WriteHeader(tt.statusCode)
				if str, ok := tt.responseBody.(string); ok {
					w.Write([]byte(str))
				} else {
					json.NewEncoder(w).Encode(tt.responseBody)
				}
			}))
			defer server.Close()

			c, err := client.NewForTesting(server.URL, "test-token")
			if err != nil {
				t.Fatalf("failed to create client: %v", err)
			}
			h := NewHandler(c)

			result, err := h.List()

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

func TestGet(t *testing.T) {
	tests := []struct {
		name          string
		edgeConnectID string
		statusCode    int
		responseBody  interface{}
		expectError   bool
		errorContains string
		validate      func(*testing.T, *EdgeConnect)
	}{
		{
			name:          "successful get",
			edgeConnectID: "ec-123",
			statusCode:    200,
			responseBody: EdgeConnect{
				ID:           "ec-123",
				Name:         "Production EdgeConnect",
				HostPatterns: []string{"*.example.com"},
			},
			expectError: false,
			validate: func(t *testing.T, ec *EdgeConnect) {
				if ec.ID != "ec-123" {
					t.Errorf("expected ID 'ec-123', got %q", ec.ID)
				}
				if ec.Name != "Production EdgeConnect" {
					t.Errorf("expected name 'Production EdgeConnect', got %q", ec.Name)
				}
			},
		},
		{
			name:          "EdgeConnect not found",
			edgeConnectID: "non-existent",
			statusCode:    404,
			responseBody:  "not found",
			expectError:   true,
			errorContains: "not found",
		},
		{
			name:          "server error",
			edgeConnectID: "ec-123",
			statusCode:    500,
			responseBody:  "internal error",
			expectError:   true,
			errorContains: "API error (500)",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				expectedPath := "/platform/app-engine/edge-connect/v1/edge-connects/" + tt.edgeConnectID
				if r.URL.Path != expectedPath {
					t.Errorf("expected path %q, got %q", expectedPath, r.URL.Path)
				}
				w.WriteHeader(tt.statusCode)
				if str, ok := tt.responseBody.(string); ok {
					w.Write([]byte(str))
				} else {
					json.NewEncoder(w).Encode(tt.responseBody)
				}
			}))
			defer server.Close()

			c, err := client.NewForTesting(server.URL, "test-token")
			if err != nil {
				t.Fatalf("failed to create client: %v", err)
			}
			h := NewHandler(c)

			result, err := h.Get(tt.edgeConnectID)

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

func TestCreate(t *testing.T) {
	tests := []struct {
		name          string
		request       EdgeConnectCreate
		statusCode    int
		responseBody  interface{}
		expectError   bool
		errorContains string
		validate      func(*testing.T, *EdgeConnect)
	}{
		{
			name: "successful create",
			request: EdgeConnectCreate{
				Name:         "New EdgeConnect",
				HostPatterns: []string{"*.newhost.com"},
			},
			statusCode: 201,
			responseBody: EdgeConnect{
				ID:           "ec-new",
				Name:         "New EdgeConnect",
				HostPatterns: []string{"*.newhost.com"},
			},
			expectError: false,
			validate: func(t *testing.T, ec *EdgeConnect) {
				if ec.ID != "ec-new" {
					t.Errorf("expected ID 'ec-new', got %q", ec.ID)
				}
			},
		},
		{
			name: "invalid configuration",
			request: EdgeConnectCreate{
				Name: "",
			},
			statusCode:    400,
			responseBody:  "invalid configuration",
			expectError:   true,
			errorContains: "invalid configuration",
		},
		{
			name: "access denied",
			request: EdgeConnectCreate{
				Name: "EdgeConnect",
			},
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
				if r.URL.Path != "/platform/app-engine/edge-connect/v1/edge-connects" {
					t.Errorf("expected path '/platform/app-engine/edge-connect/v1/edge-connects', got %q", r.URL.Path)
				}
				w.WriteHeader(tt.statusCode)
				if str, ok := tt.responseBody.(string); ok {
					w.Write([]byte(str))
				} else {
					json.NewEncoder(w).Encode(tt.responseBody)
				}
			}))
			defer server.Close()

			c, err := client.NewForTesting(server.URL, "test-token")
			if err != nil {
				t.Fatalf("failed to create client: %v", err)
			}
			h := NewHandler(c)

			result, err := h.Create(tt.request)

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

func TestUpdate(t *testing.T) {
	tests := []struct {
		name          string
		edgeConnectID string
		request       EdgeConnect
		statusCode    int
		responseBody  string
		expectError   bool
		errorContains string
	}{
		{
			name:          "successful update",
			edgeConnectID: "ec-123",
			request: EdgeConnect{
				Name:         "Updated EdgeConnect",
				HostPatterns: []string{"*.updated.com"},
			},
			statusCode:  200,
			expectError: false,
		},
		{
			name:          "EdgeConnect not found",
			edgeConnectID: "non-existent",
			request: EdgeConnect{
				Name: "EdgeConnect",
			},
			statusCode:    404,
			responseBody:  "not found",
			expectError:   true,
			errorContains: "not found",
		},
		{
			name:          "invalid configuration",
			edgeConnectID: "ec-123",
			request: EdgeConnect{
				Name: "",
			},
			statusCode:    400,
			responseBody:  "invalid",
			expectError:   true,
			errorContains: "API error (400)",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != "PUT" {
					t.Errorf("expected PUT method, got %s", r.Method)
				}
				expectedPath := "/platform/app-engine/edge-connect/v1/edge-connects/" + tt.edgeConnectID
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

			err = h.Update(tt.edgeConnectID, tt.request)

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

func TestDelete(t *testing.T) {
	tests := []struct {
		name          string
		edgeConnectID string
		statusCode    int
		responseBody  string
		expectError   bool
		errorContains string
	}{
		{
			name:          "successful delete",
			edgeConnectID: "ec-123",
			statusCode:    204,
			expectError:   false,
		},
		{
			name:          "EdgeConnect not found",
			edgeConnectID: "non-existent",
			statusCode:    404,
			responseBody:  "not found",
			expectError:   true,
			errorContains: "not found",
		},
		{
			name:          "access denied",
			edgeConnectID: "ec-protected",
			statusCode:    403,
			responseBody:  "access denied",
			expectError:   true,
			errorContains: "access denied",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != "DELETE" {
					t.Errorf("expected DELETE method, got %s", r.Method)
				}
				expectedPath := "/platform/app-engine/edge-connect/v1/edge-connects/" + tt.edgeConnectID
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

			err = h.Delete(tt.edgeConnectID)

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

func TestGetRaw(t *testing.T) {
	t.Run("successful get raw", func(t *testing.T) {
		expectedEC := EdgeConnect{
			ID:           "ec-123",
			Name:         "Test EdgeConnect",
			HostPatterns: []string{"*.example.com"},
		}

		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(200)
			json.NewEncoder(w).Encode(expectedEC)
		}))
		defer server.Close()

		c, err := client.NewForTesting(server.URL, "test-token")
		if err != nil {
			t.Fatalf("failed to create client: %v", err)
		}
		h := NewHandler(c)

		raw, err := h.GetRaw("ec-123")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		// Verify it's valid JSON
		var ec EdgeConnect
		if err := json.Unmarshal(raw, &ec); err != nil {
			t.Fatalf("failed to unmarshal raw JSON: %v", err)
		}

		if ec.ID != expectedEC.ID {
			t.Errorf("expected ID %q, got %q", expectedEC.ID, ec.ID)
		}
	})

	t.Run("get raw with non-existent EdgeConnect", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(404)
			w.Write([]byte("not found"))
		}))
		defer server.Close()

		c, err := client.NewForTesting(server.URL, "test-token")
		if err != nil {
			t.Fatalf("failed to create client: %v", err)
		}
		h := NewHandler(c)

		_, err = h.GetRaw("non-existent")
		if err == nil {
			t.Error("expected error, got nil")
		}
	})
}
