package cmd

import (
	"context"
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/dynatrace-oss/dtctl/pkg/resources/analyzer"
	"github.com/dynatrace-oss/dtctl/pkg/stability"
)

// verifyAnalyzerCmd validates an analyzer input without executing the analyzer.
var verifyAnalyzerCmd = newVerifyAnalyzerCmd()

func newVerifyAnalyzerCmd() *cobra.Command {
	c := &cobra.Command{
		Use:     "analyzer <name>",
		Aliases: []string{"az"},
		Short:   "Validate a Davis analyzer input without executing it",
		Long: `Validate an input for a Davis AI analyzer without executing it.

This calls the analyzer's validate endpoint and reports whether the input is
accepted, using the same input flags as 'exec analyzer' (--file/--input/--query).
It is the read-only counterpart to 'exec analyzer --validate' and follows the
standard 'verify' exit-code contract, so it fits CI/CD pipelines.

Exit Codes:
  0 - Input is valid
  1 - Input is invalid (validation errors)
  2 - Authentication/permission error
  3 - Network/server error

Examples:
  # Validate input from a file
  dtctl verify analyzer dt.statistics.GenericForecastAnalyzer -f input.json

  # Validate inline JSON
  dtctl verify analyzer dt.statistics.GenericForecastAnalyzer --input '{"timeSeriesData":"timeseries avg(dt.host.cpu.usage)"}'

  # Validate a DQL query shorthand
  dtctl verify analyzer dt.statistics.GenericForecastAnalyzer --query "timeseries avg(dt.host.cpu.usage)"

  # Structured output
  dtctl verify analyzer dt.statistics.GenericForecastAnalyzer -f input.json -o json
`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]

			// Reject unsupported output formats up front (same set as "verify
			// query"), so callers get a clear error rather than a silent fallback
			// to the human verdict.
			outputFmt, _ := cmd.Flags().GetString("output")
			if !isSupportedVerifyOutputFormat(outputFmt) {
				return fmt.Errorf("unsupported output format %q for verify analyzer (supported: json, yaml, toon)", outputFmt)
			}

			input, err := buildAnalyzerInput(cmd)
			if err != nil {
				return err
			}

			_, c, err := setupClient(cmdContext(cmd))
			if err != nil {
				return err
			}

			handler := analyzer.NewHandler(c)

			result, err := handler.Validate(name, input)
			exitCode := getAnalyzerValidateExitCode(result, err)

			// Network/auth/API error: emit and exit with the mapped code
			// (getAnalyzerValidateExitCode always returns non-zero when err != nil).
			if err != nil {
				fmt.Fprintln(currentStderr(cmdContext(cmd)), err)
				return &silentExitError{code: exitCode, reason: err.Error()}
			}

			switch outputFmt {
			case "json", "yaml", "yml", "toon":
				printer := newPrinter(cmdContext(cmd), outputFmt)
				if err := printer.Print(result); err != nil {
					return err
				}
			default:
				formatAnalyzerValidateHuman(cmdContext(cmd), name, result)
			}

			if exitCode != 0 {
				// Verdict already printed; the code encodes the failure class.
				return &silentExitError{code: exitCode, reason: "analyzer validation failed"}
			}
			return nil
		},
	}
	stability.MarkStable(c)
	addAnalyzerInputFlags(c)
	return c
}

// getAnalyzerValidateExitCode maps a validate outcome to the verify exit-code
// contract shared with "verify query".
func getAnalyzerValidateExitCode(result *analyzer.ValidationResult, err error) int {
	if err != nil {
		errMsg := err.Error()
		if strings.Contains(errMsg, "status 401") || strings.Contains(errMsg, "status 403") {
			return 2
		}
		if strings.Contains(errMsg, "status 5") ||
			strings.Contains(errMsg, "timeout") ||
			strings.Contains(errMsg, "connection") {
			return 3
		}
		return 1
	}
	if result == nil || !result.Valid {
		return 1
	}
	return 0
}

// formatAnalyzerValidateHuman prints the validation verdict in human-readable form.
func formatAnalyzerValidateHuman(ctx context.Context, name string, result *analyzer.ValidationResult) {
	useColor := isStderrTerminal()
	if result != nil && result.Valid {
		if useColor {
			fmt.Fprintf(currentStderr(ctx), "%s✔%s Input is valid for %s\n", colorGreen, colorReset, name)
		} else {
			fmt.Fprintf(currentStderr(ctx), "✔ Input is valid for %s\n", name)
		}
		return
	}

	if useColor {
		fmt.Fprintf(currentStderr(ctx), "%s✖%s Input is invalid for %s\n", colorRed, colorReset, name)
	} else {
		fmt.Fprintf(currentStderr(ctx), "✖ Input is invalid for %s\n", name)
	}
	if result != nil {
		for k, v := range result.Details {
			fmt.Fprintf(currentStderr(ctx), "  - %s: %v\n", k, v)
		}
	}
}

func init() {
	verifyCmd.AddCommand(verifyAnalyzerCmd)
}

// Declared stable: the invocation and output contract of this command is
// additive-only. Stable is never implied -- see AGENTS.md "Stability Tiers".
