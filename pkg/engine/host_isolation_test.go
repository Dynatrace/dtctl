package engine_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Dynatrace/dtctl/pkg/engine"
)

// The tests here pin the boundary the engine promises: a request sees its own
// files, its own stdin, and nothing of the host's. Each one corresponds to a
// hole that existed once — a user-named path that reached the host disk one
// package deeper than the cmd/-only guard could see, a spill that outlived the
// request on the server's disk, a nil stdin that fell through to the host's.

// TestExecute_DiffUsesRequestFilesNotHostDisk: `diff -f a -f b` names two files
// the *user* chose, so both resolve in the request. Reading them from the host
// disk instead would turn diff into an arbitrary file-read primitive — the
// unified patch prints the contents it compared.
func TestExecute_DiffUsesRequestFilesNotHostDisk(t *testing.T) {
	// A file that exists only on the host, with a name the request also uses.
	dir := t.TempDir()
	hostPath := filepath.Join(dir, "left.yaml")
	require.NoError(t, os.WriteFile(hostPath, []byte("secret: host-only-value\n"), 0o600))

	// Same basename in the request, different content: if the diff reports the
	// host's value, the seam leaked.
	res, err := engine.Execute(context.Background(), engine.Request{
		Command:        "diff -f left.yaml -f right.yaml --plain",
		EnvironmentURL: "https://x.example.invalid",
		Token:          "t",
		Files: map[string][]byte{
			"left.yaml":  []byte("value: from-request\n"),
			"right.yaml": []byte("value: from-request\n"),
		},
	})
	require.NoError(t, err)
	require.Zero(t, res.ExitCode, "identical request files must diff clean; stderr: %s", res.Stderr)

	combined := string(res.Stdout) + string(res.Stderr)
	require.NotContains(t, combined, "host-only-value")

	// And an absolute host path simply does not exist in the request.
	res, err = engine.Execute(context.Background(), engine.Request{
		Command:        "diff -f " + hostPath + " -f right.yaml --plain",
		EnvironmentURL: "https://x.example.invalid",
		Token:          "t",
		Files:          map[string][]byte{"right.yaml": []byte("value: from-request\n")},
	})
	require.NoError(t, err)
	require.NotZero(t, res.ExitCode, "a host path must not resolve")
	require.NotContains(t, string(res.Stdout)+string(res.Stderr), "host-only-value")
}

// TestExecute_SpillNeverTouchesHostDisk: spilling writes a result file to the
// host and hands back a path. In a service that file would outlive the request
// on the server's disk — readable by whatever runs next — while the path in the
// response means nothing to the caller. The request is refused, and nothing is
// written.
func TestExecute_SpillNeverTouchesHostDisk(t *testing.T) {
	env := newMockEnv(t)
	target := filepath.Join(t.TempDir(), "written", "by-tenant.jsonl")

	res, err := engine.Execute(context.Background(), engine.Request{
		Command:        "query 'fetch logs' --spill-to " + target + " --agent",
		EnvironmentURL: env.URL,
		Token:          "tenant-token",
	})
	require.NoError(t, err)
	require.NotZero(t, res.ExitCode)
	require.Contains(t, string(res.Stdout), `"code":"capability_disabled"`)

	_, statErr := os.Stat(target)
	require.True(t, os.IsNotExist(statErr), "no file may be written to the host disk")
	require.NoDirExists(t, filepath.Dir(target), "no directory may be created on the host disk")
}

// TestExecute_NilStdinIsEOF: a request that sends no stdin gets an empty one,
// not the host process's. Falling through to the host's stdin lets a request
// read whatever the operator's terminal or supervisor pipe holds — and, on a
// pipe that never closes, block forever while holding the invocation lock.
func TestExecute_NilStdinIsEOF(t *testing.T) {
	env := newMockEnv(t)

	r, w, err := os.Pipe()
	require.NoError(t, err)
	defer func() { _ = r.Close() }()

	orig := os.Stdin
	os.Stdin = r
	defer func() { os.Stdin = orig }()

	go func() {
		_, _ = w.Write([]byte("fetch HOST_STDIN_MARKER\n"))
		_ = w.Close()
	}()

	// `query` with no positional argument falls back to reading stdin.
	res, err := engine.Execute(context.Background(), engine.Request{
		Command:        "query --plain",
		EnvironmentURL: env.URL,
		Token:          "tenant-token",
		// Stdin deliberately unset.
	})
	require.NoError(t, err)
	require.NotZero(t, res.ExitCode, "an empty stdin yields no query to run")
	require.NotContains(t, strings.ToUpper(string(res.Stdout)+string(res.Stderr)), "HOST_STDIN_MARKER",
		"the host process's stdin must never reach a request")

	// The host stream is intact for whoever owns it.
	buf := make([]byte, 64)
	n, _ := r.Read(buf)
	require.Contains(t, string(buf[:n]), "HOST_STDIN_MARKER",
		"the request must not have consumed the host's stdin")
}
