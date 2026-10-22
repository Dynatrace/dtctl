package cmd

import (
	"errors"
	"fmt"
	"strings"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

// errEmptyFlagValue is the parse failure an explicitly empty flag value
// produces. pflag wraps it in a *pflag.InvalidValueError that carries the flag
// itself, so enhanceFlagError identifies the case by type instead of matching
// the rendered message — which %q-escapes a tab or a non-breaking space into
// something no "is it blank" pattern recognizes.
var errEmptyFlagValue = errors.New("must not be empty")

// nonEmptyStringValue wraps a string flag so that an explicitly empty value
// (--task "", or --task "$UNSET") fails the parse. Cobra's MarkFlagRequired
// only checks that the flag was given, not that it carries a usable value.
type nonEmptyStringValue struct {
	pflag.Value
	defValue string
}

func (v *nonEmptyStringValue) Set(value string) error {
	if strings.TrimSpace(value) == "" {
		return errEmptyFlagValue
	}
	return v.Value.Set(value)
}

// Reset restores the declared default. The per-invocation flag reset
// (resetFlagSet) calls it instead of Set(DefValue), which the empty check
// would reject.
func (v *nonEmptyStringValue) Reset() {
	_ = v.Value.Set(v.defValue)
}

// nonEmptySliceValue is nonEmptyStringValue for a repeatable flag
// (StringArray, StringSlice): every occurrence is checked, so `--user ""`
// fails even next to a valid `--user alice`. It keeps the SliceValue methods
// visible, because a slice flag is reset with Replace — Set appends.
type nonEmptySliceValue struct {
	nonEmptyStringValue
	slice pflag.SliceValue
}

func (v *nonEmptySliceValue) Append(value string) error {
	if strings.TrimSpace(value) == "" {
		return errEmptyFlagValue
	}
	return v.slice.Append(value)
}

func (v *nonEmptySliceValue) Replace(values []string) error { return v.slice.Replace(values) }

func (v *nonEmptySliceValue) GetSlice() []string { return v.slice.GetSlice() }

func (v *nonEmptySliceValue) Reset() {
	_ = v.slice.Replace(sliceFlagDefaults(v.defValue))
}

// rejectEmptyFlag makes an explicitly empty value for the named flag a usage
// error. It is the whole fix for a flag that is optional, or required only in
// some modes: an absent flag keeps whatever the command does without it.
func rejectEmptyFlag(cmd *cobra.Command, name string) {
	flag := lookupOwnFlag(cmd, name)
	if flag == nil {
		panic(fmt.Sprintf("rejectEmptyFlag: %s has no --%s flag", cmd.Name(), name))
	}
	wrapped := nonEmptyStringValue{Value: flag.Value, defValue: flag.DefValue}
	if slice, ok := flag.Value.(pflag.SliceValue); ok {
		flag.Value = &nonEmptySliceValue{nonEmptyStringValue: wrapped, slice: slice}
		return
	}
	flag.Value = &wrapped
}

// emptyFlagValueError is the error the parse-time rejection produces, for a
// value that is only recognizably empty once the command body has parsed it:
// `--scope ","` passes rejectEmptyFlag but names no scope. It wraps
// errEmptyFlagValue, so it exits with the usage code and reports
// validation_error in agent mode, exactly like `--scope ""`.
func emptyFlagValueError(name string) error {
	return fmt.Errorf("--%s %w; pass a value or leave the flag out", name, errEmptyFlagValue)
}

// markFlagRequiredNonEmpty marks the named string flag as required and
// rejects an empty value for it.
func markFlagRequiredNonEmpty(cmd *cobra.Command, name string) {
	rejectEmptyFlag(cmd, name)
	if err := cmd.MarkFlagRequired(name); err != nil {
		panic(err)
	}
}

// helpFlagUsages renders fs for the usage template as pflag's FlagUsages does,
// with each nonEmptySliceValue swapped back for the slice it wraps. pflag
// decides whether to print "(default ...)" by switching on the value's
// concrete type, so a wrapped StringArray would otherwise gain "(default [])".
// The registered flags are left untouched: the copies live only in this set.
func helpFlagUsages(fs *pflag.FlagSet) string {
	unwrapped := pflag.NewFlagSet(fs.Name(), pflag.ContinueOnError)
	unwrapped.SortFlags = fs.SortFlags
	fs.VisitAll(func(f *pflag.Flag) {
		if v, ok := f.Value.(*nonEmptySliceValue); ok {
			c := *f
			c.Value = v.Value
			f = &c
		}
		unwrapped.AddFlag(f)
	})
	return unwrapped.FlagUsages()
}
