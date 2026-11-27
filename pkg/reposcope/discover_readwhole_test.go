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

