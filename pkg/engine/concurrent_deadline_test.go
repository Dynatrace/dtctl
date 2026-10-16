package engine_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/dynatrace-oss/dtctl/pkg/engine"
)

// TestConcurrentDeadlineEndsAStalledRequest holds the design's single deadline:
// the request's context. Nothing preempts in-process code, so a command whose
// upstream never answers stays in its slot until its HTTP call ends, and the
// SDK's own ceiling on that call is six minutes. The context must therefore
// reach every request an invocation sends — including those from handlers that
// call the SDK with context.Background(), which is nearly all of them (`get
// workflows` and `get buckets`). The query execute is the exception, on purpose:
// the SDK detaches it from cancellation for a short grace so that a query it
// started can still be cancelled on the backend (dtctl #621).
func TestConcurrentDeadlineEndsAStalledRequest(t *testing.T) {
	stalled := make(chan struct{})
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-stalled:
		case <-time.After(time.Minute):
		}
	}))
	t.Cleanup(func() {
		close(stalled)
		upstream.Close()
	})

	const budget = 400 * time.Millisecond
	eng := engine.New(engine.Limits{MaxQueued: 8, MaxDuration: time.Minute}, engine.WithConcurrentExecution(4))

	for _, command := range []string{
		"get workflows",       // the handler calls the SDK with context.Background()
		"get buckets",         // likewise
		"get documents",       // a second background-context handler
		"describe workflow x", // a request that is not a plain list
	} {
		t.Run(command, func(t *testing.T) {
			done := make(chan struct{})
			var (
				res *engine.Result
				err error
			)
			start := time.Now()
			go func() {
				defer close(done)
				res, err = eng.ExecuteWithLimits(context.Background(),
					tenantRequest(upstream.URL, "tok", command), engine.Limits{MaxDuration: budget})
			}()

			select {
			case <-done:
			case <-time.After(10 * time.Second):
				t.Fatalf("%q was still running 10s after a %s budget: the deadline does not reach its request", command, budget)
			}

			elapsed := time.Since(start)
			require.Less(t, elapsed, 5*time.Second, "the command outlived its budget by far")
			require.NotNil(t, res, "the command ran, so its Result comes back with the error")
			require.ErrorIs(t, err, context.DeadlineExceeded, "the deadline is reported as the context's error")
		})
	}
}

// TestConcurrentDeadlineKeepsTheResult: a command the deadline cut off returns
// what it wrote alongside the context's error, even when it exits 0.
func TestConcurrentDeadlineKeepsTheResult(t *testing.T) {
	stalled := make(chan struct{})
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/platform/storage/query/v1/query:execute":
			w.WriteHeader(http.StatusAccepted)
			_, _ = w.Write([]byte(`{"state":"RUNNING","requestToken":"tok"}`))
		case "/platform/storage/query/v1/query:poll":
			select {
			case <-r.Context().Done():
			case <-stalled:
			}
			_, _ = w.Write([]byte(`{"state":"RUNNING","requestToken":"tok"}`))
		default:
			w.WriteHeader(http.StatusAccepted)
		}
	}))
	t.Cleanup(func() {
		close(stalled)
		upstream.Close()
	})

	eng := engine.New(engine.Limits{MaxQueued: 2, MaxDuration: time.Minute}, engine.WithConcurrentExecution(2))
	res, err := eng.ExecuteWithLimits(context.Background(),
		tenantRequest(upstream.URL, "tok", `query "fetch logs" --plain`), engine.Limits{MaxDuration: 300 * time.Millisecond})

	require.ErrorIs(t, err, context.DeadlineExceeded)
	require.NotNil(t, res, "the command ran: its Result must come back with the error")
	require.Zero(t, res.ExitCode, "a cancelled query exits 0")
	require.Contains(t, string(res.Stderr), "Query cancelled.")
}

// TestConcurrentWaitForSerializedEndsAtTheDeadline: a concurrent request queued
// behind a serialized invocation gives up at its own deadline without running.
func TestConcurrentWaitForSerializedEndsAtTheDeadline(t *testing.T) {
	entered := make(chan struct{}, 1)
	release := make(chan struct{})
	blocking := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case entered <- struct{}{}:
		default:
		}
		select {
		case <-release:
		case <-time.After(1500 * time.Millisecond):
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	t.Cleanup(blocking.Close)

	var hits atomic.Int64
	idle := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusNotFound)
	}))
	t.Cleanup(idle.Close)

	serialized := make(chan struct{})
	go func() {
		defer close(serialized)
		_, _ = engine.Execute(context.Background(), tenantRequest(blocking.URL, "tok", "get workflows"))
	}()
	t.Cleanup(func() {
		close(release)
		<-serialized
	})
	select {
	case <-entered:
	case <-time.After(10 * time.Second):
		t.Fatal("the serialized invocation never reached its upstream")
	}

	const budget = 200 * time.Millisecond
	eng := engine.New(engine.Limits{MaxQueued: 2, MaxDuration: time.Minute}, engine.WithConcurrentExecution(2))
	start := time.Now()
	res, err := eng.ExecuteWithLimits(context.Background(),
		tenantRequest(idle.URL, "tok", "get workflows"), engine.Limits{MaxDuration: budget})
	elapsed := time.Since(start)

	require.ErrorIs(t, err, context.DeadlineExceeded)
	require.Nil(t, res, "the request never ran")
	require.Less(t, elapsed, budget+500*time.Millisecond, "the wait outlived the request's deadline")
	require.Zero(t, hits.Load(), "a request that never ran reached its upstream")
}
