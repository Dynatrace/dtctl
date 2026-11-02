package inspect

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Dynatrace/dtctl/pkg/output"
)

// writeSpill writes records to a managed-style spill file in dir using the given
// format's printer, plus a sidecar, and returns the data path. It mirrors what
// the query spill path produces so the engine reads real artifacts.
func writeSpill(t *testing.T, dir, format string, records []map[string]interface{}, sc *output.SidecarManifest) string {
	t.Helper()
	ext := format
	if format == "jsonl" {
		ext = "jsonl"
	}
	path := filepath.Join(dir, "q-test."+ext)
	f, err := os.Create(path)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	p := output.NewPrinterWithOpts(output.PrinterOptions{Format: format, Writer: f})
	if err := p.PrintList(records); err != nil {
		t.Fatalf("print %s: %v", format, err)
	}
	_ = f.Close()
	if sc != nil {
		if err := output.WriteSidecar(path, sc); err != nil {
			t.Fatalf("sidecar: %v", err)
		}
	}
	return path
}

func sampleRecords() []map[string]interface{} {
	return []map[string]interface{}{
		{"host": "web-01", "status": float64(200), "ts": "2026-06-01T10:00:00Z"},
		{"host": "web-02", "status": float64(500), "ts": "2026-06-01T10:01:00Z"},
		{"host": "web-01", "status": float64(404), "ts": "2026-06-01T10:02:00Z"},
		{"host": "web-03", "status": float64(200), "ts": "2026-06-01T10:03:00Z"},
	}
}

func TestRun_HeadTailPage(t *testing.T) {
	dir := t.TempDir()
	for _, format := range []string{"jsonl", "json", "csv"} {
		t.Run(format, func(t *testing.T) {
			path := writeSpill(t, dir, format, sampleRecords(), &output.SidecarManifest{
				Format: format, Rows: 4, ContextName: "prod",
			})

			head, err := Run(Request{Path: path, Primitive: PrimHead, N: 2})
			if err != nil {
				t.Fatalf("head: %v", err)
			}
			if got := len(head.Records); got != 2 {
				t.Fatalf("head rows = %d, want 2", got)
			}
			if head.Records[0]["host"] != "web-01" {
				t.Errorf("head[0].host = %v, want web-01", head.Records[0]["host"])
			}

			tail, err := Run(Request{Path: path, Primitive: PrimTail, N: 1})
			if err != nil {
				t.Fatalf("tail: %v", err)
			}
			if len(tail.Records) != 1 || tail.Records[0]["host"] != "web-03" {
				t.Errorf("tail = %v, want last row web-03", tail.Records)
			}

			page, err := Run(Request{Path: path, Primitive: PrimPage, Offset: 1, Limit: 2})
			if err != nil {
				t.Fatalf("page: %v", err)
			}
			if len(page.Records) != 2 || page.Records[0]["host"] != "web-02" {
				t.Errorf("page = %v, want [web-02, web-01]", page.Records)
			}
		})
	}
}

func TestRun_FieldsProjection(t *testing.T) {
	dir := t.TempDir()
	path := writeSpill(t, dir, "jsonl", sampleRecords(), &output.SidecarManifest{
		Format: "jsonl", Rows: 4,
		Columns: []output.ColumnStats{{Name: "host"}, {Name: "status"}, {Name: "ts"}},
	})
	res, err := Run(Request{Path: path, Primitive: PrimHead, N: 1, Fields: []string{"host"}})
	if err != nil {
		t.Fatalf("head+fields: %v", err)
	}
	row := res.Records[0]
	if len(row) != 1 || row["host"] != "web-01" {
		t.Errorf("projected row = %v, want only host", row)
	}

	// Unknown field is rejected against the sidecar schema.
	_, err = Run(Request{Path: path, Primitive: PrimHead, Fields: []string{"nope"}})
	ie, ok := err.(*Error)
	if !ok || ie.Code != codeUnknownField {
		t.Fatalf("unknown field err = %v, want inspect_unknown_field", err)
	}
}

func TestRun_StatsAndSchema(t *testing.T) {
	dir := t.TempDir()
	path := writeSpill(t, dir, "jsonl", sampleRecords(), &output.SidecarManifest{Format: "jsonl", Rows: 4})

	stats, err := Run(Request{Path: path, Primitive: PrimStats})
	if err != nil {
		t.Fatalf("stats: %v", err)
	}
	if stats.Kind != output.KindFileSummary || stats.Summary == nil {
		t.Fatalf("stats kind = %q", stats.Kind)
	}
	if stats.Summary.Rows != 4 {
		t.Errorf("stats rows = %d, want 4", stats.Summary.Rows)
	}
	var host *output.ColumnStats
	for i := range stats.Summary.Columns {
		if stats.Summary.Columns[i].Name == "host" {
			host = &stats.Summary.Columns[i]
		}
	}
	if host == nil || host.Distinct == nil || *host.Distinct != 3 {
		t.Errorf("host distinct = %v, want 3", host)
	}

	schema, err := Run(Request{Path: path, Primitive: PrimSchema})
	if err != nil {
		t.Fatalf("schema: %v", err)
	}
	for _, c := range schema.Summary.Columns {
		if c.Distinct != nil || c.Top != nil {
			t.Errorf("schema col %q should be trimmed, got %+v", c.Name, c)
		}
	}
}

func TestRun_StatsSampledWarnsWithoutSidecar(t *testing.T) {
	dir := t.TempDir()
	path := writeSpill(t, dir, "jsonl", sampleRecords(), nil) // no sidecar
	res, err := Run(Request{Path: path, Primitive: PrimStats})
	if err != nil {
		t.Fatalf("stats: %v", err)
	}
	if len(res.Warnings) == 0 {
		t.Errorf("expected a sampling-unknown warning when no sidecar is present")
	}
}

func TestRun_WrongContextManagedPath(t *testing.T) {
	// Simulate a managed cache path: <cache>/dtctl/results/<ctx>/q-...
	cache, err := os.UserCacheDir()
	if err != nil {
		t.Skip("no user cache dir")
	}
	dir := filepath.Join(cache, "dtctl", "results", "other-ctx")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	path := writeSpill(t, dir, "jsonl", sampleRecords(), &output.SidecarManifest{Format: "jsonl", ContextName: "other-ctx"})
	t.Cleanup(func() { _ = os.Remove(path); _ = os.Remove(output.SidecarPathFor(path)) })

	_, err = Run(Request{Path: path, Primitive: PrimHead, ActiveContext: "prod"})
	ie, ok := err.(*Error)
	if !ok || ie.Code != output.ErrCodeSpillFileWrongContext {
		t.Fatalf("err = %v, want spill_file_wrong_context", err)
	}
}

func TestRun_NotFound(t *testing.T) {
	_, err := Run(Request{Path: filepath.Join(t.TempDir(), "q-gone.jsonl"), Primitive: PrimHead})
	ie, ok := err.(*Error)
	if !ok || ie.Code != output.ErrCodeSpillFileNotFound {
		t.Fatalf("err = %v, want spill_file_not_found", err)
	}
}

func TestRun_Parquet(t *testing.T) {
	dir := t.TempDir()
	recs := []map[string]interface{}{
		{"host": "web-01", "count": float64(10), "ts": "2026-06-01T10:00:00Z"},
		{"host": "web-02", "count": float64(20), "ts": "2026-06-01T10:01:00Z"},
	}
	path := filepath.Join(dir, "q-test.parquet")
	f, _ := os.Create(path)
	p := output.NewPrinterWithOpts(output.PrinterOptions{Format: "parquet", Writer: f, Types: []output.ColumnTypeMapping{
		{Name: "host", Type: "string"}, {Name: "count", Type: "long"}, {Name: "ts", Type: "timestamp"},
	}})
	if err := p.PrintList(recs); err != nil {
		t.Fatalf("write parquet: %v", err)
	}
	_ = f.Close()
	_ = output.WriteSidecar(path, &output.SidecarManifest{Format: "parquet", Rows: 2})

	res, err := Run(Request{Path: path, Primitive: PrimHead, N: 5})
	if err != nil {
		t.Fatalf("parquet head: %v", err)
	}
	if len(res.Records) != 2 {
		t.Fatalf("rows = %d, want 2", len(res.Records))
	}
	if res.Records[0]["count"] != int64(10) {
		t.Errorf("count = %#v, want int64(10)", res.Records[0]["count"])
	}
	// Timestamp round-trips to an RFC3339 string, not raw nanos.
	ts, _ := res.Records[0]["ts"].(string)
	if _, perr := time.Parse(time.RFC3339Nano, ts); perr != nil {
		t.Errorf("ts = %#v, want RFC3339 string", res.Records[0]["ts"])
	}
}
