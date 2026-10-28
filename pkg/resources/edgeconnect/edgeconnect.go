package edgeconnect

import (
	"context"
	"encoding/json"

	"github.com/Dynatrace/dtctl/pkg/client"
	sdkedgeconnect "github.com/Dynatrace/dtctl/sdk/api/edgeconnect"
	"github.com/Dynatrace/dtctl/sdk/httpclient"
)

// EdgeConnect is the CLI read model for an EdgeConnect configuration.
type EdgeConnect struct {
	ID                         string            `json:"id,omitempty" table:"ID"`
	Name                       string            `json:"name" table:"NAME"`
	HostPatterns               []string          `json:"hostPatterns,omitempty" table:"-"`
	OAuthClientID              string            `json:"oauthClientId,omitempty" table:"-"`
	OAuthClientSecret          string            `json:"oauthClientSecret,omitempty" table:"-"`
	OAuthClientResource        string            `json:"oauthClientResource,omitempty" table:"-"`
	ModificationInfo           *ModificationInfo `json:"modificationInfo,omitempty" table:"-"`
	ManagedByDynatraceOperator bool              `json:"managedByDynatraceOperator,omitempty" table:"MANAGED,wide"`
	Metadata                   *Metadata         `json:"metadata,omitempty" table:"-"`
}

// fromSDKEdgeConnect converts an SDK EdgeConnect to the CLI EdgeConnect.
func fromSDKEdgeConnect(e *sdkedgeconnect.EdgeConnect) *EdgeConnect {
	return &EdgeConnect{
		ID:                         e.ID,
		Name:                       e.Name,
		HostPatterns:               e.HostPatterns,
		OAuthClientID:              e.OAuthClientID,
		OAuthClientSecret:          e.OAuthClientSecret,
		OAuthClientResource:        e.OAuthClientResource,
		ModificationInfo:           e.ModificationInfo,
		ManagedByDynatraceOperator: e.ManagedByDynatraceOperator,
		Metadata:                   e.Metadata,
	}
}

// EdgeConnectList represents a list of EdgeConnects.
type EdgeConnectList struct {
	EdgeConnects []EdgeConnect `json:"edgeConnects"`
	TotalCount   int           `json:"totalCount"`
	PageSize     int           `json:"pageSize"`
}

// fromSDKEdgeConnectList converts an SDK EdgeConnectList to the CLI EdgeConnectList.
func fromSDKEdgeConnectList(l *sdkedgeconnect.EdgeConnectList) *EdgeConnectList {
	ecs := make([]EdgeConnect, len(l.EdgeConnects))
	for i := range l.EdgeConnects {
		ecs[i] = *fromSDKEdgeConnect(&l.EdgeConnects[i])
	}
	return &EdgeConnectList{
		EdgeConnects: ecs,
		TotalCount:   l.TotalCount,
		PageSize:     l.PageSize,
	}
}

// Re-export SDK types that don't have table tags.
type (
	ModificationInfo = sdkedgeconnect.ModificationInfo
	Metadata         = sdkedgeconnect.Metadata
)

// EdgeConnectCreate represents the request body for creating an EdgeConnect.
// CLI-specific type: the SDK Create method accepts an EdgeConnect directly.
type EdgeConnectCreate struct {
	Name          string   `json:"name"`
	HostPatterns  []string `json:"hostPatterns,omitempty"`
	OAuthClientID string   `json:"oauthClientId,omitempty"`
}

// Handler handles EdgeConnect resources.
// It delegates to the SDK handler and adds CLI-specific convenience methods.
type Handler struct {
	sdk *sdkedgeconnect.Handler
}

// NewHandler creates a new EdgeConnect handler.
func NewHandler(c *client.Client) *Handler {
	return &Handler{
		sdk: sdkedgeconnect.NewHandler(httpclient.Wrap(c.HTTP())),
	}
}

// List lists all EdgeConnect configurations.
func (h *Handler) List() (*EdgeConnectList, error) {
	l, err := h.sdk.List(context.Background())
	if err != nil {
		return nil, err
	}
	return fromSDKEdgeConnectList(l), nil
}

// Get gets a specific EdgeConnect by ID.
func (h *Handler) Get(edgeConnectID string) (*EdgeConnect, error) {
	e, err := h.sdk.Get(context.Background(), edgeConnectID)
	if err != nil {
		return nil, err
	}
	return fromSDKEdgeConnect(e), nil
}

// Create creates a new EdgeConnect.
func (h *Handler) Create(req EdgeConnectCreate) (*EdgeConnect, error) {
	e, err := h.sdk.Create(context.Background(), sdkedgeconnect.EdgeConnect{
		Name:          req.Name,
		HostPatterns:  req.HostPatterns,
		OAuthClientID: req.OAuthClientID,
	})
	if err != nil {
		return nil, err
	}
	return fromSDKEdgeConnect(e), nil
}

// Update updates an existing EdgeConnect.
func (h *Handler) Update(edgeConnectID string, req EdgeConnect) error {
	return h.sdk.Update(context.Background(), edgeConnectID, sdkedgeconnect.EdgeConnect{
		ID:                         req.ID,
		Name:                       req.Name,
		HostPatterns:               req.HostPatterns,
		OAuthClientID:              req.OAuthClientID,
		OAuthClientSecret:          req.OAuthClientSecret,
		OAuthClientResource:        req.OAuthClientResource,
		ModificationInfo:           req.ModificationInfo,
		ManagedByDynatraceOperator: req.ManagedByDynatraceOperator,
		Metadata:                   req.Metadata,
	})
}

// Delete deletes an EdgeConnect.
func (h *Handler) Delete(edgeConnectID string) error {
	return h.sdk.Delete(context.Background(), edgeConnectID)
}

// GetRaw gets an EdgeConnect as raw JSON bytes (for editing).
func (h *Handler) GetRaw(edgeConnectID string) ([]byte, error) {
	ec, err := h.Get(edgeConnectID)
	if err != nil {
		return nil, err
	}
	return json.MarshalIndent(ec, "", "  ")
}
