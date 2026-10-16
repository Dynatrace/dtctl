package output

import (
	"bytes"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestNewProgressReporter_Gating(t *testing.T) {
	// A bytes.Buffer is never a TTY, so animate must stay false in every case.
	// (The TTY-enabled path is exercised by constructing the struct directly in
	// the animation tests below.)
	tests := []struct {
		name    string
		enabled bool
		agent   bool
	}{
		{"enabled non-tty", true, false},
		{"disabled non-tty", false, false},
		{"enabled agent mode", true, true},
		{"disabled agent mode", false, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := newProgressReporter(tt.enabled, tt.agent, &bytes.Buffer{})
			if r.animate {
				t.Errorf("animate = true on a non-TTY writer, want false")
			}
		})
	}
}

func TestProgressReporter_DisabledIsSilent(t *testing.T) {
	var buf bytes.Buffer
	r := newProgressReporter(true, false, &buf) // enabled, but non-TTY => disabled
	r.Update(ProgressState{Progress: 50, PreviewRows: 100})
	r.Stop()
	if buf.Len() != 0 {
		t.Errorf("disabled reporter wrote %q, want nothing", buf.String())
	}
}

func TestProgressReporter_AnimateOverwritesAndClears(t *testing.T) {
	var buf bytes.Buffer
	// Force the animated path directly — a buffer is not a TTY. manualTick keeps
	// the background animator off so frames are driven synchronously by Update.
	r := &ProgressReporter{w: &buf, animate: true, manualTick: true}

	r.Update(ProgressState{Progress: 45, ScannedBytes: 13_359_294_822_752, ScannedRecords: 4_647_571_690})
	r.Update(ProgressState{Progress: 90}) // shorter line must be fully padded over
	r.Stop()

	got := buf.String()
	// Each Update begins by returning the cursor to column 0.
	if !strings.HasPrefix(got, "\r") {
		t.Errorf("animated output should start with a carriage return: %q", got)
	}
	if !strings.Contains(got, "45%") || !strings.Contains(got, "90%") {
		t.Errorf("output missing progress values: %q", got)
	}
	if !strings.Contains(got, "12.2 TB") || !strings.Contains(got, "4.6B recs") {
		t.Errorf("output missing scan stats: %q", got)
	}
	if !strings.Contains(got, "scanning") {
		t.Errorf("output should use the 'scanning' verb when bytes are present: %q", got)
	}
	// Stop clears the line and leaves the cursor at column 0.
	if !strings.HasSuffix(got, "\r") {
		t.Errorf("output should end cleared (trailing CR): %q", got)
	}
}

func TestProgressReporter_AnimatePadsByVisibleWidth(t *testing.T) {
	// With color enabled the line carries ANSI escapes; the clear on Stop must
	// be sized by visible width, not byte length, or it would over-pad and wrap.
	ResetColorCache()
	t.Setenv("FORCE_COLOR", "1")
	t.Cleanup(ResetColorCache)

	var buf bytes.Buffer
	r := &ProgressReporter{w: &buf, animate: true, manualTick: true}
	r.Update(ProgressState{Progress: 50, ScannedBytes: 1 << 40, ScannedRecords: 1_000_000})
	if !ColorEnabled() {
		t.Skip("color not enabled in this environment; visible-width path not exercised")
	}
	// The colored line contains ANSI escapes, so its byte length exceeds its
	// on-screen width; lastVis must track the (smaller) visible width so Stop
	// clears exactly the visible columns without wrapping.
	if r.lastVis == 0 || r.lastVis >= len(buf.String()) {
		t.Errorf("visible width %d should be >0 and less than byte length %d when colored", r.lastVis, len(buf.String()))
	}
}

// syncBuf is a mutex-guarded buffer so the test can read output while the
// background animator writes concurrently, without racing.
type syncBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuf) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuf) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

func TestProgressReporter_SpinnerAdvancesBetweenUpdates(t *testing.T) {
	// The animator must keep the spinner moving on its own timer, without any
	// further Update calls, and stop cleanly.
	ResetColorCache()
	t.Setenv("NO_COLOR", "1") // keep output ANSI-free so we count raw spinner runes
	t.Cleanup(ResetColorCache)

	buf := &syncBuf{}
	r := &ProgressReporter{w: buf, animate: true, start: time.Now(), tickInterval: 10 * time.Millisecond}

	r.Update(ProgressState{Progress: 30}) // starts the animator; no further updates
	time.Sleep(80 * time.Millisecond)     // ~8 ticks
	r.Stop()

	// Count how many distinct spinner frames appeared — proves it advanced
	// without additional Update calls.
	out := buf.String()
	distinct := 0
	for _, g := range progressSpinner {
		if strings.ContainsRune(out, g) {
			distinct++
		}
	}
	if distinct < 3 {
		t.Errorf("spinner advanced through only %d distinct frames, want >=3 (output=%q)", distinct, out)
	}
	// After Stop the line is cleared and the animator has exited; a subsequent
	// Stop must be a safe no-op.
	if !strings.HasSuffix(out, "\r") {
		t.Errorf("output should end cleared with a trailing CR: %q", out)
	}
	r.Stop() // must not panic (idempotent)
}

func TestProgressReporter_CompletePrintsSummary(t *testing.T) {
	var buf bytes.Buffer
	// manualTick keeps the animator off; started must be forced so Complete
	// treats this as a bar that was shown.
	r := &ProgressReporter{w: &buf, animate: true, manualTick: true, start: time.Now().Add(-42100 * time.Millisecond)}
	r.Update(ProgressState{Progress: 90, ScannedBytes: 1 << 40, ScannedRecords: 5_983_657_731})

	r.Complete(ProgressState{Progress: 100, ScannedBytes: 19_051_610_460_057, ScannedRecords: 5_983_657_731})

	got := buf.String()
	if !strings.Contains(got, "✓") {
		t.Errorf("summary should include a check mark: %q", got)
	}
	if !strings.Contains(got, "scanned 17.3 TB · 6.0B records in 42.1s") {
		t.Errorf("summary body wrong: %q", got)
	}
	if !strings.HasSuffix(got, "\n") {
		t.Errorf("summary should end with a newline: %q", got)
	}
}

func TestProgressReporter_CompleteSilentWhenNoBar(t *testing.T) {
	// A query too fast to have animated (never Update'd) must not print a
	// summary — Complete just clears (a no-op here) and stays quiet.
	var buf bytes.Buffer
	r := &ProgressReporter{w: &buf, animate: true, manualTick: true, start: time.Now()}
	r.Complete(ProgressState{Progress: 100, ScannedBytes: 1 << 40})
	if buf.Len() != 0 {
		t.Errorf("Complete without a prior bar should be silent, wrote %q", buf.String())
	}
}

func TestProgressReporter_StopAfterCompleteIsNoop(t *testing.T) {
	var buf bytes.Buffer
	r := &ProgressReporter{w: &buf, animate: true, manualTick: true, start: time.Now()}
	r.Update(ProgressState{Progress: 50})
	r.Complete(ProgressState{Progress: 100})
	n := buf.Len()
	r.Stop() // must not clear or double-print after Complete already ran
	if buf.Len() != n {
		t.Errorf("Stop after Complete wrote extra bytes: %q", buf.String()[n:])
	}
}

func TestRenderBar(t *testing.T) {
	tests := []struct {
		progress   int
		wantFilled int
	}{
		{0, 0},
		{50, 10},
		{100, 20},
		{150, 20}, // clamped
	}
	for _, tt := range tests {
		bar := renderBar(tt.progress)
		if got := strings.Count(bar, "█"); got != tt.wantFilled {
			t.Errorf("renderBar(%d) filled = %d, want %d (bar=%q)", tt.progress, got, tt.wantFilled, bar)
		}
		if total := strings.Count(bar, "█") + strings.Count(bar, "░"); total != progressBarWidth {
			t.Errorf("renderBar(%d) total cells = %d, want %d", tt.progress, total, progressBarWidth)
		}
	}
}

func TestHumanizeMetric(t *testing.T) {
	tests := map[int64]string{
		0:          "0",
		999:        "999",
		1000:       "1.0K",
		4200:       "4.2K",
		2085739:    "2.1M",
		5983657731: "6.0B",
		// Boundary roll-up: values that would render as "1000.0<unit>" must roll
		// up to the next unit instead.
		999_949:     "999.9K",
		999_950:     "1.0M",
		999_949_999: "999.9M",
		999_950_000: "1.0B",
	}
	for in, want := range tests {
		if got := humanizeMetric(in); got != want {
			t.Errorf("humanizeMetric(%d) = %q, want %q", in, got, want)
		}
	}
}

func TestFormatElapsed(t *testing.T) {
	tests := map[time.Duration]string{
		500 * time.Millisecond:  "0.5s",
		8200 * time.Millisecond: "8.2s",
		59 * time.Second:        "59.0s",
		63 * time.Second:        "1m03s",
		125 * time.Second:       "2m05s",
		// Boundary: a value that rounds to 60.0s must roll over to the minute
		// form rather than print "60.0s".
		59940 * time.Millisecond: "59.9s",
		59950 * time.Millisecond: "1m00s",
		59970 * time.Millisecond: "1m00s",
	}
	for in, want := range tests {
		if got := formatElapsed(in); got != want {
			t.Errorf("formatElapsed(%v) = %q, want %q", in, got, want)
		}
	}
}

func TestProgressReporter_ClampsToTerminalWidth(t *testing.T) {
	// A narrow terminal must not receive a line wider than its width, or it
	// wraps to a second physical row that a bare "\r" cannot erase.
	t.Setenv("NO_COLOR", "1") // keep output ANSI-free so width == byte length
	ResetColorCache()
	t.Cleanup(ResetColorCache)

	var buf bytes.Buffer
	r := &ProgressReporter{w: &buf, animate: true, manualTick: true, start: time.Now(), termWidth: 20}
	r.Update(ProgressState{Progress: 45, ScannedBytes: 13_359_294_822_752, ScannedRecords: 4_647_571_690})

	got := strings.TrimPrefix(buf.String(), "\r")
	if w := visibleWidth(got); w > 20 {
		t.Errorf("line visible width = %d, want <= 20 (line=%q)", w, got)
	}
}

func TestTruncateVisible(t *testing.T) {
	if got := truncateVisible("hello world", 5); got != "hello" {
		t.Errorf("truncateVisible plain = %q, want %q", got, "hello")
	}
	if got := truncateVisible("hi", 5); got != "hi" {
		t.Errorf("truncateVisible shorter-than-max should be unchanged, got %q", got)
	}
	// ANSI escapes are passed through uncounted; only visible columns are capped.
	colored := Cyan + "abcdef" + Reset
	got := truncateVisible(colored, 3)
	if visibleWidth(got) != 3 {
		t.Errorf("truncateVisible colored visible width = %d, want 3 (got %q)", visibleWidth(got), got)
	}
}

func TestNewProgressReporterTo_DrawsOnTheGivenWriter(t *testing.T) {
	var buf bytes.Buffer
	r := NewProgressReporterTo(true, false, &buf)
	if r.w != &buf {
		t.Errorf("reporter writes to %v, want the writer it was given", r.w)
	}
	// Not a terminal, so nothing is drawn: the invocation's stderr stays clean.
	r.Update(ProgressState{Progress: 50})
	r.Stop()
	if buf.Len() != 0 {
		t.Errorf("a reporter on a non-terminal writer wrote %q, want nothing", buf.String())
	}
}
