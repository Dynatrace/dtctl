package edgeconnect

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/Dynatrace/dtctl/sdk/httpclient"
)

// Handler handles EdgeConnect resources.
type Handler struct {
	client *httpclient.Client
}

// NewHandler creates a new EdgeConnect handler.
func NewHandler(c *httpclient.Client) *Handler {
	return &Handler{client: c}
}

// EdgeConnect represents an EdgeConnect configuration.
type EdgeConnect struct {
	ID                         string            `json:"id,omitempty"`
	Name                       string            `json:"name"`
	HostPatterns               []string          `json:"hostPatterns,omitempty"`
	OAuthClientID              string            `json:"oauthClientId,omitempty"`
	OAuthClientSecret          string            `json:"oauthClientSecret,omitempty"`
	OAuthClientResource        string            `json:"oauthClientResource,omitempty"`
	ModificationInfo           *ModificationInfo `json:"modificationInfo,omitempty"`
	ManagedByDynatraceOperator bool              `json:"managedByDynatraceOperator,omitempty"`
	Metadata                   *Metadata         `json:"metadata,omitempty"`
}

// ModificationInfo contains modification timestamps.
type ModificationInfo struct {
	CreatedBy        string `json:"createdBy,omitempty"`
	CreatedTime      string `json:"createdTime,omitempty"`
	LastModifiedBy   string `json:"lastModifiedBy,omitempty"`
	LastModifiedTime string `json:"lastModifiedTime,omitempty"`
}

// Metadata contains additional metadata.
type Metadata struct {
	Version string `json:"version,omitempty"`
}

// EdgeConnectList represents a list of EdgeConnects.
type EdgeConnectList struct {
	EdgeConnects []EdgeConnect `json:"edgeConnects"`
	TotalCount   int           `json:"totalCount"`
	PageSize     int           `json:"pageSize"`
}

// List lists all EdgeConnect configurations.
func (h *Handler) List(ctx context.Context) (*EdgeConnectList, error) {
	resp, err := h.client.HTTP().R().SetContext(ctx).
		SetQueryParam("add-fields", "modificationInfo,metadata").
		Get("/platform/app-engine/edge-connect/v1/edge-connects")
	if err != nil {
		return nil, fmt.Errorf("list edge connects: %w", err)
	}
	if err := httpclient.CheckResponse(resp); err != nil {
		return nil, fmt.Errorf("list edge connects: %w", err)
	}

	var result EdgeConnectList
	if err := json.Unmarshal(resp.Body(), &result); err != nil {
		return nil, fmt.Errorf("list edge connects: parse response: %w", err)
	}

	return &result, nil
}

// Get gets a specific EdgeConnect by ID.
func (h *Handler) Get(ctx context.Context, edgeConnectID string) (*EdgeConnect, error) {
	resp, err := h.client.HTTP().R().SetContext(ctx).
		Get(fmt.Sprintf("/platform/app-engine/edge-connect/v1/edge-connects/%s", httpclient.PathSegment(edgeConnectID)))
	if err != nil {
		return nil, fmt.Errorf("get edge connect: %w", err)
	}
	if err := httpclient.CheckResponse(resp); err != nil {
		return nil, fmt.Errorf("get edge connect %q: %w", edgeConnectID, err)
	}

	var result EdgeConnect
	if err := json.Unmarshal(resp.Body(), &result); err != nil {
		return nil, fmt.Errorf("get edge connect: parse response: %w", err)
	}

	return &result, nil
}

// Create creates a new EdgeConnect.
func (h *Handler) Create(ctx context.Context, req EdgeConnect) (*EdgeConnect, error) {
	resp, err := h.client.HTTP().R().SetContext(ctx).
		SetBody(req).
		Post("/platform/app-engine/edge-connect/v1/edge-connects")
	if err != nil {
		return nil, fmt.Errorf("create edge connect: %w", err)
	}
	if err := httpclient.CheckResponse(resp); err != nil {
		return nil, fmt.Errorf("create edge connect: %w", err)
	}

	var result EdgeConnect
	if err := json.Unmarshal(resp.Body(), &result); err != nil {
		return nil, fmt.Errorf("create edge connect: parse response: %w", err)
	}

	return &result, nil
}

// Update updates an existing EdgeConnect.
func (h *Handler) Update(ctx context.Context, edgeConnectID string, req EdgeConnect) error {
	resp, err := h.client.HTTP().R().SetContext(ctx).
		SetBody(req).
		Put(fmt.Sprintf("/platform/app-engine/edge-connect/v1/edge-connects/%s", httpclient.PathSegment(edgeConnectID)))
	if err != nil {
		return fmt.Errorf("update edge connect: %w", err)
	}
	if err := httpclient.CheckResponse(resp); err != nil {
		return fmt.Errorf("update edge connect %q: %w", edgeConnectID, err)
	}

	return nil
}

// Delete deletes an EdgeConnect.
func (h *Handler) Delete(ctx context.Context, edgeConnectID string) error {
	resp, err := h.client.HTTP().R().SetContext(ctx).
		Delete(fmt.Sprintf("/platform/app-engine/edge-connect/v1/edge-connects/%s", httpclient.PathSegment(edgeConnectID)))
	if err != nil {
		return fmt.Errorf("delete edge connect: %w", err)
	}
	if err := httpclient.CheckResponse(resp); err != nil {
		return fmt.Errorf("delete edge connect %q: %w", edgeConnectID, err)
	}

	return nil
}
