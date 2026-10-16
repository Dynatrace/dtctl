package cmd

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/dynatrace-oss/dtctl/pkg/resources/extension"
	"github.com/dynatrace-oss/dtctl/pkg/stability"
)

var downloadCmd = newDownloadCmd()

func newDownloadCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "download",
		Short: "Download raw resource artifacts",
		Long: `Download raw resource artifacts such as extension zip packages.

This command writes binary data directly to stdout. Redirect output to a file.`,
		Example: `  # Download an extension package
  dtctl download extension com.dynatrace.extension.postgres --version 2.9.3 > postgres.zip`,
		RunE: requireSubcommand,
	}
	stability.MarkStable(c)
	return c
}

var downloadExtensionCmd = newDownloadExtensionCmd()

func newDownloadExtensionCmd() *cobra.Command {
	c := &cobra.Command{
		Use:     "extension <extension-name>",
		Aliases: []string{"ext"},
		Short:   "Download an extension zip package",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			extensionName := args[0]
			versionFlag, _ := cmd.Flags().GetString("version")

			if outputFormat(cmdContext(cmd)) != "table" {
				return fmt.Errorf("download extension does not support -o output formatting")
			}
			if getAgentMode(cmdContext(cmd)) {
				return fmt.Errorf("download extension is incompatible with agent mode (-A): raw binary cannot be wrapped in a JSON envelope")
			}

			_, c, err := setupClient(cmdContext(cmd))
			if err != nil {
				return err
			}

			handler := extension.NewHandler(c)
			data, err := handler.Download(extensionName, versionFlag)
			if err != nil {
				return err
			}

			_, err = currentStdout(cmdContext(cmd)).Write(data)
			return err
		},
	}
	c.Flags().String("version", "", "Extension version to download")
	stability.MarkStable(c)
	markFlagRequiredNonEmpty(c, "version")
	return c
}

func init() {
	rootCmd.AddCommand(downloadCmd)
	downloadCmd.AddCommand(downloadExtensionCmd)
}

// Declared stable: the invocation and output contract of these commands is
// additive-only. Stable is never implied -- see AGENTS.md "Stability Tiers".
func init() {
}
