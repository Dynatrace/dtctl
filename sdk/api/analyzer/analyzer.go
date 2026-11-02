package analyzer

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/Dynatrace/dtctl/sdk/httpclient"
)

// Handler handles Davis analyzer resources
type Handler struct {
	client *httpclient.Client
}

// NewHandler creates a new analyzer handler
func NewHandler(c *httpclient.Client) *Handler {
	return &Handler{client: c}
}

// AnalyzerCategory represents the category of an analyzer
type AnalyzerCategory struct {
	DisplayName string `json:"displayName"`
}

// Analyzer represents an analyzer definition
type Analyzer struct {
	Name         string            `json:"name"`
	DisplayName  string            `json:"displayName"`
	Description  string            `json:"description,omitempty"`
	Category     *AnalyzerCategory `json:"category,omitempty"`
	Type         string            `json:"type,omitempty"`
	BaseAnalyzer string            `json:"baseAnalyzer,omitempty"`
}

// AnalyzerList represents a list of analyzers
type AnalyzerList struct {
	Analyzers   []Analyzer `json:"analyzers"`
	TotalCount  int        `json:"totalCount"`
	NextPageKey string     `json:"nextPageKey,omitempty"`
}

// AnalyzerDefinition represents detailed analyzer definition
type AnalyzerDefinition struct {
	Name         string            `json:"name"`
	DisplayName  string            `json:"displayName"`
	Description  string            `json:"description,omitempty"`
	Category     *AnalyzerCategory `json:"category,omitempty"`
	Type         string            `json:"type,omitempty"`
	BaseAnalyzer string            `json:"baseAnalyzer,omitempty"`
	Labels       []string          `json:"labels,omitempty"`
	Input        json.RawMessage   `json:"input,omitempty"`
	Output       json.RawMessage   `json:"output,omitempty"`
	AnalyzerCall json.RawMessage   `json:"analyzerCall,omitempty"`
}

// ExecuteRequest represents an analyzer execution request
type ExecuteRequest struct {
	Input map[string]interface{} `json:"input"`
}

// ExecuteResult represents an analyzer execution result
type ExecuteResult struct {
	RequestToken string          `json:"requestToken,omitempty"`
	TTLInSeconds int64           `json:"ttlInSeconds,omitempty"`
	Result       *AnalyzerResult `json:"result"`
}

// AnalyzerResult represents the result of an analyzer execution
type AnalyzerResult struct {
	ResultID        string                   `json:"resultId"`
	ResultStatus    string                   `json:"resultStatus"`
	ExecutionStatus string                   `json:"executionStatus"`
	Input           map[string]interface{}   `json:"input,omitempty"`
	Output          []map[string]interface{} `json:"output,omitempty"`
	Data            []map[string]interface{} `json:"data,omitempty"`
	Logs            []ExecutionLog           `json:"logs,omitempty"`
}

// ExecutionLog represents an execution log entry
type ExecutionLog struct {
	Level   string `json:"level"`
	Message string `json:"message"`
	Path    string `json:"path,omitempty"`
}

// ValidationResult represents the result of input validation
type ValidationResult struct {
	Valid   bool                   `json:"valid"`
	Details map[string]interface{} `json:"details,omitempty"`
}

// List retrieves all available analyzers
func (h *Handler) List(ctx context.Context, filter string) (*AnalyzerList, error) {
	req := h.client.HTTP().R().SetContext(ctx)

	if filter != "" {
		req.SetQueryParam("filter", filter)
	}
	req.SetQueryParam("add-fields", "category,type")

	resp, err := req.Get("/platform/davis/analyzers/v1/analyzers")
	if err != nil {
		return nil, fmt.Errorf("failed to list analyzers: %w", err)
	}
	if err := httpclient.CheckResponse(resp); err != nil {
		return nil, fmt.Errorf("failed to list analyzers: %w", err)
	}

	var result AnalyzerList
	if err := json.Unmarshal(resp.Body(), &result); err != nil {
		return nil, fmt.Errorf("list analyzers: parse response: %w", err)
	}

	return &result, nil
}

// Get retrieves a specific analyzer definition
func (h *Handler) Get(ctx context.Context, name string) (*AnalyzerDefinition, error) {
	resp, err := h.client.HTTP().R().SetContext(ctx).
		Get(fmt.Sprintf("/platform/davis/analyzers/v1/analyzers/%s", httpclient.PathSegment(name)))
	if err != nil {
		return nil, fmt.Errorf("failed to get analyzer: %w", err)
	}
	if err := httpclient.CheckResponse(resp); err != nil {
		var apiErr *httpclient.APIError
		if errors.As(err, &apiErr) && apiErr.StatusCode == 404 {
			return nil, fmt.Errorf("analyzer %q not found", name)
		}
		return nil, fmt.Errorf("failed to get analyzer: %w", err)
	}

	var result AnalyzerDefinition
	if err := json.Unmarshal(resp.Body(), &result); err != nil {
		return nil, fmt.Errorf("get analyzer: parse response: %w", err)
	}

	return &result, nil
}

// GetDocumentation retrieves the documentation for an analyzer
func (h *Handler) GetDocumentation(ctx context.Context, name string) (string, error) {
	resp, err := h.client.HTTP().R().SetContext(ctx).
		SetHeader("Accept", "text/markdown").
		Get(fmt.Sprintf("/platform/davis/analyzers/v1/analyzers/%s/documentation", httpclient.PathSegment(name)))
	if err != nil {
		return "", fmt.Errorf("failed to get analyzer documentation: %w", err)
	}
	if err := httpclient.CheckResponse(resp); err != nil {
		var apiErr *httpclient.APIError
		if errors.As(err, &apiErr) && apiErr.StatusCode == 404 {
			return "", fmt.Errorf("documentation for analyzer %q not found", name)
		}
		return "", fmt.Errorf("failed to get analyzer documentation: %w", err)
	}
	return resp.String(), nil
}

// GetInputSchema retrieves the JSON schema for analyzer input
func (h *Handler) GetInputSchema(ctx context.Context, name string) (map[string]interface{}, error) {
	resp, err := h.client.HTTP().R().SetContext(ctx).
		Get(fmt.Sprintf("/platform/davis/analyzers/v1/analyzers/%s/json-schema/input", httpclient.PathSegment(name)))
	if err != nil {
		return nil, fmt.Errorf("failed to get input schema: %w", err)
	}
	if err := httpclient.CheckResponse(resp); err != nil {
		return nil, fmt.Errorf("failed to get input schema: %w", err)
	}

	var result map[string]interface{}
	if err := json.Unmarshal(resp.Body(), &result); err != nil {
		return nil, fmt.Errorf("get input schema: parse response: %w", err)
	}
	return result, nil
}

// GetResultSchema retrieves the JSON schema for analyzer result
func (h *Handler) GetResultSchema(ctx context.Context, name string) (map[string]interface{}, error) {
	resp, err := h.client.HTTP().R().SetContext(ctx).
		Get(fmt.Sprintf("/platform/davis/analyzers/v1/analyzers/%s/json-schema/result", httpclient.PathSegment(name)))
	if err != nil {
		return nil, fmt.Errorf("failed to get result schema: %w", err)
	}
	if err := httpclient.CheckResponse(resp); err != nil {
		return nil, fmt.Errorf("failed to get result schema: %w", err)
	}

	var result map[string]interface{}
	if err := json.Unmarshal(resp.Body(), &result); err != nil {
		return nil, fmt.Errorf("get result schema: parse response: %w", err)
	}
	return result, nil
}

// Execute runs an analyzer with the given input
func (h *Handler) Execute(ctx context.Context, name string, input map[string]interface{}, timeoutSeconds int) (*ExecuteResult, error) {
	req := h.client.HTTP().R().SetContext(ctx)

	if timeoutSeconds > 0 {
		req.SetQueryParam("timeout-seconds", fmt.Sprintf("%d", timeoutSeconds))
	}

	var result ExecuteResult
	resp, err := req.
		SetBody(input).
		Post(fmt.Sprintf("/platform/davis/analyzers/v1/analyzers/%s:execute", httpclient.PathSegment(name)))
	if err != nil {
		return nil, fmt.Errorf("failed to execute analyzer: %w", err)
	}
	if err := httpclient.CheckResponse(resp); err != nil {
		return nil, fmt.Errorf("failed to execute analyzer: %w", err)
	}

	if err := json.Unmarshal(resp.Body(), &result); err != nil {
		return nil, fmt.Errorf("execute analyzer: parse response: %w", err)
	}

	return &result, nil
}

// Default polling parameters for ExecuteAndWait.
const (
	defaultInitialTimeout = 30 // seconds for the initial execute call
	defaultPollTimeout    = 10 // seconds for each poll call
	defaultPollInterval   = 2 * time.Second
)

// ExecuteAndWait runs an analyzer and waits for completion.
// The context can be used to cancel a long-running poll (e.g. on SIGINT).
//
// Only COMPLETED is a success; ABORTED and FAILED are errors. Any other status
// that comes with a request token is polled, because a status added to the API
// later is more likely in flight than terminal.
func (h *Handler) ExecuteAndWait(ctx context.Context, name string, input map[string]interface{}, maxWaitSeconds int) (*ExecuteResult, error) {
	// Start execution with initial timeout
	result, err := h.Execute(ctx, name, input, defaultInitialTimeout)
	if err != nil {
		return nil, err
	}

	status := executionStatus(result)
	if status == StatusCompleted {
		return result, nil
	}
	if err := failedStatusError(status); err != nil {
		return result, err
	}
	if result.RequestToken == "" {
		if result.Result == nil {
			return result, fmt.Errorf("analyzer returned neither a result nor a request token")
		}
		if status == "" {
			// A result with no executionStatus at all: the response carries
			// everything there is, so hand it over rather than inventing a
			// failure out of a missing field.
			return result, nil
		}
		return result, fmt.Errorf("analyzer execution ended in status %q with no request token to poll", status)
	}

	startTime := time.Now()
	maxDuration := time.Duration(maxWaitSeconds) * time.Second

	for {
		if time.Since(startTime) > maxDuration {
			if status != "" && status != StatusRunning {
				return nil, fmt.Errorf("analyzer execution timed out after %d seconds, last status %q", maxWaitSeconds, status)
			}
			return nil, fmt.Errorf("analyzer execution timed out after %d seconds", maxWaitSeconds)
		}

		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		default:
		}

		pollResult, err := h.Poll(ctx, name, result.RequestToken, defaultPollTimeout)
		if err != nil {
			return nil, err
		}

		status = executionStatus(pollResult)
		if status == StatusCompleted {
			return pollResult, nil
		}
		if err := failedStatusError(status); err != nil {
			// A Ctrl-C that races a completing poll must stay a cancellation,
			// not turn into an "aborted"/"failed" execution error.
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			return pollResult, err
		}

		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(defaultPollInterval):
		}
	}
}

// executionStatus returns the execution status of r, or "" when r has no
// result yet.
func executionStatus(r *ExecuteResult) string {
	if r.Result == nil {
		return ""
	}
	return r.Result.ExecutionStatus
}

// Execution statuses reported by the analyzer API.
const (
	StatusCompleted = "COMPLETED"
	StatusRunning   = "RUNNING"
	StatusAborted   = "ABORTED"
	StatusFailed    = "FAILED"
)

// failedStatusError returns the error for a failed execution status, or nil.
func failedStatusError(status string) error {
	switch status {
	case StatusAborted:
		return fmt.Errorf("analyzer execution was aborted")
	case StatusFailed:
		return fmt.Errorf("analyzer execution failed")
	default:
		return nil
	}
}

// Poll polls for the result of a started analyzer execution
func (h *Handler) Poll(ctx context.Context, name string, requestToken string, timeoutSeconds int) (*ExecuteResult, error) {
	req := h.client.HTTP().R().SetContext(ctx).
		SetQueryParam("request-token", requestToken)

	if timeoutSeconds > 0 {
		req.SetQueryParam("timeout-seconds", fmt.Sprintf("%d", timeoutSeconds))
	}

	var result ExecuteResult
	resp, err := req.
		Get(fmt.Sprintf("/platform/davis/analyzers/v1/analyzers/%s:poll", httpclient.PathSegment(name)))
	if err != nil {
		return nil, fmt.Errorf("failed to poll analyzer: %w", err)
	}
	if err := httpclient.CheckResponse(resp); err != nil {
		var apiErr *httpclient.APIError
		if errors.As(err, &apiErr) && apiErr.StatusCode == 410 {
			return nil, fmt.Errorf("analyzer result expired or already consumed")
		}
		return nil, fmt.Errorf("failed to poll analyzer: %w", err)
	}

	if err := json.Unmarshal(resp.Body(), &result); err != nil {
		return nil, fmt.Errorf("poll analyzer: parse response: %w", err)
	}

	return &result, nil
}

// Cancel cancels a running analyzer execution
func (h *Handler) Cancel(ctx context.Context, name string, requestToken string) (*ExecuteResult, error) {
	req := h.client.HTTP().R().SetContext(ctx).
		SetQueryParam("request-token", requestToken)

	resp, err := req.
		Post(fmt.Sprintf("/platform/davis/analyzers/v1/analyzers/%s:cancel", httpclient.PathSegment(name)))
	if err != nil {
		return nil, fmt.Errorf("failed to cancel analyzer: %w", err)
	}
	if err := httpclient.CheckResponse(resp); err != nil {
		return nil, fmt.Errorf("failed to cancel analyzer: %w", err)
	}

	var result ExecuteResult
	if err := json.Unmarshal(resp.Body(), &result); err != nil {
		return nil, fmt.Errorf("cancel analyzer: parse response: %w", err)
	}
	return &result, nil
}

// Validate validates the input for an analyzer execution
func (h *Handler) Validate(ctx context.Context, name string, input map[string]interface{}) (*ValidationResult, error) {
	resp, err := h.client.HTTP().R().SetContext(ctx).
		SetBody(input).
		Post(fmt.Sprintf("/platform/davis/analyzers/v1/analyzers/%s:validate", httpclient.PathSegment(name)))
	if err != nil {
		return nil, fmt.Errorf("failed to validate analyzer input: %w", err)
	}
	if err := httpclient.CheckResponse(resp); err != nil {
		return nil, fmt.Errorf("failed to validate analyzer input: %w", err)
	}

	var result ValidationResult
	if err := json.Unmarshal(resp.Body(), &result); err != nil {
		return nil, fmt.Errorf("validate analyzer input: parse response: %w", err)
	}
	return &result, nil
}
