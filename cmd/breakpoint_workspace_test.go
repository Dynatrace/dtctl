package cmd

import (
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// brokenScope is a scope file that does not load.
const brokenScope = "environments: [\n"

// The scope file decides which workspace a breakpoint command works in. When
// it is there and does not load, no command may guess: a delete or a filter
// change in the workspace of another name would hit the wrong breakpoints.
// Every command that changes something is refused with what is wrong with
// the file, its dry run included, before anything is sent.
func TestBreakpoint_ABrokenScopeFileRefusesEveryChange(t *testing.T) {
	commands := slices.Concat(mutatingBreakpointCommands, []struct {
		name string
		argv []string
	}{
		{"create", []string{"create", "breakpoint", "A.java:1"}},
		{"create with filters", []string{"create", "breakpoint", "A.java:1", "--filters", "k8s.namespace.name:web"}},
	})
	for _, tc := range commands {
		for _, mode := range []string{"", "--dry-run"} {
			t.Run(tc.name+mode, func(t *testing.T) {
				srv := newLiveDebuggerStub(t, "readwrite-all", webSetJSON)
				srv.rules = []interface{}{ruleAt("A.java", 1, "")}
				root := chdirTestRepo(t, brokenScope)
				chdirSubdir(t, "src")
				argv := slices.Concat(tc.argv, []string{"--agent", "--yes"})
				if mode != "" {
					argv = append(argv, mode)
				}

				code, stdout, stderr := runRepoScoped(argv, RunOptions{})
				require.NotZero(t, code, stdout)
				detail := agentError(t, stdout)
				require.Equal(t, "validation_error", detail.Code)
				require.Contains(t, detail.Message, ".dtctl-repo-scope.yaml: not valid YAML")
				require.Regexp(t, "so nothing was (changed|sent)$", detail.Message)
				require.NotContains(t, stdout+stderr, root, "the file is named as the repository names it")
				require.Empty(t, srv.ops())
			})
		}
	}
}

// A command that only reads carries on in the workspace the working
// directory names, as without a scope file, and says so: on stderr, and in
// the agent envelope's warnings.
func TestBreakpoint_ABrokenScopeFileLeavesReadsInTheDirectoryWorkspace(t *testing.T) {
	const warning = ".dtctl-repo-scope.yaml: not valid YAML"
	const fallback = "; reading workspace src, the one named after the working directory"
	for _, tc := range []struct {
		name string
		argv []string
	}{
		{"get breakpoints", []string{"get", "breakpoints"}},
		{"describe breakpoint", []string{"describe", "breakpoint", "A.java:1"}},
		{"get snapshots", []string{"get", "snapshots", "A.java:1"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := newLiveDebuggerStub(t, "readonly")
			srv.rules = []interface{}{ruleAt("A.java", 1, "")}
			root := chdirTestRepo(t, brokenScope)
			require.NoError(t, os.WriteFile(filepath.Join(root, ".git", "config"),
				[]byte("[remote \"origin\"]\n\turl = git@example.invalid:shop/storefront.git\n"), 0o644))
			chdirSubdir(t, "src")

			code, stdout, stderr := runRepoScoped(append(tc.argv, "--plain"), RunOptions{})
			require.Zero(t, code, "stdout: %s\nstderr: %s", stdout, stderr)
			require.Equal(t, []string{"src"}, srv.projectPaths())
			require.Contains(t, stderr, warning)
			require.Contains(t, stderr, fallback)
			require.NotContains(t, stdout+stderr, root)

			code, stdout, stderr = runRepoScoped(append(tc.argv, "--agent"), RunOptions{})
			require.Zero(t, code, "stdout: %s\nstderr: %s", stdout, stderr)
			warnings := agentEnvelope(t, stdout).Context.Warnings
			require.Len(t, warnings, 1)
			require.Contains(t, warnings[0], warning)
			require.Contains(t, warnings[0], fallback)
			require.NotContains(t, stdout+stderr, root)

			// Naming an entry asks for the file, so there it is the error.
			srv.reset()
			code, stdout, stderr = runRepoScoped(append(tc.argv, "--agent", "--repo-scope", "checkout"), RunOptions{})
			require.NotZero(t, code)
			require.Equal(t, "validation_error", agentError(t, stdout).Code)
			require.Contains(t, agentError(t, stdout).Message, "--repo-scope checkout names an entry of it, so nothing was sent")
			require.NotContains(t, stdout+stderr, root)
			require.Empty(t, srv.ops())
		})
	}
}

// The repository names the workspace only where its scope file has entries
// for this environment. A file that says nothing about the environment
// leaves the breakpoint commands where they were before it existed.
func TestBreakpoint_AScopeFileForAnotherEnvironmentNamesNoWorkspace(t *testing.T) {
	srv := newLiveDebuggerStub(t, "readonly")
	root := chdirTestRepo(t, "environments:\n  other.example.invalid:\n    - name: checkout\n      service-names: [checkout]\n")
	require.NoError(t, os.WriteFile(filepath.Join(root, ".git", "config"),
		[]byte("[remote \"origin\"]\n\turl = git@example.invalid:shop/storefront.git\n"), 0o644))
	chdirSubdir(t, "src")

	code, _, stderr := runRepoScoped([]string{"get", "breakpoints", "--no-agent"}, RunOptions{})
	require.Zero(t, code, stderr)
	require.Equal(t, []string{"src"}, srv.projectPaths())
	require.Equal(t, "workspace: src\n", stderr)
}

// misspelledScope is a scope file whose one document key is misspelled: it
// loads, and no entry is read from it.
const misspelledScope = "Environments:\n  127.0.0.1:\n    - name: checkout\n      service-names: [checkout]\n"

// A file that loads with no entry at all and a document key this dtctl does
// not know was meant to say something and says nothing: most likely the key
// is misspelled, and every entry under it would have named a workspace. It
// is treated as a file that does not load. Every change is refused, its dry
// run included, before anything is sent.
func TestBreakpoint_AScopeFileThatReadNoEntryRefusesEveryChange(t *testing.T) {
	commands := slices.Concat(mutatingBreakpointCommands, []struct {
		name string
		argv []string
	}{
		{"create", []string{"create", "breakpoint", "A.java:1"}},
		{"create with filters", []string{"create", "breakpoint", "A.java:1", "--filters", "k8s.namespace.name:web"}},
	})
	for _, tc := range commands {
		for _, mode := range []string{"", "--dry-run"} {
			t.Run(tc.name+mode, func(t *testing.T) {
				srv := newLiveDebuggerStub(t, "readwrite-all", webSetJSON)
				srv.rules = []interface{}{ruleAt("A.java", 1, "")}
				chdirTestRepo(t, misspelledScope)
				argv := slices.Concat(tc.argv, []string{"--agent", "--yes"})
				if mode != "" {
					argv = append(argv, mode)
				}

				code, stdout, _ := runRepoScoped(argv, RunOptions{})
				require.NotZero(t, code, stdout)
				detail := agentError(t, stdout)
				if slices.Contains(tc.argv, "--repo-scope") {
					// The name is looked up in the file, and is not in it.
					require.Equal(t, "not_found", detail.Code)
				} else {
					require.Equal(t, "validation_error", detail.Code)
					require.Equal(t, `.dtctl-repo-scope.yaml: no entry was read from it, and it holds unknown key "Environments" on line 1 (a typo, or written by a newer dtctl); it decides which Live Debugger workspace this command works in, so nothing was changed`, detail.Message)
				}
				require.Empty(t, srv.ops())
			})
		}
	}
}

// A read under such a file carries on in the working directory's workspace
// and says so, as under a file that does not load.
func TestBreakpoint_AScopeFileThatReadNoEntryWarnsEveryRead(t *testing.T) {
	const warning = `.dtctl-repo-scope.yaml: no entry was read from it, and it holds unknown key "Environments" on line 1 (a typo, or written by a newer dtctl); reading workspace src, the one named after the working directory, not the one the file would name`
	for _, argv := range [][]string{
		{"get", "breakpoints"},
		{"describe", "breakpoint", "A.java:1"},
		{"get", "snapshots", "A.java:1"},
	} {
		t.Run(strings.Join(argv[:2], " "), func(t *testing.T) {
			srv := newLiveDebuggerStub(t, "readonly")
			srv.rules = []interface{}{ruleAt("A.java", 1, "")}
			chdirTestRepo(t, misspelledScope)
			chdirSubdir(t, "src")

			code, stdout, stderr := runRepoScoped(append(argv, "--agent"), RunOptions{})
			require.Zero(t, code, "stdout: %s\nstderr: %s", stdout, stderr)
			require.Equal(t, []string{"src"}, srv.projectPaths())
			require.Equal(t, []string{warning}, agentEnvelope(t, stdout).Context.Warnings)
			require.True(t, strings.HasPrefix(stderr, "Warning: "+warning+"\n"), stderr)
		})
	}
}

// A document key this dtctl does not know, in a file whose entries name the
// workspace, is said by every breakpoint command, once, to a person and to
// an agent: the entries apply without it. A file with entries for another
// environment only says nothing here, whatever else it holds.
func TestBreakpoint_SaysTheScopeFilesUnknownKeys(t *testing.T) {
	const warning = `.dtctl-repo-scope.yaml: unknown key "owner" on line 1 (a typo, or written by a newer dtctl)`
	commands := slices.Concat(mutatingBreakpointCommands, []struct {
		name string
		argv []string
	}{
		{"create", []string{"create", "breakpoint", "A.java:1"}},
		{"create from an entry", []string{"create", "breakpoint", "A.java:1", "--repo-scope", "ledger"}},
		{"get breakpoints", []string{"get", "breakpoints"}},
		{"describe breakpoint", []string{"describe", "breakpoint", "A.java:1"}},
		{"get snapshots", []string{"get", "snapshots", "A.java:1"}},
	})
	for _, tc := range commands {
		t.Run(tc.name, func(t *testing.T) {
			srv := newLiveDebuggerStub(t, "readwrite-all", webSetJSON)
			srv.rules = []interface{}{ruleAt("A.java", 1, "")}
			chdirTestRepo(t, "owner: payments\n"+breakpointScope)

			argv := append(slices.Clone(tc.argv), "--agent")
			if tc.argv[0] != "get" && tc.argv[0] != "describe" {
				argv = append(argv, "--yes")
			}
			code, stdout, stderr := runRepoScoped(argv, RunOptions{})
			require.Zero(t, code, "stdout: %s\nstderr: %s", stdout, stderr)
			require.Equal(t, warning, agentEnvelope(t, stdout).Context.Warnings[0])
			require.Equal(t, 1, strings.Count(stdout, `unknown key`), stdout)
			require.True(t, strings.HasPrefix(stderr, "Warning: "+warning+"\n"), stderr)
			require.Equal(t, 1, strings.Count(stderr, "unknown key"), stderr)

			srv.reset()
			chdirTestRepo(t, "owner: payments\nenvironments:\n  other.example.invalid:\n    - name: checkout\n      service-names: [checkout]\n")
			code, stdout, stderr = runRepoScoped(argv, RunOptions{})
			if slices.Contains(tc.argv, "--repo-scope") {
				require.NotZero(t, code) // the name is not in the file
				return
			}
			require.Zero(t, code, "stdout: %s\nstderr: %s", stdout, stderr)
			require.NotContains(t, stdout+stderr, "unknown key")
		})
	}
}

// A name that does not resolve is the caller's mistake and is reported as
// such, whatever else is wrong: the workspace is resolved before a client is
// built, so a token that is missing does not hide it.
func TestBreakpoint_WorkspaceIsResolvedBeforeTheClient(t *testing.T) {
	for _, argv := range [][]string{
		{"get", "breakpoints"},
		{"describe", "breakpoint", "A.java:1"},
		{"get", "snapshots", "A.java:1"},
		{"delete", "breakpoint", "--all"},
		{"update", "breakpoint", "A.java:1", "--enabled=false"},
		{"create", "breakpoint", "A.java:1"},
	} {
		t.Run(argv[0]+" "+argv[1], func(t *testing.T) {
			writeIsolatedConfig(t, "current-context: c\ncontexts:\n  - name: c\n    context:\n      environment: http://127.0.0.1:1\n      token-ref: missing\n      safety-level: readwrite-all\n")
			optInRepoScope(t)
			chdirTestRepo(t, breakpointScope)

			code, stdout, _ := runRepoScoped(append(argv, "--agent", "--repo-scope", "nope"), RunOptions{})
			require.NotZero(t, code)
			require.Equal(t, "not_found", agentError(t, stdout).Code, stdout)
		})
	}
}

// A breakpoint that is not found is not found in one workspace, and the
// error says which: the workspace depends on where the command ran, so that
// is the first thing to check.
func TestBreakpoint_NotFoundNamesTheWorkspace(t *testing.T) {
	for _, argv := range [][]string{
		{"update", "breakpoint", "B.java:9", "--enabled=false"},
		{"delete", "breakpoint", "B.java:9", "--yes"},
		{"describe", "breakpoint", "B.java:9"},
		{"describe", "breakpoint", "dtctl-rule-9999"},
		{"get", "snapshots", "B.java:9"},
		{"get", "snapshots", "dtctl-rule-9999"},
	} {
		t.Run(strings.Join(argv[:3], " "), func(t *testing.T) {
			newLiveDebuggerStub(t, "readwrite-all").rules = []interface{}{ruleAt("A.java", 1, "")}
			chdirTestRepo(t, breakpointScope)
			chdirSubdir(t, filepath.Join("services", "ledger"))

			code, stdout, _ := runRepoScoped(append(argv, "--agent"), RunOptions{})
			require.NotZero(t, code)
			require.Contains(t, agentError(t, stdout).Message, " in workspace "+filepath.Base(filepath.Dir(filepath.Dir(mustGetwd(t))))+"/ledger")
		})
	}
}

// The error names the workspace, and a workspace is named after a directory:
// the agent code of a breakpoint that is not found is one code whatever the
// directory is called, and the exit code is the one it always was.
func TestBreakpoint_NotFoundHasOneCodeInEveryDirectory(t *testing.T) {
	for _, argv := range [][]string{
		{"describe", "breakpoint", "B.java:9"},
		{"describe", "breakpoint", "dtctl-rule-9999"},
		{"get", "snapshots", "B.java:9"},
		{"get", "snapshots", "dtctl-rule-9999"},
		{"update", "breakpoint", "B.java:9", "--enabled=false"},
		{"update", "breakpoint", "dtctl-rule-9999", "--condition", "a==1"},
		{"delete", "breakpoint", "B.java:9", "--yes"},
	} {
		for _, dir := range []string{"plain", "config-service", "timeout-worker", "invalid-data", "no context here"} {
			t.Run(strings.Join(argv[:3], " ")+" in "+dir, func(t *testing.T) {
				newLiveDebuggerStub(t, "readwrite-all").rules = []interface{}{ruleAt("A.java", 1, "")}
				t.Chdir(t.TempDir())
				chdirSubdir(t, dir)

				code, stdout, _ := runRepoScoped(append(slices.Clone(argv), "--agent"), RunOptions{})
				require.Equal(t, 1, code, stdout)
				detail := agentError(t, stdout)
				require.Equal(t, "not_found", detail.Code)
				require.True(t, strings.HasSuffix(detail.Message, " in workspace "+dir), detail.Message)
			})
		}
	}
}

// An identifier of nothing but spaces names no breakpoint. It is refused
// before anything is read, rather than sent as a rule id and reported done.
func TestBreakpoint_ABlankIdentifierNamesNothing(t *testing.T) {
	for _, argv := range [][]string{
		{"update", "breakpoint", "   ", "--enabled=false"},
		{"update", "breakpoint", "", "--condition", "a==1"},
		{"delete", "breakpoint", "   ", "--yes"},
		{"delete", "breakpoint", "", "--dry-run"},
		{"describe", "breakpoint", "   "},
		{"get", "snapshots", " "},
	} {
		t.Run(strings.Join(argv[:2], " ")+" "+strconv.Quote(argv[2]), func(t *testing.T) {
			srv := newLiveDebuggerStub(t, "readwrite-all")
			chdirTestRepo(t, breakpointScope)

			code, stdout, _ := runRepoScoped(append(slices.Clone(argv), "--agent"), RunOptions{})
			require.Equal(t, 2, code, stdout)
			require.Equal(t, "validation_error", agentError(t, stdout).Code)
			require.Equal(t, "the breakpoint identifier is empty: name a breakpoint by its id or as filename:line", agentError(t, stdout).Message)
			require.Empty(t, srv.ops())
		})
	}
}

func mustGetwd(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	require.NoError(t, err)
	return dir
}
