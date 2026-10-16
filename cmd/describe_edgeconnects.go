package cmd

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/dynatrace-oss/dtctl/pkg/output"
	"github.com/dynatrace-oss/dtctl/pkg/resources/edgeconnect"
	"github.com/dynatrace-oss/dtctl/pkg/stability"
)

// describeEdgeConnectCmd shows detailed info about an EdgeConnect
var describeEdgeConnectCmd = newDescribeEdgeConnectCmd()

func newDescribeEdgeConnectCmd() *cobra.Command {
	c := &cobra.Command{
		Use:     "edgeconnect <id>",
		Aliases: []string{"ec"},
		Short:   "Show details of an EdgeConnect configuration",
		Long: `Show detailed information about an EdgeConnect configuration.

Examples:
  # Describe an EdgeConnect
  dtctl describe edgeconnect <id>
  dtctl describe ec <id>
`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ecID := args[0]

			_, c, printer, err := setup(cmdContext(cmd))
			if err != nil {
				return err
			}

			handler := edgeconnect.NewHandler(c)

			ec, err := handler.Get(ecID)
			if err != nil {
				return err
			}

			// For table output, show detailed human-readable information
			if outputFormat(cmdContext(cmd)) == "table" {
				const w = 10
				output.FprintDescribeKV(currentStdout(cmdContext(cmd)), "ID:", w, "%s", ec.ID)
				output.FprintDescribeKV(currentStdout(cmdContext(cmd)), "Name:", w, "%s", ec.Name)
				output.FprintDescribeKV(currentStdout(cmdContext(cmd)), "Managed:", w, "%v", ec.ManagedByDynatraceOperator)

				if len(ec.HostPatterns) > 0 {
					fmt.Fprintln(currentStdout(cmdContext(cmd)))
					output.FprintDescribeSection(currentStdout(cmdContext(cmd)), "Host Patterns:")
					for _, pattern := range ec.HostPatterns {
						fmt.Fprintf(currentStdout(cmdContext(cmd)), "  - %s\n", pattern)
					}
				}

				if ec.OAuthClientID != "" {
					fmt.Fprintln(currentStdout(cmdContext(cmd)))
					output.FprintDescribeKV(currentStdout(cmdContext(cmd)), "OAuth Client ID:", 0, "%s", ec.OAuthClientID)
				}

				if ec.ModificationInfo != nil {
					fmt.Fprintln(currentStdout(cmdContext(cmd)))
					if ec.ModificationInfo.CreatedTime != "" {
						output.FprintDescribeKV(currentStdout(cmdContext(cmd)), "Created:", w, "%s (by %s)", ec.ModificationInfo.CreatedTime, ec.ModificationInfo.CreatedBy)
					}
					if ec.ModificationInfo.LastModifiedTime != "" {
						output.FprintDescribeKV(currentStdout(cmdContext(cmd)), "Modified:", w, "%s (by %s)", ec.ModificationInfo.LastModifiedTime, ec.ModificationInfo.LastModifiedBy)
					}
				}

				return nil
			}

			// For other formats, use standard printer
			enrichAgent(printer, "describe", "edgeconnect")
			return printer.Print(ec)
		},
	}
	stability.MarkStable(c)
	return c
}

// Declared stable: the invocation and output contract of this command is
// additive-only. Stable is never implied -- see AGENTS.md "Stability Tiers".
func init() {
}
