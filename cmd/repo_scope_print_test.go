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
		c.SetCommand = repoScopeSetCommand(c, repoScopeDir(report, c), nil)
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
