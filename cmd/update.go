package cmd

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/dynatrace-oss/dtctl/pkg/stability"
)

// updateCmd represents the update command.
var updateCmd = newUpdateCmd()

func newUpdateCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "update",
		Short: "Update resources",
		Long: `Update existing resources on the Dynatrace platform using flags.

Unlike 'apply' which works from YAML/JSON files, 'update' modifies individual
fields of a resource using command-line flags. This is useful for quick,
targeted changes without exporting and re-importing a full resource definition.

Most 'update' subcommands modify individual fields via flags. The 'document'
subcommand is file-based instead: it updates an existing document of any type
from a YAML/JSON file (the update-only counterpart to 'create document').

Available resources:
  document                Update an existing document of any type from a file
  breakpoint              Update breakpoint condition/enabled state or workspace filters
  extension               Activate a specific, latest-installed, or Hub-latest version of an extension
  extensions              Bulk-upgrade all installed extensions (requires --all)
  azure connection        Update Azure connection credentials
  azure monitoring        Update Azure monitoring configuration
  gcp connection          Update GCP connection credentials (Preview)
  gcp monitoring          Update GCP monitoring configuration (Preview)`,
		Example: `  # Update a custom document type (round-trip from 'get')
  dtctl update document -f doc.json --type acme:config --id acme-config

  # Update an Azure connection
  dtctl update azure connection <id> --name "New Name"

  # Update Live Debugger workspace filters
  dtctl update breakpoint --filters k8s.namespace.name:prod

  # Update an Azure monitoring config
  dtctl update azure monitoring <id> --enabled=false

  # Update a GCP connection
  dtctl update gcp connection <id> --project-id my-project`,
		RunE: requireSubcommand,
	}
	stability.MarkStable(c)
	return c
}

// updateSettingsHintCmd redirects users to 'apply' for file-based settings updates.
// Hidden, and all it does is redirect to `apply`; that redirect is the
// contract, and it is stable.
var updateSettingsHintCmd = newUpdateSettingsHintCmd()

func newUpdateSettingsHintCmd() *cobra.Command {
	c := &cobra.Command{
		Use:     "settings",
		Aliases: []string{"setting"},
		Short:   "Update a settings object (use 'apply' instead)",
		Hidden:  true,
		// Accept any args/flags so the command doesn't fail before RunE.
		Args:               cobra.ArbitraryArgs,
		DisableFlagParsing: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			return fmt.Errorf("to update settings objects from a file, use 'dtctl apply -f <file>' instead\n\n" +
				"The file should include objectId, schemaId, scope, and value fields.\n" +
				"If the objectId exists it will be updated; otherwise a new object is created.\n\n" +
				"Example:\n" +
				"  dtctl apply -f settings.yaml\n" +
				"  dtctl apply -f settings.yaml --dry-run")
		},
	}
	stability.MarkStable(c)
	return c
}

func init() {
	rootCmd.AddCommand(updateCmd)
	updateCmd.AddCommand(updateDocumentCmd)
	updateCmd.AddCommand(updateSettingsHintCmd)
}

// Declared stable: the invocation and output contract of this command is
// additive-only. Stable is never implied -- see AGENTS.md "Stability Tiers".
func init() {
}

func init() {
}
