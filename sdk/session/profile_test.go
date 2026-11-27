package session

import "testing"

func TestSegmentPrefix(t *testing.T) {
	tests := []struct {
		prefix, path string
		want         bool
	}{
		{"get", "get workflows", true},
		{"get", "get", true},
		{"get workflows", "get", false}, // prefix longer than path
		{"get", "getterm", false},       // not a whole-segment match
		{"describe", "describe analyzer", true},
		{"get problems", "get slo", false},
		{"", "get", false}, // empty prefix never matches
		{"get", "", false}, // empty path
		{"exec analyzer", "exec analyzer foo", true},
	}
	for _, tt := range tests {
		if got := segmentPrefix(tt.prefix, tt.path); got != tt.want {
			t.Errorf("segmentPrefix(%q, %q) = %v, want %v", tt.prefix, tt.path, got, tt.want)
		}
	}
}

func TestProfileAllows(t *testing.T) {
	p := &Profile{Name: "query", Commands: []string{"query", "get analyzers", "describe analyzer"}}

	allowed := []string{
		"query",             // direct
		"query foo",         // subtree of allowed entry
		"get analyzers",     // direct multi-segment
		"describe analyzer", // direct multi-segment
		"get",               // ancestor of "get analyzers" — stays reachable
		"describe",          // ancestor of "describe analyzer"
		"commands",          // always-available core
		"commands howto",    // always-available subtree
		"help",              // always-available core
	}
	for _, path := range allowed {
		if !p.Allows(path) {
			t.Errorf("Allows(%q) = false, want true", path)
		}
	}

	masked := []string{
		"auth",
		"auth login",
		"get workflows", // sibling of allowed child, not itself allowed
		"delete",
		"apply",
		"get analyzer",       // singular — not the allowlisted "get analyzers"
		"config",             // no longer always-available — must be listed
		"config set-context", // ditto (credential/context mutation)
		"ctx",                // no longer always-available
		"completion",         // no longer always-available
		"version",            // no longer always-available
	}
	for _, path := range masked {
		if p.Allows(path) {
			t.Errorf("Allows(%q) = true, want false", path)
		}
	}
}

// TestProfileAllowsOnlyAsAncestor: a parent of an entry is ancestor-only,
// unless it is listed, below an entry, or always available.
func TestProfileAllowsOnlyAsAncestor(t *testing.T) {
	p := &Profile{Name: "p", Commands: []string{
		"apply extension-config", "exec copilot nl2dql", "get gcp connections principal",
		"query", "describe", "commands howto",
	}}

	for path, want := range map[string]bool{
		"apply":                         true,
		"exec":                          true,
		"exec copilot":                  true,
		"get":                           true,
		"get gcp":                       true,
		"get gcp connections":           true,
		"apply extension-config":        false, // listed
		"exec copilot nl2dql":           false, // listed
		"get gcp connections principal": false, // listed
		"query":                         false, // listed
		"describe analyzer":             false, // below a listed entry
		"commands":                      false, // always available
		"auth":                          false, // not allowed
		"exec copilot dql2nl":           false, // a sibling, not allowed
	} {
		if got := p.AllowsOnlyAsAncestor(path); got != want {
			t.Errorf("AllowsOnlyAsAncestor(%q) = %v, want %v", path, got, want)
		}
	}

	listed := &Profile{Name: "p", Commands: []string{"apply", "apply extension-config"}}
	if listed.AllowsOnlyAsAncestor("apply") {
		t.Error("a listed parent is not ancestor-only")
	}
}

func TestResolveProfile_Precedence(t *testing.T) {
	cfg := &Config{
		CurrentContext: "ctx1",
		Contexts: []NamedContext{
			{Name: "ctx1", Context: Context{Profile: "investigate"}},
		},
		Profiles: map[string]Profile{
			"custom": {Description: "user", Commands: []string{"get"}},
		},
	}

	// Env wins over context binding.
	p, err := cfg.resolveProfile("query")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if p == nil || p.Name != "query" {
		t.Fatalf("env precedence: got %+v, want built-in query", p)
	}

	// No env → context-bound profile.
	p, err = cfg.resolveProfile("")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if p == nil || p.Name != "investigate" {
		t.Fatalf("context binding: got %+v, want investigate", p)
	}

	// User profile takes precedence over any preset of the same name and resolves.
	p, err = cfg.resolveProfile("custom")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if p == nil || p.Name != "custom" || p.Description != "user" {
		t.Fatalf("user profile: got %+v, want custom", p)
	}
}

func TestResolveProfile_FullAndNone(t *testing.T) {
	// No binding, no env → full (nil).
	cfg := &Config{CurrentContext: "ctx1", Contexts: []NamedContext{{Name: "ctx1"}}}
	p, err := cfg.resolveProfile("")
	if err != nil || p != nil {
		t.Fatalf("expected nil profile (full), got %+v err=%v", p, err)
	}

	// Explicit "full" → nil.
	p, err = cfg.resolveProfile("full")
	if err != nil || p != nil {
		t.Fatalf("explicit full: expected nil, got %+v err=%v", p, err)
	}
}

func TestResolveProfile_Unknown(t *testing.T) {
	cfg := &Config{CurrentContext: "ctx1", Contexts: []NamedContext{{Name: "ctx1"}}}
	if _, err := cfg.resolveProfile("nope"); err == nil {
		t.Fatal("expected error for unknown profile, got nil")
	}
}

func TestProfileExists(t *testing.T) {
	cfg := &Config{Profiles: map[string]Profile{"custom": {}}}
	for _, name := range []string{"full", "query", "investigate", "custom"} {
		if !cfg.ProfileExists(name) {
			t.Errorf("ProfileExists(%q) = false, want true", name)
		}
	}
	if cfg.ProfileExists("bogus") {
		t.Error("ProfileExists(bogus) = true, want false")
	}
}
