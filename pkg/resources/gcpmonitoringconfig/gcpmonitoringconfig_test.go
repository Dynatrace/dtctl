package gcpmonitoringconfig

import (
	"encoding/json"
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

func TestGetListFindAndSchemaHelpers(t *testing.T) {
	calls := 0
	h, server := newMonitoringHandler(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodGet && r.URL.Path == ExtensionAPI {
			_ = json.NewEncoder(w).Encode(ExtensionResponse{Items: []ExtensionItem{{Version: "1.0.1"}}})
			return
		}
		if r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/schema") {
			_ = json.NewEncoder(w).Encode(ExtensionSchemaResponse{Enums: map[string]SchemaEnum{
				"dynatrace.datasource.gcp:location": {Items: []SchemaEnumItem{{Value: "us-central1"}}},
				"FeatureSetsType":                   {Items: []SchemaEnumItem{{Value: "compute_engine_essential"}, {Value: "foo_autodiscovery"}}},
			}})
			return
		}
		if r.Method == http.MethodGet && r.URL.Path == BaseAPI {
			calls++
			_ = json.NewEncoder(w).Encode(ListResponse{Items: []GCPMonitoringConfig{{ObjectID: "cfg-1", Value: Value{Description: "cfg", Enabled: true, Version: "1.0.1"}}}})
			return
		}
		if r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, BaseAPI+"/") {
			_ = json.NewEncoder(w).Encode(GCPMonitoringConfig{ObjectID: "cfg-1", Value: Value{Description: "cfg", Enabled: true, Version: "1.0.1"}})
			return
		}
		w.WriteHeader(http.StatusNotFound)
	})
	defer server.Close()

	item, err := h.Get("cfg-1")
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	if item.Description != "cfg" {
		t.Fatalf("unexpected item: %+v", item)
	}

	items, err := h.List()
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("unexpected list length: %d", len(items))
	}

	_, err = h.FindByName("cfg")
	if err != nil {
		t.Fatalf("FindByName() error = %v", err)
	}

	locs, err := h.ListAvailableLocations()
	if err != nil || len(locs) != 1 {
		t.Fatalf("ListAvailableLocations() = %#v, err=%v", locs, err)
	}

	sets, err := h.ListAvailableFeatureSets()
	if err != nil || len(sets) != 2 {
		t.Fatalf("ListAvailableFeatureSets() = %#v, err=%v", sets, err)
	}
}

func TestCreateUpdateDeleteAndErrors(t *testing.T) {
	h, server := newMonitoringHandler(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.Method {
		case http.MethodPost:
			_ = json.NewEncoder(w).Encode(GCPMonitoringConfig{ObjectID: "cfg-1", Value: Value{Description: "created"}})
		case http.MethodPut:
			_ = json.NewEncoder(w).Encode(GCPMonitoringConfig{ObjectID: "cfg-1", Value: Value{Description: "updated"}})
		case http.MethodDelete:
			w.WriteHeader(http.StatusNoContent)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	})
	defer server.Close()

	created, err := h.Create([]byte(`{"value":{"description":"created"}}`))
	if err != nil || created.ObjectID != "cfg-1" {
		t.Fatalf("Create() err=%v created=%+v", err, created)
	}

	updated, err := h.Update("cfg-1", []byte(`{"value":{"description":"updated"}}`))
	if err != nil || updated.ObjectID != "cfg-1" {
		t.Fatalf("Update() err=%v updated=%+v", err, updated)
	}

	if err := h.Delete("cfg-1"); err != nil {
		t.Fatalf("Delete() err=%v", err)
	}

	hErr, serverErr := newMonitoringHandler(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte("boom"))
	})
	defer serverErr.Close()

	if _, err := hErr.Get("cfg"); err == nil || !strings.Contains(err.Error(), "failed to get gcp_monitoring_config") {
		t.Fatalf("unexpected get error: %v", err)
	}
	if _, err := hErr.List(); err == nil || !strings.Contains(err.Error(), "failed to list gcp_monitoring_configs") {
		t.Fatalf("unexpected list error: %v", err)
	}
	if _, err := hErr.Create([]byte(`{}`)); err == nil || !strings.Contains(err.Error(), "failed to create gcp_monitoring_config") {
		t.Fatalf("unexpected create error: %v", err)
	}
	if _, err := hErr.Update("cfg", []byte(`{}`)); err == nil || !strings.Contains(err.Error(), "failed to update gcp_monitoring_config") {
		t.Fatalf("unexpected update error: %v", err)
	}
	if err := hErr.Delete("cfg"); err == nil || !strings.Contains(err.Error(), "failed to delete gcp_monitoring_config") {
		t.Fatalf("unexpected delete error: %v", err)
	}
}

// TestListPaginationStitchesPages verifies the Extensions 2.0 pagination loop.
func TestListPaginationStitchesPages(t *testing.T) {
	pages := []ListResponse{
		{Items: []GCPMonitoringConfig{{ObjectID: "1", Value: Value{Description: "a", Enabled: true, Version: "1.0.0"}}}, NextPageKey: "k2"},
		{Items: []GCPMonitoringConfig{{ObjectID: "2", Value: Value{Description: "b", Enabled: false, Version: "1.0.0"}}}, NextPageKey: "k3"},
		{Items: []GCPMonitoringConfig{{ObjectID: "3", Value: Value{Description: "c", Enabled: true, Version: "1.0.0"}}}},
	}
	calls := 0
	h, server := newMonitoringHandler(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("next-page-key") != "" && r.URL.Query().Get("page-size") != "" {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":{"code":400,"message":"Constraints violated."}}`))
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
	if calls != 3 || len(items) != 3 {
		t.Fatalf("got %d calls / %d items, want 3/3", calls, len(items))
	}
}

func TestCentralEnrichmentAndDtLabelsJSON(t *testing.T) {
	central := true
	config := GoogleCloudConfig{
		UseIngestEnrichmentConfig: &central,
		DtLabelsEnrichment: map[string]DtLabelMapping{
			"dt.cost.product": {LabelKey: "product"},
		},
	}

	encoded, err := json.Marshal(config)
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}
	var decoded GoogleCloudConfig
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}
	if decoded.UseIngestEnrichmentConfig == nil || !*decoded.UseIngestEnrichmentConfig {
		t.Fatalf("central mode did not survive round trip: %s", encoded)
	}
	mapping, ok := decoded.DtLabelsEnrichment["dt.cost.product"]
	if !ok || mapping.LabelKey != "product" {
		t.Fatalf("dtLabelsEnrichment did not survive round trip: %#v", decoded.DtLabelsEnrichment)
	}
}

// Both enrichment properties are nullable and modificationPolicy NEVER in the
// extension schema: an update must echo the stored value back unchanged. That
// only works if an explicit false survives the round trip instead of being
// elided like a zero value, which is why these are *bool and not bool.
func TestEnrichmentNeverFieldsRoundTripExplicitFalse(t *testing.T) {
	no := false
	encoded, err := json.Marshal(GoogleCloudConfig{
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

	var decoded GoogleCloudConfig
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
