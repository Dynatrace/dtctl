package cmd

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/dynatrace-oss/dtctl/pkg/output"
	"github.com/dynatrace-oss/dtctl/pkg/prompt"
	"github.com/dynatrace-oss/dtctl/pkg/resources/document"
	"github.com/dynatrace-oss/dtctl/pkg/resources/resolver"
	"github.com/dynatrace-oss/dtctl/pkg/resources/workflow"
	"github.com/dynatrace-oss/dtctl/pkg/safety"
	"github.com/dynatrace-oss/dtctl/pkg/stability"
)

// restoreCmd represents the restore command
var restoreCmd = newRestoreCmd()

func newRestoreCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "restore",
		Short: "Restore resources to a previous version",
		Long:  `Restore resources like workflows, notebooks, and dashboards to a previous version.`,
		RunE:  requireSubcommand,
	}
	stability.MarkStable(c)
	return c
}

// restoreWorkflowCmd restores a workflow to a specific version
var restoreWorkflowCmd = newRestoreWorkflowCmd()

func newRestoreWorkflowCmd() *cobra.Command {
	var forceDelete bool
	c := &cobra.Command{
		Use:     "workflow <workflow-id-or-name> <version>",
		Aliases: []string{"workflows", "wf"},
		Short:   "Restore a workflow to a previous version",
		Long: `Restore a workflow to a previous version from its history.

This operation restores the workflow to the specified version and deploys it.

Examples:
  # Restore by ID to version 5
  dtctl restore workflow a1b2c3d4-e5f6-7890-abcd-ef1234567890 5

  # Restore by name
  dtctl restore workflow "My Workflow" 3

  # Restore without confirmation
  dtctl restore workflow "My Workflow" 3 --force
`,
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			identifier := args[0]
			version, err := strconv.Atoi(args[1])
			if err != nil {
				return fmt.Errorf("invalid version number: %s", args[1])
			}

			cfg, c, err := setupClient(cmdContext(cmd))
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

			// Get workflow for confirmation and ownership check
			wf, err := handler.Get(workflowID)
			if err != nil {
				return err
			}

			// Safety check with actual ownership - restore modifies the workflow
			currentUserID, _ := c.CurrentUserID()
			ownership := safety.DetermineOwnership(wf.Owner, currentUserID)
			if err := checkSafety(cmdContext(cmd), cfg, safety.OperationUpdate, ownership); err != nil {
				return err
			}

			if dryRun(cmdContext(cmd)) {
				return newDryRunReport(cmd).Linef("Dry run: would restore workflow %q to version %d", wf.Title, version).Print()
			}

			// Confirm restore unless --force or --plain
			if !forceDelete && !plainMode(cmdContext(cmd)) {
				confirmMsg := fmt.Sprintf("Restore workflow %q to version %d?", wf.Title, version)
				if !prompt.ConfirmWith(currentStdin(cmdContext(cmd)), currentStdout(cmdContext(cmd)), confirmMsg) {
					fmt.Fprintln(currentStdout(cmdContext(cmd)), "Restore cancelled")
					return nil
				}
			}

			result, err := handler.RestoreHistory(workflowID, version)
			if err != nil {
				return err
			}

			output.FprintSuccess(currentStderr(cmdContext(cmd)), "Workflow %q restored to version %d", result.Title, version)
			return nil
		},
	}
	c.Flags().BoolVarP(&forceDelete, "force", "f", false, "Skip confirmation prompt")
	stability.MarkFlag(c, "force", stability.Experimental, pre10Since)
	stability.MarkStable(c)
	return c
}

// restoreDashboardCmd restores a dashboard to a specific version
var restoreDashboardCmd = newRestoreDashboardCmd()

func newRestoreDashboardCmd() *cobra.Command {
	var forceDelete bool
	c := &cobra.Command{
		Use:     "dashboard <dashboard-id-or-name> <version>",
		Aliases: []string{"dashboards", "dash", "db"},
		Short:   "Restore a dashboard to a previous version",
		Long: `Restore a dashboard to a previous snapshot version.

This operation resets the document's content to the state it had when the snapshot
was created. A new snapshot of the current state is automatically created before
restoring (if one doesn't exist).

Note: Only the document owner can restore snapshots.

Examples:
  # Restore by ID to version 5
  dtctl restore dashboard a1b2c3d4-e5f6-7890-abcd-ef1234567890 5

  # Restore by name
  dtctl restore dashboard "Production Dashboard" 3

  # Restore without confirmation
  dtctl restore dashboard "Production Dashboard" 3 --force
`,
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			identifier := args[0]
			version, err := strconv.Atoi(args[1])
			if err != nil {
				return fmt.Errorf("invalid version number: %s", args[1])
			}

			cfg, c, err := setupClient(cmdContext(cmd))
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

			// Get dashboard metadata for confirmation and ownership check
			metadata, err := handler.GetMetadata(dashboardID)
			if err != nil {
				return err
			}

			if err := requireDocumentType(metadata, "dashboard", dashboardID); err != nil {
				return err
			}
			// Safety check with actual ownership - restore modifies the dashboard
			currentUserID, _ := c.CurrentUserID()
			ownership := safety.DetermineOwnership(metadata.Owner, currentUserID)
			if err := checkSafety(cmdContext(cmd), cfg, safety.OperationUpdate, ownership); err != nil {
				return err
			}

			if dryRun(cmdContext(cmd)) {
				return newDryRunReport(cmd).Linef("Dry run: would restore dashboard %q from snapshot %d", metadata.Name, version).Print()
			}

			// Confirm restore unless --force or --plain
			if !forceDelete && !plainMode(cmdContext(cmd)) {
				confirmMsg := fmt.Sprintf("Restore dashboard %q from snapshot %d?", metadata.Name, version)
				if !prompt.ConfirmWith(currentStdin(cmdContext(cmd)), currentStdout(cmdContext(cmd)), confirmMsg) {
					fmt.Fprintln(currentStdout(cmdContext(cmd)), "Restore cancelled")
					return nil
				}
			}

			result, err := handler.RestoreSnapshot(dashboardID, version)
			if err != nil {
				return err
			}

			output.FprintSuccess(currentStderr(cmdContext(cmd)), "Dashboard %q restored from snapshot %d (new document version: %d)", metadata.Name, version, result.Version)
			return nil
		},
	}
	c.Flags().BoolVarP(&forceDelete, "force", "f", false, "Skip confirmation prompt")
	stability.MarkFlag(c, "force", stability.Experimental, pre10Since)
	stability.MarkStable(c)
	return c
}

// restoreNotebookCmd restores a notebook to a specific version
var restoreNotebookCmd = newRestoreNotebookCmd()

func newRestoreNotebookCmd() *cobra.Command {
	var forceDelete bool
	c := &cobra.Command{
		Use:     "notebook <notebook-id-or-name> <version>",
		Aliases: []string{"notebooks", "nb"},
		Short:   "Restore a notebook to a previous version",
		Long: `Restore a notebook to a previous snapshot version.

This operation resets the document's content to the state it had when the snapshot
was created. A new snapshot of the current state is automatically created before
restoring (if one doesn't exist).

Note: Only the document owner can restore snapshots.

Examples:
  # Restore by ID to version 5
  dtctl restore notebook a1b2c3d4-e5f6-7890-abcd-ef1234567890 5

  # Restore by name
  dtctl restore notebook "Analysis Notebook" 3

  # Restore without confirmation
  dtctl restore notebook "Analysis Notebook" 3 --force
`,
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			identifier := args[0]
			version, err := strconv.Atoi(args[1])
			if err != nil {
				return fmt.Errorf("invalid version number: %s", args[1])
			}

			cfg, c, err := setupClient(cmdContext(cmd))
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

			// Get notebook metadata for confirmation and ownership check
			metadata, err := handler.GetMetadata(notebookID)
			if err != nil {
				return err
			}

			if err := requireDocumentType(metadata, "notebook", notebookID); err != nil {
				return err
			}
			// Safety check with actual ownership - restore modifies the notebook
			currentUserID, _ := c.CurrentUserID()
			ownership := safety.DetermineOwnership(metadata.Owner, currentUserID)
			if err := checkSafety(cmdContext(cmd), cfg, safety.OperationUpdate, ownership); err != nil {
				return err
			}

			if dryRun(cmdContext(cmd)) {
				return newDryRunReport(cmd).Linef("Dry run: would restore notebook %q from snapshot %d", metadata.Name, version).Print()
			}

			// Confirm restore unless --force or --plain
			if !forceDelete && !plainMode(cmdContext(cmd)) {
				confirmMsg := fmt.Sprintf("Restore notebook %q from snapshot %d?", metadata.Name, version)
				if !prompt.ConfirmWith(currentStdin(cmdContext(cmd)), currentStdout(cmdContext(cmd)), confirmMsg) {
					fmt.Fprintln(currentStdout(cmdContext(cmd)), "Restore cancelled")
					return nil
				}
			}

			result, err := handler.RestoreSnapshot(notebookID, version)
			if err != nil {
				return err
			}

			output.FprintSuccess(currentStderr(cmdContext(cmd)), "Notebook %q restored from snapshot %d (new document version: %d)", metadata.Name, version, result.Version)
			return nil
		},
	}
	c.Flags().BoolVarP(&forceDelete, "force", "f", false, "Skip confirmation prompt")
	stability.MarkFlag(c, "force", stability.Experimental, pre10Since)
	stability.MarkStable(c)
	return c
}

// restoreDocumentCmd restores a document of any type to a specific version
var restoreDocumentCmd = newRestoreDocumentCmd()

func newRestoreDocumentCmd() *cobra.Command {
	var forceDelete bool
	c := &cobra.Command{
		Use:     "document <document-id-or-name> <version>",
		Aliases: []string{"documents", "doc"},
		Short:   "Restore a document to a previous version",
		Long: `Restore a document of any type to a previous snapshot version.

Works for any document type (dashboard, notebook, launchpad, custom app documents, etc.).

This operation resets the document's content to the state it had when the snapshot
was created. A new snapshot of the current state is automatically created before
restoring (if one doesn't exist).

Note: Only the document owner can restore snapshots.

Examples:
  # Restore by ID to version 5
  dtctl restore document a1b2c3d4-e5f6-7890-abcd-ef1234567890 5

  # Restore by name
  dtctl restore document "My Launchpad" 3

  # Restore without confirmation
  dtctl restore document "My Launchpad" 3 --force
`,
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			identifier := args[0]
			version, err := strconv.Atoi(args[1])
			if err != nil {
				return fmt.Errorf("invalid version number: %s", args[1])
			}

			cfg, c, err := setupClient(cmdContext(cmd))
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

			// Get document metadata for confirmation and ownership check
			metadata, err := handler.GetMetadata(documentID)
			if err != nil {
				return err
			}

			// Safety check with actual ownership - restore modifies the document
			currentUserID, _ := c.CurrentUserID()
			ownership := safety.DetermineOwnership(metadata.Owner, currentUserID)
			if err := checkSafety(cmdContext(cmd), cfg, safety.OperationUpdate, ownership); err != nil {
				return err
			}

			if dryRun(cmdContext(cmd)) {
				return newDryRunReport(cmd).Linef("Dry run: would restore document %q (%s) from snapshot %d", metadata.Name, metadata.Type, version).Print()
			}

			// Confirm restore unless --force or --plain
			if !forceDelete && !plainMode(cmdContext(cmd)) {
				confirmMsg := fmt.Sprintf("Restore document %q (%s) from snapshot %d?", metadata.Name, metadata.Type, version)
				if !prompt.ConfirmWith(currentStdin(cmdContext(cmd)), currentStdout(cmdContext(cmd)), confirmMsg) {
					fmt.Fprintln(currentStdout(cmdContext(cmd)), "Restore cancelled")
					return nil
				}
			}

			result, err := handler.RestoreSnapshot(documentID, version)
			if err != nil {
				return err
			}

			output.FprintSuccess(currentStderr(cmdContext(cmd)), "Document %q (%s) restored from snapshot %d (new document version: %d)", metadata.Name, metadata.Type, version, result.Version)
			return nil
		},
	}
	c.Flags().BoolVarP(&forceDelete, "force", "f", false, "Skip confirmation prompt")
	stability.MarkFlag(c, "force", stability.Experimental, pre10Since)
	stability.MarkStable(c)
	return c
}

// restoreTrashCmd restores documents from trash
var restoreTrashCmd = newRestoreTrashCmd()

func newRestoreTrashCmd() *cobra.Command {
	c := &cobra.Command{
		Use:     "trash <document-id> [document-id...]",
		Aliases: []string{"deleted"},
		Short:   "Restore document(s) from trash",
		Long: `Restore one or more documents from trash.

Documents can be restored from trash if they haven't expired yet. By default,
documents are kept in trash for 30 days before permanent deletion.

Examples:
  # Restore a single document
  dtctl restore trash a1b2c3d4-e5f6-7890-abcd-ef1234567890

  # Restore multiple documents
  dtctl restore trash <id1> <id2> <id3>

  # Restore with a new name (to avoid conflicts)
  dtctl restore trash <id> --new-name "Recovered Dashboard"

  # Force restore (overwrite if name conflict exists)
  dtctl restore trash <id> --force
`,
		Args: cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			_, c, err := setupWithSafety(cmdContext(cmd), safety.OperationUpdate)
			if err != nil {
				return err
			}

			handler := document.NewTrashHandler(c)

			// Get flags
			forceRestore, _ := cmd.Flags().GetBool("force")
			newName, _ := cmd.Flags().GetString("new-name")

			if dryRun(cmdContext(cmd)) {
				return newDryRunReport(cmd).
					Linef("Dry run: would restore %d document(s) from trash: %s", len(args), strings.Join(args, ", ")).
					Detail("ids", "%s", strings.Join(args, ",")).
					Print()
			}

			opts := document.RestoreOptions{
				Force:   forceRestore,
				NewName: newName,
			}

			// Restore each document
			successCount := 0
			for _, docID := range args {
				// Get document info first
				doc, err := handler.Get(docID)
				if err != nil {
					fmt.Fprintf(currentStdout(cmdContext(cmd)), "Error getting document %s: %v\n", docID, err)
					continue
				}

				// Confirm restore unless --force or --plain or restoring multiple
				if !forceRestore && !plainMode(cmdContext(cmd)) && len(args) == 1 {
					confirmMsg := fmt.Sprintf("Restore %s %q from trash?", doc.Type, doc.Name)
					if !prompt.ConfirmWith(currentStdin(cmdContext(cmd)), currentStdout(cmdContext(cmd)), confirmMsg) {
						fmt.Fprintln(currentStdout(cmdContext(cmd)), "Restore cancelled")
						continue
					}
				}

				err = handler.Restore(docID, opts)
				if err != nil {
					fmt.Fprintf(currentStderr(cmdContext(cmd)), "Failed to restore document %s: %v\n", docID, err)
					continue
				}

				output.FprintSuccess(currentStderr(cmdContext(cmd)), "Restored %s %q (ID: %s)", doc.Type, doc.Name, docID)
				successCount++
			}

			if successCount == 0 && len(args) > 0 {
				return fmt.Errorf("failed to restore any documents")
			}

			if len(args) > 1 {
				output.FprintInfo(currentStderr(cmdContext(cmd)), "\nRestored %d of %d documents", successCount, len(args))
			}

			return nil
		},
	}
	c.Flags().Bool("force", false, "Restore even if name conflicts exist")
	c.Flags().String("new-name", "", "Restore with a new name")
	stability.MarkStable(c)
	return c
}

func init() {
	rootCmd.AddCommand(restoreCmd)

	restoreCmd.AddCommand(restoreWorkflowCmd)
	restoreCmd.AddCommand(restoreDashboardCmd)
	restoreCmd.AddCommand(restoreNotebookCmd)
	restoreCmd.AddCommand(restoreDocumentCmd)
	restoreCmd.AddCommand(restoreTrashCmd)

	// Add --force flag to restore commands
	// -f is reserved for --file in 1.0 (contrib breaking-changes/short-flag-f.md).
	// Restore trash flags
}

// Declared stable: the invocation and output contract of these commands is
// additive-only. Stable is never implied -- see AGENTS.md "Stability Tiers".
func init() {
}
