// Package vfs is the file-access seam between dtctl commands and the
// filesystem. CLI invocations read and write the host filesystem directly
// (the default). Embedded invocations (the service engine, `dtctl serve`)
// install a per-request FS — typically a MapFS built from the request's
// virtual files — so `apply -f x.yaml` works against files that exist only
// in the request, and writebacks (e.g. `apply --write-id`) land back in the
// request instead of on the host (docs/dev/SERVICE_ENGINE_DESIGN.md).
//
// Only user-supplied paths go through this seam. Internal scratch files
// (editor round-trip temp files, spill buffers, caches) deliberately stay on
// the host filesystem: they are implementation detail, not request state.
package vfs

import (
	"fmt"
	"io"
	"io/fs"
	"os"
)

// FS is the minimal file-access contract dtctl commands need for
// user-supplied paths: whole-file reads and whole-file writes.
type FS interface {
	ReadFile(name string) ([]byte, error)
	WriteFile(name string, data []byte, perm fs.FileMode) error
}

// osFS is the host filesystem.
type osFS struct{}

func (osFS) ReadFile(name string) ([]byte, error) { return os.ReadFile(name) }
func (osFS) WriteFile(name string, data []byte, perm fs.FileMode) error {
	return os.WriteFile(name, data, perm)
}

// active is the installed implementation. Package-level (rather than
// threaded through every call chain) because dtctl serializes invocations —
// see cmd.Run — and the swap happens under that serialization.
var active FS = osFS{}

// SetActive installs f as the file-access implementation and returns the
// previous one so callers can restore it. nil selects the host filesystem.
// Callers own serialization: install/restore must not race with an executing
// invocation.
func SetActive(f FS) FS {
	prev := active
	if f == nil {
		f = osFS{}
	}
	active = f
	return prev
}

// Env is one invocation's view of the host for user-supplied paths: the
// filesystem they resolve against and the stream "-" reads from. An embedder
// that runs invocations concurrently hands each its own Env, because the
// package-level state below is swapped for the whole process and so can serve
// only one invocation at a time.
//
// The zero value is the process default: the installed FS and os.Stdin, so a
// caller that never sets one behaves exactly as before.
type Env struct {
	FS    FS
	Stdin io.Reader
}

func (e Env) fs() FS {
	if e.FS != nil {
		return e.FS
	}
	return active
}

// StdinReader is the stream "-" reads from: e's, else the process's.
func (e Env) StdinReader() io.Reader {
	if e.Stdin != nil {
		return e.Stdin
	}
	return os.Stdin
}

// ReadFile reads a user-supplied path through e's FS.
func (e Env) ReadFile(name string) ([]byte, error) { return e.fs().ReadFile(name) }

// WriteFile writes a user-supplied path through e's FS.
func (e Env) WriteFile(name string, data []byte, perm fs.FileMode) error {
	return e.fs().WriteFile(name, data, perm)
}

// ReadFileOrStdin reads name through e's FS, with "-" meaning e's stdin.
func (e Env) ReadFileOrStdin(name string) ([]byte, error) {
	if name == "-" {
		content, err := io.ReadAll(e.StdinReader())
		if err != nil {
			return nil, fmt.Errorf("failed to read from stdin: %w", err)
		}
		return content, nil
	}
	return e.ReadFile(name)
}

// ReadFile reads a user-supplied path through the active FS.
func ReadFile(name string) ([]byte, error) { return Env{}.ReadFile(name) }

// WriteFile writes a user-supplied path through the active FS.
func WriteFile(name string, data []byte, perm fs.FileMode) error {
	return Env{}.WriteFile(name, data, perm)
}

// ReadFileOrStdin reads name through the active FS, with "-" meaning the
// process stdin. It is the shared implementation behind the CLI's
// file-or-stdin flag convention.
func ReadFileOrStdin(name string) ([]byte, error) { return Env{}.ReadFileOrStdin(name) }
