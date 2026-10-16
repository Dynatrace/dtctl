package cmd

import (
	"encoding/json"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/dynatrace-oss/dtctl/pkg/output"
	"github.com/dynatrace-oss/dtctl/pkg/resources/extension"
	"github.com/dynatrace-oss/dtctl/pkg/stability"
)

// describeExtensionConfigCmd shows detailed info about an extension monitoring configuration
var describeExtensionConfigCmd = newDescribeExtensionConfigCmd()

func newDescribeExtensionConfigCmd() *cobra.Command {
	c := &cobra.Command{
		Use:     "extension-config <extension-name> --config-id <config-id>",
		Aliases: []string{"ext-config"},
		Short:   "Show details of an extension monitoring configuration",
		Long: `Show detailed information about an Extensions 2.0 monitoring configuration
including scope, enabled status, version, feature sets, and full value.

Examples:
  # Describe a specific monitoring configuration
  dtctl describe extension-config com.dynatrace.extension.host-monitoring --config-id <config-id>
`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			extensionName := args[0]
			configID, _ := cmd.Flags().GetString("config-id")

			_, c, printer, err := setup(cmdContext(cmd))
			if err != nil {
				return err
			}

			handler := extension.NewHandler(c)

			config, err := handler.GetMonitoringConfiguration(extensionName, configID)
			if err != nil {
				return err
			}

			// For table output, show detailed human-readable information
			if outputFormat(cmdContext(cmd)) == "table" {
				const w = 13
				output.FprintDescribeKV(currentStdout(cmdContext(cmd)), "Extension:", w, "%s", extensionName)
				output.FprintDescribeKV(currentStdout(cmdContext(cmd)), "Config ID:", w, "%s", config.ObjectID)
				if config.Scope != "" {
					output.FprintDescribeKV(currentStdout(cmdContext(cmd)), "Scope:", w, "%s", config.Scope)
				}

				if len(config.Value) > 0 {
					var val map[string]interface{}
					if err := json.Unmarshal(config.Value, &val); err == nil {
						if enabled, ok := val["enabled"]; ok {
							output.FprintDescribeKV(currentStdout(cmdContext(cmd)), "Enabled:", w, "%v", enabled)
						}
						if desc, ok := val["description"]; ok && desc != "" {
							output.FprintDescribeKV(currentStdout(cmdContext(cmd)), "Description:", w, "%s", desc)
						}
						if version, ok := val["version"]; ok && version != "" {
							output.FprintDescribeKV(currentStdout(cmdContext(cmd)), "Version:", w, "%s", version)
						}
						if fs, ok := val["featureSets"].([]interface{}); ok && len(fs) > 0 {
							fmt.Fprintln(currentStdout(cmdContext(cmd)))
							output.FprintDescribeSection(currentStdout(cmdContext(cmd)), "Feature Sets:")
							for _, f := range fs {
								fmt.Fprintf(currentStdout(cmdContext(cmd)), "  - %v\n", f)
							}
						}
					}

					fmt.Fprintln(currentStdout(cmdContext(cmd)))
					output.FprintDescribeSection(currentStdout(cmdContext(cmd)), "Value:")
					valueJSON, err := json.MarshalIndent(json.RawMessage(config.Value), "  ", "  ")
					if err == nil {
						fmt.Fprintf(currentStdout(cmdContext(cmd)), "  %s\n", string(valueJSON))
					}
				}
				return nil
			}

			// For other formats (JSON, YAML, etc.), use the printer
			enrichAgent(printer, "describe", "extension-config")
			return printer.Print(config)
		},
	}
	c.Flags().String("config-id", "", "Monitoring configuration ID (required)")
	stability.MarkStable(c)
	markFlagRequiredNonEmpty(c, "config-id")
	return c
}

// Declared stable: the invocation and output contract of this command is
// additive-only. Stable is never implied -- see AGENTS.md "Stability Tiers".
