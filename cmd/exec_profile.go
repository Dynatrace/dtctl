package cmd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/Dynatrace/dtctl/pkg/diagnostic"
	"github.com/Dynatrace/dtctl/pkg/output"
	"github.com/Dynatrace/dtctl/pkg/resources/profile"
	"github.com/Dynatrace/dtctl/pkg/safety"
	"github.com/Dynatrace/dtctl/pkg/stability"
	"github.com/Dynatrace/dtctl/sdk/httpclient"
)

// profilingSince is the release in which `exec profiling` entered the
// experimental tier. One constant for the whole family.
const profilingSince = "0.47.0"

const (
	// profilingDefaultWindowMs is the window used when no start is given.
	profilingDefaultWindowMs = 2 * 60 * 60 * 1000
	// profilingMaxWindowMs caps the window length. The API itself accepts up to
	// 24h, but a wider window costs the server proportionally more, and 2h is
	// enough to localize a hotspot; how far back the window lies is not limited.
	profilingMaxWindowMs = 2 * 60 * 60 * 1000
)

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
// kind is a subcommand (hotspots/memory) so each declares only the flags it
// accepts and cobra rejects nonsensical combinations; the entity is a
// positional argument.
func newExecProfilingCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "profiling",
		Short: "Run a code-level profiling analysis on a process",
		Long: `Run a code-level profiling analysis via the Dynatrace code-level analysis API.

` + profilingExperimentalNotice + `

The analysis is asynchronous server-side; the command polls until it completes.
Output is collapsed (folded) stacks — one line per unique stack, carrying all
five thread-state counts so a single read shows whether a stack is CPU-bound or
blocked (this is not single-metric folded, so it does not feed flamegraph tools
directly):
  frame1;frame2;...;frameN running=16 lock=0 net_io=0 disk_io=0 wait=0
Each frame's own (self) samples are attributed to its stack, so the counts
reconcile with the analysis totals. --limit caps the number of rows; it never
changes the shape. Pass -o json for the compacted structured result.

Which function uses the most CPU? A function reached through many call paths is
split over many stack rows, so don't sum rows yourself; hotspots does it:
  hotspots <entity> --view functions   one row per function: self and total
                                       samples summed over every stack
  hotspots <entity> --focus <function> that function's callers and callees
(--state lock|offcpu|... counts other thread states; see hotspots --help.)

Kinds:
  hotspots  Method CPU hotspots (per-stack thread-state counts include lock/IO/wait)
  memory    Memory allocation analysis (allocation pressure, not live heap)

Entities: pass a process id, PROCESS-xxx. SERVICE ids are not accepted. The API
speaks the classic entity vocabulary, so dtctl rewrites PROCESS-xxx to its
PROCESS_GROUP_INSTANCE-xxx form (same entity, same id suffix). Classic ids work
as well: PROCESS_GROUP_INSTANCE-xxx, and PROCESS_GROUP-xxx for a whole process
group.

Find process ids:
  ` + profilingFindIDsHint + `

Timeframe: --default-timeframe-start/--default-timeframe-end (ISO-8601/RFC3339,
epoch millis, or now()-relative). Defaults to the last 2h. The window must not
exceed 2h, but it can lie anywhere in the past.

Required token scopes: storage:entities:read, storage:profiles:read.`,
		RunE: requireSubcommand,
	}
	stability.Mark(c, stability.Experimental, profilingSince)
	c.AddCommand(newProfilingHotspotsCmd(), newProfilingMemoryCmd())
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
	leafType      string
	showWaiting   bool
	survivorsOnly bool
	view          string // hotspots: stacks (default) or functions
	focus         string // hotspots: the function whose callers and callees to show
	state         string // hotspots functions/focus: which thread states to count
	sortBy        string // hotspots functions: self or total
}

// Default row caps of the per-function views when --limit is not given: the
// functions table is read top-down and its tail is noise, and the focus view
// caps its callers and its callees separately.
const (
	profilingFunctionsDefaultLimit = 30
	profilingFocusDefaultLimit     = 10
)

const profilingViewsHelp = `
Views. The default prints one row per unique call stack. A function reached
through many paths is then split over many rows, so it can be among the hottest
without any single row showing it. Two views sum the stacks per function:
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
  # an earlier 2h window
  dtctl exec profiling hotspots PROCESS-ABC123 \
    --default-timeframe-start 'now()-21h' --default-timeframe-end 'now()-19h'
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
	c.Flags().StringVar(&f.leafType, "leaf-type", profile.LeafTypeTotal, "CPU sample scope: total, service, or background")
	c.Flags().BoolVar(&f.showWaiting, "show-waiting", false, "include waiting (non-running) samples")
	c.Flags().StringVar(&f.view, "view", "stacks", "stacks (one row per call stack) or functions (one row per function, self and total)")
	c.Flags().StringVar(&f.focus, "focus", "", "show this function's callers and callees (an exact frame, or a substring of exactly one)")
	c.Flags().StringVar(&f.state, "state", "running", "thread states --view functions/--focus count: running, lock, net_io, disk_io, wait, offcpu, or all")
	c.Flags().StringVar(&f.sortBy, "sort", "self", "--view functions order: self or total")
	stability.Mark(c, stability.Experimental, profilingSince)
	return c
}

func newProfilingMemoryCmd() *cobra.Command {
	f := &profilingFlags{}
	c := &cobra.Command{
		Use:   "memory <entity>",
		Short: "Memory allocation analysis for a process",
		Long: `Memory allocation analysis for a process (PROCESS-xxx).

` + profilingExperimentalNotice + `

Shows where allocations happen and what survived GC within the window —
allocation pressure, not live retained heap. It does not prove a leak or predict
OOM; confirm that with a heap/RSS trend and pod restart/OOM state. The leak tell
is survived/allocated: a frame whose survivors far exceed its in-window
allocations is retaining, not churning (the biggest allocator is usually churn).
NOTE the memory tree is INVERTED vs hotspots: the allocation-site method is at
the root and its callers hang below as children.`,
		Example: `  dtctl exec profiling memory PROCESS-DEF456
  dtctl exec profiling memory PROCESS-DEF456 --survivors-only`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runProfiling(cmd, "memory", args[0], f)
		},
	}
	addProfilingCommonFlags(c, f)
	c.Flags().BoolVar(&f.survivorsOnly, "survivors-only", false, "restrict to surviving allocations")
	stability.Mark(c, stability.Experimental, profilingSince)
	return c
}

func addProfilingCommonFlags(c *cobra.Command, f *profilingFlags) {
	fl := c.Flags()
	fl.StringVar(&f.start, "default-timeframe-start", "", "window start — ISO-8601/RFC3339, epoch millis, or now()-<dur> (default: now()-2h)")
	fl.StringVar(&f.end, "default-timeframe-end", "", "window end — ISO-8601/RFC3339, epoch millis, or now() (default: now(); window max 2h)")
	fl.IntVar(&f.limit, "limit", 100, "cap output to the top N stacks: hotspots by running samples, memory by allocated bytes (0 = all)")
	fl.BoolVar(&f.appOnly, "app-only", false, "show only application frames, stripping runtime/library frames (JRE, Spring, npm packages, node:) and OneAgent frames")
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
	case "", profile.LeafTypeTotal, profile.LeafTypeService, profile.LeafTypeBackground:
	default:
		return fmt.Errorf("invalid --leaf-type %q: use total, service, or background", f.leafType)
	}
	if err := validateProfilingView(f); err != nil {
		return err
	}
	from, to, err := resolveProfilingTimeframe(f.start, f.end)
	if err != nil {
		return err
	}

	// Code-level analysis only reads profiling data — gate on OperationRead.
	_, c, err := setupWithSafety(ctx, safety.OperationRead)
	if err != nil {
		return err
	}

	apiKind := profile.Kind[kind]
	stopSpinner := startSpinner(ctx, fmt.Sprintf("running %s analysis", kind))
	resp, err := profile.NewHandler(c).Run(cmd.Context(), profile.Payload{
		Kind:          apiKind,
		EntityID:      entityID,
		From:          from,
		To:            to,
		LeafType:      f.leafType,
		ShowWaiting:   f.showWaiting,
		SurvivorsOnly: f.survivorsOnly,
	})
	stopSpinner()
	if err != nil {
		return profilingAPIError(err)
	}

	// Round-trip through JSON so the renderers and --jq see the plain envelope
	// shape: {"status":..., "kind":..., "entityId":..., "result":{...}}.
	b, err := json.Marshal(resp)
	if err != nil {
		return err
	}
	var result interface{}
	if err := json.Unmarshal(b, &result); err != nil {
		return err
	}
	result = compactResult(result)
	if f.focus != "" {
		return renderProfilingFocus(ctx, result, f)
	}
	if f.view == "functions" {
		return renderProfilingFunctions(ctx, result, f)
	}
	return renderProfiling(ctx, apiKind, result, f.limit, f.appOnly)
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
	if f.state != "" && !profile.ValidState(f.state) {
		return fmt.Errorf("invalid --state %q: use %s", f.state, strings.Join(profile.States, ", "))
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

// renderProfilingFunctions prints the functions view: rows through the standard
// printer, so a table, -o json/csv/yaml, --jq and the agent envelope all work.
func renderProfilingFunctions(ctx context.Context, result interface{}, f *profilingFlags) error {
	t := profile.HotspotFunctions(result, f.appOnly, f.state, f.sortBy)
	if t == nil {
		return printProfilingNote(ctx, noProfilingFunctionsNote(result, f))
	}
	limit := f.limit
	if !f.limitSet {
		limit = profilingFunctionsDefaultLimit
	}
	rows := t.Rows
	if limit > 0 && len(rows) > limit {
		rows = rows[:limit]
	}
	sortKey := f.sortBy
	if sortKey == "" {
		sortKey = "self"
	}
	note := fmt.Sprintf("%d of %d functions, by %s %s samples (%d %s samples in the window)",
		len(rows), len(t.Rows), sortKey, f.state, t.Samples, f.state)
	var hints []string
	if len(rows) < len(t.Rows) {
		hints = append(hints, "--limit 0 lists every function")
	}
	if len(rows) > 0 {
		hints = append(hints, fmt.Sprintf("--focus '%s' shows its callers and callees", rows[0].Function))
	}
	return printProfilingRows(ctx, rows, len(t.Rows), note, hints)
}

// renderProfilingFocus prints one function's callers and callees.
func renderProfilingFocus(ctx context.Context, result interface{}, f *profilingFlags) error {
	limit := f.limit
	if !f.limitSet {
		limit = profilingFocusDefaultLimit
	}
	rows, fn, nCallers, nCallees, err := profile.HotspotFocus(result, f.appOnly, f.state, f.focus, limit)
	if err != nil {
		if profile.HotspotFunctions(result, f.appOnly, f.state, "self") == nil {
			return printProfilingNote(ctx, noProfilingFunctionsNote(result, f))
		}
		return err
	}
	note := fmt.Sprintf("%s: %d callers, %d callees, shares of its total %s samples", fn, nCallers, nCallees, f.state)
	var hints []string
	if limit > 0 && (nCallers > limit || nCallees > limit) {
		hints = append(hints, fmt.Sprintf("showing up to %d callers and %d callees; --limit 0 lists them all", limit, limit))
	}
	hints = append(hints, "--focus <caller or callee> walks the call graph further")
	return printProfilingRows(ctx, rows, len(rows), note, hints)
}

// printProfilingRows prints a per-function view's rows. A table gets the note
// and hints as trailing # lines; the agent envelope carries them as
// suggestions; structured formats get the rows alone.
func printProfilingRows(ctx context.Context, rows interface{}, total int, note string, hints []string) error {
	p := newPrinterCtx(ctx)
	if ap := enrichAgent(p, "exec", "profiling"); ap != nil {
		ap.SetTotal(total)
		ap.SetSuggestions(append([]string{note}, hints...))
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
		fmt.Fprintf(w, "# %s\n", note)
		for _, h := range hints {
			fmt.Fprintf(w, "# %s\n", h)
		}
	}
	return nil
}

// printProfilingNote prints a one-line explanation in place of a view's output,
// inside the envelope in agent mode.
func printProfilingNote(ctx context.Context, note string) error {
	if agentMode(ctx) {
		p := newPrinterCtx(ctx)
		enrichAgent(p, "exec", "profiling")
		return p.Print(note)
	}
	_, err := fmt.Fprintln(currentStdout(ctx), note)
	return err
}

// noProfilingFunctionsNote explains an empty per-function view. Running samples
// absent means what an empty stack view means; another --state may simply have
// none in the window.
func noProfilingFunctionsNote(result interface{}, f *profilingFlags) string {
	if f.state == "" || f.state == "running" {
		return noProfilingStacksNote("methodHotspots", result, f.appOnly)
	}
	entity := ""
	if m, ok := result.(map[string]interface{}); ok {
		entity, _ = m["entityId"].(string)
	}
	return fmt.Sprintf("# no %s samples for %s in the window — the stack view (without --view/--focus) shows every thread state; waiting states may need --show-waiting", f.state, entity)
}

// profilingAPIError adds profiling-specific advice to the API failures a
// caller can act on. Everything else passes through unchanged and gets dtctl's
// generic handling.
func profilingAPIError(err error) error {
	var apiErr *httpclient.APIError
	if !errors.As(err, &apiErr) {
		return err
	}
	var suggestions []string
	switch apiErr.StatusCode {
	case 400:
		suggestions = []string{
			"The API rejected the request parameters — check the timeframe (in the past, at most 2h) and the entity id",
		}
	case 403:
		suggestions = []string{
			"Profiling needs storage:profiles:read and storage:entities:read, both on the token and in the IAM policy",
			"OAuth (dtctl auth login): log in again so the session requests storage:profiles:read",
		}
	case 404:
		suggestions = []string{
			"The environment knows no such process (ids are per environment, and short-lived process instances disappear)",
			"List eligible ids: " + profilingFindIDsQuery,
		}
	case 429:
		suggestions = []string{
			"The environment limits concurrent profiling analyses and is at that limit — wait and retry, and avoid running many analyses in parallel",
		}
	default:
		return err
	}
	return &diagnostic.Error{
		Operation:   "run profiling analysis",
		StatusCode:  apiErr.StatusCode,
		Message:     apiErr.Message,
		Suggestions: suggestions,
		Err:         err,
	}
}

// renderProfiling routes output through the standard printer so --jq and the
// agent envelope compose. Folded stacks are the default; a structured -o
// (json/yaml/toon/…) or --jq prints the compacted result instead. In agent mode
// the folded text is carried inside the envelope rather than written raw.
func renderProfiling(ctx context.Context, apiKind string, result interface{}, limit int, appOnly bool) error {
	printValue := func(v interface{}) error {
		p := newPrinterCtx(ctx)
		if ap := enrichAgent(p, "exec", "profiling"); ap != nil && apiKind == profile.Kind["hotspots"] {
			if _, folded := v.(string); folded {
				ap.SetSuggestions([]string{"one row per call stack: to rank functions, summed over every stack they appear in, use --view functions; for one function's callers, --focus <function>"})
			}
		}
		return p.Print(v)
	}
	switch outputFormat(ctx) {
	case "json", "yaml", "yml", "toon", "csv", "jsonl", "parquet":
		return printValue(result)
	}
	if jqFilter(ctx) != "" {
		return printValue(result)
	}

	// table (the -o default), wide, folded, or unset -> folded stacks
	s := profile.ToCollapsed(apiKind, result, limit, appOnly)
	if s == "" {
		// No folded output. Say why in one line rather than dumping the raw
		// result (hundreds of lines of render metadata with nothing in it);
		// -o json still prints the full result.
		s = noProfilingStacksNote(apiKind, result, appOnly) + "\n"
	}
	if agentMode(ctx) {
		return printValue(s)
	}
	_, err := fmt.Fprint(currentStdout(ctx), s)
	return err
}

// noProfilingStacksNote explains an empty folded result: either the window
// holds no samples for the entity at all, or --app-only removed every frame.
func noProfilingStacksNote(apiKind string, result interface{}, appOnly bool) string {
	entity, status := "", ""
	if m, ok := result.(map[string]interface{}); ok {
		entity, _ = m["entityId"].(string)
		status, _ = m["status"].(string)
	}
	if appOnly && profile.ToCollapsed(apiKind, result, 1, false) != "" {
		return fmt.Sprintf("# no stacks for %s: --app-only removed every frame (the profiler classified none as application code) — re-run without --app-only", entity)
	}
	return fmt.Sprintf("# no stacks for %s: no profiling samples in the window (status=%s) — the process may be idle, restarted, or not under load; try another window (at most 2h), or -o json for the raw result", entity, status)
}

// resolveProfilingTimeframe turns the --default-timeframe-start/-end values
// into an epoch-millis window. Both accept ISO-8601/RFC3339, epoch millis, or a
// now()-relative expression; an empty end defaults to now and an empty start to
// two hours before the end. The window must start in the past, be positive and
// be at most profilingMaxWindowMs, checked here so an oversized window never
// reaches the API.
func resolveProfilingTimeframe(startStr, endStr string) (int64, int64, error) {
	nowMs := time.Now().UnixMilli()

	to := nowMs
	if endStr != "" {
		var err error
		if to, err = parseProfilingTime(endStr, nowMs); err != nil {
			return 0, 0, fmt.Errorf("invalid --default-timeframe-end: %w", err)
		}
	}

	from := to - profilingDefaultWindowMs
	if startStr != "" {
		var err error
		if from, err = parseProfilingTime(startStr, nowMs); err != nil {
			return 0, 0, fmt.Errorf("invalid --default-timeframe-start: %w", err)
		}
	}

	if from >= nowMs {
		return 0, 0, fmt.Errorf("invalid timeframe: the start is in the future — profiling only covers the past (e.g. --default-timeframe-start 'now()-2h')")
	}
	if to <= from {
		return 0, 0, fmt.Errorf("invalid timeframe: the end must be after the start")
	}
	if to-from > profilingMaxWindowMs {
		return 0, 0, fmt.Errorf("invalid timeframe: the window must not exceed 2h (got %s) — narrow it; the window can lie anywhere in the past", time.Duration(to-from)*time.Millisecond)
	}
	return from, to, nil
}

// parseProfilingTime parses one timeframe bound: a now()-relative expression
// (now(), now()-90min, now()+30min), epoch millis, or RFC3339.
func parseProfilingTime(s string, nowMs int64) (int64, error) {
	s = strings.TrimSpace(s)
	if rest, ok := strings.CutPrefix(s, "now()"); ok {
		rest = strings.TrimSpace(rest)
		if rest == "" {
			return nowMs, nil
		}
		var sign int64
		switch rest[0] {
		case '-':
			sign = -1
		case '+':
			sign = 1
		default:
			return 0, fmt.Errorf("expected now()-<duration> or now()+<duration>, got %q", s)
		}
		durStr := strings.ReplaceAll(strings.TrimSpace(rest[1:]), "min", "m")
		d, err := time.ParseDuration(durStr)
		if err != nil {
			return 0, fmt.Errorf("invalid duration in %q: %w", s, err)
		}
		return nowMs + sign*d.Milliseconds(), nil
	}
	return parseProfileTimestamp(s)
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

func parseProfileTimestamp(s string) (int64, error) {
	if ms, err := strconv.ParseInt(s, 10, 64); err == nil {
		return ms, nil
	}
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return 0, fmt.Errorf("cannot parse time %q: use RFC3339, epoch millis, or now()-<dur>", s)
	}
	return t.UnixMilli(), nil
}

// startSpinner writes an animated spinner to stderr while work is in progress.
// Call the returned function to stop it and clear the line.
// Skipped in plain/agent mode (non-TTY) to keep output machine-readable.
func startSpinner(ctx context.Context, label string) func() {
	if agentMode(ctx) || plainMode(ctx) || !output.ColorEnabled() {
		return func() {}
	}
	stderr := currentStderr(ctx)
	frames := []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}
	done := make(chan struct{})
	stopped := make(chan struct{})
	go func() {
		defer close(stopped)
		ticker := time.NewTicker(100 * time.Millisecond)
		defer ticker.Stop()
		i := 0
		for {
			select {
			case <-done:
				fmt.Fprintf(stderr, "\r\033[K") // clear line
				return
			case <-ticker.C:
				fmt.Fprintf(stderr, "\r%s %s", frames[i%len(frames)], label)
				i++
			}
		}
	}()
	// Wait for the goroutine to clear the line, so nothing it writes can land
	// after the command's own output.
	return func() { close(done); <-stopped }
}

// visualOnlyKeys are fields that carry UI rendering metadata with no analytical value.
var visualOnlyKeys = map[string]bool{
	"color": true, // hex colour for charts
	"uuid":  true, // internal render ID
}

// emptyOnlyKeys are dropped only when they carry nothing. fileName/filePath are
// null on Java frames, but on Node.js they are the only place a frame's file
// lives (its className is empty), and the folded renderer labels frames from them.
var emptyOnlyKeys = map[string]bool{
	"fileName": true,
	"filePath": true,
}

// compactResult strips timeseries dataPoints and visual-only fields so agents
// receive signal without chart/render noise.
func compactResult(v interface{}) interface{} {
	switch val := v.(type) {
	case map[string]interface{}:
		out := make(map[string]interface{}, len(val))
		for k, child := range val {
			if visualOnlyKeys[k] {
				continue
			}
			if emptyOnlyKeys[k] && (child == nil || child == "") {
				continue
			}
			if k == "dataPoints" {
				if arr, ok := child.([]interface{}); ok {
					out["dataPointsCount"] = len(arr)
					continue
				}
			}
			out[k] = compactResult(child)
		}
		return out
	case []interface{}:
		out := make([]interface{}, len(val))
		for i, item := range val {
			out[i] = compactResult(item)
		}
		return out
	}
	return v
}
