//go:build integration
// +build integration

package integration

import (
	"context"
	"testing"

	sdkquery "github.com/Dynatrace/dtctl/sdk/api/query"
	"github.com/Dynatrace/dtctl/sdk/httpclient"
)

// TestQueryMetricMetadataEnrichment verifies that requesting metric-metadata
// enrichment (the `enrich=metric-metadata` query-string parameter on
// query:execute / query:poll) causes the DQL API to populate the catalogue
// fields displayName / description / unit on metadata.metrics[], and that
// omitting it leaves those fields empty.
//
// This is the end-to-end guard for the bug where dtctl never requested
// enrichment, so units/displayName/description were always missing.
func TestQueryMetricMetadataEnrichment(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test")
	}

	env := SetupIntegration(t)
	handler := sdkquery.NewHandler(httpclient.Wrap(env.Client.HTTP()))

	// A built-in host metric that carries catalogue metadata on any Grail tenant
	// that has ingested host data. Matches the metric used by other e2e tests.
	const query = "timeseries avg(dt.host.cpu.usage), from: -2h"

	// Enriched request: must return at least one metric with catalogue fields.
	enriched, err := handler.ExecuteAndPoll(
		context.Background(),
		sdkquery.ExecuteRequest{Query: query, EnrichMetricMetadata: true},
		nil,
	)
	if err != nil {
		t.Fatalf("enriched query failed: %v", err)
	}

	metrics := enriched.GetMetrics()
	if len(metrics) == 0 {
		t.Skipf("tenant returned no metrics metadata for %q (metric likely absent); cannot verify enrichment", query)
	}

	var m sdkquery.MetricInfo
	found := false
	for _, mi := range metrics {
		if mi.MetricKey == "dt.host.cpu.usage" {
			m = mi
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected a metrics entry for dt.host.cpu.usage, got %+v", metrics)
	}

	t.Logf("enriched metric: key=%q displayName=%q unit=%q description=%q",
		m.MetricKey, m.DisplayName, m.Unit, m.Description)

	// Enrichment must populate the catalogue fields. Unit and DisplayName are
	// reliably set for built-in metrics; Description may be empty for some, so
	// require the two stable ones and log the third.
	if m.Unit == "" {
		t.Errorf("expected non-empty unit for dt.host.cpu.usage when enrichment is requested")
	}
	if m.DisplayName == "" {
		t.Errorf("expected non-empty displayName for dt.host.cpu.usage when enrichment is requested")
	}

	// Contrast request: without enrichment the catalogue fields must be absent,
	// proving it is the enrich parameter — not the tenant — that drives it.
	plain, err := handler.ExecuteAndPoll(
		context.Background(),
		sdkquery.ExecuteRequest{Query: query, EnrichMetricMetadata: false},
		nil,
	)
	if err != nil {
		t.Fatalf("plain query failed: %v", err)
	}
	for _, mi := range plain.GetMetrics() {
		if mi.MetricKey != "dt.host.cpu.usage" {
			continue
		}
		if mi.DisplayName != "" || mi.Description != "" || mi.Unit != "" {
			t.Errorf("expected no catalogue fields without enrichment, got displayName=%q description=%q unit=%q",
				mi.DisplayName, mi.Description, mi.Unit)
		}
	}
}
