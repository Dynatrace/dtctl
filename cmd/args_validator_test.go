package cmd

import (
	"sort"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/Dynatrace/dtctl/pkg/config"
)

// argsExempt lists leaf commands that deliberately declare no Args validator,
// each with the reason. A leaf without one accepts any positional arguments
// and ignores what it does not use, so the exemption must say why that is
// right. The list can only shrink: an entry that no longer needs it fails the
// test until it is removed.
var argsExempt = map[string]string{}

// TestLeafCommandsValidateArgs holds the rule "input dtctl cannot use is a
// usage error": a leaf command declares what positional arguments it takes
// (cobra.NoArgs, ExactArgs, MaximumNArgs, ...), because cobra's default for a
// leaf is to accept anything and the command then ignores it.
func TestLeafCommandsValidateArgs(t *testing.T) {
	applyDevelopmentRegistration(map[string]bool{config.DevelopmentAll: true})
	defer applyDevelopmentRegistration(nil)

	seen := map[string]bool{}
	var missing []string
	var walk func(c *cobra.Command)
	walk = func(c *cobra.Command) {
		if len(c.Commands()) > 0 {
			for _, sub := range c.Commands() {
				walk(sub)
			}
			return
		}
		if !c.Runnable() {
			return
		}
		// cobra adds its own help command to the tree on first Execute.
		if c.Name() == "help" && c.Parent() == rootCmd {
			return
		}
		path := strings.TrimPrefix(c.CommandPath(), rootCmd.Name()+" ")
		if c.Args != nil {
			return
		}
		seen[path] = true
		if _, ok := argsExempt[path]; !ok {
			missing = append(missing, path)
		}
	}
	walk(rootCmd)

	sort.Strings(missing)
	if len(missing) > 0 {
		t.Errorf("%d leaf command(s) declare no Args validator: %s\n\n"+
			"Set Args to cobra.NoArgs, an exact count or a maximum, so an unused "+
			"positional argument is a usage error. If the command really takes "+
			"free-form arguments, validate them in a custom Args func, or exempt "+
			"it in argsExempt with the reason.", len(missing), strings.Join(missing, "\n  "))
	}
	for path := range argsExempt {
		if !seen[path] {
			t.Errorf("argsExempt lists %q, which no longer lacks an Args validator; remove it", path)
		}
	}
}
