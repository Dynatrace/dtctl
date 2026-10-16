package cmd

import (
	"encoding/json"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/dynatrace-oss/dtctl/pkg/output"
	"github.com/dynatrace-oss/dtctl/pkg/resources/settings"
	"github.com/dynatrace-oss/dtctl/pkg/stability"
)

// describeSettingsCmd shows detailed info about a settings object
var describeSettingsCmd = newDescribeSettingsCmd()

func newDescribeSettingsCmd() *cobra.Command {
	c := &cobra.Command{
		Use:     "settings <object-id>",
		Aliases: []string{"setting", "set"},
		Short:   "Show details of a settings object",
		Long: `Show detailed information about a settings object including its value, scope, and metadata.

Examples:
  # Describe a settings object by objectId
  dtctl describe settings vu9U3hXa3q0AAAABABlidWlsdGluOnJ1bS5mcm9...
`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			objectID := args[0]

			_, c, printer, err := setup(cmdContext(cmd))
			if err != nil {
				return err
			}

			handler := settings.NewHandler(c)

			obj, err := handler.Get(objectID)
			if err != nil {
				return err
			}

			// For table output, show detailed human-readable information
			if outputFormat(cmdContext(cmd)) == "table" {
				const w = 14
				output.FprintDescribeKV(currentStdout(cmdContext(cmd)), "Object ID:", w, "%s", obj.ObjectID)
				output.FprintDescribeKV(currentStdout(cmdContext(cmd)), "Schema ID:", w, "%s", obj.SchemaID)
				if obj.SchemaVersion != "" {
					output.FprintDescribeKV(currentStdout(cmdContext(cmd)), "Version:", w, "%s", obj.SchemaVersion)
				}
				output.FprintDescribeKV(currentStdout(cmdContext(cmd)), "Scope:", w, "%s", obj.Scope)
				if obj.ScopeType != "" {
					output.FprintDescribeKV(currentStdout(cmdContext(cmd)), "Scope Type:", w, "%s", obj.ScopeType)
				}
				if obj.ScopeID != "" {
					output.FprintDescribeKV(currentStdout(cmdContext(cmd)), "Scope ID:", w, "%s", obj.ScopeID)
				}
				if obj.ExternalID != "" {
					output.FprintDescribeKV(currentStdout(cmdContext(cmd)), "External ID:", w, "%s", obj.ExternalID)
				}
				if obj.Summary != "" {
					output.FprintDescribeKV(currentStdout(cmdContext(cmd)), "Summary:", w, "%s", obj.Summary)
				}

				// Print modification info
				if obj.ModificationInfo != nil {
					fmt.Fprintln(currentStdout(cmdContext(cmd)))
					if obj.ModificationInfo.CreatedTime != "" {
						suffix := ""
						if obj.ModificationInfo.CreatedBy != "" {
							suffix = fmt.Sprintf(" (by %s)", obj.ModificationInfo.CreatedBy)
						}
						output.FprintDescribeKV(currentStdout(cmdContext(cmd)), "Created:", w, "%s%s", obj.ModificationInfo.CreatedTime, suffix)
					}
					if obj.ModificationInfo.LastModifiedTime != "" {
						suffix := ""
						if obj.ModificationInfo.LastModifiedBy != "" {
							suffix = fmt.Sprintf(" (by %s)", obj.ModificationInfo.LastModifiedBy)
						}
						output.FprintDescribeKV(currentStdout(cmdContext(cmd)), "Modified:", w, "%s%s", obj.ModificationInfo.LastModifiedTime, suffix)
					}
				}

				// Print value as JSON
				if len(obj.Value) > 0 {
					fmt.Fprintln(currentStdout(cmdContext(cmd)))
					output.FprintDescribeSection(currentStdout(cmdContext(cmd)), "Value:")
					valueJSON, err := json.MarshalIndent(obj.Value, "  ", "  ")
					if err == nil {
						fmt.Fprintf(currentStdout(cmdContext(cmd)), "  %s\n", string(valueJSON))
					}
				}

				return nil
			}

			// For other formats, use standard printer
			enrichAgent(printer, "describe", "settings")
			return printer.Print(obj)
		},
	}
	stability.MarkStable(c)
	return c
}

// describeSettingsSchemaCmd shows detailed info about a settings schema
var describeSettingsSchemaCmd = newDescribeSettingsSchemaCmd()

func newDescribeSettingsSchemaCmd() *cobra.Command {
	c := &cobra.Command{
		Use:     "settings-schema <schema-id>",
		Aliases: []string{"schema"},
		Short:   "Show details of a settings schema",
		Long: `Show detailed information about a settings schema including properties and validation rules.

Examples:
  # Describe a settings schema
  dtctl describe settings-schema builtin:openpipeline.logs.pipelines
  dtctl describe schema builtin:anomaly-detection.infrastructure
`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			schemaID := args[0]

			_, c, printer, err := setup(cmdContext(cmd))
			if err != nil {
				return err
			}

			handler := settings.NewHandler(c)

			schema, err := handler.GetSchema(schemaID)
			if err != nil {
				return err
			}

			// For table output, show detailed human-readable information
			if outputFormat(cmdContext(cmd)) == "table" {
				const w = 18
				if schemaID, ok := schema["schemaId"].(string); ok {
					output.FprintDescribeKV(currentStdout(cmdContext(cmd)), "Schema ID:", w, "%s", schemaID)
				}
				if displayName, ok := schema["displayName"].(string); ok {
					output.FprintDescribeKV(currentStdout(cmdContext(cmd)), "Display Name:", w, "%s", displayName)
				}
				if description, ok := schema["description"].(string); ok && description != "" {
					output.FprintDescribeKV(currentStdout(cmdContext(cmd)), "Description:", w, "%s", description)
				}
				if version, ok := schema["version"].(string); ok {
					output.FprintDescribeKV(currentStdout(cmdContext(cmd)), "Version:", w, "%s", version)
				}
				if multiObj, ok := schema["multiObject"].(bool); ok {
					output.FprintDescribeKV(currentStdout(cmdContext(cmd)), "Multi-Object:", w, "%v", multiObj)
				}
				if ordered, ok := schema["ordered"].(bool); ok {
					output.FprintDescribeKV(currentStdout(cmdContext(cmd)), "Ordered:", w, "%v", ordered)
				}

				// Print properties if available
				if properties, ok := schema["properties"].(map[string]any); ok && len(properties) > 0 {
					fmt.Fprintln(currentStdout(cmdContext(cmd)))
					output.FprintDescribeKV(currentStdout(cmdContext(cmd)), "Properties:", w, "%d defined", len(properties))
				}

				// Print scopes if available
				if scopesRaw, ok := schema["scopes"].([]any); ok && len(scopesRaw) > 0 {
					fmt.Fprintln(currentStdout(cmdContext(cmd)))
					output.FprintDescribeSection(currentStdout(cmdContext(cmd)), "Scopes:")
					for _, s := range scopesRaw {
						if scope, ok := s.(string); ok {
							fmt.Fprintf(currentStdout(cmdContext(cmd)), "  - %s\n", scope)
						}
					}
				}

				return nil
			}

			// For other formats, use standard printer
			enrichAgent(printer, "describe", "settings-schema")
			return printer.Print(schema)
		},
	}
	stability.MarkStable(c)
	return c
}

// Declared stable: the invocation and output contract of these commands is
// additive-only. Stable is never implied -- see AGENTS.md "Stability Tiers".
func init() {
}
