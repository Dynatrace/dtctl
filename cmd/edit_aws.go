package cmd

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/dynatrace-oss/dtctl/pkg/output"
	"github.com/dynatrace-oss/dtctl/pkg/resources/awsmonitoringconfig"
	"github.com/dynatrace-oss/dtctl/pkg/safety"
	"github.com/dynatrace-oss/dtctl/pkg/stability"
	"github.com/dynatrace-oss/dtctl/pkg/util/format"
)

var editAWSProviderCmd = newEditAWSProviderCmd()

func newEditAWSProviderCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "aws",
		Short: "Edit AWS resources",
		RunE:  requireSubcommand,
	}
	stability.MarkStable(c)
	return c
}

var editAWSMonitoringCmd = newEditAWSMonitoringCmd()

func newEditAWSMonitoringCmd() *cobra.Command {
	var editAWSMonitoringName string
	c := &cobra.Command{
		Use:     "monitoring [id]",
		Aliases: []string{"monitoring-config"},
		Short:   "Edit an AWS monitoring configuration",
		Long: `Edit an AWS monitoring configuration by opening it in your default editor.

The configuration will be fetched, opened in your editor (defined by EDITOR env var,
defaults to vim), and updated when you save and close the editor.

By default, resources are edited in YAML format for better readability.
Use --format=json to edit in JSON format.

Examples:
  dtctl edit aws monitoring <id>
  dtctl edit aws monitoring --name "my-aws-monitoring"
  dtctl edit aws monitoring --name "my-aws-monitoring" --format=json`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) == 0 && editAWSMonitoringName == "" {
				return fmt.Errorf("provide monitoring config ID argument or --name")
			}

			cfg, c, err := setupWithSafety(cmdContext(cmd), safety.OperationUpdate)
			if err != nil {
				return err
			}

			handler := awsmonitoringconfig.NewHandler(c)

			var existing *awsmonitoringconfig.AWSMonitoringConfig
			if len(args) > 0 {
				identifier := args[0]
				existing, err = handler.FindByName(identifier)
				if err != nil {
					existing, err = handler.Get(identifier)
					if err != nil {
						return fmt.Errorf("AWS monitoring config %q not found by name or ID", identifier)
					}
				}
			} else {
				existing, err = handler.FindByName(editAWSMonitoringName)
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

			tmpfile, err := os.CreateTemp("", "dtctl-aws-monitoring-"+fileExt)
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

			var editedValue awsmonitoringconfig.Value
			if err := json.Unmarshal(jsonData, &editedValue); err != nil {
				return fmt.Errorf("failed to parse edited config: %w", err)
			}
			payload := awsmonitoringconfig.AWSMonitoringConfig{Scope: existing.Scope, Value: editedValue}
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
			output.FprintSuccess(currentStderr(cmdContext(cmd)), "AWS monitoring config %q updated", configName)
			return nil
		},
	}
	c.Flags().StringP("format", "", "yaml", "edit format (yaml|json)")
	c.Flags().StringVar(&editAWSMonitoringName, "name", "", "Monitoring config name/description (used when ID argument is not provided)")
	stability.MarkStable(c)
	return c
}

func init() {
}

// Declared stable: the invocation and output contract of these commands is
// additive-only. Stable is never implied -- see AGENTS.md "Stability Tiers".
func init() {
}
