package profiling

import (
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// DefaultWindow is the length of a window when a bound is missing: the last
// 2h, the 2h after a start, or the 2h before an end.
const DefaultWindow = 2 * time.Hour

// MaxWindow caps the analysis window. The API itself accepts up to 24h, but an
// analysis reads every sample in its window and holds the result in memory on
// the server, so its cost grows with the window, and the server only runs a
// few analyses per environment at a time. 2h is enough to localize a hotspot;
// how far back the window lies is not limited.
const MaxWindow = 2 * time.Hour

// Window is an analysis window in epoch milliseconds, from From up to To.
type Window struct {
	From int64
	To   int64
	// Default says neither bound was given, so the window is the last
	// DefaultWindow.
	Default bool
}

// Span is the window's length.
func (w Window) Span() time.Duration {
	return time.Duration(w.To-w.From) * time.Millisecond
}

// The errors of ResolveWindow for a window that was read but cannot be
// analysed. A window over MaxWindow is a *WindowTooLongError and a bound that
// could not be read a *WindowBoundError.
var (
	// ErrWindowStartInFuture: the window starts now or later.
	ErrWindowStartInFuture = errors.New("profiling: the window starts in the future")
	// ErrWindowEndInFuture: the window starts in the past and ends later than
	// now.
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

// WindowTooLongError is the error ResolveWindow returns for a window longer
// than MaxWindow. The window is refused rather than shortened; Requested holds
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
// accept what `dtctl query` accepts (ISO-8601/RFC3339, epoch millis,
// now()-relative), and either may be empty. A missing bound is DefaultWindow
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
		from = to - def
	case end == "":
		to = min(from+def, nowMs)
	}

	if from >= nowMs {
		return Window{}, ErrWindowStartInFuture
	}
	if to > nowMs {
		return Window{}, ErrWindowEndInFuture
	}
	if to <= from {
		return Window{}, ErrWindowEmpty
	}
	w := Window{From: from, To: to, Default: start == "" && end == ""}
	if w.Span() > MaxWindow {
		return Window{}, &WindowTooLongError{Requested: w, Max: MaxWindow}
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
