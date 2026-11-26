// Package schedulingrule provides access to the Dynatrace Automation Scheduling Rules API.
package schedulingrule

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/Dynatrace/dtctl/sdk/httpclient"
)

const basePath = "/platform/automation/v1/scheduling-rules"

// Handler handles scheduling rule resources.
type Handler struct {
	client *httpclient.Client
}

// NewHandler creates a new scheduling rule handler.
func NewHandler(c *httpclient.Client) *Handler {
	return &Handler{client: c}
}

// SchedulingRule represents a scheduling rule resource.
//
// Exactly one of RRule, GroupingRule, FixedOffsetRule or RelativeOffsetRule is
// populated, selected by RuleType. Those bodies stay untyped maps so an apply
// round-trip preserves them verbatim: the API spec types several recurrence
// fields as "integer or array of integers", which no single Go type models
// without loss.
type SchedulingRule struct {
	ID                 string                 `json:"id,omitempty"`
	Version            int                    `json:"version,omitempty"`
	Title              string                 `json:"title"`
	Description        string                 `json:"description,omitempty"`
	RuleType           string                 `json:"ruleType"`
	RRule              map[string]interface{} `json:"rrule,omitempty"`
	GroupingRule       map[string]interface{} `json:"groupingRule,omitempty"`
	FixedOffsetRule    map[string]interface{} `json:"fixedOffsetRule,omitempty"`
	RelativeOffsetRule map[string]interface{} `json:"relativeOffsetRule,omitempty"`
	BusinessCalendar   string                 `json:"businessCalendar,omitempty"`
	Labels             map[string]string      `json:"labels,omitempty"`
	ModificationInfo   *ModificationInfo      `json:"modificationInfo,omitempty"`
}

// ModificationInfo records who created and last changed a scheduling rule.
// The API declares it read-only.
type ModificationInfo struct {
	CreatedBy        string `json:"createdBy,omitempty"`
	CreatedTime      string `json:"createdTime,omitempty"`
	LastModifiedBy   string `json:"lastModifiedBy,omitempty"`
	LastModifiedTime string `json:"lastModifiedTime,omitempty"`
}

// Rule type values accepted by the API's ruleType field.
const (
	RuleTypeRecurrence     = "rrule"
	RuleTypeGrouping       = "grouping"
	RuleTypeFixedOffset    = "fixed_offset"
	RuleTypeRelativeOffset = "relative_offset"
)

// SchedulingRuleList represents a list of scheduling rules.
type SchedulingRuleList struct {
	Count   int              `json:"count"`
	Results []SchedulingRule `json:"results"`
}

// List retrieves scheduling rules.
// chunkSize controls page size; 0 returns only the first page.
// limit caps the total number of results; 0 means unlimited.
//
// The Automation API applies a default page size when no limit is sent, so a
// single unpaginated GET would silently truncate large environments.
func (h *Handler) List(ctx context.Context, chunkSize, limit int64) (*SchedulingRuleList, error) {
	var all []SchedulingRule
	var totalCount int
	offset := 0

	for {
		// Per-request page size: chunkSize, narrowed to the remaining budget when a limit is set.
		pageSize := chunkSize
		if limit > 0 {
			remaining := limit - int64(len(all))
			if remaining <= 0 {
				break
			}
			// chunkSize==0 (single-page mode) combined with a limit: use the limit
			// as the page size so we fetch exactly N in one request, then break.
			if pageSize == 0 || remaining < pageSize {
				pageSize = remaining
			}
		}

		req := h.client.HTTP().R().SetContext(ctx)
		if pageSize > 0 {
			req.SetQueryParam("limit", fmt.Sprintf("%d", pageSize))
			if offset > 0 {
				req.SetQueryParam("offset", fmt.Sprintf("%d", offset))
			}
		}

		resp, err := req.Get(basePath)
		if err != nil {
			return nil, fmt.Errorf("list scheduling rules: %w", err)
		}
		if err := httpclient.CheckResponse(resp); err != nil {
			return nil, fmt.Errorf("list scheduling rules: %w", err)
		}

		var page SchedulingRuleList
		if err := json.Unmarshal(resp.Body(), &page); err != nil {
			return nil, fmt.Errorf("list scheduling rules: parse response: %w", err)
		}

		totalCount = page.Count
		all = append(all, page.Results...)

		if limit > 0 && int64(len(all)) >= limit {
			break
		}
		// chunkSize == 0 means single-page mode (no pagination loop).
		if chunkSize == 0 || len(all) >= totalCount || len(page.Results) == 0 {
			break
		}
		offset += len(page.Results)
	}

	// Defensive: the loop already caps well-behaved servers, but a server that
	// ignores the limit param could over-return — trim to the requested limit.
	if limit > 0 && int64(len(all)) > limit {
		all = all[:limit]
	}

	return &SchedulingRuleList{Count: totalCount, Results: all}, nil
}

// Get retrieves a specific scheduling rule.
func (h *Handler) Get(ctx context.Context, id string) (*SchedulingRule, error) {
	resp, err := h.client.HTTP().R().SetContext(ctx).Get(fmt.Sprintf("%s/%s", basePath, httpclient.PathSegment(id)))
	if err != nil {
		return nil, fmt.Errorf("get scheduling rule: %w", err)
	}
	if err := httpclient.CheckResponse(resp); err != nil {
		return nil, fmt.Errorf("get scheduling rule: %w", err)
	}
	var result SchedulingRule
	if err := json.Unmarshal(resp.Body(), &result); err != nil {
		return nil, fmt.Errorf("get scheduling rule: parse response: %w", err)
	}
	return &result, nil
}

// Create creates a new scheduling rule.
func (h *Handler) Create(ctx context.Context, data []byte) (*SchedulingRule, error) {
	resp, err := h.client.HTTP().R().SetContext(ctx).
		SetHeader("Content-Type", "application/json").
		SetBody(data).
		Post(basePath)
	if err != nil {
		return nil, fmt.Errorf("create scheduling rule: %w", err)
	}
	if err := httpclient.CheckResponse(resp); err != nil {
		return nil, fmt.Errorf("create scheduling rule: %w", err)
	}
	var result SchedulingRule
	if err := json.Unmarshal(resp.Body(), &result); err != nil {
		return nil, fmt.Errorf("create scheduling rule: parse response: %w", err)
	}
	return &result, nil
}

// Update updates an existing scheduling rule.
func (h *Handler) Update(ctx context.Context, id string, data []byte) (*SchedulingRule, error) {
	resp, err := h.client.HTTP().R().SetContext(ctx).
		SetHeader("Content-Type", "application/json").
		SetBody(data).
		Put(fmt.Sprintf("%s/%s", basePath, httpclient.PathSegment(id)))
	if err != nil {
		return nil, fmt.Errorf("update scheduling rule: %w", err)
	}
	if err := httpclient.CheckResponse(resp); err != nil {
		return nil, fmt.Errorf("update scheduling rule: %w", err)
	}
	var result SchedulingRule
	if err := json.Unmarshal(resp.Body(), &result); err != nil {
		return nil, fmt.Errorf("update scheduling rule: parse response: %w", err)
	}
	return &result, nil
}

// Delete deletes a scheduling rule.
func (h *Handler) Delete(ctx context.Context, id string) error {
	resp, err := h.client.HTTP().R().SetContext(ctx).Delete(fmt.Sprintf("%s/%s", basePath, httpclient.PathSegment(id)))
	if err != nil {
		return fmt.Errorf("delete scheduling rule: %w", err)
	}
	if err := httpclient.CheckResponse(resp); err != nil {
		return fmt.Errorf("delete scheduling rule: %w", err)
	}
	return nil
}
