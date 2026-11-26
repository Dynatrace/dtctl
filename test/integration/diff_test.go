package integration

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/Dynatrace/dtctl/pkg/diff"
)

func TestDiff_TwoFiles(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test")
	}

	tmpDir := t.TempDir()

	file1 := filepath.Join(tmpDir, "file1.json")
	file2 := filepath.Join(tmpDir, "file2.json")

	data1 := map[string]interface{}{
		"id":    "test-1",
		"title": "Test Workflow",
		"tasks": []interface{}{
			map[string]interface{}{
				"name":   "task1",
				"action": "action1",
			},
		},
	}

	data2 := map[string]interface{}{
		"id":    "test-1",
		"title": "Test Workflow Updated",
		"tasks": []interface{}{
			map[string]interface{}{
				"name":   "task1",
				"action": "action2",
			},
		},
	}

	writeJSON(t, file1, data1)
	writeJSON(t, file2, data2)

	differ := diff.NewDiffer(diff.DiffOptions{
		Format: diff.DiffFormatUnified,
	})

	result, err := differ.CompareFiles(file1, file2)
	if err != nil {
		t.Fatalf("CompareFiles() error = %v", err)
	}

	if !result.HasChanges {
		t.Error("Expected changes but got none")
	}

	if len(result.Changes) == 0 {
		t.Error("Expected changes list to be populated")
	}

	if result.Patch == "" {
		t.Error("Expected patch output to be generated")
	}
}

func TestDiff_IdenticalFiles(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test")
	}

	tmpDir := t.TempDir()

	file1 := filepath.Join(tmpDir, "file1.json")
	file2 := filepath.Join(tmpDir, "file2.json")

	data := map[string]interface{}{
		"id":    "test-1",
		"title": "Test Workflow",
	}

	writeJSON(t, file1, data)
	writeJSON(t, file2, data)

	differ := diff.NewDiffer(diff.DiffOptions{
		Format: diff.DiffFormatUnified,
	})

	result, err := differ.CompareFiles(file1, file2)
	if err != nil {
		t.Fatalf("CompareFiles() error = %v", err)
	}

	if result.HasChanges {
		t.Error("Expected no changes but got some")
	}

	if len(result.Changes) > 0 {
		t.Errorf("Expected no changes but got %d", len(result.Changes))
	}
}

func TestDiff_JSONPatchFormat(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test")
	}

	tmpDir := t.TempDir()

	file1 := filepath.Join(tmpDir, "file1.json")
	file2 := filepath.Join(tmpDir, "file2.json")

	data1 := map[string]interface{}{
		"name": "old",
	}

	data2 := map[string]interface{}{
		"name": "new",
	}

	writeJSON(t, file1, data1)
	writeJSON(t, file2, data2)

	differ := diff.NewDiffer(diff.DiffOptions{
		Format: diff.DiffFormatJSONPatch,
	})

	result, err := differ.CompareFiles(file1, file2)
	if err != nil {
		t.Fatalf("CompareFiles() error = %v", err)
	}

	if !result.HasChanges {
		t.Error("Expected changes but got none")
	}

	var patch []map[string]interface{}
	if err := json.Unmarshal([]byte(result.Patch), &patch); err != nil {
		t.Fatalf("Failed to parse JSON patch: %v", err)
	}

	if len(patch) == 0 {
		t.Error("Expected JSON patch operations but got none")
	}
}

func TestDiff_SemanticFormat(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test")
	}

	tmpDir := t.TempDir()

	file1 := filepath.Join(tmpDir, "file1.json")
	file2 := filepath.Join(tmpDir, "file2.json")

	data1 := map[string]interface{}{
		"title": "Dashboard",
		"tiles": []interface{}{
			map[string]interface{}{"id": "1", "name": "tile1"},
		},
	}

	data2 := map[string]interface{}{
		"title": "Dashboard Updated",
		"tiles": []interface{}{
			map[string]interface{}{"id": "1", "name": "tile1"},
			map[string]interface{}{"id": "2", "name": "tile2"},
		},
	}

	writeJSON(t, file1, data1)
	writeJSON(t, file2, data2)

	differ := diff.NewDiffer(diff.DiffOptions{
		Format:   diff.DiffFormatSemantic,
		Semantic: true,
	})

	result, err := differ.CompareFiles(file1, file2)
	if err != nil {
		t.Fatalf("CompareFiles() error = %v", err)
	}

	if !result.HasChanges {
		t.Error("Expected changes but got none")
	}

	if result.Summary.Added == 0 && result.Summary.Modified == 0 {
		t.Error("Expected summary to show changes")
	}
}

func TestDiff_IgnoreMetadata(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test")
	}

	tmpDir := t.TempDir()

	file1 := filepath.Join(tmpDir, "file1.json")
	file2 := filepath.Join(tmpDir, "file2.json")

	data1 := map[string]interface{}{
		"id":   "test-1",
		"name": "Test",
		"metadata": map[string]interface{}{
			"createdAt": "2024-01-01",
			"version":   1,
		},
	}

	data2 := map[string]interface{}{
		"id":   "test-1",
		"name": "Test",
		"metadata": map[string]interface{}{
			"createdAt": "2024-01-02",
			"version":   2,
		},
	}

	writeJSON(t, file1, data1)
	writeJSON(t, file2, data2)

	differ := diff.NewDiffer(diff.DiffOptions{
		Format:         diff.DiffFormatUnified,
		IgnoreMetadata: true,
	})

	result, err := differ.CompareFiles(file1, file2)
	if err != nil {
		t.Fatalf("CompareFiles() error = %v", err)
	}

	if result.HasChanges {
		t.Error("Expected no changes when ignoring metadata")
	}
}

func TestDiff_IgnoreOrder(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test")
	}

	tmpDir := t.TempDir()

	file1 := filepath.Join(tmpDir, "file1.json")
	file2 := filepath.Join(tmpDir, "file2.json")

	data1 := map[string]interface{}{
		"items": []interface{}{
			map[string]interface{}{"id": "1", "name": "first"},
			map[string]interface{}{"id": "2", "name": "second"},
		},
	}

	data2 := map[string]interface{}{
		"items": []interface{}{
			map[string]interface{}{"id": "2", "name": "second"},
			map[string]interface{}{"id": "1", "name": "first"},
		},
	}

	writeJSON(t, file1, data1)
	writeJSON(t, file2, data2)

	differ := diff.NewDiffer(diff.DiffOptions{
		Format:      diff.DiffFormatUnified,
		IgnoreOrder: true,
	})

	result, err := differ.CompareFiles(file1, file2)
	if err != nil {
		t.Fatalf("CompareFiles() error = %v", err)
	}

	if result.HasChanges {
		t.Error("Expected no changes when ignoring order")
	}
}

func TestDiff_ComplexNestedStructure(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test")
	}

	tmpDir := t.TempDir()

	file1 := filepath.Join(tmpDir, "file1.json")
	file2 := filepath.Join(tmpDir, "file2.json")

	data1 := map[string]interface{}{
		"workflow": map[string]interface{}{
			"id":    "wf-1",
			"title": "Complex Workflow",
			"tasks": []interface{}{
				map[string]interface{}{
					"name": "task1",
					"config": map[string]interface{}{
						"timeout": 30,
						"retries": 3,
					},
				},
			},
		},
	}

	data2 := map[string]interface{}{
		"workflow": map[string]interface{}{
			"id":    "wf-1",
			"title": "Complex Workflow",
			"tasks": []interface{}{
				map[string]interface{}{
					"name": "task1",
					"config": map[string]interface{}{
						"timeout": 60,
						"retries": 5,
					},
				},
			},
		},
	}

	writeJSON(t, file1, data1)
	writeJSON(t, file2, data2)

	differ := diff.NewDiffer(diff.DiffOptions{
		Format: diff.DiffFormatUnified,
	})

	result, err := differ.CompareFiles(file1, file2)
	if err != nil {
		t.Fatalf("CompareFiles() error = %v", err)
	}

	if !result.HasChanges {
		t.Error("Expected changes in nested structure")
	}

	foundTimeoutChange := false
	foundRetriesChange := false
	for _, change := range result.Changes {
		if change.Path == "workflow.tasks[0].config.timeout" {
			foundTimeoutChange = true
		}
		if change.Path == "workflow.tasks[0].config.retries" {
			foundRetriesChange = true
		}
	}

	if !foundTimeoutChange {
		t.Error("Expected to find timeout change")
	}
	if !foundRetriesChange {
		t.Error("Expected to find retries change")
	}
}

func TestDiff_WorkflowResources(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test")
	}

	file1 := "../../test/fixtures/diff/workflow-v1.yaml"
	file2 := "../../test/fixtures/diff/workflow-v2.yaml"

	differ := diff.NewDiffer(diff.DiffOptions{
		Format: diff.DiffFormatUnified,
	})

	result, err := differ.CompareFiles(file1, file2)
	if err != nil {
		t.Fatalf("CompareFiles() error = %v", err)
	}

	if !result.HasChanges {
		t.Error("Expected changes between workflow versions")
	}

	foundDescriptionChange := false
	foundTaskNameChange := false
	foundChannelChange := false
	foundPriorityAdd := false

	for _, change := range result.Changes {
		if change.Path == "description" {
			foundDescriptionChange = true
		}
		if change.Path == "tasks[0].name" {
			foundTaskNameChange = true
		}
		if change.Path == "tasks[0].input.channel" {
			foundChannelChange = true
		}
		if change.Path == "tasks[1].input.priority" && change.Operation == diff.ChangeOpAdd {
			foundPriorityAdd = true
		}
	}

	if !foundDescriptionChange {
		t.Error("Expected to find description change")
	}
	if !foundTaskNameChange {
		t.Error("Expected to find task name change")
	}
	if !foundChannelChange {
		t.Error("Expected to find channel change")
	}
	if !foundPriorityAdd {
		t.Error("Expected to find priority field addition")
	}
}

func TestDiff_DashboardResources(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test")
	}

	file1 := "../../test/fixtures/diff/dashboard-v1.yaml"
	file2 := "../../test/fixtures/diff/dashboard-v2.yaml"

	differ := diff.NewDiffer(diff.DiffOptions{
		Format: diff.DiffFormatUnified,
	})

	result, err := differ.CompareFiles(file1, file2)
	if err != nil {
		t.Fatalf("CompareFiles() error = %v", err)
	}

	if !result.HasChanges {
		t.Error("Expected changes between dashboard versions")
	}

	foundNameChange := false
	foundDescriptionChange := false
	foundQueryChange := false
	foundTileAdd := false

	for _, change := range result.Changes {
		if change.Path == "name" {
			foundNameChange = true
		}
		if change.Path == "description" {
			foundDescriptionChange = true
		}
		if change.Path == "tiles[0].query" {
			foundQueryChange = true
		}
		if change.Path == "tiles[2]" && change.Operation == diff.ChangeOpAdd {
			foundTileAdd = true
		}
	}

	if !foundNameChange {
		t.Error("Expected to find name change")
	}
	if !foundDescriptionChange {
		t.Error("Expected to find description change")
	}
	if !foundQueryChange {
		t.Error("Expected to find query change in tile")
	}
	if !foundTileAdd {
		t.Error("Expected to find new tile addition")
	}
}

func TestDiff_NotebookResources(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test")
	}

	file1 := "../../test/fixtures/diff/notebook-v1.yaml"
	file2 := "../../test/fixtures/diff/notebook-v2.yaml"

	differ := diff.NewDiffer(diff.DiffOptions{
		Format: diff.DiffFormatUnified,
	})

	result, err := differ.CompareFiles(file1, file2)
	if err != nil {
		t.Fatalf("CompareFiles() error = %v", err)
	}

	if !result.HasChanges {
		t.Error("Expected changes between notebook versions")
	}

	foundNameChange := false
	foundDescriptionChange := false
	foundContentChange := false
	foundSectionAdd := false

	for _, change := range result.Changes {
		if change.Path == "name" {
			foundNameChange = true
		}
		if change.Path == "description" {
			foundDescriptionChange = true
		}
		if change.Path == "sections[0].content" {
			foundContentChange = true
		}
		if change.Path == "sections[2]" && change.Operation == diff.ChangeOpAdd {
			foundSectionAdd = true
		}
	}

	if !foundNameChange {
		t.Error("Expected to find name change")
	}
	if !foundDescriptionChange {
		t.Error("Expected to find description change")
	}
	if !foundContentChange {
		t.Error("Expected to find content change")
	}
	if !foundSectionAdd {
		t.Error("Expected to find new section addition")
	}
}

func TestDiff_AllFormats_Workflow(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test")
	}

	file1 := "../../test/fixtures/diff/workflow-v1.yaml"
	file2 := "../../test/fixtures/diff/workflow-v2.yaml"

	formats := []struct {
		name   string
		format diff.DiffFormat
	}{
		{"unified", diff.DiffFormatUnified},
		{"side-by-side", diff.DiffFormatSideBySide},
		{"json-patch", diff.DiffFormatJSONPatch},
		{"semantic", diff.DiffFormatSemantic},
	}

	for _, tc := range formats {
		t.Run(tc.name, func(t *testing.T) {
			differ := diff.NewDiffer(diff.DiffOptions{
				Format:   tc.format,
				Semantic: tc.format == diff.DiffFormatSemantic,
			})

			result, err := differ.CompareFiles(file1, file2)
			if err != nil {
				t.Fatalf("CompareFiles() with format %s error = %v", tc.name, err)
			}

			if !result.HasChanges {
				t.Errorf("Expected changes with format %s", tc.name)
			}

			if result.Patch == "" {
				t.Errorf("Expected non-empty patch output with format %s", tc.name)
			}

			if tc.format == diff.DiffFormatJSONPatch {
				var patch []map[string]interface{}
				if err := json.Unmarshal([]byte(result.Patch), &patch); err != nil {
					t.Errorf("Failed to parse JSON patch: %v", err)
				}
			}
		})
	}
}

func TestDiff_ResourceTypeDetection(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test")
	}

	tests := []struct {
		name     string
		file1    string
		file2    string
		wantType string
	}{
		{
			name:     "workflow detection",
			file1:    "../../test/fixtures/diff/workflow-v1.yaml",
			file2:    "../../test/fixtures/diff/workflow-v2.yaml",
			wantType: "workflow",
		},
		{
			name:     "dashboard detection",
			file1:    "../../test/fixtures/diff/dashboard-v1.yaml",
			file2:    "../../test/fixtures/diff/dashboard-v2.yaml",
			wantType: "dashboard",
		},
		{
			name:     "notebook detection",
			file1:    "../../test/fixtures/diff/notebook-v1.yaml",
			file2:    "../../test/fixtures/diff/notebook-v2.yaml",
			wantType: "notebook",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			differ := diff.NewDiffer(diff.DiffOptions{
				Format: diff.DiffFormatUnified,
			})

			result, err := differ.CompareFiles(tt.file1, tt.file2)
			if err != nil {
				t.Fatalf("CompareFiles() error = %v", err)
			}

			if !result.HasChanges {
				t.Error("Expected changes to be detected")
			}
		})
	}
}

func writeJSON(t *testing.T, path string, data interface{}) {
	t.Helper()
	jsonData, err := json.MarshalIndent(data, "", "  ")
	if err != nil {
		t.Fatalf("Failed to marshal JSON: %v", err)
	}
	if err := os.WriteFile(path, jsonData, 0644); err != nil {
		t.Fatalf("Failed to write file: %v", err)
	}
}
