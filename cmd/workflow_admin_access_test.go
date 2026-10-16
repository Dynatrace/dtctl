package cmd

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/dynatrace-oss/dtctl/pkg/auth"
	sdkworkflow "github.com/dynatrace-oss/dtctl/sdk/api/workflow"
)

// runGetWorkflowAgainst runs `get workflow wf-700` through the embedding
// entrypoint against a server that answers the way the Automation API does for
// a caller who does not own the workflow: 403 without admin access, and with it
// either the workflow (the caller holds the scope and permission) or "Only
// admins can request admin access." (they do not). It returns whether the
// request carried adminAccess=true, and the agent envelope.
func runGetWorkflowAgainst(t *testing.T, adminAccess, adminGranted bool) (bool, map[string]any) {
	t.Helper()
	var sent atomic.Bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path != "/platform/automation/v1/workflows/wf-700" {
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"error":{"code":404,"message":"not found"}}`))
			return
		}
		if r.URL.Query().Get("adminAccess") != "true" {
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte(`{"error":{"code":403,"message":"Only workflow owner can perform this action."}}`))
			return
		}
		sent.Store(true)
		if !adminGranted {
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte(`{"error":{"code":403,"message":"Only admins can request admin access."}}`))
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"id": "wf-700", "title": "Alert email", "owner": "group-1", "ownerType": "GROUP"})
	}))
	t.Cleanup(srv.Close)
	isolatedConfig(t, safetyLevelConfig(srv.URL, "readwrite-all"))

	argv := []string{"get", "workflow", "wf-700"}
	if adminAccess {
		argv = append(argv, "--admin-access")
	}
	_, env := runAgentEnvelope(t, argv)
	return sent.Load(), env
}

// envelopeError returns the error object of a failed agent envelope.
func envelopeError(t *testing.T, env map[string]any) map[string]any {
	t.Helper()
	e, ok := env["error"].(map[string]any)
	require.True(t, ok, "expected an error envelope, got %v", env)
	return e
}

// TestGetWorkflow_AdminAccessReachesAnotherOwnersWorkflow pins #700: with
// --admin-access the request carries adminAccess=true, which is what lets an
// administrator read (and so edit or apply) a group-owned workflow.
func TestGetWorkflow_AdminAccessReachesAnotherOwnersWorkflow(t *testing.T) {
	sent, env := runGetWorkflowAgainst(t, true, true)
	require.True(t, sent, "--admin-access must send adminAccess=true")
	require.Equal(t, true, env["ok"], "got %v", env)
}

// A refused --admin-access names both gates instead of passing the bare 403
// through: the scope (and whether this safety level's login requests it) and
// the IAM permission.
func TestGetWorkflow_AdminAccessDeniedIsActionable(t *testing.T) {
	_, env := runGetWorkflowAgainst(t, true, false)
	e := envelopeError(t, env)
	require.Contains(t, e["message"], "Only admins can request admin access.")
	require.Contains(t, e["operation"], "--admin-access")

	raw, _ := json.Marshal(e["suggestions"])
	all := string(raw)
	require.Contains(t, all, auth.WorkflowAdminScope)
	require.Contains(t, all, "run 'dtctl auth login' again")
	require.Contains(t, all, "--check-scopes")
	require.Contains(t, all, "ALLOW "+auth.WorkflowAdminScope+";")
}

// Without the flag a 403 is the ownership refusal the issue reports; the
// admin-access advice would be wrong for it, so it passes through untouched.
func TestGetWorkflow_PlainForbiddenIsUnchanged(t *testing.T) {
	sent, env := runGetWorkflowAgainst(t, false, true)
	require.False(t, sent)
	e := envelopeError(t, env)
	require.Contains(t, e["message"], "Only workflow owner can perform this action.")
	raw, _ := json.Marshal(e)
	require.NotContains(t, string(raw), auth.WorkflowAdminScope,
		"a plain 403 must not get --admin-access advice")
}

// The login scope and the scope the SDK documents for adminAccess are the
// same string, declared twice only because pkg/auth does not import the SDK's
// API packages.
func TestWorkflowAdminScopeMatchesSDK(t *testing.T) {
	require.Equal(t, sdkworkflow.AdminAccessScope, auth.WorkflowAdminScope)
}

// Every command that reaches the Automation API for a single workflow, or its
// executions, declares --admin-access; apply too, since that is how #700 was
// hit.
func TestWorkflowCommandsDeclareAdminAccess(t *testing.T) {
	for _, path := range [][]string{
		{"get", "workflows"},
		{"describe", "workflow"},
		{"edit", "workflow"},
		{"delete", "workflow"},
		{"history", "workflow"},
		{"restore", "workflow"},
		{"get", "workflow-executions"},
		{"describe", "workflow-execution"},
		{"logs", "workflow-execution"},
		{"get", "wfe-task-result"},
		{"apply"},
	} {
		cmd, _, err := rootCmd.Find(path)
		require.NoError(t, err, "%v", path)
		require.NotNil(t, cmd.Flags().Lookup("admin-access"), "%s has no --admin-access", cmd.CommandPath())
	}
}
