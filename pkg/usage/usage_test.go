package usage

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"sync"
	"testing"
	"time"
)

var day0 = time.Date(2026, 10, 12, 15, 4, 5, 0, time.UTC)

func clockAt(t *time.Time) func() time.Time { return func() time.Time { return *t } }

func newTestLedger(t *testing.T, now *time.Time) *Ledger {
	t.Helper()
	return New(filepath.Join(t.TempDir(), "state", "dtctl", FileName)).WithClock(clockAt(now))
}

func mustRead(t *testing.T, l *Ledger) Days {
	t.Helper()
	days, err := l.Read()
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	return days
}

func TestRecordAggregatesPerDayAndContext(t *testing.T) {
	now := day0
	l := newTestLedger(t, &now)

	for _, s := range []Sample{
		{ScannedBytes: 100, ScannedRecords: 10},
		{ScannedBytes: 50, ScannedRecords: 5, ScannedDataPoints: 3},
		{Failed: true, ScannedBytes: 7},
	} {
		if err := l.Record("prod", s); err != nil {
			t.Fatalf("Record: %v", err)
		}
	}
	if err := l.Record("dev", Sample{ScannedBytes: 1}); err != nil {
		t.Fatal(err)
	}
	now = day0.AddDate(0, 0, -3) // a sample filed three days earlier
	if err := l.Record("prod", Sample{ScannedBytes: 1000}); err != nil {
		t.Fatal(err)
	}
	now = day0

	days := mustRead(t, l)
	got := days["2026-10-12"]["prod"]
	want := Counters{Queries: 3, Failed: 1, ScannedBytes: 157, ScannedRecords: 15, ScannedDataPoints: 3}
	if got != want {
		t.Errorf("today/prod = %+v, want %+v", got, want)
	}
	if days["2026-10-12"]["dev"].Queries != 1 {
		t.Errorf("dev was not kept separate: %+v", days["2026-10-12"])
	}

	totals := Summarize(days, "prod", now, Windows)
	if len(totals) != 3 {
		t.Fatalf("got %d windows", len(totals))
	}
	if totals[0].Window.Name != "today" || totals[0].Queries != 3 || totals[0].ScannedBytes != 157 {
		t.Errorf("today total = %+v", totals[0])
	}
	if totals[1].Queries != 4 || totals[1].ScannedBytes != 1157 || totals[1].From != "2026-10-06" || totals[1].To != "2026-10-12" {
		t.Errorf("7d total = %+v", totals[1])
	}
	if totals[2].Queries != 4 || totals[2].From != "2026-09-13" {
		t.Errorf("30d total = %+v", totals[2])
	}
	if got := Contexts(days); len(got) != 2 || got[0] != "dev" || got[1] != "prod" {
		t.Errorf("Contexts = %v", got)
	}
}

func TestSummarizeWindowEdges(t *testing.T) {
	days := Days{
		"2026-10-12": {"c": {Queries: 1}},  // today
		"2026-10-06": {"c": {Queries: 10}}, // oldest day of 7d
		"2026-10-05": {"c": {Queries: 100}},
		"2026-09-13": {"c": {Queries: 1000}}, // oldest day of 30d
		"2026-09-12": {"c": {Queries: 10000}},
		"2026-10-13": {"c": {Queries: 100000}}, // future (clock skew): never counted
	}
	totals := Summarize(days, "c", day0, Windows)
	if totals[0].Queries != 1 || totals[1].Queries != 11 || totals[2].Queries != 1111 {
		t.Errorf("windows = %d / %d / %d, want 1 / 11 / 1111", totals[0].Queries, totals[1].Queries, totals[2].Queries)
	}
	if got := Summarize(days, "other", day0, Windows); got[2].Queries != 0 {
		t.Errorf("an unknown context must sum to zero, got %+v", got[2])
	}
}

func TestRecordPrunesBeyondRetention(t *testing.T) {
	now := day0
	l := newTestLedger(t, &now)
	keep := day0.AddDate(0, 0, -(RetentionDays - 1)).Format(dateLayout)
	drop := day0.AddDate(0, 0, -RetentionDays).Format(dateLayout)

	// Seed old days directly, as an earlier run would have left them.
	if err := os.MkdirAll(filepath.Dir(l.Path()), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := l.write(Days{
		keep:         {"prod": {Queries: 1}},
		drop:         {"prod": {Queries: 1}},
		"not-a-date": {"prod": {Queries: 1}},
	}); err != nil {
		t.Fatal(err)
	}
	if err := l.Record("prod", Sample{}); err != nil {
		t.Fatal(err)
	}
	days := mustRead(t, l)
	if _, ok := days[keep]; !ok {
		t.Errorf("%s is inside the %d-day window and must be kept", keep, RetentionDays)
	}
	if _, ok := days[drop]; ok {
		t.Errorf("%s is outside the %d-day window and must be pruned", drop, RetentionDays)
	}
	if _, ok := days["not-a-date"]; ok {
		t.Error("a key that is not a date must be pruned")
	}
	if len(days) != 2 {
		t.Errorf("days = %v", days)
	}
}

func TestLedgerFileModeAndShape(t *testing.T) {
	now := day0
	l := newTestLedger(t, &now)
	if err := l.Record("prod", Sample{ScannedBytes: 1}); err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" {
		info, err := os.Stat(l.Path())
		if err != nil {
			t.Fatal(err)
		}
		if mode := info.Mode().Perm(); mode != 0o600 {
			t.Errorf("ledger mode = %o, want 600", mode)
		}
		dir, err := os.Stat(filepath.Dir(l.Path()))
		if err != nil {
			t.Fatal(err)
		}
		if mode := dir.Mode().Perm(); mode&0o077 != 0 {
			t.Errorf("state directory mode = %o, want no group/other access", mode)
		}
	}

	// Only aggregate counters: the top-level keys and the per-context fields
	// are exactly the documented ones.
	raw, err := os.ReadFile(l.Path())
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	if len(doc) != 2 || doc["version"] == nil || doc["days"] == nil {
		t.Errorf("unexpected top-level keys: %s", raw)
	}
	var f struct {
		Days map[string]map[string]map[string]json.RawMessage `json:"days"`
	}
	if err := json.Unmarshal(raw, &f); err != nil {
		t.Fatal(err)
	}
	allowed := map[string]bool{"queries": true, "failed": true, "scannedBytes": true, "scannedRecords": true, "scannedDataPoints": true}
	for field := range f.Days["2026-10-12"]["prod"] {
		if !allowed[field] {
			t.Errorf("ledger stores an undocumented field %q", field)
		}
	}

	// No temp files left behind.
	entries, err := os.ReadDir(filepath.Dir(l.Path()))
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.Name() != FileName && e.Name() != FileName+".lock" {
			t.Errorf("unexpected file %q next to the ledger", e.Name())
		}
	}
}

func TestReadAndResetCreateNothingWithoutALedger(t *testing.T) {
	now := day0
	l := newTestLedger(t, &now)
	days := mustRead(t, l)
	if len(days) != 0 {
		t.Fatalf("days = %v", days)
	}
	for _, ctx := range []string{"", "prod"} {
		removed, err := l.Reset(ctx)
		if err != nil || removed {
			t.Fatalf("Reset(%q) on a missing ledger = %v, %v", ctx, removed, err)
		}
	}
	if _, err := os.Stat(filepath.Dir(l.Path())); !os.IsNotExist(err) {
		t.Fatalf("reading or resetting a missing ledger created %s (err=%v)", filepath.Dir(l.Path()), err)
	}
}

func TestReset(t *testing.T) {
	now := day0
	l := newTestLedger(t, &now)
	for _, c := range []string{"prod", "dev", "prod"} {
		if err := l.Record(c, Sample{ScannedBytes: 1}); err != nil {
			t.Fatal(err)
		}
	}

	removed, err := l.Reset("prod")
	if err != nil || !removed {
		t.Fatalf("Reset(prod) = %v, %v", removed, err)
	}
	days := mustRead(t, l)
	if _, ok := days["2026-10-12"]["prod"]; ok {
		t.Error("prod survived its reset")
	}
	if days["2026-10-12"]["dev"].Queries != 1 {
		t.Error("resetting prod touched dev")
	}
	if removed, _ := l.Reset("prod"); removed {
		t.Error("a second reset reported removing something")
	}

	removed, err = l.Reset("")
	if err != nil || !removed {
		t.Fatalf("Reset(all) = %v, %v", removed, err)
	}
	if _, err := os.Stat(l.Path()); !os.IsNotExist(err) {
		t.Error("Reset(all) must delete the ledger file")
	}
}

func TestCorruptLedgerIsReplaced(t *testing.T) {
	now := day0
	l := newTestLedger(t, &now)
	if err := os.MkdirAll(filepath.Dir(l.Path()), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(l.Path(), []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := l.Read(); err == nil {
		t.Error("Read of a corrupt ledger must report it")
	}
	if err := l.Record("prod", Sample{ScannedBytes: 5}); err != nil {
		t.Fatalf("Record over a corrupt ledger: %v", err)
	}
	if got := mustRead(t, l)["2026-10-12"]["prod"]; got.Queries != 1 || got.ScannedBytes != 5 {
		t.Errorf("after recovery: %+v", got)
	}
}

func TestNewerLedgerIsNeverRewritten(t *testing.T) {
	now := day0
	l := newTestLedger(t, &now)
	if err := os.MkdirAll(filepath.Dir(l.Path()), 0o700); err != nil {
		t.Fatal(err)
	}
	newer := []byte(`{"version":99,"days":{}}`)
	if err := os.WriteFile(l.Path(), newer, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := l.Record("prod", Sample{}); err == nil {
		t.Error("Record must refuse a ledger from a newer dtctl")
	}
	got, _ := os.ReadFile(l.Path())
	if string(got) != string(newer) {
		t.Errorf("newer ledger was rewritten: %s", got)
	}
}

func TestRecorderNeverFails(t *testing.T) {
	// A path whose parent is a regular file can never be created.
	blocker := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(blocker, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	var debug lockedBuffer
	r := &Recorder{Ledger: New(filepath.Join(blocker, "dtctl", FileName)), Context: "prod", Debug: &debug}
	r.RecordQuery(Sample{ScannedBytes: 1}) // must not panic or block
	if debug.String() == "" {
		t.Error("a skipped update should leave a debug line")
	}
	var nilRecorder *Recorder
	nilRecorder.RecordQuery(Sample{})
}

// TestConcurrentWritersInProcess hammers one ledger from many goroutines, each
// with its own Ledger (and so its own lock file handle), the way parallel
// in-process invocations would.
func TestConcurrentWritersInProcess(t *testing.T) {
	path := filepath.Join(t.TempDir(), FileName)
	const writers, perWriter = 16, 25
	var wg sync.WaitGroup
	errs := make(chan error, writers*perWriter)
	for w := 0; w < writers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			l := New(path).WithClock(func() time.Time { return day0 })
			for i := 0; i < perWriter; i++ {
				if err := l.Record(fmt.Sprintf("ctx-%d", w%2), Sample{ScannedBytes: 10, ScannedRecords: 1}); err != nil {
					errs <- err
				}
			}
		}(w)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Errorf("Record: %v", err)
	}
	assertTotal(t, path, writers*perWriter)
}

// TestConcurrentWritersAcrossProcesses runs separate processes against one
// ledger, which is how agents running dtctl in parallel actually hit it.
func TestConcurrentWritersAcrossProcesses(t *testing.T) {
	if testing.Short() {
		t.Skip("spawns subprocesses")
	}
	path := filepath.Join(t.TempDir(), FileName)
	const procs, perProc = 8, 20
	var wg sync.WaitGroup
	for p := 0; p < procs; p++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			cmd := exec.Command(os.Args[0], "-test.run=^TestHelperLedgerWriter$")
			cmd.Env = append(os.Environ(),
				"DTCTL_USAGE_HELPER_PATH="+path,
				"DTCTL_USAGE_HELPER_COUNT="+strconv.Itoa(perProc))
			if out, err := cmd.CombinedOutput(); err != nil {
				t.Errorf("helper process: %v\n%s", err, out)
			}
		}()
	}
	wg.Wait()
	assertTotal(t, path, procs*perProc)
}

// TestHelperLedgerWriter is the subprocess body for
// TestConcurrentWritersAcrossProcesses; it does nothing in a normal run.
func TestHelperLedgerWriter(t *testing.T) {
	path := os.Getenv("DTCTL_USAGE_HELPER_PATH")
	if path == "" {
		t.Skip("helper process only")
	}
	n, _ := strconv.Atoi(os.Getenv("DTCTL_USAGE_HELPER_COUNT"))
	l := New(path).WithClock(func() time.Time { return day0 })
	for i := 0; i < n; i++ {
		if err := l.Record(fmt.Sprintf("ctx-%d", i%2), Sample{ScannedBytes: 10, ScannedRecords: 1}); err != nil {
			t.Fatalf("Record: %v", err)
		}
	}
}

func assertTotal(t *testing.T, path string, want int) {
	t.Helper()
	days, err := New(path).Read()
	if err != nil {
		t.Fatal(err)
	}
	var total Counters
	for _, c := range days["2026-10-12"] {
		total.Add(c)
	}
	if total.Queries != int64(want) || total.ScannedBytes != int64(want*10) || total.ScannedRecords != int64(want) {
		t.Errorf("total = %+v, want %d queries (an update was lost)", total, want)
	}
}

func TestFormatBytes(t *testing.T) {
	for in, want := range map[int64]string{
		0:                    "0 B",
		1023:                 "1023 B",
		1024:                 "1.00 KiB",
		5 * 1024 * 1024:      "5.00 MiB",
		1536 * 1024 * 1024:   "1.50 GiB",
		3 << 40:              "3.00 TiB",
		int64(1) << 62:       "4.00 EiB",
		int64(9_000_000_000): "8.38 GiB",
	} {
		if got := FormatBytes(in); got != want {
			t.Errorf("FormatBytes(%d) = %q, want %q", in, got, want)
		}
	}
}

type lockedBuffer struct {
	mu sync.Mutex
	b  []byte
}

func (l *lockedBuffer) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.b = append(l.b, p...)
	return len(p), nil
}

func (l *lockedBuffer) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return string(l.b)
}
