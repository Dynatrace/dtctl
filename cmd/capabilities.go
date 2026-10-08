package cmd

import "fmt"

// Capabilities enumerates the process-level abilities the host grants the
// command tree: spawning a subprocess (or handing the process over entirely, as
// Unix plugin dispatch does via exec), and reaching for the host's disk outside
// the vfs seam. Every such path is gated on one of these, so an embedding
// caller — the service engine, `dtctl serve`, tests — can make it structurally
// unreachable instead of relying on configuration. The CLI binary grants
// everything; embedded callers grant nothing (see
// docs/dev/SERVICE_ENGINE_DESIGN.md).
type Capabilities struct {
	// PluginDispatch allows unknown commands to exec dtctl-* binaries from
	// PATH (full process replacement on Unix).
	PluginDispatch bool
	// ShellAliases allows shell-form aliases ("!...") to run via sh -c.
	ShellAliases bool
	// ApplyHooks allows configured pre-/post-apply hook commands to run.
	ApplyHooks bool
	// Editor allows `edit` commands to spawn $EDITOR.
	Editor bool
	// BrowserOpen allows `open` to spawn the OS URL handler.
	BrowserOpen bool
	// HostDiskSpill allows large results to spill to a file on the host disk
	// (`--spill`, `--spill-to`, and the automatic spill in agent mode). The
	// spilled file and the path returned to the caller are both host state, so
	// an embedded invocation returns its rows inline instead.
	HostDiskSpill bool
	// LongRunningStreams allows commands that can run indefinitely: --watch on
	// get subcommands and --follow on logs. The service engine leaves this
	// false so a streaming request cannot hold the execution slot forever.
	LongRunningStreams bool
	// HostWorkingDirectory allows commands to read the process working
	// directory and walk up from it to the enclosing git repository, which is
	// where the repo scope file (.dtctl-repo-scope.yaml) lives. The working
	// directory and that file are host state: a service request has neither,
	// so an embedded invocation never scopes a query to a repository.
	HostWorkingDirectory bool
}

// AllCapabilities is the CLI default: everything granted.
func AllCapabilities() Capabilities {
	return Capabilities{
		PluginDispatch:       true,
		ShellAliases:         true,
		ApplyHooks:           true,
		Editor:               true,
		BrowserOpen:          true,
		HostDiskSpill:        true,
		LongRunningStreams:   true,
		HostWorkingDirectory: true,
	}
}

// caps holds the granted set for this process. Defaults to the full CLI set;
// embedded callers override via SetCapabilities before executing commands.
// (Folded into the per-execution Runtime by the E1 refactor.)
var caps = AllCapabilities()

// SetCapabilities replaces the granted capability set. It returns the
// previous set so tests can restore it.
func SetCapabilities(c Capabilities) Capabilities {
	prev := caps
	caps = c
	return prev
}

// CapabilityError reports a feature that is not granted in this environment.
// In agent mode it renders as a structured envelope with code
// "capability_disabled".
type CapabilityError struct {
	// Feature is the human-readable name of the blocked ability.
	Feature string
}

func (e *CapabilityError) Error() string {
	return fmt.Sprintf("%s is not available in this environment", e.Feature)
}
