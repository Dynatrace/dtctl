package cmd

import (
	"context"
	"errors"
	"fmt"
	"path"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/dynatrace-oss/dtctl/pkg/config"
	"github.com/dynatrace-oss/dtctl/pkg/output"
	"github.com/dynatrace-oss/dtctl/pkg/reposcope"
	"github.com/dynatrace-oss/dtctl/pkg/stability"
	"github.com/dynatrace-oss/dtctl/sdk/urls"
)

// repoScopeSince is the release that introduced repo scopes. The command tree
// and the query flags that apply a scope all declare it.
const repoScopeSince = "0.43.0"

// The advice the repo-scope commands give in more than one place.
const (
	repoScopeDiscoverHint = "run 'dtctl repo-scope discover' to find the entities this repository runs as"
	repoScopeNarrowHint   = "add a name you know with --term <name>, or discover for another directory with --path <dir>"
	repoScopeCheckoutHint = "run dtctl from inside a git checkout"
	repoScopeNoFileReason = "this repository has no " + reposcope.FileName
)

var repoScopeCmd = newRepoScopeCmd()

func newRepoScopeCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "repo-scope",
		Short: "Link this git repository to the entities that run its code",
		Long: `Link this git repository to the entities that run its code.

The link is .dtctl-repo-scope.yaml at the repository root: per environment, the
services, process groups, service names and Kubernetes workloads that run the
repository's code. Inside a linked repository 'dtctl query', 'wait query' and
'verify query' narrow 'fetch logs' and 'fetch spans' to them and say so on
stderr. Commit the file to share it. Nothing here writes to Dynatrace.

When called without a subcommand, lists the entries for the current context's
environment.

Examples:
  # Find the entities that run this repository's code
  dtctl repo-scope discover

  # Save the candidate discovery proposed
  dtctl repo-scope set checkout --namespace payments --workload checkout --service-name checkout

  # Show the entry that applies in this directory, or why none does
  dtctl repo-scope current

  # Run one query unscoped
  dtctl query 'fetch logs | limit 20' --no-repo-scope
`,
		Args: cobra.NoArgs,
		RunE: listRepoScopes,
	}
	// Experimental: the bindings a file can carry, how they render into DQL
	// and what discovery proposes may still change. Marked on the subtree
	// root; the subcommands inherit it, and addRepoScopeFlags marks the query
	// flags.
	stability.Mark(c, stability.Experimental, repoScopeSince)
	return c
}

var repoScopeSetCmd = newRepoScopeSetCmd()

func newRepoScopeSetCmd() *cobra.Command {
	var (
		services      []string
		processGroups []string
		serviceNames  []string
		namespace     string
		workloads     []string
		entryPaths    []string
	)
	c := &cobra.Command{
		Use:   "set <name>",
		Short: "Create or replace an entry for the current context's environment",
		Long: `Create or replace an entry for the current context's environment.

Writes the named entry to .dtctl-repo-scope.yaml at the repository root, under
the current context's environment host. 'set' replaces the whole entry, so pass
every binding it should keep. Every value is checked before anything is
written, and the new file is renamed over the old one, so a reader never sees
half of it. Nothing is sent to Dynatrace.

Examples:
  # Bind a Kubernetes workload and its OpenTelemetry service name
  dtctl repo-scope set checkout --namespace payments --workload checkout --service-name checkout

  # Bind entity ids, for one directory of a monorepo
  dtctl repo-scope set ledger --path services/ledger \
    --service SERVICE-0123456789ABCDEF --process-group PROCESS_GROUP-FEDCBA9876543210

  # The same entry for the staging environment
  dtctl --context staging repo-scope set checkout --service-name checkout
`,
		Args:              cobra.ExactArgs(1),
		ValidArgsFunction: completeRepoScopeNames,
		RunE: func(cmd *cobra.Command, args []string) error {
			entry := reposcope.Entry{Name: args[0], Services: services, ProcessGroups: processGroups, ServiceNames: serviceNames}
			for _, p := range entryPaths {
				entry.Paths = append(entry.Paths, path.Clean(filepath.ToSlash(p)))
			}
			// One --path is the entry's path; several are its paths, the
			// first being its path too.
			if len(entry.Paths) > 0 {
				entry.Path = entry.Paths[0]
			}
			if len(entry.Paths) == 1 {
				entry.Paths = nil
			}
			for _, w := range workloads {
				entry.Workloads = append(entry.Workloads, reposcope.Workload{Namespace: namespace, Name: w})
			}
			return setRepoScope(cmd, entry)
		},
	}
	c.Flags().StringArrayVar(&services, "service", nil, "service id, SERVICE-<16 hex digits> (repeatable)")
	c.Flags().StringArrayVar(&processGroups, "process-group", nil, "process group id, PROCESS_GROUP-<16 hex digits> (repeatable)")
	c.Flags().StringArrayVar(&serviceNames, "service-name", nil, "OpenTelemetry service.name (repeatable)")
	c.Flags().StringVar(&namespace, "namespace", "", "Kubernetes namespace of the --workload names")
	c.Flags().StringArrayVar(&workloads, "workload", nil, "Kubernetes workload name in --namespace (repeatable)")
	c.Flags().StringArrayVar(&entryPaths, "path", nil, "directory the entry covers, relative to the repository root (repeatable; default: the whole repository)")
	for _, name := range []string{"service", "process-group", "service-name", "namespace", "workload", "path"} {
		rejectEmptyFlag(c, name)
	}
	c.MarkFlagsRequiredTogether("namespace", "workload")
	return c
}

var repoScopeCurrentCmd = newRepoScopeCurrentCmd()

func newRepoScopeCurrentCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "current",
		Short: "Show the entry that applies in the working directory",
		Long: `Show the entry that queries made from the working directory are scoped to, or
why none applies. Reads only the local file, and exits 0 whenever it can answer.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return showRepoScope(cmd, "current", "")
		},
	}
}

var repoScopeDescribeCmd = newRepoScopeDescribeCmd()

func newRepoScopeDescribeCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "describe [name]",
		Short: "Show an entry's bindings and the filters they render",
		Long: `Show an entry's bindings, the directory it covers, and the filter it inserts
after 'fetch logs' and 'fetch spans', or why it inserts none. Without a name,
describes the entry that applies in the working directory.`,
		Args:              cobra.MaximumNArgs(1),
		ValidArgsFunction: completeRepoScopeNames,
		RunE: func(cmd *cobra.Command, args []string) error {
			name := ""
			if len(args) == 1 {
				name = args[0]
			}
			return showRepoScope(cmd, "describe", name)
		},
	}
}

var repoScopeDeleteCmd = newRepoScopeDeleteCmd()

func newRepoScopeDeleteCmd() *cobra.Command {
	return &cobra.Command{
		Use:               "delete <name>",
		Aliases:           []string{"rm"},
		Short:             "Delete an entry for the current context's environment",
		Long:              "Delete the named entry for the current context's environment. The file goes with its last entry.",
		Args:              cobra.ExactArgs(1),
		ValidArgsFunction: completeRepoScopeNames,
		RunE: func(cmd *cobra.Command, args []string) error {
			return deleteRepoScope(cmd, args[0])
		},
	}
}

var repoScopeListCmd = newRepoScopeListCmd()

func newRepoScopeListCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "list",
		Aliases: []string{"ls"},
		Short:   "List the entries for the current context's environment",
		Args:    cobra.NoArgs,
		RunE:    listRepoScopes,
	}
}

// repoScopeHost is the key the scope file uses for the active context's
// environment: its host, because a context name means nothing on another
// developer's machine and the file is committed for all of them.
func repoScopeHost(cfg *config.Config) (string, error) {
	current, err := cfg.CurrentContextObj()
	if err != nil {
		return "", err
	}
	host := urls.Host(current.Environment)
	if host == "" {
		return "", fmt.Errorf("context %q has no environment URL to key a repo scope by", cfg.CurrentContext)
	}
	return host, nil
}

// repoScopeLocation finds where the scope file of the working directory's
// repository lives. Outside a git repository that is an InvalidError, so a
// command asked about a repository says why there is none.
func repoScopeLocation(ctx context.Context) (reposcope.Location, error) {
	dir, err := currentWorkDir(ctx)
	if err != nil {
		return reposcope.Location{}, err
	}
	loc, ok, err := reposcope.Locate(dir)
	if err != nil {
		return reposcope.Location{}, err
	}
	if !ok {
		return reposcope.Location{}, &reposcope.InvalidError{
			Source:      dir,
			Msg:         "is not inside a git repository: a repo scope lives in " + reposcope.FileName + " at the repository root",
			Suggestions: []string{repoScopeCheckoutHint},
		}
	}
	return loc, nil
}

// repoScopeFile is the scope file of the working directory's repository, read
// for the current context's environment.
type repoScopeFile struct {
	loc  reposcope.Location
	host string
	file *reposcope.File
}

// openRepoScope reads the scope file. A missing file reads as an empty one,
// so set can start from it. When the file is there but does not load, the
// scope comes back together with the error, so a caller that reports rather
// than fails can still say where; every earlier failure returns no scope.
func openRepoScope(ctx context.Context, cfg *config.Config) (*repoScopeFile, error) {
	host, err := repoScopeHost(cfg)
	if err != nil {
		return nil, err
	}
	loc, err := repoScopeLocation(ctx)
	if err != nil {
		return nil, err
	}
	s := &repoScopeFile{loc: loc, host: host}
	s.file, err = reposcope.Load(loc)
	return s, err
}

// resolve picks the entry named, or the one covering the working directory.
func (s *repoScopeFile) resolve(name string) (*reposcope.Status, error) {
	if !s.loc.Exists && name == "" {
		return &reposcope.Status{Environment: s.host, Dir: s.loc.RelDir, Reason: repoScopeNoFileReason}, nil
	}
	return reposcope.Resolve(s.file, s.host, s.loc.RelDir, name)
}

// openRepoScopeForEdit is openRepoScope for the commands that need the file
// intact.
func openRepoScopeForEdit(ctx context.Context) (*repoScopeFile, error) {
	cfg, err := loadConfig(ctx)
	if err != nil {
		return nil, err
	}
	s, err := openRepoScope(ctx, cfg)
	if err != nil {
		return nil, err
	}
	return s, nil
}

// repoScopeStatus is what current and describe report. Without a name, every
// answer short of a broken config is a Status, outside a repository and with
// a file that does not validate included, because these commands are how a
// user finds out why a query was not scoped. With a name, what does not
// resolve is the error.
func repoScopeStatus(ctx context.Context, name string) (*reposcope.Status, error) {
	cfg, err := loadConfig(ctx)
	if err != nil {
		return nil, err
	}
	scope, err := openRepoScope(ctx, cfg)
	var invalid *reposcope.InvalidError
	switch {
	case err == nil:
		return scope.resolve(name)
	case name != "" || !errors.As(err, &invalid):
		return nil, err
	case scope == nil:
		return &reposcope.Status{Reason: "not inside a git repository"}, nil
	}
	return &reposcope.Status{File: reposcope.FileName, Environment: scope.host, Dir: scope.loc.RelDir,
		Reason: reposcope.FileName + ": " + invalid.Msg}, nil
}

// showRepoScope is current and describe: the same Status, one line or in
// detail for a terminal, as is in every other format.
func showRepoScope(cmd *cobra.Command, verb, name string) error {
	ctx := cmdContext(cmd)
	status, err := repoScopeStatus(ctx, name)
	if err != nil {
		return err
	}
	if outputFormat(ctx) == "table" && !agentMode(ctx) {
		if status.Warning != "" {
			output.FprintWarning(currentStderr(ctx), "%s: %s", reposcope.FileName, status.Warning)
		}
		if verb == "current" {
			printRepoScopeCurrent(currentStdout(ctx), status)
		} else {
			printRepoScopeDescribe(currentStdout(ctx), status)
		}
		return nil
	}
	printer := newPrinterCtx(ctx)
	if ap := enrichAgent(printer, verb, "repo-scope"); ap != nil {
		ap.SetSuggestions(repoScopeStatusSuggestions(status))
	}
	return printer.Print(status)
}

// repoScopeStatusSuggestions are the next steps an agent reading a Status can
// take.
func repoScopeStatusSuggestions(s *reposcope.Status) []string {
	switch {
	case s.Linked:
		return []string{fmt.Sprintf("'dtctl query' narrows fetch logs and fetch spans to %q here; add --%s to run a query unscoped", s.Entry.Name, noRepoScopeFlag)}
	case s.Environment == "":
		return []string{repoScopeCheckoutHint}
	case len(s.Others) > 0:
		return []string{"no entry covers this directory; pass --repo-scope <name> to use one of: " + strings.Join(s.Others, ", ")}
	}
	return []string{repoScopeDiscoverHint}
}

// setRepoScope writes entry for the current context's environment.
func setRepoScope(cmd *cobra.Command, entry reposcope.Entry) error {
	ctx := cmdContext(cmd)
	// Checked here rather than left to Save, so the error names the flags
	// instead of the file.
	if len(entry.Services)+len(entry.ProcessGroups)+len(entry.ServiceNames)+len(entry.Workloads) == 0 {
		return &reposcope.InvalidError{
			Source:      "repo-scope set",
			Msg:         "an entry needs at least one of --service, --process-group, --service-name, or --namespace with --workload",
			Suggestions: []string{repoScopeDiscoverHint},
		}
	}
	scope, err := openRepoScopeForEdit(ctx)
	if err != nil {
		return err
	}
	scope.file.Upsert(scope.host, entry)
	if err := reposcope.Save(scope.loc, scope.file); err != nil {
		return err
	}

	if agentMode(ctx) {
		status, err := reposcope.Resolve(scope.file, scope.host, scope.loc.RelDir, entry.Name)
		if err != nil {
			return err
		}
		printer := newPrinterCtx(ctx)
		if ap := enrichAgent(printer, "set", "repo-scope"); ap != nil {
			ap.SetSuggestions([]string{"dtctl repo-scope current", "dtctl query 'fetch logs | limit 20'"})
		}
		return printer.Print(status)
	}
	output.FprintSuccess(currentStderr(ctx), "Repo scope %q saved to %s for %s (commit the file to share it)",
		entry.Name, reposcope.FileName, scope.host)
	return nil
}

// repoScopeDeleted is the agent-mode result of `repo-scope delete`.
type repoScopeDeleted struct {
	Deleted     bool   `json:"deleted"`
	Name        string `json:"name"`
	File        string `json:"file"`
	Environment string `json:"environment"`
}

// deleteRepoScope removes the named entry for the current context's
// environment.
func deleteRepoScope(cmd *cobra.Command, name string) error {
	ctx := cmdContext(cmd)
	scope, err := openRepoScopeForEdit(ctx)
	if err != nil {
		return err
	}
	if !scope.file.Remove(scope.host, name) {
		return &reposcope.NotFoundError{Name: name, Host: scope.host, Known: scope.file.Names(scope.host)}
	}
	if err := reposcope.Save(scope.loc, scope.file); err != nil {
		return err
	}

	if agentMode(ctx) {
		printer := newPrinterCtx(ctx)
		enrichAgent(printer, "delete", "repo-scope")
		return printer.Print(repoScopeDeleted{Deleted: true, Name: name, File: reposcope.FileName, Environment: scope.host})
	}
	output.FprintSuccess(currentStderr(ctx), "Repo scope %q deleted for %s", name, scope.host)
	if scope.file.Empty() {
		output.FprintInfo(currentStderr(ctx), "%s held no other entries and was removed.", reposcope.FileName)
	}
	return nil
}

// listRepoScopes is `repo-scope list`, and `repo-scope` on its own.
func listRepoScopes(cmd *cobra.Command, _ []string) error {
	ctx := cmdContext(cmd)
	scope, err := openRepoScopeForEdit(ctx)
	if err != nil {
		return err
	}
	entries := scope.file.Entries(scope.host)
	printer := newPrinterCtx(ctx)
	ap := enrichAgent(printer, "list", "repo-scope")
	switch {
	case len(entries) == 0 && ap != nil:
		ap.SetSuggestions([]string{scope.emptyReason(), repoScopeDiscoverHint})
	case len(entries) == 0:
		output.FprintInfo(currentStderr(ctx), "No repo scope for %s: %s; %s", scope.host, scope.emptyReason(), repoScopeDiscoverHint)
	case ap != nil:
		ap.SetTotal(len(entries))
	}
	if outputFormat(ctx) == "table" && !plainMode(ctx) && ap == nil {
		return printer.PrintList(repoScopeRows(entries))
	}
	if entries == nil {
		entries = []reposcope.Entry{}
	}
	return printer.PrintList(entries)
}

// emptyReason says why the current environment has no entries.
func (s *repoScopeFile) emptyReason() string {
	hosts := s.file.Hosts()
	switch {
	case !s.loc.Exists:
		return repoScopeNoFileReason
	case len(hosts) == 0:
		return reposcope.FileName + " defines no environment"
	}
	return fmt.Sprintf("%s has no entry for %s (it defines: %s)", reposcope.FileName, s.host, strings.Join(hosts, ", "))
}

// completeRepoScopeNames completes the entry names the scope file defines for
// the current context's environment.
func completeRepoScopeNames(cmd *cobra.Command, args []string, _ string) ([]string, cobra.ShellCompDirective) {
	if len(args) != 0 {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	scope, err := openRepoScopeForEdit(cmdContext(cmd))
	if err != nil {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	return scope.file.Names(scope.host), cobra.ShellCompDirectiveNoFileComp
}

func init() {
	rootCmd.AddCommand(repoScopeCmd)

	repoScopeCmd.AddCommand(repoScopeDiscoverCmd)
	repoScopeCmd.AddCommand(repoScopeSetCmd)
	repoScopeCmd.AddCommand(repoScopeCurrentCmd)
	repoScopeCmd.AddCommand(repoScopeDescribeCmd)
	repoScopeCmd.AddCommand(repoScopeDeleteCmd)
	repoScopeCmd.AddCommand(repoScopeListCmd)
}
