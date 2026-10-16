package cmd

import (
	"encoding/json"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/dynatrace-oss/dtctl/pkg/output"
	"github.com/dynatrace-oss/dtctl/pkg/resources/awsconnection"
	"github.com/dynatrace-oss/dtctl/pkg/resources/awsmonitoringconfig"
	"github.com/dynatrace-oss/dtctl/pkg/safety"
	"github.com/dynatrace-oss/dtctl/pkg/stability"
)

var enableAWSProviderCmd = newEnableAWSProviderCmd()

func newEnableAWSProviderCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "aws",
		Short: "Enable AWS resources",
		RunE:  requireSubcommand,
	}
	stability.MarkStable(c)
	return c
}

var enableAWSMonitoringCmd = newEnableAWSMonitoringCmd()

func newEnableAWSMonitoringCmd() *cobra.Command {
	var enableAWSMonitoringName string
	var enableAWSMonitoringRoleArn string
	c := &cobra.Command{
		Use:     "monitoring [id]",
		Aliases: []string{"monitoring-config"},
		Short:   "Enable AWS monitoring configuration",
		Long: `Enable an AWS monitoring configuration by optionally patching the linked connection's
roleArn and then enabling the monitoring config in a single step.

Examples:
  dtctl enable aws monitoring --name "my-aws" --roleArn arn:aws:iam::123456789012:role/DynatraceMonitoringRole
  dtctl enable aws monitoring <id> --roleArn arn:aws:iam::123456789012:role/DynatraceMonitoringRole
  dtctl enable aws monitoring --name "my-aws"`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) == 0 && enableAWSMonitoringName == "" {
				return fmt.Errorf("provide monitoring config ID argument or --name")
			}
			if enableAWSMonitoringRoleArn != "" {
				if err := awsconnection.ValidateRoleArn(enableAWSMonitoringRoleArn); err != nil {
					return err
				}
			}

			if dryRun(cmdContext(cmd)) {
				name := enableAWSMonitoringName
				if len(args) > 0 {
					name = args[0]
				}
				report := newDryRunReport(cmd).OnStderr().
					Linef("Dry run: would resolve AWS monitoring config %q", name).
					Detail("monitoring_config", "%s", name)
				if enableAWSMonitoringRoleArn != "" {
					report.Linef("Dry run: would update linked AWS connection roleArn=%q", enableAWSMonitoringRoleArn).
						Detail("role_arn", "%s", enableAWSMonitoringRoleArn)
				}
				return report.Linef("Dry run: would enable monitoring config and all credentials").Print()
			}

			_, c, err := setupWithSafety(cmdContext(cmd), safety.OperationUpdate)
			if err != nil {
				return err
			}

			monitoringHandler := awsmonitoringconfig.NewHandler(c)
			connectionHandler := awsconnection.NewHandler(c)

			var existing *awsmonitoringconfig.AWSMonitoringConfig
			if len(args) > 0 {
				identifier := args[0]
				existing, err = monitoringHandler.FindByName(identifier)
				if err != nil {
					existing, err = monitoringHandler.Get(identifier)
					if err != nil {
						return fmt.Errorf("aws monitoring config %q not found by name or ID", identifier)
					}
				}
			} else {
				existing, err = monitoringHandler.FindByName(enableAWSMonitoringName)
				if err != nil {
					return err
				}
			}

			configName := existing.Value.Description
			if configName == "" {
				configName = existing.ObjectID
			}

			// Step 1: optionally patch the linked connection's roleArn.
			if enableAWSMonitoringRoleArn != "" {
				if len(existing.Value.Aws.Credentials) == 0 {
					return fmt.Errorf("monitoring config %q has no credentials configured", configName)
				}
				if len(existing.Value.Aws.Credentials) > 1 {
					output.FprintWarning(currentStderr(cmdContext(cmd)), "monitoring config %q has %d credentials — only the first connection will be updated; use 'dtctl update aws connection' for the others",
						configName, len(existing.Value.Aws.Credentials))
				}

				connectionID := existing.Value.Aws.Credentials[0].ConnectionID
				output.FprintInfo(currentStderr(cmdContext(cmd)), "Updating AWS connection %q with role ARN...", connectionID)

				conn, err := connectionHandler.Get(connectionID)
				if err != nil {
					return fmt.Errorf("failed to get linked connection %q: %w", connectionID, err)
				}

				value := conn.Value
				if value.Type != awsconnection.TypeRoleBased {
					return fmt.Errorf("unsupported aws connection type %q", value.Type)
				}
				if value.AwsRoleBasedAuthentication == nil {
					value.AwsRoleBasedAuthentication = &awsconnection.AwsRoleBasedAuthenticationConfig{
						Consumers: []string{awsconnection.DefaultConsumer},
					}
				}
				value.AwsRoleBasedAuthentication.RoleArn = enableAWSMonitoringRoleArn
				if len(value.AwsRoleBasedAuthentication.Consumers) == 0 {
					value.AwsRoleBasedAuthentication.Consumers = []string{awsconnection.DefaultConsumer}
				}

				if _, err := connectionHandler.Update(conn.ObjectID, value); err != nil {
					return fmt.Errorf("failed to update connection roleArn: %w", err)
				}
				output.FprintSuccess(currentStderr(cmdContext(cmd)), "AWS connection %q updated", connectionID)

				// Refresh credential AccountID from updated ARN.
				existing.Value.Aws.Credentials[0].AccountID = awsmonitoringconfig.AccountIDFromRoleArn(enableAWSMonitoringRoleArn)
			}

			// Step 2: enable monitoring config and all credentials.
			output.FprintInfo(currentStderr(cmdContext(cmd)), "Enabling AWS monitoring config %q...", configName)
			value := existing.Value
			value.Enabled = true
			for i := range value.Aws.Credentials {
				value.Aws.Credentials[i].Enabled = true
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

			output.FprintSuccess(currentStderr(cmdContext(cmd)), "AWS monitoring config %q enabled (%s)", configName, updated.ObjectID)
			return nil
		},
	}
	c.Flags().StringVar(&enableAWSMonitoringName, "name", "", "Monitoring config name/description (used when ID argument is not provided)")
	c.Flags().StringVar(&enableAWSMonitoringRoleArn, "roleArn", "", "AWS IAM role ARN to set on the linked connection (optional)")
	stability.MarkFlag(c, "roleArn", stability.Experimental, pre10Since)
	stability.MarkStable(c)
	return c
}

func init() {
	enableCmd.AddCommand(enableAWSProviderCmd)
	enableAWSProviderCmd.AddCommand(enableAWSMonitoringCmd)
	// Renamed to kebab-case in 1.0 (contrib breaking-changes/cloud-flags-kebab-case.md);
	// the spelling aliases are removed outright.
}

// Declared stable: the invocation and output contract of these commands is
// additive-only. Stable is never implied -- see AGENTS.md "Stability Tiers".
func init() {
}
