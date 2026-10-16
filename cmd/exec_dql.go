package cmd

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/dynatrace-oss/dtctl/pkg/exec"
	"github.com/dynatrace-oss/dtctl/pkg/output"
	"github.com/dynatrace-oss/dtctl/pkg/safety"
	"github.com/dynatrace-oss/dtctl/pkg/stability"
)

// execDQLCmd executes a DQL query (DEPRECATED)
// A hidden alias for `query`, hidden from help but not from callers, so it
// declares the same contract `query` does.
var execDQLCmd = newExecDQLCmd()

func newExecDQLCmd() *cobra.Command {
	c := &cobra.Command{
		Use:    "dql [query]",
		Short:  "Execute a DQL query (DEPRECATED: use 'dtctl query')",
		Hidden: true, // Hide from help output
		Long: `Execute a DQL query against Grail storage.

DEPRECATED: This command is deprecated. Use 'dtctl query' instead.
The 'dtctl query' command provides the same functionality with additional
features like template variables.

Examples:
  # Execute inline query (use 'dtctl query' instead)
  dtctl query "fetch logs | limit 10"

  # Execute from file (use 'dtctl query -f' instead)
  dtctl query -f query.dql

  # Output as JSON (use 'dtctl query -o json' instead)
  dtctl query "fetch logs" -o json
`,
		RunE: func(cmd *cobra.Command, args []string) error {
			// Show deprecation warning
			output.FprintWarning(currentStderr(cmdContext(cmd)), "'dtctl exec dql' is deprecated. Use 'dtctl query' instead.")
			cfg, c, err := setupWithSafety(cmdContext(cmd), safety.OperationRead)
			if err != nil {
				return err
			}

			executor := newDQLExecutorFromConfig(cmdContext(cmd), cfg, c)

			queryFile, _ := cmd.Flags().GetString("file")

			if queryFile != "" {
				data, err := readFileFlag(cmdContext(cmd), "file", queryFile)
				if err != nil {
					return fmt.Errorf("failed to read file: %w", err)
				}
				return executor.ExecuteWithContext(cmdContext(cmd), string(data), exec.DQLExecuteOptions{OutputFormat: outputFormat(cmdContext(cmd))})
			}

			if len(args) == 0 {
				return fmt.Errorf("query string or --file is required")
			}

			query := args[0]
			return executor.ExecuteWithContext(cmdContext(cmd), query, exec.DQLExecuteOptions{OutputFormat: outputFormat(cmdContext(cmd))})
		},
	}
	c.Flags().StringP("file", "f", "", "read query from file, or - for stdin")
	stability.MarkStable(c)
	// The query can be the positional argument instead: reject only an
	// explicitly empty --file.
	rejectEmptyFlag(c, "file")
	return c
}

func init() {
	// DQL flags
}

func init() {
}
