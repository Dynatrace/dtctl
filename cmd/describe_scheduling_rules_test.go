package cmd

import (
	"bytes"
	"testing"

	cmdtestutil "github.com/Dynatrace/dtctl/cmd/testutil"
	"github.com/Dynatrace/dtctl/pkg/resources/schedulingrule"
)

func TestDescribeSchedulingRuleTableGolden(t *testing.T) {
	rule := &schedulingrule.SchedulingRule{
		ID:               "a1b2c3d4-e5f6-4a7b-8c9d-sr0000000001",
		Title:            "Weekly Maintenance",
		Description:      "Weekly maintenance window every Sunday",
		RuleType:         "rrule",
		BusinessCalendar: "us-holidays",
		Version:          3,
		RRule: map[string]interface{}{
			"freq":     "WEEKLY",
			"byday":    "SU",
			"duration": "PT2H",
		},
		ModificationInfo: &schedulingrule.ModificationInfo{
			CreatedBy:        "admin@example.invalid",
			CreatedTime:      "2024-01-15T10:00:00Z",
			LastModifiedBy:   "ops@example.invalid",
			LastModifiedTime: "2024-06-01T12:00:00Z",
		},
	}

	var buf bytes.Buffer
	printSchedulingRuleDescribeTable(&buf, rule)

	cmdtestutil.AssertGoldenStripped(t, "describe-scheduling-rule/table-full", buf.String())
}

func TestDescribeSchedulingRuleTableGolden_Minimal(t *testing.T) {
	rule := &schedulingrule.SchedulingRule{
		ID:       "b2c3d4e5-f6a7-4b8c-9d0e-sr0000000002",
		Title:    "Simple Rule",
		RuleType: "grouping",
	}

	var buf bytes.Buffer
	printSchedulingRuleDescribeTable(&buf, rule)

	cmdtestutil.AssertGoldenStripped(t, "describe-scheduling-rule/table-minimal", buf.String())
}
