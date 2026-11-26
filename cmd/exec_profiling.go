package cmd

import (
	"context"
	"errors"
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

// profilingFindIDsQuery lists eligible entity ids.
const profilingFindIDsQuery = `dtctl query 'smartscapeNodes PROCESS | fields id, dt.process_group.detected_name, dt.process_group.id'`

// profilingFindIDsHint tells the caller how to find an eligible entity id.
const profilingFindIDsHint = profilingFindIDsQuery + `
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

Timeframe: --default-timeframe-start/--default-timeframe-end take ISO-8601/
RFC3339, epoch millis, or a now()-relative time (now()-1d, now()-90m,
now()-1h30m). Without either, the last ` + profilingDefaultSpan + `. A start alone covers the ` + profilingDefaultSpan + `
after it, an end alone the ` + profilingDefaultSpan + ` before it. The window must not exceed ` + profilingMaxSpan + `, must not
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

// The --limit help of each kind. allocations has the stack view only.
const (
	profilingAllocationsLimitHelp = "cap the rows (0 = all). Default: 100 stacks (20 in agent mode)"
	profilingHotspotsLimitHelp    = profilingAllocationsLimitHelp + "; --view functions 30; --focus 10 callers and 10 callees"
)

// The default and the longest window as the help spells them, read from the
// package that applies them.
var (
	profilingDefaultSpan = spanString(profiling.DefaultWindow)
	profilingMaxSpan     = spanString(profiling.MaxWindow)
)

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
	addProfilingCommonFlags(c, f, profilingHotspotsLimitHelp)
	c.Flags().StringVar(&f.leafType, "leaf-type", string(profiling.LeafTypeTotal), "CPU sample scope: total, service, or background")
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
	addProfilingCommonFlags(c, f, profilingAllocationsLimitHelp)
	c.Flags().BoolVar(&f.survivorsOnly, "survivors-only", false, "restrict to surviving allocations, ranked by survived bytes")
	stability.Mark(c, stability.Experimental, profilingSince)
	return c
}

func addProfilingCommonFlags(c *cobra.Command, f *profilingFlags, limitHelp string) {
	fl := c.Flags()
	fl.StringVar(&f.start, "default-timeframe-start", "", "window start — ISO-8601/RFC3339, epoch millis, or now()-<dur>, e.g. now()-1d (default: "+profilingDefaultSpan+" before the end)")
	fl.StringVar(&f.end, "default-timeframe-end", "", "window end — ISO-8601/RFC3339, epoch millis, or now()-<dur>; not in the future (default: "+profilingDefaultSpan+" after the start, else now(); window max "+profilingMaxSpan+")")
	fl.IntVar(&f.limit, "limit", profilingStacksDefaultLimit, limitHelp)
	fl.BoolVar(&f.appOnly, "app-only", false, "show only application frames, stripping runtime/library frames (JRE, Spring, npm packages, node:) and OneAgent frames")
	fl.BoolVar(&f.raw, "raw", false, "print the API's whole (compacted) result instead of rows; large. Use with -o json or yaml")
}

// profilingRun is what one analysis was run on, for the agent envelope.
type profilingRun struct {
	kind     string // hotspots or allocations
	entity   string
	leafType profiling.LeafType
	status   string
	window   profiling.Window
}

// annotate fills the envelope's window and profile fields.
func (r *profilingRun) annotate(ap *output.AgentPrinter, view, state string) *output.ProfileContext {
	ctx := ap.Context()
	ctx.Window = &output.TimeWindow{
		From: time.UnixMilli(r.window.From).UTC().Format(time.RFC3339),
		To:   time.UnixMilli(r.window.To).UTC().Format(time.RFC3339),
		Span: spanString(r.window.Span()),
	}
	if r.window.Default {
		ctx.Window.Note = fmt.Sprintf("no window given, so the last %s; set one with --default-timeframe-start/--default-timeframe-end", spanString(profiling.DefaultWindow))
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
		p.LeafType = string(r.leafType)
	}
	ctx.Profile = p
	return p
}

// profilingError is a failure this command words itself, with the agent error
// code decided where the failure is recognised. The text quotes what the
// caller typed and the function names of the profile, so a code read off the
// text would follow them: --focus Config was reported as a config_error. The
// text, the exit code and the code of every other input are what they were.
type profilingError struct {
	code string // the agent error code
	err  error
}

func (e *profilingError) Error() string { return e.err.Error() }
func (e *profilingError) Unwrap() error { return e.err }

// profilingInvalid is the error for a value the caller passed that the command
// refuses before any request.
func profilingInvalid(format string, args ...interface{}) error {
	return &profilingError{code: "validation_error", err: fmt.Errorf(format, args...)}
}

// profilingFailed is the error for every other failure the command words: a
// combination of flags it refuses, and a run that could not give what was
// asked for.
func profilingFailed(format string, args ...interface{}) error {
	return &profilingError{code: "error", err: fmt.Errorf(format, args...)}
}

// runProfiling validates the input, runs one analysis and prints it. Every
// input check happens before the config is loaded, so a typo fails fast
// rather than after a multi-second analysis.
func runProfiling(cmd *cobra.Command, kind, entityID string, f *profilingFlags) error {
	ctx := cmdContext(cmd)

	if !profiling.EligibleEntity(entityID) {
		return profilingInvalid("invalid entity %q: profiling takes a process id, PROCESS-xxx (classic PROCESS_GROUP-xxx and PROCESS_GROUP_INSTANCE-xxx ids work too; SERVICE ids do not).\nFind a process id:\n  %s", entityID, profilingFindIDsHint)
	}
	leafType, err := profiling.ParseLeafType(f.leafType)
	if err != nil {
		return profilingInvalid("invalid --leaf-type %q: use total, service, or background", f.leafType)
	}
	state, sortKey, err := profilingViewOptions(f)
	if err != nil {
		return err
	}
	if f.raw {
		if f.focus != "" || (f.view != "" && f.view != "stacks") {
			return profilingFailed("--raw prints the API's whole result; drop --view/--focus")
		}
		switch outputFormat(ctx) {
		case "csv", "parquet":
			return profilingFailed("--raw prints a nested document; use -o json or -o yaml")
		}
	}
	window, err := profiling.ResolveWindow(f.start, f.end, time.Now())
	if err != nil {
		return profilingWindowError(err)
	}

	// Code-level analysis only reads profiling data — gate on OperationRead.
	_, c, err := setupWithSafety(ctx, safety.OperationRead)
	if err != nil {
		return err
	}

	run := &profilingRun{kind: kind, entity: entityID, leafType: leafType, window: window}
	h := profiling.NewHandler(c)
	progress := output.NewProgressReporterTo(true, agentMode(ctx), currentStderr(ctx))
	progress.Update(output.ProgressState{Label: fmt.Sprintf("running %s analysis", kind)})

	if kind == "allocations" {
		a, err := h.Allocations(ctx, profiling.AllocationsRequest{
			Entity:        entityID,
			Window:        window,
			SurvivorsOnly: f.survivorsOnly,
		})
		progress.Stop()
		if err != nil {
			return profilingAPIError(err)
		}
		run.status = a.Status
		if f.raw {
			return renderProfilingRaw(ctx, run, a.Raw())
		}
		return renderProfilingAllocations(ctx, run, a, f)
	}

	hs, err := h.Hotspots(ctx, profiling.HotspotsRequest{
		Entity:      entityID,
		Window:      window,
		LeafType:    leafType,
		ShowWaiting: f.showWaiting,
	})
	progress.Stop()
	if err != nil {
		return profilingAPIError(err)
	}
	run.status = hs.Status
	switch {
	case f.raw:
		return renderProfilingRaw(ctx, run, hs.Raw())
	case f.focus != "":
		return renderProfilingFocus(ctx, run, hs, f, state)
	case f.view == "functions":
		return renderProfilingFunctions(ctx, run, hs, f, state, sortKey)
	}
	return renderProfilingStacks(ctx, run, hs, f)
}

// profilingWindowError words a window ResolveWindow refused, naming the flags
// its bounds come from.
func profilingWindowError(err error) error {
	var bound *profiling.WindowBoundError
	var tooLong *profiling.WindowTooLongError
	switch {
	case errors.As(err, &bound):
		return profilingInvalid("invalid --default-timeframe-%s: %w", bound.Bound, bound.Err)
	case errors.Is(err, profiling.ErrWindowStartInFuture):
		return profilingInvalid("invalid timeframe: the start is in the future — profiling only covers the past (e.g. --default-timeframe-start 'now()-%s')", profilingDefaultSpan)
	case errors.Is(err, profiling.ErrWindowEndInFuture):
		return profilingInvalid("invalid timeframe: the end is in the future — profiling only covers the past; leave --default-timeframe-end out to end now")
	case errors.Is(err, profiling.ErrWindowEmpty):
		return profilingInvalid("invalid timeframe: the end must be after the start")
	case errors.As(err, &tooLong):
		return profilingInvalid("invalid timeframe: the window must not exceed %s (got %s) — narrow it; the window can lie anywhere in the past",
			spanString(tooLong.Max), tooLong.Requested.Span())
	}
	return err
}

// profilingViewOptions checks the hotspots view flags and returns the thread
// state and the sort key they select. A flag that only one view reads is
// refused with the others, rather than silently ignored. Flags a kind does not
// register stay at their zero value and pass.
func profilingViewOptions(f *profilingFlags) (profiling.State, profiling.SortKey, error) {
	switch f.view {
	case "", "stacks", "functions":
	default:
		return "", "", profilingInvalid("invalid --view %q: use stacks or functions", f.view)
	}
	if f.focus != "" && f.view == "functions" {
		return "", "", profilingFailed("--focus prints its own view (callers and callees); drop --view functions")
	}
	perFunction := f.focus != "" || f.view == "functions"
	state, err := profiling.ParseState(f.state)
	if err != nil {
		var names []string
		for _, s := range profiling.States() {
			names = append(names, string(s))
		}
		return "", "", profilingInvalid("invalid --state %q: use %s", f.state, strings.Join(names, ", "))
	}
	if state != profiling.StateRunning && !perFunction {
		return "", "", profilingFailed("--state applies to --view functions and --focus; the stack view always carries all five thread-state counts")
	}
	sortKey, err := profiling.ParseSortKey(f.sortBy)
	if err != nil {
		return "", "", profilingInvalid("invalid --sort %q: use self or total", f.sortBy)
	}
	if sortKey == profiling.SortByTotal && f.view != "functions" {
		return "", "", profilingFailed("--sort applies to --view functions")
	}
	return state, sortKey, nil
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

// renderProfilingStacks prints the hotspots stack view: one row per unique
// stack.
func renderProfilingStacks(ctx context.Context, run *profilingRun, h *profiling.Hotspots, f *profilingFlags) error {
	view := h.Stacks(f.appOnly)
	if view.Empty != "" {
		return printProfilingEmpty(ctx, run, "stacks", "", []profiling.StackRow{}, view.Empty)
	}
	rows := capProfilingRows(view.Rows, profilingLimit(ctx, f, profilingStacksDefaultLimit))
	return printProfilingStacks(ctx, run, profilingStackOutput{
		rows:   rows,
		shown:  len(rows),
		total:  len(view.Rows),
		unit:   "stacks, by running samples",
		folded: func() string { return foldProfilingStacks(rows) },
		profile: func(p *output.ProfileContext) {
			p.Samples = &view.Samples
			p.State = "running"
		},
		suggestion: "one row per call stack, ranked by running samples: to rank functions, summed over every stack they appear in, use --view functions (--state lock|wait|offcpu for blocked time); for one function's callers, --focus <function>",
	})
}

// renderProfilingAllocations prints the allocations stack view: one row per
// unique allocation path.
func renderProfilingAllocations(ctx context.Context, run *profilingRun, a *profiling.Allocations, f *profilingFlags) error {
	view := a.Stacks(f.appOnly, f.survivorsOnly)
	if view.Empty != "" {
		return printProfilingEmpty(ctx, run, "stacks", "", []profiling.AllocationRow{}, view.Empty)
	}
	key := "allocated"
	if f.survivorsOnly {
		key = "survived"
	}
	rows := capProfilingRows(view.Rows, profilingLimit(ctx, f, profilingStacksDefaultLimit))
	return printProfilingStacks(ctx, run, profilingStackOutput{
		rows:   rows,
		shown:  len(rows),
		total:  len(view.Rows),
		unit:   "allocation stacks, by " + key + " bytes",
		folded: func() string { return foldProfilingAllocations(rows, f.survivorsOnly) },
		profile: func(p *output.ProfileContext) {
			p.AllocatedBytes, p.SurvivedBytes = &view.AllocatedBytes, &view.SurvivedBytes
		},
		suggestion: profilingSurvivedNote,
	})
}

// profilingSurvivedNote says what the survived figures are, for every output
// that prints them: the API sums them over each GC an object lives through, so
// they read as a size (and as heap held) when they are not one.
const profilingSurvivedNote = "survived_bytes/survived_count are summed over every GC an object lived through within about a minute (10 bytes surviving 3 GCs = 30): not a size, can exceed alloc_bytes, not heap used or retained; survived/allocated ~1 = dies young, higher tracks GC frequency, not a leak signal alone"

// foldProfilingStacks renders stack rows as collapsed (folded) stacks, one
// line per row:
// `frame1;frame2;...;frameN running=.. lock=.. net_io=.. disk_io=.. wait=..`.
// Unlike single-metric folded output a line keeps all five thread-state
// counts, so one read shows whether a stack is CPU-bound or blocked without a
// separate flamegraph per state.
func foldProfilingStacks(rows []profiling.StackRow) string {
	var sb strings.Builder
	for _, r := range rows {
		fmt.Fprintf(&sb, "%s running=%d lock=%d net_io=%d disk_io=%d wait=%d\n",
			r.Stack, r.Running, r.Lock, r.NetIO, r.DiskIO, r.Wait)
	}
	return sb.String()
}

// foldProfilingAllocations renders allocation rows as folded lines weighted by
// bytes, under a header that names the sort key.
func foldProfilingAllocations(rows []profiling.AllocationRow, bySurvivors bool) string {
	if len(rows) == 0 {
		return ""
	}
	key := "alloc"
	if bySurvivors {
		key = "surv"
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "# ALLOCATIONS (bytes, sorted by %s; surv = bytes x GCs survived: not a size, can exceed alloc, not live heap)\n", key)
	for _, l := range rows {
		fmt.Fprintf(&sb, "%s alloc=%d surv=%d alloc_count=%d surv_count=%d\n",
			l.Stack, l.AllocBytes, l.SurvivedBytes, l.AllocCount, l.SurvivedCount)
	}
	return sb.String()
}

// profilingStackOutput is a stack view ready to print, of either kind.
type profilingStackOutput struct {
	rows         interface{} // the rows after --limit
	shown, total int         // row counts after and before --limit
	unit         string      // what the rows are and how they are ranked
	folded       func() string
	profile      func(*output.ProfileContext) // sets the totals the rows are shares of
	suggestion   string
}

// printProfilingStacks prints a stack view that has rows. A table (the default
// outside agent mode) prints them as folded lines; every structured format,
// --plain, --jq and the agent envelope get the same rows as fields. --limit
// caps every one of them the same way.
func printProfilingStacks(ctx context.Context, run *profilingRun, o profilingStackOutput) error {
	truncated := ""
	if o.shown < o.total {
		truncated = fmt.Sprintf("showing %d of %d %s; --limit 0 lists them all", o.shown, o.total, o.unit)
	}

	if agentMode(ctx) {
		p := newProfilingPrinter(ctx)
		if ap := enrichAgent(p, "exec", "profiling"); ap != nil {
			o.profile(run.annotate(ap, "stacks", ""))
			ap.SetTotal(o.total)
			if truncated != "" {
				ap.SetHasMore(true)
				ap.SetWarnings([]string{truncated})
			}
			ap.SetSuggestions([]string{o.suggestion})
		}
		return p.PrintList(o.rows)
	}
	if isProfilingTextFormat(ctx) && jqFilter(ctx) == "" {
		w := currentStdout(ctx)
		if _, err := fmt.Fprint(w, o.folded()); err != nil {
			return err
		}
		if truncated != "" {
			fmt.Fprintf(w, "# %s\n", truncated)
		}
		return nil
	}
	return newProfilingPrinter(ctx).PrintList(o.rows)
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

// profilingFormat is the output format the command prints in. An empty one
// (-o "$FORMAT" with the variable unset) is the default, a table. The command
// settles that here, once, because the shared code does not agree with itself
// about an empty format beside --plain: output.IsTabularFormat reads it as a
// table --plain turns into JSON, and the printer turns only a format named
// table or wide into JSON.
func profilingFormat(ctx context.Context) string {
	if format := outputFormat(ctx); format != "" {
		return format
	}
	return "table"
}

// newProfilingPrinter is newPrinterCtx for profilingFormat.
func newProfilingPrinter(ctx context.Context) output.Printer {
	if agentMode(ctx) || outputFormat(ctx) != "" {
		return newPrinterCtx(ctx)
	}
	format, plain := profilingFormat(ctx), plainMode(ctx)
	p := newPrinterOpts(ctx, output.PrinterOptions{Format: format, PlainMode: plain, JQFilter: jqFilter(ctx)})
	return shapeListOutput(ctx, p, format, output.IsTabularFormat(format, plain))
}

// isProfilingTextFormat reports whether the output is text for a person to
// read, not a document for a program: a table, or the stack view's folded
// layout named with -o folded. The stack view then prints folded lines, an
// empty view prints its note on stdout, and --raw prints JSON. --plain turns a
// table into JSON (output.NewPrinterWithOpts), so with it a table is not text,
// and stdout carries the JSON alone.
func isProfilingTextFormat(ctx context.Context) bool {
	return isProfilingTableFormat(ctx) || profilingFormat(ctx) == "folded"
}

// isProfilingTableFormat reports whether the output is a table, which a
// per-function view ends with # lines. -o folded is none: those views have no
// folded form and print their rows for it as a table without the lines.
func isProfilingTableFormat(ctx context.Context) bool {
	format := profilingFormat(ctx)
	return (format == "table" || format == "wide") && output.IsTabularFormat(format, plainMode(ctx))
}

// renderProfilingRaw prints the API's whole (compacted) result: JSON unless
// another structured format was asked for.
func renderProfilingRaw(ctx context.Context, run *profilingRun, raw map[string]interface{}) error {
	p := newProfilingPrinter(ctx)
	if ap := enrichAgent(p, "exec", "profiling"); ap != nil {
		run.annotate(ap, "raw", "")
	} else if isProfilingTextFormat(ctx) {
		p = newPrinterOpts(ctx, output.PrinterOptions{Format: "json", PlainMode: plainMode(ctx), JQFilter: jqFilter(ctx)})
	}
	return p.Print(raw)
}

// renderProfilingFunctions prints the functions view: rows through the standard
// printer, so a table, -o json/csv/yaml, --jq and the agent envelope all work.
func renderProfilingFunctions(ctx context.Context, run *profilingRun, h *profiling.Hotspots, f *profilingFlags, state profiling.State, sortKey profiling.SortKey) error {
	view, err := h.Functions(f.appOnly, state, sortKey)
	if err != nil {
		return err
	}
	if view.Empty != "" {
		return printProfilingEmpty(ctx, run, "functions", string(state), []profiling.FunctionRow{}, view.Empty)
	}
	limit := profilingLimit(ctx, f, profilingFunctionsDefaultLimit)
	rows := capProfilingRows(view.Rows, limit)
	// With --app-only the view counts what application frames carry: a
	// thread that never enters application code has no row and no share.
	counted := "in the window"
	if f.appOnly {
		counted = "on application frames"
	}
	note := fmt.Sprintf("%d of %d functions, by %s %s samples (%d %s samples %s)",
		len(rows), len(view.Rows), sortKey, state, view.Samples, state, counted)
	var warnings, hints []string
	if len(rows) < len(view.Rows) {
		warnings = append(warnings, fmt.Sprintf("showing %d of %d functions, by %s %s samples; --limit 0 lists every function", len(rows), len(view.Rows), sortKey, state))
	}
	if len(rows) > 0 {
		hints = append(hints, fmt.Sprintf("--focus '%s' shows its callers and callees", rows[0].Function))
	}
	return printProfilingRows(ctx, run, "functions", string(state), &view.Samples, rows, len(view.Rows), note, warnings, hints)
}

// profilingFocusCandidates caps the functions an ambiguous --focus lists.
const profilingFocusCandidates = 8

// renderProfilingFocus prints one function's callers and callees.
func renderProfilingFocus(ctx context.Context, run *profilingRun, h *profiling.Hotspots, f *profilingFlags, state profiling.State) error {
	view, err := h.Focus(f.appOnly, state, f.focus)
	var noMatch *profiling.NoMatchError
	var ambiguous *profiling.AmbiguousMatchError
	switch {
	case errors.As(err, &noMatch):
		return profilingFailed("no function in the profile matches %q — list them with --view functions", noMatch.Pattern)
	case errors.As(err, &ambiguous):
		shown := ambiguous.Candidates
		if len(shown) > profilingFocusCandidates {
			shown = shown[:profilingFocusCandidates]
		}
		return profilingFailed("%q matches %d functions; pass one exactly:\n  %s", ambiguous.Pattern, len(ambiguous.Candidates), strings.Join(shown, "\n  "))
	case err != nil:
		return err
	}
	if view.Empty != "" {
		return printProfilingEmpty(ctx, run, "focus", string(state), []profiling.FocusRow{}, view.Empty)
	}
	limit := profilingLimit(ctx, f, profilingFocusDefaultLimit)
	nCallers, nCallees := len(view.Callers), len(view.Callees)
	note := fmt.Sprintf("%s: %d callers, %d callees, shares of its total %s samples", view.Function, nCallers, nCallees, state)
	var warnings []string
	if limit > 0 && (nCallers > limit || nCallees > limit) {
		warnings = append(warnings, fmt.Sprintf("showing up to %d callers and %d callees; --limit 0 lists them all", limit, limit))
	}
	hints := []string{note, "--focus <caller or callee> walks the call graph further"}
	// The total counts every row before --limit: the total and self rows plus
	// all callers and callees, so has_more agrees with it.
	return printProfilingRows(ctx, run, "focus", string(state), nil, view.Rows(limit), 2+nCallers+nCallees, "", warnings, hints)
}

// printProfilingRows prints a per-function view's rows. A table gets the note,
// warnings and hints as trailing # lines; the agent envelope carries the
// warnings (truncation) as context.warnings and the hints as suggestions;
// structured formats, -o folded and --plain get the rows alone.
func printProfilingRows(ctx context.Context, run *profilingRun, view, state string, samples *int, rows interface{}, total int, note string, warnings, hints []string) error {
	p := newProfilingPrinter(ctx)
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
	if agentMode(ctx) || jqFilter(ctx) != "" || !isProfilingTableFormat(ctx) {
		return nil
	}
	w := currentStdout(ctx)
	if note != "" {
		fmt.Fprintf(w, "# %s\n", note)
	}
	for _, h := range append(warnings, hints...) {
		fmt.Fprintf(w, "# %s\n", h)
	}
	return nil
}

// printProfilingEmpty prints a view that has no rows, with a note saying why.
// A table gets the note in place of the rows; the agent envelope gets an empty
// result with the note in context.warnings; structured formats and --plain get
// the empty list on stdout and the note on stderr.
func printProfilingEmpty(ctx context.Context, run *profilingRun, view, state string, empty interface{}, reason profiling.EmptyReason) error {
	note := profilingEmptyNote(reason, run, state)
	if agentMode(ctx) {
		p := newProfilingPrinter(ctx)
		if ap := enrichAgent(p, "exec", "profiling"); ap != nil {
			run.annotate(ap, view, state)
			ap.SetTotal(0)
			ap.SetWarnings([]string{note})
		}
		return p.PrintList(empty)
	}
	if isProfilingTextFormat(ctx) && jqFilter(ctx) == "" {
		_, err := fmt.Fprintf(currentStdout(ctx), "# %s\n", note)
		return err
	}
	fmt.Fprintf(currentStderr(ctx), "# %s\n", note)
	return newProfilingPrinter(ctx).PrintList(empty)
}

// profilingEmptyNote says why a view has no rows and what to try, in one line.
// state is the thread state a per-function view counted.
func profilingEmptyNote(reason profiling.EmptyReason, run *profilingRun, state string) string {
	switch reason {
	case profiling.EmptyNoSamples:
		return fmt.Sprintf("no stacks for %s: no profiling samples in the window (status=%s) — the process may be idle, restarted, or not under load; try another window (at most %s), or --raw for the API's whole result",
			run.entity, run.status, profilingMaxSpan)
	case profiling.EmptyState:
		return fmt.Sprintf("no %s samples for %s in the window — the stack view (without --view/--focus) shows every thread state; waiting states may need --show-waiting", state, run.entity)
	case profiling.EmptyAppOnly:
		if state != "" {
			// A per-function view counts one state. Application frames may
			// well have samples in another, so this says nothing about how
			// the profiler classified the frames.
			return fmt.Sprintf("no %s samples for %s on application frames: --app-only removed the frames that have them — re-run without --app-only", state, run.entity)
		}
		return fmt.Sprintf("no stacks for %s: --app-only removed every frame (the profiler classified none as application code) — re-run without --app-only", run.entity)
	}
	// A reason this command has no words for yet: show it rather than pass it
	// off as one of the others.
	return fmt.Sprintf("no rows for %s (%s)", run.entity, reason)
}

// profilingAPIError adds profiling-specific advice to the failures a caller
// can act on. Everything else passes through unchanged and gets dtctl's generic
// handling, as do the failures whose own text already says what happened: a
// request the SDK refused and a server error. An analysis that did not finish
// keeps its text too.
func profilingAPIError(err error) error {
	f, ok := profiling.ClassifyError(err)
	if !ok {
		return err
	}
	var suggestions []string
	switch f.Reason {
	case profiling.FailureBadRequest:
		suggestions = []string{
			fmt.Sprintf("The API rejected the request parameters — check the timeframe (in the past, at most %s) and the entity id", profilingMaxSpan),
		}
	case profiling.FailureForbidden:
		suggestions = []string{
			"Profiling needs storage:profiles:read and storage:entities:read, both on the token and in the IAM policy",
			"OAuth (dtctl auth login): log in again so the session requests storage:profiles:read",
		}
	case profiling.FailureNotFound:
		suggestions = []string{
			"The environment knows no such process (ids are per environment, and short-lived process instances disappear)",
			"List eligible ids: " + profilingFindIDsQuery,
		}
	case profiling.FailureTaskExpired:
		suggestions = []string{
			"The analysis was started, but the environment no longer knows its task (it expired, or it ran on a node that is gone) — run the command again",
		}
	case profiling.FailureThrottled:
		suggestions = []string{
			"The environment is throttling profiling requests (it limits how many analyses run at once) — wait and retry, and avoid running many analyses in parallel",
		}
	case profiling.FailureTimeout:
		// Its own text says what happened. It quotes the task's token, which
		// the environment chose, so the code is fixed here.
		return &profilingError{code: "error", err: err}
	case profiling.FailureInvalidInput, profiling.FailureServer:
		// Their own text says what happened.
		return err
	default:
		return err
	}
	return &diagnostic.Error{
		Operation:   "run profiling analysis",
		StatusCode:  f.StatusCode,
		Message:     f.Message,
		Suggestions: suggestions,
		Err:         err,
	}
}
