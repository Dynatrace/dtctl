package cmd

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

// TestNewCommandTreeMatchesSingleton guards the hand-maintained factory in
// root_factory.go against drift from the init()-wired singleton tree.
//
// The singleton is what the CLI runs and what every other test exercises; the
// fresh tree is what a concurrent embedder runs. Any wiring that lives only in
// an init() — a flag, a stability mark, a scope annotation, a subcommand —
// silently disappears from the concurrent path unless the factory repeats it,
// and nothing else would notice: the equality corpus only covers a handful of
// commands. So this compares the two trees structurally, command by command
// and flag by flag.
//
// Development-tier commands are excluded: they are attached to the singleton
// per invocation and deliberately absent from fresh trees (see newCommandTree).
//
// The singleton is compared in its as-registered state. Earlier tests execute
// it, and an execution leaves per-run mutations (stability badges, masks) in
// place until the next run restores them, so the test restores first. Cobra's
// own --help flag and `help` command are ignored on both sides: cobra adds them
// the first time a command runs, which is history, not wiring.
func TestNewCommandTreeMatchesSingleton(t *testing.T) {
	restorePristineTree(context.Background())
	ctx := withInvocation(context.Background(), &invocation{concurrent: true})

	devPaths := map[string]bool{}
	for _, dc := range developmentCommands {
		walkCommands(dc.cmd, func(c *cobra.Command) { devPaths[c.CommandPath()] = true })
	}

	want := describeTree(rootCmd, devPaths)
	got := describeTree(newCommandTree(ctx).root, devPaths)

	var diffs []string
	for path, w := range want {
		g, ok := got[path]
		switch {
		case !ok:
			diffs = append(diffs, fmt.Sprintf("missing from fresh tree: %q", path))
		case g != w:
			diffs = append(diffs, fmt.Sprintf("%q differs:\n%s", path, lineDiff(w, g)))
		}
	}
	for path := range got {
		if _, ok := want[path]; !ok {
			diffs = append(diffs, fmt.Sprintf("only in fresh tree: %q", path))
		}
	}
	sort.Strings(diffs)
	for _, d := range diffs {
		t.Error(d)
	}
}

// describeTree renders every command in the tree to a stable, comparable
// string keyed by command path.
func describeTree(root *cobra.Command, skip map[string]bool) map[string]string {
	out := map[string]string{}
	walkCommands(root, func(c *cobra.Command) {
		if skip[c.CommandPath()] {
			return
		}
		// Cobra adds its default `help` command to a root the first time
		// that root executes — history, like the --help flag, not wiring.
		if c.Name() == "help" && c.HasParent() && !c.Parent().HasParent() {
			return
		}
		out[c.CommandPath()] = describeCommand(c)
	})
	return out
}

func describeCommand(c *cobra.Command) string {
	var b strings.Builder
	fmt.Fprintf(&b, "use=%q\n", c.Use)
	fmt.Fprintf(&b, "aliases=%q\n", c.Aliases)
	fmt.Fprintf(&b, "short=%q\n", c.Short)
	fmt.Fprintf(&b, "long=%q\n", c.Long)
	fmt.Fprintf(&b, "example=%q\n", c.Example)
	fmt.Fprintf(&b, "hidden=%v deprecated=%q\n", c.Hidden, c.Deprecated)
	fmt.Fprintf(&b, "runE=%v run=%v args=%v validArgsFn=%v\n",
		c.RunE != nil, c.Run != nil, c.Args != nil, c.ValidArgsFunction != nil)
	fmt.Fprintf(&b, "preRunE=%v persistentPreRunE=%v postRunE=%v\n",
		c.PreRunE != nil || c.PreRun != nil,
		c.PersistentPreRunE != nil || c.PersistentPreRun != nil,
		c.PostRunE != nil || c.PostRun != nil)
	fmt.Fprintf(&b, "disableFlagParsing=%v silenceErrors=%v silenceUsage=%v\n",
		c.DisableFlagParsing, c.SilenceErrors, c.SilenceUsage)
	fmt.Fprintf(&b, "annotations=%s\n", sortedMap(c.Annotations))
	describeFlags(&b, "local", c.LocalNonPersistentFlags())
	describeFlags(&b, "persistent", c.PersistentFlags())
	return b.String()
}

func describeFlags(b *strings.Builder, kind string, fs *pflag.FlagSet) {
	var lines []string
	fs.VisitAll(func(f *pflag.Flag) {
		if _, byCobra := f.Annotations[cobra.FlagSetByCobraAnnotation]; byCobra {
			return
		}
		// The value's Go type as well as its pflag type: a wrapper such as
		// nonEmptyStringValue (rejectEmptyFlag) delegates Type(), so a
		// constructor that forgot the wrapper would otherwise match the
		// singleton while accepting an empty value the CLI rejects.
		lines = append(lines, fmt.Sprintf("%s flag --%s -%s type=%s value_type=%T default=%q noopt=%q value=%q hidden=%v deprecated=%q usage=%q annotations=%s",
			kind, f.Name, f.Shorthand, f.Value.Type(), f.Value, f.DefValue, f.NoOptDefVal, f.Value.String(), f.Hidden, f.Deprecated, f.Usage, sortedFlagAnnotations(f.Annotations)))
	})
	sort.Strings(lines)
	for _, l := range lines {
		b.WriteString(l)
		b.WriteByte('\n')
	}
}

func sortedMap(m map[string]string) string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, fmt.Sprintf("%s=%q", k, m[k]))
	}
	return "{" + strings.Join(parts, ", ") + "}"
}

func sortedFlagAnnotations(m map[string][]string) string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, fmt.Sprintf("%s=%q", k, m[k]))
	}
	return "{" + strings.Join(parts, ", ") + "}"
}

// lineDiff lists the lines present on one side only, so a failure names the
// drifted property instead of dumping two whole command descriptions.
func lineDiff(want, got string) string {
	wl := strings.Split(want, "\n")
	gl := strings.Split(got, "\n")
	inGot := map[string]bool{}
	for _, l := range gl {
		inGot[l] = true
	}
	inWant := map[string]bool{}
	for _, l := range wl {
		inWant[l] = true
	}
	var b strings.Builder
	for _, l := range wl {
		if !inGot[l] {
			fmt.Fprintf(&b, "  - singleton: %s\n", l)
		}
	}
	for _, l := range gl {
		if !inWant[l] {
			fmt.Fprintf(&b, "  + fresh:     %s\n", l)
		}
	}
	return b.String()
}
