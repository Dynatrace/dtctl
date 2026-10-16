package cmd

import (
	"context"
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/dynatrace-oss/dtctl/pkg/client"
	"github.com/dynatrace-oss/dtctl/pkg/resources/matcherverify"
	"github.com/dynatrace-oss/dtctl/pkg/stability"
)

// verifyOpenPipelineMatcherCmd validates a DQL matcher expression against the
// OpenPipeline matcher verify endpoint.
var verifyOpenPipelineMatcherCmd = newVerifyOpenPipelineMatcherCmd()

func newVerifyOpenPipelineMatcherCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "openpipeline-matcher [matcher-expression]",
		Args:  cobra.MaximumNArgs(1),
		Short: "Verify an OpenPipeline DQL matcher expression",
		Long: `Verify an OpenPipeline DQL matcher expression without applying it.

The command sends the matcher expression to the OpenPipeline matcher verify
endpoint and reports whether it is valid. Diagnostics (errors, warnings) are
printed with their source positions when available.

The matcher can be provided as a positional argument or read from a file (use
"-" for stdin). Exactly one of these must be supplied.

Exit codes:
  0 - Matcher is valid
  1 - Matcher is invalid, or the command failed (auth, network, or server error)

Examples:
  # Verify inline matcher expression
  dtctl verify openpipeline-matcher 'matchesValue(content, "error")'

  # Read matcher from a file
  dtctl verify openpipeline-matcher -f matcher.dql

  # Read matcher from stdin
  echo 'matchesValue(content, "error")' | dtctl verify openpipeline-matcher -f -

  # Verify with a stage context
  dtctl verify openpipeline-matcher 'matchesValue(content, "error")' --context ROUTING_RULE

  # Verify scoped to a specific configuration
  dtctl verify openpipeline-matcher 'matchesValue(content, "error")' --config-id logs

  # Get structured output
  dtctl verify openpipeline-matcher 'matchesValue(content, "error")' -o json
  dtctl verify openpipeline-matcher 'matchesValue(content, "error")' -o yaml`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if !isSupportedVerifyOutputFormat(outputFormat(cmdContext(cmd))) {
				return fmt.Errorf("unsupported output format %q for verify openpipeline-matcher (supported: json, yaml, toon)", outputFormat(cmdContext(cmd)))
			}

			fileFlag, _ := cmd.Flags().GetString("file")
			contextFlag, _ := cmd.Flags().GetString("context")
			configIDFlag, _ := cmd.Flags().GetString("config-id")

			// Exactly one of: positional arg or --file
			hasArg := len(args) > 0
			hasFile := fileFlag != ""
			if hasArg && hasFile {
				return fmt.Errorf("provide either a positional matcher expression or --file, not both")
			}
			if !hasArg && !hasFile {
				return fmt.Errorf("a matcher expression or --file is required")
			}

			var query string
			if hasFile {
				content, err := readVerifyExpressionFromFile(cmdContext(cmd), fileFlag)
				if err != nil {
					return err
				}
				query = content
			} else {
				query = args[0]
			}

			_, c, printer, err := setup(cmdContext(cmd))
			if err != nil {
				return err
			}

			handler := matcherverify.NewHandler(c)
			result, err := handler.Verify(matcherverify.VerifyOptions{
				Query:           query,
				ConfigurationID: configIDFlag,
				Context:         contextFlag,
			})
			if err != nil {
				return err
			}

			ap := enrichAgent(printer, "verify", "openpipeline-matcher")

			// Agent mode, an explicit -o format, or a --jq filter: delegate to the
			// printer (agent envelope carrying valid/notifications, or
			// json/yaml/table/wide/csv/toon with jq applied). Otherwise emit the
			// default human-readable summary.
			if ap != nil || cmd.Root().PersistentFlags().Lookup("output").Changed || jqFilter(cmdContext(cmd)) != "" {
				if err := printer.Print(result); err != nil {
					return err
				}
			} else {
				printVerifyResultHuman(cmdContext(cmd), result)
			}

			// A false verdict is a successful API call that must still exit non-zero
			// in every mode. silentExitError is intercepted in root.go before the
			// agent error-envelope path, so the ok:true envelope printed above stands
			// as the sole output and the process still exits with ExitError.
			if !result.Valid {
				return &silentExitError{code: client.ExitError, reason: "verification failed"}
			}
			return nil
		},
	}
	c.Flags().StringP("file", "f", "", `read the matcher from a file ("-" for stdin)`)
	c.Flags().String("context", "", `stage context, e.g. "processing" or "ROUTING_RULE"`)
	stability.MarkFlag(c, "context", stability.Experimental, pre10Since)
	c.Flags().String("config-id", "", `configuration scope, e.g. "logs"`)
	stability.MarkStable(c)
	rejectEmptyFlag(c, "file")
	return c
}

// printVerifyResultHuman prints a verify verdict and its diagnostics in the same
// human-readable style as "verify query": the verdict is written to stderr
// (colored when stderr is a terminal) so redirecting the command's stdout keeps
// the verdict visible. Shared by the matcher and DQL-processor verify commands,
// whose VerifyResult is the same type.
func printVerifyResultHuman(ctx context.Context, result *matcherverify.VerifyResult) {
	useColor := isStderrTerminal()
	switch {
	case result.Valid && useColor:
		fmt.Fprintf(currentStderr(ctx), "%s✔%s Valid\n", colorGreen, colorReset)
	case result.Valid:
		fmt.Fprintln(currentStderr(ctx), "✔ Valid")
	case useColor:
		fmt.Fprintf(currentStderr(ctx), "%s✖%s Invalid\n", colorRed, colorReset)
	default:
		fmt.Fprintln(currentStderr(ctx), "✖ Invalid")
	}
	if result.Summary != "" {
		for _, line := range strings.Split(result.Summary, "\n") {
			fmt.Fprintf(currentStderr(ctx), "  %s\n", line)
		}
	}
}

// readVerifyExpressionFromFile reads a text expression from a user-supplied
// path ("-" for stdin) through the vfs seam, so an embedded invocation reads
// the request's virtual files rather than the host disk.
func readVerifyExpressionFromFile(ctx context.Context, path string) (string, error) {
	content, err := readFileFlag(ctx, "file", path)
	if err != nil {
		return "", fmt.Errorf("read expression from %q: %w", path, err)
	}
	return string(content), nil
}

func init() {
	// Renamed or removed in 1.0 because it hides a global flag
	// (contrib breaking-changes/unshadow-global-flags.md).
}

// Declared stable: the invocation and output contract of this command is
// additive-only. Stable is never implied -- see AGENTS.md "Stability Tiers".
func init() {
}
