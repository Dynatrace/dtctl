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

var updateGCPConnectionCmd = newUpdateGCPConnectionCmd()

func newUpdateGCPConnectionCmd() *cobra.Command {
	var updateGCPConnectionName string
	var updateGCPConnectionServiceAccountID string
	c := &cobra.Command{
		Use:     "connection [id]",
		Aliases: []string{"connections"},
		Short:   "Update GCP connection from flags",
		Long: `Update GCP connection by ID argument or by --name.

Examples:
  dtctl update gcp connection --name "my-gcp-connection" --serviceAccountId "my-reader@project.iam.gserviceaccount.com"
  dtctl update gcp connection <id> --serviceAccountId "my-reader@project.iam.gserviceaccount.com"`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if updateGCPConnectionServiceAccountID == "" {
				return fmt.Errorf("--serviceAccountId is required")
			}

			_, c, err := setupWithSafety(cmdContext(cmd), safety.OperationUpdate)
			if err != nil {
				return err
			}

			handler := gcpconnection.NewHandler(c)

			var existing *gcpconnection.GCPConnection
			if len(args) > 0 {
				existing, err = handler.Get(args[0])
				if err != nil {
					return err
				}
			} else {
				if updateGCPConnectionName == "" {
					return fmt.Errorf("provide connection ID argument or --name")
				}
				existing, err = handler.FindByName(updateGCPConnectionName)
				if err != nil {
					return err
				}
			}

			value := existing.Value
			if value.Type == "" {
				value.Type = "serviceAccountImpersonation"
			}
			if value.ServiceAccountImpersonation == nil {
				value.ServiceAccountImpersonation = &gcpconnection.ServiceAccountImpersonation{
					Consumers: []string{"SVC:com.dynatrace.da"},
				}
			}
			if len(value.ServiceAccountImpersonation.Consumers) == 0 {
				value.ServiceAccountImpersonation.Consumers = []string{"SVC:com.dynatrace.da"}
			}
			value.ServiceAccountImpersonation.ServiceAccountID = updateGCPConnectionServiceAccountID

			if dryRun(cmdContext(cmd)) {
				return newDryRunReport(cmd).
					Linef("Dry run: would update GCP connection %s", existing.ObjectID).
					Detail("object_id", "%s", existing.ObjectID).
					Field("Service account", "%s", updateGCPConnectionServiceAccountID).
					Print()
			}

			updated, err := handler.Update(existing.ObjectID, value)
			if err != nil {
				if strings.Contains(err.Error(), "GCP authentication failed") {
					return fmt.Errorf("%w\nIAM Policy update can take a couple of minutes before it becomes active, please retry in a moment", err)
				}
				return err
			}

			output.FprintSuccess(currentStderr(cmdContext(cmd)), "GCP connection updated: %s", updated.ObjectID)
			return nil
		},
	}
	c.Flags().StringVar(&updateGCPConnectionName, "name", "", "GCP connection name (used when ID argument is not provided)")
	c.Flags().StringVar(&updateGCPConnectionServiceAccountID, "serviceAccountId", "", "Service account email to set")
	c.Flags().StringVar(&updateGCPConnectionServiceAccountID, "serviceaccountid", "", "Alias for --serviceAccountId")
	stability.Mark(c, stability.Experimental, pre10Since)
	stability.MarkFlag(c, "serviceAccountId", stability.Experimental, pre10Since)
	stability.MarkFlag(c, "serviceaccountid", stability.Experimental, pre10Since)
	// Not MarkFlagRequired: --serviceaccountid sets the same value, and an
	// invocation using only that spelling is valid. The command body reports
	// both left out; an explicitly empty value is rejected at parse time.
	rejectEmptyFlag(c, "serviceAccountId")
	rejectEmptyFlag(c, "serviceaccountid")
	return c
}

var updateGCPMonitoringConfigCmd = newUpdateGCPMonitoringConfigCmd()

func newUpdateGCPMonitoringConfigCmd() *cobra.Command {
	var updateGCPMonitoringConfigFeatureSets string
	var updateGCPMonitoringConfigLocationFiltering string
	var updateGCPMonitoringConfigName string
	c := &cobra.Command{
		Use:     "monitoring [id]",
		Aliases: []string{"monitoring-config"},
		Short:   "Update GCP monitoring config from flags",
		Long: `Update GCP monitoring configuration by ID argument or by --name.

Examples:
  dtctl update gcp monitoring --name "my-monitoring" --locationFiltering "us-central1,europe-west1"
  dtctl update gcp monitoring --name "my-monitoring" --featureSets "compute_engine_essential,cloud_run_essential"
  dtctl update gcp monitoring <id> --locationFiltering "us-central1,europe-west1"
  dtctl update gcp monitoring --name "my-monitoring" --locationFiltering all   # remove the location filter`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if strings.TrimSpace(updateGCPMonitoringConfigLocationFiltering) == "" &&
				strings.TrimSpace(updateGCPMonitoringConfigFeatureSets) == "" {
				return fmt.Errorf("at least one of --locationFiltering or --featureSets is required")
			}

			_, c, err := setupWithSafety(cmdContext(cmd), safety.OperationUpdate)
			if err != nil {
				return err
			}

			handler := gcpmonitoringconfig.NewHandler(c)

			var existing *gcpmonitoringconfig.GCPMonitoringConfig
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
				if updateGCPMonitoringConfigName == "" {
					return fmt.Errorf("provide config ID argument or --name")
				}
				existing, err = handler.FindByName(updateGCPMonitoringConfigName)
				if err != nil {
					return err
				}
			}

			value := existing.Value
			if strings.TrimSpace(updateGCPMonitoringConfigLocationFiltering) != "" {
				locations, err := gcpmonitoringconfig.ParseLocations(updateGCPMonitoringConfigLocationFiltering)
				if err != nil {
					return err
				}
				value.GoogleCloud.LocationFiltering = locations
			}
			if strings.TrimSpace(updateGCPMonitoringConfigFeatureSets) != "" {
				featureSets := gcpmonitoringconfig.SplitCSV(updateGCPMonitoringConfigFeatureSets)
				if len(featureSets) == 0 {
					return fmt.Errorf("--featureSets must contain at least one feature set")
				}
				value.FeatureSets = featureSets
			}

			payload := gcpmonitoringconfig.GCPMonitoringConfig{Scope: existing.Scope, Value: value}
			body, err := json.Marshal(payload)
			if err != nil {
				return fmt.Errorf("failed to prepare request payload: %w", err)
			}

			if dryRun(cmdContext(cmd)) {
				return newDryRunReport(cmd).
					Linef("Dry run: would update GCP monitoring config %s", existing.ObjectID).
					Detail("object_id", "%s", existing.ObjectID).
					Field("Locations", "%d", len(value.GoogleCloud.LocationFiltering)).
					Field("Feature sets", "%d", len(value.FeatureSets)).
					Print()
			}

			updated, err := handler.Update(existing.ObjectID, body)
			if err != nil {
				return err
			}

			output.FprintSuccess(currentStderr(cmdContext(cmd)), "GCP monitoring config updated: %s", updated.ObjectID)
			return nil
		},
	}
	c.Flags().StringVar(&updateGCPMonitoringConfigName, "name", "", "Monitoring config name/description (used when ID argument is not provided)")
	c.Flags().StringVar(&updateGCPMonitoringConfigLocationFiltering, "locationFiltering", "", "Comma-separated locations to monitor, or 'all' to remove the location filter")
	c.Flags().StringVar(&updateGCPMonitoringConfigFeatureSets, "featureSets", "", "Comma-separated feature sets")
	c.Flags().StringVar(&updateGCPMonitoringConfigFeatureSets, "featuresets", "", "Alias for --featureSets")
	// At least one is required and a flag left out keeps the stored value, so
	// an explicitly empty one is rejected rather than read as "left out".
	for _, name := range []string{"locationFiltering", "featureSets", "featuresets"} {
		rejectEmptyFlag(c, name)
	}
	// Every path through this command needs a flag the rename takes away
	// (both --locationFiltering and --featureSets), so at a stable floor it has
	// no usable invocation left. Marking the command says that plainly instead
	// of hiding the flags and then failing on a "required" flag help no longer
	// lists. Renamed to kebab-case in 1.0 (contrib
	// breaking-changes/cloud-flags-kebab-case.md); the spelling aliases are
	// removed outright.
	stability.Mark(c, stability.Experimental, pre10Since)
	stability.MarkFlag(c, "locationFiltering", stability.Experimental, pre10Since)
	stability.MarkFlag(c, "featureSets", stability.Experimental, pre10Since)
	stability.MarkFlag(c, "featuresets", stability.Experimental, pre10Since)
	return c
}

func init() {
	updateGCPProviderCmd.AddCommand(updateGCPConnectionCmd)
	updateGCPProviderCmd.AddCommand(updateGCPMonitoringConfigCmd)
}
