package cmd

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/dynatrace-oss/dtctl/pkg/output"
	"github.com/dynatrace-oss/dtctl/pkg/resources/azureconnection"
	"github.com/dynatrace-oss/dtctl/pkg/resources/azuremonitoringconfig"
	"github.com/dynatrace-oss/dtctl/pkg/safety"
	"github.com/dynatrace-oss/dtctl/pkg/stability"
)

// deleteCmd represents the delete command
var deleteCmd = newDeleteCmd()

func newDeleteCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "delete",
		Short: "Delete resources",
		Long: `Delete a resource from the Dynatrace platform by ID or name.

Accepts either a resource ID or name. When a name is provided, dtctl resolves
it to an ID automatically. Blocked by safety level if the current context is
set to 'readonly'.

Some resources (dashboards, notebooks) may be recoverable from trash after
deletion — use 'dtctl get trash' to check.

Supported resources:
  workflows (wf)          dashboards (dash, db)     notebooks (nb)
  slos                    settings                  buckets (bkt)
  apps                    edgeconnect (ec)          notifications
  lookup-tables (lu)      trash                     segments (seg)
  anomaly-detectors (ad)  azure connection          azure monitoring`,
		Example: `  # Delete a workflow by ID
  dtctl delete workflow abc-123

  # Delete a dashboard by name
  dtctl delete dashboard "My Dashboard"

  # Delete with dry-run to preview what would be deleted
  dtctl delete workflow abc-123 --dry-run

  # Permanently remove a trashed document
  dtctl delete trash <document-id>`,
		RunE: requireSubcommand,
	}
	stability.MarkStable(c)
	return c
}

var deleteAzureProviderCmd = newDeleteAzureProviderCmd()

func newDeleteAzureProviderCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "azure",
		Short: "Delete Azure resources",
		RunE:  requireSubcommand,
	}
	stability.MarkStable(c)
	return c
}

var deleteAWSProviderCmd = newDeleteAWSProviderCmd()

func newDeleteAWSProviderCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "aws",
		Short: "Delete AWS resources",
		RunE:  requireSubcommand,
	}
	stability.MarkStable(c)
	return c
}

var deleteGCPProviderCmd = newDeleteGCPProviderCmd()

func newDeleteGCPProviderCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "gcp",
		Short: "Delete GCP resources (Preview)",
		RunE:  requireSubcommand,
	}
	stability.MarkStable(c)
	attachPreviewNotice(c, "GCP")
	return c
}

var deleteAzureConnectionCmd = newDeleteAzureConnectionCmd()

func newDeleteAzureConnectionCmd() *cobra.Command {
	c := &cobra.Command{
		Use:     "connection [ID|NAME]",
		Short:   "Delete an Azure connection",
		Aliases: []string{"connections"},
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			identifier := args[0]

			_, client, err := setupWithSafety(cmdContext(cmd), safety.OperationDelete)
			if err != nil {
				return err
			}

			handler := azureconnection.NewHandler(client)

			objectID := identifier

			// Try to find by name first to resolve ID if it's a name
			item, err := handler.FindByName(identifier)
			if err == nil {
				// Found by name
				objectID = item.ObjectID
				output.FprintInfo(currentStderr(cmdContext(cmd)), "Resolved name %q to ID %s", identifier, objectID)
			}
			// If not found by name, assume identifier is an ID

			if dryRun(cmdContext(cmd)) {
				return deleteDryRun(cmd, "Azure connection", identifier, objectID)
			}

			if err := handler.Delete(objectID); err != nil {
				return fmt.Errorf("failed to delete Azure connection %q: %w", objectID, err)
			}

			output.FprintSuccess(currentStderr(cmdContext(cmd)), "Azure connection %s deleted", objectID)
			return nil
		},
	}
	stability.MarkStable(c)
	return c
}

var deleteAzureMonitoringConfigCmd = newDeleteAzureMonitoringConfigCmd()

func newDeleteAzureMonitoringConfigCmd() *cobra.Command {
	c := &cobra.Command{
		Use:     "monitoring [ID|NAME]",
		Short:   "Delete an Azure monitoring config",
		Aliases: []string{"monitoring-config", "monitoring-configs"},
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			identifier := args[0]

			_, client, err := setupWithSafety(cmdContext(cmd), safety.OperationDelete)
			if err != nil {
				return err
			}

			handler := azuremonitoringconfig.NewHandler(client)

			objectID := identifier

			// Try to find by description first to resolve ID if it's a name
			item, err := handler.FindByName(identifier)
			if err == nil {
				// Found by name
				objectID = item.ObjectID
				output.FprintInfo(currentStderr(cmdContext(cmd)), "Resolved name %q to ID %s", identifier, objectID)
			}
			// If not found by name, assume identifier is an ID

			if dryRun(cmdContext(cmd)) {
				return deleteDryRun(cmd, "Azure monitoring config", identifier, objectID)
			}

			if err := handler.Delete(objectID); err != nil {
				return fmt.Errorf("failed to delete Azure monitoring config %q: %w", objectID, err)
			}

			output.FprintSuccess(currentStderr(cmdContext(cmd)), "Azure monitoring config %s deleted", objectID)
			return nil
		},
	}
	stability.MarkStable(c)
	return c
}

func init() {
	rootCmd.AddCommand(deleteCmd)

	// Resource delete subcommands (command definitions live in get_*.go files)
	deleteCmd.AddCommand(deleteWorkflowCmd)
	deleteCmd.AddCommand(deleteSchedulingRuleCmd)
	deleteCmd.AddCommand(deleteDashboardCmd)
	deleteCmd.AddCommand(deleteNotebookCmd)
	deleteCmd.AddCommand(deleteTrashCmd)
	deleteCmd.AddCommand(deleteSLOCmd)
	deleteCmd.AddCommand(deleteNotificationCmd)
	deleteCmd.AddCommand(deleteBucketCmd)
	deleteCmd.AddCommand(deleteLookupCmd)
	deleteCmd.AddCommand(deleteSettingsCmd)
	deleteCmd.AddCommand(deleteAppCmd)
	deleteCmd.AddCommand(deleteEdgeConnectCmd)
	deleteCmd.AddCommand(deleteDocumentCmd)
	deleteCmd.AddCommand(deleteSegmentCmd)
	deleteCmd.AddCommand(deleteAnomalyDetectorCmd)

	// Provider delete subcommands
	deleteCmd.AddCommand(deleteAzureProviderCmd)
	deleteCmd.AddCommand(deleteAWSProviderCmd)
	deleteCmd.AddCommand(deleteGCPProviderCmd)

	deleteAzureProviderCmd.AddCommand(deleteAzureConnectionCmd)
	deleteAzureProviderCmd.AddCommand(deleteAzureMonitoringConfigCmd)
}

// Declared stable: the invocation and output contract of these commands is
// additive-only. Stable is never implied -- see AGENTS.md "Stability Tiers".
