package cmd

import (
	"encoding/json"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/dynatrace-oss/dtctl/pkg/output"
	"github.com/dynatrace-oss/dtctl/pkg/resources/segment"
	"github.com/dynatrace-oss/dtctl/pkg/safety"
	"github.com/dynatrace-oss/dtctl/pkg/stability"
	"github.com/dynatrace-oss/dtctl/pkg/util/format"
)

// createSegmentCmd creates a Grail filter segment
var createSegmentCmd = newCreateSegmentCmd()

func newCreateSegmentCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "segment -f segment.yaml",
		Short: "Create a Grail filter segment",
		Long: `Create a new Grail filter segment from a YAML or JSON file.

Examples:
  # Create a segment from a YAML file
  dtctl create segment -f segment.yaml

  # Create from a JSON file
  dtctl create segment -f segment.json

  # Dry run to preview
  dtctl create segment -f segment.yaml --dry-run
`,
		Aliases: []string{"seg", "filter-segment", "filter-segments"},
		RunE: func(cmd *cobra.Command, args []string) error {
			file, _ := cmd.Flags().GetString("file")

			// Read from file
			fileData, err := readFileFlag(cmdContext(cmd), "file", file)
			if err != nil {
				return fmt.Errorf("failed to read file: %w", err)
			}

			jsonData, err := format.ValidateAndConvert(fileData)
			if err != nil {
				return fmt.Errorf("invalid file format: %w", err)
			}

			// Handle dry-run
			if dryRun(cmdContext(cmd)) {
				var seg map[string]interface{}
				if err := json.Unmarshal(jsonData, &seg); err != nil {
					return fmt.Errorf("failed to parse segment definition: %w", err)
				}

				report := newDryRunReport(cmd).Linef("Dry run: would create segment")
				if name, ok := seg["name"].(string); ok && name != "" {
					report.Linef("  Name: %s", name).Detail("name", "%s", name)
				}
				if desc, ok := seg["description"].(string); ok && desc != "" {
					report.Linef("  Description: %s", desc).Detail("description", "%s", desc)
				}
				if includes, ok := seg["includes"].([]interface{}); ok {
					report.Linef("  Includes: %d rule(s)", len(includes)).Detail("includes", "%d", len(includes))
				}
				return report.
					Linef("").
					Linef("Segment definition parsed successfully").
					Payload(jsonData).
					Print()
			}

			_, c, err := setupWithSafety(cmdContext(cmd), safety.OperationCreate)
			if err != nil {
				return err
			}

			handler := segment.NewHandler(c)

			result, err := handler.Create(jsonData)
			if err != nil {
				return fmt.Errorf("failed to create segment: %w", err)
			}

			output.FprintSuccess(currentStderr(cmdContext(cmd)), "Segment %q created (UID: %s)", result.Name, result.UID)
			return nil
		},
	}
	c.Flags().StringP("file", "f", "", "file containing segment definition (YAML or JSON), or - for stdin")
	stability.MarkStable(c)
	markFlagRequiredNonEmpty(c, "file")
	return c
}

func init() {
}

// Declared stable: the invocation and output contract of this command is
// additive-only. Stable is never implied -- see AGENTS.md "Stability Tiers".
func init() {
}
