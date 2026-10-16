package cmd

import (
	"context"
	"fmt"
	"time"

	"github.com/spf13/cobra"

	"github.com/dynatrace-oss/dtctl/pkg/exec"
	"github.com/dynatrace-oss/dtctl/pkg/stability"
	"github.com/dynatrace-oss/dtctl/pkg/util/template"
	"github.com/dynatrace-oss/dtctl/pkg/wait"
)

// waitCmd represents the wait command
var waitCmd = newWaitCmd()

func newWaitCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "wait",
		Short: "Wait for specific conditions on resources",
		Long: `Wait for specific conditions to be met on Dynatrace resources.

The wait command enables polling for data availability and other conditions.
This is useful for testing scenarios where you need to verify that data has
arrived before proceeding.`,
	}
	stability.MarkStable(c)
	return c
}

// waitQueryCmd represents the wait query subcommand
var waitQueryCmd = newWaitQueryCmd()

func newWaitQueryCmd() *cobra.Command {
	c := &cobra.Command{
		Use:     "query [dql-string]",
		Aliases: []string{"q"},
		Short:   "Wait for a DQL query to meet a condition",
		Long: `Wait for a DQL query to meet a specific condition.

This command repeatedly executes a DQL query until the specified condition
is satisfied or a timeout is reached. It uses exponential backoff to avoid
overwhelming the system.

Common use cases:
- Wait for instrumented test data to arrive
- Verify log ingestion before assertions
- Poll for specific spans with test IDs

Supported Conditions:
  count=N       - Exactly N records
  count-gte=N   - At least N records (>=)
  count-gt=N    - More than N records (>)
  count-lte=N   - At most N records (<=)
  count-lt=N    - Fewer than N records (<)
  any           - Any records returned (count > 0)
  none          - No records returned (count == 0)

Exit Codes:
  0 - Success (condition met)
  1 - Timeout reached
  2 - Max attempts exceeded
  3 - Query execution error
  4 - Invalid condition syntax
  5 - Invalid arguments

Examples:
  # Wait for a specific test span to arrive
  # (DQL string values need double quotes; wrap the query in single quotes)
  dtctl wait query 'fetch spans | filter test_id == "test-123"' --for=count=1

  # Wait for any error logs
  dtctl wait query 'fetch logs | filter status == "ERROR"' --for=any --timeout 2m

  # Query with template variables
  dtctl wait query -f query.dql --set test_id=my-test --for=count-gte=1

  # Custom backoff strategy for CI/CD
  dtctl wait query "..." --for=any --min-interval 500ms --max-interval 15s

  # Get results as JSON when condition is met
  dtctl wait query "..." --for=count=1 -o json > result.json
`,
		RunE: func(cmd *cobra.Command, args []string) error {
			// Parse the condition
			conditionStr, _ := cmd.Flags().GetString("for")

			condition, err := wait.ParseCondition(conditionStr)
			if err != nil {
				return fmt.Errorf("invalid condition: %w", err)
			}

			// Get query string
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

			// Load config and create client
			cfg, c, err := setupClient(cmdContext(cmd))
			if err != nil {
				return err
			}

			// Parse timing flags
			timeout, _ := cmd.Flags().GetDuration("timeout")
			maxAttempts, _ := cmd.Flags().GetInt("max-attempts")
			initialDelay, _ := cmd.Flags().GetDuration("initial-delay")
			minInterval, _ := cmd.Flags().GetDuration("min-interval")
			maxInterval, _ := cmd.Flags().GetDuration("max-interval")
			backoffMultiplier, _ := cmd.Flags().GetFloat64("backoff-multiplier")

			// Parse output flags
			quiet, _ := cmd.Flags().GetBool("quiet")
			verbose, _ := cmd.Flags().GetBool("verbose")

			// Build backoff config
			backoffConfig := wait.BackoffConfig{
				MinInterval:  minInterval,
				MaxInterval:  maxInterval,
				Multiplier:   backoffMultiplier,
				InitialDelay: initialDelay,
			}

			// Validate backoff config
			if err := backoffConfig.Validate(); err != nil {
				return fmt.Errorf("invalid backoff configuration: %w", err)
			}

			// Get query execution options (reuse flags from query command)
			queryLimits, err := resolveQueryLimits(cmd, cfg)
			if err != nil {
				return err
			}
			fetchTimeoutSeconds, _ := cmd.Flags().GetInt32("fetch-timeout-seconds")
			defaultTimeframeStart, _ := cmd.Flags().GetString("default-timeframe-start")
			defaultTimeframeEnd, _ := cmd.Flags().GetString("default-timeframe-end")
			locale, _ := cmd.Flags().GetString("locale")
			timezone, _ := cmd.Flags().GetString("timezone")

			queryOpts := exec.DQLExecuteOptions{
				OutputFormat:           outputFormat(cmdContext(cmd)),
				MaxResultRecords:       queryLimits.MaxResultRecords,
				MaxResultBytes:         queryLimits.MaxResultBytes,
				DefaultScanLimitGbytes: queryLimits.ScanLimitGbytes,
				DefaultSamplingRatio:   queryLimits.SamplingRatio,
				FetchTimeoutSeconds:    fetchTimeoutSeconds,
				DefaultTimeframeStart:  defaultTimeframeStart,
				DefaultTimeframeEnd:    defaultTimeframeEnd,
				Locale:                 locale,
				Timezone:               timezone,
				// ShowProgress is left false: the waiter drives its own per-attempt
				// progress output on stderr, and a per-query progress bar (plus a
				// completion summary per attempt) would fight it.
			}

			// Create wait config
			waitConfig := wait.WaitConfig{
				Query:        query,
				Condition:    condition,
				Timeout:      timeout,
				MaxAttempts:  maxAttempts,
				Backoff:      backoffConfig,
				QueryOptions: queryOpts,
				OutputFormat: outputFormat(cmdContext(cmd)),
				Quiet:        quiet,
				Verbose:      verbose,
				ProgressOut:  currentStderr(cmdContext(cmd)),
				Out:          currentStdout(cmdContext(cmd)),
			}

			// Create executor and waiter
			executor := newDQLExecutorFromConfig(cmdContext(cmd), cfg, c)
			waiter := wait.NewQueryWaiter(executor, waitConfig)

			result, err := waiter.Wait(cmdContext(cmd))
			if err != nil && err != context.DeadlineExceeded {
				return fmt.Errorf("wait failed: %w", err)
			}

			// Print results if output format specified and condition was met
			if result.Success && outputFormat(cmdContext(cmd)) != "" {
				if err := waiter.PrintResults(result); err != nil {
					return fmt.Errorf("failed to print results: %w", err)
				}
			}

			// Exit code encodes the failure mode (documented contract: 1 timeout,
			// 2 max attempts, 3 other); the result output was already printed.
			if !result.Success {
				switch result.FailureReason {
				case "timeout":
					return &silentExitError{code: 1, reason: "wait: timeout"}
				case "max attempts exceeded":
					return &silentExitError{code: 2, reason: "wait: max attempts exceeded"}
				default:
					return &silentExitError{code: 3, reason: "wait: condition not met"}
				}
			}

			return nil
		},
	}
	c.Flags().String("for", "", "condition to wait for (required: count=N, count-gte=N, count-gt=N, count-lte=N, count-lt=N, any, none)")
	c.Flags().StringP("file", "f", "", "read query from file (use - for stdin)")
	c.Flags().StringArray("set", []string{}, "set template variable (key=value)")
	c.Flags().Duration("timeout", 5*time.Minute, "maximum time to wait (0 = unlimited)")
	c.Flags().Int("max-attempts", 0, "maximum number of attempts (0 = unlimited)")
	c.Flags().Duration("initial-delay", 0, "delay before first query attempt")
	c.Flags().Duration("min-interval", 1*time.Second, "minimum interval between retries")
	c.Flags().Duration("max-interval", 10*time.Second, "maximum interval between retries")
	c.Flags().Float64("backoff-multiplier", 2.0, "exponential backoff multiplier (must be > 1.0)")
	c.Flags().BoolP("quiet", "q", false, "suppress progress messages")
	c.Flags().BoolP("verbose", "v", false, "show detailed progress")
	stability.MarkFlag(c, "verbose", stability.Experimental, pre10Since)
	c.Flags().Int64("max-result-records", 0, "maximum number of result records")
	c.Flags().Int64("max-result-bytes", 0, "maximum result size in bytes")
	c.Flags().Float64("default-scan-limit-gbytes", 0, "scan limit in gigabytes")
	c.Flags().Float64("default-sampling-ratio", 0, "default sampling ratio")
	c.Flags().Int32("fetch-timeout-seconds", 0, "time limit for fetching data in seconds")
	c.Flags().String("default-timeframe-start", "", "query timeframe start (ISO-8601/RFC3339)")
	c.Flags().String("default-timeframe-end", "", "query timeframe end (ISO-8601/RFC3339)")
	c.Flags().String("locale", "", "query locale (e.g., 'en_US', 'de_DE')")
	c.Flags().String("timezone", "", "query timezone (e.g., 'UTC', 'Europe/Paris')")
	stability.MarkStable(c)
	markFlagRequiredNonEmpty(c, "for")
	addQueryLimitFlags(c)
	rejectEmptyFlag(c, "file")
	return c
}

func init() {
	rootCmd.AddCommand(waitCmd)
	waitCmd.AddCommand(waitQueryCmd)

	// Query input flags
	// Timing flags
	// Output control flags
	// Renamed or removed in 1.0 because it hides a global flag
	// (contrib breaking-changes/unshadow-global-flags.md).
	// Query execution flags (inherited from query command)
}

// Declared stable: the invocation and output contract of these commands is
// additive-only. Stable is never implied -- see AGENTS.md "Stability Tiers".
