package auth

import (
	"testing"

	"github.com/dynatrace-oss/dtctl/pkg/config"
)

// baselineScopes pins the exact OAuth scope union each safety level requested
// before GetScopesForSafetyLevel was refactored to compose from ResourceScopes.
// TestGetScopesForSafetyLevelBaseline asserts the refactor is behavior-
// preserving: login must request exactly the same scopes as before. Update this
// table only with an intentional, reviewed change to login behavior.
var baselineScopes = map[config.SafetyLevel][]string{
	config.SafetyLevelReadOnly: { // 43 scopes
		"openid",
		"offline_access",
		"automation:workflows:read",
		"automation:rules:read",
		"document:documents:read",
		"document:direct-shares:read",
		"document:trash.documents:read",
		"slo:slos:read",
		"slo:objective-templates:read",
		"settings:objects:read",
		"app-settings:objects:read",
		"settings:schemas:read",
		"extensions:definitions:read",
		"extensions:configurations:read",
		"storage:logs:read",
		"storage:events:read",
		"storage:metrics:read",
		"storage:spans:read",
		"storage:bizevents:read",
		"storage:entities:read",
		"storage:smartscape:read",
		"storage:system:read",
		"storage:security.events:read",
		"storage:application.snapshots:read",
		"storage:user.events:read",
		"storage:user.sessions:read",
		"storage:user.replays:read",
		"storage:fieldsets:read",
		"storage:fieldset-definitions:read",
		"storage:profiles:read",
		"storage:buckets:read",
		"storage:bucket-definitions:read",
		"storage:files:read",
		"storage:filter-segments:read",
		"iam:users:read",
		"iam:groups:read",
		"davis:analyzers:read",
		"app-engine:apps:run",
		"app-engine:edge-connects:read",
		"notification:notifications:read",
		"hub:catalog:read",
		"openpipeline:configurations:read",
		"environment-api:dashboard-3rdgen-migration:execute",
	},
	config.SafetyLevelReadWriteMine: { // 57 scopes
		"openid",
		"offline_access",
		"automation:workflows:read",
		"automation:rules:read",
		"document:documents:read",
		"document:direct-shares:read",
		"document:trash.documents:read",
		"slo:slos:read",
		"slo:objective-templates:read",
		"settings:objects:read",
		"app-settings:objects:read",
		"settings:schemas:read",
		"extensions:definitions:read",
		"extensions:configurations:read",
		"storage:logs:read",
		"storage:events:read",
		"storage:metrics:read",
		"storage:spans:read",
		"storage:bizevents:read",
		"storage:entities:read",
		"storage:smartscape:read",
		"storage:system:read",
		"storage:security.events:read",
		"storage:buckets:read",
		"storage:bucket-definitions:read",
		"storage:files:read",
		"storage:filter-segments:read",
		"iam:users:read",
		"iam:groups:read",
		"davis:analyzers:read",
		"app-engine:apps:run",
		"app-engine:edge-connects:read",
		"notification:notifications:read",
		"hub:catalog:read",
		"openpipeline:configurations:read",
		"document:documents:write",
		"document:documents:delete",
		"document:direct-shares:write",
		"document:direct-shares:delete",
		"document:trash.documents:restore",
		"automation:workflows:write",
		"automation:workflows:run",
		"automation:rules:write",
		"slo:slos:write",
		"settings:objects:write",
		"extensions:definitions:write",
		"extensions:configurations:write",
		"storage:files:write",
		"storage:filter-segments:write",
		"iam:service-users:use",
		"davis:analyzers:execute",
		"davis-copilot:conversations:execute",
		"app-engine:functions:run",
		"document:environment-shares:claim",
		"email:emails:send",
		"dev-obs:breakpoints:set",
		"environment-api:dashboard-3rdgen-migration:execute",
	},
	config.SafetyLevelReadWriteAll: { // 79 scopes
		"openid",
		"offline_access",
		"automation:workflows:read",
		"automation:rules:read",
		"document:documents:read",
		"document:direct-shares:read",
		"document:trash.documents:read",
		"slo:slos:read",
		"slo:objective-templates:read",
		"settings:objects:read",
		"app-settings:objects:read",
		"settings:schemas:read",
		"extensions:definitions:read",
		"extensions:configurations:read",
		"storage:logs:read",
		"storage:events:read",
		"storage:metrics:read",
		"storage:spans:read",
		"storage:bizevents:read",
		"storage:entities:read",
		"storage:smartscape:read",
		"storage:system:read",
		"storage:security.events:read",
		"storage:application.snapshots:read",
		"storage:user.events:read",
		"storage:user.sessions:read",
		"storage:user.replays:read",
		"storage:fieldsets:read",
		"storage:fieldset-definitions:read",
		"storage:profiles:read",
		"storage:buckets:read",
		"storage:bucket-definitions:read",
		"storage:files:read",
		"storage:filter-segments:read",
		"iam:users:read",
		"iam:groups:read",
		"davis:analyzers:read",
		"app-engine:apps:run",
		"app-engine:edge-connects:read",
		"notification:notifications:read",
		"hub:catalog:read",
		"openpipeline:configurations:read",
		"document:documents:write",
		"document:documents:delete",
		"document:direct-shares:write",
		"document:direct-shares:delete",
		"document:trash.documents:restore",
		"automation:workflows:write",
		"automation:workflows:run",
		"automation:rules:write",
		"slo:slos:write",
		"settings:objects:write",
		"extensions:definitions:write",
		"extensions:configurations:write",
		"storage:files:write",
		"storage:filter-segments:write",
		"iam:service-users:use",
		"davis:analyzers:execute",
		"davis-copilot:conversations:execute",
		"app-engine:functions:run",
		"email:emails:send",
		"dev-obs:breakpoints:set",
		"environment-api:dashboard-3rdgen-migration:execute",
		"document:environment-shares:read",
		"document:environment-shares:write",
		"document:environment-shares:delete",
		"document:environment-shares:claim",
		"document:documents:admin",
		"storage:logs:write",
		"storage:events:write",
		"storage:metrics:write",
		"storage:buckets:write",
		"davis-copilot:nl2dql:execute",
		"davis-copilot:dql2nl:execute",
		"davis-copilot:document-search:execute",
		"app-engine:apps:install",
		"app-engine:apps:delete",
		"app-engine:edge-connects:write",
		"notification:notifications:write",
	},
	config.SafetyLevelDangerouslyUnrestricted: { // 87 scopes
		"openid",
		"offline_access",
		"automation:workflows:read",
		"automation:rules:read",
		"document:documents:read",
		"document:environment-shares:read",
		"document:trash.documents:read",
		"slo:slos:read",
		"slo:objective-templates:read",
		"settings:objects:read",
		"app-settings:objects:read",
		"settings:schemas:read",
		"extensions:definitions:read",
		"extensions:configurations:read",
		"storage:logs:read",
		"storage:events:read",
		"storage:metrics:read",
		"storage:spans:read",
		"storage:bizevents:read",
		"storage:entities:read",
		"storage:smartscape:read",
		"storage:system:read",
		"storage:security.events:read",
		"storage:application.snapshots:read",
		"storage:user.events:read",
		"storage:user.sessions:read",
		"storage:user.replays:read",
		"storage:fieldsets:read",
		"storage:fieldset-definitions:read",
		"storage:profiles:read",
		"storage:buckets:read",
		"storage:bucket-definitions:read",
		"storage:files:read",
		"storage:filter-segments:read",
		"iam:users:read",
		"iam:groups:read",
		"davis:analyzers:read",
		"app-engine:apps:run",
		"app-engine:edge-connects:read",
		"notification:notifications:read",
		"hub:catalog:read",
		"openpipeline:configurations:read",
		"document:documents:write",
		"document:documents:delete",
		"document:environment-shares:write",
		"document:environment-shares:delete",
		"document:environment-shares:claim",
		"document:documents:admin",
		"document:trash.documents:restore",
		"document:trash.documents:delete",
		"automation:workflows:write",
		"automation:workflows:run",
		"automation:rules:write",
		"slo:slos:write",
		"settings:objects:write",
		"extensions:definitions:write",
		"extensions:configurations:write",
		"storage:logs:write",
		"storage:events:write",
		"storage:metrics:write",
		"storage:buckets:write",
		"storage:bucket-definitions:write",
		"storage:bucket-definitions:delete",
		"storage:bucket-definitions:truncate",
		"storage:fieldset-definitions:write",
		"storage:files:write",
		"storage:files:delete",
		"storage:filter-segments:write",
		"storage:filter-segments:share",
		"storage:filter-segments:delete",
		"storage:filter-segments:admin",
		"storage:records:delete",
		"iam:service-users:use",
		"davis:analyzers:execute",
		"davis-copilot:conversations:execute",
		"davis-copilot:nl2dql:execute",
		"davis-copilot:dql2nl:execute",
		"davis-copilot:document-search:execute",
		"app-engine:apps:install",
		"app-engine:apps:delete",
		"app-engine:functions:run",
		"app-engine:edge-connects:write",
		"app-engine:edge-connects:delete",
		"notification:notifications:write",
		"email:emails:send",
		"dev-obs:breakpoints:set",
		"environment-api:dashboard-3rdgen-migration:execute",
	},
}

func asSet(scopes []string) map[string]bool {
	m := make(map[string]bool, len(scopes))
	for _, s := range scopes {
		m[s] = true
	}
	return m
}

// TestGetScopesForSafetyLevelBaseline guarantees the table-derived composition
// reproduces the historical per-level scope sets exactly (no login drift).
func TestGetScopesForSafetyLevelBaseline(t *testing.T) {
	for level, want := range baselineScopes {
		t.Run(string(level), func(t *testing.T) {
			got := GetScopesForSafetyLevel(level)

			if len(got) != len(want) {
				t.Errorf("scope count = %d, want %d", len(got), len(want))
			}
			gotSet, wantSet := asSet(got), asSet(want)
			for _, s := range want {
				if !gotSet[s] {
					t.Errorf("missing scope: %s", s)
				}
			}
			for _, s := range got {
				if !wantSet[s] {
					t.Errorf("unexpected scope: %s", s)
				}
			}

			// No duplicates in the composed output.
			seen := map[string]bool{}
			for _, s := range got {
				if seen[s] {
					t.Errorf("duplicate scope: %s", s)
				}
				seen[s] = true
			}
		})
	}
}

// TestRequiredScopesMatchSafetyLevels enforces the design's core invariant:
// every scope reachable from the canonical (resource, access) table is granted
// by the most permissive safety level (dangerously-unrestricted). This is what
// keeps per-command required_scopes from ever claiming a scope login can't
// request.
func TestRequiredScopesMatchSafetyLevels(t *testing.T) {
	union := asSet(GetScopesForSafetyLevel(config.SafetyLevelDangerouslyUnrestricted))

	for resource, as := range ResourceScopes {
		for _, access := range []Access{AccessRead, AccessWrite, AccessDelete, AccessRun, AccessClaim} {
			for _, scope := range as.For(access) {
				if !union[scope] {
					t.Errorf("ResourceScopes[%q].%s scope %q is not granted by any safety level", resource, access, scope)
				}
			}
		}
	}

	// DQL query scopes must likewise be grantable.
	for _, scope := range QueryScopes {
		if !union[scope] {
			t.Errorf("QueryScopes %q is not granted by any safety level", scope)
		}
	}
}

func TestAccessForVerb(t *testing.T) {
	cases := []struct {
		verb     string
		safetyOp string
		want     Access
	}{
		{"get", "", AccessRead},
		{"claim", "OperationCreate", AccessClaim},
		{"describe", "", AccessRead},
		{"query", "", AccessRead},
		{"create", "OperationCreate", AccessWrite},
		{"apply", "OperationCreate", AccessWrite},
		{"edit", "OperationUpdate", AccessWrite},
		{"restore", "OperationUpdate", AccessWrite},
		{"share", "OperationUpdate", AccessWrite},
		{"delete", "OperationDelete", AccessDelete},
		{"exec", "OperationCreate", AccessRun},
	}
	for _, c := range cases {
		if got := AccessForVerb(c.verb, c.safetyOp); got != c.want {
			t.Errorf("AccessForVerb(%q, %q) = %q, want %q", c.verb, c.safetyOp, got, c.want)
		}
	}
}

func TestScopesForResourcePluralAndDeleteFallback(t *testing.T) {
	// Plural resolves to the singular table entry.
	if got := ScopesForResource("workflows", AccessRead); len(got) != 1 || got[0] != "automation:workflows:read" {
		t.Errorf("ScopesForResource(workflows, read) = %v", got)
	}
	// Delete falls back to write when no distinct delete scope exists (workflows).
	if got := ScopesForResource("workflow", AccessDelete); len(got) != 1 || got[0] != "automation:workflows:write" {
		t.Errorf("ScopesForResource(workflow, delete) = %v, want workflows:write fallback", got)
	}
	// Documents expose a distinct delete scope.
	if got := ScopesForResource("dashboard", AccessDelete); len(got) != 1 || got[0] != "document:documents:delete" {
		t.Errorf("ScopesForResource(dashboard, delete) = %v", got)
	}
	// Unknown resource yields no scopes.
	if got := ScopesForResource("does-not-exist", AccessRead); got != nil {
		t.Errorf("ScopesForResource(unknown) = %v, want nil", got)
	}
}
