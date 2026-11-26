package inventory

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"
)

// DefaultTimeField is the record field windowed probes read event time from
// when a capability definition does not override it.
const DefaultTimeField = "timestamp"

// evaluateSignals answers, per signal type, whether data is arriving for the
// caller's scope inside the window.
//
// Only stream- and metric-shaped definitions are signal types; entity-census
// and probe shapes have no meaningful "since" reading and are left to the
// unwindowed path.
//
// Probes are filter-first by necessity, not style: on a live tenant, folding
// the predicate into `summarize ... countIf(...)` scanned 205 GB against 10.8
// GB for the same question asked as `| filter ... | summarize count()`, and an
// unfiltered `summarize count()` scanned 204 GB. So the scope filter is pushed
// into the fetch, and the "is this stream live at all" half of the answer is
// taken from bucket metadata that costs nothing rather than a second probe.
func (b *budgetRunner) evaluateSignals(ctx context.Context, defs map[string]*CapabilityDef, facts discoveredFacts, opts DiscoverOptions) ([]Signal, error) {
	names := make([]string, 0, len(defs))
	for n := range defs {
		if !isSignalDef(defs[n]) {
			continue
		}
		if len(opts.Signals) > 0 && !containsFold(opts.Signals, n) {
			continue
		}
		names = append(names, n)
	}
	sort.Strings(names)

	now := opts.Now
	if now == nil {
		now = time.Now
	}

	fields := scopeFields(opts.Scope)

	signals := make([]Signal, 0, len(names))
	for _, name := range names {
		def := defs[name]
		var (
			sig  Signal
			serr error
		)
		if def.MetricKey != "" {
			sig, serr = b.probeMetricSignal(ctx, name, def, facts, opts, fields, now())
		} else {
			sig, serr = b.probeStreamSignal(ctx, name, def, facts, opts, fields, now())
		}
		if serr != nil {
			return nil, serr
		}
		signals = append(signals, sig)
	}
	return signals, nil
}

// scopeSyntaxMarkers identify a probe failure caused by the scope expression
// itself rather than by the environment.
var scopeSyntaxMarkers = []string{
	"DQL-ERROR-PARSING",
	"PARSE_ERROR",
	"SYNTAX_ERROR",
	"isn't allowed here",
	"mismatched input",
}

// isScopeSyntaxError reports whether a probe failed because the scope does not
// parse. Such a failure is not a per-signal "unknown": it will repeat
// identically for every remaining signal, burn the whole budget, and print
// the same parse error a dozen times while presenting a typo as an
// inconclusive environment.
func isScopeSyntaxError(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	for _, m := range scopeSyntaxMarkers {
		if strings.Contains(msg, m) {
			return true
		}
	}
	return false
}

// scopeSyntaxError wraps a parse failure so the caller can report it against
// the scope the user wrote rather than against the signal that happened to be
// probed first.
func scopeSyntaxError(scope string, err error) error {
	return fmt.Errorf("the --scope expression does not parse as DQL, so no signal could be probed:\n  scope: %s\n  %s", scope, firstLine(err.Error()))
}

// probeStreamSignal evaluates one fetch-backed stream.
func (b *budgetRunner) probeStreamSignal(ctx context.Context, name string, def *CapabilityDef, facts discoveredFacts, opts DiscoverOptions, fields []string, now time.Time) (Signal, error) {
	sig := Signal{Name: name}

	// A stream that is not in the catalog cannot be probed, and must not be:
	// the query would fail and the failure would read as a probe error rather
	// than the plain fact that this environment does not carry the stream.
	if !facts.objects[def.DataObject] {
		if facts.objectsTruncated {
			sig.State = SignalUnknown
			sig.Evidence = "not evaluated: data-object catalog truncated, so the stream's absence is not established"
			return sig, nil
		}
		sig.State = SignalAbsent
		sig.Evidence = "no " + def.DataObject + " in the data-object catalog"
		return sig, nil
	}

	timeField := def.TimeField
	if timeField == "" {
		timeField = DefaultTimeField
	}
	dql := fmt.Sprintf("fetch %s, from:%s | filter %s | summarize matched = count(), last_seen = takeMax(%s)",
		def.DataObject, opts.Since, opts.Scope, timeField)

	res, err := b.run(ctx, dql)
	switch {
	case err == nil:
	case ctx.Err() != nil:
		return sig, ctx.Err()
	case err == errBudgetExhausted:
		sig.State = SignalUnknown
		sig.Evidence = "not evaluated: discovery budget exhausted"
		return sig, nil
	case isScopeSyntaxError(err):
		return sig, scopeSyntaxError(opts.Scope, err)
	default:
		sig.State = SignalUnknown
		sig.Evidence = "probe failed: " + firstLine(err.Error())
		return sig, nil
	}
	if res.Truncated {
		// A cut-short probe that found nothing proves nothing: the match may
		// sit in the part that was never read. Before settling for that,
		// try to buy the answer more cheaply by sampling.
		return b.recoverBySampling(ctx, sig, def, opts, timeField, now, res)
	}

	var matched int64
	var lastSeen string
	if len(res.Records) > 0 {
		matched = asInt64(res.Records[0]["matched"])
		lastSeen, _ = res.Records[0]["last_seen"].(string)
	}
	sig.Records = matched

	if matched == 0 {
		cov := retentionFor(def.DataObject, facts)
		// A stream that is empty tenant-wide is already fully explained, and
		// asking it about the scope's fields could only mislead: it has no
		// records for them to be absent from.
		if cov.known && cov.rows == 0 {
			sig.State, sig.Evidence = emptyState(def.DataObject, cov)
			return sig, nil
		}
		// Otherwise, before blaming the source, establish that the question
		// was askable of this stream at all. The check runs only here, so a
		// live signal never pays for it.
		switch app, aerr := b.streamScopeApplicability(ctx, def.DataObject, opts, fields); {
		case aerr != nil:
			return sig, aerr
		case app == applicabilityNo:
			sig.State = SignalNotApplicable
			sig.Evidence = notApplicableEvidenceStream(def.DataObject, fields, opts.Since)
			return sig, nil
		}
		sig.State, sig.Evidence = emptyState(def.DataObject, cov)
		return sig, nil
	}
	applyFreshness(&sig, lastSeen, timeField, now, opts.StaleAfter)
	return sig, nil
}

// streamScopeApplicability asks whether a stream carries any of the fields
// the scope names.
//
// `| limit 1` is what makes this affordable: it short-circuits as soon as one
// record carries the field. Measured on a live tenant — logs +
// k8s.namespace.name: 8.5 MB / 68 ms (a hit, found immediately); user.events
// + k8s.namespace.name: 50 MB / 359 ms (a miss, so the full window, but that
// is the same scan the scoped probe just did).
//
// A miss alone is not an answer, because a stream with no records at all in
// the window would miss for a field it does carry. So a miss is confirmed
// against a second, near-free probe for any record whatsoever; if the window
// is simply quiet, the question stays unanswered rather than becoming a
// false "this field does not exist here".
//
// Every failure path yields "unknown": a probe that did not answer must
// never be read as "the field is missing".
func (b *budgetRunner) streamScopeApplicability(ctx context.Context, object string, opts DiscoverOptions, fields []string) (applicability, error) {
	if len(fields) == 0 {
		return applicabilityUnknown, nil
	}
	present, err := b.countLimitOne(ctx,
		fmt.Sprintf("fetch %s, from:%s | filter %s | limit 1 | summarize present = count()",
			object, opts.Since, scopeFieldPredicate(fields)), "present")
	if err != nil {
		return applicabilityUnknown, err
	}
	if present == nil {
		return applicabilityUnknown, nil
	}
	if *present > 0 {
		return applicabilityYes, nil
	}
	any, err := b.countLimitOne(ctx,
		fmt.Sprintf("fetch %s, from:%s | limit 1 | summarize any = count()", object, opts.Since), "any")
	if err != nil {
		return applicabilityUnknown, err
	}
	if any == nil || *any == 0 {
		// Nothing at all arrived in the window, so the absence of the scope's
		// fields says nothing about whether the stream can carry them.
		return applicabilityUnknown, nil
	}
	return applicabilityNo, nil
}

// countLimitOne runs a short-circuiting existence probe and returns its
// count, or nil when the probe gave no usable answer. Cancellation is the
// only condition that propagates as an error: everything else is just an
// unanswered question.
func (b *budgetRunner) countLimitOne(ctx context.Context, dql, column string) (*int64, error) {
	res, err := b.run(ctx, dql)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, nil
	}
	if res.Truncated || len(res.Records) == 0 {
		return nil, nil
	}
	n := asInt64(res.Records[0][column])
	return &n, nil
}

// metricKeySampleSize bounds how many keys of a family get probed. Each probe
// is free (0 scanned bytes, ~25 ms), but a wide family can hold thousands of
// keys and the budget is finite.
const metricKeySampleSize = 8

// probeMetricSignal evaluates one metric family.
//
// Metrics are not fetchable, so the probe is a timeseries over concrete keys —
// `count(*)` is not expressible, and the keys cannot be combined into one
// query: a multi-aggregation timeseries returns *no rows at all* when any one
// of its keys has no data, so a single quiet key would erase the evidence of
// the live ones. Verified on a live tenant.
//
// One key is not a family, either: picking a single representative produced a
// confident "empty" for a namespace whose k8s metrics were demonstrably
// flowing, because the arbitrary pick happened to be a key nobody emits. So a
// bounded sample is probed, and a miss across the sample only becomes a real
// absence verdict when the sample was the whole family.
func (b *budgetRunner) probeMetricSignal(ctx context.Context, name string, def *CapabilityDef, facts discoveredFacts, opts DiscoverOptions, fields []string, now time.Time) (Signal, error) {
	sig := Signal{Name: name}
	if !facts.metricsOK {
		sig.State = SignalUnknown
		sig.Evidence = "not evaluated: metric catalog unavailable"
		return sig, nil
	}
	matching := globMatches(def.MetricKey, facts.metricKeys)
	if len(matching) == 0 {
		if facts.metricsTruncated {
			sig.State = SignalUnknown
			sig.Evidence = "not evaluated: metric catalog truncated, so the absence of a key matching " + def.MetricKey + " is not established"
			return sig, nil
		}
		sig.State = SignalNoData
		sig.Evidence = "no metric keys matching " + def.MetricKey + " in the window"
		return sig, nil
	}
	sample := matching
	if len(sample) > metricKeySampleSize {
		sample = sample[:metricKeySampleSize]
	}

	for _, key := range sample {
		dql := fmt.Sprintf("timeseries n = count(%s), from:%s, filter: %s", key, opts.Since, opts.Scope)
		res, err := b.run(ctx, dql)
		switch {
		case err == nil:
		case ctx.Err() != nil:
			return sig, ctx.Err()
		case err == errBudgetExhausted:
			sig.State = SignalUnknown
			sig.Evidence = "not evaluated: discovery budget exhausted"
			return sig, nil
		case isScopeSyntaxError(err):
			return sig, scopeSyntaxError(opts.Scope, err)
		default:
			sig.State = SignalUnknown
			sig.Evidence = "probe failed: " + firstLine(err.Error())
			return sig, nil
		}
		points, lastSeen := summarizeSeries(res.Records)
		if points > 0 {
			// A hit settles the family: at least one of its metrics is
			// arriving for this scope.
			sig.Datapoints = points
			applyFreshness(&sig, lastSeen, "", now, opts.StaleAfter)
			return sig, nil
		}
	}

	// Nothing in the sample reported. Before reading that as absence, settle
	// whether the family carries the scope's fields as dimensions at all —
	// dt.host.* has no k8s.namespace.name and never will, and a filter on one
	// there is silently never true.
	app, probed, aerr := b.metricScopeApplicability(ctx, sample, opts, fields)
	if aerr != nil {
		return sig, aerr
	}
	if app == applicabilityNo {
		sig.State = SignalNotApplicable
		// The structural claim ("this family cannot carry this scope") is only
		// earned when every key in the family was asked. The dimension probe
		// samples at most metricApplicabilitySampleSize of them, and a family
		// need not be homogeneous — dt.kubernetes.* spans node- and
		// container-level keys with different dimensions — so a sampled
		// verdict says what it checked and no more. n/a is still the right
		// state to report: it is what the checked keys say, and degrading it
		// to unknown would erase the distinction this state exists to draw.
		sig.Evidence = notApplicableEvidenceMetric(def.MetricKey, probed, len(matching),
			probed >= len(matching) && !facts.metricsTruncated, fields)
		return sig, nil
	}

	// That is only an absence claim if the sample was the whole family — and
	// only if the catalog it came from was complete.
	if len(sample) < len(matching) || facts.metricsTruncated {
		sig.State = SignalUnknown
		// --signals selects capability names, not metric keys, so telling the
		// reader to pass one here produced an "unknown signal" error. Point at
		// the query that actually settles it instead.
		sig.Evidence = fmt.Sprintf("not evaluated: no datapoints from %d of %d keys matching %s, which is a sample, not the family — settle it on the key you care about with: dtctl query 'timeseries count(<key>), from:%s, filter: %s'",
			len(sample), len(matching), def.MetricKey, opts.Since, opts.Scope)
		return sig, nil
	}
	sig.State = SignalEmpty
	sig.Evidence = fmt.Sprintf("all %d keys matching %s exist but reported no datapoints for this scope in the window",
		len(matching), def.MetricKey)
	return sig, nil
}

// metricApplicabilitySampleSize bounds how many keys are asked whether they
// carry the scope's dimensions. Each probe scans nothing, but the query
// budget is finite and the value probes above may already have spent eight of
// it on this family alone.
const metricApplicabilitySampleSize = 3

// metricScopeApplicability asks whether any probed key of a family carries
// any of the scope's fields as a dimension.
//
// Grouping by a dimension is the exact test and it is free: a metric without
// that dimension returns the column typed "undefined" (verified live: 0
// scanned bytes, ~150 ms), while a real dimension comes back with its own
// type. It has to be a separate query from the value probe — a `filter:` that
// matches nothing returns no rows and therefore no type information at all.
//
// Answering "yes" needs one key; answering "no" needs every probed key to
// agree, so the count that was actually checked is returned for the evidence.
func (b *budgetRunner) metricScopeApplicability(ctx context.Context, keys []string, opts DiscoverOptions, fields []string) (applicability, int, error) {
	if len(fields) == 0 || len(keys) == 0 {
		return applicabilityUnknown, 0, nil
	}
	if len(keys) > metricApplicabilitySampleSize {
		keys = keys[:metricApplicabilitySampleSize]
	}
	quoted := make([]string, 0, len(fields))
	for _, f := range fields {
		quoted = append(quoted, quoteField(f))
	}
	by := strings.Join(quoted, ", ")

	probed := 0
	for _, key := range keys {
		dql := fmt.Sprintf("timeseries n = count(%s), from:%s, by:{%s} | limit 1", key, opts.Since, by)
		res, err := b.run(ctx, dql)
		if err != nil {
			if ctx.Err() != nil {
				return applicabilityUnknown, probed, ctx.Err()
			}
			// Budget, syntax, or backend trouble: no verdict from this key.
			// A partial "no" is not a "no", so stop rather than conclude.
			return applicabilityUnknown, probed, nil
		}
		switch metricDimensionApplicability(res.ColumnTypes, fields) {
		case applicabilityYes:
			return applicabilityYes, probed + 1, nil
		case applicabilityUnknown:
			return applicabilityUnknown, probed, nil
		}
		probed++
	}
	return applicabilityNo, probed, nil
}

// truncatedEvidence names the limit that cut a probe short and the remedy that
// actually applies to it.
//
// The three causes are not interchangeable, and on a high-volume tenant the
// difference decides whether the signal is answerable at all: a scan cap is a
// dtctl setting the user owns and can raise, whereas a result cap wants
// aggregation and a timeout wants a narrower read. The scan-cap case is the
// common one and the one worth being blunt about — the stream is simply larger
// inside this window than the cap allows, so narrowing --since only helps if
// it is narrowed far enough, and on the largest tenants that can be below any
// window worth asking about.
func truncatedEvidence(object string, cause TruncationCause, since string, scanLimitGB float64) string {
	const prefix = "not evaluated: "
	switch cause {
	case TruncationScanLimit:
		return prefix + object + " scans more than " + scanCapLabel(scanLimitGB) + " over " + windowLabel(since) +
			", so the probe stopped before it could count — raise --scan-limit-gbytes, or narrow --since far enough that the stream fits under the cap"
	case TruncationResultLimit:
		return prefix + "the probe's result hit dtctl's record cap before " + object +
			" could be counted — unexpected for an aggregating probe, so treat this as a dtctl bug rather than a telemetry finding"
	case TruncationTimeout:
		return prefix + "the read timed out before " + object + " could be counted over " + windowLabel(since) +
			" — narrow --since, or retry when the tenant is less busy"
	case TruncationConsumption:
		return prefix + "the query consumption limit stopped the probe before it could count " + object
	}
	return prefix + "the probe was cut short by a limit — narrow --since or raise --scan-limit-gbytes"
}

// scanCapLabel names the cap in the evidence when the Runner reported it, and
// stays vague rather than inventing a number when it did not.
func scanCapLabel(gb float64) string {
	if gb <= 0 {
		return "the scan cap"
	}
	return "the " + strconv.FormatFloat(gb, 'g', -1, 64) + " GB scan cap"
}

// emptyState distinguishes the two zero-match cases that matter. A stream that
// holds records within retention but matched nothing for this scope is the
// onboarding-failure signal; a stream that is empty tenant-wide is not about
// this source at all.
func emptyState(object string, cov retentionCoverage) (SignalState, string) {
	switch {
	case !cov.known:
		return SignalEmpty, "0 records matched in the window (retention coverage for " + object + " is unknown, so tenant-wide liveness was not established)"
	case cov.rows == 0:
		// An empty backing is conclusive in this direction even when it is a
		// superset: nothing can be in the view if nothing is in its buckets.
		return SignalNoData, fmt.Sprintf("%s is in the catalog, but %s %s 0 records within retention", object, cov.source, cov.verb)
	case cov.exact:
		return SignalEmpty, fmt.Sprintf("0 records matched in the window, but %s %s %d records within retention: the stream works, this scope is not producing into it", cov.source, cov.verb, cov.rows)
	default:
		return SignalEmpty, fmt.Sprintf("0 records matched in the window; %s %s %d records within retention, but that is a superset of %s, so this object's own tenant-wide liveness was not established",
			cov.source, cov.verb, cov.rows, object)
	}
}

// retentionCoverage is how many records a data object holds within retention,
// and how confident that figure is about the object itself.
type retentionCoverage struct {
	rows  int64
	known bool
	// exact is false when the count covers more than the object — a view's
	// backing table, where a non-zero total says nothing about the view.
	exact bool
	// source names what was actually counted, for the evidence line, and
	// verb agrees with it — "logs holds", but "its 3 buckets hold".
	source string
	verb   string
}

// retentionFor resolves a data object's retention coverage.
//
// dt.system.buckets keys records by table, so a view — dt.davis.problems,
// dt.davis.events and dt.synthetic.events all are views over `events` — has
// no entry of its own and would otherwise lose the empty/no-data
// discrimination entirely. Resolution runs cheapest-and-most-precise first:
// the object's own table, then the buckets the view declares (exact, because
// those buckets are the view), then the table it fetches (a superset).
func retentionFor(object string, facts discoveredFacts) retentionCoverage {
	if rows, ok := facts.streamRows[object]; ok {
		return retentionCoverage{rows: rows, known: true, exact: true, source: object, verb: "holds"}
	}
	if globs, ok := facts.viewBuckets[object]; ok && facts.bucketRows != nil {
		var rows int64
		var matched int
		for name, n := range facts.bucketRows {
			for _, g := range globs {
				if globMatch(g, name) {
					rows += n
					matched++
					break
				}
			}
		}
		if matched > 0 {
			return retentionCoverage{
				rows: rows, known: true, exact: true,
				source: fmt.Sprintf("its %d %s", matched, plural(matched, "bucket")),
				verb:   verbFor(matched),
			}
		}
	}
	if table, ok := facts.viewTable[object]; ok {
		if rows, ok := facts.streamRows[table]; ok {
			return retentionCoverage{
				rows: rows, known: true, exact: false,
				source: "its backing table " + table,
				verb:   "holds",
			}
		}
	}
	return retentionCoverage{}
}

func verbFor(n int) string {
	if n == 1 {
		return "holds"
	}
	return "hold"
}

func plural(n int, word string) string {
	if n == 1 {
		return word
	}
	return word + "s"
}

// applyFreshness turns a last-seen timestamp into live-or-stale. An
// unparseable timestamp downgrades to live-without-age rather than inventing a
// staleness verdict from a value we did not understand.
//
// timeField names the record field the timestamp was read from, for the
// evidence when it yields nothing; "" for a metric family, whose event time
// comes from the timeseries frame rather than a field.
func applyFreshness(sig *Signal, lastSeen, timeField string, now time.Time, staleAfter time.Duration) {
	sig.State = SignalLive
	if lastSeen == "" {
		// Records matched but carried no event time, so this signal can only
		// ever report live — never stale, the one state the whole feature
		// exists to surface. That is exactly the defect a wrong TimeField
		// produces (takeMax over a field the stream lacks yields an undefined
		// column, not an error), and it has to be visible rather than
		// presented as a clean "live".
		sig.Evidence = "matched records, but " + freshnessSource(timeField) +
			" returned no event time, so age could not be computed and this signal cannot be reported stale"
		return
	}
	sig.LastSeen = lastSeen
	ts, err := parseGrailTime(lastSeen)
	if err != nil {
		return
	}
	// A timeseries bucket is stamped with its end, which for the current
	// bucket lies in the future. Reporting a last-seen after the run started
	// reads as a clock bug; clamp it to now.
	if ts.After(now) {
		ts = now
		sig.LastSeen = now.UTC().Format(time.RFC3339)
	}
	age := now.Sub(ts)
	sig.AgeSeconds = int64(age.Seconds())
	if staleAfter > 0 && age > staleAfter {
		sig.State = SignalStale
		sig.Evidence = fmt.Sprintf("data arrived in this window but stopped %s ago (stale after %s)",
			roundDuration(age), roundDuration(staleAfter))
	}
}

// freshnessSource names where a signal's event time was read from, for the
// evidence when it comes back empty.
func freshnessSource(timeField string) string {
	if timeField == "" {
		return "the timeseries frame"
	}
	return "takeMax(" + timeField + ")"
}

// summarizeSeries totals the datapoints of a timeseries result and derives the
// timestamp of the last non-empty interval.
func summarizeSeries(records []map[string]interface{}) (points int64, lastSeen string) {
	for _, rec := range records {
		values, ok := rec["n"].([]interface{})
		if !ok {
			continue
		}
		start, interval, haveFrame := seriesFrame(rec)
		for i, v := range values {
			if v == nil {
				continue
			}
			n := asInt64(v)
			if n <= 0 {
				continue
			}
			points += n
			if haveFrame {
				// A bucket covers [start+i*interval, start+(i+1)*interval); the
				// data it holds is no newer than its end.
				end := start.Add(time.Duration(i+1) * interval)
				if lastSeen == "" || end.Format(time.RFC3339) > lastSeen {
					lastSeen = end.Format(time.RFC3339)
				}
			}
		}
	}
	return points, lastSeen
}

// seriesFrame extracts the bucket grid from a timeseries record.
func seriesFrame(rec map[string]interface{}) (start time.Time, interval time.Duration, ok bool) {
	frame, isMap := rec["timeframe"].(map[string]interface{})
	if !isMap {
		return start, 0, false
	}
	s, _ := frame["start"].(string)
	if s == "" {
		return start, 0, false
	}
	start, err := parseGrailTime(s)
	if err != nil {
		return start, 0, false
	}
	// Grail reports the interval as a nanosecond count, sometimes as a string.
	ns := asInt64(rec["interval"])
	if ns <= 0 {
		return start, 0, false
	}
	return start, time.Duration(ns), true
}

// parseGrailTime parses the timestamp forms Grail returns, which carry up to
// nanosecond precision.
func parseGrailTime(s string) (time.Time, error) {
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339} {
		if ts, err := time.Parse(layout, s); err == nil {
			return ts, nil
		}
	}
	return time.Time{}, fmt.Errorf("unrecognized timestamp %q", s)
}

// roundDuration renders an age the way an operator reads it, not to the
// nanosecond.
func roundDuration(d time.Duration) string {
	switch {
	case d < time.Minute:
		return d.Round(time.Second).String()
	case d < time.Hour:
		return d.Round(time.Minute).String()
	default:
		return d.Round(time.Minute).String()
	}
}

// Summarize counts signals by state.
func Summarize(signals []Signal) *StateSummary {
	sum := &StateSummary{}
	for _, s := range signals {
		switch s.State {
		case SignalLive:
			sum.Live++
		case SignalStale:
			sum.Stale++
		case SignalEmpty:
			sum.Empty++
		case SignalNoData:
			sum.NoData++
		case SignalNotApplicable:
			sum.NotApplicable++
		case SignalAbsent:
			sum.Absent++
		case SignalUnknown:
			sum.Unknown++
		}
	}
	return sum
}

// SignalNames returns, sorted, the capability names windowed arrival mode can
// probe. Callers use it to reject a misspelled signal name before a run
// rather than after it, and to list the choices in the error.
func SignalNames(defs map[string]*CapabilityDef) []string {
	var out []string
	for name, def := range defs {
		if isSignalDef(def) {
			out = append(out, name)
		}
	}
	sort.Strings(out)
	return out
}

// isSignalDef reports whether a capability definition describes a signal type
// that can be asked "is data arriving": a fetchable stream or a metric family.
func isSignalDef(def *CapabilityDef) bool {
	return def != nil && (def.DataObject != "" || def.MetricKey != "")
}

// globMatches returns every key in the family, in a stable order so a run is
// reproducible.
func globMatches(pattern string, keys []string) []string {
	var out []string
	for _, k := range keys {
		if globMatch(pattern, k) {
			out = append(out, k)
		}
	}
	sort.Strings(out)
	return out
}

func containsFold(list []string, s string) bool {
	for _, item := range list {
		if strings.EqualFold(strings.TrimSpace(item), s) {
			return true
		}
	}
	return false
}
