package cmd

import (
	"fmt"
	"io"
	"strings"

	"github.com/spf13/cobra"

	"github.com/dynatrace-oss/dtctl/pkg/output"
	"github.com/dynatrace-oss/dtctl/pkg/resources/appengine"
	"github.com/dynatrace-oss/dtctl/pkg/stability"
)

// describeIntentCmd describes an app intent
var describeIntentCmd = newDescribeIntentCmd()

func newDescribeIntentCmd() *cobra.Command {
	c := &cobra.Command{
		Use:     "intent <app-id>/<intent-id>",
		Aliases: []string{},
		Short:   "Describe an App Engine intent",
		Long: `Show detailed information about an app intent.

Intents enable inter-app communication by defining entry points
that apps expose for opening resources with contextual data.

Examples:
  # Describe an intent
  dtctl describe intent dynatrace.distributedtracing/view-trace

  # Output as JSON
  dtctl describe intent dynatrace.distributedtracing/view-trace -o json

  # Output as YAML
  dtctl describe intent dynatrace.logs/view-log-entry -o yaml
`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			_, c, printer, err := setup(cmdContext(cmd))
			if err != nil {
				return err
			}

			handler := appengine.NewIntentHandler(c)

			// Get intent details
			intent, err := handler.GetIntent(args[0])
			if err != nil {
				return err
			}

			// For table output, show detailed information
			if outputFormat(cmdContext(cmd)) == "table" {
				const w = 14
				output.FprintDescribeKV(currentStdout(cmdContext(cmd)), "Intent:", w, "%s", intent.IntentID)
				output.FprintDescribeKV(currentStdout(cmdContext(cmd)), "Full Name:", w, "%s", intent.FullName)
				if intent.Name != "" && intent.Name != intent.Description {
					output.FprintDescribeKV(currentStdout(cmdContext(cmd)), "Name:", w, "%s", intent.Name)
				}
				if intent.Description != "" {
					output.FprintDescribeKV(currentStdout(cmdContext(cmd)), "Description:", w, "%s", intent.Description)
				}
				output.FprintDescribeKV(currentStdout(cmdContext(cmd)), "App:", w, "%s (%s)", intent.AppName, intent.AppID)
				if intent.Deprecated {
					if intent.DeprecationMessage != "" {
						output.FprintDescribeKV(currentStdout(cmdContext(cmd)), "Deprecated:", w, "yes (%s)", intent.DeprecationMessage)
					} else {
						output.FprintDescribeKV(currentStdout(cmdContext(cmd)), "Deprecated:", w, "yes")
					}
				}

				// Print properties, in name order so repeated runs match.
				if len(intent.Properties) > 0 {
					fmt.Fprintln(currentStdout(cmdContext(cmd)))
					output.FprintDescribeSection(currentStdout(cmdContext(cmd)), "Properties:")
					for _, propName := range intent.SortedPropertyNames() {
						describeIntentProperty(currentStdout(cmdContext(cmd)), propName, intent.Properties[propName])
					}
				}

				// Show required properties summary
				if len(intent.RequiredProps) > 0 {
					fmt.Fprintln(currentStdout(cmdContext(cmd)))
					output.FprintDescribeKV(currentStdout(cmdContext(cmd)), "Required:", w, "%s", strings.Join(intent.RequiredProps, ", "))
				}

				// Show usage example
				fmt.Fprintln(currentStdout(cmdContext(cmd)))
				output.FprintDescribeSection(currentStdout(cmdContext(cmd)), "Usage:")
				fmt.Fprintf(currentStdout(cmdContext(cmd)), "  dtctl open intent %s --data <key>=<value>\n", intent.FullName)
				fmt.Fprintf(currentStdout(cmdContext(cmd)), "  dtctl find intents --data <key>=<value>\n")

				return nil
			}

			// For other formats, use standard printer
			return printer.Print(intent)
		},
	}
	stability.MarkStable(c)
	return c
}

// describeIntentProperty prints one property of an intent's payload schema.
func describeIntentProperty(w io.Writer, propName string, prop appengine.IntentProperty) {
	required := ""
	if prop.Required {
		required = " (required)"
	}
	propType := prop.Type
	if propType == "" {
		propType = "(type not declared)"
	}
	fmt.Fprintf(w, "  - %s: %s%s\n", propName, propType, required)
	if prop.Format != "" {
		fmt.Fprintf(w, "    Format: %s\n", prop.Format)
	}

	keyIsName := len(prop.AcceptedKeys) == 1 && prop.AcceptedKeys[0] == propName
	switch {
	case keyIsName:
		if pattern := prop.KeyPattern(propName, propName); pattern != "" {
			fmt.Fprintf(w, "    Pattern: %s\n", pattern)
		}
	case len(prop.AcceptedKeys) == 0:
		fmt.Fprintf(w, "    Accepted keys: none (empty schemas; no payload can satisfy it)\n")
	default:
		fmt.Fprintf(w, "    Accepted keys:\n")
		for _, key := range prop.AcceptedKeys {
			if pattern := prop.KeyPattern(propName, key); pattern != "" {
				fmt.Fprintf(w, "      - %s  (pattern: %s)\n", key, pattern)
			} else {
				fmt.Fprintf(w, "      - %s\n", key)
			}
		}
	}

	if prop.Description != "" {
		fmt.Fprintf(w, "    Description: %s\n", prop.Description)
	}
}

// Declared stable: the invocation and output contract of this command is
// additive-only. Stable is never implied -- see AGENTS.md "Stability Tiers".
