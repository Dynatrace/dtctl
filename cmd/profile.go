package cmd

import (
	"context"
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/dynatrace-oss/dtctl/pkg/config"
)

// ProfileError is returned when a command that is masked by the active profile
// is invoked. Because the command is hidden but still parseable by Cobra, the
// guard produces a specific, signposted error rather than a generic "unknown
// command". It is a surface (topic) error, distinct from a safety (permission)
// error — see docs/dev/COMMAND_PROFILES_DESIGN.md.
type ProfileError struct {
	// Command is the space-joined command path relative to root (e.g. "auth login").
	Command string
	// Profile is the name of the active profile that masks the command.
	Profile string
}

// Headline is the one-line statement of the block, shared by the human-readable
// Error() string and the structured agent-mode ErrorDetail (errorToDetail) so
// the two never drift.
func (e *ProfileError) Headline() string {
	return fmt.Sprintf("command %q is not available in profile %q", e.Command, e.Profile)
}

// Suggestions are the remediation hints, shared with the agent-mode ErrorDetail.
func (e *ProfileError) Suggestions() []string {
	return []string{
		"run 'dtctl commands' to see the available command set",
		"unset " + config.ProfileEnvVar + " to use the full CLI",
	}
}

func (e *ProfileError) Error() string {
	return e.Headline() + "\n\n" +
		"  This profile exposes a reduced command set. Run 'dtctl commands' to see\n" +
		"  what is available, or unset " + config.ProfileEnvVar + " to use the full CLI."
}

// applyProfile masks every command outside the profile's allowlist. Masking
// means (1) setting Hidden — which removes the command from --help, the
// `dtctl commands` catalog, and shell completion in one shot, since all three
// already honor Hidden — and (2) wrapping any runnable command with a guard that
// returns a ProfileError, satisfying hard enforcement (a hidden command is still
// runnable by name in Cobra).
//
// A nil profile is the full command tree (no-op), preserving today's behavior.
// The filter runs once, after the whole command tree is registered and before
// Cobra dispatches — see executeArgs() in root.go.
func applyProfile(root *cobra.Command, p *config.Profile) {
	if p == nil {
		return
	}
	walkCommands(root, func(cmd *cobra.Command) {
		if cmd == root {
			return // never mask the root command itself
		}
		if p.Allows(commandPathRelative(cmd, root)) {
			return
		}
		cmd.Hidden = true
		if cmd.RunE != nil || cmd.Run != nil {
			// Cobra validates Args and parses flags *before* invoking RunE, so a
			// masked command with an arg validator (e.g. ExactArgs) or an unknown
			// flag would otherwise surface a generic "accepts 1 arg(s)" / "unknown
			// flag" error instead of the signposted profile block — leaking the
			// command's existence and arg/flag shape. Neutralize both checks so the
			// guard always wins and the block is the only observable outcome.
			cmd.Args = cobra.ArbitraryArgs
			cmd.DisableFlagParsing = true
			cmd.RunE = blockedRunE(commandPathRelative(cmd, root), p.Name)
			cmd.Run = nil
		}
	})
}

// blockedRunE returns a RunE that reports the command as unavailable under the
// active profile. Masking wraps RunE (rather than removing the command) so we
// can return a specific "not available in profile X" message instead of a
// generic "unknown command". applyProfile disables arg validation and flag
// parsing on the masked command so this guard is the only observable outcome.
func blockedRunE(path, profileName string) func(*cobra.Command, []string) error {
	return func(*cobra.Command, []string) error {
		return &ProfileError{Command: path, Profile: profileName}
	}
}

// completeProfileNames provides shell completion for a --profile flag value:
// the built-in presets plus any user-defined profiles, always including "full".
func completeProfileNames(*cobra.Command, []string, string) ([]string, cobra.ShellCompDirective) {
	names := append([]string{config.ProfileFull}, config.BuiltinProfileNames()...)
	if cfg, err := config.Load(); err == nil {
		for name := range cfg.Profiles {
			names = append(names, name)
		}
	}
	return names, cobra.ShellCompDirectiveNoFileComp
}

// walkCommands invokes fn for cmd and every command in its subtree.
func walkCommands(cmd *cobra.Command, fn func(*cobra.Command)) {
	fn(cmd)
	for _, sub := range cmd.Commands() {
		walkCommands(sub, fn)
	}
}

// commandPathRelative returns a command's path relative to root, space-joined
// (e.g. "get workflows"). This matches the vocabulary the profile allowlist and
// the commands catalog use.
func commandPathRelative(cmd, root *cobra.Command) string {
	return strings.TrimSpace(strings.TrimPrefix(cmd.CommandPath(), root.Name()))
}

// extractFlagValue scans raw args for a value-taking flag written as either
// "--name value" or "--name=value" and returns its value. Cobra has not parsed
// flags yet when the profile is resolved (the filter must shape the tree before
// dispatch), so the --config and --context overrides that influence profile
// selection are read directly from the args here. Returns "" when absent.
//
// Scanning stops at a bare "--" (end-of-flags terminator): a value appearing
// after it is a positional, not a flag. This is a deliberately small parser —
// it does not understand which flags take values, so a "--context" that is
// itself the value of some other flag before "--" could in theory be misread.
// That is acceptable for the two global string flags (--config, --context) it
// serves; Cobra's real parse (which runs later) remains authoritative.
func extractFlagValue(args []string, name string) string {
	long := "--" + name
	eq := long + "="
	for i := range args {
		arg := args[i]
		if arg == "--" {
			return "" // end of flags; anything after is positional
		}
		if v, ok := strings.CutPrefix(arg, eq); ok {
			return v
		}
		if arg == long && i+1 < len(args) {
			return args[i+1]
		}
	}
	return ""
}

// extractContextOverride returns the value of a --context flag in raw args.
func extractContextOverride(args []string) string {
	return extractFlagValue(args, "context")
}

// resolveActiveProfile loads config and resolves the active profile for the
// given (pre-parse) args, honoring --config (which config file to read) and
// --context (which context's binding to use) overrides. It returns (nil, nil)
// for the full command tree, and a non-nil error only when a referenced profile
// name does not exist.
func resolveActiveProfile(ctx context.Context, args []string) (*config.Profile, error) {
	// No usable config → full command tree. The real command will surface any
	// config error later with proper context. Session-backed invocations
	// resolve against the synthetic config: no user-defined profiles and no
	// context binding, but DTCTL_PROFILE (set per request via RunOptions.Env)
	// still selects a built-in preset. See configForArgs in stability.go, which
	// the stability floor shares.
	cfg := configForArgs(ctx, args)
	if cfg == nil {
		return nil, nil
	}
	return cfg.ResolveProfile()
}
