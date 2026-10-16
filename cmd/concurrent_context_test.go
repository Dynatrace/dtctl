package cmd

import (
	"bytes"
	"sync"
	"testing"

	"github.com/spf13/cobra"
)

// TestConcurrentRunNeverLacksACommandContext holds the rule the context carrier
// depends on. A command finds its invocation through cmd.Context(), which cobra
// sets only as a command executes; code that prepares a command before that —
// a constructor binding a flag, the stages that mask the tree — has no context
// to ask, and would silently bind the process's storage instead of the
// invocation's (it did, for get's --limit/--fields, and two invocations raced
// on it). newCommandTree gives every command the invocation's context once the
// tree is built, so any command asked for its context during a concurrent run
// must already have one.
func TestConcurrentRunNeverLacksACommandContext(t *testing.T) {
	var mu sync.Mutex
	var missing []string
	onMissingCommandContext = func(c *cobra.Command) {
		mu.Lock()
		defer mu.Unlock()
		missing = append(missing, c.CommandPath())
	}
	t.Cleanup(func() { onMissingCommandContext = nil })

	var wg sync.WaitGroup
	for _, argv := range [][]string{
		{"version", "--agent"},
		{"commands", "--brief"},
		{"get", "workflows", "--help"},
		{"delete", "workflow", "wf-1", "--dry-run", "--agent"},
		{"query", "--help"},
		{"get", "buckets", "--limit", "1", "--fields", "bucketName", "--no-such-flag"},
	} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			var stdout, stderr bytes.Buffer
			Run(argv, RunOptions{
				Concurrent: true,
				Session:    &Session{EnvironmentURL: "https://env.example.test", Token: "dt0c01.T.T"},
				Stdout:     &stdout,
				Stderr:     &stderr,
			})
		}()
	}
	wg.Wait()

	if len(missing) > 0 {
		t.Fatalf("commands asked for their context during a concurrent run and had none: %v", missing)
	}
}
