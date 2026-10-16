package cmd

import (
	"encoding/json"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/dynatrace-oss/dtctl/pkg/output"
	"github.com/dynatrace-oss/dtctl/pkg/resources/azureconnection"
	"github.com/dynatrace-oss/dtctl/pkg/resources/azuremonitoringconfig"
	"github.com/dynatrace-oss/dtctl/pkg/safety"
	"github.com/dynatrace-oss/dtctl/pkg/stability"
)

var enableAzureProviderCmd = newEnableAzureProviderCmd()

func newEnableAzureProviderCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "azure",
		Short: "Enable Azure resources",
		RunE:  requireSubcommand,
	}
	stability.MarkStable(c)
	return c
}

var enableAzureMonitoringCmd = newEnableAzureMonitoringCmd()

func newEnableAzureMonitoringCmd() *cobra.Command {
	var enableAzureMonitoringApplicationID string
	var enableAzureMonitoringDirectoryID string
	var enableAzureMonitoringName string
	c := &cobra.Command{
		Use:     "monitoring [id]",
		Aliases: []string{"monitoring-config"},
		Short:   "Enable Azure monitoring configuration",
		Long: `Enable an Azure monitoring configuration by optionally updating the linked connection
credentials and then enabling the monitoring config in a single step.

If --directoryId and/or --applicationId are provided, the linked Azure connection
will be updated with the specified credentials before enabling the monitoring config.
If the connection credentials are already set, these flags can be omitted.

Examples:
  dtctl enable azure monitoring --name "my-azure-monitoring" --directoryId "$TENANT_ID" --applicationId "$CLIENT_ID"
  dtctl enable azure monitoring <id> --directoryId "$TENANT_ID" --applicationId "$CLIENT_ID"
  dtctl enable azure monitoring --name "my-azure-monitoring"`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			// Early flag validation — before any auth/network calls
			if len(args) == 0 && enableAzureMonitoringName == "" {
				return fmt.Errorf("provide monitoring config ID argument or --name")
			}

			if dryRun(cmdContext(cmd)) {
				name := enableAzureMonitoringName
				if len(args) > 0 {
					name = args[0]
				}
				report := newDryRunReport(cmd).OnStderr().
					Linef("Dry run: would resolve Azure monitoring config %q", name).
					Detail("monitoring_config", "%s", name)
				if enableAzureMonitoringDirectoryID != "" || enableAzureMonitoringApplicationID != "" {
					msg := "Dry run: would update linked Azure connection"
					if enableAzureMonitoringDirectoryID != "" {
						msg += fmt.Sprintf(" directoryId=%q", enableAzureMonitoringDirectoryID)
						report.Detail("directory_id", "%s", enableAzureMonitoringDirectoryID)
					}
					if enableAzureMonitoringApplicationID != "" {
						msg += fmt.Sprintf(" applicationId=%q", enableAzureMonitoringApplicationID)
						report.Detail("application_id", "%s", enableAzureMonitoringApplicationID)
					}
					report.Linef("%s", msg)
				}
				return report.Linef("Dry run: would enable monitoring config and all credentials").Print()
			}

			_, c, err := setupWithSafety(cmdContext(cmd), safety.OperationUpdate)
			if err != nil {
				return err
			}

			monitoringHandler := azuremonitoringconfig.NewHandler(c)
			connectionHandler := azureconnection.NewHandler(c)

			// Resolve monitoring config by ID arg or --name flag
			var existing *azuremonitoringconfig.AzureMonitoringConfig
			if len(args) > 0 {
				identifier := args[0]
				existing, err = monitoringHandler.FindByName(identifier)
				if err != nil {
					existing, err = monitoringHandler.Get(identifier)
					if err != nil {
						return fmt.Errorf("azure monitoring config %q not found by name or ID", identifier)
					}
				}
			} else {
				existing, err = monitoringHandler.FindByName(enableAzureMonitoringName)
				if err != nil {
					return err
				}
			}

			configName := existing.Value.Description
			if configName == "" {
				configName = existing.ObjectID
			}

			// Step 1: Update connection credentials if directoryId or applicationId provided
			if enableAzureMonitoringDirectoryID != "" || enableAzureMonitoringApplicationID != "" {
				if len(existing.Value.Azure.Credentials) == 0 {
					return fmt.Errorf("monitoring config %q has no credentials configured", configName)
				}
				if len(existing.Value.Azure.Credentials) > 1 {
					output.FprintWarning(currentStderr(cmdContext(cmd)), "monitoring config %q has %d credentials — only the first connection will be updated; use 'dtctl update azure connection' for the others",
						configName, len(existing.Value.Azure.Credentials))
				}

				connectionID := existing.Value.Azure.Credentials[0].ConnectionId
				output.FprintInfo(currentStderr(cmdContext(cmd)), "Updating Azure connection %q with credentials...", connectionID)

				conn, err := connectionHandler.Get(connectionID)
				if err != nil {
					return fmt.Errorf("failed to get linked connection %q: %w", connectionID, err)
				}

				value := conn.Value
				switch value.Type {
				case "federatedIdentityCredential":
					if value.FederatedIdentityCredential == nil {
						value.FederatedIdentityCredential = &azureconnection.FederatedIdentityCredential{}
					}
					if enableAzureMonitoringDirectoryID != "" {
						value.FederatedIdentityCredential.DirectoryID = enableAzureMonitoringDirectoryID
					}
					if enableAzureMonitoringApplicationID != "" {
						value.FederatedIdentityCredential.ApplicationID = enableAzureMonitoringApplicationID
					}
				case "clientSecret":
					if value.ClientSecret == nil {
						value.ClientSecret = &azureconnection.ClientSecretCredential{}
					}
					if enableAzureMonitoringDirectoryID != "" {
						value.ClientSecret.DirectoryID = enableAzureMonitoringDirectoryID
					}
					if enableAzureMonitoringApplicationID != "" {
						value.ClientSecret.ApplicationID = enableAzureMonitoringApplicationID
					}
				default:
					return fmt.Errorf("unsupported azure connection type %q", value.Type)
				}

				_, err = connectionHandler.Update(conn.ObjectID, value)
				if err != nil {
					return fmt.Errorf("failed to update connection credentials: %w", err)
				}
				output.FprintSuccess(currentStderr(cmdContext(cmd)), "Azure connection %q updated", connectionID)
			}

			// Step 2: Enable monitoring config and all credentials
			output.FprintInfo(currentStderr(cmdContext(cmd)), "Enabling Azure monitoring config %q...", configName)
			value := existing.Value
			value.Enabled = true
			for i := range value.Azure.Credentials {
				value.Azure.Credentials[i].Enabled = true
			}

			payload := azuremonitoringconfig.AzureMonitoringConfig{Scope: existing.Scope, Value: value}
			body, err := json.Marshal(payload)
			if err != nil {
				return fmt.Errorf("failed to prepare request payload: %w", err)
			}

			updated, err := monitoringHandler.Update(existing.ObjectID, body)
			if err != nil {
				return err
			}

			output.FprintSuccess(currentStderr(cmdContext(cmd)), "Azure monitoring config %q enabled (%s)", configName, updated.ObjectID)
			return nil
		},
	}
	c.Flags().StringVar(&enableAzureMonitoringName, "name", "", "Monitoring config name/description (used when ID argument is not provided)")
	c.Flags().StringVar(&enableAzureMonitoringDirectoryID, "directoryId", "", "Directory (tenant) ID to set on the linked connection (optional)")
	c.Flags().StringVar(&enableAzureMonitoringApplicationID, "applicationId", "", "Application (client) ID to set on the linked connection (optional)")
	stability.MarkFlag(c, "directoryId", stability.Experimental, pre10Since)
	stability.MarkFlag(c, "applicationId", stability.Experimental, pre10Since)
	stability.MarkStable(c)
	return c
}

func init() {
	enableCmd.AddCommand(enableAzureProviderCmd)

	enableAzureProviderCmd.AddCommand(enableAzureMonitoringCmd)
	// Renamed to kebab-case in 1.0 (contrib breaking-changes/cloud-flags-kebab-case.md);
	// the spelling aliases are removed outright.
}

// Declared stable: the invocation and output contract of these commands is
// additive-only. Stable is never implied -- see AGENTS.md "Stability Tiers".
func init() {
}
