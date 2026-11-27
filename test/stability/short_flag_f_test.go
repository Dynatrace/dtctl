package stability_test

import (
	"testing"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	"github.com/Dynatrace/dtctl/cmd"
)

// TestShortFlagFMeansFile walks the complete command tree — every development
// feature registered, and serve wired as main wires it (see TestMain) — and
// fails on any binding of -f other than --file, and on any short form for
// --force. -y, --yes skips a prompt; --force overrides something dtctl refuses
// by default and is never abbreviated.
func TestShortFlagFMeansFile(t *testing.T) {
	visited := map[string]bool{}
	cmd.WalkCompleteCommandTree(func(c *cobra.Command) {
		visited[c.CommandPath()] = true
		check := func(f *pflag.Flag) {
			if f.Shorthand == "f" && f.Name != "file" {
				t.Errorf("%s: -f is bound to --%s; -f may only mean --file", c.CommandPath(), f.Name)
			}
			if f.Name == "force" && f.Shorthand != "" {
				t.Errorf("%s: --force has short form -%s; --force takes no short form", c.CommandPath(), f.Shorthand)
			}
		}
		c.Flags().VisitAll(check)
		c.PersistentFlags().VisitAll(check)
		c.InheritedFlags().VisitAll(check)
	})

	// The walk has to reach the development tier and serve, or the guard
	// silently skips the commands most likely to drift.
	for _, path := range []string{"dtctl account", "dtctl get actions", "dtctl describe action", "dtctl serve"} {
		if !visited[path] {
			t.Errorf("%s was not walked; the guard must cover the complete command tree", path)
		}
	}
}
