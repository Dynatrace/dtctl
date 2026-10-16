package cmd

import (
	"context"
	"strings"
	"testing"

	"github.com/dynatrace-oss/dtctl/pkg/output"
	"github.com/dynatrace-oss/dtctl/pkg/resources/analyzer"
)

func TestUseAnalyzerDescribeTextView(t *testing.T) {
	origAgent := agentMode(context.Background())
	origFormat := outputFormat(context.Background())
	t.Cleanup(func() {
		gFlags.agentMode = origAgent
		gFlags.outputFormat = origFormat
	})

	cases := []struct {
		name   string
		agent  bool
		format string
		want   bool
	}{
		{"default table, non-agent", false, "table", true},
		{"empty format, non-agent", false, "", true},
		{"json, non-agent", false, "json", false},
		{"yaml, non-agent", false, "yaml", false},
		// Agent mode must never take the human path, even with the default
		// "table" format that agent mode leaves in place.
		{"table, agent mode", true, "table", false},
		{"empty format, agent mode", true, "", false},
		{"json, agent mode", true, "json", false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gFlags.agentMode = tc.agent
			gFlags.outputFormat = tc.format
			if got := useAnalyzerDescribeTextView(context.Background()); got != tc.want {
				t.Errorf("useAnalyzerDescribeTextView() = %v, want %v (agent=%v format=%q)",
					got, tc.want, tc.agent, tc.format)
			}
		})
	}
}

func TestPrintAnalyzerDescribe_FullSchema(t *testing.T) {
	output.SetPlainMode(true)
	output.ResetColorCache()
	t.Cleanup(output.ResetColorCache)

	desc := &analyzer.AnalyzerDescription{
		Name:        "dt.statistics.GenericForecastAnalyzer",
		DisplayName: "Generic Forecast Analyzer",
		Category:    "Forecast",
		Type:        "DAVIS",
		Labels:      []string{"forecast", "timeseries"},
		InputSchema: map[string]interface{}{
			"type":     "object",
			"required": []interface{}{"timeSeriesData"},
			"properties": map[string]interface{}{
				"timeSeriesData":  map[string]interface{}{"type": "string", "description": "DQL query"},
				"forecastHorizon": map[string]interface{}{"type": "integer", "description": "points"},
			},
		},
		ResultSchema: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"forecastValues": map[string]interface{}{"type": "array"},
			},
		},
	}

	out := captureStdout(t, func() { printAnalyzerDescribe(context.Background(), desc) })

	for _, want := range []string{
		"Generic Forecast Analyzer",
		"Input (required):",
		"timeSeriesData",
		"Input (optional):",
		"forecastHorizon",
		"Output:",
		"forecastValues",
		"Run it:  dtctl exec analyzer dt.statistics.GenericForecastAnalyzer --query <dql>",
		"Docs:    dtctl describe analyzer dt.statistics.GenericForecastAnalyzer --doc",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q\n---\n%s", want, out)
		}
	}

	// Required field must render before the optional group.
	if strings.Index(out, "timeSeriesData") > strings.Index(out, "forecastHorizon") {
		t.Error("expected required timeSeriesData to render before optional forecastHorizon")
	}
}

func TestPrintAnalyzerDescribe_SchemaUnavailable(t *testing.T) {
	output.SetPlainMode(true)
	output.ResetColorCache()
	t.Cleanup(output.ResetColorCache)

	// Both schema calls failed (best-effort) → nil schemas. Describe must still
	// succeed and print the legible fallback marker rather than empty sections.
	desc := &analyzer.AnalyzerDescription{
		Name:        "dt.statistics.SomeAnalyzer",
		DisplayName: "Some Analyzer",
	}

	out := captureStdout(t, func() { printAnalyzerDescribe(context.Background(), desc) })

	if !strings.Contains(out, "(schema not introspectable — use -o json or --doc)") {
		t.Errorf("expected fallback marker for unavailable schema\n---\n%s", out)
	}
	if !strings.Contains(out, "Some Analyzer") {
		t.Errorf("expected metadata to still render\n---\n%s", out)
	}
}

func TestPrintSchemaFields_CompositeMarker(t *testing.T) {
	output.SetPlainMode(true)
	output.ResetColorCache()
	t.Cleanup(output.ResetColorCache)

	schema := map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"target": map[string]interface{}{
				"oneOf": []interface{}{
					map[string]interface{}{"type": "string"},
					map[string]interface{}{"type": "integer"},
				},
			},
		},
	}

	out := captureStdout(t, func() { printSchemaSection(context.Background(), "Output", schema) })

	if !strings.Contains(out, "(composite — see -o json or --doc)") {
		t.Errorf("expected composite marker\n---\n%s", out)
	}
}
