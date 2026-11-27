package cmd

import (
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/spf13/pflag"
)

// timeoutFlag is the pflag.Value behind every --timeout flag. It is a
// time.Duration flag (Type "duration") whose parse error names the fix: a bare
// integer such as `--timeout 60` is rejected rather than read as seconds, so a
// user moving a value between commands sees the migration error.
type timeoutFlag struct {
	name string
	d    *time.Duration
	// strict is set on the flags that used to take integer seconds
	// (exec analyzer, exec slo): a unit-less 0 is the old integer spelling and
	// is rejected like any bare integer, and a negative value is an error.
	strict bool
}

// timeoutValueError is the error a --timeout flag returns for a bad value.
type timeoutValueError struct {
	flag  string
	value string
	// negative marks a well-formed duration below zero.
	negative bool
}

func (e *timeoutValueError) Error() string {
	if e.negative {
		return fmt.Sprintf("invalid --%s %q: must not be negative; use a duration such as 60s", e.flag, e.value)
	}
	return fmt.Sprintf("invalid --%s %q: use a duration such as 60s", e.flag, e.value)
}

func (t *timeoutFlag) Set(s string) error {
	// A unit-less number (0, +0, -0, 60, 1.5) is an old integer spelling.
	if t.strict {
		if _, err := strconv.ParseFloat(s, 64); err == nil {
			return &timeoutValueError{flag: t.name, value: s}
		}
	}
	d, err := time.ParseDuration(s)
	if err != nil {
		return &timeoutValueError{flag: t.name, value: s}
	}
	if t.strict && d < 0 {
		return &timeoutValueError{flag: t.name, value: s, negative: true}
	}
	*t.d = d
	return nil
}

func (t *timeoutFlag) String() string {
	if t.d == nil {
		return "0s"
	}
	return t.d.String()
}

func (t *timeoutFlag) Type() string { return "duration" }

// timeoutFlagVar registers a --timeout flag on fs with def as its default.
func timeoutFlagVar(fs *pflag.FlagSet, def time.Duration, usage string) {
	d := def
	fs.Var(&timeoutFlag{name: "timeout", d: &d}, "timeout", usage)
}

// strictTimeoutFlagVar is timeoutFlagVar for the flags whose type changed from
// integer seconds: it also rejects a bare 0 and negative durations.
func strictTimeoutFlagVar(fs *pflag.FlagSet, def time.Duration, usage string) {
	d := def
	fs.Var(&timeoutFlag{name: "timeout", d: &d, strict: true}, "timeout", usage)
}

func isTimeoutValueError(err error) bool {
	_, ok := asTimeoutValueError(err)
	return ok
}

// asTimeoutValueError unwraps a --timeout parse error from pflag's wrapper.
func asTimeoutValueError(err error) (*timeoutValueError, bool) {
	var tv *timeoutValueError
	if errors.As(err, &tv) {
		return tv, true
	}
	return nil, false
}
