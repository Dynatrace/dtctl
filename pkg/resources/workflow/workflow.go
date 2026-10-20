package workflow

import (
	"context"

	"github.com/dynatrace-oss/dtctl/pkg/client"
	sdkworkflow "github.com/dynatrace-oss/dtctl/sdk/api/workflow"
	"github.com/dynatrace-oss/dtctl/sdk/httpclient"
)

// Re-export SDK types that have no table tags.
type WorkflowFilters = sdkworkflow.WorkflowFilters

// Workflow represents a workflow resource (CLI version with table tags).
type Workflow struct {
	ID                   string                 `json:"id" yaml:"id" table:"ID"`
	Title                string                 `json:"title" yaml:"title" table:"TITLE"`
	IsDeployed           bool                   `json:"isDeployed" yaml:"isDeployed" table:"DEPLOYED"`
	Description          string                 `json:"description,omitempty" yaml:"description,omitempty" table:"DESCRIPTION,wide"`
	Actor                string                 `json:"actor,omitempty" yaml:"actor,omitempty" table:"-"`
	Owner                string                 `json:"owner,omitempty" yaml:"owner,omitempty" table:"-"`
	OwnerType            string                 `json:"ownerType,omitempty" yaml:"ownerType,omitempty" table:"-"`
	Private              bool                   `json:"isPrivate" yaml:"isPrivate" table:"-"`
	SchemaVersion        int                    `json:"schemaVersion,omitempty" yaml:"schemaVersion,omitempty" table:"-"`
	Trigger              map[string]interface{} `json:"trigger,omitempty" yaml:"trigger,omitempty" table:"-"`
	TriggerType          string                 `json:"triggerType,omitempty" yaml:"triggerType,omitempty" table:"TRIGGER"`
	Result               *string                `json:"result,omitempty" yaml:"result,omitempty" table:"-"`
	Type                 string                 `json:"type,omitempty" yaml:"type,omitempty" table:"TYPE"`
	Input                map[string]interface{} `json:"input,omitempty" yaml:"input,omitempty" table:"-"`
	HourlyExecutionLimit *int                   `json:"hourlyExecutionLimit,omitempty" yaml:"hourlyExecutionLimit,omitempty" table:"-"`
	Guide                *string                `json:"guide,omitempty" yaml:"guide,omitempty" table:"-"`
	Tasks                map[string]interface{} `json:"tasks,omitempty" yaml:"tasks,omitempty" table:"-"`
}

// WorkflowList represents a list of workflows.
type WorkflowList struct {
	Count   int        `json:"count"`
	Results []Workflow `json:"results"`
}

// HistoryRecord represents a workflow version history record (CLI version with table tags).
type HistoryRecord struct {
	Version     int    `json:"version" table:"VERSION"`
	User        string `json:"user" table:"USER"`
	DateCreated string `json:"dateCreated" table:"CREATED"`
}

// HistoryList represents a paginated list of history records.
type HistoryList struct {
	Count   int             `json:"count"`
	Results []HistoryRecord `json:"results"`
}

// triggerType derives a human-readable trigger type from the trigger map.
// Matches the UI logic: schedule → "Schedule", eventTrigger → "Event", else "Manual".
func triggerType(t map[string]interface{}) string {
	if t == nil {
		return "Manual"
	}
	if _, ok := t["schedule"]; ok {
		return "Schedule"
	}
	if _, ok := t["eventTrigger"]; ok {
		return "Event"
	}
	return "Manual"
}

// fromSDKWorkflow converts an SDK Workflow to a CLI Workflow.
func fromSDKWorkflow(s *sdkworkflow.Workflow) Workflow {
	return Workflow{
		ID:                   s.ID,
		Title:                s.Title,
		IsDeployed:           s.IsDeployed,
		Description:          s.Description,
		Actor:                s.Actor,
		Owner:                s.Owner,
		OwnerType:            s.OwnerType,
		Private:              s.Private,
		SchemaVersion:        s.SchemaVersion,
		Trigger:              s.Trigger,
		TriggerType:          triggerType(s.Trigger),
		Result:               s.Result,
		Type:                 s.Type,
		Input:                s.Input,
		HourlyExecutionLimit: s.HourlyExecutionLimit,
		Guide:                s.Guide,
		Tasks:                s.Tasks,
	}
}

// fromSDKHistoryRecord converts an SDK HistoryRecord to a CLI HistoryRecord.
func fromSDKHistoryRecord(s *sdkworkflow.HistoryRecord) HistoryRecord {
	return HistoryRecord{
		Version:     s.Version,
		User:        s.User,
		DateCreated: s.DateCreated,
	}
}

// Handler handles workflow resources.
// It delegates to the SDK handler and adds CLI-specific convenience methods.
type Handler struct {
	sdk *sdkworkflow.Handler
}

// NewHandler creates a new workflow handler
func NewHandler(c *client.Client) *Handler {
	return &Handler{
		sdk: sdkworkflow.NewHandler(httpclient.Wrap(c.HTTP())),
	}
}

// WithAdminAccess returns a copy of h that acts on every workflow in the
// environment, not only the caller's own (adminAccess=true).
func (h *Handler) WithAdminAccess(on bool) *Handler {
	return &Handler{sdk: h.sdk.WithAdminAccess(on)}
}

// listFields is the field projection used for workflow listing: it omits heavy
// fields (tasks, input, result, guide) not shown in list output. This is a CLI
// display concern, so it lives here rather than in the SDK.
const listFields = "id,title,isDeployed,description,type,owner,ownerType,actor,isPrivate,schemaVersion,hourlyExecutionLimit,trigger"

// List retrieves workflows with optional filters.
// chunkSize controls page size (0 = first page only); limit caps total results (0 = unlimited).
func (h *Handler) List(filters WorkflowFilters, chunkSize, limit int64) (*WorkflowList, error) {
	// Callers may pass a custom projection; default to the lean list set.
	if filters.Fields == "" {
		filters.Fields = listFields
	}
	sdkResult, err := h.sdk.List(context.Background(), filters, chunkSize, limit)
	if err != nil {
		return nil, err
	}
	results := make([]Workflow, len(sdkResult.Results))
	for i := range sdkResult.Results {
		results[i] = fromSDKWorkflow(&sdkResult.Results[i])
	}
	return &WorkflowList{Count: sdkResult.Count, Results: results}, nil
}

// Get retrieves a specific workflow
func (h *Handler) Get(id string) (*Workflow, error) {
	sdkResult, err := h.sdk.Get(context.Background(), id)
	if err != nil {
		return nil, err
	}
	w := fromSDKWorkflow(sdkResult)
	return &w, nil
}

// Delete deletes a workflow
func (h *Handler) Delete(id string) error {
	return h.sdk.Delete(context.Background(), id)
}

// GetRaw retrieves a workflow as raw JSON (for editing)
func (h *Handler) GetRaw(id string) ([]byte, error) {
	return h.sdk.GetRaw(context.Background(), id)
}

// Update updates a workflow
func (h *Handler) Update(id string, data []byte) (*Workflow, error) {
	sdkResult, err := h.sdk.Update(context.Background(), id, data)
	if err != nil {
		return nil, err
	}
	w := fromSDKWorkflow(sdkResult)
	return &w, nil
}

// Create creates a new workflow
func (h *Handler) Create(data []byte) (*Workflow, error) {
	sdkResult, err := h.sdk.Create(context.Background(), data)
	if err != nil {
		return nil, err
	}
	w := fromSDKWorkflow(sdkResult)
	return &w, nil
}

// ListHistory retrieves version history for a workflow
func (h *Handler) ListHistory(workflowID string) (*HistoryList, error) {
	sdkResult, err := h.sdk.ListHistory(context.Background(), workflowID)
	if err != nil {
		return nil, err
	}
	results := make([]HistoryRecord, len(sdkResult.Results))
	for i := range sdkResult.Results {
		results[i] = fromSDKHistoryRecord(&sdkResult.Results[i])
	}
	return &HistoryList{Count: sdkResult.Count, Results: results}, nil
}

// GetHistoryRecord retrieves a specific version of a workflow
func (h *Handler) GetHistoryRecord(workflowID string, version int) (*Workflow, error) {
	sdkResult, err := h.sdk.GetHistoryRecord(context.Background(), workflowID, version)
	if err != nil {
		return nil, err
	}
	w := fromSDKWorkflow(sdkResult)
	return &w, nil
}

// RestoreHistory restores a workflow to a specific version
func (h *Handler) RestoreHistory(workflowID string, version int) (*Workflow, error) {
	sdkResult, err := h.sdk.RestoreHistory(context.Background(), workflowID, version)
	if err != nil {
		return nil, err
	}
	w := fromSDKWorkflow(sdkResult)
	return &w, nil
}
