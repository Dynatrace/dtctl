package cmd

import (
	"encoding/json"

	"github.com/spf13/cobra"

	"github.com/dynatrace-oss/dtctl/pkg/resources/previewprocessor"
	"github.com/dynatrace-oss/dtctl/pkg/safety"
	"github.com/dynatrace-oss/dtctl/pkg/stability"
)

// execPreviewProcessorCmd executes a processor definition against sample
// records and returns the matched/modified results.
var execPreviewProcessorCmd = newExecPreviewProcessorCmd()

func newExecPreviewProcessorCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "preview-processor",
		Args:  cobra.NoArgs,
		Short: "Preview an OpenPipeline processor definition against sample records",
		Long: `Preview an OpenPipeline processor definition against sample records.

The command runs a single processor against the sample records embedded in it
and returns the matched/modified result for each record.

The file (use "-" for stdin; there is no positional argument) is the processor
definition body itself — the same JSON shape a processor has inside a pipeline
config. It must be a complete processor definition; the endpoint validates the
full schema and rejects a partial body, e.g. a DQL processor needs at least:

  {"type":"dql","id":"preview","description":"preview","enabled":true,
   "matcher":"true","dqlScript":"fieldsAdd severity = \"INFO\"",
   "sampleData":"{\"content\":\"hello\"}"}

You do NOT wrap it in a request envelope: dtctl builds the request around it and
adds the configuration scope from --config-id. The processor body is forwarded
verbatim, so any of the endpoint's processor types are accepted.

Special error handling:
  408 - Request timed out; try reducing the number of sample records
  429 - Too many concurrent preview requests; wait and retry

Examples:
  # Preview a processor definition from a file
  dtctl exec preview-processor -f processor.json

  # Read from stdin
  cat processor.json | dtctl exec preview-processor -f -

  # Scope to a specific configuration
  dtctl exec preview-processor -f processor.json --config-id logs

  # Get structured output
  dtctl exec preview-processor -f processor.json -o json
  dtctl exec preview-processor -f processor.json -o yaml`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			fileFlag, _ := cmd.Flags().GetString("file")
			configIDFlag, _ := cmd.Flags().GetString("config-id")

			_, c, printer, err := setupWithSafetyAndPrinter(cmdContext(cmd), safety.OperationRead)
			if err != nil {
				return err
			}

			handler := previewprocessor.NewHandler(c).WithVFS(vfsEnv(cmdContext(cmd)))
			results, err := handler.Preview(fileFlag, configIDFlag)
			if err != nil {
				return err
			}

			ap := enrichAgent(printer, "exec", "preview-processor")

			// Agent mode, an explicit -o format, or a --jq filter: delegate to the
			// printer (which honors json/yaml/table/wide/csv/toon and applies jq).
			if ap != nil || cmd.Root().PersistentFlags().Lookup("output").Changed || jqFilter(cmdContext(cmd)) != "" {
				return printer.PrintList(results)
			}

			// Default (no -o): indented JSON of the results array to stdout.
			enc := json.NewEncoder(currentStdout(cmdContext(cmd)))
			enc.SetIndent("", "  ")
			return enc.Encode(results)
		},
	}
	c.Flags().StringP("file", "f", "", `read the processor definition body from a file ("-" for stdin); required`)
	c.Flags().String("config-id", "", `configuration scope, e.g. "logs"`)
	stability.MarkStable(c)
	markFlagRequiredNonEmpty(c, "file")
	return c
}

func init() {
}

// Declared stable: the invocation and output contract of this command is
// additive-only. Stable is never implied -- see AGENTS.md "Stability Tiers".
func init() {
}
