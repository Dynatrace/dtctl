//go:build !windows

package cmd

// The tests here make directories named with control characters, which
// Windows does not allow in a file name. What they assert about printed text
// that needs no such directory is in repo_scope_shown_test.go.

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Dynatrace/dtctl/pkg/reposcope"
)

// hostileName is a directory name a clone can carry: git stores any byte but
// NUL and '/', so a name can hold an escape sequence that recolours or
// clears a terminal, a bell and a carriage return that overwrites the line it
// is printed on.
const hostileName = "svc\x1b[31mRED\a\rdone"

// chdirHostileRepo makes a repository whose checkout directory and one
// subdirectory are both named hostileName, holding files and optionally a
// scope file for the mock environment, and moves into the subdirectory.
func chdirHostileRepo(t *testing.T, scopeFile string, files map[string]string) (root string) {
	t.Helper()
	root = filepath.Join(t.TempDir(), hostileName)
	sub := filepath.Join(root, hostileName)
	require.NoError(t, os.MkdirAll(filepath.Join(root, ".git"), 0o755))
	require.NoError(t, os.MkdirAll(sub, 0o755))
	if scopeFile != "" {
		require.NoError(t, os.WriteFile(filepath.Join(root, reposcope.FileName), []byte(scopeFile), 0o644))
	}
	writeRepoFiles(t, root, files)
	t.Chdir(sub)
	return root
}

const hostileScope = "environments:\n  127.0.0.1:\n    - name: checkout\n      service-names: [checkout]\n"

// Every human printer of the feature, run from a directory whose name, and
// whose checkout's name, hold control characters. The repo-scope commands
// print the unit and the directory, the notices of query and of the
// breakpoint commands name the directory and the workspace, and the
// environment's own values come back in discovery's table. None of it may
// act on the terminal.
func TestRepoScopeHumanOutputHoldsControlCharacters(t *testing.T) {
	hostileRows := map[string]string{
		"spans": `{"k8s.cluster.uid":"` + flowClusterUID + `","clusterName":"prod\u001b[2J","k8s.namespace.name":"payments","k8s.workload.name":"checkout",` +
			`"service.name":"check\u001b[31mout\u0007","records":"12","serviceCount":"0","services":[],"processGroupCount":"0","processGroups":[]}`,
	}
	unit := map[string]string{hostileName + "/go.mod": "module example.invalid/checkout\n"}
	repoScope := [][]string{
		{"repo-scope", "current"}, {"repo-scope", "describe"}, {"repo-scope", "list"}, {"repo-scope", "describe", "checkout"},
		{"repo-scope", "describe", "nope"}, {"repo-scope", "delete", "nope"},
		{"repo-scope", "discover"}, {"repo-scope", "discover", "--plain"}, {"repo-scope", "discover", "-o", "wide"},
		{"repo-scope", "discover", "--dry-run"}, {"repo-scope", "discover", "--path", hostileName},
		{"repo-scope", "discover", "--path", hostileName + "/nope"}, {"repo-scope", "discover", "--term", "ledger"},
		{"repo-scope", "set", "ledger", "--service-name", "ledger", "--path", hostileName},
		{"repo-scope", "set", "ledger", "--service-name", "ledger", "--path", hostileName + "/nope"},
		{"repo-scope", "set", "cart", "--service-name", "cart"},
		{"query", "fetch logs"}, {"query", "fetch logs", "--repo-scope", "nope"}, {"query", "fetch logs", "--repo-scope", "checkout"},
		{"verify", "query", "fetch logs"}, {"wait", "query", "fetch logs", "--for=any", "--timeout", "200ms"},
	}
	breakpoints := [][]string{
		{"get", "breakpoints"}, {"get", "snapshots", "A.java:1"}, {"describe", "breakpoint", "A.java:1"},
		{"create", "breakpoint", "A.java:1"}, {"create", "breakpoint", "A.java:1", "--dry-run"},
		{"create", "breakpoint", "A.java:1", "--repo-scope", "checkout"}, {"create", "breakpoint", "A.java:1", "--repo-scope", "nope"},
		{"create", "breakpoint", "A.java:1", "--filters", "k8s.namespace.name:payments"},
		{"update", "breakpoint", "--filters", "k8s.namespace.name:payments", "--dry-run"},
		{"update", "breakpoint", "--filters", "k8s.namespace.name:payments", "--yes"},
		{"update", "breakpoint", "--repo-scope", "checkout", "--yes"},
		{"delete", "breakpoint", "--all", "--dry-run", "--yes"}, {"delete", "breakpoint", "--all", "--yes"},
		{"delete", "breakpoint", "A.java:1", "--yes"},
	}
	for _, state := range []struct {
		name  string
		scope string
		files map[string]string
	}{
		{"linked, the directory a unit", hostileScope, unit},
		{"linked, the directory part of the root unit", hostileScope, map[string]string{"go.mod": "module example.invalid/checkout\n"}},
		{"not linked, the directory a unit", "", unit},
		{"not linked, nothing to read", "", nil},
		{"a scope file that does not load", "environments: [\n", unit},
	} {
		t.Run(state.name, func(t *testing.T) {
			for _, args := range repoScope {
				newDQLRecorder(t, "readonly", rowsByObject(hostileRows))
				chdirHostileRepo(t, state.scope, state.files)
				_, stdout, stderr := runRepoScoped(args, RunOptions{})
				expectTerminalSafe(t, stdout+stderr, args)
			}
			for _, args := range breakpoints {
				newLiveDebuggerStub(t, "readwrite-all", `{"labels":[{"field":"k8s.namespace.name","values":["payments"]}]}`)
				chdirHostileRepo(t, state.scope, state.files)
				_, stdout, stderr := runRepoScoped(args, RunOptions{})
				expectTerminalSafe(t, stdout+stderr, args)
			}
		})
	}
}

// Outside a repository the working directory's own name is all there is,
// and the commands that say where they looked must not print it raw.
func TestRepoScopeOutsideARepositoryHoldsControlCharacters(t *testing.T) {
	for _, args := range [][]string{
		{"repo-scope", "current"}, {"repo-scope", "describe"}, {"repo-scope", "list"}, {"repo-scope", "discover"},
		{"repo-scope", "set", "cart", "--service-name", "cart"}, {"repo-scope", "delete", "cart"},
		{"query", "fetch logs", "--repo-scope", "checkout"}, {"verify", "query", "fetch logs", "--repo-scope", "checkout"},
		{"get", "breakpoints"}, {"create", "breakpoint", "A.java:1"}, {"create", "breakpoint", "A.java:1", "--repo-scope", "checkout"},
		{"update", "breakpoint", "--repo-scope", "checkout", "--yes"},
	} {
		newLiveDebuggerStub(t, "readwrite-all")
		dir := filepath.Join(t.TempDir(), hostileName)
		require.NoError(t, os.Mkdir(dir, 0o755))
		t.Chdir(dir)
		_, stdout, stderr := runRepoScoped(args, RunOptions{})
		expectTerminalSafe(t, stdout+stderr, args)
	}
}

// A directory set would refuse as --path gets no line: the line would save
// nothing. The candidate says why, and the templates for linking by hand
// cover the repository instead.
func TestRepoScopeDiscoverProposesNoPathSetRefuses(t *testing.T) {
	unit := map[string]string{hostileName + "/go.mod": "module example.invalid/checkout\n"}
	row := `{"k8s.cluster.uid":"` + flowClusterUID + `","k8s.namespace.name":"payments","k8s.workload.name":"checkout",` +
		`"service.name":"checkout","records":"12","serviceCount":"0","services":[],"processGroupCount":"0","processGroups":[]}`
	const refusal = `must not contain control or non-printing characters`

	newDQLRecorder(t, "readonly", rowsByObject(map[string]string{"spans": row}))
	chdirHostileRepo(t, "", unit)
	code, stdout, stderr := runRepoScoped([]string{"repo-scope", "discover"}, RunOptions{})
	require.Zero(t, code, stderr)
	require.Contains(t, stdout, "Unit: svc\uFFFD[31mRED\uFFFD\uFFFDdone\n")
	require.Contains(t, stdout, "candidate 1 has no set line: a scope file cannot hold --path: path ")
	require.Contains(t, stdout, refusal)
	require.NotContains(t, stdout, "--path '", "a line set would refuse is not printed")
	require.Contains(t, stdout, "repo-scope set <name> --service-name <service.name>\n", "the template covers the repository")
	require.Contains(t, stdout, "a scope file cannot hold the unit's directory as --path: ")

	// The structured report carries no line for it either.
	report, _ := discoverReport(t)
	require.Empty(t, report.Candidates[0].SetCommand)
	require.Contains(t, strings.Join(report.Notes, "\n"), refusal)

	// With nothing found, the templates are all there is, and they name no
	// directory set would refuse.
	newDQLRecorder(t, "readonly", rowsByObject(nil))
	chdirHostileRepo(t, "", map[string]string{"go.mod": "module example.invalid/checkout\n"})
	code, stdout, stderr = runRepoScoped([]string{"repo-scope", "discover"}, RunOptions{})
	require.Zero(t, code, stderr)
	require.NotContains(t, stdout, "--path '")
	require.Contains(t, stdout, "a scope file cannot hold this directory as --path: ")
}

// The workspace a breakpoint command names in a message is the one it
// sends, shown without what would act on the terminal.
func TestBreakpointWorkspaceIsSentAsNamedAndShownSafe(t *testing.T) {
	for _, args := range [][]string{
		{"get", "breakpoints"}, {"get", "snapshots", "A.java:1"}, {"describe", "breakpoint", "A.java:1"},
		{"create", "breakpoint", "A.java:1"}, {"update", "breakpoint", "--filters", "k8s.namespace.name:payments", "--yes"},
		{"update", "breakpoint", "dtctl-rule-0001", "--enabled=false"}, {"delete", "breakpoint", "--all", "--yes"},
	} {
		stub := newLiveDebuggerStub(t, "readwrite-all", `{"labels":[{"field":"k8s.namespace.name","values":["payments"]}]}`)
		stub.rules = []interface{}{ruleAt("A.java", 1, "")}
		chdirHostileRepo(t, "", nil)
		code, stdout, stderr := runRepoScoped(args, RunOptions{})
		require.Zero(t, code, "%q: %s", args, stderr)
		require.Equal(t, []string{hostileName}, stub.projectPaths(), "%q", args)
		expectTerminalSafe(t, stdout+stderr, args)
		if args[1] == "breakpoints" {
			require.Contains(t, stderr, "workspace: svc\uFFFD[31mRED\uFFFD\uFFFDdone\n")
		}
	}
}

// An entry that covers a directory through the Gradle build says which module
// the directory is, and a module is named by its directory, which is whatever
// a clone carries.
func TestRepoScopeBuildReasonHoldsControlCharacters(t *testing.T) {
	const module = "li\x1b[31mb"
	for _, args := range [][]string{
		{"repo-scope", "current"}, {"repo-scope", "describe"}, {"repo-scope", "list"},
		{"query", "fetch logs"}, {"verify", "query", "fetch logs"},
	} {
		newDQLRecorder(t, "readonly", answer(http.StatusOK, oneRecord))
		repo := chdirTestRepo(t, "environments:\n  127.0.0.1:\n    - name: app\n      path: app-a\n      service-names: [app]\n")
		writeRepoFiles(t, repo, map[string]string{
			"settings.gradle":        "include 'app-a', '" + module + "'\n",
			"app-a/build.gradle":     "dependencies { implementation project(':" + module + "') }\n",
			module + "/build.gradle": "",
		})
		t.Chdir(filepath.Join(repo, module))
		_, stdout, stderr := runRepoScoped(args, RunOptions{})
		expectTerminalSafe(t, stdout+stderr, args)
		if args[1] == "current" {
			require.Equal(t, "app  (app-a · 127.0.0.1; covered through the Gradle build: \"app-a\" depends on \"li\\x1b[31mb\")\n", stdout)
		}
	}
}
