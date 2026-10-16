package cmd

import (
	"github.com/spf13/cobra"

	"github.com/dynatrace-oss/dtctl/pkg/stability"
)

var updateAWSProviderCmd = newUpdateAWSProviderCmd()

func newUpdateAWSProviderCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "aws",
		Short: "Update AWS resources",
		RunE:  requireSubcommand,
	}
	stability.MarkStable(c)
	return c
}

var updateGCPProviderCmd = newUpdateGCPProviderCmd()

func newUpdateGCPProviderCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "gcp",
		Short: "Update GCP resources (Preview)",
		RunE:  requireSubcommand,
	}
	stability.MarkStable(c)
	attachPreviewNotice(c, "GCP")
	return c
}

func init() {
	updateCmd.AddCommand(updateAWSProviderCmd)
	updateCmd.AddCommand(updateGCPProviderCmd)
}

// Declared stable: the invocation and output contract of these commands is
// additive-only. Stable is never implied -- see AGENTS.md "Stability Tiers".
