//go:build integration
// +build integration

package e2e

import (
	"encoding/json"
	"fmt"
	"testing"

	"github.com/Dynatrace/dtctl/pkg/resources/document"
	"github.com/Dynatrace/dtctl/test/integration"
)

func TestDashboardLifecycle(t *testing.T) {
	env := integration.SetupIntegration(t)
	defer env.Cleanup.Cleanup(t)

	handler := document.NewHandler(env.Client)

	tests := []struct {
		name    string
		wantErr bool
	}{
		{
			name:    "complete dashboard lifecycle",
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Step 1: Create dashboard
			t.Log("Step 1: Creating dashboard...")
			createData := integration.DashboardFixture(env.TestPrefix)

			// Parse to get the name from fixture
			var dashboardContent map[string]interface{}
			if err := json.Unmarshal(createData, &dashboardContent); err != nil {
				t.Fatalf("Failed to parse dashboard fixture: %v", err)
			}
			metadata := dashboardContent["dashboardMetadata"].(map[string]interface{})
			dashboardName := metadata["name"].(string)

			created, err := handler.Create(document.CreateRequest{
				Name:    dashboardName,
				Type:    "dashboard",
				Content: createData,
			})
			if err != nil {
				t.Fatalf("Failed to create dashboard: %v", err)
			}
			if created.ID == "" {
				t.Fatal("Created dashboard has no ID")
			}
			t.Logf("✓ Created dashboard: %s (ID: %s, Version: %d)", created.Name, created.ID, created.Version)

			// Track for cleanup (with version)
			env.Cleanup.TrackDocument("dashboard", created.ID, created.Name, created.Version)

			// Step 2: Get dashboard
			t.Log("Step 2: Getting dashboard...")
			retrieved, err := handler.Get(created.ID)
			if err != nil {
				t.Fatalf("Failed to get dashboard: %v", err)
			}
			if retrieved.ID != created.ID {
				t.Errorf("Retrieved dashboard ID mismatch: got %s, want %s", retrieved.ID, created.ID)
			}
			if retrieved.Name != created.Name {
				t.Errorf("Retrieved dashboard name mismatch: got %s, want %s", retrieved.Name, created.Name)
			}
			// CRITICAL: Verify content is not empty or truncated
			if len(retrieved.Content) == 0 {
				t.Error("Retrieved dashboard has empty content")
			}
			if len(retrieved.Content) < len(createData) {
				t.Errorf("Retrieved dashboard content appears truncated: got %d bytes, expected at least %d bytes",
					len(retrieved.Content), len(createData))
			}
			// Verify content is valid JSON
			var contentCheck map[string]interface{}
			if err := json.Unmarshal(retrieved.Content, &contentCheck); err != nil {
				t.Errorf("Retrieved dashboard content is not valid JSON: %v", err)
			}
			t.Logf("✓ Retrieved dashboard: %s (Version: %d, Content: %d bytes)", retrieved.Name, retrieved.Version, len(retrieved.Content))

			// Step 3: List dashboards (verify our dashboard appears)
			t.Log("Step 3: Listing dashboards...")
			list, err := handler.List(document.DocumentFilters{
				Type: "dashboard",
			})
			if err != nil {
				t.Fatalf("Failed to list dashboards: %v", err)
			}
			found := false
			for _, doc := range list.Documents {
				if doc.ID == created.ID {
					found = true
					break
				}
			}
			if !found {
				t.Log("Note: Created dashboard not found in list (dashboards may take time to appear in list API)")
			} else {
				t.Logf("✓ Found dashboard in list (total: %d dashboards)", list.TotalCount)
			}

			// Step 4: Update dashboard
			t.Log("Step 4: Updating dashboard...")
			updateData := integration.DashboardFixtureModified(env.TestPrefix)
			updated, err := handler.Update(created.ID, created.Version, updateData, "application/json")
			if err != nil {
				t.Fatalf("Failed to update dashboard: %v", err)
			}
			if updated.Version <= created.Version {
				t.Errorf("Dashboard version should have incremented: got %d, previous %d", updated.Version, created.Version)
			}
			t.Logf("✓ Updated dashboard: Version %d → %d", created.Version, updated.Version)

			// Update the version in cleanup tracker
			env.Cleanup.TrackDocument("dashboard", updated.ID, updated.Name, updated.Version)

			// Step 5: List snapshots (should exist after update)
			t.Log("Step 5: Listing snapshots...")
			snapshots, err := handler.ListSnapshots(created.ID)
			if err != nil {
				t.Fatalf("Failed to list snapshots: %v", err)
			}
			if len(snapshots.Snapshots) == 0 {
				t.Log("Note: No snapshots found (snapshots may be created asynchronously)")
			} else {
				t.Logf("✓ Found %d snapshot(s)", len(snapshots.Snapshots))

				// Step 6: Get specific snapshot
				if len(snapshots.Snapshots) > 0 {
					t.Log("Step 6: Getting specific snapshot...")
					firstSnapshot := snapshots.Snapshots[0]
					snapshotData, err := handler.GetSnapshot(created.ID, firstSnapshot.SnapshotVersion)
					if err != nil {
						t.Fatalf("Failed to get snapshot: %v", err)
					}
					if snapshotData.SnapshotVersion != firstSnapshot.SnapshotVersion {
						t.Errorf("Snapshot version mismatch: got %d, want %d", snapshotData.SnapshotVersion, firstSnapshot.SnapshotVersion)
					}
					t.Logf("✓ Retrieved snapshot version %d", snapshotData.SnapshotVersion)

					// Step 7: Restore from snapshot
					t.Log("Step 7: Restoring from snapshot...")
					restored, err := handler.RestoreSnapshot(created.ID, firstSnapshot.SnapshotVersion)
					if err != nil {
						t.Fatalf("Failed to restore snapshot: %v", err)
					}
					if restored.Version <= updated.Version {
						t.Log("Note: Restored version should be newer (restore creates new version)")
					}
					t.Logf("✓ Restored to snapshot %d (new version: %d)", firstSnapshot.SnapshotVersion, restored.Version)

					// Update version in cleanup tracker again
					env.Cleanup.TrackDocument("dashboard", restored.ID, restored.Name, restored.Version)
				}
			}

			// Step 8: Delete dashboard
			t.Log("Step 8: Deleting dashboard...")
			// Fetch current version before delete
			current, err := handler.Get(created.ID)
			if err != nil {
				t.Fatalf("Failed to get current dashboard version: %v", err)
			}

			err = handler.Delete(created.ID, current.Version)
			if err != nil {
				t.Fatalf("Failed to delete dashboard: %v", err)
			}
			t.Logf("✓ Deleted dashboard: %s", created.ID)

			// Step 9: Verify deletion (should get error/404)
			t.Log("Step 9: Verifying deletion...")
			_, err = handler.Get(created.ID)
			if err == nil {
				t.Error("Expected error when getting deleted dashboard, got nil")
			} else {
				t.Logf("✓ Verified deletion (got expected error: %v)", err)
			}
		})
	}
}

func TestDashboardCreateInvalid(t *testing.T) {
	env := integration.SetupIntegration(t)
	defer env.Cleanup.Cleanup(t)

	handler := document.NewHandler(env.Client)

	tests := []struct {
		name    string
		req     document.CreateRequest
		wantErr bool
	}{
		{
			name: "missing name",
			req: document.CreateRequest{
				Type:    "dashboard",
				Content: []byte(`{}`),
			},
			wantErr: true,
		},
		{
			name: "missing type",
			req: document.CreateRequest{
				Name:    fmt.Sprintf("%s-invalid", env.TestPrefix),
				Content: []byte(`{}`),
			},
			wantErr: true,
		},
		{
			name: "missing content",
			req: document.CreateRequest{
				Name: fmt.Sprintf("%s-invalid", env.TestPrefix),
				Type: "dashboard",
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := handler.Create(tt.req)
			if (err != nil) != tt.wantErr {
				t.Errorf("Create() error = %v, wantErr %v", err, tt.wantErr)
			}
			if err != nil {
				t.Logf("✓ Got expected error: %v", err)
			}
		})
	}
}

func TestDashboardOptimisticLocking(t *testing.T) {
	env := integration.SetupIntegration(t)
	defer env.Cleanup.Cleanup(t)

	handler := document.NewHandler(env.Client)

	// Create a dashboard
	createData := integration.DashboardFixture(env.TestPrefix)
	var dashboardContent map[string]interface{}
	json.Unmarshal(createData, &dashboardContent)
	metadata := dashboardContent["dashboardMetadata"].(map[string]interface{})
	dashboardName := metadata["name"].(string)

	// Generate a unique ID for the dashboard
	dashboardID := fmt.Sprintf("%s-dashboard-id", env.TestPrefix)

	created, err := handler.Create(document.CreateRequest{
		ID:      dashboardID,
		Name:    dashboardName,
		Type:    "dashboard",
		Content: createData,
	})
	if err != nil {
		t.Fatalf("Failed to create dashboard: %v", err)
	}
	env.Cleanup.TrackDocument("dashboard", created.ID, created.Name, created.Version)

	t.Logf("Created dashboard: %s (Version: %d)", created.Name, created.Version)

	// Test updating with stale version (should fail)
	t.Run("update with stale version", func(t *testing.T) {
		// First update
		updateData := integration.DashboardFixtureModified(env.TestPrefix)
		updated, err := handler.Update(created.ID, created.Version, updateData, "application/json")
		if err != nil {
			t.Fatalf("First update failed: %v", err)
		}
		t.Logf("First update successful: Version %d → %d", created.Version, updated.Version)

		// Try to update with old version (should fail with 409)
		_, err = handler.Update(created.ID, created.Version, updateData, "application/json")
		if err == nil {
			t.Error("Expected error when updating with stale version, got nil")
		} else {
			t.Logf("✓ Got expected optimistic locking error: %v", err)
		}

		// Update cleanup tracker with current version
		env.Cleanup.TrackDocument("dashboard", updated.ID, updated.Name, updated.Version)
	})
}

// TestDashboardLargeContent tests that large dashboards don't get truncated
// This is a regression test for the bug where resp.String() was truncating large responses
func TestDashboardLargeContent(t *testing.T) {
	env := integration.SetupIntegration(t)
	defer env.Cleanup.Cleanup(t)

	handler := document.NewHandler(env.Client)

	t.Log("Creating large dashboard (>10KB)...")
	createData := integration.DashboardFixtureLarge(env.TestPrefix)

	// Parse to get the name from fixture
	var dashboardContent map[string]interface{}
	if err := json.Unmarshal(createData, &dashboardContent); err != nil {
		t.Fatalf("Failed to parse dashboard fixture: %v", err)
	}
	metadata := dashboardContent["dashboardMetadata"].(map[string]interface{})
	dashboardName := metadata["name"].(string)

	t.Logf("Original dashboard size: %d bytes", len(createData))

	created, err := handler.Create(document.CreateRequest{
		Name:    dashboardName,
		Type:    "dashboard",
		Content: createData,
	})
	if err != nil {
		t.Fatalf("Failed to create large dashboard: %v", err)
	}
	if created.ID == "" {
		t.Fatal("Created dashboard has no ID")
	}

	// Track for cleanup
	env.Cleanup.TrackDocument("dashboard", created.ID, created.Name, created.Version)
	t.Logf("✓ Created large dashboard: %s (ID: %s)", created.Name, created.ID)

	// Retrieve the dashboard - this is where truncation would occur
	t.Log("Retrieving large dashboard...")
	retrieved, err := handler.Get(created.ID)
	if err != nil {
		t.Fatalf("Failed to get large dashboard: %v", err)
	}

	// CRITICAL: Verify content is not empty
	if len(retrieved.Content) == 0 {
		t.Fatal("Retrieved dashboard has empty content - TRUNCATION BUG!")
	}

	t.Logf("Retrieved dashboard size: %d bytes", len(retrieved.Content))

	// Verify content is approximately the same size (allowing for some API transformation)
	// But it should be at least 80% of the original size
	minExpectedSize := int(float64(len(createData)) * 0.8)
	if len(retrieved.Content) < minExpectedSize {
		t.Fatalf("Retrieved dashboard content appears truncated: got %d bytes, expected at least %d bytes (original: %d bytes)",
			len(retrieved.Content), minExpectedSize, len(createData))
	}

	// Verify content is valid JSON
	var contentCheck map[string]interface{}
	if err := json.Unmarshal(retrieved.Content, &contentCheck); err != nil {
		t.Fatalf("Retrieved dashboard content is not valid JSON: %v", err)
	}

	// Verify tiles are present
	tiles, ok := contentCheck["tiles"].([]interface{})
	if !ok {
		t.Fatal("Retrieved dashboard has no tiles array")
	}

	originalTiles := dashboardContent["tiles"].([]interface{})
	if len(tiles) != len(originalTiles) {
		t.Errorf("Tile count mismatch: got %d tiles, expected %d tiles - possible truncation",
			len(tiles), len(originalTiles))
	}

	t.Logf("✓ Large dashboard retrieved successfully with %d tiles and %d bytes of content",
		len(tiles), len(retrieved.Content))
}
