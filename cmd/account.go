package cmd

import (
	"github.com/spf13/cobra"
)

// accountDevelopmentFeature is the opt-in key for the still-unfinished platform
// "account" command surface (account login/status/token). Enable it with
// `dtctl config set development.account on` or DTCTL_DEVELOPMENT=account.
const accountDevelopmentFeature = "account"

var accountCmd = newAccountCmd()

func newAccountCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "account",
		Short: "Platform account administration",
		Long:  "Commands for managing Dynatrace platform account resources.",
		RunE:  requireSubcommand,
	}
	return c
}

func init() {
	// Account administration is unfinished, so it is a development-tier
	// feature: declared here (so a block message can name it) but attached to
	// the tree only when the opt-in is on. Without it, `dtctl account` is an
	// unknown command and the surface is absent from help, completion and the
	// `dtctl commands` catalog. Promote it to stable — dropping this call and
	// using rootCmd.AddCommand — when the account surface is finished.
	addDevelopmentCommand(rootCmd, accountCmd, accountDevelopmentFeature)
}
