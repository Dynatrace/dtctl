package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"runtime"
	"strings"

	"github.com/spf13/cobra"

	"github.com/dynatrace-oss/dtctl/pkg/output"
	"github.com/dynatrace-oss/dtctl/pkg/resources/awsconnection"
	"github.com/dynatrace-oss/dtctl/pkg/resources/awsmonitoringconfig"
	"github.com/dynatrace-oss/dtctl/pkg/safety"
	"github.com/dynatrace-oss/dtctl/pkg/stability"
)

var createAWSConnectionCmd = newCreateAWSConnectionCmd()

func newCreateAWSConnectionCmd() *cobra.Command {
	var createAWSConnectionName string
	var createAWSConnectionRoleArn string
	c := &cobra.Command{
		Use:     "connection",
		Aliases: []string{"connections"},
		Short:   "Create AWS connection from flags",
		Long: `Create an AWS connection (role-based authentication) for the Dynatrace AWS data
acquisition extension. The role ARN can be omitted at creation time and patched
later (after the IAM role is created with the trust policy that uses the new
connection's objectId as sts:ExternalId).

Examples:
  dtctl create aws connection --name "my-aws"
  dtctl create aws connection --name "my-aws" --roleArn arn:aws:iam::123456789012:role/DynatraceMonitoringRole`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if createAWSConnectionRoleArn != "" {
				if err := awsconnection.ValidateRoleArn(createAWSConnectionRoleArn); err != nil {
					return err
				}
			}

			_, c, err := setupWithSafety(cmdContext(cmd), safety.OperationCreate)
			if err != nil {
				return err
			}

			handler := awsconnection.NewHandler(c)

			value := awsconnection.Value{
				Name: createAWSConnectionName,
				Type: awsconnection.TypeRoleBased,
				AwsRoleBasedAuthentication: &awsconnection.AwsRoleBasedAuthenticationConfig{
					RoleArn:   createAWSConnectionRoleArn,
					Consumers: []string{awsconnection.DefaultConsumer},
				},
			}

			if dryRun(cmdContext(cmd)) {
				return newDryRunReport(cmd).
					Linef("Dry run: would create AWS connection").
					Field("Name", "%s", createAWSConnectionName).
					Field("Role ARN", "%s", createAWSConnectionRoleArn).
					Print()
			}

			created, err := handler.Create(awsconnection.AWSConnectionCreate{Value: value})
			if err != nil {
				return err
			}

			output.FprintSuccess(currentStderr(cmdContext(cmd)), "AWS connection created: %s", created.ObjectID)
			printAWSConnectionInstructions(cmdContext(cmd), c.BaseURL(), created.ObjectID, createAWSConnectionName)
			return nil
		},
	}
	c.Flags().StringVar(&createAWSConnectionName, "name", "", "AWS connection name (required)")
	c.Flags().StringVar(&createAWSConnectionRoleArn, "roleArn", "", "AWS IAM role ARN (optional; can be patched later)")
	stability.MarkFlag(c, "roleArn", stability.Experimental, pre10Since)
	stability.MarkStable(c)
	markFlagRequiredNonEmpty(c, "name")
	return c
}

var createAWSMonitoringConfigCmd = newCreateAWSMonitoringConfigCmd()

func newCreateAWSMonitoringConfigCmd() *cobra.Command {
	var createAWSMonitoringConfigCredentials string
	var createAWSMonitoringConfigFeatureSets string
	var createAWSMonitoringConfigName string
	var createAWSMonitoringConfigRegions string
	var createAWSMonitoringConfigCentral bool
	c := &cobra.Command{
		Use:     "monitoring",
		Aliases: []string{"monitoring-config"},
		Short:   "Create AWS monitoring config from flags",
		Long: `Create an AWS monitoring configuration in disabled state.

Use 'dtctl enable aws monitoring' to enable it once the underlying IAM role
is in place. The --regions flag is required (comma-separated AWS regions).

Examples:
  dtctl create aws monitoring --name "my-aws" --credentials "my-aws" --regions us-east-1,eu-central-1
  dtctl create aws monitoring --name "my-aws" --credentials "my-aws" --regions us-east-1 --featureSets EC2_essential,RDS_essential`,
		RunE: func(cmd *cobra.Command, args []string) error {

			_, c, err := setupWithSafety(cmdContext(cmd), safety.OperationCreate)
			if err != nil {
				return err
			}

			connectionHandler := awsconnection.NewHandler(c)
			monitoringHandler := awsmonitoringconfig.NewHandler(c)

			credential, err := awsmonitoringconfig.ResolveCredential(createAWSMonitoringConfigCredentials, connectionHandler)
			if err != nil {
				return err
			}
			credential.Enabled = false // created in disabled state

			regions, err := awsmonitoringconfig.ParseRequiredRegions(createAWSMonitoringConfigRegions)
			if err != nil {
				return err
			}

			featureSets, err := awsmonitoringconfig.ParseOrDefaultFeatureSets(createAWSMonitoringConfigFeatureSets, monitoringHandler)
			if err != nil {
				return err
			}

			version, err := monitoringHandler.GetLatestVersion()
			if err != nil {
				return fmt.Errorf("failed to determine extension version: %w", err)
			}

			payload := buildAWSMonitoringConfig(
				createAWSMonitoringConfigName, version, credential, regions, featureSets,
				centralEnrichmentIntent(cmd, createAWSMonitoringConfigCentral))

			body, err := json.Marshal(payload)
			if err != nil {
				return fmt.Errorf("failed to prepare request payload: %w", err)
			}

			if dryRun(cmdContext(cmd)) {
				return newDryRunReport(cmd).
					Linef("Dry run: would create AWS monitoring config (disabled)").
					Field("Name", "%s", createAWSMonitoringConfigName).
					Field("Version", "%s", version).
					Field("Regions", "%s", strings.Join(regions, ",")).
					Field("Feature sets", "%d", len(featureSets)).
					Print()
			}

			created, err := monitoringHandler.Create(body)
			if err != nil {
				return err
			}

			output.FprintSuccess(currentStderr(cmdContext(cmd)), "AWS monitoring config created (disabled): %s", created.ObjectID)
			output.FprintInfo(currentStderr(cmdContext(cmd)), "Run 'dtctl enable aws monitoring --name %q' to enable it", createAWSMonitoringConfigName)
			return nil
		},
	}
	c.Flags().StringVar(&createAWSMonitoringConfigName, "name", "", "Monitoring config name/description (required)")
	c.Flags().StringVar(&createAWSMonitoringConfigCredentials, "credentials", "", "AWS connection name or ID (required)")
	c.Flags().StringVar(&createAWSMonitoringConfigRegions, "regions", "", "Comma-separated AWS regions (required, first is the deployment region)")
	c.Flags().StringVar(&createAWSMonitoringConfigFeatureSets, "featureSets", "", "Comma-separated feature sets (default: all *_essential)")
	stability.MarkFlag(c, "featureSets", stability.Experimental, pre10Since)
	addCentralEnrichmentFlag(c, &createAWSMonitoringConfigCentral)
	stability.MarkStable(c)
	markFlagRequiredNonEmpty(c, "name")
	markFlagRequiredNonEmpty(c, "credentials")
	return c
}

func buildAWSMonitoringConfig(name, version string, credential awsmonitoringconfig.Credential,
	regions, featureSets []string, central *bool) awsmonitoringconfig.AWSMonitoringConfig {
	return awsmonitoringconfig.AWSMonitoringConfig{
		Scope: awsmonitoringconfig.DefaultScope,
		Value: awsmonitoringconfig.Value{
			Enabled:           false,
			Description:       name,
			Version:           version,
			ActivationContext: awsmonitoringconfig.DefaultActivationContext,
			FeatureSets:       featureSets,
			Aws: awsmonitoringconfig.AWSConfig{
				UseIngestEnrichmentConfig: central,
				DeploymentRegion:          regions[0],
				Credentials:               []awsmonitoringconfig.Credential{credential},
				RegionFiltering:           regions,
				TagFiltering:              []awsmonitoringconfig.TagFilter{},
				TagEnrichment:             []string{},
				Namespaces:                []awsmonitoringconfig.CustomNamespace{},
				ConfigurationMode:         "QUICK_START",
				DeploymentMode:            "AUTOMATED",
				DeploymentScope:           "SINGLE_ACCOUNT",
				SmartscapeConfiguration:   awsmonitoringconfig.FlagConfig{Enabled: true},
				MetricsConfiguration:      awsmonitoringconfig.RegionalFlagConfig{Enabled: true, Regions: regions},
			},
		},
	}
}

// printAWSConnectionInstructions prints a copy-paste 'aws cloudformation deploy'
// one-liner that creates the Dynatrace monitoring IAM role with the least-
// privilege managed policies maintained by Dynatrace. The upstream template
// itself handles trust policy (Principal + sts:ExternalId via parameters) so
// dtctl does not need to generate trust-policy.json.
func printAWSConnectionInstructions(ctx context.Context, tenantURL, objectID, connectionName string) {
	const templateURL = "https://dynatrace-data-acquisition.s3.amazonaws.com/aws/deployment/cfn/latest/da-aws-nested-monitoring-role.yaml"
	stackName := "dynatrace-monitoring-" + sanitizeRoleName(connectionName)

	fmt.Fprintln(currentStdout(ctx))
	fmt.Fprintln(currentStdout(ctx), "Create the Dynatrace monitoring IAM role with the least-privilege policy")
	fmt.Fprintln(currentStdout(ctx), "maintained by Dynatrace. Run in AWS CloudShell (aws CLI + curl pre-installed):")
	fmt.Fprintln(currentStdout(ctx))
	if runtime.GOOS == "windows" {
		fmt.Fprintf(currentStdout(ctx), "   $STACK = \"%s\"\n", stackName)
		fmt.Fprintf(currentStdout(ctx), "   curl.exe -fsSLo da-role.yaml %s\n", templateURL)
		fmt.Fprintf(currentStdout(ctx), "   aws cloudformation deploy `\n     --stack-name $STACK `\n     --template-file da-role.yaml `\n     --parameter-overrides pDynatraceUrl=%s pRoleExternalId=%s `\n     --capabilities CAPABILITY_NAMED_IAM\n",
			tenantURL, objectID)
		fmt.Fprintln(currentStdout(ctx), "   $ROLE_ARN = aws cloudformation describe-stacks --stack-name $STACK `")
		fmt.Fprintln(currentStdout(ctx), "     --query \"Stacks[0].Outputs[?OutputKey=='DynatraceMonitoringRoleArn'].OutputValue\" --output text")
		fmt.Fprintf(currentStdout(ctx), "   dtctl update aws connection --name %q --roleArn $ROLE_ARN\n", connectionName)
	} else {
		fmt.Fprintf(currentStdout(ctx), "   STACK=%q\n", stackName)
		fmt.Fprintf(currentStdout(ctx), "   curl -fsSLo da-role.yaml %s\n", templateURL)
		fmt.Fprintf(currentStdout(ctx), "   aws cloudformation deploy \\\n     --stack-name \"$STACK\" \\\n     --template-file da-role.yaml \\\n     --parameter-overrides pDynatraceUrl=%s pRoleExternalId=%s \\\n     --capabilities CAPABILITY_NAMED_IAM\n",
			tenantURL, objectID)
		fmt.Fprintln(currentStdout(ctx), "   ROLE_ARN=$(aws cloudformation describe-stacks --stack-name \"$STACK\" \\")
		fmt.Fprintln(currentStdout(ctx), "     --query \"Stacks[0].Outputs[?OutputKey=='DynatraceMonitoringRoleArn'].OutputValue\" --output text)")
		fmt.Fprintf(currentStdout(ctx), "   dtctl update aws connection --name %q --roleArn \"$ROLE_ARN\"\n", connectionName)
	}
	fmt.Fprintln(currentStdout(ctx))
	fmt.Fprintln(currentStdout(ctx), "The template is Dynatrace's source of truth for the required AWS read/describe")
	fmt.Fprintln(currentStdout(ctx), "actions; refresh later by re-running the same command (CloudFormation does an")
	fmt.Fprintln(currentStdout(ctx), "in-place update after curl re-downloads the latest template).")
}

// sanitizeRoleName produces a string usable inside an IAM role name.
func sanitizeRoleName(s string) string {
	out := strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z':
			return r
		case r >= 'A' && r <= 'Z':
			return r
		case r >= '0' && r <= '9':
			return r
		case r == '-' || r == '_':
			return r
		default:
			return '-'
		}
	}, s)
	if out == "" {
		out = "connection"
	}
	return out
}

func init() {
	createAWSProviderCmd.AddCommand(createAWSConnectionCmd)
	createAWSProviderCmd.AddCommand(createAWSMonitoringConfigCmd)
	// Renamed to kebab-case in 1.0 (contrib breaking-changes/cloud-flags-kebab-case.md);
	// the spelling aliases are removed outright.
	// Renamed to kebab-case in 1.0 (contrib breaking-changes/cloud-flags-kebab-case.md);
	// the spelling aliases are removed outright.
}

// Declared stable: the invocation and output contract of these commands is
// additive-only. Stable is never implied -- see AGENTS.md "Stability Tiers".
func init() {
}
