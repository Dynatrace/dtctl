package cmd

import (
	"bytes"
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dynatrace-oss/dtctl/pkg/client"
	"github.com/dynatrace-oss/dtctl/pkg/config"
	"github.com/dynatrace-oss/dtctl/pkg/resources/livedebugger"
)

func TestBuildBreakpointStatusResult(t *testing.T) {
	rule := livedebugger.BreakpointRule{
		ID:            "bp-1",
		IsDisabled:    false,
		DisableReason: "",
		AugJSON: map[string]interface{}{
			"location": map[string]interface{}{
				"filename": "OrderController.java",
				"lineno":   float64(306),
			},
		},
	}

	statusResp := map[string]interface{}{
		"data": map[string]interface{}{
			"org": map[string]interface{}{
				"ruleStatuses": []interface{}{
					map[string]interface{}{
						"status": "Active",
						"rookStatuses": []interface{}{
							map[string]interface{}{
								"rook": map[string]interface{}{
									"id":         "rook-1",
									"hostname":   "host-a",
									"executable": "java",
								},
								"tips": []interface{}{
									map[string]interface{}{"description": "Trigger the line", "docsLink": "https://docs.example/trigger"},
								},
							},
						},
					},
					map[string]interface{}{
						"status": "Warning",
						"rookStatuses": []interface{}{
							map[string]interface{}{
								"rook": map[string]interface{}{
									"id":         "rook-2",
									"hostname":   "host-b",
									"executable": "java",
								},
								"error": map[string]interface{}{
									"summary": map[string]interface{}{
										"title":       "Source file has changed",
										"description": "Redeploy or refresh source mappings.",
										"docsLink":    "https://docs.example/source-changed",
										"args":        []interface{}{float64(1)},
									},
								},
							},
						},
						"controllerStatuses": []interface{}{
							map[string]interface{}{
								"controllerId": "controller-1",
								"error": map[string]interface{}{
									"summary": map[string]interface{}{
										"title":       "Partial deployment",
										"description": "Some agents have not yet received the rule.",
										"docsLink":    "https://docs.example/partial-deployment",
									},
								},
							},
						},
					},
				},
			},
		},
	}

	result, err := buildBreakpointStatusResult(rule, statusResp)
	if err != nil {
		t.Fatalf("buildBreakpointStatusResult returned error: %v", err)
	}

	if result.ID != "bp-1" {
		t.Fatalf("unexpected id: %q", result.ID)
	}
	if result.Location != "OrderController.java:306" {
		t.Fatalf("unexpected location: %q", result.Location)
	}
	if result.Status != "Warning" {
		t.Fatalf("unexpected overall status: %q", result.Status)
	}
	if len(result.ActiveRooks) != 1 {
		t.Fatalf("unexpected active rook count: %d", len(result.ActiveRooks))
	}
	if len(result.ActiveTips) != 1 || result.ActiveTips[0].Description != "Trigger the line" {
		t.Fatalf("unexpected active tips: %#v", result.ActiveTips)
	}
	if len(result.Warnings) != 1 || result.Warnings[0].Title != "Source file has changed" {
		t.Fatalf("unexpected warnings: %#v", result.Warnings)
	}
	if len(result.ControllerWarnings) != 1 || result.ControllerWarnings[0].Title != "Partial deployment" {
		t.Fatalf("unexpected controller warnings: %#v", result.ControllerWarnings)
	}
}

func TestDeriveOverallBreakpointStatusDisabled(t *testing.T) {
	result := breakpointStatusResult{Enabled: false}
	if status := deriveOverallBreakpointStatus(result); status != "Disabled" {
		t.Fatalf("unexpected status: %q", status)
	}
}

func TestDescribeBreakpointCommandRegistration(t *testing.T) {
	breakpointCmd, _, err := describeCmd.Find([]string{"breakpoint"})
	if err != nil {
		t.Fatalf("expected describe breakpoint command to exist, got error: %v", err)
	}
	if breakpointCmd == nil || breakpointCmd.Name() != "breakpoint" {
		t.Fatalf("expected describe breakpoint command to exist")
	}

	breakpointsCmd, _, err := describeCmd.Find([]string{"breakpoints"})
	if err != nil {
		t.Fatalf("expected describe breakpoints alias to resolve, got error: %v", err)
	}
	if breakpointsCmd == nil || breakpointsCmd.Name() != "breakpoint" {
		t.Fatalf("expected describe breakpoints alias to resolve to breakpoint command")
	}

	bpCmd, _, err := describeCmd.Find([]string{"bp"})
	if err != nil {
		t.Fatalf("expected describe bp alias to resolve, got error: %v", err)
	}
	if bpCmd == nil || bpCmd.Name() != "breakpoint" {
		t.Fatalf("expected describe bp alias to resolve to breakpoint command")
	}
}

func TestDescribeBreakpointCommandArgs(t *testing.T) {
	if err := describeBreakpointCmd.Args(describeBreakpointCmd, []string{"OrderController.java:306"}); err != nil {
		t.Fatalf("expected filename:line to be accepted, got error: %v", err)
	}
	if err := describeBreakpointCmd.Args(describeBreakpointCmd, []string{"bp-1"}); err != nil {
		t.Fatalf("expected id to be accepted, got error: %v", err)
	}
	if err := describeBreakpointCmd.Args(describeBreakpointCmd, nil); err == nil {
		t.Fatalf("expected missing identifier error")
	}
}

func TestUseBreakpointDescribeTextView(t *testing.T) {
	originalFormat := outputFormat(context.Background())
	originalAgentMode := agentMode(context.Background())
	defer func() { gFlags.outputFormat = originalFormat }()
	defer func() { gFlags.agentMode = originalAgentMode }()

	tests := []struct {
		name   string
		format string
		want   bool
	}{
		{name: "default", format: "", want: true},
		{name: "table", format: "table", want: true},
		{name: "wide", format: "wide", want: true},
		{name: "csv", format: "csv", want: true},
		{name: "json", format: "json", want: false},
		{name: "yaml", format: "yaml", want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gFlags.agentMode = false
			gFlags.outputFormat = tt.format
			if got := useBreakpointDescribeTextView(context.Background()); got != tt.want {
				t.Fatalf("useBreakpointDescribeTextView() = %v, want %v", got, tt.want)
			}
		})
	}

	t.Run("agent mode forces structured view", func(t *testing.T) {
		gFlags.agentMode = true
		gFlags.outputFormat = "table"
		if got := useBreakpointDescribeTextView(context.Background()); got {
			t.Fatalf("useBreakpointDescribeTextView() = %v, want false when agent mode enabled", got)
		}
	})
}

func TestPrintBreakpointStatusResult(t *testing.T) {
	originalOut := rootCmd.OutOrStdout()
	defer rootCmd.SetOut(originalOut)

	var out bytes.Buffer
	rootCmd.SetOut(&out)

	result := breakpointStatusResult{
		ID:          "bp-1",
		Location:    "OrderController.java:306",
		Enabled:     true,
		Status:      "Warning",
		ActiveRooks: []breakpointRookInfo{{ID: "rook-1", Hostname: "host-a", Executable: "java"}},
		ActiveTips:  []breakpointTip{{Description: "Trigger the line", DocsLink: "https://docs.example/trigger"}},
		Warnings:    []breakpointStatusIssue{{Title: "Source file has changed", Description: "Redeploy source map"}},
		ControllerWarnings: []breakpointStatusIssue{{
			Title:       "Partial deployment",
			Description: "Some agents missing",
			Controllers: []string{"controller-1"},
		}},
	}

	printBreakpointStatusResult(context.Background(), result)

	text := out.String()
	for _, mustContain := range []string{
		"ID:",
		"bp-1",
		"Location:",
		"OrderController.java:306",
		"Status:",
		"Warning",
		"Active rooks:",
		"Active tips:",
		"Warnings:",
		"Controller warnings:",
	} {
		if !strings.Contains(text, mustContain) {
			t.Fatalf("expected output to contain %q, got: %q", mustContain, text)
		}
	}
}

func TestPrintBreakpointRooksSection(t *testing.T) {
	originalOut := rootCmd.OutOrStdout()
	defer rootCmd.SetOut(originalOut)

	var out bytes.Buffer
	rootCmd.SetOut(&out)

	rooks := []breakpointRookInfo{{ID: "rook-1", Hostname: "host-a", Executable: "java"}}
	printBreakpointRooksSection(context.Background(), "Active rooks", rooks)

	text := out.String()
	if !strings.Contains(text, "Active rooks:") || !strings.Contains(text, "host-a / java") {
		t.Fatalf("unexpected rooks section output: %q", text)
	}
}

func TestPrintBreakpointTipsSection(t *testing.T) {
	originalOut := rootCmd.OutOrStdout()
	defer rootCmd.SetOut(originalOut)

	var out bytes.Buffer
	rootCmd.SetOut(&out)

	tips := []breakpointTip{{Description: "Trigger the line", DocsLink: "https://docs.example/trigger"}}
	printBreakpointTipsSection(context.Background(), "Active tips", tips)

	text := out.String()
	if !strings.Contains(text, "Active tips:") || !strings.Contains(text, "Trigger the line") {
		t.Fatalf("unexpected tips section output: %q", text)
	}
}

func TestPrintBreakpointIssuesSection(t *testing.T) {
	originalOut := rootCmd.OutOrStdout()
	defer rootCmd.SetOut(originalOut)

	var out bytes.Buffer
	rootCmd.SetOut(&out)

	issues := []breakpointStatusIssue{{
		Title:       "Source file has changed",
		Description: "Redeploy source map",
		DocsLink:    "https://docs.example/source",
		Rooks:       []breakpointRookInfo{{ID: "rook-1", Hostname: "host-a", Executable: "java"}},
		Controllers: []string{"controller-1"},
	}}
	printBreakpointIssuesSection(context.Background(), "Warnings", issues)

	text := out.String()
	for _, mustContain := range []string{"Warnings:", "Source file has changed", "Description:", "Docs:", "Rooks:", "Controllers:"} {
		if !strings.Contains(text, mustContain) {
			t.Fatalf("expected output to contain %q, got: %q", mustContain, text)
		}
	}
}

func TestDescribeCommandRequiresSubcommand(t *testing.T) {
	err := requireSubcommand(describeCmd, nil)
	if err == nil {
		t.Fatalf("expected subcommand error")
	}
}

func TestRunDescribeBreakpoint_LoadConfigError(t *testing.T) {
	originalCfgFile := cfgFile(context.Background())
	defer func() { gFlags.cfgFile = originalCfgFile }()

	gFlags.cfgFile = filepath.Join(t.TempDir(), "missing-config.yaml")

	err := runDescribeBreakpoint(describeCmd, "OrderController.java:306")
	if err == nil {
		t.Fatalf("expected load config error")
	}
}

func TestRunDescribeBreakpoint_StructuredSuccess(t *testing.T) {
	originalOutputFormat := outputFormat(context.Background())
	originalAgentMode := agentMode(context.Background())
	originalDebugMode := debugMode(context.Background())
	originalVerbosity := verbosity(context.Background())
	defer func() {
		gFlags.outputFormat = originalOutputFormat
		gFlags.agentMode = originalAgentMode
		gFlags.debugMode = originalDebugMode
		gFlags.verbosity = originalVerbosity
	}()

	gFlags.outputFormat = "json"
	gFlags.agentMode = false
	gFlags.debugMode = false
	gFlags.verbosity = 0

	deps := liveDebuggerDeps{}
	deps.loadConfig = func(context.Context) (*config.Config, error) {
		cfg := config.NewConfig()
		cfg.SetContext("test", "https://example.invalid", "token")
		cfg.CurrentContext = "test"
		return cfg, nil
	}
	deps.newClient = func(_ context.Context, cfg *config.Config) (*client.Client, error) { return nil, nil }
	deps.newHandler = func(c *client.Client, environment string) (*livedebugger.Handler, error) { return nil, nil }
	deps.getOrCreateWorkspace = func(handler *livedebugger.Handler, projectPath string) (map[string]interface{}, string, error) {
		return map[string]interface{}{"data": map[string]interface{}{}}, "ws-1", nil
	}
	deps.getWorkspaceRules = func(handler *livedebugger.Handler, workspaceID string) (map[string]interface{}, error) {
		return map[string]interface{}{
			"data": map[string]interface{}{
				"org": map[string]interface{}{
					"workspace": map[string]interface{}{
						"rules": []interface{}{
							map[string]interface{}{
								"id":          "bp-1",
								"is_disabled": false,
								"aug_json": map[string]interface{}{
									"location": map[string]interface{}{"filename": "OrderController.java", "lineno": float64(306)},
								},
							},
						},
					},
				},
			},
		}, nil
	}
	deps.getRuleStatusBreakdown = func(handler *livedebugger.Handler, ruleID string) (map[string]interface{}, error) {
		return map[string]interface{}{
			"data": map[string]interface{}{
				"org": map[string]interface{}{
					"ruleStatuses": []interface{}{},
				},
			},
		}, nil
	}

	output := captureStdout(t, func() {
		if err := runDescribeBreakpointWithDeps(describeCmd, "bp-1", deps); err != nil {
			t.Fatalf("runDescribeBreakpoint returned error: %v", err)
		}
	})

	if !strings.Contains(output, "\"id\": \"bp-1\"") {
		t.Fatalf("unexpected structured output: %q", output)
	}
}

func TestRunDescribeBreakpoint_DirectIDSuccess(t *testing.T) {
	originalOutputFormat := outputFormat(context.Background())
	originalAgentMode := agentMode(context.Background())
	defer func() {
		gFlags.outputFormat = originalOutputFormat
		gFlags.agentMode = originalAgentMode
	}()

	gFlags.outputFormat = "json"
	// Pin non-agent mode so the assertion is deterministic regardless of ambient
	// agent detection (otherwise the output shape — bare object vs envelope —
	// depends on the environment the tests run in).
	gFlags.agentMode = false
	deps := liveDebuggerDeps{}
	deps.loadConfig = func(context.Context) (*config.Config, error) {
		cfg := config.NewConfig()
		cfg.SetContext("test", "https://example.invalid", "token")
		cfg.CurrentContext = "test"
		return cfg, nil
	}
	deps.newClient = func(_ context.Context, cfg *config.Config) (*client.Client, error) { return nil, nil }
	deps.newHandler = func(c *client.Client, environment string) (*livedebugger.Handler, error) { return nil, nil }
	deps.getOrCreateWorkspace = func(handler *livedebugger.Handler, projectPath string) (map[string]interface{}, string, error) {
		return map[string]interface{}{"data": map[string]interface{}{}}, "ws-1", nil
	}
	deps.getWorkspaceRules = func(handler *livedebugger.Handler, workspaceID string) (map[string]interface{}, error) {
		return map[string]interface{}{
			"data": map[string]interface{}{
				"org": map[string]interface{}{
					"workspace": map[string]interface{}{
						"rules": []interface{}{
							map[string]interface{}{
								"id":          "123456789",
								"is_disabled": false,
								"aug_json": map[string]interface{}{
									"location": map[string]interface{}{"filename": "OrderController.java", "lineno": float64(306)},
								},
							},
						},
					},
				},
			},
		}, nil
	}
	deps.getRuleStatusBreakdown = func(handler *livedebugger.Handler, ruleID string) (map[string]interface{}, error) {
		return map[string]interface{}{
			"data": map[string]interface{}{
				"org": map[string]interface{}{
					"ruleStatuses": []interface{}{},
				},
			},
		}, nil
	}

	output := captureStdout(t, func() {
		if err := runDescribeBreakpointWithDeps(describeCmd, "123456789", deps); err != nil {
			t.Fatalf("runDescribeBreakpoint returned error: %v", err)
		}
	})

	if !strings.Contains(output, "\"id\": \"123456789\"") {
		t.Fatalf("unexpected direct-id output: %q", output)
	}
}

func TestRunDescribeBreakpoint_UnknownIdentifierError(t *testing.T) {
	deps := liveDebuggerDeps{}
	deps.loadConfig = func(context.Context) (*config.Config, error) {
		cfg := config.NewConfig()
		cfg.SetContext("test", "https://example.invalid", "token")
		cfg.CurrentContext = "test"
		return cfg, nil
	}
	deps.newClient = func(_ context.Context, cfg *config.Config) (*client.Client, error) { return nil, nil }
	deps.newHandler = func(c *client.Client, environment string) (*livedebugger.Handler, error) { return nil, nil }
	deps.getOrCreateWorkspace = func(handler *livedebugger.Handler, projectPath string) (map[string]interface{}, string, error) {
		return map[string]interface{}{"data": map[string]interface{}{}}, "ws-1", nil
	}
	deps.getWorkspaceRules = func(handler *livedebugger.Handler, workspaceID string) (map[string]interface{}, error) {
		return map[string]interface{}{
			"data": map[string]interface{}{
				"org": map[string]interface{}{
					"workspace": map[string]interface{}{
						"rules": []interface{}{},
					},
				},
			},
		}, nil
	}
	deps.getRuleStatusBreakdown = func(handler *livedebugger.Handler, ruleID string) (map[string]interface{}, error) {
		t.Fatalf("getRuleStatusBreakdown should not be called for unknown identifier")
		return nil, nil
	}

	err := runDescribeBreakpointWithDeps(describeCmd, "asdfasdfasdfadfasfd", deps)
	if err == nil {
		t.Fatalf("expected error for unknown identifier")
	}
	if !strings.Contains(err.Error(), "no breakpoint found") {
		t.Fatalf("unexpected error message: %v", err)
	}
}

func TestRunDescribeBreakpoint_StatusBreakdownError(t *testing.T) {
	deps := liveDebuggerDeps{}
	deps.loadConfig = func(context.Context) (*config.Config, error) {
		cfg := config.NewConfig()
		cfg.SetContext("test", "https://example.invalid", "token")
		cfg.CurrentContext = "test"
		return cfg, nil
	}
	deps.newClient = func(_ context.Context, cfg *config.Config) (*client.Client, error) { return nil, nil }
	deps.newHandler = func(c *client.Client, environment string) (*livedebugger.Handler, error) { return nil, nil }
	deps.getOrCreateWorkspace = func(handler *livedebugger.Handler, projectPath string) (map[string]interface{}, string, error) {
		return map[string]interface{}{"data": map[string]interface{}{}}, "ws-1", nil
	}
	deps.getWorkspaceRules = func(handler *livedebugger.Handler, workspaceID string) (map[string]interface{}, error) {
		return map[string]interface{}{
			"data": map[string]interface{}{
				"org": map[string]interface{}{
					"workspace": map[string]interface{}{
						"rules": []interface{}{map[string]interface{}{"id": "bp-1"}},
					},
				},
			},
		}, nil
	}
	deps.getRuleStatusBreakdown = func(handler *livedebugger.Handler, ruleID string) (map[string]interface{}, error) {
		return nil, errors.New("status query failed")
	}

	err := runDescribeBreakpointWithDeps(describeCmd, "bp-1", deps)
	if err == nil {
		t.Fatalf("expected status breakdown error")
	}
}

func TestRunDescribeBreakpoint_WorkspaceResponsePrintError(t *testing.T) {
	originalDebugMode := debugMode(context.Background())
	originalVerbosity := verbosity(context.Background())
	defer func() {
		gFlags.debugMode = originalDebugMode
		gFlags.verbosity = originalVerbosity
	}()

	gFlags.debugMode = true
	gFlags.verbosity = 1

	deps := liveDebuggerDeps{}
	deps.loadConfig = func(context.Context) (*config.Config, error) {
		cfg := config.NewConfig()
		cfg.SetContext("test", "https://example.invalid", "token")
		cfg.CurrentContext = "test"
		return cfg, nil
	}
	deps.newClient = func(_ context.Context, cfg *config.Config) (*client.Client, error) { return nil, nil }
	deps.newHandler = func(c *client.Client, environment string) (*livedebugger.Handler, error) { return nil, nil }
	deps.getOrCreateWorkspace = func(handler *livedebugger.Handler, projectPath string) (map[string]interface{}, string, error) {
		return map[string]interface{}{"bad": func() {}}, "ws-1", nil
	}
	deps.getWorkspaceRules = func(handler *livedebugger.Handler, workspaceID string) (map[string]interface{}, error) {
		return map[string]interface{}{"data": map[string]interface{}{"org": map[string]interface{}{"workspace": map[string]interface{}{"rules": []interface{}{}}}}}, nil
	}
	deps.getRuleStatusBreakdown = func(handler *livedebugger.Handler, ruleID string) (map[string]interface{}, error) {
		return map[string]interface{}{"data": map[string]interface{}{"org": map[string]interface{}{"ruleStatuses": []interface{}{}}}}, nil
	}

	err := runDescribeBreakpointWithDeps(describeCmd, "bp-1", deps)
	if err == nil {
		t.Fatalf("expected printGraphQLResponse marshal error")
	}
	if !strings.Contains(err.Error(), "failed to encode getOrCreateWorkspaceV2 response") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestRunDescribeBreakpoint_WorkspaceRulesResponsePrintError(t *testing.T) {
	originalDebugMode := debugMode(context.Background())
	originalVerbosity := verbosity(context.Background())
	defer func() {
		gFlags.debugMode = originalDebugMode
		gFlags.verbosity = originalVerbosity
	}()

	gFlags.debugMode = true
	gFlags.verbosity = 1

	deps := liveDebuggerDeps{}
	deps.loadConfig = func(context.Context) (*config.Config, error) {
		cfg := config.NewConfig()
		cfg.SetContext("test", "https://example.invalid", "token")
		cfg.CurrentContext = "test"
		return cfg, nil
	}
	deps.newClient = func(_ context.Context, cfg *config.Config) (*client.Client, error) { return nil, nil }
	deps.newHandler = func(c *client.Client, environment string) (*livedebugger.Handler, error) { return nil, nil }
	deps.getOrCreateWorkspace = func(handler *livedebugger.Handler, projectPath string) (map[string]interface{}, string, error) {
		return map[string]interface{}{"data": map[string]interface{}{}}, "ws-1", nil
	}
	deps.getWorkspaceRules = func(handler *livedebugger.Handler, workspaceID string) (map[string]interface{}, error) {
		return map[string]interface{}{"bad": func() {}}, nil
	}
	deps.getRuleStatusBreakdown = func(handler *livedebugger.Handler, ruleID string) (map[string]interface{}, error) {
		return map[string]interface{}{"data": map[string]interface{}{"org": map[string]interface{}{"ruleStatuses": []interface{}{}}}}, nil
	}

	err := runDescribeBreakpointWithDeps(describeCmd, "bp-1", deps)
	if err == nil {
		t.Fatalf("expected printGraphQLResponse marshal error")
	}
	if !strings.Contains(err.Error(), "failed to encode getWorkspaceRules response") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestIssueFromError(t *testing.T) {
	t.Run("summary values win", func(t *testing.T) {
		issue := issueFromError(map[string]interface{}{
			"type":    "FallbackType",
			"message": "Fallback message",
			"summary": map[string]interface{}{
				"title":       "Summary title",
				"description": "Summary description",
				"docsLink":    "https://docs.example",
				"args":        []interface{}{1},
			},
		})

		if issue.Title != "Summary title" || issue.Description != "Summary description" || issue.DocsLink != "https://docs.example" {
			t.Fatalf("unexpected summary issue: %#v", issue)
		}
	})

	t.Run("fallback to type and message", func(t *testing.T) {
		issue := issueFromError(map[string]interface{}{"type": "TypeOnly", "message": "MessageOnly"})
		if issue.Title != "TypeOnly" || issue.Description != "MessageOnly" {
			t.Fatalf("unexpected fallback issue: %#v", issue)
		}
	})

	t.Run("unknown fallback", func(t *testing.T) {
		issue := issueFromError(nil)
		if issue.Title != "Unknown issue" {
			t.Fatalf("unexpected unknown title: %#v", issue)
		}
	})
}

func TestExtractRookInfo(t *testing.T) {
	t.Run("invalid input", func(t *testing.T) {
		if _, ok := extractRookInfo("invalid"); ok {
			t.Fatalf("expected invalid input to fail")
		}
	})

	t.Run("empty map", func(t *testing.T) {
		if _, ok := extractRookInfo(map[string]interface{}{}); ok {
			t.Fatalf("expected empty map to fail")
		}
	})

	t.Run("partial data accepted", func(t *testing.T) {
		rook, ok := extractRookInfo(map[string]interface{}{"hostname": "host-a"})
		if !ok {
			t.Fatalf("expected partial rook data to pass")
		}
		if rook.Hostname != "host-a" {
			t.Fatalf("unexpected rook: %#v", rook)
		}
	})
}
