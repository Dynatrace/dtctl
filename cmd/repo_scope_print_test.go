package cmd

import (
	"bytes"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Dynatrace/dtctl/pkg/output"
	"github.com/Dynatrace/dtctl/pkg/reposcope"
)

func TestRepoScopeDiscoveryLimitsProposedLines(t *testing.T) {
	report := &reposcope.DiscoveryReport{Unit: "services/checkout", Dir: "services/checkout", Verdict: reposcope.VerdictAmbiguous}
	for i := 1; i <= 7; i++ {
		report.Sent = append(report.Sent, fmt.Sprintf("checkout-%d", i))
		c := reposcope.Candidate{Rank: i, Name: fmt.Sprintf("checkout-%d", i), ServiceName: fmt.Sprintf("checkout-%d", i), Tier: "exact"}
		c.SetCommand = repoScopeSetCommand(report, c, nil)
		report.Candidates = append(report.Candidates, c)
	}
	var out bytes.Buffer
	printRepoScopeDiscovery(&out, report, newRepoScopeProposal(report, nil), 0)
	require.Contains(t, out.String(), "(+2 more in -o json)")
	require.Contains(t, out.String(), "--path services/checkout")
	for i, c := range report.Candidates {
		require.Contains(t, out.String(), c.ServiceName, "all candidates stay in the table")
		require.Equal(t, i < repoScopeShownCandidates, strings.Contains(out.String(), c.SetCommand))
	}
}

func TestRepoScopeDiscoveryAmbiguousWithoutBindings(t *testing.T) {
	report := &reposcope.DiscoveryReport{Unit: ".", Dir: "internal/checkout", Verdict: reposcope.VerdictAmbiguous,
		Candidates: []reposcope.Candidate{
			{Rank: 1, Name: "checkout", Workload: "checkout", Tier: "exact"},
			{Rank: 2, Name: "ledger", Workload: "ledger", Tier: "exact"},
		},
	}
	proposal := newRepoScopeProposal(report, nil)
	require.Len(t, proposal.handLinks, 3)
	require.Empty(t, proposal.selectAll)
	var out bytes.Buffer
	printRepoScopeDiscovery(&out, report, proposal, 0)
	require.Contains(t, out.String(), "No binding selects any candidate alone.")
	require.Contains(t, out.String(), "add --path internal/checkout")
	suggestions := repoScopeDiscoverySuggestions(report, nil)
	require.Contains(t, strings.Join(suggestions, "\n"), "link by hand:")
}

func TestRepoScopeDiscoveryExplainsIncompleteNoMatch(t *testing.T) {
	report := &reposcope.DiscoveryReport{Unit: ".", Dir: ".", Verdict: reposcope.VerdictNone, Partial: true,
		Queries: []reposcope.Query{
			{Purpose: "not run", DQL: "fetch spans", Skipped: true},
			{Purpose: "not authorized", DQL: "fetch logs", Error: "denied", Cause: "auth"},
			{Purpose: "capped", DQL: "fetch dt.entity.service", Rows: 200, Truncated: true},
		},
	}
	var out bytes.Buffer
	printRepoScopeDiscovery(&out, report, newRepoScopeProposal(report, nil), 0)
	require.Contains(t, out.String(), "skipped")
	require.Contains(t, out.String(), "failed (auth)")
	require.Contains(t, out.String(), "200 matched, capped")
	require.Contains(t, out.String(), "2 of 3 queries ran")
}

// An id binding saves only the ids discovery listed, so it is proposed only
// when they are all of the candidate's ids and not a sample's.
func TestRepoScopeSetCommandBindsOnlyWholeIDLists(t *testing.T) {
	root := &reposcope.DiscoveryReport{Unit: ".", Dir: "."}
	shared := reposcope.Candidate{Name: "checkout", ServiceName: "checkout", ServiceNameShared: true,
		Services: []string{"SERVICE-0000000000000001"}, ServiceCount: 1}
	require.Equal(t, "dtctl repo-scope set checkout --service SERVICE-0000000000000001", repoScopeSetCommand(root, shared, nil))

	capped := shared
	capped.ServiceCount, capped.ServicesCapped = 200, true
	require.Empty(t, repoScopeSetCommand(root, capped, nil))
	cutArrays := reposcope.Candidate{Name: "checkout", ProcessGroups: []string{"PROCESS_GROUP-0000000000000001"},
		ProcessGroupCount: 120, ProcessGroupsCapped: true}
	require.Empty(t, repoScopeSetCommand(root, cutArrays, nil), "a cut process-group list gets no id line")

	sampled := shared
	sampled.Sampled = true
	require.Empty(t, repoScopeSetCommand(root, sampled, nil))
	sampled.ServiceNameShared = false
	require.Empty(t, repoScopeSetCommand(root, sampled, nil), "nor a service name a sample cannot tell is shared")
	sampled.Namespace, sampled.Workload = "payments", "checkout"
	require.Equal(t, "dtctl repo-scope set checkout --namespace payments --workload checkout", repoScopeSetCommand(root, sampled, nil))
}

// A candidate read whole still gets no service-name binding when another data
// object was sampled: a candidate sharing its service name could sit below
// that sample's floor.
func TestRepoScopeSetCommandHoldsBackTheServiceNameWhenAnyObjectWasSampled(t *testing.T) {
	c := reposcope.Candidate{Name: "checkout", Namespace: "payments", Workload: "checkout", ServiceName: "checkout", Logs: 88000}
	whole := &reposcope.DiscoveryReport{Unit: ".", Dir: ".", Sent: []string{"checkout"}, Queries: []reposcope.Query{
		{DataObject: "spans", Rows: 0},
		{DataObject: "logs", Rows: 1},
	}}
	require.Equal(t, "dtctl repo-scope set checkout --namespace payments --workload checkout --service-name checkout",
		repoScopeSetCommand(whole, c, nil))

	sampled := &reposcope.DiscoveryReport{Unit: ".", Dir: ".", Sent: []string{"checkout"}, Queries: []reposcope.Query{
		{DataObject: "spans", Truncated: true, Cause: "truncated"},
		{DataObject: "spans", Window: "1h", SamplingRatio: 100000},
		{DataObject: "logs", Rows: 1},
	}}
	require.Equal(t, "dtctl repo-scope set checkout --namespace payments --workload checkout", repoScopeSetCommand(sampled, c, nil),
		"the spans sample matched nothing, and a spans-only checkout below its floor would share the name")

	refused := &reposcope.DiscoveryReport{Unit: ".", Dir: ".", Sent: []string{"checkout"}, Queries: []reposcope.Query{
		{DataObject: "spans", Window: "1h", SamplingRatio: 100000, Cause: "sampling_refused"},
	}}
	require.Contains(t, repoScopeSetCommand(refused, c, nil), "--service-name checkout", "a rung Grail did not sample read whole")
}

func TestRepoScopeIDsColumn(t *testing.T) {
	for _, tc := range []struct {
		ids     []string
		count   int64
		sampled bool
		want    string
	}{
		{nil, 0, false, "-"},
		{[]string{"A"}, 1, false, "A"},
		{[]string{"A", "B"}, 2, false, "A,B"},
		{[]string{"A", "B", "C"}, 3, false, "A,B +1 more"},
		{[]string{"A", "B"}, 200, false, "A,B +198 more"},
		{nil, 3, false, "+3 more"},
		{[]string{"A"}, 4, true, "A +3 more (sampled)"},
	} {
		require.Equal(t, tc.want, idsColumn(tc.ids, tc.count, tc.sampled), "%v %d %v", tc.ids, tc.count, tc.sampled)
	}
}

// One candidate can still be ambiguous: its name matched only in part, or a
// query that could have found a second one did not answer. The verdict says
// which, instead of counting a single candidate as if that were the doubt.
func TestRepoScopeDiscoveryOneAmbiguousCandidate(t *testing.T) {
	for _, tc := range []struct {
		name    string
		tier    string
		partial bool
		verdict string
	}{
		{"a partial name match", "segment", false, "Verdict: ambiguous: one candidate, matched by a segment of its name rather than the whole of it\n"},
		{"a partial result", "exact", true, "Verdict: ambiguous: one candidate, but the result is partial (see the notes)\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			report := &reposcope.DiscoveryReport{Unit: ".", Dir: ".", Verdict: reposcope.VerdictAmbiguous, Partial: tc.partial,
				Candidates: []reposcope.Candidate{{Rank: 1, Name: "checkout", Services: []string{flowService}, ServiceCount: 1, Tier: tc.tier}}}
			report.Candidates[0].SetCommand = repoScopeSetCommand(report, report.Candidates[0], nil)
			var out bytes.Buffer
			printRepoScopeDiscovery(&out, report, newRepoScopeProposal(report, nil), 0)
			require.Contains(t, out.String(), tc.verdict)
			require.Contains(t, out.String(), "If it is what runs this code, save it with:\n  dtctl repo-scope set checkout --service "+flowService+"\n")
		})
	}
}

// Identifiers are listed in one order everywhere: the Kubernetes workload,
// then the service name, then the process group and the service, the two
// names before the two deprecated ids.
func TestRepoScopeListsBindingsWorkloadFirst(t *testing.T) {
	const svc, pg = "SERVICE-0000000000000001", "PROCESS_GROUP-0000000000000001"
	r := &reposcope.DiscoveryReport{Unit: ".", Dir: ".", Verdict: reposcope.VerdictMatch,
		Verdicts: map[string]reposcope.Verdict{"service": reposcope.VerdictMatch, "processGroup": reposcope.VerdictMatch,
			"workload": reposcope.VerdictMatch, "serviceName": reposcope.VerdictMatch},
		Candidates: []reposcope.Candidate{{Rank: 1, Name: "checkout", Namespace: "payments", Workload: "checkout", ServiceName: "checkout",
			Services: []string{svc}, ServiceCount: 1, ProcessGroups: []string{pg}, ProcessGroupCount: 1, Tier: "exact", SetCommand: "x"}}}
	var out bytes.Buffer
	printRepoScopeDiscovery(&out, r, repoScopeProposal{}, 0)
	require.Regexp(t, `#\s+CLUSTER\s+WORKLOAD\s+SERVICE NAME\s+PROCESS GROUP\s+SERVICE\s+TIER\n`, out.String())
	require.Regexp(t, `1\s+-\s+payments/checkout\s+checkout\s+`+pg+`\s+`+svc+`\s+exact\n`, out.String())
	require.Contains(t, out.String(), "Verdict: match: workload, service name, process group, service\n")

	ids := reposcope.Candidate{Name: "checkout", Services: []string{svc}, ServiceCount: 1, ProcessGroups: []string{pg}, ProcessGroupCount: 1}
	require.Equal(t, "dtctl repo-scope set checkout --process-group "+pg+" --service "+svc, repoScopeSetCommand(r, ids, nil))
	links, _ := repoScopeHandLinks(r, nil)
	require.Equal(t, "dtctl repo-scope set <name> --process-group PROCESS_GROUP-… --service SERVICE-…", links[2])

	rows := repoScopeRows([]reposcope.Entry{{Name: "checkout", Services: []string{svc}}})
	var table bytes.Buffer
	require.NoError(t, output.NewPrinterWithOpts(output.PrinterOptions{Format: "table", Writer: &table}).PrintList(rows))
	require.Regexp(t, `^NAME\s+PATH\s+WORKLOADS\s+SERVICE NAMES\s+PROCESS GROUPS\s+SERVICES\s+SEGMENTS\s*\n`, table.String())
}

// A set line saves exactly the candidate shown, so it binds the cluster the
// candidate's records carry. One whose records carry none gets a workload
// binding only when no cluster runs the same namespace and workload: without
// the cluster the binding would select those too.
func TestRepoScopeSetCommandBindsTheCluster(t *testing.T) {
	const uid = "11111111-2222-3333-4444-555555555555"
	r := &reposcope.DiscoveryReport{Unit: ".", Dir: "."}
	c := reposcope.Candidate{Rank: 1, Name: "checkout", ClusterUID: uid, ClusterName: "prod-eu", Namespace: "payments", Workload: "checkout"}
	require.Equal(t, "dtctl repo-scope set checkout --cluster-uid "+uid+" --namespace payments --workload checkout", repoScopeSetCommand(r, c, nil))

	// The cluster is a key the candidate's rows were grouped by, not a list
	// a sample can cut short, so a sampled candidate's line carries it too.
	c.Sampled = true
	require.Contains(t, repoScopeSetCommand(r, c, nil), "--cluster-uid "+uid)

	none := reposcope.Candidate{Rank: 2, Name: "checkout", Namespace: "payments", Workload: "checkout", WorkloadShared: true}
	require.Empty(t, repoScopeSetCommand(r, none, nil))
	c.SetCommand = repoScopeSetCommand(r, c, nil)
	r.Candidates = []reposcope.Candidate{c, none}
	require.Equal(t, []string{"candidate 2 has no set line: its records name no cluster, and another candidate runs the same namespace and workload in one, so a binding without --cluster-uid would select both; to bind the workload in every cluster, leave --cluster-uid out of that candidate's line"},
		repoScopeNoLineNotes(r))

	var out bytes.Buffer
	writeRepoScopeCandidates(&out, &reposcope.DiscoveryReport{Unit: ".", Candidates: []reposcope.Candidate{
		{Rank: 1, ClusterUID: uid, ClusterName: "prod-eu", Namespace: "payments", Workload: "checkout", Tier: "exact"},
		{Rank: 2, ClusterUID: uid, Namespace: "payments", Workload: "ledger", Tier: "exact"},
		{Rank: 3, ServiceName: "checkout", Tier: "exact"},
	}})
	require.Regexp(t, `#\s+CLUSTER\s+WORKLOAD\s+SERVICE NAME`, out.String())
	require.Regexp(t, `1\s+prod-eu\s+payments/checkout\s`, out.String())
	require.Regexp(t, `2\s+`+uid+`\s+payments/ledger\s`, out.String(), "a cluster without a name shows its uid")
	require.Regexp(t, `3\s+-\s+-\s+checkout\s`, out.String())
}
