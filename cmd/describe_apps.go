package cmd

import (
	"strings"

	"github.com/spf13/cobra"

	"github.com/dynatrace-oss/dtctl/pkg/output"
	"github.com/dynatrace-oss/dtctl/pkg/resources/appengine"
	"github.com/dynatrace-oss/dtctl/pkg/stability"
)

// describeAppCmd shows detailed info about an app
var describeAppCmd = newDescribeAppCmd()

func newDescribeAppCmd() *cobra.Command {
	c := &cobra.Command{
		Use:     "app <app-id>",
		Aliases: []string{"apps"},
		Short:   "Show details of an App Engine app",
		Long: `Show detailed information about an App Engine app.

Examples:
  # Describe an app
  dtctl describe app my.custom-app
`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			appID := args[0]

			_, c, printer, err := setup(cmdContext(cmd))
			if err != nil {
				return err
			}

			handler := appengine.NewHandler(c)

			app, err := handler.GetApp(appID)
			if err != nil {
				return err
			}

			// For table output, show detailed human-readable information
			if outputFormat(cmdContext(cmd)) == "table" {
				const w = 13
				output.FprintDescribeKV(currentStdout(cmdContext(cmd)), "ID:", w, "%s", app.ID)
				output.FprintDescribeKV(currentStdout(cmdContext(cmd)), "Name:", w, "%s", app.Name)
				output.FprintDescribeKV(currentStdout(cmdContext(cmd)), "Version:", w, "%s", app.Version)
				output.FprintDescribeKV(currentStdout(cmdContext(cmd)), "Description:", w, "%s", app.Description)
				output.FprintDescribeKV(currentStdout(cmdContext(cmd)), "Builtin:", w, "%v", app.IsBuiltin)

				if app.ResourceStatus != nil {
					output.FprintDescribeKV(currentStdout(cmdContext(cmd)), "Status:", w, "%s", app.ResourceStatus.Status)
					if len(app.ResourceStatus.SubResourceTypes) > 0 {
						output.FprintDescribeKV(currentStdout(cmdContext(cmd)), "Resources:", w, "%s", strings.Join(app.ResourceStatus.SubResourceTypes, ", "))
					}
				}

				if app.ModificationInfo != nil {
					if app.ModificationInfo.CreatedAt != "" {
						output.FprintDescribeKV(currentStdout(cmdContext(cmd)), "Created:", w, "%s (by %s)", app.ModificationInfo.CreatedAt, app.ModificationInfo.CreatedBy)
					}
					if app.ModificationInfo.LastModifiedAt != "" {
						output.FprintDescribeKV(currentStdout(cmdContext(cmd)), "Modified:", w, "%s (by %s)", app.ModificationInfo.LastModifiedAt, app.ModificationInfo.LastModifiedBy)
					}
				}

				return nil
			}

			// For other formats, use standard printer
			enrichAgent(printer, "describe", "app")
			return printer.Print(app)
		},
	}
	stability.MarkStable(c)
	return c
}

// Declared stable: the invocation and output contract of this command is
// additive-only. Stable is never implied -- see AGENTS.md "Stability Tiers".
func init() {
}
