package cmd

import (
	"io"
	"slices"
	"strings"
	"testing"

	"github.com/dynatrace-oss/dtctl/pkg/stability"
)

// runGetActions executes a fresh `get actions` command. Every case here must
// fail validation before setup, so none of them needs a config or a server.
func runGetActions(t *testing.T, args ...string) error {
	t.Helper()
	c := newGetActionsCmd()
	c.SetArgs(args)
	c.SetOut(io.Discard)
	c.SetErr(io.Discard)
	c.SilenceUsage = true
	c.SilenceErrors = true
	return c.Execute()
}

func TestGetActions_RejectsUnusableInput(t *testing.T) {
	const id = "example.app:send-thing"

	cases := []struct {
		name string
		args []string
		want string
	}{
		{"--app with an id", []string{id, "--app", "other.app"}, "--app is a list filter and cannot be combined with an action id"},
		{"--name with an id", []string{id, "--name", "jira"}, "--name is a list filter and cannot be combined with an action id"},
		{"--filter with an id", []string{id, "--filter", "app.id = 'x'"}, "--filter is a list filter and cannot be combined with an action id"},
		// An explicitly empty value is still a value the user typed.
		{"empty --app with an id", []string{id, "--app", ""}, "--app is a list filter"},
		{"a second id", []string{id, "example.app:other"}, "accepts at most 1 arg(s), received 2"},
		{"--name over the limit", []string{"--name", strings.Repeat("x", 257)}, "--name is 257 characters, the maximum is 256"},
		{"--filter over the limit", []string{"--filter", strings.Repeat("x", 257)}, "--filter is 257 characters, the maximum is 256"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := runGetActions(t, tc.args...)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %v, want it to contain %q", err, tc.want)
			}
		})
	}
}

func TestValidateGetActionsInput_CountsCharactersNotBytes(t *testing.T) {
	c := newGetActionsCmd()
	// 256 characters, 768 bytes: at the limit, so accepted.
	if err := c.Flags().Set("name", strings.Repeat("日", 256)); err != nil {
		t.Fatal(err)
	}
	if err := validateGetActionsInput(c, nil); err != nil {
		t.Fatalf("256 multi-byte characters were refused: %v", err)
	}
}

// The action resource is one development feature: a single opt-in must grant
// both commands, or the stability floor keeps blocking the second one.
func TestActionsIsOneDevelopmentFeature(t *testing.T) {
	reg := stability.DefaultRegistry()
	paths := reg.Paths(actionsDevelopmentFeature)
	for _, want := range []string{"get actions", "describe action"} {
		if !slices.Contains(paths, want) {
			t.Errorf("feature %q does not gate %q (gates %v)", actionsDevelopmentFeature, want, paths)
		}
	}
	granted := reg.EnabledPaths(map[string]bool{actionsDevelopmentFeature: true})
	for _, want := range []string{"get actions", "describe action"} {
		if !slices.Contains(granted, want) {
			t.Errorf("enabling %q does not grant %q (grants %v)", actionsDevelopmentFeature, want, granted)
		}
	}
}

func TestDevelopmentRowsListEveryCommandOfAFeature(t *testing.T) {
	rows := developmentRows([]string{actionsDevelopmentFeature}, map[string]bool{actionsDevelopmentFeature: true})
	var commands []string
	for _, r := range rows {
		if r.Feature != actionsDevelopmentFeature || !r.Enabled {
			t.Errorf("unexpected row %+v", r)
		}
		commands = append(commands, r.Command)
	}
	slices.Sort(commands)
	if want := []string{"dtctl describe action", "dtctl get actions"}; !slices.Equal(commands, want) {
		t.Errorf("commands = %v, want %v", commands, want)
	}
}
