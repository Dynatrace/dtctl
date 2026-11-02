package hub

import (
	"context"

	"github.com/Dynatrace/dtctl/pkg/client"
	sdkhub "github.com/Dynatrace/dtctl/sdk/api/hub"
	"github.com/Dynatrace/dtctl/sdk/httpclient"
)

// HubExtension represents a Dynatrace Hub catalog extension (CLI version with table tags).
type HubExtension struct {
	ID          string `json:"id" table:"ID"`
	Name        string `json:"name" table:"NAME"`
	Type        string `json:"type,omitempty" table:"-"`
	Description string `json:"description,omitempty" table:"DESCRIPTION,wide"`
}

// HubExtensionList represents a paginated list of Hub extensions.
type HubExtensionList struct {
	Items       []HubExtension `json:"items"`
	TotalCount  int            `json:"totalCount"`
	NextPageKey string         `json:"nextPageKey,omitempty"`
}

// HubExtensionRelease represents a release of a Hub extension (CLI version with table tags).
type HubExtensionRelease struct {
	Version     string `json:"version" yaml:"version" table:"VERSION"`
	ReleaseDate string `json:"releaseDate,omitempty" yaml:"releaseDate,omitempty" table:"RELEASE_DATE,wide"`
	Notes       string `json:"notes,omitempty" yaml:"notes,omitempty" table:"-"`
}

// HubExtensionReleaseList represents a list of Hub extension releases.
type HubExtensionReleaseList struct {
	Items       []HubExtensionRelease `json:"items"`
	TotalCount  int                   `json:"totalCount"`
	NextPageKey string                `json:"nextPageKey,omitempty"`
}

// fromSDKExtension converts an SDK HubExtension to a CLI HubExtension.
func fromSDKExtension(s *sdkhub.HubExtension) HubExtension {
	return HubExtension{
		ID:          s.ID,
		Name:        s.Name,
		Type:        s.Type,
		Description: s.Description,
	}
}

// fromSDKRelease converts an SDK HubExtensionRelease to a CLI HubExtensionRelease.
func fromSDKRelease(s *sdkhub.HubExtensionRelease) HubExtensionRelease {
	return HubExtensionRelease{
		Version:     s.Version,
		ReleaseDate: s.ReleaseDate,
		Notes:       s.Notes,
	}
}

// Handler handles Dynatrace Hub catalog resources.
// It delegates to the SDK handler.
type Handler struct {
	sdk *sdkhub.Handler
}

// NewHandler creates a new Hub handler.
func NewHandler(c *client.Client) *Handler {
	return &Handler{
		sdk: sdkhub.NewHandler(httpclient.Wrap(c.HTTP())),
	}
}

// ListExtensions lists all Hub catalog extensions with automatic pagination.
// filter is a case-insensitive substring matched against id, name, and description.
func (h *Handler) ListExtensions(filter string, chunkSize int64) (*HubExtensionList, error) {
	sdkResult, err := h.sdk.ListExtensions(context.Background(), filter, chunkSize)
	if err != nil {
		return nil, err
	}
	items := make([]HubExtension, len(sdkResult.Items))
	for i := range sdkResult.Items {
		items[i] = fromSDKExtension(&sdkResult.Items[i])
	}
	return &HubExtensionList{Items: items, TotalCount: sdkResult.TotalCount}, nil
}

// GetExtension gets a specific Hub extension by ID.
func (h *Handler) GetExtension(id string) (*HubExtension, error) {
	sdkResult, err := h.sdk.GetExtension(context.Background(), id)
	if err != nil {
		return nil, err
	}
	ext := fromSDKExtension(sdkResult)
	return &ext, nil
}

// ListExtensionReleases lists all releases for a Hub extension.
func (h *Handler) ListExtensionReleases(id string, chunkSize int64) (*HubExtensionReleaseList, error) {
	sdkResult, err := h.sdk.ListExtensionReleases(context.Background(), id, chunkSize)
	if err != nil {
		return nil, err
	}
	items := make([]HubExtensionRelease, len(sdkResult.Items))
	for i := range sdkResult.Items {
		items[i] = fromSDKRelease(&sdkResult.Items[i])
	}
	return &HubExtensionReleaseList{Items: items, TotalCount: sdkResult.TotalCount}, nil
}
