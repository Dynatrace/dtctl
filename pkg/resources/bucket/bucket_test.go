package bucket

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
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
		validate      func(*testing.T, *BucketList)
	}{
		{
			name:       "successful list",
			statusCode: 200,
			responseBody: BucketList{
				Buckets: []Bucket{
					{
						BucketName:    "default_metrics",
						Table:         "metrics",
						DisplayName:   "Default Metrics",
						Status:        "active",
						RetentionDays: 35,
						Version:       1,
						Updatable:     false,
					},
					{
						BucketName:    "custom_logs",
						Table:         "logs",
						DisplayName:   "Custom Logs",
						Status:        "active",
						RetentionDays: 90,
						Version:       2,
						Updatable:     true,
					},
				},
			},
			expectError: false,
			validate: func(t *testing.T, result *BucketList) {
				if len(result.Buckets) != 2 {
					t.Errorf("expected 2 buckets, got %d", len(result.Buckets))
				}
				if result.Buckets[0].BucketName != "default_metrics" {
					t.Errorf("expected first bucket name 'default_metrics', got %q", result.Buckets[0].BucketName)
				}
			},
		},
		{
			name:         "empty list",
			statusCode:   200,
			responseBody: BucketList{Buckets: []Bucket{}},
			expectError:  false,
			validate: func(t *testing.T, result *BucketList) {
				if len(result.Buckets) != 0 {
					t.Errorf("expected 0 buckets, got %d", len(result.Buckets))
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
				if r.URL.Path != "/platform/storage/management/v1/bucket-definitions" {
					t.Errorf("expected path '/platform/storage/management/v1/bucket-definitions', got %q", r.URL.Path)
				}
				if r.URL.Query().Get("add-fields") != "records" {
					t.Errorf("expected add-fields=records query param")
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
				} else if tt.errorContains != "" && !contains(err.Error(), tt.errorContains) {
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
		bucketName    string
		statusCode    int
		responseBody  interface{}
		expectError   bool
		errorContains string
		validate      func(*testing.T, *Bucket)
	}{
		{
			name:       "successful get",
			bucketName: "my-bucket",
			statusCode: 200,
			responseBody: Bucket{
				BucketName:    "my-bucket",
				Table:         "logs",
				DisplayName:   "My Custom Bucket",
				Status:        "active",
				RetentionDays: 60,
				Version:       1,
				Updatable:     true,
			},
			expectError: false,
			validate: func(t *testing.T, bucket *Bucket) {
				if bucket.BucketName != "my-bucket" {
					t.Errorf("expected bucket name 'my-bucket', got %q", bucket.BucketName)
				}
				if bucket.RetentionDays != 60 {
					t.Errorf("expected retention days 60, got %d", bucket.RetentionDays)
				}
			},
		},
		{
			name:          "bucket not found",
			bucketName:    "non-existent",
			statusCode:    404,
			responseBody:  "not found",
			expectError:   true,
			errorContains: "not found",
		},
		{
			name:          "server error",
			bucketName:    "test-bucket",
			statusCode:    500,
			responseBody:  "internal error",
			expectError:   true,
			errorContains: "API error (500)",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				expectedPath := "/platform/storage/management/v1/bucket-definitions/" + tt.bucketName
				if r.URL.Path != expectedPath {
					t.Errorf("expected path %q, got %q", expectedPath, r.URL.Path)
				}
				if r.URL.Query().Get("add-fields") != "records,estimatedUncompressedBytes" {
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

			result, err := h.Get(tt.bucketName)

			if tt.expectError {
				if err == nil {
					t.Error("expected error, got nil")
				} else if tt.errorContains != "" && !contains(err.Error(), tt.errorContains) {
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
		request       BucketCreate
		statusCode    int
		responseBody  interface{}
		expectError   bool
		errorContains string
		validate      func(*testing.T, *Bucket)
	}{
		{
			name: "successful create",
			request: BucketCreate{
				BucketName:    "new-bucket",
				Table:         "logs",
				DisplayName:   "New Bucket",
				RetentionDays: 30,
			},
			statusCode: 201,
			responseBody: Bucket{
				BucketName:    "new-bucket",
				Table:         "logs",
				DisplayName:   "New Bucket",
				Status:        "active",
				RetentionDays: 30,
				Version:       1,
				Updatable:     true,
			},
			expectError: false,
			validate: func(t *testing.T, bucket *Bucket) {
				if bucket.BucketName != "new-bucket" {
					t.Errorf("expected bucket name 'new-bucket', got %q", bucket.BucketName)
				}
			},
		},
		{
			name: "bucket already exists",
			request: BucketCreate{
				BucketName:    "existing-bucket",
				Table:         "logs",
				RetentionDays: 30,
			},
			statusCode:    409,
			responseBody:  "bucket already exists",
			expectError:   true,
			errorContains: "already exists",
		},
		{
			name: "invalid configuration",
			request: BucketCreate{
				BucketName:    "invalid",
				Table:         "invalid-table",
				RetentionDays: -1,
			},
			statusCode:    400,
			responseBody:  "invalid configuration",
			expectError:   true,
			errorContains: "invalid configuration",
		},
		{
			name: "access denied",
			request: BucketCreate{
				BucketName:    "denied-bucket",
				Table:         "logs",
				RetentionDays: 30,
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
				if r.URL.Path != "/platform/storage/management/v1/bucket-definitions" {
					t.Errorf("expected path '/platform/storage/management/v1/bucket-definitions', got %q", r.URL.Path)
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
				} else if tt.errorContains != "" && !contains(err.Error(), tt.errorContains) {
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
		bucketName    string
		version       int
		request       BucketUpdate
		statusCode    int
		responseBody  string
		expectError   bool
		errorContains string
	}{
		{
			name:       "successful update",
			bucketName: "my-bucket",
			version:    1,
			request: BucketUpdate{
				DisplayName:   "Updated Bucket",
				RetentionDays: 90,
			},
			statusCode:  200,
			expectError: false,
		},
		{
			name:       "bucket not found",
			bucketName: "non-existent",
			version:    1,
			request: BucketUpdate{
				RetentionDays: 60,
			},
			statusCode:    404,
			responseBody:  "not found",
			expectError:   true,
			errorContains: "not found",
		},
		{
			name:       "version conflict",
			bucketName: "my-bucket",
			version:    1,
			request: BucketUpdate{
				RetentionDays: 90,
			},
			statusCode:    409,
			responseBody:  "version conflict",
			expectError:   true,
			errorContains: "version conflict",
		},
		{
			name:       "read-only bucket",
			bucketName: "default_metrics",
			version:    1,
			request: BucketUpdate{
				RetentionDays: 90,
			},
			statusCode:    403,
			responseBody:  "read-only",
			expectError:   true,
			errorContains: "read-only",
		},
		{
			name:       "invalid configuration",
			bucketName: "my-bucket",
			version:    1,
			request: BucketUpdate{
				RetentionDays: -1,
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
				if r.Method != "PATCH" {
					t.Errorf("expected PATCH method, got %s", r.Method)
				}
				expectedPath := "/platform/storage/management/v1/bucket-definitions/" + tt.bucketName
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

			err = h.Update(tt.bucketName, tt.version, tt.request)

			if tt.expectError {
				if err == nil {
					t.Error("expected error, got nil")
				} else if tt.errorContains != "" && !contains(err.Error(), tt.errorContains) {
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
		bucketName    string
		statusCode    int
		responseBody  string
		expectError   bool
		errorContains string
	}{
		{
			name:        "successful delete",
			bucketName:  "my-bucket",
			statusCode:  204,
			expectError: false,
		},
		{
			name:          "bucket not found",
			bucketName:    "non-existent",
			statusCode:    404,
			responseBody:  "not found",
			expectError:   true,
			errorContains: "not found",
		},
		{
			name:          "bucket in use",
			bucketName:    "active-bucket",
			statusCode:    409,
			responseBody:  "bucket in use",
			expectError:   true,
			errorContains: "bucket in use",
		},
		{
			name:          "read-only bucket",
			bucketName:    "default_metrics",
			statusCode:    403,
			responseBody:  "read-only",
			expectError:   true,
			errorContains: "read-only",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != "DELETE" {
					t.Errorf("expected DELETE method, got %s", r.Method)
				}
				expectedPath := "/platform/storage/management/v1/bucket-definitions/" + tt.bucketName
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

			err = h.Delete(tt.bucketName)

			if tt.expectError {
				if err == nil {
					t.Error("expected error, got nil")
				} else if tt.errorContains != "" && !contains(err.Error(), tt.errorContains) {
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

func TestTruncate(t *testing.T) {
	tests := []struct {
		name          string
		bucketName    string
		statusCode    int
		responseBody  string
		expectError   bool
		errorContains string
	}{
		{
			name:        "successful truncate",
			bucketName:  "my-bucket",
			statusCode:  204,
			expectError: false,
		},
		{
			name:          "bucket not found",
			bucketName:    "non-existent",
			statusCode:    404,
			responseBody:  "not found",
			expectError:   true,
			errorContains: "not found",
		},
		{
			name:          "access denied",
			bucketName:    "protected-bucket",
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
				expectedPath := "/platform/storage/management/v1/bucket-definitions/" + tt.bucketName + ":truncate"
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

			err = h.Truncate(tt.bucketName)

			if tt.expectError {
				if err == nil {
					t.Error("expected error, got nil")
				} else if tt.errorContains != "" && !contains(err.Error(), tt.errorContains) {
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
		expectedBucket := Bucket{
			BucketName:    "test-bucket",
			Table:         "logs",
			DisplayName:   "Test Bucket",
			Status:        "active",
			RetentionDays: 30,
			Version:       1,
			Updatable:     true,
		}

		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(200)
			json.NewEncoder(w).Encode(expectedBucket)
		}))
		defer server.Close()

		c, err := client.NewForTesting(server.URL, "test-token")
		if err != nil {
			t.Fatalf("failed to create client: %v", err)
		}
		h := NewHandler(c)

		raw, err := h.GetRaw("test-bucket")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		// Verify it's valid JSON
		var bucket Bucket
		if err := json.Unmarshal(raw, &bucket); err != nil {
			t.Fatalf("failed to unmarshal raw JSON: %v", err)
		}

		if bucket.BucketName != expectedBucket.BucketName {
			t.Errorf("expected bucket name %q, got %q", expectedBucket.BucketName, bucket.BucketName)
		}
	})

	t.Run("get raw with non-existent bucket", func(t *testing.T) {
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

// Helper function to check if a string contains a substring
func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(substr) == 0 ||
		(len(s) > 0 && len(substr) > 0 && stringContains(s, substr)))
}

func stringContains(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
