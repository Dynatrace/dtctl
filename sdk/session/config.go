package session

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/adrg/xdg"
	"gopkg.in/yaml.v3"

	"github.com/dynatrace-oss/dtctl/sdk/urls"
)

// Config represents the dtctl configuration
type Config struct {
	APIVersion     string            `yaml:"apiVersion"`
	Kind           string            `yaml:"kind"`
	CurrentContext string            `yaml:"current-context"`
	Contexts       []NamedContext    `yaml:"contexts"`
	Tokens         []NamedToken      `yaml:"tokens"`
	Preferences    Preferences       `yaml:"preferences"`
	Aliases        map[string]string `yaml:"aliases,omitempty"`
	// Spill holds the global result-spill settings (D15). Per-context overrides
	// live on Context.Spill.
	Spill SpillConfig `yaml:"spill,omitempty"`
	// QueryLimits holds the global per-query execution caps. Per-context
	// overrides live on Context.QueryLimits.
	QueryLimits QueryLimits `yaml:"query-limits,omitempty"`
	// Profiles is the set of named command profiles (default-deny allowlists of
	// commands). A profile is selected via DTCTL_PROFILE or a context binding;
	// see profile.go and docs/dev/COMMAND_PROFILES_DESIGN.md.
	Profiles map[string]Profile `yaml:"profiles,omitempty"`
	// Development enables development-tier features by key (e.g.
	// "account": true). Development commands are not registered at all unless
	// opted in here or via DTCTL_DEVELOPMENT; see stability.go.
	Development map[string]bool `yaml:"development,omitempty"`

	// localPath is the path of the auto-discovered local .dtctl.yaml this
	// config was loaded from, if any. Empty when loaded from the global config
	// or from an explicit --config file. Unexported so it is never serialized.
	localPath string
	// ignoredExecKeys is true when code-execution keys (aliases and/or apply
	// hooks) are present in an auto-discovered local config. Such keys are
	// loaded into the struct (so they round-trip safely through save/edit) but
	// are never honored at runtime — alias resolution and hook execution check
	// IsLocal() and skip them. See markLocal, GetPreApplyHook, resolveAlias.
	ignoredExecKeys bool
	// ignoredEnvRefs is true when key config values (environment URLs, inline
	// token values) in an auto-discovered local config contain literal '$'
	// characters that were not expanded because expansion is skipped for
	// untrusted local configs. The caller can surface a warning so the user
	// knows to use --config / DTCTL_CONFIG if they intended expansion.
	ignoredEnvRefs bool
	// globalBindings maps token-ref names to what the global config records for
	// them: the canonical hostname of the associated environment and that
	// context's effective safety level. When IsLocal() is true, GetToken checks
	// the host so a rogue local config cannot redirect stored credentials to an
	// attacker-controlled host, and GetEffectiveSafetyLevel clamps to the level
	// so it cannot escalate past what the credential's owner permits. Keyed by
	// token-ref rather than context name because that is the binding a local
	// config actually borrows — it is free to rename its context.
	globalBindings map[string]globalBinding
	// rawEnvironments maps context name to the environment URL as written in an
	// auto-discovered local config, for every context whose URL carries a '$' —
	// whether or not the reference was adopted. SaveTo writes these back so
	// editing a committed .dtctl.yaml does not bake the editing developer's
	// expansion into it, and EnvironmentUnresolved compares against them to
	// tell an unadopted reference from a literal URL. See
	// resolveLocalEnvironments, restoreRawEnvironments.
	rawEnvironments map[string]string
	// inlineOnly is set by SealInlineCredentials to restrict token resolution to
	// the inline Tokens list. When true, GetToken never consults the keyring,
	// file store, or OAuth machinery — only the values carried in the struct.
	inlineOnly bool
	// env, when set, answers the environment lookups this Config's methods
	// make instead of the process environment. See WithEnv.
	env func(key string) (string, bool)
}

// SealInlineCredentials marks this config as inline-only: token resolution will
// consult only c.Tokens and never touch the keyring, file store, or OAuth
// machinery. Call this on synthetic session configs to prevent host credential
// stores from overriding request-supplied tokens.
func (c *Config) SealInlineCredentials() {
	c.inlineOnly = true
}

// InlineCredentialsOnly reports whether the config has been sealed via
// SealInlineCredentials.
func (c *Config) InlineCredentialsOnly() bool {
	return c.inlineOnly
}

// NamedContext holds a context with its name
type NamedContext struct {
	Name    string  `yaml:"name" table:"NAME"`
	Context Context `yaml:"context" table:"-"`
}

// SafetyLevel defines the allowed operations for a context
type SafetyLevel string

const (
	// SafetyLevelReadOnly allows only read operations
	SafetyLevelReadOnly SafetyLevel = "readonly"
	// SafetyLevelReadWriteMine allows create/update/delete of own resources only
	SafetyLevelReadWriteMine SafetyLevel = "readwrite-mine"
	// SafetyLevelReadWriteAll allows modification of all resources (no bucket deletion)
	SafetyLevelReadWriteAll SafetyLevel = "readwrite-all"
	// SafetyLevelDangerouslyUnrestricted allows all operations including data deletion
	SafetyLevelDangerouslyUnrestricted SafetyLevel = "dangerously-unrestricted"

	// DefaultSafetyLevel is used when no safety level is specified.
	// We use readwrite-all as default to avoid breaking existing workflows.
	// This allows all operations except bucket deletion, which is the most
	// common use case and matches pre-safety-level behavior.
	DefaultSafetyLevel = SafetyLevelReadWriteAll
)

// ValidSafetyLevels returns all valid safety level values
func ValidSafetyLevels() []SafetyLevel {
	return []SafetyLevel{
		SafetyLevelReadOnly,
		SafetyLevelReadWriteMine,
		SafetyLevelReadWriteAll,
		SafetyLevelDangerouslyUnrestricted,
	}
}

// IsValid checks if the safety level is valid
func (s SafetyLevel) IsValid() bool {
	switch s {
	case SafetyLevelReadOnly, SafetyLevelReadWriteMine, SafetyLevelReadWriteAll,
		SafetyLevelDangerouslyUnrestricted:
		return true
	case "":
		return true // Empty is valid (defaults to readwrite-all)
	}
	return false
}

// String returns the string representation of the safety level
func (s SafetyLevel) String() string {
	if s == "" {
		return string(DefaultSafetyLevel)
	}
	return string(s)
}

// Hooks holds hook commands for lifecycle events
type Hooks struct {
	PreApply  string `yaml:"pre-apply,omitempty"`
	PostApply string `yaml:"post-apply,omitempty"`
}

// Context holds the connection information for a Dynatrace environment
type Context struct {
	Environment string      `yaml:"environment" table:"ENVIRONMENT"`
	TokenRef    string      `yaml:"token-ref" table:"TOKEN-REF"`
	SafetyLevel SafetyLevel `yaml:"safety-level,omitempty" table:"SAFETY-LEVEL"`
	Description string      `yaml:"description,omitempty" table:"DESCRIPTION,wide"`
	AccountUUID string      `yaml:"account-uuid,omitempty" table:"ACCOUNT-UUID,wide"`
	// Profile binds a command profile to this context, restricting the visible
	// command surface when the context is active (unless overridden by
	// DTCTL_PROFILE). Empty means the full command tree. See profile.go.
	Profile string `yaml:"profile,omitempty" table:"PROFILE,wide"`
	// MinStability is the stability floor for this context: the weakest
	// contract a command or flag may offer and still be usable. Empty means
	// DefaultMinStability (experimental). See stability.go.
	MinStability StabilityLevel `yaml:"min-stability,omitempty" table:"MIN-STABILITY,wide"`
	// StabilityExceptions are narrow, audited opt-ins below the floor, each a
	// command path optionally suffixed with one flag ("ingest",
	// "query --decode-snapshots"). They parameterize the floor for a named target rather
	// than lowering it for everything.
	StabilityExceptions []string `yaml:"stability-exceptions,omitempty" table:"-"`
	// NoDeprecated makes this context refuse deprecated commands and flags, as
	// if the removal release had already happened. Orthogonal to the floor:
	// see NoDeprecatedEnvVar, which overrides this field in either direction.
	NoDeprecated bool  `yaml:"no-deprecated,omitempty" table:"-"`
	Hooks        Hooks `yaml:"hooks,omitempty"`
	// Spill overrides the global spill settings for this context (D15). Nil
	// fields inherit the global spill config.
	Spill *SpillConfig `yaml:"spill,omitempty"`
	// QueryLimits overrides the global query limits for this context. Unset
	// fields inherit the global query-limits block.
	QueryLimits *QueryLimits `yaml:"query-limits,omitempty"`
	// UsageTracking opts this context into dtctl's local usage ledger: a
	// per-day tally of the DQL queries dtctl ran and the data they scanned,
	// kept under the XDG state directory. Off (false) by default, and when
	// off nothing is written. Only aggregate counters are stored, never query
	// text, results, identities, or tokens. See docs/USAGE_TRACKING.md.
	UsageTracking bool `yaml:"usage-tracking,omitempty" table:"-"`
}

// SpillConfig holds the result-spill settings (D15). Threshold and TTL are kept
// as human-friendly strings in the file (e.g. "50KB", "24h") and parsed when
// resolving the effective settings. All fields are optional; an unset field
// inherits from the next layer in the precedence chain
// (flag → env → context-config → global-config → built-in default).
type SpillConfig struct {
	Mode      string `yaml:"mode,omitempty"`      // auto|always|never
	Dir       string `yaml:"dir,omitempty"`       // base directory for spilled files
	Format    string `yaml:"format,omitempty"`    // jsonl|json|csv|parquet (default jsonl)
	Threshold string `yaml:"threshold,omitempty"` // e.g. "50KB"
	TTL       string `yaml:"ttl,omitempty"`       // e.g. "24h"
}

// EffectiveSpillConfig merges the global spill config with the current context's
// override (context wins per field, D15). Env and flag layers are applied by the
// caller on top of this base.
func (c *Config) EffectiveSpillConfig() SpillConfig {
	merged := c.Spill
	if ctx, err := c.CurrentContextObj(); err == nil && ctx.Spill != nil {
		ov := ctx.Spill
		if ov.Mode != "" {
			merged.Mode = ov.Mode
		}
		if ov.Dir != "" {
			merged.Dir = ov.Dir
		}
		if ov.Format != "" {
			merged.Format = ov.Format
		}
		if ov.Threshold != "" {
			merged.Threshold = ov.Threshold
		}
		if ov.TTL != "" {
			merged.TTL = ov.TTL
		}
	}
	return merged
}

// QueryLimits holds per-query execution caps for DQL-executing commands. They
// are the config-file layer of the same knobs `dtctl query` exposes as flags:
// an unset (zero) field inherits from the next layer in the precedence chain
// (flag -> context-config -> global-config -> server default), so a config that
// omits the block behaves exactly as before it existed.
//
// The point of the config layer is that a ceiling you have to remember to pass
// is not a ceiling: `--default-scan-limit-gbytes 500` protects only the
// invocations someone typed it on, whereas a context-level value also covers
// generated queries, scripts, and agent-driven runs.
type QueryLimits struct {
	// ScanLimitGbytes caps the data scanned. Exceeding it returns a PARTIAL
	// result rather than an error, so callers must read the notifications.
	ScanLimitGbytes float64 `yaml:"scan-limit-gbytes,omitempty"`
	// MaxResultRecords caps returned records (0 = server default, typically 1000).
	MaxResultRecords int64 `yaml:"max-result-records,omitempty"`
	// MaxResultBytes caps the result size in bytes.
	MaxResultBytes int64 `yaml:"max-result-bytes,omitempty"`
	// SamplingRatio applies DQL sampling to log/span fetches, trading exactness
	// for scan volume. Results become extrapolated; dtctl flags this on every
	// affected query.
	SamplingRatio float64 `yaml:"sampling-ratio,omitempty"`
}

// IsZero reports whether no limit is set at all.
func (q QueryLimits) IsZero() bool {
	return q == QueryLimits{}
}

// Validate rejects negative limits. Only a positive value is ever sent to the
// server (see pkg/exec), so a negative one would be dropped silently — leaving
// the caller believing a ceiling is in force when none is. For a cap whose
// whole purpose is to hold without being re-checked per invocation, that
// failure mode is worse than refusing to start.
func (q QueryLimits) Validate() error {
	for _, f := range []struct {
		key string
		val float64
	}{
		{"scan-limit-gbytes", q.ScanLimitGbytes},
		{"max-result-records", float64(q.MaxResultRecords)},
		{"max-result-bytes", float64(q.MaxResultBytes)},
		{"sampling-ratio", q.SamplingRatio},
	} {
		if f.val < 0 {
			return fmt.Errorf("query-limits.%s must not be negative (got %g); omit it or use 0 to defer to the next layer", f.key, f.val)
		}
	}
	return nil
}

// EffectiveQueryLimits merges the global query-limits block with the current
// context's override (context wins per field, mirroring EffectiveSpillConfig).
// The flag layer is applied by the caller on top of this base.
//
// Merging is per field rather than whole-block so a context can tighten one
// knob (say a smaller scan ceiling) without having to restate the others.
func (c *Config) EffectiveQueryLimits() QueryLimits {
	merged := c.QueryLimits
	ctx, err := c.CurrentContextObj()
	if err != nil || ctx.QueryLimits == nil {
		return merged
	}
	ov := ctx.QueryLimits
	if ov.ScanLimitGbytes != 0 {
		merged.ScanLimitGbytes = ov.ScanLimitGbytes
	}
	if ov.MaxResultRecords != 0 {
		merged.MaxResultRecords = ov.MaxResultRecords
	}
	if ov.MaxResultBytes != 0 {
		merged.MaxResultBytes = ov.MaxResultBytes
	}
	if ov.SamplingRatio != 0 {
		merged.SamplingRatio = ov.SamplingRatio
	}
	return merged
}

// UsageTrackingEnabled reports whether the current context opted into the
// local usage ledger. A config without a resolvable current context reports
// false: tracking is opt-in, so anything ambiguous resolves to "write nothing".
func (c *Config) UsageTrackingEnabled() bool {
	return c.UsageTrackingOn(c.CurrentContext)
}

// UsageTrackingOn reports whether the named context opted into the local usage
// ledger (Context.UsageTracking). The ledger writes to the user's disk, so like
// aliases and hooks the opt-in is honored only from the global config,
// --config, or DTCTL_CONFIG. An auto-discovered local .dtctl.yaml cannot turn
// it on; its context is tracked when the global context that binds the same
// token-ref and environment is.
func (c *Config) UsageTrackingOn(name string) bool {
	for i := range c.Contexts {
		if c.Contexts[i].Name != name {
			continue
		}
		ctx := c.Contexts[i].Context
		if !c.IsLocal() {
			return ctx.UsageTracking
		}
		b, ok := c.globalBindings[ctx.TokenRef]
		return ok && slices.Contains(b.tracked, urls.Host(ctx.Environment))
	}
	return false
}

// IgnoredUsageTracking reports whether the named context sets usage-tracking in
// an auto-discovered local config without the global config opting in, so the
// setting has no effect. See UsageTrackingOn.
func (c *Config) IgnoredUsageTracking(name string) bool {
	if !c.IsLocal() {
		return false
	}
	for _, nc := range c.Contexts {
		if nc.Name == name {
			return nc.Context.UsageTracking && !c.UsageTrackingOn(name)
		}
	}
	return false
}

// NamedToken holds a token with its name
type NamedToken struct {
	Name  string `yaml:"name"`
	Token string `yaml:"token"`
}

// Preferences holds user preferences
type Preferences struct {
	Output string `yaml:"output,omitempty"`
	Editor string `yaml:"editor,omitempty"`
	Hooks  Hooks  `yaml:"hooks,omitempty"`
}

// DefaultConfigPath returns the default config file path following XDG Base Directory spec
// Returns: XDG_CONFIG_HOME/dtctl/config (typically ~/.config/dtctl/config)
func DefaultConfigPath() string {
	return filepath.Join(xdg.ConfigHome, "dtctl", "config")
}

// ConfigDir returns the config directory path following XDG Base Directory spec
func ConfigDir() string {
	return filepath.Join(xdg.ConfigHome, "dtctl")
}

// CacheDir returns the cache directory path following XDG Base Directory spec
func CacheDir() string {
	return filepath.Join(xdg.CacheHome, "dtctl")
}

// DataDir returns the data directory path following XDG Base Directory spec
func DataDir() string {
	return filepath.Join(xdg.DataHome, "dtctl")
}

// StateDir returns the state directory path following XDG Base Directory spec
// (persistent but disposable data: history, logs). Typically ~/.local/state/dtctl.
func StateDir() string {
	return filepath.Join(xdg.StateHome, "dtctl")
}

// LocalConfigName is the name of the per-project config file
const LocalConfigName = ".dtctl.yaml"

// FindLocalConfig searches for a .dtctl.yaml file starting from the current
// directory and walking up to the root. Returns empty string if not found.
func FindLocalConfig() string {
	cwd, err := os.Getwd()
	if err != nil {
		return ""
	}

	return findLocalConfigFrom(cwd)
}

// findLocalConfigFrom searches for .dtctl.yaml starting from the given directory
func findLocalConfigFrom(startDir string) string {
	dir := startDir
	for {
		configPath := filepath.Join(dir, LocalConfigName)
		if _, err := os.Stat(configPath); err == nil {
			return configPath
		}

		// Move to parent directory
		parent := filepath.Dir(dir)
		if parent == dir {
			// Reached root
			return ""
		}
		dir = parent
	}
}

// EnvConfig names an explicit config file, equivalent to passing --config.
// When set, dtctl loads exactly that file and treats it as trusted (its
// aliases and apply hooks are honored), skipping auto-discovery entirely. This
// is the supported way to run in a prepared, trusted workspace — e.g. kb-run
// creates a clean directory with a .dtctl.yaml and exports this variable, so an
// agent invoking `dtctl apply` runs the intended hooks without a --config flag.
// Because it names a specific operator-chosen file (not a directory to search),
// it does not reopen the untrusted-working-directory vector that IsLocal()
// guards against: a stray .dtctl.yaml elsewhere on disk can never be picked up.
const EnvConfig = "DTCTL_CONFIG"

// Load loads the configuration with the following precedence:
//  1. Explicit config from the DTCTL_CONFIG environment variable (trusted)
//  2. Local config (.dtctl.yaml in current directory or parent directories)
//  3. Global config (XDG_CONFIG_HOME/dtctl/config)
//
// If a local config is found, it is used exclusively (not merged with global).
//
// Security: an auto-discovered local .dtctl.yaml is treated as untrusted (the
// classic "untrusted working directory / checked-out repo / shared dir"
// scenario). Code-execution keys — shell aliases and apply hooks — defined in
// such a config are never honored: alias resolution and hook execution check
// IsLocal() and skip them. These keys are honored only from the global config,
// an explicit --config file, or a config named by DTCTL_CONFIG (all loaded via
// LoadFrom without markLocal), which carry stronger ownership expectations. The
// keys are still loaded into the struct (and never mutated here) so that
// config-management commands round-trip the file without silently destroying a
// user's own aliases or hooks.
func Load() (*Config, error) {
	// An explicit config named in the environment is trusted, exactly like
	// --config, and short-circuits auto-discovery so a closer untrusted
	// .dtctl.yaml can never shadow it. It is intentionally not marked local.
	if envPath := os.Getenv(EnvConfig); envPath != "" {
		return LoadFrom(envPath)
	}

	// Check for local config first
	localConfig := FindLocalConfig()
	if localConfig != "" {
		// Auto-discovered local configs are untrusted: skip env-var expansion so a
		// malicious .dtctl.yaml cannot inject host secrets into its own values.
		cfg, err := loadFrom(localConfig, false)
		if err != nil {
			return nil, err
		}
		cfg.markLocal(localConfig)
		// The environment URL is the one local value that may name its
		// destination through ${VAR}; resolve it once here so every consumer
		// sees a real URL. See resolveLocalEnvironments.
		cfg.resolveLocalEnvironments()
		return cfg, nil
	}

	// Fall back to global config
	return LoadFrom(DefaultConfigPath())
}

// markLocal records that the config was loaded from an auto-discovered local
// .dtctl.yaml and notes whether it carries any code-execution keys (top-level
// aliases, global apply hooks, or per-context apply hooks). It does NOT mutate
// those keys: the values are needed for safe round-tripping by edit/save
// commands, and they are ignored at the point of use (resolveAlias,
// GetPreApplyHook/GetPostApplyHook) rather than being stripped at load. The
// recorded flag lets callers surface a one-line warning naming the local config
// in effect.
func (c *Config) markLocal(path string) {
	c.localPath = path
	c.ignoredExecKeys = c.hasExecKeys()
	c.ignoredEnvRefs = c.hasEnvRefs()
	c.globalBindings = buildGlobalBindings()
}

// globalBinding is what the global config records for one token-ref: the hosts
// its credential belongs to and the safety level its owner granted. Several
// global contexts may share a token-ref, so both fields accumulate across them:
// every bound host is accepted, and the level is the most restrictive one
// granted, so YAML ordering cannot widen either.
type globalBinding struct {
	hosts []string
	level SafetyLevel
	// tracked is the subset of hosts whose global context also opted into the
	// usage ledger. A local config inherits the opt-in from there; it cannot
	// grant it itself (see UsageTrackingOn).
	tracked []string
}

// allows reports whether host is one of the hosts this token-ref is bound to.
func (b globalBinding) allows(host string) bool {
	return slices.Contains(b.hosts, host)
}

// buildGlobalBindings loads the global config (best-effort, ignoring errors)
// and returns the token-ref→binding map. GetToken uses the host to reject a
// local config that redirects a credential elsewhere; GetEffectiveSafetyLevel
// uses the level to clamp one that tries to escalate. Single disk read shared
// by both concerns.
func buildGlobalBindings() map[string]globalBinding {
	globalCfg, err := LoadFrom(DefaultConfigPath())
	if err != nil {
		return nil
	}
	bindings := make(map[string]globalBinding)
	for _, nc := range globalCfg.Contexts {
		ref := nc.Context.TokenRef
		host := urls.Host(nc.Context.Environment)
		if ref == "" || host == "" {
			continue
		}
		level := nc.Context.GetEffectiveSafetyLevel()
		b, seen := bindings[ref]
		if !seen {
			b = globalBinding{hosts: []string{host}, level: level}
		}
		if nc.Context.UsageTracking && !slices.Contains(b.tracked, host) {
			b.tracked = append(b.tracked, host)
		}
		if !seen {
			bindings[ref] = b
			continue
		}
		if !b.allows(host) {
			b.hosts = append(b.hosts, host)
		}
		if safetyLevelRank(level) < safetyLevelRank(b.level) {
			b.level = level
		}
		bindings[ref] = b
	}
	return bindings
}

// hasExecKeys reports whether the config defines any code-execution key:
// a top-level alias, a global (preferences) apply hook, or a per-context hook.
func (c *Config) hasExecKeys() bool {
	if len(c.Aliases) > 0 {
		return true
	}
	if c.Preferences.Hooks.PreApply != "" || c.Preferences.Hooks.PostApply != "" {
		return true
	}
	for i := range c.Contexts {
		h := c.Contexts[i].Context.Hooks
		if h.PreApply != "" || h.PostApply != "" {
			return true
		}
	}
	return false
}

// IsLocal reports whether the config was loaded from an auto-discovered local
// .dtctl.yaml (as opposed to the global config or an explicit --config file).
// Code-execution keys (aliases, apply hooks) are ignored when this is true.
func (c *Config) IsLocal() bool { return c.localPath != "" }

// LocalConfigPath returns the path of the auto-discovered local .dtctl.yaml the
// config was loaded from, or "" if it was not loaded from a local config.
func (c *Config) LocalConfigPath() string { return c.localPath }

// IgnoredExecKeys reports whether code-execution keys (aliases, apply hooks)
// are present in the auto-discovered local config and are therefore ignored at
// runtime. See markLocal.
func (c *Config) IgnoredExecKeys() bool { return c.ignoredExecKeys }

// hasEnvRefs reports whether inline token values contain a literal '$'
// character — a sign that the author intended env-var expansion that was
// skipped because the config was auto-discovered (untrusted). Environment
// URLs are excluded: they support ${VAR} expansion via os.ExpandEnv in
// NewClientFromConfig (after URL validation), so no warning is needed.
func (c *Config) hasEnvRefs() bool {
	for _, nt := range c.Tokens {
		if strings.ContainsRune(nt.Token, '$') {
			return true
		}
	}
	return false
}

// IgnoredEnvRefs reports whether key config values in the auto-discovered local
// config contained unexpanded env-var references (literal '$' characters).
// When true the caller should warn the user to use --config or DTCTL_CONFIG.
func (c *Config) IgnoredEnvRefs() bool { return c.ignoredEnvRefs }

// LoadFrom loads the configuration from a specific path
func LoadFrom(path string) (*Config, error) {
	return loadFrom(path, true)
}

// LoadFromWithoutExpansion loads the configuration from a specific path without
// expanding environment variables. Use this to inspect raw template values.
func LoadFromWithoutExpansion(path string) (*Config, error) {
	return loadFrom(path, false)
}

// LoadWithoutExpansion loads the configuration without expanding environment variables,
// using the same search order as Load (local config, then global config).
func LoadWithoutExpansion() (*Config, error) {
	// Honor DTCTL_CONFIG with the same precedence as Load: an explicit,
	// trusted config that bypasses auto-discovery. See EnvConfig.
	if envPath := os.Getenv(EnvConfig); envPath != "" {
		return LoadFromWithoutExpansion(envPath)
	}
	if local := FindLocalConfig(); local != "" {
		cfg, err := LoadFromWithoutExpansion(local)
		if err != nil {
			return nil, err
		}
		// Tag the local config so callers ignore its code-execution keys at the
		// point of use, without mutating the loaded values (this path also
		// backs config-management commands that load-modify-save the file). See
		// Load.
		cfg.markLocal(local)
		return cfg, nil
	}
	return LoadFromWithoutExpansion(DefaultConfigPath())
}

func loadFrom(path string, expandEnv bool) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("config file not found at %s. Run 'dtctl config set-context' to create one", path)
		}
		return nil, fmt.Errorf("failed to read config file: %w", err)
	}

	if expandEnv {
		// Expand environment variables in the config file.
		//
		// We deliberately do NOT use os.ExpandEnv: it expands every $-prefixed
		// token, including shell positional parameters like $1/$2/$@ that can
		// legitimately appear in opaque config values such as hook commands.
		// expandEnvPreservingShellParams leaves those alone and substitutes only
		// real environment variable names.
		data = []byte(expandEnvPreservingShellParams(string(data)))
	}

	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("failed to parse config file: %w", err)
	}

	if !isSupportedAPIVersion(cfg.APIVersion) {
		return nil, fmt.Errorf("config file %s has schema version %q; this build understands %q — upgrade the tool reading it to a build that does", path, cfg.APIVersion, CurrentAPIVersion)
	}

	return &cfg, nil
}

// CurrentAPIVersion is the config schema version this build reads and writes.
// The schema evolves additively within a version: unknown fields are ignored
// on load and preserved on save (see SaveTo), so the version only changes on
// a breaking redefinition of existing fields. See docs/dev/CONFIG_CONTRACT.md.
const CurrentAPIVersion = "v1"

// isSupportedAPIVersion reports whether this build can read a config with the
// given apiVersion. Three spellings denote schema v1 in the wild: the empty
// string (configs predating enforcement), "v1" (NewConfig), and
// "dtctl.io/v1" (the k8s-style form written by `dtctl config init`).
func isSupportedAPIVersion(v string) bool {
	switch v {
	case "", CurrentAPIVersion, "dtctl.io/" + CurrentAPIVersion:
		return true
	}
	return false
}

// expandEnvPreservingShellParams expands $VAR and ${VAR} references using
// the process environment, but leaves shell positional parameters and
// special parameters intact (e.g. $1, ${10}, $@, $*, $#, $?, $!, $$, $0).
// This lets users embed those tokens in opaque config values such as hook
// commands without having them silently rewritten to the empty string at
// config load.
//
// Lookups for ordinary names that are not set in the environment fall back
// to "" (matching os.ExpandEnv); use "${VAR}" in the config when an
// unexpanded literal is desired (and `VAR` is in scope to be unset).
//
// We implement the scan ourselves rather than calling os.Expand so the
// brace form (`${10}`) is preserved exactly for shell positionals — the
// stdlib helper passes the bare name to the mapper, losing the braces.
func expandEnvPreservingShellParams(s string) string {
	var b strings.Builder
	i := 0
	for i < len(s) {
		c := s[i]
		if c != '$' || i+1 >= len(s) {
			b.WriteByte(c)
			i++
			continue
		}

		// Brace form: ${...}
		if s[i+1] == '{' {
			end := strings.IndexByte(s[i+2:], '}')
			if end < 0 {
				// No closing brace — treat as literal.
				b.WriteByte(c)
				i++
				continue
			}
			name := s[i+2 : i+2+end]
			if isShellPositionalOrSpecial(name) {
				b.WriteString(s[i : i+2+end+1]) // preserve `${name}` verbatim
			} else {
				b.WriteString(os.Getenv(name))
			}
			i += 2 + end + 1
			continue
		}

		// Bare form: $NAME or $1 or $@ etc.
		nameLen := bareEnvNameLen(s[i+1:])
		if nameLen == 0 {
			// `$` followed by something that is not a valid name char and
			// not a special parameter — write `$` literally.
			b.WriteByte(c)
			i++
			continue
		}
		name := s[i+1 : i+1+nameLen]
		if isShellPositionalOrSpecial(name) {
			b.WriteString(s[i : i+1+nameLen]) // preserve `$name` verbatim
		} else {
			b.WriteString(os.Getenv(name))
		}
		i += 1 + nameLen
	}
	return b.String()
}

// bareEnvNameLen returns how many bytes at the start of s form a bare
// (unbraced) shell variable reference, not counting the leading `$` (which
// must already be stripped by the caller). Returns 0 if s does not start
// with a valid name character or single-char special parameter.
//
// Matches POSIX bare-form references: `$NAME` (alpha/underscore-led name),
// `$1` (positional digit, single-char only without braces), `$@`, `$*`,
// `$#`, `$?`, `$!`, `$$`, `$-`.
func bareEnvNameLen(s string) int {
	if len(s) == 0 {
		return 0
	}
	c := s[0]
	// Single-digit positional ($0..$9 — multi-digit needs braces in POSIX).
	if c >= '0' && c <= '9' {
		return 1
	}
	// Single-char special parameters.
	switch c {
	case '@', '*', '#', '?', '!', '$', '-':
		return 1
	}
	// Identifier: [A-Za-z_][A-Za-z0-9_]*
	if !isNameStart(c) {
		return 0
	}
	n := 1
	for n < len(s) && isNameCont(s[n]) {
		n++
	}
	return n
}

func isNameStart(c byte) bool {
	return c == '_' || (c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z')
}

func isNameCont(c byte) bool {
	return isNameStart(c) || (c >= '0' && c <= '9')
}

// isShellPositionalOrSpecial reports whether name refers to a shell
// positional parameter ($0, $1, ${10}, ...) or special parameter
// ($@, $*, $#, $?, $!, $$, $-) and should therefore be preserved verbatim
// rather than expanded against the process environment.
func isShellPositionalOrSpecial(name string) bool {
	if name == "" {
		return false
	}
	// Purely numeric → positional parameter.
	allDigits := true
	for i := 0; i < len(name); i++ {
		if name[i] < '0' || name[i] > '9' {
			allDigits = false
			break
		}
	}
	if allDigits {
		return true
	}
	if len(name) == 1 {
		switch name[0] {
		case '@', '*', '#', '?', '!', '$', '-':
			return true
		}
	}
	return false
}

// Save saves the configuration to the default path
func (c *Config) Save() error {
	return c.SaveTo(DefaultConfigPath())
}

// SaveTo saves the configuration to a specific path.
//
// Contract rule (docs/dev/CONFIG_CONTRACT.md): fields unknown to this build —
// written by a newer dtctl or another schema-v1 writer — are grafted back
// from the file being overwritten, so an older writer never destroys them.
func (c *Config) SaveTo(path string) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return fmt.Errorf("failed to create config directory: %w", err)
	}

	data, err := yaml.Marshal(c.restoreRawEnvironments())
	if err != nil {
		return fmt.Errorf("failed to marshal config: %w", err)
	}

	if existing, err := os.ReadFile(path); err == nil {
		data = preserveUnknownFields(existing, data)
	}

	if err := os.WriteFile(path, data, 0600); err != nil {
		return fmt.Errorf("failed to write config file: %w", err)
	}

	return nil
}

// CurrentContextObj returns the current context object. The pointer aliases
// the entry in c.Contexts, so writes through it change the config.
func (c *Config) CurrentContextObj() (*Context, error) {
	if c.CurrentContext == "" {
		return nil, fmt.Errorf("no current context set")
	}

	// Index the slice rather than ranging over copies: a pointer into the loop
	// variable would not alias c.Contexts, and writes through it would be lost.
	for i := range c.Contexts {
		if c.Contexts[i].Name == c.CurrentContext {
			return &c.Contexts[i].Context, nil
		}
	}

	return nil, fmt.Errorf("current context %q not found", c.CurrentContext)
}

// GetContext returns a named context by name
func (c *Config) GetContext(name string) (*NamedContext, error) {
	for i := range c.Contexts {
		if c.Contexts[i].Name == name {
			return &c.Contexts[i], nil
		}
	}
	return nil, fmt.Errorf("context %q not found", name)
}

// GetToken retrieves a token by reference name.
// It first tries the OS keyring (checking both regular and OAuth tokens),
// then file-based OAuth token storage, then falls back to the config file.
// When DTCTL_TOKEN_STORAGE=file is set, keyring is skipped entirely.
func (c *Config) GetToken(tokenRef string) (string, error) {
	// Sealed configs must only use inline tokens — no keyring, file store, or
	// OAuth lookup. This prevents host credential stores from overriding a
	// synthetic session's request-supplied token.
	if c.inlineOnly {
		for _, nt := range c.Tokens {
			if nt.Name == tokenRef {
				if nt.Token != "" {
					return nt.Token, nil
				}
			}
		}
		return "", fmt.Errorf("token %q not found in sealed config", tokenRef)
	}

	// Local configs must not carry inline token values: an attacker-controlled
	// .dtctl.yaml could define an arbitrary token and point at an arbitrary host.
	// Use --config or DTCTL_CONFIG to supply inline tokens from a trusted path.
	if c.IsLocal() {
		for _, nt := range c.Tokens {
			if nt.Name == tokenRef && nt.Token != "" {
				return "", fmt.Errorf(
					"local config %q defines inline token %q; inline tokens are not allowed in auto-discovered local configs — use --config or DTCTL_CONFIG",
					c.localPath, tokenRef)
			}
		}

		// Verify that the local context's environment points to the same host as
		// the global config binding for this token-ref. This prevents a rogue
		// .dtctl.yaml from redirecting stored credentials to a foreign host.
		if ctx, err := c.CurrentContextObj(); err == nil && ctx.TokenRef == tokenRef {
			localHost := urls.Host(ctx.Environment)
			if binding, ok := c.globalBindings[tokenRef]; ok {
				if !binding.allows(localHost) {
					return "", fmt.Errorf(
						"local config %q redirects token %q from trusted host(s) %s to %q — use --config or DTCTL_CONFIG",
						c.localPath, tokenRef, strings.Join(binding.hosts, ", "), localHost)
				}
			} else {
				return "", fmt.Errorf(
					"local config %q uses token-ref %q which is not bound to any environment in the global config — use --config or DTCTL_CONFIG",
					c.localPath, tokenRef)
			}
		}
	}

	// Try keyring first (skipped when file storage is explicitly requested)
	if IsKeyringAvailable() && !IsFileTokenStorage() {
		ts := NewTokenStore()

		// First check for OAuth token.
		// Current format: oauth:<env>:<tokenRef>
		// Legacy format:  oauth:<tokenRef>
		for _, keyringName := range c.oauthKeyringNames(tokenRef) {
			oauthToken, err := ts.GetToken(keyringName)
			if err != nil || oauthToken == "" {
				continue
			}

			var tokenData struct {
				AccessToken string `json:"access_token"`
			}
			if err := json.Unmarshal([]byte(oauthToken), &tokenData); err == nil && tokenData.AccessToken != "" {
				return tokenData.AccessToken, nil
			}
		}

		// Fall back to regular token
		token, err := ts.GetToken(tokenRef)
		if err == nil && token != "" {
			return token, nil
		}
	}

	// Try file-based OAuth token storage (for headless/WSL environments)
	if !IsKeyringAvailable() || IsFileTokenStorage() {
		fileStore := NewOAuthFileStore()
		for _, keyringName := range c.oauthKeyringNames(tokenRef) {
			oauthToken, err := fileStore.GetToken(keyringName)
			if err != nil || oauthToken == "" {
				continue
			}

			var tokenData struct {
				AccessToken string `json:"access_token"`
			}
			if err := json.Unmarshal([]byte(oauthToken), &tokenData); err == nil && tokenData.AccessToken != "" {
				return tokenData.AccessToken, nil
			}
		}
	}

	// Fall back to config file
	for _, nt := range c.Tokens {
		if nt.Name == tokenRef {
			if nt.Token != "" {
				return nt.Token, nil
			}
			// Token reference exists but value is empty (migrated to keyring)
			return "", fmt.Errorf("token %q not found in keyring (may need to re-add credentials)", tokenRef)
		}
	}
	return "", fmt.Errorf("token %q not found", tokenRef)
}

func (c *Config) oauthKeyringNames(tokenRef string) []string {
	addCandidate := func(list []string, seen map[string]struct{}, key string) []string {
		if key == "" {
			return list
		}
		if _, exists := seen[key]; exists {
			return list
		}
		seen[key] = struct{}{}
		return append(list, key)
	}

	seen := make(map[string]struct{})
	var candidates []string

	// Prefer environment-specific entries from matching contexts.
	for _, nc := range c.Contexts {
		if nc.Context.TokenRef != tokenRef {
			continue
		}
		env := oauthEnvironmentFromURL(nc.Context.Environment)
		if env != "" {
			candidates = addCandidate(candidates, seen, fmt.Sprintf("oauth:%s:%s", env, tokenRef))
		}
	}

	// Also check all known environment prefixes to support shared token refs.
	for _, env := range []string{"prod", "dev", "hard"} {
		candidates = addCandidate(candidates, seen, fmt.Sprintf("oauth:%s:%s", env, tokenRef))
	}

	return candidates
}

func oauthEnvironmentFromURL(environmentURL string) string {
	url := strings.ToLower(environmentURL)

	if strings.Contains(url, "dev.apps.dynatracelabs.com") {
		return "dev"
	}
	if strings.Contains(url, "sprint.apps.dynatracelabs.com") {
		return "hard"
	}
	if strings.Contains(url, "apps.dynatrace.com") {
		return "prod"
	}

	return ""
}

// MustGetToken retrieves a token by reference name, returning empty string on error
func (c *Config) MustGetToken(tokenRef string) string {
	token, _ := c.GetToken(tokenRef)
	return token
}

// ContextOptions holds optional fields for context configuration
type ContextOptions struct {
	SafetyLevel SafetyLevel
	Description string
	Profile     string
	// MinStability is the context's stability floor: the weakest contract a
	// command or flag may offer and still be usable. See StabilityLevel.
	MinStability StabilityLevel
	// StabilityExceptions are individual commands and flags admitted below the
	// floor ("inventory", "query --decode-snapshots"). A nil slice leaves the existing list
	// untouched; an empty non-nil slice clears it, which is how a caller
	// withdraws every exception at once.
	StabilityExceptions []string
	// UsageTracking sets the context's usage-tracking opt-in. nil leaves the
	// existing value untouched (the caller did not ask to change it), so a
	// plain update cannot silently switch tracking off.
	UsageTracking *bool
}

// SetContext creates or updates a context
func (c *Config) SetContext(name, environment, tokenRef string) {
	c.SetContextWithOptions(name, environment, tokenRef, nil)
}

// SetContextWithOptions creates or updates a context with optional fields
func (c *Config) SetContextWithOptions(name, environment, tokenRef string, opts *ContextOptions) {
	environment = urls.Normalize(environment)

	for i, nc := range c.Contexts {
		if nc.Name == name {
			c.Contexts[i].Context.Environment = environment
			if tokenRef != "" {
				c.Contexts[i].Context.TokenRef = tokenRef
			}
			if opts != nil {
				if opts.SafetyLevel != "" {
					c.Contexts[i].Context.SafetyLevel = opts.SafetyLevel
				}
				if opts.Description != "" {
					c.Contexts[i].Context.Description = opts.Description
				}
				if opts.Profile != "" {
					c.Contexts[i].Context.Profile = opts.Profile
				}
				if opts.MinStability != "" {
					c.Contexts[i].Context.MinStability = opts.MinStability
				}
				if opts.StabilityExceptions != nil {
					c.Contexts[i].Context.StabilityExceptions = opts.StabilityExceptions
				}
				if opts.UsageTracking != nil {
					c.Contexts[i].Context.UsageTracking = *opts.UsageTracking
				}
			}
			return
		}
	}

	ctx := Context{
		Environment: environment,
		TokenRef:    tokenRef,
	}
	if opts != nil {
		ctx.SafetyLevel = opts.SafetyLevel
		ctx.Description = opts.Description
		ctx.Profile = opts.Profile
		ctx.MinStability = opts.MinStability
		ctx.StabilityExceptions = opts.StabilityExceptions
		if opts.UsageTracking != nil {
			ctx.UsageTracking = *opts.UsageTracking
		}
	}

	c.Contexts = append(c.Contexts, NamedContext{
		Name:    name,
		Context: ctx,
	})
}

// GetEffectiveSafetyLevel returns the effective safety level for a context
// If no safety level is set, returns the default (readwrite-all)
func (c *Context) GetEffectiveSafetyLevel() SafetyLevel {
	if c.SafetyLevel == "" {
		return DefaultSafetyLevel
	}
	return c.SafetyLevel
}

// safetyLevelRank returns a permissiveness rank (lower = more restrictive).
// Empty string is treated as DefaultSafetyLevel (readwrite-all, rank 2).
func safetyLevelRank(l SafetyLevel) int {
	switch l {
	case SafetyLevelReadOnly:
		return 0
	case SafetyLevelReadWriteMine:
		return 1
	case SafetyLevelDangerouslyUnrestricted:
		return 3
	default: // SafetyLevelReadWriteAll, "", or unknown
		return 2
	}
}

// GetEffectiveSafetyLevel returns the effective safety level for the current
// context. For non-local configs this is identical to ctx.GetEffectiveSafetyLevel().
// For auto-discovered local configs (IsLocal()), the level is clamped to
// min(local, global) so a rogue .dtctl.yaml cannot escalate beyond what the
// owner of the borrowed credential permits. The global level is looked up by
// the context's token-ref — the same binding GetToken enforces — so renaming the
// local context does not shed the clamp, and resolving it here rather than at
// load time keeps it correct under --context / DTCTL_CONTEXT overrides.
//
// A token-ref with no global binding has no trust anchor at all, so it is
// clamped to DefaultSafetyLevel rather than left free. Such a context cannot
// reach an API either way (GetToken refuses to resolve it), so this costs
// nothing and keeps the guarantee independent of token resolution.
func (c *Config) GetEffectiveSafetyLevel() SafetyLevel {
	ctx, err := c.CurrentContextObj()
	if err != nil {
		return DefaultSafetyLevel
	}
	return c.EffectiveSafetyLevelFor(ctx)
}

// EffectiveSafetyLevelFor reports the level actually enforced for ctx, which
// for a local config is its declared level clamped to the global binding of
// its token-ref. Callers that display a level for a context other than the
// current one (e.g. `dtctl ctx list`) use this so what is shown is what is
// enforced; GetEffectiveSafetyLevel is the current-context shorthand.
func (c *Config) EffectiveSafetyLevelFor(ctx *Context) SafetyLevel {
	localLevel := ctx.GetEffectiveSafetyLevel()
	if !c.IsLocal() {
		return localLevel
	}
	ceiling := DefaultSafetyLevel
	if binding, ok := c.globalBindings[ctx.TokenRef]; ok {
		ceiling = binding.level
	}
	if safetyLevelRank(localLevel) <= safetyLevelRank(ceiling) {
		return localLevel
	}
	return ceiling
}

// resolveLocalEnvironments expands ${VAR} references in the environment URL of
// every context of an auto-discovered local config — but only adopts an
// expansion that yields a bare Dynatrace origin. The unexpanded value is
// recorded so SaveTo can write it back.
//
// A committed .dtctl.yaml is allowed to name its destination through the
// environment so one file can serve several developers and CI. Resolving once
// at load rather than at each read is what keeps that promise: `dtctl doctor`,
// the live-debugger handlers, spill's tenant id, account-environment detection
// and every other reader of Context.Environment see a real URL without each
// having to remember to resolve one. Resolving in only some places is how a
// documented setup ends up reporting "cannot reach ${DT_ENVIRONMENT_URL}".
//
// Two conditions gate adoption, and together they are what make resolving this
// early safe — not every reader validates its destination, doctor's
// reachability check HEADs the URL as written:
//
//   - the value must be a *whole-value* reference ("${DT_ENVIRONMENT_URL}",
//     the shape `config init` writes), never a reference embedded in
//     surrounding text. Interpolating into the middle of a URL is how a value
//     that is not a URL gets carried somewhere as one:
//     "https://$SECRET.apps.dynatrace.com" expands to a perfectly valid
//     Dynatrace origin whose hostname is the victim's secret, and merely
//     resolving it would hand that secret to a DNS resolver.
//   - what it expands to must be a bare Dynatrace origin.
//
// A value failing either check keeps the literal it was written as, so the
// expanded form exists nowhere: not in the struct, not in a request, not in an
// error message. NewClientFromConfig re-checks the origin rule, so a rejected
// value cannot reach an API either.
//
// The environment is the only local value resolved at all: expansion of
// aliases, hooks and inline token values stays off entirely.
func (c *Config) resolveLocalEnvironments() {
	if !c.IsLocal() {
		return
	}
	for i := range c.Contexts {
		raw := c.Contexts[i].Context.Environment
		if !strings.ContainsRune(raw, '$') {
			continue
		}
		if c.rawEnvironments == nil {
			c.rawEnvironments = make(map[string]string)
		}
		c.rawEnvironments[c.Contexts[i].Name] = raw
		if !isWholeValueEnvRef(raw) {
			continue
		}
		resolved := os.ExpandEnv(raw)
		if urls.IsDynatraceEnvironmentOrigin(resolved) != nil {
			continue
		}
		c.Contexts[i].Context.Environment = resolved
	}
}

// isWholeValueEnvRef reports whether s is exactly one environment-variable
// reference — "${VAR}" or "$VAR" — and nothing else. A reference with anything
// around it interpolates a value into a position that gives it a meaning it
// was never checked for; see resolveLocalEnvironments.
func isWholeValueEnvRef(s string) bool {
	name, ok := strings.CutPrefix(s, "$")
	if !ok {
		return false
	}
	if braced, ok := strings.CutPrefix(name, "{"); ok {
		name, ok = strings.CutSuffix(braced, "}")
		if !ok {
			return false
		}
	}
	if name == "" {
		return false
	}
	for _, r := range name {
		isAlnum := r == '_' ||
			(r >= '0' && r <= '9') ||
			(r >= 'a' && r <= 'z') ||
			(r >= 'A' && r <= 'Z')
		if !isAlnum {
			return false
		}
	}
	return true
}

// EnvironmentUnresolved reports whether the named local context declares its
// environment through a ${VAR} reference that did not resolve to a usable
// Dynatrace origin — the variable is unset, or what it expanded to was
// rejected. Such a context still holds the literal reference.
func (c *Config) EnvironmentUnresolved(contextName string) bool {
	raw, ok := c.rawEnvironments[contextName]
	if !ok {
		return false
	}
	nc, err := c.GetContext(contextName)
	if err != nil {
		return false
	}
	return nc.Context.Environment == raw
}

// restoreRawEnvironments returns a copy of c with every resolved local
// environment URL written back as the ${VAR} reference it came from, so a
// load-modify-save cycle does not silently bake one developer's expansion into
// a committed .dtctl.yaml. A context whose environment was changed since load
// (e.g. `config set-context --environment`) keeps the new value.
func (c *Config) restoreRawEnvironments() *Config {
	if len(c.rawEnvironments) == 0 {
		return c
	}
	out := *c
	out.Contexts = make([]NamedContext, len(c.Contexts))
	copy(out.Contexts, c.Contexts)
	for i := range out.Contexts {
		raw, ok := c.rawEnvironments[out.Contexts[i].Name]
		if !ok {
			continue
		}
		if out.Contexts[i].Context.Environment == os.ExpandEnv(raw) {
			out.Contexts[i].Context.Environment = raw
		}
	}
	return &out
}

// GetPreApplyHook returns the effective pre-apply hook command.
// Per-context hooks take precedence over global (preferences) hooks.
// The special value "none" explicitly disables the global hook for a context.
func (c *Config) GetPreApplyHook() string {
	// Hooks from an auto-discovered local config are untrusted and never run.
	if c.IsLocal() {
		return ""
	}
	// Per-context hook wins
	if ctx, err := c.CurrentContextObj(); err == nil {
		if ctx.Hooks.PreApply != "" {
			if ctx.Hooks.PreApply == "none" {
				return "" // explicitly disabled
			}
			return ctx.Hooks.PreApply
		}
	}
	// Fall back to global
	return c.Preferences.Hooks.PreApply
}

// GetPostApplyHook returns the effective post-apply hook command.
// Per-context hooks take precedence over global (preferences) hooks.
// The special value "none" explicitly disables the global hook for a context.
func (c *Config) GetPostApplyHook() string {
	// Hooks from an auto-discovered local config are untrusted and never run.
	if c.IsLocal() {
		return ""
	}
	if ctx, err := c.CurrentContextObj(); err == nil {
		if ctx.Hooks.PostApply != "" {
			if ctx.Hooks.PostApply == "none" {
				return ""
			}
			return ctx.Hooks.PostApply
		}
	}
	return c.Preferences.Hooks.PostApply
}

// DeleteContext removes a context by name.
// Returns an error if the context is not found.
func (c *Config) DeleteContext(name string) error {
	for i, nc := range c.Contexts {
		if nc.Name == name {
			c.Contexts = append(c.Contexts[:i], c.Contexts[i+1:]...)
			return nil
		}
	}
	return fmt.Errorf("context %q not found", name)
}

// PruneEmptyEnvironments removes contexts whose names are in placeholderNames,
// except the named keepContext. Pass the context names from the raw (unexpanded)
// config file to avoid pruning contexts backed by currently-unset env vars.
func (c *Config) PruneEmptyEnvironments(keepContext string, placeholderNames map[string]bool) {
	kept := c.Contexts[:0]
	for _, nc := range c.Contexts {
		if placeholderNames[nc.Name] && nc.Name != keepContext {
			continue
		}
		kept = append(kept, nc)
	}
	c.Contexts = kept
}

// SetToken creates or updates a token.
// If keyring is available, the token is stored securely in the OS keyring
// and only a reference is kept in the config file.
// Any cached OAuth tokens for this credential name are invalidated so that
// a rotated platform token does not keep using a stale refresh token.
func (c *Config) SetToken(name, token string) error {
	return c.setTokenWithKeyring(name, token, nil, nil)
}

// setTokenWithKeyring is the testable core of SetToken; accepts an explicit
// keyringBackend and OAuthFileStore so tests avoid the OS keyring.
func (c *Config) setTokenWithKeyring(name, token string, kr keyringBackend, fileStore *OAuthFileStore) error {
	if kr == nil {
		kr = newOSKeyring()
	}
	if fileStore == nil {
		fileStore = NewOAuthFileStore()
	}

	keyringAvailable := kr.Available()
	if keyringAvailable {
		if err := kr.Set(name, token); err != nil {
			return fmt.Errorf("failed to store token in keyring: %w", err)
		}
		// Store empty token in config (reference only)
		token = ""
	}

	// Invalidate cached OAuth tokens for all known environments.
	// When a platform token is rotated, the old OAuth refresh token
	// is no longer valid and must not be reused.
	// Both keyring and file-based caches are cleared, because GetToken
	// checks both backends.
	// Deletion is best-effort: a failure here means the user will get
	// an invalid_grant error on the next request and must re-authenticate,
	// which is acceptable.
	for _, key := range c.oauthKeyringNames(name) {
		if keyringAvailable {
			_ = kr.Delete(key)
		}
		_ = fileStore.DeleteToken(key)
	}

	// Update or add token entry in config
	for i, nt := range c.Tokens {
		if nt.Name == name {
			c.Tokens[i].Token = token
			return nil
		}
	}

	c.Tokens = append(c.Tokens, NamedToken{
		Name:  name,
		Token: token,
	})
	return nil
}

// DeleteToken removes a credential and every cached derivative of it.
//
// A credential is not a single secret. One token ref fans out into the plain
// keyring entry, a cached OAuth entry per environment, a scope companion for
// each of those, and file-store copies of all of them. Teardown must clear the
// whole fan-out: anything missed is live token material outliving the
// credential the caller asked to remove.
//
// Deletion is idempotent — a credential that is already gone is not an error,
// so callers can safely retry — but a store that refuses a delete is reported,
// so no caller ever announces a credential as removed while it still exists.
func (c *Config) DeleteToken(name string) error {
	return c.deleteTokenWithKeyring(name, nil, nil)
}

// deleteTokenWithKeyring is the testable core of DeleteToken; accepts an
// explicit keyringBackend and OAuthFileStore so tests avoid the OS keyring.
//
// The sweep does not depend on the config still holding the credential's
// context: oauthKeyringNames enumerates prod/dev/hard unconditionally, and
// oauthEnvironmentFromURL can only ever return one of those three (or ""), so
// a context can only name an entry the unconditional list already covers.
func (c *Config) deleteTokenWithKeyring(name string, kr keyringBackend, fileStore *OAuthFileStore) error {
	if name == "" {
		return fmt.Errorf("credential name must not be empty")
	}
	if kr == nil {
		kr = newOSKeyring()
	}
	if fileStore == nil {
		fileStore = NewOAuthFileStore()
	}

	// Every key this credential can occupy: the plain platform/API token entry,
	// each per-environment OAuth cache entry, and each entry's scope companion.
	// The pre-environment legacy form (oauth:<tokenRef>) is swept too: GetToken
	// no longer resolves it, but an entry written by an older dtctl still holds
	// a refresh token, and an unreachable secret is still a secret.
	keys := []string{name, OAuthTokenPrefix + name, OAuthTokenPrefix + name + scopeCompanionSuffix}
	for _, oauthKey := range c.oauthKeyringNames(name) {
		keys = append(keys, oauthKey, oauthKey+scopeCompanionSuffix)
	}

	// Both backends are swept for every key regardless of which one is active:
	// GetToken reads from either, so a credential written under one backend and
	// deleted under the other would still resolve afterwards.
	var errs []error
	keyringAvailable := kr.Available()
	for _, key := range keys {
		if keyringAvailable {
			if err := kr.Delete(key); err != nil {
				errs = append(errs, fmt.Errorf("keyring entry %q: %w", key, err))
			}
		}
		if err := fileStore.DeleteToken(key); err != nil {
			errs = append(errs, fmt.Errorf("token file for %q: %w", key, err))
		}
	}

	// Drop the config entry only once the secret stores are clear: while the
	// entry is still there, a partially failed delete remains retryable by name.
	if err := errors.Join(errs...); err != nil {
		return err
	}

	for i, nt := range c.Tokens {
		if nt.Name == name {
			c.Tokens = append(c.Tokens[:i], c.Tokens[i+1:]...)
			break
		}
	}

	return nil
}

// NewConfig creates a new default configuration
func NewConfig() *Config {
	return &Config{
		APIVersion: CurrentAPIVersion,
		Kind:       "Config",
		Contexts:   []NamedContext{},
		Tokens:     []NamedToken{},
		Preferences: Preferences{
			Output: "table",
			Editor: "vim",
		},
	}
}
