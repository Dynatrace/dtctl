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
	"github.com/dynatrace-oss/dtctl/pkg/resources/azureconnection"
	"github.com/dynatrace-oss/dtctl/pkg/resources/azuremonitoringconfig"
	"github.com/dynatrace-oss/dtctl/pkg/stability"
)

// describeCmd represents the describe command
var describeCmd = newDescribeCmd()

func newDescribeCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "describe",
		Short: "Show details of a specific resource",
		Long: `Show detailed information about a specific resource.

Unlike 'get', which outputs a list or a raw resource definition, 'describe'
provides a human-readable summary with contextual details: trigger
configuration for workflows, section counts for dashboards, retention
policies for buckets, etc.

Supported resources:
  workflows (wf)          workflow-executions (wfe)  dashboards (dash, db)
  notebooks (nb)          slos                       settings
  settings-schemas        buckets (bkt)              apps
  functions (fn, func)    intents                    edgeconnect (ec)
  users                   groups                     lookup-tables (lu)
  trash                   azure connection           azure monitoring
  extensions (ext)        extension-configs (extcfg) hub-extensions
  analyzers (az)          api                        environment
  license`,
		Example: `  # Describe a workflow to see its trigger and task details
  dtctl describe workflow my-workflow

  # Describe a dashboard by name
  dtctl describe dashboard "My Dashboard"

  # Describe a bucket to see retention and schema info
  dtctl describe bucket default

  # Describe an SLO to see its evaluation status
  dtctl describe slo <slo-id>`,
		RunE: requireSubcommand,
	}
	stability.MarkStable(c)
	return c
}

var describeAzureProviderCmd = newDescribeAzureProviderCmd()

func newDescribeAzureProviderCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "azure",
		Short: "Describe Azure resources",
		RunE:  requireSubcommand,
	}
	stability.MarkStable(c)
	return c
}

var describeAWSProviderCmd = newDescribeAWSProviderCmd()

func newDescribeAWSProviderCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "aws",
		Short: "Describe AWS resources",
		RunE:  requireSubcommand,
	}
	stability.MarkStable(c)
	return c
}

var describeGCPProviderCmd = newDescribeGCPProviderCmd()

func newDescribeGCPProviderCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "gcp",
		Short: "Describe GCP resources (Preview)",
		RunE:  requireSubcommand,
	}
	stability.MarkStable(c)
	attachPreviewNotice(c, "GCP")
	return c
}

// formatDuration formats seconds into a human-readable duration
func formatDuration(seconds int) string {
	if seconds < 60 {
		return fmt.Sprintf("%ds", seconds)
	}
	if seconds < 3600 {
		m := seconds / 60
		s := seconds % 60
		if s == 0 {
			return fmt.Sprintf("%dm", m)
		}
		return fmt.Sprintf("%dm%ds", m, s)
	}
	h := seconds / 3600
	m := (seconds % 3600) / 60
	if m == 0 {
		return fmt.Sprintf("%dh", h)
	}
	return fmt.Sprintf("%dh%dm", h, m)
}

// formatBytes formats bytes into a human-readable string
func formatBytes(bytes int64) string {
	const unit = 1024
	if bytes < unit {
		return fmt.Sprintf("%d B", bytes)
	}
	div, exp := int64(unit), 0
	for n := bytes / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(bytes)/float64(div), "KMGTPE"[exp])
}

// describeAzureConnectionCmd shows details of an Azure connection (credential)
var describeAzureConnectionCmd = newDescribeAzureConnectionCmd()

func newDescribeAzureConnectionCmd() *cobra.Command {
	c := &cobra.Command{
		Use:     "connection <id>",
		Aliases: []string{"connections", "azconn"},
		Short:   "Show details of an Azure connection (credential)",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			_, c, printer, err := setup(cmdContext(cmd))
			if err != nil {
				return err
			}

			h := azureconnection.NewHandler(c)
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

				if item.Value.ClientSecret != nil {
					output.FprintDescribeSection(currentStdout(cmdContext(cmd)), "Client Secret Config:")
					output.FprintDescribeKV(currentStdout(cmdContext(cmd)), "  Application ID:", 19, "%s", item.Value.ClientSecret.ApplicationID)
					output.FprintDescribeKV(currentStdout(cmdContext(cmd)), "  Directory ID:", 19, "%s", item.Value.ClientSecret.DirectoryID)
					output.FprintDescribeKV(currentStdout(cmdContext(cmd)), "  Consumers:", 19, "%v", item.Value.ClientSecret.Consumers)
				}

				if item.Value.FederatedIdentityCredential != nil {
					output.FprintDescribeSection(currentStdout(cmdContext(cmd)), "Federated Identity Config:")
					output.FprintDescribeKV(currentStdout(cmdContext(cmd)), "  Consumers:", 14, "%v", item.Value.FederatedIdentityCredential.Consumers)
				}

				return nil
			}

			// For other formats, use standard printer
			enrichAgent(printer, "describe", "azure-connection")
			return printer.Print(item)
		},
	}
	stability.MarkStable(c)
	return c
}

// describeAzureMonitoringConfigCmd shows details of an Azure monitoring configuration
var describeAzureMonitoringConfigCmd = newDescribeAzureMonitoringConfigCmd()

func newDescribeAzureMonitoringConfigCmd() *cobra.Command {
	c := &cobra.Command{
		Use:     "monitoring <id-or-name>",
		Aliases: []string{"monitoring-config", "monitoring-configs", "azmon"},
		Short:   "Show details of an Azure monitoring configuration",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			identifier := args[0]

			_, c, printer, err := setup(cmdContext(cmd))
			if err != nil {
				return err
			}

			h := azuremonitoringconfig.NewHandler(c)

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
				output.FprintDescribeSection(currentStdout(cmdContext(cmd)), "Azure Config:")
				output.FprintDescribeKV(currentStdout(cmdContext(cmd)), "  Deployment Scope:", 32, "%s", item.Value.Azure.DeploymentScope)
				output.FprintDescribeKV(currentStdout(cmdContext(cmd)), "  Subscription Filtering Mode:", 32, "%s", item.Value.Azure.SubscriptionFilteringMode)
				output.FprintDescribeKV(currentStdout(cmdContext(cmd)), "  Configuration Mode:", 32, "%s", item.Value.Azure.ConfigurationMode)
				output.FprintDescribeKV(currentStdout(cmdContext(cmd)), "  Deployment Mode:", 32, "%s", item.Value.Azure.DeploymentMode)

				if len(item.Value.Azure.Credentials) > 0 {
					output.FprintDescribeSection(currentStdout(cmdContext(cmd)), "  Credentials:")
					for _, cred := range item.Value.Azure.Credentials {
						output.FprintDescribeKV(currentStdout(cmdContext(cmd)), "    - Description:", 21, "%s", cred.Description)
						output.FprintDescribeKV(currentStdout(cmdContext(cmd)), "      Connection ID:", 21, "%s", cred.ConnectionId)
						output.FprintDescribeKV(currentStdout(cmdContext(cmd)), "      Type:", 21, "%s", cred.Type)
					}
				}

				printAzureMonitoringConfigStatus(cmdContext(cmd), c, item.ObjectID)

				return nil
			}

			// For other formats, use standard printer
			enrichAgent(printer, "describe", "azure-monitoring")
			return printer.Print(item)
		},
	}
	stability.MarkStable(c)
	return c
}

func printAzureMonitoringConfigStatus(ctx context.Context, c *client.Client, configID string) {
	executor := newDQLExecutor(ctx, c)

	smartscapeQuery := fmt.Sprintf(`timeseries sum(dt.sfm.da.azure.smartscape.updates.count), interval:1h, by:{dt.config.id}
| filter dt.config.id == %q`, configID)
	metricsQuery := fmt.Sprintf(`timeseries sum(dt.sfm.da.azure.metric.data_points.count), interval:1h, by:{dt.config.id}
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
		if latest, ok := exec.ExtractLatestPointFromTimeseries(smartscapeRecords, "sum(dt.sfm.da.azure.smartscape.updates.count)"); ok {
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
		if latest, ok := exec.ExtractLatestPointFromTimeseries(metricsRecords, "sum(dt.sfm.da.azure.metric.data_points.count)"); ok {
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

func stringFromRecord(record map[string]interface{}, key string) string {
	if record == nil {
		return ""
	}
	value, ok := record[key]
	if !ok || value == nil {
		return ""
	}
	if s, ok := value.(string); ok {
		return s
	}
	return fmt.Sprintf("%v", value)
}

func init() {
	describeCmd.AddCommand(describeAzureProviderCmd)
	describeCmd.AddCommand(describeAWSProviderCmd)
	describeCmd.AddCommand(describeGCPProviderCmd)
	describeAzureProviderCmd.AddCommand(describeAzureConnectionCmd)
	describeAzureProviderCmd.AddCommand(describeAzureMonitoringConfigCmd)
	rootCmd.AddCommand(describeCmd)
	describeCmd.AddCommand(describeWorkflowCmd)
	describeCmd.AddCommand(describeSchedulingRuleCmd)
	describeCmd.AddCommand(describeBreakpointCmd)
	describeCmd.AddCommand(describeWorkflowExecutionCmd)
	describeCmd.AddCommand(describeDashboardCmd)
	describeCmd.AddCommand(describeNotebookCmd)
	describeCmd.AddCommand(describeTrashCmd)
	describeCmd.AddCommand(describeBucketCmd)
	describeCmd.AddCommand(describeLookupCmd)
	describeCmd.AddCommand(describeAppCmd)
	describeCmd.AddCommand(describeFunctionCmd)
	describeCmd.AddCommand(describeIntentCmd)
	describeCmd.AddCommand(describeEdgeConnectCmd)
	describeCmd.AddCommand(describeUserCmd)
	describeCmd.AddCommand(describeGroupCmd)
	describeCmd.AddCommand(describeSettingsCmd)
	describeCmd.AddCommand(describeSettingsSchemaCmd)
	describeCmd.AddCommand(describeSLOCmd)
	describeCmd.AddCommand(describeExtensionCmd)
	describeCmd.AddCommand(describeExtensionConfigCmd)
	describeCmd.AddCommand(describeDocumentCmd)
	describeCmd.AddCommand(describeSegmentCmd)
	describeCmd.AddCommand(describeAnomalyDetectorCmd)
	describeCmd.AddCommand(describeHubExtensionCmd)
	describeCmd.AddCommand(describeAnalyzerCmd)
	describeCmd.AddCommand(describeAPICmd)
	describeCmd.AddCommand(describeEnvironmentCmd)
	describeCmd.AddCommand(describeLicenseCmd)
}

// Declared stable: the invocation and output contract of these commands is
// additive-only. Stable is never implied -- see AGENTS.md "Stability Tiers".
