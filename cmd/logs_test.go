package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/dynatrace-oss/dtctl/pkg/client"
	"github.com/dynatrace-oss/dtctl/pkg/output"
)

// newTaskLogEnv serves execution exec-1 with tasks step1 and step2. step1's log
// is always fetched; step2's log answers with step2Status. The execution
// reports RUNNING for the first runningPolls state checks and SUCCESS after.
func newTaskLogEnv(t *testing.T, step2Status int, runningPolls int32) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	return newRecoveringTaskLogEnv(t, step2Status, runningPolls, -1)
}

// newRecoveringTaskLogEnv is newTaskLogEnv whose step2 log fails only for its
// first step2Failures fetches (-1: always) and then answers "step2 output".
func newRecoveringTaskLogEnv(t *testing.T, step2Status int, runningPolls, step2Failures int32) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var stateChecks, step2Fetches atomic.Int32
	now := time.Now().UTC().Format(time.RFC3339)
	later := time.Now().Add(time.Second).UTC().Format(time.RFC3339)
	const base = "/platform/automation/v1/executions/exec-1"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case base:
			state := "SUCCESS"
			if stateChecks.Add(1) <= runningPolls {
				state = "RUNNING"
			}
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprintf(w, `{"id":"exec-1","state":%q,"startedAt":%q}`, state, now)
		case base + "/log":
			fmt.Fprint(w, `"workflow log\n"`)
		case base + "/tasks":
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprintf(w, `{"step1":{"id":"t1","name":"step1","state":"SUCCESS","startedAt":%q},`+
				`"step2":{"id":"t2","name":"step2","state":"SUCCESS","startedAt":%q}}`, now, later)
		case base + "/tasks/step1/log":
			fmt.Fprint(w, `"step1 output\n"`)
		case base + "/tasks/step2/log":
			if n := step2Fetches.Add(1); step2Failures >= 0 && n > step2Failures {
				fmt.Fprint(w, `"step2 output\n"`)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(step2Status)
			fmt.Fprint(w, `{"error":{"message":"step2 refused"}}`)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	return srv, &stateChecks
}

func runLogs(t *testing.T, srv *httptest.Server, argv ...string) (code int, stdout, stderr string) {
	t.Helper()
	clearAgentEnvVars(t)
	t.Cleanup(func() { restorePristineTree(context.Background()) })
	var errBuf bytes.Buffer
	code, stdout = captureRun(t, argv, RunOptions{
		Session: &Session{EnvironmentURL: srv.URL, Token: "t", MinStability: "experimental"},
		Stderr:  &errBuf,
	})
	return code, stdout, errBuf.String()
}

// A task log that cannot be fetched fails the command, but only after the logs
// that were fetched are printed: stdout is what it always was.
func TestLogsWorkflowExecution_TaskLogFailureFailsAfterPrinting(t *testing.T) {
	for _, flag := range []string{"--tasks", "--all"} {
		t.Run(flag, func(t *testing.T) {
			srv, _ := newTaskLogEnv(t, http.StatusForbidden, 0)

			code, stdout, stderr := runLogs(t, srv, "logs", "wfe", "exec-1", flag, "--no-agent")

			require.Equal(t, client.ExitError, code, stdout)
			require.Contains(t, stdout, "=== Task: step1 [SUCCESS] ===\nstep1 output\n")
			require.Contains(t, stdout, "=== Task: step2 [SUCCESS] ===\n(failed to fetch log:")
			require.Contains(t, stderr, "could not fetch the log of 1 of 2 tasks in execution exec-1")
			require.Contains(t, stderr, "step2")
		})
	}
}

func TestLogsWorkflowExecution_TaskLogFailureAgentEnvelope(t *testing.T) {
	srv, _ := newTaskLogEnv(t, http.StatusForbidden, 0)

	code, stdout, _ := runLogs(t, srv, "-A", "logs", "wfe", "exec-1", "--tasks")

	require.Equal(t, client.ExitError, code, stdout)
	require.Contains(t, stdout, "step1 output")
	lines := strings.Split(strings.TrimRight(stdout, "\n"), "\n")
	var resp output.Response
	require.NoError(t, json.Unmarshal([]byte(lines[len(lines)-1]), &resp), stdout)
	require.False(t, resp.OK)
	require.NotNil(t, resp.Error)
	require.Equal(t, "task_log_unavailable", resp.Error.Code)
	require.Contains(t, resp.Error.Message, "step2")
	require.NotEmpty(t, resp.Error.Suggestions)
}

// A 404 for one task, while another task's log was fetched, keeps today's
// behavior: marked inline, exit 0.
func TestLogsWorkflowExecution_LoneNotFoundStillSucceeds(t *testing.T) {
	srv, _ := newTaskLogEnv(t, http.StatusNotFound, 0)

	code, stdout, _ := runLogs(t, srv, "logs", "wfe", "exec-1", "--tasks", "--no-agent")

	require.Zero(t, code, stdout)
	require.Contains(t, stdout, "step1 output")
}

// Under --follow a failed task log is a warning, not the end of the stream;
// the command only fails if the log is still incomplete when the execution
// has finished.
func TestLogsWorkflowExecution_FollowWarnsAndKeepsStreaming(t *testing.T) {
	orig := followPollInterval
	followPollInterval = time.Millisecond
	t.Cleanup(func() { followPollInterval = orig })
	srv, stateChecks := newTaskLogEnv(t, http.StatusForbidden, 2)

	code, stdout, stderr := runLogs(t, srv, "logs", "wfe", "exec-1", "--tasks", "--follow", "--no-agent")

	require.Equal(t, int32(3), stateChecks.Load(), "the stream must survive the failed fetches")
	require.Contains(t, stdout, "step1 output")
	require.Contains(t, stdout, "--- Execution SUCCESS")
	require.Equal(t, 1, strings.Count(stderr, "Warning:"), "one warning per distinct failure, not per poll:\n%s", stderr)
	require.Equal(t, client.ExitError, code, stdout)
	require.Contains(t, stderr, "could not fetch the log of 1 of 2 tasks")
}

// A task log that failed on an earlier poll and is fetched later changes the
// text in the middle. The stream cannot take back what it printed, so it prints
// the changed task again, header first, instead of a misaligned tail.
func TestLogsWorkflowExecution_FollowReprintsRecoveredTask(t *testing.T) {
	orig := followPollInterval
	followPollInterval = time.Millisecond
	t.Cleanup(func() { followPollInterval = orig })
	srv, _ := newRecoveringTaskLogEnv(t, http.StatusForbidden, 1, 1)

	code, stdout, _ := runLogs(t, srv, "logs", "wfe", "exec-1", "--tasks", "--follow", "--no-agent")

	require.Zero(t, code, stdout)
	require.Contains(t, stdout, "=== Task: step2 [SUCCESS] ===\n(failed to fetch log:")
	require.Contains(t, stdout, "=== Task: step2 [SUCCESS] ===\nstep2 output\n")
	require.Equal(t, 1, strings.Count(stdout, "step1 output"), "unchanged tasks are not printed again:\n%s", stdout)
}

func TestNextFollowChunk(t *testing.T) {
	const a = "=== Task: a [SUCCESS] ===\nA\n"
	const failedB = "\n=== Task: b [RUNNING] ===\n(failed to fetch log: boom)\n"
	const b = "\n=== Task: b [RUNNING] ===\nB\n"
	tests := []struct {
		name, printed, logs, want string
	}{
		{"first poll", "", a, a},
		{"appended", a, a + b, b},
		{"no change", a + b, a + b, ""},
		{"shorter, still a prefix", a + b, a, ""},
		{"task section replaced", a + failedB, a + b, "=== Task: b [RUNNING] ===\nB\n"},
		{"first section replaced", "=== Task: a [RUNNING] ===\nx\n", a, a},
		{"diverged in the first line", "=== Task: a", "xyz", "xyz"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, nextFollowChunk(tt.printed, tt.logs))
		})
	}
}
