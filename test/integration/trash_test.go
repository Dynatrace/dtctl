//go:build integration
// +build integration

package integration

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Dynatrace/dtctl/pkg/resources/document"
)

// TestTrash_DashboardLifecycle tests the complete lifecycle of a dashboard through trash
func TestTrash_DashboardLifecycle(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test")
	}

	env := SetupIntegration(t)
	defer env.Cleanup.Cleanup(t)

	handler := document.NewHandler(env.Client)
	trashHandler := document.NewTrashHandler(env.Client)

	// Step 1: Create a dashboard
	dashboardReq := DashboardCreateRequest(env.TestPrefix)
	created, err := handler.Create(dashboardReq)
	require.NoError(t, err, "Failed to create dashboard")
	require.NotEmpty(t, created.ID, "Created dashboard should have an ID")

	dashboardID := created.ID
	dashboardName := dashboardReq.Name
	t.Logf("Created dashboard: %s (ID: %s)", dashboardName, dashboardID)

	// Track the dashboard for cleanup (in case test fails before delete)
	env.Cleanup.TrackDocument("dashboard", dashboardID, dashboardName, created.Version)

	// Step 2: Delete the dashboard (moves to trash)
	err = handler.Delete(dashboardID, created.Version)
	require.NoError(t, err, "Failed to delete dashboard")
	t.Logf("Deleted dashboard: %s", dashboardID)

	// Step 3: Verify dashboard appears in trash
	trash, err := trashHandler.List(document.TrashListOptions{
		Type: "dashboard",
	})
	require.NoError(t, err, "Failed to list trash")

	found := false
	var trashedDoc *document.TrashDocumentListEntry
	for i := range trash {
		if trash[i].ID == dashboardID {
			found = true
			trashedDoc = &trash[i]
			break
		}
	}
	require.True(t, found, "Dashboard should be in trash")
	assert.Equal(t, dashboardName, trashedDoc.Name, "Dashboard name should match")
	assert.Equal(t, "dashboard", trashedDoc.Type, "Document type should be dashboard")
	assert.NotEmpty(t, trashedDoc.DeletedBy, "DeletedBy should be set")
	assert.False(t, trashedDoc.DeletedAt.IsZero(), "DeletedAt should be set")
	t.Logf("Verified dashboard in trash, deleted by: %s at %s", trashedDoc.DeletedBy, trashedDoc.DeletedAt)

	// Step 4: Get specific trashed document
	trashedDetail, err := trashHandler.Get(dashboardID)
	require.NoError(t, err, "Failed to get trashed document")
	assert.Equal(t, dashboardID, trashedDetail.ID, "Trashed document ID should match")
	assert.NotEmpty(t, trashedDetail.DeletedBy, "DeletedBy should be set")
	assert.False(t, trashedDetail.DeletedAt.IsZero(), "DeletedAt should be set")

	// Step 5: Restore the dashboard
	err = trashHandler.Restore(dashboardID, document.RestoreOptions{})
	require.NoError(t, err, "Failed to restore dashboard")
	t.Logf("Restored dashboard: %s", dashboardID)

	// Step 6: Verify dashboard is restored (no longer in trash)
	restored, err := handler.Get(dashboardID)
	require.NoError(t, err, "Failed to get restored dashboard")
	assert.Equal(t, dashboardName, restored.Name, "Restored dashboard name should match")

	// Step 7: Verify dashboard is NOT in trash anymore
	trash, err = trashHandler.List(document.TrashListOptions{
		Type: "dashboard",
	})
	require.NoError(t, err, "Failed to list trash after restore")

	found = false
	for i := range trash {
		if trash[i].ID == dashboardID {
			found = true
			break
		}
	}
	assert.False(t, found, "Dashboard should NOT be in trash after restore")

	// Step 8: Delete again for permanent deletion test
	err = handler.Delete(dashboardID, restored.Version)
	require.NoError(t, err, "Failed to delete dashboard again")

	// Step 9: Permanently delete from trash
	err = trashHandler.Delete(dashboardID)
	require.NoError(t, err, "Failed to permanently delete dashboard")
	t.Logf("Permanently deleted dashboard: %s", dashboardID)

	// Step 10: Verify dashboard is gone from trash
	_, err = trashHandler.Get(dashboardID)
	assert.Error(t, err, "Dashboard should not be found in trash after permanent deletion")
}

// TestTrash_NotebookLifecycle tests the complete lifecycle of a notebook through trash
func TestTrash_NotebookLifecycle(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test")
	}

	env := SetupIntegration(t)
	defer env.Cleanup.Cleanup(t)

	handler := document.NewHandler(env.Client)
	trashHandler := document.NewTrashHandler(env.Client)

	// Create a notebook
	notebookReq := NotebookCreateRequest(env.TestPrefix)
	created, err := handler.Create(notebookReq)
	require.NoError(t, err, "Failed to create notebook")
	require.NotEmpty(t, created.ID, "Created notebook should have an ID")

	notebookID := created.ID
	notebookName := notebookReq.Name
	t.Logf("Created notebook: %s (ID: %s)", notebookName, notebookID)

	env.Cleanup.TrackDocument("notebook", notebookID, notebookName, created.Version)

	// Delete the notebook
	err = handler.Delete(notebookID, created.Version)
	require.NoError(t, err, "Failed to delete notebook")

	// Verify notebook appears in trash
	trash, err := trashHandler.List(document.TrashListOptions{
		Type: "notebook",
	})
	require.NoError(t, err, "Failed to list trash")

	found := false
	for i := range trash {
		if trash[i].ID == notebookID {
			found = true
			assert.Equal(t, "notebook", trash[i].Type, "Document type should be notebook")
			break
		}
	}
	require.True(t, found, "Notebook should be in trash")

	// Restore the notebook
	err = trashHandler.Restore(notebookID, document.RestoreOptions{})
	require.NoError(t, err, "Failed to restore notebook")

	// Verify notebook is restored
	restored, err := handler.Get(notebookID)
	require.NoError(t, err, "Failed to get restored notebook")
	assert.Equal(t, notebookName, restored.Name, "Restored notebook name should match")

	// Cleanup: delete permanently
	err = handler.Delete(notebookID, restored.Version)
	require.NoError(t, err)
	err = trashHandler.Delete(notebookID)
	require.NoError(t, err)
}

// TestTrash_FilterByType tests filtering trash by document type
func TestTrash_FilterByType(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test")
	}

	env := SetupIntegration(t)
	defer env.Cleanup.Cleanup(t)

	handler := document.NewHandler(env.Client)
	trashHandler := document.NewTrashHandler(env.Client)

	// Create and delete a dashboard
	dashboardReq := DashboardCreateRequest(env.TestPrefix + "-filter-dashboard")
	createdDash, err := handler.Create(dashboardReq)
	require.NoError(t, err)
	env.Cleanup.TrackDocument("dashboard", createdDash.ID, dashboardReq.Name, createdDash.Version)

	err = handler.Delete(createdDash.ID, createdDash.Version)
	require.NoError(t, err)

	// Create and delete a notebook
	notebookReq := NotebookCreateRequest(env.TestPrefix + "-filter-notebook")
	createdNote, err := handler.Create(notebookReq)
	require.NoError(t, err)
	env.Cleanup.TrackDocument("notebook", createdNote.ID, notebookReq.Name, createdNote.Version)

	err = handler.Delete(createdNote.ID, createdNote.Version)
	require.NoError(t, err)

	// Wait a bit for trash to be updated
	time.Sleep(1 * time.Second)

	// List trash filtered by dashboard type
	dashTrash, err := trashHandler.List(document.TrashListOptions{
		Type: "dashboard",
	})
	require.NoError(t, err)

	foundDashboard := false
	foundNotebook := false
	for i := range dashTrash {
		if dashTrash[i].ID == createdDash.ID {
			foundDashboard = true
		}
		if dashTrash[i].ID == createdNote.ID {
			foundNotebook = true
		}
	}
	assert.True(t, foundDashboard, "Dashboard should be in dashboard trash list")
	assert.False(t, foundNotebook, "Notebook should NOT be in dashboard trash list")

	// List trash filtered by notebook type
	noteTrash, err := trashHandler.List(document.TrashListOptions{
		Type: "notebook",
	})
	require.NoError(t, err)

	foundDashboard = false
	foundNotebook = false
	for i := range noteTrash {
		if noteTrash[i].ID == createdDash.ID {
			foundDashboard = true
		}
		if noteTrash[i].ID == createdNote.ID {
			foundNotebook = true
		}
	}
	assert.False(t, foundDashboard, "Dashboard should NOT be in notebook trash list")
	assert.True(t, foundNotebook, "Notebook should be in notebook trash list")

	// Cleanup
	_ = trashHandler.Delete(createdDash.ID)
	_ = trashHandler.Delete(createdNote.ID)
}

// TestTrash_EmptyTrash tests the empty trash functionality
func TestTrash_EmptyTrash(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test")
	}

	env := SetupIntegration(t)
	defer env.Cleanup.Cleanup(t)

	handler := document.NewHandler(env.Client)
	trashHandler := document.NewTrashHandler(env.Client)

	// Create and delete multiple documents
	var deletedIDs []string
	for i := 0; i < 3; i++ {
		dashboardReq := DashboardCreateRequest(env.TestPrefix + "-empty-test")
		created, err := handler.Create(dashboardReq)
		require.NoError(t, err)
		env.Cleanup.TrackDocument("dashboard", created.ID, dashboardReq.Name, created.Version)

		err = handler.Delete(created.ID, created.Version)
		require.NoError(t, err)
		deletedIDs = append(deletedIDs, created.ID)
	}

	// Wait a bit for trash to be updated
	time.Sleep(1 * time.Second)

	// Verify all documents are in trash
	trash, err := trashHandler.List(document.TrashListOptions{})
	require.NoError(t, err)

	foundCount := 0
	for i := range trash {
		for _, id := range deletedIDs {
			if trash[i].ID == id {
				foundCount++
				break
			}
		}
	}
	assert.GreaterOrEqual(t, foundCount, len(deletedIDs), "All deleted documents should be in trash")

	// Note: We don't actually test Empty() here because it would delete ALL trash
	// including potentially other tests' trash items. In a real environment,
	// you'd want isolated test tenants or a way to tag test documents.
	t.Log("Skipping actual empty trash test to avoid affecting other tests")

	// Cleanup: delete each document permanently
	for _, id := range deletedIDs {
		_ = trashHandler.Delete(id)
	}
}

// TestTrash_RestoreWithConflict tests restoring a document when a name conflict exists
func TestTrash_RestoreWithConflict(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test")
	}

	env := SetupIntegration(t)
	defer env.Cleanup.Cleanup(t)

	handler := document.NewHandler(env.Client)
	trashHandler := document.NewTrashHandler(env.Client)

	// Create dashboard 1
	dashboard1Req := DashboardCreateRequest(env.TestPrefix + "-conflict")
	created1, err := handler.Create(dashboard1Req)
	require.NoError(t, err)
	env.Cleanup.TrackDocument("dashboard", created1.ID, dashboard1Req.Name, created1.Version)

	// Delete dashboard 1 (moves to trash)
	err = handler.Delete(created1.ID, created1.Version)
	require.NoError(t, err)

	// Create dashboard 2 with the same name
	dashboard2Req := DashboardCreateRequest(env.TestPrefix + "-conflict")
	created2, err := handler.Create(dashboard2Req)
	require.NoError(t, err)
	env.Cleanup.TrackDocument("dashboard", created2.ID, dashboard2Req.Name, created2.Version)

	// Restore dashboard 1 — the API allows restoring even when another
	// dashboard with the same name exists (no conflict error).
	err = trashHandler.Restore(created1.ID, document.RestoreOptions{})
	assert.NoError(t, err, "Restore should succeed even with a duplicate name")

	// Cleanup
	restored1, _ := handler.Get(created1.ID)
	if restored1 != nil {
		_ = handler.Delete(created1.ID, restored1.Version)
		_ = trashHandler.Delete(created1.ID)
	}
	_ = handler.Delete(created2.ID, created2.Version)
}
