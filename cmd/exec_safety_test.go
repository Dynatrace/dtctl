package cmd

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Dynatrace/dtctl/pkg/aidetect"
)

// safetyLevelConfig points dtctl at env with the given safety level.
func safetyLevelConfig(env, level string) string {
	return fmt.Sprintf(`current-context: c
contexts:
  - name: c
    context:
      environment: %s
      token-ref: t
      safety-level: %s
tokens:
  - name: t
    token: dt0c01.EXAMPLE
`, env, level)
}

// isolatedContext points dtctl at a live local server whose every response is a
// 404, at the given safety level.
//
// A reachable server matters for the tests that assert an invocation is *not*
// blocked: they need it to fail fast on the platform call instead of burning the
// client's connection-refused retry budget (3×1s per attempt).
func isolatedContext(t *testing.T, level string) {
	t.Helper()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"error":{"code":404,"message":"not found"}}`))
	}))
	t.Cleanup(srv.Close)

	path := filepath.Join(t.TempDir(), "config.yaml")
	require.NoError(t, os.WriteFile(path, []byte(safetyLevelConfig(srv.URL, level)), 0o600))
	t.Setenv("DTCTL_CONFIG", path)
	t.Setenv("DTCTL_PROFILE", "")
	// Suppress agent-mode auto-detection so --agent is the only thing shaping
	// output (mirrors isolatedConfig in run_test.go).
	for _, v := range aidetect.EnvVars() {
		t.Setenv(v, "")
	}
}

// runAgentEnvelope invokes argv with --agent and returns the exit code plus the
// decoded JSON envelope. Agent mode is used because it renders a blocked
// operation as a machine-checkable {"ok":false,"error":{"code":"safety_blocked"}}
// on stdout, rather than prose on stderr.
func runAgentEnvelope(t *testing.T, argv []string) (int, map[string]any) {
	t.Helper()

	code, out := captureRun(t, append(argv, "--agent"), RunOptions{})

	// A few exec commands print a progress line before their envelope, so scan
	// for the last line that parses as JSON rather than decoding the whole
	// stream. (That prose is itself an agent-mode wart, but it is pre-existing
	// and orthogonal to what these tests pin.)
	var env map[string]any
	found := false
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "{") {
			continue
		}
		var candidate map[string]any
		if json.Unmarshal([]byte(line), &candidate) == nil {
			env, found = candidate, true
		}
	}
	require.True(t, found, "expected a JSON agent envelope, got:\n%s", out)
	return code, env
}

// errorCode extracts error.code from an agent envelope, or "" when the envelope
// reports success.
func errorCode(t *testing.T, env map[string]any) string {
	t.Helper()
	if ok, _ := env["ok"].(bool); ok {
		return ""
	}
	errObj, isMap := env["error"].(map[string]any)
	require.True(t, isMap, "a failed envelope must carry an error object: %v", env)
	code, _ := errObj["code"].(string)
	return code
}

// TestExecFunctionAdHocCodeBlockedInReadonly closes the hole that made the
// generic-API work necessary: `pkg/commands/listing.go` declares
// `exec: OperationCreate`, but every exec subcommand used to call SetupClient()
// instead of SetupWithSafety(), so a readonly context did not block ad-hoc
// JavaScript that can POST or DELETE against any platform API the token reaches.
func TestExecFunctionAdHocCodeBlockedInReadonly(t *testing.T) {
	isolatedContext(t, "readonly")

	code, env := runAgentEnvelope(t, []string{
		"exec", "function", "--code", "export default async function() { return 1 }",
	})

	require.NotZero(t, code, "ad-hoc code must not run in a readonly context")
	require.Equal(t, "safety_blocked", errorCode(t, env))
}

// TestExecFunctionAdHocCodeBlockedInReadWriteMine pins the deliberate extra
// strictness: ad-hoc JavaScript is gated as a *delete*, not a create.
//
// AppEngine injects a bearer on relative fetch(), so `--code` can reach any
// endpoint the token can. Gating it as a create would leave it permitted at
// readwrite-mine while `exec api` escalates an unclassifiable request to
// OperationDelete — making that gate theatre, since a caller blocked on
// `exec api` could reach the same endpoint through `--code`.
func TestExecFunctionAdHocCodeBlockedInReadWriteMine(t *testing.T) {
	isolatedContext(t, "readwrite-mine")

	code, env := runAgentEnvelope(t, []string{
		"exec", "function", "--code", "export default async function() { return 1 }",
	})

	require.NotZero(t, code)
	require.Equal(t, "safety_blocked", errorCode(t, env),
		"ad-hoc code is classified as a delete, which readwrite-mine refuses "+
			"against unknown ownership")
}

// TestExecNamedFunctionAllowedInReadWriteMine is the counterpart: a *named* app
// function runs the app's own reviewed code and stays a create, so the extra
// strictness above must not spill onto it.
func TestExecNamedFunctionAllowedInReadWriteMine(t *testing.T) {
	isolatedContext(t, "readwrite-mine")

	_, env := runAgentEnvelope(t, []string{"exec", "function", "myapp/myfunction"})

	require.NotEqual(t, "safety_blocked", errorCode(t, env),
		"invoking a named app function is a create, which readwrite-mine permits")
}

// TestExecReadShapedSubcommandsAllowedInReadonly is the other half of the fix.
// The exec family is declared mutating at the verb level, but several of its
// members are read-shaped POSTs: the platform declares a :read scope for them
// and they persist nothing. Gating the whole family as OperationCreate would
// wrongly block them in a readonly context — the same over-blocking that
// disqualified method-based inference for `exec api`.
//
// These invocations pass the safety gate and then fail on the local server's
// 404, so the assertion is on the error *code*: anything but safety_blocked
// means the gate let them through.
func TestExecReadShapedSubcommandsAllowedInReadonly(t *testing.T) {
	cases := []struct {
		name string
		argv []string
	}{
		{"analyzer", []string{"exec", "analyzer", "dt.statistics.Example", "--query", "timeseries avg(x)"}},
		{"slo", []string{"exec", "slo", "example-slo-id"}},
		{"copilot", []string{"exec", "copilot", "hello"}},
		{"preview-processor", []string{"exec", "preview-processor", "-f", "missing.json"}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			isolatedContext(t, "readonly")

			_, env := runAgentEnvelope(t, tc.argv)

			require.NotEqual(t, "safety_blocked", errorCode(t, env),
				"%s is a read-shaped POST and must not be blocked in a readonly context", tc.name)
		})
	}
}

// TestExecWorkflowBlockedInReadonly pins the genuinely-mutating member: a
// workflow execution runs actions that create and modify resources.
func TestExecWorkflowBlockedInReadonly(t *testing.T) {
	isolatedContext(t, "readonly")

	code, env := runAgentEnvelope(t, []string{"exec", "workflow", "example-workflow-id"})

	require.NotZero(t, code)
	require.Equal(t, "safety_blocked", errorCode(t, env))
}
