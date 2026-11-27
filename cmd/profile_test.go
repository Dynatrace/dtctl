package cmd

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

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
	applyProfile(root, nil, false)
	if find(root, "auth", "login").Hidden {
		t.Fatal("nil profile should not hide any command")
	}
}

func TestApplyProfile_MasksAndAllows(t *testing.T) {
	root := newTestTree()
	p := &config.Profile{Name: "query", Commands: []string{"query", "get analyzers"}}
	applyProfile(root, p, false)

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
	applyProfile(root, p, false)

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
	applyProfile(root, p, false)

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

// TestHideMaskedCommandKeepsPersistentFlags: a mask hides a command's own
// flags but not persistent ones, which a still-visible child may use, even
// after they were merged into its flag set.
func TestHideMaskedCommandKeepsPersistentFlags(t *testing.T) {
	root := &cobra.Command{Use: "dtctl"}
	root.PersistentFlags().Bool("global", false, "")
	grp := &cobra.Command{Use: "grp"}
	grp.Flags().Bool("own", false, "")
	grp.PersistentFlags().Bool("scope", false, "")
	grp.AddCommand(&cobra.Command{Use: "leaf", RunE: func(*cobra.Command, []string) error { return nil }})
	root.AddCommand(grp)
	_ = grp.InheritedFlags() // merges --global and --scope into grp.Flags()

	hideMaskedCommand(grp)

	if !grp.Flags().Lookup("own").Hidden {
		t.Error("the masked command's own flag is visible")
	}
	for name, f := range map[string]*pflag.Flag{
		"scope":  grp.PersistentFlags().Lookup("scope"),
		"global": root.PersistentFlags().Lookup("global"),
	} {
		if f.Hidden {
			t.Errorf("persistent flag --%s was hidden", name)
		}
	}
}

// newAncestorTestTree builds a parent that runs on its own (apply, needs -f), a
// group that only refuses a missing subcommand (grp), and a child under each.
// ran records which commands ran.
func newAncestorTestTree(ran map[string]bool) *cobra.Command {
	root := &cobra.Command{Use: "dtctl", SilenceErrors: true, SilenceUsage: true}
	body := func(name string) func(*cobra.Command, []string) error {
		return func(*cobra.Command, []string) error { ran[name] = true; return nil }
	}

	apply := &cobra.Command{Use: "apply -f <file>", Args: cobra.NoArgs, RunE: body("apply")}
	apply.Flags().StringP("file", "f", "", "file")
	if err := apply.MarkFlagRequired("file"); err != nil {
		panic(err)
	}
	apply.AddCommand(&cobra.Command{Use: "extension-config", RunE: body("apply extension-config")})

	grp := &cobra.Command{Use: "grp", RunE: requireSubcommand}
	grp.AddCommand(&cobra.Command{Use: "leaf", RunE: body("grp leaf")})

	root.AddCommand(apply, grp)
	return root
}

// TestApplyProfile_AncestorRunsNoBody: a parent allowed only to reach its child
// stays visible but is blocked, even without its required -f. Its flags and
// arguments are hidden; its help and its child still work.
func TestApplyProfile_AncestorRunsNoBody(t *testing.T) {
	ran := map[string]bool{}
	root := newAncestorTestTree(ran)
	applyProfile(root, &config.Profile{Name: "p", Commands: []string{"apply extension-config", "grp leaf"}}, false)

	apply := find(root, "apply")
	if apply.Hidden {
		t.Error("apply must stay visible to reach apply extension-config")
	}
	if !apply.Flags().Lookup("file").Hidden {
		t.Error("--file of the blocked apply must be hidden")
	}
	if apply.Use != "apply" {
		t.Errorf("Use = %q, want the blocked apply's arguments dropped", apply.Use)
	}

	for _, args := range [][]string{{"apply", "-f", "d.yaml"}, {"apply"}, {"apply", "stray"}} {
		root.SetArgs(args)
		var pe *ProfileError
		if err := root.Execute(); !errors.As(err, &pe) || pe.Command != "apply" || pe.Profile != "p" {
			t.Errorf("%v: got %v, want apply blocked by the profile", args, err)
		}
	}
	if ran["apply"] {
		t.Fatal("the blocked apply ran")
	}

	var help bytes.Buffer
	root.SetOut(&help)
	root.SetArgs([]string{"apply", "--help"})
	if err := root.Execute(); err != nil {
		t.Fatalf("apply --help: %v", err)
	}
	if !strings.Contains(help.String(), "extension-config") || strings.Contains(help.String(), "-f <file>") ||
		strings.Contains(help.String(), "--file") {
		t.Errorf("apply --help should list extension-config and not -f:\n%s", help.String())
	}

	root.SetArgs([]string{"apply", "extension-config"})
	if err := root.Execute(); err != nil || !ran["apply extension-config"] {
		t.Errorf("apply extension-config: err %v, ran %v; want it to run", err, ran["apply extension-config"])
	}
}

// TestApplyProfile_ListedParentAndPureGroupAreUnchanged: a listed parent still
// runs, and a group still refuses a missing subcommand on its own.
func TestApplyProfile_ListedParentAndPureGroupAreUnchanged(t *testing.T) {
	ran := map[string]bool{}
	root := newAncestorTestTree(ran)
	applyProfile(root, &config.Profile{Name: "p", Commands: []string{"apply", "grp leaf"}}, false)

	root.SetArgs([]string{"apply", "-f", "d.yaml"})
	if err := root.Execute(); err != nil || !ran["apply"] {
		t.Errorf("listed apply: err %v, ran %v; want it to run", err, ran["apply"])
	}

	grp := find(root, "grp")
	if !isRefusal(grp) || grp.Hidden {
		t.Error("grp must keep its refusal and stay visible")
	}
	root.SetArgs([]string{"grp"})
	var pe *ProfileError
	if err := root.Execute(); err == nil || errors.As(err, &pe) {
		t.Errorf("grp: got %v, want its own missing-subcommand refusal", err)
	}
}

// TestInstallScopePreflightKeepsRefusal: a group wrapped by the scope
// preflight is still seen as a refusal.
func TestInstallScopePreflightKeepsRefusal(t *testing.T) {
	root := newAncestorTestTree(map[string]bool{})
	installScopePreflight(root)
	if !isRefusal(find(root, "grp")) {
		t.Error("the wrapped grp is no longer seen as a refusal")
	}
	if isRefusal(find(root, "apply")) {
		t.Error("apply has a body and is not a refusal")
	}
}

func TestHelpRequested(t *testing.T) {
	for _, tc := range []struct {
		args []string
		want bool
	}{
		{[]string{"--help"}, true},
		{[]string{"-f", "d.yaml", "-h"}, true},
		{[]string{"-f", "d.yaml"}, false},
		{[]string{"--", "--help"}, false}, // after "--" it is positional
		{nil, false},
	} {
		if got := helpRequested(tc.args); got != tc.want {
			t.Errorf("helpRequested(%v) = %v, want %v", tc.args, got, tc.want)
		}
	}
}
