package cmd

import (
	"context"
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/dynatrace-oss/dtctl/pkg/client"
	"github.com/dynatrace-oss/dtctl/pkg/exec"
	"github.com/dynatrace-oss/dtctl/pkg/output"
	"github.com/dynatrace-oss/dtctl/pkg/resources/anomalydetector"
	"github.com/dynatrace-oss/dtctl/pkg/stability"
)

// describeAnomalyDetectorCmd shows details of an anomaly detector
var describeAnomalyDetectorCmd = newDescribeAnomalyDetectorCmd()

func newDescribeAnomalyDetectorCmd() *cobra.Command {
	c := &cobra.Command{
		Use:     "anomaly-detector <id-or-title>",
		Aliases: []string{"ad"},
		Short:   "Show details of a custom anomaly detector",
		Long: `Show detailed information about a custom anomaly detector including analyzer
configuration, event template, and recent problems.

Examples:
  # Describe by object ID
  dtctl describe anomaly-detector <object-id>

  # Describe by title
  dtctl describe anomaly-detector "High CPU on production hosts"

  # Output as JSON
  dtctl describe anomaly-detector <object-id> -o json
`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			identifier := args[0]

			_, c, printer, err := setup(cmdContext(cmd))
			if err != nil {
				return err
			}

			handler := anomalydetector.NewHandler(c)

			ad, err := resolveAnomalyDetector(handler, identifier)
			if err != nil {
				return err
			}

			// For table output, show detailed human-readable information
			if outputFormat(cmdContext(cmd)) == "table" {
				printAnomalyDetectorDescribe(cmdContext(cmd), c, ad)
				return nil
			}

			// For other formats, use standard printer
			ap := enrichAgent(printer, "describe", "anomaly-detector")
			if ap != nil {
				ap.Context().Suggestions = []string{
					"dtctl edit anomaly-detector <title> -- modify detector configuration",
					"dtctl delete anomaly-detector <title> -- remove detector",
					"dtctl get anomaly-detectors -- list all detectors",
				}
			}
			return printer.Print(ad)
		},
	}
	stability.MarkStable(c)
	return c
}

// printAnomalyDetectorDescribe prints detailed human-readable anomaly detector info.
func printAnomalyDetectorDescribe(ctx context.Context, c *client.Client, ad *anomalydetector.AnomalyDetector) {
	const w = 22
	output.FprintDescribeKV(currentStdout(ctx), "Title:", w, "%s", ad.Title)
	output.FprintDescribeKV(currentStdout(ctx), "Object ID:", w, "%s", ad.ObjectID)
	output.FprintDescribeKV(currentStdout(ctx), "Enabled:", w, "%v", ad.Enabled)
	output.FprintDescribeKV(currentStdout(ctx), "Source:", w, "%s", ad.Source)
	if ad.Description != "" {
		output.FprintDescribeKV(currentStdout(ctx), "Description:", w, "%s", ad.Description)
	}

	// Analyzer section
	fmt.Fprintln(currentStdout(ctx))
	output.FprintDescribeSection(currentStdout(ctx), "Analyzer:")

	if analyzer, ok := ad.Value["analyzer"].(map[string]any); ok {
		if name, ok := analyzer["name"].(string); ok {
			displayName := name
			if strings.Contains(name, "StaticThreshold") {
				displayName = "Static Threshold"
			} else if strings.Contains(name, "AutoAdaptive") {
				displayName = "Auto-Adaptive Baseline"
			}
			output.FprintDescribeKV(currentStdout(ctx), "  Type:", w, "%s", displayName)
		}

		input := anomalydetector.ExtractKVMap(analyzer, "input")
		if v, ok := input["alertCondition"]; ok {
			threshold := input["threshold"]
			if threshold != "" {
				output.FprintDescribeKV(currentStdout(ctx), "  Alert Condition:", w, "%s %s", v, threshold)
			} else {
				output.FprintDescribeKV(currentStdout(ctx), "  Alert Condition:", w, "%s", v)
			}
		}

		sliding := input["slidingWindow"]
		violating := input["violatingSamples"]
		if sliding != "" && violating != "" {
			output.FprintDescribeKV(currentStdout(ctx), "  Sliding Window:", w, "%s violating samples in %s minutes", violating, sliding)
		}
		if v, ok := input["dealertingSamples"]; ok {
			output.FprintDescribeKV(currentStdout(ctx), "  De-alerting Samples:", w, "%s", v)
		}
		if v, ok := input["alertOnMissingData"]; ok {
			output.FprintDescribeKV(currentStdout(ctx), "  Missing Data Alert:", w, "%s", v)
		}
		if v, ok := input["numberOfSignalFluctuations"]; ok {
			output.FprintDescribeKV(currentStdout(ctx), "  Signal Fluctuations:", w, "%s", v)
		}
	}

	// Query section
	if analyzer, ok := ad.Value["analyzer"].(map[string]any); ok {
		input := anomalydetector.ExtractKVMap(analyzer, "input")
		query := input["query"]
		if query == "" {
			query = input["query.expression"]
		}
		if query != "" {
			fmt.Fprintln(currentStdout(ctx))
			output.FprintDescribeSection(currentStdout(ctx), "Query:")
			fmt.Fprintf(currentStdout(ctx), "  %s\n", query)
		}
	}

	// Event template section
	if et, ok := ad.Value["eventTemplate"].(map[string]any); ok {
		props := anomalydetector.ExtractKVMap(et, "properties")
		if len(props) > 0 {
			fmt.Fprintln(currentStdout(ctx))
			output.FprintDescribeSection(currentStdout(ctx), "Event Template:")
			// Print in a consistent order
			orderedKeys := []string{"event.type", "event.name", "event.description", "dt.source_entity"}
			for _, k := range orderedKeys {
				if v, ok := props[k]; ok {
					output.FprintDescribeKV(currentStdout(ctx), fmt.Sprintf("  %s:", k), w, "%s", v)
					delete(props, k)
				}
			}
			// Print remaining keys
			for k, v := range props {
				output.FprintDescribeKV(currentStdout(ctx), fmt.Sprintf("  %s:", k), w, "%s", v)
			}
		}
	}

	// Recent problems cross-reference
	printAnomalyDetectorRecentProblems(ctx, c, ad)
}

// printAnomalyDetectorRecentProblems queries DQL for recent problems triggered by this detector.
func printAnomalyDetectorRecentProblems(ctx context.Context, c *client.Client, ad *anomalydetector.AnomalyDetector) {
	eventName := anomalydetector.ExtractEventName(ad.Value)
	if eventName == "" {
		return
	}

	executor := newDQLExecutor(ctx, c)

	// Build query — use prefix match if event name contains {dims:...} placeholders
	var query string
	if idx := strings.Index(eventName, "{"); idx >= 0 {
		// Extract static prefix before first placeholder
		prefix := eventName[:idx]
		if prefix != "" {
			query = fmt.Sprintf(`fetch dt.davis.problems, from:now()-7d
| filter contains(event.name, %q)
| sort timestamp desc
| limit 10
| fields display_id, event.status, event.start, event.end, event.category`, prefix)
		}
	}

	if query == "" {
		query = fmt.Sprintf(`fetch dt.davis.problems, from:now()-7d
| filter event.name == %q
| sort timestamp desc
| limit 10
| fields display_id, event.status, event.start, event.end, event.category`, eventName)
	}

	result, err := executor.ExecuteQuery(query)
	if err != nil {
		// Silently skip if DQL query fails (may not have permissions)
		return
	}

	records := exec.ExtractQueryRecords(result)
	if len(records) == 0 {
		fmt.Fprintln(currentStdout(ctx))
		output.FprintDescribeSection(currentStdout(ctx), "Recent Problems (last 7 days):")
		fmt.Fprintln(currentStdout(ctx), "  (no problems in the last 7 days)")
		return
	}

	fmt.Fprintln(currentStdout(ctx))
	output.FprintDescribeSection(currentStdout(ctx), "Recent Problems (last 7 days):")
	fmt.Fprintf(currentStdout(ctx), "  %-16s  %-8s  %-20s  %s\n", "DISPLAY ID", "STATUS", "START", "CATEGORY")
	for _, rec := range records {
		displayID := stringFromRecord(rec, "display_id")
		status := stringFromRecord(rec, "event.status")
		start := stringFromRecord(rec, "event.start")
		category := stringFromRecord(rec, "event.category")
		fmt.Fprintf(currentStdout(ctx), "  %-16s  %-8s  %-20s  %s\n", displayID, status, start, category)
	}
	fmt.Fprintf(currentStdout(ctx), "  (%d problems in the last 7 days)\n", len(records))
}

// Declared stable: the invocation and output contract of this command is
// additive-only. Stable is never implied -- see AGENTS.md "Stability Tiers".
func init() {
}
