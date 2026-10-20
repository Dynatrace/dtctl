// Package workflow provides access to the Dynatrace Automation Workflow API.
package workflow

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/go-resty/resty/v2"

	"github.com/dynatrace-oss/dtctl/sdk/httpclient"
)

// AdminAccessScope is the scope the Automation API requires, on top of an
// operation's own scope, before it honors adminAccess=true. The tenant's IAM
// policy must grant the permission of the same name as well.
const AdminAccessScope = "automation:workflows:admin"

// Handler handles workflow resources.
type Handler struct {
	client      *httpclient.Client
	adminAccess bool
}

// NewHandler creates a new workflow handler.
func NewHandler(c *httpclient.Client) *Handler {
	return &Handler{client: c}
}

// WithAdminAccess returns a copy of h whose requests send adminAccess=true,
// which makes the Automation API act on every workflow in the environment
// rather than only those the caller owns or was shared. It requires
// AdminAccessScope.
func (h *Handler) WithAdminAccess(on bool) *Handler {
	c := *h
	c.adminAccess = on
	return &c
}

// request starts a request, carrying adminAccess=true when h was built with
// WithAdminAccess. Every workflow endpoint this handler calls accepts it.
func (h *Handler) request(ctx context.Context) *resty.Request {
	return newRequest(ctx, h.client, h.adminAccess)
}

func newRequest(ctx context.Context, c *httpclient.Client, adminAccess bool) *resty.Request {
	req := c.HTTP().R().SetContext(ctx)
	if adminAccess {
		req.SetQueryParam("adminAccess", "true")
	}
	return req
}

// Workflow represents a workflow resource.
type Workflow struct {
	ID                   string                 `json:"id"`
	Title                string                 `json:"title"`
	IsDeployed           bool                   `json:"isDeployed"`
	Description          string                 `json:"description,omitempty"`
	Actor                string                 `json:"actor,omitempty"`
	Owner                string                 `json:"owner,omitempty"`
	OwnerType            string                 `json:"ownerType,omitempty"`
	Private              bool                   `json:"isPrivate"`
	SchemaVersion        int                    `json:"schemaVersion,omitempty"`
	Trigger              map[string]interface{} `json:"trigger,omitempty"`
	Result               *string                `json:"result,omitempty"`
	Type                 string                 `json:"type,omitempty"`
	Input                map[string]interface{} `json:"input,omitempty"`
	HourlyExecutionLimit *int                   `json:"hourlyExecutionLimit,omitempty"`
	Guide                *string                `json:"guide,omitempty"`
	Tasks                map[string]interface{} `json:"tasks"`
}

// WorkflowList represents a list of workflows.
type WorkflowList struct {
	Count   int        `json:"count"`
	Results []Workflow `json:"results"`
}

// WorkflowFilters contains filter options for listing workflows.
type WorkflowFilters struct {
	Owner       string // Filter by owner ID (user ID)
	Search      string // Full-text search on title
	Type        string // Workflow type: STANDARD or SIMPLE
	TriggerType string // Trigger type: Manual, Schedule, Event, Workflow
	Fields      string // Optional field projection (comma-separated); empty fetches full objects
}

// List retrieves workflows with optional filters.
// chunkSize controls page size; 0 returns only the first page.
// limit caps the total number of results; 0 means unlimited.
func (h *Handler) List(ctx context.Context, filters WorkflowFilters, chunkSize, limit int64) (*WorkflowList, error) {
	var all []Workflow
	var totalCount int
	offset := 0

	for {
		// Per-request page size: chunkSize, narrowed to the remaining budget when a limit is set.
		pageSize := chunkSize
		if limit > 0 {
			remaining := limit - int64(len(all))
			if remaining <= 0 {
				break
			}
			// chunkSize==0 (single-page mode) combined with a limit: use the limit
			// as the page size so we fetch exactly N in one request, then break.
			if pageSize == 0 || remaining < pageSize {
				pageSize = remaining
			}
		}

		req := h.request(ctx)

		if filters.Fields != "" {
			req.SetQueryParam("fields", filters.Fields)
		}
		if filters.Owner != "" {
			req.SetQueryParam("owner", filters.Owner)
		}
		if filters.Search != "" {
			req.SetQueryParam("search", filters.Search)
		}
		if filters.Type != "" {
			req.SetQueryParam("type", filters.Type)
		}
		if filters.TriggerType != "" {
			req.SetQueryParam("triggerType", filters.TriggerType)
		}

		if pageSize > 0 {
			req.SetQueryParam("limit", fmt.Sprintf("%d", pageSize))
			if offset > 0 {
				req.SetQueryParam("offset", fmt.Sprintf("%d", offset))
			}
		}

		resp, err := req.Get("/platform/automation/v1/workflows")
		if err != nil {
			return nil, fmt.Errorf("list workflows: %w", err)
		}
		if err := httpclient.CheckResponse(resp); err != nil {
			return nil, fmt.Errorf("list workflows: %w", err)
		}

		var page WorkflowList
		if err := json.Unmarshal(resp.Body(), &page); err != nil {
			return nil, fmt.Errorf("list workflows: parse response: %w", err)
		}

		totalCount = page.Count
		all = append(all, page.Results...)

		if limit > 0 && int64(len(all)) >= limit {
			break
		}
		// chunkSize == 0 means single-page mode (no pagination loop).
		if chunkSize == 0 || len(all) >= totalCount || len(page.Results) == 0 {
			break
		}
		offset += len(page.Results)
	}

	// Defensive: the loop already caps well-behaved servers, but a server that
	// ignores the limit param could over-return — trim to the requested limit.
	if limit > 0 && int64(len(all)) > limit {
		all = all[:limit]
	}

	return &WorkflowList{Count: totalCount, Results: all}, nil
}

// Get retrieves a specific workflow.
func (h *Handler) Get(ctx context.Context, id string) (*Workflow, error) {
	resp, err := h.request(ctx).
		Get(fmt.Sprintf("/platform/automation/v1/workflows/%s", httpclient.PathSegment(id)))
	if err != nil {
		return nil, fmt.Errorf("get workflow: %w", err)
	}

	if err := httpclient.CheckResponse(resp); err != nil {
		return nil, fmt.Errorf("get workflow: %w", err)
	}

	var result Workflow
	if err := json.Unmarshal(resp.Body(), &result); err != nil {
		return nil, fmt.Errorf("get workflow: parse response: %w", err)
	}

	return &result, nil
}

// GetRaw retrieves a workflow as raw JSON bytes.
func (h *Handler) GetRaw(ctx context.Context, id string) ([]byte, error) {
	resp, err := h.request(ctx).
		Get(fmt.Sprintf("/platform/automation/v1/workflows/%s", httpclient.PathSegment(id)))
	if err != nil {
		return nil, fmt.Errorf("get workflow: %w", err)
	}

	if err := httpclient.CheckResponse(resp); err != nil {
		return nil, fmt.Errorf("get workflow: %w", err)
	}

	return resp.Body(), nil
}

// Delete deletes a workflow.
func (h *Handler) Delete(ctx context.Context, id string) error {
	resp, err := h.request(ctx).
		Delete(fmt.Sprintf("/platform/automation/v1/workflows/%s", httpclient.PathSegment(id)))
	if err != nil {
		return fmt.Errorf("delete workflow: %w", err)
	}

	if err := httpclient.CheckResponse(resp); err != nil {
		return fmt.Errorf("delete workflow: %w", err)
	}

	return nil
}

// Update updates a workflow.
func (h *Handler) Update(ctx context.Context, id string, data []byte) (*Workflow, error) {
	resp, err := h.request(ctx).
		SetHeader("Content-Type", "application/json").
		SetBody(data).
		Put(fmt.Sprintf("/platform/automation/v1/workflows/%s", httpclient.PathSegment(id)))
	if err != nil {
		return nil, fmt.Errorf("update workflow: %w", err)
	}

	if err := httpclient.CheckResponse(resp); err != nil {
		return nil, fmt.Errorf("update workflow: %w", err)
	}

	var result Workflow
	if err := json.Unmarshal(resp.Body(), &result); err != nil {
		return nil, fmt.Errorf("update workflow: parse response: %w", err)
	}

	return &result, nil
}

// Create creates a new workflow.
func (h *Handler) Create(ctx context.Context, data []byte) (*Workflow, error) {
	resp, err := h.request(ctx).
		SetHeader("Content-Type", "application/json").
		SetBody(data).
		Post("/platform/automation/v1/workflows")
	if err != nil {
		return nil, fmt.Errorf("create workflow: %w", err)
	}

	if err := httpclient.CheckResponse(resp); err != nil {
		return nil, fmt.Errorf("create workflow: %w", err)
	}

	var result Workflow
	if err := json.Unmarshal(resp.Body(), &result); err != nil {
		return nil, fmt.Errorf("create workflow: parse response: %w", err)
	}

	return &result, nil
}

// HistoryRecord represents a workflow version history record.
type HistoryRecord struct {
	Version     int    `json:"version"`
	User        string `json:"user"`
	DateCreated string `json:"dateCreated"`
}

// HistoryList represents a paginated list of history records.
type HistoryList struct {
	Count   int             `json:"count"`
	Results []HistoryRecord `json:"results"`
}

// ListHistory retrieves version history for a workflow.
func (h *Handler) ListHistory(ctx context.Context, workflowID string) (*HistoryList, error) {
	resp, err := h.request(ctx).
		Get(fmt.Sprintf("/platform/automation/v1/workflows/%s/history", httpclient.PathSegment(workflowID)))
	if err != nil {
		return nil, fmt.Errorf("list workflow history: %w", err)
	}

	if err := httpclient.CheckResponse(resp); err != nil {
		return nil, fmt.Errorf("list workflow history: %w", err)
	}

	var result HistoryList
	if err := json.Unmarshal(resp.Body(), &result); err != nil {
		return nil, fmt.Errorf("list workflow history: parse response: %w", err)
	}

	return &result, nil
}

// GetHistoryRecord retrieves a specific version of a workflow.
func (h *Handler) GetHistoryRecord(ctx context.Context, workflowID string, version int) (*Workflow, error) {
	resp, err := h.request(ctx).
		Get(fmt.Sprintf("/platform/automation/v1/workflows/%s/history/%d", httpclient.PathSegment(workflowID), version))
	if err != nil {
		return nil, fmt.Errorf("get workflow history record: %w", err)
	}

	if err := httpclient.CheckResponse(resp); err != nil {
		return nil, fmt.Errorf("get workflow history record: %w", err)
	}

	var result Workflow
	if err := json.Unmarshal(resp.Body(), &result); err != nil {
		return nil, fmt.Errorf("get workflow history record: parse response: %w", err)
	}

	return &result, nil
}

// RestoreHistory restores a workflow to a specific version.
func (h *Handler) RestoreHistory(ctx context.Context, workflowID string, version int) (*Workflow, error) {
	resp, err := h.request(ctx).
		Post(fmt.Sprintf("/platform/automation/v1/workflows/%s/history/%d/restore", httpclient.PathSegment(workflowID), version))
	if err != nil {
		return nil, fmt.Errorf("restore workflow: %w", err)
	}

	if err := httpclient.CheckResponse(resp); err != nil {
		return nil, fmt.Errorf("restore workflow: %w", err)
	}

	var result Workflow
	if err := json.Unmarshal(resp.Body(), &result); err != nil {
		return nil, fmt.Errorf("restore workflow: parse response: %w", err)
	}

	return &result, nil
}
