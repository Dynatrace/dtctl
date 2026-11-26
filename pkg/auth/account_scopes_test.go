package auth

import (
	"testing"

	"github.com/Dynatrace/dtctl/pkg/config"
)

func TestAccountScopesForSafetyLevel(t *testing.T) {
	t.Parallel()
	tests := []struct {
		level      config.SafetyLevel
		wantScopes []string
	}{
		{
			level:      config.SafetyLevelReadOnly,
			wantScopes: []string{"account-idm-read", "platform-token:tokens:manage"},
		},
		{
			level:      config.SafetyLevelReadWriteAll,
			wantScopes: []string{"account-idm-read", "account-idm-write", "platform-token:tokens:manage", "platform-token:tokens:write"},
		},
		{
			level:      config.SafetyLevelDangerouslyUnrestricted,
			wantScopes: []string{"account-idm-read", "account-idm-write", "platform-token:tokens:manage", "platform-token:tokens:write"},
		},
	}
	for _, tt := range tests {
		got := accountScopesForSafetyLevel(tt.level)
		if len(got) != len(tt.wantScopes) {
			t.Errorf("accountScopesForSafetyLevel(%q) = %v, want %v", tt.level, got, tt.wantScopes)
			continue
		}
		for i, s := range got {
			if s != tt.wantScopes[i] {
				t.Errorf("accountScopesForSafetyLevel(%q)[%d] = %q, want %q", tt.level, i, s, tt.wantScopes[i])
			}
		}
	}
}

func TestAccountOAuthConfig(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name            string
		env             Environment
		safetyLevel     config.SafetyLevel
		accountUUID     string
		wantEnvURL      string
		wantScopesCount int
	}{
		{
			name:            "prod read-only",
			env:             EnvironmentProd,
			safetyLevel:     config.SafetyLevelReadOnly,
			accountUUID:     "aaaabbbb-cccc-dddd-eeee-ffffaaaabbbb",
			wantEnvURL:      "urn:dtaccount:aaaabbbb-cccc-dddd-eeee-ffffaaaabbbb",
			wantScopesCount: 2,
		},
		{
			name:            "dev readwrite-all",
			env:             EnvironmentDev,
			safetyLevel:     config.SafetyLevelReadWriteAll,
			accountUUID:     "11111111-2222-3333-4444-555555555555",
			wantEnvURL:      "urn:dtaccount:11111111-2222-3333-4444-555555555555",
			wantScopesCount: 4,
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			cfg := AccountOAuthConfig(tt.env, tt.safetyLevel, tt.accountUUID)
			if cfg.EnvironmentURL != tt.wantEnvURL {
				t.Errorf("EnvironmentURL = %q, want %q", cfg.EnvironmentURL, tt.wantEnvURL)
			}
			if len(cfg.Scopes) != tt.wantScopesCount {
				t.Errorf("Scopes = %v, want %d scopes", cfg.Scopes, tt.wantScopesCount)
			}
		})
	}
}
