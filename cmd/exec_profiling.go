package cmd

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/Dynatrace/dtctl/pkg/diagnostic"
	"github.com/Dynatrace/dtctl/pkg/output"
	"github.com/Dynatrace/dtctl/pkg/resources/profiling"
	"github.com/Dynatrace/dtctl/pkg/safety"
	"github.com/Dynatrace/dtctl/pkg/stability"
)

// profilingSince is the release in which `exec profiling` entered the
// experimental tier. One constant for the whole family.
const profilingSince = "0.47.0"

// profilingFindIDsHint tells the caller how to find an eligible entity id.
const profilingFindIDsHint = profiling.FindIDsQuery + `
  # id is the PROCESS-xxx id to pass; dt.process_group.id is its PROCESS_GROUP-xxx id`

// profilingExperimentalNotice opens every profiling help page, so whoever
// reads the help of any profiling command sees it before anything else.
const profilingExperimentalNotice = `EXPERIMENTAL: both the underlying API and these commands may change in breaking
ways between releases.`

// execProfilingCmd is the namespace for code-level profiling analyses.
var execProfilingCmd = newExecProfilingCmd()

// newExecProfilingCmd builds `exec profiling` with its kind subcommands. The
// kind is a subcommand (hotspots/allocations) so each declares only the flags
// it accepts and cobra rejects nonsensical combinations; the entity is a
// positional argument.
func newExecProfilingCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "profiling",
		Short: "Run a code-level profiling analysis on a process",
		Long: `Run a code-level profiling analysis via the Dynatrace code-level analysis API.

` + profilingExperimentalNotice + `

The analysis is asynchronous server-side; the command polls until it completes.
The default view is one row per unique call stack. In a table it prints as
collapsed (folded) stacks carrying all five thread-state counts, so a single
read shows whether a stack is CPU-bound or blocked (this is not single-metric
folded, so it does not feed flamegraph tools directly):
  frame1;frame2;...;frameN running=16 lock=0 net_io=0 disk_io=0 wait=0
-o json/yaml/csv print the same rows as fields (the five counts and the stack),
capped by --limit like the table. Each frame's own (self) samples are attributed
to its stack, so the counts reconcile with the analysis totals. --raw prints the
API's whole result instead (large: megabytes for a busy process).

Which function uses the most CPU? A function reached through many call paths is
split over many stack rows, so don't sum rows yourself; hotspots does it:
  hotspots <entity> --view functions   one row per function: self and total
                                       samples summed over every stack
  hotspots <entity> --focus <function> that function's callers and callees
(--state lock|offcpu|... counts other thread states; see hotspots --help.)

Kinds:
  hotspots     Method CPU hotspots (per-stack thread-state counts include lock/IO/wait)
  allocations  Where memory is allocated, and what survived GC, in the window
               (alias: memory). Allocation pressure, not live heap: survived
               bytes are bytes x GCs survived, not a size (see allocations --help).

Entities: pass a process id, PROCESS-xxx. SERVICE ids are not accepted. The API
speaks the classic entity vocabulary, so dtctl rewrites PROCESS-xxx to its
PROCESS_GROUP_INSTANCE-xxx form (same entity, same id suffix; the agent envelope
names it in context.profile.api_entity). Classic ids work as well:
PROCESS_GROUP_INSTANCE-xxx, and PROCESS_GROUP-xxx for a whole process group.

Find process ids:
  ` + profilingFindIDsHint + `

Timeframe: --default-timeframe-start/--default-timeframe-end take what dtctl
query takes: ISO-8601/RFC3339, epoch millis, or now()-relative (now()-1d,
now()-90m, now()-1h30m). Without either, the last 2h. A start alone covers the 2h
after it, an end alone the 2h before it. The window must not exceed 2h, must not
end in the future, and can lie anywhere in the past.

Required token scopes: storage:entities:read, storage:profiles:read.`,
		RunE: requireSubcommand,
	}
	stability.Mark(c, stability.Experimental, profilingSince)
	// The scope catalog lists `exec profiling`, not each kind: its leaves take
	// the group's scopes, so --check-scopes answers for them.
	markScopeGroup(c)
	c.AddCommand(newProfilingHotspotsCmd(), newProfilingAllocationsCmd())
	return c
}

const profilingLeafTypeHelp = `
--leaf-type picks which CPU samples to attribute:
  total       (default) everything, including agent/framework overhead
  service     samples taken inside a traced service/request context — reach for
              this to localize why a specific endpoint or call path is hot
  background  ambient work outside any request (GC, JIT, schedulers, pools)`

// profilingFlags are one command's flag values. Flags a kind does not register
// stay at their zero value, so one run body serves every kind.
type profilingFlags struct {
	start, end    string
	limit         int
	limitSet      bool // --limit given explicitly; each view has its own default
	appOnly       bool
	raw           bool
	leafType      string
	showWaiting   bool
	survivorsOnly bool
	view          string // hotspots: stacks (default) or functions
	focus         string // hotspots: the function whose callers and callees to show
	state         string // hotspots functions/focus: which thread states to count
	sortBy        string // hotspots functions: self or total
}

// Default row caps when --limit is not given. The stack view's rows run to a
// few kB each, so an agent gets fewer of them by default (the envelope says
// how many there are and how to get the rest). The functions table is read
// top-down and its tail is noise, and the focus view caps its callers and its
// callees separately.
const (
	profilingStacksDefaultLimit      = 100
	profilingStacksAgentDefaultLimit = 20
	profilingFunctionsDefaultLimit   = 30
	profilingFocusDefaultLimit       = 10
)

const profilingLimitHelp = "cap the rows (0 = all). Default: 100 stacks (20 in agent mode); --view functions 30; --focus 10 callers and 10 callees"

const profilingViewsHelp = `
Views. The default prints one row per unique call stack, ranked by running
samples. A function reached through many paths is then split over many rows, so
it can be among the hottest without any single row showing it. Two views sum the
stacks per function:
  --view functions   one row per function: SELF (samples with the function
                     running, i.e. on top of the stack), TOTAL (samples with it
                     anywhere on the stack), their shares of all samples, and
                     PATHS (how many distinct call paths reach it). Sorted by
                     self; --sort total ranks by total. Default --limit 30.
  --focus <function> one function's callers and callees, each with its share of
                     the function's total time, plus the time on the function
                     itself. Takes an exact frame as printed, or a substring of
                     exactly one. Default --limit 10 callers and 10 callees.
Both count running (CPU) samples; --state lock, net_io, disk_io, wait, offcpu
(the four non-running states) or all counts others instead. With -o json/csv/
yaml they print their rows; recursion counts once per stack.`

func newProfilingHotspotsCmd() *cobra.Command {
	f := &profilingFlags{}
	c := &cobra.Command{
		Use:   "hotspots <entity>",
		Short: "Method CPU hotspots for a process",
		Long: `Method CPU hotspots for a process (PROCESS-xxx).

` + profilingExperimentalNotice + `
` + profilingLeafTypeHelp + `
` + profilingViewsHelp,
		Example: `  dtctl exec profiling hotspots PROCESS-ABC123
  dtctl exec profiling hotspots PROCESS-ABC123 --leaf-type service --limit 5
  dtctl exec profiling hotspots PROCESS-ABC123 --app-only \
    --default-timeframe-start 'now()-1h'
  # the same hour yesterday
  dtctl exec profiling hotspots PROCESS-ABC123 \
    --default-timeframe-start 'now()-25h' --default-timeframe-end 'now()-24h'
  # the top 5 stacks as JSON rows
  dtctl exec profiling hotspots PROCESS-ABC123 --limit 5 -o json
  # which functions use the most CPU, summed over every stack
  dtctl exec profiling hotspots PROCESS-ABC123 --view functions
  # who calls a hot function, and where its time goes
  dtctl exec profiling hotspots PROCESS-ABC123 --focus OrderService.load
  # which functions wait on locks
  dtctl exec profiling hotspots PROCESS-ABC123 --view functions --state lock`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			f.limitSet = cmd.Flags().Changed("limit")
			return runProfiling(cmd, "hotspots", args[0], f)
		},
	}
	addProfilingCommonFlags(c, f)
	c.Flags().StringVar(&f.leafType, "leaf-type", profiling.LeafTypeTotal, "CPU sample scope: total, service, or background")
	c.Flags().BoolVar(&f.showWaiting, "show-waiting", false, "include waiting (non-running) samples")
	c.Flags().StringVar(&f.view, "view", "stacks", "stacks (one row per call stack) or functions (one row per function, self and total)")
	c.Flags().StringVar(&f.focus, "focus", "", "show this function's callers and callees (an exact frame, or a substring of exactly one)")
	c.Flags().StringVar(&f.state, "state", "running", "thread states --view functions/--focus count: running, lock, net_io, disk_io, wait, offcpu, or all")
	c.Flags().StringVar(&f.sortBy, "sort", "self", "--view functions order: self or total")
	stability.Mark(c, stability.Experimental, profilingSince)
	return c
}

func newProfilingAllocationsCmd() *cobra.Command {
	f := &profilingFlags{}
	c := &cobra.Command{
		Use:     "allocations <entity>",
		Aliases: []string{"memory"},
		Short:   "Memory allocation analysis for a process (alias: memory)",
		Long: `Memory allocation analysis for a process (PROCESS-xxx). Alias: memory.

` + profilingExperimentalNotice + `

Shows where allocations happen and what survived GC within the window —
allocation pressure, not live retained heap. It cannot say which objects take
up the heap now, does not prove a leak and does not predict OOM; confirm those
with a heap/RSS trend and pod restart/OOM state.

What the numbers mean:
  alloc_bytes, alloc_count        bytes and objects allocated in the window.
  survived_bytes, survived_count  summed over every GC an object lived through
                                  until the agent's next report (about a
                                  minute): a 10-byte object that survives 3 GCs
                                  adds 30 bytes and 3 to the count. They are not
                                  a size: they can exceed alloc_bytes, and they
                                  say nothing about how much memory is held.
                                  Don't report them as heap used or retained.
  survived/allocated              GCs the average allocated byte saw within its
                                  minute. About 1 is the floor for objects that
                                  die young (the GC that frees one counts).
                                  Higher means more GCs, which tracks GC
                                  frequency as much as retention: not a leak
                                  signal on its own.
Rows are ranked by allocated bytes, or by survived bytes with --survivors-only.
NOTE the allocation tree is INVERTED vs hotspots: the allocation-site method is
at the root and its callers hang below as children, so each stack reads
allocation site first.`,
		Example: `  dtctl exec profiling allocations PROCESS-DEF456
  dtctl exec profiling allocations PROCESS-DEF456 --survivors-only`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			f.limitSet = cmd.Flags().Changed("limit")
			return runProfiling(cmd, "allocations", args[0], f)
		},
	}
	addProfilingCommonFlags(c, f)
	c.Flags().BoolVar(&f.survivorsOnly, "survivors-only", false, "restrict to surviving allocations, ranked by survived bytes")
	stability.Mark(c, stability.Experimental, profilingSince)
	return c
}

func addProfilingCommonFlags(c *cobra.Command, f *profilingFlags) {
	fl := c.Flags()
	fl.StringVar(&f.start, "default-timeframe-start", "", "window start — ISO-8601/RFC3339, epoch millis, or now()-<dur>, e.g. now()-1d (default: 2h before the end)")
	fl.StringVar(&f.end, "default-timeframe-end", "", "window end — ISO-8601/RFC3339, epoch millis, or now()-<dur>; not in the future (default: 2h after the start, else now(); window max 2h)")
	fl.IntVar(&f.limit, "limit", profilingStacksDefaultLimit, profilingLimitHelp)
	fl.BoolVar(&f.appOnly, "app-only", false, "show only application frames, stripping runtime/library frames (JRE, Spring, npm packages, node:) and OneAgent frames")
	fl.BoolVar(&f.raw, "raw", false, "print the API's whole (compacted) result instead of rows; large. Use with -o json or yaml")
}

// profilingAPIKinds maps a kind subcommand to the API kind it runs.
var profilingAPIKinds = map[string]string{
	"hotspots":    profiling.Kind["hotspots"],
	"allocations": profiling.Kind["memory"],
}

// profilingRun is what one analysis was run on, for the agent envelope.
type profilingRun struct {
	kind          string // hotspots or allocations
	entity        string
	leafType      string
	status        string
	from, to      int64
	defaultWindow bool
}

// annotate fills the envelope's window and profile fields.
func (r *profilingRun) annotate(ap *output.AgentPrinter, view, state string) *output.ProfileContext {
	ctx := ap.Context()
	ctx.Window = &output.TimeWindow{
		From: time.UnixMilli(r.from).UTC().Format(time.RFC3339),
		To:   time.UnixMilli(r.to).UTC().Format(time.RFC3339),
		Span: spanString(time.Duration(r.to-r.from) * time.Millisecond),
	}
	if r.defaultWindow {
		ctx.Window.Note = "no window given, so the last 2h; set one with --default-timeframe-start/--default-timeframe-end"
	}
	p := &output.ProfileContext{
		Entity: r.entity,
		Kind:   r.kind,
		View:   view,
		State:  state,
		Status: r.status,
	}
	if api := profiling.APIEntityID(r.entity); api != r.entity {
		p.APIEntity = api
	}
	if r.kind == "hotspots" {
		p.LeafType = r.leafType
		if p.LeafType == "" {
			p.LeafType = profiling.LeafTypeTotal
		}
	}
	ctx.Profile = p
	return p
}

// runProfiling validates the input, runs one analysis and prints it. Every
// input check happens before the config is loaded, so a typo fails fast
// rather than after a multi-second analysis.
func runProfiling(cmd *cobra.Command, kind, entityID string, f *profilingFlags) error {
	ctx := cmdContext(cmd)

	if !isEligibleProfilingEntity(entityID) {
		return fmt.Errorf("invalid entity %q: profiling takes a process id, PROCESS-xxx (classic PROCESS_GROUP-xxx and PROCESS_GROUP_INSTANCE-xxx ids work too; SERVICE ids do not).\nFind a process id:\n  %s", entityID, profilingFindIDsHint)
	}
	switch f.leafType {
	case "", profiling.LeafTypeTotal, profiling.LeafTypeService, profiling.LeafTypeBackground:
	default:
		return fmt.Errorf("invalid --leaf-type %q: use total, service, or background", f.leafType)
	}
	if err := validateProfilingView(f); err != nil {
		return err
	}
	if f.raw {
		if f.focus != "" || (f.view != "" && f.view != "stacks") {
			return fmt.Errorf("--raw prints the API's whole result; drop --view/--focus")
		}
		switch outputFormat(ctx) {
		case "csv", "parquet":
			return fmt.Errorf("--raw prints a nested document; use -o json or -o yaml")
		}
	}
	window, err := profiling.ResolveWindow(f.start, f.end, time.Now())
	if err != nil {
		return err
	}

	// Code-level analysis only reads profiling data — gate on OperationRead.
	_, c, err := setupWithSafety(ctx, safety.OperationRead)
	if err != nil {
		return err
	}

	progress := output.NewProgressReporterTo(true, agentMode(ctx), currentStderr(ctx))
	progress.Update(output.ProgressState{Label: fmt.Sprintf("running %s analysis", kind)})
	analysis, err := profiling.NewHandler(c).Analyze(cmd.Context(), profiling.Payload{
		Kind:          profilingAPIKinds[kind],
		EntityID:      entityID,
		From:          window.From,
		To:            window.To,
		LeafType:      f.leafType,
		ShowWaiting:   f.showWaiting,
		SurvivorsOnly: f.survivorsOnly,
	})
	progress.Stop()
	if err != nil {
		return profilingAPIError(err)
	}

	run := &profilingRun{
		kind:          kind,
		entity:        entityID,
		leafType:      f.leafType,
		status:        analysis.Status,
		from:          window.From,
		to:            window.To,
		defaultWindow: window.Default,
	}
	switch {
	case f.raw:
		return renderProfilingRaw(ctx, run, analysis)
	case f.focus != "":
		return renderProfilingFocus(ctx, run, analysis, f)
	case f.view == "functions":
		return renderProfilingFunctions(ctx, run, analysis, f)
	}
	return renderProfilingStacks(ctx, run, analysis, f)
}

// validateProfilingView checks the hotspots view flags. A flag that only one
// view reads is refused with the others, rather than silently ignored. Flags a
// kind does not register stay at their zero value and pass.
func validateProfilingView(f *profilingFlags) error {
	switch f.view {
	case "", "stacks", "functions":
	default:
		return fmt.Errorf("invalid --view %q: use stacks or functions", f.view)
	}
	if f.focus != "" && f.view == "functions" {
		return fmt.Errorf("--focus prints its own view (callers and callees); drop --view functions")
	}
	perFunction := f.focus != "" || f.view == "functions"
	if f.state != "" && !profiling.ValidState(f.state) {
		return fmt.Errorf("invalid --state %q: use %s", f.state, strings.Join(profiling.States, ", "))
	}
	if f.state != "" && f.state != "running" && !perFunction {
		return fmt.Errorf("--state applies to --view functions and --focus; the stack view always carries all five thread-state counts")
	}
	switch f.sortBy {
	case "", "self":
	case "total":
		if f.view != "functions" {
			return fmt.Errorf("--sort applies to --view functions")
		}
	default:
		return fmt.Errorf("invalid --sort %q: use self or total", f.sortBy)
	}
	return nil
}

// profilingLimit is the row cap for a view: --limit when given, else the
// view's default.
func profilingLimit(ctx context.Context, f *profilingFlags, def int) int {
	if f.limitSet {
		return f.limit
	}
	if def == profilingStacksDefaultLimit && agentMode(ctx) {
		return profilingStacksAgentDefaultLimit
	}
	return def
}

// renderProfilingStacks prints the stack view: one row per unique stack. A
// table (the default outside agent mode) prints the rows as folded lines; every
// structured format, --jq and the agent envelope get the same rows as fields.
// --limit caps every one of them the same way.
func renderProfilingStacks(ctx context.Context, run *profilingRun, a *profiling.Analysis, f *profilingFlags) error {
	var rows interface{}
	var n int
	var folded func() string
	unit := "stacks, by running samples"
	var profile func(p *output.ProfileContext)
	if run.kind == "allocations" {
		view := a.Allocations(f.appOnly, f.survivorsOnly)
		all := view.Rows
		n = len(all)
		key := "allocated"
		if f.survivorsOnly {
			key = "survived"
		}
		unit = "allocation stacks, by " + key + " bytes"
		limited := capProfilingRows(all, profilingLimit(ctx, f, profilingStacksDefaultLimit))
		rows = limited
		folded = func() string { return profiling.FoldAllocations(limited, f.survivorsOnly) }
		profile = func(p *output.ProfileContext) {
			p.AllocatedBytes, p.SurvivedBytes = &view.AllocatedBytes, &view.SurvivedBytes
		}
	} else {
		view := a.Stacks(f.appOnly)
		all := view.Rows
		n = len(all)
		limited := capProfilingRows(all, profilingLimit(ctx, f, profilingStacksDefaultLimit))
		rows = limited
		folded = func() string { return profiling.FoldStacks(limited) }
		profile = func(p *output.ProfileContext) {
			p.Samples = &view.Samples
			p.State = "running"
		}
	}
	shown := profilingRowCount(rows)

	if n == 0 {
		_, note := a.WhyEmpty(f.appOnly, "")
		return printProfilingEmpty(ctx, run, "stacks", "", rows, note)
	}
	truncated := ""
	if shown < n {
		truncated = fmt.Sprintf("showing %d of %d %s; --limit 0 lists them all", shown, n, unit)
	}

	if agentMode(ctx) {
		p := newPrinterCtx(ctx)
		if ap := enrichAgent(p, "exec", "profiling"); ap != nil {
			profile(run.annotate(ap, "stacks", ""))
			ap.SetTotal(n)
			if truncated != "" {
				ap.SetHasMore(true)
				ap.SetWarnings([]string{truncated})
			}
			if run.kind == "allocations" {
				ap.SetSuggestions([]string{profiling.SurvivedNote})
			}
			if run.kind == "hotspots" {
				ap.SetSuggestions([]string{"one row per call stack, ranked by running samples: to rank functions, summed over every stack they appear in, use --view functions (--state lock|wait|offcpu for blocked time); for one function's callers, --focus <function>"})
			}
		}
		return p.PrintList(rows)
	}
	if isProfilingTableFormat(ctx) && jqFilter(ctx) == "" {
		w := currentStdout(ctx)
		if _, err := fmt.Fprint(w, folded()); err != nil {
			return err
		}
		if truncated != "" {
			fmt.Fprintf(w, "# %s\n", truncated)
		}
		return nil
	}
	return newPrinterCtx(ctx).PrintList(rows)
}

func capProfilingRows[T any](rows []T, limit int) []T {
	if limit > 0 && len(rows) > limit {
		return rows[:limit]
	}
	if rows == nil {
		return []T{}
	}
	return rows
}

func profilingRowCount(rows interface{}) int {
	switch r := rows.(type) {
	case []profiling.StackRow:
		return len(r)
	case []profiling.AllocationRow:
		return len(r)
	}
	return 0
}

// isProfilingTableFormat reports whether the output format is a table, which
// the stack view prints as folded lines.
func isProfilingTableFormat(ctx context.Context) bool {
	switch outputFormat(ctx) {
	case "", "table", "wide", "folded":
		return true
	}
	return false
}

// renderProfilingRaw prints the API's whole (compacted) result: JSON unless
// another structured format was asked for.
func renderProfilingRaw(ctx context.Context, run *profilingRun, a *profiling.Analysis) error {
	p := newPrinterCtx(ctx)
	if ap := enrichAgent(p, "exec", "profiling"); ap != nil {
		run.annotate(ap, "raw", "")
	} else if isProfilingTableFormat(ctx) {
		p = newPrinterOpts(ctx, output.PrinterOptions{Format: "json", PlainMode: plainMode(ctx), JQFilter: jqFilter(ctx)})
	}
	return p.Print(a.Raw())
}

// renderProfilingFunctions prints the functions view: rows through the standard
// printer, so a table, -o json/csv/yaml, --jq and the agent envelope all work.
func renderProfilingFunctions(ctx context.Context, run *profilingRun, a *profiling.Analysis, f *profilingFlags) error {
	t := a.Functions(f.appOnly, f.state, f.sortBy)
	if t == nil {
		_, note := a.WhyEmpty(f.appOnly, f.state)
		return printProfilingEmpty(ctx, run, "functions", f.state, []profiling.FunctionRow{}, note)
	}
	limit := profilingLimit(ctx, f, profilingFunctionsDefaultLimit)
	rows := capProfilingRows(t.Rows, limit)
	sortKey := f.sortBy
	if sortKey == "" {
		sortKey = "self"
	}
	note := fmt.Sprintf("%d of %d functions, by %s %s samples (%d %s samples in the window)",
		len(rows), len(t.Rows), sortKey, f.state, t.Samples, f.state)
	var warnings, hints []string
	if len(rows) < len(t.Rows) {
		warnings = append(warnings, fmt.Sprintf("showing %d of %d functions, by %s %s samples; --limit 0 lists every function", len(rows), len(t.Rows), sortKey, f.state))
	}
	if len(rows) > 0 {
		hints = append(hints, fmt.Sprintf("--focus '%s' shows its callers and callees", rows[0].Function))
	}
	samples := t.Samples
	return printProfilingRows(ctx, run, "functions", f.state, &samples, rows, len(t.Rows), note, warnings, hints)
}

// renderProfilingFocus prints one function's callers and callees.
func renderProfilingFocus(ctx context.Context, run *profilingRun, a *profiling.Analysis, f *profilingFlags) error {
	limit := profilingLimit(ctx, f, profilingFocusDefaultLimit)
	rows, fn, nCallers, nCallees, err := a.Focus(f.appOnly, f.state, f.focus, limit)
	if err != nil {
		if a.Functions(f.appOnly, f.state, "self") == nil {
			_, note := a.WhyEmpty(f.appOnly, f.state)
			return printProfilingEmpty(ctx, run, "focus", f.state, []profiling.FocusRow{}, note)
		}
		return err
	}
	note := fmt.Sprintf("%s: %d callers, %d callees, shares of its total %s samples", fn, nCallers, nCallees, f.state)
	var warnings []string
	if limit > 0 && (nCallers > limit || nCallees > limit) {
		warnings = append(warnings, fmt.Sprintf("showing up to %d callers and %d callees; --limit 0 lists them all", limit, limit))
	}
	hints := []string{note, "--focus <caller or callee> walks the call graph further"}
	// The total counts every row before --limit: the total and self rows plus
	// all callers and callees, so has_more agrees with it.
	return printProfilingRows(ctx, run, "focus", f.state, nil, rows, 2+nCallers+nCallees, "", warnings, hints)
}

// printProfilingRows prints a per-function view's rows. A table gets the note,
// warnings and hints as trailing # lines; the agent envelope carries the
// warnings (truncation) as context.warnings and the hints as suggestions;
// structured formats get the rows alone.
func printProfilingRows(ctx context.Context, run *profilingRun, view, state string, samples *int, rows interface{}, total int, note string, warnings, hints []string) error {
	p := newPrinterCtx(ctx)
	if ap := enrichAgent(p, "exec", "profiling"); ap != nil {
		run.annotate(ap, view, state).Samples = samples
		ap.SetTotal(total)
		if len(warnings) > 0 {
			ap.SetHasMore(true)
			ap.SetWarnings(warnings)
		}
		ap.SetSuggestions(hints)
	}
	if err := p.PrintList(rows); err != nil {
		return err
	}
	if agentMode(ctx) || jqFilter(ctx) != "" {
		return nil
	}
	switch outputFormat(ctx) {
	case "", "table", "wide":
		w := currentStdout(ctx)
		if note != "" {
			fmt.Fprintf(w, "# %s\n", note)
		}
		for _, h := range append(warnings, hints...) {
			if h == note {
				continue
			}
			fmt.Fprintf(w, "# %s\n", h)
		}
	}
	return nil
}

// printProfilingEmpty prints a view that has no rows. A table gets the note in
// place of the rows; the agent envelope gets an empty result with the note in
// context.warnings; structured formats get the empty list on stdout and the
// note on stderr.
func printProfilingEmpty(ctx context.Context, run *profilingRun, view, state string, empty interface{}, note string) error {
	if agentMode(ctx) {
		p := newPrinterCtx(ctx)
		if ap := enrichAgent(p, "exec", "profiling"); ap != nil {
			run.annotate(ap, view, state)
			ap.SetTotal(0)
			ap.SetWarnings([]string{note})
		}
		return p.PrintList(empty)
	}
	if isProfilingTableFormat(ctx) && jqFilter(ctx) == "" {
		_, err := fmt.Fprintf(currentStdout(ctx), "# %s\n", note)
		return err
	}
	fmt.Fprintf(currentStderr(ctx), "# %s\n", note)
	return newPrinterCtx(ctx).PrintList(empty)
}

// profilingAPIError adds profiling-specific advice to the API failures a
// caller can act on. Everything else passes through unchanged and gets dtctl's
// generic handling.
func profilingAPIError(err error) error {
	f, ok := profiling.ClassifyError(err)
	if !ok {
		return err
	}
	return &diagnostic.Error{
		Operation:   "run profiling analysis",
		StatusCode:  f.StatusCode,
		Message:     f.Message,
		Suggestions: f.Advice,
		Err:         err,
	}
}

// isEligibleProfilingEntity reports whether id is a process-level entity the
// code-level analysis API accepts (PROCESS_GROUP, PROCESS_GROUP_INSTANCE, or the
// 3rd-gen PROCESS rename). "PROCESS-" does not prefix-match "PROCESS_GROUP-" (the
// 8th byte is '-' vs '_'), so each type is matched distinctly. The SDK enforces
// the same allowlist; this CLI check exists to fail fast with a next step.
func isEligibleProfilingEntity(id string) bool {
	return strings.HasPrefix(id, "PROCESS-") ||
		strings.HasPrefix(id, "PROCESS_GROUP-") ||
		strings.HasPrefix(id, "PROCESS_GROUP_INSTANCE-")
}
