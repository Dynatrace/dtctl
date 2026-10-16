package cmd

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// processStreamWrite matches a write that names the process's own streams:
// os.Stdout/os.Stderr (other than for a terminal check on the descriptor)
// and the fmt.Print family, which writes to os.Stdout.
var processStreamWrite = regexp.MustCompile(`\bos\.(Stdout|Stderr)\b(?:[^.]|$)|\bos\.(Stdout|Stderr)\.[^F]|\bfmt\.Print(f|ln)?\(`)

// processStreamAllowed lists the files that may name the process streams,
// each with the reason it is not a request's output.
var processStreamAllowed = map[string]string{
	"cmd/stdio.go":           "the serialized path's stream swap itself",
	"cmd/invocation.go":      "the fallback when a context carries no invocation (the plain CLI)",
	"pkg/tracing/tracing.go": "OpenTelemetry export diagnostics: host configuration, not request output",

	// The pkg/ files below name the process streams only as the default for a
	// caller that did not hand them its own: every one has an explicit form that
	// cmd/ uses (see TestRequestPathsInjectTheirStreams), so a request never
	// reaches the default.
	"pkg/apply/applier.go":     "default of Applier.WithStderr and the hook writers",
	"pkg/exec/dql.go":          "default of DQLExecutor.WithStreams",
	"pkg/output/auto.go":       "default of PrinterOptions.Notice",
	"pkg/output/live.go":       "default writer of NewLivePrinter",
	"pkg/output/messages.go":   "the Print*/Describe* forms that write to the process; Fprint* take a writer",
	"pkg/output/printer.go":    "default writer of NewPrinter and PrinterOptions.Writer",
	"pkg/output/progress.go":   "default of NewProgressReporter; NewProgressReporterTo takes a writer",
	"pkg/output/watch.go":      "default writer of NewWatchPrinter",
	"pkg/prompt/confirm.go":    "the Confirm* forms that use the process; Confirm*With take streams",
	"pkg/wait/query_waiter.go": "default of WaitConfig.ProgressOut",
}

// TestNoProcessStreamWritesOnRequestPaths keeps output on the invocation's
// streams. A concurrent invocation does not swap os.Stdout — there is one, and
// several invocations — so a write naming the process streams lands in the
// host's log instead of the tenant's response: the response loses it, and the
// log gains tenant data. In cmd/ write to currentStdout(ctx)/currentStderr(ctx)
// or the command's own writers; a pkg/ type takes its streams from its caller.
// TestConcurrentEqualsSerialized (pkg/engine) checks the same at run time for
// its corpus; this checks every line.
func TestNoProcessStreamWritesOnRequestPaths(t *testing.T) {
	for _, dir := range []string{".", "../pkg"} {
		err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() {
				// pkg/serve is a server, not a command: its writes are its own logs.
				if strings.HasSuffix(filepath.ToSlash(path), "pkg/serve") || d.Name() == "testdata" {
					return filepath.SkipDir
				}
				return nil
			}
			name := d.Name()
			if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") ||
				strings.HasSuffix(name, "_windows.go") {
				return nil
			}
			rel := filepath.ToSlash(filepath.Join("cmd", path))
			if strings.HasPrefix(filepath.ToSlash(path), "../") {
				rel = strings.TrimPrefix(filepath.ToSlash(path), "../")
			}
			if _, ok := processStreamAllowed[rel]; ok {
				return nil
			}
			src, err := os.ReadFile(filepath.Clean(path))
			require.NoError(t, err)
			for i, line := range strings.Split(string(src), "\n") {
				code, _, _ := strings.Cut(line, "//")
				if processStreamWrite.MatchString(code) {
					t.Errorf("%s:%d writes to the process's own stream: %s", rel, i+1, strings.TrimSpace(line))
				}
			}
			return nil
		})
		require.NoError(t, err)
	}
}

// legacyDefaultStream matches the pkg/ entry points that write to, or read from,
// the process's own streams or filesystem when the caller gives them nothing
// else. Each has an explicit form that takes the invocation's.
var legacyDefaultStream = regexp.MustCompile(
	`\boutput\.(PrintSuccess|PrintWarning|PrintHumanError|PrintHint|PrintInfo|DescribeKV|DescribeSection|` +
		`NewPrinter|NewPrinterWithOptions|NewPrinterWithOpts|NewWatchPrinter|NewProgressReporter)\(` +
		`|\bprompt\.(Confirm|ConfirmDeletion|ConfirmDataDeletion)\(` +
		`|\bvfs\.(ReadFile|WriteFile|ReadFileOrStdin|Stdin)\b` +
		`|\b(exec\.NewDQLExecutor|exec\.NewFunctionExecutor|apply\.NewApplier|exec\.ReadFileOrStdin|` +
		`appengine\.ReadFileOrStdin|analyzer\.ParseInputFromFile)\(`)

// TestRequestPathsInjectTheirStreams closes the other half of the stream guard.
// pkg/ may name the process streams as a default, so what keeps a request off
// them is cmd/ never reaching those defaults: it builds printers, executors and
// appliers through the helpers in invocation.go, which hand them the
// invocation's streams, and reads files through its vfs.Env.
func TestRequestPathsInjectTheirStreams(t *testing.T) {
	err := filepath.WalkDir(".", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if path != "." {
				return filepath.SkipDir // cmd/ itself, not its subpackages
			}
			return nil
		}
		name := d.Name()
		if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") || name == "invocation.go" {
			return nil
		}
		src, err := os.ReadFile(filepath.Clean(path))
		require.NoError(t, err)
		for i, line := range strings.Split(string(src), "\n") {
			code, _, _ := strings.Cut(line, "//")
			if legacyDefaultStream.MatchString(code) {
				t.Errorf("cmd/%s:%d reaches a process-stream default: %s", name, i+1, strings.TrimSpace(line))
			}
		}
		return nil
	})
	require.NoError(t, err)
}
