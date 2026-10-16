package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/dynatrace-oss/dtctl/pkg/client"
	"github.com/dynatrace-oss/dtctl/pkg/exec"
	"github.com/dynatrace-oss/dtctl/pkg/output"
	workflowpkg "github.com/dynatrace-oss/dtctl/pkg/resources/workflow"
	"github.com/dynatrace-oss/dtctl/pkg/safety"
	"github.com/dynatrace-oss/dtctl/pkg/stability"
)

// execWorkflowResult is the structured response for agent mode.
type execWorkflowResult struct {
	ExecutionID string                   `json:"executionId"`
	WorkflowID  string                   `json:"workflowId"`
	State       string                   `json:"state"`
	StateInfo   *string                  `json:"stateInfo,omitempty"`
	Duration    string                   `json:"duration,omitempty"`
	Tasks       []execWorkflowTaskResult `json:"tasks,omitempty"`
}

// execWorkflowTaskResult is a per-task entry in the agent response.
type execWorkflowTaskResult struct {
	Name   string `json:"name"`
	State  string `json:"state"`
	Result any    `json:"result,omitempty"`
}

type singleUseStringValue struct {
	value *string
	set   bool
}

func (v *singleUseStringValue) Set(value string) error {
	if v.set {
		return fmt.Errorf("flag can only be provided once")
	}
	v.set = true
	*v.value = value
	return nil
}

// Reset clears the "already provided" marker. The per-invocation flag reset
// (resetFlagSet) restores flags by calling Reset when a Value implements it;
// routing the reset through Set(DefValue) would trip the single-use guard
// before the real parse even starts.
func (v *singleUseStringValue) Reset() {
	v.set = false
	*v.value = ""
}

func (v *singleUseStringValue) String() string {
	if v.value == nil {
		return ""
	}
	return *v.value
}

func (v *singleUseStringValue) Type() string {
	return "string"
}

// execWorkflowCmd executes a workflow
var execWorkflowCmd = newExecWorkflowCmd()

func newExecWorkflowCmd() *cobra.Command {
	c := &cobra.Command{
		Use:     "workflow <workflow-id>",
		Aliases: []string{"wf"},
		Short:   "Execute a workflow",
		Long:    "Execute an automation workflow. Workflow input must be provided as a JSON object via --input.",
		Example: strings.Join([]string{
			"  # Execute workflow",
			"  dtctl exec workflow my-workflow-id",
			"",
			"  # Execute with workflow input",
			"  dtctl exec workflow my-workflow-id --input '{\"foo\":\"bar\", \"baz\":3}'",
			"",
			"  # Execute and wait for completion",
			"  dtctl exec workflow my-workflow-id --wait",
			"",
			"  # Execute with custom timeout",
			"  dtctl exec workflow my-workflow-id --wait --timeout 10m",
			"",
			"  # Execute, wait, and print each task's return value when done",
			"  dtctl exec workflow my-workflow-id --wait --show-results",
		}, "\n"),
		Args: cobra.ExactArgs(1),
		PreRunE: func(cmd *cobra.Command, args []string) error {
			showResults, _ := cmd.Flags().GetBool("show-results")
			wait, _ := cmd.Flags().GetBool("wait")
			if showResults && !wait {
				return fmt.Errorf("--show-results requires --wait")
			}

			_, err := buildWorkflowExecutionRequest(cmd)
			return err
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			workflowID := args[0]

			// Triggering a workflow runs actions that create and modify resources, so
			// it is gated as a create — the `exec` verb's declared operation.
			_, c, err := setupWithSafety(cmdContext(cmd), safety.OperationCreate)
			if err != nil {
				return err
			}

			executor := exec.NewWorkflowExecutor(c)

			request, err := buildWorkflowExecutionRequest(cmd)
			if err != nil {
				return err
			}
			wait, _ := cmd.Flags().GetBool("wait")
			// Simple workflow execution details are only available from the API for monitored
			// runs. The --wait path polls that API surface, so it must opt into monitor=true.
			request.Monitor = wait

			result, err := executor.Execute(workflowID, request)
			if err != nil {
				return err
			}

			// Agent mode: collect everything into a structured envelope
			printer := newPrinterCtx(cmdContext(cmd))
			ap := enrichAgent(printer, "exec", "workflow")
			if ap != nil {
				return execWorkflowAgent(cmd, c, executor, result, ap)
			}

			// Human mode: interactive output
			fmt.Fprintf(currentStdout(cmdContext(cmd)), "Workflow execution started\n")
			fmt.Fprintf(currentStdout(cmdContext(cmd)), "Execution ID: %s\n", result.ID)
			fmt.Fprintf(currentStdout(cmdContext(cmd)), "State: %s\n", result.State)

			// Handle --wait flag
			if wait {
				fmt.Fprintf(currentStdout(cmdContext(cmd)), "\nWaiting for execution to complete...\n")

				status, err := execWorkflowWait(cmd, executor, result.ID)
				if err != nil {
					return err
				}

				fmt.Fprintf(currentStdout(cmdContext(cmd)), "\nExecution completed\n")
				fmt.Fprintf(currentStdout(cmdContext(cmd)), "Final State: %s\n", status.State)
				if status.StateInfo != nil && *status.StateInfo != "" {
					fmt.Fprintf(currentStdout(cmdContext(cmd)), "State Info: %s\n", *status.StateInfo)
				}
				fmt.Fprintf(currentStdout(cmdContext(cmd)), "Duration: %s\n", formatExecutionDuration(status.Runtime))

				// Print task results if --show-results is set
				showResults, _ := cmd.Flags().GetBool("show-results")
				if showResults {
					if err := execWorkflowShowResults(cmdContext(cmd), c, result.ID, printer); err != nil {
						return err
					}
				}

				// Return error if execution failed
				if status.State == "ERROR" {
					return fmt.Errorf("workflow execution failed")
				}
			}

			return nil
		},
	}
	stability.Mark(c, stability.Experimental, pre10Since)
	registerWorkflowExecFlags(c)
	return c
}

// execWorkflowWait handles the --wait polling loop and returns the final status.
func execWorkflowWait(cmd *cobra.Command, executor *exec.WorkflowExecutor, executionID string) (*exec.ExecutionStatus, error) {
	timeout, _ := cmd.Flags().GetDuration("timeout")
	if timeout == 0 {
		timeout = 30 * time.Minute
	}

	opts := exec.WaitOptions{
		PollInterval: 2 * time.Second,
		Timeout:      timeout,
	}

	return executor.WaitForCompletion(cmdContext(cmd), executionID, opts)
}

// execWorkflowShowResults prints per-task results in human-readable format.
func execWorkflowShowResults(ctx context.Context, c *client.Client, executionID string, printer output.Printer) error {
	execHandler := workflowpkg.NewExecutionHandler(c)
	tasks, err := execHandler.ListTasks(executionID)
	if err != nil {
		return fmt.Errorf("failed to list tasks: %w", err)
	}
	if len(tasks) > 0 {
		fmt.Fprintf(currentStdout(ctx), "\nTask Results:\n")
		for _, task := range tasks {
			fmt.Fprintf(currentStdout(ctx), "\n--- %s [%s] ---\n", task.Name, task.State)
			if task.Result == nil {
				fmt.Fprintf(currentStdout(ctx), "(no structured return value)\n")
				continue
			}
			if err := printer.Print(task.Result); err != nil {
				fmt.Fprintf(currentStdout(ctx), "(failed to print result: %v)\n", err)
			}
		}
	}
	return nil
}

// execWorkflowAgent handles the entire exec workflow command in agent mode,
// producing a single structured JSON envelope.
func execWorkflowAgent(
	cmd *cobra.Command,
	c *client.Client,
	executor *exec.WorkflowExecutor,
	result *exec.WorkflowExecutionResponse,
	ap *output.AgentPrinter,
) error {
	resp := execWorkflowResult{
		ExecutionID: result.ID,
		WorkflowID:  result.Workflow,
		State:       result.State,
	}

	wait, _ := cmd.Flags().GetBool("wait")
	if wait {
		status, err := execWorkflowWait(cmd, executor, result.ID)
		if err != nil {
			return err
		}
		resp.State = status.State
		resp.StateInfo = status.StateInfo
		resp.Duration = formatExecutionDuration(status.Runtime)
		ap.SetDuration(resp.Duration)

		showResults, _ := cmd.Flags().GetBool("show-results")
		if showResults {
			execHandler := workflowpkg.NewExecutionHandler(c)
			tasks, err := execHandler.ListTasks(result.ID)
			if err != nil {
				return fmt.Errorf("failed to list tasks: %w", err)
			}
			for _, task := range tasks {
				resp.Tasks = append(resp.Tasks, execWorkflowTaskResult{
					Name:   task.Name,
					State:  task.State,
					Result: task.Result,
				})
			}
		}

		if status.State == "ERROR" {
			ap.SetWarnings([]string{"workflow execution failed"})
		}
	}

	ap.SetSuggestions([]string{
		fmt.Sprintf("Run 'dtctl get wfe %s' to view the execution details", result.ID),
		fmt.Sprintf("Run 'dtctl logs wfe %s' to view execution logs", result.ID),
	})

	return ap.Print(resp)
}

// formatExecutionDuration formats seconds into a human-readable duration
func formatExecutionDuration(seconds int) string {
	if seconds < 60 {
		return fmt.Sprintf("%ds", seconds)
	}
	if seconds < 3600 {
		m := seconds / 60
		s := seconds % 60
		if s == 0 {
			return fmt.Sprintf("%dm", m)
		}
		return fmt.Sprintf("%dm%ds", m, s)
	}
	h := seconds / 3600
	m := (seconds % 3600) / 60
	if m == 0 {
		return fmt.Sprintf("%dh", h)
	}
	return fmt.Sprintf("%dh%dm", h, m)
}

func buildWorkflowExecutionRequest(cmd *cobra.Command) (exec.WorkflowExecutionRequest, error) {
	inputJSONValue, _ := cmd.Flags().GetString("input")
	inputJSONValues := []string{}
	if cmd.Flags().Lookup("input").Changed {
		inputJSONValues = append(inputJSONValues, inputJSONValue)
	}
	paramStrings, _ := cmd.Flags().GetStringSlice("params")

	return buildWorkflowExecutionRequestFromValues(inputJSONValues, paramStrings)
}

func buildWorkflowExecutionRequestFromValues(inputJSONValues []string, paramStrings []string) (exec.WorkflowExecutionRequest, error) {
	request := exec.WorkflowExecutionRequest{}

	if len(inputJSONValues) > 1 {
		return request, fmt.Errorf("--input may only be provided once")
	}
	if len(inputJSONValues) == 1 {
		input, err := parseWorkflowInputJSON(inputJSONValues[0])
		if err != nil {
			return request, err
		}
		request.Input = input
	}

	params, err := exec.ParseParams(paramStrings)
	if err != nil {
		return request, err
	}
	if len(params) > 0 {
		request.Params = make(map[string]any, len(params))
		for key, value := range params {
			request.Params[key] = value
		}
	}

	return request, nil
}

func parseWorkflowInputJSON(raw string) (map[string]any, error) {
	if strings.TrimSpace(raw) == "" {
		return nil, fmt.Errorf("--input must not be empty")
	}

	var parsed any
	if err := json.Unmarshal([]byte(raw), &parsed); err != nil {
		return nil, fmt.Errorf("invalid value for --input: %w", err)
	}

	input, ok := parsed.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("--input must be a JSON object")
	}

	return input, nil
}

func registerWorkflowExecFlags(cmd *cobra.Command) {
	var inputJSON string
	cmd.Flags().Var(&singleUseStringValue{value: &inputJSON}, "input", "workflow input as a JSON object")
	cmd.Flags().StringSlice("params", []string{}, "workflow parameters (key=value)")
	cmd.Flags().Bool("wait", false, "wait for workflow execution to complete")
	cmd.Flags().Duration("timeout", 30*time.Minute, "timeout when waiting for completion")
	cmd.Flags().Bool("show-results", false, "print the result of each task after execution completes (requires --wait)")
	_ = cmd.Flags().MarkDeprecated("params", "It targets legacy execution metadata. Workflow input must be provided as a JSON object via --input.")
	_ = cmd.Flags().MarkHidden("params")
}
