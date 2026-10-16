package cmd

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/dynatrace-oss/dtctl/pkg/output"
	"github.com/dynatrace-oss/dtctl/pkg/resources/appengine"
	"github.com/dynatrace-oss/dtctl/pkg/stability"
)

// describeFunctionCmd describes an app function
var describeFunctionCmd = newDescribeFunctionCmd()

func newDescribeFunctionCmd() *cobra.Command {
	c := &cobra.Command{
		Use:     "function <app-id>/<function-name>",
		Aliases: []string{"fn", "func"},
		Short:   "Describe an App Engine function",
		Long: `Show detailed information about an app function.

Functions are serverless backend functions exposed by installed apps.
Each function can be invoked using 'dtctl exec function'.

Examples:
  # Describe a function
  dtctl describe function dynatrace.automations/execute-dql-query

  # Output as JSON
  dtctl describe function dynatrace.abuseipdb/check-ip -o json

  # Output as YAML
  dtctl describe function dynatrace.slack/slack-send-message -o yaml
`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			_, c, printer, err := setup(cmdContext(cmd))
			if err != nil {
				return err
			}

			handler := appengine.NewHandler(c)

			// Get function details
			function, err := handler.GetFunction(args[0])
			if err != nil {
				return err
			}

			// For table output, show detailed information
			if outputFormat(cmdContext(cmd)) == "table" {
				const w = 14
				output.FprintDescribeKV(currentStdout(cmdContext(cmd)), "Function:", w, "%s", function.FunctionName)
				output.FprintDescribeKV(currentStdout(cmdContext(cmd)), "Full Name:", w, "%s", function.FullName)
				if function.Title != "" {
					output.FprintDescribeKV(currentStdout(cmdContext(cmd)), "Title:", w, "%s", function.Title)
				}
				if function.Description != "" {
					output.FprintDescribeKV(currentStdout(cmdContext(cmd)), "Description:", w, "%s", function.Description)
				}
				output.FprintDescribeKV(currentStdout(cmdContext(cmd)), "App:", w, "%s (%s)", function.AppName, function.AppID)
				output.FprintDescribeKV(currentStdout(cmdContext(cmd)), "Resumable:", w, "%t", function.Resumable)
				if function.Stateful {
					output.FprintDescribeKV(currentStdout(cmdContext(cmd)), "Stateful:", w, "%t", function.Stateful)
				}
				fmt.Fprintln(currentStdout(cmdContext(cmd)))
				output.FprintDescribeSection(currentStdout(cmdContext(cmd)), "Usage:")
				fmt.Fprintf(currentStdout(cmdContext(cmd)), "  dtctl exec function %s\n", function.FullName)
				if function.Resumable {
					fmt.Fprintf(currentStdout(cmdContext(cmd)), "  dtctl exec function %s --defer  # For async execution\n", function.FullName)
				}
				return nil
			}

			// For other formats, use standard printer
			return printer.Print(function)
		},
	}
	stability.MarkStable(c)
	return c
}

func init() {
	// No flags for this command
}

// Declared stable: the invocation and output contract of this command is
// additive-only. Stable is never implied -- see AGENTS.md "Stability Tiers".
func init() {
}
