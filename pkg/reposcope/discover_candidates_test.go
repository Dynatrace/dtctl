package reposcope

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Dynatrace/dtctl/sdk/inventory"
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

// The query the scan cap stopped still returned rows, and each names an
// identity that exists. When a sampled retry answers, the ones it did not
// return are kept as candidates, without the count a partial scan cannot
// give, and the result is partial. They were thrown away, so a second cluster
// the capped scan had already shown disappeared from the report.
func TestDiscover_SampledRetryKeepsWhatTheCappedScanSaw(t *testing.T) {
	const otherCluster = "99999999-2222-3333-4444-555555555555"
	row := func(uid string, records float64) map[string]interface{} {
		return map[string]interface{}{fieldClusterUID: uid, fieldNamespace: "payments", fieldWorkload: "checkout",
			fieldServiceName: "checkout", fieldRecords: records, fieldProcessGroups: collected(pgID), fieldProcessGroupCount: float64(1)}
	}
	runner := &fakeRunner{answers: map[string][]answer{
		"spans": {
			capped([]map[string]interface{}{row(clusterUID, 4000), row(otherCluster, 9000)}),
			sampledAnswer([]map[string]interface{}{row(clusterUID, 17)}, 100_000_000),
		},
	}}
	r, err := Discover(context.Background(), runner, singleGo(t), prodHost)
	require.NoError(t, err)

	require.Len(t, r.Candidates, 2)
	found, seen := r.Candidates[0], r.Candidates[1]
	assert.Equal(t, []string{clusterUID, otherCluster}, []string{found.ClusterUID, seen.ClusterUID})
	assert.Equal(t, int64(17), found.SpansSampled)
	assert.Empty(t, found.CappedScan)
	assert.Equal(t, []string{"spans"}, seen.CappedScan)
	assert.Equal(t, []int64{0, 0}, []int64{seen.Spans, seen.SpansSampled}, "a partial scan's count is not one")
	assert.Empty(t, seen.ProcessGroups, "nor are its ids all of them")
	assert.Equal(t, "seen in the spans scan the 5 GB cap stopped, not in the sample that replaced it", seen.Evidence[len(seen.Evidence)-1])
	assert.True(t, found.WorkloadShared)
	assert.True(t, r.Partial)
	assert.Equal(t, VerdictAmbiguous, r.Verdict)
	assert.Contains(t, r.Notes, "the spans scan the 5 GB cap stopped saw candidates the sample did not return; they are listed without a count, and the result is partial")

	// What the sample returned is not listed twice, and a row the capped
	// scan saw of a candidate another query counted joins it.
	runner = &fakeRunner{answers: map[string][]answer{
		"spans": {
			capped([]map[string]interface{}{row(clusterUID, 4000), row(otherCluster, 9000)}),
			sampledAnswer([]map[string]interface{}{row(clusterUID, 17)}, 100_000_000),
		},
		"logs": {{records: []map[string]interface{}{row(otherCluster, 50)}}},
	}}
	r, err = Discover(context.Background(), runner, singleGo(t), prodHost)
	require.NoError(t, err)
	require.Len(t, r.Candidates, 2)
	for _, c := range r.Candidates {
		if c.ClusterUID == otherCluster {
			assert.Equal(t, []string{"spans"}, c.CappedScan)
			assert.Equal(t, int64(50), c.Logs)
		}
	}

	runner = &fakeRunner{answers: map[string][]answer{
		"spans": {capped([]map[string]interface{}{row(clusterUID, 4000)}), sampledAnswer([]map[string]interface{}{row(clusterUID, 17)}, 100_000_000)},
	}}
	r, err = Discover(context.Background(), runner, singleGo(t), prodHost)
	require.NoError(t, err)
	require.Len(t, r.Candidates, 1)
	assert.Empty(t, r.Candidates[0].CappedScan)
	assert.False(t, r.Partial)
}

// Grail says a result was limited in two ways. A typed RESULT_LIMIT_BYTES or
// RESULT_LIMIT_RECORDS is a page that lost rows, however short it is. Only
// the message without a type, which is also how a collected id array cut at
// its maxLength is reported, leaves a short page whole.
func TestDiscover_TypedResultLimitIsACutPage(t *testing.T) {
	for name, tc := range map[string]struct {
		answer  answer
		partial bool
	}{
		"typed":                 {answer{truncations: []inventory.Truncation{{Cause: inventory.TruncationResultLimit, Typed: true}}}, true},
		"by its message alone":  {answer{truncations: []inventory.Truncation{{Cause: inventory.TruncationResultLimit}}}, false},
		"both":                  {answer{truncations: []inventory.Truncation{{Cause: inventory.TruncationResultLimit}, {Cause: inventory.TruncationResultLimit, Typed: true}}}, true},
		"a runner with a cause": {answer{truncation: inventory.TruncationResultLimit}, true},
	} {
		t.Run(name, func(t *testing.T) {
			tc.answer.records = rows(t, "spans-checkout")
			r, err := Discover(context.Background(), &fakeRunner{answers: map[string][]answer{
				"spans": {tc.answer},
				"logs":  {{records: rows(t, "logs-checkout")}},
			}}, singleGo(t), prodHost)
			require.NoError(t, err)
			require.Len(t, r.Candidates, 1)
			assert.Equal(t, tc.partial, r.Partial)
			assert.Equal(t, tc.partial, r.Queries[0].Truncated)
			if tc.partial {
				assert.Equal(t, VerdictAmbiguous, r.Verdict)
				assert.Equal(t, []string{"spans stopped at Grail's result limit, so the verdict is capped at ambiguous"}, r.Notes)
			} else {
				assert.Equal(t, VerdictMatch, r.Verdict)
				assert.Empty(t, r.Notes)
			}
		})
	}
}

// A response can report a result limit and the scan cap together. The scan
// cap is the one that says how little was read, and the one a sampled retry
// answers, so it decides whichever notification came first.
func TestDiscover_ScanCapWinsOverAnotherLimit(t *testing.T) {
	scan := inventory.Truncation{Cause: inventory.TruncationScanLimit, Typed: true}
	for name, limits := range map[string][]inventory.Truncation{
		"reported first": {scan, {Cause: inventory.TruncationResultLimit}},
		"reported last":  {{Cause: inventory.TruncationResultLimit}, scan},
		"after a typed":  {{Cause: inventory.TruncationResultLimit, Typed: true}, scan},
	} {
		t.Run(name, func(t *testing.T) {
			runner := &fakeRunner{answers: map[string][]answer{
				"spans": {{records: rows(t, "spans-checkout"), truncations: limits}, sampledAnswer(rows(t, "spans-checkout-sampled"), 100_000_000)},
				"logs":  {{records: rows(t, "logs-checkout")}},
			}}
			r, err := Discover(context.Background(), runner, singleGo(t), prodHost)
			require.NoError(t, err)
			assert.Len(t, runner.sent, 3, "the capped query was retried sampled")
			assert.Equal(t, inventory.TruncationScanLimit, r.Queries[0].truncation)
			require.Len(t, r.Candidates, 1)
			assert.True(t, r.Candidates[0].Sampled)
		})
	}
}

// A row the query kept that discovery cannot explain is not an exact match.
// in() on an array-valued service.name keeps the row of an unrelated
// workload, and the tier used to fall back to exact, which made it a match
// with no evidence line naming a file.
func TestDiscover_UnexplainedRowIsNotExact(t *testing.T) {
	r, err := Discover(context.Background(), &fakeRunner{answers: map[string][]answer{
		"spans": {{records: []map[string]interface{}{{
			fieldNamespace: "batch", fieldWorkload: "totally-unrelated",
			fieldServiceName: []interface{}{"checkout", "nightly"}, fieldRecords: float64(40),
		}}}},
	}}, singleGo(t), prodHost)
	require.NoError(t, err)

	require.Len(t, r.Candidates, 1)
	c := r.Candidates[0]
	assert.Equal(t, "substring", c.Tier)
	assert.Equal(t, VerdictAmbiguous, r.Verdict)
	assert.Equal(t, []string{
		"neither its k8s.workload.name nor its service.name is a name discovery sent",
		"40 spans in 24h",
	}, c.Evidence)
}
