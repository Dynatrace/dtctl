package cmd

import (
	"context"

	"github.com/spf13/cobra"

	"github.com/dynatrace-oss/dtctl/pkg/output"
	"github.com/dynatrace-oss/dtctl/pkg/resources/platform"
	"github.com/dynatrace-oss/dtctl/pkg/stability"
)

// usePlatformDescribeTextView reports whether to render the human-readable KV
// text view. Agent mode always takes the structured path (outputFormat stays at
// its "table" default when --agent is set, so the check cannot be outputFormat alone).
func usePlatformDescribeTextView(ctx context.Context) bool {
	if agentMode(ctx) {
		return false
	}
	// wide selects extra columns of the describe table, not a different view
	// (same reasoning as describe_api.go:117).
	return outputFormat(ctx) == "" || outputFormat(ctx) == "table" || outputFormat(ctx) == "wide"
}

// describeEnvironmentCmd shows detailed environment information
var describeEnvironmentCmd = newDescribeEnvironmentCmd()

func newDescribeEnvironmentCmd() *cobra.Command {
	c := &cobra.Command{
		Use:     "environment",
		Aliases: []string{"env"},
		Short:   "Show details of the current environment",
		Args:    cobra.NoArgs,
		Long: `Show detailed information about the current Dynatrace environment.

Examples:
  # Describe the current environment
  dtctl describe environment
`,
		RunE: func(cmd *cobra.Command, args []string) error {
			_, c, printer, err := setup(cmdContext(cmd))
			if err != nil {
				return err
			}

			h := platform.NewHandler(c)
			info, err := h.GetEnvironment()
			if err != nil {
				return err
			}

			if usePlatformDescribeTextView(cmdContext(cmd)) {
				const w = 12
				output.FprintDescribeKV(currentStdout(cmdContext(cmd)), "ID:", w, "%s", info.EnvironmentID)
				output.FprintDescribeKV(currentStdout(cmdContext(cmd)), "Type:", w, "%s", info.Type)
				output.FprintDescribeKV(currentStdout(cmdContext(cmd)), "State:", w, "%s", info.State)
				if !info.CreateTime.IsZero() {
					output.FprintDescribeKV(currentStdout(cmdContext(cmd)), "Created:", w, "%s", info.CreateTime.Format("2006-01-02"))
				}
				if !info.BlockTime.IsZero() {
					output.FprintDescribeKV(currentStdout(cmdContext(cmd)), "Block Time:", w, "%s", info.BlockTime.Format("2006-01-02"))
				}
				return nil
			}

			enrichAgent(printer, "describe", "environment")
			return printer.Print(info)
		},
	}
	stability.MarkStable(c)
	return c
}

// describeLicenseCmd shows detailed license information
var describeLicenseCmd = newDescribeLicenseCmd()

func newDescribeLicenseCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "license",
		Short: "Show details of the environment license",
		Args:  cobra.NoArgs,
		Long: `Show detailed license information for the current Dynatrace environment.

Examples:
  # Describe the environment license
  dtctl describe license
`,
		RunE: func(cmd *cobra.Command, args []string) error {
			_, c, printer, err := setup(cmdContext(cmd))
			if err != nil {
				return err
			}

			h := platform.NewHandler(c)
			lic, err := h.GetLicense()
			if err != nil {
				return err
			}

			if usePlatformDescribeTextView(cmdContext(cmd)) {
				const w = 24
				output.FprintDescribeKV(currentStdout(cmdContext(cmd)), "Trial:", w, "%v", lic.Trial)
				output.FprintDescribeKV(currentStdout(cmdContext(cmd)), "Platform Subscription:", w, "%v", lic.PlatformSubscription)
				return nil
			}

			enrichAgent(printer, "describe", "license")
			return printer.Print(lic)
		},
	}
	stability.MarkStable(c)
	return c
}

// Declared stable: the invocation and output contract of these commands is
// additive-only. Stable is never implied -- see AGENTS.md "Stability Tiers".
func init() {
}
