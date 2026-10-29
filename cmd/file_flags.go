package cmd

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

// Annotation keys that declare which flags name files, so a caller can learn
// what a command line reads and writes without running it. Namespaced like
// pkg/stability's keys. They stay unexported: only this package reads them,
// and the commands catalog does not expose them.
//
// TestFileFlagsAreDeclared runs every command with each string flag set to a
// path on a recording filesystem, so a flag that reads a file without the
// annotation fails it; TestFileFlagAnnotationsAreValid holds the values.
const (
	// annotationFile marks a flag whose value names a file the command reads
	// through the vfs seam. Its value is one of the fileValue* kinds.
	annotationFile = "dtctl.io/file"
	// annotationWritesTo marks a flag that, when set, makes the command write
	// back to the paths of another flag on the same command. Its value is that
	// flag's name.
	annotationWritesTo = "dtctl.io/writes-to"
	// annotationInputWithoutArgs marks a command that reads its input from
	// stdin when it is given no argument and no file. Its value is "true".
	annotationInputWithoutArgs = "dtctl.io/input-without-args"
	// annotationArgumentFlag marks a flag that stands in for the command's
	// argument (query --dql, or query --file naming the query's file), so
	// setting it keeps an annotationInputWithoutArgs command from reading
	// stdin. Its value is "true".
	annotationArgumentFlag = "dtctl.io/argument"
	// annotationPrompts marks a command that asks for confirmation on stdin
	// unless one of the flags it lists is set, or the run is plain, agent or a
	// dry run. Its value is the flag names, comma-separated.
	annotationPrompts = "dtctl.io/prompts"
	// annotationRefusalOnly marks a group command whose own RunE only refuses
	// a missing or unknown subcommand and does nothing else, so Parse keeps it
	// to report the refusal Execute gives. Its value is "true". requireSubcommand
	// itself needs no mark; a group with a RunE of its own does.
	annotationRefusalOnly = "dtctl.io/refusal-only"
	// annotationPromptsOneArgument marks an annotationPrompts command that
	// asks only when it is given exactly one argument. Its value is "true".
	annotationPromptsOneArgument = "dtctl.io/prompts-one-argument"
)

// Values of annotationFile.
const (
	// fileValuePath: the value is a path; "-" is stdin.
	fileValuePath = "path"
	// fileValueAtPath: a value starting with "@" names a path, and "@-" is
	// stdin; any other value is literal content (exec api -d).
	fileValueAtPath = "at-path"
)

// markFileFlag declares that cmd's flag name names a file of the given kind.
// It panics when cmd has no such flag: a declaration that silently did nothing
// after a rename would drop the file from every caller's view.
func markFileFlag(cmd *cobra.Command, name, kind string) {
	if kind != fileValuePath && kind != fileValueAtPath {
		panic(fmt.Sprintf("cmd: %s --%s: unknown file kind %q", cmd.Name(), name, kind))
	}
	setFlagAnnotation(cmd, name, annotationFile, kind)
}

// markWritesTo declares that setting cmd's flag name makes the command write
// back to the paths of its file flag target (apply --write-id → --file).
// Declare target with markFileFlag first.
func markWritesTo(cmd *cobra.Command, name, target string) {
	f := lookupOwnFlag(cmd, target)
	if f == nil || len(f.Annotations[annotationFile]) == 0 {
		panic(fmt.Sprintf("cmd: %s --%s writes to --%s, which is not a declared file flag", cmd.Name(), name, target))
	}
	setFlagAnnotation(cmd, name, annotationWritesTo, target)
}

// markInputWithoutArgs declares that cmd reads stdin when it is given no
// argument and no file.
func markInputWithoutArgs(cmd *cobra.Command) {
	setCommandAnnotation(cmd, annotationInputWithoutArgs, "true")
}

// markArgumentFlag declares that cmd's flag name stands in for its argument:
// given the flag, a command declared with markInputWithoutArgs does not read
// stdin.
func markArgumentFlag(cmd *cobra.Command, name string) {
	setFlagAnnotation(cmd, name, annotationArgumentFlag, "true")
}

// markPrompts declares that cmd may ask for confirmation on stdin, and that
// setting any of skipFlags (--yes, --force, --confirm) keeps it from asking. A
// run that is plain (--plain, or implied by --agent) never asks, whatever the
// flags, so that is not listed.
//
// List "dry-run" only where the dry run returns before the question is asked:
// delete breakpoint and delete scheduling-rule ask first, so a dry run of
// either still reads stdin. --dry-run is bound after the constructors run, so
// it is not looked up here; TestFileFlagAnnotationsAreValid checks that the
// command has one.
//
// "May": breakpoint commands ask only when existing breakpoints are affected,
// and the declaration covers the command, not the case. It panics when cmd has
// no such flag, as markFileFlag does.
func markPrompts(cmd *cobra.Command, skipFlags ...string) {
	if len(skipFlags) == 0 {
		panic(fmt.Sprintf("cmd: %s declares a prompt with no flag that skips it", cmd.Name()))
	}
	for _, name := range skipFlags {
		if name != "dry-run" && lookupOwnFlag(cmd, name) == nil {
			panic(fmt.Sprintf("cmd: %s has no flag --%s to skip its prompt", cmd.Name(), name))
		}
	}
	setCommandAnnotation(cmd, annotationPrompts, strings.Join(skipFlags, ","))
}

// markPromptsOneArgument is markPrompts for a command that asks only when it
// is given exactly one argument (restore trash).
func markPromptsOneArgument(cmd *cobra.Command, skipFlags ...string) {
	markPrompts(cmd, skipFlags...)
	setCommandAnnotation(cmd, annotationPromptsOneArgument, "true")
}

// markRefusalOnly declares that cmd's RunE only refuses a missing or unknown
// subcommand (as requireSkillsSubcommand does), so Parse keeps it in place of
// recording a run.
func markRefusalOnly(cmd *cobra.Command) {
	setCommandAnnotation(cmd, annotationRefusalOnly, "true")
}

// lookupOwnFlag finds a flag cmd itself declares, local or persistent.
// Persistent flags only join cmd.Flags() once cobra parses, so a constructor
// has to look one up where it was declared.
func lookupOwnFlag(cmd *cobra.Command, name string) *pflag.Flag {
	if f := cmd.Flags().Lookup(name); f != nil {
		return f
	}
	return cmd.PersistentFlags().Lookup(name)
}

func setCommandAnnotation(cmd *cobra.Command, key, value string) {
	if cmd.Annotations == nil {
		cmd.Annotations = map[string]string{}
	}
	cmd.Annotations[key] = value
}

func setFlagAnnotation(cmd *cobra.Command, name, key, value string) {
	f := lookupOwnFlag(cmd, name)
	if f == nil {
		panic(fmt.Sprintf("cmd: %s has no flag --%s to declare %s on", cmd.Name(), name, key))
	}
	if f.Annotations == nil {
		f.Annotations = map[string][]string{}
	}
	f.Annotations[key] = []string{value}
}
