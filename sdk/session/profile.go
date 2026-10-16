package session

import (
	"fmt"
	"strings"
)

// ProfileEnvVar is the environment variable that selects the active command
// profile, taking precedence over any context-bound profile.
const ProfileEnvVar = "DTCTL_PROFILE"

// ProfileFull is the reserved name for the unrestricted command tree. Selecting
// it (via env or context) is equivalent to selecting no profile at all.
const ProfileFull = "full"

// Profile is a default-deny allowlist of commands. It shapes every discovery
// surface at once (--help, `dtctl commands`, shell completion) and hard-blocks
// invocation of any command outside the set. See
// docs/dev/COMMAND_PROFILES_DESIGN.md.
type Profile struct {
	// Name is the profile's key in the profiles map (or the built-in preset
	// name). It is populated at resolution time and never serialized — the map
	// key already carries it.
	Name string `yaml:"-"`
	// Description is a human-readable summary of the profile's purpose.
	Description string `yaml:"description,omitempty"`
	// Commands is the allowlist of command-path prefixes. An entry matches a
	// command when it equals or is a segment-prefix of that command's path, so
	// listing a parent verb (e.g. "describe") includes its whole subtree. There
	// is no denylist — everything not matched (and not always-available) is masked.
	Commands []string `yaml:"commands,omitempty"`
}

// alwaysAvailableCommands is the irreducible core allowed regardless of the
// active profile: the machine-readable catalog an agent bootstraps from and the
// universal help escape hatch. Both are pure discovery — they run no operation,
// read no data, and mutate nothing. Matching is segment-prefix based, so
// "commands" covers "commands howto".
//
// Everything else — including config, ctx, completion, and version — is subject
// to the allowlist. This is deliberate: config/ctx can mutate credentials and
// switch environments, so a locked-down agent profile must be able to withhold
// them. A profile that wants any of these simply lists it (e.g.
// commands: [query, config]).
var alwaysAvailableCommands = []string{
	"commands", // agents must always be able to read the catalog
	"help",     // universal help escape hatch
}

// builtinProfiles are the topical presets shipped with dtctl so products need
// not hand-author YAML. Names are deliberately topical (never permission words
// like "readonly" — that axis belongs to safety levels). The "full" profile is
// handled specially (no filtering) and is therefore not listed here.
//
// User-defined profiles in config take precedence over a preset of the same name.
var builtinProfiles = map[string]Profile{
	"query": {
		Description: "DQL queries plus Davis analyzers for investigation agents",
		Commands:    []string{"query", "get analyzers", "describe analyzer", "exec analyzer", "verify analyzer", "inventory"},
	},
	"investigate": {
		Description: "Read-only incident triage: query, logs, and resource discovery",
		Commands:    []string{"query", "logs", "get", "find", "describe", "inventory"},
	},
}

// BuiltinProfileNames returns the sorted names of the built-in presets (excluding
// the special "full" profile). Used for docs/validation surfaces.
func BuiltinProfileNames() []string {
	names := make([]string, 0, len(builtinProfiles))
	for name := range builtinProfiles {
		names = append(names, name)
	}
	return names
}

// ResolveProfile determines the active profile using the precedence
//
//	DTCTL_PROFILE env  >  context-bound profile  >  none (= full)
//
// It returns nil for the full (unrestricted) command tree — the default and
// backward-compatible behavior. A referenced profile name that does not exist
// (as a user profile or built-in preset) is a fast, explicit error rather than
// a silent fallback to full, which would be a surprising surface expansion.
func (c *Config) ResolveProfile() (*Profile, error) {
	return c.resolveProfile(c.getenv(ProfileEnvVar))
}

// resolveProfile is the testable core of ResolveProfile with the environment
// value injected explicitly.
func (c *Config) resolveProfile(envProfile string) (*Profile, error) {
	name := strings.TrimSpace(envProfile)
	source := ProfileEnvVar
	if name == "" {
		// Fall back to the current context's binding. A missing/invalid context
		// is not an error here — it simply means no context-bound profile.
		if ctx, err := c.CurrentContextObj(); err == nil {
			name = strings.TrimSpace(ctx.Profile)
			source = fmt.Sprintf("context %q", c.CurrentContext)
		}
	}

	if name == "" || name == ProfileFull {
		return nil, nil // full command tree
	}

	p, ok := c.lookupProfile(name)
	if !ok {
		return nil, fmt.Errorf("profile %q (from %s) is not defined; "+
			"define it under 'profiles:' in the config or use one of the built-in profiles (%s)",
			name, source, strings.Join(append([]string{ProfileFull}, BuiltinProfileNames()...), ", "))
	}
	return &p, nil
}

// lookupProfile resolves a profile name to its definition. User-defined profiles
// take precedence over built-in presets of the same name.
func (c *Config) lookupProfile(name string) (Profile, bool) {
	if p, ok := c.Profiles[name]; ok {
		p.Name = name
		return p, true
	}
	if p, ok := builtinProfiles[name]; ok {
		p.Name = name
		return p, true
	}
	return Profile{}, false
}

// ProfileExists reports whether a profile name is resolvable — either "full",
// a user-defined profile, or a built-in preset. Used by config-management
// commands to warn on likely typos when binding a profile to a context.
func (c *Config) ProfileExists(name string) bool {
	if name == ProfileFull {
		return true
	}
	_, ok := c.lookupProfile(name)
	return ok
}

// Allows reports whether a command identified by its space-joined path (relative
// to the root command, e.g. "get workflows") is visible/runnable under this
// profile. A command is allowed when:
//
//  1. it is in the always-available set, or
//  2. it is at or below an allowlisted entry (the entry is a segment-prefix of
//     the path — listing "describe" allows "describe analyzer"), or
//  3. it is an ancestor of an allowlisted entry (the path is a segment-prefix of
//     the entry — a parent verb stays reachable so its allowed child can be run).
//
// Everything else is masked.
func (p *Profile) Allows(path string) bool {
	for _, entry := range alwaysAvailableCommands {
		if segmentPrefix(entry, path) {
			return true
		}
	}
	for _, entry := range p.Commands {
		// Command is at/under an allowed entry.
		if segmentPrefix(entry, path) {
			return true
		}
		// Command is an ancestor of an allowed entry — keep parents reachable so
		// their allowed children can be invoked and appear in the catalog.
		if segmentPrefix(path, entry) {
			return true
		}
	}
	return false
}

// segmentPrefix reports whether prefix matches the leading whole segments of
// path. Segments are split on whitespace, so "get" is a prefix of "get
// workflows" but not of an unrelated "getterm"-style command. An empty prefix
// never matches (the root command is handled separately by the caller).
func segmentPrefix(prefix, path string) bool {
	ps := strings.Fields(prefix)
	xs := strings.Fields(path)
	if len(ps) == 0 || len(ps) > len(xs) {
		return false
	}
	for i, seg := range ps {
		if xs[i] != seg {
			return false
		}
	}
	return true
}
