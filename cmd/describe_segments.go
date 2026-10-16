package cmd

import (
	"fmt"
	"io"
	"strings"

	"github.com/spf13/cobra"
	"golang.org/x/text/cases"
	"golang.org/x/text/language"

	"github.com/dynatrace-oss/dtctl/pkg/output"
	"github.com/dynatrace-oss/dtctl/pkg/resources/segment"
	"github.com/dynatrace-oss/dtctl/pkg/stability"
)

// describeSegmentCmd shows detailed info about a segment
var describeSegmentCmd = newDescribeSegmentCmd()

func newDescribeSegmentCmd() *cobra.Command {
	c := &cobra.Command{
		Use:     "segment <uid>",
		Aliases: []string{"seg", "filter-segment", "filter-segments"},
		Short:   "Show details of a Grail filter segment",
		Long: `Show detailed information about a Grail filter segment.

Examples:
  # Describe a segment
  dtctl describe segment <uid>
  dtctl describe seg <uid>
`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			uid := args[0]

			_, c, printer, err := setup(cmdContext(cmd))
			if err != nil {
				return err
			}

			handler := segment.NewHandler(c)

			seg, err := handler.Get(uid)
			if err != nil {
				return err
			}

			// For table output, show detailed human-readable information
			if outputFormat(cmdContext(cmd)) == "table" {
				printSegmentDescribeTable(currentStdout(cmdContext(cmd)), seg)
				return nil
			}

			// For other formats, use standard printer
			enrichAgent(printer, "describe", "segment")
			return printer.Print(seg)
		},
	}
	stability.MarkStable(c)
	return c
}

// printSegmentDescribeTable renders a segment in the human-readable describe table format.
func printSegmentDescribeTable(w io.Writer, seg *segment.FilterSegment) {
	const kw = 16
	output.FprintDescribeKV(w, "Name:", kw, "%s", seg.Name)
	output.FprintDescribeKV(w, "UID:", kw, "%s", seg.UID)
	if seg.Description != "" {
		output.FprintDescribeKV(w, "Description:", kw, "%s", seg.Description)
	}
	if seg.IsPublic {
		output.FprintDescribeKV(w, "Public:", kw, "Yes")
	} else {
		output.FprintDescribeKV(w, "Public:", kw, "No")
	}
	if seg.Owner != "" {
		output.FprintDescribeKV(w, "Owner:", kw, "%s", seg.Owner)
	}
	output.FprintDescribeKV(w, "Version:", kw, "%d", seg.Version)

	// Includes
	if len(seg.Includes) > 0 {
		fmt.Fprintln(w)
		output.FprintDescribeSection(w, "Includes:")
		fmt.Fprintf(w, "  %-20s %s\n", "DATA OBJECT", "FILTER")
		for _, inc := range seg.Includes {
			dataObject := inc.DataObject
			if dataObject == "_all_data_object" {
				dataObject = "All data objects"
			} else {
				dataObject = cases.Title(language.Und).String(dataObject)
			}
			fmt.Fprintf(w, "  %-20s %s\n", dataObject, inc.Filter)
		}
	}

	// Variables
	if seg.Variables != nil {
		fmt.Fprintln(w)
		output.FprintDescribeSection(w, "Variables:")
		if seg.Variables.Type != "" {
			output.FprintDescribeKV(w, "  Type:", 12, "%s", seg.Variables.Type)
		}
		if seg.Variables.Value != "" {
			output.FprintDescribeKV(w, "  Value:", 12, "%s", seg.Variables.Value)
		}
	}

	// Allowed operations
	if len(seg.AllowedOperations) > 0 {
		fmt.Fprintln(w)
		output.FprintDescribeKV(w, "Operations:", kw, "%s", strings.Join(seg.AllowedOperations, ", "))
	}
}

// Declared stable: the invocation and output contract of this command is
// additive-only. Stable is never implied -- see AGENTS.md "Stability Tiers".
func init() {
}
