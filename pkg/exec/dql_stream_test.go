package exec

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/dynatrace-oss/dtctl/pkg/output"
)

func TestPlanStream_Eligibility(t *testing.T) {
	spillAuto := SpillOptions{Mode: SpillAuto, Threshold: 50 << 10, Dir: t.TempDir()}

	cases := []struct {
		name string
		opts DQLExecuteOptions
		want bool
		kind streamSinkKind
	}{
		{
			name: "agent mode with auto spill streams to a spill file",
			opts: DQLExecuteOptions{AgentMode: true, Spill: spillAuto},
			want: true, kind: sinkSpill,
		},
		{
			name: "-o jsonl without spilling streams to stdout",
			opts: DQLExecuteOptions{OutputFormat: "jsonl"},
			want: true, kind: sinkJSONL,
		},
		{
			name: "--jq needs the whole document",
			opts: DQLExecuteOptions{AgentMode: true, Spill: spillAuto, JQFilter: ".records"},
			want: false,
		},
		{
			name: "--typed needs the response type block",
			opts: DQLExecuteOptions{AgentMode: true, Spill: spillAuto, Typed: true},
			want: false,
		},
		{
			name: "snapshot decoding reshapes rows per format",
			opts: DQLExecuteOptions{AgentMode: true, Spill: spillAuto, Decode: DecodeSimplified},
			want: false,
		},
		{
			name: "agent mode under --spill=never always emits rows inline",
			opts: DQLExecuteOptions{AgentMode: true, Spill: SpillOptions{Mode: SpillNever}},
			want: false,
		},
		{
			name: "a json spill file needs array framing",
			opts: DQLExecuteOptions{AgentMode: true, Spill: SpillOptions{Mode: SpillAuto, Threshold: 1, Dir: t.TempDir(), Format: "json"}},
			want: false,
		},
		{
			name: "a csv spill file needs a header built from every row",
			opts: DQLExecuteOptions{AgentMode: true, Spill: SpillOptions{Mode: SpillAuto, Threshold: 1, Dir: t.TempDir(), Format: "csv"}},
			want: false,
		},
		{
			name: "-o table is not row-at-a-time",
			opts: DQLExecuteOptions{OutputFormat: "table"},
			want: false,
		},
		{
			name: "-o csv is not row-at-a-time",
			opts: DQLExecuteOptions{OutputFormat: "csv"},
			want: false,
		},
	}

	e := &DQLExecutor{}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			plan, ok := e.planStream(tc.opts)
			if ok != tc.want {
				t.Fatalf("planStream() ok = %v, want %v", ok, tc.want)
			}
			if ok && plan.kind != tc.kind {
				t.Errorf("plan.kind = %v, want %v", plan.kind, tc.kind)
			}
		})
	}
}

// The switch point may never be too early: one row past it must exceed the
// threshold whatever the rows look like, or a result that fits inline would be
// spilled. The shapes cover the cheapest rows each encoding has — an empty row
// in JSON and YAML, a single short column in the tabular ones, where the keys
// live in a header. -o auto can pick CSV for narrow rows, and is the agent
// default, so it gets the tabular bound too. Both JSON layouts are covered:
// indented (the JSON printer) and compact (a piped agent envelope), each
// measured as stdout (piped here) prints it. The compact case is the
// #570 guard for the stream: costed indented, a piped agent query switched to
// streaming at under half the rows the buffered path still emits inline.
func TestStreamSwitchRows_NeverSpillsARowSetThatFits(t *testing.T) {
	pipedStdout(t)
	shapes := map[string]func(i int) map[string]interface{}{
		"empty":        func(int) map[string]interface{} { return map[string]interface{}{} },
		"empty string": func(int) map[string]interface{} { return map[string]interface{}{"a": ""} },
		"one digit":    func(i int) map[string]interface{} { return map[string]interface{}{"a": fmt.Sprint(i % 10)} },
		"small number": func(i int) map[string]interface{} { return map[string]interface{}{"a": float64(i % 10)} },
	}
	for _, agent := range []bool{false, true} {
		for _, format := range []string{"json", "jsonl", "yaml", "toon", "csv", "auto"} {
			opts := DQLExecuteOptions{AgentMode: agent, Spill: SpillOptions{Mode: SpillAuto, Threshold: 50 << 10}}
			switchAt := streamSwitchRows(io.Discard, opts, format)
			for name, shape := range shapes {
				t.Run(fmt.Sprintf("agent=%v/%s/%s", agent, format, name), func(t *testing.T) {
					rows := make([]map[string]interface{}, switchAt+1)
					for i := range rows {
						rows[i] = shape(i)
					}
					if got, _ := output.MeasureSerializedBytes(rows, format, printedLayout(agent, format)); got <= opts.Spill.Threshold {
						t.Errorf("%d rows measure %d bytes, within the %d-byte threshold — the switch at %d is too early",
							len(rows), got, opts.Spill.Threshold, switchAt)
					}
				})
			}
		}
	}
}

// Where the empty row is the cheapest one (JSON, YAML), the switch point is
// also exact: holding one row fewer would have streamed a result that fitted.
func TestStreamSwitchRows_IsExactWhereTheEmptyRowIsCheapest(t *testing.T) {
	pipedStdout(t)
	for _, agent := range []bool{false, true} {
		for _, format := range []string{"json", "jsonl", "yaml"} {
			t.Run(fmt.Sprintf("agent=%v/%s", agent, format), func(t *testing.T) {
				opts := DQLExecuteOptions{AgentMode: agent, Spill: SpillOptions{Mode: SpillAuto, Threshold: 50 << 10}}
				switchAt := streamSwitchRows(io.Discard, opts, format)
				rows := make([]map[string]interface{}, switchAt)
				for i := range rows {
					rows[i] = map[string]interface{}{}
				}
				if fits, _ := output.MeasureSerializedBytes(rows, format, printedLayout(agent, format)); fits > opts.Spill.Threshold {
					t.Errorf("%d empty rows measure %d bytes, over the %d-byte threshold — the switch is too late",
						switchAt, fits, opts.Spill.Threshold)
				}
			})
		}
	}
}

// printedLayout is the JSON layout the inline rows are printed in to a piped
// stdout: a compact envelope in agent mode, and otherwise JSON lines for
// -o jsonl or {"records": [...]} from the JSON printer.
func printedLayout(agent bool, format string) output.JSONLayout {
	switch {
	case agent:
		return output.JSONLayout{}
	case format == "jsonl":
		return output.JSONLinesLayout
	}
	return output.IndentedJSONLayout(1)
}

func TestStreamSwitchRows_SpillAlwaysStreamsFromTheFirstRow(t *testing.T) {
	opts := DQLExecuteOptions{Spill: SpillOptions{Mode: SpillAlways, Threshold: 50 << 10}}
	if got := streamSwitchRows(io.Discard, opts, "json"); got != 0 {
		t.Errorf("streamSwitchRows() = %d, want 0 for --spill=always", got)
	}
}

// bigRecords builds n distinct rows sharing two constant columns, so the
// compaction, stats and sample accumulators all have something to find.
func bigRecords(n int) []map[string]interface{} {
	out := make([]map[string]interface{}, n)
	for i := range out {
		out[i] = map[string]interface{}{
			"timestamp":       fmt.Sprintf("2026-06-21T00:%02d:%02dZ", i/60%60, i%60),
			"content":         fmt.Sprintf("request %d completed", i),
			"loglevel":        []string{"INFO", "WARN", "ERROR"}[i%3],
			"k8s.namespace":   "production",
			"dt.entity.host":  "HOST-1",
			"response.status": float64(200 + i%3),
			"optional":        nil,
			"trace_id":        partialNull(i),
		}
	}
	return out
}

// partialNull is null on every other row, so compaction keeps the column but
// drops its null values.
func partialNull(i int) interface{} {
	if i%2 == 0 {
		return nil
	}
	return fmt.Sprintf("trace-%d", i)
}

// feed runs rows through a collector exactly as the SDK would.
func feed(t *testing.T, e *DQLExecutor, plan *streamPlan, opts DQLExecuteOptions, rows []map[string]interface{}) *streamCollector {
	t.Helper()
	col := newStreamCollector(e, plan, opts)
	for _, r := range rows {
		if err := col.observe(r); err != nil {
			t.Fatalf("observe: %v", err)
		}
	}
	return col
}

// The streamed envelope must describe the result the same way the buffered one
// does. Only the spill-measurement fields may differ: a streamed result is
// never serialised for inline emission, so there is nothing to measure.
func TestStreamedSpillResponse_MatchesBufferedEnvelope(t *testing.T) {
	const rows = 400
	records := bigRecords(rows)
	result, _ := sampleResult(false)
	result.Records = nil

	bufferedOpts := DQLExecuteOptions{
		AgentMode: true, Compact: true, ContextName: "prod", TenantID: "abc12345",
		Spill: SpillOptions{Mode: SpillAlways, Threshold: 1 << 10, Dir: t.TempDir(), Format: "jsonl"},
	}
	streamedOpts := bufferedOpts
	streamedOpts.Spill.Dir = t.TempDir()

	e := &DQLExecutor{}
	buffered, handled, err := e.buildSpillResponse("fetch logs", result, records, "json", bufferedOpts)
	if err != nil || !handled {
		t.Fatalf("buildSpillResponse: handled=%v err=%v", handled, err)
	}

	plan, ok := e.planStream(streamedOpts)
	if !ok {
		t.Fatal("planStream() declined an --spill=always jsonl agent invocation")
	}
	col := feed(t, e, plan, streamedOpts, records)
	defer col.abort()
	if !col.streaming {
		t.Fatal("collector never left the buffering phase under --spill=always")
	}
	streamed, err := e.buildStreamedSpillResponse("fetch logs", result, col, streamedOpts)
	if err != nil {
		t.Fatalf("buildStreamedSpillResponse: %v", err)
	}

	bm := buffered.Result.(*output.ResultFileManifest)
	sm := streamed.Result.(*output.ResultFileManifest)

	if bm.Rows != sm.Rows {
		t.Errorf("rows: streamed %d, buffered %d", sm.Rows, bm.Rows)
	}
	if !reflect.DeepEqual(bm.Constant, sm.Constant) {
		t.Errorf("constant:\n streamed %#v\n buffered %#v", sm.Constant, bm.Constant)
	}
	if !reflect.DeepEqual(bm.NullColumns, sm.NullColumns) {
		t.Errorf("null_columns: streamed %v, buffered %v", sm.NullColumns, bm.NullColumns)
	}
	if !reflect.DeepEqual(bm.SampleRows, sm.SampleRows) {
		t.Errorf("sample_rows:\n streamed %#v\n buffered %#v", sm.SampleRows, bm.SampleRows)
	}
	if !reflect.DeepEqual(bm.ColumnsOmitted, sm.ColumnsOmitted) {
		t.Errorf("columns_omitted: streamed %v, buffered %v", sm.ColumnsOmitted, bm.ColumnsOmitted)
	}
	if bs, ss := mustJSON(t, bm.Columns), mustJSON(t, sm.Columns); bs != ss {
		t.Errorf("column stats differ:\n streamed %s\n buffered %s", ss, bs)
	}

	if streamed.Context.Decided != buffered.Context.Decided {
		t.Errorf("decided: streamed %q, buffered %q", streamed.Context.Decided, buffered.Context.Decided)
	}
	// The two runs spill into different temp dirs, so compare with the dir
	// itself elided — the file name, which keys off the query, must still match.
	elide := func(lines []string, dir string) []string {
		out := make([]string, len(lines))
		for i, l := range lines {
			out[i] = strings.ReplaceAll(l, dir, "<dir>")
		}
		return out
	}
	gotSug := elide(streamed.Context.Suggestions, streamedOpts.Spill.Dir)
	wantSug := elide(buffered.Context.Suggestions, bufferedOpts.Spill.Dir)
	if !reflect.DeepEqual(gotSug, wantSug) {
		t.Errorf("suggestions:\n streamed %v\n buffered %v", gotSug, wantSug)
	}
	if !reflect.DeepEqual(streamed.Context.Warnings, buffered.Context.Warnings) {
		t.Errorf("warnings:\n streamed %v\n buffered %v", streamed.Context.Warnings, buffered.Context.Warnings)
	}
	if !streamed.Context.Streamed {
		t.Error("context.streamed should mark a result that was never held")
	}
	if streamed.Context.MeasuredBytes != 0 {
		t.Errorf("context.measured_bytes = %d, want 0 — nothing was serialised inline to measure", streamed.Context.MeasuredBytes)
	}

	// Both files must hold the same rows.
	if bufLines, strLines := readJSONL(t, bm.Path), readJSONL(t, sm.Path); !reflect.DeepEqual(bufLines, strLines) {
		t.Errorf("spilled rows differ: streamed %d rows, buffered %d rows", len(strLines), len(bufLines))
	}
}

// A result small enough to still fit inline must never reach the stream: it
// takes the buffered path, which is what keeps today's output byte-identical.
func TestStreamCollector_SmallResultStaysBuffered(t *testing.T) {
	records := bigRecords(5)
	opts := DQLExecuteOptions{
		AgentMode: true, Compact: true,
		Spill: SpillOptions{Mode: SpillAuto, Threshold: 50 << 10, Dir: t.TempDir(), Format: "jsonl"},
	}
	e := &DQLExecutor{}
	plan, ok := e.planStream(opts)
	if !ok {
		t.Fatal("planStream() declined an auto-spill agent invocation")
	}
	col := feed(t, e, plan, opts, records)
	defer col.abort()

	if col.streaming {
		t.Error("a 5-row result should not have switched to the stream")
	}
	if len(col.buf) != len(records) {
		t.Errorf("buffered %d rows, want %d", len(col.buf), len(records))
	}
	if col.writer != nil {
		t.Error("no spill file should have been opened for a buffered result")
	}
}

// Past the switch point the collector must stop holding rows.
func TestStreamCollector_ReleasesRowsOnceStreaming(t *testing.T) {
	opts := DQLExecuteOptions{
		AgentMode: true, Compact: true,
		Spill: SpillOptions{Mode: SpillAuto, Threshold: 512, Dir: t.TempDir(), Format: "jsonl"},
	}
	e := &DQLExecutor{}
	plan, ok := e.planStream(opts)
	if !ok {
		t.Fatal("planStream() declined an auto-spill agent invocation")
	}
	records := bigRecords(plan.switchRows + 50)
	col := feed(t, e, plan, opts, records)
	defer col.abort()

	if !col.streaming {
		t.Fatalf("%d rows past a switch point of %d should have started the stream", len(records), plan.switchRows)
	}
	if col.buf != nil {
		t.Errorf("collector still holds %d rows after switching to the stream", len(col.buf))
	}
	if col.rows != len(records) {
		t.Errorf("counted %d rows, want %d", col.rows, len(records))
	}
	if col.writer.Bytes() == 0 {
		t.Error("nothing was written to the spill file")
	}
}

// --spill=always writes even a one-row result through the stream, and the file
// holds exactly what arrived.
func TestStreamedSpill_WritesEveryRow(t *testing.T) {
	records := bigRecords(250)
	result, _ := sampleResult(false)
	result.Records = nil
	opts := DQLExecuteOptions{
		AgentMode: true, Compact: true,
		Spill: SpillOptions{Mode: SpillAlways, Threshold: 50 << 10, Dir: t.TempDir(), Format: "jsonl"},
	}

	e := &DQLExecutor{}
	plan, _ := e.planStream(opts)
	col := feed(t, e, plan, opts, records)
	defer col.abort()

	resp, err := e.buildStreamedSpillResponse("fetch logs", result, col, opts)
	if err != nil {
		t.Fatalf("buildStreamedSpillResponse: %v", err)
	}
	m := resp.Result.(*output.ResultFileManifest)
	if m.Kind != output.KindResultFile {
		t.Fatalf("kind = %q, want %q", m.Kind, output.KindResultFile)
	}
	if m.Rows != len(records) {
		t.Errorf("manifest rows = %d, want %d", m.Rows, len(records))
	}

	got := readJSONL(t, m.Path)
	if len(got) != len(records) {
		t.Fatalf("spill file holds %d rows, want %d", len(got), len(records))
	}
	for i := range records {
		want := mustJSON(t, records[i])
		if got[i] != want {
			t.Fatalf("row %d:\n got %s\nwant %s", i, got[i], want)
		}
	}

	// The sidecar is written last, so its presence implies a complete file.
	if _, err := os.Stat(output.SidecarPathFor(m.Path)); err != nil {
		t.Errorf("sidecar manifest missing: %v", err)
	}
}

// An abandoned stream must leave no committed file behind, only a .tmp for the
// TTL prune.
func TestStreamCollector_AbortLeavesNoSpillFile(t *testing.T) {
	dir := t.TempDir()
	opts := DQLExecuteOptions{
		AgentMode: true,
		Spill:     SpillOptions{Mode: SpillAlways, Threshold: 50 << 10, Dir: dir, Format: "jsonl"},
	}
	e := &DQLExecutor{}
	plan, _ := e.planStream(opts)
	col := feed(t, e, plan, opts, bigRecords(10))
	col.abort()

	var leftovers []string
	_ = filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err == nil && info != nil && !info.IsDir() {
			leftovers = append(leftovers, path)
		}
		return nil
	})
	for _, p := range leftovers {
		if !strings.HasSuffix(p, ".tmp") {
			t.Errorf("abort left a file behind: %s", p)
		}
	}
}

// Series/precision reshaping is row-independent, so folding it row by row must
// produce exactly what the buffered path produces in one call.
func TestStreamCollector_PerRowTransformMatchesBuffered(t *testing.T) {
	records := []map[string]interface{}{
		{"host": "web-01", "latency": 1.23456789},
		{"host": "web-02", "latency": 9.87654321},
		{"host": "web-03", "latency": 0.000123456789},
	}
	opts := DQLExecuteOptions{
		AgentMode: true, Precision: 4, Series: output.SeriesMode{Kind: output.SeriesFull},
		Spill: SpillOptions{Mode: SpillAuto, Threshold: 50 << 10, Dir: t.TempDir(), Format: "jsonl"},
	}

	want, wantEffect := output.ApplySeriesModeWithEffect(records, opts.Series, opts.Precision, false)

	e := &DQLExecutor{}
	plan, _ := e.planStream(opts)
	col := newStreamCollector(e, plan, opts)
	got := make([]map[string]interface{}, len(records))
	for i, r := range records {
		got[i] = col.transform(r)
	}

	if !reflect.DeepEqual(got, want) {
		t.Errorf("per-row transform:\n got %#v\nwant %#v", got, want)
	}
	if col.effect != wantEffect {
		t.Errorf("series effect = %+v, want %+v", col.effect, wantEffect)
	}
}

// A result that stays below the switch point is handed to printRecords, which
// applies --series/--precision itself. The buffer must hold the rows as they
// arrived: transformed twice, the second pass finds nothing left to round and
// the envelope loses the advice that names the opt-out flag.
func TestStreamCollector_BuffersRowsUntransformed(t *testing.T) {
	records := []map[string]interface{}{{"latency": 1.23456789}, {"latency": 9.87654321}}
	opts := DQLExecuteOptions{
		AgentMode: true, Precision: 3, PrecisionDefaulted: true, Series: output.SeriesMode{Kind: output.SeriesFull},
		Spill: SpillOptions{Mode: SpillAuto, Threshold: 50 << 10, Dir: t.TempDir(), Format: "jsonl"},
	}
	e := &DQLExecutor{}
	plan, _ := e.planStream(opts)
	col := feed(t, e, plan, opts, records)
	defer col.abort()

	if !reflect.DeepEqual(col.buf, records) {
		t.Errorf("buffered rows were reshaped before printRecords saw them:\n got %#v\nwant %#v", col.buf, records)
	}
}

// A managed spill that cannot be written degrades to a summary, as a buffered
// result does (D8), rather than failing the whole query.
func TestStreamedSpill_UnwritableManagedDirDegradesToSummary(t *testing.T) {
	blocker := filepath.Join(t.TempDir(), "not-a-dir")
	if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	opts := DQLExecuteOptions{
		AgentMode: true, Compact: true, ContextName: "prod",
		Spill: SpillOptions{Mode: SpillAlways, Threshold: 1 << 10, Dir: t.TempDir(), Format: "jsonl"},
	}
	e := &DQLExecutor{}
	plan, ok := e.planStream(opts)
	if !ok {
		t.Fatal("planStream() declined an --spill=always jsonl agent invocation")
	}
	// The directory went bad after planning: MkdirAll cannot create a
	// directory under a regular file.
	plan.spillDir = filepath.Join(blocker, "sub")

	records := bigRecords(20)
	col := feed(t, e, plan, opts, records)
	defer col.abort()
	if col.err != nil {
		t.Fatalf("a managed write failure aborted the query: %v", col.err)
	}

	result, _ := sampleResult(false)
	result.Records = nil
	resp, err := e.buildStreamedSpillResponse("fetch logs", result, col, opts)
	if err != nil {
		t.Fatalf("buildStreamedSpillResponse: %v", err)
	}
	m := resp.Result.(*output.ResultFileManifest)
	if m.Kind != output.KindSummaryOnly || resp.Context.Decided != "summary-only" {
		t.Errorf("kind=%q decided=%q, want summary-only", m.Kind, resp.Context.Decided)
	}
	if m.Rows != len(records) {
		t.Errorf("rows = %d, want %d: the summary must still count every row", m.Rows, len(records))
	}
	found := false
	for _, w := range resp.Context.Warnings {
		if strings.Contains(w, "spill write failed") {
			found = true
		}
	}
	if !found {
		t.Errorf("warnings %v do not say the write failed", resp.Context.Warnings)
	}
}

// An explicit --spill-to is a destination the caller pinned, so a failure
// there is an error, as it is on the buffered path.
func TestStreamedSpill_UnwritableExplicitTargetIsAnError(t *testing.T) {
	blocker := filepath.Join(t.TempDir(), "not-a-dir")
	if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(t.TempDir(), "out.jsonl")
	opts := DQLExecuteOptions{
		AgentMode: true,
		Spill:     SpillOptions{Mode: SpillAlways, Threshold: 1 << 10, ToPath: target},
	}
	e := &DQLExecutor{}
	plan, ok := e.planStream(opts)
	if !ok {
		t.Fatal("planStream() declined a --spill-to .jsonl agent invocation")
	}
	plan.spillDir = filepath.Join(blocker, "sub")

	col := newStreamCollector(e, plan, opts)
	defer col.abort()
	err := col.observe(bigRecords(1)[0])
	if err == nil || !strings.Contains(err.Error(), "failed to write spill file") {
		t.Fatalf("observe() = %v, want a spill write error naming the target", err)
	}
}

func readJSONL(t *testing.T, path string) []string {
	t.Helper()
	f, err := os.Open(path) //nolint:gosec // test-local temp path
	if err != nil {
		t.Fatalf("open %s: %v", path, err)
	}
	defer func() { _ = f.Close() }()

	var out []string
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64<<10), 4<<20)
	for sc.Scan() {
		if line := strings.TrimSpace(sc.Text()); line != "" {
			out = append(out, line)
		}
	}
	if err := sc.Err(); err != nil {
		t.Fatalf("scan %s: %v", path, err)
	}
	return out
}

func mustJSON(t *testing.T, v interface{}) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return string(b)
}

// A row whose weight sits in a nested array or object must count for that
// weight: the buffering cap is only a bound if the estimate sees the payload.
func TestEstimateRowBytes_CountsNestedValues(t *testing.T) {
	long := strings.Repeat("x", 100)
	items := make([]interface{}, 1000)
	for i := range items {
		items[i] = map[string]interface{}{"msg": long}
	}
	row := map[string]interface{}{
		"nested": map[string]interface{}{"items": items},
	}
	if got, least := estimateRowBytes(row), 1000*len(long); got < least {
		t.Errorf("estimateRowBytes() = %d for a row carrying %d bytes of nested strings", got, least)
	}
}

// Rows fat only in their nested values must still cross the byte cap and hand
// over to the stream before the row count would.
func TestStreamCollector_NestedRowsCrossTheByteCap(t *testing.T) {
	opts := DQLExecuteOptions{
		AgentMode: true, Compact: true,
		Spill: SpillOptions{Mode: SpillAuto, Threshold: 50 << 10, Dir: t.TempDir(), Format: "jsonl"},
	}
	e := &DQLExecutor{}
	plan, ok := e.planStream(opts)
	if !ok {
		t.Fatal("planStream() declined an auto-spill agent invocation")
	}
	plan.bufferCap = 1 << 20

	payload := make([]interface{}, 1000)
	for i := range payload {
		payload[i] = strings.Repeat("y", 200)
	}
	rows := make([]map[string]interface{}, 10) // ~2 MB, far below switchRows
	for i := range rows {
		rows[i] = map[string]interface{}{"id": float64(i), "payload": payload}
	}
	if len(rows) > plan.switchRows {
		t.Fatalf("test needs fewer rows (%d) than the switch point (%d)", len(rows), plan.switchRows)
	}
	col := feed(t, e, plan, opts, rows)
	defer col.abort()

	if !col.streaming {
		t.Error("~2 MB of nested row payload stayed buffered under a 1 MB cap")
	}
}

// A spill threshold above the default buffering cap raises the cap with it, so
// a result the buffered path would emit inline is not spilled for its size.
func TestPlanStream_BufferCapFollowsARaisedThreshold(t *testing.T) {
	e := &DQLExecutor{}
	for _, tc := range []struct {
		threshold int64
		want      int64
	}{
		{50 << 10, maxBufferedBytes},
		{maxBufferedBytes, maxBufferedBytes},
		{64 << 20, 64 << 20},
	} {
		opts := DQLExecuteOptions{
			AgentMode: true,
			Spill:     SpillOptions{Mode: SpillAuto, Threshold: tc.threshold, Dir: t.TempDir(), Format: "jsonl"},
		}
		plan, ok := e.planStream(opts)
		if !ok {
			t.Fatal("planStream() declined an auto-spill agent invocation")
		}
		if plan.bufferCap != tc.want {
			t.Errorf("threshold %d: bufferCap = %d, want %d", tc.threshold, plan.bufferCap, tc.want)
		}
	}
}
