//go:build windows

package usage

import (
	"errors"
	"fmt"
	"os"
	"time"

	"golang.org/x/sys/windows"
)

// acquireLock takes an exclusive LockFileEx byte-range lock on lockPath,
// retrying without blocking until timeout; the Windows counterpart of the
// flock(2) lock in lock_unix.go. The lock is per handle and every acquisition
// opens its own, so goroutines and processes contend alike.
func acquireLock(lockPath string, timeout time.Duration) (func(), error) {
	f, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDONLY, 0o600)
	if err != nil {
		return nil, fmt.Errorf("usage ledger: open lock: %w", err)
	}
	handle := windows.Handle(f.Fd())
	deadline := time.Now().Add(timeout)
	for {
		ol := new(windows.Overlapped)
		lockErr := windows.LockFileEx(handle,
			windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY,
			0, 1, 0, ol)
		if lockErr == nil {
			break
		}
		if !errors.Is(lockErr, windows.ERROR_LOCK_VIOLATION) {
			_ = f.Close()
			return nil, fmt.Errorf("usage ledger: lock: %w", lockErr)
		}
		if time.Now().After(deadline) {
			_ = f.Close()
			return nil, fmt.Errorf("usage ledger: timed out after %s waiting for the lock", timeout)
		}
		time.Sleep(lockRetryInterval)
	}
	return func() {
		ol := new(windows.Overlapped)
		_ = windows.UnlockFileEx(handle, 0, 1, 0, ol)
		_ = f.Close()
	}, nil
}
