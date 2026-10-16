package analyzer

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/dynatrace-oss/dtctl/pkg/client"
	"github.com/dynatrace-oss/dtctl/pkg/vfs"
)

func TestHandler_List(t *testing.T) {
	tests := []struct {
		name       string
		filter     string
		statusCode int
		response   AnalyzerList
		wantErr    bool
		wantCount  int
	}{
		{
			name:       "list all analyzers",
			filter:     "",
			statusCode: http.StatusOK,
			response: AnalyzerList{
				TotalCount: 2,
				Analyzers: []Analyzer{
					{Name: "analyzer1", DisplayName: "Analyzer 1"},
					{Name: "analyzer2", DisplayName: "Analyzer 2"},
				},
			},
			wantErr:   false,
			wantCount: 2,
		},
		{
			name:       "list with filter",
			filter:     "name:test",
			statusCode: http.StatusOK,
			response: AnalyzerList{
				TotalCount: 1,
				Analyzers: []Analyzer{
					{Name: "test-analyzer", DisplayName: "Test Analyzer"},
				},
			},
			wantErr:   false,
			wantCount: 1,
		},
		{
			name:       "list with category populated",
			filter:     "",
			statusCode: http.StatusOK,
			response: AnalyzerList{
				TotalCount: 1,
				Analyzers: []Analyzer{
					{
						Name:        "analyzer1",
						DisplayName: "Analyzer 1",
						Category:    &AnalyzerCategory{DisplayName: "TestCategory"},
					},
				},
			},
			wantErr:   false,
			wantCount: 1,
		},
		{
			name:       "server error",
			filter:     "",
			statusCode: http.StatusInternalServerError,
			response:   AnalyzerList{},
			wantErr:    true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/platform/davis/analyzers/v1/analyzers" {
					t.Errorf("unexpected path: %s", r.URL.Path)
				}

				// Verify filter query parameter if expected
				gotFilter := r.URL.Query().Get("filter")
				if gotFilter != tt.filter {
					t.Errorf("filter query param = %q, want %q", gotFilter, tt.filter)
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
			list, err := handler.List(tt.filter)

			if (err != nil) != tt.wantErr {
				t.Errorf("List() error = %v, wantErr %v", err, tt.wantErr)
				return
			}

			if !tt.wantErr {
				if list == nil {
					t.Fatal("List() returned nil")
				}
				if len(list.Analyzers) != tt.wantCount {
					t.Errorf("List() returned %d analyzers, want %d", len(list.Analyzers), tt.wantCount)
				}
				// Verify CategoryName is populated
				if tt.name == "list with category populated" && list.Analyzers[0].CategoryName != "TestCategory" {
					t.Errorf("CategoryName = %q, want %q", list.Analyzers[0].CategoryName, "TestCategory")
				}
			}
		})
	}
}

func TestHandler_Get(t *testing.T) {
	tests := []struct {
		name         string
		analyzerName string
		statusCode   int
		response     AnalyzerDefinition
		wantErr      bool
		errMsg       string
	}{
		{
			name:         "get existing analyzer",
			analyzerName: "test-analyzer",
			statusCode:   http.StatusOK,
			response: AnalyzerDefinition{
				Name:        "test-analyzer",
				DisplayName: "Test Analyzer",
				Description: "A test analyzer",
			},
			wantErr: false,
		},
		{
			name:         "get analyzer with category",
			analyzerName: "cat-analyzer",
			statusCode:   http.StatusOK,
			response: AnalyzerDefinition{
				Name:        "cat-analyzer",
				DisplayName: "Category Analyzer",
				Category:    &AnalyzerCategory{DisplayName: "TestCat"},
			},
			wantErr: false,
		},
		{
			name:         "analyzer not found",
			analyzerName: "nonexistent",
			statusCode:   http.StatusNotFound,
			wantErr:      true,
			errMsg:       "analyzer \"nonexistent\" not found",
		},
		{
			name:         "server error",
			analyzerName: "error",
			statusCode:   http.StatusInternalServerError,
			wantErr:      true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				expectedPath := "/platform/davis/analyzers/v1/analyzers/" + tt.analyzerName
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
			result, err := handler.Get(tt.analyzerName)

			if (err != nil) != tt.wantErr {
				t.Errorf("Get() error = %v, wantErr %v", err, tt.wantErr)
				return
			}

			if tt.wantErr && tt.errMsg != "" {
				if err.Error() != tt.errMsg {
					t.Errorf("Get() error = %q, want %q", err.Error(), tt.errMsg)
				}
			}

			if !tt.wantErr {
				if result == nil {
					t.Fatal("Get() returned nil")
				}
				if result.Name != tt.response.Name {
					t.Errorf("Get() Name = %v, want %v", result.Name, tt.response.Name)
				}
				// Verify CategoryName is populated
				if tt.name == "get analyzer with category" && result.CategoryName != "TestCat" {
					t.Errorf("CategoryName = %q, want %q", result.CategoryName, "TestCat")
				}
			}
		})
	}
}

func TestHandler_GetDocumentation(t *testing.T) {
	tests := []struct {
		name         string
		analyzerName string
		statusCode   int
		response     string
		wantErr      bool
		errMsg       string
	}{
		{
			name:         "get documentation",
			analyzerName: "test-analyzer",
			statusCode:   http.StatusOK,
			response:     "# Test Analyzer\n\nThis is test documentation",
			wantErr:      false,
		},
		{
			name:         "documentation not found",
			analyzerName: "nonexistent",
			statusCode:   http.StatusNotFound,
			wantErr:      true,
			errMsg:       "documentation for analyzer \"nonexistent\" not found",
		},
		{
			name:         "server error",
			analyzerName: "error",
			statusCode:   http.StatusInternalServerError,
			wantErr:      true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				expectedPath := "/platform/davis/analyzers/v1/analyzers/" + tt.analyzerName + "/documentation"
				if r.URL.Path != expectedPath {
					t.Errorf("unexpected path: got %s, want %s", r.URL.Path, expectedPath)
				}

				// Verify Accept header
				if r.Header.Get("Accept") != "text/markdown" {
					t.Errorf("Accept header = %q, want %q", r.Header.Get("Accept"), "text/markdown")
				}

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
			doc, err := handler.GetDocumentation(tt.analyzerName)

			if (err != nil) != tt.wantErr {
				t.Errorf("GetDocumentation() error = %v, wantErr %v", err, tt.wantErr)
				return
			}

			if tt.wantErr && tt.errMsg != "" {
				if err.Error() != tt.errMsg {
					t.Errorf("GetDocumentation() error = %q, want %q", err.Error(), tt.errMsg)
				}
			}

			if !tt.wantErr && doc != tt.response {
				t.Errorf("GetDocumentation() = %q, want %q", doc, tt.response)
			}
		})
	}
}

func TestHandler_GetInputSchema(t *testing.T) {
	tests := []struct {
		name         string
		analyzerName string
		statusCode   int
		response     map[string]interface{}
		wantErr      bool
	}{
		{
			name:         "get input schema",
			analyzerName: "test-analyzer",
			statusCode:   http.StatusOK,
			response: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"field1": map[string]interface{}{"type": "string"},
				},
			},
			wantErr: false,
		},
		{
			name:         "server error",
			analyzerName: "error",
			statusCode:   http.StatusInternalServerError,
			wantErr:      true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				expectedPath := "/platform/davis/analyzers/v1/analyzers/" + tt.analyzerName + "/json-schema/input"
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
			schema, err := handler.GetInputSchema(tt.analyzerName)

			if (err != nil) != tt.wantErr {
				t.Errorf("GetInputSchema() error = %v, wantErr %v", err, tt.wantErr)
				return
			}

			if !tt.wantErr && schema == nil {
				t.Fatal("GetInputSchema() returned nil")
			}
		})
	}
}

func TestHandler_GetResultSchema(t *testing.T) {
	tests := []struct {
		name         string
		analyzerName string
		statusCode   int
		response     map[string]interface{}
		wantErr      bool
	}{
		{
			name:         "get result schema",
			analyzerName: "test-analyzer",
			statusCode:   http.StatusOK,
			response: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"output": map[string]interface{}{"type": "array"},
				},
			},
			wantErr: false,
		},
		{
			name:         "server error",
			analyzerName: "error",
			statusCode:   http.StatusInternalServerError,
			wantErr:      true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				expectedPath := "/platform/davis/analyzers/v1/analyzers/" + tt.analyzerName + "/json-schema/result"
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
			schema, err := handler.GetResultSchema(tt.analyzerName)

			if (err != nil) != tt.wantErr {
				t.Errorf("GetResultSchema() error = %v, wantErr %v", err, tt.wantErr)
				return
			}

			if !tt.wantErr && schema == nil {
				t.Fatal("GetResultSchema() returned nil")
			}
		})
	}
}

func TestHandler_Execute(t *testing.T) {
	tests := []struct {
		name           string
		analyzerName   string
		input          map[string]interface{}
		timeoutSeconds int
		statusCode     int
		response       ExecuteResult
		wantErr        bool
	}{
		{
			name:           "execute analyzer",
			analyzerName:   "test-analyzer",
			input:          map[string]interface{}{"key": "value"},
			timeoutSeconds: 30,
			statusCode:     http.StatusOK,
			response: ExecuteResult{
				RequestToken: "token123",
				Result: &AnalyzerResult{
					ResultID:        "result123",
					ResultStatus:    "SUCCESS",
					ExecutionStatus: "COMPLETED",
				},
			},
			wantErr: false,
		},
		{
			name:           "execute without timeout",
			analyzerName:   "test-analyzer",
			input:          map[string]interface{}{"key": "value"},
			timeoutSeconds: 0,
			statusCode:     http.StatusOK,
			response: ExecuteResult{
				RequestToken: "token456",
			},
			wantErr: false,
		},
		{
			name:           "server error",
			analyzerName:   "error",
			input:          map[string]interface{}{},
			timeoutSeconds: 0,
			statusCode:     http.StatusInternalServerError,
			wantErr:        true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				expectedPath := "/platform/davis/analyzers/v1/analyzers/" + tt.analyzerName + ":execute"
				if r.URL.Path != expectedPath {
					t.Errorf("unexpected path: got %s, want %s", r.URL.Path, expectedPath)
				}
				if r.Method != http.MethodPost {
					t.Errorf("unexpected method: got %s, want POST", r.Method)
				}

				// Verify timeout query parameter
				if tt.timeoutSeconds > 0 {
					timeoutParam := r.URL.Query().Get("timeout-seconds")
					if timeoutParam != "30" {
						t.Errorf("timeout-seconds = %q, want %q", timeoutParam, "30")
					}
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
			result, err := handler.Execute(tt.analyzerName, tt.input, tt.timeoutSeconds)

			if (err != nil) != tt.wantErr {
				t.Errorf("Execute() error = %v, wantErr %v", err, tt.wantErr)
				return
			}

			if !tt.wantErr {
				if result == nil {
					t.Fatal("Execute() returned nil")
				}
				// Verify populateTableFields was called
				if tt.name == "execute analyzer" {
					if result.ResultID != "result123" {
						t.Errorf("ResultID not populated, got %q, want %q", result.ResultID, "result123")
					}
					if result.ResultStatus != "SUCCESS" {
						t.Errorf("ResultStatus not populated, got %q, want %q", result.ResultStatus, "SUCCESS")
					}
				}
			}
		})
	}
}

func TestHandler_Poll(t *testing.T) {
	tests := []struct {
		name           string
		analyzerName   string
		requestToken   string
		timeoutSeconds int
		statusCode     int
		response       ExecuteResult
		wantErr        bool
		errMsg         string
	}{
		{
			name:           "poll successful",
			analyzerName:   "test-analyzer",
			requestToken:   "token123",
			timeoutSeconds: 10,
			statusCode:     http.StatusOK,
			response: ExecuteResult{
				RequestToken: "token123",
				Result: &AnalyzerResult{
					ResultID:        "result123",
					ExecutionStatus: "COMPLETED",
				},
			},
			wantErr: false,
		},
		{
			name:           "result expired",
			analyzerName:   "test-analyzer",
			requestToken:   "expired",
			timeoutSeconds: 0,
			statusCode:     http.StatusGone,
			wantErr:        true,
			errMsg:         "analyzer result expired or already consumed",
		},
		{
			name:           "server error",
			analyzerName:   "error",
			requestToken:   "token",
			timeoutSeconds: 0,
			statusCode:     http.StatusInternalServerError,
			wantErr:        true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				expectedPath := "/platform/davis/analyzers/v1/analyzers/" + tt.analyzerName + ":poll"
				if r.URL.Path != expectedPath {
					t.Errorf("unexpected path: got %s, want %s", r.URL.Path, expectedPath)
				}

				// Verify request-token query parameter
				tokenParam := r.URL.Query().Get("request-token")
				if tokenParam != tt.requestToken {
					t.Errorf("request-token = %q, want %q", tokenParam, tt.requestToken)
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
			result, err := handler.Poll(tt.analyzerName, tt.requestToken, tt.timeoutSeconds)

			if (err != nil) != tt.wantErr {
				t.Errorf("Poll() error = %v, wantErr %v", err, tt.wantErr)
				return
			}

			if tt.wantErr && tt.errMsg != "" {
				if err.Error() != tt.errMsg {
					t.Errorf("Poll() error = %q, want %q", err.Error(), tt.errMsg)
				}
			}

			if !tt.wantErr && result == nil {
				t.Fatal("Poll() returned nil")
			}
		})
	}
}

func TestHandler_Cancel(t *testing.T) {
	tests := []struct {
		name         string
		analyzerName string
		requestToken string
		statusCode   int
		response     ExecuteResult
		wantErr      bool
	}{
		{
			name:         "cancel successful",
			analyzerName: "test-analyzer",
			requestToken: "token123",
			statusCode:   http.StatusOK,
			response: ExecuteResult{
				RequestToken: "token123",
			},
			wantErr: false,
		},
		{
			name:         "server error",
			analyzerName: "error",
			requestToken: "token",
			statusCode:   http.StatusInternalServerError,
			wantErr:      true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				expectedPath := "/platform/davis/analyzers/v1/analyzers/" + tt.analyzerName + ":cancel"
				if r.URL.Path != expectedPath {
					t.Errorf("unexpected path: got %s, want %s", r.URL.Path, expectedPath)
				}
				if r.Method != http.MethodPost {
					t.Errorf("unexpected method: got %s, want POST", r.Method)
				}

				// Verify request-token query parameter
				tokenParam := r.URL.Query().Get("request-token")
				if tokenParam != tt.requestToken {
					t.Errorf("request-token = %q, want %q", tokenParam, tt.requestToken)
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
			result, err := handler.Cancel(tt.analyzerName, tt.requestToken)

			if (err != nil) != tt.wantErr {
				t.Errorf("Cancel() error = %v, wantErr %v", err, tt.wantErr)
				return
			}

			if !tt.wantErr && result == nil {
				t.Fatal("Cancel() returned nil")
			}
		})
	}
}

func TestHandler_Validate(t *testing.T) {
	tests := []struct {
		name         string
		analyzerName string
		input        map[string]interface{}
		statusCode   int
		response     ValidationResult
		wantErr      bool
	}{
		{
			name:         "validate valid input",
			analyzerName: "test-analyzer",
			input:        map[string]interface{}{"key": "value"},
			statusCode:   http.StatusOK,
			response: ValidationResult{
				Valid: true,
			},
			wantErr: false,
		},
		{
			name:         "validate invalid input",
			analyzerName: "test-analyzer",
			input:        map[string]interface{}{"invalid": "data"},
			statusCode:   http.StatusOK,
			response: ValidationResult{
				Valid: false,
				Details: map[string]interface{}{
					"error": "invalid field",
				},
			},
			wantErr: false,
		},
		{
			name:         "server error",
			analyzerName: "error",
			input:        map[string]interface{}{},
			statusCode:   http.StatusInternalServerError,
			wantErr:      true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				expectedPath := "/platform/davis/analyzers/v1/analyzers/" + tt.analyzerName + ":validate"
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
			result, err := handler.Validate(tt.analyzerName, tt.input)

			if (err != nil) != tt.wantErr {
				t.Errorf("Validate() error = %v, wantErr %v", err, tt.wantErr)
				return
			}

			if !tt.wantErr {
				if result == nil {
					t.Fatal("Validate() returned nil")
				}
				if result.Valid != tt.response.Valid {
					t.Errorf("Valid = %v, want %v", result.Valid, tt.response.Valid)
				}
			}
		})
	}
}

func TestParseInputFromFile(t *testing.T) {
	tests := []struct {
		name     string
		content  string
		wantErr  bool
		wantKeys []string
	}{
		{
			name:     "valid JSON file",
			content:  `{"key1": "value1", "key2": "value2"}`,
			wantErr:  false,
			wantKeys: []string{"key1", "key2"},
		},
		{
			name:    "invalid JSON",
			content: `{invalid json}`,
			wantErr: true,
		},
		{
			name:     "empty object",
			content:  `{}`,
			wantErr:  false,
			wantKeys: []string{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Create temp file
			tmpDir := t.TempDir()
			tmpFile := filepath.Join(tmpDir, "input.json")
			if err := os.WriteFile(tmpFile, []byte(tt.content), 0644); err != nil {
				t.Fatalf("failed to create temp file: %v", err)
			}

			input, err := ParseInputFromFile(tmpFile)

			if (err != nil) != tt.wantErr {
				t.Errorf("ParseInputFromFile() error = %v, wantErr %v", err, tt.wantErr)
				return
			}

			if !tt.wantErr {
				if input == nil {
					t.Fatal("ParseInputFromFile() returned nil")
				}
				for _, key := range tt.wantKeys {
					if _, ok := input[key]; !ok {
						t.Errorf("expected key %q not found in input", key)
					}
				}
			}
		})
	}
}

func TestParseInputFromFile_FileNotFound(t *testing.T) {
	_, err := ParseInputFromFile("/nonexistent/file.json")
	if err == nil {
		t.Error("ParseInputFromFile() expected error for nonexistent file, got nil")
	}
}

func TestParseInputFromFileWith_ReadsThroughTheGivenEnv(t *testing.T) {
	env := vfs.Env{FS: vfs.NewMapFS(map[string][]byte{"in.json": []byte(`{"timeframe": "now-1h"}`)})}

	input, err := ParseInputFromFileWith(env, "in.json")
	if err != nil {
		t.Fatalf("ParseInputFromFileWith: %v", err)
	}
	if input["timeframe"] != "now-1h" {
		t.Errorf("input = %v, want the file from the Env's filesystem", input)
	}

	if _, err := ParseInputFromFileWith(env, "missing.json"); err == nil {
		t.Error("a path outside the Env's filesystem must not resolve")
	}
}
