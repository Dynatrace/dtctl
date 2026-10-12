package reposcope

import (
	"fmt"
	"strings"
)

// InvalidError is a file or value that failed validation, with what to do
// about it.
type InvalidError struct {
	// Source names what was validated: a file path or an argument.
	Source      string
	Msg         string
	Suggestions []string
}

func (e *InvalidError) Error() string {
	return e.Source + ": " + e.Msg
}

// NotFoundError is an explicitly named entry that the host's environment does
// not define. Known lists the names it does define.
type NotFoundError struct {
	Name  string
	Host  string
	Known []string
}

func (e *NotFoundError) Error() string {
	if len(e.Known) == 0 {
		return fmt.Sprintf("repo scope %q not found: %s defines no entry for %s", e.Name, FileName, e.Host)
	}
	return fmt.Sprintf("repo scope %q not found for %s (entries: %s)", e.Name, e.Host, strings.Join(e.Known, ", "))
}
