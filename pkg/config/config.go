// Package config re-exports the dtctl session layer from
// github.com/dynatrace-oss/dtctl/sdk/session, where the implementation moved:
// the config model, load/save, credential stores, and token resolution are
// the shared contract consumed by dtctl and dtctl-* plugins; this package remains so the root module's
// import surface is stable. New code (and external consumers) should import
// sdk/session directly.
package config

import (
	"context"

	"github.com/dynatrace-oss/dtctl/sdk/session"
)

// Config model — see sdk/session.
type (
	Config         = session.Config
	NamedContext   = session.NamedContext
	Context        = session.Context
	NamedToken     = session.NamedToken
	Preferences    = session.Preferences
	ContextOptions = session.ContextOptions
	Hooks          = session.Hooks
	SpillConfig    = session.SpillConfig
	QueryLimits    = session.QueryLimits
	SafetyLevel    = session.SafetyLevel
	StabilityLevel = session.StabilityLevel
	AliasEntry     = session.AliasEntry
	AliasFile      = session.AliasFile
)

// Safety levels — the shared semantics of a context's safety-level field.
const (
	SafetyLevelReadOnly                = session.SafetyLevelReadOnly
	SafetyLevelReadWriteMine           = session.SafetyLevelReadWriteMine
	SafetyLevelReadWriteAll            = session.SafetyLevelReadWriteAll
	SafetyLevelDangerouslyUnrestricted = session.SafetyLevelDangerouslyUnrestricted
	DefaultSafetyLevel                 = session.DefaultSafetyLevel
)

// Schema and file-discovery constants.
const (
	CurrentAPIVersion = session.CurrentAPIVersion
	LocalConfigName   = session.LocalConfigName
)

// Credential-store constants.
const (
	KeyringService         = session.KeyringService
	EnvDisableKeyring      = session.EnvDisableKeyring
	EnvTokenStorage        = session.EnvTokenStorage
	ErrMsgCollectionUnlock = session.ErrMsgCollectionUnlock
)

// ValidSafetyLevels returns all valid safety level values.
func ValidSafetyLevels() []SafetyLevel { return session.ValidSafetyLevels() }

// XDG paths.
func DefaultConfigPath() string { return session.DefaultConfigPath() }
func ConfigDir() string         { return session.ConfigDir() }
func CacheDir() string          { return session.CacheDir() }
func DataDir() string           { return session.DataDir() }
func StateDir() string          { return session.StateDir() }

// Config loading and discovery.
func FindLocalConfig() string                            { return session.FindLocalConfig() }
func Load() (*Config, error)                             { return session.Load() }
func LoadFrom(path string) (*Config, error)              { return session.LoadFrom(path) }
func LoadFromWithoutExpansion(p string) (*Config, error) { return session.LoadFromWithoutExpansion(p) }
func LoadWithoutExpansion() (*Config, error)             { return session.LoadWithoutExpansion() }
func NewConfig() *Config                                 { return session.NewConfig() }

// Alias name validation (alias data lives in the shared schema; alias
// execution stays in cmd/).
func ValidateAliasName(name string) error { return session.ValidateAliasName(name) }

// Credential stores.
type (
	TokenStore     = session.TokenStore
	OAuthFileStore = session.OAuthFileStore
)

func NewTokenStore() *TokenStore         { return session.NewTokenStore() }
func NewOAuthFileStore() *OAuthFileStore { return session.NewOAuthFileStore() }
func NewOAuthFileStoreWithDir(dir string) *OAuthFileStore {
	return session.NewOAuthFileStoreWithDir(dir)
}

func CheckKeyring() error                             { return session.CheckKeyring() }
func IsKeyringAvailable() bool                        { return session.IsKeyringAvailable() }
func KeyringBackend() string                          { return session.KeyringBackend() }
func IsFileTokenStorage() bool                        { return session.IsFileTokenStorage() }
func IsKeyringAbsent(err error) bool                  { return session.IsKeyringAbsent(err) }
func PersistFileTokenStorage() error                  { return session.PersistFileTokenStorage() }
func FileTokenStorageConsentPath() string             { return session.FileTokenStorageConsentPath() }
func IsOAuthStorageAvailable() bool                   { return session.IsOAuthStorageAvailable() }
func OAuthStorageBackend() string                     { return session.OAuthStorageBackend() }
func MigrateTokensToKeyring(cfg *Config) (int, error) { return session.MigrateTokensToKeyring(cfg) }
func GetTokenWithFallback(cfg *Config, tokenRef string) (string, error) {
	return session.GetTokenWithFallback(cfg, tokenRef)
}
func EnsureKeyringCollection(ctx context.Context) error {
	return session.EnsureKeyringCollection(ctx)
}

// Explicit-config environment variable — see sdk/session.EnvConfig.
const EnvConfig = session.EnvConfig

// Command profiles (default-deny allowlists of commands) — the schema and
// resolution live with the Config type in sdk/session; profile *enforcement*
// stays in cmd/.
type Profile = session.Profile

const (
	ProfileEnvVar = session.ProfileEnvVar
	ProfileFull   = session.ProfileFull
)

func BuiltinProfileNames() []string { return session.BuiltinProfileNames() }

// Stability tiers (what dtctl promises about a command's shape over time) —
// the ordered axis and its config/environment resolution live with the Config
// type in sdk/session; declaration and *enforcement* stay in pkg/stability and
// cmd/.
const (
	StabilityDevelopment  = session.StabilityDevelopment
	StabilityExperimental = session.StabilityExperimental
	StabilityStable       = session.StabilityStable
	DefaultStabilityLevel = session.DefaultStabilityLevel
	DefaultMinStability   = session.DefaultMinStability

	MinStabilityEnvVar = session.MinStabilityEnvVar
	DevelopmentEnvVar  = session.DevelopmentEnvVar
	NoDeprecatedEnvVar = session.NoDeprecatedEnvVar
	DevelopmentAll     = session.DevelopmentAll
)

// ValidStabilityLevels returns the stability levels in ascending order of promise.
func ValidStabilityLevels() []StabilityLevel { return session.ValidStabilityLevels() }

// ParseStabilityLevel validates and normalizes a user-written stability level.
func ParseStabilityLevel(s string) (StabilityLevel, error) { return session.ParseStabilityLevel(s) }
