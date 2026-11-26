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
		{
			// A query that did not answer could have found a second
			// candidate with this service name or these ids.
			name: "a partial report binds the workload only",
			report: reposcope.DiscoveryReport{Sent: []string{"checkout"}, Partial: true, Candidates: []reposcope.Candidate{
				{Rank: 1, Name: "checkout", Namespace: "payments", Workload: "checkout", ServiceName: "checkout", Tier: "exact", Spans: 10,
					ProcessGroups: []string{pg}, LogsByProcessGroup: 7},
			}},
			want: "dtctl repo-scope set checkout --namespace payments --workload checkout",
			notes: []string{"candidate 1's line leaves --process-group out, and with it the 7 logs found by process group: " +
				"the result is partial, and a candidate it did not see could share its service name or ids"},
		},
		{
			name: "a partial report binds no ids",
			report: reposcope.DiscoveryReport{Partial: true, Candidates: []reposcope.Candidate{
				{Rank: 1, Name: "checkout", Tier: "exact", ProcessGroups: []string{pg}, Services: []string{svc}},
			}},
			notes: []string{"candidate 1 has no set line: the result is partial, and a candidate it did not see could share its service name or ids, " +
				"so only --namespace with --workload is proposed, and no workload binding selects it"},
		},
		{
			name: "nor a service name alone",
			report: reposcope.DiscoveryReport{Sent: []string{"checkout"}, Partial: true, Candidates: []reposcope.Candidate{
				{Rank: 1, Name: "checkout", ServiceName: "checkout", Tier: "exact", Spans: 10},
			}},
			notes: []string{"candidate 1 has no set line: the result is partial, and a candidate it did not see could share its service name or ids, " +
				"so only --namespace with --workload is proposed, and no workload binding selects it"},
		},
		{
			// Both names are withheld as shared, and the ids are the other
			// candidate's too: nothing selects this one alone.
			name: "ids another candidate carries are not bound",
			report: reposcope.DiscoveryReport{Sent: []string{"checkout"}, Candidates: []reposcope.Candidate{
				{Rank: 1, Name: "checkout", Namespace: "payments", Workload: "checkout", WorkloadShared: true, ServiceName: "checkout", ServiceNameShared: true,
					Tier: "exact", Spans: 10, Logs: 10, ProcessGroups: []string{pg}, Services: []string{svc}},
				{Rank: 2, Name: "checkout", ClusterUID: uid, Namespace: "payments", Workload: "checkout", WorkloadShared: true, ServiceName: "checkout", ServiceNameShared: true,
					Tier: "exact", Spans: 5, Services: []string{svc}},
			}},
			notes: []string{"candidate 1 has no set line: its records name no cluster, and another candidate runs the same namespace and workload in one, so a binding without --cluster-uid would select both; to bind the workload in every cluster, leave --cluster-uid out of that candidate's line"},
		},
		{
			name: "ids of its own are",
			report: reposcope.DiscoveryReport{Sent: []string{"checkout"}, Candidates: []reposcope.Candidate{
				{Rank: 1, Name: "checkout", ServiceName: "checkout", ServiceNameShared: true, Tier: "exact", Spans: 10, Logs: 10, ProcessGroups: []string{pg}, Services: []string{svc}},
				{Rank: 2, Name: "checkout", ServiceName: "checkout", ServiceNameShared: true, Namespace: "payments", Workload: "checkout", Tier: "exact", Spans: 5,
					Services: []string{"SERVICE-0000000000000002"}},
			}},
			want: "dtctl repo-scope set checkout --process-group " + pg + " --service " + svc,
		},
		{
			name: "an id two candidates carry selects both",
			report: reposcope.DiscoveryReport{Sent: []string{"checkout"}, Candidates: []reposcope.Candidate{
				{Rank: 1, Name: "checkout", ServiceName: "checkout", ServiceNameShared: true, Tier: "exact", Spans: 10, Services: []string{svc}},
				{Rank: 2, Name: "checkout", ServiceName: "checkout", ServiceNameShared: true, Namespace: "payments", Workload: "checkout", Tier: "exact", Spans: 5, Services: []string{svc}},
			}},
			notes: []string{"candidate 1 has no set line: candidate 2 carries one of its entity ids, so a binding by id would select both"},
		},
		{
			// A scope filters spans by the service id and logs by the
			// process group. An entry of process groups alone binds nothing
			// for the spans that found the candidate.
			name: "ids that leave out the spans that found it are not bound",
			report: reposcope.DiscoveryReport{Sent: []string{"checkout"}, Candidates: []reposcope.Candidate{
				{Rank: 1, Name: "checkout", ServiceName: "checkout", ServiceNameShared: true, Tier: "exact", Spans: 10, ProcessGroups: []string{pg}},
				{Rank: 2, Name: "checkout", ServiceName: "checkout", ServiceNameShared: true, Namespace: "payments", Workload: "checkout", Tier: "exact", Spans: 5},
			}},
			notes: []string{"candidate 1 has no set line: its spans carry no dt.entity.service, the id a scope filters spans by, so a binding by id would leave them out"},
		},
		{
			name: "nor ids that leave out its logs",
			report: reposcope.DiscoveryReport{Candidates: []reposcope.Candidate{
				{Rank: 1, Name: "checkout", Workload: "checkout", Tier: "exact", Spans: 10, Logs: 4, Services: []string{svc}},
			}},
			notes: []string{"candidate 1 has no set line: its logs carry no dt.entity.process_group, the id a scope filters logs by, so a binding by id would leave them out"},
		},
		{
			// "pay" inside "prepaymentgateway" is no evidence that the
			// entity runs this code.
			name: "an entity matched inside a longer word gets no line",
			report: reposcope.DiscoveryReport{Sent: []string{"pay"}, Candidates: []reposcope.Candidate{
				{Rank: 1, Name: "pay", Tier: "substring", Services: []string{svc}},
			}},
			notes: []string{"candidate 1 has no set line: it matched only inside a longer name, which is too weak a tie to bind it by entity id"},
		},
		{
			name: "an entity matched by a whole segment does",
			report: reposcope.DiscoveryReport{Sent: []string{"checkout"}, Candidates: []reposcope.Candidate{
				{Rank: 1, Name: "checkout", Tier: "segment", Services: []string{svc}},
			}},
			want: "dtctl repo-scope set checkout --service " + svc,
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
