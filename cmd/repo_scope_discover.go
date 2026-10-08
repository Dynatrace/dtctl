package cmd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/dynatrace-oss/dtctl/pkg/exec"
	"github.com/dynatrace-oss/dtctl/pkg/output"
	"github.com/dynatrace-oss/dtctl/pkg/reposcope"
	sdkquery "github.com/dynatrace-oss/dtctl/sdk/api/query"
	"github.com/dynatrace-oss/dtctl/sdk/inventory"
)

// repoScopeDiscoverTimeout bounds one discovery run. A run that needs longer
// is better narrowed with --path or --term than waited for. A variable so a
// test can run out of time without waiting a minute.
var repoScopeDiscoverTimeout = 60 * time.Second

var repoScopeDiscoverCmd = newRepoScopeDiscoverCmd()

func newRepoScopeDiscoverCmd() *cobra.Command {
	var (
		terms []string
		dir   string
	)
	c := &cobra.Command{
		Use:   "discover",
		Short: "Find the entities that run this repository's code",
		Long: `Find the entities that run this repository's code.

Reads names from the repository (Kubernetes manifests, Helm charts, Dockerfiles,
build files and the origin remote) and compares them to k8s.workload.name and
service.name in the last 24 hours of spans and logs; entity names are searched
only when the records find nothing. It names every value before sending it,
and writes nothing: each candidate comes with the 'dtctl repo-scope set' line
that saves it.

Discovery is per unit: the nearest directory at or above the working directory
(or --path) that holds a build file (go.mod, package.json, pom.xml,
pyproject.toml, *.csproj) or a Dockerfile; the repository root otherwise.
Each proposed set line covers that unit; when the unit is the repository root,
it covers the directory discovery ran for instead if that directory's name is
what matched. It names the --context discovery ran in.

Examples:
  # Discover for the unit holding the working directory
  dtctl repo-scope discover

  # Print every query and value it would send, and send nothing
  dtctl repo-scope discover --dry-run

  # Add a name the repository does not spell out
  dtctl repo-scope discover --term billing-engine

  # Another service of a monorepo
  dtctl repo-scope discover --path services/ledger
`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			plan, err := discoveryPlan(cmdContext(cmd), dir, terms)
			if err != nil {
				return err
			}
			if dryRun(cmdContext(cmd)) {
				return printRepoScopeDiscoveryPlan(cmd, plan)
			}
			return runDiscovery(cmd, plan)
		},
	}
	c.Flags().StringArrayVar(&terms, "term", nil, "a name to look for besides those the repository holds (repeatable)")
	c.Flags().StringVar(&dir, "path", "", "directory to discover for, relative to the repository root (default: the working directory)")
	rejectEmptyFlag(c, "term")
	rejectEmptyFlag(c, "path")
	return c
}

// discoveryPlan reads the repository and plans the queries for the unit
// holding dir (relative to the repository root), or the working directory.
// It sends nothing, so a dry run prints exactly what a real run would send.
func discoveryPlan(ctx context.Context, dir string, terms []string) (*reposcope.Plan, error) {
	for _, t := range terms {
		if !reposcope.PlainName(t) {
			return nil, &reposcope.InvalidError{Source: "--term", Msg: fmt.Sprintf("%q may hold only letters, digits, '-', '_' and '.'", t)}
		}
	}
	loc, err := repoScopeLocation(ctx)
	if err != nil {
		return nil, err
	}
	repo := reposcope.RepoFS(loc)
	start := loc.RelDir
	if dir != "" {
		start = path.Clean(filepath.ToSlash(dir))
		// fs.Stat also refuses a path that leaves the repository root.
		if info, err := fs.Stat(repo, start); err != nil || !info.IsDir() {
			return nil, &reposcope.InvalidError{Source: "--path", Msg: fmt.Sprintf("%q is not a directory of this repository", dir),
				Suggestions: []string{"name the directory relative to the repository root, e.g. services/checkout"}}
		}
	}
	return reposcope.PlanDiscovery(repo, start, terms)
}

// runDiscovery sends the plan's queries and prints what they found. Discovery
// only reads, like inventory, so it sets up a client without a safety check.
func runDiscovery(cmd *cobra.Command, plan *reposcope.Plan) error {
	ctx := cmdContext(cmd)
	cfg, c, err := setupClient(ctx)
	if err != nil {
		return err
	}
	host, err := repoScopeHost(cfg)
	if err != nil {
		return err
	}
	// A token that cannot read the records is refused before anything is
	// sent. The entity-table fallbacks are checked only when they are about
	// to run: without them discovery still answers.
	for _, q := range plan.Queries {
		if slices.Contains(reposcope.DataObjects(), q.DataObject) {
			if err := dqlScopePrecheck(ctx, q.DQL); err != nil {
				return err
			}
		}
	}
	if !agentMode(ctx) && len(plan.Queries) > 0 {
		output.FprintInfo(currentStderr(ctx), "Sending to %s: %s (compared to %s); nothing else leaves this machine",
			host, quotedValues(plan.Sent), strings.Join(reposcope.MatchedFields(), ", "))
	}

	runCtx, cancel := inventoryCancelContext(cmd)
	defer cancel()
	runCtx, cancelTimeout := context.WithTimeout(runCtx, repoScopeDiscoverTimeout)
	defer cancelTimeout()
	runner := scopeCheckedRunner{&inventoryRunner{executor: newDQLExecutorFromConfig(ctx, cfg, c), scanLimitGB: reposcope.ScanLimitGB}}

	began := time.Now()
	report, err := reposcope.Discover(runCtx, runner, plan, host)
	selectors := repoScopeSelectors(ctx)
	for i, c := range report.Candidates {
		report.Candidates[i].SetCommand = repoScopeSetCommand(c, repoScopeDir(report, c), selectors)
	}
	if errors.Is(err, context.DeadlineExceeded) {
		// The report marks the query that ran out of time and says the
		// answer is partial; the run itself did what it could.
		err = nil
	}
	if err != nil && agentMode(ctx) {
		return err
	}
	if outputFormat(ctx) == "table" && !agentMode(ctx) {
		printRepoScopeDiscovery(currentStdout(ctx), report, newRepoScopeProposal(report, selectors), time.Since(began))
		return err
	}
	printer := newPrinterCtx(ctx)
	if ap := enrichAgent(printer, "discover", "repo-scope"); ap != nil {
		ap.SetSuggestions(repoScopeDiscoverySuggestions(report, selectors))
	}
	if perr := printer.Print(report); perr != nil {
		return perr
	}
	return err
}

// scopeCheckedRunner refuses a query the token provably cannot read with the
// denial Grail would send, so Discover records it like any other and carries
// on with what the token can read.
type scopeCheckedRunner struct{ inventory.Runner }

func (r scopeCheckedRunner) RunQuery(ctx context.Context, dql string) (*inventory.RunResult, error) {
	if err := dqlScopePrecheck(ctx, dql); err != nil {
		return nil, &sdkquery.QueryError{ErrorType: "NOT_AUTHORIZED_FOR_TABLE", Message: err.Error()}
	}
	return r.Runner.RunQuery(ctx, dql)
}

// printRepoScopeDiscoveryPlan is `discover --dry-run`: every query exactly as
// it would be sent and every value in it. The agent envelope carries the
// queries as its payload.
func printRepoScopeDiscoveryPlan(cmd *cobra.Command, plan *reposcope.Plan) error {
	report := newDryRunReport(cmd).As("discover", "repo-scope").Detail("unit", "%s", plan.Unit)
	if len(plan.Queries) == 0 {
		return report.Linef("Dry run: no usable name found for unit %q; %s", plan.Unit, repoScopeNarrowHint).Print()
	}
	report.
		Linef("Dry run: discovery for unit %q would send %d queries to the current context's environment; nothing was sent", plan.Unit, len(plan.Queries)).
		Linef("Values it would send: %s", quotedValues(plan.Sent)).
		Detail("sent", "%s", strings.Join(plan.Sent, ", "))
	for i, q := range plan.Queries {
		label := q.Purpose
		if !slices.Contains(reposcope.DataObjects(), q.DataObject) {
			label += " (only if the record queries find nothing)"
		}
		report.Linef("").Linef("%d. %s", i+1, label)
		for _, line := range strings.Split(q.DQL, "\n") {
			report.Linef("   %s", line)
		}
	}
	if payload, err := json.Marshal(plan.Queries); err == nil {
		report.Payload(payload)
	}
	return report.Print()
}

// repoScopeSetCommand is the `repo-scope set` line that saves a candidate
// for dir, or "" when no binding selects it alone. Discovery never writes the
// file: running this line is the confirmation, so it must save exactly the
// candidate shown, in the environment discovery asked, for the directory it
// asked about.
//
// The workload and the service name lead because they are what the records
// carry by design; the entity ids are deprecated fields and are proposed only
// for a candidate the other two cannot select. A service name another
// candidate also reports is left out: it would select that candidate's
// records as well, and the bindings are alternatives.
func repoScopeSetCommand(c reposcope.Candidate, dir string, selectors []string) string {
	var bindings []string
	flag := func(name string, values ...string) {
		for _, v := range values {
			bindings = append(bindings, name, exec.ShellQuote(v))
		}
	}
	if c.Namespace != "" && c.Workload != "" {
		flag("--namespace", c.Namespace)
		flag("--workload", c.Workload)
	}
	if c.ServiceName != "" && !c.ServiceNameShared {
		flag("--service-name", c.ServiceName)
	}
	if len(bindings) == 0 {
		flag("--service", c.Services...)
		flag("--process-group", c.ProcessGroups...)
		if len(bindings) == 0 {
			return ""
		}
	}
	return repoScopeSetLine(exec.ShellQuote(c.Name), bindings, dir, selectors)
}

// repoScopeSetLine assembles a `repo-scope set` line from its parts, each
// already quoted for a shell.
func repoScopeSetLine(name string, bindings []string, dir string, selectors []string) string {
	args := append([]string{"dtctl"}, selectors...)
	args = append(args, "repo-scope", "set", name)
	args = append(args, bindings...)
	if dir != "." {
		args = append(args, "--path", exec.ShellQuote(dir))
	}
	return strings.Join(args, " ")
}

// repoScopeDir is the directory an entry for c covers. A unit below the root
// holds its own build file, so it is the service. The root is also the unit
// of every directory that has none, and there the directory discovery ran
// for is the service only when its name is what matched c: in a monorepo
// with one build file at the top, services/ledger is the ledger, while in a
// single-module repository internal/handlers is one package of a service
// its go.mod named, and the entry covers the whole repository.
func repoScopeDir(r *reposcope.DiscoveryReport, c reposcope.Candidate) string {
	switch {
	case r.Unit != ".":
		return r.Unit
	case c.MatchedDir:
		return r.Dir
	}
	return "."
}

// repoScopeSelectors are the global flags that pick the config and context
// discovery ran against, for the set line to carry. Set files an entry under
// the current context's environment, so a line run without the context
// discovery used, in another shell or after the current context changed,
// would save the candidate for the wrong environment. The context is carried
// however it was chosen. The config only when --config named it: DTCTL_CONFIG
// is set for the shell, the set line included, while --config names it for
// one command.
func repoScopeSelectors(ctx context.Context) []string {
	var args []string
	if path := cfgFile(ctx); path != "" {
		args = append(args, "--config", exec.ShellQuote(path))
	}
	if name := contextOverride(ctx); name != "" {
		args = append(args, "--context", exec.ShellQuote(name))
	}
	return args
}

// repoScopeProposal is what discovery proposes beyond each candidate's own
// line, for the terminal and the agent alike.
type repoScopeProposal struct {
	// handLinks are the templates for linking by hand, offered when nothing
	// matched or no binding selects the one candidate that did. They carry
	// the selectors and the unit's --path: a template has no candidate whose
	// name could tie it to the directory discovery ran for.
	handLinks []string
	// dirHint says how to cover only the directory discovery ran for, when
	// that differs from what handLinks cover.
	dirHint string
	// selectAll is one line per shared service name when no candidate of an
	// ambiguous report has a line of its own: nothing tells them apart, and
	// the service name selects every candidate that reports it.
	selectAll []repoScopeSelectAll
	// sharedNotes explain each service name the candidates' lines leave out.
	sharedNotes []string
}

type repoScopeSelectAll struct {
	serviceName string
	all         bool // every candidate reports it
	line        string
}

// newRepoScopeProposal works out what r proposes beyond its candidates'
// lines, which must already be set.
func newRepoScopeProposal(r *reposcope.DiscoveryReport, selectors []string) repoScopeProposal {
	var p repoScopeProposal
	lines := 0
	for _, c := range r.Candidates {
		if c.SetCommand != "" {
			lines++
		}
	}
	switch {
	case len(r.Candidates) == 0, r.Verdict == reposcope.VerdictMatch && lines == 0:
		p.handLinks, p.dirHint = repoScopeHandLinks(r, selectors)
	case lines == 0:
		p.selectAll = repoScopeSelectAllLines(r, selectors)
		if len(p.selectAll) == 0 {
			p.handLinks, p.dirHint = repoScopeHandLinks(r, selectors)
		}
	default:
		p.sharedNotes = repoScopeSharedNameNotes(r)
	}
	return p
}

// repoScopeHandLinks are the templates for linking the unit by hand.
func repoScopeHandLinks(r *reposcope.DiscoveryReport, selectors []string) ([]string, string) {
	dir := repoScopeDir(r, reposcope.Candidate{})
	links := []string{
		repoScopeSetLine("<name>", []string{"--namespace", "<namespace>", "--workload", "<workload>"}, dir, selectors),
		repoScopeSetLine("<name>", []string{"--service-name", "<service.name>"}, dir, selectors),
		repoScopeSetLine("<name>", []string{"--service", "SERVICE-…", "--process-group", "PROCESS_GROUP-…"}, dir, selectors),
	}
	hint := ""
	if r.Unit == "." && r.Dir != "." {
		hint = fmt.Sprintf("each line covers the whole repository; add --path %s to cover only %s", exec.ShellQuote(r.Dir), r.Dir)
	}
	return links, hint
}

// repoScopeSelectAllLines is a line for each service name more than one
// candidate reports, in rank order.
func repoScopeSelectAllLines(r *reposcope.DiscoveryReport, selectors []string) []repoScopeSelectAll {
	var out []repoScopeSelectAll
	for _, c := range r.Candidates {
		if !c.ServiceNameShared || slices.ContainsFunc(out, func(s repoScopeSelectAll) bool { return s.serviceName == c.ServiceName }) {
			continue
		}
		reporting := 0
		for _, o := range r.Candidates {
			if o.ServiceName == c.ServiceName {
				reporting++
			}
		}
		c.ServiceNameShared = false
		c.Namespace, c.Workload, c.Services, c.ProcessGroups = "", "", nil, nil
		out = append(out, repoScopeSelectAll{
			serviceName: c.ServiceName,
			all:         reporting == len(r.Candidates),
			line:        repoScopeSetCommand(c, repoScopeDir(r, c), selectors),
		})
	}
	return out
}

// sentence says what the line selects.
func (s repoScopeSelectAll) sentence() string {
	which := "every candidate that reports it"
	if s.all {
		which = "all of them"
	}
	return fmt.Sprintf("nothing tells these candidates apart; --service-name %s would select %s", exec.ShellQuote(s.serviceName), which)
}

// repoScopeSharedNameNotes explains each service name the set lines leave
// out, one note per name, in rank order.
func repoScopeSharedNameNotes(r *reposcope.DiscoveryReport) []string {
	var names, notes []string
	for _, c := range r.Candidates {
		if c.ServiceNameShared && !slices.Contains(names, c.ServiceName) {
			names = append(names, c.ServiceName)
			notes = append(notes, fmt.Sprintf("--service-name %s is left out of the set lines: more than one candidate reports it, so it would select each of them",
				exec.ShellQuote(c.ServiceName)))
		}
	}
	return notes
}

// repoScopeDiscoverySuggestions are the next steps an agent reading a
// discovery report can take. Saving is the user's call, so a line is offered
// for confirmation rather than as something to run.
func repoScopeDiscoverySuggestions(r *reposcope.DiscoveryReport, selectors []string) []string {
	p := newRepoScopeProposal(r, selectors)
	var s []string
	switch {
	case len(p.handLinks) > 0:
		s = append(s, repoScopeNarrowHint, "link by hand: "+strings.Join(p.handLinks[:2], ", or "))
		if p.dirHint != "" {
			s = append(s, p.dirHint)
		}
	case len(p.selectAll) > 0:
		for _, a := range p.selectAll {
			s = append(s, a.sentence()+"; if the user confirms it should, run: "+a.line)
		}
	case r.Verdict == reposcope.VerdictMatch:
		s = append(s, "confirm with the user, then: "+r.Candidates[0].SetCommand)
	default:
		s = append(s, "show the user the candidates and ask which runs this code, then run its setCommand")
	}
	s = append(s, p.sharedNotes...)
	if len(r.Candidates) > 0 {
		s = append(s, "nothing has been saved; running the set command is the confirmation")
	}
	if r.Partial {
		s = append(s, "the result is partial (see notes): a query failed, timed out, was capped or was interrupted, so a missing candidate is not evidence that nothing runs this code")
	}
	return s
}
