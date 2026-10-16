package cmd

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/dynatrace-oss/dtctl/pkg/stability"
	"github.com/dynatrace-oss/dtctl/pkg/version"
)

// versionCmd represents the version command
var versionCmd = newVersionCmd()

func newVersionCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "version",
		Short: "Print version information",
		Long:  `Print the version, commit, and build date of dtctl.`,
		Run: func(cmd *cobra.Command, args []string) {
			fmt.Fprintf(currentStdout(cmdContext(cmd)), "dtctl version %s\n", version.Version)
			fmt.Fprintf(currentStdout(cmdContext(cmd)), "commit: %s\n", version.Commit)
			fmt.Fprintf(currentStdout(cmdContext(cmd)), "built: %s\n", version.Date)
		},
	}
	stability.MarkStable(c)
	return c
}

func init() {
	rootCmd.AddCommand(versionCmd)
}

// Declared stable: the invocation and output contract of this command is
// additive-only. Stable is never implied -- see AGENTS.md "Stability Tiers".
func init() {
}
