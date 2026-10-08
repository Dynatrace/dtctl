package cmd

import (
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/dynatrace-oss/dtctl/pkg/output"
	"github.com/dynatrace-oss/dtctl/pkg/reposcope"
)

// The environment the mock server stands for: urls.Host drops the port, so a
// scope file entry for the test server is keyed by its bare address.
const repoScopeTestHost = "127.0.0.1"

// checkoutScope links the test repository to one workload and one service
// name on the test server.
const checkoutScope = `environments:
  127.0.0.1:
    - name: checkout
      service-names: [checkout]
      workloads:
        - namespace: payments
          name: checkout
`

// checkoutLogsFilter is what checkoutScope renders for fetch logs.
const checkoutLogsFilter = `service.name == "checkout" or (k8s.namespace.name == "payments" and k8s.workload.name == "checkout")`

const (
	repoScopeTypedQuery  = "fetch logs | limit 5"
	repoScopeScopedQuery = "fetch logs | filter (" + checkoutLogsFilter + ")\n| limit 5"
	repoScopeNotice      = "applying repo scope checkout to fetch logs: " + checkoutLogsFilter + " (use --no-repo-scope to ignore it)\n"
	oneRecord            = `{"state":"SUCCEEDED","result":{"records":[{"content":"hello"}]}}`
)

// repoScopeContext decodes context.repo_scope from an agent envelope, or nil.
func repoScopeContext(t *testing.T, stdout string) *output.RepoScope {
	t.Helper()
	return agentEnvelope(t, stdout).Context.RepoScope
}

// repoScopeRun is one invocation of a TestQueryRepoScope_UnlinkedIsByteIdentical
// row.
type repoScopeRun struct {
	scopeFile string
	args      []string
	opts      func(serverURL string) RunOptions
	// files are written into the repository before the run.
	files map[string]string
	stdin string
}

// Whenever a repo scope does not apply, `query` sends the text as typed,
// prints nothing about it, and its envelope has no repo_scope key. Each row
// pairs that run with a positive control, the same fixture with the one
// disabling factor removed, which must scope the query, so a row cannot pass
// merely because scoping never happens in the test setup.
func TestQueryRepoScope_UnlinkedIsByteIdentical(t *testing.T) {
	optInRepoScope(t)
	plain := func(string) RunOptions { return RunOptions{} }
	query := []string{"query", repoScopeTypedQuery}
	withEnv := func(env map[string]string) func(string) RunOptions {
		return func(string) RunOptions { return RunOptions{Env: env} }
	}
	scoped := repoScopeRun{scopeFile: checkoutScope, args: query, opts: plain}

	rows := []struct {
		name    string
		off, on repoScopeRun
	}{
		{"unlinked repository", repoScopeRun{args: query, opts: plain}, scoped},
		{"entry for another environment only", repoScopeRun{
			scopeFile: strings.Replace(checkoutScope, repoScopeTestHost, "abc12345.apps.dynatrace.example.invalid", 1),
			args:      query, opts: plain,
		}, scoped},
		{"--no-repo-scope", repoScopeRun{scopeFile: checkoutScope, args: append(query, "--no-repo-scope"), opts: plain}, scoped},
		{"DTCTL_NO_REPO_SCOPE=1", repoScopeRun{scopeFile: checkoutScope, args: query, opts: withEnv(map[string]string{noRepoScopeEnv: "1"})},
			repoScopeRun{scopeFile: checkoutScope, args: query, opts: withEnv(map[string]string{noRepoScopeEnv: "0"})}},
		{
			// The entry is keyed to the session's own host, so only the
			// session check keeps this run unscoped.
			"session-backed invocation", repoScopeRun{scopeFile: checkoutScope, args: query, opts: func(url string) RunOptions {
				return RunOptions{Session: &Session{EnvironmentURL: url, Token: "session-token"}}
			}}, scoped,
		},
		{"HostWorkingDirectory not granted", repoScopeRun{scopeFile: checkoutScope, args: query, opts: func(string) RunOptions {
			return RunOptions{Capabilities: &Capabilities{}}
		}}, scoped},
		{"-f query.dql, unlinked",
			repoScopeRun{args: []string{"query", "-f", "query.dql"}, opts: plain, files: map[string]string{"query.dql": repoScopeTypedQuery}},
			repoScopeRun{scopeFile: checkoutScope, args: []string{"query", "-f", "query.dql"}, opts: plain, files: map[string]string{"query.dql": repoScopeTypedQuery}}},
		{"-f - (stdin), unlinked",
			repoScopeRun{args: []string{"query", "-f", "-"}, opts: plain, stdin: repoScopeTypedQuery},
			repoScopeRun{scopeFile: checkoutScope, args: []string{"query", "-f", "-"}, opts: plain, stdin: repoScopeTypedQuery}},
	}
	modes := []struct {
		name string
		args []string
	}{
		{"table", nil},
		{"agent", []string{"--agent"}},
		{"plain json", []string{"--plain", "-o", "json"}},
	}

	for _, row := range rows {
		for _, mode := range modes {
			t.Run(row.name+"/"+mode.name, func(t *testing.T) {
				srv := newDQLRecorder(t, "readonly", answer(http.StatusOK, oneRecord))
				run := func(r repoScopeRun) (string, string) {
					t.Helper()
					dir := chdirTestRepo(t, r.scopeFile)
					for name, content := range r.files {
						require.NoError(t, os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644))
					}
					opts := r.opts(srv.URL)
					if r.stdin != "" {
						opts.Stdin = strings.NewReader(r.stdin)
					}
					srv.reset()
					code, stdout, stderr := runRepoScoped(append(append([]string{}, r.args...), mode.args...), opts)
					require.Zero(t, code, "stdout: %s\nstderr: %s", stdout, stderr)
					return stdout, stderr
				}

				stdout, stderr := run(row.off)
				require.Equal(t, repoScopeTypedQuery, srv.first(t), "an unscoped run sends the query byte for byte")
				require.Empty(t, stderr, "an unscoped run prints nothing about repo scopes")
				require.NotContains(t, stdout, "repo_scope")

				stdout, stderr = run(row.on)
				require.Equal(t, repoScopeScopedQuery, srv.first(t), "positive control: the same fixture without %s must scope", row.name)
				require.Equal(t, repoScopeNotice, stderr)
				if mode.name == "agent" {
					s := repoScopeContext(t, stdout)
					require.Equal(t, output.RepoScope{Name: "checkout", File: reposcope.FileName, Environment: repoScopeTestHost,
						Applied: true, DataObject: "logs", Filter: checkoutLogsFilter, Code: "applied"}, *s)
				} else {
					require.NotContains(t, stdout, "repo_scope", "only the agent envelope has a context to carry it")
				}
			})
		}
	}
}

// Scoping is on in every mode: a non-interactive CI run is scoped like a
// terminal one. DTCTL_NO_REPO_SCOPE turns it off only for a truthy value, and
// never overrides an entry named on the command line.
func TestQueryRepoScope_Environment(t *testing.T) {
	optInRepoScope(t)
	for _, tc := range []struct {
		name   string
		env    map[string]string
		args   []string
		scoped bool
	}{
		{"CI", map[string]string{"CI": "true", "GITHUB_ACTIONS": "true"}, nil, true},
		{"opt-out", map[string]string{noRepoScopeEnv: "1"}, nil, false},
		{"opt-out set to 0", map[string]string{noRepoScopeEnv: "0"}, nil, true},
		{"opt-out set to false", map[string]string{noRepoScopeEnv: "false"}, nil, true},
		{"opt-out and an explicit name", map[string]string{noRepoScopeEnv: "1"}, []string{"--repo-scope", "checkout"}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := newDQLRecorder(t, "readonly", answer(http.StatusOK, oneRecord))
			chdirTestRepo(t, checkoutScope)
			for k, v := range tc.env {
				t.Setenv(k, v)
			}
			code, _, stderr := runRepoScoped(append([]string{"query", repoScopeTypedQuery}, tc.args...), RunOptions{})
			require.Zero(t, code, stderr)
			want := repoScopeTypedQuery
			if tc.scoped {
				want = repoScopeScopedQuery
			}
			require.Equal(t, want, srv.first(t))
		})
	}
}

// A scope file that fails validation must not break every query run in the
// repository: under the default selector it warns and the query runs as typed.
// Naming an entry asks for the file, so there it is an error.
func TestQueryRepoScope_InvalidFile(t *testing.T) {
	optInRepoScope(t)
	srv := newDQLRecorder(t, "readonly", answer(http.StatusOK, oneRecord))
	chdirTestRepo(t, strings.Replace(checkoutScope, "[checkout]", `["check\"out"]`, 1))

	code, stdout, stderr := runRepoScoped([]string{"query", repoScopeTypedQuery, "--agent"}, RunOptions{})
	require.Zero(t, code, stderr)
	require.Equal(t, repoScopeTypedQuery, srv.first(t))
	require.Equal(t, 1, strings.Count(stderr, "\n"), stderr)
	require.Equal(t, "invalid_file", repoScopeContext(t, stdout).Code)

	srv.reset()
	code, stdout, _ = runRepoScoped([]string{"query", repoScopeTypedQuery, "--repo-scope", "checkout", "--agent"}, RunOptions{})
	require.NotZero(t, code)
	require.Equal(t, "validation_error", agentError(t, stdout).Code)
	require.Empty(t, srv.queries(), "a query naming an unusable scope is not sent")
}

// Keys this dtctl does not know leave the entry in force, and the run says so
// on stderr and in context.repo_scope.reason.
func TestQueryRepoScope_UnknownKeys(t *testing.T) {
	optInRepoScope(t)
	srv := newDQLRecorder(t, "readonly", answer(http.StatusOK, oneRecord))
	chdirTestRepo(t, checkoutScope+"      metrics: [dt.service.request.count]\n")

	code, stdout, stderr := runRepoScoped([]string{"query", repoScopeTypedQuery, "--agent"}, RunOptions{})
	require.Zero(t, code, stderr)
	require.Equal(t, repoScopeScopedQuery, srv.first(t))
	require.Contains(t, stderr, `unknown key "metrics"`)
	s := repoScopeContext(t, stdout)
	require.True(t, s.Applied)
	require.Contains(t, s.Reason, `unknown key "metrics"`)
}

// An explicit --repo-scope that does not resolve is an error with the reason,
// whichever step it fails at; it never silently runs unscoped.
func TestQueryRepoScope_ExplicitNameErrors(t *testing.T) {
	optInRepoScope(t)
	for _, tc := range []struct {
		name      string
		repo      bool
		scopeFile string
		entry     string
		opts      RunOptions
		code      string
	}{
		{name: "not in a repository", entry: "checkout", code: "validation_error"},
		{name: "no scope file", repo: true, entry: "checkout", code: "not_found"},
		{name: "unknown name", repo: true, scopeFile: checkoutScope, entry: "chekout", code: "not_found"},
		{name: "no entry for this environment", repo: true, entry: "checkout", code: "not_found",
			scopeFile: strings.Replace(checkoutScope, repoScopeTestHost, "abc12345.apps.dynatrace.example.invalid", 1)},
		{name: "capability not granted", repo: true, scopeFile: checkoutScope, entry: "checkout",
			opts: RunOptions{Capabilities: &Capabilities{}}, code: "capability_disabled"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := newDQLRecorder(t, "readonly", answer(http.StatusOK, oneRecord))
			if tc.repo {
				chdirTestRepo(t, tc.scopeFile)
			} else {
				t.Chdir(t.TempDir())
			}
			code, stdout, _ := runRepoScoped([]string{"query", repoScopeTypedQuery, "--repo-scope", tc.entry, "--agent"}, tc.opts)
			require.NotZero(t, code)
			require.Equal(t, tc.code, agentError(t, stdout).Code)
			require.Empty(t, srv.queries())
		})
	}

	srv := newDQLRecorder(t, "readonly", answer(http.StatusOK, oneRecord))
	chdirTestRepo(t, checkoutScope)
	code, _, _ := runRepoScoped([]string{"query", repoScopeTypedQuery, "--repo-scope", "checkout", "--no-repo-scope"}, RunOptions{})
	require.NotZero(t, code, "the selector and the opt-out are mutually exclusive")
	require.Empty(t, srv.queries())

	// Positive control: the right name scopes the query from any directory.
	chdirSubdir(t, "docs")
	code, _, stderr := runRepoScoped([]string{"query", repoScopeTypedQuery, "--repo-scope", "checkout"}, RunOptions{})
	require.Zero(t, code, stderr)
	require.Equal(t, repoScopeScopedQuery, srv.first(t))
}

// Every way a resolved scope can decline to apply leaves the query unchanged,
// says why in one warning line, and reports its code in the envelope.
func TestQueryRepoScope_RefusalShapes(t *testing.T) {
	optInRepoScope(t)
	const pathBound = `environments:
  127.0.0.1:
    - name: checkout
      path: services/checkout
      service-names: [checkout]
`
	const servicesOnly = `environments:
  127.0.0.1:
    - name: checkout
      services: [SERVICE-0123456789ABCDEF]
`
	for _, tc := range []struct {
		code      string
		scopeFile string
		query     string
	}{
		{"not_fetch", checkoutScope, "timeseries avg(dt.host.cpu.usage)"},
		{"unsupported_object", checkoutScope, "fetch events"},
		{"no_binding", servicesOnly, "fetch logs"},
		{"user_filter", checkoutScope, `fetch logs | filter k8s.namespace.name == "payments"`},
		{"subquery", checkoutScope, "fetch logs | append [fetch logs | limit 1]"},
		{"unscannable", checkoutScope, `fetch logs | filter content == "unterminated`},
		{"no_entry", pathBound, "fetch logs"},
	} {
		t.Run(tc.code, func(t *testing.T) {
			srv := newDQLRecorder(t, "readonly", answer(http.StatusOK, oneRecord))
			chdirTestRepo(t, tc.scopeFile)

			_, stdout, stderr := runRepoScoped([]string{"query", tc.query, "--agent"}, RunOptions{})
			require.Equal(t, tc.query, srv.first(t), "a scope that does not apply leaves the query as typed")
			require.Equal(t, 1, strings.Count(stderr, "\n"), "one line: %s", stderr)
			s := repoScopeContext(t, stdout)
			require.False(t, s.Applied)
			require.Equal(t, tc.code, s.Code)
			require.NotEmpty(t, s.Reason)
			require.NotContains(t, agentEnvelope(t, stdout).Context.Suggestions, "run unscoped: add --no-repo-scope")
		})
	}
}

// Naming an entry asks for scoped rows, so an entry that resolves but cannot
// narrow the query is an error and nothing is sent. The user's own filter on
// a bound field is the exception: it wins over the scope, and the query runs
// as typed with the warning.
func TestQueryRepoScope_ExplicitNameRefusals(t *testing.T) {
	optInRepoScope(t)
	const servicesOnly = `environments:
  127.0.0.1:
    - name: checkout
      services: [SERVICE-0123456789ABCDEF]
`
	for _, tc := range []struct {
		code      string
		scopeFile string
		query     string
	}{
		{"not_fetch", checkoutScope, "timeseries avg(dt.host.cpu.usage)"},
		{"unsupported_object", checkoutScope, "fetch events"},
		{"no_binding", servicesOnly, "fetch logs"},
		{"subquery", checkoutScope, "fetch logs | append [fetch logs | limit 1]"},
		{"unscannable", checkoutScope, `fetch logs | filter content == "unterminated`},
	} {
		t.Run(tc.code, func(t *testing.T) {
			srv := newDQLRecorder(t, "readonly", answer(http.StatusOK, oneRecord))
			chdirTestRepo(t, tc.scopeFile)

			code, stdout, stderr := runRepoScoped([]string{"query", tc.query, "--repo-scope", "checkout", "--agent"}, RunOptions{})
			require.NotZero(t, code)
			detail := agentError(t, stdout)
			require.Equal(t, "validation_error", detail.Code)
			require.Contains(t, detail.Message, `--repo-scope: entry "checkout" cannot narrow this query (`+tc.code+"): ")
			require.Empty(t, srv.queries(), "a named scope that cannot apply sends nothing")
			require.NotContains(t, stderr, "runs unscoped")

			// Positive control: without the name the same query runs as typed.
			code, _, stderr = runRepoScoped([]string{"query", tc.query, "--agent"}, RunOptions{})
			require.Zero(t, code, stderr)
			require.Equal(t, tc.query, srv.first(t))
		})
	}

	t.Run("user_filter runs as typed", func(t *testing.T) {
		srv := newDQLRecorder(t, "readonly", answer(http.StatusOK, oneRecord))
		chdirTestRepo(t, checkoutScope)
		const ownFilter = `fetch logs | filter k8s.namespace.name == "payments"`
		code, stdout, stderr := runRepoScoped([]string{"query", ownFilter, "--repo-scope", "checkout", "--agent"}, RunOptions{})
		require.Zero(t, code, stderr)
		require.Equal(t, ownFilter, srv.first(t))
		require.Equal(t, "user_filter", repoScopeContext(t, stdout).Code)
		require.Contains(t, stderr, "the query runs unscoped")
	})
}

// docs/AGENT_MODE.md lists exactly the codes context.repo_scope can carry.
func TestRepoScopeCodesAreDocumented(t *testing.T) {
	doc, err := os.ReadFile(filepath.Join("..", "docs", "AGENT_MODE.md"))
	require.NoError(t, err)
	_, section, ok := strings.Cut(string(doc), "### Repo scope: `context.repo_scope`")
	require.True(t, ok)
	section, _, _ = strings.Cut(section, "\n### ")
	var documented []string
	for _, m := range regexp.MustCompile("(?m)^\\| `([a-z_]+)` \\|").FindAllStringSubmatch(section, -1) {
		documented = append(documented, m[1])
	}
	require.Equal(t, repoScopeCodes[1:], documented)
}

// Zero rows under a scope look exactly like a quiet service, so the run says
// how to tell the two apart, on stderr and first in the envelope.
func TestQueryRepoScope_ZeroRowsHint(t *testing.T) {
	optInRepoScope(t)
	newDQLRecorder(t, "readonly", answer(http.StatusOK, `{"state":"SUCCEEDED","result":{"records":[]}}`))
	chdirTestRepo(t, checkoutScope)

	code, stdout, stderr := runRepoScoped([]string{"query", repoScopeTypedQuery, "--agent"}, RunOptions{})
	require.Zero(t, code, stderr)
	require.Equal(t, repoScopeNotice+"0 records under repo scope checkout; compare with --no-repo-scope\n", stderr)
	require.Contains(t, agentEnvelope(t, stdout).Context.Suggestions[0], "--no-repo-scope")
}

// A scoped query that fails keeps the query's own error code and adds what was
// sent and how to rule the scope out: a position points into the sent text.
func TestQueryRepoScope_ErrorWrapsScope(t *testing.T) {
	optInRepoScope(t)
	newDQLRecorder(t, "readonly", answer(http.StatusBadRequest,
		`{"error":{"code":400,"message":"The parameter 'x' is not defined.","details":{"errorType":"PARSE_ERROR"}}}`))
	chdirTestRepo(t, checkoutScope)

	code, stdout, stderr := runRepoScoped([]string{"query", repoScopeTypedQuery, "--agent"}, RunOptions{})
	require.NotZero(t, code)
	require.Equal(t, repoScopeNotice, stderr)
	detail := agentError(t, stdout)
	require.Equal(t, "parse_error", detail.Code, "the query's own code survives the wrapping")
	suggestions := strings.Join(detail.Suggestions, "\n")
	require.Contains(t, suggestions, repoScopeScopedQuery)
	require.Contains(t, suggestions, "--no-repo-scope")

	// The same failure without a scope carries neither suggestion.
	code, stdout, _ = runRepoScoped([]string{"query", repoScopeTypedQuery, "--agent", "--no-repo-scope"}, RunOptions{})
	require.NotZero(t, code)
	require.NotContains(t, strings.Join(agentError(t, stdout).Suggestions, "\n"), "repo scope")
}

// exec dql is the raw passthrough: what it is given is what it sends. Pinned
// as intended, so a later change that routes it through query's input path has
// to decide this again rather than inherit scoping by accident.
func TestExecDQLIsNeverRepoScoped(t *testing.T) {
	optInRepoScope(t)
	srv := newDQLRecorder(t, "readonly", answer(http.StatusOK, oneRecord))
	chdirTestRepo(t, checkoutScope)

	code, _, stderr := runRepoScoped([]string{"exec", "dql", repoScopeTypedQuery, "--plain"}, RunOptions{})
	require.Zero(t, code, stderr)
	require.Equal(t, repoScopeTypedQuery, srv.first(t))
	require.NotContains(t, stderr, "repo scope")

	// Positive control: query, in the same repository, scopes it.
	srv.reset()
	code, _, stderr = runRepoScoped([]string{"query", repoScopeTypedQuery, "--plain"}, RunOptions{})
	require.Zero(t, code, stderr)
	require.Equal(t, repoScopeScopedQuery, srv.first(t))
}

// wait query and verify query take the same input path as query, so they
// send (and verify checks) the same scoped text, and honour the opt-out.
// Neither has an agent envelope, so in agent mode too the stderr line is all
// that reports the scope.
func TestWaitQueryAndVerifyQueryApplyRepoScope(t *testing.T) {
	optInRepoScope(t)
	for _, tc := range []struct {
		name string
		argv []string
		body string
	}{
		{"wait query", []string{"wait", "query", repoScopeTypedQuery, "--for=any"}, oneRecord},
		{"verify query", []string{"verify", "query", repoScopeTypedQuery}, `{"valid":true}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := newDQLRecorder(t, "readonly", answer(http.StatusOK, tc.body))
			chdirTestRepo(t, checkoutScope)

			code, _, stderr := runRepoScoped(tc.argv, RunOptions{})
			require.Zero(t, code, stderr)
			require.Equal(t, repoScopeScopedQuery, srv.first(t))
			require.True(t, strings.HasPrefix(stderr, repoScopeNotice), stderr)

			for _, mode := range [][]string{{"--agent"}, {"--agent", "-o", "json"}} {
				srv.reset()
				code, stdout, stderr := runRepoScoped(append(append([]string{}, tc.argv...), mode...), RunOptions{})
				require.Zero(t, code, stderr)
				require.Equal(t, repoScopeScopedQuery, srv.first(t))
				require.True(t, strings.HasPrefix(stderr, repoScopeNotice), "%v: %s", mode, stderr)
				require.NotContains(t, stdout, "repo_scope", "%v: only query has an envelope to carry it", mode)
			}

			srv.reset()
			code, _, stderr = runRepoScoped(append(tc.argv, "--no-repo-scope"), RunOptions{})
			require.Zero(t, code, stderr)
			require.Equal(t, repoScopeTypedQuery, srv.first(t))
			require.NotContains(t, stderr, "repo scope")
		})
	}
}

// stableFloorConfig is a readonly context at env with a stability floor of
// stable and the given exceptions.
func stableFloorConfig(env string, exceptions ...string) string {
	list := ""
	if len(exceptions) > 0 {
		list = "      stability-exceptions:\n"
		for _, e := range exceptions {
			list += fmt.Sprintf("        - '%s'\n", e)
		}
	}
	return fmt.Sprintf(`current-context: c
contexts:
  - name: c
    context:
      environment: %s
      token-ref: t
      safety-level: readonly
      min-stability: stable
%stokens:
  - name: t
    token: dt0c01.EXAMPLE
`, env, list)
}

// Repo scoping is experimental, and only its flags carry the mark, so a
// stable floor that hides the flags turns the default scoping off: the stable
// commands return what they always did, with nothing on stderr. Exceptions for
// both flags admit it again, and an exception for --repo-scope alone admits
// naming an entry without turning the default on.
func TestQueryRepoScope_StableFloor(t *testing.T) {
	optInRepoScope(t)
	commands := []struct {
		path string
		argv []string
	}{
		{"query", []string{"query", repoScopeTypedQuery, "--agent"}},
		{"wait query", []string{"wait", "query", repoScopeTypedQuery, "--for=any", "--agent"}},
		{"verify query", []string{"verify", "query", repoScopeTypedQuery, "--agent"}},
	}
	both := func(path string) []string { return []string{path + " --repo-scope", path + " --no-repo-scope"} }
	for _, c := range commands {
		for _, tc := range []struct {
			name       string
			exceptions []string
			env        map[string]string
			args       []string
			scoped     bool
		}{
			{name: "floor in the context"},
			{name: "floor in the environment", env: map[string]string{"DTCTL_MIN_STABILITY": "stable"}},
			{name: "an exception for the command does not reach its flags", exceptions: []string{c.path}},
			{name: "an exception for repo-scope does not admit the command's flags", exceptions: []string{"repo-scope"}},
			{name: "--repo-scope alone leaves the default off", exceptions: []string{c.path + " --repo-scope"}},
			{name: "--repo-scope alone admits naming an entry", exceptions: []string{c.path + " --repo-scope"},
				args: []string{"--repo-scope", "checkout"}, scoped: true},
			{name: "both flags admit the default", exceptions: both(c.path), scoped: true},
			{name: "the environment opt-out still works", exceptions: both(c.path), env: map[string]string{noRepoScopeEnv: "1"}},
		} {
			t.Run(c.path+"/"+tc.name, func(t *testing.T) {
				srv := newDQLRecorder(t, "readonly", func(r *http.Request, _ string) (int, string) {
					if strings.HasSuffix(r.URL.Path, "/query:verify") {
						return http.StatusOK, `{"valid":true}`
					}
					return http.StatusOK, oneRecord
				})
				chdirTestRepo(t, checkoutScope)
				if tc.env["DTCTL_MIN_STABILITY"] == "" {
					writeIsolatedConfig(t, stableFloorConfig(srv.URL, tc.exceptions...))
				}

				code, stdout, stderr := runRepoScoped(append(append([]string{}, c.argv...), tc.args...), RunOptions{Env: tc.env})
				require.Zero(t, code, "stdout: %s\nstderr: %s", stdout, stderr)
				if !tc.scoped {
					require.Equal(t, repoScopeTypedQuery, srv.first(t))
					require.NotContains(t, stderr, "repo scope")
					require.NotContains(t, stdout, "repo_scope")
					return
				}
				require.Equal(t, repoScopeScopedQuery, srv.first(t))
				require.True(t, strings.HasPrefix(stderr, repoScopeNotice), stderr)
			})
		}
	}
}
