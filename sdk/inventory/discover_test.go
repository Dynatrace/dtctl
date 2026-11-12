package inventory

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"
)

type mockResponse struct {
	match     string
	records   []map[string]interface{}
	truncated bool
	err       error
	// types mirrors the DQL per-column type block. Applicability probes read
	// it to tell a field the data does not have ("undefined") from one that
	// exists and is simply empty.
	types map[string]string
	// cause mirrors the limit that cut a result short. It matters
	// independently of truncated because only a scan cap is worth retrying
	// with sampling.
	cause TruncationCause
	// sampled mirrors the response flag saying the environment really did
	// sample, which several data objects refuse to do while still answering
	// 200.
	sampled bool
}

type mockRunner struct {
	responses []mockResponse
	calls     []string
}

func (m *mockRunner) RunQuery(_ context.Context, dql string) (*RunResult, error) {
	m.calls = append(m.calls, dql)
	for _, r := range m.responses {
		if strings.Contains(dql, r.match) {
			if r.err != nil {
				return nil, r.err
			}
			return &RunResult{Records: r.records, Seconds: 0.1, Truncated: r.truncated, TruncationCause: r.cause, ColumnTypes: r.types, Sampled: r.sampled}, nil
		}
	}
	return &RunResult{Seconds: 0.1}, nil // default: empty result
}

func rec(kv ...interface{}) map[string]interface{} {
	m := map[string]interface{}{}
	for i := 0; i+1 < len(kv); i += 2 {
		m[kv[i].(string)] = kv[i+1]
	}
	return m
}

func testDefs() map[string]*CapabilityDef {
	return map[string]*CapabilityDef{
		"spans": {DataObject: "spans"},
		"rum":   {DataObject: "user.events"},
		// References a collapsed dt.entity.* view: verdicts must still see the
		// full catalog even though the reported DataObjects list does not.
		"classic-hosts": {DataObject: "dt.entity.host"},
		"hosts":         {EntityTypes: []string{"HOST"}},
		"azure":         {EntityTypes: []string{"AZURE_*"}},
		"k8s-metrics":   {MetricKey: "dt.kubernetes.*"},
		"genai":         {Probe: "fetch spans GENAIPROBE | limit 1", Window: "24h"},
		"rap":           {Probe: "fetch security.events RAPPROBE | limit 1", Window: "24h"},
	}
}

func testRunner() *mockRunner {
	return &mockRunner{responses: []mockResponse{
		{match: "dt.system.data_objects", records: []map[string]interface{}{
			rec("name", "logs", "fetchable", true), rec("name", "spans", "fetchable", true),
			rec("name", "dt.davis.events", "fetchable", true), rec("name", "metrics", "fetchable", false),
			rec("name", "dt.entity.host", "fetchable", true),
		}},
		{match: "dt.system.buckets", records: []map[string]interface{}{
			rec("name", "default_logs", "dt.system.table", "logs", "records", float64(100), "has_access", true),
		}},
		{match: `smartscapeNodes "*"`, records: []map[string]interface{}{
			rec("type", "HOST", "c", float64(5)),
			rec("type", "K8S_POD", "c", float64(10)),
		}},
		{match: "metrics from:", records: []map[string]interface{}{rec("metric.key", "dt.kubernetes.container.cpu")}},
		{match: "GENAIPROBE", records: []map[string]interface{}{rec("gen_ai.system", "x")}},
		// RAPPROBE deliberately unmatched: empty result → absent
	}}
}

func TestDiscoverInventory(t *testing.T) {
	runner := testRunner()
	inv, err := Discover(context.Background(), runner, testDefs(), DiscoverOptions{
		ContextName: "test",
		Segments:    []SegmentInfo{{UID: "s1", Name: "prod"}},
	})
	if err != nil {
		t.Fatalf("Discover() error: %v", err)
	}
	report := inv.Discovery

	// dt.entity.* lookback views are collapsed to a count, not listed.
	if want := []string{"logs", "spans", "dt.davis.events"}; strings.Join(inv.DataObjects, ",") != strings.Join(want, ",") {
		t.Errorf("DataObjects = %v, want %v", inv.DataObjects, want)
	}
	if inv.EntityViews != 1 {
		t.Errorf("EntityViews = %d, want 1", inv.EntityViews)
	}
	if len(inv.QueryOnly) != 1 || inv.QueryOnly[0] != "metrics" {
		t.Errorf("QueryOnly = %v, want [metrics]", inv.QueryOnly)
	}
	if len(inv.Buckets) != 1 || inv.Buckets[0] != "default_logs" {
		t.Errorf("Buckets = %v", inv.Buckets)
	}
	if inv.EntityTypes["HOST"] != 5 || inv.EntityTypes["K8S_POD"] != 10 {
		t.Errorf("EntityTypes = %v", inv.EntityTypes)
	}

	wantPresent := []string{"classic-hosts", "genai", "hosts", "k8s-metrics", "spans"}
	if strings.Join(inv.Capabilities, ",") != strings.Join(wantPresent, ",") {
		t.Errorf("Capabilities = %v, want %v", inv.Capabilities, wantPresent)
	}
	// Every absent entry carries its evidence.
	wantAbsent := map[string]string{
		"azure": "no AZURE_* entities in the live census",
		"rap":   "discovery probe returned no rows in 24h",
		"rum":   "no user.events in the data-object catalog",
	}
	if len(inv.Absent) != len(wantAbsent) {
		t.Fatalf("Absent = %v, want %d entries", inv.Absent, len(wantAbsent))
	}
	for _, a := range inv.Absent {
		if want, ok := wantAbsent[a.Name]; !ok || !strings.Contains(a.Evidence, want) {
			t.Errorf("absent entry %+v, want evidence %q", a, wantAbsent[a.Name])
		}
	}
	if len(inv.Unknown) != 0 {
		t.Errorf("Unknown = %v, want none on a fully evaluated run", inv.Unknown)
	}

	if len(inv.Segments) != 1 || inv.Segments[0].UID != "s1" {
		t.Errorf("Segments = %v", inv.Segments)
	}
	// Notes: unfetchable + davis canonical stream + census lookback.
	if len(inv.Notes) != 3 {
		t.Errorf("Notes = %v, want 3", inv.Notes)
	}
	// Battery: catalog, buckets, census, metric catalog, 2 probes.
	if report.Queries != 6 {
		t.Errorf("report.Queries = %d, want 6 (calls: %v)", report.Queries, runner.calls)
	}
}

func TestDiscoverSkipsMetricCatalogWithoutMetricDefs(t *testing.T) {
	runner := testRunner()
	defs := map[string]*CapabilityDef{"spans": {DataObject: "spans"}}
	if _, err := Discover(context.Background(), runner, defs, DiscoverOptions{}); err != nil {
		t.Fatalf("Discover() error: %v", err)
	}
	for _, c := range runner.calls {
		if strings.HasPrefix(c, "metrics ") {
			t.Errorf("metric catalog queried without any metricKey definition: %v", runner.calls)
		}
	}
}

func TestDiscoverBudgetStopsProbes(t *testing.T) {
	runner := testRunner()
	// 3 queries: catalog, buckets, census — no budget left for the metric
	// catalog or the probes.
	inv, err := Discover(context.Background(), runner, testDefs(), DiscoverOptions{BudgetQueries: 3})
	if err != nil {
		t.Fatalf("Discover() error: %v", err)
	}
	report := inv.Discovery
	if report.Queries != 3 {
		t.Errorf("report.Queries = %d, want 3", report.Queries)
	}
	// Capabilities that never got their check degrade to unknown — not to
	// absent with fabricated evidence. Structural shapes whose facts did load
	// still get real verdicts.
	wantUnknown := map[string]string{
		"genai":       "budget exhausted",
		"rap":         "budget exhausted",
		"k8s-metrics": "metric catalog unavailable",
	}
	if len(inv.Unknown) != len(wantUnknown) {
		t.Fatalf("Unknown = %v, want %d entries", inv.Unknown, len(wantUnknown))
	}
	for _, u := range inv.Unknown {
		if want, ok := wantUnknown[u.Name]; !ok || !strings.Contains(u.Evidence, want) {
			t.Errorf("unknown entry %+v, want reason %q", u, wantUnknown[u.Name])
		}
	}
	for _, a := range inv.Absent {
		if _, clash := wantUnknown[a.Name]; clash {
			t.Errorf("%q reported absent, but its check never ran", a.Name)
		}
	}
	// The partial state is noted.
	found := false
	for _, n := range inv.Notes {
		if strings.Contains(n, "budget exhausted") {
			found = true
		}
	}
	if !found {
		t.Errorf("missing budget-exhausted note: %v", inv.Notes)
	}
}

func TestDiscoverExactBudgetIsNotPartial(t *testing.T) {
	runner := testRunner()
	defs := map[string]*CapabilityDef{"spans": {DataObject: "spans"}}
	// Catalog, buckets, census: exactly 3 queries — the budget ends exactly
	// spent with nothing skipped, so the inventory is complete, not partial.
	inv, err := Discover(context.Background(), runner, defs, DiscoverOptions{BudgetQueries: 3})
	if err != nil {
		t.Fatalf("Discover() error: %v", err)
	}
	for _, n := range inv.Notes {
		if strings.Contains(n, "partial") {
			t.Errorf("exactly consumed budget must not report a partial inventory: %v", inv.Notes)
		}
	}
}

func TestDiscoverProbeErrorIsUnknown(t *testing.T) {
	runner := testRunner()
	runner.responses = append([]mockResponse{
		{match: "GENAIPROBE", err: fmt.Errorf("scan limit of 25GB exceeded\nsecond line")},
	}, runner.responses...)
	inv, err := Discover(context.Background(), runner, testDefs(), DiscoverOptions{})
	if err != nil {
		t.Fatalf("Discover() error: %v", err)
	}
	var genai *CapabilityStatus
	for i := range inv.Unknown {
		if inv.Unknown[i].Name == "genai" {
			genai = &inv.Unknown[i]
		}
	}
	if genai == nil {
		t.Fatalf("genai must be unknown when its probe errors, got unknown=%v absent=%v", inv.Unknown, inv.Absent)
	}
	if !strings.Contains(genai.Evidence, "probe failed: scan limit of 25GB exceeded") {
		t.Errorf("evidence = %q, want the probe failure cited", genai.Evidence)
	}
	for _, a := range inv.Absent {
		if a.Name == "genai" {
			t.Errorf("errored probe must not fabricate absence evidence: %+v", a)
		}
	}
}

func TestDiscoverCensusFailureYieldsUnknown(t *testing.T) {
	runner := testRunner()
	runner.responses = append([]mockResponse{
		{match: `smartscapeNodes "*"`, err: fmt.Errorf("smartscape unavailable")},
	}, runner.responses...)
	defs := map[string]*CapabilityDef{"hosts": {EntityTypes: []string{"HOST"}}}
	inv, err := Discover(context.Background(), runner, defs, DiscoverOptions{})
	if err != nil {
		t.Fatalf("Discover() error: %v", err)
	}
	if len(inv.Unknown) != 1 || inv.Unknown[0].Name != "hosts" || !strings.Contains(inv.Unknown[0].Evidence, "census unavailable") {
		t.Errorf("Unknown = %v, want hosts unknown with census unavailable", inv.Unknown)
	}
	if len(inv.Absent) != 0 {
		t.Errorf("Absent = %v, want none — the census never ran", inv.Absent)
	}
}

// cancellingRunner cancels the run's context when a marker query arrives,
// mimicking Ctrl+C mid-probe: the executor reports context.Canceled and every
// later query must be refused, not issued.
type cancellingRunner struct {
	inner  *mockRunner
	cancel context.CancelFunc
	marker string
}

func (c *cancellingRunner) RunQuery(ctx context.Context, dql string) (*RunResult, error) {
	if strings.Contains(dql, c.marker) {
		c.cancel()
		c.inner.calls = append(c.inner.calls, dql)
		return nil, context.Canceled
	}
	return c.inner.RunQuery(ctx, dql)
}

func TestDiscoverCancelledMidProbesAborts(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	inner := testRunner()
	runner := &cancellingRunner{inner: inner, cancel: cancel, marker: "GENAIPROBE"}
	_, err := Discover(ctx, runner, testDefs(), DiscoverOptions{})
	if err == nil {
		t.Fatal("Discover() must abort on cancellation, not report a half-discovered inventory")
	}
	for _, c := range inner.calls {
		if strings.Contains(c, "RAPPROBE") {
			t.Errorf("query issued after cancellation: %v", inner.calls)
		}
	}
}

func TestDiscoverCatalogErrorAborts(t *testing.T) {
	runner := &mockRunner{responses: []mockResponse{
		{match: "dt.system.data_objects", err: fmt.Errorf("boom")},
	}}
	if _, err := Discover(context.Background(), runner, testDefs(), DiscoverOptions{}); err == nil {
		t.Fatal("Discover() should abort when the data-object catalog is unavailable")
	}
}

func TestDiscoverCatalogFallbackWithoutUsableWith(t *testing.T) {
	// The partitioned catalog query errors (no usable_with column); the flat
	// list must be used instead.
	runner := &mockRunner{responses: []mockResponse{
		{match: "usable_with", err: fmt.Errorf("FIELD_DOES_NOT_EXIST")},
		{match: "dt.system.data_objects | fields name", records: []map[string]interface{}{rec("name", "logs")}},
	}}
	inv, err := Discover(context.Background(), runner, map[string]*CapabilityDef{"logs": {DataObject: "logs"}}, DiscoverOptions{})
	if err != nil {
		t.Fatalf("Discover() error: %v", err)
	}
	if len(inv.DataObjects) != 1 || inv.DataObjects[0] != "logs" {
		t.Errorf("DataObjects = %v, want [logs]", inv.DataObjects)
	}
	if len(inv.Capabilities) != 1 || inv.Capabilities[0] != "logs" {
		t.Errorf("Capabilities = %v, want [logs]", inv.Capabilities)
	}
}

// Catalog membership alone must not prove a stream capability: on a tenant
// that never ingested RUM, user.events is still in the catalog, but every one
// of its buckets is empty. Bucket statistics (already fetched for the bucket
// list) close that gap.
func TestDiscoverEmptyStreamIsAbsent(t *testing.T) {
	runner := testRunner()
	runner.responses = append([]mockResponse{
		{match: "dt.system.data_objects", records: []map[string]interface{}{
			rec("name", "logs", "fetchable", true),
			rec("name", "user.events", "fetchable", true),
			rec("name", "dt.davis.problems", "fetchable", true),
		}},
		{match: "dt.system.buckets", records: []map[string]interface{}{
			rec("name", "default_logs", "dt.system.table", "logs", "records", float64(100), "has_access", true),
			rec("name", "default_user_events", "dt.system.table", "user.events", "records", float64(0), "has_access", true),
		}},
	}, runner.responses...)
	defs := map[string]*CapabilityDef{
		"logs": {DataObject: "logs"},
		"rum":  {DataObject: "user.events"},
		// No bucket covers dt.davis.problems: liveness is unjudgeable, the
		// catalog verdict stands.
		"davis": {DataObject: "dt.davis.problems"},
	}
	inv, err := Discover(context.Background(), runner, defs, DiscoverOptions{})
	if err != nil {
		t.Fatalf("Discover() error: %v", err)
	}
	if want := "davis,logs"; strings.Join(inv.Capabilities, ",") != want {
		t.Errorf("Capabilities = %v, want [%s]", inv.Capabilities, want)
	}
	if len(inv.Absent) != 1 || inv.Absent[0].Name != "rum" || !strings.Contains(inv.Absent[0].Evidence, "all its buckets are empty") {
		t.Errorf("Absent = %v, want rum absent with the empty buckets cited", inv.Absent)
	}
}

func TestDiscoverEmptyStreamLivenessSkippedWhenBucketsUnjudgeable(t *testing.T) {
	catalog := mockResponse{match: "dt.system.data_objects", records: []map[string]interface{}{
		rec("name", "user.events", "fetchable", true),
	}}
	defs := map[string]*CapabilityDef{"rum": {DataObject: "user.events"}}
	cases := []struct {
		name    string
		buckets mockResponse
	}{
		{"truncated bucket list", mockResponse{match: "dt.system.buckets", records: []map[string]interface{}{
			rec("name", "default_user_events", "dt.system.table", "user.events", "records", float64(0), "has_access", true),
		}, truncated: true}},
		{"inaccessible buckets", mockResponse{match: "dt.system.buckets", records: []map[string]interface{}{
			rec("name", "default_user_events", "dt.system.table", "user.events", "records", float64(0), "has_access", false),
		}}},
		{"bucket discovery failed", mockResponse{match: "dt.system.buckets", err: fmt.Errorf("boom")}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			runner := &mockRunner{responses: []mockResponse{catalog, tc.buckets}}
			inv, err := Discover(context.Background(), runner, defs, DiscoverOptions{})
			if err != nil {
				t.Fatalf("Discover() error: %v", err)
			}
			// Emptiness cannot be judged: the catalog verdict stands.
			if len(inv.Capabilities) != 1 || inv.Capabilities[0] != "rum" {
				t.Errorf("Capabilities = %v, want [rum] (absent=%v)", inv.Capabilities, inv.Absent)
			}
		})
	}
}

// A truncated metric catalog is the regression behind false metric-family
// absences on large tenants: >1000 distinct keys, the client cap cuts the
// list, and every miss used to become fabricated absence evidence.
func TestDiscoverTruncatedMetricCatalogMissIsUnknown(t *testing.T) {
	runner := testRunner()
	runner.responses = append([]mockResponse{
		{match: "metrics from:", records: []map[string]interface{}{rec("metric.key", "dt.kubernetes.container.cpu")}, truncated: true},
	}, runner.responses...)
	defs := map[string]*CapabilityDef{
		"k8s-metrics":  {MetricKey: "dt.kubernetes.*"},
		"host-metrics": {MetricKey: "dt.host.*"},
	}
	inv, err := Discover(context.Background(), runner, defs, DiscoverOptions{})
	if err != nil {
		t.Fatalf("Discover() error: %v", err)
	}
	// A hit in the truncated sample still proves presence.
	if len(inv.Capabilities) != 1 || inv.Capabilities[0] != "k8s-metrics" {
		t.Errorf("Capabilities = %v, want [k8s-metrics]", inv.Capabilities)
	}
	// A miss proves nothing: the key may sit in the rows that were cut.
	if len(inv.Unknown) != 1 || inv.Unknown[0].Name != "host-metrics" || !strings.Contains(inv.Unknown[0].Evidence, "metric catalog truncated") {
		t.Errorf("Unknown = %v, want host-metrics unknown with the truncation cited", inv.Unknown)
	}
	if len(inv.Absent) != 0 {
		t.Errorf("Absent = %v, want none — a truncated catalog cannot prove absence", inv.Absent)
	}
	found := false
	for _, n := range inv.Discovery.Notes {
		if strings.Contains(n, "metric catalog truncated") {
			found = true
		}
	}
	if !found {
		t.Errorf("missing truncation note in report: %v", inv.Discovery.Notes)
	}
}

func TestDiscoverTruncatedObjectCatalogMissIsUnknown(t *testing.T) {
	runner := testRunner()
	runner.responses = append([]mockResponse{
		{match: "dt.system.data_objects", records: []map[string]interface{}{
			rec("name", "spans", "fetchable", true),
		}, truncated: true},
	}, runner.responses...)
	defs := map[string]*CapabilityDef{
		"spans": {DataObject: "spans"},
		"rum":   {DataObject: "user.events"},
	}
	inv, err := Discover(context.Background(), runner, defs, DiscoverOptions{})
	if err != nil {
		t.Fatalf("Discover() error: %v", err)
	}
	if len(inv.Capabilities) != 1 || inv.Capabilities[0] != "spans" {
		t.Errorf("Capabilities = %v, want [spans]", inv.Capabilities)
	}
	if len(inv.Unknown) != 1 || inv.Unknown[0].Name != "rum" || !strings.Contains(inv.Unknown[0].Evidence, "data-object catalog truncated") {
		t.Errorf("Unknown = %v, want rum unknown with the truncation cited", inv.Unknown)
	}
	if len(inv.Absent) != 0 {
		t.Errorf("Absent = %v, want none", inv.Absent)
	}
}

func TestDiscoverTruncatedCensusMissIsUnknown(t *testing.T) {
	runner := testRunner()
	runner.responses = append([]mockResponse{
		{match: `smartscapeNodes "*"`, records: []map[string]interface{}{
			rec("type", "HOST", "c", float64(5)),
		}, truncated: true},
	}, runner.responses...)
	defs := map[string]*CapabilityDef{
		"hosts": {EntityTypes: []string{"HOST"}},
		"azure": {EntityTypes: []string{"AZURE_*"}},
	}
	inv, err := Discover(context.Background(), runner, defs, DiscoverOptions{})
	if err != nil {
		t.Fatalf("Discover() error: %v", err)
	}
	if len(inv.Capabilities) != 1 || inv.Capabilities[0] != "hosts" {
		t.Errorf("Capabilities = %v, want [hosts]", inv.Capabilities)
	}
	if len(inv.Unknown) != 1 || inv.Unknown[0].Name != "azure" || !strings.Contains(inv.Unknown[0].Evidence, "entity census truncated") {
		t.Errorf("Unknown = %v, want azure unknown with the truncation cited", inv.Unknown)
	}
}

func TestDiscoverTruncatedEmptyProbeIsUnknown(t *testing.T) {
	runner := testRunner()
	runner.responses = append([]mockResponse{
		// The probe hit a limit (e.g. the scan cap) before finding any match:
		// zero rows in a partial result is not absence.
		{match: "RAPPROBE", truncated: true},
	}, runner.responses...)
	inv, err := Discover(context.Background(), runner, testDefs(), DiscoverOptions{})
	if err != nil {
		t.Fatalf("Discover() error: %v", err)
	}
	var rap *CapabilityStatus
	for i := range inv.Unknown {
		if inv.Unknown[i].Name == "rap" {
			rap = &inv.Unknown[i]
		}
	}
	if rap == nil || !strings.Contains(rap.Evidence, "cut by a limit") {
		t.Fatalf("rap must be unknown with the limit cited, got unknown=%v absent=%v", inv.Unknown, inv.Absent)
	}
	for _, a := range inv.Absent {
		if a.Name == "rap" {
			t.Errorf("a limit-cut probe must not fabricate absence evidence: %+v", a)
		}
	}
}

// A probe cut by the byte limit (--max-result-bytes) that still returned a
// matching row is present: one hit answers an existence probe, so the rows
// past the cut cannot change the verdict.
func TestDiscoverByteCutProbeWithAMatchIsPresent(t *testing.T) {
	runner := testRunner()
	runner.responses = append([]mockResponse{
		{match: "RAPPROBE", records: []map[string]interface{}{{"n": 1}}, truncated: true, cause: TruncationResultLimit},
	}, runner.responses...)
	inv, err := Discover(context.Background(), runner, testDefs(), DiscoverOptions{})
	if err != nil {
		t.Fatalf("Discover() error: %v", err)
	}
	if !slices.Contains(inv.Capabilities, "rap") {
		t.Errorf("a byte-cut probe with a match must be present, got capabilities=%v unknown=%v", inv.Capabilities, inv.Unknown)
	}
}

// TestDiscoverRejectsInvalidDefinitions proves malformed Go-constructed
// definitions fail fast — before any query is issued — instead of silently
// getting no verdict.
func TestDiscoverRejectsInvalidDefinitions(t *testing.T) {
	runner := &mockRunner{}
	_, err := Discover(context.Background(), runner, map[string]*CapabilityDef{
		"broken": {},
	}, DiscoverOptions{})
	if err == nil || !strings.Contains(err.Error(), "invalid capability definitions") || !strings.Contains(err.Error(), `"broken"`) {
		t.Fatalf("error = %v, want invalid-definitions naming the capability", err)
	}
	if len(runner.calls) != 0 {
		t.Errorf("no query must run on invalid definitions, got %d", len(runner.calls))
	}
}

// slowRunner blocks on one query until its context ends — the shape that
// actually broke this: a metric catalog that runs for half a minute on a large
// tenant while the budget says 20 seconds.
type slowRunner struct {
	*mockRunner
	slowMatch string
	slowFor   time.Duration
}

func (s *slowRunner) RunQuery(ctx context.Context, dql string) (*RunResult, error) {
	if !strings.Contains(dql, s.slowMatch) {
		return s.mockRunner.RunQuery(ctx, dql)
	}
	s.mockRunner.calls = append(s.mockRunner.calls, dql)
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-time.After(s.slowFor):
		return &RunResult{Seconds: s.slowFor.Seconds()}, nil
	}
}

func TestDiscoverSecondsBudgetCutsSlowQuery(t *testing.T) {
	runner := &slowRunner{mockRunner: testRunner(), slowMatch: "metrics ", slowFor: 30 * time.Second}
	// The three fast queries debit 0.1s each (mockRunner), leaving ~0.7s for a
	// metric catalog that would run for 30. Debiting after the fact would let
	// it run to completion; the budget has to cut it.
	start := time.Now()
	inv, err := Discover(context.Background(), runner, testDefs(), DiscoverOptions{BudgetSeconds: 1})
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("Discover() error: %v", err)
	}
	if elapsed > 10*time.Second {
		t.Fatalf("discovery took %v with a 1s budget — the slow query was not cut", elapsed)
	}
	// Cut, not crashed: the fact source it would have filled degrades to
	// unknown, and everything already discovered keeps its verdict.
	wantUnknown := map[string]string{
		"k8s-metrics": "metric catalog unavailable",
		"genai":       "budget exhausted",
		"rap":         "budget exhausted",
	}
	if len(inv.Unknown) != len(wantUnknown) {
		t.Fatalf("Unknown = %v, want %d entries", inv.Unknown, len(wantUnknown))
	}
	for _, u := range inv.Unknown {
		if want, ok := wantUnknown[u.Name]; !ok || !strings.Contains(u.Evidence, want) {
			t.Errorf("unknown entry %+v, want reason %q", u, wantUnknown[u.Name])
		}
	}
	if len(inv.DataObjects) == 0 || len(inv.EntityTypes) == 0 {
		t.Errorf("facts discovered before the cut were lost: %+v", inv)
	}
	// The partial state is on the record, and the cut query is charged what it
	// spent rather than silently omitted.
	found := false
	for _, n := range inv.Notes {
		if strings.Contains(n, "budget exhausted") {
			found = true
		}
	}
	if !found {
		t.Errorf("missing budget-exhausted note: %v", inv.Notes)
	}
	if inv.Discovery.Queries != 4 {
		t.Errorf("report.Queries = %d, want 4 (the cut query was still issued)", inv.Discovery.Queries)
	}
	if inv.Discovery.Seconds <= 0.3 {
		t.Errorf("report.Seconds = %v, want the cut query's own time included", inv.Discovery.Seconds)
	}
}

func TestDiscoverCallerCancellationIsNotABudgetStop(t *testing.T) {
	runner := &slowRunner{mockRunner: testRunner(), slowMatch: "metrics ", slowFor: 30 * time.Second}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	time.AfterFunc(200*time.Millisecond, cancel)
	// A generous budget: only the caller's abort can end this run, and an
	// aborted run yields no inventory rather than a partial one dressed up as
	// a verdict.
	inv, err := Discover(ctx, runner, testDefs(), DiscoverOptions{BudgetSeconds: 300})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Discover() error = %v, want context.Canceled", err)
	}
	if inv != nil {
		t.Errorf("cancelled run returned an inventory: %+v", inv)
	}
}

func metricCatalogCall(calls []string) string {
	for _, c := range calls {
		if strings.HasPrefix(c, "metrics ") {
			return c
		}
	}
	return ""
}

func TestMetricCatalogPushesDefinitionGlobsIntoTheQuery(t *testing.T) {
	runner := testRunner()
	defs := map[string]*CapabilityDef{
		"k8s-metrics":  {MetricKey: "dt.kubernetes.*"},
		"host-metrics": {MetricKey: "dt.host.*"},
		// A second definition on the same glob must not widen the filter.
		"hosts-again": {MetricKey: "dt.host.*"},
		// Non-metric shapes contribute nothing to it.
		"spans": {DataObject: "spans"},
	}
	if _, err := Discover(context.Background(), runner, defs, DiscoverOptions{}); err != nil {
		t.Fatalf("Discover() error: %v", err)
	}
	got := metricCatalogCall(runner.calls)
	want := `metrics from:now()-2h | filter matchesValue(metric.key, "dt.host.*") or matchesValue(metric.key, "dt.kubernetes.*") | summarize c = count(), by:{metric.key} | limit 10000`
	if got != want {
		t.Errorf("metric catalog query:\n got %s\nwant %s", got, want)
	}
}

// An untranslatable glob must widen the read, never narrow it: a filter that
// approximated path.Match's character classes could drop a key that matches
// and turn a live metric family into a fabricated absence.
func TestMetricCatalogFallsBackForUntranslatableGlobs(t *testing.T) {
	for name, glob := range map[string]string{
		"character class": "dt.[hk]ost.*",
		"single-char":     "dt.hos?.*",
		"match-all":       "*",
		"quoted":          `dt."host".*`,
	} {
		t.Run(name, func(t *testing.T) {
			runner := testRunner()
			defs := map[string]*CapabilityDef{
				"k8s-metrics": {MetricKey: "dt.kubernetes.*"},
				"odd":         {MetricKey: glob},
			}
			if _, err := Discover(context.Background(), runner, defs, DiscoverOptions{}); err != nil {
				t.Fatalf("Discover() error: %v", err)
			}
			got := metricCatalogCall(runner.calls)
			want := "metrics from:now()-2h | summarize c = count(), by:{metric.key} | limit 10000"
			if got != want {
				t.Errorf("metric catalog query:\n got %s\nwant %s", got, want)
			}
		})
	}
}

// The filter changes which keys come back, never which verdict they produce.
func TestMetricCatalogVerdictsSurviveTheFilter(t *testing.T) {
	runner := testRunner()
	inv, err := Discover(context.Background(), runner, testDefs(), DiscoverOptions{})
	if err != nil {
		t.Fatalf("Discover() error: %v", err)
	}
	present := false
	for _, c := range inv.Capabilities {
		if c == "k8s-metrics" {
			present = true
		}
	}
	if !present {
		t.Errorf("k8s-metrics not present: capabilities=%v unknown=%v absent=%v", inv.Capabilities, inv.Unknown, inv.Absent)
	}
	if !strings.Contains(metricCatalogCall(runner.calls), `matchesValue(metric.key, "dt.kubernetes.*")`) {
		t.Errorf("filter did not carry the evaluated glob: %s", metricCatalogCall(runner.calls))
	}
}
