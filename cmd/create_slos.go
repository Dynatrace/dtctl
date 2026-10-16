package cmd

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/dynatrace-oss/dtctl/pkg/output"
	"github.com/dynatrace-oss/dtctl/pkg/resources/slo"
	"github.com/dynatrace-oss/dtctl/pkg/safety"
	"github.com/dynatrace-oss/dtctl/pkg/stability"
	"github.com/dynatrace-oss/dtctl/pkg/util/format"
	"github.com/dynatrace-oss/dtctl/pkg/util/template"
)

// createSLOCmd creates an SLO from a file
var createSLOCmd = newCreateSLOCmd()

func newCreateSLOCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "slo -f <file>",
		Short: "Create a service-level objective from a file",
		Long: `Create a new SLO from a YAML or JSON file.

Examples:
  # Create an SLO from YAML
  dtctl create slo -f slo.yaml

  # Create with template variables
  dtctl create slo -f slo.yaml --set target=99.9

  # Dry run to preview
  dtctl create slo -f slo.yaml --dry-run
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
					Linef("Dry run: would create SLO").
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

			handler := slo.NewHandler(c)

			result, err := handler.Create(jsonData)
			if err != nil {
				return fmt.Errorf("failed to create SLO: %w", err)
			}

			output.FprintSuccess(currentStderr(cmdContext(cmd)), "SLO %q created", result.Name)
			output.FprintInfo(currentStderr(cmdContext(cmd)), "  ID:   %s", result.ID)
			output.FprintInfo(currentStderr(cmdContext(cmd)), "  Name: %s", result.Name)
			output.FprintInfo(currentStderr(cmdContext(cmd)), "  URL:  %s/ui/apps/dynatrace.site.reliability/slos/%s", c.BaseURL(), result.ID)
			return nil
		},
	}
	c.Flags().StringP("file", "f", "", "file containing SLO definition, or - for stdin (required)")
	c.Flags().StringArray("set", []string{}, "set template variable (key=value)")
	stability.MarkStable(c)
	markFlagRequiredNonEmpty(c, "file")
	return c
}

// Declared stable: the invocation and output contract of this command is
// additive-only. Stable is never implied -- see AGENTS.md "Stability Tiers".
