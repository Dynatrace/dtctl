package cmd

import (
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// What dtctl advises under --repo-scope <name> has to work when it is
// followed. --repo-scope and --no-repo-scope exclude each other, and dropping
// the name applies whichever entry covers the directory, so the way to run
// the query as typed is to put --no-repo-scope in the name's place. Every
// piece of advice a named run gives says that, and the command line it
// describes is then run.
func TestQueryRepoScope_AdviceUnderANameCanBeFollowed(t *testing.T) {
	const (
		replace = "replace --repo-scope checkout with --no-repo-scope"
		inPlace = "with --no-repo-scope in place of --repo-scope checkout"
		refused = `{"error":{"code":400,"message":"The parameter 'x' is not defined.","details":{"errorType":"PARSE_ERROR"}}}`
		noRows  = `{"state":"SUCCEEDED","result":{"records":[]}}`
	)
	// scopedOnly answers a scoped text with status and body, and the typed
	// text with one record, so the advice leads somewhere.
	scopedOnly := func(status int, body string) func(*http.Request, string) (int, string) {
		return func(_ *http.Request, query string) (int, string) {
			if strings.Contains(query, "| filter (") {
				return status, body
			}
			return http.StatusOK, oneRecord
		}
	}
	for _, tc := range []struct {
		name     string
		query    string
		respond  func(*http.Request, string) (int, string)
		segments map[string]int
		// advice is where the run says how to get the query as typed.
		advice func(t *testing.T, code int, stdout, stderr string) string
		want   string
	}{
		{
			name: "an entry that cannot narrow the query", query: "timeseries avg(dt.host.cpu.usage)",
			respond: answer(http.StatusOK, oneRecord),
			advice: func(t *testing.T, code int, stdout, _ string) string {
				require.NotZero(t, code)
				return strings.Join(agentError(t, stdout).Suggestions, "\n")
			},
			want: replace + " to run the query as typed",
		},
		{
			name: "a segment of the entry that is gone", query: repoScopeTypedQuery,
			respond: answer(http.StatusOK, oneRecord), segments: map[string]int{"plain-1": http.StatusOK},
			advice: func(t *testing.T, code int, stdout, _ string) string {
				require.NotZero(t, code)
				return strings.Join(agentError(t, stdout).Suggestions, "\n")
			},
			want: replace + " to run the query as typed",
		},
		{
			name: "a segment read that fails", query: repoScopeTypedQuery,
			respond: answer(http.StatusOK, oneRecord), segments: map[string]int{"plain-1": http.StatusOK, "Ab3dEf9hIjk": http.StatusBadRequest},
			advice: func(t *testing.T, code int, stdout, _ string) string {
				require.NotZero(t, code)
				return agentError(t, stdout).Message
			},
			want: "retry, or " + replace + " to run it as typed",
		},
		{
			name: "a scoped query the platform refuses", query: repoScopeTypedQuery,
			respond: scopedOnly(http.StatusBadRequest, refused),
			advice: func(t *testing.T, code int, stdout, _ string) string {
				require.NotZero(t, code)
				return strings.Join(agentError(t, stdout).Suggestions, "\n")
			},
			want: "retry " + inPlace + " to run the query exactly as typed",
		},
		{
			name: "a scoped query that succeeds", query: repoScopeTypedQuery,
			respond: answer(http.StatusOK, oneRecord),
			advice: func(t *testing.T, code int, stdout, stderr string) string {
				require.Zero(t, code, stderr)
				require.NotContains(t, stderr, "to ignore it", "a scope the user named needs no hint that it can be ignored")
				return strings.Join(agentEnvelope(t, stdout).Context.Suggestions, "\n")
			},
			want: "run unscoped: " + replace,
		},
		{
			name: "no records, in the envelope", query: repoScopeTypedQuery,
			respond: scopedOnly(http.StatusOK, noRows),
			advice: func(t *testing.T, code int, stdout, stderr string) string {
				require.Zero(t, code, stderr)
				return strings.Join(agentEnvelope(t, stdout).Context.Suggestions, "\n")
			},
			want: "0 records under repo scope checkout: run the same query " + inPlace + " to tell whether the scope or the query filtered everything out",
		},
		{
			name: "no records, on stderr", query: repoScopeTypedQuery,
			respond: scopedOnly(http.StatusOK, noRows),
			advice: func(t *testing.T, code int, _, stderr string) string {
				require.Zero(t, code, stderr)
				return stderr
			},
			want: "0 records under repo scope checkout; compare " + inPlace + "\n",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := segmentRecorder(t, tc.respond)
			if tc.segments != nil {
				srv.segments = tc.segments
			}
			chdirTestRepo(t, segmentScope)

			named := []string{"query", tc.query, "--agent", "--repo-scope", "checkout"}
			code, stdout, stderr := runRepoScoped(named, RunOptions{})
			require.Contains(t, tc.advice(t, code, stdout, stderr), tc.want)

			// Followed: --no-repo-scope where the name was.
			at := slices.Index(named, "--repo-scope")
			followed := slices.Replace(slices.Clone(named), at, at+2, "--no-repo-scope")
			code, stdout, stderr = runRepoScoped(followed, RunOptions{})
			require.Zero(t, code, "stdout: %s\nstderr: %s", stdout, stderr)
			require.Equal(t, tc.query, srv.queries()[len(srv.queries())-1], "the advice runs the query as typed")

			// Adding the opt-out beside the name instead is refused.
			sent := len(srv.queries())
			code, stdout, _ = runRepoScoped(append(slices.Clone(named), "--no-repo-scope"), RunOptions{})
			require.NotZero(t, code)
			require.Contains(t, agentError(t, stdout).Message, "[no-repo-scope repo-scope] were all set")
			require.Len(t, srv.queries(), sent)
		})
	}
}
