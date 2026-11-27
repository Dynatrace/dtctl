package inventory

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Sampled recovery from the scan cap.
//
// On a high-volume tenant the scan cap is the dominant source of "unknown":
// the probe for logs or spans would scan more than --scan-limit-gbytes inside
// the window, so it is stopped before it can count, and the signal the
// operator came to check is exactly the one that gets no verdict. Narrowing
// --since only helps if it is narrowed below the point where the question is
// still worth asking.
//
// Grail's `samplingRatio` is the one lever that reduces the *scan* rather than
// the result: measured on a large tenant, an unscoped 15-minute count of logs
// scanned 500 GB (capped, partial) unsampled, 14.4 GB at 1-in-1000, and 0.18 GB
// at 1-in-100000 — and the scoped form of the same probe fell from 341 GB to
// 0.92 GB while landing within 1.5% of the true count. Aggregation cannot do
// that; it still scans everything.
//
// What makes this usable for arrival verification is an asymmetry. Sampling
// only ever drops records, so a sampled *hit* proves arrival at any ratio: it
// is as conclusive as an exhaustive one. A sampled *miss* proves much less. So
// the ladder below does not converge on a precise count: it stops at the first
// rung whose sample is thick enough to date the arrival as well as prove it,
// which is usually the very first and cheapest rung.
//
// The error that remains scales inversely with volume, and the scan cap only
// binds on high volume, so the two failure modes barely overlap: the signals
// sampling is imprecise about are the ones that never needed it.

// samplingLadder is the sequence of ratios the recovery walks, most aggressive
// first. Only the decades are real: Grail rounds every ratio *down* to a power
// of ten (300 becomes 100, 2 becomes 1) and caps at 100000, so there is no
// finer step to take and asking for one silently gets a coarser probe than the
// evidence would claim.
//
// Descending is what makes the walk affordable. A rung that matches
// minSampledRecords or more ends it, and so does any hit once a record inside
// the stale threshold has been seen, so the common case costs one near-free
// query. A thinner hit whose newest record is older than that does not: it
// proves arrival but cannot date it, so the walk keeps descending to thicken
// the sample, and it also continues past a rung that matched nothing, flagged
// as sampled or not. Each further rung reads
// roughly ten times the last, and the walk ends at the first rung that is cut
// short, so while every rung is sampled the total is bounded at a little over
// one cap's worth of scan, not one per rung.
//
// That bound is an argument about sampled rungs only. A rung the environment
// answered without sampling reads everything. The walk ends on such a rung
// when it matched a record or was cut short, which is how the unsampled retry
// of a capped stream comes back, but nothing in a response says how much an
// unflagged rung that matched nothing has scanned.
var samplingLadder = []int64{100000, 10000, 1000, 100, 10}

// minSampledRecords is how many sampled records a rung must match before its
// age is trusted, and it is a correctness threshold rather than a nicety.
//
// takeMax over a sample is biased old, because the newest record is unlikely to
// be among the ones read. The gap is about one sampling interval — window
// divided by the number of sampled records — which the measurements bear out:
// 11 sampled records over 15 minutes put last-seen 91s behind the truth, while
// 676 put it within a second. At 30 records the bias is a thirtieth of the
// window, comfortably inside a stale threshold that defaults to a third of it,
// so a live signal cannot be flipped to stale by the sampling alone.
//
// The floor does two things. The walk keeps descending until a rung reaches
// it, unless a record inside the stale threshold has already settled the
// verdict as live, and a hit that never reaches it may not say stale (see
// sampledVerdict). The
// sampling interval is the gap to expect for a source that emits evenly, not a
// bound: a burst early in the window followed by a trickle leaves a thin
// sample with only old timestamps while the source is alive. Subtracting the
// interval from the age does not help there, so below the floor an old
// timestamp is reported as unknown.
const minSampledRecords = 30

// sampledProbe is one rung's outcome.
type sampledProbe struct {
	// asked is the ratio the rung requested. It names the rung; it is never a
	// multiplier.
	asked int64
	// askedAt is the clock reading taken just before the rung was issued. Each
	// rung asks the server for a window that ends when the rung runs, so this
	// is the instant an age read from its result is measured from.
	askedAt time.Time
	// ratio is the *effective* ratio, divided out of the data rather than
	// copied from the request — see readSampledProbe.
	ratio    int64
	matched  int64 // records the sample actually matched
	records  int64 // extrapolated population
	lastSeen string
}

// sighting is the newest record a walk has matched so far, and the rung that
// matched it.
//
// It is kept apart from the hit because the two answer different questions.
// The hit is the sample the count is extrapolated from, so it has to be a
// response that confirmed it sampled and ran to completion. A timestamp needs
// neither: every rung filters the same window by the same scope, so a record
// any of them matched is a record of this scope, and the newest of those is
// the best lower bound on freshness the walk has.
type sighting struct {
	lastSeen string
	at       time.Time
	// askedAt is the reading of the rung that matched the record. The age is
	// measured from it, not from the reading of the rung the walk ended on:
	// what is known is how old the record was when it was seen.
	askedAt time.Time
	// asked is the ratio of the rung that matched the record, 0 for the
	// exhaustive probe that hit the cap.
	asked int64
}

// newerSighting returns the later of seen and the newest record p matched. A
// response that matched nothing dates nothing, whatever its last_seen column
// holds, and neither does a timestamp that does not parse.
func newerSighting(seen *sighting, p sampledProbe) *sighting {
	if p.matched <= 0 {
		return seen
	}
	at, err := parseGrailTime(p.lastSeen)
	if err != nil || (seen != nil && at.Before(seen.at)) {
		return seen
	}
	return &sighting{lastSeen: p.lastSeen, at: at, askedAt: p.askedAt, asked: p.asked}
}

// insideThreshold reports whether the newest record seen is fresh by the rule
// applyFreshness uses, so that a walk which ends on it is certain of the
// verdict it ends with.
func (s *sighting) insideThreshold(staleAfter time.Duration) bool {
	return s != nil && (staleAfter <= 0 || s.askedAt.Sub(s.at) <= staleAfter)
}

// sampledProbeDQL builds a rung's probe.
//
// `sum(dt.system.sampling_ratio)` is not redundant with the requested ratio: it
// is the only truthful extrapolation available. Grail answers 200 OK with a
// mere warning both when it rounds a ratio down and when it refuses to sample
// the object at all, so a probe that multiplied by what it asked for would
// overstate a refused object by exactly that factor. The per-record ratio sums
// to the population regardless of what the engine decided to do.
func sampledProbeDQL(object string, opts DiscoverOptions, timeField string, ratio int64) string {
	return fmt.Sprintf("fetch %s, from:%s, samplingRatio:%d | filter %s | summarize matched = count(), last_seen = takeMax(%s), population = sum(dt.system.sampling_ratio)",
		object, opts.Since, ratio, opts.Scope, timeField)
}

// readSampledProbe reads the result of the rung that asked for the given ratio
// at the given clock reading.
func readSampledProbe(res *RunResult, asked int64, askedAt time.Time) sampledProbe {
	p := sampledProbe{asked: asked, askedAt: askedAt}
	if len(res.Records) == 0 {
		return p
	}
	r := res.Records[0]
	p.matched = asInt64(r["matched"])
	p.records = asInt64(r["population"])
	p.lastSeen, _ = r["last_seen"].(string)
	// Derive the ratio from the two numbers rather than trusting the request.
	// A population below the sample count is incoherent (nothing can extrapolate
	// downward), so fall back to reporting the sample as the count.
	if p.matched > 0 && p.records >= p.matched {
		p.ratio = p.records / p.matched
	} else {
		p.records = p.matched
	}
	return p
}

// samplingBias is the amount by which a sampled takeMax under-reports
// freshness: roughly one sampling interval, the window divided by the number of
// records the sample matched.
func samplingBias(window time.Duration, matched int64) time.Duration {
	if window <= 0 || matched <= 0 {
		return 0
	}
	return window / time.Duration(matched)
}

// windowDuration recovers the window's length from the normalized timeframe
// expression, for the freshness-bias arithmetic. It is parsed back rather than
// threaded through DiscoverOptions so a caller cannot supply a Since and a
// window that disagree.
func windowDuration(since string) time.Duration {
	rest, ok := strings.CutPrefix(strings.TrimSpace(since), "now()-")
	if !ok {
		return 0
	}
	d, err := time.ParseDuration(rest)
	if err != nil || d <= 0 {
		return 0
	}
	return d
}

// sampledZeroBound is the largest population a sampled probe that matched
// nothing can still have missed, at about 95% confidence.
//
// This is the one place sampling yields *better* evidence than the cap it
// replaced. A truncated scan covers an unspecified portion of the window and
// supports no bound at all — the match may sit entirely in the part never read.
// A sampled miss covers the whole window uniformly, so the absence is
// quantifiable: with 1-in-R sampling, missing a population of R*3 has
// probability e^-3, just under 5%. Reporting that number is honest in a way
// "the scan stopped early" cannot be.
func sampledZeroBound(ratio int64) int64 { return ratio * 3 }

// sampledHitEvidence states what a sampled verdict rests on. Every sampled
// signal carries evidence, including a plain live one: its volume is an
// estimate and its age is biased, and a reader comparing two runs needs to know
// that before treating a difference as a change in the data.
//
// newest is the record the age was computed from. When another rung of the
// walk matched it, the sentence about the sample's bias would describe a
// timestamp the verdict did not use, so the evidence names the rung instead.
func sampledHitEvidence(object string, p sampledProbe, newest *sighting, bias time.Duration, scanLimitGB float64) string {
	s := fmt.Sprintf("%s scans more than %s in this window, so it was probed with 1-in-%s sampling: %s sampled %s extrapolate to about %s",
		object, scanCapLabel(scanLimitGB), formatCount(p.ratio),
		formatCount(p.matched), plural64(p.matched, "record"), formatCount(p.records))
	switch {
	case newest != nil && newest.asked == 0:
		s += ", and last-seen was read from the unsampled probe that hit the cap, which matched a newer record than this sample did"
	case newest != nil && newest.asked != p.asked:
		s += fmt.Sprintf(", and last-seen was read from the 1-in-%s probe of the same walk, which matched a newer record than this sample did", formatCount(newest.asked))
	case bias > 0:
		s += fmt.Sprintf(", and last-seen is biased up to ~%s old because the newest record is unlikely to be in the sample", roundDuration(bias))
	}
	return s
}

// sampledZeroEvidence phrases a sampled probe that matched nothing. The state
// stays unknown: a sampled miss is a bound, not an absence, and this feature's
// whole purpose is to keep those apart.
func sampledZeroEvidence(object string, ratio int64, since string, scanLimitGB float64) string {
	return fmt.Sprintf("not evaluated: %s scans more than %s over %s, so it was sampled instead — 1-in-%s matched nothing, which bounds this scope at under ~%s records in the window (95%%) but does not establish absence; raise --scan-limit-gbytes to count it exactly, or narrow --since",
		object, scanCapLabel(scanLimitGB), windowLabel(since),
		formatCount(ratio), formatCount(sampledZeroBound(ratio)))
}

// sampledAmbiguousEvidence phrases the one band a sampled probe cannot resolve:
// the measured age is past the stale threshold, but by less than the sampling
// bias, so the signal may be perfectly live and merely under-sampled. Claiming
// stale here would raise a false alarm during onboarding, which is worse than
// admitting the probe cannot tell.
func sampledAmbiguousEvidence(object string, age, bias, staleAfter time.Duration, ratio int64) string {
	return fmt.Sprintf("not evaluated: %s had to be sampled at 1-in-%s to fit under the scan cap, and the newest record it matched is %s old against a %s stale threshold — within the ~%s the sampling itself can shift last-seen, so live and stale cannot be told apart here; raise --scan-limit-gbytes for an exact answer",
		object, formatCount(ratio), roundDuration(age), roundDuration(staleAfter), roundDuration(bias))
}

// sampledThinEvidence phrases a hit that is too thin to date. It keeps what the
// sample did establish, that records of this scope arrived in the window, and
// says why the age next to it is not a verdict.
func sampledThinEvidence(object string, p sampledProbe, age, staleAfter time.Duration) string {
	return fmt.Sprintf("not evaluated: %s had to be sampled at 1-in-%s to fit under the scan cap, and %s sampled %s are proof that data arrived but too few to say when the data stopped: the newest record seen is %s old against a %s stale threshold, and a sample this thin can miss a newer one; raise --scan-limit-gbytes for an exact answer",
		object, formatCount(p.ratio), formatCount(p.matched), plural64(p.matched, "record"),
		roundDuration(age), roundDuration(staleAfter))
}

// samplingRefusedEvidence phrases the case where the environment would not
// sample the object, so the retry was the original capped query over again.
//
// Grail reports this as a warning on a 200, not an error: `events`, `bizevents`
// and `security.events` silently fall back to reading everything. There is no
// second lever for them, so the verdict is the one the cap already gave — said
// once, with the reason the recovery did not apply.
func samplingRefusedEvidence(object string, since string, scanLimitGB float64) string {
	return fmt.Sprintf("not evaluated: %s scans more than %s over %s, and this environment does not support sampling %s, so there is no cheaper probe to fall back to — raise --scan-limit-gbytes or narrow --since",
		object, scanCapLabel(scanLimitGB), windowLabel(since), object)
}

// formatCount renders a count compactly for evidence prose: exact numbers stay
// exact, but a sampled extrapolation is an estimate and "about 9.7M" is both
// shorter and more honest than "about 9688000".
func formatCount(n int64) string {
	switch {
	case n >= 1_000_000_000 && n%1_000_000_000 == 0:
		return strconv.FormatInt(n/1_000_000_000, 10) + "B"
	case n >= 1_000_000_000:
		return strconv.FormatFloat(float64(n)/1e9, 'f', 1, 64) + "B"
	case n >= 1_000_000 && n%1_000_000 == 0:
		return strconv.FormatInt(n/1_000_000, 10) + "M"
	case n >= 1_000_000:
		return strconv.FormatFloat(float64(n)/1e6, 'f', 1, 64) + "M"
	case n >= 10_000 && n%1_000 == 0:
		return strconv.FormatInt(n/1_000, 10) + "k"
	}
	return strconv.FormatInt(n, 10)
}

// plural64 is plural for an int64 count.
func plural64(n int64, word string) string {
	if n == 1 {
		return word
	}
	return word + "s"
}

// recoverBySampling is reached when the exhaustive stream probe was cut short,
// and is handed that probe's result and the clock reading it was issued at. It
// walks samplingLadder for a verdict the cap denied, and returns the capped
// "unknown" unchanged if it cannot find one.
//
// clock is read again before every rung. The walk can run six queries, and
// each asks for a window relative to the moment it runs, so one reading taken
// before the first of them would date the last rung's records too young by
// however long the walk took.
//
// The walk is deliberately biased toward admitting defeat. Every exit that is
// not a genuine hit leaves the signal unknown, because the alternative — a
// sampled miss presented as "empty" — would be this package's central error
// (blaming a source for a question that was never fully asked) reintroduced one
// level down, and at a scale where nobody could check it.
func (b *budgetRunner) recoverBySampling(ctx context.Context, sig Signal, def *CapabilityDef, opts DiscoverOptions, timeField string, clock func() time.Time, now time.Time, exhaustive *RunResult) (Signal, error) {
	cause := exhaustive.TruncationCause
	capped := func() Signal {
		sig.State = SignalUnknown
		sig.Truncation = cause
		sig.Evidence = truncatedEvidence(def.DataObject, cause, opts.Since, opts.ScanLimitGBytes)
		return sig
	}
	// Only a scan cap is a volume problem, and only a volume problem is one
	// sampling can solve. A result cap or a timeout would survive the retry
	// unchanged, and spending a query to rediscover that is pure cost.
	if cause != TruncationScanLimit || opts.DisableSampling {
		return capped(), nil
	}

	var hit *sampledProbe
	// newest is read from every response, before the rung is classified: a
	// refused or cut-short rung supplies no count, but a record it matched is
	// still the newest this scope is known to have. The capped probe is the
	// first such response. What it counted is not a count of the window, and
	// it is not a hit; its timestamp is a record's all the same.
	newest := newerSighting(nil, readSampledProbe(exhaustive, 0, now))
	// zeroAt remembers the least aggressive sampled rung that came back empty,
	// since that is the rung whose bound is tightest and therefore worth
	// reporting.
	var zeroAt int64
	// flagged records that some rung of this walk confirmed it sampled, which
	// is the one thing that rules out "this environment does not sample the
	// object".
	var flagged bool
	// stopped says why the walk ended early when a rung failed or the budget
	// refused it. It is reported only when no hit was made: a hit is a verdict
	// of its own, while a capped verdict or a bound next to an interrupted
	// walk would otherwise read as the most the ladder could find.
	var stopped string
walk:
	for _, ratio := range samplingLadder {
		askedAt := clock()
		res, err := b.run(ctx, sampledProbeDQL(def.DataObject, opts, timeField, ratio))
		switch {
		case err == nil:
		case ctx.Err() != nil:
			return sig, ctx.Err()
		case err == errBudgetExhausted:
			// Out of budget. Stop walking and resolve with whatever the
			// ladder already established — a bound measured by an earlier
			// rung stays valid regardless of why the next one did not run.
			stopped = "sampled retry stopped: discovery budget exhausted"
			break walk
		default:
			// The probe failed, and the same holds.
			stopped = "sampled retry failed: " + firstLine(err.Error())
			break walk
		}
		p := readSampledProbe(res, ratio, askedAt)
		newest = newerSighting(newest, p)
		if !res.Sampled {
			if p.matched == 0 && !res.Truncated {
				// Nothing matched and nothing was cut short, so this response
				// does not show the original capped query running again: a
				// refused rung reads everything, and reading everything is
				// what hit the cap. A sample too thin to read a single record
				// may come back without the flag as well. Under either reading
				// this rung is empty rather than refused, and the next one is
				// still worth asking. It lends no bound: nothing in the
				// response confirms that the window was sampled at this ratio.
				continue
			}
			// The response looks like the original capped query run again, so
			// every further rung would too, and the walk ends here. A hit an
			// earlier rung already made stands.
			if hit != nil {
				break walk
			}
			// Without one, say why the fallback does not apply, but only when
			// that is what the walk saw. A rung that confirmed it sampled
			// shows the environment does sample this object, and a response
			// cut short by something other than the scan limit is not the
			// capped query over again. Neither supports a statement about the
			// environment, and a bound an earlier rung measured does not
			// stand next to a response the walk cannot read, so both keep the
			// capped verdict, which claims nothing.
			if flagged || (res.Truncated && res.TruncationCause != TruncationScanLimit) {
				return capped(), nil
			}
			sig.State = SignalUnknown
			sig.Truncation = cause
			sig.Evidence = samplingRefusedEvidence(def.DataObject, opts.Since, opts.ScanLimitGBytes)
			return sig, nil
		}
		flagged = true
		if res.Truncated {
			// Even sampled this rung does not fit, and every rung below it
			// reads ten times more. Stop descending.
			break walk
		}
		if p.matched > 0 {
			// The thickest sample supplies the count. A later rung that
			// matched fewer records than one already held is not an
			// improvement, and its timestamp has been read above either way.
			if hit == nil || p.matched >= hit.matched {
				hit = &p
			}
			// A hit is conclusive about arrival at any ratio, but a thin one
			// cannot be trusted about *age* — so keep descending to thicken
			// the sample, holding the best rung so far as the fallback if the
			// next is refused or capped.
			if p.matched >= minSampledRecords {
				break walk
			}
			// Unless the age no longer matters. A sample is a subset, so the
			// newest record in the window is no older than the newest one
			// seen: once that is inside the stale threshold the signal is
			// live, and a finer rung could only repeat it at ten times the
			// scan.
			if newest.insideThreshold(opts.StaleAfter) {
				break walk
			}
		} else {
			zeroAt = ratio
		}
	}

	if hit == nil {
		if zeroAt == 0 {
			sig = capped()
			sig.Evidence = joinEvidence(sig.Evidence, stopped)
			return sig, nil
		}
		sig.State = SignalUnknown
		sig.Truncation = cause
		sig.Evidence = joinEvidence(sampledZeroEvidence(def.DataObject, zeroAt, opts.Since, opts.ScanLimitGBytes), stopped)
		return sig, nil
	}
	return b.sampledVerdict(sig, def, opts, timeField, *hit, newest), nil
}

// sampledVerdict turns a sampled hit into live, stale, or unknown. Unknown is
// the answer in two cases: the hit is below minSampledRecords and looks old,
// or it is thick enough to date but its age is past the threshold by less
// than the sampling can shift it.
//
// The count, the ratio and the bias are those of the hit. The age is that of
// newest, the newest record any rung of the walk matched, which is the hit's
// own unless another rung saw a later one, measured from the clock reading of
// the rung that matched it. newest is nil when no rung returned a timestamp
// that parses, and the hit's raw value is then all there is to report.
func (b *budgetRunner) sampledVerdict(sig Signal, def *CapabilityDef, opts DiscoverOptions, timeField string, p sampledProbe, newest *sighting) Signal {
	sig.Records = p.records
	sig.SamplingRatio = p.ratio
	sig.RecordsSampled = p.matched

	// The hit's own timestamp stands unless another rung saw a strictly newer
	// record, so the evidence never credits another rung with a tie.
	if own := newerSighting(nil, p); own != nil && (newest == nil || !newest.at.After(own.at)) {
		newest = own
	}
	lastSeen, askedAt := p.lastSeen, p.askedAt
	if newest != nil {
		lastSeen, askedAt = newest.lastSeen, newest.askedAt
	}
	bias := samplingBias(windowDuration(opts.Since), p.matched)
	applyFreshness(&sig, lastSeen, timeField, askedAt, opts.StaleAfter)

	// A hit below the floor proves arrival and cannot date it, so it never
	// says stale: an old timestamp there is unknown, with the count kept.
	age := time.Duration(sig.AgeSeconds) * time.Second
	if sig.State == SignalStale && p.matched < minSampledRecords {
		sig.State = SignalUnknown
		sig.Evidence = sampledThinEvidence(def.DataObject, p, age, opts.StaleAfter)
		return sig
	}

	// A sampled last-seen is biased old, so a stale verdict has to survive that
	// bias to be worth making. The sample's bias bounds the gap also when the
	// timestamp came from another rung: that record is no older than the
	// sample's own newest. Inside the bias the two states are
	// indistinguishable and the honest answer is that the probe cannot tell —
	// reporting stale there would invent an ingest outage out of the sampling,
	// which during onboarding verification is the most expensive kind of wrong.
	if sig.State == SignalStale && age-bias <= opts.StaleAfter {
		sig.State = SignalUnknown
		sig.Evidence = sampledAmbiguousEvidence(def.DataObject, age, bias, opts.StaleAfter, p.ratio)
		return sig
	}

	// Freshness sets evidence only when it has something to flag; a sampled
	// signal always does, so its provenance leads and anything freshness added
	// follows.
	sig.Evidence = joinEvidence(sampledHitEvidence(def.DataObject, p, newest, bias, opts.ScanLimitGBytes), sig.Evidence)
	return sig
}

// joinEvidence composes two evidence clauses into one line.
func joinEvidence(lead, rest string) string {
	if rest == "" {
		return lead
	}
	return lead + "; " + rest
}
