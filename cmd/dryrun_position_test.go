package cmd

import (
	"context"
	"strings"
	"testing"
)

// TestDryRunReachesOptedInCommandInEitherPosition pins the safety property the
// hidden root --dry-run exists for: on a command that implements a dry run,
// `dtctl --dry-run <cmd>` must set the same flag as `dtctl <cmd> --dry-run`.
// If the root declaration ever won over the command's own flag, the leading
// spelling would be accepted and silently ignored, and the command would do
// the real work.
func TestDryRunReachesOptedInCommandInEitherPosition(t *testing.T) {
	t.Cleanup(func() {
		gFlags.dryRun = false
		rootDryRunFlag.Changed = false
	})
	for _, c := range dryRunCommands {
		if c.Root() != rootCmd {
			continue // a separate tree (the account commands), with its own root
		}
		path := strings.Fields(c.CommandPath())[1:] // drop "dtctl"
		for name, args := range map[string][]string{
			"before": append([]string{"--dry-run"}, path...),
			"after":  append(append([]string{}, path...), "--dry-run"),
		} {
			t.Run(c.CommandPath()+"/"+name, func(t *testing.T) {
				gFlags.dryRun = false
				rootDryRunFlag.Changed = false
				found, rest, err := rootCmd.Find(args)
				if err != nil {
					t.Fatalf("find %v: %v", args, err)
				}
				if found != c {
					t.Fatalf("%v resolved to %q, want %q", args, found.CommandPath(), c.CommandPath())
				}
				if err := found.ParseFlags(rest); err != nil {
					t.Fatalf("parse %v: %v", args, err)
				}
				if !dryRun(context.Background()) {
					t.Errorf("%v: dry run not set; the command would perform the real mutation", args)
				}
				if err := rejectUnimplementedDryRun(found); err != nil {
					t.Errorf("%v: rejected although %q implements a dry run: %v", args, c.CommandPath(), err)
				}
			})
		}
	}
}
