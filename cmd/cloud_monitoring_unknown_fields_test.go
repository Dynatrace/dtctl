package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"

	"github.com/dynatrace-oss/dtctl/pkg/apply"
	"github.com/dynatrace-oss/dtctl/pkg/client"
	"github.com/dynatrace-oss/dtctl/pkg/resources/awsmonitoringconfig"
	"github.com/dynatrace-oss/dtctl/pkg/resources/azuremonitoringconfig"
	"github.com/dynatrace-oss/dtctl/pkg/resources/gcpmonitoringconfig"
)

// unmodelledCloudFixtures are monitoring configurations that carry every
// property the da-* schemas define and dtctl's typed structs do not model
// (issues #516 and #608), plus an unknown member inside a list element and an
// explicit false/null where the difference to "absent" matters. Values are
// synthetic.
//
// preserved lists the paths every typed read-modify-write must send back
// exactly as the server returned them.
var unmodelledCloudFixtures = []struct {
	cloud     string
	baseAPI   string
	id        string
	name      string
	doc       string
	preserved []string
}{
	{
		cloud:   "aws",
		baseAPI: awsmonitoringconfig.BaseAPI,
		id:      "aws-unmodelled-id",
		name:    "aws-unmodelled",
		doc: `{
			"objectId": "aws-unmodelled-id",
			"scope": "integration-aws",
			"value": {
				"enabled": false,
				"description": "aws-unmodelled",
				"version": "1.2.3",
				"activationContext": "DATA_ACQUISITION",
				"dtAttributes": {"costCenter": "synthetic"},
				"featureSets": ["EC2_essential"],
				"aws": {
					"deploymentRegion": "us-east-1",
					"credentials": [{"description": "c", "enabled": false, "connectionId": "conn-1", "accountId": "123456789012", "futureCredentialField": "kept"}],
					"regionFiltering": ["us-east-1"],
					"eventsConfiguration": {"enabled": true, "regions": ["us-east-1"]},
					"ingestPercentileMetrics": false,
					"ingestS3StorageLENSMetrics": true,
					"automatedDeploymentTemplateVersion": "0.0.1",
					"tenantInstanceId": null
				}
			}
		}`,
		preserved: []string{
			"value.dtAttributes",
			"value.aws.eventsConfiguration",
			"value.aws.ingestPercentileMetrics",
			"value.aws.ingestS3StorageLENSMetrics",
			"value.aws.automatedDeploymentTemplateVersion",
			"value.aws.tenantInstanceId",
			"value.aws.credentials.0.futureCredentialField",
		},
	},
	{
		cloud:   "azure",
		baseAPI: azuremonitoringconfig.BaseAPI,
		id:      "azure-unmodelled-id",
		name:    "azure-unmodelled",
		doc: `{
			"objectId": "azure-unmodelled-id",
			"scope": "integration-azure",
			"value": {
				"enabled": false,
				"description": "azure-unmodelled",
				"version": "1.2.3",
				"activationContext": "DATA_ACQUISITION",
				"dtAttributes": {"costCenter": "synthetic"},
				"featureSets": ["microsoft_cache.redis_essential"],
				"azure": {
					"credentials": [{"enabled": false, "description": "c", "connectionId": "conn-1", "servicePrincipalId": "sp", "type": "FEDERATED", "futureCredentialField": "kept"}],
					"locationFiltering": ["eastus"],
					"subscriptionFiltering": ["00000000-0000-0000-0000-000000000000"],
					"smartscapeConfiguration": {"enabled": false},
					"metricsConfiguration": {"enabled": true},
					"eventHubsConfiguration": {"enabled": false, "namespaces": []},
					"namespaces": [{"namespace": "synthetic", "metrics": []}],
					"manualDeploymentStatus": "NA",
					"deploymentTemplateVersion": "0.0.1",
					"configurationSource": "DTCTL",
					"ands": [],
					"tenantInstanceId": null
				}
			}
		}`,
		preserved: []string{
			"value.activationContext",
			"value.dtAttributes",
			"value.azure.subscriptionFiltering",
			"value.azure.smartscapeConfiguration",
			"value.azure.metricsConfiguration",
			"value.azure.eventHubsConfiguration",
			"value.azure.namespaces",
			"value.azure.manualDeploymentStatus",
			"value.azure.deploymentTemplateVersion",
			"value.azure.configurationSource",
			"value.azure.ands",
			"value.azure.tenantInstanceId",
			"value.azure.credentials.0.futureCredentialField",
		},
	},
	{
		cloud:   "gcp",
		baseAPI: gcpmonitoringconfig.BaseAPI,
		id:      "gcp-unmodelled-id",
		name:    "gcp-unmodelled",
		doc: `{
			"objectId": "gcp-unmodelled-id",
			"scope": "integration-gcp",
			"value": {
				"enabled": false,
				"description": "gcp-unmodelled",
				"version": "1.2.3",
				"activationContext": "DATA_ACQUISITION",
				"dtAttributes": {"costCenter": "synthetic"},
				"featureSets": ["compute_engine_essential"],
				"googleCloud": {
					"credentials": [{"description": "c", "enabled": false, "connectionId": "conn-1", "serviceAccount": "sa@example-project.iam.gserviceaccount.invalid", "futureCredentialField": "kept"}],
					"locationFiltering": ["us-central1"],
					"observabilityScopesEnabled": false,
					"logsConfiguration": {"enabled": true, "locations": [{"location": "us-central1"}]},
					"featureSetConfiguration": {"compute_engine_essential": {"enabled": true}},
					"futureCounter": 9007199254740993,
					"futureRatio": 0.25,
					"tenantInstanceId": null
				}
			}
		}`,
		preserved: []string{
			"value.activationContext",
			"value.dtAttributes",
			"value.googleCloud.logsConfiguration",
			"value.googleCloud.featureSetConfiguration",
			"value.googleCloud.tenantInstanceId",
			"value.googleCloud.observabilityScopesEnabled",
			"value.googleCloud.credentials.0.futureCredentialField",
			"value.googleCloud.futureRatio",
		},
	},
}

// putRecordingServer serves the unmodelled fixtures and records the body of
// every PUT it receives, answering with the stored document.
type putRecordingServer struct {
	*httptest.Server

	mu   sync.Mutex
	puts map[string][]byte // path -> last body
}

func (s *putRecordingServer) lastPutRaw(t *testing.T, path string) []byte {
	t.Helper()
	s.mu.Lock()
	body, ok := s.puts[path]
	s.mu.Unlock()
	if !ok {
		t.Fatalf("no PUT was sent to %s", path)
	}
	return body
}

func (s *putRecordingServer) lastPut(t *testing.T, path string) map[string]any {
	t.Helper()
	body := s.lastPutRaw(t, path)
	var doc map[string]any
	if err := json.Unmarshal(body, &doc); err != nil {
		t.Fatalf("PUT body to %s is not a JSON object: %v\n%s", path, err, body)
	}
	return doc
}

func newPutRecordingServer(t *testing.T) *putRecordingServer {
	t.Helper()
	s := &putRecordingServer{puts: map[string][]byte{}}
	mux := http.NewServeMux()
	for _, fx := range unmodelledCloudFixtures {
		fx := fx
		mux.HandleFunc(fx.baseAPI, func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Query().Get("page-size") != "" && r.URL.Query().Get("next-page-key") != "" {
				writeJSONError(w, `{"error":{"code":400,"message":"Constraints violated."}}`)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = fmt.Fprintf(w, `{"items":[%s]}`, fx.doc)
		})
		mux.HandleFunc(fx.baseAPI+"/"+fx.id, func(w http.ResponseWriter, r *http.Request) {
			if r.Method == http.MethodPut {
				body, _ := io.ReadAll(r.Body)
				s.mu.Lock()
				s.puts[r.URL.Path] = body
				s.mu.Unlock()
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, fx.doc)
		})
	}
	s.Server = httptest.NewServer(mux)
	t.Cleanup(s.Close)
	return s
}

func jsonPath(t *testing.T, doc any, path string) (any, bool) {
	t.Helper()
	node := doc
	for _, seg := range strings.Split(path, ".") {
		switch n := node.(type) {
		case map[string]any:
			v, ok := n[seg]
			if !ok {
				return nil, false
			}
			node = v
		case []any:
			var idx int
			if _, err := fmt.Sscanf(seg, "%d", &idx); err != nil || idx >= len(n) {
				return nil, false
			}
			node = n[idx]
		default:
			return nil, false
		}
	}
	return node, true
}

func assertPreserved(t *testing.T, fixtureDoc string, sent map[string]any, paths []string) {
	t.Helper()
	var server map[string]any
	if err := json.Unmarshal([]byte(fixtureDoc), &server); err != nil {
		t.Fatalf("fixture is not JSON: %v", err)
	}
	for _, p := range paths {
		want, _ := jsonPath(t, server, p)
		got, present := jsonPath(t, sent, p)
		if !present {
			t.Errorf("%s was dropped from the update payload (server had %v)", p, want)
			continue
		}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("%s was altered on the update payload: got %#v, want %#v", p, got, want)
		}
	}
}

// TestCloudMonitoringReadModifyWriteKeepsUnmodelledFields runs every typed
// read-modify-write command (enable, disable, update) against a configuration
// that carries fields dtctl does not model, and asserts the PUT sends each of
// them back unchanged. Before #516/#608 every one of them was dropped — for
// gcp that silently turned log ingestion off.
func TestCloudMonitoringReadModifyWriteKeepsUnmodelledFields(t *testing.T) {
	byCloud := map[string]int{}
	for i, fx := range unmodelledCloudFixtures {
		byCloud[fx.cloud] = i
	}

	cases := []struct {
		name  string
		cloud string
		cmd   *cobra.Command
		setup func(name string)
		check func(t *testing.T, sent map[string]any)
	}{
		{name: "enable aws monitoring", cloud: "aws", cmd: enableAWSMonitoringCmd,
			setup: func(n string) { _ = enableAWSMonitoringCmd.Flags().Set("name", n) },
			check: func(t *testing.T, sent map[string]any) { assertPathEquals(t, sent, "value.enabled", true) }},
		{name: "enable azure monitoring", cloud: "azure", cmd: enableAzureMonitoringCmd,
			setup: func(n string) { _ = enableAzureMonitoringCmd.Flags().Set("name", n) },
			check: func(t *testing.T, sent map[string]any) { assertPathEquals(t, sent, "value.enabled", true) }},
		{name: "enable gcp monitoring", cloud: "gcp", cmd: enableGCPMonitoringCmd,
			setup: func(n string) { _ = enableGCPMonitoringCmd.Flags().Set("name", n) },
			check: func(t *testing.T, sent map[string]any) { assertPathEquals(t, sent, "value.enabled", true) }},
		{name: "disable aws monitoring", cloud: "aws", cmd: disableAWSMonitoringCmd,
			setup: func(n string) { _ = disableAWSMonitoringCmd.Flags().Set("name", n) }},
		{name: "disable azure monitoring", cloud: "azure", cmd: disableAzureMonitoringCmd,
			setup: func(n string) { _ = disableAzureMonitoringCmd.Flags().Set("name", n) }},
		{name: "disable gcp monitoring", cloud: "gcp", cmd: disableGCPMonitoringCmd,
			setup: func(n string) { _ = disableGCPMonitoringCmd.Flags().Set("name", n) }},
		{name: "update aws monitoring", cloud: "aws", cmd: updateAWSMonitoringConfigCmd,
			setup: func(n string) {
				_ = updateAWSMonitoringConfigCmd.Flags().Set("name", n)
				_ = updateAWSMonitoringConfigCmd.Flags().Set("regions", "us-east-1,eu-central-1")
			},
			check: func(t *testing.T, sent map[string]any) {
				assertPathEquals(t, sent, "value.aws.regionFiltering", []any{"us-east-1", "eu-central-1"})
			}},
		{name: "update azure monitoring", cloud: "azure", cmd: updateAzureMonitoringConfigCmd,
			setup: func(n string) {
				_ = updateAzureMonitoringConfigCmd.Flags().Set("name", n)
				_ = updateAzureMonitoringConfigCmd.Flags().Set("locationFiltering", "eastus,westeurope")
			},
			check: func(t *testing.T, sent map[string]any) {
				assertPathEquals(t, sent, "value.azure.locationFiltering", []any{"eastus", "westeurope"})
			}},
		{name: "update gcp monitoring", cloud: "gcp", cmd: updateGCPMonitoringConfigCmd,
			setup: func(n string) {
				_ = updateGCPMonitoringConfigCmd.Flags().Set("name", n)
				_ = updateGCPMonitoringConfigCmd.Flags().Set("locationFiltering", "us-central1,europe-west1")
			},
			check: func(t *testing.T, sent map[string]any) {
				assertPathEquals(t, sent, "value.googleCloud.locationFiltering", []any{"us-central1", "europe-west1"})
			}},
	}

	srv := newPutRecordingServer(t)

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fx := unmodelledCloudFixtures[byCloud[tc.cloud]]
			setupPlatformCmdTest(t, srv.Server, "json")

			origDryRun := dryRun(context.Background())
			t.Cleanup(func() {
				gFlags.dryRun = origDryRun
				resetCloudFlagVars()
			})
			gFlags.dryRun = false
			resetCloudFlagVars()
			tc.setup(fx.name)

			var runErr error
			out := capturePlatformStdout(t, func() {
				runErr = tc.cmd.RunE(tc.cmd, nil)
			})
			if runErr != nil {
				t.Fatalf("%s failed: %v\n%s", tc.name, runErr, out)
			}

			sent := srv.lastPut(t, fx.baseAPI+"/"+fx.id)
			assertPreserved(t, fx.doc, sent, fx.preserved)
			if fx.cloud == "gcp" {
				raw := srv.lastPutRaw(t, fx.baseAPI+"/"+fx.id)
				if !strings.Contains(string(raw), `"futureCounter":9007199254740993`) {
					t.Errorf("large integer was altered on the update payload:\n%s", raw)
				}
			}
			if tc.check != nil {
				tc.check(t, sent)
			}
		})
	}
}

// TestCloudMonitoringGetYAMLApplyKeepsUnmodelledFields covers the
// `get -o yaml` → `apply -f` round trip of #516: the typed configuration is
// rendered the way the YAML printer renders it, read back the way apply reads
// a file, and applied; the resulting PUT must still carry every field.
func TestCloudMonitoringGetYAMLApplyKeepsUnmodelledFields(t *testing.T) {
	srv := newPutRecordingServer(t)
	c, err := client.NewForTesting(srv.URL, "test-token")
	if err != nil {
		t.Fatalf("client.NewForTesting: %v", err)
	}
	c.HTTP().SetRetryCount(0)

	decoders := map[string]func([]byte) (any, error){
		"aws": func(b []byte) (any, error) {
			var v awsmonitoringconfig.AWSMonitoringConfig
			return v, json.Unmarshal(b, &v)
		},
		"azure": func(b []byte) (any, error) {
			var v azuremonitoringconfig.AzureMonitoringConfig
			return v, json.Unmarshal(b, &v)
		},
		"gcp": func(b []byte) (any, error) {
			var v gcpmonitoringconfig.GCPMonitoringConfig
			return v, json.Unmarshal(b, &v)
		},
	}

	for _, fx := range unmodelledCloudFixtures {
		t.Run(fx.cloud, func(t *testing.T) {
			typed, err := decoders[fx.cloud]([]byte(fx.doc))
			if err != nil {
				t.Fatalf("fixture does not decode: %v", err)
			}
			rendered, err := yaml.Marshal(typed)
			if err != nil {
				t.Fatalf("yaml encode: %v", err)
			}

			// The modelled keys keep the reflected lowercase names stable
			// commands have always printed; unmodelled members are added
			// under their API names.
			for _, want := range []string{"objectid: " + fx.id, "description: " + fx.name, "dtAttributes:", "tenantInstanceId: null"} {
				if !strings.Contains(string(rendered), want) {
					t.Errorf("yaml output lacks %q:\n%s", want, rendered)
				}
			}

			results, err := apply.NewApplier(c).Apply(rendered, apply.ApplyOptions{})
			if err != nil {
				t.Fatalf("apply of the exported yaml failed: %v\n%s", err, rendered)
			}
			if len(results) != 1 {
				t.Fatalf("apply returned %d results, want 1", len(results))
			}

			sent := srv.lastPut(t, fx.baseAPI+"/"+fx.id)
			assertPreserved(t, fx.doc, sent, fx.preserved)
			assertNoCaseDuplicates(t, "", sent)

			// An integer above 2^53 must reach the API as the same token; a
			// float64 detour on the YAML path would round it to ...992.
			if fx.cloud == "gcp" {
				raw := srv.lastPutRaw(t, fx.baseAPI+"/"+fx.id)
				if !strings.Contains(string(raw), `"futureCounter":9007199254740993`) {
					t.Errorf("large integer was altered on the get -o yaml -> apply path:\n%s", raw)
				}
			}
		})
	}
}

func assertPathEquals(t *testing.T, doc map[string]any, path string, want any) {
	t.Helper()
	got, ok := jsonPath(t, doc, path)
	if !ok || !reflect.DeepEqual(got, want) {
		t.Errorf("%s = %#v (present=%v), want %#v", path, got, ok, want)
	}
}

// assertNoCaseDuplicates fails when an object carries two keys that differ
// only in case: a lowercase YAML key read back must fill the modelled field,
// not also travel on as an unknown member.
func assertNoCaseDuplicates(t *testing.T, path string, node any) {
	t.Helper()
	switch n := node.(type) {
	case map[string]any:
		seen := map[string]string{}
		for k, v := range n {
			if prev, dup := seen[strings.ToLower(k)]; dup {
				t.Errorf("%s carries both %q and %q", path, prev, k)
			}
			seen[strings.ToLower(k)] = k
			assertNoCaseDuplicates(t, path+"."+k, v)
		}
	case []any:
		for i, v := range n {
			assertNoCaseDuplicates(t, fmt.Sprintf("%s[%d]", path, i), v)
		}
	}
}
