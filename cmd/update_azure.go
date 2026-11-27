package cmd

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/Dynatrace/dtctl/pkg/output"
	"github.com/Dynatrace/dtctl/pkg/resources/azureconnection"
	"github.com/Dynatrace/dtctl/pkg/resources/azuremonitoringconfig"
	"github.com/Dynatrace/dtctl/pkg/safety"
	"github.com/Dynatrace/dtctl/pkg/stability"
)

var updateAzureProviderCmd = newUpdateAzureProviderCmd()

func newUpdateAzureProviderCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "azure",
		Short: "Update Azure resources",
		RunE:  requireSubcommand,
	}
	stability.MarkStable(c)
	return c
}

var updateAzureConnectionCmd = newUpdateAzureConnectionCmd()

func newUpdateAzureConnectionCmd() *cobra.Command {
	var updateAzureConnectionApplicationID string
	var updateAzureConnectionClientSecret string
	var updateAzureConnectionDirectoryID string
	var updateAzureConnectionName string
	c := &cobra.Command{
		Use:     "connection [id]",
		Aliases: []string{"connections"},
		Short:   "Update Azure connection from flags",
		Long: `Update Azure connection by ID argument or by --name.

Examples:
  dtctl update azure connection --name "siwek" --directory-id "XYZ" --application-id "ZUZ"
  dtctl update azure connection <id> --directory-id "XYZ" --application-id "ZUZ"`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if updateAzureConnectionDirectoryID == "" && updateAzureConnectionApplicationID == "" && updateAzureConnectionClientSecret == "" {
				return fmt.Errorf("at least one of --directory-id, --application-id, or --client-secret is required")
			}

			if len(args) == 0 && updateAzureConnectionName == "" {
				return fmt.Errorf("provide connection ID argument or --name")
			}

			_, c, err := setupWithSafety(cmdContext(cmd), safety.OperationUpdate)
			if err != nil {
				return err
			}

			handler := azureconnection.NewHandler(c)

			var existing *azureconnection.AzureConnection
			if len(args) > 0 {
				existing, err = handler.Get(args[0])
				if err != nil {
					return err
				}
			} else {
				existing, err = handler.FindByName(updateAzureConnectionName)
				if err != nil {
					return err
				}
			}

			value := existing.Value
			switch value.Type {
			case "federatedIdentityCredential":
				if updateAzureConnectionClientSecret != "" {
					return fmt.Errorf("--client-secret is not applicable to connections of type federatedIdentityCredential")
				}
				if value.FederatedIdentityCredential == nil {
					value.FederatedIdentityCredential = &azureconnection.FederatedIdentityCredential{Consumers: []string{"SVC:com.dynatrace.da"}}
				} else if len(value.FederatedIdentityCredential.Consumers) == 0 {
					value.FederatedIdentityCredential.Consumers = []string{"SVC:com.dynatrace.da"}
				}
				if updateAzureConnectionDirectoryID != "" {
					value.FederatedIdentityCredential.DirectoryID = updateAzureConnectionDirectoryID
				}
				if updateAzureConnectionApplicationID != "" {
					value.FederatedIdentityCredential.ApplicationID = updateAzureConnectionApplicationID
				}
			case "clientSecret":
				if value.ClientSecret == nil {
					if updateAzureConnectionDirectoryID == "" || updateAzureConnectionApplicationID == "" {
						return fmt.Errorf("the API returned no clientSecret data for this connection; provide both --directory-id and --application-id to initialize it")
					}
					value.ClientSecret = &azureconnection.ClientSecretCredential{Consumers: []string{"SVC:com.dynatrace.da"}}
				} else if len(value.ClientSecret.Consumers) == 0 {
					value.ClientSecret.Consumers = []string{"SVC:com.dynatrace.da"}
				}
				if updateAzureConnectionDirectoryID != "" {
					value.ClientSecret.DirectoryID = updateAzureConnectionDirectoryID
				}
				if updateAzureConnectionApplicationID != "" {
					value.ClientSecret.ApplicationID = updateAzureConnectionApplicationID
				}
				if updateAzureConnectionClientSecret != "" {
					value.ClientSecret.ClientSecret = updateAzureConnectionClientSecret
				} else {
					// API may return a masked placeholder — don't PUT it back
					value.ClientSecret.ClientSecret = ""
				}
			default:
				return fmt.Errorf("unsupported azure connection type %q", value.Type)
			}

			if dryRun(cmdContext(cmd)) {
				return newDryRunReport(cmd).
					Linef("Dry run: would update Azure connection %s", existing.ObjectID).
					Detail("object_id", "%s", existing.ObjectID).
					Print()
			}

			updated, err := handler.Update(existing.ObjectID, value)
			if err != nil {
				return err
			}

			output.FprintSuccess(currentStderr(cmdContext(cmd)), "Azure connection updated: %s", updated.ObjectID)
			return nil
		},
	}
	c.Flags().StringVar(&updateAzureConnectionName, "name", "", "Azure connection name (used when ID argument is not provided)")
	c.Flags().StringVar(&updateAzureConnectionDirectoryID, "directory-id", "", "Directory ID to set")
	c.Flags().StringVar(&updateAzureConnectionApplicationID, "application-id", "", "Application ID to set")
	c.Flags().StringVar(&updateAzureConnectionClientSecret, "client-secret", "", "Client secret value (clientSecret type only); prefer passing via env var to keep out of shell history (note: expanded value can still be visible in process arguments)")
	stability.Mark(c, stability.Experimental, pre10Since)
	stability.MarkFlag(c, "directory-id", stability.Experimental, cloudFlagsKebabSince)
	stability.MarkFlag(c, "application-id", stability.Experimental, cloudFlagsKebabSince)
	stability.MarkFlag(c, "client-secret", stability.Experimental, cloudFlagsKebabSince)
	// At least one of these is required, and a flag left out keeps the stored
	// value; an explicitly empty one (every spelling) is rejected rather than
	// read as "left out".
	for _, name := range []string{"directory-id", "application-id", "client-secret"} {
		rejectEmptyFlag(c, name)
	}
	return c
}

var updateAzureMonitoringConfigCmd = newUpdateAzureMonitoringConfigCmd()

func newUpdateAzureMonitoringConfigCmd() *cobra.Command {
	var updateAzureMonitoringConfigFeatureSets string
	var updateAzureMonitoringConfigLocationFiltering string
	var updateAzureMonitoringConfigName string
	c := &cobra.Command{
		Use:     "monitoring [id]",
		Aliases: []string{"monitoring-config"},
		Short:   "Update Azure monitoring config from flags",
		Long: `Update Azure monitoring configuration by ID argument or by --name.

Examples:
  dtctl update azure monitoring --name "siwek" --locations "eastus,westeurope"
  dtctl update azure monitoring --name "siwek" --feature-sets "microsoft_compute.virtualmachines_essential,microsoft_web.sites_functionapp_essential"
  dtctl update azure monitoring <id> --locations "eastus,westeurope"`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if strings.TrimSpace(updateAzureMonitoringConfigLocationFiltering) == "" &&
				strings.TrimSpace(updateAzureMonitoringConfigFeatureSets) == "" {
				return fmt.Errorf("at least one of --locations or --feature-sets is required")
			}

			_, c, err := setupWithSafety(cmdContext(cmd), safety.OperationUpdate)
			if err != nil {
				return err
			}

			handler := azuremonitoringconfig.NewHandler(c)

			var existing *azuremonitoringconfig.AzureMonitoringConfig
			if len(args) > 0 {
				identifier := args[0]
				existing, err = handler.FindByName(identifier)
				if err != nil {
					existing, err = handler.Get(identifier)
					if err != nil {
						return fmt.Errorf("monitoring config with name/description or ID %q not found", identifier)
					}
				}
			} else {
				if updateAzureMonitoringConfigName == "" {
					return fmt.Errorf("provide config ID argument or --name")
				}
				existing, err = handler.FindByName(updateAzureMonitoringConfigName)
				if err != nil {
					return err
				}
			}

			value := existing.Value
			if strings.TrimSpace(updateAzureMonitoringConfigLocationFiltering) != "" {
				locations := azuremonitoringconfig.SplitCSV(updateAzureMonitoringConfigLocationFiltering)
				if len(locations) == 0 {
					return fmt.Errorf("--locations must contain at least one location")
				}
				value.Azure.LocationFiltering = locations
			}
			if strings.TrimSpace(updateAzureMonitoringConfigFeatureSets) != "" {
				featureSets := azuremonitoringconfig.SplitCSV(updateAzureMonitoringConfigFeatureSets)
				if len(featureSets) == 0 {
					return fmt.Errorf("--feature-sets must contain at least one feature set")
				}
				value.FeatureSets = featureSets
			}

			payload := azuremonitoringconfig.AzureMonitoringConfig{Scope: existing.Scope, Value: value}
			body, err := json.Marshal(payload)
			if err != nil {
				return fmt.Errorf("failed to prepare request payload: %w", err)
			}

			if dryRun(cmdContext(cmd)) {
				return newDryRunReport(cmd).
					Linef("Dry run: would update Azure monitoring config %s", existing.ObjectID).
					Detail("object_id", "%s", existing.ObjectID).
					Field("Locations", "%d", len(value.Azure.LocationFiltering)).
					Field("Feature sets", "%d", len(value.FeatureSets)).
					Print()
			}

			updated, err := handler.Update(existing.ObjectID, body)
			if err != nil {
				return err
			}

			output.FprintSuccess(currentStderr(cmdContext(cmd)), "Azure monitoring config updated: %s", updated.ObjectID)
			return nil
		},
	}
	c.Flags().StringVar(&updateAzureMonitoringConfigName, "name", "", "Monitoring config name/description (used when ID argument is not provided)")
	c.Flags().StringVar(&updateAzureMonitoringConfigLocationFiltering, "locations", "", "Comma-separated locations")
	c.Flags().StringVar(&updateAzureMonitoringConfigFeatureSets, "feature-sets", "", "Comma-separated feature sets")
	stability.Mark(c, stability.Experimental, pre10Since)
	stability.MarkFlag(c, "locations", stability.Experimental, cloudFlagsKebabSince)
	stability.MarkFlag(c, "feature-sets", stability.Experimental, cloudFlagsKebabSince)
	// At least one is required and a flag left out keeps the stored value, so
	// an explicitly empty one is rejected rather than read as "left out".
	for _, name := range []string{"locations", "feature-sets"} {
		rejectEmptyFlag(c, name)
	}
	return c
}

func init() {
	updateCmd.AddCommand(updateAzureProviderCmd)

	updateAzureProviderCmd.AddCommand(updateAzureConnectionCmd)
	updateAzureProviderCmd.AddCommand(updateAzureMonitoringConfigCmd)
	// Every path through this command needs one of the kebab-case flags (every one of --directory-id, --application-id and --client-secret),
	// which are new names in 0.48.0 and not yet proven stable (experimental):
	// at a stable floor they would be hidden, leaving this command with no usable
	// invocation. Marking the command says that plainly instead of hiding the
	// flags and then failing on a "required" flag help no longer lists.
	// Every path through this command needs one of the kebab-case flags (both --locations and --feature-sets),
	// which are new names in 0.48.0 and not yet proven stable (experimental):
	// at a stable floor they would be hidden, leaving this command with no usable
	// invocation. Marking the command says that plainly instead of hiding the
	// flags and then failing on a "required" flag help no longer lists.
}

// Declared stable: the invocation and output contract of this command is
// additive-only. Stable is never implied -- see AGENTS.md "Stability Tiers".
func init() {
}
