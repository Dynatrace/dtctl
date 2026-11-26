package profiling

import (
	"errors"
	"strings"
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
		wantErr          string
	}{
		{name: "default: the last 2h", from: now - 2*h, to: now},
		{name: "explicit window", start: "1700000000000", end: "1700003600000", from: 1700000000000, to: 1700003600000},
		{name: "start alone: the 2h after it", start: "now()-1d", from: now - 24*h, to: now - 22*h},
		{name: "start alone, near now: ends now", start: "now()-1h", from: now - h, to: now},
		{name: "end alone: the 2h before it", end: "now()-1d", from: now - 26*h, to: now - 24*h},
		{name: "exactly 2h, far back", start: "now()-21h", end: "now()-19h", from: now - 21*h, to: now - 19*h},
		{name: "end before start", start: "1700003600000", end: "1700000000000", wantErr: "end must be after the start"},
		{name: "wider than 2h", start: "now()-3h", end: "now()", wantErr: "must not exceed 2h"},
		{name: "start in the future", start: "now()+5m", end: "now()+10m", wantErr: "start is in the future"},
		{name: "end in the future", start: "now()-1h", end: "now()+1h", wantErr: "end is in the future"},
		{name: "unknown unit", start: "now()-1y", wantErr: "invalid --default-timeframe-start"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w, err := ResolveWindow(tc.start, tc.end, time.UnixMilli(now))
			from, to := w.From, w.To
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("err = %v, want %q", err, tc.wantErr)
				}
				return
			}
			if err != nil || from != tc.from || to != tc.to {
				t.Fatalf("got (%d, %d, %v), want (%d, %d)", from, to, err, tc.from, tc.to)
			}
		})
	}
}

// Only a window resolved from no bounds at all is the default one.
func TestResolveWindow_Default(t *testing.T) {
	now := time.UnixMilli(1_700_100_000_000)
	cases := []struct {
		name, start, end string
		want             bool
	}{
		{"no bounds", "", "", true},
		{"start alone", "now()-1h", "", false},
		{"end alone", "", "now()-1h", false},
		{"both", "now()-2h", "now()", false},
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
			if span := time.Duration(w.To-w.From) * time.Millisecond; span <= 0 || span > MaxWindow {
				t.Errorf("span = %s, want within (0, %s]", span, MaxWindow)
			}
		})
	}
}

// A window over the cap is refused, not shortened, and the error carries the
// window as it was asked for.
func TestResolveWindow_TooLongReportsTheRequest(t *testing.T) {
	const nowMs = int64(1_700_100_000_000)
	const h = int64(3600_000)
	now := time.UnixMilli(nowMs)
	cases := []struct {
		name, start, end string
		tooLong          bool
		from, to         int64
	}{
		{name: "exactly the cap", start: "now()-2h", end: "now()"},
		{name: "one millisecond over", start: "1700092799999", end: "now()", tooLong: true, from: nowMs - 2*h - 1, to: nowMs},
		{name: "3h ending now", start: "now()-3h", end: "now()", tooLong: true, from: nowMs - 3*h, to: nowMs},
		{name: "a day, far back", start: "now()-3d", end: "now()-2d", tooLong: true, from: nowMs - 72*h, to: nowMs - 48*h},
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
			if tooLong.Requested.From != tc.from || tooLong.Requested.To != tc.to {
				t.Errorf("Requested = %+v, want (%d, %d)", tooLong.Requested, tc.from, tc.to)
			}
		})
	}
}

func TestWindowTooLongError_Message(t *testing.T) {
	_, err := ResolveWindow("now()-3h", "now()", time.UnixMilli(1_700_100_000_000))
	const want = "invalid timeframe: the window must not exceed 2h (got 3h0m0s) — narrow it; the window can lie anywhere in the past"
	if err == nil || err.Error() != want {
		t.Errorf("err = %v, want %q", err, want)
	}
}
