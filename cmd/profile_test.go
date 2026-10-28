package cmd

import (
	"errors"
	"testing"

	"github.com/spf13/cobra"

	"github.com/Dynatrace/dtctl/pkg/config"
)

// newTestTree builds a small command tree mirroring dtctl's shape for filter tests.
func newTestTree() *cobra.Command {
	root := &cobra.Command{Use: "dtctl"}

	query := &cobra.Command{Use: "query", RunE: func(*cobra.Command, []string) error { return nil }}

	get := &cobra.Command{Use: "get"}
	get.AddCommand(&cobra.Command{Use: "analyzers", RunE: func(*cobra.Command, []string) error { return nil }})
	get.AddCommand(&cobra.Command{Use: "workflows", RunE: func(*cobra.Command, []string) error { return nil }})

	auth := &cobra.Command{Use: "auth"}
	auth.AddCommand(&cobra.Command{Use: "login", RunE: func(*cobra.Command, []string) error { return nil }})

	commandsC := &cobra.Command{Use: "commands", RunE: func(*cobra.Command, []string) error { return nil }}
	commandsC.AddCommand(&cobra.Command{Use: "howto", RunE: func(*cobra.Command, []string) error { return nil }})

	root.AddCommand(query, get, auth, commandsC)
	return root
}

func find(root *cobra.Command, path ...string) *cobra.Command {
	cmd := root
	for _, name := range path {
		var next *cobra.Command
		for _, sub := range cmd.Commands() {
			if sub.Name() == name {
				next = sub
				break
			}
		}
		if next == nil {
			return nil
		}
		cmd = next
	}
	return cmd
}

func TestApplyProfile_Nil_NoOp(t *testing.T) {
	root := newTestTree()
	applyProfile(root, nil)
	if find(root, "auth", "login").Hidden {
		t.Fatal("nil profile should not hide any command")
	}
}

func TestApplyProfile_MasksAndAllows(t *testing.T) {
	root := newTestTree()
	p := &config.Profile{Name: "query", Commands: []string{"query", "get analyzers"}}
	applyProfile(root, p)

	// Allowed: query, its subtree parent chain, and the specific get child.
	if find(root, "query").Hidden {
		t.Error("query should be visible")
	}
	if find(root, "get").Hidden {
		t.Error("get (ancestor of allowed child) should stay visible")
	}
	if find(root, "get", "analyzers").Hidden {
		t.Error("get analyzers should be visible")
	}

	// Always-available survives.
	if find(root, "commands").Hidden || find(root, "commands", "howto").Hidden {
		t.Error("commands/howto must always be available")
	}

	// Masked: sibling child + unrelated verb.
	if !find(root, "get", "workflows").Hidden {
		t.Error("get workflows should be masked")
	}
	if !find(root, "auth").Hidden || !find(root, "auth", "login").Hidden {
		t.Error("auth subtree should be masked")
	}
}

func TestApplyProfile_GuardBlocksInvocation(t *testing.T) {
	root := newTestTree()
	p := &config.Profile{Name: "query", Commands: []string{"query"}}
	applyProfile(root, p)

	blocked := find(root, "auth", "login")
	err := blocked.RunE(blocked, nil)
	var pe *ProfileError
	if !errors.As(err, &pe) {
		t.Fatalf("expected ProfileError, got %v", err)
	}
	if pe.Command != "auth login" || pe.Profile != "query" {
		t.Fatalf("unexpected ProfileError: %+v", pe)
	}

	// Allowed command keeps its original RunE (no error).
	allowed := find(root, "query")
	if err := allowed.RunE(allowed, nil); err != nil {
		t.Fatalf("allowed command should run: %v", err)
	}
}

// TestApplyProfile_GuardWinsOverArgValidation verifies that masking a command
// with an Args validator (or an unknown flag) still yields the ProfileError,
// not Cobra's generic "accepts N arg(s)" / "unknown flag" error. Cobra validates
// args and parses flags before RunE, so applyProfile must neutralize both on the
// masked command for the guard to be the only observable outcome.
func TestApplyProfile_GuardWinsOverArgValidation(t *testing.T) {
	root := newTestTree()
	// A masked command whose arg validator would otherwise fire before the guard.
	del := &cobra.Command{Use: "delete", Args: cobra.ExactArgs(1), RunE: func(*cobra.Command, []string) error { return nil }}
	root.AddCommand(del)

	p := &config.Profile{Name: "query", Commands: []string{"query"}}
	applyProfile(root, p)

	// Wrong arg count *and* an unknown flag — both would pre-empt RunE if not
	// neutralized. The profile block must still win.
	root.SetArgs([]string{"delete", "--bogus"})
	root.SilenceErrors = true
	root.SilenceUsage = true

	err := root.Execute()
	var pe *ProfileError
	if !errors.As(err, &pe) {
		t.Fatalf("expected ProfileError to win over arg/flag validation, got %v", err)
	}
	if pe.Command != "delete" || pe.Profile != "query" {
		t.Fatalf("unexpected ProfileError: %+v", pe)
	}
}

func TestExtractContextOverride(t *testing.T) {
	tests := []struct {
		args []string
		want string
	}{
		{[]string{"get", "workflows"}, ""},
		{[]string{"--context", "prod", "query"}, "prod"},
		{[]string{"--context=prod", "query"}, "prod"},
		{[]string{"query", "--context", "staging"}, "staging"},
		{[]string{"--context"}, ""},                        // dangling flag, no value
		{[]string{"query", "--", "--context", "prod"}, ""}, // after "--" it's positional
	}
	for _, tt := range tests {
		if got := extractContextOverride(tt.args); got != tt.want {
			t.Errorf("extractContextOverride(%v) = %q, want %q", tt.args, got, tt.want)
		}
	}
}
