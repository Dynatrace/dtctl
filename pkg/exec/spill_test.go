package exec

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/parquet-go/parquet-go"

	"github.com/dynatrace-oss/dtctl/pkg/output"
	sdkquery "github.com/dynatrace-oss/dtctl/sdk/api/query"
)

func TestParseByteSize(t *testing.T) {
	cases := []struct {
		in   string
		want int64
		err  bool
	}{
		{"50KB", 50 * 1024, false},
		{"50kb", 50 * 1024, false},
		{"50k", 50 * 1024, false},
		{"1MB", 1024 * 1024, false},
		{"1GB", 1024 * 1024 * 1024, false},
		{"1.5KiB", 1536, false},
		{"1024", 1024, false},
		{"512B", 512, false},
		{"", 0, true},
		{"abc", 0, true},
		{"-5KB", 0, true},
		// Non-finite and out-of-range sizes must be rejected, not converted
		// into an undefined int64.
		{"NaNKB", 0, true},
		{"InfKB", 0, true},
		{"1e300GB", 0, true},
		{"99999999999999999999", 0, true},
	}
	for _, c := range cases {
		got, err := ParseByteSize(c.in)
		if c.err {
			if err == nil {
				t.Errorf("ParseByteSize(%q) expected error", c.in)
			}
			continue
		}
		if err != nil {
			t.Errorf("ParseByteSize(%q) unexpected error: %v", c.in, err)
			continue
		}
		if got != c.want {
			t.Errorf("ParseByteSize(%q) = %d, want %d", c.in, got, c.want)
		}
	}
}

func sampleResult(sampled bool) (*DQLQueryResponse, []map[string]interface{}) {
	records := []map[string]interface{}{
		{"host": "web-01", "status": float64(200)},
		{"host": "web-02", "status": float64(500)},
		{"host": "web-01", "status": float64(404)},
	}
	resp := &DQLQueryResponse{
		Records: records,
		Metadata: &DQLMetadata{
			Grail: &GrailMetadata{
				Sampled:        sampled,
				CanonicalQuery: "fetch logs",
				AnalysisTimeframe: &AnalysisTimeframe{
					Start: "2026-06-21T00:00:00Z",
					End:   "2026-06-22T00:00:00Z",
				},
			},
		},
	}
	return resp, records
}

func TestBuildSpillResponse_InlineUnderThreshold(t *testing.T) {
	e := &DQLExecutor{}
	result, records := sampleResult(false)
	opts := DQLExecuteOptions{
		Spill: SpillOptions{Mode: SpillAuto, Threshold: 1 << 20, Dir: t.TempDir(), Format: "json"},
	}
	_, spilled, err := e.buildSpillResponse("fetch logs", result, records, "json", opts)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if spilled {
		t.Errorf("expected inline (under threshold), got spilled")
	}
}

func TestBuildSpillResponse_SpillAlways(t *testing.T) {
	e := &DQLExecutor{}
	result, records := sampleResult(false)
	dir := t.TempDir()
	opts := DQLExecuteOptions{
		ContextName: "prod",
		TenantID:    "abc12345",
		Spill:       SpillOptions{Mode: SpillAlways, Threshold: 1 << 20, Dir: dir, Format: "json"},
	}
	resp, spilled, err := e.buildSpillResponse("fetch logs", result, records, "json", opts)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !spilled {
		t.Fatal("expected spilled")
	}
	if resp.EnvelopeVersion != output.EnvelopeVersion {
		t.Errorf("envelope_version = %d, want %d", resp.EnvelopeVersion, output.EnvelopeVersion)
	}
	if !resp.OK {
		t.Error("resp.OK should be true")
	}

	m, ok := resp.Result.(*output.ResultFileManifest)
	if !ok {
		t.Fatalf("result is %T, want *ResultFileManifest", resp.Result)
	}
	if m.Kind != output.KindResultFile {
		t.Errorf("kind = %q, want result-file", m.Kind)
	}
	if m.Rows != 3 {
		t.Errorf("rows = %d, want 3", m.Rows)
	}
	if m.TenantID != "abc12345" || m.ContextName != "prod" {
		t.Errorf("provenance = %q/%q", m.TenantID, m.ContextName)
	}
	if m.Path == "" {
		t.Fatal("path is empty")
	}
	if m.Columns == nil || m.SampleStats != nil {
		t.Errorf("non-sampled result should use columns, not sample_stats")
	}
	if len(m.SampleRows) == 0 {
		t.Error("expected sample_rows")
	}

	// File exists on disk (under the user-chosen dir; not context-partitioned)
	// and parses back.
	if !filepath.IsAbs(m.Path) {
		t.Errorf("path should be absolute: %q", m.Path)
	}
	if !strings.HasPrefix(m.Path, dir) {
		t.Errorf("spill path %q should be under user dir %q", m.Path, dir)
	}
	data, rerr := os.ReadFile(m.Path)
	if rerr != nil {
		t.Fatalf("read spilled file: %v", rerr)
	}
	var back []map[string]interface{}
	if jerr := json.Unmarshal(data, &back); jerr != nil {
		t.Fatalf("spilled json invalid: %v", jerr)
	}
	if len(back) != 3 {
		t.Errorf("spilled rows = %d, want 3", len(back))
	}
	if m.Bytes != int64(len(data)) {
		t.Errorf("manifest bytes = %d, file size = %d", m.Bytes, len(data))
	}

	// Sidecar written next to it.
	if _, serr := os.Stat(output.SidecarPathFor(m.Path)); serr != nil {
		t.Errorf("sidecar not written: %v", serr)
	}

	// Context provenance.
	if resp.Context.Decided != "spilled" {
		t.Errorf("decided = %q, want spilled", resp.Context.Decided)
	}
	if resp.Context.MeasuredEncoding != "json" {
		t.Errorf("measured_encoding = %q, want json", resp.Context.MeasuredEncoding)
	}
	if resp.Context.MeasuredBytes <= 0 {
		t.Errorf("measured_bytes = %d, want > 0", resp.Context.MeasuredBytes)
	}
	if len(resp.Context.Suggestions) == 0 {
		t.Error("expected suggestions")
	}
}

func TestBuildSpillResponse_WideResultCapsEnvelopeKeepsSidecarFull(t *testing.T) {
	e := &DQLExecutor{}
	// A wide result: more columns than the envelope cap. Every column is present
	// in every row (equal null counts) so the kept set is the alphabetically
	// first DefaultMaxSummaryColumns and the rest are omitted by name.
	const ncols = output.DefaultMaxSummaryColumns + 12
	rec := make(map[string]interface{}, ncols)
	for i := 0; i < ncols; i++ {
		rec[fmt.Sprintf("col%03d", i)] = fmt.Sprintf("v%d", i)
	}
	records := []map[string]interface{}{rec, rec, rec}
	result := &DQLQueryResponse{Records: records}

	dir := t.TempDir()
	opts := DQLExecuteOptions{
		ContextName: "prod",
		Spill:       SpillOptions{Mode: SpillAlways, Threshold: 1 << 20, Dir: dir, Format: "json"},
	}
	resp, handled, err := e.buildSpillResponse("fetch logs", result, records, "json", opts)
	if err != nil || !handled {
		t.Fatalf("buildSpillResponse: handled=%v err=%v", handled, err)
	}
	m := resp.Result.(*output.ResultFileManifest)

	// Envelope is capped, and the kept + omitted partition covers every column.
	if len(m.Columns) != output.DefaultMaxSummaryColumns {
		t.Errorf("envelope columns = %d, want %d (capped)", len(m.Columns), output.DefaultMaxSummaryColumns)
	}
	if len(m.ColumnsOmitted) != ncols-output.DefaultMaxSummaryColumns {
		t.Errorf("columns_omitted = %d, want %d", len(m.ColumnsOmitted), ncols-output.DefaultMaxSummaryColumns)
	}
	if len(m.Columns)+len(m.ColumnsOmitted) != ncols {
		t.Errorf("kept(%d) + omitted(%d) != total %d", len(m.Columns), len(m.ColumnsOmitted), ncols)
	}

	// The on-disk sidecar keeps the FULL per-column set (nothing dropped on disk).
	scData, rerr := os.ReadFile(output.SidecarPathFor(m.Path))
	if rerr != nil {
		t.Fatalf("read sidecar: %v", rerr)
	}
	var sc output.SidecarManifest
	if jerr := json.Unmarshal(scData, &sc); jerr != nil {
		t.Fatalf("sidecar json invalid: %v", jerr)
	}
	if len(sc.Columns) != ncols {
		t.Errorf("sidecar columns = %d, want full %d", len(sc.Columns), ncols)
	}

	// The omission is surfaced to the consumer as a suggestion.
	var noted bool
	for _, s := range resp.Context.Suggestions {
		if strings.Contains(s, "columns_omitted") {
			noted = true
		}
	}
	if !noted {
		t.Error("expected a suggestion pointing at columns_omitted")
	}
}

func TestBuildSpillResponse_InlineRecordsEnvelopeInAgentMode(t *testing.T) {
	e := &DQLExecutor{}
	result, records := sampleResult(false)
	opts := DQLExecuteOptions{
		AgentMode: true,
		Spill:     SpillOptions{Mode: SpillAuto, Threshold: 1 << 20, Dir: t.TempDir(), Format: "json"},
	}
	resp, handled, err := e.buildSpillResponse("fetch logs", result, records, "json", opts)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !handled {
		t.Fatal("agent-mode inline result should be handled (kind:records envelope), not a fall-through")
	}
	if resp.EnvelopeVersion != output.EnvelopeVersion || !resp.OK {
		t.Errorf("envelope_version=%d ok=%v", resp.EnvelopeVersion, resp.OK)
	}
	ir, ok := resp.Result.(*output.InlineRecords)
	if !ok {
		t.Fatalf("result is %T, want *InlineRecords", resp.Result)
	}
	if ir.Kind != output.KindRecords {
		t.Errorf("kind = %q, want records", ir.Kind)
	}
	if len(ir.Records) != 3 {
		t.Errorf("records len = %d, want 3", len(ir.Records))
	}
	if resp.Context.Decided != "inline" {
		t.Errorf("decided = %q, want inline", resp.Context.Decided)
	}
	// The whole point of D2/D31: a consumer can find result.kind in the inline case too.
	js, _ := json.Marshal(resp)
	if !strings.Contains(string(js), `"kind":"records"`) {
		t.Errorf("inline envelope missing kind discriminator:\n%s", js)
	}
}

// pipedStdout points os.Stdout at a regular file for the rest of the test, so
// the output is not a terminal — as for an agent or a pipe — whatever `go test`
// itself was attached to. The spill measurement follows the layout the envelope
// will be written in, and that depends on os.Stdout.
func pipedStdout(t *testing.T) {
	t.Helper()
	f, err := os.CreateTemp(t.TempDir(), "stdout")
	if err != nil {
		t.Fatal(err)
	}
	old := os.Stdout
	os.Stdout = f
	t.Cleanup(func() {
		os.Stdout = old
		_ = f.Close()
	})
}

// #570: piped agent output is a compact envelope, so --spill=auto measures the
// rows compact. At a threshold equal to their compact size the result now stays
// inline; measured indented, as before, it was ~2x over and spilled. The
// measured size is the bytes the envelope actually carries for the rows.
func TestBuildSpillResponse_AgentPipedMeasuresCompactBytes(t *testing.T) {
	pipedStdout(t)
	records := make([]map[string]interface{}, 0, 20)
	for i := 0; i < 20; i++ {
		records = append(records, map[string]interface{}{
			"host":    fmt.Sprintf("web-%02d", i),
			"status":  float64(200 + i),
			"content": fmt.Sprintf("request %d served", i),
		})
	}
	result := &DQLQueryResponse{Records: records}

	var compactBuf, indentedBuf bytes.Buffer
	if err := json.NewEncoder(&compactBuf).Encode(records); err != nil {
		t.Fatal(err)
	}
	ienc := json.NewEncoder(&indentedBuf)
	ienc.SetIndent("", "  ")
	if err := ienc.Encode(records); err != nil {
		t.Fatal(err)
	}
	compact, indented := int64(compactBuf.Len()), int64(indentedBuf.Len())
	if indented <= compact {
		t.Fatalf("fixture: indented %d should exceed compact %d", indented, compact)
	}

	e := &DQLExecutor{}
	opts := DQLExecuteOptions{
		AgentMode: true,
		Verbose:   true, // keep measured_bytes on the inline envelope
		Spill:     SpillOptions{Mode: SpillAuto, Threshold: compact, Dir: t.TempDir(), Format: "json"},
	}
	resp, handled, err := e.buildSpillResponse("fetch logs", result, records, "json", opts)
	if err != nil || !handled {
		t.Fatalf("handled=%v err=%v", handled, err)
	}
	if resp.Context.Decided != "inline" {
		t.Fatalf("decided = %q at a threshold equal to the compact size (%d B; indented %d B), want inline",
			resp.Context.Decided, compact, indented)
	}
	if resp.Context.MeasuredBytes != compact {
		t.Errorf("measured_bytes = %d, want the compact encoding's %d", resp.Context.MeasuredBytes, compact)
	}

	// And that is what the envelope written to the piped stdout carries for the
	// rows (plus the encoder's trailing newline, which the envelope does not
	// repeat after a nested value).
	var out bytes.Buffer
	if err := output.EncodeEnvelope(&out, resp); err != nil {
		t.Fatal(err)
	}
	var env struct {
		Result struct {
			Records json.RawMessage `json:"records"`
		} `json:"result"`
	}
	if err := json.Unmarshal(out.Bytes(), &env); err != nil {
		t.Fatal(err)
	}
	if got := int64(len(env.Result.Records)) + 1; got != resp.Context.MeasuredBytes {
		t.Errorf("envelope carries %d bytes of rows, measured_bytes = %d", got, resp.Context.MeasuredBytes)
	}

	// One byte under that, it spills.
	opts.Spill.Threshold = compact - 1
	resp, _, err = e.buildSpillResponse("fetch logs", result, records, "json", opts)
	if err != nil {
		t.Fatal(err)
	}
	if resp.Context.Decided != "spilled" || resp.Context.MeasuredBytes != compact {
		t.Errorf("threshold %d: decided=%q measured=%d, want spilled at %d",
			opts.Spill.Threshold, resp.Context.Decided, resp.Context.MeasuredBytes, compact)
	}
}

// Outside agent mode the rows go through the JSON printer, which indents even
// when piped, and prints them one level deep as {"records": [...]}. That is the
// size measured — exactly what printResults writes for them — so the threshold
// that keeps a piped agent result inline spills this one.
func TestBuildSpillResponse_NonAgentMeasuresPrintedBytes(t *testing.T) {
	pipedStdout(t)
	_, records := sampleResult(false)
	result := &DQLQueryResponse{Records: records}

	printed := captureStdout(t, func() {
		if err := (&DQLExecutor{}).printResults("fetch logs", result, DQLExecuteOptions{OutputFormat: "json"}); err != nil {
			t.Errorf("printResults: %v", err)
		}
	})
	layout := output.IndentedJSONLayout(1)
	var want bytes.Buffer
	enc := json.NewEncoder(&want)
	enc.SetIndent(layout.Prefix, layout.Indent)
	if err := enc.Encode(records); err != nil {
		t.Fatal(err)
	}
	text := bytes.TrimSuffix(want.Bytes(), []byte("\n"))
	if !bytes.Contains(printed, append([]byte(`"records": `), text...)) {
		t.Fatalf("printed output does not carry the rows laid out as %+v:\n%s\nprinted:\n%s", layout, text, printed)
	}

	compact, _ := output.MeasureSerializedBytes(records, "json", output.JSONLayout{})
	e := &DQLExecutor{}
	opts := DQLExecuteOptions{
		OutputFormat: "json",
		Spill:        SpillOptions{Mode: SpillAuto, Threshold: compact, Dir: t.TempDir(), Format: "json"},
	}
	resp, handled, err := e.buildSpillResponse("fetch logs", result, records, "json", opts)
	if err != nil || !handled {
		t.Fatalf("handled=%v err=%v", handled, err)
	}
	if want := int64(len(text)) + 1; resp.Context.Decided != "spilled" || resp.Context.MeasuredBytes != want {
		t.Errorf("decided=%q measured=%d, want spilled at the printed %d (+1 newline)", resp.Context.Decided, resp.Context.MeasuredBytes, want)
	}
}

// Outside agent mode -o jsonl prints the rows as they came, one compact line
// each and nothing for an empty result: no array framing, no compaction (even
// with --compact) and no type block. The spill measurement must be exactly
// that, for 0, 1 and several rows.
func TestMeasureInline_NonAgentJSONLMatchesPrintedBytes(t *testing.T) {
	pipedStdout(t)
	rows := func(n int) []map[string]interface{} {
		out := make([]map[string]interface{}, n)
		for i := range out {
			// "region" is constant across rows, so --compact would hoist it.
			out[i] = map[string]interface{}{"host": fmt.Sprintf("web-%02d", i), "region": "eu", "status": float64(200 + i)}
		}
		return out
	}
	for _, n := range []int{0, 1, 3} {
		records := rows(n)
		opts := DQLExecuteOptions{OutputFormat: "jsonl", Compact: true}
		printed := captureStdout(t, func() {
			if err := (&DQLExecutor{}).printResults("fetch logs", &DQLQueryResponse{Records: records}, opts); err != nil {
				t.Errorf("printResults: %v", err)
			}
		})
		inline := newInlineRows(records, compactionFor(records, opts), opts.MaxFieldChars)
		inline.types = []interface{}{map[string]interface{}{"mappings": map[string]interface{}{}}}
		got, _ := measureInline(inline, "jsonl", inlineLayout(io.Discard, opts, "jsonl"))
		if got != int64(len(printed)) {
			t.Errorf("%d rows: measured %d bytes, -o jsonl printed %d: %q", n, got, len(printed), printed)
		}
	}
}

// resultWithNotification returns a small result carrying a query notification,
// so the envelope-building paths can be exercised against truncation warnings.
func resultWithNotification(n QueryNotification) (*DQLQueryResponse, []map[string]interface{}) {
	resp, records := sampleResult(false)
	resp.Metadata.Grail.Notifications = []QueryNotification{n}
	return resp, records
}

func TestBuildSpillResponse_InlineSurfacesScanLimitNotification(t *testing.T) {
	e := &DQLExecutor{}
	result, records := resultWithNotification(QueryNotification{
		Severity:         "WARNING",
		NotificationType: "SCAN_LIMIT_GBYTES",
		Message:          "Your execution was stopped after 500 gigabytes of data were scanned.",
	})
	opts := DQLExecuteOptions{
		AgentMode: true,
		Spill:     SpillOptions{Mode: SpillAuto, Threshold: 1 << 20, Dir: t.TempDir(), Format: "json"},
	}
	resp, handled, err := e.buildSpillResponse("fetch logs", result, records, "json", opts)
	if err != nil || !handled {
		t.Fatalf("buildSpillResponse: handled=%v err=%v", handled, err)
	}
	if resp.Context.Decided != "inline" {
		t.Fatalf("decided = %q, want inline", resp.Context.Decided)
	}
	if len(resp.Context.Warnings) != 1 || !strings.Contains(resp.Context.Warnings[0], "500 gigabytes") {
		t.Errorf("expected scan-limit warning in envelope, got %v", resp.Context.Warnings)
	}
	if !strings.Contains(strings.Join(resp.Context.Suggestions, "\n"), "PARTIAL") {
		t.Errorf("expected PARTIAL scan-limit advice in envelope, got %v", resp.Context.Suggestions)
	}
}

func TestBuildSpillResponse_SpilledSurfacesScanLimitNotificationFirst(t *testing.T) {
	e := &DQLExecutor{}
	result, records := resultWithNotification(QueryNotification{
		Severity:         "WARNING",
		NotificationType: "SCAN_LIMIT_GBYTES",
		Message:          "Your execution was stopped after 500 gigabytes of data were scanned.",
	})
	opts := DQLExecuteOptions{
		AgentMode: true,
		Spill:     SpillOptions{Mode: SpillAlways, Threshold: 1 << 20, Dir: t.TempDir(), Format: "json"},
	}
	resp, spilled, err := e.buildSpillResponse("fetch logs", result, records, "json", opts)
	if err != nil || !spilled {
		t.Fatalf("buildSpillResponse: spilled=%v err=%v", spilled, err)
	}
	if len(resp.Context.Warnings) == 0 || !strings.Contains(strings.Join(resp.Context.Warnings, "\n"), "500 gigabytes") {
		t.Errorf("expected scan-limit warning in spilled envelope, got %v", resp.Context.Warnings)
	}
	// Notification advice must lead the suggestions — a PARTIAL result matters
	// more than the spill/inspect follow-ups.
	if len(resp.Context.Suggestions) == 0 || !strings.Contains(resp.Context.Suggestions[0], "scan limit was reached") {
		t.Errorf("scan-limit advice should lead the suggestions, got %v", resp.Context.Suggestions)
	}
	// The spill follow-ups (inspect hint) must still be present after it.
	if !strings.Contains(strings.Join(resp.Context.Suggestions, "\n"), "dtctl inspect") {
		t.Errorf("expected spill inspect follow-up to remain, got %v", resp.Context.Suggestions)
	}
}

func TestBuildSpillResponse_NeverModeInlinesInAgentMode(t *testing.T) {
	// --spill=never forces rows inline regardless of size; in agent mode that
	// still means a self-describing kind:"records" envelope, never a table. Use
	// a Threshold of 0 to prove size is irrelevant under never (auto would spill).
	e := &DQLExecutor{}
	result, records := sampleResult(false)
	opts := DQLExecuteOptions{
		AgentMode: true,
		Spill:     SpillOptions{Mode: SpillNever, Threshold: 0, Dir: t.TempDir(), Format: "json"},
	}
	resp, handled, err := e.buildSpillResponse("fetch logs", result, records, "json", opts)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !handled {
		t.Fatal("never + agent mode should emit a kind:records envelope, not a fall-through")
	}
	ir, ok := resp.Result.(*output.InlineRecords)
	if !ok || ir.Kind != output.KindRecords {
		t.Fatalf("result = %T (kind unexpected), want *InlineRecords kind:records", resp.Result)
	}
	if resp.Context.Decided != "inline" {
		t.Errorf("decided = %q, want inline", resp.Context.Decided)
	}
}

func TestBuildSpillResponse_InlineFallThroughCases(t *testing.T) {
	e := &DQLExecutor{}
	result, records := sampleResult(false)
	base := SpillOptions{Mode: SpillAuto, Threshold: 1 << 20, Dir: t.TempDir(), Format: "json"}

	cases := []struct {
		name     string
		opts     DQLExecuteOptions
		encoding string
	}{
		// Not agent mode: a human inline result must stay a fall-through (table/CSV).
		{"non-agent", DQLExecuteOptions{Spill: base}, "json"},
		// Byte-oriented encodings: an agent asking for csv/yaml wants the bytes,
		// not an envelope wrapping them.
		{"csv-encoding", DQLExecuteOptions{AgentMode: true, Spill: base}, "csv"},
		{"yaml-encoding", DQLExecuteOptions{AgentMode: true, Spill: base}, "yaml"},
		// --jq owns the output shape in agent mode.
		{"jq-set", DQLExecuteOptions{AgentMode: true, JQFilter: ".[]", Spill: base}, "json"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, handled, err := e.buildSpillResponse("fetch logs", result, records, c.encoding, c.opts)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if handled {
				t.Errorf("%s: expected fall-through (handled=false), got an envelope", c.name)
			}
		})
	}
}

// TestBuildSpillResponse_InlineTOONKeepsEnvelope pins the contract that made
// `-o toon` size-dependent before: below the spill threshold the rows now come
// back inside the envelope as an encoded string, so `ok`/`error.code`/`context`
// are present on both sides of the threshold rather than only above it.
func TestBuildSpillResponse_InlineTOONKeepsEnvelope(t *testing.T) {
	e := &DQLExecutor{}
	result, records := sampleResult(false)
	opts := DQLExecuteOptions{
		AgentMode: true,
		Spill:     SpillOptions{Mode: SpillAuto, Threshold: 1 << 20, Dir: t.TempDir(), Format: "json"},
	}

	resp, handled, err := e.buildSpillResponse("fetch logs", result, records, "toon", opts)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !handled {
		t.Fatal("agent mode + -o toon should emit an envelope, not fall through")
	}
	if !resp.OK {
		t.Error("envelope should report ok=true")
	}
	enc, ok := resp.Result.(*output.InlineRecordsEncoded)
	if !ok {
		t.Fatalf("result = %T, want *InlineRecordsEncoded", resp.Result)
	}
	if enc.Kind != output.KindRecords {
		t.Errorf("kind = %q, want %q", enc.Kind, output.KindRecords)
	}
	if enc.Encoding != "toon" {
		t.Errorf("encoding = %q, want toon", enc.Encoding)
	}
	if enc.Records == "" {
		t.Error("records should carry the TOON-encoded rows")
	}
	// The payload must be the rows, not a Go-syntax dump of them.
	if strings.Contains(enc.Records, "map[") {
		t.Errorf("records looks like a Go fmt dump, not TOON: %q", enc.Records)
	}
	if resp.Context == nil || resp.Context.Decided != "inline" {
		t.Errorf("decided = %+v, want inline", resp.Context)
	}
	if resp.Context.MeasuredEncoding != "toon" {
		t.Errorf("measured_encoding = %q, want toon", resp.Context.MeasuredEncoding)
	}
}

func TestBuildSpillResponse_ManagedPartition(t *testing.T) {
	// Redirect the OS user cache dir into the test sandbox so the managed-cache
	// path (D7) is exercised hermetically and partitioned by context (D9).
	tmp := t.TempDir()
	t.Setenv("XDG_CACHE_HOME", tmp) // Linux
	t.Setenv("HOME", tmp)           // macOS uses $HOME/Library/Caches
	t.Setenv("LocalAppData", tmp)   // Windows: os.UserCacheDir reads %LocalAppData%

	e := &DQLExecutor{}
	result, records := sampleResult(false)
	opts := DQLExecuteOptions{
		ContextName: "prod",
		Spill:       SpillOptions{Mode: SpillAlways, Threshold: 0, Format: "json", TTL: output.DefaultSpillTTL}, // no Dir -> managed
	}
	resp, spilled, err := e.buildSpillResponse("fetch logs", result, records, "json", opts)
	if err != nil || !spilled {
		t.Fatalf("spilled=%v err=%v", spilled, err)
	}
	m := resp.Result.(*output.ResultFileManifest)
	if filepath.Base(filepath.Dir(m.Path)) != "prod" {
		t.Errorf("managed cache should be partitioned by context, got %q", m.Path)
	}
	if !strings.HasPrefix(m.Path, tmp) {
		t.Errorf("managed spill escaped the test cache dir: %q (tmp=%q)", m.Path, tmp)
	}
	// Managed location must NOT carry the user-path privacy warning.
	for _, w := range resp.Context.Warnings {
		if w == userPathPrivacyWarning {
			t.Errorf("managed spill should not warn about user-path privacy opt-out")
		}
	}
}

func TestBuildSpillResponse_Sampled(t *testing.T) {
	e := &DQLExecutor{}
	result, records := sampleResult(true)
	opts := DQLExecuteOptions{
		Spill: SpillOptions{Mode: SpillAlways, Threshold: 0, Dir: t.TempDir(), Format: "json", TTL: output.DefaultSpillTTL},
	}
	resp, spilled, err := e.buildSpillResponse("fetch logs", result, records, "json", opts)
	if err != nil || !spilled {
		t.Fatalf("spilled=%v err=%v", spilled, err)
	}
	m := resp.Result.(*output.ResultFileManifest)
	if !m.Sampled {
		t.Error("manifest.Sampled should be true")
	}
	if m.SampleStats == nil || m.Columns != nil {
		t.Fatal("sampled result must use sample_stats, not columns (D23)")
	}
	if m.SampleStats.Basis != "sample" {
		t.Errorf("sample_stats basis = %q, want sample", m.SampleStats.Basis)
	}
	for _, c := range m.SampleStats.Columns {
		if c.Basis != "sample" {
			t.Errorf("column %q basis = %q, want sample", c.Name, c.Basis)
		}
	}
}

func TestBuildSpillResponse_SummaryOnly(t *testing.T) {
	e := &DQLExecutor{}
	result, records := sampleResult(false)

	// Make the preferred dir unwritable: point it under a regular file so the
	// results subdir can never be created.
	f := filepath.Join(t.TempDir(), "afile")
	if err := os.WriteFile(f, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	opts := DQLExecuteOptions{
		Spill: SpillOptions{Mode: SpillAlways, Threshold: 0, Dir: filepath.Join(f, "nope"), Format: "json"},
	}
	resp, spilled, err := e.buildSpillResponse("fetch logs", result, records, "json", opts)
	if err != nil || !spilled {
		t.Fatalf("spilled=%v err=%v", spilled, err)
	}
	m := resp.Result.(*output.ResultFileManifest)
	if m.Kind != output.KindSummaryOnly {
		t.Errorf("kind = %q, want summary-only", m.Kind)
	}
	if m.Path != "" {
		t.Errorf("summary-only must omit path, got %q", m.Path)
	}
	if m.Bytes != 0 {
		t.Errorf("summary-only must omit bytes, got %d", m.Bytes)
	}
	if m.Columns == nil {
		t.Error("summary-only should still carry computed stats")
	}
	if resp.Context.Decided != "summary-only" {
		t.Errorf("decided = %q, want summary-only", resp.Context.Decided)
	}
	if len(resp.Context.Warnings) == 0 {
		t.Error("expected a no-writable-location warning")
	}

	// A read-only filesystem makes --spill-to futile, so the advice must steer to
	// a self-bounding re-query (--spill=never plus a record/column cap), not to
	// writing a file we just proved we cannot write.
	joined := strings.Join(resp.Context.Suggestions, "\n")
	if !strings.Contains(joined, "--spill=never") || !strings.Contains(joined, "--max-result-records") {
		t.Errorf("expected --spill=never + --max-result-records advice for no-writable-location, got:\n%s", joined)
	}
	if strings.Contains(joined, "re-run with --spill-to <path> pointing at a writable location") {
		t.Errorf("must not recommend --spill-to as the remedy on a read-only filesystem, got:\n%s", joined)
	}
}

func TestSpillSuggestions(t *testing.T) {
	q := "fetch logs" // non-aggregating: the DQL-aggregate nudge applies

	t.Run("result-file points at the on-disk file", func(t *testing.T) {
		s := strings.Join(spillSuggestions(q, output.KindResultFile, ""), "\n")
		if !strings.Contains(s, "on disk at the path above") {
			t.Errorf("missing file-read hint:\n%s", s)
		}
		if strings.Contains(s, "--spill=never") || strings.Contains(s, "--spill-to") {
			t.Errorf("result-file must not suggest re-querying:\n%s", s)
		}
	})

	t.Run("no writable location steers to a bounded re-query", func(t *testing.T) {
		s := strings.Join(spillSuggestions(q, output.KindSummaryOnly, summaryReasonNoLocation), "\n")
		for _, want := range []string{"read-only filesystem", "--spill=never", "--max-result-records"} {
			if !strings.Contains(s, want) {
				t.Errorf("missing %q in no-location advice:\n%s", want, s)
			}
		}
		if strings.Contains(s, "pointing at a writable location") {
			t.Errorf("must not recommend --spill-to on a read-only filesystem:\n%s", s)
		}
	})

	t.Run("write failure can retry a different explicit path", func(t *testing.T) {
		s := strings.Join(spillSuggestions(q, output.KindSummaryOnly, summaryReasonWriteFailed), "\n")
		if !strings.Contains(s, "--spill-to") {
			t.Errorf("write-failure should offer --spill-to as a remedy:\n%s", s)
		}
		if strings.Contains(s, "--spill=never") {
			t.Errorf("write-failure should not steer to --spill=never:\n%s", s)
		}
	})
}

func TestBuildSpillResponse_SpillToExplicitPath(t *testing.T) {
	e := &DQLExecutor{}
	result, records := sampleResult(false)
	dest := filepath.Join(t.TempDir(), "out.csv")
	opts := DQLExecuteOptions{
		Spill: SpillOptions{Mode: SpillAlways, ToPath: dest, Threshold: 0},
	}
	resp, spilled, err := e.buildSpillResponse("fetch logs", result, records, "json", opts)
	if err != nil || !spilled {
		t.Fatalf("spilled=%v err=%v", spilled, err)
	}
	m := resp.Result.(*output.ResultFileManifest)
	if m.Path != dest {
		t.Errorf("path = %q, want %q", m.Path, dest)
	}
	if m.Format != "csv" {
		t.Errorf("format = %q, want csv (from extension)", m.Format)
	}
	if _, serr := os.Stat(dest); serr != nil {
		t.Errorf("explicit spill file not written: %v", serr)
	}
	// user-chosen path must surface the privacy opt-out warning (D25)
	found := false
	for _, w := range resp.Context.Warnings {
		if w == userPathPrivacyWarning {
			found = true
		}
	}
	if !found {
		t.Errorf("expected user-path privacy warning, got %v", resp.Context.Warnings)
	}
}

func TestSpillEnvelope_JSONShape(t *testing.T) {
	e := &DQLExecutor{}

	// result-file: envelope_version present, kind=result-file, has path.
	result, records := sampleResult(false)
	opts := DQLExecuteOptions{Spill: SpillOptions{Mode: SpillAlways, Threshold: 0, Dir: t.TempDir(), Format: "json"}}
	resp, _, err := e.buildSpillResponse("fetch logs", result, records, "json", opts)
	if err != nil {
		t.Fatal(err)
	}
	js, _ := json.Marshal(resp)
	s := string(js)
	for _, want := range []string{`"envelope_version":1`, `"kind":"result-file"`, `"path":`, `"decided":"spilled"`, `"columns":`} {
		if !strings.Contains(s, want) {
			t.Errorf("result-file envelope missing %s\n%s", want, s)
		}
	}
	if strings.Contains(s, `"sample_stats"`) {
		t.Errorf("non-sampled envelope must not contain sample_stats")
	}

	// summary-only: no path key, kind=summary-only.
	f := filepath.Join(t.TempDir(), "afile")
	_ = os.WriteFile(f, []byte("x"), 0o600)
	opts = DQLExecuteOptions{Spill: SpillOptions{Mode: SpillAlways, Threshold: 0, Dir: filepath.Join(f, "x"), Format: "json"}}
	resp, _, err = e.buildSpillResponse("fetch logs", result, records, "json", opts)
	if err != nil {
		t.Fatal(err)
	}
	js, _ = json.Marshal(resp)
	s = string(js)
	if !strings.Contains(s, `"kind":"summary-only"`) {
		t.Errorf("summary-only kind missing\n%s", s)
	}
	if strings.Contains(s, `"path":`) {
		t.Errorf("summary-only must omit path\n%s", s)
	}
}

func TestBuildSpillResponse_EmptyResultStillWritesOnAlways(t *testing.T) {
	e := &DQLExecutor{}
	result := &DQLQueryResponse{Records: []map[string]interface{}{}}
	dest := filepath.Join(t.TempDir(), "out.json")
	opts := DQLExecuteOptions{Spill: SpillOptions{Mode: SpillAlways, ToPath: dest, Threshold: 0}}

	resp, spilled, err := e.buildSpillResponse("fetch logs | limit 0", result, result.GetRecords(), "json", opts)
	if err != nil || !spilled {
		t.Fatalf("--spill-to must write even an empty result: spilled=%v err=%v", spilled, err)
	}
	m := resp.Result.(*output.ResultFileManifest)
	if m.Rows != 0 {
		t.Errorf("rows = %d, want 0", m.Rows)
	}
	if _, serr := os.Stat(dest); serr != nil {
		t.Errorf("explicit destination should exist for an empty result: %v", serr)
	}
}

func TestBuildSpillResponse_JQWarning(t *testing.T) {
	e := &DQLExecutor{}
	result, records := sampleResult(false)
	opts := DQLExecuteOptions{
		JQFilter: ".[] | {host}",
		Spill:    SpillOptions{Mode: SpillAlways, Threshold: 0, Dir: t.TempDir(), Format: "json"},
	}
	resp, spilled, err := e.buildSpillResponse("fetch logs", result, records, "json", opts)
	if err != nil || !spilled {
		t.Fatalf("spilled=%v err=%v", spilled, err)
	}
	found := false
	for _, w := range resp.Context.Warnings {
		if strings.Contains(w, "--jq was not applied") {
			found = true
		}
	}
	if !found {
		t.Errorf("expected a --jq-not-applied warning, got %v", resp.Context.Warnings)
	}
}

func TestBuildSpillResponse_UnsupportedFormatErrors(t *testing.T) {
	e := &DQLExecutor{}
	result, records := sampleResult(false)
	opts := DQLExecuteOptions{
		Spill: SpillOptions{Mode: SpillAlways, Threshold: 0, Dir: t.TempDir(), Format: "avro"},
	}
	_, _, err := e.buildSpillResponse("fetch logs", result, records, "json", opts)
	if err == nil {
		t.Fatal("expected error for an unsupported spill format")
	}
}

func TestBuildSpillResponse_DefaultsToJSONL(t *testing.T) {
	e := &DQLExecutor{}
	result, records := sampleResult(false)
	opts := DQLExecuteOptions{
		// No Format set -> the default spill format (jsonl) is used.
		Spill: SpillOptions{Mode: SpillAlways, Threshold: 0, Dir: t.TempDir()},
	}
	resp, spilled, err := e.buildSpillResponse("fetch logs", result, records, "json", opts)
	if err != nil || !spilled {
		t.Fatalf("spilled=%v err=%v", spilled, err)
	}
	m := resp.Result.(*output.ResultFileManifest)
	if m.Format != "jsonl" {
		t.Errorf("default format = %q, want jsonl", m.Format)
	}
	if filepath.Ext(m.Path) != ".jsonl" {
		t.Errorf("default spill path %q should end in .jsonl", m.Path)
	}
	// The file is valid JSON Lines: one JSON object per non-empty line.
	data, rerr := os.ReadFile(m.Path)
	if rerr != nil {
		t.Fatalf("read spill: %v", rerr)
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) != 3 {
		t.Fatalf("jsonl line count = %d, want 3\n%s", len(lines), data)
	}
	for _, ln := range lines {
		var row map[string]interface{}
		if jerr := json.Unmarshal([]byte(ln), &row); jerr != nil {
			t.Errorf("line is not valid JSON: %q: %v", ln, jerr)
		}
	}
}

func TestBuildSpillResponse_ParquetUsesDQLTypes(t *testing.T) {
	e := &DQLExecutor{}
	// "count" arrives as a JSON string but is typed `long` in DQL. With the DQL
	// types threaded into the Parquet writer, the schema must make it an INT64
	// column; without types it would be value-inferred as a string (BYTE_ARRAY).
	records := []map[string]interface{}{
		{"count": "194414758", "host": "web-01"},
		{"count": "42", "host": "web-02"},
	}
	result := &DQLQueryResponse{Result: &DQLResult{
		Records: records,
		Types: []sdkquery.ColumnTypes{{
			Mappings: map[string]sdkquery.ColumnType{
				"count": {Type: "long"},
				"host":  {Type: "string"},
			},
		}},
	}}
	dest := filepath.Join(t.TempDir(), "out.parquet")
	opts := DQLExecuteOptions{Spill: SpillOptions{Mode: SpillAlways, ToPath: dest, Threshold: 0}}

	_, spilled, err := e.buildSpillResponse("fetch logs | summarize count()", result, records, "json", opts)
	if err != nil || !spilled {
		t.Fatalf("spilled=%v err=%v", spilled, err)
	}

	data, rerr := os.ReadFile(dest)
	if rerr != nil {
		t.Fatalf("read parquet: %v", rerr)
	}
	f, ferr := parquet.OpenFile(bytes.NewReader(data), int64(len(data)))
	if ferr != nil {
		t.Fatalf("open parquet: %v", ferr)
	}
	col, ok := f.Schema().Lookup("count")
	if !ok {
		t.Fatal("count column missing from parquet schema")
	}
	if got := col.Node.Type().Kind(); got != parquet.Int64 {
		t.Errorf("count physical type = %v, want Int64 — DQL `long` type was not applied to the spill", got)
	}
}

func TestBuildSpillResponse_ParquetExplicitPath(t *testing.T) {
	e := &DQLExecutor{}
	result, records := sampleResult(false)
	dest := filepath.Join(t.TempDir(), "out.parquet")
	opts := DQLExecuteOptions{
		Spill: SpillOptions{Mode: SpillAlways, ToPath: dest, Threshold: 0},
	}
	resp, spilled, err := e.buildSpillResponse("fetch logs", result, records, "json", opts)
	if err != nil || !spilled {
		t.Fatalf("parquet spill should succeed now that the writer exists: spilled=%v err=%v", spilled, err)
	}
	m := resp.Result.(*output.ResultFileManifest)
	if m.Format != "parquet" {
		t.Errorf("format = %q, want parquet (from extension)", m.Format)
	}
	if fi, serr := os.Stat(dest); serr != nil || fi.Size() == 0 {
		t.Errorf("parquet spill file missing or empty: err=%v", serr)
	}
}

// resultWithScanStats returns a small result carrying Grail execution metadata
// (queryId, execution time, scanned records/bytes) so the envelope-building
// paths can be checked for the top-level `metadata` block (#362).
func resultWithScanStats() (*DQLQueryResponse, []map[string]interface{}) {
	resp, records := sampleResult(false)
	resp.Metadata.Grail.QueryID = "q-123"
	resp.Metadata.Grail.ExecutionTimeMilliseconds = 219
	resp.Metadata.Grail.ScannedRecords = 4200
	resp.Metadata.Grail.ScannedBytes = 987654
	return resp, records
}

func metadataMap(t *testing.T, resp output.Response) map[string]interface{} {
	t.Helper()
	if resp.Metadata == nil {
		t.Fatalf("envelope has no top-level metadata")
	}
	// The metadata must live at the envelope's top level, not inside the result
	// payload: an agent reads d["metadata"], mirroring `-o json --metadata`.
	js, err := json.Marshal(resp)
	if err != nil {
		t.Fatalf("marshal envelope: %v", err)
	}
	var parsed map[string]interface{}
	if err := json.Unmarshal(js, &parsed); err != nil {
		t.Fatalf("unmarshal envelope: %v", err)
	}
	m, ok := parsed["metadata"].(map[string]interface{})
	if !ok {
		t.Fatalf("top-level metadata is %T, want object:\n%s", parsed["metadata"], js)
	}
	return m
}

func TestBuildSpillResponse_InlineMetadataTopLevel(t *testing.T) {
	e := &DQLExecutor{}
	result, records := resultWithScanStats()
	opts := DQLExecuteOptions{
		AgentMode:      true,
		MetadataFields: []string{"all"},
		Spill:          SpillOptions{Mode: SpillAuto, Threshold: 1 << 20, Dir: t.TempDir(), Format: "json"},
	}
	resp, handled, err := e.buildSpillResponse("fetch logs", result, records, "json", opts)
	if err != nil || !handled {
		t.Fatalf("buildSpillResponse: handled=%v err=%v", handled, err)
	}
	if resp.Context.Decided != "inline" {
		t.Fatalf("decided = %q, want inline", resp.Context.Decided)
	}
	m := metadataMap(t, resp)
	if m["executionTimeMilliseconds"] != float64(219) {
		t.Errorf("executionTimeMilliseconds = %v, want 219", m["executionTimeMilliseconds"])
	}
	if m["scannedBytes"] != float64(987654) {
		t.Errorf("scannedBytes = %v, want 987654", m["scannedBytes"])
	}
	if m["queryId"] != "q-123" {
		t.Errorf("queryId = %v, want q-123", m["queryId"])
	}
	// The result payload must not also carry the metadata (no duplication).
	ir := resp.Result.(*output.InlineRecords)
	rjs, _ := json.Marshal(ir)
	if strings.Contains(string(rjs), "executionTimeMilliseconds") {
		t.Errorf("metadata leaked into result payload:\n%s", rjs)
	}
}

func TestBuildSpillResponse_SpilledMetadataTopLevel(t *testing.T) {
	e := &DQLExecutor{}
	result, records := resultWithScanStats()
	opts := DQLExecuteOptions{
		AgentMode:      true,
		MetadataFields: []string{"all"},
		Spill:          SpillOptions{Mode: SpillAlways, Threshold: 1 << 20, Dir: t.TempDir(), Format: "json"},
	}
	resp, spilled, err := e.buildSpillResponse("fetch logs", result, records, "json", opts)
	if err != nil || !spilled {
		t.Fatalf("expected spilled: spilled=%v err=%v", spilled, err)
	}
	m := metadataMap(t, resp)
	if m["scannedRecords"] != float64(4200) {
		t.Errorf("scannedRecords = %v, want 4200", m["scannedRecords"])
	}
}

func TestBuildSpillResponse_FieldSelectionHonored(t *testing.T) {
	e := &DQLExecutor{}
	result, records := resultWithScanStats()
	opts := DQLExecuteOptions{
		AgentMode:      true,
		MetadataFields: []string{"executionTimeMilliseconds"},
		Spill:          SpillOptions{Mode: SpillAuto, Threshold: 1 << 20, Dir: t.TempDir(), Format: "json"},
	}
	resp, _, err := e.buildSpillResponse("fetch logs", result, records, "json", opts)
	if err != nil {
		t.Fatalf("buildSpillResponse: %v", err)
	}
	m := metadataMap(t, resp)
	if _, ok := m["executionTimeMilliseconds"]; !ok {
		t.Errorf("selected field executionTimeMilliseconds missing: %v", m)
	}
	if _, ok := m["scannedBytes"]; ok {
		t.Errorf("field selection not honored: scannedBytes should be absent: %v", m)
	}
}

func TestBuildSpillResponse_NoMetadataWhenNotRequested(t *testing.T) {
	e := &DQLExecutor{}
	result, records := resultWithScanStats()
	opts := DQLExecuteOptions{
		AgentMode: true,
		// MetadataFields empty: agent did not request metadata.
		Spill: SpillOptions{Mode: SpillAuto, Threshold: 1 << 20, Dir: t.TempDir(), Format: "json"},
	}
	resp, _, err := e.buildSpillResponse("fetch logs", result, records, "json", opts)
	if err != nil {
		t.Fatalf("buildSpillResponse: %v", err)
	}
	if resp.Metadata != nil {
		t.Errorf("metadata should be omitted when not requested, got %v", resp.Metadata)
	}
	js, _ := json.Marshal(resp)
	if strings.Contains(string(js), `"metadata"`) {
		t.Errorf("metadata key should be absent:\n%s", js)
	}
}
