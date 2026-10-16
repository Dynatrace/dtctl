package cmd

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/dynatrace-oss/dtctl/pkg/prompt"
	"github.com/dynatrace-oss/dtctl/pkg/resources/livedebugger"
	"github.com/dynatrace-oss/dtctl/pkg/safety"
)

var createBreakpointCmd = newCreateBreakpointCmd()

func newCreateBreakpointCmd() *cobra.Command {
	c := &cobra.Command{
		Use:     "breakpoint <filename:line>",
		Aliases: []string{"breakpoints", "bp"},
		Short:   "Create a Live Debugger breakpoint",
		Long: `Create a Live Debugger breakpoint in the current workspace.

A breakpoint can only be created in a workspace that has filters configured.
Set filters in the same step with --filters, or beforehand with
'dtctl update breakpoint --filters key:value'. Filters are workspace-scoped and
persist, so once set you can create more breakpoints without repeating them.

Because filters are workspace-scoped, changing them with --filters also re-scopes
every existing breakpoint in the workspace. When active breakpoints would be
affected you are asked to confirm; pass --yes (-y) to skip the prompt.

Examples:
  # Create a breakpoint (workspace must already have filters)
  dtctl create breakpoint OrderController.java:306

  # Set workspace filters and create the breakpoint in one step
  dtctl create breakpoint OrderController.java:306 --filters k8s.namespace.name:prod

  # Multiple filters
  dtctl create breakpoint OrderController.java:306 --filters k8s.namespace.name:prod,dt.entity.host:HOST-123

  # Dry run to preview
  dtctl create breakpoint OrderController.java:306 --dry-run
`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			identifier := strings.TrimSpace(args[0])
			fileName, lineNumber, err := parseBreakpoint(identifier)
			if err != nil {
				return err
			}

			filters, _ := cmd.Flags().GetString("filters")
			filtersChanged := cmd.Flags().Changed("filters")
			skipConfirm, _ := cmd.Flags().GetBool("yes")

			// Validate --filters up front, before any config or network call, so
			// format errors surface immediately. Filters are optional for create:
			// only validate when the flag was actually provided.
			var filterSets []map[string]interface{}
			var filterSummary string
			if filtersChanged {
				if err := requireFiltersValue(filters); err != nil {
					return err
				}
				parsed, err := parseFilters(filters)
				if err != nil {
					return err
				}
				filterSets = livedebugger.BuildFilterSets(parsed)
				filterSummary = formatFilters(parsed)
			}

			// Dry-run preview must not be blocked by the safety check (a readonly
			// context should still show the preview), so it runs before
			// SetupWithSafety. Matches create_workflows.go / create_buckets.go.
			if dryRun(cmdContext(cmd)) {
				if filtersChanged {
					return printBreakpointMessage(cmdContext(cmd), "create", fmt.Sprintf("Dry run: would set workspace filters (%s) and create breakpoint at %s:%d (note: changing filters also re-scopes existing breakpoints in the workspace)", filterSummary, fileName, lineNumber))
				}
				return printBreakpointMessage(cmdContext(cmd), "create", fmt.Sprintf("Dry run: would create breakpoint at %s:%d", fileName, lineNumber))
			}

			cfg, c, err := setupWithSafety(cmdContext(cmd), safety.OperationCreate)
			if err != nil {
				return err
			}

			verbose := isDebugVerbose(cmdContext(cmd))

			ctx, err := cfg.CurrentContextObj()
			if err != nil {
				return err
			}

			handler, err := livedebugger.NewHandler(c, ctx.Environment)
			if err != nil {
				return err
			}

			workspaceResp, workspaceID, err := handler.GetOrCreateWorkspace(currentProjectPath())
			if err != nil {
				if verbose {
					_ = printGraphQLResponse(cmdContext(cmd), "getOrCreateWorkspaceV2", workspaceResp)
				}
				return err
			}
			if verbose {
				if err := printGraphQLResponse(cmdContext(cmd), "getOrCreateWorkspaceV2", workspaceResp); err != nil {
					return err
				}
			}

			if filtersChanged {
				// Changing workspace filters re-scopes every existing active
				// breakpoint in the workspace, not just the one being created.
				// Confirm first, unless --yes or a non-interactive (--plain/agent)
				// context. The extra read is only paid when we might prompt.
				if !skipConfirm && !plainMode(cmdContext(cmd)) {
					count, err := countActiveWorkspaceBreakpoints(handler, workspaceID)
					if err != nil {
						return err
					}
					if count > 0 && !prompt.ConfirmWith(currentStdin(cmdContext(cmd)), currentStdout(cmdContext(cmd)), filterChangeConfirmMessage(count, true)) {
						return printBreakpointMessage(cmdContext(cmd), "create", "Cancelled")
					}
				}

				updateResp, err := handler.UpdateWorkspaceFilters(workspaceID, filterSets)
				if err != nil {
					if verbose {
						_ = printGraphQLResponse(cmdContext(cmd), "updateWorkspaceV2", updateResp)
					}
					return err
				}
				if verbose {
					if err := printGraphQLResponse(cmdContext(cmd), "updateWorkspaceV2", updateResp); err != nil {
						return err
					}
				}
			} else {
				hasFilters, err := livedebugger.WorkspaceHasFilters(workspaceResp)
				if err != nil {
					return err
				}
				if !hasFilters {
					return fmt.Errorf("no workspace filters configured; set filters first with:\n  dtctl update breakpoint --filters key:value\nor pass --filters when creating:\n  dtctl create breakpoint %s:%d --filters key:value", fileName, lineNumber)
				}
			}

			createResp, err := handler.CreateBreakpoint(workspaceID, fileName, lineNumber)
			if err != nil {
				if verbose {
					_ = printGraphQLResponse(cmdContext(cmd), "createRuleV2", createResp)
				}
				return err
			}
			if verbose {
				if err := printGraphQLResponse(cmdContext(cmd), "createRuleV2", createResp); err != nil {
					return err
				}
			}

			msg := fmt.Sprintf("Created breakpoint at %s:%d", fileName, lineNumber)
			if id := extractCreatedBreakpointStableID(createResp); id != "" {
				msg += fmt.Sprintf(" (id: %s)", id)
			}
			return printBreakpointMessage(cmdContext(cmd), "create", msg)
		},
	}
	c.Flags().String("filters", "", "workspace filters to set before creating the breakpoint (comma-separated key:value pairs)")
	c.Flags().BoolP("yes", "y", false, "skip the confirmation prompt when changing workspace filters affects existing breakpoints")
	markLiveDebuggerExperimental(c)
	return c
}

func extractCreatedBreakpointStableID(resp map[string]interface{}) string {
	data, _ := resp["data"].(map[string]interface{})
	org, _ := data["org"].(map[string]interface{})
	workspace, _ := org["workspace"].(map[string]interface{})
	rule, _ := workspace["createRuleV2"].(map[string]interface{})
	id, _ := rule["id"].(string)
	return id
}
