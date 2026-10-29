package segment

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/Dynatrace/dtctl/sdk/httpclient"
)

// ErrNotFound is returned when a segment is not found (HTTP 404).
var ErrNotFound = errors.New("segment not found")

const basePath = "/platform/storage/filter-segments/v1/filter-segments"

// Handler handles Grail filter segment resources.
type Handler struct {
	client *httpclient.Client
}

// NewHandler creates a new segment handler.
func NewHandler(c *httpclient.Client) *Handler {
	return &Handler{client: c}
}

// FilterSegment is the read model for a Grail filter segment.
type FilterSegment struct {
	UID               string     `json:"uid"`
	Name              string     `json:"name"`
	Description       string     `json:"description,omitempty"`
	IsPublic          bool       `json:"isPublic"`
	Owner             string     `json:"owner,omitempty"`
	Version           int        `json:"version,omitempty"`
	IsReadyMade       bool       `json:"isReadyMade,omitempty"`
	Includes          []Include  `json:"includes,omitempty"`
	Variables         *Variables `json:"variables,omitempty"`
	AllowedOperations []string   `json:"allowedOperations,omitempty"`
}

// Include represents a single include rule within a segment.
type Include struct {
	DataObject string `json:"dataObject"`
	Filter     string `json:"filter"`
}

// Variables holds the variable configuration for a segment.
type Variables struct {
	Type  string `json:"type"`
	Value string `json:"value"`
}

// FilterSegmentList represents a list of filter segments.
type FilterSegmentList struct {
	FilterSegments []FilterSegment `json:"filterSegments"`
	TotalCount     int             `json:"totalCount,omitempty"`
}

// List lists all filter segments.
// Use addFields to request optional fields (e.g., "VARIABLES", "INCLUDES").
func (h *Handler) List(ctx context.Context, addFields ...string) (*FilterSegmentList, error) {
	req := h.client.HTTP().R().SetContext(ctx)
	if len(addFields) > 0 {
		req.SetQueryParamsFromValues(map[string][]string{
			"add-fields": addFields,
		})
	}
	resp, err := req.Get(basePath)
	if err != nil {
		return nil, fmt.Errorf("list segments: %w", err)
	}
	if err := httpclient.CheckResponse(resp); err != nil {
		return nil, fmt.Errorf("list segments: %w", err)
	}

	var result FilterSegmentList
	if err := json.Unmarshal(resp.Body(), &result); err != nil {
		return nil, fmt.Errorf("list segments: parse response: %w", err)
	}

	return &result, nil
}

// Get gets a specific filter segment by UID.
// Use addFields to request optional fields (e.g., "INCLUDES", "VARIABLES").
func (h *Handler) Get(ctx context.Context, uid string, addFields ...string) (*FilterSegment, error) {
	req := h.client.HTTP().R().SetContext(ctx)
	if len(addFields) > 0 {
		req.SetQueryParamsFromValues(map[string][]string{
			"add-fields": addFields,
		})
	}
	resp, err := req.Get(fmt.Sprintf("%s/%s", basePath, httpclient.PathSegment(uid)))
	if err != nil {
		return nil, fmt.Errorf("get segment: %w", err)
	}
	if err := httpclient.CheckResponse(resp); err != nil {
		if errors.Is(err, httpclient.ErrNotFound) {
			return nil, fmt.Errorf("segment %q: %w", uid, ErrNotFound)
		}
		return nil, fmt.Errorf("get segment: %w", err)
	}

	var result FilterSegment
	if err := json.Unmarshal(resp.Body(), &result); err != nil {
		return nil, fmt.Errorf("get segment: parse response: %w", err)
	}

	return &result, nil
}

// Create creates a new filter segment.
// The data should be JSON with the segment definition in the format the API expects
// (filter fields as JSON AST strings).
func (h *Handler) Create(ctx context.Context, data []byte) (*FilterSegment, error) {
	resp, err := h.client.HTTP().R().SetContext(ctx).
		SetHeader("Content-Type", "application/json").
		SetBody(data).
		Post(basePath)
	if err != nil {
		return nil, fmt.Errorf("create segment: %w", err)
	}
	if err := httpclient.CheckResponse(resp); err != nil {
		return nil, fmt.Errorf("create segment: %w", err)
	}

	var result FilterSegment
	if err := json.Unmarshal(resp.Body(), &result); err != nil {
		return nil, fmt.Errorf("create segment: parse response: %w", err)
	}

	return &result, nil
}

// Update updates an existing filter segment.
// The version parameter is required for optimistic locking.
// The data should be JSON in the format the API expects.
func (h *Handler) Update(ctx context.Context, uid string, version int, data []byte) error {
	resp, err := h.client.HTTP().R().SetContext(ctx).
		SetHeader("Content-Type", "application/json").
		SetQueryParam("optimistic-locking-version", fmt.Sprintf("%d", version)).
		SetBody(data).
		Patch(fmt.Sprintf("%s/%s", basePath, httpclient.PathSegment(uid)))
	if err != nil {
		return fmt.Errorf("update segment: %w", err)
	}
	if err := httpclient.CheckResponse(resp); err != nil {
		if errors.Is(err, httpclient.ErrNotFound) {
			return fmt.Errorf("segment %q: %w", uid, ErrNotFound)
		}
		return fmt.Errorf("update segment: %w", err)
	}

	return nil
}

// Delete deletes a filter segment by UID.
func (h *Handler) Delete(ctx context.Context, uid string) error {
	resp, err := h.client.HTTP().R().SetContext(ctx).
		Delete(fmt.Sprintf("%s/%s", basePath, httpclient.PathSegment(uid)))
	if err != nil {
		return fmt.Errorf("delete segment: %w", err)
	}
	if err := httpclient.CheckResponse(resp); err != nil {
		if errors.Is(err, httpclient.ErrNotFound) {
			return fmt.Errorf("segment %q: %w", uid, ErrNotFound)
		}
		return fmt.Errorf("delete segment: %w", err)
	}

	return nil
}

// IsNotFound returns true if the error indicates a segment was not found (404).
func IsNotFound(err error) bool {
	return errors.Is(err, ErrNotFound)
}
