package cmd

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/spf13/cobra"

	"github.com/dynatrace-oss/dtctl/pkg/output"
	"github.com/dynatrace-oss/dtctl/pkg/resources/workflow"
	"github.com/dynatrace-oss/dtctl/pkg/stability"
)

// describeWorkflowCmd shows detailed info about a workflow
var describeWorkflowCmd = newDescribeWorkflowCmd()

func newDescribeWorkflowCmd() *cobra.Command {
	c := &cobra.Command{
		Use:     "workflow <workflow-id>",
		Aliases: []string{"wf"},
		Short:   "Show details of a workflow",
		Long: `Show detailed information about a workflow including triggers, tasks, and recent executions.

Examples:
  # Describe a workflow
  dtctl describe workflow <workflow-id>
  dtctl describe wf <workflow-id>
`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			workflowID := args[0]

			_, c, printer, err := setup(cmdContext(cmd))
			if err != nil {
				return err
			}

			handler := workflow.NewHandler(c)
			execHandler := workflow.NewExecutionHandler(c)

			// Get workflow details
			wf, err := handler.Get(workflowID)
			if err != nil {
				return err
			}

			// For table output, show detailed human-readable information
			if outputFormat(cmdContext(cmd)) == "table" {
				execList, err := execHandler.List(workflow.ExecutionFilters{WorkflowID: workflowID}, 10)
				if err != nil {
					execList = nil
				}
				printWorkflowDescribeTable(currentStdout(cmdContext(cmd)), wf, execList)

				return nil
			}

			// For other formats, use standard printer
			enrichAgent(printer, "describe", "workflow")
			return printer.Print(wf)
		},
	}
	stability.MarkStable(c)
	return c
}

func printWorkflowDescribeTable(w io.Writer, wf *workflow.Workflow, execList *workflow.ExecutionList) {
	const kw = 13
	output.FprintDescribeKV(w, "ID:", kw, "%s", wf.ID)
	output.FprintDescribeKV(w, "Title:", kw, "%s", wf.Title)
	if wf.Description != "" {
		output.FprintDescribeKV(w, "Description:", kw, "%s", wf.Description)
	}
	output.FprintDescribeKV(w, "Owner:", kw, "%s (%s)", wf.Owner, wf.OwnerType)
	output.FprintDescribeKV(w, "Private:", kw, "%v", wf.Private)
	output.FprintDescribeKV(w, "Deployed:", kw, "%v", wf.IsDeployed)
	output.FprintDescribeKV(w, "Type:", kw, "%s", wf.Type)

	if summary := triggerSummary(wf.Trigger); summary != "" {
		output.FprintDescribeKV(w, "Trigger:", kw, "%s", summary)
	}

	printWorkflowDefinitionFields(w, kw, wf)

	if len(wf.Tasks) > 0 {
		fmt.Fprintln(w)
		output.FprintDescribeSection(w, "Tasks:")
		for name, task := range wf.Tasks {
			taskMap, ok := task.(map[string]interface{})
			if ok {
				action := ""
				if a, exists := taskMap["action"]; exists {
					action = fmt.Sprintf("%v", a)
				}
				fmt.Fprintf(w, "  - %s", name)
				if action != "" {
					fmt.Fprintf(w, " (%s)", action)
				}
				fmt.Fprintln(w)
			} else {
				fmt.Fprintf(w, "  - %s\n", name)
			}
		}
	}

	if execList == nil || execList.Count == 0 {
		return
	}

	fmt.Fprintln(w)
	output.FprintDescribeSection(w, "Recent Executions:")

	limit := 5
	if execList.Count < limit {
		limit = execList.Count
	}

	for i := 0; i < limit; i++ {
		exec := execList.Results[i]
		fmt.Fprintf(w, "  - %s  %-10s  %s  %s\n",
			exec.ID[:8]+"...",
			exec.State,
			exec.StartedAt.Format("2006-01-02 15:04"),
			formatDuration(exec.Runtime))
	}

	if execList.Count > limit {
		fmt.Fprintf(w, "  ... and %d more\n", execList.Count-limit)
	}
}

func printWorkflowDefinitionFields(w io.Writer, width int, wf *workflow.Workflow) {
	if wf.Result != nil && *wf.Result != "" {
		fmt.Fprintln(w)
		output.FprintDescribeKV(w, "Result:", width, "%s", *wf.Result)
	}

	if len(wf.Input) == 0 {
		return
	}

	inputJSON, err := json.MarshalIndent(wf.Input, "  ", "  ")
	if err != nil {
		return
	}

	fmt.Fprintln(w)
	output.FprintDescribeSection(w, "Input:")
	fmt.Fprintf(w, "  %s\n", string(inputJSON))
}

func triggerSummary(trigger map[string]interface{}) string {
	if _, ok := trigger["schedule"]; ok {
		return formatTriggerSummary("schedule", nestedTriggerString(trigger, "schedule", "trigger", "type"))
	}
	if _, ok := trigger["eventTrigger"]; ok {
		return formatTriggerSummary("eventTrigger", nestedTriggerString(trigger, "eventTrigger", "triggerConfiguration", "type"))
	}
	return ""
}

func formatTriggerSummary(familyKey, subtype string) string {
	family := strings.TrimSuffix(familyKey, "Trigger")
	if family == "" {
		return ""
	}
	family = strings.ToUpper(family[:1]) + family[1:]
	if subtype == "" {
		return family
	}
	return fmt.Sprintf("%s (%s)", family, subtype)
}

func nestedTriggerString(root map[string]interface{}, path ...string) string {
	current := any(root)
	for _, part := range path {
		currentMap, ok := current.(map[string]interface{})
		if !ok {
			return ""
		}
		current, ok = currentMap[part]
		if !ok || current == nil {
			return ""
		}
	}
	value, ok := current.(string)
	if !ok {
		return ""
	}
	return value
}

// describeWorkflowExecutionCmd shows detailed info about a workflow execution
var describeWorkflowExecutionCmd = newDescribeWorkflowExecutionCmd()

func newDescribeWorkflowExecutionCmd() *cobra.Command {
	c := &cobra.Command{
		Use:     "workflow-execution <execution-id>",
		Aliases: []string{"wfe"},
		Short:   "Show details of a workflow execution",
		Long: `Show detailed information about a workflow execution including task states.

Examples:
  # Describe a workflow execution
  dtctl describe workflow-execution <execution-id>
  dtctl describe wfe <execution-id>
`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			executionID := args[0]

			_, c, printer, err := setup(cmdContext(cmd))
			if err != nil {
				return err
			}

			handler := workflow.NewExecutionHandler(c)

			// Get execution details
			exec, err := handler.Get(executionID)
			if err != nil {
				return err
			}

			// Get task executions
			tasks, err := handler.ListTasks(executionID)
			if err != nil {
				return err
			}

			// For table output, show detailed human-readable information
			if outputFormat(cmdContext(cmd)) == "table" {
				const w = 12
				output.FprintDescribeKV(currentStdout(cmdContext(cmd)), "ID:", w, "%s", exec.ID)
				output.FprintDescribeKV(currentStdout(cmdContext(cmd)), "Workflow:", w, "%s", exec.Workflow)
				output.FprintDescribeKV(currentStdout(cmdContext(cmd)), "Title:", w, "%s", exec.Title)
				output.FprintDescribeKV(currentStdout(cmdContext(cmd)), "State:", w, "%s", exec.State)
				output.FprintDescribeKV(currentStdout(cmdContext(cmd)), "Started:", w, "%s", exec.StartedAt.Format("2006-01-02 15:04:05"))
				if exec.EndedAt != nil {
					output.FprintDescribeKV(currentStdout(cmdContext(cmd)), "Ended:", w, "%s", exec.EndedAt.Format("2006-01-02 15:04:05"))
				}
				output.FprintDescribeKV(currentStdout(cmdContext(cmd)), "Duration:", w, "%s", formatDuration(exec.Runtime))
				output.FprintDescribeKV(currentStdout(cmdContext(cmd)), "Trigger:", w, "%s", exec.TriggerType)
				if exec.StateInfo != nil && *exec.StateInfo != "" {
					output.FprintDescribeKV(currentStdout(cmdContext(cmd)), "State Info:", w, "%s", *exec.StateInfo)
				}

				// Print tasks table
				if len(tasks) > 0 {
					fmt.Fprintln(currentStdout(cmdContext(cmd)))
					output.FprintDescribeSection(currentStdout(cmdContext(cmd)), "Tasks:")

					// Find max name length for alignment
					maxNameLen := 4 // "NAME"
					for _, t := range tasks {
						if len(t.Name) > maxNameLen {
							maxNameLen = len(t.Name)
						}
					}

					// Print header
					fmt.Fprintf(currentStdout(cmdContext(cmd)), "  %-*s  %-10s  %s\n", maxNameLen, "NAME", "STATE", "DURATION")

					// Print tasks
					for _, t := range tasks {
						duration := formatDuration(t.Runtime)
						fmt.Fprintf(currentStdout(cmdContext(cmd)), "  %-*s  %-10s  %s\n", maxNameLen, t.Name, t.State, duration)
					}
				}

				return nil
			}

			// For other formats, use standard printer
			enrichAgent(printer, "describe", "workflow-execution")
			return printer.Print(exec)
		},
	}
	stability.MarkStable(c)
	return c
}

// Declared stable: the invocation and output contract of these commands is
// additive-only. Stable is never implied -- see AGENTS.md "Stability Tiers".
func init() {
}
