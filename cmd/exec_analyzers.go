package cmd

import (
	"encoding/json"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/dynatrace-oss/dtctl/pkg/output"
	"github.com/dynatrace-oss/dtctl/pkg/resources/analyzer"
	"github.com/dynatrace-oss/dtctl/pkg/safety"
	"github.com/dynatrace-oss/dtctl/pkg/stability"
)

// execAnalyzerCmd executes a Davis analyzer
var execAnalyzerCmd = newExecAnalyzerCmd()

func newExecAnalyzerCmd() *cobra.Command {
	c := &cobra.Command{
		Use:     "analyzer <analyzer-name>",
		Aliases: []string{"az"},
		Short:   "Execute a Davis AI analyzer",
		Long: `Execute a Davis AI analyzer with the given input.

Examples:
  # Execute analyzer with input from file
  dtctl exec analyzer dt.statistics.GenericForecastAnalyzer -f input.json

  # Execute with inline JSON input
  dtctl exec analyzer dt.statistics.GenericForecastAnalyzer --input '{"query":"timeseries avg(dt.host.cpu.usage)"}'

  # Execute with DQL query shorthand (for forecast/timeseries analyzers)
  dtctl exec analyzer dt.statistics.GenericForecastAnalyzer --query "timeseries avg(dt.host.cpu.usage)"

  # Validate input without executing
  dtctl exec analyzer dt.statistics.GenericForecastAnalyzer -f input.json --validate

  # Execute and wait for completion (default)
  dtctl exec analyzer dt.statistics.GenericForecastAnalyzer -f input.json --wait

  # Output as JSON
  dtctl exec analyzer dt.statistics.GenericForecastAnalyzer -f input.json -o json
`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			analyzerName := args[0]

			// Running an analyzer computes over data and persists nothing, so it is a
			// read despite being a POST.
			_, c, err := setupWithSafety(cmdContext(cmd), safety.OperationRead)
			if err != nil {
				return err
			}

			handler := analyzer.NewHandler(c)

			// Build input from flags (shared with "verify analyzer")
			input, err := buildAnalyzerInput(cmd)
			if err != nil {
				return err
			}

			// Handle validate-only mode
			validateOnly, _ := cmd.Flags().GetBool("validate")
			if validateOnly {
				result, err := handler.Validate(analyzerName, input)
				if err != nil {
					return err
				}
				printer := newPrinterCtx(cmdContext(cmd))
				return printer.Print(result)
			}

			// Execute analyzer
			wait, _ := cmd.Flags().GetBool("wait")
			timeout, _ := cmd.Flags().GetInt("timeout")

			var result *analyzer.ExecuteResult
			if wait {
				result, err = handler.ExecuteAndWait(cmd.Context(), analyzerName, input, timeout)
			} else {
				result, err = handler.Execute(analyzerName, input, 30)
			}

			if err != nil {
				return err
			}

			return printAnalyzerResult(cmd, result)
		},
	}
	c.Flags().Bool("validate", false, "validate input without executing")
	c.Flags().Bool("wait", true, "wait for analyzer execution to complete")
	stability.MarkFlag(c, "wait", stability.Experimental, pre10Since)
	c.Flags().Int("timeout", 300, "timeout in seconds when waiting for completion")
	stability.MarkFlag(c, "timeout", stability.Experimental, pre10Since)
	c.Flags().String("series", "full", `how to render timeseries embedded in the result (agent mode only):
full = every datapoint; summary = per-series min/avg/max/p95/last/n and a sparkline;
downsample:N = at most N points per series
default: summary in agent mode`)
	c.Flags().Int("precision", 0, `round numbers in the result to N significant digits (agent mode only)
0 = full precision; default: 4 in agent mode`)
	stability.MarkFlag(c, "series", stability.Experimental, analyzerSeriesSince)
	stability.MarkFlag(c, "precision", stability.Experimental, analyzerSeriesSince)
	stability.MarkStable(c)
	addAnalyzerInputFlags(c)
	return c
}

// printAnalyzerResult prints the raw result, or in agent mode a shaped one:
// no echoed input or DQL types, nulls dropped, embedded timeseries under
// --series/--precision (agent defaults: summary, 4 digits).
func printAnalyzerResult(cmd *cobra.Command, result *analyzer.ExecuteResult) error {
	ctx := cmdContext(cmd)
	if !agentMode(ctx) {
		// Default to JSON: the table shows no data.
		outputFormat, _ := cmd.Flags().GetString("output")
		if outputFormat == "" || outputFormat == "table" {
			outputFormat = "json"
		}
		return newPrinter(ctx, outputFormat).Print(result)
	}
	series, _ := cmd.Flags().GetString("series")
	precision, _ := cmd.Flags().GetInt("precision")
	opts, err := querySeriesOptions(ctx, series, cmd.Flags().Changed("series"), precision, cmd.Flags().Changed("precision"))
	if err != nil {
		return err
	}
	shaped, eff := shapeAnalyzerForAgent(result, opts.Mode, opts.Precision)
	printer := newPrinterCtx(ctx)
	if ap := enrichAgent(printer, "exec", "analyzer"); ap != nil {
		var hints []string
		if eff.NoFindings {
			hints = append(hints, "# analyzer finished SUCCESSFUL with an empty output: nothing detected in the timeframe, not missing data")
		}
		if h := analyzerSeriesAdvice(eff.SeriesEffect, opts); h != "" {
			hints = append(hints, h)
		}
		hints = append(hints, "# the echoed input and DQL types are omitted in agent mode; drop -A for the raw result")
		ap.SetSuggestions(hints)
	}
	return printer.Print(shaped)
}

// analyzerSeriesAdvice names the opt-out for an agent-mode default that changed
// the result (mirrors exec.defaultSeriesAdvice for query).
func analyzerSeriesAdvice(eff output.SeriesEffect, opts seriesOptions) string {
	summarized := eff.Summarized && opts.SeriesDefaulted
	rounded := eff.Rounded && opts.PrecisionDefaulted
	switch {
	case summarized && rounded:
		return fmt.Sprintf("# timeseries summarized and numbers rounded to %d significant digits (agent-mode default) — add --series=full --precision 0 for the raw values", opts.Precision)
	case summarized:
		return "# timeseries summarized (agent-mode default) — add --series=full for the raw datapoints"
	case rounded:
		return fmt.Sprintf("# numbers rounded to %d significant digits (agent-mode default) — add --precision 0 for full precision", opts.Precision)
	}
	return ""
}

// addAnalyzerInputFlags registers the input-source flags shared by
// "exec analyzer" and "verify analyzer".
func addAnalyzerInputFlags(cmd *cobra.Command) {
	cmd.Flags().StringP("file", "f", "", "read input from JSON file, or - for stdin")
	cmd.Flags().String("input", "", "inline JSON input")
	cmd.Flags().String("query", "", "DQL query shorthand (for timeseries analyzers)")
	// Exactly one source is required; an explicitly empty one must not count
	// as "not given".
	for _, name := range []string{"file", "input", "query"} {
		rejectEmptyFlag(cmd, name)
	}
}

// buildAnalyzerInput assembles an analyzer input map from the --file, --input,
// or --query flags. It is shared by "exec analyzer" and "verify analyzer" so the
// two commands accept identical input. Exactly one source must be provided.
func buildAnalyzerInput(cmd *cobra.Command) (map[string]interface{}, error) {
	inputFile, _ := cmd.Flags().GetString("file")
	inputJSON, _ := cmd.Flags().GetString("input")
	query, _ := cmd.Flags().GetString("query")

	switch {
	case inputFile != "":
		content, err := readFileFlag(cmdContext(cmd), "file", inputFile)
		if err != nil {
			return nil, err
		}
		var input map[string]interface{}
		if err := json.Unmarshal(content, &input); err != nil {
			return nil, fmt.Errorf("failed to parse input file: %w", err)
		}
		return input, nil
	case inputJSON != "":
		var input map[string]interface{}
		if err := json.Unmarshal([]byte(inputJSON), &input); err != nil {
			return nil, fmt.Errorf("failed to parse input JSON: %w", err)
		}
		return input, nil
	case query != "":
		// Shorthand for timeseries analyzers.
		return map[string]interface{}{"timeSeriesData": query}, nil
	default:
		return nil, fmt.Errorf("input is required: use --file, --input, or --query")
	}
}

// analyzerSeriesSince is the release that added --series/--precision to exec analyzer.
const analyzerSeriesSince = "0.42.0"

// Declared stable: the invocation and output contract of this command is
// additive-only. Stable is never implied -- see AGENTS.md "Stability Tiers".
