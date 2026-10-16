package cmd

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/dynatrace-oss/dtctl/pkg/client"
	"github.com/dynatrace-oss/dtctl/pkg/config"
	"github.com/dynatrace-oss/dtctl/pkg/output"
	"github.com/dynatrace-oss/dtctl/pkg/resources/settings"
	"github.com/dynatrace-oss/dtctl/pkg/safety"
	"github.com/dynatrace-oss/dtctl/pkg/stability"
	"github.com/dynatrace-oss/dtctl/pkg/util/format"
)

// editSettingCmd edits a settings object
var editSettingCmd = newEditSettingCmd()

func newEditSettingCmd() *cobra.Command {
	c := &cobra.Command{
		Use:     "setting <object-id>",
		Aliases: []string{"settings"},
		Short:   "Edit a settings object",
		Long: `Edit a settings object by opening it in your default editor.

The settings object will be fetched, opened in your editor (defined by EDITOR env var,
defaults to vim), and updated when you save and close the editor.

By default, settings are edited in YAML format for better readability.
Use --format=json to edit in JSON format.

Examples:
  # Edit a settings object in YAML (default)
  dtctl edit setting vu9U3hXa3q0AAAABABRidWlsdGluOnJ1bS53ZWIubmFtZQ...

  # Edit a settings object in JSON
  dtctl edit setting vu9U3hXa3q0AAAABABRidWlsdGluOnJ1bS53ZWIubmFtZQ... --format=json

  # Validate the edited value against the API without saving
  # (the editor still opens; changes are validated but not persisted)
  dtctl edit setting vu9U3hXa3q0AAAABABRidWlsdGluOnJ1bS53ZWIubmFtZQ... --validate-only
`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			identifier := args[0]
			validateOnly, _ := cmd.Flags().GetBool("validate-only")

			var cfg *config.Config
			var c *client.Client
			var err error
			if validateOnly {
				cfg, c, err = setupClient(cmdContext(cmd))
			} else {
				cfg, c, err = setupWithSafety(cmdContext(cmd), safety.OperationUpdate)
			}
			if err != nil {
				return err
			}

			handler := settings.NewHandler(c)

			// Get the settings object as raw JSON
			data, err := handler.GetRaw(identifier)
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
			tmpfile, err := os.CreateTemp("", "dtctl-setting-"+fileExt)
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

			// Parse the edited JSON into a map for the Update call
			var value map[string]any
			if err := json.Unmarshal(jsonData, &value); err != nil {
				return fmt.Errorf("failed to parse edited JSON: %w", err)
			}

			if validateOnly {
				if err := handler.ValidateUpdate(identifier, value); err != nil {
					return fmt.Errorf("validation failed: %w", err)
				}
				output.FprintSuccess(currentStderr(cmdContext(cmd)), "Validation passed")
				return nil
			}

			// Update the settings object
			result, err := handler.Update(identifier, value)
			if err != nil {
				return err
			}

			output.FprintSuccess(currentStderr(cmdContext(cmd)), "Settings object %q updated", result.ObjectID)
			return nil
		},
	}
	c.Flags().StringP("format", "", "yaml", "edit format (yaml|json)")
	c.Flags().Bool("validate-only", false, "validate the edited value against the API without saving")
	stability.MarkStable(c)
	return c
}

func init() {
}

// Declared stable: the invocation and output contract of this command is
// additive-only. Stable is never implied -- see AGENTS.md "Stability Tiers".
func init() {
}
