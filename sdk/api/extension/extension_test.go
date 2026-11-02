package extension

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Dynatrace/dtctl/sdk/httpclient"
)

func newTestClient(t *testing.T, handler http.Handler) *httpclient.Client {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	c, err := httpclient.New(srv.URL, httpclient.WithToken("dt0c01.test"))
	if err != nil {
		t.Fatalf("httpclient.New: %v", err)
	}
	return c
}

func TestList(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/platform/extensions/v2/extensions", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}

		// Simulate API constraint: page-size must not be combined with next-page-key
		if r.URL.Query().Get("page-size") != "" && r.URL.Query().Get("next-page-key") != "" {
			w.WriteHeader(http.StatusBadRequest)
			fmt.Fprintf(w, `{"error":{"code":400,"message":"Constraints violated."}}`)
			return
		}

		resp := ExtensionList{
			Items: []Extension{
				{ExtensionName: "com.dynatrace.extension.host", Version: "1.0.0"},
				{ExtensionName: "com.dynatrace.extension.jmx", Version: "2.0.0"},
			},
			TotalCount: 2,
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	})

	h := NewHandler(newTestClient(t, mux))
	result, err := h.List(context.Background(), "", 0)
	if err != nil {
		t.Fatalf("List() error: %v", err)
	}
	if len(result.Items) != 2 {
		t.Errorf("got %d extensions, want 2", len(result.Items))
	}
	if result.Items[0].ExtensionName != "com.dynatrace.extension.host" {
		t.Errorf("ExtensionName = %q, want %q", result.Items[0].ExtensionName, "com.dynatrace.extension.host")
	}
}

func TestGet(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/platform/extensions/v2/extensions/com.dynatrace.extension.host", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		resp := ExtensionVersionList{
			Items: []ExtensionVersion{
				{Version: "1.0.0", ExtensionName: "com.dynatrace.extension.host"},
				{Version: "0.9.0", ExtensionName: "com.dynatrace.extension.host"},
			},
			TotalCount: 2,
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	})
	// Environment configuration endpoint returns the active version.
	mux.HandleFunc("/platform/extensions/v2/extensions/com.dynatrace.extension.host/environment-configuration", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"version":"1.0.0"}`)
	})

	h := NewHandler(newTestClient(t, mux))
	result, err := h.Get(context.Background(), "com.dynatrace.extension.host")
	if err != nil {
		t.Fatalf("Get() error: %v", err)
	}
	if len(result.Items) != 2 {
		t.Errorf("got %d versions, want 2", len(result.Items))
	}
	if !result.Items[0].Active {
		t.Error("expected first version to be active")
	}
	if result.Items[1].Active {
		t.Error("expected second version to not be active")
	}
}

func TestGet_NoActiveVersion(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/platform/extensions/v2/extensions/com.dynatrace.extension.host", func(w http.ResponseWriter, r *http.Request) {
		resp := ExtensionVersionList{
			Items: []ExtensionVersion{
				{Version: "1.0.0", ExtensionName: "com.dynatrace.extension.host"},
			},
			TotalCount: 1,
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	})
	// Environment configuration returns 404 (no active version configured).
	mux.HandleFunc("/platform/extensions/v2/extensions/com.dynatrace.extension.host/environment-configuration", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		fmt.Fprintf(w, `{"error":{"code":404,"message":"not found"}}`)
	})

	h := NewHandler(newTestClient(t, mux))
	result, err := h.Get(context.Background(), "com.dynatrace.extension.host")
	if err != nil {
		t.Fatalf("Get() error: %v", err)
	}
	if len(result.Items) != 1 {
		t.Errorf("got %d versions, want 1", len(result.Items))
	}
	// No active version should be set when environment config returns 404.
	if result.Items[0].Active {
		t.Error("expected no active version when environment config is missing")
	}
}

func TestGet_ActiveVersionError(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/platform/extensions/v2/extensions/com.dynatrace.extension.host", func(w http.ResponseWriter, r *http.Request) {
		resp := ExtensionVersionList{
			Items:      []ExtensionVersion{{Version: "1.0.0", ExtensionName: "com.dynatrace.extension.host"}},
			TotalCount: 1,
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	})
	// Environment configuration returns 403. The version list was already
	// fetched successfully, so the enrichment failure must degrade gracefully:
	// Get() returns the versions with no Active flag set, not an error.
	mux.HandleFunc("/platform/extensions/v2/extensions/com.dynatrace.extension.host/environment-configuration", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		fmt.Fprintf(w, `{"error":{"code":403,"message":"access denied"}}`)
	})

	h := NewHandler(newTestClient(t, mux))
	result, err := h.Get(context.Background(), "com.dynatrace.extension.host")
	if err != nil {
		t.Fatalf("Get() must not fail when env-config lookup returns 403, got error: %v", err)
	}
	if len(result.Items) != 1 {
		t.Fatalf("got %d versions, want 1", len(result.Items))
	}
	if result.Items[0].Active {
		t.Error("expected no active version when env-config lookup fails with 403")
	}
}

func TestGetVersion(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/platform/extensions/v2/extensions/com.dynatrace.extension.host/1.0.0", func(w http.ResponseWriter, r *http.Request) {
		resp := ExtensionDetails{
			ExtensionName: "com.dynatrace.extension.host",
			Version:       "1.0.0",
			Author:        ExtensionAuthor{Name: "Dynatrace"},
			DataSources:   []string{"snmp"},
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	})

	h := NewHandler(newTestClient(t, mux))
	result, err := h.GetVersion(context.Background(), "com.dynatrace.extension.host", "1.0.0")
	if err != nil {
		t.Fatalf("GetVersion() error: %v", err)
	}
	if result.ExtensionName != "com.dynatrace.extension.host" {
		t.Errorf("ExtensionName = %q, want %q", result.ExtensionName, "com.dynatrace.extension.host")
	}
	if result.Author.Name != "Dynatrace" {
		t.Errorf("Author.Name = %q, want %q", result.Author.Name, "Dynatrace")
	}
}

// TestGetVersion_FeatureSetsDetails guards the wire contract for feature-set
// metrics. It serves a raw payload using the *exact* field names the Extensions
// 2.0 API returns (featureSetsDetails, isRecommended, metadata.displayName, ...)
// so that a regression in any json tag — e.g. reverting featureSetsDetails back
// to featureSetDetails — fails here instead of silently producing empty metrics.
func TestGetVersion_FeatureSetsDetails(t *testing.T) {
	const raw = `{
		"extensionName": "com.example.test-extension",
		"version": "1.2.3",
		"featureSets": ["default", "advanced"],
		"featureSetsDetails": {
			"default": {
				"isRecommended": true,
				"metrics": [
					{
						"key": "ext.uptime",
						"metadata": {
							"displayName": "Instance uptime",
							"description": "Time since instance started",
							"unit": "Second"
						}
					},
					{"key": "ext.bare"}
				]
			},
			"advanced": {"metrics": []}
		}
	}`

	mux := http.NewServeMux()
	mux.HandleFunc("/platform/extensions/v2/extensions/com.example.test-extension/1.2.3", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(raw))
	})

	h := NewHandler(newTestClient(t, mux))
	result, err := h.GetVersion(context.Background(), "com.example.test-extension", "1.2.3")
	if err != nil {
		t.Fatalf("GetVersion() error: %v", err)
	}

	detail, ok := result.FeatureSetDetails["default"]
	if !ok {
		t.Fatalf("FeatureSetDetails missing \"default\"; the featureSetsDetails json tag likely does not match the API. Got keys: %v", keysOf(result.FeatureSetDetails))
	}
	if !detail.IsRecommended {
		t.Errorf("IsRecommended = false, want true (isRecommended tag mismatch)")
	}
	if len(detail.Metrics) != 2 {
		t.Fatalf("got %d metrics, want 2", len(detail.Metrics))
	}

	// Metric with full metadata.
	m0 := detail.Metrics[0]
	if m0.Key != "ext.uptime" {
		t.Errorf("Metrics[0].Key = %q, want %q", m0.Key, "ext.uptime")
	}
	if m0.Metadata == nil {
		t.Fatalf("Metrics[0].Metadata = nil, want populated metadata")
	}
	if m0.Metadata.DisplayName != "Instance uptime" || m0.Metadata.Unit != "Second" {
		t.Errorf("Metrics[0].Metadata = %+v, want displayName=%q unit=%q", *m0.Metadata, "Instance uptime", "Second")
	}

	// Metric without metadata: the pointer must stay nil so omitempty can drop it.
	if m1 := detail.Metrics[1]; m1.Metadata != nil {
		t.Errorf("Metrics[1].Metadata = %+v, want nil for a metric with no metadata", *m1.Metadata)
	}
}

func keysOf(m map[string]FeatureSetDetail) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	return keys
}

func TestDeleteMonitoringConfiguration(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/platform/extensions/v2/extensions/com.dynatrace.extension.host/monitoring-configurations/config-1", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodDelete {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})

	h := NewHandler(newTestClient(t, mux))
	err := h.DeleteMonitoringConfiguration(context.Background(), "com.dynatrace.extension.host", "config-1")
	if err != nil {
		t.Fatalf("DeleteMonitoringConfiguration() error: %v", err)
	}
}

func TestActivateVersion(t *testing.T) {
	const extName = "com.dynatrace.extension.host"
	const version = "1.2.3"

	mux := http.NewServeMux()
	mux.HandleFunc("/platform/extensions/v2/extensions/"+extName+"/environment-configuration",
		func(w http.ResponseWriter, r *http.Request) {
			switch r.Method {
			case http.MethodPut:
				var body map[string]string
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					w.WriteHeader(http.StatusBadRequest)
					return
				}
				if body["version"] != version {
					w.WriteHeader(http.StatusBadRequest)
					fmt.Fprintf(w, `{"error":{"code":400,"message":"unexpected version %q"}}`, body["version"])
					return
				}
				w.Header().Set("Content-Type", "application/json")
				json.NewEncoder(w).Encode(ExtensionEnvironmentConfig{Version: version})
			default:
				w.WriteHeader(http.StatusMethodNotAllowed)
			}
		})

	h := NewHandler(newTestClient(t, mux))
	result, err := h.ActivateVersion(context.Background(), extName, version)
	if err != nil {
		t.Fatalf("ActivateVersion() error: %v", err)
	}
	if result.Version != version {
		t.Errorf("Version = %q, want %q", result.Version, version)
	}
}

func TestActivateVersion_NotFound(t *testing.T) {
	const extName = "com.dynatrace.extension.missing"
	mux := http.NewServeMux()
	mux.HandleFunc("/platform/extensions/v2/extensions/"+extName+"/environment-configuration",
		func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusNotFound)
			fmt.Fprintf(w, `{"error":{"code":404,"message":"Extension not found"}}`)
		})

	h := NewHandler(newTestClient(t, mux))
	_, err := h.ActivateVersion(context.Background(), extName, "1.0.0")
	if err == nil {
		t.Fatal("ActivateVersion() expected error for 404, got nil")
	}
}

func TestActivateVersion_Forbidden(t *testing.T) {
	const extName = "com.dynatrace.extension.restricted"
	mux := http.NewServeMux()
	mux.HandleFunc("/platform/extensions/v2/extensions/"+extName+"/environment-configuration",
		func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusForbidden)
			fmt.Fprintf(w, `{"error":{"code":403,"message":"Forbidden"}}`)
		})

	h := NewHandler(newTestClient(t, mux))
	_, err := h.ActivateVersion(context.Background(), extName, "1.0.0")
	if err == nil {
		t.Fatal("ActivateVersion() expected error for 403, got nil")
	}
}
