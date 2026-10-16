package cmd

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/dynatrace-oss/dtctl/pkg/stability"
)

// newDeprecatedTree builds a tree with one deprecated command, one stable
// command carrying a deprecated flag, and one of each with nothing scheduled.
//
// Synthetic because the real tree has no deprecated command at all today and
// exactly one deprecated flag (`exec workflow --params`). A fixture that
// depended on that would start passing vacuously the moment the flag is
// removed, which is precisely the release this mode exists to prepare for.
func newDeprecatedTree() *cobra.Command {
	root := &cobra.Command{Use: "dtctl"}

	gone := &cobra.Command{
		Use:  "translate",
		Args: cobra.ExactArgs(1),
		RunE: func(*cobra.Command, []string) error { return nil },
	}
	stability.MarkStable(gone)
	stability.Deprecate(gone, stability.Deprecation{
		Since: "0.38.0", RemoveIn: "1.0.0", Replacement: "query",
	})
	root.AddCommand(gone)

	query := &cobra.Command{
		Use:  "query",
		Args: cobra.ArbitraryArgs,
		RunE: func(*cobra.Command, []string) error { return nil },
	}
	stability.MarkStable(query)
	query.Flags().String("params", "", "legacy execution metadata")
	query.Flags().String("input", "", "workflow input")
	_ = query.Flags().MarkDeprecated("params", "use --input instead.")
	root.AddCommand(query)

	return root
}

func TestNoDeprecatedIsOffUntilAskedFor(t *testing.T) {
	root := newDeprecatedTree()

	// The mode is opt-in, so the tree is untouched for every caller who has not
	// asked to be shown the post-removal world. A deprecated command keeps
	// working exactly as documented until it is actually removed.
	if err := runTree(t, root, "translate", "x"); err != nil {
		t.Errorf("a deprecated command failed without the mode: %v", err)
	}
	if err := runTree(t, root, "query", "--params", "x"); err != nil {
		t.Errorf("a deprecated flag failed without the mode: %v", err)
	}
	if treeCommand(t, root, "translate").Hidden {
		t.Error("a deprecated command was hidden without the mode")
	}
}

func TestNoDeprecatedRefusesADeprecatedCommand(t *testing.T) {
	root := newDeprecatedTree()
	applyNoDeprecated(root)

	err := runTree(t, root, "translate", "x")
	var deprecated *DeprecatedError
	if !errors.As(err, &deprecated) {
		t.Fatalf("running a deprecated command returned %v, want a DeprecatedError", err)
	}
	if deprecated.Command != "translate" || deprecated.Flag != "" {
		t.Errorf("error targets %+v, want the command itself", deprecated)
	}
	// The note is the same wording help and the manifest use, so a caller who
	// has read either recognizes it — and it is the thing that says what to do.
	for _, want := range []string{"1.0.0", "query"} {
		if !strings.Contains(deprecated.Headline(), want) {
			t.Errorf("headline %q does not mention %q", deprecated.Headline(), want)
		}
	}
	if !treeCommand(t, root, "translate").Hidden {
		t.Error("a refused command stayed in help")
	}

	// A deprecated command masked with arbitrary args, so the refusal survives
	// the arg count a caller happens to pass — otherwise `translate` alone
	// would answer "accepts 1 arg(s)" and leak the shape of surface this mode
	// has removed.
	if !errors.As(runTree(t, root, "translate"), &deprecated) {
		t.Error("the refusal depended on the argument count")
	}
}

func TestNoDeprecatedRefusesOnlyAFlagThatWasUsed(t *testing.T) {
	root := newDeprecatedTree()
	applyNoDeprecated(root)

	// Hiding it from help is the whole answer for a flag nobody passed:
	// refusing the command over a flag it merely *has* would refuse most of the
	// CLI, and says nothing about what this caller depends on.
	if err := runTree(t, root, "query", "--input", "{}"); err != nil {
		t.Errorf("an unrelated invocation was refused: %v", err)
	}

	err := runTree(t, root, "query", "--params", "x")
	var deprecated *DeprecatedError
	if !errors.As(err, &deprecated) {
		t.Fatalf("using a deprecated flag returned %v, want a DeprecatedError", err)
	}
	if deprecated.Flag != "params" || deprecated.Command != "query" {
		t.Errorf("error targets %+v, want query --params", deprecated)
	}

	// Still parseable, just refused. Removing the flag outright would make it
	// an "unknown flag" — indistinguishable from a typo, and a worse answer
	// than naming the removal that excludes it.
	if treeCommand(t, root, "query").Flags().Lookup("params") == nil {
		t.Error("the deprecated flag was removed rather than refused")
	}
}

func TestDeprecatedErrorIsNotAStabilityError(t *testing.T) {
	root := newDeprecatedTree()
	applyNoDeprecated(root)
	err := runTree(t, root, "translate", "x")

	// The two are different problems with different fixes. A stability block
	// says the contract is too weak for this deployment, and a lower floor or
	// an exception resolves it. This says the surface has a removal date, and
	// only a migration resolves it — no floor and no exception will bring it
	// back.
	var stabilityErr *StabilityError
	if errors.As(err, &stabilityErr) {
		t.Error("a deprecation was reported as a stability block; the caller's " +
			"contract has not been weakened, it has an expiry date")
	}

	detail := errorToDetail(context.Background(), err)
	if detail == nil || detail.Code != "deprecated_surface" {
		t.Fatalf("agent-mode error code = %+v, want deprecated_surface", detail)
	}
	if exitCodeForError(err) == 0 {
		t.Error("a refused invocation exited 0")
	}
	// The replacement comes first and the escape hatch last: the caller set the
	// variable precisely to be told this, so a way to silence the finding is
	// not the useful answer.
	sugg := deprecatedSuggestions(t, err)
	if len(sugg) < 2 || !strings.Contains(sugg[len(sugg)-1], "DTCTL_NO_DEPRECATED") {
		t.Errorf("suggestions = %v; want the unset hint last", sugg)
	}
}

func deprecatedSuggestions(t *testing.T, err error) []string {
	t.Helper()
	var deprecated *DeprecatedError
	if !errors.As(err, &deprecated) {
		t.Fatalf("not a DeprecatedError: %v", err)
	}
	return deprecated.Suggestions()
}
