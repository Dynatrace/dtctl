package auth

import (
	"strings"
	"testing"
)

func TestScopeMatches(t *testing.T) {
	for _, tc := range []struct {
		pattern, scope string
		want           bool
	}{
		{"*", "app-engine:functions:run", true},
		{"*", "", true},
		{"document:documents:read", "document:documents:read", true},
		{"document:documents:read", "document:documents:write", false},
		{"storage:*:read", "storage:logs:read", true},
		{"storage:*:read", "storage:user.events:read", true},
		{"storage:*:read", "storage::read", true},
		{"storage:*:read", "storage:read", false},
		{"storage:*:read", "storage:logs:write", false},
		{"storage:*:read", "xstorage:logs:read", false},
		{"a*b*c", "abc", true},
		{"a*b*c", "axxbyyc", true},
		{"a*b*c", "acb", false},
		{"ab*ba", "aba", false},
		{"a.b", "axb", false},
	} {
		if got := ScopeMatches(tc.pattern, tc.scope); got != tc.want {
			t.Errorf("ScopeMatches(%q, %q) = %v, want %v", tc.pattern, tc.scope, got, tc.want)
		}
	}
}

// TestQueryScopePatternCoversGrail holds that every Grail read scope dtctl
// knows of, the ones a DQL statement can need, matches QueryScopePattern, and
// that the pattern matches no write.
func TestQueryScopePatternCoversGrail(t *testing.T) {
	grail := append(append([]string{}, QueryScopes...), grailExtendedReadScopes...)
	for _, access := range ResourceScopes {
		for _, s := range access.Read {
			if strings.HasPrefix(s, "storage:") {
				grail = append(grail, s)
			}
		}
	}
	grail = append(grail, "storage:buckets:read", "storage:user.events:read", "storage:user.sessions:read",
		"storage:filter-segments:read", "storage:files:read")
	for _, s := range grail {
		if !ScopeMatches(QueryScopePattern, s) {
			t.Errorf("%s is a Grail read scope, but does not match %s", s, QueryScopePattern)
		}
	}
	for _, s := range []string{"storage:buckets:write", "storage:files:delete", "settings:objects:read"} {
		if ScopeMatches(QueryScopePattern, s) {
			t.Errorf("%s matches %s", s, QueryScopePattern)
		}
	}
}
