package cmd

import (
	"github.com/spf13/cobra"

	"github.com/dynatrace-oss/dtctl/pkg/stability"
)

// execCmd represents the exec command
var execCmd = newExecCmd()

func newExecCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "exec",
		Short: "Execute queries, workflows, or functions",
		Long: `Execute operations on the Dynatrace platform: run workflows, invoke
serverless functions, evaluate SLOs, chat with Davis CoPilot, or preview
OpenPipeline processor definitions against sample records.

For DQL queries, use 'dtctl query' instead (exec dql is deprecated).

Available operations:
  workflow (wf)           Trigger a workflow execution and poll for results
  function (fn, func)     Invoke an app function or run ad-hoc JavaScript
  analyzer (az)           Run a Davis AI analyzer
  slo                     Evaluate a service-level objective
  copilot (cp, chat)      Chat with Davis CoPilot interactively
  preview-processor       Preview an OpenPipeline processor against sample records`,
		Example: `  # Execute a workflow and wait for completion
  dtctl exec workflow <workflow-id>

  # Execute a workflow with JSON input
  dtctl exec workflow <workflow-id> --input '{"foo":"bar", "baz":3}'

  # Invoke a serverless function
  dtctl exec function <app-id>/<function-name>

  # Evaluate an SLO
  dtctl exec slo <slo-id>

  # Chat with Davis CoPilot
  dtctl exec copilot "What happened in the last hour?"

  # Preview a processor definition against sample records
  dtctl exec preview-processor -f processor.json`,
		RunE: requireSubcommand,
	}
	stability.MarkStable(c)
	return c
}

func init() {
	rootCmd.AddCommand(execCmd)

	execCmd.AddCommand(execDQLCmd)
	execCmd.AddCommand(execWorkflowCmd)
	execCmd.AddCommand(execFunctionCmd)
	execCmd.AddCommand(execAnalyzerCmd)
	execCmd.AddCommand(execCopilotCmd)
	execCmd.AddCommand(execSLOCmd)
	execCmd.AddCommand(execPreviewProcessorCmd)
	execCmd.AddCommand(execAPICmd)
}

// Declared stable: the invocation and output contract of this command is
// additive-only. Stable is never implied -- see AGENTS.md "Stability Tiers".
func init() {
}
