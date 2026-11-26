//go:build integration
// +build integration

package e2e

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/Dynatrace/dtctl/pkg/apply"
	"github.com/Dynatrace/dtctl/pkg/resources/anomalydetector"
	"github.com/Dynatrace/dtctl/test/integration"
)

func TestAnomalyDetectorLifecycle(t *testing.T) {
	env := integration.SetupIntegration(t)
	defer env.Cleanup.Cleanup(t)

	handler := anomalydetector.NewHandler(env.Client)

	t.Run("complete anomaly detector lifecycle", func(t *testing.T) {
		// Step 1: Create anomaly detector (flattened format)
		t.Log("Step 1: Creating anomaly detector...")
		createData := integration.AnomalyDetectorFixture(env.TestPrefix)

		created, err := handler.Create(createData)
		if err != nil {
			t.Fatalf("Failed to create anomaly detector: %v", err)
		}
		if created.ObjectID == "" {
			t.Fatal("Created anomaly detector has no ObjectID")
		}
		t.Logf("Created anomaly detector: %s (ObjectID: %s)", created.Title, created.ObjectID)

		// Track for cleanup
		env.Cleanup.Track("anomalydetector", created.ObjectID, created.Title)

		// Step 2: Get anomaly detector
		t.Log("Step 2: Getting anomaly detector...")
		retrieved, err := handler.Get(created.ObjectID)
		if err != nil {
			t.Fatalf("Failed to get anomaly detector: %v", err)
		}
		if retrieved.ObjectID != created.ObjectID {
			t.Errorf("Retrieved ObjectID mismatch: got %s, want %s", retrieved.ObjectID, created.ObjectID)
		}
		expectedTitle := env.TestPrefix + "-anomaly-detector"
		if retrieved.Title != expectedTitle {
			t.Errorf("Retrieved title mismatch: got %s, want %s", retrieved.Title, expectedTitle)
		}
		if !retrieved.Enabled {
			t.Error("Retrieved anomaly detector should be enabled")
		}
		if retrieved.Source != "dtctl" {
			t.Errorf("Retrieved source mismatch: got %s, want dtctl", retrieved.Source)
		}
		t.Logf("Retrieved anomaly detector: %s (enabled: %v, analyzer: %s, eventType: %s)",
			retrieved.Title, retrieved.Enabled, retrieved.AnalyzerShort, retrieved.EventType)

		// Step 3: List anomaly detectors (verify our detector appears)
		t.Log("Step 3: Listing anomaly detectors...")
		detectors, err := handler.List(anomalydetector.ListOptions{})
		if err != nil {
			t.Fatalf("Failed to list anomaly detectors: %v", err)
		}
		found := false
		for _, d := range detectors {
			if d.ObjectID == created.ObjectID {
				found = true
				break
			}
		}
		if !found {
			t.Error("Created anomaly detector not found in list")
		} else {
			t.Logf("Found anomaly detector in list (total: %d detectors)", len(detectors))
		}

		// Step 4: List with enabled filter
		t.Log("Step 4: Listing with enabled filter...")
		enabledTrue := true
		enabledDetectors, err := handler.List(anomalydetector.ListOptions{Enabled: &enabledTrue})
		if err != nil {
			t.Fatalf("Failed to list enabled anomaly detectors: %v", err)
		}
		foundEnabled := false
		for _, d := range enabledDetectors {
			if d.ObjectID == created.ObjectID {
				foundEnabled = true
			}
			if !d.Enabled {
				t.Errorf("Enabled filter returned disabled detector: %s", d.Title)
			}
		}
		if !foundEnabled {
			t.Error("Created (enabled) detector not found in enabled-only list")
		}
		t.Logf("Enabled filter returned %d detectors", len(enabledDetectors))

		// Step 5: FindByName
		t.Log("Step 5: Finding by name...")
		foundByName, err := handler.FindByName(expectedTitle)
		if err != nil {
			t.Fatalf("Failed to find anomaly detector by name: %v", err)
		}
		if foundByName.ObjectID != created.ObjectID {
			t.Errorf("FindByName ObjectID mismatch: got %s, want %s", foundByName.ObjectID, created.ObjectID)
		}
		t.Logf("Found by name: %s (ObjectID: %s)", foundByName.Title, foundByName.ObjectID)

		// Step 6: GetRaw (for edit command flow)
		t.Log("Step 6: Getting raw anomaly detector...")
		raw, err := handler.GetRaw(created.ObjectID)
		if err != nil {
			t.Fatalf("Failed to get raw anomaly detector: %v", err)
		}
		if len(raw) == 0 {
			t.Error("Raw anomaly detector is empty")
		}
		// Verify raw is valid JSON
		var rawCheck map[string]interface{}
		if err := json.Unmarshal(raw, &rawCheck); err != nil {
			t.Errorf("Raw anomaly detector is not valid JSON: %v", err)
		}
		// Verify raw contains expected fields
		if _, ok := rawCheck["title"]; !ok {
			t.Error("Raw anomaly detector missing 'title' field")
		}
		if _, ok := rawCheck["analyzer"]; !ok {
			t.Error("Raw anomaly detector missing 'analyzer' field")
		}
		t.Logf("Got raw anomaly detector (%d bytes)", len(raw))

		// Step 7: Update anomaly detector
		t.Log("Step 7: Updating anomaly detector...")
		updateData := integration.AnomalyDetectorFixtureModified(env.TestPrefix)
		updated, err := handler.Update(created.ObjectID, updateData)
		if err != nil {
			t.Fatalf("Failed to update anomaly detector: %v", err)
		}
		expectedModifiedTitle := env.TestPrefix + "-anomaly-detector-modified"
		if updated.Title != expectedModifiedTitle {
			t.Errorf("Updated title mismatch: got %s, want %s", updated.Title, expectedModifiedTitle)
		}
		if updated.Enabled {
			t.Error("Updated anomaly detector should be disabled")
		}
		t.Logf("Updated anomaly detector: %s (enabled: %v)", updated.Title, updated.Enabled)

		// Step 8: Verify update via GET
		t.Log("Step 8: Verifying update...")
		verified, err := handler.Get(created.ObjectID)
		if err != nil {
			t.Fatalf("Failed to get updated anomaly detector: %v", err)
		}
		if verified.Title != expectedModifiedTitle {
			t.Errorf("Verified title mismatch: got %s, want %s", verified.Title, expectedModifiedTitle)
		}
		if verified.Enabled {
			t.Error("Verified anomaly detector should be disabled after update")
		}
		t.Logf("Verified update: %s (enabled: %v)", verified.Title, verified.Enabled)

		// Step 9: List with disabled filter (should find our updated detector)
		t.Log("Step 9: Listing with disabled filter...")
		enabledFalse := false
		disabledDetectors, err := handler.List(anomalydetector.ListOptions{Enabled: &enabledFalse})
		if err != nil {
			t.Fatalf("Failed to list disabled anomaly detectors: %v", err)
		}
		foundDisabled := false
		for _, d := range disabledDetectors {
			if d.ObjectID == created.ObjectID {
				foundDisabled = true
			}
			if d.Enabled {
				t.Errorf("Disabled filter returned enabled detector: %s", d.Title)
			}
		}
		if !foundDisabled {
			t.Error("Updated (disabled) detector not found in disabled-only list")
		}
		t.Logf("Disabled filter returned %d detectors", len(disabledDetectors))

		// Step 10: Delete anomaly detector
		t.Log("Step 10: Deleting anomaly detector...")
		err = handler.Delete(created.ObjectID)
		if err != nil {
			t.Fatalf("Failed to delete anomaly detector: %v", err)
		}
		t.Logf("Deleted anomaly detector: %s", created.ObjectID)

		// Untrack from cleanup since we manually deleted
		env.Cleanup.Untrack("anomalydetector", created.ObjectID)

		// Step 11: Verify deletion (should get error/404)
		t.Log("Step 11: Verifying deletion...")
		_, err = handler.Get(created.ObjectID)
		if err == nil {
			t.Error("Expected error when getting deleted anomaly detector, got nil")
		} else {
			t.Logf("Verified deletion (got expected error: %v)", err)
		}
	})
}

func TestAnomalyDetectorCreateInvalid(t *testing.T) {
	env := integration.SetupIntegration(t)
	defer env.Cleanup.Cleanup(t)

	handler := anomalydetector.NewHandler(env.Client)

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
			name:    "empty object",
			data:    []byte(`{}`),
			wantErr: true,
		},
		{
			name:    "missing analyzer",
			data:    []byte(`{"title": "test-no-analyzer", "eventTemplate": {"event.type": "CUSTOM_ALERT"}}`),
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
				env.Cleanup.Track("anomalydetector", created.ObjectID, created.Title)
			}
		})
	}
}

func TestAnomalyDetectorGetNonExistent(t *testing.T) {
	env := integration.SetupIntegration(t)
	defer env.Cleanup.Cleanup(t)

	handler := anomalydetector.NewHandler(env.Client)

	_, err := handler.Get("non-existent-object-id-12345")
	if err == nil {
		t.Error("Expected error when getting non-existent anomaly detector, got nil")
	} else {
		t.Logf("Got expected error: %v", err)
	}
}

func TestAnomalyDetectorDeleteNonExistent(t *testing.T) {
	env := integration.SetupIntegration(t)
	defer env.Cleanup.Cleanup(t)

	handler := anomalydetector.NewHandler(env.Client)

	err := handler.Delete("non-existent-object-id-12345")
	if err == nil {
		t.Error("Expected error when deleting non-existent anomaly detector, got nil")
	} else {
		t.Logf("Got expected error: %v", err)
	}
}

func TestAnomalyDetectorFindByNameNotFound(t *testing.T) {
	env := integration.SetupIntegration(t)
	defer env.Cleanup.Cleanup(t)

	handler := anomalydetector.NewHandler(env.Client)

	_, err := handler.FindByName("non-existent-anomaly-detector-name-12345")
	if err == nil {
		t.Error("Expected error when finding non-existent anomaly detector by name, got nil")
	} else {
		if !strings.Contains(err.Error(), "not found") {
			t.Errorf("Expected 'not found' error, got: %v", err)
		}
		t.Logf("Got expected error: %v", err)
	}
}

func TestAnomalyDetectorRawSettingsFormat(t *testing.T) {
	env := integration.SetupIntegration(t)
	defer env.Cleanup.Cleanup(t)

	handler := anomalydetector.NewHandler(env.Client)

	// Create using raw Settings API format
	t.Log("Creating anomaly detector using raw Settings API format...")
	rawData := map[string]interface{}{
		"schemaId": "builtin:davis.anomaly-detectors",
		"scope":    "environment",
		"value": map[string]interface{}{
			"title":       env.TestPrefix + "-raw-format-detector",
			"description": "Integration test - raw Settings format",
			"enabled":     true,
			"source":      "dtctl",
			"analyzer": map[string]interface{}{
				"name": "dt.statistics.ui.anomaly_detection.StaticThresholdAnomalyDetectionAnalyzer",
				"input": []map[string]interface{}{
					{"key": "query", "value": "timeseries avg_cpu=avg(dt.host.cpu.usage), interval:5m"},
					{"key": "threshold", "value": "99"},
					{"key": "alertCondition", "value": "ABOVE"},
					{"key": "violatingSamples", "value": "3"},
					{"key": "slidingWindow", "value": "5"},
					{"key": "dealertingSamples", "value": "5"},
					{"key": "alertOnMissingData", "value": "false"},
				},
			},
			"eventTemplate": map[string]interface{}{
				"properties": []map[string]interface{}{
					{"key": "event.type", "value": "CUSTOM_ALERT"},
					{"key": "event.name", "value": env.TestPrefix + " raw format test alert"},
				},
			},
			"executionSettings": map[string]interface{}{
				"queryOffset": 7,
			},
		},
	}
	data, _ := json.Marshal(rawData)

	created, err := handler.Create(data)
	if err != nil {
		t.Fatalf("Failed to create anomaly detector with raw format: %v", err)
	}
	env.Cleanup.Track("anomalydetector", created.ObjectID, created.Title)

	expectedTitle := env.TestPrefix + "-raw-format-detector"
	if created.Title != expectedTitle {
		t.Errorf("Title mismatch: got %s, want %s", created.Title, expectedTitle)
	}
	t.Logf("Created anomaly detector with raw format: %s (ObjectID: %s)", created.Title, created.ObjectID)

	// Verify it can be retrieved
	retrieved, err := handler.Get(created.ObjectID)
	if err != nil {
		t.Fatalf("Failed to get anomaly detector: %v", err)
	}
	if retrieved.Title != expectedTitle {
		t.Errorf("Retrieved title mismatch: got %s, want %s", retrieved.Title, expectedTitle)
	}
	t.Logf("Verified raw format detector: %s (analyzer: %s)", retrieved.Title, retrieved.AnalyzerShort)
}

func TestAnomalyDetectorFindByExactTitle(t *testing.T) {
	env := integration.SetupIntegration(t)
	defer env.Cleanup.Cleanup(t)

	handler := anomalydetector.NewHandler(env.Client)

	// Create a detector
	createData := integration.AnomalyDetectorFixture(env.TestPrefix)
	created, err := handler.Create(createData)
	if err != nil {
		t.Fatalf("Failed to create anomaly detector: %v", err)
	}
	env.Cleanup.Track("anomalydetector", created.ObjectID, created.Title)

	// FindByExactTitle with exact match should find it
	found, err := handler.FindByExactTitle(created.Title)
	if err != nil {
		t.Fatalf("FindByExactTitle() error: %v", err)
	}
	if found == nil {
		t.Fatal("FindByExactTitle() returned nil for existing detector")
	}
	if found.ObjectID != created.ObjectID {
		t.Errorf("FindByExactTitle() ObjectID mismatch: got %s, want %s", found.ObjectID, created.ObjectID)
	}

	// FindByExactTitle with case-insensitive match should also find it
	found2, err := handler.FindByExactTitle(strings.ToUpper(created.Title))
	if err != nil {
		t.Fatalf("FindByExactTitle() case-insensitive error: %v", err)
	}
	if found2 == nil {
		t.Fatal("FindByExactTitle() returned nil for case-insensitive match")
	}

	// FindByExactTitle with non-existent title should return nil, nil
	notFound, err := handler.FindByExactTitle("non-existent-title-12345")
	if err != nil {
		t.Fatalf("FindByExactTitle() should not error for non-existent title, got: %v", err)
	}
	if notFound != nil {
		t.Errorf("FindByExactTitle() should return nil for non-existent title, got: %v", notFound)
	}

	// FindByExactTitle with prefix-only should NOT match (unlike FindByName)
	prefix := created.Title[:len(created.Title)-3] // Remove last 3 chars
	prefixMatch, err := handler.FindByExactTitle(prefix)
	if err != nil {
		t.Fatalf("FindByExactTitle() prefix error: %v", err)
	}
	if prefixMatch != nil {
		t.Errorf("FindByExactTitle() should not match prefix-only, but got: %s", prefixMatch.Title)
	}

	t.Logf("FindByExactTitle tests passed for: %s", created.Title)
}

func TestAnomalyDetectorApplyIdempotent(t *testing.T) {
	env := integration.SetupIntegration(t)
	defer env.Cleanup.Cleanup(t)

	handler := anomalydetector.NewHandler(env.Client)
	applier := apply.NewApplier(env.Client)

	// Create fixture data (flattened format, no objectId)
	createData := integration.AnomalyDetectorFixture(env.TestPrefix)
	expectedTitle := env.TestPrefix + "-anomaly-detector"

	// First apply — should create
	t.Log("First apply: should create...")
	results1, err := applier.Apply(createData, apply.ApplyOptions{})
	if err != nil {
		t.Fatalf("First apply failed: %v", err)
	}
	if len(results1) != 1 {
		t.Fatalf("First apply returned %d results, want 1", len(results1))
	}
	r1, ok := results1[0].(*apply.AnomalyDetectorApplyResult)
	if !ok {
		t.Fatalf("First apply result is not *AnomalyDetectorApplyResult")
	}
	if r1.Action != apply.ActionCreated {
		t.Errorf("First apply action = %q, want %q", r1.Action, apply.ActionCreated)
	}
	firstID := r1.ID
	if firstID == "" {
		t.Fatal("First apply returned empty ID")
	}
	env.Cleanup.Track("anomalydetector", firstID, expectedTitle)
	t.Logf("First apply created: %s (ID: %s)", r1.Name, firstID)

	// Second apply with same data — should update, NOT create a duplicate
	t.Log("Second apply: should update existing...")
	results2, err := applier.Apply(createData, apply.ApplyOptions{})
	if err != nil {
		t.Fatalf("Second apply failed: %v", err)
	}
	if len(results2) != 1 {
		t.Fatalf("Second apply returned %d results, want 1", len(results2))
	}
	r2, ok := results2[0].(*apply.AnomalyDetectorApplyResult)
	if !ok {
		t.Fatalf("Second apply result is not *AnomalyDetectorApplyResult")
	}
	if r2.Action != apply.ActionUpdated {
		t.Errorf("Second apply action = %q, want %q", r2.Action, apply.ActionUpdated)
	}
	secondID := r2.ID
	if secondID != firstID {
		t.Errorf("Second apply created a NEW detector (ID: %s) instead of updating existing (ID: %s)", secondID, firstID)
		// Track the duplicate for cleanup
		env.Cleanup.Track("anomalydetector", secondID, expectedTitle)
	}
	t.Logf("Second apply updated: %s (ID: %s)", r2.Name, secondID)

	// Verify only one detector with this title exists
	detectors, err := handler.List(anomalydetector.ListOptions{})
	if err != nil {
		t.Fatalf("Failed to list detectors: %v", err)
	}
	count := 0
	for _, d := range detectors {
		if d.Title == expectedTitle {
			count++
		}
	}
	if count != 1 {
		t.Errorf("Expected exactly 1 detector with title %q, found %d", expectedTitle, count)
	}

	t.Logf("Apply idempotency verified: title %q appears exactly %d time(s)", expectedTitle, count)
}

// TestAnomalyDetectorMinimalDefinition covers issue #369: a definition that
// omits the fields the schema declares non-nullable but defaults (description,
// source, executionSettings) was rejected with "Must not be null" on a field
// the author never wrote. dtctl now fills those defaults in.
func TestAnomalyDetectorMinimalDefinition(t *testing.T) {
	env := integration.SetupIntegration(t)
	defer env.Cleanup.Cleanup(t)

	handler := anomalydetector.NewHandler(env.Client)

	title := env.TestPrefix + "-minimal-detector"
	minimal, err := json.Marshal(map[string]interface{}{
		"title":   title,
		"enabled": false,
		"analyzer": map[string]interface{}{
			"name": "dt.statistics.ui.anomaly_detection.StaticThresholdAnomalyDetectionAnalyzer",
			"input": map[string]interface{}{
				"query":              "timeseries avg_cpu=avg(dt.host.cpu.usage), interval:5m",
				"threshold":          "95",
				"alertCondition":     "ABOVE",
				"violatingSamples":   "3",
				"slidingWindow":      "5",
				"dealertingSamples":  "5",
				"alertOnMissingData": "false",
			},
		},
		"eventTemplate": map[string]interface{}{
			"event.type": "CUSTOM_ALERT",
			"event.name": title + " alert",
		},
	})
	if err != nil {
		t.Fatalf("failed to marshal minimal definition: %v", err)
	}

	created, err := handler.Create(minimal)
	if err != nil {
		t.Fatalf("Create() with a minimal definition failed: %v", err)
	}
	env.Cleanup.Track("anomalydetector", created.ObjectID, created.Title)
	t.Logf("Created detector from a minimal definition: %s (ObjectID: %s)", created.Title, created.ObjectID)

	retrieved, err := handler.Get(created.ObjectID)
	if err != nil {
		t.Fatalf("Failed to get anomaly detector: %v", err)
	}
	if retrieved.Description != "" {
		t.Errorf("Description = %q, want the schema default (empty)", retrieved.Description)
	}
	if retrieved.Source != "dtctl" {
		t.Errorf("Source = %q, want %q", retrieved.Source, "dtctl")
	}
	if _, ok := retrieved.Value["executionSettings"]; !ok {
		t.Errorf("executionSettings missing from the stored object: %v", retrieved.Value)
	}
}

// TestAnomalyDetectorValidateOnly checks the validation the --dry-run paths rely
// on: the environment's own verdict, with nothing persisted either way.
func TestAnomalyDetectorValidateOnly(t *testing.T) {
	env := integration.SetupIntegration(t)
	defer env.Cleanup.Cleanup(t)

	handler := anomalydetector.NewHandler(env.Client)

	t.Run("valid definition passes and persists nothing", func(t *testing.T) {
		data := integration.AnomalyDetectorFixture(env.TestPrefix)
		if err := handler.ValidateCreate(data); err != nil {
			t.Fatalf("ValidateCreate() error = %v, want the fixture to validate", err)
		}

		title := anomalydetector.ExtractTitle(data)
		existing, err := handler.FindByExactTitle(title)
		if err != nil {
			t.Fatalf("FindByExactTitle() error = %v", err)
		}
		if existing != nil {
			env.Cleanup.Track("anomalydetector", existing.ObjectID, existing.Title)
			t.Fatalf("validateOnly persisted %q (ObjectID: %s)", existing.Title, existing.ObjectID)
		}
	})

	t.Run("incomplete analyzer is rejected", func(t *testing.T) {
		// The analyzer name is valid but its required input fields are absent —
		// only the environment's analyzer validator knows that.
		data, err := json.Marshal(map[string]interface{}{
			"title":       env.TestPrefix + "-incomplete-analyzer",
			"description": "Integration test - incomplete analyzer",
			"enabled":     false,
			"analyzer": map[string]interface{}{
				"name": "dt.statistics.ui.anomaly_detection.StaticThresholdAnomalyDetectionAnalyzer",
			},
			"eventTemplate": map[string]interface{}{
				"event.type": "CUSTOM_ALERT",
				"event.name": env.TestPrefix + " incomplete analyzer alert",
			},
		})
		if err != nil {
			t.Fatalf("failed to marshal definition: %v", err)
		}

		err = handler.ValidateCreate(data)
		if err == nil {
			t.Fatal("ValidateCreate() error = nil, want the incomplete analyzer to be rejected")
		}
		var unavailable *anomalydetector.ValidationUnavailableError
		if errors.As(err, &unavailable) {
			t.Fatalf("ValidateCreate() error = %v, want a rejection rather than an unavailable verdict", err)
		}
		if !strings.Contains(err.Error(), "invalid anomaly detector") {
			t.Errorf("error = %v, want a field-oriented validation error", err)
		}
		t.Logf("Rejected as expected: %v", err)
	})

	t.Run("update of an existing detector validates", func(t *testing.T) {
		created, err := handler.Create(integration.AnomalyDetectorFixture(env.TestPrefix + "-upd"))
		if err != nil {
			t.Fatalf("Create() error = %v", err)
		}
		env.Cleanup.Track("anomalydetector", created.ObjectID, created.Title)

		modified := integration.AnomalyDetectorFixtureModified(env.TestPrefix + "-upd")
		if err := handler.ValidateUpdate(created.ObjectID, modified); err != nil {
			t.Fatalf("ValidateUpdate() error = %v, want the modified fixture to validate", err)
		}

		// Validation must not have applied the change.
		retrieved, err := handler.Get(created.ObjectID)
		if err != nil {
			t.Fatalf("Get() error = %v", err)
		}
		if retrieved.Title != created.Title {
			t.Errorf("Title = %q, want %q — validateOnly must not persist", retrieved.Title, created.Title)
		}
	})
}
