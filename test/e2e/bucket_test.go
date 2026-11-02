//go:build integration
// +build integration

package e2e

import (
	"fmt"
	"testing"
	"time"

	"github.com/Dynatrace/dtctl/pkg/resources/bucket"
	"github.com/Dynatrace/dtctl/test/integration"
)

// waitForBucketActive polls the bucket status until it becomes "active" or timeout is reached
func waitForBucketActive(t *testing.T, handler *bucket.Handler, bucketName string, timeout time.Duration) (*bucket.Bucket, error) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	pollInterval := 2 * time.Second
	notFoundCount := 0
	maxNotFoundRetries := 3

	for time.Now().Before(deadline) {
		b, err := handler.Get(bucketName)
		if err != nil {
			// If bucket not found, it might have been auto-deleted or not yet visible
			// Allow a few retries before failing
			if notFoundCount < maxNotFoundRetries {
				notFoundCount++
				t.Logf("Bucket %s not found (attempt %d/%d), retrying...", bucketName, notFoundCount, maxNotFoundRetries)
				time.Sleep(pollInterval)
				continue
			}
			return nil, fmt.Errorf("failed to get bucket status: %w", err)
		}

		notFoundCount = 0 // Reset counter if we got a successful response
		t.Logf("Bucket %s status: %s (version: %d)", bucketName, b.Status, b.Version)

		if b.Status == "active" {
			return b, nil
		}

		time.Sleep(pollInterval)
	}

	return nil, fmt.Errorf("bucket %s did not become active within %v", bucketName, timeout)
}

func TestBucketLifecycle(t *testing.T) {
	t.Skip("Skipping: Bucket API may auto-delete buckets that stay in 'creating' state - environment-specific limitation")

	env := integration.SetupIntegration(t)
	defer env.Cleanup.Cleanup(t)

	handler := bucket.NewHandler(env.Client)

	tests := []struct {
		name    string
		wantErr bool
	}{
		{
			name:    "complete bucket lifecycle",
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Step 1: Create bucket
			t.Log("Step 1: Creating bucket...")
			createReq := integration.BucketCreateRequest(env.TestPrefix)
			bucketName := integration.BucketName(env.TestPrefix)

			created, err := handler.Create(bucket.BucketCreate{
				BucketName:    createReq["bucketName"].(string),
				Table:         createReq["table"].(string),
				DisplayName:   createReq["displayName"].(string),
				RetentionDays: createReq["retentionDays"].(int),
			})
			if err != nil {
				t.Fatalf("Failed to create bucket: %v", err)
			}
			if created.BucketName == "" {
				t.Fatal("Created bucket has no name")
			}
			t.Logf("✓ Created bucket: %s (Display: %s, Retention: %d days, Version: %d)",
				created.BucketName, created.DisplayName, created.RetentionDays, created.Version)

			// Track for cleanup
			env.Cleanup.Track("bucket", created.BucketName, created.DisplayName)

			// Wait for bucket to become active
			t.Log("Waiting for bucket to become active...")
			activeBucket, err := waitForBucketActive(t, handler, bucketName, 60*time.Second)
			if err != nil {
				t.Fatalf("Bucket did not become active: %v", err)
			}
			t.Logf("✓ Bucket is now active (version: %d)", activeBucket.Version)

			// Step 2: Get bucket
			t.Log("Step 2: Getting bucket...")
			retrieved, err := handler.Get(bucketName)
			if err != nil {
				t.Fatalf("Failed to get bucket: %v", err)
			}
			if retrieved.BucketName != created.BucketName {
				t.Errorf("Retrieved bucket name mismatch: got %s, want %s", retrieved.BucketName, created.BucketName)
			}
			if retrieved.RetentionDays != created.RetentionDays {
				t.Errorf("Retrieved bucket retention mismatch: got %d, want %d", retrieved.RetentionDays, created.RetentionDays)
			}
			t.Logf("✓ Retrieved bucket: %s (Status: %s, Version: %d)", retrieved.BucketName, retrieved.Status, retrieved.Version)

			// Step 3: List buckets (verify our bucket appears)
			t.Log("Step 3: Listing buckets...")
			list, err := handler.List()
			if err != nil {
				t.Fatalf("Failed to list buckets: %v", err)
			}
			found := false
			for _, b := range list.Buckets {
				if b.BucketName == created.BucketName {
					found = true
					break
				}
			}
			if !found {
				t.Error("Created bucket not found in list")
			} else {
				t.Logf("✓ Found bucket in list (total: %d buckets)", len(list.Buckets))
			}

			// Step 4: Update bucket
			t.Log("Step 4: Updating bucket...")
			updateReq := integration.BucketUpdateRequest(env.TestPrefix)

			// Use the active bucket version for the update
			err = handler.Update(bucketName, activeBucket.Version, bucket.BucketUpdate{
				DisplayName:   updateReq["displayName"].(string),
				RetentionDays: updateReq["retentionDays"].(int),
			})
			if err != nil {
				t.Fatalf("Failed to update bucket: %v", err)
			}
			t.Logf("✓ Updated bucket: Retention %d → %d days", created.RetentionDays, updateReq["retentionDays"].(int))

			// Step 5: Verify update
			t.Log("Step 5: Verifying update...")
			updated, err := handler.Get(bucketName)
			if err != nil {
				t.Fatalf("Failed to get updated bucket: %v", err)
			}
			if updated.RetentionDays != updateReq["retentionDays"].(int) {
				t.Errorf("Bucket retention not updated: got %d, want %d", updated.RetentionDays, updateReq["retentionDays"].(int))
			}
			if updated.DisplayName != updateReq["displayName"].(string) {
				t.Errorf("Bucket display name not updated: got %s, want %s", updated.DisplayName, updateReq["displayName"].(string))
			}
			if updated.Version <= created.Version {
				t.Errorf("Bucket version should have incremented: got %d, previous %d", updated.Version, created.Version)
			}
			t.Logf("✓ Verified update (Version: %d → %d)", created.Version, updated.Version)

			// Step 6: Delete bucket
			t.Log("Step 6: Deleting bucket...")
			err = handler.Delete(bucketName)
			if err != nil {
				t.Fatalf("Failed to delete bucket: %v", err)
			}
			t.Logf("✓ Deleted bucket: %s", bucketName)

			// Step 7: Verify deletion (should get error/404)
			t.Log("Step 7: Verifying deletion...")
			_, err = handler.Get(bucketName)
			if err == nil {
				t.Error("Expected error when getting deleted bucket, got nil")
			} else {
				t.Logf("✓ Verified deletion (got expected error: %v)", err)
			}
		})
	}
}

func TestBucketCreateInvalid(t *testing.T) {
	env := integration.SetupIntegration(t)
	defer env.Cleanup.Cleanup(t)

	handler := bucket.NewHandler(env.Client)

	tests := []struct {
		name    string
		req     bucket.BucketCreate
		wantErr bool
	}{
		{
			name: "empty bucket name",
			req: bucket.BucketCreate{
				BucketName:    "",
				Table:         "logs",
				RetentionDays: 35,
			},
			wantErr: true,
		},
		{
			name: "invalid table",
			req: bucket.BucketCreate{
				BucketName:    integration.BucketName(env.TestPrefix),
				Table:         "invalid_table",
				RetentionDays: 35,
			},
			wantErr: true,
		},
		{
			name: "invalid retention days",
			req: bucket.BucketCreate{
				BucketName:    integration.BucketName(env.TestPrefix),
				Table:         "logs",
				RetentionDays: 0,
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			created, err := handler.Create(tt.req)
			if (err != nil) != tt.wantErr {
				t.Errorf("Create() error = %v, wantErr %v", err, tt.wantErr)
			}
			if err != nil {
				t.Logf("✓ Got expected error: %v", err)
			}
			// If creation succeeded unexpectedly, clean up
			if err == nil && created != nil {
				env.Cleanup.Track("bucket", created.BucketName, created.DisplayName)
			}
		})
	}
}

func TestBucketOptimisticLocking(t *testing.T) {
	t.Skip("Skipping: Bucket API may auto-delete buckets that stay in 'creating' state - environment-specific limitation")

	env := integration.SetupIntegration(t)
	defer env.Cleanup.Cleanup(t)

	handler := bucket.NewHandler(env.Client)

	// Create a bucket first
	createReq := integration.BucketCreateRequest(env.TestPrefix)
	bucketName := integration.BucketName(env.TestPrefix)

	created, err := handler.Create(bucket.BucketCreate{
		BucketName:    createReq["bucketName"].(string),
		Table:         createReq["table"].(string),
		DisplayName:   createReq["displayName"].(string),
		RetentionDays: createReq["retentionDays"].(int),
	})
	if err != nil {
		t.Fatalf("Failed to create bucket: %v", err)
	}
	env.Cleanup.Track("bucket", created.BucketName, created.DisplayName)

	t.Logf("Created bucket: %s (Version: %d)", created.BucketName, created.Version)

	// Wait for bucket to become active
	t.Log("Waiting for bucket to become active...")
	activeBucket, err := waitForBucketActive(t, handler, bucketName, 60*time.Second)
	if err != nil {
		t.Fatalf("Bucket did not become active: %v", err)
	}
	t.Logf("✓ Bucket is now active (version: %d)", activeBucket.Version)

	// Test updating with stale version (should fail)
	t.Run("update with stale version", func(t *testing.T) {
		// First update using active version
		err := handler.Update(bucketName, activeBucket.Version, bucket.BucketUpdate{
			RetentionDays: 45,
		})
		if err != nil {
			t.Fatalf("First update failed: %v", err)
		}
		t.Logf("First update successful")

		// Get updated version
		updated, err := handler.Get(bucketName)
		if err != nil {
			t.Fatalf("Failed to get updated bucket: %v", err)
		}
		t.Logf("Updated version: %d → %d", activeBucket.Version, updated.Version)

		// Try to update with old version (should fail with 409)
		err = handler.Update(bucketName, activeBucket.Version, bucket.BucketUpdate{
			RetentionDays: 55,
		})
		if err == nil {
			t.Error("Expected error when updating with stale version, got nil")
		} else {
			t.Logf("✓ Got expected optimistic locking error: %v", err)
		}
	})
}

func TestBucketDuplicateCreate(t *testing.T) {
	t.Skip("Skipping: Bucket API may auto-delete buckets that stay in 'creating' state - environment-specific limitation")

	env := integration.SetupIntegration(t)
	defer env.Cleanup.Cleanup(t)

	handler := bucket.NewHandler(env.Client)

	// Create a bucket
	createReq := integration.BucketCreateRequest(env.TestPrefix)

	created, err := handler.Create(bucket.BucketCreate{
		BucketName:    createReq["bucketName"].(string),
		Table:         createReq["table"].(string),
		DisplayName:   createReq["displayName"].(string),
		RetentionDays: createReq["retentionDays"].(int),
	})
	if err != nil {
		t.Fatalf("Failed to create bucket: %v", err)
	}
	env.Cleanup.Track("bucket", created.BucketName, created.DisplayName)

	t.Logf("Created bucket: %s", created.BucketName)

	// Try to create another bucket with the same name (should fail with 409)
	_, err = handler.Create(bucket.BucketCreate{
		BucketName:    createReq["bucketName"].(string),
		Table:         createReq["table"].(string),
		DisplayName:   "Duplicate Bucket",
		RetentionDays: createReq["retentionDays"].(int),
	})
	if err == nil {
		t.Error("Expected error when creating duplicate bucket, got nil")
	} else {
		t.Logf("✓ Got expected duplicate error: %v", err)
	}
}
