package cmd

import (
	"fmt"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/dynatrace-oss/dtctl/pkg/client"
	"github.com/dynatrace-oss/dtctl/pkg/output"
	"github.com/dynatrace-oss/dtctl/pkg/prompt"
	"github.com/dynatrace-oss/dtctl/pkg/resources/document"
	"github.com/dynatrace-oss/dtctl/pkg/resources/resolver"
	"github.com/dynatrace-oss/dtctl/pkg/safety"
	"github.com/dynatrace-oss/dtctl/pkg/stability"
)

// getDashboardsCmd retrieves dashboards
var getDashboardsCmd = newGetDashboardsCmd()

func newGetDashboardsCmd() *cobra.Command {
	c := &cobra.Command{
		Use:     "dashboards [id]",
		Aliases: []string{"dashboard", "dash", "db"},
		Short:   "Get dashboards",
		Long: `Get one or more dashboards.

Examples:
  # List all dashboards
  dtctl get dashboards
  dtctl get dash

  # Get a specific dashboard
  dtctl get dashboard <dashboard-id>

  # Output as JSON
  dtctl get dashboards -o json

  # Filter by name
  dtctl get dashboards --name "production"

  # List only my dashboards
  dtctl get dashboards --mine
`,
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, c, printer, err := setup(cmdContext(cmd))
			if err != nil {
				return err
			}

			handler := document.NewHandler(c)

			// Get specific dashboard if ID provided
			if len(args) > 0 {
				doc, err := handler.Get(args[0])
				if err != nil {
					return err
				}
				return printer.Print(doc)
			}

			// List all dashboards
			filters, err := buildDocumentFilters(cmd, c, "dashboard")
			if err != nil {
				return err
			}

			// Check if watch mode is enabled
			watchMode, _ := cmd.Flags().GetBool("watch")
			if watchMode {
				fetcher := func() (interface{}, error) {
					list, err := listDocuments(handler, filters, cfg)
					if err != nil {
						return nil, err
					}
					return document.ConvertToDocuments(list), nil
				}
				return executeWithWatch(cmd, fetcher, printer)
			}

			list, err := listDocuments(handler, filters, cfg)
			if err != nil {
				return err
			}

			return printer.PrintList(document.ConvertToDocuments(list))
		},
	}
	stability.MarkStable(c)
	addWatchFlags(c)
	addDocumentListFlags(c, false)
	return c
}

// getNotebooksCmd retrieves notebooks
var getNotebooksCmd = newGetNotebooksCmd()

func newGetNotebooksCmd() *cobra.Command {
	c := &cobra.Command{
		Use:     "notebooks [id]",
		Aliases: []string{"notebook", "nb"},
		Short:   "Get notebooks",
		Long: `Get one or more notebooks.

Examples:
  # List all notebooks
  dtctl get notebooks
  dtctl get nb

  # Get a specific notebook
  dtctl get notebook <notebook-id>

  # Output as JSON
  dtctl get notebooks -o json

  # Filter by name
  dtctl get notebooks --name "analysis"

  # List only my notebooks
  dtctl get notebooks --mine
`,
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, c, printer, err := setup(cmdContext(cmd))
			if err != nil {
				return err
			}

			handler := document.NewHandler(c)

			// Get specific notebook if ID provided
			if len(args) > 0 {
				doc, err := handler.Get(args[0])
				if err != nil {
					return err
				}
				return printer.Print(doc)
			}

			// List all notebooks
			filters, err := buildDocumentFilters(cmd, c, "notebook")
			if err != nil {
				return err
			}

			// Check if watch mode is enabled
			watchMode, _ := cmd.Flags().GetBool("watch")
			if watchMode {
				fetcher := func() (interface{}, error) {
					list, err := listDocuments(handler, filters, cfg)
					if err != nil {
						return nil, err
					}
					return document.ConvertToDocuments(list), nil
				}
				return executeWithWatch(cmd, fetcher, printer)
			}

			list, err := listDocuments(handler, filters, cfg)
			if err != nil {
				return err
			}

			return printer.PrintList(document.ConvertToDocuments(list))
		},
	}
	stability.MarkStable(c)
	addWatchFlags(c)
	addDocumentListFlags(c, false)
	return c
}

// getTrashCmd retrieves trashed documents
var getTrashCmd = newGetTrashCmd()

func newGetTrashCmd() *cobra.Command {
	c := &cobra.Command{
		Use:     "trash",
		Aliases: []string{"deleted"},
		Short:   "Get trashed documents",
		Long: `List or get trashed documents (dashboards and notebooks).

Documents are soft-deleted and kept in trash for 30 days before permanent deletion.

Examples:
  # List all trashed documents
  dtctl get trash

  # List only trashed dashboards
  dtctl get trash --type dashboard

  # List only trashed notebooks
  dtctl get trash --type notebook

  # Filter by who deleted it
  dtctl get trash --deleted-by user@example.com

  # Filter by deletion date
  dtctl get trash --deleted-after 2024-01-01
  dtctl get trash --deleted-before 2024-12-31

  # Output as JSON
  dtctl get trash -o json
`,
		RunE: func(cmd *cobra.Command, args []string) error {
			_, c, printer, err := setup(cmdContext(cmd))
			if err != nil {
				return err
			}

			handler := document.NewTrashHandler(c)

			// Build filter options from flags
			typeFilter, _ := cmd.Flags().GetString("type")
			deletedBy, _ := cmd.Flags().GetString("deleted-by")
			deletedAfter, _ := cmd.Flags().GetString("deleted-after")
			deletedBefore, _ := cmd.Flags().GetString("deleted-before")

			opts := document.TrashListOptions{
				Type:      typeFilter,
				DeletedBy: deletedBy,
				ChunkSize: getChunkSize(cmdContext(cmd)),
			}

			// Parse date filters if provided
			if deletedAfter != "" {
				t, err := time.Parse("2006-01-02", deletedAfter)
				if err != nil {
					return fmt.Errorf("invalid deleted-after date format (use YYYY-MM-DD): %w", err)
				}
				opts.DeletedAfter = t
			}
			if deletedBefore != "" {
				t, err := time.Parse("2006-01-02", deletedBefore)
				if err != nil {
					return fmt.Errorf("invalid deleted-before date format (use YYYY-MM-DD): %w", err)
				}
				opts.DeletedBefore = t
			}

			// Check if watch mode is enabled
			watchMode, _ := cmd.Flags().GetBool("watch")
			if watchMode {
				fetcher := func() (interface{}, error) {
					return handler.List(opts)
				}
				return executeWithWatch(cmd, fetcher, printer)
			}

			// List trash
			docs, err := handler.List(opts)
			if err != nil {
				return err
			}

			return printer.PrintList(docs)
		},
	}
	c.Flags().String("type", "", "Filter by type: dashboard, notebook")
	c.Flags().String("deleted-by", "", "Filter by who deleted it")
	c.Flags().String("deleted-after", "", "Show documents deleted after date (YYYY-MM-DD)")
	c.Flags().String("deleted-before", "", "Show documents deleted before date (YYYY-MM-DD)")
	stability.MarkStable(c)
	addWatchFlags(c)
	return c
}

// deleteDashboardCmd deletes a dashboard
var deleteDashboardCmd = newDeleteDashboardCmd()

func newDeleteDashboardCmd() *cobra.Command {
	var forceDelete bool
	c := &cobra.Command{
		Use:     "dashboard <dashboard-id-or-name>",
		Aliases: []string{"dashboards", "dash", "db"},
		Short:   "Delete a dashboard",
		Long: `Delete a dashboard by ID or name.

Examples:
  # Delete by ID
  dtctl delete dashboard a1b2c3d4-e5f6-7890-abcd-ef1234567890

  # Delete by name (interactive disambiguation if multiple matches)
  dtctl delete dashboard "Production Dashboard"

  # Delete without confirmation
  dtctl delete dashboard "Production Dashboard" -y
`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			identifier := args[0]

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

			// Get current version for optimistic locking and details for confirmation
			metadata, err := handler.GetMetadata(dashboardID)
			if err != nil {
				return err
			}

			if err := requireDocumentType(metadata, "dashboard", dashboardID); err != nil {
				return err
			}
			// Safety check with actual ownership
			currentUserID, _ := c.CurrentUserID()
			ownership := safety.DetermineOwnership(metadata.Owner, currentUserID)
			if err := checkSafety(cmdContext(cmd), cfg, safety.OperationDelete, ownership); err != nil {
				return err
			}

			if dryRun(cmdContext(cmd)) {
				return deleteDryRun(cmd, "dashboard", metadata.Name, dashboardID)
			}

			// Confirm deletion unless --force or --plain
			if !forceDelete && !plainMode(cmdContext(cmd)) {
				if !prompt.ConfirmDeletionWith(currentStdin(cmdContext(cmd)), currentStdout(cmdContext(cmd)), "dashboard", metadata.Name, dashboardID) {
					fmt.Fprintln(currentStdout(cmdContext(cmd)), "Deletion cancelled")
					return nil
				}
			}

			if err := handler.Delete(dashboardID, metadata.Version); err != nil {
				return err
			}

			output.FprintSuccess(currentStderr(cmdContext(cmd)), "Dashboard %q deleted (moved to trash)", metadata.Name)
			return nil
		},
	}
	c.Flags().BoolVarP(&forceDelete, "yes", "y", false, "Skip confirmation prompt")
	stability.MarkStable(c)
	return c
}

// deleteNotebookCmd deletes a notebook
var deleteNotebookCmd = newDeleteNotebookCmd()

func newDeleteNotebookCmd() *cobra.Command {
	var forceDelete bool
	c := &cobra.Command{
		Use:     "notebook <notebook-id-or-name>",
		Aliases: []string{"notebooks", "nb"},
		Short:   "Delete a notebook",
		Long: `Delete a notebook by ID or name.

Examples:
  # Delete by ID
  dtctl delete notebook a1b2c3d4-e5f6-7890-abcd-ef1234567890

  # Delete by name (interactive disambiguation if multiple matches)
  dtctl delete notebook "Analysis Notebook"

  # Delete without confirmation
  dtctl delete notebook "Analysis Notebook" -y
`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			identifier := args[0]

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

			// Get current version for optimistic locking and details for confirmation
			metadata, err := handler.GetMetadata(notebookID)
			if err != nil {
				return err
			}

			if err := requireDocumentType(metadata, "notebook", notebookID); err != nil {
				return err
			}
			// Safety check with actual ownership
			currentUserID, _ := c.CurrentUserID()
			ownership := safety.DetermineOwnership(metadata.Owner, currentUserID)
			if err := checkSafety(cmdContext(cmd), cfg, safety.OperationDelete, ownership); err != nil {
				return err
			}

			if dryRun(cmdContext(cmd)) {
				return deleteDryRun(cmd, "notebook", metadata.Name, notebookID)
			}

			// Confirm deletion unless --force or --plain
			if !forceDelete && !plainMode(cmdContext(cmd)) {
				if !prompt.ConfirmDeletionWith(currentStdin(cmdContext(cmd)), currentStdout(cmdContext(cmd)), "notebook", metadata.Name, notebookID) {
					fmt.Fprintln(currentStdout(cmdContext(cmd)), "Deletion cancelled")
					return nil
				}
			}

			if err := handler.Delete(notebookID, metadata.Version); err != nil {
				return err
			}

			output.FprintSuccess(currentStderr(cmdContext(cmd)), "Notebook %q deleted (moved to trash)", metadata.Name)
			return nil
		},
	}
	c.Flags().BoolVarP(&forceDelete, "yes", "y", false, "Skip confirmation prompt")
	stability.MarkStable(c)
	return c
}

// deleteTrashCmd permanently deletes documents from trash
var deleteTrashCmd = newDeleteTrashCmd()

func newDeleteTrashCmd() *cobra.Command {
	var forceDelete bool
	c := &cobra.Command{
		Use:     "trash <document-id> [document-id...]",
		Aliases: []string{"deleted"},
		Short:   "Permanently delete document(s) from trash",
		Long: `Permanently delete one or more documents from trash.

WARNING: This operation cannot be undone. Documents will be permanently deleted
and cannot be recovered.

The --permanent flag is required to prevent accidental deletion.

Examples:
  # Permanently delete a single document
  dtctl delete trash a1b2c3d4-e5f6-7890-abcd-ef1234567890 --permanent

  # Permanently delete multiple documents
  dtctl delete trash <id1> <id2> <id3> --permanent -y
`,
		Args: cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			// Check for --permanent flag before setup (no API call needed)
			permanent, _ := cmd.Flags().GetBool("permanent")
			if !permanent {
				return fmt.Errorf("--permanent flag is required to delete from trash")
			}

			_, c, err := setupWithSafety(cmdContext(cmd), safety.OperationDelete)
			if err != nil {
				return err
			}

			handler := document.NewTrashHandler(c)

			if dryRun(cmdContext(cmd)) {
				return newDryRunReport(cmd).
					Linef("Dry run: would permanently delete %d document(s) from trash: %s", len(args), strings.Join(args, ", ")).
					Detail("ids", "%s", strings.Join(args, ",")).
					Print()
			}

			// Confirm deletion unless --force or --plain or deleting multiple
			if !forceDelete && !plainMode(cmdContext(cmd)) {
				var docNames []string
				for _, docID := range args {
					doc, err := handler.Get(docID)
					if err != nil {
						output.FprintWarning(currentStderr(cmdContext(cmd)), "Could not get document %s: %v", docID, err)
						docNames = append(docNames, docID)
					} else {
						docNames = append(docNames, fmt.Sprintf("%s %q", doc.Type, doc.Name))
					}
				}

				confirmMsg := fmt.Sprintf("PERMANENTLY DELETE %d document(s) from trash? This cannot be undone.", len(args))
				if !prompt.ConfirmWith(currentStdin(cmdContext(cmd)), currentStdout(cmdContext(cmd)), confirmMsg) {
					fmt.Fprintln(currentStdout(cmdContext(cmd)), "Deletion cancelled")
					return nil
				}
			}

			// Delete each document
			successCount := 0
			for _, docID := range args {
				err := handler.Delete(docID)
				if err != nil {
					fmt.Fprintf(currentStderr(cmdContext(cmd)), "Failed to delete document %s: %v\n", docID, err)
					continue
				}

				output.FprintSuccess(currentStderr(cmdContext(cmd)), "Permanently deleted document %s", docID)
				successCount++
			}

			if successCount == 0 && len(args) > 0 {
				return fmt.Errorf("failed to delete any documents")
			}

			if len(args) > 1 {
				output.FprintInfo(currentStderr(cmdContext(cmd)), "\nDeleted %d of %d documents", successCount, len(args))
			}

			return nil
		},
	}
	c.Flags().Bool("permanent", false, "Permanently delete (required)")
	c.Flags().BoolVarP(&forceDelete, "yes", "y", false, "Skip confirmation prompt")
	stability.MarkStable(c)
	return c
}

// DocumentTypeCount holds a count per document type (for --types flag)
type DocumentTypeCount struct {
	Type  string `table:"TYPE" json:"type" yaml:"type"`
	Count int    `table:"COUNT" json:"count" yaml:"count"`
}

// getDocumentsCmd retrieves generic documents (any type)
var getDocumentsCmd = newGetDocumentsCmd()

func newGetDocumentsCmd() *cobra.Command {
	c := &cobra.Command{
		Use:     "documents [id]",
		Aliases: []string{"document", "doc"},
		Short:   "Get documents (any type)",
		Long: `Get one or more documents of any type.

Unlike 'dtctl get dashboards' or 'dtctl get notebooks' which filter by a
specific type, this command lists ALL document types by default.

The TYPE column is always shown to disambiguate across types.

Examples:
  # List all documents (all types)
  dtctl get documents
  dtctl get doc

  # Get a specific document by ID
  dtctl get document <document-id>

  # Filter by type
  dtctl get documents --type dashboard
  dtctl get documents --type launchpad
  dtctl get documents --type my-custom-app:config

  # Filter by name
  dtctl get documents --name "production"

  # List only my documents
  dtctl get documents --mine

  # Discover what document types exist in the environment
  dtctl get documents --types

  # Output as JSON
  dtctl get documents -o json
`,
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, c, printer, err := setup(cmdContext(cmd))
			if err != nil {
				return err
			}

			handler := document.NewHandler(c)

			// Get specific document if ID provided
			if len(args) > 0 {
				doc, err := handler.Get(args[0])
				if err != nil {
					return err
				}
				return printer.Print(doc)
			}

			// Check for --types flag (type discovery)
			typesMode, _ := cmd.Flags().GetBool("types")
			typeFilter, _ := cmd.Flags().GetString("type")

			filters, err := buildDocumentFilters(cmd, c, typeFilter)
			if err != nil {
				return err
			}

			if typesMode {
				// Fetch all documents (no type filter) and count by type
				allFilters := document.DocumentFilters{
					Owner:     filters.Owner,
					ChunkSize: getChunkSize(cmdContext(cmd)),
				}
				list, err := handler.List(allFilters)
				if err != nil {
					return err
				}
				typeCounts := map[string]int{}
				for _, doc := range list.Documents {
					typeCounts[doc.Type]++
				}
				var counts []DocumentTypeCount
				for t, n := range typeCounts {
					counts = append(counts, DocumentTypeCount{Type: t, Count: n})
				}
				return printer.PrintList(counts)
			}

			// Check if watch mode is enabled
			watchMode, _ := cmd.Flags().GetBool("watch")
			if watchMode {
				fetcher := func() (interface{}, error) {
					list, err := listDocuments(handler, filters, cfg)
					if err != nil {
						return nil, err
					}
					return document.ConvertToDocuments(list), nil
				}
				return executeWithWatch(cmd, fetcher, printer)
			}

			list, err := listDocuments(handler, filters, cfg)
			if err != nil {
				return err
			}

			return printer.PrintList(document.ConvertToDocuments(list))
		},
	}
	c.Flags().Bool("types", false, "List distinct document types and counts")
	stability.MarkStable(c)
	addWatchFlags(c)
	addDocumentListFlags(c, true)
	return c
}

// deleteDocumentCmd deletes a generic document (any type)
var deleteDocumentCmd = newDeleteDocumentCmd()

func newDeleteDocumentCmd() *cobra.Command {
	var forceDelete bool
	c := &cobra.Command{
		Use:     "document <document-id-or-name>",
		Aliases: []string{"documents", "doc"},
		Short:   "Delete a document",
		Long: `Delete a document by ID or name.

Works for any document type (dashboard, notebook, launchpad, custom app documents, etc.).
An argument that is an existing document's ID, including a custom non-UUID ID,
always refers to that document; otherwise it is matched against document names.

Examples:
  # Delete by ID
  dtctl delete document a1b2c3d4-e5f6-7890-abcd-ef1234567890

  # Delete by a custom ID (e.g. one set with 'create document --id')
  dtctl delete document my-launchpad

  # Delete by name (interactive disambiguation if multiple matches)
  dtctl delete document "My Launchpad"

  # Delete without confirmation
  dtctl delete document "My Launchpad" -y
`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			identifier := args[0]

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

			// Get current version for optimistic locking and details for confirmation
			metadata, err := handler.GetMetadata(documentID)
			if err != nil {
				return err
			}

			// Safety check with actual ownership
			currentUserID, _ := c.CurrentUserID()
			ownership := safety.DetermineOwnership(metadata.Owner, currentUserID)
			if err := checkSafety(cmdContext(cmd), cfg, safety.OperationDelete, ownership); err != nil {
				return err
			}

			if dryRun(cmdContext(cmd)) {
				return deleteDryRun(cmd, metadata.Type, metadata.Name, documentID)
			}

			// Confirm deletion unless --force or --plain
			if !forceDelete && !plainMode(cmdContext(cmd)) {
				if !prompt.ConfirmDeletionWith(currentStdin(cmdContext(cmd)), currentStdout(cmdContext(cmd)), metadata.Type, metadata.Name, documentID) {
					fmt.Fprintln(currentStdout(cmdContext(cmd)), "Deletion cancelled")
					return nil
				}
			}

			if err := handler.Delete(documentID, metadata.Version); err != nil {
				return err
			}

			output.FprintSuccess(currentStderr(cmdContext(cmd)), "Document %q (%s) deleted (moved to trash)", metadata.Name, metadata.Type)
			return nil
		},
	}
	c.Flags().BoolVarP(&forceDelete, "yes", "y", false, "Skip confirmation prompt")
	stability.MarkStable(c)
	return c
}

// buildDocumentFilters reads the listing flags from cmd. implicitType is the
// type baked into the subcommand (empty for `dtctl get documents`).
func buildDocumentFilters(cmd *cobra.Command, c *client.Client, implicitType string) (document.DocumentFilters, error) {
	rawFilter, _ := cmd.Flags().GetString("filter")
	nameFilter, _ := cmd.Flags().GetString("name")
	mineOnly, _ := cmd.Flags().GetBool("mine")
	sortOrder, _ := cmd.Flags().GetString("sort")
	addFields, _ := cmd.Flags().GetStringSlice("add-fields")
	adminAccess, _ := cmd.Flags().GetBool("admin-access")

	filters := document.DocumentFilters{
		ChunkSize:   getChunkSize(cmdContext(cmd)),
		Sort:        sortOrder,
		AddFields:   addFields,
		AdminAccess: adminAccess,
	}

	if rawFilter != "" {
		if nameFilter != "" || mineOnly {
			fmt.Fprintln(currentStderr(cmdContext(cmd)), "warning: --filter overrides --name/--mine; the raw filter is sent verbatim to the API")
		}
		// For type-scoped commands (dashboards, notebooks), enforce the implicit
		// type even when --filter is provided, so dtctl get dashboards --filter "..."
		// still returns only dashboards.
		if implicitType != "" {
			filters.Filter = fmt.Sprintf("type=='%s' and (%s)", implicitType, rawFilter)
		} else {
			filters.Filter = rawFilter
		}
		return filters, nil
	}

	filters.Type = implicitType
	filters.Name = nameFilter
	if mineOnly {
		userID, err := c.CurrentUserID()
		if err != nil {
			return filters, fmt.Errorf("failed to get current user ID for --mine filter: %w", err)
		}
		filters.Owner = userID
	}
	return filters, nil
}

// addDocumentListFlags registers the flags shared by dashboards/notebooks/documents listing commands.
func addDocumentListFlags(cmd *cobra.Command, includeType bool) {
	if includeType {
		cmd.Flags().String("type", "", "Filter by document type (e.g. dashboard, notebook, launchpad)")
	}
	cmd.Flags().String("name", "", "Filter by name (partial match, case-insensitive)")
	cmd.Flags().Bool("mine", false, "Show only documents owned by current user")
	cmd.Flags().String("filter", "", "Raw Document API filter expression, ANDed with the type scope (overrides --name/--mine)")
	cmd.Flags().String("sort", "", "Sort fields, comma-separated, prefix with '-' for descending (e.g. \"name,-modificationInfo.lastModifiedTime\")")
	cmd.Flags().StringSlice("add-fields", nil, "Request fields the API omits by default (e.g. originExtensionId,labels,shareInfo.isShared)")
	cmd.Flags().Bool("admin-access", false, "List documents as effective owner; requires document:documents:admin permission")
}

// Declared stable: the invocation and output contract of these commands is
// additive-only. Stable is never implied -- see AGENTS.md "Stability Tiers".
