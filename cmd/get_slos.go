package cmd

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/dynatrace-oss/dtctl/pkg/output"
	"github.com/dynatrace-oss/dtctl/pkg/prompt"
	"github.com/dynatrace-oss/dtctl/pkg/resources/slo"
	"github.com/dynatrace-oss/dtctl/pkg/safety"
	"github.com/dynatrace-oss/dtctl/pkg/stability"
)

// getSLOsCmd retrieves SLOs
var getSLOsCmd = newGetSLOsCmd()

func newGetSLOsCmd() *cobra.Command {
	c := &cobra.Command{
		Use:     "slos [id]",
		Aliases: []string{"slo"},
		Short:   "Get service-level objectives",
		Long: `Get service-level objectives.

Examples:
  # List all SLOs
  dtctl get slos

  # Get a specific SLO
  dtctl get slo <slo-id>

  # Filter SLOs by name
  dtctl get slos --filter "name~'production'"

  # Output as JSON
  dtctl get slos -o json
`,
		RunE: func(cmd *cobra.Command, args []string) error {
			filter, _ := cmd.Flags().GetString("filter")

			_, c, printer, err := setup(cmdContext(cmd))
			if err != nil {
				return err
			}

			handler := slo.NewHandler(c)

			// Get specific SLO if ID provided
			if len(args) > 0 {
				s, err := handler.Get(args[0])
				if err != nil {
					return err
				}
				return printer.Print(s)
			}

			// List all SLOs
			list, err := handler.List(filter, getChunkSize(cmdContext(cmd)))
			if err != nil {
				return err
			}

			// A definition is not a status; point at exec slo.
			if ap := enrichAgent(printer, "get", "slo"); ap != nil && len(list.SLOs) > 0 {
				ap.SetSuggestions([]string{sloEvaluateAdvice})
			}
			return printer.PrintList(list.SLOs)
		},
	}
	c.Flags().String("filter", "", "Filter SLOs (e.g., \"name~'production'\")")
	stability.MarkStable(c)
	return c
}

// sloEvaluateAdvice says how to get an SLO's status from its definition.
const sloEvaluateAdvice = "these are SLO definitions (targets), not their status — evaluate each with 'dtctl exec slo <id>' for its current value, status and error budget"

// getSLOTemplatesCmd retrieves SLO templates
var getSLOTemplatesCmd = newGetSLOTemplatesCmd()

func newGetSLOTemplatesCmd() *cobra.Command {
	c := &cobra.Command{
		Use:     "slo-templates [id]",
		Aliases: []string{"slo-template"},
		Short:   "Get SLO objective templates",
		Long: `Get SLO objective templates.

Examples:
  # List all SLO templates
  dtctl get slo-templates

  # Get a specific template
  dtctl get slo-template <template-id>

  # Filter templates
  dtctl get slo-templates --filter "builtIn==true"

  # Output as JSON
  dtctl get slo-templates -o json
`,
		RunE: func(cmd *cobra.Command, args []string) error {
			filter, _ := cmd.Flags().GetString("filter")

			_, c, printer, err := setup(cmdContext(cmd))
			if err != nil {
				return err
			}

			handler := slo.NewHandler(c)

			// Get specific template if ID provided
			if len(args) > 0 {
				t, err := handler.GetTemplate(args[0])
				if err != nil {
					return err
				}
				return printer.Print(t)
			}

			// List all templates
			list, err := handler.ListTemplates(filter)
			if err != nil {
				return err
			}

			return printer.PrintList(list.Items)
		},
	}
	c.Flags().String("filter", "", "Filter templates (e.g., \"builtIn==true\")")
	stability.MarkStable(c)
	return c
}

// deleteSLOCmd deletes an SLO
var deleteSLOCmd = newDeleteSLOCmd()

func newDeleteSLOCmd() *cobra.Command {
	var forceDelete bool
	c := &cobra.Command{
		Use:   "slo <slo-id>",
		Short: "Delete a service-level objective",
		Long: `Delete a service-level objective by ID.

Examples:
  # Delete an SLO
  dtctl delete slo <slo-id>

  # Delete without confirmation
  dtctl delete slo <slo-id> -y
`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			sloID := args[0]

			_, c, err := setupWithSafety(cmdContext(cmd), safety.OperationDelete)
			if err != nil {
				return err
			}

			handler := slo.NewHandler(c)

			// Get current version for optimistic locking
			s, err := handler.Get(sloID)
			if err != nil {
				return err
			}

			if dryRun(cmdContext(cmd)) {
				return deleteDryRun(cmd, "SLO", s.Name, sloID)
			}

			// Confirm deletion unless --force or --plain
			if !forceDelete && !plainMode(cmdContext(cmd)) {
				if !prompt.ConfirmDeletionWith(currentStdin(cmdContext(cmd)), currentStdout(cmdContext(cmd)), "SLO", s.Name, sloID) {
					fmt.Fprintln(currentStdout(cmdContext(cmd)), "Deletion cancelled")
					return nil
				}
			}

			if err := handler.Delete(sloID, s.Version); err != nil {
				return err
			}

			output.FprintSuccess(currentStderr(cmdContext(cmd)), "SLO %q deleted", s.Name)
			return nil
		},
	}
	c.Flags().BoolVarP(&forceDelete, "yes", "y", false, "Skip confirmation prompt")
	stability.MarkStable(c)
	return c
}

func init() {
	// SLO flags
	// Delete confirmation flags
}

// Declared stable: the invocation and output contract of these commands is
// additive-only. Stable is never implied -- see AGENTS.md "Stability Tiers".
func init() {
}
