package cmd

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// notedScope is one entry that binds everything a workspace's filters do
// not carry, or carry unverified.
const notedScope = `environments:
  127.0.0.1:
    - name: checkout
      services: [SERVICE-0123456789ABCDEF]
      service-names: [checkout]
      workloads:
        - cluster-uid: 11111111-2222-3333-4444-555555555555
          namespace: payments
          name: checkout
      segments:
        - id: Ab3dEf9hIjk
`

// The lines a command says about notedScope's entry, in order.
var entryNotes = []string{
	"repo scope checkout's service ids are not sent: Live Debugger does not honour dt.entity.service as a workspace label",
	"repo scope checkout binds a cluster, which a Live Debugger workspace is not narrowed by: the filters include the same workload in other clusters",
	"repo scope checkout's filter segments are not applied: a workspace is filtered by labels and takes no segment, so the filters are wider than the entry's queries",
	"repo scope checkout sets service.name, which is not verified as a workspace label: a filter set a workspace does not honour selects nothing, and a breakpoint under it is created and never hits; check with dtctl --context c describe breakpoint <id> --repo-scope checkout",
}

// workspaceNote is the line a command that changes something in a workspace
// of a linked checkout says about it. The test repositories have no origin
// remote, so the checkout directory names their workspaces.
func workspaceNote(workspace string) string {
	const byDirectory = " is named after the checkout directory, as no origin remote names the repository, so a clone in a directory of another name works in another one"
	if strings.Contains(workspace, "/") {
		return "workspace " + workspace + byDirectory + `: it is not verified that Live Debugger accepts "/" in a workspace name`
	}
	return "workspace " + workspace + byDirectory
}

// originWorkspaceNote is workspaceNote for a workspace the origin remote
// named.
func originWorkspaceNote(workspace string) string {
	if strings.Contains(workspace, "/") {
		return "workspace " + workspace + ` is named after the repository, so every clone of it names the same one: it is not verified that Live Debugger accepts "/" in a workspace name, nor that a workspace is private to its user`
	}
	return "workspace " + workspace + " is named after the repository, so every clone of it names the same one: it is not verified that a workspace is private to its user"
}

// Only a workspace the origin remote named is the same one in every clone,
// and only there the line says so, with what that relies on.
func TestBreakpoint_OnlyAnOriginNamedWorkspaceIsSharedByEveryClone(t *testing.T) {
	newLiveDebuggerStub(t, "readwrite-all", webSetJSON)
	root := chdirTestRepo(t, breakpointScope)
	require.NoError(t, os.WriteFile(filepath.Join(root, ".git", "config"),
		[]byte("[remote \"origin\"]\n\turl = git@example.invalid:shop/storefront.git\n"), 0o644))

	notes := func() []string {
		code, stdout, stderr := runRepoScoped([]string{"update", "breakpoint", "--filters", "k8s.namespace.name:web", "--agent", "--dry-run"}, RunOptions{})
		require.Zero(t, code, stderr)
		return agentEnvelope(t, stdout).Context.Warnings
	}
	require.Equal(t, []string{originWorkspaceNote("storefront/checkout")}, notes())

	chdirTestRepo(t, "environments:\n  127.0.0.1:\n    - name: ledger\n      path: services/ledger\n      service-names: [ledger]\n")
	root, _ = os.Getwd()
	require.NoError(t, os.WriteFile(filepath.Join(root, ".git", "config"),
		[]byte("[remote \"origin\"]\n\turl = git@example.invalid:shop/storefront.git\n"), 0o644))
	require.Equal(t, []string{originWorkspaceNote("storefront")}, notes())

	// A worktree: .git is a file, and there is no origin to read.
	root = t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(root, ".git"), []byte("gitdir: elsewhere\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(root, ".dtctl-repo-scope.yaml"), []byte(breakpointScope), 0o644))
	t.Chdir(root)
	require.Equal(t, []string{workspaceNote(filepath.Base(root) + "/checkout")}, notes())
	require.NotContains(t, notes()[0], "every clone")
}

// Filters set from an entry are not the entry: what they leave out, and what
// they carry unverified, is said on every path that sets or previews them,
// to a person on stderr and to an agent in context.warnings, and before the
// confirmation, so a run that is declined has said it too.
func TestBreakpoint_SaysWhatAnEntrysFiltersLeaveOut(t *testing.T) {
	for _, tc := range []struct {
		name string
		argv []string
	}{
		{"create", []string{"create", "breakpoint", "A.java:1", "--repo-scope", "checkout"}},
		{"create, dry run", []string{"create", "breakpoint", "A.java:1", "--repo-scope", "checkout", "--dry-run"}},
		{"update", []string{"update", "breakpoint", "--repo-scope", "checkout"}},
		{"update, dry run", []string{"update", "breakpoint", "--repo-scope", "checkout", "--dry-run"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := newLiveDebuggerStub(t, "readwrite-all")
			srv.activeRules = 1
			want := append([]string{workspaceNote(filepath.Base(chdirTestRepo(t, notedScope)) + "/checkout")}, entryNotes...)

			code, stdout, stderr := runRepoScoped(append(tc.argv, "--agent"), RunOptions{})
			require.Zero(t, code, "stdout: %s\nstderr: %s", stdout, stderr)
			require.Equal(t, want, agentEnvelope(t, stdout).Context.Warnings)
			require.Equal(t, strings.Join(want, "\n")+"\n", stderr)

			srv.reset()
			code, stdout, stderr = runRepoScoped(append(tc.argv, "--no-agent"), RunOptions{Stdin: strings.NewReader("n\n")})
			require.Zero(t, code, "stdout: %s\nstderr: %s", stdout, stderr)
			require.Equal(t, strings.Join(want, "\n")+"\n", stderr)
			require.Empty(t, srv.mutations())
		})
	}
}

// Every command that changes something in a workspace named after the
// repository says what about such a workspace is not verified, before it
// changes anything. A workspace named after the working directory is what
// it always was, and gets no line.
func TestBreakpoint_ChangesInARepositoryWorkspaceSayWhatIsUnverified(t *testing.T) {
	commands := append([]struct {
		name string
		argv []string
	}{{"create", []string{"create", "breakpoint", "A.java:1"}}}, mutatingBreakpointCommands...)
	for _, tc := range commands {
		if slices.Contains(tc.argv, "--repo-scope") {
			continue // needs the file; covered above
		}
		t.Run(tc.name, func(t *testing.T) {
			srv := newLiveDebuggerStub(t, "readwrite-all", webSetJSON)
			srv.rules = []interface{}{ruleAt("A.java", 1, "")}
			repo := filepath.Base(chdirTestRepo(t, "environments:\n  127.0.0.1:\n    - name: ledger\n      path: services/ledger\n      service-names: [ledger]\n"))

			code, stdout, stderr := runRepoScoped(append(tc.argv, "--agent", "--yes"), RunOptions{})
			require.Zero(t, code, "stdout: %s\nstderr: %s", stdout, stderr)
			require.Equal(t, []string{workspaceNote(repo)}, agentEnvelope(t, stdout).Context.Warnings)
			require.Equal(t, workspaceNote(repo)+"\n", stderr)

			chdirSubdir(t, filepath.Join("services", "ledger"))
			code, stdout, stderr = runRepoScoped(append(tc.argv, "--agent", "--dry-run", "--yes"), RunOptions{})
			require.Zero(t, code, "stdout: %s\nstderr: %s", stdout, stderr)
			require.Equal(t, []string{workspaceNote(repo + "/ledger")}, agentEnvelope(t, stdout).Context.Warnings)

			t.Chdir(t.TempDir())
			code, stdout, stderr = runRepoScoped(append(tc.argv, "--agent", "--yes"), RunOptions{})
			require.Zero(t, code, "stdout: %s\nstderr: %s", stdout, stderr)
			require.Empty(t, agentEnvelope(t, stdout).Context.Warnings)
			require.Empty(t, stderr)
		})
	}
}

// afterWorkspaceNote asserts that stderr opens with the line a change in a
// workspace named after the repository says, and returns what follows it.
func afterWorkspaceNote(t *testing.T, stderr, workspace string) string {
	t.Helper()
	rest, ok := strings.CutPrefix(stderr, workspaceNote(workspace)+"\n")
	require.True(t, ok, "stderr does not open with the workspace note: %s", stderr)
	return rest
}
