package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/dynatrace-oss/dtctl/cmd/testutil"
	"github.com/dynatrace-oss/dtctl/pkg/resources/workflow"
)

func TestPrintWorkflowDescribeTableGolden(t *testing.T) {
	result := "{{ result('deploy') }}"
	wf := &workflow.Workflow{
		ID:         "wf-123",
		Title:      "Deploy to Production",
		Owner:      "user-123",
		OwnerType:  "USER",
		Private:    false,
		Type:       "STANDARD",
		IsDeployed: true,
		Result:     &result,
		Input: map[string]interface{}{
			"environment": map[string]interface{}{"type": "string"},
		},
		Tasks: map[string]interface{}{
			"deploy": map[string]interface{}{"action": "dynatrace.automations:run-javascript"},
		},
	}

	var buf bytes.Buffer
	printWorkflowDescribeTable(&buf, wf, nil)

	want := "ID:          wf-123\nTitle:       Deploy to Production\nOwner:       user-123 (USER)\nPrivate:     false\nDeployed:    true\nType:        STANDARD\n\nResult:      {{ result('deploy') }}\n\nInput:\n  {\n    \"environment\": {\n      \"type\": \"string\"\n    }\n  }\n\nTasks:\n  - deploy (dynatrace.automations:run-javascript)\n"
	if got := buf.String(); got != want {
		t.Fatalf("printWorkflowDescribeTable() = %q, want %q", got, want)
	}
}

func TestPrintWorkflowDefinitionFields(t *testing.T) {
	result := "{{ result('deploy') }}"
	wf := &workflow.Workflow{
		Result: &result,
		Input: map[string]interface{}{
			"environment": map[string]interface{}{"type": "string"},
		},
	}

	var buf bytes.Buffer
	printWorkflowDefinitionFields(&buf, 13, wf)

	want := "\nResult:      {{ result('deploy') }}\n\nInput:\n  {\n    \"environment\": {\n      \"type\": \"string\"\n    }\n  }\n"
	if got := buf.String(); got != want {
		t.Fatalf("printWorkflowDefinitionFields() = %q, want %q", got, want)
	}
}

func TestPrintWorkflowDefinitionFieldsSkipsInvalidInputJSON(t *testing.T) {
	result := "{{ result('deploy') }}"
	wf := &workflow.Workflow{
		Result: &result,
		Input: map[string]interface{}{
			"invalid": func() {},
		},
	}

	var buf bytes.Buffer
	printWorkflowDefinitionFields(&buf, 13, wf)

	got := buf.String()
	if !strings.Contains(got, "Result:      {{ result('deploy') }}") {
		t.Fatalf("expected result to be printed before invalid input is skipped, got %q", got)
	}
	if strings.Contains(got, "Input:") {
		t.Fatalf("expected invalid input JSON to be skipped, got %q", got)
	}
}

func TestPrintWorkflowDescribeTableIncludesTriggerAndRecentExecutions(t *testing.T) {
	startedAt := time.Date(2026, 5, 18, 12, 30, 0, 0, time.UTC)
	wf := &workflow.Workflow{
		ID:          "wf-456",
		Title:       "Incident Response",
		Description: "Handles incident workflows",
		Owner:       "user-456",
		OwnerType:   "USER",
		Private:     true,
		Type:        "STANDARD",
		IsDeployed:  false,
		Trigger: map[string]interface{}{
			"type": "event",
			"schedule": map[string]interface{}{
				"rule":     "0 9 * * 1-5",
				"timezone": "UTC",
			},
		},
		Tasks: map[string]interface{}{
			"notify": "plain-task",
		},
	}
	execList := &workflow.ExecutionList{
		Count: 1,
		Results: []workflow.Execution{
			{ID: "12345678-aaaa-bbbb-cccc-1234567890ab", State: "SUCCEEDED", StartedAt: startedAt, Runtime: 61},
		},
	}

	var buf bytes.Buffer
	printWorkflowDescribeTable(&buf, wf, execList)

	got := buf.String()
	checks := []string{
		"Description: Handles incident workflows",
		"Trigger:     Schedule",
		"Tasks:\n  - notify",
		"Recent Executions:\n  - 12345678...  SUCCEEDED",
	}
	for _, want := range checks {
		if !strings.Contains(got, want) {
			t.Fatalf("expected output to contain %q, got %q", want, got)
		}
	}
}

func TestPrintWorkflowDescribeTableSkipsNilScheduleRuleAndUsesNestedTrigger(t *testing.T) {
	wf := &workflow.Workflow{
		ID:         "wf-789",
		Title:      "Daily Cleanup",
		Owner:      "user-789",
		OwnerType:  "USER",
		Private:    false,
		Type:       "STANDARD",
		IsDeployed: true,
		Trigger: map[string]interface{}{
			"schedule": map[string]interface{}{
				"rule": nil,
				"trigger": map[string]interface{}{
					"type": "cron",
					"cron": "0 9 * * 1-5",
				},
				"timezone": "UTC",
			},
		},
	}

	var buf bytes.Buffer
	printWorkflowDescribeTable(&buf, wf, nil)

	got := buf.String()
	if strings.Contains(got, "<nil>") {
		t.Fatalf("expected output to skip nil trigger values, got %q", got)
	}
	if !strings.Contains(got, "Trigger:     Schedule (cron)") {
		t.Fatalf("expected schedule subtype summary to be rendered, got %q", got)
	}
}

func TestPrintWorkflowDescribeTableShowsTimeBasedNestedScheduleTrigger(t *testing.T) {
	wf := &workflow.Workflow{
		ID:         "wf-999",
		Title:      "Morning Run",
		Owner:      "user-999",
		OwnerType:  "USER",
		Private:    false,
		Type:       "STANDARD",
		IsDeployed: true,
		Trigger: map[string]interface{}{
			"schedule": map[string]interface{}{
				"rule":     nil,
				"timezone": "UTC",
				"trigger": map[string]interface{}{
					"type": "time",
					"time": "10:00",
				},
			},
		},
	}

	var buf bytes.Buffer
	printWorkflowDescribeTable(&buf, wf, nil)

	got := buf.String()
	for _, want := range []string{"Type:        STANDARD", "Trigger:     Schedule (time)"} {
		if !strings.Contains(got, want) {
			t.Fatalf("expected output to contain %q, got %q", want, got)
		}
	}
	if strings.Contains(got, "<nil>") {
		t.Fatalf("expected output to skip nil trigger values, got %q", got)
	}
}

func TestPrintWorkflowDescribeTableShowsGenericEventTriggerSummary(t *testing.T) {
	wf := &workflow.Workflow{
		ID:         "wf-event",
		Title:      "Generic Event Workflow",
		Owner:      "user-event",
		OwnerType:  "USER",
		Private:    false,
		Type:       "STANDARD",
		IsDeployed: true,
		Trigger: map[string]interface{}{
			"eventTrigger": map[string]interface{}{
				"triggerConfiguration": map[string]interface{}{
					"type": "event",
				},
			},
		},
	}

	var buf bytes.Buffer
	printWorkflowDescribeTable(&buf, wf, nil)

	if got := buf.String(); !strings.Contains(got, "Trigger:     Event (event)") {
		t.Fatalf("expected generic event trigger summary, got %q", got)
	}
}

func TestPrintWorkflowDescribeTableShowsDavisProblemTriggerSummary(t *testing.T) {
	wf := &workflow.Workflow{
		ID:         "wf-davis-problem",
		Title:      "Davis Problem Workflow",
		Owner:      "user-davis",
		OwnerType:  "USER",
		Private:    false,
		Type:       "STANDARD",
		IsDeployed: true,
		Trigger: map[string]interface{}{
			"eventTrigger": map[string]interface{}{
				"triggerConfiguration": map[string]interface{}{
					"type": "davis-problem",
				},
			},
		},
	}

	var buf bytes.Buffer
	printWorkflowDescribeTable(&buf, wf, nil)

	if got := buf.String(); !strings.Contains(got, "Trigger:     Event (davis-problem)") {
		t.Fatalf("expected davis problem trigger summary, got %q", got)
	}
}

func TestPrintWorkflowDescribeTableShowsMoreRecentExecutionsNotice(t *testing.T) {
	startedAt := time.Date(2026, 5, 18, 12, 30, 0, 0, time.UTC)
	execs := make([]workflow.Execution, 6)
	for i := range execs {
		execs[i] = workflow.Execution{
			ID:        "12345678-aaaa-bbbb-cccc-1234567890ab",
			State:     "SUCCEEDED",
			StartedAt: startedAt,
			Runtime:   61,
		}
	}

	var buf bytes.Buffer
	printWorkflowDescribeTable(&buf, &workflow.Workflow{}, &workflow.ExecutionList{Count: 6, Results: execs})

	got := buf.String()
	if !strings.Contains(got, "... and 1 more") {
		t.Fatalf("expected overflow notice in output, got %q", got)
	}
	if strings.Count(got, "12345678...") != 5 {
		t.Fatalf("expected only 5 recent executions to be printed, got %q", got)
	}
}

func TestPrintWorkflowDefinitionFieldsSkipsEmptyValues(t *testing.T) {
	wf := &workflow.Workflow{
		Input: map[string]interface{}{},
	}

	var buf bytes.Buffer
	printWorkflowDefinitionFields(&buf, 13, wf)

	if got := buf.String(); got != "" {
		t.Fatalf("printWorkflowDefinitionFields() = %q, want empty output", got)
	}
}

func TestFormatTriggerSummaryWithoutSubtype(t *testing.T) {
	if got := formatTriggerSummary("schedule", ""); got != "Schedule" {
		t.Fatalf("formatTriggerSummary() = %q, want %q", got, "Schedule")
	}
}

func TestFormatTriggerSummaryEmptyFamily(t *testing.T) {
	if got := formatTriggerSummary("Trigger", "cron"); got != "" {
		t.Fatalf("formatTriggerSummary() = %q, want empty", got)
	}
}

func TestNestedTriggerStringEdgeCases(t *testing.T) {
	trigger := map[string]interface{}{
		"schedule": map[string]interface{}{
			"trigger": map[string]interface{}{
				"type": "cron",
			},
			"nonStringType": map[string]interface{}{
				"type": 42,
			},
			"invalid": "not-a-map",
		},
	}

	if got := nestedTriggerString(trigger, "schedule", "trigger", "type"); got != "cron" {
		t.Fatalf("nestedTriggerString() = %q, want %q", got, "cron")
	}
	if got := nestedTriggerString(trigger, "schedule", "missing", "type"); got != "" {
		t.Fatalf("nestedTriggerString() missing path = %q, want empty", got)
	}
	if got := nestedTriggerString(trigger, "schedule", "invalid", "type"); got != "" {
		t.Fatalf("nestedTriggerString() non-map path = %q, want empty", got)
	}
	if got := nestedTriggerString(trigger, "schedule", "nonStringType", "type"); got != "" {
		t.Fatalf("nestedTriggerString() non-string leaf = %q, want empty", got)
	}
}

func TestTriggerSummaryUnknownTrigger(t *testing.T) {
	if got := triggerSummary(map[string]interface{}{"manual": true}); got != "" {
		t.Fatalf("triggerSummary() = %q, want empty", got)
	}
}

func TestTriggerSummaryNilTrigger(t *testing.T) {
	var trigger map[string]interface{}
	if got := triggerSummary(trigger); got != "" {
		t.Fatalf("triggerSummary(nil) = %q, want empty", got)
	}
}

func TestDescribeWorkflowCmd_TableOutputFetchesExecutions(t *testing.T) {
	ms := testutil.NewMockServer(t, map[string]http.HandlerFunc{
		"/platform/automation/v1/workflows/wf-describe-1": func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]any{
				"id": "wf-describe-1", "title": "Describe Me", "owner": "u1", "ownerType": "USER",
				"type": "STANDARD", "isDeployed": true,
			})
		},
		"/platform/automation/v1/executions": func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]any{"count": 0, "results": []any{}})
		},
	})
	defer ms.Close()

	configPath, cleanup := testutil.SetupTestConfig(t, ms.URL)
	defer cleanup()

	origCfgFile := cfgFile(context.Background())
	origOutputFormat := outputFormat(context.Background())
	origPlain := plainMode(context.Background())
	defer func() {
		gFlags.cfgFile = origCfgFile
		gFlags.outputFormat = origOutputFormat
		gFlags.plainMode = origPlain
	}()

	gFlags.cfgFile = configPath
	gFlags.outputFormat = "table"
	gFlags.plainMode = true

	if err := describeWorkflowCmd.RunE(describeWorkflowCmd, []string{"wf-describe-1"}); err != nil {
		t.Fatalf("RunE() error = %v", err)
	}
	if ms.RequestCount != 2 {
		t.Errorf("expected 2 requests (get workflow + list executions), got %d", ms.RequestCount)
	}
}
