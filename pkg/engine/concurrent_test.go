package engine

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// These tests exist to measure and to bound: they establish that an Engine
// built with WithConcurrentExecution really does overlap executions, that each
// execution's output comes back to the caller that asked for it, and that the
// serialized default is unchanged. The broader correctness gates — the same
// output as the serialized engine across the surface, dry runs, tenant
// isolation — are in concurrent_isolation_test.go.

// slowEnv is a stand-in Dynatrace environment whose responses are delayed, so
// that a test can tell overlapping executions from sequential ones by wall
// clock rather than by racing.
func slowEnv(t *testing.T, delay time.Duration) (url string, inFlight *atomic.Int64, peak *atomic.Int64) {
	t.Helper()
	inFlight, peak = &atomic.Int64{}, &atomic.Int64{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := inFlight.Add(1)
		for {
			old := peak.Load()
			if n <= old || peak.CompareAndSwap(old, n) {
				break
			}
		}
		time.Sleep(delay)
		inFlight.Add(-1)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"records":[],"metadata":{}}`))
	}))
	t.Cleanup(srv.Close)
	return srv.URL, inFlight, peak
}

func req(url, command string) Request {
	return Request{
		Command:        command,
		EnvironmentURL: url,
		Token:          "dt0c01.CONCURRENT.TESTTOKENTESTTOKENTESTTOKENTESTTOKENTESTTOKENTESTTOKEN",
		SafetyLevel:    "readonly",
	}
}

// TestEngineOverlapsExecutions is the headline claim: with a concurrency above
// one, several commands are in the platform round trip at the same time.
func TestEngineOverlapsExecutions(t *testing.T) {
	const n = 8
	url, _, peak := slowEnv(t, 150*time.Millisecond)

	e := New(Limits{MaxQueued: n, MaxDuration: 30 * time.Second}, WithConcurrentExecution(n))

	var wg sync.WaitGroup
	start := time.Now()
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := e.Execute(context.Background(), req(url, "get workflows --agent")); err != nil {
				t.Errorf("execute: %v", err)
			}
		}()
	}
	wg.Wait()
	elapsed := time.Since(start)

	if got := peak.Load(); got < 2 {
		t.Fatalf("peak concurrent upstream requests = %d, want >= 2 (executions did not overlap)", got)
	}
	// Serialized, n requests at 150ms each could not finish this fast.
	if budget := time.Duration(n) * 150 * time.Millisecond; elapsed >= budget {
		t.Errorf("elapsed %v >= serialized budget %v; executions appear serialized", elapsed, budget)
	}
	t.Logf("n=%d elapsed=%v peak_upstream_concurrency=%d", n, elapsed, peak.Load())
}

// TestSerializedEngineStillSerializes guards the default: nothing in the
// concurrent path may make the ordinary embedded caller concurrent by accident.
func TestSerializedEngineStillSerializes(t *testing.T) {
	const n = 4
	url, _, peak := slowEnv(t, 40*time.Millisecond)

	e := New(Limits{MaxQueued: n, MaxDuration: 30 * time.Second})

	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = e.Execute(context.Background(), req(url, "get workflows --agent"))
		}()
	}
	wg.Wait()

	if got := peak.Load(); got != 1 {
		t.Errorf("peak concurrent upstream requests = %d, want 1 (serialized engine overlapped)", got)
	}
}

// TestConcurrencyIsOptIn guards the switch: an Engine runs one invocation at a
// time unless it was built with WithConcurrentExecution, however deep its
// queue, so no caller ends up running invocations side by side by accident.
func TestConcurrencyIsOptIn(t *testing.T) {
	const n = 4
	url, _, peak := slowEnv(t, 40*time.Millisecond)

	e := New(Limits{MaxQueued: n, MaxDuration: 30 * time.Second})
	if got := e.MaxConcurrent(); got != 1 {
		t.Errorf("MaxConcurrent() = %d without WithConcurrentExecution, want 1", got)
	}

	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = e.Execute(context.Background(), req(url, "get workflows --agent"))
		}()
	}
	wg.Wait()

	if got := peak.Load(); got != 1 {
		t.Errorf("peak concurrent upstream requests = %d, want 1: an engine built without WithConcurrentExecution overlapped", got)
	}
}

// overlapCorpus is the set of commands the equality gate runs. They cover the
// distinct output paths — the agent envelope (get) and bare fmt.Print* in a
// command body (version) — and they differ only in root flags, which each
// invocation snapshots.
//
// Two things are deliberately absent. Nothing here varies a
// *command-specific* flag, and nothing here reads the command tree. Both are
// measured instead, by TestConcurrentCommandSpecificFlagBleed and
// TestConcurrentSurfaceReadBleed.
var overlapCorpus = []string{
	"version --agent",
	"get workflows --agent",
	"get slos --agent",
	"get buckets --agent",
}

// TestConcurrentOutputEqualsSerialized is the correctness gate.
//
// It takes each command's serialized output as the reference — that is what
// the CLI produces — then runs the whole corpus
// concurrently, many times over, and requires every result to be byte-equal
// to its own reference.
//
// A mismatch is not a flake to be retried: it names an output path that still
// reaches process-global state, and each one is either fixed or recorded in
// docs/dev/CONCURRENT_EXECUTION.md.
func TestConcurrentOutputEqualsSerialized(t *testing.T) {
	url, _, _ := slowEnv(t, 15*time.Millisecond)

	// Reference outputs, one command at a time through the serialized engine.
	want := make(map[string]string, len(overlapCorpus))
	for _, c := range overlapCorpus {
		res, err := Execute(context.Background(), req(url, c))
		if err != nil {
			t.Fatalf("serialized %q: %v", c, err)
		}
		want[c] = string(res.Stdout)
		if strings.TrimSpace(want[c]) == "" {
			t.Fatalf("serialized %q produced no output; corpus entry is useless", c)
		}
	}

	e := New(Limits{MaxQueued: 128, MaxDuration: 30 * time.Second}, WithConcurrentExecution(16))

	const rounds = 10
	var wg sync.WaitGroup
	var mismatches sync.Map // command -> *atomic.Int64
	for _, c := range overlapCorpus {
		mismatches.Store(c, &atomic.Int64{})
	}
	for r := 0; r < rounds; r++ {
		for _, c := range overlapCorpus {
			wg.Add(1)
			go func() {
				defer wg.Done()
				res, err := e.Execute(context.Background(), req(url, c))
				if err != nil {
					t.Errorf("concurrent %q: %v", c, err)
					return
				}
				if string(res.Stdout) != want[c] {
					n, _ := mismatches.Load(c)
					n.(*atomic.Int64).Add(1)
				}
			}()
		}
	}
	wg.Wait()

	failed := false
	for _, c := range overlapCorpus {
		n, _ := mismatches.Load(c)
		got := n.(*atomic.Int64).Load()
		t.Logf("%-26s %d/%d runs differed from serialized output", c, got, rounds)
		if got > 0 {
			failed = true
		}
	}
	if failed {
		t.Error("concurrent output diverged from serialized output; see the per-command counts above")
	}
}

// TestConcurrentCommandSpecificFlagBleed is the gate for command-specific
// flags: overlapping runs of the same command that differ only in such a flag
// must each see their own value.
//
// On the shared tree these were package variables on a single shared cobra tree, which
// restorePristineTree reset to their defaults at the start of every
// invocation — including while another invocation's body was still reading
// them — and about half of all overlapping runs came back corrupted. A
// per-invocation tree binds them to constructor-local variables instead.
//
// `dtctl commands` and `dtctl commands --brief` are the demonstration: --brief
// belongs to that command, so with shared storage the plain run saw the brief
// listing and the brief run the plain one, depending on who reset last.
func TestConcurrentCommandSpecificFlagBleed(t *testing.T) {
	url, _, _ := slowEnv(t, 10*time.Millisecond)

	variants := []string{"commands --agent", "commands --brief --agent"}
	want := make(map[string]string, len(variants))
	for _, c := range variants {
		res, err := Execute(context.Background(), req(url, c))
		if err != nil {
			t.Fatalf("serialized %q: %v", c, err)
		}
		want[c] = string(res.Stdout)
	}
	if want[variants[0]] == want[variants[1]] {
		t.Fatal("the two variants produce identical output; they cannot demonstrate bleed")
	}

	e := New(Limits{MaxQueued: 64, MaxDuration: 30 * time.Second}, WithConcurrentExecution(8))

	const rounds = 20
	counts := map[string]*atomic.Int64{}
	for _, c := range variants {
		counts[c] = &atomic.Int64{}
	}
	var wg sync.WaitGroup
	for r := 0; r < rounds; r++ {
		for _, c := range variants {
			wg.Add(1)
			go func() {
				defer wg.Done()
				res, err := e.Execute(context.Background(), req(url, c))
				if err != nil {
					return
				}
				if string(res.Stdout) != want[c] {
					counts[c].Add(1)
				}
			}()
		}
	}
	wg.Wait()

	total := int64(0)
	for _, c := range variants {
		got := counts[c].Load()
		total += got
		t.Logf("%-26s %d/%d runs corrupted by a concurrent peer", c, got, rounds)
	}
	if total > 0 {
		t.Errorf("command-specific flag bleed: %d/%d runs (%.0f%%) saw a concurrent peer's flags",
			total, int64(rounds*len(variants)),
			100*float64(total)/float64(rounds*len(variants)))
	}
}

// TestConcurrentSurfaceReadBleed is the gate for the command surface.
//
// `dtctl commands` reports the command surface by walking a tree. An engine
// invocation masks the host-only commands (alias, config, ctx, ...) via
// BlockedCommands. On the shared tree a peer's restorePristineTree un-masked
// everything before re-applying its own masks, and a catalog read inside that
// window listed commands this engine does not offer; on a per-invocation tree
// the catalog must be built from that invocation's own masked tree, never the
// singleton's (which a concurrent invocation never masks at all).
//
// That makes it a surface-disclosure bug, not just a formatting one: the
// catalog is what an agent reads to decide what it may call.
func TestConcurrentSurfaceReadBleed(t *testing.T) {
	url, _, _ := slowEnv(t, 10*time.Millisecond)

	want, err := Execute(context.Background(), req(url, "commands --agent"))
	if err != nil {
		t.Fatalf("serialized: %v", err)
	}

	e := New(Limits{MaxQueued: 64, MaxDuration: 30 * time.Second}, WithConcurrentExecution(8))

	const rounds = 20
	var corrupted, leakedBlocked atomic.Int64
	var wg sync.WaitGroup
	for r := 0; r < rounds; r++ {
		// A peer doing anything at all is enough; it only has to restore the
		// tree while the catalog is being walked.
		wg.Add(2)
		go func() { defer wg.Done(); _, _ = e.Execute(context.Background(), req(url, "version --agent")) }()
		go func() {
			defer wg.Done()
			res, err := e.Execute(context.Background(), req(url, "commands --agent"))
			if err != nil {
				return
			}
			if string(res.Stdout) != string(want.Stdout) {
				corrupted.Add(1)
			}
			// "alias" is in the engine's unsupportedCommands list, so it must
			// never appear in a catalog this engine produced.
			for _, blocked := range []string{"\n  alias:", "\n  config:", "\n  ctx:"} {
				if strings.Contains(string(res.Stdout), blocked) {
					leakedBlocked.Add(1)
					break
				}
			}
		}()
	}
	wg.Wait()

	if n := corrupted.Load(); n > 0 {
		t.Errorf("commands catalog corrupted by a concurrent peer: %d/%d runs", n, rounds)
	}
	if n := leakedBlocked.Load(); n > 0 {
		t.Errorf("catalogs listing a command the engine blocks: %d/%d runs", n, rounds)
	}
}

// TestConcurrentMemoryFootprint reports what one in-flight command costs
// in-process, to compare with the ~25 MiB a native process per command costs.
func TestConcurrentMemoryFootprint(t *testing.T) {
	if testing.Short() {
		t.Skip("measurement test")
	}
	url, _, _ := slowEnv(t, 300*time.Millisecond)

	for _, n := range []int{1, 8, 64, 256} {
		t.Run(fmt.Sprintf("n=%d", n), func(t *testing.T) {
			e := New(Limits{MaxQueued: n * 2, MaxDuration: 60 * time.Second}, WithConcurrentExecution(n))

			runtime.GC()
			var before runtime.MemStats
			runtime.ReadMemStats(&before)
			goroutinesBefore := runtime.NumGoroutine()

			var peakHeap atomic.Uint64
			done := make(chan struct{})
			go func() {
				var m runtime.MemStats
				for {
					select {
					case <-done:
						return
					default:
						runtime.ReadMemStats(&m)
						for {
							cur := peakHeap.Load()
							if m.HeapAlloc <= cur || peakHeap.CompareAndSwap(cur, m.HeapAlloc) {
								break
							}
						}
						time.Sleep(10 * time.Millisecond)
					}
				}
			}()

			var wg sync.WaitGroup
			start := time.Now()
			for i := 0; i < n; i++ {
				wg.Add(1)
				go func() {
					defer wg.Done()
					_, _ = e.Execute(context.Background(), req(url, "get workflows --agent"))
				}()
			}
			wg.Wait()
			close(done)
			elapsed := time.Since(start)

			peak := peakHeap.Load()
			perCommand := "n/a"
			if peak > before.HeapAlloc {
				perCommand = fmt.Sprintf("%.2f MiB", float64(peak-before.HeapAlloc)/(1<<20)/float64(n))
			}
			t.Logf("n=%-4d elapsed=%-12v peak_heap=%.1f MiB  per_command=%s  goroutines=%d->%d",
				n, elapsed, float64(peak)/(1<<20), perCommand,
				goroutinesBefore, runtime.NumGoroutine())
		})
	}
}

// TestEngineExecuteWithLimitsFillsFromTheEngine pins where a per-request budget
// gets what it leaves out: the engine's limits, not DefaultLimits. A host that
// sized MaxOutputBytes at construction and names only a MaxDuration per request
// would otherwise silently run under the default cap.
func TestEngineExecuteWithLimitsFillsFromTheEngine(t *testing.T) {
	e := New(Limits{MaxQueued: 7, MaxDuration: 9 * time.Second, MaxOutputBytes: 123, MaxFileBytes: 456})

	got := e.effectiveLimits(Limits{MaxDuration: time.Second})
	want := Limits{MaxQueued: 7, MaxDuration: time.Second, MaxOutputBytes: 123, MaxFileBytes: 456}
	if got != want {
		t.Errorf("effectiveLimits(MaxDuration only) = %+v, want %+v", got, want)
	}

	// Admission belongs to the engine: a request may not resize it.
	if got := e.effectiveLimits(Limits{MaxQueued: 99}); got.MaxQueued != 7 {
		t.Errorf("effectiveLimits(MaxQueued: 99).MaxQueued = %d, want the engine's 7", got.MaxQueued)
	}
	// An engine built on defaults still fills from defaults.
	if got := New(Limits{}).effectiveLimits(Limits{}); got != DefaultLimits() {
		t.Errorf("effectiveLimits on a default engine = %+v, want DefaultLimits", got)
	}
}
