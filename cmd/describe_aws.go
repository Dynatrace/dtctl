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
	"github.com/dynatrace-oss/dtctl/pkg/resources/awsconnection"
	"github.com/dynatrace-oss/dtctl/pkg/resources/awsmonitoringconfig"
	"github.com/dynatrace-oss/dtctl/pkg/stability"
)

var describeAWSConnectionCmd = newDescribeAWSConnectionCmd()

func newDescribeAWSConnectionCmd() *cobra.Command {
	c := &cobra.Command{
		Use:     "connection <id-or-name>",
		Aliases: []string{"connections", "awsconn"},
		Short:   "Show details of an AWS connection",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			_, c, printer, err := setup(cmdContext(cmd))
			if err != nil {
				return err
			}

			h := awsconnection.NewHandler(c)
			identifier := args[0]
			item, err := h.FindByName(identifier)
			if err != nil {
				item, err = h.Get(identifier)
				if err != nil {
					return fmt.Errorf("aws connection with name or ID %q not found", identifier)
				}
			}

			if outputFormat(cmdContext(cmd)) == "table" {
				const w = 6
				output.FprintDescribeKV(currentStdout(cmdContext(cmd)), "ID:", w, "%s", item.ObjectID)
				output.FprintDescribeKV(currentStdout(cmdContext(cmd)), "Name:", w, "%s", item.Value.Name)
				output.FprintDescribeKV(currentStdout(cmdContext(cmd)), "Type:", w, "%s", item.Value.Type)
				if item.Value.AwsRoleBasedAuthentication != nil {
					output.FprintDescribeSection(currentStdout(cmdContext(cmd)), "Role-Based Auth Config:")
					output.FprintDescribeKV(currentStdout(cmdContext(cmd)), "  Role ARN:", 14, "%s", item.Value.AwsRoleBasedAuthentication.RoleArn)
					output.FprintDescribeKV(currentStdout(cmdContext(cmd)), "  Consumers:", 14, "%v", item.Value.AwsRoleBasedAuthentication.Consumers)
				}
				return nil
			}

			enrichAgent(printer, "describe", "aws-connection")
			return printer.Print(item)
		},
	}
	stability.MarkStable(c)
	return c
}

var describeAWSMonitoringConfigCmd = newDescribeAWSMonitoringConfigCmd()

func newDescribeAWSMonitoringConfigCmd() *cobra.Command {
	c := &cobra.Command{
		Use:     "monitoring <id-or-name>",
		Aliases: []string{"monitoring-config", "monitoring-configs", "awsmon"},
		Short:   "Show details of an AWS monitoring configuration",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			identifier := args[0]

			_, c, printer, err := setup(cmdContext(cmd))
			if err != nil {
				return err
			}

			h := awsmonitoringconfig.NewHandler(c)

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

			if outputFormat(cmdContext(cmd)) == "table" {
				const w = 13
				output.FprintDescribeKV(currentStdout(cmdContext(cmd)), "ID:", w, "%s", item.ObjectID)
				output.FprintDescribeKV(currentStdout(cmdContext(cmd)), "Description:", w, "%s", item.Value.Description)
				output.FprintDescribeKV(currentStdout(cmdContext(cmd)), "Enabled:", w, "%v", item.Value.Enabled)
				output.FprintDescribeKV(currentStdout(cmdContext(cmd)), "Version:", w, "%s", item.Value.Version)
				output.FprintDescribeSection(currentStdout(cmdContext(cmd)), "AWS Config:")
				output.FprintDescribeKV(currentStdout(cmdContext(cmd)), "  Deployment Region:", 25, "%s", item.Value.Aws.DeploymentRegion)
				output.FprintDescribeKV(currentStdout(cmdContext(cmd)), "  Deployment Scope:", 25, "%s", item.Value.Aws.DeploymentScope)
				output.FprintDescribeKV(currentStdout(cmdContext(cmd)), "  Configuration Mode:", 25, "%s", item.Value.Aws.ConfigurationMode)
				output.FprintDescribeKV(currentStdout(cmdContext(cmd)), "  Deployment Mode:", 25, "%s", item.Value.Aws.DeploymentMode)
				output.FprintDescribeKV(currentStdout(cmdContext(cmd)), "  Smartscape Enabled:", 25, "%v", item.Value.Aws.SmartscapeConfiguration.Enabled)
				output.FprintDescribeKV(currentStdout(cmdContext(cmd)), "  Metrics Enabled:", 25, "%v", item.Value.Aws.MetricsConfiguration.Enabled)
				output.FprintDescribeKV(currentStdout(cmdContext(cmd)), "  Metrics Regions:", 25, "%v", item.Value.Aws.MetricsConfiguration.Regions)
				output.FprintDescribeKV(currentStdout(cmdContext(cmd)), "  CW Logs Enabled:", 25, "%v", item.Value.Aws.CloudWatchLogsConfiguration.Enabled)
				output.FprintDescribeKV(currentStdout(cmdContext(cmd)), "  Region Filtering:", 25, "%v", item.Value.Aws.RegionFiltering)

				if len(item.Value.Aws.TagEnrichment) > 0 {
					output.FprintDescribeKV(currentStdout(cmdContext(cmd)), "  Tag Enrichment:", 25, "%v", item.Value.Aws.TagEnrichment)
				}

				if len(item.Value.Aws.Credentials) > 0 {
					output.FprintDescribeSection(currentStdout(cmdContext(cmd)), "  Credentials:")
					for _, cred := range item.Value.Aws.Credentials {
						output.FprintDescribeKV(currentStdout(cmdContext(cmd)), "    - Description:", 21, "%s", cred.Description)
						output.FprintDescribeKV(currentStdout(cmdContext(cmd)), "      Connection ID:", 21, "%s", cred.ConnectionID)
						output.FprintDescribeKV(currentStdout(cmdContext(cmd)), "      Account ID:", 21, "%s", cred.AccountID)
						output.FprintDescribeKV(currentStdout(cmdContext(cmd)), "      Enabled:", 21, "%v", cred.Enabled)
					}
				}

				if len(item.Value.Aws.Namespaces) > 0 {
					output.FprintDescribeSection(currentStdout(cmdContext(cmd)), "  Custom Namespaces:")
					for _, ns := range item.Value.Aws.Namespaces {
						output.FprintDescribeKV(currentStdout(cmdContext(cmd)), "    - Namespace:", 21, "%s", ns.Namespace)
						for _, m := range ns.Metrics {
							output.FprintDescribeKV(currentStdout(cmdContext(cmd)), "      Metric:", 21, "%s (%s) dims=%v agg=%v", m.Name, m.Unit, m.Dimensions, m.Aggregations)
						}
					}
				}

				printAWSMonitoringConfigStatus(cmdContext(cmd), c, item.ObjectID)
				return nil
			}

			enrichAgent(printer, "describe", "aws-monitoring")
			return printer.Print(item)
		},
	}
	stability.MarkStable(c)
	return c
}

func printAWSMonitoringConfigStatus(ctx context.Context, c *client.Client, configID string) {
	executor := newDQLExecutor(ctx, c)

	smartscapeQuery := fmt.Sprintf(`timeseries sum(dt.sfm.da.aws.smartscape.updates.count), interval:1h, by:{dt.config.id}
| filter dt.config.id == %q`, configID)
	metricsQuery := fmt.Sprintf(`timeseries sum(dt.sfm.da.aws.metric.data_points.count), interval:1h, by:{dt.config.id}
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
		records := exec.ExtractQueryRecords(smartscapeResult)
		if latest, ok := exec.ExtractLatestPointFromTimeseries(records, "sum(dt.sfm.da.aws.smartscape.updates.count)"); ok {
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
		records := exec.ExtractQueryRecords(metricsResult)
		if latest, ok := exec.ExtractLatestPointFromTimeseries(records, "sum(dt.sfm.da.aws.metric.data_points.count)"); ok {
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
	describeAWSProviderCmd.AddCommand(describeAWSConnectionCmd)
	describeAWSProviderCmd.AddCommand(describeAWSMonitoringConfigCmd)
}

// Declared stable: the invocation and output contract of these commands is
// additive-only. Stable is never implied -- see AGENTS.md "Stability Tiers".
func init() {
}
