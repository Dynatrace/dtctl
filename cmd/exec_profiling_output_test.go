package cmd

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Dynatrace/dtctl/pkg/diagnostic"
	"github.com/Dynatrace/dtctl/pkg/output"
	"github.com/Dynatrace/dtctl/pkg/resources/profiling"
	sdkcla "github.com/Dynatrace/dtctl/sdk/api/codelevelanalysis"
	"github.com/Dynatrace/dtctl/sdk/httpclient"
)

const profilingAllocTree = `{"analysisResult":{"stacktreeRootIds":[1],"stacktreeNodes":[` +
	`{"id":1,"name":"Cache.put","childIds":[],"apiInfo":{"systemApi":true},` +
	`"allocationInfo":{"allocationSize":100,"allocationCount":1,"survivorSize":500,"survivorCount":5}}]}}`

// --plain turns a table into JSON for a program to read, so stdout must hold
// one JSON document and nothing else: no folded lines, and none of the # lines
// a table ends with.
func TestExecProfiling_PlainPrintsOnlyJSON(t *testing.T) {
	cases := []struct {
		name, body string
		args       []string
		rows       int
		first      string // the first row, as JSON
	}{
		{"stacks", profilingTree, []string{"hotspots", testProcess}, 5,
			`{"running":30,"lock":0,"net_io":0,"disk_io":0,"wait":0,"stack":"app.Main.main;lib.Loader.load"}`},
		{"stacks, truncated", profilingTree, []string{"hotspots", testProcess, "--limit", "1"}, 1,
			`{"running":30,"lock":0,"net_io":0,"disk_io":0,"wait":0,"stack":"app.Main.main;lib.Loader.load"}`},
		{"functions", profilingTree, []string{"hotspots", testProcess, "--view", "functions", "--limit", "1"}, 1,
			`{"self":50,"selfPct":50,"total":50,"totalPct":50,"paths":2,"function":"lib.Util.wrap"}`},
		{"focus", profilingTree, []string{"hotspots", testProcess, "--focus", "wrap"}, 4,
			`{"relation":"total","samples":50,"share":100,"function":"lib.Util.wrap"}`},
		{"allocations", profilingAllocTree, []string{"allocations", testProcess}, 1,
			`{"alloc_bytes":100,"survived_bytes":500,"alloc_count":1,"survived_count":5,"stack":"Cache.put"}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			newProfilingAPI(t, http.StatusOK, tc.body)
			code, stdout, stderr := runProfilingStreams(t, append(tc.args, "--plain")...)
			require.Zero(t, code, stderr)
			var rows []json.RawMessage
			require.NoError(t, json.Unmarshal([]byte(stdout), &rows), "stdout is not one JSON document:\n%s", stdout)
			require.Len(t, rows, tc.rows)
			require.JSONEq(t, tc.first, string(rows[0]))
		})
	}

	// A view without rows prints the empty list, and says why on stderr.
	newProfilingAPI(t, http.StatusOK, `{"dataRootNodeId":"0","dataNodes":[]}`)
	for _, args := range [][]string{{"hotspots", testProcess}, {"hotspots", testProcess, "--view", "functions"}, {"hotspots", testProcess, "--focus", "f"}} {
		code, stdout, stderr := runProfilingStreams(t, append(args, "--plain")...)
		require.Zero(t, code, stderr)
		require.JSONEq(t, `[]`, stdout, args)
		require.Equal(t, "# no stacks for "+testProcess+": no profiling samples in the window (status=completed) — the process may be idle, restarted, or not under load; try another window (at most 2h), or --raw for the API's whole result\n", stderr)
	}
}

// -o yaml names the fields as -o json and the documentation do.
func TestExecProfiling_YAMLFieldNames(t *testing.T) {
	cases := []struct {
		name, body string
		args       []string
		want       string
	}{
		{"stacks", profilingTree, []string{"hotspots", testProcess, "--limit", "1"},
			"- running: 30\n  lock: 0\n  net_io: 0\n  disk_io: 0\n  wait: 0\n  stack: app.Main.main;lib.Loader.load\n"},
		{"functions", profilingTree, []string{"hotspots", testProcess, "--view", "functions", "--limit", "1"},
			"- self: 50\n  selfPct: 50\n  total: 50\n  totalPct: 50\n  paths: 2\n  function: lib.Util.wrap\n"},
		{"focus", profilingTree, []string{"hotspots", testProcess, "--focus", "main", "--limit", "1"},
			"- relation: total\n  samples: 100\n  share: 100\n  function: app.Main.main\n" +
				"- relation: self\n  samples: 10\n  share: 10\n  function: app.Main.main\n" +
				"- relation: caller\n  samples: 100\n  share: 100\n  function: (root)\n" +
				"- relation: callee\n  samples: 60\n  share: 60\n  function: lib.Loader.load\n"},
		{"allocations", profilingAllocTree, []string{"allocations", testProcess},
			"- alloc_bytes: 100\n  survived_bytes: 500\n  alloc_count: 1\n  survived_count: 5\n  stack: Cache.put\n"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			newProfilingAPI(t, http.StatusOK, tc.body)
			code, stdout, stderr := runProfilingStreams(t, append(tc.args, "-o", "yaml")...)
			require.Zero(t, code, stderr)
			require.Equal(t, tc.want, stdout)
		})
	}
}

// An empty --state is the default, running. A --focus that then matches no
// function, or several, fails like any other: it used to print an empty view
// and exit 0.
func TestExecProfiling_EmptyStateFocusStillHasToMatch(t *testing.T) {
	newProfilingAPI(t, http.StatusOK, profilingTree)

	code, stdout, stderr := runProfilingStreams(t, "hotspots", testProcess, "--state", "", "--focus", "absent")
	require.Equal(t, 1, code)
	require.Empty(t, stdout)
	require.Equal(t, "Error: no function in the profile matches \"absent\" — list them with --view functions\n", stderr)

	code, stdout, stderr = runProfilingStreams(t, "hotspots", testProcess, "--state", "", "--focus", "Main")
	require.Equal(t, 1, code)
	require.Empty(t, stdout)
	require.Equal(t, "Error: \"Main\" matches 2 functions; pass one exactly:\n  app.Main.main\n  app.Main.handle\n", stderr)

	code, stdout, stderr = runProfilingStreams(t, "hotspots", testProcess, "--state", "", "--focus", "absent", "--agent")
	require.Equal(t, 1, code)
	require.Empty(t, stderr)
	require.JSONEq(t, `{"ok":false,"result":null,"error":{"code":"error","message":"no function in the profile matches \"absent\" — list them with --view functions"}}`, stdout)
}

// An end alone in the future is refused as an end in the future. The start is
// then derived from it, and the message used to blame that start.
func TestExecProfiling_EndAloneFarInTheFuture(t *testing.T) {
	api := newProfilingAPI(t, http.StatusOK, profilingTree)
	code, stdout, stderr := runProfilingStreams(t, "hotspots", testProcess, "--default-timeframe-end", "now()+3h")
	require.Equal(t, 1, code)
	require.Empty(t, stdout)
	require.Equal(t, "Error: invalid timeframe: the end is in the future — profiling only covers the past; leave --default-timeframe-end out to end now\n", stderr)
	require.Empty(t, api.paths)
}

// allocations has one view, so its --limit help names no other.
func TestExecProfiling_AllocationsLimitHelp(t *testing.T) {
	require.Equal(t, "cap the rows (0 = all). Default: 100 stacks (20 in agent mode)",
		findProfilingCmd(t, "allocations").Flags().Lookup("limit").Usage)
	require.Equal(t, "cap the rows (0 = all). Default: 100 stacks (20 in agent mode); --view functions 30; --focus 10 callers and 10 callees",
		findProfilingCmd(t, "hotspots").Flags().Lookup("limit").Usage)
}

// Every reason a view can be empty for has its own line, and one the command
// has no words for is shown as it is, not as one of the others.
func TestProfilingEmptyNote(t *testing.T) {
	run := &profilingRun{entity: "PROCESS-1", status: "completed"}
	cases := []struct {
		reason profiling.EmptyReason
		state  string
		want   string
	}{
		{profiling.EmptyNoSamples, "", "no stacks for PROCESS-1: no profiling samples in the window (status=completed) — the process may be idle, restarted, or not under load; try another window (at most 2h), or --raw for the API's whole result"},
		{profiling.EmptyNoSamples, "lock", "no stacks for PROCESS-1: no profiling samples in the window (status=completed) — the process may be idle, restarted, or not under load; try another window (at most 2h), or --raw for the API's whole result"},
		{profiling.EmptyState, "lock", "no lock samples for PROCESS-1 in the window — the stack view (without --view/--focus) shows every thread state; waiting states may need --show-waiting"},
		{profiling.EmptyAppOnly, "", "no stacks for PROCESS-1: --app-only removed every frame (the profiler classified none as application code) — re-run without --app-only"},
		{profiling.EmptyAppOnly, "wait", "no wait samples for PROCESS-1 on application frames: --app-only removed the frames that have them — re-run without --app-only"},
		{"sampling_off", "", "no rows for PROCESS-1 (sampling_off)"},
	}
	for _, tc := range cases {
		require.Equal(t, tc.want, profilingEmptyNote(tc.reason, run, tc.state), "%s, state %q", tc.reason, tc.state)
	}
}

// What the command makes of every reason an analysis can fail for: the advice,
// the exit code and the agent error code. profilingAPIError is a function of
// the error alone, so a throttled analysis is pinned here without the minutes
// of waiting a real one takes.
func TestProfilingAPIError(t *testing.T) {
	api := func(phase sdkcla.Phase, status int) error {
		return &sdkcla.Error{Phase: phase, Err: fmt.Errorf("codelevelanalysis: %s: %w", phase, httpclient.NewAPIError(status, "the API's message", ""))}
	}
	cases := []struct {
		reason      profiling.FailureReason
		err         error
		exit        int
		code        string   // the agent error code; empty when the error passes through
		suggestions []string // nil when the error passes through unchanged
	}{
		{reason: profiling.FailureInvalidInput, exit: 1,
			err: &sdkcla.Error{Phase: sdkcla.PhaseValidate, Err: profiling.ErrWindowEmpty}},
		{reason: profiling.FailureBadRequest, err: api(sdkcla.PhaseSubmit, 400), exit: 1, code: "bad_request", suggestions: []string{
			"The API rejected the request parameters — check the timeframe (in the past, at most 2h) and the entity id",
		}},
		{reason: profiling.FailureForbidden, err: api(sdkcla.PhaseSubmit, 403), exit: 5, code: "permission_denied", suggestions: []string{
			"Profiling needs storage:profiles:read and storage:entities:read, both on the token and in the IAM policy",
			"OAuth (dtctl auth login): log in again so the session requests storage:profiles:read",
		}},
		{reason: profiling.FailureNotFound, err: api(sdkcla.PhaseSubmit, 404), exit: 4, code: "not_found", suggestions: []string{
			"The environment knows no such process (ids are per environment, and short-lived process instances disappear)",
			"List eligible ids: dtctl query 'smartscapeNodes PROCESS | fields id, dt.process_group.detected_name, dt.process_group.id'",
		}},
		{reason: profiling.FailureTaskExpired, err: api(sdkcla.PhasePoll, 404), exit: 4, code: "not_found", suggestions: []string{
			"The analysis was started, but the environment no longer knows its task (it expired, or it ran on a node that is gone) — run the command again",
		}},
		{reason: profiling.FailureThrottled, err: api(sdkcla.PhaseSubmit, 429), exit: 1, code: "rate_limited", suggestions: []string{
			"The environment is throttling profiling requests (it limits how many analyses run at once) — wait and retry, and avoid running many analyses in parallel",
		}},
		// A poll budget that ended throttled is the same failure.
		{reason: profiling.FailureThrottled, err: api(sdkcla.PhasePoll, 429), exit: 1, code: "rate_limited", suggestions: []string{
			"The environment is throttling profiling requests (it limits how many analyses run at once) — wait and retry, and avoid running many analyses in parallel",
		}},
		{reason: profiling.FailureServer, err: api(sdkcla.PhaseSubmit, 503), exit: 1},
		{reason: profiling.FailureTimeout, exit: 1,
			err: &sdkcla.Error{Phase: sdkcla.PhasePoll, Err: fmt.Errorf("%w within 60s (token t1)", sdkcla.ErrNotCompleted)}},
	}
	seen := map[profiling.FailureReason]bool{}
	for _, tc := range cases {
		t.Run(string(tc.reason), func(t *testing.T) {
			f, ok := profiling.ClassifyError(tc.err)
			require.True(t, ok)
			require.Equal(t, tc.reason, f.Reason)
			seen[tc.reason] = true

			got := profilingAPIError(tc.err)
			require.Equal(t, tc.exit, exitCodeForError(got))
			if tc.suggestions == nil {
				require.Same(t, tc.err, got, "an error with nothing profiling-specific to add passes through")
				return
			}
			var diag *diagnostic.Error
			require.True(t, errors.As(got, &diag), "%T", got)
			require.Equal(t, "run profiling analysis", diag.Operation)
			require.Equal(t, "the API's message", diag.Message)
			require.Equal(t, tc.suggestions, diag.Suggestions)
			require.Equal(t, tc.code, output.ClassifyHTTPError(diag.StatusCode))
			require.Same(t, tc.err, diag.Err)
		})
	}
	// Every reason the package names is in the table.
	for _, r := range []profiling.FailureReason{
		profiling.FailureInvalidInput, profiling.FailureBadRequest, profiling.FailureForbidden, profiling.FailureNotFound,
		profiling.FailureTaskExpired, profiling.FailureThrottled, profiling.FailureServer, profiling.FailureTimeout,
	} {
		require.True(t, seen[r], "no case for %s", r)
	}

	// An error that is no failure of an analysis passes through too.
	other := errors.New("boom")
	require.Same(t, other, profilingAPIError(other))
}

// allocations words its failures as hotspots does.
func TestExecProfiling_AllocationsAPIError(t *testing.T) {
	newProfilingAPI(t, http.StatusForbidden, `{"error":{"code":403,"message":"the API's message"}}`)
	code, stdout, stderr := runProfilingStreams(t, "allocations", testProcess)
	require.Equal(t, 5, code)
	require.Empty(t, stdout)
	require.Equal(t, "Error: Failed to run profiling analysis (HTTP 403): the API's message\n\nTroubleshooting suggestions:\n"+
		"  • Profiling needs storage:profiles:read and storage:entities:read, both on the token and in the IAM policy\n"+
		"  • OAuth (dtctl auth login): log in again so the session requests storage:profiles:read\n", stderr)
}

// A table prints the stack views as folded lines: every thread-state count on
// the line of its stack, and for allocations a header that names the sort key
// and says the survived figure is not a size.
func TestFoldProfilingRows(t *testing.T) {
	require.Equal(t, "a.A.run;b.B.wait running=3 lock=4 net_io=5 disk_io=6 wait=7\na.A.run running=1 lock=0 net_io=0 disk_io=0 wait=0\n",
		foldProfilingStacks([]profiling.StackRow{
			{Running: 3, Lock: 4, NetIO: 5, DiskIO: 6, Wait: 7, Stack: "a.A.run;b.B.wait"},
			{Running: 1, Stack: "a.A.run"},
		}))
	require.Empty(t, foldProfilingStacks(nil))

	rows := []profiling.AllocationRow{{AllocBytes: 100, SurvivedBytes: 500, AllocCount: 2, SurvivedCount: 9, Stack: "Cache.put;Svc.get"}}
	const line = "Cache.put;Svc.get alloc=100 surv=500 alloc_count=2 surv_count=9\n"
	require.Equal(t, "# ALLOCATIONS (bytes, sorted by alloc; surv = bytes x GCs survived: not a size, can exceed alloc, not live heap)\n"+line,
		foldProfilingAllocations(rows, false))
	require.Equal(t, "# ALLOCATIONS (bytes, sorted by surv; surv = bytes x GCs survived: not a size, can exceed alloc, not live heap)\n"+line,
		foldProfilingAllocations(rows, true))
	require.Empty(t, foldProfilingAllocations(nil, false))
}

// The table of the allocations command is those lines.
func TestExecProfiling_AllocationsTable(t *testing.T) {
	newProfilingAPI(t, http.StatusOK, profilingAllocTree)
	code, stdout, stderr := runProfilingStreams(t, "allocations", testProcess, "--survivors-only")
	require.Zero(t, code, stderr)
	require.Equal(t, "# ALLOCATIONS (bytes, sorted by surv; surv = bytes x GCs survived: not a size, can exceed alloc, not live heap)\n"+
		"Cache.put alloc=100 surv=500 alloc_count=1 surv_count=5\n", stdout)
}
