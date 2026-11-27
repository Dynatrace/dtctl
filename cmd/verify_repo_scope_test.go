package cmd

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"
)

// verify query is read by programs: its exit code gates a commit or a CI job,
// --canonical is written back to the file it came from, and its diagnostics
// carry positions in the text. So a scope must not change what it verifies
// unless the caller asked: like wait query it applies one only under
// --repo-scope <name>. Inside a linked repository whose covering entry would
// narrow the query it says so in one line on stderr, and is otherwise the
// command it is outside one: the same request, and the same stdout.
func TestVerifyQuery_ScopesOnlyUnderAName(t *testing.T) {
	optInRepoScope(t)
	const hint = "repo scope checkout covers this directory and is not applied: verify query applies a scope only when it is named, with --repo-scope checkout\n"
	// The canonical form is of whatever text the server was sent, so the
	// output shows which text that was.
	canonicalOf := func(query string) string { return "CANONICAL " + query }
	respond := func(_ *http.Request, query string) (int, string) {
		body, err := json.Marshal(map[string]any{"valid": true, "canonicalQuery": canonicalOf(query)})
		require.NoError(t, err)
		return http.StatusOK, string(body)
	}
	verify := func(args ...string) []string {
		return append([]string{"verify", "query", repoScopeTypedQuery, "--canonical", "-o", "json"}, args...)
	}
	canonical := func(t *testing.T, stdout string) string {
		t.Helper()
		var result struct {
			CanonicalQuery string `json:"canonicalQuery"`
		}
		require.NoError(t, json.Unmarshal([]byte(stdout), &result), stdout)
		return result.CanonicalQuery
	}

	// The reference: outside any repository, where the command is main's.
	srv := segmentRecorder(t, respond)
	t.Chdir(t.TempDir())
	code, plainOut, plainErr := runRepoScoped(verify(), RunOptions{})
	require.Zero(t, code, plainErr)
	reference := srv.requests()
	require.Len(t, reference, 1)
	require.Contains(t, reference[0], `"query":"`+repoScopeTypedQuery+`"`)
	require.Equal(t, canonicalOf(repoScopeTypedQuery), canonical(t, plainOut))
	require.NotContains(t, plainErr, "repo scope")

	for _, tc := range []struct {
		name string
		env  map[string]string
		hint string
	}{
		{name: "a covering entry is named in one line", hint: hint},
		{name: "not where repo scopes are turned off", env: map[string]string{noRepoScopeEnv: "1"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := segmentRecorder(t, respond)
			chdirTestRepo(t, segmentScope)

			code, stdout, stderr := runRepoScoped(verify(), RunOptions{Env: tc.env})
			require.Zero(t, code, stderr)
			require.Equal(t, reference, srv.requests(), "the request, whole, is the one sent outside a repository")
			require.Equal(t, plainOut, stdout, "the canonical form is the typed text's")
			require.Equal(t, plainErr+tc.hint, stderr)
		})
	}

	t.Run("an entry that is only segments says nothing: verify query sends none", func(t *testing.T) {
		srv := segmentRecorder(t, respond)
		repo := chdirTestRepo(t, segmentScope)
		writeRepoFiles(t, repo, map[string]string{"tools/README.md": "tools\n"})
		chdirSubdir(t, "tools")
		code, stdout, stderr := runRepoScoped(verify(), RunOptions{})
		require.Zero(t, code, stderr)
		require.Equal(t, reference, srv.requests())
		require.Equal(t, plainOut, stdout)
		require.Equal(t, plainErr, stderr)
	})

	t.Run("a file that does not load says nothing", func(t *testing.T) {
		srv := segmentRecorder(t, respond)
		chdirTestRepo(t, "environments: [")
		code, stdout, stderr := runRepoScoped(verify(), RunOptions{})
		require.Zero(t, code, stderr)
		require.Equal(t, reference, srv.requests())
		require.Equal(t, plainOut, stdout)
		require.Equal(t, plainErr, stderr)
	})

	t.Run("named, the scoped text is what is verified", func(t *testing.T) {
		srv := segmentRecorder(t, respond)
		chdirTestRepo(t, segmentScope)
		code, stdout, stderr := runRepoScoped(verify("--repo-scope", "checkout"), RunOptions{})
		require.Zero(t, code, stderr)
		require.Equal(t, []string{repoScopeScopedQuery}, srv.queries())
		require.Equal(t, canonicalOf(repoScopeScopedQuery), canonical(t, stdout))
		require.Contains(t, stderr, "applying repo scope checkout to fetch logs: ")
		require.NotContains(t, stderr, "covers this directory")
		require.NotContains(t, stderr, "--no-repo-scope", "the command has no opt-out to advise")
	})

	t.Run("named, an entry that cannot narrow the query is an error", func(t *testing.T) {
		srv := segmentRecorder(t, respond)
		chdirTestRepo(t, segmentScope)
		code, stdout, _ := runRepoScoped([]string{"verify", "query", "timeseries avg(dt.host.cpu.usage)", "--repo-scope", "checkout", "--agent"}, RunOptions{})
		require.NotZero(t, code)
		detail := agentError(t, stdout)
		require.Contains(t, detail.Message, "not_fetch")
		require.Equal(t, []string{"drop --repo-scope checkout to run the query as typed"}, detail.Suggestions)
		require.Empty(t, srv.requests())
	})

	t.Run("there is no opt-out, because there is no default", func(t *testing.T) {
		srv := segmentRecorder(t, respond)
		chdirTestRepo(t, segmentScope)
		code, _, stderr := runRepoScoped(verify("--no-repo-scope"), RunOptions{})
		require.NotZero(t, code)
		require.Contains(t, stderr, "unknown flag")
		require.Empty(t, srv.requests())
	})
}
