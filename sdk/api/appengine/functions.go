package appengine

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/go-resty/resty/v2"

	"github.com/Dynatrace/dtctl/sdk/httpclient"
)

// FunctionHandler handles App Engine function operations
type FunctionHandler struct {
	client *httpclient.Client
}

// NewFunctionHandler creates a new function handler
func NewFunctionHandler(c *httpclient.Client) *FunctionHandler {
	return &FunctionHandler{client: c}
}

// FunctionInvokeRequest represents a function invocation request
type FunctionInvokeRequest struct {
	Method       string            // HTTP method (GET, POST, PUT, PATCH, DELETE)
	AppID        string            // App ID
	FunctionName string            // Function name
	Payload      string            // Request body/payload
	Headers      map[string]string // Additional headers
}

// FunctionInvokeResponse represents a function invocation response
type FunctionInvokeResponse struct {
	StatusCode int               `json:"statusCode"`
	Headers    map[string]string `json:"headers,omitempty"`
	Body       string            `json:"body"`
	RawBody    interface{}       `json:"-"` // For direct JSON output
}

// DeferredExecutionRequest represents a deferred execution request
type DeferredExecutionRequest struct {
	AppID        string `json:"appId"`
	FunctionName string `json:"functionName"`
	Body         string `json:"body,omitempty"`
}

// DeferredExecutionResponse represents a deferred execution response
type DeferredExecutionResponse struct {
	ID string `json:"id"`
}

// FunctionExecutorRequest represents an ad-hoc function execution request
type FunctionExecutorRequest struct {
	SourceCode string `json:"sourceCode"`
	Payload    string `json:"payload,omitempty"`
}

// FunctionExecutorResponse represents an ad-hoc function execution response
type FunctionExecutorResponse struct {
	Result interface{} `json:"result"`
	Logs   string      `json:"logs"`
}

// SDKVersion represents an SDK version
type SDKVersion struct {
	Version string `json:"version"`
	Default bool   `json:"default"`
}

// SDKVersionsResponse represents SDK versions response
type SDKVersionsResponse struct {
	Versions []SDKVersion `json:"versions"`
}

// escapeFunctionName escapes a function name for the "/api/" portion of an
// invoke path. Unlike every other id dtctl interpolates, a function name is a
// path by design: parseFullFunctionName splits "app-id/a/b/c" at the first
// slash only, so "a/b/c" is the name and its slashes are real separators.
// httpclient.PathSegment on the whole name would turn them into "%2F" and address a
// function that does not exist, so each part is escaped on its own.
func escapeFunctionName(name string) string {
	parts := strings.Split(name, "/")
	for i, part := range parts {
		parts[i] = httpclient.PathSegment(part)
	}
	return strings.Join(parts, "/")
}

// InvokeFunction invokes an app function
func (h *FunctionHandler) InvokeFunction(ctx context.Context, req *FunctionInvokeRequest) (*FunctionInvokeResponse, error) {
	reqPath := fmt.Sprintf("/platform/app-engine/app-functions/v1/apps/%s/api/%s",
		httpclient.PathSegment(req.AppID), escapeFunctionName(req.FunctionName))

	httpReq := h.client.HTTP().R().SetContext(ctx)

	for key, value := range req.Headers {
		httpReq.SetHeader(key, value)
	}

	if req.Payload != "" {
		httpReq.SetBody(req.Payload)
	}

	var resp *resty.Response
	var err error

	switch req.Method {
	case "GET":
		resp, err = httpReq.Get(reqPath)
	case "POST":
		resp, err = httpReq.Post(reqPath)
	case "PUT":
		resp, err = httpReq.Put(reqPath)
	case "PATCH":
		resp, err = httpReq.Patch(reqPath)
	case "DELETE":
		resp, err = httpReq.Delete(reqPath)
	default:
		return nil, fmt.Errorf("unsupported HTTP method: %s", req.Method)
	}

	if err != nil {
		return nil, fmt.Errorf("invoke function: %w", err)
	}

	// A failure in the submitted code, reported before CheckResponse can read the
	// non-standard status as an HTTP failure.
	if resp.IsError() {
		if execErr := newExecutionError(resp.StatusCode(), resp.String()); execErr != nil {
			return nil, execErr
		}
	}

	if err := httpclient.CheckResponse(resp); err != nil {
		return nil, err
	}

	var jsonBody interface{}
	bodyBytes := resp.Body()
	body := string(bodyBytes)
	if err := json.Unmarshal(bodyBytes, &jsonBody); err == nil {
		if jsonMap, ok := jsonBody.(map[string]interface{}); ok {
			if errVal, hasError := jsonMap["error"]; hasError && errVal != nil {
				if errStr, ok := errVal.(string); ok && errStr != "" {
					return nil, fmt.Errorf("app function returned an error: %s", errStr)
				}
			}
		}
		return &FunctionInvokeResponse{
			StatusCode: resp.StatusCode(),
			Headers:    make(map[string]string),
			Body:       body,
			RawBody:    jsonBody,
		}, nil
	}

	return &FunctionInvokeResponse{
		StatusCode: resp.StatusCode(),
		Headers:    make(map[string]string),
		Body:       body,
	}, nil
}

// DeferExecution defers execution of a resumable function
func (h *FunctionHandler) DeferExecution(ctx context.Context, req *DeferredExecutionRequest) (*DeferredExecutionResponse, error) {
	resp, err := h.client.HTTP().R().SetContext(ctx).
		SetBody(req).
		Post("/platform/app-engine/app-functions/v1/deferred-execution")

	if err != nil {
		return nil, fmt.Errorf("defer execution: %w", err)
	}
	if err := httpclient.CheckResponse(resp); err != nil {
		return nil, fmt.Errorf("defer execution: %w", err)
	}

	var result DeferredExecutionResponse
	if err := json.Unmarshal(resp.Body(), &result); err != nil {
		return nil, fmt.Errorf("parse deferred execution response: %w", err)
	}

	return &result, nil
}

// ExecuteCode executes ad-hoc JavaScript code using the function executor
func (h *FunctionHandler) ExecuteCode(ctx context.Context, sourceCode, payload string) (*FunctionExecutorResponse, error) {
	req := FunctionExecutorRequest{
		SourceCode: sourceCode,
		Payload:    payload,
	}

	resp, err := h.client.HTTP().R().SetContext(ctx).
		SetBody(req).
		Post("/platform/app-engine/function-executor/v1/executions")

	if err != nil {
		return nil, fmt.Errorf("execute code: %w", err)
	}

	// A failure in the submitted code, reported before CheckResponse can read the
	// non-standard status as an HTTP failure.
	if resp.IsError() {
		if execErr := newExecutionError(resp.StatusCode(), resp.String()); execErr != nil {
			return nil, execErr
		}
	}

	if err := httpclient.CheckResponse(resp); err != nil {
		return nil, err
	}

	var result FunctionExecutorResponse
	if err := json.Unmarshal(resp.Body(), &result); err != nil {
		return nil, fmt.Errorf("parse execution response: %w", err)
	}

	return &result, nil
}

// GetSDKVersions lists available SDK versions
func (h *FunctionHandler) GetSDKVersions(ctx context.Context) (*SDKVersionsResponse, error) {
	resp, err := h.client.HTTP().R().SetContext(ctx).
		Get("/platform/app-engine/function-executor/v1/sdk-versions")

	if err != nil {
		return nil, fmt.Errorf("get SDK versions: %w", err)
	}
	if err := httpclient.CheckResponse(resp); err != nil {
		return nil, fmt.Errorf("get SDK versions: %w", err)
	}

	var result SDKVersionsResponse
	if err := json.Unmarshal(resp.Body(), &result); err != nil {
		return nil, fmt.Errorf("parse SDK versions response: %w", err)
	}

	return &result, nil
}
