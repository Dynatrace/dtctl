package cmd

import (
	"github.com/spf13/cobra"

	"github.com/dynatrace-oss/dtctl/pkg/resources/hub"
	"github.com/dynatrace-oss/dtctl/pkg/stability"
)

// getHubExtensionsCmd retrieves Hub catalog extensions
var getHubExtensionsCmd = newGetHubExtensionsCmd()

func newGetHubExtensionsCmd() *cobra.Command {
	c := &cobra.Command{
		Use:     "hub-extensions [id]",
		Aliases: []string{"hub-extension"},
		Short:   "Get Dynatrace Hub catalog extensions",
		Long: `Get Dynatrace Hub catalog extensions.

Examples:
  # List all Hub extensions
  dtctl get hub-extensions

  # Filter by name, ID, or description (case-insensitive substring)
  dtctl get hub-extensions --filter kafka

  # Get a specific Hub extension by ID
  dtctl get hub-extensions my-extension-id

  # Output as JSON
  dtctl get hub-extensions -o json
`,
		RunE: func(cmd *cobra.Command, args []string) error {
			filter, _ := cmd.Flags().GetString("filter")

			_, c, printer, err := setup(cmdContext(cmd))
			if err != nil {
				return err
			}

			handler := hub.NewHandler(c)

			if len(args) > 0 {
				ext, err := handler.GetExtension(args[0])
				if err != nil {
					return err
				}
				ap := enrichAgent(printer, "get", "hub-extension")
				if ap != nil {
					ap.SetSuggestions([]string{
						"dtctl describe hub-extensions " + args[0] + " -- view full extension details",
						"dtctl get hub-extension-releases " + args[0] + " -- list available releases",
						"dtctl get hub-extensions -- list all Hub extensions",
					})
				}
				return printer.Print(ext)
			}

			list, err := handler.ListExtensions(filter, getChunkSize(cmdContext(cmd)))
			if err != nil {
				return err
			}
			ap := enrichAgent(printer, "get", "hub-extension")
			if ap != nil {
				ap.SetTotal(len(list.Items))
				ap.Context().Suggestions = []string{
					"dtctl get hub-extensions --filter <keyword> -- filter by name, ID, or description",
					"dtctl describe hub-extensions <id> -- view full extension details",
					"dtctl get hub-extension-releases <id> -- list releases for an extension",
				}
			}
			return printer.PrintList(list.Items)
		},
	}
	c.Flags().String("filter", "", "Filter by name, ID, or description (case-insensitive substring)")
	stability.MarkStable(c)
	return c
}

// getHubExtensionReleasesCmd retrieves releases for a Hub catalog extension
var getHubExtensionReleasesCmd = newGetHubExtensionReleasesCmd()

func newGetHubExtensionReleasesCmd() *cobra.Command {
	c := &cobra.Command{
		Use:     "hub-extension-releases <id>",
		Aliases: []string{"hub-extension-release"},
		Short:   "Get releases for a Dynatrace Hub extension",
		Long: `Get releases for a Dynatrace Hub catalog extension.

Examples:
  # List all releases for a Hub extension
  dtctl get hub-extension-releases my-extension-id

  # Output as JSON
  dtctl get hub-extension-releases my-extension-id -o json
`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			id := args[0]

			_, c, printer, err := setup(cmdContext(cmd))
			if err != nil {
				return err
			}

			handler := hub.NewHandler(c)

			list, err := handler.ListExtensionReleases(id, getChunkSize(cmdContext(cmd)))
			if err != nil {
				return err
			}
			ap := enrichAgent(printer, "get", "hub-extension-release")
			if ap != nil {
				ap.SetTotal(len(list.Items))
				ap.Context().Suggestions = []string{
					"dtctl describe hub-extensions " + id + " -- view full extension details",
					"dtctl get hub-extensions -- list all Hub extensions",
				}
			}
			return printer.PrintList(list.Items)
		},
	}
	stability.MarkStable(c)
	return c
}

func init() {
}

// Declared stable: the invocation and output contract of these commands is
// additive-only. Stable is never implied -- see AGENTS.md "Stability Tiers".
func init() {
}
