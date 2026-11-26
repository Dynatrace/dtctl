package cmd

import (
	"context"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/spf13/cobra"

	"github.com/Dynatrace/dtctl/pkg/config"
	"github.com/Dynatrace/dtctl/pkg/output"
	"github.com/Dynatrace/dtctl/pkg/reposcope"
)

// breakpointWorkspace is the Live Debugger workspace of one invocation: every
// breakpoint command works in exactly one, and which one is decided here and
// nowhere else, before anything is sent.
type breakpointWorkspace struct {
	// name is the project path the environment keeps the workspace under.
	name string
	// scope is the resolution the name was taken from, in a checkout whose
	// scope file has entries for this environment: its Entry is the one
	// --repo-scope names or the one covering the working directory, nil
	// where none covers it. scope is nil when the working directory named
	// the workspace.
	scope *reposcope.Status
	// unusable is why the repository's scope file is there and did not
	// load, naming the file as the repository does. The workspace is then
	// the working directory's, which is not where a loading file would put
	// this command.
	unusable string
}

// resolveBreakpointWorkspace names the workspace of this invocation.
//
// In a checkout whose scope file has entries for this environment the
// workspace is named after the repository (see repositoryWorkspace):
// <repo>/<name> with --repo-scope <name>, wherever the command runs;
// otherwise <repo>/<entry> where an entry covers the working directory, so
// every directory an entry covers shares one workspace, under the same name
// on every machine, and two entries never share one; and <repo> in any other
// directory of that checkout, one two entries tie on included.
//
// Everywhere else it is named after the working directory, the name the
// breakpoint commands have always used (see directoryWorkspace): outside a
// checkout, in one without a scope file, and in one whose file has no entry
// for this environment, so that a file written for another environment moves
// nobody's breakpoints. An embedded invocation has no working directory (see
// currentWorkDir) and gets "no-project".
//
// Naming is not scoping: DTCTL_NO_REPO_SCOPE and the stability floor leave
// the name alone, so every breakpoint command finds the same workspace, and
// the commands that set no filters take --repo-scope only to name it. A name
// that does not resolve is an error.
//
// A scope file that is there and does not load names nothing, and the result
// says so in unusable: the file would have decided the workspace, so a
// command that changes something must refuse (requireScopeFile) rather than
// work in the directory's workspace, and one that only reads says where it
// read (readWarnings).
func resolveBreakpointWorkspace(cmd *cobra.Command, cfg *config.Config) (breakpointWorkspace, error) {
	ctx := cmdContext(cmd)
	name, _ := cmd.Flags().GetString(repoScopeFlag)
	scope, err := openRepoScope(ctx, cfg)
	switch {
	case err != nil && scope != nil && name != "":
		return breakpointWorkspace{}, unusableScopeFile(scope.unusable(err), "--"+repoScopeFlag+" "+name+" names an entry of it, so nothing was sent")
	case err != nil && name != "":
		return breakpointWorkspace{}, err
	case err != nil && scope != nil:
		return breakpointWorkspace{name: workingDirectoryWorkspace(ctx), unusable: scope.unusable(err)}, nil
	case err != nil || (name == "" && len(scope.file.Entries(scope.host)) == 0):
		return breakpointWorkspace{name: workingDirectoryWorkspace(ctx)}, nil
	}
	status, err := scope.resolve(name)
	if err != nil {
		return breakpointWorkspace{}, err
	}
	ws := breakpointWorkspace{name: repositoryWorkspace(scope.loc), scope: status}
	if status.Entry != nil {
		ws.name += "/" + status.Entry.Name
	}
	return ws, nil
}

// shown is the name as a message prints it. A workspace named after a
// directory carries whatever the directory is called, and a name can hold an
// escape sequence; the environment is sent the name itself.
func (w breakpointWorkspace) shown() string {
	return repoScopeText(w.name)
}

// requireScopeFile refuses a command that changes something when the scope
// file did not load. The file decides which workspace the command works in,
// and the working directory's is a different one: deleting or re-scoping
// breakpoints there would hit the wrong ones, and creating one there would
// put it where no later command looks. A dry run is refused the same way,
// since it cannot say where the real run would work.
func (w breakpointWorkspace) requireScopeFile() error {
	if w.unusable == "" {
		return nil
	}
	return unusableScopeFile(w.unusable, "it decides which Live Debugger workspace this command works in, so nothing was changed")
}

// changeNotes is what a command that changes something says about the
// workspace first. A workspace named after the working directory is what it
// has always been and needs no line. One named after the repository is new,
// and two things it relies on have not been tried against an environment:
// that a name may hold the "/" of <repo>/<entry>, and that a workspace
// belongs to one user, since every clone of the repository now names the
// same one.
func (w breakpointWorkspace) changeNotes() []string {
	switch {
	case w.scope == nil:
		return nil
	case strings.Contains(w.name, "/"):
		return []string{"workspace " + w.shown() + ` is named after the repository, so every clone of it names the same one: it is not verified that Live Debugger accepts "/" in a workspace name, nor that a workspace is private to its user`}
	}
	return []string{"workspace " + w.shown() + " is named after the repository, so every clone of it names the same one: it is not verified that a workspace is private to its user"}
}

// unusableScopeFile is the error of a command that needs the scope file and
// found one that does not load, ending with what follows from that. unusable
// is repoScopeFile.unusable's reason, which names the file without its path
// on this machine.
func unusableScopeFile(unusable, consequence string) error {
	return &reposcope.InvalidError{
		Source:      reposcope.FileName,
		Msg:         strings.TrimPrefix(unusable, reposcope.FileName+": ") + "; " + consequence,
		Suggestions: []string{"dtctl repo-scope current says what is wrong with the file; repair it, then run the command again"},
	}
}

// readWarnings is what a command that only reads says about the workspace it
// read: nothing, unless the scope file did not load and the working
// directory named it. It prints the lines on stderr and returns them for the
// agent envelope's context.warnings.
func (w breakpointWorkspace) readWarnings(ctx context.Context) []string {
	if w.unusable == "" {
		return nil
	}
	warning := w.unusable + "; reading workspace " + w.shown() + ", the one named after the working directory, not the one the file would name"
	output.FprintWarning(currentStderr(ctx), "%s", warning)
	return []string{warning}
}

// workingDirectoryWorkspace names the workspace of an invocation after its
// working directory, or "no-project" when it has none.
func workingDirectoryWorkspace(ctx context.Context) string {
	cwd, err := currentWorkDir(ctx)
	if err != nil {
		return "no-project"
	}
	return directoryWorkspace(cwd)
}

// directoryWorkspace names a workspace after a directory: its base name, or
// "no-project" for a directory that has none, the root.
func directoryWorkspace(dir string) string {
	base := filepath.Base(dir)
	if base == "" || base == "." || base == string(filepath.Separator) {
		return "no-project"
	}
	return base
}

// plainRepositoryName is what a repository name may be to name a workspace:
// letters, digits, ".", "_" and "-".
var plainRepositoryName = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)

// repositoryWorkspace is the name the workspaces of a linked checkout start
// with: the origin remote's repository name, so every clone uses the same
// one, or the checkout directory's when there is no origin to read (a
// worktree) or its last segment is not a plain name. The remote is text from
// a file, and the name is sent to the environment and printed: a query
// string, a quote or a parent directory in it names no repository.
func repositoryWorkspace(loc reposcope.Location) string {
	repo := reposcope.RepositoryName(reposcope.RepoFS(loc))
	if !plainRepositoryName.MatchString(repo) || repo == "." || repo == ".." {
		return directoryWorkspace(loc.Root)
	}
	return repo
}
