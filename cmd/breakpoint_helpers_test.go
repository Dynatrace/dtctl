package cmd

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/dynatrace-oss/dtctl/pkg/client"
	"github.com/dynatrace-oss/dtctl/pkg/config"
	"github.com/dynatrace-oss/dtctl/pkg/resources/livedebugger"
)

func TestGetBreakpointsCommandRegistration(t *testing.T) {
	getCmd, _, err := rootCmd.Find([]string{"get"})
	if err != nil {
		t.Fatalf("expected get command to exist, got error: %v", err)
	}
	if getCmd == nil || getCmd.Name() != "get" {
		t.Fatalf("expected get command to exist")
	}

	breakpointsCmd, _, err := rootCmd.Find([]string{"get", "breakpoints"})
	if err != nil {
		t.Fatalf("expected get breakpoints command to exist, got error: %v", err)
	}
	if breakpointsCmd == nil || breakpointsCmd.Name() != "breakpoints" {
		t.Fatalf("expected get breakpoints command to exist")
	}
}

func TestExtractBreakpointRows(t *testing.T) {
	resp := map[string]interface{}{
		"data": map[string]interface{}{
			"org": map[string]interface{}{
				"workspace": map[string]interface{}{
					"rules": []interface{}{
						map[string]interface{}{
							"id":          "bp-2",
							"is_disabled": false,
							"aug_json": map[string]interface{}{
								"location": map[string]interface{}{
									"filename": "OrderController.java",
									"lineno":   float64(1337),
								},
							},
						},
						map[string]interface{}{
							"id":          "bp-1",
							"is_disabled": true,
							"aug_json": map[string]interface{}{
								"location": map[string]interface{}{
									"filename": "AController.java",
									"lineno":   float64(42),
								},
							},
						},
					},
				},
			},
		},
	}

	rows, err := extractBreakpointRows(resp)
	if err != nil {
		t.Fatalf("extractBreakpointRows returned error: %v", err)
	}

	if len(rows) != 2 {
		t.Fatalf("expected 2 rows, got %d", len(rows))
	}

	if rows[0].ID != "bp-1" || rows[0].Filename != "AController.java" || rows[0].Line != 42 || rows[0].Active {
		t.Fatalf("unexpected first row: %#v", rows[0])
	}

	if rows[1].ID != "bp-2" || rows[1].Filename != "OrderController.java" || rows[1].Line != 1337 || !rows[1].Active {
		t.Fatalf("unexpected second row: %#v", rows[1])
	}
}

func TestParseFilters(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		wantErr bool
		want    map[string][]string
	}{
		{
			name:  "single filter",
			input: "k8s.namespace.name:prod",
			want: map[string][]string{
				"k8s.namespace.name": {"prod"},
			},
		},
		{
			name:  "multiple filters and duplicate keys",
			input: "k8s.namespace.name:prod, dt.entity.host:HOST-1,k8s.namespace.name:stage",
			want: map[string][]string{
				"k8s.namespace.name": {"prod", "stage"},
				"dt.entity.host":     {"HOST-1"},
			},
		},
		{
			name:  "equals separator remains supported",
			input: "k8s.namespace.name=prod",
			want: map[string][]string{
				"k8s.namespace.name": {"prod"},
			},
		},
		{name: "missing separator", input: "k8s.namespace.name", wantErr: true},
		{name: "empty input", input: "", wantErr: true},
		{name: "empty key", input: ":prod", wantErr: true},
		{name: "empty value", input: "k8s.namespace.name:", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseFilters(tt.input)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("expected error, got nil")
				}
				return
			}

			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			if len(got) != len(tt.want) {
				t.Fatalf("unexpected map size: got=%d want=%d", len(got), len(tt.want))
			}

			for key, expectedValues := range tt.want {
				values, ok := got[key]
				if !ok {
					t.Fatalf("missing key %q", key)
				}
				if len(values) != len(expectedValues) {
					t.Fatalf("unexpected values length for %q: got=%d want=%d", key, len(values), len(expectedValues))
				}
				for i := range values {
					if values[i] != expectedValues[i] {
						t.Fatalf("unexpected value at %q[%d]: got=%q want=%q", key, i, values[i], expectedValues[i])
					}
				}
			}
		})
	}
}

// filterOpsStub stands in for the Live Debugger handler behind
// applyWorkspaceFilters, recording which calls were made.
type filterOpsStub struct {
	activeRules int
	counted     bool
	sent        []map[string]interface{}
	updated     bool
}

func (s *filterOpsStub) deps() liveDebuggerDeps {
	return liveDebuggerDeps{
		getWorkspaceRules: func(_ *livedebugger.Handler, _ string) (map[string]interface{}, error) {
			s.counted = true
			rules := make([]interface{}, 0, s.activeRules)
			for i := 0; i < s.activeRules; i++ {
				rules = append(rules, map[string]interface{}{"id": fmt.Sprintf("bp-%d", i), "is_disabled": false})
			}
			return map[string]interface{}{"data": map[string]interface{}{"org": map[string]interface{}{
				"workspace": map[string]interface{}{"rules": rules},
			}}}, nil
		},
		updateWorkspaceFilters: func(_ *livedebugger.Handler, _ string, filterSets []map[string]interface{}) (map[string]interface{}, error) {
			s.updated = true
			s.sent = filterSets
			return map[string]interface{}{}, nil
		},
	}
}

func TestApplyWorkspaceFilters(t *testing.T) {
	sets := []map[string][]string{
		{"k8s.workload.name": {"checkout"}, "k8s.namespace.name": {"payments"}},
		{"service.name": {"checkout"}},
	}
	// run calls the helper on an invocation of its own, so the plain-mode
	// switch and the answer typed at the prompt stay out of the process.
	call := func(stub *filterOpsStub, plain bool, change workspaceFilterChange, answer string) (bool, string, error) {
		var out bytes.Buffer
		ctx := withInvocation(context.Background(), &invocation{
			concurrent: true,
			flags:      rootFlags{plainMode: plain},
			stdin:      strings.NewReader(answer),
			stdout:     &out,
		})
		change.creating = true
		cancelled, err := applyWorkspaceFiltersWithDeps(ctx, nil, "ws-1", sets, change, stub.deps())
		return cancelled, out.String(), err
	}
	run := func(t *testing.T, stub *filterOpsStub, plain bool, change workspaceFilterChange, answer string) (bool, string) {
		t.Helper()
		cancelled, out, err := call(stub, plain, change, answer)
		if err != nil {
			t.Fatalf("applyWorkspaceFilters: %v", err)
		}
		return cancelled, out
	}

	t.Run("a declined confirmation changes nothing", func(t *testing.T) {
		stub := &filterOpsStub{activeRules: 2}
		cancelled, out := run(t, stub, false, workspaceFilterChange{}, "n\n")
		if !cancelled || stub.updated {
			t.Fatalf("cancelled=%t updated=%t, want a cancel and no update", cancelled, stub.updated)
		}
		if !strings.Contains(out, filterChangeConfirmMessage(2, true)) {
			t.Fatalf("prompt = %q, want the creating confirmation for 2 breakpoints", out)
		}
	})

	t.Run("an accepted confirmation sends every set", func(t *testing.T) {
		stub := &filterOpsStub{activeRules: 1}
		if cancelled, _ := run(t, stub, false, workspaceFilterChange{}, "y\n"); cancelled {
			t.Fatal("cancelled after the confirmation was accepted")
		}
		if want := livedebugger.BuildFilterSets(sets...); !reflect.DeepEqual(stub.sent, want) {
			t.Fatalf("sent %#v, want %#v", stub.sent, want)
		}
	})

	t.Run("no active breakpoints asks nothing", func(t *testing.T) {
		stub := &filterOpsStub{}
		cancelled, out := run(t, stub, false, workspaceFilterChange{}, "")
		if cancelled || !stub.counted || !stub.updated || out != "" {
			t.Fatalf("cancelled=%t counted=%t updated=%t prompt=%q, want a silent count and an update", cancelled, stub.counted, stub.updated, out)
		}
	})

	for _, tt := range []struct {
		name               string
		plain, skipConfirm bool
	}{
		{"--yes skips the count", false, true},
		{"a non-interactive run skips the count", true, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			stub := &filterOpsStub{activeRules: 3}
			cancelled, out := run(t, stub, tt.plain, workspaceFilterChange{skipConfirm: tt.skipConfirm}, "")
			if cancelled || stub.counted || !stub.updated || out != "" {
				t.Fatalf("cancelled=%t counted=%t updated=%t prompt=%q, want no count, no prompt and an update", cancelled, stub.counted, stub.updated, out)
			}
		})
	}

	t.Run("an unasked change is declined where nobody can confirm it", func(t *testing.T) {
		stub := &filterOpsStub{activeRules: 1}
		cancelled, out, err := call(stub, true, workspaceFilterChange{unasked: true}, "y\n")
		if !errors.Is(err, errFiltersDeclined) || cancelled || !stub.counted || stub.updated || out != "" {
			t.Fatalf("err=%v cancelled=%t counted=%t updated=%t prompt=%q, want a count, errFiltersDeclined, no update and no prompt", err, cancelled, stub.counted, stub.updated, out)
		}
	})

	// Outside plain mode with stdin at its end, as from /dev/null or a closed
	// pipe, the prompt reads nothing: that is nobody to ask, as in plain mode,
	// not a user who said no.
	t.Run("an unasked change at a prompt whose input ended is declined", func(t *testing.T) {
		stub := &filterOpsStub{activeRules: 1}
		cancelled, _, err := call(stub, false, workspaceFilterChange{unasked: true}, "")
		if !errors.Is(err, errFiltersDeclined) || cancelled || stub.updated {
			t.Fatalf("err=%v cancelled=%t updated=%t, want errFiltersDeclined and no update", err, cancelled, stub.updated)
		}
	})

	t.Run("a typed change at a prompt whose input ended is cancelled", func(t *testing.T) {
		stub := &filterOpsStub{activeRules: 1}
		cancelled, _ := run(t, stub, false, workspaceFilterChange{}, "")
		if !cancelled || stub.updated {
			t.Fatalf("cancelled=%t updated=%t, want a cancel and no update", cancelled, stub.updated)
		}
	})

	t.Run("an unasked change that re-scopes nothing is applied without asking", func(t *testing.T) {
		stub := &filterOpsStub{}
		cancelled, out := run(t, stub, true, workspaceFilterChange{unasked: true}, "")
		if cancelled || !stub.counted || !stub.updated || out != "" {
			t.Fatalf("cancelled=%t counted=%t updated=%t prompt=%q, want a silent count and an update", cancelled, stub.counted, stub.updated, out)
		}
	})
}

func TestFormatFilters(t *testing.T) {
	t.Run("sorted deterministic output", func(t *testing.T) {
		parsed := map[string][]string{
			"ns":   {"prod", "staging"},
			"host": {"HOST-1"},
		}
		got := formatFilters(parsed)
		want := "host:HOST-1,ns:prod,ns:staging"
		if got != want {
			t.Fatalf("unexpected formatted filters: got %q want %q", got, want)
		}
	})

	t.Run("normalizes raw input spacing", func(t *testing.T) {
		parsed, err := parseFilters("ns : prod")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got := formatFilters(parsed); got != "ns:prod" {
			t.Fatalf("expected normalized 'ns:prod', got %q", got)
		}
	})

	t.Run("empty map yields empty string", func(t *testing.T) {
		if got := formatFilters(map[string][]string{}); got != "" {
			t.Fatalf("expected empty string, got %q", got)
		}
	})
}

func TestRequireFiltersValue(t *testing.T) {
	t.Run("non-empty value passes", func(t *testing.T) {
		if err := requireFiltersValue("ns:prod"); err != nil {
			t.Fatalf("expected no error, got %v", err)
		}
	})

	t.Run("empty and whitespace-only values are rejected", func(t *testing.T) {
		for _, in := range []string{"", "   ", "\t"} {
			err := requireFiltersValue(in)
			if err == nil {
				t.Fatalf("expected error for %q, got nil", in)
			}
			if !strings.Contains(err.Error(), "provided without a value") {
				t.Fatalf("unexpected error message for %q: %v", in, err)
			}
		}
	})
}

func TestCountActiveRules(t *testing.T) {
	makeResp := func(rules []interface{}) map[string]interface{} {
		return map[string]interface{}{
			"data": map[string]interface{}{
				"org": map[string]interface{}{
					"workspace": map[string]interface{}{
						"rules": rules,
					},
				},
			},
		}
	}
	rule := func(id string, disabled bool) map[string]interface{} {
		return map[string]interface{}{"id": id, "is_disabled": disabled}
	}

	t.Run("counts only active (non-disabled) rules", func(t *testing.T) {
		resp := makeResp([]interface{}{
			rule("bp-1", false),
			rule("bp-2", true), // hit-limit-reached, auto-disabled
			rule("bp-3", false),
		})
		got, err := countActiveRules(resp)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got != 2 {
			t.Fatalf("expected 2 active rules, got %d", got)
		}
	})

	t.Run("all disabled yields zero", func(t *testing.T) {
		resp := makeResp([]interface{}{rule("bp-1", true), rule("bp-2", true)})
		got, err := countActiveRules(resp)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got != 0 {
			t.Fatalf("expected 0 active rules, got %d", got)
		}
	})

	t.Run("empty rules list yields zero", func(t *testing.T) {
		got, err := countActiveRules(makeResp([]interface{}{}))
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got != 0 {
			t.Fatalf("expected 0 active rules, got %d", got)
		}
	})

	t.Run("malformed response returns error", func(t *testing.T) {
		if _, err := countActiveRules(map[string]interface{}{}); err == nil {
			t.Fatal("expected error for malformed response, got nil")
		}
	})
}

func TestFilterChangeConfirmMessage(t *testing.T) {
	tests := []struct {
		name     string
		count    int
		creating bool
		want     string
	}{
		{
			name:  "update singular",
			count: 1,
			want:  "This will change the workspace filters for 1 active breakpoint. Continue?",
		},
		{
			name:  "update plural",
			count: 3,
			want:  "This will change the workspace filters for 3 active breakpoints. Continue?",
		},
		{
			name:     "create singular",
			count:    1,
			creating: true,
			want:     "In addition to creating this breakpoint, this will change the workspace filters for 1 active breakpoint. Continue?",
		},
		{
			name:     "create plural",
			count:    5,
			creating: true,
			want:     "In addition to creating this breakpoint, this will change the workspace filters for 5 active breakpoints. Continue?",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := filterChangeConfirmMessage(tt.count, tt.creating); got != tt.want {
				t.Fatalf("unexpected message:\n got: %q\nwant: %q", got, tt.want)
			}
		})
	}
}

func TestParseBreakpoint(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		wantFile string
		wantLine int
		wantErr  bool
	}{
		{name: "valid", input: "OrderController.java:306", wantFile: "OrderController.java", wantLine: 306},
		{name: "valid with spaces", input: " OrderController.java : 306 ", wantFile: "OrderController.java", wantLine: 306},
		{name: "missing separator", input: "OrderController.java", wantErr: true},
		{name: "empty file", input: ":123", wantErr: true},
		{name: "non numeric line", input: "OrderController.java:abc", wantErr: true},
		{name: "non positive line", input: "OrderController.java:0", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fileName, lineNumber, err := parseBreakpoint(tt.input)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("expected error, got nil")
				}
				return
			}

			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			if fileName != tt.wantFile {
				t.Fatalf("unexpected file name: got=%q want=%q", fileName, tt.wantFile)
			}

			if lineNumber != tt.wantLine {
				t.Fatalf("unexpected line number: got=%d want=%d", lineNumber, tt.wantLine)
			}
		})
	}
}

func TestBuildGraphQLResponse(t *testing.T) {
	payload := map[string]interface{}{"data": "value"}
	wrapper := buildGraphQLResponse("getWorkspaceRules", payload)

	if wrapper["operation"] != "getWorkspaceRules" {
		t.Fatalf("unexpected operation: %#v", wrapper["operation"])
	}
	response, ok := wrapper["response"].(map[string]interface{})
	if !ok {
		t.Fatalf("unexpected response type: %#v", wrapper["response"])
	}
	if response["data"] != payload["data"] {
		t.Fatalf("unexpected response payload: %#v", response)
	}
}

func TestPrintGraphQLResponse(t *testing.T) {
	originalOut := rootCmd.OutOrStdout()
	defer rootCmd.SetOut(originalOut)

	var out bytes.Buffer
	rootCmd.SetOut(&out)

	payload := map[string]interface{}{
		"data": map[string]interface{}{"ok": true},
	}

	if err := printGraphQLResponse(context.Background(), "getWorkspaceRules", payload); err != nil {
		t.Fatalf("printGraphQLResponse returned error: %v", err)
	}

	output := out.String()
	if !strings.Contains(output, "\"operation\": \"getWorkspaceRules\"") {
		t.Fatalf("missing operation in output: %q", output)
	}
	if !strings.Contains(output, "\"response\"") {
		t.Fatalf("missing response in output: %q", output)
	}
}

func TestPrintGraphQLResponse_NilPayload(t *testing.T) {
	if err := printGraphQLResponse(context.Background(), "noop", nil); err != nil {
		t.Fatalf("expected nil payload to return nil error, got: %v", err)
	}
}

func TestCurrentProjectPath(t *testing.T) {
	originalCwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("failed to get cwd: %v", err)
	}
	defer func() { _ = os.Chdir(originalCwd) }()

	tmp := t.TempDir()
	if err := os.Chdir(tmp); err != nil {
		t.Fatalf("failed to chdir: %v", err)
	}

	if got := currentProjectPath(context.Background()); got == "" || got == "no-project" {
		t.Fatalf("expected project name from cwd, got %q", got)
	}

	// An embedded invocation has no working directory of its own, so it never
	// names its workspace after the server's.
	for name, inv := range map[string]*invocation{
		"without HostWorkingDirectory": {concurrent: true},
		"serving a session":            {concurrent: true, caps: Capabilities{HostWorkingDirectory: true}, session: &Session{}},
	} {
		if got := currentProjectPath(withInvocation(context.Background(), inv)); got != "no-project" {
			t.Errorf("%s: currentProjectPath = %q, want no-project", name, got)
		}
	}
}

func TestIsDebugVerbose(t *testing.T) {
	originalDebugMode := debugMode(context.Background())
	originalVerbosity := verbosity(context.Background())
	defer func() {
		gFlags.debugMode = originalDebugMode
		gFlags.verbosity = originalVerbosity
	}()

	gFlags.debugMode = false
	gFlags.verbosity = 0
	if isDebugVerbose(context.Background()) {
		t.Fatalf("expected false when debugMode() and verbosity() are disabled")
	}

	gFlags.verbosity = 1
	if !isDebugVerbose(context.Background()) {
		t.Fatalf("expected true when verbosity() > 0")
	}

	gFlags.verbosity = 0
	gFlags.debugMode = true
	if !isDebugVerbose(context.Background()) {
		t.Fatalf("expected true when debugMode() is enabled")
	}
}

func TestRunGetBreakpoints_LoadConfigError(t *testing.T) {
	originalCfgFile := cfgFile(context.Background())
	defer func() { gFlags.cfgFile = originalCfgFile }()

	gFlags.cfgFile = filepath.Join(t.TempDir(), "missing-config.yaml")

	err := runGetBreakpoints(nil, nil)
	if err == nil {
		t.Fatalf("expected load config error")
	}
}

func TestRunGetBreakpoints_TableView(t *testing.T) {
	originalOutputFormat := outputFormat(context.Background())
	originalAgentMode := agentMode(context.Background())
	originalDebugMode := debugMode(context.Background())
	originalVerbosity := verbosity(context.Background())
	originalOut := rootCmd.OutOrStdout()
	defer func() {
		gFlags.outputFormat = originalOutputFormat
		gFlags.agentMode = originalAgentMode
		gFlags.debugMode = originalDebugMode
		gFlags.verbosity = originalVerbosity
		rootCmd.SetOut(originalOut)
	}()

	gFlags.outputFormat = ""
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

	var out bytes.Buffer
	rootCmd.SetOut(&out)

	if err := runGetBreakpointsWithDeps(nil, nil, deps); err != nil {
		t.Fatalf("runGetBreakpoints returned error: %v", err)
	}

	text := out.String()
	if !strings.Contains(text, "bp-1") || !strings.Contains(text, "OrderController.java") {
		t.Fatalf("unexpected table output: %q", text)
	}
}

func TestRunGetBreakpoints_StructuredView(t *testing.T) {
	originalOutputFormat := outputFormat(context.Background())
	originalAgentMode := agentMode(context.Background())
	originalDebugMode := debugMode(context.Background())
	originalVerbosity := verbosity(context.Background())
	originalOut := rootCmd.OutOrStdout()
	defer func() {
		gFlags.outputFormat = originalOutputFormat
		gFlags.agentMode = originalAgentMode
		gFlags.debugMode = originalDebugMode
		gFlags.verbosity = originalVerbosity
		rootCmd.SetOut(originalOut)
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

	var out bytes.Buffer
	rootCmd.SetOut(&out)

	if err := runGetBreakpointsWithDeps(nil, nil, deps); err != nil {
		t.Fatalf("runGetBreakpoints returned error: %v", err)
	}

	output := out.String()

	if !strings.Contains(output, "\"id\": \"bp-1\"") || !strings.Contains(output, "\"filename\": \"OrderController.java\"") {
		t.Fatalf("unexpected structured output: %q", output)
	}
	if !strings.Contains(output, "\"logMessage\": \"Hit on {frame.filename}:{frame.line}\"") {
		t.Fatalf("expected unqualified log message in output: %q", output)
	}
}

func TestRunGetBreakpoints_AgentEnvelope(t *testing.T) {
	originalOutputFormat := outputFormat(context.Background())
	originalAgentMode := agentMode(context.Background())
	originalDebugMode := debugMode(context.Background())
	originalVerbosity := verbosity(context.Background())
	originalOut := rootCmd.OutOrStdout()
	defer func() {
		gFlags.outputFormat = originalOutputFormat
		gFlags.agentMode = originalAgentMode
		gFlags.debugMode = originalDebugMode
		gFlags.verbosity = originalVerbosity
		rootCmd.SetOut(originalOut)
	}()

	gFlags.outputFormat = ""
	gFlags.agentMode = true
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

	var out bytes.Buffer
	rootCmd.SetOut(&out)

	if err := runGetBreakpointsWithDeps(nil, nil, deps); err != nil {
		t.Fatalf("runGetBreakpoints returned error: %v", err)
	}

	output := out.String()

	if !strings.Contains(output, "\"ok\":true") || !strings.Contains(output, "\"resource\":\"breakpoint\"") {
		t.Fatalf("unexpected agent output: %q", output)
	}
}

func TestRunGetBreakpoints_GetWorkspaceRulesError(t *testing.T) {
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
		return nil, os.ErrPermission
	}

	err := runGetBreakpointsWithDeps(nil, nil, deps)
	if err == nil {
		t.Fatalf("expected get workspace rules error")
	}
}

func TestExtractWorkspaceRulesErrors(t *testing.T) {
	tests := []struct {
		name string
		resp map[string]interface{}
		want string
	}{
		{name: "missing data", resp: map[string]interface{}{}, want: "missing data object"},
		{name: "missing org", resp: map[string]interface{}{"data": map[string]interface{}{}}, want: "missing org object"},
		{name: "missing workspace", resp: map[string]interface{}{"data": map[string]interface{}{"org": map[string]interface{}{}}}, want: "missing workspace object"},
		{name: "missing rules", resp: map[string]interface{}{"data": map[string]interface{}{"org": map[string]interface{}{"workspace": map[string]interface{}{}}}}, want: "missing rules list"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := extractWorkspaceRules(tt.resp)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("expected error containing %q, got %v", tt.want, err)
			}
		})
	}
}

func TestExtractBreakpointRowsSkipsInvalidRules(t *testing.T) {
	resp := map[string]interface{}{
		"data": map[string]interface{}{
			"org": map[string]interface{}{
				"workspace": map[string]interface{}{
					"rules": []interface{}{
						map[string]interface{}{"id": "bad-1"},
						map[string]interface{}{
							"id":          "ok-1",
							"is_disabled": false,
							"aug_json": map[string]interface{}{
								"location": map[string]interface{}{"filename": "A.java", "lineno": float64(2)},
							},
						},
					},
				},
			},
		},
	}

	rows, err := extractBreakpointRows(resp)
	if err != nil {
		t.Fatalf("extractBreakpointRows returned error: %v", err)
	}
	if len(rows) != 1 || rows[0].ID != "ok-1" {
		t.Fatalf("unexpected rows: %#v", rows)
	}
}

func TestPadBreakpointID(t *testing.T) {
	tests := []struct {
		name string
		id   string
		want string
	}{
		{name: "empty passthrough", id: "", want: ""},
		{name: "short id padded to 32", id: "abc", want: strings.Repeat("0", 29) + "abc"},
		{name: "exactly 32 chars passthrough", id: strings.Repeat("a", 32), want: strings.Repeat("a", 32)},
		{name: "longer than 32 passthrough", id: strings.Repeat("a", 33), want: strings.Repeat("a", 33)},
		{name: "numeric id padded", id: "12345", want: strings.Repeat("0", 27) + "12345"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := padBreakpointID(tt.id)
			if got != tt.want {
				t.Fatalf("padBreakpointID(%q) = %q, want %q", tt.id, got, tt.want)
			}
		})
	}
}

func TestBreakpointRowFromRule_LinenoTypesAndFallbacks(t *testing.T) {
	tests := []struct {
		name     string
		lineno   interface{}
		wantLine int
	}{
		{name: "int32", lineno: int32(5), wantLine: 5},
		{name: "int64", lineno: int64(6), wantLine: 6},
		{name: "invalid type uses zero", lineno: "7", wantLine: 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			row, ok := breakpointRowFromRule(livedebugger.BreakpointRule{
				ID:         "bp-1",
				IsDisabled: false,
				AugJSON: map[string]interface{}{
					"location": map[string]interface{}{"filename": "A.java", "lineno": tt.lineno},
				},
			})
			if !ok {
				t.Fatalf("expected valid row")
			}
			if row.Line != tt.wantLine {
				t.Fatalf("line=%d want=%d", row.Line, tt.wantLine)
			}
		})
	}

	if _, ok := breakpointRowFromRule(livedebugger.BreakpointRule{ID: "bp-1"}); ok {
		t.Fatalf("expected rule without aug_json to fail")
	}
}
