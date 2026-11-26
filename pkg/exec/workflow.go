package exec

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/Dynatrace/dtctl/pkg/client"
	"github.com/Dynatrace/dtctl/sdk/httpclient"
)

// WorkflowExecutor handles workflow execution
type WorkflowExecutor struct {
	client *client.Client
}

// NewWorkflowExecutor creates a new workflow executor
func NewWorkflowExecutor(c *client.Client) *WorkflowExecutor {
	return &WorkflowExecutor{client: c}
}

// WorkflowExecutionRequest represents a workflow execution request
type WorkflowExecutionRequest struct {
	Input   map[string]any         `json:"input,omitempty"`
	Params  map[string]interface{} `json:"params,omitempty"`
	Monitor bool                   `json:"-"`
}

// WorkflowExecutionResponse represents a workflow execution response
type WorkflowExecutionResponse struct {
	ID       string `json:"id"`
	Workflow string `json:"workflow"`
	State    string `json:"state"`
}

type apiErrorEnvelope struct {
	Error struct {
		Message string `json:"message"`
	} `json:"error"`
}

// Execute executes a workflow
func (e *WorkflowExecutor) Execute(workflowID string, req WorkflowExecutionRequest) (*WorkflowExecutionResponse, error) {
	var result WorkflowExecutionResponse

	httpReq := e.client.HTTP().R().
		SetHeader("Content-Type", "application/json").
		SetBody(req).
		SetResult(&result)

	if req.Monitor {
		httpReq.SetQueryParam("monitor", "true")
	}

	resp, err := httpReq.
		Post(fmt.Sprintf("/platform/automation/v1/workflows/%s/run", httpclient.PathSegment(workflowID)))

	if err != nil {
		return nil, fmt.Errorf("failed to execute workflow: %w", err)
	}

	if resp.IsError() {
		return nil, formatWorkflowAPIError("workflow execution failed", resp.StatusCode(), resp.String())
	}

	return &result, nil
}

// ParseParams parses key=value parameter strings
func ParseParams(paramStrings []string) (map[string]string, error) {
	params := make(map[string]string)

	for _, p := range paramStrings {
		parts := strings.SplitN(p, "=", 2)
		if len(parts) != 2 {
			return nil, fmt.Errorf("invalid parameter format: %s (expected key=value)", p)
		}
		params[parts[0]] = parts[1]
	}

	return params, nil
}

// ExecutionStatus represents the status of a workflow execution
type ExecutionStatus struct {
	ID        string     `json:"id"`
	Workflow  string     `json:"workflow"`
	State     string     `json:"state"`
	StateInfo *string    `json:"stateInfo,omitempty"`
	StartedAt time.Time  `json:"startedAt"`
	EndedAt   *time.Time `json:"endedAt,omitempty"`
	Runtime   int        `json:"runtime,omitempty"`
	Result    any        `json:"result,omitempty"`
}

// IsTerminalState returns true if the execution state is terminal (completed, failed, etc.)
func (s *ExecutionStatus) IsTerminalState() bool {
	switch s.State {
	case "SUCCESS", "ERROR", "CANCELED", "CANCELLED":
		return true
	default:
		return false
	}
}

// GetStatus retrieves the current status of an execution
func (e *WorkflowExecutor) GetStatus(executionID string) (*ExecutionStatus, error) {
	var result ExecutionStatus

	resp, err := e.client.HTTP().R().
		SetResult(&result).
		Get(fmt.Sprintf("/platform/automation/v1/executions/%s", httpclient.PathSegment(executionID)))

	if err != nil {
		return nil, fmt.Errorf("failed to get execution status: %w", err)
	}

	if resp.IsError() {
		return nil, formatWorkflowAPIError("failed to get execution status", resp.StatusCode(), resp.String())
	}

	return &result, nil
}

func formatWorkflowAPIError(prefix string, statusCode int, body string) error {
	var envelope apiErrorEnvelope
	if err := json.Unmarshal([]byte(body), &envelope); err == nil && envelope.Error.Message != "" {
		return fmt.Errorf("%s: status %d: %s", prefix, statusCode, envelope.Error.Message)
	}

	return fmt.Errorf("%s: status %d: %s", prefix, statusCode, body)
}

// WaitOptions configures the wait behavior
type WaitOptions struct {
	PollInterval time.Duration
	Timeout      time.Duration
}

// DefaultWaitOptions returns sensible defaults for waiting
func DefaultWaitOptions() WaitOptions {
	return WaitOptions{
		PollInterval: 2 * time.Second,
		Timeout:      30 * time.Minute,
	}
}

// WaitForCompletion polls the execution status until it reaches a terminal state
func (e *WorkflowExecutor) WaitForCompletion(ctx context.Context, executionID string, opts WaitOptions) (*ExecutionStatus, error) {
	if opts.PollInterval == 0 {
		opts.PollInterval = 2 * time.Second
	}
	if opts.Timeout == 0 {
		opts.Timeout = 30 * time.Minute
	}

	ctx, cancel := context.WithTimeout(ctx, opts.Timeout)
	defer cancel()

	ticker := time.NewTicker(opts.PollInterval)
	defer ticker.Stop()

	for {
		status, err := e.GetStatus(executionID)
		if err != nil {
			return nil, err
		}

		if status.IsTerminalState() {
			return status, nil
		}

		select {
		case <-ctx.Done():
			return status, fmt.Errorf("timeout waiting for execution to complete (current state: %s)", status.State)
		case <-ticker.C:
			// Continue polling
		}
	}
}
