package cmd

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/dynatrace-oss/dtctl/pkg/resources/document"
	"github.com/dynatrace-oss/dtctl/pkg/resources/resolver"
	"github.com/dynatrace-oss/dtctl/pkg/resources/workflow"
	"github.com/dynatrace-oss/dtctl/pkg/stability"
)

// historyCmd represents the history command
var historyCmd = newHistoryCmd()

func newHistoryCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "history",
		Short: "Show version history of resources",
		Long:  `Show the version history of resources like workflows, notebooks, and dashboards.`,
		RunE:  requireSubcommand,
	}
	stability.MarkStable(c)
	return c
}

// historyWorkflowCmd shows version history for a workflow
var historyWorkflowCmd = newHistoryWorkflowCmd()

func newHistoryWorkflowCmd() *cobra.Command {
	c := &cobra.Command{
		Use:     "workflow <workflow-id-or-name>",
		Aliases: []string{"workflows", "wf"},
		Short:   "Show version history of a workflow",
		Long: `Show the version history of a workflow.

Workflow history is automatically tracked when workflows are modified.

Examples:
  # Show version history by ID
  dtctl history workflow a1b2c3d4-e5f6-7890-abcd-ef1234567890

  # Show version history by name
  dtctl history workflow "My Workflow"

  # Output as JSON
  dtctl history workflow "My Workflow" -o json
`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			identifier := args[0]

			_, c, printer, err := setup(cmdContext(cmd))
			if err != nil {
				return err
			}

			// Resolve name to ID
			res := resolver.NewResolver(c)
			workflowID, err := res.ResolveID(resolver.TypeWorkflow, identifier)
			if err != nil {
				return err
			}

			handler := workflow.NewHandler(c)

			history, err := handler.ListHistory(workflowID)
			if err != nil {
				return err
			}

			if len(history.Results) == 0 {
				fmt.Fprintln(currentStdout(cmdContext(cmd)), "No history found for this workflow")
				return nil
			}

			return printer.PrintList(history.Results)
		},
	}
	stability.MarkStable(c)
	return c
}

// historyDashboardCmd shows version history for a dashboard
var historyDashboardCmd = newHistoryDashboardCmd()

func newHistoryDashboardCmd() *cobra.Command {
	c := &cobra.Command{
		Use:     "dashboard <dashboard-id-or-name>",
		Aliases: []string{"dashboards", "dash", "db"},
		Short:   "Show version history of a dashboard",
		Long: `Show the version history (snapshots) of a dashboard.

Snapshots are not kept automatically: pass --create-snapshot when updating a
document ('dtctl update document', 'dtctl apply', 'dtctl edit') to capture its
content before the update overwrites it. Restoring also snapshots the current
state first (if one doesn't exist), so a restore is normally reversible.

Examples:
  # Show version history by ID
  dtctl history dashboard a1b2c3d4-e5f6-7890-abcd-ef1234567890

  # Show version history by name
  dtctl history dashboard "Production Dashboard"

  # Output as JSON
  dtctl history dashboard "Production Dashboard" -o json
`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			identifier := args[0]

			_, c, printer, err := setup(cmdContext(cmd))
			if err != nil {
				return err
			}

			// Resolve name to ID
			res := resolver.NewResolver(c)
			dashboardID, err := res.ResolveID(resolver.TypeDashboard, identifier)
			if err != nil {
				return err
			}

			handler := document.NewHandler(c)

			snapshots, err := handler.ListSnapshots(dashboardID)
			if err != nil {
				return err
			}

			if len(snapshots.Snapshots) == 0 {
				fmt.Fprintln(currentStdout(cmdContext(cmd)), "No snapshots found for this dashboard")
				if !plainMode(cmdContext(cmd)) {
					fmt.Fprintln(currentStdout(cmdContext(cmd)), "Snapshots are opt-in: pass --create-snapshot when updating to capture the pre-update content")
				}
				return nil
			}

			return printer.PrintList(snapshots.Snapshots)
		},
	}
	stability.MarkStable(c)
	return c
}

// historyNotebookCmd shows version history for a notebook
var historyNotebookCmd = newHistoryNotebookCmd()

func newHistoryNotebookCmd() *cobra.Command {
	c := &cobra.Command{
		Use:     "notebook <notebook-id-or-name>",
		Aliases: []string{"notebooks", "nb"},
		Short:   "Show version history of a notebook",
		Long: `Show the version history (snapshots) of a notebook.

Snapshots are not kept automatically: pass --create-snapshot when updating a
document ('dtctl update document', 'dtctl apply', 'dtctl edit') to capture its
content before the update overwrites it. Restoring also snapshots the current
state first (if one doesn't exist), so a restore is normally reversible.

Examples:
  # Show version history by ID
  dtctl history notebook a1b2c3d4-e5f6-7890-abcd-ef1234567890

  # Show version history by name
  dtctl history notebook "Analysis Notebook"

  # Output as JSON
  dtctl history notebook "Analysis Notebook" -o json
`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			identifier := args[0]

			_, c, printer, err := setup(cmdContext(cmd))
			if err != nil {
				return err
			}

			// Resolve name to ID
			res := resolver.NewResolver(c)
			notebookID, err := res.ResolveID(resolver.TypeNotebook, identifier)
			if err != nil {
				return err
			}

			handler := document.NewHandler(c)

			snapshots, err := handler.ListSnapshots(notebookID)
			if err != nil {
				return err
			}

			if len(snapshots.Snapshots) == 0 {
				fmt.Fprintln(currentStdout(cmdContext(cmd)), "No snapshots found for this notebook")
				if !plainMode(cmdContext(cmd)) {
					fmt.Fprintln(currentStdout(cmdContext(cmd)), "Snapshots are opt-in: pass --create-snapshot when updating to capture the pre-update content")
				}
				return nil
			}

			return printer.PrintList(snapshots.Snapshots)
		},
	}
	stability.MarkStable(c)
	return c
}

// historyDocumentCmd shows version history for a document of any type
var historyDocumentCmd = newHistoryDocumentCmd()

func newHistoryDocumentCmd() *cobra.Command {
	c := &cobra.Command{
		Use:     "document <document-id-or-name>",
		Aliases: []string{"documents", "doc"},
		Short:   "Show version history of a document",
		Long: `Show the version history (snapshots) of a document of any type.

Works for any document type (dashboard, notebook, launchpad, custom app documents, etc.).

Snapshots are not kept automatically: pass --create-snapshot when updating a
document ('dtctl update document', 'dtctl apply', 'dtctl edit') to capture its
content before the update overwrites it. Restoring also snapshots the current
state first (if one doesn't exist), so a restore is normally reversible.

Examples:
  # Show version history by ID
  dtctl history document a1b2c3d4-e5f6-7890-abcd-ef1234567890

  # Show version history by name
  dtctl history document "My Launchpad"

  # Output as JSON
  dtctl history document "My Launchpad" -o json
`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			identifier := args[0]

			_, c, printer, err := setup(cmdContext(cmd))
			if err != nil {
				return err
			}

			// Resolve name to ID (searches across all document types)
			res := resolver.NewResolver(c)
			documentID, err := res.ResolveID(resolver.TypeDocument, identifier)
			if err != nil {
				return err
			}

			handler := document.NewHandler(c)

			snapshots, err := handler.ListSnapshots(documentID)
			if err != nil {
				return err
			}

			if len(snapshots.Snapshots) == 0 {
				fmt.Fprintln(currentStdout(cmdContext(cmd)), "No snapshots found for this document")
				if !plainMode(cmdContext(cmd)) {
					fmt.Fprintln(currentStdout(cmdContext(cmd)), "Snapshots are opt-in: pass --create-snapshot when updating to capture the pre-update content")
				}
				return nil
			}

			return printer.PrintList(snapshots.Snapshots)
		},
	}
	stability.MarkStable(c)
	return c
}

func init() {
	rootCmd.AddCommand(historyCmd)

	historyCmd.AddCommand(historyWorkflowCmd)
	historyCmd.AddCommand(historyDashboardCmd)
	historyCmd.AddCommand(historyNotebookCmd)
	historyCmd.AddCommand(historyDocumentCmd)
}

// Declared stable: the invocation and output contract of these commands is
// additive-only. Stable is never implied -- see AGENTS.md "Stability Tiers".
func init() {
}
