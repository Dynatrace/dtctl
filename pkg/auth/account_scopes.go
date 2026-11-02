package auth

import (
	"github.com/Dynatrace/dtctl/pkg/config"
	"github.com/Dynatrace/dtctl/sdk/session"
)

// accountScopesForSafetyLevel returns account OAuth scopes for the given safety level.
// platform-token:tokens:manage covers list/revoke; platform-token:tokens:write is required for create.
func accountScopesForSafetyLevel(level config.SafetyLevel) []string {
	switch level {
	case config.SafetyLevelReadOnly:
		return []string{"account-idm-read", "platform-token:tokens:manage"}
	default:
		return []string{"account-idm-read", "account-idm-write", "platform-token:tokens:manage", "platform-token:tokens:write"}
	}
}

// AccountOAuthConfig returns an OAuth config for account-plane operations.
// The resource is set to "urn:dtaccount:{uuid}" per Dynatrace account OAuth spec.
func AccountOAuthConfig(env Environment, safetyLevel config.SafetyLevel, accountUUID string) *OAuthConfig {
	cfg := session.OAuthConfigForEnvironment(env, safetyLevel, accountScopesForSafetyLevel(safetyLevel))
	cfg.EnvironmentURL = "urn:dtaccount:" + accountUUID
	return cfg
}
