package cmd

import (
	"bytes"
	"context"
	"errors"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dynatrace-oss/dtctl/pkg/client"
	"github.com/dynatrace-oss/dtctl/pkg/config"
)

func TestRunConcurrentRequiresSession(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := Run([]string{"version"}, RunOptions{
		Concurrent: true,
		Stdout:     &stdout,
		Stderr:     &stderr,
	})
	if code != client.ExitUsageError {
		t.Fatalf("exit code = %d, want %d (ExitUsageError)", code, client.ExitUsageError)
	}
	if !strings.Contains(stderr.String(), "RunOptions.Concurrent requires a Session") {
		t.Fatalf("stderr = %q, want the options error", stderr.String())
	}
	if stdout.Len() != 0 {
		t.Fatalf("stdout = %q, want nothing: the command must not run", stdout.String())
	}
}

// A concurrent invocation that gives up waiting behind a serialized one leaves
// nothing behind: no goroutine waits on its behalf, and nothing it acquires
// later is left to release. A sync.RWMutex left two goroutines per abandoned
// wait for as long as the serialized invocation ran.
func TestRunLockAbandonedWaitsLeaveNothingBehind(t *testing.T) {
	l := newRunLock()
	l.Lock()

	const abandoned = 50
	before := runtime.NumGoroutine()
	for range abandoned {
		ctx, cancel := context.WithTimeout(context.Background(), time.Millisecond)
		err := l.RLock(ctx)
		cancel()
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("RLock behind an exclusive hold = %v, want DeadlineExceeded", err)
		}
	}
	if left := runtime.NumGoroutine() - before; left > 2 {
		t.Errorf("%d abandoned waits left %d goroutines behind while the serialized invocation still runs", abandoned, left)
	}
	l.Unlock()

	// Nothing was acquired on an abandoned wait's behalf: the lock is free.
	if !l.sem.TryAcquire(runLockExclusive) {
		t.Fatal("the lock is not free after the exclusive hold ended")
	}
	l.Unlock()
}

// A serialized invocation waiting for running concurrent ones holds back the
// concurrent ones that arrive after it, as a waiting writer does for a
// sync.RWMutex, so a steady stream of concurrent requests cannot starve it; the
// held-back ones run once it is done.
func TestRunLockWaitingSerializedHoldsBackLaterConcurrent(t *testing.T) {
	l := newRunLock()
	if err := l.RLock(context.Background()); err != nil {
		t.Fatal(err)
	}

	exclusive := make(chan struct{})
	go func() {
		l.Lock()
		close(exclusive)
	}()
	waitUntil(t, func() bool { return !l.sem.TryAcquire(0) }, "the serialized invocation to queue")

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if err := l.RLock(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("RLock behind a waiting serialized invocation = %v, want DeadlineExceeded", err)
	}

	l.RUnlock()
	select {
	case <-exclusive:
	case <-time.After(5 * time.Second):
		t.Fatal("the serialized invocation never got the lock after the concurrent one finished")
	}
	l.Unlock()
	if err := l.RLock(context.Background()); err != nil {
		t.Fatalf("RLock once the serialized invocation is done = %v", err)
	}
	l.RUnlock()
}

// An uncontended lock is taken whatever the context's state, so a concurrent
// invocation behaves as it did on a sync.RWMutex when no serialized invocation
// is around.
func TestRunLockUncontendedIgnoresTheContext(t *testing.T) {
	l := newRunLock()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := l.RLock(ctx); err != nil {
		t.Fatalf("uncontended RLock with an ended context = %v, want nil", err)
	}
	l.RUnlock()
}

func waitUntil(t *testing.T, cond func() bool, what string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(time.Millisecond)
	}
}

// TestSessionInvocationsReadNoHostConfig holds what a Session promises: a
// session-backed invocation is detached from the host's config, so it does not
// open DTCTL_CONFIG, a .dtctl.yaml above the host's working directory, or the
// global config, not even to discard what it read. An invocation without a
// session still loads it (for its aliases), which is what shows the check
// watches the right call.
func TestSessionInvocationsReadNoHostConfig(t *testing.T) {
	var loads atomic.Int64
	orig := loadHostConfig
	loadHostConfig = func() (*config.Config, error) {
		loads.Add(1)
		return nil, errors.New("no host config in this test")
	}
	t.Cleanup(func() { loadHostConfig = orig })

	session := &Session{EnvironmentURL: "https://env.example.test", Token: "dt0c01.T.T"}
	for _, tc := range []struct {
		name string
		opts RunOptions
	}{
		{"concurrent", RunOptions{Concurrent: true, Session: session}},
		{"serialized", RunOptions{Session: session}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			loads.Store(0)
			var stdout, stderr bytes.Buffer
			tc.opts.Stdout, tc.opts.Stderr = &stdout, &stderr
			if code := Run([]string{"version"}, tc.opts); code != 0 {
				t.Fatalf("exit code = %d, stderr = %q", code, stderr.String())
			}
			if n := loads.Load(); n != 0 {
				t.Errorf("a session-backed invocation loaded the host config %d time(s)", n)
			}
		})
	}

	t.Run("without a session", func(t *testing.T) {
		loads.Store(0)
		var stdout, stderr bytes.Buffer
		Run([]string{"version"}, RunOptions{Stdout: &stdout, Stderr: &stderr})
		if loads.Load() == 0 {
			t.Error("an invocation without a session did not load the host config for its aliases; this test no longer watches the right call")
		}
	})
}
