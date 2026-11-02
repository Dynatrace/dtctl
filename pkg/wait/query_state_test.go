package wait

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/Dynatrace/dtctl/pkg/exec"
)

// TestWait_FailedStateFailsFast pins that a FAILED query is not retried: the
// backend ran it and it failed, so the identical query fails again.
func TestWait_FailedStateFailsFast(t *testing.T) {
	callCount := 0
	executor, cleanup := newWaiterTestExecutor(t, func(w http.ResponseWriter, r *http.Request) {
		callCount++
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(exec.DQLQueryResponse{State: "FAILED"})
	})
	defer cleanup()

	waiter := NewQueryWaiter(executor, WaitConfig{
		Query:       "fetch logs",
		Condition:   Condition{Type: ConditionTypeAny, Operator: OpGreater, Value: 0},
		MaxAttempts: 5,
		ProgressOut: &bytes.Buffer{},
		Backoff:     BackoffConfig{MinInterval: 0, MaxInterval: 0},
	})

	result, err := waiter.Wait(context.Background())
	if err != nil {
		t.Fatalf("Wait() error = %v", err)
	}
	if result.Success || result.FailureReason != "query error" {
		t.Fatalf("result = %+v, want a non-success query error", result)
	}
	if callCount != 1 {
		t.Errorf("attempts = %d, want exactly 1 (no retries)", callCount)
	}
}

// TestWait_ExpiredResultIsRetried pins the other half: an expired result is
// fixed by exactly the re-run this waiter performs, so it must stay retryable.
func TestWait_ExpiredResultIsRetried(t *testing.T) {
	callCount := 0
	executor, cleanup := newWaiterTestExecutor(t, func(w http.ResponseWriter, r *http.Request) {
		callCount++
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(exec.DQLQueryResponse{State: "RESULT_GONE"})
	})
	defer cleanup()

	waiter := NewQueryWaiter(executor, WaitConfig{
		Query:       "fetch logs",
		Condition:   Condition{Type: ConditionTypeAny, Operator: OpGreater, Value: 0},
		MaxAttempts: 3,
		ProgressOut: &bytes.Buffer{},
		Backoff:     BackoffConfig{MinInterval: 0, MaxInterval: 0},
	})

	if _, err := waiter.Wait(context.Background()); err != nil {
		t.Fatalf("Wait() error = %v", err)
	}
	if callCount != 3 {
		t.Errorf("attempts = %d, want 3 (retried until max attempts)", callCount)
	}
}
