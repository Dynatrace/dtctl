package cmd

import (
	"fmt"

	"github.com/spf13/cobra"
)

// UnsupportedCommandError is returned when an invocation names a top-level
// command that the embedding caller has removed from the surface via
// RunOptions.BlockedCommands — commands that only make sense on a host CLI
// (config, ctx, auth, ...) and are meaningless or unsafe inside a service.
// It is a surface error like ProfileError, distinct from safety (permission)
// and capability (process-ability) errors; agent mode renders it with the
// stable code "unsupported_in_service" (see errorToDetail).
type UnsupportedCommandError struct {
	// Command is the space-joined command path relative to root (e.g. "config view").
	Command string
	// Reason says why the command is out of scope for this environment.
	Reason string
}

// Headline is the one-line statement of the block, shared by the
// human-readable Error() string and the structured agent-mode ErrorDetail so
// the two never drift.
func (e *UnsupportedCommandError) Headline() string {
	return fmt.Sprintf("command %q is not supported in this environment", e.Command)
}

// Suggestions are the remediation hints, shared with the agent-mode ErrorDetail.
func (e *UnsupportedCommandError) Suggestions() []string {
	return []string{
		e.Reason,
		"run 'dtctl commands' to see the available command set",
	}
}

func (e *UnsupportedCommandError) Error() string {
	return e.Headline() + "\n\n" +
		"  " + e.Reason + ". Run 'dtctl commands' to see what is available here,\n" +
		"  or run this command with a local dtctl installation."
}

// runBlocked holds the active invocation's blocked-command set (top-level
// command name → reason). Guarded by runMu like the rest of the per-invocation
// state; nil means the full surface (the CLI default).
var runBlocked map[string]string

// applyBlockedCommands masks every subtree whose top-level command name is in
// blocked, using the same mechanics as the profile mask (applyProfile): Hidden
// removes the command from --help, the `dtctl commands` catalog, and shell
// completion in one shot, and runnable nodes are wrapped with a guard that
// returns an UnsupportedCommandError. Arg validation and flag parsing are
// neutralized so the guard is the only observable outcome — a blocked
// command's arg/flag shape must not leak through generic Cobra errors.
//
// The restriction composes with profiles: both masks apply per run and the
// pristine-tree restore (restorePristineTree) undoes them before the next.
func applyBlockedCommands(root *cobra.Command, blocked map[string]string) {
	if len(blocked) == 0 {
		return
	}
	for _, top := range root.Commands() {
		reason, ok := blocked[top.Name()]
		if !ok {
			continue
		}
		walkCommands(top, func(cmd *cobra.Command) {
			markSurfaceMasked(cmd)
			cmd.Hidden = true
			// Unlike the profile mask, guard even non-runnable parents: a
			// blocked bare parent (e.g. `config`) would otherwise print its
			// help with exit 0, leaking the subtree and reporting success.
			cmd.Args = cobra.ArbitraryArgs
			cmd.DisableFlagParsing = true
			cmd.RunE = unsupportedRunE(commandPathRelative(cmd, root), reason)
			cmd.Run = nil
		})
	}
}

// unsupportedRunE returns a RunE that reports the command as unsupported in
// this environment. Masking wraps RunE (rather than removing the command) so
// the caller gets a specific, signposted error instead of a generic "unknown
// command".
func unsupportedRunE(path, reason string) func(*cobra.Command, []string) error {
	return func(*cobra.Command, []string) error {
		return &UnsupportedCommandError{Command: path, Reason: reason}
	}
}
