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
// the ladder below does not converge on precision — it stops at the first
// conclusive answer, which is usually the very first and cheapest rung.
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
// Descending is what makes the walk affordable. A hit ends it, so the common
// case costs one near-free query; each further rung costs roughly ten times the
// last, and the walk stops as soon as a rung is itself cut short by the cap. The
// total is therefore bounded at a little over one cap's worth of scan, not one
// per rung.
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
const minSampledRecords = 30

// sampledProbe is one rung's outcome.
type sampledProbe struct {
	// ratio is the *effective* ratio, divided out of the data rather than
	// copied from the request — see readSampledProbe.
	ratio    int64
	matched  int64 // records the sample actually matched
	records  int64 // extrapolated population
	lastSeen string
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

// readSampledProbe reads a rung's result.
func readSampledProbe(res *RunResult) sampledProbe {
	var p sampledProbe
	if len(res.Records) == 0 {
		return p
	}
	r := res.Records[0]
	p.matched = asInt64(r["matched"])
	p.records = asInt64(r["population"])
	p.lastSeen, _ = r["last_seen"].(string)
	// Derive the ratio from the two numbers rather than trusting the request;
	// without a coherent one, report the sample as the count.
	if p.ratio = effectiveRatio(p.records, p.matched); p.ratio == 0 {
		p.records = p.matched
	}
	return p
}

// effectiveRatio is the sampling ratio a sampled count actually ran at: the
// extrapolated population divided by the records the sample matched.
//
// It is divided out of the data rather than copied from the request, because
// Grail rounds every requested ratio down to a power of ten and answers a
// refusal to sample with a mere warning, so the request overstates what was
// read by up to the whole ratio. population is the sum of
// dt.system.sampling_ratio over the matched records. It returns 0 when the
// two numbers do not yield a ratio: nothing was matched, or the population is
// below the sample count, which is incoherent because nothing extrapolates
// downward.
func effectiveRatio(population, matched int64) int64 {
	if matched <= 0 || population < matched {
		return 0
	}
	return population / matched
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

// SampledMissBound is the largest population a count sampled 1-in-ratio that
// matched nothing can still have missed, at about 95% confidence.
//
// This is the one place sampling yields *better* evidence than the cap it
// replaced. A truncated scan covers an unspecified portion of the window and
// supports no bound at all — the match may sit entirely in the part never read.
// A sampled miss covers the whole window uniformly, so the absence is
// quantifiable: with 1-in-R sampling, missing a population of R*3 has
// probability e^-3, just under 5%. Reporting that number is honest in a way
// "the scan stopped early" cannot be.
func SampledMissBound(ratio int64) int64 { return ratio * 3 }

// sampledHitEvidence states what a sampled verdict rests on. Every sampled
// signal carries evidence, including a plain live one: its volume is an
// estimate and its age is biased, and a reader comparing two runs needs to know
// that before treating a difference as a change in the data.
func sampledHitEvidence(object string, p sampledProbe, bias time.Duration, scanLimitGB float64) string {
	s := fmt.Sprintf("%s scans more than %s in this window, so it was probed with 1-in-%s sampling: %s sampled %s extrapolate to about %s",
		object, scanCapLabel(scanLimitGB), formatCount(p.ratio),
		formatCount(p.matched), plural64(p.matched, "record"), formatCount(p.records))
	if bias > 0 {
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
		formatCount(ratio), formatCount(SampledMissBound(ratio)))
}

// sampledAmbiguousEvidence phrases the one band a sampled probe cannot resolve:
// the measured age is past the stale threshold, but by less than the sampling
// bias, so the signal may be perfectly live and merely under-sampled. Claiming
// stale here would raise a false alarm during onboarding, which is worse than
// admitting the probe cannot tell.
func sampledAmbiguousEvidence(object string, age, bias, staleAfter time.Duration, ratio int64) string {
	return fmt.Sprintf("not evaluated: %s had to be sampled at 1-in-%s to fit under the scan cap, and its newest sampled record is %s old against a %s stale threshold — within the ~%s the sampling itself can shift last-seen, so live and stale cannot be told apart here; raise --scan-limit-gbytes for an exact answer",
		object, formatCount(ratio), roundDuration(age), roundDuration(staleAfter), roundDuration(bias))
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

// recoverBySampling is reached when the exhaustive stream probe was cut short.
// It walks samplingLadder for a verdict the cap denied, and returns the capped
// "unknown" unchanged if it cannot find one.
//
// The walk is deliberately biased toward admitting defeat. Every exit that is
// not a genuine hit leaves the signal unknown, because the alternative — a
// sampled miss presented as "empty" — would be this package's central error
// (blaming a source for a question that was never fully asked) reintroduced one
// level down, and at a scale where nobody could check it.
func (b *budgetRunner) recoverBySampling(ctx context.Context, sig Signal, def *CapabilityDef, opts DiscoverOptions, timeField string, now time.Time, cause TruncationCause) (Signal, error) {
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
	// zeroAt remembers the least aggressive rung that came back empty, since
	// that is the rung whose bound is tightest and therefore worth reporting.
	var zeroAt int64
walk:
	for _, ratio := range samplingLadder {
		res, err := b.run(ctx, sampledProbeDQL(def.DataObject, opts, timeField, ratio))
		switch {
		case err == nil:
		case ctx.Err() != nil:
			return sig, ctx.Err()
		default:
			// Out of budget, or the probe failed. Stop walking and resolve
			// with whatever the ladder already established — a bound measured
			// by an earlier rung stays valid regardless of why the next one
			// did not run.
			break walk
		}
		if !res.Sampled {
			// The environment declined to sample this object, so that retry
			// was the original capped query over again and every further rung
			// would be too. Say why the fallback does not apply here.
			sig.State = SignalUnknown
			sig.Truncation = cause
			sig.Evidence = samplingRefusedEvidence(def.DataObject, opts.Since, opts.ScanLimitGBytes)
			return sig, nil
		}
		if res.Truncated {
			// Even sampled this rung does not fit, and every rung below it
			// reads ten times more. Stop descending.
			break walk
		}
		if p := readSampledProbe(res); p.matched > 0 {
			hit = &p
			// A hit is conclusive about arrival at any ratio, but a thin one
			// cannot be trusted about *age* — so keep descending to thicken
			// the sample, holding this rung as the fallback if the next is
			// refused or capped.
			if p.matched >= minSampledRecords {
				break walk
			}
		} else {
			zeroAt = ratio
		}
	}

	if hit == nil {
		if zeroAt == 0 {
			return capped(), nil
		}
		sig.State = SignalUnknown
		sig.Truncation = cause
		sig.Evidence = sampledZeroEvidence(def.DataObject, zeroAt, opts.Since, opts.ScanLimitGBytes)
		return sig, nil
	}
	return b.sampledVerdict(sig, def, opts, timeField, now, *hit), nil
}

// sampledVerdict turns a sampled hit into live, stale, or — in the one band the
// sampling cannot resolve — unknown.
func (b *budgetRunner) sampledVerdict(sig Signal, def *CapabilityDef, opts DiscoverOptions, timeField string, now time.Time, p sampledProbe) Signal {
	sig.Records = p.records
	sig.SamplingRatio = p.ratio
	sig.RecordsSampled = p.matched

	bias := samplingBias(windowDuration(opts.Since), p.matched)
	applyFreshness(&sig, p.lastSeen, timeField, now, opts.StaleAfter)

	// A sampled last-seen is biased old, so a stale verdict has to survive that
	// bias to be worth making. Inside the bias the two states are
	// indistinguishable and the honest answer is that the probe cannot tell —
	// reporting stale there would invent an ingest outage out of the sampling,
	// which during onboarding verification is the most expensive kind of wrong.
	age := time.Duration(sig.AgeSeconds) * time.Second
	if sig.State == SignalStale && age-bias <= opts.StaleAfter {
		sig.State = SignalUnknown
		sig.Evidence = sampledAmbiguousEvidence(def.DataObject, age, bias, opts.StaleAfter, p.ratio)
		return sig
	}

	// Freshness sets evidence only when it has something to flag; a sampled
	// signal always does, so its provenance leads and anything freshness added
	// follows.
	sig.Evidence = joinEvidence(sampledHitEvidence(def.DataObject, p, bias, opts.ScanLimitGBytes), sig.Evidence)
	return sig
}

// joinEvidence composes two evidence clauses into one line.
func joinEvidence(lead, rest string) string {
	if rest == "" {
		return lead
	}
	return lead + "; " + rest
}
