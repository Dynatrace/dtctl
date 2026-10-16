package cmd

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"
	"golang.org/x/term"
	"gopkg.in/yaml.v3"

	"github.com/dynatrace-oss/dtctl/pkg/exec"
	"github.com/dynatrace-oss/dtctl/pkg/output"
	"github.com/dynatrace-oss/dtctl/pkg/resources/resolver"
	"github.com/dynatrace-oss/dtctl/pkg/stability"
	"github.com/dynatrace-oss/dtctl/pkg/util/template"
)

// isTerminal checks if the given file is a terminal
func isTerminal(f *os.File) bool {
	return term.IsTerminal(int(f.Fd()))
}

// writerIsTerminal reports whether w is a terminal. An invocation's stdout is
// often not a file at all, and then nobody is watching it.
func writerIsTerminal(w io.Writer) bool {
	f, ok := w.(*os.File)
	return ok && isTerminal(f)
}

// ANSI color codes for terminal output
const (
	colorReset  = "\033[0m"
	colorRed    = "\033[31m"
	colorGreen  = "\033[32m"
	colorYellow = "\033[33m"
	colorCyan   = "\033[36m"
)

// isStderrTerminal checks if stderr is a terminal (for color output)
func isStderrTerminal() bool {
	return term.IsTerminal(int(os.Stderr.Fd()))
}

// formatRequiresIncludeTypes reports whether the output format needs DQL column
// type metadata to render correctly, so the query layer can request it even when
// the user did not pass --include-types. Parquet derives its columnar schema from
// these types (a "long" must become an INT64 column, not a value-inferred DOUBLE).
func formatRequiresIncludeTypes(format string) bool {
	return strings.EqualFold(strings.TrimSpace(format), "parquet")
}

func isSupportedQueryOutputFormat(format string) bool {
	switch strings.ToLower(strings.TrimSpace(format)) {
	case "", "table", "wide", "json", "yaml", "yml", "csv", "jsonl", "parquet", "toon", "auto", "chart", "sparkline", "spark", "barchart", "bar", "braille", "br":
		return true
	default:
		return false
	}
}

// agentResultFormat returns the output format for `query` and whether it is the
// agent-mode default. In agent mode with no -o, `query` defaults to -o auto
// (the cheapest lossless encoding for the result's shape); any explicit -o
// wins, and outside agent mode the -o value is returned unchanged.
func agentResultFormat(ctx context.Context) (format string, byDefault bool) {
	if agentMode(ctx) && !outputFormatChanged(ctx) {
		return output.FormatAuto, true
	}
	return outputFormat(ctx), false
}

// compactSince is the release that introduced --compact (#578). It is
// experimental because it reshapes the rows a stable command returns.
const compactSince = "0.40.0"

// resolveQueryCompact decides whether query output is compacted (nulls omitted,
// single-value columns hoisted into a `constant` map). Agent mode is
// token-optimal by default, so it is on there and off otherwise; an explicit
// --compact / --compact=false always wins. It applies only where the output has
// a place for the constant map: the agent envelope (json/toon/auto, and the
// table layouts agent mode renders as json) and plain json/yaml/toon/auto. An explicit
// --compact on any other format returns a warning; the agent-mode default stays
// silent there.
func resolveQueryCompact(cmd *cobra.Command, agent bool, format string) (bool, string) {
	compact := agent
	explicit := cmd.Flags().Changed("compact")
	if explicit {
		compact, _ = cmd.Flags().GetBool("compact")
	}
	if !compact {
		return false, ""
	}
	switch strings.ToLower(strings.TrimSpace(format)) {
	case "json", "yaml", "yml", "toon", "auto":
		return true, ""
	case "", "table", "wide":
		if agent {
			return true, ""
		}
	}
	if explicit {
		return false, fmt.Sprintf("--compact is ignored with -o %s (it applies to json, yaml and toon output)", displayFormatName(format))
	}
	return false, ""
}

func displayFormatName(format string) string {
	if f := strings.TrimSpace(format); f != "" {
		return f
	}
	return "table"
}

// queryCmd represents the query command
var queryCmd = newQueryCmd()

func newQueryCmd() *cobra.Command {
	c := &cobra.Command{
		Use:     "query [dql-string]",
		Aliases: []string{"q"},
		Short:   "Execute a DQL query",
		Long: `Execute a DQL query against Grail storage.

DQL (Dynatrace Query Language) queries can be executed inline or from a file.
Template variables can be used with the --set flag for reusable queries.

Template Syntax:
  Use {{.variable}} to reference variables.
  Use {{.variable | default "value"}} for default values.

Examples:
  # Execute inline query
  dtctl query "fetch logs | limit 10"

  # Execute from file
  dtctl query -f query.dql

  # Read from stdin (avoids shell escaping issues)
  dtctl query -f - -o json <<'EOF'
  metrics | filter startsWith(metric.key, "dt") | limit 10
  EOF

  # PowerShell: pipe a here-string in -- as an argument, Windows PowerShell 5.1
  # strips the inner double quotes DQL needs (see docs/WINDOWS.md#quoting)
  @'
  fetch logs, bucket:{"custom-logs"} | filter contains(host.name, "api")
  '@ | dtctl query -o json

  # Pipe query from file
  cat query.dql | dtctl query -o json

  # Execute with template variables
  dtctl query -f query.dql --set host=h-123 --set timerange=1h

  # Output as JSON or CSV
  dtctl query "fetch logs" -o json
  dtctl query "fetch logs" -o csv

  # Output as JSON Lines (one JSON object per line) or Parquet for large exports
  dtctl query "fetch logs" -o jsonl
  dtctl query "fetch logs" --max-result-records 100000 -o parquet > logs.parquet

  # Download large datasets with custom limits
  dtctl query "fetch logs" --max-result-records 10000 -o csv > logs.csv

  # Query with specific timeframe
  dtctl query "fetch logs" --default-timeframe-start "2024-01-01T00:00:00Z" \
    --default-timeframe-end "2024-01-02T00:00:00Z" -o csv

  # Query with timezone and locale
  dtctl query "fetch logs" --timezone "Europe/Paris" --locale "fr_FR" -o json

  # Query with sampling for large datasets
  dtctl query "fetch logs" --default-sampling-ratio 10 --max-result-records 10000 -o csv

  # Display as chart with live updates (refresh every 10s)
  dtctl query "timeseries avg(dt.host.cpu.usage)" -o chart --live

  # Live mode with custom interval
  dtctl query "timeseries avg(dt.host.cpu.usage)" -o chart --live --interval 5s

  # Fullscreen chart (uses terminal dimensions)
  dtctl query "timeseries avg(dt.host.cpu.usage)" -o chart --fullscreen

  # Custom chart dimensions
  dtctl query "timeseries avg(dt.host.cpu.usage)" -o chart --width 150 --height 30

  # Compact timeseries: per-series summary + sparkline, or downsampling
  # that keeps each bucket's min and max
  dtctl query "timeseries avg(dt.host.cpu.usage), by:{host.name}" --series=summary
  dtctl query "timeseries avg(dt.host.cpu.usage)" --series=downsample:30 -o csv

  # Round numbers to 3 significant digits
  dtctl query "timeseries avg(dt.host.cpu.usage)" --precision 3 -o json

  # In agent mode --series=summary and --precision 4 are the defaults;
  # restore the raw datapoints with
  dtctl query "timeseries avg(dt.host.cpu.usage)" -A --series=full --precision 0

  # Include query metadata (execution time, scanned records, etc.)
  dtctl query "fetch logs | limit 10" --metadata
  dtctl query "fetch logs | limit 10" -M -o json

  # Include only selected metadata fields
  dtctl query "fetch logs | limit 10" --metadata=executionTimeMilliseconds,scannedRecords,scannedBytes

  # Include only the metadata worth acting on (cost, sampling, default window)
  dtctl query "fetch logs | limit 10" --metadata=minimal
  dtctl query "fetch logs | limit 10" -M=queryId,analysisTimeframe -o json

  # Apply a filter segment to narrow results
  dtctl query "fetch logs | limit 10" --segment my-segment-uid

  # Apply multiple segments (AND-combined)
  dtctl query "fetch logs | limit 10" -S seg-uid-1 -S seg-uid-2

  # Bind variables to a segment inline (URL-query style)
  dtctl query "fetch logs | limit 10" -S "my-segment?host=HOST-001"

  # Multiple values for a variable (comma-separated)
  dtctl query "fetch logs | limit 10" -S "my-segment?host=HOST-001,HOST-002"

  # Multiple variables on one segment
  dtctl query "fetch logs | limit 10" -S "my-segment?host=HOST-001&ns=production"

  # Apply segments with variables from a YAML file
  dtctl query "fetch logs | limit 10" --segments-file segments.yaml
`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if !isSupportedQueryOutputFormat(outputFormat(cmdContext(cmd))) {
				return fmt.Errorf("unsupported output format %q for query", outputFormat(cmdContext(cmd)))
			}

			cfg, c, err := setupClient(cmdContext(cmd))
			if err != nil {
				return err
			}

			executor := newDQLExecutorFromConfig(cmdContext(cmd), cfg, c)

			// Set up signal handling so a running Grail query is cancelled on Ctrl+C / SIGTERM.
			// NotifyContext leaves no goroutine behind when the command ends or the
			// caller's context is cancelled first.
			ctx, stop := signal.NotifyContext(cmdContext(cmd), os.Interrupt, syscall.SIGTERM)
			defer stop()

			queryFile, _ := cmd.Flags().GetString("file")
			setFlags, _ := cmd.Flags().GetStringArray("set")
			dqlFlag, _ := cmd.Flags().GetString("dql")
			if dqlFlag != "" && len(args) == 0 {
				args = []string{dqlFlag}
			}
			// Agents write `dtctl query dql <text>` / `query execute <text>`
			// (hallucinated subcommands) and shell-split queries into several
			// positional args. Until now args[0] was sent alone — the literal
			// string "dql", or a truncated query — producing an opaque
			// UNKNOWN_COMMAND or silently wrong results. Strip the marker token
			// and rejoin the fragments instead (no DQL statement starts with
			// these words).
			if len(args) > 1 {
				switch args[0] {
				case "dql", "execute", "exec", "run":
					args = args[1:]
				}
			}
			if len(args) > 1 {
				args = []string{strings.Join(args, " ")}
			}

			query, err := resolveQueryInput(cmdContext(cmd), queryFile, args, invocationStdin(cmdContext(cmd)))
			if err != nil {
				return err
			}

			// Apply template rendering if --set flags are provided
			if len(setFlags) > 0 {
				vars, err := template.ParseSetFlags(setFlags)
				if err != nil {
					return fmt.Errorf("invalid --set flag: %w", err)
				}

				rendered, err := template.RenderTemplate(query, vars)
				if err != nil {
					return fmt.Errorf("template rendering failed: %w", err)
				}

				query = rendered
			}

			// Fail fast, before any request, when the query provably needs a
			// storage scope the token lacks — instead of one NOT_AUTHORIZED_FOR_TABLE
			// per query an agent fans out.
			if err := dqlScopePrecheck(cmdContext(cmd), query); err != nil {
				return err
			}

			// Get visualization options
			live, _ := cmd.Flags().GetBool("live")
			interval, _ := cmd.Flags().GetDuration("interval")
			width, _ := cmd.Flags().GetInt("width")
			height, _ := cmd.Flags().GetInt("height")
			fullscreen, _ := cmd.Flags().GetBool("fullscreen")

			// Query limits resolve flag -> context config -> global config -> server
			// default, so a context-level ceiling also covers invocations that never
			// passed the flags. See resolveQueryLimits.
			queryLimits, err := resolveQueryLimits(cmd, cfg)
			if err != nil {
				return err
			}

			// Get query execution options
			fetchTimeoutSeconds, _ := cmd.Flags().GetInt32("fetch-timeout-seconds")
			enablePreview, _ := cmd.Flags().GetBool("enable-preview")
			noProgress, _ := cmd.Flags().GetBool("no-progress")
			enforceQueryConsumptionLimit, _ := cmd.Flags().GetBool("enforce-query-consumption-limit")
			includeTypes, _ := cmd.Flags().GetBool("include-types")
			typed, _ := cmd.Flags().GetBool("typed")
			// Only an explicit --include-types surfaces the DQL type block as a
			// top-level "types" key in structured output. Parquet/--typed force
			// includeTypes on below to *consume* the metadata internally, but that
			// must not start emitting the block, so capture intent before forcing.
			emitTypes := cmd.Flags().Changed("include-types")
			// Only an explicit, true --include-types asked for the block, so only
			// that warns when a format or live mode cannot show it;
			// --include-types=false declines it and must stay quiet.
			typesRequested := emitTypes && includeTypes
			// Parquet derives its column schema from DQL types, so request them even
			// if the user did not pass --include-types. The type metadata is consumed
			// to build the schema and is not added to the output rows.
			if formatRequiresIncludeTypes(outputFormat(cmdContext(cmd))) {
				includeTypes = true
			}
			// --typed casts scalar columns using the DQL type metadata, so it likewise
			// needs the types requested even without an explicit --include-types.
			if typed {
				includeTypes = true
			}
			includeContributions, _ := cmd.Flags().GetBool("include-contributions")

			// Get timeframe options
			defaultTimeframeStart, _ := cmd.Flags().GetString("default-timeframe-start")
			defaultTimeframeEnd, _ := cmd.Flags().GetString("default-timeframe-end")

			// Get localization options
			locale, _ := cmd.Flags().GetString("locale")
			timezone, _ := cmd.Flags().GetString("timezone")

			metadataFields, metadataDefaulted, err := resolveMetadataFlag(cmd, agentMode(cmdContext(cmd)))
			if err != nil {
				return err
			}

			// Get snapshot decode option
			decodeVal, _ := cmd.Flags().GetString("decode-snapshots")
			var decodeMode exec.DecodeMode
			if cmd.Flags().Changed("decode-snapshots") {
				switch decodeVal {
				case "", "simplified":
					decodeMode = exec.DecodeSimplified
				case "full":
					decodeMode = exec.DecodeFull
				default:
					return fmt.Errorf("unsupported --decode-snapshots value %q (use \"simplified\" or \"full\")", decodeVal)
				}
			}

			// Parse filter segments
			segmentFlags, _ := cmd.Flags().GetStringArray("segment")
			segmentsFile, _ := cmd.Flags().GetString("segments-file")
			segmentVarFlags, _ := cmd.Flags().GetStringArray("segment-var")

			var segments []exec.FilterSegmentRef
			if len(segmentFlags) > 0 || segmentsFile != "" {
				var flagRefs, fileRefs []exec.FilterSegmentRef

				// Track original (pre-resolution) IDs so --segment-var can
				// reference segments by the same name/UID the user typed.
				origIDs := make(map[string]string) // resolved UID -> original flag value

				if len(segmentFlags) > 0 {
					flagRefs, err = parseSegmentFlags(segmentFlags)
					if err != nil {
						return err
					}

					// Resolve segment names to UIDs for --segment flag values.
					// IDs from --segments-file are assumed to be UIDs already (the file
					// format mirrors the API and should use UIDs).
					res := resolver.NewResolver(c)
					for i, ref := range flagRefs {
						orig := ref.ID
						resolved, resolveErr := res.ResolveID(resolver.TypeSegment, ref.ID)
						if resolveErr != nil {
							return fmt.Errorf("failed to resolve segment %q: %w", ref.ID, resolveErr)
						}
						flagRefs[i].ID = resolved
						origIDs[resolved] = orig
					}
				}

				if segmentsFile != "" {
					fileRefs, err = parseSegmentsFile(cmdContext(cmd), segmentsFile)
					if err != nil {
						return err
					}
				}

				segments = mergeSegmentRefs(flagRefs, fileRefs)

				// Apply --segment-var bindings
				if len(segmentVarFlags) > 0 {
					varMap, varErr := parseSegmentVarFlags(segmentVarFlags)
					if varErr != nil {
						return varErr
					}
					segments, err = applySegmentVars(segments, varMap, origIDs)
					if err != nil {
						return err
					}
				}

				if len(segments) > maxSegmentsPerQuery {
					return fmt.Errorf("too many segments: %d specified, maximum is %d per query", len(segments), maxSegmentsPerQuery)
				}
			} else if len(segmentVarFlags) > 0 {
				return fmt.Errorf("--segment-var requires at least one --segment or --segments-file")
			}

			clientContext, _ := cmd.Flags().GetString("client-context")

			// Resolved against the effective format, so the agent-mode -o auto
			// default composes with the agent-mode --compact default.
			queryFormat, autoByDefault := agentResultFormat(cmdContext(cmd))
			compact, compactWarning := resolveQueryCompact(cmd, agentMode(cmdContext(cmd)), queryFormat)
			if compactWarning != "" {
				output.FprintWarning(currentStderr(cmdContext(cmd)), "%s", compactWarning)
			}

			seriesVal, _ := cmd.Flags().GetString("series")
			precision, _ := cmd.Flags().GetInt("precision")
			seriesOpts, err := querySeriesOptions(cmdContext(cmd), seriesVal, cmd.Flags().Changed("series"),
				precision, cmd.Flags().Changed("precision"))
			if err != nil {
				return err
			}

			spillOpts, err := resolveSpillOptions(cmd, cfg)
			if err != nil {
				return err
			}
			bounds, err := resolveOutputBounds(cmd)
			if err != nil {
				return err
			}
			for _, w := range bounds.Warnings {
				output.FprintWarning(currentStderr(cmdContext(cmd)), "%s", w)
			}
			spillTenantID, spillContextName := spillProvenance(cfg)
			// A Parquet spill derives its columnar schema from DQL types, so request
			// them even when the displayed output format does not — same reasoning as
			// the -o parquet path above, applied to the spill destination.
			if spillOpts.Enabled() && spillWritesParquet(spillOpts) {
				includeTypes = true
			}

			opts := exec.DQLExecuteOptions{
				OutputFormat:                 queryFormat,
				AutoFormatByDefault:          autoByDefault,
				JQFilter:                     jqFilter(cmdContext(cmd)),
				AgentMode:                    agentMode(cmdContext(cmd)),
				Decode:                       decodeMode,
				Width:                        width,
				Height:                       height,
				Fullscreen:                   fullscreen,
				MaxResultRecords:             queryLimits.MaxResultRecords,
				MaxResultBytes:               queryLimits.MaxResultBytes,
				DefaultScanLimitGbytes:       queryLimits.ScanLimitGbytes,
				DefaultSamplingRatio:         queryLimits.SamplingRatio,
				FetchTimeoutSeconds:          fetchTimeoutSeconds,
				EnablePreview:                enablePreview,
				EnforceQueryConsumptionLimit: enforceQueryConsumptionLimit,
				IncludeTypes:                 includeTypes,
				EmitTypes:                    emitTypes,
				TypesRequested:               typesRequested,
				IncludeContributions:         includeContributions,
				Typed:                        typed,
				Compact:                      compact,
				Series:                       seriesOpts.Mode,
				Precision:                    seriesOpts.Precision,
				SeriesDefaulted:              seriesOpts.SeriesDefaulted,
				PrecisionDefaulted:           seriesOpts.PrecisionDefaulted,
				DefaultTimeframeStart:        defaultTimeframeStart,
				DefaultTimeframeEnd:          defaultTimeframeEnd,
				Locale:                       locale,
				Timezone:                     timezone,
				MetadataFields:               metadataFields,
				MetadataDefaulted:            metadataDefaulted,
				Verbose:                      verbosity(cmdContext(cmd)) > 0,
				Segments:                     segments,
				ClientContext:                clientContext,
				Spill:                        spillOpts,
				MaxFieldChars:                bounds.MaxFieldChars,
				MaxOutputBytes:               bounds.MaxOutputBytes,
				TenantID:                     spillTenantID,
				ContextName:                  spillContextName,
				Decorate: func(ctx *output.ResponseContext, result *exec.DQLQueryResponse, _ []map[string]interface{}) {
					named := defaultTimeframeStart != "" || defaultTimeframeEnd != "" || queryNamesWindow(query)
					ctx.Window = queryWindowContext(result, named)
				},
				// The progress bar is a user-facing affordance of the `query`
				// command only; opt in here (subject to --no-progress) so internal
				// query callers stay silent by default.
				ShowProgress: !noProgress,
			}

			// Handle live mode
			if live {
				if !currentCaps(cmdContext(cmd)).LongRunningStreams {
					return &CapabilityError{Feature: "live mode"}
				}
				// Warn about flags that are not meaningfully applicable in live mode
				if len(metadataFields) > 0 {
					output.FprintWarning(currentStderr(cmdContext(cmd)), "--metadata is ignored in live mode (metadata is not displayed during live updates)")
				}
				if agentMode(cmdContext(cmd)) {
					output.FprintWarning(currentStderr(cmdContext(cmd)), "--agent is ignored in live mode (live mode requires an interactive terminal)")
				}
				if spillOpts.Enabled() {
					output.FprintWarning(currentStderr(cmdContext(cmd)), "--spill is ignored in live mode (live mode streams rows to the terminal)")
				}
				if includeContributions {
					output.FprintWarning(currentStderr(cmdContext(cmd)), "--include-contributions is ignored in live mode (contribution data is not displayed during live updates)")
				}
				if typed {
					output.FprintWarning(currentStderr(cmdContext(cmd)), "--typed is ignored in live mode (live mode renders a table, where the API's string encoding is not surfaced)")
				}
				if typesRequested {
					output.FprintWarning(currentStderr(cmdContext(cmd)), "--include-types is ignored in live mode (live updates render the records only, without the types block)")
				}
				if cmd.Flags().Changed("series") || cmd.Flags().Changed("precision") {
					output.FprintWarning(currentStderr(cmdContext(cmd)), "--series and --precision are ignored in live mode (live mode renders the full series)")
				}

				if interval == 0 {
					interval = output.DefaultLiveInterval
				}

				// Live mode owns the terminal via its own printer; a per-fetch
				// progress bar would fight it, so keep it off.
				opts.ShowProgress = false

				// Create printer options for live mode (needed for resize support)
				printerOpts := output.PrinterOptions{
					Format:     outputFormat(cmdContext(cmd)),
					JQFilter:   jqFilter(cmdContext(cmd)),
					Width:      width,
					Height:     height,
					Fullscreen: fullscreen,
				}

				printer := newPrinterOpts(cmdContext(cmd), printerOpts)
				livePrinter := output.NewLivePrinterWithOpts(printer, interval, currentStdout(cmdContext(cmd)), printerOpts)

				// Create data fetcher that re-executes the query
				fetcher := func(fetchCtx context.Context) (interface{}, error) {
					result, err := executor.ExecuteQueryWithContext(fetchCtx, query, opts)
					if err != nil {
						return nil, err
					}
					if result == nil {
						return nil, nil // context cancelled; message already printed
					}
					// Extract records
					records := result.Records
					if result.Result != nil && len(result.Result.Records) > 0 {
						records = result.Result.Records
					}
					// Apply snapshot decoding if requested
					if decodeMode != exec.DecodeNone && len(records) > 0 {
						simplify := decodeMode == exec.DecodeSimplified
						records = output.DecodeSnapshotRecords(records, simplify)

						// For tabular formats, replace parsed_snapshot with a summary string
						switch outputFormat(fetchCtx) {
						case "", "table", "wide", "csv":
							records = output.SummarizeSnapshotForTable(records)
						}
					}
					return map[string]interface{}{"records": records}, nil
				}

				return livePrinter.RunLive(ctx, fetcher)
			}

			return executor.ExecuteWithContext(ctx, query, opts)
		},
	}
	c.Flags().StringP("file", "f", "", "read query from file")
	rejectEmptyFlag(c, "file")
	c.Flags().StringArray("set", []string{}, "set template variable (key=value)")
	c.Flags().String("dql", "", "DQL text (alias for the positional argument)")
	c.Flags().Bool("live", false, "enable live mode with periodic updates")
	c.Flags().Duration("interval", 60*time.Second, "refresh interval for live mode")
	c.Flags().Int("width", 0, "chart width in characters (0 = default)")
	c.Flags().Int("height", 0, "chart height in lines (0 = default)")
	c.Flags().Bool("fullscreen", false, "use terminal dimensions for chart")
	c.Flags().Int64("max-result-records", 0, "maximum number of result records to return (0 = use default, typically 1000)")
	c.Flags().Int64("max-result-bytes", 0, "maximum result size in bytes (0 = use default)")
	c.Flags().Float64("default-scan-limit-gbytes", 0, "scan limit in gigabytes (0 = use default)")
	c.Flags().Float64("default-sampling-ratio", 0, "default sampling ratio (0 = use default, normalized to power of 10 <= 100000)")
	c.Flags().Int32("fetch-timeout-seconds", 0, "time limit for fetching data in seconds (0 = use default)")
	c.Flags().Bool("enable-preview", false, "request preview results if available within timeout")
	c.Flags().Bool("no-progress", false, "disable the live progress bar shown on stderr for long queries")
	c.Flags().Bool("enforce-query-consumption-limit", false, "enforce query consumption limit")
	c.Flags().Bool("include-types", false, "surface DQL per-column type info as a top-level \"types\" key (json/yaml output)")
	c.Flags().Bool("include-contributions", false, "include bucket contribution information in query results")
	c.Flags().Bool("typed", false, "cast scalar columns (long, double, duration, boolean) to native JSON/YAML types instead of the API's string encoding; opt-in, implies --include-types")
	c.Flags().Bool("compact", false, `omit null values and print columns that hold one value in every row once, under "constant"
(json/yaml/toon and the agent envelope; also collapses them in a spill summary). Default: on in agent mode`)
	stability.MarkFlag(c, "compact", stability.Experimental, compactSince)
	c.Flags().String("default-timeframe-start", "", "query timeframe start timestamp (ISO-8601/RFC3339, e.g., '2022-04-20T12:10:04.123Z')")
	c.Flags().String("default-timeframe-end", "", "query timeframe end timestamp (ISO-8601/RFC3339, e.g., '2022-04-20T13:10:04.123Z')")
	c.Flags().String("locale", "", "query locale (e.g., 'en_US', 'de_DE')")
	c.Flags().String("timezone", "", "query timezone (e.g., 'UTC', 'Europe/Paris', 'America/New_York')")
	c.Flags().StringP("metadata", "M", "", `include query metadata in output (use = for field selection)
bare --metadata or -M shows all fields; --metadata=field1,field2 selects specific fields
--metadata=minimal keeps only execution time, scanned bytes/data points, sampled (when true),
approximations (when present), and analysisTimeframe (when the query named no window);
it combines with field names. In agent mode the default is minimal; -M=all restores the full block
available: executionTimeMilliseconds,scannedRecords,scannedBytes,scannedDataPoints,
sampled,approximations,notifications,queryId,dqlVersion,query,canonicalQuery,timezone,locale,
analysisTimeframe,contributions,metrics`)
	c.Flags().Lookup("metadata").NoOptDefVal = "all"
	c.Flags().String("decode-snapshots", "", `decode Live Debugger snapshot payloads in query results
bare --decode-snapshots simplifies variant wrappers to plain values;
--decode-snapshots=full preserves the full decoded tree with type annotations`)
	c.Flags().Lookup("decode-snapshots").NoOptDefVal = "simplified"
	stability.MarkFlag(c, "decode-snapshots", stability.Experimental, liveDebuggerSince)
	c.Flags().StringArrayP("segment", "S", nil, `filter segment ID or name (repeatable, max 10, AND-combined)
supports inline variables: -S "SEGMENT?var=val&var2=val1,val2"`)
	c.Flags().String("segments-file", "", "YAML file with filter segment definitions (supports variables)")
	c.Flags().StringArrayP("segment-var", "V", nil, `override a segment variable (repeatable)
format: SEGMENT:VARIABLE=VALUE[,VALUE,...]
takes precedence over --segments-file variables`)
	c.Flags().String("client-context", "", `optional caller context included in the dt-client-context request header
useful for AI agents or scripts to declare their intent (e.g. "root-cause-analysis", "anomaly-investigation")`)
	c.Flags().String("spill", "", `spill a large result to a local file and return a summary instead of the rows
bare --spill = always; --spill=auto spills above --spill-threshold; --spill=never forces rows
default: never for a bare command, auto in agent mode`)
	c.Flags().Lookup("spill").NoOptDefVal = "always"
	c.Flags().String("spill-to", "", "explicit spill destination file (implies --spill=always; format inferred from extension)")
	c.Flags().String("spill-format", "", "spill file format when spilling to the default dir: jsonl|json|csv|parquet (default jsonl)")
	c.Flags().String("spill-threshold", "", "serialised output size above which a result spills, e.g. 50KB (default 50KB)")
	addOutputBoundFlags(c)
	stability.MarkFlag(c, "max-field-chars", stability.Experimental, outputBoundsSince)
	stability.MarkFlag(c, "max-output-bytes", stability.Experimental, outputBoundsSince)
	stability.MarkFlag(c, "max-output-tokens", stability.Experimental, outputBoundsSince)
	stability.MarkStable(c)
	addQueryLimitFlags(c)
	addQuerySeriesFlags(c)
	registerFlagCompletion(c, "metadata", metadataFieldCompletion)
	registerFlagCompletion(c, "decode-snapshots", func(_ *cobra.Command, _ []string, _ string) ([]string, cobra.ShellCompDirective) {
		return []string{
			"simplified\tFlatten variant wrappers to plain values (default)",
			"full\tPreserve full decoded tree with type annotations",
		}, cobra.ShellCompDirectiveNoFileComp
	})
	registerFlagCompletion(c, "segments-file", func(_ *cobra.Command, _ []string, _ string) ([]string, cobra.ShellCompDirective) {
		return []string{"yaml", "yml"}, cobra.ShellCompDirectiveFilterFileExt
	})
	registerFlagCompletion(c, "spill", func(_ *cobra.Command, _ []string, _ string) ([]string, cobra.ShellCompDirective) {
		return []string{
			"auto\tspill above the threshold, inline below",
			"always\talways spill",
			"never\tnever spill (rows inline)",
		}, cobra.ShellCompDirectiveNoFileComp
	})
	registerFlagCompletion(c, "spill-format", func(_ *cobra.Command, _ []string, _ string) ([]string, cobra.ShellCompDirective) {
		return []string{"jsonl", "json", "csv", "parquet"}, cobra.ShellCompDirectiveNoFileComp
	})
	return c
}

// maxSegmentsPerQuery is the maximum number of filter segments allowed per query (Dynatrace limit).
const maxSegmentsPerQuery = 10

// parseSegmentFlags parses --segment flag values into FilterSegmentRef entries.
// Each value can be a plain segment ID/name, or include inline variable bindings
// using URL-query-style syntax: "SEGMENT?var=val&var2=val1,val2"
func parseSegmentFlags(segmentIDs []string) ([]exec.FilterSegmentRef, error) {
	var refs []exec.FilterSegmentRef
	for _, raw := range segmentIDs {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			return nil, fmt.Errorf("--segment value must not be empty")
		}

		id := raw
		var variables []exec.FilterSegmentVariable

		// Split on first "?" to separate segment ID from inline variables
		if qIdx := strings.Index(raw, "?"); qIdx >= 0 {
			id = strings.TrimSpace(raw[:qIdx])
			queryStr := raw[qIdx+1:]

			if id == "" {
				return nil, fmt.Errorf("invalid --segment %q: segment ID must not be empty", raw)
			}
			if queryStr == "" {
				return nil, fmt.Errorf("invalid --segment %q: expected variables after '?'", raw)
			}

			// Parse "var=val&var2=val1,val2" pairs
			pairs := strings.Split(queryStr, "&")
			for _, pair := range pairs {
				pair = strings.TrimSpace(pair)
				if pair == "" {
					continue
				}
				eqIdx := strings.Index(pair, "=")
				if eqIdx < 0 {
					return nil, fmt.Errorf("invalid --segment %q: expected VARIABLE=VALUE in %q", raw, pair)
				}
				varName := strings.TrimSpace(pair[:eqIdx])
				valuesStr := strings.TrimSpace(pair[eqIdx+1:])
				if varName == "" {
					return nil, fmt.Errorf("invalid --segment %q: variable name must not be empty", raw)
				}
				if valuesStr == "" {
					return nil, fmt.Errorf("invalid --segment %q: variable %q value must not be empty", raw, varName)
				}
				values := strings.Split(valuesStr, ",")
				for i, v := range values {
					values[i] = strings.TrimSpace(v)
				}
				variables = append(variables, exec.FilterSegmentVariable{
					Name:   varName,
					Values: values,
				})
			}
		}

		refs = append(refs, exec.FilterSegmentRef{ID: id, Variables: variables})
	}
	return refs, nil
}

// parseSegmentsFile reads a YAML file containing an array of FilterSegmentRef entries.
func parseSegmentsFile(ctx context.Context, path string) ([]exec.FilterSegmentRef, error) {
	data, err := readFileFlag(ctx, "segments-file", path)
	if err != nil {
		return nil, fmt.Errorf("failed to read segments file: %w", err)
	}

	var refs []exec.FilterSegmentRef
	if err := yaml.Unmarshal(data, &refs); err != nil {
		return nil, fmt.Errorf("failed to parse segments file %q: %w", path, err)
	}

	// Validate entries
	for i, ref := range refs {
		if ref.ID == "" {
			return nil, fmt.Errorf("segment entry %d in %q is missing required 'id' field", i+1, path)
		}
	}

	return refs, nil
}

// parseSegmentVarFlags parses --segment-var flag values into a map of segment ID -> variables.
// Format: "SEGMENT:VARIABLE=VALUE[,VALUE,...]"
//
// Examples:
//
//	"seg-uid:host=HOST-001"           -> seg-uid: [{name: "host", values: ["HOST-001"]}]
//	"seg-uid:host=HOST-001,HOST-002"  -> seg-uid: [{name: "host", values: ["HOST-001", "HOST-002"]}]
//
// Multiple --segment-var flags for the same segment accumulate variables.
func parseSegmentVarFlags(vars []string) (map[string][]exec.FilterSegmentVariable, error) {
	result := make(map[string][]exec.FilterSegmentVariable)

	for _, v := range vars {
		v = strings.TrimSpace(v)
		if v == "" {
			return nil, fmt.Errorf("--segment-var value must not be empty")
		}

		// Split on first ":" to get segment ID and variable assignment
		colonIdx := strings.Index(v, ":")
		if colonIdx < 0 {
			return nil, fmt.Errorf("invalid --segment-var %q: expected format SEGMENT:VARIABLE=VALUE[,VALUE,...]", v)
		}

		segmentID := strings.TrimSpace(v[:colonIdx])
		varAssignment := strings.TrimSpace(v[colonIdx+1:])

		if segmentID == "" {
			return nil, fmt.Errorf("invalid --segment-var %q: segment ID must not be empty", v)
		}
		if varAssignment == "" {
			return nil, fmt.Errorf("invalid --segment-var %q: variable assignment must not be empty", v)
		}

		// Split variable assignment on first "=" to get name and values
		eqIdx := strings.Index(varAssignment, "=")
		if eqIdx < 0 {
			return nil, fmt.Errorf("invalid --segment-var %q: expected VARIABLE=VALUE[,VALUE,...] after segment ID", v)
		}

		varName := strings.TrimSpace(varAssignment[:eqIdx])
		valuesStr := strings.TrimSpace(varAssignment[eqIdx+1:])

		if varName == "" {
			return nil, fmt.Errorf("invalid --segment-var %q: variable name must not be empty", v)
		}
		if valuesStr == "" {
			return nil, fmt.Errorf("invalid --segment-var %q: variable value must not be empty", v)
		}

		// Split values on comma
		values := strings.Split(valuesStr, ",")
		for i, val := range values {
			values[i] = strings.TrimSpace(val)
		}

		// Check if we already have a variable with this name for this segment
		// (merge values if so)
		found := false
		for i, existing := range result[segmentID] {
			if existing.Name == varName {
				result[segmentID][i].Values = append(result[segmentID][i].Values, values...)
				found = true
				break
			}
		}
		if !found {
			result[segmentID] = append(result[segmentID], exec.FilterSegmentVariable{
				Name:   varName,
				Values: values,
			})
		}
	}

	return result, nil
}

// applySegmentVars applies parsed --segment-var bindings to a slice of segment refs.
// Variables are matched by the original (pre-resolution) segment identifier, which is
// looked up via the origIDs map (resolved ID -> original flag value). This allows users
// to specify variables using the same name/UID they passed to --segment.
//
// Returns an error if a --segment-var references a segment not present in the refs.
func applySegmentVars(refs []exec.FilterSegmentRef, varMap map[string][]exec.FilterSegmentVariable, origIDs map[string]string) ([]exec.FilterSegmentRef, error) {
	if len(varMap) == 0 {
		return refs, nil
	}

	// Build reverse lookup: original ID -> index in refs (using origIDs map)
	origToIdx := make(map[string]int, len(refs))
	for i, ref := range refs {
		if orig, ok := origIDs[ref.ID]; ok {
			origToIdx[orig] = i
		}
		// Also allow matching by resolved ID directly
		origToIdx[ref.ID] = i
	}

	for segID, variables := range varMap {
		idx, ok := origToIdx[segID]
		if !ok {
			return nil, fmt.Errorf("--segment-var references segment %q which is not specified via --segment or --segments-file", segID)
		}
		// Merge variables: CLI vars take precedence over file vars for the same name
		existing := refs[idx].Variables
		existingMap := make(map[string]int, len(existing))
		for i, v := range existing {
			existingMap[v.Name] = i
		}
		for _, newVar := range variables {
			if i, ok := existingMap[newVar.Name]; ok {
				// Replace existing variable values
				existing[i] = newVar
			} else {
				existing = append(existing, newVar)
			}
		}
		refs[idx].Variables = existing
	}

	return refs, nil
}

// mergeSegmentRefs merges segment refs from --segment flags and --segments-file.
// File entries win on ID conflict (they may carry variables). Duplicates by ID are deduplicated.
func mergeSegmentRefs(flagRefs, fileRefs []exec.FilterSegmentRef) []exec.FilterSegmentRef {
	// Build map keyed by ID; file entries are added first so flag entries
	// only fill in IDs not already present (file wins).
	seen := make(map[string]exec.FilterSegmentRef, len(flagRefs)+len(fileRefs))
	order := make([]string, 0, len(flagRefs)+len(fileRefs))

	// File entries first (higher priority)
	for _, ref := range fileRefs {
		if _, exists := seen[ref.ID]; !exists {
			order = append(order, ref.ID)
		}
		seen[ref.ID] = ref
	}

	// Flag entries only if not already present from file
	for _, ref := range flagRefs {
		if _, exists := seen[ref.ID]; !exists {
			order = append(order, ref.ID)
			seen[ref.ID] = ref
		}
	}

	merged := make([]exec.FilterSegmentRef, 0, len(order))
	for _, id := range order {
		merged = append(merged, seen[id])
	}
	return merged
}

func init() {
	rootCmd.AddCommand(queryCmd)
}

// resolveMetadataFlag reads --metadata. Agent mode is token-optimal by default:
// without the flag it gets the minimal set (defaulted=true, so the envelope can
// name the -M=all opt-out when that dropped something). An explicit value
// always wins, and outside agent mode an absent flag means no metadata.
func resolveMetadataFlag(cmd *cobra.Command, agent bool) (fields []string, defaulted bool, err error) {
	if agent && !cmd.Flags().Changed("metadata") {
		return []string{output.MetadataMinimal}, true, nil
	}
	val, _ := cmd.Flags().GetString("metadata")
	fields, err = output.ParseMetadataFields(val)
	return fields, false, err
}

// metadataFieldCompletion provides shell completion for --metadata flag values.
// It supports comma-separated field selection: already-typed fields are excluded
// from suggestions, and completions include the existing prefix so the shell
// appends correctly (e.g., typing "scannedRecords," suggests "scannedRecords,queryId").
func metadataFieldCompletion(_ *cobra.Command, _ []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	allFields := output.ValidMetadataFieldNames()

	// If nothing typed yet, offer "all" and "minimal" plus individual field names
	if toComplete == "" {
		suggestions := make([]string, 0, len(allFields)+2)
		suggestions = append(suggestions, "all\tInclude all metadata fields")
		suggestions = append(suggestions, output.MetadataMinimal+"\tInclude only cost and sampling fields worth acting on")
		suggestions = append(suggestions, allFields...)
		return suggestions, cobra.ShellCompDirectiveNoFileComp | cobra.ShellCompDirectiveNoSpace
	}

	// Split on comma to find already-selected fields and the current partial
	parts := strings.Split(toComplete, ",")
	currentPartial := parts[len(parts)-1]
	prefix := ""
	if len(parts) > 1 {
		prefix = strings.Join(parts[:len(parts)-1], ",") + ","
	}

	// Build set of already-selected fields
	selected := make(map[string]bool, len(parts)-1)
	for _, p := range parts[:len(parts)-1] {
		selected[strings.TrimSpace(p)] = true
	}

	// Suggest unselected fields (and the minimal selector) that match the current partial
	var suggestions []string
	for _, f := range append([]string{output.MetadataMinimal}, allFields...) {
		if selected[f] {
			continue
		}
		if strings.HasPrefix(f, currentPartial) {
			suggestions = append(suggestions, prefix+f)
		}
	}

	return suggestions, cobra.ShellCompDirectiveNoFileComp | cobra.ShellCompDirectiveNoSpace
}

// Declared stable: the invocation and output contract of this command is
// additive-only. Stable is never implied -- see AGENTS.md "Stability Tiers".
