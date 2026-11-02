package settings

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/Dynatrace/dtctl/sdk/httpclient"
)

// Handler handles settings resources.
type Handler struct {
	client *httpclient.Client
}

// NewHandler creates a new settings handler.
func NewHandler(c *httpclient.Client) *Handler {
	return &Handler{client: c}
}

// Schema represents a settings schema.
type Schema struct {
	SchemaID    string         `json:"schemaId"`
	DisplayName string         `json:"displayName"`
	Description string         `json:"description,omitempty"`
	Version     string         `json:"version"`
	MultiObject bool           `json:"multiObject,omitempty"`
	Ordered     bool           `json:"ordered,omitempty"`
	Properties  map[string]any `json:"properties,omitempty"`
	Scopes      []string       `json:"scopes,omitempty"`
}

// SchemaList represents a list of schemas.
type SchemaList struct {
	Items      []Schema `json:"items"`
	TotalCount int      `json:"totalCount"`
}

// SettingsObject represents a settings object.
type SettingsObject struct {
	ObjectID         string            `json:"objectId"`
	SchemaID         string            `json:"schemaId"`
	SchemaVersion    string            `json:"schemaVersion,omitempty"`
	Scope            string            `json:"scope"`
	ExternalID       string            `json:"externalId,omitempty"`
	Summary          string            `json:"summary,omitempty"`
	Value            map[string]any    `json:"value,omitempty"`
	ModificationInfo *ModificationInfo `json:"modificationInfo,omitempty"`
}

// ModificationInfo contains modification timestamps.
type ModificationInfo struct {
	CreatedBy        string `json:"createdBy,omitempty"`
	CreatedTime      string `json:"createdTime,omitempty"`
	LastModifiedBy   string `json:"lastModifiedBy,omitempty"`
	LastModifiedTime string `json:"lastModifiedTime,omitempty"`
}

// SettingsObjectsList represents a list of settings objects.
type SettingsObjectsList struct {
	Items       []SettingsObject `json:"items"`
	TotalCount  int              `json:"totalCount"`
	NextPageKey string           `json:"nextPageKey,omitempty"`
}

// SettingsObjectCreate represents the request body for creating a settings object.
type SettingsObjectCreate struct {
	SchemaID      string         `json:"schemaId"`
	Scope         string         `json:"scope"`
	Value         map[string]any `json:"value"`
	SchemaVersion string         `json:"schemaVersion,omitempty"`
	ExternalID    string         `json:"externalId,omitempty"`
}

// SettingsObjectResponse represents the response from creating/updating a settings object.
type SettingsObjectResponse struct {
	ObjectID string `json:"objectId"`
	Code     int    `json:"code,omitempty"`
	Error    *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error,omitempty"`
}

// CreateResponse represents the response from batch create.
type CreateResponse struct {
	Items []SettingsObjectResponse `json:"items"`
}

// ListSchemas lists all available settings schemas.
func (h *Handler) ListSchemas(ctx context.Context) (*SchemaList, error) {
	resp, err := h.client.HTTP().R().SetContext(ctx).
		Get("/platform/classic/environment-api/v2/settings/schemas")
	if err != nil {
		return nil, fmt.Errorf("list schemas: %w", err)
	}
	if err := httpclient.CheckResponse(resp); err != nil {
		return nil, fmt.Errorf("list schemas: %w", err)
	}

	var result SchemaList
	if err := json.Unmarshal(resp.Body(), &result); err != nil {
		return nil, fmt.Errorf("parse schemas response: %w", err)
	}

	return &result, nil
}

// GetSchema gets a specific schema definition.
func (h *Handler) GetSchema(ctx context.Context, schemaID string) (map[string]any, error) {
	resp, err := h.client.HTTP().R().SetContext(ctx).
		Get(fmt.Sprintf("/platform/classic/environment-api/v2/settings/schemas/%s", httpclient.PathSegment(schemaID)))
	if err != nil {
		return nil, fmt.Errorf("get schema %q: %w", schemaID, err)
	}
	if err := httpclient.CheckResponse(resp); err != nil {
		return nil, fmt.Errorf("get schema %q: %w", schemaID, err)
	}

	var result map[string]any
	if err := json.Unmarshal(resp.Body(), &result); err != nil {
		return nil, fmt.Errorf("parse schema response: %w", err)
	}

	return result, nil
}

// ListObjects lists settings objects for a schema with automatic pagination.
func (h *Handler) ListObjects(ctx context.Context, schemaID, scope string, chunkSize int64) (*SettingsObjectsList, error) {
	var allItems []SettingsObject
	var totalCount int
	nextPageKey := ""

	for {
		req := h.client.HTTP().R().SetContext(ctx)

		params := httpclient.PaginationParams{
			Style:         httpclient.PaginationSettingsAPI,
			PageKeyParam:  "nextPageKey",
			PageSizeParam: "pageSize",
			NextPageKey:   nextPageKey,
			PageSize:      chunkSize,
			Filters:       map[string]string{"schemaIds": schemaID, "scopes": scope},
		}.QueryParams()

		req.SetQueryParamsFromValues(params)

		// Settings API embeds ALL query params (including fields) in the nextPageKey
		// token. Sending fields on page 2+ causes HTTP 400. Only set on first request.
		if nextPageKey == "" {
			req.SetQueryParam("fields", "objectId,scope,schemaId,schemaVersion,externalId,summary,value,modificationInfo")
		}

		resp, err := req.Get("/platform/classic/environment-api/v2/settings/objects")
		if err != nil {
			return nil, fmt.Errorf("list settings objects: %w", err)
		}
		if err := httpclient.CheckResponse(resp); err != nil {
			return nil, fmt.Errorf("list settings objects for schema %q: %w", schemaID, err)
		}

		var result SettingsObjectsList
		if err := json.Unmarshal(resp.Body(), &result); err != nil {
			return nil, fmt.Errorf("parse settings objects response: %w", err)
		}

		// API bug workaround: The API returns empty schemaId field, so populate it from the query parameter
		if schemaID != "" {
			for i := range result.Items {
				if result.Items[i].SchemaID == "" {
					result.Items[i].SchemaID = schemaID
				}
			}
		}

		allItems = append(allItems, result.Items...)
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

	return &SettingsObjectsList{
		Items:      allItems,
		TotalCount: totalCount,
	}, nil
}

// Get gets a specific settings object by objectId.
func (h *Handler) Get(ctx context.Context, objectID string) (*SettingsObject, error) {
	resp, err := h.client.HTTP().R().SetContext(ctx).
		SetQueryParam("fields", "objectId,scope,schemaId,schemaVersion,externalId,summary,value,modificationInfo").
		Get(fmt.Sprintf("/platform/classic/environment-api/v2/settings/objects/%s", httpclient.PathSegment(objectID)))
	if err != nil {
		return nil, fmt.Errorf("get settings object %q: %w", objectID, err)
	}
	if err := httpclient.CheckResponse(resp); err != nil {
		return nil, fmt.Errorf("get settings object %q: %w", objectID, err)
	}

	var result SettingsObject
	if err := json.Unmarshal(resp.Body(), &result); err != nil {
		return nil, fmt.Errorf("parse settings object response: %w", err)
	}

	return &result, nil
}

// ValidateCreate validates a settings object without creating it.
func (h *Handler) ValidateCreate(ctx context.Context, req SettingsObjectCreate) error {
	body := []SettingsObjectCreate{req}

	resp, err := h.client.HTTP().R().SetContext(ctx).
		SetBody(body).
		SetQueryParam("validateOnly", "true").
		Post("/platform/classic/environment-api/v2/settings/objects")
	if err != nil {
		return fmt.Errorf("validate settings object: %w", err)
	}
	if err := httpclient.CheckResponse(resp); err != nil {
		return fmt.Errorf("validate settings object for schema %q: %w", req.SchemaID, err)
	}

	return nil
}

// Create creates a new settings object.
func (h *Handler) Create(ctx context.Context, req SettingsObjectCreate) (*SettingsObjectResponse, error) {
	body := []SettingsObjectCreate{req}

	resp, err := h.client.HTTP().R().SetContext(ctx).
		SetBody(body).
		Post("/platform/classic/environment-api/v2/settings/objects")
	if err != nil {
		return nil, fmt.Errorf("create settings object: %w", err)
	}
	if err := httpclient.CheckResponse(resp); err != nil {
		return nil, fmt.Errorf("create settings object for schema %q: %w", req.SchemaID, err)
	}

	var createResp []SettingsObjectResponse
	if err := json.Unmarshal(resp.Body(), &createResp); err != nil {
		return nil, fmt.Errorf("parse create response: %w", err)
	}

	if len(createResp) == 0 {
		return nil, fmt.Errorf("no items returned in create response")
	}

	result := &createResp[0]
	if result.Error != nil {
		return nil, fmt.Errorf("create failed: %s", result.Error.Message)
	}

	return result, nil
}

// ValidateUpdate validates a settings object update without applying it.
// The schemaVersion is used for the If-Match header (obtain it from Get).
func (h *Handler) ValidateUpdate(ctx context.Context, objectID, schemaVersion string, value map[string]any) error {
	body := map[string]any{"value": value}

	resp, err := h.client.HTTP().R().SetContext(ctx).
		SetBody(body).
		SetHeader("If-Match", schemaVersion).
		SetQueryParam("validateOnly", "true").
		Put(fmt.Sprintf("/platform/classic/environment-api/v2/settings/objects/%s", httpclient.PathSegment(objectID)))
	if err != nil {
		return fmt.Errorf("validate settings object update: %w", err)
	}
	if err := httpclient.CheckResponse(resp); err != nil {
		return fmt.Errorf("validate settings object %q update: %w", objectID, err)
	}

	return nil
}

// Update updates an existing settings object.
// The schemaVersion is used for the If-Match header (obtain it from Get).
func (h *Handler) Update(ctx context.Context, objectID, schemaVersion string, value map[string]any) error {
	body := map[string]any{"value": value}

	resp, err := h.client.HTTP().R().SetContext(ctx).
		SetBody(body).
		SetHeader("If-Match", schemaVersion).
		Put(fmt.Sprintf("/platform/classic/environment-api/v2/settings/objects/%s", httpclient.PathSegment(objectID)))
	if err != nil {
		return fmt.Errorf("update settings object %q: %w", objectID, err)
	}
	if err := httpclient.CheckResponse(resp); err != nil {
		return fmt.Errorf("update settings object %q: %w", objectID, err)
	}

	return nil
}

// Delete deletes a settings object.
// The schemaVersion is used for the If-Match header (obtain it from Get).
func (h *Handler) Delete(ctx context.Context, objectID, schemaVersion string) error {
	resp, err := h.client.HTTP().R().SetContext(ctx).
		SetHeader("If-Match", schemaVersion).
		Delete(fmt.Sprintf("/platform/classic/environment-api/v2/settings/objects/%s", httpclient.PathSegment(objectID)))
	if err != nil {
		return fmt.Errorf("delete settings object %q: %w", objectID, err)
	}
	if err := httpclient.CheckResponse(resp); err != nil {
		return fmt.Errorf("delete settings object %q: %w", objectID, err)
	}

	return nil
}
