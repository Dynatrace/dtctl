package cmd

import (
	"encoding/json"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/dynatrace-oss/dtctl/pkg/output"
	"github.com/dynatrace-oss/dtctl/pkg/resources/awsmonitoringconfig"
	"github.com/dynatrace-oss/dtctl/pkg/safety"
	"github.com/dynatrace-oss/dtctl/pkg/stability"
)

var disableAWSProviderCmd = newDisableAWSProviderCmd()

func newDisableAWSProviderCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "aws",
		Short: "Disable AWS resources",
		RunE:  requireSubcommand,
	}
	stability.MarkStable(c)
	return c
}

var disableAWSMonitoringCmd = newDisableAWSMonitoringCmd()

func newDisableAWSMonitoringCmd() *cobra.Command {
	var disableAWSMonitoringName string
	c := &cobra.Command{
		Use:     "monitoring [id]",
		Aliases: []string{"monitoring-config"},
		Short:   "Disable AWS monitoring configuration",
		Long: `Disable an AWS monitoring configuration by setting it and all its credentials
to disabled in a single step.

The monitoring configuration and linked connection are preserved — only the
enabled flag is toggled off.

Examples:
  dtctl disable aws monitoring --name "my-aws-monitoring"
  dtctl disable aws monitoring <id>`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) == 0 && disableAWSMonitoringName == "" {
				return fmt.Errorf("provide monitoring config ID argument or --name")
			}

			if dryRun(cmdContext(cmd)) {
				name := disableAWSMonitoringName
				if len(args) > 0 {
					name = args[0]
				}
				return newDryRunReport(cmd).OnStderr().
					Linef("Dry run: would resolve AWS monitoring config %q", name).
					Detail("monitoring_config", "%s", name).
					Linef("Dry run: would disable monitoring config and all credentials").
					Print()
			}

			_, c, err := setupWithSafety(cmdContext(cmd), safety.OperationUpdate)
			if err != nil {
				return err
			}

			monitoringHandler := awsmonitoringconfig.NewHandler(c)

			var existing *awsmonitoringconfig.AWSMonitoringConfig
			if len(args) > 0 {
				identifier := args[0]
				existing, err = monitoringHandler.FindByName(identifier)
				if err != nil {
					existing, err = monitoringHandler.Get(identifier)
					if err != nil {
						return fmt.Errorf("AWS monitoring config %q not found by name or ID", identifier)
					}
				}
			} else {
				existing, err = monitoringHandler.FindByName(disableAWSMonitoringName)
				if err != nil {
					return err
				}
			}

			configName := existing.Value.Description
			if configName == "" {
				configName = existing.ObjectID
			}

			output.FprintInfo(currentStderr(cmdContext(cmd)), "Disabling AWS monitoring config %q...", configName)
			value := existing.Value
			value.Enabled = false
			for i := range value.Aws.Credentials {
				value.Aws.Credentials[i].Enabled = false
			}

			payload := awsmonitoringconfig.AWSMonitoringConfig{Scope: existing.Scope, Value: value}
			body, err := json.Marshal(payload)
			if err != nil {
				return fmt.Errorf("failed to prepare request payload: %w", err)
			}

			updated, err := monitoringHandler.Update(existing.ObjectID, body)
			if err != nil {
				return err
			}

			output.FprintSuccess(currentStderr(cmdContext(cmd)), "AWS monitoring config %q disabled (%s)", configName, updated.ObjectID)
			return nil
		},
	}
	c.Flags().StringVar(&disableAWSMonitoringName, "name", "", "Monitoring config name/description (used when ID argument is not provided)")
	stability.MarkStable(c)
	return c
}

func init() {
	disableAWSProviderCmd.AddCommand(disableAWSMonitoringCmd)
}

// Declared stable: the invocation and output contract of these commands is
// additive-only. Stable is never implied -- see AGENTS.md "Stability Tiers".
func init() {
}
