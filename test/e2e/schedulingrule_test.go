//go:build integration
// +build integration

package e2e

import (
	"testing"

	"github.com/Dynatrace/dtctl/pkg/resources/schedulingrule"
	"github.com/Dynatrace/dtctl/test/integration"
)

func TestSchedulingRuleLifecycle(t *testing.T) {
	env := integration.SetupIntegration(t)
	defer env.Cleanup.Cleanup(t)

	handler := schedulingrule.NewHandler(env.Client)

	// Step 1: Create scheduling rule
	t.Log("Step 1: Creating scheduling rule...")
	created, err := handler.Create(integration.SchedulingRuleFixture(env.TestPrefix))
	if err != nil {
		t.Fatalf("Failed to create scheduling rule: %v", err)
	}
	if created.ID == "" {
		t.Fatal("Created scheduling rule has no ID")
	}
	t.Logf("✓ Created scheduling rule: %s (ID: %s)", created.Title, created.ID)

	env.Cleanup.Track("scheduling-rule", created.ID, created.Title)

	// Step 2: Get scheduling rule
	t.Log("Step 2: Getting scheduling rule...")
	retrieved, err := handler.Get(created.ID)
	if err != nil {
		t.Fatalf("Failed to get scheduling rule: %v", err)
	}
	if retrieved.ID != created.ID {
		t.Errorf("Retrieved scheduling rule ID mismatch: got %s, want %s", retrieved.ID, created.ID)
	}
	if retrieved.RuleType == "" {
		t.Error("Retrieved scheduling rule has an empty ruleType")
	}
	if len(retrieved.RRule) == 0 {
		t.Error("Retrieved rrule rule has an empty rrule body")
	}
	t.Logf("✓ Retrieved scheduling rule: %s", retrieved.Title)

	// Step 3: List scheduling rules and find ours
	t.Log("Step 3: Listing scheduling rules...")
	list, err := handler.List(500, 0)
	if err != nil {
		t.Fatalf("Failed to list scheduling rules: %v", err)
	}
	found := false
	for _, r := range list.Results {
		if r.ID == created.ID {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("Created scheduling rule %s not found in list of %d", created.ID, len(list.Results))
	}
	// The list must not silently truncate: a paginated List returns everything Count reports.
	if list.Count > len(list.Results) {
		t.Errorf("List truncated: Count = %d but got %d results", list.Count, len(list.Results))
	}
	t.Logf("✓ Listed %d scheduling rules", len(list.Results))

	// Step 4: Update scheduling rule
	t.Log("Step 4: Updating scheduling rule...")
	updated, err := handler.Update(created.ID, integration.SchedulingRuleFixtureModified(env.TestPrefix))
	if err != nil {
		t.Fatalf("Failed to update scheduling rule: %v", err)
	}
	if updated.Title == created.Title {
		t.Errorf("Title unchanged after update: %s", updated.Title)
	}
	t.Logf("✓ Updated scheduling rule: %s", updated.Title)

	// Step 5: Delete scheduling rule
	t.Log("Step 5: Deleting scheduling rule...")
	if err := handler.Delete(created.ID); err != nil {
		t.Fatalf("Failed to delete scheduling rule: %v", err)
	}
	if _, err := handler.Get(created.ID); err == nil {
		t.Errorf("Scheduling rule %s still exists after deletion", created.ID)
	}
	t.Logf("✓ Deleted scheduling rule: %s", created.ID)
}
