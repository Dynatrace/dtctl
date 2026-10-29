package awsmonitoringconfig

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Dynatrace/dtctl/pkg/client"
)

func newMonitoringHandler(t *testing.T, fn http.HandlerFunc) (*Handler, *httptest.Server) {
	t.Helper()
	server := httptest.NewServer(fn)
	c, err := client.NewForTesting(server.URL, "test-token")
	if err != nil {
		server.Close()
		t.Fatalf("client.New() error = %v", err)
	}
	c.HTTP().SetRetryCount(0)
	return NewHandler(c), server
}

// TestListPaginationStitchesPages verifies List follows next-page-key across
// pages and that subsequent requests do not combine page-size with
// next-page-key (Extensions 2.0 API constraint per AGENTS.md).
func TestListPaginationStitchesPages(t *testing.T) {
	pages := []ListResponse{
		{Items: []AWSMonitoringConfig{{ObjectID: "1", Value: Value{Description: "a", Enabled: true, Version: "1.0.0"}}}, NextPageKey: "k2"},
		{Items: []AWSMonitoringConfig{{ObjectID: "2", Value: Value{Description: "b", Enabled: false, Version: "1.0.0"}}}, NextPageKey: "k3"},
		{Items: []AWSMonitoringConfig{{ObjectID: "3", Value: Value{Description: "c", Enabled: true, Version: "1.0.0"}}}},
	}
	calls := 0
	h, server := newMonitoringHandler(t, func(w http.ResponseWriter, r *http.Request) {
		// Constraint guard: page-size must not be combined with next-page-key.
		if r.URL.Query().Get("next-page-key") != "" && r.URL.Query().Get("page-size") != "" {
			w.WriteHeader(http.StatusBadRequest)
			fmt.Fprint(w, `{"error":{"code":400,"message":"Constraints violated."}}`)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(pages[calls])
		calls++
	})
	defer server.Close()

	items, err := h.List()
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if calls != 3 {
		t.Fatalf("expected 3 page requests, got %d", calls)
	}
	if len(items) != 3 {
		t.Fatalf("expected 3 items, got %d (%+v)", len(items), items)
	}
	for i, want := range []string{"a", "b", "c"} {
		if items[i].Description != want {
			t.Errorf("item[%d].Description = %q, want %q", i, items[i].Description, want)
		}
	}
}

func TestCentralEnrichmentModeJSON(t *testing.T) {
	for _, test := range []struct {
		name    string
		input   string
		present bool
		value   bool
	}{
		{name: "omitted", input: `{}`},
		{name: "legacy", input: `{"useIngestEnrichmentConfig":false}`, present: true},
		{name: "central", input: `{"useIngestEnrichmentConfig":true}`, present: true, value: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			var config AWSConfig
			if err := json.Unmarshal([]byte(test.input), &config); err != nil {
				t.Fatalf("json.Unmarshal() error = %v", err)
			}
			if (config.UseIngestEnrichmentConfig != nil) != test.present {
				t.Fatalf("mode presence = %v, want %v", config.UseIngestEnrichmentConfig != nil, test.present)
			}
			if test.present && *config.UseIngestEnrichmentConfig != test.value {
				t.Fatalf("mode = %v, want %v", *config.UseIngestEnrichmentConfig, test.value)
			}
			encoded, err := json.Marshal(config)
			if err != nil {
				t.Fatalf("json.Marshal() error = %v", err)
			}
			var fields map[string]any
			if err := json.Unmarshal(encoded, &fields); err != nil {
				t.Fatalf("json.Unmarshal(encoded) error = %v", err)
			}
			_, present := fields["useIngestEnrichmentConfig"]
			if present != test.present {
				t.Errorf("encoded mode presence = %v, want %v", present, test.present)
			}
		})
	}
}

// Both enrichment properties are nullable and modificationPolicy NEVER in the
// extension schema: an update must echo the stored value back unchanged. That
// only works if an explicit false survives the round trip instead of being
// elided like a zero value, which is why these are *bool and not bool.
func TestEnrichmentNeverFieldsRoundTripExplicitFalse(t *testing.T) {
	no := false
	encoded, err := json.Marshal(AWSConfig{
		UseIngestEnrichmentConfig:          &no,
		IngestEnrichmentMigrationProcessed: &no,
	})
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}
	for _, want := range []string{`"useIngestEnrichmentConfig":false`, `"ingestEnrichmentMigrationProcessed":false`} {
		if !strings.Contains(string(encoded), want) {
			t.Errorf("encoded config %s does not contain %s", encoded, want)
		}
	}

	var decoded AWSConfig
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}
	if decoded.UseIngestEnrichmentConfig == nil || *decoded.UseIngestEnrichmentConfig {
		t.Errorf("useIngestEnrichmentConfig = %v, want false", decoded.UseIngestEnrichmentConfig)
	}
	if decoded.IngestEnrichmentMigrationProcessed == nil || *decoded.IngestEnrichmentMigrationProcessed {
		t.Errorf("ingestEnrichmentMigrationProcessed = %v, want false", decoded.IngestEnrichmentMigrationProcessed)
	}
}
