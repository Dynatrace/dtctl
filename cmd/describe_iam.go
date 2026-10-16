package cmd

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/dynatrace-oss/dtctl/pkg/output"
	"github.com/dynatrace-oss/dtctl/pkg/resources/iam"
	"github.com/dynatrace-oss/dtctl/pkg/stability"
)

// describeUserCmd shows detailed info about a user
var describeUserCmd = newDescribeUserCmd()

func newDescribeUserCmd() *cobra.Command {
	c := &cobra.Command{
		Use:     "user <user-uuid>",
		Aliases: []string{"users"},
		Short:   "Show details of an IAM user",
		Long: `Show detailed information about an IAM user.

Examples:
  # Describe a user by UUID
  dtctl describe user <user-uuid>
`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			userUUID := args[0]

			_, c, printer, err := setup(cmdContext(cmd))
			if err != nil {
				return err
			}

			handler := iam.NewHandler(c)

			user, err := handler.GetUser(userUUID)
			if err != nil {
				return err
			}

			// For table output, show detailed human-readable information
			if outputFormat(cmdContext(cmd)) == "table" {
				const w = 13
				output.FprintDescribeKV(currentStdout(cmdContext(cmd)), "UUID:", w, "%s", user.UID)
				output.FprintDescribeKV(currentStdout(cmdContext(cmd)), "Email:", w, "%s", user.Email)
				if user.Name != "" {
					output.FprintDescribeKV(currentStdout(cmdContext(cmd)), "Name:", w, "%s", user.Name)
				}
				if user.Surname != "" {
					output.FprintDescribeKV(currentStdout(cmdContext(cmd)), "Surname:", w, "%s", user.Surname)
				}
				if user.Description != "" {
					output.FprintDescribeKV(currentStdout(cmdContext(cmd)), "Description:", w, "%s", user.Description)
				}

				return nil
			}

			// For other formats, use standard printer
			enrichAgent(printer, "describe", "user")
			return printer.Print(user)
		},
	}
	stability.MarkStable(c)
	return c
}

// describeGroupCmd shows detailed info about a group
var describeGroupCmd = newDescribeGroupCmd()

func newDescribeGroupCmd() *cobra.Command {
	c := &cobra.Command{
		Use:     "group <group-uuid>",
		Aliases: []string{"groups"},
		Short:   "Show details of an IAM group",
		Long: `Show detailed information about an IAM group.

Examples:
  # List all groups to find UUID, then describe
  dtctl get groups
  dtctl describe group <group-uuid>
`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			groupUUID := args[0]

			_, c, printer, err := setup(cmdContext(cmd))
			if err != nil {
				return err
			}

			handler := iam.NewHandler(c)

			// Since there's no get single group endpoint, we list and filter
			list, err := handler.ListGroups("", []string{groupUUID}, getChunkSize(cmdContext(cmd)))
			if err != nil {
				return err
			}

			if len(list.Results) == 0 {
				return fmt.Errorf("group %q not found", groupUUID)
			}

			group := list.Results[0]

			// For table output, show detailed human-readable information
			if outputFormat(cmdContext(cmd)) == "table" {
				const w = 11
				output.FprintDescribeKV(currentStdout(cmdContext(cmd)), "UUID:", w, "%s", group.UUID)
				output.FprintDescribeKV(currentStdout(cmdContext(cmd)), "Name:", w, "%s", group.GroupName)
				output.FprintDescribeKV(currentStdout(cmdContext(cmd)), "Type:", w, "%s", group.Type)

				return nil
			}

			// For other formats, use standard printer
			enrichAgent(printer, "describe", "group")
			return printer.Print(group)
		},
	}
	stability.MarkStable(c)
	return c
}

// Declared stable: the invocation and output contract of these commands is
// additive-only. Stable is never implied -- see AGENTS.md "Stability Tiers".
func init() {
}
