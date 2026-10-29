package extension

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/Dynatrace/dtctl/pkg/client"
	"github.com/Dynatrace/dtctl/sdk/httpclient"
)

func TestNewHandler(t *testing.T) {
	c, err := client.New("https://test.dynatrace.com", "test-token")
	if err != nil {
		t.Fatalf("failed to create client: %v", err)
	}
	h := NewHandler(c)

	if h == nil {
		t.Fatal("NewHandler() returned nil")
	}
	if h.sdk == nil {
		t.Error("Handler.sdk is nil")
	}
}

func TestList(t *testing.T) {
	tests := []struct {
		name          string
		chunkSize     int64
		nameFilter    string
		pages         []ExtensionList
		expectError   bool
		errorContains string
		validate      func(*testing.T, *ExtensionList)
	}{
		{
			name:      "successful list single page",
			chunkSize: 0,
			pages: []ExtensionList{
				{
					TotalCount: 2,
					Items: []Extension{
						{ExtensionName: "com.dynatrace.extension.host-monitoring", Version: "1.2.3"},
						{ExtensionName: "com.dynatrace.extension.jmx", Version: "2.0.0"},
					},
				},
			},
			validate: func(t *testing.T, result *ExtensionList) {
				if len(result.Items) != 2 {
					t.Errorf("expected 2 extensions, got %d", len(result.Items))
				}
				if result.TotalCount != 2 {
					t.Errorf("expected TotalCount 2, got %d", result.TotalCount)
				}
			},
		},
		{
			name:      "paginated list with chunking",
			chunkSize: 10,
			pages: []ExtensionList{
				{
					TotalCount:  3,
					NextPageKey: "page2",
					Items: []Extension{
						{ExtensionName: "ext-1", Version: "1.0.0"},
						{ExtensionName: "ext-2", Version: "2.0.0"},
					},
				},
				{
					TotalCount: 3,
					Items: []Extension{
						{ExtensionName: "ext-3", Version: "3.0.0"},
					},
				},
			},
			validate: func(t *testing.T, result *ExtensionList) {
				if len(result.Items) != 3 {
					t.Errorf("expected 3 extensions across pages, got %d", len(result.Items))
				}
				if result.TotalCount != 3 {
					t.Errorf("expected TotalCount 3, got %d", result.TotalCount)
				}
			},
		},
		{
			name:      "empty list",
			chunkSize: 0,
			pages: []ExtensionList{
				{
					TotalCount: 0,
					Items:      []Extension{},
				},
			},
			validate: func(t *testing.T, result *ExtensionList) {
				if len(result.Items) != 0 {
					t.Errorf("expected 0 extensions, got %d", len(result.Items))
				}
			},
		},
		{
			name:       "with name filter",
			chunkSize:  0,
			nameFilter: "sql",
			pages: []ExtensionList{
				{
					TotalCount: 4,
					Items: []Extension{
						{ExtensionName: "com.dynatrace.extension.host-monitoring", Version: "1.0.0"},
						{ExtensionName: "com.dynatrace.extension.sql-oracle", Version: "2.0.0"},
						{ExtensionName: "com.dynatrace.extension.mysql", Version: "3.0.0"},
						{ExtensionName: "com.dynatrace.extension.sql-db2", Version: "1.5.0"},
					},
				},
			},
			validate: func(t *testing.T, result *ExtensionList) {
				// API returns all 4, client-side filter keeps only those containing "sql" (case-insensitive)
				if len(result.Items) != 3 {
					t.Errorf("expected 3 filtered extensions, got %d", len(result.Items))
				}
				expected := map[string]bool{
					"com.dynatrace.extension.sql-oracle": true,
					"com.dynatrace.extension.mysql":      true,
					"com.dynatrace.extension.sql-db2":    true,
				}
				for _, ext := range result.Items {
					if !expected[ext.ExtensionName] {
						t.Errorf("unexpected extension in filtered result: %s", ext.ExtensionName)
					}
				}
			},
		},
		{
			name:      "chunk size capped to API maximum",
			chunkSize: 500,
			pages: []ExtensionList{
				{
					TotalCount: 1,
					Items: []Extension{
						{ExtensionName: "ext-1", Version: "1.0.0"},
					},
				},
			},
			validate: func(t *testing.T, result *ExtensionList) {
				if len(result.Items) != 1 {
					t.Errorf("expected 1 extension, got %d", len(result.Items))
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pageIndex := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				// Environment configuration requests return 404 (no active version) by default.
				// This allows the concurrent active version fetch in List() to silently skip.
				if strings.HasSuffix(r.URL.Path, "/environment-configuration") {
					w.WriteHeader(http.StatusNotFound)
					return
				}

				if r.URL.Path != "/platform/extensions/v2/extensions" {
					t.Errorf("unexpected path: %s", r.URL.Path)
					w.WriteHeader(http.StatusNotFound)
					return
				}

				// Simulate API constraint: page-size must not be combined with next-page-key
				if r.URL.Query().Get("page-size") != "" && r.URL.Query().Get("next-page-key") != "" {
					w.WriteHeader(http.StatusBadRequest)
					w.Write([]byte(`{"error":{"code":400,"message":"Constraints violated.","constraintViolations":[{"path":"page-size","message":"must not be used in combination with next-page-key query parameter."}]}}`))
					return
				}

				// Simulate API page-size limit (rejects > maxPageSize)
				if ps := r.URL.Query().Get("page-size"); ps != "" {
					pageSizeVal, _ := strconv.ParseInt(ps, 10, 64)
					if pageSizeVal > 100 {
						w.WriteHeader(http.StatusBadRequest)
						w.Write([]byte(`{"error":"page-size exceeds maximum"}`))
						return
					}
				}

				if tt.nameFilter != "" {
					name := r.URL.Query().Get("name")
					if name != tt.nameFilter {
						t.Errorf("expected name filter %q, got %q", tt.nameFilter, name)
					}
				}

				if pageIndex >= len(tt.pages) {
					t.Error("received more requests than expected pages")
					w.WriteHeader(http.StatusInternalServerError)
					return
				}

				w.Header().Set("Content-Type", "application/json")
				json.NewEncoder(w).Encode(tt.pages[pageIndex])
				pageIndex++
			}))
			defer server.Close()

			c, err := client.New(server.URL, "test-token")
			if err != nil {
				t.Fatalf("failed to create client: %v", err)
			}

			handler := NewHandler(c)
			result, err := handler.List(context.Background(), tt.nameFilter, tt.chunkSize)

			if tt.expectError {
				if err == nil {
					t.Fatal("expected error but got nil")
				}
				return
			}

			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			if tt.validate != nil {
				tt.validate(t, result)
			}
		})
	}
}

func TestList_ActiveVersionEnrichment(t *testing.T) {
	extensions := []Extension{
		{ExtensionName: "com.dynatrace.extension.host-monitoring", Version: "1.2.3"},
		{ExtensionName: "com.dynatrace.extension.jmx", Version: "2.0.0"},
	}
	activeVersions := map[string]string{
		"com.dynatrace.extension.host-monitoring": "1.2.0",
		// jmx has no active version (404)
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")

		// Handle environment configuration requests
		for extName, activeVer := range activeVersions {
			envCfgPath := "/platform/extensions/v2/extensions/" + httpclient.PathSegment(extName) + "/environment-configuration"
			if r.URL.Path == envCfgPath {
				json.NewEncoder(w).Encode(map[string]string{"version": activeVer})
				return
			}
		}
		// Unknown env config path: 404 (extension has no active version)
		if strings.HasSuffix(r.URL.Path, "/environment-configuration") {
			w.WriteHeader(http.StatusNotFound)
			return
		}

		// Extension list endpoint
		if r.URL.Path == "/platform/extensions/v2/extensions" {
			json.NewEncoder(w).Encode(ExtensionList{TotalCount: len(extensions), Items: extensions})
			return
		}

		t.Errorf("unexpected path: %s", r.URL.Path)
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()

	c, err := client.New(server.URL, "test-token")
	if err != nil {
		t.Fatalf("failed to create client: %v", err)
	}

	handler := NewHandler(c)
	result, err := handler.List(context.Background(), "", 0)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(result.Items) != 2 {
		t.Fatalf("expected 2 extensions, got %d", len(result.Items))
	}

	byName := make(map[string]Extension)
	for _, ext := range result.Items {
		byName[ext.ExtensionName] = ext
	}

	hostMon := byName["com.dynatrace.extension.host-monitoring"]
	if hostMon.ActiveVersion != "1.2.0" {
		t.Errorf("expected ActiveVersion %q for host-monitoring, got %q", "1.2.0", hostMon.ActiveVersion)
	}

	jmx := byName["com.dynatrace.extension.jmx"]
	if jmx.ActiveVersion != "" {
		t.Errorf("expected empty ActiveVersion for jmx, got %q", jmx.ActiveVersion)
	}
}

func TestList_ActiveVersionEnrichmentError(t *testing.T) {
	extensions := []Extension{
		{ExtensionName: "com.dynatrace.extension.host-monitoring", Version: "1.2.3"},
		{ExtensionName: "com.dynatrace.extension.jmx", Version: "2.0.0"},
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.HasSuffix(r.URL.Path, "/environment-configuration") {
			// Return 403 for all env-config requests — should not abort the listing.
			w.WriteHeader(http.StatusForbidden)
			return
		}
		if r.URL.Path == "/platform/extensions/v2/extensions" {
			json.NewEncoder(w).Encode(ExtensionList{TotalCount: len(extensions), Items: extensions})
			return
		}
		t.Errorf("unexpected path: %s", r.URL.Path)
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()

	c, err := client.New(server.URL, "test-token")
	if err != nil {
		t.Fatalf("failed to create client: %v", err)
	}

	handler := NewHandler(c)
	result, err := handler.List(context.Background(), "", 0)
	if err != nil {
		t.Fatalf("non-404 env-config error must not abort listing, got error: %v", err)
	}
	for _, ext := range result.Items {
		if ext.ActiveVersion != "" {
			t.Errorf("expected empty ActiveVersion for %q on 403, got %q", ext.ExtensionName, ext.ActiveVersion)
		}
	}
}

func TestGet(t *testing.T) {
	tests := []struct {
		name          string
		extensionName string
		statusCode    int
		response      ExtensionVersionList
		activeVersion string
		expectError   bool
		errorContains string
	}{
		{
			name:          "successful get",
			extensionName: "com.dynatrace.extension.host-monitoring",
			statusCode:    200,
			response: ExtensionVersionList{
				TotalCount: 2,
				Items: []ExtensionVersion{
					{Version: "1.2.3", ExtensionName: "com.dynatrace.extension.host-monitoring"},
					{Version: "1.2.2", ExtensionName: "com.dynatrace.extension.host-monitoring"},
				},
			},
			activeVersion: "1.2.3",
		},
		{
			name:          "not found",
			extensionName: "com.dynatrace.extension.nonexistent",
			statusCode:    404,
			expectError:   true,
			errorContains: "not found",
		},
		{
			name:          "access denied",
			extensionName: "com.dynatrace.extension.restricted",
			statusCode:    403,
			expectError:   true,
			errorContains: "access denied",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				versionListPath := "/platform/extensions/v2/extensions/" + tt.extensionName
				envConfigPath := versionListPath + "/environment-configuration"

				w.Header().Set("Content-Type", "application/json")

				switch r.URL.Path {
				case envConfigPath:
					if tt.activeVersion != "" {
						w.WriteHeader(http.StatusOK)
						json.NewEncoder(w).Encode(map[string]string{"version": tt.activeVersion})
					} else {
						w.WriteHeader(http.StatusNotFound)
					}
				case versionListPath:
					w.WriteHeader(tt.statusCode)
					if tt.statusCode == 200 {
						json.NewEncoder(w).Encode(tt.response)
					}
				default:
					t.Errorf("unexpected path: %s", r.URL.Path)
					w.WriteHeader(http.StatusNotFound)
				}
			}))
			defer server.Close()

			c, err := client.New(server.URL, "test-token")
			if err != nil {
				t.Fatalf("failed to create client: %v", err)
			}

			handler := NewHandler(c)
			result, err := handler.Get(tt.extensionName)

			if tt.expectError {
				if err == nil {
					t.Fatal("expected error but got nil")
				}
				if tt.errorContains != "" && !strings.Contains(err.Error(), tt.errorContains) {
					t.Errorf("expected error containing %q, got %q", tt.errorContains, err.Error())
				}
				return
			}

			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			if len(result.Items) != len(tt.response.Items) {
				t.Errorf("expected %d versions, got %d", len(tt.response.Items), len(result.Items))
			}

			if tt.activeVersion != "" {
				found := false
				for _, v := range result.Items {
					if v.Version == tt.activeVersion && v.Active {
						found = true
					}
					if v.Version != tt.activeVersion && v.Active {
						t.Errorf("version %q should not be active", v.Version)
					}
				}
				if !found {
					t.Errorf("expected version %q to be marked active", tt.activeVersion)
				}
			}
		})
	}
}

func TestGetVersion(t *testing.T) {
	tests := []struct {
		name          string
		extensionName string
		version       string
		statusCode    int
		response      ExtensionDetails
		expectError   bool
		errorContains string
	}{
		{
			name:          "successful get version",
			extensionName: "com.dynatrace.extension.host-monitoring",
			version:       "1.2.3",
			statusCode:    200,
			response: ExtensionDetails{
				ExtensionName:       "com.dynatrace.extension.host-monitoring",
				Version:             "1.2.3",
				Author:              ExtensionAuthor{Name: "Dynatrace"},
				DataSources:         []string{"snmp", "wmi"},
				FeatureSets:         []string{"default", "advanced"},
				MinDynatraceVersion: "1.250",
			},
		},
		{
			name:          "version not found",
			extensionName: "com.dynatrace.extension.host-monitoring",
			version:       "99.99.99",
			statusCode:    404,
			expectError:   true,
			errorContains: "not found",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				expectedPath := "/platform/extensions/v2/extensions/" + tt.extensionName + "/" + tt.version
				if r.URL.Path != expectedPath {
					t.Errorf("unexpected path: %s (expected %s)", r.URL.Path, expectedPath)
					w.WriteHeader(http.StatusNotFound)
					return
				}

				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(tt.statusCode)
				if tt.statusCode == 200 {
					json.NewEncoder(w).Encode(tt.response)
				}
			}))
			defer server.Close()

			c, err := client.New(server.URL, "test-token")
			if err != nil {
				t.Fatalf("failed to create client: %v", err)
			}

			handler := NewHandler(c)
			result, err := handler.GetVersion(tt.extensionName, tt.version)

			if tt.expectError {
				if err == nil {
					t.Fatal("expected error but got nil")
				}
				if tt.errorContains != "" && !strings.Contains(err.Error(), tt.errorContains) {
					t.Errorf("expected error containing %q, got %q", tt.errorContains, err.Error())
				}
				return
			}

			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			if result.ExtensionName != tt.response.ExtensionName {
				t.Errorf("expected name %q, got %q", tt.response.ExtensionName, result.ExtensionName)
			}
			if result.Version != tt.response.Version {
				t.Errorf("expected version %q, got %q", tt.response.Version, result.Version)
			}
			if result.Author.Name != tt.response.Author.Name {
				t.Errorf("expected author %q, got %q", tt.response.Author.Name, result.Author.Name)
			}
		})
	}
}

func TestGetEnvironmentConfig(t *testing.T) {
	tests := []struct {
		name          string
		extensionName string
		version       string
		statusCode    int
		response      ExtensionEnvironmentConfig
		expectError   bool
		errorContains string
	}{
		{
			name:          "successful get config",
			extensionName: "com.dynatrace.extension.host-monitoring",
			version:       "1.2.3",
			statusCode:    200,
			response:      ExtensionEnvironmentConfig{Version: "1.2.3"},
		},
		{
			name:          "no active config",
			extensionName: "com.dynatrace.extension.inactive",
			version:       "1.0.0",
			statusCode:    404,
			expectError:   true,
			errorContains: "no environment configuration",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				expectedPath := "/platform/extensions/v2/extensions/" + tt.extensionName + "/" + tt.version + "/environment-configuration"
				if r.URL.Path != expectedPath {
					t.Errorf("unexpected path: %s (expected %s)", r.URL.Path, expectedPath)
					w.WriteHeader(http.StatusNotFound)
					return
				}

				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(tt.statusCode)
				if tt.statusCode == 200 {
					json.NewEncoder(w).Encode(tt.response)
				}
			}))
			defer server.Close()

			c, err := client.New(server.URL, "test-token")
			if err != nil {
				t.Fatalf("failed to create client: %v", err)
			}

			handler := NewHandler(c)
			result, err := handler.GetEnvironmentConfig(tt.extensionName, tt.version)

			if tt.expectError {
				if err == nil {
					t.Fatal("expected error but got nil")
				}
				if tt.errorContains != "" && !strings.Contains(err.Error(), tt.errorContains) {
					t.Errorf("expected error containing %q, got %q", tt.errorContains, err.Error())
				}
				return
			}

			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			if result.Version != tt.response.Version {
				t.Errorf("expected version %q, got %q", tt.response.Version, result.Version)
			}
		})
	}
}

func TestListMonitoringConfigurations(t *testing.T) {
	tests := []struct {
		name          string
		extensionName string
		version       string
		chunkSize     int64
		statusCode    int
		response      MonitoringConfigurationList
		expectedCount int
		expectedIDs   []string
		expectError   bool
		errorContains string
	}{
		{
			name:          "successful list",
			extensionName: "com.dynatrace.extension.host-monitoring",
			chunkSize:     0,
			statusCode:    200,
			response: MonitoringConfigurationList{
				TotalCount: 2,
				Items: []MonitoringConfiguration{
					{ObjectID: "config-1", Scope: "HOST-123"},
					{ObjectID: "config-2", Scope: "HOST_GROUP-456"},
				},
			},
			expectedCount: 2,
		},
		{
			name:          "with version filter",
			extensionName: "com.dynatrace.extension.host-monitoring",
			version:       "1.2.3",
			chunkSize:     0,
			statusCode:    200,
			response: MonitoringConfigurationList{
				TotalCount: 3,
				Items: []MonitoringConfiguration{
					{ObjectID: "config-1", Scope: "HOST-123", Value: json.RawMessage(`{"version":"1.2.3","enabled":true}`)},
					{ObjectID: "config-2", Scope: "HOST-456", Value: json.RawMessage(`{"version":"2.0.0","enabled":true}`)},
					{ObjectID: "config-3", Scope: "HOST-789", Value: json.RawMessage(`{"version":"1.2.3","enabled":false}`)},
				},
			},
			expectedCount: 2,
			expectedIDs:   []string{"config-1", "config-3"},
		},
		{
			name:          "extension not found",
			extensionName: "com.dynatrace.extension.nonexistent",
			chunkSize:     0,
			statusCode:    404,
			expectError:   true,
			errorContains: "not found",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				expectedPath := "/platform/extensions/v2/extensions/" + tt.extensionName + "/monitoring-configurations"
				if r.URL.Path != expectedPath {
					t.Errorf("unexpected path: %s (expected %s)", r.URL.Path, expectedPath)
					w.WriteHeader(http.StatusNotFound)
					return
				}

				// NOTE: Implementation does not set version as a query param, so do not check for it here.

				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(tt.statusCode)
				if tt.statusCode == 200 {
					json.NewEncoder(w).Encode(tt.response)
				}
			}))
			defer server.Close()

			c, err := client.New(server.URL, "test-token")
			if err != nil {
				t.Fatalf("failed to create client: %v", err)
			}

			handler := NewHandler(c)
			result, err := handler.ListMonitoringConfigurations(tt.extensionName, tt.version, tt.chunkSize)

			if tt.expectError {
				if err == nil {
					t.Fatal("expected error but got nil")
				}
				if tt.errorContains != "" && !strings.Contains(err.Error(), tt.errorContains) {
					t.Errorf("expected error containing %q, got %q", tt.errorContains, err.Error())
				}
				return
			}

			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			expectedCount := tt.expectedCount
			if expectedCount == 0 {
				expectedCount = len(tt.response.Items)
			}
			if len(result.Items) != expectedCount {
				t.Errorf("expected %d configs, got %d", expectedCount, len(result.Items))
			}
			if len(tt.expectedIDs) > 0 {
				for i, id := range tt.expectedIDs {
					if i < len(result.Items) && result.Items[i].ObjectID != id {
						t.Errorf("expected item %d to have ID %q, got %q", i, id, result.Items[i].ObjectID)
					}
				}
			}
		})
	}
}

func TestGetMonitoringConfiguration(t *testing.T) {
	tests := []struct {
		name          string
		extensionName string
		configID      string
		statusCode    int
		response      MonitoringConfiguration
		expectError   bool
		errorContains string
	}{
		{
			name:          "successful get",
			extensionName: "com.dynatrace.extension.host-monitoring",
			configID:      "config-1",
			statusCode:    200,
			response:      MonitoringConfiguration{ObjectID: "config-1", Scope: "HOST-123"},
		},
		{
			name:          "config not found",
			extensionName: "com.dynatrace.extension.host-monitoring",
			configID:      "nonexistent",
			statusCode:    404,
			expectError:   true,
			errorContains: "not found",
		},
		{
			name:          "access denied",
			extensionName: "com.dynatrace.extension.restricted",
			configID:      "config-1",
			statusCode:    403,
			expectError:   true,
			errorContains: "access denied",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				expectedPath := "/platform/extensions/v2/extensions/" + tt.extensionName + "/monitoring-configurations/" + tt.configID
				if r.URL.Path != expectedPath {
					t.Errorf("unexpected path: %s (expected %s)", r.URL.Path, expectedPath)
					w.WriteHeader(http.StatusNotFound)
					return
				}
				if r.Method != http.MethodGet {
					t.Errorf("unexpected method: %s (expected GET)", r.Method)
					w.WriteHeader(http.StatusMethodNotAllowed)
					return
				}

				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(tt.statusCode)
				if tt.statusCode == 200 {
					json.NewEncoder(w).Encode(tt.response)
				}
			}))
			defer server.Close()

			c, err := client.New(server.URL, "test-token")
			if err != nil {
				t.Fatalf("failed to create client: %v", err)
			}

			handler := NewHandler(c)
			result, err := handler.GetMonitoringConfiguration(tt.extensionName, tt.configID)

			if tt.expectError {
				if err == nil {
					t.Fatal("expected error but got nil")
				}
				if tt.errorContains != "" && !strings.Contains(err.Error(), tt.errorContains) {
					t.Errorf("expected error containing %q, got %q", tt.errorContains, err.Error())
				}
				return
			}

			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			if result.ObjectID != tt.response.ObjectID {
				t.Errorf("expected objectId %q, got %q", tt.response.ObjectID, result.ObjectID)
			}
			if result.Scope != tt.response.Scope {
				t.Errorf("expected scope %q, got %q", tt.response.Scope, result.Scope)
			}
			// Verify enrichment fields
			if result.Type != "extension_monitoring_config" {
				t.Errorf("expected type %q, got %q", "extension_monitoring_config", result.Type)
			}
			if result.ExtensionName != tt.extensionName {
				t.Errorf("expected extensionName %q, got %q", tt.extensionName, result.ExtensionName)
			}
		})
	}
}

func TestCreateMonitoringConfiguration(t *testing.T) {
	tests := []struct {
		name          string
		extensionName string
		body          MonitoringConfigurationCreate
		statusCode    int
		response      MonitoringConfiguration
		expectError   bool
		errorContains string
	}{
		{
			name:          "successful create",
			extensionName: "com.dynatrace.extension.host-monitoring",
			body: MonitoringConfigurationCreate{
				Scope: "HOST-123",
				Value: map[string]any{"enabled": true, "description": "test"},
			},
			statusCode: 200,
			response:   MonitoringConfiguration{ObjectID: "new-config-1", Scope: "HOST-123"},
		},
		{
			name:          "extension not found",
			extensionName: "com.dynatrace.extension.nonexistent",
			body: MonitoringConfigurationCreate{
				Value: map[string]any{"enabled": true},
			},
			statusCode:    404,
			expectError:   true,
			errorContains: "not found",
		},
		{
			name:          "access denied",
			extensionName: "com.dynatrace.extension.host-monitoring",
			body: MonitoringConfigurationCreate{
				Value: map[string]any{"enabled": true},
			},
			statusCode:    403,
			expectError:   true,
			errorContains: "access denied",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				expectedPath := "/platform/extensions/v2/extensions/" + tt.extensionName + "/monitoring-configurations"
				if r.URL.Path != expectedPath {
					t.Errorf("unexpected path: %s (expected %s)", r.URL.Path, expectedPath)
					w.WriteHeader(http.StatusNotFound)
					return
				}
				if r.Method != http.MethodPost {
					t.Errorf("unexpected method: %s (expected POST)", r.Method)
					w.WriteHeader(http.StatusMethodNotAllowed)
					return
				}

				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(tt.statusCode)
				if tt.statusCode == 200 {
					json.NewEncoder(w).Encode(tt.response)
				}
			}))
			defer server.Close()

			c, err := client.New(server.URL, "test-token")
			if err != nil {
				t.Fatalf("failed to create client: %v", err)
			}

			handler := NewHandler(c)
			result, err := handler.CreateMonitoringConfiguration(tt.extensionName, tt.body)

			if tt.expectError {
				if err == nil {
					t.Fatal("expected error but got nil")
				}
				if tt.errorContains != "" && !strings.Contains(err.Error(), tt.errorContains) {
					t.Errorf("expected error containing %q, got %q", tt.errorContains, err.Error())
				}
				return
			}

			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			if result.ObjectID != tt.response.ObjectID {
				t.Errorf("expected objectId %q, got %q", tt.response.ObjectID, result.ObjectID)
			}
		})
	}
}

func TestUpdateMonitoringConfiguration(t *testing.T) {
	tests := []struct {
		name          string
		extensionName string
		configID      string
		body          MonitoringConfigurationCreate
		statusCode    int
		response      MonitoringConfiguration
		expectError   bool
		errorContains string
	}{
		{
			name:          "successful update",
			extensionName: "com.dynatrace.extension.host-monitoring",
			configID:      "config-1",
			body: MonitoringConfigurationCreate{
				Scope: "HOST-123",
				Value: map[string]any{"enabled": false, "description": "updated"},
			},
			statusCode: 200,
			response:   MonitoringConfiguration{ObjectID: "config-1", Scope: "HOST-123"},
		},
		{
			name:          "config not found",
			extensionName: "com.dynatrace.extension.host-monitoring",
			configID:      "nonexistent",
			body: MonitoringConfigurationCreate{
				Value: map[string]any{"enabled": true},
			},
			statusCode:    404,
			expectError:   true,
			errorContains: "not found",
		},
		{
			name:          "access denied",
			extensionName: "com.dynatrace.extension.host-monitoring",
			configID:      "config-1",
			body: MonitoringConfigurationCreate{
				Value: map[string]any{"enabled": true},
			},
			statusCode:    403,
			expectError:   true,
			errorContains: "access denied",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				expectedPath := "/platform/extensions/v2/extensions/" + tt.extensionName + "/monitoring-configurations/" + tt.configID
				if r.URL.Path != expectedPath {
					t.Errorf("unexpected path: %s (expected %s)", r.URL.Path, expectedPath)
					w.WriteHeader(http.StatusNotFound)
					return
				}
				if r.Method != http.MethodPut {
					t.Errorf("unexpected method: %s (expected PUT)", r.Method)
					w.WriteHeader(http.StatusMethodNotAllowed)
					return
				}

				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(tt.statusCode)
				if tt.statusCode == 200 {
					json.NewEncoder(w).Encode(tt.response)
				}
			}))
			defer server.Close()

			c, err := client.New(server.URL, "test-token")
			if err != nil {
				t.Fatalf("failed to create client: %v", err)
			}

			handler := NewHandler(c)
			result, err := handler.UpdateMonitoringConfiguration(tt.extensionName, tt.configID, tt.body)

			if tt.expectError {
				if err == nil {
					t.Fatal("expected error but got nil")
				}
				if tt.errorContains != "" && !strings.Contains(err.Error(), tt.errorContains) {
					t.Errorf("expected error containing %q, got %q", tt.errorContains, err.Error())
				}
				return
			}

			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			if result.ObjectID != tt.response.ObjectID {
				t.Errorf("expected objectId %q, got %q", tt.response.ObjectID, result.ObjectID)
			}
		})
	}
}

func TestDeleteMonitoringConfiguration(t *testing.T) {
	tests := []struct {
		name          string
		extensionName string
		configID      string
		statusCode    int
		expectError   bool
		errorContains string
	}{
		{
			name:          "successful delete",
			extensionName: "com.dynatrace.extension.host-monitoring",
			configID:      "config-1",
			statusCode:    204,
		},
		{
			name:          "config not found",
			extensionName: "com.dynatrace.extension.host-monitoring",
			configID:      "nonexistent",
			statusCode:    404,
			expectError:   true,
			errorContains: "not found",
		},
		{
			name:          "access denied",
			extensionName: "com.dynatrace.extension.restricted",
			configID:      "config-1",
			statusCode:    403,
			expectError:   true,
			errorContains: "access denied",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				expectedPath := "/platform/extensions/v2/extensions/" + tt.extensionName + "/monitoring-configurations/" + tt.configID
				if r.URL.Path != expectedPath {
					t.Errorf("unexpected path: %s (expected %s)", r.URL.Path, expectedPath)
					w.WriteHeader(http.StatusNotFound)
					return
				}
				if r.Method != http.MethodDelete {
					t.Errorf("unexpected method: %s (expected DELETE)", r.Method)
					w.WriteHeader(http.StatusMethodNotAllowed)
					return
				}

				w.WriteHeader(tt.statusCode)
			}))
			defer server.Close()

			c, err := client.New(server.URL, "test-token")
			if err != nil {
				t.Fatalf("failed to create client: %v", err)
			}

			handler := NewHandler(c)
			err = handler.DeleteMonitoringConfiguration(tt.extensionName, tt.configID)

			if tt.expectError {
				if err == nil {
					t.Fatal("expected error but got nil")
				}
				if tt.errorContains != "" && !strings.Contains(err.Error(), tt.errorContains) {
					t.Errorf("expected error containing %q, got %q", tt.errorContains, err.Error())
				}
				return
			}

			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
}

func TestMonitoringConfiguration_MarshalYAML(t *testing.T) {
	tests := []struct {
		name     string
		config   MonitoringConfiguration
		contains []string
		excludes []string
	}{
		{
			name: "structured value serialized as YAML map",
			config: MonitoringConfiguration{
				Type:          "extension_monitoring_config",
				ExtensionName: "com.dynatrace.extension.host-monitoring",
				ObjectID:      "test-id-001",
				Scope:         "environment",
				Value:         json.RawMessage(`{"enabled":true,"description":"test config","version":"1.0.0"}`),
			},
			contains: []string{
				"extensionName: com.dynatrace.extension.host-monitoring",
				"objectId: test-id-001",
				"scope: environment",
				"enabled: true",
				"description: test config",
				"version: 1.0.0",
			},
			// Must NOT contain byte array representation
			excludes: []string{
				"- 123",
				"- 34",
			},
		},
		{
			name: "empty value omitted",
			config: MonitoringConfiguration{
				ExtensionName: "com.dynatrace.extension.jmx",
				ObjectID:      "test-id-002",
				Scope:         "HOST-ABC",
			},
			contains: []string{
				"objectId: test-id-002",
				"scope: HOST-ABC",
			},
			excludes: []string{
				"value:",
			},
		},
		{
			name: "nested value preserved",
			config: MonitoringConfiguration{
				ObjectID: "test-id-003",
				Value:    json.RawMessage(`{"endpoints":[{"host":"example.invalid","port":5432}]}`),
			},
			contains: []string{
				"objectId: test-id-003",
				"host: example.invalid",
				"port: 5432",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := tt.config.MarshalYAML()
			if err != nil {
				t.Fatalf("MarshalYAML() unexpected error: %v", err)
			}
			if result == nil {
				t.Fatal("MarshalYAML() returned nil")
			}

			// Marshal the result to YAML string for content checks
			yamlBytes, err := yaml.Marshal(result)
			if err != nil {
				t.Fatalf("failed to marshal YAML result: %v", err)
			}
			yamlStr := string(yamlBytes)

			for _, want := range tt.contains {
				if !strings.Contains(yamlStr, want) {
					t.Errorf("YAML output missing expected content %q\ngot:\n%s", want, yamlStr)
				}
			}
			for _, exclude := range tt.excludes {
				if strings.Contains(yamlStr, exclude) {
					t.Errorf("YAML output should not contain %q\ngot:\n%s", exclude, yamlStr)
				}
			}
		})
	}
}

func TestUpload(t *testing.T) {
	tests := []struct {
		name          string
		fileName      string
		zipData       []byte
		statusCode    int
		response      ExtensionVersion
		expectError   bool
		errorContains string
	}{
		{
			name:       "successful upload",
			fileName:   "custom-extension.zip",
			zipData:    []byte("PK\x03\x04fake-zip-content"),
			statusCode: 200,
			response: ExtensionVersion{
				ExtensionName: "custom:my-extension",
				Version:       "1.0.0",
			},
		},
		{
			name:       "empty fileName defaults to extension.zip",
			fileName:   "",
			zipData:    []byte("PK\x03\x04fake-zip-content"),
			statusCode: 200,
			response: ExtensionVersion{
				ExtensionName: "custom:my-extension",
				Version:       "1.0.0",
			},
		},
		{
			name:          "invalid extension package",
			fileName:      "bad.zip",
			zipData:       []byte("not-a-zip"),
			statusCode:    400,
			expectError:   true,
			errorContains: "invalid extension package",
		},
		{
			name:          "access denied",
			fileName:      "my-extension.zip",
			zipData:       []byte("PK\x03\x04fake"),
			statusCode:    403,
			expectError:   true,
			errorContains: "access denied",
		},
		{
			name:          "version already exists",
			fileName:      "my-extension.zip",
			zipData:       []byte("PK\x03\x04fake"),
			statusCode:    409,
			expectError:   true,
			errorContains: "already exists",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/platform/extensions/v2/extensions" {
					t.Errorf("unexpected path: %s", r.URL.Path)
					w.WriteHeader(http.StatusNotFound)
					return
				}
				if r.Method != http.MethodPost {
					t.Errorf("unexpected method: %s (expected POST)", r.Method)
					w.WriteHeader(http.StatusMethodNotAllowed)
					return
				}
				ct := r.Header.Get("Content-Type")
				if ct != "application/octet-stream" {
					t.Errorf("expected application/octet-stream content type, got %s", ct)
					w.WriteHeader(http.StatusBadRequest)
					return
				}

				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(tt.statusCode)
				if tt.statusCode == 200 {
					json.NewEncoder(w).Encode(tt.response)
				}
			}))
			defer server.Close()

			c, err := client.New(server.URL, "test-token")
			if err != nil {
				t.Fatalf("failed to create client: %v", err)
			}

			handler := NewHandler(c)
			result, err := handler.Upload(tt.fileName, tt.zipData)

			if tt.expectError {
				if err == nil {
					t.Fatal("expected error but got nil")
				}
				if tt.errorContains != "" && !strings.Contains(err.Error(), tt.errorContains) {
					t.Errorf("expected error containing %q, got %q", tt.errorContains, err.Error())
				}
				return
			}

			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if result.ExtensionName != tt.response.ExtensionName {
				t.Errorf("expected extensionName %q, got %q", tt.response.ExtensionName, result.ExtensionName)
			}
			if result.Version != tt.response.Version {
				t.Errorf("expected version %q, got %q", tt.response.Version, result.Version)
			}
		})
	}
}

func TestInstallFromHub(t *testing.T) {
	tests := []struct {
		name          string
		extensionName string
		version       string
		installCode   int
		installResp   ExtensionVersion
		expectError   bool
		errorContains string
	}{
		{
			name:          "install specific version",
			extensionName: "com.dynatrace.extension.host-monitoring",
			version:       "1.2.3",
			installCode:   200,
			installResp: ExtensionVersion{
				ExtensionName: "com.dynatrace.extension.host-monitoring",
				Version:       "1.2.3",
			},
		},
		{
			name:          "install without version (latest)",
			extensionName: "com.dynatrace.extension.host-monitoring",
			version:       "",
			installCode:   200,
			installResp: ExtensionVersion{
				ExtensionName: "com.dynatrace.extension.host-monitoring",
				Version:       "2.0.0",
			},
		},
		{
			name:          "extension not found",
			extensionName: "com.dynatrace.extension.nonexistent",
			version:       "1.0.0",
			installCode:   404,
			expectError:   true,
			errorContains: "not found",
		},
		{
			name:          "access denied",
			extensionName: "com.dynatrace.extension.restricted",
			version:       "1.0.0",
			installCode:   403,
			expectError:   true,
			errorContains: "access denied",
		},
		{
			name:          "already installed",
			extensionName: "com.dynatrace.extension.host-monitoring",
			version:       "1.0.0",
			installCode:   409,
			expectError:   true,
			errorContains: `version "1.0.0" is already installed`,
		},
		{
			name:          "already installed (latest, no version specified)",
			extensionName: "com.dynatrace.extension.host-monitoring",
			version:       "",
			installCode:   409,
			expectError:   true,
			errorContains: "(latest version) is already installed",
		},
		{
			name:          "extension name with special characters is URL-encoded",
			extensionName: "com.example.extension+special",
			version:       "1.0.0",
			installCode:   200,
			installResp: ExtensionVersion{
				ExtensionName: "com.example.extension+special",
				Version:       "1.0.0",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				expectedPath := "/platform/extensions/v2/extensions/" + httpclient.PathSegment(tt.extensionName)
				if r.URL.Path != expectedPath {
					t.Errorf("unexpected path: %s (expected %s)", r.URL.Path, expectedPath)
					w.WriteHeader(http.StatusNotFound)
					return
				}
				if r.Method != http.MethodPost {
					t.Errorf("unexpected method: %s (expected POST)", r.Method)
					w.WriteHeader(http.StatusMethodNotAllowed)
					return
				}
				// Verify version query parameter
				gotVersion := r.URL.Query().Get("version")
				if tt.version != "" && gotVersion != tt.version {
					t.Errorf("expected version query param %q, got %q", tt.version, gotVersion)
				}
				if tt.version == "" && gotVersion != "" {
					t.Errorf("expected no version query param, got %q", gotVersion)
				}

				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(tt.installCode)
				if tt.installCode == 200 {
					json.NewEncoder(w).Encode(tt.installResp)
				}
			}))
			defer server.Close()

			c, err := client.New(server.URL, "test-token")
			if err != nil {
				t.Fatalf("failed to create client: %v", err)
			}

			handler := NewHandler(c)
			result, err := handler.InstallFromHub(tt.extensionName, tt.version)

			if tt.expectError {
				if err == nil {
					t.Fatal("expected error but got nil")
				}
				if tt.errorContains != "" && !strings.Contains(err.Error(), tt.errorContains) {
					t.Errorf("expected error containing %q, got %q", tt.errorContains, err.Error())
				}
				return
			}

			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if result.ExtensionName != tt.installResp.ExtensionName {
				t.Errorf("expected extensionName %q, got %q", tt.installResp.ExtensionName, result.ExtensionName)
			}
			if result.Version != tt.installResp.Version {
				t.Errorf("expected version %q, got %q", tt.installResp.Version, result.Version)
			}
		})
	}
}
func TestGetMonitoringConfigurationSchema(t *testing.T) {
	tests := []struct {
		name          string
		extensionName string
		version       string
		statusCode    int
		responseBody  string
		expectError   bool
		errorContains string
	}{
		{
			name:          "successful get schema",
			extensionName: "com.dynatrace.extension.host-monitoring",
			version:       "1.2.3",
			statusCode:    200,
			responseBody:  `{"type":"object","properties":{"enabled":{"type":"boolean"},"description":{"type":"string"}}}`,
		},
		{
			name:          "extension version not found",
			extensionName: "com.dynatrace.extension.host-monitoring",
			version:       "99.99.99",
			statusCode:    404,
			expectError:   true,
			errorContains: "not found",
		},
		{
			name:          "access denied",
			extensionName: "com.dynatrace.extension.host-monitoring",
			version:       "1.0.0",
			statusCode:    403,
			expectError:   true,
			errorContains: "access denied",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				expectedPath := "/platform/extensions/v2/extensions/" + tt.extensionName + "/" + tt.version + "/schema"
				if r.URL.Path != expectedPath {
					t.Errorf("unexpected path: %s (expected %s)", r.URL.Path, expectedPath)
					w.WriteHeader(http.StatusNotFound)
					return
				}
				if r.Method != http.MethodGet {
					t.Errorf("unexpected method: %s (expected GET)", r.Method)
					w.WriteHeader(http.StatusMethodNotAllowed)
					return
				}

				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(tt.statusCode)
				if tt.statusCode == 200 {
					w.Write([]byte(tt.responseBody))
				}
			}))
			defer server.Close()

			c, err := client.New(server.URL, "test-token")
			if err != nil {
				t.Fatalf("failed to create client: %v", err)
			}

			handler := NewHandler(c)
			result, err := handler.GetMonitoringConfigurationSchema(tt.extensionName, tt.version)

			if tt.expectError {
				if err == nil {
					t.Fatal("expected error but got nil")
				}
				if tt.errorContains != "" && !strings.Contains(err.Error(), tt.errorContains) {
					t.Errorf("expected error containing %q, got %q", tt.errorContains, err.Error())
				}
				return
			}

			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if string(result) != tt.responseBody {
				t.Errorf("expected schema body %q, got %q", tt.responseBody, string(result))
			}
		})
	}
}

func TestGetActiveGateGroups(t *testing.T) {
	tests := []struct {
		name          string
		extensionName string
		version       string
		statusCode    int
		response      ActiveGateGroupList
		expectError   bool
		errorContains string
	}{
		{
			name:          "successful get active gate groups",
			extensionName: "com.dynatrace.extension.host-monitoring",
			version:       "1.2.3",
			statusCode:    200,
			response: ActiveGateGroupList{
				Items: []ActiveGateGroupItem{
					{
						GroupName:            "esx-linux-ag",
						AvailableActiveGates: 2,
						ActiveGates: []ActiveGateEntry{
							{ID: 187309619, Errors: json.RawMessage(`[]`)},
							{ID: 1981204261, Errors: json.RawMessage(`[]`)},
						},
					},
				},
			},
		},
		{
			name:          "empty groups",
			extensionName: "com.dynatrace.extension.host-monitoring",
			version:       "1.2.3",
			statusCode:    200,
			response:      ActiveGateGroupList{Items: []ActiveGateGroupItem{}},
		},
		{
			name:          "extension version not found",
			extensionName: "com.dynatrace.extension.host-monitoring",
			version:       "99.99.99",
			statusCode:    404,
			expectError:   true,
			errorContains: "not found",
		},
		{
			name:          "access denied",
			extensionName: "com.dynatrace.extension.host-monitoring",
			version:       "1.0.0",
			statusCode:    403,
			expectError:   true,
			errorContains: "access denied",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				expectedPath := "/platform/extensions/v2/extensions/" + tt.extensionName + "/" + tt.version + "/active-gate-groups"
				if r.URL.Path != expectedPath {
					t.Errorf("unexpected path: %s (expected %s)", r.URL.Path, expectedPath)
					w.WriteHeader(http.StatusNotFound)
					return
				}
				if r.Method != http.MethodGet {
					t.Errorf("unexpected method: %s (expected GET)", r.Method)
					w.WriteHeader(http.StatusMethodNotAllowed)
					return
				}

				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(tt.statusCode)
				if tt.statusCode == 200 {
					json.NewEncoder(w).Encode(tt.response)
				}
			}))
			defer server.Close()

			c, err := client.New(server.URL, "test-token")
			if err != nil {
				t.Fatalf("failed to create client: %v", err)
			}

			handler := NewHandler(c)
			result, err := handler.GetActiveGateGroups(tt.extensionName, tt.version)

			if tt.expectError {
				if err == nil {
					t.Fatal("expected error but got nil")
				}
				if tt.errorContains != "" && !strings.Contains(err.Error(), tt.errorContains) {
					t.Errorf("expected error containing %q, got %q", tt.errorContains, err.Error())
				}
				return
			}

			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if len(result.Items) != len(tt.response.Items) {
				t.Errorf("expected %d groups, got %d", len(tt.response.Items), len(result.Items))
			}
			if len(tt.response.Items) > 0 {
				if result.Items[0].GroupName != tt.response.Items[0].GroupName {
					t.Errorf("expected GroupName %q, got %q", tt.response.Items[0].GroupName, result.Items[0].GroupName)
				}
				if result.Items[0].AvailableActiveGates != tt.response.Items[0].AvailableActiveGates {
					t.Errorf("expected AvailableActiveGates %d, got %d", tt.response.Items[0].AvailableActiveGates, result.Items[0].AvailableActiveGates)
				}
			}
		})
	}
}

func TestSemverGreater(t *testing.T) {
	tests := []struct {
		a, b string
		want bool
	}{
		{"1.2.3", "1.2.2", true},
		{"1.2.2", "1.2.3", false},
		{"2.0.0", "1.9.9", true},
		{"1.0.0", "1.0.0", false},
		{"1.10.0", "1.9.0", true},
		{"1.2", "1.1.99", true},
		{"1.0.0", "0.9.0", true},
	}
	for _, tt := range tests {
		got := SemverGreater(tt.a, tt.b)
		if got != tt.want {
			t.Errorf("SemverGreater(%q, %q) = %v, want %v", tt.a, tt.b, got, tt.want)
		}
	}
}
