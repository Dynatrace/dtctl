package cmd

import (
	"context"
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/dynatrace-oss/dtctl/pkg/exec"
	"github.com/dynatrace-oss/dtctl/pkg/output"
	"github.com/dynatrace-oss/dtctl/pkg/stability"
)

// querySeriesSince is the release that introduced --series and --precision.
const querySeriesSince = "0.40.0"

// maxQueryPrecision is the largest --precision accepted: a float64 carries
// about 17 significant digits, so anything above that rounds nothing.
const maxQueryPrecision = 17

// seriesOptions is the resolved --series / --precision pair, with whether each
// is an agent-mode default rather than the caller's choice.
type seriesOptions struct {
	Mode               output.SeriesMode
	Precision          int
	SeriesDefaulted    bool
	PrecisionDefaulted bool
}

// resolveSeriesOptions validates --series and --precision against the output
// format and applies the agent-mode defaults: a flag the caller did not set
// becomes --series=summary and --precision 4 in agent mode (token-optimal
// output), and stays full/unrounded otherwise. An explicit value always wins.
//
// A summary replaces the arrays the chart formats plot, so an explicit summary
// with a chart format is an error and the agent default skips charts. A Parquet
// export keeps the raw data, so no default applies to it.
func resolveSeriesOptions(series string, seriesSet bool, precision int, precisionSet bool, format string, agent bool) (seriesOptions, error) {
	var opts seriesOptions
	mode, err := output.ParseSeriesMode(series)
	if err != nil {
		return opts, err
	}
	if precision < 0 || precision > maxQueryPrecision {
		return opts, fmt.Errorf("invalid --precision %d: use 1-%d significant digits, or 0 to keep full precision", precision, maxQueryPrecision)
	}
	opts.Mode, opts.Precision = mode, precision

	f := strings.ToLower(strings.TrimSpace(format))
	chart := false
	switch f {
	case "chart", "sparkline", "spark", "barchart", "bar", "braille", "br":
		chart = true
	}
	if seriesSet && mode.Kind == output.SeriesSummary && chart {
		return seriesOptions{}, fmt.Errorf("--series=summary cannot be combined with -o %s, which plots the full series (use --series=downsample:N to reduce it instead)", format)
	}

	if !agent || f == "parquet" {
		return opts, nil
	}
	if !seriesSet && !chart {
		opts.Mode, opts.SeriesDefaulted = output.SeriesMode{Kind: output.SeriesSummary}, true
	}
	if !precisionSet {
		opts.Precision, opts.PrecisionDefaulted = exec.AgentDefaultPrecision, true
	}
	return opts, nil
}

// querySeriesOptions resolves the series options against the format the query
// actually renders with. Agent mode replaces a non-explicit -o (such as one
// from DTCTL_OUTPUT) with -o auto, so the raw output format would skip the
// series defaults for a chart or parquet format that never applies.
func querySeriesOptions(ctx context.Context, series string, seriesSet bool, precision int, precisionSet bool) (seriesOptions, error) {
	format, _ := agentResultFormat(ctx)
	return resolveSeriesOptions(series, seriesSet, precision, precisionSet, format, agentMode(ctx))
}

// addQuerySeriesFlags registers --series and --precision on the query command.
// Called from newQueryCmd, so a per-invocation tree gets them too.
func addQuerySeriesFlags(c *cobra.Command) {
	c.Flags().String("series", "full", `how to render timeseries arrays (records with timeframe and interval):
full = every datapoint; summary = per-series min/avg/max/p95/last/n,
peak/trough times, a sparkline and a level-shift hint; downsample:N = at most
N points per series, keeping each bucket's min and max so extremes survive
default: full, summary in agent mode`)
	c.Flags().Int("precision", 0, `round numbers in the result to N significant digits, never into the integer part
0 = full precision; default: 0, 4 in agent mode (--series=summary statistics use 3 when 0)`)

	registerFlagCompletion(c, "series", func(_ *cobra.Command, _ []string, _ string) ([]string, cobra.ShellCompDirective) {
		return []string{
			"full\tevery datapoint (default)",
			"summary\tper-series statistics and a sparkline",
			"downsample:60\tat most 60 points per series, extremes kept",
		}, cobra.ShellCompDirectiveNoFileComp
	})

	// Both change the numbers a caller receives; they ship experimental on the
	// stable query command until the summary shape has settled.
	stability.MarkFlag(c, "series", stability.Experimental, querySeriesSince)
	stability.MarkFlag(c, "precision", stability.Experimental, querySeriesSince)
}
