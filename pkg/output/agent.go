package output

import (
	"encoding/json"
	"fmt"
	"io"
	"os"

	"golang.org/x/term"
)

// EncodeEnvelope writes the agent Response to w as JSON. It pretty-prints (2-space
// indent) only when w is an interactive terminal — a human running --agent by
// hand — and emits compact JSON otherwise (piped to a tool or AI agent, redirected
// to a file, or a test buffer). The compact form is what machine consumers get and
// drops the ~⅓ of bytes that indentation would otherwise add; the envelope shape
// is identical either way, only whitespace differs.
func EncodeEnvelope(w io.Writer, resp Response) error {
	return encodeEnvelopeTo(w, resp, EnvelopeIndented(w))
}

// EnvelopeIndented reports whether EncodeEnvelope pretty-prints for w. It is
// the one place that decision is made: anything that has to predict the bytes
// an envelope written to w will take (EnvelopeSize, the spill measurement via
// EnvelopeRecordsLayout) asks it rather than repeating the test, so the
// prediction cannot drift from what is actually written.
func EnvelopeIndented(w io.Writer) bool {
	return isTerminalWriter(w)
}

// EnvelopeRecordsLayout is the JSON layout of the rows of a kind:"records"
// result (InlineRecords: result.records, and result.constant and result.types
// beside them) in an envelope EncodeEnvelope writes to w, for the spill
// measurement (MeasureSerializedBytes): compact unless w is a terminal, and
// indented two levels deep, {"result": {"records": ...}}, when it is.
func EnvelopeRecordsLayout(w io.Writer) JSONLayout {
	return envelopeRecordsLayout(EnvelopeIndented(w))
}

func envelopeRecordsLayout(indented bool) JSONLayout {
	if !indented {
		return JSONLayout{}
	}
	return IndentedJSONLayout(2)
}

// EnvelopeSize returns the number of bytes EncodeEnvelope would write for resp
// to w — the same indentation decision, so the count is what the reader of w
// actually receives. Nothing is written to w. An output budget
// (--max-output-bytes) is measured with it rather than with a separate
// serialisation of the rows, so the envelope, the context and the encoding the
// caller chose are all inside the budget.
func EnvelopeSize(w io.Writer, resp Response) (int64, error) {
	counter := &countingWriter{w: io.Discard}
	err := encodeEnvelopeTo(counter, resp, EnvelopeIndented(w))
	return counter.n, err
}

func encodeEnvelopeTo(w io.Writer, resp Response, indent bool) error {
	enc := json.NewEncoder(w)
	if indent {
		enc.SetIndent("", "  ")
	}
	return enc.Encode(resp)
}

// isTerminalWriter reports whether w is an interactive terminal. Only an *os.File
// can be one; buffers, pipes, and redirected files are not — so machine and test
// output is always compact and deterministic.
func isTerminalWriter(w io.Writer) bool {
	f, ok := w.(*os.File)
	return ok && term.IsTerminal(int(f.Fd()))
}

// Response is the agent mode envelope that wraps all CLI output.
// Success responses have OK=true with Result populated.
// Error responses have OK=false with Error populated.
type Response struct {
	OK bool `json:"ok"`
	// EnvelopeVersion is the contract version (D31). It is set on envelopes that
	// participate in the versioned spill contract (result.kind ∈ result-file /
	// summary-only) so a consumer can detect "this dtctl is newer than I was
	// written for". Omitted (0) on legacy envelopes to keep existing output
	// byte-for-byte unchanged.
	EnvelopeVersion int              `json:"envelope_version,omitempty"`
	Result          interface{}      `json:"result"`
	Error           *ErrorDetail     `json:"error,omitempty"`
	Context         *ResponseContext `json:"context,omitempty"`
	// Metadata carries the Grail query metadata (execution time, scanned
	// records/bytes, sampled, queryId, ...) alongside the result, mirroring the
	// non-agent `-o json --metadata` shape. Populated on the query envelope when
	// --metadata is requested and the response carried metadata; omitted
	// otherwise so other envelopes stay byte-for-byte unchanged.
	Metadata interface{} `json:"metadata,omitempty"`
}

// ResponseContext provides operational metadata alongside the result.
type ResponseContext struct {
	Total       *int              `json:"total,omitempty"`
	HasMore     bool              `json:"has_more,omitempty"`
	Verb        string            `json:"verb,omitempty"`
	Resource    string            `json:"resource,omitempty"`
	Suggestions []string          `json:"suggestions,omitempty"`
	Warnings    []string          `json:"warnings,omitempty"`
	Duration    string            `json:"duration,omitempty"`
	Links       map[string]string `json:"links,omitempty"`

	// Format names the encoding `-o auto` chose for result ("csv", "yaml" or
	// "json"), so a consumer can branch on it before parsing. For csv and yaml
	// the result is a string in that format; for json it is a native value.
	// Omitted for every explicitly requested format.
	Format string `json:"format,omitempty"`

	// Spill decision provenance (D2/D24). Populated only on the spill path so
	// both agents and humans can see *why* they got the shape they got.
	// Decided is one of "inline", "spilled", "summary-only". MeasuredBytes is
	// the size of the rows as they would be printed inline, in MeasuredEncoding
	// and in the JSON layout of the output (compact unless stdout is a terminal,
	// see EnvelopeRecordsLayout); --spill=auto compares it with ThresholdBytes.
	Decided          string `json:"decided,omitempty"`
	ThresholdBytes   int64  `json:"threshold_bytes,omitempty"`
	MeasuredBytes    int64  `json:"measured_bytes,omitempty"`
	MeasuredEncoding string `json:"measured_encoding,omitempty"`
	// Streamed marks a result written to disk row by row as it arrived, without
	// ever being held whole. The spill decision was then settled by row count
	// alone, so measured_bytes/measured_encoding are absent: nothing was
	// serialised for inline emission to measure.
	Streamed bool `json:"streamed,omitempty"`

	// EmptyReason explains an empty query result when a cheap follow-up
	// probe found a likely cause. Omitted when the result is not empty or the
	// probes found nothing conclusive.
	EmptyReason *EmptyReason `json:"empty_reason,omitempty"`

	// Truncation markers. Truncated is true whenever the result is not the
	// complete answer because dtctl bounded it: rows were dropped to fit an
	// output budget (Returned < Total, see NextOffset/Next), or string values
	// were clipped to MaxFieldChars (each clipped value ends in "…(+N chars)"
	// and its field is named in TruncatedFields). Both can apply at once.
	Truncated bool `json:"truncated,omitempty"`
	// Returned is the number of rows in the result when an output budget
	// dropped some; Total still counts every row the query returned.
	Returned *int `json:"returned,omitempty"`
	// NextOffset is the zero-based index of the first row the budget dropped.
	NextOffset *int `json:"next_offset,omitempty"`
	// Next is a ready-to-run command that continues at NextOffset without
	// re-querying, when the full result was written to disk. Empty when it was
	// not (spilling disabled); context.suggestions then says how to get the rest.
	Next string `json:"next,omitempty"`
	// BudgetBytes is the output budget that dropped rows, in bytes of the
	// encoded envelope (--max-output-bytes, or --max-output-tokens × 4).
	BudgetBytes int64 `json:"budget_bytes,omitempty"`
	// MaxFieldChars is the per-value cap in runes, set only when it clipped at
	// least one value.
	MaxFieldChars int `json:"max_field_chars,omitempty"`
	// TruncatedFields names the fields with at least one clipped value.
	TruncatedFields []string `json:"truncated_fields,omitempty"`

	// Window is the window `dtctl query` searched, as the response's own
	// metadata states it.
	Window *TimeWindow `json:"window,omitempty"`
}

// TimeWindow is a query window as absolute RFC3339 instants.
type TimeWindow struct {
	From string `json:"from"`
	To   string `json:"to"`
	// Span is the window's length (2h, 24h, 7d), so a reader can hold it
	// against the question without subtracting timestamps.
	Span string `json:"span,omitempty"`
	// Note says the window is the default one: the query named none.
	Note string `json:"note,omitempty"`
}

// MarkFieldsClipped records on the context that values in fields were clipped
// to max runes. A no-op when nothing was clipped, so an unclipped result keeps
// a clean context.
func (c *ResponseContext) MarkFieldsClipped(max int, fields []string) {
	if len(fields) == 0 {
		return
	}
	c.Truncated = true
	c.MaxFieldChars = max
	c.TruncatedFields = fields
}

// EmptyReason is a diagnosed cause of an empty query result. Each finding
// names its basis in Evidence (a sample, or the window that was checked), so
// a consumer never mistakes a sampled observation for a catalog fact.
type EmptyReason struct {
	// Code is "field_not_in_sample" or "metric_not_in_window".
	Code       string   `json:"code"`
	Field      string   `json:"field,omitempty"`
	Metric     string   `json:"metric,omitempty"`
	DataObject string   `json:"data_object,omitempty"`
	DidYouMean []string `json:"did_you_mean,omitempty"`
	SampleSize int      `json:"sample_size,omitempty"`
	Evidence   string   `json:"evidence"`
}

// ErrorDetail is a structured error for machine consumption.
// It lives in the Response envelope when ok=false.
type ErrorDetail struct {
	Code       string `json:"code"`
	Message    string `json:"message"`
	Operation  string `json:"operation,omitempty"`
	StatusCode int    `json:"status_code,omitempty"`
	RequestID  string `json:"request_id,omitempty"`
	// Scope-check fields, populated for code == "insufficient_scope".
	RequiredScopes []string `json:"required_scopes,omitempty"`
	GrantedScopes  []string `json:"granted_scopes,omitempty"`
	MissingScopes  []string `json:"missing_scopes,omitempty"`
	// AlternativeScopes are scope sets the endpoint accepts in place of
	// RequiredScopes; granting any one set in full would also suffice.
	AlternativeScopes [][]string `json:"alternative_scopes,omitempty"`
	// Query-error fields, populated when the DQL API reports where in the
	// query the error is: the span, and the offending line with a caret line
	// marking it.
	Position    *ErrorPosition `json:"position,omitempty"`
	Snippet     string         `json:"snippet,omitempty"`
	Suggestions []string       `json:"suggestions,omitempty"`
}

// ErrorPosition locates an error in a query: 1-based line and column, in
// characters, with the end inclusive. The end is omitted when unknown.
type ErrorPosition struct {
	Line      int `json:"line"`
	Column    int `json:"column"`
	EndLine   int `json:"end_line,omitempty"`
	EndColumn int `json:"end_column,omitempty"`
}

// ClassifyHTTPError maps an HTTP status code to a machine-readable error code.
func ClassifyHTTPError(statusCode int) string {
	switch statusCode {
	case 400:
		return "bad_request"
	case 401:
		return "auth_required"
	case 403:
		return "permission_denied"
	case 404:
		return "not_found"
	case 409:
		return "conflict"
	case 429:
		return "rate_limited"
	default:
		if statusCode >= 500 {
			return "server_error"
		}
		return "error"
	}
}

// AgentPrinter wraps CLI output in a Response envelope for machine consumers.
// It implements the Printer interface.
//
// By default, the result field inside the JSON envelope is encoded as native
// JSON. Callers can switch to TOON via SetResultFormat("toon") for token
// efficiency.
type AgentPrinter struct {
	writer       io.Writer
	ctx          *ResponseContext
	resultFormat string // "json" (default), "toon" or "auto"
	// autoByDefault marks auto as the agent-mode default rather than an
	// explicit -o auto, so a non-JSON choice carries the -o json opt-out hint.
	autoByDefault bool
	jqFilter      string
	metadata      interface{}
	// maxFieldChars clips long string values in the result after the jq
	// filter ran (0 = off); see SetMaxFieldChars.
	maxFieldChars int
}

// NewAgentPrinter creates an AgentPrinter that writes envelope-wrapped JSON to writer.
// The result field defaults to JSON encoding. Use SetResultFormat("toon") to enable
// TOON encoding for token efficiency.
func NewAgentPrinter(writer io.Writer, ctx *ResponseContext) *AgentPrinter {
	if ctx == nil {
		ctx = &ResponseContext{}
	}
	return &AgentPrinter{writer: writer, ctx: ctx, resultFormat: "json"}
}

// SetResultFormat controls how the result field is encoded inside the agent
// envelope. Supported values are "toon", "auto" (chosen per result, reported in
// context.format) and "json" (default). Any other value
// falls back to "json" — the result must be a valid native JSON value inside
// the envelope — and records a warning on the response context, so an agent
// that passed `-o csv` learns its format was not honoured instead of silently
// receiving JSON that looks like it was what it asked for.
func (p *AgentPrinter) SetResultFormat(format string) {
	if IsAutoFormat(format) {
		format = FormatAuto
	}
	switch format {
	case "toon", "json", FormatAuto:
		p.resultFormat = format
	case "":
		// Not an explicit choice; keep the default without warning.
	default:
		p.resultFormat = "json"
		p.addWarning(fmt.Sprintf(
			"-o %s is not supported inside the agent envelope; the result was encoded as JSON (agent mode supports json and toon)",
			format,
		))
	}
}

// UseAutoByDefault selects -o auto as the agent-mode default, used when the
// caller gave no -o. It differs from SetResultFormat("auto") only in that a
// csv/yaml choice adds AutoDefaultSuggestion, since the caller did not ask for
// a non-JSON result and may not expect one.
func (p *AgentPrinter) UseAutoByDefault() {
	p.resultFormat = FormatAuto
	p.autoByDefault = true
}

// Print writes a single result wrapped in the agent envelope.
func (p *AgentPrinter) Print(data interface{}) error {
	transformed, err := ApplyJQ(p.jqFilter, data)
	if err != nil {
		return err
	}
	if p.maxFieldChars > 0 {
		var fields []string
		transformed, fields = ClipValue(transformed, p.maxFieldChars)
		p.ctx.MarkFieldsClipped(p.maxFieldChars, fields)
	}

	result, err := p.encodeResult(transformed)
	if err != nil {
		return err
	}
	resp := Response{
		OK:       true,
		Result:   result,
		Context:  p.ctx,
		Metadata: p.metadata,
	}
	return EncodeEnvelope(p.writer, resp)
}

// encodeResult encodes data according to the configured result format.
// For "toon" it returns a TOON-encoded string (which json.Encoder will
// emit as a JSON string value inside the envelope). For "json" (or any
// other format) it returns the data as-is so json.Encoder serialises it
// as a native JSON value.
//
// If TOON encoding fails, the method falls back to returning the raw data
// (which json.Encoder will serialise as native JSON) and adds a warning
// to the response context so the consumer can detect the fallback.
func (p *AgentPrinter) encodeResult(data interface{}) (interface{}, error) {
	if p.resultFormat == FormatAuto {
		return p.encodeAutoResult(data), nil
	}
	if p.resultFormat != "toon" || data == nil {
		return data, nil
	}

	encoded, err := MarshalTOON(data)
	if err != nil {
		p.addWarning(fmt.Sprintf("TOON encoding failed: %v; fell back to JSON", err))
		return data, nil // fall back to raw data on encoding error
	}

	return encoded, nil
}

// encodeAutoResult encodes data in the format ChooseAutoFormat picks and records
// that format in context.format. A json choice keeps data as a native value. If
// encoding fails, the result falls back to native JSON with a warning, and
// context.format says json so it still describes what the consumer received.
func (p *AgentPrinter) encodeAutoResult(data interface{}) interface{} {
	choice, encoded, err := MarshalAuto(data)
	if err != nil {
		p.ctx.Format = "json"
		p.addWarning(fmt.Sprintf("-o auto encoding failed: %v; fell back to JSON", err))
		return data
	}
	p.ctx.Format = choice.Format
	if choice.Format == "json" {
		return data
	}
	if p.autoByDefault {
		p.ctx.Suggestions = append(p.ctx.Suggestions, AutoDefaultSuggestion(choice.Format))
	}
	return encoded
}

// addWarning appends a warning message to the response context.
func (p *AgentPrinter) addWarning(msg string) {
	p.ctx.Warnings = append(p.ctx.Warnings, msg)
}

// PrintList writes a list result wrapped in the agent envelope.
// If Total has been set via SetTotal, it is included in the context.
func (p *AgentPrinter) PrintList(data interface{}) error {
	return p.Print(data)
}

// SetJQFilter applies a jq transform to the result before envelope encoding.
func (p *AgentPrinter) SetJQFilter(filter string) {
	p.jqFilter = filter
}

// SetMaxFieldChars clips every string value in the result longer than n runes
// (0 = off, the default), marking the clip on the context. It runs after the jq
// filter, so a filter still sees — and can match on — the full values.
func (p *AgentPrinter) SetMaxFieldChars(n int) {
	p.maxFieldChars = n
}

// SetMetadata sets the envelope's metadata sibling (Grail query metadata). It
// keeps metadata reachable next to `result` even when a --jq filter narrowed the
// payload down to the rows, which is how the unfiltered query envelope presents
// it. Nil (the default) omits the field.
func (p *AgentPrinter) SetMetadata(meta interface{}) {
	p.metadata = meta
}

// SetTotal sets the total item count in the response context.
func (p *AgentPrinter) SetTotal(total int) {
	p.ctx.Total = &total
}

// SetResource sets the resource type in the response context.
func (p *AgentPrinter) SetResource(resource string) {
	p.ctx.Resource = resource
}

// SetSuggestions sets the follow-up suggestions in the response context.
func (p *AgentPrinter) SetSuggestions(suggestions []string) {
	p.ctx.Suggestions = suggestions
}

// SetWarnings sets non-fatal warnings in the response context.
func (p *AgentPrinter) SetWarnings(warnings []string) {
	p.ctx.Warnings = warnings
}

// SetDuration sets the operation duration in the response context.
// Only use for commands where timing is meaningful (wait, query, exec).
func (p *AgentPrinter) SetDuration(duration string) {
	p.ctx.Duration = duration
}

// SetLinks sets deep links to the Dynatrace UI in the response context.
func (p *AgentPrinter) SetLinks(links map[string]string) {
	p.ctx.Links = links
}

// SetHasMore indicates whether more results exist beyond the current page.
func (p *AgentPrinter) SetHasMore(hasMore bool) {
	p.ctx.HasMore = hasMore
}

// Context returns the response context for direct manipulation.
func (p *AgentPrinter) Context() *ResponseContext {
	return p.ctx
}

// PrintError writes an error response to the given writer in agent envelope format.
// This is a package-level function because errors are handled centrally in cmd/root.go,
// not through the printer instance.
func PrintError(writer io.Writer, detail *ErrorDetail) error {
	resp := Response{
		OK:    false,
		Error: detail,
	}
	return EncodeEnvelope(writer, resp)
}
