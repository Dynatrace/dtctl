package cmd

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/dynatrace-oss/dtctl/pkg/output"
	"github.com/dynatrace-oss/dtctl/pkg/resources/edgeconnect"
	"github.com/dynatrace-oss/dtctl/pkg/safety"
	"github.com/dynatrace-oss/dtctl/pkg/stability"
	"github.com/dynatrace-oss/dtctl/pkg/util/format"
)

// createEdgeConnectCmd creates an EdgeConnect
var createEdgeConnectCmd = newCreateEdgeConnectCmd()

func newCreateEdgeConnectCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "edgeconnect --name <name> [--host-patterns <patterns>]",
		Short: "Create an EdgeConnect configuration",
		Long: `Create a new EdgeConnect configuration.

Examples:
  # Create an EdgeConnect with host patterns
  dtctl create edgeconnect --name my-edgeconnect --host-patterns "*.internal.example.com,api.example.com"

  # Create from a file
  dtctl create edgeconnect -f edgeconnect.yaml

  # Dry run to preview
  dtctl create edgeconnect --name my-edgeconnect --host-patterns "*.example.com" --dry-run
`,
		Aliases: []string{"ec"},
		RunE: func(cmd *cobra.Command, args []string) error {
			file, _ := cmd.Flags().GetString("file")
			name, _ := cmd.Flags().GetString("name")
			hostPatterns, _ := cmd.Flags().GetString("host-patterns")

			var req edgeconnect.EdgeConnectCreate

			if file != "" {
				// Read from file
				fileData, err := readFileFlag(cmdContext(cmd), "file", file)
				if err != nil {
					return fmt.Errorf("failed to read file: %w", err)
				}

				jsonData, err := format.ValidateAndConvert(fileData)
				if err != nil {
					return fmt.Errorf("invalid file format: %w", err)
				}

				if err := json.Unmarshal(jsonData, &req); err != nil {
					return fmt.Errorf("failed to parse EdgeConnect definition: %w", err)
				}
			} else {
				// Use flags
				if name == "" {
					return fmt.Errorf("--name is required (or use -f to specify a file)")
				}

				var patterns []string
				if hostPatterns != "" {
					patterns = strings.Split(hostPatterns, ",")
					for i := range patterns {
						patterns[i] = strings.TrimSpace(patterns[i])
					}
				}

				req = edgeconnect.EdgeConnectCreate{
					Name:         name,
					HostPatterns: patterns,
				}
			}

			// Handle dry-run
			if dryRun(cmdContext(cmd)) {
				report := newDryRunReport(cmd).
					Linef("Dry run: would create EdgeConnect").
					Field("Name", "%s", req.Name)
				if len(req.HostPatterns) > 0 {
					report.Field("Host Patterns", "%s", strings.Join(req.HostPatterns, ", "))
				}
				return report.Print()
			}

			_, c, err := setupWithSafety(cmdContext(cmd), safety.OperationCreate)
			if err != nil {
				return err
			}

			handler := edgeconnect.NewHandler(c)

			result, err := handler.Create(req)
			if err != nil {
				return fmt.Errorf("failed to create EdgeConnect: %w", err)
			}

			output.FprintSuccess(currentStderr(cmdContext(cmd)), "EdgeConnect %q created (ID: %s)", result.Name, result.ID)
			if result.OAuthClientSecret != "" {
				output.FprintInfo(currentStderr(cmdContext(cmd)), "\nOAuth Client Credentials (save these, the secret won't be shown again):")
				output.FprintInfo(currentStderr(cmdContext(cmd)), "  Client ID:     %s", result.OAuthClientID)
				output.FprintInfo(currentStderr(cmdContext(cmd)), "  Client Secret: %s", result.OAuthClientSecret)
				if result.OAuthClientResource != "" {
					output.FprintInfo(currentStderr(cmdContext(cmd)), "  Resource:      %s", result.OAuthClientResource)
				}
			}
			return nil
		},
	}
	c.Flags().StringP("file", "f", "", "file containing EdgeConnect definition, or - for stdin")
	c.Flags().String("name", "", "EdgeConnect name (RFC 1123 compliant, max 50 chars)")
	c.Flags().String("host-patterns", "", "comma-separated list of host patterns")
	stability.MarkStable(c)
	// Not needed with -f: only an explicitly empty value is rejected here.
	rejectEmptyFlag(c, "name")
	return c
}

func init() {
	// EdgeConnect flags
}

// Declared stable: the invocation and output contract of this command is
// additive-only. Stable is never implied -- see AGENTS.md "Stability Tiers".
func init() {
}
