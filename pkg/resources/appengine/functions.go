package appengine

import (
	"context"
	"fmt"

	"github.com/dynatrace-oss/dtctl/pkg/client"
	"github.com/dynatrace-oss/dtctl/pkg/vfs"
	sdkae "github.com/dynatrace-oss/dtctl/sdk/api/appengine"
	"github.com/dynatrace-oss/dtctl/sdk/httpclient"
)

// Re-export SDK types that have no table tags.
type (
	FunctionInvokeRequest    = sdkae.FunctionInvokeRequest
	FunctionInvokeResponse   = sdkae.FunctionInvokeResponse
	DeferredExecutionRequest = sdkae.DeferredExecutionRequest
	FunctionExecutorRequest  = sdkae.FunctionExecutorRequest
	FunctionExecutorResponse = sdkae.FunctionExecutorResponse
)

// DeferredExecutionResponse represents a deferred execution response (CLI version with table tags).
type DeferredExecutionResponse struct {
	ID string `json:"id" table:"ID"`
}

// SDKVersion represents an SDK version (CLI version with table tags).
type SDKVersion struct {
	Version string `json:"version" table:"VERSION"`
	Default bool   `json:"default" table:"DEFAULT"`
}

// SDKVersionsResponse represents SDK versions response.
type SDKVersionsResponse struct {
	Versions []SDKVersion `json:"versions"`
}

// fromSDKDeferredExecutionResponse converts an SDK DeferredExecutionResponse to CLI.
func fromSDKDeferredExecutionResponse(s *sdkae.DeferredExecutionResponse) *DeferredExecutionResponse {
	return &DeferredExecutionResponse{ID: s.ID}
}

// fromSDKVersion converts an SDK SDKVersion to CLI.
func fromSDKVersion(s *sdkae.SDKVersion) SDKVersion {
	return SDKVersion{Version: s.Version, Default: s.Default}
}

// ReadFileOrStdin reads content from a file (through the vfs seam, so
// embedded invocations resolve virtual request files) or stdin.
// This is a CLI-layer helper and intentionally not part of the SDK.
func ReadFileOrStdin(filename string) (string, error) {
	return ReadFileOrStdinWith(vfs.Env{}, filename)
}

// ReadFileOrStdinWith is ReadFileOrStdin through the given invocation Env.
func ReadFileOrStdinWith(env vfs.Env, filename string) (string, error) {
	content, err := env.ReadFileOrStdin(filename)
	if err != nil {
		return "", fmt.Errorf("failed to read content: %w", err)
	}

	return string(content), nil
}

// FunctionHandler handles App Engine function operations.
type FunctionHandler struct {
	sdk *sdkae.FunctionHandler
}

// NewFunctionHandler creates a new function handler
func NewFunctionHandler(c *client.Client) *FunctionHandler {
	return &FunctionHandler{
		sdk: sdkae.NewFunctionHandler(httpclient.Wrap(c.HTTP())),
	}
}

// InvokeFunction invokes an app function
func (h *FunctionHandler) InvokeFunction(req *FunctionInvokeRequest) (*FunctionInvokeResponse, error) {
	return h.sdk.InvokeFunction(context.Background(), req)
}

// DeferExecution defers execution of a resumable function
func (h *FunctionHandler) DeferExecution(req *DeferredExecutionRequest) (*DeferredExecutionResponse, error) {
	sdkResult, err := h.sdk.DeferExecution(context.Background(), req)
	if err != nil {
		return nil, err
	}
	return fromSDKDeferredExecutionResponse(sdkResult), nil
}

// ExecuteCode executes ad-hoc JavaScript code using the function executor
func (h *FunctionHandler) ExecuteCode(sourceCode, payload string) (*FunctionExecutorResponse, error) {
	return h.sdk.ExecuteCode(context.Background(), sourceCode, payload)
}

// GetSDKVersions lists available SDK versions
func (h *FunctionHandler) GetSDKVersions() (*SDKVersionsResponse, error) {
	sdkResult, err := h.sdk.GetSDKVersions(context.Background())
	if err != nil {
		return nil, err
	}
	versions := make([]SDKVersion, len(sdkResult.Versions))
	for i := range sdkResult.Versions {
		versions[i] = fromSDKVersion(&sdkResult.Versions[i])
	}
	return &SDKVersionsResponse{Versions: versions}, nil
}
