package bucket

import (
	"context"
	"encoding/json"

	"github.com/Dynatrace/dtctl/pkg/client"
	sdkbucket "github.com/Dynatrace/dtctl/sdk/api/bucket"
	"github.com/Dynatrace/dtctl/sdk/httpclient"
)

// Re-export SDK types that have no table tags.
type (
	BucketCreate = sdkbucket.BucketCreate
	BucketUpdate = sdkbucket.BucketUpdate
)

// Bucket represents a Grail bucket definition (CLI version with table tags).
type Bucket struct {
	BucketName                 string `json:"bucketName" table:"NAME"`
	Table                      string `json:"table" table:"TABLE"`
	DisplayName                string `json:"displayName" table:"DISPLAY_NAME"`
	Status                     string `json:"status" table:"STATUS"`
	RetentionDays              int    `json:"retentionDays" table:"RETENTION_DAYS"`
	IncludedQueryLimitDays     int    `json:"includedQueryLimitDays,omitempty" table:"-"`
	MetricInterval             string `json:"metricInterval,omitempty" table:"INTERVAL,wide"`
	Version                    int    `json:"version" table:"-"`
	Updatable                  bool   `json:"updatable" table:"UPDATABLE,wide"`
	Records                    *int64 `json:"records,omitempty" table:"RECORDS,wide"`
	EstimatedUncompressedBytes *int64 `json:"estimatedUncompressedBytes,omitempty" table:"-"`
}

// BucketList represents a list of bucket definitions.
type BucketList struct {
	Buckets []Bucket `json:"buckets"`
}

// fromSDKBucket converts an SDK Bucket to a CLI Bucket.
func fromSDKBucket(s *sdkbucket.Bucket) Bucket {
	return Bucket{
		BucketName:                 s.BucketName,
		Table:                      s.Table,
		DisplayName:                s.DisplayName,
		Status:                     s.Status,
		RetentionDays:              s.RetentionDays,
		IncludedQueryLimitDays:     s.IncludedQueryLimitDays,
		MetricInterval:             s.MetricInterval,
		Version:                    s.Version,
		Updatable:                  s.Updatable,
		Records:                    s.Records,
		EstimatedUncompressedBytes: s.EstimatedUncompressedBytes,
	}
}

// Handler handles Grail bucket resources.
// It delegates to the SDK handler and adds CLI-specific convenience methods.
type Handler struct {
	sdk *sdkbucket.Handler
}

// NewHandler creates a new bucket handler.
func NewHandler(c *client.Client) *Handler {
	return &Handler{
		sdk: sdkbucket.NewHandler(httpclient.Wrap(c.HTTP())),
	}
}

// List lists all bucket definitions.
func (h *Handler) List() (*BucketList, error) {
	sdkResult, err := h.sdk.List(context.Background())
	if err != nil {
		return nil, err
	}
	buckets := make([]Bucket, len(sdkResult.Buckets))
	for i := range sdkResult.Buckets {
		buckets[i] = fromSDKBucket(&sdkResult.Buckets[i])
	}
	return &BucketList{Buckets: buckets}, nil
}

// Get gets a specific bucket by name.
func (h *Handler) Get(bucketName string) (*Bucket, error) {
	sdkResult, err := h.sdk.Get(context.Background(), bucketName)
	if err != nil {
		return nil, err
	}
	b := fromSDKBucket(sdkResult)
	return &b, nil
}

// Create creates a new bucket.
func (h *Handler) Create(req BucketCreate) (*Bucket, error) {
	sdkResult, err := h.sdk.Create(context.Background(), req)
	if err != nil {
		return nil, err
	}
	b := fromSDKBucket(sdkResult)
	return &b, nil
}

// Update updates an existing bucket.
func (h *Handler) Update(bucketName string, version int, req BucketUpdate) error {
	return h.sdk.Update(context.Background(), bucketName, version, req)
}

// Delete deletes a bucket.
func (h *Handler) Delete(bucketName string) error {
	return h.sdk.Delete(context.Background(), bucketName)
}

// Truncate empties a bucket (removes all data).
func (h *Handler) Truncate(bucketName string) error {
	return h.sdk.Truncate(context.Background(), bucketName)
}

// GetRaw gets a bucket as raw JSON bytes (for editing).
func (h *Handler) GetRaw(bucketName string) ([]byte, error) {
	bucket, err := h.sdk.Get(context.Background(), bucketName)
	if err != nil {
		return nil, err
	}
	return json.MarshalIndent(bucket, "", "  ")
}
