package cmd

import (
	"context"
	"fmt"

	"errors"
	"io"
	"strings"
	"testing"

	"github.com/Dynatrace/dtctl/pkg/util/template"

	"github.com/spf13/cobra"
)

// runRejected executes a fresh command and returns its error. Every case must
// fail validation before setup, so none needs a config or a server.
func runRejected(t *testing.T, c *cobra.Command, args ...string) error {
	t.Helper()
	c.SetArgs(args)
	c.SetOut(io.Discard)
	c.SetErr(io.Discard)
	c.SilenceUsage = true
	c.SilenceErrors = true
	return c.Execute()
}

func TestEnumFilterValuesAreChecked(t *testing.T) {
	cases := []struct {
		name string
		cmd  func() *cobra.Command
		args []string
		want []string
	}{
		{"workflow type", newGetWorkflowsCmd, []string{"--type", "bogus"}, []string{"--type", `"bogus"`, "STANDARD, SIMPLE"}},
		{"execution state", newGetWorkflowExecutionsCmd, []string{"--state", "done"}, []string{"--state", "RUNNING, SUCCESS, ERROR, CANCELLED, UNKNOWN"}},
		{"execution trigger", newGetWorkflowExecutionsCmd, []string{"--trigger", "cron"}, []string{"--trigger", "Manual, Schedule, Event, Workflow"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := runRejected(t, tc.cmd(), tc.args...)
			if err == nil {
				t.Fatal("want an error")
			}
			var invalid *invalidInputError
			if !errors.As(err, &invalid) {
				t.Errorf("error %v is not a usage (invalidInputError) error", err)
			}
			for _, w := range tc.want {
				if !strings.Contains(err.Error(), w) {
					t.Errorf("error %q does not contain %q", err, w)
				}
			}
		})
	}
}

func TestWorkflowsHasNoTriggerFilter(t *testing.T) {
	if newGetWorkflowsCmd().Flags().Lookup("trigger") != nil {
		t.Error("get workflows --trigger is applied by no server and must stay removed")
	}
}

func TestListFiltersAreRejectedWithAnID(t *testing.T) {
	cases := []struct {
		name string
		cmd  func() *cobra.Command
		args []string
		flag string
	}{
		{"workflow --type", newGetWorkflowsCmd, []string{"wf-1", "--type", "standard"}, "--type"},
		{"workflow --mine", newGetWorkflowsCmd, []string{"wf-1", "--mine"}, "--mine"},
		{"execution --state", newGetWorkflowExecutionsCmd, []string{"ex-1", "--state", "SUCCESS"}, "--state"},
		{"intent --app", newGetIntentsCmd, []string{"app/intent", "--app", "other"}, "--app"},
		{"function --app", newGetFunctionsCmd, []string{"app/fn", "--app", "other"}, "--app"},
		{"workflow --limit", newGetWorkflowsCmd, []string{"wf-1", "--limit", "5"}, "--limit"},
		{"slo --filter", newGetSLOsCmd, []string{"slo-1", "--filter", "x"}, "--filter"},
		{"notification --type", newGetNotificationsCmd, []string{"n-1", "--type", "x"}, "--type"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := runRejected(t, tc.cmd(), tc.args...)
			if err == nil || !strings.Contains(err.Error(), tc.flag+" is a list filter and cannot be combined with an id") {
				t.Fatalf("error = %v, want %s rejected with an id", err, tc.flag)
			}
		})
	}
}

func TestUnusedPositionalArgumentsAreRejected(t *testing.T) {
	cases := []struct {
		name string
		cmd  func() *cobra.Command
		args []string
	}{
		{"find intents <name>", newFindIntentsCmd, []string{"my-intent", "--data", "a=b"}},
		{"get groups <uuid>", newGetGroupsCmd, []string{"some-uuid"}},
		{"get workflows two ids", newGetWorkflowsCmd, []string{"a", "b"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := runRejected(t, tc.cmd(), tc.args...)
			if err == nil || !(strings.Contains(err.Error(), "arg(s)") || strings.Contains(err.Error(), "takes no arguments")) {
				t.Fatalf("error = %v, want an unused-argument error", err)
			}
		})
	}
}

func TestUsageErrorsExitWithTheUsageCode(t *testing.T) {
	err := runRejected(t, newFindIntentsCmd(), "x")
	if code := exitCodeForError(err); code != 2 {
		t.Errorf("stray positional exit code = %d, want 2", code)
	}
	if !strings.Contains(err.Error(), `takes no arguments (got "x")`) {
		t.Errorf("error = %v", err)
	}
	err = fmt.Errorf("template rendering failed: %w", &template.VariableError{Missing: []string{"a"}})
	if code := exitCodeForError(err); code != 2 {
		t.Errorf("template error exit code = %d, want 2", code)
	}
	if d := errorToDetail(context.Background(), err); d.Code != "validation_error" {
		t.Errorf("template error code = %q, want validation_error", d.Code)
	}
}

func TestArgumentConflictsWithInputFlags(t *testing.T) {
	for _, c := range []struct {
		cmd  *cobra.Command
		args []string
	}{
		{newExecFunctionCmd(), []string{"app/fn", "--code", "x"}},
		{newExecFunctionCmd(), []string{"app/fn", "--file", "x.js"}},
	} {
		err := runRejected(t, c.cmd, c.args...)
		if err == nil || !strings.Contains(err.Error(), "cannot be combined with") {
			t.Errorf("%v: error = %v", c.args, err)
		}
	}
}
