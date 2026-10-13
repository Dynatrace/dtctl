//go:build integration
// +build integration

package integration

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// A generated result keeps every row deterministic, so the streamed and buffered
// runs can be compared value by value without depending on tenant data. Two
// expands of a 40-element array give more rows than the switch point admits at a
// 1KB threshold, which is what makes the streaming path engage at all.
const streamTestArray = `array(1,2,3,4,5,6,7,8,9,10,11,12,13,14,15,16,17,18,19,20,21,22,23,24,25,26,27,28,29,30,31,32,33,34,35,36,37,38,39,40)`

const streamTestQuery = `data record(a = ` + streamTestArray + `)
| expand a
| fieldsAdd b = ` + streamTestArray + `
| expand b
| fieldsAdd pad = concat("row-", toString(a), "-", toString(b), "-padding-to-grow-the-serialised-size")`

// Grail returns at most 1000 records unless asked for more, and the tenant's
// default would cap the 1600 generated rows below the ~1024-row point at which
// an auto-format result (costed at one byte per row) can be known to exceed a
// 1KB threshold. The result would then be buffered, never streamed.
const streamTestMaxRecords = "10000"

// Live-tenant guard for the CLI half of the streaming path: against a real Grail
// response, `dtctl query -A` above the switch point must write every row to disk
// without holding the result, say so in the envelope, and land the same file the
// buffered path would have written.
func TestQueryAgentSpill_StreamsAgainstLiveTenant(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test")
	}
	if os.Getenv("DTCTL_INTEGRATION_ENV") == "" || os.Getenv("DTCTL_INTEGRATION_TOKEN") == "" {
		t.Skip("Skipping integration test: DTCTL_INTEGRATION_ENV / DTCTL_INTEGRATION_TOKEN not set")
	}

	exe := BuildDtctl(t)
	cfg := WriteCLIConfig(t, CLIConfig{
		Environment: os.Getenv("DTCTL_INTEGRATION_ENV"),
		Token:       os.Getenv("DTCTL_INTEGRATION_TOKEN"),
	})

	// jsonl is the streaming-capable spill format; json takes the buffered path,
	// which makes the format the only difference between the two runs.
	streamed := runQuerySpill(t, exe, cfg, "jsonl")
	buffered := runQuerySpill(t, exe, cfg, "json")

	if !streamed.Context.Streamed {
		t.Error("context.streamed is false; -A --spill=auto must stream above the switch point")
	}
	if buffered.Context.Streamed {
		t.Error("the buffered path must not claim it streamed")
	}
	// Nothing was serialised for inline emission on the streamed path, so there
	// is no measurement to report.
	if streamed.Context.MeasuredBytes != nil {
		t.Errorf("context.measured_bytes = %d on the streamed path, want absent", *streamed.Context.MeasuredBytes)
	}
	if buffered.Context.MeasuredBytes == nil {
		t.Error("context.measured_bytes is absent on the buffered path; that is where it is still measured")
	}

	if streamed.Result.Kind != "result-file" || buffered.Result.Kind != "result-file" {
		t.Fatalf("kind: streamed %q, buffered %q, want result-file for both", streamed.Result.Kind, buffered.Result.Kind)
	}
	if streamed.Result.Rows != buffered.Result.Rows {
		t.Errorf("rows: streamed %d, buffered %d", streamed.Result.Rows, buffered.Result.Rows)
	}
	if streamed.Context.Decided != buffered.Context.Decided {
		t.Errorf("decided: streamed %q, buffered %q", streamed.Context.Decided, buffered.Context.Decided)
	}
	if streamed.Context.Total != buffered.Context.Total {
		t.Errorf("total: streamed %d, buffered %d", streamed.Context.Total, buffered.Context.Total)
	}

	streamedRows := readSpilledJSONL(t, streamed.Result.Path)
	bufferedRows := readSpilledJSON(t, buffered.Result.Path)
	if len(streamedRows) != streamed.Result.Rows {
		t.Errorf("spill file holds %d rows, envelope reports %d", len(streamedRows), streamed.Result.Rows)
	}
	if !reflect.DeepEqual(streamedRows, bufferedRows) {
		t.Errorf("spilled rows differ between the streamed and buffered paths (%d vs %d rows)",
			len(streamedRows), len(bufferedRows))
	}
}

type spillEnvelope struct {
	OK     bool `json:"ok"`
	Result struct {
		Kind string `json:"kind"`
		Path string `json:"path"`
		Rows int    `json:"rows"`
	} `json:"result"`
	Context struct {
		Total         int    `json:"total"`
		Decided       string `json:"decided"`
		Streamed      bool   `json:"streamed"`
		MeasuredBytes *int64 `json:"measured_bytes"`
	} `json:"context"`
}

func runQuerySpill(t *testing.T, exe, cfg, format string) spillEnvelope {
	t.Helper()
	// --spill carries a NoOptDefVal, so the value has to be attached with "=".
	code, stdout, stderr := RunDtctl(t, exe, cfg, map[string]string{"DTCTL_SPILL_DIR": t.TempDir()},
		"query", streamTestQuery, "--agent", "--spill=auto", "--spill-format", format, "--spill-threshold", "1KB",
		"--max-result-records", streamTestMaxRecords)
	if code != 0 {
		t.Fatalf("dtctl query (spill-format %s) exited %d\nstdout: %s\nstderr: %s", format, code, stdout, stderr)
	}

	var env spillEnvelope
	if err := json.Unmarshal([]byte(strings.TrimSpace(stdout)), &env); err != nil {
		t.Fatalf("stdout is not a single envelope (spill-format %s): %v\n%s", format, err, stdout)
	}
	if !env.OK {
		t.Fatalf("query failed (spill-format %s): %s", format, stdout)
	}
	return env
}

func readSpilledJSONL(t *testing.T, path string) []map[string]interface{} {
	t.Helper()
	f := openSpillFile(t, path)
	defer func() { _ = f.Close() }()

	var rows []map[string]interface{}
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
	for sc.Scan() {
		var row map[string]interface{}
		if err := json.Unmarshal(sc.Bytes(), &row); err != nil {
			t.Fatalf("spill line is not JSON: %v (%s)", err, sc.Text())
		}
		rows = append(rows, row)
	}
	if err := sc.Err(); err != nil {
		t.Fatalf("scan spill file: %v", err)
	}
	return rows
}

func readSpilledJSON(t *testing.T, path string) []map[string]interface{} {
	t.Helper()
	f := openSpillFile(t, path)
	defer func() { _ = f.Close() }()

	var rows []map[string]interface{}
	if err := json.NewDecoder(f).Decode(&rows); err != nil {
		t.Fatalf("decode spill file %s: %v", path, err)
	}
	return rows
}

func openSpillFile(t *testing.T, path string) *os.File {
	t.Helper()
	if !filepath.IsAbs(path) {
		t.Fatalf("envelope path %q is not absolute", path)
	}
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("open spill file: %v", err)
	}
	return f
}
