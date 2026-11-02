package inspect

import (
	"strings"
	"testing"

	"github.com/Dynatrace/dtctl/pkg/output"
)

// hostsOf collects the "host" column from a row set for order-sensitive asserts.
func hostsOf(rows []map[string]interface{}) []string {
	out := make([]string, 0, len(rows))
	for _, r := range rows {
		if h, ok := r["host"].(string); ok {
			out = append(out, h)
		}
	}
	return out
}

func TestRunFilter_PredicateWholeFile(t *testing.T) {
	dir := t.TempDir()
	for _, format := range []string{"jsonl", "json", "csv"} {
		t.Run(format, func(t *testing.T) {
			path := writeSpill(t, dir, format, sampleRecords(), &output.SidecarManifest{
				Format: format, Rows: 4, ContextName: "prod",
			})

			// status is a number in jsonl/json but a string in csv; compare as a
			// string via tostring so one predicate works across formats.
			res, err := Run(Request{Path: path, Primitive: PrimFilter, Filter: `select((.status|tostring) == "200")`})
			if err != nil {
				t.Fatalf("filter: %v", err)
			}
			if res.Kind != output.KindRecords {
				t.Fatalf("kind = %q, want records", res.Kind)
			}
			if got := hostsOf(res.Records); strings.Join(got, ",") != "web-01,web-03" {
				t.Errorf("hosts = %v, want [web-01 web-03]", got)
			}
		})
	}
}

func TestRunFilter_NoMatchesIsEmpty(t *testing.T) {
	dir := t.TempDir()
	path := writeSpill(t, dir, "jsonl", sampleRecords(), nil)

	res, err := Run(Request{Path: path, Primitive: PrimFilter, Filter: `select(.status == 999)`})
	if err != nil {
		t.Fatalf("filter: %v", err)
	}
	if res.Records == nil {
		t.Fatalf("records should be non-nil empty slice, got nil")
	}
	if len(res.Records) != 0 {
		t.Errorf("want 0 rows, got %d", len(res.Records))
	}
}

func TestRunFilter_HeadBoundsMatches(t *testing.T) {
	dir := t.TempDir()
	path := writeSpill(t, dir, "jsonl", sampleRecords(), nil)

	// Two rows match; --head 1 keeps the first match in file order.
	res, err := Run(Request{Path: path, Primitive: PrimHead, N: 1, Filter: `select(.status == 200)`})
	if err != nil {
		t.Fatalf("filter: %v", err)
	}
	if got := hostsOf(res.Records); strings.Join(got, ",") != "web-01" {
		t.Errorf("hosts = %v, want [web-01]", got)
	}
}

func TestRunFilter_TailBoundsMatches(t *testing.T) {
	dir := t.TempDir()
	path := writeSpill(t, dir, "jsonl", sampleRecords(), nil)

	res, err := Run(Request{Path: path, Primitive: PrimTail, N: 1, Filter: `select(.status == 200)`})
	if err != nil {
		t.Fatalf("filter: %v", err)
	}
	if got := hostsOf(res.Records); strings.Join(got, ",") != "web-03" {
		t.Errorf("hosts = %v, want [web-03] (last match)", got)
	}
}

func TestRunFilter_PageBoundsMatches(t *testing.T) {
	dir := t.TempDir()
	path := writeSpill(t, dir, "jsonl", sampleRecords(), nil)

	// Both 200s match; offset 1, limit 1 → the second match.
	res, err := Run(Request{Path: path, Primitive: PrimPage, Offset: 1, Limit: 1, Filter: `select(.status == 200)`})
	if err != nil {
		t.Fatalf("filter: %v", err)
	}
	if got := hostsOf(res.Records); strings.Join(got, ",") != "web-03" {
		t.Errorf("hosts = %v, want [web-03]", got)
	}
}

func TestRunFilter_FieldsProjectMatchedRows(t *testing.T) {
	dir := t.TempDir()
	path := writeSpill(t, dir, "jsonl", sampleRecords(), nil)

	res, err := Run(Request{Path: path, Primitive: PrimFilter, Fields: []string{"host"}, Filter: `select(.status == 500)`})
	if err != nil {
		t.Fatalf("filter: %v", err)
	}
	if len(res.Records) != 1 {
		t.Fatalf("want 1 row, got %d", len(res.Records))
	}
	row := res.Records[0]
	if _, ok := row["status"]; ok {
		t.Errorf("status should be projected away, row = %v", row)
	}
	if row["host"] != "web-02" {
		t.Errorf("host = %v, want web-02", row["host"])
	}
}

func TestRunFilter_TransformToObject(t *testing.T) {
	dir := t.TempDir()
	path := writeSpill(t, dir, "jsonl", sampleRecords(), nil)

	// Reshaping to a new object is allowed (still record-shaped).
	res, err := Run(Request{Path: path, Primitive: PrimFilter, Filter: `select(.status == 500) | {h: .host}`})
	if err != nil {
		t.Fatalf("filter: %v", err)
	}
	if len(res.Records) != 1 || res.Records[0]["h"] != "web-02" {
		t.Errorf("records = %v, want [{h: web-02}]", res.Records)
	}
}

func TestRunFilter_NonObjectOutputIsError(t *testing.T) {
	dir := t.TempDir()
	path := writeSpill(t, dir, "jsonl", sampleRecords(), nil)

	// `.host` emits scalars, which the record-oriented pipeline cannot carry.
	_, err := Run(Request{Path: path, Primitive: PrimFilter, Filter: `.host`})
	ie, ok := err.(*Error)
	if !ok || ie.Code != codeBadFlags {
		t.Fatalf("err = %v, want inspect_bad_flags", err)
	}
	if !strings.Contains(ie.Message, "must emit objects") {
		t.Errorf("message = %q, want it to mention emitting objects", ie.Message)
	}
}

func TestRunFilter_InvalidProgramIsError(t *testing.T) {
	dir := t.TempDir()
	path := writeSpill(t, dir, "jsonl", sampleRecords(), nil)

	_, err := Run(Request{Path: path, Primitive: PrimFilter, Filter: `select(`})
	ie, ok := err.(*Error)
	if !ok || ie.Code != codeBadFlags {
		t.Fatalf("err = %v, want inspect_bad_flags", err)
	}
}

func TestRunFilter_MultipleEmitsPerRecord(t *testing.T) {
	dir := t.TempDir()
	records := []map[string]interface{}{
		{"items": []interface{}{map[string]interface{}{"host": "a"}, map[string]interface{}{"host": "b"}}},
	}
	path := writeSpill(t, dir, "jsonl", records, nil)

	// One source record fans out to two emitted objects.
	res, err := Run(Request{Path: path, Primitive: PrimFilter, Filter: `.items[]`})
	if err != nil {
		t.Fatalf("filter: %v", err)
	}
	if got := hostsOf(res.Records); strings.Join(got, ",") != "a,b" {
		t.Errorf("hosts = %v, want [a b]", got)
	}
}
