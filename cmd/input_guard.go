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
