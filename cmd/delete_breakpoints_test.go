package cmd

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/dynatrace-oss/dtctl/pkg/config"
	"github.com/dynatrace-oss/dtctl/pkg/resources/livedebugger"
)

func TestDeleteBreakpointCommandRegistration(t *testing.T) {
	deleteCmd, _, err := rootCmd.Find([]string{"delete"})
	if err != nil {
		t.Fatalf("expected delete command to exist, got error: %v", err)
	}
	if deleteCmd == nil || deleteCmd.Name() != "delete" {
		t.Fatalf("expected delete command to exist")
	}

	breakpointCmd, _, err := rootCmd.Find([]string{"delete", "breakpoint"})
	if err != nil {
		t.Fatalf("expected delete breakpoint command to exist, got error: %v", err)
	}
	if breakpointCmd == nil || breakpointCmd.Name() != "breakpoint" {
		t.Fatalf("expected delete breakpoint command to exist")
	}
}

func TestValidateDeleteBreakpointArgs(t *testing.T) {
	tests := []struct {
		name    string
		all     bool
		args    []string
		wantErr bool
	}{
		{name: "id argument", args: []string{"bp-1"}},
		{name: "location argument", args: []string{"MyFile.java:42"}},
		{name: "all without arg", all: true},
		{name: "all with arg", all: true, args: []string{"bp-1"}, wantErr: true},
		{name: "missing arg", wantErr: true},
		{name: "too many args", args: []string{"bp-1", "bp-2"}, wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cmd := deleteBreakpointCmd
			_ = cmd.Flags().Set("all", "false")
			if tt.all {
				_ = cmd.Flags().Set("all", "true")
			}

			err := validateDeleteBreakpointArgs(cmd, tt.args)
			if tt.wantErr && err == nil {
				t.Fatalf("expected error, got nil")
			}
			if !tt.wantErr && err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
}

func TestFindBreakpointRowsByLocation(t *testing.T) {
	rows := []breakpointRow{
		{ID: "bp-1", Filename: "A.java", Line: 10, Active: true},
		{ID: "bp-2", Filename: "A.java", Line: 10, Active: false},
		{ID: "bp-3", Filename: "A.java", Line: 11, Active: true},
	}

	matches := findBreakpointRowsByLocation(rows, "A.java", 10)
	if len(matches) != 2 {
		t.Fatalf("expected 2 matches, got %d", len(matches))
	}
	if matches[0].ID != "bp-1" || matches[1].ID != "bp-2" {
		t.Fatalf("unexpected matches: %#v", matches)
	}
}

func TestFindBreakpointRowByID(t *testing.T) {
	rows := []breakpointRow{{ID: "bp-1", Filename: "A.java", Line: 10, Active: true}}

	row, ok := findBreakpointRowByID(rows, "bp-1")
	if !ok {
		t.Fatalf("expected row to be found")
	}
	if row.ID != "bp-1" {
		t.Fatalf("unexpected row: %#v", row)
	}

	if _, ok := findBreakpointRowByID(rows, "missing"); ok {
		t.Fatalf("expected missing row lookup to fail")
	}
}

func TestExtractDeletedBreakpointIDs(t *testing.T) {
	resp := map[string]interface{}{
		"data": map[string]interface{}{
			"org": map[string]interface{}{
				"workspace": map[string]interface{}{
					"deleteAllRulesFromWorkspaceV2": []interface{}{"imm-1", "imm-2"},
				},
			},
		},
	}

	ids, err := extractDeletedBreakpointIDs(resp)
	if err != nil {
		t.Fatalf("extractDeletedBreakpointIDs returned error: %v", err)
	}
	if len(ids) != 2 || ids[0] != "imm-1" || ids[1] != "imm-2" {
		t.Fatalf("unexpected ids: %#v", ids)
	}
}

func TestFormatBreakpointLocation(t *testing.T) {
	if got := formatBreakpointLocation(breakpointRow{Filename: "A.java", Line: 10}); got != "A.java:10" {
		t.Fatalf("unexpected location: %q", got)
	}
	if got := formatBreakpointLocation(breakpointRow{ID: "bp-1"}); got != "unknown location" {
		t.Fatalf("unexpected fallback location: %q", got)
	}
}

func TestCheckDeleteBreakpointSafety(t *testing.T) {
	t.Run("readonly is blocked", func(t *testing.T) {
		cfg := config.NewConfig()
		cfg.SetContextWithOptions("readonly-ctx", "https://prod.dt.com", "token", &config.ContextOptions{
			SafetyLevel: config.SafetyLevelReadOnly,
		})
		cfg.CurrentContext = "readonly-ctx"

		err := checkDeleteBreakpointSafety(context.Background(), cfg)
		if err == nil {
			t.Fatalf("expected readonly delete safety error")
		}
		if !strings.Contains(err.Error(), "readonly") {
			t.Fatalf("expected readonly error message, got: %v", err)
		}
	})

	t.Run("readwrite-all is allowed", func(t *testing.T) {
		cfg := config.NewConfig()
		cfg.SetContextWithOptions("rw-all", "https://staging.dt.com", "token", &config.ContextOptions{
			SafetyLevel: config.SafetyLevelReadWriteAll,
		})
		cfg.CurrentContext = "rw-all"

		if err := checkDeleteBreakpointSafety(context.Background(), cfg); err != nil {
			t.Fatalf("expected delete to be allowed, got: %v", err)
		}
	})

	t.Run("missing current context returns error", func(t *testing.T) {
		cfg := config.NewConfig()
		cfg.CurrentContext = "missing"

		err := checkDeleteBreakpointSafety(context.Background(), cfg)
		if err == nil {
			t.Fatalf("expected missing context error")
		}
	})
}

func TestRunDeleteAllBreakpoints_NoRows(t *testing.T) {
	originalOutputFormat := outputFormat(context.Background())
	originalAgentMode := agentMode(context.Background())
	originalPlainMode := plainMode(context.Background())
	defer func() {
		gFlags.outputFormat = originalOutputFormat
		gFlags.agentMode = originalAgentMode
		gFlags.plainMode = originalPlainMode
	}()

	gFlags.outputFormat = "table"
	gFlags.agentMode = false
	gFlags.plainMode = true

	output := captureStdout(t, func() {
		if err := runDeleteAllBreakpoints(context.Background(), nil, "workspace-1", nil, true, false); err != nil {
			t.Fatalf("runDeleteAllBreakpoints returned error: %v", err)
		}
	})

	if !strings.Contains(output, "No breakpoints found in the current workspace") {
		t.Fatalf("unexpected output: %q", output)
	}
}

func TestRunDeleteBreakpointRows_DryRun(t *testing.T) {
	originalDryRun := dryRun(context.Background())
	originalOutputFormat := outputFormat(context.Background())
	originalAgentMode := agentMode(context.Background())
	originalPlainMode := plainMode(context.Background())
	defer func() {
		gFlags.dryRun = originalDryRun
		gFlags.outputFormat = originalOutputFormat
		gFlags.agentMode = originalAgentMode
		gFlags.plainMode = originalPlainMode
	}()

	gFlags.dryRun = true
	gFlags.outputFormat = "table"
	gFlags.agentMode = false
	gFlags.plainMode = true

	rows := []breakpointRow{
		{ID: "bp-1", Filename: "OrderController.java", Line: 306},
		{ID: "bp-2", Filename: "OrderController.java", Line: 307},
	}

	output := captureStdout(t, func() {
		if err := runDeleteBreakpointRows(context.Background(), nil, "workspace-1", rows, true, false); err != nil {
			t.Fatalf("runDeleteBreakpointRows returned error: %v", err)
		}
	})

	if !strings.Contains(output, "Dry run: would delete breakpoint bp-1 (OrderController.java:306)") {
		t.Fatalf("missing first dry-run output: %q", output)
	}
	if !strings.Contains(output, "Dry run: would delete breakpoint bp-2 (OrderController.java:307)") {
		t.Fatalf("missing second dry-run output: %q", output)
	}
}

func TestRunDeleteAllBreakpoints_Success(t *testing.T) {
	originalDryRun := dryRun(context.Background())
	originalOutputFormat := outputFormat(context.Background())
	originalAgentMode := agentMode(context.Background())
	originalPlainMode := plainMode(context.Background())
	defer func() {
		gFlags.dryRun = originalDryRun
		gFlags.outputFormat = originalOutputFormat
		gFlags.agentMode = originalAgentMode
		gFlags.plainMode = originalPlainMode
	}()

	gFlags.dryRun = false
	gFlags.outputFormat = "table"
	gFlags.agentMode = false
	gFlags.plainMode = true
	ops := breakpointDeleteOps{
		deleteAll: func(handler *livedebugger.Handler, workspaceID string) (map[string]interface{}, error) {
			return map[string]interface{}{
				"data": map[string]interface{}{
					"org": map[string]interface{}{
						"workspace": map[string]interface{}{
							"deleteAllRulesFromWorkspaceV2": []interface{}{"imm-1", "imm-2"},
						},
					},
				},
			}, nil
		},
	}

	rows := []breakpointRow{{ID: "bp-1", Filename: "A.java", Line: 10}}
	output := captureStdout(t, func() {
		if err := runDeleteAllBreakpointsWithOps(context.Background(), nil, "workspace-1", rows, true, false, ops); err != nil {
			t.Fatalf("runDeleteAllBreakpoints returned error: %v", err)
		}
	})

	if !strings.Contains(output, "Deleted 2 breakpoint(s)") {
		t.Fatalf("unexpected output: %q", output)
	}
}

func TestRunDeleteAllBreakpoints_MalformedResponse(t *testing.T) {
	originalDryRun := dryRun(context.Background())
	defer func() {
		gFlags.dryRun = originalDryRun
	}()

	gFlags.dryRun = false
	ops := breakpointDeleteOps{
		deleteAll: func(handler *livedebugger.Handler, workspaceID string) (map[string]interface{}, error) {
			return map[string]interface{}{"data": map[string]interface{}{}}, nil
		},
	}

	rows := []breakpointRow{{ID: "bp-1", Filename: "A.java", Line: 10}}
	err := runDeleteAllBreakpointsWithOps(context.Background(), nil, "workspace-1", rows, true, false, ops)
	if err == nil {
		t.Fatalf("expected error for malformed deleteAll response")
	}
	if !strings.Contains(err.Error(), "missing org object") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestRunDeleteBreakpointRows_Empty(t *testing.T) {
	if err := runDeleteBreakpointRows(context.Background(), nil, "workspace-1", nil, true, false); err != nil {
		t.Fatalf("expected nil error for empty rows, got: %v", err)
	}
}

func TestRunDeleteBreakpointRows_PartialFailure(t *testing.T) {
	originalDryRun := dryRun(context.Background())
	originalOutputFormat := outputFormat(context.Background())
	originalAgentMode := agentMode(context.Background())
	originalPlainMode := plainMode(context.Background())
	defer func() {
		gFlags.dryRun = originalDryRun
		gFlags.outputFormat = originalOutputFormat
		gFlags.agentMode = originalAgentMode
		gFlags.plainMode = originalPlainMode
	}()

	gFlags.dryRun = false
	gFlags.outputFormat = "table"
	gFlags.agentMode = false
	gFlags.plainMode = true
	ops := breakpointDeleteOps{
		deleteOne: func(handler *livedebugger.Handler, workspaceID, breakpointID string) (map[string]interface{}, error) {
			if breakpointID == "bp-2" {
				return nil, errors.New("remote delete failed")
			}
			return map[string]interface{}{"ok": true}, nil
		},
	}

	rows := []breakpointRow{
		{ID: "bp-1", Filename: "OrderController.java", Line: 306},
		{ID: "bp-2", Filename: "OrderController.java", Line: 307},
	}

	output := captureStdout(t, func() {
		err := runDeleteBreakpointRowsWithOps(context.Background(), nil, "workspace-1", rows, true, false, ops)
		if err == nil {
			t.Fatalf("expected partial failure error")
		}
		if !strings.Contains(err.Error(), "bp-2") {
			t.Fatalf("unexpected error: %v", err)
		}
	})

	if !strings.Contains(output, "Deleted breakpoint bp-1 (OrderController.java:306)") {
		t.Fatalf("missing success output: %q", output)
	}
	if !strings.Contains(output, "Failed to delete 1 breakpoint(s) after deleting 1 successfully") {
		t.Fatalf("missing partial-failure summary: %q", output)
	}
}

func TestRunDeleteBreakpointRows_CancelledConfirmation(t *testing.T) {
	originalDryRun := dryRun(context.Background())
	originalPlainMode := plainMode(context.Background())
	originalStdin := os.Stdin
	defer func() {
		gFlags.dryRun = originalDryRun
		gFlags.plainMode = originalPlainMode
		os.Stdin = originalStdin
	}()

	gFlags.dryRun = false
	gFlags.plainMode = false
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe error: %v", err)
	}
	if _, err := w.WriteString("n\n"); err != nil {
		t.Fatalf("write stdin stub failed: %v", err)
	}
	_ = w.Close()
	os.Stdin = r

	output := captureStdout(t, func() {
		if err := runDeleteBreakpointRows(context.Background(), nil, "workspace-1", []breakpointRow{{ID: "bp-1", Filename: "A.java", Line: 10}}, false, false); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	})
	if !strings.Contains(output, "Deletion cancelled") {
		t.Fatalf("unexpected output: %q", output)
	}
}

func TestRunDeleteBreakpointRows_VerbosePrintError(t *testing.T) {
	originalDryRun := dryRun(context.Background())
	originalPlainMode := plainMode(context.Background())
	defer func() {
		gFlags.dryRun = originalDryRun
		gFlags.plainMode = originalPlainMode
	}()

	gFlags.dryRun = false
	gFlags.plainMode = true
	ops := breakpointDeleteOps{
		deleteOne: func(handler *livedebugger.Handler, workspaceID, breakpointID string) (map[string]interface{}, error) {
			return map[string]interface{}{"bad": func() {}}, nil
		},
	}

	err := runDeleteBreakpointRowsWithOps(context.Background(), nil, "workspace-1", []breakpointRow{{ID: "bp-1", Filename: "A.java", Line: 10}}, true, true, ops)
	if err == nil {
		t.Fatalf("expected printGraphQLResponse marshal error")
	}
	if !strings.Contains(err.Error(), "failed to encode deleteRuleV2 response") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestRunDeleteAllBreakpoints_VerbosePrintError(t *testing.T) {
	originalDryRun := dryRun(context.Background())
	defer func() {
		gFlags.dryRun = originalDryRun
	}()

	gFlags.dryRun = false
	ops := breakpointDeleteOps{
		deleteAll: func(handler *livedebugger.Handler, workspaceID string) (map[string]interface{}, error) {
			return map[string]interface{}{"bad": func() {}}, nil
		},
	}

	err := runDeleteAllBreakpointsWithOps(context.Background(), nil, "workspace-1", []breakpointRow{{ID: "bp-1", Filename: "A.java", Line: 10}}, true, true, ops)
	if err == nil {
		t.Fatalf("expected printGraphQLResponse marshal error")
	}
	if !strings.Contains(err.Error(), "failed to encode deleteAllRulesFromWorkspaceV2 response") {
		t.Fatalf("unexpected error: %v", err)
	}
}
