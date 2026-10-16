package exec

import (
	"bufio"
	"encoding/json"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/dynatrace-oss/dtctl/pkg/output"
)

// compactableResult returns rows shaped like real span/log results: a resource
// attribute shared by every row, an explicit null column, and a varying column.
func compactableResult() (*DQLQueryResponse, []map[string]interface{}) {
	records := []map[string]interface{}{
		{"k8s.cluster.name": "prod-eu", "service.name": "payment", "span.name": "POST /pay", "error": nil},
		{"k8s.cluster.name": "prod-eu", "service.name": "payment", "span.name": "GET /cart", "error": nil},
		{"k8s.cluster.name": "prod-eu", "service.name": "payment", "span.name": "GET /pay", "error": nil},
	}
	return &DQLQueryResponse{Records: records}, records
}

func inlineOpts(compact bool) DQLExecuteOptions {
	return DQLExecuteOptions{
		AgentMode: true,
		Compact:   compact,
		Spill:     SpillOptions{Mode: SpillAuto, Threshold: 1 << 20, Format: "json"},
	}
}

func TestBuildSpillResponse_CompactInlineRecords(t *testing.T) {
	e := &DQLExecutor{}
	result, records := compactableResult()
	opts := inlineOpts(true)
	opts.Spill.Dir = t.TempDir()

	resp, handled, err := e.buildSpillResponse("fetch spans", result, records, "json", opts)
	if err != nil || !handled {
		t.Fatalf("handled=%v err=%v", handled, err)
	}
	ir, ok := resp.Result.(*output.InlineRecords)
	if !ok {
		t.Fatalf("result is %T, want *InlineRecords", resp.Result)
	}
	if ir.Kind != output.KindRecords {
		t.Errorf("kind = %q, want records (compaction must not change the discriminator)", ir.Kind)
	}
	wantConst := map[string]interface{}{"k8s.cluster.name": "prod-eu", "service.name": "payment"}
	if !reflect.DeepEqual(ir.Constant, wantConst) {
		t.Errorf("constant = %v, want %v", ir.Constant, wantConst)
	}
	for i, r := range ir.Records {
		if len(r) != 1 || r["span.name"] == nil {
			t.Errorf("record %d = %v, want only span.name", i, r)
		}
	}
	if total := *resp.Context.Total; total != 3 {
		t.Errorf("context.total = %d, want 3", total)
	}
	// The caller's rows are untouched: they are also what a spill would write.
	if _, ok := records[0]["error"]; !ok {
		t.Error("input records were mutated")
	}

	js, _ := json.Marshal(resp)
	if strings.Index(string(js), `"constant"`) > strings.Index(string(js), `"records":[`) {
		t.Errorf("constant must precede records:\n%s", js)
	}
	if strings.Contains(string(js), ":null") {
		t.Errorf("compacted envelope still carries nulls:\n%s", js)
	}
}

// Compaction measures what the agent actually receives, so a result whose bulk
// is shared attributes stays inline instead of spilling.
func TestBuildSpillResponse_CompactMeasuresCompactedSize(t *testing.T) {
	e := &DQLExecutor{}
	result, records := compactableResult()

	full := inlineOpts(false)
	full.Spill.Dir = t.TempDir()
	respFull, _, _ := e.buildSpillResponse("fetch spans", result, records, "json", full)

	compact := inlineOpts(true)
	compact.Spill.Dir = t.TempDir()
	respCompact, _, _ := e.buildSpillResponse("fetch spans", result, records, "json", compact)

	if respCompact.Context.MeasuredBytes >= respFull.Context.MeasuredBytes {
		t.Errorf("measured_bytes compact=%d full=%d, want compact smaller",
			respCompact.Context.MeasuredBytes, respFull.Context.MeasuredBytes)
	}

	// A threshold between the two sizes: compact stays inline, full spills.
	threshold := (respCompact.Context.MeasuredBytes + respFull.Context.MeasuredBytes) / 2
	compact.Spill.Threshold = threshold
	full.Spill.Threshold = threshold
	if r, _, _ := e.buildSpillResponse("fetch spans", result, records, "json", compact); r.Context.Decided != "inline" {
		t.Errorf("compact decided = %q, want inline", r.Context.Decided)
	}
	if r, _, _ := e.buildSpillResponse("fetch spans", result, records, "json", full); r.Context.Decided != "spilled" {
		t.Errorf("full decided = %q, want spilled", r.Context.Decided)
	}
}

func TestBuildSpillResponse_NoCompactKeepsRowsVerbatim(t *testing.T) {
	e := &DQLExecutor{}
	result, records := compactableResult()
	opts := inlineOpts(false)
	opts.Spill.Dir = t.TempDir()

	resp, _, err := e.buildSpillResponse("fetch spans", result, records, "json", opts)
	if err != nil {
		t.Fatal(err)
	}
	ir := resp.Result.(*output.InlineRecords)
	if ir.Constant != nil {
		t.Errorf("constant = %v, want nil without --compact", ir.Constant)
	}
	if !reflect.DeepEqual(ir.Records, records) {
		t.Errorf("records changed without --compact: %v", ir.Records)
	}
}

func TestBuildSpillResponse_CompactTOON(t *testing.T) {
	e := &DQLExecutor{}
	result, records := compactableResult()
	opts := inlineOpts(true)
	opts.Spill.Dir = t.TempDir()

	resp, handled, err := e.buildSpillResponse("fetch spans", result, records, "toon", opts)
	if err != nil || !handled {
		t.Fatalf("handled=%v err=%v", handled, err)
	}
	enc, ok := resp.Result.(*output.InlineRecordsEncoded)
	if !ok {
		t.Fatalf("result is %T, want *InlineRecordsEncoded", resp.Result)
	}
	if enc.Constant["service.name"] != "payment" {
		t.Errorf("constant = %v, want service.name hoisted", enc.Constant)
	}
	for _, hoisted := range []string{"service.name", "k8s.cluster.name", "error"} {
		if strings.Contains(enc.Records, hoisted) {
			t.Errorf("TOON rows still carry %q:\n%s", hoisted, enc.Records)
		}
	}
	if !strings.Contains(enc.Records, "span.name") {
		t.Errorf("TOON rows lost the varying column:\n%s", enc.Records)
	}
}

// The spill manifest collapses single-value columns into one map and lists the
// all-null ones by name, while the file and the sidecar keep everything.
func TestBuildSpillResponse_CompactManifest(t *testing.T) {
	e := &DQLExecutor{}
	result, records := compactableResult()
	opts := DQLExecuteOptions{
		AgentMode: true,
		Compact:   true,
		Spill:     SpillOptions{Mode: SpillAlways, Threshold: 1 << 20, Dir: t.TempDir(), Format: "jsonl"},
	}
	resp, _, err := e.buildSpillResponse("fetch spans", result, records, "json", opts)
	if err != nil {
		t.Fatal(err)
	}
	m := resp.Result.(*output.ResultFileManifest)

	wantConst := map[string]interface{}{"k8s.cluster.name": "prod-eu", "service.name": "payment"}
	if !reflect.DeepEqual(m.Constant, wantConst) {
		t.Errorf("constant = %v, want %v", m.Constant, wantConst)
	}
	if !reflect.DeepEqual(m.NullColumns, []string{"error"}) {
		t.Errorf("null_columns = %v, want [error]", m.NullColumns)
	}
	if len(m.Columns) != 1 || m.Columns[0].Name != "span.name" {
		t.Errorf("columns = %+v, want only span.name", m.Columns)
	}
	for i, r := range m.SampleRows {
		if len(r) != 1 || r["span.name"] == nil {
			t.Errorf("sample row %d = %v, want only span.name", i, r)
		}
	}

	sc, err := output.ReadSidecar(m.Path)
	if err != nil {
		t.Fatal(err)
	}
	if len(sc.Columns) != 4 {
		t.Errorf("sidecar columns = %d, want all 4", len(sc.Columns))
	}
	f, err := os.Open(m.Path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	sc2 := bufio.NewScanner(f)
	if !sc2.Scan() {
		t.Fatal("spill file is empty")
	}
	var row map[string]interface{}
	if err := json.Unmarshal(sc2.Bytes(), &row); err != nil {
		t.Fatal(err)
	}
	if len(row) != 4 {
		t.Errorf("spilled row = %v, want the full untouched row", row)
	}
}

// A sampled manifest collapses the same columns out of sample_stats.
func TestBuildSpillResponse_CompactManifestSampled(t *testing.T) {
	e := &DQLExecutor{}
	result, records := compactableResult()
	result.Metadata = &DQLMetadata{Grail: &GrailMetadata{Sampled: true}}
	opts := DQLExecuteOptions{
		AgentMode: true,
		Compact:   true,
		Spill:     SpillOptions{Mode: SpillAlways, Threshold: 1 << 20, Dir: t.TempDir(), Format: "jsonl"},
	}
	resp, _, err := e.buildSpillResponse("fetch spans", result, records, "json", opts)
	if err != nil {
		t.Fatal(err)
	}
	m := resp.Result.(*output.ResultFileManifest)
	if m.SampleStats == nil || len(m.SampleStats.Columns) != 1 {
		t.Errorf("sample_stats = %+v, want only span.name", m.SampleStats)
	}
	if m.Constant == nil {
		t.Error("constant missing on a sampled manifest")
	}
}

func TestBuildSpillResponse_NoCompactManifestUnchanged(t *testing.T) {
	e := &DQLExecutor{}
	result, records := compactableResult()
	opts := DQLExecuteOptions{
		AgentMode: true,
		Spill:     SpillOptions{Mode: SpillAlways, Threshold: 1 << 20, Dir: t.TempDir(), Format: "jsonl"},
	}
	resp, _, err := e.buildSpillResponse("fetch spans", result, records, "json", opts)
	if err != nil {
		t.Fatal(err)
	}
	m := resp.Result.(*output.ResultFileManifest)
	if m.Constant != nil || m.NullColumns != nil {
		t.Errorf("constant=%v null_columns=%v, want neither without --compact", m.Constant, m.NullColumns)
	}
	if len(m.Columns) != 4 {
		t.Errorf("columns = %d, want 4", len(m.Columns))
	}
}

// Outside agent mode --compact is opt-in and reshapes the plain json document
// the same way: a sibling `constant` map before `records`.
func TestPrintResults_CompactPlainJSON(t *testing.T) {
	e := &DQLExecutor{}
	result, _ := compactableResult()

	out := captureStdout(t, func() {
		if err := e.printResults("fetch spans", result, DQLExecuteOptions{OutputFormat: "json", Compact: true}); err != nil {
			t.Fatal(err)
		}
	})
	var doc struct {
		Constant map[string]interface{}   `json:"constant"`
		Records  []map[string]interface{} `json:"records"`
	}
	if err := json.Unmarshal(out, &doc); err != nil {
		t.Fatalf("invalid JSON: %v\n%s", err, out)
	}
	if doc.Constant["service.name"] != "payment" {
		t.Errorf("constant = %v", doc.Constant)
	}
	if len(doc.Records) != 3 || len(doc.Records[0]) != 1 {
		t.Errorf("records = %v, want 3 rows with only span.name", doc.Records)
	}
	if strings.Index(string(out), `"constant"`) > strings.Index(string(out), `"records"`) {
		t.Errorf("constant must precede records:\n%s", out)
	}
}

func TestPrintResults_PlainJSONUnchangedWithoutCompact(t *testing.T) {
	e := &DQLExecutor{}
	result, _ := compactableResult()
	out := captureStdout(t, func() {
		if err := e.printResults("fetch spans", result, DQLExecuteOptions{OutputFormat: "json"}); err != nil {
			t.Fatal(err)
		}
	})
	if strings.Contains(string(out), `"constant"`) || !strings.Contains(string(out), `"error": null`) {
		t.Errorf("plain json changed without --compact:\n%s", out)
	}
}

// A --jq program addresses the full rows, so --compact never reshapes its input.
func TestPrintResults_CompactIgnoredUnderJQ(t *testing.T) {
	e := &DQLExecutor{}
	result, _ := compactableResult()
	out := captureStdout(t, func() {
		if err := e.printResults("fetch spans", result, DQLExecuteOptions{OutputFormat: "json", Compact: true, JQFilter: ".records[0][\"service.name\"]"}); err != nil {
			t.Fatal(err)
		}
	})
	if strings.TrimSpace(string(out)) != `"payment"` {
		t.Errorf("jq saw compacted rows: %s", out)
	}
}

// Under -o toon a partial null stays in its row, so the rows remain one table.
func TestBuildSpillResponse_CompactTOONStaysTabular(t *testing.T) {
	e := &DQLExecutor{}
	records := []map[string]interface{}{
		{"service.name": "payment", "span.name": "a", "status": nil},
		{"service.name": "payment", "span.name": "b", "status": "ERROR"},
	}
	opts := inlineOpts(true)
	opts.Spill.Dir = t.TempDir()
	resp, _, err := e.buildSpillResponse("fetch spans", &DQLQueryResponse{Records: records}, records, "toon", opts)
	if err != nil {
		t.Fatal(err)
	}
	enc := resp.Result.(*output.InlineRecordsEncoded)
	if !strings.Contains(enc.Records, "{span.name,status}") {
		t.Errorf("TOON rows are not one table:\n%s", enc.Records)
	}
	if strings.Contains(enc.Records, "service.name") {
		t.Errorf("constant column still in TOON rows:\n%s", enc.Records)
	}
}

func TestPrintResults_CompactPlainTOONStaysTabular(t *testing.T) {
	e := &DQLExecutor{}
	records := []map[string]interface{}{
		{"service.name": "payment", "span.name": "a", "status": nil},
		{"service.name": "payment", "span.name": "b", "status": "ERROR"},
	}
	out := captureStdout(t, func() {
		if err := e.printResults("fetch spans", &DQLQueryResponse{Records: records}, DQLExecuteOptions{OutputFormat: "toon", Compact: true}); err != nil {
			t.Fatal(err)
		}
	})
	if !strings.Contains(string(out), "{span.name,status}") || !strings.Contains(string(out), "constant") {
		t.Errorf("plain toon not compacted into constant + one table:\n%s", out)
	}
}

func hasCompactSuggestion(resp output.Response) bool {
	for _, s := range resp.Context.Suggestions {
		if strings.Contains(s, "--compact=false") {
			return true
		}
	}
	return false
}

// A compaction that changed the rows says so once, naming the opt-out; one
// that changed nothing, or no compaction at all, adds nothing.
func TestBuildSpillResponse_CompactSuggestionOnlyWhenChanged(t *testing.T) {
	e := &DQLExecutor{}
	distinct := []map[string]interface{}{{"a": "x"}, {"a": "y"}}
	partialNull := []map[string]interface{}{{"a": "x", "b": nil}, {"a": "y", "b": "z"}}
	cases := []struct {
		name    string
		records []map[string]interface{}
		format  string
		compact bool
		spill   SpillMode
		want    bool
	}{
		{"json hoisted", nil, "json", true, SpillAuto, true},
		{"json nothing to compact", distinct, "json", true, SpillAuto, false},
		{"json partial null dropped", partialNull, "json", true, SpillAuto, true},
		// TOON keeps partial nulls, so the same rows are not changed there.
		{"toon partial null kept", partialNull, "toon", true, SpillAuto, false},
		{"toon hoisted", nil, "toon", true, SpillAuto, true},
		{"opt-out", nil, "json", false, SpillAuto, false},
		{"manifest compacted", nil, "json", true, SpillAlways, true},
		{"manifest nothing to compact", distinct, "json", true, SpillAlways, false},
		{"manifest opt-out", nil, "json", false, SpillAlways, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			records := tc.records
			if records == nil {
				_, records = compactableResult()
			}
			opts := DQLExecuteOptions{
				AgentMode: true,
				Compact:   tc.compact,
				Spill:     SpillOptions{Mode: tc.spill, Threshold: 1 << 20, Dir: t.TempDir(), Format: "jsonl"},
			}
			resp, _, err := e.buildSpillResponse("fetch spans", &DQLQueryResponse{Records: records}, records, tc.format, opts)
			if err != nil {
				t.Fatal(err)
			}
			if got := hasCompactSuggestion(resp); got != tc.want {
				t.Errorf("compact suggestion = %v, want %v (suggestions: %q)", got, tc.want, resp.Context.Suggestions)
			}
		})
	}
}

// -o auto with compaction chooses the encoding from the compacted table, keeps
// constant as a JSON map beside the encoded rows, and a tabular choice (CSV)
// keeps partial nulls as empty cells so the rows stay one table.
func TestBuildSpillResponse_CompactAutoPicksCSVWithConstant(t *testing.T) {
	e := &DQLExecutor{}
	records := []map[string]interface{}{
		// The nested constant alone would push -o auto to yaml; hoisted, the
		// remaining rows are flat and csv wins.
		{"dt.source_entity": []interface{}{"HOST-1"}, "service.name": "payment", "span.name": "a", "status": nil, "gone": nil},
		{"dt.source_entity": []interface{}{"HOST-1"}, "service.name": "payment", "span.name": "b", "status": "ERROR", "gone": nil},
		{"dt.source_entity": []interface{}{"HOST-1"}, "service.name": "payment", "span.name": "c", "status": "OK", "gone": nil},
	}
	opts := inlineOpts(true)
	opts.Spill.Dir = t.TempDir()

	resp, handled, err := e.buildSpillResponse("fetch spans", &DQLQueryResponse{Records: records}, records, "auto", opts)
	if err != nil || !handled {
		t.Fatalf("handled=%v err=%v", handled, err)
	}
	enc, ok := resp.Result.(*output.InlineRecordsEncoded)
	if !ok {
		t.Fatalf("result is %T, want *InlineRecordsEncoded", resp.Result)
	}
	if enc.Encoding != "csv" || resp.Context.Format != "csv" || resp.Context.MeasuredEncoding != "csv" {
		t.Errorf("encoding=%q context.format=%q measured=%q, want csv throughout", enc.Encoding, resp.Context.Format, resp.Context.MeasuredEncoding)
	}
	if enc.Constant["service.name"] != "payment" || enc.Constant["dt.source_entity"] == nil {
		t.Errorf("constant = %v, want service.name and dt.source_entity", enc.Constant)
	}
	lines := strings.Split(strings.TrimSpace(enc.Records), "\n")
	if len(lines) != 4 || lines[0] != "span.name,status" {
		t.Fatalf("csv = %q, want header span.name,status + 3 rows", enc.Records)
	}
	if lines[1] != "a," {
		t.Errorf("partial null should stay an empty cell, got %q", lines[1])
	}
	if !hasCompactSuggestion(resp) {
		t.Error("compaction changed the rows; expected the opt-out suggestion")
	}
	js, _ := json.Marshal(resp)
	if strings.Index(string(js), `"constant"`) > strings.Index(string(js), `"records":"`) {
		t.Errorf("constant must precede records:\n%s", js)
	}
}

func TestBuildSpillResponse_CompactAutoYAMLDropsNulls(t *testing.T) {
	e := &DQLExecutor{}
	records := []map[string]interface{}{
		{"service.name": "payment", "attrs": map[string]interface{}{"a": "1"}, "status": nil},
		{"service.name": "payment", "attrs": map[string]interface{}{"a": "2"}, "status": "ERROR"},
	}
	opts := inlineOpts(true)
	opts.Spill.Dir = t.TempDir()

	resp, _, err := e.buildSpillResponse("fetch spans", &DQLQueryResponse{Records: records}, records, "auto", opts)
	if err != nil {
		t.Fatal(err)
	}
	enc := resp.Result.(*output.InlineRecordsEncoded)
	if enc.Encoding != "yaml" {
		t.Fatalf("encoding = %q, want yaml for nested rows", enc.Encoding)
	}
	if enc.Constant["service.name"] != "payment" {
		t.Errorf("constant = %v", enc.Constant)
	}
	if strings.Contains(enc.Records, "service.name") || strings.Contains(enc.Records, "null") {
		t.Errorf("yaml rows still carry the constant or a null:\n%s", enc.Records)
	}
}

// --compact=false under -o auto is exactly main's -o auto envelope.
func TestBuildSpillResponse_AutoOptOutMatchesUncompacted(t *testing.T) {
	e := &DQLExecutor{}
	records := []map[string]interface{}{
		{"service.name": "payment", "span.name": "a", "status": nil},
		{"service.name": "payment", "span.name": "b", "status": "ERROR"},
	}
	opts := inlineOpts(false)
	opts.Spill.Dir = t.TempDir()
	resp, _, err := e.buildSpillResponse("fetch spans", &DQLQueryResponse{Records: records}, records, "auto", opts)
	if err != nil {
		t.Fatal(err)
	}
	enc := resp.Result.(*output.InlineRecordsEncoded)
	_, want, err := output.MarshalAuto(records)
	if err != nil {
		t.Fatal(err)
	}
	if enc.Constant != nil || enc.Records != want {
		t.Errorf("opt-out changed -o auto output:\ngot  %q (constant %v)\nwant %q", enc.Records, enc.Constant, want)
	}
	if hasCompactSuggestion(resp) {
		t.Error("no compaction, so no suggestion")
	}
}

// Outside agent mode -o auto chooses from the full rows: a csv choice prints
// them unchanged (plain CSV has no place for constant), a yaml choice is
// compacted like -o yaml.
func TestPrintResults_CompactPlainAuto(t *testing.T) {
	e := &DQLExecutor{}
	flat := []map[string]interface{}{
		{"service.name": "payment", "span.name": "a"},
		{"service.name": "payment", "span.name": "b"},
	}
	out := captureStdout(t, func() {
		if err := e.printResults("fetch spans", &DQLQueryResponse{Records: flat}, DQLExecuteOptions{OutputFormat: "auto", Compact: true}); err != nil {
			t.Fatal(err)
		}
	})
	if !strings.HasPrefix(string(out), "service.name,span.name\n") {
		t.Errorf("csv choice should print the full rows:\n%s", out)
	}

	nested := []map[string]interface{}{
		{"service.name": "payment", "attrs": map[string]interface{}{"a": "1"}, "status": nil},
		{"service.name": "payment", "attrs": map[string]interface{}{"a": "2"}, "status": nil},
	}
	out = captureStdout(t, func() {
		if err := e.printResults("fetch spans", &DQLQueryResponse{Records: nested}, DQLExecuteOptions{OutputFormat: "auto", Compact: true}); err != nil {
			t.Fatal(err)
		}
	})
	if !strings.Contains(string(out), "constant:") || strings.Contains(string(out), "null") {
		t.Errorf("yaml choice should be compacted:\n%s", out)
	}
}

// The agent-mode defaults stack: -o auto by default (AutoFormatByDefault) with
// compaction on encodes the compacted rows, keeps constant beside them and
// carries both advice lines; --compact=false restores main's -o auto output.
func TestBuildSpillResponse_AgentDefaultsCompose(t *testing.T) {
	e := &DQLExecutor{}
	records := []map[string]interface{}{
		{"service.name": "payment", "span.name": "a", "gone": nil},
		{"service.name": "payment", "span.name": "b", "gone": nil},
	}
	hasAutoSuggestion := func(resp output.Response) bool {
		for _, s := range resp.Context.Suggestions {
			if s == output.AutoDefaultSuggestion("csv") {
				return true
			}
		}
		return false
	}

	opts := inlineOpts(true)
	opts.AutoFormatByDefault = true
	opts.Spill.Dir = t.TempDir()
	resp, _, err := e.buildSpillResponse("fetch spans", &DQLQueryResponse{Records: records}, records, "auto", opts)
	if err != nil {
		t.Fatal(err)
	}
	enc := resp.Result.(*output.InlineRecordsEncoded)
	if enc.Encoding != "csv" || enc.Records != "span.name\na\nb\n" || enc.Constant["service.name"] != "payment" {
		t.Errorf("defaults: encoding=%q records=%q constant=%v", enc.Encoding, enc.Records, enc.Constant)
	}
	if !hasAutoSuggestion(resp) || !hasCompactSuggestion(resp) {
		t.Errorf("want both the -o auto and the --compact advice, got %q", resp.Context.Suggestions)
	}

	opts.Compact = false
	resp, _, err = e.buildSpillResponse("fetch spans", &DQLQueryResponse{Records: records}, records, "auto", opts)
	if err != nil {
		t.Fatal(err)
	}
	enc = resp.Result.(*output.InlineRecordsEncoded)
	_, want, _ := output.MarshalAuto(records)
	if enc.Constant != nil || enc.Records != want || hasCompactSuggestion(resp) || !hasAutoSuggestion(resp) {
		t.Errorf("opt-out: records=%q constant=%v suggestions=%q, want %q", enc.Records, enc.Constant, resp.Context.Suggestions, want)
	}
}

// TestCompactRowsSuggestionNamesConstantKeys pins that the note names the columns moved to result.constant.
func TestCompactRowsSuggestionNamesConstantKeys(t *testing.T) {
	s := compactRowsSuggestion(&output.Compaction{Constant: map[string]interface{}{"total": 3, "share": 0.5}})
	if !strings.HasPrefix(s, compactRowsPrefix) || !strings.Contains(s, "result.constant: share, total;") {
		t.Errorf("suggestion = %q, want the sorted constant keys", s)
	}
	if s := compactRowsSuggestion(nil); !strings.HasPrefix(s, compactRowsPrefix) || strings.Contains(s, "listed once") {
		t.Errorf("suggestion without constants = %q", s)
	}
}
