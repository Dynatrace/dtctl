package vfs

import (
	"io/fs"
	"path"
	"strings"
	"sync"
)

// MapFS is an in-memory FS for embedded invocations: the request's virtual
// files go in, and anything the invocation writes (writebacks, generated
// files) can be read back out with Files. Safe for concurrent use.
//
// Paths are normalized with path.Clean and a stripped leading "/", so
// "./x.yaml", "x.yaml" and "/x.yaml" address the same entry — a request's
// virtual namespace has no meaningful root or working directory.
type MapFS struct {
	mu    sync.RWMutex
	files map[string][]byte
}

// NewMapFS builds a MapFS from the given files (copied; the input map is not
// retained). A nil map yields an empty filesystem.
func NewMapFS(files map[string][]byte) *MapFS {
	m := &MapFS{files: make(map[string][]byte, len(files))}
	for name, data := range files {
		m.files[Key(name)] = append([]byte(nil), data...)
	}
	return m
}

// Key is the name a MapFS keeps a file under: "x.yaml", "./x.yaml" and
// "/x.yaml" are one file. A caller matching paths against the files it hands a
// MapFS uses it instead of a rule of its own.
func Key(name string) string {
	return strings.TrimPrefix(path.Clean("/"+name), "/")
}

func (m *MapFS) ReadFile(name string) ([]byte, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	data, ok := m.files[Key(name)]
	if !ok {
		return nil, &fs.PathError{Op: "open", Path: name, Err: fs.ErrNotExist}
	}
	return append([]byte(nil), data...), nil
}

func (m *MapFS) WriteFile(name string, data []byte, _ fs.FileMode) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.files[Key(name)] = append([]byte(nil), data...)
	return nil
}

// Files returns a snapshot of the current contents, keyed by normalized
// path. Embedders use it to hand written files back to the request's caller.
func (m *MapFS) Files() map[string][]byte {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make(map[string][]byte, len(m.files))
	for name, data := range m.files {
		out[name] = append([]byte(nil), data...)
	}
	return out
}
