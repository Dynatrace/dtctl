package slo

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/Dynatrace/dtctl/sdk/httpclient"
)

// Handler handles SLO resources
type Handler struct {
	client *httpclient.Client
}

// NewHandler creates a new SLO handler
func NewHandler(c *httpclient.Client) *Handler {
	return &Handler{client: c}
}

// SLO represents a service-level objective
type SLO struct {
	ID          string                 `json:"id"`
	Name        string                 `json:"name"`
	Description string                 `json:"description,omitempty"`
	Version     string                 `json:"version,omitempty"`
	Criteria    []Criteria             `json:"criteria,omitempty"`
	Tags        []string               `json:"tags,omitempty"`
	CustomSli   map[string]interface{} `json:"customSli,omitempty"`
	ExternalID  string                 `json:"externalId,omitempty"`
}

// Criteria represents SLO criteria
type Criteria struct {
	TimeframeFrom string   `json:"timeframeFrom"`
	TimeframeTo   string   `json:"timeframeTo,omitempty"`
	Target        float64  `json:"target"`
	Warning       *float64 `json:"warning,omitempty"`
}

// SLOList represents a list of SLOs
type SLOList struct {
	SLOs        []SLO  `json:"slos"`
	TotalCount  int    `json:"totalCount"`
	NextPageKey string `json:"nextPageKey,omitempty"`
}

// Template represents an SLO objective template
type Template struct {
	ID              string             `json:"id"`
	Name            string             `json:"name"`
	Description     string             `json:"description,omitempty"`
	BuiltIn         bool               `json:"builtIn"`
	ApplicableScope string             `json:"applicableScope,omitempty"`
	Indicator       string             `json:"indicator,omitempty"`
	Variables       []TemplateVariable `json:"variables,omitempty"`
	Version         string             `json:"version,omitempty"`
}

// TemplateVariable represents a variable in an SLO template
type TemplateVariable struct {
	Name  string `json:"name"`
	Scope string `json:"scope"`
}

// TemplateList represents a list of templates
type TemplateList struct {
	Items       []Template `json:"items"`
	TotalCount  int        `json:"totalCount"`
	NextPageKey string     `json:"nextPageKey,omitempty"`
}

// EvaluationResult represents an SLO evaluation result
type EvaluationResult struct {
	Criteria    string   `json:"criteria"`
	Status      string   `json:"status"`
	Value       *float64 `json:"value,omitempty"`
	ErrorBudget *float64 `json:"errorBudget,omitempty"`
	Message     string   `json:"message,omitempty"`
}

// EvaluationResponse represents the response from SLO evaluation
type EvaluationResponse struct {
	Definition        *SLO               `json:"definition,omitempty"`
	EvaluationResults []EvaluationResult `json:"evaluationResults,omitempty"`
	EvaluationToken   string             `json:"evaluationToken,omitempty"`
	TTLSeconds        int64              `json:"ttlSeconds,omitempty"`
}

// List lists all SLOs with automatic pagination
func (h *Handler) List(ctx context.Context, filter string, chunkSize int64) (*SLOList, error) {
	var allSLOs []SLO
	var totalCount int
	nextPageKey := ""

	for {
		var result SLOList
		req := h.client.HTTP().R().SetContext(ctx)

		params := httpclient.PaginationParams{
			Style:         httpclient.PaginationDefault,
			PageKeyParam:  "page-key",
			PageSizeParam: "page-size",
			NextPageKey:   nextPageKey,
			PageSize:      chunkSize,
			Filters:       map[string]string{"filter": filter},
		}.QueryParams()
		req.SetQueryParamsFromValues(params)

		resp, err := req.Get("/platform/slo/v1/slos")
		if err != nil {
			return nil, fmt.Errorf("failed to list SLOs: %w", err)
		}

		if err := httpclient.CheckResponse(resp); err != nil {
			return nil, fmt.Errorf("failed to list SLOs: %w", err)
		}

		if err := json.Unmarshal(resp.Body(), &result); err != nil {
			return nil, fmt.Errorf("list SLOs: parse response: %w", err)
		}

		allSLOs = append(allSLOs, result.SLOs...)
		totalCount = result.TotalCount

		// If chunking is disabled (chunkSize == 0), return first page only
		if chunkSize == 0 {
			return &result, nil
		}

		// Check if there are more pages
		if result.NextPageKey == "" {
			break
		}
		nextPageKey = result.NextPageKey
	}

	return &SLOList{
		SLOs:       allSLOs,
		TotalCount: totalCount,
	}, nil
}

// Get gets a specific SLO by ID
func (h *Handler) Get(ctx context.Context, id string) (*SLO, error) {
	resp, err := h.client.HTTP().R().SetContext(ctx).
		Get(fmt.Sprintf("/platform/slo/v1/slos/%s", httpclient.PathSegment(id)))

	if err != nil {
		return nil, fmt.Errorf("failed to get SLO: %w", err)
	}

	if err := httpclient.CheckResponse(resp); err != nil {
		return nil, fmt.Errorf("failed to get SLO: %w", err)
	}

	var result SLO
	if err := json.Unmarshal(resp.Body(), &result); err != nil {
		return nil, fmt.Errorf("get SLO: parse response: %w", err)
	}

	return &result, nil
}

// Create creates a new SLO
func (h *Handler) Create(ctx context.Context, data []byte) (*SLO, error) {
	resp, err := h.client.HTTP().R().SetContext(ctx).
		SetBody(data).
		SetHeader("Content-Type", "application/json").
		Post("/platform/slo/v1/slos")

	if err != nil {
		return nil, fmt.Errorf("failed to create SLO: %w", err)
	}

	if err := httpclient.CheckResponse(resp); err != nil {
		return nil, fmt.Errorf("failed to create SLO: %w", err)
	}

	var result SLO
	if err := json.Unmarshal(resp.Body(), &result); err != nil {
		return nil, fmt.Errorf("create SLO: parse response: %w", err)
	}

	return &result, nil
}

// Update updates an existing SLO
func (h *Handler) Update(ctx context.Context, id string, version string, data []byte) error {
	resp, err := h.client.HTTP().R().SetContext(ctx).
		SetBody(data).
		SetHeader("Content-Type", "application/json").
		SetQueryParam("optimistic-locking-version", version).
		Put(fmt.Sprintf("/platform/slo/v1/slos/%s", httpclient.PathSegment(id)))

	if err != nil {
		return fmt.Errorf("failed to update SLO: %w", err)
	}

	if err := httpclient.CheckResponse(resp); err != nil {
		return fmt.Errorf("failed to update SLO: %w", err)
	}

	return nil
}

// Delete deletes an SLO
func (h *Handler) Delete(ctx context.Context, id string, version string) error {
	resp, err := h.client.HTTP().R().SetContext(ctx).
		SetQueryParam("optimistic-locking-version", version).
		Delete(fmt.Sprintf("/platform/slo/v1/slos/%s", httpclient.PathSegment(id)))

	if err != nil {
		return fmt.Errorf("failed to delete SLO: %w", err)
	}

	if err := httpclient.CheckResponse(resp); err != nil {
		return fmt.Errorf("failed to delete SLO: %w", err)
	}

	return nil
}

// ListTemplates lists all SLO templates
func (h *Handler) ListTemplates(ctx context.Context, filter string) (*TemplateList, error) {
	req := h.client.HTTP().R().SetContext(ctx)

	if filter != "" {
		req.SetQueryParam("filter", filter)
	}

	resp, err := req.Get("/platform/slo/v1/objective-templates")

	if err != nil {
		return nil, fmt.Errorf("failed to list SLO templates: %w", err)
	}

	if err := httpclient.CheckResponse(resp); err != nil {
		return nil, fmt.Errorf("failed to list SLO templates: %w", err)
	}

	var result TemplateList
	if err := json.Unmarshal(resp.Body(), &result); err != nil {
		return nil, fmt.Errorf("list SLO templates: parse response: %w", err)
	}

	return &result, nil
}

// GetTemplate gets a specific SLO template by ID
func (h *Handler) GetTemplate(ctx context.Context, id string) (*Template, error) {
	resp, err := h.client.HTTP().R().SetContext(ctx).
		Get(fmt.Sprintf("/platform/slo/v1/objective-templates/%s", httpclient.PathSegment(id)))

	if err != nil {
		return nil, fmt.Errorf("failed to get SLO template: %w", err)
	}

	if err := httpclient.CheckResponse(resp); err != nil {
		return nil, fmt.Errorf("failed to get SLO template: %w", err)
	}

	var result Template
	if err := json.Unmarshal(resp.Body(), &result); err != nil {
		return nil, fmt.Errorf("get SLO template: parse response: %w", err)
	}

	return &result, nil
}

// Evaluate starts an SLO evaluation
func (h *Handler) Evaluate(ctx context.Context, id string) (*EvaluationResponse, error) {
	body := map[string]interface{}{
		"id": id,
	}

	resp, err := h.client.HTTP().R().SetContext(ctx).
		SetBody(body).
		SetHeader("Content-Type", "application/json").
		Post("/platform/slo/v1/slos/evaluation:start")

	if err != nil {
		return nil, fmt.Errorf("failed to evaluate SLO: %w", err)
	}

	if err := httpclient.CheckResponse(resp); err != nil {
		return nil, fmt.Errorf("failed to evaluate SLO: %w", err)
	}

	var result EvaluationResponse
	if err := json.Unmarshal(resp.Body(), &result); err != nil {
		return nil, fmt.Errorf("evaluate SLO: parse response: %w", err)
	}

	return &result, nil
}

// PollEvaluation polls for SLO evaluation results
func (h *Handler) PollEvaluation(ctx context.Context, token string, timeoutMs int) (*EvaluationResponse, error) {
	req := h.client.HTTP().R().SetContext(ctx).
		SetQueryParam("evaluation-token", token)

	if timeoutMs > 0 {
		req.SetQueryParam("request-timeout-milliseconds", fmt.Sprintf("%d", timeoutMs))
	}

	resp, err := req.Get("/platform/slo/v1/slos/evaluation:poll")

	if err != nil {
		return nil, fmt.Errorf("failed to poll SLO evaluation: %w", err)
	}

	if err := httpclient.CheckResponse(resp); err != nil {
		return nil, fmt.Errorf("failed to poll SLO evaluation: %w", err)
	}

	var result EvaluationResponse
	if err := json.Unmarshal(resp.Body(), &result); err != nil {
		return nil, fmt.Errorf("poll SLO evaluation: parse response: %w", err)
	}

	return &result, nil
}
