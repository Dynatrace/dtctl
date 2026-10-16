package cmd

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/dynatrace-oss/dtctl/pkg/output"
	"github.com/dynatrace-oss/dtctl/pkg/resources/document"
	"github.com/dynatrace-oss/dtctl/pkg/resources/resolver"
	"github.com/dynatrace-oss/dtctl/pkg/safety"
	"github.com/dynatrace-oss/dtctl/pkg/stability"
	"github.com/dynatrace-oss/dtctl/pkg/util/format"
)

// editUpdateRequest builds the update request for an interactive edit. Snapshot
// creation is opt-in: without --create-snapshot the edit overwrites the document
// in place, matching the behavior of every other dtctl write path.
func editUpdateRequest(cmd *cobra.Command, content []byte) document.UpdateRequest {
	createSnapshot, _ := cmd.Flags().GetBool("create-snapshot")
	snapshotDescription, _ := cmd.Flags().GetString("snapshot-description")
	return document.UpdateRequest{
		Content:             content,
		ContentType:         "application/json",
		CreateSnapshot:      createSnapshot,
		SnapshotDescription: snapshotDescription,
	}
}

// editDashboardCmd edits a dashboard
var editDashboardCmd = newEditDashboardCmd()

func newEditDashboardCmd() *cobra.Command {
	c := &cobra.Command{
		Use:     "dashboard <dashboard-id-or-name>",
		Aliases: []string{"dashboards", "dash", "db"},
		Short:   "Edit a dashboard",
		Long: `Edit a dashboard by opening it in your default editor.

The dashboard will be fetched, opened in your editor (defined by EDITOR env var,
defaults to vim), and updated when you save and close the editor.

By default, resources are edited in YAML format for better readability.
Use --format=json to edit in JSON format.

Saving overwrites the current content. Pass --create-snapshot to keep the
pre-edit state as a snapshot, recoverable via 'dtctl history' and 'dtctl restore'.

Examples:
  # Edit a dashboard in YAML (default)
  dtctl edit dashboard <dashboard-id>
  dtctl edit dashboard "Production Dashboard"

  # Edit a dashboard in JSON
  dtctl edit dashboard <dashboard-id> --format=json

  # Keep the pre-edit content as a snapshot
  dtctl edit dashboard <dashboard-id> --create-snapshot
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

			// Get the dashboard with content
			doc, err := handler.Get(dashboardID)
			if err != nil {
				return err
			}

			// Get metadata separately for ownership check - the multipart response
			// from Get() may not include the owner field
			metadata, err := handler.GetMetadata(dashboardID)
			if err != nil {
				return err
			}

			if err := requireDocumentType(metadata, "dashboard", dashboardID); err != nil {
				return err
			}
			// Determine ownership for safety check
			currentUserID, _ := c.CurrentUserID() // Ignore error - will be empty string
			ownership := safety.DetermineOwnership(metadata.Owner, currentUserID)

			// Safety check with actual ownership
			if err := checkSafety(cmdContext(cmd), cfg, safety.OperationUpdate, ownership); err != nil {
				return err
			}

			// Get format preference
			editFormat, _ := cmd.Flags().GetString("format")
			var editData []byte
			var fileExt string

			if editFormat == "yaml" {
				// Convert JSON to YAML for editing
				editData, err = format.JSONToYAML(doc.Content)
				if err != nil {
					return fmt.Errorf("failed to convert to YAML: %w", err)
				}
				fileExt = "*.yaml"
			} else {
				// Pretty print JSON for editing
				editData, err = format.PrettyJSON(doc.Content)
				if err != nil {
					return fmt.Errorf("failed to format JSON: %w", err)
				}
				fileExt = "*.json"
			}

			// Create a temp file with appropriate extension
			tmpfile, err := os.CreateTemp("", "dtctl-dashboard-"+fileExt)
			if err != nil {
				return fmt.Errorf("failed to create temp file: %w", err)
			}
			defer func() {
				if err := os.Remove(tmpfile.Name()); err != nil {
					fmt.Fprintf(currentStderr(cmdContext(cmd)), "failed to remove temp file: %v\n", err)
				}
			}()

			if _, err := tmpfile.Write(editData); err != nil {
				return fmt.Errorf("failed to write temp file: %w", err)
			}
			if err := tmpfile.Close(); err != nil {
				return fmt.Errorf("failed to close temp file: %w", err)
			}

			// Open the editor (single gateway; enforces the Editor capability)
			if err := launchEditor(cmdContext(cmd), cfg.Preferences.Editor, tmpfile.Name()); err != nil {
				return err
			}

			// Read the edited file
			editedData, err := os.ReadFile(tmpfile.Name())
			if err != nil {
				return fmt.Errorf("failed to read edited file: %w", err)
			}

			// Convert edited data to JSON (auto-detect format)
			jsonData, err := format.ValidateAndConvert(editedData)
			if err != nil {
				return fmt.Errorf("invalid format: %w", err)
			}

			// Check if anything changed
			var originalCompact, editedCompact bytes.Buffer
			if err := json.Compact(&originalCompact, doc.Content); err != nil {
				return fmt.Errorf("failed to compact original JSON: %w", err)
			}
			if err := json.Compact(&editedCompact, jsonData); err != nil {
				return fmt.Errorf("failed to compact edited JSON: %w", err)
			}

			if bytes.Equal(originalCompact.Bytes(), editedCompact.Bytes()) {
				fmt.Fprintln(currentStdout(cmdContext(cmd)), "Edit cancelled, no changes made.")
				return nil
			}

			// Re-fetch metadata for version (optimistic locking) - the document may have been
			// modified since we fetched it for the ownership check
			metadata, err = handler.GetMetadata(dashboardID)
			if err != nil {
				return err
			}

			// Update the dashboard
			result, err := handler.UpdateDocument(dashboardID, metadata.Version, editUpdateRequest(cmd, jsonData))
			if err != nil {
				return err
			}

			output.FprintSuccess(currentStderr(cmdContext(cmd)), "Dashboard %q updated", result.Name)
			return nil
		},
	}
	stability.MarkStable(c)
	addEditDocumentFlags(c)
	return c
}

// editNotebookCmd edits a notebook
var editNotebookCmd = newEditNotebookCmd()

func newEditNotebookCmd() *cobra.Command {
	c := &cobra.Command{
		Use:     "notebook <notebook-id-or-name>",
		Aliases: []string{"notebooks", "nb"},
		Short:   "Edit a notebook",
		Long: `Edit a notebook by opening it in your default editor.

The notebook will be fetched, opened in your editor (defined by EDITOR env var,
defaults to vim), and updated when you save and close the editor.

By default, resources are edited in YAML format for better readability.
Use --format=json to edit in JSON format.

Saving overwrites the current content. Pass --create-snapshot to keep the
pre-edit state as a snapshot, recoverable via 'dtctl history' and 'dtctl restore'.

Examples:
  # Edit a notebook in YAML (default)
  dtctl edit notebook <notebook-id>
  dtctl edit notebook "Analysis Notebook"

  # Edit a notebook in JSON
  dtctl edit notebook <notebook-id> --format=json

  # Keep the pre-edit content as a snapshot
  dtctl edit notebook <notebook-id> --create-snapshot
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

			// Get the notebook with content
			doc, err := handler.Get(notebookID)
			if err != nil {
				return err
			}

			// Get metadata separately for ownership check - the multipart response
			// from Get() may not include the owner field
			metadata, err := handler.GetMetadata(notebookID)
			if err != nil {
				return err
			}

			if err := requireDocumentType(metadata, "notebook", notebookID); err != nil {
				return err
			}
			// Determine ownership for safety check
			currentUserID, _ := c.CurrentUserID() // Ignore error - will be empty string
			ownership := safety.DetermineOwnership(metadata.Owner, currentUserID)

			// Safety check with actual ownership
			if err := checkSafety(cmdContext(cmd), cfg, safety.OperationUpdate, ownership); err != nil {
				return err
			}

			// Get format preference
			editFormat, _ := cmd.Flags().GetString("format")
			var editData []byte
			var fileExt string

			if editFormat == "yaml" {
				// Convert JSON to YAML for editing
				editData, err = format.JSONToYAML(doc.Content)
				if err != nil {
					return fmt.Errorf("failed to convert to YAML: %w", err)
				}
				fileExt = "*.yaml"
			} else {
				// Pretty print JSON for editing
				editData, err = format.PrettyJSON(doc.Content)
				if err != nil {
					return fmt.Errorf("failed to format JSON: %w", err)
				}
				fileExt = "*.json"
			}

			// Create a temp file with appropriate extension
			tmpfile, err := os.CreateTemp("", "dtctl-notebook-"+fileExt)
			if err != nil {
				return fmt.Errorf("failed to create temp file: %w", err)
			}
			defer os.Remove(tmpfile.Name())

			if _, err := tmpfile.Write(editData); err != nil {
				return fmt.Errorf("failed to write temp file: %w", err)
			}
			if err := tmpfile.Close(); err != nil {
				return fmt.Errorf("failed to close temp file: %w", err)
			}

			// Open the editor (single gateway; enforces the Editor capability)
			if err := launchEditor(cmdContext(cmd), cfg.Preferences.Editor, tmpfile.Name()); err != nil {
				return err
			}

			// Read the edited file
			editedData, err := os.ReadFile(tmpfile.Name())
			if err != nil {
				return fmt.Errorf("failed to read edited file: %w", err)
			}

			// Convert edited data to JSON (auto-detect format)
			jsonData, err := format.ValidateAndConvert(editedData)
			if err != nil {
				return fmt.Errorf("invalid format: %w", err)
			}

			// Check if anything changed
			var originalCompact, editedCompact bytes.Buffer
			if err := json.Compact(&originalCompact, doc.Content); err != nil {
				return fmt.Errorf("failed to compact original JSON: %w", err)
			}
			if err := json.Compact(&editedCompact, jsonData); err != nil {
				return fmt.Errorf("failed to compact edited JSON: %w", err)
			}

			if bytes.Equal(originalCompact.Bytes(), editedCompact.Bytes()) {
				fmt.Fprintln(currentStdout(cmdContext(cmd)), "Edit cancelled, no changes made.")
				return nil
			}

			// Re-fetch metadata for version (optimistic locking) - the document may have been
			// modified since we fetched it for the ownership check
			metadata, err = handler.GetMetadata(notebookID)
			if err != nil {
				return err
			}

			// Update the notebook
			result, err := handler.UpdateDocument(notebookID, metadata.Version, editUpdateRequest(cmd, jsonData))
			if err != nil {
				return err
			}

			output.FprintSuccess(currentStderr(cmdContext(cmd)), "Notebook %q updated", result.Name)
			return nil
		},
	}
	stability.MarkStable(c)
	addEditDocumentFlags(c)
	return c
}

// editDocumentCmd edits a document of any type
var editDocumentCmd = newEditDocumentCmd()

func newEditDocumentCmd() *cobra.Command {
	c := &cobra.Command{
		Use:     "document <document-id-or-name>",
		Aliases: []string{"documents", "doc"},
		Short:   "Edit a document",
		Long: `Edit a document of any type by opening it in your default editor.

Works for any document type (dashboard, notebook, launchpad, custom app documents, etc.).

The document will be fetched, opened in your editor (defined by EDITOR env var,
defaults to vim), and updated when you save and close the editor.

By default, resources are edited in YAML format for better readability.
Use --format=json to edit in JSON format.

Saving overwrites the current content. Pass --create-snapshot to keep the
pre-edit state as a snapshot, recoverable via 'dtctl history' and 'dtctl restore'.

Examples:
  # Edit a document in YAML (default)
  dtctl edit document <document-id>
  dtctl edit document "My Launchpad"

  # Edit a document in JSON
  dtctl edit document <document-id> --format=json

  # Keep the pre-edit content as a snapshot
  dtctl edit document <document-id> --create-snapshot
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

			// Get the document with content
			doc, err := handler.Get(documentID)
			if err != nil {
				return err
			}

			// Get metadata separately for ownership check
			metadata, err := handler.GetMetadata(documentID)
			if err != nil {
				return err
			}

			// Determine ownership for safety check
			currentUserID, _ := c.CurrentUserID() // Ignore error - will be empty string
			ownership := safety.DetermineOwnership(metadata.Owner, currentUserID)

			// Safety check with actual ownership
			if err := checkSafety(cmdContext(cmd), cfg, safety.OperationUpdate, ownership); err != nil {
				return err
			}

			// Get format preference
			editFormat, _ := cmd.Flags().GetString("format")
			var editData []byte
			var fileExt string

			if editFormat == "yaml" {
				// Convert JSON to YAML for editing
				editData, err = format.JSONToYAML(doc.Content)
				if err != nil {
					return fmt.Errorf("failed to convert to YAML: %w", err)
				}
				fileExt = "*.yaml"
			} else {
				// Pretty print JSON for editing
				editData, err = format.PrettyJSON(doc.Content)
				if err != nil {
					return fmt.Errorf("failed to format JSON: %w", err)
				}
				fileExt = "*.json"
			}

			// Create a temp file with appropriate extension
			tmpfile, err := os.CreateTemp("", "dtctl-document-"+fileExt)
			if err != nil {
				return fmt.Errorf("failed to create temp file: %w", err)
			}
			defer os.Remove(tmpfile.Name())

			if _, err := tmpfile.Write(editData); err != nil {
				return fmt.Errorf("failed to write temp file: %w", err)
			}
			if err := tmpfile.Close(); err != nil {
				return fmt.Errorf("failed to close temp file: %w", err)
			}

			// Open the editor (single gateway; enforces the Editor capability)
			if err := launchEditor(cmdContext(cmd), cfg.Preferences.Editor, tmpfile.Name()); err != nil {
				return err
			}

			// Read the edited file
			editedData, err := os.ReadFile(tmpfile.Name())
			if err != nil {
				return fmt.Errorf("failed to read edited file: %w", err)
			}

			// Convert edited data to JSON (auto-detect format)
			jsonData, err := format.ValidateAndConvert(editedData)
			if err != nil {
				return fmt.Errorf("invalid format: %w", err)
			}

			// Check if anything changed
			var originalCompact, editedCompact bytes.Buffer
			if err := json.Compact(&originalCompact, doc.Content); err != nil {
				return fmt.Errorf("failed to compact original JSON: %w", err)
			}
			if err := json.Compact(&editedCompact, jsonData); err != nil {
				return fmt.Errorf("failed to compact edited JSON: %w", err)
			}

			if bytes.Equal(originalCompact.Bytes(), editedCompact.Bytes()) {
				fmt.Fprintln(currentStdout(cmdContext(cmd)), "Edit cancelled, no changes made.")
				return nil
			}

			// Re-fetch metadata for version (optimistic locking)
			metadata, err = handler.GetMetadata(documentID)
			if err != nil {
				return err
			}

			// Update the document
			result, err := handler.UpdateDocument(documentID, metadata.Version, editUpdateRequest(cmd, jsonData))
			if err != nil {
				return err
			}

			output.FprintSuccess(currentStderr(cmdContext(cmd)), "Document %q updated", result.Name)
			return nil
		},
	}
	stability.MarkStable(c)
	addEditDocumentFlags(c)
	return c
}

// Declared stable: the invocation and output contract of these commands is
// additive-only. Stable is never implied -- see AGENTS.md "Stability Tiers".

// addEditDocumentFlags registers the flags edit dashboard|notebook|document
// share. Called from each constructor, so a per-invocation tree gets them too.
func addEditDocumentFlags(c *cobra.Command) {
	c.Flags().StringP("format", "", "yaml", "edit format (yaml|json)")
	c.Flags().Bool("create-snapshot", false, "snapshot the current content before saving the edit, so the previous version stays available via 'dtctl history'/'dtctl restore'")
	c.Flags().String("snapshot-description", "", "description for the snapshot created by --create-snapshot (max 128 characters)")
	// Reject an orphaned --snapshot-description before the editor opens,
	// rather than after the user has already written their changes.
	c.PreRunE = func(cmd *cobra.Command, _ []string) error { return validateSnapshotFlags(cmd) }
}
