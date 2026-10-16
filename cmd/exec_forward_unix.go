//go:build !windows

package cmd

import (
	"context"
	"fmt"
	"path/filepath"
	"syscall"
)

// execForward replaces the dtctl process with the target binary (a dtctl-* plugin) so it owns the terminal end to end — raw mode, signals,
// exit code — with no parent in between. It only returns on failure to exec;
// the exit code in the return value is meaningful only on Windows, where
// exec is emulated with a child process. The in-flight OTel span is
// intentionally abandoned — the target process owns the invocation from here.
func execForward(_ context.Context, bin string, argv []string, env []string) (int, error) {
	if err := syscall.Exec(bin, append([]string{filepath.Base(bin)}, argv...), env); err != nil {
		return 1, fmt.Errorf("failed to launch %s: %w", bin, err)
	}
	return 0, nil // unreachable: Exec does not return on success
}
