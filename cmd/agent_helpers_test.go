package cmd

import (
	"bytes"
	"testing"

	"github.com/Dynatrace/dtctl/pkg/apply"
	"github.com/Dynatrace/dtctl/pkg/output"
)

func TestExtractApplyBase_PointerTypes(t *testing.T) {
	tests := []struct {
		name   string
		result apply.ApplyResult
		want   string // expected ResourceType
	}{
		{
			name: "WorkflowApplyResult pointer",
			result: &apply.WorkflowApplyResult{
				ApplyResultBase: apply.ApplyResultBase{
					Action: apply.ActionCreated, ResourceType: "workflow", ID: "w1",
				},
			},
			want: "workflow",
		},
		{
			name: "DashboardApplyResult pointer",
			result: &apply.DashboardApplyResult{
				ApplyResultBase: apply.ApplyResultBase{
					Action: apply.ActionUpdated, ResourceType: "dashboard", ID: "d1",
				},
			},
			want: "dashboard",
		},
		{
			name: "DocumentApplyResult pointer (custom type)",
			result: &apply.DocumentApplyResult{
				ApplyResultBase: apply.ApplyResultBase{
					Action: apply.ActionUpdated, ResourceType: "acme:config", ID: "doc1",
				},
				Labels: []string{"team-a"},
			},
			want: "acme:config",
		},
		{
			name: "SettingsApplyResult pointer",
			result: &apply.SettingsApplyResult{
				ApplyResultBase: apply.ApplyResultBase{
					Action: apply.ActionUnchanged, ResourceType: "settings", ID: "s1",
				},
			},
			want: "settings",
		},
		{
			name: "BucketApplyResult pointer",
			result: &apply.BucketApplyResult{
				ApplyResultBase: apply.ApplyResultBase{
					Action: apply.ActionCreated, ResourceType: "bucket", ID: "b1",
				},
			},
			want: "bucket",
		},
		{
			name: "ConnectionApplyResult pointer",
			result: &apply.ConnectionApplyResult{
				ApplyResultBase: apply.ApplyResultBase{
					Action: apply.ActionCreated, ResourceType: "connection", ID: "c1",
				},
			},
			want: "connection",
		},
		{
			name: "MonitoringConfigApplyResult pointer",
			result: &apply.MonitoringConfigApplyResult{
				ApplyResultBase: apply.ApplyResultBase{
					Action: apply.ActionCreated, ResourceType: "monitoring-config", ID: "m1",
				},
			},
			want: "monitoring-config",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			base := extractApplyBase(tt.result)
			if base == nil {
				t.Fatal("expected non-nil base")
			}
			if base.ResourceType != tt.want {
				t.Errorf("expected ResourceType=%q, got %q", tt.want, base.ResourceType)
			}
		})
	}
}

func TestExtractApplyBase_ValueTypes(t *testing.T) {
	// Verify value types (not pointers) also work — this was the panic scenario
	result := apply.WorkflowApplyResult{
		ApplyResultBase: apply.ApplyResultBase{
			Action: apply.ActionCreated, ResourceType: "workflow", ID: "w1",
		},
	}

	base := extractApplyBase(result)
	if base == nil {
		t.Fatal("expected non-nil base for value type")
	}
	if base.ResourceType != "workflow" {
		t.Errorf("expected ResourceType=workflow, got %q", base.ResourceType)
	}
}

func TestExtractApplyBase_NilResult(t *testing.T) {
	// Type assertion on nil interface should not panic
	var result apply.ApplyResult
	if result != nil {
		base := extractApplyBase(result)
		if base != nil {
			t.Error("expected nil base for nil input")
		}
	}
	// If result is nil, callers should not call extractApplyBase — this is fine
}

func TestBuildApplySuggestions_Created(t *testing.T) {
	results := []apply.ApplyResult{
		&apply.WorkflowApplyResult{
			ApplyResultBase: apply.ApplyResultBase{
				Action: apply.ActionCreated, ResourceType: "workflow", ID: "abc-123",
			},
		},
	}

	suggestions := buildApplySuggestions(results)
	if len(suggestions) != 2 {
		t.Fatalf("expected 2 suggestions for created, got %d", len(suggestions))
	}
	if suggestions[0] != "Created successfully. Verify with 'dtctl describe workflow abc-123'" {
		t.Errorf("unexpected suggestion[0]: %q", suggestions[0])
	}
	if suggestions[1] != "Monitor with 'dtctl get workflow --watch'" {
		t.Errorf("unexpected suggestion[1]: %q", suggestions[1])
	}
}

func TestBuildApplySuggestions_Updated(t *testing.T) {
	results := []apply.ApplyResult{
		&apply.DashboardApplyResult{
			ApplyResultBase: apply.ApplyResultBase{
				Action: apply.ActionUpdated, ResourceType: "dashboard", ID: "d-456",
			},
		},
	}

	suggestions := buildApplySuggestions(results)
	if len(suggestions) != 2 {
		t.Fatalf("expected 2 suggestions for updated, got %d", len(suggestions))
	}
	if suggestions[0] != "Updated successfully. Review history with 'dtctl history dashboard d-456'" {
		t.Errorf("unexpected suggestion[0]: %q", suggestions[0])
	}
}

func TestBuildApplySuggestions_Unchanged(t *testing.T) {
	results := []apply.ApplyResult{
		&apply.SLOApplyResult{
			ApplyResultBase: apply.ApplyResultBase{
				Action: apply.ActionUnchanged, ResourceType: "slo", ID: "s-789",
			},
		},
	}

	suggestions := buildApplySuggestions(results)
	if len(suggestions) != 1 {
		t.Fatalf("expected 1 suggestion for unchanged, got %d", len(suggestions))
	}
	if suggestions[0] != "No changes detected. The resource is already up to date." {
		t.Errorf("unexpected suggestion: %q", suggestions[0])
	}
}

func TestBuildApplySuggestions_EmptyResults(t *testing.T) {
	suggestions := buildApplySuggestions(nil)
	if suggestions != nil {
		t.Errorf("expected nil suggestions for empty results, got %v", suggestions)
	}

	suggestions = buildApplySuggestions([]apply.ApplyResult{})
	if suggestions != nil {
		t.Errorf("expected nil suggestions for empty slice, got %v", suggestions)
	}
}

func TestEnrichAgent_AgentPrinter(t *testing.T) {
	var buf bytes.Buffer
	ctx := &output.ResponseContext{}
	printer := output.NewAgentPrinter(&buf, ctx)

	ap := enrichAgent(printer, "get", "workflow")
	if ap == nil {
		t.Fatal("expected non-nil AgentPrinter from enrichAgent")
	}
	if ap.Context().Verb != "get" {
		t.Errorf("expected verb=get, got %q", ap.Context().Verb)
	}
	if ap.Context().Resource != "workflow" {
		t.Errorf("expected resource=workflow, got %q", ap.Context().Resource)
	}
}

func TestEnrichAgent_NonAgentPrinter(t *testing.T) {
	// When not in agent mode, enrichAgent should return nil
	printer := output.NewPrinterWithOptions("json", &bytes.Buffer{}, false)

	ap := enrichAgent(printer, "get", "workflow")
	if ap != nil {
		t.Error("expected nil for non-AgentPrinter")
	}
}
