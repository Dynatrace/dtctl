package cmd

import (
	"bytes"
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
	"gopkg.in/yaml.v3"

	"github.com/dynatrace-oss/dtctl/cmd/testutil"
	"github.com/dynatrace-oss/dtctl/pkg/client"
	"github.com/dynatrace-oss/dtctl/pkg/output"
	"github.com/dynatrace-oss/dtctl/pkg/reposcope"
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
		if !strings.HasSuffix(r.URL.Path, "/query:execute") && !strings.HasSuffix(r.URL.Path, "/query:verify") {
			http.NotFound(w, r)
			return
		}
		var req struct {
			Query string `json:"query"`
		}
		raw, _ := io.ReadAll(r.Body)
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
		{"path outside", []string{"--service-name", "checkout", "--path", "../elsewhere"}, reposcope.FileName},
		{"path taken", []string{"--service-name", "checkout", "--path", "services/ledger"}, reposcope.FileName},
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
	for _, flag := range []string{"service", "process-group", "service-name", "namespace", "workload", "path"} {
		code, _, stderr := runRepoScoped([]string{"repo-scope", "set", "checkout", "--service-name", "checkout", "--" + flag, " "}, RunOptions{})
		require.Equal(t, client.ExitUsageError, code, "--%s: %s", flag, stderr)
	}
	// A workload is only unique within its namespace.
	code, _, _ := runRepoScoped([]string{"repo-scope", "set", "checkout", "--workload", "checkout"}, RunOptions{})
	require.NotZero(t, code)
	_, err := os.Stat(reposcope.FileName)
	require.ErrorIs(t, err, os.ErrNotExist)
}

// Keys this dtctl does not know are a typo or a newer dtctl's. Queries and
// current say so; set refuses, because saving would drop them.
func TestRepoScopeUnknownKeys(t *testing.T) {
	localRepoScopeConfig(t)
	withKey := strings.Replace(monorepoScope, "      service-names: [checkout]\n", "      service-names: [checkout]\n      service_names: [checkout-v2]\n", 1)
	repo := chdirTestRepo(t, withKey)
	chdirSubdir(t, "services/checkout")

	code, stdout, stderr := runRepoScoped([]string{"repo-scope", "current"}, RunOptions{})
	require.Zero(t, code, stderr)
	require.Equal(t, "checkout  (services/checkout · abc12345.apps.dynatrace.com)\n", stdout)
	require.Contains(t, stderr, `unknown key "service_names" on line 6`)

	code, stdout, _ = runRepoScoped([]string{"repo-scope", "current", "-o", "json"}, RunOptions{})
	require.Zero(t, code)
	var status reposcope.Status
	require.NoError(t, json.Unmarshal([]byte(stdout), &status))
	require.Contains(t, status.Warning, "service_names")

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

// The terminal output of current, describe and list, one golden per command:
// current answers wherever it is run and exits 0 whenever it can answer,
// because it is how a user finds out why a query was or was not scoped.
func TestRepoScopeHumanOutput(t *testing.T) {
	invalid := strings.Replace(monorepoScope, "name: ledger", "name: Ledger", 1)
	billing := monorepoScope + `    - name: billing
      path: services/billing
      services: [SERVICE-0123456789ABCDEF]
`
	for _, tc := range []struct {
		golden    string
		repo      bool
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
		{golden: "list", repo: true, scopeFile: billing, args: []string{"list"}},
	} {
		t.Run(tc.golden, func(t *testing.T) {
			localRepoScopeConfig(t)
			if tc.repo {
				chdirTestRepo(t, tc.scopeFile)
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
