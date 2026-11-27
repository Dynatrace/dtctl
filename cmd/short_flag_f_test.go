package cmd

import (
	"testing"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

// TestShortFlagFMeansFile walks the whole command tree and fails on any
// binding of -f other than --file, and on any short form for --force. -y,
// --yes skips a prompt; --force overrides something dtctl refuses by default
// and is never abbreviated.
func TestShortFlagFMeansFile(t *testing.T) {
	var walk func(c *cobra.Command)
	walk = func(c *cobra.Command) {
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
		for _, sub := range c.Commands() {
			walk(sub)
		}
	}
	walk(rootCmd)
}
