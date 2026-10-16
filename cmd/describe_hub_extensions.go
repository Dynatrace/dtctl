package cmd

import (
	"github.com/spf13/cobra"

	"github.com/dynatrace-oss/dtctl/pkg/resources/hub"
	"github.com/dynatrace-oss/dtctl/pkg/stability"
)

// describeHubExtensionCmd shows detailed info about a Hub catalog extension
var describeHubExtensionCmd = newDescribeHubExtensionCmd()

func newDescribeHubExtensionCmd() *cobra.Command {
	c := &cobra.Command{
		Use:     "hub-extensions <id>",
		Aliases: []string{"hub-extension"},
		Short:   "Show details of a Dynatrace Hub extension",
		Long: `Show detailed information about a Dynatrace Hub catalog extension.

Examples:
  # Describe a Hub extension
  dtctl describe hub-extensions my-extension-id

  # Output as YAML
  dtctl describe hub-extensions my-extension-id -o yaml
`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			id := args[0]

			_, c, printer, err := setup(cmdContext(cmd))
			if err != nil {
				return err
			}

			handler := hub.NewHandler(c)

			ext, err := handler.GetExtension(id)
			if err != nil {
				return err
			}
			ap := enrichAgent(printer, "describe", "hub-extension")
			if ap != nil {
				ap.SetSuggestions([]string{
					"dtctl get hub-extension-releases " + id + " -- list available releases",
					"dtctl get hub-extensions -- list all Hub extensions",
				})
			}
			return printer.Print(ext)
		},
	}
	stability.MarkStable(c)
	return c
}

// Declared stable: the invocation and output contract of this command is
// additive-only. Stable is never implied -- see AGENTS.md "Stability Tiers".
func init() {
}
