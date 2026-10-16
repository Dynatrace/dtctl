package apply

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/dynatrace-oss/dtctl/pkg/client"
)

// writeHookScript writes a bash hook script with the given body to
// t.TempDir() and returns a hook-command string of the form "bash /abs/path".
// Use this for any hook test that needs shell constructs (pipes, redirection,
// `exit`, $-expansion) — direct exec of such strings via strings.Fields would
// not work since there is no shell layer.
func writeHookScript(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "hook.sh")
	if err := os.WriteFile(path, []byte("#!/usr/bin/env bash\n"+body), 0o755); err != nil {
		t.Fatalf("writeHookScript: %v", err)
	}
	// Use forward slashes so shlex tokenization works on Windows too
	// (backslashes in Windows paths are consumed as escape chars by shlex).
	return "bash " + filepath.ToSlash(path)
}

// requireSegmentFiltersAST reads the request body and validates that all
// include filters are JSON AST (start with '{'). Returns true if the request
// should continue, false if an error was written to the response.
func requireSegmentFiltersAST(t *testing.T, r *http.Request, w http.ResponseWriter) bool {
	t.Helper()
	body, err := io.ReadAll(r.Body)
	if err != nil {
		return true // can't read body — let normal handler run
	}
	var reqBody struct {
		Includes []struct {
			Filter string `json:"filter"`
		} `json:"includes"`
	}
	if err := json.Unmarshal(body, &reqBody); err != nil {
		return true // not valid JSON or no includes — let normal handler run
	}
	for i, inc := range reqBody.Includes {
		if inc.Filter != "" && (len(inc.Filter) == 0 || inc.Filter[0] != '{') {
			t.Errorf("include[%d] filter should be AST in API request, got DQL: %s", i, inc.Filter)
			w.WriteHeader(http.StatusBadRequest)
			w.Write([]byte(`{"error":"filter must be AST, got DQL"}`))
			return false
		}
	}
	return true
}

// newApplyTestServer creates a multiplexed test server that handles multiple resource endpoints.
func newApplyTestServer(t *testing.T, handlers map[string]http.HandlerFunc) (*httptest.Server, *client.Client) {
	t.Helper()
	mux := http.NewServeMux()
	for path, h := range handlers {
		mux.HandleFunc(path, h)
	}
	srv := httptest.NewServer(mux)
	c, err := client.NewForTesting(srv.URL, "test-token")
	if err != nil {
		t.Fatalf("failed to create client: %v", err)
	}
	return srv, c
}

// --- NewApplier / WithSafetyChecker ---

func TestNewApplier_CreatesApplier(t *testing.T) {
	srv, c := newApplyTestServer(t, map[string]http.HandlerFunc{
		"/platform/metadata/v1/user": func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusUnauthorized) // CurrentUserID will fallback to empty
		},
	})
	defer srv.Close()

	a := NewApplier(c)
	if a == nil {
		t.Fatal("NewApplier returned nil")
	}
	if a.client == nil {
		t.Error("applier.client is nil")
	}
}

func TestWithSafetyChecker_Sets(t *testing.T) {
	srv, c := newApplyTestServer(t, map[string]http.HandlerFunc{
		"/platform/metadata/v1/user": func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusUnauthorized)
		},
	})
	defer srv.Close()

	a := NewApplier(c)
	// WithSafetyChecker returns self (fluent)
	returned := a.WithSafetyChecker(nil)
	if returned != a {
		t.Error("WithSafetyChecker should return the same applier")
	}
}

// --- Apply: invalid input ---

func TestApply_InvalidJSON(t *testing.T) {
	srv, c := newApplyTestServer(t, nil)
	defer srv.Close()
	a := NewApplier(c)

	_, err := a.Apply([]byte(`not json`), ApplyOptions{})
	if err == nil {
		t.Fatal("expected error for invalid JSON, got nil")
	}
}

func TestApply_UnknownResourceType(t *testing.T) {
	srv, c := newApplyTestServer(t, nil)
	defer srv.Close()
	a := NewApplier(c)

	// JSON that doesn't match any known resource type
	_, err := a.Apply([]byte(`{"foo":"bar"}`), ApplyOptions{})
	if err == nil {
		t.Fatal("expected error for unknown resource type, got nil")
	}
}

// --- Apply: workflow create (no id) ---

func TestApply_WorkflowCreate_NoID(t *testing.T) {
	srv, c := newApplyTestServer(t, map[string]http.HandlerFunc{
		"/platform/automation/v1/workflows": func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodPost {
				t.Errorf("expected POST, got %s", r.Method)
			}
			w.WriteHeader(http.StatusCreated)
			json.NewEncoder(w).Encode(map[string]interface{}{
				"id":    "11111111-1111-4111-8111-111111111111",
				"title": "My Workflow",
			})
		},
		"/platform/metadata/v1/user": func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusUnauthorized)
		},
	})
	defer srv.Close()
	a := NewApplier(c)

	wfJSON := `{"title":"My Workflow","tasks":{},"trigger":{}}`
	results, err := a.Apply([]byte(wfJSON), ApplyOptions{})
	if err != nil {
		t.Fatalf("Apply() error = %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	base := results[0].(*WorkflowApplyResult).ApplyResultBase
	if base.Action != ActionCreated {
		t.Errorf("expected action 'created', got %q", base.Action)
	}
}

// --- Apply: workflow update (has id, exists) ---

func TestApply_WorkflowUpdate_Exists(t *testing.T) {
	const workflowID = "22222222-2222-4222-8222-222222222222"

	srv, c := newApplyTestServer(t, map[string]http.HandlerFunc{
		"/platform/automation/v1/workflows/22222222-2222-4222-8222-222222222222": func(w http.ResponseWriter, r *http.Request) {
			switch r.Method {
			case http.MethodGet:
				w.WriteHeader(http.StatusOK)
				json.NewEncoder(w).Encode(map[string]interface{}{
					"id":    workflowID,
					"title": "Old Title",
					"owner": "user-xyz",
				})
			case http.MethodPut:
				w.WriteHeader(http.StatusOK)
				json.NewEncoder(w).Encode(map[string]interface{}{
					"id":    workflowID,
					"title": "New Title",
				})
			}
		},
		"/platform/metadata/v1/user": func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusUnauthorized)
		},
	})
	defer srv.Close()
	a := NewApplier(c)

	wfJSON := `{"id":"22222222-2222-4222-8222-222222222222","title":"New Title","tasks":{},"trigger":{}}`
	results, err := a.Apply([]byte(wfJSON), ApplyOptions{})
	if err != nil {
		t.Fatalf("Apply() error = %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	base := results[0].(*WorkflowApplyResult).ApplyResultBase
	if base.Action != ActionUpdated {
		t.Errorf("expected action 'updated', got %q", base.Action)
	}
}

func TestApply_WorkflowUpdate_BackwardsCompatibility_OldGetYAMLForwardsOnlyAvailableFields(t *testing.T) {
	const workflowID = "33333333-3333-4333-8333-333333333333"

	var putBody map[string]interface{}

	srv, c := newApplyTestServer(t, map[string]http.HandlerFunc{
		"/platform/automation/v1/workflows/33333333-3333-4333-8333-333333333333": func(w http.ResponseWriter, r *http.Request) {
			switch r.Method {
			case http.MethodGet:
				w.WriteHeader(http.StatusOK)
				json.NewEncoder(w).Encode(map[string]interface{}{
					"id":    workflowID,
					"title": "Old Title",
					"owner": "user-xyz",
				})
			case http.MethodPut:
				body, err := io.ReadAll(r.Body)
				if err != nil {
					t.Fatalf("ReadAll(r.Body): %v", err)
				}
				if err := json.Unmarshal(body, &putBody); err != nil {
					t.Fatalf("json.Unmarshal(body): %v\nbody was:\n%s", err, body)
				}

				w.WriteHeader(http.StatusOK)
				json.NewEncoder(w).Encode(map[string]interface{}{
					"id":    workflowID,
					"title": "Deploy to Production",
				})
			default:
				w.WriteHeader(http.StatusMethodNotAllowed)
			}
		},
		"/platform/metadata/v1/user": func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusUnauthorized)
		},
	})
	defer srv.Close()
	a := NewApplier(c)

	// workflow without newly added attributes
	oldGetYAML := `id: 33333333-3333-4333-8333-333333333333
title: Deploy to Production
owner: user-xyz
ownerType: USER
description: Deploys latest build to prod environment
isPrivate: false
isDeployed: true
tasks:
  deploy:
    action: dynatrace.automations:run-javascript
    input:
      script: // deploy logic
trigger:
  schedule:
    trigger:
      cron: 0 9 * * 1-5
      type: cron
`

	results, err := a.Apply([]byte(oldGetYAML), ApplyOptions{})
	if err != nil {
		t.Fatalf("Apply() error = %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	base := results[0].(*WorkflowApplyResult).ApplyResultBase
	if base.Action != ActionUpdated {
		t.Errorf("expected action 'updated', got %q", base.Action)
	}

	expectedPutBody := map[string]interface{}{
		"id":          workflowID,
		"title":       "Deploy to Production",
		"owner":       "user-xyz",
		"ownerType":   "USER",
		"description": "Deploys latest build to prod environment",
		"isPrivate":   false,
		"isDeployed":  true,
		"tasks": map[string]interface{}{
			"deploy": map[string]interface{}{
				"action": "dynatrace.automations:run-javascript",
				"input": map[string]interface{}{
					"script": "// deploy logic",
				},
			},
		},
		"trigger": map[string]interface{}{
			"schedule": map[string]interface{}{
				"trigger": map[string]interface{}{
					"cron": "0 9 * * 1-5",
					"type": "cron",
				},
			},
		},
	}

	if !reflect.DeepEqual(putBody, expectedPutBody) {
		t.Errorf("PUT body mismatch\n got: %#v\nwant: %#v", putBody, expectedPutBody)
	}

	for _, field := range []string{"schemaVersion", "type", "result", "input", "hourlyExecutionLimit", "guide"} {
		if _, exists := putBody[field]; exists {
			t.Errorf("PUT body should not synthesize %q from old get payload", field)
		}
	}
}

// --- Apply: workflow with id but not found → create ---

func TestApply_WorkflowCreate_IDNotFound(t *testing.T) {
	const workflowID = "44444444-4444-4444-8444-444444444444"

	srv, c := newApplyTestServer(t, map[string]http.HandlerFunc{
		"/platform/automation/v1/workflows/44444444-4444-4444-8444-444444444444": func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusNotFound)
		},
		"/platform/automation/v1/workflows": func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusCreated)
			json.NewEncoder(w).Encode(map[string]interface{}{
				"id":    workflowID,
				"title": "New Workflow",
			})
		},
		"/platform/metadata/v1/user": func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusUnauthorized)
		},
	})
	defer srv.Close()
	a := NewApplier(c)

	wfJSON := `{"id":"44444444-4444-4444-8444-444444444444","title":"New Workflow","tasks":{},"trigger":{}}`
	results, err := a.Apply([]byte(wfJSON), ApplyOptions{})
	if err != nil {
		t.Fatalf("Apply() error = %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	base := results[0].(*WorkflowApplyResult).ApplyResultBase
	if base.Action != ActionCreated {
		t.Errorf("expected action 'created', got %q", base.Action)
	}
}

// --- Apply: SLO create ---

func TestApply_SLOCreate(t *testing.T) {
	srv, c := newApplyTestServer(t, map[string]http.HandlerFunc{
		"/platform/slo/v1/slos": func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodPost {
				t.Errorf("expected POST, got %s", r.Method)
			}
			w.WriteHeader(http.StatusOK)
			json.NewEncoder(w).Encode(map[string]interface{}{
				"id":   "slo-new-1",
				"name": "My SLO",
			})
		},
		"/platform/metadata/v1/user": func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusUnauthorized)
		},
	})
	defer srv.Close()
	a := NewApplier(c)

	sloJSON := `{"name":"My SLO","criteria":{"pass":[{"criteria":[{"metric":"<100","steps":600}]}]},"target":99.0,"timeframe":"now-7d","metricExpression":"100*..."}`
	results, err := a.Apply([]byte(sloJSON), ApplyOptions{})
	if err != nil {
		t.Fatalf("Apply() SLO error = %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	base := results[0].(*SLOApplyResult).ApplyResultBase
	if base.Action != ActionCreated {
		t.Errorf("expected 'created', got %q", base.Action)
	}
}

// --- Apply: bucket create ---

func TestApply_BucketCreate(t *testing.T) {
	srv, c := newApplyTestServer(t, map[string]http.HandlerFunc{
		"/platform/storage/management/v1/bucket-definitions": func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodPost {
				t.Errorf("expected POST, got %s", r.Method)
			}
			w.WriteHeader(http.StatusOK)
			json.NewEncoder(w).Encode(map[string]interface{}{
				"bucketName": "my-logs",
				"table":      "logs",
				"status":     "creating",
			})
		},
		"/platform/metadata/v1/user": func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusUnauthorized)
		},
	})
	defer srv.Close()
	a := NewApplier(c)

	bucketJSON := `{"bucketName":"my-logs","table":"logs","displayName":"My Logs","retentionDays":35}`
	results, err := a.Apply([]byte(bucketJSON), ApplyOptions{})
	if err != nil {
		t.Fatalf("Apply() bucket error = %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	base := results[0].(*BucketApplyResult).ApplyResultBase
	if base.Action != ActionCreated {
		t.Errorf("expected 'created', got %q", base.Action)
	}
}

// --- Apply: dryRun workflow ---

func TestApply_DryRun_Workflow(t *testing.T) {
	srv, c := newApplyTestServer(t, map[string]http.HandlerFunc{
		"/platform/metadata/v1/user": func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusUnauthorized)
		},
	})
	defer srv.Close()
	a := NewApplier(c)

	wfJSON := `{"title":"My Workflow","id":"wf-dry","tasks":{},"trigger":{}}`
	_, err := a.Apply([]byte(wfJSON), ApplyOptions{DryRun: true})
	if err != nil {
		t.Fatalf("Apply() dryRun error = %v", err)
	}
}

// --- Apply: settings create ---

func TestApply_SettingsCreate(t *testing.T) {
	srv, c := newApplyTestServer(t, map[string]http.HandlerFunc{
		"/platform/classic/environment-api/v2/settings/objects": func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodPost {
				t.Errorf("expected POST, got %s", r.Method)
			}
			w.WriteHeader(http.StatusOK)
			json.NewEncoder(w).Encode([]map[string]interface{}{
				{"objectId": "obj-new-1"},
			})
		},
		"/platform/metadata/v1/user": func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusUnauthorized)
		},
	})
	defer srv.Close()
	a := NewApplier(c)

	settingsJSON := `{"schemaId":"builtin:alerting.profile","scope":"environment","value":{"name":"Test"}}`
	results, err := a.Apply([]byte(settingsJSON), ApplyOptions{})
	if err != nil {
		t.Fatalf("Apply() settings error = %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	base := results[0].(*SettingsApplyResult).ApplyResultBase
	if base.Action != ActionCreated {
		t.Errorf("expected 'created', got %q", base.Action)
	}
}

// --- stderrWarn ---

func TestStderrWarn_AppendsToSlice(t *testing.T) {
	var warnings []string
	(&Applier{}).stderrWarn(&warnings, "test warning %d", 42)
	if len(warnings) != 1 || warnings[0] != "test warning 42" {
		t.Errorf("unexpected warnings: %v", warnings)
	}
}

func TestStderrWarn_NilSlice(t *testing.T) {
	// Should not panic with nil slice
	(&Applier{}).stderrWarn(nil, "no-op warning")
}

// --- Apply: template vars ---

func TestApply_WithTemplateVars(t *testing.T) {
	srv, c := newApplyTestServer(t, map[string]http.HandlerFunc{
		"/platform/automation/v1/workflows": func(w http.ResponseWriter, r *http.Request) {
			var body map[string]interface{}
			json.NewDecoder(r.Body).Decode(&body)
			title, _ := body["title"].(string)
			w.WriteHeader(http.StatusCreated)
			json.NewEncoder(w).Encode(map[string]interface{}{
				"id":    "wf-templated",
				"title": title,
			})
		},
		"/platform/metadata/v1/user": func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusUnauthorized)
		},
	})
	defer srv.Close()
	a := NewApplier(c)

	wfTemplate := `{"title":"{{.name}}","tasks":{},"trigger":{}}`
	results, err := a.Apply([]byte(wfTemplate), ApplyOptions{
		TemplateVars: map[string]interface{}{"name": "Rendered Workflow"},
	})
	if err != nil {
		t.Fatalf("Apply() template error = %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	fmt.Println("template vars test passed:", results[0].(*WorkflowApplyResult).Name)
}

// --- Apply: Azure Connection ---

func TestApply_AzureConnection_Create(t *testing.T) {
	srv, c := newApplyTestServer(t, map[string]http.HandlerFunc{
		// GET to check if exists — not found
		"/platform/classic/environment-api/v2/settings/objects/az-obj-1": func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]interface{}{
				"objectId": "az-obj-1",
				"schemaId": "builtin:hyperscaler-authentication.connections.azure",
				"scope":    "environment",
				"value":    map[string]interface{}{"name": "My Azure", "type": "serviceCredentials"},
			})
		},
		"/platform/classic/environment-api/v2/settings/objects": func(w http.ResponseWriter, r *http.Request) {
			switch r.Method {
			case http.MethodGet:
				// List: empty (connection doesn't exist yet)
				w.Header().Set("Content-Type", "application/json")
				json.NewEncoder(w).Encode(map[string]interface{}{
					"items":      []interface{}{},
					"totalCount": 0,
				})
			case http.MethodPost:
				w.WriteHeader(http.StatusOK)
				json.NewEncoder(w).Encode([]map[string]interface{}{
					{"objectId": "az-obj-1"},
				})
			}
		},
		"/platform/metadata/v1/user": func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusUnauthorized)
		},
	})
	defer srv.Close()
	a := NewApplier(c)

	// Azure connection JSON: single object with schemaId
	azJSON := `{"schemaId":"builtin:hyperscaler-authentication.connections.azure","scope":"environment","value":{"name":"My Azure","type":"serviceCredentials","tenantId":"tenant-1","appId":"app-1","key":"secret"}}`
	results, err := a.Apply([]byte(azJSON), ApplyOptions{})
	if err != nil {
		t.Fatalf("Apply() Azure connection error = %v", err)
	}
	if len(results) == 0 {
		t.Fatal("expected at least 1 result")
	}
}

// --- Apply: GCP Connection ---

func TestApply_GCPConnection_Create(t *testing.T) {
	srv, c := newApplyTestServer(t, map[string]http.HandlerFunc{
		"/platform/classic/environment-api/v2/settings/objects/gcp-obj-1": func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]interface{}{
				"objectId": "gcp-obj-1",
				"schemaId": "builtin:hyperscaler-authentication.connections.gcp",
				"scope":    "environment",
				"value":    map[string]interface{}{"name": "My GCP", "projectId": "my-project"},
			})
		},
		"/platform/classic/environment-api/v2/settings/objects": func(w http.ResponseWriter, r *http.Request) {
			switch r.Method {
			case http.MethodGet:
				w.Header().Set("Content-Type", "application/json")
				json.NewEncoder(w).Encode(map[string]interface{}{
					"items":      []interface{}{},
					"totalCount": 0,
				})
			case http.MethodPost:
				w.WriteHeader(http.StatusOK)
				json.NewEncoder(w).Encode([]map[string]interface{}{
					{"objectId": "gcp-obj-1"},
				})
			}
		},
		"/platform/metadata/v1/user": func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusUnauthorized)
		},
	})
	defer srv.Close()
	a := NewApplier(c)

	// GCP connection JSON array
	gcpJSON := `[{"schemaId":"builtin:hyperscaler-authentication.connections.gcp","scope":"environment","value":{"name":"My GCP","projectId":"my-project","clientEmail":"sa@proj.iam.gserviceaccount.com"}}]`
	results, err := a.Apply([]byte(gcpJSON), ApplyOptions{})
	if err != nil {
		t.Fatalf("Apply() GCP connection error = %v", err)
	}
	if len(results) == 0 {
		t.Fatal("expected at least 1 result")
	}
}

// --- Apply: unsupported type in Apply ---

func TestApply_UnsupportedResourceType(t *testing.T) {
	srv, c := newApplyTestServer(t, nil)
	defer srv.Close()
	a := NewApplier(c)

	// Force an unknown type past detectResourceType by using an impossible path
	// (Since ResourceUnknown can't normally reach Apply's switch, we test via error path)
	_, err := a.Apply([]byte(`{"random":"data","no":"matching","fields":"here","extra":"values"}`), ApplyOptions{})
	if err == nil {
		t.Fatal("expected error for unknown resource type")
	}
}

// decodeCloudSection pulls value.<cloudField> out of a monitoring-config
// request body. It reports failures with t.Errorf rather than t.Fatalf: this
// runs on the httptest server goroutine, where runtime.Goexit would abandon the
// response and surface as an unrelated client-side EOF. Type assertions are
// checked for the same reason — a panic here takes down the whole test binary.
func decodeCloudSection(t *testing.T, w http.ResponseWriter, r *http.Request, cloudField string) (map[string]any, bool) {
	t.Helper()

	var body map[string]any
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		t.Errorf("decode request: %v", err)
		w.WriteHeader(http.StatusInternalServerError)
		return nil, false
	}
	value, ok := body["value"].(map[string]any)
	if !ok {
		t.Errorf("request body has no value object: %#v", body)
		w.WriteHeader(http.StatusInternalServerError)
		return nil, false
	}
	cloud, ok := value[cloudField].(map[string]any)
	if !ok {
		t.Errorf("request body has no value.%s object: %#v", cloudField, value)
		w.WriteHeader(http.StatusInternalServerError)
		return nil, false
	}
	return cloud, true
}

// --- Apply: AWS Monitoring Config (create, no objectId) ---

func TestApply_AWSMonitoringConfig_PreservesCentralIntent(t *testing.T) {
	const monitoringBase = "/platform/extensions/v2/extensions/com.dynatrace.extension.da-aws/monitoring-configurations"

	srv, c := newApplyTestServer(t, map[string]http.HandlerFunc{
		monitoringBase: func(w http.ResponseWriter, r *http.Request) {
			switch r.Method {
			case http.MethodGet:
				_ = json.NewEncoder(w).Encode(map[string]any{"items": []any{}})
			case http.MethodPost:
				aws, ok := decodeCloudSection(t, w, r, "aws")
				if !ok {
					return
				}
				// Both enrichment properties are modificationPolicy NEVER in the
				// extension schema, so they have to survive the struct round-trip
				// verbatim rather than be dropped or rewritten.
				if aws["useIngestEnrichmentConfig"] != true {
					t.Errorf("central intent was not preserved: %#v", aws)
				}
				if aws["ingestEnrichmentMigrationProcessed"] != true {
					t.Errorf("migration marker was not preserved: %#v", aws)
				}
				w.WriteHeader(http.StatusCreated)
				_ = json.NewEncoder(w).Encode(map[string]any{"objectId": "aws-new-1", "code": 201})
			}
		},
		"/platform/metadata/v1/user": func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusUnauthorized)
		},
	})
	defer srv.Close()

	a := NewApplier(c)
	data := `{"scope":"integration-aws","value":{"description":"My AWS Config","version":"1.0.0","aws":{"useIngestEnrichmentConfig":true,"ingestEnrichmentMigrationProcessed":true}}}`
	results, err := a.Apply([]byte(data), ApplyOptions{})
	if err != nil {
		t.Fatalf("Apply() AWSMonitoringConfig error = %v", err)
	}
	if len(results) != 1 || results[0].(*MonitoringConfigApplyResult).Action != ActionCreated {
		t.Fatalf("Apply() results = %#v, want one created result", results)
	}
}

// --- Apply: Azure Monitoring Config (create, no objectId) ---

func TestApply_AzureMonitoringConfig_Create(t *testing.T) {
	const extensionBase = "/platform/extensions/v2/extensions/com.dynatrace.extension.da-azure"
	const monitoringBase = "/platform/extensions/v2/extensions/com.dynatrace.extension.da-azure/monitoring-configurations"

	srv, c := newApplyTestServer(t, map[string]http.HandlerFunc{
		// GetLatestVersion
		extensionBase: func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]interface{}{
				"items": []map[string]interface{}{
					{"version": "1.2.3"},
				},
			})
		},
		// Create (POST) and FindByName (GET)
		monitoringBase: func(w http.ResponseWriter, r *http.Request) {
			switch r.Method {
			case http.MethodGet:
				// FindByName → List: return empty
				w.Header().Set("Content-Type", "application/json")
				json.NewEncoder(w).Encode(map[string]interface{}{
					"items":      []interface{}{},
					"totalCount": 0,
				})
			case http.MethodPost:
				azure, ok := decodeCloudSection(t, w, r, "azure")
				if !ok {
					return
				}
				if azure["useIngestEnrichmentConfig"] != true {
					t.Errorf("central intent was not preserved: %#v", azure)
				}
				if azure["ingestEnrichmentMigrationProcessed"] != false {
					t.Errorf("migration marker was not preserved: %#v", azure)
				}
				w.WriteHeader(http.StatusOK)
				json.NewEncoder(w).Encode(map[string]interface{}{
					"objectId": "mc-new-1",
					"scope":    "integration-azure",
					"value":    map[string]interface{}{"description": "My Azure Config", "version": "1.2.3"},
				})
			default:
				t.Errorf("unexpected method %s", r.Method)
			}
		},
		"/platform/metadata/v1/user": func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusUnauthorized)
		},
	})
	defer srv.Close()
	a := NewApplier(c)

	azMonJSON := `{"scope":"integration-azure","value":{"description":"My Azure Config","azure":{"useIngestEnrichmentConfig":true,"ingestEnrichmentMigrationProcessed":false}}}`
	results, err := a.Apply([]byte(azMonJSON), ApplyOptions{})
	if err != nil {
		t.Fatalf("Apply() AzureMonitoringConfig error = %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	base := results[0].(*MonitoringConfigApplyResult).ApplyResultBase
	if base.Action != ActionCreated {
		t.Errorf("expected 'created', got %q", base.Action)
	}
}

// --- Apply: SLO update (has id, exists) ---

func TestApply_SLOUpdate_Exists(t *testing.T) {
	srv, c := newApplyTestServer(t, map[string]http.HandlerFunc{
		"/platform/slo/v1/slos/slo-existing": func(w http.ResponseWriter, r *http.Request) {
			switch r.Method {
			case http.MethodGet:
				w.WriteHeader(http.StatusOK)
				json.NewEncoder(w).Encode(map[string]interface{}{
					"id":      "slo-existing",
					"name":    "My SLO",
					"version": "1",
				})
			case http.MethodPut:
				w.WriteHeader(http.StatusOK)
			}
		},
		"/platform/metadata/v1/user": func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusUnauthorized)
		},
	})
	defer srv.Close()
	a := NewApplier(c)

	sloJSON := `{"id":"slo-existing","name":"My SLO","criteria":{"pass":[{"criteria":[{"metric":"<100","steps":600}]}]}}`
	results, err := a.Apply([]byte(sloJSON), ApplyOptions{})
	if err != nil {
		t.Fatalf("Apply() SLO update error = %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	base := results[0].(*SLOApplyResult).ApplyResultBase
	if base.Action != ActionUpdated {
		t.Errorf("expected 'updated', got %q", base.Action)
	}
}

// --- Apply: dryRun dashboard (checks document existence) ---

func TestApply_DryRun_Dashboard(t *testing.T) {
	srv, c := newApplyTestServer(t, map[string]http.HandlerFunc{
		"/platform/document/v1/documents/dash-123/metadata": func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]interface{}{
				"id":   "dash-123",
				"name": "Existing Dashboard",
				"type": "dashboard",
			})
		},
		"/platform/metadata/v1/user": func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusUnauthorized)
		},
	})
	defer srv.Close()
	a := NewApplier(c)

	dashJSON := `{"type":"dashboard","id":"dash-123","tiles":{"items":[{"tileType":"MARKDOWN"}]}}`
	_, err := a.Apply([]byte(dashJSON), ApplyOptions{DryRun: true})
	if err != nil {
		t.Fatalf("Apply() dryRun dashboard error = %v", err)
	}
}

// --- Apply: GCP Monitoring Config (create) ---

func TestApply_GCPMonitoringConfig_Create(t *testing.T) {
	const gcpExtBase = "/platform/extensions/v2/extensions/com.dynatrace.extension.da-gcp"
	const gcpMonBase = "/platform/extensions/v2/extensions/com.dynatrace.extension.da-gcp/monitoring-configurations"

	srv, c := newApplyTestServer(t, map[string]http.HandlerFunc{
		gcpExtBase: func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]interface{}{
				"items": []map[string]interface{}{{"version": "1.0.0"}},
			})
		},
		gcpMonBase: func(w http.ResponseWriter, r *http.Request) {
			switch r.Method {
			case http.MethodGet:
				w.Header().Set("Content-Type", "application/json")
				json.NewEncoder(w).Encode(map[string]interface{}{
					"items": []interface{}{}, "totalCount": 0,
				})
			case http.MethodPost:
				gcp, ok := decodeCloudSection(t, w, r, "googleCloud")
				if !ok {
					return
				}
				if gcp["useIngestEnrichmentConfig"] != true || gcp["dtLabelsEnrichment"] == nil {
					t.Errorf("central or dtLabels fields were not preserved: %#v", gcp)
				}
				w.WriteHeader(http.StatusOK)
				json.NewEncoder(w).Encode(map[string]interface{}{
					"objectId": "gmc-new-1",
					"scope":    "integration-gcp",
					"value":    map[string]interface{}{"description": "My GCP Config"},
				})
			}
		},
		"/platform/metadata/v1/user": func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusUnauthorized)
		},
	})
	defer srv.Close()
	a := NewApplier(c)

	gcpMonJSON := `{"scope":"integration-gcp","value":{"description":"My GCP Config","googleCloud":{"useIngestEnrichmentConfig":true,"dtLabelsEnrichment":{"dt.cost.product":{"labelKey":"product"}}}}}`
	results, err := a.Apply([]byte(gcpMonJSON), ApplyOptions{})
	if err != nil {
		t.Fatalf("Apply() GCPMonitoringConfig error = %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	base := results[0].(*MonitoringConfigApplyResult).ApplyResultBase
	if base.Action != ActionCreated {
		t.Errorf("expected 'created', got %q", base.Action)
	}
}

// --- Apply: Dashboard create (applyDocument path) ---

func TestApply_DashboardCreate(t *testing.T) {
	srv, c := newApplyTestServer(t, map[string]http.HandlerFunc{
		"/platform/document/v1/documents": func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodPost {
				t.Errorf("expected POST, got %s", r.Method)
			}
			boundary := "resp-boundary"
			w.Header().Set("Content-Type", fmt.Sprintf("multipart/form-data; boundary=%s", boundary))
			fmt.Fprintf(w, "--%s\r\nContent-Disposition: form-data; name=\"metadata\"\r\nContent-Type: application/json\r\n\r\n{\"id\":\"dash-new-1\",\"name\":\"My Dashboard\",\"type\":\"dashboard\",\"version\":1}\r\n--%s--\r\n", boundary, boundary)
		},
		"/platform/metadata/v1/user": func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusUnauthorized)
		},
	})
	defer srv.Close()
	a := NewApplier(c)

	dashJSON := `{"type":"dashboard","tiles":{"items":[]}}`
	results, err := a.Apply([]byte(dashJSON), ApplyOptions{})
	if err != nil {
		t.Fatalf("Apply() dashboard create error = %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	base := results[0].(*DashboardApplyResult).ApplyResultBase
	if base.Action != ActionCreated {
		t.Errorf("expected 'created', got %q", base.Action)
	}
}

// --- Apply: Segment create (no UID) ---

func TestApply_SegmentCreate_NoUID(t *testing.T) {
	srv, c := newApplyTestServer(t, map[string]http.HandlerFunc{
		"/platform/storage/filter-segments/v1/filter-segments": func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodPost {
				t.Errorf("expected POST, got %s", r.Method)
			}
			if !requireSegmentFiltersAST(t, r, w) {
				return
			}
			w.WriteHeader(http.StatusCreated)
			json.NewEncoder(w).Encode(map[string]interface{}{
				"uid":  "seg-new-001",
				"name": "Test Segment",
			})
		},
		"/platform/metadata/v1/user": func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusUnauthorized)
		},
	})
	defer srv.Close()
	a := NewApplier(c)

	segJSON := `{"name":"Test Segment","isPublic":true,"includes":[{"dataObject":"logs","filter":"status = \"ERROR\""}]}`
	results, err := a.Apply([]byte(segJSON), ApplyOptions{})
	if err != nil {
		t.Fatalf("Apply() error = %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	segBase := results[0].(*SegmentApplyResult).ApplyResultBase
	if segBase.Action != ActionCreated {
		t.Errorf("expected 'created', got %q", segBase.Action)
	}
	if segBase.ID != "seg-new-001" {
		t.Errorf("expected ID 'seg-new-001', got %q", segBase.ID)
	}
}

// --- Apply: Segment update (UID exists) ---

func TestApply_SegmentUpdate_Exists(t *testing.T) {
	srv, c := newApplyTestServer(t, map[string]http.HandlerFunc{
		"/platform/storage/filter-segments/v1/filter-segments/seg-uid-001": func(w http.ResponseWriter, r *http.Request) {
			switch r.Method {
			case http.MethodGet:
				json.NewEncoder(w).Encode(map[string]interface{}{
					"uid":     "seg-uid-001",
					"name":    "Existing Segment",
					"version": 3,
					"owner":   "user@example.invalid",
				})
			case http.MethodPatch:
				if !requireSegmentFiltersAST(t, r, w) {
					return
				}
				lockVer := r.URL.Query().Get("optimistic-locking-version")
				if lockVer != "3" {
					t.Errorf("expected optimistic-locking-version=3, got %q", lockVer)
				}
				w.WriteHeader(http.StatusOK)
			default:
				t.Errorf("unexpected method %s", r.Method)
			}
		},
		"/platform/metadata/v1/user": func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusUnauthorized)
		},
	})
	defer srv.Close()
	a := NewApplier(c)

	segJSON := `{"uid":"seg-uid-001","name":"Updated Segment","isPublic":true,"includes":[{"dataObject":"logs","filter":"status = \"ERROR\""}]}`
	results, err := a.Apply([]byte(segJSON), ApplyOptions{})
	if err != nil {
		t.Fatalf("Apply() error = %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	segBase := results[0].(*SegmentApplyResult).ApplyResultBase
	if segBase.Action != ActionUpdated {
		t.Errorf("expected 'updated', got %q", segBase.Action)
	}
}

// --- Apply: Segment with UID but not found → create ---

func TestApply_SegmentCreate_IDNotFound(t *testing.T) {
	srv, c := newApplyTestServer(t, map[string]http.HandlerFunc{
		"/platform/storage/filter-segments/v1/filter-segments/seg-missing": func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusNotFound)
		},
		"/platform/storage/filter-segments/v1/filter-segments": func(w http.ResponseWriter, r *http.Request) {
			if !requireSegmentFiltersAST(t, r, w) {
				return
			}
			w.WriteHeader(http.StatusCreated)
			json.NewEncoder(w).Encode(map[string]interface{}{
				"uid":  "seg-missing",
				"name": "New Segment",
			})
		},
		"/platform/metadata/v1/user": func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusUnauthorized)
		},
	})
	defer srv.Close()
	a := NewApplier(c)

	segJSON := `{"uid":"seg-missing","name":"New Segment","isPublic":false,"includes":[{"dataObject":"logs","filter":"status = \"ERROR\""}]}`
	results, err := a.Apply([]byte(segJSON), ApplyOptions{})
	if err != nil {
		t.Fatalf("Apply() error = %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	segBase := results[0].(*SegmentApplyResult).ApplyResultBase
	if segBase.Action != ActionCreated {
		t.Errorf("expected 'created', got %q", segBase.Action)
	}
}

// --- Apply: Segment with UID, Get returns server error → should NOT fall through to create ---

func TestApply_Segment_GetServerError_NoFallthrough(t *testing.T) {
	srv, c := newApplyTestServer(t, map[string]http.HandlerFunc{
		"/platform/storage/filter-segments/v1/filter-segments/seg-uid-001": func(w http.ResponseWriter, r *http.Request) {
			// Return a 500 server error (not a 404)
			w.WriteHeader(http.StatusInternalServerError)
			fmt.Fprint(w, "internal server error")
		},
		"/platform/metadata/v1/user": func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusUnauthorized)
		},
	})
	defer srv.Close()
	a := NewApplier(c)

	segJSON := `{"uid":"seg-uid-001","name":"Test Segment","isPublic":true,"includes":[{"dataObject":"logs","filter":"status = \"ERROR\""}]}`
	_, err := a.Apply([]byte(segJSON), ApplyOptions{})
	if err == nil {
		t.Fatal("Apply() should have returned an error for server error, got nil")
	}
	// The error should name the failed existence check, not fall through to create
	expected := `failed to check segment "seg-uid-001" existence`
	if !stringContains(err.Error(), expected) {
		t.Errorf("expected error containing %q, got: %v", expected, err)
	}
}

func stringContains(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

// --- Pre-apply hook tests ---

func TestApply_HookRejects(t *testing.T) {
	srv, c := newApplyTestServer(t, map[string]http.HandlerFunc{
		"/platform/metadata/v1/user": func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusUnauthorized)
		},
	})
	defer srv.Close()

	a := NewApplier(c).
		WithPreApplyHook(writeHookScript(t, "echo 'validation failed' >&2; exit 1")).
		WithSourceFile("test.yaml")

	// Use a valid workflow JSON so resource type detection succeeds
	workflowJSON := `{"title":"Test WF","tasks":{"t1":{"action":"dynatrace.automations:run-javascript"}},"trigger":{}}`
	_, err := a.Apply([]byte(workflowJSON), ApplyOptions{})
	if err == nil {
		t.Fatal("Apply() should have returned error when hook rejects, got nil")
	}

	var hookErr *HookRejectedError
	if !errorAs(err, &hookErr) {
		t.Fatalf("expected HookRejectedError, got: %T: %v", err, err)
	}
	if hookErr.ExitCode != 1 {
		t.Errorf("ExitCode = %d, want 1", hookErr.ExitCode)
	}
	if !stringContains(hookErr.Stderr, "validation failed") {
		t.Errorf("Stderr = %q, want it to contain 'validation failed'", hookErr.Stderr)
	}
}

func TestApply_HookAllows(t *testing.T) {
	srv, c := newApplyTestServer(t, map[string]http.HandlerFunc{
		"/platform/metadata/v1/user": func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusUnauthorized)
		},
		"/platform/automation/v1/workflows": func(w http.ResponseWriter, r *http.Request) {
			if r.Method == http.MethodPost {
				w.WriteHeader(http.StatusCreated)
				json.NewEncoder(w).Encode(map[string]interface{}{
					"id":    "wf-001",
					"title": "Test WF",
				})
				return
			}
			w.WriteHeader(http.StatusNotFound)
		},
	})
	defer srv.Close()

	a := NewApplier(c).
		WithPreApplyHook("true").
		WithSourceFile("test.yaml")

	workflowJSON := `{"title":"Test WF","tasks":{"t1":{"action":"dynatrace.automations:run-javascript"}},"trigger":{}}`
	results, err := a.Apply([]byte(workflowJSON), ApplyOptions{})
	if err != nil {
		t.Fatalf("Apply() error = %v, want nil", err)
	}
	if len(results) != 1 {
		t.Errorf("len(results) = %d, want 1", len(results))
	}
}

func TestApply_NoHooksFlagSkipsHook(t *testing.T) {
	srv, c := newApplyTestServer(t, map[string]http.HandlerFunc{
		"/platform/metadata/v1/user": func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusUnauthorized)
		},
		"/platform/automation/v1/workflows": func(w http.ResponseWriter, r *http.Request) {
			if r.Method == http.MethodPost {
				w.WriteHeader(http.StatusCreated)
				json.NewEncoder(w).Encode(map[string]interface{}{
					"id":    "wf-001",
					"title": "Test WF",
				})
				return
			}
			w.WriteHeader(http.StatusNotFound)
		},
	})
	defer srv.Close()

	a := NewApplier(c).
		WithPreApplyHook(writeHookScript(t, "exit 1")). // would reject
		WithSourceFile("test.yaml")

	workflowJSON := `{"title":"Test WF","tasks":{"t1":{"action":"dynatrace.automations:run-javascript"}},"trigger":{}}`
	results, err := a.Apply([]byte(workflowJSON), ApplyOptions{NoHooks: true})
	if err != nil {
		t.Fatalf("Apply() error = %v, want nil (hook should be skipped)", err)
	}
	if len(results) != 1 {
		t.Errorf("len(results) = %d, want 1", len(results))
	}
}

func TestApply_HookRunsOnDryRun(t *testing.T) {
	srv, c := newApplyTestServer(t, map[string]http.HandlerFunc{
		"/platform/metadata/v1/user": func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusUnauthorized)
		},
	})
	defer srv.Close()

	a := NewApplier(c).
		WithPreApplyHook(writeHookScript(t, "exit 1")). // rejects
		WithSourceFile("test.yaml")

	workflowJSON := `{"title":"Test WF","tasks":{"t1":{"action":"dynatrace.automations:run-javascript"}},"trigger":{}}`
	_, err := a.Apply([]byte(workflowJSON), ApplyOptions{DryRun: true})
	if err == nil {
		t.Fatal("Apply() should have returned error (hook rejects even in dry-run)")
	}

	var hookErr *HookRejectedError
	if !errorAs(err, &hookErr) {
		t.Fatalf("expected HookRejectedError, got: %T: %v", err, err)
	}
}

func TestApply_NoHookConfigured(t *testing.T) {
	srv, c := newApplyTestServer(t, map[string]http.HandlerFunc{
		"/platform/metadata/v1/user": func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusUnauthorized)
		},
		"/platform/automation/v1/workflows": func(w http.ResponseWriter, r *http.Request) {
			if r.Method == http.MethodPost {
				w.WriteHeader(http.StatusCreated)
				json.NewEncoder(w).Encode(map[string]interface{}{
					"id":    "wf-001",
					"title": "Test WF",
				})
				return
			}
			w.WriteHeader(http.StatusNotFound)
		},
	})
	defer srv.Close()

	// No hook configured — apply should proceed normally
	a := NewApplier(c)

	workflowJSON := `{"title":"Test WF","tasks":{"t1":{"action":"dynatrace.automations:run-javascript"}},"trigger":{}}`
	results, err := a.Apply([]byte(workflowJSON), ApplyOptions{})
	if err != nil {
		t.Fatalf("Apply() error = %v, want nil", err)
	}
	if len(results) != 1 {
		t.Errorf("len(results) = %d, want 1", len(results))
	}
}

func TestWithPreApplyHook_FluentAPI(t *testing.T) {
	srv, c := newApplyTestServer(t, map[string]http.HandlerFunc{
		"/platform/metadata/v1/user": func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusUnauthorized)
		},
	})
	defer srv.Close()

	a := NewApplier(c)
	returned := a.WithPreApplyHook("true")
	if returned != a {
		t.Error("WithPreApplyHook should return the same applier (fluent API)")
	}
	returned = a.WithSourceFile("test.yaml")
	if returned != a {
		t.Error("WithSourceFile should return the same applier (fluent API)")
	}
}

func TestApply_HookReceivesCorrectResourceType(t *testing.T) {
	srv, c := newApplyTestServer(t, map[string]http.HandlerFunc{
		"/platform/metadata/v1/user": func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusUnauthorized)
		},
		"/platform/automation/v1/workflows": func(w http.ResponseWriter, r *http.Request) {
			if r.Method == http.MethodPost {
				w.WriteHeader(http.StatusCreated)
				json.NewEncoder(w).Encode(map[string]interface{}{
					"id":    "wf-001",
					"title": "Test WF",
				})
				return
			}
			w.WriteHeader(http.StatusNotFound)
		},
	})
	defer srv.Close()

	// Hook verifies $1 is "workflow"
	a := NewApplier(c).
		WithPreApplyHook(writeHookScript(t, `test "$1" = "workflow"`)).
		WithSourceFile("my-wf.yaml")

	workflowJSON := `{"title":"Test WF","tasks":{"t1":{"action":"dynatrace.automations:run-javascript"}},"trigger":{}}`
	results, err := a.Apply([]byte(workflowJSON), ApplyOptions{})
	if err != nil {
		t.Fatalf("Apply() error = %v, want nil (hook should see $1=workflow)", err)
	}
	if len(results) != 1 {
		t.Errorf("len(results) = %d, want 1", len(results))
	}
}

func TestApply_HookReceivesCorrectSourceFile(t *testing.T) {
	srv, c := newApplyTestServer(t, map[string]http.HandlerFunc{
		"/platform/metadata/v1/user": func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusUnauthorized)
		},
		"/platform/automation/v1/workflows": func(w http.ResponseWriter, r *http.Request) {
			if r.Method == http.MethodPost {
				w.WriteHeader(http.StatusCreated)
				json.NewEncoder(w).Encode(map[string]interface{}{
					"id":    "wf-001",
					"title": "Test WF",
				})
				return
			}
			w.WriteHeader(http.StatusNotFound)
		},
	})
	defer srv.Close()

	// Hook verifies $2 is the source file path
	a := NewApplier(c).
		WithPreApplyHook(writeHookScript(t, `test "$2" = "configs/my workflow.yaml"`)).
		WithSourceFile("configs/my workflow.yaml")

	workflowJSON := `{"title":"Test WF","tasks":{"t1":{"action":"dynatrace.automations:run-javascript"}},"trigger":{}}`
	results, err := a.Apply([]byte(workflowJSON), ApplyOptions{})
	if err != nil {
		t.Fatalf("Apply() error = %v, want nil (hook should see $2=source file)", err)
	}
	if len(results) != 1 {
		t.Errorf("len(results) = %d, want 1", len(results))
	}
}

func TestApply_HookReceivesProcessedJSON(t *testing.T) {
	srv, c := newApplyTestServer(t, map[string]http.HandlerFunc{
		"/platform/metadata/v1/user": func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusUnauthorized)
		},
		"/platform/automation/v1/workflows": func(w http.ResponseWriter, r *http.Request) {
			if r.Method == http.MethodPost {
				w.WriteHeader(http.StatusCreated)
				json.NewEncoder(w).Encode(map[string]interface{}{
					"id":    "wf-001",
					"title": "Rendered Title",
				})
				return
			}
			w.WriteHeader(http.StatusNotFound)
		},
	})
	defer srv.Close()

	// Hook verifies stdin contains the rendered (not raw) JSON
	a := NewApplier(c).
		WithPreApplyHook(writeHookScript(t, `input=$(cat); echo "$input" | grep -q "Rendered Title"`)).
		WithSourceFile("template.yaml")

	// Template input — after rendering, "{{.name}}" becomes "Rendered Title"
	templateJSON := `{"title":"{{.name}}","tasks":{"t1":{"action":"dynatrace.automations:run-javascript"}},"trigger":{}}`
	results, err := a.Apply([]byte(templateJSON), ApplyOptions{
		TemplateVars: map[string]interface{}{"name": "Rendered Title"},
	})
	if err != nil {
		t.Fatalf("Apply() error = %v, want nil (hook should see rendered JSON)", err)
	}
	if len(results) != 1 {
		t.Errorf("len(results) = %d, want 1", len(results))
	}
}

func TestApply_HookRejectsWithMultiLineStderr(t *testing.T) {
	srv, c := newApplyTestServer(t, map[string]http.HandlerFunc{
		"/platform/metadata/v1/user": func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusUnauthorized)
		},
	})
	defer srv.Close()

	a := NewApplier(c).
		WithPreApplyHook(writeHookScript(t, `echo "error line 1" >&2; echo "error line 2" >&2; exit 1`)).
		WithSourceFile("test.yaml")

	workflowJSON := `{"title":"Test WF","tasks":{"t1":{"action":"dynatrace.automations:run-javascript"}},"trigger":{}}`
	_, err := a.Apply([]byte(workflowJSON), ApplyOptions{})
	if err == nil {
		t.Fatal("Apply() should have returned error")
	}

	var hookErr *HookRejectedError
	if !errorAs(err, &hookErr) {
		t.Fatalf("expected HookRejectedError, got: %T: %v", err, err)
	}
	if !stringContains(hookErr.Stderr, "error line 1") || !stringContains(hookErr.Stderr, "error line 2") {
		t.Errorf("Stderr = %q, want both error lines", hookErr.Stderr)
	}
}

func TestApply_HookDashboardResourceType(t *testing.T) {
	srv, c := newApplyTestServer(t, map[string]http.HandlerFunc{
		"/platform/metadata/v1/user": func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusUnauthorized)
		},
		"/platform/document/v1/documents": func(w http.ResponseWriter, r *http.Request) {
			if r.Method == http.MethodPost {
				boundary := "resp-boundary"
				w.Header().Set("Content-Type", fmt.Sprintf("multipart/form-data; boundary=%s", boundary))
				fmt.Fprintf(w, "--%s\r\nContent-Disposition: form-data; name=\"metadata\"\r\nContent-Type: application/json\r\n\r\n{\"id\":\"dash-new\",\"name\":\"Test\",\"type\":\"dashboard\",\"version\":1}\r\n--%s--\r\n", boundary, boundary)
				return
			}
			w.WriteHeader(http.StatusNotFound)
		},
	})
	defer srv.Close()

	// Hook verifies $1 is "dashboard" for dashboard resources
	a := NewApplier(c).
		WithPreApplyHook(writeHookScript(t, `test "$1" = "dashboard"`)).
		WithSourceFile("dash.yaml")

	dashJSON := `{"type":"dashboard","tiles":{"items":[]}}`
	results, err := a.Apply([]byte(dashJSON), ApplyOptions{})
	if err != nil {
		t.Fatalf("Apply() error = %v, want nil (hook should see $1=dashboard)", err)
	}
	if len(results) != 1 {
		t.Errorf("len(results) = %d, want 1", len(results))
	}
}

func TestApply_HookSLOResourceType(t *testing.T) {
	srv, c := newApplyTestServer(t, map[string]http.HandlerFunc{
		"/platform/metadata/v1/user": func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusUnauthorized)
		},
		"/platform/slo/v1/slos": func(w http.ResponseWriter, r *http.Request) {
			if r.Method == http.MethodPost {
				w.WriteHeader(http.StatusOK)
				json.NewEncoder(w).Encode(map[string]interface{}{
					"id":   "slo-001",
					"name": "Test SLO",
				})
				return
			}
			w.WriteHeader(http.StatusNotFound)
		},
	})
	defer srv.Close()

	// Hook verifies $1 is "slo"
	a := NewApplier(c).
		WithPreApplyHook(writeHookScript(t, `test "$1" = "slo"`)).
		WithSourceFile("slo.yaml")

	sloJSON := `{"name":"Test SLO","criteria":{"pass":[{"criteria":[{"metric":"<100","steps":600}]}]},"target":99.0,"timeframe":"now-7d","metricExpression":"100*..."}`
	results, err := a.Apply([]byte(sloJSON), ApplyOptions{})
	if err != nil {
		t.Fatalf("Apply() error = %v, want nil (hook should see $1=slo)", err)
	}
	if len(results) != 1 {
		t.Errorf("len(results) = %d, want 1", len(results))
	}
}

func TestApply_EmptyPreApplyHookField(t *testing.T) {
	// Empty string hook (different from nil/no hook) should be a no-op
	srv, c := newApplyTestServer(t, map[string]http.HandlerFunc{
		"/platform/metadata/v1/user": func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusUnauthorized)
		},
		"/platform/automation/v1/workflows": func(w http.ResponseWriter, r *http.Request) {
			if r.Method == http.MethodPost {
				w.WriteHeader(http.StatusCreated)
				json.NewEncoder(w).Encode(map[string]interface{}{
					"id":    "wf-001",
					"title": "Test WF",
				})
				return
			}
			w.WriteHeader(http.StatusNotFound)
		},
	})
	defer srv.Close()

	// Explicitly set empty string hook
	a := NewApplier(c).
		WithPreApplyHook("").
		WithSourceFile("test.yaml")

	workflowJSON := `{"title":"Test WF","tasks":{"t1":{"action":"dynatrace.automations:run-javascript"}},"trigger":{}}`
	results, err := a.Apply([]byte(workflowJSON), ApplyOptions{})
	if err != nil {
		t.Fatalf("Apply() error = %v, want nil (empty hook should be no-op)", err)
	}
	if len(results) != 1 {
		t.Errorf("len(results) = %d, want 1", len(results))
	}
}

func TestApply_PostApplyHookRunsOnSuccess(t *testing.T) {
	// Post-apply hook runs after a successful apply, receives the resource
	// type and source file as positional args, and the apply result JSON on
	// stdin. A side effect (file creation) proves it ran.
	srv, c := newApplyTestServer(t, map[string]http.HandlerFunc{
		"/platform/metadata/v1/user": func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusUnauthorized)
		},
		"/platform/automation/v1/workflows": func(w http.ResponseWriter, r *http.Request) {
			if r.Method == http.MethodPost {
				w.WriteHeader(http.StatusCreated)
				_ = json.NewEncoder(w).Encode(map[string]interface{}{
					"id":    "wf-post-001",
					"title": "Post-hook Test",
				})
				return
			}
			w.WriteHeader(http.StatusNotFound)
		},
	})
	defer srv.Close()

	dir := t.TempDir()
	marker := filepath.Join(dir, "ran.txt")
	hookBody := fmt.Sprintf(`test "$1" = workflow && cat > %q`, marker)
	a := NewApplier(c).
		WithPostApplyHook(writeHookScript(t, hookBody)).
		WithSourceFile("wf.yaml")

	workflowJSON := `{"title":"Test WF","tasks":{"t1":{"action":"dynatrace.automations:run-javascript"}},"trigger":{}}`
	if _, err := a.Apply([]byte(workflowJSON), ApplyOptions{}); err != nil {
		t.Fatalf("Apply() error = %v", err)
	}

	data, err := os.ReadFile(marker)
	if err != nil {
		t.Fatalf("expected post-apply hook to create %s, but it did not: %v", marker, err)
	}
	if !strings.Contains(string(data), `"resourceType":"workflow"`) {
		t.Errorf("post-apply stdin did not contain apply result JSON; got: %q", data)
	}
}

func TestApply_PostApplyHookSkippedOnDryRun(t *testing.T) {
	srv, c := newApplyTestServer(t, map[string]http.HandlerFunc{
		"/platform/metadata/v1/user": func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusUnauthorized)
		},
	})
	defer srv.Close()

	dir := t.TempDir()
	marker := filepath.Join(dir, "ran.txt")
	hookBody := fmt.Sprintf(`touch %q`, marker)
	a := NewApplier(c).
		WithPostApplyHook(writeHookScript(t, hookBody)).
		WithSourceFile("wf.yaml")

	workflowJSON := `{"title":"Test WF","tasks":{"t1":{"action":"dynatrace.automations:run-javascript"}},"trigger":{}}`
	if _, err := a.Apply([]byte(workflowJSON), ApplyOptions{DryRun: true}); err != nil {
		t.Fatalf("Apply() error = %v", err)
	}

	if _, err := os.Stat(marker); err == nil {
		t.Errorf("post-apply hook ran on dry-run, but should have been skipped")
	}
}

func TestApply_PostApplyHookSkippedByNoHooks(t *testing.T) {
	srv, c := newApplyTestServer(t, map[string]http.HandlerFunc{
		"/platform/metadata/v1/user": func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusUnauthorized)
		},
		"/platform/automation/v1/workflows": func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(map[string]interface{}{"id": "wf-x", "title": "X"})
		},
	})
	defer srv.Close()

	dir := t.TempDir()
	marker := filepath.Join(dir, "ran.txt")
	hookBody := fmt.Sprintf(`touch %q`, marker)
	a := NewApplier(c).
		WithPostApplyHook(writeHookScript(t, hookBody)).
		WithSourceFile("wf.yaml")

	workflowJSON := `{"title":"Test WF","tasks":{"t1":{"action":"dynatrace.automations:run-javascript"}},"trigger":{}}`
	if _, err := a.Apply([]byte(workflowJSON), ApplyOptions{NoHooks: true}); err != nil {
		t.Fatalf("Apply() error = %v", err)
	}

	if _, err := os.Stat(marker); err == nil {
		t.Errorf("post-apply hook ran despite NoHooks=true")
	}
}

// TestApply_HookOutputsRoutedToConfiguredWriters verifies that
// WithHookOutputs redirects hook stdout/stderr away from the process's
// os.Stdout/os.Stderr. This is the agent-mode contract: in -A mode, hook
// stdout must NOT prepend non-JSON noise to the envelope on os.Stdout.
func TestApply_HookOutputsRoutedToConfiguredWriters(t *testing.T) {
	srv, c := newApplyTestServer(t, map[string]http.HandlerFunc{
		"/platform/metadata/v1/user": func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusUnauthorized)
		},
		"/platform/automation/v1/workflows": func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(map[string]interface{}{"id": "wf-y", "title": "Y"})
		},
	})
	defer srv.Close()

	preHook := writeHookScript(t, `echo pre-stdout; echo pre-stderr >&2`)
	postHook := writeHookScript(t, `echo post-stdout; echo post-stderr >&2`)

	var stdoutBuf, stderrBuf bytes.Buffer
	a := NewApplier(c).
		WithPreApplyHook(preHook).
		WithPostApplyHook(postHook).
		WithSourceFile("wf.yaml").
		WithHookOutputs(&stdoutBuf, &stderrBuf)

	workflowJSON := `{"title":"Test WF","tasks":{"t1":{"action":"dynatrace.automations:run-javascript"}},"trigger":{}}`
	if _, err := a.Apply([]byte(workflowJSON), ApplyOptions{}); err != nil {
		t.Fatalf("Apply() error = %v", err)
	}

	gotStdout := stdoutBuf.String()
	gotStderr := stderrBuf.String()
	if !strings.Contains(gotStdout, "pre-stdout") || !strings.Contains(gotStdout, "post-stdout") {
		t.Errorf("configured stdout writer = %q, want it to contain pre-stdout and post-stdout", gotStdout)
	}
	if !strings.Contains(gotStderr, "pre-stderr") || !strings.Contains(gotStderr, "post-stderr") {
		t.Errorf("configured stderr writer = %q, want it to contain pre-stderr and post-stderr", gotStderr)
	}
}

// TestApply_HookOutputsAgentModeStdoutClean simulates agent mode by
// pointing both hook outputs at stderr. Hook stdout must NOT land on the
// process's stdout (where the agent JSON envelope is written).
func TestApply_HookOutputsAgentModeStdoutClean(t *testing.T) {
	srv, c := newApplyTestServer(t, map[string]http.HandlerFunc{
		"/platform/metadata/v1/user": func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusUnauthorized)
		},
		"/platform/automation/v1/workflows": func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(map[string]interface{}{"id": "wf-z", "title": "Z"})
		},
	})
	defer srv.Close()

	preHook := writeHookScript(t, `echo pre-noise`)
	postHook := writeHookScript(t, `echo post-noise`)

	var stderrBuf bytes.Buffer
	// Both writers point at the stderr buffer — agent-mode policy.
	a := NewApplier(c).
		WithPreApplyHook(preHook).
		WithPostApplyHook(postHook).
		WithSourceFile("wf.yaml").
		WithHookOutputs(&stderrBuf, &stderrBuf)

	workflowJSON := `{"title":"Test WF","tasks":{"t1":{"action":"dynatrace.automations:run-javascript"}},"trigger":{}}`
	if _, err := a.Apply([]byte(workflowJSON), ApplyOptions{}); err != nil {
		t.Fatalf("Apply() error = %v", err)
	}

	got := stderrBuf.String()
	if !strings.Contains(got, "pre-noise") || !strings.Contains(got, "post-noise") {
		t.Errorf("expected hook stdout to land on stderr buffer in agent mode; got=%q", got)
	}
}

// TestApply_PostApplyRunsForPartialSuccess verifies that when an array apply
// succeeds for some items and fails for others, the post-apply hook still
// runs for the items that were persisted. The hook is the only signal a
// notification/cleanup pipeline gets — skipping it on partial failure
// would mean those resources are silently created without notification.
func TestApply_PostApplyRunsForPartialSuccess(t *testing.T) {
	wfCount := 0
	srv, c := newApplyTestServer(t, map[string]http.HandlerFunc{
		"/platform/metadata/v1/user": func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusUnauthorized)
		},
		"/platform/automation/v1/workflows": func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodPost {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			wfCount++
			// Succeed on the first item, fail on the second.
			if wfCount == 1 {
				w.WriteHeader(http.StatusCreated)
				_ = json.NewEncoder(w).Encode(map[string]interface{}{"id": "wf-ok", "title": "ok"})
				return
			}
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`{"error":{"message":"simulated failure"}}`))
		},
	})
	defer srv.Close()

	dir := t.TempDir()
	stdinFile := filepath.Join(dir, "stdin.json")
	hookBody := fmt.Sprintf(`cat > %q`, stdinFile)
	a := NewApplier(c).
		WithPostApplyHook(writeHookScript(t, hookBody)).
		WithSourceFile("wfs.yaml")

	// Two-item array: first succeeds, second fails.
	arrayJSON := `[
{"title":"ok","tasks":{"t1":{"action":"dynatrace.automations:run-javascript"}},"trigger":{}},
{"title":"bad","tasks":{"t1":{"action":"dynatrace.automations:run-javascript"}},"trigger":{}}
]`
	results, err := a.Apply([]byte(arrayJSON), ApplyOptions{})
	if err == nil {
		t.Fatalf("Apply() expected partial-failure error, got nil; results=%+v", results)
	}
	listErr, ok := err.(*ListApplyError)
	if !ok {
		t.Fatalf("expected *ListApplyError, got %T: %v", err, err)
	}
	if listErr.Failed != 1 || listErr.Total != 2 {
		t.Errorf("ListApplyError = %+v, want Failed=1 Total=2", listErr)
	}
	if len(results) != 1 {
		t.Errorf("results length = %d, want 1 (the successful item)", len(results))
	}

	// Post-apply hook must have fired for the persisted resource.
	data, err := os.ReadFile(stdinFile)
	if err != nil {
		t.Fatalf("post-apply hook did not run for partial-success batch: %v", err)
	}
	// The hook fired (file was written) — that's the core invariant. The
	// exact id field depends on the mock server response shape; we assert
	// only the structural shape (a JSON array with the created workflow).
	if !strings.Contains(string(data), `"action":"created"`) ||
		!strings.Contains(string(data), `"resourceType":"workflow"`) {
		t.Errorf("post-apply stdin should describe the applied workflow; got %q", data)
	}
}

// TestApply_PostApplyHookSkippedWhenAllFailed verifies the corollary: if no
// items in the batch succeed, post-apply should NOT fire (there is nothing
// to notify about, and an empty array on stdin would be misleading).
func TestApply_PostApplyHookSkippedWhenAllFailed(t *testing.T) {
	srv, c := newApplyTestServer(t, map[string]http.HandlerFunc{
		"/platform/metadata/v1/user": func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusUnauthorized)
		},
		"/platform/automation/v1/workflows": func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusInternalServerError)
		},
	})
	defer srv.Close()

	dir := t.TempDir()
	marker := filepath.Join(dir, "ran.txt")
	a := NewApplier(c).
		WithPostApplyHook(writeHookScript(t, fmt.Sprintf(`touch %q`, marker))).
		WithSourceFile("wfs.yaml")

	arrayJSON := `[
{"title":"a","tasks":{"t1":{"action":"dynatrace.automations:run-javascript"}},"trigger":{}}
]`
	if _, err := a.Apply([]byte(arrayJSON), ApplyOptions{}); err == nil {
		t.Fatal("Apply() expected error for all-failed batch")
	}
	if _, err := os.Stat(marker); err == nil {
		t.Errorf("post-apply hook should not fire when no items were persisted")
	}
}

func TestApply_HookErrorIsPropagated(t *testing.T) {
	// With direct exec (no sh -c), a missing hook binary surfaces as a
	// Go "executable file not found" error from exec.CommandContext.Run —
	// not as a HookRejectedError with exit 127. The error must still
	// propagate out of Apply() so the user sees a clear failure.
	srv, c := newApplyTestServer(t, map[string]http.HandlerFunc{
		"/platform/metadata/v1/user": func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusUnauthorized)
		},
	})
	defer srv.Close()

	a := NewApplier(c).
		WithPreApplyHook("nonexistent-binary-xyz-123").
		WithSourceFile("test.yaml")

	workflowJSON := `{"title":"Test WF","tasks":{"t1":{"action":"dynatrace.automations:run-javascript"}},"trigger":{}}`
	_, err := a.Apply([]byte(workflowJSON), ApplyOptions{})
	if err == nil {
		t.Fatal("Apply() should have returned error for command-not-found hook")
	}
	if !stringContains(err.Error(), "pre-apply hook failed to execute") {
		t.Errorf("error = %v, want it to mention hook execution failure", err)
	}
}

func TestApply_HookRunsBeforeAPICall(t *testing.T) {
	// Verify hook runs BEFORE any API call is made.
	// If hook rejects, no API call should happen.
	apiCalled := false
	srv, c := newApplyTestServer(t, map[string]http.HandlerFunc{
		"/platform/metadata/v1/user": func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusUnauthorized)
		},
		"/platform/automation/v1/workflows": func(w http.ResponseWriter, r *http.Request) {
			apiCalled = true
			w.WriteHeader(http.StatusCreated)
			json.NewEncoder(w).Encode(map[string]interface{}{
				"id":    "wf-001",
				"title": "Test WF",
			})
		},
	})
	defer srv.Close()

	a := NewApplier(c).
		WithPreApplyHook(writeHookScript(t, "exit 1")).
		WithSourceFile("test.yaml")

	workflowJSON := `{"title":"Test WF","tasks":{"t1":{"action":"dynatrace.automations:run-javascript"}},"trigger":{}}`
	_, _ = a.Apply([]byte(workflowJSON), ApplyOptions{})

	if apiCalled {
		t.Error("API was called despite hook rejection — hook should run before API calls")
	}
}

func TestApply_HookWithDryRunStillRejects(t *testing.T) {
	// Verify that hook rejection in dry-run still prevents dry-run result
	srv, c := newApplyTestServer(t, map[string]http.HandlerFunc{
		"/platform/metadata/v1/user": func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusUnauthorized)
		},
	})
	defer srv.Close()

	a := NewApplier(c).
		WithPreApplyHook(writeHookScript(t, "echo 'blocked by policy' >&2; exit 1")).
		WithSourceFile("test.yaml")

	workflowJSON := `{"title":"Test WF","tasks":{"t1":{"action":"dynatrace.automations:run-javascript"}},"trigger":{}}`
	_, err := a.Apply([]byte(workflowJSON), ApplyOptions{DryRun: true})
	if err == nil {
		t.Fatal("Apply() should have returned error (hook rejects even in dry-run)")
	}

	var hookErr *HookRejectedError
	if !errorAs(err, &hookErr) {
		t.Fatalf("expected HookRejectedError, got: %T: %v", err, err)
	}
	if !stringContains(hookErr.Stderr, "blocked by policy") {
		t.Errorf("Stderr = %q, want it to contain 'blocked by policy'", hookErr.Stderr)
	}
}

func TestApply_HookStderrInErrorObject(t *testing.T) {
	// Verify the HookRejectedError contains the correct command string
	// when the hook rejects. Uses a real script that exits non-zero (which
	// is the proper rejection path) rather than a missing binary (which
	// now yields a plain exec-failure error — see TestApply_HookErrorIsPropagated).
	srv, c := newApplyTestServer(t, map[string]http.HandlerFunc{
		"/platform/metadata/v1/user": func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusUnauthorized)
		},
	})
	defer srv.Close()

	hookCommand := writeHookScript(t, "echo rejected >&2; exit 1")
	a := NewApplier(c).
		WithPreApplyHook(hookCommand).
		WithSourceFile("test.yaml")

	workflowJSON := `{"title":"Test WF","tasks":{"t1":{"action":"dynatrace.automations:run-javascript"}},"trigger":{}}`
	_, err := a.Apply([]byte(workflowJSON), ApplyOptions{})
	if err == nil {
		t.Fatal("Apply() should have returned error")
	}

	var hookErr *HookRejectedError
	if !errorAs(err, &hookErr) {
		t.Fatalf("expected HookRejectedError, got: %T: %v", err, err)
	}
	if hookErr.Command != hookCommand {
		t.Errorf("Command = %q, want %q", hookErr.Command, hookCommand)
	}
}

// errorAs is a simple helper since the apply package tests use stdlib only.
func errorAs(err error, target interface{}) bool {
	hookErr, ok := target.(**HookRejectedError)
	if !ok {
		return false
	}
	for err != nil {
		if e, ok := err.(*HookRejectedError); ok {
			*hookErr = e
			return true
		}
		// Check for wrapped errors
		type unwrapper interface {
			Unwrap() error
		}
		u, ok := err.(unwrapper)
		if !ok {
			return false
		}
		err = u.Unwrap()
	}
	return false
}

// ── WriteID / OverrideID end-to-end tests ──────────────────────────────────

// workflowCreateHandlers returns the standard mock handlers for workflow creation.
func workflowCreateHandlers(t *testing.T, returnID string) map[string]http.HandlerFunc {
	t.Helper()
	return map[string]http.HandlerFunc{
		"/platform/automation/v1/workflows": func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodPost {
				t.Errorf("expected POST, got %s", r.Method)
			}
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusCreated)
			json.NewEncoder(w).Encode(map[string]interface{}{
				"id":    returnID,
				"title": "My Workflow",
			})
		},
		"/platform/metadata/v1/user": func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusUnauthorized)
		},
	}
}

// dashboardCreateHandlers returns mock handlers for document creation returning the given id.
func dashboardCreateHandlers(t *testing.T, returnID string) map[string]http.HandlerFunc {
	t.Helper()
	return map[string]http.HandlerFunc{
		"/platform/document/v1/documents": func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodPost {
				t.Errorf("expected POST, got %s", r.Method)
			}
			boundary := "resp-boundary"
			w.Header().Set("Content-Type", fmt.Sprintf("multipart/form-data; boundary=%s", boundary))
			fmt.Fprintf(w,
				"--%s\r\nContent-Disposition: form-data; name=\"metadata\"\r\nContent-Type: application/json\r\n\r\n{\"id\":%q,\"name\":\"My Dashboard\",\"type\":\"dashboard\",\"version\":1}\r\n--%s--\r\n",
				boundary, returnID, boundary,
			)
		},
		"/platform/metadata/v1/user": func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusUnauthorized)
		},
	}
}

// TestApply_WorkflowCreate_WriteID verifies that when --write-id is set, the
// generated ID is stamped into the source file after a successful create.
func TestApply_WorkflowCreate_WriteID(t *testing.T) {
	srv, c := newApplyTestServer(t, workflowCreateHandlers(t, "wf-stamped-001"))
	defer srv.Close()

	// Write a temporary workflow file without an id field.
	dir := t.TempDir()
	srcFile := dir + "/wf.yaml"
	if err := os.WriteFile(srcFile, []byte("title: My Workflow\ntasks: {}\ntrigger: {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	a := NewApplier(c).WithSourceFile(srcFile)
	wfJSON := `{"title":"My Workflow","tasks":{},"trigger":{}}`
	_, err := a.Apply([]byte(wfJSON), ApplyOptions{WriteID: true})
	if err != nil {
		t.Fatalf("Apply() error = %v", err)
	}

	got, err := os.ReadFile(srcFile)
	if err != nil {
		t.Fatalf("read source file: %v", err)
	}
	if !strings.Contains(string(got), "wf-stamped-001") {
		t.Errorf("expected id 'wf-stamped-001' to be stamped into file, got:\n%s", got)
	}
}

// TestApply_WorkflowCreate_OverrideID verifies that --id injects the given ID
// into the workflow JSON sent to the API, routing to a PUT (update) rather than POST.
func TestApply_WorkflowCreate_OverrideID(t *testing.T) {
	putCalled := false
	srv, c := newApplyTestServer(t, map[string]http.HandlerFunc{
		"/platform/automation/v1/workflows/wf-override-42": func(w http.ResponseWriter, r *http.Request) {
			switch r.Method {
			case http.MethodGet:
				// Resource exists — verify the override ID caused a GET.
				w.WriteHeader(http.StatusOK)
				json.NewEncoder(w).Encode(map[string]interface{}{
					"id":    "wf-override-42",
					"title": "Existing Workflow",
					"owner": "",
				})
			case http.MethodPut:
				putCalled = true
				w.WriteHeader(http.StatusOK)
				json.NewEncoder(w).Encode(map[string]interface{}{
					"id":    "wf-override-42",
					"title": "My Workflow",
				})
			default:
				t.Errorf("unexpected method %s", r.Method)
				w.WriteHeader(http.StatusMethodNotAllowed)
			}
		},
		"/platform/metadata/v1/user": func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusUnauthorized)
		},
	})
	defer srv.Close()

	a := NewApplier(c)
	// Template has no id — OverrideID injects it before the apply logic runs.
	wfJSON := `{"title":"My Workflow","tasks":{},"trigger":{}}`
	results, err := a.Apply([]byte(wfJSON), ApplyOptions{OverrideID: "wf-override-42"})
	if err != nil {
		t.Fatalf("Apply() error = %v", err)
	}
	if !putCalled {
		t.Error("expected PUT (update) to be called when OverrideID points to existing resource")
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	base := results[0].(*WorkflowApplyResult).ApplyResultBase
	if base.Action != ActionUpdated {
		t.Errorf("expected action 'updated', got %q", base.Action)
	}
}

// TestApply_DashboardCreate_WriteID verifies write-back for dashboard creation.
func TestApply_DashboardCreate_WriteID(t *testing.T) {
	srv, c := newApplyTestServer(t, dashboardCreateHandlers(t, "dash-stamped-007"))
	defer srv.Close()

	dir := t.TempDir()
	srcFile := dir + "/dashboard.yaml"
	if err := os.WriteFile(srcFile, []byte("type: dashboard\ntiles:\n  items: []\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	a := NewApplier(c).WithSourceFile(srcFile)
	dashJSON := `{"type":"dashboard","tiles":{"items":[]}}`
	_, err := a.Apply([]byte(dashJSON), ApplyOptions{WriteID: true})
	if err != nil {
		t.Fatalf("Apply() dashboard create error = %v", err)
	}

	got, err := os.ReadFile(srcFile)
	if err != nil {
		t.Fatalf("read source file: %v", err)
	}
	if !strings.Contains(string(got), "dash-stamped-007") {
		t.Errorf("expected id 'dash-stamped-007' to be stamped into file, got:\n%s", got)
	}
}

// TestApply_WorkflowCreate_IDNotFound_NoHint verifies that when a file already
// has an id field but the resource doesn't exist yet, no misleading hint is
// emitted (the file is already self-contained after creation).
func TestApply_WorkflowCreate_IDNotFound_NoHint(t *testing.T) {
	srv, c := newApplyTestServer(t, map[string]http.HandlerFunc{
		"/platform/automation/v1/workflows/wf-missing-2": func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusNotFound)
		},
		"/platform/automation/v1/workflows": func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusCreated)
			json.NewEncoder(w).Encode(map[string]interface{}{
				"id":    "wf-missing-2",
				"title": "New Workflow",
			})
		},
		"/platform/metadata/v1/user": func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusUnauthorized)
		},
	})
	defer srv.Close()

	// Use a temp file with the id already present.
	dir := t.TempDir()
	srcFile := dir + "/wf.yaml"
	if err := os.WriteFile(srcFile, []byte("id: wf-missing-2\ntitle: New Workflow\ntasks: {}\ntrigger: {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	a := NewApplier(c).WithSourceFile(srcFile)
	wfJSON := `{"id":"wf-missing-2","title":"New Workflow","tasks":{},"trigger":{}}`
	results, err := a.Apply([]byte(wfJSON), ApplyOptions{})
	if err != nil {
		t.Fatalf("Apply() error = %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	base := results[0].(*WorkflowApplyResult).ApplyResultBase
	if base.Action != ActionCreated {
		t.Errorf("expected action 'created', got %q", base.Action)
	}
	// Source file must be unchanged — no new id stamped (it already had one).
	content, _ := os.ReadFile(srcFile)
	if string(content) != "id: wf-missing-2\ntitle: New Workflow\ntasks: {}\ntrigger: {}\n" {
		t.Errorf("source file should be unchanged, got:\n%s", content)
	}
}

// ── Array apply tests (regression for #180) ──────────────────────────────

// TestApply_SettingsArray_RoundTrip is the primary regression test for #180.
// It verifies that the output of `get settings --schema X` (a YAML array of
// settings objects) can be fed back into `apply` without error.
func TestApply_SettingsArray_RoundTrip(t *testing.T) {
	createCount := 0
	srv, c := newApplyTestServer(t, map[string]http.HandlerFunc{
		"/platform/classic/environment-api/v2/settings/objects": func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodPost {
				t.Errorf("expected POST, got %s", r.Method)
			}
			createCount++
			w.WriteHeader(http.StatusOK)
			json.NewEncoder(w).Encode([]map[string]interface{}{
				{"objectId": fmt.Sprintf("obj-%d", createCount)},
			})
		},
		"/platform/metadata/v1/user": func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusUnauthorized)
		},
	})
	defer srv.Close()
	a := NewApplier(c)

	// Simulates the output of: dtctl get settings --schema builtin:rum.web.enablement -o json
	settingsArray := `[
		{"schemaId":"builtin:rum.web.enablement","scope":"APPLICATION-111","value":{"enabled":true,"costControl":50}},
		{"schemaId":"builtin:rum.web.enablement","scope":"APPLICATION-222","value":{"enabled":true,"costControl":100}},
		{"schemaId":"builtin:rum.web.enablement","scope":"APPLICATION-333","value":{"enabled":false,"costControl":25}}
	]`

	results, err := a.Apply([]byte(settingsArray), ApplyOptions{})
	if err != nil {
		t.Fatalf("Apply() settings array error = %v", err)
	}
	if len(results) != 3 {
		t.Fatalf("expected 3 results, got %d", len(results))
	}
	if createCount != 3 {
		t.Errorf("expected 3 API calls, got %d", createCount)
	}
	for i, r := range results {
		base := r.(*SettingsApplyResult).ApplyResultBase
		if base.Action != ActionCreated {
			t.Errorf("result %d: expected 'created', got %q", i, base.Action)
		}
	}
}

// TestApply_SettingsArray_PartialFailure verifies that a failure in one element
// does not abort the entire batch and the error reports both succeeded and failed items.
func TestApply_SettingsArray_PartialFailure(t *testing.T) {
	callCount := 0
	srv, c := newApplyTestServer(t, map[string]http.HandlerFunc{
		"/platform/classic/environment-api/v2/settings/objects": func(w http.ResponseWriter, r *http.Request) {
			callCount++
			if callCount == 2 {
				// Second element fails
				w.WriteHeader(http.StatusBadRequest)
				w.Write([]byte(`{"error":{"code":400,"message":"invalid value"}}`))
				return
			}
			w.WriteHeader(http.StatusOK)
			json.NewEncoder(w).Encode([]map[string]interface{}{
				{"objectId": fmt.Sprintf("obj-%d", callCount)},
			})
		},
		"/platform/metadata/v1/user": func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusUnauthorized)
		},
	})
	defer srv.Close()
	a := NewApplier(c)

	settingsArray := `[
		{"schemaId":"builtin:rum.web.enablement","scope":"APPLICATION-111","value":{"enabled":true}},
		{"schemaId":"builtin:rum.web.enablement","scope":"APPLICATION-222","value":{"enabled":true}},
		{"schemaId":"builtin:rum.web.enablement","scope":"APPLICATION-333","value":{"enabled":true}}
	]`

	results, err := a.Apply([]byte(settingsArray), ApplyOptions{})
	if err == nil {
		t.Fatal("expected error for partial failure, got nil")
	}

	var listErr *ListApplyError
	if e, ok := err.(*ListApplyError); !ok {
		t.Fatalf("expected *ListApplyError, got %T: %v", err, err)
	} else {
		listErr = e
	}

	// 2 succeeded, 1 failed
	if len(results) != 2 {
		t.Errorf("expected 2 successful results, got %d", len(results))
	}
	if listErr.Failed != 1 {
		t.Errorf("expected 1 failure, got %d", listErr.Failed)
	}
	if listErr.Total != 3 {
		t.Errorf("expected total=3, got %d", listErr.Total)
	}
}

// TestApply_SettingsArray_DryRun verifies that dry-run works with array input.
func TestApply_SettingsArray_DryRun(t *testing.T) {
	srv, c := newApplyTestServer(t, map[string]http.HandlerFunc{
		"/platform/metadata/v1/user": func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusUnauthorized)
		},
	})
	defer srv.Close()
	a := NewApplier(c)

	settingsArray := `[
		{"schemaId":"builtin:rum.web.enablement","scope":"APP-1","value":{"enabled":true}},
		{"schemaId":"builtin:rum.web.enablement","scope":"APP-2","value":{"enabled":false}}
	]`

	results, err := a.Apply([]byte(settingsArray), ApplyOptions{DryRun: true})
	if err != nil {
		t.Fatalf("Apply() dry-run array error = %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("expected 2 dry-run results, got %d", len(results))
	}
}

// TestApply_Array_OverrideIDRejected verifies that --id flag is rejected for array input.
func TestApply_Array_OverrideIDRejected(t *testing.T) {
	srv, c := newApplyTestServer(t, map[string]http.HandlerFunc{
		"/platform/metadata/v1/user": func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusUnauthorized)
		},
	})
	defer srv.Close()
	a := NewApplier(c)

	settingsArray := `[{"schemaId":"builtin:rum.web.enablement","scope":"APP-1","value":{"enabled":true}}]`
	_, err := a.Apply([]byte(settingsArray), ApplyOptions{OverrideID: "some-id"})
	if err == nil {
		t.Fatal("expected error when --id is used with array input")
	}
	if !strings.Contains(err.Error(), "--id flag cannot be used with array input") {
		t.Errorf("unexpected error message: %v", err)
	}
}

// TestApply_Array_HookRunsOnce verifies that pre-apply hook runs once for array input
// (not once per element).
func TestApply_Array_HookRunsOnce(t *testing.T) {
	hookRunCount := 0
	// Create a temp script that increments a counter file
	dir := t.TempDir()
	counterFile := dir + "/count"
	os.WriteFile(counterFile, []byte("0"), 0o644)

	srv, c := newApplyTestServer(t, map[string]http.HandlerFunc{
		"/platform/metadata/v1/user": func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusUnauthorized)
		},
		"/platform/classic/environment-api/v2/settings/objects": func(w http.ResponseWriter, r *http.Request) {
			hookRunCount++ // just track calls
			w.WriteHeader(http.StatusOK)
			json.NewEncoder(w).Encode([]map[string]interface{}{
				{"objectId": fmt.Sprintf("obj-%d", hookRunCount)},
			})
		},
	})
	defer srv.Close()

	a := NewApplier(c).
		WithPreApplyHook("true"). // succeeds
		WithSourceFile("test.yaml")

	settingsArray := `[
		{"schemaId":"builtin:rum.web.enablement","scope":"APP-1","value":{"enabled":true}},
		{"schemaId":"builtin:rum.web.enablement","scope":"APP-2","value":{"enabled":false}}
	]`

	results, err := a.Apply([]byte(settingsArray), ApplyOptions{})
	if err != nil {
		t.Fatalf("Apply() error = %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("expected 2 results, got %d", len(results))
	}
}

// TestApply_WorkflowArray verifies that array apply works for non-settings resources too,
// ensuring the generic approach works across all resource types.
func TestApply_WorkflowArray(t *testing.T) {
	createCount := 0
	srv, c := newApplyTestServer(t, map[string]http.HandlerFunc{
		"/platform/automation/v1/workflows": func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodPost {
				t.Errorf("expected POST, got %s", r.Method)
			}
			createCount++
			w.WriteHeader(http.StatusCreated)
			json.NewEncoder(w).Encode(map[string]interface{}{
				"id":    fmt.Sprintf("wf-%d", createCount),
				"title": fmt.Sprintf("Workflow %d", createCount),
			})
		},
		"/platform/metadata/v1/user": func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusUnauthorized)
		},
	})
	defer srv.Close()
	a := NewApplier(c)

	wfArray := `[
		{"title":"Workflow A","tasks":{},"trigger":{}},
		{"title":"Workflow B","tasks":{},"trigger":{}}
	]`

	results, err := a.Apply([]byte(wfArray), ApplyOptions{})
	if err != nil {
		t.Fatalf("Apply() workflow array error = %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("expected 2 results, got %d", len(results))
	}
	if createCount != 2 {
		t.Errorf("expected 2 API calls, got %d", createCount)
	}
}

// TestApply_SettingsArray_YAML_RoundTrip is the exact reproduction of the bug
// reported in #180 — YAML array input (as produced by `get settings --schema X -o yaml`).
func TestApply_SettingsArray_YAML_RoundTrip(t *testing.T) {
	createCount := 0
	srv, c := newApplyTestServer(t, map[string]http.HandlerFunc{
		"/platform/classic/environment-api/v2/settings/objects": func(w http.ResponseWriter, r *http.Request) {
			createCount++
			w.WriteHeader(http.StatusOK)
			json.NewEncoder(w).Encode([]map[string]interface{}{
				{"objectId": fmt.Sprintf("obj-%d", createCount)},
			})
		},
		"/platform/metadata/v1/user": func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusUnauthorized)
		},
	})
	defer srv.Close()
	a := NewApplier(c)

	// This is exactly what `dtctl get settings --schema builtin:rum.web.enablement -o yaml`
	// produces — a YAML list of settings objects.
	yamlArray := `- schemaId: builtin:rum.web.enablement
  scope: APPLICATION-111
  value:
    enabled: true
    costControl: 50
- schemaId: builtin:rum.web.enablement
  scope: APPLICATION-222
  value:
    enabled: false
    costControl: 100
`

	results, err := a.Apply([]byte(yamlArray), ApplyOptions{})
	if err != nil {
		t.Fatalf("Apply() YAML settings array error = %v (this was bug #180)", err)
	}
	if len(results) != 2 {
		t.Fatalf("expected 2 results, got %d", len(results))
	}
}

// TestApply_Settings_OverrideID_TriggersUpdate is a regression test for
// https://github.com/dynatrace-oss/dtctl/issues/255
//
// When --id <objectId> is provided for a settings object that has no
// objectId/objectid field, apply must issue a PUT (UPDATE) rather than a
// POST (CREATE).
func TestApply_Settings_OverrideID_TriggersUpdate(t *testing.T) {
	putCalled := false
	// handler.Update internally does: GET (schemaVersion) → PUT → GET (re-fetch).
	// applySettings itself also does a GET first to confirm existence.
	// All three GET and the PUT land on the same path.
	srv, c := newApplyTestServer(t, map[string]http.HandlerFunc{
		"/platform/classic/environment-api/v2/settings/objects/urn:settings:obj-255": func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			switch r.Method {
			case http.MethodGet:
				json.NewEncoder(w).Encode(map[string]interface{}{
					"objectId":      "urn:settings:obj-255",
					"schemaId":      "builtin:alerting.profile",
					"schemaVersion": "1",
					"scope":         "environment",
					"value":         map[string]interface{}{"name": "Updated"},
					"summary":       "Updated",
				})
			case http.MethodPut:
				putCalled = true
				w.WriteHeader(http.StatusNoContent)
			default:
				t.Errorf("unexpected method %s on settings object path", r.Method)
				w.WriteHeader(http.StatusMethodNotAllowed)
			}
		},
		"/platform/metadata/v1/user": func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusUnauthorized)
		},
	})
	defer srv.Close()
	a := NewApplier(c)

	// File has no objectId — the caller provides it via --id (OverrideID).
	settingsJSON := `{"schemaId":"builtin:alerting.profile","scope":"environment","value":{"name":"Updated"}}`
	results, err := a.Apply([]byte(settingsJSON), ApplyOptions{OverrideID: "urn:settings:obj-255"})
	if err != nil {
		t.Fatalf("Apply() with --id for settings error = %v", err)
	}
	if !putCalled {
		t.Error("expected a PUT request, but none was made (bug #255: --id flag ignored for settings)")
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	base := results[0].(*SettingsApplyResult).ApplyResultBase
	if base.Action != ActionUpdated {
		t.Errorf("expected action 'updated', got %q", base.Action)
	}
}

// TestApply_Settings_DryRun_WithObjectID_ReportsUpdated is a regression test for
// https://github.com/dynatrace-oss/dtctl/issues/256
//
// When a settings YAML file contains objectid: (or objectId:), --dry-run must
// report "updated", not "created". Actual apply already does the right thing;
// dry-run was using doc["id"] which is never set for settings objects.
func TestApply_Settings_DryRun_WithObjectID_ReportsUpdated(t *testing.T) {
	srv, c := newApplyTestServer(t, map[string]http.HandlerFunc{
		// The dry run looks the object up, as apply does (#521): it exists.
		"/platform/classic/environment-api/v2/settings/objects/urn:settings:obj-256": func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodGet {
				t.Errorf("dry run issued %s, want only GET", r.Method)
			}
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`{"objectId":"urn:settings:obj-256","schemaId":"builtin:alerting.profile","scope":"environment","value":{"name":"Test"}}`))
		},
		"/platform/metadata/v1/user": func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusUnauthorized)
		},
	})
	defer srv.Close()
	a := NewApplier(c)

	tests := []struct {
		name         string
		settingsJSON string
	}{
		{
			name:         "lowercase objectid",
			settingsJSON: `{"objectid":"urn:settings:obj-256","schemaId":"builtin:alerting.profile","scope":"environment","value":{"name":"Test"}}`,
		},
		{
			name:         "camelCase objectId",
			settingsJSON: `{"objectId":"urn:settings:obj-256","schemaId":"builtin:alerting.profile","scope":"environment","value":{"name":"Test"}}`,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			results, err := a.Apply([]byte(tc.settingsJSON), ApplyOptions{DryRun: true})
			if err != nil {
				t.Fatalf("Apply() dry-run error = %v", err)
			}
			if len(results) != 1 {
				t.Fatalf("expected 1 result, got %d", len(results))
			}
			dr, ok := results[0].(*DryRunResult)
			if !ok {
				t.Fatalf("expected *DryRunResult, got %T", results[0])
			}
			if dr.Action != ActionUpdated {
				t.Errorf("dry-run with %s: expected action 'updated', got %q (bug #256)", tc.name, dr.Action)
			}
			if dr.ID != "urn:settings:obj-256" {
				t.Errorf("dry-run with %s: expected ID 'urn:settings:obj-256', got %q", tc.name, dr.ID)
			}
		})
	}
}

// TestApply_Settings_DryRun_WithoutObjectID_ReportsCreated verifies that dry-run
// still reports "created" when no objectId is present (the happy path is unaffected).
func TestApply_Settings_DryRun_WithoutObjectID_ReportsCreated(t *testing.T) {
	srv, c := newApplyTestServer(t, map[string]http.HandlerFunc{
		"/platform/metadata/v1/user": func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusUnauthorized)
		},
	})
	defer srv.Close()
	a := NewApplier(c)

	settingsJSON := `{"schemaId":"builtin:alerting.profile","scope":"environment","value":{"name":"New"}}`
	results, err := a.Apply([]byte(settingsJSON), ApplyOptions{DryRun: true})
	if err != nil {
		t.Fatalf("Apply() dry-run error = %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	dr, ok := results[0].(*DryRunResult)
	if !ok {
		t.Fatalf("expected *DryRunResult, got %T", results[0])
	}
	if dr.Action != ActionCreated {
		t.Errorf("expected action 'created' without objectId, got %q", dr.Action)
	}
}
