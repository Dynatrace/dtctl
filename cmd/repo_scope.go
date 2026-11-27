package cmd

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"path"
	"path/filepath"
	"slices"
	"strings"

	"github.com/spf13/cobra"
	"golang.org/x/text/unicode/norm"

	"github.com/Dynatrace/dtctl/pkg/config"
	"github.com/Dynatrace/dtctl/pkg/output"
	"github.com/Dynatrace/dtctl/pkg/reposcope"
	"github.com/Dynatrace/dtctl/pkg/stability"
	"github.com/Dynatrace/dtctl/pkg/suggest"
	"github.com/Dynatrace/dtctl/sdk/urls"
)

// repoScopeSince is the release that introduced repo scopes. The command tree
// and the query flags that apply a scope all declare it.
const repoScopeSince = "0.48.0"

// The advice the repo-scope commands give in more than one place.
const (
	repoScopeDiscoverHint = "run 'dtctl repo-scope discover' to find the entities this repository runs as"
	repoScopeNarrowHint   = "add a name you know with --term <name>, or discover for another directory with --path <dir>"
	repoScopeCheckoutHint = "run dtctl from inside a git checkout"
	repoScopeNoFileReason = "this repository has no " + reposcope.FileName
	// repoScopePickHint is how a directory without an entry of its own gets
	// one: reposcope says that none applies, the command says with which
	// flag one is named.
	repoScopePickHint = "pick one with --" + repoScopeFlag + " <name>"
)

var repoScopeCmd = newRepoScopeCmd()

func newRepoScopeCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "repo-scope",
		Short: "Link this git repository to the entities that run its code",
		Long: `Link this git repository to the entities that run its code.

The link is .dtctl-repo-scope.yaml at the repository root: per environment, the
Kubernetes workloads, service names, process groups and services that run the
repository's code, and the filter segments to query under. Inside a linked
repository 'dtctl query', 'wait query' and 'verify query' narrow 'fetch logs'
and 'fetch spans' to them and say so on stderr. Commit the file to share it.
Nothing here writes to Dynatrace.

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
		// A word after the group is a subcommand that does not exist. Cobra
		// would say so too, and the root would then suggest one of its own
		// commands for it. So would the root's table of synonyms ("search"
		// for query), so the closest subcommand is looked up here.
		Args: func(cmd *cobra.Command, args []string) error {
			if len(args) == 0 {
				return nil
			}
			return suggest.ParseCommandError(fmt.Sprintf("unknown command %q for %q", args[0], cmd.CommandPath()), collectSubcommands(cmd))
		},
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
		clusterUIDs   []string
		segments      []string
		namespaces    []string
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

  # Bind the workload in one cluster, where several run that namespace and name
  dtctl repo-scope set checkout --cluster-uid 11111111-2222-3333-4444-555555555555 \
    --namespace payments --workload checkout

  # Bind entity ids, for one directory of a monorepo
  dtctl repo-scope set ledger --path services/ledger \
    --service SERVICE-0123456789ABCDEF --process-group PROCESS_GROUP-FEDCBA9876543210

  # Also send an existing filter segment, by id, with its variable
  dtctl repo-scope set checkout --service-name checkout \
    --segment "Ab3dEf9hIjk?environment=production"

  # The same entry for the staging environment
  dtctl --context staging repo-scope set checkout --service-name checkout
`,
		Args:              cobra.ExactArgs(1),
		ValidArgsFunction: completeRepoScopeNames,
		RunE: func(cmd *cobra.Command, args []string) error {
			entry := reposcope.Entry{Name: args[0], Services: services, ProcessGroups: processGroups, ServiceNames: serviceNames}
			if len(entryPaths) > 1 {
				return &reposcope.InvalidError{
					Source: "repo-scope set",
					Msg:    "--path takes one directory; a Gradle deployable's modules are read from its build",
				}
			}
			if len(entryPaths) == 1 {
				entry.Path = path.Clean(filepath.ToSlash(entryPaths[0]))
				if entry.Path == "." {
					return &reposcope.InvalidError{
						Source:      "--path",
						Msg:         `"." is the repository root, which an entry without a path covers`,
						Suggestions: []string{"leave --path out for an entry that covers the whole repository"},
					}
				}
			}
			// Each qualifies every --workload of the command, so a second
			// value is refused: taking the last one would rebind the
			// workloads named before it. Refused here and not while the
			// flags are parsed, so that the refusal is reported in the mode
			// the whole command line asks for.
			var namespace, clusterUID string
			for _, qualifier := range []struct {
				flag   string
				values []string
				value  *string
			}{{"namespace", namespaces, &namespace}, {"cluster-uid", clusterUIDs, &clusterUID}} {
				if len(qualifier.values) > 1 {
					return &reposcope.InvalidError{
						Source:      "repo-scope set",
						Msg:         fmt.Sprintf("--%s takes one value, for every --workload of the command", qualifier.flag),
						Suggestions: []string{"an entry whose workloads differ in it is written in " + reposcope.FileName + " by hand"},
					}
				}
				if len(qualifier.values) == 1 {
					*qualifier.value = qualifier.values[0]
				}
			}
			// Said here rather than by a flag group, whose error carries
			// neither the usage exit code nor a code an agent can read.
			switch {
			case len(workloads) > 0 && namespace == "":
				return &reposcope.InvalidError{
					Source: "repo-scope set",
					Msg:    "--workload names a workload of a namespace; pass --namespace with it",
				}
			case namespace != "" && len(workloads) == 0:
				return &reposcope.InvalidError{
					Source: "repo-scope set",
					Msg:    "--namespace qualifies the --workload names; pass --workload with it",
				}
			}
			if clusterUID != "" && len(workloads) == 0 {
				return &reposcope.InvalidError{
					Source: "repo-scope set",
					Msg:    "--cluster-uid narrows the --workload names; pass --namespace and --workload with it",
				}
			}
			for _, w := range workloads {
				entry.Workloads = append(entry.Workloads, reposcope.Workload{ClusterUID: clusterUID, Namespace: namespace, Name: w})
			}
			var err error
			if entry.Segments, err = repoScopeSegments(segments); err != nil {
				return &reposcope.InvalidError{Source: "repo-scope set", Msg: err.Error()}
			}
			return setRepoScope(cmd, entry)
		},
	}
	c.Flags().StringArrayVar(&services, "service", nil, "service id, SERVICE-<16 hex digits> (repeatable)")
	c.Flags().StringArrayVar(&processGroups, "process-group", nil, "process group id, PROCESS_GROUP-<16 hex digits> (repeatable)")
	c.Flags().StringArrayVar(&serviceNames, "service-name", nil, "OpenTelemetry service.name (repeatable)")
	c.Flags().StringArrayVar(&clusterUIDs, "cluster-uid", nil, "Kubernetes cluster of the --workload names, as its k8s.cluster.uid (default: any cluster)")
	c.Flags().StringArrayVar(&namespaces, "namespace", nil, "Kubernetes namespace of the --workload names")
	c.Flags().StringArrayVar(&workloads, "workload", nil, "Kubernetes workload name in --namespace (repeatable)")
	c.Flags().StringArrayVar(&entryPaths, "path", nil, "directory the entry covers, relative to the repository root (default: the whole repository)")
	c.Flags().StringArrayVar(&segments, "segment", nil, `existing filter segment a query under the entry also sends, by the id 'dtctl get segments' lists (repeatable, max 10)
bind its variables inline: "ID?var=val&var2=val1,val2"`)
	for _, name := range []string{"service", "process-group", "service-name", "cluster-uid", "namespace", "workload", "segment", "path"} {
		rejectEmptyFlag(c, name)
	}
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
	// The dot that ends a fully qualified name is not part of the key: the
	// file takes a host without it, and it is the same environment.
	host := strings.TrimSuffix(urls.Host(current.Environment), ".")
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
		// The file is there and cannot be inspected: loc says where.
		return loc, err
	}
	if !ok {
		// Without the directory's path: the message travels in an agent
		// envelope and in logs, and the path is this machine's.
		return reposcope.Location{}, &reposcope.InvalidError{
			Source:      "repo scope",
			Msg:         "the working directory is not inside a git repository: a repo scope lives in " + reposcope.FileName + " at the repository root",
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
	// selectors are the global flags that pick the config and the context
	// the file was read for (see repoScopeSelectors).
	selectors []string
}

// openRepoScope reads the scope file. A missing file reads as an empty one,
// so set can start from it. When the file is there but cannot be inspected or
// does not load, the scope comes back together with the error, so a caller
// that reports rather than fails can still say where; every earlier failure
// returns no scope.
func openRepoScope(ctx context.Context, cfg *config.Config) (*repoScopeFile, error) {
	host, err := repoScopeHost(cfg)
	if err != nil {
		return nil, err
	}
	loc, err := repoScopeLocation(ctx)
	s, err := loadRepoScope(host, loc, err)
	if s != nil {
		s.selectors = repoScopeSelectors(ctx, cfg)
	}
	return s, err
}

// loadRepoScope reads the file at loc for host. located is why loc could not
// be established. With a path in it the file is there and could not be
// inspected, which is an unusable file like one that does not load: the
// scope is returned beside the error, so a caller that warns about those and
// carries on does so here too. Without one there is no file to speak of.
//
// An error about the file names it as the repository does (see named).
func loadRepoScope(host string, loc reposcope.Location, located error) (*repoScopeFile, error) {
	if located != nil && loc.Path == "" {
		return nil, located
	}
	s := &repoScopeFile{loc: loc, host: host}
	if located != nil {
		return s, s.named(located)
	}
	var err error
	s.file, err = reposcope.Load(loc)
	return s, s.named(err)
}

// named is an error of reading or writing the scope file, with the file named
// as the repository names it. reposcope names it by its path on this machine,
// home directory included, and the message is printed beside a query's
// output, travels in the agent envelope and ends up in logs. The error stays
// what it was for whoever asks its type.
func (s *repoScopeFile) named(err error) error {
	if err == nil {
		return nil
	}
	if invalid, ok := err.(*reposcope.InvalidError); ok {
		renamed := *invalid
		renamed.Source = strings.ReplaceAll(invalid.Source, s.loc.Path, reposcope.FileName)
		return &renamed
	}
	msg := strings.ReplaceAll(err.Error(), reposcope.Shown(s.loc.Path), reposcope.FileName)
	if msg == err.Error() {
		return err
	}
	return namedError{msg: msg, err: err}
}

// namedError is an error whose message names the scope file without its path.
type namedError struct {
	msg string
	err error
}

func (e namedError) Error() string { return e.msg }
func (e namedError) Unwrap() error { return e.err }

// resolve picks the entry named, or the one covering the working directory.
func (s *repoScopeFile) resolve(name string) (*reposcope.Status, error) {
	if !s.loc.Exists && name == "" {
		return &reposcope.Status{Code: reposcope.StatusNoFile, Environment: s.host, Dir: s.loc.RelDir, Reason: repoScopeNoFileReason}, nil
	}
	if !s.loc.Exists && s.file.Empty() {
		return nil, &reposcope.NotFoundError{Name: name, Host: s.host, NoFile: true}
	}
	return reposcope.Resolve(reposcope.RepoFS(s.loc), s.file, s.host, s.loc.RelDir, name)
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

// repoScopeStatus is what current and describe report, with the advice a
// file that does not load gives about itself. Without a name, every answer
// short of a broken config is a Status, outside a repository and with a file
// that does not validate included, because these commands are how a user
// finds out why a query was not scoped. With a name, what does not resolve
// is the error.
func repoScopeStatus(ctx context.Context, name string) (status *reposcope.Status, advice []string, err error) {
	cfg, err := loadConfig(ctx)
	if err != nil {
		return nil, nil, err
	}
	scope, err := openRepoScope(ctx, cfg)
	var invalid *reposcope.InvalidError
	switch {
	case err == nil:
		status, err = scope.resolve(name)
		return status, nil, err
	case name != "":
		return nil, nil, err
	case scope != nil:
		// The file is there and did not load: it does not validate, or it
		// cannot be read.
		status = &reposcope.Status{Code: reposcope.StatusInvalidFile, File: reposcope.FileName, Environment: scope.host, Dir: scope.loc.RelDir,
			Reason: scope.unusable(err)}
		if errors.As(err, &invalid) {
			advice = invalid.Suggestions
		}
		return status, advice, nil
	case errors.As(err, &invalid):
		return &reposcope.Status{Code: reposcope.StatusNotARepository, Reason: "not inside a git repository"}, nil, nil
	}
	return nil, nil, err
}

// unusable is why the file did not load, for a command that reports it and
// carries on. It names the file as the repository does (see named).
func (s *repoScopeFile) unusable(err error) string {
	return s.named(err).Error()
}

// repoScopeReason is a Status's reason as a command prints it: where no
// entry applies and one could be named, it ends with the flag that names
// one.
func repoScopeReason(s *reposcope.Status) string {
	switch s.Code {
	case reposcope.StatusNoEntry, reposcope.StatusAmbiguousEntry:
		return s.Reason + "; " + repoScopePickHint
	}
	return s.Reason
}

// showRepoScope is current and describe: the same Status, one line or in
// detail for a terminal, as is in every other format.
func showRepoScope(cmd *cobra.Command, verb, name string) error {
	ctx := cmdContext(cmd)
	// A Status is one value. A format that prints rows would print its
	// address, or a row of its fields.
	format := outputFormat(ctx)
	terminal := format == "" || format == "table" || format == "wide" || format == output.FormatAuto
	if !terminal && !slices.Contains([]string{"json", "yaml", "yml", "toon"}, format) {
		return &reposcope.InvalidError{Source: "-o " + format,
			Msg: fmt.Sprintf("repo-scope %s prints one entry, not rows; use -o table, -o json, -o yaml or -o toon", verb)}
	}
	status, advice, err := repoScopeStatus(ctx, name)
	if err != nil {
		return err
	}
	if terminal && !agentMode(ctx) {
		warnRepoScopeFile(currentStderr(ctx), status.Warning)
		if verb == "current" {
			printRepoScopeCurrent(currentStdout(ctx), status)
		} else {
			printRepoScopeDescribe(currentStdout(ctx), status)
		}
		return nil
	}
	printer := newPrinterCtx(ctx)
	if ap := enrichAgent(printer, verb, "repo-scope"); ap != nil {
		ap.SetSuggestions(repoScopeStatusSuggestions(status, advice))
	}
	return printer.Print(status)
}

// repoScopeStatusSuggestions are the next steps an agent reading a Status can
// take. advice is what a file that did not load says about itself.
func repoScopeStatusSuggestions(s *reposcope.Status, advice []string) []string {
	switch s.Code {
	case reposcope.StatusLinked:
		// Only the entry in effect here narrows the queries run here. One
		// asked for by name may cover another directory.
		if s.Selected == reposcope.SelectedByName {
			return []string{fmt.Sprintf("pass --%s %s to 'dtctl query' to narrow fetch logs and fetch spans to this entry from any directory", repoScopeFlag, s.Entry.Name)}
		}
		return []string{fmt.Sprintf("'dtctl query' narrows fetch logs and fetch spans to %q here; add --%s to run a query unscoped", s.Entry.Name, noRepoScopeFlag)}
	case reposcope.StatusNotARepository:
		return []string{repoScopeCheckoutHint}
	case reposcope.StatusInvalidFile:
		return append([]string{"correct " + reposcope.FileName + ": nothing in it is applied until it loads"}, advice...)
	case reposcope.StatusAmbiguousEntry:
		return []string{repoScopeReason(s)}
	case reposcope.StatusNoEntry:
		return []string{"no entry covers this directory; pass --" + repoScopeFlag + " <name> to use one of: " + strings.Join(s.Others, ", ")}
	}
	return []string{repoScopeDiscoverHint}
}

// repoScopeSegments reads set's --segment values, in the spelling query's
// --segment takes, into the references an entry holds. The id is kept as
// typed: set sends nothing, so a name is not looked up, and Save refuses one
// that is not an id.
func repoScopeSegments(flags []string) ([]reposcope.Segment, error) {
	refs, err := parseSegmentFlags(flags)
	if err != nil {
		return nil, err
	}
	var out []reposcope.Segment
	for i, ref := range refs {
		// query's --segment has always read "id?&" as the bare segment, and
		// goes on doing so. Here the result is written to a committed file,
		// so a ? that introduces nothing is asked about instead.
		if strings.Contains(flags[i], "?") && len(ref.Variables) == 0 {
			return nil, fmt.Errorf("invalid --segment %q: expected VARIABLE=VALUE after '?'", strings.TrimSpace(flags[i]))
		}
		seg := reposcope.Segment{ID: ref.ID}
		for _, v := range ref.Variables {
			if _, twice := seg.Variables[v.Name]; twice {
				return nil, fmt.Errorf("invalid --segment %q: variable %q is given twice; list its values as %s=a,b", strings.TrimSpace(flags[i]), v.Name, v.Name)
			}
			if seg.Variables == nil {
				seg.Variables = map[string][]string{}
			}
			seg.Variables[v.Name] = v.Values
		}
		out = append(out, seg)
	}
	return out, nil
}

// setRepoScope writes entry for the current context's environment.
func setRepoScope(cmd *cobra.Command, entry reposcope.Entry) error {
	ctx := cmdContext(cmd)
	// Checked here rather than left to Save, so the error names the flags
	// instead of the file.
	if !entry.HasBindings() && len(entry.Segments) == 0 {
		return &reposcope.InvalidError{
			Source:      "repo-scope set",
			Msg:         "an entry needs at least one of --namespace with --workload, --service-name, --process-group, --service or --segment",
			Suggestions: []string{repoScopeDiscoverHint},
		}
	}
	scope, err := openRepoScopeForEdit(ctx)
	if err != nil {
		return err
	}
	if entry.Path != "" {
		// A path that names no directory would be committed and never cover
		// a query. The file takes the spelling the repository holds the
		// directory in, composed, whatever the shell handed over.
		dir, err := repoScopeOnDiskDir(reposcope.RepoFS(scope.loc), entry.Path)
		if err != nil {
			return &reposcope.InvalidError{Source: "--path", Msg: err.Error(),
				Suggestions: []string{"name the directory relative to the repository root, e.g. services/checkout"}}
		}
		// Nor would a path inside a checkout of its own: the walk up from a
		// directory there stops at the inner .git, and never reads this file.
		if inner := nestedCheckout(reposcope.RepoFS(scope.loc), dir); inner != "" {
			return &reposcope.InvalidError{Source: "--path",
				Msg:         fmt.Sprintf("%q is in the checkout at %q, which has a .git of its own, so no query run there reads this repository's %s", dir, inner, reposcope.FileName),
				Suggestions: []string{"run 'dtctl repo-scope set' inside " + inner + " to link that checkout"}}
		}
		entry.Path = norm.NFC.String(dir)
	}
	// set replaces the bindings of an entry. Its annotations are not among
	// them and are not dtctl's to drop.
	for _, old := range scope.file.Entries(scope.host) {
		if old.Name == entry.Name {
			entry.Annotations = old.Annotations
		}
	}
	scope.file.Upsert(scope.host, entry)
	if err := reposcope.Save(scope.loc, scope.file); err != nil {
		return scope.named(err)
	}

	if agentMode(ctx) {
		status, err := scope.resolve(entry.Name)
		if err != nil {
			return err
		}
		printer := newPrinterCtx(ctx)
		if ap := enrichAgent(printer, "set", "repo-scope"); ap != nil {
			// About the entry just saved, in the environment it was saved
			// for: `current` and a bare query answer for the working
			// directory and the current context, which may be others.
			selectors := strings.Join(append([]string{""}, scope.selectors...), " ")
			ap.SetSuggestions([]string{
				"dtctl repo-scope describe " + entry.Name + selectors,
				fmt.Sprintf("dtctl query 'fetch logs | limit 20' --%s %s%s", repoScopeFlag, entry.Name, selectors),
			})
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
		return &reposcope.NotFoundError{Name: name, Host: scope.host, Known: scope.file.Names(scope.host), NoFile: !scope.loc.Exists}
	}
	if err := reposcope.Save(scope.loc, scope.file); err != nil {
		return scope.named(err)
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
	// Entries are rows or values. A format that draws a chart of numbers, or
	// one the printer does not know and reads as a table, prints neither.
	if format := outputFormat(ctx); !slices.Contains([]string{"", "table", "wide", "csv", "json", "jsonl", "yaml", "yml", "toon", output.FormatAuto}, format) {
		return &reposcope.InvalidError{Source: "-o " + format,
			Msg: "repo-scope list prints entries; use -o table, -o wide, -o csv, -o json, -o jsonl, -o yaml or -o toon"}
	}
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
	if warning := scope.file.Warning(); ap != nil && warning != "" {
		ap.SetWarnings([]string{reposcope.FileName + ": " + warning})
	}
	if ap == nil {
		warnRepoScopeFile(currentStderr(ctx), scope.file.Warning())
		// Every format that prints rows prints the rows of the table; the
		// others print the entries themselves.
		if format := outputFormat(ctx); format != "toon" && output.IsTabularFormat(format, plainMode(ctx)) {
			return printer.PrintList(repoScopeRows(entries))
		}
	}
	if entries == nil {
		entries = []reposcope.Entry{}
	}
	return printer.PrintList(entries)
}

// nestedCheckout is the directory at or above dir, below the repository
// root, that holds a .git of its own (a submodule, a vendored clone), or "".
func nestedCheckout(repo fs.FS, dir string) string {
	inner := ""
	for d := dir; d != "."; d = path.Dir(d) {
		if _, err := fs.Stat(repo, path.Join(d, ".git")); err == nil {
			inner = d
		}
	}
	return inner
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
