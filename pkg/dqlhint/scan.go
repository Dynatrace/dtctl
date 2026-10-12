package dqlhint

import (
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/dynatrace-oss/dtctl/pkg/dql"
)

// queryContext is a query error prepared for the rules: the query, a masked
// structural view of it with bracket depths, its pipeline segments, and the
// error span as byte offsets.
type queryContext struct {
	q     string
	code  string // dql.Masked.Code
	depth []int  // dql.Masked.Depth
	segs  []dql.Segment
	start int // byte offset of the span start in q
	end   int // byte offset of the span end in q, inclusive
	args  []string
}

// newContext prepares e for the rules. It returns nil — no rule runs — when
// there is no query or position, the position does not point into the query,
// or dql.Scan refuses the text (unbalanced brackets or quotes). Comments
// are refused here as well: the rules build their fixes from the raw text,
// and a fix spliced around a comment could land inside it.
func newContext(e Error) *queryContext {
	if e.Query == "" || e.Span == nil {
		return nil
	}
	start := byteOffset(e.Query, e.Span.StartLine, e.Span.StartColumn)
	if start < 0 {
		return nil
	}
	end := start
	if e.Span.EndLine > 0 {
		if o := byteOffset(e.Query, e.Span.EndLine, e.Span.EndColumn); o > start {
			end = o
		}
	}
	m, ok := dql.Scan(e.Query)
	if !ok || m.HasComments {
		return nil
	}
	return &queryContext{q: e.Query, code: m.Code, depth: m.Depth, segs: m.Segments, start: start, end: end, args: e.Arguments}
}

// byteOffset converts a 1-based line and character column into a byte offset,
// or -1 when it points past the line.
func byteOffset(q string, line, col int) int {
	if line < 1 || col < 1 {
		return -1
	}
	off := 0
	for l := 1; l < line; l++ {
		nl := strings.IndexByte(q[off:], '\n')
		if nl < 0 {
			return -1
		}
		off += nl + 1
	}
	for c := 1; c < col; c++ {
		if off >= len(q) || q[off] == '\n' {
			return -1
		}
		_, size := utf8.DecodeRuneInString(q[off:])
		off += size
	}
	if off >= len(q) || q[off] == '\n' {
		return -1
	}
	return off
}

// segmentAt returns the pipeline segment holding byte offset off.
func (c *queryContext) segmentAt(off int) *dql.Segment {
	for i := range c.segs {
		if off >= c.segs[i].Start && off < c.segs[i].End {
			return &c.segs[i]
		}
	}
	return nil
}

func (c *queryContext) arg(i int) string {
	if i < len(c.args) {
		return c.args[i]
	}
	return ""
}

// edit replaces q[start:end] with text.
type edit struct {
	start, end int
	text       string
}

// apply returns q with edits applied. Overlapping edits have no single
// meaning, so they leave q unchanged, which drops the hint.
func apply(q string, edits []edit) string {
	sort.Slice(edits, func(i, j int) bool { return edits[i].start < edits[j].start })
	var out strings.Builder
	last := 0
	for _, e := range edits {
		if e.start < last {
			return q
		}
		out.WriteString(q[last:e.start])
		out.WriteString(e.text)
		last = e.end
	}
	out.WriteString(q[last:])
	return out.String()
}

func isIdentByte(b byte) bool {
	return b == '_' || b == '.' || (b >= '0' && b <= '9') || (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z')
}

// wordAt reports whether word occurs in s at off as a whole identifier.
func wordAt(s string, off int, word string) bool {
	if off < 0 || !strings.HasPrefix(s[off:], word) {
		return false
	}
	if off > 0 && isIdentByte(s[off-1]) {
		return false
	}
	after := off + len(word)
	return after >= len(s) || !isIdentByte(s[after])
}

// closingParen returns the offset of the bracket closing the one at open.
func (c *queryContext) closingParen(open int) int {
	for i := open + 1; i < len(c.code); i++ {
		if c.code[i] == ')' && c.depth[i] == c.depth[open] {
			return i
		}
	}
	return -1
}

// splitArgs splits the call arguments between the brackets at open and close
// on their top-level commas, returning each argument trimmed.
func (c *queryContext) splitArgs(open, close int) []string {
	var out []string
	from := open + 1
	for i := open + 1; i <= close; i++ {
		if i == close || (c.code[i] == ',' && c.depth[i] == c.depth[open]+1) {
			out = append(out, strings.TrimSpace(c.q[from:i]))
			from = i + 1
		}
	}
	return out
}
