// Package dqlhint turns a DQL error reported by the Grail query API into
// repair hints: a caret-marked snippet of the offending query text, and — for
// a small table of known authoring traps — a corrected query.
//
// The rules are deliberately conservative. Each one fires only when the error
// type, its arguments, the reported position, and the query text all agree on
// the same mistake; anything less produces no hint. An agent that receives a
// wrong "fix" loses more turns than one that receives none.
package dqlhint

import (
	"strings"
)

// Span is the offending part of a query: 1-based line and column, measured in
// characters, with the end inclusive. A zero EndLine means the end is unknown
// and the span covers a single character.
type Span struct {
	StartLine, StartColumn int
	EndLine, EndColumn     int
}

// Error is the part of a Grail query error the rules read.
type Error struct {
	// Type is the Grail error type, e.g. PARSE_ERROR.
	Type string
	// Arguments are the error message's format arguments, e.g. the offending
	// token or parameter name.
	Arguments []string
	// Query is the query as the backend parsed it; Span indexes into it.
	Query string
	Span  *Span
}

// hint is one rule's output: either a corrected query or, when the fix depends
// on something the error does not reveal, advice text.
type hint struct {
	rule   string
	reason string
	query  string
	advice string
}

// Repair is one suggestion for a query error: a corrected query with the
// rule behind it, or, when the fix depends on something the error does not
// reveal, advice.
type Repair struct {
	// Reason is the rule a rewrite follows.
	Reason string
	// Query is Error.Query with the mistake corrected, "" for advice.
	Query string
	// Advice is the suggestion when there is no rewrite.
	Advice string
}

// Render returns the suggestion as a caller shows it: the advice, or the
// reason and a runnable `dtctl query '…'` command, so the reader learns the
// rule and not just the fix. The command runs query, which is r.Query unless
// the caller sent a text of its own making and repairs what the user typed
// instead, followed by flags.
func (r Repair) Render(query string, flags ...string) string {
	if r.Query == "" {
		return r.Advice
	}
	command := "dtctl query " + shellQuote(query)
	for _, f := range flags {
		command += " " + f
	}
	return r.Reason + ": " + command
}

// Repairs returns the repairs for a query error, most specific first.
func Repairs(e Error) []Repair {
	var out []Repair
	for _, h := range hints(e) {
		out = append(out, Repair{Reason: h.reason, Query: h.query, Advice: h.advice})
	}
	return out
}

// Suggest returns the repair suggestions for a query error, most specific
// first, each rendered for the query the error reports.
func Suggest(e Error) []string {
	var out []string
	for _, r := range Repairs(e) {
		out = append(out, r.Render(r.Query))
	}
	return out
}

func hints(e Error) []hint {
	c := newContext(e)
	if c == nil {
		return nil
	}
	var out []hint
	for _, r := range rules {
		if !r.matchesType(e.Type) {
			continue
		}
		h, ok := r.apply(c)
		if !ok || h.query == e.Query {
			continue
		}
		h.rule = r.name
		out = append(out, h)
	}
	return out
}

// shellQuote wraps s in single quotes for a POSIX shell, the form every other
// dtctl suggestion uses for a query.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
