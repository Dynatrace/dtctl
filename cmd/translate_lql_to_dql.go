package cmd

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/dynatrace-oss/dtctl/pkg/resources/appengine"
	"github.com/dynatrace-oss/dtctl/pkg/resources/matcherlqltodql"
	"github.com/dynatrace-oss/dtctl/pkg/stability"
)

var translateLqlToDqlCmd = newTranslateLqlToDqlCmd()

func newTranslateLqlToDqlCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "lql-to-dql [lql-expression]",
		Short: "Translate an LQL matcher expression into a DQL matcher expression",
		Long: `Translate a single LQL matcher expression into its semantically equivalent
DQL matcher expression using the OpenPipeline translation endpoint.

The translated DQL expression is returned verbatim. By default it is printed as
plain text, ready to copy-paste or pipe into another command. Use -o json or
-o yaml for structured output; -o table for a single-row table.

The LQL expression can be supplied as a positional argument or read from a file
(or stdin with "-"). Exactly one of the two forms must be given.

Required OAuth scope: openpipeline:configurations:read

Examples:
  # Translate an inline LQL expression
  dtctl translate lql-to-dql 'log.source="snmptraps" AND snmp.trap_oid="F5-BIGIP-COMMON-MIB"'

  # Read the LQL expression from a file
  dtctl translate lql-to-dql -f matcher.lql

  # Read from stdin
  echo 'log.source="snmptraps"' | dtctl translate lql-to-dql -f -

  # Output as JSON
  dtctl translate lql-to-dql 'log.source="snmptraps"' -o json

  # Use in agent mode
  dtctl translate lql-to-dql 'log.source="snmptraps"' -A`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			filePath, _ := cmd.Flags().GetString("file")

			hasArg := len(args) == 1
			hasFile := filePath != ""

			if hasArg && hasFile {
				return fmt.Errorf("provide either a positional LQL expression or --file, not both")
			}
			if !hasArg && !hasFile {
				return fmt.Errorf("provide an LQL expression as an argument or via --file (use \"-\" for stdin)")
			}

			var lql string
			if hasFile {
				content, err := appengine.ReadFileOrStdinWith(vfsEnv(cmdContext(cmd)), filePath)
				if err != nil {
					return fmt.Errorf("read LQL expression: %w", err)
				}
				lql = strings.TrimSpace(content)
			} else {
				lql = args[0]
			}

			_, c, printer, err := setup(cmdContext(cmd))
			if err != nil {
				return err
			}

			handler := matcherlqltodql.NewHandler(c)
			result, err := handler.Translate(lql)
			if err != nil {
				return err
			}

			ap := enrichAgent(printer, "translate", "lql-to-dql")
			if ap != nil {
				ap.SetSuggestions([]string{
					"Exchange your LQL matcher with the translated DQL matcher",
				})
				return printer.Print(result)
			}

			// Use .Changed (not outputFormat) to distinguish "user passed -o" from
			// "defaulted to table" — outputFormat defaults to "table", so checking
			// its value alone can't tell whether the user asked for it explicitly.
			outputFlag := cmd.Root().PersistentFlags().Lookup("output")
			if outputFlag != nil && outputFlag.Changed {
				return printer.Print(result)
			}
			_, err = fmt.Fprintln(currentStdout(cmdContext(cmd)), result.Query)
			return err
		},
	}
	c.Flags().StringP("file", "f", "", `Read the LQL expression from a file ("-" for stdin)`)
	stability.MarkStable(c)
	return c
}

func init() {
}

// Declared stable: the invocation and output contract of this command is
// additive-only. Stable is never implied -- see AGENTS.md "Stability Tiers".
func init() {
}
