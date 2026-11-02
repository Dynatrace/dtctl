//go:build integration
// +build integration

package e2e

import (
	"testing"

	"github.com/Dynatrace/dtctl/pkg/resources/classicpipelinestranslate"
	"github.com/Dynatrace/dtctl/test/integration"
)

// TestClassicPipelinesTranslation exercises the read-only classic-pipelines
// translation endpoint. The endpoint is early-adopter and may not be available
// on every tenant, so failures are reported but the test tolerates an empty
// translation (some tenants have no classic pipeline configured for a scope).
func TestClassicPipelinesTranslation(t *testing.T) {
	env := integration.SetupIntegration(t)
	defer env.Cleanup.Cleanup(t)

	handler := classicpipelinestranslate.NewHandler(env.Client)

	for _, scope := range classicpipelinestranslate.ValidConfigurations {
		t.Run("translate "+scope, func(t *testing.T) {
			result, err := handler.Translate(classicpipelinestranslate.TranslateOptions{
				Configuration:     scope,
				SkipDisabledRules: true,
			})
			if err != nil {
				t.Fatalf("Translate(%q) failed: %v", scope, err)
			}

			if result.Value == nil {
				t.Logf("No translated pipeline returned for scope %q (none configured)", scope)
				return
			}
			t.Logf("Translated %q pipeline returned (withWarning=%v)", scope, result.WithWarning)
		})
	}
}
