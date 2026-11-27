package reposcope

import (
	"fmt"
	"strings"
	"unicode"
)

// InvalidError is a file or value that failed validation, with what to do
// about it.
type InvalidError struct {
	// Source names what was validated: a file path or an argument.
	Source      string
	Msg         string
	Suggestions []string
}

// Error is the source and the message, as Shown prints them. The checks
// quote the values they refuse, so a quoted value reads as it was written;
// showing the whole text is what holds for a check that does not.
func (e *InvalidError) Error() string {
	return Shown(e.Source + ": " + e.Msg)
}

// Shown is s as a message may print it. A source is often a path, and a
// directory name is whatever a clone carries: git stores any byte but NUL
// and "/", so a name can hold an escape sequence that acts on the terminal
// the message is printed to. A value of the scope file is as much the
// clone's. Each character a terminal does not show as itself is replaced by
// U+FFFD.
func Shown(s string) string {
	return strings.Map(func(r rune) rune {
		if !unicode.IsPrint(r) {
			return unicode.ReplacementChar
		}
		return r
	}, s)
}

// NotFoundError is an explicitly named entry that the host's environment does
// not define. Known lists the names it does define. NoFile says the repository
// has no scope file at all, which only the caller knows: a missing file loads
// as an empty one.
type NotFoundError struct {
	Name   string
	Host   string
	Known  []string
	NoFile bool
}

func (e *NotFoundError) Error() string {
	if e.NoFile {
		return fmt.Sprintf("repo scope %q not found: this repository has no %s ('dtctl repo-scope set' writes one)", e.Name, FileName)
	}
	if len(e.Known) == 0 {
		return fmt.Sprintf("repo scope %q not found: %s defines no entry for %s", e.Name, FileName, e.Host)
	}
	return fmt.Sprintf("repo scope %q not found for %s (entries: %s)", e.Name, e.Host, strings.Join(e.Known, ", "))
}
