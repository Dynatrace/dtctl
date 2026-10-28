package segment

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Dynatrace/dtctl/pkg/client"
)

// astFilterFor converts a DQL expression to its AST form for use in mock API
// responses. Test helpers call this to simulate real API behaviour where the
// server always stores and returns JSON AST, never plain DQL.
func astFilterFor(t *testing.T, dql string) string {
	t.Helper()
	ast, err := FilterToAST(dql)
	if err != nil {
		t.Fatalf("astFilterFor(%q): %v", dql, err)
	}
	return ast
}

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
		buildResponse func(t *testing.T) interface{} // builds response body; nil uses responseBody field
		responseBody  interface{}
		expectError   bool
		errorContains string
		validate      func(*testing.T, *FilterSegmentList)
	}{
		{
			name:       "successful list",
			statusCode: 200,
			responseBody: FilterSegmentList{
				FilterSegments: []FilterSegment{
					{
						UID:      "seg-uid-001",
						Name:     "k8s-alpha",
						IsPublic: true,
						Owner:    "user@example.invalid",
					},
					{
						UID:      "seg-uid-002",
						Name:     "prod-logs",
						IsPublic: false,
						Owner:    "admin@example.invalid",
					},
				},
				TotalCount: 2,
			},
			expectError: false,
			validate: func(t *testing.T, result *FilterSegmentList) {
				if len(result.FilterSegments) != 2 {
					t.Errorf("expected 2 segments, got %d", len(result.FilterSegments))
				}
				if result.FilterSegments[0].UID != "seg-uid-001" {
					t.Errorf("expected first segment UID 'seg-uid-001', got %q", result.FilterSegments[0].UID)
				}
				if result.FilterSegments[1].Name != "prod-logs" {
					t.Errorf("expected second segment name 'prod-logs', got %q", result.FilterSegments[1].Name)
				}
			},
		},
		{
			name:       "list with AST filters converts to DQL",
			statusCode: 200,
			buildResponse: func(t *testing.T) interface{} {
				return FilterSegmentList{
					FilterSegments: []FilterSegment{
						{
							UID:      "seg-uid-003",
							Name:     "with-includes",
							IsPublic: true,
							Includes: []Include{
								{DataObject: "logs", Filter: astFilterFor(t, `status = "ERROR"`)},
								{DataObject: "spans", Filter: astFilterFor(t, `span.kind = "SERVER" OR span.kind = "CLIENT"`)},
							},
						},
					},
					TotalCount: 1,
				}
			},
			expectError: false,
			validate: func(t *testing.T, result *FilterSegmentList) {
				if len(result.FilterSegments) != 1 {
					t.Fatalf("expected 1 segment, got %d", len(result.FilterSegments))
				}
				seg := result.FilterSegments[0]
				if len(seg.Includes) != 2 {
					t.Fatalf("expected 2 includes, got %d", len(seg.Includes))
				}
				// Verify AST→DQL conversion happened
				if seg.Includes[0].Filter != `status = "ERROR"` {
					t.Errorf("expected DQL filter for include[0], got %q", seg.Includes[0].Filter)
				}
				if seg.Includes[1].Filter != `span.kind = "SERVER" OR span.kind = "CLIENT"` {
					t.Errorf("expected DQL filter for include[1], got %q", seg.Includes[1].Filter)
				}
			},
		},
		{
			name:       "empty list",
			statusCode: 200,
			responseBody: FilterSegmentList{
				FilterSegments: []FilterSegment{},
				TotalCount:     0,
			},
			expectError: false,
			validate: func(t *testing.T, result *FilterSegmentList) {
				if len(result.FilterSegments) != 0 {
					t.Errorf("expected 0 segments, got %d", len(result.FilterSegments))
				}
			},
		},
		{
			name:          "server error",
			statusCode:    500,
			responseBody:  "internal server error",
			expectError:   true,
			errorContains: "500",
		},
		{
			name:          "forbidden",
			statusCode:    403,
			responseBody:  "access denied",
			expectError:   true,
			errorContains: "403",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/platform/storage/filter-segments/v1/filter-segments" {
					t.Errorf("expected path '/platform/storage/filter-segments/v1/filter-segments', got %q", r.URL.Path)
				}
				// The list endpoint does not support pagination params
				if r.URL.Query().Get("page-size") != "" {
					t.Error("list endpoint should not send page-size (API has no pagination)")
				}
				responseBody := tt.responseBody
				if tt.buildResponse != nil {
					responseBody = tt.buildResponse(t)
				}
				w.WriteHeader(tt.statusCode)
				if str, ok := responseBody.(string); ok {
					w.Write([]byte(str))
				} else {
					json.NewEncoder(w).Encode(responseBody)
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
		uid           string
		statusCode    int
		buildResponse func(t *testing.T) interface{} // builds response body; nil uses responseBody field
		responseBody  interface{}
		expectError   bool
		errorContains string
		validate      func(*testing.T, *FilterSegment)
	}{
		{
			name:       "successful get",
			uid:        "seg-uid-001",
			statusCode: 200,
			buildResponse: func(t *testing.T) interface{} {
				return FilterSegment{
					UID:         "seg-uid-001",
					Name:        "k8s-alpha",
					Description: "Kubernetes cluster alpha",
					IsPublic:    true,
					Owner:       "user@example.invalid",
					Version:     3,
					Includes: []Include{
						{DataObject: "_all_data_object", Filter: astFilterFor(t, `k8s.cluster.name = "alpha"`)},
						{DataObject: "logs", Filter: astFilterFor(t, `dt.system.bucket = "custom-logs"`)},
					},
					Variables: &Variables{
						Type:  "query",
						Value: `fetch logs | limit 1`,
					},
					AllowedOperations: []string{"READ", "WRITE", "DELETE"},
				}
			},
			expectError: false,
			validate: func(t *testing.T, seg *FilterSegment) {
				if seg.UID != "seg-uid-001" {
					t.Errorf("expected UID 'seg-uid-001', got %q", seg.UID)
				}
				if seg.Name != "k8s-alpha" {
					t.Errorf("expected name 'k8s-alpha', got %q", seg.Name)
				}
				if len(seg.Includes) != 2 {
					t.Errorf("expected 2 includes, got %d", len(seg.Includes))
				}
				// Verify AST→DQL conversion happened: filters should be DQL, not AST
				if seg.Includes[0].DataObject != "_all_data_object" {
					t.Errorf("expected first include dataObject '_all_data_object', got %q", seg.Includes[0].DataObject)
				}
				if seg.Includes[0].Filter != `k8s.cluster.name = "alpha"` {
					t.Errorf("expected first filter as DQL, got %q", seg.Includes[0].Filter)
				}
				if seg.Includes[1].Filter != `dt.system.bucket = "custom-logs"` {
					t.Errorf("expected second filter as DQL, got %q", seg.Includes[1].Filter)
				}
				if seg.Variables == nil {
					t.Error("expected variables to be non-nil")
				} else {
					if seg.Variables.Type != "query" {
						t.Errorf("expected variables type 'query', got %q", seg.Variables.Type)
					}
					if seg.Variables.Value != "fetch logs | limit 1" {
						t.Errorf("expected variables value 'fetch logs | limit 1', got %q", seg.Variables.Value)
					}
				}
			},
		},
		{
			name:       "successful get with add-fields params",
			uid:        "seg-uid-002",
			statusCode: 200,
			responseBody: FilterSegment{
				UID:      "seg-uid-002",
				Name:     "test",
				IsPublic: false,
				Version:  1,
			},
			expectError: false,
			validate: func(t *testing.T, seg *FilterSegment) {
				// Just validates that the request succeeds
				if seg.UID != "seg-uid-002" {
					t.Errorf("expected UID 'seg-uid-002', got %q", seg.UID)
				}
			},
		},
		{
			name:          "segment not found",
			uid:           "non-existent",
			statusCode:    404,
			responseBody:  "not found",
			expectError:   true,
			errorContains: "not found",
		},
		{
			name:          "server error",
			uid:           "seg-uid-001",
			statusCode:    500,
			responseBody:  "internal error",
			expectError:   true,
			errorContains: "500",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Build response body: prefer buildResponse function, fall back to static field
			responseBody := tt.responseBody
			if tt.buildResponse != nil {
				responseBody = tt.buildResponse(t)
			}

			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				expectedPath := fmt.Sprintf("/platform/storage/filter-segments/v1/filter-segments/%s", tt.uid)
				if r.URL.Path != expectedPath {
					t.Errorf("expected path %q, got %q", expectedPath, r.URL.Path)
				}
				// Verify add-fields params are valid enum values
				addFields := r.URL.Query()["add-fields"]
				for _, f := range addFields {
					switch f {
					case "INCLUDES", "VARIABLES", "EXTERNALID", "RESOURCECONTEXT":
						// valid
					default:
						t.Errorf("invalid add-fields value: %q", f)
					}
				}
				w.WriteHeader(tt.statusCode)
				if str, ok := responseBody.(string); ok {
					w.Write([]byte(str))
				} else {
					json.NewEncoder(w).Encode(responseBody)
				}
			}))
			defer server.Close()

			c, err := client.NewForTesting(server.URL, "test-token")
			if err != nil {
				t.Fatalf("failed to create client: %v", err)
			}
			h := NewHandler(c)

			result, err := h.Get(tt.uid)

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
		input         FilterSegment
		statusCode    int
		responseBody  interface{}
		expectError   bool
		errorContains string
		validate      func(*testing.T, *FilterSegment)
	}{
		{
			name: "successful create",
			input: FilterSegment{
				Name:     "new-segment",
				IsPublic: true,
				Includes: []Include{{DataObject: "logs", Filter: `status = "ERROR"`}},
			},
			statusCode: 201,
			responseBody: FilterSegment{
				UID:      "seg-new-001",
				Name:     "new-segment",
				IsPublic: true,
				Owner:    "user@example.invalid",
				Version:  1,
				Includes: []Include{{DataObject: "logs", Filter: `status = "ERROR"`}},
			},
			expectError: false,
			validate: func(t *testing.T, seg *FilterSegment) {
				if seg.UID != "seg-new-001" {
					t.Errorf("expected UID 'seg-new-001', got %q", seg.UID)
				}
				if seg.Name != "new-segment" {
					t.Errorf("expected name 'new-segment', got %q", seg.Name)
				}
			},
		},
		{
			name: "invalid definition",
			input: FilterSegment{
				Name: "",
			},
			statusCode:    400,
			responseBody:  "invalid segment definition",
			expectError:   true,
			errorContains: "400",
		},
		{
			name: "access denied",
			input: FilterSegment{
				Name: "denied-segment",
			},
			statusCode:    403,
			responseBody:  "access denied",
			expectError:   true,
			errorContains: "403",
		},
		{
			name: "conflict - segment already exists",
			input: FilterSegment{
				Name: "duplicate-segment",
			},
			statusCode:    409,
			responseBody:  `{"error":"segment already exists"}`,
			expectError:   true,
			errorContains: "409",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != "POST" {
					t.Errorf("expected POST method, got %s", r.Method)
				}
				if r.URL.Path != "/platform/storage/filter-segments/v1/filter-segments" {
					t.Errorf("expected path '/platform/storage/filter-segments/v1/filter-segments', got %q", r.URL.Path)
				}

				// Validate that DQL filters were converted to AST before sending
				body, _ := io.ReadAll(r.Body)
				var reqBody struct {
					Includes []struct {
						Filter string `json:"filter"`
					} `json:"includes"`
				}
				if err := json.Unmarshal(body, &reqBody); err == nil {
					for i, inc := range reqBody.Includes {
						if inc.Filter != "" && !isFilterAST(inc.Filter) {
							t.Errorf("include[%d] filter should be AST in API request, got DQL: %s", i, inc.Filter)
							w.WriteHeader(http.StatusBadRequest)
							w.Write([]byte(`{"error":"filter must be AST, got DQL"}`))
							return
						}
					}
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

			inputJSON, _ := json.Marshal(tt.input)
			result, err := h.Create(inputJSON)

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
		uid           string
		version       int
		statusCode    int
		responseBody  string
		expectError   bool
		errorContains string
	}{
		{
			name:        "successful update",
			uid:         "seg-uid-001",
			version:     3,
			statusCode:  200,
			expectError: false,
		},
		{
			name:          "segment not found",
			uid:           "non-existent",
			version:       1,
			statusCode:    404,
			responseBody:  "not found",
			expectError:   true,
			errorContains: "not found",
		},
		{
			name:          "version conflict",
			uid:           "seg-uid-001",
			version:       2,
			statusCode:    409,
			responseBody:  "version conflict",
			expectError:   true,
			errorContains: "409",
		},
		{
			name:          "invalid definition",
			uid:           "seg-uid-001",
			version:       1,
			statusCode:    400,
			responseBody:  "invalid",
			expectError:   true,
			errorContains: "400",
		},
		{
			name:          "access denied",
			uid:           "seg-uid-001",
			version:       1,
			statusCode:    403,
			responseBody:  "access denied",
			expectError:   true,
			errorContains: "403",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != "PATCH" {
					t.Errorf("expected PATCH method, got %s", r.Method)
				}
				expectedPath := fmt.Sprintf("/platform/storage/filter-segments/v1/filter-segments/%s", tt.uid)
				if r.URL.Path != expectedPath {
					t.Errorf("expected path %q, got %q", expectedPath, r.URL.Path)
				}
				// Verify optimistic-locking-version is sent
				lockVer := r.URL.Query().Get("optimistic-locking-version")
				if lockVer == "" {
					t.Error("expected optimistic-locking-version query param")
				}
				expectedVer := fmt.Sprintf("%d", tt.version)
				if lockVer != expectedVer {
					t.Errorf("expected optimistic-locking-version %q, got %q", expectedVer, lockVer)
				}

				// Validate that DQL filters were converted to AST before sending
				body, _ := io.ReadAll(r.Body)
				var reqBody struct {
					Includes []struct {
						Filter string `json:"filter"`
					} `json:"includes"`
				}
				if err := json.Unmarshal(body, &reqBody); err == nil {
					for i, inc := range reqBody.Includes {
						if inc.Filter != "" && !isFilterAST(inc.Filter) {
							t.Errorf("include[%d] filter should be AST in API request, got DQL: %s", i, inc.Filter)
							w.WriteHeader(http.StatusBadRequest)
							w.Write([]byte(`{"error":"filter must be AST, got DQL"}`))
							return
						}
					}
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

			updateData := []byte(`{"name":"updated-segment","isPublic":true,"includes":[{"dataObject":"logs","filter":"status = \"ERROR\""}]}`)
			err = h.Update(tt.uid, tt.version, updateData)

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
		uid           string
		statusCode    int
		responseBody  string
		expectError   bool
		errorContains string
	}{
		{
			name:        "successful delete",
			uid:         "seg-uid-001",
			statusCode:  204,
			expectError: false,
		},
		{
			name:          "segment not found",
			uid:           "non-existent",
			statusCode:    404,
			responseBody:  "not found",
			expectError:   true,
			errorContains: "not found",
		},
		{
			name:          "access denied",
			uid:           "seg-uid-001",
			statusCode:    403,
			responseBody:  "access denied",
			expectError:   true,
			errorContains: "403",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != "DELETE" {
					t.Errorf("expected DELETE method, got %s", r.Method)
				}
				expectedPath := fmt.Sprintf("/platform/storage/filter-segments/v1/filter-segments/%s", tt.uid)
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

			err = h.Delete(tt.uid)

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
		// Server returns AST filters (simulating real API behaviour)
		expectedSegment := FilterSegment{
			UID:      "seg-uid-001",
			Name:     "test-segment",
			IsPublic: true,
			Owner:    "user@example.invalid",
			Version:  1,
			Includes: []Include{
				{DataObject: "logs", Filter: astFilterFor(t, `status = "ERROR"`)},
			},
		}

		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(200)
			json.NewEncoder(w).Encode(expectedSegment)
		}))
		defer server.Close()

		c, err := client.NewForTesting(server.URL, "test-token")
		if err != nil {
			t.Fatalf("failed to create client: %v", err)
		}
		h := NewHandler(c)

		raw, err := h.GetRaw("seg-uid-001")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		// Verify it's valid JSON
		var seg FilterSegment
		if err := json.Unmarshal(raw, &seg); err != nil {
			t.Fatalf("failed to unmarshal raw JSON: %v", err)
		}

		if seg.UID != expectedSegment.UID {
			t.Errorf("expected UID %q, got %q", expectedSegment.UID, seg.UID)
		}
		if seg.Name != expectedSegment.Name {
			t.Errorf("expected name %q, got %q", expectedSegment.Name, seg.Name)
		}
		// Verify AST→DQL conversion happened in GetRaw output
		if len(seg.Includes) != 1 {
			t.Fatalf("expected 1 include, got %d", len(seg.Includes))
		}
		if seg.Includes[0].Filter != `status = "ERROR"` {
			t.Errorf("expected DQL filter in GetRaw output, got %q", seg.Includes[0].Filter)
		}
	})

	t.Run("get raw with non-existent segment", func(t *testing.T) {
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

func TestIsNotFound(t *testing.T) {
	tests := []struct {
		name     string
		err      error
		expected bool
	}{
		{
			name:     "nil error",
			err:      nil,
			expected: false,
		},
		{
			name:     "direct ErrNotFound",
			err:      ErrNotFound,
			expected: true,
		},
		{
			name:     "wrapped ErrNotFound from Get",
			err:      fmt.Errorf("segment %q: %w", "seg-uid-001", ErrNotFound),
			expected: true,
		},
		{
			name:     "double-wrapped ErrNotFound",
			err:      fmt.Errorf("failed: %w", fmt.Errorf("segment %q: %w", "x", ErrNotFound)),
			expected: true,
		},
		{
			name:     "generic error",
			err:      fmt.Errorf("failed to get segment: status 500: internal error"),
			expected: false,
		},
		{
			name:     "access denied error",
			err:      fmt.Errorf("access denied to get segment"),
			expected: false,
		},
		{
			name:     "different sentinel error",
			err:      errors.New("something else not found"),
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := IsNotFound(tt.err)
			if got != tt.expected {
				t.Errorf("IsNotFound(%v) = %v, want %v", tt.err, got, tt.expected)
			}
		})
	}
}

func TestConvertIncludesForAPI_PreservesFieldOrder(t *testing.T) {
	// Input has fields in a specific order: name, includes, isPublic, description.
	// convertIncludesForAPI must preserve this order, not alphabetize it.
	input := []byte(`{"name":"test","includes":[{"dataObject":"logs","filter":"status = \"ERROR\""}],"isPublic":true,"description":"keep order"}`)

	result, err := convertIncludesForAPI(input)
	if err != nil {
		t.Fatalf("convertIncludesForAPI() error: %v", err)
	}

	// The result must still have "name" before "includes" before "isPublic" before "description".
	resultStr := string(result)
	nameIdx := strings.Index(resultStr, `"name"`)
	includesIdx := strings.Index(resultStr, `"includes"`)
	isPublicIdx := strings.Index(resultStr, `"isPublic"`)
	descIdx := strings.Index(resultStr, `"description"`)

	if nameIdx < 0 || includesIdx < 0 || isPublicIdx < 0 || descIdx < 0 {
		t.Fatalf("missing expected fields in result: %s", resultStr)
	}
	if nameIdx >= includesIdx {
		t.Errorf("field order not preserved: 'name' (%d) should come before 'includes' (%d) in: %s", nameIdx, includesIdx, resultStr)
	}
	if includesIdx >= isPublicIdx {
		t.Errorf("field order not preserved: 'includes' (%d) should come before 'isPublic' (%d) in: %s", includesIdx, isPublicIdx, resultStr)
	}
	if isPublicIdx >= descIdx {
		t.Errorf("field order not preserved: 'isPublic' (%d) should come before 'description' (%d) in: %s", isPublicIdx, descIdx, resultStr)
	}

	// Also verify the filter was actually converted to AST
	var payload struct {
		Includes []struct {
			Filter string `json:"filter"`
		} `json:"includes"`
	}
	if err := json.Unmarshal(result, &payload); err != nil {
		t.Fatalf("failed to parse result: %v", err)
	}
	if len(payload.Includes) != 1 {
		t.Fatalf("expected 1 include, got %d", len(payload.Includes))
	}
	if !isFilterAST(payload.Includes[0].Filter) {
		t.Errorf("expected AST filter, got: %s", payload.Includes[0].Filter)
	}
}

func TestCreate_InvalidDQL_PropagatesError(t *testing.T) {
	// The server should never be called — the error should happen client-side
	// during DQL→AST conversion before the HTTP request is made.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("server should not be called when DQL conversion fails")
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()

	c, err := client.NewForTesting(server.URL, "test-token")
	if err != nil {
		t.Fatalf("failed to create client: %v", err)
	}
	h := NewHandler(c)

	// Use "==" which is explicitly rejected by the parser with a helpful message
	input := []byte(`{"name":"test","includes":[{"dataObject":"logs","filter":"status == \"ERROR\""}]}`)
	_, err = h.Create(input)
	if err == nil {
		t.Fatal("expected error for invalid DQL filter, got nil")
	}
	if !strings.Contains(err.Error(), "==") {
		t.Errorf("expected error message to mention '==', got: %v", err)
	}
	if !strings.Contains(err.Error(), "include[0]") {
		t.Errorf("expected error to identify include index, got: %v", err)
	}
}

func TestUpdate_InvalidDQL_PropagatesError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("server should not be called when DQL conversion fails")
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()

	c, err := client.NewForTesting(server.URL, "test-token")
	if err != nil {
		t.Fatalf("failed to create client: %v", err)
	}
	h := NewHandler(c)

	input := []byte(`{"name":"test","includes":[{"dataObject":"logs","filter":"status == \"ERROR\""}]}`)
	err = h.Update("seg-uid-001", 1, input)
	if err == nil {
		t.Fatal("expected error for invalid DQL filter, got nil")
	}
	if !strings.Contains(err.Error(), "==") {
		t.Errorf("expected error message to mention '==', got: %v", err)
	}
}
