package cmd

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/dynatrace-oss/dtctl/pkg/resources/extension"
	"github.com/dynatrace-oss/dtctl/pkg/stability"
)

// getExtensionsCmd retrieves Extensions 2.0 extensions
var getExtensionsCmd = newGetExtensionsCmd()

func newGetExtensionsCmd() *cobra.Command {
	c := &cobra.Command{
		Use:     "extensions [name]",
		Aliases: []string{"extension", "ext", "exts"},
		Short:   "Get Extensions 2.0 extensions",
		Long: `Get Extensions 2.0 extensions.

Examples:
  # List all extensions
  dtctl get extensions

  # List extensions matching a name
  dtctl get extensions --name "com.dynatrace"

  # Get versions of a specific extension
  dtctl get extension com.dynatrace.extension.host-monitoring

  # Output as JSON
  dtctl get extensions -o json
`,
		RunE: func(cmd *cobra.Command, args []string) error {
			name, _ := cmd.Flags().GetString("name")

			_, c, printer, err := setup(cmdContext(cmd))
			if err != nil {
				return err
			}

			handler := extension.NewHandler(c)

			// Get specific extension versions if name provided as argument
			if len(args) > 0 {
				versions, err := handler.Get(args[0])
				if err != nil {
					return err
				}
				return printer.PrintList(versions.Items)
			}

			list, err := handler.List(cmd.Context(), name, getChunkSize(cmdContext(cmd)))
			if err != nil {
				return err
			}

			return printer.PrintList(list.Items)
		},
	}
	c.Flags().String("name", "", "Filter extensions by name")
	stability.MarkStable(c)
	return c
}

// getExtensionConfigsCmd retrieves monitoring configurations for an extension
var getExtensionConfigsCmd = newGetExtensionConfigsCmd()

func newGetExtensionConfigsCmd() *cobra.Command {
	c := &cobra.Command{
		Use:     "extension-configs <extension-name>",
		Aliases: []string{"extension-config", "ext-configs", "ext-config"},
		Short:   "Get monitoring configurations for an extension",
		Long: `Get monitoring configurations for an Extensions 2.0 extension.

Examples:
  # List all monitoring configurations for an extension
  dtctl get extension-configs com.dynatrace.extension.host-monitoring

  # Get a specific monitoring configuration
  dtctl get extension-config com.dynatrace.extension.host-monitoring --config-id <config-id>

  # Output as JSON
  dtctl get extension-configs com.dynatrace.extension.host-monitoring -o json
`,
		// Not ExactArgs: evals showed agents calling this bare 8×/batch and getting
		// the unhelpful "accepts 1 arg(s), received 0".
		Args: func(cmd *cobra.Command, args []string) error {
			if len(args) != 1 {
				return fmt.Errorf("extension-configs requires the extension name — list installed extensions first: dtctl get extensions")
			}
			return nil
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			extensionName := args[0]
			configID, _ := cmd.Flags().GetString("config-id")
			version, _ := cmd.Flags().GetString("version")

			_, c, printer, err := setup(cmdContext(cmd))
			if err != nil {
				return err
			}

			handler := extension.NewHandler(c)

			// Get specific monitoring configuration if config ID provided
			if configID != "" {
				config, err := handler.GetMonitoringConfiguration(extensionName, configID)
				if err != nil {
					return err
				}
				return printer.Print(config)
			}

			// List all monitoring configurations
			list, err := handler.ListMonitoringConfigurations(extensionName, version, getChunkSize(cmdContext(cmd)))
			if err != nil {
				return err
			}

			return printer.PrintList(list.Items)
		},
	}
	c.Flags().String("config-id", "", "Get a specific monitoring configuration by ID")
	c.Flags().String("version", "", "Filter configs by extension version")
	stability.MarkStable(c)
	return c
}

func init() {
	// Extension flags
	// Extension config flags
}

// Declared stable: the invocation and output contract of these commands is
// additive-only. Stable is never implied -- see AGENTS.md "Stability Tiers".
func init() {
}
