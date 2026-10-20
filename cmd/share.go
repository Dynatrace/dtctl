package cmd

import (
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/spf13/cobra"

	"github.com/dynatrace-oss/dtctl/pkg/config"
	"github.com/dynatrace-oss/dtctl/pkg/diagnostic"
	"github.com/dynatrace-oss/dtctl/pkg/output"
	"github.com/dynatrace-oss/dtctl/pkg/resources/document"
	"github.com/dynatrace-oss/dtctl/pkg/safety"
	"github.com/dynatrace-oss/dtctl/pkg/stability"
	"github.com/dynatrace-oss/dtctl/sdk/httpclient"
)

// environmentSince is the release that introduced share/unshare --environment.
const environmentSince = "0.44.0"

// The two ways --environment opens a document to everyone in the environment.
// The Document API keeps them apart, so each mode does (and undoes) only its
// own half; `--environment link,public` asks for both.
const (
	// environmentLink is an environment share: a #share=<id> link that anyone
	// in the environment can claim, at read or read-write access.
	environmentLink = "link"
	// environmentPublic is isPrivate=false: everyone in the environment can
	// find and read the document, with no share object involved.
	environmentPublic = "public"
)

// environmentModes is which of the two ways to open a document to the
// environment an invocation asks for.
type environmentModes struct {
	link, public bool
}

func (m environmentModes) none() bool { return !m.link && !m.public }

func (m environmentModes) String() string {
	var parts []string
	if m.link {
		parts = append(parts, environmentLink)
	}
	if m.public {
		parts = append(parts, environmentPublic)
	}
	return strings.Join(parts, ",")
}

// parseEnvironmentModes reads a list of modes from flag, comma-separated or
// repeated: "link", "public", or both. The zero value means the flag was not
// given. The flag deliberately has no default value: with one, pflag would
// read `--environment link` as a bare --environment followed by a second
// positional argument "link".
func parseEnvironmentModes(cmd *cobra.Command, flag string) (environmentModes, error) {
	var m environmentModes
	if !cmd.Flags().Changed(flag) {
		return m, nil
	}
	values, _ := cmd.Flags().GetStringSlice(flag)
	for _, v := range values {
		switch strings.TrimSpace(v) {
		case environmentLink:
			m.link = true
		case environmentPublic:
			m.public = true
		default:
			return m, fmt.Errorf("invalid --%s value %q, must be 'link', 'public', or both ('link,public')", flag, v)
		}
	}
	if m.none() {
		return m, fmt.Errorf("--%s needs a value: 'link', 'public', or both ('link,public')", flag)
	}
	return m, nil
}

// environmentShareResult is what `share --environment` reports in agent mode.
// Link and Visibility are set for the modes that were asked for.
type environmentShareResult struct {
	DocumentID string                       `json:"documentId"`
	Link       *environmentLinkResult       `json:"link,omitempty"`
	Visibility *environmentVisibilityResult `json:"visibility,omitempty"`
}

// environmentLinkResult is the environment share `--environment link` ensured.
type environmentLinkResult struct {
	ShareID string `json:"shareId"`
	Access  string `json:"access"`
	Created bool   `json:"created"`
	URL     string `json:"url"`
}

// environmentVisibilityResult is what `--environment public` did.
type environmentVisibilityResult struct {
	Public  bool `json:"public"`
	Changed bool `json:"changed"`
}

// shareCmd represents the share command
var shareCmd = newShareCmd()

func newShareCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "share",
		Short: "Share documents with users, groups, or the environment",
		Long:  `Share documents (dashboards, notebooks, launchpads, ...) with specific users or groups, or with everyone in the environment.`,
	}
	stability.MarkStable(c)
	return c
}

// shareDocumentCmd shares a document with users/groups
var shareDocumentCmd = newShareDocumentCmd()

func newShareDocumentCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "document <document-id> --user <user-id> | --group <group-id> | --environment link,public",
		Short: "Share a document with users, groups, or the environment",
		Long: `Share a document of any type with specific users or groups, or with
everyone in the environment.

--environment opens the document to everyone in the environment, in two
independent ways; pass one or both ('--environment link,public'). They are
independent of --user/--group shares too, and --environment cannot be
combined with --user or --group:

  link    Create an environment share at --access and print its link
          (https://<environment>/ui/document/v0/#share=<id>). Anyone in the
          environment who opens the link gets that access to the document.
          The document's visibility is not changed. A document has at most one
          link per access level: re-running reuses it, and a read and a
          read-write link can exist side by side.
  public  Mark the document public (isPrivate=false): everyone in the
          environment can find and read it. No share is created, and --access
          applies only to link.

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

  # Create a link anyone in the environment can open to read a launchpad
  # (or any other document)
  dtctl share document my-launchpad-id --environment link

  # Create a link that grants read-write access
  dtctl share document my-launchpad-id --environment link --access read-write

  # Make a document readable by everyone in the environment
  dtctl share document my-launchpad-id --environment public

  # Both: make it public and print a link that grants write access
  dtctl share document my-launchpad-id --environment link,public --access read-write
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
		Long:  `Remove sharing from documents (dashboards, notebooks, launchpads, ...).`,
	}
	stability.MarkStable(c)
	return c
}

// unshareDocumentCmd removes sharing from a document
var unshareDocumentCmd = newUnshareDocumentCmd()

func newUnshareDocumentCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "document <document-id> [--user <user-id>] [--group <group-id>] [--all] [--environment link,public]",
		Short: "Remove sharing from a document",
		Long: `Remove sharing from a document. Can remove specific users/groups, all
user/group shares, or what --environment link,public on share created.

--all removes user and group shares only. --environment undoes one or both of
the two ways a document is opened to the environment, and can be combined
with the other flags:

  link    Delete the document's environment share(s), so their links stop
          working. With --access, only the share at exactly that level is
          deleted. The document's visibility is not changed.
  public  Mark the document private again (isPrivate=true). Environment
          shares are not deleted.

Examples:
  # Remove a specific user from all shares
  dtctl unshare document my-dashboard-id --user user-sso-id

  # Remove a group from all shares
  dtctl unshare document my-dashboard-id --group group-sso-id

  # Remove all shares (revoke all access)
  dtctl unshare document my-dashboard-id --all

  # Remove only read shares
  dtctl unshare document my-dashboard-id --all --access read

  # Delete the document's environment share links
  dtctl unshare document my-launchpad-id --environment link

  # Take back write access but keep a read-only link working
  dtctl unshare document my-launchpad-id --environment link --access read-write

  # Make a public document private again
  dtctl unshare document my-launchpad-id --environment public

  # Close the document to the environment entirely
  dtctl unshare document my-launchpad-id --environment link,public

  # Remove every user, group, and environment share
  dtctl unshare document my-launchpad-id --all --environment link
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
		Use:     "notebook <notebook-id> --user <user-id> | --group <group-id> | --environment link,public",
		Short:   "Share a notebook with users, groups, or the environment",
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
		Use:     "dashboard <dashboard-id> --user <user-id> | --group <group-id> | --environment link,public",
		Short:   "Share a dashboard with users, groups, or the environment",
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
		Use:     "notebook <notebook-id> [--user <user-id>] [--group <group-id>] [--all] [--environment link,public]",
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
		Use:     "dashboard <dashboard-id> [--user <user-id>] [--group <group-id>] [--all] [--environment link,public]",
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
	c.Flags().StringSlice("environment", nil, "open the document to everyone in the environment: 'link' (an environment share link, at --access), 'public' (isPrivate=false, read access), or both ('link,public'); cannot be combined with --user/--group")
	stability.MarkFlag(c, "no-notify", stability.Experimental, "0.40.0")
	stability.MarkFlag(c, "environment", stability.Experimental, environmentSince)
	// A blank recipient would be sent to the API as an empty SSO ID.
	rejectEmptyFlag(c, "user")
	rejectEmptyFlag(c, "group")
}

// addUnshareFlags registers the flags every unshare subcommand takes.
func addUnshareFlags(c *cobra.Command) {
	c.Flags().StringArray("user", []string{}, "SSO user ID to remove (can be specified multiple times)")
	c.Flags().StringArray("group", []string{}, "SSO group ID to remove (can be specified multiple times)")
	c.Flags().Bool("all", false, "remove all user and group shares (add --environment link to delete the environment shares too)")
	c.Flags().String("access", "", "filter by access level: 'read' or 'read-write'")
	c.Flags().StringSlice("environment", nil, "'link' deletes the environment share(s) (filtered by --access), 'public' makes the document private again; 'link,public' does both")
	stability.MarkFlag(c, "environment", stability.Experimental, environmentSince)
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
	environment, err := parseEnvironmentModes(cmd, "environment")
	if err != nil {
		return err
	}

	if !environment.none() {
		if len(users) > 0 || len(groups) > 0 {
			return fmt.Errorf("--environment cannot be combined with --user or --group")
		}
		if noNotify {
			return fmt.Errorf("--no-notify applies to --user/--group shares only, not to --environment")
		}
		if !environment.link && cmd.Flags().Changed("access") {
			return fmt.Errorf("--access does not apply to --environment public, which grants read access; " +
				"use --environment link --access read-write for a link with write access")
		}
	} else if len(users) == 0 && len(groups) == 0 {
		return fmt.Errorf("at least one --user, --group, or --environment link,public is required")
	}

	// Validate access level
	if access != "read" && access != "read-write" {
		return fmt.Errorf("invalid access level %q, must be 'read' or 'read-write'", access)
	}

	if !environment.none() && dryRun(cmdContext(cmd)) {
		report := newDryRunReport(cmd).
			Detail("document", "%s", documentID).
			Detail("environment", "%s", environment)
		if environment.link {
			report.
				Linef("Dry run: would ensure a %s environment share link for document %q", access, documentID).
				Detail("access", "%s", access)
		}
		if environment.public {
			report.Linef("Dry run: would make document %q public to everyone in the environment", documentID)
		}
		return report.Print()
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

	if !environment.none() {
		return shareWithEnvironment(cmd, cfg, handler, c.BaseURL(), metadata, access, environment)
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
	direct := all || len(users) > 0 || len(groups) > 0
	environment, err := parseEnvironmentModes(cmd, "environment")
	if err != nil {
		return err
	}

	if !direct && environment.none() {
		return fmt.Errorf("specify --user, --group, --all, or --environment link,public")
	}
	// An unrecognized level would otherwise be matched as 'read'.
	if access != "" && access != "read" && access != "read-write" {
		return fmt.Errorf("invalid access level %q, must be 'read' or 'read-write'", access)
	}
	// --access filters shares, and --environment public removes none.
	if !environment.link && environment.public && access != "" && !direct {
		return fmt.Errorf("--access does not apply to --environment public, which deletes no share")
	}

	if dryRun(cmdContext(cmd)) {
		report := newDryRunReport(cmd).Detail("document", "%s", documentID)
		if all {
			report.Linef("Dry run: would remove all user and group shares from document %q", documentID)
		} else if direct {
			report.
				Linef("Dry run: would remove %d user(s) and %d group(s) from document %q shares",
					len(users), len(groups), documentID).
				Detail("users", "%d", len(users)).
				Detail("groups", "%d", len(groups))
		}
		if !environment.none() {
			report.Detail("environment", "%s", environment)
		}
		if environment.public {
			report.Linef("Dry run: would make document %q private", documentID)
		}
		if environment.link {
			which := "all environment shares"
			if access != "" {
				which = "the " + access + " environment share"
			}
			report.Linef("Dry run: would delete %s of document %q", which, documentID)
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

	stdout, stderr := currentStdout(cmdContext(cmd)), currentStderr(cmdContext(cmd))

	// Private first: isPrivate=false lets everyone read the document, a link
	// only those who claim it, so a failure partway leaves the narrower of the
	// two.
	if environment.public {
		if err := handler.SetPrivate(documentID, true); err != nil {
			return fmt.Errorf("failed to make document %q private: %w", documentID, err)
		}
		if metadata.IsPrivate {
			fmt.Fprintf(stdout, "Document %q is already private\n", documentID)
		} else {
			output.FprintSuccess(stderr, "Document %q is private again", documentID)
		}
	}
	if environment.link {
		deleted, err := handler.RemoveEnvironmentLinks(documentID, access)
		if err != nil {
			return environmentShareScopeError(err, cfg,
				fmt.Sprintf("delete the environment share of document %q", documentID), environmentShareDeleteScopes)
		}
		switch {
		case deleted > 0:
			output.FprintSuccess(stderr, "Deleted %d environment share(s) from document %q; links to them no longer work",
				deleted, documentID)
		case access != "":
			fmt.Fprintf(stdout, "No %s environment share found for document %q, nothing changed\n", access, documentID)
		default:
			fmt.Fprintf(stdout, "Document %q has no environment share, nothing changed\n", documentID)
		}
	}
	if !direct {
		return nil
	}

	// Get existing shares for this document
	shares, err := handler.ListDirectShares(documentID)
	if err != nil {
		return fmt.Errorf("failed to list shares: %w", err)
	}

	if len(shares.Shares) == 0 {
		fmt.Fprintf(stdout, "No user or group shares found for document %q\n", documentID)
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

// shareWithEnvironment opens the document to the environment in the given
// modes: the link first, so a failure to make the document public leaves the
// narrower of the two in place. The link goes to stdout as a plain line, so it
// can be captured, or into the result in agent mode.
func shareWithEnvironment(cmd *cobra.Command, cfg *config.Config, handler *document.Handler, baseURL string,
	metadata *document.DocumentMetadata, access string, modes environmentModes) error {
	ctx := cmdContext(cmd)
	documentID := metadata.ID
	result := environmentShareResult{DocumentID: documentID}

	if modes.link {
		res, err := handler.EnsureEnvironmentLink(documentID, access)
		if err != nil {
			return environmentShareScopeError(err, cfg,
				fmt.Sprintf("create an environment share for document %q", documentID), environmentShareWriteScopes)
		}
		result.Link = &environmentLinkResult{
			ShareID: res.Share.ID,
			Access:  res.Share.Level(),
			Created: res.Created,
			URL:     document.ShareURL(baseURL, res.Share.ID),
		}
	}

	if modes.public {
		if err := handler.SetPrivate(documentID, false); err != nil {
			if result.Link != nil {
				return fmt.Errorf("created the environment share link %s, but failed to make document %q public: %w",
					result.Link.URL, documentID, err)
			}
			return fmt.Errorf("failed to make document %q public: %w", documentID, err)
		}
		result.Visibility = &environmentVisibilityResult{Public: true, Changed: metadata.IsPrivate}
	}

	printer := newPrinterCtx(ctx)
	if ap := enrichAgent(printer, "share", cmd.Name()); ap != nil {
		return printer.Print(result)
	}
	stderr := currentStderr(ctx)
	if l := result.Link; l != nil {
		if l.Created {
			output.FprintSuccess(stderr, "Created %s environment share %s for document %q", l.Access, l.ShareID, documentID)
		} else {
			output.FprintSuccess(stderr, "Document %q already has a %s environment share (%s)", documentID, l.Access, l.ShareID)
		}
	}
	if v := result.Visibility; v != nil {
		if v.Changed {
			output.FprintSuccess(stderr, "Document %q is now public: everyone in the environment can find and read it", documentID)
		} else {
			output.FprintSuccess(stderr, "Document %q is already public", documentID)
		}
	}
	if result.Link != nil {
		fmt.Fprintln(currentStdout(ctx), result.Link.URL)
	}
	return nil
}

// environmentShareWriteScopes are what `share --environment link` needs on
// top of the document scopes.
var environmentShareWriteScopes = []string{"document:environment-shares:read", "document:environment-shares:write"}

// environmentShareDeleteScopes are what `unshare --environment link` needs on
// top of the document scopes. OAuth sessions from before dtctl requested
// :delete at login do not carry it.
var environmentShareDeleteScopes = []string{"document:environment-shares:read", "document:environment-shares:delete"}

// environmentShareScopeError turns a 403 from the environment-shares API into
// an error that names the scopes and how to get them. Any other error is
// wrapped with the operation.
func environmentShareScopeError(err error, cfg *config.Config, operation string, scopes []string) error {
	var apiErr *httpclient.APIError
	if !errors.As(err, &apiErr) || apiErr.StatusCode != 403 {
		return fmt.Errorf("failed to %s: %w", operation, err)
	}
	return &diagnostic.Error{
		Operation:  operation,
		StatusCode: apiErr.StatusCode,
		Message:    "access denied: " + err.Error(),
		Err:        err,
		Suggestions: append(loginScopeAdvice(cfg, scopes),
			"Platform token: create it with "+strings.Join(scopes, ", "),
			"Check the scope gate: re-run the same command with --check-scopes"),
	}
}
