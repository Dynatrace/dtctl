//go:build integration
// +build integration

package e2e

import (
	"strings"
	"testing"

	"github.com/Dynatrace/dtctl/pkg/resources/settings"
	"github.com/Dynatrace/dtctl/test/integration"
)

func TestSettingsListSchemas(t *testing.T) {
	env := integration.SetupIntegration(t)
	defer env.Cleanup.Cleanup(t)

	handler := settings.NewHandler(env.Client)

	t.Log("Step 1: Listing all schemas...")
	schemaList, err := handler.ListSchemas()
	if err != nil {
		t.Fatalf("Failed to list schemas: %v", err)
	}

	if schemaList.TotalCount == 0 {
		t.Fatal("No schemas returned")
	}

	t.Logf("✓ Listed %d schemas", schemaList.TotalCount)

	// Verify we have OpenPipeline schemas
	foundOpenPipeline := false
	for _, schema := range schemaList.Items {
		if strings.Contains(schema.SchemaID, "openpipeline") {
			foundOpenPipeline = true
			t.Logf("  Found OpenPipeline schema: %s - %s", schema.SchemaID, schema.DisplayName)
			break
		}
	}

	if !foundOpenPipeline {
		t.Error("No OpenPipeline schemas found")
	}
}

func TestSettingsGetSchema(t *testing.T) {
	env := integration.SetupIntegration(t)
	defer env.Cleanup.Cleanup(t)

	handler := settings.NewHandler(env.Client)

	// Test with a known schema
	schemaID := "builtin:openpipeline.logs.pipelines"
	t.Logf("Getting schema: %s", schemaID)

	schema, err := handler.GetSchema(schemaID)
	if err != nil {
		t.Fatalf("Failed to get schema: %v", err)
	}

	// Verify schema structure
	if schemaIDField, ok := schema["schemaId"].(string); ok {
		if schemaIDField != schemaID {
			t.Errorf("Schema ID = %v, want %v", schemaIDField, schemaID)
		}
	} else {
		t.Error("schemaId field not found in schema")
	}

	if displayName, ok := schema["displayName"].(string); ok {
		t.Logf("✓ Got schema: %s", displayName)
	} else {
		t.Error("displayName field not found in schema")
	}

	// Check for properties
	if properties, ok := schema["properties"].(map[string]any); ok {
		t.Logf("  Properties: %d defined", len(properties))
	}
}

func TestSettingsGetNonExistentSchema(t *testing.T) {
	env := integration.SetupIntegration(t)
	defer env.Cleanup.Cleanup(t)

	handler := settings.NewHandler(env.Client)

	schemaID := "builtin:nonexistent.schema.doesnotexist"
	t.Logf("Attempting to get non-existent schema: %s", schemaID)

	_, err := handler.GetSchema(schemaID)
	if err == nil {
		t.Fatal("Expected error for non-existent schema, got nil")
	}

	if !strings.Contains(err.Error(), "not found") {
		t.Errorf("Expected 'not found' error, got: %v", err)
	}

	t.Logf("✓ Got expected error: %v", err)
}

func TestSettingsListObjects(t *testing.T) {
	env := integration.SetupIntegration(t)
	defer env.Cleanup.Cleanup(t)

	handler := settings.NewHandler(env.Client)

	// List settings objects for a known schema
	schemaID := "builtin:openpipeline.logs.pipelines"
	t.Logf("Listing settings objects for schema: %s", schemaID)

	objectsList, err := handler.ListObjects(schemaID, "environment", 0)
	if err != nil {
		t.Fatalf("Failed to list objects: %v", err)
	}

	t.Logf("✓ Listed settings objects (total: %d)", objectsList.TotalCount)

	// Note: The count might be 0 if no pipelines are configured
	if objectsList.TotalCount > 0 {
		t.Logf("  Found %d objects", len(objectsList.Items))
		for i, obj := range objectsList.Items {
			if i >= 3 {
				t.Logf("  ... and %d more", len(objectsList.Items)-3)
				break
			}
			t.Logf("  - %s: %s", obj.ObjectID, obj.Summary)
		}
	}
}

func TestSettingsListObjectsWithPagination(t *testing.T) {
	env := integration.SetupIntegration(t)
	defer env.Cleanup.Cleanup(t)

	handler := settings.NewHandler(env.Client)

	// List with pagination enabled (chunkSize > 0)
	schemaID := "builtin:openpipeline.logs.pipelines"
	chunkSize := int64(10)

	t.Logf("Listing settings objects with pagination (chunkSize=%d)...", chunkSize)

	objectsList, err := handler.ListObjects(schemaID, "", chunkSize)
	if err != nil {
		t.Fatalf("Failed to list objects with pagination: %v", err)
	}

	t.Logf("✓ Successfully paginated through objects (total: %d)", objectsList.TotalCount)
}

func TestSettingsGetNonExistentObject(t *testing.T) {
	env := integration.SetupIntegration(t)
	defer env.Cleanup.Cleanup(t)

	handler := settings.NewHandler(env.Client)

	objectID := "aaaaaaaa-bbbb-cccc-dddd-000000000000"
	t.Logf("Attempting to get non-existent object: %s", objectID)

	_, err := handler.Get(objectID)
	if err == nil {
		t.Fatal("Expected error for non-existent object, got nil")
	}

	if !strings.Contains(err.Error(), "not found") {
		t.Errorf("Expected 'not found' error, got: %v", err)
	}

	t.Logf("✓ Got expected error: %v", err)
}

func TestSettingsListObjectsScopePopulated(t *testing.T) {
	env := integration.SetupIntegration(t)
	defer env.Cleanup.Cleanup(t)

	handler := settings.NewHandler(env.Client)

	// Use a schema that has entity-scoped objects (not singletons) so we can
	// verify scope splitting into ScopeType/ScopeID.
	schemaID := "builtin:anomaly-detection.services"
	t.Logf("Listing objects for schema: %s", schemaID)

	list, err := handler.ListObjects(schemaID, "", 0)
	if err != nil {
		t.Fatalf("ListObjects() error = %v", err)
	}

	if list.TotalCount == 0 {
		t.Skip("No settings objects found for this schema — cannot verify scope population")
	}

	t.Logf("✓ Listed %d objects", list.TotalCount)

	for i, obj := range list.Items {
		if obj.Scope == "" {
			t.Errorf("item[%d] Scope is empty — fields parameter may not be working", i)
			continue
		}
		if obj.ScopeType == "" {
			t.Errorf("item[%d] ScopeType is empty for scope %q", i, obj.Scope)
		}
		if i < 3 {
			t.Logf("  item[%d]: scope=%q scopeType=%q scopeID=%q", i, obj.Scope, obj.ScopeType, obj.ScopeID)
		}
	}
}

func TestSettingsListObjectsObjectIDShortPopulated(t *testing.T) {
	env := integration.SetupIntegration(t)
	defer env.Cleanup.Cleanup(t)

	handler := settings.NewHandler(env.Client)

	schemaID := "builtin:anomaly-detection.services"
	list, err := handler.ListObjects(schemaID, "", 0)
	if err != nil {
		t.Fatalf("ListObjects() error = %v", err)
	}
	if list.TotalCount == 0 {
		t.Skip("No settings objects found")
	}

	obj := list.Items[0]
	if obj.ObjectIDShort == "" {
		t.Error("ObjectIDShort is empty")
	}
	if len(obj.ObjectID) > 23 && !strings.HasSuffix(obj.ObjectIDShort, "...") {
		t.Errorf("ObjectIDShort %q should end with '...' for long objectId", obj.ObjectIDShort)
	}
	t.Logf("✓ ObjectIDShort=%q for objectId length %d", obj.ObjectIDShort, len(obj.ObjectID))
}

// TestSettingsCRUDLifecycle tests the full lifecycle of a settings object
// NOTE: This test is commented out by default as it requires careful selection
// of a test-safe schema and may affect the environment. Uncomment and customize
// for specific testing needs.
/*
func TestSettingsCRUDLifecycle(t *testing.T) {
	env := integration.SetupIntegration(t)
	defer env.Cleanup.Cleanup(t)

	handler := settings.NewHandler(env.Client)

	// TODO: Select an appropriate test schema that is safe to create/modify/delete
	// For example, a test configuration schema that doesn't affect production
	testSchemaID := "builtin:some.test.schema"
	testScope := "environment"

	t.Logf("Testing CRUD lifecycle with schema: %s", testSchemaID)

	// Step 1: Create a settings object
	t.Log("Step 1: Creating settings object...")
	createReq := settings.SettingsObjectCreate{
		SchemaID: testSchemaID,
		Scope:    testScope,
		Value: map[string]any{
			"displayName": fmt.Sprintf("Test Settings %s", env.TestPrefix),
			// Add other required fields based on schema
		},
	}

	createResp, err := handler.Create(createReq)
	if err != nil {
		t.Fatalf("Failed to create settings object: %v", err)
	}

	objectID := createResp.ObjectID
	t.Logf("✓ Created settings object: %s", objectID)

	// Track for cleanup
	env.Cleanup.Track("settings", objectID, fmt.Sprintf("Test Settings %s", env.TestPrefix))

	// Step 2: Get the settings object
	t.Log("Step 2: Getting settings object...")
	obj, err := handler.Get(objectID)
	if err != nil {
		t.Fatalf("Failed to get settings object: %v", err)
	}

	if obj.ObjectID != objectID {
		t.Errorf("ObjectID = %v, want %v", obj.ObjectID, objectID)
	}
	if obj.SchemaID != testSchemaID {
		t.Errorf("SchemaID = %v, want %v", obj.SchemaID, testSchemaID)
	}

	t.Logf("✓ Got settings object")
	t.Logf("  Schema: %s", obj.SchemaID)
	t.Logf("  Scope: %s", obj.Scope)
	t.Logf("  Summary: %s", obj.Summary)

	// Step 3: Update the settings object
	t.Log("Step 3: Updating settings object...")
	updatedValue := map[string]any{
		"displayName": fmt.Sprintf("Updated Test Settings %s", env.TestPrefix),
		// Add other fields as needed
	}

	updateResp, err := handler.Update(objectID, updatedValue)
	if err != nil {
		t.Fatalf("Failed to update settings object: %v", err)
	}

	if updateResp.ObjectID != objectID {
		t.Errorf("Updated ObjectID = %v, want %v", updateResp.ObjectID, objectID)
	}

	t.Logf("✓ Updated settings object")

	// Step 4: Verify the update
	t.Log("Step 4: Verifying update...")
	updatedObj, err := handler.Get(objectID)
	if err != nil {
		t.Fatalf("Failed to get updated object: %v", err)
	}

	if displayName, ok := updatedObj.Value["displayName"].(string); ok {
		expectedName := fmt.Sprintf("Updated Test Settings %s", env.TestPrefix)
		if displayName != expectedName {
			t.Errorf("displayName = %v, want %v", displayName, expectedName)
		}
	}

	t.Logf("✓ Verified update")

	// Step 5: Delete the settings object
	t.Log("Step 5: Deleting settings object...")
	err = handler.Delete(objectID)
	if err != nil {
		t.Fatalf("Failed to delete settings object: %v", err)
	}

	t.Logf("✓ Deleted settings object")

	// Step 6: Verify deletion
	t.Log("Step 6: Verifying deletion...")
	_, err = handler.Get(objectID)
	if err == nil {
		t.Error("Expected error when getting deleted object, got nil")
	}

	if !strings.Contains(err.Error(), "not found") {
		t.Errorf("Expected 'not found' error, got: %v", err)
	}

	t.Logf("✓ Verified deletion")

	// Remove from cleanup tracker since we already deleted it
	env.Cleanup.Remove("settings", objectID)
}
*/

func TestSettingsValidateCreate(t *testing.T) {
	env := integration.SetupIntegration(t)
	defer env.Cleanup.Cleanup(t)

	handler := settings.NewHandler(env.Client)

	// Test validation with invalid schema
	t.Log("Testing validation with non-existent schema...")
	createReq := settings.SettingsObjectCreate{
		SchemaID: "builtin:nonexistent.schema",
		Scope:    "environment",
		Value: map[string]any{
			"test": "value",
		},
	}

	err := handler.ValidateCreate(createReq)
	if err == nil {
		t.Error("Expected validation error for non-existent schema, got nil")
	} else {
		t.Logf("✓ Got expected validation error: %v", err)
	}
}

func TestSettingsListObjectsInvalidSchema(t *testing.T) {
	env := integration.SetupIntegration(t)
	defer env.Cleanup.Cleanup(t)

	handler := settings.NewHandler(env.Client)

	// Try to list objects for non-existent schema
	schemaID := "builtin:nonexistent.schema.doesnotexist"
	t.Logf("Attempting to list objects for non-existent schema: %s", schemaID)

	_, err := handler.ListObjects(schemaID, "", 0)
	if err == nil {
		t.Fatal("Expected error for non-existent schema, got nil")
	}

	// Accept either the legacy "not found" wording or the current
	// "does not exist" wording — both ride on top of an HTTP 404 from the
	// Settings API, and the exact phrasing has drifted over time.
	msg := err.Error()
	if !strings.Contains(msg, "404") &&
		!strings.Contains(msg, "not found") &&
		!strings.Contains(msg, "does not exist") {
		t.Errorf("Expected 404 / 'not found' / 'does not exist' error, got: %v", err)
	}

	t.Logf("✓ Got expected error: %v", err)
}
