package cmd

import (
	"context"
	"fmt"
	"io"

	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"

	"github.com/dynatrace-oss/dtctl/pkg/output"
	"github.com/dynatrace-oss/dtctl/pkg/resources/appengine"
	"github.com/dynatrace-oss/dtctl/pkg/util/unknownfields"
)

var describeActionCmd = newDescribeActionCmd()

func newDescribeActionCmd() *cobra.Command {
	c := &cobra.Command{
		Use:     "action <app-id>:<action-name>",
		Aliases: []string{"actions"},
		Short:   "Describe an App Engine action",
		Long: `Show detailed information about an app action.

Actions are declared in an app's manifest. Every attribute the manifest
declares for the action is shown, including nested ones such as
docs.inputSchema.

Examples:
  # Describe an action
  dtctl describe action dynatrace.automations:execute-dql-query

  # Output as JSON
  dtctl describe action dynatrace.automations:execute-dql-query -o json

  # Output as YAML
  dtctl describe action dynatrace.automations:execute-dql-query -o yaml
`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			_, c, printer, err := setup(cmdContext(cmd))
			if err != nil {
				return err
			}

			handler := appengine.NewHandler(c)

			action, err := handler.GetAction(args[0])
			if err != nil {
				return err
			}

			if useActionDescribeTextView(cmdContext(cmd)) {
				return describeActionTable(currentStdout(cmdContext(cmd)), action)
			}

			enrichAgent(printer, "describe", "action")
			return printer.Print(action)
		},
	}
	return c
}

// useActionDescribeTextView reports whether to render the human-readable text
// view. Agent mode always takes the structured envelope path: it leaves
// outputFormat at its "table" default, so a bare format check would emit human
// text into an agent session.
func useActionDescribeTextView(ctx context.Context) bool {
	if agentMode(ctx) {
		return false
	}
	return outputFormat(ctx) == "" || outputFormat(ctx) == "table"
}

// describeActionTable renders the vertical view. Stateful prints only when
// set: the manifest omits it rather than declaring it false, so "Stateful: no"
// would assert something the manifest never said.
func describeActionTable(w io.Writer, action *appengine.AppAction) error {
	const kw = 14

	output.FprintDescribeKV(w, "Action:", kw, "%s", action.ActionName)
	output.FprintDescribeKV(w, "Full Name:", kw, "%s", action.FullName)
	output.FprintDescribeKV(w, "App:", kw, "%s", action.AppID)
	if action.Title != "" {
		output.FprintDescribeKV(w, "Title:", kw, "%s", action.Title)
	}
	if action.Description != "" {
		output.FprintDescribeKV(w, "Description:", kw, "%s", action.Description)
	}
	if action.Stateful {
		output.FprintDescribeKV(w, "Stateful:", kw, "yes")
	}

	if len(action.Extra) > 0 {
		node, err := unknownfields.YAML(struct{}{}, action.Extra)
		if err != nil {
			return fmt.Errorf("render action attributes: %w", err)
		}
		// The empty base mapping encodes in flow style ("{}"), and the
		// appended attributes would inherit it; describe wants block style.
		if mapping, ok := node.(*yaml.Node); ok {
			mapping.Style = 0
		}
		fmt.Fprintln(w)
		enc := yaml.NewEncoder(w)
		enc.SetIndent(2)
		if err := enc.Encode(node); err != nil {
			return fmt.Errorf("render action attributes: %w", err)
		}
		if err := enc.Close(); err != nil {
			return fmt.Errorf("render action attributes: %w", err)
		}
	}

	return nil
}
