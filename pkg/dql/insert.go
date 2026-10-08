package dql

import (
	"fmt"
	"sort"
	"strings"
)

// Filter is the condition InsertFilter adds for one data object.
type Filter struct {
	// Expr is the expression, without "filter". Empty means the object is
	// one the caller scopes but has no condition for: a query that fetches
	// it is refused, and a subquery that fetches it still counts as one.
	Expr string
	// Fields are the record fields Expr compares, so a filter the query
	// already has on one of them is recognised as the user's own.
	Fields []string
}

// Outcome says what InsertFilter did, or why it did nothing.
type Outcome string

const (
	Applied           Outcome = "applied"
	NotFetch          Outcome = "not_fetch"
	UnsupportedObject Outcome = "unsupported_object"
	UserFilter        Outcome = "user_filter"
	Subquery          Outcome = "subquery"
	Unscannable       Outcome = "unscannable"
)

// Rewrite is InsertFilter's result. Query equals the input unless Outcome is
// Applied.
type Rewrite struct {
	Query      string
	Outcome    Outcome
	DataObject string
	Filter     string
	Reason     string // one sentence for a person, when not applied
}

// filteringCommands are the stages that drop records by a condition. A user's
// condition on a bound field in one of them wins over the inserted filter. A
// filtering command DQL adds later belongs here: otherwise the scope is ANDed
// with it and reported as applied.
var filteringCommands = map[string]bool{"filter": true, "filterOut": true}

// InsertFilter adds "| filter (<expr>)" right after the query's leading
// `fetch <object>`, with the filter for that object. filters is keyed by data
// object.
//
// A wrong insertion silently returns the wrong data, so whatever the scan
// cannot vouch for is refused with the query unchanged: text Scan rejects or
// that holds a single quote; a first stage that is not exactly
// `fetch <name>`; an object without a filter; a subquery that fetches a
// filtered object again, which the insertion cannot reach; and a condition
// the query already places on one of the filter's fields.
func InsertFilter(query string, filters map[string]Filter) Rewrite {
	refuse := func(o Outcome, object, reason string) Rewrite {
		return Rewrite{Query: query, Outcome: o, DataObject: object, Reason: reason}
	}

	m, ok := Scan(query)
	if !ok || m.singleQuote {
		return refuse(Unscannable, "",
			"dtctl cannot read the query text safely (an unterminated string or comment, unbalanced brackets, or a single quote)")
	}
	first := m.Segments[0]
	object, ok := leadingFetch(m.Code[:first.End])
	if !ok {
		return refuse(NotFetch, "", "the query does not start with 'fetch <data object>'")
	}
	f := filters[object]
	if f.Expr == "" {
		return refuse(UnsupportedObject, object,
			fmt.Sprintf("repo scope has no filter for %s (it filters %s)", object, filteredObjects(filters)))
	}
	if sub, found := nestedFetch(m, filters); found {
		return refuse(Subquery, object,
			fmt.Sprintf("the query fetches %s again in a subquery, which the scope cannot reach", sub))
	}
	if field, found := userFilterField(query, m, f.Fields); found {
		return refuse(UserFilter, object,
			fmt.Sprintf("the query filters on %s itself, and its own filter wins", field))
	}

	// After a following stage's pipe the filter ends its own line; with no
	// stage after it, it starts one, so a trailing // comment cannot swallow it.
	at, insert := first.End, "| filter ("+f.Expr+")\n"
	if len(m.Segments) == 1 {
		at, insert = len(query), "\n| filter ("+f.Expr+")"
	}
	return Rewrite{
		Query:      query[:at] + insert + query[at:],
		Outcome:    Applied,
		DataObject: object,
		Filter:     f.Expr,
	}
}

// byteOrderMark is the UTF-8 BOM an editor may leave at the start of a file.
const byteOrderMark = "\uFEFF"

// leadingFetch returns the object a masked first stage fetches when the stage
// is exactly `fetch <name>`, optionally followed by its parameters.
func leadingFetch(stage string) (object string, ok bool) {
	i := 0
	if strings.HasPrefix(stage, byteOrderMark) {
		i = len(byteOrderMark)
	}
	i = skipSpaceAndComments(stage, i)
	if !strings.HasPrefix(stage[i:], "fetch") {
		return "", false
	}
	i += len("fetch")
	nameStart := skipSpaceAndComments(stage, i)
	if nameStart == i || nameStart >= len(stage) || !isNameStart(stage[nameStart]) {
		return "", false
	}
	end := nameEnd(stage, nameStart)
	if end < len(stage) && !isSpace(stage[end]) && stage[end] != ',' && stage[end] != mask {
		return "", false
	}
	return stage[nameStart:end], true
}

// nestedFetch finds a `fetch <object>` inside brackets for an object filters
// names, with or without a condition.
func nestedFetch(m Masked, filters map[string]Filter) (object string, found bool) {
	for off := 0; ; {
		i := strings.Index(m.Code[off:], "fetch")
		if i < 0 {
			return "", false
		}
		i += off
		off = i + len("fetch")
		if m.Depth[i] == 0 || !isWholeWord(m.Code, i, "fetch") {
			continue
		}
		name := skipSpaceAndComments(m.Code, off)
		if name == off {
			continue
		}
		if _, scoped := filters[m.Code[name:nameEnd(m.Code, name)]]; scoped {
			return m.Code[name:nameEnd(m.Code, name)], true
		}
	}
}

// userFilterField returns the first of fields that a filtering stage names,
// bare or as a backtick identifier.
func userFilterField(q string, m Masked, fields []string) (field string, found bool) {
	for _, seg := range m.Segments {
		if !filteringCommands[seg.Command] {
			continue
		}
		code := m.Code[seg.Start:seg.End]
		for _, f := range fields {
			if containsWholeWord(code, f) || hasBacktickName(q[seg.Start:seg.End], code, f) {
				return f, true
			}
		}
	}
	return "", false
}

// hasBacktickName reports whether a `…` identifier in text spells name.
// Backticks survive masking, so in code they are delimiters, never content.
func hasBacktickName(text, code, name string) bool {
	for off := 0; ; {
		open := strings.IndexByte(code[off:], '`')
		if open < 0 {
			return false
		}
		open += off
		close := open + 1 + strings.IndexByte(code[open+1:], '`')
		if text[open+1:close] == name {
			return true
		}
		off = close + 1
	}
}

func filteredObjects(filters map[string]Filter) string {
	var names []string
	for name, f := range filters {
		if f.Expr != "" {
			names = append(names, name)
		}
	}
	if len(names) == 0 {
		return "nothing"
	}
	sort.Strings(names)
	return strings.Join(names, ", ")
}

func skipSpaceAndComments(code string, i int) int {
	for i < len(code) && (isSpace(code[i]) || code[i] == mask) {
		i++
	}
	return i
}

func nameEnd(code string, i int) int {
	for i < len(code) && isNameByte(code[i]) {
		i++
	}
	return i
}

func containsWholeWord(s, word string) bool {
	for off := 0; ; {
		i := strings.Index(s[off:], word)
		if i < 0 {
			return false
		}
		if isWholeWord(s, off+i, word) {
			return true
		}
		off += i + 1
	}
}

// isWholeWord reports whether s[at:] starts with word and word is not part of
// a longer identifier; dots belong to identifiers, so dt.entity.service is not
// a word of dt.entity.service.name.
func isWholeWord(s string, at int, word string) bool {
	if !strings.HasPrefix(s[at:], word) || (at > 0 && isNameByte(s[at-1])) {
		return false
	}
	after := at + len(word)
	return after >= len(s) || !isNameByte(s[after])
}

func isSpace(b byte) bool {
	return b == ' ' || b == '\t' || b == '\n' || b == '\r'
}

func isNameStart(b byte) bool {
	return b == '_' || (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z')
}

func isNameByte(b byte) bool {
	return isNameStart(b) || b == '.' || (b >= '0' && b <= '9')
}
