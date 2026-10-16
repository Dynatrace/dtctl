package exec

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/dynatrace-oss/dtctl/pkg/output"
)

// TestBuildSpillResponse_InlineAuto pins the agent-mode `-o auto` contract for
// an inline query result: the rows stay inside the kind:"records" envelope,
// encoded in the chosen format, and context.format names that format.
func TestBuildSpillResponse_InlineAuto(t *testing.T) {
	flat := []map[string]interface{}{
		{"host": "web-01", "status": float64(200)},
		{"host": "web-02", "status": float64(500)},
	}
	nested := []map[string]interface{}{
		{"host": "web-01", "tags": []interface{}{"a"}},
		{"host": "web-02", "tags": []interface{}{"b"}},
	}
	cases := []struct {
		name        string
		records     []map[string]interface{}
		wantFormat  string
		wantRecords string // "" means native JSON rows
	}{
		{"flat rows", flat, "csv", "host,status\nweb-01,200\nweb-02,500\n"},
		{"nested rows", nested, "yaml", "- host: web-01\n  tags:\n    - a\n- host: web-02\n  tags:\n    - b\n"},
		{"empty result", []map[string]interface{}{}, "json", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			e := &DQLExecutor{}
			result := &DQLQueryResponse{Records: c.records}
			opts := DQLExecuteOptions{
				AgentMode:    true,
				OutputFormat: "auto",
				Spill:        SpillOptions{Mode: SpillAuto, Threshold: 1 << 20, Dir: t.TempDir(), Format: "json"},
			}
			resp, handled, err := e.buildSpillResponse("fetch logs", result, c.records, "auto", opts)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if !handled {
				t.Fatal("agent mode + -o auto should emit an envelope, not fall through")
			}
			if resp.Context == nil || resp.Context.Format != c.wantFormat {
				t.Fatalf("context.format = %+v, want %q", resp.Context, c.wantFormat)
			}
			if resp.Context.MeasuredEncoding != c.wantFormat {
				t.Errorf("measured_encoding = %q, want %q", resp.Context.MeasuredEncoding, c.wantFormat)
			}
			if c.wantRecords == "" {
				rec, ok := resp.Result.(*output.InlineRecords)
				if !ok {
					t.Fatalf("result = %T, want *InlineRecords", resp.Result)
				}
				if rec.Kind != output.KindRecords {
					t.Errorf("kind = %q, want %q", rec.Kind, output.KindRecords)
				}
				return
			}
			enc, ok := resp.Result.(*output.InlineRecordsEncoded)
			if !ok {
				t.Fatalf("result = %T, want *InlineRecordsEncoded", resp.Result)
			}
			if enc.Kind != output.KindRecords || enc.Encoding != c.wantFormat {
				t.Errorf("kind/encoding = %q/%q, want %q/%q", enc.Kind, enc.Encoding, output.KindRecords, c.wantFormat)
			}
			if enc.Records != c.wantRecords {
				t.Errorf("records = %q, want %q", enc.Records, c.wantRecords)
			}
		})
	}
}

// TestBuildSpillResponse_ExplicitFormatOmitsContextFormat keeps the existing
// envelopes byte-for-byte: context.format belongs to -o auto only.
func TestBuildSpillResponse_ExplicitFormatOmitsContextFormat(t *testing.T) {
	for _, f := range []string{"json", "toon"} {
		e := &DQLExecutor{}
		result, records := sampleResult(false)
		opts := DQLExecuteOptions{
			AgentMode:    true,
			OutputFormat: f,
			Spill:        SpillOptions{Mode: SpillAuto, Threshold: 1 << 20, Dir: t.TempDir(), Format: "json"},
		}
		resp, _, err := e.buildSpillResponse("fetch logs", result, records, f, opts)
		if err != nil {
			t.Fatalf("-o %s: unexpected error: %v", f, err)
		}
		if resp.Context.Format != "" {
			t.Errorf("-o %s: context.format = %q, want empty", f, resp.Context.Format)
		}
	}
}

// TestPrintResults_AutoOutsideAgentMode checks that a human `-o auto` query
// prints exactly what the chosen explicit format would print.
func TestPrintResults_AutoOutsideAgentMode(t *testing.T) {
	cases := []struct {
		name    string
		records []map[string]interface{}
		want    string
	}{
		{
			"flat rows print as csv",
			[]map[string]interface{}{{"host": "a", "count": float64(1)}, {"host": "b", "count": float64(2)}},
			"count,host\n1,a\n2,b\n",
		},
		{
			"single row prints as yaml",
			[]map[string]interface{}{{"host": "a", "count": float64(1)}},
			"records:\n  - count: 1\n    host: a\n",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			e := &DQLExecutor{}
			result := &DQLQueryResponse{Records: c.records}
			out := captureStdout(t, func() {
				if err := e.printResults("fetch logs", result, DQLExecuteOptions{OutputFormat: "auto"}); err != nil {
					t.Fatalf("printResults: %v", err)
				}
			})
			if got := string(out); got != c.want {
				t.Errorf("stdout = %q, want %q", got, c.want)
			}
			if strings.Contains(string(out), "-o auto") {
				t.Error("the auto notice belongs on stderr, not stdout")
			}
		})
	}
}

// TestPrintResults_AgentAutoWithJQChoosesOnFilterOutput pins that with --jq the
// encoding is chosen from what the filter emits, not from the unfiltered rows:
// flat rows (csv on their own) filtered down to a number come back as json.
func TestPrintResults_AgentAutoWithJQChoosesOnFilterOutput(t *testing.T) {
	records := []map[string]interface{}{{"host": "a", "count": float64(1)}, {"host": "b", "count": float64(2)}}
	e := &DQLExecutor{}
	out := captureStdout(t, func() {
		err := e.printResults("fetch logs", &DQLQueryResponse{Records: records}, DQLExecuteOptions{
			AgentMode:    true,
			OutputFormat: "auto",
			JQFilter:     ".records | length",
			Spill:        SpillOptions{Mode: SpillAuto, Threshold: 1 << 20, Dir: t.TempDir(), Format: "json"},
		})
		if err != nil {
			t.Fatalf("printResults: %v", err)
		}
	})
	var resp struct {
		Result  interface{}            `json:"result"`
		Context output.ResponseContext `json:"context"`
	}
	if err := json.Unmarshal(out, &resp); err != nil {
		t.Fatalf("unmarshal: %v\n%s", err, out)
	}
	if resp.Context.Format != "json" || resp.Result != float64(2) {
		t.Errorf("format=%q result=%v, want json and 2", resp.Context.Format, resp.Result)
	}
}

// TestBuildSpillResponse_AutoByDefault covers the agent-mode default for
// `query`: with no -o, the inline envelope is auto-encoded, and the opt-out
// suggestion appears only when the rows are not native JSON.
func TestBuildSpillResponse_AutoByDefault(t *testing.T) {
	flat := []map[string]interface{}{{"host": "a", "count": float64(1)}, {"host": "b", "count": float64(2)}}
	cases := []struct {
		name           string
		records        []map[string]interface{}
		wantSuggestion bool
	}{
		{"csv choice suggests the opt-out", flat, true},
		{"json choice stays quiet", []map[string]interface{}{}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			e := &DQLExecutor{}
			opts := DQLExecuteOptions{
				AgentMode:           true,
				OutputFormat:        output.FormatAuto,
				AutoFormatByDefault: true,
				Spill:               SpillOptions{Mode: SpillAuto, Threshold: 1 << 20, Dir: t.TempDir(), Format: "json"},
			}
			resp, handled, err := e.buildSpillResponse("fetch logs | limit 2", &DQLQueryResponse{Records: c.records}, c.records, output.FormatAuto, opts)
			if err != nil || !handled {
				t.Fatalf("handled=%v err=%v", handled, err)
			}
			got := false
			for _, s := range resp.Context.Suggestions {
				if s == output.AutoDefaultSuggestion(resp.Context.Format) {
					got = true
				}
			}
			if got != c.wantSuggestion {
				t.Errorf("opt-out suggestion present = %v, want %v (suggestions %v)", got, c.wantSuggestion, resp.Context.Suggestions)
			}
		})
	}
}

// TestPrintResults_AgentJQWithoutFormatIsJSON pins JSON output for --jq with no -o.
func TestPrintResults_AgentJQWithoutFormatIsJSON(t *testing.T) {
	records := []map[string]interface{}{{"host": "a", "count": float64(1)}, {"host": "b", "count": float64(2)}}
	e := &DQLExecutor{}
	out := captureStdout(t, func() {
		err := e.printResults("fetch logs", &DQLQueryResponse{Records: records}, DQLExecuteOptions{
			AgentMode:           true,
			OutputFormat:        "auto",
			AutoFormatByDefault: true,
			JQFilter:            ".records",
			Spill:               SpillOptions{Mode: SpillAuto, Threshold: 1 << 20, Dir: t.TempDir(), Format: "json"},
		})
		if err != nil {
			t.Fatalf("printResults: %v", err)
		}
	})
	var resp struct {
		Result  []map[string]interface{} `json:"result"`
		Context output.ResponseContext   `json:"context"`
	}
	if err := json.Unmarshal(out, &resp); err != nil {
		t.Fatalf("result is not JSON rows: %v\n%s", err, out)
	}
	if (resp.Context.Format != "" && resp.Context.Format != "json") || len(resp.Result) != 2 {
		t.Errorf("format=%q rows=%d, want JSON and 2", resp.Context.Format, len(resp.Result))
	}
}
