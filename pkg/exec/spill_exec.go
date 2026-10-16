package exec

import (
	"bytes"
	"fmt"
	"io"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/dynatrace-oss/dtctl/pkg/output"
)

// Compaction advice (#578). Compaction is the agent-mode default, so when it
// changed what the agent sees it says how to read it and how to opt out.
const (
	compactRowsPrefix        = "# rows compacted: "
	compactSummarySuggestion = "# summary compacted: one-value columns in result.constant, all-null in result.null_columns; --compact=false for the full profile"
)

// compactRowsSuggestion says how to read compacted rows, naming the columns moved to result.constant.
func compactRowsSuggestion(c *output.Compaction) string {
	s := compactRowsPrefix + "row = result.constant + record, absent key = null; --compact=false for full rows"
	if c == nil || len(c.Constant) == 0 {
		return s
	}
	keys := make([]string, 0, len(c.Constant))
	for k := range c.Constant {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	if len(keys) > 8 {
		keys = append(keys[:8], "…")
	}
	return compactRowsPrefix + "the same in every row, so listed once in result.constant: " + strings.Join(keys, ", ") +
		"; row = result.constant + record, absent key = null; --compact=false for full rows"
}

const userPathPrivacyWarning = "spill path is a user-chosen location and opts out of the managed privacy guarantees (no TTL pruning, no per-context partitioning, best-effort 0600 only); you own its lifetime"

// trySpill runs the spill decision and emits the appropriate agent envelope.
// It returns handled=true when it produced output (the caller returns
// immediately); handled=false means the caller should continue with the
// unchanged output path (a non-agent inline result, or a shape this path
// deliberately leaves alone — see buildSpillResponse).
func (e *DQLExecutor) trySpill(query string, result *DQLQueryResponse, records []map[string]interface{}, displayFormat string, opts DQLExecuteOptions) (bool, error) {
	resp, handled, err := e.buildSpillResponse(query, result, records, displayFormat, opts)
	if err != nil {
		return true, err
	}
	if !handled {
		return false, nil
	}
	return true, output.EncodeEnvelope(e.outW(), resp)
}

// buildSpillResponse makes the inline-vs-spill decision (D5/D19-buffered),
// writes the spilled file + sidecar when applicable, and assembles the agent
// envelope. The handled return reports whether this path produced a Response to
// emit: true for a spilled/summary-only result, and for an inline result in
// agent mode (a self-describing kind:"records" envelope, D2/D31); false when the
// caller should fall through to today's unchanged output path. It is separated
// from trySpill so it can be unit-tested without capturing stdout: it returns
// the Response and leaves emission to the caller, while its only side effect
// (writing to disk) is fully controlled via opts.Spill (ToPath / Dir).
func (e *DQLExecutor) buildSpillResponse(query string, result *DQLQueryResponse, records []map[string]interface{}, displayFormat string, opts DQLExecuteOptions) (output.Response, bool, error) {
	compaction := compactionFor(records, opts)

	// Inline output carries the rows compacted and then with long values
	// clipped (--max-field-chars), so that is what the spill decision measures,
	// in the display encoding (D24); a spill file keeps the full rows.
	inline := newInlineRows(records, compaction, opts.MaxFieldChars)
	inline.types = emittedTypes(result, opts)
	measured, encoding := measureInline(inline, displayFormat, inlineLayout(e.outW(), opts, displayFormat))
	switch opts.Spill.Mode {
	case SpillAuto:
		if measured <= opts.Spill.Threshold {
			return e.inlineRecordsResponse(query, result, inline, measured, encoding, output.IsAutoFormat(displayFormat), opts) // inline
		}
	case SpillAlways:
		// always spill
	default:
		return e.inlineRecordsResponse(query, result, inline, measured, encoding, output.IsAutoFormat(displayFormat), opts) // never / unknown -> inline
	}

	sampled, canonical, tfStart, tfEnd, samplingRatio := spillProvenanceOf(query, result, opts)

	format, targetPath, baseDir, managed, summaryOnly, warnings, err := e.resolveSpillTarget(canonical, tfStart, tfEnd, opts)
	if err != nil {
		return output.Response{}, true, err
	}
	// When resolveSpillTarget already degraded to summary-only, the cause is a
	// read-only/unwritable location; a later write failure overrides this.
	summaryReason := ""
	if summaryOnly {
		summaryReason = summaryReasonNoLocation
	}

	// A --jq transform is not applied to spilled rows (the file holds the full
	// untransformed result so stats/sample stay columnar). Surface it loudly
	// rather than silently dropping the filter.
	if opts.JQFilter != "" {
		warnings = append(warnings, "--jq was not applied to the spilled result; the file holds the full untransformed rows — apply your filter to the file locally")
	}

	cols := output.ComputeColumnStats(records, sampled, output.DefaultStatsTopK, output.DefaultStatsMaxDistinct)

	// Compaction collapses the single-value columns into one map and names the
	// all-null ones instead of profiling them; the sample rows drop both. The
	// file and the sidecar below still carry every column.
	summaryCols, sampleSource := cols, records
	if compaction != nil {
		summaryCols, sampleSource = compaction.FilterColumns(cols), compaction.Records
	}
	sampleRows := output.SampleRows(sampleSource, output.DefaultSampleRows)

	// The in-context envelope carries a size-bounded view of the columns (the
	// most-populated ones, by null count); the on-disk sidecar written below
	// keeps the full per-column set so nothing is lost for later inspection.
	envCols, omittedCols := output.CapColumnsForEnvelope(summaryCols, output.DefaultMaxSummaryColumns)

	manifest := &output.ResultFileManifest{
		Query:         query,
		Format:        format,
		Rows:          len(records),
		ContextName:   opts.ContextName,
		TenantID:      opts.TenantID,
		Sampled:       sampled,
		SamplingRatio: samplingRatio,
		SampleRows:    sampleRows,
	}
	manifest.SetStats(envCols, sampled)
	manifest.ColumnsOmitted = omittedCols
	manifest.Types = inline.types
	if compaction != nil {
		manifest.Constant = compaction.EnvelopeConstant()
		manifest.NullColumns = compaction.NullColumns
	}

	decided := "spilled"
	if !summaryOnly {
		written, werr := writeResultFile(targetPath, format, query, result, records, cols, sampled, samplingRatio, managed, baseDir, opts)
		if werr != nil {
			if opts.Spill.ToPath != "" {
				// The caller pinned an explicit destination; a failure there is a
				// real error, not a reason to silently degrade.
				return output.Response{}, true, fmt.Errorf("failed to write spill file %q: %w", targetPath, werr)
			}
			// Managed write failed unexpectedly -> degrade to summary-only rather
			// than dumping rows into context (D8: never dump on failure).
			summaryOnly = true
			summaryReason = summaryReasonWriteFailed
			warnings = append(warnings, "spill write failed; returning overview only")
		} else {
			manifest.Kind = output.KindResultFile
			manifest.Path = targetPath
			manifest.Bytes = written
		}
	}

	if summaryOnly {
		manifest.Kind = output.KindSummaryOnly
		decided = "summary-only"
	}

	emptyReason, emptySuggestions := e.emptyResultAdvice(query, result, records, opts)
	adviceWarnings, suggestions := spillAdvice(query, result, manifest, summaryReason, len(omittedCols), compaction, emptySuggestions, opts)
	warnings = append(warnings, adviceWarnings...)

	total := len(records)
	ctx := &output.ResponseContext{
		Verb:             "query",
		Resource:         resourceFromQuery(query),
		Total:            &total,
		Decided:          decided,
		ThresholdBytes:   opts.Spill.Threshold,
		MeasuredBytes:    measured,
		MeasuredEncoding: encoding,
		Warnings:         warnings,
		Suggestions:      suggestions,
		EmptyReason:      emptyReason,
	}
	if opts.Decorate != nil {
		opts.Decorate(ctx, result, records)
	}

	resp := output.Response{
		OK:              true,
		EnvelopeVersion: output.EnvelopeVersion,
		Result:          manifest,
		Context:         ctx,
		Metadata:        envelopeMetadata(query, result, opts),
	}
	return resp, true, nil
}

// spillAdvice assembles the warnings and suggestions of a spilled or
// summary-only envelope, after the ones the spill target itself produced. Both
// the buffered and the streamed builder use it, so the two envelopes read the
// same. emptySuggestions is the empty-result diagnosis, which only a buffered
// result can have.
func spillAdvice(query string, result *DQLQueryResponse, manifest *output.ResultFileManifest, summaryReason string, omittedCols int, compaction *output.Compaction, emptySuggestions []string, opts DQLExecuteOptions) (warnings, suggestions []string) {
	suggestions = spillSuggestions(query, manifest.Kind, summaryReason)
	// Now that `dtctl inspect` ships (Layer 2), point at concrete, bounded
	// row-access follow-ups on the spilled file — the calls an agent cannot
	// satisfy from the summary it just received (INSPECT IN4/D30).
	if manifest.Kind == output.KindResultFile && manifest.Path != "" {
		suggestions = append(suggestions,
			"# for bounded row access without re-querying Grail: dtctl inspect "+manifest.Path+" --head 20 (also --tail, --page --offset N --limit M, --fields a,b)")
	}
	if omittedCols > 0 {
		if manifest.Kind == output.KindResultFile {
			suggestions = append(suggestions, fmt.Sprintf("# %d sparser columns were omitted from this summary to keep it compact; their names are in result.columns_omitted and full per-column stats are in the sidecar manifest next to the file", omittedCols))
		} else {
			// Summary-only: nothing was written, so there is no sidecar to point at.
			suggestions = append(suggestions, fmt.Sprintf("# %d sparser columns were omitted from this summary to keep it compact; their names are in result.columns_omitted (the rows were not written to disk, so there is no sidecar manifest)", omittedCols))
		}
	}

	// Surface query notifications (scan-limit truncation, result caps, timeouts,
	// sampling) into the envelope. Their advice leads the suggestions because a
	// PARTIAL result is more consequential to an agent than the spill/inspect
	// follow-ups — an agent parsing stdout must learn the result is incomplete.
	notifWarnings, notifSuggestions := queryNotificationAdvice(query, result.GetNotifications())
	warnings = append(warnings, notifWarnings...)
	warnings = append(warnings, approximationWarnings(result)...)
	suggestions = append(notifSuggestions, suggestions...)
	scanWarnings, scanSuggestions := heavyScanAdvice(result)
	warnings = append(warnings, scanWarnings...)
	suggestions = append(suggestions, scanSuggestions...)
	suggestions = append(suggestions, emptySuggestions...)
	suggestions = append(suggestions, lookbackAdvice(query)...)
	suggestions = append(suggestions, metadataDefaultAdvice(query, extractQueryMetadata(result), opts, false)...)
	if compaction != nil && compaction.Changed("json") {
		suggestions = append(suggestions, compactSummarySuggestion)
	}
	suggestions = append(suggestions, seriesAdvice(opts)...)
	return warnings, suggestions
}

// inlineRecordsResponse handles the inline (not-spilled) decision. In agent mode
// it returns a self-describing kind:"records" envelope so a consumer branches on
// result.kind uniformly across inline and spilled results (D2/D31); the rows are
// carried directly, natively for -o json and as an encoded string for -o toon
// (see InlineRecordsEncoded) — both under kind:"records", so the envelope is
// present either side of the spill threshold rather than only above it.
//
// It deliberately leaves two shapes alone — falling through to the caller's
// unchanged output path (handled=false) — so it never overrides an explicit
// choice the caller already made that the envelope cannot carry:
//   - a byte-oriented display encoding (-o csv/yaml/chart): a caller asking for
//     those wants the bytes, not an envelope wrapping them.
//   - a --jq transform: printResults owns that shape via printAgentJQ, which
//     wraps the filter output in this same envelope (#413).
//
// Outside agent mode an inline result is always a fall-through (a human wants the
// table/CSV, not an envelope).
//
// With auto (-o auto), encoding is the format output.ChooseAutoFormat picked
// for these rows. csv and yaml then stay in the envelope too, encoded like
// toon, because the agent asked for the cheapest encoding, not for raw bytes;
// context.format names the choice. The rows are clipped (--max-field-chars)
// before auto chooses and encodes them, so the choice fits what is emitted.
func (e *DQLExecutor) inlineRecordsResponse(query string, result *DQLQueryResponse, rows inlineRows, measured int64, encoding string, auto bool, opts DQLExecuteOptions) (output.Response, bool, error) {
	// A --jq transform reshapes the result into something this kind:"records"
	// envelope cannot describe, so it falls through to printResults, which wraps
	// the filter output in the same {ok, result, context} envelope (#413). So do
	// the raw byte-oriented encodings (csv/yaml): an agent asking for those
	// wants the bytes, not an envelope wrapping them.
	if !opts.AgentMode || opts.JQFilter != "" {
		return output.Response{}, false, nil
	}
	switch encoding {
	case "json", "toon":
	case "csv", "yaml":
		if !auto {
			return output.Response{}, false, nil
		}
	default:
		return output.Response{}, false, nil
	}
	res, used, encodeWarning := inlineResult(encoding, auto, rows, len(rows.full))
	if auto {
		encoding = used
	}

	// Even an inline (small) result can be PARTIAL — a scan-limit stop can leave
	// few rows. Surface the same notification advice so the agent isn't misled
	// into treating a truncated scan as the complete answer.
	notifWarnings, notifSuggestions := queryNotificationAdvice(query, result.GetNotifications())
	notifWarnings = append(notifWarnings, approximationWarnings(result)...)
	scanWarnings, scanSuggestions := heavyScanAdvice(result)
	notifWarnings = append(notifWarnings, scanWarnings...)
	notifSuggestions = append(notifSuggestions, scanSuggestions...)
	emptyReason, emptySuggestions := e.emptyResultAdvice(query, result, rows.full, opts)
	notifSuggestions = append(notifSuggestions, emptySuggestions...)
	notifSuggestions = append(notifSuggestions, lookbackAdvice(query)...)
	notifSuggestions = append(notifSuggestions, sampleAdvice(query, len(rows.full))...)
	notifSuggestions = append(notifSuggestions, durationAdvice(query, result)...)
	notifSuggestions = append(notifSuggestions, seriesAdvice(opts)...)
	if encodeWarning != "" {
		notifWarnings = append(notifWarnings, encodeWarning)
	}
	if auto && opts.AutoFormatByDefault && encoding != "json" {
		notifSuggestions = append(notifSuggestions, output.AutoDefaultSuggestion(encoding))
	}
	if rows.compaction != nil && rows.compaction.Changed(encoding) {
		notifSuggestions = append(notifSuggestions, compactRowsSuggestion(rows.compaction))
	}

	total := len(rows.full)
	ctx := &output.ResponseContext{
		Verb:        "query",
		Resource:    resourceFromQuery(query),
		Total:       &total,
		Decided:     "inline",
		Warnings:    notifWarnings,
		Suggestions: notifSuggestions,
		EmptyReason: emptyReason,
	}
	// The threshold/measured sizes explain a spill decision; on an inline result
	// they are debugging detail, which --metadata=minimal leaves to -v (#577).
	dropSpillDebug := output.IsMinimalFields(opts.MetadataFields) && !opts.Verbose
	if !dropSpillDebug {
		ctx.ThresholdBytes = opts.Spill.Threshold
		ctx.MeasuredBytes = measured
		ctx.MeasuredEncoding = encoding
	}
	if auto {
		ctx.Format = encoding
	}
	ctx.Suggestions = append(ctx.Suggestions, metadataDefaultAdvice(query, extractQueryMetadata(result), opts, dropSpillDebug)...)
	if len(rows.clippedFields) > 0 {
		ctx.MarkFieldsClipped(opts.MaxFieldChars, rows.clippedFields)
		ctx.Suggestions = append(ctx.Suggestions, fieldClipSuggestion(opts.MaxFieldChars, rows.clippedFields))
	}
	if opts.Decorate != nil {
		opts.Decorate(ctx, result, rows.full)
	}
	resp := output.Response{
		OK:              true,
		EnvelopeVersion: output.EnvelopeVersion,
		Result:          res,
		Context:         ctx,
		Metadata:        envelopeMetadata(query, result, opts),
	}
	if opts.MaxOutputBytes > 0 {
		resp = e.fitToBudget(query, result, resp, rows, encoding, auto, opts)
	}
	return resp, true, nil
}

// inlineResult builds the kind:"records" payload for the first k rows in the
// given encoding and returns the encoding it actually used. `-o toon` keeps the
// envelope, with the rows encoded inside it. This is what makes the contract
// independent of result size: the same flags produce a `kind: "records"`
// envelope below the spill threshold and a `kind: "result-file"` envelope above
// it, so `ok`, `error.code` and `context` (heavy-scan warnings, suggestions)
// survive either way. The constant map of a compacted result stays a JSON map
// next to the rows whatever encodes them. With rechoose, a csv/yaml encoding is
// the -o auto choice for the k rows, so a budget-cut prefix is encoded in the
// format that suits it (a single row is yaml, not one-line csv); for all the
// rows that is the choice measureInline made. Without it, csv/yaml encode the
// k rows as given. An encoding failure falls back to native JSON and returns
// the warning to surface.
func inlineResult(encoding string, rechoose bool, rows inlineRows, k int) (interface{}, string, string) {
	native := &output.InlineRecords{Kind: output.KindRecords, Constant: rows.constant, Records: rows.records[:k], Types: rows.types}
	switch encoding {
	case "toon":
		toonRows, err := output.MarshalTOON(rows.forEncoding("toon")[:k])
		if err != nil {
			return native, "json", fmt.Sprintf("TOON encoding failed: %v; the rows were encoded as JSON instead", err)
		}
		return &output.InlineRecordsEncoded{Kind: output.KindRecords, Encoding: "toon", Constant: rows.constant, Records: toonRows, Types: rows.types}, "toon", ""
	case "csv", "yaml":
		format := encoding
		var encoded string
		var err error
		switch {
		case rechoose && rows.compaction == nil:
			var choice output.AutoChoice
			choice, encoded, err = output.MarshalAuto(rows.records[:k])
			format = choice.Format
		case rechoose:
			// Chosen on the compacted table, as measureInline does.
			format = output.ChooseAutoFormat(rows.tabular[:k]).Format
			fallthrough
		default:
			if format != "json" {
				encoded, err = encodeRows(rows.forEncoding(format)[:k], format)
			}
		}
		if err != nil {
			return native, "json", fmt.Sprintf("-o auto encoding failed: %v; the rows were encoded as JSON instead", err)
		}
		if format == "json" {
			return native, "json", ""
		}
		return &output.InlineRecordsEncoded{Kind: output.KindRecords, Encoding: format, Constant: rows.constant, Records: encoded, Types: rows.types}, format, ""
	}
	return native, "json", ""
}

// emittedTypes returns the DQL type block for the agent envelope's result
// payload, or nil. Like the plain json/yaml output it is emitted only for an
// explicit --include-types (EmitTypes): --typed and Parquet request the types
// to consume them, not to print them.
func emittedTypes(result *DQLQueryResponse, opts DQLExecuteOptions) interface{} {
	if !opts.EmitTypes {
		return nil
	}
	if types := result.GetTypes(); len(types) > 0 {
		return types
	}
	return nil
}

// compactionFor returns the compacted view of records when opts asks for it, or
// nil. A --jq program addresses the full rows, so it never sees a compaction.
func compactionFor(records []map[string]interface{}, opts DQLExecuteOptions) *output.Compaction {
	if !opts.Compact || opts.JQFilter != "" {
		return nil
	}
	c := output.CompactRecords(records)
	return &c
}

// inlineLayout is the JSON layout this invocation would print its inline rows
// in, which is what the spill decision has to measure (#570). In agent mode the
// rows go into an envelope and EnvelopeRecordsLayout asks the question
// EncodeEnvelope does, of the same writer: compact when piped, as for an agent
// (up to about 2.5x smaller than the same rows indented, for narrow rows), and indented at the depth
// of result.records on a terminal. Outside agent mode printRecords prints
// {"records": [...]} with the indenting JSON printer, or for -o jsonl the
// rows one compact line each with JSONLPrinter. stdout is the writer this
// invocation prints to, not the process's: the layout depends on whether it is
// a terminal.
func inlineLayout(stdout io.Writer, opts DQLExecuteOptions, format string) output.JSONLayout {
	if opts.AgentMode {
		return output.EnvelopeRecordsLayout(stdout)
	}
	if strings.ToLower(strings.TrimSpace(format)) == "jsonl" {
		return output.JSONLinesLayout
	}
	return output.IndentedJSONLayout(1)
}

// measureInline measures the inline payload in the display encoding, laid out
// as the writer will print it (see inlineLayout): the rows as emitted plus,
// when compacting, the constant map they share, and the type block when one
// rides along. Both sit beside the rows, at their depth. Under -o auto with
// compaction the encoding is chosen from the compacted table (constant and
// all-null columns removed), since that is what gets encoded; the returned
// encoding is the chosen one.
func measureInline(rows inlineRows, format string, layout output.JSONLayout) (int64, string) {
	if layout.Lines {
		// printRecords prints -o jsonl rows as they came: not compacted, and
		// with no constant map or type block, which a line format has no place
		// for.
		return output.MeasureSerializedBytes(rows.full, format, layout)
	}
	n, enc := measureInlineRows(rows, format, layout)
	if rows.types != nil {
		// Like constant, the type block is always native JSON in the envelope.
		m, _ := output.MeasureSerializedBytes(rows.types, "json", layout)
		n += m
	}
	return n, enc
}

func measureInlineRows(rows inlineRows, format string, layout output.JSONLayout) (int64, string) {
	if rows.compaction == nil {
		return output.MeasureSerializedBytes(rows.records, format, layout)
	}
	if output.IsAutoFormat(format) {
		format = output.ChooseAutoFormat(rows.tabular).Format
	}
	n, enc := output.MeasureSerializedBytes(rows.forEncoding(output.NormalizeMeasureEncoding(format)), format, layout)
	if len(rows.constant) > 0 {
		// constant is always a JSON map in the envelope, whatever encodes the rows.
		m, _ := output.MeasureSerializedBytes(rows.constant, "json", layout)
		n += m
	}
	return n, enc
}

// compactedRows returns the rows c emits in the given encoding: without nulls,
// except under the tabular encodings (TOON, CSV), which keep partial nulls so
// the rows stay one table (see output.Compaction.Tabular).
func compactedRows(c *output.Compaction, records []map[string]interface{}, encoding string) []map[string]interface{} {
	if tabularEncoding(encoding) {
		return c.Tabular(records)
	}
	return c.Records
}

// tabularEncoding reports whether encoding prints rows as one table (TOON,
// CSV), which needs every row to carry the same columns.
func tabularEncoding(encoding string) bool {
	return encoding == "toon" || encoding == "csv"
}

// encodeRows renders rows in format (csv or yaml) as a string for an
// InlineRecordsEncoded payload.
func encodeRows(rows []map[string]interface{}, format string) (string, error) {
	var buf bytes.Buffer
	p := output.NewPrinterWithOpts(output.PrinterOptions{Format: format, Writer: &buf})
	if err := p.PrintList(rows); err != nil {
		return "", err
	}
	return buf.String(), nil
}

// envelopeMetadata returns the Grail/metrics query metadata for the agent
// envelope's top-level `metadata` key, honoring --metadata field selection. It
// returns nil when metadata was not requested (MetadataFields empty) or the
// response carried none, so the key is omitted rather than emitted empty. Agent
// mode defaults --metadata to "minimal", so an agent gets the lean metadata
// without asking for it; the same placement is used for inline and spilled
// results. --metadata=minimal is resolved against the response here.
func envelopeMetadata(query string, result *DQLQueryResponse, opts DQLExecuteOptions) interface{} {
	if len(opts.MetadataFields) == 0 {
		return nil
	}
	meta := extractQueryMetadata(result)
	if meta == nil {
		return nil
	}
	return output.MetadataToMap(meta, resolveMetadataFields(query, meta, opts))
}

// resolveMetadataFields expands the --metadata=minimal selector into the
// concrete fields worth reporting for this response (output.ExpandMetadataFields).
// Any other selection is returned unchanged.
func resolveMetadataFields(query string, meta *output.QueryMetadata, opts DQLExecuteOptions) []string {
	return output.ExpandMetadataFields(meta, opts.MetadataFields, usesDefaultWindow(query, opts))
}

// windowParamRe matches a window parameter name. DQL parameter names are
// case-insensitive and allow whitespace before the colon (`FROM :` is valid).
var windowParamRe = regexp.MustCompile(`(?i)\b(?:from|to|timeframe)\s*:`)

// metadataDefaultSuggestion names the opt-out from agent mode's default
// --metadata=minimal. Kept short: it rides on most agent query results.
const metadataDefaultSuggestion = "# metadata trimmed by default; -M=all for the full block"

// metadataDefaultAdvice returns the opt-out suggestion when agent mode applied
// --metadata=minimal by default and that actually dropped output: a metadata
// field the response carried, or (droppedSpillDebug) the inline spill
// measurements. An explicit -M=minimal asked for exactly this, so it gets none.
func metadataDefaultAdvice(query string, meta *output.QueryMetadata, opts DQLExecuteOptions, droppedSpillDebug bool) []string {
	if !opts.MetadataDefaulted || !output.IsMinimalFields(opts.MetadataFields) {
		return nil
	}
	if droppedSpillDebug || output.MetadataOmitsFields(meta, resolveMetadataFields(query, meta, opts)) {
		return []string{metadataDefaultSuggestion}
	}
	return nil
}

// usesDefaultWindow reports whether the query ran on the server's default
// analysis window: neither the query text nor --default-timeframe-* named one.
// Only then is the analysis timeframe news to the caller.
func usesDefaultWindow(query string, opts DQLExecuteOptions) bool {
	if opts.DefaultTimeframeStart != "" || opts.DefaultTimeframeEnd != "" {
		return false
	}
	// Only DQL code can name a window: strings, backtick identifiers and
	// comments are blanked first. A query the stripper cannot read is treated
	// as windowless, so the timeframe is kept rather than wrongly dropped.
	code, ok := stripDQLLiterals(query)
	if !ok {
		return true
	}
	return !windowParamRe.MatchString(code)
}

// resolveSpillTarget decides the format, destination path, and base dir for a
// spill, and whether it must degrade to summary-only (D7/D8/D25).
func (e *DQLExecutor) resolveSpillTarget(canonical, tfStart, tfEnd string, opts DQLExecuteOptions) (format, targetPath, baseDir string, managed, summaryOnly bool, warnings []string, err error) {
	// Explicit caller-chosen destination (--spill-to): write exactly there.
	if opts.Spill.ToPath != "" {
		f, ferr := spillFormatForPath(opts.Spill.ToPath, opts.Spill.Format)
		if ferr != nil {
			return "", "", "", false, false, nil, ferr
		}
		dir := filepath.Dir(opts.Spill.ToPath)
		if !output.ProbeWritable(dir) {
			return "", "", "", false, false, nil, fmt.Errorf("spill destination directory %q is not writable", dir)
		}
		return f, opts.Spill.ToPath, "", false, false, []string{userPathPrivacyWarning}, nil
	}

	format = opts.Spill.Format
	if format == "" {
		format = defaultSpillFormat
	}
	if verr := validateSpillFormat(format); verr != nil {
		return "", "", "", false, false, nil, verr
	}

	base, isManaged, berr := output.SpillBaseDir(opts.Spill.Dir)
	if berr != nil {
		// No writable location anywhere -> summary-only (D8). Stats are still
		// computable without disk.
		return format, "", "", false, true,
			[]string{"no writable spill location — overview only; local follow-up unavailable"}, nil
	}
	// The managed cache is partitioned by context (D9); a user-chosen dir opts
	// out of partitioning (and of TTL/perms guarantees) and is flagged (D25).
	dir := base
	if isManaged {
		dir = filepath.Join(base, output.SanitizeContextName(opts.ContextName))
	} else {
		warnings = append(warnings, userPathPrivacyWarning)
	}
	hash := output.SpillHash(canonical, tfStart, tfEnd, fmt.Sprintf("%v", opts.Segments), fmt.Sprintf("%g", opts.DefaultSamplingRatio))
	targetPath = filepath.Join(dir, "q-"+hash+"."+extForFormat(format))
	return format, targetPath, base, isManaged, false, warnings, nil
}

// defaultSpillFormat is the spill format used when none is configured. JSON Lines
// is the default (D26): schema-less, append-friendly, one record per line, and
// read natively by common local tooling — it reuses the `-o jsonl` writer added
// alongside Parquet in the output-formats change (PR1).
const defaultSpillFormat = "jsonl"

// validateSpillFormat accepts the formats dtctl can spill to, all backed by the
// existing `-o` writers: JSON Lines (default), JSON, CSV, and Parquet.
func validateSpillFormat(format string) error {
	switch strings.ToLower(format) {
	case "jsonl", "json", "csv", "parquet":
		return nil
	default:
		return fmt.Errorf("unsupported spill format %q (use jsonl, json, csv, or parquet)", format)
	}
}

// spillFormatForPath infers the spill format from a destination file extension,
// falling back to the configured format (or the default) for an extension-less path.
func spillFormatForPath(path, fallback string) (string, error) {
	ext := strings.ToLower(strings.TrimPrefix(filepath.Ext(path), "."))
	switch ext {
	case "jsonl", "json", "csv", "parquet":
		return ext, nil
	case "":
		if fallback == "" {
			return defaultSpillFormat, nil
		}
		return fallback, validateSpillFormat(fallback)
	default:
		return "", fmt.Errorf("unsupported --spill-to file extension %q (use .jsonl, .json, .csv, or .parquet)", ext)
	}
}

func extForFormat(format string) string {
	return output.ExtForFormat(format)
}

// Summary-only degradation causes. They select the right next-step advice:
// a read-only filesystem makes any --spill-to destination fail too, so we steer
// to a self-bounding re-query; a one-off write failure can still succeed at a
// different, explicitly chosen path.
const (
	summaryReasonNoLocation  = "no-writable-location"
	summaryReasonWriteFailed = "write-failed"
)

// spillSuggestions builds the public, tool-agnostic follow-up hints (D27/D28).
// It nudges toward DQL aggregation for non-aggregating queries and points at
// generic local tooling — it never names a specific third-party project. For a
// summary-only result the rows are not on disk, so the advice is keyed on why
// the spill degraded (summaryReason); it is ignored for the result-file kind.
func spillSuggestions(query, kind, summaryReason string) []string {
	var s []string
	if kind == output.KindResultFile {
		s = append(s, "# the full result is on disk at the path above; read it with your file tooling for row-level follow-up")
	}
	if isNonAggregatingQuery(query) {
		s = append(s, "# for aggregate questions, prefer pushing the work into DQL, e.g. add '| summarize count(), by:{<field>}' and re-query")
	}
	switch kind {
	case output.KindResultFile:
		s = append(s, "# for complex local analysis, process the spilled file with your preferred local analytics tooling")
	case output.KindSummaryOnly:
		s = append(s, summaryOnlyFollowups(summaryReason)...)
	}
	return s
}

// summaryOnlyFollowups returns the next-step hints for a summary-only result —
// the rows could not be written to disk, so local file follow-up is impossible.
// A read-only filesystem (no-location) makes --spill-to futile, so it steers to
// a self-bounding re-query (--spill=never plus a column/row cap) that keeps the
// inline result small; a transient write failure can still succeed at a
// different, explicitly chosen destination.
func summaryOnlyFollowups(reason string) []string {
	if reason == summaryReasonWriteFailed {
		return []string{
			"# the spill file could not be written, so the rows are NOT on disk",
			"# retry, or re-run with --spill-to <path> pointing at a writable location you choose",
		}
	}
	// Default: no writable location anywhere (read-only filesystem).
	return []string{
		"# no writable location for a spill file (read-only filesystem), so the rows are NOT on disk and --spill-to would fail too",
		"# to get the rows inline without flooding context, re-query with --spill=never and bound the result — add '| fields <columns you need>' and/or '| limit <N>', or pass --max-result-records <N>",
	}
}

// isNonAggregatingQuery is a cheap heuristic: a query that does not summarise or
// build a timeseries is "raw row" shaped, which is exactly when the DQL-aggregate
// nudge is worth showing (D27).
func isNonAggregatingQuery(query string) bool {
	l := strings.ToLower(query)
	for _, agg := range []string{"summarize", "maketimeseries", "makets"} {
		if strings.Contains(l, agg) {
			return false
		}
	}
	return true
}

// resourceFromQuery extracts the fetched resource (e.g. "logs") from a DQL query
// for the envelope's context.resource, best-effort.
func resourceFromQuery(query string) string {
	fields := strings.Fields(strings.ToLower(query))
	for i, f := range fields {
		if f == "fetch" && i+1 < len(fields) {
			return strings.TrimRight(fields[i+1], ",")
		}
	}
	return ""
}
