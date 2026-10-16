package exec

import (
	"fmt"
	"math"
	"slices"
	"sort"
	"strings"

	"github.com/dynatrace-oss/dtctl/pkg/output"
)

// inlineRows carries a result's rows to the inline emitter in two forms: full
// (what a file written to disk holds, and what counts, windows and advice are
// computed from) and the rows the envelope carries. Those are compacted first
// (--compact) and then clipped to --max-field-chars, the constant map
// included: compaction compares the full values, so two long values that share
// their first N chars are never hoisted into constant as one clipped value.
type inlineRows struct {
	full []map[string]interface{}
	// compaction is nil when the rows are not compacted; the fields below then
	// hold the clipped full rows.
	compaction *output.Compaction
	// constant is compaction.Constant, clipped.
	constant map[string]interface{}
	// records are the rows without nulls (compaction.Records), clipped.
	records []map[string]interface{}
	// tabular are the rows the tabular encodings print (compaction.Tabular),
	// clipped.
	tabular       []map[string]interface{}
	clippedFields []string
	// types is the DQL type block the payload carries next to the rows, or nil
	// (see emittedTypes).
	types interface{}
}

// newInlineRows clips the rows of compaction c (computed from full; nil when
// not compacting) to max runes per value.
func newInlineRows(full []map[string]interface{}, c *output.Compaction, max int) inlineRows {
	r := inlineRows{full: full, compaction: c}
	if c == nil {
		r.records, r.clippedFields = output.ClipRecordValues(full, max)
		r.tabular = r.records
		return r
	}
	// records hold the same values as tabular minus nulls, so tabular's clipped
	// fields cover both.
	r.records, _ = output.ClipRecordValues(c.Records, max)
	var rowFields, constFields []string
	r.tabular, rowFields = output.ClipRecordValues(c.Tabular(full), max)
	if len(c.Constant) > 0 {
		var v interface{}
		v, constFields = output.ClipValue(c.Constant, max)
		r.constant, _ = v.(map[string]interface{})
	}
	r.clippedFields = mergeSorted(rowFields, constFields)
	return r
}

// forEncoding returns the rows the given encoding prints: tabular ones for
// TOON and CSV, which keep partial nulls, the null-free records otherwise.
func (r inlineRows) forEncoding(encoding string) []map[string]interface{} {
	if tabularEncoding(encoding) {
		return r.tabular
	}
	return r.records
}

// mergeSorted returns the sorted union of two sorted string slices.
func mergeSorted(a, b []string) []string {
	if len(b) == 0 {
		return a
	}
	out := append(append([]string(nil), a...), b...)
	sort.Strings(out)
	return slices.Compact(out)
}

// fitToBudget bounds an inline kind:"records" envelope to opts.MaxOutputBytes,
// measured with output.EnvelopeSize on the bytes that will actually reach
// stdout — compact or indented, JSON, TOON or the -o auto choice, context and
// metadata included —
// rather than on a separate serialisation of the rows. An envelope within the
// budget is returned unchanged. Otherwise it keeps the longest prefix of rows
// that fits and marks the cut on the context (truncated, returned, next_offset,
// budget_bytes); the order of the rows is never changed, so next_offset indexes
// the full result.
//
// When spilling is available the full result is also written to disk and
// context.next names the inspect command that continues at next_offset, so the
// agent reads the rest without re-querying Grail. When it is not (--spill=never,
// or no host disk) nothing is written and the suggestions say how to bound the
// query instead.
func (e *DQLExecutor) fitToBudget(query string, result *DQLQueryResponse, resp output.Response, rows inlineRows, encoding string, auto bool, opts DQLExecuteOptions) output.Response {
	budget := opts.MaxOutputBytes
	size := func(r output.Response) int64 {
		n, err := output.EnvelopeSize(e.outW(), r)
		if err != nil {
			return math.MaxInt64
		}
		return n
	}
	if size(resp) <= budget {
		return resp
	}

	var path string
	var fileWarnings []string
	if opts.Spill.Enabled() {
		path, fileWarnings = e.writeContinuation(query, result, rows.full, opts)
	}

	total := len(rows.full)
	base := *resp.Context
	build := func(k int, tooSmall, rechoose bool) output.Response {
		ctx := base
		ctx.Warnings = append(append([]string(nil), base.Warnings...), fileWarnings...)
		if tooSmall {
			ctx.Warnings = append(ctx.Warnings, fmt.Sprintf(
				"the output budget of %d bytes is smaller than the envelope without any rows; no rows were returned", budget))
		}
		ctx.Suggestions = append([]string{budgetSuggestion(budget, k, total, path, rows.clippedFields)}, base.Suggestions...)
		returned, next := k, k
		ctx.Truncated = true
		ctx.Returned = &returned
		ctx.NextOffset = &next
		ctx.BudgetBytes = budget
		if path != "" {
			ctx.Next = fmt.Sprintf("dtctl inspect %s --page --offset %d --limit %d", shellQuote(path), k, max(k, 1))
		}
		res, used, _ := inlineResult(encoding, auto && rechoose, rows, k)
		if auto {
			// -o auto re-chooses for the kept rows; name what was emitted, and
			// keep the format-dependent hints (the default's -o json one, and
			// --compact=false, since csv/toon keep partial nulls that json/yaml
			// drop) in step with that choice.
			ctx.Format = used
			if used != encoding {
				ctx.Suggestions = slices.DeleteFunc(ctx.Suggestions, func(s string) bool {
					return s == output.AutoDefaultSuggestion(encoding) || strings.HasPrefix(s, compactRowsPrefix)
				})
				if opts.AutoFormatByDefault && used != "json" {
					ctx.Suggestions = append(ctx.Suggestions, output.AutoDefaultSuggestion(used))
				}
				if rows.compaction != nil && rows.compaction.Changed(used) {
					ctx.Suggestions = append(ctx.Suggestions, compactRowsSuggestion(rows.compaction))
				}
			}
		}
		r := resp
		r.Result = res
		r.Context = &ctx
		return r
	}

	// In one format the envelope grows with every row, so the largest fitting
	// prefix is found by bisection over [0, total-1] (total rows is already
	// known not to fit). The search keeps the format chosen for the full result:
	// -o auto re-chooses per prefix (a sparse or single-row prefix is yaml, a
	// longer dense one csv), and sizes across such a switch are not monotonic,
	// so a bisection over re-chosen prefixes could skip a longer one that fits.
	best := -1
	for lo, hi := 0, total-1; lo <= hi; {
		mid := (lo + hi) / 2
		if size(build(mid, false, false)) <= budget {
			best, lo = mid, mid+1
		} else {
			hi = mid - 1
		}
	}
	if best < 0 {
		return build(0, true, false)
	}
	// The kept rows get the format -o auto picks for them when that still fits.
	if auto {
		if r := build(best, false, true); size(r) <= budget {
			return r
		}
	}
	return build(best, false, false)
}

// shellQuote returns s as a single POSIX shell word, so context.next runs as
// written even when the spill location (DTCTL_SPILL_DIR, the user's cache dir)
// contains spaces or shell metacharacters — an agent that hands the command to
// a shell must never have part of a path interpreted. Paths made only of
// characters no shell treats specially stay bare, which is the common case.
func shellQuote(s string) string {
	if s != "" && strings.IndexFunc(s, func(r rune) bool {
		return !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || strings.ContainsRune("_-./:@%+=,", r))
	}) < 0 {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// writeContinuation writes the full result to the spill location a spill of
// the same query would use, so an agent whose output budget cut the rows can
// page through the rest with inspect. It returns the file path, or "" and the
// reason when nothing could be written; the query itself never fails over it.
func (e *DQLExecutor) writeContinuation(query string, result *DQLQueryResponse, records []map[string]interface{}, opts DQLExecuteOptions) (string, []string) {
	sampled, canonical, tfStart, tfEnd, samplingRatio := spillProvenanceOf(query, result, opts)
	format, targetPath, baseDir, managed, summaryOnly, warnings, err := e.resolveSpillTarget(canonical, tfStart, tfEnd, opts)
	if err != nil {
		return "", append(warnings, fmt.Sprintf("the rows beyond the output budget were not written to disk: %v", err))
	}
	if summaryOnly {
		return "", warnings
	}
	cols := output.ComputeColumnStats(records, sampled, output.DefaultStatsTopK, output.DefaultStatsMaxDistinct)
	if _, werr := writeResultFile(targetPath, format, query, result, records, cols, sampled, samplingRatio, managed, baseDir, opts); werr != nil {
		return "", append(warnings, fmt.Sprintf("the rows beyond the output budget could not be written to disk: %v", werr))
	}
	return targetPath, warnings
}

// budgetSuggestion is the lead suggestion on a budget-truncated result. It
// comes first because an agent must learn before anything else that it holds
// only part of the rows.
func budgetSuggestion(budget int64, returned, total int, path string, clippedFields []string) string {
	head := fmt.Sprintf("# output budget of %d bytes reached: %d of %d rows returned", budget, returned, total)
	if path != "" {
		s := head + "; the full result was written to disk, run context.next to continue at row " +
			fmt.Sprint(returned) + " without re-querying Grail"
		if len(clippedFields) > 0 {
			s += " (the file keeps the unclipped values; add --fields " + strings.Join(clippedFields, ",") + " to read them)"
		}
		return s
	}
	return head + fmt.Sprintf("; the rest were not written to disk — narrow the query ('| fields …', '| limit N', '| summarize …'), raise --max-output-bytes, or re-run with --spill-to <file> and read on with dtctl inspect <file> --page --offset %d", returned)
}

// fieldClipSuggestion names the opt-out for clipped values. It is emitted only
// when something was clipped and kept short: in agent mode the cap is on by
// default, so this line rides on every query that returns a long value.
func fieldClipSuggestion(max int, fields []string) string {
	return fmt.Sprintf("# %s clipped to %d chars; full values: --max-field-chars 0 with '| fields %s'",
		strings.Join(fields, ", "), max, strings.Join(fields, ", "))
}
