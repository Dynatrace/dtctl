package cmd

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/dynatrace-oss/dtctl/pkg/output"
	"github.com/dynatrace-oss/dtctl/pkg/resources/workflow"
	"github.com/dynatrace-oss/dtctl/pkg/safety"
	"github.com/dynatrace-oss/dtctl/pkg/stability"
	"github.com/dynatrace-oss/dtctl/pkg/util/format"

	"github.com/dynatrace-oss/dtctl/pkg/util/template"
)

// createWorkflowCmd creates a workflow from a file
var createWorkflowCmd = newCreateWorkflowCmd()

func newCreateWorkflowCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "workflow -f <file>",
		Short: "Create a workflow from a file",
		Long: `Create a new workflow from a YAML or JSON file.

Examples:
  # Create a workflow from YAML
  dtctl create workflow -f workflow.yaml

  # Create with template variables
  dtctl create workflow -f workflow.yaml --set env=prod --set owner=team-a

  # Dry run to preview
  dtctl create workflow -f workflow.yaml --dry-run
`,
		RunE: func(cmd *cobra.Command, args []string) error {
			file, _ := cmd.Flags().GetString("file")

			setFlags, _ := cmd.Flags().GetStringArray("set")

			// Read the file
			fileData, err := readFileFlag(cmdContext(cmd), "file", file)
			if err != nil {
				return fmt.Errorf("failed to read file: %w", err)
			}

			// Convert to JSON if needed
			jsonData, err := format.ValidateAndConvert(fileData)
			if err != nil {
				return fmt.Errorf("invalid file format: %w", err)
			}

			// Apply template rendering if variables provided
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

			// Handle dry-run
			if dryRun(cmdContext(cmd)) {
				return newDryRunReport(cmd).
					Linef("Dry run: would create workflow").
					Linef("---").
					Linef("%s", string(jsonData)).
					Linef("---").
					Payload(jsonData).
					Print()
			}

			_, c, err := setupWithSafety(cmdContext(cmd), safety.OperationCreate)
			if err != nil {
				return err
			}

			handler := workflow.NewHandler(c)

			result, err := handler.Create(jsonData)
			if err != nil {
				return fmt.Errorf("failed to create workflow: %w", err)
			}

			output.FprintSuccess(currentStderr(cmdContext(cmd)), "Workflow %q created", result.Title)
			output.FprintInfo(currentStderr(cmdContext(cmd)), "  ID:   %s", result.ID)
			output.FprintInfo(currentStderr(cmdContext(cmd)), "  Name: %s", result.Title)
			output.FprintInfo(currentStderr(cmdContext(cmd)), "  URL:  %s/ui/apps/dynatrace.automations/workflows/%s", c.BaseURL(), result.ID)
			return nil
		},
	}
	c.Flags().StringP("file", "f", "", "file containing workflow definition, or - for stdin (required)")
	c.Flags().StringArray("set", []string{}, "set template variable (key=value)")
	stability.MarkStable(c)
	markFlagRequiredNonEmpty(c, "file")
	return c
}

// Declared stable: the invocation and output contract of this command is
// additive-only. Stable is never implied -- see AGENTS.md "Stability Tiers".
