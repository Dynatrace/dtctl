package cmd

import (
	"strings"
	"testing"
)

func TestParseProfileTimestamp(t *testing.T) {
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
		got, err := parseProfileTimestamp(tc.input)
		if (err != nil) != tc.wantErr {
			t.Errorf("parseProfileTimestamp(%q) error = %v, wantErr %v", tc.input, err, tc.wantErr)
			continue
		}
		if !tc.wantErr && got != tc.wantMs {
			t.Errorf("parseProfileTimestamp(%q) = %d, want %d", tc.input, got, tc.wantMs)
		}
	}
}

func TestParseProfilingTime_NowRelative(t *testing.T) {
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
		got, err := parseProfilingTime(tc.input, now)
		if (err != nil) != tc.wantErr {
			t.Errorf("parseProfilingTime(%q) error = %v, wantErr %v", tc.input, err, tc.wantErr)
			continue
		}
		if !tc.wantErr && got != tc.want {
			t.Errorf("parseProfilingTime(%q) = %d, want %d", tc.input, got, tc.want)
		}
	}
}

func TestResolveProfilingTimeframe(t *testing.T) {
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
			from, to, err := resolveProfilingTimeframe(tc.start, tc.end, now)
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

func TestRunProfiling_RejectsBadInputBeforeAnyAPICall(t *testing.T) {
	cases := []struct {
		name string
		args []string
		want string
	}{
		{"service entity", []string{"hotspots", "SERVICE-0000000000000000"}, "invalid entity"},
		{"bad leaf type", []string{"hotspots", "PROCESS_GROUP-0000000000000000", "--leaf-type", "nope"}, "invalid --leaf-type"},
		{"window too wide", []string{"allocations", "PROCESS_GROUP-0000000000000000", "--default-timeframe-start", "now()-3h", "--default-timeframe-end", "now()"}, "must not exceed 2h"},
		{"future end", []string{"hotspots", "PROCESS_GROUP-0000000000000000", "--default-timeframe-end", "now()+1h"}, "end is in the future"},
		{"raw with a view", []string{"hotspots", "PROCESS-0000000000000000", "--raw", "--view", "functions"}, "drop --view/--focus"},
		{"bad view", []string{"hotspots", "PROCESS-0000000000000000", "--view", "flame"}, "invalid --view"},
		{"focus with functions view", []string{"hotspots", "PROCESS-0000000000000000", "--view", "functions", "--focus", "f"}, "drop --view functions"},
		{"bad state", []string{"hotspots", "PROCESS-0000000000000000", "--view", "functions", "--state", "sleeping"}, "invalid --state"},
		{"state on stack view", []string{"hotspots", "PROCESS-0000000000000000", "--state", "lock"}, "--state applies to --view functions and --focus"},
		{"sort on stack view", []string{"hotspots", "PROCESS-0000000000000000", "--sort", "total"}, "--sort applies to --view functions"},
		{"bad sort", []string{"hotspots", "PROCESS-0000000000000000", "--view", "functions", "--sort", "name"}, "invalid --sort"},
		{"view on allocations", []string{"allocations", "PROCESS-0000000000000000", "--view", "functions"}, "unknown flag: --view"},
		{"memory alias", []string{"memory", "PROCESS-0000000000000000", "--view", "functions"}, "unknown flag: --view"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := newExecProfilingCmd()
			c.SetArgs(tc.args)
			c.SilenceUsage, c.SilenceErrors = true, true
			for _, sub := range c.Commands() {
				sub.SilenceUsage, sub.SilenceErrors = true, true
			}
			err := c.Execute()
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want it to contain %q", err, tc.want)
			}
		})
	}
}

func TestNoProfilingStacksNote(t *testing.T) {
	empty := map[string]interface{}{"entityId": "PROCESS_GROUP-1", "status": "no-data"}
	if got := noProfilingStacksNote("METHOD_HOTSPOTS", empty, false); !strings.Contains(got, "no profiling samples in the window") {
		t.Errorf("note = %q", got)
	}
}

// Node.js frames carry their file in fileName/filePath (className is empty), so
// compactProfilingResult must keep those when set; it drops them only when empty (Java).
func TestCompactProfilingResult_KeepsFileOnlyWhenSet(t *testing.T) {
	in := map[string]interface{}{"dataNodes": []interface{}{
		map[string]interface{}{"methodName": "Socket", "fileName": "node:net", "filePath": "", "color": "#fff"},
		map[string]interface{}{"methodName": "run", "fileName": nil, "filePath": nil},
	}}
	nodes := compactProfilingResult(in).(map[string]interface{})["dataNodes"].([]interface{})
	js, java := nodes[0].(map[string]interface{}), nodes[1].(map[string]interface{})
	if js["fileName"] != "node:net" {
		t.Errorf("Node.js fileName dropped: %v", js)
	}
	if _, ok := js["filePath"]; ok {
		t.Errorf("empty filePath kept: %v", js)
	}
	if _, ok := js["color"]; ok {
		t.Errorf("visual-only key kept: %v", js)
	}
	if _, ok := java["fileName"]; ok {
		t.Errorf("null fileName kept: %v", java)
	}
}
