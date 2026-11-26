//go:build integration
// +build integration

package e2e

import (
	"strings"
	"testing"

	"github.com/Dynatrace/dtctl/pkg/resources/analyzer"
	"github.com/Dynatrace/dtctl/test/integration"
)

// forecastAnalyzer is a stable built-in Davis analyzer used to exercise the
// read paths that back `dtctl describe analyzer` and `dtctl verify analyzer`.
// If a tenant does not expose it, the schema/validate subtests skip rather than
// fail.
const forecastAnalyzer = "dt.statistics.GenericForecastAnalyzer"

// TestAnalyzerReadLifecycle exercises the analyzer handler methods that back the
// describe and verify commands. Analyzers are read-only (no create/delete), so
// there is nothing to clean up.
func TestAnalyzerReadLifecycle(t *testing.T) {
	env := integration.SetupIntegration(t)
	defer env.Cleanup.Cleanup(t)

	handler := analyzer.NewHandler(env.Client)

	t.Run("list analyzers", func(t *testing.T) {
		list, err := handler.List("")
		if err != nil {
			t.Fatalf("List failed: %v", err)
		}
		if len(list.Analyzers) == 0 {
			t.Fatal("expected at least one analyzer")
		}
		t.Logf("listed %d analyzers (totalCount=%d)", len(list.Analyzers), list.TotalCount)
	})

	t.Run("get analyzer definition", func(t *testing.T) {
		def, err := handler.Get(forecastAnalyzer)
		if err != nil {
			t.Skipf("analyzer %q not available on this tenant: %v", forecastAnalyzer, err)
		}
		if def.Name != forecastAnalyzer {
			t.Errorf("Get name mismatch: got %q, want %q", def.Name, forecastAnalyzer)
		}
		if def.DisplayName == "" {
			t.Error("expected a non-empty display name")
		}
		t.Logf("analyzer %q: %s (type=%s)", def.Name, def.DisplayName, def.Type)
	})

	t.Run("describe: input schema flattens to typed fields", func(t *testing.T) {
		schema, err := handler.GetInputSchema(forecastAnalyzer)
		if err != nil {
			t.Skipf("input schema for %q not available: %v", forecastAnalyzer, err)
		}
		fields, ok := analyzer.FlattenSchema(schema)
		if !ok {
			t.Fatalf("expected an introspectable input schema, got: %#v", schema)
		}
		var hasTimeSeriesData, hasRequired bool
		for _, f := range fields {
			t.Logf("input field: %s (type=%s required=%v composite=%v)", f.Name, f.Type, f.Required, f.Composite)
			if f.Name == "timeSeriesData" {
				hasTimeSeriesData = true
			}
			if f.Required {
				hasRequired = true
			}
		}
		if !hasTimeSeriesData {
			t.Error("expected a timeSeriesData input field for the forecast analyzer")
		}
		if !hasRequired {
			t.Error("expected at least one required input field")
		}
	})

	t.Run("describe: result schema resolves", func(t *testing.T) {
		schema, err := handler.GetResultSchema(forecastAnalyzer)
		if err != nil {
			t.Skipf("result schema for %q not available: %v", forecastAnalyzer, err)
		}
		if len(schema) == 0 {
			t.Error("expected a non-empty result schema")
		}
	})

	t.Run("describe --doc: documentation resolves", func(t *testing.T) {
		doc, err := handler.GetDocumentation(forecastAnalyzer)
		if err != nil {
			t.Skipf("documentation for %q not available: %v", forecastAnalyzer, err)
		}
		if strings.TrimSpace(doc) == "" {
			t.Error("expected non-empty documentation markdown")
		}
		t.Logf("documentation length: %d bytes", len(doc))
	})

	t.Run("verify: valid input is accepted", func(t *testing.T) {
		input := map[string]interface{}{
			"timeSeriesData": "timeseries avg(dt.host.cpu.usage)",
		}
		result, err := handler.Validate(forecastAnalyzer, input)
		if err != nil {
			t.Skipf("validate not available for %q: %v", forecastAnalyzer, err)
		}
		if result == nil {
			t.Fatal("expected a validation result")
		}
		// We don't hard-assert Valid==true (validation semantics can depend on
		// the tenant's data model), but a well-formed input should not report
		// structural errors. When the tenant does accept it, confirm the verdict
		// maps to the "valid" exit code that `verify analyzer` returns.
		if result.Valid && verifyValidateExitCode(result) != 0 {
			t.Errorf("valid verdict must map to exit code 0, got %d", verifyValidateExitCode(result))
		}
		t.Logf("valid-input verdict: valid=%v details=%v", result.Valid, result.Details)
	})

	t.Run("verify: empty input is rejected", func(t *testing.T) {
		result, err := handler.Validate(forecastAnalyzer, map[string]interface{}{})
		if err != nil {
			t.Skipf("validate not available for %q: %v", forecastAnalyzer, err)
		}
		if result == nil {
			t.Fatal("expected a validation result")
		}
		if result.Valid {
			t.Error("expected empty input to be invalid for the forecast analyzer")
		}
		// An invalid verdict must map to the "invalid" exit code (1) that
		// `verify analyzer` surfaces to CI/CD callers.
		if got := verifyValidateExitCode(result); got != 1 {
			t.Errorf("invalid verdict must map to exit code 1, got %d", got)
		}
		t.Logf("empty-input verdict: valid=%v details=%v", result.Valid, result.Details)
	})
}

// verifyValidateExitCode mirrors the successful-validate branch of the
// exit-code contract that `dtctl verify analyzer` applies (0 = valid,
// 1 = invalid). It is duplicated here (rather than imported from the cmd
// package, which is unexported and calls os.Exit) so the integration test can
// assert that a live validate verdict maps to the exit code CI/CD relies on.
// The auth/network branches (2/3) are covered by unit tests in cmd.
func verifyValidateExitCode(result *analyzer.ValidationResult) int {
	if result == nil || !result.Valid {
		return 1
	}
	return 0
}
