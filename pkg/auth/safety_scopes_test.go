package auth

import (
	"testing"

	"github.com/dynatrace-oss/dtctl/pkg/config"
)

// TestGetScopesForSafetyLevel tests that scopes are correctly assigned for each safety level
func TestGetScopesForSafetyLevel(t *testing.T) {
	tests := []struct {
		name           string
		safetyLevel    config.SafetyLevel
		mustInclude    []string // Scopes that MUST be present
		mustNotInclude []string // Scopes that MUST NOT be present
		minScopeCount  int      // Minimum number of scopes expected
	}{
		{
			name:        "readonly scopes",
			safetyLevel: config.SafetyLevelReadOnly,
			mustInclude: []string{
				"openid",
				"offline_access",
				"document:documents:read",
				"automation:workflows:read",
				"storage:logs:read",
				"storage:buckets:read",
				"hub:catalog:read",
			},
			mustNotInclude: []string{
				"document:documents:write",
				"document:documents:delete",
				"automation:workflows:write",
				"storage:logs:write",
				"storage:buckets:write",
				"storage:bucket-definitions:delete",
				"storage:bucket-definitions:truncate",
				"dev-obs:breakpoints:set",
				"extensions:definitions:write",
				// --admin-access (#375) reaches other users' documents; readonly
				// does not request it.
				"document:documents:admin",
				// ...nor, for the workflow --admin-access, other users' workflows.
				"automation:workflows:admin",
			},
			minScopeCount: 36, // readonly has many read scopes
		},
		{
			name:        "readwrite-mine scopes",
			safetyLevel: config.SafetyLevelReadWriteMine,
			mustInclude: []string{
				"openid",
				"offline_access",
				"document:documents:read",
				"document:documents:write",
				"document:documents:delete",
				"automation:workflows:read",
				"automation:workflows:write",
				"automation:workflows:run",
				"storage:logs:read",
				"storage:files:write",
				"extensions:definitions:write",
				"email:emails:send",
				"dev-obs:breakpoints:set",
				"hub:catalog:read",
				"iam:service-users:use",
			},
			mustNotInclude: []string{
				"storage:logs:write",
				"storage:bucket-definitions:delete",
				"storage:bucket-definitions:truncate",
				"storage:records:delete",
				// readwrite-mine exists to keep other users' resources out of
				// reach, so it does not request the document or workflow admin
				// scope.
				"document:documents:admin",
				"automation:workflows:admin",
			},
			minScopeCount: 46,
		},
		{
			name:        "readwrite-all scopes",
			safetyLevel: config.SafetyLevelReadWriteAll,
			mustInclude: []string{
				"openid",
				"offline_access",
				"document:documents:read",
				"document:documents:write",
				"document:documents:delete",
				"automation:workflows:read",
				"automation:workflows:write",
				"automation:workflows:run",
				"storage:logs:read",
				"storage:logs:write",
				"storage:buckets:read",
				"storage:buckets:write",
				"storage:events:write",
				"storage:metrics:write",
				"extensions:definitions:write",
				"email:emails:send",
				"dev-obs:breakpoints:set",
				"hub:catalog:read",
				"iam:service-users:use",
				// get documents|dashboards|notebooks --admin-access (#375)
				"document:documents:admin",
				// the workflow commands' --admin-access (#700)
				"automation:workflows:admin",
			},
			mustNotInclude: []string{
				"storage:bucket-definitions:delete",
				"storage:bucket-definitions:truncate",
				"storage:records:delete",
			},
			minScopeCount: 64,
		},
		{
			name:        "dangerously-unrestricted scopes",
			safetyLevel: config.SafetyLevelDangerouslyUnrestricted,
			mustInclude: []string{
				"openid",
				"offline_access",
				"document:documents:read",
				"document:documents:write",
				"automation:workflows:read",
				"automation:workflows:write",
				"automation:workflows:run",
				"storage:logs:read",
				"storage:logs:write",
				"storage:buckets:read",
				"storage:buckets:write",
				"storage:bucket-definitions:delete",
				"storage:bucket-definitions:truncate",
				"storage:records:delete",
				"email:emails:send",
				"dev-obs:breakpoints:set",
				"hub:catalog:read",
				"extensions:definitions:write",
				"iam:service-users:use",
				"document:documents:admin",
				"automation:workflows:admin",
			},
			mustNotInclude: []string{},
			minScopeCount:  73,
		},
		{
			name:        "empty safety level defaults to readwrite-all",
			safetyLevel: "",
			mustInclude: []string{
				"openid",
				"offline_access",
				"storage:logs:write",
				"storage:buckets:write",
				"dev-obs:breakpoints:set",
			},
			mustNotInclude: []string{
				"storage:bucket-definitions:delete",
			},
			minScopeCount: 64,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			scopes := GetScopesForSafetyLevel(tt.safetyLevel)

			// Check minimum scope count
			if len(scopes) < tt.minScopeCount {
				t.Errorf("Expected at least %d scopes, got %d", tt.minScopeCount, len(scopes))
			}

			// Create scope map for easier lookup
			scopeMap := make(map[string]bool)
			for _, scope := range scopes {
				scopeMap[scope] = true
			}

			// Verify required scopes are present
			for _, required := range tt.mustInclude {
				if !scopeMap[required] {
					t.Errorf("Missing required scope: %s", required)
				}
			}

			// Verify forbidden scopes are NOT present
			for _, forbidden := range tt.mustNotInclude {
				if scopeMap[forbidden] {
					t.Errorf("Forbidden scope present: %s", forbidden)
				}
			}

			// Verify openid is always present (required for OAuth)
			if !scopeMap["openid"] {
				t.Error("openid scope must always be present")
			}
		})
	}
}

// TestOAuthConfigWithSafetyLevel tests that OAuth config properly integrates safety levels
func TestOAuthConfigWithSafetyLevel(t *testing.T) {
	tests := []struct {
		name         string
		env          Environment
		safetyLevel  config.SafetyLevel
		expectScopes int
	}{
		{
			name:         "Production with readonly",
			env:          EnvironmentProd,
			safetyLevel:  config.SafetyLevelReadOnly,
			expectScopes: 36,
		},
		{
			name:         "Development with readwrite-all",
			env:          EnvironmentDev,
			safetyLevel:  config.SafetyLevelReadWriteAll,
			expectScopes: 64,
		},
		{
			name:         "Hardening with dangerously-unrestricted",
			env:          EnvironmentHard,
			safetyLevel:  config.SafetyLevelDangerouslyUnrestricted,
			expectScopes: 73,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := OAuthConfigForEnvironment(tt.env, tt.safetyLevel)

			// Verify safety level is set
			if cfg.SafetyLevel != tt.safetyLevel {
				t.Errorf("SafetyLevel = %v, want %v", cfg.SafetyLevel, tt.safetyLevel)
			}

			// Verify scopes match safety level
			if len(cfg.Scopes) < tt.expectScopes {
				t.Errorf("Expected at least %d scopes, got %d", tt.expectScopes, len(cfg.Scopes))
			}

			// Verify no duplicate scopes
			seen := make(map[string]bool)
			for _, scope := range cfg.Scopes {
				if seen[scope] {
					t.Errorf("Duplicate scope found: %s", scope)
				}
				seen[scope] = true
			}
		})
	}
}

// TestOAuthConfigFromEnvironmentURLWithSafety tests the convenience function
func TestOAuthConfigFromEnvironmentURLWithSafety(t *testing.T) {
	tests := []struct {
		name        string
		url         string
		safetyLevel config.SafetyLevel
		wantEnv     Environment
		wantSafety  config.SafetyLevel
	}{
		{
			name:        "Production URL with readonly",
			url:         "https://abc123.apps.dynatrace.com",
			safetyLevel: config.SafetyLevelReadOnly,
			wantEnv:     EnvironmentProd,
			wantSafety:  config.SafetyLevelReadOnly,
		},
		{
			name:        "Dev URL with dangerously-unrestricted",
			url:         "https://dev456.dev.apps.dynatracelabs.com",
			safetyLevel: config.SafetyLevelDangerouslyUnrestricted,
			wantEnv:     EnvironmentDev,
			wantSafety:  config.SafetyLevelDangerouslyUnrestricted,
		},
		{
			name:        "Sprint URL with default safety (empty string)",
			url:         "https://sprint789.sprint.apps.dynatracelabs.com",
			safetyLevel: "",
			wantEnv:     EnvironmentHard,
			wantSafety:  config.DefaultSafetyLevel,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := OAuthConfigFromEnvironmentURLWithSafety(tt.url, tt.safetyLevel)

			if cfg.Environment != tt.wantEnv {
				t.Errorf("Environment = %v, want %v", cfg.Environment, tt.wantEnv)
			}

			if cfg.SafetyLevel != tt.wantSafety {
				t.Errorf("SafetyLevel = %v, want %v", cfg.SafetyLevel, tt.wantSafety)
			}

			// Verify scopes match the expected safety level
			expectedScopes := GetScopesForSafetyLevel(tt.wantSafety)
			if len(cfg.Scopes) != len(expectedScopes) {
				t.Errorf("Scope count = %d, want %d", len(cfg.Scopes), len(expectedScopes))
			}
		})
	}
}

// TestSafetyLevelScopeHierarchy tests that higher safety levels include appropriate permissions
func TestSafetyLevelScopeHierarchy(t *testing.T) {
	readonly := GetScopesForSafetyLevel(config.SafetyLevelReadOnly)
	readwriteMine := GetScopesForSafetyLevel(config.SafetyLevelReadWriteMine)
	readwriteAll := GetScopesForSafetyLevel(config.SafetyLevelReadWriteAll)
	unrestricted := GetScopesForSafetyLevel(config.SafetyLevelDangerouslyUnrestricted)

	// Note: readwrite-mine may have fewer scopes than readonly because it focuses on
	// write permissions for personal resources rather than broad read access.
	// The hierarchy is about capability, not scope count.

	// Verify that readwrite-all has more capabilities than readwrite-mine
	if len(readwriteAll) <= len(readwriteMine) {
		t.Error("readwrite-all should have more scopes than readwrite-mine")
	}

	// Verify that dangerously-unrestricted has the most scopes
	if len(unrestricted) <= len(readwriteAll) {
		t.Error("dangerously-unrestricted should have more scopes than readwrite-all")
	}

	// Verify that all levels include openid
	levels := []struct {
		name   string
		scopes []string
	}{
		{"readonly", readonly},
		{"readwrite-mine", readwriteMine},
		{"readwrite-all", readwriteAll},
		{"dangerously-unrestricted", unrestricted},
	}

	for _, level := range levels {
		found := false
		for _, scope := range level.scopes {
			if scope == "openid" {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("%s level missing openid scope", level.name)
		}
	}
}

// TestSafetyLevelNoDuplicateScopes ensures no safety level has duplicate scopes
func TestSafetyLevelNoDuplicateScopes(t *testing.T) {
	levels := []config.SafetyLevel{
		config.SafetyLevelReadOnly,
		config.SafetyLevelReadWriteMine,
		config.SafetyLevelReadWriteAll,
		config.SafetyLevelDangerouslyUnrestricted,
	}

	for _, level := range levels {
		t.Run(string(level), func(t *testing.T) {
			scopes := GetScopesForSafetyLevel(level)
			seen := make(map[string]bool)

			for _, scope := range scopes {
				if seen[scope] {
					t.Errorf("Duplicate scope '%s' in %s level", scope, level)
				}
				seen[scope] = true
			}
		})
	}
}
