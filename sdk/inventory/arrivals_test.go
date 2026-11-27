package inventory

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

var fixedNow = time.Date(2026, 9, 11, 6, 25, 0, 0, time.UTC)

func at(minutes, seconds int) string {
	return fixedNow.Add(-(time.Duration(minutes)*time.Minute + time.Duration(seconds)*time.Second)).Format(time.RFC3339)
}

func windowDefs() map[string]*CapabilityDef {
	return map[string]*CapabilityDef{
		"logs":      {DataObject: "logs"},
		"spans":     {DataObject: "spans", TimeField: "start_time"},
		"bizevents": {DataObject: "bizevents"},
		"rum":       {DataObject: "user.events"},
		// security.events is in the catalog and busy, but carries nothing the
		// scope names — the n/a case.
		"security":  {DataObject: "security.events"},
		"synthetic": {DataObject: "dt.synthetic.events"},
		"k8smetric": {MetricKey: "dt.kubernetes.*"},
		// Non-signal shapes must be ignored entirely by windowed mode.
		"hosts": {EntityTypes: []string{"HOST"}},
		"genai": {Probe: "fetch spans GENAIPROBE | limit 1", Window: "24h"},
	}
}

// windowRunner serves a tenant where each state is represented exactly once.
//
// Applicability probes share a prefix with the scoped probes they follow, so
// they are listed first: the mock returns the first response whose match is a
// substring of the query.
func windowRunner() *mockRunner {
	return &mockRunner{responses: []mockResponse{
		// user.events does carry the scope's field, so its zero match is a
		// real "this scope is not producing into it".
		{match: "fetch user.events, from:now()-15m | filter isNotNull",
			records: []map[string]interface{}{rec("present", "1")}},
		// security.events carries no such field...
		{match: "fetch security.events, from:now()-15m | filter isNotNull",
			records: []map[string]interface{}{rec("present", "0")}},
		// ...and is demonstrably receiving records, so the miss is about the
		// field, not about a quiet window.
		{match: "fetch security.events, from:now()-15m | limit 1 | summarize any",
			records: []map[string]interface{}{rec("any", "9")}},

		{match: "dt.system.data_objects", records: []map[string]interface{}{
			rec("name", "logs", "fetchable", true, "type", "table"),
			rec("name", "spans", "fetchable", true, "type", "table"),
			rec("name", "bizevents", "fetchable", true, "type", "table"),
			rec("name", "user.events", "fetchable", true, "type", "table"),
			rec("name", "security.events", "fetchable", true, "type", "table"),
			rec("name", "metrics", "fetchable", false, "type", "table"),
		}},
		{match: "dt.system.buckets", records: []map[string]interface{}{
			rec("name", "default_logs", "dt.system.table", "logs", "records", float64(1000), "has_access", true),
			rec("name", "default_spans", "dt.system.table", "spans", "records", float64(500), "has_access", true),
			// bizevents is in the catalog but empty within retention → no-data.
			rec("name", "default_bizevents", "dt.system.table", "bizevents", "records", float64(0), "has_access", true),
			// user.events holds plenty tenant-wide but matches nothing → empty.
			rec("name", "default_rum", "dt.system.table", "user.events", "records", float64(2000), "has_access", true),
			rec("name", "default_security", "dt.system.table", "security.events", "records", float64(3000), "has_access", true),
		}},
		{match: "metrics from:", records: []map[string]interface{}{rec("metric.key", "dt.kubernetes.container.cpu")}},
		// The exhaustive probes, matched by their whole opening so that a
		// sampled rung a test did not list is not answered by them: it would
		// come back as 100 matches without the sampled flag, which the walk
		// reads as a refusal.
		{match: "fetch logs, from:now()-15m | filter", records: []map[string]interface{}{
			rec("matched", "100", "last_seen", at(0, 30)),
		}},
		{match: "fetch spans, from:now()-15m | filter", records: []map[string]interface{}{
			rec("matched", "50", "last_seen", at(20, 0)),
		}},
		{match: "fetch bizevents, from:", records: []map[string]interface{}{rec("matched", "0")}},
		{match: "fetch user.events, from:", records: []map[string]interface{}{rec("matched", "0")}},
		{match: "fetch security.events, from:", records: []map[string]interface{}{rec("matched", "0")}},
		{match: "timeseries", records: []map[string]interface{}{
			rec("n", []interface{}{float64(5), float64(7)},
				"timeframe", map[string]interface{}{"start": at(3, 0)},
				"interval", "60000000000"),
		}},
	}}
}

func windowOpts() DiscoverOptions {
	return DiscoverOptions{
		Now:        func() time.Time { return fixedNow },
		Since:      "now()-15m",
		Scope:      `k8s.namespace.name == "payments"`,
		StaleAfter: 5 * time.Minute,
	}
}

func signalByName(t *testing.T, inv *Inventory, name string) Signal {
	t.Helper()
	for _, s := range inv.Signals {
		if s.Name == name {
			return s
		}
	}
	t.Fatalf("signal %q not in result (%d signals)", name, len(inv.Signals))
	return Signal{}
}

func TestWindowedSignalStates(t *testing.T) {
	runner := windowRunner()
	inv, err := Discover(context.Background(), runner, windowDefs(), windowOpts())
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}

	for _, tc := range []struct {
		name  string
		state SignalState
	}{
		{"logs", SignalLive},
		{"spans", SignalStale},
		{"bizevents", SignalNoData},
		{"rum", SignalEmpty},
		{"security", SignalNotApplicable},
		{"synthetic", SignalAbsent},
		{"k8smetric", SignalLive},
	} {
		if got := signalByName(t, inv, tc.name).State; got != tc.state {
			t.Errorf("%s state = %q, want %q", tc.name, got, tc.state)
		}
	}

	// Non-signal shapes have no "since" reading and must not appear.
	for _, absent := range []string{"hosts", "genai"} {
		for _, s := range inv.Signals {
			if s.Name == absent {
				t.Errorf("%s must not be reported as a signal", absent)
			}
		}
	}

	if logs := signalByName(t, inv, "logs"); logs.Records != 100 || logs.AgeSeconds != 30 {
		t.Errorf("logs = %d records / age %ds, want 100 / 30", logs.Records, logs.AgeSeconds)
	}
	if m := signalByName(t, inv, "k8smetric"); m.Datapoints != 12 {
		t.Errorf("k8smetric datapoints = %d, want 12", m.Datapoints)
	}

	// The empty verdict must cite tenant-wide liveness, since that is the whole
	// reason it is distinguishable from no-data.
	if ev := signalByName(t, inv, "rum").Evidence; !strings.Contains(ev, "2000 records within retention") {
		t.Errorf("rum evidence should cite retention, got %q", ev)
	}

	// A stream's n/a verdict must name the field and stay within what was
	// actually measured: the window was sampled, so it may not claim the
	// stream structurally cannot carry the scope. Overstating that would
	// reproduce, one level up, the false-structural reading this state exists
	// to prevent.
	ev := signalByName(t, inv, "security").Evidence
	if !strings.Contains(ev, "k8s.namespace.name") || !strings.Contains(ev, "the last 15m") {
		t.Errorf("stream n/a evidence should name the field and the window, got %q", ev)
	}
	if strings.Contains(ev, "structurally") || strings.Contains(ev, "property of the question") {
		t.Errorf("stream n/a evidence must not claim a structural absence, got %q", ev)
	}

	want := StateSummary{Live: 2, Stale: 1, Empty: 1, NoData: 1, NotApplicable: 1, Absent: 1}
	if *inv.Summary != want {
		t.Errorf("summary = %+v, want %+v", *inv.Summary, want)
	}
	if inv.Window == nil || inv.Window.Since != "now()-15m" || inv.Window.Filter == "" {
		t.Errorf("window not recorded: %+v", inv.Window)
	}
}

func TestWindowedProbeShape(t *testing.T) {
	runner := windowRunner()
	if _, err := Discover(context.Background(), runner, windowDefs(), windowOpts()); err != nil {
		t.Fatalf("Discover: %v", err)
	}
	joined := strings.Join(runner.calls, "\n")

	// Filter-first is a cost requirement, not a style choice: countIf-shaped
	// probes measured ~19x the scan of the filtered form.
	for _, call := range runner.calls {
		if strings.Contains(call, "countIf(") {
			t.Errorf("probe must not use countIf (measured ~19x scan cost): %s", call)
		}
	}
	if !strings.Contains(joined, `fetch logs, from:now()-15m | filter k8s.namespace.name == "payments" | summarize matched = count(), last_seen = takeMax(timestamp)`) {
		t.Errorf("logs probe not filter-first with the scope pushed into the fetch:\n%s", joined)
	}
	// spans carries start_time and no timestamp at all.
	if !strings.Contains(joined, "fetch spans, from:now()-15m | filter") || !strings.Contains(joined, "takeMax(start_time)") {
		t.Errorf("spans probe must read start_time:\n%s", joined)
	}
	// A stream outside the catalog must not be probed: the query would fail and
	// the failure would masquerade as a probe error.
	if strings.Contains(joined, "fetch dt.synthetic.events") {
		t.Error("must not probe a stream that is absent from the catalog")
	}
	// The entity census has no windowed meaning and is pure cost here.
	if strings.Contains(joined, "smartscapeNodes") {
		t.Error("windowed mode must not run the entity census")
	}
}

func TestWindowedSuppressesEnvironmentListings(t *testing.T) {
	inv, err := Discover(context.Background(), windowRunner(), windowDefs(), windowOpts())
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if len(inv.DataObjects) > 0 || len(inv.Buckets) > 0 || len(inv.QueryOnly) > 0 || len(inv.EntityTypes) > 0 {
		t.Errorf("windowed mode must not carry environment-wide listings: objects=%d buckets=%d queryOnly=%d entities=%d",
			len(inv.DataObjects), len(inv.Buckets), len(inv.QueryOnly), len(inv.EntityTypes))
	}
	if len(inv.Capabilities) > 0 || len(inv.Absent) > 0 {
		t.Error("windowed mode must not emit the retention-scoped capability verdicts")
	}
}

func TestWindowedStaleBoundary(t *testing.T) {
	for _, tc := range []struct {
		name  string
		age   time.Duration
		state SignalState
	}{
		{"just inside", 4*time.Minute + 59*time.Second, SignalLive},
		{"exactly at threshold", 5 * time.Minute, SignalLive},
		{"just past", 5*time.Minute + 1*time.Second, SignalStale},
	} {
		t.Run(tc.name, func(t *testing.T) {
			runner := windowRunner()
			runner.responses = append([]mockResponse{{
				match:   "fetch logs, from:",
				records: []map[string]interface{}{rec("matched", "7", "last_seen", fixedNow.Add(-tc.age).Format(time.RFC3339))},
			}}, runner.responses...)
			inv, err := Discover(context.Background(), runner, windowDefs(), windowOpts())
			if err != nil {
				t.Fatalf("Discover: %v", err)
			}
			if got := signalByName(t, inv, "logs").State; got != tc.state {
				t.Errorf("age %s → %q, want %q", tc.age, got, tc.state)
			}
		})
	}
}

func TestWindowedTruncatedProbeIsUnknownNotAbsent(t *testing.T) {
	runner := windowRunner()
	runner.responses = append([]mockResponse{{
		match: "fetch logs, from:", truncated: true,
		records: []map[string]interface{}{rec("matched", "0")},
	}}, runner.responses...)
	inv, err := Discover(context.Background(), runner, windowDefs(), windowOpts())
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	logs := signalByName(t, inv, "logs")
	if logs.State != SignalUnknown {
		t.Fatalf("truncated probe → %q, want unknown: a cut-short probe that found nothing proves nothing", logs.State)
	}
	if !strings.Contains(logs.Evidence, "cut short") {
		t.Errorf("evidence should say the probe was cut short, got %q", logs.Evidence)
	}
}

func TestWindowedProbeFailureIsUnknown(t *testing.T) {
	runner := windowRunner()
	runner.responses = append([]mockResponse{{
		match: "fetch logs, from:", err: fmt.Errorf("boom"),
	}}, runner.responses...)
	inv, err := Discover(context.Background(), runner, windowDefs(), windowOpts())
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if got := signalByName(t, inv, "logs").State; got != SignalUnknown {
		t.Errorf("failed probe → %q, want unknown", got)
	}
}

func TestWindowedSignalsFilter(t *testing.T) {
	opts := windowOpts()
	opts.Signals = []string{"logs", "SPANS"}
	runner := windowRunner()
	inv, err := Discover(context.Background(), runner, windowDefs(), opts)
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if len(inv.Signals) != 2 {
		t.Fatalf("got %d signals, want 2", len(inv.Signals))
	}
	// Case-insensitive, so --signals SPANS works as typed.
	signalByName(t, inv, "spans")
	// With no metric signal selected the catalog read must be skipped.
	for _, call := range runner.calls {
		if strings.HasPrefix(call, "metrics from:") {
			t.Error("metric catalog read despite no metric signal in scope")
		}
	}
}

func TestAllZeroTripwire(t *testing.T) {
	live := []Signal{{Name: "logs", State: SignalLive}, {Name: "spans", State: SignalEmpty}}
	if note := allZeroTripwire(live); note != "" {
		t.Errorf("tripwire must stay quiet when something is live, got %q", note)
	}

	allEmpty := []Signal{{Name: "logs", State: SignalEmpty}, {Name: "spans", State: SignalEmpty}}
	note := allZeroTripwire(allEmpty)
	if note == "" {
		t.Fatal("tripwire must fire when every signal is empty")
	}
	if !strings.Contains(note, "scope") {
		t.Errorf("tripwire should implicate the scope, got %q", note)
	}

	// One empty signal is not a pattern; absent/unknown are not "empty".
	if n := allZeroTripwire([]Signal{{Name: "logs", State: SignalEmpty}}); n != "" {
		t.Errorf("single empty signal must not trip the wire, got %q", n)
	}
	if n := allZeroTripwire([]Signal{{Name: "a", State: SignalAbsent}, {Name: "b", State: SignalUnknown}}); n != "" {
		t.Errorf("absent/unknown must not trip the wire, got %q", n)
	}

	// States that say nothing about whether the scope is valid must not weigh
	// against the wrong-scope explanation either. Counting no-data as
	// "evaluated but not empty" made the tripwire unfireable on any tenant
	// with one quiet stream, which is nearly all of them.
	withNoData := []Signal{
		{Name: "logs", State: SignalEmpty},
		{Name: "spans", State: SignalEmpty},
		{Name: "synthetic", State: SignalNoData},
	}
	if allZeroTripwire(withNoData) == "" {
		t.Error("a tenant-wide-empty stream must not suppress the tripwire")
	}
	withNA := append(append([]Signal{}, withNoData...), Signal{Name: "rum", State: SignalNotApplicable})
	if allZeroTripwire(withNA) == "" {
		t.Error("an inapplicable signal must not suppress the tripwire")
	}

	// An unknown says nothing about the scope unless it carries a match: a
	// sampled hit too thin to date is unknown and has still proven that the
	// scope selects records. "Every signal matched 0 records" is then false,
	// and the scope is the one thing known to be right.
	for name, matched := range map[string]Signal{
		"a sampled count":       {Name: "logs", State: SignalUnknown, RecordsSampled: 2},
		"an extrapolated count": {Name: "logs", State: SignalUnknown, Records: 200000},
	} {
		if n := allZeroTripwire([]Signal{{Name: "rum", State: SignalEmpty}, {Name: "spans", State: SignalEmpty}, matched}); n != "" {
			t.Errorf("an unknown signal with %s must suppress the tripwire, got %q", name, n)
		}
	}
}

// TestTripwireStaysQuietNextToAThinSampledHit drives the same case through
// Discover: two empty streams and a logs signal whose sample matched records
// but is too thin to date.
func TestTripwireStaysQuietNextToAThinSampledHit(t *testing.T) {
	runner := windowRunner()
	rungs := []mockResponse{cappedLogs(), sampledRung(100000, 2, 200000, at(6, 0))}
	for _, ratio := range []int64{10000, 1000, 100, 10} {
		rungs = append(rungs, sampledRung(ratio, 0, 0, ""))
	}
	// spans matches nothing here, like user.events, and both hold data.
	rungs = append(rungs, mockResponse{match: "fetch spans, from:now()-15m | filter k8s", records: []map[string]interface{}{rec("matched", "0")}})
	runner.responses = append(rungs, runner.responses...)
	opts := windowOpts()
	opts.Signals = []string{"logs", "spans", "rum"}

	inv := discoverWith(t, runner, opts)
	if logs := signalByName(t, inv, "logs"); logs.State != SignalUnknown || logs.RecordsSampled != 2 {
		t.Fatalf("logs state/sampled = %q/%d, want unknown/2", logs.State, logs.RecordsSampled)
	}
	for _, name := range []string{"spans", "rum"} {
		if got := signalByName(t, inv, name).State; got != SignalEmpty {
			t.Fatalf("%s state = %q, want empty", name, got)
		}
	}
	for _, note := range inv.Notes {
		if strings.Contains(note, "every signal matched 0 records") {
			t.Errorf("note %q blames the scope while logs matched records for it", note)
		}
	}
}

func TestTimeFieldOnlyWithDataObject(t *testing.T) {
	err := ValidateDefinitions(map[string]*CapabilityDef{
		"bad": {MetricKey: "dt.*", TimeField: "timestamp"},
	})
	if err == nil {
		t.Fatal("timeField on a non-dataObject shape must be rejected")
	}
}

func TestBuiltinTimeFieldOverrides(t *testing.T) {
	defs := BuiltinDefinitions()
	// spans and user.events carry start_time and no timestamp at all;
	// takeMax(timestamp) there yields an undefined column rather than an
	// error, so the signal would report live with no age and could never go
	// stale. Verified against a live tenant for every built-in stream.
	for _, name := range []string{"spans", "rum"} {
		if got := defs[name].TimeField; got != "start_time" {
			t.Errorf("builtin %s timeField = %q, want start_time", name, got)
		}
	}
	for _, name := range []string{"logs", "bizevents", "security", "davis", "davis-events", "synthetic"} {
		if got := defs[name].TimeField; got != "" {
			t.Errorf("%s carries timestamp and should use the default time field, got %q", name, got)
		}
	}
}

func TestBuiltinDavisViewsDeclareTheirBuckets(t *testing.T) {
	// dt.system.buckets keys records by table, and the davis views are views
	// over `events` that declare no query_string in the catalog. Without an
	// explicit bucket list they lose the empty/no-data discrimination
	// entirely and can only ever report "coverage unknown".
	defs := BuiltinDefinitions()
	for _, name := range []string{"davis", "davis-events"} {
		if len(defs[name].BackingBuckets) == 0 {
			t.Errorf("%s is a view over events and must declare its backing buckets", name)
		}
	}
}

func TestBackingBucketsOnlyWithDataObject(t *testing.T) {
	err := ValidateDefinitions(map[string]*CapabilityDef{
		"bad": {MetricKey: "dt.*", BackingBuckets: []string{"x*"}},
	})
	if err == nil {
		t.Fatal("backingBuckets on a non-dataObject shape must be rejected")
	}
}

func TestScopeFields(t *testing.T) {
	for _, tc := range []struct {
		scope string
		want  []string
	}{
		{`k8s.namespace.name == "payments"`, []string{"k8s.namespace.name"}},
		// Values never contribute field names, even when they look like one.
		{`log.source == "service.name"`, []string{"log.source"}},
		{`a == "x" and b != "y"`, []string{"a", "b"}},
		// Functions are dropped by their trailing paren, not by a builtin list.
		{`matchesPhrase(content, "boom")`, []string{"content"}},
		{`isNotNull(k8s.pod.name)`, []string{"k8s.pod.name"}},
		// Backtick-quoted fields are taken verbatim.
		{"`my-weird field` == \"x\"", []string{"my-weird field"}},
		// Literals and operators are not fields.
		{`true`, nil},
		{`status == "ERROR" or status == "WARN"`, []string{"status"}},
		{``, nil},
		// A duration literal's unit is not a field. The identifier match
		// starts mid-literal (at the "m" of "5m", the "s" of "1.5s") because a
		// bare identifier cannot begin with a digit, and the phantom field
		// that produced reached the user twice: named in the n/a evidence, and
		// forcing every metric dimension probe — which needs a verdict on
		// every field — down to unknown.
		{`timestamp > now()-5m and service.name == "x"`, []string{"timestamp", "service.name"}},
		{`duration > 1.5s`, []string{"duration"}},
	} {
		got := scopeFields(tc.scope)
		if strings.Join(got, ",") != strings.Join(tc.want, ",") {
			t.Errorf("scopeFields(%q) = %v, want %v", tc.scope, got, tc.want)
		}
	}
}

// TestMatchedWithoutEventTimeSaysSo covers the failure a wrong TimeField
// produces: takeMax over a field the stream does not carry yields an undefined
// column rather than an error, so the signal reports "live" with no age and can
// never reach "stale" — the one state this feature exists to surface. Silence
// there would present a broken probe as a clean verdict.
func TestMatchedWithoutEventTimeSaysSo(t *testing.T) {
	var sig Signal
	applyFreshness(&sig, "", "timestamp", time.Now(), 2*time.Minute)
	if sig.State != SignalLive {
		t.Errorf("state = %q, want live: records did match", sig.State)
	}
	if sig.AgeSeconds != 0 || sig.LastSeen != "" {
		t.Errorf("no event time must not invent an age: %+v", sig)
	}
	for _, want := range []string{"takeMax(timestamp)", "cannot be reported stale"} {
		if !strings.Contains(sig.Evidence, want) {
			t.Errorf("evidence %q should mention %q", sig.Evidence, want)
		}
	}

	// A metric family reads its event time from the timeseries frame, not a
	// record field, so the evidence must not name a field that does not exist.
	var metric Signal
	applyFreshness(&metric, "", "", time.Now(), 2*time.Minute)
	if !strings.Contains(metric.Evidence, "the timeseries frame") {
		t.Errorf("metric evidence should name the frame, got %q", metric.Evidence)
	}

	// The normal path stays clean: a live signal carries no evidence, so the
	// human renderer's evidence block keeps listing only what needs explaining.
	var live Signal
	applyFreshness(&live, time.Now().UTC().Format(time.RFC3339), "timestamp", time.Now(), 2*time.Minute)
	if live.Evidence != "" {
		t.Errorf("a live signal with an age needs no evidence, got %q", live.Evidence)
	}
}

func TestMetricDimensionApplicability(t *testing.T) {
	fields := []string{"k8s.namespace.name"}
	// "undefined" is Grail's way of saying the metric has no such dimension.
	if got := metricDimensionApplicability(map[string]string{"k8s.namespace.name": TypeUndefined}, fields); got != applicabilityNo {
		t.Errorf("an undefined dimension should read as not-applicable, got %v", got)
	}
	if got := metricDimensionApplicability(map[string]string{"k8s.namespace.name": "string"}, fields); got != applicabilityYes {
		t.Errorf("a typed dimension should read as applicable, got %v", got)
	}
	// One real dimension is enough to make the question askable.
	two := []string{"k8s.namespace.name", "host.name"}
	types := map[string]string{"k8s.namespace.name": TypeUndefined, "host.name": "string"}
	if got := metricDimensionApplicability(types, two); got != applicabilityYes {
		t.Errorf("one existing dimension should make the scope applicable, got %v", got)
	}
	// Missing type information is never an absence claim.
	if got := metricDimensionApplicability(nil, fields); got != applicabilityUnknown {
		t.Errorf("absent type metadata must yield unknown, got %v", got)
	}
	if got := metricDimensionApplicability(map[string]string{"other": "string"}, fields); got != applicabilityUnknown {
		t.Errorf("a field the probe did not report on must yield unknown, got %v", got)
	}
}

func TestMetricFamilyThatCannotCarryTheScope(t *testing.T) {
	defs := map[string]*CapabilityDef{"host-metrics": {MetricKey: "dt.host.*"}}
	runner := &mockRunner{responses: []mockResponse{
		{match: "dt.system.data_objects", records: []map[string]interface{}{
			rec("name", "metrics", "fetchable", false, "type", "table"),
		}},
		{match: "dt.system.buckets"},
		{match: "metrics from:", records: []map[string]interface{}{rec("metric.key", "dt.host.cpu.usage")}},
		// The grouping probe is the applicability test; it must precede the
		// scoped one in the fixture because both start with "timeseries".
		{match: "by:{`k8s.namespace.name`}", types: map[string]string{"k8s.namespace.name": TypeUndefined}},
		{match: "timeseries"}, // scoped probe: no rows, as a live tenant returns
	}}
	inv, err := Discover(context.Background(), runner, defs, windowOpts())
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	sig := signalByName(t, inv, "host-metrics")
	if sig.State != SignalNotApplicable {
		t.Fatalf("dt.host.* has no k8s.namespace.name dimension, so the state should be n/a, got %q (%s)", sig.State, sig.Evidence)
	}
	// The grouping probe scans nothing, which is what makes it affordable.
	found := false
	for _, c := range runner.calls {
		if strings.Contains(c, "by:{`k8s.namespace.name`}") {
			found = true
		}
	}
	if !found {
		t.Errorf("expected a by:{} dimension probe, calls: %v", runner.calls)
	}
}

func TestQuietStreamIsNotCalledNotApplicable(t *testing.T) {
	// A stream that received nothing at all in the window cannot answer
	// whether it carries the scope's fields — the isNotNull probe misses for
	// the same reason the scoped probe did. Reporting n/a here would invent a
	// schema claim out of a quiet window.
	defs := map[string]*CapabilityDef{"logs": {DataObject: "logs"}}
	runner := &mockRunner{responses: []mockResponse{
		{match: "dt.system.data_objects", records: []map[string]interface{}{
			rec("name", "logs", "fetchable", true, "type", "table"),
		}},
		{match: "dt.system.buckets", records: []map[string]interface{}{
			rec("name", "default_logs", "dt.system.table", "logs", "records", float64(5000), "has_access", true),
		}},
		{match: "| filter isNotNull", records: []map[string]interface{}{rec("present", "0")}},
		{match: "| limit 1 | summarize any", records: []map[string]interface{}{rec("any", "0")}},
		{match: "fetch logs, from:", records: []map[string]interface{}{rec("matched", "0")}},
	}}
	inv, err := Discover(context.Background(), runner, defs, windowOpts())
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if got := signalByName(t, inv, "logs").State; got != SignalEmpty {
		t.Errorf("a quiet window must stay empty, not become n/a: got %q", got)
	}
}

func TestNoDataSkipsTheApplicabilityProbe(t *testing.T) {
	// A stream with no records within retention is already fully explained,
	// and asking it about the scope's fields could only mislead.
	runner := windowRunner()
	if _, err := Discover(context.Background(), runner, windowDefs(), windowOpts()); err != nil {
		t.Fatalf("Discover: %v", err)
	}
	for _, c := range runner.calls {
		if strings.Contains(c, "fetch bizevents") && strings.Contains(c, "isNotNull") {
			t.Errorf("no-data stream must not be probed for applicability: %s", c)
		}
	}
}

func TestViewRetentionResolvesThroughBuckets(t *testing.T) {
	// dt.davis.problems is a view over `events` with no table of its own, so
	// without bucket resolution its retention coverage is simply unknown and
	// empty/no-data collapses.
	facts := discoveredFacts{
		streamRows:  map[string]int64{"events": 900},
		bucketRows:  map[string]int64{"default_davis_problems": 40, "default_logs": 7},
		viewBuckets: map[string][]string{"dt.davis.problems": {"default_davis*"}},
		viewTable:   map[string]string{"dt.davis.problems": "events"},
	}
	cov := retentionFor("dt.davis.problems", facts)
	if !cov.known || cov.rows != 40 || !cov.exact {
		t.Fatalf("buckets should give an exact figure for the view: %+v", cov)
	}
	state, ev := emptyState("dt.davis.problems", cov)
	if state != SignalEmpty || !strings.Contains(ev, "the stream works") {
		t.Errorf("a live view should support the strong empty claim: %q / %q", state, ev)
	}

	// Without declared buckets the backing table is all there is, and it is a
	// superset — so the strong claim must not be made.
	noBuckets := facts
	noBuckets.viewBuckets = nil
	cov = retentionFor("dt.davis.problems", noBuckets)
	if !cov.known || cov.exact {
		t.Fatalf("the backing table is a superset, not an exact figure: %+v", cov)
	}
	_, ev = emptyState("dt.davis.problems", cov)
	if strings.Contains(ev, "the stream works") {
		t.Errorf("a superset count must not be presented as the view's own liveness: %q", ev)
	}
	if !strings.Contains(ev, "superset") {
		t.Errorf("the evidence should say the figure is a superset: %q", ev)
	}

	// An empty backing is conclusive in the other direction.
	empty := facts
	empty.bucketRows = map[string]int64{"default_davis_problems": 0}
	state, _ = emptyState("dt.davis.problems", retentionFor("dt.davis.problems", empty))
	if state != SignalNoData {
		t.Errorf("empty buckets mean the view holds nothing: got %q", state)
	}
}

func TestViewBacking(t *testing.T) {
	buckets, table := viewBacking(`// Bucket filters are added for performance
// Custom bucket filter: 'davis*'
fetch events, scanLimitGBytes:-1, bucket: {"default_davis*", "davis*"}
| filter event.kind == "DAVIS_PROBLEM"`)
	if strings.Join(buckets, ",") != "default_davis*,davis*" {
		t.Errorf("buckets = %v", buckets)
	}
	if table != "events" {
		t.Errorf("table = %q, want events", table)
	}

	// A view that names no buckets still resolves to its table.
	buckets, table = viewBacking(`fetch user.sessions`)
	if len(buckets) != 0 || table != "user.sessions" {
		t.Errorf("buckets = %v, table = %q", buckets, table)
	}
}

func TestScopeSyntaxErrorAbortsTheWholeRun(t *testing.T) {
	// A scope that does not parse fails identically for every signal. Marking
	// each one "unknown" would burn the budget, print the same parse error a
	// dozen times, and dress a typo up as an inconclusive environment.
	runner := windowRunner()
	runner.responses = append([]mockResponse{{
		match: "fetch logs, from:",
		err:   fmt.Errorf(`{"errorType":"PARSE_ERROR","errorMessage":"` + "`" + `(` + "`" + ` isn't allowed here"}`),
	}}, runner.responses...)
	_, err := Discover(context.Background(), runner, windowDefs(), windowOpts())
	if err == nil {
		t.Fatal("a scope that does not parse must abort the run, not yield unknowns")
	}
	if !strings.Contains(err.Error(), "--scope") {
		t.Errorf("the error should point at the scope the user wrote: %v", err)
	}
	for _, c := range runner.calls {
		if strings.Contains(c, "fetch user.events") {
			t.Errorf("probing must stop at the first parse failure, but continued: %v", runner.calls)
		}
	}
}

func TestSignalNames(t *testing.T) {
	names := SignalNames(windowDefs())
	if strings.Join(names, ",") != "bizevents,k8smetric,logs,rum,security,spans,synthetic" {
		t.Errorf("SignalNames = %v", names)
	}
	// Entity-census and probe shapes have no windowed reading and are not
	// signal names a caller may require.
	for _, n := range names {
		if n == "hosts" || n == "genai" {
			t.Errorf("%q is not a signal type", n)
		}
	}
}

// TestTruncatedEvidenceNamesItsRemedy pins the distinction that matters on a
// high-volume tenant: the three truncation causes have different fixes, and a
// scan cap in particular is a dtctl setting rather than a finding about the
// customer's data. A message that lists all three leaves the reader to guess.
func TestTruncatedEvidenceNamesItsRemedy(t *testing.T) {
	tests := []struct {
		name        string
		cause       TruncationCause
		scanLimitGB float64
		want        []string
		notWant     []string
	}{
		{
			name:        "scan cap names the cap and the flag",
			cause:       TruncationScanLimit,
			scanLimitGB: 25,
			want:        []string{"spans", "25 GB scan cap", "the last 15m", "--scan-limit-gbytes"},
		},
		{
			name:    "scan cap stays honest when the cap is unknown",
			cause:   TruncationScanLimit,
			want:    []string{"the scan cap"},
			notWant: []string{"0 GB"},
		},
		{
			name:    "timeout does not advise raising the scan cap",
			cause:   TruncationTimeout,
			want:    []string{"timed out", "--since"},
			notWant: []string{"--scan-limit-gbytes"},
		},
		{
			name:  "an unrecognised cause still degrades to something actionable",
			cause: "",
			want:  []string{"cut short by a limit"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := truncatedEvidence("spans", tt.cause, "now()-15m", tt.scanLimitGB)
			for _, w := range tt.want {
				if !strings.Contains(got, w) {
					t.Errorf("evidence %q should contain %q", got, w)
				}
			}
			for _, w := range tt.notWant {
				if strings.Contains(got, w) {
					t.Errorf("evidence %q should not contain %q", got, w)
				}
			}
		})
	}
}

// TestNotApplicableClaimStrength guards the asymmetry between the two n/a
// probes. A metric dimension is resolved against the metric definition, so
// "undefined" really is structural; a stream is only sampled over the window,
// so the same word there would be a claim the evidence does not support.
func TestNotApplicableClaimStrength(t *testing.T) {
	fields := []string{"k8s.namespace.name"}

	stream := notApplicableEvidenceStream("user.events", fields, "now()-15m")
	if !strings.Contains(stream, "the last 15m") {
		t.Errorf("stream n/a must scope its claim to the window, got %q", stream)
	}
	if strings.Contains(stream, "structurally") {
		t.Errorf("stream n/a must not claim a structural absence, got %q", stream)
	}

	// Every key in the family was asked, so the family-wide structural claim
	// is earned.
	metric := notApplicableEvidenceMetric("dt.kubernetes.*", 3, 3, true, fields)
	if !strings.Contains(metric, "structurally") || !strings.Contains(metric, "not a dimension") {
		t.Errorf("metric n/a may and should claim a structural absence, got %q", metric)
	}
	if strings.Contains(metric, "widen --since") {
		t.Errorf("metric n/a needs no wider window to be sound, got %q", metric)
	}

	// Sampled: 3 keys of 46 were asked. "undefined" is still structural for
	// those three, but a family need not be homogeneous, so the line must not
	// generalise to the family it did not probe.
	sampled := notApplicableEvidenceMetric("dt.process.*", 3, 46, false, fields)
	if strings.Contains(sampled, "structurally cannot carry this scope") {
		t.Errorf("a 3-of-46 sample must not claim the whole family, got %q", sampled)
	}
	for _, want := range []string{"3 keys sampled", "46 in the family", "the rest of the family was not asked"} {
		if !strings.Contains(sampled, want) {
			t.Errorf("sampled metric n/a must say what it checked (%q), got %q", want, sampled)
		}
	}
}

// TestMetricSampleAdviceIsRunnable guards a defect the live runs surfaced: the
// sampled-family "unknown" evidence used to tell the reader to "probe a
// specific key with --signals", but --signals only accepts capability names, so
// following the advice returned "unknown signal <metric key>". Evidence that
// names a remedy has to name one the CLI accepts.
func TestMetricSampleAdviceIsRunnable(t *testing.T) {
	// More keys than metricKeySampleSize, so the value probes cover a sample
	// rather than the family and the verdict has to be "unknown".
	keys := make([]string, 0, metricKeySampleSize+2)
	for i := 0; i < metricKeySampleSize+2; i++ {
		keys = append(keys, fmt.Sprintf("dt.process.k%02d", i))
	}
	facts := discoveredFacts{metricsOK: true, metricKeys: keys}
	// Every probe reports no datapoints, and the dimension probe cannot settle
	// applicability, so the family lands on the sampled "unknown".
	// Every probe returns an empty result, so no key reports datapoints and the
	// dimension probe (no ColumnTypes) cannot settle applicability either.
	b := &budgetRunner{runner: &mockRunner{}, report: &Report{}, queries: 100, seconds: 100}
	opts := DiscoverOptions{Since: "now()-15m", Scope: `k8s.namespace.name == "x"`}
	sig, err := b.probeMetricSignal(context.Background(), "process-metrics",
		&CapabilityDef{MetricKey: "dt.process.*"}, facts, opts, []string{"k8s.namespace.name"}, time.Now())
	if err != nil {
		t.Fatalf("probeMetricSignal: %v", err)
	}
	if strings.Contains(sig.Evidence, "--signals") {
		t.Errorf("--signals takes capability names, not metric keys, so it cannot be the remedy here: %q", sig.Evidence)
	}
	if !strings.Contains(sig.Evidence, "dtctl query") {
		t.Errorf("evidence should name a runnable remedy, got %q", sig.Evidence)
	}
}

// TestWindowLabelReadsAsProse keeps the DQL timeframe out of user-facing
// sentences without mangling a shape it does not recognise.
func TestWindowLabelReadsAsProse(t *testing.T) {
	if got := windowLabel("now()-15m"); got != "the last 15m" {
		t.Errorf("windowLabel(now()-15m) = %q", got)
	}
	if got := windowLabel("2026-09-11T00:00:00Z"); got != "2026-09-11T00:00:00Z" {
		t.Errorf("windowLabel should pass through an absolute timeframe, got %q", got)
	}
}

// --- Sampled recovery from the scan cap -------------------------------------

// cappedLogs makes the exhaustive logs probe hit the scan cap, which is the
// only condition the sampled fallback is meant to rescue.
func cappedLogs() mockResponse {
	return mockResponse{
		match: "fetch logs, from:now()-15m | filter", truncated: true, cause: TruncationScanLimit,
	}
}

// decades are the five ratios the walk asks for, most aggressive first. They
// are written out here rather than read from samplingLadder, so that a rung
// removed from the ladder fails the tests instead of shrinking them.
var decades = []int64{100000, 10000, 1000, 100, 10}

// rungMatch is the text that identifies the rung asking for one ratio. The
// delimiter is part of it: without one, the match for 1-in-10 is a prefix of
// every other rung's query and would answer for whichever of them a test left
// out.
func rungMatch(ratio int64) string {
	return fmt.Sprintf("samplingRatio:%d |", ratio)
}

// sampledRung is a response for one rung of the ladder. population is what
// sum(dt.system.sampling_ratio) returns, which is what the extrapolation must
// be read from.
func sampledRung(ratio int64, matched, population int64, lastSeen string) mockResponse {
	return mockResponse{
		match:   rungMatch(ratio),
		sampled: true,
		records: []map[string]interface{}{
			rec("matched", matched, "population", population, "last_seen", lastSeen),
		},
	}
}

func sampledCalls(runner *mockRunner) []string {
	var out []string
	for _, c := range runner.calls {
		if strings.Contains(c, "samplingRatio:") {
			out = append(out, c)
		}
	}
	return out
}

// unlistedRungs returns the sampled probes the runner answered without a
// response written for them. Such a probe falls through to the exhaustive
// fixture of its stream, which answers as an unflagged hit and so ends the
// walk as a refusal: a test that leaves a rung out would pass for a reason it
// does not state.
func unlistedRungs(runner *mockRunner) []string {
	var out []string
calls:
	for _, c := range sampledCalls(runner) {
		for _, r := range runner.responses {
			if strings.Contains(r.match, "samplingRatio:") && strings.Contains(c, r.match) {
				continue calls
			}
		}
		out = append(out, c)
	}
	return out
}

func assertEveryRungListed(t *testing.T, runner *mockRunner) {
	t.Helper()
	for _, c := range unlistedRungs(runner) {
		t.Errorf("the walk ran a sampled probe this test lists no response for: %s", c)
	}
}

// discoverWith runs Discover over the windowed definitions and fails the test
// when the walk asked for a rung the test did not list.
func discoverWith(t *testing.T, runner *mockRunner, opts DiscoverOptions) *Inventory {
	t.Helper()
	inv, err := Discover(context.Background(), runner, windowDefs(), opts)
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	assertEveryRungListed(t, runner)
	return inv
}

// TestWalkAsksTheFiveDecadesInOrder pins the ladder itself. Grail rounds every
// ratio down to a power of ten and caps it at 100000, so these five are all
// the rungs there are, and a walk that skips one gives up a tenfold step in
// precision without any test of a verdict noticing.
func TestWalkAsksTheFiveDecadesInOrder(t *testing.T) {
	runner := windowRunner()
	runner.responses = append([]mockResponse{
		cappedLogs(),
		sampledRung(100000, 0, 0, ""), sampledRung(10000, 0, 0, ""), sampledRung(1000, 0, 0, ""),
		sampledRung(100, 0, 0, ""), sampledRung(10, 0, 0, ""),
	}, runner.responses...)

	discoverWith(t, runner, windowOpts())
	want := []string{
		"fetch logs, from:now()-15m, samplingRatio:100000 | filter",
		"fetch logs, from:now()-15m, samplingRatio:10000 | filter",
		"fetch logs, from:now()-15m, samplingRatio:1000 | filter",
		"fetch logs, from:now()-15m, samplingRatio:100 | filter",
		"fetch logs, from:now()-15m, samplingRatio:10 | filter",
	}
	got := sampledCalls(runner)
	if len(got) != len(want) {
		t.Fatalf("ran %d sampled probes, want %d:\n%s", len(got), len(want), strings.Join(got, "\n"))
	}
	for i := range want {
		if !strings.HasPrefix(got[i], want[i]) {
			t.Errorf("sampled probe %d = %q, want it to start with %q", i+1, got[i], want[i])
		}
	}
}

// TestWalkEndsAtThirtyMatchesAndNotBefore pins the floor from both sides, on
// a hit whose newest record is past the stale threshold. Thirty matches are
// enough to date it, so the walk ends there and says stale; one fewer is not,
// so it runs every remaining rung to thicken the sample and, as none does,
// ends on that hit without a verdict.
func TestWalkEndsAtThirtyMatchesAndNotBefore(t *testing.T) {
	below := []mockResponse{
		sampledRung(10000, 0, 0, ""), sampledRung(1000, 0, 0, ""),
		sampledRung(100, 0, 0, ""), sampledRung(10, 0, 0, ""),
	}
	for _, tc := range []struct {
		matched int64
		calls   int
		state   SignalState
	}{
		{30, 1, SignalStale},
		{29, 5, SignalUnknown},
	} {
		t.Run(fmt.Sprintf("%d matches", tc.matched), func(t *testing.T) {
			runner := windowRunner()
			rungs := append([]mockResponse{cappedLogs(), sampledRung(100000, tc.matched, tc.matched*100000, at(9, 0))}, below...)
			runner.responses = append(rungs, runner.responses...)

			logs := signalByName(t, discoverWith(t, runner, windowOpts()), "logs")
			if got := len(sampledCalls(runner)); got != tc.calls {
				t.Errorf("ran %d sampled probes, want %d", got, tc.calls)
			}
			if logs.State != tc.state || logs.RecordsSampled != tc.matched || logs.SamplingRatio != 100000 {
				t.Errorf("state/sampled/ratio = %q/%d/%d, want %q/%d/100000 (evidence: %s)",
					logs.State, logs.RecordsSampled, logs.SamplingRatio, tc.state, tc.matched, logs.Evidence)
			}
		})
	}
}

// TestSampledProbeRescuesCappedSignal is the whole point of the fallback: a
// signal the cap made unanswerable gets a real verdict, for a scan small
// enough that the cap never comes into it.
func TestSampledProbeRescuesCappedSignal(t *testing.T) {
	runner := windowRunner()
	runner.responses = append([]mockResponse{
		cappedLogs(),
		// 96 sampled records at 1-in-100000 → about 9.6M in the window.
		sampledRung(100000, 96, 9600000, fixedNow.Add(-30*time.Second).Format(time.RFC3339)),
		sampledRung(10000, 968, 9680000, fixedNow.Add(-20*time.Second).Format(time.RFC3339)),
	}, runner.responses...)

	logs := signalByName(t, discoverWith(t, runner, windowOpts()), "logs")
	if logs.State != SignalLive {
		t.Fatalf("sampled hit → %q, want live: sampling only ever drops records, so a match still proves arrival (evidence: %s)", logs.State, logs.Evidence)
	}
	if logs.Truncation != "" {
		t.Errorf("Truncation = %q, want empty: the signal was resolved, so it must not still read as capped", logs.Truncation)
	}
	if logs.Records != 9600000 || logs.SamplingRatio != 100000 || logs.RecordsSampled != 96 {
		t.Errorf("records/ratio/sampled = %d/%d/%d, want 9600000/100000/96", logs.Records, logs.SamplingRatio, logs.RecordsSampled)
	}
	for _, want := range []string{"1-in-100k", "extrapolate to about", "biased"} {
		if !strings.Contains(logs.Evidence, want) {
			t.Errorf("evidence %q missing %q: a sampled verdict has to disclose that it is sampled", logs.Evidence, want)
		}
	}
}

// TestSampledProbeStopsAtFirstConclusiveRung pins the cost model. A hit is
// conclusive at any ratio, so the walk must not keep descending "for
// accuracy" — each further rung scans about ten times as much.
func TestSampledProbeStopsAtFirstConclusiveRung(t *testing.T) {
	runner := windowRunner()
	runner.responses = append([]mockResponse{
		cappedLogs(),
		sampledRung(100000, 96, 9600000, fixedNow.Format(time.RFC3339)),
		sampledRung(10000, 968, 9680000, fixedNow.Format(time.RFC3339)),
		sampledRung(1000, 9688, 9688000, fixedNow.Format(time.RFC3339)),
	}, runner.responses...)

	discoverWith(t, runner, windowOpts())
	if got := sampledCalls(runner); len(got) != 1 {
		t.Errorf("ran %d sampled probes, want 1: the first rung already answered, and every rung below it costs ~10x more\n%v", len(got), got)
	}
}

// TestThinSampleDescendsForATrustworthyAge is the "reduce until it is good
// enough" half, and it is about freshness rather than about the count: a
// handful of sampled records whose newest is past the stale threshold cannot
// say whether the source stopped, so the walk asks a finer rung.
func TestThinSampleDescendsForATrustworthyAge(t *testing.T) {
	runner := windowRunner()
	runner.responses = append([]mockResponse{
		cappedLogs(),
		// Two records are a hit, but six minutes against a five minute
		// threshold is not a verdict a sample this thin can give.
		sampledRung(100000, 2, 200000, at(6, 0)),
		sampledRung(10000, 40, 400000, at(0, 10)),
	}, runner.responses...)

	logs := signalByName(t, discoverWith(t, runner, windowOpts()), "logs")
	if logs.State != SignalLive || logs.RecordsSampled != 40 || logs.SamplingRatio != 10000 {
		t.Errorf("settled on %q with %d records at 1-in-%d, want live with the thicker 40 at 1-in-10000", logs.State, logs.RecordsSampled, logs.SamplingRatio)
	}
	if got := len(sampledCalls(runner)); got != 2 {
		t.Errorf("ran %d sampled probes, want 2: one to find the signal, one to make its age trustworthy", got)
	}
}

// TestThinSampleInsideTheThresholdEndsTheWalk: a sample is a subset, so its
// newest record can only be older than the newest in the window. Once a hit is
// held and a record inside the stale threshold has been seen, the verdict is
// live and no finer rung can change it. Each of those rungs reads ten times
// more than the last, so the walk does not ask them. The floor withholds only
// the stale verdict: the count stays the thin estimate it is labelled as.
func TestThinSampleInsideTheThresholdEndsTheWalk(t *testing.T) {
	capped := cappedLogs()
	capped.records = []map[string]interface{}{rec("matched", int64(700), "last_seen", at(0, 3))}
	for _, tc := range []struct {
		name     string
		capped   mockResponse
		rungs    []mockResponse
		calls    int
		lastSeen string
	}{
		{
			name:   "the thin hit is fresh itself",
			capped: cappedLogs(),
			rungs: []mockResponse{
				sampledRung(100000, 2, 200000, at(1, 0)),
				sampledRung(10000, 40, 400000, at(0, 10)),
			},
			calls: 1, lastSeen: at(1, 0),
		},
		{
			name:   "the threshold itself is still inside",
			capped: cappedLogs(),
			rungs: []mockResponse{
				sampledRung(100000, 2, 200000, at(5, 0)),
				sampledRung(10000, 40, 400000, at(0, 10)),
			},
			calls: 1, lastSeen: at(5, 0),
		},
		{
			// No hit yet when the fresh record is seen, so the walk goes on
			// until a rung supplies a count, and ends there.
			name:   "the capped probe matched the fresh record",
			capped: capped,
			rungs: []mockResponse{
				sampledRung(100000, 0, 0, ""),
				sampledRung(10000, 2, 20000, at(9, 0)),
				sampledRung(1000, 40, 40000, at(0, 10)),
			},
			calls: 2, lastSeen: at(0, 3),
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			runner := windowRunner()
			runner.responses = append(append([]mockResponse{tc.capped}, tc.rungs...), runner.responses...)

			logs := signalByName(t, discoverWith(t, runner, windowOpts()), "logs")
			if got := len(sampledCalls(runner)); got != tc.calls {
				t.Errorf("ran %d sampled probes, want %d: the signal was live and a finer rung could not change that", got, tc.calls)
			}
			if logs.State != SignalLive || logs.RecordsSampled != 2 || logs.LastSeen != tc.lastSeen {
				t.Errorf("state/sampled/lastSeen = %q/%d/%q, want live/2/%q (evidence: %s)", logs.State, logs.RecordsSampled, logs.LastSeen, tc.lastSeen, logs.Evidence)
			}
		})
	}
}

// TestThinSampleSurvivesACappedNextRung: descending for precision must not be
// able to lose an answer that was already conclusive.
func TestThinSampleSurvivesACappedNextRung(t *testing.T) {
	runner := windowRunner()
	thin := sampledRung(100000, 2, 200000, at(6, 0))
	capped := sampledRung(10000, 0, 0, "")
	capped.truncated = true
	capped.cause = TruncationScanLimit
	runner.responses = append([]mockResponse{cappedLogs(), thin, capped}, runner.responses...)

	// Six minutes old is past the threshold, so the hit cannot be dated and
	// the state is unknown. What must survive is the arrival it proved.
	logs := signalByName(t, discoverWith(t, runner, windowOpts()), "logs")
	if logs.State != SignalUnknown || logs.RecordsSampled != 2 || logs.Records != 200000 || logs.Truncation != "" {
		t.Errorf("state/sampled/records/truncation = %q/%d/%d/%q, want unknown/2/200000 and no truncation: the thin rung had already proven arrival (evidence: %s)",
			logs.State, logs.RecordsSampled, logs.Records, logs.Truncation, logs.Evidence)
	}
	// Every rung below a capped one reads ten times more, so the walk ends on
	// it instead of asking them.
	if got := len(sampledCalls(runner)); got != 2 {
		t.Errorf("ran %d sampled probes, want 2: a sampled rung that hit the cap ends the walk", got)
	}
}

// TestThickestHitSuppliesTheCount: the walk descends to thicken the sample,
// so a later rung that matched fewer records than one already held is not an
// improvement. Ending on it would extrapolate the volume from the thinner
// sample and state the wider bias.
func TestThickestHitSuppliesTheCount(t *testing.T) {
	runner := windowRunner()
	capped := sampledRung(1000, 0, 0, "")
	capped.truncated, capped.cause = true, TruncationScanLimit
	runner.responses = append([]mockResponse{
		cappedLogs(),
		sampledRung(100000, 20, 2000000, at(6, 0)),
		sampledRung(10000, 2, 20000, at(7, 0)),
		capped,
	}, runner.responses...)

	logs := signalByName(t, discoverWith(t, runner, windowOpts()), "logs")
	if logs.RecordsSampled != 20 || logs.SamplingRatio != 100000 || logs.Records != 2000000 {
		t.Errorf("sampled/ratio/records = %d/%d/%d, want 20/100000/2000000: the first rung held the thicker sample",
			logs.RecordsSampled, logs.SamplingRatio, logs.Records)
	}
	if got := len(sampledCalls(runner)); got != 3 {
		t.Errorf("ran %d sampled probes, want 3", got)
	}
}

// TestSampledZeroIsNeverEmpty is the guard on the failure this package exists
// to prevent. A sampled miss is a bound, not an absence — reporting it as
// "empty" would blame a source for a question that was only partly asked.
func TestSampledZeroIsNeverEmpty(t *testing.T) {
	runner := windowRunner()
	rungs := []mockResponse{cappedLogs()}
	for _, r := range decades {
		rungs = append(rungs, sampledRung(r, 0, 0, ""))
	}
	runner.responses = append(rungs, runner.responses...)

	logs := signalByName(t, discoverWith(t, runner, windowOpts()), "logs")
	if logs.State != SignalUnknown {
		t.Fatalf("sampled zero → %q, want unknown: not finding a match in 1-in-10 of the window is not absence", logs.State)
	}
	// Still capped as far as a structured caller is concerned: the sampled
	// miss bounded the scope, it did not count it.
	if logs.Truncation != TruncationScanLimit {
		t.Errorf("Truncation = %q, want %q", logs.Truncation, TruncationScanLimit)
	}
	// The bound must come from the *least* aggressive rung reached, since that
	// is the tightest one.
	if !strings.Contains(logs.Evidence, "1-in-10 matched nothing") || !strings.Contains(logs.Evidence, "under ~30 records") {
		t.Errorf("evidence %q should quantify what the miss rules out at the tightest rung", logs.Evidence)
	}
	if strings.Contains(logs.Evidence, "absent") {
		t.Errorf("evidence %q must not read as an absence claim", logs.Evidence)
	}
}

// TestSamplingRefusalIsNotRetried: Grail declines to sample several data
// objects and says so in a warning on a 200, returning the full unsampled
// result. Walking the rest of the ladder would re-run the same capped query
// four more times.
func TestSamplingRefusalIsNotRetried(t *testing.T) {
	runner := windowRunner()
	refused := sampledRung(100000, 500, 500, "")
	refused.sampled = false
	runner.responses = append([]mockResponse{cappedLogs(), refused}, runner.responses...)

	logs := signalByName(t, discoverWith(t, runner, windowOpts()), "logs")
	if logs.State != SignalUnknown || logs.Truncation != TruncationScanLimit {
		t.Errorf("state/truncation = %q/%q, want unknown/scan_limit", logs.State, logs.Truncation)
	}
	if logs.SamplingRatio != 0 || logs.Records != 0 {
		t.Errorf("ratio/records = %d/%d, want 0/0: nothing was sampled, so there is nothing to extrapolate", logs.SamplingRatio, logs.Records)
	}
	if !strings.Contains(logs.Evidence, "does not support sampling") {
		t.Errorf("evidence %q should say why the fallback did not apply", logs.Evidence)
	}
	if got := len(sampledCalls(runner)); got != 1 {
		t.Errorf("ran %d sampled probes, want 1: a refusal applies to every rung", got)
	}
}

// unflaggedEmptyRung is a rung that matched nothing and whose response carries
// no sampled flag. A sample too thin to read a record can come back this way,
// so on its own it says nothing about whether the environment samples the
// object. The summarize row is there in one shape and missing in the other.
func unflaggedEmptyRung(ratio int64, withRow bool) mockResponse {
	r := mockResponse{match: rungMatch(ratio)}
	if withRow {
		r.records = []map[string]interface{}{rec("matched", int64(0))}
	}
	return r
}

// refusedRung is a rung the environment answered without sampling: the full
// result, with matches, and no sampled flag.
func refusedRung(ratio, matched int64) mockResponse {
	r := sampledRung(ratio, matched, matched, "")
	r.sampled = false
	return r
}

// TestUnflaggedEmptyRungDoesNotEndTheWalk: the coarsest rung of a capped
// signal often reads no record at all, and its response then lacks the sampled
// flag. That is an empty rung, not a refusal. Ending the walk there left the
// signal unknown one query in, with evidence blaming the environment, while
// the next rung would have found the data.
func TestUnflaggedEmptyRungDoesNotEndTheWalk(t *testing.T) {
	for name, withRow := range map[string]bool{"no record": false, "a zero count": true} {
		t.Run(name, func(t *testing.T) {
			runner := windowRunner()
			runner.responses = append([]mockResponse{
				cappedLogs(),
				unflaggedEmptyRung(100000, withRow),
				sampledRung(10000, 96, 960000, fixedNow.Add(-30*time.Second).Format(time.RFC3339)),
			}, runner.responses...)

			logs := signalByName(t, discoverWith(t, runner, windowOpts()), "logs")
			if got := len(sampledCalls(runner)); got != 2 {
				t.Errorf("ran %d sampled probes, want 2: an empty rung is followed by the next one, and the hit there ends the walk", got)
			}
			if logs.State != SignalLive || logs.Truncation != "" {
				t.Fatalf("state/truncation = %q/%q, want live with no truncation (evidence: %s)", logs.State, logs.Truncation, logs.Evidence)
			}
			if logs.Records != 960000 || logs.SamplingRatio != 10000 || logs.RecordsSampled != 96 {
				t.Errorf("records/ratio/sampled = %d/%d/%d, want 960000/10000/96", logs.Records, logs.SamplingRatio, logs.RecordsSampled)
			}
			if strings.Contains(logs.Evidence, "does not support sampling") {
				t.Errorf("evidence %q blames the environment for a rung that merely read nothing", logs.Evidence)
			}
		})
	}
}

// TestUnflaggedEmptyRungsKeepTheCappedVerdict: when no rung matched and none
// reported that it sampled, nothing was learned either way. The signal keeps
// the capped verdict: it must not claim a sampled bound that no response
// confirmed, nor that the environment refuses to sample.
func TestUnflaggedEmptyRungsKeepTheCappedVerdict(t *testing.T) {
	runner := windowRunner()
	rungs := []mockResponse{cappedLogs()}
	for _, r := range decades {
		rungs = append(rungs, unflaggedEmptyRung(r, true))
	}
	runner.responses = append(rungs, runner.responses...)
	opts := windowOpts()

	logs := signalByName(t, discoverWith(t, runner, opts), "logs")
	if got := len(sampledCalls(runner)); got != 5 {
		t.Errorf("ran %d sampled probes, want 5: no rung gave a reason to stop", got)
	}
	if logs.State != SignalUnknown || logs.Truncation != TruncationScanLimit {
		t.Errorf("state/truncation = %q/%q, want unknown/scan_limit", logs.State, logs.Truncation)
	}
	if want := truncatedEvidence("logs", TruncationScanLimit, opts.Since, opts.ScanLimitGBytes); logs.Evidence != want {
		t.Errorf("evidence = %q, want the capped verdict unchanged: %q", logs.Evidence, want)
	}
	if logs.SamplingRatio != 0 || logs.Records != 0 || logs.RecordsSampled != 0 {
		t.Errorf("ratio/records/sampled = %d/%d/%d, want 0/0/0", logs.SamplingRatio, logs.Records, logs.RecordsSampled)
	}
}

// TestSampledBoundComesFromAFlaggedRung: the bound a miss reports is a claim
// that the window was sampled at that ratio, so only a rung whose response
// says it sampled may supply it. The unflagged rungs below the flagged one are
// walked, but they do not tighten the bound.
func TestSampledBoundComesFromAFlaggedRung(t *testing.T) {
	runner := windowRunner()
	rungs := []mockResponse{cappedLogs(), sampledRung(100000, 0, 0, "")}
	for _, r := range decades[1:] {
		rungs = append(rungs, unflaggedEmptyRung(r, false))
	}
	runner.responses = append(rungs, runner.responses...)

	logs := signalByName(t, discoverWith(t, runner, windowOpts()), "logs")
	if logs.State != SignalUnknown || logs.Truncation != TruncationScanLimit {
		t.Fatalf("state/truncation = %q/%q, want unknown/scan_limit", logs.State, logs.Truncation)
	}
	if !strings.Contains(logs.Evidence, "1-in-100k matched nothing") || !strings.Contains(logs.Evidence, "under ~300k records") {
		t.Errorf("evidence %q should report the bound of the one rung that confirmed it sampled", logs.Evidence)
	}
	if got := len(sampledCalls(runner)); got != 5 {
		t.Errorf("ran %d sampled probes, want 5", got)
	}
}

// TestTruncatedUnflaggedRungIsARefusal: a rung that was cut short without
// having sampled is the original capped query over again, whether or not the
// part it read held a match. Every further rung would be the same query.
func TestTruncatedUnflaggedRungIsARefusal(t *testing.T) {
	runner := windowRunner()
	cut := unflaggedEmptyRung(100000, true)
	cut.truncated = true
	cut.cause = TruncationScanLimit
	runner.responses = append([]mockResponse{
		cappedLogs(), cut,
		sampledRung(10000, 96, 960000, fixedNow.Format(time.RFC3339)),
	}, runner.responses...)

	logs := signalByName(t, discoverWith(t, runner, windowOpts()), "logs")
	if got := len(sampledCalls(runner)); got != 1 {
		t.Errorf("ran %d sampled probes, want 1: an unsampled rung that hit the cap will hit it at every ratio", got)
	}
	if logs.State != SignalUnknown || logs.Truncation != TruncationScanLimit {
		t.Errorf("state/truncation = %q/%q, want unknown/scan_limit", logs.State, logs.Truncation)
	}
	if !strings.Contains(logs.Evidence, "does not support sampling") {
		t.Errorf("evidence %q should say why the fallback did not apply", logs.Evidence)
	}
}

// TestRefusalIsClaimedOnlyWhenNothingContradictsIt: "this environment does not
// support sampling logs" is a statement about the environment, and the walk
// may make it only on the evidence the comment at the refusal describes: an
// unflagged response that looks like the capped query run again, in a walk
// where no rung confirmed that it sampled. After a flagged rung the sentence
// is false, and after an unflagged rung that was cut short by something other
// than the scan limit it is unsupported. Both keep the capped verdict, which
// claims nothing. A bound an earlier flagged miss measured is not reported
// next to a response that cannot be reconciled with it.
func TestRefusalIsClaimedOnlyWhenNothingContradictsIt(t *testing.T) {
	timedOut := func(ratio int64) mockResponse {
		return mockResponse{match: rungMatch(ratio), truncated: true, cause: TruncationTimeout}
	}
	for _, tc := range []struct {
		name  string
		rungs []mockResponse
	}{
		{"matches without the flag after a flagged miss", []mockResponse{sampledRung(100000, 0, 0, ""), refusedRung(10000, 500)}},
		{"a timeout without the flag after a flagged miss", []mockResponse{sampledRung(100000, 0, 0, ""), timedOut(10000)}},
		{"a timeout without the flag on the first rung", []mockResponse{timedOut(100000)}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			runner := windowRunner()
			runner.responses = append(append([]mockResponse{cappedLogs()}, tc.rungs...), runner.responses...)
			opts := windowOpts()

			logs := signalByName(t, discoverWith(t, runner, opts), "logs")
			if logs.State != SignalUnknown || logs.Truncation != TruncationScanLimit {
				t.Errorf("state/truncation = %q/%q, want unknown/scan_limit", logs.State, logs.Truncation)
			}
			if want := truncatedEvidence("logs", TruncationScanLimit, opts.Since, opts.ScanLimitGBytes); logs.Evidence != want {
				t.Errorf("evidence = %q, want the capped verdict unchanged: %q", logs.Evidence, want)
			}
			if got, want := len(sampledCalls(runner)), len(tc.rungs); got != want {
				t.Errorf("ran %d sampled probes, want %d: the walk ends on the rung it cannot read", got, want)
			}
		})
	}
}

// TestRungThatFailsLeavesItsReason: a rung that fails, or that the budget
// refuses, ends the walk with whatever the ladder had established. Without a
// hit that is the capped verdict or a sampled bound, and the reader has to be
// told that the recovery was interrupted and by what: the plain exhaustive
// probe reports the same failure as "probe failed", and a walk that says only
// "the scan cap" hides a permission error behind a volume problem.
func TestRungThatFailsLeavesItsReason(t *testing.T) {
	failed := func(ratio int64) mockResponse {
		return mockResponse{match: rungMatch(ratio), err: errors.New("HTTP 403: missing scope storage:logs:read\nrequest id 0000")}
	}
	const reason = "; sampled retry failed: HTTP 403: missing scope storage:logs:read"
	for _, tc := range []struct {
		name  string
		rungs []mockResponse
		lead  func(opts DiscoverOptions) string
	}{
		{
			name:  "on the first rung",
			rungs: []mockResponse{failed(100000)},
			lead: func(o DiscoverOptions) string {
				return truncatedEvidence("logs", TruncationScanLimit, o.Since, o.ScanLimitGBytes)
			},
		},
		{
			name:  "after an empty rung without the flag",
			rungs: []mockResponse{unflaggedEmptyRung(100000, true), failed(10000)},
			lead: func(o DiscoverOptions) string {
				return truncatedEvidence("logs", TruncationScanLimit, o.Since, o.ScanLimitGBytes)
			},
		},
		{
			name:  "after a sampled miss",
			rungs: []mockResponse{sampledRung(100000, 0, 0, ""), failed(10000)},
			lead: func(o DiscoverOptions) string {
				return sampledZeroEvidence("logs", 100000, o.Since, o.ScanLimitGBytes)
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			runner := windowRunner()
			runner.responses = append(append([]mockResponse{cappedLogs()}, tc.rungs...), runner.responses...)
			opts := windowOpts()

			logs := signalByName(t, discoverWith(t, runner, opts), "logs")
			if logs.State != SignalUnknown || logs.Truncation != TruncationScanLimit {
				t.Errorf("state/truncation = %q/%q, want unknown/scan_limit", logs.State, logs.Truncation)
			}
			if want := tc.lead(opts) + reason; logs.Evidence != want {
				t.Errorf("evidence = %q\nwant       %q", logs.Evidence, want)
			}
			if got, want := len(sampledCalls(runner)), len(tc.rungs); got != want {
				t.Errorf("ran %d sampled probes, want %d: a failed rung ends the walk", got, want)
			}
		})
	}
}

// TestRungThatFailsDoesNotUndoAHit: with a hit held, the verdict is the hit's
// and a failure further down the ladder takes nothing from it.
func TestRungThatFailsDoesNotUndoAHit(t *testing.T) {
	runner := windowRunner()
	runner.responses = append([]mockResponse{
		cappedLogs(),
		sampledRung(100000, 2, 200000, at(6, 0)),
		{match: rungMatch(10000), err: errors.New("HTTP 500")},
	}, runner.responses...)

	logs := signalByName(t, discoverWith(t, runner, windowOpts()), "logs")
	if logs.State != SignalUnknown || logs.RecordsSampled != 2 || logs.Records != 200000 || logs.Truncation != "" {
		t.Errorf("state/sampled/records/truncation = %q/%d/%d/%q, want unknown/2/200000 and no truncation (evidence: %s)",
			logs.State, logs.RecordsSampled, logs.Records, logs.Truncation, logs.Evidence)
	}
	if !strings.Contains(logs.Evidence, "too few to say when the data stopped") {
		t.Errorf("evidence %q should be the thin hit's own", logs.Evidence)
	}
	if got := len(sampledCalls(runner)); got != 2 {
		t.Errorf("ran %d sampled probes, want 2", got)
	}
}

// TestBudgetThatEndsMidWalkIsNamed: the walk shares the run's query budget.
// When the budget refuses a rung, the signal keeps what the earlier rungs
// established and says that the budget, not the ladder, ended the recovery.
func TestBudgetThatEndsMidWalkIsNamed(t *testing.T) {
	for _, tc := range []struct {
		name  string
		rungs []mockResponse
		lead  func(opts DiscoverOptions) string
	}{
		{
			name:  "with nothing established",
			rungs: []mockResponse{unflaggedEmptyRung(100000, true), unflaggedEmptyRung(10000, true)},
			lead: func(o DiscoverOptions) string {
				return truncatedEvidence("logs", TruncationScanLimit, o.Since, o.ScanLimitGBytes)
			},
		},
		{
			name:  "with a sampled bound",
			rungs: []mockResponse{sampledRung(100000, 0, 0, ""), sampledRung(10000, 0, 0, "")},
			lead: func(o DiscoverOptions) string {
				return sampledZeroEvidence("logs", 10000, o.Since, o.ScanLimitGBytes)
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			runner := windowRunner()
			runner.responses = append(append([]mockResponse{cappedLogs()}, tc.rungs...), runner.responses...)
			opts := windowOpts()
			opts.Signals = []string{"logs"}
			// The catalog, the buckets and the capped probe take three; two
			// rungs take the rest.
			opts.BudgetQueries = 5

			inv := discoverWith(t, runner, opts)
			logs := signalByName(t, inv, "logs")
			if got := len(sampledCalls(runner)); got != 2 {
				t.Fatalf("ran %d sampled probes, want 2 before the budget ended", got)
			}
			if logs.State != SignalUnknown || logs.Truncation != TruncationScanLimit {
				t.Errorf("state/truncation = %q/%q, want unknown/scan_limit", logs.State, logs.Truncation)
			}
			if want := tc.lead(opts) + "; sampled retry stopped: discovery budget exhausted"; logs.Evidence != want {
				t.Errorf("evidence = %q\nwant       %q", logs.Evidence, want)
			}
		})
	}
}

// TestThinSampleSurvivesARefusedNextRung: a refusal met while descending for
// precision must not lose an answer that was already conclusive, any more
// than a capped rung may.
func TestThinSampleSurvivesARefusedNextRung(t *testing.T) {
	// The unsampled retry of a capped stream usually hits the cap again, so
	// the refusal arrives cut short as often as it arrives with matches.
	cutShort := unflaggedEmptyRung(10000, true)
	cutShort.truncated, cutShort.cause = true, TruncationScanLimit
	for name, after := range map[string][]mockResponse{
		"refused on the next rung":   {refusedRung(10000, 500)},
		"refused after an empty one": {unflaggedEmptyRung(10000, true), refusedRung(1000, 500)},
		"refused and cut short":      {cutShort},
	} {
		t.Run(name, func(t *testing.T) {
			runner := windowRunner()
			// Six minutes old is past the threshold, so the walk descends, and
			// the hit it falls back on is too thin to date: unknown, with the
			// arrival it proved kept.
			rungs := []mockResponse{cappedLogs(), sampledRung(100000, 2, 200000, at(6, 0))}
			rungs = append(rungs, after...)
			runner.responses = append(rungs, runner.responses...)

			logs := signalByName(t, discoverWith(t, runner, windowOpts()), "logs")
			if logs.State != SignalUnknown || logs.RecordsSampled != 2 || logs.SamplingRatio != 100000 || logs.Records != 200000 {
				t.Errorf("state/sampled/ratio/records = %q/%d/%d/%d, want unknown/2/100000/200000: the thin rung had already proven arrival (evidence: %s)",
					logs.State, logs.RecordsSampled, logs.SamplingRatio, logs.Records, logs.Evidence)
			}
			if logs.Truncation != "" || strings.Contains(logs.Evidence, "does not support sampling") {
				t.Errorf("truncation = %q, evidence = %q: a resolved signal must not read as capped or refused", logs.Truncation, logs.Evidence)
			}
			if got, want := len(sampledCalls(runner)), len(after)+1; got != want {
				t.Errorf("ran %d sampled probes, want %d: the refusal ends the walk", got, want)
			}
		})
	}
}

// TestNewestMatchOfTheWalkDatesTheHit: every rung asks the same scoped question
// of the same window, so a record any of them matched is a record of this
// scope, whether or not that response was sampled or ran to completion. The
// hit the walk ends on supplies the count; the newest record the walk saw
// supplies the age. Dating the hit by its own rung alone reported a signal
// with a ten second old record as stale.
func TestNewestMatchOfTheWalkDatesTheHit(t *testing.T) {
	unflagged := func(r mockResponse) mockResponse { r.sampled = false; return r }
	cut := func(r mockResponse) mockResponse {
		r.truncated, r.cause = true, TruncationScanLimit
		return r
	}
	for _, tc := range []struct {
		name          string
		rungs         []mockResponse
		sampled       int64
		ratio         int64
		records       int64
		lastSeen      string
		age           int64
		lastSeenRatio string
	}{
		{
			name: "an unsampled response after a thin hit",
			rungs: []mockResponse{
				sampledRung(100000, 2, 200000, at(14, 0)),
				unflagged(sampledRung(10000, 500, 500, at(0, 10))),
			},
			sampled: 2, ratio: 100000, records: 200000,
			lastSeen: at(0, 10), age: 10, lastSeenRatio: "1-in-10k",
		},
		{
			name: "a thinner hit that the thicker one replaces",
			rungs: []mockResponse{
				sampledRung(100000, 2, 200000, at(14, 0)),
				sampledRung(10000, 1, 10000, at(0, 20)),
			},
			sampled: 2, ratio: 100000, records: 200000,
			lastSeen: at(0, 20), age: 20, lastSeenRatio: "1-in-10k",
		},
		{
			name: "a sampled response that was cut short",
			rungs: []mockResponse{
				sampledRung(100000, 2, 200000, at(14, 0)),
				cut(sampledRung(10000, 7, 70000, at(0, 5))),
			},
			sampled: 2, ratio: 100000, records: 200000,
			lastSeen: at(0, 5), age: 5, lastSeenRatio: "1-in-10k",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			runner := windowRunner()
			runner.responses = append(append([]mockResponse{cappedLogs()}, tc.rungs...), runner.responses...)

			logs := signalByName(t, discoverWith(t, runner, windowOpts()), "logs")
			if logs.State != SignalLive {
				t.Fatalf("state = %q, want live: a rung of this walk matched a record %ds old (evidence: %s)", logs.State, tc.age, logs.Evidence)
			}
			if logs.LastSeen != tc.lastSeen || logs.AgeSeconds != tc.age {
				t.Errorf("lastSeen/age = %q/%d, want %q/%d", logs.LastSeen, logs.AgeSeconds, tc.lastSeen, tc.age)
			}
			// The newer timestamp dates the hit and nothing else: the volume is
			// still the sample's.
			if logs.RecordsSampled != tc.sampled || logs.SamplingRatio != tc.ratio || logs.Records != tc.records {
				t.Errorf("sampled/ratio/records = %d/%d/%d, want %d/%d/%d",
					logs.RecordsSampled, logs.SamplingRatio, logs.Records, tc.sampled, tc.ratio, tc.records)
			}
			if got, want := len(sampledCalls(runner)), len(tc.rungs); got != want {
				t.Errorf("ran %d sampled probes, want %d", got, want)
			}
			if want := "last-seen was read from the " + tc.lastSeenRatio + " probe"; !strings.Contains(logs.Evidence, want) {
				t.Errorf("evidence %q should say %q: the age does not come from the sample the count does", logs.Evidence, want)
			}
			if strings.Contains(logs.Evidence, "unlikely to be in the sample") {
				t.Errorf("evidence %q describes the bias of a timestamp it did not use", logs.Evidence)
			}
		})
	}
}

// TestCappedProbesOwnMatchDatesTheHit: the exhaustive probe that hit the cap
// is the first response of the walk. It could not count, but a record it
// matched before it was stopped is a record of this scope like any other, and
// no sampled rung may report the signal older than that.
func TestCappedProbesOwnMatchDatesTheHit(t *testing.T) {
	runner := windowRunner()
	capped := cappedLogs()
	capped.records = []map[string]interface{}{rec("matched", int64(700), "last_seen", at(0, 3))}
	runner.responses = append([]mockResponse{
		capped, sampledRung(100000, 40, 4000000, at(9, 0)),
	}, runner.responses...)

	logs := signalByName(t, discoverWith(t, runner, windowOpts()), "logs")
	if logs.State != SignalLive || logs.LastSeen != at(0, 3) || logs.AgeSeconds != 3 {
		t.Fatalf("state/lastSeen/age = %q/%q/%d, want live/%q/3: the capped probe matched a record 3s old (evidence: %s)",
			logs.State, logs.LastSeen, logs.AgeSeconds, at(0, 3), logs.Evidence)
	}
	// What the capped probe counted is not a count of the window.
	if logs.RecordsSampled != 40 || logs.SamplingRatio != 100000 || logs.Records != 4000000 {
		t.Errorf("sampled/ratio/records = %d/%d/%d, want 40/100000/4000000", logs.RecordsSampled, logs.SamplingRatio, logs.Records)
	}
	if want := "last-seen was read from the unsampled probe that hit the cap"; !strings.Contains(logs.Evidence, want) {
		t.Errorf("evidence %q should say %q", logs.Evidence, want)
	}
}

// TestOlderRungStillDatesAThickerHit: the newest record of the walk may sit
// in a thinner, earlier rung and still be past the threshold. The thicker hit
// the walk ends on then supplies the count and the bias, and the verdict is
// stale from the newer of the two timestamps, not from the hit's own.
func TestOlderRungStillDatesAThickerHit(t *testing.T) {
	runner := windowRunner()
	runner.responses = append([]mockResponse{
		cappedLogs(),
		sampledRung(100000, 2, 200000, at(6, 0)),
		sampledRung(10000, 40, 400000, at(9, 0)),
	}, runner.responses...)

	logs := signalByName(t, discoverWith(t, runner, windowOpts()), "logs")
	if logs.State != SignalStale || logs.LastSeen != at(6, 0) || logs.AgeSeconds != 360 || logs.RecordsSampled != 40 {
		t.Errorf("state/lastSeen/age/sampled = %q/%q/%d/%d, want stale/%q/360/40 (evidence: %s)",
			logs.State, logs.LastSeen, logs.AgeSeconds, logs.RecordsSampled, at(6, 0), logs.Evidence)
	}
	for _, want := range []string{"last-seen was read from the 1-in-100k probe", "stopped 6m0s ago"} {
		if !strings.Contains(logs.Evidence, want) {
			t.Errorf("evidence %q missing %q", logs.Evidence, want)
		}
	}
}

// TestTiedTimestampStaysWithTheSample: the hit's own timestamp is the one
// reported unless another rung saw a strictly newer record, so the evidence
// keeps describing the sample's bias instead of crediting a rung that added
// nothing. The verdict is asked directly, because the walk hands over a tie
// only in the order that already favours the hit.
func TestTiedTimestampStaysWithTheSample(t *testing.T) {
	seen, err := parseGrailTime(at(0, 30))
	if err != nil {
		t.Fatal(err)
	}
	hit := sampledProbe{asked: 1000, askedAt: fixedNow, ratio: 1000, matched: 40, records: 40000, lastSeen: at(0, 30)}
	tie := &sighting{lastSeen: at(0, 30), at: seen, askedAt: fixedNow.Add(time.Minute), asked: 10000}

	b := &budgetRunner{}
	sig := b.sampledVerdict(Signal{Name: "logs"}, &CapabilityDef{DataObject: "logs"}, windowOpts(), DefaultTimeField, hit, tie)
	if sig.State != SignalLive || sig.LastSeen != at(0, 30) || sig.AgeSeconds != 30 {
		t.Fatalf("state/lastSeen/age = %q/%q/%d, want live/%q/30: the tie is dated against the hit's own reading", sig.State, sig.LastSeen, sig.AgeSeconds, at(0, 30))
	}
	if strings.Contains(sig.Evidence, "was read from") || !strings.Contains(sig.Evidence, "unlikely to be in the sample") {
		t.Errorf("evidence %q should describe the sample's own timestamp", sig.Evidence)
	}
}

// TestRungWithoutAMatchDatesNothing: a timestamp is evidence of arrival only
// next to a match. A response that matched nothing may not make a signal
// fresh, whatever its last_seen column holds.
func TestRungWithoutAMatchDatesNothing(t *testing.T) {
	runner := windowRunner()
	rungs := []mockResponse{cappedLogs(), sampledRung(100000, 2, 200000, at(14, 0))}
	for _, ratio := range []int64{10000, 1000, 100, 10} {
		rungs = append(rungs, sampledRung(ratio, 0, 0, at(0, 5)))
	}
	runner.responses = append(rungs, runner.responses...)

	logs := signalByName(t, discoverWith(t, runner, windowOpts()), "logs")
	if logs.State == SignalLive || logs.LastSeen != at(14, 0) {
		t.Errorf("state/lastSeen = %q/%q, want a signal that is not live and was last seen at %q: only the first rung matched a record",
			logs.State, logs.LastSeen, at(14, 0))
	}
	if got := len(sampledCalls(runner)); got != 5 {
		t.Errorf("ran %d sampled probes, want 5", got)
	}
}

// tickingRunner advances a clock by a fixed step each time a logs query
// returns, the way a walk of real queries takes time.
type tickingRunner struct {
	inner *mockRunner
	now   time.Time
	step  time.Duration
}

func (r *tickingRunner) RunQuery(ctx context.Context, dql string) (*RunResult, error) {
	res, err := r.inner.RunQuery(ctx, dql)
	if strings.Contains(dql, "fetch logs") {
		r.now = r.now.Add(r.step)
	}
	return res, err
}

// TestEachRungIsDatedAgainstItsOwnClockReading: every rung asks the server for
// a window that ends when the rung runs, so the age of what it returns is
// measured from that moment. Measuring it from the reading taken before the
// exhaustive probe made a record look younger by the time the walk had taken,
// which turned a stale signal into a live one.
func TestEachRungIsDatedAgainstItsOwnClockReading(t *testing.T) {
	mock := windowRunner()
	mock.responses = append([]mockResponse{
		cappedLogs(),
		unflaggedEmptyRung(100000, true),
		// Four minutes old when the run starts, six when this rung is asked:
		// the capped probe and the empty rung each took a minute.
		sampledRung(10000, 100, 1000000, at(4, 0)),
	}, mock.responses...)
	runner := &tickingRunner{inner: mock, now: fixedNow, step: time.Minute}
	opts := windowOpts()
	opts.Now = func() time.Time { return runner.now }
	opts.Signals = []string{"logs"}

	inv, err := Discover(context.Background(), runner, windowDefs(), opts)
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	assertEveryRungListed(t, mock)
	logs := signalByName(t, inv, "logs")
	if logs.State != SignalStale || logs.AgeSeconds != 360 {
		t.Errorf("state/age = %q/%ds, want stale/360s against a 5m threshold: the record was 6m old when its rung was asked (evidence: %s)",
			logs.State, logs.AgeSeconds, logs.Evidence)
	}
	if got := len(sampledCalls(mock)); got != 2 {
		t.Errorf("ran %d sampled probes, want 2", got)
	}
}

// TestNewestMatchKeepsTheReadingOfItsOwnRung: when the timestamp and the count
// come from different rungs, the age is measured from the reading of the rung
// that returned the timestamp, not from the later one the walk ended on.
func TestNewestMatchKeepsTheReadingOfItsOwnRung(t *testing.T) {
	mock := windowRunner()
	mock.responses = append([]mockResponse{
		cappedLogs(),
		// Asked one minute into the run, about a record five minutes older
		// than the start: six minutes old at this rung, so the walk descends.
		sampledRung(100000, 2, 200000, at(5, 0)),
		// Asked two minutes in, and thick enough to end the walk.
		sampledRung(10000, 40, 400000, at(9, 0)),
	}, mock.responses...)
	runner := &tickingRunner{inner: mock, now: fixedNow, step: time.Minute}
	opts := windowOpts()
	opts.Now = func() time.Time { return runner.now }
	opts.Signals = []string{"logs"}

	inv, err := Discover(context.Background(), runner, windowDefs(), opts)
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	assertEveryRungListed(t, mock)
	logs := signalByName(t, inv, "logs")
	if logs.State != SignalStale || logs.LastSeen != at(5, 0) || logs.AgeSeconds != 360 || logs.RecordsSampled != 40 {
		t.Errorf("state/lastSeen/age/sampled = %q/%q/%ds/%d, want stale/%q/360s/40", logs.State, logs.LastSeen, logs.AgeSeconds, logs.RecordsSampled, at(5, 0))
	}
}

// TestCancellationEndsTheWalk: a run cancelled mid-walk surfaces the
// cancellation from the walk itself and issues no further rung, also when the
// rung before it was an empty one that the walk continues past. The walk is
// driven directly because Discover would report the cancellation on its own
// at the next signal.
func TestCancellationEndsTheWalk(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	// The marked rung is answered by the cancellation itself, so it needs no
	// response of its own.
	mock := &mockRunner{responses: []mockResponse{unflaggedEmptyRung(100000, false)}}
	runner := &cancellingRunner{inner: mock, cancel: cancel, marker: rungMatch(10000)}
	b := &budgetRunner{runner: runner, report: &Report{}, queries: 100, seconds: 100}

	sig, err := b.recoverBySampling(ctx, Signal{Name: "logs"}, &CapabilityDef{DataObject: "logs"},
		windowOpts(), DefaultTimeField, func() time.Time { return fixedNow }, fixedNow, &RunResult{Truncated: true, TruncationCause: TruncationScanLimit})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("recoverBySampling = %+v, %v; want context.Canceled", sig, err)
	}
	if sig.State != "" || sig.Evidence != "" {
		t.Errorf("state/evidence = %q/%q, want neither: a cancelled walk reaches no verdict", sig.State, sig.Evidence)
	}
	if got := len(sampledCalls(mock)); got != 2 {
		t.Errorf("ran %d sampled probes, want 2: nothing may run after the cancellation", got)
	}
}

// expiringRunner answers its marker query only once the context it was given
// has ended, the way an executor does when a caller's deadline passes while a
// query is in flight.
type expiringRunner struct {
	inner  *mockRunner
	marker string
}

func (r *expiringRunner) RunQuery(ctx context.Context, dql string) (*RunResult, error) {
	if strings.Contains(dql, r.marker) {
		r.inner.calls = append(r.inner.calls, dql)
		<-ctx.Done()
		return nil, ctx.Err()
	}
	return r.inner.RunQuery(ctx, dql)
}

// TestParentDeadlineEndsTheWalk: a deadline on the caller's context is the
// caller's abort as much as a cancellation is. The walk surfaces it and does
// not resolve the signal from the rungs it had, which would hand back a
// verdict for a run the caller has given up on.
func TestParentDeadlineEndsTheWalk(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	mock := &mockRunner{responses: []mockResponse{sampledRung(100000, 0, 0, "")}}
	runner := &expiringRunner{inner: mock, marker: rungMatch(10000)}
	b := &budgetRunner{runner: runner, report: &Report{}, queries: 100, seconds: 100}

	sig, err := b.recoverBySampling(ctx, Signal{Name: "logs"}, &CapabilityDef{DataObject: "logs"},
		windowOpts(), DefaultTimeField, func() time.Time { return fixedNow }, fixedNow, &RunResult{Truncated: true, TruncationCause: TruncationScanLimit})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("recoverBySampling = %+v, %v; want context.DeadlineExceeded", sig, err)
	}
	if sig.State != "" || sig.Evidence != "" {
		t.Errorf("state/evidence = %q/%q, want neither: an abandoned walk reaches no verdict", sig.State, sig.Evidence)
	}
	if got := len(sampledCalls(mock)); got != 2 {
		t.Errorf("ran %d sampled probes, want 2: nothing may run after the deadline", got)
	}
}

// TestOneUnflaggedMatchIsARefusal: the rule that separates an empty rung from
// the capped query run again is a single matched record, not a volume. A rung
// without the flag that matched one record ends the walk like one that matched
// five hundred.
func TestOneUnflaggedMatchIsARefusal(t *testing.T) {
	runner := windowRunner()
	runner.responses = append([]mockResponse{cappedLogs(), refusedRung(100000, 1)}, runner.responses...)

	logs := signalByName(t, discoverWith(t, runner, windowOpts()), "logs")
	if got := len(sampledCalls(runner)); got != 1 {
		t.Errorf("ran %d sampled probes, want 1", got)
	}
	if logs.State != SignalUnknown || !strings.Contains(logs.Evidence, "does not support sampling") {
		t.Errorf("state = %q, evidence = %q, want unknown with the refusal named", logs.State, logs.Evidence)
	}
	if logs.Records != 0 || logs.RecordsSampled != 0 {
		t.Errorf("records/sampled = %d/%d, want 0/0: an unsampled match supplies no count", logs.Records, logs.RecordsSampled)
	}
}

// TestHitOutranksAnEarlierSampledMiss: a coarse rung that matched nothing says
// only that the scope is small, and a finer rung that then matches settles it.
// The signal reports the hit, not the bound the miss would have given.
func TestHitOutranksAnEarlierSampledMiss(t *testing.T) {
	runner := windowRunner()
	runner.responses = append([]mockResponse{
		cappedLogs(),
		sampledRung(100000, 0, 0, ""),
		sampledRung(10000, 40, 400000, at(0, 30)),
	}, runner.responses...)

	logs := signalByName(t, discoverWith(t, runner, windowOpts()), "logs")
	if logs.State != SignalLive || logs.Truncation != "" || logs.RecordsSampled != 40 || logs.SamplingRatio != 10000 {
		t.Errorf("state/truncation/sampled/ratio = %q/%q/%d/%d, want live with no truncation, 40 at 1-in-10000 (evidence: %s)",
			logs.State, logs.Truncation, logs.RecordsSampled, logs.SamplingRatio, logs.Evidence)
	}
	if strings.Contains(logs.Evidence, "matched nothing") {
		t.Errorf("evidence %q reports the miss of a rung the hit superseded", logs.Evidence)
	}
}

// TestSampledCountIgnoresRequestedRatio is the other half of the refusal
// hazard, and the reason the probe asks for sum(dt.system.sampling_ratio) at
// all: Grail also rounds a ratio down silently, so the requested ratio is not
// a safe multiplier even when sampling did happen.
func TestSampledCountIgnoresRequestedRatio(t *testing.T) {
	runner := windowRunner()
	// Asked for 1-in-100000; the engine actually sampled 1-in-100.
	rounded := sampledRung(100000, 50, 5000, fixedNow.Format(time.RFC3339))
	runner.responses = append([]mockResponse{cappedLogs(), rounded, sampledRung(10000, 60, 6000, fixedNow.Format(time.RFC3339))}, runner.responses...)

	logs := signalByName(t, discoverWith(t, runner, windowOpts()), "logs")
	if logs.Records != 5000 || logs.SamplingRatio != 100 {
		t.Errorf("records/ratio = %d/%d, want 5000/100 read from the data; the requested 100000 would overstate by 1000x",
			logs.Records, logs.SamplingRatio)
	}
}

// TestThinSampleNeverSaysStale: fewer than minSampledRecords sampled records
// prove arrival and nothing about when it stopped. Window divided by the
// sample count is the expected gap between sampled records, not a bound on
// it: a burst fourteen minutes ago and one recent record the sample missed
// give exactly these two timestamps. So a thin hit that looks old stays
// unknown, with the count it did establish, and a real outage on a thinly
// sampled source costs the gate its exit 10.
func TestThinSampleNeverSaysStale(t *testing.T) {
	timedOut := mockResponse{match: rungMatch(10000), truncated: true, cause: TruncationTimeout}
	for _, tc := range []struct {
		name    string
		rungs   []mockResponse
		sampled int64
		ratio   int64
		records int64
	}{
		{
			// Fourteen minutes is past the threshold by more than 15m/2.
			name:    "two records, then a rung that timed out",
			rungs:   []mockResponse{sampledRung(100000, 2, 200000, at(14, 0)), timedOut},
			sampled: 2, ratio: 100000, records: 200000,
		},
		{
			// One short of the floor on the last rung, and far beyond the bias.
			name: "twenty-nine records on the last rung",
			rungs: []mockResponse{
				sampledRung(100000, 0, 0, ""), sampledRung(10000, 0, 0, ""), sampledRung(1000, 0, 0, ""),
				sampledRung(100, 3, 300, at(14, 0)), sampledRung(10, 29, 290, at(14, 0)),
			},
			sampled: 29, ratio: 10, records: 290,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			runner := windowRunner()
			runner.responses = append(append([]mockResponse{cappedLogs()}, tc.rungs...), runner.responses...)

			inv := discoverWith(t, runner, windowOpts())
			logs := signalByName(t, inv, "logs")
			if logs.State != SignalUnknown {
				t.Fatalf("state = %q, want unknown: %d sampled records cannot date an outage (evidence: %s)", logs.State, tc.sampled, logs.Evidence)
			}
			// The arrival the sample proved is kept, as is what it was last seen at.
			if logs.RecordsSampled != tc.sampled || logs.SamplingRatio != tc.ratio || logs.Records != tc.records || logs.LastSeen != at(14, 0) {
				t.Errorf("sampled/ratio/records/lastSeen = %d/%d/%d/%q, want %d/%d/%d/%q",
					logs.RecordsSampled, logs.SamplingRatio, logs.Records, logs.LastSeen, tc.sampled, tc.ratio, tc.records, at(14, 0))
			}
			for _, want := range []string{"too few to say when the data stopped", "14m0s old", "5m0s stale threshold"} {
				if !strings.Contains(logs.Evidence, want) {
					t.Errorf("evidence %q missing %q", logs.Evidence, want)
				}
			}
			if strings.Contains(logs.Evidence, "stopped 14m") {
				t.Errorf("evidence %q still asserts that the source stopped", logs.Evidence)
			}
			if got := CheckRequired(inv, []string{"logs"}).ExitCode(); got != ExitRequiredSignalUnknown {
				t.Errorf("gate exit = %d, want %d", got, ExitRequiredSignalUnknown)
			}
		})
	}
}

// TestSampledStaleInsideTheBiasIsUnknown: a sampled last-seen is biased old,
// so a stale verdict that the bias alone could explain is a false ingest
// outage. During onboarding verification that is the most expensive wrong
// answer available, so the probe declines to make it. The band exists at and
// above the floor too: thirty records over 15m leave 30s of bias.
func TestSampledStaleInsideTheBiasIsUnknown(t *testing.T) {
	opts := windowOpts()
	opts.StaleAfter = 2 * time.Minute
	runner := windowRunner()
	runner.responses = append([]mockResponse{
		cappedLogs(),
		sampledRung(100000, 30, 3000000, at(2, 20)),
	}, runner.responses...)

	logs := signalByName(t, discoverWith(t, runner, opts), "logs")
	if logs.State != SignalUnknown {
		t.Fatalf("state = %q, want unknown: 30s of sampling bias can fully explain a 2m20s age against a 2m threshold", logs.State)
	}
	if !strings.Contains(logs.Evidence, "cannot be told apart") {
		t.Errorf("evidence %q should say the two states are indistinguishable here", logs.Evidence)
	}
}

// TestSampledStaleBeyondTheBiasIsStale is the converse: a signal that really
// did stop must still be reported, or the fallback would have traded one blind
// spot for another. Thirty records is the fewest that may say so.
func TestSampledStaleBeyondTheBiasIsStale(t *testing.T) {
	// 300 sampled records leave 3s of bias and 30 leave 30s, neither anywhere
	// near enough to explain a 9m age.
	for _, matched := range []int64{300, 30} {
		t.Run(fmt.Sprintf("%d records", matched), func(t *testing.T) {
			opts := windowOpts()
			opts.StaleAfter = 2 * time.Minute
			runner := windowRunner()
			runner.responses = append([]mockResponse{
				cappedLogs(),
				sampledRung(100000, matched, matched*100000, at(9, 0)),
			}, runner.responses...)

			inv := discoverWith(t, runner, opts)
			logs := signalByName(t, inv, "logs")
			if logs.State != SignalStale {
				t.Errorf("state = %q, want stale: a 9m age survives the bias of %d records (evidence: %s)", logs.State, matched, logs.Evidence)
			}
			// The sample's provenance leads and the freshness finding follows
			// it; neither replaces the other.
			for _, want := range []string{"probed with 1-in-100k sampling", "; data arrived in this window but stopped 9m0s ago (stale after 2m0s)"} {
				if !strings.Contains(logs.Evidence, want) {
					t.Errorf("evidence %q missing %q", logs.Evidence, want)
				}
			}
			if got := CheckRequired(inv, []string{"logs"}).ExitCode(); got != ExitRequiredSignalNotLive {
				t.Errorf("gate exit = %d, want %d", got, ExitRequiredSignalNotLive)
			}
		})
	}
}

// TestBiasBelowASecondIsNotABand: a thick sample shifts last-seen by a
// fraction of a second, which is the feature's ordinary good case. Such a bias
// is neither printed, since it rounds to "~0s", nor used to withhold a stale
// verdict: the age is compared exactly, not after truncation to whole seconds.
func TestBiasBelowASecondIsNotABand(t *testing.T) {
	for _, tc := range []struct {
		name  string
		ago   time.Duration
		state SignalState
	}{
		{"half a second past the threshold", 5*time.Minute + 500*time.Millisecond, SignalStale},
		{"fresh", 30 * time.Second, SignalLive},
	} {
		t.Run(tc.name, func(t *testing.T) {
			runner := windowRunner()
			runner.responses = append([]mockResponse{
				cappedLogs(),
				// 15m over 9688 records is 93ms of bias.
				sampledRung(1000, 9688, 9688000, fixedNow.Add(-tc.ago).Format(time.RFC3339Nano)),
				sampledRung(100000, 0, 0, ""), sampledRung(10000, 0, 0, ""),
			}, runner.responses...)

			logs := signalByName(t, discoverWith(t, runner, windowOpts()), "logs")
			if logs.State != tc.state {
				t.Errorf("state = %q, want %q (evidence: %s)", logs.State, tc.state, logs.Evidence)
			}
			if strings.Contains(logs.Evidence, "~0s") || strings.Contains(logs.Evidence, "biased") {
				t.Errorf("evidence %q states a bias that rounds to nothing", logs.Evidence)
			}
			if !strings.Contains(logs.Evidence, "9688 sampled records extrapolate to about 9.7M") {
				t.Errorf("evidence %q should still disclose the sample", logs.Evidence)
			}
		})
	}
}

// TestOneSampledRecordReadsAsOne: the evidence counts one record in the
// singular, verb included.
func TestOneSampledRecordReadsAsOne(t *testing.T) {
	one := sampledProbe{asked: 100000, ratio: 100000, matched: 1, records: 100000}
	if got := sampledHitEvidence("logs", one, nil, 0, 25); !strings.Contains(got, "1 sampled record extrapolates to about 100k") {
		t.Errorf("hit evidence = %q", got)
	}
	if got := sampledThinEvidence("logs", one, 6*time.Minute, 5*time.Minute); !strings.Contains(got, "1 sampled record is proof") {
		t.Errorf("thin evidence = %q", got)
	}
	two := sampledProbe{asked: 100000, ratio: 100000, matched: 2, records: 200000}
	if got := sampledHitEvidence("logs", two, nil, 0, 25); !strings.Contains(got, "2 sampled records extrapolate to about 200k") {
		t.Errorf("hit evidence = %q", got)
	}
	if got := sampledThinEvidence("logs", two, 6*time.Minute, 5*time.Minute); !strings.Contains(got, "2 sampled records are proof") {
		t.Errorf("thin evidence = %q", got)
	}
}

// TestNoSampleKeepsTheCappedVerdict: a caller who would rather have no answer
// than an approximate one can say so, and then no sampled query runs at all.
func TestNoSampleKeepsTheCappedVerdict(t *testing.T) {
	opts := windowOpts()
	opts.DisableSampling = true
	runner := windowRunner()
	runner.responses = append([]mockResponse{
		cappedLogs(), sampledRung(100000, 96, 9600000, fixedNow.Format(time.RFC3339)),
	}, runner.responses...)

	logs := signalByName(t, discoverWith(t, runner, opts), "logs")
	if logs.State != SignalUnknown || logs.Truncation != TruncationScanLimit {
		t.Errorf("state/truncation = %q/%q, want unknown/scan_limit", logs.State, logs.Truncation)
	}
	if got := sampledCalls(runner); len(got) != 0 {
		t.Errorf("ran %d sampled probes with --no-sample: %v", len(got), got)
	}
}

// TestOnlyTheScanCapIsWorthSampling: a result cap or a timeout would survive
// the retry unchanged, so spending a query to rediscover that is pure cost.
func TestOnlyTheScanCapIsWorthSampling(t *testing.T) {
	for _, cause := range []TruncationCause{TruncationResultLimit, TruncationTimeout, TruncationConsumption, ""} {
		t.Run(string(cause), func(t *testing.T) {
			runner := windowRunner()
			capped := cappedLogs()
			capped.cause = cause
			runner.responses = append([]mockResponse{
				capped, sampledRung(100000, 96, 9600000, fixedNow.Format(time.RFC3339)),
			}, runner.responses...)

			logs := signalByName(t, discoverWith(t, runner, windowOpts()), "logs")
			if logs.State != SignalUnknown {
				t.Errorf("state = %q, want unknown", logs.State)
			}
			if got := sampledCalls(runner); len(got) != 0 {
				t.Errorf("sampled a %q truncation, which sampling cannot fix: %v", cause, got)
			}
		})
	}
}

// TestCountsDecodedAsJSONNumbersAreRead: a Runner that decodes its responses
// with json.Decoder.UseNumber hands every count over as a json.Number. Reading
// those as zero turned five rungs of 96 matches each into "1-in-10 matched
// nothing" and a bound of under 30 records.
func TestCountsDecodedAsJSONNumbersAreRead(t *testing.T) {
	runner := windowRunner()
	rungs := []mockResponse{cappedLogs()}
	for _, ratio := range decades {
		rungs = append(rungs, mockResponse{match: rungMatch(ratio), sampled: true, records: []map[string]interface{}{
			rec("matched", json.Number("96"), "population", json.Number(fmt.Sprint(96*ratio)), "last_seen", at(0, 30)),
		}})
	}
	runner.responses = append(rungs, runner.responses...)

	logs := signalByName(t, discoverWith(t, runner, windowOpts()), "logs")
	if logs.State != SignalLive || logs.RecordsSampled != 96 || logs.SamplingRatio != 100000 || logs.Records != 9600000 {
		t.Errorf("state/sampled/ratio/records = %q/%d/%d/%d, want live/96/100000/9600000 (evidence: %s)",
			logs.State, logs.RecordsSampled, logs.SamplingRatio, logs.Records, logs.Evidence)
	}
	if got := len(sampledCalls(runner)); got != 1 {
		t.Errorf("ran %d sampled probes, want 1: 96 matches on the first rung end the walk", got)
	}
}

// TestAsInt64 covers the shapes a count arrives in. Grail sends long values as
// strings, encoding/json decodes a bare number as float64, and a decoder set
// to UseNumber keeps it as json.Number.
func TestAsInt64(t *testing.T) {
	for _, tc := range []struct {
		in   interface{}
		want int64
	}{
		{float64(96), 96}, {int64(96), 96}, {96, 96}, {"96", 96},
		{json.Number("96"), 96}, {json.Number("9600000"), 9600000},
		// A count written in float notation is still a count.
		{json.Number("96.0"), 96}, {json.Number("1e3"), 1000},
		// Nothing readable is zero, as for every other shape.
		{json.Number(""), 0}, {json.Number("many"), 0}, {"many", 0}, {nil, 0}, {true, 0},
	} {
		if got := asInt64(tc.in); got != tc.want {
			t.Errorf("asInt64(%#v) = %d, want %d", tc.in, got, tc.want)
		}
	}
}

// TestSampledProbeKeepsTheScopeAndTimeField guards the probe text itself: a
// sampled rung that quietly dropped the scope would count the whole stream and
// report it as the scope's arrival.
func TestSampledProbeKeepsTheScopeAndTimeField(t *testing.T) {
	runner := windowRunner()
	// spans carries a non-default TimeField, which the sampled probe must use.
	runner.responses = append([]mockResponse{
		{match: "fetch spans, from:now()-15m | filter", truncated: true, cause: TruncationScanLimit},
		sampledRung(100000, 96, 9600000, at(0, 30)),
	}, runner.responses...)

	discoverWith(t, runner, windowOpts())
	calls := sampledCalls(runner)
	if len(calls) == 0 {
		t.Fatal("no sampled probe ran")
	}
	for _, want := range []string{`k8s.namespace.name == "payments"`, "takeMax(start_time)", "sum(dt.system.sampling_ratio)", "from:now()-15m"} {
		if !strings.Contains(calls[0], want) {
			t.Errorf("sampled probe %q missing %q", calls[0], want)
		}
	}
}

func TestSamplingBiasAndWindowDuration(t *testing.T) {
	if got := windowDuration("now()-15m"); got != 15*time.Minute {
		t.Errorf("windowDuration = %s, want 15m", got)
	}
	// An expression that is not the normalized form yields no window rather
	// than a wrong one, which disables the bias arithmetic instead of
	// corrupting it.
	if got := windowDuration("-15m"); got != 0 {
		t.Errorf("windowDuration(%q) = %s, want 0", "-15m", got)
	}
	// The measured law: ~window/matched. 11 records over 15m came back 91s
	// behind the truth on a live tenant.
	if got := samplingBias(15*time.Minute, 11); got < 80*time.Second || got > 90*time.Second {
		t.Errorf("samplingBias(15m, 11) = %s, want ~82s (measured 91s of real drift)", got)
	}
	if got := samplingBias(15*time.Minute, 0); got != 0 {
		t.Errorf("samplingBias with no records = %s, want 0", got)
	}
}

func TestFormatCount(t *testing.T) {
	for _, tc := range []struct {
		in   int64
		want string
	}{
		{96, "96"}, {9688, "9688"}, {30000, "30k"}, {100000, "100k"},
		{9688000, "9.7M"}, {5000000, "5M"}, {5103785000, "5.1B"},
	} {
		if got := formatCount(tc.in); got != tc.want {
			t.Errorf("formatCount(%d) = %q, want %q", tc.in, got, tc.want)
		}
	}
}
