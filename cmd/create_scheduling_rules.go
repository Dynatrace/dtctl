package cmd

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/dynatrace-oss/dtctl/pkg/output"
	"github.com/dynatrace-oss/dtctl/pkg/resources/schedulingrule"
	"github.com/dynatrace-oss/dtctl/pkg/safety"
	"github.com/dynatrace-oss/dtctl/pkg/stability"
	"github.com/dynatrace-oss/dtctl/pkg/util/format"
	"github.com/dynatrace-oss/dtctl/pkg/util/template"
)

// createSchedulingRuleCmd creates a scheduling rule from a file
var createSchedulingRuleCmd = newCreateSchedulingRuleCmd()

func newCreateSchedulingRuleCmd() *cobra.Command {
	c := &cobra.Command{
		Use:     "scheduling-rule -f <file>",
		Aliases: []string{"scheduling-rules", "sr"},
		Short:   "Create a scheduling rule from a file",
		Long: `Create a new scheduling rule from a YAML or JSON file.

Examples:
  # Create a scheduling rule from YAML
  dtctl create scheduling-rule -f rule.yaml

  # Create with template variables
  dtctl create scheduling-rule -f rule.yaml --set freq=WEEKLY

  # Dry run to preview
  dtctl create scheduling-rule -f rule.yaml --dry-run
`,
		RunE: func(cmd *cobra.Command, args []string) error {
			file, _ := cmd.Flags().GetString("file")

			setFlags, _ := cmd.Flags().GetStringArray("set")

			fileData, err := readFileFlag(cmdContext(cmd), "file", file)
			if err != nil {
				return fmt.Errorf("failed to read file: %w", err)
			}

			jsonData, err := format.ValidateAndConvert(fileData)
			if err != nil {
				return fmt.Errorf("invalid file format: %w", err)
			}

			if len(setFlags) > 0 {
				templateVars, err := template.ParseSetFlags(setFlags)
				if err != nil {
					return fmt.Errorf("invalid --set flag: %w", err)
				}
				rendered, err := template.RenderTemplate(string(jsonData), templateVars)
				if err != nil {
					return fmt.Errorf("template rendering failed: %w", err)
				}
				jsonData = []byte(rendered)
			}

			if dryRun(cmdContext(cmd)) {
				printer := newPrinterCtx(cmdContext(cmd))
				ap := enrichAgent(printer, "create", "scheduling-rule")
				if ap != nil {
					ap.SetSuggestions([]string{"Remove --dry-run to create the scheduling rule"})
					return printer.Print(map[string]interface{}{
						"dryRun":  true,
						"content": string(jsonData),
					})
				}
				output.FprintInfo(currentStderr(cmdContext(cmd)), "Dry run: would create scheduling rule")
				output.FprintInfo(currentStderr(cmdContext(cmd)), "---")
				output.FprintInfo(currentStderr(cmdContext(cmd)), "%s", string(jsonData))
				output.FprintInfo(currentStderr(cmdContext(cmd)), "---")
				return nil
			}

			_, c, err := setupWithSafety(cmdContext(cmd), safety.OperationCreate)
			if err != nil {
				return err
			}

			handler := schedulingrule.NewHandler(c)

			result, err := handler.Create(jsonData)
			if err != nil {
				return fmt.Errorf("failed to create scheduling rule: %w", err)
			}

			if agentMode(cmdContext(cmd)) {
				printer := newPrinterCtx(cmdContext(cmd))
				ap := enrichAgent(printer, "create", "scheduling-rule")
				if ap != nil {
					ap.SetSuggestions([]string{
						fmt.Sprintf("Run 'dtctl describe scheduling-rule %s' to view details", result.ID),
						"Run 'dtctl get scheduling-rules' to list all rules",
					})
				}
				return printer.Print(result)
			}
			output.FprintSuccess(currentStderr(cmdContext(cmd)), "Scheduling rule %q created", result.Title)
			output.FprintInfo(currentStderr(cmdContext(cmd)), "  ID:    %s", result.ID)
			output.FprintInfo(currentStderr(cmdContext(cmd)), "  Title: %s", result.Title)
			return nil
		},
	}
	c.Flags().StringP("file", "f", "", "file containing scheduling rule definition, or - for stdin (required)")
	c.Flags().StringArray("set", []string{}, "set template variable (key=value)")
	stability.MarkStable(c)
	markFlagRequiredNonEmpty(c, "file")
	return c
}

// Declared stable: the invocation and output contract of this command is
// additive-only. Stable is never implied -- see AGENTS.md "Stability Tiers".
