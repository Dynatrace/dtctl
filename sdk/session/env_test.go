package session

import "testing"

func TestConfigWithEnvAnswersLookupsFromItsOwnEnvironment(t *testing.T) {
	// The process says "development"; the Config's own environment says
	// "stable". The Config must believe its own.
	t.Setenv(MinStabilityEnvVar, string(StabilityDevelopment))

	cfg := NewConfig().WithEnv(func(key string) (string, bool) {
		if key == MinStabilityEnvVar {
			return string(StabilityStable), true
		}
		return "", false
	})
	got, err := cfg.ResolveMinStability()
	if err != nil {
		t.Fatalf("ResolveMinStability: %v", err)
	}
	if got != StabilityStable {
		t.Errorf("ResolveMinStability = %q, want %q from the Config's own environment", got, StabilityStable)
	}
}

func TestConfigWithEnvCanScrubAVariableTheProcessSets(t *testing.T) {
	t.Setenv(MinStabilityEnvVar, string(StabilityDevelopment))

	scrubbed := NewConfig().WithEnv(func(string) (string, bool) { return "", false })
	want, err := NewConfig().resolveMinStability("")
	if err != nil {
		t.Fatalf("resolveMinStability: %v", err)
	}
	got, err := scrubbed.ResolveMinStability()
	if err != nil {
		t.Fatalf("ResolveMinStability: %v", err)
	}
	if got != want {
		t.Errorf("ResolveMinStability = %q, want the unset-variable default %q", got, want)
	}
}

func TestConfigWithEnvNilRestoresTheProcessEnvironment(t *testing.T) {
	t.Setenv(MinStabilityEnvVar, string(StabilityDevelopment))

	cfg := NewConfig().WithEnv(func(string) (string, bool) { return string(StabilityStable), true })
	cfg.WithEnv(nil)

	got, err := cfg.ResolveMinStability()
	if err != nil {
		t.Fatalf("ResolveMinStability: %v", err)
	}
	if got != StabilityDevelopment {
		t.Errorf("ResolveMinStability = %q, want %q from the process environment", got, StabilityDevelopment)
	}
}

func TestConfigWithEnvAppliesToProfilesAndDevelopmentFeatures(t *testing.T) {
	t.Setenv(ProfileEnvVar, "nonexistent-in-process")
	t.Setenv(DevelopmentEnvVar, "")

	cfg := NewConfig().WithEnv(func(key string) (string, bool) {
		switch key {
		case ProfileEnvVar:
			return "query", true
		case DevelopmentEnvVar:
			return "all", true
		}
		return "", false
	})

	profile, err := cfg.ResolveProfile()
	if err != nil {
		t.Fatalf("ResolveProfile: %v (the process variable must not be consulted)", err)
	}
	if profile == nil || profile.Name != "query" {
		t.Errorf("ResolveProfile = %+v, want the %q profile from the Config's own environment", profile, "query")
	}
	if !cfg.EnabledDevelopmentFeatures()[DevelopmentAll] {
		t.Errorf("EnabledDevelopmentFeatures = %v, want %q enabled by the Config's own environment", cfg.EnabledDevelopmentFeatures(), DevelopmentAll)
	}
}
