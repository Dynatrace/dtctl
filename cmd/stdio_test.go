package cmd

import (
	"bytes"
	"context"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestRunRedirectedStdout: with RunOptions.Stdout set, the invocation's
// complete output lands in the caller's writer and the process streams are
// restored afterwards.
func TestRunRedirectedStdout(t *testing.T) {
	isolatedConfig(t, "contexts: []\n")
	origOut, origErr := os.Stdout, os.Stderr

	var stdout, stderr bytes.Buffer
	code := Run([]string{"commands"}, RunOptions{Stdout: &stdout, Stderr: &stderr})
	require.Zero(t, code)
	require.Contains(t, stdout.String(), "tool: dtctl")
	require.Same(t, origOut, os.Stdout, "process stdout must be restored")
	require.Same(t, origErr, os.Stderr, "process stderr must be restored")
}

// TestRedirectStdioLargeOutput: writes far beyond the OS pipe buffer (64KB on
// Linux) must be drained concurrently — no deadlock — and arrive complete and
// in order after cleanup.
func TestRedirectStdioLargeOutput(t *testing.T) {
	var stdout bytes.Buffer
	cleanup, err := redirectStdio(context.Background(), &stdout, nil, nil)
	require.NoError(t, err)

	const chunk = "0123456789abcdef"
	const chunks = 1 << 16 // 1 MiB total
	for i := 0; i < chunks; i++ {
		_, err := os.Stdout.WriteString(chunk)
		require.NoError(t, err)
	}
	cleanup()

	require.Equal(t, len(chunk)*chunks, stdout.Len(),
		"every byte must be drained before cleanup returns")
	require.True(t, strings.HasSuffix(stdout.String(), chunk))
}

// TestRunRedirectedStreamsSeparated: stdout carries the structured result,
// stderr the human messages — the separation must survive redirection.
func TestRunRedirectedStreamsSeparated(t *testing.T) {
	env := newSessionMockEnv(t)
	clearAgentEnvVars(t)

	var stdout, stderr bytes.Buffer
	code := Run(
		[]string{"create", "bucket", "--name", "b", "--table", "logs", "--retention", "35", "--plain"},
		RunOptions{
			Session: &Session{EnvironmentURL: env.URL, Token: "t"},
			Stdout:  &stdout,
			Stderr:  &stderr,
		})
	require.Zero(t, code)
	require.Contains(t, stderr.String(), "created",
		"human success message goes to stderr")
	require.NotContains(t, stdout.String(), "created",
		"stdout must not carry the human message")
}

// TestRedirectStdioStdin: the mechanism-level check that os.Stdin serves the
// caller's reader during the redirection window and is restored after.
func TestRedirectStdioStdin(t *testing.T) {
	orig := os.Stdin
	cleanup, err := redirectStdio(context.Background(), nil, nil, strings.NewReader("from-the-request"))
	require.NoError(t, err)

	data, err := io.ReadAll(os.Stdin)
	require.NoError(t, err)
	require.Equal(t, "from-the-request", string(data),
		"os.Stdin must serve the request reader until EOF")

	cleanup()
	require.Same(t, orig, os.Stdin, "process stdin must be restored")
}

// TestRedirectStdioUnreadStdin: a command that never touches stdin must not
// wedge the cleanup path.
func TestRedirectStdioUnreadStdin(t *testing.T) {
	cleanup, err := redirectStdio(context.Background(), nil, nil, strings.NewReader(strings.Repeat("x", 1<<20)))
	require.NoError(t, err)
	cleanup() // must return promptly with the feeder goroutine unblocked
}
