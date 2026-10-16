package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/dynatrace-oss/dtctl/cmd/testutil"
)

func newExecWorkflowRunCmdForTest() *cobra.Command {
	cmd := &cobra.Command{
		PreRunE: execWorkflowCmd.PreRunE,
		RunE:    execWorkflowCmd.RunE,
	}
	registerWorkflowExecFlags(cmd)
	return cmd
}

func decodeWorkflowRunRequestBody(t *testing.T, body io.Reader) map[string]any {
	t.Helper()

	var requestBody map[string]any
	if err := json.NewDecoder(body).Decode(&requestBody); err != nil {
		t.Fatalf("failed to decode workflow run request body: %v", err)
	}

	return requestBody
}

func TestExecWorkflowRunE_SendsWorkflowInputRequest(t *testing.T) {
	ms := testutil.NewMockServer(t, map[string]http.HandlerFunc{
		"/platform/automation/v1/workflows/wf-123/run": func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodPost {
				t.Fatalf("unexpected method: %s", r.Method)
			}
			if got := r.URL.Query().Get("monitor"); got != "" {
				t.Fatalf("expected monitor query param to be omitted without --wait, got %q", got)
			}

			requestBody := decodeWorkflowRunRequestBody(t, r.Body)
			input, ok := requestBody["input"].(map[string]any)
			if !ok {
				t.Fatalf("expected request body to contain workflow input, got %#v", requestBody["input"])
			}
			if input["enabled"] != true {
				t.Fatalf("expected enabled=true, got %#v", input["enabled"])
			}
			if input["count"] != float64(2) {
				t.Fatalf("expected count=2, got %#v", input["count"])
			}
			if _, exists := requestBody["params"]; exists {
				t.Fatalf("expected workflow input request to omit legacy params, got %#v", requestBody)
			}

			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"id":"exec-123","workflow":"wf-123","state":"RUNNING"}`))
		},
	})
	defer ms.Close()

	configPath, cleanup := testutil.SetupTestConfig(t, ms.URL)
	defer cleanup()

	origCfgFile := cfgFile(context.Background())
	defer func() {
		gFlags.cfgFile = origCfgFile
	}()
	gFlags.cfgFile = configPath

	cmd := newExecWorkflowRunCmdForTest()
	_ = cmd.Flags().Set("input", `{"enabled":true,"count":2}`)

	err := cmd.RunE(cmd, []string{"wf-123"})
	if err != nil {
		t.Fatalf("RunE() error = %v", err)
	}
	if ms.RequestCount != 1 {
		t.Fatalf("expected 1 request, got %d", ms.RequestCount)
	}
}

func TestExecWorkflowRunE_SetsMonitorQueryParamWhenWaiting(t *testing.T) {
	ms := testutil.NewMockServer(t, map[string]http.HandlerFunc{
		"/platform/automation/v1/workflows/wf-123/run": func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodPost {
				t.Fatalf("unexpected method: %s", r.Method)
			}
			if got := r.URL.Query().Get("monitor"); got != "true" {
				t.Fatalf("expected monitor=true query param when waiting, got %q", got)
			}

			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"id":"exec-123","workflow":"wf-123","state":"RUNNING"}`))
		},
		"/platform/automation/v1/executions/exec-123": func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"id":"exec-123","workflow":"wf-123","state":"SUCCESS","runtime":0}`))
		},
	})
	defer ms.Close()

	configPath, cleanup := testutil.SetupTestConfig(t, ms.URL)
	defer cleanup()

	origCfgFile := cfgFile(context.Background())
	defer func() {
		gFlags.cfgFile = origCfgFile
	}()
	gFlags.cfgFile = configPath

	cmd := newExecWorkflowRunCmdForTest()
	_ = cmd.Flags().Set("wait", "true")

	err := cmd.RunE(cmd, []string{"wf-123"})
	if err != nil {
		t.Fatalf("RunE() error = %v", err)
	}
	if ms.RequestCount != 2 {
		t.Fatalf("expected 2 requests (run + poll), got %d", ms.RequestCount)
	}
}

func TestExecWorkflowRunE_SendsLegacyParamsCompatibilityRequestAndWarning(t *testing.T) {
	ms := testutil.NewMockServer(t, map[string]http.HandlerFunc{
		"/platform/automation/v1/workflows/wf-123/run": func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodPost {
				t.Fatalf("unexpected method: %s", r.Method)
			}

			requestBody := decodeWorkflowRunRequestBody(t, r.Body)
			params, ok := requestBody["params"].(map[string]any)
			if !ok {
				t.Fatalf("expected request body to contain legacy params, got %#v", requestBody["params"])
			}
			if params["severity"] != "high" {
				t.Fatalf("expected severity=high, got %#v", params["severity"])
			}
			if _, exists := requestBody["input"]; exists {
				t.Fatalf("expected legacy params compatibility request to omit workflow input, got %#v", requestBody)
			}

			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"id":"exec-456","workflow":"wf-123","state":"RUNNING"}`))
		},
	})
	defer ms.Close()

	configPath, cleanup := testutil.SetupTestConfig(t, ms.URL)
	defer cleanup()

	origCfgFile := cfgFile(context.Background())
	defer func() {
		gFlags.cfgFile = origCfgFile
	}()
	gFlags.cfgFile = configPath

	cmd := newExecWorkflowRunCmdForTest()
	var stderr bytes.Buffer
	cmd.SetErr(&stderr)
	cmd.Flags().SetOutput(&stderr)

	err := cmd.ParseFlags([]string{"--params", "severity=high"})
	if err != nil {
		t.Fatalf("ParseFlags() error = %v", err)
	}
	if !strings.Contains(stderr.String(), "deprecated") {
		t.Fatalf("expected deprecation warning, got %q", stderr.String())
	}

	err = cmd.RunE(cmd, []string{"wf-123"})
	if err != nil {
		t.Fatalf("RunE() error = %v", err)
	}
	if ms.RequestCount != 1 {
		t.Fatalf("expected 1 request, got %d", ms.RequestCount)
	}
}

func TestBuildWorkflowExecutionRequest_BuildsWorkflowInputFromFlagValue(t *testing.T) {
	request, err := buildWorkflowExecutionRequestFromValues(
		[]string{`{"severity":"high","count":3,"options":{"dryRun":true},"items":["a",false,null]}`},
		nil,
	)
	if err != nil {
		t.Fatalf("buildWorkflowExecutionRequest() error = %v", err)
	}
	if request.Input["severity"] != "high" {
		t.Fatalf("expected severity=high, got %#v", request.Input["severity"])
	}
	if request.Input["count"] != float64(3) {
		t.Fatalf("expected count=3, got %#v", request.Input["count"])
	}
	options, ok := request.Input["options"].(map[string]any)
	if !ok || options["dryRun"] != true {
		t.Fatalf("expected options.dryRun=true, got %#v", request.Input["options"])
	}
	items, ok := request.Input["items"].([]any)
	if !ok || len(items) != 3 || items[2] != nil {
		t.Fatalf("expected items to preserve JSON array semantics, got %#v", request.Input["items"])
	}
	if request.Params != nil {
		t.Fatalf("did not expect params in input request, got %#v", request.Params)
	}
}

func TestBuildWorkflowExecutionRequest_RejectsInvalidWorkflowInput(t *testing.T) {
	tests := []struct {
		name            string
		inputValues     []string
		wantErrContains string
	}{
		{
			name:            "empty input",
			inputValues:     []string{""},
			wantErrContains: "--input must not be empty",
		},
		{
			name:            "invalid json",
			inputValues:     []string{`{"severity":`},
			wantErrContains: "invalid value for --input",
		},
		{
			name:            "non-object json",
			inputValues:     []string{`[1,2,3]`},
			wantErrContains: "--input must be a JSON object",
		},
		{
			name:            "repeated input flag",
			inputValues:     []string{`{"first":true}`, `{"second":true}`},
			wantErrContains: "--input may only be provided once",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := buildWorkflowExecutionRequestFromValues(tt.inputValues, nil)
			if err == nil {
				t.Fatal("expected invalid workflow input to be rejected")
			}
			if !strings.Contains(err.Error(), tt.wantErrContains) {
				t.Fatalf("expected error containing %q, got %v", tt.wantErrContains, err)
			}
		})
	}
}

func TestExecWorkflowInputFlagSurvivesFlagReset(t *testing.T) {
	// resetFlagSet restores defaults via flag.Value.Set(DefValue); for
	// singleUseStringValue that must go through Reset, or the flag is marked
	// as already provided and every real --input is rejected.
	cmd := newExecWorkflowRunCmdForTest()
	resetFlagSet(cmd.Flags())

	if err := cmd.ParseFlags([]string{"--input", `{"matcher":"Control-M"}`}); err != nil {
		t.Fatalf("ParseFlags() after resetFlagSet error = %v", err)
	}
	got, _ := cmd.Flags().GetString("input")
	if got != `{"matcher":"Control-M"}` {
		t.Fatalf("expected --input value to be retained, got %q", got)
	}
}

func TestExecWorkflowParamsFlagHiddenFromUsage(t *testing.T) {
	usage := newExecWorkflowRunCmdForTest().UsageString()
	if strings.Contains(usage, "--params") {
		t.Fatalf("expected --params to be hidden from usage, got: %s", usage)
	}
}

func TestExecWorkflowParamsFlagEmitsDeprecationWarning(t *testing.T) {
	var stderr bytes.Buffer
	cmd := newExecWorkflowRunCmdForTest()
	cmd.SetErr(&stderr)
	cmd.Flags().SetOutput(&stderr)

	err := cmd.ParseFlags([]string{"--params", "env=prod"})
	if err != nil {
		t.Fatalf("ParseFlags() error = %v", err)
	}

	output := stderr.String()
	if !strings.Contains(output, "deprecated") {
		t.Fatalf("expected deprecation warning, got: %q", output)
	}
	if !strings.Contains(output, "--input") {
		t.Fatalf("expected migration guidance in warning, got: %q", output)
	}
}

func TestExecWorkflowInputHelpType(t *testing.T) {
	usage := newExecWorkflowRunCmdForTest().UsageString()
	if !strings.Contains(usage, "--input string") {
		t.Fatalf("expected --input help to show string type, got: %s", usage)
	}
}

func TestExecWorkflowExampleFormatting(t *testing.T) {
	help := execWorkflowCmd.HelpTemplate()
	_ = help
	if strings.Contains(execWorkflowCmd.Example, "\n\t# Execute with workflow input") {
		t.Fatalf("expected workflow example to avoid tab indentation, got: %q", execWorkflowCmd.Example)
	}
}
