package apply

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
)

// Regression tests for https://github.com/Dynatrace/dtctl/issues/491
//
// apply looks the object up to decide between create and update. Only a 404
// means "create". A 403, a 5xx or a network error says nothing about whether
// the object exists, and falling through to create adds a duplicate on every
// run for the resources whose create request does not carry the id.

// lookupCase describes one resource's create-vs-update lookup.
type lookupCase struct {
	name string
	// payload carries an id, so apply takes the lookup path.
	payload string
	// lookupPath is the endpoint apply reads to decide create vs update.
	lookupPath string
	// createPath is the endpoint that must stay untouched when the lookup fails.
	createPath string
	// createResponse answers the create request in the 404 case.
	createResponse http.HandlerFunc
	// wantErr is a fragment of the error a failed lookup must produce.
	wantErr string
}

func lookupCases() []lookupCase {
	return []lookupCase{
		{
			name:       "workflow",
			payload:    `{"id":"wf-491","title":"My Workflow","tasks":{},"trigger":{}}`,
			lookupPath: "/platform/automation/v1/workflows/wf-491",
			createPath: "/platform/automation/v1/workflows",
			createResponse: func(w http.ResponseWriter, r *http.Request) {
				json.NewEncoder(w).Encode(map[string]any{"id": "wf-new", "title": "My Workflow"})
			},
			wantErr: `failed to check workflow "wf-491" existence`,
		},
		{
			name:       "slo",
			payload:    `{"id":"slo-491","name":"My SLO","criteria":{"pass":[{"criteria":[{"metric":"<100","steps":600}]}]},"target":99.0,"timeframe":"now-7d","metricExpression":"100*..."}`,
			lookupPath: "/platform/slo/v1/slos/slo-491",
			createPath: "/platform/slo/v1/slos",
			createResponse: func(w http.ResponseWriter, r *http.Request) {
				json.NewEncoder(w).Encode(map[string]any{"id": "slo-new", "name": "My SLO"})
			},
			wantErr: `failed to check SLO "slo-491" existence`,
		},
		{
			name:       "bucket",
			payload:    `{"bucketName":"my-logs","table":"logs","displayName":"My Logs","retentionDays":35}`,
			lookupPath: "/platform/storage/management/v1/bucket-definitions/my-logs",
			createPath: "/platform/storage/management/v1/bucket-definitions",
			createResponse: func(w http.ResponseWriter, r *http.Request) {
				json.NewEncoder(w).Encode(map[string]any{"bucketName": "my-logs", "table": "logs", "status": "creating"})
			},
			wantErr: `failed to check bucket "my-logs" existence`,
		},
		{
			name:       "settings",
			payload:    `{"objectId":"urn:settings:obj-491","schemaId":"builtin:alerting.profile","scope":"environment","value":{"name":"Nightly"}}`,
			lookupPath: "/platform/classic/environment-api/v2/settings/objects/urn:settings:obj-491",
			createPath: "/platform/classic/environment-api/v2/settings/objects",
			createResponse: func(w http.ResponseWriter, r *http.Request) {
				json.NewEncoder(w).Encode([]map[string]any{{"objectId": "urn:settings:obj-new"}})
			},
			wantErr: `failed to check settings object "urn:settings:obj-491" existence`,
		},
		{
			name:       "dashboard",
			payload:    `{"type":"dashboard","id":"dash-491","tiles":{"items":[]}}`,
			lookupPath: "/platform/document/v1/documents/dash-491/metadata",
			createPath: "/platform/document/v1/documents",
			createResponse: func(w http.ResponseWriter, r *http.Request) {
				boundary := "resp-boundary"
				w.Header().Set("Content-Type", fmt.Sprintf("multipart/form-data; boundary=%s", boundary))
				fmt.Fprintf(w, "--%s\r\nContent-Disposition: form-data; name=\"metadata\"\r\nContent-Type: application/json\r\n\r\n"+
					"{\"id\":\"dash-new\",\"name\":\"My Dashboard\",\"type\":\"dashboard\",\"version\":1}\r\n--%s--\r\n", boundary, boundary)
			},
			wantErr: `failed to check dashboard "dash-491" existence`,
		},
	}
}

// newLookupApplier serves the lookup with lookupStatus and counts create calls.
func newLookupApplier(t *testing.T, tc lookupCase, lookupStatus int, creates *int) (*Applier, func()) {
	t.Helper()
	srv, c := newApplyTestServer(t, map[string]http.HandlerFunc{
		tc.lookupPath: func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(lookupStatus)
			fmt.Fprintf(w, `{"error":{"code":%d,"message":"lookup failed"}}`, lookupStatus)
		},
		tc.createPath: func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodPost {
				t.Errorf("%s: unexpected %s on the create path", tc.name, r.Method)
				w.WriteHeader(http.StatusMethodNotAllowed)
				return
			}
			*creates++
			w.Header().Set("Content-Type", "application/json")
			tc.createResponse(w, r)
		},
		"/platform/metadata/v1/user": func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusUnauthorized)
		},
	})
	return NewApplier(c), srv.Close
}

func TestApply_LookupError_DoesNotCreate(t *testing.T) {
	for _, tc := range lookupCases() {
		for _, status := range []int{http.StatusForbidden, http.StatusInternalServerError} {
			t.Run(fmt.Sprintf("%s/%d", tc.name, status), func(t *testing.T) {
				creates := 0
				a, closeSrv := newLookupApplier(t, tc, status, &creates)
				defer closeSrv()

				_, err := a.Apply([]byte(tc.payload), ApplyOptions{})
				if err == nil {
					t.Fatalf("Apply() error = nil, want the %d lookup to stop apply", status)
				}
				if !strings.Contains(err.Error(), tc.wantErr) {
					t.Errorf("error = %v, want it to contain %q", err, tc.wantErr)
				}
				if creates != 0 {
					t.Errorf("create calls = %d, want 0: a failed lookup created a duplicate", creates)
				}
			})
		}
	}
}

func TestApply_LookupNotFound_Creates(t *testing.T) {
	for _, tc := range lookupCases() {
		t.Run(tc.name, func(t *testing.T) {
			creates := 0
			a, closeSrv := newLookupApplier(t, tc, http.StatusNotFound, &creates)
			defer closeSrv()

			results, err := a.Apply([]byte(tc.payload), ApplyOptions{})
			if err != nil {
				t.Fatalf("Apply() error = %v, want a 404 to create", err)
			}
			if creates != 1 {
				t.Fatalf("create calls = %d, want 1", creates)
			}
			if len(results) != 1 {
				t.Fatalf("results = %d, want 1", len(results))
			}
			if got := resultAction(t, results[0]); got != ActionCreated {
				t.Errorf("action = %q, want %q", got, ActionCreated)
			}
		})
	}
}

// A closed server stands for the network error the helper's comment names:
// no status code at all, and still no evidence that the object is absent.
func TestApply_LookupNetworkError_DoesNotCreate(t *testing.T) {
	for _, tc := range lookupCases() {
		t.Run(tc.name, func(t *testing.T) {
			creates := 0
			a, closeSrv := newLookupApplier(t, tc, http.StatusNotFound, &creates)
			closeSrv() // the applier now talks to a dead server

			_, err := a.Apply([]byte(tc.payload), ApplyOptions{})
			if err == nil {
				t.Fatalf("Apply() error = nil, want the transport failure to stop apply")
			}
			if creates != 0 {
				t.Errorf("create calls = %d, want 0", creates)
			}
		})
	}
}

// A dry run resolves create vs update through the same lookup. Reporting
// "would create" for a lookup the real apply refuses is its own bug class.
func TestApply_DryRun_LookupError_Fails(t *testing.T) {
	tc := lookupCases()[4] // dashboard: the only lookupCases entry with a dry-run lookup
	creates := 0
	a, closeSrv := newLookupApplier(t, tc, http.StatusForbidden, &creates)
	defer closeSrv()

	_, err := a.Apply([]byte(tc.payload), ApplyOptions{DryRun: true})
	if err == nil {
		t.Fatal("Apply(dry-run) error = nil, want the same 403 that stops the real apply")
	}
	if !strings.Contains(err.Error(), tc.wantErr) {
		t.Errorf("error = %v, want it to contain %q", err, tc.wantErr)
	}
}

func TestDryRunAnomalyDetector_TitleLookupError_Fails(t *testing.T) {
	a, closeSrv := newAnomalyDetectorApplier(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			t.Error("dry run sent a POST after a failed lookup")
		}
		w.WriteHeader(http.StatusForbidden)
		fmt.Fprint(w, `{"error":{"code":403,"message":"missing settings:objects:read"}}`)
	})
	defer closeSrv()

	_, err := a.Apply([]byte(anomalyDetectorYAML), ApplyOptions{DryRun: true})
	if err == nil {
		t.Fatal("Apply(dry-run) error = nil, want the failed title lookup to fail the dry run")
	}
	if !strings.Contains(err.Error(), `failed to check anomaly detector "Test alert" existence`) {
		t.Errorf("error = %v, want it to name the failed existence check", err)
	}
}

// cloudNameLookupCase describes one cloud resource whose create-vs-update
// decision comes from searching a list for a name.
type cloudNameLookupCase struct {
	name     string
	listPath string
	payload  string
	wantErr  string
}

func cloudNameLookupCases() []cloudNameLookupCase {
	const settingsPath = "/platform/classic/environment-api/v2/settings/objects"
	return []cloudNameLookupCase{
		{
			name:     "aws_monitoring_config",
			listPath: "/platform/extensions/v2/extensions/com.dynatrace.extension.da-aws/monitoring-configurations",
			payload:  `{"scope":"integration-aws","value":{"description":"My AWS Config","awsAuthenticationMethod":"role","version":"1.0.0"}}`,
			wantErr:  `failed to check AWS monitoring config "My AWS Config" existence`,
		},
		{
			name:     "azure_monitoring_config",
			listPath: "/platform/extensions/v2/extensions/com.dynatrace.extension.da-azure/monitoring-configurations",
			payload:  `{"scope":"integration-azure","value":{"description":"My Azure Config","subscriptionId":"sub-1","tenantId":"tenant-1","credentials":"cred-1","version":"1.0.0"}}`,
			wantErr:  `failed to check Azure monitoring config "My Azure Config" existence`,
		},
		{
			name:     "gcp_monitoring_config",
			listPath: "/platform/extensions/v2/extensions/com.dynatrace.extension.da-gcp/monitoring-configurations",
			payload:  `{"scope":"integration-gcp","value":{"description":"My GCP Config","projectId":"my-proj","serviceAccountKey":"{}","version":"1.0.0"}}`,
			wantErr:  `failed to check GCP monitoring config "My GCP Config" existence`,
		},
		{
			name:     "azure_connection",
			listPath: settingsPath,
			payload:  `{"schemaId":"builtin:hyperscaler-authentication.connections.azure","scope":"environment","value":{"name":"my-conn","type":"federatedIdentityCredential"}}`,
			wantErr:  `failed to check Azure connection "my-conn" existence`,
		},
		{
			name:     "gcp_connection",
			listPath: settingsPath,
			payload:  `{"schemaId":"builtin:hyperscaler-authentication.connections.gcp","scope":"environment","value":{"name":"my-gcp-conn","type":"serviceAccountImpersonation"}}`,
			wantErr:  `failed to check GCP connection "my-gcp-conn" existence`,
		},
	}
}

// newCloudNameLookupApplier fails the name lookup with a 500 and counts the
// create calls that a correct applier never makes.
func newCloudNameLookupApplier(t *testing.T, tc cloudNameLookupCase, creates *int) (*Applier, func()) {
	t.Helper()
	srv, c := newApplyTestServer(t, map[string]http.HandlerFunc{
		tc.listPath: func(w http.ResponseWriter, r *http.Request) {
			if r.Method == http.MethodPost {
				*creates++
				w.Header().Set("Content-Type", "application/json")
				w.Write([]byte(`[{"objectId":"obj-new"}]`))
				return
			}
			w.WriteHeader(http.StatusInternalServerError)
			fmt.Fprint(w, `{"error":{"code":500,"message":"lookup failed"}}`)
		},
		"/platform/metadata/v1/user": func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusUnauthorized)
		},
	})
	return NewApplier(c), srv.Close
}

// The cloud appliers resolve create vs update by searching a list for a name.
// A list that fails is not an empty list.
func TestApply_CloudNameLookupError_DoesNotCreate(t *testing.T) {
	for _, tc := range cloudNameLookupCases() {
		t.Run(tc.name, func(t *testing.T) {
			creates := 0
			a, closeSrv := newCloudNameLookupApplier(t, tc, &creates)
			defer closeSrv()

			_, err := a.Apply([]byte(tc.payload), ApplyOptions{})
			if err == nil {
				t.Fatal("Apply() error = nil, want the failed name lookup to stop apply")
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("error = %v, want it to contain %q", err, tc.wantErr)
			}
			if creates != 0 {
				t.Errorf("create calls = %d, want 0: a failed lookup created a duplicate", creates)
			}
		})
	}
}

// A dry run of a cloud resource resolves create vs update through the very
// same name lookup (#509), so a failed lookup must fail the dry run instead of
// reporting the create the apply behind it would refuse.
func TestApply_CloudNameLookupError_FailsDryRun(t *testing.T) {
	for _, tc := range cloudNameLookupCases() {
		t.Run(tc.name, func(t *testing.T) {
			creates := 0
			a, closeSrv := newCloudNameLookupApplier(t, tc, &creates)
			defer closeSrv()

			_, err := a.Apply([]byte(tc.payload), ApplyOptions{DryRun: true})
			if err == nil {
				t.Fatal("Apply(dry-run) error = nil, want the same failed lookup that stops the real apply")
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("error = %v, want it to contain %q", err, tc.wantErr)
			}
			if creates != 0 {
				t.Errorf("create calls = %d, want 0: a dry run writes nothing", creates)
			}
		})
	}
}

// resultAction reads the action out of any per-resource result type.
func resultAction(t *testing.T, r ApplyResult) string {
	t.Helper()
	encoded, err := json.Marshal(r)
	if err != nil {
		t.Fatalf("marshal result: %v", err)
	}
	var decoded struct {
		Action string `json:"action"`
	}
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatalf("decode result: %v", err)
	}
	return decoded.Action
}

// A detector with no objectId is matched by title through a list call. That
// list failing is not evidence that no detector with the title exists.
func TestApply_AnomalyDetector_TitleLookupError_DoesNotCreate(t *testing.T) {
	creates := 0
	a, closeSrv := newAnomalyDetectorApplier(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			w.WriteHeader(http.StatusForbidden)
			fmt.Fprint(w, `{"error":{"code":403,"message":"missing settings:objects:read"}}`)
		case http.MethodPost:
			creates++
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`[{"code":200,"objectId":"urn:settings:obj-new"}]`))
		}
	})
	defer closeSrv()

	_, err := a.Apply([]byte(anomalyDetectorYAML), ApplyOptions{})
	if err == nil {
		t.Fatal("Apply() error = nil, want the failed title lookup to stop apply")
	}
	if !strings.Contains(err.Error(), `failed to check anomaly detector "Test alert" existence`) {
		t.Errorf("error = %v, want it to name the failed existence check", err)
	}
	if creates != 0 {
		t.Errorf("create calls = %d, want 0: a failed lookup created a second detector", creates)
	}
}
