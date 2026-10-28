package bucket

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/Dynatrace/dtctl/sdk/httpclient"
)

// Handler handles Grail bucket resources.
type Handler struct {
	client *httpclient.Client
}

// NewHandler creates a new bucket handler.
func NewHandler(c *httpclient.Client) *Handler {
	return &Handler{client: c}
}

// Bucket represents a Grail bucket definition.
type Bucket struct {
	BucketName                 string `json:"bucketName"`
	Table                      string `json:"table"`
	DisplayName                string `json:"displayName"`
	Status                     string `json:"status"`
	RetentionDays              int    `json:"retentionDays"`
	IncludedQueryLimitDays     int    `json:"includedQueryLimitDays,omitempty"`
	MetricInterval             string `json:"metricInterval,omitempty"`
	Version                    int    `json:"version"`
	Updatable                  bool   `json:"updatable"`
	Records                    *int64 `json:"records,omitempty"`
	EstimatedUncompressedBytes *int64 `json:"estimatedUncompressedBytes,omitempty"`
}

// BucketList represents a list of bucket definitions.
type BucketList struct {
	Buckets []Bucket `json:"buckets"`
}

// BucketCreate represents the request body for creating a bucket.
type BucketCreate struct {
	BucketName             string `json:"bucketName"`
	Table                  string `json:"table"`
	DisplayName            string `json:"displayName,omitempty"`
	RetentionDays          int    `json:"retentionDays"`
	IncludedQueryLimitDays int    `json:"includedQueryLimitDays,omitempty"`
}

// BucketUpdate represents the request body for updating a bucket.
type BucketUpdate struct {
	DisplayName            string `json:"displayName,omitempty"`
	RetentionDays          int    `json:"retentionDays,omitempty"`
	IncludedQueryLimitDays int    `json:"includedQueryLimitDays,omitempty"`
}

// List lists all bucket definitions.
func (h *Handler) List(ctx context.Context) (*BucketList, error) {
	resp, err := h.client.HTTP().R().SetContext(ctx).
		SetQueryParam("add-fields", "records").
		Get("/platform/storage/management/v1/bucket-definitions")
	if err != nil {
		return nil, fmt.Errorf("list buckets: %w", err)
	}
	if err := httpclient.CheckResponse(resp); err != nil {
		return nil, fmt.Errorf("list buckets: %w", err)
	}
	var result BucketList
	if err := json.Unmarshal(resp.Body(), &result); err != nil {
		return nil, fmt.Errorf("list buckets: parse response: %w", err)
	}
	return &result, nil
}

// Get gets a specific bucket by name.
func (h *Handler) Get(ctx context.Context, bucketName string) (*Bucket, error) {
	resp, err := h.client.HTTP().R().SetContext(ctx).
		SetQueryParam("add-fields", "records,estimatedUncompressedBytes").
		Get(fmt.Sprintf("/platform/storage/management/v1/bucket-definitions/%s", httpclient.PathSegment(bucketName)))
	if err != nil {
		return nil, fmt.Errorf("get bucket: %w", err)
	}
	if err := httpclient.CheckResponse(resp); err != nil {
		return nil, fmt.Errorf("get bucket %q: %w", bucketName, err)
	}
	var result Bucket
	if err := json.Unmarshal(resp.Body(), &result); err != nil {
		return nil, fmt.Errorf("get bucket: parse response: %w", err)
	}
	return &result, nil
}

// Create creates a new bucket.
func (h *Handler) Create(ctx context.Context, req BucketCreate) (*Bucket, error) {
	resp, err := h.client.HTTP().R().SetContext(ctx).
		SetBody(req).
		Post("/platform/storage/management/v1/bucket-definitions")
	if err != nil {
		return nil, fmt.Errorf("create bucket: %w", err)
	}
	if err := httpclient.CheckResponse(resp); err != nil {
		return nil, fmt.Errorf("create bucket: %w", err)
	}
	var result Bucket
	if err := json.Unmarshal(resp.Body(), &result); err != nil {
		return nil, fmt.Errorf("create bucket: parse response: %w", err)
	}
	return &result, nil
}

// Update updates an existing bucket.
// The version parameter is required for optimistic locking.
func (h *Handler) Update(ctx context.Context, bucketName string, version int, req BucketUpdate) error {
	resp, err := h.client.HTTP().R().SetContext(ctx).
		SetBody(req).
		SetQueryParam("optimistic-locking-version", fmt.Sprintf("%d", version)).
		Patch(fmt.Sprintf("/platform/storage/management/v1/bucket-definitions/%s", httpclient.PathSegment(bucketName)))
	if err != nil {
		return fmt.Errorf("update bucket: %w", err)
	}
	if err := httpclient.CheckResponse(resp); err != nil {
		return fmt.Errorf("update bucket %q: %w", bucketName, err)
	}
	return nil
}

// Delete deletes a bucket by name.
func (h *Handler) Delete(ctx context.Context, bucketName string) error {
	resp, err := h.client.HTTP().R().SetContext(ctx).
		Delete(fmt.Sprintf("/platform/storage/management/v1/bucket-definitions/%s", httpclient.PathSegment(bucketName)))
	if err != nil {
		return fmt.Errorf("delete bucket: %w", err)
	}
	if err := httpclient.CheckResponse(resp); err != nil {
		return fmt.Errorf("delete bucket %q: %w", bucketName, err)
	}
	return nil
}

// Truncate empties a bucket (removes all data).
func (h *Handler) Truncate(ctx context.Context, bucketName string) error {
	resp, err := h.client.HTTP().R().SetContext(ctx).
		Post(fmt.Sprintf("/platform/storage/management/v1/bucket-definitions/%s:truncate", httpclient.PathSegment(bucketName)))
	if err != nil {
		return fmt.Errorf("truncate bucket: %w", err)
	}
	if err := httpclient.CheckResponse(resp); err != nil {
		return fmt.Errorf("truncate bucket %q: %w", bucketName, err)
	}
	return nil
}
