package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/dynatrace-oss/dtctl/pkg/resources/livedebugger"
	"github.com/dynatrace-oss/dtctl/pkg/stability"
)

// breakpointScope links the test repository on the test server: checkout
// covers the whole repository with one workload, ledger a directory with a
// service name, split a directory with both and a service id, so it renders
// two filter sets, and regions a directory with a workload in each of two
// namespaces.
const breakpointScope = `environments:
  127.0.0.1:
    - name: checkout
      workloads:
        - namespace: payments
          name: checkout
    - name: ledger
      path: services/ledger
      service-names: [ledger]
    - name: split
      path: services/split
      services: [SERVICE-0123456789ABCDEF]
      service-names: [split]
      workloads:
        - namespace: payments
          name: split
    - name: regions
      path: services/regions
      workloads:
        - namespace: payments
          name: regions
        - namespace: web
          name: regions
`

var (
	checkoutFilterSet = map[string][]string{"k8s.namespace.name": {"payments"}, "k8s.workload.name": {"checkout"}}
	ledgerFilterSet   = map[string][]string{"service.name": {"ledger"}}
	splitFilterSets   = []map[string][]string{
		{"k8s.namespace.name": {"payments"}, "k8s.workload.name": {"split"}},
		{"service.name": {"split"}},
	}
)

// graphQLCall is one request the Live Debugger stub answered.
type graphQLCall struct {
	op        string
	variables map[string]interface{}
}

// liveDebuggerStub is a Live Debugger GraphQL endpoint holding one workspace.
// The client sends a query text and variables but no operation name, so each
// request is matched by the operation its text declares.
type liveDebuggerStub struct {
	*httptest.Server
	mu    sync.Mutex
	calls []graphQLCall
	// filterSets are the workspace's filters, in the response's shape.
	filterSets []interface{}
	// rules are breakpoints the workspace holds, in the response's shape.
	rules []interface{}
	// activeRules is how many more enabled breakpoints it holds.
	activeRules int
}

// liveDebuggerOperations are the operations the stub answers, by the
// declaration that opens their query text.
var liveDebuggerOperations = []string{
	"query GetOrCreateWorkspaceV2(",
	"mutation UpdateWorkspaceV2(",
	"mutation CreateRule(",
	"query GetWorkspaceRules(",
	"mutation EnableOrDisableRules(",
}

// newLiveDebuggerStub starts the stub and points an isolated config's only
// context at it, at the given safety level, with repo scoping opted in.
func newLiveDebuggerStub(t *testing.T, safetyLevel string, filterSets ...map[string][]string) *liveDebuggerStub {
	t.Helper()
	s := &liveDebuggerStub{filterSets: workspaceFilterSetsJSON(filterSets...)}
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/platform/storage/query/v1/query:execute" {
			_, _ = w.Write([]byte(`{"state":"SUCCEEDED","result":{"records":[]}}`))
			return
		}
		if r.URL.Path != "/platform/dob/graphql" {
			http.NotFound(w, r)
			return
		}
		var req struct {
			Query     string                 `json:"query"`
			Variables map[string]interface{} `json:"variables"`
		}
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &req)
		op := ""
		for _, decl := range liveDebuggerOperations {
			if strings.HasPrefix(strings.TrimSpace(req.Query), decl) {
				op = strings.Fields(decl)[1]
				op = strings.TrimSuffix(op, "(")
			}
		}
		s.mu.Lock()
		defer s.mu.Unlock()
		s.calls = append(s.calls, graphQLCall{op: op, variables: req.Variables})
		var body interface{}
		switch op {
		case "GetOrCreateWorkspaceV2":
			body = map[string]interface{}{"org": map[string]interface{}{"getOrCreateUserWorkspaceV2": map[string]interface{}{
				"id": "ws-1", "filterSets": s.filterSets,
			}}}
		case "UpdateWorkspaceV2":
			body = map[string]interface{}{"org": map[string]interface{}{"updateWorkspaceV2": map[string]interface{}{"id": "ws-1"}}}
		case "CreateRule":
			body = map[string]interface{}{"org": map[string]interface{}{"workspace": map[string]interface{}{
				"createRuleV2": map[string]interface{}{"id": "dtctl-rule-0001"},
			}}}
		case "GetWorkspaceRules":
			rules := append([]interface{}{}, s.rules...)
			for i := 0; i < s.activeRules; i++ {
				rules = append(rules, map[string]interface{}{"id": fmt.Sprintf("bp-%d", i), "is_disabled": false})
			}
			body = map[string]interface{}{"org": map[string]interface{}{"workspace": map[string]interface{}{"rules": rules}}}
		case "EnableOrDisableRules":
			body = map[string]interface{}{"org": map[string]interface{}{"workspace": map[string]interface{}{"enableOrDisableRules": []interface{}{}}}}
		default:
			t.Errorf("unexpected GraphQL request: %.80s", req.Query)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{"data": body})
	}))
	t.Cleanup(s.Close)
	writeIsolatedConfig(t, safetyLevelConfig(s.URL, safetyLevel))
	optInRepoScope(t)
	t.Cleanup(func() { restorePristineTree(context.Background()) })
	return s
}

// ruleAt is a breakpoint at filename:line as GetWorkspaceRules returns it,
// disabled for disableReason when that is set.
func ruleAt(filename string, line int, disableReason string) map[string]interface{} {
	return map[string]interface{}{
		"id": "dtctl-rule-0001", "immutableId": "abc12345", "is_disabled": disableReason != "", "disable_reason": disableReason,
		"aug_json": map[string]interface{}{"location": map[string]interface{}{"filename": filename, "lineno": line}},
	}
}

// workspaceFilterSetsJSON is sets as getOrCreateUserWorkspaceV2 returns them.
func workspaceFilterSetsJSON(sets ...map[string][]string) []interface{} {
	out := []interface{}{}
	for _, set := range livedebugger.BuildFilterSets(sets...) {
		out = append(out, set)
	}
	return out
}

// ops lists the operations the stub was sent, in order.
func (s *liveDebuggerStub) ops() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	ops := make([]string, len(s.calls))
	for i, c := range s.calls {
		ops[i] = c.op
	}
	return ops
}

// sentFilterSets is the filterSets variable of the one workspace update.
func (s *liveDebuggerStub) sentFilterSets(t *testing.T) string {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, c := range s.calls {
		if c.op == "UpdateWorkspaceV2" {
			data, _ := c.variables["data"].(map[string]interface{})
			raw, err := json.Marshal(data["filterSets"])
			require.NoError(t, err)
			return string(raw)
		}
	}
	t.Fatal("no workspace update was sent")
	return ""
}

// projectPaths lists the workspaces the stub was asked for, in order.
func (s *liveDebuggerStub) projectPaths() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	var paths []string
	for _, c := range s.calls {
		if input, ok := c.variables["workspaceInput"].(map[string]interface{}); ok && c.op == "GetOrCreateWorkspaceV2" {
			path, _ := input["projectPath"].(string)
			paths = append(paths, path)
		}
	}
	return paths
}

func (s *liveDebuggerStub) reset() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls = nil
}

// filterSetsPayload is what BuildFilterSets sends for sets, as JSON.
func filterSetsPayload(t *testing.T, sets ...map[string][]string) string {
	t.Helper()
	raw, err := json.Marshal(livedebugger.BuildFilterSets(sets...))
	require.NoError(t, err)
	return string(raw)
}

const (
	checkoutApplying = "set the workspace filters from repo scope checkout: k8s.namespace.name:payments,k8s.workload.name:checkout (--filters sets them by hand, --no-repo-scope turns this default off)\n"
	noFiltersError   = "no workspace filters configured"
)

// A workspace without filters takes them from the entry covering the working
// directory, says so on stderr and in the result, and then gets its
// breakpoint; an agent is told how to opt out.
func TestCreateBreakpoint_RepoScopeDefault(t *testing.T) {
	srv := newLiveDebuggerStub(t, "readwrite-all")
	chdirTestRepo(t, breakpointScope)

	code, stdout, stderr := runRepoScoped([]string{"create", "breakpoint", "A.java:1", "--plain"}, RunOptions{})
	require.Zero(t, code, "stdout: %s\nstderr: %s", stdout, stderr)
	require.Equal(t, checkoutApplying, stderr)
	require.Equal(t, []string{"GetOrCreateWorkspaceV2", "GetWorkspaceRules", "UpdateWorkspaceV2", "CreateRule"}, srv.ops(),
		"the default counts the active breakpoints even in a non-interactive run")
	require.JSONEq(t, filterSetsPayload(t, checkoutFilterSet), srv.sentFilterSets(t))
	require.Contains(t, stdout, "Created breakpoint at A.java:1 (id: dtctl-rule-0001); workspace filters set from repo scope checkout: k8s.namespace.name:payments,k8s.workload.name:checkout")

	srv.reset()
	code, stdout, stderr = runRepoScoped([]string{"create", "breakpoint", "A.java:1", "--agent"}, RunOptions{})
	require.Zero(t, code, stderr)
	resp := agentEnvelope(t, stdout)
	require.True(t, resp.OK)
	require.Contains(t, strings.Join(resp.Context.Suggestions, "\n"), "pass --filters to set them by hand, --no-repo-scope to turn this default off")
	require.Contains(t, strings.Join(resp.Context.Suggestions, "\n"), "--repo-scope <name>")
}

// A workspace without filters holds no breakpoints, so the default asks
// nothing, even where it could: interactive, with nothing on stdin.
func TestCreateBreakpoint_RepoScopeDefaultAsksOnlyWhenItRescopes(t *testing.T) {
	srv := newLiveDebuggerStub(t, "readwrite-all")
	chdirTestRepo(t, breakpointScope)

	code, stdout, stderr := runRepoScoped([]string{"create", "breakpoint", "A.java:1", "--no-agent"}, RunOptions{Stdin: strings.NewReader("")})
	require.Zero(t, code, "stdout: %s\nstderr: %s", stdout, stderr)
	require.NotContains(t, stdout, "Continue?")
	require.Equal(t, []string{"GetOrCreateWorkspaceV2", "GetWorkspaceRules", "UpdateWorkspaceV2", "CreateRule"}, srv.ops())

	srv.reset()
	srv.activeRules = 1
	code, stdout, _ = runRepoScoped([]string{"create", "breakpoint", "A.java:1", "--no-agent"}, RunOptions{Stdin: strings.NewReader("y\n")})
	require.Zero(t, code)
	require.Contains(t, stdout, filterChangeConfirmMessage(1, true))
	require.Contains(t, srv.ops(), "UpdateWorkspaceV2")
}

// A prompt whose input ended asked nobody, so the run fails as in plain mode,
// naming --yes, rather than reporting a cancel nobody chose.
func TestCreateBreakpoint_RepoScopeDefaultAtAClosedStdin(t *testing.T) {
	srv := newLiveDebuggerStub(t, "readwrite-all")
	srv.activeRules = 2
	chdirTestRepo(t, breakpointScope)

	code, stdout, stderr := runRepoScoped([]string{"create", "breakpoint", "A.java:1", "--no-agent"}, RunOptions{Stdin: strings.NewReader("")})
	require.NotZero(t, code, "stdout: %s", stdout)
	require.Contains(t, stderr, "pass --yes")
	require.NotContains(t, stdout, "Cancelled")
	require.NotContains(t, stderr, "set the workspace filters from repo scope", "nothing was set")
	require.NotContains(t, srv.ops(), "UpdateWorkspaceV2")
}

// The default changes filters nobody typed, so a workspace that holds active
// breakpoints but no filters is never re-scoped without a confirmation, and a
// non-interactive run, which cannot ask, refuses without a prompt on stdout.
func TestCreateBreakpoint_RepoScopeDefaultNeverRescopesUnasked(t *testing.T) {
	srv := newLiveDebuggerStub(t, "readwrite-all")
	srv.activeRules = 2
	chdirTestRepo(t, breakpointScope)

	code, stdout, _ := runRepoScoped([]string{"create", "breakpoint", "A.java:1", "--agent"}, RunOptions{})
	require.NotZero(t, code)
	require.Contains(t, agentError(t, stdout).Message, "pass --yes")
	require.NotContains(t, srv.ops(), "UpdateWorkspaceV2")

	srv.reset()
	code, _, stderr := runRepoScoped([]string{"create", "breakpoint", "A.java:1", "--plain", "--yes"}, RunOptions{})
	require.Zero(t, code, stderr)
	require.Equal(t, []string{"GetOrCreateWorkspaceV2", "UpdateWorkspaceV2", "CreateRule"}, srv.ops())
}

func TestCreateBreakpoint_RepoScopeOff(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
		env  map[string]string
	}{
		{"--no-repo-scope", []string{"--no-repo-scope"}, nil},
		{"DTCTL_NO_REPO_SCOPE=1", nil, map[string]string{noRepoScopeEnv: "1"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := newLiveDebuggerStub(t, "readwrite-all")
			chdirTestRepo(t, breakpointScope)

			argv := append([]string{"create", "breakpoint", "A.java:1", "--agent"}, tc.args...)
			code, stdout, stderr := runRepoScoped(argv, RunOptions{Env: tc.env})
			require.NotZero(t, code)
			require.True(t, strings.HasPrefix(agentError(t, stdout).Message, noFiltersError), stdout)
			require.Empty(t, stderr)
			require.Equal(t, []string{"GetOrCreateWorkspaceV2"}, srv.ops())
		})
	}

	// "Use what I typed" does not conflict with typed filters.
	srv := newLiveDebuggerStub(t, "readwrite-all")
	chdirTestRepo(t, breakpointScope)
	code, _, stderr := runRepoScoped([]string{"create", "breakpoint", "A.java:1", "--plain", "--no-repo-scope", "--filters", "k8s.namespace.name:web"}, RunOptions{})
	require.Zero(t, code, stderr)
	require.JSONEq(t, filterSetsPayload(t, map[string][]string{"k8s.namespace.name": {"web"}}), srv.sentFilterSets(t))
}

// A workspace that already has filters keeps them. When the covering entry
// would set different ones, one warning says how to apply it.
func TestCreateBreakpoint_RepoScopeLeavesExistingFilters(t *testing.T) {
	t.Run("different", func(t *testing.T) {
		srv := newLiveDebuggerStub(t, "readwrite-all", map[string][]string{"k8s.namespace.name": {"web"}})
		chdirTestRepo(t, breakpointScope)

		code, _, stderr := runRepoScoped([]string{"create", "breakpoint", "A.java:1", "--plain"}, RunOptions{})
		require.Zero(t, code, stderr)
		require.Equal(t, []string{"GetOrCreateWorkspaceV2", "CreateRule"}, srv.ops())
		require.Contains(t, stderr, "the workspace already has filters (k8s.namespace.name:web); repo scope checkout would set k8s.namespace.name:payments,k8s.workload.name:checkout; pass --repo-scope checkout to apply it")
	})
	t.Run("the same", func(t *testing.T) {
		srv := newLiveDebuggerStub(t, "readwrite-all", checkoutFilterSet)
		chdirTestRepo(t, breakpointScope)

		code, _, stderr := runRepoScoped([]string{"create", "breakpoint", "A.java:1", "--plain"}, RunOptions{})
		require.Zero(t, code, stderr)
		require.Empty(t, stderr)
		require.Equal(t, []string{"GetOrCreateWorkspaceV2", "CreateRule"}, srv.ops())
	})
}

// The entry discovery proposes binds a workload and a service name. The
// default sends only its workload set, says what it left out, and the whole
// entry takes --repo-scope, which says that its service id is not sent.
func TestCreateBreakpoint_RepoScopeDefaultSendsTheWorkloadSet(t *testing.T) {
	srv := newLiveDebuggerStub(t, "readwrite-all")
	chdirTestRepo(t, breakpointScope)
	chdirSubdir(t, filepath.Join("services", "split"))

	code, stdout, stderr := runRepoScoped([]string{"create", "breakpoint", "A.java:1", "--plain"}, RunOptions{})
	require.Zero(t, code, "stdout: %s\nstderr: %s", stdout, stderr)
	require.JSONEq(t, filterSetsPayload(t, splitFilterSets[0]), srv.sentFilterSets(t))
	require.Contains(t, stderr, "repo scope split's other filter sets (service.name:split) are set only with --repo-scope split")
	require.Contains(t, stdout, "workspace filters set from repo scope split: k8s.namespace.name:payments,k8s.workload.name:split")

	srv.reset()
	code, _, stderr = runRepoScoped([]string{"create", "breakpoint", "A.java:1", "--plain", "--repo-scope", "split"}, RunOptions{})
	require.Zero(t, code, stderr)
	require.JSONEq(t, filterSetsPayload(t, splitFilterSets...), srv.sentFilterSets(t))
	require.Equal(t, "repo scope split's service ids are not sent: Live Debugger does not honour dt.entity.service as a workspace label\n", stderr)
}

// A workspace that holds every set of the covering entry, from an earlier
// --repo-scope, is not warned about.
func TestCreateBreakpoint_RepoScopeKnowsTheWholeEntry(t *testing.T) {
	srv := newLiveDebuggerStub(t, "readwrite-all", splitFilterSets[1], splitFilterSets[0])
	chdirTestRepo(t, breakpointScope)
	chdirSubdir(t, filepath.Join("services", "split"))

	code, _, stderr := runRepoScoped([]string{"create", "breakpoint", "A.java:1", "--plain"}, RunOptions{})
	require.Zero(t, code, stderr)
	require.Empty(t, stderr)
	require.Equal(t, []string{"GetOrCreateWorkspaceV2", "CreateRule"}, srv.ops())
}

// An entry with a workload set per namespace has no one default set, so the
// caller has to ask for them.
func TestCreateBreakpoint_RepoScopeSeveralWorkloadSets(t *testing.T) {
	srv := newLiveDebuggerStub(t, "readwrite-all")
	chdirTestRepo(t, breakpointScope)
	chdirSubdir(t, filepath.Join("services", "regions"))

	code, stdout, _ := runRepoScoped([]string{"create", "breakpoint", "A.java:1", "--agent"}, RunOptions{})
	require.NotZero(t, code)
	require.Contains(t, agentError(t, stdout).Message, "repo scope regions has no single workload filter set (it renders k8s.namespace.name:payments,k8s.workload.name:regions; k8s.namespace.name:web,k8s.workload.name:regions): pass --repo-scope regions for all of them")
	require.NotContains(t, srv.ops(), "UpdateWorkspaceV2")
}

func TestBreakpoint_NamedRepoScope(t *testing.T) {
	srv := newLiveDebuggerStub(t, "readwrite-all", map[string][]string{"k8s.namespace.name": {"web"}})
	chdirTestRepo(t, breakpointScope)

	code, stdout, stderr := runRepoScoped([]string{"create", "breakpoint", "A.java:1", "--plain", "--repo-scope", "ledger", "--yes"}, RunOptions{})
	require.Zero(t, code, stderr)
	require.Empty(t, stderr)
	require.JSONEq(t, filterSetsPayload(t, ledgerFilterSet), srv.sentFilterSets(t), "a named entry replaces filters the workspace has")
	require.Contains(t, stdout, "workspace filters set from repo scope ledger: service.name:ledger")

	srv.reset()
	code, stdout, stderr = runRepoScoped([]string{"update", "breakpoint", "--plain", "--repo-scope", "ledger", "--yes"}, RunOptions{})
	require.Zero(t, code, stderr)
	require.Equal(t, []string{"GetOrCreateWorkspaceV2", "UpdateWorkspaceV2"}, srv.ops())
	require.JSONEq(t, filterSetsPayload(t, ledgerFilterSet), srv.sentFilterSets(t))
	require.Contains(t, stdout, "Updated Live Debugger workspace filters from repo scope ledger (service.name:ledger)")

	for _, tc := range []struct {
		name string
		argv []string
		code string
		msg  string
	}{
		{"unknown entry on create", []string{"create", "breakpoint", "A.java:1", "--repo-scope", "nope"}, "not_found", `repo scope "nope" not found`},
		{"unknown entry on update", []string{"update", "breakpoint", "--repo-scope", "nope"}, "not_found", `repo scope "nope" not found`},
		{"with --filters on create", []string{"create", "breakpoint", "A.java:1", "--repo-scope", "ledger", "--filters", "k:v"}, "", "[filters repo-scope] were all set"},
		{"with --filters on update", []string{"update", "breakpoint", "--repo-scope", "ledger", "--filters", "k:v"}, "", "[filters repo-scope] were all set"},
		{"with --condition on update", []string{"update", "breakpoint", "--repo-scope", "ledger", "--condition", "a==1"}, "",
			"--repo-scope sets the workspace's filters, shared by every breakpoint in it; change --condition, --log-message, or --enabled in a separate call"},
		{"with an identifier and --filters on update", []string{"update", "breakpoint", "A.java:1", "--filters", "k:v"}, "",
			"--filters sets the workspace's filters, shared by every breakpoint in it; run it without the breakpoint identifier"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv.reset()
			code, stdout, _ := runRepoScoped(append(tc.argv, "--agent"), RunOptions{})
			require.NotZero(t, code)
			detail := agentError(t, stdout)
			if tc.code != "" {
				require.Equal(t, tc.code, detail.Code)
			}
			require.Contains(t, detail.Message, tc.msg)
			require.Empty(t, srv.ops())
		})
	}
}

// A scope file that offers no entry here says why in the error, ahead of
// --filters, rather than in a warning beside it.
func TestCreateBreakpoint_RepoScopeSkipped(t *testing.T) {
	srv := newLiveDebuggerStub(t, "readwrite-all")
	chdirTestRepo(t, "environments:\n  127.0.0.1:\n    - name: ledger\n      path: services/ledger\n      service-names: [ledger]\n")
	chdirSubdir(t, "docs")

	code, stdout, stderr := runRepoScoped([]string{"create", "breakpoint", "A.java:1", "--agent"}, RunOptions{})
	require.NotZero(t, code)
	require.Equal(t, `no workspace filters configured: no entry covers "docs"; entries here cover services/ledger; pick one with --repo-scope <name>, or pass --filters key:value`,
		agentError(t, stdout).Message)
	require.Empty(t, stderr)
	require.Equal(t, []string{"GetOrCreateWorkspaceV2"}, srv.ops())
}

// Outside a git repository the default is off and the command is what it was;
// a named entry is an error, since there is no file to name it in.
func TestCreateBreakpoint_OutsideARepository(t *testing.T) {
	srv := newLiveDebuggerStub(t, "readwrite-all")
	t.Chdir(t.TempDir())

	code, stdout, stderr := runRepoScoped([]string{"create", "breakpoint", "A.java:1", "--agent"}, RunOptions{})
	require.NotZero(t, code)
	require.True(t, strings.HasPrefix(agentError(t, stdout).Message, noFiltersError), stdout)
	require.Empty(t, stderr)

	srv.reset()
	code, stdout, _ = runRepoScoped([]string{"create", "breakpoint", "A.java:1", "--repo-scope", "checkout", "--agent"}, RunOptions{})
	require.NotZero(t, code)
	require.Equal(t, "validation_error", agentError(t, stdout).Code)
	require.Empty(t, srv.ops())
}

// A dry run writes nothing and needs no safety level. Without a filter source
// it reads the workspace and says what the default will do to it.
func TestCreateBreakpoint_RepoScopeDryRun(t *testing.T) {
	for _, tc := range []struct {
		name    string
		dir     string
		args    []string
		filters []map[string][]string
		want    string // formatted with the workspace name
	}{
		{"a workspace without filters", "", nil, nil,
			"Dry run: would create breakpoint at A.java:1; would set workspace %s's filters from repo scope checkout: k8s.namespace.name:payments,k8s.workload.name:checkout"},
		{"the entry's set", "", nil, []map[string][]string{checkoutFilterSet},
			"Dry run: would create breakpoint at A.java:1; workspace %s keeps its filters (k8s.namespace.name:payments,k8s.workload.name:checkout)"},
		{"other filters", "", nil, []map[string][]string{{"k8s.namespace.name": {"web"}}},
			"Dry run: would create breakpoint at A.java:1; would warn: workspace %s holds k8s.namespace.name:web, repo scope checkout would set k8s.namespace.name:payments,k8s.workload.name:checkout"},
		{"no single set", filepath.Join("services", "regions"), nil, nil,
			"Dry run: creating breakpoint at A.java:1 in workspace %s would fail: no workspace filters configured; repo scope regions has no single workload filter set (it renders k8s.namespace.name:payments,k8s.workload.name:regions; k8s.namespace.name:web,k8s.workload.name:regions): pass --repo-scope regions for all of them, or --filters key:value"},
		{"no entry", "", []string{"--no-repo-scope"}, nil,
			"Dry run: creating breakpoint at A.java:1 in workspace %s would fail: no workspace filters configured; pass --filters key:value"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := newLiveDebuggerStub(t, "readonly", tc.filters...)
			repo := filepath.Base(chdirTestRepo(t, breakpointScope))
			entry := "checkout"
			if tc.dir != "" {
				chdirSubdir(t, tc.dir)
				entry = filepath.Base(tc.dir)
			}
			code, stdout, stderr := runRepoScoped(append([]string{"create", "breakpoint", "A.java:1", "--plain", "--dry-run"}, tc.args...), RunOptions{})
			require.Zero(t, code, stderr)
			require.Equal(t, fmt.Sprintf(tc.want, repo+"/"+entry), breakpointMessage(t, stdout))
			require.Equal(t, []string{"GetOrCreateWorkspaceV2"}, srv.ops(), "the preview reads the workspace and writes nothing")
		})
	}

	srv := newLiveDebuggerStub(t, "readonly")
	repo := filepath.Base(chdirTestRepo(t, breakpointScope))
	code, stdout, stderr := runRepoScoped([]string{"create", "breakpoint", "A.java:1", "--plain", "--dry-run", "--repo-scope", "ledger"}, RunOptions{})
	require.Zero(t, code, stderr)
	require.Contains(t, stdout, "Dry run: would set workspace "+repo+"/ledger's filters from repo scope ledger (service.name:ledger) and create breakpoint at A.java:1",
		"a named entry names the workspace, whichever entry covers the working directory")
	require.Empty(t, srv.ops())

	code, stdout, _ = runRepoScoped([]string{"create", "breakpoint", "A.java:1", "--agent"}, RunOptions{})
	require.NotZero(t, code)
	require.Contains(t, agentError(t, stdout).Message, "readonly", "the real run is still refused")
	require.Empty(t, srv.ops())

	// A preview never newly requires config: one that does not load leaves
	// the default off and the workspace unnamed.
	t.Setenv("DTCTL_CONFIG", filepath.Join(t.TempDir(), "missing.yaml"))
	code, stdout, stderr = runRepoScoped([]string{"create", "breakpoint", "A.java:1", "--plain", "--dry-run"}, RunOptions{})
	require.Zero(t, code, stderr)
	require.Equal(t, "Dry run: would create breakpoint at A.java:1", breakpointMessage(t, stdout))
	code, stdout, stderr = runRepoScoped([]string{"create", "breakpoint", "A.java:1", "--plain", "--dry-run", "--filters", "k8s.namespace.name:web"}, RunOptions{})
	require.Zero(t, code, stderr)
	require.Contains(t, breakpointMessage(t, stdout), "Dry run: would set workspace filters (k8s.namespace.name:web)")
}

// A client that does not build fails the preview, as a read that fails does
// and as delete --dry-run does: there is no workspace to say anything about.
func TestCreateBreakpoint_DryRunNeedsAClient(t *testing.T) {
	writeIsolatedConfig(t, "current-context: c\ncontexts:\n  - name: c\n    context:\n      environment: http://127.0.0.1:1\n      token-ref: missing\n      safety-level: readonly\n")
	optInRepoScope(t)
	chdirTestRepo(t, breakpointScope)
	code, stdout, _ := runRepoScoped([]string{"create", "breakpoint", "A.java:1", "--agent", "--dry-run"}, RunOptions{})
	require.NotZero(t, code, stdout)
	require.Contains(t, agentError(t, stdout).Message, "missing")
}

// breakpointMessage is the message of a --plain breakpoint result.
func breakpointMessage(t *testing.T, stdout string) string {
	t.Helper()
	var out breakpointCommandOutput
	require.NoError(t, json.Unmarshal([]byte(stdout), &out), "stdout: %s", stdout)
	return out.Message
}

// Naming is not scoping: opted out of the repo scope or not, every breakpoint
// command uses the workspace named after the repository and the entry
// covering the working directory, and says which.
func TestBreakpoint_WorkspaceNamedAfterTheEntry(t *testing.T) {
	srv := newLiveDebuggerStub(t, "readwrite-all", ledgerFilterSet)
	root := chdirTestRepo(t, breakpointScope)
	require.NoError(t, os.WriteFile(filepath.Join(root, ".git", "config"),
		[]byte("[remote \"origin\"]\n\turl = git@example.invalid:shop/storefront.git\n"), 0o644))
	chdirSubdir(t, filepath.Join("services", "ledger", "src"))

	for _, tc := range []struct {
		name   string
		argv   []string
		stdout string
		stderr string
	}{
		{"create", []string{"create", "breakpoint", "A.java:1", "--plain"}, "(workspace storefront/ledger)", ""},
		{"create --no-repo-scope", []string{"create", "breakpoint", "A.java:1", "--plain", "--no-repo-scope"}, "(workspace storefront/ledger)", ""},
		{"update", []string{"update", "breakpoint", "--plain", "--filters", "service.name:ledger", "--yes"}, "(workspace storefront/ledger)", ""},
		{"get", []string{"get", "breakpoints", "--no-agent"}, "", "workspace: storefront/ledger\n"},
		{"get --plain", []string{"get", "breakpoints", "--no-agent", "--plain"}, "", "workspace: storefront/ledger\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv.reset()
			code, stdout, stderr := runRepoScoped(tc.argv, RunOptions{})
			require.Zero(t, code, "stdout: %s\nstderr: %s", stdout, stderr)
			require.Equal(t, []string{"storefront/ledger"}, srv.projectPaths())
			require.Contains(t, stdout, tc.stdout)
			require.Equal(t, tc.stderr, stderr)
		})
	}
}

// --repo-scope names the workspace wherever the command runs, so a read
// command, or an update of one breakpoint, finds what a create with the same
// name put there; a name that does not resolve fails before any request.
func TestBreakpoint_WorkspaceNamedByRepoScope(t *testing.T) {
	srv := newLiveDebuggerStub(t, "readwrite-all")
	srv.rules = []interface{}{ruleAt("A.java", 1, "")}
	root := chdirTestRepo(t, breakpointScope)
	require.NoError(t, os.WriteFile(filepath.Join(root, ".git", "config"),
		[]byte("[remote \"origin\"]\n\turl = git@example.invalid:shop/storefront.git\n"), 0o644))
	chdirSubdir(t, filepath.Join("services", "ledger", "src"))

	for _, argv := range [][]string{
		{"create", "breakpoint", "A.java:1", "--plain", "--yes", "--repo-scope", "regions"},
		{"get", "breakpoints", "--plain", "--repo-scope", "regions"},
		{"update", "breakpoint", "A.java:1", "--enabled=false", "--plain", "--repo-scope", "regions"},
	} {
		srv.reset()
		code, stdout, stderr := runRepoScoped(argv, RunOptions{})
		require.Zero(t, code, "stdout: %s\nstderr: %s", stdout, stderr)
		require.Equal(t, []string{"storefront/regions"}, srv.projectPaths(), argv)
	}
	require.NotContains(t, srv.ops(), "UpdateWorkspaceV2", "with an identifier, update leaves the workspace's filters alone")

	srv.reset()
	code, stdout, _ := runRepoScoped([]string{"get", "breakpoints", "--repo-scope", "nope", "--agent"}, RunOptions{})
	require.NotZero(t, code)
	require.Equal(t, "not_found", agentError(t, stdout).Code)
	require.Empty(t, srv.ops())
}

// update applies a scope only when one is named, so it has no opt-out.
func TestUpdateBreakpoint_HasNoRepoScopeOptOut(t *testing.T) {
	newLiveDebuggerStub(t, "readwrite-all")
	chdirTestRepo(t, breakpointScope)

	code, stdout, _ := runRepoScoped([]string{"update", "breakpoint", "--no-repo-scope", "--filters", "k:v", "--agent"}, RunOptions{})
	require.NotZero(t, code)
	require.Contains(t, agentError(t, stdout).Message, "unknown flag --no-repo-scope")
}

func TestBreakpointRepoScopeFlagsAreExperimental(t *testing.T) {
	for _, tc := range []struct {
		cmd  string
		flag string
	}{
		{"create breakpoint", repoScopeFlag},
		{"create breakpoint", noRepoScopeFlag},
		{"update breakpoint", repoScopeFlag},
	} {
		c, _, err := rootCmd.Find(strings.Fields(tc.cmd))
		require.NoError(t, err)
		require.NotNil(t, c.Flags().Lookup(tc.flag), "%s --%s", tc.cmd, tc.flag)
		require.Equal(t, stability.Experimental, stability.OfFlag(c, tc.flag), "%s --%s", tc.cmd, tc.flag)
	}
}

// A workspace label that lists a value twice holds the same filter as the
// de-duplicated set dtctl renders; the sets compare in any order.
func TestWorkspaceHolds(t *testing.T) {
	label := func(field string, values ...string) livedebugger.FilterSet {
		return livedebugger.FilterSet{Labels: []livedebugger.FilterField{{Field: field, Values: values}}}
	}
	want := []map[string][]string{{"service.name": {"a"}}}
	require.True(t, workspaceHolds([]livedebugger.FilterSet{label("service.name", "a", "a")}, want))
	require.False(t, workspaceHolds([]livedebugger.FilterSet{label("service.name", "a", "b")}, want))

	two := []map[string][]string{{"service.name": {"a"}}, {"k8s.namespace.name": {"web"}}}
	require.True(t, workspaceHolds([]livedebugger.FilterSet{label("k8s.namespace.name", "web"), label("service.name", "a")}, two))
	require.False(t, workspaceHolds([]livedebugger.FilterSet{label("service.name", "a"), label("service.name", "a")}, two))
}
