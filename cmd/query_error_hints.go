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
	var span *dqlhint.Span
	if p := e.Position; p != nil && p.Start != nil {
		span = &dqlhint.Span{StartLine: p.Start.Line, StartColumn: p.Start.Column}
		detail.Position = &output.ErrorPosition{Line: p.Start.Line, Column: p.Start.Column}
		if p.End != nil {
			span.EndLine, span.EndColumn = p.End.Line, p.End.Column
			detail.Position.EndLine, detail.Position.EndColumn = p.End.Line, p.End.Column
		}
		detail.Snippet = dqlhint.Snippet(e.Query, *span)
	}
	detail.Suggestions = append(dqlhint.Suggest(dqlhint.Error{
		Type:      e.ErrorType,
		Arguments: e.Arguments,
		Query:     e.Query,
		Span:      span,
	}), dqlErrorAdvice(e)...)
}
