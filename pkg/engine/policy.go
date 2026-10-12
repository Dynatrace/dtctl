package engine

// unsupportedCommands are the top-level dtctl commands the engine removes from
// the surface: they manage host-local state (config file, keyring, shell,
// installed tools) that does not exist for a service request, or would nest
// the service in itself. Everything else — the entire resource surface (get,
// describe, create, apply, delete, query, exec, ...) — runs as-is.
//
// A blocked command is hidden from `dtctl commands` and help, and invoking it
// returns the stable agent-mode error code "unsupported_in_service" with the
// reason below (see cmd.UnsupportedCommandError).
//
// Note the axis: this list is about the *environment* (service vs. host CLI).
// What a request may do to its tenant is governed separately by the
// per-request safety level, and its visible command set optionally by a
// per-request profile.
var unsupportedCommands = map[string]string{
	"config":     "it manages the local dtctl config file; a service request carries its environment and token directly",
	"ctx":        "it switches between local config contexts; a service request targets exactly one environment",
	"auth":       "it manages browser-based OAuth login and locally stored credentials; a service request authenticates with its own token",
	"account":    "it administers platform accounts using locally stored account credentials",
	"alias":      "it manages command aliases in the local dtctl config file",
	"edit":       "it opens an interactive editor",
	"plugin":     "it manages dtctl plugin executables installed on the local machine",
	"skills":     "it installs skill files for locally installed AI coding assistants",
	"doctor":     "it diagnoses the local dtctl installation and config",
	"completion": "it generates shell completion scripts for a local shell",
	"serve":      "the service cannot be nested inside itself",
	"repo-scope": "it reads and writes a file in the git repository around the local working directory, which a service request does not have",
	// inspect is a reader for files dtctl spilled to the local disk. A service
	// request never spills (the HostDiskSpill capability is not granted, so
	// results come back inline), and the path it takes would resolve on the
	// server's disk rather than in the request — host state on both counts.
	"inspect": "it reads result files spilled to the local disk; a service request receives its rows inline instead",
}

// UnsupportedCommands returns the top-level commands the engine blocks, keyed
// by command name with the human-readable reason as value. The map is a copy;
// the policy itself is fixed.
func UnsupportedCommands() map[string]string {
	out := make(map[string]string, len(unsupportedCommands))
	for k, v := range unsupportedCommands {
		out[k] = v
	}
	return out
}
