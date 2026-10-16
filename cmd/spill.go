package cmd

import (
	"fmt"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/dynatrace-oss/dtctl/pkg/config"
	"github.com/dynatrace-oss/dtctl/pkg/exec"
	"github.com/dynatrace-oss/dtctl/pkg/output"
	"github.com/dynatrace-oss/dtctl/sdk/httpclient"
)

// spillExplicitlyRequested reports whether the command line asked for a spill,
// as opposed to leaving it to the mode default. --spill-format and
// --spill-threshold only shape a spill that happens anyway, so they do not
// count — they are inert here exactly as they are under --spill=never.
func spillExplicitlyRequested(cmd *cobra.Command) bool {
	if to, _ := cmd.Flags().GetString("spill-to"); to != "" {
		return true
	}
	if !cmd.Flags().Changed("spill") {
		return false
	}
	val, _ := cmd.Flags().GetString("spill")
	return normalizeMode(val) != string(exec.SpillNever)
}

// resolveSpillOptions resolves the effective result-spill settings using the
// precedence flag → env → context-config → global-config → built-in default
// (D15), and enforces the fixed flag-conflict rules (D25). It emits a warning
// (not an error) for flags that are inert under --spill=never.
func resolveSpillOptions(cmd *cobra.Command, cfg *config.Config) (exec.SpillOptions, error) {
	// A nil config (e.g. `dtctl inspect` on a local file with no usable context)
	// resolves against the built-in defaults; flags and env vars still apply.
	var base config.SpillConfig
	if cfg != nil {
		base = cfg.EffectiveSpillConfig()
	}

	// Spilling writes results to the host's disk and hands the caller a path to
	// read later, so it needs the HostDiskSpill capability. An embedded
	// invocation has no host disk of its own: the file would land on the
	// *server's* disk (durable, and visible to later requests) while the path in
	// the response would be unreadable by the caller. Ungranted, spilling is off
	// — silently when it would only have been automatic, loudly when the command
	// line asked for it, since silently inlining a result the caller asked to
	// spill would misreport what happened.
	if !currentCaps(cmdContext(cmd)).HostDiskSpill {
		if spillExplicitlyRequested(cmd) {
			return exec.SpillOptions{}, &CapabilityError{Feature: "result spilling to disk"}
		}
		return exec.SpillOptions{Mode: exec.SpillNever}, nil
	}

	spillChanged := cmd.Flags().Changed("spill")
	spillVal, _ := cmd.Flags().GetString("spill")
	toPath, _ := cmd.Flags().GetString("spill-to")
	formatChanged := cmd.Flags().Changed("spill-format")
	formatVal, _ := cmd.Flags().GetString("spill-format")
	thresholdChanged := cmd.Flags().Changed("spill-threshold")
	thresholdVal, _ := cmd.Flags().GetString("spill-threshold")

	// Resolve mode: flag → env → config → default (auto in agent mode, else never).
	var mode string
	switch {
	case spillChanged:
		mode = normalizeMode(spillVal)
	case getenv(cmdContext(cmd), "DTCTL_SPILL") != "":
		mode = normalizeMode(getenv(cmdContext(cmd), "DTCTL_SPILL"))
	case base.Mode != "":
		mode = normalizeMode(base.Mode)
	case agentMode(cmdContext(cmd)):
		mode = string(exec.SpillAuto)
	default:
		mode = string(exec.SpillNever)
	}
	if !isValidMode(mode) {
		return exec.SpillOptions{}, fmt.Errorf("invalid --spill value %q (use auto, always, or never)", mode)
	}

	// --spill-to implies always; an explicit --spill=never with --spill-to is
	// contradictory and fails fast (D25).
	if toPath != "" {
		if spillChanged && mode == string(exec.SpillNever) {
			return exec.SpillOptions{}, fmt.Errorf("--spill-to conflicts with --spill=never (contradictory intent)")
		}
		mode = string(exec.SpillAlways)
	}

	opts := exec.SpillOptions{Mode: exec.SpillMode(mode), ToPath: toPath}

	// Dir: env → config (no flag). A user-chosen dir opts out of managed privacy
	// (handled downstream, D25).
	if d := getenv(cmdContext(cmd), "DTCTL_SPILL_DIR"); d != "" {
		opts.Dir = d
	} else {
		opts.Dir = base.Dir
	}

	// Format: flag → config.
	if formatChanged && formatVal != "" {
		opts.Format = strings.ToLower(formatVal)
	} else {
		opts.Format = strings.ToLower(base.Format)
	}

	// Threshold: flag → config → default.
	thrStr := base.Threshold
	if thresholdChanged && thresholdVal != "" {
		thrStr = thresholdVal
	}
	if thrStr != "" {
		n, err := exec.ParseByteSize(thrStr)
		if err != nil {
			return opts, fmt.Errorf("invalid spill threshold: %w", err)
		}
		opts.Threshold = n
	} else {
		opts.Threshold = exec.DefaultSpillThresholdBytes
	}

	// TTL: config → default.
	if base.TTL != "" {
		d, err := time.ParseDuration(base.TTL)
		if err != nil {
			return opts, fmt.Errorf("invalid spill ttl %q: %w", base.TTL, err)
		}
		opts.TTL = d
	} else {
		opts.TTL = output.DefaultSpillTTL
	}

	// Conflict: an explicit --spill-format that disagrees with the --spill-to
	// extension — pick one source of truth (D25).
	if toPath != "" && formatChanged && formatVal != "" {
		if extFmt := spillFormatFromExt(toPath); extFmt != "" && extFmt != opts.Format {
			return opts, fmt.Errorf("--spill-format %q disagrees with --spill-to extension %q; specify only one", opts.Format, extFmt)
		}
	}

	// Inert flags under --spill=never are warned, not errored (D25).
	if opts.Mode == exec.SpillNever && (formatChanged || thresholdChanged) {
		output.FprintWarning(currentStderr(cmdContext(cmd)), "--spill-format/--spill-threshold are ignored because spilling is disabled (--spill=never)")
	}

	return opts, nil
}

// addSpillFlags registers the shared --spill* flag namespace (D4) on a command.
// Both `query` and `inspect` honour the same spill controls so an oversized
// result — a Grail query result, or a wide/large `inspect` row window (IN8) —
// collapses to a managed file + summary instead of flooding the agent context.
func addSpillFlags(cmd *cobra.Command) {
	cmd.Flags().String("spill", "", `spill a large result to a local file and return a summary instead of the rows
bare --spill = always; --spill=auto spills above --spill-threshold; --spill=never forces rows
(default: auto in agent mode, never otherwise)`)
	cmd.Flags().Lookup("spill").NoOptDefVal = "always"
	cmd.Flags().String("spill-to", "", "explicit spill destination file (implies --spill=always; format inferred from extension)")
	cmd.Flags().String("spill-format", "", "spill file format when spilling to the default dir: jsonl|json|csv|parquet (default jsonl)")
	cmd.Flags().String("spill-threshold", "", "serialised output size above which a result spills, e.g. 50KB (default 50KB)")

	registerFlagCompletion(cmd, "spill", func(_ *cobra.Command, _ []string, _ string) ([]string, cobra.ShellCompDirective) {
		return []string{
			"auto\tspill above the threshold, inline below",
			"always\talways spill",
			"never\tnever spill (rows inline)",
		}, cobra.ShellCompDirectiveNoFileComp
	})
	registerFlagCompletion(cmd, "spill-format", func(_ *cobra.Command, _ []string, _ string) ([]string, cobra.ShellCompDirective) {
		return []string{"jsonl", "json", "csv", "parquet"}, cobra.ShellCompDirectiveNoFileComp
	})
}

// spillProvenance returns the tenant id (environment subdomain) and the active
// context name for the spill manifest (D9). Both are best-effort: a missing or
// unparseable environment simply yields an empty tenant id.
func spillProvenance(cfg *config.Config) (tenantID, contextName string) {
	// A nil config (local-only `inspect` with no usable config) carries no
	// provenance — return zero values rather than nil-panicking, mirroring the
	// nil tolerance of resolveSpillOptions.
	if cfg == nil {
		return "", ""
	}
	contextName = cfg.CurrentContext
	if ctx, err := cfg.CurrentContextObj(); err == nil {
		if sub, serr := httpclient.ExtractSubdomain(ctx.Environment); serr == nil {
			tenantID = sub
		}
	}
	return tenantID, contextName
}

func normalizeMode(s string) string {
	return strings.ToLower(strings.TrimSpace(s))
}

func isValidMode(mode string) bool {
	switch exec.SpillMode(mode) {
	case exec.SpillAuto, exec.SpillAlways, exec.SpillNever:
		return true
	default:
		return false
	}
}

// spillWritesParquet reports whether the resolved spill will produce a Parquet
// file. An explicit --spill-to extension wins; otherwise the configured format
// (which falls back to the non-Parquet default when unset) decides. Used to
// auto-request DQL type info so the Parquet schema is built from real types.
func spillWritesParquet(opts exec.SpillOptions) bool {
	if opts.ToPath != "" {
		if ext := spillFormatFromExt(opts.ToPath); ext != "" {
			return ext == "parquet"
		}
		// Extension-less --spill-to falls back to the configured format below.
	}
	return strings.EqualFold(opts.Format, "parquet")
}

// spillFormatFromExt maps a destination file extension to a spill format, or ""
// for an unrecognised/missing extension. It delegates to the shared
// output.FormatForExt so the supported-extension set lives in one place.
func spillFormatFromExt(path string) string {
	return output.FormatForExt(path)
}
