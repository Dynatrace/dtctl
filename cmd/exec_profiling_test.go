package cmd

import (
	"strings"
	"testing"
)

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
