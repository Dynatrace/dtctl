package cmd

import (
	"context"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/dynatrace-oss/dtctl/pkg/output"
)

// previewNoticeShown records the notices this process has printed. A
// concurrent invocation keeps its own record (invocation.previewNoticeShown):
// shared, this map would be written by several requests at once — a fatal
// error in Go, not a recoverable panic — and one tenant's request would
// silence the notice for every later one.
var previewNoticeShown = map[string]bool{}

// attachPreviewNotice prints area's Preview notice before a command under cmd
// runs. Cobra runs only the nearest PersistentPreRunE (EnableTraverseRunHooks
// is off), so this hook would otherwise replace the root's, and with it the
// --dry-run rejection and global-flag validation on every tree and initConfig
// on a concurrent one. It runs the nearest ancestor's hook first, as cobra
// would have without it, and prints nothing for a command line that hook
// rejects.
func attachPreviewNotice(cmd *cobra.Command, area string) {
	prev := cmd.PersistentPreRunE
	cmd.PersistentPreRunE = func(c *cobra.Command, args []string) error {
		for p := cmd.Parent(); p != nil; p = p.Parent() {
			if p.PersistentPreRunE != nil {
				if err := p.PersistentPreRunE(c, args); err != nil {
					return err
				}
				break
			}
		}
		if prev != nil {
			if err := prev(c, args); err != nil {
				return err
			}
		}
		printPreviewNotice(cmdContext(c), area)
		return nil
	}
}

func printPreviewNotice(ctx context.Context, area string) {
	shown := previewNoticeShown
	if inv := concurrentInvocation(ctx); inv != nil {
		if inv.previewNoticeShown == nil {
			inv.previewNoticeShown = map[string]bool{}
		}
		shown = inv.previewNoticeShown
	}
	if shown[area] {
		return
	}
	shown[area] = true

	message := fmt.Sprintf("%s commands are in Preview and may change in future releases.", area)
	tag := output.Colorize(output.Yellow, "[Preview]")
	fmt.Fprintf(currentStderr(ctx), "%s %s\n", tag, message)
}
