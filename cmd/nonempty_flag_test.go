package cmd

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/Dynatrace/dtctl/pkg/client"
)

// TestEmptyFlagValueIsAUsageError covers the whole invocation: a live run
// showed the rejection exiting 1 with cobra's raw wording, where a bad flag
// value is a usage error (exit 2). The tab and the non-breaking space are
// there because %q escapes them, which a message-matching enhancer missed.
func TestEmptyFlagValueIsAUsageError(t *testing.T) {
	values := map[string]string{
		"empty":              "",
		"space":              " ",
		"tab":                "\t",
		"newline":            "\n",
		"non-breaking space": " ",
	}

	for name, value := range values {
		t.Run(name, func(t *testing.T) {
			t.Cleanup(func() { resetFlagSet(logsWorkflowExecutionCmd.Flags()) })

			args := []string{"--plain", "logs", "wfe", "exec-1", "--task", value}
			if code := Run(args, RunOptions{}); code != client.ExitUsageError {
				t.Errorf("exit code = %d, want %d", code, client.ExitUsageError)
			}
		})
	}
}

// TestEmptyFlagValueIsRejected covers #494: an explicitly empty value, as an
// unset shell variable produces, must fail the parse instead of counting as
// "flag given" (required flags) or as "flag absent" (logs --task).
func TestEmptyFlagValueIsRejected(t *testing.T) {
	tests := []struct {
		name     string
		cmd      *cobra.Command
		wantFlag string
		args     []string
	}{
		{"logs task", logsWorkflowExecutionCmd, "task", []string{"logs", "wfe", "exec-1", "--task", ""}},
		{"logs task with all", logsWorkflowExecutionCmd, "task", []string{"logs", "wfe", "exec-1", "--task=", "--all"}},
		{"task result", getWfeTaskResultCmd, "task", []string{"get", "wfe-task-result", "exec-1", "--task", " "}},
		{"settings schema", createSettingsCmd, "schema", []string{"create", "settings", "-f", "x.yaml", "--schema", "", "--scope", "environment"}},
		{"apply file", applyCmd, "file", []string{"apply", "-f", ""}},
		{"global context", rootCmd, "context", []string{"get", "workflows", "--context", ""}},
		{"global config", rootCmd, "config", []string{"get", "workflows", "--config", ""}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Cleanup(func() {
				rootCmd.SetArgs(nil)
				resetFlagSet(tt.cmd.Flags())
				resetFlagSet(rootCmd.PersistentFlags())
			})

			setupErrorHandlers(rootCmd)
			rootCmd.SetArgs(tt.args)
			err := rootCmd.Execute()
			if err == nil {
				t.Fatal("expected an error for an empty flag value, got nil")
			}

			if !errors.Is(err, errEmptyFlagValue) {
				t.Fatalf("error = %q (%T), want it to wrap errEmptyFlagValue so the invocation exits with the usage code", err, err)
			}
			if want := "--" + tt.wantFlag + " "; !strings.HasPrefix(err.Error(), want) {
				t.Errorf("error = %q, want it to name the flag (prefix %q)", err, want)
			}
			if code := exitCodeForError(err); code != client.ExitUsageError {
				t.Errorf("exit code = %d, want %d", code, client.ExitUsageError)
			}
		})
	}
}

// clearFlag returns one flag to its declared default, as resetFlagSet does for
// a whole set. Tests that reset a flag with Set(name, "") cannot use that on a
// flag that rejects an empty value: the Set fails and the old value stays.
func clearFlag(cmd *cobra.Command, name string) {
	f := cmd.Flags().Lookup(name)
	f.Changed = false
	if rv, ok := f.Value.(interface{ Reset() }); ok {
		rv.Reset()
		return
	}
	_ = f.Value.Set(f.DefValue)
}

// TestRejectEmptySliceFlagKeepsHelpDefault pins that wrapping a repeatable
// flag leaves its help line alone: pflag prints "(default [])" for a value
// type it does not recognize, and the wrapper is one.
func TestRejectEmptySliceFlagKeepsHelpDefault(t *testing.T) {
	if _, ok := shareDashboardCmd.Flags().Lookup("user").Value.(*nonEmptySliceValue); !ok {
		t.Fatal("share dashboard --user is no longer wrapped; pick another wrapped slice flag")
	}
	usage := shareDashboardCmd.UsageString()
	if strings.Contains(usage, "(default [])") {
		t.Errorf("usage shows a default for a wrapped slice flag:\n%s", usage)
	}
	if !strings.Contains(usage, "--user stringArray") {
		t.Errorf("usage lost the --user flag:\n%s", usage)
	}
}

func TestNonEmptyFlagResetRestoresDefault(t *testing.T) {
	cmd := &cobra.Command{Use: "x"}
	cmd.Flags().String("name", "fallback", "")
	rejectEmptyFlag(cmd, "name")

	if err := cmd.Flags().Set("name", "given"); err != nil {
		t.Fatalf("Set: %v", err)
	}
	resetFlagSet(cmd.Flags())

	if got, _ := cmd.Flags().GetString("name"); got != "fallback" {
		t.Errorf("after reset --name = %q, want %q", got, "fallback")
	}
}

// TestUnknownFlagIsAUsageError guards the enhancer against enhancing twice:
// the second pass no longer recognizes its own wording and used to hand back
// an untyped error, which cost the invocation its exit code.
func TestUnknownFlagIsAUsageError(t *testing.T) {
	t.Cleanup(func() { resetFlagSet(rootCmd.PersistentFlags()) })

	if code := Run([]string{"--plain", "logs", "wfe", "exec-1", "--bogus"}, RunOptions{}); code != client.ExitUsageError {
		t.Errorf("exit code = %d, want %d", code, client.ExitUsageError)
	}
}

// TestFlagErrorEnvelope covers the agent side of a flag error. pflag stops at
// the bad flag, so an --agent given after it never reaches the flag vars; the
// envelope must not depend on where the caller put it. An empty value is a
// validation_error: the flag exists, so unknown_command's did-you-mean advice
// would send the caller looking for a flag it already spelled right.
func TestFlagErrorEnvelope(t *testing.T) {
	tests := []struct {
		name     string
		args     []string
		wantCode string
	}{
		{"empty value, agent first", []string{"--agent", "logs", "wfe", "exec-1", "--task", ""}, "validation_error"},
		{"empty value, agent last", []string{"logs", "wfe", "exec-1", "--task", "", "--agent"}, "validation_error"},
		{"empty value, short agent last", []string{"logs", "wfe", "exec-1", "--task", "", "-A"}, "validation_error"},
		{"unknown flag, agent last", []string{"logs", "wfe", "exec-1", "--bogus", "--agent"}, "unknown_command"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Cleanup(func() {
				resetFlagSet(logsWorkflowExecutionCmd.Flags())
				resetFlagSet(rootCmd.PersistentFlags())
			})

			var code int
			out := captureStdout(t, func() { code = Run(tt.args, RunOptions{}) })

			if code != client.ExitUsageError {
				t.Errorf("exit code = %d, want %d", code, client.ExitUsageError)
			}
			var resp struct {
				OK    bool `json:"ok"`
				Error struct {
					Code string `json:"code"`
				} `json:"error"`
			}
			if err := json.Unmarshal([]byte(out), &resp); err != nil {
				t.Fatalf("stdout is not an envelope: %v\n%s", err, out)
			}
			if resp.OK || resp.Error.Code != tt.wantCode {
				t.Errorf("envelope ok=%v code=%q, want ok=false code=%q", resp.OK, resp.Error.Code, tt.wantCode)
			}
		})
	}
}
