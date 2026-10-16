package cmd

import (
	"encoding/json"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/dynatrace-oss/dtctl/pkg/output"
	"github.com/dynatrace-oss/dtctl/pkg/resources/azuremonitoringconfig"
	"github.com/dynatrace-oss/dtctl/pkg/safety"
	"github.com/dynatrace-oss/dtctl/pkg/stability"
)

var disableAzureProviderCmd = newDisableAzureProviderCmd()

func newDisableAzureProviderCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "azure",
		Short: "Disable Azure resources",
		RunE:  requireSubcommand,
	}
	stability.MarkStable(c)
	return c
}

var disableAzureMonitoringCmd = newDisableAzureMonitoringCmd()

func newDisableAzureMonitoringCmd() *cobra.Command {
	var disableAzureMonitoringName string
	c := &cobra.Command{
		Use:     "monitoring [id]",
		Aliases: []string{"monitoring-config"},
		Short:   "Disable Azure monitoring configuration",
		Long: `Disable an Azure monitoring configuration by setting it and all its credentials
to disabled in a single step.

The monitoring configuration and linked connection are preserved — only the
enabled flag is toggled off.

Examples:
  dtctl disable azure monitoring --name "my-azure-monitoring"
  dtctl disable azure monitoring <id>`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) == 0 && disableAzureMonitoringName == "" {
				return fmt.Errorf("provide monitoring config ID argument or --name")
			}

			if dryRun(cmdContext(cmd)) {
				name := disableAzureMonitoringName
				if len(args) > 0 {
					name = args[0]
				}
				return newDryRunReport(cmd).OnStderr().
					Linef("Dry run: would resolve Azure monitoring config %q", name).
					Detail("monitoring_config", "%s", name).
					Linef("Dry run: would disable monitoring config and all credentials").
					Print()
			}

			_, c, err := setupWithSafety(cmdContext(cmd), safety.OperationUpdate)
			if err != nil {
				return err
			}

			monitoringHandler := azuremonitoringconfig.NewHandler(c)

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
				existing, err = monitoringHandler.FindByName(disableAzureMonitoringName)
				if err != nil {
					return err
				}
			}

			configName := existing.Value.Description
			if configName == "" {
				configName = existing.ObjectID
			}

			output.FprintInfo(currentStderr(cmdContext(cmd)), "Disabling Azure monitoring config %q...", configName)
			value := existing.Value
			value.Enabled = false
			for i := range value.Azure.Credentials {
				value.Azure.Credentials[i].Enabled = false
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

			output.FprintSuccess(currentStderr(cmdContext(cmd)), "Azure monitoring config %q disabled (%s)", configName, updated.ObjectID)
			return nil
		},
	}
	c.Flags().StringVar(&disableAzureMonitoringName, "name", "", "Monitoring config name/description (used when ID argument is not provided)")
	stability.MarkStable(c)
	return c
}

func init() {
	disableAzureProviderCmd.AddCommand(disableAzureMonitoringCmd)
}

// Declared stable: the invocation and output contract of these commands is
// additive-only. Stable is never implied -- see AGENTS.md "Stability Tiers".
func init() {
}
