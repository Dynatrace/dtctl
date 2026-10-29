package cmd

import (
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/Dynatrace/dtctl/sdk/inventory"
)

func windowedInv(signals ...inventory.Signal) *inventory.Inventory {
	return &inventory.Inventory{
		Window:  &inventory.ArrivalWindow{Since: "now()-15m", Filter: `x == "y"`, StaleAfter: "5m0s"},
		Signals: signals,
		Summary: inventory.Summarize(signals),
	}
}

func TestInventorySuggestionsAreResultSpecific(t *testing.T) {
	inv := windowedInv(
		inventory.Signal{Name: "bizevents", State: inventory.SignalEmpty},
		inventory.Signal{Name: "spans", State: inventory.SignalStale, AgeSeconds: 1620},
	)
	joined := strings.Join(inventorySuggestions(inv), "\n")
	if !strings.Contains(joined, "bizevents is live tenant-wide") {
		t.Errorf("empty signal should steer to the source, not the tenant: %q", joined)
	}
	if !strings.Contains(joined, "spans stopped") {
		t.Errorf("stale signal should be called out: %q", joined)
	}

	// Unwindowed runs keep the original catalog-oriented advice.
	plain := inventorySuggestions(&inventory.Inventory{})
	if !strings.Contains(strings.Join(plain, "\n"), "fetch <object>") {
		t.Errorf("unwindowed suggestions changed: %v", plain)
	}
}

func TestSignalCountRendering(t *testing.T) {
	for _, tc := range []struct {
		sig  inventory.Signal
		want string
	}{
		{inventory.Signal{State: inventory.SignalLive, Records: 42}, "42"},
		{inventory.Signal{State: inventory.SignalLive, Datapoints: 400}, "400 pts"},
		{inventory.Signal{State: inventory.SignalAbsent}, "—"},
		{inventory.Signal{State: inventory.SignalUnknown}, "—"},
		{inventory.Signal{State: inventory.SignalEmpty}, "0"},
		// n/a has no count to report: the probe was never asked.
		{inventory.Signal{State: inventory.SignalNotApplicable}, "—"},
		// A sampled volume is an extrapolation from a fraction of the records.
		// The table is what gets compared between runs, so an unmarked
		// estimate invites a few percent of sampling noise to be read as a
		// change in ingest.
		{inventory.Signal{State: inventory.SignalLive, Records: 9688000, SamplingRatio: 1000, RecordsSampled: 9688}, "~9688000"},
	} {
		if got := signalCount(tc.sig); got != tc.want {
			t.Errorf("signalCount(%+v) = %q, want %q", tc.sig, got, tc.want)
		}
	}
}

func TestExitCodesDoNotCollideWithGenericFailure(t *testing.T) {
	// A CI gate has to be able to tell "logs are not arriving" from "dtctl
	// could not authenticate", and cobra returns 1 for the latter.
	for _, code := range []int{inventory.ExitRequiredSignalNotLive, inventory.ExitRequiredSignalUnknown} {
		if code <= 3 {
			t.Errorf("gate exit code %d sits in the band generic command failures use", code)
		}
	}
	if inventory.ExitRequiredSignalNotLive == inventory.ExitRequiredSignalUnknown {
		t.Error("not-live and no-verdict must be distinguishable by exit code")
	}
}

func TestInventorySuggestionsFlagNotApplicable(t *testing.T) {
	inv := windowedInv(
		inventory.Signal{Name: "logs", State: inventory.SignalLive},
		inventory.Signal{Name: "rum", State: inventory.SignalNotApplicable},
	)
	joined := strings.Join(inventorySuggestions(inv), "\n")
	if !strings.Contains(joined, "cannot carry this scope") {
		t.Errorf("agents must be told not to report n/a as missing telemetry: %q", joined)
	}
}

func TestStateRankOrdersNotApplicableAfterData(t *testing.T) {
	// n/a says something about the question, not the data, so it sorts past
	// every state that does say something about the data.
	for _, state := range []inventory.SignalState{
		inventory.SignalLive, inventory.SignalStale,
		inventory.SignalEmpty, inventory.SignalNoData, inventory.SignalAbsent,
	} {
		if stateRank(string(inventory.SignalNotApplicable)) <= stateRank(string(state)) {
			t.Errorf("n/a should rank after %s", state)
		}
	}
	if stateRank(string(inventory.SignalUnknown)) <= stateRank(string(inventory.SignalNotApplicable)) {
		t.Error("unknown should rank last")
	}
}

// TestArrivalsUsageErrorsPrecedeClientSetup pins two things that only a real
// invocation can show.
//
// First, the --scope error has to be the explanatory one. MarkFlagRequired used
// to shadow it: cobra rejected the call with `required flag(s) "scope" not set`
// before RunE ran, so the message explaining *why* an unscoped window is not
// offered — the whole reason the flag is mandatory — was unreachable in the one
// case that triggers it.
//
// Second, every usage check has to run before SetupClient. A mistyped --since
// or signal name is the user's typo; resolving credentials first meant that on
// a machine with no usable context the typo surfaced as an auth failure and
// sent the reader to fix the wrong thing. These cases all reach their error
// without a client, which is what makes them safe to run here at all.
func TestArrivalsUsageErrorsPrecedeClientSetup(t *testing.T) {
	flags := inventoryArrivalsCmd.Flags()
	t.Cleanup(func() { resetFlagSet(flags) })

	for _, tc := range []struct {
		name string
		set  map[string]string
		want string
	}{
		{
			name: "omitted scope explains itself",
			set:  map[string]string{},
			want: "--scope is required: see 'dtctl inventory arrivals --help'",
		},
		{
			name: "bad window is a usage error, not an auth error",
			set:  map[string]string{"scope": `a == "b"`, "since": "5x"},
			want: "invalid window 5x",
		},
		{
			name: "unknown signal name is rejected before any probe",
			set:  map[string]string{"scope": `a == "b"`, "since": "15m", "signals": "logz"},
			want: "unknown signal logz",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			resetFlagSet(flags)
			for k, v := range tc.set {
				if err := flags.Set(k, v); err != nil {
					t.Fatalf("set %s=%q: %v", k, v, err)
				}
			}
			err := inventoryArrivalsCmd.RunE(inventoryArrivalsCmd, nil)
			if err == nil {
				t.Fatal("expected a usage error before the client is built")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error = %q, want it to contain %q", err, tc.want)
			}
		})
	}

	// The guard for the regression itself: with the flag marked required,
	// cobra would preempt RunE and the message above could never be produced.
	if _, marked := flags.Lookup("scope").Annotations[cobra.BashCompOneRequiredFlag]; marked {
		t.Error("--scope must not be MarkFlagRequired: it shadows the error that explains why a scope is needed")
	}
}

// TestSampledSignalsAreCalledOutToAgents: an agent that diffed a sampled
// volume between two runs would manufacture ingest changes out of sampling
// noise, so the envelope has to say which signals are estimates.
func TestSampledSignalsAreCalledOutToAgents(t *testing.T) {
	inv := &inventory.Inventory{
		Window:  &inventory.ArrivalWindow{Since: "now()-15m", ScanLimitGBytes: 25},
		Summary: &inventory.StateSummary{Live: 1},
		Signals: []inventory.Signal{
			{Name: "logs", State: inventory.SignalLive, Records: 9688000, SamplingRatio: 1000, RecordsSampled: 9688},
			{Name: "spans", State: inventory.SignalLive, Records: 12},
		},
	}
	joined := strings.Join(inventorySuggestions(inv), "\n")
	if !strings.Contains(joined, "logs") || !strings.Contains(joined, "extrapolation") {
		t.Errorf("suggestions should name the sampled signal and call its volume an extrapolation:\n%s", joined)
	}
	if strings.Contains(joined, "spans") {
		t.Errorf("spans was counted exhaustively and must not be flagged as sampled:\n%s", joined)
	}
}

// TestNoSampleFlagExists guards the opt-out: the sampled fallback trades
// precision for an answer, and a caller who would rather have "unknown" than
// an approximation needs a way to say so.
func TestNoSampleFlagExists(t *testing.T) {
	if inventoryArrivalsCmd.Flags().Lookup("no-sample") == nil {
		t.Error("--no-sample missing: sampled verdicts must be refusable")
	}
}
