package cmd

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/dynatrace-oss/dtctl/pkg/resources/appengine"
	"github.com/dynatrace-oss/dtctl/pkg/stability"
)

// findIntentsCmd finds intents that match given data
var findIntentsCmd = newFindIntentsCmd()

func newFindIntentsCmd() *cobra.Command {
	var findIntentsData string
	var findIntentsDataFile string
	var findIntentsLimit int
	c := &cobra.Command{
		Use:   "intents",
		Short: "Find intents that match given data",
		Long: `Find app intents that can handle the provided data.

This command matches the provided data against all available intents
and returns intents that can handle the data, sorted by match quality.

A property is satisfied when the data carries one of the keys the intent
accepts for it (see acceptedKeys in 'dtctl describe intent -o json') and,
where the app declares a pattern for that key, the value matches it.

Match quality is calculated based on property coverage:
  - 0%: Intent has required properties the data does not satisfy, or
        declares no property the data satisfies (not listed)
  - 1-100%: Percentage of intent properties the data satisfies

At equal quality, deprecated intents are listed after the others.

Examples:
  # Find intents for trace data
  dtctl find intents --data trace_id=abc123,timestamp=2026-02-02T10:00:00Z

  # Find intents from JSON file
  dtctl find intents --data-file payload.json

  # Find intents from JSON stdin
  echo '{"trace_id":"abc123"}' | dtctl find intents --data-file -

  # Limit results
  dtctl find intents --data trace_id=abc123 --limit 5

  # Output as JSON
  dtctl find intents --data trace_id=abc123 -o json
`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if findIntentsData == "" && findIntentsDataFile == "" {
				return fmt.Errorf("either --data or --data-file must be specified")
			}

			if findIntentsData != "" && findIntentsDataFile != "" {
				return fmt.Errorf("--data and --data-file are mutually exclusive")
			}

			_, c, err := setupClient(cmdContext(cmd))
			if err != nil {
				return err
			}

			handler := appengine.NewIntentHandler(c)

			// Parse data
			var data map[string]interface{}

			if findIntentsDataFile != "" {
				// Read from file, or stdin for "-" — both through the vfs seam, so
				// an embedded invocation reads the request's files and stdin.
				content, err := readFileFlag(cmdContext(cmd), "data-file", findIntentsDataFile)
				if err != nil {
					return fmt.Errorf("failed to read data file: %w", err)
				}

				if err := json.Unmarshal(content, &data); err != nil {
					return fmt.Errorf("failed to parse JSON data: %w", err)
				}
			} else {
				// Parse key=value pairs
				data = make(map[string]interface{})
				pairs := strings.Split(findIntentsData, ",")
				for _, pair := range pairs {
					parts := strings.SplitN(pair, "=", 2)
					if len(parts) != 2 {
						return fmt.Errorf("invalid data format, expected key=value, got %q", pair)
					}
					data[strings.TrimSpace(parts[0])] = strings.TrimSpace(parts[1])
				}
			}

			// Find matching intents
			matches, err := handler.FindIntentsForData(data)
			if err != nil {
				return err
			}

			// Apply limit
			if findIntentsLimit > 0 && len(matches) > findIntentsLimit {
				matches = matches[:findIntentsLimit]
			}

			// Print results
			printer := newPrinterCtx(cmdContext(cmd))
			return printer.PrintList(matches)
		},
	}
	c.Flags().StringVar(&findIntentsData, "data", "", "data as comma-separated key=value pairs")
	c.Flags().StringVar(&findIntentsDataFile, "data-file", "", "JSON file containing data (use - for stdin)")
	c.Flags().IntVar(&findIntentsLimit, "limit", 0, "limit number of results (0 for unlimited)")
	stability.MarkStable(c)
	return c
}

func init() {
}

// Declared stable: the invocation and output contract of this command is
// additive-only. Stable is never implied -- see AGENTS.md "Stability Tiers".
func init() {
}
