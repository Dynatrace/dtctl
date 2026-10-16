package cmd

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/dynatrace-oss/dtctl/pkg/output"
	"github.com/dynatrace-oss/dtctl/pkg/resources/gcpconnection"
	"github.com/dynatrace-oss/dtctl/pkg/resources/gcpmonitoringconfig"
	"github.com/dynatrace-oss/dtctl/pkg/safety"
	"github.com/dynatrace-oss/dtctl/pkg/stability"
)

var enableGCPProviderCmd = newEnableGCPProviderCmd()

func newEnableGCPProviderCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "gcp",
		Short: "Enable GCP resources (Preview)",
		RunE:  requireSubcommand,
	}
	stability.MarkStable(c)
	attachPreviewNotice(c, "GCP")
	return c
}

var enableGCPMonitoringCmd = newEnableGCPMonitoringCmd()

func newEnableGCPMonitoringCmd() *cobra.Command {
	var enableGCPMonitoringName string
	var enableGCPMonitoringServiceAccountID string
	c := &cobra.Command{
		Use:     "monitoring [id]",
		Aliases: []string{"monitoring-config"},
		Short:   "Enable GCP monitoring configuration",
		Long: `Enable a GCP monitoring configuration by updating the linked connection
credentials and then enabling the monitoring config in a single step.

If --serviceAccountId is provided, dtctl will:
  1. Update the linked GCP connection with the specified service account
  2. Populate the serviceAccount field in the monitoring credential
  3. Enable the monitoring config and all credentials

If the linked connection already has a service account configured, --serviceAccountId
can be omitted; in that case the connection update step is skipped and only the
monitoring config is enabled. Passing --serviceAccountId always overwrites the
service account on the linked connection.

Examples:
  dtctl enable gcp monitoring --name "my-gcp-monitoring" --serviceAccountId "sa@project.iam.gserviceaccount.com"
  dtctl enable gcp monitoring <id> --serviceAccountId "sa@project.iam.gserviceaccount.com"
  dtctl enable gcp monitoring --name "my-gcp-monitoring"`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			// Early flag validation — before any auth/network calls
			if len(args) == 0 && enableGCPMonitoringName == "" {
				return fmt.Errorf("provide monitoring config ID argument or --name")
			}

			if dryRun(cmdContext(cmd)) {
				name := enableGCPMonitoringName
				if len(args) > 0 {
					name = args[0]
				}
				report := newDryRunReport(cmd).OnStderr().
					Linef("Dry run: would resolve GCP monitoring config %q", name).
					Detail("monitoring_config", "%s", name)
				if enableGCPMonitoringServiceAccountID != "" {
					report.Linef("Dry run: would update linked GCP connection with service account %q", enableGCPMonitoringServiceAccountID).
						Detail("service_account_id", "%s", enableGCPMonitoringServiceAccountID)
				}
				return report.Linef("Dry run: would enable monitoring config and all credentials").Print()
			}

			_, c, err := setupWithSafety(cmdContext(cmd), safety.OperationUpdate)
			if err != nil {
				return err
			}

			monitoringHandler := gcpmonitoringconfig.NewHandler(c)
			connectionHandler := gcpconnection.NewHandler(c)

			// Resolve monitoring config by ID arg or --name flag
			var existing *gcpmonitoringconfig.GCPMonitoringConfig
			if len(args) > 0 {
				identifier := args[0]
				existing, err = monitoringHandler.FindByName(identifier)
				if err != nil {
					existing, err = monitoringHandler.Get(identifier)
					if err != nil {
						return fmt.Errorf("GCP monitoring config %q not found by name or ID", identifier)
					}
				}
			} else {
				existing, err = monitoringHandler.FindByName(enableGCPMonitoringName)
				if err != nil {
					return err
				}
			}

			configName := existing.Value.Description
			if configName == "" {
				configName = existing.ObjectID
			}

			// Step 1: Update linked GCP connection with service account if --serviceAccountId provided
			if enableGCPMonitoringServiceAccountID != "" {
				if len(existing.Value.GoogleCloud.Credentials) == 0 {
					return fmt.Errorf("monitoring config %q has no credentials configured", configName)
				}
				if len(existing.Value.GoogleCloud.Credentials) > 1 {
					output.FprintWarning(currentStderr(cmdContext(cmd)), "monitoring config %q has %d credentials — only the first connection will be updated; use 'dtctl update gcp connection' for the others",
						configName, len(existing.Value.GoogleCloud.Credentials))
				}

				connectionID := existing.Value.GoogleCloud.Credentials[0].ConnectionID
				output.FprintInfo(currentStderr(cmdContext(cmd)), "Updating GCP connection %q with service account...", connectionID)

				conn, err := connectionHandler.Get(connectionID)
				if err != nil {
					return fmt.Errorf("failed to get linked connection %q: %w", connectionID, err)
				}

				connValue := conn.Value
				if connValue.Type == "" {
					connValue.Type = "serviceAccountImpersonation"
				}
				if connValue.ServiceAccountImpersonation == nil {
					connValue.ServiceAccountImpersonation = &gcpconnection.ServiceAccountImpersonation{
						Consumers: []string{"SVC:com.dynatrace.da"},
					}
				}
				if len(connValue.ServiceAccountImpersonation.Consumers) == 0 {
					connValue.ServiceAccountImpersonation.Consumers = []string{"SVC:com.dynatrace.da"}
				}
				if prev := connValue.ServiceAccountImpersonation.ServiceAccountID; prev != "" && prev != enableGCPMonitoringServiceAccountID {
					output.FprintWarning(currentStderr(cmdContext(cmd)), "Overwriting service account on connection %q (was %q)", connectionID, prev)
				}
				connValue.ServiceAccountImpersonation.ServiceAccountID = enableGCPMonitoringServiceAccountID

				_, err = connectionHandler.Update(conn.ObjectID, connValue)
				if err != nil {
					if strings.Contains(err.Error(), "GCP authentication failed") {
						return fmt.Errorf("%w\nIAM Policy update can take a couple of minutes before it becomes active, please retry in a moment", err)
					}
					return fmt.Errorf("failed to update connection credentials: %w", err)
				}
				output.FprintSuccess(currentStderr(cmdContext(cmd)), "GCP connection %q updated", connectionID)
			}

			// Step 2: Enable monitoring config and all credentials
			output.FprintInfo(currentStderr(cmdContext(cmd)), "Enabling GCP monitoring config %q...", configName)
			value := existing.Value
			value.Enabled = true
			for i := range value.GoogleCloud.Credentials {
				value.GoogleCloud.Credentials[i].Enabled = true
			}
			// Populate serviceAccount on the first credential — the one whose connection was updated above
			if enableGCPMonitoringServiceAccountID != "" && len(value.GoogleCloud.Credentials) > 0 {
				value.GoogleCloud.Credentials[0].ServiceAccount = enableGCPMonitoringServiceAccountID
			}

			payload := gcpmonitoringconfig.GCPMonitoringConfig{Scope: existing.Scope, Value: value}
			body, err := json.Marshal(payload)
			if err != nil {
				return fmt.Errorf("failed to prepare request payload: %w", err)
			}

			updated, err := monitoringHandler.Update(existing.ObjectID, body)
			if err != nil {
				return err
			}

			output.FprintSuccess(currentStderr(cmdContext(cmd)), "GCP monitoring config %q enabled (%s)", configName, updated.ObjectID)
			return nil
		},
	}
	c.Flags().StringVar(&enableGCPMonitoringName, "name", "", "Monitoring config name/description (used when ID argument is not provided)")
	c.Flags().StringVar(&enableGCPMonitoringServiceAccountID, "serviceAccountId", "", "Service account email to set on the linked connection (optional)")
	stability.MarkFlag(c, "serviceAccountId", stability.Experimental, pre10Since)
	stability.MarkStable(c)
	return c
}

func init() {
	enableGCPProviderCmd.AddCommand(enableGCPMonitoringCmd)
	// Renamed to kebab-case in 1.0 (contrib breaking-changes/cloud-flags-kebab-case.md);
	// the spelling aliases are removed outright.
}

// Declared stable: the invocation and output contract of these commands is
// additive-only. Stable is never implied -- see AGENTS.md "Stability Tiers".
