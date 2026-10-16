package cmd

import (
	"context"
	"strings"

	"github.com/spf13/cobra"

	"github.com/dynatrace-oss/dtctl/pkg/output"
	"github.com/dynatrace-oss/dtctl/pkg/resources/document"
	"github.com/dynatrace-oss/dtctl/pkg/resources/resolver"
	"github.com/dynatrace-oss/dtctl/pkg/stability"
)

// printDocumentDetails prints common document metadata using bold labels.
func printDocumentDetails(ctx context.Context, metadata *document.DocumentMetadata) {
	const w = 13
	output.FprintDescribeKV(currentStdout(ctx), "ID:", w, "%s", metadata.ID)
	output.FprintDescribeKV(currentStdout(ctx), "Name:", w, "%s", metadata.Name)
	output.FprintDescribeKV(currentStdout(ctx), "Type:", w, "%s", metadata.Type)
	if metadata.Description != "" {
		output.FprintDescribeKV(currentStdout(ctx), "Description:", w, "%s", metadata.Description)
	}
	output.FprintDescribeKV(currentStdout(ctx), "Version:", w, "%d", metadata.Version)
	output.FprintDescribeKV(currentStdout(ctx), "Owner:", w, "%s", metadata.Owner)
	output.FprintDescribeKV(currentStdout(ctx), "Private:", w, "%v", metadata.IsPrivate)
	output.FprintDescribeKV(currentStdout(ctx), "Created:", w, "%s (by %s)",
		metadata.ModificationInfo.CreatedTime.Format("2006-01-02 15:04:05"),
		metadata.ModificationInfo.CreatedBy)
	output.FprintDescribeKV(currentStdout(ctx), "Modified:", w, "%s (by %s)",
		metadata.ModificationInfo.LastModifiedTime.Format("2006-01-02 15:04:05"),
		metadata.ModificationInfo.LastModifiedBy)
	if len(metadata.Access) > 0 {
		output.FprintDescribeKV(currentStdout(ctx), "Access:", w, "%s", strings.Join(metadata.Access, ", "))
	}
}

// printDocumentOrFormat prints document details as table or uses the printer for other formats.
func printDocumentOrFormat(ctx context.Context, metadata *document.DocumentMetadata, resource string) error {
	if outputFormat(ctx) == "table" {
		printDocumentDetails(ctx, metadata)
		return nil
	}

	printer := newPrinterCtx(ctx)
	enrichAgent(printer, "describe", resource)
	return printer.Print(metadata)
}

// describeDashboardCmd shows detailed info about a dashboard
var describeDashboardCmd = newDescribeDashboardCmd()

func newDescribeDashboardCmd() *cobra.Command {
	c := &cobra.Command{
		Use:     "dashboard <dashboard-id-or-name>",
		Aliases: []string{"dash", "db"},
		Short:   "Show details of a dashboard",
		Long: `Show detailed information about a dashboard including metadata and sharing info.

Examples:
  # Describe a dashboard by ID
  dtctl describe dashboard <dashboard-id>
  dtctl describe dash <dashboard-id>

  # Describe a dashboard by name
  dtctl describe dashboard "Production Dashboard"
`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			identifier := args[0]

			_, c, err := setupClient(cmdContext(cmd))
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

			// Get full metadata
			metadata, err := handler.GetMetadata(dashboardID)
			if err != nil {
				return err
			}

			if err := requireDocumentType(metadata, "dashboard", dashboardID); err != nil {
				return err
			}

			return printDocumentOrFormat(cmdContext(cmd), metadata, "dashboard")
		},
	}
	stability.MarkStable(c)
	return c
}

// describeNotebookCmd shows detailed info about a notebook
var describeNotebookCmd = newDescribeNotebookCmd()

func newDescribeNotebookCmd() *cobra.Command {
	c := &cobra.Command{
		Use:     "notebook <notebook-id-or-name>",
		Aliases: []string{"nb"},
		Short:   "Show details of a notebook",
		Long: `Show detailed information about a notebook including metadata and sharing info.

Examples:
  # Describe a notebook by ID
  dtctl describe notebook <notebook-id>
  dtctl describe nb <notebook-id>

  # Describe a notebook by name
  dtctl describe notebook "Analysis Notebook"
`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			identifier := args[0]

			_, c, err := setupClient(cmdContext(cmd))
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

			// Get full metadata
			metadata, err := handler.GetMetadata(notebookID)
			if err != nil {
				return err
			}

			if err := requireDocumentType(metadata, "notebook", notebookID); err != nil {
				return err
			}

			return printDocumentOrFormat(cmdContext(cmd), metadata, "notebook")
		},
	}
	stability.MarkStable(c)
	return c
}

// describeDocumentCmd shows detailed info about any document
var describeDocumentCmd = newDescribeDocumentCmd()

func newDescribeDocumentCmd() *cobra.Command {
	c := &cobra.Command{
		Use:     "document <document-id-or-name>",
		Aliases: []string{"doc"},
		Short:   "Show details of a document",
		Long: `Show detailed information about a document of any type.

Works for any document type (dashboard, notebook, launchpad, custom app documents, etc.).

Examples:
  # Describe a document by ID
  dtctl describe document <document-id>

  # Describe a document by name
  dtctl describe document "My Launchpad"
`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			identifier := args[0]

			_, c, err := setupClient(cmdContext(cmd))
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

			// Get full metadata
			metadata, err := handler.GetMetadata(documentID)
			if err != nil {
				return err
			}

			return printDocumentOrFormat(cmdContext(cmd), metadata, "document")
		},
	}
	stability.MarkStable(c)
	return c
}

var describeTrashCmd = newDescribeTrashCmd()

func newDescribeTrashCmd() *cobra.Command {
	c := &cobra.Command{
		Use:     "trash <document-id>",
		Aliases: []string{"deleted"},
		Short:   "Show details of a trashed document",
		Long: `Show detailed information about a trashed document.

Examples:
  # Describe a trashed document by ID
  dtctl describe trash <document-id>
`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			documentID := args[0]

			_, c, printer, err := setup(cmdContext(cmd))
			if err != nil {
				return err
			}

			handler := document.NewTrashHandler(c)

			// Get trashed document details
			doc, err := handler.Get(documentID)
			if err != nil {
				return err
			}

			// For table output, show detailed human-readable information
			if outputFormat(cmdContext(cmd)) == "table" {
				const w = 18
				output.FprintDescribeKV(currentStdout(cmdContext(cmd)), "ID:", w, "%s", doc.ID)
				output.FprintDescribeKV(currentStdout(cmdContext(cmd)), "Name:", w, "%s", doc.Name)
				output.FprintDescribeKV(currentStdout(cmdContext(cmd)), "Type:", w, "%s", doc.Type)
				output.FprintDescribeKV(currentStdout(cmdContext(cmd)), "Version:", w, "%d", doc.Version)
				output.FprintDescribeKV(currentStdout(cmdContext(cmd)), "Owner:", w, "%s", doc.Owner)
				output.FprintDescribeKV(currentStdout(cmdContext(cmd)), "Deleted By:", w, "%s", doc.DeletedBy)
				output.FprintDescribeKV(currentStdout(cmdContext(cmd)), "Deleted At:", w, "%s", doc.DeletedAt.Format("2006-01-02 15:04:05"))

				// Show modification info if available
				if !doc.ModificationInfo.LastModifiedTime.IsZero() {
					output.FprintDescribeKV(currentStdout(cmdContext(cmd)), "Last Modified:", w, "%s", doc.ModificationInfo.LastModifiedTime.Format("2006-01-02 15:04:05"))
				}
				if doc.ModificationInfo.LastModifiedBy != "" {
					output.FprintDescribeKV(currentStdout(cmdContext(cmd)), "Last Modified By:", w, "%s", doc.ModificationInfo.LastModifiedBy)
				}

				return nil
			}

			// For other formats, use standard printer
			enrichAgent(printer, "describe", "trash")
			return printer.Print(doc)
		},
	}
	stability.MarkStable(c)
	return c
}

// Declared stable: the invocation and output contract of these commands is
// additive-only. Stable is never implied -- see AGENTS.md "Stability Tiers".
func init() {
}
