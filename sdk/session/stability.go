package session

import (
	"fmt"
	"sort"
	"strings"
)

// MinStabilityEnvVar is the environment variable that sets the stability floor,
// taking precedence over any context-bound value. It is deliberately not
// exposed as a command-line flag: in an embedding (the platform service) the
// caller controls argv, so a flag would hand the opt-in to exactly the party
// the floor is meant to constrain.
const MinStabilityEnvVar = "DTCTL_MIN_STABILITY"

// DevelopmentEnvVar is the environment variable that enables development-tier
// features, as a comma-separated list of feature keys (e.g. "account,serve").
// The value "all" enables every registered feature.
const DevelopmentEnvVar = "DTCTL_DEVELOPMENT"

// NoDeprecatedEnvVar makes dtctl behave as if every deprecated command and flag
// had already been removed.
//
// It exists so that a caller can find out *before* the removal release whether
// they still depend on something scheduled to go. A deprecation warning is easy
// to miss in a log; a failing pipeline is not. The same idea as Python's
// `-W error::DeprecationWarning`.
//
// Deliberately not a stability tier: a deprecated command is still stable in
// shape, merely scheduled for removal, so folding it into the ordered axis
// would make it read as a demotion. This is a separate, orthogonal filter.
const NoDeprecatedEnvVar = "DTCTL_NO_DEPRECATED"

// DevelopmentAll is the sentinel that enables every registered development
// feature at once. Convenient for dtctl's own test and development builds.
const DevelopmentAll = "all"

// StabilityLevel is the contract dtctl offers for a command, flag or output
// field: what we promise about its shape over time. It is an ordered axis,
// independent of both the safety level ("what may this command do?") and the
// command profile ("which commands exist here?").
//
// See dtctl-contrib dev/STABILITY_TIERS_DESIGN.md.
type StabilityLevel string

const (
	// StabilityDevelopment is unfinished surface with no guarantees at all. It
	// may be abandoned or may not work. Commands at this level are *not
	// registered* unless explicitly opted in, so they are absent from help,
	// completion and the `dtctl commands` catalog.
	StabilityDevelopment StabilityLevel = "development"
	// StabilityExperimental is shipped and complete enough to use, but may
	// change or be removed in any minor release. Registered and visible, and
	// badged so the absence of a guarantee is never inferred from the name.
	StabilityExperimental StabilityLevel = "experimental"
	// StabilityStable means the invocation *and* output contract are
	// additive-only; removal or incompatible change requires a deprecation
	// cycle. It is never implied: a command is stable only because someone
	// declared it so.
	StabilityStable StabilityLevel = "stable"

	// DefaultStabilityLevel is the rank the empty level takes when a declared
	// level is combined with an absent one. It is the *strongest*, so that an
	// absent declaration contributes nothing to Weakest and a child simply
	// inherits its parent -- "no opinion", not "stable".
	//
	// Do not read this as the tier of an unannotated command. A command that
	// declares nothing resolves to FallbackStabilityLevel; see
	// stability.Effective.
	DefaultStabilityLevel = StabilityStable

	// FallbackStabilityLevel is what a command resolves to when neither it nor
	// any ancestor declares a tier.
	//
	// Experimental, never stable. Stable is a promise, and a promise nobody
	// made is not one dtctl should keep: a command added without a tier would
	// otherwise ship an additive-only guarantee on its first day purely
	// because its author forgot. The build refuses an undeclared command
	// (stability.Lint), so this value is the second line of defence -- and it
	// fails safe, because experimental still runs under the default floor and
	// is only refused where a caller pinned stable.
	FallbackStabilityLevel = StabilityExperimental

	// DefaultMinStability is the floor when a context sets none. Experimental
	// rather than stable so humans and interactive agents keep getting new
	// surface (with a badge) — the whole point of having a middle tier. Only
	// the platform service and CI pipelines pin stable.
	DefaultMinStability = StabilityExperimental
)

// stabilityRanks orders the levels. Higher means a stronger promise.
var stabilityRanks = map[StabilityLevel]int{
	StabilityDevelopment:  0,
	StabilityExperimental: 1,
	StabilityStable:       2,
}

// ValidStabilityLevels returns the levels in ascending order of promise.
func ValidStabilityLevels() []StabilityLevel {
	return []StabilityLevel{StabilityDevelopment, StabilityExperimental, StabilityStable}
}

// IsValid reports whether the level is one of the three tiers. The empty string
// is valid and means "unset" (the caller substitutes its own default).
func (s StabilityLevel) IsValid() bool {
	if s == "" {
		return true
	}
	_, ok := stabilityRanks[s]
	return ok
}

// String renders the level, substituting the default for the empty value.
func (s StabilityLevel) String() string {
	if s == "" {
		return string(DefaultStabilityLevel)
	}
	return string(s)
}

// Rank returns the level's position on the ordered axis. An unset level ranks
// as the default (stable), matching String.
func (s StabilityLevel) Rank() int {
	if s == "" {
		return stabilityRanks[DefaultStabilityLevel]
	}
	return stabilityRanks[s]
}

// AtLeast reports whether s offers a promise at least as strong as floor.
func (s StabilityLevel) AtLeast(floor StabilityLevel) bool {
	return s.Rank() >= floor.Rank()
}

// Weakest returns whichever level makes the weaker promise. It is how a
// command's *effective* stability is derived from its own annotation and those
// of its ancestors: a stable subcommand under an experimental verb is stable in
// name only.
func Weakest(a, b StabilityLevel) StabilityLevel {
	if b.Rank() < a.Rank() {
		return b
	}
	return a
}

// ParseStabilityLevel validates and normalizes a level written by a user (in
// config or an environment variable).
func ParseStabilityLevel(s string) (StabilityLevel, error) {
	lvl := StabilityLevel(strings.ToLower(strings.TrimSpace(s)))
	if lvl == "" {
		return "", nil
	}
	if !lvl.IsValid() {
		names := make([]string, 0, len(stabilityRanks))
		for _, l := range ValidStabilityLevels() {
			names = append(names, string(l))
		}
		return "", fmt.Errorf("invalid stability level %q; valid levels are %s",
			s, strings.Join(names, ", "))
	}
	return lvl, nil
}

// ResolveMinStability determines the active stability floor using the precedence
//
//	DTCTL_MIN_STABILITY env  >  context-bound min-stability  >  DefaultMinStability
//
// An invalid value is a fast, explicit error rather than a silent fallback,
// which for a floor would be a surprising *widening* of the surface.
func (c *Config) ResolveMinStability() (StabilityLevel, error) {
	return c.resolveMinStability(c.getenv(MinStabilityEnvVar))
}

// resolveMinStability is the testable core of ResolveMinStability with the
// environment value injected explicitly.
func (c *Config) resolveMinStability(envValue string) (StabilityLevel, error) {
	if raw := strings.TrimSpace(envValue); raw != "" {
		lvl, err := ParseStabilityLevel(raw)
		if err != nil {
			return "", fmt.Errorf("%s: %w", MinStabilityEnvVar, err)
		}
		return lvl, nil
	}
	if ctx, err := c.CurrentContextObj(); err == nil && ctx.MinStability != "" {
		lvl, err := ParseStabilityLevel(string(ctx.MinStability))
		if err != nil {
			return "", fmt.Errorf("context %q: min-stability: %w", c.CurrentContext, err)
		}
		return lvl, nil
	}
	return DefaultMinStability, nil
}

// StabilityExceptions returns the current context's per-command and per-flag
// exceptions to the floor. Each entry is a command path, optionally suffixed
// with a single flag ("inventory", "query --decode-snapshots").
//
// Exceptions live on the context rather than in a profile deliberately: a
// profile that named a tier would be encoding another axis's intent, and
// because profiles are reusable topical sets, one deployment's risk acceptance
// would propagate to every context binding that profile.
func (c *Config) StabilityExceptions() []string {
	ctx, err := c.CurrentContextObj()
	if err != nil {
		return nil
	}
	return ctx.StabilityExceptions
}

// EnabledDevelopmentFeatures returns the set of development-tier feature keys
// the caller has opted into, merging the environment variable with the config's
// `development` map. The environment wins per key, so a CI job can enable a
// feature without editing config.
//
// The returned set may contain DevelopmentAll, which callers treat as "every
// registered feature".
func (c *Config) EnabledDevelopmentFeatures() map[string]bool {
	return c.enabledDevelopmentFeatures(c.getenv(DevelopmentEnvVar))
}

// enabledDevelopmentFeatures is the testable core of
// EnabledDevelopmentFeatures with the environment value injected explicitly.
func (c *Config) enabledDevelopmentFeatures(envValue string) map[string]bool {
	enabled := make(map[string]bool)
	for key, on := range c.Development {
		if on {
			enabled[normalizeFeatureKey(key)] = true
		}
	}
	for _, key := range strings.Split(envValue, ",") {
		key = normalizeFeatureKey(key)
		if key == "" {
			continue
		}
		// An explicit off-value for a single key ("account=off") removes it.
		if name, value, ok := strings.Cut(key, "="); ok {
			switch strings.TrimSpace(value) {
			case "", "0", "false", "no", "off":
				delete(enabled, strings.TrimSpace(name))
			default:
				enabled[strings.TrimSpace(name)] = true
			}
			continue
		}
		enabled[key] = true
	}
	return enabled
}

// DevelopmentEnvSet reports whether DTCTL_DEVELOPMENT is present in the
// environment at all, including when it is set to an empty or off value. A
// caller who has set it has evidently been told the mechanism exists, which is
// the signal used to decide whether a disabled development command explains
// itself or stays silent.
func (c *Config) DevelopmentEnvSet() bool {
	_, ok := c.lookupEnv(DevelopmentEnvVar)
	return ok
}

// NoDeprecated reports whether this context refuses deprecated surface, using
// the precedence
//
//	DTCTL_NO_DEPRECATED env  >  context no-deprecated  >  off
//
// The environment wins in *both* directions, unlike the floor where an empty
// value falls through: a pipeline turning the mode on for one run is the
// motivating case, and the inverse -- one run that needs the deprecated
// command it is in the middle of migrating off -- has to be expressible
// without editing the config it shares with every other run.
//
// Unlike a bad floor, a misread value here cannot widen anything, so there is
// no error return: the mode only ever removes surface.
func (c *Config) NoDeprecated() bool {
	if v, ok := c.lookupEnv(NoDeprecatedEnvVar); ok {
		return truthyEnvValue(v)
	}
	ctx, err := c.CurrentContextObj()
	if err != nil {
		return false
	}
	return ctx.NoDeprecated
}

// truthyEnvValue reads a boolean environment variable, using the same off-value
// vocabulary as a development feature key so the two cannot disagree about what
// "off" looks like.
func truthyEnvValue(v string) bool {
	switch strings.TrimSpace(strings.ToLower(v)) {
	case "", "0", "false", "no", "off":
		return false
	default:
		return true
	}
}

// SetDevelopmentFeature turns a development feature on or off in the config's
// `development` map, creating the map on first use. Passing false records an
// explicit off rather than deleting the key, so `config view` shows what was
// considered and then declined.
func (c *Config) SetDevelopmentFeature(key string, on bool) {
	key = normalizeFeatureKey(key)
	if c.Development == nil {
		c.Development = make(map[string]bool)
	}
	c.Development[key] = on
}

// DevelopmentFeatureNames returns the sorted keys of the config's development
// map, whether enabled or not.
func (c *Config) DevelopmentFeatureNames() []string {
	names := make([]string, 0, len(c.Development))
	for key := range c.Development {
		names = append(names, key)
	}
	sort.Strings(names)
	return names
}

// normalizeFeatureKey accepts both the bare feature key ("account") and the
// config-style dotted form ("development.account"), returning the bare key.
func normalizeFeatureKey(key string) string {
	key = strings.ToLower(strings.TrimSpace(key))
	return strings.TrimPrefix(key, "development.")
}
