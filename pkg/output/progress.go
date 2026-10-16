package output

import (
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"time"

	"golang.org/x/term"
)

// progressBarWidth is the number of cells in the rendered bar.
const progressBarWidth = 20

// defaultTickInterval is how often the animated line is redrawn so the spinner
// and elapsed timer advance smoothly between (much slower) query polls.
const defaultTickInterval = 100 * time.Millisecond

// progressSpinner holds the braille spinner frames advanced on each update.
var progressSpinner = []rune{'⠋', '⠙', '⠹', '⠸', '⠼', '⠴', '⠦', '⠧', '⠇', '⠏'}

// partialBlocks maps an eighth (0-7) to the left-aligned partial block glyph
// used to render sub-cell bar progress. Index 0 is empty (handled separately).
var partialBlocks = []rune{' ', '▏', '▎', '▍', '▌', '▋', '▊', '▉'}

// ProgressState is a snapshot of a running query's progress passed to
// ProgressReporter.Update. All fields are optional; zero values are omitted
// from the rendered line.
type ProgressState struct {
	// Progress is the completion percentage (0-100).
	Progress int
	// ScannedBytes / ScannedRecords are the running scan totals reported by the
	// backend. When non-zero they are shown in preference to the preview count,
	// since they reflect the actual work (and cost) done so far.
	ScannedBytes   int64
	ScannedRecords int64
	// PreviewRows is the number of rows in the latest preview snapshot, shown
	// only when no scan totals are available (e.g. non-Grail-scan queries).
	PreviewRows int
}

// ProgressReporter renders an in-place progress line to stderr while a query
// runs. It is drawn only for an interactive terminal; when disabled (non-TTY
// stderr, --plain, agent mode, or the user opted out) every method is a no-op,
// so callers can wire it unconditionally without disturbing piped or structured
// output. Color is emitted only when ColorEnabled() is true, so NO_COLOR still
// yields a plain (monochrome) bar.
//
// A background goroutine redraws the line on a fixed interval (so the spinner
// and elapsed timer stay smooth between the far slower query polls), while
// Update — called from the poll loop — only refreshes the shared state. The
// mutex guards that state and serializes writes between the two.
type ProgressReporter struct {
	w io.Writer
	// animate is true when the reporter is enabled and stderr is an interactive
	// TTY: the line is redrawn in place with a carriage return and cleared on Stop.
	animate bool
	start   time.Time
	// tickInterval overrides the redraw cadence (tests only); 0 => default.
	tickInterval time.Duration
	// manualTick disables the background animator so tests can drive frames
	// synchronously via Update; production always leaves it false.
	manualTick bool
	// termWidth overrides the detected terminal width (tests only); 0 => detect
	// from the writer, and detection failing (non-*os.File writer) means no
	// truncation.
	termWidth int

	mu        sync.Mutex
	latest    ProgressState
	spinIdx   int
	lastVis   int  // visible width of the last animated line, for padding/clearing
	shown     bool // at least one frame was drawn (gates the completion summary)
	animating bool // the background ticker goroutine is running
	stopped   bool
	ticker    *time.Ticker
	done      chan struct{}
	wg        sync.WaitGroup
}

// NewProgressReporter returns a reporter. When enabled is false (the user opted
// out), or in agent mode, or under --plain, or when stderr is not an
// interactive terminal, the reporter is a silent no-op. NO_COLOR is respected
// for color only — the bar is still drawn, just without color.
func NewProgressReporter(enabled, agentMode bool) *ProgressReporter {
	return newProgressReporter(enabled, agentMode, os.Stderr)
}

// NewProgressReporterTo is NewProgressReporter drawing on w instead of the
// process's stderr, for a caller that owns the invocation's streams.
func NewProgressReporterTo(enabled, agentMode bool, w io.Writer) *ProgressReporter {
	return newProgressReporter(enabled, agentMode, w)
}

// newProgressReporter is the testable core of NewProgressReporter with an
// injectable writer.
func newProgressReporter(enabled, agentMode bool, w io.Writer) *ProgressReporter {
	r := &ProgressReporter{w: w, start: time.Now()}
	// Progress is a stderr affordance: gate on stderr being a TTY (not stdout)
	// and on --plain/agent mode — but not on NO_COLOR, which only drops color.
	r.animate = enabled && !agentMode && !plainModeEnabled && isTerminalWriter(w)
	return r
}

// Update refreshes the progress state. In animated mode it also starts the
// background redraw loop on first call and repaints immediately so new poll
// data (progress, scan totals) appears without waiting for the next tick. It is
// a no-op when the reporter is disabled.
func (p *ProgressReporter) Update(s ProgressState) {
	if p == nil {
		return
	}
	if s.Progress < 0 {
		s.Progress = 0
	}
	if s.Progress > 100 {
		s.Progress = 100
	}

	if !p.animate {
		return
	}

	p.mu.Lock()
	// A late Update after Stop/Complete must not repaint the cleared line.
	if p.stopped {
		p.mu.Unlock()
		return
	}
	p.latest = s
	if !p.animating && !p.manualTick {
		p.animating = true
		p.startTickerLocked()
	}
	p.renderLocked()
	p.mu.Unlock()
}

// startTickerLocked launches the background redraw goroutine. Caller holds mu.
func (p *ProgressReporter) startTickerLocked() {
	iv := p.tickInterval
	if iv <= 0 {
		iv = defaultTickInterval
	}
	p.ticker = time.NewTicker(iv)
	p.done = make(chan struct{})
	p.wg.Add(1)
	go func() {
		defer p.wg.Done()
		for {
			select {
			case <-p.done:
				return
			case <-p.ticker.C:
				p.mu.Lock()
				p.spinIdx++
				p.renderLocked()
				p.mu.Unlock()
			}
		}
	}()
}

// renderLocked paints one frame from the current state. Caller holds mu.
func (p *ProgressReporter) renderLocked() {
	s := p.latest
	verb := "querying"
	if s.ScannedBytes > 0 {
		verb = "scanning"
	}
	spin := Colorize(Cyan, string(progressSpinner[p.spinIdx%len(progressSpinner)]))
	pct := Colorize(Bold, fmt.Sprintf("%3d%%", s.Progress))
	line := fmt.Sprintf("%s %s %s %s%s", spin, verb, pct, renderBar(s.Progress), Colorize(Dim, statsSuffix(s, p.start)))

	// Clamp to the terminal width so a narrow terminal never wraps the line onto
	// a second physical row (which a bare "\r" on the next frame cannot reach to
	// erase, leaving stale rows behind). Truncation counts visible columns only.
	if width := p.terminalWidth(); width > 0 {
		line = truncateVisible(line, width)
	}

	// Return the cursor to column 0, write the new line, then pad with spaces to
	// erase any tail left by a previously longer line. Padding is measured in
	// visible columns (ANSI escapes excluded) so a colored line never overflows
	// the terminal width and wraps.
	vis := visibleWidth(line)
	pad := ""
	if n := p.lastVis - vis; n > 0 {
		pad = strings.Repeat(" ", n)
	}
	fmt.Fprintf(p.w, "\r%s%s", line, pad)
	p.lastVis = vis
	p.shown = true
}

// Drawing reports whether the reporter is actively drawing to the terminal
// (enabled and stderr is a TTY). Callers use it to decide whether Stop() will
// have cleared the line for them, or whether they must emit their own leading
// newline to avoid gluing output to a partially printed line (e.g. a shell's
// "^C" echo).
func (p *ProgressReporter) Drawing() bool {
	return p != nil && p.animate
}

// Stop halts the background animator and erases the progress line so the real
// result renders on a clean row. Use it on error or cancellation; use Complete
// on success to leave a summary. It is safe to call multiple times and when
// disabled.
func (p *ProgressReporter) Stop() {
	if p == nil || !p.animate {
		return
	}
	if _, first := p.stopAnimator(); !first {
		return
	}
	p.clearLine()
}

// Complete halts the animator and, when a bar was actually shown, replaces it
// with a one-line completion summary (a green check, the final scan totals, and
// elapsed time). For a query too fast to have animated it just clears, adding
// no noise. It is idempotent with Stop; whichever runs first wins.
func (p *ProgressReporter) Complete(final ProgressState) {
	if p == nil || !p.animate {
		return
	}
	shown, first := p.stopAnimator()
	if !first {
		return
	}
	p.clearLine()
	if !shown {
		return
	}
	check := Colorize(Bold+Green, "✓")
	fmt.Fprintf(p.w, "%s %s\n", check, Colorize(Dim, summaryText(final, p.start)))
}

// stopAnimator halts the background loop exactly once, waiting for the goroutine
// to exit. It reports whether an animation had started and whether this was the
// first stop (so callers don't double-clear or double-print). The wait happens
// outside the lock to avoid deadlocking the goroutine.
func (p *ProgressReporter) stopAnimator() (shown, firstStop bool) {
	p.mu.Lock()
	if p.stopped {
		s := p.shown
		p.mu.Unlock()
		return s, false
	}
	p.stopped = true
	shown = p.shown
	animating := p.animating
	done, ticker := p.done, p.ticker
	p.mu.Unlock()

	if animating {
		ticker.Stop()
		close(done)
		p.wg.Wait()
	}
	return shown, true
}

// clearLine erases the current animated line, if any.
func (p *ProgressReporter) clearLine() {
	p.mu.Lock()
	if p.lastVis > 0 {
		fmt.Fprintf(p.w, "\r%s\r", strings.Repeat(" ", p.lastVis))
		p.lastVis = 0
	}
	p.mu.Unlock()
}

// renderBar draws a fixed-width bar for a 0-100 percentage using block glyphs
// with sub-cell resolution. The filled portion is colored; the remainder dim.
func renderBar(progress int) string {
	exact := float64(progress) / 100 * float64(progressBarWidth)
	full := int(exact)
	if full > progressBarWidth {
		full = progressBarWidth
	}
	if full < 0 {
		full = 0
	}
	partial := ""
	if full < progressBarWidth {
		if e := int((exact - float64(full)) * 8); e > 0 {
			partial = string(partialBlocks[e])
		}
	}
	empty := progressBarWidth - full
	if partial != "" {
		empty--
	}

	var b strings.Builder
	b.WriteString("▕")
	// Render the moving frontier (the partial cell, or the last full cell when
	// the bar lands exactly on a boundary) in bright cyan, with the body a
	// calmer cyan — a subtle leading-edge glow rather than a flat block.
	switch {
	case partial != "":
		if full > 0 {
			b.WriteString(Colorize(Cyan, strings.Repeat("█", full)))
		}
		b.WriteString(Colorize(BrightCyan, partial))
	case full > 0:
		if full > 1 {
			b.WriteString(Colorize(Cyan, strings.Repeat("█", full-1)))
		}
		b.WriteString(Colorize(BrightCyan, "█"))
	}
	b.WriteString(Colorize(Dim, strings.Repeat("░", empty)))
	b.WriteString("▏")
	return b.String()
}

// summaryText renders the completion line body, e.g.
// "scanned 17.3 TB · 6.0B records in 42.1s", or "done in 3.2s" for a query that
// reported no scan totals.
func summaryText(s ProgressState, start time.Time) string {
	var b strings.Builder
	if s.ScannedBytes > 0 {
		b.WriteString("scanned ")
		b.WriteString(formatBytes(s.ScannedBytes))
		if s.ScannedRecords > 0 {
			b.WriteString(" · ")
			b.WriteString(humanizeMetric(s.ScannedRecords))
			b.WriteString(" records")
		}
		b.WriteString(" in ")
	} else {
		b.WriteString("done in ")
	}
	b.WriteString(formatElapsed(time.Since(start)))
	return b.String()
}

// statsSuffix renders the trailing stats segment for the animated line: scan
// totals (preferred) or preview rows, plus elapsed time.
func statsSuffix(s ProgressState, start time.Time) string {
	var parts []string
	if s.ScannedBytes > 0 {
		parts = append(parts, formatBytes(s.ScannedBytes))
		if s.ScannedRecords > 0 {
			parts = append(parts, humanizeMetric(s.ScannedRecords)+" recs")
		}
	} else if s.PreviewRows > 0 {
		parts = append(parts, "preview: "+formatNumber(int64(s.PreviewRows))+" rows")
	}
	parts = append(parts, formatElapsed(time.Since(start)))
	return "  " + strings.Join(parts, " · ")
}

// formatElapsed renders a duration compactly: "8.2s" under a minute, "1m03s"
// above.
func formatElapsed(d time.Duration) string {
	// Round to the display resolution (tenths of a second) first so a value
	// just under a minute (e.g. 59.97s) rolls over to "1m00s" instead of
	// rounding to a nonsensical "60.0s".
	d = d.Round(100 * time.Millisecond)
	if d < time.Minute {
		return fmt.Sprintf("%.1fs", d.Seconds())
	}
	m := int(d / time.Minute)
	sec := int((d % time.Minute) / time.Second)
	return fmt.Sprintf("%dm%02ds", m, sec)
}

// humanizeMetric formats a large count with a decimal SI-style suffix, e.g.
// 5983657731 -> "6.0B", 2085739 -> "2.1M", 4200 -> "4.2K". Each threshold sits
// just below its SI boundary so a value that would render as "1000.0<unit>"
// (e.g. 999,950,000 -> "1000.0M") rolls up to the next unit ("1.0B") instead.
func humanizeMetric(n int64) string {
	f := float64(n)
	switch {
	case f >= 999_950_000:
		return fmt.Sprintf("%.1fB", f/1_000_000_000)
	case f >= 999_950:
		return fmt.Sprintf("%.1fM", f/1_000_000)
	case f >= 1_000:
		return fmt.Sprintf("%.1fK", f/1_000)
	default:
		return fmt.Sprintf("%d", n)
	}
}

// terminalWidth returns the writer's terminal width in columns, or 0 when it is
// unknown (not an *os.File, or GetSize failed) — in which case the caller skips
// truncation. A test override (termWidth) takes precedence.
func (p *ProgressReporter) terminalWidth() int {
	if p.termWidth > 0 {
		return p.termWidth
	}
	f, ok := p.w.(*os.File)
	if !ok {
		return 0
	}
	w, _, err := term.GetSize(int(f.Fd()))
	if err != nil {
		return 0
	}
	return w
}

// truncateVisible returns s limited to at most max visible columns, passing ANSI
// SGR escape sequences through uncounted so color codes are preserved. A reset is
// appended when color is enabled so a cut inside a colored run does not bleed.
func truncateVisible(s string, max int) string {
	if max < 0 {
		max = 0
	}
	if visibleWidth(s) <= max {
		return s
	}
	var b strings.Builder
	w, inEsc := 0, false
	for _, r := range s {
		switch {
		case inEsc:
			b.WriteRune(r)
			if r == 'm' {
				inEsc = false
			}
		case r == '\x1b':
			inEsc = true
			b.WriteRune(r)
		default:
			if w >= max {
				// Visible budget spent; drop the rest (trailing glyphs/escapes).
				if ColorEnabled() {
					b.WriteString(Reset)
				}
				return b.String()
			}
			b.WriteRune(r)
			w++
		}
	}
	if ColorEnabled() {
		b.WriteString(Reset)
	}
	return b.String()
}

// visibleWidth returns the number of visible columns in s, skipping ANSI SGR
// escape sequences (\x1b[...m). Used so colored lines are padded and cleared by
// their on-screen width rather than their byte length.
func visibleWidth(s string) int {
	w, inEsc := 0, false
	for _, r := range s {
		switch {
		case inEsc:
			if r == 'm' {
				inEsc = false
			}
		case r == '\x1b':
			inEsc = true
		default:
			w++
		}
	}
	return w
}
