package cmd

import (
	"github.com/spf13/cobra"

	"github.com/dynatrace-oss/dtctl/pkg/stability"
)

var translateCmd = newTranslateCmd()

func newTranslateCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "translate",
		Short: "Translate expressions between formats",
		Long: `Translate expressions and configurations between formats.

Available subcommands:
  lql-to-dql         Translate an LQL matcher expression into a DQL matcher expression
  classic-pipelines  Translate a Classic pipeline into an OpenPipeline configuration pipeline`,
	}
	stability.MarkStable(c)
	return c
}

func init() {
	rootCmd.AddCommand(translateCmd)
	translateCmd.AddCommand(translateLqlToDqlCmd)
	translateCmd.AddCommand(translateClassicPipelinesCmd)
}

// Declared stable: the invocation and output contract of this command is
// additive-only. Stable is never implied -- see AGENTS.md "Stability Tiers".
func init() {
}
