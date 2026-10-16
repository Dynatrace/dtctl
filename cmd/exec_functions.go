package cmd

import (
	"github.com/spf13/cobra"

	"github.com/dynatrace-oss/dtctl/pkg/exec"
	"github.com/dynatrace-oss/dtctl/pkg/safety"
	"github.com/dynatrace-oss/dtctl/pkg/stability"
)

// execFunctionCmd executes an app function or ad-hoc code
var execFunctionCmd = newExecFunctionCmd()

func newExecFunctionCmd() *cobra.Command {
	c := &cobra.Command{
		Use:     "function [app-id/function-name]",
		Aliases: []string{"fn", "func"},
		Short:   "Execute an app function or ad-hoc JavaScript code",
		Long: `Execute a function from an installed app or run ad-hoc JavaScript code.

App Function Execution:
  Execute a function from an installed app by providing the app ID and function name.

Ad-hoc Code Execution:
  Execute JavaScript code directly without deploying an app.

Examples:
  # Execute an app function (GET)
  dtctl exec function myapp/myfunction

  # Execute with POST and payload
  dtctl exec function myapp/myfunction --method POST --payload '{"key":"value"}'

  # Execute with payload from file (use - to read the payload from stdin)
  dtctl exec function myapp/myfunction --method POST --data payload.json

  # Defer execution (async, for resumable functions)
  dtctl exec function myapp/myfunction --defer

  # Execute ad-hoc JavaScript code
  dtctl exec function --code 'export default async function() { return "hello" }'

  # Execute JavaScript from file
  dtctl exec function -f script.js

  # Execute with payload
  dtctl exec function -f script.js --payload '{"input":"data"}'
`,
		RunE: func(cmd *cobra.Command, args []string) error {
			// Get flags
			method, _ := cmd.Flags().GetString("method")
			payload, _ := cmd.Flags().GetString("payload")
			payloadFile, _ := cmd.Flags().GetString("data")
			sourceCode, _ := cmd.Flags().GetString("code")
			sourceCodeFile, _ := cmd.Flags().GetString("file")
			defer_, _ := cmd.Flags().GetBool("defer")

			// Ad-hoc JavaScript is unclassifiable by construction: AppEngine injects a
			// bearer on relative fetch(), so the code can POST or DELETE against any
			// platform API the token reaches. It is gated as a delete — the strictest
			// generally-reachable operation — for the same reason `exec api` escalates a
			// request whose operation it cannot resolve. Gating it any lower would make
			// that gate theatre, since a caller blocked on `exec api` could reach the
			// same endpoint through --code. A *named* app function runs the app's own
			// reviewed code and is gated as a create, matching the `exec` verb.
			op := safety.OperationCreate
			if sourceCode != "" || sourceCodeFile != "" {
				op = safety.OperationDelete
			}

			_, c, err := setupWithSafety(cmdContext(cmd), op)
			if err != nil {
				return err
			}

			executor := newFunctionExecutor(cmdContext(cmd), c)

			opts := exec.FunctionExecuteOptions{
				Method:         method,
				Payload:        payload,
				PayloadFile:    payloadFile,
				SourceCode:     sourceCode,
				SourceCodeFile: sourceCodeFile,
				Defer:          defer_,
			}

			// Parse function reference from args if provided
			if len(args) > 0 {
				opts.FunctionName = args[0]
			}

			// Execute the function
			result, err := executor.Execute(opts)
			if err != nil {
				return err
			}

			// Handle different result types and print
			printer := newPrinterCtx(cmdContext(cmd))
			return printer.Print(result)
		},
	}
	c.Flags().String("method", "GET", "HTTP method for app function (GET, POST, PUT, PATCH, DELETE)")
	c.Flags().String("payload", "", "request payload (JSON string)")
	c.Flags().String("data", "", "read payload from file (or - for stdin)")
	c.Flags().String("code", "", "JavaScript code to execute (for ad-hoc execution)")
	c.Flags().StringP("file", "f", "", "read JavaScript code from file (for ad-hoc execution)")
	c.Flags().Bool("defer", false, "defer execution (async, for resumable functions)")
	stability.MarkStable(c)
	return c
}

func init() {
	// Function flags
}

// Declared stable: the invocation and output contract of this command is
// additive-only. Stable is never implied -- see AGENTS.md "Stability Tiers".
func init() {
}
