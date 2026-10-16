package cmd

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"golang.org/x/text/cases"
	"golang.org/x/text/language"

	"github.com/dynatrace-oss/dtctl/pkg/output"
	"github.com/dynatrace-oss/dtctl/pkg/prompt"
	"github.com/dynatrace-oss/dtctl/pkg/resources/resolver"
	"github.com/dynatrace-oss/dtctl/pkg/resources/workflow"
	"github.com/dynatrace-oss/dtctl/pkg/safety"
	"github.com/dynatrace-oss/dtctl/pkg/stability"
)

// minAutomationChunkSize is the smallest allowed --chunk-size for listing any
// limit/offset-paginated Automation API resource (workflows, scheduling rules).
// Smaller pages multiply the request count for no benefit and risk hammering the API.
const minAutomationChunkSize = 20

// validateAutomationChunkSize rejects tiny page sizes that fan a full listing out
// into excessive API requests. 0 (single page) and >= minAutomationChunkSize are allowed.
func validateAutomationChunkSize(chunk int64) error {
	if chunk > 0 && chunk < minAutomationChunkSize {
		return fmt.Errorf("--chunk-size must be 0 or at least %d (got %d)", minAutomationChunkSize, chunk)
	}
	return nil
}

// titleCase converts s to title case using golang.org/x/text/cases. A new
// Caser is allocated per call because cases.Caser is stateful and not safe
// for concurrent use.
func titleCase(s string) string {
	return cases.Title(language.Und).String(s)
}

// getWorkflowsCmd retrieves workflows
var getWorkflowsCmd = newGetWorkflowsCmd()

func newGetWorkflowsCmd() *cobra.Command {
	c := &cobra.Command{
		Use:     "workflows [id]",
		Aliases: []string{"workflow", "wf"},
		Short:   "Get workflows",
		Long: `Get one or more workflows.

Examples:
  # List all workflows
  dtctl get workflows

  # Get a specific workflow
  dtctl get workflow <workflow-id>

  # Output as JSON
  dtctl get workflows -o json

  # List only my workflows
  dtctl get workflows --mine
`,
		RunE: func(cmd *cobra.Command, args []string) error {
			_, c, printer, err := setup(cmdContext(cmd))
			if err != nil {
				return err
			}

			handler := workflow.NewHandler(c)
			ap := enrichAgent(printer, "get", "workflow")

			// Get specific workflow if ID provided
			if len(args) > 0 {
				wf, err := handler.Get(args[0])
				if err != nil {
					return err
				}
				if ap != nil {
					ap.SetSuggestions([]string{
						fmt.Sprintf("Run 'dtctl exec workflow %s' to trigger this workflow", args[0]),
						fmt.Sprintf("Run 'dtctl get workflow-executions --workflow %s' to see past executions", args[0]),
					})
				}
				return printer.Print(wf)
			}

			// List workflows with filters
			mineOnly, _ := cmd.Flags().GetBool("mine")
			filterStr, _ := cmd.Flags().GetString("filter")
			typeStr, _ := cmd.Flags().GetString("type")
			triggerStr, _ := cmd.Flags().GetString("trigger")
			limit, _ := cmd.Flags().GetInt64("limit")
			limit = agentPageLimit(cmd, limit)

			chunk := getChunkSize(cmdContext(cmd))
			if err := validateAutomationChunkSize(chunk); err != nil {
				return err
			}

			filters := workflow.WorkflowFilters{
				Search:      filterStr,
				TriggerType: titleCase(strings.ToLower(triggerStr)),
			}

			if typeStr != "" {
				filters.Type = strings.ToUpper(typeStr)
			}

			// If --mine flag is set, get current user ID and filter by owner
			if mineOnly {
				userID, err := c.CurrentUserID()
				if err != nil {
					return fmt.Errorf("failed to get current user ID for --mine filter: %w", err)
				}
				filters.Owner = userID
			}

			// Check if watch mode is enabled
			watchMode, _ := cmd.Flags().GetBool("watch")
			if watchMode {
				fetcher := func() (interface{}, error) {
					list, err := handler.List(filters, chunk, limit)
					if err != nil {
						return nil, err
					}
					return list.Results, nil
				}
				return executeWithWatch(cmd, fetcher, printer)
			}

			list, err := handler.List(filters, chunk, limit)
			if err != nil {
				return err
			}

			if ap != nil {
				// The server's count, not the page fetched: a --limit (or the
				// agent-mode default page) makes the page smaller than the list.
				ap.SetTotal(max(list.Count, len(list.Results)))
				suggestions := []string{
					"Run 'dtctl describe workflow <id>' for details",
					"Run 'dtctl exec workflow <id>' to trigger a workflow",
				}
				// If count from API exceeds returned results, more data exists. The
				// remedy depends on what capped the result: an explicit --limit, or
				// single-page mode (--chunk-size 0).
				if list.Count > len(list.Results) {
					ap.SetHasMore(true)
					if limit > 0 {
						suggestions = append(suggestions, fmt.Sprintf("Showing %d of %d. Raise --limit (currently %d) or set it to 0 for unlimited.", len(list.Results), list.Count, limit))
					} else {
						suggestions = append(suggestions, fmt.Sprintf("Showing %d of %d. Increase --chunk-size to page through all results.", len(list.Results), list.Count))
					}
				}
				ap.SetSuggestions(suggestions)
			}

			return printer.PrintList(list.Results)
		},
	}
	c.Flags().Bool("mine", false, "Show only workflows owned by current user")
	c.Flags().String("filter", "", "Search workflows by title")
	c.Flags().String("type", "", "Filter by workflow type: standard or simple")
	c.Flags().String("trigger", "", "Filter by trigger type: Manual, Schedule, Event")
	stability.MarkFlag(c, "trigger", stability.Experimental, pre10Since)
	c.Flags().Int64("limit", 0, "Maximum number of workflows to return (0 = unlimited)")
	stability.MarkStable(c)
	addWatchFlags(c)
	return c
}

// getWorkflowExecutionsCmd retrieves workflow executions
var getWorkflowExecutionsCmd = newGetWorkflowExecutionsCmd()

func newGetWorkflowExecutionsCmd() *cobra.Command {
	var workflowFilter string
	c := &cobra.Command{
		Use:     "workflow-executions [id]",
		Aliases: []string{"workflow-execution", "wfe"},
		Short:   "Get workflow executions",
		Long: `Get one or more workflow executions.

Examples:
  # List all workflow executions
  dtctl get workflow-executions
  dtctl get wfe

  # List executions for a specific workflow
  dtctl get wfe --workflow <workflow-id>
  dtctl get wfe -w <workflow-id>

  # Get a specific execution
  dtctl get wfe <execution-id>

  # Output as JSON
  dtctl get wfe -o json
`,
		RunE: func(cmd *cobra.Command, args []string) error {
			_, c, printer, err := setup(cmdContext(cmd))
			if err != nil {
				return err
			}

			handler := workflow.NewExecutionHandler(c)
			ap := enrichAgent(printer, "get", "workflow-execution")

			// Get specific execution if ID provided
			if len(args) > 0 {
				exec, err := handler.Get(args[0])
				if err != nil {
					return err
				}
				if ap != nil {
					ap.SetSuggestions([]string{
						fmt.Sprintf("Run 'dtctl logs workflow-execution %s' to view execution logs", args[0]),
					})
				}
				return printer.Print(exec)
			}

			// List executions (optionally filtered)
			limit, _ := cmd.Flags().GetInt64("limit")
			stateStr, _ := cmd.Flags().GetString("state")
			triggerStr, _ := cmd.Flags().GetString("trigger")
			sinceStr, _ := cmd.Flags().GetString("started-since")
			untilStr, _ := cmd.Flags().GetString("started-until")

			since, err := parseExecTime(sinceStr, false)
			if err != nil {
				return fmt.Errorf("invalid --started-since: %w", err)
			}
			until, err := parseExecTime(untilStr, true)
			if err != nil {
				return fmt.Errorf("invalid --started-until: %w", err)
			}

			list, err := handler.List(workflow.ExecutionFilters{
				WorkflowID:   workflowFilter,
				State:        strings.ToUpper(stateStr),
				TriggerType:  titleCase(strings.ToLower(triggerStr)),
				StartedSince: since,
				StartedUntil: until,
			}, limit)
			if err != nil {
				return err
			}

			if ap != nil {
				// The server's count, not the page fetched: a --limit (or the
				// agent-mode default page) makes the page smaller than the list.
				ap.SetTotal(max(list.Count, len(list.Results)))
				suggestions := []string{
					"Run 'dtctl get workflow-executions <id>' for execution details",
					"Run 'dtctl logs workflow-execution <id>' to view execution logs",
				}
				// Executions are limit-windowed (not fully paginated); flag when the
				// server total exceeds what was returned so agents don't assume completeness.
				if list.Count > len(list.Results) {
					ap.SetHasMore(true)
					suggestions = append(suggestions, executionsCapAdvice(len(list.Results), list.Count, limit)...)
				}
				ap.SetSuggestions(suggestions)
			}

			return printer.PrintList(list.Results)
		},
	}
	c.Flags().StringVarP(&workflowFilter, "workflow", "w", "", "Filter executions by workflow ID")
	c.Flags().Int64("limit", 100, "Maximum number of executions to return (max 1000)")
	c.Flags().String("state", "", "Filter by state: RUNNING, SUCCESS, ERROR, CANCELLED, UNKNOWN")
	c.Flags().String("trigger", "", "Filter by trigger type: Manual, Schedule, Event, Workflow")
	c.Flags().String("started-since", "", "Show executions started at or after this time (a duration ago such as 7d, YYYY-MM-DD or ISO 8601)")
	c.Flags().String("started-until", "", "Show executions started at or before this time (YYYY-MM-DD = end of day 23:59:59, or ISO 8601)")
	stability.MarkStable(c)
	return c
}

// deleteWorkflowCmd deletes a workflow
var deleteWorkflowCmd = newDeleteWorkflowCmd()

func newDeleteWorkflowCmd() *cobra.Command {
	var forceDelete bool
	c := &cobra.Command{
		Use:     "workflow <workflow-id-or-name>",
		Aliases: []string{"workflows", "wf"},
		Short:   "Delete a workflow",
		Long: `Delete a workflow by ID or name.

Examples:
  # Delete by ID
  dtctl delete workflow a1b2c3d4-e5f6-7890-abcd-ef1234567890

  # Delete by name (interactive disambiguation if multiple matches)
  dtctl delete workflow "My Workflow"

  # Delete without confirmation
  dtctl delete workflow "My Workflow" -y
`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			identifier := args[0]

			cfg, err := loadConfig(cmdContext(cmd))
			if err != nil {
				return err
			}

			c, err := newClientFromConfig(cmdContext(cmd), cfg)
			if err != nil {
				return err
			}

			// Resolve name to ID
			res := resolver.NewResolver(c)
			workflowID, err := res.ResolveID(resolver.TypeWorkflow, identifier)
			if err != nil {
				return err
			}

			handler := workflow.NewHandler(c)

			// Get workflow details for confirmation and ownership check
			wf, err := handler.Get(workflowID)
			if err != nil {
				return err
			}

			// Safety check with actual ownership
			currentUserID, _ := c.CurrentUserID()
			ownership := safety.DetermineOwnership(wf.Owner, currentUserID)
			if err := checkSafety(cmdContext(cmd), cfg, safety.OperationDelete, ownership); err != nil {
				return err
			}

			if dryRun(cmdContext(cmd)) {
				return deleteDryRun(cmd, "workflow", wf.Title, workflowID)
			}

			// Confirm deletion unless --force or --plain
			if !forceDelete && !plainMode(cmdContext(cmd)) {
				if !prompt.ConfirmDeletionWith(currentStdin(cmdContext(cmd)), currentStdout(cmdContext(cmd)), "workflow", wf.Title, workflowID) {
					fmt.Fprintln(currentStdout(cmdContext(cmd)), "Deletion cancelled")
					return nil
				}
			}

			if err := handler.Delete(workflowID); err != nil {
				return err
			}

			// In agent mode, output structured response
			if agentMode(cmdContext(cmd)) {
				printer := newPrinterCtx(cmdContext(cmd))
				ap := enrichAgent(printer, "delete", "workflow")
				if ap != nil {
					ap.SetSuggestions([]string{
						"Deleted. Verify with 'dtctl get workflows'",
					})
				}
				return printer.Print(map[string]string{
					"id":     workflowID,
					"title":  wf.Title,
					"status": "deleted",
				})
			}

			output.FprintSuccess(currentStderr(cmdContext(cmd)), "Workflow %q deleted", wf.Title)
			return nil
		},
	}
	c.Flags().BoolVarP(&forceDelete, "yes", "y", false, "Skip confirmation prompt")
	stability.MarkStable(c)
	return c
}

// parseExecTime parses a date string as YYYY-MM-DD or ISO 8601 and returns RFC3339.
// When endOfDay is true and input is date-only, the time is set to 23:59:59.
func parseExecTime(s string, endOfDay bool) (string, error) {
	if s == "" {
		return "", nil
	}
	// Common ISO 8601 date-time forms (with/without seconds, with/without zone).
	for _, layout := range []string{
		time.RFC3339,             // 2006-01-02T15:04:05Z07:00
		"2006-01-02T15:04Z07:00", // seconds omitted, with zone
		"2006-01-02T15:04:05",    // no zone (treated as UTC)
		"2006-01-02T15:04",       // no seconds, no zone
	} {
		if t, err := time.Parse(layout, s); err == nil {
			return t.UTC().Format(time.RFC3339), nil
		}
	}
	// A duration ago ("7d", "12h"), as --from takes it elsewhere.
	if d, err := parseAgo(s); err == nil {
		return time.Now().Add(-d).UTC().Format(time.RFC3339), nil
	}
	// Fall back to date-only
	t, err := time.Parse("2006-01-02", s)
	if err != nil {
		return "", fmt.Errorf("use a duration ago (7d, 12h), YYYY-MM-DD or ISO 8601 (e.g. 2006-01-02T15:04:05Z)")
	}
	if endOfDay {
		t = t.Add(23*time.Hour + 59*time.Minute + 59*time.Second)
	}
	return t.UTC().Format(time.RFC3339), nil
}

// Declared stable: the invocation and output contract of these commands is
// additive-only. Stable is never implied -- see AGENTS.md "Stability Tiers".

// executionsCapAdvice explains a truncated listing; at MaxExecutionLimit raising --limit cannot help.
func executionsCapAdvice(shown, total int, limit int64) []string {
	if limit > 0 && limit < workflow.MaxExecutionLimit {
		return []string{fmt.Sprintf("Showing %d of %d. Raise --limit (currently %d, at most %d) or narrow the window with --started-since/--state.", shown, total, limit, workflow.MaxExecutionLimit)}
	}
	return []string{fmt.Sprintf("Showing %d of %d: the listing returns at most %d executions, so counts taken from it are incomplete; narrow the window with --started-since/--state.", shown, total, workflow.MaxExecutionLimit)}
}

// parseAgo reads a duration ago: a Go duration (12h, 90m) or whole days (7d).
func parseAgo(s string) (time.Duration, error) {
	if n, ok := strings.CutSuffix(s, "d"); ok {
		days, err := strconv.Atoi(n)
		if err != nil || days <= 0 {
			return 0, fmt.Errorf("invalid duration %q", s)
		}
		return time.Duration(days) * 24 * time.Hour, nil
	}
	d, err := time.ParseDuration(s)
	if err != nil || d <= 0 {
		return 0, fmt.Errorf("invalid duration %q", s)
	}
	return d, nil
}
