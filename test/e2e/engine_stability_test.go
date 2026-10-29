//go:build integration
// +build integration

package e2e

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/Dynatrace/dtctl/test/integration"
)

// Embedded-engine coverage for the stability floor, against a real tenant and
// through a real server process.
//
// pkg/engine's own tests cover the mechanics in-process. What only this shape
// can answer is the deployment question: when dtctl runs as a service, does
// the floor actually decide what a *request* reaches — including when the
// operator's own shell has DTCTL_MIN_STABILITY set to something else? That
// leak is invisible in-process, because a Go test and the code under test
// share one environment by construction.
//
// Read-only throughout: `inventory` only runs DQL against the tenant, and the
// query budgets are pinned small so a probe cannot turn into a scan.

// serveProcess is a `dtctl serve http` child listening on a loopback port.
type serveProcess struct {
	baseURL string
	cmd     *exec.Cmd
	log     *bytes.Buffer
}

// startServe builds and starts the server, waits for /healthz, and stops it on
// cleanup. hostEnv is injected into the *server* process — the lever these
// tests use to prove a host variable does not reach a request.
func startServe(t *testing.T, exe string, hostEnv map[string]string) *serveProcess {
	t.Helper()

	// An ephemeral port, released immediately: two suites must be able to run
	// side by side without colliding on the default 7211.
	l, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	addr := l.Addr().String()
	require.NoError(t, l.Close())

	env := map[string]string{
		// `serve` is development-tier, so it is not registered without this.
		// That is the tier working as intended: a reference server is exactly
		// the kind of surface that should not appear by default.
		"DTCTL_DEVELOPMENT": "serve",
	}
	for k, v := range hostEnv {
		env[k] = v
	}

	log := &bytes.Buffer{}
	cmd := exec.Command(exe, "serve", "http", "--addr", addr)
	cmd.Dir = "../.."
	cmd.Env = integration.ScrubbedCLIEnviron(env)
	cmd.Stdout = log
	cmd.Stderr = log
	require.NoError(t, cmd.Start())

	s := &serveProcess{baseURL: "http://" + addr, cmd: cmd, log: log}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
	})

	deadline := time.Now().Add(20 * time.Second)
	for {
		resp, err := http.Get(s.baseURL + "/healthz")
		if err == nil {
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return s
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("serve http never became healthy; output:\n%s", log.String())
		}
		time.Sleep(100 * time.Millisecond)
	}
}

type executeResult struct {
	status   int
	body     string
	ExitCode int    `json:"exitCode"`
	Stdout   string `json:"stdout"`
	Stderr   string `json:"stderr"`
}

// combined is stdout plus stderr, which is what a caller actually reads.
func (r executeResult) combined() string { return r.Stdout + r.Stderr }

func (s *serveProcess) execute(t *testing.T, body map[string]any) executeResult {
	t.Helper()
	payload, err := json.Marshal(body)
	require.NoError(t, err)

	resp, err := http.Post(s.baseURL+"/v1/execute", "application/json", bytes.NewReader(payload))
	require.NoError(t, err)
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	require.NoError(t, err)

	out := executeResult{status: resp.StatusCode, body: string(raw)}
	if resp.StatusCode == http.StatusOK {
		require.NoError(t, json.Unmarshal(raw, &out), "body: %s", raw)
	}
	return out
}

// TestEngineStabilityFloorAgainstTenant drives the floor through a real server
// against a real tenant. `inventory` is the subject: classified experimental,
// and it needs no arguments, so "blocked" and "reached the tenant" stay
// cleanly distinguishable.
func TestEngineStabilityFloorAgainstTenant(t *testing.T) {
	envURL, token := liveDebuggerEnv(t)
	exe := integration.BuildDtctl(t)

	// Small budgets: enough to prove the command ran, not enough to scan.
	const probe = "inventory --agent --budget-queries 1 --budget-seconds 15"

	tenant := func(extra map[string]any) map[string]any {
		body := map[string]any{
			"command":        probe,
			"environmentUrl": envURL,
			"token":          token,
			// The probe is read-only; say so at the other axis too, so a
			// regression in one cannot be masked by the other.
			"safetyLevel": "readonly",
		}
		for k, v := range extra {
			body[k] = v
		}
		return body
	}

	t.Run("a request with no floor gets stable and is refused", func(t *testing.T) {
		srv := startServe(t, exe, nil)
		res := srv.execute(t, tenant(nil))

		require.Equal(t, http.StatusOK, res.status, "a refused command is still a 200 with an exit code")
		require.NotZero(t, res.ExitCode)
		require.Contains(t, res.combined(), "stability_blocked")

		// Refused *before* the request: a floor that only checked after
		// connecting would leak the tenant's answer, so no auth or API
		// outcome may appear.
		for _, marker := range []string{"401", "403", "Unauthorized", "dt.system"} {
			require.NotContains(t, res.combined(), marker,
				"the floor must refuse before anything reaches the tenant")
		}
	})

	t.Run("a host env var cannot widen a request", func(t *testing.T) {
		// The operator's own shell says experimental. The request said
		// nothing, so it gets stable — the surface belongs to the request.
		srv := startServe(t, exe, map[string]string{"DTCTL_MIN_STABILITY": "experimental"})
		res := srv.execute(t, tenant(nil))

		require.Equal(t, http.StatusOK, res.status)
		require.Contains(t, res.combined(), "stability_blocked",
			"DTCTL_MIN_STABILITY in the server's environment must not reshape a tenant request")
	})

	t.Run("an exception admits exactly what it names, for real", func(t *testing.T) {
		srv := startServe(t, exe, nil)

		res := srv.execute(t, tenant(map[string]any{
			"stabilityExceptions": []string{"inventory"},
		}))
		require.Equal(t, http.StatusOK, res.status)
		require.NotContains(t, res.combined(), "stability_blocked")
		// Past every narrowing stage and through to the tenant: a real
		// inventory answer, not a usage or auth error.
		require.Zero(t, res.ExitCode, "stdout: %s\nstderr: %s", res.Stdout, res.Stderr)
		require.Contains(t, res.Stdout, `"ok":true`)

		// The command's own flags came with it. They are below the floor only
		// because the command is, so requiring a separate entry for each would
		// make a command-level grant unusable — and would break this caller the
		// next time `inventory` gained a flag.
		require.NotContains(t, res.combined(), "--budget-queries")

		// A subcommand is *not* covered: `inventory arrivals` is a distinct
		// command with its own behaviour, and an audited grant should name it.
		// This is the line between "the flags of what I named" (inherited, so
		// included) and "other commands under what I named" (not).
		res = srv.execute(t, tenant(map[string]any{
			"stabilityExceptions": []string{"inventory"},
			"command":             "inventory arrivals --agent",
		}))
		require.Equal(t, http.StatusOK, res.status)
		require.Contains(t, res.combined(), "stability_blocked",
			"an exception on the parent must not silently grant its subcommands")

		// Naming it works, and the suggestion in the refusal above is exactly
		// what to name.
		res = srv.execute(t, tenant(map[string]any{
			"stabilityExceptions": []string{"inventory arrivals"},
			"command":             "inventory arrivals --agent --budget-queries 1 --budget-seconds 15",
		}))
		require.Equal(t, http.StatusOK, res.status)
		require.NotContains(t, res.combined(), "stability_blocked")
	})

	t.Run("lowering the floor reaches the tenant too", func(t *testing.T) {
		srv := startServe(t, exe, nil)
		res := srv.execute(t, tenant(map[string]any{"minStability": "experimental"}))

		require.Equal(t, http.StatusOK, res.status)
		require.NotContains(t, res.combined(), "stability_blocked")
		require.Zero(t, res.ExitCode, "stdout: %s\nstderr: %s", res.Stdout, res.Stderr)
	})

	t.Run("a floor value that grants nothing is rejected, not honored", func(t *testing.T) {
		srv := startServe(t, exe, nil)

		for _, bad := range []string{"beta", "development"} {
			res := srv.execute(t, tenant(map[string]any{"minStability": bad}))
			require.Equal(t, http.StatusBadRequest, res.status,
				"minStability %q must fail the request, not run it: %s", bad, res.body)
			require.Contains(t, res.body, "invalid minimum stability")
		}
	})

	t.Run("the catalog tells a request its own floor", func(t *testing.T) {
		srv := startServe(t, exe, nil)

		// Without this, an embedded agent reads every refusal as "no such
		// command" and stops asking — the floor becomes indistinguishable
		// from a dtctl that never shipped the feature.
		res := srv.execute(t, tenant(map[string]any{"command": "commands -o json"}))
		require.Equal(t, http.StatusOK, res.status)
		require.Zero(t, res.ExitCode, "stderr: %s", res.Stderr)
		require.Contains(t, res.Stdout, `"min_stability": "stable"`)
		require.NotContains(t, res.Stdout, `"inventory"`)

		res = srv.execute(t, tenant(map[string]any{
			"command":      "commands -o json",
			"minStability": "experimental",
		}))
		require.Equal(t, http.StatusOK, res.status)
		require.Zero(t, res.ExitCode, "stderr: %s", res.Stderr)
		require.Contains(t, res.Stdout, `"min_stability": "experimental"`)
		require.Contains(t, res.Stdout, `"inventory"`)
	})
}

// TestEngineDevelopmentSurfaceIsUnreachableFromARequest: the development tier
// has no per-request field, and the server's own opt-in must not become one.
// This process was started with DTCTL_DEVELOPMENT=serve — if that leaked into
// requests, every development command would be live for every tenant.
func TestEngineDevelopmentSurfaceIsUnreachableFromARequest(t *testing.T) {
	envURL, token := liveDebuggerEnv(t)
	srv := startServe(t, integration.BuildDtctl(t), map[string]string{
		"DTCTL_DEVELOPMENT": "all",
	})

	for _, floor := range []string{"", "experimental"} {
		body := map[string]any{
			"command":        "serve http --agent",
			"environmentUrl": envURL,
			"token":          token,
		}
		if floor != "" {
			body["minStability"] = floor
		}
		res := srv.execute(t, body)
		require.Equal(t, http.StatusOK, res.status)
		require.NotZero(t, res.ExitCode,
			fmt.Sprintf("floor %q must not reach a development command", floor))
		require.NotContains(t, strings.ToLower(res.combined()), "listening")
	}
}
