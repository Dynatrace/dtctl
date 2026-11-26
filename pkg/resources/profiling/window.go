package profiling

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// MaxWindow caps the analysis window, and is also the length used when a bound
// is missing. The API itself accepts up to 24h, but an analysis reads every
// sample in its window and holds the result in memory on the server, so its
// cost grows with the window, and the server only runs a few analyses per
// environment at a time. 2h is enough to localize a hotspot; how far back the
// window lies is not limited.
const MaxWindow = 2 * time.Hour

// Window is an analysis window in epoch milliseconds, the unit Payload takes.
type Window struct {
	From int64
	To   int64
	// Default says neither bound was given, so the window is the last
	// MaxWindow.
	Default bool
}

// WindowTooLongError is the error ResolveWindow returns for a window longer
// than MaxWindow. The window is refused rather than shortened; Requested holds
// it as asked for, so a caller can narrow it and say that it did.
type WindowTooLongError struct {
	Requested Window
}

func (e *WindowTooLongError) Error() string {
	return fmt.Sprintf("invalid timeframe: the window must not exceed 2h (got %s) — narrow it; the window can lie anywhere in the past",
		time.Duration(e.Requested.To-e.Requested.From)*time.Millisecond)
}

// ResolveWindow turns a requested start and end into an analysis window. Both
// accept what `dtctl query` accepts (ISO-8601/RFC3339, epoch millis,
// now()-relative), and either may be empty. A missing bound is MaxWindow from
// the other: a start alone covers the 2h after it (but not past now), an end
// alone the 2h before it, neither the last 2h. The window must not end in the
// future, must be positive and must be at most MaxWindow (a
// *WindowTooLongError otherwise), checked here so an invalid window never
// reaches the API. The error texts name the dtctl flags the bounds come from.
func ResolveWindow(start, end string, now time.Time) (Window, error) {
	nowMs := now.UnixMilli()
	var from, to int64
	var err error
	if end != "" {
		if to, err = parseTime(end, nowMs); err != nil {
			return Window{}, fmt.Errorf("invalid --default-timeframe-end: %w", err)
		}
	}
	if start != "" {
		if from, err = parseTime(start, nowMs); err != nil {
			return Window{}, fmt.Errorf("invalid --default-timeframe-start: %w", err)
		}
	}
	switch {
	case start == "" && end == "":
		to = nowMs
		from = to - MaxWindow.Milliseconds()
	case start == "":
		from = to - MaxWindow.Milliseconds()
	case end == "":
		to = min(from+MaxWindow.Milliseconds(), nowMs)
	}

	if from >= nowMs {
		return Window{}, fmt.Errorf("invalid timeframe: the start is in the future — profiling only covers the past (e.g. --default-timeframe-start 'now()-2h')")
	}
	if to > nowMs {
		return Window{}, fmt.Errorf("invalid timeframe: the end is in the future — profiling only covers the past; leave --default-timeframe-end out to end now")
	}
	if to <= from {
		return Window{}, fmt.Errorf("invalid timeframe: the end must be after the start")
	}
	w := Window{From: from, To: to, Default: start == "" && end == ""}
	if to-from > MaxWindow.Milliseconds() {
		return Window{}, &WindowTooLongError{Requested: w}
	}
	return w, nil
}

// durationUnits are the duration units a now()-relative bound takes: DQL's,
// plus Go's spellings.
var durationUnits = map[string]time.Duration{
	"ns": time.Nanosecond, "us": time.Microsecond, "µs": time.Microsecond, "ms": time.Millisecond,
	"s": time.Second, "m": time.Minute, "min": time.Minute, "h": time.Hour,
	"d": 24 * time.Hour, "w": 7 * 24 * time.Hour,
}

var durationPart = regexp.MustCompile(`^(\d+(?:\.\d+)?)(ns|us|µs|ms|min|s|m|h|d|w)`)

// parseDuration parses a duration such as 1d, 90m, 1h30m or 1.5h.
func parseDuration(s string) (time.Duration, error) {
	s = strings.ReplaceAll(s, " ", "")
	if s == "" {
		return 0, fmt.Errorf("missing duration")
	}
	var d time.Duration
	for s != "" {
		m := durationPart.FindStringSubmatch(s)
		if m == nil {
			return 0, fmt.Errorf("unknown duration %q: use a number and a unit (ms, s, m, h, d, w), e.g. 1d or 1h30m", s)
		}
		n, err := strconv.ParseFloat(m[1], 64)
		if err != nil {
			return 0, err
		}
		d += time.Duration(n * float64(durationUnits[m[2]]))
		s = s[len(m[0]):]
	}
	return d, nil
}

// parseTime parses one window bound: a now()-relative expression (now(),
// now()-1d, now()-90m, now()-1h30m), epoch millis, or ISO-8601/RFC3339.
func parseTime(s string, nowMs int64) (int64, error) {
	s = strings.TrimSpace(s)
	if rest, ok := strings.CutPrefix(s, "now()"); ok {
		rest = strings.TrimSpace(rest)
		if rest == "" {
			return nowMs, nil
		}
		var sign int64
		switch rest[0] {
		case '-':
			sign = -1
		case '+':
			sign = 1
		default:
			return 0, fmt.Errorf("expected now()-<duration> or now()+<duration>, got %q", s)
		}
		d, err := parseDuration(rest[1:])
		if err != nil {
			return 0, fmt.Errorf("invalid duration in %q: %w", s, err)
		}
		return nowMs + sign*d.Milliseconds(), nil
	}
	return parseTimestamp(s)
}

// timeLayouts are the absolute timestamp forms a bound takes, beyond epoch
// millis. Forms without a zone are UTC.
var timeLayouts = []string{
	time.RFC3339Nano,
	"2006-01-02T15:04:05.999999999",
	"2006-01-02T15:04",
	"2006-01-02",
}

func parseTimestamp(s string) (int64, error) {
	if ms, err := strconv.ParseInt(s, 10, 64); err == nil {
		return ms, nil
	}
	for _, layout := range timeLayouts {
		if t, err := time.Parse(layout, s); err == nil {
			return t.UnixMilli(), nil
		}
	}
	return 0, fmt.Errorf("cannot parse time %q: use ISO-8601/RFC3339, epoch millis, or now()-<dur>", s)
}
