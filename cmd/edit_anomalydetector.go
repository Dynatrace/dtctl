package cmd

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/dynatrace-oss/dtctl/pkg/output"
	"github.com/dynatrace-oss/dtctl/pkg/resources/anomalydetector"
	"github.com/dynatrace-oss/dtctl/pkg/safety"
	"github.com/dynatrace-oss/dtctl/pkg/stability"
	"github.com/dynatrace-oss/dtctl/pkg/util/format"
)

// editAnomalyDetectorCmd edits an anomaly detector
var editAnomalyDetectorCmd = newEditAnomalyDetectorCmd()

func newEditAnomalyDetectorCmd() *cobra.Command {
	c := &cobra.Command{
		Use:     "anomaly-detector <id-or-title>",
		Aliases: []string{"ad"},
		Short:   "Edit a custom anomaly detector",
		Long: `Edit a custom anomaly detector by opening it in your default editor.

The detector configuration will be fetched, converted to the flattened YAML
format for readability, opened in your editor (defined by EDITOR env var,
defaults to vim), and updated when you save and close the editor.

Examples:
  # Edit by object ID
  dtctl edit anomaly-detector <object-id>

  # Edit by title
  dtctl edit anomaly-detector "High CPU on production hosts"
`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			identifier := args[0]

			cfg, c, err := setupWithSafety(cmdContext(cmd), safety.OperationUpdate)
			if err != nil {
				return err
			}

			handler := anomalydetector.NewHandler(c)

			// Resolve identifier to anomaly detector
			ad, err := resolveAnomalyDetector(handler, identifier)
			if err != nil {
				return err
			}

			// Convert to flattened YAML for editing
			flattened := anomalydetector.ToFlattenedYAML(ad.Value)
			flatJSON, err := json.Marshal(flattened)
			if err != nil {
				return fmt.Errorf("failed to marshal flattened value: %w", err)
			}

			editData, err := format.JSONToYAML(flatJSON)
			if err != nil {
				return fmt.Errorf("failed to convert to YAML: %w", err)
			}

			// Create a temp file
			tmpfile, err := os.CreateTemp("", "dtctl-anomaly-detector-*.yaml")
			if err != nil {
				return fmt.Errorf("failed to create temp file: %w", err)
			}
			defer os.Remove(tmpfile.Name())

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
			if err := json.Compact(&originalCompact, flatJSON); err != nil {
				return fmt.Errorf("failed to compact original JSON: %w", err)
			}
			if err := json.Compact(&editedCompact, jsonData); err != nil {
				return fmt.Errorf("failed to compact edited JSON: %w", err)
			}

			if bytes.Equal(originalCompact.Bytes(), editedCompact.Bytes()) {
				fmt.Fprintln(currentStdout(cmdContext(cmd)), "Edit cancelled, no changes made.")
				return nil
			}

			// Update the anomaly detector
			result, err := handler.Update(ad.ObjectID, jsonData)
			if err != nil {
				return err
			}

			output.FprintSuccess(currentStderr(cmdContext(cmd)), "Anomaly detector %q updated", result.Title)
			output.FprintInfo(currentStderr(cmdContext(cmd)), "  Object ID: %s", result.ObjectID)
			return nil
		},
	}
	stability.MarkStable(c)
	return c
}

// Declared stable: the invocation and output contract of this command is
// additive-only. Stable is never implied -- see AGENTS.md "Stability Tiers".
func init() {
}
