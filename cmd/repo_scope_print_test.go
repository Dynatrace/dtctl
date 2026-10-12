package cmd

import (
	"bytes"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/dynatrace-oss/dtctl/pkg/reposcope"
)

func TestRepoScopeDiscoveryLimitsProposedLines(t *testing.T) {
	report := &reposcope.DiscoveryReport{Unit: "services/checkout", Dir: "services/checkout", Verdict: reposcope.VerdictAmbiguous}
	for i := 1; i <= 7; i++ {
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

// A module three deployables share names them as prose and proposes
// discovering each.
func TestRepoScopeDiscoverySharedByThree(t *testing.T) {
	report := &reposcope.DiscoveryReport{Unit: ".", Dir: "core", SharedBy: []string{"billing", "checkout", "ledger"}}
	proposal := newRepoScopeProposal(report, nil)
	require.Equal(t, "no entry is proposed for core, shared by billing, checkout and ledger; discover each to save its entry", proposal.shared)
	require.Len(t, proposal.discoverEach, 3)
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
	whole := &reposcope.DiscoveryReport{Unit: ".", Dir: ".", Queries: []reposcope.Query{
		{DataObject: "spans", Rows: 0},
		{DataObject: "logs", Rows: 1},
	}}
	require.Equal(t, "dtctl repo-scope set checkout --namespace payments --workload checkout --service-name checkout",
		repoScopeSetCommand(whole, c, nil))

	sampled := &reposcope.DiscoveryReport{Unit: ".", Dir: ".", Queries: []reposcope.Query{
		{DataObject: "spans", Truncated: true, Cause: "truncated"},
		{DataObject: "spans", Window: "1h", SamplingRatio: 100000},
		{DataObject: "logs", Rows: 1},
	}}
	require.Equal(t, "dtctl repo-scope set checkout --namespace payments --workload checkout", repoScopeSetCommand(sampled, c, nil),
		"the spans sample matched nothing, and a spans-only checkout below its floor would share the name")

	refused := &reposcope.DiscoveryReport{Unit: ".", Dir: ".", Queries: []reposcope.Query{
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
