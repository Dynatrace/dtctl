package cmd

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/require"

	"github.com/Dynatrace/dtctl/pkg/aidetect"
)

// captureRun invokes Run and returns its exit code plus everything written to
// stdout. Stdout is swapped process-wide, so callers must not run in parallel
// with other stdout-writing tests (cmd tests never do).
func captureRun(t *testing.T, argv []string, opts RunOptions) (int, string) {
	t.Helper()

	orig := os.Stdout
	r, w, err := os.Pipe()
	require.NoError(t, err)
	os.Stdout = w
	done := make(chan string, 1)
	go func() {
		var buf bytes.Buffer
		_, _ = io.Copy(&buf, r)
		done <- buf.String()
	}()

	code := Run(argv, opts)

	_ = w.Close()
	os.Stdout = orig
	return code, <-done
}

// isolatedConfig points DTCTL_CONFIG at a minimal config file in a temp dir so
// Run tests neither read nor write the developer's real config, and disables
// agent auto-detection so output shape is stable under CI agents.
func isolatedConfig(t *testing.T, content string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.yaml")
	require.NoError(t, os.WriteFile(path, []byte(content), 0o600))
	t.Setenv("DTCTL_CONFIG", path)
	t.Setenv("DTCTL_PROFILE", "")
	// Suppress agent-mode auto-detection: the test machine may itself run
	// under an AI agent (any of sdk/agentmode's known env vars), which would
	// silently flip output to envelopes. Empty means "not set" to Detect.
	for _, v := range aidetect.EnvVars() {
		t.Setenv(v, "")
	}
}

// TestRunFlagStateDoesNotLeakBetweenInvocations is the E1 isolation guard: a
// flag set in one invocation (here the root persistent -o) must not change the
// behavior of the next. `commands` is used because it renders purely from the
// command tree — no config or network.
func TestRunFlagStateDoesNotLeakBetweenInvocations(t *testing.T) {
	isolatedConfig(t, "contexts: []\n")

	code, first := captureRun(t, []string{"commands"}, RunOptions{})
	require.Zero(t, code)
	require.Contains(t, first, "tool: dtctl", "default output is TOON")

	code, jsonOut := captureRun(t, []string{"commands", "-o", "json"}, RunOptions{})
	require.Zero(t, code)
	require.True(t, strings.HasPrefix(strings.TrimSpace(jsonOut), "{"),
		"-o json must produce JSON")

	code, again := captureRun(t, []string{"commands"}, RunOptions{})
	require.Zero(t, code)
	require.Equal(t, first, again,
		"a later invocation must not inherit the earlier -o json")
}

// TestRunProfileMaskDoesNotLeakBetweenInvocations: applyProfile destructively
// masks commands (Hidden, RunE overwritten). A profiled invocation must leave
// no trace on the next unprofiled one.
func TestRunProfileMaskDoesNotLeakBetweenInvocations(t *testing.T) {
	isolatedConfig(t, "contexts: []\n")

	t.Setenv("DTCTL_PROFILE", "query")
	code, masked := captureRun(t, []string{"get", "--help"}, RunOptions{})
	require.Zero(t, code)
	require.NotContains(t, availableCommandsSection(t, masked), "workflows",
		"profile 'query' must hide get workflows from help")
	require.Contains(t, availableCommandsSection(t, masked), "analyzers",
		"profile 'query' allows get analyzers")

	t.Setenv("DTCTL_PROFILE", "")
	code, full := captureRun(t, []string{"get", "--help"}, RunOptions{})
	require.Zero(t, code)
	require.Contains(t, availableCommandsSection(t, full), "workflows",
		"after a profiled run, the full tree must be restored")

	// The mask must also be functionally undone, not just cosmetically:
	// invoking a previously-masked command must reach its real RunE (which
	// fails on missing context config, not with a profile block).
	code, out := captureRun(t, []string{"get", "workflows", "--plain"}, RunOptions{})
	require.NotZero(t, code)
	require.NotContains(t, out, "not available",
		"previously-masked command must not still be profile-blocked")
}

// availableCommandsSection extracts the subcommand listing from cobra help
// output. The static Long text of `get` mentions resource names as prose, so
// mask assertions must scope to the actual command list.
func availableCommandsSection(t *testing.T, help string) string {
	t.Helper()
	_, after, found := strings.Cut(help, "Available Commands:")
	require.True(t, found, "help output must contain an Available Commands section:\n%s", help)
	section, _, _ := strings.Cut(after, "Flags:")
	return section
}

// TestRunRestoresCapabilities: Run must restore the process capability set it
// found, whatever it granted for the invocation.
func TestRunRestoresCapabilities(t *testing.T) {
	isolatedConfig(t, "contexts: []\n")

	before := caps
	_, _ = captureRun(t, []string{"commands"}, RunOptions{Capabilities: &Capabilities{}})
	require.Equal(t, before, caps, "Run must restore the prior capability set")
}

// TestRunSerializesConcurrentInvocations: concurrent Run calls must queue, so
// each complete output appears intact (never interleaved). Run with -race this
// also proves the tree state handoff between invocations is race-free.
func TestRunSerializesConcurrentInvocations(t *testing.T) {
	isolatedConfig(t, "contexts: []\n")

	const workers = 4

	orig := os.Stdout
	r, w, err := os.Pipe()
	require.NoError(t, err)
	os.Stdout = w
	done := make(chan string, 1)
	go func() {
		var buf bytes.Buffer
		_, _ = io.Copy(&buf, r)
		done <- buf.String()
	}()

	var wg sync.WaitGroup
	codes := make([]int, workers)
	for i := range codes {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			codes[i] = Run([]string{"commands"}, RunOptions{})
		}(i)
	}
	wg.Wait()

	_ = w.Close()
	os.Stdout = orig
	out := <-done

	for i, code := range codes {
		require.Zero(t, code, "worker %d", i)
	}
	require.Equal(t, workers, strings.Count(out, "tool: dtctl"),
		"each serialized invocation must emit one complete catalog")
}

// TestRunUsesArgvNotOsArgs: embedded invocations must never fall back to the
// host process's os.Args (cobra's default when no args are set).
func TestRunUsesArgvNotOsArgs(t *testing.T) {
	isolatedConfig(t, "contexts: []\n")

	origArgs := os.Args
	os.Args = []string{"dtctl", "get", "workflows"} // would need network if used
	defer func() { os.Args = origArgs }()

	code, out := captureRun(t, []string{"commands"}, RunOptions{})
	require.Zero(t, code)
	require.Contains(t, out, "tool: dtctl")
}

// TestRunExitCodeForUnknownCommand: error paths must come back as exit codes,
// never a process exit (the E2 contract holds through Run).
func TestRunExitCodeForUnknownCommand(t *testing.T) {
	isolatedConfig(t, "contexts: []\n")

	code, _ := captureRun(t, []string{"no-such-command-xyz", "--plain"},
		RunOptions{Capabilities: &Capabilities{}})
	require.NotZero(t, code)
}

// TestRunScopePreflightNotStacked: installScopePreflight wraps every RunE per
// invocation; without the pristine-tree restore the wrappers would stack one
// deeper on every Run. Assert repeated runs stay well-formed and identical.
func TestRunScopePreflightNotStacked(t *testing.T) {
	isolatedConfig(t, "contexts: []\n")

	var outputs []string
	for i := 0; i < 3; i++ {
		code, out := captureRun(t, []string{"commands"}, RunOptions{})
		require.Zero(t, code, "run %d", i)
		outputs = append(outputs, out)
	}
	require.Equal(t, outputs[0], outputs[1])
	require.Equal(t, outputs[1], outputs[2])
}

// TestRunOptionsContextReachesCommandTree: RunOptions.Context must actually
// arrive at a command body via cmd.Context(), not just get stored in runCtx
// and dropped. Regression test for a gap where executeArgs called
// rootCmd.Execute() instead of rootCmd.ExecuteContext(runCtx): every
// cmd.Context() call added for cancellation support (get.go, logs.go,
// wait.go, inventory.go, exec_slos.go, exec_workflows.go) silently read a
// fresh context.Background() instead of the caller's context.
func TestRunOptionsContextReachesCommandTree(t *testing.T) {
	isolatedConfig(t, "contexts: []\n")

	type probeKey struct{}
	var seenValue any
	var seenDone bool

	probe := &cobra.Command{
		Use: "zz-context-probe",
		RunE: func(c *cobra.Command, _ []string) error {
			ctx := c.Context()
			require.NotNil(t, ctx, "cmd.Context() must never be nil after Run")
			seenValue = ctx.Value(probeKey{})
			seenDone = ctx.Err() != nil
			return nil
		},
	}
	rootCmd.AddCommand(probe)
	defer rootCmd.RemoveCommand(probe)

	ctx, cancel := context.WithCancel(context.WithValue(context.Background(), probeKey{}, "marker"))
	cancel() // cancelled up front, so a real propagation failure is unambiguous

	code, _ := captureRun(t, []string{"zz-context-probe"}, RunOptions{Context: ctx})

	require.Zero(t, code)
	require.Equal(t, "marker", seenValue, "RunOptions.Context value must reach cmd.Context()")
	require.True(t, seenDone, "RunOptions.Context cancellation must reach cmd.Context()")
}

// BenchmarkRun measures the per-invocation overhead of the pristine-tree
// restore plus a full offline command dispatch.
func BenchmarkRun(b *testing.B) {
	path := filepath.Join(b.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte("contexts: []\n"), 0o600); err != nil {
		b.Fatal(err)
	}
	b.Setenv("DTCTL_CONFIG", path)

	devNull, err := os.OpenFile(os.DevNull, os.O_WRONLY, 0)
	if err != nil {
		b.Fatal(err)
	}
	defer devNull.Close()
	orig := os.Stdout
	os.Stdout = devNull
	defer func() { os.Stdout = orig }()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if code := Run([]string{"commands"}, RunOptions{}); code != 0 {
			b.Fatalf("exit code %d", code)
		}
	}
}
