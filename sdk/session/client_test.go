package session

import (
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-resty/resty/v2"

	"github.com/Dynatrace/dtctl/sdk/httpclient"
)

func TestNew(t *testing.T) {
	tests := []struct {
		name        string
		baseURL     string
		token       string
		wantErr     bool
		errContains string
	}{
		{
			name:    "valid config",
			baseURL: "https://example.dynatrace.com",
			token:   "dt0c01.token",
			wantErr: false,
		},
		{
			name:        "empty base URL",
			baseURL:     "",
			token:       "dt0c01.token",
			wantErr:     true,
			errContains: "base URL is required",
		},
		{
			name:        "empty token",
			baseURL:     "https://example.dynatrace.com",
			token:       "",
			wantErr:     true,
			errContains: "token is required",
		},
		{
			name:        "both empty",
			baseURL:     "",
			token:       "",
			wantErr:     true,
			errContains: "base URL is required",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel() // Enable parallel execution
			client, err := NewClient(tt.baseURL, tt.token)
			if (err != nil) != tt.wantErr {
				t.Errorf("NewClient() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if tt.wantErr && tt.errContains != "" {
				if err == nil || !contains(err.Error(), tt.errContains) {
					t.Errorf("NewClient() error = %v, want error containing %q", err, tt.errContains)
				}
			}
			if !tt.wantErr && client == nil {
				t.Error("NewClient() returned nil client without error")
			}
			if !tt.wantErr {
				if client.BaseURL() != tt.baseURL {
					t.Errorf("BaseURL() = %v, want %v", client.BaseURL(), tt.baseURL)
				}
				// Verify client is properly configured
				if client.HTTP() == nil {
					t.Error("HTTP client not initialized")
				}
			}
		})
	}
}

// contains checks if a string contains a substring (helper for error checking)
func contains(s, substr string) bool {
	return len(substr) > 0 && len(s) >= len(substr) &&
		(s == substr || len(s) > len(substr) && (s[:len(substr)] == substr || s[len(s)-len(substr):] == substr || containsMiddle(s, substr)))
}

func containsMiddle(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

func TestClient_HTTP(t *testing.T) {
	client, err := NewClient("https://example.dynatrace.com", "test-token")
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}

	httpClient := client.HTTP()
	if httpClient == nil {
		t.Error("HTTP() returned nil")
	}
}

func TestClient_SetVerbosity(t *testing.T) {
	client, err := NewClient("https://example.dynatrace.com", "test-token")
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}

	// Test various verbosity levels - should not panic
	client.SetVerbosity(0)
	client.SetVerbosity(1)
	client.SetVerbosity(2)
}

func TestIsRetryable(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	client, err := NewClient(server.URL, "test-token")
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}

	// Make a request to get a response object for testing
	resp, err := client.HTTP().R().Get("/test")
	if err != nil {
		t.Fatalf("Request failed: %v", err)
	}

	// Test with successful response - should not retry
	if httpclient.IsRetryable(resp, nil) {
		t.Error("httpclient.IsRetryable() should return false for 200 response")
	}

	// A transport error still carries a response: resty builds one around the
	// request before calling the transport, and only leaves RawResponse nil. That
	// is the case worth retrying.
	if !httpclient.IsRetryable(&resty.Response{Request: resp.Request}, http.ErrServerClosed) {
		t.Error("httpclient.IsRetryable() should return true for a transport error")
	}

	// No response at all means the request never left the process -- resty
	// returns (nil, err) from a before-request middleware. Retrying cannot change
	// the outcome, and resty dereferences the nil response to prepare the retry.
	if httpclient.IsRetryable(nil, http.ErrServerClosed) {
		t.Error("httpclient.IsRetryable() should return false when the request was never sent")
	}
}

func TestClient_CurrentUser(t *testing.T) {
	tests := []struct {
		name       string
		statusCode int
		response   interface{}
		wantErr    bool
	}{
		{
			name:       "successful response",
			statusCode: http.StatusOK,
			response: UserInfo{
				UserName:     "test.user",
				UserID:       "user-123",
				EmailAddress: "test@example.com",
			},
			wantErr: false,
		},
		{
			name:       "unauthorized",
			statusCode: http.StatusUnauthorized,
			response:   map[string]string{"error": "unauthorized"},
			wantErr:    true,
		},
		{
			name:       "server error",
			statusCode: http.StatusInternalServerError,
			response:   map[string]string{"error": "internal error"},
			wantErr:    true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/platform/metadata/v1/user" {
					t.Errorf("unexpected path: %s", r.URL.Path)
				}
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(tt.statusCode)
				_ = json.NewEncoder(w).Encode(tt.response)
			}))
			defer server.Close()

			client, err := NewClient(server.URL, "test-token")
			if err != nil {
				t.Fatalf("NewClient() error = %v", err)
			}
			// Disable retries for faster tests
			client.HTTP().SetRetryCount(0)

			userInfo, err := client.CurrentUser()
			if (err != nil) != tt.wantErr {
				t.Errorf("CurrentUser() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if !tt.wantErr {
				if userInfo.UserID != "user-123" {
					t.Errorf("CurrentUser() UserID = %v, want user-123", userInfo.UserID)
				}
			}
		})
	}
}

func TestExtractUserIDFromToken(t *testing.T) {
	tests := []struct {
		name    string
		token   string
		want    string
		wantErr bool
	}{
		{
			name:    "valid JWT with sub claim",
			token:   "eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9.eyJzdWIiOiJ1c2VyLTEyMyIsIm5hbWUiOiJUZXN0IFVzZXIifQ.signature",
			want:    "user-123",
			wantErr: false,
		},
		{
			name:    "invalid JWT format - too few parts",
			token:   "invalid.token",
			want:    "",
			wantErr: true,
		},
		{
			name:    "invalid JWT format - not base64",
			token:   "header.!!!invalid!!!.signature",
			want:    "",
			wantErr: true,
		},
		{
			name:    "JWT without sub claim",
			token:   "eyJhbGciOiJIUzI1NiJ9.eyJuYW1lIjoiVGVzdCJ9.signature",
			want:    "",
			wantErr: true,
		},
		{
			name:    "empty token",
			token:   "",
			want:    "",
			wantErr: true,
		},
		{
			// Regression for #210: platform tokens have three dot-separated
			// parts but are not JWTs. Without the IsPlatformToken guard,
			// base64-decoding the middle segment yields random bytes and
			// json.Unmarshal reports e.g. "invalid character '#' looking for
			// beginning of value", which is what the bug report shows.
			name:    "platform token rejected (not a JWT)",
			token:   "dt0s16.ABCDEFGHIJKLMNOPQRSTUVWX.YZ0123456789abcdefghijkl",
			want:    "",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ExtractUserIDFromToken(tt.token)
			if (err != nil) != tt.wantErr {
				t.Errorf("ExtractUserIDFromToken() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if got != tt.want {
				t.Errorf("ExtractUserIDFromToken() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestIsPlatformToken(t *testing.T) {
	tests := []struct {
		name  string
		token string
		want  bool
	}{
		{
			name:  "platform token",
			token: "dt0s16.ABCDEFGHIJK.secret-portion-of-token",
			want:  true,
		},
		{
			name:  "platform token with empty payload",
			token: "dt0s16.",
			want:  true,
		},
		{
			name:  "classic environment API token",
			token: "dt0c01.ST.test-public.test-secret",
			want:  false,
		},
		{
			name:  "OAuth client ID prefix",
			token: "dt0s12.dtctl-prod",
			want:  false,
		},
		{
			name:  "OAuth access token (JWT shape)",
			token: "eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiJ1c2VyIn0.signature",
			want:  false,
		},
		{
			name:  "empty",
			token: "",
			want:  false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := IsPlatformToken(tt.token); got != tt.want {
				t.Errorf("IsPlatformToken(%q) = %v, want %v", tt.token, got, tt.want)
			}
		})
	}
}

func TestClient_RetryBehavior(t *testing.T) {
	requestCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestCount++
		if requestCount < 3 {
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"status": "ok"}`))
	}))
	defer server.Close()

	client, err := NewClient(server.URL, "test-token")
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}
	// Configure faster retries for testing
	client.HTTP().SetRetryWaitTime(10 * time.Millisecond)
	client.HTTP().SetRetryMaxWaitTime(50 * time.Millisecond)

	resp, err := client.HTTP().R().Get("/test")
	if err != nil {
		t.Fatalf("Request failed: %v", err)
	}

	if resp.StatusCode() != http.StatusOK {
		t.Errorf("Expected status 200 after retries, got %d", resp.StatusCode())
	}

	if requestCount < 3 {
		t.Errorf("Expected at least 3 requests (with retries), got %d", requestCount)
	}
}

// TestClient_RetryOnlyResendsSafeRequests: the CLI's client shares
// httpclient.IsRetryable, so a POST (query:execute, a workflow run) that hits
// a 5xx is not sent a second time, while a 429 still is.
func TestClient_RetryOnlyResendsSafeRequests(t *testing.T) {
	tests := []struct {
		method string
		status int
		want   int
	}{
		{method: http.MethodPost, status: http.StatusInternalServerError, want: 1},
		{method: http.MethodPost, status: http.StatusBadGateway, want: 1},
		{method: http.MethodPost, status: http.StatusTooManyRequests, want: 4},
		{method: http.MethodGet, status: http.StatusBadGateway, want: 4},
	}
	for _, tt := range tests {
		t.Run(fmt.Sprintf("%s %d", tt.method, tt.status), func(t *testing.T) {
			var attempts int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				atomic.AddInt32(&attempts, 1)
				w.WriteHeader(tt.status)
			}))
			defer server.Close()

			client, err := NewClient(server.URL, "test-token")
			if err != nil {
				t.Fatalf("NewClient() error = %v", err)
			}
			client.HTTP().SetRetryWaitTime(time.Millisecond)
			client.HTTP().SetRetryMaxWaitTime(time.Millisecond)

			if _, err := client.HTTP().R().Execute(tt.method, "/platform/x/v1/y"); err != nil {
				t.Fatalf("request failed: %v", err)
			}
			if got := atomic.LoadInt32(&attempts); int(got) != tt.want {
				t.Errorf("attempts = %d, want %d", got, tt.want)
			}
		})
	}
}

// TestClient_RetryResendsMultipartBody: the document write paths build their
// multipart parts from one-shot readers, and resty re-reads them on every
// attempt. Without rewinding, a retried create or update sends an empty part
// and still reports success. httpclient.New got this in #398; the CLI's client
// is built here and needs it too.
func TestClient_RetryResendsMultipartBody(t *testing.T) {
	const content = `{"tiles":{"0":{"type":"markdown"}}}`

	var attempts int32
	received := ""
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if atomic.AddInt32(&attempts, 1) == 1 {
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		file, _, err := r.FormFile("content")
		if err != nil {
			t.Errorf("FormFile: %v", err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		defer file.Close()
		body, err := io.ReadAll(file)
		if err != nil {
			t.Errorf("ReadAll: %v", err)
		}
		received = string(body)
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	client, err := NewClient(server.URL, "test-token")
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}
	client.HTTP().SetRetryWaitTime(time.Millisecond)
	client.HTTP().SetRetryMaxWaitTime(time.Millisecond)

	resp, err := client.HTTP().R().
		SetMultipartField("content", "content.json", "application/json", strings.NewReader(content)).
		Patch("/platform/document/v1/documents/doc-1")
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	if resp.StatusCode() != http.StatusOK {
		t.Errorf("status = %d, want 200", resp.StatusCode())
	}
	if got := atomic.LoadInt32(&attempts); got != 2 {
		t.Errorf("attempts = %d, want 2", got)
	}
	if received != content {
		t.Errorf("content = %q, want %q", received, content)
	}
}

func TestClient_Timeout(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(100 * time.Millisecond)
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	client, err := NewClient(server.URL, "test-token")
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}
	// Set very short timeout
	client.HTTP().SetTimeout(10 * time.Millisecond)
	client.HTTP().SetRetryCount(0)

	_, err = client.HTTP().R().Get("/test")
	if err == nil {
		t.Error("Expected timeout error, got nil")
	}
}

func TestClient_AuthHeader(t *testing.T) {
	var receivedAuth string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedAuth = r.Header.Get("Authorization")
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	token := "my-secret-token"
	client, err := NewClient(server.URL, token)
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}

	_, err = client.HTTP().R().Get("/test")
	if err != nil {
		t.Fatalf("Request failed: %v", err)
	}

	expectedAuth := "Bearer " + token
	if receivedAuth != expectedAuth {
		t.Errorf("Authorization header = %v, want %v", receivedAuth, expectedAuth)
	}
}

func TestClient_UserAgent(t *testing.T) {
	var receivedUA string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedUA = r.Header.Get("User-Agent")
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	client, err := NewClient(server.URL, "test-token")
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}

	_, err = client.HTTP().R().Get("/test")
	if err != nil {
		t.Fatalf("Request failed: %v", err)
	}

	// Without an identity option the UA falls back to the sdk default
	if !strings.HasPrefix(receivedUA, "dtctl-sdk") {
		t.Errorf("User-Agent = %v, want prefix dtctl-sdk", receivedUA)
	}

	// A consumer identity set via option must win (consumers must stay
	// distinguishable in tenant-side request logs).
	client, err = NewClient(server.URL, "test-token", WithUserAgentProduct("dtctl-foo", "9.9.9"))
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}
	if _, err = client.HTTP().R().Get("/test"); err != nil {
		t.Fatalf("Request failed: %v", err)
	}
	if !strings.HasPrefix(receivedUA, "dtctl-foo/9.9.9") {
		t.Errorf("User-Agent = %v, want prefix dtctl-foo/9.9.9", receivedUA)
	}

	// May include AI agent suffix like " (AI-Agent: opencode)" depending on environment
	// Just verify the base format is correct
}

func TestClient_BaseURL(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		baseURL string
	}{
		{
			name:    "standard dynatrace URL",
			baseURL: "https://example.dynatrace.com",
		},
		{
			name:    "managed URL",
			baseURL: "https://managed.example.com/e/environment-id",
		},
		{
			name:    "localhost",
			baseURL: "http://localhost:8080",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			client, err := NewClient(tt.baseURL, "test-token")
			if err != nil {
				t.Fatalf("NewClient() error = %v", err)
			}
			if client.BaseURL() != tt.baseURL {
				t.Errorf("BaseURL() = %v, want %v", client.BaseURL(), tt.baseURL)
			}
		})
	}
}

func TestIsSensitiveHeader(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		header string
		want   bool
	}{
		{
			name:   "authorization header",
			header: "Authorization",
			want:   true,
		},
		{
			name:   "authorization lowercase",
			header: "authorization",
			want:   true,
		},
		{
			name:   "x-api-key",
			header: "X-API-Key",
			want:   true,
		},
		{
			name:   "cookie",
			header: "Cookie",
			want:   true,
		},
		{
			name:   "set-cookie",
			header: "Set-Cookie",
			want:   true,
		},
		{
			name:   "content-type not sensitive",
			header: "Content-Type",
			want:   false,
		},
		{
			name:   "user-agent not sensitive",
			header: "User-Agent",
			want:   false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := isSensitiveHeader(tt.header)
			if got != tt.want {
				t.Errorf("isSensitiveHeader(%q) = %v, want %v", tt.header, got, tt.want)
			}
		})
	}
}

func TestClient_SetVerbosityLevels(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		level int
	}{
		{
			name:  "level 0 - no debug",
			level: 0,
		},
		{
			name:  "level 1 - summary",
			level: 1,
		},
		{
			name:  "level 2 - full details",
			level: 2,
		},
		{
			name:  "level 3 - verbose",
			level: 3,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			client, err := NewClient("https://example.dynatrace.com", "test-token")
			if err != nil {
				t.Fatalf("NewClient() error = %v", err)
			}
			// Should not panic
			client.SetVerbosity(tt.level)
		})
	}
}

func TestClient_CurrentUserID(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		apiSuccess  bool
		apiResponse UserInfo
		apiStatus   int
		token       string
		wantID      string
		wantErr     bool
		errContains string
	}{
		{
			name:       "successful API call",
			apiSuccess: true,
			apiResponse: UserInfo{
				UserID:       "api-user-123",
				UserName:     "test.user",
				EmailAddress: "test@example.com",
			},
			apiStatus: http.StatusOK,
			token:     "eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9.eyJzdWIiOiJ0b2tlbi11c2VyLTQ1NiJ9.signature",
			wantID:    "api-user-123",
			wantErr:   false,
		},
		{
			name:       "API fails, fallback to JWT",
			apiSuccess: false,
			apiStatus:  http.StatusUnauthorized,
			token:      "eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9.eyJzdWIiOiJ0b2tlbi11c2VyLTQ1NiJ9.signature",
			wantID:     "token-user-456",
			wantErr:    false,
		},
		{
			name:        "API fails and invalid JWT",
			apiSuccess:  false,
			apiStatus:   http.StatusUnauthorized,
			token:       "invalid-token",
			wantErr:     true,
			errContains: "invalid JWT token format",
		},
		{
			// Regression for #210: when /platform/metadata/v1/user fails
			// (e.g. 403 because the token lacks app-engine:apps:run) and the
			// token is a platform token, we must not silently fall through to
			// the JWT decoder — that produces a confusing "invalid character
			// '#' looking for beginning of value" error. Surface an actionable
			// message instead.
			name:        "platform token + metadata 403 returns actionable error",
			apiSuccess:  false,
			apiStatus:   http.StatusForbidden,
			token:       "dt0s16.ABCDEFGHIJKLMNOPQRSTUVWX.YZ0123456789abcdefghijkl",
			wantErr:     true,
			errContains: "platform token",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(tt.apiStatus)
				if tt.apiSuccess {
					_ = json.NewEncoder(w).Encode(tt.apiResponse)
				} else {
					_ = json.NewEncoder(w).Encode(map[string]string{"error": "unauthorized"})
				}
			}))
			defer server.Close()

			client, err := NewClient(server.URL, tt.token)
			if err != nil {
				t.Fatalf("NewClient() error = %v", err)
			}
			client.HTTP().SetRetryCount(0)

			userID, err := client.CurrentUserID()
			if (err != nil) != tt.wantErr {
				t.Errorf("CurrentUserID() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if tt.wantErr && tt.errContains != "" {
				if err == nil || !contains(err.Error(), tt.errContains) {
					t.Errorf("CurrentUserID() error = %v, want error containing %q", err, tt.errContains)
				}
			}
			if !tt.wantErr && userID != tt.wantID {
				t.Errorf("CurrentUserID() = %v, want %v", userID, tt.wantID)
			}
		})
	}
}

func TestIsRetryable_ContextDeadline(t *testing.T) {
	t.Parallel()

	// Test that context deadline errors are NOT retried
	err := context.DeadlineExceeded
	if httpclient.IsRetryable(nil, err) {
		t.Error("httpclient.IsRetryable() should return false for context.DeadlineExceeded")
	}
}

func TestIsRetryable_ContextCanceled(t *testing.T) {
	t.Parallel()

	// context.Canceled is the error produced when the user presses Ctrl+C.
	// It must not trigger retries, otherwise the resty WARN/ERROR logs
	// reappear and the process hangs for the full retry back-off period.
	if httpclient.IsRetryable(nil, context.Canceled) {
		t.Error("httpclient.IsRetryable() should return false for context.Canceled")
	}
}

func TestIsRetryable_StatusCodes(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		statusCode int
		want       bool
	}{
		{
			name:       "200 OK - no retry",
			statusCode: http.StatusOK,
			want:       false,
		},
		{
			name:       "400 Bad Request - no retry",
			statusCode: http.StatusBadRequest,
			want:       false,
		},
		{
			name:       "404 Not Found - no retry",
			statusCode: http.StatusNotFound,
			want:       false,
		},
		{
			name:       "429 Rate Limit - retry",
			statusCode: http.StatusTooManyRequests,
			want:       true,
		},
		{
			name:       "500 Internal Server Error - retry",
			statusCode: http.StatusInternalServerError,
			want:       true,
		},
		{
			name:       "502 Bad Gateway - retry",
			statusCode: http.StatusBadGateway,
			want:       true,
		},
		{
			name:       "503 Service Unavailable - retry",
			statusCode: http.StatusServiceUnavailable,
			want:       true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tt.statusCode)
			}))
			defer server.Close()

			client, err := NewClient(server.URL, "test-token")
			if err != nil {
				t.Fatalf("NewClient() error = %v", err)
			}
			client.HTTP().SetRetryCount(0)

			resp, _ := client.HTTP().R().Get("/test")
			got := httpclient.IsRetryable(resp, nil)
			if got != tt.want {
				t.Errorf("httpclient.IsRetryable() with status %d = %v, want %v", tt.statusCode, got, tt.want)
			}
		})
	}
}

func TestClient_AcceptEncodingGzip(t *testing.T) {
	var receivedEncoding string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedEncoding = r.Header.Get("Accept-Encoding")
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	client, err := NewClient(server.URL, "test-token")
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}

	_, err = client.HTTP().R().Get("/test")
	if err != nil {
		t.Fatalf("Request failed: %v", err)
	}

	if receivedEncoding != "gzip" {
		t.Errorf("Accept-Encoding = %v, want gzip", receivedEncoding)
	}
}

func TestClient_GzipResponseDecompression(t *testing.T) {
	// Verify that gzip-compressed responses are transparently decompressed.
	// When Accept-Encoding is explicitly set, Go's net/http transport skips
	// automatic decompression; resty's own middleware handles it instead.
	expectedBody := `{"items":[{"id":"abc-123","name":"test-workflow"}]}`

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Encoding", "gzip")
		w.Header().Set("Content-Type", "application/json")
		gz := gzip.NewWriter(w)
		_, _ = gz.Write([]byte(expectedBody))
		_ = gz.Close()
	}))
	defer server.Close()

	client, err := NewClient(server.URL, "test-token")
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}
	client.HTTP().SetRetryCount(0)

	resp, err := client.HTTP().R().Get("/test")
	if err != nil {
		t.Fatalf("Request failed: %v", err)
	}

	if resp.String() != expectedBody {
		t.Errorf("Response body = %q, want %q", resp.String(), expectedBody)
	}
}

func TestReadRequestBodyForDebug_NilGetBodyReader(t *testing.T) {
	req := &http.Request{
		GetBody: func() (io.ReadCloser, error) {
			return nil, nil
		},
	}

	got := readRequestBodyForDebug(req)
	if got != "" {
		t.Fatalf("readRequestBodyForDebug() = %q, want empty string", got)
	}
}

// TestNewClientFromConfig_LocalConfigEnvVarExpansion verifies that
// ${VAR} references in the environment URL of a local config are expanded
// by NewClientFromConfig before the Dynatrace URL validation runs.
func TestNewClientFromConfig_LocalConfigEnvVarExpansion(t *testing.T) {
	t.Setenv(EnvDisableKeyring, "1")

	t.Run("valid Dynatrace URL via env var", func(t *testing.T) {
		t.Setenv("DT_ENV_URL", "https://abc12345.apps.dynatrace.com")
		cfg := newLocalConfig(t, "${DT_ENV_URL}", "some-ref", nil)
		// Token resolution fails (no real keyring), but the URL check must pass.
		_, err := NewClientFromConfig(cfg)
		if err != nil && contains(err.Error(), "dynatrace") {
			t.Errorf("URL check failed for valid Dynatrace URL via env var: %v", err)
		}
	})

	t.Run("non-Dynatrace URL via env var rejected", func(t *testing.T) {
		t.Setenv("DT_ENV_URL", "https://evil.example.com")
		cfg := newLocalConfig(t, "${DT_ENV_URL}", "some-ref", nil)
		_, err := NewClientFromConfig(cfg)
		if err == nil {
			t.Fatal("NewClientFromConfig() succeeded for non-Dynatrace URL via env var, want error")
		}
		if !contains(err.Error(), "dynatrace") {
			t.Errorf("error = %q, want it to mention dynatrace", err.Error())
		}
	})
}

// TestNewClientFromConfig_LocalConfigDomainAllowlist verifies that
// NewClientFromConfig rejects local configs targeting non-Dynatrace hosts.
// The guard fires before token resolution so no real token is needed.
func TestNewClientFromConfig_LocalConfigDomainAllowlist(t *testing.T) {
	t.Setenv(EnvDisableKeyring, "1")

	tests := []struct {
		name        string
		environment string
		wantErr     string
	}{
		{
			name:        "foreign host rejected",
			environment: "https://evil.example.com",
			wantErr:     "dynatrace.com",
		},
		{
			name:        "http rejected",
			environment: "http://abc12345.apps.dynatrace.com",
			wantErr:     "https",
		},
		{
			name:        "embedded query rejected",
			environment: "https://abc12345.apps.dynatrace.com?foo=bar",
			wantErr:     "query",
		},
		{
			name:        "embedded credentials rejected",
			environment: "https://user:pass@abc12345.apps.dynatrace.com",
			wantErr:     "credentials",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := newLocalConfig(t, tt.environment, "some-ref", nil)
			_, err := NewClientFromConfig(cfg)
			if err == nil {
				t.Fatalf("NewClientFromConfig() succeeded for local config with %q, want error containing %q", tt.environment, tt.wantErr)
			}
			if !contains(err.Error(), tt.wantErr) {
				t.Errorf("NewClientFromConfig() error = %q, want it to contain %q", err.Error(), tt.wantErr)
			}
		})
	}
}

// TestNewClientFromConfig_LocalUnresolvedEnvVarNamesTheReference verifies that
// a .dtctl.yaml whose environment did not resolve is reported in terms of the
// reference the file actually contains. An unresolved value stays literal, and
// the origin check can only describe that as a malformed URL — useless to a
// developer who has just cloned a repo and not exported the variable yet.
func TestNewClientFromConfig_LocalUnresolvedEnvVarNamesTheReference(t *testing.T) {
	cfg := newLocalConfig(t, "${DT_TEST_UNSET_ENVIRONMENT_URL}", "some-ref", nil)

	_, err := NewClientFromConfig(cfg)
	if err == nil {
		t.Fatal("NewClientFromConfig() succeeded with an unset environment variable, want error")
	}
	for _, want := range []string{"DT_TEST_UNSET_ENVIRONMENT_URL", "did not resolve"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("NewClientFromConfig() error = %q, want it to contain %q", err.Error(), want)
		}
	}
}
