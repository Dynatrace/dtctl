package cmd

import (
	"github.com/spf13/cobra"

	"github.com/dynatrace-oss/dtctl/pkg/resources/analyzer"
	"github.com/dynatrace-oss/dtctl/pkg/resources/copilot"
	"github.com/dynatrace-oss/dtctl/pkg/stability"
)

// getAnalyzersCmd retrieves Davis analyzers
var getAnalyzersCmd = newGetAnalyzersCmd()

func newGetAnalyzersCmd() *cobra.Command {
	c := &cobra.Command{
		Use:     "analyzers [name]",
		Aliases: []string{"analyzer", "az"},
		Short:   "Get Davis AI analyzers",
		Long: `Get available Davis AI analyzers.

Examples:
  # List all analyzers
  dtctl get analyzers

  # Get a specific analyzer definition
  dtctl get analyzer dt.statistics.GenericForecastAnalyzer

  # Filter analyzers
  dtctl get analyzers --filter "name contains 'forecast'"

  # Output as JSON
  dtctl get analyzers -o json
`,
		RunE: func(cmd *cobra.Command, args []string) error {
			_, c, printer, err := setup(cmdContext(cmd))
			if err != nil {
				return err
			}

			handler := analyzer.NewHandler(c)

			// Get specific analyzer if name provided
			if len(args) > 0 {
				az, err := handler.Get(args[0])
				if err != nil {
					return err
				}
				return printer.Print(az)
			}

			// List all analyzers
			filter, _ := cmd.Flags().GetString("filter")
			list, err := handler.List(filter)
			if err != nil {
				return err
			}

			return printer.PrintList(list.Analyzers)
		},
	}
	c.Flags().String("filter", "", "Filter analyzers (e.g., \"name contains 'forecast'\")")
	stability.MarkStable(c)
	return c
}

// getCopilotSkillsCmd retrieves Davis CoPilot skills
var getCopilotSkillsCmd = newGetCopilotSkillsCmd()

func newGetCopilotSkillsCmd() *cobra.Command {
	c := &cobra.Command{
		Use:     "copilot-skills",
		Aliases: []string{"copilot-skill"},
		Short:   "Get Davis CoPilot skills",
		Long: `Get available Davis CoPilot skills.

Examples:
  # List all CoPilot skills
  dtctl get copilot-skills

  # Output as JSON
  dtctl get copilot-skills -o json
`,
		RunE: func(cmd *cobra.Command, args []string) error {
			_, c, printer, err := setup(cmdContext(cmd))
			if err != nil {
				return err
			}

			handler := copilot.NewHandler(c)

			list, err := handler.ListSkills()
			if err != nil {
				return err
			}

			return printer.PrintList(list.Skills)
		},
	}
	stability.MarkStable(c)
	return c
}

func init() {
	// Analyzer flags
}

// Declared stable: the invocation and output contract of these commands is
// additive-only. Stable is never implied -- see AGENTS.md "Stability Tiers".
func init() {
}
