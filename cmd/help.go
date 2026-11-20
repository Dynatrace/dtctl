package cmd

import (
	"context"
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/Dynatrace/dtctl/pkg/stability"
)

// newHelpCmd replaces cobra's built-in help command with one that answers
// exactly what `<command> --help` answers.
//
// Cobra's own `help <command>` prints the target's help unconditionally. The
// surface stages (profile, blocked commands, stability floor, deprecated
// surface) mask a command by hiding it and replacing its body with a guard, and
// `<command> --help` on a masked command reaches that guard, because masking
// also turns off flag parsing. Cobra's `help` went around the guard, so
// `help apply` printed the full help of a command the profile masks.
//
// Cobra also creates its help command lazily, inside Execute, after the
// surface stages have run. With no tier of its own it then resolved to
// experimental on a reused tree, so whether `help` survived a stable floor
// depended on whether an earlier run had created it. Installing the command
// here, declared stable, makes it the same on every run.
func newHelpCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "help [command]",
		Short: "Help about any command",
		Long: `Help provides help for any command in the application.
Simply type dtctl help [path to command] for full details.`,
		ValidArgsFunction: func(c *cobra.Command, args []string, toComplete string) ([]cobra.Completion, cobra.ShellCompDirective) {
			target, _, err := c.Root().Find(args)
			if err != nil {
				return nil, cobra.ShellCompDirectiveNoFileComp
			}
			if target == nil || isSurfaceMasked(target) {
				return nil, cobra.ShellCompDirectiveNoFileComp
			}
			var completions []cobra.Completion
			for _, sub := range target.Commands() {
				if sub.IsAvailableCommand() && strings.HasPrefix(sub.Name(), toComplete) {
					completions = append(completions, cobra.CompletionWithDesc(sub.Name(), sub.Short))
				}
			}
			return completions, cobra.ShellCompDirectiveNoFileComp
		},
		RunE: func(c *cobra.Command, args []string) error {
			root := c.Root()
			target, _, err := root.Find(args)
			if target == nil || err != nil {
				c.Printf("Unknown help topic %#q\n", args)
				return root.Usage()
			}
			if target.Context() == nil {
				target.SetContext(c.Context())
			}
			// A command that parses its own flags answers --help itself; every
			// masked command is one, and answers it with its block.
			if target.DisableFlagParsing && target.RunE != nil {
				return target.RunE(target, []string{"--help"})
			}
			target.InitDefaultHelpFlag()
			target.InitDefaultVersionFlag()
			return target.Help()
		},
	}
	stability.MarkStable(c)
	return c
}

// surfaceMaskedAnnotation marks a command one of the surface stages masked
// for this run. The pristine-tree restore resets annotations, so a mark does
// not outlive the run that set it.
const surfaceMaskedAnnotation = "dtctl.surface.masked"

// markSurfaceMasked records that a surface stage masked c.
func markSurfaceMasked(c *cobra.Command) {
	if c.Annotations == nil {
		c.Annotations = map[string]string{}
	}
	c.Annotations[surfaceMaskedAnnotation] = "true"
}

// isSurfaceMasked reports whether c, or any command above it, was masked for
// this run.
func isSurfaceMasked(c *cobra.Command) bool {
	for ; c != nil; c = c.Parent() {
		if c.Annotations[surfaceMaskedAnnotation] != "" {
			return true
		}
	}
	return false
}

// dropStaleCompletionCommand removes the `__complete` command a previous run
// on this tree left behind. Cobra adds it inside Execute when a completion is
// requested and never removes it, so on a reused tree the next run's surface
// stages would mask it like a user command, and cobra's fresh copy would be
// shadowed by the masked one. Removed before the stages run, it is never
// masked; cobra adds its own again when a run asks for completion.
func dropStaleCompletionCommand(root *cobra.Command) {
	for _, c := range root.Commands() {
		if c.Name() == cobra.ShellCompRequestCmd {
			root.RemoveCommand(c)
		}
	}
}

// completeMaskedTarget answers a shell-completion request whose target command
// is masked: no candidates, no file completion. Cobra would otherwise list the
// masked command's flags and call its argument-completion function, which can
// reach the platform. It reports false, writing nothing, when argv is not a
// completion request or its target is not masked.
func completeMaskedTarget(ctx context.Context, root *cobra.Command, argv []string) bool {
	if len(argv) < 2 || (argv[0] != cobra.ShellCompRequestCmd && argv[0] != cobra.ShellCompNoDescRequestCmd) {
		return false
	}
	// The last word is the one being completed; the command is what precedes it.
	target, _, err := root.Find(argv[1 : len(argv)-1])
	if err != nil || target == nil || !isSurfaceMasked(target) {
		return false
	}
	// The same two lines cobra prints for a completion with no candidates.
	fmt.Fprintf(currentStdout(ctx), ":%d\n", cobra.ShellCompDirectiveNoFileComp)
	fmt.Fprintln(currentStderr(ctx), "Completion ended with directive: ShellCompDirectiveNoFileComp")
	return true
}
