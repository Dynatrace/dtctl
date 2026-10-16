package cmd

import (
	"encoding/json"
	"errors"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/dynatrace-oss/dtctl/pkg/client"
	"github.com/dynatrace-oss/dtctl/pkg/output"
	"github.com/dynatrace-oss/dtctl/pkg/resources/anomalydetector"
	"github.com/dynatrace-oss/dtctl/pkg/safety"
	"github.com/dynatrace-oss/dtctl/pkg/stability"
	"github.com/dynatrace-oss/dtctl/pkg/util/format"
	"github.com/dynatrace-oss/dtctl/pkg/util/template"
)

// createAnomalyDetectorCmd creates an anomaly detector from a file
var createAnomalyDetectorCmd = newCreateAnomalyDetectorCmd()

func newCreateAnomalyDetectorCmd() *cobra.Command {
	c := &cobra.Command{
		Use:     "anomaly-detector -f <file>",
		Aliases: []string{"ad"},
		Short:   "Create a custom anomaly detector from a file",
		Long: `Create a new custom anomaly detector from a YAML or JSON file.

Accepts both flattened YAML format (recommended) and raw Settings API format.
When the source field is omitted in flattened format, it defaults to "dtctl".

Examples:
  # Create from flattened YAML (recommended)
  dtctl create anomaly-detector -f detector.yaml

  # Create from raw Settings API format
  dtctl create anomaly-detector -f detector-raw.yaml

  # Create with template variables
  dtctl create anomaly-detector -f detector.yaml --set threshold=95

  # Dry run to preview
  dtctl create anomaly-detector -f detector.yaml --dry-run
`,
		RunE: func(cmd *cobra.Command, args []string) error {
			file, _ := cmd.Flags().GetString("file")

			setFlags, _ := cmd.Flags().GetStringArray("set")

			// Read the file
			fileData, err := readFileFlag(cmdContext(cmd), "file", file)
			if err != nil {
				return fmt.Errorf("failed to read file: %w", err)
			}

			// Convert to JSON if needed
			jsonData, err := format.ValidateAndConvert(fileData)
			if err != nil {
				return fmt.Errorf("invalid file format: %w", err)
			}

			// Apply template rendering if variables provided
			if len(setFlags) > 0 {
				templateVars, err := template.ParseSetFlags(setFlags)
				if err != nil {
					return fmt.Errorf("invalid --set flag: %w", err)
				}
				rendered, err := template.RenderTemplate(string(jsonData), templateVars)
				if err != nil {
					return fmt.Errorf("template rendering failed: %w", err)
				}
				jsonData = []byte(rendered)
			}

			// Handle dry-run
			if dryRun(cmdContext(cmd)) {
				return dryRunCreateAnomalyDetector(cmd, jsonData)
			}

			_, c, err := setupWithSafety(cmdContext(cmd), safety.OperationCreate)
			if err != nil {
				return err
			}

			handler := anomalydetector.NewHandler(c).WithDefaultActor(currentActor(c))

			result, err := handler.Create(jsonData)
			if err != nil {
				return fmt.Errorf("failed to create anomaly detector: %w", err)
			}

			output.FprintSuccess(currentStderr(cmdContext(cmd)), "Anomaly detector %q created", result.Title)
			output.FprintInfo(currentStderr(cmdContext(cmd)), "  Object ID: %s", result.ObjectID)
			output.FprintInfo(currentStderr(cmdContext(cmd)), "  Title:     %s", result.Title)
			output.FprintInfo(currentStderr(cmdContext(cmd)), "  Analyzer:  %s", result.AnalyzerShort)
			output.FprintInfo(currentStderr(cmdContext(cmd)), "  Enabled:   %v", result.Enabled)
			output.FprintInfo(currentStderr(cmdContext(cmd)), "")
			output.FprintInfo(currentStderr(cmdContext(cmd)), "Run 'dtctl describe anomaly-detector %s' to view details", result.ObjectID)
			return nil
		},
	}
	c.Flags().StringP("file", "f", "", "file containing anomaly detector definition, or - for stdin (required)")
	c.Flags().StringArray("set", []string{}, "set template variable (key=value)")
	stability.MarkStable(c)
	markFlagRequiredNonEmpty(c, "file")
	return c
}

// currentActor resolves the identity used for executionSettings.actor when a
// definition omits it. An unresolvable identity is not fatal: the environment
// may accept the detector without an actor, and the resulting API error names
// the field if it does not.
func currentActor(c *client.Client) string {
	actor, err := c.CurrentUserID()
	if err != nil {
		return ""
	}
	return actor
}

// dryRunCreateAnomalyDetector prints the payload dtctl would send and reports
// the verdict of server-side schema validation. Echoing the input back
// unchecked reported success for definitions the live call rejects (issue #369).
//
// In agent mode the verdict travels in the plan as details.schema_validation
// ("passed", or "skipped: <reason>"), so it is read from the envelope rather
// than from stderr. A definition the schema rejects is an error, not a plan.
func dryRunCreateAnomalyDetector(cmd *cobra.Command, jsonData []byte) error {
	_, c, err := setupClient(cmdContext(cmd))
	if err != nil {
		// No usable environment: fall back to local validation only.
		body, prepErr := anomalydetector.NewHandler(nil).PrepareCreateBody(jsonData)
		if prepErr != nil {
			return prepErr
		}
		return printDryRunAnomalyDetector(cmd, body, nil, err)
	}

	handler := anomalydetector.NewHandler(c).WithDefaultActor(currentActor(c))
	body, err := handler.PrepareCreateBody(jsonData)
	if err != nil {
		return err
	}

	var unavailable *anomalydetector.ValidationUnavailableError
	switch err := handler.ValidateCreate(jsonData); {
	case err == nil:
		return printDryRunAnomalyDetector(cmd, body, nil, nil)
	case errors.As(err, &unavailable):
		return printDryRunAnomalyDetector(cmd, body, nil, unavailable.Err)
	default:
		return printDryRunAnomalyDetector(cmd, body, err, nil)
	}
}

// printDryRunAnomalyDetector shows the request body, including the schema
// defaults dtctl fills in, so the dry run reflects what would actually be sent,
// followed by the validation verdict.
//
// At most one of invalid (the schema rejected the definition) and skipped
// (validation could not run) is set; neither means validation passed. A human
// sees the body first in every case, then the verdict. In agent mode an invalid
// definition produces the error envelope alone, since stdout carries one
// document.
func printDryRunAnomalyDetector(cmd *cobra.Command, body map[string]any, invalid, skipped error) error {
	ctx := cmdContext(cmd)
	report := newDryRunReport(cmd).OnStderr().
		Linef("Dry run: would create anomaly detector").
		Linef("---")
	rendered, err := json.Marshal(body)
	if err != nil {
		report.Linef("%v", body)
	} else {
		report.Linef("%s", rendered).Payload(rendered)
	}
	report.Linef("---")

	if invalid != nil {
		if !agentMode(ctx) {
			if err := report.Print(); err != nil {
				return err
			}
		}
		return invalid
	}

	if skipped != nil {
		report.Detail("schema_validation", "skipped: %v", skipped)
	} else {
		report.Detail("schema_validation", "passed")
	}
	if err := report.Print(); err != nil {
		return err
	}

	switch {
	case skipped != nil:
		output.FprintWarning(currentStderr(ctx), "schema validation skipped: %v", skipped)
	case !agentMode(ctx):
		output.FprintSuccess(currentStderr(ctx), "Schema validation passed")
	}
	return nil
}

// Declared stable: the invocation and output contract of this command is
// additive-only. Stable is never implied -- see AGENTS.md "Stability Tiers".
