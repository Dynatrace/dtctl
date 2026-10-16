package cmd

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/dynatrace-oss/dtctl/pkg/output"
	"github.com/dynatrace-oss/dtctl/pkg/resources/awsconnection"
	"github.com/dynatrace-oss/dtctl/pkg/resources/awsmonitoringconfig"
	"github.com/dynatrace-oss/dtctl/pkg/safety"
	"github.com/dynatrace-oss/dtctl/pkg/stability"
)

var deleteAWSConnectionCmd = newDeleteAWSConnectionCmd()

func newDeleteAWSConnectionCmd() *cobra.Command {
	c := &cobra.Command{
		Use:     "connection [ID|NAME]",
		Short:   "Delete an AWS connection",
		Aliases: []string{"connections"},
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			identifier := args[0]

			_, c, err := setupWithSafety(cmdContext(cmd), safety.OperationDelete)
			if err != nil {
				return err
			}

			handler := awsconnection.NewHandler(c)

			objectID := identifier
			item, err := handler.FindByName(identifier)
			if err == nil {
				objectID = item.ObjectID
				output.FprintInfo(currentStderr(cmdContext(cmd)), "Resolved name %q to ID %s", identifier, objectID)
			}

			if dryRun(cmdContext(cmd)) {
				return deleteDryRun(cmd, "AWS connection", identifier, objectID)
			}

			if err := handler.Delete(objectID); err != nil {
				return fmt.Errorf("failed to delete AWS connection %q: %w", objectID, err)
			}

			output.FprintSuccess(currentStderr(cmdContext(cmd)), "AWS connection %s deleted", objectID)
			return nil
		},
	}
	stability.MarkStable(c)
	return c
}

var deleteAWSMonitoringConfigCmd = newDeleteAWSMonitoringConfigCmd()

func newDeleteAWSMonitoringConfigCmd() *cobra.Command {
	c := &cobra.Command{
		Use:     "monitoring [ID|NAME]",
		Short:   "Delete an AWS monitoring config",
		Aliases: []string{"monitoring-config", "monitoring-configs"},
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			identifier := args[0]

			_, c, err := setupWithSafety(cmdContext(cmd), safety.OperationDelete)
			if err != nil {
				return err
			}

			handler := awsmonitoringconfig.NewHandler(c)

			objectID := identifier
			item, err := handler.FindByName(identifier)
			if err == nil {
				objectID = item.ObjectID
				output.FprintInfo(currentStderr(cmdContext(cmd)), "Resolved name %q to ID %s", identifier, objectID)
			}

			if dryRun(cmdContext(cmd)) {
				return deleteDryRun(cmd, "AWS monitoring config", identifier, objectID)
			}

			if err := handler.Delete(objectID); err != nil {
				return fmt.Errorf("failed to delete AWS monitoring config %q: %w", objectID, err)
			}

			output.FprintSuccess(currentStderr(cmdContext(cmd)), "AWS monitoring config %s deleted", objectID)
			return nil
		},
	}
	stability.MarkStable(c)
	return c
}

func init() {
	deleteAWSProviderCmd.AddCommand(deleteAWSConnectionCmd)
	deleteAWSProviderCmd.AddCommand(deleteAWSMonitoringConfigCmd)
}

// Declared stable: the invocation and output contract of these commands is
// additive-only. Stable is never implied -- see AGENTS.md "Stability Tiers".
func init() {
}
