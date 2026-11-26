package cmd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
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

			// Naming the table beside --plain changes nothing: -o table and
			// -o wide are the table --plain turns into JSON.
			for _, format := range []string{"table", "wide"} {
				code, got, stderr := runProfilingStreams(t, append(tc.args, "--plain", "-o", format)...)
				require.Zero(t, code, stderr)
				require.Empty(t, stderr)
				require.Equal(t, stdout, got, "-o %s", format)
			}
		})
	}

	// A view without rows prints the empty list, and says why on stderr.
	newProfilingAPI(t, http.StatusOK, `{"dataRootNodeId":"0","dataNodes":[]}`)
	for _, args := range [][]string{{"hotspots", testProcess}, {"hotspots", testProcess, "--view", "functions"}, {"hotspots", testProcess, "--focus", "f"}} {
		code, stdout, stderr := runProfilingStreams(t, append(args, "--plain")...)
		require.Zero(t, code, stderr)
		require.JSONEq(t, `[]`, stdout, args)
		const note = "# no stacks for " + testProcess + ": no profiling samples in the window (status=completed) — the process may be idle, restarted, or not under load; try another window (at most 2h), or --raw for the API's whole result\n"
		require.Equal(t, note, stderr)
		for _, format := range []string{"table", "wide"} {
			code, got, stderr := runProfilingStreams(t, append(args, "--plain", "-o", format)...)
			require.Zero(t, code, stderr)
			require.Equal(t, stdout, got, "-o %s", format)
			require.Equal(t, note, stderr, "-o %s", format)
		}
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
			err: &sdkcla.Error{Phase: sdkcla.PhasePoll, Err: fmt.Errorf("%w within 60s (token config-timeout)", sdkcla.ErrNotCompleted)}},
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
			if tc.reason == profiling.FailureTimeout {
				// The text is the error's own, and the agent code does not
				// follow the token it quotes.
				require.Equal(t, tc.err.Error(), got.Error())
				require.ErrorIs(t, got, tc.err)
				require.Equal(t, "error", errorToDetail(context.Background(), got).Code)
				return
			}
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
			// The envelope the command prints for it in agent mode.
			require.Equal(t, &output.ErrorDetail{
				Code:        tc.code,
				Message:     "the API's message",
				Operation:   "run profiling analysis",
				StatusCode:  diag.StatusCode,
				Suggestions: tc.suggestions,
			}, errorToDetail(context.Background(), got))
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

// An empty --state counts the running samples, as leaving the flag out does.
// It used to count no state at all, and reported a busy process as one
// without samples.
func TestExecProfiling_EmptyStateIsRunning(t *testing.T) {
	newProfilingAPI(t, http.StatusOK, profilingTree)

	code, want, stderr := runProfilingStreams(t, "hotspots", testProcess, "--view", "functions")
	require.Zero(t, code, stderr)
	code, stdout, stderr := runProfilingStreams(t, "hotspots", testProcess, "--view", "functions", "--state", "")
	require.Zero(t, code, stderr)
	require.Empty(t, stderr)
	require.Equal(t, want, stdout)
	require.Contains(t, stdout, "# 4 of 4 functions, by self running samples (100 running samples in the window)\n")

	env := runProfilingAgent(t, "hotspots", testProcess, "--view", "functions", "--state", "")
	require.Len(t, env.Result, 4)
	require.Equal(t, "running", env.Context.Profile.State)
}

// A completed analysis carries a JSON object. Any other JSON value is a result
// the command cannot read, and it fails saying so, in every view and with
// --raw. It used to print the note of a process without samples and exit 0,
// which is a different finding.
func TestExecProfiling_ResultThatIsNoObject(t *testing.T) {
	check := func(t *testing.T, is string, args ...string) {
		t.Helper()
		want := "profiling: malformed analysis result: the result of a completed analysis is " + is + ", not an object"
		code, stdout, stderr := runProfilingStreams(t, args...)
		require.Equal(t, 1, code, args)
		require.Empty(t, stdout, args)
		require.Equal(t, "Error: "+want+"\n", stderr, args)

		code, stdout, stderr = runProfilingStreams(t, append(args, "--agent")...)
		require.Equal(t, 1, code, args)
		require.Empty(t, stderr, args)
		require.Equal(t, profilingErrorEnvelope{Error: profilingEnvelopeError{Code: "error", Message: want}}, decodeProfilingError(t, stdout), args)
	}

	for _, tc := range []struct{ body, is string }{
		{`[]`, "an array"},
		{`null`, "null"},
		{`"done"`, "a string"},
		{`3`, "a number"},
		{`true`, "a boolean"},
	} {
		t.Run("inline "+tc.body, func(t *testing.T) {
			newProfilingAPI(t, http.StatusOK, tc.body)
			check(t, tc.is, "hotspots", testProcess)
			check(t, tc.is, "hotspots", testProcess, "--view", "functions")
			check(t, tc.is, "hotspots", testProcess, "--focus", "wrap")
			check(t, tc.is, "hotspots", testProcess, "--raw")
			check(t, tc.is, "allocations", testProcess)
			check(t, tc.is, "allocations", testProcess, "--raw")
		})
	}
	// The result of a poll is read the same way.
	t.Run("polled", func(t *testing.T) {
		newProfilingPollAPI(t, http.StatusOK, `[]`)
		check(t, "an array", "hotspots", testProcess)
		check(t, "an array", "allocations", testProcess, "--raw")
	})
}

// A 204 is the API's answer when there is nothing to analyse. It ends the
// analysis as one without data whether it answers a poll or the submit itself:
// the submit also carries a result when it answers 200. On the submit it used
// to fail as "submit returned 202 without a progress token".
func TestExecProfiling_NoContentIsNoData(t *testing.T) {
	const note = "no stacks for " + testProcess + ": no profiling samples in the window (status=no-data) — the process may be idle, restarted, or not under load; try another window (at most 2h), or --raw for the API's whole result"
	table := func(t *testing.T, args ...string) {
		t.Helper()
		code, stdout, stderr := runProfilingStreams(t, args...)
		require.Zero(t, code, args)
		require.Empty(t, stderr, args)
		require.Equal(t, "# "+note+"\n", stdout, args)
	}
	rawAndAgent := func(t *testing.T, polls int) {
		t.Helper()
		code, stdout, stderr := runProfilingStreams(t, "hotspots", testProcess, "--raw")
		require.Zero(t, code)
		require.Empty(t, stderr)
		require.JSONEq(t, fmt.Sprintf(`{"status":"no-data","kind":"methodHotspots","entityId":%q,"pollCount":%d,"result":null}`, testProcess, polls), stdout)

		env := runProfilingAgent(t, "hotspots", testProcess)
		require.Empty(t, env.Result)
		require.Equal(t, 0, *env.Context.Total)
		require.Equal(t, []string{note}, env.Context.Warnings)
		require.Equal(t, "no-data", env.Context.Profile.Status)
		require.Nil(t, env.Context.Profile.Samples)
	}

	t.Run("submit", func(t *testing.T) {
		api := newProfilingAPI(t, http.StatusNoContent, "")
		table(t, "hotspots", testProcess)
		table(t, "hotspots", testProcess, "--view", "functions")
		table(t, "hotspots", testProcess, "--focus", "wrap")
		table(t, "allocations", testProcess)
		rawAndAgent(t, 0)
		require.Len(t, api.paths, 6, "a submit without content is not polled")
	})
	t.Run("poll", func(t *testing.T) {
		newProfilingPollAPI(t, http.StatusNoContent, "")
		table(t, "hotspots", testProcess)
		rawAndAgent(t, 1)
	})
}

// The epoch is an instant like any other, so a window that starts or ends at
// epoch millisecond 0 is analysed. It used to be refused before any request,
// as bounds that were not given.
func TestExecProfiling_WindowAtTheEpoch(t *testing.T) {
	const stacks = "app.Main.main;lib.Loader.load running=30 lock=0 net_io=0 disk_io=0 wait=0\n" +
		"app.Main.main;lib.Loader.load;lib.Util.wrap running=30 lock=0 net_io=0 disk_io=0 wait=0\n" +
		"app.Main.main;app.Main.handle;lib.Util.wrap running=20 lock=5 net_io=0 disk_io=0 wait=0\n" +
		"app.Main.main running=10 lock=0 net_io=0 disk_io=0 wait=0\n" +
		"app.Main.main;app.Main.handle running=10 lock=0 net_io=0 disk_io=0 wait=0\n"
	cases := []struct {
		name  string
		args  []string
		query string
	}{
		{"starts there", []string{"--default-timeframe-start", "0", "--default-timeframe-end", "60000"}, "from=0&to=60000"},
		{"starts there, no end", []string{"--default-timeframe-start", "0"}, "from=0&to=7200000"},
		{"ends there", []string{"--default-timeframe-start=-60000", "--default-timeframe-end", "0"}, "from=-60000&to=0"},
		{"ends there, no start", []string{"--default-timeframe-end", "0"}, "from=-7200000&to=0"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			api := newProfilingAPI(t, http.StatusOK, profilingTree)
			code, stdout, stderr := runProfilingStreams(t, append([]string{"hotspots", testProcess}, tc.args...)...)
			require.Zero(t, code, stderr)
			require.Empty(t, stderr)
			require.Equal(t, stacks, stdout)
			require.Equal(t, []string{tc.query}, api.queries)
		})
	}

	newProfilingAPI(t, http.StatusOK, profilingTree)
	env := runProfilingAgent(t, "hotspots", testProcess, "--default-timeframe-start", "0", "--default-timeframe-end", "60000")
	require.Len(t, env.Result, 5)
	require.Equal(t, "1970-01-01T00:00:00Z", env.Context.Window.From)
	require.Equal(t, "1970-01-01T00:01:00Z", env.Context.Window.To)
	require.Equal(t, "1m", env.Context.Window.Span)

	// Both bounds there is still no window.
	api := newProfilingAPI(t, http.StatusOK, profilingTree)
	code, stdout, stderr := runProfilingStreams(t, "hotspots", testProcess, "--default-timeframe-start", "0", "--default-timeframe-end", "0")
	require.Equal(t, 1, code)
	require.Empty(t, stdout)
	require.Equal(t, "Error: invalid timeframe: the end must be after the start\n", stderr)
	require.Empty(t, api.paths)
}

// The agent error code of a failure the command words itself is decided where
// the failure is recognised. Its text quotes what the caller typed and the
// function names of the profile, and a code read off that text would follow
// them: --focus Config was a config_error, --focus Timeout a timeout.
func TestExecProfiling_AgentCodeIgnoresWhatTheMessageQuotes(t *testing.T) {
	// Two functions whose names hold a word the generic classification reads.
	const configured = `{"dataRootNodeId":"0","dataNodes":[
 {"id":"0","className":"","methodName":"","samples":{"RUNNING":0},"childIds":["A","B"]},
 {"id":"A","className":"app.Configuration","methodName":"load","samples":{"RUNNING":7},"childIds":[]},
 {"id":"B","className":"app.Configuration","methodName":"timeout","samples":{"RUNNING":3},"childIds":[]}]}`
	api := newProfilingAPI(t, http.StatusOK, configured)

	cases := []struct {
		name string
		args []string
		code string
	}{
		{"focus matches nothing", []string{"hotspots", testProcess, "--focus", "Config.absent"}, "error"},
		{"focus matches nothing, timeout", []string{"hotspots", testProcess, "--focus", "request timed out"}, "error"},
		{"focus matches nothing, invalid", []string{"hotspots", testProcess, "--focus", "Validation.invalid"}, "error"},
		{"focus matches nothing, context", []string{"hotspots", testProcess, "--focus", "no context"}, "error"},
		{"focus matches several", []string{"hotspots", testProcess, "--focus", "app"}, "error"},
		{"entity", []string{"hotspots", "SERVICE-config"}, "validation_error"},
		{"leaf type", []string{"hotspots", testProcess, "--leaf-type", "config"}, "validation_error"},
		{"view", []string{"hotspots", testProcess, "--view", "no context"}, "validation_error"},
		{"state", []string{"hotspots", testProcess, "--view", "functions", "--state", "config"}, "validation_error"},
		{"sort", []string{"hotspots", testProcess, "--view", "functions", "--sort", "timeout"}, "validation_error"},
		{"start", []string{"hotspots", testProcess, "--default-timeframe-start", "config"}, "validation_error"},
		{"end", []string{"allocations", testProcess, "--default-timeframe-end", "now()-1timeout"}, "validation_error"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			code, _, stderr := runProfilingStreams(t, tc.args...)
			require.Equal(t, 1, code)
			require.True(t, strings.HasPrefix(stderr, "Error: "), stderr)
			want := strings.TrimSuffix(strings.TrimPrefix(stderr, "Error: "), "\n")

			code, stdout, stderr := runProfilingStreams(t, append(tc.args, "--agent")...)
			require.Equal(t, 1, code)
			require.Empty(t, stderr)
			require.Equal(t, profilingErrorEnvelope{Error: profilingEnvelopeError{Code: tc.code, Message: want}}, decodeProfilingError(t, stdout))
		})
	}
	require.Len(t, api.paths, 10, "only the --focus cases reach the API, once with and once without --agent")
}
