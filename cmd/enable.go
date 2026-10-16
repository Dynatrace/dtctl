package cmd

import (
	"github.com/spf13/cobra"

	"github.com/dynatrace-oss/dtctl/pkg/stability"
)

var enableCmd = newEnableCmd()

func newEnableCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "enable",
		Short: "Enable cloud monitoring configurations",
		Long: `Enable a cloud monitoring configuration by optionally updating the connection
credentials and enabling the monitoring config in a single step.

This is a convenience command that combines two operations:
  1. Updates the cloud connection with authentication details (service account, directory/app ID) — optional
  2. Enables the monitoring configuration and its credentials

Available resources:
  gcp monitoring          Enable GCP monitoring configuration (Preview)
  azure monitoring        Enable Azure monitoring configuration`,
		Example: `  # Enable GCP monitoring with service account
  dtctl enable gcp monitoring --name "my-gcp-monitoring" --serviceAccountId "sa@project.iam.gserviceaccount.com"

  # Enable Azure monitoring with federated identity
  dtctl enable azure monitoring --name "my-azure-monitoring" --directoryId "$TENANT_ID" --applicationId "$CLIENT_ID"

  # Enable monitoring without updating connection credentials
  dtctl enable gcp monitoring --name "my-gcp-monitoring"`,
		RunE: requireSubcommand,
	}
	stability.MarkStable(c)
	return c
}

func init() {
	rootCmd.AddCommand(enableCmd)
}

// Declared stable: the invocation and output contract of this command is
// additive-only. Stable is never implied -- see AGENTS.md "Stability Tiers".
func init() {
}
