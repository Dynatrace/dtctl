// Package schedulingrule provides the CLI resource handler for Dynatrace scheduling rules.
package schedulingrule

import (
	"context"

	"github.com/Dynatrace/dtctl/pkg/client"
	sdkschedulingrule "github.com/Dynatrace/dtctl/sdk/api/schedulingrule"
	"github.com/Dynatrace/dtctl/sdk/httpclient"
)

// SchedulingRule represents a scheduling rule (CLI version with table tags).
type SchedulingRule struct {
	ID                 string                 `json:"id,omitempty"                 yaml:"id,omitempty"                 table:"ID"`
	Title              string                 `json:"title"                        yaml:"title"                        table:"TITLE"`
	RuleType           string                 `json:"ruleType"                     yaml:"ruleType"                     table:"TYPE"`
	Description        string                 `json:"description,omitempty"        yaml:"description,omitempty"        table:"DESCRIPTION,wide"`
	BusinessCalendar   string                 `json:"businessCalendar,omitempty"   yaml:"businessCalendar,omitempty"   table:"CALENDAR,wide"`
	Version            int                    `json:"version,omitempty"            yaml:"version,omitempty"            table:"-"`
	RRule              map[string]interface{} `json:"rrule,omitempty"              yaml:"rrule,omitempty"              table:"-"`
	GroupingRule       map[string]interface{} `json:"groupingRule,omitempty"       yaml:"groupingRule,omitempty"       table:"-"`
	FixedOffsetRule    map[string]interface{} `json:"fixedOffsetRule,omitempty"    yaml:"fixedOffsetRule,omitempty"    table:"-"`
	RelativeOffsetRule map[string]interface{} `json:"relativeOffsetRule,omitempty" yaml:"relativeOffsetRule,omitempty" table:"-"`
	Labels             map[string]string      `json:"labels,omitempty"             yaml:"labels,omitempty"             table:"-"`
	ModificationInfo   *ModificationInfo      `json:"modificationInfo,omitempty"   yaml:"modificationInfo,omitempty"   table:"-"`
}

// ModificationInfo records who created and last changed a scheduling rule.
type ModificationInfo struct {
	CreatedBy        string `json:"createdBy,omitempty"        yaml:"createdBy,omitempty"`
	CreatedTime      string `json:"createdTime,omitempty"      yaml:"createdTime,omitempty"`
	LastModifiedBy   string `json:"lastModifiedBy,omitempty"   yaml:"lastModifiedBy,omitempty"`
	LastModifiedTime string `json:"lastModifiedTime,omitempty" yaml:"lastModifiedTime,omitempty"`
}

// OwnerID returns the user the rule belongs to, for safety-level ownership
// checks. The API exposes no owner field, so creation is the ownership signal:
// modificationInfo.createdBy. An empty result yields OwnershipUnknown, which the
// safety checker already treats as the restrictive case.
func (s *SchedulingRule) OwnerID() string {
	if s.ModificationInfo == nil {
		return ""
	}
	return s.ModificationInfo.CreatedBy
}

// SchedulingRuleList represents a list of scheduling rules.
type SchedulingRuleList struct {
	Count   int              `json:"count"`
	Results []SchedulingRule `json:"results"`
}

// fromSDKSchedulingRule converts an SDK SchedulingRule to a CLI SchedulingRule.
func fromSDKSchedulingRule(s *sdkschedulingrule.SchedulingRule) SchedulingRule {
	r := SchedulingRule{
		ID:                 s.ID,
		Title:              s.Title,
		RuleType:           s.RuleType,
		Description:        s.Description,
		BusinessCalendar:   s.BusinessCalendar,
		Version:            s.Version,
		RRule:              s.RRule,
		GroupingRule:       s.GroupingRule,
		FixedOffsetRule:    s.FixedOffsetRule,
		RelativeOffsetRule: s.RelativeOffsetRule,
		Labels:             s.Labels,
	}
	if s.ModificationInfo != nil {
		r.ModificationInfo = &ModificationInfo{
			CreatedBy:        s.ModificationInfo.CreatedBy,
			CreatedTime:      s.ModificationInfo.CreatedTime,
			LastModifiedBy:   s.ModificationInfo.LastModifiedBy,
			LastModifiedTime: s.ModificationInfo.LastModifiedTime,
		}
	}
	return r
}

// Handler handles scheduling rule resources.
// It delegates to the SDK handler and adds CLI-specific convenience methods.
type Handler struct {
	sdk *sdkschedulingrule.Handler
}

// NewHandler creates a new scheduling rule handler.
func NewHandler(c *client.Client) *Handler {
	return &Handler{
		sdk: sdkschedulingrule.NewHandler(httpclient.Wrap(c.HTTP())),
	}
}

// List retrieves scheduling rules.
// chunkSize controls page size; 0 returns only the first page.
// limit caps the total number of results; 0 means unlimited.
func (h *Handler) List(chunkSize, limit int64) (*SchedulingRuleList, error) {
	sdkResult, err := h.sdk.List(context.Background(), chunkSize, limit)
	if err != nil {
		return nil, err
	}
	results := make([]SchedulingRule, len(sdkResult.Results))
	for i := range sdkResult.Results {
		results[i] = fromSDKSchedulingRule(&sdkResult.Results[i])
	}
	return &SchedulingRuleList{Count: sdkResult.Count, Results: results}, nil
}

// Get retrieves a specific scheduling rule.
func (h *Handler) Get(id string) (*SchedulingRule, error) {
	sdkResult, err := h.sdk.Get(context.Background(), id)
	if err != nil {
		return nil, err
	}
	r := fromSDKSchedulingRule(sdkResult)
	return &r, nil
}

// Create creates a new scheduling rule.
func (h *Handler) Create(data []byte) (*SchedulingRule, error) {
	sdkResult, err := h.sdk.Create(context.Background(), data)
	if err != nil {
		return nil, err
	}
	r := fromSDKSchedulingRule(sdkResult)
	return &r, nil
}

// Update updates an existing scheduling rule.
func (h *Handler) Update(id string, data []byte) (*SchedulingRule, error) {
	sdkResult, err := h.sdk.Update(context.Background(), id, data)
	if err != nil {
		return nil, err
	}
	r := fromSDKSchedulingRule(sdkResult)
	return &r, nil
}

// Delete deletes a scheduling rule.
func (h *Handler) Delete(id string) error {
	return h.sdk.Delete(context.Background(), id)
}
