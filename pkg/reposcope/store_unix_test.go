//go:build unix

package reposcope

import (
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
	require.ErrorContains(t, err, "read "+loc.Path)
}
