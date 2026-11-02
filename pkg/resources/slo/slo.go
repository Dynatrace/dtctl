package slo

import (
	"context"
	"encoding/json"

	"github.com/Dynatrace/dtctl/pkg/client"
	sdkslo "github.com/Dynatrace/dtctl/sdk/api/slo"
	"github.com/Dynatrace/dtctl/sdk/httpclient"
)

// Re-export SDK types that don't need table tags as aliases.
type (
	Criteria         = sdkslo.Criteria
	TemplateVariable = sdkslo.TemplateVariable
)

// SLO represents a service-level objective with CLI display fields.
type SLO struct {
	ID          string                 `json:"id" table:"ID"`
	Name        string                 `json:"name" table:"NAME"`
	Description string                 `json:"description,omitempty" table:"DESCRIPTION,wide"`
	Version     string                 `json:"version,omitempty" table:"-"`
	Criteria    []Criteria             `json:"criteria,omitempty" table:"-"`
	Tags        []string               `json:"tags,omitempty" table:"-"`
	CustomSli   map[string]interface{} `json:"customSli,omitempty" table:"-"`
	ExternalID  string                 `json:"externalId,omitempty" table:"-"`
}

// SLOList represents a list of SLOs.
type SLOList struct {
	SLOs        []SLO  `json:"slos"`
	TotalCount  int    `json:"totalCount"`
	NextPageKey string `json:"nextPageKey,omitempty"`
}

// Template represents an SLO objective template with CLI display fields.
type Template struct {
	ID              string             `json:"id" table:"ID"`
	Name            string             `json:"name" table:"NAME"`
	Description     string             `json:"description,omitempty" table:"DESCRIPTION,wide"`
	BuiltIn         bool               `json:"builtIn" table:"BUILTIN"`
	ApplicableScope string             `json:"applicableScope,omitempty" table:"SCOPE,wide"`
	Indicator       string             `json:"indicator,omitempty" table:"-"`
	Variables       []TemplateVariable `json:"variables,omitempty" table:"-"`
	Version         string             `json:"version,omitempty" table:"-"`
}

// TemplateList represents a list of templates.
type TemplateList struct {
	Items       []Template `json:"items"`
	TotalCount  int        `json:"totalCount"`
	NextPageKey string     `json:"nextPageKey,omitempty"`
}

// EvaluationResult represents an SLO evaluation result with CLI display fields.
type EvaluationResult struct {
	Criteria    string   `json:"criteria" table:"CRITERIA"`
	Status      string   `json:"status" table:"STATUS"`
	Value       *float64 `json:"value,omitempty" table:"VALUE"`
	ErrorBudget *float64 `json:"errorBudget,omitempty" table:"ERROR_BUDGET"`
	Message     string   `json:"message,omitempty" table:"MESSAGE,wide"`
}

// EvaluationResponse represents the response from SLO evaluation.
type EvaluationResponse struct {
	Definition        *SLO               `json:"definition,omitempty"`
	EvaluationResults []EvaluationResult `json:"evaluationResults,omitempty"`
	EvaluationToken   string             `json:"evaluationToken,omitempty"`
	TTLSeconds        int64              `json:"ttlSeconds,omitempty"`
}

// fromSDKSLO converts an SDK SLO to the CLI SLO.
func fromSDKSLO(s *sdkslo.SLO) SLO {
	return SLO{
		ID:          s.ID,
		Name:        s.Name,
		Description: s.Description,
		Version:     s.Version,
		Criteria:    s.Criteria,
		Tags:        s.Tags,
		CustomSli:   s.CustomSli,
		ExternalID:  s.ExternalID,
	}
}

// fromSDKSLOList converts an SDK SLOList to the CLI SLOList.
func fromSDKSLOList(s *sdkslo.SLOList) *SLOList {
	result := &SLOList{
		TotalCount:  s.TotalCount,
		NextPageKey: s.NextPageKey,
	}
	result.SLOs = make([]SLO, len(s.SLOs))
	for i, slo := range s.SLOs {
		result.SLOs[i] = fromSDKSLO(&slo)
	}
	return result
}

// fromSDKTemplate converts an SDK Template to the CLI Template.
func fromSDKTemplate(s *sdkslo.Template) Template {
	return Template{
		ID:              s.ID,
		Name:            s.Name,
		Description:     s.Description,
		BuiltIn:         s.BuiltIn,
		ApplicableScope: s.ApplicableScope,
		Indicator:       s.Indicator,
		Variables:       s.Variables,
		Version:         s.Version,
	}
}

// fromSDKTemplateList converts an SDK TemplateList to the CLI TemplateList.
func fromSDKTemplateList(s *sdkslo.TemplateList) *TemplateList {
	result := &TemplateList{
		TotalCount:  s.TotalCount,
		NextPageKey: s.NextPageKey,
	}
	result.Items = make([]Template, len(s.Items))
	for i, t := range s.Items {
		result.Items[i] = fromSDKTemplate(&t)
	}
	return result
}

// fromSDKEvaluationResult converts an SDK EvaluationResult to the CLI type.
func fromSDKEvaluationResult(s *sdkslo.EvaluationResult) EvaluationResult {
	return EvaluationResult{
		Criteria:    s.Criteria,
		Status:      s.Status,
		Value:       s.Value,
		ErrorBudget: s.ErrorBudget,
		Message:     s.Message,
	}
}

// fromSDKEvaluationResponse converts an SDK EvaluationResponse to the CLI type.
func fromSDKEvaluationResponse(s *sdkslo.EvaluationResponse) *EvaluationResponse {
	r := &EvaluationResponse{
		EvaluationToken: s.EvaluationToken,
		TTLSeconds:      s.TTLSeconds,
	}
	if s.Definition != nil {
		def := fromSDKSLO(s.Definition)
		r.Definition = &def
	}
	r.EvaluationResults = make([]EvaluationResult, len(s.EvaluationResults))
	for i, er := range s.EvaluationResults {
		r.EvaluationResults[i] = fromSDKEvaluationResult(&er)
	}
	return r
}

// Handler handles SLO resources.
// It delegates to the SDK handler and adds CLI-specific convenience methods.
type Handler struct {
	sdk *sdkslo.Handler
}

// NewHandler creates a new SLO handler
func NewHandler(c *client.Client) *Handler {
	return &Handler{
		sdk: sdkslo.NewHandler(httpclient.Wrap(c.HTTP())),
	}
}

// List lists all SLOs with automatic pagination
func (h *Handler) List(filter string, chunkSize int64) (*SLOList, error) {
	sdkResult, err := h.sdk.List(context.Background(), filter, chunkSize)
	if err != nil {
		return nil, err
	}
	return fromSDKSLOList(sdkResult), nil
}

// Get gets a specific SLO by ID
func (h *Handler) Get(id string) (*SLO, error) {
	sdkResult, err := h.sdk.Get(context.Background(), id)
	if err != nil {
		return nil, err
	}
	s := fromSDKSLO(sdkResult)
	return &s, nil
}

// Create creates a new SLO
func (h *Handler) Create(data []byte) (*SLO, error) {
	sdkResult, err := h.sdk.Create(context.Background(), data)
	if err != nil {
		return nil, err
	}
	s := fromSDKSLO(sdkResult)
	return &s, nil
}

// Update updates an existing SLO
func (h *Handler) Update(id string, version string, data []byte) error {
	return h.sdk.Update(context.Background(), id, version, data)
}

// Delete deletes an SLO
func (h *Handler) Delete(id string, version string) error {
	return h.sdk.Delete(context.Background(), id, version)
}

// ListTemplates lists all SLO templates
func (h *Handler) ListTemplates(filter string) (*TemplateList, error) {
	sdkResult, err := h.sdk.ListTemplates(context.Background(), filter)
	if err != nil {
		return nil, err
	}
	return fromSDKTemplateList(sdkResult), nil
}

// GetTemplate gets a specific SLO template by ID
func (h *Handler) GetTemplate(id string) (*Template, error) {
	sdkResult, err := h.sdk.GetTemplate(context.Background(), id)
	if err != nil {
		return nil, err
	}
	t := fromSDKTemplate(sdkResult)
	return &t, nil
}

// Evaluate starts an SLO evaluation
func (h *Handler) Evaluate(id string) (*EvaluationResponse, error) {
	sdkResult, err := h.sdk.Evaluate(context.Background(), id)
	if err != nil {
		return nil, err
	}
	return fromSDKEvaluationResponse(sdkResult), nil
}

// PollEvaluation polls for SLO evaluation results
func (h *Handler) PollEvaluation(token string, timeoutMs int) (*EvaluationResponse, error) {
	sdkResult, err := h.sdk.PollEvaluation(context.Background(), token, timeoutMs)
	if err != nil {
		return nil, err
	}
	return fromSDKEvaluationResponse(sdkResult), nil
}

// GetRaw gets an SLO as raw JSON bytes (for editing)
func (h *Handler) GetRaw(id string) ([]byte, error) {
	sloObj, err := h.sdk.Get(context.Background(), id)
	if err != nil {
		return nil, err
	}
	return json.MarshalIndent(sloObj, "", "  ")
}
