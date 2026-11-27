package cmd

import (
	"context"
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/Dynatrace/dtctl/pkg/config"
	"github.com/Dynatrace/dtctl/pkg/output"
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

In a repository linked with 'dtctl repo-scope', --repo-scope <name> sets the
filters from the named entry: every filter set it renders, service names and
process groups included. Nothing is set from an entry you did not name: where
an entry covers the working directory, the command says so and names the flag.

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
			notes := workspace.changeNotes()
			var entryNotes []string
			if scopeName != "" {
				if filterSets, entryNotes, err = namedBreakpointFilters(workspace); err != nil {
					return err
				}
				filterSummary = filterSetsSummary(filterSets)
				notes = append(notes, entryNotes...)
			}
			// Said before the confirmation and before anything is sent.
			ctx := sayBreakpointWarnings(workspace.warnScopeFile(cmdContext(cmd)), notes...)

			// A preview sends nothing, not even the read that finds the
			// workspace: asking for a workspace may create it.
			if dryRun(cmdContext(cmd)) {
				preview.workspace, preview.filterSummary = workspace.shown(), filterSummary
				if filterSets == nil {
					preview.cov = coveringBreakpointEntry(cmd, workspace)
				}
				return printBreakpointMessage(ctx, "create", preview.message())
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

			// Filters are set only from a source the user named. Without one
			// the workspace must have its own, and hint says afterwards when
			// they are not the covering entry's.
			var hint string
			var changed *workspaceFiltersChange
			if filterSets != nil {
				// Read before the write: a create that fails after it says
				// what the filters were.
				before, err := livedebugger.WorkspaceFilterSets(workspaceResp)
				if err != nil {
					return err
				}
				changed = &workspaceFiltersChange{workspace: workspace.shown(), before: selectingFilterSets(before), after: filterSummary, scopeName: scopeName, location: location,
					selectors: breakpointSelectors(cmdContext(cmd), cfg), notes: entryNotes}
				if scopeName == "" {
					changed.covering = coveringBreakpointEntry(cmd, workspace)
				}
				cancelled, err := applyWorkspaceFilters(ctx, handler, workspaceID, filterSets,
					workspaceFilterChange{creating: true, skipConfirm: skipConfirm})
				if err != nil {
					return err
				}
				if cancelled {
					return printBreakpointMessage(ctx, "create", "Cancelled")
				}
			} else if hint, err = unfilteredCreate(cmd, cfg, workspace, workspaceResp, location); err != nil {
				return err
			}

			createResp, err := handler.CreateBreakpoint(workspaceID, fileName, lineNumber)
			if err != nil {
				if verbose {
					_ = printGraphQLResponse(cmdContext(cmd), "createRuleV2", createResp)
				}
				// The filters, if this run set them, stay set: undoing them
				// is a second write that can fail too and re-scopes the
				// workspace once more. The error says what was changed.
				if changed != nil {
					return changed.thenFailed(err)
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
			if scopeName != "" {
				msg += fmt.Sprintf("; workspace filters set from repo scope %s: %s", scopeName, filterSummary)
			}
			var suggestions []string
			if hint != "" {
				output.FprintInfo(currentStderr(cmdContext(cmd)), "%s", hint)
				suggestions = []string{hint}
			}
			return printBreakpointMessageWith(ctx, "create", inWorkspace(msg, workspace.shown()), suggestions)
		},
	}
	c.Flags().String("filters", "", "workspace filters to set before creating the breakpoint (comma-separated key:value pairs)")
	c.Flags().BoolP("yes", "y", false, "skip the confirmation prompt when changing workspace filters affects existing breakpoints")
	addRepoScopeFlags(c, createBreakpointRepoScopeUsage, "")
	c.MarkFlagsMutuallyExclusive(repoScopeFlag, "filters")
	markPrompts(c, "yes", "dry-run")
	markLiveDebuggerExperimental(c)
	return c
}

// unfilteredCreate is a create at location that names no filters, in the
// workspace workspaceResp describes: it fails when the workspace has none,
// and otherwise returns the hint to print once the breakpoint exists.
func unfilteredCreate(cmd *cobra.Command, cfg *config.Config, workspace breakpointWorkspace, workspaceResp map[string]interface{}, location string) (hint string, err error) {
	cov := coveringBreakpointEntry(cmd, workspace)
	hasFilters, err := livedebugger.WorkspaceHasFilters(workspaceResp)
	if err != nil {
		return "", err
	}
	if !hasFilters {
		return "", cov.noFilters(workspace.shown(), location)
	}
	current, err := livedebugger.WorkspaceFilterSets(workspaceResp)
	if err != nil {
		return "", err
	}
	return cov.hint(workspace.shown(), current, breakpointSelectors(cmdContext(cmd), cfg)), nil
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
// what the scope file says about the working directory (cov). workspace is
// empty when no config loaded.
type createBreakpointPreview struct {
	location, workspace, scopeName string
	filtersChanged                 bool
	filterSummary                  string
	cov                            coveringEntry
}

// message words the preview. Whether the workspace has filters is not
// known, so the breakpoint's need for them is said rather than checked.
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
	case p.workspace == "":
		return "Dry run: would create breakpoint at " + p.location
	}
	return "Dry run: would create breakpoint at " + p.location + " in workspace " + p.workspace + p.cov.preview()
}

func extractCreatedBreakpointStableID(resp map[string]interface{}) string {
	data, _ := resp["data"].(map[string]interface{})
	org, _ := data["org"].(map[string]interface{})
	workspace, _ := org["workspace"].(map[string]interface{})
	rule, _ := workspace["createRuleV2"].(map[string]interface{})
	id, _ := rule["id"].(string)
	return id
}
