package workflow

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/dynatrace-oss/dtctl/pkg/client"
	sdkworkflow "github.com/dynatrace-oss/dtctl/sdk/api/workflow"
	"github.com/dynatrace-oss/dtctl/sdk/httpclient"
)

// Re-export SDK types that have no table tags.
type TaskExecutionMap = sdkworkflow.TaskExecutionMap

// Execution represents a workflow execution (CLI version with table tags).
type Execution struct {
	ID          string     `json:"id" table:"ID"`
	Workflow    string     `json:"workflow" table:"WORKFLOW"`
	Title       string     `json:"title" table:"TITLE"`
	State       string     `json:"state" table:"STATE"`
	StateInfo   *string    `json:"stateInfo,omitempty" table:"-"`
	StartedAt   time.Time  `json:"startedAt" table:"STARTED"`
	EndedAt     *time.Time `json:"endedAt,omitempty" table:"-"`
	Runtime     int        `json:"runtime,omitempty" table:"RUNTIME"`
	Trigger     *string    `json:"trigger,omitempty" table:"-"`
	TriggerType string     `json:"triggerType,omitempty" table:"TRIGGER"`
	User        *string    `json:"user,omitempty" table:"-"`
	Actor       string     `json:"actor,omitempty" table:"-"`
	Input       any        `json:"input,omitempty" table:"-"`
	Params      any        `json:"params,omitempty" table:"-"`
	Result      any        `json:"result,omitempty" table:"-"`
}

// ExecutionList represents a list of executions.
type ExecutionList struct {
	Count   int         `json:"count"`
	Results []Execution `json:"results"`
}

// TaskExecution represents a task execution within a workflow execution (CLI version with table tags).
type TaskExecution struct {
	ID        string     `json:"id" table:"ID"`
	Name      string     `json:"name" table:"NAME"`
	State     string     `json:"state" table:"STATE"`
	StartedAt *time.Time `json:"startedAt,omitempty" table:"STARTED"`
	EndedAt   *time.Time `json:"endedAt,omitempty" table:"-"`
	Runtime   int        `json:"runtime,omitempty" table:"RUNTIME"`
	StateInfo *string    `json:"stateInfo,omitempty" table:"-"`
	Input     any        `json:"input,omitempty" table:"-"`
	Result    any        `json:"result,omitempty" table:"-"`
}

// fromSDKExecution converts an SDK Execution to a CLI Execution.
func fromSDKExecution(s *sdkworkflow.Execution) Execution {
	return Execution{
		ID:          s.ID,
		Workflow:    s.Workflow,
		Title:       s.Title,
		State:       s.State,
		StateInfo:   s.StateInfo,
		StartedAt:   s.StartedAt,
		EndedAt:     s.EndedAt,
		Runtime:     s.Runtime,
		Trigger:     s.Trigger,
		TriggerType: s.TriggerType,
		User:        s.User,
		Actor:       s.Actor,
		Input:       s.Input,
		Params:      s.Params,
		Result:      s.Result,
	}
}

// fromSDKTaskExecution converts an SDK TaskExecution to a CLI TaskExecution.
func fromSDKTaskExecution(s *sdkworkflow.TaskExecution) TaskExecution {
	return TaskExecution{
		ID:        s.ID,
		Name:      s.Name,
		State:     s.State,
		StartedAt: s.StartedAt,
		EndedAt:   s.EndedAt,
		Runtime:   s.Runtime,
		StateInfo: s.StateInfo,
		Input:     s.Input,
		Result:    s.Result,
	}
}

// ExecutionHandler handles execution resources.
// It delegates to the SDK handler and adds CLI-specific convenience methods.
type ExecutionHandler struct {
	sdk *sdkworkflow.ExecutionHandler
}

// NewExecutionHandler creates a new execution handler
func NewExecutionHandler(c *client.Client) *ExecutionHandler {
	return &ExecutionHandler{
		sdk: sdkworkflow.NewExecutionHandler(httpclient.Wrap(c.HTTP())),
	}
}

// Re-export SDK type so callers don't need to import the SDK directly.
type ExecutionFilters = sdkworkflow.ExecutionFilters

// MaxExecutionLimit is the most executions one listing returns.
const MaxExecutionLimit = sdkworkflow.MaxExecutionLimit

// List retrieves executions with optional filters.
func (h *ExecutionHandler) List(filters ExecutionFilters, limit int64) (*ExecutionList, error) {
	sdkResult, err := h.sdk.List(context.Background(), filters, limit)
	if err != nil {
		return nil, err
	}
	results := make([]Execution, len(sdkResult.Results))
	for i := range sdkResult.Results {
		results[i] = fromSDKExecution(&sdkResult.Results[i])
	}
	return &ExecutionList{Count: sdkResult.Count, Results: results}, nil
}

// Get retrieves a specific execution
func (h *ExecutionHandler) Get(id string) (*Execution, error) {
	sdkResult, err := h.sdk.Get(context.Background(), id)
	if err != nil {
		return nil, err
	}
	e := fromSDKExecution(sdkResult)
	return &e, nil
}

// Cancel cancels an active execution
func (h *ExecutionHandler) Cancel(id string) error {
	return h.sdk.Cancel(context.Background(), id)
}

// ListTasks retrieves all task executions for a workflow execution
func (h *ExecutionHandler) ListTasks(executionID string) ([]TaskExecution, error) {
	sdkResult, err := h.sdk.ListTasks(context.Background(), executionID)
	if err != nil {
		return nil, err
	}
	tasks := make([]TaskExecution, len(sdkResult))
	for i := range sdkResult {
		tasks[i] = fromSDKTaskExecution(&sdkResult[i])
	}
	return tasks, nil
}

// GetTaskLog retrieves the log output of a specific task execution
func (h *ExecutionHandler) GetTaskLog(executionID, taskName string) (string, error) {
	return h.sdk.GetTaskLog(context.Background(), executionID, taskName)
}

// GetTaskResult retrieves the structured return value of a specific task execution
func (h *ExecutionHandler) GetTaskResult(executionID, taskName string) (any, error) {
	return h.sdk.GetTaskResult(context.Background(), executionID, taskName)
}

// GetExecutionLog retrieves the combined log output of all tasks in an execution
func (h *ExecutionHandler) GetExecutionLog(executionID string) (string, error) {
	return h.sdk.GetExecutionLog(context.Background(), executionID)
}

// TaskLogFailure is one task whose log could not be fetched.
type TaskLogFailure struct {
	Task string
	Err  error
}

// TaskLogError reports task logs that could not be fetched. It is returned
// together with the log text, which still holds every log that was fetched and
// an inline marker for each one that was not.
type TaskLogError struct {
	ExecutionID string
	Total       int
	Failed      []TaskLogFailure
}

func (e *TaskLogError) Error() string {
	var b strings.Builder
	fmt.Fprintf(&b, "could not fetch the log of %d of %d tasks in execution %s:", len(e.Failed), e.Total, e.ExecutionID)
	for _, f := range e.Failed {
		fmt.Fprintf(&b, "\n  %s: %v", f.Task, f.Err)
	}
	return b.String()
}

// Unwrap exposes the per-task errors to errors.Is and errors.As.
func (e *TaskLogError) Unwrap() []error {
	errs := make([]error, len(e.Failed))
	for i, f := range e.Failed {
		errs[i] = f.Err
	}
	return errs
}

// GetFullExecutionLog retrieves logs for all tasks in an execution, formatted with headers.
//
// A task log that cannot be fetched is marked inline and the remaining tasks
// are still fetched. The text is then returned together with a *TaskLogError
// if any fetch failed for a reason other than 404, or if no task log could be
// fetched at all. A lone 404 is not reported as an error: the text already
// says the log is missing, and a task that has no log is not a failure.
func (h *ExecutionHandler) GetFullExecutionLog(executionID string) (string, error) {
	// Get all tasks
	tasks, err := h.ListTasks(executionID)
	if err != nil {
		return "", err
	}

	if len(tasks) == 0 {
		return "", nil
	}

	// Sort tasks by start time
	sortTasksByStartTime(tasks)

	var builder strings.Builder
	var failed []TaskLogFailure
	realFailure := false

	for i, task := range tasks {
		// Add separator between tasks
		if i > 0 {
			builder.WriteString("\n")
		}

		// Task header
		builder.WriteString(fmt.Sprintf("=== Task: %s [%s] ===\n", task.Name, task.State))

		// Get task log
		log, err := h.sdk.GetTaskLog(context.Background(), executionID, task.Name)
		if err != nil {
			builder.WriteString(fmt.Sprintf("(failed to fetch log: %v)\n", err))
			failed = append(failed, TaskLogFailure{Task: task.Name, Err: err})
			if !errors.Is(err, httpclient.ErrNotFound) {
				realFailure = true
			}
			continue
		}

		if log == "" {
			builder.WriteString("(no log output)\n")
		} else {
			builder.WriteString(log)
			// Ensure log ends with newline
			if !strings.HasSuffix(log, "\n") {
				builder.WriteString("\n")
			}
		}
	}

	if realFailure || len(failed) == len(tasks) {
		return builder.String(), &TaskLogError{ExecutionID: executionID, Total: len(tasks), Failed: failed}
	}
	return builder.String(), nil
}

// GetCompleteExecutionLog retrieves both the workflow execution log and all task logs
func (h *ExecutionHandler) GetCompleteExecutionLog(executionID string) (string, error) {
	var builder strings.Builder

	// Get workflow execution log first
	execLog, err := h.sdk.GetExecutionLog(context.Background(), executionID)
	if err != nil {
		return "", err
	}

	if execLog != "" {
		builder.WriteString("=== Workflow Execution Log ===\n")
		builder.WriteString(execLog)
		// Ensure log ends with newline
		if !strings.HasSuffix(execLog, "\n") {
			builder.WriteString("\n")
		}
		builder.WriteString("\n")
	}

	// Get all task logs. A *TaskLogError still comes with the logs that were
	// fetched, so it is passed on with the text rather than instead of it.
	taskLogs, err := h.GetFullExecutionLog(executionID)
	var taskErr *TaskLogError
	if err != nil && !errors.As(err, &taskErr) {
		return "", err
	}

	if taskLogs != "" {
		builder.WriteString(taskLogs)
	}

	return builder.String(), err
}

// sortTasksByStartTime sorts tasks by their start time (nil times go last)
func sortTasksByStartTime(tasks []TaskExecution) {
	slices.SortFunc(tasks, func(a, b TaskExecution) int {
		if a.StartedAt == nil && b.StartedAt == nil {
			return 0
		}
		if a.StartedAt == nil {
			return 1
		}
		if b.StartedAt == nil {
			return -1
		}
		return a.StartedAt.Compare(*b.StartedAt)
	})
}
