package reposcope

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newRepo creates a temporary repository root with a .git directory and
// returns its resolved path (macOS puts temp dirs behind a symlink).
func newRepo(t *testing.T) string {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)
	require.NoError(t, os.Mkdir(filepath.Join(root, ".git"), 0o755))
	return root
}

func mkdirs(t *testing.T, root string, dirs ...string) {
	t.Helper()
	for _, d := range dirs {
		require.NoError(t, os.MkdirAll(filepath.Join(root, filepath.FromSlash(d)), 0o755))
	}
}

func TestLocate(t *testing.T) {
	t.Run("at the root", func(t *testing.T) {
		root := newRepo(t)
		loc, ok, err := Locate(root)
		require.NoError(t, err)
		require.True(t, ok)
		assert.Equal(t, Location{Root: root, Path: filepath.Join(root, FileName), RelDir: "."}, loc)
	})
	t.Run("below the root, with the file present", func(t *testing.T) {
		root := newRepo(t)
		mkdirs(t, root, "services/checkout/internal")
		require.NoError(t, os.WriteFile(filepath.Join(root, FileName), nil, 0o644))
		loc, ok, err := Locate(filepath.Join(root, "services", "checkout", "internal"))
		require.NoError(t, err)
		require.True(t, ok)
		assert.Equal(t, root, loc.Root)
		assert.True(t, loc.Exists)
		assert.Equal(t, "services/checkout/internal", loc.RelDir)
	})
	t.Run("outside any repository", func(t *testing.T) {
		dir, err := filepath.EvalSymlinks(t.TempDir())
		require.NoError(t, err)
		_, ok, err := Locate(dir)
		require.NoError(t, err)
		assert.False(t, ok)
	})
	t.Run("a nested repository stops the walk", func(t *testing.T) {
		outer := newRepo(t)
		require.NoError(t, os.WriteFile(filepath.Join(outer, FileName), nil, 0o644))
		mkdirs(t, outer, "vendor/inner/.git", "vendor/inner/pkg")
		loc, ok, err := Locate(filepath.Join(outer, "vendor", "inner", "pkg"))
		require.NoError(t, err)
		require.True(t, ok)
		assert.Equal(t, filepath.Join(outer, "vendor", "inner"), loc.Root)
		assert.False(t, loc.Exists, "the outer repository's file does not leak in")
		assert.Equal(t, "pkg", loc.RelDir)
	})
	t.Run("a .git file marks a worktree root", func(t *testing.T) {
		root, err := filepath.EvalSymlinks(t.TempDir())
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(filepath.Join(root, ".git"), []byte("gitdir: /elsewhere\n"), 0o644))
		mkdirs(t, root, "cmd")
		loc, ok, err := Locate(filepath.Join(root, "cmd"))
		require.NoError(t, err)
		require.True(t, ok)
		assert.Equal(t, root, loc.Root)
	})
	t.Run("a symlinked working directory", func(t *testing.T) {
		root := newRepo(t)
		mkdirs(t, root, "services/checkout")
		link := filepath.Join(t.TempDir(), "shortcut")
		require.NoError(t, os.Symlink(filepath.Join(root, "services", "checkout"), link))
		loc, ok, err := Locate(link)
		require.NoError(t, err)
		require.True(t, ok)
		assert.Equal(t, root, loc.Root)
		assert.Equal(t, "services/checkout", loc.RelDir)
	})
	t.Run("an unreadable parent is not an error", func(t *testing.T) {
		if runtime.GOOS == "windows" || os.Geteuid() == 0 {
			t.Skip("permission bits do not stop this user")
		}
		dir, err := filepath.EvalSymlinks(t.TempDir())
		require.NoError(t, err)
		mkdirs(t, dir, "locked/inner")
		require.NoError(t, os.Chmod(filepath.Join(dir, "locked"), 0o600))
		t.Cleanup(func() { _ = os.Chmod(filepath.Join(dir, "locked"), 0o755) })
		_, ok, err := Locate(filepath.Join(dir, "locked", "inner"))
		require.NoError(t, err)
		assert.False(t, ok)
	})
}

func TestLoad_MissingFileIsEmpty(t *testing.T) {
	loc, _, err := Locate(newRepo(t))
	require.NoError(t, err)
	f, err := Load(loc)
	require.NoError(t, err)
	assert.True(t, f.Empty())
}

func TestLoad_InvalidNamesThePath(t *testing.T) {
	root := newRepo(t)
	require.NoError(t, os.WriteFile(filepath.Join(root, FileName), []byte("environments:\n  "+prodHost+":\n    - name: Bad\n      service-names: [x1x]\n"), 0o644))
	loc, _, err := Locate(root)
	require.NoError(t, err)
	_, err = Load(loc)
	var invalidErr *InvalidError
	require.ErrorAs(t, err, &invalidErr)
	assert.Equal(t, loc.Path, invalidErr.Source)
}

func TestSave(t *testing.T) {
	t.Run("writes the canonical form", func(t *testing.T) {
		root := newRepo(t)
		loc, _, err := Locate(root)
		require.NoError(t, err)
		require.NoError(t, Save(loc, exampleFile()))

		data, err := os.ReadFile(loc.Path)
		require.NoError(t, err)
		assert.Equal(t, strings.ReplaceAll(string(readFixture(t, "example.yaml")), "\r\n", "\n"), string(data))
		info, err := os.Stat(loc.Path)
		require.NoError(t, err)
		if runtime.GOOS != "windows" {
			assert.Equal(t, os.FileMode(fileMode), info.Mode().Perm())
		}
		assertOnlyEntries(t, root, ".git", FileName)

		loaded, err := Load(loc)
		require.NoError(t, err)
		assert.Equal(t, exampleFile(), loaded, "what Save writes, Load reads back")
	})
	t.Run("removes the file when nothing is left", func(t *testing.T) {
		root := newRepo(t)
		loc, _, err := Locate(root)
		require.NoError(t, err)
		require.NoError(t, Save(loc, exampleFile()))
		require.NoError(t, Save(loc, &File{}))
		assertOnlyEntries(t, root, ".git")
		require.NoError(t, Save(loc, &File{}), "removing a missing file is fine")
	})
	t.Run("an invalid file leaves the old bytes", func(t *testing.T) {
		root := newRepo(t)
		loc, _, err := Locate(root)
		require.NoError(t, err)
		require.NoError(t, Save(loc, exampleFile()))
		before, err := os.ReadFile(loc.Path)
		require.NoError(t, err)

		f, err := Load(loc)
		require.NoError(t, err)
		f.Upsert(prodHost, Entry{Name: "checkout", Services: []string{"SERVICE-123"}})
		err = Save(loc, f)
		var invalidErr *InvalidError
		require.ErrorAs(t, err, &invalidErr)
		assert.Equal(t, loc.Path, invalidErr.Source)

		after, err := os.ReadFile(loc.Path)
		require.NoError(t, err)
		assert.Equal(t, before, after)
		assertOnlyEntries(t, root, ".git", FileName)
	})
	t.Run("refuses to drop keys a newer dtctl wrote", func(t *testing.T) {
		root := newRepo(t)
		newer := readFixture(t, "newer-dtctl.yaml")
		require.NoError(t, os.WriteFile(filepath.Join(root, FileName), newer, 0o644))
		loc, _, err := Locate(root)
		require.NoError(t, err)

		f, err := Load(loc)
		require.NoError(t, err)
		f.Upsert(prodHost, Entry{Name: "ledger", Path: "services/ledger", ServiceNames: []string{"ledger"}})
		err = Save(loc, f)
		var invalidErr *InvalidError
		require.ErrorAs(t, err, &invalidErr)
		assert.Equal(t, loc.Path, invalidErr.Source)
		assert.Equal(t, f.Warning(), invalidErr.Msg)

		after, err := os.ReadFile(loc.Path)
		require.NoError(t, err)
		assert.Equal(t, newer, after)
	})
}

// assertOnlyEntries checks that dir holds exactly names: no temp file left
// behind by an atomic write.
func assertOnlyEntries(t *testing.T, dir string, names ...string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	var got []string
	for _, e := range entries {
		got = append(got, e.Name())
	}
	assert.ElementsMatch(t, names, got)
}

func TestRepoFS(t *testing.T) {
	root := newRepo(t)
	require.NoError(t, os.WriteFile(filepath.Join(root, "go.mod"), []byte("module example.invalid/acme/orders\n"), 0o644))
	loc, _, err := Locate(root)
	require.NoError(t, err)
	s, err := extract(RepoFS(loc), loc.RelDir)
	require.NoError(t, err)
	assert.Equal(t, []string{"module orders go.mod:1"}, tokenLines(s.tokens))
}
