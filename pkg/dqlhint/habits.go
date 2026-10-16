package dqlhint

import (
	"regexp"
	"slices"
	"strings"
)

// bareArg is arg i without the backticks some error types quote it in.
func (c *queryContext) bareArg(i int) string { return strings.Trim(c.arg(i), "`") }

// Rules for habits carried over from SQL, Splunk and general-purpose languages.

// sqlAggregation: `stats count() as n by f` is rewritten whole, so one retry fixes all three habits.
func sqlAggregation(c *queryContext) (hint, bool) {
	seg := c.segmentAt(c.start)
	if seg == nil {
		return hint{}, false
	}
	switch {
	case c.arg(0) == "`as`" && wordAt(c.code, c.start, "as"):
		if seg.cmd != "stats" && !slices.Contains(assigningCommands, seg.cmd) {
			return hint{}, false
		}
	case c.arg(0) == "`by`" && wordAt(c.code, c.start, "by"):
		// summarize … by is byParameter's; only the stats spelling is ours.
		if seg.cmd != "stats" {
			return hint{}, false
		}
	case strings.TrimSpace(c.q[c.start:min(c.end+1, len(c.q))]) == "stats":
		if seg.cmd != "stats" {
			return hint{}, false
		}
	default:
		return hint{}, false
	}

	text := c.q[seg.start:seg.end]
	code := c.code[seg.start:seg.end]
	lead := len(text) - len(strings.TrimLeft(text, " \t\r\n"))
	tail := trailingSpace.FindString(text)
	bodyStart := lead + len(seg.cmd)
	bodyEnd := len(text) - len(tail)
	if bodyStart > bodyEnd {
		return hint{}, false
	}

	// The grouping clause: a trailing top-level `by`.
	aggEnd, fields := bodyEnd, ""
	for i := bodyStart; i < bodyEnd; i++ {
		if c.depth[seg.start+i] == 0 && wordAt(code, i, "by") {
			rest := strings.TrimSpace(text[i+len("by") : bodyEnd])
			rest = strings.TrimSpace(strings.TrimPrefix(rest, ":"))
			if strings.HasPrefix(rest, "{") && strings.HasSuffix(rest, "}") {
				rest = strings.TrimSpace(rest[1 : len(rest)-1])
			}
			if !fieldListRe.MatchString(rest) {
				return hint{}, false
			}
			aggEnd, fields = i, strings.Join(fieldSplitRe.Split(rest, -1), ", ")
			break
		}
	}

	// The column list: split on top-level commas, `expr as name` → `name = expr`.
	var items []string
	var unnamed [][2]string // original and DQL spelling of each unnamed column
	from := bodyStart
	for i := bodyStart; i <= aggEnd; i++ {
		if i < aggEnd && (code[i] != ',' || c.depth[seg.start+i] != 0) {
			continue
		}
		item := strings.TrimSpace(text[from:i])
		itemCode := strings.TrimSpace(code[from:i])
		from = i + 1
		if item == "" {
			continue
		}
		named := assignedRe.MatchString(itemCode)
		if m := asAliasRe.FindStringSubmatchIndex(itemCode); m != nil {
			item, named = item[m[2]:m[3]]+" = "+strings.TrimSpace(item[:m[0]]), true
		} else if strings.Contains(" "+itemCode+" ", " as ") {
			return hint{}, false
		}
		// Rename SQL aggregation spellings (count_distinct) too.
		renamed := item
		if m := callNameRe.FindStringSubmatchIndex(item); m != nil && m[0] == 0 {
			if to, ok := functionSynonyms[item[m[2]:m[3]]]; ok {
				renamed = to + item[m[3]:]
			}
		}
		if !named {
			unnamed = append(unnamed, [2]string{item, renamed})
		}
		items = append(items, renamed)
	}
	if len(items) == 0 {
		return hint{}, false
	}

	cmd := seg.cmd
	if cmd == "stats" {
		cmd = "summarize"
	}
	out := text[:lead] + cmd + " " + strings.Join(items, ", ")
	if fields != "" {
		out += ", by:{" + fields + "}"
	}
	edits := []edit{{seg.start, seg.end, out + tail}}
	// Later commands sorting or filtering on an unnamed column need its expression as name.
	for _, u := range unnamed {
		if !strings.HasSuffix(u[0], ")") {
			continue
		}
		for off := seg.end; ; {
			i := strings.Index(c.code[off:], u[0])
			if i < 0 {
				break
			}
			at := off + i
			if at == 0 || !isIdentByte(c.code[at-1]) && c.code[at-1] != '`' {
				edits = append(edits, edit{at, at + len(u[0]), "`" + u[1] + "`"})
			}
			off = at + len(u[0])
		}
	}
	return hint{
		reason: "DQL aggregates with summarize name = aggregation(…), by:{field} — no stats command, no `as` alias, no trailing by",
		query:  apply(c.q, edits),
	}, true
}

// assignedRe matches a column that is already named: `name = expr`.
var assignedRe = regexp.MustCompile(`^` + fieldName + `\s*=[^=]`)

// commandSynonyms map SQL, KQL and Splunk command words to DQL; distinct and count go through commandSynonym.
var commandSynonyms = map[string]string{
	"head": "limit", "take": "limit",
	"where": "filter", "project": "fields", "select": "fields",
	"eval": "fieldsAdd", "extend": "fieldsAdd",
}

// commandSynonym rewrites every foreign command word so the retry does not fail on the next.
func commandSynonym(c *queryContext) (hint, bool) {
	seg := c.segmentAt(c.start)
	if seg == nil || !wordAt(c.code, c.start, seg.cmd) {
		return hint{}, false
	}
	if _, ok := commandSynonyms[seg.cmd]; !ok && !slices.Contains([]string{"distinct", "unique", "count"}, seg.cmd) {
		return hint{}, false
	}
	var edits []edit
	for _, seg := range c.segs {
		text := c.q[seg.start:seg.end]
		lead := len(text) - len(strings.TrimLeft(text, " \t\r\n"))
		tail := trailingSpace.FindString(text)
		body := strings.TrimSpace(text[lead+len(seg.cmd) : len(text)-len(tail)])
		var out string
		switch to, ok := commandSynonyms[seg.cmd]; {
		case ok:
			out = to + text[lead+len(seg.cmd):len(text)-len(tail)]
		case seg.cmd == "distinct" || seg.cmd == "unique":
			if !fieldListRe.MatchString(body) {
				return hint{}, false
			}
			out = " summarize count(), by:{" + strings.Join(fieldSplitRe.Split(body, -1), ", ") + "}"
		case seg.cmd == "count" && body == "":
			out = " summarize count()"
		default:
			continue
		}
		edits = append(edits, edit{seg.start + lead, seg.end - len(tail), strings.TrimLeft(out, " ")})
	}
	if len(edits) == 0 {
		return hint{}, false
	}
	return hint{
		reason: "DQL spells these commands limit, filter, fields, fieldsAdd and summarize count(), by:{…}",
		query:  apply(c.q, edits),
	}, true
}

// asAliasRe matches the `as name` suffix of a column expression.
var asAliasRe = regexp.MustCompile(`\s+as\s+(` + fieldName + `)$`)

// singleQuotes: DQL strings take double quotes; a doubled single quote is SQL's escape.
func singleQuotes(c *queryContext) (hint, bool) {
	q := c.q
	var out strings.Builder
	changed := false
	for i := 0; i < len(q); i++ {
		switch q[i] {
		case '"', '`':
			j := i + 1
			for ; j < len(q) && q[j] != q[i]; j++ {
				if q[j] == '\\' && q[i] == '"' {
					j++
				}
			}
			if j >= len(q) {
				return hint{}, false
			}
			out.WriteString(q[i : j+1])
			i = j
		case '\'':
			var lit strings.Builder
			j := i + 1
			for ; j < len(q); j++ {
				if q[j] == '\'' {
					if j+1 < len(q) && q[j+1] == '\'' {
						lit.WriteByte('\'')
						j++
						continue
					}
					break
				}
				if q[j] == '"' || q[j] == '\\' {
					lit.WriteByte('\\')
				}
				lit.WriteByte(q[j])
			}
			if j >= len(q) {
				return hint{}, false
			}
			out.WriteString(`"` + lit.String() + `"`)
			changed = true
			i = j
		default:
			out.WriteByte(q[i])
		}
	}
	if !changed {
		return hint{}, false
	}
	return hint{reason: "DQL strings take double quotes", query: out.String()}, true
}

// functionSynonyms maps exact renames only; arguments stay as they are.
var functionSynonyms = map[string]string{
	"toLower": "lower", "tolower": "lower", "toLowerCase": "lower", "lowercase": "lower", "lcase": "lower",
	"toUpper": "upper", "toupper": "upper", "toUpperCase": "upper", "uppercase": "upper", "ucase": "upper",
	"tonumber": "toDouble", "toNumber": "toDouble", "to_number": "toDouble", "toFloat": "toDouble", "tofloat": "toDouble", "parseFloat": "toDouble", "todouble": "toDouble",
	"toInt": "toLong", "toint": "toLong", "toInteger": "toLong", "parseInt": "toLong", "tolong": "toLong",
	"tostring": "toString", "to_string": "toString",
	"len": "stringLength", "length": "stringLength", "strlen": "stringLength", "char_length": "stringLength",
	"substr":   "substring",
	"count_if": "countIf", "countif": "countIf",
	"dcount": "countDistinct", "distinct_count": "countDistinct", "count_distinct": "countDistinct", "countdistinct": "countDistinct",
	"mean":    "avg",
	"is_null": "isNull", "isnull": "isNull", "is_not_null": "isNotNull", "isnotnull": "isNotNull",
	"ifnull": "coalesce", "nvl": "coalesce",
}

// caseFoldingCompare are the string tests that take caseSensitive: false.
var caseFoldingCompare = []string{"contains", "startsWith", "endsWith"}

var callNameRe = regexp.MustCompile(`([A-Za-z_][\w.]*)\s*\(`)

// functionSynonym renames functions; a case conversion around a contains/startsWith/endsWith subject becomes caseSensitive: false, but is left alone elsewhere (it defeats the index).
func functionSynonym(c *queryContext) (hint, bool) {
	name := c.bareArg(0)
	if name == "" || !wordAt(c.code, c.start, name) {
		return hint{}, false
	}
	target, ok := functionSynonyms[name]
	if !ok {
		return hint{}, false
	}
	folding := target == "lower" || target == "upper"
	var edits []edit
	for _, m := range callNameRe.FindAllStringSubmatchIndex(c.code, -1) {
		if c.code[m[2]:m[3]] != name || !wordAt(c.code, m[2], name) {
			continue
		}
		open := m[1] - 1
		if !folding {
			edits = append(edits, edit{m[2], m[3], target})
			continue
		}
		e, ok := foldingEdit(c, m[2], open, target)
		if !ok {
			return hint{}, false
		}
		edits = append(edits, e)
	}
	if len(edits) == 0 {
		return hint{}, false
	}
	reason := "DQL calls it " + target + "(…)"
	if folding {
		reason = "compare without case with contains(x, \"…\", caseSensitive: false) rather than a " + name + "() around the field"
	}
	return hint{reason: reason, query: apply(c.q, edits)}, true
}

// foldingEdit rewrites one case-conversion call to target.
func foldingEdit(c *queryContext, nameStart, open int, target string) (edit, bool) {
	closing := c.closingParen(open)
	if closing < 0 {
		return edit{}, false
	}
	if inner := c.splitArgs(open, closing); len(inner) == 1 && inner[0] != "" {
		// The first argument of contains(…)/startsWith(…)/endsWith(…)?
		i := nameStart - 1
		for i >= 0 && (c.code[i] == ' ' || c.code[i] == '\t') {
			i--
		}
		if i >= 0 && c.code[i] == '(' {
			if fn := identBefore(c.code, i); slices.Contains(caseFoldingCompare, fn) {
				outerClose := c.closingParen(i)
				if outerClose < 0 {
					return edit{}, false
				}
				args := c.splitArgs(i, outerClose)
				if len(args) != 2 || args[1] == "" {
					return edit{}, false
				}
				return edit{i + 1, outerClose, inner[0] + ", " + args[1] + ", caseSensitive: false"}, true
			}
		}
	}
	if seg := c.segmentAt(nameStart); seg == nil || isFilterCommand(seg.cmd) {
		return edit{}, false
	}
	return edit{nameStart, open, target}, true
}

// identBefore returns the identifier that ends right before s[end], skipping
// blanks, or "".
func identBefore(s string, end int) string {
	j := end
	for j > 0 && (s[j-1] == ' ' || s[j-1] == '\t') {
		j--
	}
	k := j
	for k > 0 && isIdentByte(s[k-1]) {
		k--
	}
	return s[k:j]
}

// aggregationReference: an unnamed aggregation becomes a field named `count()` that later commands must backtick-quote.
func aggregationReference(c *queryContext) (hint, bool) {
	ref := strings.TrimSpace(c.q[c.start:min(c.end+1, len(c.q))])
	if ref == "" || !strings.HasSuffix(ref, ")") || c.code[c.start:c.start+len(ref)] != ref {
		return hint{}, false
	}
	at := c.segmentAt(c.start)
	if at == nil || slices.Contains(groupingCommands, at.cmd) {
		return hint{}, false
	}
	// The aggregating command before it must produce that column unnamed.
	agg := -1
	for i := range c.segs {
		if c.segs[i].start >= at.start {
			break
		}
		if slices.Contains(groupingCommands, c.segs[i].cmd) {
			agg = i
		}
	}
	if agg < 0 || !producesUnnamed(c, c.segs[agg], ref) {
		return hint{}, false
	}
	var edits []edit
	for off := c.segs[agg].end; ; {
		i := strings.Index(c.code[off:], ref)
		if i < 0 {
			break
		}
		s := off + i
		if s == 0 || !isIdentByte(c.code[s-1]) {
			edits = append(edits, edit{s, s + len(ref), "`" + ref + "`"})
		}
		off = s + len(ref)
	}
	if len(edits) == 0 {
		return hint{}, false
	}
	return hint{
		reason: "an unnamed aggregation is a field named " + ref + " after summarize: quote it in backticks, or name it there (n = " + ref + ")",
		query:  apply(c.q, edits),
	}, true
}

// producesUnnamed reports whether seg lists ref as a top-level column with no
// `name =` in front of it.
func producesUnnamed(c *queryContext, seg segment, ref string) bool {
	for off := seg.start; ; {
		i := strings.Index(c.code[off:seg.end], ref)
		if i < 0 {
			return false
		}
		s := off + i
		if c.depth[s] == 0 && (s == 0 || !isIdentByte(c.code[s-1])) {
			before := strings.TrimRight(c.code[seg.start:s], " \t\r\n")
			if !strings.HasSuffix(before, "=") {
				return true
			}
		}
		off = s + len(ref)
	}
}

// entityFieldNames maps guessed field names to the ones entity tables carry.
var entityFieldNames = map[string]string{
	"name": "entity.name", "display_name": "entity.name", "displayName": "entity.name", "displayname": "entity.name",
	"entity_name": "entity.name", "entityName": "entity.name",
	"entity.id": "id", "entityId": "id", "entity_id": "id", "entityid": "id",
}

var entityFetchRe = regexp.MustCompile(`^\s*fetch\s+dt\.entity\.[a-z_]+\b`)

// entityField: entity tables use entity.name and id; every use is rewritten.
func entityField(c *queryContext) (hint, bool) {
	field := c.bareArg(0)
	target, ok := entityFieldNames[field]
	if !ok || !wordAt(c.code, c.start, field) || !entityFetchRe.MatchString(c.code[c.segs[0].start:c.segs[0].end]) {
		return hint{}, false
	}
	var edits []edit
	for off := c.segs[0].end; ; {
		i := strings.Index(c.code[off:], field)
		if i < 0 {
			break
		}
		s := off + i
		if wordAt(c.code, s, field) {
			edits = append(edits, edit{s, s + len(field), target})
		}
		off = s + len(field)
	}
	return hint{
		reason: "dt.entity.* tables name an entity entity.name and its ID id",
		query:  apply(c.q, edits),
	}, len(edits) > 0
}

var countFilterRe = regexp.MustCompile(`\bcount\s*\(\s*filter\s*:`)

// countFilter: DQL counts conditionally with countIf(condition).
func countFilter(c *queryContext) (hint, bool) {
	if c.bareArg(0) != "filter" {
		return hint{}, false
	}
	var edits []edit
	for _, m := range countFilterRe.FindAllStringIndex(c.code, -1) {
		open := strings.IndexByte(c.code[m[0]:m[1]], '(') + m[0]
		closing := c.closingParen(open)
		if closing < 0 || len(c.splitArgs(open, closing)) != 1 {
			return hint{}, false
		}
		cond := strings.TrimSpace(c.q[m[1]:closing])
		edits = append(edits, edit{m[0], closing + 1, "countIf(" + cond + ")"})
	}
	if len(edits) == 0 {
		return hint{}, false
	}
	return hint{reason: "count conditionally with countIf(condition); count() takes no filter", query: apply(c.q, edits)}, true
}

var (
	windowParams  = []string{"from", "to", "timeframe"}
	fetchWindowRe = regexp.MustCompile(`\b(?:from|to|timeframe)\s*:`)
)

// windowOutsideFetch: the window is a parameter of fetch, so it moves there.
func windowOutsideFetch(c *queryContext) (hint, bool) {
	if p := c.bareArg(0); !slices.Contains(windowParams, p) || !wordAt(c.code, c.start, p) || c.depth[c.start] != 0 {
		return hint{}, false
	}
	seg := c.segmentAt(c.start)
	first := c.segs[0]
	if seg == nil || seg == &c.segs[0] || first.cmd != "fetch" || slices.Contains(groupingCommands, seg.cmd) {
		return hint{}, false
	}
	param := strings.TrimSpace(c.q[c.start:min(c.end+1, seg.end)])
	// Drop the parameter with the comma before it.
	cut := c.start
	for cut > seg.start && (c.code[cut-1] == ' ' || c.code[cut-1] == '\t') {
		cut--
	}
	if cut == seg.start || c.code[cut-1] != ',' {
		return hint{}, false
	}
	cut--
	end := c.start + len(param)
	if strings.TrimSpace(c.code[end:seg.end]) != "" && !strings.HasPrefix(strings.TrimSpace(c.code[end:seg.end]), ",") {
		return hint{}, false
	}
	if fetchWindowRe.MatchString(c.code[first.start:first.end]) {
		return hint{}, false // fetch already names a window
	}
	insert := first.start + len(strings.TrimRight(c.q[first.start:first.end], " \t\r\n"))
	return hint{
		reason: "the time window is a parameter of fetch: fetch …, " + param,
		query:  apply(c.q, []edit{{insert, insert, ", " + param}, {cut, end, ""}}),
	}, true
}
