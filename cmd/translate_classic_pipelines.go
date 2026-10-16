package cmd

import (
	"context"
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/dynatrace-oss/dtctl/pkg/output"
	"github.com/dynatrace-oss/dtctl/pkg/resources/classicpipelinestranslate"
	"github.com/dynatrace-oss/dtctl/pkg/stability"
)

// translateClassicPipelinesCmd translates a Classic pipeline into an
// OpenPipeline configuration pipeline.
var translateClassicPipelinesCmd = newTranslateClassicPipelinesCmd()

func newTranslateClassicPipelinesCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "classic-pipelines <logs|bizevents>",
		Short: "Translate a Classic pipeline into an OpenPipeline configuration pipeline",
		Long: `Translate the tenant's Classic pipeline for a configuration scope into an
OpenPipeline configuration pipeline (Settings shape).

This is a read-only call that returns the translated pipeline verbatim. Every
output format emits the pipeline document itself (so it is directly reviewable
and applyable via the Settings API). The translation is deterministic where
possible; when a processing rule's definition script could not be translated
automatically it is reported via a warning on stderr (and in the agent envelope
under -A), and that part needs a manual rewrite.

The scope is a positional argument and must be one of: logs, bizevents.

Note: the underlying API is public but early-adopter and may change.

Examples:
  # Translate the logs Classic pipeline (pretty-printed pipeline document)
  dtctl translate classic-pipelines logs

  # Translate bizevents and export the document as YAML for review/editing
  dtctl translate classic-pipelines bizevents -o yaml > reference-pipeline.yaml

  # Print the translated pipeline as JSON
  dtctl translate classic-pipelines logs -o json

  # Skip disabled rules in the translation (overrides the server default)
  dtctl translate classic-pipelines logs --skip-disabled-rules=true`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			scope := args[0]
			if !classicpipelinestranslate.IsValidConfiguration(scope) {
				return fmt.Errorf(
					"invalid configuration scope %q: must be one of %s",
					scope, strings.Join(classicpipelinestranslate.ValidConfigurations, ", "),
				)
			}

			includeSampleData, _ := cmd.Flags().GetBool("include-sample-data")
			skipDisabledRules, _ := cmd.Flags().GetBool("skip-disabled-rules")
			skipBuiltinProcessingRules, _ := cmd.Flags().GetBool("skip-builtin-processing-rules")

			_, c, printer, err := setup(cmdContext(cmd))
			if err != nil {
				return err
			}

			handler := classicpipelinestranslate.NewHandler(c)
			result, err := handler.Translate(classicpipelinestranslate.TranslateOptions{
				Configuration:              scope,
				IncludeSampleData:          includeSampleData,
				SkipDisabledRules:          skipDisabledRules,
				SkipBuiltinProcessingRules: skipBuiltinProcessingRules,
			})
			if err != nil {
				return err
			}

			ap := enrichAgent(printer, "translate", "classic-pipelines")

			// Surface the partial-translation warning out-of-band so it never
			// pollutes the deliverable on stdout: on stderr for humans, via the
			// agent envelope for agents.
			if result.WithWarning {
				const warn = "some processing rules could not be translated automatically and need a manual rewrite (withWarning=true)"
				if ap != nil {
					ap.SetWarnings([]string{warn})
				} else {
					output.FprintWarning(currentStderr(cmdContext(cmd)), "%s", warn)
				}
			}

			// A scope with no Classic pipeline configured yields a null document.
			// Tell a human there is nothing to translate; structured output still
			// emits null so piped/scripted callers see a consistent shape.
			if result.Value == nil && ap == nil {
				output.FprintInfo(currentStderr(cmdContext(cmd)), "No Classic pipeline is configured for scope %q; nothing to translate.", scope)
			}

			// The deliverable is the translated pipeline document (result.Value) in
			// every mode — never the {value, withWarning} envelope — so the output
			// is directly reviewable and applyable via the Settings API. withWarning
			// is surfaced out-of-band above.
			if ap != nil {
				ap.SetSuggestions([]string{
					"Review the translated pipeline, then apply it with 'dtctl create settings --schema builtin:openpipeline." + scope + ".pipelines -f <file>'",
				})
				return printer.Print(result.Value)
			}

			// In an explicitly requested structured format, defer to the printer
			// (which also honors the requested format and --jq). Otherwise default
			// to indented JSON of the document rather than an unhelpful table of an
			// opaque map.
			switch outputFormat(cmdContext(cmd)) {
			case "json", "yaml", "yml", "toon":
				return printer.Print(result.Value)
			default:
				return printValueAsJSON(cmdContext(cmd), result.Value)
			}
		},
	}
	c.Flags().Bool("include-sample-data", true, "Include processor sample data in the translation")
	c.Flags().Bool("skip-disabled-rules", false, "Skip disabled rules during translation")
	c.Flags().Bool("skip-builtin-processing-rules", true, "Skip built-in processing rules during translation")
	stability.MarkStable(c)
	return c
}

// printValueAsJSON prints v as indented JSON to stdout, honoring the global
// --jq filter. Used for the default (no -o) output, where the deliverable is
// the translated pipeline document rather than a table.
func printValueAsJSON(ctx context.Context, v any) error {
	return newPrinterOpts(ctx, output.PrinterOptions{
		Format:    "json",
		Writer:    currentStdout(ctx),
		PlainMode: plainMode(ctx),
		JQFilter:  jqFilter(ctx),
	}).Print(v)
}

func init() {
}

// Declared stable: the invocation and output contract of this command is
// additive-only. Stable is never implied -- see AGENTS.md "Stability Tiers".
func init() {
}
