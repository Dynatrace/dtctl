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

// mask is what Scan writes over the contents of literals and comments, so no
// rule reading Masked.Code mistakes masked text for an operator, a pipe or a
// bracket. Quotes and backticks stay unmasked, so a run of mask that starts a
// token is always a comment.
//
// That only holds while the byte cannot also be code. DQL has no use for a #
// outside a literal: its comments are // and /* */, as every reader of DQL in
// dtctl assumes, and no operator is spelled with one. Scan records a query
// that types one there (Masked.typedMask), and a caller that acts on Code
// refuses it rather than read the #, and what follows it, as a comment Grail
// would not see.
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

	// typedMask is the mask byte outside every literal and comment, where
	// Code cannot tell it from masked text. It is recorded rather than
	// refused for the same reason: a single-quoted 'issue #12' holds one,
	// and that query is the quoting hint's to repair.
	typedMask bool
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
		case c == mask:
			m.typedMask = true
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

var (
	byParamRe = regexp.MustCompile(`\bby\s*:`)
	// countAllRe is one whole aggregation that counts every record, with or
	// without a name.
	countAllRe = regexp.MustCompile(`^(?:([A-Za-z_][\w.]*)\s*=\s*)?count\(\s*\)$`)
)

// UngroupedCount reports the column in which query's last stage counts the
// records it was given: a summarize without by: that holds count(), named or
// not, as a whole aggregation. Such a stage returns one row even for no
// records, so a zero in that column is an empty result that does not look
// like one. ok is false for everything else: a grouped summarize, a stage
// after it, an expression around the count, or a backtick-quoted name.
func UngroupedCount(query string) (column string, ok bool) {
	m, scanned := Scan(query)
	if !scanned || m.typedMask {
		return "", false
	}
	last := m.Segments[len(m.Segments)-1]
	if asciiLower(last.Command) != "summarize" {
		return "", false
	}
	const blank = " \t\r\n#" // whitespace, and what Scan masks a comment with
	start := last.Start + strings.Index(m.Code[last.Start:last.End], last.Command) + len(last.Command)
	end := last.Start + len(strings.TrimRight(m.Code[last.Start:last.End], blank))
	if byParamRe.MatchString(m.Code[start:end]) {
		return "", false
	}
	// The aggregations sit at the stage's own depth, or one deeper when they
	// are grouped in braces.
	level := 0
	if open := start + len(m.Code[start:end]) - len(strings.TrimLeft(m.Code[start:end], blank)); open < end && m.Code[open] == '{' && m.Code[end-1] == '}' {
		start, end, level = open+1, end-1, 1
	}
	from := start
	for i := start; i <= end; i++ {
		if i < end && (m.Code[i] != ',' || m.Depth[i] != level) {
			continue
		}
		if match := countAllRe.FindStringSubmatch(strings.Trim(m.Code[from:i], blank)); match != nil {
			if match[1] == "" {
				return "count()", true
			}
			return match[1], true
		}
		from = i + 1
	}
	return "", false
}
