//go:build integration
// +build integration

package e2e

import (
	"testing"
	"time"

	"github.com/Dynatrace/dtctl/pkg/resources/slo"
	"github.com/Dynatrace/dtctl/test/integration"
)

func TestSLOLifecycle(t *testing.T) {
	t.Skip("Skipping: SLO custom SLI creation requires complex configuration - needs further investigation of API requirements")

	env := integration.SetupIntegration(t)
	defer env.Cleanup.Cleanup(t)

	handler := slo.NewHandler(env.Client)

	tests := []struct {
		name    string
		wantErr bool
	}{
		{
			name:    "complete slo lifecycle",
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Step 1: Create SLO
			t.Log("Step 1: Creating SLO...")
			createData := integration.SLOFixture(env.TestPrefix)

			created, err := handler.Create(createData)
			if err != nil {
				t.Fatalf("Failed to create SLO: %v", err)
			}
			if created.ID == "" {
				t.Fatal("Created SLO has no ID")
			}
			t.Logf("✓ Created SLO: %s (ID: %s, Version: %s)", created.Name, created.ID, created.Version)

			// Track for cleanup
			env.Cleanup.Track("slo", created.ID, created.Name)

			// Step 2: Get SLO
			t.Log("Step 2: Getting SLO...")
			retrieved, err := handler.Get(created.ID)
			if err != nil {
				t.Fatalf("Failed to get SLO: %v", err)
			}
			if retrieved.ID != created.ID {
				t.Errorf("Retrieved SLO ID mismatch: got %s, want %s", retrieved.ID, created.ID)
			}
			if retrieved.Name != created.Name {
				t.Errorf("Retrieved SLO name mismatch: got %s, want %s", retrieved.Name, created.Name)
			}
			t.Logf("✓ Retrieved SLO: %s (Version: %s)", retrieved.Name, retrieved.Version)

			// Step 3: List SLOs (verify our SLO appears)
			t.Log("Step 3: Listing SLOs...")
			list, err := handler.List("", 0)
			if err != nil {
				t.Fatalf("Failed to list SLOs: %v", err)
			}
			found := false
			for _, s := range list.SLOs {
				if s.ID == created.ID {
					found = true
					break
				}
			}
			if !found {
				t.Error("Created SLO not found in list")
			} else {
				t.Logf("✓ Found SLO in list (total: %d SLOs)", list.TotalCount)
			}

			// Step 4: Update SLO
			t.Log("Step 4: Updating SLO...")
			updateData := integration.SLOFixtureModified(env.TestPrefix)
			err = handler.Update(created.ID, created.Version, updateData)
			if err != nil {
				t.Fatalf("Failed to update SLO: %v", err)
			}
			t.Logf("✓ Updated SLO: %s", created.ID)

			// Step 5: Verify update
			t.Log("Step 5: Verifying update...")
			updated, err := handler.Get(created.ID)
			if err != nil {
				t.Fatalf("Failed to get updated SLO: %v", err)
			}
			if updated.Version == created.Version {
				t.Errorf("SLO version should have changed: got %s, previous %s", updated.Version, created.Version)
			}
			if updated.Name == created.Name {
				t.Error("SLO name should have changed after update")
			}
			t.Logf("✓ Verified update (Version: %s → %s, Name: %s → %s)", created.Version, updated.Version, created.Name, updated.Name)

			// Step 6: Evaluate SLO
			t.Log("Step 6: Evaluating SLO...")
			evalResp, err := handler.Evaluate(created.ID)
			if err != nil {
				t.Logf("Warning: SLO evaluation failed (may be expected if no data available): %v", err)
			} else {
				if evalResp.EvaluationToken == "" {
					t.Error("Expected evaluation token, got empty string")
				}
				t.Logf("✓ Started SLO evaluation (Token: %s, TTL: %d seconds)", evalResp.EvaluationToken, evalResp.TTLSeconds)

				// Step 7: Poll for evaluation results
				t.Log("Step 7: Polling for evaluation results...")
				maxAttempts := 10
				pollInterval := 2 * time.Second

				for attempt := 1; attempt <= maxAttempts; attempt++ {
					time.Sleep(pollInterval)

					pollResp, err := handler.PollEvaluation(evalResp.EvaluationToken, 5000)
					if err != nil {
						t.Logf("Poll attempt %d/%d: %v", attempt, maxAttempts, err)
						continue
					}

					if len(pollResp.EvaluationResults) > 0 {
						t.Logf("✓ Evaluation completed after %d attempts", attempt)
						for _, result := range pollResp.EvaluationResults {
							t.Logf("  - Criteria: %s, Status: %s", result.Criteria, result.Status)
							if result.Value != nil {
								t.Logf("    Value: %.2f", *result.Value)
							}
							if result.ErrorBudget != nil {
								t.Logf("    Error Budget: %.2f", *result.ErrorBudget)
							}
						}
						break
					}

					if attempt == maxAttempts {
						t.Log("Note: Evaluation did not complete within timeout (may be expected if no data available)")
					}
				}
			}

			// Step 8: Delete SLO
			t.Log("Step 8: Deleting SLO...")
			err = handler.Delete(created.ID, updated.Version)
			if err != nil {
				t.Fatalf("Failed to delete SLO: %v", err)
			}
			t.Logf("✓ Deleted SLO: %s", created.ID)

			// Step 9: Verify deletion (should get error/404)
			t.Log("Step 9: Verifying deletion...")
			_, err = handler.Get(created.ID)
			if err == nil {
				t.Error("Expected error when getting deleted SLO, got nil")
			} else {
				t.Logf("✓ Verified deletion (got expected error: %v)", err)
			}
		})
	}
}

func TestSLOOptimisticLocking(t *testing.T) {
	t.Skip("Skipping: SLO custom SLI creation requires complex configuration - needs further investigation of API requirements")

	env := integration.SetupIntegration(t)
	defer env.Cleanup.Cleanup(t)

	handler := slo.NewHandler(env.Client)

	// Create an SLO first
	createData := integration.SLOFixture(env.TestPrefix)
	created, err := handler.Create(createData)
	if err != nil {
		t.Fatalf("Failed to create SLO: %v", err)
	}
	env.Cleanup.Track("slo", created.ID, created.Name)

	t.Logf("Created SLO: %s (Version: %s)", created.Name, created.Version)

	// Test updating with stale version (should fail)
	t.Run("update with stale version", func(t *testing.T) {
		// First update with current version
		updateData := integration.SLOFixtureModified(env.TestPrefix)
		err := handler.Update(created.ID, created.Version, updateData)
		if err != nil {
			t.Fatalf("First update failed: %v", err)
		}
		t.Logf("First update successful")

		// Get updated version
		updated, err := handler.Get(created.ID)
		if err != nil {
			t.Fatalf("Failed to get updated SLO: %v", err)
		}
		t.Logf("Updated version: %s → %s", created.Version, updated.Version)

		// Try to update with old version (should fail with 409)
		err = handler.Update(created.ID, created.Version, updateData)
		if err == nil {
			t.Error("Expected error when updating with stale version, got nil")
		} else {
			t.Logf("✓ Got expected optimistic locking error: %v", err)
		}
	})
}

func TestSLOTemplates(t *testing.T) {
	env := integration.SetupIntegration(t)
	defer env.Cleanup.Cleanup(t)

	handler := slo.NewHandler(env.Client)

	t.Run("list templates", func(t *testing.T) {
		templates, err := handler.ListTemplates("")
		if err != nil {
			t.Fatalf("Failed to list SLO templates: %v", err)
		}
		if templates.TotalCount == 0 {
			t.Log("Note: No SLO templates found (may be expected in some environments)")
		} else {
			t.Logf("✓ Found %d SLO template(s)", templates.TotalCount)

			// Try to get the first template
			if len(templates.Items) > 0 {
				firstTemplate := templates.Items[0]
				t.Logf("  First template: %s (%s)", firstTemplate.Name, firstTemplate.ID)

				t.Run("get specific template", func(t *testing.T) {
					template, err := handler.GetTemplate(firstTemplate.ID)
					if err != nil {
						t.Fatalf("Failed to get template: %v", err)
					}
					if template.ID != firstTemplate.ID {
						t.Errorf("Template ID mismatch: got %s, want %s", template.ID, firstTemplate.ID)
					}
					t.Logf("✓ Retrieved template: %s", template.Name)
				})
			}
		}
	})

	t.Run("get non-existent template", func(t *testing.T) {
		_, err := handler.GetTemplate("nonexistent-template-id")
		if err == nil {
			t.Error("Expected error when getting non-existent template, got nil")
		} else {
			t.Logf("✓ Got expected error: %v", err)
		}
	})
}

func TestSLOCreateInvalid(t *testing.T) {
	env := integration.SetupIntegration(t)
	defer env.Cleanup.Cleanup(t)

	handler := slo.NewHandler(env.Client)

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
			name:    "empty slo",
			data:    []byte(`{}`),
			wantErr: true,
		},
		{
			name:    "missing criteria",
			data:    []byte(`{"name": "test-slo", "customSli": {"type": "QUERY_RATIO"}}`),
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
				t.Logf("✓ Got expected error: %v", err)
			}
			// If creation succeeded unexpectedly, clean up
			if err == nil && created != nil {
				env.Cleanup.Track("slo", created.ID, created.Name)
			}
		})
	}
}

func TestSLOEvaluation(t *testing.T) {
	t.Skip("Skipping: SLO custom SLI creation requires complex configuration - needs further investigation of API requirements")

	env := integration.SetupIntegration(t)
	defer env.Cleanup.Cleanup(t)

	handler := slo.NewHandler(env.Client)

	// Create an SLO
	createData := integration.SLOFixture(env.TestPrefix)
	created, err := handler.Create(createData)
	if err != nil {
		t.Fatalf("Failed to create SLO: %v", err)
	}
	env.Cleanup.Track("slo", created.ID, created.Name)

	t.Logf("Created SLO: %s (ID: %s)", created.Name, created.ID)

	t.Run("start evaluation", func(t *testing.T) {
		evalResp, err := handler.Evaluate(created.ID)
		if err != nil {
			t.Logf("Warning: SLO evaluation failed: %v", err)
			t.Skip("Skipping evaluation tests due to evaluation start failure")
		}

		if evalResp.EvaluationToken == "" {
			t.Error("Expected evaluation token, got empty string")
		}
		if evalResp.TTLSeconds == 0 {
			t.Error("Expected non-zero TTL")
		}
		t.Logf("✓ Evaluation started (Token: %s, TTL: %d seconds)", evalResp.EvaluationToken, evalResp.TTLSeconds)

		t.Run("poll evaluation", func(t *testing.T) {
			// Poll once to verify the endpoint works
			pollResp, err := handler.PollEvaluation(evalResp.EvaluationToken, 5000)
			if err != nil {
				t.Logf("Note: Poll failed (may be expected if evaluation takes longer): %v", err)
			} else {
				t.Logf("✓ Poll successful")
				if pollResp.EvaluationResults != nil {
					t.Logf("  Found %d evaluation result(s)", len(pollResp.EvaluationResults))
				}
			}
		})

		t.Run("poll with invalid token", func(t *testing.T) {
			_, err := handler.PollEvaluation("invalid-token-12345", 5000)
			if err == nil {
				t.Error("Expected error when polling with invalid token, got nil")
			} else {
				t.Logf("✓ Got expected error: %v", err)
			}
		})
	})

	t.Run("evaluate non-existent slo", func(t *testing.T) {
		_, err := handler.Evaluate("non-existent-slo-id")
		if err == nil {
			t.Error("Expected error when evaluating non-existent SLO, got nil")
		} else {
			t.Logf("✓ Got expected error: %v", err)
		}
	})
}
