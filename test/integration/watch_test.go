package integration

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Dynatrace/dtctl/pkg/watch"
)

func TestWatch_BasicFunctionality(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	callCount := 0
	fetcher := func() (interface{}, error) {
		callCount++
		return []interface{}{
			map[string]interface{}{"id": "1", "name": "test", "status": "RUNNING"},
		}, nil
	}

	watcher := watch.NewWatcher(watch.WatcherOptions{
		Interval:    time.Second,
		Fetcher:     fetcher,
		ShowInitial: true,
	})

	ctx, cancel := context.WithTimeout(context.Background(), 1200*time.Millisecond)
	defer cancel()

	err := watcher.Start(ctx)
	require.NoError(t, err)

	assert.Greater(t, callCount, 1, "Fetcher should be called multiple times")
}

func TestWatch_DetectChanges(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	state := []interface{}{
		map[string]interface{}{"id": "1", "name": "workflow-1", "status": "RUNNING"},
	}

	callCount := 0
	fetcher := func() (interface{}, error) {
		callCount++
		if callCount > 2 {
			state = []interface{}{
				map[string]interface{}{"id": "1", "name": "workflow-1", "status": "FAILED"},
			}
		}
		return state, nil
	}

	watcher := watch.NewWatcher(watch.WatcherOptions{
		Interval:    time.Second,
		Fetcher:     fetcher,
		ShowInitial: true,
	})

	ctx, cancel := context.WithTimeout(context.Background(), 2200*time.Millisecond)
	defer cancel()

	err := watcher.Start(ctx)
	require.NoError(t, err)

	assert.Greater(t, callCount, 2, "Fetcher should be called enough times to detect change")
}

func TestWatch_StopGracefully(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	fetcher := func() (interface{}, error) {
		return []interface{}{
			map[string]interface{}{"id": "1", "name": "test"},
		}, nil
	}

	watcher := watch.NewWatcher(watch.WatcherOptions{
		Interval:    time.Second,
		Fetcher:     fetcher,
		ShowInitial: true,
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan error, 1)
	go func() {
		done <- watcher.Start(ctx)
	}()

	time.Sleep(100 * time.Millisecond)
	watcher.Stop()

	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(500 * time.Millisecond):
		t.Fatal("Watcher did not stop gracefully")
	}
}

func TestWatch_ContextCancellation(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	fetcher := func() (interface{}, error) {
		return []interface{}{
			map[string]interface{}{"id": "1", "name": "test"},
		}, nil
	}

	watcher := watch.NewWatcher(watch.WatcherOptions{
		Interval:    time.Second,
		Fetcher:     fetcher,
		ShowInitial: true,
	})

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	err := watcher.Start(ctx)
	require.NoError(t, err)
}
