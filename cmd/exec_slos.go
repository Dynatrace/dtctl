package cmd

import (
	"context"
	"fmt"
	"time"

	"github.com/spf13/cobra"

	"github.com/dynatrace-oss/dtctl/pkg/resources/slo"
	"github.com/dynatrace-oss/dtctl/pkg/safety"
	"github.com/dynatrace-oss/dtctl/pkg/stability"
)

// execSLOCmd evaluates an SLO
var execSLOCmd = newExecSLOCmd()

func newExecSLOCmd() *cobra.Command {
	c := &cobra.Command{
		Use:     "slo <slo-id>",
		Aliases: []string{"service-level-objective"},
		Short:   "Evaluate a service-level objective",
		Long: `Evaluate an SLO and retrieve its current status.

This command starts an SLO evaluation and polls for the results. The evaluation
assesses the SLO against its defined criteria and returns the current status,
value, and error budget for each criterion.

Examples:
  # Evaluate an SLO
  dtctl exec slo my-slo-id

  # Evaluate with custom timeout (default: 30s)
  dtctl exec slo my-slo-id --timeout 60

  # Output as JSON
  dtctl exec slo my-slo-id -o json

  # Output as YAML
  dtctl exec slo my-slo-id -o yaml
`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			sloID := args[0]

			// SLO evaluation is a read-shaped POST: it computes a result and persists
			// nothing (the API declares a :read scope), so gating it as a create would
			// wrongly block it in a readonly context.
			_, c, err := setupWithSafety(cmdContext(cmd), safety.OperationRead)
			if err != nil {
				return err
			}

			handler := slo.NewHandler(c)

			// Start the evaluation
			fmt.Fprintf(currentStdout(cmdContext(cmd)), "Starting SLO evaluation for %q...\n", sloID)
			evalResult, err := handler.Evaluate(sloID)
			if err != nil {
				return err
			}

			// Check if results are already available (immediate response)
			if len(evalResult.EvaluationResults) > 0 {
				fmt.Fprintf(currentStdout(cmdContext(cmd)), "SLO Evaluation Complete\n")

				// Check output format
				if outputFormat(cmdContext(cmd)) == "" || outputFormat(cmdContext(cmd)) == "table" {
					// Print in table format
					printer := newPrinterCtx(cmdContext(cmd))
					return printer.PrintList(evalResult.EvaluationResults)
				}

				// Print in requested format (json/yaml)
				printer := newPrinterCtx(cmdContext(cmd))
				return printer.Print(evalResult)
			}

			// If no immediate results, check for evaluation token for async polling
			evaluationToken := evalResult.EvaluationToken
			if evaluationToken == "" {
				return fmt.Errorf("no evaluation token returned and no immediate results available")
			}

			// Get timeout from flags
			timeoutSeconds, _ := cmd.Flags().GetInt("timeout")
			timeoutMs := timeoutSeconds * 1000

			// Poll for results with exponential backoff
			fmt.Fprintf(currentStdout(cmdContext(cmd)), "Polling for evaluation results...\n")

			ctx, cancel := context.WithTimeout(cmdContext(cmd), time.Duration(timeoutSeconds)*time.Second)
			defer cancel()

			pollInterval := 2 * time.Second
			maxPollInterval := 10 * time.Second

			for {
				select {
				case <-ctx.Done():
					return fmt.Errorf("timeout waiting for SLO evaluation to complete")
				default:
					result, err := handler.PollEvaluation(evaluationToken, timeoutMs)
					if err != nil {
						// Check if it's a timeout or other error
						if ctx.Err() != nil {
							return fmt.Errorf("timeout waiting for SLO evaluation to complete")
						}
						return err
					}

					// Check if we have results
					if len(result.EvaluationResults) > 0 {
						fmt.Fprintf(currentStdout(cmdContext(cmd)), "\nSLO Evaluation Complete\n")

						// Check output format
						if outputFormat(cmdContext(cmd)) == "" || outputFormat(cmdContext(cmd)) == "table" {
							// Print in table format
							printer := newPrinterCtx(cmdContext(cmd))
							return printer.PrintList(result.EvaluationResults)
						}

						// Print in requested format (json/yaml)
						printer := newPrinterCtx(cmdContext(cmd))
						return printer.Print(result)
					}

					// Wait before next poll with exponential backoff, but stay
					// responsive to cancellation instead of blocking through it.
					select {
					case <-ctx.Done():
						return fmt.Errorf("timeout waiting for SLO evaluation to complete")
					case <-time.After(pollInterval):
					}
					if pollInterval < maxPollInterval {
						pollInterval *= 2
						if pollInterval > maxPollInterval {
							pollInterval = maxPollInterval
						}
					}
				}
			}
		},
	}
	c.Flags().Int("timeout", 30, "timeout in seconds when polling for evaluation results")
	stability.MarkFlag(c, "timeout", stability.Experimental, pre10Since)
	stability.MarkStable(c)
	return c
}

func init() {
	// SLO flags
	// Becomes a duration flag in 1.0; a bare integer errors
	// (contrib breaking-changes/timeout-duration.md).
}

// Declared stable: the invocation and output contract of this command is
// additive-only. Stable is never implied -- see AGENTS.md "Stability Tiers".
func init() {
}
