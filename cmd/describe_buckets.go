package cmd

import (
	"github.com/spf13/cobra"

	"github.com/dynatrace-oss/dtctl/pkg/output"
	"github.com/dynatrace-oss/dtctl/pkg/resources/bucket"
	"github.com/dynatrace-oss/dtctl/pkg/stability"
)

// describeBucketCmd shows detailed info about a bucket
var describeBucketCmd = newDescribeBucketCmd()

func newDescribeBucketCmd() *cobra.Command {
	c := &cobra.Command{
		Use:     "bucket <bucket-name>",
		Aliases: []string{"bkt"},
		Short:   "Show details of a Grail storage bucket",
		Long: `Show detailed information about a Grail storage bucket.

Examples:
  # Describe a bucket
  dtctl describe bucket default_logs
  dtctl describe bkt custom_logs
`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			bucketName := args[0]

			_, c, printer, err := setup(cmdContext(cmd))
			if err != nil {
				return err
			}

			handler := bucket.NewHandler(c)

			b, err := handler.Get(bucketName)
			if err != nil {
				return err
			}

			// For table output, show detailed human-readable information
			if outputFormat(cmdContext(cmd)) == "table" {
				const w = 16
				output.FprintDescribeKV(currentStdout(cmdContext(cmd)), "Name:", w, "%s", b.BucketName)
				output.FprintDescribeKV(currentStdout(cmdContext(cmd)), "Display Name:", w, "%s", b.DisplayName)
				output.FprintDescribeKV(currentStdout(cmdContext(cmd)), "Table:", w, "%s", b.Table)
				output.FprintDescribeKV(currentStdout(cmdContext(cmd)), "Status:", w, "%s", b.Status)
				output.FprintDescribeKV(currentStdout(cmdContext(cmd)), "Retention:", w, "%d days", b.RetentionDays)
				output.FprintDescribeKV(currentStdout(cmdContext(cmd)), "Updatable:", w, "%v", b.Updatable)
				output.FprintDescribeKV(currentStdout(cmdContext(cmd)), "Version:", w, "%d", b.Version)
				if b.MetricInterval != "" {
					output.FprintDescribeKV(currentStdout(cmdContext(cmd)), "Metric Interval:", w, "%s", b.MetricInterval)
				}
				if b.Records != nil {
					output.FprintDescribeKV(currentStdout(cmdContext(cmd)), "Records:", w, "%d", *b.Records)
				}
				if b.EstimatedUncompressedBytes != nil {
					output.FprintDescribeKV(currentStdout(cmdContext(cmd)), "Est. Size:", w, "%s", formatBytes(*b.EstimatedUncompressedBytes))
				}

				return nil
			}

			// For other formats, use standard printer
			enrichAgent(printer, "describe", "bucket")
			return printer.Print(b)
		},
	}
	stability.MarkStable(c)
	return c
}

// Declared stable: the invocation and output contract of this command is
// additive-only. Stable is never implied -- see AGENTS.md "Stability Tiers".
func init() {
}
