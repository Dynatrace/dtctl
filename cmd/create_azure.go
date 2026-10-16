package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"runtime"
	"strings"

	"github.com/spf13/cobra"

	"github.com/dynatrace-oss/dtctl/pkg/output"
	"github.com/dynatrace-oss/dtctl/pkg/resources/azureconnection"
	"github.com/dynatrace-oss/dtctl/pkg/resources/azuremonitoringconfig"
	"github.com/dynatrace-oss/dtctl/pkg/safety"
	"github.com/dynatrace-oss/dtctl/pkg/stability"
)

var createAzureProviderCmd = newCreateAzureProviderCmd()

func newCreateAzureProviderCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "azure",
		Short: "Create Azure resources",
		RunE:  requireSubcommand,
	}
	stability.MarkStable(c)
	return c
}

var createAzureConnectionCmd = newCreateAzureConnectionCmd()

func newCreateAzureConnectionCmd() *cobra.Command {
	var createAzureConnectionApplicationID string
	var createAzureConnectionClientSecret string
	var createAzureConnectionDirectoryID string
	var createAzureConnectionIssuer string
	var createAzureConnectionName string
	var createAzureConnectionType string
	c := &cobra.Command{
		Use:     "connection",
		Aliases: []string{"connections"},
		Short:   "Create Azure connection from flags",
		Long: `Create Azure connection using command flags.

Examples:
  dtctl create azure connection --name "siwek" --type "federatedIdentityCredential"
  dtctl create azure connection --name "siwek" --type "clientSecret" --directoryId "$TENANT_ID" --applicationId "$CLIENT_ID" --clientSecret "$CLIENT_SECRET"`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if createAzureConnectionName == "" || createAzureConnectionType == "" {
				missing := make([]string, 0, 2)
				if createAzureConnectionName == "" {
					missing = append(missing, "--name")
				}
				if createAzureConnectionType == "" {
					missing = append(missing, "--type")
				}

				return fmt.Errorf(
					"required flag(s) %s not set\nAvailable --type values: federatedIdentityCredential, clientSecret\nExample: dtctl create azure connection --name \"my-conn\" --type federatedIdentityCredential",
					strings.Join(missing, ", "),
				)
			}

			switch createAzureConnectionType {
			case "federatedIdentityCredential", "clientSecret":
				// valid
			default:
				return fmt.Errorf("unsupported --type %q (supported: federatedIdentityCredential, clientSecret)", createAzureConnectionType)
			}

			if createAzureConnectionType == "federatedIdentityCredential" &&
				(createAzureConnectionDirectoryID != "" || createAzureConnectionApplicationID != "" || createAzureConnectionClientSecret != "") {
				return fmt.Errorf("--directoryId, --applicationId, and --clientSecret are only supported for --type clientSecret\nFor federatedIdentityCredential, run 'dtctl update azure connection' after setting up federation in Azure")
			}

			if createAzureConnectionType == "clientSecret" && createAzureConnectionIssuer != "" {
				return fmt.Errorf("--issuer is only supported for --type federatedIdentityCredential (clientSecret connections do not use a token issuer)")
			}

			_, c, err := setupWithSafety(cmdContext(cmd), safety.OperationCreate)
			if err != nil {
				return err
			}

			handler := azureconnection.NewHandler(c)

			value := azureconnection.Value{
				Name: createAzureConnectionName,
				Type: createAzureConnectionType,
			}

			switch createAzureConnectionType {
			case "federatedIdentityCredential":
				value.FederatedIdentityCredential = &azureconnection.FederatedIdentityCredential{Consumers: []string{"SVC:com.dynatrace.da"}}
			case "clientSecret":
				value.ClientSecret = &azureconnection.ClientSecretCredential{
					DirectoryID:   createAzureConnectionDirectoryID,
					ApplicationID: createAzureConnectionApplicationID,
					ClientSecret:  createAzureConnectionClientSecret,
					Consumers:     []string{"SVC:com.dynatrace.da"},
				}
			}

			if dryRun(cmdContext(cmd)) {
				return newDryRunReport(cmd).
					Linef("Dry run: would create Azure connection").
					Field("Name", "%s", createAzureConnectionName).
					Print()
			}

			created, err := handler.Create(azureconnection.AzureConnectionCreate{Value: value})
			if err != nil {
				return err
			}

			output.FprintSuccess(currentStderr(cmdContext(cmd)), "Azure connection created: %s", created.ObjectID)
			if createAzureConnectionType == "federatedIdentityCredential" {
				printFederatedCreateInstructions(cmdContext(cmd), c.BaseURL(), created.ObjectID, createAzureConnectionName, createAzureConnectionIssuer)
			}
			return nil
		},
	}
	c.Flags().StringVar(&createAzureConnectionName, "name", "", "Azure connection name (required)")
	c.Flags().StringVar(&createAzureConnectionType, "type", "", "Azure connection type: federatedIdentityCredential or clientSecret (required)")
	c.Flags().StringVar(&createAzureConnectionDirectoryID, "directoryId", "", "Directory (tenant) ID — clientSecret type only")
	c.Flags().StringVar(&createAzureConnectionApplicationID, "applicationId", "", "Application (client) ID — clientSecret type only")
	c.Flags().StringVar(&createAzureConnectionClientSecret, "clientSecret", "", "Client secret value — clientSecret type only; prefer passing via env var to keep out of shell history (note: expanded value can still be visible in process arguments)")
	stability.MarkFlag(c, "directoryId", stability.Experimental, pre10Since)
	stability.MarkFlag(c, "applicationId", stability.Experimental, pre10Since)
	stability.MarkFlag(c, "clientSecret", stability.Experimental, pre10Since)
	c.Flags().StringVar(&createAzureConnectionIssuer, "issuer", "", "Token issuer URL for federatedIdentityCredential (default: auto-detected from tenant host)")
	stability.MarkStable(c)
	registerFlagCompletion(c, "type", func(_ *cobra.Command, _ []string, _ string) ([]string, cobra.ShellCompDirective) {
		return []string{
			"federatedIdentityCredential\tUse workload identity federation (recommended)",
			"clientSecret\tUse service principal client secret",
		}, cobra.ShellCompDirectiveNoFileComp
	})
	return c
}

var createAzureMonitoringConfigCmd = newCreateAzureMonitoringConfigCmd()

func newCreateAzureMonitoringConfigCmd() *cobra.Command {
	var createAzureMonitoringConfigCredentials string
	var createAzureMonitoringConfigFeatureSets string
	var createAzureMonitoringConfigLocationFiltering string
	var createAzureMonitoringConfigName string
	var createAzureMonitoringConfigCentral bool
	c := &cobra.Command{
		Use:     "monitoring",
		Aliases: []string{"monitoring-config"},
		Short:   "Create Azure monitoring config from flags",
		Long: `Create Azure monitoring configuration using command flags.

Examples:
  dtctl create azure monitoring --name "siwek" --credentials "siwek" --locationFiltering "eastus,northcentralus" --featureSets "microsoft_apimanagement.service_essential,microsoft_cache.redis_essential"
  dtctl create azure monitoring --name "siwek" --credentials "<connection-id>"`,
		RunE: func(cmd *cobra.Command, args []string) error {

			_, c, err := setupWithSafety(cmdContext(cmd), safety.OperationCreate)
			if err != nil {
				return err
			}

			connectionHandler := azureconnection.NewHandler(c)
			monitoringHandler := azuremonitoringconfig.NewHandler(c)

			credential, err := azuremonitoringconfig.ResolveCredential(createAzureMonitoringConfigCredentials, connectionHandler)
			if err != nil {
				return err
			}
			credential.Enabled = false // Created in disabled state; use 'dtctl enable azure monitoring' to enable

			locations, err := azuremonitoringconfig.ParseOrDefaultLocations(createAzureMonitoringConfigLocationFiltering, monitoringHandler)
			if err != nil {
				return err
			}

			featureSets, err := azuremonitoringconfig.ParseOrDefaultFeatureSets(createAzureMonitoringConfigFeatureSets, monitoringHandler)
			if err != nil {
				return err
			}

			version, err := monitoringHandler.GetLatestVersion()
			if err != nil {
				return fmt.Errorf("failed to determine extension version: %w", err)
			}

			payload := buildAzureMonitoringConfig(
				createAzureMonitoringConfigName, version, credential, locations, featureSets,
				centralEnrichmentIntent(cmd, createAzureMonitoringConfigCentral))

			body, err := json.Marshal(payload)
			if err != nil {
				return fmt.Errorf("failed to prepare request payload: %w", err)
			}

			if dryRun(cmdContext(cmd)) {
				return newDryRunReport(cmd).
					Linef("Dry run: would create Azure monitoring config (disabled)").
					Field("Name", "%s", createAzureMonitoringConfigName).
					Field("Version", "%s", version).
					Field("Locations", "%d", len(locations)).
					Field("Feature sets", "%d", len(featureSets)).
					Print()
			}

			created, err := monitoringHandler.Create(body)
			if err != nil {
				return err
			}

			output.FprintSuccess(currentStderr(cmdContext(cmd)), "Azure monitoring config created (disabled): %s", created.ObjectID)
			output.FprintInfo(currentStderr(cmdContext(cmd)), "Run 'dtctl enable azure monitoring --name %q' to enable it", createAzureMonitoringConfigName)
			return nil
		},
	}
	c.Flags().StringVar(&createAzureMonitoringConfigName, "name", "", "Monitoring config name/description (required)")
	c.Flags().StringVar(&createAzureMonitoringConfigCredentials, "credentials", "", "Azure connection name or ID (required)")
	c.Flags().StringVar(&createAzureMonitoringConfigLocationFiltering, "locationFiltering", "", "Comma-separated locations (default: all from schema)")
	c.Flags().StringVar(&createAzureMonitoringConfigFeatureSets, "featureSets", "", "Comma-separated feature sets (default: all *_essential from schema)")
	c.Flags().StringVar(&createAzureMonitoringConfigFeatureSets, "featuresets", "", "Alias for --featureSets")
	stability.MarkFlag(c, "locationFiltering", stability.Experimental, pre10Since)
	stability.MarkFlag(c, "featureSets", stability.Experimental, pre10Since)
	stability.MarkFlag(c, "featuresets", stability.Experimental, pre10Since)
	addCentralEnrichmentFlag(c, &createAzureMonitoringConfigCentral)
	stability.MarkStable(c)
	markFlagRequiredNonEmpty(c, "name")
	markFlagRequiredNonEmpty(c, "credentials")
	return c
}

func buildAzureMonitoringConfig(name, version string, credential azuremonitoringconfig.Credential,
	locations, featureSets []string, central *bool) azuremonitoringconfig.AzureMonitoringConfig {
	return azuremonitoringconfig.AzureMonitoringConfig{
		Scope: "integration-azure",
		Value: azuremonitoringconfig.Value{
			Enabled:     false,
			Description: name,
			Version:     version,
			Azure: azuremonitoringconfig.AzureConfig{
				UseIngestEnrichmentConfig: central,
				DeploymentScope:           "SUBSCRIPTION",
				ConfigurationMode:         "ADVANCED",
				DeploymentMode:            "AUTOMATED",
				SubscriptionFilteringMode: "INCLUDE",
				Credentials:               []azuremonitoringconfig.Credential{credential},
				LocationFiltering:         locations,
			},
			FeatureSets: featureSets,
		},
	}
}

func printFederatedCreateInstructions(ctx context.Context, baseURL, objectID, connectionName, issuerOverride string) {
	u, err := url.Parse(baseURL)
	if err != nil {
		output.FprintWarning(currentStderr(ctx), "Could not parse base URL for instructions: %v", err)
		return
	}
	host := u.Host

	issuer := issuerOverride
	if issuer == "" {
		issuer = azureconnection.TokenIssuerForHost(host)
	}

	fmt.Fprintln(currentStdout(ctx), "\nTo complete the configuration, additional setup is required in the Azure Portal (Federated Credentials).")
	fmt.Fprintln(currentStdout(ctx), "Details for Azure configuration:")
	fmt.Fprintf(currentStdout(ctx), "  Issuer:    %s\n", issuer)
	fmt.Fprintf(currentStdout(ctx), "  Subject:   dt:connection-id/%s\n", objectID)
	fmt.Fprintf(currentStdout(ctx), "  Audiences: %s/svc-id/com.dynatrace.da\n", host)
	fmt.Fprintln(currentStdout(ctx))
	fmt.Fprintln(currentStdout(ctx), "Azure CLI commands:")
	fmt.Fprintln(currentStdout(ctx), "1. Create Service Principal and capture IDs:")
	if runtime.GOOS == "windows" {
		fmt.Fprintf(currentStdout(ctx), "   $CLIENT_ID = az ad sp create-for-rbac --name %q --create-password false --query appId -o tsv\n", connectionName)
		fmt.Fprintln(currentStdout(ctx), "   $TENANT_ID = az account show --query tenantId -o tsv")
		fmt.Fprintln(currentStdout(ctx))
		fmt.Fprintln(currentStdout(ctx), "2. Assign Reader role on subscription scope:")
		fmt.Fprintln(currentStdout(ctx), "   $IAM_SCOPE = \"/subscriptions/00000000-0000-0000-0000-000000000000\"")
		fmt.Fprintln(currentStdout(ctx), "   az role assignment create --assignee \"$CLIENT_ID\" --role Reader --scope \"$IAM_SCOPE\"")
		fmt.Fprintln(currentStdout(ctx))
		fmt.Fprintln(currentStdout(ctx), "3. Create Federated Credential:")
		fmt.Fprintf(currentStdout(ctx), "   az ad app federated-credential create --id \"$CLIENT_ID\" --parameters \"{'name': 'fd-Federated-Credential', 'issuer': '%s', 'subject': 'dt:connection-id/%s', 'audiences': ['%s/svc-id/com.dynatrace.da']}\"\n", issuer, objectID, host)
		fmt.Fprintln(currentStdout(ctx))
		fmt.Fprintln(currentStdout(ctx), "4. Update connection in Dynatrace (set directoryId + applicationId):")
		fmt.Fprintf(currentStdout(ctx), "   dtctl update azure connection --name %q --directoryId \"$TENANT_ID\" --applicationId \"$CLIENT_ID\"\n", connectionName)
	} else {
		fmt.Fprintf(currentStdout(ctx), "   CLIENT_ID=$(az ad sp create-for-rbac --name %q --create-password false --query appId -o tsv)\n", connectionName)
		fmt.Fprintln(currentStdout(ctx), "   TENANT_ID=$(az account show --query tenantId -o tsv)")
		fmt.Fprintln(currentStdout(ctx))
		fmt.Fprintln(currentStdout(ctx), "2. Assign Reader role on subscription scope:")
		fmt.Fprintln(currentStdout(ctx), "   IAM_SCOPE=\"/subscriptions/00000000-0000-0000-0000-000000000000\"")
		fmt.Fprintln(currentStdout(ctx), "   az role assignment create --assignee \"$CLIENT_ID\" --role Reader --scope \"$IAM_SCOPE\"")
		fmt.Fprintln(currentStdout(ctx))
		fmt.Fprintln(currentStdout(ctx), "3. Create Federated Credential:")
		fmt.Fprintf(currentStdout(ctx), "   az ad app federated-credential create --id \"$CLIENT_ID\" --parameters \"{'name': 'fd-Federated-Credential', 'issuer': '%s', 'subject': 'dt:connection-id/%s', 'audiences': ['%s/svc-id/com.dynatrace.da']}\"\n", issuer, objectID, host)
		fmt.Fprintln(currentStdout(ctx))
		fmt.Fprintln(currentStdout(ctx), "4. Update connection in Dynatrace (set directoryId + applicationId):")
		fmt.Fprintf(currentStdout(ctx), "   dtctl update azure connection --name %q --directoryId \"$TENANT_ID\" --applicationId \"$CLIENT_ID\"\n", connectionName)
	}
	fmt.Fprintln(currentStdout(ctx))
}

func init() {
	createCmd.AddCommand(createAzureProviderCmd)

	createAzureProviderCmd.AddCommand(createAzureConnectionCmd)
	createAzureProviderCmd.AddCommand(createAzureMonitoringConfigCmd)
	// Renamed to kebab-case in 1.0 (contrib breaking-changes/cloud-flags-kebab-case.md);
	// the spelling aliases are removed outright.
}

// Declared stable: the invocation and output contract of these commands is
// additive-only. Stable is never implied -- see AGENTS.md "Stability Tiers".
