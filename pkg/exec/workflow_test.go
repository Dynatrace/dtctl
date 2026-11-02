package exec

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Dynatrace/dtctl/pkg/client"
)

func TestWorkflowExecutor_Execute_SendsLegacyParamsCompatibilityRequest(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Fatalf("expected POST, got %s", r.Method)
		}
		if r.URL.Path != "/platform/automation/v1/workflows/wf-123/run" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}

		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("failed to decode request body: %v", err)
		}

		params, ok := body["params"].(map[string]any)
		if !ok {
			t.Fatalf("expected request body to contain legacy params, got %#v", body["params"])
		}
		if params["severity"] != "high" {
			t.Fatalf("expected params.severity=high, got %#v", params["severity"])
		}
		if params["env"] != "prod" {
			t.Fatalf("expected params.env=prod, got %#v", params["env"])
		}
		if _, exists := body["input"]; exists {
			t.Fatalf("expected legacy params compatibility request to omit workflow input, got %#v", body)
		}

		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"exec-123","workflow":"wf-123","state":"RUNNING"}`))
	}))
	defer server.Close()

	c, err := client.NewForTesting(server.URL, "test-token")
	if err != nil {
		t.Fatalf("failed to create client: %v", err)
	}

	executor := NewWorkflowExecutor(c)
	result, err := executor.Execute("wf-123", WorkflowExecutionRequest{
		Params: map[string]any{"severity": "high", "env": "prod"},
	})
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if result.ID != "exec-123" {
		t.Fatalf("expected execution id exec-123, got %s", result.ID)
	}
}

func TestWorkflowExecutor_Execute_SendsWorkflowInputRequest(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Fatalf("expected POST, got %s", r.Method)
		}
		if r.URL.Path != "/platform/automation/v1/workflows/wf-123/run" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}

		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("failed to decode request body: %v", err)
		}

		input, ok := body["input"].(map[string]any)
		if !ok {
			t.Fatalf("expected request body to contain workflow input, got %#v", body["input"])
		}
		if input["severity"] != "high" {
			t.Fatalf("expected input.severity=high, got %#v", input["severity"])
		}
		if input["count"] != float64(3) {
			t.Fatalf("expected input.count=3, got %#v", input["count"])
		}
		options, ok := input["options"].(map[string]any)
		if !ok || options["dryRun"] != true {
			t.Fatalf("expected nested options.dryRun=true, got %#v", input["options"])
		}
		items, ok := input["items"].([]any)
		if !ok || len(items) != 3 {
			t.Fatalf("expected items array, got %#v", input["items"])
		}
		if items[2] != nil {
			t.Fatalf("expected items[2]=nil, got %#v", items[2])
		}
		if _, exists := body["params"]; exists {
			t.Fatalf("expected workflow input request to omit legacy params, got %#v", body)
		}

		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"exec-456","workflow":"wf-123","state":"RUNNING"}`))
	}))
	defer server.Close()

	c, err := client.NewForTesting(server.URL, "test-token")
	if err != nil {
		t.Fatalf("failed to create client: %v", err)
	}

	executor := NewWorkflowExecutor(c)
	result, err := executor.Execute("wf-123", WorkflowExecutionRequest{
		Input: map[string]any{
			"severity": "high",
			"count":    3,
			"options": map[string]any{
				"dryRun": true,
			},
			"items": []any{"a", false, nil},
		},
	})
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if result.ID != "exec-456" {
		t.Fatalf("expected execution id exec-456, got %s", result.ID)
	}
}

func TestWorkflowExecutor_Execute_SendsMonitorQueryParamWhenRequested(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.Query().Get("monitor"); got != "true" {
			t.Fatalf("expected monitor=true query param, got %q", got)
		}

		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"exec-monitor","workflow":"wf-123","state":"RUNNING"}`))
	}))
	defer server.Close()

	c, err := client.NewForTesting(server.URL, "test-token")
	if err != nil {
		t.Fatalf("failed to create client: %v", err)
	}

	executor := NewWorkflowExecutor(c)
	_, err = executor.Execute("wf-123", WorkflowExecutionRequest{Monitor: true})
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
}

func TestWorkflowExecutor_Execute_UsesAPIErrorMessageForRateLimit(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"error":{"code":429,"message":"Too many requests. Please wait for 17 seconds before making another request."}}`))
	}))
	defer server.Close()

	c, err := client.NewForTesting(server.URL, "test-token")
	if err != nil {
		t.Fatalf("failed to create client: %v", err)
	}

	executor := NewWorkflowExecutor(c)
	_, err = executor.Execute("wf-123", WorkflowExecutionRequest{})
	if err == nil {
		t.Fatal("Execute() expected error, got nil")
	}

	if !strings.Contains(err.Error(), "Too many requests. Please wait for 17 seconds before making another request.") {
		t.Fatalf("Execute() error = %q, want server error message", err.Error())
	}
}

func TestWorkflowExecutor_GetStatus_UsesAPIErrorMessage(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"error":{"code":429,"message":"Too many requests. Please wait for 9 seconds before making another request."}}`))
	}))
	defer server.Close()

	c, err := client.NewForTesting(server.URL, "test-token")
	if err != nil {
		t.Fatalf("failed to create client: %v", err)
	}

	executor := NewWorkflowExecutor(c)
	_, err = executor.GetStatus("exec-123")
	if err == nil {
		t.Fatal("GetStatus() expected error, got nil")
	}

	if !strings.Contains(err.Error(), "Too many requests. Please wait for 9 seconds before making another request.") {
		t.Fatalf("GetStatus() error = %q, want server error message", err.Error())
	}
}

func TestWorkflowExecutor_Execute_FallsBackToRawErrorBody(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write([]byte("upstream gateway timeout"))
	}))
	defer server.Close()

	c, err := client.NewForTesting(server.URL, "test-token")
	if err != nil {
		t.Fatalf("failed to create client: %v", err)
	}

	executor := NewWorkflowExecutor(c)
	_, err = executor.Execute("wf-123", WorkflowExecutionRequest{})
	if err == nil {
		t.Fatal("Execute() expected error, got nil")
	}

	if !strings.Contains(err.Error(), "status 502: upstream gateway timeout") {
		t.Fatalf("Execute() error = %q, want raw response body fallback", err.Error())
	}
}
