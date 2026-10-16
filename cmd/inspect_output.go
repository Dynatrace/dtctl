package cmd

import (
	"context"

	"github.com/spf13/cobra"

	"github.com/dynatrace-oss/dtctl/pkg/config"
	"github.com/dynatrace-oss/dtctl/pkg/inspect"
	"github.com/dynatrace-oss/dtctl/pkg/output"
)

// emitInspectResult renders an inspect result. Row access (kind:records) and the
// re-derived summary (kind:file-summary) reuse the same envelope contract and
// printers as the rest of dtctl, so an agent learns nothing new (IN5). In agent
// mode an oversized row window re-spills to a new managed file instead of
// flooding context (IN8).
func emitInspectResult(cmd *cobra.Command, cfg *config.Config, req inspect.Request, res *inspect.Result) error {
	if res.Kind == output.KindFileSummary {
		return emitInspectSummary(cmdContext(cmd), req, res)
	}
	return emitInspectRecords(cmd, cfg, req, res)
}

// emitInspectRecords renders a row-access or filtered (--jq) result. Both produce
// the same kind:records shape: a --jq filter is applied per record IN THE ENGINE
// (full-file streaming, PrimFilter), so by the time records reach here they are
// already the filtered objects — the printer must NOT re-apply jq, and the output
// format stays the caller's choice (filtered rows are ordinary records, not
// arbitrary jq scalars). In agent mode an oversized result re-spills first (IN8).
func emitInspectRecords(cmd *cobra.Command, cfg *config.Config, req inspect.Request, res *inspect.Result) error {
	records := res.Records

	if agentMode(cmdContext(cmd)) {
		spilled, err := maybeRespill(cmd, cfg, req, res)
		if err != nil {
			return err
		}
		if spilled != nil {
			return output.EncodeEnvelope(currentStdout(cmdContext(cmd)), *spilled)
		}

		// A non-JSON result encoding (-o csv/parquet/toon) owns the output shape;
		// defer to the standard agent printer (mirrors `query`'s inline path).
		if output.NormalizeMeasureEncoding(outputFormat(cmdContext(cmd))) != "json" {
			ctx := inspectContext(req, res, len(records))
			ap := output.NewAgentPrinter(currentStdout(cmdContext(cmd)), ctx)
			ap.SetResultFormat(outputFormat(cmdContext(cmd)))
			return ap.PrintList(records)
		}

		ctx := inspectContext(req, res, len(records))
		resp := output.Response{
			OK:              true,
			EnvelopeVersion: output.EnvelopeVersion,
			Result:          &output.InlineRecords{Kind: output.KindRecords, Records: records},
			Context:         ctx,
		}
		return output.EncodeEnvelope(currentStdout(cmdContext(cmd)), resp)
	}

	// Human / scripted output: print the rows in the chosen format, warnings to
	// stderr so they never corrupt a piped result.
	printInspectWarnings(cmdContext(cmd), res.Warnings)
	p := newPrinterOpts(cmdContext(cmd), output.PrinterOptions{Format: outputFormat(cmdContext(cmd)), Writer: currentStdout(cmdContext(cmd))})
	return p.PrintList(records)
}

// emitInspectSummary renders a re-derived file-summary (--schema/--stats/--sample).
// These primitives never carry a --jq filter (it is rejected as mutually
// exclusive at flag-validation time), so there is no per-record jq to apply here.
func emitInspectSummary(ictx context.Context, req inspect.Request, res *inspect.Result) error {
	total := 0
	if res.Summary != nil {
		total = res.Summary.Rows
	}

	if agentMode(ictx) {
		ctx := inspectContext(req, res, total)
		resp := output.Response{
			OK:              true,
			EnvelopeVersion: output.EnvelopeVersion,
			Result:          res.Summary,
			Context:         ctx,
		}
		return output.EncodeEnvelope(currentStdout(ictx), resp)
	}

	// Human output: a struct does not table well, so default tabular formats to
	// pretty JSON; honour an explicit structured format otherwise.
	printInspectWarnings(ictx, res.Warnings)
	format := outputFormat(ictx)
	switch format {
	case "", "table", "wide":
		format = "json"
	}
	p := newPrinterOpts(ictx, output.PrinterOptions{Format: format, Writer: currentStdout(ictx)})
	return p.Print(res.Summary)
}

// inspectContext builds the agent envelope context shared by both result kinds.
func inspectContext(req inspect.Request, res *inspect.Result, total int) *output.ResponseContext {
	t := total
	ctx := &output.ResponseContext{
		Verb:        "inspect",
		Resource:    inspectResourceFromSidecar(res.Sidecar),
		Total:       &t,
		Warnings:    res.Warnings,
		Suggestions: inspectSuggestions(req, res),
	}
	return ctx
}

// inspectSuggestions returns tool-agnostic follow-up hints (D28: no third-party
// project is ever named). A re-derived summary leads with row access (IN4) since
// that is the call an agent cannot satisfy from a manifest it already holds.
func inspectSuggestions(req inspect.Request, res *inspect.Result) []string {
	if res.Kind != output.KindFileSummary {
		return nil
	}
	return []string{
		"# this is a re-derived summary; for the rows themselves use row access, e.g. dtctl inspect " + req.Path + " --head 20",
		"# for aggregate questions, prefer pushing the work into DQL and re-querying ('… | summarize …')",
	}
}

// printInspectWarnings prints engine warnings to stderr for a human reader.
func printInspectWarnings(ctx context.Context, warnings []string) {
	for _, w := range warnings {
		output.FprintWarning(currentStderr(ctx), "%s", w)
	}
}
