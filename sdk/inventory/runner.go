package inventory

import (
	"strconv"
	"strings"
	"time"

	"github.com/Dynatrace/dtctl/sdk/api/query"
)

// This file is the Runner author's half of the contract.
//
// Discover drives every probe through a Runner, which means the quality of
// several verdicts depends on how faithfully the Runner translates a Grail
// response into a RunResult. Two of those translations are not obvious, and
// getting them wrong does not produce an error — it produces a confident wrong
// answer:
//
//   - ColumnTypes decides whether a metric family reports "n/a" (the scope
//     names a dimension this metric does not have) or "empty" (it has the
//     dimension and simply produced nothing). A Runner that leaves it nil
//     silently converts every n/a into an empty, which blames a source for a
//     question that was never askable of it — the exact failure this package
//     went out of its way to eliminate.
//   - TruncationCause decides whether a capped probe can name the limit that
//     stopped it and the remedy that applies.
//
// Rather than document those as rules to follow, the helpers below do the
// translation, so a Runner over the Dynatrace query API can hand the response
// straight through. See RunResult.

// ColumnTypesOf flattens the query API's per-index-range type blocks into the
// single column→type map RunResult carries.
//
// A response describes types per record-index range, and the same column can
// be described more than once — typically as a real type in one range and as
// "undefined" in another, when only some records carry the field. A known type
// therefore wins: "this column exists somewhere in the result" is the claim the
// applicability probe needs, and letting an undefined range overwrite it would
// invent a missing field.
func ColumnTypesOf(blocks []query.ColumnTypes) map[string]string {
	if len(blocks) == 0 {
		return nil
	}
	out := make(map[string]string)
	for _, b := range blocks {
		for col, t := range b.Mappings {
			if prev, ok := out[col]; ok && prev != TypeUndefined {
				continue
			}
			out[col] = t.Type
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// Truncating notification types, as the query API reports them.
const (
	notifyScanLimit     = "SCAN_LIMIT_GBYTES"
	notifyResultRecords = "RESULT_LIMIT_RECORDS"
	notifyResultBytes   = "RESULT_LIMIT_BYTES"
	notifyFetchTimeout  = "FETCH_TIMEOUT"
	notifyExecTimeLimit = "FETCH_EXEC_TIME_LIMIT"
	notifyConsumption   = "QUERY_CONSUMPTION_LIMIT"
)

// TruncationCauseOf reports which limit, if any, cut a result short.
//
// It returns "" for a notification that does not truncate — sampling, for
// instance, is declared in the query text rather than imposed on it, so a
// sampled result is complete for what it asked.
//
// Some deployments report a cut-short read only in the message, with no
// notificationType, so the message is a fallback rather than a nicety: without
// it a truncated result reads as complete and every row below the cut becomes
// a fabricated absence.
func TruncationCauseOf(n query.Notification) TruncationCause {
	switch n.NotificationType {
	case notifyScanLimit:
		return TruncationScanLimit
	case notifyResultRecords, notifyResultBytes:
		return TruncationResultLimit
	case notifyFetchTimeout, notifyExecTimeLimit:
		return TruncationTimeout
	case notifyConsumption:
		return TruncationConsumption
	}
	msg := strings.ToLower(n.Message)
	switch {
	case strings.Contains(msg, "scan") && strings.Contains(msg, "gigabyte"):
		return TruncationScanLimit
	case strings.Contains(msg, "result has been limited"), strings.Contains(msg, "limited to"):
		return TruncationResultLimit
	case strings.Contains(msg, "internal time limit"):
		return TruncationTimeout
	}
	return ""
}

// FirstTruncationCause reports the first truncating cause among a response's
// notifications, which is the one a RunResult should carry.
func FirstTruncationCause(notifications []query.Notification) TruncationCause {
	for _, n := range notifications {
		if c := TruncationCauseOf(n); c != "" {
			return c
		}
	}
	return ""
}

// NormalizeSince turns a window given as a duration into the DQL timeframe
// expression DiscoverOptions.Since expects, and returns the window length
// alongside it.
//
// Both "15m" and "-15m" are accepted: a lookback is written either way. The
// expression is emitted in whole minutes where possible and seconds otherwise,
// so a Go duration like "1h30m" — which DQL does not accept verbatim — still
// produces a valid timeframe. An empty value yields the default window.
func NormalizeSince(v string) (expr string, window time.Duration, err error) {
	v = strings.TrimSpace(v)
	if v == "" {
		v = DefaultWindow
	}
	d, perr := time.ParseDuration(strings.TrimPrefix(v, "-"))
	if perr != nil {
		return "", 0, &InvalidSinceError{Value: v}
	}
	if d <= 0 {
		return "", 0, &InvalidSinceError{Value: v}
	}
	if d%time.Minute == 0 {
		return "now()-" + strconv.FormatInt(int64(d/time.Minute), 10) + "m", d, nil
	}
	return "now()-" + strconv.FormatInt(int64(d/time.Second), 10) + "s", d, nil
}

// DefaultWindow is the arrival window used when none is given: short enough
// that every probe stays cheap, long enough to survive ordinary ingest jitter.
const DefaultWindow = "15m"

// InvalidSinceError reports a window that is not a positive Go duration.
type InvalidSinceError struct{ Value string }

func (e *InvalidSinceError) Error() string {
	return "invalid window " + e.Value + ": expected a positive duration such as 15m, 2h, or 90s"
}

// DefaultStaleAfter derives the freshness threshold from the window: a third
// of the window, floored at two minutes so a short window does not report
// normally-jittery ingest as stale.
func DefaultStaleAfter(window time.Duration) time.Duration {
	if third := window / 3; third > 2*time.Minute {
		return third
	}
	return 2 * time.Minute
}
