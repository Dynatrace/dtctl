package hub

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/Dynatrace/dtctl/sdk/httpclient"
)

// Handler handles Dynatrace Hub catalog resources.
type Handler struct {
	client *httpclient.Client
}

// NewHandler creates a new Hub handler.
func NewHandler(c *httpclient.Client) *Handler {
	return &Handler{client: c}
}

// HubExtension represents a Dynatrace Hub catalog extension.
type HubExtension struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Type        string `json:"type,omitempty"`
	Description string `json:"description,omitempty"`
}

// HubExtensionList represents a paginated list of Hub extensions.
type HubExtensionList struct {
	Items       []HubExtension `json:"items"`
	TotalCount  int            `json:"totalCount"`
	NextPageKey string         `json:"nextPageKey,omitempty"`
}

// HubExtensionRelease represents a release of a Hub extension.
type HubExtensionRelease struct {
	Version     string `json:"version" yaml:"version"`
	ReleaseDate string `json:"releaseDate,omitempty" yaml:"releaseDate,omitempty"`
	Notes       string `json:"notes,omitempty" yaml:"notes,omitempty"`
}

// HubExtensionReleaseList represents a list of Hub extension releases.
type HubExtensionReleaseList struct {
	Items       []HubExtensionRelease `json:"releases"`
	TotalCount  int                   `json:"totalCount"`
	NextPageKey string                `json:"nextPageKey,omitempty"`
}

// ListExtensions lists all Hub catalog extensions with automatic pagination.
// filter is a case-insensitive substring matched against id, name, and description.
func (h *Handler) ListExtensions(ctx context.Context, filter string, chunkSize int64) (*HubExtensionList, error) {
	var allItems []HubExtension
	var totalCount int
	nextPageKey := ""

	for {
		req := h.client.HTTP().R().SetContext(ctx)

		params := httpclient.PaginationParams{
			Style:         httpclient.PaginationDefault,
			PageKeyParam:  "page-key",
			PageSizeParam: "page-size",
			NextPageKey:   nextPageKey,
			PageSize:      chunkSize,
		}.QueryParams()

		req.SetQueryParamsFromValues(params)

		resp, err := req.Get("/platform/hub/v1/catalog/extensions")
		if err != nil {
			return nil, fmt.Errorf("list hub extensions: %w", err)
		}
		if err := httpclient.CheckResponse(resp); err != nil {
			return nil, fmt.Errorf("list hub extensions: %w", err)
		}

		var result HubExtensionList
		if err := json.Unmarshal(resp.Body(), &result); err != nil {
			return nil, fmt.Errorf("list hub extensions: parse response: %w", err)
		}

		allItems = append(allItems, result.Items...)
		totalCount = result.TotalCount

		if chunkSize == 0 || result.NextPageKey == "" {
			break
		}
		nextPageKey = result.NextPageKey
	}

	// Client-side filtering: the API does not support server-side filtering,
	// so we match case-insensitively against id, name, and description.
	if filter != "" {
		q := strings.ToLower(filter)
		filtered := allItems[:0]
		for _, ext := range allItems {
			if strings.Contains(strings.ToLower(ext.ID), q) ||
				strings.Contains(strings.ToLower(ext.Name), q) ||
				strings.Contains(strings.ToLower(ext.Description), q) {
				filtered = append(filtered, ext)
			}
		}
		allItems = filtered
		totalCount = len(filtered)
	}

	return &HubExtensionList{Items: allItems, TotalCount: totalCount}, nil
}

// GetExtension gets a specific Hub extension by ID.
func (h *Handler) GetExtension(ctx context.Context, id string) (*HubExtension, error) {
	resp, err := h.client.HTTP().R().SetContext(ctx).
		Get(fmt.Sprintf("/platform/hub/v1/catalog/extensions/%s", httpclient.PathSegment(id)))
	if err != nil {
		return nil, fmt.Errorf("get hub extension: %w", err)
	}
	if err := httpclient.CheckResponse(resp); err != nil {
		return nil, fmt.Errorf("get hub extension %q: %w", id, err)
	}

	var result HubExtension
	if err := json.Unmarshal(resp.Body(), &result); err != nil {
		return nil, fmt.Errorf("get hub extension: parse response: %w", err)
	}

	return &result, nil
}

// ListExtensionReleases lists all releases for a Hub extension with automatic pagination.
func (h *Handler) ListExtensionReleases(ctx context.Context, id string, chunkSize int64) (*HubExtensionReleaseList, error) {
	var allItems []HubExtensionRelease
	var totalCount int
	nextPageKey := ""

	for {
		req := h.client.HTTP().R().SetContext(ctx)

		params := httpclient.PaginationParams{
			Style:         httpclient.PaginationDefault,
			PageKeyParam:  "page-key",
			PageSizeParam: "page-size",
			NextPageKey:   nextPageKey,
			PageSize:      chunkSize,
		}.QueryParams()

		req.SetQueryParamsFromValues(params)

		resp, err := req.Get(fmt.Sprintf("/platform/hub/v1/catalog/extensions/%s/releases", httpclient.PathSegment(id)))
		if err != nil {
			return nil, fmt.Errorf("list hub extension releases: %w", err)
		}
		if err := httpclient.CheckResponse(resp); err != nil {
			return nil, fmt.Errorf("list hub extension %q releases: %w", id, err)
		}

		var result HubExtensionReleaseList
		if err := json.Unmarshal(resp.Body(), &result); err != nil {
			return nil, fmt.Errorf("list hub extension releases: parse response: %w", err)
		}

		allItems = append(allItems, result.Items...)
		totalCount = result.TotalCount

		if chunkSize == 0 || result.NextPageKey == "" {
			break
		}
		nextPageKey = result.NextPageKey
	}

	return &HubExtensionReleaseList{Items: allItems, TotalCount: totalCount}, nil
}
