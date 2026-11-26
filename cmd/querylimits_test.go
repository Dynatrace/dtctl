package cmd

import (
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/Dynatrace/dtctl/pkg/config"
)

// newQueryLimitsTestCmd builds a command carrying the same limit flags as
// queryCmd so resolveQueryLimits can be exercised in isolation.
func newQueryLimitsTestCmd() *cobra.Command {
	c := &cobra.Command{Use: "test"}
	c.Flags().Int64("max-result-records", 0, "")
	c.Flags().Int64("max-result-bytes", 0, "")
	c.Flags().Float64("default-scan-limit-gbytes", 0, "")
	c.Flags().Float64("default-sampling-ratio", 0, "")
	addQueryLimitFlags(c)
	return c
}

// configWithLimits builds a config whose current context is "prod", with the
// given global block and optional context override.
func configWithLimits(global config.QueryLimits, ctxOverride *config.QueryLimits) *config.Config {
	return &config.Config{
		CurrentContext: "prod",
		Contexts: []config.NamedContext{
			{Name: "prod", Context: config.Context{Environment: "https://abc123.apps.dynatrace.com", QueryLimits: ctxOverride}},
		},
		QueryLimits: global,
	}
}

// mustResolve fails the test if resolution errors; every case below uses a
// valid block, so an error is always a bug in the code under test.
func mustResolve(t *testing.T, cmd *cobra.Command, cfg *config.Config) config.QueryLimits {
	t.Helper()
	lim, err := resolveQueryLimits(cmd, cfg)
	if err != nil {
		t.Fatalf("resolveQueryLimits: unexpected error: %v", err)
	}
	return lim
}

func TestResolveQueryLimits_NoConfigIsServerDefault(t *testing.T) {
	got := mustResolve(t, newQueryLimitsTestCmd(), &config.Config{})
	if !got.IsZero() {
		t.Errorf("empty config = %+v, want all zero (server defaults)", got)
	}

	// A nil config must not panic: embedded invocations have no config at all.
	if got := mustResolve(t, newQueryLimitsTestCmd(), nil); !got.IsZero() {
		t.Errorf("nil config = %+v, want all zero", got)
	}
}

func TestResolveQueryLimits_GlobalConfigApplies(t *testing.T) {
	cfg := configWithLimits(config.QueryLimits{ScanLimitGbytes: 500, MaxResultRecords: 5000}, nil)

	got := mustResolve(t, newQueryLimitsTestCmd(), cfg)
	if got.ScanLimitGbytes != 500 {
		t.Errorf("scan limit = %g, want 500", got.ScanLimitGbytes)
	}
	if got.MaxResultRecords != 5000 {
		t.Errorf("max records = %d, want 5000", got.MaxResultRecords)
	}
	if got.MaxResultBytes != 0 || got.SamplingRatio != 0 {
		t.Errorf("unset fields = %+v, want zero", got)
	}
}

func TestResolveQueryLimits_ContextBeatsGlobalPerField(t *testing.T) {
	// The context tightens the scan ceiling only; the global record cap must
	// survive rather than being reset by the partial override.
	cfg := configWithLimits(
		config.QueryLimits{ScanLimitGbytes: 500, MaxResultRecords: 5000},
		&config.QueryLimits{ScanLimitGbytes: 50},
	)

	got := mustResolve(t, newQueryLimitsTestCmd(), cfg)
	if got.ScanLimitGbytes != 50 {
		t.Errorf("scan limit = %g, want 50 (context override)", got.ScanLimitGbytes)
	}
	if got.MaxResultRecords != 5000 {
		t.Errorf("max records = %d, want 5000 (inherited from global)", got.MaxResultRecords)
	}
}

func TestResolveQueryLimits_FlagBeatsConfig(t *testing.T) {
	cfg := configWithLimits(config.QueryLimits{ScanLimitGbytes: 500}, nil)

	c := newQueryLimitsTestCmd()
	_ = c.Flags().Set("default-scan-limit-gbytes", "10")
	if got := mustResolve(t, c, cfg); got.ScanLimitGbytes != 10 {
		t.Errorf("scan limit = %g, want 10 (flag wins)", got.ScanLimitGbytes)
	}
}

func TestResolveQueryLimits_ExplicitZeroFlagMeansServerDefault(t *testing.T) {
	// The whole reason resolution keys off Changed(): an explicit 0 keeps its
	// documented "use the server default" meaning even with a config ceiling.
	cfg := configWithLimits(config.QueryLimits{ScanLimitGbytes: 500, MaxResultRecords: 5000}, nil)

	c := newQueryLimitsTestCmd()
	_ = c.Flags().Set("default-scan-limit-gbytes", "0")
	got := mustResolve(t, c, cfg)
	if got.ScanLimitGbytes != 0 {
		t.Errorf("scan limit = %g, want 0 (explicit flag beats config)", got.ScanLimitGbytes)
	}
	if got.MaxResultRecords != 5000 {
		t.Errorf("max records = %d, want 5000 (untouched by the other flag)", got.MaxResultRecords)
	}
}

func TestResolveQueryLimits_NoQueryLimitsDropsConfigButKeepsFlags(t *testing.T) {
	cfg := configWithLimits(config.QueryLimits{ScanLimitGbytes: 500, MaxResultRecords: 5000}, nil)

	c := newQueryLimitsTestCmd()
	_ = c.Flags().Set(noQueryLimitsFlag, "true")
	if got := mustResolve(t, c, cfg); !got.IsZero() {
		t.Errorf("--no-query-limits = %+v, want all zero", got)
	}

	// "Ignore what the config says", not "ignore what I just typed".
	c = newQueryLimitsTestCmd()
	_ = c.Flags().Set(noQueryLimitsFlag, "true")
	_ = c.Flags().Set("max-result-records", "42")
	got := mustResolve(t, c, cfg)
	if got.MaxResultRecords != 42 {
		t.Errorf("max records = %d, want 42 (explicit flag survives the opt-out)", got.MaxResultRecords)
	}
	if got.ScanLimitGbytes != 0 {
		t.Errorf("scan limit = %g, want 0 (config dropped)", got.ScanLimitGbytes)
	}
}

// A command that never registered the opt-out flag must still resolve config
// limits rather than tripping over the missing flag.
func TestResolveQueryLimits_CommandWithoutOptOutFlag(t *testing.T) {
	c := &cobra.Command{Use: "test"}
	c.Flags().Int64("max-result-records", 0, "")

	cfg := configWithLimits(config.QueryLimits{MaxResultRecords: 100}, nil)
	if got := mustResolve(t, c, cfg); got.MaxResultRecords != 100 {
		t.Errorf("max records = %d, want 100", got.MaxResultRecords)
	}
}

func TestDescribeQueryLimits(t *testing.T) {
	got := describeQueryLimits(config.QueryLimits{ScanLimitGbytes: 500, MaxResultRecords: 5000})
	want := "--default-scan-limit-gbytes 500, --max-result-records 5000"
	if got != want {
		t.Errorf("describeQueryLimits() = %q, want %q", got, want)
	}
}

// Both DQL-executing commands must expose the opt-out, or the config ceiling
// becomes inescapable on one of them.
func TestQueryLimitFlagsRegistered(t *testing.T) {
	for _, c := range []*cobra.Command{queryCmd, waitQueryCmd} {
		if c.Flags().Lookup(noQueryLimitsFlag) == nil {
			t.Errorf("%s: --%s not registered", c.CommandPath(), noQueryLimitsFlag)
		}
	}
}

// A negative limit never reaches the server (pkg/exec only forwards positive
// values), so accepting one would report a ceiling that is not in force.
func TestResolveQueryLimits_NegativeConfigIsRejected(t *testing.T) {
	for _, tc := range []struct {
		name string
		lim  config.QueryLimits
		want string
	}{
		{"scan", config.QueryLimits{ScanLimitGbytes: -1}, "query-limits.scan-limit-gbytes"},
		{"records", config.QueryLimits{MaxResultRecords: -1}, "query-limits.max-result-records"},
		{"bytes", config.QueryLimits{MaxResultBytes: -1}, "query-limits.max-result-bytes"},
		{"sampling", config.QueryLimits{SamplingRatio: -1}, "query-limits.sampling-ratio"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := resolveQueryLimits(newQueryLimitsTestCmd(), configWithLimits(tc.lim, nil))
			if err == nil {
				t.Fatalf("negative %s: error = nil, want rejection", tc.name)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error = %q, want it to name %q", err, tc.want)
			}
		})
	}

	// A bad value in a context override is caught the same way...
	_, err := resolveQueryLimits(newQueryLimitsTestCmd(),
		configWithLimits(config.QueryLimits{ScanLimitGbytes: 500}, &config.QueryLimits{ScanLimitGbytes: -2}))
	if err == nil {
		t.Error("negative context override: error = nil, want rejection")
	}

	// ...but --no-query-limits drops the config layers entirely, so an
	// unusable block must not brick the escape hatch out of it.
	c := newQueryLimitsTestCmd()
	_ = c.Flags().Set(noQueryLimitsFlag, "true")
	if _, err := resolveQueryLimits(c, configWithLimits(config.QueryLimits{ScanLimitGbytes: -1}, nil)); err != nil {
		t.Errorf("--no-query-limits with a bad config: error = %v, want nil", err)
	}
}
