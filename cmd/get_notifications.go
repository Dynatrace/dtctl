package cmd

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/dynatrace-oss/dtctl/pkg/output"
	"github.com/dynatrace-oss/dtctl/pkg/prompt"
	"github.com/dynatrace-oss/dtctl/pkg/resources/notification"
	"github.com/dynatrace-oss/dtctl/pkg/safety"
	"github.com/dynatrace-oss/dtctl/pkg/stability"
)

// getNotificationsCmd retrieves notifications
var getNotificationsCmd = newGetNotificationsCmd()

func newGetNotificationsCmd() *cobra.Command {
	c := &cobra.Command{
		Use:     "notifications [id]",
		Aliases: []string{"notification", "notif"},
		Short:   "Get event notifications",
		Long: `Get event notifications.

Examples:
  # List all event notifications
  dtctl get notifications

  # Get a specific notification
  dtctl get notification <notification-id>

  # Filter by notification type
  dtctl get notifications --type my-notification-type

  # Output as JSON
  dtctl get notifications -o json
`,
		RunE: func(cmd *cobra.Command, args []string) error {
			notifType, _ := cmd.Flags().GetString("type")

			_, c, printer, err := setup(cmdContext(cmd))
			if err != nil {
				return err
			}

			handler := notification.NewHandler(c)

			// Get specific notification if ID provided
			if len(args) > 0 {
				n, err := handler.GetEventNotification(args[0])
				if err != nil {
					return err
				}
				return printer.Print(n)
			}

			// List all notifications
			list, err := handler.ListEventNotifications(notifType)
			if err != nil {
				return err
			}

			return printer.PrintList(list.Results)
		},
	}
	c.Flags().String("type", "", "Filter by notification type")
	stability.MarkStable(c)
	return c
}

// deleteNotificationCmd deletes a notification
var deleteNotificationCmd = newDeleteNotificationCmd()

func newDeleteNotificationCmd() *cobra.Command {
	var forceDelete bool
	c := &cobra.Command{
		Use:   "notification <notification-id>",
		Short: "Delete an event notification",
		Long: `Delete an event notification by ID.

Examples:
  # Delete a notification
  dtctl delete notification <notification-id>

  # Delete without confirmation
  dtctl delete notification <notification-id> -y
`,
		Aliases: []string{"notif"},
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			notifID := args[0]

			cfg, c, err := setupClient(cmdContext(cmd))
			if err != nil {
				return err
			}

			handler := notification.NewHandler(c)

			// Get notification for confirmation and ownership check
			n, err := handler.GetEventNotification(notifID)
			if err != nil {
				return err
			}

			// Safety check with actual ownership
			currentUserID, _ := c.CurrentUserID()
			ownership := safety.DetermineOwnership(n.Owner, currentUserID)
			if err := checkSafety(cmdContext(cmd), cfg, safety.OperationDelete, ownership); err != nil {
				return err
			}

			if dryRun(cmdContext(cmd)) {
				return deleteDryRun(cmd, "notification", n.NotificationType, notifID)
			}

			// Confirm deletion unless --force or --plain
			if !forceDelete && !plainMode(cmdContext(cmd)) {
				if !prompt.ConfirmDeletionWith(currentStdin(cmdContext(cmd)), currentStdout(cmdContext(cmd)), "notification", n.NotificationType, notifID) {
					fmt.Fprintln(currentStdout(cmdContext(cmd)), "Deletion cancelled")
					return nil
				}
			}

			if err := handler.DeleteEventNotification(notifID); err != nil {
				return err
			}

			output.FprintSuccess(currentStderr(cmdContext(cmd)), "Notification %q deleted", notifID)
			return nil
		},
	}
	c.Flags().BoolVarP(&forceDelete, "yes", "y", false, "Skip confirmation prompt")
	stability.MarkStable(c)
	return c
}

func init() {
	// Notification flags
	// Delete confirmation flags
}

// Declared stable: the invocation and output contract of these commands is
// additive-only. Stable is never implied -- see AGENTS.md "Stability Tiers".
func init() {
}
