package cmd

import (
	"context"
	"net/http"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// wait query is read by its exit code, often where nobody reads stderr, so a
// scope must not change what it waits on unless the caller asked: it applies
// one only under --repo-scope <name>. Inside a linked repository whose
// covering entry would narrow the query it says so in one line, and is
// otherwise the command it is outside one: the same requests, in the same
// order, with the same output.
func TestWaitQuery_ScopesOnlyUnderAName(t *testing.T) {
	optInRepoScope(t)
	const hint = "repo scope checkout covers this directory and is not applied: wait query applies a scope only when it is named, with --repo-scope checkout\n"
	wait := func(query string, args ...string) []string {
		return append([]string{"wait", "query", query, "--for=any", "--min-interval", "1ms", "--max-interval", "1ms"}, args...)
	}

	// The reference: outside any repository.
	srv := segmentRecorder(t, answer(http.StatusOK, oneRecord))
	t.Chdir(t.TempDir())
	code, plainOut, plainErr := runRepoScoped(wait(repoScopeTypedQuery), RunOptions{})
	require.Zero(t, code, plainErr)
	reference := srv.requestLog()
	require.Equal(t, []string{"POST /platform/storage/query/v1/query:execute query=" + repoScopeTypedQuery}, reference)
	require.NotContains(t, plainErr, "repo scope")

	for _, tc := range []struct {
		name  string
		query string
		args  []string
		env   map[string]string
		hint  string
	}{
		{name: "a covering entry is named in one line", query: repoScopeTypedQuery, hint: hint},
		{name: "not under --quiet", query: repoScopeTypedQuery, args: []string{"--quiet"}},
		{name: "not where repo scopes are turned off", query: repoScopeTypedQuery, env: map[string]string{noRepoScopeEnv: "1"}},
		// Named, this entry's filter cannot be inserted into a timeseries
		// and the wait is refused, so the line would advise a dead end.
		{name: "not for a query the named entry refuses", query: "timeseries avg(dt.host.cpu.usage)"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := segmentRecorder(t, answer(http.StatusOK, oneRecord))
			chdirTestRepo(t, segmentScope)

			code, stdout, stderr := runRepoScoped(wait(tc.query, tc.args...), RunOptions{Env: tc.env})
			require.Zero(t, code, stderr)
			require.Equal(t, []string{"POST /platform/storage/query/v1/query:execute query=" + tc.query}, srv.requestLog(),
				"the typed query, no segment read and no segment sent")
			require.NotContains(t, stderr, "applying repo scope")
			if tc.hint == "" {
				require.NotContains(t, stderr, "repo scope")
				return
			}
			require.Contains(t, stderr, tc.hint)
			if tc.query == repoScopeTypedQuery {
				require.Equal(t, plainOut, stdout)
			}
		})
	}

	t.Run("the command the line names is one that runs", func(t *testing.T) {
		const timeseries = "timeseries avg(dt.host.cpu.usage)"
		srv := segmentRecorder(t, answer(http.StatusOK, oneRecord))
		repo := chdirTestRepo(t, segmentScope)
		writeRepoFiles(t, repo, map[string]string{"tools/README.md": "tools\n"})
		t.Chdir(filepath.Join(repo, "tools"))

		code, _, stderr := runRepoScoped(wait(timeseries), RunOptions{})
		require.Zero(t, code, stderr)
		require.Contains(t, stderr, "wait query applies a scope only when it is named, with --repo-scope tools\n")
		require.Equal(t, []string{"POST /platform/storage/query/v1/query:execute query=" + timeseries}, srv.requestLog())

		srv = segmentRecorder(t, answer(http.StatusOK, oneRecord))
		code, _, stderr = runRepoScoped(wait(timeseries, "--repo-scope", "tools"), RunOptions{})
		require.Zero(t, code, stderr)
		body, _ := srv.executed(t)
		require.Contains(t, body, `"filterSegments":[{"id":"plain-1"}]`)

		// The entry the line stayed silent about is refused when named.
		srv = segmentRecorder(t, answer(http.StatusOK, oneRecord))
		code, _, stderr = runRepoScoped(wait(timeseries, "--repo-scope", "checkout"), RunOptions{})
		require.NotZero(t, code)
		require.Contains(t, stderr, "not_fetch")
		require.Empty(t, srv.requests())
	})

	t.Run("an entry that cannot narrow the query says nothing", func(t *testing.T) {
		srv := newDQLRecorder(t, "readonly", answer(http.StatusOK, oneRecord))
		chdirTestRepo(t, checkoutScope)
		code, _, stderr := runRepoScoped(wait("timeseries avg(dt.host.cpu.usage)"), RunOptions{})
		require.Zero(t, code, stderr)
		require.Len(t, srv.queries(), 1)
		require.NotContains(t, stderr, "repo scope")
	})

	t.Run("a file that does not load says nothing", func(t *testing.T) {
		srv := newDQLRecorder(t, "readonly", answer(http.StatusOK, oneRecord))
		chdirTestRepo(t, "environments: [")
		code, _, stderr := runRepoScoped(wait(repoScopeTypedQuery), RunOptions{})
		require.Zero(t, code, stderr)
		require.Equal(t, repoScopeTypedQuery, srv.first(t))
		require.NotContains(t, stderr, "repo scope")
	})

	t.Run("named, the scope is applied", func(t *testing.T) {
		srv := segmentRecorder(t, answer(http.StatusOK, oneRecord))
		chdirTestRepo(t, segmentScope)
		code, _, stderr := runRepoScoped(wait(repoScopeTypedQuery, "--repo-scope", "checkout"), RunOptions{})
		require.Zero(t, code, stderr)
		body, reads := srv.executed(t)
		require.Contains(t, body, segmentsSent)
		require.Len(t, reads, 2)
		require.Contains(t, stderr, "applying repo scope checkout to fetch logs: ")
		require.NotContains(t, stderr, "covers this directory")
		require.NotContains(t, stderr, "--no-repo-scope", "the command has no opt-out to advise")
	})

	t.Run("there is no opt-out, because there is no default", func(t *testing.T) {
		srv := newDQLRecorder(t, "readonly", answer(http.StatusOK, oneRecord))
		chdirTestRepo(t, checkoutScope)
		code, _, stderr := runRepoScoped(wait(repoScopeTypedQuery, "--no-repo-scope"), RunOptions{})
		require.NotZero(t, code)
		require.Contains(t, stderr, "unknown flag")
		require.Empty(t, srv.queries())
	})
}

// A named wait checks its own flags before it reads the entry's segments: a
// command line that cannot run sends nothing.
func TestWaitQuery_ValidatesItsFlagsBeforeReadingSegments(t *testing.T) {
	srv := segmentRecorder(t, answer(http.StatusOK, oneRecord))
	chdirTestRepo(t, segmentScope)
	code, _, stderr := runRepoScoped([]string{"wait", "query", repoScopeTypedQuery, "--for=any", "--repo-scope", "checkout", "--min-interval=0s"}, RunOptions{})
	require.NotZero(t, code)
	require.Contains(t, stderr, "invalid backoff configuration")
	require.Empty(t, srv.requests())
}

// The outcome line compares a condition that was evaluated. A deadline that
// falls in the initial delay evaluated nothing.
func TestWaitQuery_NoOutcomeLineWithoutAnAttempt(t *testing.T) {
	srv := segmentRecorder(t, answer(http.StatusOK, oneRecord))
	chdirTestRepo(t, segmentScope)
	code, _, stderr := runRepoScoped([]string{"wait", "query", repoScopeTypedQuery, "--for=any", "--repo-scope", "checkout",
		"--timeout", "50ms", "--initial-delay", "2s"}, RunOptions{})
	require.NotZero(t, code)
	require.Empty(t, srv.queries())
	require.NotContains(t, stderr, "condition not met under repo scope")
}

// Only the wait's own deadline is its timeout. A read that ends on another
// one, here the invocation's with no --timeout at all, is reported as the
// interrupted read it is.
func TestWaitQuery_AnotherDeadlineIsNotTheWaitsTimeout(t *testing.T) {
	srv := segmentRecorder(t, answer(http.StatusOK, oneRecord))
	slowSegmentReads(srv)
	chdirTestRepo(t, segmentScope)

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	code, _, stderr := runRepoScoped([]string{"wait", "query", repoScopeTypedQuery, "--for=any", "--repo-scope", "checkout", "--timeout", "0"},
		RunOptions{Context: ctx})
	require.NotZero(t, code)
	require.Empty(t, srv.queries())
	require.NotContains(t, stderr, "Timeout reached")
	require.Contains(t, stderr, "reading filter segment Ab3dEf9hIjk was interrupted")
}
