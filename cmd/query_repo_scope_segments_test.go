package cmd

import (
	"context"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/require"

	"github.com/Dynatrace/dtctl/pkg/dql"
	"github.com/Dynatrace/dtctl/pkg/exec"
	"github.com/Dynatrace/dtctl/pkg/output"
	"github.com/Dynatrace/dtctl/pkg/reposcope"
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
	require.Equal(t, "applying repo scope tools: segments plain-1\n", stderr)
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
	// The entry's bindings were refused, so the rows are wider than the file
	// says: that is never reported as applied. The code is the refusal's, and
	// the segments that were sent are listed.
	scope = repoScopeContext(t, stdout)
	require.False(t, scope.Applied, "a scope without its filter is wider than the file says")
	require.Equal(t, "not_fetch", scope.Code)
	require.Empty(t, scope.Filter)
	require.Equal(t, "the query does not start with 'fetch <data object>'; the entry's filter segments were sent", scope.Reason)
	require.Len(t, scope.Segments, 2)
	require.Contains(t, agentEnvelope(t, stdout).Context.Suggestions, "run unscoped: pass --no-repo-scope", "the segments are still the scope's")

	// The segments still narrowed the request, so nothing back is still
	// worth comparing with the query as typed.
	segmentRecorder(t, answer(http.StatusOK, `{"state":"SUCCEEDED","result":{"records":[]}}`))
	code, _, stderr = runRepoScoped([]string{"query", timeseries, "--agent"}, RunOptions{})
	require.Zero(t, code, stderr)
	require.True(t, strings.HasSuffix(stderr, "\n0 records under repo scope checkout; compare with --no-repo-scope\n"), stderr)

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
	const failed = `repo scope checkout: filter segment Ab3dEf9hIjk could not be read (get segment: API error (400): 400 Bad Request); the query was not sent: retry, or `
	for _, tc := range []struct {
		args    []string
		asTyped string
	}{
		{nil, "pass --no-repo-scope"},
		{[]string{"--repo-scope", "checkout"}, "replace --repo-scope checkout with --no-repo-scope"},
	} {
		args, message := tc.args, failed+tc.asTyped+" to run it as typed"
		named := []string{"--repo-scope", "checkout"}
		srv := newDQLRecorder(t, "readonly", answer(http.StatusOK, oneRecord))
		srv.segments = map[string]int{"Ab3dEf9hIjk": http.StatusBadRequest, "plain-1": http.StatusOK}
		chdirTestRepo(t, segmentScope)

		code, stdout, stderr := runRepoScoped(append([]string{"query", repoScopeTypedQuery, "--agent"}, args...), RunOptions{})
		require.NotZero(t, code)
		require.Equal(t, message, agentError(t, stdout).Message)
		require.Empty(t, srv.queries(), "nothing is sent")
		require.NotContains(t, stderr, "applying repo scope")

		// wait query scopes under a name only, and has no opt-out to advise.
		code, stdout, _ = runRepoScoped(append([]string{"wait", "query", repoScopeTypedQuery, "--for=any", "--agent"}, named...), RunOptions{})
		require.NotZero(t, code)
		require.Equal(t, failed+"drop --repo-scope checkout to run it as typed", agentError(t, stdout).Message)
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

// A wait under a named entry one of whose segments is gone does not start:
// the caller asked for that entry's rows.
func TestWaitQuery_SegmentUnavailable(t *testing.T) {
	srv := newDQLRecorder(t, "readonly", answer(http.StatusOK, oneRecord))
	srv.segments = map[string]int{"plain-1": http.StatusOK}
	chdirTestRepo(t, segmentScope)

	code, stdout, _ := runRepoScoped([]string{"wait", "query", repoScopeTypedQuery, "--for=any", "--agent", "--repo-scope", "checkout"}, RunOptions{})
	require.NotZero(t, code)
	detail := agentError(t, stdout)
	require.Equal(t, "validation_error", detail.Code)
	require.Contains(t, detail.Message, `entry "checkout" cannot be applied: filter segment Ab3dEf9hIjk does not exist in this environment, or is not shared with you`)
	require.Contains(t, detail.Suggestions, "drop --repo-scope checkout to run the query as typed")
	require.Empty(t, srv.queries(), "no attempt is made")

	// Without the name the wait is not scoped, so the segment is not read.
	before := len(srv.requestLog())
	code, _, stderr := runRepoScoped([]string{"wait", "query", repoScopeTypedQuery, "--for=any", "--plain"}, RunOptions{})
	require.Zero(t, code, stderr)
	require.Equal(t, []string{"POST /platform/storage/query/v1/query:execute query=" + repoScopeTypedQuery}, srv.requestLog()[before:])
}

// Live mode refreshes for as long as it is open, and its one warning is on a
// screen the first redraw clears. Like a wait, it does not start when the
// scope was dropped over a segment that cannot be read.
func TestQueryLive_SegmentUnavailable(t *testing.T) {
	srv := newDQLRecorder(t, "readonly", answer(http.StatusOK, oneRecord))
	srv.segments = map[string]int{"plain-1": http.StatusOK}
	chdirTestRepo(t, segmentScope)

	code, _, stderr := runRepoScoped([]string{"query", repoScopeTypedQuery, "--live"}, RunOptions{})
	require.NotZero(t, code)
	require.Contains(t, stderr, "repo scope checkout cannot be applied: filter segment Ab3dEf9hIjk does not exist in this environment, or is not shared with you; "+
		"query --live does not refresh unscoped: correct the entry's segments in .dtctl-repo-scope.yaml, or pass --no-repo-scope")
	require.Empty(t, srv.queries(), "live mode does not start")
	require.NotContains(t, stderr, "the query runs unscoped")
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

	// The notice and the envelope report what the scope sent, not what the
	// entry lists: the overridden segment is the user's now, and it is not
	// read a second time for the entry.
	require.Contains(t, stderr, "applying repo scope checkout to fetch logs: "+checkoutLogsFilter+" with segments plain-1 (use --no-repo-scope to ignore it)")
	require.Equal(t, []output.RepoScopeSegment{{ID: "plain-1"}}, repoScopeContext(t, stdout).Segments)

	// Eleven named, ten sent: a segment both name counts once.
	fits := []string{"query", repoScopeTypedQuery, "--agent", "--segment", "plain-1"}
	for _, id := range []string{"u1", "u2", "u3", "u4", "u5", "u6", "u7", "u8"} {
		srv.segments[id] = http.StatusOK
		fits = append(fits, "--segment", id)
	}
	code, stdout, stderr = runRepoScoped(fits, RunOptions{})
	require.Zero(t, code, stderr+stdout)
	requests := srv.requests()
	body = requests[len(requests)-1]
	require.Contains(t, body, `{"id":"u8"},{"id":"Ab3dEf9hIjk","variables":[{"name":"environment","values":["production"]}]}]`)
	_, sentSegments, found := strings.Cut(body, `"filterSegments"`)
	require.True(t, found, body)
	require.Equal(t, 10, strings.Count(sentSegments, `"id":`))

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

// An entry that references only segments, every one of them given on the
// command line, sent nothing of its own: the scope reports that it was not
// applied, not that it was.
func TestQueryRepoScope_EverySegmentOverridden(t *testing.T) {
	const timeseries = "timeseries avg(dt.host.cpu.usage)"
	srv := segmentRecorder(t, answer(http.StatusOK, oneRecord))
	repo := chdirTestRepo(t, segmentScope)
	writeRepoFiles(t, repo, map[string]string{"tools/README.md": "tools\n"})
	chdirSubdir(t, "tools")

	code, stdout, stderr := runRepoScoped([]string{"query", timeseries, "--agent", "--segment", "plain-1"}, RunOptions{})
	require.Zero(t, code, stderr)
	body, _ := srv.executed(t)
	require.Contains(t, body, `"filterSegments":[{"id":"plain-1"}]`)
	const reason = "every segment it references is given on the command line, and those are sent"
	require.Equal(t, "repo scope tools: segment plain-1 is also given on the command line; its variables there are used\n"+
		"repo scope tools not applied: "+reason+" (use --no-repo-scope to silence this)\n", stderr)
	scope := repoScopeContext(t, stdout)
	require.Equal(t, []any{false, "user_segment", reason}, []any{scope.Applied, scope.Code, scope.Reason})
	require.Empty(t, scope.Segments)
}

// --segment-var binds a variable of a segment given with --segment. Aimed at
// one the entry references, it fails before the scope line and before any
// request, and says how to give the entry's segment its variables.
func TestQueryRepoScope_SegmentVarForAnEntrysSegment(t *testing.T) {
	srv := segmentRecorder(t, answer(http.StatusOK, oneRecord))
	chdirTestRepo(t, segmentScope)

	code, stdout, stderr := runRepoScoped([]string{"query", repoScopeTypedQuery, "--agent", "--segment-var", "Ab3dEf9hIjk:environment=staging"}, RunOptions{})
	require.NotZero(t, code)
	require.Equal(t, `--segment-var names segment "Ab3dEf9hIjk", which repo scope checkout references; to give it other variables, pass --segment "Ab3dEf9hIjk?environment=staging"`,
		agentError(t, stdout).Message)
	require.Empty(t, srv.requests(), "nothing is read or sent")
	require.Empty(t, stderr)

	// Still refused beside a --segment that names another segment.
	srv.segments["other-2"] = http.StatusOK
	code, _, stderr = runRepoScoped([]string{"query", repoScopeTypedQuery, "--segment", "other-2", "--segment-var", " Ab3dEf9hIjk : environment=staging"}, RunOptions{})
	require.NotZero(t, code)
	require.Contains(t, stderr, `--segment-var names segment "Ab3dEf9hIjk", which repo scope checkout references; to give it other variables, pass --segment "Ab3dEf9hIjk?environment=staging"`)
	require.Empty(t, srv.queries())
}

// --segment takes a name, and --segment-var the id the name resolves to:
// that works outside a repository, so it works under an entry that lists the
// same segment. The segment is then the user's, sent once with their value.
func TestQueryRepoScope_SegmentVarForASegmentGivenByName(t *testing.T) {
	srv := newDQLRecorder(t, "readonly", answer(http.StatusOK, oneRecord))
	srv.segments = map[string]int{"plain-1": http.StatusOK}
	repo := chdirTestRepo(t, segmentScope)
	writeRepoFiles(t, repo, map[string]string{"tools/README.md": "tools\n"})
	chdirSubdir(t, "tools")

	code, _, stderr := runRepoScoped([]string{"query", repoScopeTypedQuery, "--segment", "a segment", "--segment-var", "plain-1:host=a"}, RunOptions{})
	require.Zero(t, code, stderr)
	body, reads := srv.executed(t)
	require.Contains(t, body, `"filterSegments":[{"id":"plain-1","variables":[{"name":"host","values":["a"]}]}]`)
	require.Empty(t, reads, "the entry's copy is not read")
	require.Contains(t, stderr, "repo scope tools: segment plain-1 is also given on the command line; its variables there are used")
}

// wait query, under a named entry, sends its segments on every attempt and
// reads them once. verify query checks the text alone, and says the segments are not
// part of what it checked.
func TestWaitQueryAndVerifyQuery_EntrySegments(t *testing.T) {
	srv := segmentRecorder(t, answer(http.StatusOK, oneRecord))
	chdirTestRepo(t, segmentScope)

	code, _, stderr := runRepoScoped([]string{"wait", "query", repoScopeTypedQuery, "--for=any", "--repo-scope", "checkout"}, RunOptions{})
	require.Zero(t, code, stderr)
	body, reads := srv.executed(t)
	require.Contains(t, body, segmentsSent)
	require.Len(t, reads, 2)
	require.Contains(t, stderr, " with segments "+entrySegmentRefs+"; ")

	srv = segmentRecorder(t, answer(http.StatusOK, `{"valid":true}`))
	code, _, stderr = runRepoScoped([]string{"verify", "query", repoScopeTypedQuery}, RunOptions{})
	require.Zero(t, code, stderr)
	require.Equal(t, repoScopeScopedQuery, srv.first(t))
	for _, r := range srv.requests() {
		require.NotContains(t, r, "filter-segments", "verification reads no segment")
		require.NotContains(t, r, "filterSegments")
	}
	require.True(t, strings.HasPrefix(stderr, repoScopeApplying+"\n"+
		"repo scope checkout: its segments "+entrySegmentRefs+" are neither read nor sent: verify query checks the query text only; query sends them\n"), stderr)

	// An entry that is only segments has nothing verify query applies.
	repo := chdirTestRepo(t, segmentScope)
	writeRepoFiles(t, repo, map[string]string{"tools/README.md": "tools\n"})
	chdirSubdir(t, "tools")
	code, _, stderr = runRepoScoped([]string{"verify", "query", repoScopeTypedQuery}, RunOptions{})
	require.Zero(t, code, stderr)
	require.True(t, strings.HasPrefix(stderr, "repo scope tools: its segments plain-1 are neither read nor sent: verify query checks the query text only; query sends them\n"), stderr)
	require.NotContains(t, stderr, "applying repo scope")

	// Named, it is the same: verifying is not refused, and nothing says
	// that a scope was applied.
	code, _, stderr = runRepoScoped([]string{"verify", "query", repoScopeTypedQuery, "--repo-scope", "tools"}, RunOptions{})
	require.Zero(t, code, stderr)
	require.True(t, strings.HasPrefix(stderr, "repo scope tools: its segments plain-1 are neither read nor sent: verify query checks the query text only; query sends them\n"), stderr)
	require.NotContains(t, stderr, "applying repo scope")
}

// A scope whose only content the command dropped is not an applied one, and
// its code says why rather than "applied".
func TestRepoScopedQuery_SegmentsOnlyEntryWithoutSegments(t *testing.T) {
	q := &repoScopedQuery{
		scope:    &output.RepoScope{Name: "tools", Code: string(dql.Applied)},
		segments: []reposcope.Segment{{ID: "plain-1"}},
	}
	q.settle()
	require.True(t, q.scope.Applied, "control: with its segments the scope is applied")

	q.withoutSegments()
	require.False(t, q.scope.Applied)
	require.Equal(t, "segments_not_sent", q.scope.Code)
	require.Equal(t, "the entry references only filter segments, and verify query checks the query text only", q.scope.Reason)
	require.Empty(t, q.scope.Segments)
}

// A binding names its segment as it was typed or by the id that resolved to.
// A --segment whose typed name happens to be an id the entry lists, while
// resolving to another segment, is still the user's: the binding is theirs
// to make and is not refused as aimed at the entry's.
func TestRepoScopedQuery_SegmentVarNamesTheTypedSegment(t *testing.T) {
	c := &cobra.Command{}
	c.Flags().StringArray("segment-var", nil, "")
	require.NoError(t, c.Flags().Set("segment-var", "plain-1:environment=staging"))
	q := &repoScopedQuery{
		scope:    &output.RepoScope{Name: "tools"},
		segments: []reposcope.Segment{{ID: "plain-1"}},
	}

	user := []exec.FilterSegmentRef{{ID: "resolved-9"}}
	require.NoError(t, q.refuseSegmentVars(c, user, map[string]string{"resolved-9": "plain-1"}))
	require.NoError(t, q.refuseSegmentVars(c, []exec.FilterSegmentRef{{ID: "plain-1"}}, nil))
	require.ErrorContains(t, q.refuseSegmentVars(c, user, map[string]string{"resolved-9": "another name"}),
		`--segment-var names segment "plain-1", which repo scope tools references`)
}

// Whether a segment's variables are all bound is the platform's to say. When
// the segment is the entry's, the failure is the entry's: it names the entry
// and the file as the source, and both ways out, where the executor's own
// text would advise a -S the user never typed.
func TestQueryRepoScope_SegmentVariableErrorNamesTheEntry(t *testing.T) {
	const required = `segment Ab3dEf9hIjk requires variable "region", and repo scope checkout sends it without one: ` +
		`give the variable a value under the entry's segments in .dtctl-repo-scope.yaml, or pass --no-repo-scope to run the query as typed`
	const missing = `repo scope checkout sends the filter segments ` + entrySegmentRefs + `, and one of them needs a variable this query cannot supply ` +
		`(query failed (MISSING_FILTER_SEGMENT_VARIABLE): A variable is missing.): ` +
		`remove the segment from the entry in .dtctl-repo-scope.yaml, or pass --no-repo-scope to run the query as typed`
	for name, tc := range map[string]struct{ response, message string }{
		"a required variable is not bound": {
			`{"error":{"code":400,"message":"Segment requires a variable.","details":{"errorType":"FILTER_SEGMENT_REQUIRES_VARIABLE","arguments":["` + "`Ab3dEf9hIjk`" + `","x","$region"]}}}`,
			required},
		"the query cannot supply a variable": {
			`{"error":{"code":400,"message":"A variable is missing.","details":{"errorType":"MISSING_FILTER_SEGMENT_VARIABLE"}}}`,
			missing},
	} {
		t.Run(name, func(t *testing.T) {
			segmentRecorder(t, answer(http.StatusBadRequest, tc.response))
			chdirTestRepo(t, segmentScope)

			code, stdout, _ := runRepoScoped([]string{"query", repoScopeTypedQuery, "--agent"}, RunOptions{})
			require.NotZero(t, code)
			detail := agentError(t, stdout)
			require.Equal(t, "repo_scope_segment_variable", detail.Code)
			require.Equal(t, tc.message, detail.Message)
			require.Contains(t, strings.Join(detail.Suggestions, "\n"), "retry with --no-repo-scope to run the query exactly as typed")

			code, _, stderr := runRepoScoped([]string{"query", repoScopeTypedQuery, "--no-agent"}, RunOptions{})
			require.NotZero(t, code)
			require.Contains(t, stderr, tc.message)
			require.NotContains(t, stderr, "-S")
		})
	}

	// A segment the user gave is theirs to bind, and keeps the executor's text.
	srv := segmentRecorder(t, answer(http.StatusBadRequest,
		`{"error":{"code":400,"message":"Segment requires a variable.","details":{"errorType":"FILTER_SEGMENT_REQUIRES_VARIABLE","arguments":["`+"`other-2`"+`","x","$region"]}}}`))
	srv.segments["other-2"] = http.StatusOK
	chdirTestRepo(t, segmentScope)
	code, stdout, _ := runRepoScoped([]string{"query", repoScopeTypedQuery, "--agent", "--segment", "other-2"}, RunOptions{})
	require.NotZero(t, code)
	detail := agentError(t, stdout)
	require.Equal(t, "error", detail.Code)
	require.Contains(t, detail.Message, `-S "other-2?region=your-value-here"`)

	// A failure that names no segment, on a run that sent the entry's and the
	// user's, is not pinned on the entry: both sources are named.
	srv = segmentRecorder(t, answer(http.StatusBadRequest,
		`{"error":{"code":400,"message":"A variable is missing.","details":{"errorType":"MISSING_FILTER_SEGMENT_VARIABLE"}}}`))
	srv.segments["other-2"] = http.StatusOK
	chdirTestRepo(t, segmentScope)
	code, stdout, _ = runRepoScoped([]string{"query", repoScopeTypedQuery, "--agent", "--segment", "other-2"}, RunOptions{})
	require.NotZero(t, code)
	detail = agentError(t, stdout)
	require.Equal(t, "repo_scope_segment_variable", detail.Code)
	require.Equal(t, `one of the filter segments sent needs a variable this query cannot supply (query failed (MISSING_FILTER_SEGMENT_VARIABLE): A variable is missing.): `+
		`repo scope checkout sent `+entrySegmentRefs+` and the command line sent other-2; `+
		`remove the segment from the entry in .dtctl-repo-scope.yaml or from the command line, or pass --no-repo-scope to send only the command line's`, detail.Message)
}

// The empty-result diagnosis samples the data object to tell a mistyped field
// from a quiet service. Under a scope the sample is taken where the query
// ran: with the inserted filter and the segments sent. Without one, a field
// the scoped records carry could be reported missing from records the query
// could never have returned.
func TestQueryRepoScope_EmptyResultSampleStaysInScope(t *testing.T) {
	const typed = `fetch logs | filter loglevl == "ERROR"`
	const filter = `filter (service.name == \"checkout\" or (k8s.namespace.name == \"payments\" and k8s.workload.name == \"checkout\"))`
	const sentSegments = `"filterSegments":[{"id":"Ab3dEf9hIjk","variables":[{"name":"environment","values":["production"]}]},{"id":"plain-1"}]`
	srv := segmentRecorder(t, answer(http.StatusOK, `{"state":"SUCCEEDED","result":{"records":[]}}`))
	chdirTestRepo(t, segmentScope)

	code, _, stderr := runRepoScoped([]string{"query", typed, "--agent"}, RunOptions{})
	require.Zero(t, code, stderr)
	var posts []string
	for _, r := range srv.requests() {
		if strings.Contains(r, "/query:execute") {
			posts = append(posts, r)
		}
	}
	require.Len(t, posts, 2, "the query and its sample")
	for _, post := range posts {
		require.Contains(t, post, filter)
		require.Contains(t, post, sentSegments)
	}
	require.Contains(t, posts[1], filter+` | limit 100"`)

	// A block comment that holds a pipe does not move the sample out of the
	// scope: its head is cut where the scan placed the inserted stage.
	const commented = `fetch logs, from:now()-1h /* | filter x */ | filter loglevl == "ERROR"`
	srv = segmentRecorder(t, answer(http.StatusOK, `{"state":"SUCCEEDED","result":{"records":[]}}`))
	code, _, stderr = runRepoScoped([]string{"query", commented, "--agent"}, RunOptions{})
	require.Zero(t, code, stderr)
	posts = nil
	for _, r := range srv.requests() {
		if strings.Contains(r, "/query:execute") {
			posts = append(posts, r)
		}
	}
	require.Len(t, posts, 2, "the query and its sample")
	require.Contains(t, posts[1], `/* | filter x */ | `+filter+` | limit 100"`)
	require.Contains(t, posts[1], sentSegments)

	// A fetch stage with its own scan limit would carry it into the sample,
	// over the sample's 1 GB cap, so a scoped query takes none.
	srv = segmentRecorder(t, answer(http.StatusOK, `{"state":"SUCCEEDED","result":{"records":[]}}`))
	code, _, stderr = runRepoScoped([]string{"query", `fetch logs, scanLimitGBytes:-1 | filter loglevl == "ERROR"`, "--agent"}, RunOptions{})
	require.Zero(t, code, stderr)
	posts = nil
	for _, r := range srv.requests() {
		if strings.Contains(r, "/query:execute") {
			posts = append(posts, r)
		}
	}
	require.Len(t, posts, 1, "the query alone")

	// Unscoped, the sample is main's: the fetch stage alone, and no segment
	// the query was given.
	srv = segmentRecorder(t, answer(http.StatusOK, `{"state":"SUCCEEDED","result":{"records":[]}}`))
	code, _, stderr = runRepoScoped([]string{"query", typed, "--agent", "--no-repo-scope", "--segment", "plain-1"}, RunOptions{})
	require.Zero(t, code, stderr)
	log := srv.requestLog()
	require.Equal(t, "POST /platform/storage/query/v1/query:execute query=fetch logs | limit 100", log[len(log)-1])
}

// slowSegmentReads makes every segment read of srv hang until its request is
// abandoned, or for two seconds when nothing abandons it. started is closed
// by the first read.
func slowSegmentReads(srv *dqlRecorder) (started chan struct{}) {
	started = make(chan struct{})
	var once sync.Once
	srv.onSegmentRead = func(r *http.Request) {
		once.Do(func() { close(started) })
		select {
		case <-r.Context().Done():
		case <-time.After(2 * time.Second):
		}
	}
	return started
}

// The reads of the entry's segments belong to the invocation: Ctrl+C, or an
// embedding caller going away, ends them, and nothing is sent afterwards.
func TestQueryRepoScope_SegmentReadIsCancelled(t *testing.T) {
	srv := segmentRecorder(t, answer(http.StatusOK, oneRecord))
	started := slowSegmentReads(srv)
	chdirTestRepo(t, segmentScope)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		<-started
		cancel()
	}()
	begun := time.Now()
	code, _, stderr := runRepoScoped([]string{"query", repoScopeTypedQuery}, RunOptions{Context: ctx})
	require.NotZero(t, code)
	require.Less(t, time.Since(begun), time.Second, "the read outlived its cancellation")
	require.Empty(t, srv.queries(), "a cancelled invocation sends no query")
	require.Contains(t, stderr, "reading filter segment Ab3dEf9hIjk was interrupted")
	require.Contains(t, stderr, "the query was not sent")
}

// --timeout bounds the whole wait, the reads of the entry's segments
// included: a wait that cannot start in time ends as a timeout, exit code 1.
func TestWaitQuery_TimeoutCoversTheSegmentReads(t *testing.T) {
	srv := segmentRecorder(t, answer(http.StatusOK, oneRecord))
	slowSegmentReads(srv)
	chdirTestRepo(t, segmentScope)

	begun := time.Now()
	code, _, stderr := runRepoScoped([]string{"wait", "query", repoScopeTypedQuery, "--for=any", "--repo-scope", "checkout", "--timeout", "100ms"}, RunOptions{})
	require.Equal(t, 1, code, stderr)
	require.Less(t, time.Since(begun), time.Second)
	require.Empty(t, srv.queries(), "no attempt is made after the deadline")
	require.Contains(t, stderr, "Timeout reached")
	require.Contains(t, stderr, "reading filter segment Ab3dEf9hIjk was interrupted")

	code, _, stderr = runRepoScoped([]string{"wait", "query", repoScopeTypedQuery, "--for=any", "--repo-scope", "checkout", "--timeout", "100ms", "--quiet"}, RunOptions{})
	require.Equal(t, 1, code, stderr)
	require.NotContains(t, stderr, "Timeout reached")
}

// The entry's segments travel with every attempt of a wait, not only the
// first, and are read once however often it polls: an attempt without them
// would be judged on environment-wide rows.
func TestWaitQuery_SendsTheSegmentsOnEveryPoll(t *testing.T) {
	polls := 0
	srv := segmentRecorder(t, func(*http.Request, string) (int, string) {
		polls++
		if polls == 1 {
			return http.StatusOK, `{"state":"SUCCEEDED","result":{"records":[]}}`
		}
		return http.StatusOK, oneRecord
	})
	chdirTestRepo(t, segmentScope)

	code, _, stderr := runRepoScoped([]string{"wait", "query", repoScopeTypedQuery, "--for=any", "--repo-scope", "checkout",
		"--min-interval", "10ms", "--max-interval", "20ms"}, RunOptions{})
	require.Zero(t, code, stderr)

	var attempts, reads []string
	for _, r := range srv.requests() {
		switch {
		case strings.Contains(r, "/query:execute"):
			attempts = append(attempts, r)
		case strings.HasPrefix(r, segmentRequests):
			reads = append(reads, r)
		}
	}
	require.Len(t, attempts, 2)
	require.Len(t, reads, 2, "each of the two segments is read once")
	for _, body := range attempts {
		require.Contains(t, body, segmentsSent)
	}
	require.Equal(t, []string{repoScopeScopedQuery, repoScopeScopedQuery}, srv.queries())
}

// A wait under a scope ends on the first attempt the platform refuses for
// good, and says what the scope had to do with it: a segment of the entry
// that requires a variable is the entry's to correct, and any other refusal
// is shown with the text that was sent. Polling on would never succeed, and
// with --timeout 0 and no --max-attempts never end.
func TestWaitQuery_PermanentErrorUnderAScope(t *testing.T) {
	wait := func(args ...string) []string {
		return append([]string{"wait", "query", repoScopeTypedQuery, "--for=any", "--repo-scope", "checkout",
			"--timeout", "0", "--min-interval", "10ms", "--max-interval", "20ms"}, args...)
	}
	executes := func(srv *dqlRecorder) (n int) {
		for _, r := range srv.requests() {
			if strings.Contains(r, "/query:execute") {
				n++
			}
		}
		return n
	}

	srv := segmentRecorder(t, answer(http.StatusBadRequest,
		`{"error":{"code":400,"message":"Segment requires a variable.","details":{"errorType":"FILTER_SEGMENT_REQUIRES_VARIABLE","arguments":["`+"`Ab3dEf9hIjk`"+`","x","$region"]}}}`))
	chdirTestRepo(t, segmentScope)
	code, _, stderr := runRepoScoped(wait("--max-attempts", "3"), RunOptions{})
	require.Equal(t, 3, code, stderr)
	require.Equal(t, 1, executes(srv), "a refusal that cannot pass is not retried")
	require.Contains(t, stderr, `Query error (not retryable): segment Ab3dEf9hIjk requires variable "region", and repo scope checkout sends it without one`)
	require.NotContains(t, stderr, `-S "`, "the executor's advice is for a segment the user typed")
	require.NotContains(t, stderr, "condition not met", "a refused query is not an outcome of the condition")

	// No attempt limit and no timeout: before, this never returned.
	srv = segmentRecorder(t, answer(http.StatusBadRequest,
		`{"error":{"code":400,"message":"mismatched input","details":{"errorType":"PARSE_ERROR"}}}`))
	code, _, stderr = runRepoScoped(wait(), RunOptions{})
	require.Equal(t, 3, code, stderr)
	require.Equal(t, 1, executes(srv))
	require.Contains(t, stderr, "Query error (not retryable): ")
	require.Contains(t, stderr, "  the query ran under repo scope checkout, which inserted a filter after fetch logs; the text sent was: "+repoScopeScopedQuery+"\n")
	require.Contains(t, stderr, "  repo scope checkout also sent the filter segments "+entrySegmentRefs)

	// --quiet silences the wait's own lines, the error among them, as it
	// does without a scope.
	code, _, stderr = runRepoScoped(wait("--quiet"), RunOptions{})
	require.Equal(t, 3, code, stderr)
	require.NotContains(t, stderr, "Query error")
	require.NotContains(t, stderr, "the text sent was")
}

// An entry that references only segments still scopes: what a scoped query
// says about an empty result, a wait's outcome and a failure holds for it,
// though it inserted no filter.
func TestQueryRepoScope_SegmentsOnlyEntryIsAScope(t *testing.T) {
	const (
		timeseries = "timeseries avg(dt.host.cpu.usage)"
		noRecords  = `{"state":"SUCCEEDED","result":{"records":[]}}`
		applying   = "applying repo scope tools: segments plain-1 (use --no-repo-scope to ignore it)\n"
	)
	inTools := func(t *testing.T, respond func(*http.Request, string) (int, string)) {
		segmentRecorder(t, respond)
		repo := chdirTestRepo(t, segmentScope)
		writeRepoFiles(t, repo, map[string]string{"tools/README.md": "tools\n"})
		chdirSubdir(t, "tools")
	}

	inTools(t, answer(http.StatusOK, noRecords))
	code, stdout, stderr := runRepoScoped([]string{"query", timeseries, "--agent"}, RunOptions{})
	require.Zero(t, code, stderr)
	require.Equal(t, applying+"0 records under repo scope tools; compare with --no-repo-scope\n", stderr)
	require.Equal(t, "0 records under repo scope tools: run the same query with --no-repo-scope to tell whether the scope or the query filtered everything out",
		agentEnvelope(t, stdout).Context.Suggestions[0])

	poll := []string{"--max-attempts", "2", "--min-interval", "1ms", "--max-interval", "1ms"}
	poll = append(poll, "--repo-scope", "tools")
	code, _, stderr = runRepoScoped(append([]string{"wait", "query", timeseries, "--for=any"}, poll...), RunOptions{})
	require.Equal(t, 2, code, stderr)
	require.True(t, strings.HasSuffix(stderr, "condition not met under repo scope tools; compare without --repo-scope tools\n"), stderr)
	code, _, stderr = runRepoScoped(append([]string{"wait", "query", timeseries, "--for=none"}, poll...), RunOptions{})
	require.Zero(t, code, stderr)
	require.True(t, strings.HasSuffix(stderr, "0 records under repo scope tools; compare without --repo-scope tools\n"), stderr)

	inTools(t, answer(http.StatusBadRequest, `{"error":{"code":400,"message":"no","details":{"errorType":"FILTER_SEGMENT_NOT_FOUND"}}}`))
	code, stdout, _ = runRepoScoped([]string{"query", timeseries, "--agent"}, RunOptions{})
	require.NotZero(t, code)
	require.Equal(t, []string{
		"repo scope tools also sent the filter segments plain-1; if the failure names a segment or one of its variables, correct the entry's segments in .dtctl-repo-scope.yaml",
		"retry with --no-repo-scope to run the query exactly as typed",
	}, agentError(t, stdout).Suggestions)
}

// Off is off for the whole entry, by the environment as by the flag, and so
// is a stability floor that hides the flags: no segment is read, none is
// sent, the cluster filter is not inserted, and nothing is said.
func TestQueryRepoScope_OffLeavesSegmentsAndClusterBehind(t *testing.T) {
	const scope = `environments:
  127.0.0.1:
    - name: checkout
      workloads:
        - cluster-uid: 11111111-2222-3333-4444-555555555555
          namespace: payments
          name: checkout
      segments:
        - id: plain-1
`
	for name, tc := range map[string]struct {
		env   map[string]string
		floor bool
	}{
		"DTCTL_NO_REPO_SCOPE=1":        {env: map[string]string{noRepoScopeEnv: "1"}},
		"a floor that hides the flags": {floor: true},
	} {
		for _, argv := range [][]string{
			{"query", repoScopeTypedQuery, "--agent"},
			{"wait", "query", repoScopeTypedQuery, "--for=any", "--agent"},
		} {
			t.Run(name+"/"+argv[0], func(t *testing.T) {
				srv := segmentRecorder(t, answer(http.StatusOK, oneRecord))
				chdirTestRepo(t, scope)
				if tc.floor {
					writeIsolatedConfig(t, stableFloorConfig(srv.URL))
				}
				code, stdout, stderr := runRepoScoped(argv, RunOptions{Env: tc.env})
				require.Zero(t, code, "stdout: %s\nstderr: %s", stdout, stderr)
				body, reads := srv.executed(t)
				require.Equal(t, repoScopeTypedQuery, srv.first(t))
				require.Empty(t, reads, "no segment is read")
				require.NotContains(t, body, "filterSegments")
				require.NotContains(t, stderr, "repo scope")
				require.NotContains(t, stdout, "repo_scope")
			})
		}
	}

	// The same file with nothing turned off does all three.
	srv := segmentRecorder(t, answer(http.StatusOK, oneRecord))
	chdirTestRepo(t, scope)
	code, _, stderr := runRepoScoped([]string{"query", repoScopeTypedQuery, "--agent"}, RunOptions{})
	require.Zero(t, code, stderr)
	body, reads := srv.executed(t)
	require.Equal(t, []string{"plain-1"}, reads)
	require.Contains(t, body, `k8s.cluster.uid == \"11111111-2222-3333-4444-555555555555\"`)
	require.Contains(t, body, `"filterSegments":[{"id":"plain-1"}]`)
}
