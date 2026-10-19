package cmd

import (
	"bytes"
	"context"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/adrg/xdg"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
	"github.com/stretchr/testify/require"

	"github.com/dynatrace-oss/dtctl/pkg/config"
)

// isolateProbeHost points everything a command can write on the host at
// temporary directories. The probes run every command, including config,
// alias and auth, which write the config file (the global one, or a
// .dtctl.yaml in the working directory) and the data and cache dirs. NOT
// parallel: the environment and the working directory are process-global.
func isolateProbeHost(t *testing.T) {
	t.Helper()
	// Registered before t.Setenv so it runs after the variables are restored
	// (see chdirWithLocalConfig).
	t.Cleanup(xdg.Reload)
	for _, v := range []string{"HOME", "XDG_CONFIG_HOME", "XDG_DATA_HOME", "XDG_CACHE_HOME", "XDG_STATE_HOME"} {
		t.Setenv(v, t.TempDir())
	}
	t.Setenv("DTCTL_CONFIG", "")
	xdg.Reload()
	t.Chdir(t.TempDir())
}

// fileFlagProbe is one run: a command with one string flag set to a sentinel.
type fileFlagProbe struct {
	command string // "create workflow"
	argv    []string
	values  map[string]string // sentinel value → flag it was given to
}

// attribute names the flag and kind a read of name came from: a read of a
// value given verbatim is a path flag, a read of a value given as "@value"
// is an at-path flag.
func (p fileFlagProbe) attribute(name string) (key, kind string, ok bool) {
	if flag, ok := p.values[name]; ok {
		return p.command + " --" + flag, fileValuePath, true
	}
	if flag, ok := p.values["@"+name]; ok {
		return p.command + " --" + flag, fileValueAtPath, true
	}
	return "", "", false
}

// fileFlagProbes builds a probe per runnable command, string flag, argument
// shape and value form. Each required flag gets a sentinel of its own, so a
// read through it is attributed to it.
func fileFlagProbes(root *cobra.Command) []fileFlagProbe {
	var probes []fileFlagProbe
	walkCommands(root, func(c *cobra.Command) {
		if !c.Runnable() || c == root {
			return
		}
		path := commandPathArgs(c)
		command := strings.Join(path, " ")
		flags := commandFlags(root, c)
		var required []string
		for name, f := range flags {
			if v := f.Annotations[cobra.BashCompOneRequiredFlag]; len(v) > 0 && v[0] == "true" {
				required = append(required, name)
			}
		}
		sort.Strings(required)

		for _, args := range probeArgShapes(c) {
			for name, f := range flags {
				if !isStringFlag(f) {
					continue
				}
				times := 1
				if f.Value.Type() != "string" {
					// A slice flag is also given twice, for commands that read
					// one or two files from it (diff -f a [-f b]).
					times = 2
				}
				for _, form := range probeValueForms(times) {
					p := fileFlagProbe{command: command, values: map[string]string{}}
					p.argv = append(append([]string{}, path...), args...)
					for range form.times {
						// --name=value: a flag with an optional value would take a
						// separate token as the command's argument instead.
						p.argv = append(p.argv, "--"+name+"="+p.sentinel(form.at, name))
					}
					for _, r := range required {
						if r != name {
							p.argv = append(p.argv, "--"+r+"="+requiredValue(flags[r], p.sentinel("", r)))
						}
					}
					p.argv = append(append(p.argv, fileFlagProbeHints[command]...), "--agent")
					probes = append(probes, p)
				}
			}
		}
	})
	return probes
}

func (p fileFlagProbe) sentinel(at, flag string) string {
	v := at + "dtctl-file-flag-probe-" + flag + ".yaml"
	p.values[v] = flag
	return v
}

// probeValueForm is how a probe gives its flag a sentinel: as a plain path or
// as "@path" (at), and how many times.
type probeValueForm struct {
	at    string
	times int
}

// probeValueForms lists each form given once and, up to maxTimes, more often.
func probeValueForms(maxTimes int) []probeValueForm {
	var forms []probeValueForm
	for times := 1; times <= maxTimes; times++ {
		forms = append(forms, probeValueForm{"", times}, probeValueForm{"@", times})
	}
	return forms
}

// probeArgShapes returns the fewest arguments the command accepts and, if it
// accepts one more, that many too: an optional argument can select the branch
// that reads (exec function app/fn --data). Each count comes as plain words and
// as "app/name" pairs and as "/path"s, which some commands parse before they
// read (open intent app/intent, exec api /path).
func probeArgShapes(c *cobra.Command) [][]string {
	var shapes [][]string
	counts := 0
	for n := 0; n <= 3 && counts < 2; n++ {
		plain, pair, path := make([]string, n), make([]string, n), make([]string, n)
		for i := range plain {
			plain[i], pair[i], path[i] = "probe", "app/probe", "/probe"
		}
		if c.ValidateArgs(plain) != nil {
			if counts > 0 {
				break
			}
			continue
		}
		counts++
		shapes = append(shapes, plain)
		if n > 0 {
			shapes = append(shapes, pair, path)
		}
	}
	return shapes
}

func isStringFlag(f *pflag.Flag) bool {
	switch f.Value.Type() {
	case "string", "stringSlice", "stringArray":
		return true
	}
	return false
}

func requiredValue(f *pflag.Flag, sentinel string) string {
	switch f.Value.Type() {
	case "bool":
		return "true"
	case "int", "int32", "int64", "float64":
		return "1"
	case "duration":
		return "1s"
	}
	return sentinel
}

func commandPathArgs(c *cobra.Command) []string {
	return strings.Fields(c.CommandPath())[1:] // drop "dtctl"
}

func runFileFlagProbe(serverURL string, p fileFlagProbe) []recordedRead {
	rec := &recordingFS{}
	runProbe(serverURL, p.argv, rec, &recordingReader{r: strings.NewReader("")})
	return rec.reads
}

func runProbe(serverURL string, argv []string, fsys *recordingFS, stdin io.Reader) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var stdout, stderr bytes.Buffer
	Run(argv, RunOptions{
		Concurrent: true,
		Session: &Session{
			EnvironmentURL: serverURL,
			Token:          "dt0c01.T.T",
			MinStability:   config.StabilityExperimental,
		},
		FS:      fsys,
		Stdin:   stdin,
		Stdout:  &stdout,
		Stderr:  &stderr,
		Context: ctx,
	})
}

// commandFlags is every flag c accepts except the root's persistent ones, which
// name no files: its own, and those it inherits from a parent group.
func commandFlags(root, c *cobra.Command) map[string]*pflag.Flag {
	flags := map[string]*pflag.Flag{}
	visit := func(f *pflag.Flag) {
		if _, dup := flags[f.Name]; !dup && root.PersistentFlags().Lookup(f.Name) != f {
			flags[f.Name] = f
		}
	}
	c.NonInheritedFlags().VisitAll(visit)
	c.InheritedFlags().VisitAll(visit)
	return flags
}

// declaredFileFlags maps "<command> --<flag>" of every runnable command to its
// declared kind, or for a writes-to flag to annotationWritesTo.
func declaredFileFlags(root *cobra.Command) map[string]string {
	declared := map[string]string{}
	walkCommands(root, func(c *cobra.Command) {
		if !c.Runnable() || c == root {
			return
		}
		command := strings.Join(commandPathArgs(c), " ")
		for name, f := range commandFlags(root, c) {
			if v := f.Annotations[annotationFile]; len(v) == 1 {
				declared[command+" --"+name] = v[0]
			}
			if v := f.Annotations[annotationWritesTo]; len(v) == 1 {
				declared[command+" --"+name] = annotationWritesTo
			}
		}
	})
	return declared
}

// recordedRead is a read of the recording filesystem, with the cmd/ frames
// ("file.go:line") on its stack.
type recordedRead struct {
	name  string
	stack []string
}

// recordingFS records every read and answers it with an empty document, so a
// command that reads two files gets to the second.
type recordingFS struct {
	mu    sync.Mutex
	reads []recordedRead
}

func (r *recordingFS) ReadFile(name string) ([]byte, error) {
	pcs := make([]uintptr, 64)
	frames := runtime.CallersFrames(pcs[:runtime.Callers(2, pcs)])
	var stack []string
	for {
		f, more := frames.Next()
		if strings.HasPrefix(f.Function, "github.com/dynatrace-oss/dtctl/cmd.") && !strings.HasSuffix(f.File, "_test.go") {
			stack = append(stack, fmt.Sprintf("%s:%d", filepath.Base(f.File), f.Line))
		}
		if !more {
			break
		}
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.reads = append(r.reads, recordedRead{name: name, stack: stack})
	return []byte("{}\n"), nil
}

func (r *recordingFS) WriteFile(string, []byte, fs.FileMode) error { return fs.ErrPermission }

// recordingReader records whether anything read from it.
type recordingReader struct {
	r    io.Reader
	read atomic.Bool
}

func (r *recordingReader) Read(p []byte) (int, error) {
	r.read.Store(true)
	return r.r.Read(p)
}

// fileReadCall is a call to one of the helpers that read a user-named file,
// in the function encloses it.
type fileReadCall struct {
	file, caller, fn string
	start, end       int
}

// key names the call in fileFlagUnreached: by function, not line, so the entry
// survives edits elsewhere in the file.
func (c fileReadCall) key() string { return c.file + ":" + c.caller }

func (c fileReadCall) reachedBy(sites map[string]bool) bool {
	for line := c.start; line <= c.end; line++ {
		if sites[fmt.Sprintf("%s:%d", c.file, line)] {
			return true
		}
	}
	return false
}

// fileReadCallSites lists the calls in cmd/ to the helpers that read a
// user-named file, outside the helpers themselves.
func fileReadCallSites(t *testing.T) []fileReadCall {
	t.Helper()
	helpers := map[string]bool{
		"readFileFlag":      true,
		"readFileFlagFrom":  true,
		"resolveQueryInput": true,
		"readRequestBody":   true,
	}
	files, err := filepath.Glob("*.go")
	require.NoError(t, err)
	fset := token.NewFileSet()
	var calls []fileReadCall
	for _, name := range files {
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		src, err := os.ReadFile(name)
		require.NoError(t, err)
		f, err := parser.ParseFile(fset, name, src, 0)
		require.NoError(t, err)
		for _, decl := range f.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil || helpers[fn.Name.Name] {
				continue
			}
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				if id, ok := call.Fun.(*ast.Ident); ok && helpers[id.Name] {
					calls = append(calls, fileReadCall{
						file:   name,
						caller: fn.Name.Name,
						fn:     id.Name,
						start:  fset.Position(call.Pos()).Line,
						end:    fset.Position(call.End()).Line,
					})
				}
				return true
			})
		}
	}
	if len(calls) < 25 {
		t.Fatalf("found %d file-read calls, want at least 25: the guard is not seeing them", len(calls))
	}
	return calls
}

func flagOf(key string) string { return key[strings.LastIndex(key, "--")+2:] }

func kindConst(kind string) string {
	if kind == fileValueAtPath {
		return "AtPath"
	}
	return "Path"
}

func dedupe(s []string) []string {
	sort.Strings(s)
	out := s[:0]
	for i, v := range s {
		if i == 0 || v != s[i-1] {
			out = append(out, v)
		}
	}
	return out
}
