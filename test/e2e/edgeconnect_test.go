//go:build integration
// +build integration

package e2e

import (
	"testing"

	"github.com/Dynatrace/dtctl/pkg/resources/edgeconnect"
	"github.com/Dynatrace/dtctl/test/integration"
)

func TestEdgeConnectLifecycle(t *testing.T) {
	env := integration.SetupIntegration(t)
	defer env.Cleanup.Cleanup(t)

	handler := edgeconnect.NewHandler(env.Client)

	tests := []struct {
		name    string
		wantErr bool
	}{
		{
			name:    "complete edgeconnect lifecycle",
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Step 1: Create EdgeConnect
			t.Log("Step 1: Creating EdgeConnect...")
			fixture := integration.EdgeConnectFixture(env.TestPrefix)
			createReq := edgeconnect.EdgeConnectCreate{
				Name:          fixture["name"].(string),
				HostPatterns:  convertToStringSlice(fixture["hostPatterns"].([]string)),
				OAuthClientID: fixture["oauthClientId"].(string),
			}

			created, err := handler.Create(createReq)
			if err != nil {
				t.Fatalf("Failed to create EdgeConnect: %v", err)
			}
			if created.ID == "" {
				t.Fatal("Created EdgeConnect has no ID")
			}
			t.Logf("✓ Created EdgeConnect: %s (ID: %s)", created.Name, created.ID)

			// Track for cleanup
			env.Cleanup.Track("edgeconnect", created.ID, created.Name)

			// Step 2: Get EdgeConnect
			t.Log("Step 2: Getting EdgeConnect...")
			retrieved, err := handler.Get(created.ID)
			if err != nil {
				t.Fatalf("Failed to get EdgeConnect: %v", err)
			}
			if retrieved.ID != created.ID {
				t.Errorf("Retrieved EdgeConnect ID mismatch: got %s, want %s", retrieved.ID, created.ID)
			}
			if retrieved.Name != created.Name {
				t.Errorf("Retrieved EdgeConnect name mismatch: got %s, want %s", retrieved.Name, created.Name)
			}
			t.Logf("✓ Retrieved EdgeConnect: %s", retrieved.Name)

			// Step 3: List EdgeConnects (verify our EdgeConnect appears)
			t.Log("Step 3: Listing EdgeConnects...")
			list, err := handler.List()
			if err != nil {
				t.Fatalf("Failed to list EdgeConnects: %v", err)
			}
			found := false
			for _, ec := range list.EdgeConnects {
				if ec.ID == created.ID {
					found = true
					break
				}
			}
			if !found {
				t.Error("Created EdgeConnect not found in list")
			} else {
				t.Logf("✓ Found EdgeConnect in list (total: %d EdgeConnects)", list.TotalCount)
			}

			// Step 4: Update EdgeConnect
			t.Log("Step 4: Updating EdgeConnect...")
			updateFixture := integration.EdgeConnectFixtureModified(env.TestPrefix)
			// Note: Name cannot be changed after creation
			updateReq := edgeconnect.EdgeConnect{
				Name:          created.Name, // Keep original name
				HostPatterns:  convertToStringSlice(updateFixture["hostPatterns"].([]string)),
				OAuthClientID: fixture["oauthClientId"].(string), // Keep original OAuth client ID
			}

			err = handler.Update(created.ID, updateReq)
			if err != nil {
				t.Fatalf("Failed to update EdgeConnect: %v", err)
			}
			t.Logf("✓ Updated EdgeConnect host patterns")

			// Step 5: Verify update
			t.Log("Step 5: Verifying update...")
			updated, err := handler.Get(created.ID)
			if err != nil {
				t.Fatalf("Failed to get updated EdgeConnect: %v", err)
			}
			if len(updated.HostPatterns) != len(updateReq.HostPatterns) {
				t.Errorf("EdgeConnect host patterns count mismatch: got %d, want %d", len(updated.HostPatterns), len(updateReq.HostPatterns))
			}
			t.Logf("✓ Verified update (Host Patterns: %d)", len(updated.HostPatterns))

			// Step 6: Delete EdgeConnect
			t.Log("Step 6: Deleting EdgeConnect...")
			err = handler.Delete(created.ID)
			if err != nil {
				t.Fatalf("Failed to delete EdgeConnect: %v", err)
			}
			t.Logf("✓ Deleted EdgeConnect: %s", created.ID)

			// Step 7: Verify deletion (should get error/404)
			t.Log("Step 7: Verifying deletion...")
			_, err = handler.Get(created.ID)
			if err == nil {
				t.Error("Expected error when getting deleted EdgeConnect, got nil")
			} else {
				t.Logf("✓ Verified deletion (got expected error: %v)", err)
			}
		})
	}
}

func TestEdgeConnectCreateInvalid(t *testing.T) {
	env := integration.SetupIntegration(t)
	defer env.Cleanup.Cleanup(t)

	handler := edgeconnect.NewHandler(env.Client)

	tests := []struct {
		name    string
		req     edgeconnect.EdgeConnectCreate
		wantErr bool
	}{
		{
			name: "missing name",
			req: edgeconnect.EdgeConnectCreate{
				Name:         "",
				HostPatterns: []string{"*.example.com"},
			},
			wantErr: true,
		},
		// Note: Skipping "valid edgeconnect" test as there may be environment limits on EdgeConnect creation
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
			// If creation succeeded, clean up
			if err == nil && created != nil {
				env.Cleanup.Track("edgeconnect", created.ID, created.Name)
				t.Logf("✓ Created EdgeConnect (will be cleaned up): %s", created.ID)
			}
		})
	}
}

func TestEdgeConnectUpdate(t *testing.T) {
	env := integration.SetupIntegration(t)
	defer env.Cleanup.Cleanup(t)

	handler := edgeconnect.NewHandler(env.Client)

	// Create an EdgeConnect first
	fixture := integration.EdgeConnectFixture(env.TestPrefix)
	createReq := edgeconnect.EdgeConnectCreate{
		Name:          fixture["name"].(string),
		HostPatterns:  convertToStringSlice(fixture["hostPatterns"].([]string)),
		OAuthClientID: fixture["oauthClientId"].(string),
	}

	created, err := handler.Create(createReq)
	if err != nil {
		t.Fatalf("Failed to create EdgeConnect: %v", err)
	}
	env.Cleanup.Track("edgeconnect", created.ID, created.Name)

	t.Logf("Created EdgeConnect: %s (ID: %s)", created.Name, created.ID)

	tests := []struct {
		name    string
		update  edgeconnect.EdgeConnect
		wantErr bool
	}{
		{
			name: "update host patterns",
			update: edgeconnect.EdgeConnect{
				Name: created.Name, // Name cannot be changed
				HostPatterns: []string{
					"*.modified.test.invalid",
					"*.another.test.invalid",
				},
				OAuthClientID: created.OAuthClientID, // Required field
			},
			wantErr: false,
		},
		{
			name: "update with empty name",
			update: edgeconnect.EdgeConnect{
				Name:         "",
				HostPatterns: []string{"*.test.invalid"},
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Get current state
			current, err := handler.Get(created.ID)
			if err != nil {
				t.Fatalf("Failed to get current EdgeConnect: %v", err)
			}

			err = handler.Update(created.ID, tt.update)
			if (err != nil) != tt.wantErr {
				t.Errorf("Update() error = %v, wantErr %v", err, tt.wantErr)
				return
			}

			if !tt.wantErr {
				// Verify the update
				updated, err := handler.Get(created.ID)
				if err != nil {
					t.Fatalf("Failed to get updated EdgeConnect: %v", err)
				}
				if len(updated.HostPatterns) != len(tt.update.HostPatterns) {
					t.Errorf("Host patterns not updated: got %d, want %d", len(updated.HostPatterns), len(tt.update.HostPatterns))
				}
				t.Logf("✓ Updated EdgeConnect successfully (Host Patterns: %d → %d)", len(current.HostPatterns), len(updated.HostPatterns))
			} else {
				t.Logf("✓ Got expected error: %v", err)
			}
		})
	}
}

func TestEdgeConnectGetNonExistent(t *testing.T) {
	env := integration.SetupIntegration(t)
	defer env.Cleanup.Cleanup(t)

	handler := edgeconnect.NewHandler(env.Client)

	t.Run("get non-existent edgeconnect", func(t *testing.T) {
		_, err := handler.Get("non-existent-id-12345")
		if err == nil {
			t.Error("Expected error when getting non-existent EdgeConnect, got nil")
		} else {
			t.Logf("✓ Got expected error: %v", err)
		}
	})
}

// Helper function to convert []string to []string (type assertion safety)
func convertToStringSlice(input []string) []string {
	return input
}
