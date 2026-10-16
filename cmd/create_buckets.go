package cmd

import (
	"encoding/json"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/dynatrace-oss/dtctl/pkg/output"
	"github.com/dynatrace-oss/dtctl/pkg/resources/bucket"
	"github.com/dynatrace-oss/dtctl/pkg/safety"
	"github.com/dynatrace-oss/dtctl/pkg/stability"
	"github.com/dynatrace-oss/dtctl/pkg/util/format"
)

// createBucketCmd creates a Grail bucket
var createBucketCmd = newCreateBucketCmd()

func newCreateBucketCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "bucket --name <name> --table <table> --retention <days>",
		Short: "Create a Grail storage bucket",
		Long: `Create a new Grail storage bucket.

Examples:
  # Create a logs bucket with 35 days retention
  dtctl create bucket --name custom_logs --table logs --retention 35

  # Create with display name
  dtctl create bucket --name custom_logs --table logs --retention 35 --display-name "Custom Logs Bucket"

  # Create from a file
  dtctl create bucket -f bucket.yaml

  # Dry run to preview
  dtctl create bucket --name custom_logs --table logs --retention 35 --dry-run
`,
		Aliases: []string{"bkt"},
		RunE: func(cmd *cobra.Command, args []string) error {
			file, _ := cmd.Flags().GetString("file")
			name, _ := cmd.Flags().GetString("name")
			table, _ := cmd.Flags().GetString("table")
			retention, _ := cmd.Flags().GetInt("retention")
			displayName, _ := cmd.Flags().GetString("display-name")

			var req bucket.BucketCreate

			if file != "" {
				// Read from file
				fileData, err := readFileFlag(cmdContext(cmd), "file", file)
				if err != nil {
					return fmt.Errorf("failed to read file: %w", err)
				}

				jsonData, err := format.ValidateAndConvert(fileData)
				if err != nil {
					return fmt.Errorf("invalid file format: %w", err)
				}

				if err := json.Unmarshal(jsonData, &req); err != nil {
					return fmt.Errorf("failed to parse bucket definition: %w", err)
				}
			} else {
				// Use flags
				if name == "" {
					return fmt.Errorf("--name is required (or use -f to specify a file)")
				}
				if table == "" {
					return fmt.Errorf("--table is required (logs, events, or bizevents)")
				}
				if retention == 0 {
					return fmt.Errorf("--retention is required (1-3657 days)")
				}

				req = bucket.BucketCreate{
					BucketName:    name,
					Table:         table,
					RetentionDays: retention,
					DisplayName:   displayName,
				}
			}

			// Handle dry-run
			if dryRun(cmdContext(cmd)) {
				report := newDryRunReport(cmd).
					Linef("Dry run: would create bucket").
					Field("Name", "%s", req.BucketName).
					Field("Table", "%s", req.Table).
					Field("Retention", "%d days", req.RetentionDays)
				if req.DisplayName != "" {
					report.Field("Display Name", "%s", req.DisplayName)
				}
				return report.Print()
			}

			_, c, err := setupWithSafety(cmdContext(cmd), safety.OperationCreate)
			if err != nil {
				return err
			}

			handler := bucket.NewHandler(c)

			result, err := handler.Create(req)
			if err != nil {
				return fmt.Errorf("failed to create bucket: %w", err)
			}

			output.FprintSuccess(currentStderr(cmdContext(cmd)), "Bucket %q created (status: %s)", result.BucketName, result.Status)
			output.FprintInfo(currentStderr(cmdContext(cmd)), "Note: Bucket creation can take up to 1 minute to complete")
			return nil
		},
	}
	c.Flags().StringP("file", "f", "", "file containing bucket definition, or - for stdin")
	c.Flags().String("name", "", "bucket name (3-100 chars, lowercase alphanumeric, underscores, hyphens)")
	c.Flags().String("table", "", "table type (logs, events, or bizevents)")
	c.Flags().Int("retention", 0, "retention period in days (1-3657)")
	c.Flags().String("display-name", "", "display name for the bucket")
	stability.MarkStable(c)
	// Not needed with -f, so only an explicitly empty value is rejected here;
	// the command body reports a flag left out.
	rejectEmptyFlag(c, "name")
	rejectEmptyFlag(c, "table")
	rejectEmptyFlag(c, "retention")
	return c
}

func init() {
	// Bucket flags
}

// Declared stable: the invocation and output contract of this command is
// additive-only. Stable is never implied -- see AGENTS.md "Stability Tiers".
func init() {
}
