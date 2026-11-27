package profiling

import (
	"errors"
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// DefaultWindow is the length of a window when a bound is missing: the last
// 2h, the 2h after a start, or the 2h before an end.
const DefaultWindow = 2 * time.Hour

// MaxWindow caps the analysis window, for ResolveWindow and for a Handler
// alike. The API itself accepts up to 24h, but an
// analysis reads every sample in its window and holds the result in memory on
// the server, so its cost grows with the window, and the server only runs a
// few analyses per environment at a time. 2h is enough to localize a hotspot;
// how far back the window lies is not limited.
const MaxWindow = 2 * time.Hour

// Window is an analysis window in epoch milliseconds, from From up to To.
// ResolveWindow builds one from a requested timeframe. A Window built by hand
// must end after its start and be at most MaxWindow long, or a Handler refuses
// it; that it lies in the past is ResolveWindow's check alone, since a Handler
// has no clock, and is otherwise left to the API.
type Window struct {
	From int64
	To   int64
	// Default says neither bound was given, so the window is the last
	// DefaultWindow.
	Default bool
}

// Span is the window's length. A window longer than a Duration can express
// has the longest one.
func (w Window) Span() time.Duration {
	ms := satSub(w.To, w.From)
	switch {
	case ms > math.MaxInt64/int64(time.Millisecond):
		return math.MaxInt64
	case ms < math.MinInt64/int64(time.Millisecond):
		return math.MinInt64
	}
	return time.Duration(ms) * time.Millisecond
}

// The errors of ResolveWindow for a window that was read but cannot be
// analysed. A window over MaxWindow is a *WindowTooLongError and a bound that
// could not be read a *WindowBoundError.
var (
	// ErrWindowStartInFuture: the start that was asked for is now or later.
	ErrWindowStartInFuture = errors.New("profiling: the window starts in the future")
	// ErrWindowEndInFuture: the window ends later than now, and its start is
	// in the past or was not given.
	ErrWindowEndInFuture = errors.New("profiling: the window ends in the future")
	// ErrWindowEmpty: the window ends at or before its start.
	ErrWindowEmpty = errors.New("profiling: the window ends at or before its start")
)

// Bound names one end of a window.
type Bound string

const (
	BoundStart Bound = "start"
	BoundEnd   Bound = "end"
)

// WindowBoundError is the error ResolveWindow returns for a bound it could not
// read.
type WindowBoundError struct {
	Bound Bound
	// Value is the bound as it was given.
	Value string
	// Err says what is wrong with it.
	Err error
}

func (e *WindowBoundError) Error() string {
	return fmt.Sprintf("profiling: invalid window %s: %v", e.Bound, e.Err)
}

func (e *WindowBoundError) Unwrap() error { return e.Err }

// WindowTooLongError is the error ResolveWindow and a Handler return for a
// window longer than MaxWindow. The window is refused rather than shortened; Requested holds
// it as asked for, so a caller can narrow it and say that it did.
type WindowTooLongError struct {
	Requested Window
	// Max is the cap the window is over: MaxWindow.
	Max time.Duration
}

func (e *WindowTooLongError) Error() string {
	return fmt.Sprintf("profiling: the window is %s long, over the limit of %s", e.Requested.Span(), e.Max)
}

// ResolveWindow turns a requested start and end into an analysis window. Both
// are ISO-8601/RFC3339, epoch millis or now()-relative (now()-1d, now()-90m,
// now()-1h30m), and either may be empty. A missing bound is DefaultWindow
// from the other: a start alone covers the 2h after it (but not past now), an
// end alone the 2h before it, neither the last 2h.
//
// The window must start in the past, must not end in the future, must be
// positive and must be at most MaxWindow, checked here so an invalid window
// never reaches the API. Each failure has its own error: a *WindowBoundError
// for a bound that cannot be read, ErrWindowStartInFuture,
// ErrWindowEndInFuture, ErrWindowEmpty, and a *WindowTooLongError.
func ResolveWindow(start, end string, now time.Time) (Window, error) {
	nowMs := now.UnixMilli()
	def := DefaultWindow.Milliseconds()
	var from, to int64
	var err error
	if end != "" {
		if to, err = parseTime(end, nowMs); err != nil {
			return Window{}, &WindowBoundError{Bound: BoundEnd, Value: end, Err: err}
		}
	}
	if start != "" {
		if from, err = parseTime(start, nowMs); err != nil {
			return Window{}, &WindowBoundError{Bound: BoundStart, Value: start, Err: err}
		}
	}
	switch {
	case start == "" && end == "":
		to = nowMs
		from = to - def
	case start == "":
		if to < math.MinInt64+def {
			// No time lies DefaultWindow before this one.
			return Window{}, &WindowBoundError{Bound: BoundEnd, Value: end, Err: fmt.Errorf("time %q is out of range", end)}
		}
		from = to - def
	case end == "":
		to = min(satAdd(from, def), nowMs)
	}

	if from >= nowMs {
		if start == "" {
			// The start was derived from the end, so the end is what the
			// caller gave and what lies in the future.
			return Window{}, ErrWindowEndInFuture
		}
		return Window{}, ErrWindowStartInFuture
	}
	if to > nowMs {
		return Window{}, ErrWindowEndInFuture
	}
	w := Window{From: from, To: to, Default: start == "" && end == ""}
	if err := w.check(); err != nil {
		return Window{}, err
	}
	return w, nil
}

// check reports why an analysis does not run on w: ErrWindowEmpty, or a
// *WindowTooLongError. It needs no clock, so it is also what a Handler applies
// to the window of a request.
func (w Window) check() error {
	if w.To <= w.From {
		return ErrWindowEmpty
	}
	if w.Span() > MaxWindow {
		return &WindowTooLongError{Requested: w, Max: MaxWindow}
	}
	return nil
}

// satAdd and satSub are a+b and a-b, held at the nearest int64 where the exact
// result is none. A bound is whatever epoch millisecond the caller wrote, so
// arithmetic on one must not wrap around into a valid-looking window.
func satAdd(a, b int64) int64 {
	switch {
	case b > 0 && a > math.MaxInt64-b:
		return math.MaxInt64
	case b < 0 && a < math.MinInt64-b:
		return math.MinInt64
	}
	return a + b
}

// satAddInt is satAdd for an int, which is narrower than an int64 on a 32-bit
// platform.
func satAddInt(a, b int) int {
	return int(min(max(satAdd(int64(a), int64(b)), math.MinInt), math.MaxInt))
}

func satSub(a, b int64) int64 {
	switch {
	case b < 0 && a > math.MaxInt64+b:
		return math.MaxInt64
	case b > 0 && a < math.MinInt64+b:
		return math.MinInt64
	}
	return a - b
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
	whole := s
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
		// float64(math.MaxInt64) is 2^63, the first value a Duration cannot
		// hold; converting it would not fail but give an arbitrary one.
		// Rounded, not cut off: 2.01 * 1e9 is 2009999999.9999998.
		part := math.Round(n * float64(durationUnits[m[2]]))
		if part >= float64(math.MaxInt64) || time.Duration(part) > math.MaxInt64-d {
			return 0, fmt.Errorf("duration %q is out of range", whole)
		}
		d += time.Duration(part)
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
