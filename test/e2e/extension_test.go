//go:build integration
// +build integration

package e2e

import (
	"archive/zip"
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/Dynatrace/dtctl/pkg/resources/extension"
	"github.com/Dynatrace/dtctl/test/integration"
)

func TestExtensionList(t *testing.T) {
	env := integration.SetupIntegration(t)
	defer env.Cleanup.Cleanup(t)

	handler := extension.NewHandler(env.Client)

	t.Run("list all extensions", func(t *testing.T) {
		result, err := handler.List(context.Background(), "", 0)
		if err != nil {
			t.Fatalf("Failed to list extensions: %v", err)
		}

		if result.TotalCount == 0 {
			t.Skip("No extensions installed in this environment")
		}

		t.Logf("Found %d extension(s)", result.TotalCount)
		for i, ext := range result.Items {
			if i >= 5 {
				t.Logf("  ... and %d more", len(result.Items)-5)
				break
			}
			t.Logf("  - %s (active: %s)", ext.ExtensionName, ext.Version)
		}
	})

	t.Run("list with name filter", func(t *testing.T) {
		// Use a broad filter that should match at least some extensions
		result, err := handler.List(context.Background(), "com.dynatrace", 10)
		if err != nil {
			t.Fatalf("Failed to list extensions with filter: %v", err)
		}

		t.Logf("Found %d extension(s) matching filter", result.TotalCount)
		for _, ext := range result.Items {
			t.Logf("  - %s", ext.ExtensionName)
		}
	})

	t.Run("list with pagination", func(t *testing.T) {
		// Use small page size to exercise pagination
		result, err := handler.List(context.Background(), "", 2)
		if err != nil {
			t.Fatalf("Failed to list extensions with pagination: %v", err)
		}

		t.Logf("Found %d total extension(s) via paginated fetch, got %d items", result.TotalCount, len(result.Items))

		// All items should have been collected across pages
		if result.TotalCount > 0 && len(result.Items) != result.TotalCount {
			t.Errorf("Expected %d items after pagination, got %d", result.TotalCount, len(result.Items))
		}
	})
}

// findFirstExtension is a helper that returns the first available extension.
// Skips the test if no extensions are installed.
func findFirstExtension(t *testing.T, handler *extension.Handler) extension.Extension {
	t.Helper()
	result, err := handler.List(context.Background(), "", 0)
	if err != nil {
		t.Fatalf("Failed to list extensions: %v", err)
	}
	if len(result.Items) == 0 {
		t.Skip("No extensions installed in this environment")
	}
	return result.Items[0]
}

func TestExtensionGet(t *testing.T) {
	env := integration.SetupIntegration(t)
	defer env.Cleanup.Cleanup(t)

	handler := extension.NewHandler(env.Client)
	ext := findFirstExtension(t, handler)

	t.Run("get extension versions", func(t *testing.T) {
		versions, err := handler.Get(ext.ExtensionName)
		if err != nil {
			t.Fatalf("Failed to get extension %q: %v", ext.ExtensionName, err)
		}

		if len(versions.Items) == 0 {
			t.Fatal("Expected at least one version")
		}

		t.Logf("Extension %q has %d version(s):", ext.ExtensionName, versions.TotalCount)
		for _, v := range versions.Items {
			activeStr := ""
			if v.Active {
				activeStr = " (active)"
			}
			t.Logf("  - %s%s", v.Version, activeStr)
		}
	})
}

func TestExtensionGetVersion(t *testing.T) {
	env := integration.SetupIntegration(t)
	defer env.Cleanup.Cleanup(t)

	handler := extension.NewHandler(env.Client)
	ext := findFirstExtension(t, handler)

	// Get the version list first to find a valid version
	versions, err := handler.Get(ext.ExtensionName)
	if err != nil {
		t.Fatalf("Failed to get extension versions: %v", err)
	}
	if len(versions.Items) == 0 {
		t.Skip("No versions available for extension")
	}

	version := versions.Items[0].Version

	t.Run("get version details", func(t *testing.T) {
		details, err := handler.GetVersion(ext.ExtensionName, version)
		if err != nil {
			t.Fatalf("Failed to get version details: %v", err)
		}

		if details.ExtensionName != ext.ExtensionName {
			t.Errorf("Expected extension name %q, got %q", ext.ExtensionName, details.ExtensionName)
		}
		if details.Version != version {
			t.Errorf("Expected version %q, got %q", version, details.Version)
		}

		t.Logf("Extension: %s v%s", details.ExtensionName, details.Version)
		t.Logf("  Author: %s", details.Author.Name)
		t.Logf("  DataSources: %v", details.DataSources)
		t.Logf("  FeatureSets: %v", details.FeatureSets)
		if details.MinDynatraceVersion != "" {
			t.Logf("  MinDT: %s", details.MinDynatraceVersion)
		}
	})

	t.Run("get non-existent version", func(t *testing.T) {
		_, err := handler.GetVersion(ext.ExtensionName, "99.99.99")
		if err == nil {
			t.Error("Expected error when getting non-existent version, got nil")
		} else {
			t.Logf("Got expected error: %v", err)
		}
	})
}

func TestExtensionGetEnvironmentConfig(t *testing.T) {
	env := integration.SetupIntegration(t)
	defer env.Cleanup.Cleanup(t)

	handler := extension.NewHandler(env.Client)

	// Find an extension with an active version (environment config only exists if a version is active)
	result, err := handler.List(context.Background(), "", 0)
	if err != nil {
		t.Fatalf("Failed to list extensions: %v", err)
	}

	var activeExt *extension.Extension
	for _, ext := range result.Items {
		if ext.Version != "" {
			activeExt = &ext
			break
		}
	}

	if activeExt == nil {
		t.Skip("No extension with an active version found in this environment")
	}

	t.Run("get environment config", func(t *testing.T) {
		// Try all extensions with a version — not all have an environment configuration
		var config *extension.ExtensionEnvironmentConfig
		var foundExt *extension.Extension
		for _, ext := range result.Items {
			if ext.Version == "" {
				continue
			}
			cfg, err := handler.GetEnvironmentConfig(ext.ExtensionName, ext.Version)
			if err != nil {
				t.Logf("Extension %q version %q has no environment config: %v", ext.ExtensionName, ext.Version, err)
				continue
			}
			config = cfg
			foundExt = &ext
			break
		}

		if config == nil {
			t.Skip("No extension with an active environment configuration found in this environment")
		}

		if config.Version == "" {
			t.Error("Expected non-empty version in environment config")
		}

		t.Logf("Extension %q environment config: version=%s", foundExt.ExtensionName, config.Version)
	})

	t.Run("get environment config for non-existent extension", func(t *testing.T) {
		_, err := handler.GetEnvironmentConfig("com.example.nonexistent.extension.invalid", "1.0.0")
		if err == nil {
			t.Error("Expected error when getting env config for non-existent extension, got nil")
		} else {
			t.Logf("Got expected error: %v", err)
		}
	})
}

func TestExtensionGetNonExistent(t *testing.T) {
	env := integration.SetupIntegration(t)
	defer env.Cleanup.Cleanup(t)

	handler := extension.NewHandler(env.Client)

	t.Run("get non-existent extension", func(t *testing.T) {
		_, err := handler.Get("com.example.nonexistent.extension.invalid")
		if err == nil {
			t.Error("Expected error when getting non-existent extension, got nil")
		} else {
			t.Logf("Got expected error: %v", err)
		}
	})
}

func TestMonitoringConfigurationLifecycle(t *testing.T) {
	env := integration.SetupIntegration(t)
	defer env.Cleanup.Cleanup(t)

	handler := extension.NewHandler(env.Client)

	// Find an extension with an active version that accepts our generic fixture.
	// Extensions like da-aws require cloud-provider-specific fields; we iterate
	// until we find one that creates successfully, then use it for the lifecycle.
	result, err := handler.List(context.Background(), "", 0)
	if err != nil {
		t.Fatalf("Failed to list extensions: %v", err)
	}

	var activeExt *extension.Extension
	var created *extension.MonitoringConfiguration
	for i := range result.Items {
		ext := &result.Items[i]
		if ext.Version == "" {
			continue
		}
		body := integration.MonitoringConfigFixture(env.TestPrefix, ext.ExtensionName, ext.Version)
		c, createErr := handler.CreateMonitoringConfiguration(ext.ExtensionName, body)
		if createErr == nil {
			activeExt = ext
			created = c
			env.Cleanup.TrackExtensionConfig(ext.ExtensionName, c.ObjectID)
			break
		}
		if strings.Contains(createErr.Error(), "write access") || strings.Contains(createErr.Error(), "access denied") || strings.Contains(createErr.Error(), "403") {
			t.Skipf("Insufficient permissions to create monitoring configuration: %v", createErr)
		}
		t.Logf("Extension %q requires additional configuration (%v), trying next...", ext.ExtensionName, createErr)
	}

	if activeExt == nil {
		t.Skip("No extension accepts the generic monitoring config fixture — skipping lifecycle test")
	}

	t.Logf("Using extension %q (version: %s) for monitoring config lifecycle test",
		activeExt.ExtensionName, activeExt.Version)

	t.Run("complete lifecycle", func(t *testing.T) {
		// Step 1 already done above (create was needed to find a compatible extension)
		if created.ObjectID == "" {
			t.Fatal("Created monitoring configuration has no ObjectID")
		}
		t.Logf("Created monitoring config: %s (scope: %s)", created.ObjectID, created.Scope)

		// Step 2: Get monitoring configuration
		t.Log("Step 2: Getting monitoring configuration...")
		retrieved, err := handler.GetMonitoringConfiguration(activeExt.ExtensionName, created.ObjectID)
		if err != nil {
			t.Fatalf("Failed to get monitoring configuration: %v", err)
		}
		if retrieved.ObjectID != created.ObjectID {
			t.Errorf("Retrieved config ID mismatch: got %s, want %s", retrieved.ObjectID, created.ObjectID)
		}
		t.Logf("Retrieved monitoring config: %s", retrieved.ObjectID)

		// Step 3: List monitoring configurations
		t.Log("Step 3: Listing monitoring configurations...")
		list, err := handler.ListMonitoringConfigurations(activeExt.ExtensionName, "", 0)
		if err != nil {
			t.Fatalf("Failed to list monitoring configurations: %v", err)
		}
		found := false
		for _, cfg := range list.Items {
			if cfg.ObjectID == created.ObjectID {
				found = true
				break
			}
		}
		if !found {
			t.Error("Created monitoring config not found in list")
		} else {
			t.Logf("Found monitoring config in list (total: %d configs)", list.TotalCount)
		}

		// Step 4: Update monitoring configuration
		t.Log("Step 4: Updating monitoring configuration...")
		updateBody := integration.MonitoringConfigFixtureModified(env.TestPrefix, activeExt.Version)
		updated, err := handler.UpdateMonitoringConfiguration(activeExt.ExtensionName, created.ObjectID, updateBody)
		if err != nil {
			t.Fatalf("Failed to update monitoring configuration: %v", err)
		}
		t.Logf("Updated monitoring config: %s", updated.ObjectID)

		// Step 5: Verify update
		t.Log("Step 5: Verifying update...")
		verified, err := handler.GetMonitoringConfiguration(activeExt.ExtensionName, created.ObjectID)
		if err != nil {
			t.Fatalf("Failed to get updated monitoring configuration: %v", err)
		}
		if verified.ObjectID != created.ObjectID {
			t.Errorf("Verified config ID mismatch: got %s, want %s", verified.ObjectID, created.ObjectID)
		}
		t.Logf("Verified updated monitoring config: %s", verified.ObjectID)

		// Step 6: Delete monitoring configuration
		t.Log("Step 6: Deleting monitoring configuration...")
		err = handler.DeleteMonitoringConfiguration(activeExt.ExtensionName, created.ObjectID)
		if err != nil {
			t.Fatalf("Failed to delete monitoring configuration: %v", err)
		}
		t.Logf("Deleted monitoring config: %s", created.ObjectID)

		// Untrack since we already deleted it
		env.Cleanup.Untrack("extension-config", created.ObjectID)

		// Step 7: Verify deletion
		t.Log("Step 7: Verifying deletion...")
		_, err = handler.GetMonitoringConfiguration(activeExt.ExtensionName, created.ObjectID)
		if err == nil {
			t.Error("Expected error when getting deleted monitoring config, got nil")
		} else {
			t.Logf("Verified deletion (got expected error: %v)", err)
		}
	})
}

func TestMonitoringConfigurationList(t *testing.T) {
	env := integration.SetupIntegration(t)
	defer env.Cleanup.Cleanup(t)

	handler := extension.NewHandler(env.Client)
	ext := findFirstExtension(t, handler)

	t.Run("list monitoring configurations", func(t *testing.T) {
		result, err := handler.ListMonitoringConfigurations(ext.ExtensionName, "", 0)
		if err != nil {
			t.Fatalf("Failed to list monitoring configurations: %v", err)
		}

		t.Logf("Extension %q has %d monitoring configuration(s)", ext.ExtensionName, result.TotalCount)
		for _, cfg := range result.Items {
			t.Logf("  - %s (scope: %s)", cfg.ObjectID, cfg.Scope)
		}
	})

	t.Run("list for non-existent extension", func(t *testing.T) {
		_, err := handler.ListMonitoringConfigurations("com.example.nonexistent.extension.invalid", "", 0)
		if err == nil {
			t.Error("Expected error when listing configs for non-existent extension, got nil")
		} else {
			t.Logf("Got expected error: %v", err)
		}
	})
}

func TestMonitoringConfigurationGetNonExistent(t *testing.T) {
	env := integration.SetupIntegration(t)
	defer env.Cleanup.Cleanup(t)

	handler := extension.NewHandler(env.Client)
	ext := findFirstExtension(t, handler)

	t.Run("get non-existent monitoring configuration", func(t *testing.T) {
		_, err := handler.GetMonitoringConfiguration(ext.ExtensionName, "nonexistent-config-id-12345")
		if err == nil {
			t.Error("Expected error when getting non-existent monitoring config, got nil")
		} else {
			t.Logf("Got expected error: %v", err)
		}
	})
}

func TestMonitoringConfigurationDeleteNonExistent(t *testing.T) {
	env := integration.SetupIntegration(t)
	defer env.Cleanup.Cleanup(t)

	handler := extension.NewHandler(env.Client)
	ext := findFirstExtension(t, handler)

	t.Run("delete non-existent monitoring configuration", func(t *testing.T) {
		err := handler.DeleteMonitoringConfiguration(ext.ExtensionName, "nonexistent-config-id-12345")
		if err == nil {
			t.Error("Expected error when deleting non-existent monitoring config, got nil")
		} else {
			t.Logf("Got expected error: %v", err)
		}
	})
}

func TestExtensionDownload(t *testing.T) {
	env := integration.SetupIntegration(t)
	defer env.Cleanup.Cleanup(t)

	handler := extension.NewHandler(env.Client)
	ext := findFirstExtension(t, handler)

	versions, err := handler.Get(ext.ExtensionName)
	if err != nil {
		t.Fatalf("Failed to get extension versions: %v", err)
	}
	if len(versions.Items) == 0 {
		t.Skip("No versions available")
	}
	version := versions.Items[0].Version

	t.Run("download returns valid zip bytes", func(t *testing.T) {
		data, err := handler.Download(ext.ExtensionName, version)
		if err != nil {
			t.Fatalf("Download failed: %v", err)
		}
		if len(data) == 0 {
			t.Fatal("Download returned empty bytes")
		}
		zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
		if err != nil {
			t.Fatalf("Downloaded bytes are not a valid zip: %v", err)
		}
		t.Logf("Extension %s v%s: downloaded %d bytes, %d zip entries", ext.ExtensionName, version, len(data), len(zr.File))
	})

	t.Run("download non-existent extension returns error", func(t *testing.T) {
		_, err := handler.Download("com.example.nonexistent.extension.invalid", "1.0.0")
		if err == nil {
			t.Error("Expected error for non-existent extension, got nil")
		} else {
			t.Logf("Got expected error: %v", err)
		}
	})
}

func TestExtensionParseAssets(t *testing.T) {
	env := integration.SetupIntegration(t)
	defer env.Cleanup.Cleanup(t)

	handler := extension.NewHandler(env.Client)
	ext := findFirstExtension(t, handler)

	versions, err := handler.Get(ext.ExtensionName)
	if err != nil {
		t.Fatalf("Failed to get extension versions: %v", err)
	}
	if len(versions.Items) == 0 {
		t.Skip("No versions available")
	}
	version := versions.Items[0].Version

	data, err := handler.Download(ext.ExtensionName, version)
	if err != nil {
		t.Fatalf("Download failed: %v", err)
	}

	t.Run("parse alert_templates returns no content when full=false", func(t *testing.T) {
		result, err := extension.ParseAssets(data, []string{"alert_templates"}, false)
		if err != nil {
			t.Fatalf("ParseAssets failed: %v", err)
		}
		t.Logf("Found %d alert template(s)", len(result.AlertTemplates))
		for _, a := range result.AlertTemplates {
			if a.Content != nil {
				t.Errorf("alert %q has non-nil Content with full=false (content leak bug)", a.File)
			}
			t.Logf("  alert: %s / %s / enabled=%v", a.Name, a.EventType, a.Enabled)
		}
	})

	t.Run("parse alert_templates returns content when full=true", func(t *testing.T) {
		result, err := extension.ParseAssets(data, []string{"alert_templates"}, true)
		if err != nil {
			t.Fatalf("ParseAssets failed: %v", err)
		}
		for _, a := range result.AlertTemplates {
			if a.Content == nil {
				t.Errorf("alert %q has nil Content with full=true", a.File)
			}
		}
	})

	t.Run("parse smartscape returns result", func(t *testing.T) {
		result, err := extension.ParseAssets(data, []string{"smartscape"}, false)
		if err != nil {
			t.Fatalf("ParseAssets failed: %v", err)
		}
		if result.Smartscape == nil {
			t.Fatal("Expected non-nil Smartscape result")
		}
		t.Logf("Found %d node(s) and %d edge(s)", len(result.Smartscape.Nodes), len(result.Smartscape.Edges))
		for _, n := range result.Smartscape.Nodes {
			if n.Content != nil {
				t.Errorf("node %q has non-nil Content with full=false", n.NodeType)
			}
		}
	})

	t.Run("parse both types in one call", func(t *testing.T) {
		result, err := extension.ParseAssets(data, []string{"alert_templates", "smartscape"}, false)
		if err != nil {
			t.Fatalf("ParseAssets failed: %v", err)
		}
		if result.AlertTemplates == nil {
			t.Error("Expected non-nil AlertTemplates")
		}
		if result.Smartscape == nil {
			t.Error("Expected non-nil Smartscape")
		}
	})

	t.Run("unknown asset type returns error", func(t *testing.T) {
		_, err := extension.ParseAssets(data, []string{"dashboards"}, false)
		if err == nil {
			t.Error("Expected error for unknown asset type, got nil")
		} else {
			t.Logf("Got expected error: %v", err)
		}
	})
}
