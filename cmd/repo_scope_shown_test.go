package cmd

import (
	"bytes"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"unicode"

	"github.com/stretchr/testify/require"

	"github.com/Dynatrace/dtctl/pkg/output"
	"github.com/Dynatrace/dtctl/pkg/reposcope"
)

// expectTerminalSafe fails when text holds a control character other than
// the line feeds that end its lines.
func expectTerminalSafe(t *testing.T, text string, args []string) {
	t.Helper()
	if i := strings.IndexFunc(text, func(r rune) bool { return r != '\n' && unicode.IsControl(r) }); i >= 0 {
		line := text[strings.LastIndexByte(text[:i], '\n')+1:]
		line, _, _ = strings.Cut(line, "\n")
		t.Errorf("%q prints a control character: %q", args, line)
	}
}

// Outside a repository the refusal names no path: the message travels in an
// agent envelope and in logs, and the path is this machine's.
func TestRepoScopeOutsideARepositoryNamesNoPath(t *testing.T) {
	const want = "repo scope: the working directory is not inside a git repository: a repo scope lives in .dtctl-repo-scope.yaml at the repository root"
	for _, args := range [][]string{
		{"query", "fetch logs", "--repo-scope", "x"}, {"verify", "query", "fetch logs", "--repo-scope", "x"},
		{"repo-scope", "list"}, {"repo-scope", "discover"}, {"repo-scope", "set", "x", "--service-name", "x"}, {"repo-scope", "delete", "x"},
		{"create", "breakpoint", "A.java:1", "--repo-scope", "x"},
	} {
		newLiveDebuggerStub(t, "readwrite-all")
		dir := t.TempDir()
		t.Chdir(dir)
		code, stdout, stderr := runRepoScoped(args, RunOptions{})
		require.NotZero(t, code, "%q", args)
		require.Equal(t, "Error: "+want+"\n", strings.SplitAfter(stderr, "\n")[0], "%q", args)
		require.NotContains(t, stdout+stderr, filepath.Base(dir), "%q", args)

		code, stdout, _ = runRepoScoped(append(args, "--agent"), RunOptions{})
		require.NotZero(t, code, "%q", args)
		require.Equal(t, want, agentError(t, stdout).Message, "%q", args)
		require.NotContains(t, stdout, filepath.Base(dir), "%q", args)
	}
}

// The scope file comes with a clone, and a refusal names the value it
// refuses. Whichever value that is, and whichever check finds it, it must not
// act on the terminal: the commands that say why a file does not load, and a
// query run beside it, print none of it raw.
func TestRepoScopeRefusalHoldsTheFilesValues(t *testing.T) {
	const hostile = `"\e[2J\a"`
	entry := func(lines ...string) string {
		return "environments:\n  127.0.0.1:\n    - " + strings.Join(lines, "\n      ") + "\n"
	}
	for name, file := range map[string]string{
		"a cluster, on a workload listed twice": entry("name: checkout", "workloads:", "  - {namespace: ns, name: w}", "  - {namespace: ns, name: w, cluster-uid: "+hostile+"}"),
		"a namespace, on a workload listed twice": entry("name: checkout", "workloads:",
			"  - {namespace: "+hostile+", name: w}", "  - {namespace: "+hostile+", name: w}"),
		"a workload name, with and without a cluster": entry("name: checkout", "workloads:",
			"  - {namespace: ns, name: "+hostile+"}", "  - {namespace: ns, name: "+hostile+", cluster-uid: 11111111-2222-3333-4444-555555555555}"),
		"an entry name":           entry("name: "+hostile, "service-names: [checkout]"),
		"a service name":          entry("name: checkout", "service-names: ["+hostile+"]"),
		"a service name twice":    entry("name: checkout", "service-names: ["+hostile+", "+hostile+"]"),
		"a service id":            entry("name: checkout", "services: ["+hostile+"]"),
		"a process group id":      entry("name: checkout", "process-groups: ["+hostile+"]"),
		"a segment id":            entry("name: checkout", "segments: [{id: "+hostile+"}]"),
		"a segment variable":      entry("name: checkout", "segments: [{id: abc, variables: {"+hostile+": [a]}}]"),
		"a segment value":         entry("name: checkout", "segments: [{id: abc, variables: {env: ["+hostile+"]}}]"),
		"an annotation key":       entry("name: checkout", "service-names: [checkout]", "annotations: {"+hostile+": a}"),
		"an annotation value":     entry("name: checkout", "service-names: [checkout]", "annotations: {owner: "+hostile+"}"),
		"an unknown entry key":    entry("name: checkout", "service-names: [checkout]", hostile+": a"),
		"an entry key twice":      entry("name: checkout", "service-names: [checkout]", hostile+": a", hostile+": a"),
		"an environment":          "environments:\n  " + hostile + ":\n    - name: checkout\n      service-names: [checkout]\n",
		"a version":               "apiVersion: " + hostile + "\n" + entry("name: checkout", "service-names: [checkout]"),
		"an unknown document key": hostile + ": 1\n" + entry("name: checkout", "service-names: [checkout]"),
	} {
		t.Run(name, func(t *testing.T) {
			for _, args := range [][]string{
				{"repo-scope", "current"}, {"repo-scope", "describe"}, {"repo-scope", "list"}, {"repo-scope", "describe", "checkout"},
				{"repo-scope", "set", "cart", "--service-name", "cart"}, {"repo-scope", "delete", "checkout"},
				{"query", "fetch logs"}, {"query", "fetch logs", "--repo-scope", "checkout"}, {"verify", "query", "fetch logs"},
			} {
				newDQLRecorder(t, "readonly", answer(http.StatusOK, oneRecord))
				chdirTestRepo(t, file)
				_, stdout, stderr := runRepoScoped(args, RunOptions{})
				expectTerminalSafe(t, stdout+stderr, args)
			}
		})
	}
}

// The printers are the boundary. A reason or a warning is written from a
// file and from directory names, and whatever one comes to hold, the lines a
// person reads show it without what would act on the terminal.
func TestRepoScopePrintersHoldAReasonAndAWarning(t *testing.T) {
	const hostile = "a\x1b[2Jb\ac"
	const shown = "a�[2Jb�c"
	entry := &reposcope.Entry{Name: "checkout", ServiceNames: []string{"checkout"}, Segments: []reposcope.Segment{{ID: "abc"}}}
	for name, print := range map[string]func(w io.Writer){
		"current, no entry":  func(w io.Writer) { printRepoScopeCurrent(w, &reposcope.Status{Reason: hostile}) },
		"current, an entry":  func(w io.Writer) { printRepoScopeCurrent(w, &reposcope.Status{Entry: entry, Reason: hostile}) },
		"describe, no entry": func(w io.Writer) { printRepoScopeDescribe(w, &reposcope.Status{Reason: hostile}) },
		"describe, an entry": func(w io.Writer) { printRepoScopeDescribe(w, &reposcope.Status{Entry: entry, Reason: hostile}) },
		"the file's warning": func(w io.Writer) { warnRepoScopeFile(w, hostile) },
		"query, not applied": func(w io.Writer) {
			(&repoScopedQuery{stderr: w, scope: &output.RepoScope{}, notApplied: hostile, warning: hostile}).announce(nil)
		},
		"query, a segment of the command line": func(w io.Writer) {
			(&repoScopedQuery{stderr: w, scope: &output.RepoScope{Name: "checkout", Code: repoScopeUserSegment}, notApplied: hostile}).announce(nil)
		},
		"query, the filter refused beside segments": func(w io.Writer) {
			(&repoScopedQuery{stderr: w, scope: &output.RepoScope{Name: "checkout"}, bound: true, segments: entry.Segments, refusedReason: hostile}).announce(nil)
		},
	} {
		var out bytes.Buffer
		print(&out)
		expectTerminalSafe(t, out.String(), []string{name})
		require.Contains(t, out.String(), shown, name)
	}
}
