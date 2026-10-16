package cmd

import (
	"github.com/spf13/cobra"

	"github.com/dynatrace-oss/dtctl/pkg/output"
	"github.com/dynatrace-oss/dtctl/pkg/resources/serviceuser"
	"github.com/dynatrace-oss/dtctl/pkg/safety"
)

var (
	setupServiceUserAccount           = SetupAccount
	setupServiceUserAccountWithSafety = SetupAccountWithSafety
)

var accountListServiceUserCmd = &cobra.Command{
	Use:     "service-user",
	Aliases: []string{"service-users"},
	Short:   "List service users",
	Args:    cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		accountClient, accountUUID, err := setupServiceUserAccount()
		if err != nil {
			return err
		}

		handler := serviceuser.NewHandler(accountClient, accountUUID)
		users, err := handler.List()
		if err != nil {
			return err
		}
		printer := newPrinterCtx(cmdContext(cmd))
		enrichAgent(printer, "list", "service-user")
		return printer.PrintList(users)
	},
}

var accountCreateServiceUserCmd = &cobra.Command{
	Use:     "service-user",
	Aliases: []string{"service-users"},
	Short:   "Create a service user",
	Args:    cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		name, _ := cmd.Flags().GetString("name")
		description, _ := cmd.Flags().GetString("description")

		req := serviceuser.ServiceUserCreate{Name: name, Description: description}

		if dryRun(cmdContext(cmd)) {
			if agentMode(cmdContext(cmd)) {
				printer := newPrinterCtx(cmdContext(cmd))
				enrichAgent(printer, "create", "service-user")
				return printer.Print(map[string]interface{}{
					"dryRun":      true,
					"name":        req.Name,
					"description": req.Description,
				})
			}
			output.FprintInfo(currentStderr(cmdContext(cmd)), "Dry run: would create service user")
			output.FprintInfo(currentStderr(cmdContext(cmd)), "Name: %s", req.Name)
			if req.Description != "" {
				output.FprintInfo(currentStderr(cmdContext(cmd)), "Description: %s", req.Description)
			}
			return nil
		}

		accountClient, accountUUID, err := setupServiceUserAccountWithSafety(safety.OperationCreate)
		if err != nil {
			return err
		}
		handler := serviceuser.NewHandler(accountClient, accountUUID)
		result, err := handler.Create(req)
		if err != nil {
			return err
		}

		output.FprintSuccess(currentStderr(cmdContext(cmd)), "Service user %q created", result.Name)
		printer := newPrinterCtx(cmdContext(cmd))
		enrichAgent(printer, "create", "service-user")
		return printer.Print(result)
	},
}

var accountDeleteServiceUserCmd = &cobra.Command{
	Use:     "service-user <userUuid>",
	Aliases: []string{"service-users"},
	Short:   "Delete a service user",
	Args:    cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		userUUID := args[0]
		if dryRun(cmdContext(cmd)) {
			if agentMode(cmdContext(cmd)) {
				printer := newPrinterCtx(cmdContext(cmd))
				enrichAgent(printer, "delete", "service-user")
				return printer.Print(map[string]interface{}{
					"dryRun": true,
					"uid":    userUUID,
				})
			}
			output.FprintInfo(currentStderr(cmdContext(cmd)), "Dry run: would delete service user %q", userUUID)
			return nil
		}

		accountClient, accountUUID, err := setupServiceUserAccountWithSafety(safety.OperationDelete)
		if err != nil {
			return err
		}
		handler := serviceuser.NewHandler(accountClient, accountUUID)
		if err := handler.Delete(userUUID); err != nil {
			return err
		}

		if agentMode(cmdContext(cmd)) {
			printer := newPrinterCtx(cmdContext(cmd))
			enrichAgent(printer, "delete", "service-user")
			return printer.Print(map[string]interface{}{
				"deleted": true,
				"uid":     userUUID,
			})
		}
		output.FprintSuccess(currentStderr(cmdContext(cmd)), "Service user %q deleted", userUUID)
		return nil
	},
}

func init() {
	accountListCmd.AddCommand(accountListServiceUserCmd)
	accountCreateCmd.AddCommand(accountCreateServiceUserCmd)
	accountDeleteCmd.AddCommand(accountDeleteServiceUserCmd)

	accountCreateServiceUserCmd.Flags().String("name", "", "service-user name (required)")
	accountCreateServiceUserCmd.Flags().String("description", "", "service-user description")
	markFlagRequiredNonEmpty(accountCreateServiceUserCmd, "name")
}
