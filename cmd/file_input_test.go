package cmd

import (
	"bytes"
	"context"
	"go/ast"
	"go/parser"
	"go/token"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/dynatrace-oss/dtctl/pkg/client"
)

// TestFileFlagsReadThroughReadFileFlag guards #563: `-f -` used to read stdin
// in a third of the commands and a file literally named "-" in the rest,
// because every command resolved its --file value with its own vfs.ReadFile
// call. A command must read a user-named input through readFileFlag, which is
// where "-" means stdin. The allowlist names the few call sites that resolve
// "-" themselves before touching the path.
//
// pkg/ helpers that take a path and read it with vfs.ReadFile are the same
// hole one frame deeper, so calling one from cmd/ fails too.
func TestFileFlagsReadThroughReadFileFlag(t *testing.T) {
	allowed := map[string]string{
		"file_input.go":  "the shared implementation",
		"query_input.go": `resolveQueryInput handles "-" (with its own terminal guard) before it reads a path`,
		"exec_api.go":    `-d @name, not a --file flag; "@-" is resolved by vfs.ReadFileOrStdin`,
	}

	// pkg/ helpers that read a path argument with vfs.ReadFile, so "-" is a
	// file name there.
	pathReaders := map[string]bool{
		"ExecuteFromFile":        true, // pkg/exec DQLExecutor
		"ParseInputFromFile":     true, // pkg/resources/analyzer
		"ParseInputFromFileWith": true,
		"CompareFiles":           true, // pkg/diff Differ
	}

	files, err := filepath.Glob("*.go")
	require.NoError(t, err)
	fset := token.NewFileSet()
	seen := 0
	for _, name := range files {
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		src, err := os.ReadFile(name)
		require.NoError(t, err)
		f, err := parser.ParseFile(fset, name, src, 0)
		require.NoError(t, err, "parse %s", name)
		ast.Inspect(f, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			if pathReaders[sel.Sel.Name] {
				t.Errorf("%s: %s reads a user-named path with vfs.ReadFile; read it with readFileFlag and pass the bytes",
					fset.Position(call.Pos()), sel.Sel.Name)
				return true
			}
			// The seam is vfs itself, or the invocation's Env, vfsEnv(ctx).
			isSeam := false
			switch x := sel.X.(type) {
			case *ast.Ident:
				isSeam = x.Name == "vfs"
			case *ast.CallExpr:
				if id, ok := x.Fun.(*ast.Ident); ok {
					isSeam = id.Name == "vfsEnv"
				}
			}
			if !isSeam || (sel.Sel.Name != "ReadFile" && sel.Sel.Name != "ReadFileOrStdin") {
				return true
			}
			seen++
			if _, ok := allowed[name]; !ok {
				t.Errorf("%s: vfs.%s reads a user-named path directly; use readFileFlag so `-f -` reads stdin",
					fset.Position(call.Pos()), sel.Sel.Name)
			}
			return true
		})
	}
	if seen < 3 {
		t.Fatalf("found %d vfs read calls, want at least 3 (the allowlisted ones): the guard is not seeing them", seen)
	}
}

func TestReadFileFlagFrom(t *testing.T) {
	t.Run("path is read through the vfs seam", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "r.yaml")
		require.NoError(t, os.WriteFile(path, []byte("from-file"), 0o600))

		got, err := readFileFlagFrom(context.Background(), "file", path, queryStdin{r: strings.NewReader("from-pipe")})
		require.NoError(t, err)
		require.Equal(t, "from-file", string(got))
	})

	t.Run("dash reads stdin even when a file named dash exists", func(t *testing.T) {
		dir := t.TempDir()
		require.NoError(t, os.WriteFile(filepath.Join(dir, "-"), []byte("from-file"), 0o600))
		t.Chdir(dir)

		got, err := readFileFlagFrom(context.Background(), "file", "-", queryStdin{r: strings.NewReader("from-pipe")})
		require.NoError(t, err)
		require.Equal(t, "from-pipe", string(got))
	})

	t.Run("terminal stdin is a usage error, not a hang", func(t *testing.T) {
		_, err := readFileFlagFrom(context.Background(), "file", "-", queryStdin{r: strings.NewReader("never read"), isTerminal: true})
		require.Error(t, err)
		require.Contains(t, err.Error(), "stdin is a terminal")
		require.Equal(t, client.ExitUsageError, exitCodeForError(err))
	})

	t.Run("empty stdin names stdin", func(t *testing.T) {
		_, err := readFileFlagFrom(context.Background(), "file", "-", queryStdin{r: strings.NewReader("")})
		require.Error(t, err)
		require.Contains(t, err.Error(), "no input arrived on stdin")
	})
}

const pipedWorkflow = `title: FROM-THE-PIPE
tasks: {}
`

// runWithStdin runs one invocation with stdin fed from content, in a working
// directory that holds a decoy file named "-": a command that treats "-" as a
// path reads the decoy instead of the pipe.
func runWithStdin(t *testing.T, srvURL, stdin string, argv ...string) (int, string, string) {
	t.Helper()
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "-"), []byte("title: FROM-THE-DISK\ntasks: {}\n"), 0o600))
	t.Chdir(dir)
	clearAgentEnvVars(t)
	t.Cleanup(func() { restorePristineTree(context.Background()) })

	var stdout, stderr bytes.Buffer
	code := Run(argv, RunOptions{
		Session: &Session{EnvironmentURL: srvURL, Token: "t", MinStability: "experimental"},
		Stdin:   strings.NewReader(stdin),
		Stdout:  &stdout,
		Stderr:  &stderr,
	})
	return code, stdout.String(), stderr.String()
}

// newNoWriteServer answers every read with 404 (nothing exists yet) and
// records any mutating request, which a dry run must never send.
func newNoWriteServer(t *testing.T) (*httptest.Server, func() []string) {
	t.Helper()
	var (
		mu       sync.Mutex
		mutating []string
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			mu.Lock()
			mutating = append(mutating, r.Method+" "+r.URL.Path)
			mu.Unlock()
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(srv.Close)
	return srv, func() []string {
		mu.Lock()
		defer mu.Unlock()
		return append([]string(nil), mutating...)
	}
}

func TestCreateWorkflow_FileDashReadsStdin(t *testing.T) {
	srv, mutating := newNoWriteServer(t)

	code, out, errOut := runWithStdin(t, srv.URL, pipedWorkflow, "create", "workflow", "-f", "-", "--dry-run")

	require.Zero(t, code, "stdout:\n%s\nstderr:\n%s", out, errOut)
	require.Contains(t, out, "FROM-THE-PIPE")
	require.NotContains(t, out, "FROM-THE-DISK")
	require.Empty(t, mutating())
}

func TestApply_FileDashReadsStdin(t *testing.T) {
	srv, mutating := newNoWriteServer(t)

	code, out, errOut := runWithStdin(t, srv.URL, pipedWorkflow, "apply", "-f", "-", "--dry-run", "-o", "json")

	require.Zero(t, code, "stdout:\n%s\nstderr:\n%s", out, errOut)
	require.Contains(t, out, "FROM-THE-PIPE")
	require.NotContains(t, out, "FROM-THE-DISK")
	require.Empty(t, mutating())
}

// TestApply_WriteIDRefusedWithStdin: --write-id rewrites the input file in
// place. A pipe has nothing to write back to, so the combination is a usage
// error before anything is read or sent.
func TestApply_WriteIDRefusedWithStdin(t *testing.T) {
	srv, mutating := newNoWriteServer(t)

	code, out, errOut := runWithStdin(t, srv.URL, pipedWorkflow, "apply", "-f", "-", "--write-id")

	require.Equal(t, client.ExitUsageError, code, "stdout:\n%s\nstderr:\n%s", out, errOut)
	require.Contains(t, out+errOut, "--write-id")
	require.Empty(t, mutating())
}

func TestDiff_FileDashReadsStdin(t *testing.T) {
	right := filepath.Join(t.TempDir(), "right.yaml")
	require.NoError(t, os.WriteFile(right, []byte("title: FROM-THE-FILE\ntasks: {}\n"), 0o600))

	code, out, errOut := runWithStdin(t, "https://x.example.invalid", pipedWorkflow, "diff", "-f", "-", "-f", right, "--color=false")

	require.Equal(t, ExitCodeHasDiff, code, "stdout:\n%s\nstderr:\n%s", out, errOut)
	require.Contains(t, out, "FROM-THE-PIPE")
	require.Contains(t, out, stdinSourceName)
	require.NotContains(t, out, "FROM-THE-DISK")
}

// TestDiff_StdinTwiceRefused: stdin can be read only once, so `-f - -f -`
// would compare the pipe against nothing.
func TestDiff_StdinTwiceRefused(t *testing.T) {
	code, out, errOut := runWithStdin(t, "https://x.example.invalid", pipedWorkflow, "diff", "-f", "-", "-f", "-")

	require.Equal(t, client.ExitUsageError, code, "stdout:\n%s\nstderr:\n%s", out, errOut)
	require.Contains(t, out+errOut, "stdin")
}
