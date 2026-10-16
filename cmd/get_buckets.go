package cmd

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/dynatrace-oss/dtctl/pkg/output"
	"github.com/dynatrace-oss/dtctl/pkg/prompt"
	"github.com/dynatrace-oss/dtctl/pkg/resources/bucket"
	"github.com/dynatrace-oss/dtctl/pkg/safety"
	"github.com/dynatrace-oss/dtctl/pkg/stability"
)

// getBucketsCmd retrieves Grail buckets
var getBucketsCmd = newGetBucketsCmd()

func newGetBucketsCmd() *cobra.Command {
	c := &cobra.Command{
		Use:     "buckets [name]",
		Aliases: []string{"bucket", "bkt"},
		Short:   "Get Grail storage buckets",
		Long: `Get Grail storage buckets.

Examples:
  # List all buckets
  dtctl get buckets

  # Get a specific bucket
  dtctl get bucket <bucket-name>

  # Output as JSON
  dtctl get buckets -o json
`,
		RunE: func(cmd *cobra.Command, args []string) error {
			_, c, printer, err := setup(cmdContext(cmd))
			if err != nil {
				return err
			}

			handler := bucket.NewHandler(c)

			// Get specific bucket if name provided
			if len(args) > 0 {
				b, err := handler.Get(args[0])
				if err != nil {
					return err
				}
				return printer.Print(b)
			}

			// List all buckets
			list, err := handler.List()
			if err != nil {
				return err
			}

			return printer.PrintList(list.Buckets)
		},
	}
	stability.MarkStable(c)
	return c
}

// deleteBucketCmd deletes a bucket
var deleteBucketCmd = newDeleteBucketCmd()

func newDeleteBucketCmd() *cobra.Command {
	var forceDelete bool
	c := &cobra.Command{
		Use:     "bucket <bucket-name>",
		Aliases: []string{"buckets", "bkt"},
		Short:   "Delete a Grail storage bucket",
		Long: `Delete a Grail storage bucket by name.

WARNING: This operation is irreversible and will delete all data in the bucket.

Examples:
  # Delete a bucket (requires typing the name to confirm)
  dtctl delete bucket <bucket-name>

  # Delete with confirmation flag (non-interactive)
  dtctl delete bucket <bucket-name> --confirm=<bucket-name>

  # Delete without confirmation (use with caution)
  dtctl delete bucket <bucket-name> -y
`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			bucketName := args[0]

			_, c, err := setupWithSafety(cmdContext(cmd), safety.OperationDeleteBucket)
			if err != nil {
				return err
			}

			handler := bucket.NewHandler(c)

			// Verify bucket exists before prompting for confirmation
			if _, err := handler.Get(bucketName); err != nil {
				return err
			}

			if dryRun(cmdContext(cmd)) {
				return deleteDryRun(cmd, "bucket", "", bucketName)
			}

			// Handle confirmation for data deletion
			confirmFlag, _ := cmd.Flags().GetString("confirm")
			if !forceDelete && !plainMode(cmdContext(cmd)) {
				// If --confirm flag provided, validate it matches the bucket name
				if confirmFlag != "" {
					if !prompt.ValidateConfirmFlag(confirmFlag, bucketName) {
						return fmt.Errorf("confirmation value %q does not match bucket name %q", confirmFlag, bucketName)
					}
				} else {
					// Interactive confirmation - require typing the bucket name
					if !prompt.ConfirmDataDeletionWith(currentStdin(cmdContext(cmd)), currentStdout(cmdContext(cmd)), "bucket", bucketName) {
						fmt.Fprintln(currentStdout(cmdContext(cmd)), "Deletion cancelled")
						return nil
					}
				}
			}

			if err := handler.Delete(bucketName); err != nil {
				return err
			}

			output.FprintSuccess(currentStderr(cmdContext(cmd)), "Bucket %q deletion initiated (async operation)", bucketName)
			return nil
		},
	}
	c.Flags().BoolVarP(&forceDelete, "yes", "y", false, "Skip confirmation prompt")
	c.Flags().String("confirm", "", "Confirm deletion by providing the bucket name (for non-interactive use)")
	stability.MarkStable(c)
	return c
}

func init() {
	// Delete confirmation flags
}

// Declared stable: the invocation and output contract of these commands is
// additive-only. Stable is never implied -- see AGENTS.md "Stability Tiers".
func init() {
}
