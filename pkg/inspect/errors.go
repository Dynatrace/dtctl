// Package inspect implements `dtctl inspect`: a small, fixed set of pure-Go
// analytics primitives over a spilled query-result file (Layer 2 of the result
// spill design). Its genuinely new capability over the Layer 1 manifest is row
// access (--head/--tail/--page/--fields); --schema/--stats/--sample re-derive the
// manifest from disk for a file whose envelope is no longer in context.
//
// The package is pure: it reads files and returns data structures. It never
// writes to stdout, parses CLI flags, or talks to Grail — the command layer owns
// those. It deliberately exposes no query/expression language (D16): aggregate
// questions go back to DQL, complex local analysis goes to an external tool on
// the file.
package inspect

import "github.com/Dynatrace/dtctl/pkg/output"

// Error is a typed inspect failure carrying a stable envelope error code (D32 /
// INSPECT IN9) and actionable suggestions. The command layer surfaces Code and
// Suggestions in the agent envelope (via errorToDetail) and Message to humans.
type Error struct {
	Code        string
	Message     string
	Suggestions []string
	// Err is an optional wrapped cause for errors.Is/As chains; it is not shown
	// to the user (Message already carries the human-readable text).
	Err error
}

func (e *Error) Error() string { return e.Message }

func (e *Error) Unwrap() error { return e.Err }

// errNotFound reports a spilled path that is missing / pruned / never existed
// (D32). The suggestion re-runs the original query when it is known from the
// sidecar, otherwise it nudges to re-query generically.
func errNotFound(path, query string, cause error) *Error {
	return &Error{
		Code:        output.ErrCodeSpillFileNotFound,
		Message:     "spill file not found: " + path,
		Suggestions: requerySuggestions(query),
		Err:         cause,
	}
}

// errUnreadable reports a present-but-unreadable file: a truncated .tmp survivor,
// a corrupt/partial file, or one written by an incompatible dtctl version (D32).
func errUnreadable(path, query string, cause error) *Error {
	msg := "spill file is unreadable: " + path
	if cause != nil {
		msg += " (" + cause.Error() + ")"
	}
	return &Error{
		Code:        output.ErrCodeSpillFileUnreadable,
		Message:     msg,
		Suggestions: requerySuggestions(query),
		Err:         cause,
	}
}

// errWrongContext refuses a path that belongs to a different context/tenant than
// the active one (D9/D32) — never read another tenant's spilled data after a
// context switch.
func errWrongContext(path, fileContext, activeContext string) *Error {
	msg := "spill file belongs to a different context"
	if fileContext != "" && activeContext != "" {
		msg += " (file: " + fileContext + ", active: " + activeContext + ")"
	}
	msg += ": " + path
	return &Error{
		Code:    output.ErrCodeSpillFileWrongContext,
		Message: msg,
		Suggestions: []string{
			"switch to the file's context with 'dtctl ctx use <name>', or",
			"re-run the original query in the active context to spill a fresh file",
		},
	}
}

// errWrongTenant refuses a path whose recorded tenant differs from the active
// tenant (D9/D32) — the strongest cross-account signal. It carries the same
// envelope code as errWrongContext but names the tenant axis so the message
// matches the mismatch that was actually detected.
func errWrongTenant(path, fileTenant, activeTenant string) *Error {
	msg := "spill file belongs to a different tenant"
	if fileTenant != "" && activeTenant != "" {
		msg += " (file tenant: " + fileTenant + ", active tenant: " + activeTenant + ")"
	}
	msg += ": " + path
	return &Error{
		Code:    output.ErrCodeSpillFileWrongContext,
		Message: msg,
		Suggestions: []string{
			"switch to the file's tenant/context with 'dtctl ctx use <name>', or",
			"re-run the original query in the active context to spill a fresh file",
		},
	}
}

// errUnknownField reports a --fields name absent from the file schema (IN9). The
// suggestion lists the available columns so the caller can correct the call.
func errUnknownField(field string, available []string) *Error {
	return &Error{
		Code:        codeUnknownField,
		Message:     "unknown field " + quote(field) + " is not a column in this file",
		Suggestions: []string{"available columns: " + joinColumns(available)},
	}
}

// errBadFlags reports invalid primitive/flag usage (IN9). It is constructed in
// the command layer (which owns flag parsing) but uses the shared code so the
// envelope error contract is consistent.
func errBadFlags(message string, suggestions ...string) *Error {
	return &Error{Code: codeBadFlags, Message: message, Suggestions: suggestions}
}

// BadFlags builds an inspect_bad_flags error. It is exported for the command
// layer, which owns flag parsing and one-primitive-per-call validation (IN9), so
// usage errors share the same stable envelope code as engine-side failures.
func BadFlags(message string, suggestions ...string) *Error {
	return errBadFlags(message, suggestions...)
}

// Inspect-local error codes (IN9). The spill_file_* codes are shared with Layer 1
// (output.ErrCodeSpillFile*) so the stale/missing-file contract is identical.
const (
	codeBadFlags     = "inspect_bad_flags"
	codeUnknownField = "inspect_unknown_field"
)

func requerySuggestions(query string) []string {
	if query != "" {
		return []string{"re-run the original query to spill a fresh file: dtctl query " + quote(query)}
	}
	return []string{"re-run the original query to spill a fresh file"}
}
