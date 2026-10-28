package gcpmonitoringconfig

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/Dynatrace/dtctl/pkg/client"
	"github.com/Dynatrace/dtctl/pkg/resources/gcpconnection"
)

func TestSplitCSV(t *testing.T) {
	got := SplitCSV(" a, b ,, c ")
	want := []string{"a", "b", "c"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("SplitCSV() = %#v, want %#v", got, want)
	}
}

func TestParseLocations(t *testing.T) {
	for _, tc := range []struct {
		name    string
		input   string
		want    []string
		wantErr string
	}{
		// #573: no flag means no filter, not every schema location.
		{name: "blank means no filter", input: "  ", want: []string{}},
		{name: "all means no filter", input: "all", want: []string{}},
		{name: "all is case-insensitive", input: " ALL ", want: []string{}},
		{name: "explicit list", input: "us-central1, europe-west1", want: []string{"us-central1", "europe-west1"}},
		{name: "only separators", input: " , ", wantErr: "at least one location"},
		{name: "all combined with a location", input: "all,us-central1", wantErr: "cannot be combined"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ParseLocations(tc.input)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("ParseLocations(%q) error = %v, want %q", tc.input, err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("ParseLocations(%q) error = %v", tc.input, err)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("ParseLocations(%q) = %#v, want %#v", tc.input, got, tc.want)
			}
		})
	}
}

func TestParseOrDefaultFeatureSets(t *testing.T) {
	calls := 0
	h, server := newMonitoringHandler(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		calls++
		if calls == 1 {
			_ = json.NewEncoder(w).Encode(ExtensionResponse{Items: []ExtensionItem{{Version: "1.0.0"}}})
			return
		}
		_ = json.NewEncoder(w).Encode(ExtensionSchemaResponse{Enums: map[string]SchemaEnum{
			"FeatureSetsType": {Items: []SchemaEnumItem{{Value: "compute_engine_essential"}, {Value: "metrics_all"}}},
		}})
	})
	defer server.Close()

	sets, err := ParseOrDefaultFeatureSets("", h)
	if err != nil {
		t.Fatalf("ParseOrDefaultFeatureSets() error = %v", err)
	}
	if !reflect.DeepEqual(sets, []string{"compute_engine_essential"}) {
		t.Fatalf("unexpected feature sets: %#v", sets)
	}
}

func TestResolveCredential(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodGet && r.URL.Query().Get("schemaIds") != "" {
			_ = json.NewEncoder(w).Encode(gcpconnection.ListResponse{Items: []gcpconnection.GCPConnection{{
				ObjectID: "obj-1",
				Value: gcpconnection.Value{
					Name:                        "conn-a",
					Type:                        "serviceAccountImpersonation",
					ServiceAccountImpersonation: &gcpconnection.ServiceAccountImpersonation{ServiceAccountID: "sa@test"},
				},
			}}})
			return
		}
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"error":{"message":"not found"}}`))
	}))
	defer server.Close()

	c, err := client.NewForTesting(server.URL, "test-token")
	if err != nil {
		t.Fatalf("client.New() error = %v", err)
	}
	c.HTTP().SetRetryCount(0)
	connHandler := gcpconnection.NewHandler(c)

	cred, err := ResolveCredential("conn-a", connHandler)
	if err != nil {
		t.Fatalf("ResolveCredential() error = %v", err)
	}
	if cred.ConnectionID != "obj-1" || cred.ServiceAccount != "sa@test" {
		t.Fatalf("unexpected credential: %#v", cred)
	}

	_, err = ResolveCredential("missing", connHandler)
	if err == nil || !strings.Contains(err.Error(), "not found by name or ID") {
		t.Fatalf("expected not found error, got %v", err)
	}
}

func TestResolveCredential_DoesNotMaskListFailureAsNotFound(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && r.URL.Query().Get("schemaIds") != "" {
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`{"error":{"message":"backend unavailable"}}`))
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()

	c, err := client.NewForTesting(server.URL, "test-token")
	if err != nil {
		t.Fatalf("client.New() error = %v", err)
	}
	c.HTTP().SetRetryCount(0)
	connHandler := gcpconnection.NewHandler(c)

	_, err = ResolveCredential("conn-a", connHandler)
	if err == nil {
		t.Fatalf("expected error, got nil")
	}
	if strings.Contains(strings.ToLower(err.Error()), "not found by name or id") {
		t.Fatalf("expected non-not-found error, got %v", err)
	}
	if !strings.Contains(err.Error(), "failed to resolve gcp connection") {
		t.Fatalf("expected wrapped resolve error, got %v", err)
	}
}
