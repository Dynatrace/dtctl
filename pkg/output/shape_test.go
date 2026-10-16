package output

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

type shapeTestModInfo struct {
	CreatedBy        string `json:"createdBy"`
	LastModifiedTime string `json:"lastModifiedTime"`
}

type shapeTestDoc struct {
	ID               string           `json:"id" table:"ID"`
	Name             string           `json:"name" table:"NAME"`
	Owner            string           `json:"owner" table:"OWNER"`
	ModificationInfo shapeTestModInfo `json:"modificationInfo" table:"-"`
}

func shapeTestDocs() []shapeTestDoc {
	return []shapeTestDoc{
		{ID: "d-1", Name: "Checkout", Owner: "u-1", ModificationInfo: shapeTestModInfo{CreatedBy: "u-1", LastModifiedTime: "2026-01-01T00:00:00Z"}},
		{ID: "d-2", Name: "Payments", Owner: "u-2", ModificationInfo: shapeTestModInfo{CreatedBy: "u-2", LastModifiedTime: "2026-01-02T00:00:00Z"}},
		{ID: "d-3", Name: "Search", Owner: "u-3", ModificationInfo: shapeTestModInfo{CreatedBy: "u-3", LastModifiedTime: "2026-01-03T00:00:00Z"}},
	}
}

func TestShapingPrinter_LimitKeepsTypedOutput(t *testing.T) {
	var want bytes.Buffer
	if err := (&JSONPrinter{writer: &want}).PrintList(shapeTestDocs()[:2]); err != nil {
		t.Fatal(err)
	}

	var got, notices bytes.Buffer
	p := NewShapingPrinter(&JSONPrinter{writer: &got}, ShapeOptions{Limit: 2, Notices: &notices})
	if err := p.PrintList(shapeTestDocs()); err != nil {
		t.Fatalf("PrintList: %v", err)
	}
	if got.String() != want.String() {
		t.Errorf("--limit must only truncate, not reshape.\ngot:\n%s\nwant:\n%s", got.String(), want.String())
	}
	if !strings.Contains(notices.String(), "2 of 3") {
		t.Errorf("expected a truncation notice naming 2 of 3, got %q", notices.String())
	}
}

func TestShapingPrinter_LimitAboveLengthIsSilent(t *testing.T) {
	var got, notices bytes.Buffer
	p := NewShapingPrinter(&JSONPrinter{writer: &got}, ShapeOptions{Limit: 10, Notices: &notices})
	if err := p.PrintList(shapeTestDocs()); err != nil {
		t.Fatal(err)
	}
	var items []map[string]any
	if err := json.Unmarshal(got.Bytes(), &items); err != nil {
		t.Fatal(err)
	}
	if len(items) != 3 {
		t.Errorf("got %d items, want 3", len(items))
	}
	if notices.Len() != 0 {
		t.Errorf("no notice expected when nothing was cut, got %q", notices.String())
	}
}

func TestShapingPrinter_NegativeLimitRejected(t *testing.T) {
	p := NewShapingPrinter(&JSONPrinter{writer: &bytes.Buffer{}}, ShapeOptions{Limit: -1})
	if err := p.PrintList(shapeTestDocs()); err == nil || !strings.Contains(err.Error(), "--limit") {
		t.Fatalf("expected a --limit error, got %v", err)
	}
}

func TestShapingPrinter_FieldsRejectedForChartFormats(t *testing.T) {
	p := NewShapingPrinter(&JSONPrinter{writer: &bytes.Buffer{}}, ShapeOptions{Fields: []string{"id"}, Format: "chart"})
	err := p.PrintList(shapeTestDocs())
	if err == nil || !strings.Contains(err.Error(), "-o chart") {
		t.Fatalf("expected --fields to be rejected for -o chart, got %v", err)
	}
}

func TestShapingPrinter_FieldsJSONKeepsRequestedOrderAndNesting(t *testing.T) {
	var got bytes.Buffer
	p := NewShapingPrinter(&JSONPrinter{writer: &got}, ShapeOptions{
		Fields: []string{"name", "modificationInfo.lastModifiedTime", "id"},
	})
	if err := p.PrintList(shapeTestDocs()[:1]); err != nil {
		t.Fatal(err)
	}
	want := `[
  {
    "name": "Checkout",
    "modificationInfo": {
      "lastModifiedTime": "2026-01-01T00:00:00Z"
    },
    "id": "d-1"
  }
]
`
	if got.String() != want {
		t.Errorf("got:\n%s\nwant:\n%s", got.String(), want)
	}
}

func TestShapingPrinter_FieldsYAMLKeepsRequestedOrder(t *testing.T) {
	var got bytes.Buffer
	p := NewShapingPrinter(&YAMLPrinter{writer: &got}, ShapeOptions{Fields: []string{"name", "id"}})
	if err := p.PrintList(shapeTestDocs()[:1]); err != nil {
		t.Fatal(err)
	}
	want := "- name: Checkout\n  id: d-1\n"
	if got.String() != want {
		t.Errorf("got:\n%q\nwant:\n%q", got.String(), want)
	}
}

func TestShapingPrinter_FieldsSingleObject(t *testing.T) {
	var got bytes.Buffer
	p := NewShapingPrinter(&JSONPrinter{writer: &got}, ShapeOptions{Fields: []string{"id", "owner"}})
	if err := p.Print(shapeTestDocs()[0]); err != nil {
		t.Fatal(err)
	}
	want := "{\n  \"id\": \"d-1\",\n  \"owner\": \"u-1\"\n}\n"
	if got.String() != want {
		t.Errorf("got:\n%s\nwant:\n%s", got.String(), want)
	}
}

func TestShapingPrinter_FieldsThenJQ(t *testing.T) {
	var got bytes.Buffer
	p := NewShapingPrinter(&JSONPrinter{writer: &got, jqFilter: "[.[].modificationInfo.lastModifiedTime]"}, ShapeOptions{
		Fields: []string{"modificationInfo.lastModifiedTime"},
		Limit:  2,
	})
	if err := p.PrintList(shapeTestDocs()); err != nil {
		t.Fatal(err)
	}
	var times []string
	if err := json.Unmarshal(got.Bytes(), &times); err != nil {
		t.Fatalf("unmarshal %q: %v", got.String(), err)
	}
	if len(times) != 2 || times[1] != "2026-01-02T00:00:00Z" {
		t.Errorf("jq over the projected list: got %v", times)
	}
}

func TestShapingPrinter_FieldsTOONIsTabularWithDottedColumns(t *testing.T) {
	var got bytes.Buffer
	p := NewShapingPrinter(&ToonPrinter{writer: &got}, ShapeOptions{
		Fields:  []string{"id", "name", "modificationInfo.lastModifiedTime"},
		Tabular: true,
	})
	if err := p.PrintList(shapeTestDocs()[:2]); err != nil {
		t.Fatal(err)
	}
	want := "[2]{id,name,modificationInfo.lastModifiedTime}:\n" +
		"  d-1,Checkout,\"2026-01-01T00:00:00Z\"\n" +
		"  d-2,Payments,\"2026-01-02T00:00:00Z\"\n"
	if got.String() != want {
		t.Errorf("got:\n%s\nwant:\n%s", got.String(), want)
	}
}

func TestShapingPrinter_TabularExpandsObjectField(t *testing.T) {
	var got bytes.Buffer
	p := NewShapingPrinter(&CSVPrinter{writer: &got}, ShapeOptions{
		Fields:  []string{"id", "modificationInfo"},
		Tabular: true,
	})
	if err := p.PrintList(shapeTestDocs()[:1]); err != nil {
		t.Fatal(err)
	}
	want := "id,modificationInfo.createdBy,modificationInfo.lastModifiedTime\n" +
		"d-1,u-1,2026-01-01T00:00:00Z\n"
	if got.String() != want {
		t.Errorf("got:\n%s\nwant:\n%s", got.String(), want)
	}
}

func TestShapingPrinter_FieldsTableHeadersInRequestedOrder(t *testing.T) {
	var got bytes.Buffer
	p := NewShapingPrinter(&TablePrinter{writer: &got}, ShapeOptions{
		Fields:  []string{"owner", "modificationInfo.lastModifiedTime", "id"},
		Tabular: true,
	})
	if err := p.PrintList(shapeTestDocs()[:2]); err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(got.String()), "\n")
	header := strings.Fields(lines[0])
	wantHeader := []string{"OWNER", "MODIFICATIONINFO.LASTMODIFIEDTIME", "ID"}
	if strings.Join(header, " ") != strings.Join(wantHeader, " ") {
		t.Errorf("header = %v, want %v", header, wantHeader)
	}
	if len(lines) != 3 || !strings.Contains(lines[2], "2026-01-02T00:00:00Z") {
		t.Errorf("unexpected rows:\n%s", got.String())
	}
}

func TestShapingPrinter_FieldsTableSingleObject(t *testing.T) {
	var got bytes.Buffer
	p := NewShapingPrinter(&TablePrinter{writer: &got}, ShapeOptions{Fields: []string{"name"}, Tabular: true})
	if err := p.Print(shapeTestDocs()[0]); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got.String(), "NAME") || !strings.Contains(got.String(), "Checkout") {
		t.Errorf("got:\n%s", got.String())
	}
}

func TestShapingPrinter_UnknownFieldWarns(t *testing.T) {
	var got, notices bytes.Buffer
	p := NewShapingPrinter(&JSONPrinter{writer: &got}, ShapeOptions{Fields: []string{"id", "nmae"}, Notices: &notices})
	if err := p.PrintList(shapeTestDocs()); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(notices.String(), `"nmae"`) || !strings.Contains(notices.String(), "name") {
		t.Errorf("expected a warning naming the unknown field and the available ones, got %q", notices.String())
	}
}

func TestShapingPrinter_KeyContainingDot(t *testing.T) {
	var got bytes.Buffer
	p := NewShapingPrinter(&JSONPrinter{writer: &got}, ShapeOptions{Fields: []string{"a.b"}})
	if err := p.PrintList([]map[string]any{{"a.b": 1, "c": 2}}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got.String(), `"a.b": 1`) {
		t.Errorf("a literal dotted key must win over path traversal, got:\n%s", got.String())
	}
}

func TestLookupPath_NestedKeyContainingDot(t *testing.T) {
	item := map[string]any{
		"a.b":   map[string]any{"c": 1},
		"x":     map[string]any{"y.z": map[string]any{"w": 2}},
		"p.q.r": map[string]any{"s": 3},
	}
	for path, want := range map[string]any{"a.b.c": 1, "x.y.z.w": 2, "p.q.r.s": 3} {
		got, ok := lookupPath(item, path)
		if !ok || got != want {
			t.Errorf("lookupPath(%q) = %v, %v; want %v", path, got, ok, want)
		}
	}
	if _, ok := lookupPath(item, "a.b.missing"); ok {
		t.Error("a path with no matching leaf must not resolve")
	}
}

func TestShapingPrinter_AgentTruncationSetsContext(t *testing.T) {
	var got bytes.Buffer
	ap := NewAgentPrinter(&got, nil)
	p := NewShapingPrinter(ap, ShapeOptions{Limit: 2, Fields: []string{"id"}})
	if err := p.PrintList(shapeTestDocs()); err != nil {
		t.Fatal(err)
	}
	var resp struct {
		Result  []map[string]any `json:"result"`
		Context ResponseContext  `json:"context"`
	}
	if err := json.Unmarshal(got.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal %q: %v", got.String(), err)
	}
	if len(resp.Result) != 2 || len(resp.Result[0]) != 1 {
		t.Errorf("result = %v, want 2 items with only id", resp.Result)
	}
	if resp.Context.Total == nil || *resp.Context.Total != 3 {
		t.Errorf("context.total = %v, want 3", resp.Context.Total)
	}
	if !resp.Context.HasMore {
		t.Error("context.has_more should be true")
	}
	if len(resp.Context.Suggestions) == 0 || !strings.Contains(resp.Context.Suggestions[0], "--limit") {
		t.Errorf("expected a --limit suggestion, got %v", resp.Context.Suggestions)
	}
}

func TestShapingPrinter_AgentKeepsLargerServerTotal(t *testing.T) {
	var got bytes.Buffer
	ap := NewAgentPrinter(&got, nil)
	ap.SetTotal(500) // the server reported more than was fetched
	p := NewShapingPrinter(ap, ShapeOptions{Limit: 2})
	if err := p.PrintList(shapeTestDocs()); err != nil {
		t.Fatal(err)
	}
	if *ap.Context().Total != 500 {
		t.Errorf("context.total = %d, want the server total 500", *ap.Context().Total)
	}
}

func TestShapingPrinter_AgentUnknownFieldIsEnvelopeWarning(t *testing.T) {
	var got, notices bytes.Buffer
	ap := NewAgentPrinter(&got, nil)
	p := NewShapingPrinter(ap, ShapeOptions{Fields: []string{"nope"}, Notices: &notices})
	if err := p.PrintList(shapeTestDocs()); err != nil {
		t.Fatal(err)
	}
	if notices.Len() != 0 {
		t.Errorf("agent mode must keep notices in the envelope, stderr got %q", notices.String())
	}
	if !strings.Contains(got.String(), `\"nope\"`) {
		t.Errorf("expected envelope warning for unknown field, got %s", got.String())
	}
}

func TestShapingPrinter_AgentTOONIsTabular(t *testing.T) {
	var got bytes.Buffer
	ap := NewAgentPrinter(&got, nil)
	ap.SetResultFormat("toon")
	p := NewShapingPrinter(ap, ShapeOptions{Fields: []string{"name", "id"}, Tabular: true})
	if err := p.PrintList(shapeTestDocs()[:2]); err != nil {
		t.Fatal(err)
	}
	var resp struct {
		Result string `json:"result"`
	}
	if err := json.Unmarshal(got.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if want := "[2]{name,id}:\n  Checkout,d-1\n  Payments,d-2"; resp.Result != want {
		t.Errorf("got %q, want %q", resp.Result, want)
	}
}

func TestShapingPrinter_UnwrapReturnsInner(t *testing.T) {
	ap := NewAgentPrinter(&bytes.Buffer{}, nil)
	p := NewShapingPrinter(ap, ShapeOptions{Limit: 1})
	u, ok := p.(interface{ Unwrap() Printer })
	if !ok || u.Unwrap() != Printer(ap) {
		t.Fatal("shaping printer must expose the wrapped printer")
	}
}

func TestParseFields(t *testing.T) {
	got := ParseFields(" id, name ,,id,modificationInfo.lastModifiedTime ")
	want := []string{"id", "name", "modificationInfo.lastModifiedTime"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("ParseFields = %v, want %v", got, want)
	}
	if ParseFields("") != nil {
		t.Error("empty input must parse to nil")
	}
}
