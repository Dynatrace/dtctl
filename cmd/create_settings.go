package cmd

import (
	"encoding/json"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/dynatrace-oss/dtctl/pkg/output"
	"github.com/dynatrace-oss/dtctl/pkg/resources/settings"
	"github.com/dynatrace-oss/dtctl/pkg/safety"
	"github.com/dynatrace-oss/dtctl/pkg/stability"
	"github.com/dynatrace-oss/dtctl/pkg/util/format"
	"github.com/dynatrace-oss/dtctl/pkg/util/template"
)

// createSettingsCmd creates a settings object from a file
var createSettingsCmd = newCreateSettingsCmd()

func newCreateSettingsCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "settings -f <file> --schema <schema-id> --scope <scope>",
		Short: "Create a settings object from a file",
		Long: `Create a new settings object from a YAML or JSON file.

Examples:
  # Create a settings object
  dtctl create settings -f pipeline.yaml --schema builtin:openpipeline.logs.pipelines --scope environment

  # Create with template variables
  dtctl create settings -f settings.yaml --schema builtin:openpipeline.logs.pipelines --scope environment --set name=prod

  # Dry run to preview
  dtctl create settings -f settings.yaml --schema builtin:openpipeline.logs.pipelines --scope environment --dry-run

  # Validate against the API without creating
  dtctl create settings -f settings.yaml --schema builtin:openpipeline.logs.pipelines --scope environment --validate-only
`,
		Aliases: []string{"setting"},
		RunE: func(cmd *cobra.Command, args []string) error {
			file, _ := cmd.Flags().GetString("file")
			schemaID, _ := cmd.Flags().GetString("schema")
			scope, _ := cmd.Flags().GetString("scope")
			setFlags, _ := cmd.Flags().GetStringArray("set")
			validateOnly, _ := cmd.Flags().GetBool("validate-only")

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

			// Parse the value
			var value map[string]any
			if err := json.Unmarshal(jsonData, &value); err != nil {
				return fmt.Errorf("failed to parse settings value: %w", err)
			}

			// Handle dry-run
			if dryRun(cmdContext(cmd)) {
				return newDryRunReport(cmd).
					Linef("Dry run: would create settings object").
					Field("Schema", "%s", schemaID).
					Field("Scope", "%s", scope).
					Linef("---").
					Linef("%s", string(jsonData)).
					Linef("---").
					Payload(jsonData).
					Print()
			}

			req := settings.SettingsObjectCreate{
				SchemaID: schemaID,
				Scope:    scope,
				Value:    value,
			}

			if validateOnly {
				_, c, err := setupClient(cmdContext(cmd))
				if err != nil {
					return err
				}
				handler := settings.NewHandler(c)
				if err := handler.ValidateCreate(req); err != nil {
					return fmt.Errorf("validation failed: %w", err)
				}
				output.FprintSuccess(currentStderr(cmdContext(cmd)), "Validation passed")
				return nil
			}

			_, c, err := setupWithSafety(cmdContext(cmd), safety.OperationCreate)
			if err != nil {
				return err
			}

			handler := settings.NewHandler(c)

			result, err := handler.Create(req)
			if err != nil {
				return fmt.Errorf("failed to create settings object: %w", err)
			}

			output.FprintSuccess(currentStderr(cmdContext(cmd)), "Settings object %q created", result.ObjectID)
			return nil
		},
	}
	c.Flags().StringP("file", "f", "", "file containing settings value, or - for stdin (required)")
	c.Flags().String("schema", "", "schema ID (required)")
	c.Flags().String("scope", "", "scope for the settings object (required)")
	c.Flags().StringArray("set", []string{}, "set template variable (key=value)")
	c.Flags().Bool("validate-only", false, "validate the settings object against the API without creating it")
	stability.MarkStable(c)
	markFlagRequiredNonEmpty(c, "file")
	markFlagRequiredNonEmpty(c, "schema")
	markFlagRequiredNonEmpty(c, "scope")
	return c
}

// Declared stable: the invocation and output contract of this command is
// additive-only. Stable is never implied -- see AGENTS.md "Stability Tiers".
