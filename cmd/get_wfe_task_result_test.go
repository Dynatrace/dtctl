package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/spf13/cobra"

	"github.com/dynatrace-oss/dtctl/cmd/testutil"
	"github.com/dynatrace-oss/dtctl/pkg/client"
	"github.com/dynatrace-oss/dtctl/pkg/exec"
	"github.com/dynatrace-oss/dtctl/pkg/output"
	workflowpkg "github.com/dynatrace-oss/dtctl/pkg/resources/workflow"
)

func newExecWorkflowCmdForTest() *cobra.Command {
	cmd := &cobra.Command{PreRunE: execWorkflowCmd.PreRunE}
	registerWorkflowExecFlags(cmd)
	return cmd
}

func TestGetWfeTaskResult_RunE(t *testing.T) {
	ms := testutil.NewMockServer(t, map[string]http.HandlerFunc{
		"/platform/automation/v1/executions/exec-123/tasks/rca_analysis/result": func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodGet {
				t.Errorf("unexpected method: %s", r.Method)
			}
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]any{
				"results": []any{
					map[string]any{"serviceId": "SVC-001", "score": 0.95},
				},
			})
		},
	})
	defer ms.Close()

	configPath, cleanup := testutil.SetupTestConfig(t, ms.URL)
	defer cleanup()

	origCfgFile := cfgFile(context.Background())
	origOutputFormat := outputFormat(context.Background())
	origPlainMode := plainMode(context.Background())
	defer func() {
		gFlags.cfgFile = origCfgFile
		gFlags.outputFormat = origOutputFormat
		gFlags.plainMode = origPlainMode
	}()

	gFlags.cfgFile = configPath
	gFlags.outputFormat = "json"
	gFlags.plainMode = true

	testutil.ResetCommandFlags(getWfeTaskResultCmd)
	_ = getWfeTaskResultCmd.Flags().Set("task", "rca_analysis")

	err := getWfeTaskResultCmd.RunE(getWfeTaskResultCmd, []string{"exec-123"})
	if err != nil {
		t.Fatalf("RunE returned error: %v", err)
	}

	if ms.RequestCount != 1 {
		t.Errorf("expected 1 request, got %d", ms.RequestCount)
	}
}

func TestGetWfeTaskResult_MissingTaskFlag(t *testing.T) {
	// PreRunE / MarkFlagRequired should reject calls without --task
	testutil.ResetCommandFlags(getWfeTaskResultCmd)

	// When the flag is marked required, Cobra enforces it during Execute.
	// Test the behavior by checking that the flag has no default.
	taskFlag := getWfeTaskResultCmd.Flags().Lookup("task")
	if taskFlag == nil {
		t.Fatal("expected --task flag to exist")
	}
	if taskFlag.DefValue != "" {
		t.Errorf("expected --task default to be empty, got %q", taskFlag.DefValue)
	}
}

func TestGetWfeTaskResult_AgentMode(t *testing.T) {
	ms := testutil.NewMockServer(t, map[string]http.HandlerFunc{
		"/platform/automation/v1/executions/exec-456/tasks/my_task/result": func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]any{"answer": 42})
		},
	})
	defer ms.Close()

	c, err := client.NewForTesting(ms.URL, "dt0c01.ST.test.secret")
	if err != nil {
		t.Fatalf("NewForTesting: %v", err)
	}

	handler := workflowpkg.NewExecutionHandler(c)
	result, err := handler.GetTaskResult("exec-456", "my_task")
	if err != nil {
		t.Fatalf("GetTaskResult: %v", err)
	}

	// Verify agent printer produces valid envelope
	var buf bytes.Buffer
	ctx := &output.ResponseContext{}
	ap := output.NewAgentPrinter(&buf, ctx)
	ap = enrichAgent(ap, "get", "wfe-task-result")
	if ap == nil {
		t.Fatal("expected non-nil AgentPrinter")
	}
	ap.SetSuggestions([]string{
		"Run 'dtctl get wfe exec-456' to view the full execution",
	})

	if err := ap.Print(result); err != nil {
		t.Fatalf("AgentPrinter.Print: %v", err)
	}

	// Parse the agent envelope
	var envelope map[string]any
	if err := json.Unmarshal(buf.Bytes(), &envelope); err != nil {
		t.Fatalf("failed to parse agent envelope: %v\nraw: %s", err, buf.String())
	}

	if envelope["ok"] != true {
		t.Errorf("expected ok=true, got %v", envelope["ok"])
	}

	resultData, ok := envelope["result"].(map[string]any)
	if !ok {
		t.Fatalf("expected result to be a map, got %T", envelope["result"])
	}
	if resultData["answer"] != float64(42) {
		t.Errorf("expected answer=42, got %v", resultData["answer"])
	}

	context, ok := envelope["context"].(map[string]any)
	if !ok {
		t.Fatalf("expected context to be a map, got %T", envelope["context"])
	}
	if context["verb"] != "get" {
		t.Errorf("expected verb=get, got %v", context["verb"])
	}
	if context["resource"] != "wfe-task-result" {
		t.Errorf("expected resource=wfe-task-result, got %v", context["resource"])
	}
}

func TestExecWorkflowAgent_Success(t *testing.T) {
	ms := testutil.NewMockServer(t, map[string]http.HandlerFunc{
		"/platform/automation/v1/executions/exec-789": func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]any{
				"id":       "exec-789",
				"workflow": "wf-abc",
				"state":    "SUCCESS",
				"runtime":  7,
			})
		},
		"/platform/automation/v1/executions/exec-789/tasks": func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]any{
				"js_task": map[string]any{
					"id": "t1", "name": "js_task", "state": "SUCCESS",
					"result": map[string]any{"value": 42},
				},
			})
		},
	})
	defer ms.Close()

	c, err := client.NewForTesting(ms.URL, "dt0c01.ST.test.secret")
	if err != nil {
		t.Fatalf("NewForTesting: %v", err)
	}

	executor := exec.NewWorkflowExecutor(c)

	// Build a mock execution response (as if Execute() just returned)
	execResult := &exec.WorkflowExecutionResponse{
		ID:       "exec-789",
		Workflow: "wf-abc",
		State:    "RUNNING",
	}

	// Set up a command with --wait and --show-results flags
	cmd := newExecWorkflowCmdForTest()
	_ = cmd.Flags().Set("wait", "true")
	_ = cmd.Flags().Set("show-results", "true")

	var buf bytes.Buffer
	ctx := &output.ResponseContext{}
	ap := output.NewAgentPrinter(&buf, ctx)

	err = execWorkflowAgent(cmd, c, executor, execResult, ap)
	if err != nil {
		t.Fatalf("execWorkflowAgent: %v", err)
	}

	// Parse the agent envelope
	var envelope map[string]any
	if err := json.Unmarshal(buf.Bytes(), &envelope); err != nil {
		t.Fatalf("failed to parse agent envelope: %v\nraw: %s", err, buf.String())
	}

	if envelope["ok"] != true {
		t.Errorf("expected ok=true, got %v", envelope["ok"])
	}

	resultData, ok := envelope["result"].(map[string]any)
	if !ok {
		t.Fatalf("expected result to be a map, got %T", envelope["result"])
	}

	if resultData["executionId"] != "exec-789" {
		t.Errorf("expected executionId=exec-789, got %v", resultData["executionId"])
	}
	if resultData["state"] != "SUCCESS" {
		t.Errorf("expected state=SUCCESS, got %v", resultData["state"])
	}

	// Verify tasks were included
	tasks, ok := resultData["tasks"].([]any)
	if !ok {
		t.Fatalf("expected tasks to be an array, got %T", resultData["tasks"])
	}
	if len(tasks) != 1 {
		t.Errorf("expected 1 task, got %d", len(tasks))
	}
	task0 := tasks[0].(map[string]any)
	if task0["name"] != "js_task" {
		t.Errorf("expected task name=js_task, got %v", task0["name"])
	}
	taskResult := task0["result"].(map[string]any)
	if taskResult["value"] != float64(42) {
		t.Errorf("expected task result value=42, got %v", taskResult["value"])
	}

	// Verify suggestions are set
	agentCtx, ok := envelope["context"].(map[string]any)
	if !ok {
		t.Fatalf("expected context to be a map, got %T", envelope["context"])
	}
	suggestions, ok := agentCtx["suggestions"].([]any)
	if !ok || len(suggestions) == 0 {
		t.Error("expected non-empty suggestions in agent context")
	}
}

func TestExecWorkflowAgent_NoWait(t *testing.T) {
	execResult := &exec.WorkflowExecutionResponse{
		ID:       "exec-no-wait",
		Workflow: "wf-xyz",
		State:    "RUNNING",
	}

	cmd := newExecWorkflowCmdForTest()
	// --wait defaults to false

	var buf bytes.Buffer
	ctx := &output.ResponseContext{}
	ap := output.NewAgentPrinter(&buf, ctx)

	// No mock server needed — without --wait we don't poll
	err := execWorkflowAgent(cmd, nil, nil, execResult, ap)
	if err != nil {
		t.Fatalf("execWorkflowAgent: %v", err)
	}

	var envelope map[string]any
	if err := json.Unmarshal(buf.Bytes(), &envelope); err != nil {
		t.Fatalf("failed to parse: %v", err)
	}

	resultData := envelope["result"].(map[string]any)
	if resultData["state"] != "RUNNING" {
		t.Errorf("expected state=RUNNING, got %v", resultData["state"])
	}
	// tasks should be absent when not waiting
	if resultData["tasks"] != nil {
		t.Errorf("expected no tasks when --wait not set, got %v", resultData["tasks"])
	}
}

func TestExecWorkflowAgent_ErrorState(t *testing.T) {
	ms := testutil.NewMockServer(t, map[string]http.HandlerFunc{
		"/platform/automation/v1/executions/exec-err": func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]any{
				"id":        "exec-err",
				"workflow":  "wf-err",
				"state":     "ERROR",
				"stateInfo": "task failed: timeout",
				"runtime":   3,
			})
		},
	})
	defer ms.Close()

	c, err := client.NewForTesting(ms.URL, "dt0c01.ST.test.secret")
	if err != nil {
		t.Fatalf("NewForTesting: %v", err)
	}

	executor := exec.NewWorkflowExecutor(c)
	execResult := &exec.WorkflowExecutionResponse{
		ID:       "exec-err",
		Workflow: "wf-err",
		State:    "RUNNING",
	}

	cmd := newExecWorkflowCmdForTest()
	_ = cmd.Flags().Set("wait", "true")

	var buf bytes.Buffer
	ctx := &output.ResponseContext{}
	ap := output.NewAgentPrinter(&buf, ctx)

	err = execWorkflowAgent(cmd, c, executor, execResult, ap)
	if err != nil {
		t.Fatalf("execWorkflowAgent should not return error (it sets warnings): %v", err)
	}

	var envelope map[string]any
	if err := json.Unmarshal(buf.Bytes(), &envelope); err != nil {
		t.Fatalf("failed to parse: %v", err)
	}

	resultData := envelope["result"].(map[string]any)
	if resultData["state"] != "ERROR" {
		t.Errorf("expected state=ERROR, got %v", resultData["state"])
	}

	// Verify warnings are set for error state
	agentCtx := envelope["context"].(map[string]any)
	warnings, ok := agentCtx["warnings"].([]any)
	if !ok || len(warnings) == 0 {
		t.Error("expected warnings for ERROR state")
	}
}

func TestExecWorkflowShowResults_PreRunE(t *testing.T) {
	// --show-results without --wait should fail
	cmd := newExecWorkflowCmdForTest()
	_ = cmd.Flags().Set("show-results", "true")

	err := cmd.PreRunE(cmd, []string{"wf-123"})
	if err == nil {
		t.Fatal("expected error when --show-results used without --wait")
	}
	if err.Error() != "--show-results requires --wait" {
		t.Errorf("unexpected error message: %s", err.Error())
	}

	// --show-results with --wait should pass
	_ = cmd.Flags().Set("wait", "true")
	err = cmd.PreRunE(cmd, []string{"wf-123"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}
