package cmd

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/dynatrace-oss/dtctl/pkg/output"
	"github.com/dynatrace-oss/dtctl/pkg/prompt"
	"github.com/dynatrace-oss/dtctl/pkg/resources/segment"
	"github.com/dynatrace-oss/dtctl/pkg/safety"
	"github.com/dynatrace-oss/dtctl/pkg/stability"
)

// getSegmentsCmd retrieves Grail filter segments
var getSegmentsCmd = newGetSegmentsCmd()

func newGetSegmentsCmd() *cobra.Command {
	c := &cobra.Command{
		Use:     "segments [uid]",
		Aliases: []string{"segment", "seg", "filter-segments", "filter-segment"},
		Short:   "Get Grail filter segments",
		Long: `Get Grail filter segments.

Examples:
  # List all segments
  dtctl get segments

  # Get a specific segment by UID
  dtctl get segment <uid>

  # Output as JSON
  dtctl get segments -o json

  # Wide output with description and owner
  dtctl get segments -o wide
`,
		RunE: func(cmd *cobra.Command, args []string) error {
			_, c, printer, err := setup(cmdContext(cmd))
			if err != nil {
				return err
			}

			handler := segment.NewHandler(c)

			// Get specific segment if UID provided
			if len(args) > 0 {
				seg, err := handler.Get(args[0])
				if err != nil {
					return err
				}
				return printer.Print(seg)
			}

			// List all segments
			list, err := handler.List()
			if err != nil {
				return err
			}

			return printer.PrintList(list.FilterSegments)
		},
	}
	stability.MarkStable(c)
	return c
}

// deleteSegmentCmd deletes a filter segment
var deleteSegmentCmd = newDeleteSegmentCmd()

func newDeleteSegmentCmd() *cobra.Command {
	var forceDelete bool
	c := &cobra.Command{
		Use:     "segment <uid>",
		Aliases: []string{"segments", "seg", "filter-segment", "filter-segments"},
		Short:   "Delete a Grail filter segment",
		Long: `Delete a Grail filter segment by UID.

Examples:
  # Delete a segment (requires typing the UID to confirm)
  dtctl delete segment <uid>

  # Delete with confirmation flag (non-interactive)
  dtctl delete segment <uid> --confirm=<uid>

  # Delete without confirmation (use with caution)
  dtctl delete segment <uid> -y
`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			uid := args[0]

			cfg, c, err := setupClient(cmdContext(cmd))
			if err != nil {
				return err
			}

			handler := segment.NewHandler(c)

			// Verify segment exists before prompting for confirmation
			seg, err := handler.Get(uid)
			if err != nil {
				return err
			}

			// Safety check with actual ownership
			currentUserID, _ := c.CurrentUserID()
			ownership := safety.DetermineOwnership(seg.Owner, currentUserID)
			if err := checkSafety(cmdContext(cmd), cfg, safety.OperationDelete, ownership); err != nil {
				return err
			}

			// Handle confirmation
			displayName := seg.Name
			if displayName == "" {
				displayName = uid
			}

			if dryRun(cmdContext(cmd)) {
				return deleteDryRun(cmd, "segment", displayName, uid)
			}

			confirmFlag, _ := cmd.Flags().GetString("confirm")
			if !forceDelete && !plainMode(cmdContext(cmd)) {
				if confirmFlag != "" {
					if !prompt.ValidateConfirmFlag(confirmFlag, uid) {
						return fmt.Errorf("confirmation value %q does not match segment UID %q", confirmFlag, uid)
					}
				} else {
					if !prompt.ConfirmDataDeletionWith(currentStdin(cmdContext(cmd)), currentStdout(cmdContext(cmd)), "segment", displayName) {
						fmt.Fprintln(currentStdout(cmdContext(cmd)), "Deletion cancelled")
						return nil
					}
				}
			}

			if err := handler.Delete(uid); err != nil {
				return err
			}

			output.FprintSuccess(currentStderr(cmdContext(cmd)), "Segment %q deleted", displayName)
			return nil
		},
	}
	c.Flags().BoolVarP(&forceDelete, "yes", "y", false, "Skip confirmation prompt")
	c.Flags().String("confirm", "", "Confirm deletion by providing the segment UID (for non-interactive use)")
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
