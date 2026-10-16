package vfs

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestOSDefaultReadsHostFilesystem(t *testing.T) {
	path := filepath.Join(t.TempDir(), "x.txt")
	require.NoError(t, os.WriteFile(path, []byte("host"), 0o600))

	data, err := ReadFile(path)
	require.NoError(t, err)
	require.Equal(t, []byte("host"), data)
}

func TestSetActiveInstallsAndRestores(t *testing.T) {
	m := NewMapFS(map[string][]byte{"a.yaml": []byte("virtual")})

	prev := SetActive(m)
	t.Cleanup(func() { SetActive(prev) })

	data, err := ReadFile("a.yaml")
	require.NoError(t, err)
	require.Equal(t, []byte("virtual"), data)

	restored := SetActive(prev)
	require.Equal(t, FS(m), restored, "SetActive must return what it replaced")

	// Back on the host filesystem: the virtual file no longer resolves.
	_, err = ReadFile("a.yaml")
	require.Error(t, err)
}

func TestSetActiveNilSelectsHostFilesystem(t *testing.T) {
	prev := SetActive(NewMapFS(nil))
	SetActive(nil)
	t.Cleanup(func() { SetActive(prev) })

	path := filepath.Join(t.TempDir(), "y.txt")
	require.NoError(t, os.WriteFile(path, []byte("host"), 0o600))
	data, err := ReadFile(path)
	require.NoError(t, err)
	require.Equal(t, []byte("host"), data)
}

func TestMapFSPathNormalization(t *testing.T) {
	m := NewMapFS(map[string][]byte{"./dir/../x.yaml": []byte("v")})

	for _, name := range []string{"x.yaml", "./x.yaml", "/x.yaml", "a/../x.yaml"} {
		data, err := m.ReadFile(name)
		require.NoError(t, err, "path form %q must resolve", name)
		require.Equal(t, []byte("v"), data)
	}
}

func TestMapFSNotExist(t *testing.T) {
	m := NewMapFS(nil)
	_, err := m.ReadFile("missing.yaml")
	require.True(t, errors.Is(err, fs.ErrNotExist),
		"missing entries must report fs.ErrNotExist, got %v", err)
	require.Contains(t, err.Error(), "missing.yaml")
}

func TestMapFSWriteAndSnapshot(t *testing.T) {
	m := NewMapFS(map[string][]byte{"in.yaml": []byte("original")})

	require.NoError(t, m.WriteFile("./out.yaml", []byte("written"), 0o644))
	require.NoError(t, m.WriteFile("in.yaml", []byte("updated"), 0o644))

	files := m.Files()
	require.Equal(t, []byte("written"), files["out.yaml"])
	require.Equal(t, []byte("updated"), files["in.yaml"])

	// Snapshot is a copy: mutating it must not affect the FS.
	files["in.yaml"][0] = 'X'
	data, err := m.ReadFile("in.yaml")
	require.NoError(t, err)
	require.Equal(t, []byte("updated"), data)
}

func TestMapFSDoesNotRetainInputMap(t *testing.T) {
	src := map[string][]byte{"a": []byte("one")}
	m := NewMapFS(src)
	src["a"][0] = 'X'
	src["b"] = []byte("late")

	data, err := m.ReadFile("a")
	require.NoError(t, err)
	require.Equal(t, []byte("one"), data)
	_, err = m.ReadFile("b")
	require.Error(t, err)
}

func TestEnvResolvesPathsThroughItsOwnFilesystem(t *testing.T) {
	prev := SetActive(NewMapFS(map[string][]byte{"a.yaml": []byte("active")}))
	t.Cleanup(func() { SetActive(prev) })

	own := NewMapFS(map[string][]byte{"a.yaml": []byte("own")})
	data, err := Env{FS: own}.ReadFile("a.yaml")
	require.NoError(t, err)
	require.Equal(t, []byte("own"), data, "an Env with a filesystem of its own must not read the installed one")

	data, err = Env{}.ReadFile("a.yaml")
	require.NoError(t, err)
	require.Equal(t, []byte("active"), data, "the zero Env is the process default")

	require.NoError(t, Env{FS: own}.WriteFile("out.yaml", []byte("written"), 0o644))
	require.Equal(t, []byte("written"), own.Files()["out.yaml"])
	_, err = ReadFile("out.yaml")
	require.Error(t, err, "a write through an Env must not reach the installed filesystem")
}

func TestEnvReadsDashFromItsOwnStdin(t *testing.T) {
	data, err := Env{Stdin: strings.NewReader("piped")}.ReadFileOrStdin("-")
	require.NoError(t, err)
	require.Equal(t, []byte("piped"), data)

	own := NewMapFS(map[string][]byte{"f.yaml": []byte("file")})
	data, err = Env{FS: own, Stdin: strings.NewReader("ignored")}.ReadFileOrStdin("f.yaml")
	require.NoError(t, err)
	require.Equal(t, []byte("file"), data, "a path is read from the filesystem, never from stdin")
}

func TestEnvStdinReaderDefaultsToTheProcessStdin(t *testing.T) {
	if got := (Env{}).StdinReader(); got != os.Stdin {
		t.Errorf("the zero Env's stdin = %v, want os.Stdin", got)
	}
	own := strings.NewReader("own")
	if got := (Env{Stdin: own}).StdinReader(); got != own {
		t.Errorf("an Env with its own stdin returned %v", got)
	}
}

func TestPackageLevelFunctionsUseTheInstalledFilesystem(t *testing.T) {
	m := NewMapFS(map[string][]byte{"in.yaml": []byte("input")})
	prev := SetActive(m)
	t.Cleanup(func() { SetActive(prev) })

	require.NoError(t, WriteFile("out.yaml", []byte("written"), 0o644))
	require.Equal(t, []byte("written"), m.Files()["out.yaml"])

	data, err := ReadFileOrStdin("in.yaml")
	require.NoError(t, err)
	require.Equal(t, []byte("input"), data)
}
