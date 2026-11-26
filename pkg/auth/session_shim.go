// The OAuth flow, token manager, and cross-process refresh lock moved to
// github.com/Dynatrace/dtctl/sdk/session:
// every long-running consumer of the shared token store must
// refresh through the same locked path, so the machinery is part of the
// shared session layer. This file re-exports it for the root module.
//
// What stays here is scope composition: the resource-scope tables
// (resource_scopes.go) that map safety levels to OAuth scopes also feed the
// `dtctl commands` catalog — they are CLI domain. The sdk's OAuth
// constructors take the composed scope list as an argument; the wrappers
// below keep this package's historical signatures by composing it.
package auth

import (
	"time"

	"github.com/Dynatrace/dtctl/pkg/config"
	sdkauth "github.com/Dynatrace/dtctl/sdk/auth"
	"github.com/Dynatrace/dtctl/sdk/session"
)

type (
	TokenManager = session.TokenManager
	StoredToken  = session.StoredToken
	TokenSet     = session.TokenSet
	OAuthFlow    = session.OAuthFlow
	OAuthConfig  = session.OAuthConfig
	Environment  = session.Environment
	// UserInfo is the SSO userinfo response (session.OAuthUserInfo).
	UserInfo = session.OAuthUserInfo
	// TokenStorage names the store an OAuth token actually lives in.
	TokenStorage = session.TokenStorage
)

const (
	EnvironmentProd = session.EnvironmentProd
	EnvironmentDev  = session.EnvironmentDev
	EnvironmentHard = session.EnvironmentHard

	OAuthTokenPrefix   = session.OAuthTokenPrefix
	TokenRefreshBuffer = session.TokenRefreshBuffer

	TokenStorageKeyring = session.TokenStorageKeyring
	TokenStorageFile    = session.TokenStorageFile
)

// ErrOAuthSessionRevoked mirrors session.ErrOAuthSessionRevoked (same value,
// so errors.Is works across both names).
var ErrOAuthSessionRevoked = session.ErrOAuthSessionRevoked

// ErrNoRefreshToken mirrors session.ErrNoRefreshToken (same value, so
// errors.Is works across both names).
var ErrNoRefreshToken = session.ErrNoRefreshToken

func NewTokenManager(oauthConfig *OAuthConfig) (*TokenManager, error) {
	return session.NewTokenManager(oauthConfig)
}

func NewOAuthFlow(cfg *OAuthConfig) (*OAuthFlow, error) { return session.NewOAuthFlow(cfg) }

func DetectEnvironment(environmentURL string) Environment {
	return session.DetectEnvironment(environmentURL)
}

func IsOAuthToken(tokenName string) bool   { return session.IsOAuthToken(tokenName) }
func IsTokenExpired(tokens *TokenSet) bool { return session.IsTokenExpired(tokens) }
func DecodeRefreshTokenExpiry(refreshToken string) (time.Time, bool) {
	return session.DecodeRefreshTokenExpiry(refreshToken)
}

// ExtractJWTScopes returns the granted scopes decoded from a JWT access token,
// or nil when they cannot be determined. Used as a fallback for scope display
// when the scope string was dropped from compact keyring storage.
func ExtractJWTScopes(accessToken string) []string {
	return sdkauth.ExtractJWTScopes(accessToken)
}

// GetScopesForSafetyLevel returns the OAuth scopes required for a given safety
// level. The union is composed from the canonical ResourceScopes table plus the
// non-resource scope groups in resource_scopes.go, so the per-command scopes
// surfaced by `dtctl commands` cannot diverge from what login actually requests.
func GetScopesForSafetyLevel(level config.SafetyLevel) []string {
	return safetyLevelScopes(level)
}

// DefaultOAuthConfig returns the default OAuth configuration for production
// with the default safety level's scopes.
func DefaultOAuthConfig() *OAuthConfig {
	return OAuthConfigForEnvironment(EnvironmentProd, config.DefaultSafetyLevel)
}

// OAuthConfigForEnvironment creates an OAuth configuration for the specified
// environment with the scopes composed for the safety level.
func OAuthConfigForEnvironment(env Environment, safetyLevel config.SafetyLevel) *OAuthConfig {
	return session.OAuthConfigForEnvironment(env, safetyLevel, safetyLevelScopes(safetyLevel))
}

// OAuthConfigFromEnvironmentURL creates an OAuth configuration by detecting
// the environment from a URL, with the default safety level's scopes.
func OAuthConfigFromEnvironmentURL(environmentURL string) *OAuthConfig {
	return OAuthConfigFromEnvironmentURLWithSafety(environmentURL, config.DefaultSafetyLevel)
}

// OAuthConfigFromEnvironmentURLWithSafety creates an OAuth configuration with
// the scopes composed for the given safety level.
func OAuthConfigFromEnvironmentURLWithSafety(environmentURL string, safetyLevel config.SafetyLevel) *OAuthConfig {
	return session.OAuthConfigFromEnvironmentURL(environmentURL, safetyLevel, safetyLevelScopes(safetyLevel))
}
