package cmd

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
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

// A --state with no samples in the window explains itself rather than printing
// an empty table.
func TestExecProfiling_FunctionsViewEmptyState(t *testing.T) {
	newProfilingAPI(t, http.StatusOK, profilingTree)

	code, out := runProfilingCmd(t, "hotspots", testProcess, "--view", "functions", "--state", "disk_io")
	require.Zero(t, code, out)
	require.Contains(t, out, "# no disk_io samples for")
}

// In agent mode the per-function view rides in the envelope, with the note and
// hints as suggestions instead of trailing # lines.
func TestExecProfiling_FunctionsViewAgent(t *testing.T) {
	newProfilingAPI(t, http.StatusOK, profilingTree)

	code, out := runProfilingCmd(t, "hotspots", testProcess, "--view", "functions", "-A")
	require.Zero(t, code, out)
	var env map[string]interface{}
	require.NoError(t, json.Unmarshal([]byte(out), &env), out)
	ctx, _ := env["context"].(map[string]interface{})
	require.Contains(t, fmt.Sprint(ctx["suggestions"]), "4 of 4 functions", out)
	require.NotContains(t, out, "\n# ")
}

// The structured stack view keeps the envelope.
func TestExecProfiling_StackViewJSON(t *testing.T) {
	newProfilingAPI(t, http.StatusOK, profilingTree)

	code, out := runProfilingCmd(t, "hotspots", testProcess, "-o", "json")
	require.Zero(t, code, out)
	var env map[string]interface{}
	require.NoError(t, json.Unmarshal([]byte(out), &env), out)
	require.Equal(t, "completed", env["status"])
	require.Equal(t, testProcess, env["entityId"])
}

// An API failure fails the command.
func TestExecProfiling_APIErrorFails(t *testing.T) {
	newProfilingAPI(t, http.StatusForbidden, `{"error":{"code":403,"message":"missing scope"}}`)

	code, _ := runProfilingCmd(t, "hotspots", testProcess)
	require.NotZero(t, code)
}
