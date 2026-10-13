// Package usage keeps dtctl's opt-in local usage ledger: a per-day tally of the
// DQL queries this machine's dtctl ran and the data they scanned.
//
// The ledger exists because a user without access to the tenant's billing
// events (`fetch dt.system.events | filter event.kind == "BILLING_USAGE_EVENT"`
// needs storage:system:read, which cannot be scoped to one user) otherwise has
// no running total of what their own scripts and agents consume. It is
// deliberately minimal:
//
//   - opt-in per context (`usage-tracking: true`); with it off nothing is
//     written, not even an empty file
//   - aggregate counters only, keyed by (UTC date, context name): query count,
//     failed-query count, scanned bytes, records and data points. Never query
//     text, results, user identity, environment URLs or tokens
//   - one small JSON file under the XDG state directory, mode 0600, updated
//     under a cross-process lock with an atomic temp-file + rename, and pruned
//     to the last RetentionDays days on every write
//   - best effort: recording never fails or noticeably delays the command that
//     ran the query. Any error skips the update.
//
// It counts only what dtctl itself ran on this machine: queries from the web
// UI, notebooks, dashboards, apps, MCP servers or other machines are not in
// it. Billing events remain the authoritative source.
package usage

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/dynatrace-oss/dtctl/pkg/config"
)

const (
	// FileName is the ledger's file name inside the dtctl state directory.
	FileName = "usage.json"
	// RetentionDays is how many UTC days the ledger keeps, today included.
	// Older days are pruned on every write.
	RetentionDays = 90
	// schemaVersion is the ledger format this package reads and writes. A
	// file with a newer version is left untouched rather than rewritten.
	schemaVersion = 1
	// dateLayout keys each day of the ledger (UTC).
	dateLayout = "2006-01-02"
	// defaultLockTimeout bounds how long a writer waits for a concurrent one.
	// An update holds the lock for well under a millisecond, so this is only
	// ever reached when something is wrong; the update is then skipped.
	defaultLockTimeout = 2 * time.Second
	lockRetryInterval  = 5 * time.Millisecond
)

// BillingHint names the authoritative source of consumption data. Help text
// and command output repeat it so nobody mistakes the local tally for a bill.
const BillingHint = `fetch dt.system.events | filter event.kind == "BILLING_USAGE_EVENT"`

// Sample is what one DQL execution contributes to the ledger.
type Sample struct {
	// Failed marks an execution that did not return a result (an error or a
	// cancellation). It still counts as a query, and whatever the backend
	// reported as scanned before it ended is still added.
	Failed            bool
	ScannedBytes      int64
	ScannedRecords    int64
	ScannedDataPoints int64
}

// Counters is the aggregate for one context on one day, or a sum of them.
type Counters struct {
	Queries           int64 `json:"queries"`
	Failed            int64 `json:"failed,omitempty"`
	ScannedBytes      int64 `json:"scannedBytes"`
	ScannedRecords    int64 `json:"scannedRecords"`
	ScannedDataPoints int64 `json:"scannedDataPoints,omitempty"`
}

// Add folds o into c.
func (c *Counters) Add(o Counters) {
	c.Queries += o.Queries
	c.Failed += o.Failed
	c.ScannedBytes += o.ScannedBytes
	c.ScannedRecords += o.ScannedRecords
	c.ScannedDataPoints += o.ScannedDataPoints
}

func (c *Counters) addSample(s Sample) {
	c.Queries++
	if s.Failed {
		c.Failed++
	}
	c.ScannedBytes += nonNegative(s.ScannedBytes)
	c.ScannedRecords += nonNegative(s.ScannedRecords)
	c.ScannedDataPoints += nonNegative(s.ScannedDataPoints)
}

func nonNegative(v int64) int64 {
	if v < 0 {
		return 0
	}
	return v
}

// Days maps a UTC date ("2006-01-02") to the counters of each context name.
type Days map[string]map[string]Counters

// file is the on-disk shape of the ledger.
type file struct {
	Version int  `json:"version"`
	Days    Days `json:"days"`
}

// ErrNewerFormat reports a ledger written by a newer dtctl. It is never
// rewritten, so a downgrade cannot destroy the newer data.
var ErrNewerFormat = errors.New("usage ledger was written by a newer dtctl version")

// Ledger is the usage file at one path.
type Ledger struct {
	path        string
	now         func() time.Time
	lockTimeout time.Duration
}

// DefaultPath is where the ledger lives: $XDG_STATE_HOME/dtctl/usage.json
// (typically ~/.local/state/dtctl/usage.json). It is state rather than cache
// or config: losing it is harmless, but it is not regenerable.
func DefaultPath() string {
	return filepath.Join(config.StateDir(), FileName)
}

// New returns the ledger at path.
func New(path string) *Ledger {
	return &Ledger{path: path, now: time.Now, lockTimeout: defaultLockTimeout}
}

// WithClock overrides the clock (tests).
func (l *Ledger) WithClock(now func() time.Time) *Ledger {
	l.now = now
	return l
}

// Path is the ledger file's location.
func (l *Ledger) Path() string { return l.path }

// Today is the UTC date the ledger files a sample under right now.
func (l *Ledger) Today() string { return l.now().UTC().Format(dateLayout) }

// Record adds one query execution for contextName to today's counters and
// prunes days beyond the retention window, all under the ledger lock.
func (l *Ledger) Record(contextName string, s Sample) error {
	if contextName == "" {
		return errors.New("usage ledger: no context name")
	}
	return l.update(func(days Days) bool {
		today := l.Today()
		perCtx := days[today]
		if perCtx == nil {
			perCtx = map[string]Counters{}
			days[today] = perCtx
		}
		c := perCtx[contextName]
		c.addSample(s)
		perCtx[contextName] = c
		return true
	})
}

// Reset removes contextName's counters from every day, or the whole ledger
// when contextName is empty. It reports whether anything was removed.
func (l *Ledger) Reset(contextName string) (bool, error) {
	if !l.exists() {
		return false, nil // nothing was ever recorded; create nothing either
	}
	if contextName == "" {
		// Under the lock, so a writer that read the old contents cannot put
		// them back after the file is gone.
		unlock, err := acquireLock(l.path+".lock", l.lockTimeout)
		if err != nil {
			return false, err
		}
		defer unlock()
		err = os.Remove(l.path)
		if errors.Is(err, fs.ErrNotExist) {
			return false, nil
		}
		return err == nil, err
	}
	removed := false
	err := l.update(func(days Days) bool {
		for date, perCtx := range days {
			if _, ok := perCtx[contextName]; ok {
				delete(perCtx, contextName)
				removed = true
			}
			if len(perCtx) == 0 {
				delete(days, date)
			}
		}
		return removed
	})
	return removed, err
}

// Read returns the ledger's days. A missing file is an empty ledger, not an
// error, and reading one creates nothing: with tracking off, even `dtctl
// usage` leaves the disk untouched.
func (l *Ledger) Read() (Days, error) {
	if !l.exists() {
		return Days{}, nil
	}
	unlock, err := acquireLock(l.path+".lock", l.lockTimeout)
	if err != nil {
		// Reading without the lock is still safe: writers replace the file
		// atomically, so a reader sees either the old or the new version.
		// The lock only keeps a Windows reader from holding the file open
		// across a writer's rename.
		return l.load()
	}
	defer unlock()
	return l.load()
}

func (l *Ledger) exists() bool {
	_, err := os.Stat(l.path)
	return err == nil
}

// update runs mutate on the current contents under the lock and writes the
// result back atomically when mutate reports a change.
func (l *Ledger) update(mutate func(Days) bool) error {
	if err := os.MkdirAll(filepath.Dir(l.path), 0o700); err != nil {
		return fmt.Errorf("usage ledger: create directory: %w", err)
	}
	unlock, err := acquireLock(l.path+".lock", l.lockTimeout)
	if err != nil {
		return err
	}
	defer unlock()

	days, err := l.load()
	if errors.Is(err, ErrNewerFormat) {
		return err
	}
	if err != nil {
		// A corrupt ledger holds nothing worth keeping more than the update
		// in hand; start over rather than stop counting for good.
		days = Days{}
	}
	changed := mutate(days)
	if prune(days, l.now()) {
		changed = true
	}
	if !changed {
		return nil
	}
	return l.write(days)
}

func (l *Ledger) load() (Days, error) {
	data, err := os.ReadFile(l.path)
	if errors.Is(err, fs.ErrNotExist) {
		return Days{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("usage ledger: read: %w", err)
	}
	var f file
	if err := json.Unmarshal(data, &f); err != nil {
		return nil, fmt.Errorf("usage ledger: %s is not valid JSON: %w", l.path, err)
	}
	if f.Version > schemaVersion {
		return nil, ErrNewerFormat
	}
	if f.Days == nil {
		f.Days = Days{}
	}
	return f.Days, nil
}

// write replaces the ledger atomically: a temp file in the same directory,
// renamed over the old one, so a concurrent reader never sees half a file.
func (l *Ledger) write(days Days) error {
	data, err := json.Marshal(file{Version: schemaVersion, Days: days})
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(l.path), "."+FileName+".*.tmp")
	if err != nil {
		return fmt.Errorf("usage ledger: %w", err)
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }() // no-op once renamed
	// os.CreateTemp creates the file 0600, and the rename keeps that mode.
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("usage ledger: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("usage ledger: %w", err)
	}
	if err := os.Rename(tmpName, l.path); err != nil {
		return fmt.Errorf("usage ledger: %w", err)
	}
	return nil
}

// prune drops days outside the retention window, and any key that is not a
// date at all. It reports whether anything was dropped.
func prune(days Days, now time.Time) bool {
	cutoff := startOfDay(now).AddDate(0, 0, -(RetentionDays - 1))
	changed := false
	for date := range days {
		d, err := time.Parse(dateLayout, date)
		if err != nil || d.Before(cutoff) {
			delete(days, date)
			changed = true
		}
	}
	return changed
}

func startOfDay(t time.Time) time.Time {
	t = t.UTC()
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC)
}

// Window is a span of whole UTC days ending today.
type Window struct {
	// Name is the window's label ("today", "7d", "30d").
	Name string
	// Days is how many UTC days it covers, today included.
	Days int
}

// Windows are the spans `dtctl usage` reports.
var Windows = []Window{{"today", 1}, {"7d", 7}, {"30d", 30}}

// Total is one window's sum for one context.
type Total struct {
	Window Window
	// From and To are the first and last UTC date the window covers.
	From, To string
	Counters
}

// Summarize sums contextName's counters over each window ending at now.
func Summarize(days Days, contextName string, now time.Time, windows []Window) []Total {
	today := startOfDay(now)
	out := make([]Total, 0, len(windows))
	for _, w := range windows {
		from := today.AddDate(0, 0, -(w.Days - 1))
		t := Total{Window: w, From: from.Format(dateLayout), To: today.Format(dateLayout)}
		for date, perCtx := range days {
			d, err := time.Parse(dateLayout, date)
			if err != nil || d.Before(from) || d.After(today) {
				continue
			}
			if c, ok := perCtx[contextName]; ok {
				t.Add(c)
			}
		}
		out = append(out, t)
	}
	return out
}

// Contexts lists every context name with counters in days, sorted.
func Contexts(days Days) []string {
	seen := map[string]bool{}
	for _, perCtx := range days {
		for name := range perCtx {
			seen[name] = true
		}
	}
	names := make([]string, 0, len(seen))
	for name := range seen {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// FormatBytes renders a scanned-data volume in binary units (GiB, MiB, ...),
// the unit Grail reports scan volume in. It never renders a currency: what a
// GiB costs depends on the contract.
func FormatBytes(b int64) string {
	const unit = 1024
	if b < unit {
		return fmt.Sprintf("%d B", b)
	}
	div, exp := int64(unit), 0
	for n := b / unit; n >= unit && exp < 5; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.2f %ciB", float64(b)/float64(div), "KMGTPE"[exp])
}

// Recorder files samples for one context in a ledger, best effort: errors are
// reported to debug (when set) and otherwise dropped, because a failed tally
// must never fail or delay the query that produced it.
type Recorder struct {
	Ledger  *Ledger
	Context string
	// Debug, when non-nil, receives one line per skipped update.
	Debug io.Writer
}

// RecordQuery files s. It never fails.
func (r *Recorder) RecordQuery(s Sample) {
	if r == nil || r.Ledger == nil {
		return
	}
	if err := r.Ledger.Record(r.Context, s); err != nil && r.Debug != nil {
		fmt.Fprintf(r.Debug, "usage tracking: skipped recording a query: %v\n", err)
	}
}
