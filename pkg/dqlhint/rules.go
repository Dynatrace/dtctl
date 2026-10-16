package dqlhint

import (
	"fmt"
	"regexp"
	"slices"
	"strings"
)

// rule is one known DQL authoring trap: the Grail error types it answers, and
// a matcher that confirms the trap in the query text and builds the fix.
type rule struct {
	name       string
	errorTypes []string
	apply      func(c *queryContext) (hint, bool)
}

func (r rule) matchesType(t string) bool {
	return slices.Contains(r.errorTypes, t)
}

// rules is the known-trap table, drawn from the most frequent DQL authoring
// errors agents make. Every entry has positive and negative fixtures in
// rules_test.go; add both when adding a rule.
var rules = []rule{
	{"filter-equals", []string{"MANDATORY_PARAMETER_HAS_TO_BE_BUT_WAS", "PARSE_ERROR"}, filterEquals},
	{"by-parameter", []string{"PARSE_ERROR"}, byParameter},
	{"colon-assignment", []string{"UNKNOWN_PARAMETER_DEFINED"}, colonAssignment},
	{"array-contains", []string{"UNKNOWN_FUNCTION"}, arrayContains},
	{"rollup-duration", []string{"NAMED_PARAMETER_HAS_TO_BE"}, rollupDuration},
	{"timeseries-condition", []string{"MANDATORY_PARAMETER_HAS_TO_BE"}, timeseriesCondition},
	{"timeseries-aggregation", []string{"MANDATORY_PARAMETER_HAS_TO_BE"}, timeseriesAggregation},
	{"entity-table", []string{"UNKNOWN_DATA_OBJECT"}, entityTable},
	{"sql-aggregation", []string{"PARSE_ERROR", "UNKNOWN_COMMAND"}, sqlAggregation},
	{"single-quotes", []string{"PARSE_ERROR_SINGLE_QUOTES"}, singleQuotes},
	{"function-synonym", []string{"UNKNOWN_FUNCTION"}, functionSynonym},
	{"aggregation-reference", []string{"PARAMETER_MUST_NOT_BE_AN_AGGREGATION"}, aggregationReference},
	{"entity-field", []string{"FIELD_DOES_NOT_EXIST"}, entityField},
	{"count-filter", []string{"UNKNOWN_PARAMETER_DEFINED"}, countFilter},
	{"window-outside-fetch", []string{"UNKNOWN_PARAMETER_DEFINED"}, windowOutsideFetch},
	{"command-synonym", []string{"UNKNOWN_COMMAND"}, commandSynonym},
}

// filterEquals: `filter x = "v"` — a single = is not a comparison in DQL.
// Grail reports it as a non-boolean filter condition or, after a boolean
// operator, as an unexpected `=`. Every bare = in every filter command is
// rewritten, so the agent does not hit the next one on the retry.
func filterEquals(c *queryContext) (hint, bool) {
	if !(c.arg(0) == "a boolean" || c.arg(0) == "`=`") {
		return hint{}, false
	}
	if seg := c.segmentAt(c.start); seg == nil || !isFilterCommand(seg.cmd) {
		return hint{}, false
	}
	var edits []edit
	for _, seg := range c.segs {
		if !isFilterCommand(seg.cmd) {
			continue
		}
		for i := seg.start; i < seg.end; i++ {
			if c.code[i] != '=' {
				continue
			}
			prev, next := byte(0), byte(0)
			if i > 0 {
				prev = c.code[i-1]
			}
			if i+1 < len(c.code) {
				next = c.code[i+1]
			}
			if next != '=' && !strings.ContainsRune("=!<>", rune(prev)) {
				edits = append(edits, edit{i, i + 1, "=="})
			}
		}
	}
	if len(edits) == 0 {
		return hint{}, false
	}
	return hint{reason: "DQL compares with ==, a single = is not a comparison", query: apply(c.q, edits)}, true
}

func isFilterCommand(cmd string) bool { return cmd == "filter" || cmd == "filterOut" }

// groupingCommands take their grouping fields as the by: parameter.
var groupingCommands = []string{"summarize", "makeTimeseries", "timeseries"}

var (
	fieldName     = "(?:[A-Za-z_][\\w.]*|`[^`]+`)"
	fieldListRe   = regexp.MustCompile(`^` + fieldName + `(?:\s*,\s*` + fieldName + `)*$`)
	fieldSplitRe  = regexp.MustCompile(`\s*,\s*`)
	trailingSpace = regexp.MustCompile(`\s*$`)
)

// byParameter: `summarize count() by field` — grouping is the by: parameter,
// not a keyword. Fires only when the text after `by` is a plain field list.
func byParameter(c *queryContext) (hint, bool) {
	if c.arg(0) != "`by`" || !wordAt(c.code, c.start, "by") || c.depth[c.start] != 0 {
		return hint{}, false
	}
	seg := c.segmentAt(c.start)
	if seg == nil || !slices.Contains(groupingCommands, seg.cmd) {
		return hint{}, false
	}
	rest := strings.TrimSpace(c.q[c.start+len("by") : seg.end])
	rest = strings.TrimSpace(strings.TrimPrefix(rest, ":"))
	if strings.HasPrefix(rest, "{") && strings.HasSuffix(rest, "}") {
		rest = strings.TrimSpace(rest[1 : len(rest)-1])
	}
	if !fieldListRe.MatchString(rest) {
		return hint{}, false
	}
	head := strings.TrimRight(c.q[seg.start:c.start], " \t\r\n")
	sep := ", "
	if strings.HasSuffix(head, ",") {
		sep = " "
	}
	tail := trailingSpace.FindString(c.q[seg.start:seg.end])
	fields := strings.Join(fieldSplitRe.Split(rest, -1), ", ")
	return hint{
		reason: "group with the by: parameter, by:{field, …}, not a trailing by keyword",
		query:  apply(c.q, []edit{{seg.start, seg.end, head + sep + "by:{" + fields + "}" + tail}}),
	}, true
}

// assigningCommands name their output fields with `name = expression`.
var assigningCommands = []string{"summarize", "fieldsAdd", "fields", "makeTimeseries", "timeseries"}

// knownParameters are real named parameters of the assigning commands; a
// `name:` using one of them is never rewritten into an assignment.
var knownParameters = []string{
	"by", "filter", "from", "to", "timeframe", "interval", "bins", "shift",
	"nonempty", "union", "rollup", "time", "spread", "scanLimitGBytes", "samplingRatio",
}

var namedArgRe = regexp.MustCompile(`([A-Za-z_][\w.]*)\s*:`)

// colonAssignment: `summarize name: countIf(…)` — a field is named with =,
// the colon form is read as a named parameter that does not exist. Every
// top-level `name: function(` in the command is rewritten together.
func colonAssignment(c *queryContext) (hint, bool) {
	name := c.arg(0)
	if name == "" || slices.Contains(knownParameters, name) || !wordAt(c.code, c.start, name) || c.depth[c.start] != 0 {
		return hint{}, false
	}
	seg := c.segmentAt(c.start)
	if seg == nil || !slices.Contains(assigningCommands, seg.cmd) {
		return hint{}, false
	}
	var edits []edit
	for _, m := range namedArgRe.FindAllStringSubmatchIndex(c.code[seg.start:seg.end], -1) {
		ks, ke, colon := seg.start+m[2], seg.start+m[3], seg.start+m[1]
		key := c.q[ks:ke]
		if c.depth[ks] != 0 || (ks > 0 && isIdentByte(c.code[ks-1])) {
			continue
		}
		if ks != c.start && (slices.Contains(knownParameters, key) || !callFollows(c.code[colon:])) {
			continue
		}
		edits = append(edits, edit{ks, colon, key + " ="})
	}
	if len(edits) == 0 {
		return hint{}, false
	}
	return hint{reason: "name a field with =, name = aggregation(…); name: is read as a parameter", query: apply(c.q, edits)}, true
}

var callRe = regexp.MustCompile(`^\s*[A-Za-z_][\w.]*\s*\(`)

func callFollows(s string) bool { return callRe.MatchString(s) }

// arrayMembership are function names agents invent for "array contains value".
var arrayMembership = []string{"array.contains", "arrayContains", "array_contains"}

var arrayMembershipRe = regexp.MustCompile(`(array\.contains|arrayContains|array_contains)\s*\(`)

// arrayContains: `array.contains(arr, v)` — DQL tests membership with
// in(v, arr). Every such call is rewritten; any call that is not a clean
// two-argument form leaves the whole query alone.
func arrayContains(c *queryContext) (hint, bool) {
	if !slices.Contains(arrayMembership, c.arg(0)) || !wordAt(c.code, c.start, c.arg(0)) {
		return hint{}, false
	}
	var edits []edit
	for _, m := range arrayMembershipRe.FindAllStringSubmatchIndex(c.code, -1) {
		if !wordAt(c.code, m[2], c.code[m[2]:m[3]]) {
			continue
		}
		// A call nested in another's arguments would need the outer rewrite
		// to carry the inner one; leave such queries alone.
		if len(edits) > 0 && m[2] < edits[len(edits)-1].end {
			return hint{}, false
		}
		open := m[1] - 1
		closing := c.closingParen(open)
		if closing < 0 {
			return hint{}, false
		}
		args := c.splitArgs(open, closing)
		if len(args) != 2 || args[0] == "" || args[1] == "" {
			return hint{}, false
		}
		edits = append(edits, edit{m[0], closing + 1, "in(" + args[1] + ", " + args[0] + ")"})
	}
	if len(edits) == 0 {
		return hint{}, false
	}
	return hint{reason: "DQL has no " + c.arg(0) + ", test array membership with in(value, array)", query: apply(c.q, edits)}, true
}

var (
	rollupRe   = regexp.MustCompile(`,?\s*\brollup\s*:\s*([^\s,)]+)`)
	durationRe = regexp.MustCompile(`^\d+(?:ns|us|ms|s|m|h|d|w)$`)
	intervalRe = regexp.MustCompile(`\binterval\s*:`)
)

// rollupDuration: `timeseries …, rollup: 5m` — rollup takes an aggregation
// (min, max, sum, avg, total); the bucket size is interval:.
func rollupDuration(c *queryContext) (hint, bool) {
	if c.arg(0) != "rollup" {
		return hint{}, false
	}
	seg := c.segmentAt(c.start)
	if seg == nil || seg.cmd != "timeseries" || intervalRe.MatchString(c.code[seg.start:seg.end]) {
		return hint{}, false
	}
	for _, m := range rollupRe.FindAllStringSubmatchIndex(c.code[seg.start:seg.end], -1) {
		from, to := seg.start+m[0], seg.start+m[1]
		vs, ve := seg.start+m[2], seg.start+m[3]
		if c.start < vs || c.start >= ve || !durationRe.MatchString(c.code[vs:ve]) {
			continue
		}
		value := c.q[vs:ve]
		reason := "rollup takes an aggregation (min, max, sum, avg, total), set the bucket size with interval"
		key := strings.Index(c.code[from:to], "rollup") + from
		if c.depth[key] == 0 {
			return hint{reason: reason, query: apply(c.q, []edit{{key, key + len("rollup"), "interval"}})}, true
		}
		body := strings.TrimRight(c.q[seg.start:seg.end], " \t\r\n")
		tail := c.q[seg.start+len(body) : seg.end]
		fixed := c.q[seg.start:from] + c.q[to:seg.start+len(body)] + ", interval: " + value + tail
		return hint{reason: reason, query: apply(c.q, []edit{{seg.start, seg.end, fixed}})}, true
	}
	return hint{}, false
}

const metricAggregationArg = "a metric-based timeseries aggregation"

var (
	comparisonRe = regexp.MustCompile(`^[A-Za-z_][\w.]*\s*(?:==|!=|<=|>=|<|>)\s*\S`)
	filterParam  = regexp.MustCompile(`\bfilter\s*:`)
)

// timeseriesCondition: `timeseries avg(m), dt.entity.host == "HOST-1"` — a
// positional condition is read as another aggregation; conditions go in
// filter:.
func timeseriesCondition(c *queryContext) (hint, bool) {
	if c.arg(0) != metricAggregationArg {
		return hint{}, false
	}
	seg := c.segmentAt(c.start)
	if seg == nil || seg.cmd != "timeseries" || c.depth[c.start] != 0 ||
		filterParam.MatchString(c.code[seg.start:seg.end]) {
		return hint{}, false
	}
	text := c.code[c.start : c.end+1]
	if !comparisonRe.MatchString(text) || strings.Contains(strings.NewReplacer("==", "", "!=", "", "<=", "", ">=", "").Replace(text), "=") {
		return hint{}, false
	}
	if before := strings.TrimRight(c.code[seg.start:c.start], " \t\r\n"); !strings.HasSuffix(before, ",") {
		return hint{}, false
	}
	return hint{
		reason: "timeseries takes conditions as the filter: parameter",
		query:  apply(c.q, []edit{{c.start, c.start, "filter: "}}),
	}, true
}

// timeseriesAggregations are the functions timeseries accepts over a metric.
var timeseriesAggregations = []string{"avg", "sum", "min", "max", "count", "countDistinct", "median", "percentile"}

var assignedNameRe = regexp.MustCompile(`([A-Za-z_][\w.]*)\s*=\s*$`)

var aggregationRe = regexp.MustCompile(`^(?:([A-Za-z_][\w.]*)\s*=\s*)?([A-Za-z_]\w*)\s*\(\s*([A-Za-z_][\w.]*)\s*[,)]`)

// timeseriesAggregation: `timeseries x = last(field)` — timeseries reads
// metrics only, through its own aggregation functions. Whether the field is a
// metric key or a record field decides the fix, and the error does not say, so
// this is advice, not a rewrite.
func timeseriesAggregation(c *queryContext) (hint, bool) {
	if c.arg(0) != metricAggregationArg {
		return hint{}, false
	}
	seg := c.segmentAt(c.start)
	if seg == nil || seg.cmd != "timeseries" || c.depth[c.start] != 0 {
		return hint{}, false
	}
	m := aggregationRe.FindStringSubmatch(c.q[c.start : c.end+1])
	if m == nil || slices.Contains(timeseriesAggregations, m[2]) {
		return hint{}, false
	}
	name, fn, field := m[1], m[2], m[3]
	if name == "" {
		// Grail's span starts at the function; the field name, if any, is
		// just before it.
		if n := assignedNameRe.FindStringSubmatch(c.code[seg.start:c.start]); n != nil {
			name = n[1]
		}
	}
	example := "timeseries avg(" + field + ")"
	if name != "" {
		example = "timeseries " + name + " = avg(" + field + ")"
	}
	return hint{advice: fmt.Sprintf(
		"%s() is not a timeseries aggregation: timeseries reads metric keys only, with %s. "+
			"If %s is a metric key, pick one of those, e.g. %s. "+
			"If it is a field of logs, spans or events it is not a metric: chart the records instead, "+
			"e.g. fetch logs | makeTimeseries count(), by:{%s}",
		fn, strings.Join(timeseriesAggregations, ", "), field, example, field)}, true
}

// entitySmartscapeTypes maps the classic entity tables whose Smartscape node
// type is known to be a plain rename. Other tables stay unmapped rather than
// guessed.
var entitySmartscapeTypes = map[string]string{
	"dt.entity.service": "SERVICE",
	"dt.entity.host":    "HOST",
}

var (
	bareFetchRe  = regexp.MustCompile(`^\s*fetch\s+(dt\.entity\.[a-z_]+)\s*$`)
	entityNameRe = regexp.MustCompile(`entity\.name`)
)

// entityTable: `fetch dt.entity.service` on an environment where the classic
// entity tables are not data objects — the topology lives in Smartscape, whose
// nodes carry the name as name rather than entity.name.
func entityTable(c *queryContext) (hint, bool) {
	first := c.segs[0]
	m := bareFetchRe.FindStringSubmatch(c.code[first.start:first.end])
	if m == nil || m[1] != c.arg(0) || c.segmentAt(c.start) != &c.segs[0] {
		return hint{}, false
	}
	nodeType, ok := entitySmartscapeTypes[m[1]]
	if !ok {
		return hint{}, false
	}
	seg := c.q[first.start:first.end]
	lead := seg[:len(seg)-len(strings.TrimLeft(seg, " \t\r\n"))]
	edits := []edit{{first.start, first.end, lead + `smartscapeNodes "` + nodeType + `"` + trailingSpace.FindString(seg)}}
	for _, loc := range entityNameRe.FindAllStringIndex(c.code[first.end:], -1) {
		s := first.end + loc[0]
		if wordAt(c.code, s, "entity.name") {
			edits = append(edits, edit{s, s + len("entity.name"), "name"})
		}
	}
	return hint{
		reason: m[1] + " is not a data object here, read the topology with smartscapeNodes (entity.name is name there)",
		query:  apply(c.q, edits),
	}, true
}
