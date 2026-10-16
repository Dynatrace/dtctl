package cmd

import (
	"context"
	"math"

	"golang.org/x/sync/semaphore"
)

// runLock is the lock behind runMu: held shared by concurrent invocations and
// exclusively by serialized ones.
//
// It is a weighted semaphore rather than a sync.RWMutex because a concurrent
// invocation's wait for it has to end with the invocation's context, and a wait
// for a sync.RWMutex cannot be abandoned: giving up on one left a goroutine
// blocked in RLock, and another waiting to release what it eventually acquired,
// for as long as the serialized invocation ran, two for every request that gave
// up. A semaphore waiter whose context ends leaves the queue and holds nothing.
//
// The queue is first in, first out, so a serialized invocation waiting for
// concurrent ones to finish holds back the concurrent ones that arrive after
// it, as a waiting writer does for a sync.RWMutex, and they run once it is done.
type runLock struct {
	sem *semaphore.Weighted
}

// runLockExclusive is what a serialized invocation takes: the whole lock.
const runLockExclusive = math.MaxInt64

func newRunLock() *runLock {
	return &runLock{sem: semaphore.NewWeighted(runLockExclusive)}
}

// Lock takes the lock exclusively, waiting for as long as that takes.
func (l *runLock) Lock() {
	// Cannot fail: the context never ends.
	_ = l.sem.Acquire(context.Background(), runLockExclusive)
}

// Unlock releases an exclusive hold.
func (l *runLock) Unlock() {
	l.sem.Release(runLockExclusive)
}

// RLock takes the lock shared. When a serialized invocation holds the lock or
// waits for it, RLock waits too, and returns ctx's error, holding nothing, if
// ctx ends first. An uncontended lock is taken whatever ctx's state, as a
// sync.RWMutex would.
func (l *runLock) RLock(ctx context.Context) error {
	if l.sem.TryAcquire(1) {
		return nil
	}
	return l.sem.Acquire(ctx, 1)
}

// RUnlock releases a shared hold.
func (l *runLock) RUnlock() {
	l.sem.Release(1)
}
