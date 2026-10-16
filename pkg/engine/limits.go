package engine

import (
	"context"
	"time"
)

// Limits controls resource budgets for a single engine execution.
type Limits struct {
	// MaxQueued bounds how many requests may be waiting for or holding an
	// execution slot at once. Exceeding it returns ErrTooManyQueued. An Engine
	// built with WithConcurrentExecution(n) has n slots, and raises MaxQueued
	// to n if it is lower.
	MaxQueued int
	// MaxDuration bounds the whole request (queue wait + execution): it is
	// applied as a context.WithTimeout threaded into the command tree via
	// RunOptions.Context. Cancellation is cooperative — see the package
	// doc's "Cancellation" section for which commands actually observe it.
	MaxDuration time.Duration
	// MaxOutputBytes caps stdout and stderr independently (so the real
	// combined worst case is 2x this value); excess bytes are dropped and
	// Result.Truncated is set.
	MaxOutputBytes int64
	// MaxFileBytes caps the total size of Request.Files (summed) rejected
	// up front, before admission control, with no queue slot consumed. It
	// does NOT bound growth of the virtual filesystem during execution — a
	// command that writes a large file back (e.g. apply --write-id) is not
	// capped by this field today.
	MaxFileBytes int64
}

// DefaultLimits returns conservative defaults for production use.
func DefaultLimits() Limits {
	return Limits{
		MaxQueued:      4,
		MaxDuration:    5 * time.Minute,
		MaxOutputBytes: 32 * 1024 * 1024, // 32 MiB
		MaxFileBytes:   16 * 1024 * 1024, // 16 MiB
	}
}

// ExecuteWithLimits runs one dtctl invocation with explicit resource budgets.
// The limits govern queue depth, duration (via context timeout on the command
// tree), and stdout/stderr size (via capped buffers).
//
// A zero value in any field is filled from DefaultLimits rather than taken
// literally: a caller that only cares about, say, MaxOutputBytes and leaves
// the rest at Go's zero value would otherwise get MaxQueued=0 (every request
// rejected as "too many queued") and MaxDuration=0 (every request cancelled
// before it starts). There is currently no way to make a budget unbounded;
// pass DefaultLimits()'s own (generous) values back if a caller needs to
// widen just one field.
func ExecuteWithLimits(ctx context.Context, req Request, limits Limits) (*Result, error) {
	return executeInner(ctx, req, limits.withDefaults(), defaultAdmission, false)
}

// withDefaults fills zero-value fields from DefaultLimits().
func (l Limits) withDefaults() Limits {
	return l.withDefaultsFrom(DefaultLimits())
}

// withDefaultsFrom fills zero-value fields from d, a budget that already
// carries a value in every field.
func (l Limits) withDefaultsFrom(d Limits) Limits {
	if l.MaxQueued == 0 {
		l.MaxQueued = d.MaxQueued
	}
	if l.MaxDuration == 0 {
		l.MaxDuration = d.MaxDuration
	}
	if l.MaxOutputBytes == 0 {
		l.MaxOutputBytes = d.MaxOutputBytes
	}
	if l.MaxFileBytes == 0 {
		l.MaxFileBytes = d.MaxFileBytes
	}
	return l
}
