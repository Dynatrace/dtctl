package wait

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Dynatrace/dtctl/pkg/client"
	"github.com/Dynatrace/dtctl/pkg/exec"
)

// newWaiterTestExecutor creates a DQL executor backed by a test server.
func newWaiterTestExecutor(t *testing.T, handler http.HandlerFunc) (*exec.DQLExecutor, func()) {
	t.Helper()
	srv := httptest.NewServer(handler)
	c, err := client.NewForTesting(srv.URL, "test-token")
	if err != nil {
		t.Fatalf("failed to create client: %v", err)
	}
	return exec.NewDQLExecutor(c), srv.Close
}

// --- NewQueryWaiter ---

func TestNewQueryWaiter_Defaults(t *testing.T) {
	executor, cleanup := newWaiterTestExecutor(t, func(w http.ResponseWriter, r *http.Request) {})
	defer cleanup()

	config := WaitConfig{
		Query:     "fetch logs",
		Condition: Condition{Type: ConditionTypeAny, Operator: OpGreater, Value: 0},
		Timeout:   0,
	}
	waiter := NewQueryWaiter(executor, config)
	if waiter == nil {
		t.Fatal("NewQueryWaiter returned nil")
	}
	// ProgressOut should default to os.Stderr (non-nil)
	if waiter.config.ProgressOut == nil {
		t.Error("expected ProgressOut to be set to default")
	}
}

func TestNewQueryWaiter_CustomProgressOut(t *testing.T) {
	executor, cleanup := newWaiterTestExecutor(t, func(w http.ResponseWriter, r *http.Request) {})
	defer cleanup()

	buf := &bytes.Buffer{}
	config := WaitConfig{
		Query:       "fetch logs",
		Condition:   Condition{Type: ConditionTypeAny, Operator: OpGreater, Value: 0},
		ProgressOut: buf,
	}
	waiter := NewQueryWaiter(executor, config)
	if waiter.config.ProgressOut != buf {
		t.Error("expected custom ProgressOut to be preserved")
	}
}

// --- Wait: immediate success ---

func TestWait_ImmediateSuccess(t *testing.T) {
	executor, cleanup := newWaiterTestExecutor(t, func(w http.ResponseWriter, r *http.Request) {
		response := exec.DQLQueryResponse{
			State: "SUCCEEDED",
			Result: &exec.DQLResult{
				Records: []map[string]interface{}{
					{"host.name": "server-01"},
				},
			},
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(response)
	})
	defer cleanup()

	buf := &bytes.Buffer{}
	config := WaitConfig{
		Query:       "fetch hosts",
		Condition:   Condition{Type: ConditionTypeAny, Operator: OpGreater, Value: 0},
		MaxAttempts: 5,
		Quiet:       true,
		ProgressOut: buf,
		Backoff:     DefaultBackoffConfig(),
	}
	// Override backoff for speed
	config.Backoff.MinInterval = 0
	config.Backoff.MaxInterval = 0

	waiter := NewQueryWaiter(executor, config)
	ctx := context.Background()
	result, err := waiter.Wait(ctx)
	if err != nil {
		t.Fatalf("Wait() error = %v", err)
	}
	if result == nil {
		t.Fatal("expected result, got nil")
	}
	if !result.Success {
		t.Errorf("expected success, got failure: %s", result.FailureReason)
	}
}

// --- Wait: max attempts exceeded ---

func TestWait_MaxAttemptsExceeded(t *testing.T) {
	callCount := 0
	executor, cleanup := newWaiterTestExecutor(t, func(w http.ResponseWriter, r *http.Request) {
		callCount++
		// Always return empty records (condition never met)
		response := exec.DQLQueryResponse{
			State:  "SUCCEEDED",
			Result: &exec.DQLResult{Records: []map[string]interface{}{}},
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(response)
	})
	defer cleanup()

	buf := &bytes.Buffer{}
	config := WaitConfig{
		Query:       "fetch logs",
		Condition:   Condition{Type: ConditionTypeCount, Operator: OpGreaterEqual, Value: 10},
		MaxAttempts: 2,
		Quiet:       true,
		ProgressOut: buf,
		Backoff:     BackoffConfig{MinInterval: 0, MaxInterval: 0},
	}
	waiter := NewQueryWaiter(executor, config)
	ctx := context.Background()
	result, err := waiter.Wait(ctx)
	if err != nil {
		t.Fatalf("Wait() error = %v", err)
	}
	if result.Success {
		t.Error("expected failure (max attempts), got success")
	}
	if callCount != 2 {
		t.Errorf("expected 2 calls, got %d", callCount)
	}
}

// --- Wait: permanent query error fails fast ---

func TestWait_PermanentQueryError_FailsFast(t *testing.T) {
	callCount := 0
	executor, cleanup := newWaiterTestExecutor(t, func(w http.ResponseWriter, r *http.Request) {
		callCount++
		// A malformed DQL query — the backend rejects it with a 4xx parse error.
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		fmt.Fprint(w, `{"error":{"message":"single quotes are not allowed","details":{"errorType":"PARSE_ERROR_SINGLE_QUOTES"}}}`)
	})
	defer cleanup()

	buf := &bytes.Buffer{}
	config := WaitConfig{
		Query:       "fetch logs | filter status == 'ERROR'",
		Condition:   Condition{Type: ConditionTypeAny, Operator: OpGreater, Value: 0},
		MaxAttempts: 5,
		Quiet:       false,
		ProgressOut: buf,
		Backoff:     BackoffConfig{MinInterval: 0, MaxInterval: 0},
	}
	waiter := NewQueryWaiter(executor, config)

	result, err := waiter.Wait(context.Background())
	if err != nil {
		t.Fatalf("Wait() error = %v (expected nil; controlled failure via result)", err)
	}
	if result == nil || result.Success {
		t.Fatalf("expected non-success result, got %+v", result)
	}
	if result.FailureReason != "query error" {
		t.Errorf("FailureReason = %q, want %q", result.FailureReason, "query error")
	}
	// Must not retry a query that can never parse.
	if callCount != 1 {
		t.Errorf("expected exactly 1 attempt (no retries), got %d", callCount)
	}
	if !strings.Contains(buf.String(), "not retryable") {
		t.Errorf("expected progress output to explain the query is not retryable, got:\n%s", buf.String())
	}
}

// --- Wait: context timeout ---

func TestWait_ContextTimeout(t *testing.T) {
	executor, cleanup := newWaiterTestExecutor(t, func(w http.ResponseWriter, r *http.Request) {
		// Return empty - condition never met
		response := exec.DQLQueryResponse{
			State:  "SUCCEEDED",
			Result: &exec.DQLResult{Records: []map[string]interface{}{}},
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(response)
	})
	defer cleanup()

	buf := &bytes.Buffer{}
	config := WaitConfig{
		Query:       "fetch logs",
		Condition:   Condition{Type: ConditionTypeCount, Operator: OpGreaterEqual, Value: 100},
		Quiet:       true,
		ProgressOut: buf,
		Timeout:     50, // 50ns — will expire almost immediately
		Backoff:     BackoffConfig{MinInterval: 0, MaxInterval: 0},
	}
	waiter := NewQueryWaiter(executor, config)
	ctx := context.Background()
	result, err := waiter.Wait(ctx)
	// Context timeout returns ctx.Err() — either nil result or timeout error
	if err == nil && result != nil && result.Success {
		t.Error("expected non-success for timed-out wait")
	}
}

// The wait's deadline cancels the query that is running at that moment, rather
// than letting it poll on after the wait has given up (#502).
func TestWait_TimeoutCancelsInFlightQuery(t *testing.T) {
	cancelled := make(chan string, 1)
	executor, cleanup := newWaiterTestExecutor(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasSuffix(r.URL.Path, "query:execute"):
			w.WriteHeader(http.StatusAccepted)
			_, _ = w.Write([]byte(`{"state":"RUNNING","requestToken":"tok-wait"}`))
		case strings.HasSuffix(r.URL.Path, "query:poll"):
			select {
			case <-r.Context().Done():
			case <-time.After(5 * time.Second):
			}
			_, _ = w.Write([]byte(`{"state":"RUNNING","requestToken":"tok-wait"}`))
		case strings.HasSuffix(r.URL.Path, "query:cancel"):
			select {
			case cancelled <- r.URL.Query().Get("request-token"):
			default:
			}
			w.WriteHeader(http.StatusAccepted)
		}
	})
	defer cleanup()

	waiter := NewQueryWaiter(executor, WaitConfig{
		Query:       "fetch logs",
		Condition:   Condition{Type: ConditionTypeCount, Operator: OpGreaterEqual, Value: 1},
		Quiet:       true,
		ProgressOut: &bytes.Buffer{},
		Timeout:     200 * time.Millisecond,
		Backoff:     BackoffConfig{MinInterval: 0, MaxInterval: 0},
	})

	start := time.Now()
	result, err := waiter.Wait(context.Background())
	if elapsed := time.Since(start); elapsed > 3*time.Second {
		t.Fatalf("Wait returned after %s; the running query outlived the deadline", elapsed)
	}
	if err == nil {
		t.Fatal("expected a deadline error")
	}
	if result == nil || result.Success {
		t.Fatalf("result = %+v, want a non-success result", result)
	}
	select {
	case got := <-cancelled:
		if got != "tok-wait" {
			t.Errorf("cancelled token = %q, want %q", got, "tok-wait")
		}
	default:
		t.Error("the running query was never cancelled on the backend")
	}
}

// --- PrintResults ---

func TestPrintResults_EmptyFormat(t *testing.T) {
	executor, cleanup := newWaiterTestExecutor(t, func(w http.ResponseWriter, r *http.Request) {})
	defer cleanup()

	waiter := NewQueryWaiter(executor, WaitConfig{OutputFormat: ""})
	// Should return nil for empty format
	err := waiter.PrintResults(&Result{Records: []map[string]any{{"key": "val"}}})
	if err != nil {
		t.Fatalf("PrintResults() with empty format error = %v", err)
	}
}

func TestPrintResults_NilRecords(t *testing.T) {
	executor, cleanup := newWaiterTestExecutor(t, func(w http.ResponseWriter, r *http.Request) {})
	defer cleanup()

	waiter := NewQueryWaiter(executor, WaitConfig{OutputFormat: "json"})
	// Should return nil for nil records
	err := waiter.PrintResults(&Result{Records: nil})
	if err != nil {
		t.Fatalf("PrintResults() with nil records error = %v", err)
	}
}

func TestPrintResults_JSONFormat(t *testing.T) {
	executor, cleanup := newWaiterTestExecutor(t, func(w http.ResponseWriter, r *http.Request) {})
	defer cleanup()

	waiter := NewQueryWaiter(executor, WaitConfig{OutputFormat: "json"})
	err := waiter.PrintResults(&Result{
		Records: []map[string]any{{"host": "server-01", "status": "ok"}},
	})
	if err != nil {
		t.Fatalf("PrintResults() JSON error = %v", err)
	}
}

// Test verbose mode path
func TestWait_VerboseOutput(t *testing.T) {
	executor, cleanup := newWaiterTestExecutor(t, func(w http.ResponseWriter, r *http.Request) {
		response := exec.DQLQueryResponse{
			State:  "SUCCEEDED",
			Result: &exec.DQLResult{Records: []map[string]interface{}{{"x": 1}}},
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(response)
	})
	defer cleanup()

	buf := &bytes.Buffer{}
	config := WaitConfig{
		Query:       "fetch logs",
		Condition:   Condition{Type: ConditionTypeAny, Operator: OpGreater, Value: 0},
		MaxAttempts: 1,
		Quiet:       false,
		Verbose:     true,
		Timeout:     5000000000, // 5 seconds
		ProgressOut: buf,
		Backoff:     BackoffConfig{MinInterval: 0, MaxInterval: 0},
	}
	waiter := NewQueryWaiter(executor, config)
	ctx := context.Background()
	_, err := waiter.Wait(ctx)
	if err != nil {
		t.Fatalf("Wait() verbose error = %v", err)
	}
	// Verbose mode should have written something to ProgressOut
	output := buf.String()
	if len(output) == 0 {
		t.Error("expected verbose output, got nothing")
	}
	_ = fmt.Sprintf("verbose output: %q", output) // suppress unused
}
