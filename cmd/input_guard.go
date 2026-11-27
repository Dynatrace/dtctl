package cmd

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"
)

// The rule behind this file: if dtctl does not use a value the caller gave, the
// command stops with a usage error before it calls the server. Two helpers
// cover the filter cases so no command words the rule on its own.

// enumFlagValue validates the value of an enum filter flag against allowed,
// case-insensitively, and returns the permitted spelling. An absent or empty
// flag returns "" (no filter); an empty value is not an error. An unknown
// value is a usage error that lists the permitted values.
func enumFlagValue(cmd *cobra.Command, name string, allowed []string) (string, error) {
	value, _ := cmd.Flags().GetString(name)
	if strings.TrimSpace(value) == "" {
		return "", nil
	}
	for _, a := range allowed {
		if strings.EqualFold(strings.TrimSpace(value), a) {
			return a, nil
		}
	}
	return "", &invalidInputError{err: fmt.Errorf("invalid --%s %q: must be one of %s",
		name, value, strings.Join(allowed, ", "))}
}

// rejectFiltersWithID makes a list filter given together with an id a usage
// error: the id selects one object, so the filter would be ignored. flags names
// the list-only flags; use it only on a command that takes an optional id.
func rejectFiltersWithID(cmd *cobra.Command, args []string, flags ...string) error {
	if len(args) == 0 {
		return nil
	}
	for _, flag := range flags {
		if cmd.Flags().Changed(flag) {
			return &invalidInputError{err: fmt.Errorf("--%s is a list filter and cannot be combined with an id (%q)", flag, args[0])}
		}
	}
	return nil
}

// Positional-argument validators. They wrap cobra's so that a wrong argument
// count is a usage error (exit 2, validation_error), not a generic failure,
// and so a stray argument says what the command takes instead of cobra's
// `unknown command "x" for "intents"`.

func usageArgs(v cobra.PositionalArgs) cobra.PositionalArgs {
	return func(cmd *cobra.Command, args []string) error {
		if err := v(cmd, args); err != nil {
			return &invalidInputError{err: err}
		}
		return nil
	}
}

// noArgs is cobra.NoArgs for a command that takes no positional argument. A
// command with subcommands keeps cobra's unknown-command handling (and its
// did-you-mean suggestions).
func noArgs(cmd *cobra.Command, args []string) error {
	if cmd.HasSubCommands() {
		return cobra.NoArgs(cmd, args)
	}
	if len(args) > 0 {
		return &invalidInputError{err: fmt.Errorf("%q takes no arguments (got %q)", cmd.CommandPath(), args[0])}
	}
	return nil
}

func maximumNArgs(n int) cobra.PositionalArgs   { return usageArgs(cobra.MaximumNArgs(n)) }
func exactArgs(n int) cobra.PositionalArgs      { return usageArgs(cobra.ExactArgs(n)) }
func minimumNArgs(n int) cobra.PositionalArgs   { return usageArgs(cobra.MinimumNArgs(n)) }
func rangeArgs(lo, hi int) cobra.PositionalArgs { return usageArgs(cobra.RangeArgs(lo, hi)) }

// rejectArgWithFlags makes a positional argument given together with a flag
// that supplies the same input a usage error: one of the two would be ignored.
func rejectArgWithFlags(cmd *cobra.Command, args []string, flags ...string) error {
	if len(args) == 0 {
		return nil
	}
	for _, flag := range flags {
		if cmd.Flags().Changed(flag) {
			return &invalidInputError{err: fmt.Errorf("argument %q cannot be combined with --%s; give the input one way", args[0], flag)}
		}
	}
	return nil
}
