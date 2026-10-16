package exec

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"path/filepath"

	"github.com/dynatrace-oss/dtctl/pkg/output"
	sdkquery "github.com/dynatrace-oss/dtctl/sdk/api/query"
)

// Streaming output for `dtctl query`.
//
// The SDK can deliver rows one at a time (ExecuteAndPollStream), but that only
// bounds memory if what consumes them does not put them back into a slice.
// Most of the output pipeline does: a table needs every row to size its
// columns, CSV needs every row for its header, compaction hoists the columns
// that are constant *across* rows, and --jq addresses the whole document.
//
// So streaming applies where rows reach a destination that takes them one at a
// time — a JSONL spill file or `-o jsonl` on stdout — and everything else keeps
// today's accumulate-then-print path. Even on an eligible invocation the rows
// are buffered at first, so a result that turns out to be small takes exactly
// the same code path it does today; the stream only takes over once the result
// is provably too big to be emitted inline (see streamSwitchRows).

// maxBufferedBytes caps what the buffering phase holds before handing over to
// the stream. The row count below settles the inline decision exactly, but says
// nothing about how fat a row is, so this bounds the phase in bytes too. A
// --spill-threshold above it raises the cap to the threshold (see bufferCap):
// the caller asked for results that large inline, and inline output needs every
// row in hand anyway.
//
// Crossing it can in principle spill a result that compaction would have
// squeezed back under the threshold — that needs every column of a multi-megabyte
// result to be constant, and the row count would have settled it first in all
// but the most contrived shapes. The cost if it ever happens is a result on disk
// instead of inline, which the envelope describes either way.
const maxBufferedBytes = 32 << 20

// streamSinkKind is where a streamed row goes.
type streamSinkKind int

const (
	sinkSpill streamSinkKind = iota // a JSONL spill file, summarised in an agent envelope
	sinkJSONL                       // -o jsonl on stdout
)

// streamPlan is the streaming decision, made before the query runs because the
// sink has to be wired into the SDK call.
type streamPlan struct {
	kind streamSinkKind
	// spillDir is the directory the streamed file is built in. The final name
	// depends on the response metadata, which arrives after the rows, so the
	// file is committed to it only at the end (output.SpillWriter).
	spillDir string
	// switchRows is how many rows may be buffered before the result is known to
	// exceed the spill threshold; 0 streams from the first row.
	switchRows int
	// bufferCap is the estimated size (estimateRowBytes) the buffering phase
	// may reach before it hands over to the stream regardless of the row count.
	bufferCap int64
	// tabular mirrors what printResults passes to ApplySeriesMode for this
	// destination.
	tabular bool
	format  string // effective display format
}

// planStream decides whether this invocation can stream its rows, and where to.
// It mirrors the branches printResults would take, so an ineligible invocation
// is one whose output genuinely needs every row at once.
func (e *DQLExecutor) planStream(opts DQLExecuteOptions) (*streamPlan, bool) {
	format := opts.OutputFormat

	// --jq addresses the whole document, and snapshot decoding reshapes rows
	// per display format; both want the result in hand. --typed casts rows
	// using the response's type block, which the decoder may only reach after
	// the rows it describes. All three are opt-in.
	if opts.JQFilter != "" || opts.Decode != DecodeNone || opts.Typed {
		return nil, false
	}

	// -o jsonl with no spill destination writes rows straight to stdout.
	if !opts.Spill.Enabled() && !opts.AgentMode {
		if format != "jsonl" {
			return nil, false
		}
		return &streamPlan{kind: sinkJSONL, format: format}, true
	}

	// Agent mode under --spill=never always emits the rows inline, so they are
	// all needed at once.
	if !opts.Spill.Enabled() {
		return nil, false
	}

	// Only JSONL can be written row by row: JSON needs array framing the
	// printer owns, CSV a header built from every row, Parquet a full schema.
	spillFormat, err := e.streamedSpillFormat(opts)
	if err != nil || spillFormat != "jsonl" {
		return nil, false
	}

	dir, ok := e.streamedSpillDir(opts)
	if !ok {
		return nil, false
	}

	return &streamPlan{
		kind:       sinkSpill,
		spillDir:   dir,
		switchRows: streamSwitchRows(e.outW(), opts, format),
		bufferCap:  bufferCap(opts),
		tabular:    seriesTabular(format, opts),
		format:     format,
	}, true
}

// streamedSpillFormat is the format a streamed spill would be written in.
func (e *DQLExecutor) streamedSpillFormat(opts DQLExecuteOptions) (string, error) {
	if opts.Spill.ToPath != "" {
		return spillFormatForPath(opts.Spill.ToPath, opts.Spill.Format)
	}
	if opts.Spill.Format == "" {
		return defaultSpillFormat, nil
	}
	return opts.Spill.Format, validateSpillFormat(opts.Spill.Format)
}

// streamedSpillDir is the directory a streamed spill file is built in. It is
// the destination's own directory for --spill-to, and the managed per-context
// cache otherwise — the same choices resolveSpillTarget makes, minus the file
// name, which depends on metadata that has not arrived yet.
func (e *DQLExecutor) streamedSpillDir(opts DQLExecuteOptions) (string, bool) {
	if opts.Spill.ToPath != "" {
		dir := filepath.Dir(opts.Spill.ToPath)
		if !output.ProbeWritable(dir) {
			return "", false
		}
		return dir, true
	}
	base, managed, err := output.SpillBaseDir(opts.Spill.Dir)
	if err != nil {
		// No writable location: the buffered path degrades to summary-only,
		// which still needs the decision logic there.
		return "", false
	}
	if managed {
		return filepath.Join(base, output.SanitizeContextName(opts.ContextName)), true
	}
	return base, true
}

// streamSwitchRows is the largest number of rows that can still be emitted
// inline, and so the most the buffering phase may hold. Any count above it
// exceeds the threshold whatever the rows turn out to contain, because every
// row costs at least minRowCost in the envelope's encoding. At or below it the
// buffered path is kept, which is what keeps a small result's output exactly
// what it is today. The bound is taken in the layout the buffered path measures
// (inlineLayout), so the two settle the same result the same way: costed
// indented, a compact envelope would switch to streaming at under half the rows
// the buffered path still emits inline.
func streamSwitchRows(stdout io.Writer, opts DQLExecuteOptions, format string) int {
	if opts.Spill.Mode == SpillAlways {
		return 0
	}
	per, frame := minRowCost(format, inlineLayout(stdout, opts, format))
	if opts.Spill.Threshold <= 0 || per <= 0 {
		return 0
	}
	n := (opts.Spill.Threshold - frame) / per
	if n < 0 {
		return 0
	}
	return int(n)
}

// bufferCap is how large the buffering phase may grow, by estimateRowBytes,
// before it hands over to the stream. It is maxBufferedBytes, or the spill
// threshold when that is larger: the byte cap is a memory bound, not a size
// measurement, so it must not overrule a threshold the caller raised above it
// and spill a result the buffered path would have emitted inline.
func bufferCap(opts DQLExecuteOptions) int64 {
	if opts.Spill.Threshold > maxBufferedBytes {
		return opts.Spill.Threshold
	}
	return maxBufferedBytes
}

// minRowCost is a lower bound on what one row costs in the inline envelope (per)
// and on the fixed cost around the rows (frame), in the given JSON layout.
//
// In JSON and YAML every key a row carries adds bytes, so compaction's best case
// — every column hoisted, an empty row — is also the cheapest row, and it is
// measured rather than assumed. The tabular encodings break that: CSV and TOON
// move the keys into a header, so a narrow row can be cheaper than an empty one
// (an empty row makes TOON fall back to its list form). -o auto can pick CSV. For
// those the only bound that holds for any row is the newline that ends it.
func minRowCost(format string, layout output.JSONLayout) (per, frame int64) {
	enc := output.NormalizeMeasureEncoding(format)
	if output.IsAutoFormat(format) || (enc != "json" && enc != "yaml") {
		return 1, 0
	}
	empty := map[string]interface{}{}
	one, _ := output.MeasureSerializedBytes([]map[string]interface{}{empty}, format, layout)
	two, _ := output.MeasureSerializedBytes([]map[string]interface{}{empty, empty}, format, layout)
	per = two - one
	if per <= 0 {
		return 1, 0
	}
	return per, one - per
}

// seriesTabular mirrors the tabular flag printResults computes for
// ApplySeriesMode, so a streamed row is transformed exactly as a buffered one.
func seriesTabular(format string, opts DQLExecuteOptions) bool {
	switch format {
	case "", "table", "wide":
		return !opts.AgentMode
	case "csv":
		return true
	}
	return false
}

// executeStreaming runs the query with rows delivered one at a time and hands
// the outcome to the collector, which either replays a buffered result through
// the ordinary output path or finishes the stream it started.
func (e *DQLExecutor) executeStreaming(ctx context.Context, query string, opts DQLExecuteOptions, plan *streamPlan) error {
	col := newStreamCollector(e, plan, opts)
	defer col.abort()

	result, err := e.runQuery(ctx, query, opts, col)
	if col.err != nil {
		// The sink failed; the SDK only relays that error wrapped in its decode
		// context, which would misdescribe it.
		return col.err
	}
	if err != nil {
		return err
	}
	if result == nil {
		return nil // context was cancelled; message already printed to stderr
	}
	return col.finish(query, result, opts)
}

// streamCollector receives rows as they are decoded. It buffers at first and
// switches to the streamed sink once the result is provably too big to emit
// inline; from then on each row is written out and released, and everything the
// summary needs is folded into accumulators instead.
type streamCollector struct {
	e    *DQLExecutor
	plan *streamPlan
	opts DQLExecuteOptions

	buf       []map[string]interface{}
	bufBytes  int64
	streaming bool

	rows    int
	stats   *output.StatsAccumulator
	compact *output.CompactionAccumulator
	sample  []map[string]interface{}

	writer *output.SpillWriter
	stdout *bufio.Writer // -o jsonl: rows are batched into few writes
	enc    *json.Encoder
	// writeFailed records that the managed spill file could not be written. The
	// rows are still folded into the summary, and the result degrades to
	// summary-only, as a buffered result does when its write fails (D8).
	writeFailed bool

	// beforeStdout runs once, just before the first row goes to stdout. The
	// query runner sets it to settle the progress line on stderr, which would
	// otherwise keep redrawing between the rows on a shared terminal.
	beforeStdout func()

	effect output.SeriesEffect
	err    error
}

func newStreamCollector(e *DQLExecutor, plan *streamPlan, opts DQLExecuteOptions) *streamCollector {
	return &streamCollector{e: e, plan: plan, opts: opts}
}

// observe takes one decoded row.
func (c *streamCollector) observe(row map[string]interface{}) error {
	if c.err != nil {
		return c.err
	}

	if !c.streaming {
		// Held untransformed: a result that stays small is replayed through
		// printRecords, which applies --series/--precision itself.
		c.buf = append(c.buf, row)
		c.bufBytes += int64(estimateRowBytes(row))
		if len(c.buf) > c.plan.switchRows || c.bufBytes > c.plan.bufferCap {
			if err := c.startStreaming(); err != nil {
				c.err = err
				return err
			}
		}
		return nil
	}
	return c.write(c.transform(row))
}

// transform applies the per-row reshaping printResults applies to the whole
// slice. Both are row-independent, so folding the effect flags row by row
// yields the same advice the buffered path produces.
func (c *streamCollector) transform(row map[string]interface{}) map[string]interface{} {
	if c.opts.Series.Kind == output.SeriesFull && c.opts.Precision <= 0 {
		return row
	}
	out, eff := output.ApplySeriesModeWithEffect(
		[]map[string]interface{}{row}, c.opts.Series, c.opts.Precision, c.plan.tabular)
	c.effect.Summarized = c.effect.Summarized || eff.Summarized
	c.effect.Rounded = c.effect.Rounded || eff.Rounded
	if len(out) == 1 {
		return out[0]
	}
	return row
}

// startStreaming opens the sink and hands it every buffered row.
func (c *streamCollector) startStreaming() error {
	if c.plan.kind == sinkSpill {
		w, err := output.NewSpillWriter(c.plan.spillDir)
		if err != nil {
			if c.opts.Spill.ToPath != "" {
				return fmt.Errorf("failed to write spill file %q: %w", c.opts.Spill.ToPath, err)
			}
			c.writeFailed = true
			c.enc = json.NewEncoder(io.Discard)
		} else {
			c.writer = w
			c.enc = json.NewEncoder(w.Writer())
		}
	} else {
		if c.beforeStdout != nil {
			c.beforeStdout()
		}
		c.stdout = bufio.NewWriterSize(c.e.outW(), 64<<10)
		c.enc = json.NewEncoder(c.stdout)
	}
	c.stats = output.NewStatsAccumulator(output.DefaultStatsTopK, output.DefaultStatsMaxDistinct)
	c.compact = output.NewCompactionAccumulator()
	c.streaming = true

	buffered := c.buf
	c.buf, c.bufBytes = nil, 0
	for _, row := range buffered {
		if err := c.write(c.transform(row)); err != nil {
			return err
		}
	}
	return nil
}

// write folds a row into the summary accumulators and emits it.
func (c *streamCollector) write(row map[string]interface{}) error {
	c.rows++
	if c.plan.kind == sinkSpill {
		c.stats.Observe(row)
		c.compact.Observe(row)
		if len(c.sample) < output.DefaultSampleRows {
			c.sample = append(c.sample, row)
		}
	}
	// json.Encoder writes one compact object per line — the JSONL contract, and
	// what the -o jsonl and jsonl-spill writers produce for a buffered result.
	if err := c.enc.Encode(row); err != nil {
		if c.plan.kind == sinkSpill && c.opts.Spill.ToPath == "" {
			// A managed write failed (a full disk, say). Keep summarising the
			// remaining rows and report the overview only.
			c.writeFailed = true
			c.writer.Abort()
			c.writer = nil
			c.enc = json.NewEncoder(io.Discard)
			return nil
		}
		if c.plan.kind == sinkSpill {
			err = fmt.Errorf("failed to write spill file %q: %w", c.opts.Spill.ToPath, err)
		}
		c.err = err
		return err
	}
	return nil
}

// flushStdout pushes the -o jsonl rows still batched in memory to stdout.
func (c *streamCollector) flushStdout() error {
	if c.stdout == nil {
		return nil
	}
	return c.stdout.Flush()
}

// abort discards an uncommitted spill file, and flushes the -o jsonl rows
// already written so a failed query still leaves them on stdout, as it would
// have unbatched.
func (c *streamCollector) abort() {
	if c.writer != nil {
		c.writer.Abort()
	}
	_ = c.flushStdout()
}

// finish emits the result. A collector that never left the buffering phase
// holds every row, so it replays the ordinary output path unchanged; one that
// started streaming has already emitted the rows and only has to close out.
func (c *streamCollector) finish(query string, result *DQLQueryResponse, opts DQLExecuteOptions) error {
	if !c.streaming {
		return c.e.printRecords(query, result, orEmptyRows(c.buf), opts)
	}
	opts.seriesAdvice = defaultSeriesAdvice(c.effect, opts)
	if c.plan.kind == sinkJSONL {
		if err := c.flushStdout(); err != nil {
			return err
		}
		// The rows are already on stdout; the notifications follow on stderr.
		return c.e.finishStreamedJSONL(query, result)
	}
	return c.e.finishStreamedSpill(query, result, c, opts)
}

// orEmptyRows keeps a nil buffer distinguishable from "no rows at all" for the
// output path, which treats a nil slice as an absent result.
func orEmptyRows(rows []map[string]interface{}) []map[string]interface{} {
	if rows == nil {
		return []map[string]interface{}{}
	}
	return rows
}

// estimateRowBytes approximates a row's in-memory cost for the buffering cap.
// It counts keys, strings and a fixed cost per scalar, recursing into nested
// objects and arrays, rather than serialising the row — the cap only has to
// bound the buffer, not measure it. Nested values have to be walked: a row
// whose payload is one large array or object would otherwise count as a
// single scalar and let the buffer grow far past the cap.
func estimateRowBytes(row map[string]interface{}) int {
	return estimateValueBytes(row)
}

func estimateValueBytes(v interface{}) int {
	switch x := v.(type) {
	case string:
		return len(x) + 16
	case map[string]interface{}:
		n := 16
		for k, e := range x {
			n += len(k) + estimateValueBytes(e)
		}
		return n
	case []interface{}:
		n := 16
		for _, e := range x {
			n += estimateValueBytes(e)
		}
		return n
	case []map[string]interface{}:
		n := 16
		for _, e := range x {
			n += estimateValueBytes(e)
		}
		return n
	default:
		return 16
	}
}

// streamCall picks the accumulating or the streaming SDK driver. Everything
// around it — progress reporting, token refresh, cancellation — is shared.
func streamCall(ctx context.Context, handler *sdkquery.Handler, req sdkquery.ExecuteRequest, sdkOpts sdkquery.ExecuteAndPollOptions, onRecord func(map[string]interface{}) error) (*sdkquery.Response, error) {
	if onRecord == nil {
		return handler.ExecuteAndPollWithOptions(ctx, req, sdkOpts)
	}
	return handler.ExecuteAndPollStream(ctx, req, sdkOpts, onRecord)
}
