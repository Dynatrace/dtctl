package cmd

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/dynatrace-oss/dtctl/pkg/output"
	"github.com/dynatrace-oss/dtctl/pkg/prompt"
	"github.com/dynatrace-oss/dtctl/pkg/resources/edgeconnect"
	"github.com/dynatrace-oss/dtctl/pkg/safety"
	"github.com/dynatrace-oss/dtctl/pkg/stability"
)

// getEdgeConnectsCmd retrieves EdgeConnect configurations
var getEdgeConnectsCmd = newGetEdgeConnectsCmd()

func newGetEdgeConnectsCmd() *cobra.Command {
	c := &cobra.Command{
		Use:     "edgeconnects [id]",
		Aliases: []string{"edgeconnect", "ec"},
		Short:   "Get EdgeConnect configurations",
		Long: `Get EdgeConnect configurations.

Examples:
  # List all EdgeConnects
  dtctl get edgeconnects

  # Get a specific EdgeConnect
  dtctl get edgeconnect <id>

  # Output as JSON
  dtctl get edgeconnects -o json
`,
		RunE: func(cmd *cobra.Command, args []string) error {
			_, c, printer, err := setup(cmdContext(cmd))
			if err != nil {
				return err
			}

			handler := edgeconnect.NewHandler(c)

			// Get specific EdgeConnect if ID provided
			if len(args) > 0 {
				ec, err := handler.Get(args[0])
				if err != nil {
					return err
				}
				return printer.Print(ec)
			}

			// List all EdgeConnects
			list, err := handler.List()
			if err != nil {
				return err
			}

			return printer.PrintList(list.EdgeConnects)
		},
	}
	stability.MarkStable(c)
	return c
}

// deleteEdgeConnectCmd deletes an EdgeConnect
var deleteEdgeConnectCmd = newDeleteEdgeConnectCmd()

func newDeleteEdgeConnectCmd() *cobra.Command {
	var forceDelete bool
	c := &cobra.Command{
		Use:     "edgeconnect <id>",
		Aliases: []string{"ec"},
		Short:   "Delete an EdgeConnect configuration",
		Long: `Delete an EdgeConnect configuration by ID.

Examples:
  # Delete an EdgeConnect
  dtctl delete edgeconnect <id>

  # Delete without confirmation
  dtctl delete edgeconnect <id> -y
`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ecID := args[0]

			_, c, err := setupWithSafety(cmdContext(cmd), safety.OperationDelete)
			if err != nil {
				return err
			}

			handler := edgeconnect.NewHandler(c)

			// Get EdgeConnect for confirmation
			ec, err := handler.Get(ecID)
			if err != nil {
				return err
			}

			if dryRun(cmdContext(cmd)) {
				return deleteDryRun(cmd, "EdgeConnect", ec.Name, ecID)
			}

			// Confirm deletion unless --force or --plain
			if !forceDelete && !plainMode(cmdContext(cmd)) {
				if !prompt.ConfirmDeletionWith(currentStdin(cmdContext(cmd)), currentStdout(cmdContext(cmd)), "EdgeConnect", ec.Name, ecID) {
					fmt.Fprintln(currentStdout(cmdContext(cmd)), "Deletion cancelled")
					return nil
				}
			}

			if err := handler.Delete(ecID); err != nil {
				return err
			}

			output.FprintSuccess(currentStderr(cmdContext(cmd)), "EdgeConnect %q deleted", ec.Name)
			return nil
		},
	}
	c.Flags().BoolVarP(&forceDelete, "yes", "y", false, "Skip confirmation prompt")
	stability.MarkStable(c)
	return c
}

func init() {
	// Delete confirmation flags
}

// Declared stable: the invocation and output contract of these commands is
// additive-only. Stable is never implied -- see AGENTS.md "Stability Tiers".
func init() {
}
