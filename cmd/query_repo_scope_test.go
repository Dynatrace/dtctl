package cmd

import (
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Dynatrace/dtctl/pkg/output"
	"github.com/Dynatrace/dtctl/pkg/reposcope"
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
	repoScopeApplying    = "applying repo scope checkout to fetch logs: " + checkoutLogsFilter + " (use --no-repo-scope to ignore it)"
	repoScopeScanClause  = "the repo scope filters early with ==, but no scan limit is set for this query, so the environment's default cap applies: add scanLimitGBytes: or --default-scan-limit-gbytes to scan less"
	// repoScopeNotice is the line for a run nothing bounds the scan of, as
	// in a test that sets no limit.
	repoScopeNotice = repoScopeApplying + "; " + repoScopeScanClause + "\n"
	oneRecord       = `{"state":"SUCCEEDED","result":{"records":[{"content":"hello"}]}}`
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
		{"opt-out set to true", map[string]string{noRepoScopeEnv: "true"}, nil, false},
		{"opt-out set to yes", map[string]string{noRepoScopeEnv: "yes"}, nil, false},
		{"opt-out set to any other text", map[string]string{noRepoScopeEnv: "on"}, nil, false},
		{"opt-out set to 0", map[string]string{noRepoScopeEnv: "0"}, nil, true},
		{"opt-out set to false", map[string]string{noRepoScopeEnv: "false"}, nil, true},
		{"opt-out set to FALSE", map[string]string{noRepoScopeEnv: "FALSE"}, nil, true},
		{"opt-out set to no", map[string]string{noRepoScopeEnv: "no"}, nil, true},
		{"opt-out set to off", map[string]string{noRepoScopeEnv: "off"}, nil, true},
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
	repo := chdirTestRepo(t, strings.Replace(checkoutScope, "[checkout]", `["check\"out"]`, 1))

	code, stdout, stderr := runRepoScoped([]string{"query", repoScopeTypedQuery, "--agent"}, RunOptions{})
	require.Zero(t, code, stderr)
	require.Equal(t, repoScopeTypedQuery, srv.first(t))
	require.Equal(t, 1, strings.Count(stderr, "\n"), stderr)
	scope := repoScopeContext(t, stdout)
	require.Equal(t, "invalid_file", scope.Code)
	// The file is named the way repo-scope current names it, relative to the
	// repository root: an envelope may be forwarded, and where the checkout
	// sits on this machine is nobody else's business.
	require.True(t, strings.HasPrefix(scope.Reason, ".dtctl-repo-scope.yaml: "), scope.Reason)
	require.NotContains(t, stdout+stderr, filepath.Base(filepath.Dir(repo)))

	srv.reset()
	code, stdout, _ = runRepoScoped([]string{"query", repoScopeTypedQuery, "--repo-scope", "checkout", "--agent"}, RunOptions{})
	require.NotZero(t, code)
	require.Equal(t, "validation_error", agentError(t, stdout).Code)
	require.Empty(t, srv.queries(), "a query naming an unusable scope is not sent")
}

// A workload key this dtctl does not know may be a constraint: a misspelt
// cluster-uid, read past, leaves the workload bound in every cluster. The
// scope is not applied and says so; it is never reported as applied while
// selecting more than the file says.
func TestQueryRepoScope_UnknownEntryKeyIsNotApplied(t *testing.T) {
	optInRepoScope(t)
	srv := newDQLRecorder(t, "readonly", answer(http.StatusOK, oneRecord))
	chdirTestRepo(t, strings.Replace(checkoutScope, "        - namespace: payments\n",
		"        - cluster_uid: 11111111-2222-3333-4444-555555555555\n          namespace: payments\n", 1))
	const reason = `.dtctl-repo-scope.yaml: unknown key "cluster_uid" on line 6, in a workload (a typo, or written by a newer dtctl); ` +
		"a key there can narrow the entry, so the file is not applied without it"

	code, stdout, stderr := runRepoScoped([]string{"query", repoScopeTypedQuery, "--agent"}, RunOptions{})
	require.Zero(t, code, stderr)
	require.Equal(t, repoScopeTypedQuery, srv.first(t))
	require.Equal(t, "Warning: repo scope not applied: "+reason+"; the query runs unscoped (use --no-repo-scope to silence this)\n", stderr)
	scope := repoScopeContext(t, stdout)
	require.Equal(t, []any{false, "invalid_file", reason}, []any{scope.Applied, scope.Code, scope.Reason})

	srv.reset()
	code, stdout, _ = runRepoScoped([]string{"query", repoScopeTypedQuery, "--repo-scope", "checkout", "--agent"}, RunOptions{})
	require.NotZero(t, code)
	require.Equal(t, "validation_error", agentError(t, stdout).Code)
	require.Empty(t, srv.queries())
}

// A document key this dtctl does not know leaves the entry in force, and the
// run says so on stderr and in context.repo_scope.reason.
func TestQueryRepoScope_UnknownKeys(t *testing.T) {
	optInRepoScope(t)
	srv := newDQLRecorder(t, "readonly", answer(http.StatusOK, oneRecord))
	chdirTestRepo(t, "metrics: [dt.service.request.count]\n"+checkoutScope)

	code, stdout, stderr := runRepoScoped([]string{"query", repoScopeTypedQuery, "--agent"}, RunOptions{})
	require.Zero(t, code, stderr)
	require.Equal(t, repoScopeScopedQuery, srv.first(t))
	require.Contains(t, stderr, `unknown key "metrics"`)
	s := repoScopeContext(t, stdout)
	require.True(t, s.Applied)
	require.Contains(t, s.Reason, `unknown key "metrics"`)

	// A query the scope does not apply to says each thing once: the keys in
	// their own warning, why it was not applied in the other. The envelope
	// has one reason field, which carries both.
	code, stdout, stderr = runRepoScoped([]string{"query", "timeseries avg(dt.host.cpu.usage)", "--agent"}, RunOptions{})
	require.Zero(t, code, stderr)
	require.Equal(t, 1, strings.Count(stderr, `unknown key "metrics"`), stderr)
	require.Contains(t, stderr, "repo scope checkout not applied: the query does not start with 'fetch <data object>'; the query runs unscoped")
	s = repoScopeContext(t, stdout)
	require.Contains(t, s.Reason, "does not start with")
	require.Contains(t, s.Reason, `unknown key "metrics"`)
}

// The scan clause and the scan-cap line name the comparison the scope
// inserted, which for entity ids is in(), not ==.
func TestQueryRepoScope_ScanLinesNameTheFilter(t *testing.T) {
	optInRepoScope(t)
	newDQLRecorder(t, "readonly", answer(http.StatusOK, scanCappedRecord))
	const ids = "environments:\n  127.0.0.1:\n    - name: checkout\n      process-groups: [PROCESS_GROUP-FEDCBA9876543210]\n"
	for _, tc := range []struct {
		name, file, with string
	}{
		{"ids only", ids, "in()"},
		{"ids and a workload", ids + "      workloads:\n        - namespace: payments\n          name: checkout\n", "== and in()"},
		{"names only", checkoutScope, "=="},
	} {
		t.Run(tc.name, func(t *testing.T) {
			chdirTestRepo(t, tc.file)
			_, stdout, stderr := runRepoScoped([]string{"query", repoScopeTypedQuery, "--agent"}, RunOptions{})
			clause := "the repo scope filters early with " + tc.with + ", but no scan limit is set for this query"
			capped := "repo scope checkout already filters early with " + tc.with + ", and the scan still hit its cap"
			require.Contains(t, stderr, clause)
			require.Contains(t, stderr, capped)
			require.Contains(t, strings.Join(agentEnvelope(t, stdout).Context.Suggestions, "\n"), capped)

			_, _, stderr = runRepoScoped([]string{"query", repoScopeTypedQuery, "--agent", "--default-scan-limit-gbytes", "5"}, RunOptions{})
			require.Contains(t, stderr, capped)
			require.NotContains(t, stderr, clause)
		})
	}
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

// An ungrouped summarize returns one row for nothing: the count is in the
// row, and it is zero. That is the same empty answer, so it gets the same
// hint. No other aggregate is read that way: a zero countIf says nothing
// about how many records the scope let through.
func TestQueryRepoScope_ZeroCountHint(t *testing.T) {
	optInRepoScope(t)
	chdirTestRepo(t, checkoutScope)
	const hint = "0 records under repo scope checkout; compare with --no-repo-scope\n"
	row := func(field, value string) string {
		return `{"state":"SUCCEEDED","result":{"records":[{"` + field + `":` + value + `}]}}`
	}
	for _, tc := range []struct {
		name, query, body string
		hinted            bool
	}{
		{"a zero count", "fetch logs | summarize c = count()", row("c", `"0"`), true},
		{"a zero count as a number", "fetch logs | summarize c = count()", row("c", `0`), true},
		{"an unnamed zero count", "fetch logs | summarize count()", row("count()", `"0"`), true},
		{"a count of records", "fetch logs | summarize c = count()", row("c", `"7"`), false},
		{"a zero that is not a count of records", `fetch logs | summarize errors = countIf(loglevel == "ERROR")`, row("errors", `"0"`), false},
		{"a zero field of a record", "fetch logs | fields c | limit 1", row("c", `"0"`), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			newDQLRecorder(t, "readonly", answer(http.StatusOK, tc.body))
			code, stdout, stderr := runRepoScoped([]string{"query", tc.query, "--agent"}, RunOptions{})
			require.Zero(t, code, stderr)
			suggestions := agentEnvelope(t, stdout).Context.Suggestions
			if !tc.hinted {
				require.NotContains(t, stderr, "0 records under repo scope")
				require.NotContains(t, strings.Join(suggestions, "\n"), "0 records under repo scope")
				return
			}
			require.True(t, strings.HasSuffix(stderr, hint), stderr)
			require.True(t, strings.HasPrefix(suggestions[0], "0 records under repo scope checkout: "), suggestions[0])
		})
		// The same on every path a result takes to the output: -o jsonl
		// streams its rows instead of holding them.
		for _, format := range []string{"json", "jsonl"} {
			t.Run(tc.name+" as "+format, func(t *testing.T) {
				newDQLRecorder(t, "readonly", answer(http.StatusOK, tc.body))
				code, _, stderr := runRepoScoped([]string{"query", tc.query, "--plain", "--no-agent", "-o", format}, RunOptions{})
				require.Zero(t, code, stderr)
				require.Equal(t, tc.hinted, strings.Contains(stderr, hint), stderr)
			})
		}
	}
}

// wait query prints no hint per attempt, since zero rows are usually the
// condition not being met yet. Its outcome is where a scope matters: a wait
// that ends unmet, or that is satisfied by zero records, says once that the
// scope was applied, long after the line that announced it scrolled away.
func TestWaitQueryRepoScope_OutcomeNamesTheScope(t *testing.T) {
	optInRepoScope(t)
	chdirTestRepo(t, checkoutScope)
	const (
		noRecords = `{"state":"SUCCEEDED","result":{"records":[]}}`
		unmet     = "condition not met under repo scope checkout; compare with --no-repo-scope\n"
		vacuous   = "0 records under repo scope checkout; compare with --no-repo-scope\n"
	)
	poll := []string{"--max-attempts", "2", "--min-interval", "1ms", "--max-interval", "1ms"}
	for _, tc := range []struct {
		name, body, condition string
		extra                 []string
		code                  int
		line                  string
	}{
		{"none is satisfied by nothing", noRecords, "none", nil, 0, vacuous},
		{"any never arrives", noRecords, "any", nil, 2, unmet},
		{"a count is not reached", oneRecord, "count-gte=5", nil, 2, unmet},
		{"any arrives", oneRecord, "any", nil, 0, ""},
		{"unscoped and unmet", noRecords, "any", []string{"--no-repo-scope"}, 2, ""},
		{"unscoped and satisfied by nothing", noRecords, "none", []string{"--no-repo-scope"}, 0, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			newDQLRecorder(t, "readonly", answer(http.StatusOK, tc.body))
			args := append([]string{"wait", "query", repoScopeTypedQuery, "--for=" + tc.condition}, poll...)
			code, _, stderr := runRepoScoped(append(args, tc.extra...), RunOptions{})
			require.Equal(t, tc.code, code, stderr)
			if tc.line == "" {
				require.NotContains(t, stderr, "under repo scope")
				return
			}
			require.True(t, strings.HasSuffix(stderr, tc.line), stderr)
			require.Equal(t, 1, strings.Count(stderr, "under repo scope"), stderr)
		})
	}
}

// scanCappedRecord is one record from a scan the 5 GB cap stopped.
const scanCappedRecord = `{"state":"SUCCEEDED","result":{"records":[{"content":"hello"}],"metadata":{"grail":{"notifications":[` +
	`{"notificationType":"SCAN_LIMIT_GBYTES","severity":"WARN","message":"Scan limit of 5 gigabytes reached"}]}}}}`

const repoScopeScanCapNotice = "repo scope checkout already filters early with ==, and the scan still hit its cap; scan less another way the scan-limit hint lists"

// A scoped query nothing bounds the scan of says so before it is sent: in the
// scope line, and as the envelope's one scan suggestion. A positive scan limit
// (ScanCapNotice runs with one) or a sampling ratio above 1 bounds it. A
// limit of -1, as a flag or in the query, lifts the environment's default cap
// as well, and the clause says that instead of naming a cap that is gone.
func TestQueryRepoScope_ScanClause(t *testing.T) {
	optInRepoScope(t)
	newDQLRecorder(t, "readonly", answer(http.StatusOK, oneRecord))
	chdirTestRepo(t, checkoutScope)
	const lifted = "the repo scope filters early with ==, and no scan limit applies to this query (a scanLimitGBytes of -1): set a positive one to bound the scan"

	for _, tc := range []struct {
		name   string
		query  string
		args   []string
		clause string
	}{
		{"no limit", repoScopeTypedQuery, nil, repoScopeScanClause},
		{"unlimited", repoScopeTypedQuery, []string{"--default-scan-limit-gbytes", "-1"}, lifted},
		{"unlimited in the query", "fetch logs, scanLimitGBytes: -1 | limit 5", nil, lifted},
		{"unlimited in the query, whatever the flag", "fetch logs, scanLimitGBytes: -1 | limit 5", []string{"--default-scan-limit-gbytes", "5"}, lifted},
		{"a sampling ratio", repoScopeTypedQuery, []string{"--default-sampling-ratio", "100"}, ""},
		{"a limit in the query", "fetch logs, scanLimitGBytes: 5 | limit 5", nil, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			code, stdout, stderr := runRepoScoped(append([]string{"query", tc.query, "--agent"}, tc.args...), RunOptions{})
			require.Zero(t, code, stderr)
			suggestions := agentEnvelope(t, stdout).Context.Suggestions
			if tc.clause == "" {
				require.NotContains(t, stderr, "scan limit")
				require.NotContains(t, strings.Join(suggestions, "\n"), "scan limit")
				return
			}
			require.Equal(t, repoScopeApplying+"; "+tc.clause+"\n", stderr)
			require.Contains(t, suggestions, tc.clause)
		})
	}
}

// A scope's filter helps the scan but cannot by itself keep a wide timeframe
// under the cap, so a scoped query the cap stopped says so once: on stderr in
// every mode and as a suggestion in the envelope, on the buffered and the
// streamed (-o jsonl) path alike. wait query polls, and still says it once.
func TestQueryRepoScope_ScanCapNotice(t *testing.T) {
	optInRepoScope(t)
	srv := newDQLRecorder(t, "readonly", answer(http.StatusOK, scanCappedRecord))
	chdirTestRepo(t, checkoutScope)
	bounded := []string{"--default-scan-limit-gbytes", "5"}

	for _, mode := range [][]string{{"--plain"}, {"--agent"}, {"-o", "jsonl"}} {
		code, stdout, stderr := runRepoScoped(append(append([]string{"query", repoScopeTypedQuery}, bounded...), mode...), RunOptions{})
		require.Zero(t, code, stderr)
		require.Equal(t, 1, strings.Count(stderr, repoScopeScanCapNotice), "%v: %s", mode, stderr)
		if mode[0] == "--agent" {
			require.Contains(t, agentEnvelope(t, stdout).Context.Suggestions, repoScopeScanCapNotice)
		}
	}

	code, stdout, stderr := runRepoScoped([]string{"query", repoScopeTypedQuery, "--agent", "--no-repo-scope"}, RunOptions{})
	require.Zero(t, code, stderr)
	require.NotContains(t, stderr, "repo scope")
	require.NotContains(t, stdout, "under repo scope")

	wait := []string{"wait", "query", repoScopeTypedQuery, "--for=count-gte=5",
		"--max-attempts", "2", "--min-interval", "1ms", "--max-interval", "1ms"}
	srv.reset()
	_, _, stderr = runRepoScoped(append(append([]string{}, wait...), bounded...), RunOptions{})
	require.Len(t, srv.queries(), 2)
	require.Equal(t, 1, strings.Count(stderr, repoScopeScanCapNotice), stderr)

	// What is said before the run is advice; the cap that was then hit is an
	// observation, and is reported once whatever the advice said.
	_, _, stderr = runRepoScoped(wait, RunOptions{})
	require.Equal(t, 1, strings.Count(stderr, repoScopeScanClause), stderr)
	require.Equal(t, 1, strings.Count(stderr, repoScopeScanCapNotice), stderr)

	code, stdout, stderr = runRepoScoped([]string{"query", repoScopeTypedQuery, "--agent"}, RunOptions{})
	require.Zero(t, code, stderr)
	require.Equal(t, repoScopeNotice+repoScopeScanCapNotice+"\n", stderr)
	suggestions := agentEnvelope(t, stdout).Context.Suggestions
	require.Contains(t, suggestions, repoScopeScanCapNotice)
	require.NotContains(t, suggestions, repoScopeScanClause, "one scan suggestion, and the observation is it")
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

	// Nor does a failure that is not the platform refusing the query: a --jq
	// program that fails after a scoped query succeeded is the program's, and
	// running unscoped would not help it.
	newDQLRecorder(t, "readonly", answer(http.StatusOK, oneRecord))
	code, stdout, _ = runRepoScoped([]string{"query", repoScopeTypedQuery, "--agent", "--jq", `.records[] | error("x")`}, RunOptions{})
	require.NotZero(t, code)
	detail = agentError(t, stdout)
	require.Contains(t, detail.Message, "--jq")
	require.NotContains(t, strings.Join(detail.Suggestions, "\n"), "repo scope")
	require.NotContains(t, strings.Join(detail.Suggestions, "\n"), "--no-repo-scope")
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
// that reports the scope. Verifying scans nothing, so its line has no scan
// clause.
func TestWaitQueryAndVerifyQueryApplyRepoScope(t *testing.T) {
	optInRepoScope(t)
	for _, tc := range []struct {
		name   string
		argv   []string
		body   string
		notice string
	}{
		{"wait query", []string{"wait", "query", repoScopeTypedQuery, "--for=any"}, oneRecord, repoScopeNotice},
		{"verify query", []string{"verify", "query", repoScopeTypedQuery}, `{"valid":true}`, repoScopeApplying + "\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := newDQLRecorder(t, "readonly", answer(http.StatusOK, tc.body))
			chdirTestRepo(t, checkoutScope)

			code, _, stderr := runRepoScoped(tc.argv, RunOptions{})
			require.Zero(t, code, stderr)
			require.Equal(t, repoScopeScopedQuery, srv.first(t))
			require.True(t, strings.HasPrefix(stderr, tc.notice), stderr)

			for _, mode := range [][]string{{"--agent"}, {"--agent", "-o", "json"}} {
				srv.reset()
				code, stdout, stderr := runRepoScoped(append(append([]string{}, tc.argv...), mode...), RunOptions{})
				require.Zero(t, code, stderr)
				require.Equal(t, repoScopeScopedQuery, srv.first(t))
				require.True(t, strings.HasPrefix(stderr, tc.notice), "%v: %s", mode, stderr)
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
				// A named entry needs no hint that it can be ignored.
				applying := repoScopeApplying
				if len(tc.args) > 0 {
					applying = strings.TrimSuffix(applying, " (use --no-repo-scope to ignore it)")
				}
				require.True(t, strings.HasPrefix(stderr, applying), stderr)
				require.Equal(t, len(tc.args) == 0, strings.Contains(stderr, "--no-repo-scope"), "the opt-out is named only where the floor admits it: %s", stderr)
			})
		}
	}
}

// dtui's workspace file is dtui's: a `.dtui.yaml` beside the scope file
// changes nothing about a query. Its environment must not pick the context,
// which in a CLI that writes would let a cloned file choose the environment;
// its timeframe and its segments, by name or by id, must not travel with the
// query; and the file is not opened at all.
//
// Each of those is a difference the comparison can see. The whole of every
// request is compared, not the query text, because a timeframe or a segment
// id is sent beside the query and needs no lookup. The file's environment
// does not resolve, so a run that honoured it would never reach the recorder.
// And a workspace file that cannot be read, or is a directory, leaves the run
// exactly as it was, which a run that opened it could not.
func TestQueryLeavesTheDtuiWorkspaceFileUnread(t *testing.T) {
	const workspaceFile = `version: 1
environment: https://dtui-workspace.invalid
timeframe: 30m
segments:
  - payments-prod
  - 5fRkT2mQx7L
`
	type outcome struct {
		code           int
		requests       []string
		stdout, stderr string
	}
	run := func(t *testing.T, place func(t *testing.T, path string)) outcome {
		srv := newDQLRecorder(t, "readonly", answer(http.StatusOK, oneRecord))
		dir := chdirTestRepo(t, checkoutScope)
		if place != nil {
			place(t, filepath.Join(dir, ".dtui.yaml"))
		}
		code, stdout, stderr := runRepoScoped([]string{"query", repoScopeTypedQuery}, RunOptions{})
		return outcome{code, srv.requests(), stdout, stderr}
	}

	var without outcome
	t.Run("scope file only", func(t *testing.T) { without = run(t, nil) })
	require.Zero(t, without.code, without.stderr)
	require.Len(t, without.requests, 1)
	require.Contains(t, without.requests[0], "/query:execute")
	require.NotContains(t, without.requests[0], "filterSegments")
	require.NotContains(t, without.requests[0], "defaultTimeframe")

	for name, place := range map[string]func(t *testing.T, path string){
		"with .dtui.yaml": func(t *testing.T, path string) {
			require.NoError(t, os.WriteFile(path, []byte(workspaceFile), 0o644))
		},
		"an unreadable .dtui.yaml": func(t *testing.T, path string) {
			if runtime.GOOS == "windows" || os.Geteuid() == 0 {
				t.Skip("the file mode does not keep this user from reading")
			}
			require.NoError(t, os.WriteFile(path, []byte(workspaceFile), 0o000))
		},
		"a directory named .dtui.yaml": func(t *testing.T, path string) {
			require.NoError(t, os.Mkdir(path, 0o755))
		},
	} {
		t.Run(name, func(t *testing.T) {
			require.Equal(t, without, run(t, place))
		})
	}
}
