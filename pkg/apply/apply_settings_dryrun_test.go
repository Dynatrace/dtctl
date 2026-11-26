package apply

import (
	"net/http"
	"strings"
	"testing"
)

// Regression tests for https://github.com/Dynatrace/dtctl/issues/521
//
// The generic dry-run tail decided the settings action from the presence of an
// objectId alone, so a payload whose objectId no longer exists — and that has
// no schemaId/scope to fall back to a create with — was reported as "updated"
// while the real apply refused it. The settings dry run now resolves create vs
// update exactly as applySettings does.

const settingsDryRunObjectPrefix = "/platform/classic/environment-api/v2/settings/objects/"

// newSettingsDryRunServer serves the settings object lookup. existing names
// the one objectId that exists; lookupStatus, when non-zero, answers every
// lookup with that status instead. Any request other than a GET fails the test.
func newSettingsDryRunServer(t *testing.T, existing string, lookupStatus int) (*Applier, func()) {
	t.Helper()
	srv, c := newApplyTestServer(t, map[string]http.HandlerFunc{
		settingsDryRunObjectPrefix: func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodGet {
				t.Errorf("dry run issued %s %s, want only GET", r.Method, r.URL.Path)
				w.WriteHeader(http.StatusMethodNotAllowed)
				return
			}
			if lookupStatus != 0 {
				w.WriteHeader(lookupStatus)
				return
			}
			id := strings.TrimPrefix(r.URL.Path, settingsDryRunObjectPrefix)
			if id != existing {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusNotFound)
				w.Write([]byte(`{"error":{"code":404,"message":"Settings object not found"}}`))
				return
			}
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`{"objectId":"` + existing + `","schemaId":"builtin:synthetic.schema","schemaVersion":"1","scope":"environment","summary":"Existing","value":{"name":"Existing"}}`))
		},
		"/platform/metadata/v1/user": func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusUnauthorized)
		},
	})
	return NewApplier(c), srv.Close
}

func TestApply_SettingsDryRun_ExistingObjectID_ReportsUpdated(t *testing.T) {
	cases := []struct {
		name       string
		payload    string
		overrideID string
	}{
		{
			name:    "objectId",
			payload: `{"objectId":"obj-exists-521","schemaId":"builtin:synthetic.schema","scope":"environment","value":{"name":"Existing"}}`,
		},
		{
			name:    "objectid",
			payload: `{"objectid":"obj-exists-521","schemaId":"builtin:synthetic.schema","scope":"environment","value":{"name":"Existing"}}`,
		},
		{
			// --id injects doc["id"]; apply honors it, so the dry run must too.
			name:       "--id flag",
			payload:    `{"schemaId":"builtin:synthetic.schema","scope":"environment","value":{"name":"Existing"}}`,
			overrideID: "obj-exists-521",
		},
		{
			// Legacy AWS connection export: no schemaId/scope, which an update
			// does not need.
			name:    "legacy aws connection export",
			payload: `{"objectId":"obj-exists-521","value":{"name":"my-aws-conn","type":"awsRoleBasedAuthentication","awsRoleBasedAuthentication":{"roleArn":"arn:aws:iam::123456789012:role/Example","consumers":["SVC:com.dynatrace.da"]}},"name":"my-aws-conn","type":"awsRoleBasedAuthentication"}`,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			a, done := newSettingsDryRunServer(t, "obj-exists-521", 0)
			defer done()

			results, err := a.Apply([]byte(tc.payload), ApplyOptions{DryRun: true, OverrideID: tc.overrideID})
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
			if dr.Action != ActionUpdated {
				t.Errorf("action = %q, want %q", dr.Action, ActionUpdated)
			}
			if dr.ID != "obj-exists-521" {
				t.Errorf("id = %q, want %q", dr.ID, "obj-exists-521")
			}
			if dr.ResourceType != string(ResourceSettings) {
				t.Errorf("resourceType = %q, want %q", dr.ResourceType, ResourceSettings)
			}
		})
	}
}

func TestApply_SettingsDryRun_MissingObjectIDWithSchemaAndScope_ReportsCreated(t *testing.T) {
	a, done := newSettingsDryRunServer(t, "obj-exists-521", 0)
	defer done()

	dr := cloudDryRun(t, a, `{"objectId":"obj-gone-521","schemaId":"builtin:synthetic.schema","scope":"environment","value":{"name":"New"}}`)
	if dr.Action != ActionCreated {
		t.Errorf("action = %q, want %q (bug #521: a missing objectId was reported as an update)", dr.Action, ActionCreated)
	}
	if dr.ID != "" {
		t.Errorf("id = %q, want empty: the create mints a new objectId", dr.ID)
	}
	if dr.Scope != "environment" {
		t.Errorf("scope = %q, want %q", dr.Scope, "environment")
	}
}

// A missing objectId without the fields a create needs must fail the dry run
// with the very error the apply gives — the case the issue reported.
func TestApply_SettingsDryRun_MissingObjectIDWithoutCreateFields_FailsLikeApply(t *testing.T) {
	cases := []struct {
		name    string
		payload string
		wantErr string
	}{
		{
			// The legacy AWS connection export from the issue: objectId and
			// value only, routed down the settings path.
			name:    "legacy aws connection export",
			payload: `{"objectId":"obj-gone-521","value":{"name":"my-aws-conn","type":"awsRoleBasedAuthentication","awsRoleBasedAuthentication":{"roleArn":"arn:aws:iam::123456789012:role/Example","consumers":["SVC:com.dynatrace.da"]}},"name":"my-aws-conn","type":"awsRoleBasedAuthentication"}`,
			wantErr: `schemaId is required to create a settings object (objectId "obj-gone-521" not found)`,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			a, done := newSettingsDryRunServer(t, "obj-exists-521", 0)
			defer done()

			_, dryErr := a.Apply([]byte(tc.payload), ApplyOptions{DryRun: true})
			if dryErr == nil {
				t.Fatal("dry run succeeded, want the error the apply gives (bug #521)")
			}
			_, applyErr := a.Apply([]byte(tc.payload), ApplyOptions{})
			if applyErr == nil {
				t.Fatal("apply succeeded, want an error")
			}
			if dryErr.Error() != applyErr.Error() {
				t.Errorf("dry run error = %q, apply error = %q: they must agree", dryErr, applyErr)
			}
			if !strings.Contains(dryErr.Error(), tc.wantErr) {
				t.Errorf("error = %q, want it to contain %q", dryErr, tc.wantErr)
			}
		})
	}
}

// A lookup that fails for any reason other than 404 says nothing about whether
// the object exists; the apply stops on it, so the dry run must too.
func TestApply_SettingsDryRun_LookupError_Fails(t *testing.T) {
	a, done := newSettingsDryRunServer(t, "", http.StatusForbidden)
	defer done()

	_, err := a.Apply([]byte(`{"objectId":"obj-521","schemaId":"builtin:synthetic.schema","scope":"environment","value":{"name":"X"}}`), ApplyOptions{DryRun: true})
	if err == nil {
		t.Fatal("dry run succeeded on a 403 lookup, want an error")
	}
	if !strings.Contains(err.Error(), `failed to check settings object "obj-521" existence`) {
		t.Errorf("error = %q, want the lookup failure", err)
	}
}

// A legacy export carries no scope, and its update reports the scope and name
// of the live object. The dry run must report the same object the apply result
// does, not an empty scope read from the payload.
func TestApply_SettingsDryRun_LegacyExportWithoutScope_ReportsLiveObject(t *testing.T) {
	const objectID = "obj-legacy-521"
	liveObject := `{"objectId":"` + objectID + `","schemaId":"builtin:synthetic.schema","schemaVersion":"1","scope":"environment","summary":"my-aws-conn","value":{"name":"my-aws-conn"}}`
	srv, c := newApplyTestServer(t, map[string]http.HandlerFunc{
		settingsDryRunObjectPrefix + objectID: func(w http.ResponseWriter, r *http.Request) {
			switch r.Method {
			case http.MethodGet:
				w.Header().Set("Content-Type", "application/json")
				w.Write([]byte(liveObject))
			case http.MethodPut:
				w.WriteHeader(http.StatusNoContent)
			default:
				t.Errorf("unexpected %s", r.Method)
				w.WriteHeader(http.StatusMethodNotAllowed)
			}
		},
		"/platform/metadata/v1/user": func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusUnauthorized)
		},
	})
	defer srv.Close()
	a := NewApplier(c)

	payload := `{"objectId":"` + objectID + `","value":{"name":"my-aws-conn","type":"awsRoleBasedAuthentication","awsRoleBasedAuthentication":{"roleArn":"arn:aws:iam::123456789012:role/Example","consumers":["SVC:com.dynatrace.da"]}}}`

	dr := cloudDryRun(t, a, payload)

	results, err := a.Apply([]byte(payload), ApplyOptions{})
	if err != nil {
		t.Fatalf("Apply() error = %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("results = %d, want 1", len(results))
	}
	applied, ok := results[0].(*SettingsApplyResult)
	if !ok {
		t.Fatalf("result type = %T, want *SettingsApplyResult", results[0])
	}

	if dr.Scope != "environment" || dr.Scope != applied.Scope {
		t.Errorf("dry run scope = %q, apply scope = %q, want both %q", dr.Scope, applied.Scope, "environment")
	}
	if dr.Name != "my-aws-conn" || dr.Name != applied.Name {
		t.Errorf("dry run name = %q, apply name = %q, want both %q", dr.Name, applied.Name, "my-aws-conn")
	}
	if dr.ID != applied.ID || dr.Action != applied.Action {
		t.Errorf("dry run (%s %q) and apply (%s %q) disagree", dr.Action, dr.ID, applied.Action, applied.ID)
	}
}
