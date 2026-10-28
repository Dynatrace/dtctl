package exec

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/Dynatrace/dtctl/pkg/client"
)

// mockGrail returns an httptest server that answers any query:execute with a
// SUCCEEDED result carrying the given records, so the full executor print path
// (ExecuteWithOptions → printResults → trySpill → writeEnvelope) runs end-to-end
// against it with no live credentials.
func mockGrail(t *testing.T, records []map[string]interface{}) *DQLExecutor {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		resp := DQLQueryResponse{
			State:  "SUCCEEDED",
			Result: &DQLResult{Records: records},
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	t.Cleanup(srv.Close)

	c, err := client.NewForTesting(srv.URL, "test-token")
	if err != nil {
		t.Fatalf("client: %v", err)
	}
	return NewDQLExecutor(c)
}

// runAndCapture executes fn (the spill path emits its envelope straight to
// os.Stdout) via the shared captureStdout helper and returns the captured text,
// failing the test if fn errored.
func runAndCapture(t *testing.T, fn func() error) string {
	t.Helper()
	var runErr error
	out := captureStdout(t, func() { runErr = fn() })
	if runErr != nil {
		t.Fatalf("execute: %v", runErr)
	}
	return string(out)
}

func manyRecords(n int) []map[string]interface{} {
	recs := make([]map[string]interface{}, n)
	for i := range recs {
		recs[i] = map[string]interface{}{
			"host":    "web-0" + string(rune('0'+i%10)),
			"status":  float64(200 + i%5),
			"content": rowMarker(i) + " log line padding to grow the serialised size beyond the threshold",
		}
	}
	return recs
}

// rowMarker is a per-row sentinel: the first few rows ride along as sample_rows
// (so their markers legitimately appear in stdout), but a late row's marker must
// only ever live in the spilled file, never in the returned summary.
func rowMarker(i int) string { return "row-" + strconv.Itoa(i) + "-marker" }

// TestE2E_QuerySpillsLargeResultToFile drives the real executor: a result above
// the threshold must spill to the managed cache and emit a result-file envelope
// on stdout, while the bulk of the rows never reach stdout.
func TestE2E_QuerySpillsLargeResultToFile(t *testing.T) {
	// Redirect the OS user cache dir into the sandbox so the default (managed,
	// context-partitioned) path is exercised hermetically.
	tmp := t.TempDir()
	t.Setenv("XDG_CACHE_HOME", tmp) // Linux
	t.Setenv("HOME", tmp)           // macOS uses $HOME/Library/Caches
	t.Setenv("LocalAppData", tmp)   // Windows: os.UserCacheDir reads %LocalAppData%

	e := mockGrail(t, manyRecords(50))

	out := runAndCapture(t, func() error {
		return e.ExecuteWithOptions("fetch logs", DQLExecuteOptions{
			OutputFormat: "json",
			AgentMode:    true,
			ContextName:  "prod",
			Spill:        SpillOptions{Mode: SpillAuto, Threshold: 200, Format: "json", TTL: 0},
		})
	})

	var env struct {
		OK              bool `json:"ok"`
		EnvelopeVersion int  `json:"envelope_version"`
		Result          struct {
			Kind string `json:"kind"`
			Path string `json:"path"`
			Rows int    `json:"rows"`
		} `json:"result"`
		Context struct {
			Decided string `json:"decided"`
		} `json:"context"`
	}
	if err := json.Unmarshal([]byte(out), &env); err != nil {
		t.Fatalf("stdout is not a single JSON envelope: %v\n%s", err, out)
	}
	if !env.OK || env.EnvelopeVersion == 0 {
		t.Errorf("ok=%v envelope_version=%d", env.OK, env.EnvelopeVersion)
	}
	if env.Result.Kind != "result-file" {
		t.Fatalf("kind = %q, want result-file\n%s", env.Result.Kind, out)
	}
	if env.Result.Rows != 50 || env.Context.Decided != "spilled" {
		t.Errorf("rows=%d decided=%q", env.Result.Rows, env.Context.Decided)
	}
	// The bulk of the rows must NOT reach stdout — a late row's marker lives only
	// in the spilled file (the leading rows ride along as sample_rows by design).
	if strings.Contains(out, rowMarker(49)) {
		t.Errorf("a non-sampled row leaked into stdout:\n%s", out)
	}
	// The spill landed in the managed cache, partitioned by context (D9).
	if filepath.Base(filepath.Dir(env.Result.Path)) != "prod" {
		t.Errorf("spill not partitioned by context: %q", env.Result.Path)
	}
	if !strings.HasPrefix(env.Result.Path, tmp) {
		t.Errorf("managed spill escaped the sandbox cache: %q (tmp=%q)", env.Result.Path, tmp)
	}
	// The spilled file holds the full result, including the row that never hit stdout.
	data, rerr := os.ReadFile(env.Result.Path)
	if rerr != nil {
		t.Fatalf("read spilled file: %v", rerr)
	}
	var back []map[string]interface{}
	if jerr := json.Unmarshal(data, &back); jerr != nil || len(back) != 50 {
		t.Fatalf("spilled file: len=%d err=%v", len(back), jerr)
	}
	if !strings.Contains(string(data), rowMarker(49)) {
		t.Errorf("spilled file is missing the full result (row 49 marker absent)")
	}
}

// TestE2E_QueryInlineRecordsEnvelope drives the real executor for a small result
// in agent mode: it stays inline but still carries result.kind:"records" so a
// consumer branches on result.kind uniformly (D2/D31).
func TestE2E_QueryInlineRecordsEnvelope(t *testing.T) {
	e := mockGrail(t, []map[string]interface{}{{"host": "web-01", "status": float64(200)}})

	out := runAndCapture(t, func() error {
		return e.ExecuteWithOptions("fetch logs", DQLExecuteOptions{
			OutputFormat: "json",
			AgentMode:    true,
			Spill:        SpillOptions{Mode: SpillAuto, Threshold: 1 << 20, Dir: t.TempDir(), Format: "json"},
		})
	})

	var env struct {
		OK     bool `json:"ok"`
		Result struct {
			Kind    string                   `json:"kind"`
			Records []map[string]interface{} `json:"records"`
		} `json:"result"`
		Context struct {
			Decided string `json:"decided"`
		} `json:"context"`
	}
	if err := json.Unmarshal([]byte(out), &env); err != nil {
		t.Fatalf("stdout is not a single JSON envelope: %v\n%s", err, out)
	}
	if env.Result.Kind != "records" {
		t.Fatalf("kind = %q, want records\n%s", env.Result.Kind, out)
	}
	if len(env.Result.Records) != 1 || env.Context.Decided != "inline" {
		t.Errorf("records=%d decided=%q", len(env.Result.Records), env.Context.Decided)
	}
}

// TestE2E_QueryNeverSpillStillEmitsEnvelopeInAgentMode is the regression guard
// for the rough edge where --spill=never in agent mode bypassed the spill-aware
// emitter entirely and reverted to a human table (printResults gated the path on
// Spill.Enabled(), which is false for never). In agent mode a never result —
// however large — must still come back as a structured kind:"records" envelope
// with every row inline, never a table.
func TestE2E_QueryNeverSpillStillEmitsEnvelopeInAgentMode(t *testing.T) {
	// A result far above any sane threshold, to prove "never" forces inline
	// regardless of size rather than silently degrading to a table.
	e := mockGrail(t, manyRecords(50))

	out := runAndCapture(t, func() error {
		return e.ExecuteWithOptions("fetch logs", DQLExecuteOptions{
			AgentMode: true,
			// No explicit -o: this is the exact invocation that used to emit a
			// table. OutputFormat is left at its zero value (the default path).
			Spill: SpillOptions{Mode: SpillNever, Threshold: 200, Format: "json", TTL: 0},
		})
	})

	var env struct {
		OK     bool `json:"ok"`
		Result struct {
			Kind    string                   `json:"kind"`
			Records []map[string]interface{} `json:"records"`
		} `json:"result"`
		Context struct {
			Decided string `json:"decided"`
		} `json:"context"`
	}
	if err := json.Unmarshal([]byte(out), &env); err != nil {
		t.Fatalf("--spill=never in agent mode did not emit a JSON envelope (regressed to a table?): %v\n%s", err, out)
	}
	if !env.OK || env.Result.Kind != "records" {
		t.Fatalf("kind = %q, want records (ok=%v)\n%s", env.Result.Kind, env.OK, out)
	}
	if len(env.Result.Records) != 50 || env.Context.Decided != "inline" {
		t.Errorf("records=%d decided=%q, want 50 rows inline", len(env.Result.Records), env.Context.Decided)
	}
}
