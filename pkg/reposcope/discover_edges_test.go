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
		r, err := Discover(t.Context(), &fakeRunner{answers: map[string]answer{
			"spans": {records: []map[string]interface{}{
				{fieldNamespace: "ignored", "records": float64(100)},
				{fieldServiceName: "checkout", "records": nil},
			}},
		}}, p, prodHost)
		require.NoError(t, err)
		require.Len(t, r.Candidates, 1)
		require.Equal(t, []string{`service.name "checkout" from term "checkout"`}, r.Candidates[0].Evidence)
		require.Zero(t, r.Candidates[0].Spans)
		require.Equal(t, VerdictMatch, r.Verdict)
	})
	t.Run("process group fallback", func(t *testing.T) {
		r, err := Discover(t.Context(), &fakeRunner{answers: map[string]answer{
			"dt.entity.process_group": {records: []map[string]interface{}{
				{fieldEntityName: "checkout"},
				{fieldEntityID: "PROCESS_GROUP-0000000000000001", fieldEntityName: "unrelated"},
				{fieldEntityID: pgID, fieldEntityName: "checkout"},
			}},
		}}, p, prodHost)
		require.NoError(t, err)
		require.Len(t, r.Candidates, 1)
		require.Equal(t, []string{pgID}, r.Candidates[0].ProcessGroups)
		require.Equal(t, []string{`entity.name "checkout" = term "checkout" (exact match)`}, r.Candidates[0].Evidence)
		require.Equal(t, VerdictMatch, r.Verdicts["processGroup"])
	})
}

func TestDiscoverTruncationNotes(t *testing.T) {
	for _, tc := range []struct {
		cause inventory.TruncationCause
		note  string
	}{
		{inventory.TruncationResultLimit, "Grail's result limit"},
		{inventory.TruncationTimeout, "Grail's time limit"},
		{inventory.TruncationConsumption, "the query consumption limit"},
		{inventory.TruncationCause("future-limit"), "a limit Grail reported"},
	} {
		t.Run(string(tc.cause), func(t *testing.T) {
			r, err := Discover(t.Context(), &fakeRunner{answers: map[string]answer{
				"spans": {records: rows(t, "spans-checkout"), truncation: tc.cause},
			}}, singleGo(t), prodHost)
			require.NoError(t, err)
			require.True(t, r.Partial)
			require.Equal(t, VerdictAmbiguous, r.Verdict)
			require.Equal(t, []string{"spans stopped at " + tc.note + ", so the verdict is capped at ambiguous"}, r.Notes)
		})
	}
}

func TestDiscoveryFieldAndVerdictLabels(t *testing.T) {
	require.Equal(t, []string{"k8s.workload.name", "service.name", "entity.name"}, MatchedFields())
	require.Equal(t, "process group", VerdictLabel("processGroup"))
	require.Equal(t, "future", VerdictLabel("future"))
}
