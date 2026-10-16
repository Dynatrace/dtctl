package cmd

import (
	"encoding/json"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/dynatrace-oss/dtctl/pkg/apply"
	"github.com/dynatrace-oss/dtctl/pkg/resources/extension"
	"github.com/dynatrace-oss/dtctl/pkg/safety"
	"github.com/dynatrace-oss/dtctl/pkg/stability"
	"github.com/dynatrace-oss/dtctl/pkg/util/format"
	"github.com/dynatrace-oss/dtctl/pkg/util/template"
)

// applyExtensionConfigCmd creates or updates a monitoring configuration for an extension
var applyExtensionConfigCmd = newApplyExtensionConfigCmd()

func newApplyExtensionConfigCmd() *cobra.Command {
	c := &cobra.Command{
		Use:     "extension-config <extension-name> -f <file>",
		Aliases: []string{"ext-config"},
		Short:   "Apply a monitoring configuration for an extension",
		Long: `Apply a monitoring configuration for an Extensions 2.0 extension from a YAML or JSON file.

The file should contain the full monitoring configuration object, including scope and value fields.
The --scope flag overrides any scope set in the file.

How it works:
  - If the file contains an objectId field: UPDATE the existing configuration
  - If the file has no objectId field: CREATE a new configuration

Examples:
  # Create a new monitoring configuration
  dtctl apply extension-config com.dynatrace.extension.host-monitoring -f config.yaml

  # Create with a specific scope
  dtctl apply extension-config com.dynatrace.extension.host-monitoring -f config.yaml --scope HOST-1234

  # Update an existing configuration (objectId in file)
  dtctl apply extension-config com.dynatrace.extension.host-monitoring -f config.yaml

  # Apply with template variables
  dtctl apply extension-config com.dynatrace.extension.host-monitoring -f config.yaml --set env=prod

  # Dry run to preview
  dtctl apply extension-config com.dynatrace.extension.host-monitoring -f config.yaml --dry-run
`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			extensionName := args[0]
			file, _ := cmd.Flags().GetString("file")
			scope, _ := cmd.Flags().GetString("scope")
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

			// Parse the full monitoring configuration (scope + value)
			var config extension.MonitoringConfigurationCreate
			if err := json.Unmarshal(jsonData, &config); err != nil {
				return fmt.Errorf("failed to parse configuration: %w", err)
			}

			// Override scope from flag if provided
			if scope != "" {
				config.Scope = scope
			}

			// Determine if this is a create or update by checking for objectId
			var configID string
			var raw map[string]any
			if err := json.Unmarshal(jsonData, &raw); err == nil {
				if id, ok := raw["objectId"].(string); ok && id != "" {
					configID = id
				}
			}
			isUpdate := configID != ""

			// Handle dry-run
			if dryRun(cmdContext(cmd)) {
				report := newDryRunReport(cmd)
				if isUpdate {
					report.
						Linef("Dry run: would update extension monitoring configuration").
						Field("Config ID", "%s", configID)
				} else {
					report.Linef("Dry run: would create extension monitoring configuration")
				}
				report.Field("Extension", "%s", extensionName)
				if config.Scope != "" {
					// Padded to align with the line above it, so the field is recorded
					// separately rather than reformatting what a human sees.
					report.Linef("Scope:     %s", config.Scope).Detail("scope", "%s", config.Scope)
				}
				return report.
					Linef("---").
					Linef("%s", string(jsonData)).
					Linef("---").
					Payload(jsonData).
					Print()
			}

			// Determine if this is a create or update
			operation := safety.OperationCreate
			if isUpdate {
				operation = safety.OperationUpdate
			}

			_, c, err := setupWithSafety(cmdContext(cmd), operation)
			if err != nil {
				return err
			}

			handler := extension.NewHandler(c)
			printer := newPrinterCtx(cmdContext(cmd))

			if isUpdate {
				result, err := handler.UpdateMonitoringConfiguration(extensionName, configID, config)
				if err != nil {
					return fmt.Errorf("failed to update monitoring configuration: %w", err)
				}
				return printer.Print(&apply.ExtensionConfigApplyResult{
					ApplyResultBase: apply.ApplyResultBase{
						Action:       apply.ActionUpdated,
						ResourceType: "extension_config",
						ID:           result.ObjectID,
					},
					ExtensionName: extensionName,
					Scope:         result.Scope,
				})
			}

			result, err := handler.CreateMonitoringConfiguration(extensionName, config)
			if err != nil {
				return fmt.Errorf("failed to create monitoring configuration: %w", err)
			}
			return printer.Print(&apply.ExtensionConfigApplyResult{
				ApplyResultBase: apply.ApplyResultBase{
					Action:       apply.ActionCreated,
					ResourceType: "extension_config",
					ID:           result.ObjectID,
				},
				ExtensionName: extensionName,
				Scope:         result.Scope,
			})
		},
	}
	c.Flags().StringP("file", "f", "", "file containing the monitoring configuration (scope + value), or - for stdin (required)")
	c.Flags().String("scope", "", "scope for the monitoring configuration (e.g. HOST-1234, only for create)")
	c.Flags().StringArray("set", []string{}, "set template variable (key=value)")
	stability.MarkStable(c)
	markFlagRequiredNonEmpty(c, "file")
	return c
}

func init() {
	applyCmd.AddCommand(applyExtensionConfigCmd)
}

// Declared stable: the invocation and output contract of this command is
// additive-only. Stable is never implied -- see AGENTS.md "Stability Tiers".
