package cmd

import (
	"context"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/dynatrace-oss/dtctl/pkg/output"
	resapi "github.com/dynatrace-oss/dtctl/pkg/resources/api"
	"github.com/dynatrace-oss/dtctl/pkg/stability"
)

// getAPIsCmd lists the APIs the environment publishes specifications for.
//
// The listing mirrors the environment's own API index — the same document its
// Swagger UI reads — and neither adds nor filters entries. What is on it is a
// property of the environment, not of dtctl.
var getAPIsCmd = newGetAPIsCmd()

func newGetAPIsCmd() *cobra.Command {
	c := &cobra.Command{
		Use:     "apis",
		Aliases: []string{"api"},
		Short:   "List the APIs this environment publishes specifications for",
		Long: `List the APIs this environment publishes OpenAPI specifications for.

The list comes from the environment's own API index, so it shows exactly what
that environment publishes — dtctl neither adds nor hides entries.

The DTCTL column names the dtctl resource that already wraps an API. A blank
means there is no native command for it yet; --uncovered filters to those, which
makes the gap between the platform and dtctl visible and actionable.

Drill down with 'dtctl describe api <name>' for an API's operations, and call an
operation with 'dtctl exec api <path>'.

Examples:
  # What does this environment publish?
  dtctl get apis

  # Which of those has no native dtctl command yet?
  dtctl get apis --uncovered

  # Include operation counts and categories (one request per API)
  dtctl get apis --ops-count -o wide

  # Structured output
  dtctl get apis -o json
`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			_, c, printer, err := setup(cmdContext(cmd))
			if err != nil {
				return err
			}

			uncovered, _ := cmd.Flags().GetBool("uncovered")
			opsCount, _ := cmd.Flags().GetBool("ops-count")

			handler := resapi.NewHandler(c)
			rows, err := handler.List(resapi.ListOptions{Uncovered: uncovered, OpsCount: opsCount})
			if err != nil {
				return err
			}

			ap := enrichAgent(printer, "get", "apis")
			if ap != nil {
				ap.Context().Suggestions = []string{
					"dtctl describe api <name>  -- list an API's operations",
					"dtctl describe api <name> --operation 'GET /path'  -- one operation in full",
					"dtctl get apis --uncovered  -- APIs with no native dtctl command",
				}
			}
			warnUnreadableSpecs(cmdContext(cmd), ap, rows)

			return printer.PrintList(rows)
		},
	}
	c.Flags().Bool("uncovered", false, "only APIs with no native dtctl command")
	c.Flags().Bool("ops-count", false, "fetch every specification to fill in operation counts and categories (one request per API)")
	stability.MarkStable(c)
	return c
}

// warnUnreadableSpecs surfaces rows whose specification could not be read. The
// count goes to the caller rather than the log: a blank OPS cell otherwise looks
// like "zero operations", and the per-row reason is in the structured output.
func warnUnreadableSpecs(ctx context.Context, ap *output.AgentPrinter, rows []resapi.APIInfo) {
	failed := 0
	for _, r := range rows {
		if r.SpecError != "" {
			failed++
		}
	}
	if failed == 0 {
		return
	}

	const format = "%d of %d specifications could not be read; those rows have no operation count (use -o json to see spec_error)"
	if ap != nil {
		ap.Context().Warnings = append(ap.Context().Warnings,
			fmt.Sprintf(format, failed, len(rows)))
		return
	}
	output.FprintWarning(currentStderr(ctx), format, failed, len(rows))
}

func init() {
}

// Declared stable: the invocation and output contract of this command is
// additive-only. Stable is never implied -- see AGENTS.md "Stability Tiers".
func init() {
}
