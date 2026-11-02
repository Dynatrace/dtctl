//go:build integration
// +build integration

package e2e

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/Dynatrace/dtctl/pkg/exec"
	"github.com/Dynatrace/dtctl/pkg/resources/workflow"
	"github.com/Dynatrace/dtctl/test/integration"
)

func TestWorkflowLifecycle(t *testing.T) {
	env := integration.SetupIntegration(t)
	defer env.Cleanup.Cleanup(t)

	handler := workflow.NewHandler(env.Client)
	execHandler := exec.NewWorkflowExecutor(env.Client)
	executionHandler := workflow.NewExecutionHandler(env.Client)

	tests := []struct {
		name    string
		wantErr bool
	}{
		{
			name:    "complete workflow lifecycle",
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Step 1: Create workflow
			t.Log("Step 1: Creating workflow...")
			createData := integration.WorkflowFixture(env.TestPrefix)
			created, err := handler.Create(createData)
			if err != nil {
				t.Fatalf("Failed to create workflow: %v", err)
			}
			if created.ID == "" {
				t.Fatal("Created workflow has no ID")
			}
			t.Logf("✓ Created workflow: %s (ID: %s)", created.Title, created.ID)

			// Track for cleanup
			env.Cleanup.Track("workflow", created.ID, created.Title)

			// Step 2: Get workflow
			t.Log("Step 2: Getting workflow...")
			retrieved, err := handler.Get(created.ID)
			if err != nil {
				t.Fatalf("Failed to get workflow: %v", err)
			}
			if retrieved.ID != created.ID {
				t.Errorf("Retrieved workflow ID mismatch: got %s, want %s", retrieved.ID, created.ID)
			}
			if retrieved.Title != created.Title {
				t.Errorf("Retrieved workflow title mismatch: got %s, want %s", retrieved.Title, created.Title)
			}
			t.Logf("✓ Retrieved workflow: %s", retrieved.Title)

			// Step 3: List workflows (verify our workflow appears)
			t.Log("Step 3: Listing workflows...")
			list, err := handler.List(workflow.WorkflowFilters{}, 500, 0)
			if err != nil {
				t.Fatalf("Failed to list workflows: %v", err)
			}
			found := false
			for _, wf := range list.Results {
				if wf.ID == created.ID {
					found = true
					break
				}
			}
			if !found {
				t.Error("Created workflow not found in list")
			} else {
				t.Logf("✓ Found workflow in list (total: %d workflows)", list.Count)
			}

			// Step 4: Update workflow
			t.Log("Step 4: Updating workflow...")
			updateData := integration.WorkflowFixtureModified(env.TestPrefix)
			updated, err := handler.Update(created.ID, updateData)
			if err != nil {
				t.Fatalf("Failed to update workflow: %v", err)
			}
			if updated.Title == created.Title {
				t.Error("Workflow title should have changed after update")
			}
			t.Logf("✓ Updated workflow: %s → %s", created.Title, updated.Title)

			// Step 5: List history (should have at least 1 version)
			t.Log("Step 5: Checking version history...")
			history, err := handler.ListHistory(created.ID)
			if err != nil {
				t.Fatalf("Failed to list history: %v", err)
			}
			if len(history.Results) < 1 {
				t.Errorf("Expected at least 1 history record, got %d", len(history.Results))
			} else {
				t.Logf("✓ Version history contains %d version(s)", len(history.Results))
			}

			// Step 6: Get specific history record
			if len(history.Results) >= 2 {
				t.Log("Step 6: Getting specific history version...")
				firstVersion := history.Results[0].Version
				historyRecord, err := handler.GetHistoryRecord(created.ID, firstVersion)
				if err != nil {
					t.Fatalf("Failed to get history record: %v", err)
				}
				if historyRecord.Title != created.Title {
					t.Logf("Note: History record title (%s) differs from original (%s)", historyRecord.Title, created.Title)
				}
				t.Logf("✓ Retrieved history version %d", firstVersion)
			}

			// Step 7: Execute workflow
			t.Log("Step 7: Executing workflow...")
			input := integration.WorkflowExecutionInput()
			execution, err := execHandler.Execute(created.ID, exec.WorkflowExecutionRequest{
				Input: input,
			})
			if err != nil {
				t.Fatalf("Failed to execute workflow: %v", err)
			}
			if execution.ID == "" {
				t.Fatal("Execution has no ID")
			}
			t.Logf("✓ Started execution: %s (state: %s)", execution.ID, execution.State)

			// Step 8: Wait for execution completion (with timeout)
			t.Log("Step 8: Waiting for execution to complete...")
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
			defer cancel()

			opts := exec.WaitOptions{
				PollInterval: 2 * time.Second,
				Timeout:      2 * time.Minute,
			}

			finalStatus, err := execHandler.WaitForCompletion(ctx, execution.ID, opts)
			if err != nil {
				t.Logf("Warning: Execution wait failed or timed out: %v", err)
				// Don't fail the test - execution might take longer or fail for other reasons
			} else {
				t.Logf("✓ Execution completed with state: %s", finalStatus.State)
				if finalStatus.State == "ERROR" && finalStatus.StateInfo != nil {
					t.Logf("  Execution error info: %s", *finalStatus.StateInfo)
				}
			}

			// Step 8b: Fetch the execution and verify persisted workflow input.
			t.Log("Step 8b: Verifying execution input...")
			executionDetails, err := executionHandler.Get(execution.ID)
			if err != nil {
				t.Fatalf("Failed to get execution details: %v", err)
			}
			gotInput, err := json.Marshal(executionDetails.Input)
			if err != nil {
				t.Fatalf("Failed to marshal execution input: %v", err)
			}
			wantInput, err := json.Marshal(input)
			if err != nil {
				t.Fatalf("Failed to marshal expected input: %v", err)
			}
			if string(gotInput) != string(wantInput) {
				t.Fatalf("execution input mismatch: got %s want %s", gotInput, wantInput)
			}

			// Step 9: Restore to previous version
			if len(history.Results) >= 2 {
				t.Log("Step 9: Restoring to previous version...")
				firstVersion := history.Results[0].Version
				restored, err := handler.RestoreHistory(created.ID, firstVersion)
				if err != nil {
					t.Fatalf("Failed to restore history: %v", err)
				}
				// After restore, title should revert
				t.Logf("✓ Restored to version %d (title: %s)", firstVersion, restored.Title)
			}

			// Step 10: Delete workflow
			t.Log("Step 10: Deleting workflow...")
			err = handler.Delete(created.ID)
			if err != nil {
				t.Fatalf("Failed to delete workflow: %v", err)
			}
			t.Logf("✓ Deleted workflow: %s", created.ID)

			// Step 11: Verify deletion (should get error/404)
			t.Log("Step 11: Verifying deletion...")
			_, err = handler.Get(created.ID)
			if err == nil {
				t.Error("Expected error when getting deleted workflow, got nil")
			} else {
				t.Logf("✓ Verified deletion (got expected error: %v)", err)
			}
		})
	}
}

func TestWorkflowCreateInvalid(t *testing.T) {
	env := integration.SetupIntegration(t)
	defer env.Cleanup.Cleanup(t)

	handler := workflow.NewHandler(env.Client)

	tests := []struct {
		name            string
		data            []byte
		wantErrContains string
	}{
		{
			name:            "invalid json",
			data:            []byte(`{"invalid": json`),
			wantErrContains: "400",
		},
		{
			// API validates task position constraints and surfaces details.
			// y must be >= 1; the error should carry the field path.
			name:            "negative task y position",
			data:            []byte(`{"title":"dtctl-test-invalid-position","tasks":{"t":{"action":"dynatrace.automations:run-javascript","position":{"x":0,"y":-1},"input":{"script":"export default async function(){return{}}"}}}}`),
			wantErrContains: "position",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := handler.Create(tt.data)
			if err == nil {
				t.Fatalf("Create() expected error, got nil")
			}
			if !strings.Contains(err.Error(), tt.wantErrContains) {
				t.Errorf("Create() error = %v, want containing %q", err, tt.wantErrContains)
			}
			t.Logf("✓ Got expected error: %v", err)
		})
	}
}

func TestWorkflowUpdate(t *testing.T) {
	env := integration.SetupIntegration(t)
	defer env.Cleanup.Cleanup(t)

	handler := workflow.NewHandler(env.Client)

	// Create a workflow first
	createData := integration.WorkflowFixture(env.TestPrefix)
	created, err := handler.Create(createData)
	if err != nil {
		t.Fatalf("Failed to create workflow: %v", err)
	}
	env.Cleanup.Track("workflow", created.ID, created.Title)

	tests := []struct {
		name    string
		update  func() []byte
		wantErr bool
	}{
		{
			name: "valid update with new task",
			update: func() []byte {
				return integration.WorkflowFixtureModified(env.TestPrefix)
			},
			wantErr: false,
		},
		{
			name: "update with description change",
			update: func() []byte {
				wf := map[string]interface{}{
					"title":       created.Title,
					"description": "Updated description for integration test",
					"tasks": map[string]interface{}{
						"test_task": map[string]interface{}{
							"name":   "test_task",
							"action": "dynatrace.automations:run-javascript",
							"input": map[string]interface{}{
								"script": "export default async function() { return { updated: true }; }",
							},
						},
					},
				}
				data, _ := json.Marshal(wf)
				return data
			},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			updateData := tt.update()
			updated, err := handler.Update(created.ID, updateData)
			if (err != nil) != tt.wantErr {
				t.Errorf("Update() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if !tt.wantErr && updated == nil {
				t.Error("Update() returned nil workflow")
			}
			if !tt.wantErr {
				t.Logf("✓ Updated workflow successfully")
			}
		})
	}
}
