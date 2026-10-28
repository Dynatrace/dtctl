package azuremonitoringconfig

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/Dynatrace/dtctl/pkg/client"
	"github.com/Dynatrace/dtctl/pkg/resources/azureconnection"
)

func TestSplitCSV(t *testing.T) {
	got := SplitCSV(" a, b ,, c ")
	want := []string{"a", "b", "c"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("SplitCSV() = %#v, want %#v", got, want)
	}
}

func TestParseTagFiltering(t *testing.T) {
	t.Run("empty returns nil", func(t *testing.T) {
		out, err := ParseTagFiltering("   ")
		if err != nil {
			t.Fatalf("ParseTagFiltering() error = %v", err)
		}
		if out != nil {
			t.Fatalf("ParseTagFiltering() = %#v, want nil", out)
		}
	})

	t.Run("success include and exclude", func(t *testing.T) {
		out, err := ParseTagFiltering("include:k1=v1,k2=v2;exclude:k3=v3")
		if err != nil {
			t.Fatalf("ParseTagFiltering() error = %v", err)
		}
		if len(out) != 3 {
			t.Fatalf("len(ParseTagFiltering()) = %d, want 3", len(out))
		}
		if out[0].Condition != "INCLUDE" || out[2].Condition != "EXCLUDE" {
			t.Fatalf("unexpected conditions: %#v", out)
		}
	})

	t.Run("accept typo eclude for compatibility", func(t *testing.T) {
		out, err := ParseTagFiltering("eclude:k=v")
		if err != nil {
			t.Fatalf("ParseTagFiltering() error = %v", err)
		}
		if len(out) != 1 || out[0].Condition != "EXCLUDE" {
			t.Fatalf("unexpected output: %#v", out)
		}
	})

	t.Run("invalid condition", func(t *testing.T) {
		_, err := ParseTagFiltering("foo:k=v")
		if err == nil || !strings.Contains(err.Error(), "invalid tagfiltering condition") {
			t.Fatalf("expected invalid condition error, got %v", err)
		}
	})

	t.Run("invalid section", func(t *testing.T) {
		_, err := ParseTagFiltering("include")
		if err == nil || !strings.Contains(err.Error(), "invalid tagfiltering section") {
			t.Fatalf("expected invalid section error, got %v", err)
		}
	})

	t.Run("invalid key value pair", func(t *testing.T) {
		_, err := ParseTagFiltering("include:key")
		if err == nil || !strings.Contains(err.Error(), "invalid tagfiltering pair") {
			t.Fatalf("expected invalid pair error, got %v", err)
		}
	})
}

func TestParseOrDefaultLocationsAndFeatureSets(t *testing.T) {
	calls := 0
	h, server := newMonitoringHandler(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		calls++
		if calls == 1 {
			_ = json.NewEncoder(w).Encode(ExtensionResponse{Items: []ExtensionItem{{Version: "1.0.0"}}})
			return
		}
		_ = json.NewEncoder(w).Encode(ExtensionSchemaResponse{Enums: map[string]SchemaEnum{
			"dynatrace.datasource.azure:location": {Items: []SchemaEnumItem{{Value: "eastus"}, {Value: "westeurope"}}},
			"FeatureSetsType":                     {Items: []SchemaEnumItem{{Value: "logs_essential"}, {Value: "metrics_all"}}},
		}})
	})
	defer server.Close()

	locs, err := ParseOrDefaultLocations("", h)
	if err != nil {
		t.Fatalf("ParseOrDefaultLocations() error = %v", err)
	}
	if !reflect.DeepEqual(locs, []string{"eastus", "westeurope"}) {
		t.Fatalf("unexpected locations: %#v", locs)
	}

	locs, err = ParseOrDefaultLocations("eu,north", h)
	if err != nil {
		t.Fatalf("ParseOrDefaultLocations(csv) error = %v", err)
	}
	if !reflect.DeepEqual(locs, []string{"eu", "north"}) {
		t.Fatalf("unexpected parsed locations: %#v", locs)
	}

	calls = 0
	sets, err := ParseOrDefaultFeatureSets("", h)
	if err != nil {
		t.Fatalf("ParseOrDefaultFeatureSets() error = %v", err)
	}
	if !reflect.DeepEqual(sets, []string{"logs_essential"}) {
		t.Fatalf("unexpected feature sets: %#v", sets)
	}

	sets, err = ParseOrDefaultFeatureSets("a,b", h)
	if err != nil {
		t.Fatalf("ParseOrDefaultFeatureSets(csv) error = %v", err)
	}
	if !reflect.DeepEqual(sets, []string{"a", "b"}) {
		t.Fatalf("unexpected parsed feature sets: %#v", sets)
	}
}

func TestParseOrDefaultFeatureSets_NoEssential(t *testing.T) {
	calls := 0
	h, server := newMonitoringHandler(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		calls++
		if calls == 1 {
			_ = json.NewEncoder(w).Encode(ExtensionResponse{Items: []ExtensionItem{{Version: "1.0.0"}}})
			return
		}
		_ = json.NewEncoder(w).Encode(ExtensionSchemaResponse{Enums: map[string]SchemaEnum{
			"FeatureSetsType": {Items: []SchemaEnumItem{{Value: "metrics_all"}}},
		}})
	})
	defer server.Close()

	_, err := ParseOrDefaultFeatureSets("", h)
	if err == nil || !strings.Contains(err.Error(), "no feature sets with suffix _essential found") {
		t.Fatalf("expected no essential error, got %v", err)
	}
}

func TestResolveCredential(t *testing.T) {
	t.Run("resolve by name client secret", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			if r.Method == http.MethodGet && r.URL.Query().Get("schemaIds") != "" {
				_ = json.NewEncoder(w).Encode(azureconnection.ListResponse{Items: []azureconnection.AzureConnection{{
					ObjectID: "obj-1",
					Value: azureconnection.Value{
						Name:         "conn-a",
						Type:         "clientSecret",
						ClientSecret: &azureconnection.ClientSecretCredential{ApplicationID: "app-1"},
					},
				}}})
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
		connHandler := azureconnection.NewHandler(c)

		cred, err := ResolveCredential("conn-a", connHandler)
		if err != nil {
			t.Fatalf("ResolveCredential() error = %v", err)
		}
		if cred.Type != "SECRET" || cred.ServicePrincipalId != "app-1" || cred.ConnectionId != "obj-1" {
			t.Fatalf("unexpected credential: %#v", cred)
		}
	})

	t.Run("resolve by id fallback federated", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			if r.Method == http.MethodGet && r.URL.Query().Get("schemaIds") != "" {
				_ = json.NewEncoder(w).Encode(azureconnection.ListResponse{Items: []azureconnection.AzureConnection{}})
				return
			}
			if r.Method == http.MethodGet {
				_ = json.NewEncoder(w).Encode(azureconnection.AzureConnection{
					ObjectID: "obj-2",
					Value: azureconnection.Value{
						Name:                        "conn-b",
						Type:                        "federatedIdentityCredential",
						FederatedIdentityCredential: &azureconnection.FederatedIdentityCredential{ApplicationID: "app-fed"},
					},
				})
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
		connHandler := azureconnection.NewHandler(c)

		cred, err := ResolveCredential("obj-2", connHandler)
		if err != nil {
			t.Fatalf("ResolveCredential() error = %v", err)
		}
		if cred.Type != "FEDERATED" || cred.ServicePrincipalId != "app-fed" || cred.ConnectionId != "obj-2" {
			t.Fatalf("unexpected credential: %#v", cred)
		}
	})

	t.Run("not found", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte("not found"))
		}))
		defer server.Close()

		c, err := client.NewForTesting(server.URL, "test-token")
		if err != nil {
			t.Fatalf("client.New() error = %v", err)
		}
		c.HTTP().SetRetryCount(0)
		connHandler := azureconnection.NewHandler(c)

		_, err = ResolveCredential("missing", connHandler)
		if err == nil || !strings.Contains(err.Error(), "not found by name or ID") {
			t.Fatalf("expected not found error, got %v", err)
		}
	})
}
