package apply

import (
	"encoding/json"
	"fmt"

	"github.com/dynatrace-oss/dtctl/pkg/resources/bucket"
)

// applyBucket applies a bucket resource
func (a *Applier) applyBucket(data []byte) (ApplyResult, error) {
	var b bucket.BucketCreate
	if err := json.Unmarshal(data, &b); err != nil {
		return nil, fmt.Errorf("failed to parse bucket JSON: %w", err)
	}

	handler := bucket.NewHandler(a.client)

	// Check if bucket exists
	existing, err := handler.Get(b.BucketName)
	if err != nil {
		if lookupErr := lookupError("bucket", b.BucketName, err); lookupErr != nil {
			return nil, lookupErr
		}

		// Bucket doesn't exist, create it
		result, err := handler.Create(b)
		if err != nil {
			return nil, fmt.Errorf("failed to create bucket: %w", err)
		}
		var warnings []string
		a.stderrWarn(&warnings, "Bucket creation can take up to 1 minute to complete")
		return &BucketApplyResult{
			ApplyResultBase: ApplyResultBase{
				Action:       ActionCreated,
				ResourceType: "bucket",
				ID:           result.BucketName,
				Name:         result.BucketName,
				Warnings:     warnings,
			},
			Status: result.Status,
		}, nil
	}

	// Update existing bucket
	update := bucket.BucketUpdate{
		DisplayName:   b.DisplayName,
		RetentionDays: b.RetentionDays,
	}

	if err := handler.Update(b.BucketName, existing.Version, update); err != nil {
		return nil, fmt.Errorf("failed to update bucket: %w", err)
	}

	return &BucketApplyResult{
		ApplyResultBase: ApplyResultBase{
			Action:       ActionUpdated,
			ResourceType: "bucket",
			ID:           b.BucketName,
			Name:         b.BucketName,
		},
		Status: existing.Status,
	}, nil
}
