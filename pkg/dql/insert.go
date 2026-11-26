package dql

import (
	"fmt"
	"regexp"
	"sort"
	"strconv"
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
	// ScanBounded reports that the fetch stage itself bounds what Grail
	// scans: a scanLimitGBytes above 0, a samplingRatio above 1, or either
	// with a value that is not a plain number and so may be one. The
	// inserted filter does not, so a caller can say so before sending.
	ScanBounded bool
	// ScanLimitLifted reports a negative scanLimitGBytes on the fetch stage,
	// which removes the scan limit, the environment's default included.
	ScanLimitLifted bool
}

// filteringCommands are the stages that drop records by a condition, in lower
// case. A user's condition on a bound field in one of them wins over the
// inserted filter. A filtering command DQL adds later belongs here: otherwise
// the scope is ANDed with it and reported as applied.
var filteringCommands = map[string]bool{"filter": true, "filterout": true}

// InsertFilter adds "| filter (<expr>)" right after the query's leading
// `fetch <object>`, with the filter for that object. filters is keyed by data
// object, in lower case.
//
// Grail reads command and data object names without regard to case (`FETCH
// Spans` is `fetch spans`), so they are matched that way here and a scoped
// object is reported under its key. Field names are case-sensitive and are
// compared as written.
//
// A wrong insertion silently returns the wrong data, so whatever the scan
// cannot vouch for is refused with the query unchanged: text Scan rejects,
// or that holds a single quote or a # outside a literal; a first stage that is not exactly
// `fetch <name>`; an object without a filter; a subquery that fetches a
// filtered object again, which the insertion cannot reach; and a condition
// the query already places on one of the filter's fields.
func InsertFilter(query string, filters map[string]Filter) Rewrite {
	refuse := func(o Outcome, object, reason string) Rewrite {
		return Rewrite{Query: query, Outcome: o, DataObject: object, Reason: reason}
	}

	m, ok := Scan(query)
	if !ok || m.singleQuote || m.typedMask {
		return refuse(Unscannable, "",
			"dtctl cannot read the query text safely (an unterminated string or comment, unbalanced brackets, a single quote, a # outside a string, or a fourth quote right after a triple-quoted string)")
	}
	first := m.Segments[0]
	object, ok := leadingFetch(m.Code[:first.End])
	if !ok {
		return refuse(NotFetch, "", "the query does not start with 'fetch <data object>'")
	}
	f, scoped := filters[asciiLower(object)]
	if scoped {
		object = asciiLower(object)
	}
	if f.Expr == "" {
		return refuse(UnsupportedObject, object,
			fmt.Sprintf("repo scope has no filter for %s (it filters %s)", object, filteredObjects(filters)))
	}
	if sub, found := nestedFetch(query, m, filters); found {
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
	stage := asciiLower(m.Code[first.Start:first.End])
	limit, sampling := scanParameter(stage, "scanlimitgbytes", 0), scanParameter(stage, "samplingratio", 1)
	return Rewrite{
		Query:           query[:at] + insert + query[at:],
		Outcome:         Applied,
		DataObject:      object,
		Filter:          f.Expr,
		ScanBounded:     limit == scanBound || sampling == scanBound,
		ScanLimitLifted: limit == scanLifted,
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
	if len(stage)-i < len("fetch") || !strings.EqualFold(stage[i:i+len("fetch")], "fetch") {
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
//
// It errs towards finding one: a subquery it misses is reported as scoped
// over rows the filter never saw. So the object may follow the word after
// anything Grail could take for white space, not only what dtctl knows to be
// (see isBlank), and may be spelled as a backtick identifier.
func nestedFetch(query string, m Masked, filters map[string]Filter) (object string, found bool) {
	code := asciiLower(m.Code)
	for off := 0; ; {
		i := strings.Index(code[off:], "fetch")
		if i < 0 {
			return "", false
		}
		i += off
		off = i + len("fetch")
		if m.Depth[i] == 0 || !isWholeWord(code, i, "fetch") {
			continue
		}
		start := off
		for start < len(code) && isBlank(code[start]) {
			start++
		}
		if start == off || start == len(code) {
			continue
		}
		name := code[start:nameEnd(code, start)]
		if code[start] == '`' {
			// Scan masked the identifier's contents and kept its backticks,
			// so the next one closes it.
			closing := start + 1 + strings.IndexByte(code[start+1:], '`')
			name = asciiLower(query[start+1 : closing])
		}
		if _, scoped := filters[name]; scoped {
			return name, true
		}
	}
}

// asciiLower lower-cases the ASCII letters of s byte by byte and changes
// nothing else, so offsets into the result are offsets into s whatever s
// holds. Folding by rune would not keep them: a byte that is not UTF-8
// decodes to U+FFFD, which is three bytes long.
func asciiLower(s string) string {
	b := []byte(s)
	for i, c := range b {
		if c >= 'A' && c <= 'Z' {
			b[i] = c + 'a' - 'A'
		}
	}
	return string(b)
}

// userFilterField returns the first of fields that a filtering stage names,
// bare or as a backtick identifier.
//
// A name inside square brackets is not the stage's: there it is a field of a
// subquery's records or an index into another field, and neither is a
// condition on the fetched record. Taking it for one would drop the scope
// for a filter the user never wrote.
func userFilterField(q string, m Masked, fields []string) (field string, found bool) {
	for _, seg := range m.Segments {
		if !filteringCommands[asciiLower(seg.Command)] {
			continue
		}
		code := outsideSquareBrackets(m.Code[seg.Start:seg.End])
		for _, f := range fields {
			if containsWholeWord(code, f) || hasBacktickName(q[seg.Start:seg.End], code, f) {
				return f, true
			}
		}
	}
	return "", false
}

// outsideSquareBrackets returns a masked stage with everything between a [
// and its ] blanked, at any nesting, so offsets stay offsets. Scan has
// already balanced the brackets.
func outsideSquareBrackets(code string) string {
	out := []byte(code)
	square := 0
	for i, c := range out {
		switch {
		case c == '[':
			square++
		case c == ']':
			square--
		case square > 0:
			out[i] = ' '
		}
	}
	return string(out)
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

// scanEffect is what one fetch parameter does to the scan.
type scanEffect int

const (
	scanUnbounded scanEffect = iota // absent, or a value that bounds nothing
	scanBound                       // a value above the neutral one, or one that is not a plain number
	scanLifted                      // a negative value, which for scanLimitGBytes removes the limit
)

// numberRe reads a plain decimal number at the start of a parameter's value.
var numberRe = regexp.MustCompile(`^-?(?:\d+\.?\d*|\.\d+)`)

// scanParameter reads param from a masked, lower-cased fetch stage and says
// what its value does to the scan. neutral is the value that changes
// nothing: a scan limit of 0 and a sampling ratio of 1. Comments between the
// name, the colon and the value are skipped.
func scanParameter(stage, param string, neutral float64) scanEffect {
	for off := 0; ; {
		i := strings.Index(stage[off:], param)
		if i < 0 {
			return scanUnbounded
		}
		at := off + i
		off = at + 1
		if !isWholeWord(stage, at, param) {
			continue
		}
		colon := skipSpaceAndComments(stage, at+len(param))
		if colon == len(stage) || stage[colon] != ':' {
			continue
		}
		value := stage[skipSpaceAndComments(stage, colon+1):]
		number := numberRe.FindString(value)
		n, err := strconv.ParseFloat(number, 64)
		switch rest := value[len(number):]; {
		case err != nil || (rest != "" && isNameByte(rest[0])):
			return scanBound
		case n < 0:
			return scanLifted
		case n > neutral:
			return scanBound
		}
		return scanUnbounded
	}
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

// isBlank reports a byte that may separate two words for Grail: white space,
// masked text, a control character, or part of a character outside ASCII
// (a no-break space, a line separator). Which of those Grail accepts is not
// known here, so the subquery check, which must not miss, takes them all.
func isBlank(b byte) bool {
	return b <= ' ' || b == mask || b >= 0x7f
}

func isNameStart(b byte) bool {
	return b == '_' || (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z')
}

func isNameByte(b byte) bool {
	return isNameStart(b) || b == '.' || (b >= '0' && b <= '9')
}
