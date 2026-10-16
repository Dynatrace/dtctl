package appengine

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/dynatrace-oss/dtctl/pkg/client"
	"github.com/dynatrace-oss/dtctl/pkg/vfs"
)

func TestReadFileOrStdin(t *testing.T) {
	t.Run("read nonexistent file", func(t *testing.T) {
		_, err := ReadFileOrStdin("/nonexistent/file.txt")
		if err == nil {
			t.Error("Expected error for nonexistent file")
		}
	})
}

// TestFunctionHandler_InvokeFunction_CustomError verifies that HTTP 200 responses
// carrying a custom error envelope are treated as failures. App functions signal
// errors via {"data": null, "error": "..."} at HTTP 200, so dtctl must inspect
// the body rather than relying on the status code alone.
func TestFunctionHandler_InvokeFunction_CustomError(t *testing.T) {
	tests := []struct {
		name        string
		body        string
		wantErr     bool
		errContains string
	}{
		{
			name:        "custom error with string message",
			body:        `{"data":null,"error":"Oooops, this should not have happened."}`,
			wantErr:     true,
			errContains: "Oooops, this should not have happened.",
		},
		{
			name:    "null error field is not an error",
			body:    `{"data":{"key":"value"},"error":null}`,
			wantErr: false,
		},
		{
			name:    "no error field is not an error",
			body:    `{"data":{"key":"value"}}`,
			wantErr: false,
		},
		{
			name:    "plain string response is not an error",
			body:    `"hello world"`,
			wantErr: false,
		},
		{
			name:    "empty error string is not an error",
			body:    `{"data":{"key":"value"},"error":""}`,
			wantErr: false,
		},
		{
			name:    "error false is not an error",
			body:    `{"data":null,"error":false}`,
			wantErr: false,
		},
		{
			name:    "error zero is not an error",
			body:    `{"data":null,"error":0}`,
			wantErr: false,
		},
		{
			name:    "array response is not an error",
			body:    `[1,2,3]`,
			wantErr: false,
		},
		{
			name:    "structured error object is not treated as error",
			body:    `{"data":null,"error":{"code":400,"message":"bad request"}}`,
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte(tt.body))
			}))
			defer server.Close()

			c, err := client.NewForTesting(server.URL, "test-token")
			if err != nil {
				t.Fatalf("failed to create client: %v", err)
			}

			handler := NewFunctionHandler(c)
			_, err = handler.InvokeFunction(&FunctionInvokeRequest{
				Method:       "POST",
				AppID:        "dynatrace.automations",
				FunctionName: "execute-dql-query",
			})

			if (err != nil) != tt.wantErr {
				t.Errorf("InvokeFunction() error = %v, wantErr %v", err, tt.wantErr)
				return
			}

			if tt.wantErr && tt.errContains != "" && !strings.Contains(err.Error(), tt.errContains) {
				t.Errorf("InvokeFunction() error = %q, want it to contain %q", err.Error(), tt.errContains)
			}
		})
	}
}

func TestReadFileOrStdinWith_UsesTheGivenEnv(t *testing.T) {
	env := vfs.Env{
		FS:    vfs.NewMapFS(map[string][]byte{"code.js": []byte("export default 1")}),
		Stdin: strings.NewReader("from stdin"),
	}

	got, err := ReadFileOrStdinWith(env, "code.js")
	if err != nil || got != "export default 1" {
		t.Errorf("ReadFileOrStdinWith(file) = %q, %v, want the file from the Env's filesystem", got, err)
	}
	got, err = ReadFileOrStdinWith(env, "-")
	if err != nil || got != "from stdin" {
		t.Errorf("ReadFileOrStdinWith(-) = %q, %v, want the Env's stdin", got, err)
	}
}
