package cmd

import (
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Dynatrace/dtctl/pkg/output"
)

// segmentScope has an entry with bindings and segments, and one, for tools/,
// that references a segment and binds nothing.
const segmentScope = `environments:
  127.0.0.1:
    - name: checkout
      service-names: [checkout]
      workloads:
        - namespace: payments
          name: checkout
      segments:
        - id: Ab3dEf9hIjk
          variables:
            environment: [production]
        - id: plain-1
    - name: tools
      path: tools
      segments:
        - id: plain-1
`

const (
	entrySegmentRefs = "Ab3dEf9hIjk?environment=production, plain-1"
	segmentsSent     = `"filterSegments":[{"id":"Ab3dEf9hIjk","variables":[{"name":"environment","values":["production"]}]},{"id":"plain-1"}]`
	segmentRequests  = "GET /platform/storage/filter-segments/v1/filter-segments/"
)

// segmentRecorder is a DQL recorder whose environment holds the segments
// segmentScope references.
func segmentRecorder(t *testing.T, respond func(*http.Request, string) (int, string)) *dqlRecorder {
	t.Helper()
	srv := newDQLRecorder(t, "readonly", respond)
	srv.segments = map[string]int{"Ab3dEf9hIjk": http.StatusOK, "plain-1": http.StatusOK}
	return srv
}

// executed is the body of the one query:execute request the server got, and
// reads the segment ids it was asked for before that.
func (s *dqlRecorder) executed(t *testing.T) (body string, reads []string) {
	t.Helper()
	for _, r := range s.requests() {
		switch {
		case strings.Contains(r, "/query:execute") && body == "":
			body = r
		case strings.HasPrefix(r, segmentRequests):
			id, _, _ := strings.Cut(strings.Fields(strings.TrimPrefix(r, segmentRequests))[0], "?")
			reads = append(reads, id)
		}
	}
	require.NotEmpty(t, body, "no query reached the server")
	return body, reads
}

// A query under an entry that references segments sends them beside the
// scoped text, as if --segment had been typed, after reading each once.
func TestQueryRepoScope_SendsTheEntrysSegments(t *testing.T) {
	srv := segmentRecorder(t, answer(http.StatusOK, oneRecord))
	chdirTestRepo(t, segmentScope)

	code, stdout, stderr := runRepoScoped([]string{"query", repoScopeTypedQuery, "--agent"}, RunOptions{})
	require.Zero(t, code, stderr)
	body, reads := srv.executed(t)
	require.Equal(t, repoScopeScopedQuery, srv.first(t))
	require.Contains(t, body, segmentsSent)
	require.Equal(t, []string{"Ab3dEf9hIjk", "plain-1"}, reads, "each segment is read once")
	require.Equal(t, "applying repo scope checkout to fetch logs: "+checkoutLogsFilter+" with segments "+entrySegmentRefs+
		" (use --no-repo-scope to ignore it); "+repoScopeScanClause+"\n", stderr)
	scope := repoScopeContext(t, stdout)
	require.True(t, scope.Applied)
	require.Equal(t, "applied", scope.Code)
	require.Equal(t, []output.RepoScopeSegment{
		{ID: "Ab3dEf9hIjk", Variables: map[string][]string{"environment": {"production"}}}, {ID: "plain-1"},
	}, scope.Segments)

	// Off is off for the whole entry: nothing is read and nothing sent.
	srv2 := segmentRecorder(t, answer(http.StatusOK, oneRecord))
	code, stdout, stderr = runRepoScoped([]string{"query", repoScopeTypedQuery, "--agent", "--no-repo-scope"}, RunOptions{})
	require.Zero(t, code, stderr)
	body, reads = srv2.executed(t)
	require.NotContains(t, body, "filterSegments")
	require.Empty(t, reads)
	require.Empty(t, stderr)
	require.Nil(t, repoScopeContext(t, stdout))
}

// Segments apply where no binding does. An entry that only references
// segments scopes any query, and an entry with bindings still sends its
// segments when its filter cannot be inserted, saying which half applied.
func TestQueryRepoScope_SegmentsWithoutAFilter(t *testing.T) {
	const timeseries = "timeseries avg(dt.host.cpu.usage)"
	srv := segmentRecorder(t, answer(http.StatusOK, oneRecord))
	repo := chdirTestRepo(t, segmentScope)
	writeRepoFiles(t, repo, map[string]string{"tools/README.md": "tools\n"})

	code, stdout, stderr := runRepoScoped([]string{"query", timeseries, "--agent", "--repo-scope", "tools"}, RunOptions{})
	require.Zero(t, code, stderr)
	body, _ := srv.executed(t)
	require.Equal(t, timeseries, srv.first(t), "an entry without bindings inserts nothing")
	require.Contains(t, body, `"filterSegments":[{"id":"plain-1"}]`)
	require.Equal(t, "applying repo scope tools: segments plain-1 (use --no-repo-scope to ignore it)\n", stderr)
	scope := repoScopeContext(t, stdout)
	require.Equal(t, []any{true, "applied", "", ""}, []any{scope.Applied, scope.Code, scope.Filter, scope.Reason})

	srv = segmentRecorder(t, answer(http.StatusOK, oneRecord))
	code, stdout, stderr = runRepoScoped([]string{"query", timeseries, "--agent"}, RunOptions{})
	require.Zero(t, code, stderr)
	body, _ = srv.executed(t)
	require.Equal(t, timeseries, srv.first(t))
	require.Contains(t, body, segmentsSent)
	require.Contains(t, stderr, "repo scope checkout: its filter was not inserted: ")
	require.Contains(t, stderr, "; its segments "+entrySegmentRefs+" still apply (use --no-repo-scope to ignore them)\n")
	scope = repoScopeContext(t, stdout)
	require.True(t, scope.Applied, "the query sent was narrowed")
	require.Empty(t, scope.Filter)
	require.Contains(t, scope.Reason, "filter not inserted (not_fetch): ")
	require.Len(t, scope.Segments, 2)

	// Naming an entry asks for its rows; a filter that cannot be inserted
	// stays the error it was.
	code, stdout, _ = runRepoScoped([]string{"query", timeseries, "--agent", "--repo-scope", "checkout"}, RunOptions{})
	require.NotZero(t, code)
	require.Equal(t, "validation_error", agentError(t, stdout).Code)
}

// The platform fails a query that names a segment it cannot find, so one
// deleted segment would break every query in the repository. The entry's
// segments are read first, and when one cannot be, the query runs exactly as
// typed, without the filter either: half a scope would look scoped.
func TestQueryRepoScope_SegmentUnavailable(t *testing.T) {
	for name, tc := range map[string]struct {
		segments map[string]int
		reason   string
	}{
		"deleted":    {map[string]int{"plain-1": http.StatusOK}, "filter segment Ab3dEf9hIjk does not exist in this environment, or is not shared with you"},
		"unreadable": {map[string]int{"Ab3dEf9hIjk": http.StatusForbidden, "plain-1": http.StatusOK}, "filter segment Ab3dEf9hIjk could not be read ("},
	} {
		t.Run(name, func(t *testing.T) {
			srv := newDQLRecorder(t, "readonly", answer(http.StatusOK, oneRecord))
			srv.segments = tc.segments
			chdirTestRepo(t, segmentScope)

			code, stdout, stderr := runRepoScoped([]string{"query", repoScopeTypedQuery, "--agent"}, RunOptions{})
			require.Zero(t, code, stderr)
			body, _ := srv.executed(t)
			require.Equal(t, repoScopeTypedQuery, srv.first(t))
			require.NotContains(t, body, "filterSegments")
			require.Contains(t, stderr, "repo scope checkout not applied: "+tc.reason)
			require.Contains(t, stderr, "; the query runs unscoped (use --no-repo-scope to silence this)\n")
			require.NotContains(t, stderr, "applying repo scope")
			scope := repoScopeContext(t, stdout)
			require.False(t, scope.Applied)
			require.Equal(t, "segment_unavailable", scope.Code)
			require.Contains(t, scope.Reason, tc.reason)
			require.Empty(t, scope.Segments)
			require.Empty(t, scope.Filter)

			// Asked for by name, the entry's rows are what the caller wants.
			srv.reset()
			code, stdout, _ = runRepoScoped([]string{"query", repoScopeTypedQuery, "--agent", "--repo-scope", "checkout"}, RunOptions{})
			require.NotZero(t, code)
			detail := agentError(t, stdout)
			require.Equal(t, "validation_error", detail.Code)
			require.Contains(t, detail.Message, tc.reason)
			require.Empty(t, srv.queries(), "nothing is sent")
		})
	}
}

// Only a segment that is not there, or not the caller's to read, drops the
// scope. Any other failed read says nothing about the segment, and running
// environment-wide on a failure that may pass a moment later would return
// rows the caller takes for scoped ones: it is an error and nothing is sent.
// The status here is one the client does not retry; a 500, a 429 and a
// timeout take the same branch after its retries.
func TestQueryRepoScope_SegmentReadFails(t *testing.T) {
	const message = `repo scope checkout: filter segment Ab3dEf9hIjk could not be read (get segment: API error (400): 400 Bad Request); the query was not sent: retry, or pass --no-repo-scope to run it as typed`
	for _, args := range [][]string{nil, {"--repo-scope", "checkout"}} {
		srv := newDQLRecorder(t, "readonly", answer(http.StatusOK, oneRecord))
		srv.segments = map[string]int{"Ab3dEf9hIjk": http.StatusBadRequest, "plain-1": http.StatusOK}
		chdirTestRepo(t, segmentScope)

		code, stdout, stderr := runRepoScoped(append([]string{"query", repoScopeTypedQuery, "--agent"}, args...), RunOptions{})
		require.NotZero(t, code)
		require.Equal(t, message, agentError(t, stdout).Message)
		require.Empty(t, srv.queries(), "nothing is sent")
		require.NotContains(t, stderr, "applying repo scope")

		code, stdout, _ = runRepoScoped(append([]string{"wait", "query", repoScopeTypedQuery, "--for=any", "--agent"}, args...), RunOptions{})
		require.NotZero(t, code)
		require.Equal(t, message, agentError(t, stdout).Message)
		require.Empty(t, srv.queries())
	}
}

// The read asks for the segment and nothing more: it only needs to know the
// segment is there.
func TestQueryRepoScope_SegmentReadAsksForNoFields(t *testing.T) {
	srv := segmentRecorder(t, answer(http.StatusOK, oneRecord))
	chdirTestRepo(t, segmentScope)
	code, _, stderr := runRepoScoped([]string{"query", repoScopeTypedQuery}, RunOptions{})
	require.Zero(t, code, stderr)
	require.Equal(t, []string{segmentRequests + "Ab3dEf9hIjk ", segmentRequests + "plain-1 "}, srv.requests()[:2])
}

// wait query polls until its condition holds, so a wait whose scope was
// dropped would poll the whole environment for minutes on one stderr line.
// It fails before the first attempt instead.
func TestWaitQuery_SegmentUnavailable(t *testing.T) {
	srv := newDQLRecorder(t, "readonly", answer(http.StatusOK, oneRecord))
	srv.segments = map[string]int{"plain-1": http.StatusOK}
	chdirTestRepo(t, segmentScope)

	code, stdout, _ := runRepoScoped([]string{"wait", "query", repoScopeTypedQuery, "--for=any", "--agent"}, RunOptions{})
	require.NotZero(t, code)
	require.Equal(t, "repo scope checkout cannot be applied: filter segment Ab3dEf9hIjk does not exist in this environment, or is not shared with you; "+
		"wait query does not poll unscoped: correct the entry's segments in .dtctl-repo-scope.yaml, or pass --no-repo-scope", agentError(t, stdout).Message)
	require.Empty(t, srv.queries(), "no attempt is made")

	code, _, stderr := runRepoScoped([]string{"wait", "query", repoScopeTypedQuery, "--for=any", "--plain", "--no-repo-scope"}, RunOptions{})
	require.Zero(t, code, stderr)
	require.Equal(t, repoScopeTypedQuery, srv.first(t))
}

// What is typed wins over what is committed: a segment the user also names
// is sent once, with the user's variables. Together they cannot exceed what a
// query takes, and that is an error rather than a silent choice.
func TestQueryRepoScope_SegmentsBesideTheUsers(t *testing.T) {
	srv := segmentRecorder(t, answer(http.StatusOK, oneRecord))
	srv.segments["other-2"] = http.StatusOK
	chdirTestRepo(t, segmentScope)

	code, stdout, stderr := runRepoScoped([]string{"query", repoScopeTypedQuery, "--agent",
		"--segment", "Ab3dEf9hIjk?environment=staging", "--segment", "other-2"}, RunOptions{})
	require.Zero(t, code, stderr+stdout)
	body, _ := srv.executed(t)
	require.Contains(t, body, `"filterSegments":[{"id":"Ab3dEf9hIjk","variables":[{"name":"environment","values":["staging"]}]},{"id":"other-2"},{"id":"plain-1"}]`)
	require.Contains(t, stderr, "repo scope checkout: segment Ab3dEf9hIjk is also given on the command line; its variables there are used\n")

	args := []string{"query", repoScopeTypedQuery, "--agent"}
	for _, id := range []string{"u1", "u2", "u3", "u4", "u5", "u6", "u7", "u8", "u9"} {
		srv.segments[id] = http.StatusOK
		args = append(args, "--segment", id)
	}
	srv.reset()
	code, stdout, _ = runRepoScoped(args, RunOptions{})
	require.NotZero(t, code)
	require.Equal(t, "too many segments: 9 given and 2 from repo scope checkout, maximum is 10 per query; give fewer, or pass --no-repo-scope",
		agentError(t, stdout).Message)
	require.Empty(t, srv.queries())
}

// wait query sends the entry's segments on every attempt and reads them
// once. verify query checks the text alone, and says the segments are not
// part of what it checked.
func TestWaitQueryAndVerifyQuery_EntrySegments(t *testing.T) {
	srv := segmentRecorder(t, answer(http.StatusOK, oneRecord))
	chdirTestRepo(t, segmentScope)

	code, _, stderr := runRepoScoped([]string{"wait", "query", repoScopeTypedQuery, "--for=any"}, RunOptions{})
	require.Zero(t, code, stderr)
	body, reads := srv.executed(t)
	require.Contains(t, body, segmentsSent)
	require.Len(t, reads, 2)
	require.Contains(t, stderr, " with segments "+entrySegmentRefs+" (use --no-repo-scope to ignore it)")

	srv = segmentRecorder(t, answer(http.StatusOK, `{"valid":true}`))
	code, _, stderr = runRepoScoped([]string{"verify", "query", repoScopeTypedQuery}, RunOptions{})
	require.Zero(t, code, stderr)
	require.Equal(t, repoScopeScopedQuery, srv.first(t))
	for _, r := range srv.requests() {
		require.NotContains(t, r, "filter-segments", "verification reads no segment")
		require.NotContains(t, r, "filterSegments")
	}
	require.True(t, strings.HasPrefix(stderr, repoScopeApplying+"\n"+
		"repo scope checkout: its segments "+entrySegmentRefs+" are not checked; verify query sends the query text only\n"), stderr)
}

// Whether a segment's variables are all bound is the platform's to say. Its
// failure names the scope and the segments, and the way to rule them out.
func TestQueryRepoScope_SegmentErrorNamesTheScope(t *testing.T) {
	segmentRecorder(t, answer(http.StatusBadRequest,
		`{"error":{"code":400,"message":"Segment requires a variable.","details":{"errorType":"FILTER_SEGMENT_REQUIRES_VARIABLE"}}}`))
	chdirTestRepo(t, segmentScope)

	code, stdout, _ := runRepoScoped([]string{"query", repoScopeTypedQuery, "--agent"}, RunOptions{})
	require.NotZero(t, code)
	suggestions := strings.Join(agentError(t, stdout).Suggestions, "\n")
	require.Contains(t, suggestions, "repo scope checkout also sent the filter segments "+entrySegmentRefs+
		"; if the failure names a segment or one of its variables, correct the entry's segments in .dtctl-repo-scope.yaml")
	require.Contains(t, suggestions, "--no-repo-scope")
}
