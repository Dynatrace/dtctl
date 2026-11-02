// Package stability_test holds the CI gate for dtctl's stability contract.
//
// It lives outside package cmd on purpose: the manifest must cover the complete
// command surface, and `dtctl serve` is wired in main rather than in cmd
// (pkg/serve imports pkg/engine, which imports cmd). Mirroring main's wiring
// here is the only place both halves of the tree are visible at once.
package stability_test

import (
	"flag"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/Dynatrace/dtctl/cmd"
	"github.com/Dynatrace/dtctl/pkg/serve"
)

// update mirrors the -update switch the golden tests use, so one flag
// regenerates every checked-in artifact.
var update = flag.Bool("update", false, "update the checked-in stability manifest")

// manifestPath is the checked-in stability manifest, relative to this package.
const manifestPath = "../../docs/STABILITY.md"

func TestMain(m *testing.M) {
	// Mirror main's wiring so the tree under test is the whole surface a
	// released binary can expose. Declaring serve does not register it — that
	// is the registration stage's decision, which the generator overrides.
	cmd.AddDevelopmentCommand(serve.NewCommand(), serve.DevelopmentFeature)
	os.Exit(m.Run())
}

// TestManifestIsCurrent is the gate that makes the stability contract real
// rather than aspirational.
//
// The manifest is generated from the live command tree and checked in, so any
// change to what dtctl promises — a new experimental flag, a promotion to
// stable, and above all the *removal* of something stable — shows up as a
// reviewable diff and a failing build. Without this, "stable" would be a claim
// in a document that nothing enforces.
func TestManifestIsCurrent(t *testing.T) {
	got := cmd.StabilityManifest()

	if *update {
		if err := os.WriteFile(filepath.Clean(manifestPath), []byte(got), 0o644); err != nil {
			t.Fatalf("writing %s: %v", manifestPath, err)
		}
		t.Logf("updated %s", manifestPath)
		return
	}

	want, err := os.ReadFile(filepath.Clean(manifestPath))
	if err != nil {
		t.Fatalf("reading %s: %v\nRun 'make stability-manifest' to create it.", manifestPath, err)
	}
	if diff := firstDiff(strings.ReplaceAll(string(want), "\r\n", "\n"), got); diff != "" {
		t.Errorf("docs/STABILITY.md is out of date:\n%s\n\n"+
			"A command or flag's stability contract changed. If that was intended, "+
			"regenerate with 'make stability-manifest' and review the diff — "+
			"especially any line that disappeared, which is a broken promise.", diff)
	}
}

// TestDeclarationsAreConsistent reports declarations that are internally
// inconsistent — an experimental command with no since-version, a development
// command with no opt-in key, a flag claiming a stronger promise than the
// command it hangs off. These are author errors, so they fail the build rather
// than degrade at runtime.
func TestDeclarationsAreConsistent(t *testing.T) {
	for _, problem := range cmd.StabilityLint() {
		t.Errorf("stability declaration: %v", problem)
	}
}

// TestEveryCommandDeclaresItsTier is the gate behind "stable is explicit, not
// implicit".
//
// Stable is the strongest promise dtctl makes -- additive-only, removal only
// through a deprecation cycle -- and it used to be what a command got for
// saying nothing. That made the default promise the one nobody chose: a
// command shipped an additive-only contract because its author had not thought
// about the question, and by the time anyone noticed, callers had already built
// on it.
//
// So the tier is now written down at every command, and this test is what makes
// "written down" mean something. An undeclared command resolves to
// experimental at runtime (session.FallbackStabilityLevel), which fails safe
// but is nobody's intent either; the fix is a declaration, not a fallback.
func TestEveryCommandDeclaresItsTier(t *testing.T) {
	undeclared := cmd.StabilityUndeclaredCommands()
	if len(undeclared) == 0 {
		return
	}
	t.Errorf("%d command(s) declare no stability tier: %s\n\n"+
		"Add stability.MarkStable(cmd) where the command is built if its "+
		"invocation and output contract are additive-only from now on, or "+
		"stability.Mark(cmd, stability.Experimental, \"<version>\") if not. "+
		"Stable is never implied -- see AGENTS.md \"Stability Tiers\".",
		len(undeclared), strings.Join(undeclared, ", "))
}

// firstDiff returns a short description of the first differing line, or "" when
// the two strings match. A whole-file diff of a 250-command manifest is
// unreadable in test output; the first divergence is what a maintainer needs.
func firstDiff(want, got string) string {
	if want == got {
		return ""
	}
	wantLines := strings.Split(want, "\n")
	gotLines := strings.Split(got, "\n")
	for i := 0; i < len(wantLines) || i < len(gotLines); i++ {
		w, g := "", ""
		if i < len(wantLines) {
			w = wantLines[i]
		}
		if i < len(gotLines) {
			g = gotLines[i]
		}
		if w != g {
			return "line " + strconv.Itoa(i+1) +
				":\n  checked in: " + w + "\n  generated:  " + g
		}
	}
	return "files differ in trailing content"
}
