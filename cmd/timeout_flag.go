package cmd

import (
	"errors"
	"fmt"
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
	d, err := time.ParseDuration(s)
	if err != nil {
		return &timeoutValueError{flag: t.name, value: s}
	}
	if d < 0 {
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

// asTimeoutValueError unwraps a --timeout parse error from pflag's wrapper.
func asTimeoutValueError(err error) (*timeoutValueError, bool) {
	var tv *timeoutValueError
	if errors.As(err, &tv) {
		return tv, true
	}
	return nil, false
}
