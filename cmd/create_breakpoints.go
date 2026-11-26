package cmd

import (
	"context"
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/Dynatrace/dtctl/pkg/config"
	"github.com/Dynatrace/dtctl/pkg/resources/livedebugger"
	"github.com/Dynatrace/dtctl/pkg/safety"
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

In a repository linked with 'dtctl repo-scope', a workspace without filters
gets the namespace and workload filter of the entry covering the working
directory, when it has exactly one; --repo-scope <name> sets every filter set
of a named entry, service names and process groups included, and
--no-repo-scope turns the default off. A workspace that already has filters
keeps them.

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

  # Set workspace filters from a repo scope entry
  dtctl create breakpoint OrderController.java:306 --repo-scope checkout

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
			location := fmt.Sprintf("%s:%d", fileName, lineNumber)

			filters, _ := cmd.Flags().GetString("filters")
			filtersChanged := cmd.Flags().Changed("filters")
			scopeName, _ := cmd.Flags().GetString(repoScopeFlag)
			skipConfirm, _ := cmd.Flags().GetBool("yes")

			// Validate --filters up front, before any config or network call, so
			// format errors surface immediately. Filters are optional for create:
			// only validate when the flag was actually provided.
			var filterSets []map[string][]string
			var filterSummary string
			if filtersChanged {
				if err := requireFiltersValue(filters); err != nil {
					return err
				}
				parsed, err := parseFilters(filters)
				if err != nil {
					return err
				}
				filterSets = []map[string][]string{parsed}
				filterSummary = formatFilters(parsed)
			}

			preview := createBreakpointPreview{location: location, scopeName: scopeName, filtersChanged: filtersChanged, filterSummary: filterSummary}
			cfg, err := loadConfig(cmdContext(cmd))
			if err != nil {
				// A preview never newly requires config, except to find the
				// entry it was asked for: without one it says less.
				if dryRun(cmdContext(cmd)) && scopeName == "" {
					return printBreakpointMessage(cmdContext(cmd), "create", preview.message())
				}
				return err
			}
			if err := checkSafety(cmdContext(cmd), cfg, safety.OperationCreate, safety.OwnershipUnknown); err != nil {
				return err
			}
			// Setting the workspace's filters re-scopes every breakpoint in
			// it, so it is checked as the update it is, like update
			// breakpoint --filters, before the entry is read or anything is
			// sent.
			if filtersChanged || scopeName != "" {
				if err := checkSafety(cmdContext(cmd), cfg, safety.OperationUpdate, safety.OwnershipUnknown); err != nil {
					return err
				}
			}

			// The workspace is resolved before anything else reads the scope
			// file, so a file that does not load refuses the run here.
			workspace, err := resolveBreakpointWorkspace(cmd, cfg)
			if err != nil {
				return err
			}
			if err := workspace.requireScopeFile(); err != nil {
				return err
			}

			// A named entry is a filter source like --filters, resolved before
			// any network call.
			if scopeName != "" {
				if filterSets, err = namedBreakpointFilters(cmd, cfg, scopeName); err != nil {
					return err
				}
				filterSummary = filterSetsSummary(filterSets)
			}

			// A preview sends nothing, not even the read that finds the
			// workspace: asking for a workspace may create it.
			if dryRun(cmdContext(cmd)) {
				preview.workspace, preview.filterSummary = workspace.name, filterSummary
				if filterSets == nil {
					if preview.cov, err = defaultBreakpointFilters(cmd, cfg); err != nil {
						return err
					}
					if preview.cov.set != nil {
						noteClusterNotSent(currentStderr(cmdContext(cmd)), preview.cov.entry)
					}
				}
				return printBreakpointMessage(cmdContext(cmd), "create", preview.message())
			}

			handler, err := liveDebuggerHandler(cmdContext(cmd), cfg)
			if err != nil {
				return err
			}

			verbose := isDebugVerbose(cmdContext(cmd))

			workspaceResp, workspaceID, err := handler.GetOrCreateWorkspace(workspace.name)
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

			// appliedScope names the entry the filters came from, when one did.
			appliedScope := scopeName
			var suggestions []string
			if filterSets != nil {
				cancelled, err := applyWorkspaceFilters(cmdContext(cmd), handler, workspaceID, filterSets,
					workspaceFilterChange{creating: true, skipConfirm: skipConfirm})
				if err != nil {
					return err
				}
				if cancelled {
					return printBreakpointMessage(cmdContext(cmd), "create", "Cancelled")
				}
			} else {
				applied, err := applyDefaultBreakpointFilters(cmd, cfg, handler, workspaceID, workspaceResp, location, skipConfirm)
				if err != nil {
					return err
				}
				if applied.cancelled {
					return printBreakpointMessage(cmdContext(cmd), "create", "Cancelled")
				}
				if applied.scope != "" {
					appliedScope, filterSummary = applied.scope, applied.summary
					suggestions = []string{fmt.Sprintf(
						"the workspace filters were set from the workload filter of repo scope %s, the entry covering the working directory; pass --filters to set them by hand, --%s to turn this default off, or --%s <name> to set every filter set of an entry",
						applied.scope, noRepoScopeFlag, repoScopeFlag)}
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

			msg := "Created breakpoint at " + location
			if id := extractCreatedBreakpointStableID(createResp); id != "" {
				msg += fmt.Sprintf(" (id: %s)", id)
			}
			if appliedScope != "" {
				msg += fmt.Sprintf("; workspace filters set from repo scope %s: %s", appliedScope, filterSummary)
			}
			return printBreakpointMessageWith(cmdContext(cmd), "create", inWorkspace(msg, workspace.name), suggestions)
		},
	}
	c.Flags().String("filters", "", "workspace filters to set before creating the breakpoint (comma-separated key:value pairs)")
	c.Flags().BoolP("yes", "y", false, "skip the confirmation prompt when changing workspace filters affects existing breakpoints")
	addRepoScopeFlags(c, createBreakpointRepoScopeUsage, createBreakpointNoRepoScopeUsage)
	c.MarkFlagsMutuallyExclusive(repoScopeFlag, "filters")
	markPrompts(c, "yes", "dry-run")
	markLiveDebuggerExperimental(c)
	return c
}

// liveDebuggerHandler is the Live Debugger handler for cfg's current context.
func liveDebuggerHandler(ctx context.Context, cfg *config.Config) (*livedebugger.Handler, error) {
	c, err := newClientFromConfig(ctx, cfg)
	if err != nil {
		return nil, err
	}
	current, err := cfg.CurrentContextObj()
	if err != nil {
		return nil, err
	}
	return livedebugger.NewHandler(c, current.Environment)
}

// createBreakpointPreview is what a create's dry run knows without asking
// the environment: the filter source it was given, if any, and otherwise
// what the repo scope default offers a workspace without filters (cov).
// workspace is empty when no config loaded.
type createBreakpointPreview struct {
	location, workspace, scopeName string
	filtersChanged                 bool
	filterSummary                  string
	cov                            coveringFilters
}

// message words the preview. Whether the workspace has filters is not
// known, so what depends on it is worded as the condition it is.
func (p createBreakpointPreview) message() string {
	const rescopeNote = "(note: changing filters also re-scopes existing breakpoints in the workspace)"
	filtersOf := "workspace filters"
	if p.workspace != "" {
		filtersOf = "workspace " + p.workspace + "'s filters"
	}
	switch {
	case p.filtersChanged:
		return fmt.Sprintf("Dry run: would set %s (%s) and create breakpoint at %s %s", filtersOf, p.filterSummary, p.location, rescopeNote)
	case p.scopeName != "":
		return fmt.Sprintf("Dry run: would set %s from repo scope %s (%s) and create breakpoint at %s %s", filtersOf, p.scopeName, p.filterSummary, p.location, rescopeNote)
	}
	msg := "Dry run: would create breakpoint at " + p.location
	if p.workspace == "" {
		return msg
	}
	msg += " in workspace " + p.workspace
	switch {
	case p.cov.set != nil:
		return msg + fmt.Sprintf("; if it has no filters, would set them from repo scope %s: %s; a workspace that has filters keeps them, and one with active breakpoints asks first",
			p.cov.name, formatFilters(p.cov.set))
	case p.cov.name != "":
		return msg + fmt.Sprintf(", if it has filters: repo scope %s has no single workload filter set (it renders %s), so pass --%s %s for all of them, or --filters key:value",
			p.cov.name, filterSetsSummary(p.cov.sets), repoScopeFlag, p.cov.name)
	case p.cov.skipped != "":
		return msg + fmt.Sprintf(", if it has filters: %s, or pass --filters key:value", p.cov.skipped)
	}
	return msg + ", if it has filters: otherwise pass --filters key:value"
}

func extractCreatedBreakpointStableID(resp map[string]interface{}) string {
	data, _ := resp["data"].(map[string]interface{})
	org, _ := data["org"].(map[string]interface{})
	workspace, _ := org["workspace"].(map[string]interface{})
	rule, _ := workspace["createRuleV2"].(map[string]interface{})
	id, _ := rule["id"].(string)
	return id
}
