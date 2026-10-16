package cmd

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/dynatrace-oss/dtctl/pkg/inspect"
	"github.com/dynatrace-oss/dtctl/pkg/output"
	"github.com/dynatrace-oss/dtctl/pkg/stability"
)

// statsAllColumns is the NoOptDefVal for --stats: a bare `--stats` (no value)
// profiles every column, while `--stats a,b` selects columns. It is a sentinel
// that cannot collide with a real column name.
const statsAllColumns = "\x00all"

var inspectCmd = newInspectCmd()

func newInspectCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "inspect <file>",
		Short: "Inspect a spilled query-result file locally (row access, schema, stats)",
		Long: `Inspect a query-result file that 'dtctl query' spilled to disk, without
re-querying Grail and without pulling the whole result back into context.

The expensive Grail scan happens once (the original query). 'inspect' reads only
what each call needs from the file and emits only the answer — so a paused agent
session, a large export, or a sandbox with no shell tooling can still interrogate
the rows.

Its primary capability is ROW ACCESS — specific rows, ranges, the tail, a column
projection — which the spill summary never carried:

  --head N                 first N rows
  --tail N                 last N rows
  --page --offset O --limit L   a paginated window (row order = result order)
  --fields col1,col2,…     project columns (composable with the row-access flags)

It can also re-derive the summary for a file whose manifest is no longer in
context (a stale spill, a file handed between sessions):

  --schema                 columns + types + null counts
  --stats [col,col]        per-column profile (counts, null%, min/max, mean, top-K)
  --sample N               N representative (leading) rows

For "give me only the matching rows", a --jq program runs as a streaming filter
over the WHOLE file (per record, like jq over an NDJSON file) — not a dtctl
predicate language, just jq as an opt-in post-processor:

  --jq '<program>'         keep the objects the program emits, scanning every row

The program is run on each record and only the objects it emits are collected,
so 'select(.status == 500)' keeps matching rows and '{host, timestamp}' reshapes
them. A row-access window bounds the result ('--jq … --head 20' = the first 20
matches), and a bare '--jq …' over a large match set re-spills to a new file via
the same --spill* guard as row access, so it never floods context. (The program
must emit objects; for free-form scalar extraction, run jq on the file yourself.)

And when the file handle itself is gone — the original spill envelope was
trimmed or summarised out of context, but the file is still on disk — it can
enumerate the spilled files in the active context and their provenance, so you
can recover a handle instead of re-querying Grail:

  --list                   list spilled files in the active context (query, rows, age)

Choose exactly one primitive per call (--jq may add a bounding window and
--fields). 'inspect' is not a query engine: there is no SQL, no GROUP BY, and no
dtctl predicate language. For aggregate questions, push the work back into DQL
and re-query ('… | summarize …'); for complex local analysis, hand the file to
your preferred local analytics tooling.`,
		Example: `  # First 20 rows of a spilled result
  dtctl inspect ~/.cache/dtctl/results/prod/q-7f3a9c.jsonl --head 20

  # A projected window deep in the result
  dtctl inspect q-7f3a9c.jsonl --page --offset 1000 --limit 50 --fields timestamp,content

  # The tail of the result
  dtctl inspect q-7f3a9c.jsonl --tail 10

  # Keep only the matching rows (full-file streaming jq filter)
  dtctl inspect q-7f3a9c.jsonl --jq 'select(.status == 500)'

  # The first 20 matches, projected to two columns
  dtctl inspect q-7f3a9c.jsonl --jq 'select(.status == 500)' --head 20 --fields host,timestamp

  # Re-derive the per-column profile for an out-of-context file
  dtctl inspect q-7f3a9c.jsonl --stats

  # Just the schema, as an agent envelope
  dtctl inspect q-7f3a9c.jsonl --schema --agent

  # Recover a lost handle: what has been spilled in this context?
  dtctl inspect --list`,
		// At most one positional <file>. --list takes none; every primitive takes one.
		// The exact requirement is enforced in RunE so usage errors share the typed
		// inspect_bad_flags envelope rather than cobra's generic arg error.
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			// --list is a directory-scoped enumeration, not a file primitive: it takes
			// no <file> and stands alone.
			if cmd.Flags().Changed("list") {
				return runInspectList(cmd, args)
			}

			if len(args) != 1 {
				return inspect.BadFlags(
					"inspect requires exactly one <file> argument",
					"pass a spilled file (e.g. dtctl inspect q-7f3a9c.jsonl --head 20), or use --list to enumerate spilled files",
				)
			}

			req, err := buildInspectRequest(cmd, args[0])
			if err != nil {
				return err
			}

			// Local-only command: no client, no auth. Load config best-effort purely
			// to learn the active context/tenant for the cross-context refusal (D9) and
			// to resolve re-spill settings (IN8). A missing/unusable config is fine.
			cfg, _ := loadConfig(cmdContext(cmd))
			if cfg != nil {
				req.ActiveTenant, req.ActiveContext = spillProvenance(cfg)
			}

			res, err := inspect.Run(req)
			if err != nil {
				return err // typed *inspect.Error → structured envelope via errorToDetail
			}

			return emitInspectResult(cmd, cfg, req, res)
		},
	}
	stability.MarkStable(c)
	addInspectFlags(c)
	return c
}

// buildInspectRequest parses and validates the inspect flag surface into a
// fully-formed engine request, enforcing exactly-one-primitive and the
// --offset/--limit-requires-page rule (IN9 inspect_bad_flags).
func buildInspectRequest(cmd *cobra.Command, path string) (inspect.Request, error) {
	f := cmd.Flags()
	var selected []string
	add := func(name string) bool {
		if f.Changed(name) {
			selected = append(selected, name)
			return true
		}
		return false
	}

	hasSchema := add("schema")
	hasStats := add("stats")
	hasSample := add("sample")
	hasHead := add("head")
	hasTail := add("tail")
	hasPage := add("page")

	fieldsRaw, _ := f.GetString("fields")
	fields := splitCSVList(fieldsRaw)

	offsetChanged := f.Changed("offset")
	limitChanged := f.Changed("limit")

	// A --jq program turns the call into a full-file streaming filter (PrimFilter):
	// every record is run through jq and the emitted objects are collected,
	// re-spill-guarded. The row-access window (--head/--tail/--page) then *bounds*
	// that output rather than selecting a primitive of its own, so it is handled
	// here before the one-primitive-per-call check below (which counts windows as
	// primitives). It deliberately does not introduce a dtctl predicate language —
	// jq is a general post-processor the caller opts into (D16).
	if jqFilter(cmdContext(cmd)) != "" {
		return buildInspectFilterRequest(path, fields, hasSchema, hasStats, hasSample,
			hasHead, hasTail, hasPage, offsetChanged, limitChanged, cmd)
	}

	if len(selected) > 1 {
		return inspect.Request{}, inspect.BadFlags(
			fmt.Sprintf("choose exactly one primitive, not %d (%s)", len(selected), strings.Join(dashify(selected), ", ")),
			"e.g. dtctl inspect "+path+" --head 20",
		)
	}
	if (offsetChanged || limitChanged) && !hasPage {
		return inspect.Request{}, inspect.BadFlags(
			"--offset/--limit require --page",
			"e.g. dtctl inspect "+path+" --page --offset 1000 --limit 50",
		)
	}

	req := inspect.Request{Path: path, Fields: fields}
	switch {
	case hasSchema:
		req.Primitive = inspect.PrimSchema
	case hasStats:
		req.Primitive = inspect.PrimStats
		statsVal, _ := f.GetString("stats")
		if statsVal != statsAllColumns {
			req.StatsColumns = splitCSVList(statsVal)
		}
	case hasSample:
		req.Primitive = inspect.PrimSample
		req.N, _ = f.GetInt("sample")
	case hasHead:
		req.Primitive = inspect.PrimHead
		req.N, _ = f.GetInt("head")
	case hasTail:
		req.Primitive = inspect.PrimTail
		req.N, _ = f.GetInt("tail")
	case hasPage:
		req.Primitive = inspect.PrimPage
		req.Offset, _ = f.GetInt("offset")
		// An unspecified --limit defaults to DefaultRowCount; an explicit value
		// (including 0) is honoured by the engine. The command layer owns
		// defaulting so `--limit 0` means an empty window, not the default.
		if limitChanged {
			req.Limit, _ = f.GetInt("limit")
		} else {
			req.Limit = inspect.DefaultRowCount
		}
	default:
		// No primitive chosen. A bare --fields defaults to --head (row access is
		// the point of the command); nothing at all is a usage error.
		if len(fields) == 0 {
			return inspect.Request{}, inspect.BadFlags(
				"no primitive selected",
				"choose one of --head, --tail, --page, --schema, --stats, --sample, or pass --fields to project the first rows",
			)
		}
		// Bare --fields is an unspecified head, so apply the default count here
		// (the engine honours an explicit count, including 0, verbatim).
		req.Primitive = inspect.PrimHead
		req.N = inspect.DefaultRowCount
	}
	return req, nil
}

// buildInspectFilterRequest builds a full-file streaming-filter request (--jq,
// PrimFilter). --jq selects rows; an optional row-access window bounds the
// output. It is mutually exclusive with the re-derivation primitives
// (--schema/--stats/--sample): those summarise the whole file, and "filter then
// summarise" is a two-step that belongs in DQL or a re-spill, not a single
// inspect call.
func buildInspectFilterRequest(
	path string, fields []string,
	hasSchema, hasStats, hasSample, hasHead, hasTail, hasPage bool,
	offsetChanged, limitChanged bool,
	cmd *cobra.Command,
) (inspect.Request, error) {
	f := cmd.Flags()

	if hasSchema || hasStats || hasSample {
		return inspect.Request{}, inspect.BadFlags(
			"--jq filters rows and cannot be combined with --schema/--stats/--sample",
			"filter first, then summarise the result — push the aggregate into DQL ('… | summarize …'), or re-spill the filtered rows and run --stats on that file",
		)
	}

	windows := 0
	for _, on := range []bool{hasHead, hasTail, hasPage} {
		if on {
			windows++
		}
	}
	if windows > 1 {
		return inspect.Request{}, inspect.BadFlags(
			"--jq accepts at most one bounding window (--head, --tail, or --page)",
			"e.g. dtctl inspect "+path+" --jq 'select(.status == 500)' --head 20",
		)
	}
	if (offsetChanged || limitChanged) && !hasPage {
		return inspect.Request{}, inspect.BadFlags(
			"--offset/--limit require --page",
			"e.g. dtctl inspect "+path+" --jq 'select(.status == 500)' --page --offset 1000 --limit 50",
		)
	}

	req := inspect.Request{Path: path, Fields: fields, Filter: jqFilter(cmdContext(cmd))}
	switch {
	case hasHead:
		req.Primitive = inspect.PrimHead
		req.N, _ = f.GetInt("head")
	case hasTail:
		req.Primitive = inspect.PrimTail
		req.N, _ = f.GetInt("tail")
	case hasPage:
		req.Primitive = inspect.PrimPage
		req.Offset, _ = f.GetInt("offset")
		if limitChanged {
			req.Limit, _ = f.GetInt("limit")
		} else {
			req.Limit = inspect.DefaultRowCount
		}
	default:
		// No window: the whole-file form. Its output is unbounded, so the re-spill
		// guard (maybeRespill / IN8) is what keeps it context-safe.
		req.Primitive = inspect.PrimFilter
	}
	return req, nil
}

func init() {
	rootCmd.AddCommand(inspectCmd)
}

// addInspectFlags registers the closed primitive set and the shared spill
// namespace on a command. Factored out so tests can build an equivalent command.
func addInspectFlags(cmd *cobra.Command) {
	cmd.Flags().Bool("schema", false, "re-derive the schema: columns + types + null counts")
	// --stats takes an OPTIONAL column list: bare `--stats` profiles every column,
	// `--stats=col,col` selects (the value must use '=', like --spill).
	cmd.Flags().String("stats", "", "re-derive per-column stats; bare profiles all columns, or --stats=col,col to select")
	cmd.Flags().Lookup("stats").NoOptDefVal = statsAllColumns
	cmd.Flags().Int("sample", 0, "re-emit N representative (leading) rows")
	cmd.Flags().Int("head", 0, "the first N rows")
	cmd.Flags().Int("tail", 0, "the last N rows")
	cmd.Flags().Bool("page", false, "a paginated row window (use with --offset/--limit)")
	cmd.Flags().Int("offset", 0, "row offset for --page")
	cmd.Flags().Int("limit", 0, fmt.Sprintf("row limit for --page (default %d)", inspect.DefaultRowCount))
	cmd.Flags().String("fields", "", "project to these columns (comma-separated); composable with row-access primitives")
	cmd.Flags().Bool("list", false, "list spilled files in the active context (recover a lost file handle); takes no <file>")

	// inspect honours the same --spill* namespace as query (IN8): an oversized
	// row window re-spills to a new managed file rather than becoming a context
	// hazard itself.
	addSpillFlags(cmd)
}

// splitCSVList splits a comma-separated flag value into trimmed, non-empty parts.
func splitCSVList(s string) []string {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if t := strings.TrimSpace(p); t != "" {
			out = append(out, t)
		}
	}
	return out
}

// dashify renders flag names with their leading -- for an error message.
func dashify(names []string) []string {
	out := make([]string, len(names))
	for i, n := range names {
		out[i] = "--" + n
	}
	return out
}

// inspectResourceFromSidecar best-effort extracts the fetched resource (e.g.
// "logs") from the original query recorded in the sidecar, for context.resource.
func inspectResourceFromSidecar(sc *output.SidecarManifest) string {
	if sc == nil {
		return ""
	}
	fields := strings.Fields(strings.ToLower(sc.Query))
	for i, f := range fields {
		if f == "fetch" && i+1 < len(fields) {
			return strings.TrimRight(fields[i+1], ",")
		}
	}
	return ""
}

// Declared stable: the invocation and output contract of this command is
// additive-only. Stable is never implied -- see AGENTS.md "Stability Tiers".
