package cmd

import (
	"context"
	"fmt"
	"io"
	"sort"
	"time"

	"github.com/spf13/cobra"

	"github.com/dynatrace-oss/dtctl/pkg/client"
	"github.com/dynatrace-oss/dtctl/pkg/config"
	"github.com/dynatrace-oss/dtctl/pkg/exec"
	"github.com/dynatrace-oss/dtctl/pkg/output"
	"github.com/dynatrace-oss/dtctl/pkg/prompt"
	"github.com/dynatrace-oss/dtctl/pkg/stability"
	"github.com/dynatrace-oss/dtctl/pkg/usage"
)

// usageScopeNote is the one sentence every surface of `dtctl usage` repeats:
// what the tally covers, and where the authoritative numbers are.
const usageScopeNote = "Counts only DQL queries run by dtctl on this machine, not queries from the web UI, " +
	"notebooks, dashboards, apps, MCP servers or other machines. For authoritative consumption, " +
	"query the billing events (needs storage:system:read): " + usage.BillingHint

// bindUsageTracking attaches the local usage ledger to c when the current
// context opted in, so every DQL executor built on c records its scan volume.
// It writes nothing otherwise, and never for an embedded invocation: a
// Session's synthetic context has no opt-in, and the ledger is host state
// behind the HostUsageLedger capability.
func bindUsageTracking(ctx context.Context, cfg *config.Config, c *client.Client) {
	if cfg == nil || c == nil || currentSession(ctx) != nil || !currentCaps(ctx).HostUsageLedger {
		return
	}
	if !cfg.UsageTrackingEnabled() {
		return
	}
	var debug io.Writer
	if debugMode(ctx) || verbosity(ctx) > 0 {
		debug = currentStderr(ctx)
	}
	exec.TrackUsage(c, &usage.Recorder{
		Ledger:  usage.New(usage.DefaultPath()),
		Context: cfg.CurrentContext,
		Debug:   debug,
	})
}

// usageRow is one (context, window) line of `dtctl usage`.
type usageRow struct {
	Context           string `json:"context" yaml:"context" table:"CONTEXT"`
	Period            string `json:"period" yaml:"period" table:"PERIOD"`
	From              string `json:"from" yaml:"from" table:"FROM,wide"`
	To                string `json:"to" yaml:"to" table:"TO,wide"`
	Queries           int64  `json:"queries" yaml:"queries" table:"QUERIES"`
	Failed            int64  `json:"failed" yaml:"failed" table:"FAILED"`
	Scanned           string `json:"scanned" yaml:"scanned" table:"SCANNED"`
	ScannedBytes      int64  `json:"scannedBytes" yaml:"scannedBytes" table:"-"`
	ScannedRecords    int64  `json:"scannedRecords" yaml:"scannedRecords" table:"RECORDS"`
	ScannedDataPoints int64  `json:"scannedDataPoints" yaml:"scannedDataPoints" table:"DATA-POINTS,wide"`
	Tracking          bool   `json:"tracking" yaml:"tracking" table:"TRACKING,wide"`
}

// usageResetResult is what `dtctl usage reset` reports in agent mode.
type usageResetResult struct {
	Removed  bool     `json:"removed" yaml:"removed"`
	Contexts []string `json:"contexts,omitempty" yaml:"contexts,omitempty"`
	Message  string   `json:"message" yaml:"message"`
}

func newUsageCmd() *cobra.Command {
	var allContexts bool
	c := &cobra.Command{
		Use:   "usage",
		Short: "Show the DQL queries dtctl ran here and the data they scanned (opt-in)",
		Long: `Show a local tally of the DQL queries dtctl ran and the data they scanned:
today, the last 7 days and the last 30 days (UTC days), for the current
context or, with --all-contexts, for every context.

The tally is opt-in per context and off by default. Turn it on with:

  dtctl config set-context <context> --usage-tracking

With it off, dtctl writes nothing. With it on, every DQL execution dtctl runs
in that context (query, wait, inventory, lookups, ...) adds to a per-day,
per-context counter: query count, failed queries, scanned bytes, scanned
records and scanned data points. Query text, results, user identity and
tokens are never stored. The counters live in usage.json under dtctl's XDG
state directory (typically ~/.local/state/dtctl/), and days older than 90 are
pruned. Clear them with 'dtctl usage reset'.

Scan volume is shown in binary units (GiB/MiB scanned), never as a cost: what
a GiB costs depends on your contract.

` + usageScopeNote + `

This is not a bill. It is a running total so you can see what your own
scripts and agents consume.`,
		Example: `  # Today, last 7 days and last 30 days for the current context
  dtctl usage

  # Every context that has recorded usage
  dtctl usage --all-contexts

  # Machine-readable
  dtctl usage -o json

  # Turn tracking on for a context
  dtctl config set-context prod --usage-tracking

  # Clear the tally for the current context
  dtctl usage reset`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runUsage(cmdContext(cmd), allContexts, time.Now())
		},
	}
	c.Flags().BoolVar(&allContexts, "all-contexts", false, "show every context with recorded usage, not just the current one")
	stability.Mark(c, stability.Experimental, "0.43.0")
	return c
}

var (
	usageCmd      = newUsageCmd()
	usageResetCmd = newUsageResetCmd()
)

func init() {
	rootCmd.AddCommand(usageCmd)
	usageCmd.AddCommand(usageResetCmd)
}

func newUsageResetCmd() *cobra.Command {
	var allContexts, yes bool
	c := &cobra.Command{
		Use:   "reset",
		Short: "Clear the local usage tally",
		Long: `Clear the local usage tally of the current context, or of every context with
--all-contexts (which deletes the usage file). Tracking stays on or off as
configured; to stop recording, run:

  dtctl config set-context <context> --usage-tracking=false`,
		Example: `  # Clear the current context's tally
  dtctl usage reset

  # Delete the whole usage file, without prompting
  dtctl usage reset --all-contexts --yes`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runUsageReset(cmdContext(cmd), allContexts, yes)
		},
	}
	c.Flags().BoolVar(&allContexts, "all-contexts", false, "clear every context's tally (deletes the usage file)")
	c.Flags().BoolVarP(&yes, "yes", "y", false, "skip the confirmation prompt")
	stability.Mark(c, stability.Experimental, "0.43.0")
	return c
}

func runUsage(ctx context.Context, allContexts bool, now time.Time) error {
	cfg, err := loadConfig(ctx)
	if err != nil {
		return err
	}
	if !allContexts && cfg.CurrentContext == "" {
		return fmt.Errorf("no current context; select one with 'dtctl ctx <name>' or pass --all-contexts")
	}

	var warnings, suggestions []string
	ledger := usage.New(usage.DefaultPath())
	days, readErr := ledger.Read()
	if readErr != nil {
		warnings = append(warnings, fmt.Sprintf("could not read the usage file %s: %v", ledger.Path(), readErr))
		days = usage.Days{}
	}

	tracked := map[string]bool{}
	for _, nc := range cfg.Contexts {
		tracked[nc.Name] = cfg.UsageTrackingOn(nc.Name)
	}

	names := []string{cfg.CurrentContext}
	if allContexts {
		names = usageContextNames(cfg, days)
	}

	rows := make([]usageRow, 0, len(names)*len(usage.Windows))
	for _, name := range names {
		for _, t := range usage.Summarize(days, name, now, usage.Windows) {
			rows = append(rows, usageRow{
				Context:           name,
				Period:            t.Window.Name,
				From:              t.From,
				To:                t.To,
				Queries:           t.Queries,
				Failed:            t.Failed,
				Scanned:           usage.FormatBytes(t.ScannedBytes),
				ScannedBytes:      t.ScannedBytes,
				ScannedRecords:    t.ScannedRecords,
				ScannedDataPoints: t.ScannedDataPoints,
				Tracking:          tracked[name],
			})
		}
	}

	if !allContexts && cfg.IgnoredUsageTracking(cfg.CurrentContext) {
		warnings = append(warnings, fmt.Sprintf("usage-tracking in the local config %s is ignored: only the global config can turn it on", cfg.LocalConfigPath()))
		suggestions = append(suggestions, fmt.Sprintf("turn it on in the global config: dtctl config set-context %s --usage-tracking --global", cfg.CurrentContext))
	} else if !allContexts && !tracked[cfg.CurrentContext] {
		warnings = append(warnings, fmt.Sprintf("usage tracking is off for context %q, so dtctl records nothing for it", cfg.CurrentContext))
		suggestions = append(suggestions, fmt.Sprintf("turn it on: dtctl config set-context %s --usage-tracking", cfg.CurrentContext))
	}
	if allContexts && len(names) == 0 {
		warnings = append(warnings, "no context has usage tracking on and no usage is recorded")
		suggestions = append(suggestions, "turn it on for a context: dtctl config set-context <context> --usage-tracking")
	}

	printer := newPrinterCtx(ctx)
	if ap := enrichAgent(printer, "usage", "usage"); ap != nil {
		ap.SetWarnings(warnings)
		ap.SetSuggestions(append(suggestions, usageScopeNote))
		return printer.PrintList(rows)
	}

	stderr := currentStderr(ctx)
	for _, w := range warnings {
		output.FprintWarning(stderr, "%s", w)
	}
	for _, s := range suggestions {
		output.FprintHint(stderr, "%s", s)
	}
	if err := printer.PrintList(rows); err != nil {
		return err
	}
	if output.IsTabularFormat(outputFormat(ctx), plainMode(ctx)) {
		fmt.Fprintln(stderr)
		output.FprintInfo(stderr, "%s", usageScopeNote)
		output.FprintInfo(stderr, "Usage file: %s", ledger.Path())
	}
	return nil
}

// usageContextNames is every context worth a line under --all-contexts: those
// with tracking on, plus any with recorded usage (a context whose tracking was
// switched off, or that was deleted, keeps its history until reset).
func usageContextNames(cfg *config.Config, days usage.Days) []string {
	seen := map[string]bool{}
	for _, nc := range cfg.Contexts {
		if cfg.UsageTrackingOn(nc.Name) {
			seen[nc.Name] = true
		}
	}
	for _, name := range usage.Contexts(days) {
		seen[name] = true
	}
	names := make([]string, 0, len(seen))
	for name := range seen {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func runUsageReset(ctx context.Context, allContexts, yes bool) error {
	target := ""
	if !allContexts {
		cfg, err := loadConfig(ctx)
		if err != nil {
			return err
		}
		if cfg.CurrentContext == "" {
			return fmt.Errorf("no current context; select one with 'dtctl ctx <name>' or pass --all-contexts")
		}
		target = cfg.CurrentContext
	}

	what := fmt.Sprintf("the local usage tally of context %q", target)
	if allContexts {
		what = "the local usage tally of every context"
	}
	if !yes && !plainMode(ctx) {
		if !prompt.ConfirmWith(currentStdin(ctx), currentStdout(ctx), "Clear "+what+"?") {
			output.FprintInfo(currentStderr(ctx), "Reset cancelled")
			return nil
		}
	}

	ledger := usage.New(usage.DefaultPath())
	removed, err := ledger.Reset(target)
	if err != nil {
		return fmt.Errorf("failed to clear %s: %w", what, err)
	}

	result := usageResetResult{Removed: removed}
	if target != "" {
		result.Contexts = []string{target}
	}
	switch {
	case removed:
		result.Message = "Cleared " + what
	case allContexts:
		result.Message = "Nothing to clear: no usage is recorded"
	default:
		result.Message = fmt.Sprintf("Nothing to clear: no usage is recorded for context %q", target)
	}

	printer := newPrinterCtx(ctx)
	if ap := enrichAgent(printer, "reset", "usage"); ap != nil {
		return printer.Print(result)
	}
	if removed {
		output.FprintSuccess(currentStderr(ctx), "%s", result.Message)
	} else {
		output.FprintInfo(currentStderr(ctx), "%s", result.Message)
	}
	return nil
}
