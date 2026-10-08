//go:build unix

package reposcope

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Discovery reads .git/config by name, outside the walk that skips anything
// but regular files, so it is held to the scope file's rule: a symlink out of
// the repository names nothing, and a FIFO neither names anything nor blocks.
func TestExtract_GitConfigOnlyARegularFile(t *testing.T) {
	tests := map[string]func(t *testing.T, path string){
		"a symlink out of the repository": func(t *testing.T, path string) {
			target := filepath.Join(t.TempDir(), "config")
			require.NoError(t, os.WriteFile(target, []byte("[remote \"origin\"]\n\turl = https://host.example.invalid/team/elsewhere.git\n"), 0o644))
			require.NoError(t, os.Symlink(target, path))
		},
		"a FIFO": func(t *testing.T, path string) {
			require.NoError(t, syscall.Mkfifo(path, 0o644))
		},
	}
	for name, create := range tests {
		t.Run(name, func(t *testing.T) {
			root := newRepo(t)
			create(t, filepath.Join(root, ".git", "config"))
			done := make(chan []token, 1)
			go func() {
				s, err := extract(os.DirFS(root), ".")
				assert.NoError(t, err)
				done <- s.tokens
			}()
			select {
			case tokens := <-done:
				assert.Empty(t, tokenLines(tokens))
			case <-time.After(5 * time.Second):
				t.Fatal("discovery blocked reading .git/config")
			}
		})
	}
}
