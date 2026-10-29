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

func TestNotebookLifecycle(t *testing.T) {
	env := integration.SetupIntegration(t)
	defer env.Cleanup.Cleanup(t)

	handler := document.NewHandler(env.Client)

	tests := []struct {
		name    string
		wantErr bool
	}{
		{
			name:    "complete notebook lifecycle",
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Step 1: Create notebook
			t.Log("Step 1: Creating notebook...")
			createData := integration.NotebookFixture(env.TestPrefix)

			// Parse to get the name from fixture
			var notebookContent map[string]interface{}
			if err := json.Unmarshal(createData, &notebookContent); err != nil {
				t.Fatalf("Failed to parse notebook fixture: %v", err)
			}
			notebookName := notebookContent["name"].(string)

			// Generate a unique ID for the notebook
			notebookID := fmt.Sprintf("%s-notebook-id", env.TestPrefix)

			created, err := handler.Create(document.CreateRequest{
				ID:      notebookID,
				Name:    notebookName,
				Type:    "notebook",
				Content: createData,
			})
			if err != nil {
				t.Fatalf("Failed to create notebook: %v", err)
			}
			if created.ID == "" {
				t.Fatal("Created notebook has no ID")
			}
			t.Logf("✓ Created notebook: %s (ID: %s, Version: %d)", created.Name, created.ID, created.Version)

			// Track for cleanup (with version)
			env.Cleanup.TrackDocument("notebook", created.ID, created.Name, created.Version)

			// Step 2: Get notebook
			t.Log("Step 2: Getting notebook...")
			retrieved, err := handler.Get(created.ID)
			if err != nil {
				t.Fatalf("Failed to get notebook: %v", err)
			}
			if retrieved.ID != created.ID {
				t.Errorf("Retrieved notebook ID mismatch: got %s, want %s", retrieved.ID, created.ID)
			}
			if retrieved.Name != created.Name {
				t.Errorf("Retrieved notebook name mismatch: got %s, want %s", retrieved.Name, created.Name)
			}
			t.Logf("✓ Retrieved notebook: %s (Version: %d)", retrieved.Name, retrieved.Version)

			// Step 3: List notebooks (verify our notebook appears)
			t.Log("Step 3: Listing notebooks...")
			list, err := handler.List(document.DocumentFilters{
				Type: "notebook",
			})
			if err != nil {
				t.Fatalf("Failed to list notebooks: %v", err)
			}
			found := false
			for _, doc := range list.Documents {
				if doc.ID == created.ID {
					found = true
					break
				}
			}
			if !found {
				t.Log("Note: Created notebook not found in list (notebooks may take time to appear in list API)")
			} else {
				t.Logf("✓ Found notebook in list (total: %d notebooks)", list.TotalCount)
			}

			// Step 4: Update notebook
			t.Log("Step 4: Updating notebook...")
			updateData := integration.NotebookFixtureModified(env.TestPrefix)
			updated, err := handler.Update(created.ID, created.Version, updateData, "application/json")
			if err != nil {
				t.Fatalf("Failed to update notebook: %v", err)
			}
			if updated.Version <= created.Version {
				t.Errorf("Notebook version should have incremented: got %d, previous %d", updated.Version, created.Version)
			}
			t.Logf("✓ Updated notebook: Version %d → %d", created.Version, updated.Version)

			// Update the version in cleanup tracker
			env.Cleanup.TrackDocument("notebook", updated.ID, updated.Name, updated.Version)

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
					env.Cleanup.TrackDocument("notebook", restored.ID, restored.Name, restored.Version)
				}
			}

			// Step 8: Delete notebook
			t.Log("Step 8: Deleting notebook...")
			// Fetch current version before delete
			current, err := handler.Get(created.ID)
			if err != nil {
				t.Fatalf("Failed to get current notebook version: %v", err)
			}

			err = handler.Delete(created.ID, current.Version)
			if err != nil {
				t.Fatalf("Failed to delete notebook: %v", err)
			}
			t.Logf("✓ Deleted notebook: %s", created.ID)

			// Step 9: Verify deletion (should get error/404)
			t.Log("Step 9: Verifying deletion...")
			_, err = handler.Get(created.ID)
			if err == nil {
				t.Error("Expected error when getting deleted notebook, got nil")
			} else {
				t.Logf("✓ Verified deletion (got expected error: %v)", err)
			}
		})
	}
}

func TestNotebookUpdate(t *testing.T) {
	env := integration.SetupIntegration(t)
	defer env.Cleanup.Cleanup(t)

	handler := document.NewHandler(env.Client)

	// Create a notebook first
	createData := integration.NotebookFixture(env.TestPrefix)
	var notebookContent map[string]interface{}
	json.Unmarshal(createData, &notebookContent)
	notebookName := notebookContent["name"].(string)

	// Generate a unique ID for the notebook
	notebookID := fmt.Sprintf("%s-notebook-id", env.TestPrefix)

	created, err := handler.Create(document.CreateRequest{
		ID:      notebookID,
		Name:    notebookName,
		Type:    "notebook",
		Content: createData,
	})
	if err != nil {
		t.Fatalf("Failed to create notebook: %v", err)
	}
	env.Cleanup.TrackDocument("notebook", created.ID, created.Name, created.Version)

	tests := []struct {
		name    string
		update  func() []byte
		wantErr bool
	}{
		{
			name: "valid update with new section",
			update: func() []byte {
				return integration.NotebookFixtureModified(env.TestPrefix)
			},
			wantErr: false,
		},
		{
			name: "update with content changes",
			update: func() []byte {
				notebook := map[string]interface{}{
					"name": notebookName,
					"sections": []map[string]interface{}{
						{
							"type":     "markdown",
							"title":    "Updated Section",
							"state":    "default",
							"markdown": "# Updated Content\n\nThis section has been updated.",
						},
					},
				}
				data, _ := json.Marshal(notebook)
				return data
			},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			updateData := tt.update()

			// Get current version before update
			current, err := handler.Get(created.ID)
			if err != nil {
				t.Fatalf("Failed to get current version: %v", err)
			}

			updated, err := handler.Update(created.ID, current.Version, updateData, "application/json")
			if (err != nil) != tt.wantErr {
				t.Errorf("Update() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if !tt.wantErr && updated == nil {
				t.Error("Update() returned nil notebook")
			}
			if !tt.wantErr {
				t.Logf("✓ Updated notebook successfully (Version: %d → %d)", current.Version, updated.Version)

				// Update cleanup tracker
				env.Cleanup.TrackDocument("notebook", updated.ID, updated.Name, updated.Version)
			}
		})
	}
}
