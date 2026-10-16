package cmd

import (
	"github.com/spf13/cobra"

	"github.com/dynatrace-oss/dtctl/pkg/resources/iam"
	"github.com/dynatrace-oss/dtctl/pkg/stability"
)

// getUsersCmd retrieves IAM users
var getUsersCmd = newGetUsersCmd()

func newGetUsersCmd() *cobra.Command {
	c := &cobra.Command{
		Use:     "users [uuid]",
		Aliases: []string{"user"},
		Short:   "Get IAM users",
		Long: `Get users from Identity and Access Management.

Examples:
  # List all users
  dtctl get users

  # Get a specific user by UUID
  dtctl get user <user-uuid>

  # Filter users by email or name
  dtctl get users --filter "john"

  # Output as JSON
  dtctl get users -o json
`,
		RunE: func(cmd *cobra.Command, args []string) error {
			_, c, printer, err := setup(cmdContext(cmd))
			if err != nil {
				return err
			}

			handler := iam.NewHandler(c)

			// Get specific user if UUID provided
			if len(args) > 0 {
				user, err := handler.GetUser(args[0])
				if err != nil {
					return err
				}
				return printer.Print(user)
			}

			// List all users with optional filter
			filterStr, _ := cmd.Flags().GetString("filter")
			list, err := handler.ListUsers(filterStr, nil, getChunkSize(cmdContext(cmd)))
			if err != nil {
				return err
			}

			return printer.PrintList(list.Results)
		},
	}
	c.Flags().String("filter", "", "Filter users by email or name (partial match)")
	stability.MarkStable(c)
	return c
}

// getGroupsCmd retrieves IAM groups
var getGroupsCmd = newGetGroupsCmd()

func newGetGroupsCmd() *cobra.Command {
	c := &cobra.Command{
		Use:     "groups [uuid]",
		Aliases: []string{"group"},
		Short:   "Get IAM groups",
		Long: `Get groups from Identity and Access Management.

Examples:
  # List all groups
  dtctl get groups

  # Filter groups by name
  dtctl get groups --filter "admin"

  # Output as JSON
  dtctl get groups -o json
`,
		RunE: func(cmd *cobra.Command, args []string) error {
			_, c, printer, err := setup(cmdContext(cmd))
			if err != nil {
				return err
			}

			handler := iam.NewHandler(c)

			// List all groups with optional filter
			filterStr, _ := cmd.Flags().GetString("filter")
			list, err := handler.ListGroups(filterStr, nil, getChunkSize(cmdContext(cmd)))
			if err != nil {
				return err
			}

			return printer.PrintList(list.Results)
		},
	}
	c.Flags().String("filter", "", "Filter groups by name (partial match)")
	stability.MarkStable(c)
	return c
}

func init() {
	// IAM flags
}

// Declared stable: the invocation and output contract of these commands is
// additive-only. Stable is never implied -- see AGENTS.md "Stability Tiers".
func init() {
}
