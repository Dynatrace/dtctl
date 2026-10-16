package engine_test

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/dynatrace-oss/dtctl/pkg/aidetect"
	"github.com/dynatrace-oss/dtctl/pkg/engine"
)

// TestEngineOutputEqualsCLI is the E8 acceptance gate: for the same command
// line, tenant, and environment, the engine's stdout/stderr/exit code are
// byte-identical to the real dtctl binary's. This is the whole service
// contract — an agent moving between local CLI and service must not be able
// to tell the difference.
//
// The CLI side runs the actual built binary against a config file pointing at
// the mock environment; the engine side runs in-process with a Session. Only
// the envelope's wall-clock duration field is normalized.
func TestEngineOutputEqualsCLI(t *testing.T) {
	if testing.Short() {
		t.Skip("builds the dtctl binary; skipped in -short mode")
	}
	env := newMockEnv(t)
	exe := buildCLI(t)
	cfgPath := writeCLIConfig(t, env.URL)

	for _, command := range []string{
		"get buckets --agent",
		"get buckets -o json --plain",
		"get buckets --plain",
		"get bucket missing_bucket --agent",
		"totally-unknown-command --agent",
		"config --agent",
	} {
		t.Run(command, func(t *testing.T) {
			cliCode, cliStdout, cliStderr := runCLI(t, exe, cfgPath, command)

			res, err := engine.Execute(context.Background(), engine.Request{
				Command:        command,
				EnvironmentURL: env.URL,
				Token:          "tenant-token",
			})
			require.NoError(t, err)

			// The one deliberate divergence: host-only commands exist locally
			// but are unsupported in the service. Everything else must match.
			if strings.HasPrefix(command, "config") {
				require.NotZero(t, res.ExitCode)
				require.Contains(t, string(res.Stdout), `"code":"unsupported_in_service"`)
				return
			}

			require.Equal(t, cliCode, res.ExitCode, "exit codes must match\nCLI stderr: %s\nengine stderr: %s", cliStderr, res.Stderr)
			require.Equal(t, normalizeEnvelope(cliStdout), normalizeEnvelope(string(res.Stdout)),
				"stdout must be byte-identical to the CLI")
			require.Equal(t, normalizeEnvelope(cliStderr), normalizeEnvelope(string(res.Stderr)),
				"stderr must be byte-identical to the CLI")
		})
	}
}

// buildCLI compiles the real dtctl binary once per test run.
func buildCLI(t *testing.T) string {
	t.Helper()
	name := "dtctl"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	exe := filepath.Join(t.TempDir(), name)
	build := exec.Command("go", "build", "-o", exe, ".")
	build.Dir = "../.." // module root, where package main lives
	out, err := build.CombinedOutput()
	require.NoError(t, err, "go build failed: %s", out)
	return exe
}

// writeCLIConfig writes a single-context config equivalent to what the engine
// synthesizes from the request's Session.
func writeCLIConfig(t *testing.T, envURL string) string {
	t.Helper()
	cfg := fmt.Sprintf(`apiVersion: v1
kind: Config
current-context: session
contexts:
  - name: session
    context:
      environment: %s
      token-ref: session
tokens:
  - name: session
    token: tenant-token
`, envURL)
	path := filepath.Join(t.TempDir(), "config.yaml")
	require.NoError(t, os.WriteFile(path, []byte(cfg), 0o600))
	return path
}

// runCLI executes the built binary with a scrubbed environment: no dtctl or
// credential variables, no AI-agent markers, no color overrides — the same
// neutral conditions the engine guarantees per request.
func runCLI(t *testing.T, exe, cfgPath, command string) (int, string, string) {
	t.Helper()
	args, err := splitTestCommand(command)
	require.NoError(t, err)

	cli := exec.Command(exe, args...)
	cli.Env = scrubbedEnviron(map[string]string{
		"DTCTL_CONFIG":          cfgPath,
		"DTCTL_DISABLE_KEYRING": "1",
	})
	var stdout, stderr bytes.Buffer
	cli.Stdout, cli.Stderr = &stdout, &stderr
	runErr := cli.Run()
	code := 0
	if exitErr, ok := runErr.(*exec.ExitError); ok {
		code = exitErr.ExitCode()
	} else {
		require.NoError(t, runErr)
	}
	return code, stdout.String(), stderr.String()
}

// splitTestCommand splits on spaces — sufficient for the fixed commands above
// (none quote); the engine side uses its real shell-words parser.
func splitTestCommand(command string) ([]string, error) {
	return strings.Fields(command), nil
}

func scrubbedEnviron(extra map[string]string) []string {
	agentVars := map[string]bool{"NO_COLOR": true, "FORCE_COLOR": true}
	for _, v := range aidetect.EnvVars() {
		agentVars[v] = true
	}
	var out []string
	for _, kv := range os.Environ() {
		key, _, _ := strings.Cut(kv, "=")
		if strings.HasPrefix(key, "DTCTL_") || strings.HasPrefix(key, "DT_") ||
			strings.HasPrefix(key, "OTEL_") || agentVars[key] {
			continue
		}
		out = append(out, kv)
	}
	for k, v := range extra {
		out = append(out, k+"="+v)
	}
	return out
}

// TestConcurrentOutputEqualsCLI is the correctness gate for the per-invocation tree: the same corpus as
// TestEngineOutputEqualsCLI, run concurrently with RunOptions.Concurrent set,
// must produce output byte-identical to the serial CLI reference for every
// command. With a per-invocation tree each invocation gets its own fresh tree, so
// different commands running simultaneously have disjoint flag state.
//
// Run with -race to surface any remaining data races in the new code paths.
func TestConcurrentOutputEqualsCLI(t *testing.T) {
	if testing.Short() {
		t.Skip("builds the dtctl binary; skipped in -short mode")
	}
	env := newMockEnv(t)
	exe := buildCLI(t)
	cfgPath := writeCLIConfig(t, env.URL)

	commands := []string{
		"get buckets --agent",
		"get buckets -o json --plain",
		"get buckets --plain",
		"get bucket missing_bucket --agent",
		"totally-unknown-command --agent",
	}

	// Collect serial CLI references first.
	type ref struct {
		code           int
		stdout, stderr string
	}
	refs := make(map[string]ref, len(commands))
	for _, command := range commands {
		code, stdout, stderr := runCLI(t, exe, cfgPath, command)
		refs[command] = ref{code, stdout, stderr}
	}

	// Run all commands concurrently, N rounds each, and compare against refs.
	// An Engine built with WithConcurrentExecution sets RunOptions.Concurrent on each
	// invocation, which builds a fresh tree per call.
	const rounds = 5
	total := len(commands) * rounds
	eng := engine.New(engine.Limits{MaxQueued: total}, engine.WithConcurrentExecution(total))

	var wg sync.WaitGroup
	type result struct {
		command        string
		round          int
		code           int
		stdout, stderr string
		err            error
	}
	results := make(chan result, total)

	for _, command := range commands {
		for round := 0; round < rounds; round++ {
			wg.Add(1)
			go func(cmd string, r int) {
				defer wg.Done()
				res, err := eng.ExecuteWithLimits(context.Background(), engine.Request{
					Command:        cmd,
					EnvironmentURL: env.URL,
					Token:          "tenant-token",
				}, engine.Limits{})
				code := 0
				var stdout, stderr string
				if res != nil {
					code, stdout, stderr = res.ExitCode, string(res.Stdout), string(res.Stderr)
				}
				results <- result{cmd, r, code, stdout, stderr, err}
			}(command, round)
		}
	}
	wg.Wait()
	close(results)

	for res := range results {
		t.Run(fmt.Sprintf("%s/round%d", res.command, res.round), func(t *testing.T) {
			require.NoError(t, res.err)
			r := refs[res.command]
			require.Equal(t, r.code, res.code,
				"exit codes differ\nserial stdout: %s\nconcurrent stdout: %s", r.stdout, res.stdout)
			require.Equal(t, normalizeEnvelope(r.stdout), normalizeEnvelope(res.stdout),
				"stdout differs from serial CLI reference")
			require.Equal(t, normalizeEnvelope(r.stderr), normalizeEnvelope(res.stderr),
				"stderr differs from serial CLI reference")
		})
	}
}

var durationField = regexp.MustCompile(`"duration":"[^"]*"`)

func normalizeEnvelope(s string) string {
	return durationField.ReplaceAllString(s, `"duration":"<normalized>"`)
}
