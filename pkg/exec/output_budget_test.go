package exec

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/dynatrace-oss/dtctl/pkg/output"
)

// longContentResult is a result whose content values are far longer than a
// field cap, the shape `fetch logs | fields content | limit N` produces.
func longContentResult(n, contentLen int) (*DQLQueryResponse, []map[string]interface{}) {
	records := make([]map[string]interface{}, n)
	for i := range records {
		records[i] = map[string]interface{}{
			"host":    fmt.Sprintf("web-%02d", i),
			"content": fmt.Sprintf("row-%03d ", i) + strings.Repeat("x", contentLen),
		}
	}
	return &DQLQueryResponse{Records: records}, records
}

func envelopeRows(t *testing.T, resp output.Response) []map[string]interface{} {
	t.Helper()
	ir, ok := resp.Result.(*output.InlineRecords)
	if !ok {
		t.Fatalf("result = %T, want *output.InlineRecords", resp.Result)
	}
	return ir.Records
}

func encodedSize(t *testing.T, resp output.Response) int {
	t.Helper()
	var buf bytes.Buffer
	if err := output.EncodeEnvelope(&buf, resp); err != nil {
		t.Fatalf("encode: %v", err)
	}
	return buf.Len()
}

func TestBuildSpillResponse_FieldCapClipsInlineValues(t *testing.T) {
	e := &DQLExecutor{}
	result, records := longContentResult(3, 1000)
	opts := DQLExecuteOptions{
		AgentMode:     true,
		MaxFieldChars: 50,
		Spill:         SpillOptions{Mode: SpillNever},
	}

	resp, handled, err := e.buildSpillResponse("fetch logs", result, records, "json", opts)
	if err != nil || !handled {
		t.Fatalf("handled=%v err=%v", handled, err)
	}
	rows := envelopeRows(t, resp)
	got := rows[0]["content"].(string)
	if !strings.HasSuffix(got, "…(+958 chars)") || len([]rune(got)) != 50+len([]rune("…(+958 chars)")) {
		t.Errorf("content not clipped with marker: %q", got)
	}
	if rows[0]["host"] != "web-00" {
		t.Errorf("short value changed: %v", rows[0]["host"])
	}
	ctx := resp.Context
	if !ctx.Truncated || ctx.MaxFieldChars != 50 || !reflect.DeepEqual(ctx.TruncatedFields, []string{"content"}) {
		t.Errorf("context does not report the clip: %+v", ctx)
	}
	if ctx.Returned != nil || ctx.NextOffset != nil {
		t.Errorf("a field clip must not claim dropped rows: %+v", ctx)
	}
	if !hasSuggestion(ctx.Suggestions, "--max-field-chars 0") || !hasSuggestion(ctx.Suggestions, "'| fields content'") {
		t.Errorf("suggestions do not say how to get full values: %v", ctx.Suggestions)
	}
	if !strings.HasPrefix(records[0]["content"].(string), "row-000 xxx") || len(records[0]["content"].(string)) != 1008 {
		t.Error("caller's records were mutated")
	}
}

func TestBuildSpillResponse_FieldCapZeroKeepsFullValues(t *testing.T) {
	e := &DQLExecutor{}
	result, records := longContentResult(2, 1000)
	opts := DQLExecuteOptions{AgentMode: true, Spill: SpillOptions{Mode: SpillNever}}

	resp, _, err := e.buildSpillResponse("fetch logs", result, records, "json", opts)
	if err != nil {
		t.Fatal(err)
	}
	// The opt-out restores the pre-cap output exactly: the caller's rows as-is
	// and no truncation marker or suggestion in the envelope.
	if !reflect.DeepEqual(envelopeRows(t, resp), records) {
		t.Error("--max-field-chars 0 must return the rows unchanged")
	}
	var buf bytes.Buffer
	if err := output.EncodeEnvelope(&buf, resp); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"truncated", "max_field_chars", "chars)", "--max-field-chars"} {
		if strings.Contains(buf.String(), key) {
			t.Errorf("uncapped envelope mentions %q", key)
		}
	}
}

// The spill decision measures what inline output would carry — the clipped
// rows — so a result that is large only because of a few long values stays
// inline once they are clipped, instead of spilling to a file.
func TestBuildSpillResponse_FieldCapMeasuredBeforeSpillDecision(t *testing.T) {
	e := &DQLExecutor{}
	result, records := longContentResult(5, 5000) // ~25 KB uncapped
	opts := DQLExecuteOptions{
		AgentMode:     true,
		MaxFieldChars: 100,
		Spill:         SpillOptions{Mode: SpillAuto, Threshold: 10 * 1024, Dir: t.TempDir(), Format: "jsonl"},
	}

	resp, _, err := e.buildSpillResponse("fetch logs", result, records, "json", opts)
	if err != nil {
		t.Fatal(err)
	}
	if resp.Context.Decided != "inline" {
		t.Fatalf("decided = %q, want inline", resp.Context.Decided)
	}
	if resp.Context.MeasuredBytes > 10*1024 {
		t.Errorf("measured_bytes = %d, want the clipped size", resp.Context.MeasuredBytes)
	}
}

// With the cap on (the agent-mode default) but nothing long enough to clip,
// the envelope carries no marker and no opt-out suggestion.
func TestBuildSpillResponse_FieldCapNothingClippedAddsNothing(t *testing.T) {
	e := &DQLExecutor{}
	result, records := longContentResult(3, 10)
	opts := DQLExecuteOptions{AgentMode: true, MaxFieldChars: 500, Spill: SpillOptions{Mode: SpillNever}}

	resp, _, err := e.buildSpillResponse("fetch logs", result, records, "json", opts)
	if err != nil {
		t.Fatal(err)
	}
	if resp.Context.Truncated || resp.Context.MaxFieldChars != 0 || hasSuggestion(resp.Context.Suggestions, "--max-field-chars") {
		t.Errorf("nothing was clipped, context must not say so: %+v", resp.Context)
	}
}

func TestBuildSpillResponse_SpillFileKeepsFullValues(t *testing.T) {
	e := &DQLExecutor{}
	result, records := longContentResult(3, 1000)
	dest := filepath.Join(t.TempDir(), "out.jsonl")
	opts := DQLExecuteOptions{
		AgentMode:     true,
		MaxFieldChars: 50,
		Spill:         SpillOptions{Mode: SpillAlways, ToPath: dest},
	}

	if _, _, err := e.buildSpillResponse("fetch logs", result, records, "json", opts); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(dest)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "chars)") || !strings.Contains(string(data), strings.Repeat("x", 1000)) {
		t.Error("spilled file must hold the full, unclipped values")
	}
}

func TestBuildSpillResponse_BudgetDropsRowsToFit(t *testing.T) {
	e := &DQLExecutor{}
	result, records := longContentResult(50, 100)
	const budget = 2000
	opts := DQLExecuteOptions{
		AgentMode:      true,
		MaxOutputBytes: budget,
		Spill:          SpillOptions{Mode: SpillNever},
	}

	resp, _, err := e.buildSpillResponse("fetch logs", result, records, "json", opts)
	if err != nil {
		t.Fatal(err)
	}
	if size := encodedSize(t, resp); size > budget {
		t.Fatalf("encoded envelope is %d bytes, over the %d-byte budget", size, budget)
	}
	rows := envelopeRows(t, resp)
	ctx := resp.Context
	if len(rows) == 0 || len(rows) >= 50 {
		t.Fatalf("returned %d rows, want a proper prefix", len(rows))
	}
	for i, r := range rows {
		if r["host"] != fmt.Sprintf("web-%02d", i) {
			t.Fatalf("row %d is %v: the kept rows must be the leading rows in order", i, r["host"])
		}
	}
	if !ctx.Truncated || *ctx.Total != 50 || *ctx.Returned != len(rows) || *ctx.NextOffset != len(rows) || ctx.BudgetBytes != budget {
		t.Errorf("context = %+v", ctx)
	}
	if ctx.Next != "" {
		t.Errorf("next = %q, want empty: nothing was written to disk under --spill=never", ctx.Next)
	}
	if !hasSuggestion(ctx.Suggestions, "--spill-to") {
		t.Errorf("suggestions must say how to get the remaining rows: %v", ctx.Suggestions)
	}

	// The fit is maximal: one more row would not have fit.
	opts.MaxOutputBytes = int64(encodedSize(t, resp)) + 100 // < one row's ~139 bytes
	bigger, _, err := e.buildSpillResponse("fetch logs", result, records, "json", opts)
	if err != nil {
		t.Fatal(err)
	}
	if n := len(envelopeRows(t, bigger)); n != len(rows) {
		t.Errorf("with a slightly larger budget %d rows fit, want still %d", n, len(rows))
	}
}

func TestBuildSpillResponse_UnderBudgetIsUntouched(t *testing.T) {
	e := &DQLExecutor{}
	result, records := longContentResult(3, 10)
	opts := DQLExecuteOptions{AgentMode: true, MaxOutputBytes: 1 << 20, Spill: SpillOptions{Mode: SpillNever}}

	resp, _, err := e.buildSpillResponse("fetch logs", result, records, "json", opts)
	if err != nil {
		t.Fatal(err)
	}
	if len(envelopeRows(t, resp)) != 3 || resp.Context.Truncated || resp.Context.BudgetBytes != 0 {
		t.Errorf("an under-budget result must be emitted unchanged: %+v", resp.Context)
	}
}

// With spilling available, a budget cut writes the full result to disk so the
// agent continues from next_offset with inspect instead of re-querying Grail.
func TestBuildSpillResponse_BudgetWritesContinuationFile(t *testing.T) {
	e := &DQLExecutor{}
	result, records := longContentResult(50, 100)
	dir := t.TempDir()
	opts := DQLExecuteOptions{
		AgentMode:      true,
		MaxOutputBytes: 3000,
		ContextName:    "prod",
		Spill:          SpillOptions{Mode: SpillAuto, Threshold: 1 << 20, Dir: dir, Format: "jsonl"},
	}

	resp, _, err := e.buildSpillResponse("fetch logs", result, records, "json", opts)
	if err != nil {
		t.Fatal(err)
	}
	ctx := resp.Context
	if ctx.Decided != "inline" || !ctx.Truncated {
		t.Fatalf("context = %+v", ctx)
	}
	k := *ctx.NextOffset
	matches, _ := filepath.Glob(filepath.Join(dir, "results", "q-*.jsonl"))
	if len(matches) != 1 {
		t.Fatalf("continuation files = %v, want one", matches)
	}
	want := fmt.Sprintf("dtctl inspect %s --page --offset %d --limit %d", ShellQuote(matches[0]), k, k)
	if ctx.Next != want {
		t.Errorf("next = %q, want %q", ctx.Next, want)
	}
	data, err := os.ReadFile(matches[0])
	if err != nil {
		t.Fatal(err)
	}
	if lines := strings.Count(string(data), "\n"); lines != 50 {
		t.Errorf("continuation file has %d rows, want all 50", lines)
	}
	if size := encodedSize(t, resp); size > 3000 {
		t.Errorf("envelope %d bytes over budget", size)
	}
}

// context.next must stay one runnable argument even when the spill location
// contains spaces or shell metacharacters.
func TestBuildSpillResponse_BudgetNextQuotesPath(t *testing.T) {
	e := &DQLExecutor{}
	result, records := longContentResult(50, 100)
	dir := filepath.Join(t.TempDir(), "my dir $(touch x); 'q'")
	opts := DQLExecuteOptions{
		AgentMode:      true,
		MaxOutputBytes: 3000,
		Spill:          SpillOptions{Mode: SpillAuto, Threshold: 1 << 20, Dir: dir, Format: "jsonl"},
	}

	resp, _, err := e.buildSpillResponse("fetch logs", result, records, "json", opts)
	if err != nil {
		t.Fatal(err)
	}
	matches, _ := filepath.Glob(filepath.Join(dir, "results", "q-*.jsonl"))
	if len(matches) != 1 {
		t.Fatalf("continuation files = %v", matches)
	}
	k := *resp.Context.NextOffset
	quoted := "'" + strings.ReplaceAll(matches[0], "'", `'\''`) + "'"
	want := fmt.Sprintf("dtctl inspect %s --page --offset %d --limit %d", quoted, k, k)
	if resp.Context.Next != want {
		t.Errorf("next = %q, want %q", resp.Context.Next, want)
	}
}

func TestShellQuote(t *testing.T) {
	for in, want := range map[string]string{
		"/home/u/.cache/dtctl/results/prod/q-1a2b.jsonl": "/home/u/.cache/dtctl/results/prod/q-1a2b.jsonl",
		"/tmp/my dir/q.jsonl":                            "'/tmp/my dir/q.jsonl'",
		"/tmp/$(rm -rf ~)/q.jsonl":                       "'/tmp/$(rm -rf ~)/q.jsonl'",
		"/tmp/it's/q.jsonl":                              `'/tmp/it'\''s/q.jsonl'`,
		"":                                               "''",
	} {
		if got := ShellQuote(in); got != want {
			t.Errorf("ShellQuote(%q) = %s, want %s", in, got, want)
		}
	}
}

func TestBuildSpillResponse_BudgetTOON(t *testing.T) {
	e := &DQLExecutor{}
	result, records := longContentResult(50, 100)
	opts := DQLExecuteOptions{AgentMode: true, MaxOutputBytes: 2500, Spill: SpillOptions{Mode: SpillNever}}

	resp, _, err := e.buildSpillResponse("fetch logs", result, records, "toon", opts)
	if err != nil {
		t.Fatal(err)
	}
	enc, ok := resp.Result.(*output.InlineRecordsEncoded)
	if !ok {
		t.Fatalf("result = %T", resp.Result)
	}
	if size := encodedSize(t, resp); size > 2500 {
		t.Errorf("envelope %d bytes over budget", size)
	}
	k := *resp.Context.Returned
	if k == 0 || k >= 50 || !strings.Contains(enc.Records, fmt.Sprintf("row-%03d", k-1)) || strings.Contains(enc.Records, fmt.Sprintf("row-%03d", k)) {
		t.Errorf("TOON payload does not hold exactly the first %d rows", k)
	}
}

func TestBuildSpillResponse_BudgetBelowEnvelopeOverhead(t *testing.T) {
	e := &DQLExecutor{}
	result, records := longContentResult(5, 100)
	opts := DQLExecuteOptions{AgentMode: true, MaxOutputBytes: 10, Spill: SpillOptions{Mode: SpillNever}}

	resp, _, err := e.buildSpillResponse("fetch logs", result, records, "json", opts)
	if err != nil {
		t.Fatal(err)
	}
	if n := len(envelopeRows(t, resp)); n != 0 || *resp.Context.Returned != 0 {
		t.Errorf("returned %d rows, want 0", n)
	}
	found := false
	for _, w := range resp.Context.Warnings {
		if strings.Contains(w, "smaller than the envelope") {
			found = true
		}
	}
	if !found {
		t.Errorf("warnings must say the budget cannot hold even one row: %v", resp.Context.Warnings)
	}
}

func TestE2E_QueryFieldCapWithJQ(t *testing.T) {
	_, records := longContentResult(3, 1000)
	e := mockGrail(t, records)

	out := runAndCapture(t, func() error {
		return e.ExecuteWithOptions("fetch logs", DQLExecuteOptions{
			OutputFormat:   "json",
			AgentMode:      true,
			JQFilter:       `[.records[] | select(.content | endswith("xxx"))]`,
			MaxFieldChars:  20,
			MaxOutputBytes: 1 << 20,
			Spill:          SpillOptions{Mode: SpillNever},
		})
	})

	var env struct {
		Result  []map[string]interface{} `json:"result"`
		Context output.ResponseContext   `json:"context"`
	}
	if err := json.Unmarshal([]byte(out), &env); err != nil {
		t.Fatalf("decode: %v\n%s", err, out)
	}
	if len(env.Result) != 3 {
		t.Fatalf("the filter must see full values; got %d rows", len(env.Result))
	}
	if c := env.Result[0]["content"].(string); !strings.HasSuffix(c, "…(+988 chars)") {
		t.Errorf("content = %q", c)
	}
	if !env.Context.Truncated || env.Context.MaxFieldChars != 20 {
		t.Errorf("context = %+v", env.Context)
	}
	found := false
	for _, w := range env.Context.Warnings {
		if strings.Contains(w, "--max-output-bytes") {
			found = true
		}
	}
	if !found {
		t.Errorf("an output budget that --jq cannot honor must warn: %v", env.Context.Warnings)
	}
}

// Under -o auto the field cap applies first: auto measures, chooses and encodes
// the clipped rows, so the long values never reach the envelope.
func TestBuildSpillResponse_FieldCapBeforeAutoFormat(t *testing.T) {
	e := &DQLExecutor{}
	result, records := longContentResult(5, 5000)
	opts := DQLExecuteOptions{
		AgentMode:     true,
		MaxFieldChars: 20,
		Spill:         SpillOptions{Mode: SpillAuto, Threshold: 10 * 1024, Dir: t.TempDir(), Format: "jsonl"},
	}

	resp, _, err := e.buildSpillResponse("fetch logs", result, records, "auto", opts)
	if err != nil {
		t.Fatal(err)
	}
	enc, ok := resp.Result.(*output.InlineRecordsEncoded)
	if !ok {
		t.Fatalf("result = %T, want the auto-encoded inline rows", resp.Result)
	}
	if enc.Encoding != "csv" || resp.Context.Format != "csv" {
		t.Errorf("encoding = %q, format = %q, want csv", enc.Encoding, resp.Context.Format)
	}
	if strings.Contains(enc.Records, strings.Repeat("x", 100)) || !strings.Contains(enc.Records, "…(+4988 chars)") {
		t.Errorf("auto encoded unclipped values: %.200s", enc.Records)
	}
	if resp.Context.Decided != "inline" || resp.Context.MeasuredBytes > 10*1024 {
		t.Errorf("auto must measure the clipped rows: %+v", resp.Context)
	}
	if !resp.Context.Truncated || !reflect.DeepEqual(resp.Context.TruncatedFields, []string{"content"}) {
		t.Errorf("clip not reported: %+v", resp.Context)
	}
}

func TestBuildSpillResponse_BudgetWithAutoFormat(t *testing.T) {
	e := &DQLExecutor{}
	result, records := longContentResult(50, 100)
	opts := DQLExecuteOptions{AgentMode: true, MaxOutputBytes: 2000, Spill: SpillOptions{Mode: SpillNever}}

	resp, _, err := e.buildSpillResponse("fetch logs", result, records, "auto", opts)
	if err != nil {
		t.Fatal(err)
	}
	if size := encodedSize(t, resp); size > 2000 {
		t.Errorf("envelope %d bytes over budget", size)
	}
	enc, ok := resp.Result.(*output.InlineRecordsEncoded)
	if !ok {
		t.Fatalf("result = %T", resp.Result)
	}
	k := *resp.Context.Returned
	if k < 2 || k >= 50 || enc.Encoding != resp.Context.Format {
		t.Errorf("returned %d, encoding %q, context.format %q", k, enc.Encoding, resp.Context.Format)
	}
	if !strings.Contains(enc.Records, fmt.Sprintf("row-%03d", k-1)) || strings.Contains(enc.Records, fmt.Sprintf("row-%03d", k)) {
		t.Errorf("auto payload does not hold exactly the first %d rows", k)
	}
}

// TestBuildSpillResponse_AutoAndFieldCapCompose runs `query` as an agent gets it
// with no -o and no bound flags: -o auto by default plus the default field cap.
// Both apply, each names its own opt-out only when it changed something, and
// the opt-outs together restore the native-JSON, unclipped rows exactly.
func TestBuildSpillResponse_AutoAndFieldCapCompose(t *testing.T) {
	defaults := func(dir string) DQLExecuteOptions {
		return DQLExecuteOptions{
			AgentMode:           true,
			OutputFormat:        output.FormatAuto,
			AutoFormatByDefault: true,
			MaxFieldChars:       output.DefaultAgentMaxFieldChars,
			Spill:               SpillOptions{Mode: SpillAuto, Threshold: 50 * 1024, Dir: dir, Format: "jsonl"},
		}
	}
	e := &DQLExecutor{}

	t.Run("long values are clipped, then auto-encoded", func(t *testing.T) {
		result, records := longContentResult(3, 2000)
		resp, _, err := e.buildSpillResponse("fetch logs", result, records, output.FormatAuto, defaults(t.TempDir()))
		if err != nil {
			t.Fatal(err)
		}
		enc, ok := resp.Result.(*output.InlineRecordsEncoded)
		if !ok || enc.Encoding != "csv" || resp.Context.Format != "csv" {
			t.Fatalf("result = %T, format %q; want csv", resp.Result, resp.Context.Format)
		}
		if !strings.Contains(enc.Records, "…(+1508 chars)") || strings.Contains(enc.Records, strings.Repeat("x", 600)) {
			t.Errorf("default cap not applied before auto: %.200s", enc.Records)
		}
		if !hasSuggestion(resp.Context.Suggestions, "-o json") || !hasSuggestion(resp.Context.Suggestions, "--max-field-chars 0") {
			t.Errorf("each default must name its opt-out: %v", resp.Context.Suggestions)
		}
	})

	t.Run("short values add only the auto hint", func(t *testing.T) {
		result, records := longContentResult(3, 10)
		resp, _, err := e.buildSpillResponse("fetch logs", result, records, output.FormatAuto, defaults(t.TempDir()))
		if err != nil {
			t.Fatal(err)
		}
		if resp.Context.Truncated || hasSuggestion(resp.Context.Suggestions, "--max-field-chars") {
			t.Errorf("nothing was clipped, yet the cap reports it: %+v", resp.Context)
		}
		if !reflect.DeepEqual(resp.Context.Suggestions, []string{output.AutoDefaultSuggestion("csv")}) {
			t.Errorf("suggestions = %v, want only the auto opt-out", resp.Context.Suggestions)
		}
	})

	t.Run("opt-outs restore native unclipped rows", func(t *testing.T) {
		result, records := longContentResult(3, 2000)
		opts := defaults(t.TempDir())
		opts.OutputFormat, opts.AutoFormatByDefault, opts.MaxFieldChars = "json", false, 0
		resp, _, err := e.buildSpillResponse("fetch logs", result, records, "json", opts)
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(envelopeRows(t, resp), records) {
			t.Error("-o json --max-field-chars 0 must return the rows unchanged")
		}
		if resp.Context.Format != "" || resp.Context.Truncated || len(resp.Context.Suggestions) != 0 {
			t.Errorf("opt-out envelope carries default markers: %+v", resp.Context)
		}
	})

	t.Run("a budget keeps the auto hint in step with the emitted format", func(t *testing.T) {
		result, records := longContentResult(50, 100)
		opts := defaults(t.TempDir())
		opts.MaxOutputBytes = 2000
		resp, _, err := e.buildSpillResponse("fetch logs", result, records, output.FormatAuto, opts)
		if err != nil {
			t.Fatal(err)
		}
		if size := encodedSize(t, resp); size > 2000 {
			t.Errorf("envelope %d bytes over budget", size)
		}
		hints := 0
		for _, s := range resp.Context.Suggestions {
			if strings.Contains(s, "agent default -o auto") {
				hints++
				if s != output.AutoDefaultSuggestion(resp.Context.Format) {
					t.Errorf("auto hint %q does not match context.format %q", s, resp.Context.Format)
				}
			}
		}
		if want := map[bool]int{true: 0, false: 1}[resp.Context.Format == "json"]; hints != want {
			t.Errorf("%d auto hints for format %q, want %d: %v", hints, resp.Context.Format, want, resp.Context.Suggestions)
		}
	})
}

// TestBuildSpillResponse_FieldCapAfterCompaction checks the field cap against
// the agent-mode compaction default: compaction compares the full values, and
// clipping then applies to constant as well as to records.
func TestBuildSpillResponse_FieldCapAfterCompaction(t *testing.T) {
	e := &DQLExecutor{}
	opts := func(dir string) DQLExecuteOptions {
		o := inlineOpts(true)
		o.MaxFieldChars = 500
		o.Spill.Dir = dir
		return o
	}
	shared := strings.Repeat("s", 2000)
	prefix := strings.Repeat("p", 600)

	t.Run("constant values are clipped", func(t *testing.T) {
		records := []map[string]interface{}{
			{"id": "a", "stack": shared},
			{"id": "b", "stack": shared},
		}
		resp, _, err := e.buildSpillResponse("fetch logs", &DQLQueryResponse{Records: records}, records, "json", opts(t.TempDir()))
		if err != nil {
			t.Fatal(err)
		}
		ir := resp.Result.(*output.InlineRecords)
		got, _ := ir.Constant["stack"].(string)
		if !strings.HasSuffix(got, "…(+1500 chars)") || utf8.RuneCountInString(got) > 500+len("…(+1500 chars)") {
			t.Errorf("constant.stack not clipped: %.80q", got)
		}
		if !reflect.DeepEqual(resp.Context.TruncatedFields, []string{"stack"}) || !resp.Context.Truncated {
			t.Errorf("context = %+v, want truncated_fields [stack]", resp.Context)
		}
		if records[0]["stack"] != shared {
			t.Error("the caller's rows must keep the full value")
		}
	})

	t.Run("values equal only after clipping are not hoisted", func(t *testing.T) {
		records := []map[string]interface{}{
			{"id": "a", "stack": prefix + "one"},
			{"id": "b", "stack": prefix + "two"},
		}
		resp, _, err := e.buildSpillResponse("fetch logs", &DQLQueryResponse{Records: records}, records, "json", opts(t.TempDir()))
		if err != nil {
			t.Fatal(err)
		}
		ir := resp.Result.(*output.InlineRecords)
		if _, hoisted := ir.Constant["stack"]; hoisted {
			t.Fatalf("distinct values collapsed into constant: %v", ir.Constant)
		}
		for i, row := range ir.Records {
			if s, _ := row["stack"].(string); !strings.HasSuffix(s, "…(+103 chars)") {
				t.Errorf("row %d stack = %.60q, want clipped", i, s)
			}
		}
	})

	t.Run("a budget cut keeps the clipped constant", func(t *testing.T) {
		_, base := longContentResult(40, 100)
		records := make([]map[string]interface{}, len(base))
		for i, r := range base {
			records[i] = map[string]interface{}{"host": r["host"], "content": r["content"], "stack": shared}
		}
		o := opts(t.TempDir())
		o.MaxOutputBytes = 3000
		resp, _, err := e.buildSpillResponse("fetch logs", &DQLQueryResponse{Records: records}, records, "json", o)
		if err != nil {
			t.Fatal(err)
		}
		ir := resp.Result.(*output.InlineRecords)
		if resp.Context.Returned == nil || *resp.Context.Returned >= len(records) || *resp.Context.Returned == 0 {
			t.Fatalf("returned = %v, want a non-empty budget cut", resp.Context.Returned)
		}
		if got, _ := ir.Constant["stack"].(string); !strings.HasSuffix(got, "…(+1500 chars)") {
			t.Errorf("budget-cut constant.stack = %.60q, want clipped", got)
		}
		if n := encodedSize(t, resp); int64(n) > o.MaxOutputBytes {
			t.Errorf("envelope is %d bytes, over the %d budget", n, o.MaxOutputBytes)
		}
	})

	t.Run("--max-field-chars 0 restores the full constant", func(t *testing.T) {
		records := []map[string]interface{}{
			{"id": "a", "stack": shared},
			{"id": "b", "stack": shared},
		}
		o := opts(t.TempDir())
		o.MaxFieldChars = 0
		resp, _, err := e.buildSpillResponse("fetch logs", &DQLQueryResponse{Records: records}, records, "json", o)
		if err != nil {
			t.Fatal(err)
		}
		if got := resp.Result.(*output.InlineRecords).Constant["stack"]; got != shared || resp.Context.Truncated {
			t.Errorf("opt-out: constant.stack clipped or context truncated: %+v", resp.Context)
		}
	})
}

// TestBuildSpillResponse_BudgetRechoiceUpdatesCompactHint checks the
// --compact=false hint against the format -o auto re-chooses for budget-cut
// rows. The full result is flat csv, where a partial null is kept, so the hint
// is absent; one kept row is yaml, which drops that null, so the hint is due.
func TestBuildSpillResponse_BudgetRechoiceUpdatesCompactHint(t *testing.T) {
	e := &DQLExecutor{}
	records := make([]map[string]interface{}, 40)
	for i := range records {
		records[i] = map[string]interface{}{"id": fmt.Sprintf("row-%03d", i), "msg": fmt.Sprintf("%03d ", i) + strings.Repeat("m", 200), "opt": fmt.Sprint(i)}
	}
	records[0]["opt"] = nil
	opts := inlineOpts(true)
	opts.AutoFormatByDefault = true
	opts.Spill.Dir = t.TempDir()

	full, _, err := e.buildSpillResponse("fetch logs", &DQLQueryResponse{Records: records}, records, "auto", opts)
	if err != nil {
		t.Fatal(err)
	}
	if full.Context.Format != "csv" || hasCompactSuggestion(full) {
		t.Fatalf("full result: format %q, suggestions %q; want csv without the compact hint", full.Context.Format, full.Context.Suggestions)
	}

	// The smallest budget, in 50-byte steps, that fits one row as yaml. (A
	// smaller one can fit that row as csv, the format the budget search
	// keeps, since yaml does not fit.)
	var resp output.Response
	for opts.MaxOutputBytes = 500; opts.MaxOutputBytes < int64(encodedSize(t, full)); opts.MaxOutputBytes += 50 {
		resp, _, err = e.buildSpillResponse("fetch logs", &DQLQueryResponse{Records: records}, records, "auto", opts)
		if err != nil {
			t.Fatal(err)
		}
		if resp.Context.Returned != nil && (*resp.Context.Returned > 1 || resp.Context.Format == "yaml") {
			break
		}
	}
	if resp.Context.Returned == nil || *resp.Context.Returned != 1 || resp.Context.Format != "yaml" {
		t.Fatalf("budget cut: returned %v, format %q; want 1 row as yaml", resp.Context.Returned, resp.Context.Format)
	}
	if !hasCompactSuggestion(resp) {
		t.Errorf("yaml dropped the row's null, yet no --compact=false hint: %q", resp.Context.Suggestions)
	}
}

// TestBuildSpillResponse_BudgetSearchAcrossAutoFormats checks that the budget
// finds the longest fitting prefix when -o auto would pick a different format
// for shorter prefixes. The first 20 rows are sparse, so a prefix of fewer
// than 36 rows is yaml, which repeats every long key on every row; longer
// prefixes are dense csv. A search that re-chooses per prefix sees the yaml
// midpoint overflow and settles on a short prefix, though a long csv one fits.
func TestBuildSpillResponse_BudgetSearchAcrossAutoFormats(t *testing.T) {
	e := &DQLExecutor{}
	const cols, sparse, total = 10, 20, 60
	records := make([]map[string]interface{}, total)
	for i := range records {
		row := map[string]interface{}{}
		for c := 0; c < cols; c++ {
			key := fmt.Sprintf("attribute.with.a.rather.long.name.%02d", c)
			if i < sparse && c > 0 {
				row[key] = nil
			} else {
				row[key] = fmt.Sprintf("v%d", i)
			}
		}
		records[i] = row
	}
	opts := inlineOpts(false)
	opts.AutoFormatByDefault = true
	opts.Spill.Dir = t.TempDir()

	full, _, err := e.buildSpillResponse("fetch logs", &DQLQueryResponse{Records: records}, records, "auto", opts)
	if err != nil {
		t.Fatal(err)
	}
	if full.Context.Format != "csv" {
		t.Fatalf("full result format = %q, want csv", full.Context.Format)
	}

	opts.MaxOutputBytes = int64(encodedSize(t, full)) - 150
	resp, _, err := e.buildSpillResponse("fetch logs", &DQLQueryResponse{Records: records}, records, "auto", opts)
	if err != nil {
		t.Fatal(err)
	}
	if resp.Context.Returned == nil {
		t.Fatal("the budget cut no rows; want a truncated result")
	}
	if got := *resp.Context.Returned; got < 40 {
		t.Errorf("returned %d rows, want the longest fitting csv prefix (at least 40 of %d)", got, total)
	}
	if n := encodedSize(t, resp); int64(n) > opts.MaxOutputBytes {
		t.Errorf("envelope is %d bytes, over the %d budget", n, opts.MaxOutputBytes)
	}
}
