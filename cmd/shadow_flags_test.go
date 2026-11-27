package cmd

import (
	"testing"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

// TestNoLocalFlagShadowsRootPersistentFlag pins the 1.0 rule that a command
// never declares a flag whose name or shorthand equals a root persistent flag.
// A shadowing flag replaces the global one on that command, so
// `dtctl diff --context prod` would send "prod" to the wrong flag.
//
// The hidden root --dry-run is exempt: it exists only so the parser knows the
// flag is boolean and so a command without a dry run can reject it. Commands in
// dryRunCommands opt in by declaring their own (see dryrun.go).
func TestNoLocalFlagShadowsRootPersistentFlag(t *testing.T) {
	restorePristineTree(t.Context())

	byName := map[string]*pflag.Flag{}
	byShort := map[string]*pflag.Flag{}
	rootCmd.PersistentFlags().VisitAll(func(f *pflag.Flag) {
		if f.Hidden {
			return
		}
		byName[f.Name] = f
		if f.Shorthand != "" {
			byShort[f.Shorthand] = f
		}
	})
	if len(byName) == 0 {
		t.Fatal("found no root persistent flags")
	}

	var walk func(*cobra.Command)
	walk = func(c *cobra.Command) {
		check := func(fs *pflag.FlagSet) {
			fs.VisitAll(func(f *pflag.Flag) {
				if g, ok := byName[f.Name]; ok && g != f {
					t.Errorf("%q defines --%s, which shadows the root persistent flag", c.CommandPath(), f.Name)
				}
				if f.Shorthand != "" {
					if g, ok := byShort[f.Shorthand]; ok && g != f {
						t.Errorf("%q defines -%s (--%s), which shadows the root persistent flag --%s", c.CommandPath(), f.Shorthand, f.Name, g.Name)
					}
				}
			})
		}
		check(c.Flags())
		check(c.PersistentFlags())
		for _, sub := range c.Commands() {
			walk(sub)
		}
	}
	walk(rootCmd)
}
