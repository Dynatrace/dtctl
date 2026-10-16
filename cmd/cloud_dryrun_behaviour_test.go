package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
	"github.com/stretchr/testify/require"

	"github.com/dynatrace-oss/dtctl/pkg/resources/awsconnection"
	"github.com/dynatrace-oss/dtctl/pkg/resources/awsmonitoringconfig"
	"github.com/dynatrace-oss/dtctl/pkg/resources/azureconnection"
	"github.com/dynatrace-oss/dtctl/pkg/resources/azuremonitoringconfig"
	"github.com/dynatrace-oss/dtctl/pkg/resources/gcpconnection"
	"github.com/dynatrace-oss/dtctl/pkg/resources/gcpmonitoringconfig"
)

// Synthetic fixture identities served by the mock environment. Names and IDs
// are made up; nothing here corresponds to a real environment.
const (
	mockAWSConnectionName   = "dtctl-test-aws"
	mockAzureConnectionName = "dtctl-test-azure"
	mockGCPConnectionName   = "dtctl-test-gcp"

	mockAWSConnectionID   = "aws-connection-object-id"
	mockAzureConnectionID = "azure-connection-object-id"
	mockGCPConnectionID   = "gcp-connection-object-id"

	mockAWSConfigName   = "dtctl-test-aws-monitoring"
	mockAzureConfigName = "dtctl-test-azure-monitoring"
	mockGCPConfigName   = "dtctl-test-gcp-monitoring"

	mockAWSConfigID   = "aws-monitoring-config-id"
	mockAzureConfigID = "azure-monitoring-config-id"
	mockGCPConfigID   = "gcp-monitoring-config-id"

	mockExtensionVersion = "1.2.3"
	mockRoleArn          = "arn:aws:iam::123456789012:role/DynatraceMonitoringRole"
)

// recordingServer is a mock Dynatrace environment that answers the reads the
// hyperscaler commands perform and records every request it receives. Writes
// (POST/PUT/PATCH/DELETE) are answered with 500 rather than a plausible
// success body: a command whose --dry-run guard is missing must fail loudly
// instead of silently "succeeding" against a fake API.
type recordingServer struct {
	*httptest.Server

	mu       sync.Mutex
	requests []string
}

func (s *recordingServer) record(method, path string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.requests = append(s.requests, method+" "+path)
}

// mutatingCalls returns every non-GET request the server saw.
func (s *recordingServer) mutatingCalls() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []string
	for _, req := range s.requests {
		if !strings.HasPrefix(req, http.MethodGet+" ") {
			out = append(out, req)
		}
	}
	return out
}

func (s *recordingServer) reset() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.requests = nil
}

func newCloudMockServer(t *testing.T) *recordingServer {
	t.Helper()

	rs := &recordingServer{}
	mux := http.NewServeMux()

	// Settings objects: the three hyperscaler connection schemas.
	settingsObjects := map[string]map[string]any{
		awsconnection.SchemaID: {
			"objectId": mockAWSConnectionID,
			"schemaId": awsconnection.SchemaID,
			"value": map[string]any{
				"name": mockAWSConnectionName,
				"type": awsconnection.TypeRoleBased,
				"awsRoleBasedAuthentication": map[string]any{
					"roleArn":   mockRoleArn,
					"consumers": []string{awsconnection.DefaultConsumer},
				},
			},
		},
		azureconnection.SchemaID: {
			"objectId": mockAzureConnectionID,
			"schemaId": azureconnection.SchemaID,
			"value": map[string]any{
				"name": mockAzureConnectionName,
				"type": "federatedIdentityCredential",
				"federatedIdentityCredential": map[string]any{
					"directoryId":   "00000000-0000-0000-0000-000000000000",
					"applicationId": "11111111-1111-1111-1111-111111111111",
					"consumers":     []string{"SVC:com.dynatrace.da"},
				},
			},
		},
		gcpconnection.SchemaID: {
			"objectId": mockGCPConnectionID,
			"schemaId": gcpconnection.SchemaID,
			"value": map[string]any{
				"name": mockGCPConnectionName,
				"type": "serviceAccountImpersonation",
				"serviceAccountImpersonation": map[string]any{
					"serviceAccountId": "reader@example-project.iam.gserviceaccount.invalid",
					"consumers":        []string{"SVC:com.dynatrace.da"},
				},
			},
		},
	}

	mux.HandleFunc(awsconnection.SettingsAPI, func(w http.ResponseWriter, r *http.Request) {
		// Settings API constraint guard (see AGENTS.md pagination section).
		if r.URL.Query().Get("nextPageKey") != "" {
			for _, param := range []string{"pageSize", "schemaIds", "scopes", "fields"} {
				if r.URL.Query().Get(param) != "" {
					writeJSONError(w, `{"error":{"code":400,"message":"Constraints violated."}}`)
					return
				}
			}
		}
		items := []map[string]any{}
		if obj, ok := settingsObjects[r.URL.Query().Get("schemaIds")]; ok {
			items = append(items, obj)
		}
		writeJSON(w, map[string]any{"items": items, "totalCount": len(items)})
	})

	mux.HandleFunc(awsconnection.SettingsAPI+"/", func(w http.ResponseWriter, r *http.Request) {
		id := strings.TrimPrefix(r.URL.Path, awsconnection.SettingsAPI+"/")
		for _, obj := range settingsObjects {
			if obj["objectId"] == id {
				writeJSON(w, obj)
				return
			}
		}
		w.WriteHeader(http.StatusNotFound)
		_, _ = fmt.Fprint(w, `{"error":{"code":404,"message":"not found"}}`)
	})

	// Extension monitoring configurations, one extension per cloud.
	type extensionFixture struct {
		name    string
		baseAPI string
		enums   map[string]any
		configs []map[string]any
	}
	extensions := []extensionFixture{
		{
			name:    awsmonitoringconfig.ExtensionName,
			baseAPI: awsmonitoringconfig.BaseAPI,
			enums: map[string]any{
				"dynatrace.datasource.aws:region": enumItems("us-east-1", "eu-central-1"),
				"FeatureSetsType":                 enumItems("EC2_essential", "RDS_essential", "EC2_advanced"),
			},
			configs: []map[string]any{{
				"objectId": mockAWSConfigID,
				"scope":    awsmonitoringconfig.DefaultScope,
				"value": map[string]any{
					"enabled":     false,
					"description": mockAWSConfigName,
					"version":     mockExtensionVersion,
					"featureSets": []string{"EC2_essential"},
					"aws": map[string]any{
						"deploymentRegion": "us-east-1",
						"regionFiltering":  []string{"us-east-1"},
					},
				},
			}},
		},
		{
			name:    azuremonitoringconfig.ExtensionName,
			baseAPI: azuremonitoringconfig.BaseAPI,
			enums: map[string]any{
				"dynatrace.datasource.azure:location": enumItems("eastus", "westeurope"),
				"FeatureSetsType":                     enumItems("microsoft_cache.redis_essential", "microsoft_cache.redis_advanced"),
			},
			configs: []map[string]any{{
				"objectId": mockAzureConfigID,
				"scope":    "integration-azure",
				"value": map[string]any{
					"enabled":     false,
					"description": mockAzureConfigName,
					"version":     mockExtensionVersion,
					"featureSets": []string{"microsoft_cache.redis_essential"},
					"azure": map[string]any{
						"locationFiltering": []string{"eastus"},
					},
				},
			}},
		},
		{
			name:    gcpmonitoringconfig.ExtensionName,
			baseAPI: gcpmonitoringconfig.BaseAPI,
			enums: map[string]any{
				"dynatrace.datasource.gcp:location": enumItems("us-central1", "europe-west1"),
				"FeatureSetsType":                   enumItems("compute_engine_essential", "cloud_run_essential"),
			},
			configs: []map[string]any{{
				"objectId": mockGCPConfigID,
				"scope":    "integration-gcp",
				"value": map[string]any{
					"enabled":     false,
					"description": mockGCPConfigName,
					"version":     mockExtensionVersion,
					"featureSets": []string{"compute_engine_essential"},
					"googleCloud": map[string]any{
						"locationFiltering": []string{"us-central1"},
					},
				},
			}},
		},
	}

	for _, ext := range extensions {
		ext := ext
		extensionAPI := "/platform/extensions/v2/extensions/" + ext.name

		mux.HandleFunc(extensionAPI, func(w http.ResponseWriter, _ *http.Request) {
			writeJSON(w, map[string]any{"items": []map[string]any{
				{"version": "1.0.0"},
				{"version": mockExtensionVersion},
			}})
		})

		mux.HandleFunc(extensionAPI+"/"+mockExtensionVersion+"/schema", func(w http.ResponseWriter, _ *http.Request) {
			writeJSON(w, map[string]any{"enums": ext.enums})
		})

		mux.HandleFunc(ext.baseAPI, func(w http.ResponseWriter, r *http.Request) {
			// Default-style pagination constraint guard (see AGENTS.md).
			if r.URL.Query().Get("page-size") != "" && r.URL.Query().Get("next-page-key") != "" {
				writeJSONError(w, `{"error":{"code":400,"message":"Constraints violated."}}`)
				return
			}
			writeJSON(w, map[string]any{"items": ext.configs})
		})

		mux.HandleFunc(ext.baseAPI+"/", func(w http.ResponseWriter, r *http.Request) {
			id := strings.TrimPrefix(r.URL.Path, ext.baseAPI+"/")
			for _, cfg := range ext.configs {
				if cfg["objectId"] == id {
					writeJSON(w, cfg)
					return
				}
			}
			w.WriteHeader(http.StatusNotFound)
			_, _ = fmt.Fprint(w, `{"error":{"code":404,"message":"not found"}}`)
		})
	}

	rs.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rs.record(r.Method, r.URL.Path)
		if r.Method != http.MethodGet {
			// A missing dry-run guard must surface as a failed command, not as
			// a fake success.
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = fmt.Fprintf(w, `{"error":{"code":500,"message":"%s %s must not be sent during --dry-run"}}`,
				r.Method, r.URL.Path)
			return
		}
		mux.ServeHTTP(w, r)
	}))
	t.Cleanup(rs.Close)
	return rs
}

func enumItems(values ...string) map[string]any {
	items := make([]map[string]any, 0, len(values))
	for _, v := range values {
		items = append(items, map[string]any{"value": v})
	}
	return map[string]any{"items": items}
}

func writeJSON(w http.ResponseWriter, body any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(body)
}

func writeJSONError(w http.ResponseWriter, body string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusBadRequest)
	_, _ = fmt.Fprint(w, body)
}

// resetCloudFlagVars resets every hyperscaler command's cobra flags to their
// default values so one subtest cannot leak state into the next.
func resetCloudFlagVars() {
	resetCmdFlags(
		createAWSConnectionCmd, createAWSMonitoringConfigCmd,
		createAzureConnectionCmd, createAzureMonitoringConfigCmd,
		createGCPConnectionCmd, createGCPMonitoringConfigCmd,
		updateAWSConnectionCmd, updateAWSMonitoringConfigCmd,
		updateAzureConnectionCmd, updateAzureMonitoringConfigCmd,
		updateGCPConnectionCmd, updateGCPMonitoringConfigCmd,
		enableAWSMonitoringCmd, enableAzureMonitoringCmd, enableGCPMonitoringCmd,
		disableAWSMonitoringCmd, disableAzureMonitoringCmd, disableGCPMonitoringCmd,
	)
}

// resetCmdFlags resets every flag on each command to its declared default value.
func resetCmdFlags(cmds ...*cobra.Command) {
	for _, cmd := range cmds {
		cmd.Flags().VisitAll(func(f *pflag.Flag) {
			_ = f.Value.Set(f.DefValue)
		})
	}
}

// TestCloudCommandsDryRunSendsNoMutatingRequest is the behavioural half of
// TestCloudCommandsHonorDryRun, which is an AST check: it pins the shape of the
// code (an `if dryRun` block before every Create/Update call) but not what the
// command actually puts on the wire. A guard placed after a request, or a
// second mutating call the AST walk does not classify, would still pass there.
//
// Here every hyperscaler create/update command runs for real against a mock
// environment with --dry-run set, and the assertion is on the recorded traffic:
// the command must succeed, must report a dry run, and must not have sent a
// single POST/PUT/PATCH/DELETE.
func TestCloudCommandsDryRunSendsNoMutatingRequest(t *testing.T) {
	cases := []struct {
		name  string
		cmd   *cobra.Command
		args  []string
		setup func()
	}{
		{
			name: "create aws connection",
			cmd:  createAWSConnectionCmd,
			setup: func() {
				_ = createAWSConnectionCmd.Flags().Set("name", "new-aws-connection")
				_ = createAWSConnectionCmd.Flags().Set("roleArn", mockRoleArn)
			},
		},
		{
			// --featureSets is left empty on purpose so the command resolves
			// the connection and reads the extension schema before reaching
			// its guard; that is the traffic the guard has to survive.
			name: "create aws monitoring",
			cmd:  createAWSMonitoringConfigCmd,
			setup: func() {
				_ = createAWSMonitoringConfigCmd.Flags().Set("name", "new-aws-monitoring")
				_ = createAWSMonitoringConfigCmd.Flags().Set("credentials", mockAWSConnectionName)
				_ = createAWSMonitoringConfigCmd.Flags().Set("regions", "us-east-1")
			},
		},
		{
			name: "create azure connection",
			cmd:  createAzureConnectionCmd,
			setup: func() {
				_ = createAzureConnectionCmd.Flags().Set("name", "new-azure-connection")
				_ = createAzureConnectionCmd.Flags().Set("type", "federatedIdentityCredential")
			},
		},
		{
			name: "create azure monitoring",
			cmd:  createAzureMonitoringConfigCmd,
			setup: func() {
				_ = createAzureMonitoringConfigCmd.Flags().Set("name", "new-azure-monitoring")
				_ = createAzureMonitoringConfigCmd.Flags().Set("credentials", mockAzureConnectionName)
			},
		},
		{
			name: "create gcp connection",
			cmd:  createGCPConnectionCmd,
			setup: func() {
				_ = createGCPConnectionCmd.Flags().Set("name", "new-gcp-connection")
			},
		},
		{
			name: "create gcp monitoring",
			cmd:  createGCPMonitoringConfigCmd,
			setup: func() {
				_ = createGCPMonitoringConfigCmd.Flags().Set("name", "new-gcp-monitoring")
				_ = createGCPMonitoringConfigCmd.Flags().Set("credentials", mockGCPConnectionName)
			},
		},
		{
			name: "update aws connection",
			cmd:  updateAWSConnectionCmd,
			setup: func() {
				_ = updateAWSConnectionCmd.Flags().Set("name", mockAWSConnectionName)
				_ = updateAWSConnectionCmd.Flags().Set("roleArn", mockRoleArn)
			},
		},
		{
			name: "update aws monitoring",
			cmd:  updateAWSMonitoringConfigCmd,
			setup: func() {
				_ = updateAWSMonitoringConfigCmd.Flags().Set("name", mockAWSConfigName)
				_ = updateAWSMonitoringConfigCmd.Flags().Set("regions", "us-east-1,eu-central-1")
			},
		},
		{
			name: "update azure connection",
			cmd:  updateAzureConnectionCmd,
			setup: func() {
				_ = updateAzureConnectionCmd.Flags().Set("name", mockAzureConnectionName)
				_ = updateAzureConnectionCmd.Flags().Set("applicationId", "22222222-2222-2222-2222-222222222222")
			},
		},
		{
			name: "update azure monitoring",
			cmd:  updateAzureMonitoringConfigCmd,
			setup: func() {
				_ = updateAzureMonitoringConfigCmd.Flags().Set("name", mockAzureConfigName)
				_ = updateAzureMonitoringConfigCmd.Flags().Set("locationFiltering", "eastus,westeurope")
			},
		},
		{
			name: "update gcp connection",
			cmd:  updateGCPConnectionCmd,
			setup: func() {
				_ = updateGCPConnectionCmd.Flags().Set("name", mockGCPConnectionName)
				_ = updateGCPConnectionCmd.Flags().Set("serviceAccountId", "reader@example-project.iam.gserviceaccount.invalid")
			},
		},
		{
			name: "update gcp monitoring",
			cmd:  updateGCPMonitoringConfigCmd,
			setup: func() {
				_ = updateGCPMonitoringConfigCmd.Flags().Set("name", mockGCPConfigName)
				_ = updateGCPMonitoringConfigCmd.Flags().Set("locationFiltering", "us-central1,europe-west1")
			},
		},
	}

	// The command set is fixed; a new cloud create/update command must be
	// added here too.
	require.Len(t, cases, 12, "every hyperscaler create/update command must be covered")

	srv := newCloudMockServer(t)

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			setupPlatformCmdTest(t, srv.Server, "json")

			origDryRun := dryRun(context.Background())
			t.Cleanup(func() {
				gFlags.dryRun = origDryRun
				resetCloudFlagVars()
			})
			resetCloudFlagVars()
			gFlags.dryRun = true
			tc.setup()
			srv.reset()

			var runErr error
			out := capturePlatformStdout(t, func() {
				runErr = tc.cmd.RunE(tc.cmd, tc.args)
			})

			require.NoError(t, runErr, "dtctl %s --dry-run failed; output:\n%s", tc.name, out)
			require.Contains(t, out, "Dry run:",
				"dtctl %s --dry-run returned without reporting a dry run — the guard was never reached", tc.name)
			require.Empty(t, srv.mutatingCalls(),
				"dtctl %s --dry-run sent a mutating request", tc.name)
		})
	}
}
