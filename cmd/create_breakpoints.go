package cmd

import (
	"context"
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/dynatrace-oss/dtctl/pkg/config"
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

			// A named entry is a filter source like --filters, resolved before
			// any network call. It needs the config: the entries are keyed by
			// the context's environment.
			var cfg *config.Config
			if scopeName != "" {
				if cfg, err = loadConfig(cmdContext(cmd)); err != nil {
					return err
				}
				if filterSets, err = namedBreakpointFilters(cmd, cfg, scopeName); err != nil {
					return err
				}
				filterSummary = filterSetsSummary(filterSets)
			}

			// The preview writes nothing; checkSafety, which it passes
			// through before its one read, exempts it.
			if dryRun(cmdContext(cmd)) {
				msg, err := previewCreateBreakpoint(cmd, cfg, location, scopeName, filtersChanged, filterSummary)
				if err != nil {
					return err
				}
				return printBreakpointMessage(cmdContext(cmd), "create", msg)
			}

			if cfg == nil {
				if cfg, err = loadConfig(cmdContext(cmd)); err != nil {
					return err
				}
			}
			if err := checkSafety(cmdContext(cmd), cfg, safety.OperationCreate, safety.OwnershipUnknown); err != nil {
				return err
			}
			handler, err := liveDebuggerHandler(cmdContext(cmd), cfg)
			if err != nil {
				return err
			}

			verbose := isDebugVerbose(cmdContext(cmd))

			workspaceResp, workspaceID, err := handler.GetOrCreateWorkspace(currentProjectPath(cmdContext(cmd)))
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
				applied, err := applyDefaultBreakpointFilters(cmd, cfg, breakpointWorkspace{handler: handler, id: workspaceID, resp: workspaceResp}, location, skipConfirm)
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
			return printBreakpointMessageWith(cmdContext(cmd), "create", msg, suggestions)
		},
	}
	c.Flags().String("filters", "", "workspace filters to set before creating the breakpoint (comma-separated key:value pairs)")
	c.Flags().BoolP("yes", "y", false, "skip the confirmation prompt when changing workspace filters affects existing breakpoints")
	addRepoScopeFlags(c, createBreakpointRepoScopeUsage, createBreakpointNoRepoScopeUsage)
	c.MarkFlagsMutuallyExclusive(repoScopeFlag, "filters")
	markLiveDebuggerExperimental(c)
	return c
}

// previewCreateBreakpoint is a dry run's message. Without a filter source it
// reads the workspace, as get breakpoints and delete --dry-run do
// (getOrCreateWorkspaceV2 is a read, though it may create an empty
// workspace), so it can say what the repo scope default will do; a client
// that does not build fails the preview like a read that fails. A preview
// with a filter source sends nothing, and never newly requires config: when
// none loads, the workspace goes unnamed.
func previewCreateBreakpoint(cmd *cobra.Command, cfg *config.Config, location, scopeName string, filtersChanged bool, filterSummary string) (string, error) {
	ctx := cmdContext(cmd)
	preview := createBreakpointPreview{location: location, scopeName: scopeName, filtersChanged: filtersChanged, filterSummary: filterSummary}
	if cfg == nil {
		loaded, err := loadConfig(ctx)
		if err != nil {
			return preview.message(), nil
		}
		cfg = loaded
	}
	preview.workspace = currentProjectPath(ctx)
	if filtersChanged || scopeName != "" {
		return preview.message(), nil
	}
	var err error
	if preview.cov, err = defaultBreakpointFilters(cmd, cfg); err != nil {
		return "", err
	}
	if err := checkSafety(ctx, cfg, safety.OperationCreate, safety.OwnershipUnknown); err != nil {
		return "", err
	}
	handler, err := liveDebuggerHandler(ctx, cfg)
	if err != nil {
		return "", err
	}
	resp, _, err := handler.GetOrCreateWorkspace(preview.workspace)
	if err != nil {
		return "", err
	}
	if preview.hasFilters, err = livedebugger.WorkspaceHasFilters(resp); err != nil {
		return "", err
	}
	if preview.current, err = livedebugger.WorkspaceFilterSets(resp); err != nil {
		return "", err
	}
	return preview.message(), nil
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

// createBreakpointPreview is what a create's dry run knows: the filter source
// it was given, if any, and otherwise the workspace's filters (hasFilters,
// current) and what the repo scope default would do to them (cov).
// workspace is empty when no config loaded.
type createBreakpointPreview struct {
	location, workspace, scopeName string
	filtersChanged                 bool
	filterSummary                  string
	cov                            coveringFilters
	hasFilters                     bool
	current                        []livedebugger.FilterSet
}

// message words the preview, the default's part as cov.outcome decides it.
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
	switch p.cov.outcome(p.hasFilters, p.current) {
	case defaultSets:
		msg += fmt.Sprintf("; would set %s from repo scope %s: %s", filtersOf, p.cov.name, formatFilters(p.cov.set))
	case defaultNoEntry, defaultNoSet:
		hint := "pass --filters"
		if p.cov.name != "" {
			hint = fmt.Sprintf("repo scope %s has no single set for it; pass --filters or --%s %s", p.cov.name, repoScopeFlag, p.cov.name)
		}
		return fmt.Sprintf("Dry run: creating breakpoint at %s would fail: workspace %s has no filters; %s", p.location, p.workspace, hint)
	case defaultWarns:
		msg += fmt.Sprintf("; would warn: workspace %s holds %s, repo scope %s would set %s",
			p.workspace, workspaceFilterSummary(p.current), p.cov.name, filterSetsSummary(p.cov.sets))
	case defaultKeeps:
		msg += fmt.Sprintf("; workspace %s keeps its filters (%s)", p.workspace, workspaceFilterSummary(p.current))
	}
	return msg
}

func extractCreatedBreakpointStableID(resp map[string]interface{}) string {
	data, _ := resp["data"].(map[string]interface{})
	org, _ := data["org"].(map[string]interface{})
	workspace, _ := org["workspace"].(map[string]interface{})
	rule, _ := workspace["createRuleV2"].(map[string]interface{})
	id, _ := rule["id"].(string)
	return id
}
