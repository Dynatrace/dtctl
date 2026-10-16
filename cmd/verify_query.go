package cmd

import (
	"context"
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/dynatrace-oss/dtctl/pkg/exec"
	"github.com/dynatrace-oss/dtctl/pkg/stability"
	"github.com/dynatrace-oss/dtctl/pkg/util/template"
)

// verifyQueryCmd represents the verify query subcommand
var verifyQueryCmd = newVerifyQueryCmd()

func newVerifyQueryCmd() *cobra.Command {
	c := &cobra.Command{
		Use:     "query [dql-string]",
		Aliases: []string{"q"},
		Short:   "Verify a DQL query without executing it",
		Long: `Verify a DQL query without executing it against Grail storage.

This command validates query syntax, checks for errors and warnings, and optionally 
returns the canonical representation of the query. This is useful for testing queries 
in CI/CD pipelines or checking query correctness before execution.

The verify command returns different exit codes based on the result:
  0 - Query is valid
  1 - Query is invalid or has errors (or warnings with --fail-on-warn)
  2 - Authentication/permission error
  3 - Network/server error

DQL (Dynatrace Query Language) queries can be verified inline or from a file.
Template variables can be used with the --set flag for reusable queries.

Template Syntax:
  Use {{.variable}} to reference variables.
  Use {{.variable | default "value"}} for default values.

Examples:
  # Verify inline query
  dtctl verify query "fetch logs | limit 10"

  # Verify query from file
  dtctl verify query -f query.dql

  # Read from stdin (recommended for complex queries)
  dtctl verify query -f - <<'EOF'
  fetch logs | filter status == "ERROR"
  EOF

  # Pipe query from file or command
  cat query.dql | dtctl verify query
  echo 'fetch logs | limit 10' | dtctl verify query

  # PowerShell: pipe a here-string in (an argument loses the inner quotes on 5.1)
  @'
  fetch logs, bucket:{"custom-logs"} | filter contains(host.name, "api")
  '@ | dtctl verify query

  # Verify with template variables
  dtctl verify query -f query.dql --set host=h-123 --set timerange=1h

  # Get canonical query representation (normalized format)
  dtctl verify query "fetch logs" --canonical

  # Verify with specific timezone and locale
  dtctl verify query "fetch logs" --timezone "Europe/Paris" --locale "fr_FR"

  # Get structured output (JSON, YAML, or TOON)
  dtctl verify query "fetch logs" -o json
  dtctl verify query "fetch logs" -o yaml
  dtctl verify query "fetch logs" -o toon

  # CI/CD: Fail on warnings (strict validation)
  dtctl verify query -f query.dql --fail-on-warn
  if [ $? -eq 0 ]; then echo "Query is valid"; fi

  # CI/CD: Validate all queries in a directory
  for file in queries/*.dql; do
    echo "Verifying $file..."
    dtctl verify query -f "$file" --fail-on-warn || exit 1
  done

  # CI/CD: Validate query with canonical output
  dtctl verify query -f query.dql --canonical -o json | jq '.canonicalQuery'

  # Pre-commit hook: Verify staged query files
  git diff --cached --name-only --diff-filter=ACM "*.dql" | \
    xargs -I {} dtctl verify query -f {} --fail-on-warn

  # Check exit codes for different scenarios
  dtctl verify query "invalid query syntax"       # Exit 1: syntax error
  dtctl verify query "fetch logs" --fail-on-warn  # Exit 0 or 1 based on warnings

  # Verify query with all options
  dtctl verify query -f query.dql --canonical --timezone "UTC" --locale "en_US" --fail-on-warn

  # Verify template query before execution
  dtctl verify query -f template.dql --set env=prod --set timerange=1h
  dtctl query -f template.dql --set env=prod --set timerange=1h

  # Script usage: check if query is valid before running
  if dtctl verify query -f query.dql --fail-on-warn 2>/dev/null; then
    dtctl query -f query.dql -o csv > results.csv
  else
    echo "Query validation failed"
    exit 1
  fi
`,
		RunE: func(cmd *cobra.Command, args []string) error {
			outputFmt, _ := cmd.Flags().GetString("output")
			if !isSupportedVerifyOutputFormat(outputFmt) {
				return fmt.Errorf("unsupported output format %q for verify query (supported: json, yaml, toon)", outputFmt)
			}

			_, c, err := setupClient(cmdContext(cmd))
			if err != nil {
				return err
			}

			executor := newDQLExecutor(cmdContext(cmd), c)

			queryFile, _ := cmd.Flags().GetString("file")
			setFlags, _ := cmd.Flags().GetStringArray("set")

			query, err := resolveQueryInput(cmdContext(cmd), queryFile, args, invocationStdin(cmdContext(cmd)))
			if err != nil {
				return err
			}

			// Apply template rendering if --set flags are provided
			if len(setFlags) > 0 {
				vars, err := template.ParseSetFlags(setFlags)
				if err != nil {
					return fmt.Errorf("invalid --set flag: %w", err)
				}

				rendered, err := template.RenderTemplate(query, vars)
				if err != nil {
					return fmt.Errorf("template rendering failed: %w", err)
				}

				query = rendered
			}

			// Get verify options
			canonical, _ := cmd.Flags().GetBool("canonical")
			timezone, _ := cmd.Flags().GetString("timezone")
			locale, _ := cmd.Flags().GetString("locale")
			failOnWarn, _ := cmd.Flags().GetBool("fail-on-warn")
			clientContext, _ := cmd.Flags().GetString("client-context")

			opts := exec.DQLVerifyOptions{
				GenerateCanonicalQuery: canonical,
				Timezone:               timezone,
				Locale:                 locale,
				ClientContext:          clientContext,
			}

			// Call VerifyQuery and handle response
			result, err := executor.VerifyQueryWithContext(cmdContext(cmd), query, opts)

			// Get exit code first (needed for all output formats)
			exitCode := getVerifyExitCode(result, err, failOnWarn)

			// Handle errors (network, auth, API)
			if err != nil {
				// Exit with the mapped code; nothing further is printed (the
				// verify contract encodes the failure class in the exit code).
				if exitCode != 0 {
					return &silentExitError{code: exitCode, reason: err.Error()}
				}
				return err
			}

			// Format output based on --output flag (already validated above)
			switch outputFmt {
			case "json":
				// Print full DQLVerifyResponse as JSON
				printer := newPrinter(cmdContext(cmd), "json")
				if err := printer.Print(result); err != nil {
					return fmt.Errorf("failed to print JSON output: %w", err)
				}
			case "yaml", "yml":
				// Print full DQLVerifyResponse as YAML
				printer := newPrinter(cmdContext(cmd), "yaml")
				if err := printer.Print(result); err != nil {
					return fmt.Errorf("failed to print YAML output: %w", err)
				}
			case "toon":
				// Print full DQLVerifyResponse as TOON
				printer := newPrinter(cmdContext(cmd), "toon")
				if err := printer.Print(result); err != nil {
					return fmt.Errorf("failed to print TOON output: %w", err)
				}
			default:
				// Default: human-readable format
				if err := formatVerifyResultHuman(cmdContext(cmd), result, query, canonical); err != nil {
					return fmt.Errorf("failed to format output: %w", err)
				}
			}

			// Non-zero code without an error: the verdict was already printed.
			if exitCode != 0 {
				return &silentExitError{code: exitCode, reason: "query verification failed"}
			}

			return nil
		},
	}
	c.Flags().StringP("file", "f", "", "read query from file (use '-' for stdin)")
	c.Flags().StringArray("set", []string{}, "set template variable (key=value)")
	c.Flags().Bool("canonical", false, "print canonical query representation")
	c.Flags().String("timezone", "", "timezone for query verification (IANA, CET, +01:00, etc.)")
	c.Flags().String("locale", "", "locale for query verification (en, en_US, de_AT, etc.)")
	c.Flags().Bool("fail-on-warn", false, "exit with non-zero status on warnings (useful for CI/CD)")
	c.Flags().String("client-context", "", `optional caller context included in the dt-client-context request header
useful for AI agents or scripts to declare their intent (e.g. "root-cause-analysis")`)
	stability.MarkStable(c)
	rejectEmptyFlag(c, "file")
	return c
}

// formatVerifyResultHuman prints verification results in human-readable format
func formatVerifyResultHuman(ctx context.Context, result *exec.DQLVerifyResponse, query string, showCanonical bool) error {
	useColor := isStderrTerminal()

	// Print validation status
	if result.Valid {
		if useColor {
			fmt.Fprintf(currentStderr(ctx), "%s✔%s Query is valid\n", colorGreen, colorReset)
		} else {
			fmt.Fprintf(currentStderr(ctx), "✔ Query is valid\n")
		}
	} else {
		if useColor {
			fmt.Fprintf(currentStderr(ctx), "%s✖%s Query is invalid\n", colorRed, colorReset)
		} else {
			fmt.Fprintf(currentStderr(ctx), "✖ Query is invalid\n")
		}
	}

	// Print notifications grouped by severity
	for _, notification := range result.Notifications {
		severity := notification.Severity
		if severity == "" {
			severity = "INFO"
		}

		// Determine color based on severity
		var color string
		switch severity {
		case "ERROR":
			color = colorRed
		case "WARN", "WARNING":
			color = colorYellow
		case "INFO":
			color = colorCyan
		default:
			color = colorCyan
		}

		// Format notification message
		var prefix string
		if useColor {
			prefix = fmt.Sprintf("%s%s:%s", color, severity, colorReset)
		} else {
			prefix = fmt.Sprintf("%s:", severity)
		}

		// Print notification type and message
		if notification.NotificationType != "" {
			fmt.Fprintf(currentStderr(ctx), "%s %s: %s", prefix, notification.NotificationType, notification.Message)
		} else {
			fmt.Fprintf(currentStderr(ctx), "%s %s", prefix, notification.Message)
		}

		// Add line/column info if available
		if notification.SyntaxPosition != nil && notification.SyntaxPosition.Start != nil {
			fmt.Fprintf(currentStderr(ctx), " (line %d, col %d)", notification.SyntaxPosition.Start.Line, notification.SyntaxPosition.Start.Column)
		}
		fmt.Fprintf(currentStderr(ctx), "\n")

		// Print caret indicator for syntax errors with position
		if severity == "ERROR" && notification.SyntaxPosition != nil && notification.SyntaxPosition.Start != nil {
			if err := printSyntaxError(ctx, query, notification.SyntaxPosition, useColor); err != nil {
				// If we can't print the caret, just continue
				continue
			}
		}
	}

	// Print canonical query if requested
	if showCanonical && result.CanonicalQuery != "" {
		fmt.Fprintf(currentStderr(ctx), "\nCanonical Query:\n%s\n", result.CanonicalQuery)
	}

	return nil
}

// printSyntaxError prints the query line with a caret indicator pointing to the error position
func printSyntaxError(ctx context.Context, query string, pos *exec.SyntaxPosition, useColor bool) error {
	if pos == nil || pos.Start == nil {
		return fmt.Errorf("no position information")
	}

	// Split query into lines
	lines := strings.Split(query, "\n")

	// Line numbers are 1-based
	lineNum := pos.Start.Line
	if lineNum < 1 || lineNum > len(lines) {
		return fmt.Errorf("line number out of range")
	}

	// Get the relevant line (0-indexed)
	line := lines[lineNum-1]

	// Column is 1-based
	col := pos.Start.Column
	if col < 1 {
		col = 1
	}

	// Print the line
	fmt.Fprintf(currentStderr(ctx), "  %s\n", line)

	// Print caret indicator
	// Account for the "  " indent
	spaces := strings.Repeat(" ", col+1) // +2 for indent, -1 for 1-based column

	// Determine caret length (if End position is available)
	caretLen := 1
	if pos.End != nil && pos.End.Line == lineNum && pos.End.Column > col {
		caretLen = pos.End.Column - col + 1
	}
	carets := strings.Repeat("^", caretLen)

	if useColor {
		fmt.Fprintf(currentStderr(ctx), "%s%s%s%s\n", spaces, colorRed, carets, colorReset)
	} else {
		fmt.Fprintf(currentStderr(ctx), "%s%s\n", spaces, carets)
	}

	return nil
}

// getVerifyExitCode determines the exit code based on verification results and errors
func getVerifyExitCode(result *exec.DQLVerifyResponse, err error, failOnWarn bool) int {
	// Handle errors first
	if err != nil {
		errMsg := err.Error()

		// Check for auth/permission errors (401, 403)
		if strings.Contains(errMsg, "status 401") || strings.Contains(errMsg, "status 403") {
			return 2
		}

		// Check for network/server errors (timeout, 5xx)
		if strings.Contains(errMsg, "status 5") ||
			strings.Contains(errMsg, "timeout") ||
			strings.Contains(errMsg, "connection") {
			return 3
		}

		// Other errors (likely client-side issues)
		return 1
	}

	// No error from API call, check verification result
	if result == nil {
		return 1
	}

	// Check if query is invalid
	if !result.Valid {
		return 1
	}

	// Check for ERROR notifications
	for _, notification := range result.Notifications {
		if notification.Severity == "ERROR" {
			return 1
		}
	}

	// Check for WARN notifications if --fail-on-warn is set
	if failOnWarn {
		for _, notification := range result.Notifications {
			if notification.Severity == "WARN" || notification.Severity == "WARNING" {
				return 1
			}
		}
	}

	// Valid query with no errors (and no warnings, or warnings without --fail-on-warn)
	return 0
}

func init() {
	verifyCmd.AddCommand(verifyQueryCmd)

	// Flags for verify query command
}

// Declared stable: the invocation and output contract of this command is
// additive-only. Stable is never implied -- see AGENTS.md "Stability Tiers".
func init() {
}
