package cmd

import (
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	"github.com/Dynatrace/dtctl/pkg/config"
	"github.com/Dynatrace/dtctl/pkg/stability"
)

// StabilityManifest renders the stability manifest for the complete command
// tree — every development-tier feature registered, whether or not this
// environment opted into it.
//
// It is exported for the manifest generator and its CI gate, which live outside
// this package: they must see `dtctl serve` too, and serve is wired in main
// because pkg/serve imports pkg/engine, which imports cmd.
func StabilityManifest() string {
	return withCompleteCommandTree(func() string { return stability.Manifest(rootCmd) })
}

// StabilityLint reports declarations that are internally inconsistent across
// the complete command tree. Same reason for being exported as
// StabilityManifest.
func StabilityLint() []error {
	return withCompleteCommandTree(func() []error { return stability.Lint(rootCmd) })
}

// StabilityUndeclaredCommands returns the paths of commands that declare no
// stability tier at all, across the complete command tree.
//
// StabilityLint reports these too, among its other findings. This exists so the
// CI gate can fail on *just* this rule with a message about this rule: it is
// the one a new command trips by omission rather than by a mistake, and its
// author needs to be told that silence is not stable rather than handed a list
// of unrelated lint categories.
func StabilityUndeclaredCommands() []string {
	return withCompleteCommandTree(func() []string {
		var undeclared []string
		stability.Walk(rootCmd, func(c *cobra.Command) {
			if !stability.Declared(c) && stability.DeclarationRequired(c, rootCmd) {
				undeclared = append(undeclared, stability.Path(c, rootCmd))
			}
		})
		return undeclared
	})
}

// withCompleteCommandTree registers every development feature, runs fn, and
// restores the tree. The restore matters: these helpers run in-process
// alongside ordinary command execution, and leaving a development feature
// attached would hand the next invocation surface it never opted into.
func withCompleteCommandTree[T any](fn func() T) T {
	applyDevelopmentRegistration(map[string]bool{config.DevelopmentAll: true})
	defer applyDevelopmentRegistration(nil)
	return fn()
}

// StabilitySinceVersions returns every version named by a stability-since
// declaration in the complete command tree, mapped to the commands and flags
// that name it.
//
// A since-version is a promise about a *release*: "experimental as of 0.39.0"
// tells a caller which version withdrew the guarantee. The declarations are
// written before that release exists, so they are the one part of the manifest
// that can be falsified by the release itself simply being numbered
// differently. Exported so test/stability can check them against the version
// the next release will carry.
func StabilitySinceVersions() map[string][]string {
	return withCompleteCommandTree(func() map[string][]string {
		out := map[string][]string{}
		stability.Walk(rootCmd, func(c *cobra.Command) {
			path := stability.Path(c, rootCmd)
			if since := stability.Since(c); since != "" {
				out[since] = append(out[since], path)
			}
			c.LocalFlags().VisitAll(func(f *pflag.Flag) {
				if c.InheritedFlags().Lookup(f.Name) != nil {
					return
				}
				if since := stability.SinceFlag(c, f.Name); since != "" {
					out[since] = append(out[since], path+" --"+f.Name)
				}
			})
		})
		return out
	})
}
