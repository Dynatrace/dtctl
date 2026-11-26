package cmd

import (
	"fmt"

	"github.com/Dynatrace/dtctl/pkg/apply"
)

// extractApplyBase extracts the embedded ApplyResultBase from any ApplyResult
// concrete type using a type switch. Returns nil if the type is unrecognized.
func extractApplyBase(result apply.ApplyResult) *apply.ApplyResultBase {
	switch r := result.(type) {
	case *apply.WorkflowApplyResult:
		return &r.ApplyResultBase
	case apply.WorkflowApplyResult:
		return &r.ApplyResultBase
	case *apply.SchedulingRuleApplyResult:
		return &r.ApplyResultBase
	case apply.SchedulingRuleApplyResult:
		return &r.ApplyResultBase
	case *apply.DashboardApplyResult:
		return &r.ApplyResultBase
	case apply.DashboardApplyResult:
		return &r.ApplyResultBase
	case *apply.NotebookApplyResult:
		return &r.ApplyResultBase
	case apply.NotebookApplyResult:
		return &r.ApplyResultBase
	case *apply.DocumentApplyResult:
		return &r.ApplyResultBase
	case apply.DocumentApplyResult:
		return &r.ApplyResultBase
	case *apply.SLOApplyResult:
		return &r.ApplyResultBase
	case apply.SLOApplyResult:
		return &r.ApplyResultBase
	case *apply.BucketApplyResult:
		return &r.ApplyResultBase
	case apply.BucketApplyResult:
		return &r.ApplyResultBase
	case *apply.SettingsApplyResult:
		return &r.ApplyResultBase
	case apply.SettingsApplyResult:
		return &r.ApplyResultBase
	case *apply.ConnectionApplyResult:
		return &r.ApplyResultBase
	case apply.ConnectionApplyResult:
		return &r.ApplyResultBase
	case *apply.MonitoringConfigApplyResult:
		return &r.ApplyResultBase
	case apply.MonitoringConfigApplyResult:
		return &r.ApplyResultBase
	case *apply.ExtensionConfigApplyResult:
		return &r.ApplyResultBase
	case apply.ExtensionConfigApplyResult:
		return &r.ApplyResultBase
	case *apply.SegmentApplyResult:
		return &r.ApplyResultBase
	case apply.SegmentApplyResult:
		return &r.ApplyResultBase
	case *apply.AnomalyDetectorApplyResult:
		return &r.ApplyResultBase
	case apply.AnomalyDetectorApplyResult:
		return &r.ApplyResultBase
	default:
		return nil
	}
}

// buildApplySuggestions returns agent-mode suggestions based on apply results.
func buildApplySuggestions(results []apply.ApplyResult) []string {
	if len(results) == 0 {
		return nil
	}

	// Determine the action from the first result
	base := extractApplyBase(results[0])
	if base == nil {
		return nil
	}

	switch base.Action {
	case apply.ActionCreated:
		return []string{
			fmt.Sprintf("Created successfully. Verify with 'dtctl describe %s %s'", base.ResourceType, base.ID),
			fmt.Sprintf("Monitor with 'dtctl get %s --watch'", base.ResourceType),
		}
	case apply.ActionUpdated:
		return []string{
			fmt.Sprintf("Updated successfully. Review history with 'dtctl history %s %s'", base.ResourceType, base.ID),
			"Compare with 'dtctl diff' to verify changes",
		}
	case apply.ActionUnchanged:
		return []string{
			"No changes detected. The resource is already up to date.",
		}
	default:
		return nil
	}
}
