package cmd

import (
	"github.com/Dynatrace/dtctl/pkg/dqlhint"
	"github.com/Dynatrace/dtctl/pkg/output"
	sdkquery "github.com/Dynatrace/dtctl/sdk/api/query"
)

// addQueryErrorHints fills the repair fields of a DQL error envelope: the
// position Grail reported with a caret-marked snippet, the known-trap
// rewrites (pkg/dqlhint) and then the broader mistake-class advice.
func addQueryErrorHints(detail *output.ErrorDetail, e *sdkquery.QueryError) {
	hinted := queryHintError(e)
	if span := hinted.Span; span != nil {
		detail.Position = &output.ErrorPosition{Line: span.StartLine, Column: span.StartColumn, EndLine: span.EndLine, EndColumn: span.EndColumn}
		detail.Snippet = dqlhint.Snippet(e.Query, *span)
	}
	detail.Suggestions = append(dqlhint.Suggest(hinted), dqlErrorAdvice(e)...)
}

// queryHintError is a query error as the repair rules read it.
func queryHintError(e *sdkquery.QueryError) dqlhint.Error {
	var span *dqlhint.Span
	if p := e.Position; p != nil && p.Start != nil {
		span = &dqlhint.Span{StartLine: p.Start.Line, StartColumn: p.Start.Column}
		if p.End != nil {
			span.EndLine, span.EndColumn = p.End.Line, p.End.Column
		}
	}
	return dqlhint.Error{Type: e.ErrorType, Arguments: e.Arguments, Query: e.Query, Span: span}
}
