package apply

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

// TestApplyWorkflow_AdminAccess pins #700: `apply --admin-access` must send
// adminAccess=true on the existence check as well as on the update. With the
// flag on the PUT alone, the GET before it is what the API refuses with 403
// "Only workflow owner can perform this action" — the error the issue reports.
func TestApplyWorkflow_AdminAccess(t *testing.T) {
	seen := map[string]string{}
	srv, c := newApplyTestServer(t, map[string]http.HandlerFunc{
		"/platform/automation/v1/workflows/wf-700": func(w http.ResponseWriter, r *http.Request) {
			seen[r.Method] = r.URL.Query().Get("adminAccess")
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]any{"id": "wf-700", "title": "Alert email", "owner": "group-1", "ownerType": "GROUP"})
		},
		"/platform/metadata/v1/user": func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusUnauthorized)
		},
	})
	defer srv.Close()

	results, err := NewApplier(c).Apply(
		[]byte(`{"id":"wf-700","title":"Alert email","tasks":{},"trigger":{}}`),
		ApplyOptions{AdminAccess: true},
	)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("got %d results, want 1", len(results))
	}
	if wf, ok := results[0].(*WorkflowApplyResult); !ok || wf.Action != ActionUpdated {
		t.Fatalf("result = %+v, want a workflow update", results[0])
	}
	for _, method := range []string{http.MethodGet, http.MethodPut} {
		if seen[method] != "true" {
			t.Errorf("%s adminAccess = %q, want \"true\"", method, seen[method])
		}
	}
}

// TestApply_AdminAccessRefusedForNonWorkflow: only the Automation API has the
// parameter. Applying a dashboard without it while the caller asked for admin
// access would look like success as an administrator, so apply refuses, before
// any request, in both the real and the dry run.
func TestApply_AdminAccessRefusedForNonWorkflow(t *testing.T) {
	srv, c := newApplyTestServer(t, map[string]http.HandlerFunc{})
	defer srv.Close()

	for _, dryRun := range []bool{false, true} {
		_, err := NewApplier(c).Apply(
			[]byte(`{"type":"dashboard","id":"dash-700","tiles":{"items":[]}}`),
			ApplyOptions{AdminAccess: true, DryRun: dryRun},
		)
		if err == nil || !strings.Contains(err.Error(), "--admin-access applies to workflows only") {
			t.Errorf("dryRun=%v: err = %v, want the workflows-only refusal", dryRun, err)
		}
	}
}
