package platform

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Dynatrace/dtctl/pkg/client"
)

func newTestHandler(t *testing.T, mux *http.ServeMux) *Handler {
	t.Helper()
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	c, err := client.NewForTesting(srv.URL, "dt0c01.test")
	if err != nil {
		t.Fatalf("client.NewForTesting: %v", err)
	}
	return NewHandler(c)
}

func TestGetEnvironment(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/platform/management/v1/environment", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"environmentId": "abc12345",
			"type":          "INTERNAL",
			"state":         "ACTIVE",
			"createTime":    "2025-03-13T13:11:04Z",
			"blockTime":     "2031-01-01T00:00:00Z",
		})
	})

	h := newTestHandler(t, mux)
	info, err := h.GetEnvironment()
	if err != nil {
		t.Fatalf("GetEnvironment() error: %v", err)
	}
	if info.EnvironmentID != "abc12345" {
		t.Errorf("EnvironmentID = %q, want %q", info.EnvironmentID, "abc12345")
	}
	if info.Type != "INTERNAL" {
		t.Errorf("Type = %q, want %q", info.Type, "INTERNAL")
	}
	if info.State != "ACTIVE" {
		t.Errorf("State = %q, want %q", info.State, "ACTIVE")
	}
}

func TestGetLicenseSettings(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/platform/management/v1/environment/license/settings", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"settings": []map[string]string{
				{"key": "AUTOMATION", "value": "true"},
				{"key": "AI_FUNCTIONS", "value": "true"},
				{"key": "LIVE_DEBUGGING", "value": "false"},
			},
		})
	})

	h := newTestHandler(t, mux)
	settings, err := h.GetLicenseSettings()
	if err != nil {
		t.Fatalf("GetLicenseSettings() error: %v", err)
	}
	if len(settings) != 3 {
		t.Errorf("len(settings) = %d, want 3", len(settings))
	}
	if settings[0].Key != "AUTOMATION" {
		t.Errorf("settings[0].Key = %q, want %q", settings[0].Key, "AUTOMATION")
	}
	if settings[0].Value != "true" {
		t.Errorf("settings[0].Value = %q, want %q", settings[0].Value, "true")
	}
}

func TestGetLicenseSettings_WithKey(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/platform/management/v1/environment/license/settings", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		keys := r.URL.Query()["keys"]
		if len(keys) != 1 || keys[0] != "AUTOMATION" {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"settings": []map[string]string{
				{"key": "AUTOMATION", "value": "true"},
			},
		})
	})

	h := newTestHandler(t, mux)
	settings, err := h.GetLicenseSettings("AUTOMATION")
	if err != nil {
		t.Fatalf("GetLicenseSettings() error: %v", err)
	}
	if len(settings) != 1 {
		t.Errorf("len(settings) = %d, want 1", len(settings))
	}
	if settings[0].Key != "AUTOMATION" {
		t.Errorf("settings[0].Key = %q, want %q", settings[0].Key, "AUTOMATION")
	}
}

func TestGetLicense(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/platform/management/v1/environment/license", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"trial":                false,
			"platformSubscription": true,
		})
	})

	h := newTestHandler(t, mux)
	lic, err := h.GetLicense()
	if err != nil {
		t.Fatalf("GetLicense() error: %v", err)
	}
	if lic.Trial {
		t.Errorf("Trial = true, want false")
	}
	if !lic.PlatformSubscription {
		t.Errorf("PlatformSubscription = false, want true")
	}
}
