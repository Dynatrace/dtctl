package platform

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

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

func TestGetEnvironment(t *testing.T) {
	createTime := time.Date(2025, 3, 13, 13, 11, 4, 0, time.UTC)
	blockTime := time.Date(2031, 1, 1, 0, 0, 0, 0, time.UTC)
	mux := http.NewServeMux()
	mux.HandleFunc("/platform/management/v1/environment", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		resp := EnvironmentInfo{
			EnvironmentID: "abc12345",
			Type:          "INTERNAL",
			State:         "ACTIVE",
			CreateTime:    createTime,
			BlockTime:     blockTime,
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	})

	h := NewHandler(newTestClient(t, mux))
	result, err := h.GetEnvironment(context.Background())
	if err != nil {
		t.Fatalf("GetEnvironment() error: %v", err)
	}
	if result.EnvironmentID != "abc12345" {
		t.Errorf("EnvironmentID = %q, want %q", result.EnvironmentID, "abc12345")
	}
	if result.Type != "INTERNAL" {
		t.Errorf("Type = %q, want %q", result.Type, "INTERNAL")
	}
	if result.State != "ACTIVE" {
		t.Errorf("State = %q, want %q", result.State, "ACTIVE")
	}
}

func TestGetEnvironment_Error(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/platform/management/v1/environment", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		fmt.Fprintf(w, `{"error":{"message":"missing scope"}}`)
	})

	h := NewHandler(newTestClient(t, mux))
	_, err := h.GetEnvironment(context.Background())
	if err == nil {
		t.Fatal("GetEnvironment() expected error for 403")
	}
}

func TestGetLicense(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/platform/management/v1/environment/license", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		resp := License{
			Trial:                false,
			PlatformSubscription: true,
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	})

	h := NewHandler(newTestClient(t, mux))
	result, err := h.GetLicense(context.Background())
	if err != nil {
		t.Fatalf("GetLicense() error: %v", err)
	}
	if result.Trial {
		t.Errorf("Trial = true, want false")
	}
	if !result.PlatformSubscription {
		t.Errorf("PlatformSubscription = false, want true")
	}
}

func TestGetLicenseSettings(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/platform/management/v1/environment/license/settings", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		resp := LicenseSettings{
			Settings: []LicenseSetting{
				{Key: "AUTOMATION", Value: "true"},
				{Key: "AI_FUNCTIONS", Value: "true"},
				{Key: "LIVE_DEBUGGING", Value: "true"},
			},
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	})

	h := NewHandler(newTestClient(t, mux))
	result, err := h.GetLicenseSettings(context.Background())
	if err != nil {
		t.Fatalf("GetLicenseSettings() error: %v", err)
	}
	if len(result.Settings) != 3 {
		t.Errorf("len(Settings) = %d, want 3", len(result.Settings))
	}
	if result.Settings[0].Key != "AUTOMATION" {
		t.Errorf("Settings[0].Key = %q, want %q", result.Settings[0].Key, "AUTOMATION")
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
			fmt.Fprintf(w, `{"error":{"message":"unexpected keys param"}}`)
			return
		}
		resp := LicenseSettings{
			Settings: []LicenseSetting{
				{Key: "AUTOMATION", Value: "true"},
			},
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	})

	h := NewHandler(newTestClient(t, mux))
	result, err := h.GetLicenseSettings(context.Background(), "AUTOMATION")
	if err != nil {
		t.Fatalf("GetLicenseSettings() error: %v", err)
	}
	if len(result.Settings) != 1 {
		t.Errorf("len(Settings) = %d, want 1", len(result.Settings))
	}
	if result.Settings[0].Key != "AUTOMATION" {
		t.Errorf("Settings[0].Key = %q, want %q", result.Settings[0].Key, "AUTOMATION")
	}
}

func TestGetLicenseSettings_WithMultipleKeys(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/platform/management/v1/environment/license/settings", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		keys := r.URL.Query()["keys"]
		if len(keys) != 2 {
			w.WriteHeader(http.StatusBadRequest)
			fmt.Fprintf(w, `{"error":{"message":"expected 2 keys, got %d"}}`, len(keys))
			return
		}
		resp := LicenseSettings{
			Settings: []LicenseSetting{
				{Key: keys[0], Value: "true"},
				{Key: keys[1], Value: "true"},
			},
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	})

	h := NewHandler(newTestClient(t, mux))
	result, err := h.GetLicenseSettings(context.Background(), "AUTOMATION", "AI_FUNCTIONS")
	if err != nil {
		t.Fatalf("GetLicenseSettings() error: %v", err)
	}
	if len(result.Settings) != 2 {
		t.Errorf("len(Settings) = %d, want 2", len(result.Settings))
	}
}

func TestGetLicenseSettings_Error(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/platform/management/v1/environment/license/settings", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		fmt.Fprintf(w, `{"error":{"message":"missing scope"}}`)
	})

	h := NewHandler(newTestClient(t, mux))
	_, err := h.GetLicenseSettings(context.Background())
	if err == nil {
		t.Fatal("GetLicenseSettings() expected error for 403")
	}
}

func TestGetLicense_Error(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/platform/management/v1/environment/license", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		fmt.Fprintf(w, `{"error":{"message":"missing scope"}}`)
	})

	h := NewHandler(newTestClient(t, mux))
	_, err := h.GetLicense(context.Background())
	if err == nil {
		t.Fatal("GetLicense() expected error for 403")
	}
}
