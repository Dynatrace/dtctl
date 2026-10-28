//go:build integration
// +build integration

package e2e

import (
	"testing"

	"github.com/Dynatrace/dtctl/pkg/resources/hub"
	"github.com/Dynatrace/dtctl/test/integration"
)

func TestHubExtensionList(t *testing.T) {
	env := integration.SetupIntegration(t)
	defer env.Cleanup.Cleanup(t)

	handler := hub.NewHandler(env.Client)

	t.Run("list all hub extensions", func(t *testing.T) {
		result, err := handler.ListExtensions("", 0)
		if err != nil {
			t.Fatalf("Failed to list Hub extensions: %v", err)
		}

		if len(result.Items) == 0 {
			t.Skip("No Hub extensions available in this environment")
		}

		t.Logf("Found %d Hub extension(s)", len(result.Items))
		for i, ext := range result.Items {
			if i >= 5 {
				t.Logf("  ... and %d more", len(result.Items)-5)
				break
			}
			t.Logf("  - %s (%s)", ext.ID, ext.Name)
		}
	})

	t.Run("list with pagination", func(t *testing.T) {
		// Use small page size to exercise pagination
		result, err := handler.ListExtensions("", 2)
		if err != nil {
			t.Fatalf("Failed to list Hub extensions with pagination: %v", err)
		}

		t.Logf("Found %d Hub extension(s) via paginated fetch, got %d items",
			result.TotalCount, len(result.Items))
	})

	t.Run("list with filter", func(t *testing.T) {
		// Use a broad filter that should match common extensions
		result, err := handler.ListExtensions("dynatrace", 0)
		if err != nil {
			t.Fatalf("Failed to list Hub extensions with filter: %v", err)
		}

		t.Logf("Found %d Hub extension(s) matching 'dynatrace'", len(result.Items))
		for i, ext := range result.Items {
			if i >= 5 {
				t.Logf("  ... and %d more", len(result.Items)-5)
				break
			}
			t.Logf("  - %s (%s)", ext.ID, ext.Name)
		}
	})

	t.Run("filter with no matches", func(t *testing.T) {
		result, err := handler.ListExtensions("zzz-nonexistent-filter-zzz", 0)
		if err != nil {
			t.Fatalf("Failed to list Hub extensions with non-matching filter: %v", err)
		}

		if len(result.Items) != 0 {
			t.Errorf("Expected 0 results for non-matching filter, got %d", len(result.Items))
		}
	})
}

func TestHubExtensionGet(t *testing.T) {
	env := integration.SetupIntegration(t)
	defer env.Cleanup.Cleanup(t)

	handler := hub.NewHandler(env.Client)

	// Find the first available extension to use for get/describe
	list, err := handler.ListExtensions("", 0)
	if err != nil {
		t.Fatalf("Failed to list Hub extensions: %v", err)
	}
	if len(list.Items) == 0 {
		t.Skip("No Hub extensions available in this environment")
	}
	firstExt := list.Items[0]

	t.Run("get specific extension", func(t *testing.T) {
		ext, err := handler.GetExtension(firstExt.ID)
		if err != nil {
			t.Fatalf("Failed to get Hub extension %q: %v", firstExt.ID, err)
		}

		if ext.ID != firstExt.ID {
			t.Errorf("Expected ID %q, got %q", firstExt.ID, ext.ID)
		}
		if ext.Name == "" {
			t.Error("Expected non-empty name")
		}

		t.Logf("Got Hub extension: %s (%s)", ext.ID, ext.Name)
	})

	t.Run("get non-existent extension", func(t *testing.T) {
		_, err := handler.GetExtension("com.example.nonexistent.hub-extension.invalid")
		if err == nil {
			t.Error("Expected error when getting non-existent Hub extension, got nil")
		} else {
			t.Logf("Got expected error: %v", err)
		}
	})
}

func TestHubExtensionReleases(t *testing.T) {
	env := integration.SetupIntegration(t)
	defer env.Cleanup.Cleanup(t)

	handler := hub.NewHandler(env.Client)

	// Find the first available extension
	list, err := handler.ListExtensions("", 0)
	if err != nil {
		t.Fatalf("Failed to list Hub extensions: %v", err)
	}
	if len(list.Items) == 0 {
		t.Skip("No Hub extensions available in this environment")
	}
	firstExt := list.Items[0]

	t.Run("list releases", func(t *testing.T) {
		releases, err := handler.ListExtensionReleases(firstExt.ID, 0)
		if err != nil {
			t.Fatalf("Failed to list releases for %q: %v", firstExt.ID, err)
		}

		t.Logf("Extension %q has %d release(s)", firstExt.ID, len(releases.Items))
		for i, rel := range releases.Items {
			if i >= 5 {
				t.Logf("  ... and %d more", len(releases.Items)-5)
				break
			}
			t.Logf("  - %s (released: %s)", rel.Version, rel.ReleaseDate)
		}
	})

	t.Run("list releases with pagination", func(t *testing.T) {
		releases, err := handler.ListExtensionReleases(firstExt.ID, 2)
		if err != nil {
			t.Fatalf("Failed to list releases with pagination: %v", err)
		}

		t.Logf("Found %d release(s) via paginated fetch", len(releases.Items))
	})
}
