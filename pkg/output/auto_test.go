package output

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

func TestChooseAutoFormat(t *testing.T) {
	type named struct {
		ID    string `json:"id"`
		Count int    `json:"count"`
	}
	type withNested struct {
		ID   string            `json:"id"`
		Info map[string]string `json:"info"`
	}

	tests := []struct {
		name       string
		data       interface{}
		wantFormat string
		wantReason string
	}{
		{"nil", nil, "json", AutoReasonEmpty},
		{"empty records", []map[string]interface{}{}, "json", AutoReasonEmpty},
		{"empty generic list", []interface{}{}, "json", AutoReasonEmpty},
		{"scalar", 42.0, "json", AutoReasonScalar},
		{"string", "hello", "json", AutoReasonScalar},
		{"single object", map[string]interface{}{"a": 1.0, "b": "x"}, "yaml", AutoReasonSingleObject},
		{"single row", []map[string]interface{}{{"a": 1.0, "b": "x"}}, "yaml", AutoReasonSingleRow},
		{
			"uniform flat rows",
			[]map[string]interface{}{{"host": "a", "cpu": 1.5}, {"host": "b", "cpu": 2.0}},
			"csv", AutoReasonFlatRows,
		},
		{
			"flat rows with nulls stay tabular",
			[]map[string]interface{}{{"host": "a", "cpu": nil}, {"host": "b", "cpu": 2.0}},
			"csv", AutoReasonFlatRows,
		},
		{
			// 3 rows × 3 columns = 9 cells, 5 filled: density 0.56 ≥ 0.5.
			"heterogeneous but dense rows stay tabular",
			[]map[string]interface{}{{"a": "1", "b": "2"}, {"a": "1", "c": "3"}, {"a": "1"}},
			"csv", AutoReasonFlatRows,
		},
		{
			// 4 rows × 4 columns = 16 cells, 4 filled: density 0.25 < 0.5.
			"sparse rows",
			[]map[string]interface{}{{"a": "1"}, {"b": "2"}, {"c": "3"}, {"d": "4"}},
			"yaml", AutoReasonSparseRows,
		},
		{
			"nested map value",
			[]map[string]interface{}{{"id": "a", "info": map[string]interface{}{"k": "v"}}, {"id": "b"}},
			"yaml", AutoReasonNested,
		},
		{
			"nested array value",
			[]interface{}{
				map[string]interface{}{"id": "a", "values": []interface{}{1.0, 2.0}},
				map[string]interface{}{"id": "b", "values": []interface{}{3.0}},
			},
			"yaml", AutoReasonNested,
		},
		{"list of scalars", []interface{}{"a", "b", "c"}, "yaml", AutoReasonNotRows},
		{"flat structs", []named{{"a", 1}, {"b", 2}}, "csv", AutoReasonFlatRows},
		{"nested structs", []withNested{{"a", map[string]string{"k": "v"}}, {"b", nil}}, "yaml", AutoReasonNested},
		{"single struct", named{"a", 1}, "yaml", AutoReasonSingleObject},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ChooseAutoFormat(tt.data)
			if got.Format != tt.wantFormat || got.Reason != tt.wantReason {
				t.Errorf("ChooseAutoFormat() = {%s, %s}, want {%s, %s}", got.Format, got.Reason, tt.wantFormat, tt.wantReason)
			}
		})
	}
}

func TestIsAutoFormat(t *testing.T) {
	for _, f := range []string{"auto", "AUTO", " auto "} {
		if !IsAutoFormat(f) {
			t.Errorf("IsAutoFormat(%q) = false, want true", f)
		}
	}
	for _, f := range []string{"", "json", "toon", "autos"} {
		if IsAutoFormat(f) {
			t.Errorf("IsAutoFormat(%q) = true, want false", f)
		}
	}
}

func TestAutoPrinter_PrintsChosenFormatAndAnnouncesIt(t *testing.T) {
	tests := []struct {
		name       string
		data       interface{}
		wantOut    string
		wantNotice string
	}{
		{
			name:       "flat rows as csv",
			data:       []map[string]interface{}{{"host": "a", "cpu": 1.5}, {"host": "b", "cpu": 2.0}},
			wantOut:    "cpu,host\n1.5,a\n2,b\n",
			wantNotice: "-o auto: csv (uniform flat rows)\n",
		},
		{
			name:       "single object as yaml key-value lines",
			data:       map[string]interface{}{"name": "x", "count": 3.0},
			wantOut:    "count: 3\nname: x\n",
			wantNotice: "-o auto: yaml (single object)\n",
		},
		{
			name:       "empty list as json",
			data:       []interface{}{},
			wantOut:    "[]\n",
			wantNotice: "-o auto: json (empty result)\n",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var out, notice bytes.Buffer
			p := &AutoPrinter{writer: &out, notice: &notice}
			if err := p.PrintList(tt.data); err != nil {
				t.Fatalf("PrintList: %v", err)
			}
			if out.String() != tt.wantOut {
				t.Errorf("stdout = %q, want %q", out.String(), tt.wantOut)
			}
			if notice.String() != tt.wantNotice {
				t.Errorf("notice = %q, want %q", notice.String(), tt.wantNotice)
			}
		})
	}
}

func TestAutoPrinter_ChoosesAfterJQ(t *testing.T) {
	// The filter reduces a nested document to flat rows; the choice must be
	// made on what is printed, not on the input.
	data := map[string]interface{}{
		"records": []interface{}{
			map[string]interface{}{"id": "a", "n": 1.0},
			map[string]interface{}{"id": "b", "n": 2.0},
		},
	}
	var out, notice bytes.Buffer
	p := &AutoPrinter{writer: &out, notice: &notice, jqFilter: ".records"}
	if err := p.Print(data); err != nil {
		t.Fatalf("Print: %v", err)
	}
	if got, want := out.String(), "id,n\na,1\nb,2\n"; got != want {
		t.Errorf("stdout = %q, want %q", got, want)
	}
}

func TestAutoPrinter_StructsKeepJSONFieldNamesAndIntegers(t *testing.T) {
	type row struct {
		ID    string `json:"id"`
		Count int64  `json:"count"`
	}
	var out, notice bytes.Buffer
	p := &AutoPrinter{writer: &out, notice: &notice}
	if err := p.PrintList([]row{{"a", 12345678901}, {"b", 2}}); err != nil {
		t.Fatalf("PrintList: %v", err)
	}
	// Large integers must not come out in float exponent notation.
	if got, want := out.String(), "count,id\n12345678901,a\n2,b\n"; got != want {
		t.Errorf("stdout = %q, want %q", got, want)
	}
}

func TestNewPrinterWithOpts_Auto(t *testing.T) {
	p := NewPrinterWithOpts(PrinterOptions{Format: "auto", Writer: &bytes.Buffer{}})
	if _, ok := p.(*AutoPrinter); !ok {
		t.Fatalf("NewPrinterWithOpts(auto) = %T, want *AutoPrinter", p)
	}
}

func TestAgentPrinter_AutoReportsFormatInContext(t *testing.T) {
	tests := []struct {
		name       string
		data       interface{}
		wantFormat string
		wantResult interface{}
	}{
		{
			name:       "flat rows become a csv string",
			data:       []map[string]interface{}{{"host": "a", "cpu": 1.5}, {"host": "b", "cpu": 2.0}},
			wantFormat: "csv",
			wantResult: "cpu,host\n1.5,a\n2,b\n",
		},
		{
			name:       "nested rows become a yaml string",
			data:       []map[string]interface{}{{"id": "a", "tags": []interface{}{"x"}}, {"id": "b", "tags": []interface{}{}}},
			wantFormat: "yaml",
			wantResult: "- id: a\n  tags:\n    - x\n- id: b\n  tags: []\n",
		},
		{
			name:       "empty result stays native json",
			data:       []interface{}{},
			wantFormat: "json",
			wantResult: []interface{}{},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var buf bytes.Buffer
			ap := NewAgentPrinter(&buf, nil)
			ap.SetResultFormat("auto")
			if err := ap.PrintList(tt.data); err != nil {
				t.Fatalf("PrintList: %v", err)
			}
			var resp struct {
				OK      bool            `json:"ok"`
				Result  interface{}     `json:"result"`
				Context ResponseContext `json:"context"`
			}
			if err := json.Unmarshal(buf.Bytes(), &resp); err != nil {
				t.Fatalf("unmarshal envelope: %v\n%s", err, buf.String())
			}
			if resp.Context.Format != tt.wantFormat {
				t.Errorf("context.format = %q, want %q", resp.Context.Format, tt.wantFormat)
			}
			gotJSON, _ := json.Marshal(resp.Result)
			wantJSON, _ := json.Marshal(tt.wantResult)
			if string(gotJSON) != string(wantJSON) {
				t.Errorf("result = %s, want %s", gotJSON, wantJSON)
			}
			if len(resp.Context.Warnings) != 0 {
				t.Errorf("unexpected warnings: %v", resp.Context.Warnings)
			}
		})
	}
}

func TestAgentPrinter_ExplicitFormatsDoNotReportFormat(t *testing.T) {
	// context.format is the auto choice; explicit formats keep today's envelope
	// byte-for-byte.
	for _, f := range []string{"", "json", "toon"} {
		var buf bytes.Buffer
		ap := NewAgentPrinter(&buf, nil)
		ap.SetResultFormat(f)
		if err := ap.Print([]interface{}{map[string]interface{}{"a": 1.0}}); err != nil {
			t.Fatalf("Print: %v", err)
		}
		if strings.Contains(buf.String(), `"format"`) {
			t.Errorf("-o %q: envelope unexpectedly carries format: %s", f, buf.String())
		}
	}
}

func TestMeasureSerializedBytes_AutoMeasuresChosenEncoding(t *testing.T) {
	records := []map[string]interface{}{{"host": "a", "cpu": 1.5}, {"host": "b", "cpu": 2.0}}
	n, enc := MeasureSerializedBytes(records, "auto", JSONLayout{})
	if enc != "csv" {
		t.Fatalf("encoding = %q, want csv", enc)
	}
	if want := int64(len("cpu,host\n1.5,a\n2,b\n")); n != want {
		t.Errorf("measured = %d, want %d", n, want)
	}
}

func TestIsStructuredOutputFormat_Auto(t *testing.T) {
	// --jq output can be any JSON value; auto chooses after the filter runs, so
	// it must not be demoted to json.
	if got := NormalizeJQOutputFormat("auto"); got != "auto" {
		t.Errorf("NormalizeJQOutputFormat(auto) = %q, want auto", got)
	}
}

// TestAutoFormat_CaseAndWhitespaceInsensitive pins that every entry point
// recognises auto the way IsAutoFormat and `query` validation do, so
// `-o AUTO` or `-o ' auto '` never silently degrades to another format.
func TestAutoFormat_CaseAndWhitespaceInsensitive(t *testing.T) {
	rows := []map[string]interface{}{{"host": "a", "cpu": 1.5}, {"host": "b", "cpu": 2.0}}
	for _, f := range []string{"AUTO", " auto ", "Auto"} {
		t.Run(f, func(t *testing.T) {
			var buf bytes.Buffer
			ap := NewAgentPrinter(&buf, nil)
			ap.SetResultFormat(f)
			if err := ap.PrintList(rows); err != nil {
				t.Fatalf("PrintList: %v", err)
			}
			if ap.Context().Format != "csv" || len(ap.Context().Warnings) != 0 {
				t.Errorf("agent: format=%q warnings=%v, want csv and no warnings", ap.Context().Format, ap.Context().Warnings)
			}
			if _, ok := NewPrinterWithOpts(PrinterOptions{Format: f, Writer: &bytes.Buffer{}}).(*AutoPrinter); !ok {
				t.Errorf("NewPrinterWithOpts(%q) is not an AutoPrinter", f)
			}
			if !IsStructuredOutputFormat(f) {
				t.Errorf("IsStructuredOutputFormat(%q) = false; --jq would demote it to json", f)
			}
		})
	}
}

// TestAgentPrinter_AutoByDefault covers the agent-mode default (no -o given):
// the result is auto-encoded, and a single suggestion naming the opt-out
// appears only when the encoding actually differs from native JSON.
func TestAgentPrinter_AutoByDefault(t *testing.T) {
	flat := []map[string]interface{}{{"host": "a", "cpu": 1.5}, {"host": "b", "cpu": 2.0}}

	t.Run("changed encoding suggests -o json", func(t *testing.T) {
		ap := NewAgentPrinter(&bytes.Buffer{}, nil)
		ap.UseAutoByDefault()
		if err := ap.PrintList(flat); err != nil {
			t.Fatalf("PrintList: %v", err)
		}
		if ap.Context().Format != "csv" {
			t.Errorf("context.format = %q, want csv", ap.Context().Format)
		}
		want := []string{AutoDefaultSuggestion("csv")}
		if len(ap.Context().Suggestions) != 1 || ap.Context().Suggestions[0] != want[0] {
			t.Errorf("suggestions = %v, want %v", ap.Context().Suggestions, want)
		}
		if !strings.Contains(want[0], "-o json") {
			t.Errorf("suggestion %q must name the -o json opt-out", want[0])
		}
	})

	t.Run("native json choice adds no suggestion", func(t *testing.T) {
		ap := NewAgentPrinter(&bytes.Buffer{}, nil)
		ap.UseAutoByDefault()
		if err := ap.PrintList([]interface{}{}); err != nil {
			t.Fatalf("PrintList: %v", err)
		}
		if ap.Context().Format != "json" || len(ap.Context().Suggestions) != 0 {
			t.Errorf("format=%q suggestions=%v, want json and none", ap.Context().Format, ap.Context().Suggestions)
		}
	})

	t.Run("explicit -o auto adds no suggestion", func(t *testing.T) {
		ap := NewAgentPrinter(&bytes.Buffer{}, nil)
		ap.SetResultFormat("auto")
		if err := ap.PrintList(flat); err != nil {
			t.Fatalf("PrintList: %v", err)
		}
		if len(ap.Context().Suggestions) != 0 {
			t.Errorf("suggestions = %v, want none for an explicit -o auto", ap.Context().Suggestions)
		}
	})
}

func TestNewPrinterWithOpts_AutoNoticeGoesToTheNoticeWriter(t *testing.T) {
	var out, notice bytes.Buffer
	p := NewPrinterWithOpts(PrinterOptions{Format: "auto", Writer: &out, Notice: &notice})

	if err := p.PrintList([]map[string]interface{}{{"host": "a"}, {"host": "b"}}); err != nil {
		t.Fatalf("PrintList: %v", err)
	}
	if out.String() != "host\na\nb\n" {
		t.Errorf("stdout = %q", out.String())
	}
	if notice.String() != "-o auto: csv (uniform flat rows)\n" {
		t.Errorf("the format notice must reach PrinterOptions.Notice, got %q", notice.String())
	}
}
