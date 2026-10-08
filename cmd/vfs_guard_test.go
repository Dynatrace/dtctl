package cmd

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// hostFileAccessRoots are the trees the guard scans, relative to this package's
// directory. cmd/ holds the flag plumbing and pkg/ the code it delegates to —
// a user-named path is just as request-bound one call deeper, which is exactly
// how `dtctl diff -f a.yaml -f b.yaml` (pkg/diff) and `dtctl inspect <file>`
// (pkg/inspect) escaped the seam while a cmd/-only scan stayed green.
//
// sdk/ is deliberately out of scope: it is a separate module with no notion of
// a dtctl invocation, and sdk/session owns the config file and credential
// stores by contract (see docs/dev/CONFIG_CONTRACT.md).
var hostFileAccessRoots = []string{"..", "../pkg"}

// hostFileAccessAllowlist records every file permitted to touch the host
// filesystem directly, keyed by slash-separated path from the repository root,
// with the reason it is not request state. A file not listed here must read and
// write user-supplied paths through pkg/vfs.
//
// The distinction is *whose* file it is:
//
//   - a path the user named on the command line (-f, --file, --data-file, a
//     query file, a file to diff) is request state — under an embedded
//     invocation it exists only in the request's virtual filesystem, so it must
//     go through vfs
//   - a path dtctl chose itself (an editor round-trip temp file, the config
//     file, a spill buffer) is host state and belongs on the host disk
//
// Host state is only defensible when an embedded invocation cannot reach it, so
// each entry below names what keeps it unreachable: a blocked command, an
// ungranted capability, or both.
var hostFileAccessAllowlist = map[string]string{
	// The config file is host state by definition, and the whole `config`
	// command tree is unavailable in a service (engine policy).
	"cmd/config.go": "reads/writes the local dtctl config file — host state, and `config` is blocked in a service",

	// Editor round-trip files are dtctl's own scratch space: written to the
	// host, opened in the host's editor, read back. `edit` requires the Editor
	// capability and is blocked in a service, so this never runs embedded.
	"cmd/edit_anomalydetector.go": "editor round-trip temp file (Editor capability, blocked in a service)",
	"cmd/edit_aws.go":             "editor round-trip temp file (Editor capability, blocked in a service)",
	"cmd/edit_azure.go":           "editor round-trip temp file (Editor capability, blocked in a service)",
	"cmd/edit_documents.go":       "editor round-trip temp file (Editor capability, blocked in a service)",
	"cmd/edit_gcp.go":             "editor round-trip temp file (Editor capability, blocked in a service)",
	"cmd/edit_segments.go":        "editor round-trip temp file (Editor capability, blocked in a service)",
	"cmd/edit_settings.go":        "editor round-trip temp file (Editor capability, blocked in a service)",
	"cmd/edit_workflows.go":       "editor round-trip temp file (Editor capability, blocked in a service)",

	// Test-only helper: golden files are repository state, read and written by
	// the test suite, never by a command.
	"cmd/testutil/golden.go": "golden-file helper for tests — repository state, not reachable from a command",

	// Spill machinery writes results to, and lists them from, a host directory
	// dtctl chooses. It requires the HostDiskSpill capability, which embedded
	// callers do not grant: a spilled file would outlive the request on the
	// server's disk and the path returned would be unreadable by the caller.
	"pkg/output/spill.go":      "writes/prunes dtctl's own spill directory (HostDiskSpill capability, never granted embedded)",
	"pkg/output/spill_list.go": "enumerates dtctl's own spill directory (HostDiskSpill capability, never granted embedded)",
	"pkg/inspect/reader.go":    "streams a spilled result file from the host disk; `inspect` is blocked in a service and nothing spills there",

	// Plugin discovery stats candidate executables on PATH; skills installation
	// writes files into locally installed AI assistants' directories. Both are
	// host state, gated by the PluginDispatch capability and the `skills` block.
	"pkg/plugin/plugin.go":    "stats plugin executables on the host PATH (PluginDispatch capability, never granted embedded)",
	"pkg/skills/installer.go": "installs skill files for locally installed assistants — host state, and `skills` is blocked in a service",

	// The repo scope file is found by walking up from the working directory
	// to the enclosing git repository: the CLI's own host state.
	"pkg/reposcope/store.go": "the repo scope file (.dtctl-repo-scope.yaml) is host state found by walking up from the working directory — reachable only with the HostWorkingDirectory capability, which embedded callers do not grant, and `repo-scope` is blocked in a service",

	// The seam itself: osFS is what "the host filesystem" means.
	"pkg/vfs/vfs.go": "the vfs seam's own host-filesystem implementation",
}

// hostFileAccessCalls are the direct host-filesystem entry points that bypass
// the vfs seam. os.Stdin/os.Stdout/os.Stderr are deliberately absent: the
// stream seam swaps those variables per invocation (see stdio.go), so using
// them is correct. Opening "/dev/stdin" as a *path* is not — that reaches the
// process's real fd 0, past the redirection, and does not exist on Windows.
var hostFileAccessCalls = []string{
	"os.ReadFile(",
	"os.WriteFile(",
	"os.Open(",
	"os.OpenFile(",
	"os.Create(",
	"os.ReadDir(",
	`"/dev/stdin"`,
}

// TestUserFilePathsGoThroughVFS is the E6 guard: user-supplied file paths must
// resolve through pkg/vfs, never through os directly. A service request's
// files exist only in the request — a direct os.ReadFile in a command (or in
// the package it delegates to) silently reads the *server's* disk instead,
// which is both a broken feature and a path traversal into host state.
//
// Adding a file to hostFileAccessAllowlist is a deliberate act: it asserts the
// path is dtctl's own scratch/host state, not something the user named, and
// names what keeps an embedded invocation from reaching it.
func TestUserFilePathsGoThroughVFS(t *testing.T) {
	scanned := 0
	for _, root := range hostFileAccessRoots {
		err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() {
				// ".." is the repository root: descend into cmd/ (this package
				// and its helpers) but not into its sibling trees, which the
				// roots list covers explicitly or excludes by design.
				if root == ".." && path != ".." && filepath.Base(path) != "cmd" {
					return filepath.SkipDir
				}
				if skipDir(filepath.Base(path)) {
					return filepath.SkipDir
				}
				return nil
			}
			name := d.Name()
			if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
				return nil
			}
			scanned++

			src, readErr := os.ReadFile(filepath.Clean(path))
			require.NoError(t, readErr)
			text := string(src)

			rel := repoRelative(path)
			for _, call := range hostFileAccessCalls {
				if !strings.Contains(text, call) {
					continue
				}
				reason, allowed := hostFileAccessAllowlist[rel]
				require.True(t, allowed,
					"%s calls %s directly — user-supplied paths must go through pkg/vfs "+
						"(vfs.ReadFile / vfs.WriteFile / vfs.ReadFileOrStdin) so embedded "+
						"invocations resolve them against the request's virtual files. If this "+
						"path is dtctl's own scratch or host state, add %s to "+
						"hostFileAccessAllowlist with the reason it cannot be reached from an "+
						"embedded invocation.", rel, call, rel)
				require.NotEmpty(t, reason)
			}
			return nil
		})
		require.NoError(t, err)
	}
	// A scan that silently walks nothing would pass forever.
	require.Greater(t, scanned, 100, "guard scanned suspiciously few files")
}

// skipDir excludes trees that hold no invocation code: generated protobuf
// bindings, test fixtures, and vendored or hidden directories.
func skipDir(name string) bool {
	switch name {
	case "proto", "testdata", "vendor", "node_modules":
		return true
	}
	return strings.HasPrefix(name, ".")
}

// repoRelative turns a walk path (relative to cmd/) into a slash-separated
// path from the repository root, so allowlist keys read the way a contributor
// would write them.
func repoRelative(path string) string {
	clean := filepath.ToSlash(filepath.Clean(path))
	if trimmed, ok := strings.CutPrefix(clean, "../"); ok {
		return trimmed
	}
	return "cmd/" + clean
}
