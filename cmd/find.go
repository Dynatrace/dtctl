package cmd

import (
	"github.com/spf13/cobra"

	"github.com/dynatrace-oss/dtctl/pkg/stability"
)

// findCmd represents the find command
var findCmd = newFindCmd()

func newFindCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "find",
		Short: "Find resources based on criteria",
		Long: `Find Dynatrace resources that match specific criteria.

Searches across resources using platform-level matching rather than simple
listing. Use 'dtctl get' to list all resources of a type, and 'dtctl find'
when you need to match against specific data or capabilities.

Available finders:
  intents                 Find app intents that match given input data`,
		Example: `  # Find intents that can handle specific data
  dtctl find intents --app-id <app-id>

  # Find intents with JSON output
  dtctl find intents --app-id <app-id> -o json

  # Find intents matching a payload
  dtctl find intents --data '{"key": "value"}'`,
		RunE: requireSubcommand,
	}
	stability.MarkStable(c)
	return c
}

func init() {
	rootCmd.AddCommand(findCmd)
	findCmd.AddCommand(findIntentsCmd)
}

// Declared stable: the invocation and output contract of this command is
// additive-only. Stable is never implied -- see AGENTS.md "Stability Tiers".
func init() {
}
