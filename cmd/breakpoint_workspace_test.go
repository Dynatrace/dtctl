package cmd

import (
	"os"
	"path/filepath"
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
	commands := append(mutatingBreakpointCommands[:len(mutatingBreakpointCommands):len(mutatingBreakpointCommands)], []struct {
		name string
		argv []string
	}{
		{"create", []string{"create", "breakpoint", "A.java:1"}},
		{"create with filters", []string{"create", "breakpoint", "A.java:1", "--filters", "k8s.namespace.name:web"}},
	}...)
	for _, tc := range commands {
		for _, mode := range []string{"", "--dry-run"} {
			t.Run(tc.name+mode, func(t *testing.T) {
				srv := newLiveDebuggerStub(t, "readwrite-all", webSetJSON)
				srv.rules = []interface{}{ruleAt("A.java", 1, "")}
				root := chdirTestRepo(t, brokenScope)
				chdirSubdir(t, "src")
				argv := append(tc.argv, "--agent", "--yes")
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
