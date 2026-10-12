package livedebugger

import sdkld "github.com/dynatrace-oss/dtctl/sdk/api/livedebugger"

// Re-export SDK types that have no table tags.
type (
	GraphQLWorkspaceResponse = sdkld.GraphQLWorkspaceResponse
	Workspace                = sdkld.Workspace
	FilterSet                = sdkld.FilterSet
	FilterField              = sdkld.FilterField
	RuleStatusNode           = sdkld.RuleStatusNode
	DeleteAllRulesResponse   = sdkld.DeleteAllRulesResponse
)

// BreakpointRule represents a breakpoint rule (CLI version with table tags).
type BreakpointRule struct {
	ID            string                 `json:"id" table:"ID"`
	ImmutableID   string                 `json:"immutableId,omitempty"`
	IsDisabled    bool                   `json:"is_disabled" table:"ACTIVE"`
	DisableReason string                 `json:"disable_reason,omitempty"`
	AugJSON       map[string]interface{} `json:"aug_json"`
	Processing    map[string]interface{} `json:"processing"`
}

// fromSDKBreakpointRule converts an SDK BreakpointRule to a CLI BreakpointRule.
func fromSDKBreakpointRule(s *sdkld.BreakpointRule) BreakpointRule {
	return BreakpointRule{
		ID:            s.ID,
		ImmutableID:   s.ImmutableID,
		IsDisabled:    s.IsDisabled,
		DisableReason: s.DisableReason,
		AugJSON:       s.AugJSON,
		Processing:    s.Processing,
	}
}

// Re-export SDK helper functions.
var (
	ExtractWorkspaceID    = sdkld.ExtractWorkspaceID
	ExtractDeletedRuleIDs = sdkld.ExtractDeletedRuleIDs
	ExtractRuleStatuses   = sdkld.ExtractRuleStatuses
	WorkspaceHasFilters   = sdkld.WorkspaceHasFilters
	WorkspaceFilterSets   = sdkld.WorkspaceFilterSets
)

// ExtractWorkspaceRules extracts breakpoint rules from a GraphQL response,
// converting SDK types to CLI types with table tags.
func ExtractWorkspaceRules(resp map[string]interface{}) ([]BreakpointRule, error) {
	sdkRules, err := sdkld.ExtractWorkspaceRules(resp)
	if err != nil {
		return nil, err
	}
	rules := make([]BreakpointRule, len(sdkRules))
	for i := range sdkRules {
		rules[i] = fromSDKBreakpointRule(&sdkRules[i])
	}
	return rules, nil
}
