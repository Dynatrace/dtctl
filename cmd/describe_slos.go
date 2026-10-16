package cmd

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/dynatrace-oss/dtctl/pkg/output"
	"github.com/dynatrace-oss/dtctl/pkg/resources/slo"
	"github.com/dynatrace-oss/dtctl/pkg/stability"
)

// describeSLOCmd shows detailed info about an SLO
var describeSLOCmd = newDescribeSLOCmd()

func newDescribeSLOCmd() *cobra.Command {
	c := &cobra.Command{
		Use:     "slo <slo-id>",
		Aliases: []string{},
		Short:   "Show details of a service-level objective",
		Long: `Show detailed information about a service-level objective including criteria, tags, and metadata.

Examples:
  # Describe an SLO by ID
  dtctl describe slo <slo-id>
`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			sloID := args[0]

			_, c, printer, err := setup(cmdContext(cmd))
			if err != nil {
				return err
			}

			handler := slo.NewHandler(c)

			// Get SLO details
			s, err := handler.Get(sloID)
			if err != nil {
				return err
			}

			// For table output, show detailed human-readable information
			if outputFormat(cmdContext(cmd)) == "table" {
				const w = 13
				output.FprintDescribeKV(currentStdout(cmdContext(cmd)), "ID:", w, "%s", s.ID)
				output.FprintDescribeKV(currentStdout(cmdContext(cmd)), "Name:", w, "%s", s.Name)
				if s.Description != "" {
					output.FprintDescribeKV(currentStdout(cmdContext(cmd)), "Description:", w, "%s", s.Description)
				}
				if s.ExternalID != "" {
					output.FprintDescribeKV(currentStdout(cmdContext(cmd)), "External ID:", w, "%s", s.ExternalID)
				}
				if s.Version != "" {
					if ts := slo.DecodeVersionTimestamp(s.Version); ts != nil {
						output.FprintDescribeKV(currentStdout(cmdContext(cmd)), "Modified:", w, "%s", ts.Format("2006-01-02 15:04:05 UTC"))
					}
				}

				// Print tags
				if len(s.Tags) > 0 {
					output.FprintDescribeKV(currentStdout(cmdContext(cmd)), "Tags:", w, "%s", strings.Join(s.Tags, ", "))
				}

				// Print criteria
				if len(s.Criteria) > 0 {
					fmt.Fprintln(currentStdout(cmdContext(cmd)))
					output.FprintDescribeSection(currentStdout(cmdContext(cmd)), "Criteria:")
					for _, c := range s.Criteria {
						timeframe := c.TimeframeFrom
						if c.TimeframeTo != "" {
							timeframe = fmt.Sprintf("%s to %s", c.TimeframeFrom, c.TimeframeTo)
						}
						fmt.Fprintf(currentStdout(cmdContext(cmd)), "  - Timeframe: %s\n", timeframe)
						fmt.Fprintf(currentStdout(cmdContext(cmd)), "    Target:    %.2f%%\n", c.Target)
						if c.Warning != nil {
							fmt.Fprintf(currentStdout(cmdContext(cmd)), "    Warning:   %.2f%%\n", *c.Warning)
						}
					}
				}

				// Print custom SLI if present
				if len(s.CustomSli) > 0 {
					fmt.Fprintln(currentStdout(cmdContext(cmd)))
					output.FprintDescribeSection(currentStdout(cmdContext(cmd)), "Custom SLI:")
					sliJSON, err := json.MarshalIndent(s.CustomSli, "  ", "  ")
					if err == nil {
						fmt.Fprintf(currentStdout(cmdContext(cmd)), "  %s\n", string(sliJSON))
					}
				}

				return nil
			}

			// For other formats, use standard printer
			enrichAgent(printer, "describe", "slo")
			return printer.Print(s)
		},
	}
	stability.MarkStable(c)
	return c
}

// Declared stable: the invocation and output contract of this command is
// additive-only. Stable is never implied -- see AGENTS.md "Stability Tiers".
func init() {
}
