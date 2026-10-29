package inventory

import (
	"context"
	"testing"

	"github.com/Dynatrace/dtctl/sdk/api/query"
)

// TestTruncationCauseOf pins the classification a Runner depends on. Getting
// it wrong does not fail loudly — it downgrades a scan-capped probe to a
// generic "cut short by a limit" and loses the only actionable remedy.
func TestTruncationCauseOf(t *testing.T) {
	tests := []struct {
		name string
		n    query.Notification
		want TruncationCause
	}{
		{"scan limit", query.Notification{NotificationType: "SCAN_LIMIT_GBYTES"}, TruncationScanLimit},
		{"result records", query.Notification{NotificationType: "RESULT_LIMIT_RECORDS"}, TruncationResultLimit},
		{"result bytes", query.Notification{NotificationType: "RESULT_LIMIT_BYTES"}, TruncationResultLimit},
		{"fetch timeout", query.Notification{NotificationType: "FETCH_TIMEOUT"}, TruncationTimeout},
		{"exec time limit", query.Notification{NotificationType: "FETCH_EXEC_TIME_LIMIT"}, TruncationTimeout},
		{"consumption", query.Notification{NotificationType: "QUERY_CONSUMPTION_LIMIT"}, TruncationConsumption},
		// Sampling is declared in the query, not imposed on it.
		{"sampling is not truncation", query.Notification{NotificationType: "SAMPLING_APPLIED"}, ""},
		{"unrelated", query.Notification{NotificationType: "SOMETHING_ELSE"}, ""},
		// Some deployments report the cut only in the message.
		{"scan limit by message only", query.Notification{
			Message: "Your execution was stopped after 25 gigabytes of data were scanned."}, TruncationScanLimit},
		{"internal time limit by message only", query.Notification{
			Message: "Query exceeded the internal time limit"}, TruncationTimeout},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := TruncationCauseOf(tt.n); got != tt.want {
				t.Errorf("TruncationCauseOf = %q, want %q", got, tt.want)
			}
		})
	}
}

// TestFirstTruncationCauseSkipsNonTruncating ensures a sampling notice sitting
// ahead of a real cap does not mask it.
func TestFirstTruncationCauseSkipsNonTruncating(t *testing.T) {
	got := FirstTruncationCause([]query.Notification{
		{NotificationType: "SAMPLING_APPLIED"},
		{NotificationType: "SCAN_LIMIT_GBYTES"},
	})
	if got != TruncationScanLimit {
		t.Errorf("FirstTruncationCause = %q, want %q", got, TruncationScanLimit)
	}
}

// TestColumnTypesOfPrefersAKnownType is the property the n/a verdict rests on:
// a column typed in one index range and undefined in another exists, and must
// not be reported as missing.
func TestColumnTypesOfPrefersAKnownType(t *testing.T) {
	got := ColumnTypesOf([]query.ColumnTypes{
		{IndexRange: []int{0, 0}, Mappings: map[string]query.ColumnType{
			"k8s.namespace.name": {Type: TypeUndefined},
		}},
		{IndexRange: []int{1, 1}, Mappings: map[string]query.ColumnType{
			"k8s.namespace.name": {Type: "string"},
		}},
	})
	if got["k8s.namespace.name"] != "string" {
		t.Errorf("a known type must win over undefined, got %q", got["k8s.namespace.name"])
	}
	if ColumnTypesOf(nil) != nil {
		t.Error("no type blocks should yield no map, so applicability stays unknown")
	}
}

// TestRunnerOmittingColumnTypesLosesNotApplicable documents the cost of the
// obligation ColumnTypesOf exists to discharge. It is not asserting desired
// behaviour — it pins the degradation, so that if a future change makes the
// omission detectable instead of silent, this test fails and gets revisited.
func TestRunnerOmittingColumnTypesLosesNotApplicable(t *testing.T) {
	defs := map[string]*CapabilityDef{"host-metrics": {MetricKey: "dt.host.*"}}
	fixture := func(withTypes bool) []mockResponse {
		grouping := mockResponse{match: "by:{`k8s.namespace.name`}"}
		if withTypes {
			grouping.types = map[string]string{"k8s.namespace.name": TypeUndefined}
		}
		return []mockResponse{
			{match: "dt.system.data_objects", records: []map[string]interface{}{
				rec("name", "metrics", "fetchable", false, "type", "table"),
			}},
			{match: "dt.system.buckets"},
			{match: "metrics from:", records: []map[string]interface{}{rec("metric.key", "dt.host.cpu.usage")}},
			grouping,
			{match: "timeseries"},
		}
	}
	state := func(withTypes bool) SignalState {
		t.Helper()
		inv, err := Discover(context.Background(), &mockRunner{responses: fixture(withTypes)}, defs, windowOpts())
		if err != nil {
			t.Fatalf("Discover: %v", err)
		}
		return signalByName(t, inv, "host-metrics").State
	}
	if got := state(true); got != SignalNotApplicable {
		t.Fatalf("with column types the verdict should be n/a, got %q", got)
	}
	if got := state(false); got != SignalEmpty {
		t.Fatalf("without column types the n/a verdict silently degrades to empty; got %q — if this now reports unknown or errors, the contract improved and this test should be updated", got)
	}
}
