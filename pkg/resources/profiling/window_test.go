package profiling

import (
	"errors"
	"math"
	"testing"
	"time"
)

func TestParseTimestamp(t *testing.T) {
	cases := []struct {
		input   string
		wantMs  int64
		wantErr bool
	}{
		{"1700000000000", 1700000000000, false},
		{"2023-11-14T22:13:20Z", 1700000000000, false},
		{"not-a-time", 0, true},
		{"", 0, true},
	}

	for _, tc := range cases {
		got, err := parseTimestamp(tc.input)
		if (err != nil) != tc.wantErr {
			t.Errorf("parseTimestamp(%q) error = %v, wantErr %v", tc.input, err, tc.wantErr)
			continue
		}
		if !tc.wantErr && got != tc.wantMs {
			t.Errorf("parseTimestamp(%q) = %d, want %d", tc.input, got, tc.wantMs)
		}
	}
}

func TestParseTime_NowRelative(t *testing.T) {
	const now = int64(1_700_000_000_000)
	const h = int64(3600_000)
	cases := []struct {
		input   string
		want    int64
		wantErr bool
	}{
		{"now()", now, false},
		{"now()-1h", now - h, false},
		{"now()-30min", now - h/2, false},
		{"now()+15m", now + h/4, false},
		// what dtctl query takes: days, weeks, compound and fractional durations
		{"now()-1d", now - 24*h, false},
		{"now()-1w", now - 7*24*h, false},
		{"now()-1h30m", now - 3*h/2, false},
		{"now()-1.5h", now - 3*h/2, false},
		{"now() - 2h", now - 2*h, false},
		{"1700000000000", now, false},
		{"2023-11-14T22:13:20Z", now, false},
		{"2023-11-14T22:13:20.000+00:00", now, false},
		{"2023-11-14T22:13:20", now, false}, // no zone: UTC
		{"now()-", 0, true},
		{"now()*5m", 0, true},
		{"now()-1y", 0, true},
		{"now()-1h-", 0, true},
	}
	for _, tc := range cases {
		got, err := parseTime(tc.input, now)
		if (err != nil) != tc.wantErr {
			t.Errorf("parseTime(%q) error = %v, wantErr %v", tc.input, err, tc.wantErr)
			continue
		}
		if !tc.wantErr && got != tc.want {
			t.Errorf("parseTime(%q) = %d, want %d", tc.input, got, tc.want)
		}
	}
}

func TestResolveWindow(t *testing.T) {
	const now = int64(1_700_100_000_000)
	const h = int64(3600_000)
	cases := []struct {
		name, start, end string
		from, to         int64
	}{
		{name: "default: the last 2h", from: now - 2*h, to: now},
		{name: "explicit window", start: "1700000000000", end: "1700003600000", from: 1700000000000, to: 1700003600000},
		{name: "start alone: the 2h after it", start: "now()-1d", from: now - 24*h, to: now - 22*h},
		{name: "start alone, near now: ends now", start: "now()-1h", from: now - h, to: now},
		{name: "end alone: the 2h before it", end: "now()-1d", from: now - 26*h, to: now - 24*h},
		{name: "exactly 2h, far back", start: "now()-21h", end: "now()-19h", from: now - 21*h, to: now - 19*h},
		{name: "before the epoch", start: "1960-01-01", from: -315619200000, to: -315619200000 + 2*h},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w, err := ResolveWindow(tc.start, tc.end, time.UnixMilli(now))
			if err != nil || w.From != tc.from || w.To != tc.to {
				t.Fatalf("got (%d, %d, %v), want (%d, %d)", w.From, w.To, err, tc.from, tc.to)
			}
		})
	}
}

// A window that cannot be analysed is refused with an error a caller can
// branch on, without reading its text.
func TestResolveWindow_Errors(t *testing.T) {
	now := time.UnixMilli(1_700_100_000_000)
	cases := []struct {
		name, start, end string
		is               error
		text             string
	}{
		{name: "end before start", start: "1700003600000", end: "1700000000000", is: ErrWindowEmpty,
			text: "profiling: the window ends at or before its start"},
		{name: "end at start", start: "1700000000000", end: "1700000000000", is: ErrWindowEmpty,
			text: "profiling: the window ends at or before its start"},
		{name: "start in the future", start: "now()+5m", end: "now()+10m", is: ErrWindowStartInFuture,
			text: "profiling: the window starts in the future"},
		{name: "start now", start: "now()", is: ErrWindowStartInFuture,
			text: "profiling: the window starts in the future"},
		{name: "start alone, in the future", start: "9223372036854775807", is: ErrWindowStartInFuture,
			text: "profiling: the window starts in the future"},
		{name: "end in the future", start: "now()-1h", end: "now()+1h", is: ErrWindowEndInFuture,
			text: "profiling: the window ends in the future"},
		{name: "end alone, in the future", end: "now()+1ms", is: ErrWindowEndInFuture,
			text: "profiling: the window ends in the future"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w, err := ResolveWindow(tc.start, tc.end, now)
			if !errors.Is(err, tc.is) {
				t.Fatalf("err = %v, want %v", err, tc.is)
			}
			if err.Error() != tc.text {
				t.Errorf("err = %q, want %q", err, tc.text)
			}
			if w != (Window{}) {
				t.Errorf("a refused window must not be returned, got %+v", w)
			}
		})
	}
}

// A bound that cannot be read says which bound, what was given and why. The
// end is read first, so it is the one reported when both are unreadable.
func TestResolveWindow_UnreadableBound(t *testing.T) {
	now := time.UnixMilli(1_700_100_000_000)
	cases := []struct {
		name, start, end string
		bound            Bound
		value, why       string
	}{
		{name: "unknown unit", start: "now()-1y", bound: BoundStart, value: "now()-1y",
			why: `invalid duration in "now()-1y": unknown duration "1y": use a number and a unit (ms, s, m, h, d, w), e.g. 1d or 1h30m`},
		{name: "not a time", end: "bogus", bound: BoundEnd, value: "bogus",
			why: `cannot parse time "bogus": use ISO-8601/RFC3339, epoch millis, or now()-<dur>`},
		{name: "both", start: "yesterday", end: "tomorrow", bound: BoundEnd, value: "tomorrow",
			why: `cannot parse time "tomorrow": use ISO-8601/RFC3339, epoch millis, or now()-<dur>`},
		{name: "now() without a sign", start: "now()*5m", bound: BoundStart, value: "now()*5m",
			why: `expected now()-<duration> or now()+<duration>, got "now()*5m"`},
		// An end with no time 2h before it, and a duration no time is that
		// far from: both used to wrap around into a valid-looking window.
		{name: "end alone, before every start", end: "-9223372036854775808", bound: BoundEnd, value: "-9223372036854775808",
			why: `time "-9223372036854775808" is out of range`},
		{name: "duration out of range", start: "now()-99999999999999999999w", bound: BoundStart, value: "now()-99999999999999999999w",
			why: `invalid duration in "now()-99999999999999999999w": duration "99999999999999999999w" is out of range`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ResolveWindow(tc.start, tc.end, now)
			var bound *WindowBoundError
			if !errors.As(err, &bound) {
				t.Fatalf("err = %v (%T), want a *WindowBoundError", err, err)
			}
			if bound.Bound != tc.bound || bound.Value != tc.value || bound.Err.Error() != tc.why {
				t.Errorf("got bound %q, value %q, why %q\nwant      %q,       %q,     %q", bound.Bound, bound.Value, bound.Err, tc.bound, tc.value, tc.why)
			}
			if want := "profiling: invalid window " + string(tc.bound) + ": " + tc.why; err.Error() != want {
				t.Errorf("err = %q, want %q", err, want)
			}
		})
	}
}

// Only a window resolved from no bounds at all is the default one, and a
// missing bound is DefaultWindow from the other.
func TestResolveWindow_Default(t *testing.T) {
	now := time.UnixMilli(1_700_100_000_000)
	cases := []struct {
		name, start, end string
		want             bool
		span             time.Duration
	}{
		{"no bounds", "", "", true, DefaultWindow},
		{"start alone, cut off at now", "now()-1h", "", false, time.Hour},
		{"start alone", "now()-1d", "", false, DefaultWindow},
		{"end alone", "", "now()-1h", false, DefaultWindow},
		{"both", "now()-90m", "now()", false, 90 * time.Minute},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w, err := ResolveWindow(tc.start, tc.end, now)
			if err != nil {
				t.Fatal(err)
			}
			if w.Default != tc.want {
				t.Errorf("Default = %v, want %v", w.Default, tc.want)
			}
			if w.Span() != tc.span {
				t.Errorf("Span() = %s, want %s", w.Span(), tc.span)
			}
		})
	}
}

// A window over the cap is refused, not shortened, and the error carries the
// window as it was asked for and the cap.
func TestResolveWindow_TooLongReportsTheRequest(t *testing.T) {
	const nowMs = int64(1_700_100_000_000)
	const h = int64(3600_000)
	now := time.UnixMilli(nowMs)
	cases := []struct {
		name, start, end string
		tooLong          bool
		from, to         int64
		text             string
	}{
		{name: "exactly the cap", start: "now()-2h", end: "now()"},
		{name: "one millisecond over", start: "1700092799999", end: "now()", tooLong: true, from: nowMs - 2*h - 1, to: nowMs,
			text: "profiling: the window is 2h0m0.001s long, over the limit of 2h0m0s"},
		{name: "3h ending now", start: "now()-3h", end: "now()", tooLong: true, from: nowMs - 3*h, to: nowMs,
			text: "profiling: the window is 3h0m0s long, over the limit of 2h0m0s"},
		{name: "a day, far back", start: "now()-3d", end: "now()-2d", tooLong: true, from: nowMs - 72*h, to: nowMs - 48*h,
			text: "profiling: the window is 24h0m0s long, over the limit of 2h0m0s"},
		// Longer than a Duration can say, and than the subtraction can: it
		// used to wrap around and pass as a window within the cap.
		{name: "from the first representable millisecond", start: "-9223372036854775808", end: "now()", tooLong: true, from: math.MinInt64, to: nowMs,
			text: "profiling: the window is 2562047h47m16.854775807s long, over the limit of 2h0m0s"},
		{name: "start alone is never over", start: "now()-3d"},
		{name: "end alone is never over", end: "now()-3d"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w, err := ResolveWindow(tc.start, tc.end, now)
			var tooLong *WindowTooLongError
			if got := errors.As(err, &tooLong); got != tc.tooLong {
				t.Fatalf("too long = %v (err %v), want %v", got, err, tc.tooLong)
			}
			if !tc.tooLong {
				if err != nil {
					t.Fatal(err)
				}
				return
			}
			if w != (Window{}) {
				t.Errorf("a refused window must not be returned, got %+v", w)
			}
			if tooLong.Requested.From != tc.from || tooLong.Requested.To != tc.to || tooLong.Max != MaxWindow {
				t.Errorf("got %+v, want (%d, %d) and the cap %s", tooLong, tc.from, tc.to, MaxWindow)
			}
			if err.Error() != tc.text {
				t.Errorf("err = %q, want %q", err, tc.text)
			}
		})
	}
}

func TestWindow_Span(t *testing.T) {
	cases := []struct {
		w    Window
		want time.Duration
	}{
		{Window{From: 1000, To: 4000}, 3 * time.Second},
		{Window{From: -1000, To: 1000}, 2 * time.Second},
		{Window{}, 0},
		{Window{From: 4000, To: 1000}, -3 * time.Second},
		// Longer than a Duration can say: the longest one, not a wrapped one.
		{Window{From: math.MinInt64, To: math.MaxInt64}, math.MaxInt64},
		{Window{From: 0, To: math.MaxInt64}, math.MaxInt64},
		{Window{From: math.MaxInt64, To: math.MinInt64}, math.MinInt64},
	}
	for _, tc := range cases {
		if got := tc.w.Span(); got != tc.want {
			t.Errorf("%+v.Span() = %d, want %d", tc.w, got, tc.want)
		}
	}
}

// The default length and the cap are two rules that happen to agree today.
func TestWindowLengths(t *testing.T) {
	if DefaultWindow != 2*time.Hour || MaxWindow != 2*time.Hour {
		t.Errorf("DefaultWindow = %s, MaxWindow = %s; want 2h each", DefaultWindow, MaxWindow)
	}
	if DefaultWindow > MaxWindow {
		t.Error("the default window must fit the cap")
	}
}
