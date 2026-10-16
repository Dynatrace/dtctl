package cmd

import (
	"strings"

	"github.com/spf13/cobra"

	"github.com/dynatrace-oss/dtctl/pkg/stability"
)

// isSupportedVerifyOutputFormat reports whether the given output format is
// accepted by the verify subcommands. The verify family emits either a
// human-readable verdict (empty/table) or a structured ValidationResult
// (json/yaml/yml/toon); any other format (csv, wide, charts, …) is rejected so
// the caller gets a clear error instead of a silent fallback. Shared by
// "verify query" and "verify analyzer" so both reject the same set.
func isSupportedVerifyOutputFormat(format string) bool {
	switch strings.ToLower(strings.TrimSpace(format)) {
	case "", "table", "json", "yaml", "yml", "toon":
		return true
	default:
		return false
	}
}

// verifyCmd represents the verify command
var verifyCmd = newVerifyCmd()

func newVerifyCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "verify",
		Short: "Verify resources without executing them",
		Long: `Verify resources without executing them.

The verify command validates resources before execution, checking for syntax errors,
semantic issues, and configuration problems. This is useful for:

- CI/CD pipelines: validate queries before deployment
- Development: catch errors early without executing
- Configuration validation: ensure correctness before applying

Examples:
  # Verify a DQL query from file
  dtctl verify query -f query.dql

  # Verify inline DQL query
  dtctl verify query "fetch logs | summarize count()"

  # Verify an OpenPipeline matcher expression
  dtctl verify openpipeline-matcher 'matchesValue(content, "error")'

  # Verify an OpenPipeline DQL processor script
  dtctl verify openpipeline-dql-processor 'fieldsAdd severity = "INFO"'

  # Verify and fail on warnings (strict mode for CI/CD)
  dtctl verify query -f query.dql --fail-on-warn

Exit Codes:
  0 - Verification successful
  1 - Verification failed (errors found)
  2 - Authentication/permission error
  3 - Network/server error

Use "dtctl verify <command> --help" for more information about a command.
`,
	}
	stability.MarkStable(c)
	return c
}

func init() {
	rootCmd.AddCommand(verifyCmd)
	verifyCmd.AddCommand(verifyOpenPipelineMatcherCmd)
	verifyCmd.AddCommand(verifyOpenPipelineDQLProcessorCmd)
}

// Declared stable: the invocation and output contract of this command is
// additive-only. Stable is never implied -- see AGENTS.md "Stability Tiers".
func init() {
}
