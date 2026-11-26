package cmd

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Dynatrace/dtctl/pkg/reposcope"
)

// Running a proposed line is the confirmation, so a line binds only what
// discovery verified selects the candidate and nothing else. Each row is a
// report discovery can produce and the line its first candidate gets.
func TestRepoScopeSetLine(t *testing.T) {
	const (
		uid = "11111111-2222-3333-4444-555555555555"
		svc = "SERVICE-0000000000000001"
		pg  = "PROCESS_GROUP-0000000000000001"
	)
	for _, tc := range []struct {
		name   string
		report reposcope.DiscoveryReport
		want   string
		notes  []string
	}{
		{
			// The record filter compares service.name to the sent names
			// only, so nothing looked for other workloads that report a
			// name the candidate merely carries, and the bindings of an
			// entry are alternatives: the name would select all of them.
			name: "a service name the repository did not name is not bound",
			report: reposcope.DiscoveryReport{Sent: []string{"checkout"}, Candidates: []reposcope.Candidate{
				{Rank: 1, Name: "checkout", ClusterUID: uid, Namespace: "payments", Workload: "checkout", ServiceName: "gateway", Tier: "exact", Spans: 10},
			}},
			want: "dtctl repo-scope set checkout --cluster-uid " + uid + " --namespace payments --workload checkout",
		},
		{
			name: "a service name the repository named is",
			report: reposcope.DiscoveryReport{Sent: []string{"checkout"}, Candidates: []reposcope.Candidate{
				{Rank: 1, Name: "checkout", ServiceName: "checkout", Tier: "exact", Spans: 10},
			}},
			want: "dtctl repo-scope set checkout --service-name checkout",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := tc.report
			r.Unit, r.Dir = ".", "."
			for i, c := range r.Candidates {
				r.Candidates[i].SetCommand = repoScopeSetCommand(&r, c, nil)
			}
			require.Equal(t, tc.want, r.Candidates[0].SetCommand)
			require.Equal(t, tc.notes, repoScopeNoLineNotes(&r))
		})
	}
}
