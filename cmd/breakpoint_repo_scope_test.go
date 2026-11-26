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
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Dynatrace/dtctl/pkg/resources/livedebugger"
	"github.com/Dynatrace/dtctl/pkg/stability"
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

// The filter sets the entries of breakpointScope render, as the request and
// the response spell them.
const (
	checkoutSetJSON  = `{"filters":[],"labels":[{"field":"k8s.namespace.name","values":["payments"]},{"field":"k8s.workload.name","values":["checkout"]}]}`
	ledgerSetJSON    = `{"filters":[],"labels":[{"field":"service.name","values":["ledger"]}]}`
	splitWorkloadSet = `{"filters":[],"labels":[{"field":"k8s.namespace.name","values":["payments"]},{"field":"k8s.workload.name","values":["split"]}]}`
	splitServiceSet  = `{"filters":[],"labels":[{"field":"service.name","values":["split"]}]}`
	webSetJSON       = `{"filters":[],"labels":[{"field":"k8s.namespace.name","values":["web"]}]}`
)

// graphQLCall is one request the Live Debugger stub answered.
type graphQLCall struct {
	op        string
	mutation  bool
	variables map[string]interface{}
}

// liveDebuggerStub is a Live Debugger GraphQL endpoint. It keeps one
// workspace per project path, whose id it derives from the path, refuses a
// workspace id it did not issue, and keeps the filter sets an update sends.
// The client sends a query text and variables but no operation name, so each
// request is matched by the operation its text declares.
type liveDebuggerStub struct {
	*httptest.Server
	mu    sync.Mutex
	calls []graphQLCall
	// seed is the filter sets a workspace holds when it is first asked for.
	seed []interface{}
	// filterSets are the filter sets of each workspace, by project path.
	filterSets map[string][]interface{}
	// rules are breakpoints every workspace holds, in the response's shape.
	rules []interface{}
	// activeRules is how many more enabled breakpoints every workspace holds.
	activeRules int
	// fail makes an operation answer with this GraphQL error.
	fail map[string]string
}

// liveDebuggerOperations are the operations the stub answers, by the
// declaration that opens their query text.
var liveDebuggerOperations = []string{
	"query GetOrCreateWorkspaceV2(",
	"mutation UpdateWorkspaceV2(",
	"mutation CreateRule(",
	"query GetWorkspaceRules(",
	"query GetRuleStatusBreakdown(",
	"mutation EditRuleV2(",
	"mutation EnableOrDisableRules(",
	"mutation DeleteRule(",
	"mutation DeleteAllRulesFromWorkspace(",
}

// stubWorkspaceID is the id the stub issues for the workspace of a project
// path.
func stubWorkspaceID(projectPath string) string { return "ws:" + projectPath }

// newLiveDebuggerStub starts the stub and points an isolated config's only
// context at it, at the given safety level, with repo scoping opted in. Each
// of seed is one filter set, as JSON, that every workspace starts with.
func newLiveDebuggerStub(t *testing.T, safetyLevel string, seed ...string) *liveDebuggerStub {
	t.Helper()
	s := &liveDebuggerStub{seed: []interface{}{}, filterSets: map[string][]interface{}{}, fail: map[string]string{}}
	for _, set := range seed {
		var decoded interface{}
		require.NoError(t, json.Unmarshal([]byte(set), &decoded))
		s.seed = append(s.seed, decoded)
	}
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
		op, mutation := "", false
		for _, decl := range liveDebuggerOperations {
			if strings.HasPrefix(strings.TrimSpace(req.Query), decl) {
				kind, name, _ := strings.Cut(strings.TrimSuffix(decl, "("), " ")
				op, mutation = name, kind == "mutation"
			}
		}
		s.mu.Lock()
		defer s.mu.Unlock()
		s.calls = append(s.calls, graphQLCall{op: op, mutation: mutation, variables: req.Variables})
		w.Header().Set("Content-Type", "application/json")
		if msg := s.fail[op]; msg != "" {
			_ = json.NewEncoder(w).Encode(map[string]interface{}{"errors": []interface{}{map[string]interface{}{"message": msg}}})
			return
		}
		// Every operation but the one that issues a workspace id names the
		// workspace it works in by that id.
		path := ""
		if op != "GetOrCreateWorkspaceV2" && op != "GetRuleStatusBreakdown" {
			id, _ := req.Variables["workspaceId"].(string)
			path = strings.TrimPrefix(id, "ws:")
			if _, issued := s.filterSets[path]; !issued || id != stubWorkspaceID(path) {
				t.Errorf("%s names workspace id %q, which the stub did not issue", op, id)
				_ = json.NewEncoder(w).Encode(map[string]interface{}{"errors": []interface{}{map[string]interface{}{"message": "unknown workspace"}}})
				return
			}
		}
		workspace := func(body map[string]interface{}) map[string]interface{} {
			return map[string]interface{}{"org": map[string]interface{}{"workspace": body}}
		}
		var body interface{}
		switch op {
		case "GetOrCreateWorkspaceV2":
			input, _ := req.Variables["workspaceInput"].(map[string]interface{})
			path, _ = input["projectPath"].(string)
			if _, ok := s.filterSets[path]; !ok {
				s.filterSets[path] = s.seed
			}
			body = map[string]interface{}{"org": map[string]interface{}{"getOrCreateUserWorkspaceV2": map[string]interface{}{
				"id": stubWorkspaceID(path), "filterSets": s.filterSets[path],
			}}}
		case "UpdateWorkspaceV2":
			data, _ := req.Variables["data"].(map[string]interface{})
			sets, _ := data["filterSets"].([]interface{})
			s.filterSets[path] = sets
			body = map[string]interface{}{"org": map[string]interface{}{"updateWorkspaceV2": map[string]interface{}{
				"id": stubWorkspaceID(path), "filterSets": sets,
			}}}
		case "CreateRule":
			body = workspace(map[string]interface{}{"createRuleV2": map[string]interface{}{"id": "dtctl-rule-0001"}})
		case "GetWorkspaceRules":
			rules := append([]interface{}{}, s.rules...)
			for i := 0; i < s.activeRules; i++ {
				rules = append(rules, map[string]interface{}{"id": fmt.Sprintf("bp-%d", i), "is_disabled": false})
			}
			body = workspace(map[string]interface{}{"rules": rules})
		case "GetRuleStatusBreakdown":
			body = map[string]interface{}{"org": map[string]interface{}{"ruleStatuses": []interface{}{}}}
		case "EditRuleV2":
			body = workspace(map[string]interface{}{"editRuleV2": map[string]interface{}{"id": "dtctl-rule-0001"}})
		case "EnableOrDisableRules":
			body = workspace(map[string]interface{}{"enableOrDisableRules": []interface{}{}})
		case "DeleteRule":
			body = workspace(map[string]interface{}{"deleteRuleV2": true})
		case "DeleteAllRulesFromWorkspace":
			body = workspace(map[string]interface{}{"deleteAllRulesFromWorkspaceV2": []interface{}{"dtctl-rule-0001"}})
		default:
			t.Errorf("unexpected GraphQL request: %.80s", req.Query)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
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

// mutations lists the operations the stub was sent that change something.
func (s *liveDebuggerStub) mutations() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	ops := []string{}
	for _, c := range s.calls {
		if c.mutation {
			ops = append(ops, c.op)
		}
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

// workedIn lists, without repeats, the workspaces the requests that name one
// by id worked in, by project path.
func (s *liveDebuggerStub) workedIn() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	paths := []string{}
	for _, c := range s.calls {
		if id, ok := c.variables["workspaceId"].(string); ok {
			if path := strings.TrimPrefix(id, "ws:"); !slices.Contains(paths, path) {
				paths = append(paths, path)
			}
		}
	}
	return paths
}

// reset forgets the requests seen so far. The workspaces stay as they are.
func (s *liveDebuggerStub) reset() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls = nil
}

// filterSetsJSON is the JSON list of the given filter sets.
func filterSetsJSON(sets ...string) string {
	return "[" + strings.Join(sets, ",") + "]"
}

// mainNoFilters is the error of a create in a workspace without filters
// where no repo scope has anything to say, as it was before repo scopes.
const mainNoFilters = "no workspace filters configured; set filters first with:\n  dtctl update breakpoint --filters key:value\nor pass --filters when creating:\n  dtctl create breakpoint A.java:1 --filters key:value"

// Nothing sets a workspace's filters unless the user names them: in a linked
// repository a create without --filters or --repo-scope writes no filters,
// whatever the workspace holds and however it is run, and says in the error
// that the covering entry exists and how to apply it.
func TestCreateBreakpoint_SetsNoFiltersUnasked(t *testing.T) {
	for _, tc := range []struct {
		name string
		dir  string
		want string // formatted with the workspace name
	}{
		{"an entry covers the directory", "",
			"workspace %s has no filters, and a breakpoint needs them: repo scope checkout covers this directory, so pass --repo-scope checkout to set its filters (k8s.namespace.name:payments,k8s.workload.name:checkout), or --filters key:value"},
		{"an entry with several sets", filepath.Join("services", "regions"),
			"workspace %s has no filters, and a breakpoint needs them: repo scope regions covers this directory, so pass --repo-scope regions to set its filters (k8s.namespace.name:payments,k8s.workload.name:regions; k8s.namespace.name:web,k8s.workload.name:regions), or --filters key:value"},
	} {
		for _, mode := range [][]string{{"--agent"}, {"--agent", "--yes"}, {"--plain"}, {"--no-agent", "--yes"}, {"--no-agent"}} {
			t.Run(tc.name+" "+strings.Join(mode, " "), func(t *testing.T) {
				srv := newLiveDebuggerStub(t, "readwrite-all")
				srv.activeRules = 1
				repo := filepath.Base(chdirTestRepo(t, breakpointScope))
				entry := "checkout"
				if tc.dir != "" {
					chdirSubdir(t, tc.dir)
					entry = filepath.Base(tc.dir)
				}

				code, stdout, stderr := runRepoScoped(append([]string{"create", "breakpoint", "A.java:1"}, mode...), RunOptions{Stdin: strings.NewReader("y\n")})
				require.Equal(t, 2, code, "stdout: %s\nstderr: %s", stdout, stderr)
				want := fmt.Sprintf(tc.want, repo+"/"+entry)
				if mode[0] == "--agent" {
					detail := agentError(t, stdout)
					require.Equal(t, "validation_error", detail.Code)
					require.Equal(t, want, detail.Message)
				} else {
					require.Contains(t, stdout+stderr, want)
				}
				require.Equal(t, []string{"GetOrCreateWorkspaceV2"}, srv.ops(), "the workspace is read and nothing is written")
			})
		}
	}
}

// A workspace that has filters gets its breakpoint and keeps them. When the
// entry covering the directory renders other filters, one line says so and
// names the command that applies them: an update, since the breakpoint
// exists by then. In agent mode the line is a suggestion.
func TestCreateBreakpoint_HintsAtTheCoveringEntry(t *testing.T) {
	const hint = "workspace %s's filters (k8s.namespace.name:web) are not the ones repo scope checkout, which covers this directory, renders (k8s.namespace.name:payments,k8s.workload.name:checkout); to apply them: dtctl update breakpoint --repo-scope checkout"
	t.Run("other filters", func(t *testing.T) {
		srv := newLiveDebuggerStub(t, "readwrite-all", webSetJSON)
		repo := filepath.Base(chdirTestRepo(t, breakpointScope))
		want := fmt.Sprintf(hint, repo+"/checkout")

		code, stdout, stderr := runRepoScoped([]string{"create", "breakpoint", "A.java:1", "--plain"}, RunOptions{})
		require.Zero(t, code, stderr)
		require.Equal(t, want+"\n", stderr)
		require.Equal(t, []string{"GetOrCreateWorkspaceV2", "CreateRule"}, srv.ops())
		require.Equal(t, "Created breakpoint at A.java:1 (id: dtctl-rule-0001) (workspace "+repo+"/checkout)", breakpointMessage(t, stdout))

		code, stdout, stderr = runRepoScoped([]string{"create", "breakpoint", "A.java:1", "--agent"}, RunOptions{})
		require.Zero(t, code, stderr)
		require.Equal(t, []string{want}, agentEnvelope(t, stdout).Context.Suggestions)
	})
	for name, seed := range map[string][]string{
		"the entry's filters":         {checkoutSetJSON},
		"every set of the entry":      nil, // seeded below, in the split directory
		"the hint turned off":         {webSetJSON},
		"a floor that hides the flag": {webSetJSON},
	} {
		t.Run(name, func(t *testing.T) {
			if seed == nil {
				seed = []string{splitServiceSet, splitWorkloadSet}
			}
			srv := newLiveDebuggerStub(t, "readwrite-all", seed...)
			chdirTestRepo(t, breakpointScope)
			var env map[string]string
			switch name {
			case "every set of the entry":
				chdirSubdir(t, filepath.Join("services", "split"))
			case "the hint turned off":
				env = map[string]string{noRepoScopeEnv: "1"}
			case "a floor that hides the flag":
				writeIsolatedConfig(t, strings.Replace(safetyLevelConfig(srv.URL, "readwrite-all"), "      safety-level:",
					"      min-stability: stable\n      stability-exceptions: [create breakpoint]\n      safety-level:", 1))
			}

			code, stdout, stderr := runRepoScoped([]string{"create", "breakpoint", "A.java:1", "--agent"}, RunOptions{Env: env})
			require.Zero(t, code, "stdout: %s\nstderr: %s", stdout, stderr)
			require.Empty(t, stderr)
			require.Empty(t, agentEnvelope(t, stdout).Context.Suggestions)
			require.Equal(t, []string{"GetOrCreateWorkspaceV2", "CreateRule"}, srv.ops())
		})
	}
}

// Where the hint is off, a workspace without filters fails as it did before
// repo scopes existed. create breakpoint has no --no-repo-scope: there is no
// default to turn off.
func TestCreateBreakpoint_RepoScopeOff(t *testing.T) {
	srv := newLiveDebuggerStub(t, "readwrite-all")
	chdirTestRepo(t, breakpointScope)

	code, stdout, stderr := runRepoScoped([]string{"create", "breakpoint", "A.java:1", "--agent"}, RunOptions{Env: map[string]string{noRepoScopeEnv: "1"}})
	require.Equal(t, 2, code)
	require.Equal(t, mainNoFilters, agentError(t, stdout).Message)
	require.Equal(t, "validation_error", agentError(t, stdout).Code)
	require.Empty(t, stderr)
	require.Equal(t, []string{"GetOrCreateWorkspaceV2"}, srv.ops())

	srv.reset()
	code, stdout, _ = runRepoScoped([]string{"create", "breakpoint", "A.java:1", "--agent", "--no-repo-scope"}, RunOptions{})
	require.NotZero(t, code)
	require.Contains(t, agentError(t, stdout).Message, "unknown flag --no-repo-scope")
	require.Empty(t, srv.ops())
}

// A workspace is not narrowed by cluster: the label is not sent, since
// nothing confirms a workspace honours it and a set that selects nothing
// would be a breakpoint that never hits. Every path that sets filters from
// an entry binding a cluster says so, the dry runs included.
func TestBreakpoint_RepoScopeClusterIsNotSent(t *testing.T) {
	const scope = `environments:
  127.0.0.1:
    - name: checkout
      workloads:
        - cluster-uid: 11111111-2222-3333-4444-555555555555
          namespace: payments
          name: checkout
`
	const note = "repo scope checkout binds a cluster, which a Live Debugger workspace is not narrowed by: the filters include the same workload in other clusters\n"
	for _, tc := range []struct {
		name  string
		args  []string
		sends bool
	}{
		{"create named", []string{"create", "breakpoint", "A.java:1", "--repo-scope", "checkout", "--yes"}, true},
		{"create named, dry run", []string{"create", "breakpoint", "A.java:1", "--repo-scope", "checkout", "--dry-run"}, false},
		{"update named", []string{"update", "breakpoint", "--repo-scope", "checkout", "--yes"}, true},
		{"update named, dry run", []string{"update", "breakpoint", "--repo-scope", "checkout", "--dry-run"}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := newLiveDebuggerStub(t, "readwrite-all")
			chdirTestRepo(t, scope)
			code, stdout, stderr := runRepoScoped(append(tc.args, "--plain"), RunOptions{})
			require.Zero(t, code, "stdout: %s\nstderr: %s", stdout, stderr)
			require.Equal(t, note, stderr)
			require.NotContains(t, stdout, "k8s.cluster.uid")
			if tc.sends {
				require.JSONEq(t, filterSetsJSON(checkoutSetJSON), srv.sentFilterSets(t))
			} else {
				require.Empty(t, srv.ops())
			}
		})
	}
}

// A named entry sends every set it renders, and says that its service ids
// are not among them.
func TestCreateBreakpoint_NamedRepoScopeSendsEverySet(t *testing.T) {
	srv := newLiveDebuggerStub(t, "readwrite-all")
	chdirTestRepo(t, breakpointScope)

	code, stdout, stderr := runRepoScoped([]string{"create", "breakpoint", "A.java:1", "--plain", "--repo-scope", "split"}, RunOptions{})
	require.Zero(t, code, stderr)
	require.Equal(t, []string{"GetOrCreateWorkspaceV2", "UpdateWorkspaceV2", "CreateRule"}, srv.ops())
	require.JSONEq(t, filterSetsJSON(splitWorkloadSet, splitServiceSet), srv.sentFilterSets(t))
	require.Equal(t, "repo scope split's service ids are not sent: Live Debugger does not honour dt.entity.service as a workspace label\n", stderr)
	require.Contains(t, stdout, "workspace filters set from repo scope split: k8s.namespace.name:payments,k8s.workload.name:split; service.name:split")
}

func TestBreakpoint_NamedRepoScope(t *testing.T) {
	srv := newLiveDebuggerStub(t, "readwrite-all", webSetJSON)
	chdirTestRepo(t, breakpointScope)

	code, stdout, stderr := runRepoScoped([]string{"create", "breakpoint", "A.java:1", "--plain", "--repo-scope", "ledger", "--yes"}, RunOptions{})
	require.Zero(t, code, stderr)
	require.Empty(t, stderr)
	require.JSONEq(t, filterSetsJSON(ledgerSetJSON), srv.sentFilterSets(t), "a named entry replaces filters the workspace has")
	require.Contains(t, stdout, "workspace filters set from repo scope ledger: service.name:ledger")

	srv.reset()
	code, stdout, stderr = runRepoScoped([]string{"update", "breakpoint", "--plain", "--repo-scope", "ledger", "--yes"}, RunOptions{})
	require.Zero(t, code, stderr)
	require.Equal(t, []string{"GetOrCreateWorkspaceV2", "UpdateWorkspaceV2"}, srv.ops())
	require.JSONEq(t, filterSetsJSON(ledgerSetJSON), srv.sentFilterSets(t))
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

// A linked repository whose entries do not cover the directory says so in
// the error, ahead of --filters.
func TestCreateBreakpoint_RepoScopeCoversNothingHere(t *testing.T) {
	srv := newLiveDebuggerStub(t, "readwrite-all")
	repo := filepath.Base(chdirTestRepo(t, "environments:\n  127.0.0.1:\n    - name: ledger\n      path: services/ledger\n      service-names: [ledger]\n"))
	chdirSubdir(t, "docs")

	code, stdout, stderr := runRepoScoped([]string{"create", "breakpoint", "A.java:1", "--agent"}, RunOptions{})
	require.NotZero(t, code)
	require.Equal(t, "workspace "+repo+` has no filters, and a breakpoint needs them: no entry covers "docs"; entries here cover services/ledger; pick one with --repo-scope <name>, or pass --filters key:value`,
		agentError(t, stdout).Message)
	require.Empty(t, stderr)
	require.Equal(t, []string{"GetOrCreateWorkspaceV2"}, srv.ops())
}

// Outside a git repository the command is what it was; a named entry is an
// error, since there is no file to name it in.
func TestCreateBreakpoint_OutsideARepository(t *testing.T) {
	srv := newLiveDebuggerStub(t, "readwrite-all")
	t.Chdir(t.TempDir())

	code, stdout, stderr := runRepoScoped([]string{"create", "breakpoint", "A.java:1", "--agent"}, RunOptions{})
	require.NotZero(t, code)
	require.Equal(t, mainNoFilters, agentError(t, stdout).Message)
	require.Empty(t, stderr)

	srv.reset()
	code, stdout, _ = runRepoScoped([]string{"create", "breakpoint", "A.java:1", "--repo-scope", "checkout", "--agent"}, RunOptions{})
	require.NotZero(t, code)
	require.Equal(t, "validation_error", agentError(t, stdout).Code)
	require.Empty(t, srv.ops())
}

// A dry run sends nothing, whatever the workspace holds, and needs no safety
// level. Without a filter source it cannot know whether the workspace has
// filters, so it says that the breakpoint needs them and what would set
// them.
func TestCreateBreakpoint_RepoScopeDryRun(t *testing.T) {
	for _, tc := range []struct {
		name string
		dir  string
		env  map[string]string
		want string // formatted with the workspace name
	}{
		{"an entry covers the directory", "", nil,
			"Dry run: would create breakpoint at A.java:1 in workspace %s, which must have filters; repo scope checkout covers this directory, and --repo-scope checkout sets its filters (k8s.namespace.name:payments,k8s.workload.name:checkout)"},
		{"several sets", filepath.Join("services", "regions"), nil,
			"Dry run: would create breakpoint at A.java:1 in workspace %s, which must have filters; repo scope regions covers this directory, and --repo-scope regions sets its filters (k8s.namespace.name:payments,k8s.workload.name:regions; k8s.namespace.name:web,k8s.workload.name:regions)"},
		{"the hint turned off", "", map[string]string{noRepoScopeEnv: "1"},
			"Dry run: would create breakpoint at A.java:1 in workspace %s, which must have filters; --filters key:value sets them"},
	} {
		for _, filters := range [][]string{nil, {webSetJSON}} {
			t.Run(tc.name, func(t *testing.T) {
				srv := newLiveDebuggerStub(t, "readonly", filters...)
				srv.activeRules = 2
				repo := filepath.Base(chdirTestRepo(t, breakpointScope))
				entry := "checkout"
				if tc.dir != "" {
					chdirSubdir(t, tc.dir)
					entry = filepath.Base(tc.dir)
				}
				code, stdout, stderr := runRepoScoped([]string{"create", "breakpoint", "A.java:1", "--plain", "--dry-run"}, RunOptions{Env: tc.env})
				require.Zero(t, code, stderr)
				require.Equal(t, fmt.Sprintf(tc.want, repo+"/"+entry), breakpointMessage(t, stdout))
				require.Empty(t, srv.ops(), "a preview sends nothing: asking for a workspace may create it")
			})
		}
	}

	t.Run("outside a repository", func(t *testing.T) {
		srv := newLiveDebuggerStub(t, "readonly")
		t.Chdir(t.TempDir())
		code, stdout, stderr := runRepoScoped([]string{"create", "breakpoint", "A.java:1", "--plain", "--dry-run"}, RunOptions{})
		require.Zero(t, code, stderr)
		require.Contains(t, breakpointMessage(t, stdout), ", which must have filters; --filters key:value sets them")
		require.Empty(t, srv.ops())
	})

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

	// A preview never newly requires config, nor a token: with a config
	// that does not load the workspace goes unnamed.
	t.Setenv("DTCTL_CONFIG", filepath.Join(t.TempDir(), "missing.yaml"))
	code, stdout, stderr = runRepoScoped([]string{"create", "breakpoint", "A.java:1", "--plain", "--dry-run"}, RunOptions{})
	require.Zero(t, code, stderr)
	require.Equal(t, "Dry run: would create breakpoint at A.java:1", breakpointMessage(t, stdout))
	code, stdout, stderr = runRepoScoped([]string{"create", "breakpoint", "A.java:1", "--plain", "--dry-run", "--filters", "k8s.namespace.name:web"}, RunOptions{})
	require.Zero(t, code, stderr)
	require.Contains(t, breakpointMessage(t, stdout), "Dry run: would set workspace filters (k8s.namespace.name:web)")

	writeIsolatedConfig(t, "current-context: c\ncontexts:\n  - name: c\n    context:\n      environment: http://127.0.0.1:1\n      token-ref: missing\n      safety-level: readonly\n")
	code, stdout, stderr = runRepoScoped([]string{"create", "breakpoint", "A.java:1", "--plain", "--dry-run"}, RunOptions{})
	require.Zero(t, code, stderr)
	require.Contains(t, breakpointMessage(t, stdout), "Dry run: would create breakpoint at A.java:1 in workspace "+repo+"/checkout")
}

// breakpointMessage is the message of a --plain breakpoint result.
func breakpointMessage(t *testing.T, stdout string) string {
	t.Helper()
	var out breakpointCommandOutput
	require.NoError(t, json.Unmarshal([]byte(stdout), &out), "stdout: %s", stdout)
	return out.Message
}

// Every breakpoint command uses the workspace named after the repository and
// the entry covering the working directory, and says which.
func TestBreakpoint_WorkspaceNamedAfterTheEntry(t *testing.T) {
	srv := newLiveDebuggerStub(t, "readwrite-all", ledgerSetJSON)
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

// The origin remote is text from a file, and only a plain repository name is
// taken from it: anything else would put a query string, a quote or a
// directory of this machine into the name that is sent and printed, so the
// checkout directory names the repository instead.
func TestBreakpoint_WorkspaceNameTakesOnlyAPlainRepositoryName(t *testing.T) {
	for _, tc := range []struct{ name, url string }{
		{"a query string", "git@example.invalid:shop/cart.git?ticket=example"},
		{"a quoted url", `"https://example.invalid/shop/storefront.git"`},
		{"a parent directory", "https://example.invalid/shop/.."},
		{"a Windows path", `C:\Users\dev\repos\storefront.git`},
		{"a space", "https://example.invalid/shop/store%20front.git"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := newLiveDebuggerStub(t, "readwrite-all", webSetJSON)
			root := chdirTestRepo(t, breakpointScope)
			require.NoError(t, os.WriteFile(filepath.Join(root, ".git", "config"), []byte("[remote \"origin\"]\n\turl = "+tc.url+"\n"), 0o644))
			want := filepath.Base(root) + "/checkout"

			code, stdout, stderr := runRepoScoped([]string{"create", "breakpoint", "A.java:1", "--plain"}, RunOptions{})
			require.Zero(t, code, "stdout: %s\nstderr: %s", stdout, stderr)
			require.Equal(t, []string{want}, srv.projectPaths())
			require.Contains(t, breakpointMessage(t, stdout), "(workspace "+want+")")
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
