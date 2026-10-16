package cmd

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/spf13/viper"

	"github.com/dynatrace-oss/dtctl/pkg/resources/livedebugger"
)

func TestUpdateBreakpointCommandRegistration(t *testing.T) {
	updateCmd, _, err := rootCmd.Find([]string{"update"})
	if err != nil {
		t.Fatalf("expected update command to exist, got error: %v", err)
	}
	if updateCmd == nil || updateCmd.Name() != "update" {
		t.Fatalf("expected update command to exist")
	}

	breakpointCmd, _, err := rootCmd.Find([]string{"update", "breakpoint"})
	if err != nil {
		t.Fatalf("expected update breakpoint command to exist, got error: %v", err)
	}
	if breakpointCmd == nil || breakpointCmd.Name() != "breakpoint" {
		t.Fatalf("expected update breakpoint command to exist")
	}

	breakpointsCmd, _, err := rootCmd.Find([]string{"update", "breakpoints"})
	if err != nil {
		t.Fatalf("expected update breakpoints alias to exist, got error: %v", err)
	}
	if breakpointsCmd == nil || breakpointsCmd.Name() != "breakpoint" {
		t.Fatalf("expected update breakpoints alias to resolve to breakpoint command")
	}
}

func TestUpdateBreakpointYesFlagRegistered(t *testing.T) {
	flag := updateBreakpointCmd.Flags().Lookup("yes")
	if flag == nil {
		t.Fatalf("expected --yes flag to be registered on update breakpoint")
	}
	if flag.Shorthand != "y" {
		t.Fatalf("expected --yes shorthand 'y', got %q", flag.Shorthand)
	}
	if flag.DefValue != "false" {
		t.Fatalf("expected --yes default 'false', got %q", flag.DefValue)
	}
}

func TestUpdateBreakpointFilters_NoIdentifier_NoPanic(t *testing.T) {
	viper.Reset()

	tmpDir := t.TempDir()
	gFlags.cfgFile = filepath.Join(tmpDir, "missing-config.yaml")
	defer func() { gFlags.cfgFile = "" }()

	rootCmd.SetArgs([]string{"update", "breakpoint", "--filters", "k8s.container.name=credit-card-order-service"})
	err := rootCmd.Execute()

	if err == nil {
		t.Fatal("expected error but got nil")
	}

	if strings.Contains(strings.ToLower(err.Error()), "slice bounds out of range") {
		t.Fatalf("unexpected panic-like error: %v", err)
	}
}

func TestBuildEditBreakpointSettings(t *testing.T) {
	rule := livedebugger.BreakpointRule{
		ID: "dtctl-rule-1",
		AugJSON: map[string]interface{}{
			"action": map[string]interface{}{
				"operations": []interface{}{
					map[string]interface{}{
						"name": "set",
						"paths": map[string]interface{}{
							"store.rookout.frame":                    "frame.dump()",
							"store.rookout.traceback":                "stack.traceback()",
							"store.rookout.tracing":                  "trace.dump()",
							"store.rookout.processMonitoring":        "state.dump()",
							"store.rookout.variables.customerId":     "frame.customerId",
							"store.rookout.variables.MyClass.thread": "utils.class(\"com.example.MyClass\").thread",
						},
					},
				},
			},
			"conditional":            nil,
			"globalDisableAfterTime": "2026-03-17T08:25:11Z",
			"globalHitLimit":         float64(100),
			"location": map[string]interface{}{
				"filename": "OrderController.java",
				"lineno":   float64(306),
			},
			"rateLimit": "150/20000",
		},
		Processing: map[string]interface{}{
			"operations": []interface{}{
				map[string]interface{}{"name": "set", "paths": map[string]interface{}{"temp.message.rookout": "store.rookout"}},
				map[string]interface{}{"name": "format", "path": "temp.message.rookout.message", "format": "Hit on {store.rookout.frame.filename}:{store.rookout.frame.line}"},
				map[string]interface{}{"name": "send_rookout", "path": "temp.message"},
			},
		},
	}

	settings, err := buildEditBreakpointSettings(rule, "value>othervalue", true, "", false)
	if err != nil {
		t.Fatalf("buildEditBreakpointSettings returned error: %v", err)
	}

	if settings["mutableRuleId"] != "dtctl-rule-1" {
		t.Fatalf("unexpected mutableRuleId: %#v", settings["mutableRuleId"])
	}
	if settings["condition"] != "value>othervalue" {
		t.Fatalf("unexpected condition: %#v", settings["condition"])
	}
	if settings["outputMessage"] != breakpointDefaultOutputMessage {
		t.Fatalf("unexpected outputMessage: %#v", settings["outputMessage"])
	}
	if settings["collectLocalsMethod"] != "frame.dump()" {
		t.Fatalf("unexpected collectLocalsMethod: %#v", settings["collectLocalsMethod"])
	}
	if settings["stackTraceCollection"] != true {
		t.Fatalf("unexpected stackTraceCollection: %#v", settings["stackTraceCollection"])
	}
	if settings["tracingCollection"] != true {
		t.Fatalf("unexpected tracingCollection: %#v", settings["tracingCollection"])
	}
	if settings["processMonitoringCollection"] != true {
		t.Fatalf("unexpected processMonitoringCollection: %#v", settings["processMonitoringCollection"])
	}

	collectedVariables, ok := settings["collectedVariables"].([]string)
	if !ok {
		t.Fatalf("unexpected collectedVariables type: %#v", settings["collectedVariables"])
	}
	if len(collectedVariables) != 2 {
		t.Fatalf("unexpected collectedVariables length: %#v", collectedVariables)
	}
	if collectedVariables[0] != "com.example.MyClass.thread" || collectedVariables[1] != "customerId" {
		t.Fatalf("unexpected collectedVariables: %#v", collectedVariables)
	}

	targetConfiguration, ok := settings["targetConfiguration"].(map[string]interface{})
	if !ok {
		t.Fatalf("unexpected targetConfiguration: %#v", settings["targetConfiguration"])
	}
	if targetConfiguration["targetId"] != breakpointRookoutTargetID {
		t.Fatalf("unexpected targetId: %#v", targetConfiguration["targetId"])
	}
}

func TestBuildEditBreakpointSettings_LogMessageChanged(t *testing.T) {
	rule := livedebugger.BreakpointRule{
		ID: "dtctl-rule-2",
		AugJSON: map[string]interface{}{
			"action": map[string]interface{}{
				"operations": []interface{}{
					map[string]interface{}{
						"name": "set",
						"paths": map[string]interface{}{
							"store.rookout.frame":     "frame.dump()",
							"store.rookout.traceback": "stack.traceback()",
						},
					},
				},
			},
			"location":  map[string]interface{}{"filename": "OrderController.java", "lineno": float64(306)},
			"rateLimit": "150/20000",
		},
		Processing: map[string]interface{}{
			"operations": []interface{}{
				map[string]interface{}{"name": "set", "paths": map[string]interface{}{"temp.message.rookout": "store.rookout"}},
				map[string]interface{}{"name": "format", "path": "temp.message.rookout.message", "format": "Hit on {store.rookout.frame.filename}:{store.rookout.frame.line}"},
				map[string]interface{}{"name": "send_rookout", "path": "temp.message"},
			},
		},
	}

	settings, err := buildEditBreakpointSettings(rule, "", false, "Hit on {frame.filename}:{frame.line} value={myVar}", true)
	if err != nil {
		t.Fatalf("buildEditBreakpointSettings returned error: %v", err)
	}

	// When logMessageChanged=true, outputMessage must be the raw user-supplied string.
	// The backend is responsible for qualifying {frame.X} → store.rookout.frame.X at storage time.
	if settings["outputMessage"] != "Hit on {frame.filename}:{frame.line} value={myVar}" {
		t.Fatalf("expected raw user-supplied log message, got: %#v", settings["outputMessage"])
	}

	// Empty string resets to the friendly default rather than sending an empty message.
	settings, err = buildEditBreakpointSettings(rule, "", false, "", true)
	if err != nil {
		t.Fatalf("buildEditBreakpointSettings returned error: %v", err)
	}
	if settings["outputMessage"] != "Hit on {frame.filename}:{frame.line}" {
		t.Fatalf("expected friendly default on empty log message, got: %#v", settings["outputMessage"])
	}
}

func TestResolveBreakpointRulesForEdit(t *testing.T) {
	rules := []livedebugger.BreakpointRule{
		{
			ID: "bp-1",
			AugJSON: map[string]interface{}{
				"location": map[string]interface{}{"filename": "A.java", "lineno": float64(10)},
			},
		},
		{
			ID: "bp-2",
			AugJSON: map[string]interface{}{
				"location": map[string]interface{}{"filename": "A.java", "lineno": float64(10)},
			},
		},
	}

	matches, description, allowDirectID, err := resolveBreakpointRulesForEdit(rules, "A.java:10")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(matches) != 2 || description != "A.java:10" || allowDirectID {
		t.Fatalf("unexpected location resolution result: len=%d desc=%q direct=%t", len(matches), description, allowDirectID)
	}

	matches, description, allowDirectID, err = resolveBreakpointRulesForEdit(rules, "bp-1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(matches) != 1 || description != "bp-1" || allowDirectID {
		t.Fatalf("unexpected id resolution result: len=%d desc=%q direct=%t", len(matches), description, allowDirectID)
	}

	matches, description, allowDirectID, err = resolveBreakpointRulesForEdit(rules, "missing")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(matches) != 0 || description != "missing" || !allowDirectID {
		t.Fatalf("unexpected fallback resolution result: len=%d desc=%q direct=%t", len(matches), description, allowDirectID)
	}
}

func TestGetOptionalBoolFlag(t *testing.T) {
	cmd := &cobra.Command{Use: "test"}
	cmd.Flags().String("enabled", "", "")
	cmd.Flags().Lookup("enabled").NoOptDefVal = "true"

	if err := cmd.Flags().Parse([]string{"--enabled", "true"}); err != nil {
		t.Fatalf("unexpected parse error for explicit true: %v", err)
	}

	enabled, changed, err := getOptionalBoolFlag(cmd, "enabled", nil)
	if err != nil {
		t.Fatalf("unexpected getOptionalBoolFlag error: %v", err)
	}
	if !changed || !enabled {
		t.Fatalf("expected enabled=true changed=true, got enabled=%t changed=%t", enabled, changed)
	}

	cmd = &cobra.Command{Use: "test"}
	cmd.Flags().String("enabled", "", "")
	cmd.Flags().Lookup("enabled").NoOptDefVal = "true"
	if err := cmd.Flags().Parse([]string{"--enabled", "false"}); err != nil {
		t.Fatalf("unexpected parse error for explicit false: %v", err)
	}

	enabled, changed, err = getOptionalBoolFlag(cmd, "enabled", []string{"false"})
	if err != nil {
		t.Fatalf("unexpected getOptionalBoolFlag error: %v", err)
	}
	if !changed || enabled {
		t.Fatalf("expected enabled=false changed=true, got enabled=%t changed=%t", enabled, changed)
	}

	cmd = &cobra.Command{Use: "test"}
	cmd.Flags().String("enabled", "", "")
	cmd.Flags().Lookup("enabled").NoOptDefVal = "true"
	if err := cmd.Flags().Parse([]string{"--enabled"}); err != nil {
		t.Fatalf("unexpected parse error for implicit true: %v", err)
	}

	enabled, changed, err = getOptionalBoolFlag(cmd, "enabled", nil)
	if err != nil {
		t.Fatalf("unexpected getOptionalBoolFlag error: %v", err)
	}
	if !changed || !enabled {
		t.Fatalf("expected enabled=true changed=true, got enabled=%t changed=%t", enabled, changed)
	}
}

func TestGetOptionalBoolFlag_ErrorCases(t *testing.T) {
	t.Run("too many trailing args without flag", func(t *testing.T) {
		cmd := &cobra.Command{Use: "test"}
		got, changed, err := getOptionalBoolFlag(cmd, "enabled", []string{"true", "false"})
		if err == nil {
			t.Fatalf("expected error")
		}
		if got || changed {
			t.Fatalf("unexpected result: got=%t changed=%t", got, changed)
		}
	})

	t.Run("too many trailing args with flag", func(t *testing.T) {
		cmd := &cobra.Command{Use: "test"}
		cmd.Flags().String("enabled", "", "")
		cmd.Flags().Lookup("enabled").NoOptDefVal = "true"
		if err := cmd.Flags().Parse([]string{"--enabled", "true"}); err != nil {
			t.Fatalf("parse error: %v", err)
		}
		got, changed, err := getOptionalBoolFlag(cmd, "enabled", []string{"true", "false"})
		if err == nil {
			t.Fatalf("expected error")
		}
		if !changed || got {
			t.Fatalf("unexpected result: got=%t changed=%t", got, changed)
		}
	})

	t.Run("invalid trailing arg bool", func(t *testing.T) {
		cmd := &cobra.Command{Use: "test"}
		cmd.Flags().String("enabled", "", "")
		if err := cmd.Flags().Parse([]string{"--enabled", "true"}); err != nil {
			t.Fatalf("parse error: %v", err)
		}
		_, changed, err := getOptionalBoolFlag(cmd, "enabled", []string{"not-bool"})
		if err == nil || !changed {
			t.Fatalf("expected invalid bool error with changed=true, got err=%v changed=%t", err, changed)
		}
	})

	t.Run("invalid flag value bool", func(t *testing.T) {
		cmd := &cobra.Command{Use: "test"}
		cmd.Flags().String("enabled", "", "")
		if err := cmd.Flags().Set("enabled", "invalid"); err != nil {
			t.Fatalf("set error: %v", err)
		}
		_, changed, err := getOptionalBoolFlag(cmd, "enabled", nil)
		if err == nil || !changed {
			t.Fatalf("expected invalid bool error with changed=true, got err=%v changed=%t", err, changed)
		}
	})
}

func TestExtractBreakpointOperationPaths(t *testing.T) {
	t.Run("missing action", func(t *testing.T) {
		paths := extractBreakpointOperationPaths(map[string]interface{}{})
		if len(paths) != 0 {
			t.Fatalf("expected empty map, got %#v", paths)
		}
	})

	t.Run("invalid operations type", func(t *testing.T) {
		paths := extractBreakpointOperationPaths(map[string]interface{}{"action": map[string]interface{}{"operations": "invalid"}})
		if len(paths) != 0 {
			t.Fatalf("expected empty map, got %#v", paths)
		}
	})

	t.Run("merges valid operation paths", func(t *testing.T) {
		paths := extractBreakpointOperationPaths(map[string]interface{}{
			"action": map[string]interface{}{
				"operations": []interface{}{
					"skip",
					map[string]interface{}{"name": "set"},
					map[string]interface{}{"name": "set", "paths": map[string]interface{}{"a": "1", "b": "2"}},
					map[string]interface{}{"name": "set", "paths": map[string]interface{}{"b": "3"}},
				},
			},
		})

		if got := len(paths); got != 2 {
			t.Fatalf("expected 2 paths, got %d (%#v)", got, paths)
		}
		if paths["a"] != "1" || paths["b"] != "3" {
			t.Fatalf("unexpected merged paths: %#v", paths)
		}
	})
}

func TestIntValue(t *testing.T) {
	if got := intValue(10, 99); got != 10 {
		t.Fatalf("unexpected int conversion: %d", got)
	}
	if got := intValue(int32(11), 99); got != 11 {
		t.Fatalf("unexpected int32 conversion: %d", got)
	}
	if got := intValue(int64(12), 99); got != 12 {
		t.Fatalf("unexpected int64 conversion: %d", got)
	}
	if got := intValue(float64(13.9), 99); got != 13 {
		t.Fatalf("unexpected float64 conversion: %d", got)
	}
	if got := intValue("invalid", 99); got != 99 {
		t.Fatalf("unexpected default conversion: %d", got)
	}
}

func TestDescribeBreakpointEdits(t *testing.T) {
	if got := describeBreakpointEdits(false, "", false, "", false, false); got != "" {
		t.Fatalf("expected empty changes, got %q", got)
	}
	if got := describeBreakpointEdits(true, "a>1", false, "", false, false); got != "condition=\"a>1\"" {
		t.Fatalf("unexpected condition-only description: %q", got)
	}
	if got := describeBreakpointEdits(false, "", false, "", true, true); got != "enabled=true" {
		t.Fatalf("unexpected enabled-only description: %q", got)
	}
	if got := describeBreakpointEdits(true, "a>1", false, "", true, false); got != "condition=\"a>1\", enabled=false" {
		t.Fatalf("unexpected combined description: %q", got)
	}
	if got := describeBreakpointEdits(false, "", true, "Hit {frame.line}", false, false); got != "log-message=\"Hit {frame.line}\"" {
		t.Fatalf("unexpected log-message-only description: %q", got)
	}
	if got := describeBreakpointEdits(false, "", true, "", false, false); got != "log-message=\"Hit on {frame.filename}:{frame.line}\"" {
		t.Fatalf("unexpected empty log-message reset description: %q", got)
	}
}

func TestUnqualifyBreakpointOutputMessage(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "frame prefix stripped to frame",
			in:   "Hit on {store.rookout.frame.filename}:{store.rookout.frame.line}",
			want: "Hit on {frame.filename}:{frame.line}",
		},
		{
			name: "variables prefix stripped entirely",
			in:   "value={store.rookout.variables.newTodoRecord.title}",
			want: "value={newTodoRecord.title}",
		},
		{
			name: "other namespaces pass through unchanged",
			in:   "agent {agent.id} rook {rook.id} bp {bp.id}",
			want: "agent {agent.id} rook {rook.id} bp {bp.id}",
		},
		{
			name: "mixed prefixes",
			in:   "{store.rookout.frame.line} {store.rookout.variables.x.y} {controller.id}",
			want: "{frame.line} {x.y} {controller.id}",
		},
		{
			name: "no placeholders",
			in:   "plain message",
			want: "plain message",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := unqualifyBreakpointOutputMessage(tc.in); got != tc.want {
				t.Fatalf("unqualifyBreakpointOutputMessage(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}
