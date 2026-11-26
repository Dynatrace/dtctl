package cmd

import (
	"net/http"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// queryBodyRe reads the two parts of a query:execute body the comparison is
// about: the text, and whether segments travel beside it.
var queryBodyRe = regexp.MustCompile(`"query":"((?:[^"\\]|\\.)*)"`)

// requestLog is every request the recorder got, in order, reduced to what a
// query may differ in: the method and URI, and for a query its text and its
// filter segments.
func (s *dqlRecorder) requestLog() []string {
	var log []string
	for _, r := range s.requests() {
		method, rest, _ := strings.Cut(r, " ")
		uri, body, _ := strings.Cut(rest, " ")
		line := method + " " + uri
		if m := queryBodyRe.FindStringSubmatch(body); m != nil {
			line += " query=" + m[1]
		}
		if i := strings.Index(body, `"filterSegments"`); i >= 0 {
			end := strings.Index(body[i:], "]")
			line += " " + body[i:i+end+1]
		}
		log = append(log, line)
	}
	return log
}

// A user who has not opted in pays nothing: with repo scoping off, `query`
// sends the requests main sends, in main's order, and fails where main fails.
//
// The expected sequences are main's, read from the RunE of newQueryCmd in
// cmd/query.go at origin/main 7099fe3d, which runs in this order:
//
//  1. dqlScopePrecheck, "before any request"
//  2. resolveQueryLimits
//  3. resolveMetadataFlag, then the --decode-snapshots value
//  4. the filter segments: parseSegmentFlags, one list request per --segment
//     to resolve it (resolver.ResolveID with TypeSegment lists for an id as
//     for a name), the segments file, --segment-var, the limit of ten
//  5. querySeriesOptions, resolveSpillOptions, resolveOutputBounds
//  6. the query itself
//
// So a flag checked in steps 1 to 3 fails with no request at all, one checked
// in step 5 fails after the list request of step 4, and nothing but that
// list request ever precedes the query. Each row runs in every situation
// that turns scoping off, and once more with scoping on as the control that
// the fixture can scope at all.
func TestQuery_ScopingOffSendsWhatMainSends(t *testing.T) {
	const (
		listSegments = "GET /platform/storage/filter-segments/v1/filter-segments?add-fields=VARIABLES"
		execute      = "POST /platform/storage/query/v1/query:execute query=" + repoScopeTypedQuery
		userSegment  = ` "filterSegments":[{"id":"seg-1"}]`
	)
	rows := []struct {
		name string
		args []string
		// granted is the token's scopes, when the row is about the precheck.
		granted  []string
		requests []string
		// stderr is a part of the error, "" for a run that succeeds.
		stderr string
	}{
		{name: "a plain query", requests: []string{execute}},
		{name: "a segment", args: []string{"--segment", "seg-1"},
			requests: []string{listSegments, execute + userSegment}},
		{name: "a segment that does not resolve", args: []string{"--segment", "no such"},
			requests: []string{listSegments}, stderr: `failed to resolve segment "no such"`},
		{name: "a token that cannot read logs", args: []string{"--segment", "seg-1"},
			granted: []string{"storage:spans:read"}, stderr: "fetch logs needs storage:logs:read"},
		{name: "an unknown metadata field", args: []string{"--segment", "seg-1", "--metadata=bogus"},
			stderr: "unknown metadata field(s): bogus"},
		{name: "an unknown decode mode", args: []string{"--segment", "seg-1", "--decode-snapshots=bogus"},
			stderr: `unsupported --decode-snapshots value "bogus"`},
		{name: "a segment variable without a segment", args: []string{"--segment-var", "seg-1:host=a"},
			stderr: "--segment-var requires at least one --segment or --segments-file"},
		{name: "an unknown series mode, checked after the segments", args: []string{"--segment", "seg-1", "--series", "bogus"},
			requests: []string{listSegments}, stderr: "bogus"},
	}
	situations := []struct {
		name      string
		scopeFile string
		linked    bool
		args      []string
		env       string
	}{
		{name: "outside a repository"},
		{name: "a repository without a scope file", linked: true},
		{name: "--no-repo-scope", linked: true, scopeFile: segmentScope, args: []string{"--no-repo-scope"}},
		{name: "DTCTL_NO_REPO_SCOPE", linked: true, scopeFile: segmentScope, env: "1"},
	}
	run := func(t *testing.T, linked bool, scopeFile, env string, granted []string, args ...string) (int, string, []string) {
		t.Helper()
		srv := newDQLRecorder(t, "readonly", answer(http.StatusOK, oneRecord))
		srv.segments = map[string]int{"seg-1": http.StatusOK, "Ab3dEf9hIjk": http.StatusOK, "plain-1": http.StatusOK}
		if linked {
			chdirTestRepo(t, scopeFile)
		} else {
			t.Chdir(t.TempDir())
		}
		t.Setenv(noRepoScopeEnv, env)
		if granted != nil {
			withGrantedScopes(t, granted, true)
		}
		code, _, stderr := runRepoScoped(append([]string{"query", repoScopeTypedQuery}, args...), RunOptions{})
		return code, stderr, srv.requestLog()
	}

	for _, row := range rows {
		for _, sit := range situations {
			t.Run(row.name+"/"+sit.name, func(t *testing.T) {
				code, stderr, requests := run(t, sit.linked, sit.scopeFile, sit.env, row.granted, append(sit.args, row.args...)...)
				require.Equal(t, row.requests, requests, "stderr: %s", stderr)
				require.NotContains(t, stderr, "repo scope")
				if row.stderr == "" {
					require.Zero(t, code, stderr)
					require.Empty(t, stderr)
					return
				}
				require.NotZero(t, code)
				require.Contains(t, stderr, row.stderr)
			})
		}
		// The control: the same line in the same repository, scoping on. A
		// flag refused before any request is refused before the scope reads
		// its segments or says anything, too.
		t.Run(row.name+"/scoped", func(t *testing.T) {
			code, stderr, requests := run(t, true, segmentScope, "", row.granted, row.args...)
			if row.stderr != "" {
				require.NotZero(t, code)
				require.Contains(t, stderr, row.stderr)
				require.Equal(t, row.requests, requests, "a refused command line sends what it sends unscoped")
				require.NotContains(t, stderr, "applying repo scope", "nothing was applied")
				return
			}
			require.Zero(t, code, stderr)
			require.Contains(t, stderr, "applying repo scope checkout")
			require.Contains(t, strings.Join(requests, "\n"), "plain-1", "the entry's segments are read and sent")
		})
	}
}
