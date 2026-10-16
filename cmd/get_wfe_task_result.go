package cmd

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/dynatrace-oss/dtctl/pkg/resources/workflow"
	"github.com/dynatrace-oss/dtctl/pkg/stability"
)

// getWfeTaskResultCmd retrieves the structured return value of a workflow execution task
var getWfeTaskResultCmd = newGetWfeTaskResultCmd()

func newGetWfeTaskResultCmd() *cobra.Command {
	c := &cobra.Command{
		Use:     "wfe-task-result <execution-id>",
		Aliases: []string{"workflow-execution-task-result"},
		Short:   "Get the return value of a workflow execution task",
		Long: `Get the structured return value produced by a task in a workflow execution.

Unlike 'dtctl logs wfe' (stdout/stderr), this retrieves the data returned by the
task (e.g. the object from a JavaScript task's default export function).`,
		Example: `  # Get the return value of a specific task
  dtctl get wfe-task-result <execution-id> --task <task-name>
  dtctl get wfe-task-result <execution-id> -t <task-name> -o json`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			executionID := args[0]

			taskName, _ := cmd.Flags().GetString("task")

			_, c, printer, err := setup(cmdContext(cmd))
			if err != nil {
				return err
			}

			handler := workflow.NewExecutionHandler(c)
			result, err := handler.GetTaskResult(executionID, taskName)
			if err != nil {
				return err
			}
			ap := enrichAgent(printer, "get", "wfe-task-result")
			if ap != nil {
				ap.SetSuggestions([]string{
					fmt.Sprintf("Run 'dtctl get wfe %s' to view the full execution", executionID),
					fmt.Sprintf("Run 'dtctl logs wfe %s --task %s' to view task logs", executionID, taskName),
				})
			}

			return printer.Print(result)
		},
	}
	c.Flags().StringP("task", "t", "", "Task name to retrieve the result for (required)")
	stability.MarkStable(c)
	markFlagRequiredNonEmpty(c, "task")
	return c
}

// Declared stable: the invocation and output contract of this command is
// additive-only. Stable is never implied -- see AGENTS.md "Stability Tiers".
