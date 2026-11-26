//go:build !windows

package usage

import (
	"errors"
	"fmt"
	"os"
	"time"

	"golang.org/x/sys/unix"
)

// acquireLock takes an exclusive flock(2) on lockPath, retrying without
// blocking until timeout. The lock file sits next to the ledger in the user's
// own state directory, is opened with O_NOFOLLOW and mode 0600, and is never
// written: it is purely a synchronisation primitive. A separate file (rather
// than the ledger itself) is what lets writers replace the ledger by rename
// while others wait on a stable inode.
func acquireLock(lockPath string, timeout time.Duration) (func(), error) {
	f, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDONLY|unix.O_NOFOLLOW, 0o600)
	if err != nil {
		return nil, fmt.Errorf("usage ledger: open lock: %w", err)
	}
	deadline := time.Now().Add(timeout)
	for {
		lockErr := unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB)
		if lockErr == nil {
			break
		}
		if !errors.Is(lockErr, unix.EWOULDBLOCK) {
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
		_ = unix.Flock(int(f.Fd()), unix.LOCK_UN)
		_ = f.Close()
	}, nil
}
