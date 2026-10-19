package cmd

import (
	"fmt"
	"unicode/utf8"

	"github.com/spf13/cobra"

	"github.com/dynatrace-oss/dtctl/pkg/resources/appengine"
)

// actionsDevelopmentFeature is the opt-in key for the action resource: it gates
// both `get actions` and `describe action`.
const actionsDevelopmentFeature = "actions"

var getActionsCmd = newGetActionsCmd()

func newGetActionsCmd() *cobra.Command {
	var (
		getActionsAppFilter string
		getActionsName      string
		getActionsFilter    string
	)
	c := &cobra.Command{
		Use:     "actions [app-id:action-name]",
		Aliases: []string{"action"},
		Short:   "Get App Engine actions",
		Long: `Get app actions from installed apps.

Actions are declared in an app's manifest and describe the operations the
app exposes, for example to workflow tasks.

--name is a whitespace-separated list of search terms, matched
case-insensitively against the app id and name and the action name, title and
description. Each additional term narrows the result further.

--filter is a raw AppRegistry filter expression on app.id and the action's
fields (action.<field>, nested paths included), sent to the API verbatim. Combined with
--name, an action must match both. Both are limited to 256 characters.

Examples:
  # List all actions across all apps
  dtctl get actions

  # List actions for a specific app
  dtctl get actions --app dynatrace.automations

  # Search actions -- each term must match, so terms narrow the result
  dtctl get actions --name jira
  dtctl get actions --name 'jira create'

  # Filter on action fields, nested ones included
  dtctl get actions --filter "not (action.docs.inputSchema is-empty)"
  dtctl get actions --filter "action.name starts-with 'jira-'"

  # Action fields are JSON-typed: quote the value, even for a boolean
  dtctl get actions --filter "action.stateful = 'true'"

  # Search and filter together
  dtctl get actions --name jira --filter "not (action.docs.inputSchema is-empty)"

  # Get a specific action
  dtctl get action dynatrace.automations:execute-dql-query

  # Output as JSON
  dtctl get actions -o json

  # Wide output (adds description and stateful flag)
  dtctl get actions -o wide
`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := validateGetActionsInput(cmd, args); err != nil {
				return err
			}

			_, c, printer, err := setup(cmdContext(cmd))
			if err != nil {
				return err
			}

			handler := appengine.NewHandler(c)

			if len(args) > 0 {
				action, err := handler.GetAction(args[0])
				if err != nil {
					return err
				}
				return printer.Print(action)
			}

			actions, err := handler.ListActions(appengine.ListActionsOptions{
				AppID:  getActionsAppFilter,
				Query:  getActionsName,
				Filter: getActionsFilter,
			})
			if err != nil {
				return err
			}

			return printer.PrintList(actions)
		},
	}
	c.Flags().StringVar(&getActionsAppFilter, "app", "", "filter by app ID")
	c.Flags().StringVar(&getActionsName, "name", "", "search terms, matched against action name, title, description and app id and name")
	c.Flags().StringVar(&getActionsFilter, "filter", "", "raw AppRegistry filter expression, e.g. \"not (action.docs.inputSchema is-empty)\"")
	return c
}

// validateGetActionsInput rejects input the command would otherwise ignore or
// the API would refuse, naming the flag the user typed.
func validateGetActionsInput(cmd *cobra.Command, args []string) error {
	if len(args) > 0 {
		for _, flag := range []string{"app", "name", "filter"} {
			if cmd.Flags().Changed(flag) {
				return fmt.Errorf("--%s is a list filter and cannot be combined with an action id", flag)
			}
		}
	}
	limits := []struct {
		flag string
		max  int
	}{
		{"name", appengine.MaxActionQueryLength},
		{"filter", appengine.MaxActionFilterLength},
	}
	for _, l := range limits {
		value, _ := cmd.Flags().GetString(l.flag)
		if n := utf8.RuneCountInString(value); n > l.max {
			return fmt.Errorf("--%s is %d characters, the maximum is %d", l.flag, n, l.max)
		}
	}
	return nil
}
