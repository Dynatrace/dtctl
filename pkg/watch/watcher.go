package watch

import (
	"context"
	"log"
	"reflect"
	"regexp"
	"strconv"
	"time"

	"github.com/Dynatrace/dtctl/pkg/output"
)

func NewWatcher(opts WatcherOptions) *Watcher {
	if opts.Interval < time.Second {
		opts.Interval = time.Second
	}

	return &Watcher{
		interval:    opts.Interval,
		client:      opts.Client,
		fetcher:     opts.Fetcher,
		differ:      NewDiffer(),
		printer:     opts.Printer,
		stopCh:      make(chan struct{}),
		showInitial: opts.ShowInitial,
	}
}

func (w *Watcher) Start(ctx context.Context) error {
	ticker := time.NewTicker(w.interval)
	defer ticker.Stop()

	if err := w.poll(ctx, true); err != nil {
		return err
	}

	for {
		select {
		case <-ctx.Done():
			return nil
		case <-w.stopCh:
			return nil
		case <-ticker.C:
			if err := w.poll(ctx, false); err != nil {
				if isTransient(err) {
					log.Printf("Warning: Temporary error, retrying: %v\n", err)
					if !w.sleep(ctx, w.interval) {
						return nil
					}
					continue
				}
				if isRateLimited(err) {
					backoff := extractRetryAfter(err)
					if backoff <= 0 {
						backoff = w.interval * 2
					}
					if !w.sleep(ctx, backoff) {
						return nil
					}
					continue
				}
				if isNetworkError(err) {
					log.Printf("Warning: Connection lost, retrying...\n")
					if !w.sleep(ctx, w.interval*2) {
						return nil
					}
					continue
				}
				return err
			}
		}
	}
}

// sleep waits for d, returning false if the watcher was cancelled
// (via ctx or Stop) during the wait.
func (w *Watcher) sleep(ctx context.Context, d time.Duration) bool {
	if d <= 0 {
		return true
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return true
	case <-ctx.Done():
		return false
	case <-w.stopCh:
		return false
	}
}

func (w *Watcher) Stop() {
	w.stopOnce.Do(func() { close(w.stopCh) })
}

func (w *Watcher) poll(ctx context.Context, initial bool) error {
	result, err := w.fetcher()
	if err != nil {
		return err
	}

	resources, err := normalizeToSlice(result)
	if err != nil {
		return err
	}

	if initial {
		// Always seed the differ's baseline on the initial poll, so that
		// the next poll has something to diff against. Without this,
		// --watch-only would print every existing resource as ADDED.
		w.differ.Detect(resources)
		if w.showInitial && w.printer != nil {
			return w.printer.PrintList(resources)
		}
		return nil
	}

	changes := w.differ.Detect(resources)

	// Always print the full table with change indicators
	if w.printer != nil {
		watchPrinter, ok := w.printer.(output.WatchPrinterInterface)
		if ok {
			return watchPrinter.PrintChanges(changes)
		}
		// Fallback for non-watch printers - only print actual changes
		for _, change := range changes {
			if change.Type != "" {
				if err := w.printer.Print(change.Resource); err != nil {
					return err
				}
			}
		}
	}

	return nil
}

func normalizeToSlice(result interface{}) ([]interface{}, error) {
	if result == nil {
		return []interface{}{}, nil
	}

	switch v := result.(type) {
	case []interface{}:
		return v, nil
	case []map[string]interface{}:
		slice := make([]interface{}, len(v))
		for i, item := range v {
			slice[i] = item
		}
		return slice, nil
	default:
		// Use reflection to handle any slice type
		rv := reflect.ValueOf(result)
		if rv.Kind() == reflect.Slice {
			slice := make([]interface{}, rv.Len())
			for i := 0; i < rv.Len(); i++ {
				slice[i] = rv.Index(i).Interface()
			}
			return slice, nil
		}
		return []interface{}{result}, nil
	}
}

func isTransient(err error) bool {
	if err == nil {
		return false
	}
	errStr := err.Error()
	return contains(errStr, "timeout") ||
		contains(errStr, "temporary") ||
		contains(errStr, "connection reset")
}

func isRateLimited(err error) bool {
	if err == nil {
		return false
	}
	errStr := err.Error()
	return contains(errStr, "rate limit") ||
		contains(errStr, "429") ||
		contains(errStr, "too many requests")
}

func isNetworkError(err error) bool {
	if err == nil {
		return false
	}
	errStr := err.Error()
	return contains(errStr, "connection refused") ||
		contains(errStr, "no such host") ||
		contains(errStr, "network unreachable")
}

// retryAfterPattern matches "Retry-After: <seconds>" in an error message,
// case-insensitively. Dynatrace clients surface this as part of the rate-limit
// error string. The header value is in seconds per RFC 7231.
// Note: RFC 7231 also allows an HTTP-date value (e.g. "Thu, 01 Jan 2026 …");
// that form is not handled here. Dynatrace only ever sends the integer form.
var retryAfterPattern = regexp.MustCompile(`(?i)retry[-_ ]?after[:=\s]+(\d+)`)

func extractRetryAfter(err error) time.Duration {
	if err == nil {
		return 0
	}
	m := retryAfterPattern.FindStringSubmatch(err.Error())
	if len(m) < 2 {
		return 0
	}
	secs, parseErr := strconv.Atoi(m[1])
	if parseErr != nil || secs <= 0 {
		return 0
	}
	// Cap the back-off so a hostile or buggy server can't pin us forever.
	if secs > 300 {
		secs = 300
	}
	return time.Duration(secs) * time.Second
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > len(substr) && findSubstring(s, substr))
}

func findSubstring(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
