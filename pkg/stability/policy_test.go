package stability

import (
	"slices"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

func TestParseExceptions(t *testing.T) {
	got, err := ParseExceptions([]string{"ingest", "query --spill", "  get workflows  "})
	if err != nil {
		t.Fatalf("ParseExceptions: %v", err)
	}
	want := []Exception{
		{Command: "ingest"},
		{Command: "query", Flag: "spill"},
		{Command: "get workflows"},
	}
	if len(got) != len(want) {
		t.Fatalf("got %d exceptions (%v), want %d", len(got), got, len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("exception %d = %+v, want %+v", i, got[i], want[i])
		}
	}
}

func TestParseExceptionsRejectsMalformedEntries(t *testing.T) {
	// A typo in an exception would otherwise silently tighten the surface and
	// produce a confusing block much later, far from its cause.
	cases := map[string]string{
		"two flags":         "query --spill --raw",
		"command last":      "query --spill workflows",
		"flag with no path": "--spill",
	}
	for name, entry := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := ParseExceptions([]string{entry}); err == nil {
				t.Errorf("ParseExceptions(%q) succeeded; want an error", entry)
			}
		})
	}
}

func TestPolicyDefaultFloorAdmitsExperimental(t *testing.T) {
	var p Policy // unset floor
	if got := p.EffectiveFloor(); got != Experimental {
		t.Errorf("default floor = %q, want experimental: humans and interactive "+
			"agents should keep getting new (badged) surface", got)
	}
	if !p.AllowsCommand("ingest", Experimental) {
		t.Error("the default floor must admit experimental commands")
	}
	if p.AllowsCommand("account", Development) {
		t.Error("the default floor must not admit development commands")
	}
}

func TestPolicyFloorBlocksAndExceptionsAdmit(t *testing.T) {
	p := Policy{
		Floor: Stable,
		Exceptions: []Exception{
			{Command: "ingest"},
			{Command: "query", Flag: "spill"},
		},
	}

	if p.AllowsCommand("translate", Experimental) {
		t.Error("a stable floor must block an unlisted experimental command")
	}
	if !p.AllowsCommand("ingest", Experimental) {
		t.Error("an exception must admit the command it names")
	}
	if !p.AllowsFlag("query", "spill", Experimental, Experimental) {
		t.Error("a flag exception must admit the flag it names")
	}
	// The narrow grant is the whole point: naming a command must not
	// blanket-admit a flag that declares its *own* weaker contract — there the
	// flag is the risk being accepted, not the command.
	if p.AllowsFlag("ingest", "wait", Experimental, Experimental) {
		t.Error("a command exception must not admit a flag with its own experimental mark")
	}
	// A flag that declares nothing of its own is below the floor only because
	// its command is, so it stands or falls with the command. Otherwise
	// admitting `ingest` would mean enumerating every flag it has, and a flag
	// added later would silently break the caller that did.
	if !p.AllowsFlag("ingest", "wait", Experimental, Undeclared) {
		t.Error("a command exception must admit the flags that only inherit its level")
	}
	// A flag declared stable promises nothing weaker than its command, so it
	// too is below the floor only because of the command. (Lint forbids one
	// under an experimental command; this pins the policy, not the tree.)
	if !p.AllowsFlag("ingest", "wait", Experimental, Stable) {
		t.Error("a command exception must admit a flag declared stable")
	}
	// ... and only for the command it names.
	if p.AllowsFlag("translate", "wait", Experimental, Undeclared) {
		t.Error("inheritance must not admit a flag on a command that has no exception")
	}
	if p.AllowsFlag("query", "raw", Experimental, Experimental) {
		t.Error("a flag exception must not spill onto sibling flags")
	}
}

func TestPolicyRestrictsIsFalseAtTheWeakestFloor(t *testing.T) {
	// A floor that cannot block anything lets the caller skip the tree walk
	// entirely, which is what keeps today's default path free.
	if (Policy{Floor: Development}).Restricts() {
		t.Error("a development floor blocks nothing and must report no restriction")
	}
	if !(Policy{Floor: Stable}).Restricts() {
		t.Error("a stable floor restricts")
	}
}

func TestRegisterDeclaresEvenWhenDisabled(t *testing.T) {
	r := &Registry{features: map[string][]string{}}
	r.Declare("account", "account")

	// A disabled feature must still be nameable: Cobra cannot attach an error
	// to a command that was never added to the tree, so the unknown-command
	// path has nothing else to consult.
	if path, ok := r.Path("account"); !ok || path != "account" {
		t.Errorf("Path(account) = %q,%v; want \"account\",true", path, ok)
	}
	// The dotted config form resolves to the same feature.
	if _, ok := r.Path("development.account"); !ok {
		t.Error("the config-style dotted key must resolve to the same feature")
	}
	if feature, ok := r.FeatureForCommand("account login"); !ok || feature != "account" {
		t.Errorf("FeatureForCommand(account login) = %q,%v; want \"account\",true", feature, ok)
	}
	// Segment matching, not string prefix: an unrelated "accounting" command
	// must not resolve to the "account" feature.
	if _, ok := r.FeatureForCommand("accounting report"); ok {
		t.Error("FeatureForCommand matched a partial segment")
	}
}

func TestRegisterAttachesOnlyWhenEnabled(t *testing.T) {
	parent := &cobra.Command{Use: "dtctl"}
	child := &cobra.Command{Use: "account"}

	if Register(parent, child, "account", nil) {
		t.Error("Register attached a disabled feature")
	}
	if len(parent.Commands()) != 0 {
		t.Error("a disabled development command must not be on the tree at all")
	}
	if Of(child) != Development {
		t.Error("Register must mark the command development-tier regardless")
	}

	if !Register(parent, child, "account", map[string]bool{"all": true}) {
		t.Error("the \"all\" sentinel must enable every registered feature")
	}
	if len(parent.Commands()) != 1 {
		t.Error("an enabled development command must be attached")
	}
}

func TestExceptionStringRoundTrips(t *testing.T) {
	for _, entry := range []string{"ingest", "query --spill", "get workflows --raw"} {
		parsed, err := ParseExceptions([]string{entry})
		if err != nil {
			t.Fatalf("ParseExceptions(%q): %v", entry, err)
		}
		if got := parsed[0].String(); got != strings.TrimSpace(entry) {
			t.Errorf("round trip of %q produced %q", entry, got)
		}
	}
}

func TestRegistryOneFeatureGatesSeveralCommands(t *testing.T) {
	r := &Registry{features: map[string][]string{}}
	r.Declare("actions", "get actions")
	r.Declare("actions", "describe action")
	r.Declare("actions", "get actions") // declaring a path again is a no-op

	if got, want := r.Paths("actions"), []string{"get actions", "describe action"}; !slices.Equal(got, want) {
		t.Errorf("Paths = %v, want %v (declaration order, no duplicates)", got, want)
	}
	if path, ok := r.Path("actions"); !ok || path != "get actions" {
		t.Errorf("Path = %q,%v; want the first declared path", path, ok)
	}
	for _, cmd := range []string{"get actions", "describe action"} {
		if feature, ok := r.FeatureForCommand(cmd); !ok || feature != "actions" {
			t.Errorf("FeatureForCommand(%q) = %q,%v; want \"actions\",true", cmd, feature, ok)
		}
	}
	// The floor exempts every path of an enabled feature, not only one: with a
	// single path per key, `describe action` stayed blocked after opting in.
	if got, want := r.EnabledPaths(map[string]bool{"actions": true}), []string{"describe action", "get actions"}; !slices.Equal(got, want) {
		t.Errorf("EnabledPaths = %v, want %v", got, want)
	}
	if got := r.EnabledPaths(map[string]bool{}); len(got) != 0 {
		t.Errorf("EnabledPaths with nothing enabled = %v", got)
	}
	if r.Paths("missing") != nil {
		t.Error("Paths of an unregistered feature should be nil")
	}
}
