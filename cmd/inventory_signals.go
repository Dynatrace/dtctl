package cmd

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/dynatrace-oss/dtctl/pkg/output"
	"github.com/dynatrace-oss/dtctl/sdk/inventory"
)

var inventoryArrivalsCmd = newInventoryArrivalsCmd()

func newInventoryArrivalsCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "arrivals",
		Short: "Is data arriving for this source right now? Per-signal ingest state over a window",
		Long: `Report, per signal type, whether data matching one scope is arriving inside a
window — the question you actually have after instrumenting a service, pointing a
collector at the tenant, or running an ingest.

'dtctl inventory' cannot answer it: its verdicts are retention-scoped, so a stream
that received data once last week reads as present. This command windows the
question and narrows it to one scope.

States:
  live    records matched and the newest is fresh
  stale   records matched, but the source stopped inside the window
  empty   nothing matched, yet the stream holds data within retention —
          the stream works, this scope is not producing into it
  no-data nothing matched and the stream is empty tenant-wide
  n/a     the scope selects on nothing this signal has (there is no
          k8s.namespace.name on RUM data, and no service.name dimension on
          Kubernetes metrics). For a metric family this is structural — the
          dimension is not in the metric definition. For a stream it means no
          record in the window carried the field, which is strong evidence but
          not proof; widen --since to test it harder.
  absent  the stream is not in this environment's data-object catalog
  unknown no verdict — the probe was truncated, capped, or failed. Never to
          be read as absence.

On a high-volume tenant the scan cap is the usual source of 'unknown': a probe
that would scan more than --scan-limit-gbytes (default 25) is stopped before it
can count, and logs and spans are the first to hit it. That is a dtctl limit,
not a finding about your data — the run says so explicitly and names the cap.

Rather than give up there, a capped stream is re-probed with DQL sampling, which
reduces the scan itself and not merely the result. Sampling only ever drops
records, so a sampled match still proves data is arriving; what it costs is
precision, so the volume becomes an estimate and last-seen a lower bound, and
both are labelled as such. A sampled probe that matches nothing stays 'unknown'
and reports the population it can rule out — it is never read as absence. Pass
--no-sample to skip the fallback and take the plain 'unknown' instead.

--scope is required. An unscoped windowed count is the single most expensive query
available, and the unscoped question is already answered for free, without a window,
by plain 'dtctl inventory'. ('--scope true' is legal DQL and will be honoured — it is
simply the expensive query this default is steering you away from.)

Examples:
  # Is anything arriving for this namespace?
  dtctl inventory arrivals --scope 'k8s.namespace.name == "payments"'

  # Widen the window
  dtctl inventory arrivals --since 1h --scope 'service.name == "checkout"'

  # Gate a pipeline on it (exit 10 if a required signal is not live, 11 if unknown)
  dtctl inventory arrivals --scope 'service.name == "checkout"' --require logs,spans

  # Confirm an ingest landed, probing only what can answer
  dtctl inventory arrivals --since 5m --scope 'log.source == "batch-import"' \
      --signals logs --require logs
`,
		RunE: func(cmd *cobra.Command, args []string) error {
			// Usage is validated before the client is set up: a mistyped --since
			// or signal name is the user's typo, and reporting it as an auth
			// failure on a machine that happens to have no token sends them to
			// fix the wrong thing.
			scope, _ := cmd.Flags().GetString("scope")
			since, _ := cmd.Flags().GetString("since")
			signals, _ := cmd.Flags().GetStringSlice("signals")
			require, _ := cmd.Flags().GetStringSlice("require")
			staleAfterFlag, _ := cmd.Flags().GetDuration("stale-after")

			// Not MarkFlagRequired: cobra's "required flag(s) \"scope\" not set"
			// would preempt this message, and the whole point of requiring a scope
			// is the reason behind it — an unscoped windowed count is the most
			// expensive query in the battery, and plain 'dtctl inventory' already
			// answers the unscoped question for free.
			if strings.TrimSpace(scope) == "" {
				return fmt.Errorf("--scope is required: see 'dtctl inventory arrivals --help' for why an unscoped window is not offered")
			}
			sinceExpr, windowLen, err := inventory.NormalizeSince(since)
			if err != nil {
				return err
			}

			defs, err := inventoryDefinitions(cmd)
			if err != nil {
				return err
			}
			// Names are checked before a single query runs: a typo is a usage
			// error, and discovering it after the battery has spent the budget
			// would both waste the run and dress the mistake up as a telemetry
			// verdict.
			if err := inventory.ValidateSignalNames(defs, signals, require); err != nil {
				return err
			}

			staleAfter := staleAfterFlag
			if staleAfter == 0 {
				staleAfter = inventory.DefaultStaleAfter(windowLen)
			}
			if windowLen > time.Hour {
				fmt.Fprintf(currentStderr(cmdContext(cmd)), "warning: a %s window makes each probe scan proportionally more; probes cut short by the scan cap report as unknown, not absent — narrow --since if that happens\n", roundWindow(windowLen))
			}

			cfg, c, err := setupClient(cmdContext(cmd))
			if err != nil {
				return err
			}

			scanLimitGB, _ := cmd.Flags().GetFloat64("scan-limit-gbytes")
			noSample, _ := cmd.Flags().GetBool("no-sample")
			runner := newInventoryRunner(cmd, cfg, c)
			ctx, cancel := inventoryCancelContext(cmd)
			defer cancel()

			budgetQueries, budgetSeconds := inventoryBudget(cmd)
			inv, err := inventory.Discover(ctx, runner, defs, inventory.DiscoverOptions{
				ContextName:     cfg.CurrentContext,
				BudgetQueries:   budgetQueries,
				BudgetSeconds:   budgetSeconds,
				Since:           sinceExpr,
				Scope:           scope,
				StaleAfter:      staleAfter,
				Signals:         signals,
				ScanLimitGBytes: scanLimitGB,
				DisableSampling: noSample,
			})
			if err != nil {
				return err
			}

			if outputFormat(cmdContext(cmd)) == "table" && !agentMode(cmdContext(cmd)) {
				printInventorySignalsHuman(cmdContext(cmd), inv)
				return exitForRequiredSignals(cmdContext(cmd), inv, require)
			}
			printer := newPrinterCtx(cmdContext(cmd))
			if ap := enrichAgent(printer, "inventory", "arrivals"); ap != nil {
				ap.SetSuggestions(inventorySuggestions(inv))
			}
			if err := printer.Print(inv); err != nil {
				return err
			}
			return exitForRequiredSignals(cmdContext(cmd), inv, require)
		},
	}
	c.Flags().String("scope", "", "DQL filter fragment scoping every probe, e.g. 'k8s.namespace.name == \"payments\"' (required)")
	c.Flags().String("since", inventory.DefaultWindow, "Window to report arrivals over (e.g. 15m, 1h)")
	c.Flags().StringSlice("signals", nil, "Restrict probing to these signals (default: all signal streams and metric families)")
	c.Flags().StringSlice("require", nil, "Exit non-zero unless every named signal is live (10 = not live, 11 = no verdict)")
	c.Flags().Duration("stale-after", 0, "Age past which a matched signal is stale rather than live (default max(2m, window/3))")
	c.Flags().Bool("no-sample", false, "Do not fall back to sampled probing when a signal exceeds the scan cap; report it as unknown instead")
	addInventoryDiscoveryFlags(c)
	// Not marked required (see the RunE check for why); an explicitly empty
	// value is still a usage error.
	rejectEmptyFlag(c, "scope")
	return c
}

func roundWindow(d time.Duration) string {
	if d%time.Minute == 0 {
		return d.String()
	}
	return d.Round(time.Second).String()
}

// exitForRequiredSignals enforces --require after the report has been printed,
// so the operator sees the evidence for the failure rather than just a code.
//
// It returns a *silentExitError rather than calling os.Exit: the gate's code is
// part of the command's contract, not an error to re-print, and a command body
// that terminates the process cannot be embedded (see the E2 guard in
// silent_exit_test.go).
func exitForRequiredSignals(ctx context.Context, inv *inventory.Inventory, require []string) error {
	verdict := inventory.CheckRequired(inv, require)
	for _, m := range verdict.Messages {
		fmt.Fprintf(currentStderr(ctx), "\n%s\n", m)
	}
	if code := verdict.ExitCode(); code != 0 {
		return &silentExitError{code: code, reason: "required signals not live"}
	}
	return nil
}

// scanCappedSignals names the signals whose probe was stopped by the scan cap.
func scanCappedSignals(inv *inventory.Inventory) []string {
	var out []string
	for _, sig := range inv.Signals {
		if sig.Truncation == inventory.TruncationScanLimit {
			out = append(out, sig.Name)
		}
	}
	return out
}

// signalNames lists signals by name, for a prose callout.
func signalNames(sigs []inventory.Signal) []string {
	out := make([]string, 0, len(sigs))
	for _, s := range sigs {
		out = append(out, s.Name)
	}
	return out
}

// scanLimitOf reports the cap the run used, for the warning above.
func scanLimitOf(inv *inventory.Inventory) float64 {
	if inv.Window != nil {
		return inv.Window.ScanLimitGBytes
	}
	return 0
}

// inventorySuggestions tailors agent-mode guidance to what the run actually
// found, so the advice is about this result rather than generic.
func inventorySuggestions(inv *inventory.Inventory) []string {
	if inv == nil || inv.Window == nil {
		return []string{
			"Run 'dtctl query \"fetch <object> | limit 10\"' to sample any listed data object",
			"Cite the evidence carried by absent capabilities instead of re-probing; unknown capabilities got no verdict and may still exist",
		}
	}
	var out []string
	for _, sig := range inv.Signals {
		switch sig.State {
		case inventory.SignalEmpty:
			out = append(out, fmt.Sprintf("%s is live tenant-wide but matched nothing for this scope — investigate the source's emission, not the tenant", sig.Name))
		case inventory.SignalStale:
			out = append(out, fmt.Sprintf("%s stopped %ds ago inside the window — the source emitted and then went quiet; this is not a query problem", sig.Name, sig.AgeSeconds))
		}
	}
	if inv.Summary != nil && inv.Summary.NotApplicable > 0 {
		out = append(out, "Signals marked n/a cannot carry this scope at all — do not report them as missing telemetry, and do not re-probe them")
	}
	if capped := scanCappedSignals(inv); len(capped) > 0 {
		out = append(out, fmt.Sprintf("%s hit the %g GB scan cap, so dtctl could not count them — this says nothing about whether that data is arriving; raise --scan-limit-gbytes or narrow --since, and never report these as missing",
			strings.Join(capped, ", "), scanLimitOf(inv)))
	}
	if sampled := sampledSignals(inv); len(sampled) > 0 {
		out = append(out, fmt.Sprintf("%s exceeded the scan cap and were probed on a sample: their arrival is proven, but records is an extrapolation (see samplingRatio/recordsSampled) and lastSeen is a lower bound — do not diff these volumes between runs as if they were counts",
			strings.Join(signalNames(sampled), ", ")))
	}
	if inv.Summary != nil && inv.Summary.Unknown > 0 {
		out = append(out, "Unknown signals got no verdict: re-run with a narrower --since or a raised --scan-limit-gbytes rather than treating them as absent")
	}
	if len(out) == 0 {
		out = append(out, "Every required signal is arriving; re-run with --require to turn this into a gate")
	}
	return out
}

// printInventorySignalsHuman renders windowed arrival mode for a terminal: the
// per-signal state table first, then the evidence for everything that is not
// simply live.
func printInventorySignalsHuman(ctx context.Context, inv *inventory.Inventory) {
	const w = 12
	output.FprintDescribeKV(currentStdout(ctx), "Context:", w, "%s", inv.Context)
	output.FprintDescribeKV(currentStdout(ctx), "Generated:", w, "%s", inv.GeneratedAt)
	if inv.Window != nil {
		output.FprintDescribeKV(currentStdout(ctx), "Window:", w, "%s → now()  (stale after %s)", inv.Window.Since, inv.Window.StaleAfter)
		if inv.Window.Filter != "" {
			output.FprintDescribeKV(currentStdout(ctx), "Scope:", w, "%s", inv.Window.Filter)
		}
	}

	if len(inv.Signals) == 0 {
		fmt.Fprintln(currentStdout(ctx), "\nNo signal types were probed — every capability was excluded by --signals.")
		return
	}

	rows := make([][5]string, 0, len(inv.Signals))
	for _, sig := range inv.Signals {
		rows = append(rows, [5]string{
			sig.Name,
			string(sig.State),
			signalCount(sig),
			signalLastSeen(sig),
			signalAge(sig),
		})
	}
	// Most-alive first: the states a reader acts on sit together at the bottom.
	sort.SliceStable(rows, func(i, j int) bool {
		return stateRank(rows[i][1]) < stateRank(rows[j][1])
	})

	// "Volume", not "Records": metric families are counted in datapoints.
	headers := [5]string{"SIGNAL", "STATE", "VOLUME", "LAST SEEN", "AGE"}
	width := [5]int{}
	for i, h := range headers {
		width[i] = len(h)
	}
	for _, r := range rows {
		for i, cell := range r {
			if len(cell) > width[i] {
				width[i] = len(cell)
			}
		}
	}
	fmt.Fprintln(currentStdout(ctx))
	fmt.Fprintf(currentStdout(ctx), "%-*s  %-*s  %*s  %-*s  %*s\n",
		width[0], headers[0], width[1], headers[1], width[2], headers[2], width[3], headers[3], width[4], headers[4])
	for _, r := range rows {
		fmt.Fprintf(currentStdout(ctx), "%-*s  %-*s  %*s  %-*s  %*s\n",
			width[0], r[0], width[1], r[1], width[2], r[2], width[3], r[3], width[4], r[4])
	}

	if s := inv.Summary; s != nil {
		parts := []string{}
		for _, p := range []struct {
			n int
			l string
		}{{s.Live, "live"}, {s.Stale, "stale"}, {s.Empty, "empty"}, {s.NoData, "no-data"}, {s.NotApplicable, "n/a"}, {s.Absent, "absent"}, {s.Unknown, "unknown"}} {
			if p.n > 0 {
				parts = append(parts, fmt.Sprintf("%d %s", p.n, p.l))
			}
		}
		if len(parts) > 0 {
			fmt.Fprintf(currentStdout(ctx), "\n%s\n", strings.Join(parts, " · "))
		}
	}

	// A scan-capped signal is the one "unknown" a reader is most likely to
	// misread as a finding, and on a high-volume tenant it can hit exactly
	// the signals they came to check. Say so above the evidence block rather
	// than leaving it to be inferred from a per-signal line.
	if capped := scanCappedSignals(inv); len(capped) > 0 {
		// "each scans" reads wrong for a single signal, and one is the common
		// case under --signals.
		verb := "each scans"
		if len(capped) == 1 {
			verb = "it scans"
		}
		fmt.Fprintf(currentStdout(ctx), "\nScan cap: %s could not be counted — %s more than the %g GB cap over this window.\n",
			strings.Join(capped, ", "), verb, scanLimitOf(inv))
		fmt.Fprintln(currentStdout(ctx), "  This is a dtctl limit, not a verdict about your data. Raise --scan-limit-gbytes or narrow --since.")
	}

	// Sampled signals resolved *because* of the cap, so this sits with the
	// cap note rather than in the evidence block: a reader who takes the
	// volume column at face value needs to know before they read it.
	if sampled := sampledSignals(inv); len(sampled) > 0 {
		fmt.Fprintf(currentStdout(ctx), "\nSampling: %s exceeded the %g GB cap, so %s probed on a sample.\n",
			strings.Join(signalNames(sampled), ", "), scanLimitOf(inv),
			map[bool]string{true: "it was", false: "they were"}[len(sampled) == 1])
		for _, sig := range sampled {
			fmt.Fprintf(currentStdout(ctx), "  %s — 1-in-%d, %d sampled records; volume is an estimate and last seen is a lower bound.\n",
				sig.Name, sig.SamplingRatio, sig.RecordsSampled)
		}
	}

	var evidence []inventory.Signal
	for _, sig := range inv.Signals {
		if sig.Evidence != "" {
			evidence = append(evidence, sig)
		}
	}
	if len(evidence) > 0 {
		fmt.Fprintln(currentStdout(ctx))
		output.FprintDescribeSection(currentStdout(ctx), "Evidence (what was checked)")
		for _, sig := range evidence {
			fmt.Fprintf(currentStdout(ctx), "  %s — %s\n", sig.Name, sig.Evidence)
		}
	}
	for _, n := range inv.Notes {
		output.FprintDescribeKV(currentStdout(ctx), "Note:", w, "%s", n)
	}
	if r := inv.Discovery; r != nil {
		fmt.Fprintf(currentStderr(ctx), "\nDiscovery: %d queries, %.1fs query time\n", r.Queries, r.Seconds)
		for _, n := range r.Notes {
			fmt.Fprintf(currentStderr(ctx), "  note: %s\n", n)
		}
	}
}

// stateRank orders states from most to least alive. n/a sits past the states
// that say something about the data, because it says something about the
// question instead.
func stateRank(state string) int {
	switch inventory.SignalState(state) {
	case inventory.SignalLive:
		return 0
	case inventory.SignalStale:
		return 1
	case inventory.SignalEmpty:
		return 2
	case inventory.SignalNoData:
		return 3
	case inventory.SignalAbsent:
		return 4
	case inventory.SignalNotApplicable:
		return 5
	default:
		return 6
	}
}

func signalCount(sig inventory.Signal) string {
	switch {
	case sig.Datapoints > 0:
		return strconv.FormatInt(sig.Datapoints, 10) + " pts"
	case sig.State == inventory.SignalAbsent ||
		sig.State == inventory.SignalUnknown ||
		sig.State == inventory.SignalNotApplicable:
		return "—"
	case sig.SamplingRatio > 0:
		// A sampled volume is an extrapolation from a fraction of the records,
		// so it is marked here as well as in the evidence: the table is what
		// most readers will compare between runs, and an unmarked estimate
		// invites a swing of a few percent to be read as a change in ingest.
		return "~" + strconv.FormatInt(sig.Records, 10)
	default:
		return strconv.FormatInt(sig.Records, 10)
	}
}

// sampledSignals names the signals whose verdict rests on a sampled probe.
func sampledSignals(inv *inventory.Inventory) []inventory.Signal {
	var out []inventory.Signal
	for _, sig := range inv.Signals {
		if sig.SamplingRatio > 0 {
			out = append(out, sig)
		}
	}
	return out
}

func signalLastSeen(sig inventory.Signal) string {
	if sig.LastSeen == "" {
		return "—"
	}
	if ts, err := time.Parse(time.RFC3339, sig.LastSeen); err == nil {
		return ts.UTC().Format("15:04:05")
	}
	return sig.LastSeen
}

func signalAge(sig inventory.Signal) string {
	if sig.LastSeen == "" {
		return "—"
	}
	return (time.Duration(sig.AgeSeconds) * time.Second).String()
}
