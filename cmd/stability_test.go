package cmd

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/dynatrace-oss/dtctl/pkg/config"
	"github.com/dynatrace-oss/dtctl/pkg/stability"
)

// newFloorTree builds a throwaway tree with one stable command carrying an
// experimental flag, and one experimental command with a subcommand.
func newFloorTree() *cobra.Command {
	root := &cobra.Command{Use: "dtctl"}

	ingest := &cobra.Command{
		Use:  "ingest",
		Args: cobra.ExactArgs(1),
		RunE: func(*cobra.Command, []string) error { return nil },
	}
	stability.Mark(ingest, stability.Experimental, "0.38.0")
	logs := &cobra.Command{Use: "logs", RunE: func(*cobra.Command, []string) error { return nil }}
	ingest.AddCommand(logs)
	root.AddCommand(ingest)

	query := &cobra.Command{
		Use:  "query",
		Args: cobra.ExactArgs(1),
		RunE: func(*cobra.Command, []string) error { return nil },
	}
	stability.MarkStable(query)
	query.Flags().Bool("spill", false, "spill results to a file")
	query.Flags().String("timeframe", "", "query timeframe")
	stability.MarkFlag(query, "spill", stability.Experimental, "0.38.0")
	root.AddCommand(query)

	return root
}

// treeCommand returns a named direct child of a throwaway tree.
func treeCommand(t *testing.T, root *cobra.Command, name string) *cobra.Command {
	t.Helper()
	for _, sub := range root.Commands() {
		if sub.Name() == name {
			return sub
		}
	}
	t.Fatalf("command %q not found on the test tree", name)
	return nil
}

// runTree executes a command line against a throwaway tree and returns the
// error, with output swallowed.
func runTree(t *testing.T, root *cobra.Command, args ...string) error {
	t.Helper()
	root.SetOut(&strings.Builder{})
	root.SetErr(&strings.Builder{})
	root.SetArgs(args)
	root.SilenceUsage = true
	root.SilenceErrors = true
	return root.Execute()
}

func TestStabilityFloorIsANoOpByDefault(t *testing.T) {
	root := newFloorTree()
	applyStabilityFloor(root, stability.Policy{})

	// The default floor admits experimental surface, so nothing changes for a
	// caller who has not configured anything. This is the compatibility
	// guarantee for every existing user.
	if err := runTree(t, root, "ingest", "logs"); err != nil {
		t.Errorf("default floor blocked an experimental command: %v", err)
	}
	if treeCommand(t, root, "ingest").Hidden {
		t.Error("default floor hid a command")
	}
}

func TestStabilityFloorBlocksBelowFloorCommands(t *testing.T) {
	root := newFloorTree()
	applyStabilityFloor(root, stability.Policy{Floor: stability.Stable})

	err := runTree(t, root, "ingest", "x")
	var blocked *StabilityError
	if !errors.As(err, &blocked) {
		t.Fatalf("running an experimental command under a stable floor returned %v, want *StabilityError", err)
	}
	if blocked.Command != "ingest" || blocked.Level != stability.Experimental {
		t.Errorf("unexpected block: %+v", blocked)
	}
	// The narrow opt-in comes first: lowering the floor would grant the entire
	// below-floor surface, which is almost never what the caller wanted.
	if !strings.Contains(blocked.Suggestions()[0], "stability-exceptions") {
		t.Errorf("the narrow opt-in should be suggested first, got %q", blocked.Suggestions()[0])
	}
}

func TestStabilityFloorDoesNotLeakArgShape(t *testing.T) {
	root := newFloorTree()
	applyStabilityFloor(root, stability.Policy{Floor: stability.Stable})

	// `ingest` declares ExactArgs(1). Without neutralizing arg validation and
	// flag parsing, Cobra would answer with "accepts 1 arg(s)" or "unknown
	// flag", either of which confirms the command exists and describes its
	// shape — the exact thing the block is meant not to do.
	for _, args := range [][]string{{"ingest"}, {"ingest", "--nope"}, {"ingest", "a", "b"}} {
		err := runTree(t, root, args...)
		var blocked *StabilityError
		if !errors.As(err, &blocked) {
			t.Errorf("dtctl %s returned %v, want *StabilityError", strings.Join(args, " "), err)
		}
	}
}

func TestStabilityFloorBlocksInheritedSubcommands(t *testing.T) {
	root := newFloorTree()
	applyStabilityFloor(root, stability.Policy{Floor: stability.Stable})

	// `ingest logs` carries no annotation of its own, but it is only reachable
	// through an experimental parent, so it offers no stronger promise.
	err := runTree(t, root, "ingest", "logs")
	var blocked *StabilityError
	if !errors.As(err, &blocked) {
		t.Fatalf("an unannotated child of an experimental verb was allowed: %v", err)
	}
}

func TestStabilityExceptionAdmitsOneCommand(t *testing.T) {
	root := newFloorTree()
	applyStabilityFloor(root, stability.Policy{
		Floor:      stability.Stable,
		Exceptions: []stability.Exception{{Command: "ingest"}},
	})

	if err := runTree(t, root, "ingest", "x"); err != nil {
		t.Errorf("the named exception was still blocked: %v", err)
	}
	// The grant is per target, not per subtree: it must not cascade to
	// children, which is what keeps the exception a contract rather than a
	// topic. (Prefix-based, subtree-inclusive matching is right for profiles
	// and wrong here.)
	err := runTree(t, root, "ingest", "logs")
	var blocked *StabilityError
	if !errors.As(err, &blocked) {
		t.Errorf("a command exception cascaded to a subcommand: %v", err)
	}
}

func TestStabilityFloorBlocksBelowFloorFlags(t *testing.T) {
	root := newFloorTree()
	applyStabilityFloor(root, stability.Policy{Floor: stability.Stable})

	// The command itself is stable, so it must still work.
	if err := runTree(t, root, "query", "fetch logs"); err != nil {
		t.Fatalf("a stable command was blocked because one of its flags is experimental: %v", err)
	}
	if err := runTree(t, root, "query", "fetch logs", "--timeframe", "-1h"); err != nil {
		t.Fatalf("a stable flag was blocked: %v", err)
	}

	// Using the experimental flag is what is refused.
	err := runTree(t, root, "query", "fetch logs", "--spill")
	var blocked *StabilityError
	if !errors.As(err, &blocked) {
		t.Fatalf("an experimental flag was accepted under a stable floor: %v", err)
	}
	if blocked.Flag != "spill" || blocked.Command != "query" {
		t.Errorf("unexpected flag block: %+v", blocked)
	}
	if !strings.Contains(blocked.Headline(), "--spill") {
		t.Errorf("the headline should name the flag, got %q", blocked.Headline())
	}

	// Hidden, not removed: removing it would make --spill an "unknown flag",
	// indistinguishable from a typo and a worse answer than naming the
	// contract that excludes it.
	if f := treeCommand(t, root, "query").Flags().Lookup("spill"); f == nil || !f.Hidden {
		t.Error("a below-floor flag should be hidden but still parseable")
	}
}

func TestStabilityFlagExceptionAdmitsOnlyThatFlag(t *testing.T) {
	root := newFloorTree()
	query := treeCommand(t, root, "query")
	query.Flags().Bool("raw", false, "")
	stability.MarkFlag(query, "raw", stability.Experimental, "0.38.0")

	applyStabilityFloor(root, stability.Policy{
		Floor:      stability.Stable,
		Exceptions: []stability.Exception{{Command: "query", Flag: "spill"}},
	})

	if err := runTree(t, root, "query", "fetch logs", "--spill"); err != nil {
		t.Errorf("the named flag exception was still blocked: %v", err)
	}
	err := runTree(t, root, "query", "fetch logs", "--raw")
	var blocked *StabilityError
	if !errors.As(err, &blocked) {
		t.Errorf("a flag exception spilled onto a sibling flag: %v", err)
	}
}

func TestStabilityExceptionCannotResurrectADevelopmentCommand(t *testing.T) {
	// The pipeline invariant: registration runs before the floor, so no
	// exception can re-add what stage 1 never registered. Were this to fail, a
	// config entry would become a way to reach unfinished surface.
	root := &cobra.Command{Use: "dtctl"}
	// A sibling so the tree has subcommands at all; otherwise cobra prints the
	// root help for any argument instead of reporting an unknown command.
	root.AddCommand(&cobra.Command{Use: "query", RunE: func(*cobra.Command, []string) error { return nil }})
	gated := &cobra.Command{Use: "account", RunE: func(*cobra.Command, []string) error { return nil }}
	stability.MarkDevelopment(gated, "account")

	applyStabilityFloor(root, stability.Policy{
		Floor:      stability.Stable,
		Exceptions: []stability.Exception{{Command: "account"}},
	})

	err := runTree(t, root, "account")
	if err == nil {
		t.Fatal("an unregistered development command was reachable")
	}
	var blocked *StabilityError
	if errors.As(err, &blocked) {
		t.Fatalf("an unregistered command produced a floor block (%v); it should be "+
			"an ordinary unknown command", blocked)
	}
	if !strings.Contains(err.Error(), "unknown command") {
		t.Errorf("got %v, want an unknown-command error", err)
	}
}

func TestStabilityBadgesStateTheGuarantee(t *testing.T) {
	root := newFloorTree()
	applyStabilityBadges(root)

	ingest := treeCommand(t, root, "ingest")
	if !strings.HasPrefix(ingest.Short, "[Experimental]") {
		t.Errorf("ingest.Short = %q, want an [Experimental] prefix", ingest.Short)
	}
	// The guarantee is spelled out rather than left to be inferred from the
	// tier's name: AIP-181 defines "experimental" as the weakest level of all.
	if !strings.Contains(ingest.Long, "not covered by dtctl's stability guarantees") {
		t.Errorf("ingest.Long does not state the guarantee: %q", ingest.Long)
	}

	query := treeCommand(t, root, "query")
	if strings.Contains(query.Short, "[") {
		t.Errorf("a stable command was badged: %q", query.Short)
	}
	// A flag may be weaker than its command, so the command's (absent) badge
	// does not cover it.
	if usage := query.Flags().Lookup("spill").Usage; !strings.HasPrefix(usage, "[Experimental]") {
		t.Errorf("--spill usage = %q, want an [Experimental] prefix", usage)
	}
	if usage := query.Flags().Lookup("timeframe").Usage; strings.Contains(usage, "[") {
		t.Errorf("a stable flag was badged: %q", usage)
	}
}

func TestDevelopmentRegistrationIsIdempotentAndReversible(t *testing.T) {
	before := len(rootCmd.Commands())

	applyDevelopmentRegistration(map[string]bool{config.DevelopmentAll: true})
	enabled := len(rootCmd.Commands())
	if enabled <= before {
		t.Fatalf("enabling every development feature added no command (%d to %d)", before, enabled)
	}

	// Re-applying must not duplicate: an embedded caller runs this once per
	// request against the same process-wide tree.
	applyDevelopmentRegistration(map[string]bool{config.DevelopmentAll: true})
	if got := len(rootCmd.Commands()); got != enabled {
		t.Errorf("re-registering duplicated commands (%d to %d)", enabled, got)
	}

	applyDevelopmentRegistration(nil)
	if got := len(rootCmd.Commands()); got != before {
		t.Errorf("disabling did not detach (%d, want %d): one request's opt-in "+
			"must not leak into the next", got, before)
	}
}

func TestDevelopmentSignpostingRequiresDemonstratedKnowledge(t *testing.T) {
	errStr := "unknown command \"account\" for \"dtctl\""

	// The default is silence. A caller who has never heard of the mechanism
	// must not learn that unfinished surface exists, which is the point of
	// gating by registration rather than by hiding.
	if got := developmentHint(errStr, false); got != nil {
		t.Errorf("signposted to a caller who had not opted in: %v", got)
	}

	var devErr *DevelopmentError
	if err := developmentHint(errStr, true); !errors.As(err, &devErr) {
		t.Fatalf("developmentHint = %v, want *DevelopmentError", err)
	}
	if devErr.Feature != accountDevelopmentFeature {
		t.Errorf("feature = %q, want %q", devErr.Feature, accountDevelopmentFeature)
	}
	if !strings.Contains(devErr.Suggestions()[0], "config set development.account on") {
		t.Errorf("suggestions do not name the opt-in: %v", devErr.Suggestions())
	}

	// An unrelated typo stays a typo.
	if got := developmentHint("unknown command \"quer\" for \"dtctl\"", true); got != nil {
		t.Errorf("developmentHint answered for an unrelated command: %v", got)
	}
}

func TestDevelopmentSignpostingIsOffInAgentMode(t *testing.T) {
	prev := agentMode(context.Background())
	gFlags.agentMode = true
	t.Cleanup(func() { gFlags.agentMode = prev })

	cfg := config.NewConfig()
	cfg.SetDevelopmentFeature("account", true)

	// An absolute override, not a heuristic. An agent cannot weigh "unfinished,
	// may disappear" against its task, and naming an opt-in in a
	// machine-readable envelope invites it to take the opt-in.
	if developmentSignposting(context.Background(), cfg) {
		t.Error("agent mode must never signpost a development feature")
	}
}

func TestStabilityErrorMapsToItsOwnCode(t *testing.T) {
	// A third code alongside profile_blocked and safety_blocked: the three are
	// different axes and a caller resolves them differently.
	detail := errorToDetail(context.Background(), &StabilityError{
		Command: "ingest", Level: stability.Experimental, Floor: stability.Stable,
	})
	if detail.Code != "stability_blocked" {
		t.Errorf("code = %q, want stability_blocked", detail.Code)
	}
	if len(detail.Suggestions) == 0 {
		t.Error("a structured block with no remediation is a dead end")
	}

	devDetail := errorToDetail(context.Background(), &DevelopmentError{Command: "account", Feature: "account"})
	if devDetail.Code != "development_disabled" {
		t.Errorf("code = %q, want development_disabled", devDetail.Code)
	}
}

func TestLegacyExperimentalEnvVarsStillEnableTheirFeature(t *testing.T) {
	// Honored as deprecated aliases so an existing script or deployment does
	// not break on upgrade.
	t.Setenv("DTCTL_EXPERIMENTAL_ACCOUNT", "1")
	enabled := legacyDevelopmentFeatures(context.Background(), nil)
	if !enabled[accountDevelopmentFeature] {
		t.Error("DTCTL_EXPERIMENTAL_ACCOUNT no longer enables the account feature")
	}

	// An unset legacy variable is silence, not an explicit off, so it must not
	// override an opt-in expressed the current way.
	t.Setenv("DTCTL_EXPERIMENTAL_ACCOUNT", "")
	enabled = legacyDevelopmentFeatures(context.Background(), map[string]bool{accountDevelopmentFeature: true})
	if !enabled[accountDevelopmentFeature] {
		t.Error("an unset legacy variable turned off a current opt-in")
	}
}

// TestEnvironmentOptInSurvivesAMissingConfig pins the case CI caught and a
// developer machine cannot: with no loadable config file at all — a fresh
// install, a container, a CI runner — the environment must still be able to
// switch a development feature on and to set the floor.
//
// Both are read through a Config, so a resolver that gave up when the file was
// missing silently ignored DTCTL_DEVELOPMENT and DTCTL_MIN_STABILITY while the
// deprecated DTCTL_EXPERIMENTAL_* variables (plain os.Getenv) kept working —
// making the spelling we want to retire the only reliable one.
func TestEnvironmentOptInSurvivesAMissingConfig(t *testing.T) {
	// Signposting is off in agent mode unconditionally, so this test has to
	// state which mode it is asserting about rather than inherit whatever the
	// previously-run test left in the package variable.
	prev := agentMode(context.Background())
	gFlags.agentMode = false
	t.Cleanup(func() { gFlags.agentMode = prev })

	t.Setenv(config.EnvConfig, filepath.Join(t.TempDir(), "absent.yaml"))

	t.Setenv(config.DevelopmentEnvVar, "serve")
	enabled, signpost := resolveDevelopmentFeatures(context.Background(), nil)
	if !stability.Enabled("serve", enabled) {
		t.Error("an environment opt-in was dropped because no config file exists")
	}
	if !signpost {
		t.Error("a caller who set DTCTL_DEVELOPMENT was not treated as knowing the mechanism")
	}

	t.Setenv(config.MinStabilityEnvVar, "stable")
	policy, err := resolveStabilityPolicy(context.Background(), nil, enabled)
	if err != nil {
		t.Fatalf("resolveStabilityPolicy: %v", err)
	}
	if policy.EffectiveFloor() != stability.Stable {
		t.Errorf("floor = %q, want stable", policy.EffectiveFloor())
	}
}

// TestStabilityFloorBlocksBelowFloorGlobalFlags covers a flag declared once on
// the root and usable on every command.
//
// This is the case the floor used to miss entirely. blockBelowFloorFlags walked
// only visitOwnFlags, which skips inherited persistent flags, so a marked
// global flag was badged in help and listed in the manifest while every
// subcommand went on accepting it — a promise weaker on paper than in practice.
func TestStabilityFloorBlocksBelowFloorGlobalFlags(t *testing.T) {
	root := newFloorTree()
	root.PersistentFlags().Bool("spill-global", false, "")
	root.PersistentFlags().String("jq", "", "")
	stability.MarkFlag(root, "spill-global", stability.Experimental, "0.38.0")

	applyStabilityFloor(root, stability.Policy{Floor: stability.Stable})

	// A stable global flag is untouched on a stable command.
	if err := runTree(t, root, "query", "fetch logs", "--jq", ".records"); err != nil {
		t.Fatalf("a stable global flag was blocked: %v", err)
	}

	// The experimental global flag is refused on a subcommand, which is the
	// only place a caller can type it.
	err := runTree(t, root, "query", "fetch logs", "--spill-global")
	var blocked *StabilityError
	if !errors.As(err, &blocked) {
		t.Fatalf("an experimental global flag was accepted under a stable floor: %v", err)
	}
	if blocked.Flag != "spill-global" || blocked.Command != "query" {
		t.Errorf("the block should name the flag and the command a caller typed, got %+v", blocked)
	}
}

// TestStabilityExceptionAdmitsAGlobalFlag checks a global flag can be admitted
// one command at a time, so accepting `--spill-global` on `query` does not
// hand it to every other command as well.
func TestStabilityExceptionAdmitsAGlobalFlag(t *testing.T) {
	root := newFloorTree()
	root.PersistentFlags().Bool("spill-global", false, "")
	stability.MarkFlag(root, "spill-global", stability.Experimental, "0.38.0")

	applyStabilityFloor(root, stability.Policy{
		Floor:      stability.Stable,
		Exceptions: []stability.Exception{{Command: "query", Flag: "spill-global"}},
	})

	if err := runTree(t, root, "query", "fetch logs", "--spill-global"); err != nil {
		t.Errorf("the named global-flag exception was still blocked: %v", err)
	}

	// `logs` did not get an exception, so it still refuses the flag.
	err := runTree(t, root, "ingest", "logs", "--spill-global")
	var blocked *StabilityError
	if !errors.As(err, &blocked) {
		t.Errorf("a global-flag exception on one command spilled onto another: %v", err)
	}
}

// TestStabilityFloorBlocksAFlagShadowingAGlobal covers a flag a command
// redefines under a name the root already uses persistently.
//
// Such a flag fell through both halves of the walk: visitOwnFlags skipped it
// because an ancestor declares the name, and visitInheritedFlags skipped the
// ancestor's flag because the command shadows it. Nobody checked the one cobra
// actually binds, so an experimental flag passed a stable floor untouched.
func TestStabilityFloorBlocksAFlagShadowingAGlobal(t *testing.T) {
	root := newFloorTree()
	root.PersistentFlags().String("timeframe", "", "global timeframe")
	stability.MarkFlag(treeCommand(t, root, "query"), "timeframe", stability.Experimental, "0.38.0")

	applyStabilityFloor(root, stability.Policy{Floor: stability.Stable})

	err := runTree(t, root, "query", "fetch logs", "--timeframe", "-2h")
	var blocked *StabilityError
	if !errors.As(err, &blocked) {
		t.Fatalf("an experimental flag shadowing a global was accepted under a stable floor: %v", err)
	}
	if blocked.Flag != "timeframe" || blocked.Command != "query" {
		t.Errorf("the block should name the flag and the command a caller typed, got %+v", blocked)
	}
}
