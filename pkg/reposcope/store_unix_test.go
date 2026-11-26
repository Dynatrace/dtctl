//go:build unix

package reposcope

import (
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A clone can bring any kind of file under the scope file's name. Only a
// regular file within the size cap is read; anything else is an invalid file,
// which a query reports and runs unscoped instead of hanging on.
func TestLoad_OnlyARegularFile(t *testing.T) {
	tests := map[string]func(t *testing.T, path string){
		"a symlink to /dev/zero": func(t *testing.T, path string) {
			require.NoError(t, os.Symlink("/dev/zero", path))
		},
		"a symlink to a regular file": func(t *testing.T, path string) {
			target := filepath.Join(t.TempDir(), "elsewhere.yaml")
			require.NoError(t, os.WriteFile(target, readFixture(t, "example.yaml"), 0o644))
			require.NoError(t, os.Symlink(target, path))
		},
		"a FIFO": func(t *testing.T, path string) {
			require.NoError(t, syscall.Mkfifo(path, 0o644))
		},
		"a directory": func(t *testing.T, path string) {
			require.NoError(t, os.Mkdir(path, 0o755))
		},
		"a file over 1 MiB": func(t *testing.T, path string) {
			require.NoError(t, os.WriteFile(path, make([]byte, maxFileBytes+1), 0o644))
		},
	}
	for name, create := range tests {
		t.Run(name, func(t *testing.T) {
			root := newRepo(t)
			create(t, filepath.Join(root, FileName))
			loc, _, err := Locate(root)
			require.NoError(t, err)
			require.True(t, loc.Exists)
			_, err = Load(loc)
			var invalidErr *InvalidError
			require.ErrorAs(t, err, &invalidErr)
			assert.Equal(t, loc.Path, invalidErr.Source)
		})
	}
}

func TestLoadUnreadableFile(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("permission bits do not stop root")
	}
	root := newRepo(t)
	loc := Location{Path: filepath.Join(root, FileName)}
	require.NoError(t, os.WriteFile(loc.Path, []byte("environments: {}"), 0o000))
	t.Cleanup(func() { _ = os.Chmod(loc.Path, 0o644) })
	_, err := Load(loc)
	require.ErrorIs(t, err, os.ErrPermission)
	require.ErrorContains(t, err, loc.Path+": cannot be read: permission denied")
}

// A checkout directory is named whatever the clone carries. The messages
// that say where the file is show the path without the characters that would
// act on a terminal, whichever step failed.
func TestStoreErrorsShowAHostilePathSafely(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("permission bits do not stop root")
	}
	const hostile = "svc\x1b[31mRED\rdone"
	root := filepath.Join(t.TempDir(), hostile)
	require.NoError(t, os.MkdirAll(filepath.Join(root, ".git"), 0o755))
	loc, ok, err := Locate(root)
	require.NoError(t, err)
	require.True(t, ok)
	safe := func(err error) {
		t.Helper()
		require.Error(t, err)
		require.NotContains(t, err.Error(), "\x1b")
		require.NotContains(t, err.Error(), "\r")
		require.Contains(t, err.Error(), "svc\uFFFD[31mRED\uFFFDdone")
	}

	require.NoError(t, os.WriteFile(loc.Path, []byte("environments: [\n"), 0o644))
	_, err = Load(loc)
	safe(err)

	require.NoError(t, os.Chmod(loc.Path, 0o000))
	_, err = Load(loc)
	safe(err)
	require.NoError(t, os.Chmod(loc.Path, 0o644))

	broken := errors.New("input/output error")
	_, _, err = locate(root, func(name string) (fs.FileInfo, error) {
		return nil, &fs.PathError{Op: "lstat", Path: name, Err: broken}
	})
	safe(err)

	f := &File{}
	f.Upsert("abc12345.apps.dynatrace.com", Entry{Name: "checkout", ServiceNames: []string{"checkout"}})
	require.NoError(t, os.Chmod(root, 0o555))
	t.Cleanup(func() { _ = os.Chmod(root, 0o755) })
	safe(Save(loc, f))
	safe(Save(loc, &File{}))
}

// Save replaces the file rather than writing into it: the new bytes arrive
// as a new file under the old name, so a reader that has the old one open
// keeps reading the old bytes to their end.
func TestSave_ReplacesTheFile(t *testing.T) {
	root := newRepo(t)
	loc, _, err := Locate(root)
	require.NoError(t, err)
	require.NoError(t, Save(loc, exampleFile()))
	before, err := os.Stat(loc.Path)
	require.NoError(t, err)
	old, err := os.ReadFile(loc.Path)
	require.NoError(t, err)
	reader, err := os.Open(loc.Path)
	require.NoError(t, err)
	defer reader.Close()

	f := exampleFile()
	f.Upsert(prodHost, Entry{Name: "tools", Path: "tools", ServiceNames: []string{"tools"}})
	require.NoError(t, Save(loc, f))

	after, err := os.Stat(loc.Path)
	require.NoError(t, err)
	require.False(t, os.SameFile(before, after), "the file was written in place")
	seen, err := io.ReadAll(reader)
	require.NoError(t, err)
	require.Equal(t, old, seen)
}

// The temp file of a save is the scope file's name with a suffix, so the
// patterns that ignore one ignore the other.
func TestSave_TempFileName(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root writes to a read-only directory")
	}
	root := newRepo(t)
	loc, _, err := Locate(root)
	require.NoError(t, err)
	require.NoError(t, os.Chmod(root, 0o555))
	t.Cleanup(func() { _ = os.Chmod(root, 0o755) })

	err = Save(loc, exampleFile())
	var pathErr *fs.PathError
	require.ErrorAs(t, err, &pathErr)
	require.Regexp(t, `^\.dtctl-repo-scope\.yaml\.\d+\.tmp$`, filepath.Base(pathErr.Path))
}
