package apply

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
)

// Regression tests for https://github.com/Dynatrace/dtctl/issues/509
//
// --dry-run reported "created" with an empty id for every cloud resource, even
// for a document whose apply issues a PUT: the generic dry-run tail only read
// doc["id"], a field these resources never carry, and the objectId fallback
// added for #256 was gated on the settings resource type. "created" reads as
// safe, so the error pointed the dangerous way — dry run called an overwrite a
// new configuration.

const (
	awsMonitoringListPath   = "/platform/extensions/v2/extensions/com.dynatrace.extension.da-aws/monitoring-configurations"
	azureMonitoringListPath = "/platform/extensions/v2/extensions/com.dynatrace.extension.da-azure/monitoring-configurations"
	gcpMonitoringListPath   = "/platform/extensions/v2/extensions/com.dynatrace.extension.da-gcp/monitoring-configurations"
)

// cloudDryRunCase describes one cloud resource's dry run, in both the shape
// that carries an objectId and the shape that has to resolve one by name.
type cloudDryRunCase struct {
	name         string
	resourceType string
	// withID carries an objectId: create vs update needs no lookup.
	withID   string
	objectID string
	// withoutID carries no objectId, so the name lookup decides.
	withoutID string
	// listPath answers the name lookup.
	listPath string
	// listMatch holds an object matching withoutID; listEmpty holds none.
	listMatch string
	listEmpty string
	foundID   string
	wantName  string
	wantScope string
}

func cloudDryRunCases() []cloudDryRunCase {
	return []cloudDryRunCase{
		{
			name:         "aws_monitoring_config",
			resourceType: "aws_monitoring_config",
			withID:       `{"objectId":"aws-obj-509","scope":"integration-aws","value":{"description":"My AWS Config","version":"1.0.0"}}`,
			objectID:     "aws-obj-509",
			withoutID:    `{"scope":"integration-aws","value":{"description":"My AWS Config","version":"1.0.0"}}`,
			listPath:     awsMonitoringListPath,
			listMatch:    `{"items":[{"objectId":"aws-found-509","value":{"description":"My AWS Config"}}]}`,
			listEmpty:    `{"items":[]}`,
			foundID:      "aws-found-509",
			wantName:     "My AWS Config",
			wantScope:    "integration-aws",
		},
		{
			name:         "azure_monitoring_config",
			resourceType: "azure_monitoring_config",
			withID:       `{"objectId":"azure-obj-509","scope":"integration-azure","value":{"description":"My Azure Config","version":"1.0.0"}}`,
			objectID:     "azure-obj-509",
			withoutID:    `{"scope":"integration-azure","value":{"description":"My Azure Config","version":"1.0.0"}}`,
			listPath:     azureMonitoringListPath,
			listMatch:    `{"items":[{"objectId":"azure-found-509","value":{"description":"My Azure Config"}}]}`,
			listEmpty:    `{"items":[]}`,
			foundID:      "azure-found-509",
			wantName:     "My Azure Config",
			wantScope:    "integration-azure",
		},
		{
			name:         "gcp_monitoring_config",
			resourceType: "gcp_monitoring_config",
			withID:       `{"objectId":"gcp-obj-509","scope":"integration-gcp","value":{"description":"My GCP Config","version":"1.0.0"}}`,
			objectID:     "gcp-obj-509",
			withoutID:    `{"scope":"integration-gcp","value":{"description":"My GCP Config","version":"1.0.0"}}`,
			listPath:     gcpMonitoringListPath,
			listMatch:    `{"items":[{"objectId":"gcp-found-509","value":{"description":"My GCP Config"}}]}`,
			listEmpty:    `{"items":[]}`,
			foundID:      "gcp-found-509",
			wantName:     "My GCP Config",
			wantScope:    "integration-gcp",
		},
		{
			name:         "azure_connection",
			resourceType: "azure_connection",
			withID:       `{"objectId":"azure-conn-509","schemaId":"builtin:hyperscaler-authentication.connections.azure","scope":"environment","value":{"name":"my-azure-conn","type":"federatedIdentityCredential"}}`,
			objectID:     "azure-conn-509",
			withoutID:    `{"schemaId":"builtin:hyperscaler-authentication.connections.azure","scope":"environment","value":{"name":"my-azure-conn","type":"federatedIdentityCredential"}}`,
			listPath:     settingsObjectsPath,
			listMatch:    `{"items":[{"objectId":"azure-conn-found-509","value":{"name":"my-azure-conn","type":"federatedIdentityCredential"}}]}`,
			listEmpty:    `{"items":[]}`,
			foundID:      "azure-conn-found-509",
			wantName:     "my-azure-conn",
			wantScope:    "environment",
		},
		{
			name:         "gcp_connection",
			resourceType: "gcp_connection",
			withID:       `{"objectId":"gcp-conn-509","schemaId":"builtin:hyperscaler-authentication.connections.gcp","scope":"environment","value":{"name":"my-gcp-conn","type":"serviceAccountImpersonation"}}`,
			objectID:     "gcp-conn-509",
			withoutID:    `{"schemaId":"builtin:hyperscaler-authentication.connections.gcp","scope":"environment","value":{"name":"my-gcp-conn","type":"serviceAccountImpersonation"}}`,
			listPath:     settingsObjectsPath,
			listMatch:    `{"items":[{"objectId":"gcp-conn-found-509","value":{"name":"my-gcp-conn","type":"serviceAccountImpersonation"}}]}`,
			listEmpty:    `{"items":[]}`,
			foundID:      "gcp-conn-found-509",
			wantName:     "my-gcp-conn",
			wantScope:    "environment",
		},
	}
}

// newCloudDryRunApplier answers the case's name lookup with listBody. A nil
// listBody means the lookup must not happen at all.
func newCloudDryRunApplier(t *testing.T, tc cloudDryRunCase, listBody *string) (*Applier, func()) {
	t.Helper()
	srv, c := newApplyTestServer(t, map[string]http.HandlerFunc{
		tc.listPath: func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodGet {
				t.Errorf("%s: dry run sent a %s to %s — it must not write anything", tc.name, r.Method, tc.listPath)
				w.WriteHeader(http.StatusMethodNotAllowed)
				return
			}
			if listBody == nil {
				t.Errorf("%s: dry run looked the name up although the payload carries an objectId", tc.name)
				w.WriteHeader(http.StatusInternalServerError)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, *listBody)
		},
		"/platform/metadata/v1/user": func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusUnauthorized)
		},
	})
	return NewApplier(c), srv.Close
}

// cloudDryRun runs a dry run and returns its single result.
func cloudDryRun(t *testing.T, a *Applier, payload string) *DryRunResult {
	t.Helper()
	results, err := a.Apply([]byte(payload), ApplyOptions{DryRun: true})
	if err != nil {
		t.Fatalf("Apply(dry-run) error = %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("results = %d, want 1", len(results))
	}
	dr, ok := results[0].(*DryRunResult)
	if !ok {
		t.Fatalf("result type = %T, want *DryRunResult", results[0])
	}
	return dr
}

// A payload carrying an objectId is an update, and dry run must say so without
// any lookup — this is the case the issue reported as "created", id "".
func TestApply_CloudDryRun_WithObjectID_ReportsUpdated(t *testing.T) {
	for _, tc := range cloudDryRunCases() {
		// A YAML round-trip lowercases the key, so both spellings must count.
		for _, spelling := range []string{"objectId", "objectid"} {
			t.Run(fmt.Sprintf("%s/%s", tc.name, spelling), func(t *testing.T) {
				a, closeSrv := newCloudDryRunApplier(t, tc, nil)
				defer closeSrv()

				payload := strings.Replace(tc.withID, `"objectId"`, `"`+spelling+`"`, 1)
				dr := cloudDryRun(t, a, payload)

				if dr.Action != ActionUpdated {
					t.Errorf("action = %q, want %q (bug #509: dry run reported a create for an overwrite)", dr.Action, ActionUpdated)
				}
				if dr.ID != tc.objectID {
					t.Errorf("id = %q, want %q", dr.ID, tc.objectID)
				}
				if dr.ResourceType != tc.resourceType {
					t.Errorf("resourceType = %q, want %q", dr.ResourceType, tc.resourceType)
				}
				if dr.Name != tc.wantName {
					t.Errorf("name = %q, want %q", dr.Name, tc.wantName)
				}
				if dr.Scope != tc.wantScope {
					t.Errorf("scope = %q, want %q", dr.Scope, tc.wantScope)
				}
			})
		}
	}
}

// The cloud appliers also resolve the target by name, so a payload with no
// objectId at all still becomes an update. Dry run has to do that lookup too.
func TestApply_CloudDryRun_NameMatch_ReportsUpdated(t *testing.T) {
	for _, tc := range cloudDryRunCases() {
		t.Run(tc.name, func(t *testing.T) {
			a, closeSrv := newCloudDryRunApplier(t, tc, &tc.listMatch)
			defer closeSrv()

			dr := cloudDryRun(t, a, tc.withoutID)

			if dr.Action != ActionUpdated {
				t.Errorf("action = %q, want %q: the apply behind this dry run updates the object it found by name", dr.Action, ActionUpdated)
			}
			if dr.ID != tc.foundID {
				t.Errorf("id = %q, want the id found by name %q", dr.ID, tc.foundID)
			}
		})
	}
}

// Nothing matching the name means the apply really would create.
func TestApply_CloudDryRun_NoNameMatch_ReportsCreated(t *testing.T) {
	for _, tc := range cloudDryRunCases() {
		t.Run(tc.name, func(t *testing.T) {
			a, closeSrv := newCloudDryRunApplier(t, tc, &tc.listEmpty)
			defer closeSrv()

			dr := cloudDryRun(t, a, tc.withoutID)

			if dr.Action != ActionCreated {
				t.Errorf("action = %q, want %q", dr.Action, ActionCreated)
			}
			if dr.ID != "" {
				t.Errorf("id = %q, want empty for a create", dr.ID)
			}
			if dr.Name != tc.wantName {
				t.Errorf("name = %q, want %q", dr.Name, tc.wantName)
			}
		})
	}
}

// A cloud connection exported before the field projection was fixed carries
// only objectId and value — the Settings API list used to strip schemaId and
// scope — so the flattened "type" field sent the file down the generic document
// path. Dry run then reported a create of a document named after the
// authentication type, and a real apply created one.
func TestApply_LegacyConnectionExport_DetectedAsConnection(t *testing.T) {
	cases := []struct {
		name             string
		payload          string
		wantResourceType ResourceType
		wantID           string
	}{
		{
			name:             "aws",
			payload:          `{"objectId":"aws-conn-legacy","value":{"name":"my-aws-conn","type":"awsRoleBasedAuthentication","awsRoleBasedAuthentication":{"roleArn":"arn:aws:iam::123456789012:role/Dynatrace","consumers":["SVC:com.dynatrace.da"]}},"name":"my-aws-conn","type":"awsRoleBasedAuthentication","roleArn":"arn:aws:iam::123456789012:role/Dynatrace"}`,
			wantResourceType: ResourceSettings,
			wantID:           "aws-conn-legacy",
		},
		{
			name:             "azure",
			payload:          `{"objectId":"azure-conn-legacy","value":{"name":"my-azure-conn","type":"federatedIdentityCredential","federatedIdentityCredential":{"applicationId":"00000000-0000-0000-0000-000000000001","directoryId":"00000000-0000-0000-0000-000000000002","consumers":["SVC:com.dynatrace.da"]}},"name":"my-azure-conn","type":"federatedIdentityCredential"}`,
			wantResourceType: ResourceAzureConnection,
			wantID:           "azure-conn-legacy",
		},
		{
			name:             "gcp",
			payload:          `{"objectId":"gcp-conn-legacy","value":{"name":"my-gcp-conn","type":"serviceAccountImpersonation","serviceAccountImpersonation":{"serviceAccountId":"sa@example.invalid","consumers":["SVC:com.dynatrace.da"]}},"name":"my-gcp-conn","type":"serviceAccountImpersonation"}`,
			wantResourceType: ResourceGCPConnection,
			wantID:           "gcp-conn-legacy",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, isArray, err := detectResourceType([]byte(tc.payload))
			if err != nil {
				t.Fatalf("detectResourceType() error = %v", err)
			}
			if isArray {
				t.Error("isArray = true, want false")
			}
			if got != tc.wantResourceType {
				t.Errorf("resource type = %q, want %q (bug #509: read as a document named after the auth type)", got, tc.wantResourceType)
			}

			srv, c := newApplyTestServer(t, map[string]http.HandlerFunc{
				"/platform/document/v1/documents": func(w http.ResponseWriter, r *http.Request) {
					t.Errorf("dry run reached the documents API — the export was read as a document")
					w.WriteHeader(http.StatusInternalServerError)
				},
				// The settings dry run (the AWS case) looks the object up, as
				// apply does (#521): it exists.
				"/platform/classic/environment-api/v2/settings/objects/aws-conn-legacy": func(w http.ResponseWriter, r *http.Request) {
					w.Header().Set("Content-Type", "application/json")
					w.Write([]byte(`{"objectId":"aws-conn-legacy","schemaId":"builtin:hyperscaler-authentication.connections.aws","scope":"environment","value":{"name":"my-aws-conn","type":"awsRoleBasedAuthentication"}}`))
				},
				"/platform/metadata/v1/user": func(w http.ResponseWriter, r *http.Request) {
					w.WriteHeader(http.StatusUnauthorized)
				},
			})
			defer srv.Close()

			dr := cloudDryRun(t, NewApplier(c), tc.payload)
			if dr.Action != ActionUpdated {
				t.Errorf("action = %q, want %q", dr.Action, ActionUpdated)
			}
			if dr.ID != tc.wantID {
				t.Errorf("id = %q, want %q", dr.ID, tc.wantID)
			}
			if dr.ResourceType != string(tc.wantResourceType) {
				t.Errorf("resourceType = %q, want %q", dr.ResourceType, tc.wantResourceType)
			}
		})
	}
}

// The apply behind that dry run must update the connection through the Settings
// API, not create a document.
func TestApply_LegacyAWSConnectionExport_Updates(t *testing.T) {
	const objectPath = "/platform/classic/environment-api/v2/settings/objects/aws-conn-legacy"
	putCalled := false
	srv, c := newApplyTestServer(t, map[string]http.HandlerFunc{
		objectPath: func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			switch r.Method {
			case http.MethodGet:
				json.NewEncoder(w).Encode(map[string]interface{}{
					"objectId":      "aws-conn-legacy",
					"schemaId":      "builtin:hyperscaler-authentication.connections.aws",
					"schemaVersion": "1",
					"scope":         "environment",
					"value":         map[string]interface{}{"name": "my-aws-conn", "type": "awsRoleBasedAuthentication"},
				})
			case http.MethodPut:
				putCalled = true
				w.WriteHeader(http.StatusNoContent)
			default:
				t.Errorf("unexpected %s on the settings object path", r.Method)
				w.WriteHeader(http.StatusMethodNotAllowed)
			}
		},
		"/platform/document/v1/documents": func(w http.ResponseWriter, r *http.Request) {
			t.Error("apply created a document instead of updating the AWS connection")
			w.WriteHeader(http.StatusInternalServerError)
		},
		"/platform/metadata/v1/user": func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusUnauthorized)
		},
	})
	defer srv.Close()

	payload := `{"objectId":"aws-conn-legacy","value":{"name":"my-aws-conn","type":"awsRoleBasedAuthentication","awsRoleBasedAuthentication":{"roleArn":"arn:aws:iam::123456789012:role/Dynatrace","consumers":["SVC:com.dynatrace.da"]}},"name":"my-aws-conn","type":"awsRoleBasedAuthentication"}`
	results, err := NewApplier(c).Apply([]byte(payload), ApplyOptions{})
	if err != nil {
		t.Fatalf("Apply() error = %v", err)
	}
	if !putCalled {
		t.Error("no PUT was issued: the AWS connection was not updated")
	}
	if len(results) != 1 {
		t.Fatalf("results = %d, want 1", len(results))
	}
	if got := resultAction(t, results[0]); got != ActionUpdated {
		t.Errorf("action = %q, want %q", got, ActionUpdated)
	}
}

// The legacy-export fallback keys off the authentication type in the value, and
// "clientSecret" is a generic enough name that another schema could carry it.
// The hyperscaler connection schemas are discriminated unions — the value holds
// "type" *and* a sub-object named after it — so the fallback demands both. It
// has to: the connection appliers re-marshal the value through their own struct
// and PUT the result, which would strip every field a foreign schema has and
// azureconnection.Value does not model.
func TestDetectResourceType_ForeignValueTypeIsNotAConnection(t *testing.T) {
	cases := []struct {
		name    string
		payload string
		want    ResourceType
	}{
		{
			// No sub-object named after the type: not a connection.
			name:    "clientSecret without discriminant",
			payload: `{"objectId":"foreign-obj","value":{"type":"clientSecret","tokenUrl":"https://example.invalid/token","name":"some-integration"},"type":"clientSecret"}`,
			want:    ResourceDocument,
		},
		{
			name:    "serviceAccountImpersonation without discriminant",
			payload: `{"objectId":"foreign-obj","value":{"type":"serviceAccountImpersonation","audience":"x"},"type":"serviceAccountImpersonation"}`,
			want:    ResourceDocument,
		},
		{
			// A real legacy export does carry the sub-object.
			name:    "clientSecret with discriminant",
			payload: `{"objectId":"azure-obj","value":{"name":"c","type":"clientSecret","clientSecret":{"applicationId":"a","directoryId":"d","consumers":["SVC:com.dynatrace.da"]}},"type":"clientSecret"}`,
			want:    ResourceAzureConnection,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, _, err := detectResourceType([]byte(tc.payload))
			if err != nil {
				t.Fatalf("detectResourceType() error = %v", err)
			}
			if got != tc.want {
				t.Errorf("resource type = %q, want %q", got, tc.want)
			}
		})
	}
}
