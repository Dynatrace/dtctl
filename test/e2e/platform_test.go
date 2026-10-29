//go:build integration
// +build integration

package e2e

import (
	"testing"

	"github.com/Dynatrace/dtctl/pkg/resources/platform"
	"github.com/Dynatrace/dtctl/test/integration"
)

// TestPlatformReadLifecycle exercises the platform management handler methods
// that back get environment, get license, and get license-settings. All three
// endpoints are read-only (no create/delete), so there is nothing to clean up.
func TestPlatformReadLifecycle(t *testing.T) {
	env := integration.SetupIntegration(t)
	defer env.Cleanup.Cleanup(t)

	handler := platform.NewHandler(env.Client)

	t.Run("get environment", func(t *testing.T) {
		info, err := handler.GetEnvironment()
		if err != nil {
			t.Fatalf("GetEnvironment failed: %v", err)
		}
		if info.EnvironmentID == "" {
			t.Error("expected a non-empty environment ID")
		}
		if info.State == "" {
			t.Error("expected a non-empty state")
		}
		t.Logf("environment %q: type=%s state=%s", info.EnvironmentID, info.Type, info.State)
	})

	t.Run("get license", func(t *testing.T) {
		lic, err := handler.GetLicense()
		if err != nil {
			t.Fatalf("GetLicense failed: %v", err)
		}
		t.Logf("license: trial=%v platformSubscription=%v", lic.Trial, lic.PlatformSubscription)
	})

	t.Run("get license-settings all", func(t *testing.T) {
		settings, err := handler.GetLicenseSettings()
		if err != nil {
			t.Fatalf("GetLicenseSettings() failed: %v", err)
		}
		t.Logf("license-settings: %d entries", len(settings))
	})

	t.Run("get license-settings filtered", func(t *testing.T) {
		all, err := handler.GetLicenseSettings()
		if err != nil {
			t.Fatalf("GetLicenseSettings() failed: %v", err)
		}
		if len(all) == 0 {
			t.Skip("no license settings available to filter")
		}
		key := all[0].Key
		filtered, err := handler.GetLicenseSettings(key)
		if err != nil {
			t.Fatalf("GetLicenseSettings(%q) failed: %v", key, err)
		}
		if len(filtered) == 0 {
			t.Errorf("expected at least one result when filtering by key %q", key)
		}
		for _, s := range filtered {
			if s.Key != key {
				t.Errorf("got unexpected key %q in filtered result (wanted %q)", s.Key, key)
			}
		}
		t.Logf("filtered by %q: %d result(s)", key, len(filtered))
	})
}
