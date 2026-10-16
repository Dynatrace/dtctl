package cmd

import (
	"github.com/spf13/cobra"

	"github.com/dynatrace-oss/dtctl/pkg/stability"
)

var createAWSProviderCmd = newCreateAWSProviderCmd()

func newCreateAWSProviderCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "aws",
		Short: "Create AWS resources",
		RunE:  requireSubcommand,
	}
	stability.MarkStable(c)
	return c
}

var createGCPProviderCmd = newCreateGCPProviderCmd()

func newCreateGCPProviderCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "gcp",
		Short: "Create GCP resources (Preview)",
		RunE:  requireSubcommand,
	}
	stability.MarkStable(c)
	attachPreviewNotice(c, "GCP")
	return c
}

func init() {
	createCmd.AddCommand(createAWSProviderCmd)
	createCmd.AddCommand(createGCPProviderCmd)
}

// Declared stable: the invocation and output contract of these commands is
// additive-only. Stable is never implied -- see AGENTS.md "Stability Tiers".
