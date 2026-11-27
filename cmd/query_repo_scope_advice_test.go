package cmd

import (
	"encoding/json"
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

// A repair dtctl offers for a scoped query the platform refused is a command
// line, and running it must ask for what the failed run asked for: the typed
// text repaired, under the same entry. Built from the text that was sent, it
// carried the inserted filter as if the user had written it, and dropped the
// name, so the replay ran under whichever entry covers the directory.
func TestQueryRepoScope_RepairKeepsTheScope(t *testing.T) {
	const typed = "fetch logs | head 1"
	unknownHead := func(_ *http.Request, query string) (int, string) {
		if !strings.Contains(query, "| head 1") {
			return http.StatusOK, oneRecord
		}
		// Grail reports the position in the text it was sent.
		line, column := 1, strings.Index(query, "head")+1
		if at := strings.LastIndex(query, "\n"); at >= 0 {
			line, column = 2, strings.Index(query[at+1:], "head")+1
		}
		body, err := json.Marshal(map[string]any{"error": map[string]any{"code": 400, "message": "'head' isn't a valid command.", "details": map[string]any{
			"errorType": "UNKNOWN_COMMAND", "arguments": []string{"head"}, "queryString": query,
			"syntaxErrorPosition": map[string]any{
				"start": map[string]int{"line": line, "column": column},
				"end":   map[string]int{"line": line, "column": column + 3},
			},
		}}})
		require.NoError(t, err)
		return http.StatusBadRequest, string(body)
	}
	firstCommand := func(t *testing.T, stdout string) []string {
		t.Helper()
		suggestions := agentError(t, stdout).Suggestions
		require.NotEmpty(t, suggestions)
		_, command, found := strings.Cut(suggestions[0], ": dtctl ")
		require.True(t, found, suggestions[0])
		query, flags, found := strings.Cut(strings.TrimPrefix(command, "query '"), "'")
		require.True(t, found, command)
		return append([]string{"query", query}, strings.Fields(flags)...)
	}

	t.Run("named", func(t *testing.T) {
		srv := segmentRecorder(t, unknownHead)
		repo := chdirTestRepo(t, segmentScope)
		writeRepoFiles(t, repo, map[string]string{"tools/README.md": "tools\n"})
		chdirSubdir(t, "tools")

		code, stdout, _ := runRepoScoped([]string{"query", typed, "--agent", "--repo-scope", "checkout"}, RunOptions{})
		require.NotZero(t, code)
		failed, _ := srv.executed(t)
		require.Contains(t, failed, segmentsSent)

		repair := firstCommand(t, stdout)
		require.Equal(t, []string{"query", "fetch logs | limit 1", "--repo-scope", "checkout"}, repair)
		srv.reset()
		code, stdout, stderr := runRepoScoped(append(repair, "--agent"), RunOptions{})
		require.Zero(t, code, stderr)
		replayed := srv.requests()[len(srv.requests())-1]
		require.Equal(t, strings.Replace(failed, "| head 1", "| limit 1", 1), replayed,
			"the repair asks for what the failed run asked for, repaired")
		require.Equal(t, "checkout", repoScopeContext(t, stdout).Name)
	})

	t.Run("unnamed", func(t *testing.T) {
		srv := segmentRecorder(t, unknownHead)
		chdirTestRepo(t, segmentScope)

		code, stdout, _ := runRepoScoped([]string{"query", typed, "--agent"}, RunOptions{})
		require.NotZero(t, code)
		failed, _ := srv.executed(t)
		repair := firstCommand(t, stdout)
		require.Equal(t, []string{"query", "fetch logs | limit 1"}, repair)
		srv.reset()
		code, _, stderr := runRepoScoped(append(repair, "--agent"), RunOptions{})
		require.Zero(t, code, stderr)
		require.Equal(t, strings.Replace(failed, "| head 1", "| limit 1", 1), srv.requests()[len(srv.requests())-1])
	})

	t.Run("an entry that sends only segments", func(t *testing.T) {
		srv := segmentRecorder(t, unknownHead)
		chdirTestRepo(t, segmentScope)

		code, stdout, _ := runRepoScoped([]string{"query", typed, "--agent", "--repo-scope", "tools"}, RunOptions{})
		require.NotZero(t, code)
		require.Equal(t, []string{"query", "fetch logs | limit 1", "--repo-scope", "tools"}, firstCommand(t, stdout))
		require.Len(t, srv.queries(), 1)
	})

	t.Run("without a scope the repair is main's", func(t *testing.T) {
		segmentRecorder(t, unknownHead)
		chdirTestRepo(t, segmentScope)

		code, stdout, _ := runRepoScoped([]string{"query", typed, "--agent", "--no-repo-scope"}, RunOptions{})
		require.NotZero(t, code)
		require.Equal(t, "DQL spells these commands limit, filter, fields, fieldsAdd and summarize count(), by:{…}: dtctl query 'fetch logs | limit 1'",
			agentError(t, stdout).Suggestions[0])
	})
}
