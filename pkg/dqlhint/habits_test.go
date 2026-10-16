package dqlhint

import "testing"

// Fixtures are error payloads captured from a live environment; each corrected query ran successfully.
func TestSuggest_HabitRewrites(t *testing.T) {
	tests := []struct {
		name  string
		err   Error
		want  string
		match string
	}{
		{
			name: "stats … by",
			err: Error{Type: "PARSE_ERROR", Arguments: []string{"`by`"},
				Query: "fetch logs | stats count() by log.source", Span: span(28, 29)},
			want:  "fetch logs | summarize count(), by:{log.source}",
			match: "sql-aggregation",
		},
		{
			name: "stats … as … by, fixed in one go",
			err: Error{Type: "PARSE_ERROR", Arguments: []string{"`as`"},
				Query: "fetch logs | stats count() as n by log.source | sort n desc", Span: span(28, 29)},
			want:  "fetch logs | summarize n = count(), by:{log.source} | sort n desc",
			match: "sql-aggregation",
		},
		{
			name: "as alias in summarize",
			err: Error{Type: "PARSE_ERROR", Arguments: []string{"`as`"},
				Query: "fetch logs | summarize count() as n, by:{log.source}", Span: span(32, 33)},
			want:  "fetch logs | summarize n = count(), by:{log.source}",
			match: "sql-aggregation",
		},
		{
			name: "stats without grouping",
			err: Error{Type: "UNKNOWN_COMMAND", Arguments: []string{"stats"},
				Query: "fetch logs | stats count()", Span: span(14, 18)},
			want:  "fetch logs | summarize count()",
			match: "sql-aggregation",
		},
		{
			name: "single-quoted string",
			err: Error{Type: "PARSE_ERROR_SINGLE_QUOTES",
				Query: "fetch logs | filter loglevel == 'ERROR' | limit 1", Span: span(33, 39)},
			want:  `fetch logs | filter loglevel == "ERROR" | limit 1`,
			match: "single-quotes",
		},
		{
			name: "single quotes with an SQL-escaped quote",
			err: Error{Type: "PARSE_ERROR_SINGLE_QUOTES",
				Query: "fetch logs | filter loglevel == 'ERROR' and content != 'it''s'", Span: span(60, 62)},
			want:  `fetch logs | filter loglevel == "ERROR" and content != "it's"`,
			match: "single-quotes",
		},
		{
			name: "toLower around the subject of contains",
			err: Error{Type: "UNKNOWN_FUNCTION", Arguments: []string{"toLower"},
				Query: `fetch logs | filter contains(toLower(content), "x") | limit 1`, Span: span(30, 36)},
			want:  `fetch logs | filter contains(content, "x", caseSensitive: false) | limit 1`,
			match: "function-synonym",
		},
		{
			name: "toLower outside a filter",
			err: Error{Type: "UNKNOWN_FUNCTION", Arguments: []string{"toLower"},
				Query: "fetch logs | fieldsAdd s = toLower(log.source)", Span: span(28, 34)},
			want:  "fetch logs | fieldsAdd s = lower(log.source)",
			match: "function-synonym",
		},
		{
			name: "tonumber",
			err: Error{Type: "UNKNOWN_FUNCTION", Arguments: []string{"tonumber"},
				Query: `fetch logs | fieldsAdd d = tonumber("1") | limit 1`, Span: span(28, 35)},
			want:  `fetch logs | fieldsAdd d = toDouble("1") | limit 1`,
			match: "function-synonym",
		},
		{
			name: "unnamed aggregation referenced bare after summarize",
			err: Error{Type: "PARAMETER_MUST_NOT_BE_AN_AGGREGATION", Arguments: []string{"count()"},
				Query: "fetch logs | summarize count(), by:{log.source} | sort count() desc", Span: span(56, 62)},
			want:  "fetch logs | summarize count(), by:{log.source} | sort `count()` desc",
			match: "aggregation-reference",
		},
		{
			name: "name on an entity table",
			err: Error{Type: "FIELD_DOES_NOT_EXIST", Arguments: []string{"name"},
				Query: "fetch dt.entity.service | fields name | limit 1", Span: span(34, 37)},
			want:  "fetch dt.entity.service | fields entity.name | limit 1",
			match: "entity-field",
		},
		{
			name: "entity.id on an entity table",
			err: Error{Type: "FIELD_DOES_NOT_EXIST", Arguments: []string{"entity.id"},
				Query: "fetch dt.entity.service | fields entity.name, entity.id | limit 1", Span: span(47, 55)},
			want:  "fetch dt.entity.service | fields entity.name, id | limit 1",
			match: "entity-field",
		},
		{
			name: "display_name used twice",
			err: Error{Type: "FIELD_DOES_NOT_EXIST", Arguments: []string{"display_name"},
				Query: `fetch dt.entity.service | filter display_name == "x" | fields display_name`, Span: span(34, 45)},
			want:  `fetch dt.entity.service | filter entity.name == "x" | fields entity.name`,
			match: "entity-field",
		},
		{
			name: "count(filter: …)",
			err: Error{Type: "UNKNOWN_PARAMETER_DEFINED", Arguments: []string{"filter"},
				Query: "fetch spans | summarize failed = count(filter: request.is_failed == true)", Span: span(40, 72)},
			want:  "fetch spans | summarize failed = countIf(request.is_failed == true)",
			match: "count-filter",
		},
		{
			name: "from: on filter",
			err: Error{Type: "UNKNOWN_PARAMETER_DEFINED", Arguments: []string{"from"},
				Query: `fetch logs | filter loglevel == "ERROR", from:now()-1d | limit 1`, Span: span(42, 54)},
			want:  `fetch logs, from:now()-1d | filter loglevel == "ERROR" | limit 1`,
			match: "window-outside-fetch",
		},
		{
			name: "stats with a SQL aggregation name, referenced bare later",
			err: Error{Type: "PARSE_ERROR", Arguments: []string{"`by`"},
				Query: "fetch logs | stats count_distinct(log.source) by loglevel | sort count_distinct(log.source) desc | limit 1", Span: span(47, 48)},
			want:  "fetch logs | summarize countDistinct(log.source), by:{loglevel} | sort `countDistinct(log.source)` desc | limit 1",
			match: "sql-aggregation",
		},
		{
			name: "head",
			err: Error{Type: "UNKNOWN_COMMAND", Arguments: []string{"head"},
				Query: "fetch logs | head 3", Span: span(14, 17)},
			want:  "fetch logs | limit 3",
			match: "command-synonym",
		},
		{
			name: "distinct, with a later head",
			err: Error{Type: "UNKNOWN_COMMAND", Arguments: []string{"distinct"},
				Query: "fetch logs | distinct log.source | head 5", Span: span(14, 21)},
			want:  "fetch logs | summarize count(), by:{log.source} | limit 5",
			match: "command-synonym",
		},
		{
			name: "bare count",
			err: Error{Type: "UNKNOWN_COMMAND", Arguments: []string{"count"},
				Query: "fetch logs | count", Span: span(14, 18)},
			want:  "fetch logs | summarize count()",
			match: "command-synonym",
		},
		{
			name: "KQL where and project",
			err: Error{Type: "UNKNOWN_COMMAND", Arguments: []string{"where"},
				Query: `fetch logs | where loglevel == "ERROR" | project content | head 1`, Span: span(14, 18)},
			want:  `fetch logs | filter loglevel == "ERROR" | fields content | limit 1`,
			match: "command-synonym",
		},
		{
			name: "eval",
			err: Error{Type: "UNKNOWN_COMMAND", Arguments: []string{"eval"},
				Query: "fetch logs | eval x = 1 | limit 1", Span: span(14, 17)},
			want:  "fetch logs | fieldsAdd x = 1 | limit 1",
			match: "command-synonym",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			hs := hints(tt.err)
			if len(hs) != 1 {
				t.Fatalf("hints = %+v, want exactly one", hs)
			}
			if hs[0].rule != tt.match {
				t.Errorf("rule = %s, want %s", hs[0].rule, tt.match)
			}
			if hs[0].query != tt.want {
				t.Errorf("query =\n  %s\nwant\n  %s", hs[0].query, tt.want)
			}
		})
	}
}

func TestSuggest_HabitsConservative(t *testing.T) {
	tests := []struct {
		name string
		err  Error
	}{
		{"as followed by an expression", Error{Type: "PARSE_ERROR", Arguments: []string{"`as`"},
			Query: "fetch logs | summarize count() as n + 1", Span: span(32, 33)}},
		{"stats by with an expression", Error{Type: "PARSE_ERROR", Arguments: []string{"`by`"},
			Query: "fetch logs | stats count() by lower(log.source)", Span: span(28, 29)}},
		{"case conversion compared in a filter", Error{Type: "UNKNOWN_FUNCTION", Arguments: []string{"toLower"},
			Query: `fetch logs | filter toLower(loglevel) == "error"`, Span: span(21, 27)}},
		{"unknown function with no known synonym", Error{Type: "UNKNOWN_FUNCTION", Arguments: []string{"frobnicate"},
			Query: "fetch logs | fieldsAdd x = frobnicate(content)", Span: span(28, 37)}},
		{"aggregation already named in summarize", Error{Type: "PARAMETER_MUST_NOT_BE_AN_AGGREGATION", Arguments: []string{"count()"},
			Query: "fetch logs | summarize n = count() | sort count() desc", Span: span(43, 49)}},
		{"guessed field on a table that is not an entity table", Error{Type: "FIELD_DOES_NOT_EXIST", Arguments: []string{"name"},
			Query: "fetch logs | fields name", Span: span(21, 24)}},
		{"window on filter when fetch already has one", Error{Type: "UNKNOWN_PARAMETER_DEFINED", Arguments: []string{"from"},
			Query: `fetch logs, from:now()-2h | filter x == 1, from:now()-1d`, Span: span(44, 56)}},
		{"count with an argument and a filter", Error{Type: "UNKNOWN_PARAMETER_DEFINED", Arguments: []string{"filter"},
			Query: "fetch spans | summarize n = count(x, filter: y)", Span: span(38, 46)}},
		{"distinct over an expression", Error{Type: "UNKNOWN_COMMAND", Arguments: []string{"distinct"},
			Query: "fetch logs | distinct lower(log.source)", Span: span(14, 21)}},
		{"count with arguments", Error{Type: "UNKNOWN_COMMAND", Arguments: []string{"count"},
			Query: "fetch logs | count by log.source", Span: span(14, 18)}},
		{"an unknown command with no DQL synonym", Error{Type: "UNKNOWN_COMMAND", Arguments: []string{"groupby"},
			Query: "fetch logs | groupby log.source", Span: span(14, 20)}},
		{"unterminated single quote", Error{Type: "PARSE_ERROR_SINGLE_QUOTES",
			Query: "fetch logs | filter a == 'x", Span: span(26, 27)}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if s := Suggest(tt.err); len(s) != 0 {
				t.Errorf("Suggest = %q, want none", s)
			}
		})
	}
}
