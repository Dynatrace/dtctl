package reposcope

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Logs of a workload need not carry a service name while its spans do. The
// two rows are one workload: as two candidates the report said the first had
// no logs under its workload, beside a second candidate that was those logs.
func TestDiscover_RowWithoutServiceNameJoinsItsWorkload(t *testing.T) {
	row := func(uid, serviceName string, records float64) map[string]interface{} {
		r := map[string]interface{}{fieldNamespace: "payments", fieldWorkload: "checkout", fieldRecords: records,
			fieldProcessGroups: collected(pgID), fieldProcessGroupCount: float64(1)}
		if uid != "" {
			r[fieldClusterUID] = uid
		}
		if serviceName != "" {
			r[fieldServiceName] = serviceName
		}
		return r
	}
	for name, uid := range map[string]string{"in a cluster": clusterUID, "without a cluster": ""} {
		t.Run(name, func(t *testing.T) {
			runner := &fakeRunner{answers: map[string][]answer{
				"spans": {{records: []map[string]interface{}{row(uid, "checkout", 300)}}},
				"logs":  {{records: []map[string]interface{}{row(uid, "", 500)}}},
			}}
			r, err := Discover(context.Background(), runner, singleGo(t), prodHost)
			require.NoError(t, err)

			require.Len(t, r.Candidates, 1)
			c := r.Candidates[0]
			assert.Equal(t, []string{uid, "checkout", "checkout"}, []string{c.ClusterUID, c.Workload, c.ServiceName})
			assert.Equal(t, []int64{300, 500, 0}, []int64{c.Spans, c.Logs, c.LogsByProcessGroup})
			assert.Equal(t, VerdictMatch, r.Verdict)
			assert.Empty(t, r.Notes, "its logs were found")
			assert.Len(t, runner.sent, 2, "no lookup by process group")
		})
	}

	t.Run("then its cluster", func(t *testing.T) {
		// A row with neither joins the service name first, then the cluster.
		runner := &fakeRunner{answers: map[string][]answer{
			"spans": {{records: []map[string]interface{}{row(clusterUID, "checkout", 300), row(clusterUID, "", 20)}}},
			"logs":  {{records: []map[string]interface{}{row("", "", 500)}}},
		}}
		r, err := Discover(context.Background(), runner, singleGo(t), prodHost)
		require.NoError(t, err)
		require.Len(t, r.Candidates, 1)
		c := r.Candidates[0]
		assert.Equal(t, []int64{320, 500}, []int64{c.Spans, c.Logs})
		assert.Equal(t, []string{"logs"}, c.WithoutCluster)
	})

	t.Run("not beside two service names", func(t *testing.T) {
		// Which of the two the row belongs to is not known.
		runner := &fakeRunner{answers: map[string][]answer{
			"spans": {{records: []map[string]interface{}{row(clusterUID, "checkout", 300), row(clusterUID, "checkout-service", 100)}}},
			"logs":  {{records: []map[string]interface{}{row(clusterUID, "", 500)}}},
		}}
		r, err := Discover(context.Background(), runner, singleGo(t), prodHost)
		require.NoError(t, err)
		require.Len(t, r.Candidates, 3)
		assert.Equal(t, VerdictAmbiguous, r.Verdict)
	})
}

// Two candidates whose records name no workload are two services, not one
// workload: a process group both carry is neither's alone. The first was
// credited with the second's logs, and its line would have bound the group.
func TestDiscover_ProcessGroupSharedByServicesWithoutAWorkload(t *testing.T) {
	row := func(serviceName string, records float64) map[string]interface{} {
		return map[string]interface{}{fieldServiceName: serviceName, fieldRecords: records,
			fieldProcessGroups: collected(pgID), fieldProcessGroupCount: float64(1)}
	}
	runner := &fakeRunner{answers: map[string][]answer{
		"spans": {{records: []map[string]interface{}{row("checkout", 100)}}},
		"logs": {
			{records: []map[string]interface{}{row("checkout-service", 5000)}},
			{records: []map[string]interface{}{pgLogs(5000, pgID)}},
		},
	}}
	r, err := Discover(context.Background(), runner, singleGo(t), prodHost)
	require.NoError(t, err)

	require.Len(t, r.Candidates, 2)
	assert.Len(t, runner.sent, 2, "the group is not looked up for either")
	for _, c := range r.Candidates {
		assert.Zero(t, c.LogsByProcessGroup, c.ServiceName)
	}
	assert.Equal(t, []string{
		"candidate 1 has spans, but no logs carry its k8s.workload.name or service.name, so an entry bound by those reaches no logs; if its logs carry a process group, bind it with --process-group",
	}, r.Notes)
}
