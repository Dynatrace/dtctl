package cmd

import (
	"fmt"
	"slices"
	"strings"

	"github.com/spf13/cobra"

	"github.com/Dynatrace/dtctl/pkg/output"
	"github.com/Dynatrace/dtctl/pkg/resources/document"
	"github.com/Dynatrace/dtctl/pkg/safety"
	"github.com/Dynatrace/dtctl/pkg/stability"
)

// shareCmd represents the share command
var shareCmd = newShareCmd()

func newShareCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "share",
		Short: "Share documents with users or groups",
		Long:  `Share documents (dashboards, notebooks) with specific users or groups.`,
	}
	stability.MarkStable(c)
	return c
}

// shareDocumentCmd shares a document with users/groups
var shareDocumentCmd = newShareDocumentCmd()

func newShareDocumentCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "document <document-id> --user <user-id> | --group <group-id>",
		Short: "Share a document with users or groups",
		Long: `Share a document with specific users or groups.

Examples:
  # Share a document with a user (read access)
  dtctl share document my-dashboard-id --user user-sso-id

  # Share with read-write access
  dtctl share document my-dashboard-id --user user-sso-id --access read-write

  # Share with multiple users
  dtctl share document my-dashboard-id --user user1 --user user2

  # Share with a group
  dtctl share document my-dashboard-id --group group-sso-id

  # Share with both users and groups
  dtctl share document my-dashboard-id --user user1 --group group1

  # Share without notifying the recipients (sharing with a group notifies
  # every member of the group by default)
  dtctl share document my-dashboard-id --group group-sso-id --no-notify
`,
		Aliases: []string{"doc"},
		Args:    cobra.ExactArgs(1),
		RunE:    runShareDocument,
	}
	stability.MarkStable(c)
	addShareFlags(c)
	return c
}

// unshareCmd represents the unshare command
var unshareCmd = newUnshareCmd()

func newUnshareCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "unshare",
		Short: "Remove sharing from documents",
		Long:  `Remove sharing from documents (dashboards, notebooks).`,
	}
	stability.MarkStable(c)
	return c
}

// unshareDocumentCmd removes sharing from a document
var unshareDocumentCmd = newUnshareDocumentCmd()

func newUnshareDocumentCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "document <document-id> [--user <user-id>] [--group <group-id>] [--all]",
		Short: "Remove sharing from a document",
		Long: `Remove sharing from a document. Can remove specific users/groups or all shares.

Examples:
  # Remove a specific user from all shares
  dtctl unshare document my-dashboard-id --user user-sso-id

  # Remove a group from all shares
  dtctl unshare document my-dashboard-id --group group-sso-id

  # Remove all shares (revoke all access)
  dtctl unshare document my-dashboard-id --all

  # Remove only read shares
  dtctl unshare document my-dashboard-id --all --access read
`,
		Aliases: []string{"doc"},
		Args:    cobra.ExactArgs(1),
		RunE:    runUnshareDocument,
	}
	stability.MarkStable(c)
	addUnshareFlags(c)
	return c
}

// shareNotebookCmd is an alias for sharing notebooks
var shareNotebookCmd = newShareNotebookCmd()

func newShareNotebookCmd() *cobra.Command {
	c := &cobra.Command{
		Use:     "notebook <notebook-id> --user <user-id> | --group <group-id>",
		Short:   "Share a notebook with users or groups",
		Aliases: []string{"nb"},
		Args:    cobra.ExactArgs(1),
		RunE:    runShareDocument,
	}
	stability.MarkStable(c)
	addShareFlags(c)
	return c
}

// shareDashboardCmd is an alias for sharing dashboards
var shareDashboardCmd = newShareDashboardCmd()

func newShareDashboardCmd() *cobra.Command {
	c := &cobra.Command{
		Use:     "dashboard <dashboard-id> --user <user-id> | --group <group-id>",
		Short:   "Share a dashboard with users or groups",
		Aliases: []string{"db"},
		Args:    cobra.ExactArgs(1),
		RunE:    runShareDocument,
	}
	stability.MarkStable(c)
	addShareFlags(c)
	return c
}

// unshareNotebookCmd is an alias for unsharing notebooks
var unshareNotebookCmd = newUnshareNotebookCmd()

func newUnshareNotebookCmd() *cobra.Command {
	c := &cobra.Command{
		Use:     "notebook <notebook-id> [--user <user-id>] [--group <group-id>] [--all]",
		Short:   "Remove sharing from a notebook",
		Aliases: []string{"nb"},
		Args:    cobra.ExactArgs(1),
		RunE:    runUnshareDocument,
	}
	stability.MarkStable(c)
	addUnshareFlags(c)
	return c
}

// unshareDashboardCmd is an alias for unsharing dashboards
var unshareDashboardCmd = newUnshareDashboardCmd()

func newUnshareDashboardCmd() *cobra.Command {
	c := &cobra.Command{
		Use:     "dashboard <dashboard-id> [--user <user-id>] [--group <group-id>] [--all]",
		Short:   "Remove sharing from a dashboard",
		Aliases: []string{"db"},
		Args:    cobra.ExactArgs(1),
		RunE:    runUnshareDocument,
	}
	stability.MarkStable(c)
	addUnshareFlags(c)
	return c
}

func init() {
	rootCmd.AddCommand(shareCmd)
	rootCmd.AddCommand(unshareCmd)

	// Share subcommands
	shareCmd.AddCommand(shareDocumentCmd)
	shareCmd.AddCommand(shareNotebookCmd)
	shareCmd.AddCommand(shareDashboardCmd)

	// Unshare subcommands
	unshareCmd.AddCommand(unshareDocumentCmd)
	unshareCmd.AddCommand(unshareNotebookCmd)
	unshareCmd.AddCommand(unshareDashboardCmd)
}

// formatRecipients formats recipients for display
//
//nolint:unused // Reserved for future share features
func formatRecipients(recipients []document.SsoEntity) string {
	var parts []string
	for _, r := range recipients {
		parts = append(parts, fmt.Sprintf("%s:%s", r.Type, r.ID))
	}
	return strings.Join(parts, ", ")
}

// Declared stable: the invocation and output contract of these commands is
// additive-only. Stable is never implied -- see AGENTS.md "Stability Tiers".

// addShareFlags registers the flags every share subcommand takes.
func addShareFlags(c *cobra.Command) {
	c.Flags().StringArray("user", []string{}, "SSO user ID to share with (can be specified multiple times)")
	c.Flags().StringArray("group", []string{}, "SSO group ID to share with (can be specified multiple times)")
	c.Flags().String("access", "read", "access level: 'read' or 'read-write'")
	c.Flags().Bool("no-notify", false, "do not notify recipients of the share (a group recipient notifies every member)")
	stability.MarkFlag(c, "no-notify", stability.Experimental, "0.40.0")
	// A blank recipient would be sent to the API as an empty SSO ID.
	rejectEmptyFlag(c, "user")
	rejectEmptyFlag(c, "group")
}

// addUnshareFlags registers the flags every unshare subcommand takes.
func addUnshareFlags(c *cobra.Command) {
	c.Flags().StringArray("user", []string{}, "SSO user ID to remove (can be specified multiple times)")
	c.Flags().StringArray("group", []string{}, "SSO group ID to remove (can be specified multiple times)")
	c.Flags().Bool("all", false, "remove all shares")
	c.Flags().String("access", "", "filter by access level: 'read' or 'read-write'")
	rejectEmptyFlag(c, "user")
	rejectEmptyFlag(c, "group")
}

// runShareDocument is the shared body of share document|notebook|dashboard.
// A named function rather than the document command's RunE value, so each
// alias on a per-invocation tree runs its own copy, not the singleton's
// (possibly wrapped) one.
func runShareDocument(cmd *cobra.Command, args []string) error {
	documentID := args[0]
	users, _ := cmd.Flags().GetStringArray("user")
	groups, _ := cmd.Flags().GetStringArray("group")
	access, _ := cmd.Flags().GetString("access")
	noNotify, _ := cmd.Flags().GetBool("no-notify")

	if len(users) == 0 && len(groups) == 0 {
		return fmt.Errorf("at least one --user or --group is required")
	}

	// Validate access level
	if access != "read" && access != "read-write" {
		return fmt.Errorf("invalid access level %q, must be 'read' or 'read-write'", access)
	}

	// Build recipients list
	var recipients []document.SsoEntity
	for _, u := range users {
		recipients = append(recipients, document.SsoEntity{ID: u, Type: "user"})
	}
	for _, g := range groups {
		recipients = append(recipients, document.SsoEntity{ID: g, Type: "group"})
	}

	if dryRun(cmdContext(cmd)) {
		report := newDryRunReport(cmd).
			Linef("Dry run: would share document %q with %d recipient(s) (%s access)",
				documentID, len(recipients), access).
			Detail("document", "%s", documentID).
			Detail("access", "%s", access).
			Detail("recipients", "%d", len(recipients)).
			Detail("notify", "%t", !noNotify)
		for _, r := range recipients {
			report.Linef("  - %s: %s", r.Type, r.ID)
		}
		return report.Print()
	}

	cfg, c, err := setupClient(cmdContext(cmd))
	if err != nil {
		return err
	}

	handler := document.NewHandler(c)

	// Get document metadata for ownership check
	metadata, err := handler.GetMetadata(documentID)
	if err != nil {
		return err
	}

	// Safety check with actual ownership - sharing modifies document permissions
	currentUserID, _ := c.CurrentUserID()
	ownership := safety.DetermineOwnership(metadata.Owner, currentUserID)
	if err := checkSafety(cmdContext(cmd), cfg, safety.OperationUpdate, ownership); err != nil {
		return err
	}

	// Check if a share already exists for this document with the same access level
	shares, err := handler.ListDirectShares(documentID)
	if err != nil {
		return fmt.Errorf("failed to check existing shares: %w", err)
	}

	var existingShare *document.DirectShare
	for _, s := range shares.Shares {
		if s.ExactAccess(access) {
			existingShare = &s
			break
		}
	}

	if existingShare != nil {
		// Add recipients to existing share
		err = handler.AddDirectShareRecipientsWithOptions(existingShare.ID, recipients,
			document.AddDirectShareRecipientsOptions{SuppressNotification: noNotify})
		if err != nil {
			return fmt.Errorf("failed to add recipients to share: %w", err)
		}
		output.FprintSuccess(currentStderr(cmdContext(cmd)), "Added %d recipient(s) to existing %s share for document %q",
			len(recipients), access, documentID)
	} else {
		// Create new share
		share, err := handler.CreateDirectShare(document.CreateDirectShareRequest{
			DocumentID:           documentID,
			Access:               access,
			Recipients:           recipients,
			SuppressNotification: noNotify,
		})
		if err != nil {
			return fmt.Errorf("failed to create share: %w", err)
		}
		output.FprintSuccess(currentStderr(cmdContext(cmd)), "Created %s share (%s) for document %q with %d recipient(s)",
			access, share.ID, documentID, len(recipients))
	}

	return nil
}

// runUnshareDocument is the shared body of unshare document|notebook|dashboard.
func runUnshareDocument(cmd *cobra.Command, args []string) error {
	documentID := args[0]
	users, _ := cmd.Flags().GetStringArray("user")
	groups, _ := cmd.Flags().GetStringArray("group")
	all, _ := cmd.Flags().GetBool("all")
	access, _ := cmd.Flags().GetString("access")

	if !all && len(users) == 0 && len(groups) == 0 {
		return fmt.Errorf("specify --user, --group, or --all")
	}

	if dryRun(cmdContext(cmd)) {
		report := newDryRunReport(cmd).Detail("document", "%s", documentID)
		if all {
			report.Linef("Dry run: would remove all shares from document %q", documentID)
		} else {
			report.
				Linef("Dry run: would remove %d user(s) and %d group(s) from document %q shares",
					len(users), len(groups), documentID).
				Detail("users", "%d", len(users)).
				Detail("groups", "%d", len(groups))
		}
		return report.Print()
	}

	cfg, c, err := setupClient(cmdContext(cmd))
	if err != nil {
		return err
	}

	handler := document.NewHandler(c)

	// Get document metadata for ownership check
	metadata, err := handler.GetMetadata(documentID)
	if err != nil {
		return err
	}

	// Safety check with actual ownership - unsharing modifies document permissions
	currentUserID, _ := c.CurrentUserID()
	ownership := safety.DetermineOwnership(metadata.Owner, currentUserID)

	// New: may still change shape (output fields, fallback order) -- see
	// AGENTS.md "Stability Tiers".
	if err := checkSafety(cmdContext(cmd), cfg, safety.OperationUpdate, ownership); err != nil {
		return err
	}

	// Get existing shares for this document
	shares, err := handler.ListDirectShares(documentID)
	if err != nil {
		return fmt.Errorf("failed to list shares: %w", err)
	}

	if len(shares.Shares) == 0 {
		fmt.Fprintf(currentStdout(cmdContext(cmd)), "No shares found for document %q\n", documentID)
		return nil
	}

	if all {
		// Delete all shares (optionally filtered by access level)
		deleted := 0
		for _, share := range shares.Shares {
			if access != "" && !share.ExactAccess(access) {
				continue
			}
			if err := handler.DeleteDirectShare(share.ID); err != nil {
				return fmt.Errorf("failed to delete share %s: %w", share.ID, err)
			}
			deleted++
		}
		output.FprintSuccess(currentStderr(cmdContext(cmd)), "Deleted %d share(s) from document %q", deleted, documentID)
	} else {
		// Remove specific recipients from all shares
		recipientIDs := slices.Concat(users, groups)
		removed := 0
		for _, share := range shares.Shares {
			if access != "" && !share.ExactAccess(access) {
				continue
			}
			if err := handler.RemoveDirectShareRecipients(share.ID, recipientIDs); err != nil {
				// Log but continue - recipient might not be in this share
				if verbosity(cmdContext(cmd)) > 0 {
					output.FprintInfo(currentStderr(cmdContext(cmd)), "Note: could not remove from share %s: %v", share.ID, err)
				}
				continue
			}
			removed++
		}
		output.FprintSuccess(currentStderr(cmdContext(cmd)), "Removed %d recipient(s) from %d share(s) of document %q",
			len(recipientIDs), removed, documentID)
	}

	return nil
}
