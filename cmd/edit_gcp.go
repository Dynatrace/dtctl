package cmd

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/dynatrace-oss/dtctl/pkg/output"
	"github.com/dynatrace-oss/dtctl/pkg/resources/gcpmonitoringconfig"
	"github.com/dynatrace-oss/dtctl/pkg/safety"
	"github.com/dynatrace-oss/dtctl/pkg/stability"
	"github.com/dynatrace-oss/dtctl/pkg/util/format"
)

var editGCPProviderCmd = newEditGCPProviderCmd()

func newEditGCPProviderCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "gcp",
		Short: "Edit GCP resources (Preview)",
		RunE:  requireSubcommand,
	}
	stability.MarkStable(c)
	attachPreviewNotice(c, "GCP")
	return c
}

var editGCPMonitoringCmd = newEditGCPMonitoringCmd()

func newEditGCPMonitoringCmd() *cobra.Command {
	var editGCPMonitoringName string
	c := &cobra.Command{
		Use:     "monitoring [id]",
		Aliases: []string{"monitoring-config"},
		Short:   "Edit a GCP monitoring configuration",
		Long: `Edit a GCP monitoring configuration by opening it in your default editor.

The configuration will be fetched, opened in your editor (defined by EDITOR env var,
defaults to vim), and updated when you save and close the editor.

By default, resources are edited in YAML format for better readability.
Use --format=json to edit in JSON format.

Examples:
  dtctl edit gcp monitoring <id>
  dtctl edit gcp monitoring --name "my-gcp-monitoring"
  dtctl edit gcp monitoring --name "my-gcp-monitoring" --format=json`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) == 0 && editGCPMonitoringName == "" {
				return fmt.Errorf("provide monitoring config ID argument or --name")
			}

			cfg, c, err := setupWithSafety(cmdContext(cmd), safety.OperationUpdate)
			if err != nil {
				return err
			}

			handler := gcpmonitoringconfig.NewHandler(c)

			var existing *gcpmonitoringconfig.GCPMonitoringConfig
			if len(args) > 0 {
				identifier := args[0]
				existing, err = handler.FindByName(identifier)
				if err != nil {
					existing, err = handler.Get(identifier)
					if err != nil {
						return fmt.Errorf("GCP monitoring config %q not found by name or ID", identifier)
					}
				}
			} else {
				existing, err = handler.FindByName(editGCPMonitoringName)
				if err != nil {
					return err
				}
			}

			data, err := handler.GetRaw(existing.ObjectID)
			if err != nil {
				return err
			}

			editFormat, _ := cmd.Flags().GetString("format")
			var editData []byte
			var fileExt string

			if editFormat == "yaml" {
				editData, err = format.JSONToYAML(data)
				if err != nil {
					return fmt.Errorf("failed to convert to YAML: %w", err)
				}
				fileExt = "*.yaml"
			} else {
				editData, err = format.PrettyJSON(data)
				if err != nil {
					return fmt.Errorf("failed to format JSON: %w", err)
				}
				fileExt = "*.json"
			}

			tmpfile, err := os.CreateTemp("", "dtctl-gcp-monitoring-"+fileExt)
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

			editedData, err := os.ReadFile(tmpfile.Name())
			if err != nil {
				return fmt.Errorf("failed to read edited file: %w", err)
			}

			jsonData, err := format.ValidateAndConvert(editedData)
			if err != nil {
				return fmt.Errorf("invalid format: %w", err)
			}

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

			var editedValue gcpmonitoringconfig.Value
			if err := json.Unmarshal(jsonData, &editedValue); err != nil {
				return fmt.Errorf("failed to parse edited config: %w", err)
			}
			payload := gcpmonitoringconfig.GCPMonitoringConfig{Scope: existing.Scope, Value: editedValue}
			payloadBytes, err := json.Marshal(payload)
			if err != nil {
				return fmt.Errorf("failed to marshal payload: %w", err)
			}

			updated, err := handler.Update(existing.ObjectID, payloadBytes)
			if err != nil {
				return err
			}

			configName := updated.Value.Description
			if configName == "" {
				configName = updated.ObjectID
			}
			output.FprintSuccess(currentStderr(cmdContext(cmd)), "GCP monitoring config %q updated", configName)
			return nil
		},
	}
	c.Flags().StringP("format", "", "yaml", "edit format (yaml|json)")
	c.Flags().StringVar(&editGCPMonitoringName, "name", "", "Monitoring config name/description (used when ID argument is not provided)")
	stability.MarkStable(c)
	return c
}

// Declared stable: the invocation and output contract of these commands is
// additive-only. Stable is never implied -- see AGENTS.md "Stability Tiers".
