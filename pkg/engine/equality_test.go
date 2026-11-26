package engine_test

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Dynatrace/dtctl/pkg/aidetect"
	"github.com/Dynatrace/dtctl/pkg/engine"
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

// TestEngineNeverAppliesARepoScope: a service request has no working
// directory, so an entry of the repository the host process happens to stand
// in must not reach it, least of all its filter segments, which travel
// beside the query where no rewritten text would show them. The CLI in the
// same directory applies the entry; the engine's bytes equal the CLI's with
// the scope turned off, and it reads and sends no segment.
func TestEngineNeverAppliesARepoScope(t *testing.T) {
	if testing.Short() {
		t.Skip("builds the dtctl binary; skipped in -short mode")
	}
	var mu sync.Mutex
	var requests []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		requests = append(requests, r.Method+" "+r.URL.Path+" "+string(body))
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		if strings.HasSuffix(r.URL.Path, "/query:execute") {
			_, _ = w.Write([]byte(`{"state":"SUCCEEDED","result":{"records":[{"content":"hello"}]}}`))
			return
		}
		_, _ = w.Write([]byte(`{"uid":"plain-1","name":"a segment"}`))
	}))
	t.Cleanup(srv.Close)
	sent := func() string {
		mu.Lock()
		defer mu.Unlock()
		all := strings.Join(requests, "\n")
		requests = nil
		return all
	}

	exe := buildCLI(t) // before the chdir: it builds from a relative path
	repo := t.TempDir()
	require.NoError(t, os.Mkdir(filepath.Join(repo, ".git"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(repo, ".dtctl-repo-scope.yaml"), []byte(`environments:
  127.0.0.1:
    - name: checkout
      workloads:
        - cluster-uid: 11111111-2222-3333-4444-555555555555
          namespace: payments
          name: checkout
      segments:
        - id: plain-1
`), 0o644))
	t.Chdir(repo)

	cfgPath := writeCLIConfig(t, srv.URL)
	cli := func(env map[string]string) (int, string, string) {
		c := exec.Command(exe, "query", "fetch logs", "--agent")
		c.Dir = repo
		env["DTCTL_CONFIG"], env["DTCTL_DISABLE_KEYRING"] = cfgPath, "1"
		c.Env = scrubbedEnviron(env)
		var stdout, stderr bytes.Buffer
		c.Stdout, c.Stderr = &stdout, &stderr
		runErr := c.Run()
		code := 0
		if exitErr, ok := runErr.(*exec.ExitError); ok {
			code = exitErr.ExitCode()
		} else {
			require.NoError(t, runErr)
		}
		return code, stdout.String(), stderr.String()
	}

	code, stdout, stderr := cli(map[string]string{})
	require.Zero(t, code, stderr)
	scoped := sent()
	require.Contains(t, scoped, "GET /platform/storage/filter-segments/v1/filter-segments/plain-1", "the CLI applies the entry here")
	require.Contains(t, scoped, `"filterSegments":[{"id":"plain-1"}]`)
	require.Contains(t, stdout, `"repo_scope"`)

	cliCode, cliStdout, cliStderr := cli(map[string]string{"DTCTL_NO_REPO_SCOPE": "1"})
	unscoped := sent()

	// At the default floor the experimental flags are not admitted, which
	// alone keeps a scope from resolving. A request may lower the floor, and
	// then the only thing between it and the host's repository is that a
	// service request has no working directory: both floors are run, so the
	// test fails without either.
	for _, floor := range []string{"", "experimental"} {
		res, err := engine.Execute(context.Background(), engine.Request{
			Command:        `query "fetch logs" --agent`,
			EnvironmentURL: srv.URL,
			Token:          "tenant-token",
			MinStability:   floor,
		})
		require.NoError(t, err)
		fromEngine := sent()
		require.NotContains(t, fromEngine, "filter-segments", floor)
		require.NotContains(t, fromEngine, "filterSegments", floor)
		require.NotContains(t, fromEngine, "k8s.cluster.uid", floor)
		require.Equal(t, unscoped, fromEngine, "the engine sends what the CLI sends with the scope off")
		require.Equal(t, cliCode, res.ExitCode)
		require.Equal(t, normalizeEnvelope(cliStdout), normalizeEnvelope(string(res.Stdout)))
		require.Equal(t, normalizeEnvelope(cliStderr), normalizeEnvelope(string(res.Stderr)))
	}

	// Asked for by name, an entry is an error where it cannot be read: the
	// request is told that it has no working directory, and nothing is sent.
	res, err := engine.Execute(context.Background(), engine.Request{
		Command:        `query "fetch logs" --repo-scope checkout --agent`,
		EnvironmentURL: srv.URL,
		Token:          "tenant-token",
		MinStability:   "experimental",
	})
	require.NoError(t, err)
	require.NotZero(t, res.ExitCode)
	require.Contains(t, string(res.Stdout), `"code":"capability_disabled"`)
	require.Empty(t, sent())
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
	// Outside any git repository, so a repo scope file in the checkout cannot
	// narrow the CLI's queries: the engine never resolves one.
	cli.Dir = t.TempDir()
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
