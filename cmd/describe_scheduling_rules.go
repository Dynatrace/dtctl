package cmd

import (
	"context"
	"fmt"
	"io"
	"sort"

	"github.com/spf13/cobra"

	"github.com/dynatrace-oss/dtctl/pkg/output"
	"github.com/dynatrace-oss/dtctl/pkg/resources/schedulingrule"
	"github.com/dynatrace-oss/dtctl/pkg/stability"
)

// describeSchedulingRuleCmd shows detailed info about a scheduling rule
var describeSchedulingRuleCmd = newDescribeSchedulingRuleCmd()

func newDescribeSchedulingRuleCmd() *cobra.Command {
	c := &cobra.Command{
		Use:     "scheduling-rule <id>",
		Aliases: []string{"sr"},
		Short:   "Show details of a scheduling rule",
		Long: `Show detailed information about a scheduling rule.

Examples:
  # Describe a scheduling rule
  dtctl describe scheduling-rule <scheduling-rule-id>
  dtctl describe sr <scheduling-rule-id>
`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			id := args[0]

			_, c, printer, err := setup(cmdContext(cmd))
			if err != nil {
				return err
			}

			handler := schedulingrule.NewHandler(c)

			rule, err := handler.Get(id)
			if err != nil {
				return err
			}

			if useSchedulingRuleDescribeTextView(cmdContext(cmd)) {
				printSchedulingRuleDescribeTable(currentStdout(cmdContext(cmd)), rule)
				return nil
			}

			enrichAgent(printer, "describe", "scheduling-rule")
			return printer.Print(rule)
		},
	}
	stability.MarkStable(c)
	return c
}

// useSchedulingRuleDescribeTextView reports whether to render the human-readable
// text view. Agent mode always takes the structured envelope path — note that
// agent mode leaves outputFormat at its "table" default, so a bare format check
// would wrongly emit human text into an agent session.
func useSchedulingRuleDescribeTextView(ctx context.Context) bool {
	if agentMode(ctx) {
		return false
	}
	return outputFormat(ctx) == "" || outputFormat(ctx) == "table"
}

// printSchedulingRuleDescribeTable renders a scheduling rule in human-readable describe format.
func printSchedulingRuleDescribeTable(w io.Writer, rule *schedulingrule.SchedulingRule) {
	// Wide enough that the longest static label ("Business Calendar:", 18
	// chars) still gets padding — FprintDescribeKV clamps to a single space
	// once the label reaches the width, which knocks that row out of column.
	const kw = 20
	output.FprintDescribeKV(w, "ID:", kw, "%s", rule.ID)
	output.FprintDescribeKV(w, "Title:", kw, "%s", rule.Title)
	if rule.Description != "" {
		output.FprintDescribeKV(w, "Description:", kw, "%s", rule.Description)
	}
	output.FprintDescribeKV(w, "Rule Type:", kw, "%s", rule.RuleType)
	if rule.BusinessCalendar != "" {
		output.FprintDescribeKV(w, "Business Calendar:", kw, "%s", rule.BusinessCalendar)
	}
	if rule.Version != 0 {
		output.FprintDescribeKV(w, "Version:", kw, "%d", rule.Version)
	}
	if body := schedulingRuleBody(rule); body != nil {
		fmt.Fprintln(w)
		fmt.Fprintln(w, "Rule:")
		for _, k := range schedulingRuleBodyKeys(body) {
			output.FprintDescribeKV(w, "  "+k+":", kw, "%v", body[k])
		}
	}
	if rule.ModificationInfo != nil {
		fmt.Fprintln(w)
		fmt.Fprintln(w, "Modification Info:")
		output.FprintDescribeKV(w, "  Created By:", kw, "%s", rule.ModificationInfo.CreatedBy)
		output.FprintDescribeKV(w, "  Created:", kw, "%s", rule.ModificationInfo.CreatedTime)
		output.FprintDescribeKV(w, "  Modified By:", kw, "%s", rule.ModificationInfo.LastModifiedBy)
		output.FprintDescribeKV(w, "  Modified:", kw, "%s", rule.ModificationInfo.LastModifiedTime)
	}
}

// schedulingRuleBody returns the rule body matching the rule's type, or nil.
func schedulingRuleBody(r *schedulingrule.SchedulingRule) map[string]interface{} {
	switch r.RuleType {
	case "rrule":
		return r.RRule
	case "grouping":
		return r.GroupingRule
	case "fixed_offset":
		return r.FixedOffsetRule
	case "relative_offset":
		return r.RelativeOffsetRule
	}
	return nil
}

// schedulingRuleBodyKeys keeps the describe output stable across runs.
func schedulingRuleBodyKeys(m map[string]interface{}) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// Declared stable: the invocation and output contract of this command is
// additive-only. Stable is never implied -- see AGENTS.md "Stability Tiers".
func init() {
}
