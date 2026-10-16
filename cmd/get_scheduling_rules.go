package cmd

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/dynatrace-oss/dtctl/pkg/output"
	"github.com/dynatrace-oss/dtctl/pkg/prompt"
	"github.com/dynatrace-oss/dtctl/pkg/resources/schedulingrule"
	"github.com/dynatrace-oss/dtctl/pkg/safety"
	"github.com/dynatrace-oss/dtctl/pkg/stability"
)

// getSchedulingRulesCmd retrieves scheduling rules
var getSchedulingRulesCmd = newGetSchedulingRulesCmd()

func newGetSchedulingRulesCmd() *cobra.Command {
	c := &cobra.Command{
		Use:     "scheduling-rules [id]",
		Aliases: []string{"scheduling-rule", "sr"},
		Short:   "Get scheduling rules",
		Long: `Get one or more scheduling rules.

Examples:
  # List all scheduling rules
  dtctl get scheduling-rules

  # Get a specific scheduling rule
  dtctl get scheduling-rule <scheduling-rule-id>

  # Output as JSON
  dtctl get scheduling-rules -o json
`,
		RunE: func(cmd *cobra.Command, args []string) error {
			_, c, printer, err := setup(cmdContext(cmd))
			if err != nil {
				return err
			}

			handler := schedulingrule.NewHandler(c)
			ap := enrichAgent(printer, "get", "scheduling-rule")

			if len(args) > 0 {
				rule, err := handler.Get(args[0])
				if err != nil {
					return err
				}
				if ap != nil {
					ap.SetSuggestions([]string{
						fmt.Sprintf("Run 'dtctl describe scheduling-rule %s' for details", args[0]),
					})
				}
				return printer.Print(rule)
			}

			limit, _ := cmd.Flags().GetInt64("limit")
			limit = agentPageLimit(cmd, limit)
			chunk := getChunkSize(cmdContext(cmd))
			if err := validateAutomationChunkSize(chunk); err != nil {
				return err
			}

			watchMode, _ := cmd.Flags().GetBool("watch")
			if watchMode {
				fetcher := func() (interface{}, error) {
					list, err := handler.List(chunk, limit)
					if err != nil {
						return nil, err
					}
					return list.Results, nil
				}
				return executeWithWatch(cmd, fetcher, printer)
			}

			list, err := handler.List(chunk, limit)
			if err != nil {
				return err
			}

			if ap != nil {
				// The server's count, not the page fetched: a --limit (or the
				// agent-mode default page) makes the page smaller than the list.
				ap.SetTotal(max(list.Count, len(list.Results)))
				suggestions := []string{
					"Run 'dtctl describe scheduling-rule <id>' for details",
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
	c.Flags().Int64("limit", 0, "Maximum number of scheduling rules to return (0 = unlimited)")
	stability.MarkStable(c)
	addWatchFlags(c)
	return c
}

// deleteSchedulingRuleCmd deletes a scheduling rule
var deleteSchedulingRuleCmd = newDeleteSchedulingRuleCmd()

func newDeleteSchedulingRuleCmd() *cobra.Command {
	var forceDelete bool
	c := &cobra.Command{
		Use:     "scheduling-rule <id>",
		Aliases: []string{"scheduling-rules", "sr"},
		Short:   "Delete a scheduling rule",
		Long: `Delete a scheduling rule by ID.

Examples:
  # Delete by ID
  dtctl delete scheduling-rule a1b2c3d4-e5f6-7890-abcd-ef1234567890

  # Delete without confirmation
  dtctl delete scheduling-rule a1b2c3d4-e5f6-7890-abcd-ef1234567890 -y
`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			id := args[0]

			cfg, err := loadConfig(cmdContext(cmd))
			if err != nil {
				return err
			}

			c, err := newClientFromConfig(cmdContext(cmd), cfg)
			if err != nil {
				return err
			}

			handler := schedulingrule.NewHandler(c)

			// Get rule details for confirmation and ownership check.
			rule, err := handler.Get(id)
			if err != nil {
				return err
			}

			// Safety check with actual ownership.
			currentUserID, _ := c.CurrentUserID()
			ownership := safety.DetermineOwnership(rule.OwnerID(), currentUserID)
			if err := checkSafety(cmdContext(cmd), cfg, safety.OperationDelete, ownership); err != nil {
				return err
			}

			// Confirm deletion unless --yes or --plain.
			if !forceDelete && !plainMode(cmdContext(cmd)) {
				if !prompt.ConfirmDeletionWith(currentStdin(cmdContext(cmd)), currentStdout(cmdContext(cmd)), "scheduling-rule", rule.Title, id) {
					fmt.Fprintln(currentStdout(cmdContext(cmd)), "Deletion cancelled")
					return nil
				}
			}

			if dryRun(cmdContext(cmd)) {
				if agentMode(cmdContext(cmd)) {
					printer := newPrinterCtx(cmdContext(cmd))
					ap := enrichAgent(printer, "delete", "scheduling-rule")
					if ap != nil {
						ap.SetSuggestions([]string{"Remove --dry-run to delete the scheduling rule"})
					}
					return printer.Print(map[string]string{
						"id":     id,
						"title":  rule.Title,
						"status": "dry-run",
					})
				}
				output.FprintInfo(currentStderr(cmdContext(cmd)), "Dry run: would delete scheduling rule %q (%s)", rule.Title, id)
				return nil
			}

			if err := handler.Delete(id); err != nil {
				return err
			}

			// In agent mode, output structured response.
			if agentMode(cmdContext(cmd)) {
				printer := newPrinterCtx(cmdContext(cmd))
				ap := enrichAgent(printer, "delete", "scheduling-rule")
				if ap != nil {
					ap.SetSuggestions([]string{
						"Deleted. Verify with 'dtctl get scheduling-rules'",
					})
				}
				return printer.Print(map[string]string{
					"id":     id,
					"title":  rule.Title,
					"status": "deleted",
				})
			}

			output.FprintSuccess(currentStderr(cmdContext(cmd)), "Scheduling rule %q deleted", rule.Title)
			return nil
		},
	}
	c.Flags().BoolVarP(&forceDelete, "yes", "y", false, "Skip confirmation prompt")
	stability.MarkStable(c)
	return c
}

// Declared stable: the invocation and output contract of these commands is
// additive-only. Stable is never implied -- see AGENTS.md "Stability Tiers".
