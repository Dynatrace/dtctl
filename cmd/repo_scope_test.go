package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"

	"github.com/Dynatrace/dtctl/cmd/testutil"
	"github.com/Dynatrace/dtctl/pkg/client"
	"github.com/Dynatrace/dtctl/pkg/output"
	"github.com/Dynatrace/dtctl/pkg/reposcope"
)

// repoScopeTestEnvironment, and its host, are the context environment for the
// tests that never reach the network: set, delete, current, describe and list
// read and write only the local file.
const (
	repoScopeTestEnvironment = "https://abc12345.apps.dynatrace.com"
	repoScopeTestEnvHost     = "abc12345.apps.dynatrace.com"
)

// monorepoScope links two directories of one repository and nothing else.
const monorepoScope = `environments:
  abc12345.apps.dynatrace.com:
    - name: checkout
      path: services/checkout
      service-names: [checkout]
    - name: ledger
      path: services/ledger
      workloads:
        - namespace: payments
          name: ledger
`

// localRepoScopeConfig points the only context at an environment no test
// talks to, at the strictest safety level: none of the local commands may
// need more.
func localRepoScopeConfig(t *testing.T) {
	t.Helper()
	writeIsolatedConfig(t, safetyLevelConfig(repoScopeTestEnvironment, "readonly"))
}

// chdirTestRepo makes a fresh directory holding a .git the working directory,
// with scopeFile as its .dtctl-repo-scope.yaml unless it is empty. Every test
// runs inside one, so none can find the dtctl checkout's own .git.
func chdirTestRepo(t *testing.T, scopeFile string) string {
	t.Helper()
	dir := t.TempDir()
	require.NoError(t, os.Mkdir(filepath.Join(dir, ".git"), 0o755))
	if scopeFile != "" {
		require.NoError(t, os.WriteFile(filepath.Join(dir, reposcope.FileName), []byte(scopeFile), 0o644))
	}
	t.Chdir(dir)
	return dir
}

// chdirSubdir makes dir (relative to the working directory) and moves into it.
func chdirSubdir(t *testing.T, dir string) {
	t.Helper()
	require.NoError(t, os.MkdirAll(dir, 0o755))
	t.Chdir(dir)
}

// runRepoScoped runs argv and returns its exit code and streams.
func runRepoScoped(argv []string, opts RunOptions) (code int, stdout, stderr string) {
	var out, errOut bytes.Buffer
	opts.Stdout, opts.Stderr = &out, &errOut
	code = Run(argv, opts)
	return code, out.String(), errOut.String()
}

func agentEnvelope(t *testing.T, stdout string) output.Response {
	t.Helper()
	var resp output.Response
	require.NoError(t, json.Unmarshal([]byte(stdout), &resp), "stdout: %s", stdout)
	return resp
}

func agentError(t *testing.T, stdout string) *output.ErrorDetail {
	t.Helper()
	resp := agentEnvelope(t, stdout)
	require.False(t, resp.OK, "stdout: %s", stdout)
	require.NotNil(t, resp.Error)
	return resp.Error
}

// agentResult decodes an agent envelope's result into v.
func agentResult(t *testing.T, stdout string, v any) output.Response {
	t.Helper()
	resp := agentEnvelope(t, stdout)
	raw, err := json.Marshal(resp.Result)
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(raw, v))
	return resp
}

func readScopeFile(t *testing.T, repo string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(repo, reposcope.FileName))
	require.NoError(t, err)
	return data
}

func loadScopeFile(t *testing.T, repo string) *reposcope.File {
	t.Helper()
	f, err := reposcope.Load(reposcope.Location{Path: reposcope.FileName})
	require.NoError(t, err)
	return f
}

// dqlRecorder is a mock environment that records the text of every query
// it is sent and answers query:execute and query:verify through respond.
type dqlRecorder struct {
	*httptest.Server
	mu   sync.Mutex
	sent []string
	// all is every request the server got, answered or not: method, path
	// and body.
	all []string
	// segments are the filter segments the environment holds, by id, with
	// the status reading one answers; any other id is a 404.
	segments map[string]int
}

// newDQLRecorder starts the server and points an isolated config's only
// context at it, at the given safety level. It also calls optInRepoScope: a
// test that sends a query here is usually about what the scope does to it. A
// test that brings its own server has to opt in itself, or every "not scoped"
// assertion it makes passes whatever the scoping does.
func newDQLRecorder(t *testing.T, safetyLevel string, respond func(r *http.Request, query string) (int, string)) *dqlRecorder {
	t.Helper()
	s := &dqlRecorder{}
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		s.mu.Lock()
		s.all = append(s.all, fmt.Sprintf("%s %s %s", r.Method, r.URL.RequestURI(), raw))
		s.mu.Unlock()
		if id, ok := strings.CutPrefix(r.URL.Path, "/platform/storage/filter-segments/v1/filter-segments/"); ok && r.Method == http.MethodGet {
			s.mu.Lock()
			status, known := s.segments[id]
			s.mu.Unlock()
			if known {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(status)
				_, _ = fmt.Fprintf(w, `{"uid":%q,"name":"a segment"}`, id)
				return
			}
		}
		if r.URL.Path == "/platform/storage/filter-segments/v1/filter-segments" && r.Method == http.MethodGet {
			s.mu.Lock()
			var list []string
			for _, id := range slices.Sorted(maps.Keys(s.segments)) {
				list = append(list, fmt.Sprintf(`{"uid":%q,"name":"a segment"}`, id))
			}
			s.mu.Unlock()
			if list != nil {
				w.Header().Set("Content-Type", "application/json")
				_, _ = fmt.Fprintf(w, `{"filterSegments":[%s]}`, strings.Join(list, ","))
				return
			}
		}
		if !strings.HasSuffix(r.URL.Path, "/query:execute") && !strings.HasSuffix(r.URL.Path, "/query:verify") {
			http.NotFound(w, r)
			return
		}
		var req struct {
			Query string `json:"query"`
		}
		_ = json.Unmarshal(raw, &req)
		s.mu.Lock()
		s.sent = append(s.sent, req.Query)
		s.mu.Unlock()
		status, body := respond(r, req.Query)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(s.Close)
	writeIsolatedConfig(t, safetyLevelConfig(s.URL, safetyLevel))
	optInRepoScope(t)
	t.Cleanup(func() { restorePristineTree(context.Background()) })
	return s
}

// optInRepoScope turns default repo scoping back on for t. TestMain turns it
// off for the package, so that no test is scoped by a file it did not write.
func optInRepoScope(t *testing.T) {
	t.Helper()
	t.Setenv(noRepoScopeEnv, "")
}

// answer responds to every query with status and body.
func answer(status int, body string) func(*http.Request, string) (int, string) {
	return func(*http.Request, string) (int, string) { return status, body }
}

// rowsByObject responds with the records in rows for the data object a query
// fetches, and none for any other.
func rowsByObject(rows map[string]string) func(*http.Request, string) (int, string) {
	return func(_ *http.Request, query string) (int, string) {
		object := strings.TrimSuffix(strings.Fields(query + " ,")[1], ",")
		return http.StatusOK, fmt.Sprintf(`{"state":"SUCCEEDED","result":{"records":[%s]}}`, rows[object])
	}
}

// spansByName answers a spans query with the rows whose key is a value the
// query carries, in the order of the keys, and every other query with
// nothing. Unlike rowsByObject it returns what was asked for, so two units
// discovered in one run get different answers.
func spansByName(rows map[string]string) func(*http.Request, string) (int, string) {
	return func(_ *http.Request, query string) (int, string) {
		var found []string
		for _, name := range slices.Sorted(maps.Keys(rows)) {
			if strings.HasPrefix(query, "fetch spans") && strings.Contains(query, `"`+name+`"`) {
				found = append(found, rows[name])
			}
		}
		return http.StatusOK, fmt.Sprintf(`{"state":"SUCCEEDED","result":{"records":[%s]}}`, strings.Join(found, ","))
	}
}

// requests is every request the server was sent, whole: a test that claims
// nothing travels beside the query compares these, not the query text.
func (s *dqlRecorder) requests() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.all...)
}

func (s *dqlRecorder) queries() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.sent...)
}

// first is the first query the server was sent; a zero-row result is followed
// by the empty-result probe, which is not what these tests are about.
func (s *dqlRecorder) first(t *testing.T) string {
	t.Helper()
	sent := s.queries()
	require.NotEmpty(t, sent, "no query reached the server")
	return sent[0]
}

func (s *dqlRecorder) reset() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sent = nil
}

func TestRepoScopeSet(t *testing.T) {
	localRepoScopeConfig(t)
	repo := chdirTestRepo(t, monorepoScope)

	code, stdout, stderr := runRepoScoped([]string{"repo-scope", "set", "checkout",
		"--service", "SERVICE-0123456789ABCDEF", "--process-group", "PROCESS_GROUP-FEDCBA9876543210",
		"--namespace", "payments", "--workload", "checkout", "--service-name", "checkout",
		"--path", "./services/checkout/"}, RunOptions{})
	require.Zero(t, code, stderr)
	require.Empty(t, stdout)
	require.Contains(t, stderr, `Repo scope "checkout" saved to .dtctl-repo-scope.yaml for abc12345.apps.dynatrace.com`)
	require.Equal(t, []reposcope.Entry{
		{
			Name:          "checkout",
			Path:          "services/checkout",
			Services:      []string{"SERVICE-0123456789ABCDEF"},
			ProcessGroups: []string{"PROCESS_GROUP-FEDCBA9876543210"},
			ServiceNames:  []string{"checkout"},
			Workloads:     []reposcope.Workload{{Namespace: "payments", Name: "checkout"}},
		},
		{Name: "ledger", Path: "services/ledger", Workloads: []reposcope.Workload{{Namespace: "payments", Name: "ledger"}}},
	}, loadScopeFile(t, repo).Entries(repoScopeTestEnvHost), "set replaces the named entry whole and keeps the others")
}

// A shell on macOS completes a directory name in the decomposed spelling the
// filesystem may hold; the file takes the composed one, so set writes that.
func TestRepoScopeSetComposesThePath(t *testing.T) {
	localRepoScopeConfig(t)
	repo := chdirTestRepo(t, "")
	code, _, stderr := runRepoScoped([]string{"repo-scope", "set", "cafe", "--service-name", "cafe", "--path", "services/cafe\u0301"}, RunOptions{})
	require.Zero(t, code, stderr)
	require.Equal(t, "services/caf\u00e9", loadScopeFile(t, repo).Entries(repoScopeTestEnvHost)[0].Path)
}

// --cluster-uid qualifies the --workload names, as --namespace does.
func TestRepoScopeSetClusterUID(t *testing.T) {
	const uid = "11111111-2222-3333-4444-555555555555"
	localRepoScopeConfig(t)
	repo := chdirTestRepo(t, "")

	code, _, stderr := runRepoScoped([]string{"repo-scope", "set", "checkout",
		"--cluster-uid", uid, "--namespace", "payments", "--workload", "checkout", "--workload", "checkout-worker"}, RunOptions{})
	require.Zero(t, code, stderr)
	require.Equal(t, []reposcope.Entry{{Name: "checkout", Workloads: []reposcope.Workload{
		{ClusterUID: uid, Namespace: "payments", Name: "checkout"},
		{ClusterUID: uid, Namespace: "payments", Name: "checkout-worker"},
	}}}, loadScopeFile(t, repo).Entries(repoScopeTestEnvHost))
	before := readScopeFile(t, repo)

	// A workload given twice is refused, as a segment given twice is.
	code, stdout, _ := runRepoScoped([]string{"repo-scope", "set", "checkout", "--agent",
		"--namespace", "payments", "--workload", "checkout", "--workload", "checkout"}, RunOptions{})
	require.NotZero(t, code)
	require.True(t, strings.HasSuffix(agentError(t, stdout).Message,
		`.dtctl-repo-scope.yaml: environments[abc12345.apps.dynatrace.com] entry "checkout": workloads[1] payments/checkout is listed twice`), stdout)
	require.Equal(t, before, readScopeFile(t, repo))

	// A cluster narrows a workload, so alone it binds nothing.
	code, stdout, _ = runRepoScoped([]string{"repo-scope", "set", "checkout", "--agent", "--service-name", "checkout", "--cluster-uid", uid}, RunOptions{})
	require.NotZero(t, code)
	detail := agentError(t, stdout)
	require.Equal(t, "validation_error", detail.Code)
	require.Equal(t, "repo-scope set: --cluster-uid narrows the --workload names; pass --namespace and --workload with it", detail.Message)
	require.Equal(t, before, readScopeFile(t, repo))
}

// set --segment takes the spelling query --segment does, and saves the
// reference as data. A segment is given by id: set sends nothing, so it
// cannot look a name up, and a committed name could change its meaning.
func TestRepoScopeSetSegments(t *testing.T) {
	localRepoScopeConfig(t)
	repo := chdirTestRepo(t, "")

	code, _, stderr := runRepoScoped([]string{"repo-scope", "set", "checkout", "--service-name", "checkout",
		"--segment", "Ab3dEf9hIjk?environment=production,staging&k8s.namespace.name=payments", "--segment", "plain-1"}, RunOptions{})
	require.Zero(t, code, stderr)
	// A segment alone makes an entry.
	code, _, stderr = runRepoScoped([]string{"repo-scope", "set", "tools", "--path", "tools", "--segment", "plain-1"}, RunOptions{})
	require.Zero(t, code, stderr)
	require.Equal(t, []reposcope.Entry{
		{Name: "checkout", ServiceNames: []string{"checkout"}, Segments: []reposcope.Segment{
			{ID: "Ab3dEf9hIjk", Variables: map[string][]string{"environment": {"production", "staging"}, "k8s.namespace.name": {"payments"}}},
			{ID: "plain-1"},
		}},
		{Name: "tools", Path: "tools", Segments: []reposcope.Segment{{ID: "plain-1"}}},
	}, loadScopeFile(t, repo).Entries(repoScopeTestEnvHost))
	before := readScopeFile(t, repo)

	for name, tc := range map[string]struct {
		segment, message string
	}{
		"a name":           {"Payments prod", `entry "checkout": segments[0] id "Payments prod" is not a filter segment id (letters, digits, - and _; a name is not accepted)`},
		"a variable twice": {"Ab3dEf9hIjk?env=a&env=b", `repo-scope set: invalid --segment "Ab3dEf9hIjk?env=a&env=b": variable "env" is given twice; list its values as env=a,b`},
		"no value":         {"Ab3dEf9hIjk?env=", `repo-scope set: invalid --segment "Ab3dEf9hIjk?env=": variable "env" value must not be empty`},
	} {
		code, stdout, _ := runRepoScoped([]string{"repo-scope", "set", "checkout", "--agent", "--segment", tc.segment}, RunOptions{})
		require.NotZero(t, code, name)
		detail := agentError(t, stdout)
		require.Equal(t, "validation_error", detail.Code, name)
		require.True(t, strings.HasSuffix(detail.Message, tc.message), "%s: %s", name, detail.Message)
		require.Equal(t, before, readScopeFile(t, repo), name)
	}

	code, stdout, stderr := runRepoScoped([]string{"repo-scope", "describe", "checkout", "--plain", "--no-agent"}, RunOptions{})
	require.Zero(t, code, stderr)
	require.Regexp(t, `Segments:\s+Ab3dEf9hIjk\?environment=production,staging&k8s\.namespace\.name=payments, plain-1\n`, stdout)
	var table bytes.Buffer
	rows := repoScopeRows(loadScopeFile(t, repo).Entries(repoScopeTestEnvHost))
	require.NoError(t, output.NewPrinterWithOpts(output.PrinterOptions{Format: "table", Writer: &table}).PrintList(rows))
	require.Regexp(t, `NAME\s+PATH\s+WORKLOADS\s+SERVICE NAMES\s+PROCESS GROUPS\s+SERVICES\s+SEGMENTS\s*\n`, table.String())
	require.Regexp(t, `tools\s+tools\s+plain-1\s*\n`, table.String())
}

// A refused set must leave a committed file exactly as it was.
func TestRepoScopeSetValidatesBeforeWriting(t *testing.T) {
	localRepoScopeConfig(t)
	repo := chdirTestRepo(t, monorepoScope)
	before := readScopeFile(t, repo)

	for _, tc := range []struct {
		name   string
		args   []string
		source string
	}{
		{"service id", []string{"--service", "SERVICE-123"}, reposcope.FileName},
		{"process group id", []string{"--process-group", "PG-1"}, reposcope.FileName},
		{"service name", []string{"--service-name", `a"b`}, reposcope.FileName},
		{"namespace", []string{"--namespace", "Payments", "--workload", "checkout"}, reposcope.FileName},
		{"cluster name", []string{"--cluster-uid", "prod-eu", "--namespace", "payments", "--workload", "checkout"}, reposcope.FileName},
		{"path outside", []string{"--service-name", "checkout", "--path", "../elsewhere"}, reposcope.FileName},
		{"path taken", []string{"--service-name", "checkout", "--path", "services/ledger"}, reposcope.FileName},
		{"two paths", []string{"--service-name", "checkout", "--path", "services/checkout", "--path", "libs/cart"}, "repo-scope set"},
		{"entry name", nil, reposcope.FileName},
		{"no binding", nil, "repo-scope set"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			name := "checkout"
			if tc.name == "entry name" {
				name, tc.args = "Checkout", []string{"--service-name", "checkout"}
			}
			code, stdout, _ := runRepoScoped(append([]string{"repo-scope", "set", name, "--agent"}, tc.args...), RunOptions{})
			require.NotZero(t, code)
			detail := agentError(t, stdout)
			require.Equal(t, "validation_error", detail.Code)
			require.Contains(t, detail.Message, tc.source+": ", "the error names what was refused")
			require.Equal(t, before, readScopeFile(t, repo), "a refused set must not touch the file")
		})
	}
	entries, err := os.ReadDir(repo)
	require.NoError(t, err)
	for _, e := range entries {
		require.False(t, strings.HasSuffix(e.Name(), ".tmp"), "the atomic write left %s behind", e.Name())
	}
}

func TestRepoScopeSetFlagsRejectEmptyValues(t *testing.T) {
	localRepoScopeConfig(t)
	chdirTestRepo(t, "")
	for _, flag := range []string{"service", "process-group", "service-name", "cluster-uid", "namespace", "workload", "path"} {
		code, _, stderr := runRepoScoped([]string{"repo-scope", "set", "checkout", "--service-name", "checkout", "--" + flag, " "}, RunOptions{})
		require.Equal(t, client.ExitUsageError, code, "--%s: %s", flag, stderr)
	}
	// A workload is only unique within its namespace.
	code, _, _ := runRepoScoped([]string{"repo-scope", "set", "checkout", "--workload", "checkout"}, RunOptions{})
	require.NotZero(t, code)
	_, err := os.Stat(reposcope.FileName)
	require.ErrorIs(t, err, os.ErrNotExist)
}

// A document key this dtctl does not know is a typo or a newer dtctl's.
// Queries and current say so; set refuses, because saving would drop it.
func TestRepoScopeUnknownKeys(t *testing.T) {
	localRepoScopeConfig(t)
	withKey := "kind: RepoScope\n" + monorepoScope
	repo := chdirTestRepo(t, withKey)
	chdirSubdir(t, "services/checkout")

	code, stdout, stderr := runRepoScoped([]string{"repo-scope", "current"}, RunOptions{})
	require.Zero(t, code, stderr)
	require.Equal(t, "checkout  (services/checkout · abc12345.apps.dynatrace.com)\n", stdout)
	require.Contains(t, stderr, `unknown key "kind" on line 1`)

	code, stdout, _ = runRepoScoped([]string{"repo-scope", "current", "-o", "json"}, RunOptions{})
	require.Zero(t, code)
	var status reposcope.Status
	require.NoError(t, json.Unmarshal([]byte(stdout), &status))
	require.Contains(t, status.Warning, "kind")

	code, stdout, _ = runRepoScoped([]string{"repo-scope", "set", "ledger", "--service-name", "ledger", "--agent"}, RunOptions{})
	require.NotZero(t, code)
	require.Equal(t, "validation_error", agentError(t, stdout).Code)
	require.Equal(t, withKey, string(readScopeFile(t, repo)))
}

// A key inside an entry that this dtctl does not know could be a constraint,
// so the file is not applied without it: current says why no entry applies,
// and nothing rewrites the file.
func TestRepoScopeUnknownEntryKeys(t *testing.T) {
	localRepoScopeConfig(t)
	withKey := strings.Replace(monorepoScope, "      service-names: [checkout]\n", "      service-names: [checkout]\n      service_names: [checkout-v2]\n", 1)
	repo := chdirTestRepo(t, withKey)
	chdirSubdir(t, "services/checkout")

	code, stdout, stderr := runRepoScoped([]string{"repo-scope", "current"}, RunOptions{})
	require.Zero(t, code, stderr)
	require.Equal(t, `none  (.dtctl-repo-scope.yaml: unknown key "service_names" on line 6, in an entry (a typo, or written by a newer dtctl); `+
		"a key there can narrow the entry, so the file is not applied without it)\n", stdout)

	code, stdout, _ = runRepoScoped([]string{"repo-scope", "set", "ledger", "--service-name", "ledger", "--agent"}, RunOptions{})
	require.NotZero(t, code)
	require.Equal(t, "validation_error", agentError(t, stdout).Code)
	require.Equal(t, withKey, string(readScopeFile(t, repo)))
}

func TestRepoScopeDelete(t *testing.T) {
	localRepoScopeConfig(t)
	repo := chdirTestRepo(t, monorepoScope)

	code, stdout, _ := runRepoScoped([]string{"repo-scope", "delete", "chekout", "--agent"}, RunOptions{})
	require.NotZero(t, code)
	detail := agentError(t, stdout)
	require.Equal(t, "not_found", detail.Code)
	require.Equal(t, `did you mean "checkout"?`, detail.Suggestions[0])

	code, _, stderr := runRepoScoped([]string{"repo-scope", "rm", "checkout"}, RunOptions{})
	require.Zero(t, code, stderr)
	require.Equal(t, []string{"ledger"}, loadScopeFile(t, repo).Names(repoScopeTestEnvHost))

	// The last entry takes the file with it rather than leaving an empty
	// document to commit.
	code, stdout, _ = runRepoScoped([]string{"repo-scope", "delete", "ledger", "--agent"}, RunOptions{})
	require.Zero(t, code)
	var deleted repoScopeDeleted
	agentResult(t, stdout, &deleted)
	require.Equal(t, repoScopeDeleted{Deleted: true, Name: "ledger", File: reposcope.FileName, Environment: repoScopeTestEnvHost}, deleted)
	_, err := os.Stat(filepath.Join(repo, reposcope.FileName))
	require.ErrorIs(t, err, os.ErrNotExist)
}

// gradleMultiRepo is reposcope's gradle-multi fixture, an absolute path so it
// survives a test's chdir.
func gradleMultiRepo(t *testing.T) string {
	t.Helper()
	dir, err := filepath.Abs(filepath.Join("..", "pkg", "reposcope", "testdata", "repos", "gradle-multi"))
	require.NoError(t, err)
	return dir
}

// The terminal output of current, describe and list, one golden per command:
// current answers wherever it is run and exits 0 whenever it can answer,
// because it is how a user finds out why a query was or was not scoped.
func TestRepoScopeHumanOutput(t *testing.T) {
	invalid := strings.Replace(monorepoScope, "name: ledger", "name: Ledger", 1)
	billing := monorepoScope + `    - name: billing
      path: services/billing
      services: [SERVICE-0123456789ABCDEF]
`
	gradle := gradleMultiRepo(t)
	webproxy := `environments:
  abc12345.apps.dynatrace.com:
    - name: webproxy
      path: webproxy
      service-names: [webproxy]
`
	// A cluster narrows the workload it is written on and nothing else: the
	// service name beside it is an alternative, in every cluster.
	oneCluster := `environments:
  abc12345.apps.dynatrace.com:
    - name: checkout
      workloads:
        - cluster-uid: 11111111-2222-3333-4444-555555555555
          namespace: payments
          name: checkout
      service-names: [checkout]
      process-groups: [PROCESS_GROUP-FEDCBA9876543210]
`
	// An entry that references only segments still narrows every query, and
	// one with both sends its segments where its bindings filter nothing.
	segments := `environments:
  abc12345.apps.dynatrace.com:
    - name: tools
      segments:
        - id: plain-1
    - name: billing
      path: services/billing
      services: [SERVICE-0123456789ABCDEF]
      segments:
        - id: Ab3dEf9hIjk
          variables:
            environment: [production]
`
	for _, tc := range []struct {
		golden    string
		repo      bool
		fixture   string
		scopeFile string
		dir       string
		args      []string
	}{
		{golden: "current-outside-a-repository", args: []string{"current"}},
		{golden: "current-no-file", repo: true, args: []string{"current"}},
		{golden: "current-covered", repo: true, scopeFile: monorepoScope, dir: "services/ledger/internal", args: []string{"current"}},
		{golden: "current-uncovered", repo: true, scopeFile: monorepoScope, dir: "docs", args: []string{"current"}},
		{golden: "current-other-environment", repo: true, args: []string{"current"},
			scopeFile: strings.Replace(monorepoScope, repoScopeTestEnvHost, "stg98765.apps.dynatrace.com", 1)},
		{golden: "current-invalid-file", repo: true, scopeFile: invalid, args: []string{"current"}},
		{golden: "describe-by-name", repo: true, scopeFile: monorepoScope, args: []string{"describe", "ledger"}},
		{golden: "describe-one-object", repo: true, scopeFile: billing, dir: "services/billing", args: []string{"describe"}},
		{golden: "describe-uncovered", repo: true, scopeFile: monorepoScope, dir: "docs", args: []string{"describe"}},
		{golden: "describe-gradle-library", repo: true, fixture: gradle, scopeFile: webproxy, dir: "webproxy-api", args: []string{"describe"}},
		{golden: "current-cluster", repo: true, scopeFile: oneCluster, args: []string{"current"}},
		{golden: "describe-cluster", repo: true, scopeFile: oneCluster, args: []string{"describe"}},
		{golden: "describe-segments-only", repo: true, scopeFile: segments, args: []string{"describe", "tools"}},
		{golden: "describe-segments-and-bindings", repo: true, scopeFile: segments, args: []string{"describe", "billing"}},
		{golden: "list", repo: true, scopeFile: billing, args: []string{"list"}},
	} {
		t.Run(tc.golden, func(t *testing.T) {
			localRepoScopeConfig(t)
			if tc.repo {
				repo := chdirTestRepo(t, tc.scopeFile)
				if tc.fixture != "" {
					require.NoError(t, os.CopyFS(repo, os.DirFS(tc.fixture)))
				}
			} else {
				t.Chdir(t.TempDir())
			}
			if tc.dir != "" {
				chdirSubdir(t, tc.dir)
			}
			code, stdout, stderr := runRepoScoped(append([]string{"repo-scope"}, tc.args...), RunOptions{})
			require.Zero(t, code, stderr)
			testutil.AssertGolden(t, "repo-scope/"+tc.golden, stdout)
		})
	}
}

// The structured views carry the same Status in every format the printers
// offer, and list keeps the file's own spelling in YAML.
func TestRepoScopeStructuredOutput(t *testing.T) {
	localRepoScopeConfig(t)
	chdirTestRepo(t, monorepoScope)
	chdirSubdir(t, "services/checkout")

	decoders := map[string]func(t *testing.T, stdout string) reposcope.Status{
		"json": func(t *testing.T, stdout string) (s reposcope.Status) {
			require.NoError(t, json.Unmarshal([]byte(stdout), &s))
			return s
		},
		"yaml": func(t *testing.T, stdout string) (s reposcope.Status) {
			require.NoError(t, yaml.Unmarshal([]byte(stdout), &s))
			return s
		},
		"agent": func(t *testing.T, stdout string) (s reposcope.Status) {
			resp := agentResult(t, stdout, &s)
			require.Equal(t, "repo-scope", resp.Context.Resource)
			return s
		},
	}
	for _, verb := range []string{"current", "describe"} {
		for format, decode := range decoders {
			t.Run(verb+" "+format, func(t *testing.T) {
				args := []string{"repo-scope", verb, "-o", format}
				if format == "agent" {
					args = []string{"repo-scope", verb, "--agent"}
				}
				code, stdout, stderr := runRepoScoped(args, RunOptions{})
				require.Zero(t, code, stderr)
				s := decode(t, stdout)
				require.True(t, s.Linked)
				require.Equal(t, "checkout", s.Entry.Name)
				require.Equal(t, "services/checkout", s.Dir)
				require.Equal(t, `service.name == "checkout"`, s.Filters["spans"])
				require.Equal(t, []string{"ledger"}, s.Others)
			})
		}
	}

	code, stdout, stderr := runRepoScoped([]string{"repo-scope", "-o", "yaml"}, RunOptions{})
	require.Zero(t, code, stderr)
	require.Contains(t, stdout, "service-names:")
	code, stdout, _ = runRepoScoped([]string{"repo-scope", "ls", "--agent"}, RunOptions{})
	require.Zero(t, code)
	require.Equal(t, 2, *agentEnvelope(t, stdout).Context.Total)
}

func TestRepoScopeListWithoutAFile(t *testing.T) {
	localRepoScopeConfig(t)
	chdirTestRepo(t, "")

	code, stdout, stderr := runRepoScoped([]string{"repo-scope"}, RunOptions{})
	require.Zero(t, code, stderr)
	require.Contains(t, stdout, "No resources found.")
	require.Contains(t, stderr, repoScopeNoFileReason)

	code, stdout, stderr = runRepoScoped([]string{"repo-scope", "list", "--agent"}, RunOptions{})
	require.Zero(t, code, stderr)
	require.Empty(t, stderr, "the agent envelope carries the reason")
	resp := agentEnvelope(t, stdout)
	require.Equal(t, []interface{}{}, resp.Result)
	require.Equal(t, []string{repoScopeNoFileReason, repoScopeDiscoverHint}, resp.Context.Suggestions)
}

// Every command but current needs a repository to work on, and says so in
// terms of what to do rather than failing on a missing file.
func TestRepoScopeOutsideARepository(t *testing.T) {
	localRepoScopeConfig(t)
	dir := t.TempDir()
	t.Chdir(dir)

	for _, args := range [][]string{
		{"repo-scope", "list"},
		{"repo-scope", "set", "checkout", "--service-name", "checkout"},
		{"repo-scope", "delete", "checkout"},
		{"repo-scope", "describe", "checkout"},
		{"repo-scope", "discover", "--dry-run"},
	} {
		t.Run(strings.Join(args[1:], " "), func(t *testing.T) {
			code, stdout, _ := runRepoScoped(append(args, "--agent"), RunOptions{})
			require.NotZero(t, code)
			detail := agentError(t, stdout)
			require.Equal(t, "validation_error", detail.Code)
			require.Equal(t, []string{repoScopeCheckoutHint}, detail.Suggestions)
		})
	}
	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	require.Empty(t, entries)
}

// An invocation without the HostWorkingDirectory capability has no working
// directory to find a repository from, whatever the process's own is.
func TestRepoScopeNeedsTheWorkingDirectory(t *testing.T) {
	localRepoScopeConfig(t)
	chdirTestRepo(t, monorepoScope)

	code, stdout, _ := runRepoScoped([]string{"repo-scope", "list", "--agent"}, RunOptions{Capabilities: &Capabilities{}})
	require.NotZero(t, code)
	require.Equal(t, "capability_disabled", agentError(t, stdout).Code)

	// Positive control: the same run with the CLI's capabilities lists both.
	code, stdout, stderr := runRepoScoped([]string{"repo-scope", "list", "--agent"}, RunOptions{})
	require.Zero(t, code, stderr)
	require.Equal(t, 2, *agentEnvelope(t, stdout).Context.Total)
}

func TestRepoScopeErrorDetails(t *testing.T) {
	for _, tc := range []struct {
		name        string
		err         error
		code        string
		suggestions []string
	}{
		{"unknown name", &reposcope.NotFoundError{Name: "ledgr", Host: repoScopeTestEnvHost, Known: []string{"checkout", "ledger"}},
			"not_found", []string{`did you mean "ledger"?`, "entries for abc12345.apps.dynatrace.com: checkout, ledger"}},
		{"no entries", &reposcope.NotFoundError{Name: "ledger", Host: repoScopeTestEnvHost},
			"not_found", []string{repoScopeDiscoverHint}},
		{"invalid value", &reposcope.InvalidError{Source: "--term", Msg: "bad", Suggestions: []string{"fix it"}},
			"validation_error", []string{"fix it"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			detail := errorToDetail(t.Context(), tc.err)
			require.Equal(t, tc.code, detail.Code)
			require.Equal(t, tc.err.Error(), detail.Message)
			require.Equal(t, tc.suggestions, detail.Suggestions)
		})
	}
}
