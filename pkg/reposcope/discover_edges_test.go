package reposcope

import (
	"testing"
	"testing/fstest"

	"github.com/stretchr/testify/require"

	"github.com/dynatrace-oss/dtctl/sdk/inventory"
)

func TestDiscoverTermEvidenceAndMalformedRows(t *testing.T) {
	p, err := PlanDiscovery(fstest.MapFS{}, ".", []string{"checkout"})
	require.NoError(t, err)
	t.Run("record evidence", func(t *testing.T) {
		r, err := Discover(t.Context(), &fakeRunner{answers: map[string][]answer{
			"spans": {{records: []map[string]interface{}{
				{fieldNamespace: "ignored", fieldRecords: float64(100)},
				{fieldServiceName: "checkout", fieldRecords: nil, fieldServices: nil, fieldServiceCount: nil},
			}}},
		}}, p, prodHost)
		require.NoError(t, err)
		require.Len(t, r.Candidates, 1)
		require.Equal(t, []string{`service.name "checkout" from term "checkout"`}, r.Candidates[0].Evidence)
		require.Zero(t, r.Candidates[0].Spans)
		require.Zero(t, r.Candidates[0].ServiceCount)
		require.Equal(t, VerdictMatch, r.Verdict)
	})
	t.Run("process group fallback", func(t *testing.T) {
		r, err := Discover(t.Context(), &fakeRunner{answers: map[string][]answer{
			"dt.entity.process_group": {{records: []map[string]interface{}{
				{fieldEntityName: "checkout"},
				{fieldEntityID: "PROCESS_GROUP-0000000000000001", fieldEntityName: "unrelated"},
				{fieldEntityID: pgID, fieldEntityName: "checkout"},
			}}},
		}}, p, prodHost)
		require.NoError(t, err)
		require.Len(t, r.Candidates, 1)
		require.Equal(t, []string{pgID}, r.Candidates[0].ProcessGroups)
		require.Equal(t, []string{`entity.name "checkout" = term "checkout" (exact match)`}, r.Candidates[0].Evidence)
		require.Equal(t, VerdictMatch, r.Verdicts["processGroup"])
	})
}

// Only a scan cap is a volume problem, and only a volume problem is one
// sampling can solve (sdk/inventory/sampling.go): any other limit Grail
// reports caps the verdict with a note naming it, and sends no retry.
func TestDiscoverTruncationNotes(t *testing.T) {
	for _, tc := range []struct {
		cause inventory.TruncationCause
		note  string
	}{
		{inventory.TruncationTimeout, "Grail's time limit"},
		{inventory.TruncationConsumption, "the query consumption limit"},
		{inventory.TruncationCause("future-limit"), "a limit Grail reported"},
	} {
		t.Run(string(tc.cause), func(t *testing.T) {
			runner := &fakeRunner{answers: map[string][]answer{
				"spans": {{records: rows(t, "spans-checkout"), truncation: tc.cause}},
			}}
			r, err := Discover(t.Context(), runner, singleGo(t), prodHost)
			require.NoError(t, err)
			require.Len(t, runner.sent, 2, "no sampled retry")
			require.True(t, r.Partial)
			require.Equal(t, VerdictAmbiguous, r.Verdict)
			require.Equal(t, []string{"spans stopped at " + tc.note + ", so the verdict is capped at ambiguous"}, r.Notes)
		})
	}
	// On a record query a result limit is an id list cut at its maxLength
	// (TestDiscover_CollectedArrayCapIsNotACutPage); on an entity search it
	// is a cut page.
	t.Run(string(inventory.TruncationResultLimit), func(t *testing.T) {
		r, err := Discover(t.Context(), &fakeRunner{answers: map[string][]answer{
			"dt.entity.service": {{records: rows(t, "services-by-name"), truncation: inventory.TruncationResultLimit}},
		}}, singleGo(t), prodHost)
		require.NoError(t, err)
		require.True(t, r.Partial)
		require.Equal(t, []string{"dt.entity.service stopped at Grail's result limit, so the verdict is capped at ambiguous"}, r.Notes)
	})
}

func TestDiscoveryFieldAndVerdictLabels(t *testing.T) {
	require.Equal(t, []string{"k8s.workload.name", "service.name", "entity.name"}, MatchedFields())
	require.Equal(t, "process group", VerdictLabel("processGroup"))
	require.Equal(t, "future", VerdictLabel("future"))
}
