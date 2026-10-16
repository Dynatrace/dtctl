package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/dynatrace-oss/dtctl/pkg/output"
	sdkquery "github.com/dynatrace-oss/dtctl/sdk/api/query"
)

// TestErrorToDetail_QueryErrorPositionAndFix pins the DQL error envelope an
// agent repairs a query from: the position and a caret-marked snippet from
// Grail's syntaxErrorPosition, and the known-trap rewrite first among the
// suggestions.
func TestErrorToDetail_QueryErrorPositionAndFix(t *testing.T) {
	err := fmt.Errorf("query execution failed: %w", &sdkquery.QueryError{
		StatusCode: 400,
		Message:    "PARSE_ERROR",
		ErrorType:  "PARSE_ERROR",
		Detail:     "`by` isn't allowed here. Please check the autocomplete suggestions before the error for alternative options.",
		Arguments:  []string{"`by`"},
		Query:      "fetch logs | summarize count() by service.name",
		Position: &sdkquery.SyntaxPosition{
			Start: &sdkquery.Position{Line: 1, Column: 32},
			End:   &sdkquery.Position{Line: 1, Column: 33},
		},
	})

	d := errorToDetail(context.Background(), err)

	if d.Code != "parse_error" {
		t.Errorf("Code = %q, want parse_error", d.Code)
	}
	want := &output.ErrorPosition{Line: 1, Column: 32, EndLine: 1, EndColumn: 33}
	if d.Position == nil || *d.Position != *want {
		t.Errorf("Position = %+v, want %+v", d.Position, want)
	}
	if d.Snippet != "fetch logs | summarize count() by service.name\n"+strings.Repeat(" ", 31)+"^^" {
		t.Errorf("Snippet = %q", d.Snippet)
	}
	if len(d.Suggestions) == 0 || !strings.HasSuffix(d.Suggestions[0], "dtctl query 'fetch logs | summarize count(), by:{service.name}'") {
		t.Errorf("Suggestions = %q, want the by: rewrite first", d.Suggestions)
	}

	raw, _ := json.Marshal(d)
	for _, key := range []string{`"position":{"line":1,"column":32,"end_line":1,"end_column":33}`, `"snippet":`} {
		if !strings.Contains(string(raw), key) {
			t.Errorf("envelope %s lacks %s", raw, key)
		}
	}
}

// TestErrorToDetail_QueryErrorKeepsExistingAdvice keeps the pre-existing
// advice after the new rewrite when both apply.
func TestErrorToDetail_QueryErrorKeepsExistingAdvice(t *testing.T) {
	d := errorToDetail(context.Background(), &sdkquery.QueryError{
		StatusCode: 400,
		Message:    "UNKNOWN_DATA_OBJECT",
		ErrorType:  "UNKNOWN_DATA_OBJECT",
		Detail:     "dt.entity.service isn't a valid data object.",
		Arguments:  []string{"dt.entity.service"},
		Query:      "fetch dt.entity.service",
		Position: &sdkquery.SyntaxPosition{
			Start: &sdkquery.Position{Line: 1, Column: 7},
			End:   &sdkquery.Position{Line: 1, Column: 23},
		},
	})
	if len(d.Suggestions) != 2 {
		t.Fatalf("Suggestions = %q, want rewrite + census advice", d.Suggestions)
	}
	if !strings.HasSuffix(d.Suggestions[0], `dtctl query 'smartscapeNodes "SERVICE"'`) {
		t.Errorf("Suggestions[0] = %q, want the smartscapeNodes rewrite", d.Suggestions[0])
	}
	if !strings.Contains(d.Suggestions[1], "event-lookback") {
		t.Errorf("Suggestions[1] = %q, want the existing census advice", d.Suggestions[1])
	}
}

// TestErrorToDetail_QueryErrorWithoutPosition leaves an error without a
// reported position free of position and snippet.
func TestErrorToDetail_QueryErrorWithoutPosition(t *testing.T) {
	d := errorToDetail(context.Background(), &sdkquery.QueryError{StatusCode: 400, Message: "bad", ErrorType: "SYNTAX_ERROR"})
	if d.Position != nil || d.Snippet != "" || len(d.Suggestions) != 0 {
		t.Errorf("detail = %+v, want no position, snippet or suggestions", d)
	}
	raw, _ := json.Marshal(d)
	if strings.Contains(string(raw), "position") || strings.Contains(string(raw), "snippet") {
		t.Errorf("envelope %s carries empty position fields", raw)
	}
}

// TestDqlErrorAdvicePlatformObject points a fetch of a platform resource at its get command.
func TestDqlErrorAdvicePlatformObject(t *testing.T) {
	for _, guess := range []string{"dt.automation.workflow_executions", "dt.slo", "dashboards"} {
		s := dqlErrorAdvice(&sdkquery.QueryError{ErrorType: "UNKNOWN_DATA_OBJECT",
			Message: "UNKNOWN_DATA_OBJECT", Detail: guess + " isn't a valid data object."})
		if len(s) == 0 || !strings.Contains(s[0], "dtctl get") {
			t.Errorf("advice for %q = %q, want a dtctl get pointer", guess, s)
		}
	}
	s := dqlErrorAdvice(&sdkquery.QueryError{ErrorType: "UNKNOWN_DATA_OBJECT",
		Message: "UNKNOWN_DATA_OBJECT", Detail: "usersessions isn't a valid data object."})
	if len(s) > 0 && strings.Contains(s[0], "dtctl get") {
		t.Errorf("a Grail near-miss got the platform advice: %q", s)
	}
}
