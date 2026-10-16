package diff

import (
	"fmt"
	"testing"

	"github.com/dynatrace-oss/dtctl/pkg/vfs"
)

func TestDiffer_Compare(t *testing.T) {
	tests := []struct {
		name       string
		left       interface{}
		right      interface{}
		wantChange bool
	}{
		{
			name:       "no changes",
			left:       map[string]interface{}{"key": "value"},
			right:      map[string]interface{}{"key": "value"},
			wantChange: false,
		},
		{
			name:       "field changed",
			left:       map[string]interface{}{"key": "old"},
			right:      map[string]interface{}{"key": "new"},
			wantChange: true,
		},
		{
			name:       "field added",
			left:       map[string]interface{}{"key1": "value1"},
			right:      map[string]interface{}{"key1": "value1", "key2": "value2"},
			wantChange: true,
		},
		{
			name:       "field removed",
			left:       map[string]interface{}{"key1": "value1", "key2": "value2"},
			right:      map[string]interface{}{"key1": "value1"},
			wantChange: true,
		},
		{
			name: "nested change",
			left: map[string]interface{}{
				"outer": map[string]interface{}{
					"inner": "old",
				},
			},
			right: map[string]interface{}{
				"outer": map[string]interface{}{
					"inner": "new",
				},
			},
			wantChange: true,
		},
		{
			name: "array change",
			left: map[string]interface{}{
				"items": []interface{}{"a", "b", "c"},
			},
			right: map[string]interface{}{
				"items": []interface{}{"a", "b", "d"},
			},
			wantChange: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			differ := NewDiffer(DiffOptions{
				Format: DiffFormatUnified,
			})

			result, err := differ.Compare(tt.left, tt.right, "left", "right")
			if err != nil {
				t.Errorf("Compare() error = %v", err)
				return
			}

			if result.HasChanges != tt.wantChange {
				t.Errorf("Compare() HasChanges = %v, want %v", result.HasChanges, tt.wantChange)
			}

			if tt.wantChange && len(result.Changes) == 0 {
				t.Errorf("Compare() expected changes but got none")
			}

			if !tt.wantChange && len(result.Changes) > 0 {
				t.Errorf("Compare() expected no changes but got %d", len(result.Changes))
			}
		})
	}
}

func TestDiffer_CompareWithIgnoreMetadata(t *testing.T) {
	left := map[string]interface{}{
		"id":   "123",
		"name": "test",
		"metadata": map[string]interface{}{
			"createdAt": "2024-01-01",
			"version":   1,
		},
	}

	right := map[string]interface{}{
		"id":   "123",
		"name": "test",
		"metadata": map[string]interface{}{
			"createdAt": "2024-01-02",
			"version":   2,
		},
	}

	differ := NewDiffer(DiffOptions{
		Format:         DiffFormatUnified,
		IgnoreMetadata: true,
	})

	result, err := differ.Compare(left, right, "left", "right")
	if err != nil {
		t.Errorf("Compare() error = %v", err)
		return
	}

	if result.HasChanges {
		t.Errorf("Compare() with IgnoreMetadata should not detect changes in metadata fields")
	}
}

func TestDiffer_CompareWithIgnoreOrder(t *testing.T) {
	left := map[string]interface{}{
		"items": []interface{}{
			map[string]interface{}{"id": "1", "name": "first"},
			map[string]interface{}{"id": "2", "name": "second"},
		},
	}

	right := map[string]interface{}{
		"items": []interface{}{
			map[string]interface{}{"id": "2", "name": "second"},
			map[string]interface{}{"id": "1", "name": "first"},
		},
	}

	differ := NewDiffer(DiffOptions{
		Format:      DiffFormatUnified,
		IgnoreOrder: true,
	})

	result, err := differ.Compare(left, right, "left", "right")
	if err != nil {
		t.Errorf("Compare() error = %v", err)
		return
	}

	if result.HasChanges {
		t.Errorf("Compare() with IgnoreOrder should not detect changes when only order differs")
	}
}

func TestComputeSummary(t *testing.T) {
	tests := []struct {
		name    string
		changes []Change
		wantAdd int
		wantRem int
		wantMod int
	}{
		{
			name:    "no changes",
			changes: []Change{},
			wantAdd: 0,
			wantRem: 0,
			wantMod: 0,
		},
		{
			name: "mixed changes",
			changes: []Change{
				{Operation: ChangeOpAdd},
				{Operation: ChangeOpAdd},
				{Operation: ChangeOpRemove},
				{Operation: ChangeOpReplace},
				{Operation: ChangeOpReplace},
				{Operation: ChangeOpReplace},
			},
			wantAdd: 2,
			wantRem: 1,
			wantMod: 3,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			summary := computeSummary(tt.changes)

			if summary.Added != tt.wantAdd {
				t.Errorf("computeSummary() Added = %v, want %v", summary.Added, tt.wantAdd)
			}
			if summary.Removed != tt.wantRem {
				t.Errorf("computeSummary() Removed = %v, want %v", summary.Removed, tt.wantRem)
			}
			if summary.Modified != tt.wantMod {
				t.Errorf("computeSummary() Modified = %v, want %v", summary.Modified, tt.wantMod)
			}
		})
	}
}

func TestCalculateImpact(t *testing.T) {
	tests := []struct {
		name    string
		summary DiffSummary
		want    ImpactLevel
	}{
		{
			name:    "no changes - low impact",
			summary: DiffSummary{Added: 0, Removed: 0, Modified: 0},
			want:    ImpactLow,
		},
		{
			name:    "few changes - low impact",
			summary: DiffSummary{Added: 2, Removed: 0, Modified: 3},
			want:    ImpactLow,
		},
		{
			name:    "some changes - medium impact",
			summary: DiffSummary{Added: 5, Removed: 1, Modified: 5},
			want:    ImpactMedium,
		},
		{
			name:    "many changes - high impact",
			summary: DiffSummary{Added: 10, Removed: 6, Modified: 10},
			want:    ImpactHigh,
		},
		{
			name:    "many removals - high impact",
			summary: DiffSummary{Added: 2, Removed: 8, Modified: 2},
			want:    ImpactHigh,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := calculateImpact(tt.summary)
			if got != tt.want {
				t.Errorf("calculateImpact() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestDiffer_GetFormatter(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		options  DiffOptions
		wantType string
	}{
		{
			name: "semantic formatter via Semantic flag",
			options: DiffOptions{
				Semantic: true,
				Format:   DiffFormatUnified,
			},
			wantType: "*diff.SemanticFormatter",
		},
		{
			name: "side-by-side formatter",
			options: DiffOptions{
				Format:   DiffFormatSideBySide,
				Colorize: true,
			},
			wantType: "*diff.SideBySideFormatter",
		},
		{
			name: "json-patch formatter",
			options: DiffOptions{
				Format: DiffFormatJSONPatch,
			},
			wantType: "*diff.JSONPatchFormatter",
		},
		{
			name: "semantic formatter via Format",
			options: DiffOptions{
				Format: DiffFormatSemantic,
			},
			wantType: "*diff.SemanticFormatter",
		},
		{
			name: "unified formatter (default)",
			options: DiffOptions{
				Format:       DiffFormatUnified,
				ContextLines: 3,
				Colorize:     false,
			},
			wantType: "*diff.UnifiedFormatter",
		},
		{
			name: "unified formatter with empty format",
			options: DiffOptions{
				Format: "",
			},
			wantType: "*diff.UnifiedFormatter",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			differ := NewDiffer(tt.options)
			formatter := differ.getFormatter()

			gotType := fmt.Sprintf("%T", formatter)
			if gotType != tt.wantType {
				t.Errorf("getFormatter() type = %v, want %v", gotType, tt.wantType)
			}
		})
	}
}

func TestDiffer_Compare_AllFormats(t *testing.T) {
	t.Parallel()

	left := map[string]interface{}{
		"name":  "old-name",
		"value": 10,
	}
	right := map[string]interface{}{
		"name":  "new-name",
		"value": 20,
	}

	formats := []DiffFormat{
		DiffFormatUnified,
		DiffFormatSideBySide,
		DiffFormatJSONPatch,
		DiffFormatSemantic,
	}

	for _, format := range formats {
		t.Run(string(format), func(t *testing.T) {
			t.Parallel()
			differ := NewDiffer(DiffOptions{
				Format: format,
			})

			result, err := differ.Compare(left, right, "left", "right")
			if err != nil {
				t.Errorf("Compare() with format %s error = %v", format, err)
				return
			}

			if !result.HasChanges {
				t.Errorf("Compare() with format %s should detect changes", format)
			}

			if result.Patch == "" && format != DiffFormatUnified {
				// Unified format can have empty patch if no changes shown
				t.Errorf("Compare() with format %s should generate patch", format)
			}
		})
	}
}

func TestDiffer_WithVFSComparesFilesFromItsOwnEnv(t *testing.T) {
	env := vfs.Env{FS: vfs.NewMapFS(map[string][]byte{
		"left.json":  []byte(`{"key": "old"}`),
		"right.json": []byte(`{"key": "new"}`),
	})}

	result, err := NewDiffer(DiffOptions{}).WithVFS(env).CompareFiles("left.json", "right.json")
	if err != nil {
		t.Fatalf("CompareFiles: %v", err)
	}
	if !result.HasChanges {
		t.Error("the two files differ, but no change was reported")
	}

	if _, err := NewDiffer(DiffOptions{}).WithVFS(env).CompareFiles("left.json", "missing.json"); err == nil {
		t.Error("a path outside the Env's filesystem must not resolve")
	}
}
