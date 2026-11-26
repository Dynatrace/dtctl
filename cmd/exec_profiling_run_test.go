package cmd

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/require"

	"github.com/Dynatrace/dtctl/pkg/resources/profiling"
)

// profilingTree is a methodHotspots result with cumulative counts, the way the
// API reports them:
//
//	app.Main.main (100)
//	├── lib.Loader.load (60)
//	│   └── lib.Util.wrap (30)
//	└── app.Main.handle (30, lock 5)
//	    └── lib.Util.wrap (20, lock 5)
//
// Self: main 10, load 30, wrap 30+20, handle 10.
const profilingTree = `{"dataRootNodeId":"0","dataNodes":[
 {"id":"0","className":"","methodName":"","apiInfo":{"systemApi":false},"samples":{"RUNNING":0},"childIds":["A"]},
 {"id":"A","className":"app.Main","methodName":"main","apiInfo":{"systemApi":true},"samples":{"RUNNING":100,"LOCK":5},"childIds":["B","D"]},
 {"id":"B","className":"lib.Loader","methodName":"load","apiInfo":{"systemApi":false},"samples":{"RUNNING":60},"childIds":["C1"]},
 {"id":"C1","className":"lib.Util","methodName":"wrap","apiInfo":{"systemApi":false},"samples":{"RUNNING":30},"childIds":[]},
 {"id":"D","className":"app.Main","methodName":"handle","apiInfo":{"systemApi":true},"samples":{"RUNNING":30,"LOCK":5},"childIds":["C2"]},
 {"id":"C2","className":"lib.Util","methodName":"wrap","apiInfo":{"systemApi":false},"samples":{"RUNNING":20,"LOCK":5},"childIds":[]}
]}`

// profilingAPI is a code-level analysis endpoint that answers every submit
// synchronously with one canned status and body, and records the paths hit.
type profilingAPI struct {
	mu    sync.Mutex
	paths []string
}

func newProfilingAPI(t *testing.T, status int, body string) *profilingAPI {
	t.Helper()
	api := &profilingAPI{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		api.mu.Lock()
		api.paths = append(api.paths, r.URL.Path)
		api.mu.Unlock()
		if !strings.HasPrefix(r.URL.Path, "/platform-reserved/codelevelanalysis/") {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = fmt.Fprint(w, body)
	}))
	t.Cleanup(srv.Close)
	writeIsolatedConfig(t, safetyLevelConfig(srv.URL, "readonly"))
	return api
}

func runProfilingCmd(t *testing.T, args ...string) (int, string) {
	t.Helper()
	return captureRun(t, append([]string{"exec", "profiling"}, args...), RunOptions{})
}

const testProcess = "PROCESS_GROUP_INSTANCE-0000000000000001"

// runProfilingStreams runs the command outside agent mode and returns its exit
// code, stdout and stderr.
func runProfilingStreams(t *testing.T, args ...string) (int, string, string) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	code := Run(append([]string{"exec", "profiling"}, args...), RunOptions{Stdout: &stdout, Stderr: &stderr})
	return code, stdout.String(), stderr.String()
}

// The default hotspots view prints one folded stack per line.
func TestExecProfiling_StackView(t *testing.T) {
	api := newProfilingAPI(t, http.StatusOK, profilingTree)

	code, out := runProfilingCmd(t, "hotspots", testProcess)
	require.Zero(t, code, out)
	require.Contains(t, out, "app.Main.main;app.Main.handle;lib.Util.wrap running=20 lock=5 net_io=0 disk_io=0 wait=0\n")
	require.Contains(t, out, "app.Main.main;lib.Loader.load;lib.Util.wrap running=30 lock=0")
	require.Len(t, api.paths, 1, "a synchronous result must cost one request")
	require.Contains(t, api.paths[0], "/methodhotspots/")
}

// --app-only on a result with no application frames says so in one line
// instead of printing nothing.
func TestExecProfiling_AppOnlyRemovedEverything(t *testing.T) {
	noApp := strings.ReplaceAll(profilingTree, `"systemApi":true`, `"systemApi":false`)
	newProfilingAPI(t, http.StatusOK, noApp)

	code, out := runProfilingCmd(t, "hotspots", testProcess, "--app-only")
	require.Zero(t, code, out)
	require.Contains(t, out, "--app-only removed every frame")
}

// --view functions sums each function over every stack it is on, and a table
// ends with the note and the hints as # lines.
func TestExecProfiling_FunctionsView(t *testing.T) {
	newProfilingAPI(t, http.StatusOK, profilingTree)

	code, out := runProfilingCmd(t, "hotspots", testProcess, "--view", "functions")
	require.Zero(t, code, out)
	var rows []string
	for _, l := range strings.Split(strings.TrimSpace(out), "\n") {
		if !strings.HasPrefix(l, "#") && strings.TrimSpace(l) != "" {
			rows = append(rows, l)
		}
	}
	require.GreaterOrEqual(t, len(rows), 2, out)
	require.Contains(t, rows[1], "wrap", "wrap has the most self samples (50), so it is the first row: %s", out)
	require.Contains(t, out, "# 4 of 4 functions, by self running samples")
	require.Contains(t, out, "--focus 'lib.Util.wrap'")
}

// -o json prints the rows alone, without the # lines a table gets.
func TestExecProfiling_FunctionsViewJSON(t *testing.T) {
	newProfilingAPI(t, http.StatusOK, profilingTree)

	code, out := runProfilingCmd(t, "hotspots", testProcess, "--view", "functions", "-o", "json")
	require.Zero(t, code, out)
	require.NotContains(t, out, "# ")
	var rows []map[string]interface{}
	require.NoError(t, json.Unmarshal([]byte(out), &rows), out)
	require.Len(t, rows, 4)
}

// --focus prints the callers and callees of one function, with a hint to walk
// further.
func TestExecProfiling_Focus(t *testing.T) {
	newProfilingAPI(t, http.StatusOK, profilingTree)

	code, out := runProfilingCmd(t, "hotspots", testProcess, "--focus", "wrap")
	require.Zero(t, code, out)
	require.Contains(t, out, "lib.Util.wrap: 2 callers, 0 callees")
	require.Contains(t, out, "load")
	require.Contains(t, out, "handle")
	require.Contains(t, out, "--focus <caller or callee> walks the call graph further")
}

// A --focus that does not name one function fails saying what would: the view
// that lists the functions, or the candidates it matched.
func TestExecProfiling_FocusNeedsOneFunction(t *testing.T) {
	newProfilingAPI(t, http.StatusOK, profilingTree)

	code, stdout, stderr := runProfilingStreams(t, "hotspots", testProcess, "--focus", "nope")
	require.Equal(t, 1, code)
	require.Empty(t, stdout)
	require.Equal(t, "Error: no function in the profile matches \"nope\" — list them with --view functions\n", stderr)

	// Candidates are listed heaviest first.
	code, stdout, stderr = runProfilingStreams(t, "hotspots", testProcess, "--focus", "Main")
	require.Equal(t, 1, code)
	require.Empty(t, stdout)
	require.Equal(t, "Error: \"Main\" matches 2 functions; pass one exactly:\n  app.Main.main\n  app.Main.handle\n", stderr)

	// At most eight are listed, and the count is of all of them.
	var nodes, ids []string
	for i := 0; i < 10; i++ {
		ids = append(ids, fmt.Sprintf(`"n%d"`, i))
		nodes = append(nodes, fmt.Sprintf(`{"id":"n%d","className":"app.C","methodName":"m%d","samples":{"RUNNING":%d},"childIds":[]}`, i, i, 10-i))
	}
	newProfilingAPI(t, http.StatusOK, fmt.Sprintf(`{"dataRootNodeId":"0","dataNodes":[{"id":"0","className":"","methodName":"","samples":{"RUNNING":0},"childIds":[%s]},%s]}`,
		strings.Join(ids, ","), strings.Join(nodes, ",")))
	code, _, stderr = runProfilingStreams(t, "hotspots", testProcess, "--focus", "app.C")
	require.Equal(t, 1, code)
	require.Equal(t, "Error: \"app.C\" matches 10 functions; pass one exactly:\n  app.C.m0\n  app.C.m1\n  app.C.m2\n  app.C.m3\n  app.C.m4\n  app.C.m5\n  app.C.m6\n  app.C.m7\n", stderr)
}

// A --limit that cuts the focus callers reports the full row count as the
// envelope total, not the rows returned.
func TestExecProfiling_FocusAgentTotal(t *testing.T) {
	newProfilingAPI(t, http.StatusOK, profilingTree)

	env := runProfilingAgent(t, "hotspots", testProcess, "--focus", "wrap", "--limit", "1")
	require.Len(t, env.Result, 3)
	require.Equal(t, 4, *env.Context.Total)
	require.True(t, env.Context.HasMore)
	require.NotEmpty(t, env.Context.Warnings)
}

// A --state with no samples in the window explains itself rather than printing
// an empty table.
func TestExecProfiling_FunctionsViewEmptyState(t *testing.T) {
	newProfilingAPI(t, http.StatusOK, profilingTree)

	code, out := runProfilingCmd(t, "hotspots", testProcess, "--view", "functions", "--state", "disk_io")
	require.Zero(t, code, out)
	require.Contains(t, out, "# no disk_io samples for")
}

// A view without rows prints one line saying why, and the reason is the one
// that holds: no samples at all, none in the counted state, or --app-only.
func TestExecProfiling_EmptyViewSaysWhy(t *testing.T) {
	// Ten LOCK samples on one library frame, and nothing running.
	const lockedLib = `{"dataRootNodeId":"0","dataNodes":[
	 {"id":"0","className":"","methodName":"","samples":{"RUNNING":0},"childIds":["A"]},
	 {"id":"A","className":"lib.Pool","methodName":"take","apiInfo":{"systemApi":false},"samples":{"RUNNING":0,"LOCK":10},"childIds":[]}]}`
	const noNodes = `{"dataRootNodeId":"0","dataNodes":[]}`
	const (
		noSamples = "# no stacks for " + testProcess + ": no profiling samples in the window (status=completed) — the process may be idle, restarted, or not under load; try another window (at most 2h), or --raw for the API's whole result\n"
		appOnly   = "# no stacks for " + testProcess + ": --app-only removed every frame (the profiler classified none as application code) — re-run without --app-only\n"
		noRunning = "# no running samples for " + testProcess + " in the window — the stack view (without --view/--focus) shows every thread state; waiting states may need --show-waiting\n"
		noDiskIO  = "# no disk_io samples for " + testProcess + " in the window — the stack view (without --view/--focus) shows every thread state; waiting states may need --show-waiting\n"
	)
	cases := []struct {
		name, tree string
		args       []string
		want       string
	}{
		{"stacks, nothing sampled", noNodes, nil, noSamples},
		{"stacks, app-only removed them", lockedLib, []string{"--app-only"}, appOnly},
		{"functions, nothing sampled", noNodes, []string{"--view", "functions"}, noSamples},
		{"functions, nothing sampled, another state", noNodes, []string{"--view", "functions", "--state", "lock"}, noSamples},
		{"functions, nothing running", lockedLib, []string{"--view", "functions"}, noRunning},
		{"functions, nothing running, app-only", lockedLib, []string{"--view", "functions", "--app-only"}, noRunning},
		{"functions, no samples in the state", lockedLib, []string{"--view", "functions", "--state", "disk_io"}, noDiskIO},
		{"functions, app-only removed the state's frames", lockedLib, []string{"--view", "functions", "--state", "lock", "--app-only"}, appOnly},
		{"focus, nothing sampled, another state", noNodes, []string{"--focus", "take", "--state", "offcpu"}, noSamples},
		{"focus, nothing running", lockedLib, []string{"--focus", "take"}, noRunning},
		{"focus, app-only removed the state's frames", lockedLib, []string{"--focus", "take", "--state", "lock", "--app-only"}, appOnly},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			newProfilingAPI(t, http.StatusOK, tc.tree)
			code, out := runProfilingCmd(t, append([]string{"hotspots", testProcess}, tc.args...)...)
			require.Zero(t, code, out)
			require.Equal(t, tc.want, out)
		})
	}

	// The agent envelope carries the same line as its warning.
	newProfilingAPI(t, http.StatusOK, lockedLib)
	env := runProfilingAgent(t, "hotspots", testProcess, "--view", "functions", "--state", "lock", "--app-only")
	require.Empty(t, env.Result)
	require.Equal(t, []string{strings.TrimSuffix(strings.TrimPrefix(appOnly, "# "), "\n")}, env.Context.Warnings)
}

// In agent mode the per-function view rides in the envelope: the hints as
// suggestions, the analysis as context.profile fields, no trailing # lines.
func TestExecProfiling_FunctionsViewAgent(t *testing.T) {
	newProfilingAPI(t, http.StatusOK, profilingTree)

	env := runProfilingAgent(t, "hotspots", testProcess, "--view", "functions")
	require.Contains(t, fmt.Sprint(env.Context.Suggestions), "--focus 'lib.Util.wrap'")
	require.Equal(t, "functions", env.Context.Profile.View)
	require.Equal(t, 100, *env.Context.Profile.Samples)
	require.Equal(t, 4, *env.Context.Total)
	require.False(t, env.Context.HasMore)
	require.Empty(t, env.Context.Warnings)
}

type profilingEnvelope struct {
	OK      bool                     `json:"ok"`
	Result  []map[string]interface{} `json:"result"`
	Context struct {
		Total       *int     `json:"total"`
		HasMore     bool     `json:"has_more"`
		Warnings    []string `json:"warnings"`
		Suggestions []string `json:"suggestions"`
		Window      *struct {
			From, To, Span, Note string
		} `json:"window"`
		Profile *struct {
			Entity         string `json:"entity"`
			APIEntity      string `json:"api_entity"`
			Kind           string `json:"kind"`
			View           string `json:"view"`
			LeafType       string `json:"leaf_type"`
			State          string `json:"state"`
			Status         string `json:"status"`
			Samples        *int   `json:"samples"`
			AllocatedBytes *int64 `json:"allocated_bytes"`
		} `json:"profile"`
	} `json:"context"`
}

func runProfilingAgent(t *testing.T, args ...string) profilingEnvelope {
	t.Helper()
	code, out := runProfilingCmd(t, append(args, "-A", "-o", "json")...)
	require.Zero(t, code, out)
	var env profilingEnvelope
	require.NoError(t, json.Unmarshal([]byte(out), &env), out)
	require.True(t, env.OK, out)
	return env
}

// -o json prints the stack view's rows (the five counts and the stack), ranked
// by running samples and capped by --limit like the table, not the API's
// payload.
func TestExecProfiling_StackViewJSON(t *testing.T) {
	newProfilingAPI(t, http.StatusOK, profilingTree)

	code, out := runProfilingCmd(t, "hotspots", testProcess, "-o", "json", "--limit", "3")
	require.Zero(t, code, out)
	var rows []map[string]interface{}
	require.NoError(t, json.Unmarshal([]byte(out), &rows), out)
	require.Len(t, rows, 3)
	require.Equal(t, "app.Main.main;lib.Loader.load", rows[0]["stack"])
	require.EqualValues(t, 30, rows[0]["running"])
	require.Equal(t, "app.Main.main;app.Main.handle;lib.Util.wrap", rows[2]["stack"])
	require.EqualValues(t, 5, rows[2]["lock"])
	for _, k := range []string{"running", "lock", "net_io", "disk_io", "wait", "stack"} {
		require.Contains(t, rows[0], k)
	}
}

// -o csv prints the same rows as columns, not a dump of a Go map.
func TestExecProfiling_StackViewCSV(t *testing.T) {
	newProfilingAPI(t, http.StatusOK, profilingTree)

	code, out := runProfilingCmd(t, "hotspots", testProcess, "-o", "csv")
	require.Zero(t, code, out)
	require.NotContains(t, out, "map[")
	lines := strings.Split(strings.TrimSpace(out), "\n")
	require.Len(t, lines, 6, out) // header + 5 stacks
	require.Contains(t, strings.ToLower(lines[0]), "running")
	require.Contains(t, strings.ToLower(lines[0]), "stack")
}

// A truncated table says how many stacks there are and how to see them all.
func TestExecProfiling_StackViewTruncatedTable(t *testing.T) {
	newProfilingAPI(t, http.StatusOK, profilingTree)

	code, out := runProfilingCmd(t, "hotspots", testProcess, "--limit", "1")
	require.Zero(t, code, out)
	require.Contains(t, out, "# showing 1 of 5 stacks, by running samples; --limit 0 lists them all")
}

// In agent mode the stack view's rows ride in the envelope with the analysis
// as fields, the PROCESS- rewrite disclosed, and truncation as a warning with
// total and has_more.
func TestExecProfiling_StackViewAgent(t *testing.T) {
	newProfilingAPI(t, http.StatusOK, profilingTree)

	env := runProfilingAgent(t, "hotspots", "PROCESS-0000000000000001", "--limit", "1")
	require.Len(t, env.Result, 1)
	require.Equal(t, 5, *env.Context.Total)
	require.True(t, env.Context.HasMore)
	require.Contains(t, fmt.Sprint(env.Context.Warnings), "showing 1 of 5 stacks")
	p := env.Context.Profile
	require.Equal(t, "PROCESS-0000000000000001", p.Entity)
	require.Equal(t, "PROCESS_GROUP_INSTANCE-0000000000000001", p.APIEntity)
	require.Equal(t, "hotspots", p.Kind)
	require.Equal(t, "stacks", p.View)
	require.Equal(t, "total", p.LeafType)
	require.Equal(t, 100, *p.Samples)
	require.Equal(t, "2h", env.Context.Window.Span)
	require.NotEmpty(t, env.Context.Window.Note, "the default window says so")
}

// An agent gets fewer stacks by default than a table, and is told so.
func TestExecProfiling_StackViewAgentDefaultLimit(t *testing.T) {
	var nodes []string
	var ids []string
	for i := 0; i < profilingStacksAgentDefaultLimit+5; i++ {
		id := fmt.Sprintf("n%d", i)
		ids = append(ids, fmt.Sprintf("%q", id))
		nodes = append(nodes, fmt.Sprintf(`{"id":%q,"className":"app.C","methodName":"m%d","apiInfo":{"systemApi":true},"samples":{"RUNNING":%d},"childIds":[]}`, id, i, i+1))
	}
	tree := fmt.Sprintf(`{"dataRootNodeId":"0","dataNodes":[{"id":"0","className":"","methodName":"","samples":{"RUNNING":0},"childIds":[%s]},%s]}`,
		strings.Join(ids, ","), strings.Join(nodes, ","))
	newProfilingAPI(t, http.StatusOK, tree)

	env := runProfilingAgent(t, "hotspots", testProcess)
	require.Len(t, env.Result, profilingStacksAgentDefaultLimit)
	require.True(t, env.Context.HasMore)
	require.Equal(t, profilingStacksAgentDefaultLimit+5, *env.Context.Total)
}

// An empty view is an empty result, with the explanation in context.warnings.
func TestExecProfiling_EmptyAgent(t *testing.T) {
	newProfilingAPI(t, http.StatusOK, `{"dataRootNodeId":"0","dataNodes":[]}`)

	env := runProfilingAgent(t, "hotspots", testProcess)
	require.Empty(t, env.Result)
	require.Equal(t, 0, *env.Context.Total)
	require.Contains(t, fmt.Sprint(env.Context.Warnings), "no profiling samples in the window")
}

// --raw prints the API's whole result.
func TestExecProfiling_Raw(t *testing.T) {
	newProfilingAPI(t, http.StatusOK, profilingTree)

	code, out := runProfilingCmd(t, "hotspots", testProcess, "--raw")
	require.Zero(t, code, out)
	var env map[string]interface{}
	require.NoError(t, json.Unmarshal([]byte(out), &env), out)
	require.Equal(t, "completed", env["status"])
	require.Equal(t, testProcess, env["entityId"])
}

// allocations runs the memory analysis; memory is its alias.
func TestExecProfiling_AllocationsAlias(t *testing.T) {
	api := newProfilingAPI(t, http.StatusOK, `{"stacktreeRootIds":[],"stacktreeNodes":[]}`)

	for _, kind := range []string{"allocations", "memory"} {
		code, out := runProfilingCmd(t, kind, testProcess)
		require.Zero(t, code, out)
	}
	require.Len(t, api.paths, 2)
	for _, p := range api.paths {
		require.Contains(t, p, "/memoryallocation/")
	}
}

// In agent mode an allocations result says what the survived figures are, so
// an agent doesn't report bytes-times-GCs as heap held. The help says it too.
func TestExecProfiling_AllocationsSurvivedNote(t *testing.T) {
	newProfilingAPI(t, http.StatusOK, `{"analysisResult":{"stacktreeRootIds":[1],"stacktreeNodes":[`+
		`{"id":1,"name":"Cache.put","childIds":[],"apiInfo":{"systemApi":true},`+
		`"allocationInfo":{"allocationSize":100,"allocationCount":1,"survivorSize":500,"survivorCount":5}}]}}`)

	env := runProfilingAgent(t, "allocations", testProcess)
	require.Contains(t, env.Context.Suggestions, profiling.SurvivedNote)
	require.EqualValues(t, 500, env.Result[0]["survived_bytes"])

	help := findProfilingCmd(t, "allocations").Long
	require.Contains(t, help, "Don't report them as heap used or retained.")
	require.Contains(t, findProfilingCmd(t, "allocations").Parent().Long, "bytes x GCs survived")
}

// --check-scopes answers for a profiling leaf with the group's catalog scopes,
// rather than "ok, no scopes".
func TestExecProfiling_CheckScopes(t *testing.T) {
	for _, kind := range []string{"hotspots", "allocations"} {
		c := findProfilingCmd(t, kind)
		verb, resource := verbResource(c)
		require.Equal(t, "profiling", resource, kind)
		got, req := scopesForInvocation(c, verb, resource)
		require.Equal(t, scopeRequirementKnown, req, kind)
		require.ElementsMatch(t, []string{"storage:entities:read", "storage:profiles:read"}, got, kind)
	}
}

func findProfilingCmd(t *testing.T, kind string) *cobra.Command {
	t.Helper()
	c, _, err := rootCmd.Find([]string{"exec", "profiling", kind})
	require.NoError(t, err)
	return c
}

// A window that cannot be analysed is refused before any request, in words that
// name the flags to change.
func TestExecProfiling_WindowErrors(t *testing.T) {
	api := newProfilingAPI(t, http.StatusOK, profilingTree)
	cases := []struct {
		name string
		args []string
		want string
	}{
		{"too long", []string{"--default-timeframe-start", "now()-3h", "--default-timeframe-end", "now()"},
			"invalid timeframe: the window must not exceed 2h (got 3h0m0s) — narrow it; the window can lie anywhere in the past"},
		{"too long, far back", []string{"--default-timeframe-start", "1700000000000", "--default-timeframe-end", "1700007200001"},
			"invalid timeframe: the window must not exceed 2h (got 2h0m0.001s) — narrow it; the window can lie anywhere in the past"},
		{"start in the future", []string{"--default-timeframe-start", "now()+5m"},
			"invalid timeframe: the start is in the future — profiling only covers the past (e.g. --default-timeframe-start 'now()-2h')"},
		{"end in the future", []string{"--default-timeframe-end", "now()+1h"},
			"invalid timeframe: the end is in the future — profiling only covers the past; leave --default-timeframe-end out to end now"},
		{"end before start", []string{"--default-timeframe-start", "1700003600000", "--default-timeframe-end", "1700000000000"},
			"invalid timeframe: the end must be after the start"},
		{"unreadable start", []string{"--default-timeframe-start", "yesterday"},
			`invalid --default-timeframe-start: cannot parse time "yesterday": use ISO-8601/RFC3339, epoch millis, or now()-<dur>`},
		{"unreadable end", []string{"--default-timeframe-start", "now()-1h", "--default-timeframe-end", "now()-1y"},
			`invalid --default-timeframe-end: invalid duration in "now()-1y": unknown duration "1y": use a number and a unit (ms, s, m, h, d, w), e.g. 1d or 1h30m`},
		// An end with no time 2h before it. The subtraction used to wrap
		// around, and this was refused as a start in the future.
		{"end out of range", []string{"--default-timeframe-end=-9223372036854775808"},
			`invalid --default-timeframe-end: time "-9223372036854775808" is out of range`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			code, stdout, stderr := runProfilingStreams(t, append([]string{"hotspots", testProcess}, tc.args...)...)
			require.Equal(t, 1, code)
			require.Empty(t, stdout)
			require.Equal(t, "Error: "+tc.want+"\n", stderr)
		})
	}
	require.Empty(t, api.paths, "a refused window must not reach the API")
}

// The envelope says which window was analysed, and when it was nobody's
// choice.
func TestExecProfiling_WindowInEnvelope(t *testing.T) {
	newProfilingAPI(t, http.StatusOK, profilingTree)

	env := runProfilingAgent(t, "hotspots", testProcess)
	require.Equal(t, "2h", env.Context.Window.Span)
	require.Equal(t, "no window given, so the last 2h; set one with --default-timeframe-start/--default-timeframe-end", env.Context.Window.Note)

	env = runProfilingAgent(t, "hotspots", testProcess, "--default-timeframe-start", "1700000000000", "--default-timeframe-end", "1700005400000")
	require.Equal(t, "2023-11-14T22:13:20Z", env.Context.Window.From)
	require.Equal(t, "2023-11-14T23:43:20Z", env.Context.Window.To)
	require.Equal(t, "90m", env.Context.Window.Span)
	require.Empty(t, env.Context.Window.Note)
}

// newProfilingPollAPI is an endpoint that accepts every analysis and then
// answers each poll for its result with one canned status and body.
func newProfilingPollAPI(t *testing.T, status int, body string) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.HasSuffix(r.URL.Path, "/result") {
			w.WriteHeader(status)
			_, _ = fmt.Fprint(w, body)
			return
		}
		w.WriteHeader(http.StatusAccepted)
		_, _ = fmt.Fprint(w, `{"token":"t1"}`)
	}))
	t.Cleanup(srv.Close)
	writeIsolatedConfig(t, safetyLevelConfig(srv.URL, "readonly"))
}

// A failure the caller can act on carries advice for it, and the exit code and
// the agent error code of its status.
func TestExecProfiling_APIErrors(t *testing.T) {
	cases := []struct {
		name        string
		status      int
		poll        bool
		exit        int
		code        string
		suggestions []string
	}{
		{name: "bad request", status: 400, exit: 1, code: "bad_request", suggestions: []string{
			"The API rejected the request parameters — check the timeframe (in the past, at most 2h) and the entity id",
		}},
		{name: "missing scope", status: 403, exit: 5, code: "permission_denied", suggestions: []string{
			"Profiling needs storage:profiles:read and storage:entities:read, both on the token and in the IAM policy",
			"OAuth (dtctl auth login): log in again so the session requests storage:profiles:read",
		}},
		{name: "unknown process", status: 404, exit: 4, code: "not_found", suggestions: []string{
			"The environment knows no such process (ids are per environment, and short-lived process instances disappear)",
			"List eligible ids: dtctl query 'smartscapeNodes PROCESS | fields id, dt.process_group.detected_name, dt.process_group.id'",
		}},
		// The process was found when the analysis started: a poll that finds
		// nothing has lost the task, and must not send the caller looking for
		// another id.
		{name: "task gone", status: 404, poll: true, exit: 4, code: "not_found", suggestions: []string{
			"The analysis was started, but the environment no longer knows its task (it expired, or it ran on a node that is gone) — run the command again",
		}},
		{name: "missing scope on a poll", status: 403, poll: true, exit: 5, code: "permission_denied", suggestions: []string{
			"Profiling needs storage:profiles:read and storage:entities:read, both on the token and in the IAM policy",
			"OAuth (dtctl auth login): log in again so the session requests storage:profiles:read",
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			body := fmt.Sprintf(`{"error":{"code":%d,"message":"the API's message"}}`, tc.status)
			if tc.poll {
				newProfilingPollAPI(t, tc.status, body)
			} else {
				newProfilingAPI(t, tc.status, body)
			}

			code, stdout, stderr := runProfilingStreams(t, "hotspots", testProcess)
			require.Equal(t, tc.exit, code)
			require.Empty(t, stdout)
			want := fmt.Sprintf("Error: Failed to run profiling analysis (HTTP %d): the API's message\n\nTroubleshooting suggestions:\n", tc.status)
			for _, s := range tc.suggestions {
				want += "  • " + s + "\n"
			}
			require.Equal(t, want, stderr)

			code, stdout, stderr = runProfilingStreams(t, "hotspots", testProcess, "--agent")
			require.Equal(t, tc.exit, code)
			require.Empty(t, stderr)
			var env struct {
				OK    bool `json:"ok"`
				Error struct {
					Code        string   `json:"code"`
					Message     string   `json:"message"`
					Operation   string   `json:"operation"`
					StatusCode  int      `json:"status_code"`
					Suggestions []string `json:"suggestions"`
				} `json:"error"`
			}
			require.NoError(t, json.Unmarshal([]byte(stdout), &env), stdout)
			require.False(t, env.OK)
			require.Equal(t, tc.code, env.Error.Code)
			require.Equal(t, "the API's message", env.Error.Message)
			require.Equal(t, "run profiling analysis", env.Error.Operation)
			require.Equal(t, tc.status, env.Error.StatusCode)
			require.Equal(t, tc.suggestions, env.Error.Suggestions)
		})
	}
}

// A failure with nothing profiling-specific to add is shown as it is.
func TestExecProfiling_ErrorsPassedThrough(t *testing.T) {
	t.Run("server error", func(t *testing.T) {
		newProfilingAPI(t, http.StatusNotImplemented, `{"error":{"code":501,"message":"boom"}}`)
		code, stdout, stderr := runProfilingStreams(t, "hotspots", testProcess)
		require.Equal(t, 1, code)
		require.Empty(t, stdout)
		require.Equal(t, "Error: codelevelanalysis: submit: API error (501): boom\n", stderr)
	})
	t.Run("ineligible entity", func(t *testing.T) {
		api := newProfilingAPI(t, http.StatusOK, profilingTree)
		code, stdout, stderr := runProfilingStreams(t, "hotspots", "SERVICE-0000000000000001")
		require.Equal(t, 1, code)
		require.Empty(t, stdout)
		require.Equal(t, `Error: invalid entity "SERVICE-0000000000000001": profiling takes a process id, PROCESS-xxx (classic PROCESS_GROUP-xxx and PROCESS_GROUP_INSTANCE-xxx ids work too; SERVICE ids do not).
Find a process id:
  dtctl query 'smartscapeNodes PROCESS | fields id, dt.process_group.detected_name, dt.process_group.id'
  # id is the PROCESS-xxx id to pass; dt.process_group.id is its PROCESS_GROUP-xxx id
`, stderr)
		require.Empty(t, api.paths)
	})
}
