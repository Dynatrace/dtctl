package cmd

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/Dynatrace/dtctl/pkg/output"
	"github.com/Dynatrace/dtctl/pkg/reposcope"
)

// More reports discovery can produce, and what their candidates get: a line
// binds only what was verified to select the candidate and nothing else, and
// a candidate left without one is told why.
func TestRepoScopeSetLineBindsNoMoreThanWasRead(t *testing.T) {
	const (
		uid = "11111111-2222-3333-4444-555555555555"
		svc = "SERVICE-0000000000000001"
		pg  = "PROCESS_GROUP-0000000000000001"
	)
	sampled := []reposcope.Query{{DataObject: "spans", SamplingRatio: 100000, Rows: 1}}
	for _, tc := range []struct {
		name   string
		report reposcope.DiscoveryReport
		want   []string
		notes  []string
	}{
		{
			// Whatever made Grail return the row, no file explains it.
			name: "a row neither of whose names was sent",
			report: reposcope.DiscoveryReport{Sent: []string{"checkout"}, Candidates: []reposcope.Candidate{
				{Rank: 1, Name: "gateway", ClusterUID: uid, Namespace: "infra", Workload: "gateway", ServiceName: "edge", Tier: "substring", Spans: 10},
			}},
			want: []string{""},
			notes: []string{"candidate 1 has no set line: neither its k8s.workload.name nor its service.name is a name discovery sent, so nothing ties it to this code; " +
				"if it does, run discover again with --term gateway"},
		},
		{
			// Three rows: the workload in a cluster, the same without one,
			// and without one under another service name. The first two are
			// one candidate, and dropping its cluster would select the third.
			name: "a workload both shared and partly without a cluster keeps the cluster",
			report: reposcope.DiscoveryReport{Sent: []string{"checkout"}, Candidates: []reposcope.Candidate{
				{Rank: 1, Name: "checkout", ClusterUID: uid, Namespace: "infra", Workload: "checkout", ServiceName: "checkout", Tier: "exact", Spans: 10,
					WorkloadShared: true, WithoutCluster: []string{"spans"}},
				{Rank: 2, Name: "checkout", Namespace: "infra", Workload: "checkout", ServiceName: "metrics", Tier: "exact", Spans: 10, WorkloadShared: true},
			}},
			want: []string{"dtctl repo-scope set checkout --cluster-uid " + uid + " --namespace infra --workload checkout", ""},
			notes: []string{
				"candidate 1's line keeps --cluster-uid although its spans carry no k8s.cluster.uid, so the entry returns none of them: another candidate runs infra/checkout, and a binding without the cluster would select both",
				"candidate 2 has no set line: its records name no cluster, and another candidate runs the same namespace and workload in one, so a binding without --cluster-uid would select both; to bind the workload in every cluster, leave --cluster-uid out of that candidate's line",
			},
		},
		{
			// The candidate was read whole, the spans beside it by a
			// sample: one sharing its ids could sit below the floor.
			name: "a candidate read whole on a sampled report is not bound by id",
			report: reposcope.DiscoveryReport{Sent: []string{"checkout"}, Queries: sampled, Candidates: []reposcope.Candidate{
				{Rank: 1, Name: "checkout", ServiceName: "checkout", Tier: "exact", Logs: 10, ProcessGroups: []string{pg}, Services: []string{svc}},
			}},
			want:  []string{""},
			notes: []string{"candidate 1 has no set line: a record query was sampled, which can miss another candidate sharing its service name or ids, so only --namespace with --workload is proposed, and no workload binding selects it"},
		},
		{
			name: "nor is one that has no ids left without a word",
			report: reposcope.DiscoveryReport{Sent: []string{"checkout"}, Queries: sampled, Candidates: []reposcope.Candidate{
				{Rank: 1, Name: "checkout", ServiceName: "checkout", Tier: "exact", Logs: 10},
			}},
			want:  []string{""},
			notes: []string{"candidate 1 has no set line: a record query was sampled, which can miss another candidate sharing its service name or ids, so only --namespace with --workload is proposed, and no workload binding selects it"},
		},
		{
			name: "while its workload still is",
			report: reposcope.DiscoveryReport{Sent: []string{"checkout"}, Queries: sampled, Candidates: []reposcope.Candidate{
				{Rank: 1, Name: "checkout", Namespace: "payments", Workload: "checkout", ServiceName: "checkout", Tier: "exact", Logs: 10, ProcessGroups: []string{pg}},
			}},
			want: []string{"dtctl repo-scope set checkout --namespace payments --workload checkout"},
		},
		{
			// The other candidate lists 50 of its process groups; this
			// one's could be the 51st.
			name: "an id is not exclusive beside a candidate that lists only some of its own",
			report: reposcope.DiscoveryReport{Sent: []string{"checkout", "ledger"}, Candidates: []reposcope.Candidate{
				{Rank: 1, Name: "gateway", ServiceName: "gateway", Tier: "exact", Logs: 10, ProcessGroups: []string{pg}},
				{Rank: 2, Name: "ledger", Namespace: "payments", Workload: "ledger", Tier: "exact", Logs: 10, ProcessGroupsCapped: true,
					ProcessGroups: []string{"PROCESS_GROUP-0000000000000002"}},
			}},
			want:  []string{"", "dtctl repo-scope set ledger --namespace payments --workload ledger"},
			notes: []string{"candidate 1 has no set line: candidate 2 carries more entity ids than discovery lists, and one of them could be this candidate's, so a binding by id could select both"},
		},
		{
			name: "and the process group a line adds for the logs is left out beside one",
			report: reposcope.DiscoveryReport{Sent: []string{"checkout", "ledger"}, Candidates: []reposcope.Candidate{
				{Rank: 1, Name: "checkout", Namespace: "payments", Workload: "checkout", Tier: "exact", Spans: 10, ProcessGroups: []string{pg}, LogsByProcessGroup: 77},
				{Rank: 2, Name: "ledger", Namespace: "payments", Workload: "ledger", Tier: "exact", Logs: 10, ProcessGroupsCapped: true},
			}},
			want: []string{"dtctl repo-scope set checkout --namespace payments --workload checkout", "dtctl repo-scope set ledger --namespace payments --workload ledger"},
			notes: []string{"candidate 1's line leaves --process-group out, and with it the 77 logs found by process group: " +
				"candidate 2 carries more entity ids than discovery lists, and one of them could be this candidate's, so a binding by id could select both"},
		},
		{
			name: "a service id is not weighed against a cut list of process groups",
			report: reposcope.DiscoveryReport{Sent: []string{"checkout", "ledger"}, Candidates: []reposcope.Candidate{
				{Rank: 1, Name: "gateway", ServiceName: "gateway", Tier: "exact", Spans: 10, Services: []string{svc}},
				{Rank: 2, Name: "ledger", Namespace: "payments", Workload: "ledger", Tier: "exact", Logs: 10, ProcessGroupsCapped: true},
			}},
			want: []string{"dtctl repo-scope set gateway --service " + svc, "dtctl repo-scope set ledger --namespace payments --workload ledger"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := tc.report
			r.Unit, r.Dir = ".", "."
			var lines []string
			for i, c := range r.Candidates {
				r.Candidates[i].SetCommand = repoScopeSetCommand(&r, c, nil)
				lines = append(lines, r.Candidates[i].SetCommand)
			}
			require.Equal(t, tc.want, lines)
			require.Equal(t, tc.notes, repoScopeNoLineNotes(&r))
		})
	}
}

// One candidate, whose names nothing explains: the verdict line says that,
// not that a part of its name matched.
func TestRepoScopeDoubtOfAnUnexplainedRow(t *testing.T) {
	r := &reposcope.DiscoveryReport{Candidates: []reposcope.Candidate{{Rank: 1, Workload: "gateway", ServiceName: "edge", Tier: "substring"}}}
	require.Equal(t, ": one candidate, and neither its workload nor its service name is a name discovery sent", repoScopeDoubt(r))
	r.Candidates[0] = reposcope.Candidate{Rank: 1, Tier: "substring", Services: []string{"SERVICE-0000000000000001"}}
	require.Equal(t, ": one candidate, matched by a substring of its name rather than the whole of it", repoScopeDoubt(r), "an entity found by its name")
}

// A report is one value per unit, so only the formats that print one are
// taken, and the others are refused before anything is sent: the printer
// answers a format it does not know with a table of its own.
func TestRepoScopeDiscoverRefusesFormatsThatPrintRows(t *testing.T) {
	srv := newDQLRecorder(t, "readonly", rowsByObject(checkoutRows))
	chdirCheckoutRepo(t)
	for _, format := range []string{"csv", "jsonl", "chart", "parquet", "jsno"} {
		code, stdout, stderr := runRepoScoped([]string{"repo-scope", "discover", "--agent", "-o", format}, RunOptions{})
		require.NotZero(t, code, format)
		detail := agentError(t, stdout)
		require.Equal(t, "validation_error", detail.Code, format)
		require.Equal(t, "-o "+format+": a discovery report is not rows; use -o table, -o json, -o yaml or -o toon", detail.Message)
		require.NotContains(t, stderr, "Sending to")
	}
	require.Empty(t, srv.queries(), "refused before the first query")
	for _, format := range []string{"table", "wide", "json", "yaml", "yml", "toon"} {
		code, _, stderr := runRepoScoped([]string{"repo-scope", "discover", "--plain", "-o", format}, RunOptions{})
		require.Zero(t, code, "%s: %s", format, stderr)
	}
}

// An interrupted run is an error. Where the error is an envelope on stdout,
// in agent and in plain mode, it is the only document there: the reports of
// the units that had run are not printed in front of it. Without either mode
// the error goes to stderr, and stdout holds the reports alone.
func TestRepoScopeDiscoverInterruptedPrintsOneDocument(t *testing.T) {
	for _, tc := range []struct {
		name     string
		args     []string
		envelope bool
	}{
		{"--agent", []string{"--agent"}, true},
		{"--plain -o json", []string{"--plain", "-o", "json"}, true},
		{"-o json", []string{"--no-agent", "-o", "json"}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			newDQLRecorder(t, "readonly", func(r *http.Request, query string) (int, string) {
				cancel() // while the first query runs
				select {
				case <-r.Context().Done():
				case <-time.After(5 * time.Second):
				}
				return rowsByObject(checkoutRows)(r, query)
			})
			chdirCheckoutRepo(t)
			code, stdout, stderr := runRepoScoped(append([]string{"repo-scope", "discover"}, tc.args...), RunOptions{Context: ctx})
			require.NotZero(t, code, "stdout: %s\nstderr: %s", stdout, stderr)

			dec := json.NewDecoder(strings.NewReader(stdout))
			var first json.RawMessage
			require.NoError(t, dec.Decode(&first), "stdout: %s", stdout)
			require.False(t, dec.More(), "one document, not two: %s", stdout)
			if tc.envelope {
				var resp output.Response
				require.NoError(t, json.Unmarshal(first, &resp))
				require.False(t, resp.OK)
				require.NotNil(t, resp.Error)
				return
			}
			var reports []reposcope.DiscoveryReport
			require.NoError(t, json.Unmarshal(first, &reports), stdout)
			require.Len(t, reports, 1)
			require.True(t, reports[0].Partial)
		})
	}
}

// The unit's own time bound ends a unit and leaves a partial report; a
// deadline the caller set ends the run, and the caller hears of it. The query
// the bound stopped says it ran out of time, not that it was cancelled.
func TestRepoScopeDiscoverDeadlines(t *testing.T) {
	slow := func(r *http.Request, _ string) (int, string) {
		select {
		case <-r.Context().Done():
		case <-time.After(5 * time.Second):
		}
		return http.StatusOK, `{"state":"SUCCEEDED","result":{"records":[]}}`
	}
	newDQLRecorder(t, "readonly", slow)
	chdirCheckoutRepo(t)

	code, stdout, stderr := runRepoScoped([]string{"repo-scope", "discover", "--agent"}, RunOptions{
		Context: context.WithValue(context.Background(), repoScopeDiscoverTimeoutKey{}, 200*time.Millisecond),
	})
	require.Zero(t, code, "stdout: %s\nstderr: %s", stdout, stderr)
	var reports []reposcope.DiscoveryReport
	agentResult(t, stdout, &reports)
	require.Equal(t, "timeout", reports[0].Queries[0].Cause)
	require.Equal(t, "context deadline exceeded", reports[0].Queries[0].Error)

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	code, stdout, _ = runRepoScoped([]string{"repo-scope", "discover", "--agent"}, RunOptions{Context: ctx})
	require.NotZero(t, code, "the caller's deadline is the caller's error: %s", stdout)
	require.False(t, agentEnvelope(t, stdout).OK)
}

// A directory several deployables share is named in the refusal of a term,
// and a directory's name is whatever the clone carries: it is printed as a
// terminal shows it, never with an escape sequence in it.
func TestRepoScopeDiscoverTermRefusalShowsNoControlCharacter(t *testing.T) {
	if _, err := os.Stat(gradleMultiRepo(t)); err != nil {
		t.Skip(err)
	}
	repo := chdirTestRepo(t, "")
	writeRepoFiles(t, repo, map[string]string{
		"settings.gradle":             "include ':app\x1b[2J', ':web', ':lib'\n",
		"app\x1b[2J/build.gradle":     "dependencies { implementation project(':lib') }\n",
		"app\x1b[2J/Dockerfile":       "FROM scratch\n",
		"web/build.gradle":            "dependencies { implementation project(':lib') }\n",
		"web/Dockerfile":              "FROM scratch\n",
		"lib/build.gradle":            "plugins { id 'java-library' }\n",
		"lib/src/main/java/Lib.java":  "class Lib {}\n",
		"app\x1b[2J/src/App.java":     "class App {}\n",
		"web/src/main/java/Web.java":  "class Web {}\n",
		"lib/src/main/java/Lib2.java": "class Lib2 {}\n",
	})
	t.Chdir(filepath.Join(repo, "lib"))
	code, stdout, stderr := runRepoScoped([]string{"repo-scope", "discover", "--dry-run", "--term", "billing-engine"}, RunOptions{})
	require.NotZero(t, code)
	require.Contains(t, stdout+stderr, "this directory is discovered for each of: ")
	require.NotContains(t, stdout+stderr, "\x1b")
	require.Contains(t, stdout+stderr, "app\uFFFD[2J")
}
