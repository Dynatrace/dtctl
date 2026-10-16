package cmd

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/dynatrace-oss/dtctl/pkg/output"
	"github.com/dynatrace-oss/dtctl/pkg/resources/gcpconnection"
	"github.com/dynatrace-oss/dtctl/pkg/resources/gcpmonitoringconfig"
	"github.com/dynatrace-oss/dtctl/pkg/safety"
	"github.com/dynatrace-oss/dtctl/pkg/stability"
)

var createGCPConnectionCmd = newCreateGCPConnectionCmd()

func newCreateGCPConnectionCmd() *cobra.Command {
	var createGCPConnectionName string
	var createGCPConnectionServiceAccountID string
	c := &cobra.Command{
		Use:     "connection [name]",
		Aliases: []string{"connections"},
		Short:   "Create GCP connection from flags",
		Long: `Create GCP connection using command flags.

Examples:
	  dtctl create gcp connection --name "my-gcp-connection"
	  dtctl create gcp connection my-gcp-connection
	  dtctl create gcp connection --name "my-gcp-connection" --serviceAccountId "my-reader@project.iam.gserviceaccount.com"`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if createGCPConnectionName == "" && len(args) > 0 {
				createGCPConnectionName = args[0]
			}

			if createGCPConnectionName == "" {
				return fmt.Errorf("connection name is required (use positional argument or --name)")
			}

			_, c, err := setupWithSafety(cmdContext(cmd), safety.OperationCreate)
			if err != nil {
				return err
			}

			handler := gcpconnection.NewHandler(c)
			value := gcpconnection.Value{
				Name: createGCPConnectionName,
				Type: "serviceAccountImpersonation",
				ServiceAccountImpersonation: &gcpconnection.ServiceAccountImpersonation{
					ServiceAccountID: createGCPConnectionServiceAccountID,
					Consumers:        []string{"SVC:com.dynatrace.da"},
				},
			}

			if dryRun(cmdContext(cmd)) {
				return newDryRunReport(cmd).
					Linef("Dry run: would create GCP connection").
					Field("Name", "%s", createGCPConnectionName).
					Field("Service account", "%s", createGCPConnectionServiceAccountID).
					Print()
			}

			created, err := handler.Create(gcpconnection.GCPConnectionCreate{Value: value})
			if err != nil {
				printGCPPrincipalHint(cmdContext(cmd), handler, createGCPConnectionName, createGCPConnectionServiceAccountID)
				return err
			}

			output.FprintSuccess(currentStderr(cmdContext(cmd)), "GCP connection created: %s", created.ObjectID)
			printGCPPrincipalHint(cmdContext(cmd), handler, createGCPConnectionName, createGCPConnectionServiceAccountID)
			return nil
		},
	}
	c.Flags().StringVar(&createGCPConnectionName, "name", "", "GCP connection name (required)")
	c.Flags().StringVar(&createGCPConnectionServiceAccountID, "serviceAccountId", "", "Customer service account email (optional; can be set later with update)")
	c.Flags().StringVar(&createGCPConnectionServiceAccountID, "serviceaccountid", "", "Alias for --serviceAccountId")
	stability.MarkFlag(c, "serviceAccountId", stability.Experimental, pre10Since)
	stability.MarkFlag(c, "serviceaccountid", stability.Experimental, pre10Since)
	stability.MarkStable(c)
	// The name can come from the positional argument instead, so --name is
	// not marked required; an explicitly empty value is still rejected.
	rejectEmptyFlag(c, "name")
	return c
}

func printGCPPrincipalHint(ctx context.Context, handler *gcpconnection.Handler, connectionName, serviceAccountID string) {
	principal, err := handler.GetDynatracePrincipal()
	if err != nil {
		return
	}

	fmt.Fprintln(currentStdout(ctx), "Dynatrace GCP principal:")
	fmt.Fprintf(currentStdout(ctx), "  Principal ID: %s\n", principal.ObjectID)
	if principal.Principal != "" {
		fmt.Fprintf(currentStdout(ctx), "  Principal:    %s\n", principal.Principal)
	}

	if serviceAccountID != "" && principal.Principal != "" {
		fmt.Fprintln(currentStdout(ctx), "Grant Token Creator role (copy/paste):")
		fmt.Fprintf(currentStdout(ctx), "gcloud iam service-accounts add-iam-policy-binding %q --project=\"${PROJECT_ID}\" --member=\"serviceAccount:%s\" --role=\"roles/iam.serviceAccountTokenCreator\"\n", serviceAccountID, principal.Principal)
	}

	dynatracePrincipal := principal.Principal
	if dynatracePrincipal == "" {
		dynatracePrincipal = "dynatrace-<tenant-id>@dtp-prod-gcp-auth.iam.gserviceaccount.com"
	}

	customerServiceAccount := serviceAccountID
	if customerServiceAccount == "" {
		customerServiceAccount = "${CUSTOMER_SA_EMAIL}"
	}

	fmt.Fprintln(currentStdout(ctx), "GCP quickstart snippets:")
	fmt.Fprintln(currentStdout(ctx), "1) Define variables:")
	fmt.Fprintln(currentStdout(ctx), "PROJECT_ID=\"my-project-id\"")
	fmt.Fprintf(currentStdout(ctx), "DT_GCP_PRINCIPAL=%q\n", dynatracePrincipal)
	fmt.Fprintln(currentStdout(ctx), "CUSTOMER_SA_NAME=\"dynatrace-integration\"")
	fmt.Fprintln(currentStdout(ctx), "CUSTOMER_SA_EMAIL=\"${CUSTOMER_SA_NAME}@${PROJECT_ID}.iam.gserviceaccount.com\"")
	fmt.Fprintln(currentStdout(ctx))
	fmt.Fprintln(currentStdout(ctx), "2) Create customer service account:")
	fmt.Fprintln(currentStdout(ctx), "gcloud iam service-accounts create \"${CUSTOMER_SA_NAME}\" \\")
	fmt.Fprintln(currentStdout(ctx), "  --project \"${PROJECT_ID}\" \\")
	fmt.Fprintln(currentStdout(ctx), "  --display-name \"Dynatrace Integration\"")
	fmt.Fprintln(currentStdout(ctx))
	fmt.Fprintln(currentStdout(ctx), "3) Grant required viewer roles:")
	fmt.Fprintln(currentStdout(ctx), "for ROLE in roles/browser roles/monitoring.viewer roles/compute.viewer roles/cloudasset.viewer; do")
	fmt.Fprintln(currentStdout(ctx), "  gcloud projects add-iam-policy-binding \"${PROJECT_ID}\" \\")
	fmt.Fprintln(currentStdout(ctx), "    --quiet --format=\"none\" \\")
	fmt.Fprintln(currentStdout(ctx), "    --member \"serviceAccount:${CUSTOMER_SA_EMAIL}\" \\")
	fmt.Fprintln(currentStdout(ctx), "    --role \"${ROLE}\"")
	fmt.Fprintln(currentStdout(ctx), "done")
	fmt.Fprintln(currentStdout(ctx))
	fmt.Fprintln(currentStdout(ctx), "4) Grant Token Creator role to Dynatrace principal:")
	fmt.Fprintf(currentStdout(ctx), "gcloud iam service-accounts add-iam-policy-binding %q \\\n", customerServiceAccount)
	fmt.Fprintln(currentStdout(ctx), "  --project \"${PROJECT_ID}\" \\")
	fmt.Fprintf(currentStdout(ctx), "  --member=\"serviceAccount:%s\" \\\n", dynatracePrincipal)
	fmt.Fprintln(currentStdout(ctx), "  --role=\"roles/iam.serviceAccountTokenCreator\"")
	fmt.Fprintln(currentStdout(ctx))
	fmt.Fprintln(currentStdout(ctx), "5) Update connection in Dynatrace with customer service account:")
	fmt.Fprintf(currentStdout(ctx), "dtctl update gcp connection --name %q --serviceAccountId \"${CUSTOMER_SA_EMAIL}\"\n", connectionName)
	fmt.Fprintln(currentStdout(ctx))
	fmt.Fprintln(currentStdout(ctx), "Optional: check Domain Restricted Sharing policy allows Dynatrace customer:")
	fmt.Fprintln(currentStdout(ctx), "gcloud resource-manager org-policies describe constraints/iam.allowedPolicyMemberDomains \\")
	fmt.Fprintln(currentStdout(ctx), "  --project=\"${PROJECT_ID}\" \\")
	fmt.Fprintln(currentStdout(ctx), "  --format=\"value(spec.rules.values.allowedValues)\" | tr ';' '\\n' | grep -Fx 'customers/C03cngnp6'")
	fmt.Fprintln(currentStdout(ctx))
}

var createGCPMonitoringConfigCmd = newCreateGCPMonitoringConfigCmd()

func newCreateGCPMonitoringConfigCmd() *cobra.Command {
	var createGCPMonitoringConfigCredentials string
	var createGCPMonitoringConfigFeatureSets string
	var createGCPMonitoringConfigLocationFiltering string
	var createGCPMonitoringConfigName string
	var createGCPMonitoringConfigCentral bool
	c := &cobra.Command{
		Use:     "monitoring",
		Aliases: []string{"monitoring-config"},
		Short:   "Create GCP monitoring config from flags",
		Long: `Create GCP monitoring configuration using command flags.

Examples:
  dtctl create gcp monitoring --name "my-gcp-monitoring" --credentials "my-gcp-connection"
  dtctl create gcp monitoring --name "my-gcp-monitoring" --credentials "<connection-id>" --locationFiltering "us-central1,europe-west1"`,
		RunE: func(cmd *cobra.Command, args []string) error {

			_, c, err := setupWithSafety(cmdContext(cmd), safety.OperationCreate)
			if err != nil {
				return err
			}

			connectionHandler := gcpconnection.NewHandler(c)
			monitoringHandler := gcpmonitoringconfig.NewHandler(c)

			credential, err := gcpmonitoringconfig.ResolveCredential(createGCPMonitoringConfigCredentials, connectionHandler)
			if err != nil {
				return err
			}
			credential.Enabled = false // Created in disabled state; use 'dtctl enable gcp monitoring' to enable

			locations, err := gcpmonitoringconfig.ParseLocations(createGCPMonitoringConfigLocationFiltering)
			if err != nil {
				return err
			}

			featureSets, err := gcpmonitoringconfig.ParseOrDefaultFeatureSets(createGCPMonitoringConfigFeatureSets, monitoringHandler)
			if err != nil {
				return err
			}

			version, err := monitoringHandler.GetLatestVersion()
			if err != nil {
				return fmt.Errorf("failed to determine extension version: %w", err)
			}

			payload := buildGCPMonitoringConfig(
				createGCPMonitoringConfigName, version, credential, locations, featureSets,
				centralEnrichmentIntent(cmd, createGCPMonitoringConfigCentral))

			body, err := json.Marshal(payload)
			if err != nil {
				return fmt.Errorf("failed to prepare request payload: %w", err)
			}

			if dryRun(cmdContext(cmd)) {
				return newDryRunReport(cmd).
					Linef("Dry run: would create GCP monitoring config (disabled)").
					Field("Name", "%s", createGCPMonitoringConfigName).
					Field("Version", "%s", version).
					Field("Locations", "%d", len(locations)).
					Field("Feature sets", "%d", len(featureSets)).
					Print()
			}

			created, err := monitoringHandler.Create(body)
			if err != nil {
				return err
			}

			output.FprintSuccess(currentStderr(cmdContext(cmd)), "GCP monitoring config created (disabled): %s", created.ObjectID)
			output.FprintInfo(currentStderr(cmdContext(cmd)), "Run 'dtctl enable gcp monitoring --name %q' to enable it", createGCPMonitoringConfigName)
			return nil
		},
	}
	c.Flags().StringVar(&createGCPMonitoringConfigName, "name", "", "Monitoring config name/description (required)")
	c.Flags().StringVar(&createGCPMonitoringConfigCredentials, "credentials", "", "GCP connection name or ID (required)")
	c.Flags().StringVar(&createGCPMonitoringConfigLocationFiltering, "locationFiltering", "", "Comma-separated locations to monitor, or 'all' for no filter (default: all)")
	c.Flags().StringVar(&createGCPMonitoringConfigFeatureSets, "featureSets", "", "Comma-separated feature sets (default: all *_essential from schema)")
	c.Flags().StringVar(&createGCPMonitoringConfigFeatureSets, "featuresets", "", "Alias for --featureSets")
	stability.MarkFlag(c, "locationFiltering", stability.Experimental, pre10Since)
	stability.MarkFlag(c, "featureSets", stability.Experimental, pre10Since)
	stability.MarkFlag(c, "featuresets", stability.Experimental, pre10Since)
	addCentralEnrichmentFlag(c, &createGCPMonitoringConfigCentral)
	stability.MarkStable(c)
	markFlagRequiredNonEmpty(c, "name")
	markFlagRequiredNonEmpty(c, "credentials")
	return c
}

func buildGCPMonitoringConfig(name, version string, credential gcpmonitoringconfig.Credential,
	locations, featureSets []string, central *bool) gcpmonitoringconfig.GCPMonitoringConfig {
	return gcpmonitoringconfig.GCPMonitoringConfig{
		Scope: "integration-gcp",
		Value: gcpmonitoringconfig.Value{
			Enabled:     false,
			Description: name,
			Version:     version,
			GoogleCloud: gcpmonitoringconfig.GoogleCloudConfig{
				UseIngestEnrichmentConfig: central,
				Credentials:               []gcpmonitoringconfig.Credential{credential},
				LocationFiltering:         locations,
				ProjectFiltering:          []string{},
				FolderFiltering:           []string{},
				TagFiltering:              []gcpmonitoringconfig.TagFilter{},
				LabelFiltering:            []gcpmonitoringconfig.TagFilter{},
				TagEnrichment:             []string{},
				LabelEnrichment:           []string{},
				// ObservabilityScopesEnabled stays nil: create has always left
				// it to the schema default rather than sending false.
				SmartscapeConfiguration: gcpmonitoringconfig.FlagConfig{Enabled: true},
				Resources:               []gcpmonitoringconfig.MetricSource{},
			},
			FeatureSets: featureSets,
		},
	}
}

func init() {
	createGCPProviderCmd.AddCommand(createGCPConnectionCmd)
	createGCPProviderCmd.AddCommand(createGCPMonitoringConfigCmd)
	// Renamed to kebab-case in 1.0 (contrib breaking-changes/cloud-flags-kebab-case.md);
	// the spelling aliases are removed outright.
	// Renamed to kebab-case in 1.0 (contrib breaking-changes/cloud-flags-kebab-case.md);
	// the spelling aliases are removed outright.
}

// Declared stable: the invocation and output contract of these commands is
// additive-only. Stable is never implied -- see AGENTS.md "Stability Tiers".
