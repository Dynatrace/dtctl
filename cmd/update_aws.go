package cmd

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/Dynatrace/dtctl/pkg/output"
	"github.com/Dynatrace/dtctl/pkg/resources/awsconnection"
	"github.com/Dynatrace/dtctl/pkg/resources/awsmonitoringconfig"
	"github.com/Dynatrace/dtctl/pkg/safety"
	"github.com/Dynatrace/dtctl/pkg/stability"
)

var updateAWSConnectionCmd = newUpdateAWSConnectionCmd()

func newUpdateAWSConnectionCmd() *cobra.Command {
	var updateAWSConnectionName string
	var updateAWSConnectionRoleArn string
	c := &cobra.Command{
		Use:     "connection [id]",
		Aliases: []string{"connections"},
		Short:   "Update AWS connection from flags",
		Long: `Patch an existing AWS connection. Currently supports updating the IAM role ARN.

Examples:
  dtctl update aws connection --name "my-aws" --role-arn arn:aws:iam::123456789012:role/DynatraceMonitoringRole
  dtctl update aws connection <id> --role-arn arn:aws:iam::123456789012:role/DynatraceMonitoringRole`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := awsconnection.ValidateRoleArn(updateAWSConnectionRoleArn); err != nil {
				return err
			}

			_, c, err := setupWithSafety(cmdContext(cmd), safety.OperationUpdate)
			if err != nil {
				return err
			}

			handler := awsconnection.NewHandler(c)

			var existing *awsconnection.AWSConnection
			if len(args) > 0 {
				existing, err = handler.Get(args[0])
				if err != nil {
					return err
				}
			} else {
				if updateAWSConnectionName == "" {
					return fmt.Errorf("provide connection ID argument or --name")
				}
				existing, err = handler.FindByName(updateAWSConnectionName)
				if err != nil {
					return err
				}
			}

			value := existing.Value
			if value.Type != awsconnection.TypeRoleBased {
				return fmt.Errorf("unsupported aws connection type %q", value.Type)
			}
			if value.AwsRoleBasedAuthentication == nil {
				value.AwsRoleBasedAuthentication = &awsconnection.AwsRoleBasedAuthenticationConfig{
					Consumers: []string{awsconnection.DefaultConsumer},
				}
			}
			value.AwsRoleBasedAuthentication.RoleArn = updateAWSConnectionRoleArn
			if len(value.AwsRoleBasedAuthentication.Consumers) == 0 {
				value.AwsRoleBasedAuthentication.Consumers = []string{awsconnection.DefaultConsumer}
			}

			if dryRun(cmdContext(cmd)) {
				return newDryRunReport(cmd).
					Linef("Dry run: would update AWS connection %s", existing.ObjectID).
					Detail("object_id", "%s", existing.ObjectID).
					Field("Role ARN", "%s", updateAWSConnectionRoleArn).
					Print()
			}

			updated, err := handler.Update(existing.ObjectID, value)
			if err != nil {
				return err
			}

			output.FprintSuccess(currentStderr(cmdContext(cmd)), "AWS connection updated: %s", updated.ObjectID)
			return nil
		},
	}
	c.Flags().StringVar(&updateAWSConnectionName, "name", "", "AWS connection name (used when ID argument is not provided)")
	c.Flags().StringVar(&updateAWSConnectionRoleArn, "role-arn", "", "AWS IAM role ARN (required)")
	stability.Mark(c, stability.Experimental, pre10Since)
	stability.MarkFlag(c, "role-arn", stability.Experimental, cloudFlagsKebabSince)
	markFlagRequiredNonEmpty(c, "role-arn")
	return c
}

var updateAWSMonitoringConfigCmd = newUpdateAWSMonitoringConfigCmd()

func newUpdateAWSMonitoringConfigCmd() *cobra.Command {
	var updateAWSMonitoringConfigFeatureSets string
	var updateAWSMonitoringConfigName string
	var updateAWSMonitoringConfigRegions string
	c := &cobra.Command{
		Use:     "monitoring [id]",
		Aliases: []string{"monitoring-config"},
		Short:   "Update AWS monitoring config from flags",
		Long: `Update an AWS monitoring configuration by ID argument or by --name.

Examples:
  dtctl update aws monitoring --name "my-aws" --regions us-east-1,eu-central-1
  dtctl update aws monitoring --name "my-aws" --feature-sets EC2_essential,RDS_essential`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if strings.TrimSpace(updateAWSMonitoringConfigRegions) == "" &&
				strings.TrimSpace(updateAWSMonitoringConfigFeatureSets) == "" {
				return fmt.Errorf("at least one of --regions or --feature-sets is required")
			}

			_, c, err := setupWithSafety(cmdContext(cmd), safety.OperationUpdate)
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
						return fmt.Errorf("monitoring config with name/description or ID %q not found", identifier)
					}
				}
			} else {
				if updateAWSMonitoringConfigName == "" {
					return fmt.Errorf("provide config ID argument or --name")
				}
				existing, err = handler.FindByName(updateAWSMonitoringConfigName)
				if err != nil {
					return err
				}
			}

			value := existing.Value
			if strings.TrimSpace(updateAWSMonitoringConfigRegions) != "" {
				regions, err := awsmonitoringconfig.ParseRequiredRegions(updateAWSMonitoringConfigRegions)
				if err != nil {
					return err
				}
				value.Aws.MetricsConfiguration.Regions = regions
				value.Aws.RegionFiltering = regions
				value.Aws.CloudWatchLogsConfiguration.Regions = regions
				if value.Aws.DeploymentRegion == "" {
					value.Aws.DeploymentRegion = regions[0]
				}
			}
			if strings.TrimSpace(updateAWSMonitoringConfigFeatureSets) != "" {
				featureSets := awsmonitoringconfig.SplitCSV(updateAWSMonitoringConfigFeatureSets)
				if len(featureSets) == 0 {
					return fmt.Errorf("--feature-sets must contain at least one feature set")
				}
				value.FeatureSets = featureSets
			}

			payload := awsmonitoringconfig.AWSMonitoringConfig{Scope: existing.Scope, Value: value}
			body, err := json.Marshal(payload)
			if err != nil {
				return fmt.Errorf("failed to prepare request payload: %w", err)
			}

			if dryRun(cmdContext(cmd)) {
				return newDryRunReport(cmd).
					Linef("Dry run: would update AWS monitoring config %s", existing.ObjectID).
					Detail("object_id", "%s", existing.ObjectID).
					Field("Regions", "%s", strings.Join(value.Aws.RegionFiltering, ",")).
					Field("Feature sets", "%d", len(value.FeatureSets)).
					Print()
			}

			updated, err := handler.Update(existing.ObjectID, body)
			if err != nil {
				return err
			}

			output.FprintSuccess(currentStderr(cmdContext(cmd)), "AWS monitoring config updated: %s", updated.ObjectID)
			return nil
		},
	}
	c.Flags().StringVar(&updateAWSMonitoringConfigName, "name", "", "Monitoring config name/description (used when ID argument is not provided)")
	c.Flags().StringVar(&updateAWSMonitoringConfigRegions, "regions", "", "Comma-separated AWS regions")
	c.Flags().StringVar(&updateAWSMonitoringConfigFeatureSets, "feature-sets", "", "Comma-separated feature sets")
	stability.MarkFlag(c, "feature-sets", stability.Experimental, cloudFlagsKebabSince)
	stability.MarkStable(c)
	// At least one of these is required, and a flag left out keeps the stored
	// value; an explicitly empty one is rejected rather than read as "left out".
	rejectEmptyFlag(c, "regions")
	rejectEmptyFlag(c, "feature-sets")
	return c
}

func init() {
	updateAWSProviderCmd.AddCommand(updateAWSConnectionCmd)
	updateAWSProviderCmd.AddCommand(updateAWSMonitoringConfigCmd)
	// Every path through this command needs one of the kebab-case flags (--role-arn is required),
	// which are new names in 0.48.0 and not yet proven stable (experimental):
	// at a stable floor they would be hidden, leaving this command with no usable
	// invocation. Marking the command says that plainly instead of hiding the
	// flags and then failing on a "required" flag help no longer lists.
}

// Declared stable: the invocation and output contract of this command is
// additive-only. Stable is never implied -- see AGENTS.md "Stability Tiers".
func init() {
}
