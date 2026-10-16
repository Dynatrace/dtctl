package exec

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dynatrace-oss/dtctl/pkg/client"
	"github.com/dynatrace-oss/dtctl/pkg/vfs"
)

// TestFunctionExecutor_Execute_ErrorEnvelope verifies end-to-end that the
// FunctionExecutor surfaces custom error envelopes from app functions.
// This complements the unit tests in pkg/resources/appengine/functions_test.go
// by exercising the full executor → handler → HTTP chain.
func TestFunctionExecutor_Execute_ErrorEnvelope(t *testing.T) {
	tests := []struct {
		name        string
		body        string
		statusCode  int
		wantErr     bool
		errContains string
	}{
		{
			name:        "custom error envelope is surfaced as error",
			body:        `{"data":null,"error":"Invalid value. Valid values: a,b,c."}`,
			statusCode:  http.StatusOK,
			wantErr:     true,
			errContains: "Invalid value. Valid values: a,b,c.",
		},
		{
			name:       "successful response with data passes through",
			body:       `{"data":{"result":"ok"},"error":null}`,
			statusCode: http.StatusOK,
			wantErr:    false,
		},
		{
			name:       "response without error field passes through",
			body:       `{"result":"ok"}`,
			statusCode: http.StatusOK,
			wantErr:    false,
		},
		{
			name:       "empty error string is not treated as failure",
			body:       `{"data":{"key":"value"},"error":""}`,
			statusCode: http.StatusOK,
			wantErr:    false,
		},
		{
			name:       "non-string error field is not treated as failure",
			body:       `{"data":null,"error":{"code":400,"message":"bad request"}}`,
			statusCode: http.StatusOK,
			wantErr:    false,
		},
		{
			name:        "HTTP error status is surfaced",
			body:        `{"error":"not found"}`,
			statusCode:  http.StatusNotFound,
			wantErr:     true,
			errContains: "not found",
		},
		{
			name:        "HTTP 540 JavaScript error",
			body:        `TypeError: undefined is not a function`,
			statusCode:  540,
			wantErr:     true,
			errContains: "JavaScript error",
		},
		{
			name:       "plain text success response",
			body:       `hello world`,
			statusCode: http.StatusOK,
			wantErr:    false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				// Verify the request path follows the expected pattern
				if !strings.Contains(r.URL.Path, "/platform/app-engine/app-functions/v1/apps/") {
					t.Errorf("unexpected request path: %s", r.URL.Path)
				}
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(tt.statusCode)
				_, _ = w.Write([]byte(tt.body))
			}))
			defer server.Close()

			c, err := client.NewForTesting(server.URL, "test-token")
			if err != nil {
				t.Fatalf("failed to create client: %v", err)
			}

			executor := NewFunctionExecutor(c)
			_, err = executor.Execute(FunctionExecuteOptions{
				FunctionName: "my.app/my-function",
				Method:       "POST",
				Payload:      `{"input":"test"}`,
			})

			if (err != nil) != tt.wantErr {
				t.Errorf("Execute() error = %v, wantErr %v", err, tt.wantErr)
				return
			}

			if tt.wantErr && tt.errContains != "" && !strings.Contains(err.Error(), tt.errContains) {
				t.Errorf("Execute() error = %q, want it to contain %q", err.Error(), tt.errContains)
			}
		})
	}
}

// TestFunctionExecutor_Execute_DeferredExecution tests that deferred execution
// mode calls the correct endpoint.
func TestFunctionExecutor_Execute_DeferredExecution(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.URL.Path, "/deferred-execution") {
			t.Errorf("expected deferred-execution path, got: %s", r.URL.Path)
		}
		if r.Method != "POST" {
			t.Errorf("expected POST, got: %s", r.Method)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"id":"exec-123"}`))
	}))
	defer server.Close()

	c, err := client.NewForTesting(server.URL, "test-token")
	if err != nil {
		t.Fatalf("failed to create client: %v", err)
	}

	executor := NewFunctionExecutor(c)
	result, err := executor.Execute(FunctionExecuteOptions{
		FunctionName: "my.app/my-function",
		Defer:        true,
		Payload:      `{"input":"test"}`,
	})

	if err != nil {
		t.Fatalf("Execute() unexpected error: %v", err)
	}
	if result == nil {
		t.Fatal("Execute() returned nil result")
	}
}

// TestFunctionExecutor_Execute_ValidationErrors tests input validation.
func TestFunctionExecutor_Execute_ValidationErrors(t *testing.T) {
	tests := []struct {
		name        string
		opts        FunctionExecuteOptions
		errContains string
	}{
		{
			name:        "missing function name",
			opts:        FunctionExecuteOptions{Method: "GET"},
			errContains: "app ID and function name are required",
		},
		{
			name:        "missing function after slash",
			opts:        FunctionExecuteOptions{FunctionName: "my.app/", Method: "GET"},
			errContains: "app ID and function name are required",
		},
		{
			name:        "empty source code",
			opts:        FunctionExecuteOptions{SourceCode: ""},
			errContains: "app ID and function name are required",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// No server needed — validation fails before HTTP call
			c, err := client.NewForTesting("http://localhost:0", "test-token")
			if err != nil {
				t.Fatalf("failed to create client: %v", err)
			}

			executor := NewFunctionExecutor(c)
			_, err = executor.Execute(tt.opts)

			if err == nil {
				t.Fatal("Execute() expected error, got nil")
			}
			if !strings.Contains(err.Error(), tt.errContains) {
				t.Errorf("Execute() error = %q, want it to contain %q", err.Error(), tt.errContains)
			}
		})
	}
}

// An executor given a vfs.Env reads its code and payload files through it: a
// path that exists only in the invocation's own filesystem resolves, and one
// that exists only on the host does not.
func TestFunctionExecutor_WithVFSReadsCodeAndPayloadThroughItsEnv(t *testing.T) {
	var gotBody string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"result":"ok","id":"exec-1"}`))
	}))
	defer server.Close()

	c, err := client.NewForTesting(server.URL, "test-token")
	if err != nil {
		t.Fatalf("failed to create client: %v", err)
	}
	env := vfs.Env{FS: vfs.NewMapFS(map[string][]byte{
		"code.js":      []byte("export default () => 42"),
		"payload.json": []byte(`{"input":"from-the-env"}`),
	})}
	executor := NewFunctionExecutor(c).WithVFS(env)

	t.Run("ad-hoc code", func(t *testing.T) {
		if _, err := executor.Execute(FunctionExecuteOptions{SourceCodeFile: "code.js", PayloadFile: "payload.json"}); err != nil {
			t.Fatalf("Execute() error = %v", err)
		}
		var sent struct {
			SourceCode string `json:"sourceCode"`
			Payload    string `json:"payload"`
		}
		if err := json.Unmarshal([]byte(gotBody), &sent); err != nil {
			t.Fatalf("request body %q is not JSON: %v", gotBody, err)
		}
		if sent.SourceCode != "export default () => 42" {
			t.Errorf("sourceCode = %q, want the code from the env's file", sent.SourceCode)
		}
		if sent.Payload != `{"input":"from-the-env"}` {
			t.Errorf("payload = %q, want the payload from the env's file", sent.Payload)
		}
	})

	t.Run("app function payload", func(t *testing.T) {
		gotBody = ""
		if _, err := executor.Execute(FunctionExecuteOptions{FunctionName: "my.app/my-function", Defer: true, PayloadFile: "payload.json"}); err != nil {
			t.Fatalf("Execute() error = %v", err)
		}
		if !strings.Contains(gotBody, "from-the-env") {
			t.Errorf("request body %q lacks the payload from the env's file", gotBody)
		}
	})

	t.Run("a host-only path does not resolve", func(t *testing.T) {
		hostFile := filepath.Join(t.TempDir(), "host.json")
		if err := os.WriteFile(hostFile, []byte(`{"input":"host"}`), 0o600); err != nil {
			t.Fatal(err)
		}
		_, err := executor.Execute(FunctionExecuteOptions{SourceCodeFile: "code.js", PayloadFile: hostFile})
		if err == nil || !strings.Contains(err.Error(), "failed to read payload") {
			t.Errorf("Execute() error = %v, want a payload read failure for a path outside the env", err)
		}
	})
}

func TestReadFileOrStdinWith_UsesTheGivenEnv(t *testing.T) {
	env := vfs.Env{
		FS:    vfs.NewMapFS(map[string][]byte{"f.txt": []byte("from the env")}),
		Stdin: strings.NewReader("from stdin"),
	}

	if got, err := ReadFileOrStdinWith(env, "f.txt"); err != nil || got != "from the env" {
		t.Errorf("ReadFileOrStdinWith(file) = %q, %v", got, err)
	}
	if got, err := ReadFileOrStdinWith(env, "-"); err != nil || got != "from stdin" {
		t.Errorf("ReadFileOrStdinWith(-) = %q, %v", got, err)
	}
	if _, err := ReadFileOrStdinWith(env, "missing.txt"); err == nil || !strings.Contains(err.Error(), `failed to read file "missing.txt"`) {
		t.Errorf("ReadFileOrStdinWith(missing) error = %v, want the path named", err)
	}

	// The entry point without an env resolves through the installed filesystem.
	prev := vfs.SetActive(vfs.NewMapFS(map[string][]byte{"g.txt": []byte("installed")}))
	t.Cleanup(func() { vfs.SetActive(prev) })
	if got, err := ReadFileOrStdin("g.txt"); err != nil || got != "installed" {
		t.Errorf("ReadFileOrStdin = %q, %v, want the installed filesystem's file", got, err)
	}
}
