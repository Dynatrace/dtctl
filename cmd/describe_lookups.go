package cmd

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/dynatrace-oss/dtctl/pkg/output"
	"github.com/dynatrace-oss/dtctl/pkg/resources/lookup"
	"github.com/dynatrace-oss/dtctl/pkg/stability"
)

// describeLookupCmd shows detailed info about a lookup table
var describeLookupCmd = newDescribeLookupCmd()

func newDescribeLookupCmd() *cobra.Command {
	c := &cobra.Command{
		Use:     "lookup <path>",
		Aliases: []string{"lookups", "lkup", "lu"},
		Short:   "Show details of a lookup table",
		Long: `Show detailed information about a lookup table including metadata and data preview.

Examples:
  # Describe a lookup table
  dtctl describe lookup /lookups/grail/pm/error_codes

  # Output as JSON
  dtctl describe lookup /lookups/grail/pm/error_codes -o json
`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			path := args[0]

			_, c, printer, err := setup(cmdContext(cmd))
			if err != nil {
				return err
			}

			handler := lookup.NewHandler(c)

			// Get lookup metadata
			lu, err := handler.Get(path)
			if err != nil {
				return err
			}

			// Get preview data (first 5 rows)
			dataResult, err := handler.GetData(path, 5)
			if err != nil {
				return err
			}

			// For structured formats, use printer
			if outputFormat(cmdContext(cmd)) != "table" {
				enrichAgent(printer, "describe", "lookup")
				lookupData := struct {
					*lookup.Lookup
					PreviewData []map[string]interface{} `json:"previewData"`
				}{
					Lookup:      lu,
					PreviewData: dataResult.Records,
				}
				return printer.Print(lookupData)
			}

			// Print lookup details
			const w = 14
			output.FprintDescribeKV(currentStdout(cmdContext(cmd)), "Path:", w, "%s", lu.Path)
			if lu.DisplayName != "" {
				output.FprintDescribeKV(currentStdout(cmdContext(cmd)), "Display Name:", w, "%s", lu.DisplayName)
			}
			if lu.Description != "" {
				output.FprintDescribeKV(currentStdout(cmdContext(cmd)), "Description:", w, "%s", lu.Description)
			}
			if lu.FileSize > 0 {
				output.FprintDescribeKV(currentStdout(cmdContext(cmd)), "File Size:", w, "%s", formatBytes(lu.FileSize))
			}
			if lu.Records > 0 {
				output.FprintDescribeKV(currentStdout(cmdContext(cmd)), "Records:", w, "%d", lu.Records)
			}
			if lu.LookupField != "" {
				output.FprintDescribeKV(currentStdout(cmdContext(cmd)), "Lookup Field:", w, "%s", lu.LookupField)
			}
			if len(lu.Columns) > 0 {
				output.FprintDescribeKV(currentStdout(cmdContext(cmd)), "Columns:", w, "%s", strings.Join(lu.Columns, ", "))
			}
			if lu.Modified != "" {
				output.FprintDescribeKV(currentStdout(cmdContext(cmd)), "Modified:", w, "%s", lu.Modified)
			}

			// Print data preview
			if len(dataResult.Records) > 0 {
				fmt.Fprintln(currentStdout(cmdContext(cmd)))
				output.FprintDescribeSection(currentStdout(cmdContext(cmd)), fmt.Sprintf("Data Preview (first %d rows):", len(dataResult.Records)))

				// Create table header
				if len(lu.Columns) > 0 {
					fmt.Fprintln(currentStdout(cmdContext(cmd)), strings.Join(lu.Columns, "\t"))
				}

				// Print rows
				for _, row := range dataResult.Records {
					var values []string
					for _, col := range lu.Columns {
						val := fmt.Sprintf("%v", row[col])
						values = append(values, val)
					}
					fmt.Fprintln(currentStdout(cmdContext(cmd)), strings.Join(values, "\t"))
				}
			}

			return nil
		},
	}
	stability.MarkStable(c)
	return c
}

// Declared stable: the invocation and output contract of this command is
// additive-only. Stable is never implied -- see AGENTS.md "Stability Tiers".
func init() {
}
