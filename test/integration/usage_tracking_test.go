//go:build integration
// +build integration

package integration

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

type usageTrackingRow struct {
	Context        string `json:"context"`
	Period         string `json:"period"`
	Queries        int64  `json:"queries"`
	Failed         int64  `json:"failed"`
	ScannedBytes   int64  `json:"scannedBytes"`
	ScannedRecords int64  `json:"scannedRecords"`
	Tracking       bool   `json:"tracking"`
}

// Live-tenant guard for the opt-in usage ledger: real DQL executions from
// several dtctl processes at once, a failed one included, all land in the
// tally, and the ledger holds counters only.
func TestUsageTracking_CountsLiveQueries(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test")
	}
	if os.Getenv("DTCTL_INTEGRATION_ENV") == "" || os.Getenv("DTCTL_INTEGRATION_TOKEN") == "" {
		t.Skip("Skipping integration test: DTCTL_INTEGRATION_ENV / DTCTL_INTEGRATION_TOKEN not set")
	}

	exe := BuildDtctl(t)
	state := filepath.Join(t.TempDir(), "state")
	env := map[string]string{"XDG_STATE_HOME": state}
	cfg := WriteCLIConfig(t, CLIConfig{
		Environment:   os.Getenv("DTCTL_INTEGRATION_ENV"),
		Token:         os.Getenv("DTCTL_INTEGRATION_TOKEN"),
		UsageTracking: true,
	})

	const procs, perProc = 4, 2
	var wg sync.WaitGroup
	errs := make(chan string, procs*perProc)
	for p := 0; p < procs; p++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < perProc; i++ {
				code, _, stderr := RunDtctl(t, exe, cfg, env, "query", "fetch logs, from:-10m | limit 1", "-o", "json", "--plain")
				if code != 0 {
					errs <- stderr
				}
			}
		}()
	}
	wg.Wait()
	close(errs)
	for e := range errs {
		t.Fatalf("query failed: %s", e)
	}

	// A query the tenant rejects still counts, as a failure.
	if code, _, _ := RunDtctl(t, exe, cfg, env, "query", "fetch logs | fieldsAdd x = noSuchFunction()", "--plain"); code == 0 {
		t.Fatal("the invalid query unexpectedly succeeded")
	}

	code, out, stderr := RunDtctl(t, exe, cfg, env, "usage", "-o", "json", "--plain")
	if code != 0 {
		t.Fatalf("dtctl usage exited %d: %s", code, stderr)
	}
	var rows []usageTrackingRow
	if err := json.Unmarshal([]byte(out), &rows); err != nil {
		t.Fatalf("decode usage output: %v\n%s", err, out)
	}
	if len(rows) != 3 {
		t.Fatalf("got %d rows, want today/7d/30d: %s", len(rows), out)
	}
	today := rows[0]
	if today.Period != "today" || today.Context != "cli" || !today.Tracking {
		t.Fatalf("unexpected first row: %+v", today)
	}
	t.Logf("today: %d queries, %d failed, %d bytes, %d records scanned",
		today.Queries, today.Failed, today.ScannedBytes, today.ScannedRecords)
	if want := int64(procs*perProc + 1); today.Queries != want {
		t.Errorf("queries = %d, want %d (concurrent writers must not lose updates)", today.Queries, want)
	}
	if today.Failed != 1 {
		t.Errorf("failed = %d, want 1", today.Failed)
	}
	if today.ScannedBytes < 0 || today.ScannedRecords < 0 {
		t.Errorf("negative scan totals: %+v", today)
	}

	raw, err := os.ReadFile(filepath.Join(state, "dtctl", "usage.json"))
	if err != nil {
		t.Fatalf("read ledger: %v", err)
	}
	for _, leaked := range []string{"fetch logs", os.Getenv("DTCTL_INTEGRATION_TOKEN"), os.Getenv("DTCTL_INTEGRATION_ENV")} {
		if strings.Contains(string(raw), leaked) {
			t.Error("the ledger holds query text, the token or the environment URL")
		}
	}

	if code, _, stderr := RunDtctl(t, exe, cfg, env, "usage", "reset", "--yes"); code != 0 {
		t.Fatalf("usage reset exited %d: %s", code, stderr)
	}
	_, out, _ = RunDtctl(t, exe, cfg, env, "usage", "-o", "json", "--plain")
	rows = nil
	if err := json.Unmarshal([]byte(out), &rows); err != nil || len(rows) == 0 || rows[0].Queries != 0 {
		t.Errorf("after reset: %s (err=%v)", out, err)
	}
}

// With usage tracking off (the default), a live query leaves no trace on disk.
func TestUsageTracking_OffWritesNothing(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test")
	}
	if os.Getenv("DTCTL_INTEGRATION_ENV") == "" || os.Getenv("DTCTL_INTEGRATION_TOKEN") == "" {
		t.Skip("Skipping integration test: DTCTL_INTEGRATION_ENV / DTCTL_INTEGRATION_TOKEN not set")
	}

	exe := BuildDtctl(t)
	state := filepath.Join(t.TempDir(), "state")
	env := map[string]string{"XDG_STATE_HOME": state}
	cfg := WriteCLIConfig(t, CLIConfig{
		Environment: os.Getenv("DTCTL_INTEGRATION_ENV"),
		Token:       os.Getenv("DTCTL_INTEGRATION_TOKEN"),
	})

	if code, _, stderr := RunDtctl(t, exe, cfg, env, "query", "fetch logs, from:-10m | limit 1", "-o", "json", "--plain"); code != 0 {
		t.Fatalf("query failed: %s", stderr)
	}
	code, _, stderr := RunDtctl(t, exe, cfg, env, "usage", "--plain")
	if code != 0 {
		t.Fatalf("dtctl usage exited %d: %s", code, stderr)
	}
	if !strings.Contains(stderr, "usage tracking is off") {
		t.Errorf("usage did not say tracking is off: %s", stderr)
	}
	if _, err := os.Stat(state); !os.IsNotExist(err) {
		t.Errorf("tracking off must not create the state directory (stat err=%v)", err)
	}
}
