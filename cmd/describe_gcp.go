package cmd

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/dynatrace-oss/dtctl/pkg/client"
	"github.com/dynatrace-oss/dtctl/pkg/exec"
	"github.com/dynatrace-oss/dtctl/pkg/output"
	"github.com/dynatrace-oss/dtctl/pkg/resources/gcpconnection"
	"github.com/dynatrace-oss/dtctl/pkg/resources/gcpmonitoringconfig"
	"github.com/dynatrace-oss/dtctl/pkg/stability"
)

var describeGCPConnectionCmd = newDescribeGCPConnectionCmd()

func newDescribeGCPConnectionCmd() *cobra.Command {
	c := &cobra.Command{
		Use:     "connection <id>",
		Aliases: []string{"connections", "gcpconn"},
		Short:   "Show details of a GCP connection",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			_, c, printer, err := setup(cmdContext(cmd))
			if err != nil {
				return err
			}

			h := gcpconnection.NewHandler(c)
			item, err := h.Get(args[0])
			if err != nil {
				return err
			}

			// For table output, show detailed human-readable information
			if outputFormat(cmdContext(cmd)) == "table" {
				const w = 6
				output.FprintDescribeKV(currentStdout(cmdContext(cmd)), "ID:", w, "%s", item.ObjectID)
				output.FprintDescribeKV(currentStdout(cmdContext(cmd)), "Name:", w, "%s", item.Value.Name)
				output.FprintDescribeKV(currentStdout(cmdContext(cmd)), "Type:", w, "%s", item.Value.Type)
				if item.Value.ServiceAccountImpersonation != nil {
					output.FprintDescribeSection(currentStdout(cmdContext(cmd)), "Service Account Impersonation:")
					output.FprintDescribeKV(currentStdout(cmdContext(cmd)), "  Service Account ID:", 23, "%s", item.Value.ServiceAccountImpersonation.ServiceAccountID)
					output.FprintDescribeKV(currentStdout(cmdContext(cmd)), "  Consumers:", 23, "%v", item.Value.ServiceAccountImpersonation.Consumers)
				}

				return nil
			}

			// For other formats, use standard printer
			enrichAgent(printer, "describe", "gcp-connection")
			return printer.Print(item)
		},
	}
	stability.MarkStable(c)
	return c
}

var describeGCPMonitoringConfigCmd = newDescribeGCPMonitoringConfigCmd()

func newDescribeGCPMonitoringConfigCmd() *cobra.Command {
	c := &cobra.Command{
		Use:     "monitoring <id-or-name>",
		Aliases: []string{"monitoring-config", "monitoring-configs", "gcpmon"},
		Short:   "Show details of a GCP monitoring configuration",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			identifier := args[0]

			_, c, printer, err := setup(cmdContext(cmd))
			if err != nil {
				return err
			}

			h := gcpmonitoringconfig.NewHandler(c)
			connHandler := gcpconnection.NewHandler(c)

			item, err := h.FindByName(identifier)
			if err != nil {
				if strings.Contains(strings.ToLower(err.Error()), "not found") {
					item, err = h.Get(identifier)
					if err != nil {
						return fmt.Errorf("monitoring config with name/description or ID %q not found", identifier)
					}
				} else {
					return err
				}
			}

			// For table output, show detailed human-readable information
			if outputFormat(cmdContext(cmd)) == "table" {
				const w = 13
				output.FprintDescribeKV(currentStdout(cmdContext(cmd)), "ID:", w, "%s", item.ObjectID)
				output.FprintDescribeKV(currentStdout(cmdContext(cmd)), "Description:", w, "%s", item.Value.Description)
				output.FprintDescribeKV(currentStdout(cmdContext(cmd)), "Enabled:", w, "%v", item.Value.Enabled)
				output.FprintDescribeKV(currentStdout(cmdContext(cmd)), "Version:", w, "%s", item.Value.Version)
				output.FprintDescribeSection(currentStdout(cmdContext(cmd)), "Google Cloud Config:")
				output.FprintDescribeKV(currentStdout(cmdContext(cmd)), "  Location Filtering:", 23, "%v", item.Value.GoogleCloud.LocationFiltering)
				output.FprintDescribeKV(currentStdout(cmdContext(cmd)), "  Project Filtering:", 23, "%v", item.Value.GoogleCloud.ProjectFiltering)
				output.FprintDescribeKV(currentStdout(cmdContext(cmd)), "  Folder Filtering:", 23, "%v", item.Value.GoogleCloud.FolderFiltering)
				output.FprintDescribeKV(currentStdout(cmdContext(cmd)), "  Feature Sets:", 23, "%v", item.Value.FeatureSets)

				if len(item.Value.GoogleCloud.Credentials) > 0 {
					output.FprintDescribeSection(currentStdout(cmdContext(cmd)), "  Credentials:")
					for _, cred := range item.Value.GoogleCloud.Credentials {
						output.FprintDescribeKV(currentStdout(cmdContext(cmd)), "    - Description:", 23, "%s", cred.Description)
						output.FprintDescribeKV(currentStdout(cmdContext(cmd)), "      Connection ID:", 23, "%s", cred.ConnectionID)
						output.FprintDescribeKV(currentStdout(cmdContext(cmd)), "      Service Account:", 23, "%s", cred.ServiceAccount)
					}
				}

				if principal, principalErr := connHandler.GetDynatracePrincipal(); principalErr == nil {
					output.FprintDescribeSection(currentStdout(cmdContext(cmd)), "Dynatrace:")
					output.FprintDescribeKV(currentStdout(cmdContext(cmd)), "  Principal ID:", 17, "%s", principal.ObjectID)
					if principal.Principal != "" {
						output.FprintDescribeKV(currentStdout(cmdContext(cmd)), "  Principal:", 17, "%s", principal.Principal)
					}
				}

				printGCPMonitoringConfigStatus(cmdContext(cmd), c, item.ObjectID)

				return nil
			}

			// For other formats, use standard printer
			enrichAgent(printer, "describe", "gcp-monitoring")
			return printer.Print(item)
		},
	}
	stability.MarkStable(c)
	return c
}

func printGCPMonitoringConfigStatus(ctx context.Context, c *client.Client, configID string) {
	executor := newDQLExecutor(ctx, c)

	smartscapeQuery := fmt.Sprintf(`timeseries sum(dt.sfm.da.gcp.smartscape.updates.count), interval:1h, by:{dt.config.id}
| filter dt.config.id == %q`, configID)
	metricsQuery := fmt.Sprintf(`timeseries sum(dt.sfm.da.gcp.metric.data_points.count), interval:1h, by:{dt.config.id}
| filter dt.config.id == %q`, configID)
	eventsQuery := fmt.Sprintf(`fetch dt.system.events
| filter event.kind == "DATA_ACQUISITION_EVENT"
| filter da.clouds.configurationId == %q
| sort timestamp desc
| limit 100`, configID)

	fmt.Fprintln(currentStdout(ctx))
	output.FprintDescribeSection(currentStdout(ctx), "Status:")

	smartscapeResult, err := executor.ExecuteQuery(smartscapeQuery)
	if err != nil {
		fmt.Fprintf(currentStdout(ctx), "  Smartscape updates: query failed (%v)\n", err)
	} else {
		smartscapeRecords := exec.ExtractQueryRecords(smartscapeResult)
		smartscapeMetricConfigID := configID
		for _, rec := range smartscapeRecords {
			candidate := stringFromRecord(rec, "dt.config.id")
			if candidate != "" {
				smartscapeMetricConfigID = candidate
				break
			}
		}
		fmt.Fprintf(currentStdout(ctx), "  Smartscape metric config ID: %s\n", smartscapeMetricConfigID)
		if latest, ok := exec.ExtractLatestPointFromTimeseries(smartscapeRecords, "sum(dt.sfm.da.gcp.smartscape.updates.count)"); ok {
			if !latest.Timestamp.IsZero() {
				fmt.Fprintf(currentStdout(ctx), "  Smartscape updates (latest sum, 1h): %.2f at %s\n", latest.Value, latest.Timestamp.Format(time.RFC3339))
			} else {
				fmt.Fprintf(currentStdout(ctx), "  Smartscape updates (latest sum, 1h): %.2f\n", latest.Value)
			}
		} else {
			fmt.Fprintln(currentStdout(ctx), "  Smartscape updates: no data")
		}
	}

	metricsResult, err := executor.ExecuteQuery(metricsQuery)
	if err != nil {
		fmt.Fprintf(currentStdout(ctx), "  Metrics ingest: query failed (%v)\n", err)
	} else {
		metricsRecords := exec.ExtractQueryRecords(metricsResult)
		if latest, ok := exec.ExtractLatestPointFromTimeseries(metricsRecords, "sum(dt.sfm.da.gcp.metric.data_points.count)"); ok {
			if !latest.Timestamp.IsZero() {
				fmt.Fprintf(currentStdout(ctx), "  Metrics ingest (latest sum, 1h): %.2f at %s\n", latest.Value, latest.Timestamp.Format(time.RFC3339))
			} else {
				fmt.Fprintf(currentStdout(ctx), "  Metrics ingest (latest sum, 1h): %.2f\n", latest.Value)
			}
		} else {
			fmt.Fprintln(currentStdout(ctx), "  Metrics ingest: no data")
		}
	}

	eventsResult, err := executor.ExecuteQuery(eventsQuery)
	if err != nil {
		fmt.Fprintf(currentStdout(ctx), "  Events: query failed (%v)\n", err)
		return
	}

	eventRecords := exec.ExtractQueryRecords(eventsResult)
	if len(eventRecords) == 0 {
		fmt.Fprintln(currentStdout(ctx), "  Events: no recent data acquisition events")
		return
	}

	latestStatus := stringFromRecord(eventRecords[0], "da.clouds.status")
	if latestStatus == "" {
		latestStatus = "UNKNOWN"
	}
	fmt.Fprintf(currentStdout(ctx), "  Latest event status: %s\n", latestStatus)

	fmt.Fprintln(currentStdout(ctx))
	output.FprintDescribeSection(currentStdout(ctx), "Recent events:")
	fmt.Fprintf(currentStdout(ctx), "%-35s  %s\n", "TIMESTAMP", "DA.CLOUDS.CONTENT")
	for _, rec := range eventRecords {
		timestamp := stringFromRecord(rec, "timestamp")
		content := stringFromRecord(rec, "da.clouds.content")
		if content == "" {
			content = "-"
		}
		fmt.Fprintf(currentStdout(ctx), "%-35s  %s\n", timestamp, content)
	}
}

func init() {
	describeGCPProviderCmd.AddCommand(describeGCPConnectionCmd)
	describeGCPProviderCmd.AddCommand(describeGCPMonitoringConfigCmd)
}

// Declared stable: the invocation and output contract of these commands is
// additive-only. Stable is never implied -- see AGENTS.md "Stability Tiers".
func init() {
}
