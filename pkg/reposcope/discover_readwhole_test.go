package reposcope

import (
	"context"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Dynatrace/dtctl/sdk/inventory"
)

// untypedLimit is a result limit a runner recognised by its message alone.
var untypedLimit = []inventory.Truncation{{Cause: inventory.TruncationResultLimit}}

func manyIDs(n int) []interface{} {
	ids := make([]interface{}, n)
	for i := range ids {
		ids[i] = fmt.Sprintf("SERVICE-%016X", i)
	}
	return ids
}

// Some deployments report a cut page only in a message, the way Grail may
// also word an id array cut at its maxLength. Which of the two it is shows in
// the rows: a clipped array is there, longer than a binding takes. Without
// one, the message says rows were lost, and one exact row is no match.
func TestDiscover_UntypedResultLimitIsACutUnlessARowShowsTheClippedArray(t *testing.T) {
	row := map[string]interface{}{fieldNamespace: "payments", fieldWorkload: "checkout", fieldServiceName: "checkout", fieldRecords: float64(1)}
	r, err := Discover(context.Background(), &fakeRunner{answers: map[string][]answer{
		"spans": {{records: []map[string]interface{}{row}, truncations: untypedLimit}},
	}}, singleGo(t), prodHost)
	require.NoError(t, err)
	assert.Equal(t, VerdictAmbiguous, r.Verdict)
	assert.True(t, r.Partial)
	assert.True(t, r.Queries[0].Truncated)
	assert.Equal(t, "spans stopped at Grail's result limit, so the result is partial", r.Notes[0])

	// 50 ids are a whole list, so they explain no message either.
	row[fieldServices] = manyIDs(discoveryIDLimit)
	r, err = Discover(context.Background(), &fakeRunner{answers: map[string][]answer{
		"spans": {{records: []map[string]interface{}{row}, truncations: untypedLimit}},
	}}, singleGo(t), prodHost)
	require.NoError(t, err)
	assert.True(t, r.Partial)

	row[fieldServices] = manyIDs(discoveryIDLimit + 1)
	r, err = Discover(context.Background(), &fakeRunner{answers: map[string][]answer{
		"spans": {{records: []map[string]interface{}{row}, truncations: untypedLimit}},
	}}, singleGo(t), prodHost)
	require.NoError(t, err)
	assert.False(t, r.Partial)
	assert.False(t, r.Queries[0].Truncated)
	assert.Equal(t, VerdictMatch, r.Verdicts["workload"])
}

// The 50-id bound is on ids, not on the arrays that hold them: one array of
// 120 ids is a cut list like 120 collected ones.
func TestDiscover_NestedIDsAreBounded(t *testing.T) {
	nested := make([]interface{}, 120)
	for i := range nested {
		nested[i] = fmt.Sprintf("PROCESS_GROUP-%016X", i)
	}
	ids, _, cut := idsField(map[string]interface{}{fieldProcessGroups: []interface{}{nested}}, fieldProcessGroups, processGroupRe)
	assert.Len(t, ids, discoveryIDLimit)
	assert.True(t, cut)

	ids, _, cut = idsField(map[string]interface{}{fieldProcessGroups: []interface{}{nested[:discoveryIDLimit]}}, fieldProcessGroups, processGroupRe)
	assert.Len(t, ids, discoveryIDLimit)
	assert.False(t, cut)

	r, err := Discover(context.Background(), &fakeRunner{answers: map[string][]answer{
		"logs": {{records: []map[string]interface{}{
			{fieldServiceName: "checkout", fieldProcessGroupCount: "1", fieldProcessGroups: []interface{}{nested}, fieldRecords: "10"},
		}}},
	}}, singleGo(t), prodHost)
	require.NoError(t, err)
	c := r.Candidates[0]
	assert.Len(t, c.ProcessGroups, discoveryIDLimit)
	assert.True(t, c.ProcessGroupsCapped)
	assert.Equal(t, int64(discoveryIDLimit+1), c.ProcessGroupCount)
}


// A sampled retry that was cut short, or that Grail did not sample, is not
// the answer, but the rows it returned name candidates that exist. They are
// listed without a count beside what the capped scan returned.
func TestDiscover_AnUnfinishedRetryKeepsTheCandidatesItNamed(t *testing.T) {
	row := []map[string]interface{}{{fieldNamespace: "payments", fieldWorkload: "checkout", fieldServiceName: "checkout",
		fieldRecords: float64(9), fieldProcessGroups: collected(pgID), fieldProcessGroupCount: float64(1)}}
	for name, rung := range map[string]answer{
		"a typed result limit": {records: row, sampled: true, scannedBytes: 1, truncations: []inventory.Truncation{{Cause: inventory.TruncationResultLimit, Typed: true}}},
		"a scan limit":         {records: row, sampled: true, scannedBytes: 1, truncation: inventory.TruncationScanLimit},
		"a timeout":            {records: row, sampled: true, scannedBytes: 1, truncation: inventory.TruncationTimeout},
		"not sampled":          {records: row},
	} {
		t.Run(name, func(t *testing.T) {
			r, err := Discover(context.Background(), &fakeRunner{answers: map[string][]answer{"spans": {capped(nil), rung}}}, singleGo(t), prodHost)
			require.NoError(t, err)
			require.Len(t, r.Candidates, 1)
			c := r.Candidates[0]
			assert.Equal(t, "checkout", c.Workload)
			assert.Equal(t, []string{"spans"}, c.CappedScan)
			assert.Equal(t, []int64{0, 0}, []int64{c.Spans, c.SpansSampled}, "an unfinished read's count is not one")
			assert.Empty(t, c.ProcessGroups, "nor are its ids all of them")
			assert.False(t, c.Sampled)
			assert.Equal(t, "seen in a sampled retry over spans that did not finish, so without a count", c.Evidence[len(c.Evidence)-1])
			assert.True(t, r.Partial)
			assert.Equal(t, VerdictAmbiguous, r.Verdict)
			assert.False(t, r.SampledRecords(), "no sample answered")
			assert.Empty(t, r.SamplingNotes())
		})
	}

	// A candidate the capped scan counted is not listed as uncounted.
	r, err := Discover(context.Background(), &fakeRunner{answers: map[string][]answer{"spans": {capped(row), {records: row}}}}, singleGo(t), prodHost)
	require.NoError(t, err)
	require.Len(t, r.Candidates, 1)
	assert.Empty(t, r.Candidates[0].CappedScan)
	assert.Equal(t, int64(9), r.Candidates[0].Spans)
}

// What only the capped scan saw of a workload is folded like what a query
// counted: its row without a cluster marks the object as carrying none, the
// sampled hits of a row it joins take it off the list, and its cluster name
// is kept.
func TestDiscover_FoldsKeepWhatTheCappedScanSaw(t *testing.T) {
	row := func(uid, service string, records float64) map[string]interface{} {
		rec := map[string]interface{}{fieldNamespace: "payments", fieldWorkload: "checkout", fieldRecords: records}
		if uid != "" {
			rec[fieldClusterUID] = uid
		}
		if service != "" {
			rec[fieldServiceName] = service
		}
		return rec
	}
	t.Run("a clusterless row only the capped scan saw", func(t *testing.T) {
		r, err := Discover(context.Background(), &fakeRunner{answers: map[string][]answer{
			"spans": {{records: []map[string]interface{}{row(clusterUID, "checkout", 300)}}},
			"logs": {
				capped([]map[string]interface{}{row("", "checkout", 4000)}),
				sampledAnswer([]map[string]interface{}{row("", "other-checkout", 5)}, 100_000_000),
			},
		}}, singleGo(t), prodHost)
		require.NoError(t, err)
		for _, c := range r.Candidates {
			if c.ClusterUID == clusterUID {
				assert.Equal(t, []string{"logs"}, c.CappedScan)
				assert.Equal(t, []string{"logs"}, c.WithoutCluster, "a line with the cluster would select none of those logs")
				return
			}
		}
		t.Fatal("no candidate in the cluster")
	})
	t.Run("a row the sample returned under the same workload", func(t *testing.T) {
		r, err := Discover(context.Background(), &fakeRunner{answers: map[string][]answer{
			"spans": {{records: []map[string]interface{}{row(clusterUID, "checkout", 300)}}},
			"logs": {
				capped([]map[string]interface{}{row(clusterUID, "checkout", 4000)}),
				sampledAnswer([]map[string]interface{}{row(clusterUID, "", 25)}, 100_000_000),
			},
		}}, singleGo(t), prodHost)
		require.NoError(t, err)
		require.Len(t, r.Candidates, 1)
		c := r.Candidates[0]
		assert.Equal(t, int64(25), c.LogsSampled)
		assert.Empty(t, c.CappedScan, "the sample returned its logs")
		for _, e := range c.Evidence {
			assert.NotContains(t, e, "not in the sample")
		}
	})
	t.Run("the cluster name", func(t *testing.T) {
		seen := row("99999999-2222-3333-4444-555555555555", "checkout", 9000)
		seen[fieldAnyClusterName] = "prod-east"
		r, err := Discover(context.Background(), &fakeRunner{answers: map[string][]answer{
			"spans": {
				capped([]map[string]interface{}{seen}),
				sampledAnswer([]map[string]interface{}{row(clusterUID, "checkout", 17)}, 100_000_000),
			},
		}}, singleGo(t), prodHost)
		require.NoError(t, err)
		require.Len(t, r.Candidates, 2)
		assert.Equal(t, "prod-east", r.Candidates[1].ClusterName)
	})
}

// The report's two statements about sampling rest on one reading of the
// queries: a retry that was itself cut short answered nothing, so no record
// was read by sampling and there is no floor to state.
func TestDiscover_ACutRetryIsNoSample(t *testing.T) {
	cut := answer{records: rows(t, "spans-checkout-sampled"), sampled: true, scannedBytes: 1,
		truncations: []inventory.Truncation{{Cause: inventory.TruncationTimeout, Typed: true}}}
	r, err := Discover(context.Background(), &fakeRunner{answers: map[string][]answer{"spans": {capped(rows(t, "spans-checkout")), cut}}}, singleGo(t), prodHost)
	require.NoError(t, err)
	assert.False(t, r.SampledRecords())
	assert.Empty(t, r.SamplingNotes())
	assert.True(t, r.Partial)

	// A retry that filled its page did read by sampling.
	r, err = Discover(context.Background(), &fakeRunner{answers: map[string][]answer{
		"spans": {capped(nil), sampledAnswer(serviceNames(discoveryRowLimit), 1)}}}, singleGo(t), prodHost)
	require.NoError(t, err)
	assert.True(t, r.SampledRecords())
	assert.Len(t, r.SamplingNotes(), 1)
}
