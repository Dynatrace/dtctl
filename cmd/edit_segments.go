package cmd

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/dynatrace-oss/dtctl/pkg/output"
	"github.com/dynatrace-oss/dtctl/pkg/resources/segment"
	"github.com/dynatrace-oss/dtctl/pkg/safety"
	"github.com/dynatrace-oss/dtctl/pkg/stability"
	"github.com/dynatrace-oss/dtctl/pkg/util/format"
)

// editSegmentCmd edits a filter segment
var editSegmentCmd = newEditSegmentCmd()

func newEditSegmentCmd() *cobra.Command {
	c := &cobra.Command{
		Use:     "segment <uid>",
		Aliases: []string{"seg", "filter-segment", "filter-segments"},
		Short:   "Edit a filter segment",
		Long: `Edit a filter segment by opening it in your default editor.

The segment will be fetched, opened in your editor (defined by EDITOR env var,
defaults to vim), and updated when you save and close the editor.

By default, resources are edited in YAML format for better readability.
Use --format=json to edit in JSON format.

Examples:
  # Edit a segment in YAML (default)
  dtctl edit segment <uid>

  # Edit a segment in JSON
  dtctl edit segment <uid> --format=json
`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			uid := args[0]

			cfg, c, err := setupClient(cmdContext(cmd))
			if err != nil {
				return err
			}

			handler := segment.NewHandler(c)

			// Get segment to check ownership
			seg, err := handler.Get(uid)
			if err != nil {
				return err
			}

			// Determine ownership for safety check
			currentUserID, _ := c.CurrentUserID()
			ownership := safety.DetermineOwnership(seg.Owner, currentUserID)

			// Safety check with actual ownership
			if err := checkSafety(cmdContext(cmd), cfg, safety.OperationUpdate, ownership); err != nil {
				return err
			}

			// Get the segment as raw JSON
			data, err := handler.GetRaw(uid)
			if err != nil {
				return err
			}

			// Get format preference
			editFormat, _ := cmd.Flags().GetString("format")
			var editData []byte
			var fileExt string

			if editFormat == "yaml" {
				// Convert JSON to YAML for editing
				editData, err = format.JSONToYAML(data)
				if err != nil {
					return fmt.Errorf("failed to convert to YAML: %w", err)
				}
				fileExt = "*.yaml"
			} else {
				// Pretty print JSON for editing
				editData, err = format.PrettyJSON(data)
				if err != nil {
					return fmt.Errorf("failed to format JSON: %w", err)
				}
				fileExt = "*.json"
			}

			// Create a temp file with appropriate extension
			tmpfile, err := os.CreateTemp("", "dtctl-segment-"+fileExt)
			if err != nil {
				return fmt.Errorf("failed to create temp file: %w", err)
			}
			defer func() {
				_ = os.Remove(tmpfile.Name())
			}()

			if _, err := tmpfile.Write(editData); err != nil {
				return fmt.Errorf("failed to write temp file: %w", err)
			}
			if err := tmpfile.Close(); err != nil {
				return fmt.Errorf("failed to close temp file: %w", err)
			}

			// Open the editor (single gateway; enforces the Editor capability)
			if err := launchEditor(cmdContext(cmd), cfg.Preferences.Editor, tmpfile.Name()); err != nil {
				return err
			}

			// Read the edited file
			editedData, err := os.ReadFile(tmpfile.Name())
			if err != nil {
				return fmt.Errorf("failed to read edited file: %w", err)
			}

			// Convert edited data to JSON (auto-detect format)
			jsonData, err := format.ValidateAndConvert(editedData)
			if err != nil {
				return fmt.Errorf("invalid format: %w", err)
			}

			// Check if anything changed
			var originalCompact, editedCompact bytes.Buffer
			if err := json.Compact(&originalCompact, data); err != nil {
				return fmt.Errorf("failed to compact original JSON: %w", err)
			}
			if err := json.Compact(&editedCompact, jsonData); err != nil {
				return fmt.Errorf("failed to compact edited JSON: %w", err)
			}

			if bytes.Equal(originalCompact.Bytes(), editedCompact.Bytes()) {
				fmt.Fprintln(currentStdout(cmdContext(cmd)), "Edit cancelled, no changes made.")
				return nil
			}

			// Update the segment
			if err := handler.Update(uid, seg.Version, jsonData); err != nil {
				return err
			}

			output.FprintSuccess(currentStderr(cmdContext(cmd)), "Segment %q updated", seg.Name)
			return nil
		},
	}
	c.Flags().StringP("format", "", "yaml", "edit format (yaml|json)")
	stability.MarkStable(c)
	return c
}

func init() {
}

// Declared stable: the invocation and output contract of this command is
// additive-only. Stable is never implied -- see AGENTS.md "Stability Tiers".
func init() {
}
