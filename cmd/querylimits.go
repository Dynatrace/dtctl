package cmd

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/dynatrace-oss/dtctl/pkg/config"
	"github.com/dynatrace-oss/dtctl/pkg/output"
)

// noQueryLimitsFlag is the one-shot escape hatch from configured query limits.
// It is registered on every command that honors the query-limits config block.
const noQueryLimitsFlag = "no-query-limits"

// addQueryLimitFlags registers the opt-out flag on a DQL-executing command. The
// individual limit flags themselves stay where they are declared today, since
// their help text and defaults differ per command.
func addQueryLimitFlags(cmd *cobra.Command) {
	cmd.Flags().Bool(noQueryLimitsFlag, false,
		"ignore the query-limits block from the config for this invocation (explicit limit flags still apply)")
}

// resolveQueryLimits determines the per-query caps for one invocation:
//
//	explicit flag -> context query-limits -> global query-limits -> server default
//
// A flag wins only when it was actually passed, so `--max-result-records 0`
// keeps meaning "use the server default" instead of being indistinguishable
// from an unset flag — the reason the resolution is written against
// Flags().Changed rather than against the flag values.
//
// --no-query-limits drops the config layers but keeps explicit flags, so it
// means "ignore what the config says", not "ignore what I just typed". It is
// also the only way to loosen a configured limit: zero means "inherit" at every
// config layer, so a context can tighten a global ceiling but never lift it.
func resolveQueryLimits(cmd *cobra.Command, cfg *config.Config) (config.QueryLimits, error) {
	var base config.QueryLimits
	// A nil config (embedded invocation, or a command run without a usable
	// context) resolves against the server defaults; flags still apply.
	if cfg != nil && !queryLimitsDisabled(cmd) {
		base = cfg.EffectiveQueryLimits()
		// Validate the merged block, so the error names the value actually in
		// effect rather than whichever layer happened to spell it.
		if err := base.Validate(); err != nil {
			return config.QueryLimits{}, err
		}
	}

	lim := base
	f := cmd.Flags()
	// fromConfig tracks the config-sourced values that survived the flag layer,
	// so the verbose line below reports what is actually in effect rather than
	// what the config asked for.
	fromConfig := base
	if f.Changed("default-scan-limit-gbytes") {
		v, _ := f.GetFloat64("default-scan-limit-gbytes")
		lim.ScanLimitGbytes = v
		fromConfig.ScanLimitGbytes = 0
	}
	if f.Changed("max-result-records") {
		v, _ := f.GetInt64("max-result-records")
		lim.MaxResultRecords = v
		fromConfig.MaxResultRecords = 0
	}
	if f.Changed("max-result-bytes") {
		v, _ := f.GetInt64("max-result-bytes")
		lim.MaxResultBytes = v
		fromConfig.MaxResultBytes = 0
	}
	if f.Changed("default-sampling-ratio") {
		v, _ := f.GetFloat64("default-sampling-ratio")
		lim.SamplingRatio = v
		fromConfig.SamplingRatio = 0
	}

	// A cap the caller never typed should not be invisible: a PARTIAL result
	// otherwise looks like it came from a server default, and the advice to
	// raise --default-scan-limit-gbytes reads oddly when the ceiling came from
	// the config file.
	if !fromConfig.IsZero() && verbosity(cmdContext(cmd)) > 0 {
		output.FprintInfo(currentStderr(cmdContext(cmd)), "applying query limits from config: %s (use --%s to ignore them)",
			describeQueryLimits(fromConfig), noQueryLimitsFlag)
	}
	return lim, nil
}

// queryLimitsDisabled reports whether this invocation opted out of the
// configured limits. Commands that do not register the flag never opt out.
func queryLimitsDisabled(cmd *cobra.Command) bool {
	if cmd.Flags().Lookup(noQueryLimitsFlag) == nil {
		return false
	}
	off, _ := cmd.Flags().GetBool(noQueryLimitsFlag)
	return off
}

// describeQueryLimits renders the set limits for a verbose one-liner, naming the
// CLI flags rather than the config keys so the value is actionable as typed.
func describeQueryLimits(lim config.QueryLimits) string {
	var parts []string
	if lim.ScanLimitGbytes != 0 {
		parts = append(parts, fmt.Sprintf("--default-scan-limit-gbytes %g", lim.ScanLimitGbytes))
	}
	if lim.MaxResultRecords != 0 {
		parts = append(parts, fmt.Sprintf("--max-result-records %d", lim.MaxResultRecords))
	}
	if lim.MaxResultBytes != 0 {
		parts = append(parts, fmt.Sprintf("--max-result-bytes %d", lim.MaxResultBytes))
	}
	if lim.SamplingRatio != 0 {
		parts = append(parts, fmt.Sprintf("--default-sampling-ratio %g", lim.SamplingRatio))
	}
	return strings.Join(parts, ", ")
}
