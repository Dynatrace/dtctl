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

// An API failure fails the command.
func TestExecProfiling_APIErrorFails(t *testing.T) {
	newProfilingAPI(t, http.StatusForbidden, `{"error":{"code":403,"message":"missing scope"}}`)

	code, _ := runProfilingCmd(t, "hotspots", testProcess)
	require.NotZero(t, code)
}
