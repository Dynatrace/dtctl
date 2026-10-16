package cmd

import (
	"context"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/dynatrace-oss/dtctl/pkg/client"
	"github.com/dynatrace-oss/dtctl/pkg/exec"
	"github.com/dynatrace-oss/dtctl/pkg/output"
	"github.com/dynatrace-oss/dtctl/pkg/prompt"
	"github.com/dynatrace-oss/dtctl/pkg/resources/lookup"
	"github.com/dynatrace-oss/dtctl/pkg/safety"
	"github.com/dynatrace-oss/dtctl/pkg/stability"
)

// getLookupsCmd retrieves lookup tables
var getLookupsCmd = newGetLookupsCmd()

func newGetLookupsCmd() *cobra.Command {
	c := &cobra.Command{
		Use:     "lookups [path]",
		Aliases: []string{"lookup", "lkup", "lu"},
		Short:   "Get lookup tables",
		Long: `Get lookup tables from Grail Resource Store.

Lookup tables are tabular files stored in Grail that can be loaded and
joined with observability data in DQL queries for data enrichment.

Output formats:
  - List view (no path): Shows lookup table metadata (path, size, records, etc.)
  - Table output (-o table): Shows the actual lookup table data as a table
  - YAML output (-o yaml): Shows both metadata and full data
  - CSV/JSON output: Shows the lookup table data only

Examples:
  # List all lookup tables (shows metadata)
  dtctl get lookups

  # View lookup table data as a table (default)
  dtctl get lookup /lookups/grail/pm/error_codes

  # View with metadata included
  dtctl get lookup /lookups/grail/pm/error_codes -o yaml

  # Export lookup data as CSV
  dtctl get lookup /lookups/grail/pm/error_codes -o csv > error_codes.csv

  # Export lookup data as JSON
  dtctl get lookup /lookups/grail/pm/error_codes -o json

  # List all lookups with additional columns
  dtctl get lookups -o wide
`,
		RunE: func(cmd *cobra.Command, args []string) error {
			_, c, printer, err := setup(cmdContext(cmd))
			if err != nil {
				return err
			}

			handler := lookup.NewHandler(c)

			// Get specific lookup if path provided
			if len(args) > 0 {
				// For table output, show the actual lookup table data (not metadata)
				if outputFormat(cmdContext(cmd)) == "table" || outputFormat(cmdContext(cmd)) == "wide" {
					dataResult, err := handler.GetData(args[0], 0)
					if err != nil {
						return err
					}
					printLookupNotifications(cmdContext(cmd), c, dataResult.Notifications)
					return printer.PrintList(dataResult.Records)
				}

				// For CSV/JSON output, return full data
				if outputFormat(cmdContext(cmd)) == "csv" || outputFormat(cmdContext(cmd)) == "json" {
					dataResult, err := handler.GetData(args[0], 0)
					if err != nil {
						return err
					}
					printLookupNotifications(cmdContext(cmd), c, dataResult.Notifications)
					return printer.PrintList(dataResult.Records)
				}

				// For YAML output, return full structure (metadata + data)
				lookupData, notifications, err := handler.GetWithData(args[0], 0)
				if err != nil {
					return err
				}
				printLookupNotifications(cmdContext(cmd), c, notifications)
				return printer.Print(lookupData)
			}

			// List all lookups
			list, err := handler.List()
			if err != nil {
				return err
			}

			return printer.PrintList(list)
		},
	}
	stability.MarkStable(c)
	return c
}

// deleteLookupCmd deletes a lookup table
var deleteLookupCmd = newDeleteLookupCmd()

func newDeleteLookupCmd() *cobra.Command {
	var forceDelete bool
	c := &cobra.Command{
		Use:     "lookup <path>",
		Aliases: []string{"lookups", "lkup", "lu"},
		Short:   "Delete a lookup table",
		Long: `Delete a lookup table from Grail Resource Store.

ATTENTION: This operation is irreversible and will permanently delete the lookup table.

Examples:
  # Delete a lookup table
  dtctl delete lookup /lookups/grail/pm/error_codes

  # Delete without confirmation
  dtctl delete lookup /lookups/grail/pm/error_codes -y
`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			path := args[0]

			_, c, err := setupWithSafety(cmdContext(cmd), safety.OperationDelete)
			if err != nil {
				return err
			}

			handler := lookup.NewHandler(c)

			// Get lookup for confirmation
			lu, err := handler.Get(path)
			if err != nil {
				return err
			}

			if dryRun(cmdContext(cmd)) {
				return deleteDryRun(cmd, "lookup table", lu.DisplayName, path)
			}

			// Confirm deletion unless --force or --plain
			if !forceDelete && !plainMode(cmdContext(cmd)) {
				displayName := lu.DisplayName
				if displayName == "" {
					displayName = path
				}
				if !prompt.ConfirmDeletionWith(currentStdin(cmdContext(cmd)), currentStdout(cmdContext(cmd)), "lookup table", displayName, path) {
					fmt.Fprintln(currentStdout(cmdContext(cmd)), "Deletion cancelled")
					return nil
				}
			}

			if err := handler.Delete(path); err != nil {
				return err
			}

			output.FprintSuccess(currentStderr(cmdContext(cmd)), "Lookup table %q deleted", path)
			return nil
		},
	}
	c.Flags().BoolVarP(&forceDelete, "yes", "y", false, "Skip confirmation prompt")
	stability.MarkStable(c)
	return c
}

func init() {
	// Delete confirmation flags
}

// printLookupNotifications surfaces DQL query notifications (e.g., truncation warnings) to stderr
func printLookupNotifications(ctx context.Context, c *client.Client, notifications []exec.QueryNotification) {
	if len(notifications) == 0 {
		return
	}
	executor := newDQLExecutor(ctx, c)
	executor.PrintNotifications(notifications)
}

// Declared stable: the invocation and output contract of these commands is
// additive-only. Stable is never implied -- see AGENTS.md "Stability Tiers".
func init() {
}
