package cmd

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/spf13/cobra"

	"github.com/dynatrace-oss/dtctl/pkg/client"
)

// errRootHookReached stands in for the root's PersistentPreRunE, so the guard
// below sees which hook a command reaches without running it.
var errRootHookReached = errors.New("root PersistentPreRunE reached")

// TestEveryCommandReachesTheRootPreRunHook holds the rule attachPreviewNotice
// once broke. Cobra runs only the nearest PersistentPreRunE, so a command that
// sets its own drops the root's for every command under it — the --dry-run
// rejection and global-flag validation on both trees, and initConfig on a
// concurrent one — unless it chains up.
func TestEveryCommandReachesTheRootPreRunHook(t *testing.T) {
	trees := map[string]*cobra.Command{
		"singleton":  rootCmd,
		"concurrent": newCommandTree(withInvocation(context.Background(), &invocation{concurrent: true})).root,
	}
	for name, root := range trees {
		t.Run(name, func(t *testing.T) {
			orig := root.PersistentPreRunE
			t.Cleanup(func() { root.PersistentPreRunE = orig })
			root.PersistentPreRunE = func(*cobra.Command, []string) error { return errRootHookReached }

			walkCommands(root, func(c *cobra.Command) {
				if !c.Runnable() {
					return
				}
				hook := nearestPersistentPreRunE(c)
				if hook == nil {
					t.Errorf("%q: no PersistentPreRunE runs", c.CommandPath())
					return
				}
				if err := hook(c, nil); !errors.Is(err, errRootHookReached) {
					t.Errorf("%q: the root's PersistentPreRunE does not run (the nearest hook returned %v)", c.CommandPath(), err)
				}
			})
		})
	}
}

// nearestPersistentPreRunE is the hook cobra runs for c while
// EnableTraverseRunHooks is off: the first one set on c or an ancestor. A
// PersistentPreRun found first yields nil, because it cannot chain to the root.
func nearestPersistentPreRunE(c *cobra.Command) func(*cobra.Command, []string) error {
	for p := c; p != nil; p = p.Parent() {
		switch {
		case p.PersistentPreRunE != nil:
			return p.PersistentPreRunE
		case p.PersistentPreRun != nil:
			return nil
		}
	}
	return nil
}

// TestConcurrentPreviewCommandRejectsUnimplementedDryRun is the case the guard
// above was written for: on a concurrent tree, `get gcp connection --dry-run`
// skipped the root's hook, ignored --dry-run and made the API call.
func TestConcurrentPreviewCommandRejectsUnimplementedDryRun(t *testing.T) {
	var requests atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(srv.Close)

	var stdout, stderr bytes.Buffer
	code := Run([]string{"get", "gcp", "connection", "--dry-run", "--agent"}, RunOptions{
		Concurrent: true,
		Session:    &Session{EnvironmentURL: srv.URL, Token: "dt0c01.T.T"},
		Stdout:     &stdout,
		Stderr:     &stderr,
	})

	if code != client.ExitUsageError {
		t.Errorf("exit code = %d, want %d (ExitUsageError)", code, client.ExitUsageError)
	}
	if !strings.Contains(stdout.String(), `"code":"unknown_command"`) || !strings.Contains(stdout.String(), "dry run") {
		t.Errorf("stdout = %q, want the unimplemented --dry-run usage error", stdout.String())
	}
	if n := requests.Load(); n != 0 {
		t.Errorf("%d API requests made; a rejected command line must not reach the platform", n)
	}
	if strings.Contains(stderr.String(), "[Preview]") {
		t.Errorf("stderr = %q, want no Preview notice for a rejected command line", stderr.String())
	}
}
