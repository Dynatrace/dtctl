package cmd

import (
	"github.com/spf13/cobra"

	"github.com/dynatrace-oss/dtctl/pkg/stability"
)

var disableCmd = newDisableCmd()

func newDisableCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "disable",
		Short: "Disable cloud monitoring configurations",
		Long: `Disable a cloud monitoring configuration by setting it and all its credentials
to disabled in a single step.

This is the inverse of 'dtctl enable'. The monitoring configuration and linked
connection are preserved — only the enabled flag is toggled off.

Available resources:
  aws monitoring          Disable AWS monitoring configuration
  azure monitoring        Disable Azure monitoring configuration
  gcp monitoring          Disable GCP monitoring configuration (Preview)`,
		Example: `  # Disable an AWS monitoring configuration by name
  dtctl disable aws monitoring --name "my-aws-monitoring"

  # Disable a GCP monitoring configuration by ID
  dtctl disable gcp monitoring <id>

  # Disable an Azure monitoring configuration
  dtctl disable azure monitoring --name "my-azure-monitoring"`,
		RunE: requireSubcommand,
	}
	stability.MarkStable(c)
	return c
}

func init() {
	rootCmd.AddCommand(disableCmd)
}

// Declared stable: the invocation and output contract of this command is
// additive-only. Stable is never implied -- see AGENTS.md "Stability Tiers".
func init() {
}
