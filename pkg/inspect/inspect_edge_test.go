package inspect

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Dynatrace/dtctl/pkg/output"
)

// writeRaw writes exact bytes to dir/name (so a test can craft empty, blank-line,
// non-array, or corrupt files the printers would never produce), optionally with a
// sidecar declaring the format authoritatively.
func writeRaw(t *testing.T, dir, name, content string, sc *output.SidecarManifest) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
	if sc != nil {
		if err := output.WriteSidecar(path, sc); err != nil {
			t.Fatalf("sidecar: %v", err)
		}
	}
	return path
}

// errCode runs req and asserts it fails with a *Error carrying the wanted code.
func errCode(t *testing.T, req Request, want string) {
	t.Helper()
	_, err := Run(req)
	ie, ok := err.(*Error)
	if !ok {
		t.Fatalf("err = %v (%T), want *inspect.Error with code %q", err, err, want)
	}
	if ie.Code != want {
		t.Fatalf("err code = %q, want %q (msg: %s)", ie.Code, want, ie.Message)
	}
}

// TestRun_TailAndPageBoundaries covers the count<n tail branch and an offset past
// the end (both untested by the happy-path suite).
func TestRun_TailAndPageBoundaries(t *testing.T) {
	dir := t.TempDir()
	path := writeSpill(t, dir, "jsonl", sampleRecords(), &output.SidecarManifest{Format: "jsonl", Rows: 4})

	// tail N larger than the file → all rows, in file order.
	tail, err := Run(Request{Path: path, Primitive: PrimTail, N: 100})
	if err != nil {
		t.Fatalf("tail: %v", err)
	}
	if len(tail.Records) != 4 || tail.Records[0]["host"] != "web-01" || tail.Records[3]["host"] != "web-03" {
		t.Errorf("tail(100) = %v, want all 4 rows in order", tail.Records)
	}

	// page offset past the end → empty window, not an error.
	page, err := Run(Request{Path: path, Primitive: PrimPage, Offset: 100, Limit: 10})
	if err != nil {
		t.Fatalf("page past end: %v", err)
	}
	if len(page.Records) != 0 {
		t.Errorf("page past end = %v, want empty", page.Records)
	}

	// tail N<=0 → empty. The engine honours an explicit count (the command layer
	// owns defaulting an unspecified count), so a negative clamps to 0 rows rather
	// than being reinterpreted as the default.
	zero, err := Run(Request{Path: path, Primitive: PrimTail, N: -1})
	if err != nil {
		t.Fatalf("tail(-1): %v", err)
	}
	if len(zero.Records) != 0 {
		t.Errorf("tail(-1) = %d rows, want 0 (negative clamps to empty)", len(zero.Records))
	}

	// tail 0 → explicit empty (no longer the default).
	none, err := Run(Request{Path: path, Primitive: PrimTail, N: 0})
	if err != nil {
		t.Fatalf("tail(0): %v", err)
	}
	if len(none.Records) != 0 {
		t.Errorf("tail(0) = %d rows, want 0 (explicit zero honoured)", len(none.Records))
	}
}

// TestRun_HugeRowCountDoesNotPreallocate exercises the capped-preallocation path
// (N far larger than rowPreallocCap) on a tiny file: head and tail must still
// return exactly the rows present, growing the buffer lazily rather than
// reserving N slots up front.
func TestRun_HugeRowCountDoesNotPreallocate(t *testing.T) {
	dir := t.TempDir()
	path := writeSpill(t, dir, "jsonl", sampleRecords(), &output.SidecarManifest{Format: "jsonl", Rows: 4})
	huge := rowPreallocCap * 1000 // well past the prealloc cap

	head, err := Run(Request{Path: path, Primitive: PrimHead, N: huge})
	if err != nil {
		t.Fatalf("head(huge): %v", err)
	}
	if len(head.Records) != 4 || head.Records[0]["host"] != "web-01" {
		t.Errorf("head(huge) = %d rows, want all 4 in order", len(head.Records))
	}

	tail, err := Run(Request{Path: path, Primitive: PrimTail, N: huge})
	if err != nil {
		t.Fatalf("tail(huge): %v", err)
	}
	if len(tail.Records) != 4 || tail.Records[3]["host"] != "web-03" {
		t.Errorf("tail(huge) = %d rows, want all 4 in order", len(tail.Records))
	}
}

// TestRun_EmptyFiles ensures an empty NDJSON and an empty (headerless) CSV both
// read as a valid zero-row result rather than erroring.
func TestRun_EmptyFiles(t *testing.T) {
	dir := t.TempDir()
	for _, tc := range []struct{ name, content, format string }{
		{"q-empty.jsonl", "", "jsonl"},
		{"q-blanklines.jsonl", "\n\n", "jsonl"},
		{"q-empty.csv", "", "csv"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := writeRaw(t, dir, tc.name, tc.content, &output.SidecarManifest{Format: tc.format})
			res, err := Run(Request{Path: path, Primitive: PrimHead, N: 10})
			if err != nil {
				t.Fatalf("head: %v", err)
			}
			if len(res.Records) != 0 {
				t.Errorf("rows = %d, want 0 for empty file", len(res.Records))
			}
		})
	}
}

// TestRun_NDJSONBlankLinesBetweenRecords confirms blank lines interleaved with
// records (and a missing trailing newline) are tolerated.
func TestRun_NDJSONBlankLinesBetweenRecords(t *testing.T) {
	dir := t.TempDir()
	// Two records separated by a blank line, last line has no trailing newline.
	path := writeRaw(t, dir, "q-gaps.jsonl",
		"{\"a\":1}\n\n{\"a\":2}", &output.SidecarManifest{Format: "jsonl"})
	res, err := Run(Request{Path: path, Primitive: PrimHead, N: 10})
	if err != nil {
		t.Fatalf("head: %v", err)
	}
	if len(res.Records) != 2 {
		t.Fatalf("rows = %d, want 2 (blank line skipped)", len(res.Records))
	}
	if res.Records[1]["a"] != float64(2) {
		t.Errorf("last record = %v, want a=2", res.Records[1])
	}
}

// TestRun_UnreadableAndUndeterminable covers the error contract for files the
// reader cannot stream as records, and for a path whose format cannot be resolved.
func TestRun_UnreadableAndUndeterminable(t *testing.T) {
	dir := t.TempDir()

	// A JSON document that is an object, not an array of records → unreadable.
	obj := writeRaw(t, dir, "q-obj.json", `{"not":"an array"}`, &output.SidecarManifest{Format: "json"})
	errCode(t, Request{Path: obj, Primitive: PrimHead}, output.ErrCodeSpillFileUnreadable)

	// Garbage bytes declared as parquet → the reader fails to initialise → unreadable.
	pq := writeRaw(t, dir, "q-corrupt.parquet", "definitely not parquet", &output.SidecarManifest{Format: "parquet"})
	errCode(t, Request{Path: pq, Primitive: PrimHead}, output.ErrCodeSpillFileUnreadable)

	// No sidecar and an unrecognised extension → the format is undeterminable → bad flags.
	txt := writeRaw(t, dir, "q-mystery.txt", "{}", nil)
	errCode(t, Request{Path: txt, Primitive: PrimHead}, codeBadFlags)
}

// TestRun_FormatFromExtensionWithoutSidecar exercises extension-based format
// inference (the no-sidecar fallback) for csv, including header-derived schema.
func TestRun_FormatFromExtensionWithoutSidecar(t *testing.T) {
	dir := t.TempDir()
	// A real CSV with no sidecar: format is inferred from .csv, the header gives
	// the schema, and --fields validates against it (the authoritative-Columns path).
	path := writeSpill(t, dir, "csv", sampleRecords(), nil)
	res, err := Run(Request{Path: path, Primitive: PrimHead, N: 2, Fields: []string{"host"}})
	if err != nil {
		t.Fatalf("csv head: %v", err)
	}
	if res.Format != "csv" {
		t.Errorf("format = %q, want csv (inferred from extension)", res.Format)
	}
	if len(res.Records) != 2 || len(res.Records[0]) != 1 || res.Records[0]["host"] == nil {
		t.Errorf("projected csv rows = %v, want host-only", res.Records)
	}

	// An unknown --fields name is a hard error against the CSV header even without a sidecar.
	errCode(t, Request{Path: path, Primitive: PrimHead, Fields: []string{"ghost"}}, codeUnknownField)
}

// TestRun_CrossTenantUserPath covers the sidecar-tenant branch of checkContext:
// a non-managed (user-chosen) path is refused when its sidecar tenant disagrees
// with the active tenant.
func TestRun_CrossTenantUserPath(t *testing.T) {
	dir := t.TempDir() // user-chosen location, not the managed cache
	path := writeSpill(t, dir, "jsonl", sampleRecords(), &output.SidecarManifest{
		Format: "jsonl", TenantID: "tenant-a", ContextName: "ctx-a",
	})

	// Active tenant differs → refuse.
	errCode(t, Request{Path: path, Primitive: PrimHead, ActiveTenant: "tenant-b"},
		output.ErrCodeSpillFileWrongContext)

	// Same tenant → allowed.
	res, err := Run(Request{Path: path, Primitive: PrimHead, N: 1, ActiveTenant: "tenant-a"})
	if err != nil {
		t.Fatalf("same-tenant head: %v", err)
	}
	if len(res.Records) != 1 {
		t.Errorf("rows = %d, want 1", len(res.Records))
	}
}

// TestRun_StatsColumnSelection covers selectColumns: restricting --stats to named
// columns, and the unknown-column error.
func TestRun_StatsColumnSelection(t *testing.T) {
	dir := t.TempDir()
	path := writeSpill(t, dir, "jsonl", sampleRecords(), &output.SidecarManifest{Format: "jsonl", Rows: 4})

	res, err := Run(Request{Path: path, Primitive: PrimStats, StatsColumns: []string{"host"}})
	if err != nil {
		t.Fatalf("stats select: %v", err)
	}
	if res.Summary == nil || len(res.Summary.Columns) != 1 || res.Summary.Columns[0].Name != "host" {
		t.Fatalf("selected stats = %+v, want only host", res.Summary)
	}

	errCode(t, Request{Path: path, Primitive: PrimStats, StatsColumns: []string{"nope"}}, codeUnknownField)
}

// TestRun_DeferredFieldsWarning covers the NDJSON-without-schema path: an unknown
// --fields name cannot be validated up front, so it is not an error but produces
// a missing-field warning; a present field produces none.
func TestRun_DeferredFieldsWarning(t *testing.T) {
	dir := t.TempDir()
	path := writeSpill(t, dir, "jsonl", sampleRecords(), nil) // no sidecar → schema unknowable up front

	// Unknown field: no error, but a warning.
	missing, err := Run(Request{Path: path, Primitive: PrimHead, N: 2, Fields: []string{"nonexistent"}})
	if err != nil {
		t.Fatalf("unexpected error for deferred field: %v", err)
	}
	if len(missing.Warnings) == 0 {
		t.Errorf("expected a missing-field warning for an absent field on a schemaless file")
	}

	// Present field: no warning.
	present, err := Run(Request{Path: path, Primitive: PrimHead, N: 2, Fields: []string{"host"}})
	if err != nil {
		t.Fatalf("present field: %v", err)
	}
	if len(present.Warnings) != 0 {
		t.Errorf("unexpected warnings for a present field: %v", present.Warnings)
	}
}

// TestRun_ParquetProjectionTailPage exercises Parquet beyond head: --fields
// projection (which Parquet applies after decode), tail, and a page window.
func TestRun_ParquetProjectionTailPage(t *testing.T) {
	dir := t.TempDir()
	recs := []map[string]interface{}{
		{"host": "web-01", "count": float64(10)},
		{"host": "web-02", "count": float64(20)},
		{"host": "web-03", "count": float64(30)},
	}
	path := filepath.Join(dir, "q-p.parquet")
	f, _ := os.Create(path)
	p := output.NewPrinterWithOpts(output.PrinterOptions{Format: "parquet", Writer: f, Types: []output.ColumnTypeMapping{
		{Name: "host", Type: "string"}, {Name: "count", Type: "long"},
	}})
	if err := p.PrintList(recs); err != nil {
		t.Fatalf("write parquet: %v", err)
	}
	_ = f.Close()
	_ = output.WriteSidecar(path, &output.SidecarManifest{Format: "parquet", Rows: 3})

	// Projection: keep only host.
	proj, err := Run(Request{Path: path, Primitive: PrimHead, N: 3, Fields: []string{"host"}})
	if err != nil {
		t.Fatalf("parquet head+fields: %v", err)
	}
	if len(proj.Records) != 3 || len(proj.Records[0]) != 1 || proj.Records[0]["host"] == nil {
		t.Errorf("projected parquet rows = %v, want host-only", proj.Records)
	}

	// Tail.
	tail, err := Run(Request{Path: path, Primitive: PrimTail, N: 1})
	if err != nil {
		t.Fatalf("parquet tail: %v", err)
	}
	if len(tail.Records) != 1 || tail.Records[0]["host"] != "web-03" {
		t.Errorf("parquet tail = %v, want web-03", tail.Records)
	}

	// Page window [1,3).
	page, err := Run(Request{Path: path, Primitive: PrimPage, Offset: 1, Limit: 2})
	if err != nil {
		t.Fatalf("parquet page: %v", err)
	}
	if len(page.Records) != 2 || page.Records[0]["host"] != "web-02" {
		t.Errorf("parquet page = %v, want [web-02, web-03]", page.Records)
	}
	// Parquet integers round-trip as int64.
	if page.Records[0]["count"] != int64(20) {
		t.Errorf("count = %#v, want int64(20)", page.Records[0]["count"])
	}
}

// TestRun_Sample covers the sample primitive end to end (leading N rows as a
// file-summary, with embedded sample_rows).
func TestRun_Sample(t *testing.T) {
	dir := t.TempDir()
	path := writeSpill(t, dir, "jsonl", sampleRecords(), &output.SidecarManifest{Format: "jsonl", Rows: 4})
	res, err := Run(Request{Path: path, Primitive: PrimSample, N: 2})
	if err != nil {
		t.Fatalf("sample: %v", err)
	}
	if res.Kind != output.KindFileSummary || res.Summary == nil {
		t.Fatalf("sample kind = %q", res.Kind)
	}
	if len(res.Summary.SampleRows) != 2 {
		t.Errorf("sample_rows = %d, want 2", len(res.Summary.SampleRows))
	}
}

// TestRun_NDJSONCorruptMidStream confirms a malformed record encountered while
// streaming surfaces as an unreadable-file error (the wrapReadError path), rather
// than silently truncating the result.
func TestRun_NDJSONCorruptMidStream(t *testing.T) {
	dir := t.TempDir()
	// First line valid, second line malformed JSON.
	path := writeRaw(t, dir, "q-corrupt.jsonl",
		"{\"a\":1}\n{not valid json}\n", &output.SidecarManifest{Format: "jsonl"})
	// N large enough to reach the bad line.
	errCode(t, Request{Path: path, Primitive: PrimHead, N: 10}, output.ErrCodeSpillFileUnreadable)

	// The error message names the offending file (the NDJSON reader wraps with its
	// path, matching the json/csv/parquet readers).
	_, err := Run(Request{Path: path, Primitive: PrimHead, N: 10})
	if ie, ok := err.(*Error); !ok || !strings.Contains(ie.Message, path) {
		t.Errorf("unreadable error %q should contain the path %q", err, path)
	}
}

// TestRun_ParquetScalarKinds covers the boolean and double branches of the Parquet
// value→map conversion (the int64/timestamp branches are covered elsewhere).
func TestRun_ParquetScalarKinds(t *testing.T) {
	dir := t.TempDir()
	recs := []map[string]interface{}{
		{"ok": true, "ratio": float64(1.5)},
		{"ok": false, "ratio": float64(2.25)},
	}
	path := filepath.Join(dir, "q-kinds.parquet")
	f, _ := os.Create(path)
	p := output.NewPrinterWithOpts(output.PrinterOptions{Format: "parquet", Writer: f, Types: []output.ColumnTypeMapping{
		{Name: "ok", Type: "boolean"}, {Name: "ratio", Type: "double"},
	}})
	if err := p.PrintList(recs); err != nil {
		t.Fatalf("write parquet: %v", err)
	}
	_ = f.Close()
	_ = output.WriteSidecar(path, &output.SidecarManifest{Format: "parquet", Rows: 2})

	res, err := Run(Request{Path: path, Primitive: PrimHead, N: 2})
	if err != nil {
		t.Fatalf("parquet head: %v", err)
	}
	if res.Records[0]["ok"] != true || res.Records[1]["ok"] != false {
		t.Errorf("boolean round-trip = %v / %v, want true / false", res.Records[0]["ok"], res.Records[1]["ok"])
	}
	if res.Records[0]["ratio"] != float64(1.5) {
		t.Errorf("double round-trip = %#v, want 1.5", res.Records[0]["ratio"])
	}
}

// TestRun_StatsMixedAndComplexColumns confirms the streaming stats path classifies
// type-mixed and nested columns as "complex" (never crashing on a variant column)
// and reports timestamps as a timestamp column.
func TestRun_StatsMixedAndComplexColumns(t *testing.T) {
	dir := t.TempDir()
	recs := []map[string]interface{}{
		{"mixed": float64(1), "nested": map[string]interface{}{"k": "v"}, "ts": "2026-06-01T10:00:00Z"},
		{"mixed": "two", "nested": map[string]interface{}{"k": "w"}, "ts": "2026-06-01T11:00:00Z"},
	}
	path := writeSpill(t, dir, "jsonl", recs, &output.SidecarManifest{Format: "jsonl", Rows: 2})
	res, err := Run(Request{Path: path, Primitive: PrimStats})
	if err != nil {
		t.Fatalf("stats: %v", err)
	}
	byName := map[string]output.ColumnStats{}
	for _, c := range res.Summary.Columns {
		byName[c.Name] = c
	}
	if byName["mixed"].Type != "complex" {
		t.Errorf("mixed type = %q, want complex", byName["mixed"].Type)
	}
	if byName["nested"].Type != "complex" {
		t.Errorf("nested type = %q, want complex", byName["nested"].Type)
	}
	if byName["ts"].Type != "timestamp" {
		t.Errorf("ts type = %q, want timestamp", byName["ts"].Type)
	}
}

// TestRun_StatsCapsWideColumns confirms that re-deriving stats over a file with
// more columns than the envelope cap returns at most DefaultMaxSummaryColumns
// columns and names the rest in ColumnsOmitted — inspect must not become the
// context blowup it exists to prevent.
func TestRun_StatsCapsWideColumns(t *testing.T) {
	dir := t.TempDir()
	wide := map[string]interface{}{}
	for i := 0; i < output.DefaultMaxSummaryColumns+15; i++ {
		wide[fmt.Sprintf("c%03d", i)] = float64(i)
	}
	path := writeSpill(t, dir, "jsonl", []map[string]interface{}{wide}, &output.SidecarManifest{Format: "jsonl", Rows: 1})

	stats, err := Run(Request{Path: path, Primitive: PrimStats})
	if err != nil {
		t.Fatalf("stats: %v", err)
	}
	if len(stats.Summary.Columns) > output.DefaultMaxSummaryColumns {
		t.Errorf("stats returned %d columns, want at most %d", len(stats.Summary.Columns), output.DefaultMaxSummaryColumns)
	}
	if len(stats.Summary.ColumnsOmitted) == 0 {
		t.Errorf("expected omitted columns to be reported for a wide file")
	}

	// --schema is capped the same way.
	schema, err := Run(Request{Path: path, Primitive: PrimSchema})
	if err != nil {
		t.Fatalf("schema: %v", err)
	}
	if len(schema.Summary.Columns) > output.DefaultMaxSummaryColumns {
		t.Errorf("schema returned %d columns, want at most %d", len(schema.Summary.Columns), output.DefaultMaxSummaryColumns)
	}

	// An explicit column selection is NOT capped — the caller chose the scope.
	sel, err := Run(Request{Path: path, Primitive: PrimStats, StatsColumns: []string{"c000", "c001"}})
	if err != nil {
		t.Fatalf("stats select: %v", err)
	}
	if len(sel.Summary.Columns) != 2 {
		t.Errorf("explicit selection = %d columns, want 2 (uncapped)", len(sel.Summary.Columns))
	}
}

// TestRun_CrossContextUserPathViaSidecar covers the provenance context guard: a
// non-managed (user-chosen) path whose sidecar records a different context (and no
// tenant id) is refused when an active context is set.
func TestRun_CrossContextUserPathViaSidecar(t *testing.T) {
	dir := t.TempDir() // user-chosen location, not the managed cache
	path := writeSpill(t, dir, "jsonl", sampleRecords(), &output.SidecarManifest{
		Format: "jsonl", ContextName: "ctx-a", // note: no TenantID
	})

	// Active context differs → refuse via the sidecar context provenance.
	errCode(t, Request{Path: path, Primitive: PrimHead, ActiveContext: "ctx-b"},
		output.ErrCodeSpillFileWrongContext)

	// Same context → allowed.
	res, err := Run(Request{Path: path, Primitive: PrimHead, N: 1, ActiveContext: "ctx-a"})
	if err != nil {
		t.Fatalf("same-context head: %v", err)
	}
	if len(res.Records) != 1 {
		t.Errorf("rows = %d, want 1", len(res.Records))
	}
}

// TestFormatTimestamp_OverflowGuard confirms a far-future millis timestamp that
// would overflow int64 when scaled to nanoseconds surfaces the raw value rather
// than a wrapped, garbage wall-clock string.
func TestFormatTimestamp_OverflowGuard(t *testing.T) {
	const millis = int64(1)
	mult := int64(1_000_000) // ms → ns

	// In range: converts to an RFC3339 string.
	if got := formatTimestamp(millis, mult); got != "1970-01-01T00:00:00.001Z" {
		t.Errorf("in-range = %v, want RFC3339 string", got)
	}

	// Overflow: value*mult exceeds int64 → raw value returned.
	huge := int64(9_300_000_000_000) // ms past year 2262 once scaled
	got := formatTimestamp(huge, mult)
	if got != huge {
		t.Errorf("overflow = %v (%T), want raw int64 %d", got, got, huge)
	}
}
