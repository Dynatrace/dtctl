package reposcope

import (
	"io/fs"
	"os"
	"path/filepath"
	"testing"
	"testing/fstest"

	"github.com/stretchr/testify/require"
)

func TestSaveIOFailures(t *testing.T) {
	t.Run("missing parent", func(t *testing.T) {
		root := newRepo(t)
		loc := Location{Path: filepath.Join(root, "missing", FileName)}
		require.ErrorIs(t, Save(loc, exampleFile()), fs.ErrNotExist)
		assertOnlyEntries(t, root, ".git")
	})
	t.Run("cannot replace directory", func(t *testing.T) {
		root := newRepo(t)
		loc := Location{Path: filepath.Join(root, FileName)}
		require.NoError(t, os.Mkdir(loc.Path, 0o755))
		keep := filepath.Join(loc.Path, "keep")
		require.NoError(t, os.WriteFile(keep, []byte("unchanged"), 0o644))
		require.ErrorContains(t, Save(loc, exampleFile()), "write "+loc.Path)
		assertOnlyEntries(t, root, ".git", FileName)
		data, err := os.ReadFile(keep)
		require.NoError(t, err)
		require.Equal(t, "unchanged", string(data))
		require.ErrorContains(t, Save(loc, &File{}), "remove "+loc.Path)
		require.FileExists(t, keep)
	})
}

// Stat succeeds, but opening or reading the file fails (for example if it
// disappears or permissions change between the two operations).
type failingReadFS struct {
	fstest.MapFS
	failOpen bool
}

func (f failingReadFS) Open(name string) (fs.File, error) {
	if f.failOpen {
		return nil, fs.ErrPermission
	}
	file, err := f.MapFS.Open(name)
	if err != nil {
		return nil, err
	}
	return failingReadFile{file}, nil
}

type failingReadFile struct{ fs.File }

func (f failingReadFile) Read([]byte) (int, error) { return 0, fs.ErrPermission }

func TestReadRegularIOFailures(t *testing.T) {
	for _, failOpen := range []bool{true, false} {
		data, err := readRegular(failingReadFS{fstest.MapFS{"go.mod": {Data: []byte("module checkout")}}, failOpen}, "go.mod")
		require.ErrorIs(t, err, fs.ErrPermission)
		require.Nil(t, data)
	}
}
