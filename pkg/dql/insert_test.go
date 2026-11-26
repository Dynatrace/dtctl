package dql

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The filters for an entry binding a process group, a service and one
// workload, as reposcope renders them. Spelled out, because this package
// imports nothing that renders them.
const (
	logsExpr  = `in(dt.entity.process_group, array("PROCESS_GROUP-FEDCBA9876543210")) or (k8s.namespace.name == "payments" and k8s.workload.name == "checkout")`
	spansExpr = `in(dt.entity.service, array("SERVICE-0123456789ABCDEF"))`
	logsStage = "| filter (" + logsExpr + ")"
	spanStage = "| filter (" + spansExpr + ")"
	bom       = "\uFEFF"
)

var testFilters = map[string]Filter{
	"logs":  {Expr: logsExpr, Fields: []string{"dt.entity.process_group", "k8s.namespace.name", "k8s.workload.name"}},
	"spans": {Expr: spansExpr, Fields: []string{"dt.entity.service"}},
}

func TestInsertFilter(t *testing.T) {
	tests := []struct {
		name    string
		query   string
		outcome Outcome
		object  string
		want    string // the rewritten query, for applied rows
	}{
		// Applied: the filter goes right after the fetch stage.
		{"bare fetch", "fetch logs", Applied, "logs",
			"fetch logs\n" + logsStage},
		{"fetch with a stage", "fetch logs | limit 5", Applied, "logs",
			"fetch logs " + logsStage + "\n| limit 5"},
		{"fetch parameters", "fetch logs, from:now()-24h, samplingRatio:100 | summarize count()", Applied, "logs",
			"fetch logs, from:now()-24h, samplingRatio:100 " + logsStage + "\n| summarize count()"},
		{"no space around the pipe", "fetch logs|limit 1", Applied, "logs",
			"fetch logs" + logsStage + "\n|limit 1"},
		{"leading whitespace", "  fetch logs", Applied, "logs",
			"  fetch logs\n" + logsStage},
		{"byte order mark", bom + "fetch logs", Applied, "logs",
			bom + "fetch logs\n" + logsStage},
		{"trailing line comment, no pipe", "fetch logs // trailing", Applied, "logs",
			"fetch logs // trailing\n" + logsStage},
		{"line comment before the pipe", "fetch logs // c\n| limit 5", Applied, "logs",
			"fetch logs // c\n" + logsStage + "\n| limit 5"},
		{"leading block comment", "/* c */ fetch logs", Applied, "logs",
			"/* c */ fetch logs\n" + logsStage},
		{"block comment right after the object", "fetch logs/* c */| limit 1", Applied, "logs",
			"fetch logs/* c */" + logsStage + "\n| limit 1"},
		{"a fetch inside a string is text", `fetch spans | filter contains(content, "| fetch logs")`, Applied, "spans",
			"fetch spans " + spanStage + "\n" + `| filter contains(content, "| fetch logs")`},
		{"a bound field inside a string is text", `fetch logs | filter matchesPhrase(content, """he said "dt.entity.process_group" ok""")`, Applied, "logs",
			"fetch logs " + logsStage + "\n" + `| filter matchesPhrase(content, """he said "dt.entity.process_group" ok""")`},
		{"grouping by a bound field is not an override", "fetch logs | summarize count(), by:{k8s.workload.name}", Applied, "logs",
			"fetch logs " + logsStage + "\n| summarize count(), by:{k8s.workload.name}"},
		{"computing from a bound field is not an override", "fetch logs | fieldsAdd x = dt.entity.process_group", Applied, "logs",
			"fetch logs " + logsStage + "\n| fieldsAdd x = dt.entity.process_group"},
		// Only the stages in filteringCommands override; see its comment.
		{"a stage outside filteringCommands is not an override", "fetch logs | dedup k8s.workload.name", Applied, "logs",
			"fetch logs " + logsStage + "\n| dedup k8s.workload.name"},
		{"a longer field is a different field", `fetch logs | filter dt.entity.process_group.name == "x"`, Applied, "logs",
			"fetch logs " + logsStage + "\n" + `| filter dt.entity.process_group.name == "x"`},
		{"a subquery over an unscoped object", "fetch logs | filter x in [fetch dt.system.data_objects | fields name]", Applied, "logs",
			"fetch logs " + logsStage + "\n| filter x in [fetch dt.system.data_objects | fields name]"},
		{"a nested fetch of an unscoped backtick identifier", "fetch logs | append [fetch `logs archive`]", Applied, "logs",
			"fetch logs " + logsStage + "\n| append [fetch `logs archive`]"},
		{"a field named fetch inside brackets", "fetch logs | filter (fetch == logs)", Applied, "logs",
			"fetch logs " + logsStage + "\n| filter (fetch == logs)"},
		// An identifier escapes its backticks, so what follows one is still
		// inside it: neither a stage nor a filter.
		{"an escaped backtick in an identifier", "fetch logs | fields `field\\`name`", Applied, "logs",
			"fetch logs " + logsStage + "\n| fields `field\\`name`"},
		{"a filter spelled inside an identifier is text", "fetch logs | fields `field\\` | filter k8s.workload.name \\`name`", Applied, "logs",
			"fetch logs " + logsStage + "\n| fields `field\\` | filter k8s.workload.name \\`name`"},
		{"a # inside a string is text", `fetch logs | filter content == "#1 | fetch logs"`, Applied, "logs",
			"fetch logs " + logsStage + "\n" + `| filter content == "#1 | fetch logs"`},
		{"a # inside a comment is text", "fetch logs // #1\n| limit 1", Applied, "logs",
			"fetch logs // #1\n" + logsStage + "\n| limit 1"},
		{"CRLF line endings are kept", "fetch logs\r\n| limit 1", Applied, "logs",
			"fetch logs\r\n" + logsStage + "\n| limit 1"},
		// Bytes that are not UTF-8 are Grail's to reject. The rewrite reads
		// bytes, so offsets stay offsets whatever the text holds.
		{"invalid UTF-8 before a word the subquery check reads", "fetch logs \xff\xff\xff fetch", Applied, "logs",
			"fetch logs \xff\xff\xff fetch\n" + logsStage},
		{"invalid UTF-8 inside a subquery", "fetch logs | append [\xff\xfe fetch\xff bizevents]", Applied, "logs",
			"fetch logs " + logsStage + "\n| append [\xff\xfe fetch\xff bizevents]"},

		// Grail reads command and data object names without regard to case,
		// so `fetch Spans` is the scoped object, reported under its own name.
		{"a capitalised object", "fetch Spans | limit 5", Applied, "spans",
			"fetch Spans " + spanStage + "\n| limit 5"},
		{"an upper-case fetch", "FETCH LOGS", Applied, "logs",
			"FETCH LOGS\n" + logsStage},

		// The user's own filter on a bound field wins.
		{"filter on a bound field", `fetch logs | filter k8s.namespace.name == "x"`, UserFilter, "logs", ""},
		{"filterOut on a bound field", `fetch logs | filterOut dt.entity.process_group == "x"`, UserFilter, "logs", ""},
		{"a backtick-quoted bound field", "fetch logs | filter `k8s.workload.name` == \"x\"", UserFilter, "logs", ""},
		{"a filter between identifiers ending in escaped backticks", "fetch logs | fields `a\\`` | filter k8s.workload.name == \"x\" | fields `b\\``", UserFilter, "logs", ""},
		{"a comment before the filter command", `fetch logs | /* c */ filter k8s.workload.name == "x"`, UserFilter, "logs", ""},
		{"a capitalised filter command", `fetch logs | Filter k8s.workload.name == "x"`, UserFilter, "logs", ""},
		{"an upper-case filterOut", `fetch logs | FILTEROUT k8s.workload.name == "x"`, UserFilter, "logs", ""},
		// Field names, unlike commands, are case-sensitive: this is another field.
		{"a bound field in another case", `fetch logs | filter K8S.Workload.Name == "x"`, Applied, "logs",
			"fetch logs " + logsStage + "\n" + `| filter K8S.Workload.Name == "x"`},

		// A scoped object fetched again in a subquery.
		{"subquery in in()", "fetch logs | filter in(x, [fetch spans | fields id])", Subquery, "logs", ""},
		{"subquery in append", `fetch logs | append [fetch logs | filter loglevel == "ERROR"]`, Subquery, "logs", ""},
		{"a comment between fetch and the object", "fetch logs | join [fetch /*x*/ logs], on:{a}", Subquery, "logs", ""},
		{"a subquery in another case", "fetch logs | append [Fetch Spans | limit 1]", Subquery, "logs", ""},
		// What separates a nested fetch from its object is Grail's to define.
		// Anything that is not printable ASCII may be white space to it, and
		// missing the subquery would report environment-wide rows as scoped.
		{"a form feed after a nested fetch", "fetch logs | append [fetch\flogs]", Subquery, "logs", ""},
		{"a vertical tab after a nested fetch", "fetch logs | append [fetch\vspans]", Subquery, "logs", ""},
		{"a no-break space after a nested fetch", "fetch logs | append [fetch\u00a0logs]", Subquery, "logs", ""},
		{"a line separator after a nested fetch", "fetch logs | append [fetch\u2028logs | limit 1]", Subquery, "logs", ""},
		{"a nested object as a backtick identifier", "fetch logs | append [fetch `logs`]", Subquery, "logs", ""},
		{"a nested object as a backtick identifier in another case", "fetch logs | append [fetch /* c */ `Spans`]", Subquery, "logs", ""},

		// Not a leading fetch.
		{"timeseries", "timeseries avg(x)", NotFetch, "", ""},
		{"smartscape", "smartscapeNodes SERVICE", NotFetch, "", ""},
		{"bracketed fetch", "(fetch logs)", NotFetch, "", ""},
		{"fetch glued to its object", "fetchlogs", NotFetch, "", ""},
		{"an object name that does not end cleanly", "fetch logs-archive", NotFetch, "", ""},
		{"empty query", "", NotFetch, "", ""},

		// Objects without a filter.
		{"events", "fetch events", UnsupportedObject, "events", ""},
		{"an entity table", "fetch dt.entity.service", UnsupportedObject, "dt.entity.service", ""},
		{"an unscoped object keeps its spelling", "fetch BizEvents", UnsupportedObject, "BizEvents", ""},
		{"a name that only starts like a scoped one", "fetch logs_archive", UnsupportedObject, "logs_archive", ""},

		// Text a lexical rewrite must not touch.
		{"unterminated string", `fetch logs | filter content == "unterminated`, Unscannable, "", ""},
		{"unterminated comment", "fetch logs /* x", Unscannable, "", ""},
		{"unterminated identifier", "fetch logs | fields `x", Unscannable, "", ""},
		{"unbalanced bracket", "fetch logs | filter (a == 1", Unscannable, "", ""},
		{"single quote", "fetch logs | filter it's", Unscannable, "", ""},
		// A # outside a literal is the byte the scan masks with, so the scan
		// could not tell it from a comment: the text after it would be skipped.
		{"a # glued to the object", "fetch logs#| limit 1", Unscannable, "", ""},
		{"a # before the object", "fetch # logs", Unscannable, "", ""},
		{"a # in a later stage", "fetch logs | limit 1 #", Unscannable, "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := InsertFilter(tt.query, testFilters)
			assert.Equal(t, tt.outcome, got.Outcome)
			assert.Equal(t, tt.object, got.DataObject)
			if tt.outcome != Applied {
				assert.Equal(t, tt.query, got.Query, "a refusal sends the input byte for byte")
				assert.NotEmpty(t, got.Reason)
				assert.Empty(t, got.Filter)
				return
			}
			assert.Equal(t, tt.want, got.Query)
			assertOnlyInserted(t, tt.query, got.Query)
			assert.Equal(t, testFilters[tt.object].Expr, got.Filter)
			assert.Empty(t, got.Reason)
		})
	}
}

// assertOnlyInserted checks that got is input with one run of bytes added.
func assertOnlyInserted(t *testing.T, input, got string) {
	t.Helper()
	require.Greater(t, len(got), len(input))
	at := 0
	for at < len(input) && input[at] == got[at] {
		at++
	}
	added := len(got) - len(input)
	assert.Equal(t, input, got[:at]+got[at+added:])
}

// Only a parameter of the fetch stage itself bounds the scan, and only with
// a value that does: the word in a comment, a literal or a later stage does
// not, nor does a sampling ratio of 1 or a scan limit of 0, and
// scanLimitGBytes: -1 lifts the limit.
func TestInsertFilter_ScanBounded(t *testing.T) {
	tests := []struct {
		query           string
		bounded, lifted bool
	}{
		{"fetch logs | limit 5", false, false},
		{"fetch logs,\n  scanLimitGBytes: 10 | limit 5", true, false},
		{"fetch logs, scanLimitGBytes:0.5", true, false},
		{"fetch spans, samplingRatio: 100", true, false},
		{"fetch logs /* scanLimitGBytes: 10 */ | limit 5", false, false},
		{`fetch logs | filter content == "samplingRatio: 100"`, false, false},
		{"fetch logs | fieldsAdd scanLimitGBytes = 1", false, false},
		{"fetch logs, scanLimitGBytes: -1 | limit 5", false, true},
		// A value that bounds nothing.
		{"fetch spans, samplingRatio: 1", false, false},
		{"fetch spans, samplingRatio: 1.0", false, false},
		{"fetch logs, scanLimitGBytes: 0", false, false},
		// A comment between the name and the value hides neither.
		{"fetch logs, scanLimitGBytes:/*x*/-1", false, true},
		{"fetch logs, scanLimitGBytes /*x*/ : // y\n 10", true, false},
		// Grail reads names without regard to case; whether that holds for
		// parameter names is not verified, and reading them so costs nothing.
		{"fetch logs, scanlimitgbytes: 10", true, false},
		{"fetch logs, SamplingRatio: 1", false, false},
		// A value dtctl cannot read may bound the scan, so nothing is said.
		{"fetch logs, scanLimitGBytes: $limit", true, false},
		{"fetch logs, samplingRatio: toLong(100)", true, false},
		{"fetch logs, samplingRatio: 1e5", true, false},
		// Each parameter is judged by itself.
		{"fetch logs, scanLimitGBytes: -1, samplingRatio: 100", true, true},
		{"fetch logs, myscanLimitGBytes: 10", false, false},
	}
	for _, tt := range tests {
		t.Run(tt.query, func(t *testing.T) {
			got := InsertFilter(tt.query, testFilters)
			require.Equal(t, Applied, got.Outcome)
			assert.Equal(t, tt.bounded, got.ScanBounded, "bounded")
			assert.Equal(t, tt.lifted, got.ScanLimitLifted, "lifted")
		})
	}
}

func TestInsertFilter_Reasons(t *testing.T) {
	tests := []struct {
		query  string
		reason string
	}{
		{"fetch events", "repo scope has no filter for events (it filters logs, spans)"},
		{`fetch logs | filter k8s.namespace.name == "x"`, "the query filters on k8s.namespace.name itself, and its own filter wins"},
		{"fetch logs | filter in(x, [fetch spans | fields id])", "the query fetches spans again in a subquery, which the scope cannot reach"},
		{"timeseries avg(x)", "the query does not start with 'fetch <data object>'"},
	}
	for _, tt := range tests {
		t.Run(tt.query, func(t *testing.T) {
			assert.Equal(t, tt.reason, InsertFilter(tt.query, testFilters).Reason)
		})
	}
}

func TestInsertFilter_ObjectWithoutCondition(t *testing.T) {
	// Spans are scoped but this entry has no condition for them: a spans
	// query is refused, and a spans subquery under fetch logs is still one
	// the insertion cannot reach.
	filters := map[string]Filter{"logs": testFilters["logs"], "spans": {}}

	assert.Equal(t, UnsupportedObject, InsertFilter("fetch spans", filters).Outcome)
	assert.Equal(t, Subquery, InsertFilter("fetch logs | append [fetch spans]", filters).Outcome)
	assert.Equal(t, "repo scope has no filter for spans (it filters logs)", InsertFilter("fetch spans", filters).Reason)
	assert.Equal(t, UnsupportedObject, InsertFilter("fetch logs", nil).Outcome)
}
