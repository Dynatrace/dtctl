//go:build integration
// +build integration

package e2e

import (
	"encoding/json"
	"testing"

	"github.com/Dynatrace/dtctl/pkg/exec"
	"github.com/Dynatrace/dtctl/pkg/resources/segment"
	"github.com/Dynatrace/dtctl/test/integration"
)

func TestSegmentLifecycle(t *testing.T) {
	env := integration.SetupIntegration(t)
	defer env.Cleanup.Cleanup(t)

	handler := segment.NewHandler(env.Client)

	t.Run("complete segment lifecycle", func(t *testing.T) {
		// Step 1: Create segment
		t.Log("Step 1: Creating segment...")
		createData := integration.SegmentFixture(env.TestPrefix)

		created, err := handler.Create(createData)
		if err != nil {
			t.Fatalf("Failed to create segment: %v", err)
		}
		if created.UID == "" {
			t.Fatal("Created segment has no UID")
		}
		t.Logf("Created segment: %s (UID: %s)", created.Name, created.UID)

		// Track for cleanup
		env.Cleanup.Track("segment", created.UID, created.Name)

		// Step 2: Get segment
		t.Log("Step 2: Getting segment...")
		retrieved, err := handler.Get(created.UID)
		if err != nil {
			t.Fatalf("Failed to get segment: %v", err)
		}
		if retrieved.UID != created.UID {
			t.Errorf("Retrieved segment UID mismatch: got %s, want %s", retrieved.UID, created.UID)
		}
		if retrieved.Name != created.Name {
			t.Errorf("Retrieved segment name mismatch: got %s, want %s", retrieved.Name, created.Name)
		}
		if len(retrieved.Includes) == 0 {
			t.Error("Retrieved segment has no includes")
		}
		t.Logf("Retrieved segment: %s (includes: %d, public: %v)", retrieved.Name, len(retrieved.Includes), retrieved.IsPublic)

		// Step 3: List segments (verify our segment appears)
		t.Log("Step 3: Listing segments...")
		list, err := handler.List()
		if err != nil {
			t.Fatalf("Failed to list segments: %v", err)
		}
		found := false
		for _, s := range list.FilterSegments {
			if s.UID == created.UID {
				found = true
				break
			}
		}
		if !found {
			t.Error("Created segment not found in list")
		} else {
			t.Logf("Found segment in list (total: %d segments)", list.TotalCount)
		}

		// Step 4: Update segment
		t.Log("Step 4: Updating segment...")
		updateData := integration.SegmentFixtureModified(env.TestPrefix)
		err = handler.Update(created.UID, retrieved.Version, updateData)
		if err != nil {
			t.Fatalf("Failed to update segment: %v", err)
		}
		t.Logf("Updated segment: %s", created.UID)

		// Step 5: Verify update
		t.Log("Step 5: Verifying update...")
		updated, err := handler.Get(created.UID)
		if err != nil {
			t.Fatalf("Failed to get updated segment: %v", err)
		}
		expectedName := env.TestPrefix + "-segment-modified"
		if updated.Name != expectedName {
			t.Errorf("Updated segment name mismatch: got %s, want %s", updated.Name, expectedName)
		}
		if !updated.IsPublic {
			t.Error("Updated segment should be public after update")
		}
		if len(updated.Includes) < 2 {
			t.Errorf("Updated segment should have 2 includes, got %d", len(updated.Includes))
		}
		t.Logf("Verified update (name: %s, public: %v, includes: %d)", updated.Name, updated.IsPublic, len(updated.Includes))

		// Step 6: Get raw (for edit command flow)
		t.Log("Step 6: Getting raw segment...")
		raw, err := handler.GetRaw(created.UID)
		if err != nil {
			t.Fatalf("Failed to get raw segment: %v", err)
		}
		if len(raw) == 0 {
			t.Error("Raw segment is empty")
		}
		// Verify raw is valid JSON
		var rawCheck map[string]interface{}
		if err := json.Unmarshal(raw, &rawCheck); err != nil {
			t.Errorf("Raw segment is not valid JSON: %v", err)
		}
		t.Logf("Got raw segment (%d bytes)", len(raw))

		// Step 7: Delete segment
		t.Log("Step 7: Deleting segment...")
		err = handler.Delete(created.UID)
		if err != nil {
			t.Fatalf("Failed to delete segment: %v", err)
		}
		t.Logf("Deleted segment: %s", created.UID)

		// Untrack from cleanup since we manually deleted
		env.Cleanup.Untrack("segment", created.UID)

		// Step 8: Verify deletion (should get error/404)
		t.Log("Step 8: Verifying deletion...")
		_, err = handler.Get(created.UID)
		if err == nil {
			t.Error("Expected error when getting deleted segment, got nil")
		} else {
			t.Logf("Verified deletion (got expected error: %v)", err)
		}
	})
}

func TestSegmentCreateInvalid(t *testing.T) {
	env := integration.SetupIntegration(t)
	defer env.Cleanup.Cleanup(t)

	handler := segment.NewHandler(env.Client)

	tests := []struct {
		name    string
		data    []byte
		wantErr bool
	}{
		{
			name:    "invalid json",
			data:    []byte(`{"invalid": json`),
			wantErr: true,
		},
		{
			name:    "empty segment",
			data:    []byte(`{}`),
			wantErr: true,
		},
		{
			name:    "missing includes",
			data:    []byte(`{"name": "test-segment"}`),
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			created, err := handler.Create(tt.data)
			if (err != nil) != tt.wantErr {
				t.Errorf("Create() error = %v, wantErr %v", err, tt.wantErr)
			}
			if err != nil {
				t.Logf("Got expected error: %v", err)
			}
			// If creation succeeded unexpectedly, clean up
			if err == nil && created != nil {
				env.Cleanup.Track("segment", created.UID, created.Name)
			}
		})
	}
}

func TestSegmentGetNonExistent(t *testing.T) {
	env := integration.SetupIntegration(t)
	defer env.Cleanup.Cleanup(t)

	handler := segment.NewHandler(env.Client)

	_, err := handler.Get("non-existent-segment-uid-12345")
	if err == nil {
		t.Error("Expected error when getting non-existent segment, got nil")
	} else {
		t.Logf("Got expected error: %v", err)
	}
}

func TestSegmentDeleteNonExistent(t *testing.T) {
	env := integration.SetupIntegration(t)
	defer env.Cleanup.Cleanup(t)

	handler := segment.NewHandler(env.Client)

	err := handler.Delete("non-existent-segment-uid-12345")
	if err == nil {
		t.Error("Expected error when deleting non-existent segment, got nil")
	} else {
		t.Logf("Got expected error: %v", err)
	}
}

func TestSegmentQueryIntegration(t *testing.T) {
	env := integration.SetupIntegration(t)
	defer env.Cleanup.Cleanup(t)

	handler := segment.NewHandler(env.Client)
	dqlExecutor := exec.NewDQLExecutor(env.Client)

	// Step 1: Create a segment to use in queries
	t.Log("Step 1: Creating segment for query testing...")
	createData := integration.SegmentFixtureMultiInclude(env.TestPrefix)

	created, err := handler.Create(createData)
	if err != nil {
		t.Fatalf("Failed to create segment: %v", err)
	}
	if created.UID == "" {
		t.Fatal("Created segment has no UID")
	}
	t.Logf("Created segment: %s (UID: %s)", created.Name, created.UID)
	env.Cleanup.Track("segment", created.UID, created.Name)

	// Step 2: Execute a query with the segment applied
	t.Run("query with single segment", func(t *testing.T) {
		t.Log("Executing DQL query with segment filter...")

		opts := exec.DQLExecuteOptions{
			OutputFormat: "json",
			Segments: []exec.FilterSegmentRef{
				{ID: created.UID},
			},
		}

		// Use a simple query that the segment filter can apply to
		result, err := dqlExecutor.ExecuteQueryWithOptions("fetch logs | limit 5", opts)
		if err != nil {
			t.Fatalf("Failed to execute query with segment: %v", err)
		}

		// The query should succeed (even if no results match the segment filter)
		if result.State != "SUCCEEDED" {
			t.Errorf("Query state mismatch: got %s, want SUCCEEDED", result.State)
		}
		t.Logf("Query with segment succeeded (state: %s)", result.State)

		// Log record count
		records := result.Records
		if result.Result != nil && len(result.Result.Records) > 0 {
			records = result.Result.Records
		}
		t.Logf("Query returned %d records with segment filter applied", len(records))
	})

	// Step 3: Execute a query with multiple segments
	t.Run("query with multiple segments", func(t *testing.T) {
		// Create a second segment
		secondData := integration.SegmentFixture(env.TestPrefix + "-second")
		second, err := handler.Create(secondData)
		if err != nil {
			t.Fatalf("Failed to create second segment: %v", err)
		}
		env.Cleanup.Track("segment", second.UID, second.Name)
		t.Logf("Created second segment: %s (UID: %s)", second.Name, second.UID)

		opts := exec.DQLExecuteOptions{
			OutputFormat: "json",
			Segments: []exec.FilterSegmentRef{
				{ID: created.UID},
				{ID: second.UID},
			},
		}

		result, err := dqlExecutor.ExecuteQueryWithOptions("fetch logs | limit 5", opts)
		if err != nil {
			t.Fatalf("Failed to execute query with multiple segments: %v", err)
		}

		if result.State != "SUCCEEDED" {
			t.Errorf("Query state mismatch: got %s, want SUCCEEDED", result.State)
		}
		t.Logf("Query with multiple segments succeeded (state: %s)", result.State)
	})

	// Step 4: Verify query with non-existent segment fails gracefully
	t.Run("query with non-existent segment", func(t *testing.T) {
		opts := exec.DQLExecuteOptions{
			OutputFormat: "json",
			Segments: []exec.FilterSegmentRef{
				{ID: "non-existent-uid-12345"},
			},
		}

		_, err := dqlExecutor.ExecuteQueryWithOptions("fetch logs | limit 1", opts)
		if err == nil {
			t.Log("Note: Query with non-existent segment did not return an error (API may ignore unknown segments)")
		} else {
			t.Logf("Query with non-existent segment returned expected error: %v", err)
		}
	})
}

func TestSegmentListPagination(t *testing.T) {
	env := integration.SetupIntegration(t)
	defer env.Cleanup.Cleanup(t)

	handler := segment.NewHandler(env.Client)

	// List should work even if there are no segments (or many)
	t.Log("Testing segment list (may include pre-existing segments)...")
	list, err := handler.List()
	if err != nil {
		t.Fatalf("Failed to list segments: %v", err)
	}

	t.Logf("Listed %d segments (totalCount: %d)", len(list.FilterSegments), list.TotalCount)

	// Verify consistency
	if len(list.FilterSegments) != list.TotalCount {
		t.Errorf("Segment count mismatch: len=%d, totalCount=%d", len(list.FilterSegments), list.TotalCount)
	}

	// Verify each segment has required fields
	for i, s := range list.FilterSegments {
		if s.UID == "" {
			t.Errorf("Segment %d has empty UID", i)
		}
		if s.Name == "" {
			t.Errorf("Segment %d has empty name", i)
		}
	}
}
