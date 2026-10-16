package cmd

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/dynatrace-oss/dtctl/pkg/config"
	"github.com/dynatrace-oss/dtctl/pkg/inspect"
	"github.com/dynatrace-oss/dtctl/pkg/output"
)

// newInspectTestCmd builds a command carrying the inspect flag surface and parses
// argv, so buildInspectRequest can be exercised exactly as cobra would invoke it.
func newInspectTestCmd(t *testing.T, argv ...string) *cobra.Command {
	t.Helper()
	c := &cobra.Command{Use: "inspect", RunE: func(*cobra.Command, []string) error { return nil }}
	addInspectFlags(c)
	c.SetArgs(argv)
	if err := c.ParseFlags(argv); err != nil {
		t.Fatalf("parse %v: %v", argv, err)
	}
	return c
}

func TestBuildInspectRequest_Primitives(t *testing.T) {
	cases := []struct {
		name string
		argv []string
		want inspect.Primitive
		n    int
	}{
		{"head", []string{"--head", "20"}, inspect.PrimHead, 20},
		{"tail", []string{"--tail", "5"}, inspect.PrimTail, 5},
		{"sample", []string{"--sample", "3"}, inspect.PrimSample, 3},
		{"schema", []string{"--schema"}, inspect.PrimSchema, 0},
		{"bare-fields-defaults-to-head", []string{"--fields", "a,b"}, inspect.PrimHead, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := newInspectTestCmd(t, tc.argv...)
			req, err := buildInspectRequest(c, "f.jsonl")
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if req.Primitive != tc.want {
				t.Errorf("primitive = %q, want %q", req.Primitive, tc.want)
			}
			if tc.n != 0 && req.N != tc.n {
				t.Errorf("N = %d, want %d", req.N, tc.n)
			}
		})
	}
}

func TestBuildInspectRequest_Page(t *testing.T) {
	c := newInspectTestCmd(t, "--page", "--offset", "100", "--limit", "25")
	req, err := buildInspectRequest(c, "f.jsonl")
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if req.Primitive != inspect.PrimPage || req.Offset != 100 || req.Limit != 25 {
		t.Errorf("page req = %+v", req)
	}
}

func TestBuildInspectRequest_StatsColumns(t *testing.T) {
	all := newInspectTestCmd(t, "--stats")
	req, err := buildInspectRequest(all, "f.jsonl")
	if err != nil || req.Primitive != inspect.PrimStats || req.StatsColumns != nil {
		t.Fatalf("bare --stats req = %+v, err %v (want all columns)", req, err)
	}

	sel := newInspectTestCmd(t, "--stats=host,status")
	req, err = buildInspectRequest(sel, "f.jsonl")
	if err != nil || len(req.StatsColumns) != 2 || req.StatsColumns[0] != "host" {
		t.Fatalf("--stats=host,status req = %+v, err %v", req, err)
	}
}

func TestBuildInspectRequest_Errors(t *testing.T) {
	cases := []struct {
		name string
		argv []string
	}{
		{"two-primitives", []string{"--head", "2", "--tail", "2"}},
		{"offset-without-page", []string{"--offset", "5"}},
		{"limit-without-page", []string{"--limit", "5"}},
		{"nothing", []string{}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := newInspectTestCmd(t, tc.argv...)
			_, err := buildInspectRequest(c, "f.jsonl")
			ie, ok := err.(*inspect.Error)
			if !ok || ie.Code != "inspect_bad_flags" {
				t.Fatalf("err = %v, want inspect_bad_flags", err)
			}
		})
	}
}

// withJQ sets the package-level jqFilter (bound to the persistent --jq flag at
// runtime) for the duration of a test, since buildInspectRequest reads it there.
func withJQ(t *testing.T, filter string) {
	t.Helper()
	orig := jqFilter(context.Background())
	t.Cleanup(func() { gFlags.jqFilter = orig })
	gFlags.jqFilter = filter
}

func TestBuildInspectRequest_FilterModes(t *testing.T) {
	withJQ(t, "select(.status == 500)")

	t.Run("whole-file", func(t *testing.T) {
		c := newInspectTestCmd(t)
		req, err := buildInspectRequest(c, "f.jsonl")
		if err != nil {
			t.Fatalf("err: %v", err)
		}
		if req.Primitive != inspect.PrimFilter || req.Filter == "" {
			t.Errorf("req = %+v, want PrimFilter with a filter", req)
		}
	})

	t.Run("head-bound", func(t *testing.T) {
		c := newInspectTestCmd(t, "--head", "20")
		req, err := buildInspectRequest(c, "f.jsonl")
		if err != nil {
			t.Fatalf("err: %v", err)
		}
		if req.Primitive != inspect.PrimHead || req.N != 20 || req.Filter == "" {
			t.Errorf("req = %+v, want head-bounded filter", req)
		}
	})

	t.Run("page-bound", func(t *testing.T) {
		c := newInspectTestCmd(t, "--page", "--offset", "5", "--limit", "3")
		req, err := buildInspectRequest(c, "f.jsonl")
		if err != nil {
			t.Fatalf("err: %v", err)
		}
		if req.Primitive != inspect.PrimPage || req.Offset != 5 || req.Limit != 3 || req.Filter == "" {
			t.Errorf("req = %+v, want page-bounded filter", req)
		}
	})

	t.Run("fields-compose", func(t *testing.T) {
		c := newInspectTestCmd(t, "--fields", "host,ts")
		req, err := buildInspectRequest(c, "f.jsonl")
		if err != nil {
			t.Fatalf("err: %v", err)
		}
		if req.Primitive != inspect.PrimFilter || len(req.Fields) != 2 {
			t.Errorf("req = %+v, want filter with 2 fields", req)
		}
	})
}

func TestBuildInspectRequest_FilterErrors(t *testing.T) {
	withJQ(t, "select(.status == 500)")
	cases := []struct {
		name string
		argv []string
	}{
		{"with-schema", []string{"--schema"}},
		{"with-stats", []string{"--stats"}},
		{"with-sample", []string{"--sample", "5"}},
		{"two-windows", []string{"--head", "5", "--tail", "5"}},
		{"offset-without-page", []string{"--offset", "5"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := newInspectTestCmd(t, tc.argv...)
			_, err := buildInspectRequest(c, "f.jsonl")
			ie, ok := err.(*inspect.Error)
			if !ok || ie.Code != "inspect_bad_flags" {
				t.Fatalf("err = %v, want inspect_bad_flags", err)
			}
		})
	}
}

// writeRecords writes records to path using the jsonl printer (mirrors a spill).
func writeRecords(t *testing.T, path string, records []map[string]interface{}) {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	p := output.NewPrinterWithOpts(output.PrinterOptions{Format: "jsonl", Writer: f})
	if err := p.PrintList(records); err != nil {
		t.Fatal(err)
	}
	_ = f.Close()
}

func TestMaybeRespill_InlineBelowThreshold(t *testing.T) {
	orig := agentMode(context.Background())
	defer func() { gFlags.agentMode = orig }()
	gFlags.agentMode = true

	dir := t.TempDir()
	path := filepath.Join(dir, "q-x.jsonl")
	writeRecords(t, path, []map[string]interface{}{{"a": 1}, {"a": 2}})

	c := newInspectTestCmd(t, "--head", "2", "--spill=auto")
	res := &inspect.Result{Kind: output.KindRecords, Records: []map[string]interface{}{{"a": 1}, {"a": 2}}}
	resp, err := maybeRespill(c, &config.Config{}, inspect.Request{Path: path, Primitive: inspect.PrimHead}, res)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if resp != nil {
		t.Errorf("small result should stay inline, got a spill response")
	}
}

func TestMaybeRespill_SpillsAboveThreshold(t *testing.T) {
	orig := agentMode(context.Background())
	defer func() { gFlags.agentMode = orig }()
	gFlags.agentMode = true

	dir := t.TempDir()
	// Force a tiny threshold via --spill-threshold so a small set still spills.
	c := newInspectTestCmd(t, "--head", "100", "--spill=auto", "--spill-threshold", "10", "--spill-to", filepath.Join(dir, "out.jsonl"))

	records := []map[string]interface{}{
		{"host": "web-01", "status": float64(200)},
		{"host": "web-02", "status": float64(500)},
	}
	res := &inspect.Result{Kind: output.KindRecords, Records: records, Format: "jsonl"}
	resp, err := maybeRespill(c, &config.Config{}, inspect.Request{Path: "src.jsonl", Primitive: inspect.PrimHead, N: 100}, res)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if resp == nil {
		t.Fatal("expected a re-spill response above threshold")
	}
	m, ok := resp.Result.(*output.ResultFileManifest)
	if !ok || m.Kind != output.KindResultFile {
		t.Fatalf("result = %#v, want a result-file manifest", resp.Result)
	}
	if m.Rows != 2 {
		t.Errorf("rows = %d, want 2", m.Rows)
	}
	if _, statErr := os.Stat(filepath.Join(dir, "out.jsonl")); statErr != nil {
		t.Errorf("spill file not written: %v", statErr)
	}
	if resp.Context.Decided != "spilled" {
		t.Errorf("decided = %q, want spilled", resp.Context.Decided)
	}
}

// TestMaybeRespill_WarnsFilteredOutput confirms that when a --jq filter result
// re-spills, the envelope notes the file holds the FILTERED rows (the engine
// applied jq per record over the whole source file before the re-spill) rather
// than warning the filter was dropped — the jq output went to the file, not the
// inline path.
func TestMaybeRespill_WarnsFilteredOutput(t *testing.T) {
	origAgent, origJQ := agentMode(context.Background()), jqFilter(context.Background())
	defer func() { gFlags.agentMode, gFlags.jqFilter = origAgent, origJQ }()
	gFlags.agentMode = true
	gFlags.jqFilter = "select(.status == 500)"

	dir := t.TempDir()
	c := newInspectTestCmd(t, "--spill=auto", "--spill-threshold", "10", "--spill-to", filepath.Join(dir, "out.jsonl"))

	// The records here are the FILTERED output the engine already produced.
	records := []map[string]interface{}{
		{"host": "web-02", "status": float64(500)},
	}
	res := &inspect.Result{Kind: output.KindRecords, Records: records, Format: "jsonl"}
	req := inspect.Request{Path: "src.jsonl", Primitive: inspect.PrimFilter, Filter: "select(.status == 500)"}
	resp, err := maybeRespill(c, &config.Config{}, req, res)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if resp == nil {
		t.Fatal("expected a re-spill response")
	}
	var foundFiltered, foundDropped bool
	for _, w := range resp.Context.Warnings {
		if strings.Contains(w, "holds the rows matched by --jq") {
			foundFiltered = true
		}
		if strings.Contains(w, "--jq was not applied") {
			foundDropped = true
		}
	}
	if !foundFiltered {
		t.Errorf("expected a filtered-output note, got %v", resp.Context.Warnings)
	}
	if foundDropped {
		t.Errorf("must not warn the filter was dropped — the engine applied it: %v", resp.Context.Warnings)
	}
}
