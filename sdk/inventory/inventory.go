// Package inventory probes a Dynatrace environment for what data actually
// exists there: which Grail data objects are fetchable, which buckets and
// entity types are present, which capabilities (spans, RUM, k8s, cloud
// integrations, …) are backed by live evidence — and which are absent, with
// the evidence cited. The capability set is defined declaratively and is
// user-extensible (see Definitions).
//
// Everything here is read-only: discovery runs a small, budgeted battery of
// DQL queries and returns a report; nothing is persisted.
//
// The package is execution-agnostic: discovery drives every query through the
// Runner interface, so any DQL executor — the dtctl CLI, a backend service
// with its own query client — can embed it without further dependencies.
package inventory

// APIVersion / KindDefinitions identify a capability-definitions file.
const (
	APIVersion      = "dtctl.dev/v1alpha1"
	KindDefinitions = "InventoryDefinitions"
)

// CapabilityDef defines a capability by how it is discovered — one of four
// fixed shapes, deliberately not an expression language:
//
//   - DataObject: the named object exists in the dt.system.data_objects
//     catalog — and, where bucket statistics cover the object, its buckets
//     hold at least one record (an empty stream is absent, not present)
//   - EntityTypes: at least one entity of a matching type (glob patterns) is in
//     the live topology census
//   - MetricKey: at least one key matching the glob is in the metric catalog
//   - Probe (+ Window): a DQL probe returns at least one row — weak evidence,
//     since absence of events is not absence of capability
//
// Structural shapes (the first three) are preferred: they are cheap, and their
// negatives are strong. Exactly one shape must be set.
type CapabilityDef struct {
	DataObject string `json:"dataObject,omitempty" yaml:"dataObject,omitempty"`
	// TimeField names the record field carrying each record's event time, used
	// only by windowed arrival probes to report last_seen. It defaults to
	// "timestamp"; `spans` is the known exception (it carries start_time and no
	// timestamp at all, so takeMax(timestamp) silently yields nothing there).
	TimeField string `json:"timeField,omitempty" yaml:"timeField,omitempty"`
	// BackingBuckets names the Grail buckets a view-shaped dataObject reads,
	// as glob patterns ("default_davis*"). It exists because retention
	// coverage is only available per bucket: dt.system.buckets keys records by
	// *table*, so a dataObject that is a view — dt.davis.problems and friends
	// are views over `events` — never matches and loses the empty/no-data
	// discrimination entirely. Most views declare their buckets in the
	// catalog's query_string and are resolved automatically; this is the
	// override for the ones that do not.
	BackingBuckets []string `json:"backingBuckets,omitempty" yaml:"backingBuckets,omitempty"`
	EntityTypes    []string `json:"entityTypes,omitempty" yaml:"entityTypes,omitempty"`
	MetricKey      string   `json:"metricKey,omitempty" yaml:"metricKey,omitempty"`
	Probe          string   `json:"probe,omitempty" yaml:"probe,omitempty"`
	Window         string   `json:"window,omitempty" yaml:"window,omitempty"`
}

// Definitions is the on-disk customization format: a named set of capability
// definitions merged over (or replacing) the built-in set. A capability mapped
// to null removes it from the merged set.
type Definitions struct {
	APIVersion   string                    `yaml:"apiVersion,omitempty"`
	Kind         string                    `yaml:"kind,omitempty"`
	Capabilities map[string]*CapabilityDef `yaml:"capabilities"`
}

// SegmentInfo is a Grail filter segment present on the environment.
type SegmentInfo struct {
	UID         string `json:"uid" yaml:"uid"`
	Name        string `json:"name,omitempty" yaml:"name,omitempty"`
	Description string `json:"description,omitempty" yaml:"description,omitempty"`
}

// CapabilityStatus names a capability together with the evidence for its
// verdict: for an absent capability, what was checked and came up empty; for
// an unknown one, why no check actually produced a verdict.
type CapabilityStatus struct {
	Name     string `json:"name" yaml:"name"`
	Evidence string `json:"evidence" yaml:"evidence"`
}

// Inventory is the discovery result: what data is available on the
// environment, with negative findings carried as structured evidence.
type Inventory struct {
	Context     string `json:"context,omitempty" yaml:"context,omitempty"`
	GeneratedAt string `json:"generatedAt" yaml:"generatedAt"`
	// Capabilities that discovery found present. Absent capabilities cite what
	// was checked ("no user.events in the data-object catalog"), so the
	// negative is usable without re-probing. Unknown capabilities got no
	// verdict — the probe failed, the budget ran out, or the fact source they
	// evaluate against was unavailable — and must not be read as absent.
	Capabilities []string           `json:"capabilities,omitempty" yaml:"capabilities,omitempty"`
	Absent       []CapabilityStatus `json:"absent,omitempty" yaml:"absent,omitempty"`
	Unknown      []CapabilityStatus `json:"unknown,omitempty" yaml:"unknown,omitempty"`
	// EntityTypes is the live topology census: entity type → count.
	EntityTypes map[string]int64 `json:"entityTypes,omitempty" yaml:"entityTypes,omitempty"`
	// DataObjects are catalog objects that support fetch; QueryOnly are
	// catalog objects that are queried through other commands instead
	// (metrics → timeseries, smartscape.* → smartscapeNodes/smartscapeEdges).
	// The legacy dt.entity.* lookback views are collapsed out of DataObjects
	// into EntityViews (a count): they number in the hundreds, and the live
	// census in EntityTypes is the canonical entity surface.
	DataObjects []string      `json:"dataObjects,omitempty" yaml:"dataObjects,omitempty"`
	EntityViews int           `json:"entityViews,omitempty" yaml:"entityViews,omitempty"`
	QueryOnly   []string      `json:"queryOnly,omitempty" yaml:"queryOnly,omitempty"`
	Buckets     []string      `json:"buckets,omitempty" yaml:"buckets,omitempty"`
	Segments    []SegmentInfo `json:"segments,omitempty" yaml:"segments,omitempty"`
	// Notes carry cross-cutting facts about how this environment's data is
	// queried (canonical streams, catalog caveats).
	Notes []string `json:"notes,omitempty" yaml:"notes,omitempty"`
	// Window, Signals, and Summary are populated only in windowed arrival mode
	// (DiscoverOptions.Since set). Windowed mode answers "what is arriving for
	// this scope, now", so it reports per-signal states instead of the
	// environment-wide Capabilities/Absent/Unknown verdicts, which are
	// retention-scoped and cannot be windowed.
	Window  *ArrivalWindow `json:"window,omitempty" yaml:"window,omitempty"`
	Signals []Signal       `json:"signals,omitempty" yaml:"signals,omitempty"`
	Summary *StateSummary  `json:"summary,omitempty" yaml:"summary,omitempty"`
	// Discovery is the consumption receipt of the run that produced this
	// inventory.
	Discovery *Report `json:"discovery,omitempty" yaml:"discovery,omitempty"`
}

// SignalState is one signal type's ingest verdict within the arrival window.
// The split exists because present/absent is too coarse once there is a
// window: the interesting onboarding failures are a source that stopped
// mid-window (stale) and a live stream that this particular scope is not
// producing into (empty).
type SignalState string

const (
	// SignalLive: records matched and the newest is within StaleAfter.
	SignalLive SignalState = "live"
	// SignalStale: records matched, but the newest predates StaleAfter — the
	// source emitted inside the window and then stopped.
	SignalStale SignalState = "stale"
	// SignalEmpty: nothing matched, but the stream holds records within
	// retention — the stream works, this scope is not producing into it.
	SignalEmpty SignalState = "empty"
	// SignalNoData: nothing matched and the stream is empty within retention.
	SignalNoData SignalState = "no-data"
	// SignalNotApplicable: nothing matched because the signal cannot carry
	// this scope at all — none of the fields the scope names exist on it.
	// There will never be RUM data under k8s.namespace.name, nor Kubernetes
	// metrics under service.name, and reporting those as "empty" blames a
	// source for a question that was never askable of it.
	SignalNotApplicable SignalState = "n/a"
	// SignalAbsent: the stream is not in this environment's catalog.
	SignalAbsent SignalState = "absent"
	// SignalUnknown: no verdict — the probe was truncated, capped, or failed.
	// Never to be read as absence.
	SignalUnknown SignalState = "unknown"
)

// ArrivalWindow records what a windowed run actually asked.
type ArrivalWindow struct {
	Since      string `json:"since" yaml:"since"`
	Filter     string `json:"filter,omitempty" yaml:"filter,omitempty"`
	StaleAfter string `json:"staleAfter" yaml:"staleAfter"`
	// ScanLimitGBytes is the per-probe scan cap this run was given. It is
	// reported because it is the difference between "this signal is not
	// arriving" and "dtctl was not allowed to look far enough to tell", and
	// a caller that sees an unknown needs the number to decide.
	ScanLimitGBytes float64 `json:"scanLimitGbytes,omitempty" yaml:"scanLimitGbytes,omitempty"`
}

// Signal is one signal type's arrival state within the window. Records and
// Datapoints are mutually exclusive: fetch-backed streams count records,
// metric families count datapoints.
type Signal struct {
	Name       string      `json:"signal" yaml:"signal"`
	State      SignalState `json:"state" yaml:"state"`
	Records    int64       `json:"records,omitempty" yaml:"records,omitempty"`
	Datapoints int64       `json:"datapoints,omitempty" yaml:"datapoints,omitempty"`
	LastSeen   string      `json:"lastSeen,omitempty" yaml:"lastSeen,omitempty"`
	AgeSeconds int64       `json:"ageSeconds,omitempty" yaml:"ageSeconds,omitempty"`
	// Evidence says what was checked, for every state that is not live, and
	// for a live signal whose verdict needs a qualifier: one that was sampled,
	// or one that matched records without an event time. It is carried so a
	// negative is citable without re-probing.
	Evidence string `json:"evidence,omitempty" yaml:"evidence,omitempty"`
	// Truncation is set when State is unknown because a limit cut the probe
	// short, naming which limit. It is structured rather than left to the
	// evidence prose so a caller can tell "dtctl could not afford to look"
	// from "the probe failed", and act on the scan cap specifically.
	Truncation TruncationCause `json:"truncation,omitempty" yaml:"truncation,omitempty"`
	// SamplingRatio is the effective ratio behind Records when the exhaustive
	// probe did not fit under the scan cap and a sampled one answered instead:
	// 1 record in SamplingRatio was read. It is the flag that makes Records an
	// extrapolation and LastSeen a lower bound on freshness rather than the
	// fact of the matter. 0 means the probe was exhaustive.
	SamplingRatio int64 `json:"samplingRatio,omitempty" yaml:"samplingRatio,omitempty"`
	// RecordsSampled is how many records the sample itself matched — the
	// number both the extrapolation and the freshness bias rest on, and the
	// one a reader needs to judge how much to trust either.
	RecordsSampled int64 `json:"recordsSampled,omitempty" yaml:"recordsSampled,omitempty"`
}

// StateSummary counts signals by state.
type StateSummary struct {
	Live          int `json:"live" yaml:"live"`
	Stale         int `json:"stale" yaml:"stale"`
	Empty         int `json:"empty" yaml:"empty"`
	NoData        int `json:"noData" yaml:"noData"`
	NotApplicable int `json:"notApplicable" yaml:"notApplicable"`
	Absent        int `json:"absent" yaml:"absent"`
	Unknown       int `json:"unknown" yaml:"unknown"`
}

// Report is the consumption receipt of a discovery run.
type Report struct {
	Queries int      `json:"queries" yaml:"queries"`
	Seconds float64  `json:"seconds" yaml:"seconds"`
	Notes   []string `json:"notes,omitempty" yaml:"notes,omitempty"`
}
