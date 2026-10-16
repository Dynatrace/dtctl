package analyzer

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/dynatrace-oss/dtctl/pkg/client"
	"github.com/dynatrace-oss/dtctl/pkg/util/format"
	"github.com/dynatrace-oss/dtctl/pkg/vfs"
	sdkana "github.com/dynatrace-oss/dtctl/sdk/api/analyzer"
	"github.com/dynatrace-oss/dtctl/sdk/httpclient"
)

// Re-export SDK types that don't need table tags as aliases.
type (
	AnalyzerCategory = sdkana.AnalyzerCategory
	ExecuteRequest   = sdkana.ExecuteRequest
	AnalyzerResult   = sdkana.AnalyzerResult
	ExecutionLog     = sdkana.ExecutionLog
	ValidationResult = sdkana.ValidationResult
)

// Analyzer represents an analyzer definition with CLI display fields.
type Analyzer struct {
	Name         string            `json:"name" table:"NAME"`
	DisplayName  string            `json:"displayName" table:"DISPLAY NAME"`
	Description  string            `json:"description,omitempty" table:"DESCRIPTION,wide"`
	Category     *AnalyzerCategory `json:"category,omitempty" table:"-"`
	CategoryName string            `json:"-" table:"CATEGORY"`
	Type         string            `json:"type,omitempty" table:"TYPE"`
	BaseAnalyzer string            `json:"baseAnalyzer,omitempty" table:"-"`
}

// MarshalYAML renders the analyzer through its JSON shape so YAML output matches
// JSON: the display-only CategoryName (json:"-") is excluded, keys keep their
// camelCase, and omitempty is honored. Without it, yaml.v3 reflection would
// lowercase keys and leak categoryname.
func (a Analyzer) MarshalYAML() (any, error) {
	return format.YAMLNodeFromJSON(a)
}

// AnalyzerList represents a list of analyzers.
type AnalyzerList struct {
	Analyzers  []Analyzer `json:"analyzers"`
	TotalCount int        `json:"totalCount"`
}

// AnalyzerDefinition represents detailed analyzer definition with CLI display fields.
type AnalyzerDefinition struct {
	Name         string            `json:"name" table:"NAME"`
	DisplayName  string            `json:"displayName" table:"DISPLAY NAME"`
	Description  string            `json:"description,omitempty" table:"DESCRIPTION"`
	Category     *AnalyzerCategory `json:"category,omitempty" table:"-"`
	CategoryName string            `json:"-" table:"CATEGORY"`
	Type         string            `json:"type,omitempty" table:"TYPE"`
	BaseAnalyzer string            `json:"baseAnalyzer,omitempty" table:"BASE ANALYZER"`
	Labels       []string          `json:"labels,omitempty" table:"-"`
	Input        json.RawMessage   `json:"input,omitempty" table:"-"`
	Output       json.RawMessage   `json:"output,omitempty" table:"-"`
	AnalyzerCall json.RawMessage   `json:"analyzerCall,omitempty" table:"-"`
}

// MarshalYAML renders the definition through its JSON shape so YAML output
// matches JSON. This is essential here: Input/Output/AnalyzerCall are
// json.RawMessage ([]byte), which yaml.v3 reflection would emit as a list of
// raw byte values instead of the structured schema; it also drops the
// display-only CategoryName and preserves camelCase keys.
func (d AnalyzerDefinition) MarshalYAML() (any, error) {
	return format.YAMLNodeFromJSON(d)
}

// ExecuteResult represents an analyzer execution result with CLI display fields.
type ExecuteResult struct {
	RequestToken string          `json:"requestToken,omitempty" table:"REQUEST TOKEN,wide"`
	TTLInSeconds int64           `json:"ttlInSeconds,omitempty" table:"-"`
	Result       *AnalyzerResult `json:"result" table:"-"`
	// Flattened fields for table display
	ResultID        string `json:"-" table:"RESULT ID"`
	ResultStatus    string `json:"-" table:"STATUS"`
	ExecutionStatus string `json:"-" table:"EXECUTION"`
}

// MarshalYAML renders the result through its JSON shape so YAML output matches
// JSON: the flattened display-only fields (ResultID/ResultStatus/ExecutionStatus,
// all json:"-") are excluded and keys keep their camelCase.
func (r ExecuteResult) MarshalYAML() (any, error) {
	return format.YAMLNodeFromJSON(r)
}

// populateTableFields copies Result fields to top-level for table display.
func (r *ExecuteResult) populateTableFields() {
	if r.Result != nil {
		r.ResultID = r.Result.ResultID
		r.ResultStatus = r.Result.ResultStatus
		r.ExecutionStatus = r.Result.ExecutionStatus
	}
}

// fromSDKAnalyzer converts an SDK Analyzer to the CLI Analyzer.
func fromSDKAnalyzer(s *sdkana.Analyzer) Analyzer {
	a := Analyzer{
		Name:         s.Name,
		DisplayName:  s.DisplayName,
		Description:  s.Description,
		Category:     s.Category,
		Type:         s.Type,
		BaseAnalyzer: s.BaseAnalyzer,
	}
	if s.Category != nil {
		a.CategoryName = s.Category.DisplayName
	}
	return a
}

// fromSDKAnalyzerDefinition converts an SDK AnalyzerDefinition to the CLI AnalyzerDefinition.
func fromSDKAnalyzerDefinition(s *sdkana.AnalyzerDefinition) *AnalyzerDefinition {
	d := &AnalyzerDefinition{
		Name:         s.Name,
		DisplayName:  s.DisplayName,
		Description:  s.Description,
		Category:     s.Category,
		Type:         s.Type,
		BaseAnalyzer: s.BaseAnalyzer,
		Labels:       s.Labels,
		Input:        s.Input,
		Output:       s.Output,
		AnalyzerCall: s.AnalyzerCall,
	}
	if s.Category != nil {
		d.CategoryName = s.Category.DisplayName
	}
	return d
}

// fromSDKExecuteResult converts an SDK ExecuteResult to the CLI ExecuteResult.
func fromSDKExecuteResult(s *sdkana.ExecuteResult) *ExecuteResult {
	r := &ExecuteResult{
		RequestToken: s.RequestToken,
		TTLInSeconds: s.TTLInSeconds,
		Result:       s.Result,
	}
	r.populateTableFields()
	return r
}

// ParseInputFromFile reads and parses analyzer input from a file.
// This is a CLI-layer helper and intentionally not part of the SDK.
func ParseInputFromFile(filename string) (map[string]interface{}, error) {
	return ParseInputFromFileWith(vfs.Env{}, filename)
}

// ParseInputFromFileWith is ParseInputFromFile through the given invocation Env.
func ParseInputFromFileWith(env vfs.Env, filename string) (map[string]interface{}, error) {
	content, err := env.ReadFile(filename)
	if err != nil {
		return nil, err
	}

	var input map[string]interface{}
	if err := json.Unmarshal(content, &input); err != nil {
		return nil, fmt.Errorf("failed to parse input file: %w", err)
	}

	return input, nil
}

// Handler handles Davis analyzer resources.
type Handler struct {
	sdk *sdkana.Handler
}

// NewHandler creates a new analyzer handler
func NewHandler(c *client.Client) *Handler {
	return &Handler{
		sdk: sdkana.NewHandler(httpclient.Wrap(c.HTTP())),
	}
}

// List retrieves all available analyzers
func (h *Handler) List(filter string) (*AnalyzerList, error) {
	sdkResult, err := h.sdk.List(context.Background(), filter)
	if err != nil {
		return nil, err
	}
	result := &AnalyzerList{
		TotalCount: sdkResult.TotalCount,
	}
	result.Analyzers = make([]Analyzer, len(sdkResult.Analyzers))
	for i, a := range sdkResult.Analyzers {
		result.Analyzers[i] = fromSDKAnalyzer(&a)
	}
	return result, nil
}

// Get retrieves a specific analyzer definition
func (h *Handler) Get(name string) (*AnalyzerDefinition, error) {
	sdkResult, err := h.sdk.Get(context.Background(), name)
	if err != nil {
		return nil, err
	}
	return fromSDKAnalyzerDefinition(sdkResult), nil
}

// GetDocumentation retrieves the documentation for an analyzer
func (h *Handler) GetDocumentation(name string) (string, error) {
	return h.sdk.GetDocumentation(context.Background(), name)
}

// GetInputSchema retrieves the JSON schema for analyzer input
func (h *Handler) GetInputSchema(name string) (map[string]interface{}, error) {
	return h.sdk.GetInputSchema(context.Background(), name)
}

// GetResultSchema retrieves the JSON schema for analyzer result
func (h *Handler) GetResultSchema(name string) (map[string]interface{}, error) {
	return h.sdk.GetResultSchema(context.Background(), name)
}

// Execute runs an analyzer with the given input
func (h *Handler) Execute(name string, input map[string]interface{}, timeoutSeconds int) (*ExecuteResult, error) {
	sdkResult, err := h.sdk.Execute(context.Background(), name, input, timeoutSeconds)
	if err != nil {
		return nil, err
	}
	return fromSDKExecuteResult(sdkResult), nil
}

// ExecuteAndWait runs an analyzer and waits for completion.
// The context can be used to cancel a long-running poll (e.g. on SIGINT).
func (h *Handler) ExecuteAndWait(ctx context.Context, name string, input map[string]interface{}, maxWaitSeconds int) (*ExecuteResult, error) {
	sdkResult, err := h.sdk.ExecuteAndWait(ctx, name, input, maxWaitSeconds)
	if err != nil {
		return nil, err
	}
	return fromSDKExecuteResult(sdkResult), nil
}

// Poll polls for the result of a started analyzer execution
func (h *Handler) Poll(name string, requestToken string, timeoutSeconds int) (*ExecuteResult, error) {
	sdkResult, err := h.sdk.Poll(context.Background(), name, requestToken, timeoutSeconds)
	if err != nil {
		return nil, err
	}
	return fromSDKExecuteResult(sdkResult), nil
}

// Cancel cancels a running analyzer execution
func (h *Handler) Cancel(name string, requestToken string) (*ExecuteResult, error) {
	sdkResult, err := h.sdk.Cancel(context.Background(), name, requestToken)
	if err != nil {
		return nil, err
	}
	return fromSDKExecuteResult(sdkResult), nil
}

// Validate validates the input for an analyzer execution
func (h *Handler) Validate(name string, input map[string]interface{}) (*ValidationResult, error) {
	return h.sdk.Validate(context.Background(), name, input)
}
