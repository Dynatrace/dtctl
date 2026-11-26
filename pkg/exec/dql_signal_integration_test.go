//go:build !windows

package exec

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/signal"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/Dynatrace/dtctl/pkg/client"
)

// TestDQLExecutor_SIGINT_CancelsBackendQuery is an integration-style test that
// exercises the full SIGINT -> cancel flow that cmd/query.go wires up: an OS
// signal triggers context cancellation, the in-flight poll is aborted, and a
// best-effort query:cancel is sent to the backend. It uses a real OS signal
// (sent to this process) and a real HTTP server, so it covers the signal
// plumbing that the unit tests skip by calling cancel() directly.
func TestDQLExecutor_SIGINT_CancelsBackendQuery(t *testing.T) {
	pollStarted := make(chan struct{})
	var cancelCalled atomic.Bool
	var cancelToken atomic.Value

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/platform/storage/query/v1/query:execute":
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusAccepted)
			_ = json.NewEncoder(w).Encode(DQLQueryResponse{
				State:        "RUNNING",
				RequestToken: "tok-sigint",
			})
		case "/platform/storage/query/v1/query:poll":
			// Signal that the poll is in-flight so the test can deliver SIGINT,
			// then block long enough for the cancel to propagate.
			select {
			case <-pollStarted:
				// Already signalled by an earlier poll iteration.
			default:
				close(pollStarted)
			}
			select {
			case <-r.Context().Done():
				// Client aborted the request because of cancellation - good.
				return
			case <-time.After(2 * time.Second):
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(DQLQueryResponse{
				State:        "RUNNING",
				RequestToken: "tok-sigint",
			})
		case "/platform/storage/query/v1/query:cancel":
			cancelCalled.Store(true)
			cancelToken.Store(r.URL.Query().Get("request-token"))
			w.WriteHeader(http.StatusOK)
		}
	}))
	defer server.Close()

	c, err := client.NewForTesting(server.URL, "test-token")
	if err != nil {
		t.Fatalf("failed to create client: %v", err)
	}

	// Replicate the signal plumbing from cmd/query.go.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(sigCh)
	go func() {
		<-sigCh
		cancel()
	}()

	type outcome struct {
		result *DQLQueryResponse
		err    error
	}
	done := make(chan outcome, 1)

	executor := NewDQLExecutor(c)
	go func() {
		result, err := executor.ExecuteQueryWithContext(ctx, "fetch logs", DQLExecuteOptions{})
		done <- outcome{result, err}
	}()

	// Wait until the poll is in-flight, then deliver SIGINT to ourselves.
	select {
	case <-pollStarted:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for poll to start")
	}

	if err := syscall.Kill(os.Getpid(), syscall.SIGINT); err != nil {
		t.Fatalf("failed to send SIGINT: %v", err)
	}

	select {
	case o := <-done:
		if o.err != nil {
			t.Fatalf("expected nil error after SIGINT cancellation, got: %v", o.err)
		}
		if o.result != nil {
			t.Errorf("expected nil result after SIGINT cancellation, got: %+v", o.result)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("ExecuteQueryWithContext did not return after SIGINT")
	}

	// The cancel call is a best-effort POST that runs after the poll returns;
	// give the goroutine a moment to issue it.
	deadline := time.Now().Add(2 * time.Second)
	for !cancelCalled.Load() && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if !cancelCalled.Load() {
		t.Fatal("expected /query:cancel to be called after SIGINT")
	}
	if got, _ := cancelToken.Load().(string); got != "tok-sigint" {
		t.Errorf("cancel token = %q, want %q", got, "tok-sigint")
	}
}

// TestDQLExecutor_PollErrorPropagatesWhenCallerCtxAlive verifies that a poll
// failure that is unrelated to caller cancellation (e.g. backend 5xx) is
// surfaced as an error rather than being silently swallowed as a
// "cancelled" outcome.
func TestDQLExecutor_PollErrorPropagatesWhenCallerCtxAlive(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/platform/storage/query/v1/query:execute":
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusAccepted)
			_ = json.NewEncoder(w).Encode(DQLQueryResponse{
				State:        "RUNNING",
				RequestToken: "tok-poll-error",
			})
		case "/platform/storage/query/v1/query:poll":
			// Return a non-cancellation error from the backend.
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`{"error":{"message":"internal"}}`))
		}
	}))
	defer server.Close()

	c, err := client.NewForTesting(server.URL, "test-token")
	if err != nil {
		t.Fatalf("failed to create client: %v", err)
	}

	executor := NewDQLExecutor(c)
	// Caller's context is alive throughout; only the poll fails.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	result, err := executor.ExecuteQueryWithContext(ctx, "fetch logs", DQLExecuteOptions{})
	if err == nil {
		t.Fatal("expected an error when poll fails and caller ctx is alive, got nil")
	}
	if result != nil {
		t.Errorf("expected nil result on poll error, got: %+v", result)
	}
}
