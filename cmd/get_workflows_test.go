package cmd

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/dynatrace-oss/dtctl/cmd/testutil"
)

func TestGetWorkflowsCmd_ListWithFilters(t *testing.T) {
	ms := testutil.NewMockServer(t, map[string]http.HandlerFunc{
		"/platform/automation/v1/workflows": func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Query().Get("search") != "deploy" {
				t.Errorf("expected search=deploy, got %q", r.URL.Query().Get("search"))
			}
			if r.URL.Query().Get("type") != "STANDARD" {
				t.Errorf("expected type=STANDARD, got %q", r.URL.Query().Get("type"))
			}
			if r.URL.Query().Get("triggerType") != "Schedule" {
				t.Errorf("expected triggerType=Schedule, got %q", r.URL.Query().Get("triggerType"))
			}
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]any{"count": 1, "results": []any{
				map[string]any{"id": "wf-1", "title": "Deploy", "owner": "u1", "ownerType": "USER"},
			}})
		},
	})
	defer ms.Close()

	configPath, cleanup := testutil.SetupTestConfig(t, ms.URL)
	defer cleanup()

	origCfgFile := cfgFile(context.Background())
	origPlain := plainMode(context.Background())
	origChunk := chunkSize(context.Background())
	defer func() {
		gFlags.cfgFile = origCfgFile
		gFlags.plainMode = origPlain
		gFlags.chunkSize = origChunk
	}()

	gFlags.cfgFile = configPath
	gFlags.plainMode = true
	gFlags.chunkSize = 500

	testutil.ResetCommandFlags(getWorkflowsCmd)
	_ = getWorkflowsCmd.Flags().Set("filter", "deploy")
	_ = getWorkflowsCmd.Flags().Set("type", "standard")
	_ = getWorkflowsCmd.Flags().Set("trigger", "schedule")

	if err := getWorkflowsCmd.RunE(getWorkflowsCmd, nil); err != nil {
		t.Fatalf("RunE() error = %v", err)
	}
}

func TestGetWorkflowsCmd_InvalidChunkSize(t *testing.T) {
	origCfgFile := cfgFile(context.Background())
	origChunk := chunkSize(context.Background())
	defer func() {
		gFlags.cfgFile = origCfgFile
		gFlags.chunkSize = origChunk
	}()

	gFlags.cfgFile = "nonexistent-cfg"
	gFlags.chunkSize = 5 // below minimum of 20

	// Need a valid config so Setup() doesn't fail before chunk validation.
	ms := testutil.NewMockServer(t, map[string]http.HandlerFunc{})
	defer ms.Close()
	configPath, cleanup := testutil.SetupTestConfig(t, ms.URL)
	defer cleanup()
	gFlags.cfgFile = configPath

	testutil.ResetCommandFlags(getWorkflowsCmd)
	err := getWorkflowsCmd.RunE(getWorkflowsCmd, nil)
	if err == nil || err.Error() != "--chunk-size must be 0 or at least 20 (got 5)" {
		t.Fatalf("expected chunk-size validation error, got %v", err)
	}
}

func TestGetWorkflowsCmd_HasMoreWithLimit(t *testing.T) {
	ms := testutil.NewMockServer(t, map[string]http.HandlerFunc{
		"/platform/automation/v1/workflows": func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			// count > len(results) simulates server having more
			json.NewEncoder(w).Encode(map[string]any{"count": 50, "results": []any{
				map[string]any{"id": "wf-1", "title": "WF1", "owner": "u1", "ownerType": "USER"},
			}})
		},
	})
	defer ms.Close()

	configPath, cleanup := testutil.SetupTestConfig(t, ms.URL)
	defer cleanup()

	origCfgFile := cfgFile(context.Background())
	origPlain := plainMode(context.Background())
	origChunk := chunkSize(context.Background())
	origAgent := agentMode(context.Background())
	defer func() {
		gFlags.cfgFile = origCfgFile
		gFlags.plainMode = origPlain
		gFlags.chunkSize = origChunk
		gFlags.agentMode = origAgent
	}()

	gFlags.cfgFile = configPath
	gFlags.plainMode = true
	gFlags.chunkSize = 0 // single-page mode: stop after first page
	gFlags.agentMode = true

	testutil.ResetCommandFlags(getWorkflowsCmd)
	_ = getWorkflowsCmd.Flags().Set("limit", "10")

	if err := getWorkflowsCmd.RunE(getWorkflowsCmd, nil); err != nil {
		t.Fatalf("RunE() error = %v", err)
	}
}

func TestGetWorkflowExecutionsCmd_ListWithFilters(t *testing.T) {
	ms := testutil.NewMockServer(t, map[string]http.HandlerFunc{
		"/platform/automation/v1/executions": func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Query().Get("workflow") != "wf-abc" {
				t.Errorf("expected workflow=wf-abc, got %q", r.URL.Query().Get("workflow"))
			}
			if r.URL.Query().Get("state") != "SUCCESS" {
				t.Errorf("expected state=SUCCESS, got %q", r.URL.Query().Get("state"))
			}
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]any{"count": 1, "results": []any{
				map[string]any{
					"id": "exec-1", "workflow": "wf-abc", "state": "SUCCESS",
					"startedAt": "2026-06-01T10:00:00Z", "runtime": 5,
				},
			}})
		},
	})
	defer ms.Close()

	configPath, cleanup := testutil.SetupTestConfig(t, ms.URL)
	defer cleanup()

	origCfgFile := cfgFile(context.Background())
	origPlain := plainMode(context.Background())
	defer func() {
		gFlags.cfgFile = origCfgFile
		gFlags.plainMode = origPlain
	}()

	gFlags.cfgFile = configPath
	gFlags.plainMode = true

	testutil.ResetCommandFlags(getWorkflowExecutionsCmd)
	_ = getWorkflowExecutionsCmd.Flags().Set("workflow", "wf-abc")
	_ = getWorkflowExecutionsCmd.Flags().Set("state", "success")

	if err := getWorkflowExecutionsCmd.RunE(getWorkflowExecutionsCmd, nil); err != nil {
		t.Fatalf("RunE() error = %v", err)
	}
}

func TestGetWorkflowExecutionsCmd_InvalidStartedSince(t *testing.T) {
	ms := testutil.NewMockServer(t, map[string]http.HandlerFunc{})
	defer ms.Close()

	configPath, cleanup := testutil.SetupTestConfig(t, ms.URL)
	defer cleanup()

	origCfgFile := cfgFile(context.Background())
	origPlain := plainMode(context.Background())
	defer func() {
		gFlags.cfgFile = origCfgFile
		gFlags.plainMode = origPlain
	}()

	gFlags.cfgFile = configPath
	gFlags.plainMode = true

	testutil.ResetCommandFlags(getWorkflowExecutionsCmd)
	_ = getWorkflowExecutionsCmd.Flags().Set("started-since", "not-a-date")

	err := getWorkflowExecutionsCmd.RunE(getWorkflowExecutionsCmd, nil)
	if err == nil || err.Error()[:len("invalid --started-since:")] != "invalid --started-since:" {
		t.Fatalf("expected invalid --started-since error, got %v", err)
	}
}

func TestGetWorkflowExecutionsCmd_InvalidStartedUntil(t *testing.T) {
	ms := testutil.NewMockServer(t, map[string]http.HandlerFunc{})
	defer ms.Close()

	configPath, cleanup := testutil.SetupTestConfig(t, ms.URL)
	defer cleanup()

	origCfgFile := cfgFile(context.Background())
	origPlain := plainMode(context.Background())
	defer func() {
		gFlags.cfgFile = origCfgFile
		gFlags.plainMode = origPlain
	}()

	gFlags.cfgFile = configPath
	gFlags.plainMode = true

	testutil.ResetCommandFlags(getWorkflowExecutionsCmd)
	_ = getWorkflowExecutionsCmd.Flags().Set("started-until", "bad-date")

	err := getWorkflowExecutionsCmd.RunE(getWorkflowExecutionsCmd, nil)
	if err == nil || err.Error()[:len("invalid --started-until:")] != "invalid --started-until:" {
		t.Fatalf("expected invalid --started-until error, got %v", err)
	}
}

func TestGetWorkflowExecutionsCmd_HasMore(t *testing.T) {
	ms := testutil.NewMockServer(t, map[string]http.HandlerFunc{
		"/platform/automation/v1/executions": func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]any{"count": 200, "results": []any{
				map[string]any{
					"id": "exec-1", "workflow": "wf-1", "state": "SUCCESS",
					"startedAt": "2026-06-01T10:00:00Z", "runtime": 5,
				},
			}})
		},
	})
	defer ms.Close()

	configPath, cleanup := testutil.SetupTestConfig(t, ms.URL)
	defer cleanup()

	origCfgFile := cfgFile(context.Background())
	origPlain := plainMode(context.Background())
	origAgent := agentMode(context.Background())
	defer func() {
		gFlags.cfgFile = origCfgFile
		gFlags.plainMode = origPlain
		gFlags.agentMode = origAgent
	}()

	gFlags.cfgFile = configPath
	gFlags.plainMode = true
	gFlags.agentMode = true

	testutil.ResetCommandFlags(getWorkflowExecutionsCmd)

	if err := getWorkflowExecutionsCmd.RunE(getWorkflowExecutionsCmd, nil); err != nil {
		t.Fatalf("RunE() error = %v", err)
	}
}

func TestParseExecTime(t *testing.T) {
	tests := []struct {
		name     string
		in       string
		endOfDay bool
		want     string
		wantErr  bool
	}{
		{name: "empty returns empty", in: "", want: ""},
		{name: "RFC3339 passthrough (UTC)", in: "2026-06-10T15:04:05Z", want: "2026-06-10T15:04:05Z"},
		{name: "RFC3339 with offset normalized to UTC", in: "2026-06-10T17:04:05+02:00", want: "2026-06-10T15:04:05Z"},
		{name: "ISO 8601 without seconds, with zone", in: "2026-06-10T15:04Z", want: "2026-06-10T15:04:00Z"},
		{name: "ISO 8601 without zone treated as UTC", in: "2026-06-10T15:04:05", want: "2026-06-10T15:04:05Z"},
		{name: "date-only start of day", in: "2026-06-10", endOfDay: false, want: "2026-06-10T00:00:00Z"},
		{name: "date-only end of day", in: "2026-06-10", endOfDay: true, want: "2026-06-10T23:59:59Z"},
		{name: "invalid input errors", in: "notadate", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseExecTime(tt.in, tt.endOfDay)
			if (err != nil) != tt.wantErr {
				t.Fatalf("parseExecTime(%q, %v) error = %v, wantErr %v", tt.in, tt.endOfDay, err, tt.wantErr)
			}
			if !tt.wantErr && got != tt.want {
				t.Errorf("parseExecTime(%q, %v) = %q, want %q", tt.in, tt.endOfDay, got, tt.want)
			}
		})
	}
}

func TestValidateWorkflowChunkSize(t *testing.T) {
	tests := []struct {
		name    string
		chunk   int64
		wantErr bool
	}{
		{name: "zero is allowed (single page)", chunk: 0, wantErr: false},
		{name: "below minimum rejected", chunk: 1, wantErr: true},
		{name: "just below minimum rejected", chunk: 19, wantErr: true},
		{name: "at minimum allowed", chunk: 20, wantErr: false},
		{name: "default allowed", chunk: 500, wantErr: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateAutomationChunkSize(tt.chunk)
			if (err != nil) != tt.wantErr {
				t.Errorf("validateAutomationChunkSize(%d) error = %v, wantErr %v", tt.chunk, err, tt.wantErr)
			}
		})
	}
}
