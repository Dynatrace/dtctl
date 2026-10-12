package inventory

import (
	"context"
	"errors"
	"fmt"
	"path"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// RunResult is one probe/query outcome as discovery sees it. Truncated means
// the result is incomplete — a record/byte cap, scan limit, or timeout cut it
// short — so "no matching row" in it is not evidence that none exists.
type RunResult struct {
	Records   []map[string]interface{}
	Seconds   float64
	Truncated bool
	// TruncationCause says which limit cut the result short, so the evidence
	// can name the one remedy that applies instead of listing three. Empty
	// when Truncated is false, or when the runner cannot tell.
	TruncationCause TruncationCause
	// ColumnTypes maps a result column to its DQL type descriptor, merged
	// across the response's index ranges. A column the query named but the
	// data does not carry comes back as "undefined" — that is how a probe can
	// tell "this dimension does not exist here" from "it exists and is empty".
	ColumnTypes map[string]string
	// Sampled reports that the environment actually applied sampling to this
	// result. It is deliberately not a copy of what the query asked for:
	// Grail refuses to sample some data objects (events, bizevents,
	// security.events) and rounds every ratio down to a power of ten, and it
	// reports both as a warning on a 200 rather than an error. A probe that
	// assumed its requested ratio had been honoured would extrapolate a
	// refused object's count by that whole factor.
	Sampled bool
	// ScannedBytes is how much the query read, as the environment reports
	// it; 0 when it does not. A caller choosing its next query predicts that
	// one's scan from it.
	ScannedBytes int64
}

// TypeUndefined is the DQL type of a column the query referenced but the
// underlying data has no such field for.
const TypeUndefined = "undefined"

// TruncationCause identifies which limit cut a probe short. The distinction is
// load-bearing for onboarding verification on large tenants: a scan cap is a
// dtctl setting the user can raise, while a result cap or a read timeout calls
// for a different fix entirely, and a message that lists all three leaves the
// reader to guess which of their signals is actually unanswerable and why.
type TruncationCause string

const (
	TruncationScanLimit   TruncationCause = "scan_limit"
	TruncationResultLimit TruncationCause = "result_limit"
	TruncationTimeout     TruncationCause = "timeout"
	TruncationConsumption TruncationCause = "consumption"
)

// Runner executes DQL on the live environment. The cmd layer implements it
// over the existing DQL executor (with scan caps); tests implement it over
// fixtures.
//
// Returning records is not the whole job. RunResult also carries the response's
// column types and the reason a result was cut short, and several verdicts are
// only as good as those two fields — a Runner that omits ColumnTypes turns every
// "n/a" into a confident "empty", with no error to say so. Build the result with
// ColumnTypesOf and FirstTruncationCause rather than filling those fields by
// hand; see runner.go.
type Runner interface {
	RunQuery(ctx context.Context, dql string) (*RunResult, error)
}

// DiscoverOptions parameterizes a discovery run.
type DiscoverOptions struct {
	ContextName string
	Now         func() time.Time
	// Segments are pre-fetched by the caller (segments come from the API, not
	// DQL) and pass through into the inventory.
	Segments []SegmentInfo
	// Budget is mandatory, not advisory: discovery stops with a partial
	// inventory rather than overrunning. BudgetSeconds bounds each query by
	// the budget still unspent when it is issued, so a single slow query
	// cannot overrun the budget — it is cut off at it and its capabilities
	// degrade to unknown.
	BudgetQueries int     // 0 = 100
	BudgetSeconds float64 // 0 = 300

	// Since switches discovery into windowed arrival mode: instead of the
	// retention-scoped capability verdicts, report per-signal ingest state for
	// Scope inside the window. It is a DQL timeframe expression
	// ("now()-15m"), already normalized by the caller.
	Since string
	// Scope is the DQL filter fragment scoping every windowed probe. Windowed
	// mode requires it: an unscoped windowed count is the single most
	// expensive query in the battery (measured 204 GB on one tenant's logs),
	// and the unscoped question is already answered for free by bucket
	// metadata.
	Scope string
	// StaleAfter is the age past which a signal that did match is reported
	// stale rather than live.
	StaleAfter time.Duration
	// Signals optionally restricts windowed probing to named capabilities.
	Signals []string
	// ScanLimitGBytes is the per-probe scan cap the Runner applies, carried
	// here only so evidence can name the number the user has to raise. The
	// SDK never enforces it — that is the Runner's job — and 0 means the
	// Runner did not say, in which case the evidence stays unquantified.
	ScanLimitGBytes float64
	// DisableSampling turns off the sampled retry that a scan-capped stream
	// probe otherwise falls back to (see sampling.go). The retry never weakens
	// a verdict it could have reached exhaustively — it only runs where the
	// alternative is no verdict at all — but it does make the surviving
	// numbers estimates, and a caller that would rather have "unknown" than an
	// approximation can say so.
	DisableSampling bool
}

type budgetRunner struct {
	runner  Runner
	report  *Report
	queries int
	seconds float64
	// skipped records that at least one query was refused for lack of budget —
	// the condition under which the inventory is actually partial. Merely
	// ending a run with zero budget left is not.
	skipped bool
}

var errBudgetExhausted = fmt.Errorf("discovery budget exhausted")

// metricCatalogLimit bounds the metric-key catalog read. Hitting it exactly is
// treated as truncation: see the read site.
const metricCatalogLimit = 10000

func (b *budgetRunner) run(ctx context.Context, dql string) (*RunResult, error) {
	// A cancelled run must not keep issuing queries (each would round-trip to
	// the executor just to fail); surface the cancellation instead.
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if b.queries <= 0 || b.seconds <= 0 {
		b.skipped = true
		return nil, errBudgetExhausted
	}
	// The budget has to bound the query, not just the decision to issue it:
	// debiting it afterwards lets one slow query overrun by its whole
	// duration, which is how a 20s budget produced a 60s run (and, under an
	// outer wall, a SIGKILL with no inventory at all).
	qctx, cancel := context.WithTimeout(ctx, time.Duration(b.seconds*float64(time.Second)))
	defer cancel()
	start := time.Now()
	res, err := b.runner.RunQuery(qctx, dql)
	b.report.Queries++
	b.queries--
	if res != nil {
		b.report.Seconds += res.Seconds
		b.seconds -= res.Seconds
	}
	// Cut at the budget: the query is charged what it actually spent and the
	// budget closes, so the caller degrades this fact source to unknown the
	// same way it would for any other refusal. A cancelled parent is the
	// caller's abort, not a budget stop, and keeps propagating.
	if err != nil && ctx.Err() == nil && errors.Is(qctx.Err(), context.DeadlineExceeded) {
		if res == nil {
			b.report.Seconds += time.Since(start).Seconds()
		}
		b.seconds = 0
		b.skipped = true
		return nil, errBudgetExhausted
	}
	return res, err
}

// discoveredFacts carries the structural facts capability evaluation runs
// against, with availability flags: a fact source that failed or was skipped
// must yield "unknown" verdicts, never fabricated absence evidence.
type discoveredFacts struct {
	objects    map[string]bool
	census     map[string]int64
	censusOK   bool
	metricKeys []string
	metricsOK  bool
	// streamRows sums bucket records per stream (dt.system.table → records
	// across its accessible buckets): a catalog object whose buckets are all
	// empty holds no data within retention. Streams without bucket coverage
	// are simply not in the map and keep their catalog verdict.
	streamRows map[string]int64
	// bucketRows is the same figure one level down (bucket name → records),
	// needed because a view-shaped data object has no table of its own and
	// can only be costed through the buckets it reads.
	bucketRows map[string]int64
	// viewBuckets maps a view-shaped data object to the bucket globs its
	// catalog query_string names, so its retention coverage resolves as
	// precisely as a table's rather than not at all.
	viewBuckets map[string][]string
	// viewTable maps a view-shaped data object to the table it fetches, the
	// weaker fallback when the view names no buckets.
	viewTable map[string]string
	// The *Truncated flags mark a fact source that loaded but incompletely
	// (result cap, scan limit): a hit in it still proves presence, but a miss
	// proves nothing and must degrade to unknown, not absent.
	objectsTruncated bool
	censusTruncated  bool
	metricsTruncated bool
}

// Discover runs the discovery battery against a live environment and returns
// the inventory, its consumption receipt attached. Probes run cheapest-first:
// data-object catalog → buckets → entity census → metric catalog (only when a
// metricKey definition needs it) → probe-shaped capability definitions.
// Cancellation aborts the whole run: a half-discovered inventory is never
// returned as if it were a verdict.
func Discover(ctx context.Context, runner Runner, defs map[string]*CapabilityDef, opts DiscoverOptions) (*Inventory, error) {
	// Fail fast on a malformed definition set: the CLI path arrives validated
	// (ParseDefinitions), but a set constructed in Go bypasses that — and a
	// def with no shape would otherwise get no verdict at all, silently.
	if err := ValidateDefinitions(defs); err != nil {
		return nil, fmt.Errorf("invalid capability definitions: %w", err)
	}
	now := opts.Now
	if now == nil {
		now = time.Now
	}
	report := &Report{}
	br := &budgetRunner{
		runner:  runner,
		report:  report,
		queries: valueOr(opts.BudgetQueries, 100),
		seconds: valueOrF(opts.BudgetSeconds, 300),
	}

	inv := &Inventory{
		Context:     opts.ContextName,
		GeneratedAt: now().UTC().Format(time.RFC3339),
		Segments:    opts.Segments,
		Discovery:   report,
	}

	// A hard error on the catalog aborts: without it nothing below is
	// meaningful. Everything after degrades to a report note.
	fetchable, queryOnly, viewQueries, objectsTruncated, err := br.dataObjectCatalog(ctx)
	if err != nil {
		return nil, fmt.Errorf("data-object discovery failed: %w", err)
	}
	// Windowed mode answers a narrow question and its result is consumed by
	// gates and agents, so the environment-wide catalog listings are left out
	// of it: they are not what was asked and they dominate the payload.
	windowed := opts.Since != ""
	// The legacy dt.entity.* lookback views are collapsed to a count: they can
	// number in the hundreds, and the census below is the canonical entity
	// surface. The full catalog still backs capability verdicts.
	if !windowed {
		for _, name := range fetchable {
			if strings.HasPrefix(name, "dt.entity.") {
				inv.EntityViews++
			} else {
				inv.DataObjects = append(inv.DataObjects, name)
			}
		}
		inv.QueryOnly = queryOnly
	}
	if len(queryOnly) > 0 && !windowed {
		inv.Notes = append(inv.Notes, fmt.Sprintf(
			"catalog objects without fetch support: %s — metric data is queried via the timeseries/metrics commands, smartscape via smartscapeNodes/smartscapeEdges",
			strings.Join(queryOnly, ", ")))
	}
	facts := discoveredFacts{
		// Capability shapes check catalog membership, not fetchability.
		objects:          stringSet(append(append([]string{}, fetchable...), queryOnly...)),
		objectsTruncated: objectsTruncated,
		census:           map[string]int64{},
		viewBuckets:      map[string][]string{},
		viewTable:        map[string]string{},
	}
	for name, qs := range viewQueries {
		buckets, table := viewBacking(qs)
		if len(buckets) > 0 {
			facts.viewBuckets[name] = buckets
		}
		if table != "" {
			facts.viewTable[name] = table
		}
	}
	// A definition may name its own buckets, for views the catalog describes
	// without a query_string. An explicit declaration wins over inference.
	for _, def := range defs {
		if def != nil && def.DataObject != "" && len(def.BackingBuckets) > 0 {
			facts.viewBuckets[def.DataObject] = def.BackingBuckets
		}
	}
	if objectsTruncated {
		report.Notes = append(report.Notes, "data-object catalog truncated — the object list is incomplete")
	}

	if res, err := br.run(ctx, "fetch dt.system.buckets | fields name, dt.system.table, records, has_access | sort name asc | limit 1000"); err == nil {
		streamRows := map[string]int64{}
		bucketRows := map[string]int64{}
		for _, rec := range res.Records {
			name, _ := rec["name"].(string)
			if name != "" && !windowed {
				inv.Buckets = append(inv.Buckets, name)
			}
			// Inaccessible buckets report no usable record count — leaving them
			// out keeps an all-inaccessible stream on its catalog verdict
			// instead of a fabricated "empty".
			if access, ok := rec["has_access"].(bool); ok && !access {
				continue
			}
			records := asInt64(rec["records"])
			if name != "" {
				bucketRows[name] += records
			}
			if table, ok := rec["dt.system.table"].(string); ok && table != "" {
				streamRows[table] += records
			}
		}
		if res.Truncated {
			// Unseen buckets may hold the rows: liveness can't be judged.
			report.Notes = append(report.Notes, "bucket list truncated — buckets are missing")
		} else {
			facts.streamRows = streamRows
			facts.bucketRows = bucketRows
		}
	} else if ctx.Err() != nil {
		return nil, ctx.Err()
	} else if err != errBudgetExhausted {
		report.Notes = append(report.Notes, "bucket discovery failed: "+firstLine(err.Error()))
	}

	if windowed {
		// The entity census is a live-topology count with no "since" reading,
		// and no signal state depends on it.
	} else if res, err := br.run(ctx, `smartscapeNodes "*" | summarize c = count(), by:{type} | sort c desc | limit 1000`); err == nil {
		for _, rec := range res.Records {
			if t, ok := rec["type"].(string); ok {
				facts.census[t] = asInt64(rec["c"])
			}
		}
		facts.censusOK = true
		facts.censusTruncated = res.Truncated
		inv.EntityTypes = facts.census
		if res.Truncated {
			report.Notes = append(report.Notes, "entity census truncated — rare entity types may be missing")
		}
	} else if ctx.Err() != nil {
		return nil, ctx.Err()
	} else if err != errBudgetExhausted {
		report.Notes = append(report.Notes, "entity census failed: "+firstLine(err.Error()))
	}

	if needMetricCatalog(defs, opts, windowed) {
		// In windowed mode the catalog is read over the caller's window, so a
		// family that only reported before it does not resolve to a key and
		// cannot be mistaken for arriving data.
		catalogWindow := "now()-2h"
		if windowed {
			catalogWindow = opts.Since
		}
		if keys, truncated, err := br.stringColumn(ctx, metricCatalogDQL(defs, catalogWindow), "metric.key"); err == nil {
			facts.metricKeys = keys
			facts.metricsOK = true
			// Grail caps the metric catalog silently — on one tenant it
			// returned exactly 100,000 keys with no notification at all — and
			// our own limit caps it again. Either way a full page means keys
			// were cut, and a cut catalog cannot support an absence verdict.
			facts.metricsTruncated = truncated || len(keys) >= metricCatalogLimit
			if truncated {
				report.Notes = append(report.Notes, fmt.Sprintf("metric catalog truncated at %d keys — keys are missing", len(keys)))
			}
		} else if ctx.Err() != nil {
			return nil, ctx.Err()
		} else if err != errBudgetExhausted {
			report.Notes = append(report.Notes, "metric catalog failed: "+firstLine(err.Error()))
		}
	}

	if windowed {
		inv.Window = &ArrivalWindow{
			Since:           opts.Since,
			Filter:          opts.Scope,
			StaleAfter:      roundDuration(opts.StaleAfter),
			ScanLimitGBytes: opts.ScanLimitGBytes,
		}
		inv.Signals, err = br.evaluateSignals(ctx, defs, facts, opts)
		if err != nil {
			return nil, err
		}
		inv.Summary = Summarize(inv.Signals)
		if note := allZeroTripwire(inv.Signals); note != "" {
			inv.Notes = append(inv.Notes, note)
		}
		if br.skipped {
			note := fmt.Sprintf("discovery budget exhausted after %d queries / %.0fs — the inventory is partial", report.Queries, report.Seconds)
			inv.Notes = append(inv.Notes, note)
			report.Notes = append(report.Notes, note)
		}
		return inv, nil
	}

	inv.Capabilities, inv.Absent, inv.Unknown, err = br.evaluateCapabilities(ctx, defs, facts)
	if err != nil {
		return nil, err
	}

	// Canonical-stream notes: recurring mistakes where a plausible query
	// silently measures the wrong thing — stated as facts so consumers can
	// steer before the mistake, not after.
	if facts.objects["dt.davis.events"] {
		inv.Notes = append(inv.Notes,
			"Davis problem/event analytics: fetch dt.davis.events — the generic `events` stream mixes other event kinds and its counts diverge from Davis")
	}
	if len(facts.census) > 0 {
		inv.Notes = append(inv.Notes,
			"current-state entity census: smartscapeNodes \"<TYPE>\" — `fetch dt.entity.*` is a lookback view over the query window and diverges from the live topology")
	}
	if br.skipped {
		note := fmt.Sprintf("discovery budget exhausted after %d queries / %.0fs — the inventory is partial", report.Queries, report.Seconds)
		inv.Notes = append(inv.Notes, note)
		report.Notes = append(report.Notes, note)
	}
	return inv, nil
}

// evaluateCapabilities evaluates every definition: structural shapes against
// the discovered facts, probe shapes against the live environment. Verdicts
// are honest three-state: present, absent (with the evidence checked), or
// unknown when no check actually ran — a failed probe, an exhausted budget, or
// an unavailable fact source is not evidence of absence.
func (b *budgetRunner) evaluateCapabilities(ctx context.Context, defs map[string]*CapabilityDef, facts discoveredFacts) (present []string, absent, unknown []CapabilityStatus, err error) {
	names := make([]string, 0, len(defs))
	for n := range defs {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, name := range names {
		def := defs[name]
		// Carry the evidence with the verdict, so the negative is citable
		// without anyone re-deriving it with fresh probes.
		absentWith := func(evidence string) {
			absent = append(absent, CapabilityStatus{Name: name, Evidence: evidence})
		}
		verdict := func(ok bool) {
			if ok {
				present = append(present, name)
			} else {
				absentWith(absenceEvidence(def))
			}
		}
		noVerdict := func(reason string) {
			unknown = append(unknown, CapabilityStatus{Name: name, Evidence: reason})
		}
		// A miss in a truncated fact source proves nothing: the match may sit in
		// the rows that were cut off. A hit still proves presence.
		switch {
		case def.DataObject != "":
			switch {
			case facts.objects[def.DataObject]:
				// Catalog membership proves the stream exists, not that it holds
				// data: on a tenant that never ingested RUM, user.events is
				// still in the catalog. Bucket statistics close that gap for
				// free — when they cover the object and every accessible bucket
				// is empty, the capability is absent, with the emptiness cited.
				if rows, covered := facts.streamRows[def.DataObject]; covered && rows == 0 {
					absentWith(def.DataObject + " is in the catalog, but all its buckets are empty (0 records within retention)")
				} else {
					verdict(true)
				}
			case facts.objectsTruncated:
				noVerdict("not evaluated: data-object catalog truncated")
			default:
				verdict(false)
			}
		case len(def.EntityTypes) > 0:
			switch {
			case !facts.censusOK:
				noVerdict("not evaluated: entity census unavailable")
			case censusMatches(def.EntityTypes, facts.census):
				verdict(true)
			case facts.censusTruncated:
				noVerdict("not evaluated: entity census truncated")
			default:
				verdict(false)
			}
		case def.MetricKey != "":
			switch {
			case !facts.metricsOK:
				noVerdict("not evaluated: metric catalog unavailable")
			case anyGlobMatch(def.MetricKey, facts.metricKeys):
				verdict(true)
			case facts.metricsTruncated:
				noVerdict("not evaluated: metric catalog truncated")
			default:
				verdict(false)
			}
		case def.Probe != "":
			res, perr := b.run(ctx, def.Probe)
			switch {
			case perr == nil && len(res.Records) > 0:
				verdict(true)
			case perr == nil && res.Truncated:
				noVerdict("not evaluated: probe result was cut by a limit before any match")
			case perr == nil:
				verdict(false)
			case ctx.Err() != nil:
				return nil, nil, nil, ctx.Err()
			case perr == errBudgetExhausted:
				noVerdict("not evaluated: discovery budget exhausted")
			default:
				noVerdict("probe failed: " + firstLine(perr.Error()))
			}
		}
	}
	return present, absent, unknown, nil
}

// absenceEvidence says what was checked when a capability came up absent.
func absenceEvidence(def *CapabilityDef) string {
	switch {
	case def.DataObject != "":
		return "no " + def.DataObject + " in the data-object catalog"
	case len(def.EntityTypes) > 0:
		return "no " + strings.Join(def.EntityTypes, "|") + " entities in the live census"
	case def.MetricKey != "":
		return "no metric keys matching " + def.MetricKey + " in the live metric catalog"
	case def.Probe != "":
		return "discovery probe returned no rows in " + def.Window
	default:
		return "no discovery definition matched"
	}
}

// censusMatches reports whether any census type with a live count matches any
// of the glob patterns.
func censusMatches(patterns []string, census map[string]int64) bool {
	for _, pattern := range patterns {
		for t, c := range census {
			if c > 0 && globMatch(pattern, t) {
				return true
			}
		}
	}
	return false
}

func anyGlobMatch(pattern string, keys []string) bool {
	for _, k := range keys {
		if globMatch(pattern, k) {
			return true
		}
	}
	return false
}

// dataObjectCatalog returns the queryable-object catalog partitioned by fetch
// support (the catalog's usable_with column). The catalog lists objects that
// only work through other commands — advertising those as fetch targets bakes
// in DATA_OBJECT_NOT_SUPPORTED failures for consumers.
// dataObjectCatalog reads the catalog. query_string comes along because it is
// the only place a view's backing buckets are stated: a view is fetchable like
// a table but owns no buckets, so without it every view loses its retention
// coverage (see viewBacking).
func (b *budgetRunner) dataObjectCatalog(ctx context.Context) (fetchable, queryOnly []string, views map[string]string, truncated bool, err error) {
	res, err := b.run(ctx,
		`fetch dt.system.data_objects | fieldsAdd fetchable = in("fetch", usable_with) | fields name, fetchable, type, query_string | sort name asc | limit 5000`)
	if err != nil {
		if ctx.Err() != nil {
			return nil, nil, nil, false, ctx.Err()
		}
		// A budget stop is not a schema mismatch: retrying the flat list would
		// only spend a query to be refused again.
		if errors.Is(err, errBudgetExhausted) {
			return nil, nil, nil, false, err
		}
		// Environments without usable_with fall back to the flat list.
		names, truncated, ferr := b.stringColumn(ctx, "fetch dt.system.data_objects | fields name | sort name asc | limit 5000", "name")
		return names, nil, nil, truncated, ferr
	}
	views = map[string]string{}
	for _, rec := range res.Records {
		name, _ := rec["name"].(string)
		if name == "" {
			continue
		}
		if f, ok := rec["fetchable"].(bool); ok && !f {
			queryOnly = append(queryOnly, name)
		} else {
			fetchable = append(fetchable, name)
		}
		if t, _ := rec["type"].(string); t == "view" {
			if qs, _ := rec["query_string"].(string); qs != "" {
				views[name] = qs
			}
		}
	}
	return fetchable, queryOnly, views, res.Truncated, nil
}

// viewBucketPattern captures the bucket list of a view's `fetch <table>,
// bucket: {"a", "b*"}` clause. Views are the only catalog entries that carry a
// query_string, and parsing just this one clause out of it is enough: the goal
// is a retention figure, not a query planner.
var viewBucketPattern = regexp.MustCompile(`bucket\s*:\s*\{([^}]*)\}`)

// viewFetchPattern captures the table a view fetches, the coarser fallback
// when it names no buckets.
var viewFetchPattern = regexp.MustCompile(`(?m)^\s*fetch\s+([A-Za-z_][A-Za-z0-9_.]*)`)

// viewQuoted pulls the individual quoted bucket names out of a bucket clause.
var viewQuoted = regexp.MustCompile(`"([^"]+)"`)

// viewBacking resolves a view's query_string to the buckets it reads, or
// failing that the table it fetches.
func viewBacking(queryString string) (buckets []string, table string) {
	// Comments can carry an example `fetch`/`bucket:` that is not the query.
	var body []string
	for _, line := range strings.Split(queryString, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "//") {
			continue
		}
		body = append(body, line)
	}
	clean := strings.Join(body, "\n")
	if m := viewBucketPattern.FindStringSubmatch(clean); m != nil {
		for _, q := range viewQuoted.FindAllStringSubmatch(m[1], -1) {
			buckets = append(buckets, q[1])
		}
	}
	if m := viewFetchPattern.FindStringSubmatch(clean); m != nil {
		table = m[1]
	}
	return buckets, table
}

func (b *budgetRunner) stringColumn(ctx context.Context, dql, column string) ([]string, bool, error) {
	res, err := b.run(ctx, dql)
	if err != nil {
		return nil, false, err
	}
	var out []string
	for _, rec := range res.Records {
		if s, ok := rec[column].(string); ok && s != "" {
			out = append(out, s)
		}
	}
	return out, res.Truncated, nil
}

// metricCatalogDQL builds the catalog read, pushing the definitions' own globs
// into the query whenever every one of them translates.
//
// Reading the whole catalog to glob it locally is the most expensive thing
// discovery does: on a large tenant it is 51.9s against 3,245 keys, while the
// five built-in globs as a filter return the 363 keys that can actually decide
// a verdict in 12.4s. Everything the filter drops is a key no definition could
// have matched, so the verdicts are identical — the cost was never buying
// anything.
//
// It has to stay a superset of what the local globs accept, or a dropped key
// becomes a fabricated absence. It is: matchesValue is case-insensitive where
// path.Match is not, and untranslatable patterns fall back to the full read
// rather than being approximated. The local glob match still decides.
func metricCatalogDQL(defs map[string]*CapabilityDef, window string) string {
	if filter, ok := metricKeyFilter(metricKeyGlobs(defs)); ok {
		return fmt.Sprintf("metrics from:%s | filter %s | summarize c = count(), by:{metric.key} | limit %d",
			window, filter, metricCatalogLimit)
	}
	return fmt.Sprintf("metrics from:%s | summarize c = count(), by:{metric.key} | limit %d", window, metricCatalogLimit)
}

// metricKeyGlobs collects every metric glob in play, deduplicated and ordered
// so the query text is stable. It reads *all* metric definitions, not just the
// ones a windowed run selected, because the catalog it fills is shared: a
// narrower filter would starve any other consumer of facts.metricKeys.
func metricKeyGlobs(defs map[string]*CapabilityDef) []string {
	seen := map[string]bool{}
	globs := make([]string, 0, len(defs))
	for _, d := range defs {
		if d.MetricKey == "" || seen[d.MetricKey] {
			continue
		}
		seen[d.MetricKey] = true
		globs = append(globs, d.MetricKey)
	}
	sort.Strings(globs)
	return globs
}

// metricKeyFilter renders the globs as a DQL filter, or reports that they
// cannot be pushed down. Only `*` wildcards over the characters metric keys
// are actually made of translate; path.Match's `?` and `[a-z]` classes have no
// matchesValue equivalent, and a bare `*` filters nothing, so those take the
// full catalog read instead of a filter that might exclude a real match.
func metricKeyFilter(globs []string) (string, bool) {
	clauses := make([]string, 0, len(globs))
	for _, g := range globs {
		if !translatableGlob(g) {
			return "", false
		}
		clauses = append(clauses, fmt.Sprintf("matchesValue(metric.key, %q)", g))
	}
	if len(clauses) == 0 {
		return "", false
	}
	return strings.Join(clauses, " or "), true
}

func translatableGlob(g string) bool {
	if g == "" || strings.Trim(g, "*") == "" {
		return false
	}
	for _, r := range g {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		case r == '.', r == '_', r == '-', r == ':', r == '*':
		default:
			return false
		}
	}
	return true
}

func anyMetricDef(defs map[string]*CapabilityDef) bool {
	for _, d := range defs {
		if d.MetricKey != "" {
			return true
		}
	}
	return false
}

func globMatch(pattern, s string) bool {
	ok, err := path.Match(pattern, s)
	return err == nil && ok
}

func stringSet(items []string) map[string]bool {
	set := make(map[string]bool, len(items))
	for _, s := range items {
		set[s] = true
	}
	return set
}

func asInt64(v interface{}) int64 {
	switch n := v.(type) {
	case float64:
		return int64(n)
	case int64:
		return n
	case int:
		return int64(n)
	case string:
		i, _ := strconv.ParseInt(n, 10, 64)
		return i
	}
	return 0
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	if len(s) > 220 {
		s = s[:220] + "…"
	}
	return s
}

func valueOr(v, def int) int {
	if v > 0 {
		return v
	}
	return def
}

func valueOrF(v, def float64) float64 {
	if v > 0 {
		return v
	}
	return def
}

// needMetricCatalog reports whether the metric catalog has to be read: only
// when some metric-shaped definition will actually be evaluated.
func needMetricCatalog(defs map[string]*CapabilityDef, opts DiscoverOptions, windowed bool) bool {
	if !windowed {
		return anyMetricDef(defs)
	}
	for name, d := range defs {
		if d.MetricKey == "" {
			continue
		}
		if len(opts.Signals) == 0 || containsFold(opts.Signals, name) {
			return true
		}
	}
	return false
}

// allZeroTripwire warns when every signal came up empty. Field existence
// cannot be validated up front — neither dt.system.data_objects nor `describe`
// lists the dynamic attributes people actually scope by — so a scope naming a
// field or value that does not exist is indistinguishable from a source that
// genuinely stopped. When nothing at all matched while the streams themselves
// hold data, a wrong scope is by far the likelier explanation, and saying so
// is cheaper and more honest than a confident all-empty verdict.
func allZeroTripwire(signals []Signal) string {
	evaluated, empty := 0, 0
	for _, s := range signals {
		switch s.State {
		// None of these say anything about whether the scope is valid, so
		// none of them can weigh against the wrong-scope explanation. Leaving
		// no-data in the denominator made the tripwire unfireable in
		// practice: one stream that is empty tenant-wide — a tenant without
		// synthetic monitoring, say — permanently suppressed the warning.
		case SignalUnknown, SignalAbsent, SignalNoData, SignalNotApplicable:
			continue
		case SignalEmpty:
			empty++
		}
		evaluated++
	}
	if evaluated < 2 || empty != evaluated {
		return ""
	}
	return "every signal matched 0 records while the streams hold data within retention — " +
		"the scope is more likely wrong than every signal failing at once; check the field " +
		"names and values in --scope (dtctl cannot validate them: Grail does not expose the " +
		"dynamic attributes a filter may reference)"
}
