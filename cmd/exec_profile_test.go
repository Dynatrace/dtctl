package cmd

import (
	"strings"
	"testing"
	"time"
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
	cases := []struct {
		input   string
		want    int64
		wantErr bool
	}{
		{"now()", now, false},
		{"now()-1h", now - 3600_000, false},
		{"now()-30min", now - 1800_000, false},
		{"now()+15m", now + 900_000, false},
		{"1700000000000", now, false},
		{"now()-", 0, true},
		{"now()*5m", 0, true},
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
	// Defaults: empty start/end -> a ~2h window ending about now.
	from, to, err := resolveProfilingTimeframe("", "")
	if err != nil {
		t.Fatalf("default timeframe errored: %v", err)
	}
	if d := to - from; d != 2*60*60*1000 {
		t.Errorf("default window = %dms, want 2h", d)
	}
	if delta := time.Now().UnixMilli() - to; delta < 0 || delta > 5000 {
		t.Errorf("default end should be ~now, off by %dms", delta)
	}

	// Explicit epoch window is honored.
	from, to, err = resolveProfilingTimeframe("1700000000000", "1700003600000")
	if err != nil || from != 1700000000000 || to != 1700003600000 {
		t.Errorf("explicit window = (%d,%d,%v), want (1700000000000,1700003600000,nil)", from, to, err)
	}

	// end <= start is rejected.
	if _, _, err := resolveProfilingTimeframe("1700003600000", "1700000000000"); err == nil {
		t.Error("expected error when end precedes start")
	}

	// Exactly 2h is allowed, however far back it lies; anything wider is not.
	if _, _, err := resolveProfilingTimeframe("now()-21h", "now()-19h"); err != nil {
		t.Errorf("an earlier 2h window errored: %v", err)
	}
	if _, _, err := resolveProfilingTimeframe("now()-3h", ""); err == nil {
		t.Error("expected error for window > 2h")
	}

	// A start in the future is rejected client-side.
	if _, _, err := resolveProfilingTimeframe("now()+5m", "now()+10m"); err == nil {
		t.Error("expected error for a start in the future")
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
		{"window too wide", []string{"memory", "PROCESS_GROUP-0000000000000000", "--default-timeframe-start", "now()-3h"}, "must not exceed 2h"},
		{"bad view", []string{"hotspots", "PROCESS-0000000000000000", "--view", "flame"}, "invalid --view"},
		{"focus with functions view", []string{"hotspots", "PROCESS-0000000000000000", "--view", "functions", "--focus", "f"}, "drop --view functions"},
		{"bad state", []string{"hotspots", "PROCESS-0000000000000000", "--view", "functions", "--state", "sleeping"}, "invalid --state"},
		{"state on stack view", []string{"hotspots", "PROCESS-0000000000000000", "--state", "lock"}, "--state applies to --view functions and --focus"},
		{"sort on stack view", []string{"hotspots", "PROCESS-0000000000000000", "--sort", "total"}, "--sort applies to --view functions"},
		{"bad sort", []string{"hotspots", "PROCESS-0000000000000000", "--view", "functions", "--sort", "name"}, "invalid --sort"},
		{"view on memory", []string{"memory", "PROCESS-0000000000000000", "--view", "functions"}, "unknown flag: --view"},
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
// compactResult must keep those when set; it drops them only when empty (Java).
func TestCompactResult_KeepsFileOnlyWhenSet(t *testing.T) {
	in := map[string]interface{}{"dataNodes": []interface{}{
		map[string]interface{}{"methodName": "Socket", "fileName": "node:net", "filePath": "", "color": "#fff"},
		map[string]interface{}{"methodName": "run", "fileName": nil, "filePath": nil},
	}}
	nodes := compactResult(in).(map[string]interface{})["dataNodes"].([]interface{})
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
