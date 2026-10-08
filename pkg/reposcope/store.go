package reposcope

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
)

// The only file in the package that touches the host disk; see
// hostFileAccessAllowlist in cmd/vfs_guard_test.go.

// fileMode is the scope file's permission: it is committed, so it is as
// readable as the rest of the repository.
const fileMode = 0o644

// Location is where the scope file lives (or would live) for a working
// directory.
type Location struct {
	// Root is the nearest ancestor of the start directory (inclusive) holding
	// a .git entry — a directory, or the file a worktree or submodule carries.
	Root string
	// Path is Root/FileName; Exists says whether it is there.
	Path   string
	Exists bool
	// RelDir is the start directory relative to Root, slash-separated, "." at
	// the root. Resolve matches Entry.Path against it.
	RelDir string
}

// Locate walks up from startDir to the first directory holding .git and
// reports the scope file's location there. It never walks past a .git, so
// a parent repository's file cannot leak into a nested one, and it never
// accepts a file in a directory without .git.
//
// startDir is resolved with filepath.EvalSymlinks first: a logical $PWD
// through a symlink would otherwise miss the .git. ok is false, the ordinary
// "not in a repository" case, when no .git is found and also when a stat
// fails for another reason: an unreadable parent must not fail the query,
// the same choice the local .dtctl.yaml lookup makes. err is reserved for a
// scope file that is there but cannot be inspected.
func Locate(startDir string) (loc Location, ok bool, err error) {
	start, err := filepath.Abs(startDir)
	if err != nil {
		return Location{}, false, nil
	}
	if resolved, err := filepath.EvalSymlinks(start); err == nil {
		start = resolved
	}
	root, found := gitRoot(start)
	if !found {
		return Location{}, false, nil
	}
	rel, err := filepath.Rel(root, start)
	if err != nil {
		return Location{}, false, nil
	}
	loc = Location{Root: root, Path: filepath.Join(root, FileName), RelDir: filepath.ToSlash(rel)}
	switch _, err := os.Lstat(loc.Path); {
	case err == nil:
		loc.Exists = true
	case !errors.Is(err, fs.ErrNotExist):
		return Location{}, false, fmt.Errorf("inspect %s: %w", loc.Path, err)
	}
	return loc, true, nil
}

func gitRoot(dir string) (string, bool) {
	for {
		_, err := os.Lstat(filepath.Join(dir, ".git"))
		switch {
		case err == nil:
			return dir, true
		case !errors.Is(err, fs.ErrNotExist):
			return "", false
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", false
		}
		dir = parent
	}
}

// Load reads and validates the file at loc.Path. A missing file yields an
// empty, valid File, so a caller that saves can start from Load.
//
// Only a regular file of at most 1 MiB is read. git commits symlinks, so a
// cloned repository could otherwise point the file at /dev/zero or a FIFO and
// hang every query run inside it.
func Load(loc Location) (*File, error) {
	data, err := readRegular(os.DirFS(filepath.Dir(loc.Path)), filepath.Base(loc.Path))
	var invalidErr *InvalidError
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return &File{}, nil
	case errors.As(err, &invalidErr):
		invalidErr.Source = loc.Path
		return nil, invalidErr
	case err != nil:
		return nil, fmt.Errorf("read %s: %w", loc.Path, err)
	}
	f, invalidErr := decode(data)
	if invalidErr != nil {
		invalidErr.Source = loc.Path
		return nil, invalidErr
	}
	return f, nil
}

// Save writes f atomically, through a temp file in the same directory, so an
// interrupted write never leaves a half-written file and a refused one leaves
// the old file byte-identical. It removes the file when f is empty, and
// refuses while the loaded file holds keys this dtctl does not know, which
// writing would drop. Comments and key order are not preserved; the header is
// rewritten.
func Save(loc Location, f *File) error {
	if err := f.check(); err != nil {
		err.Source = loc.Path
		return err
	}
	if w := f.Warning(); w != "" {
		return &InvalidError{Source: loc.Path, Msg: w,
			Suggestions: []string{"fix or remove the key by hand, or upgrade dtctl if a newer version wrote it"}}
	}
	if f.Empty() {
		if err := os.Remove(loc.Path); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return fmt.Errorf("remove %s: %w", loc.Path, err)
		}
		return nil
	}
	data, err := encode(f)
	if err != nil {
		return err
	}
	return writeAtomic(loc.Path, data)
}

// writeAtomic replaces path with data so that a reader sees either the old
// file or the new one, never a mix.
func writeAtomic(path string, data []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+FileName+".*.tmp")
	if err == nil {
		_, err = tmp.Write(data)
		err = errors.Join(err, tmp.Sync(), tmp.Chmod(fileMode), tmp.Close())
		if err == nil {
			err = os.Rename(tmp.Name(), path)
		}
		if err != nil {
			_ = os.Remove(tmp.Name())
		}
	}
	if err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	return nil
}

// readRegular reads name from fsys when it is a regular file of at most
// maxFileBytes, and is the one reader for files a repository controls.
// fs.Lstat does not follow a symlink, so a file a clone controls cannot point
// outside the repository, at /dev/zero or at a FIFO. Anything else is an
// InvalidError naming name, so it reads as a broken file rather than as an
// I/O failure.
func readRegular(fsys fs.FS, name string) ([]byte, error) {
	info, err := fs.Lstat(fsys, name)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, &InvalidError{Source: name, Msg: "is not a regular file", Suggestions: []string{"replace it with a regular file, or delete it"}}
	}
	file, err := fsys.Open(name)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, maxFileBytes+1))
	if err != nil {
		return nil, err
	}
	if len(data) > maxFileBytes {
		return nil, &InvalidError{Source: name, Msg: "is larger than 1 MiB"}
	}
	return data, nil
}

// RepoFS is the repository root as a read-only fs.FS for PlanDiscovery.
func RepoFS(loc Location) fs.FS {
	return os.DirFS(loc.Root)
}
