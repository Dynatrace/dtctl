// Package dql reads DQL text without parsing it. dtctl has no DQL grammar by
// design: Grail is the parser. What dtctl does to query text it does on an
// offset-preserving scan that masks literals and comments, and it refuses
// whatever that scan cannot vouch for.
//
// The package imports nothing from dtctl, so the hint rules, the executor and
// the repo-scope file can all use it without pulling in one another.
package dql

import (
	"regexp"
	"strings"
)

// mask is what Scan writes over the contents of literals and comments. DQL
// code cannot contain it, so no rule reading Masked.Code mistakes masked text
// for an operator, a pipe or a bracket. Quotes and backticks stay unmasked,
// so a run of mask that starts a token is always a comment.
const mask = '#'

// Masked is a structural view of a query. Code has the query's length, with
// the contents of every string literal ("…", """…""", `…` identifiers) and
// every comment (// to end of line, /* … */) replaced by '#'. Depth holds each
// byte's bracket nesting; a bracket belongs to its outer level. Segments are
// the pipeline stages, split at depth-0 pipes. Offsets into Code are offsets
// into the query.
type Masked struct {
	Code     string
	Depth    []int
	Segments []Segment
	// HasComments reports at least one comment. A caller that builds edits
	// from the raw text refuses on it: an edit spliced next to a comment can
	// land inside it.
	HasComments bool

	// singleQuote is a single quote outside every literal and comment. DQL
	// has no single-quoted strings, so what follows one is prose to Grail.
	// It is left unmasked because the quoting hint reads the query as typed.
	singleQuote bool
}

// Segment is one pipeline stage, query[Start:End] without its pipe, and the
// command it starts with ("" when none).
type Segment struct {
	Start, End int
	Command    string
}

// commandWordRe finds a stage's command, past whitespace and comments.
var commandWordRe = regexp.MustCompile(`^[\s#]*([A-Za-z_]\w*)`)

var openingBracket = map[byte]byte{')': '(', ']': '[', '}': '{'}

// Scan returns the masked view of q. ok is false for text no lexical rule
// should act on: an unterminated literal or comment, or unbalanced brackets.
func Scan(q string) (m Masked, ok bool) {
	code := []byte(q)
	depth := make([]int, len(q))
	var open []byte

	// span marks q[from:to] as one token at the current depth, masks
	// q[maskFrom:maskTo], and returns the offset the loop resumes after.
	span := func(from, to, maskFrom, maskTo int) int {
		for j := from; j < to; j++ {
			depth[j] = len(open)
		}
		for j := maskFrom; j < maskTo; j++ {
			code[j] = mask
		}
		return to - 1
	}

	for i := 0; i < len(q); i++ {
		depth[i] = len(open)
		switch c := q[i]; {
		case strings.HasPrefix(q[i:], `"""`):
			end := strings.Index(q[i+3:], `"""`)
			if end < 0 {
				return Masked{}, false
			}
			next := i + 3 + end + 3
			i = span(i, next, i+3, next-3)
		case c == '"' || c == '`':
			close := literalEnd(q, i)
			if close < 0 {
				return Masked{}, false
			}
			i = span(i, close+1, i+1, close)
		case strings.HasPrefix(q[i:], "//"):
			next := len(q)
			if nl := strings.IndexByte(q[i:], '\n'); nl >= 0 {
				next = i + nl
			}
			m.HasComments = true
			i = span(i, next, i, next)
		case strings.HasPrefix(q[i:], "/*"):
			end := strings.Index(q[i+2:], "*/")
			if end < 0 {
				return Masked{}, false
			}
			next := i + 2 + end + 2
			m.HasComments = true
			i = span(i, next, i, next)
		case c == '\'':
			m.singleQuote = true
		case c == '(' || c == '[' || c == '{':
			open = append(open, c)
		case c == ')' || c == ']' || c == '}':
			if len(open) == 0 || open[len(open)-1] != openingBracket[c] {
				return Masked{}, false
			}
			open = open[:len(open)-1]
			depth[i] = len(open)
		}
	}
	if len(open) != 0 {
		return Masked{}, false
	}
	m.Code = string(code)
	m.Depth = depth
	m.Segments = segments(m.Code, depth)
	return m, true
}

// literalEnd returns the offset of the quote closing the "…" string or `…`
// identifier opened at q[open], or -1. Both escape with a backslash: an
// identifier escapes its backticks and backslashes the way a string escapes
// its quotes.
func literalEnd(q string, open int) int {
	quote := q[open]
	for j := open + 1; j < len(q); j++ {
		switch {
		case q[j] == '\\':
			j++
		case q[j] == quote:
			return j
		}
	}
	return -1
}

func segments(code string, depth []int) []Segment {
	var out []Segment
	from := 0
	for i := 0; i <= len(code); i++ {
		if i < len(code) && (code[i] != '|' || depth[i] != 0) {
			continue
		}
		seg := Segment{Start: from, End: i}
		if m := commandWordRe.FindStringSubmatch(code[from:i]); m != nil {
			seg.Command = m[1]
		}
		out = append(out, seg)
		from = i + 1
	}
	return out
}

// Quote renders v as a DQL double-quoted string, backslash-escaping backslash
// and double quote. It does not make arbitrary text safe: callers hold v to a
// character set first.
func Quote(v string) string {
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(v) + `"`
}
