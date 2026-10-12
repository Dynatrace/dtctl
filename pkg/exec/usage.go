package exec

import (
	"runtime"
	"sync"
	"weak"

	"github.com/dynatrace-oss/dtctl/pkg/client"
	"github.com/dynatrace-oss/dtctl/pkg/usage"
)

// UsageRecorder receives one sample per DQL execution: the scan totals the
// backend reported, whether or not the query succeeded. It must not block or
// fail the query; usage.Recorder is the ledger-backed implementation.
type UsageRecorder interface {
	RecordQuery(usage.Sample)
}

// usageRecorders binds a recorder to the client a command built for a
// context with usage tracking on, so every executor on that client records
// (`dtctl query`, `wait`, `inventory`, lookups, ...) without each construction
// site having to know about tracking. Keyed weakly: a binding lives only as
// long as its client, so a long-lived process that builds many clients does
// not accumulate them. Nothing is bound unless the command layer opted in.
var usageRecorders sync.Map // weak.Pointer[client.Client] -> UsageRecorder

// TrackUsage makes every DQL executor built on c from now on report to r.
func TrackUsage(c *client.Client, r UsageRecorder) {
	if c == nil || r == nil {
		return
	}
	key := weak.Make(c)
	if _, loaded := usageRecorders.Swap(key, r); !loaded {
		runtime.AddCleanup(c, func(k weak.Pointer[client.Client]) { usageRecorders.Delete(k) }, key)
	}
}

func usageRecorderFor(c *client.Client) UsageRecorder {
	if c == nil {
		return nil
	}
	if r, ok := usageRecorders.Load(weak.Make(c)); ok {
		return r.(UsageRecorder)
	}
	return nil
}

// WithUsageRecorder sets the recorder this executor reports to, replacing any
// bound to its client. nil turns recording off.
func (e *DQLExecutor) WithUsageRecorder(r UsageRecorder) *DQLExecutor {
	e.usage = r
	return e
}

// recordUsage files one execution. A nil result means the query failed or was
// cancelled; the live totals from the last poll are then all there is.
func (e *DQLExecutor) recordUsage(result *DQLQueryResponse, failed bool, liveBytes, liveRecords int64) {
	if e.usage == nil {
		return
	}
	s := usage.Sample{Failed: failed, ScannedBytes: liveBytes, ScannedRecords: liveRecords}
	if result != nil {
		if m := result.GetMetadata(); m != nil {
			if m.ScannedBytes > 0 {
				s.ScannedBytes = m.ScannedBytes
			}
			if m.ScannedRecords > 0 {
				s.ScannedRecords = m.ScannedRecords
			}
			s.ScannedDataPoints = m.ScannedDataPoints
		}
	}
	e.usage.RecordQuery(s)
}
