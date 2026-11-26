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
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/Dynatrace/dtctl/pkg/config"
	"github.com/Dynatrace/dtctl/pkg/exec"
	"github.com/Dynatrace/dtctl/pkg/output"
	"github.com/Dynatrace/dtctl/pkg/reposcope"
	sdkquery "github.com/Dynatrace/dtctl/sdk/api/query"
	"github.com/Dynatrace/dtctl/sdk/inventory"
)

// repoScopeDiscoverTimeout bounds the discovery of one unit. A run that needs
// longer is better narrowed with --path or --term than waited for.
const repoScopeDiscoverTimeout = 60 * time.Second

// repoScopeDiscoverTimeoutKey is the context key of a bound that replaces
// repoScopeDiscoverTimeout for one invocation: a test runs out of time
// without waiting a minute, and without a variable every other run shares.
type repoScopeDiscoverTimeoutKey struct{}

// repoScopeDiscoverBound is the time one unit's discovery may take in ctx.
func repoScopeDiscoverBound(ctx context.Context) time.Duration {
	if d, ok := ctx.Value(repoScopeDiscoverTimeoutKey{}).(time.Duration); ok {
		return d
	}
	return repoScopeDiscoverTimeout
}

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
only when the records find nothing. A workload whose name only holds one of
them between "-" or "." (acme-prod-checkout for checkout) is found too, ranked
below one named exactly, when the name has at least five letters and digits.
Each candidate is one workload in one cluster, and its line binds that cluster. When a candidate's spans match and no logs
do, its logs are looked up once by the process group the spans carried. It
names every value on stderr before sending it, and writes nothing: each candidate comes with the 'dtctl repo-scope set' line
that saves it. Test content (test, testdata, e2e, examples and similar
directories, and files such as Dockerfile.test) is not read, and a name that
starts with test, mock or fixture is not sent unless given with --term.

Discovery is per unit: the nearest directory at or above the working directory
(or --path) that holds a build file (go.mod, package.json, pom.xml,
build.gradle(.kts), settings.gradle(.kts), pyproject.toml, *.csproj) or a
Dockerfile; the repository root otherwise. A Gradle build is one unit unless
several of its modules are deployed (hold a Dockerfile, or are held or named
by a Helm chart or workload manifest): each of those is then a unit with the
modules it depends on, and a module several of them depend on, or the
settings directory, is discovered for each of them. Each proposed set line
covers that unit; when the unit is the repository root, it covers the
directory discovery ran for instead if that directory's name is what
matched. It names the --context discovery ran in.

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
			if outputFormat(cmdContext(cmd)) == "csv" {
				return fmt.Errorf("a discovery report is not rows; use -o json, -o yaml or -o toon")
			}
			plans, err := discoveryPlans(cmdContext(cmd), dir, terms)
			if err != nil {
				return err
			}
			if dryRun(cmdContext(cmd)) {
				return printRepoScopeDiscoveryPlans(cmd, plans)
			}
			return runDiscovery(cmd, plans)
		},
	}
	c.Flags().StringArrayVar(&terms, "term", nil, "a name to look for besides those the repository holds, of at least three letters or digits and not a generic word (repeatable)")
	c.Flags().StringVar(&dir, "path", "", "directory to discover for, relative to the repository root (default: the working directory)")
	rejectEmptyFlag(c, "term")
	rejectEmptyFlag(c, "path")
	return c
}

// discoveryPlans reads the repository and plans the queries for the unit
// holding dir (relative to the repository root), or the working directory;
// for a directory several Gradle deployables share, one plan per deployable,
// since an entry for the directory would be none of theirs. It sends nothing,
// so a dry run prints exactly what a real run would send.
func discoveryPlans(ctx context.Context, dir string, terms []string) ([]*reposcope.Plan, error) {
	loc, err := repoScopeLocation(ctx)
	if err != nil {
		return nil, err
	}
	repo := reposcope.RepoFS(loc)
	start, source := loc.RelDir, "the working directory"
	if dir != "" {
		start, source = path.Clean(filepath.ToSlash(dir)), "--path"
	}
	if start, err = repoScopeOnDiskDir(repo, start); err != nil {
		return nil, &reposcope.InvalidError{Source: source, Msg: err.Error(),
			Suggestions: []string{"name the directory relative to the repository root, e.g. services/checkout"}}
	}
	plan, err := reposcope.PlanDiscovery(repo, start, terms)
	if err != nil {
		return nil, err
	}
	if len(plan.SharedBy) == 0 {
		return []*reposcope.Plan{plan}, nil
	}
	// Every deployable's run would find a term's entity and propose it, so
	// one workload would end up bound to each of them.
	if len(terms) > 0 {
		return nil, &reposcope.InvalidError{Source: "--term",
			Msg:         "a term is looked up for one deployable, and this directory is discovered for each of: " + strings.Join(plan.SharedBy, ", "),
			Suggestions: []string{"pass --path <deployable> with --term"}}
	}
	plans := make([]*reposcope.Plan, len(plan.SharedBy))
	for i, d := range plan.SharedBy {
		if plans[i], err = reposcope.PlanDiscovery(repo, d, nil); err != nil {
			return nil, err
		}
	}
	return plans, nil
}

// repoScopeOnDiskDir is dir, a slash-separated directory of repo, in the
// spelling the repository holds it in, found by listing each directory on the
// way. Discovery finds a directory's unit by comparing paths, and a volume
// that ignores case opens Services/Checkout for services/checkout, so the
// spelling a user typed must not be the one compared. A name that differs
// from an entry only in case is that entry when it is the only such one.
//
// A symbolic link on the way is refused: the walk does not follow links, so
// what lies behind one would not be read, and its name would be sent as a
// name of whatever unit holds the link.
func repoScopeOnDiskDir(repo fs.FS, dir string) (string, error) {
	notDir := fmt.Errorf("%q is not a directory of this repository", dir)
	if !fs.ValidPath(dir) {
		return "", notDir
	}
	at := "."
	for _, name := range strings.Split(dir, "/") {
		if name == "." {
			continue // dir is the root
		}
		entries, err := fs.ReadDir(repo, at)
		if err != nil {
			return "", notDir
		}
		var exact, folded []fs.DirEntry
		for _, e := range entries {
			switch {
			case e.Name() == name:
				exact = append(exact, e)
			case strings.EqualFold(e.Name(), name):
				folded = append(folded, e)
			}
		}
		if len(exact) == 0 {
			exact = folded
		}
		switch {
		case len(exact) != 1:
			return "", notDir
		case exact[0].Type()&fs.ModeSymlink != 0:
			return "", fmt.Errorf("%q is a symbolic link, which discovery does not follow; name the directory it points to", path.Join(at, exact[0].Name()))
		case !exact[0].IsDir():
			return "", notDir
		}
		at = path.Join(at, exact[0].Name())
	}
	return at, nil
}

// runDiscovery sends each plan's queries and prints what they found, a
// report per plan. Discovery only reads, like inventory, so it sets up a
// client without a safety check.
func runDiscovery(cmd *cobra.Command, plans []*reposcope.Plan) error {
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
	for _, plan := range plans {
		for _, q := range plan.Queries {
			if slices.Contains(reposcope.DataObjects(), q.DataObject) {
				if err := dqlScopePrecheck(ctx, q.DQL); err != nil {
					return err
				}
			}
		}
	}
	// wide is the table too: it asks for more columns where a table has
	// them, not for another view.
	table := (outputFormat(ctx) == "table" || outputFormat(ctx) == "wide") && !agentMode(ctx)

	runCtx, cancel := inventoryCancelContext(cmd)
	defer cancel()
	runner := &scopeCheckedRunner{Runner: &inventoryRunner{executor: newDQLExecutorFromConfig(ctx, cfg, c),
		scanLimitGB: reposcope.ScanLimitGB, clientContext: "repo-scope-discover"}}
	selectors := repoScopeSelectors(ctx, cfg)
	names := repoScopeNames{}
	// Each plan is a run of its own with its own time bound. After a Ctrl-C
	// the rest still report, every query marked as never run, so in table
	// output no deployable drops out of the answer unsaid. The only error is
	// the shared context's cancellation, so the last plan's stands for all.
	discover := func(plan *reposcope.Plan) (*reposcope.DiscoveryReport, time.Duration, error) {
		// The disclosure goes to stderr in every mode, agent mode included:
		// that mode is detected rather than chosen, and its envelope carries
		// the values only once the queries have run, or not at all after an
		// interruption.
		if len(plan.Queries) > 0 && runCtx.Err() == nil {
			output.FprintInfo(currentStderr(ctx), "Sending to %s: %s (compared to %s); nothing else leaves this machine",
				host, quotedValues(plan.Sent), strings.Join(reposcope.MatchedFields(), ", "))
		}
		runCtx, cancelTimeout := context.WithTimeout(runCtx, repoScopeDiscoverBound(runCtx))
		defer cancelTimeout()
		began := time.Now()
		report, err := reposcope.Discover(runCtx, runner, plan, host)
		for i := range report.Queries {
			// Refused here, so never sent: the report must not count it
			// among the queries that ran.
			if q := &report.Queries[i]; runner.refused[q.DQL] {
				q.Skipped = true
			}
		}
		for i := range report.Candidates {
			c := &report.Candidates[i]
			if c.SetCommand = repoScopeSetCommand(report, *c, selectors); c.SetCommand == "" {
				continue
			}
			if name, holder := names.claim(c.Name, plan.Unit); name != c.Name {
				report.Notes = append(report.Notes, fmt.Sprintf("candidate %d is saved as %q: the line for %s saves an entry named %q, and a second set under that name would replace it",
					c.Rank, name, holder, c.Name))
				c.Name = name
				c.SetCommand = repoScopeSetCommand(report, *c, selectors)
			}
		}
		if errors.Is(err, context.DeadlineExceeded) {
			// The report marks the query that ran out of time and says the
			// answer is partial; the run itself did what it could.
			err = nil
		}
		return report, time.Since(began), err
	}

	reports := make([]*reposcope.DiscoveryReport, len(plans))
	proposals := make([]repoScopeProposal, len(plans))
	for i, plan := range plans {
		var elapsed time.Duration
		reports[i], elapsed, err = discover(plan)
		proposals[i] = newRepoScopeProposal(reports[i], selectors, names)
		if table {
			if i > 0 {
				fmt.Fprintln(currentStdout(ctx))
			}
			printRepoScopeDiscovery(currentStdout(ctx), reports[i], proposals[i], elapsed)
		}
	}
	if table {
		return err
	}
	// An interrupted run has printed its tables by now. An agent envelope is
	// a result or an error, never both, so there the interruption is the
	// whole answer and the partial reports are not printed.
	if err != nil && agentMode(ctx) {
		return err
	}
	// A structured report is read without the lines the table prints under
	// it, and outside agent mode without suggestions either, so what
	// qualifies its set lines travels in its notes.
	for i, r := range reports {
		r.Notes = append(r.Notes, proposals[i].notes()...)
	}
	printer := newPrinterCtx(ctx)
	if ap := enrichAgent(printer, "discover", "repo-scope"); ap != nil {
		var suggestions []string
		for i, r := range reports {
			for _, s := range repoScopeDiscoverySuggestions(r, proposals[i]) {
				if len(reports) > 1 {
					s = r.Unit + ": " + s
				}
				suggestions = append(suggestions, s)
			}
		}
		ap.SetSuggestions(suggestions)
		ap.SetTotal(len(reports))
	}
	// Always a list, one report per unit: a reader need not know beforehand
	// whether the directory is one several deployables share.
	if err := printer.PrintList(reports); err != nil {
		return err
	}
	return err
}

// repoScopeNames are the entry names the lines of one run save under, each
// with the unit whose line took it. A run for a directory several Gradle
// deployables share proposes lines for each of them, and set replaces an
// entry of the same name, so two units' lines under one name would leave
// only the entry of the line run last.
type repoScopeNames map[string]string

// claim is the name a line for unit saves under: name, unless a line for
// another unit has taken it, and then name after the unit. holder is the
// unit that has name. Lines for one unit may share a name: they are
// alternatives for one directory, of which one is saved.
func (n repoScopeNames) claim(name, unit string) (claimed, holder string) {
	free := func(s string) bool {
		u, taken := n[s]
		return !taken || u == unit
	}
	holder, claimed = n[name], name
	for i := 1; !free(claimed); i++ {
		// A name is at most 63 characters, so the number that tells a third
		// taker apart goes in front, where it is never cut off.
		prefix := unit
		if i > 1 {
			prefix = fmt.Sprintf("%d-%s", i, unit)
		}
		claimed = reposcope.EntryName(prefix + "-" + name)
	}
	n[claimed] = unit
	return claimed, holder
}

// scopeCheckedRunner refuses a query the token provably cannot read with the
// denial Grail would send, so Discover records it like any other and carries
// on with what the token can read. refused holds the text of each query it
// turned away: those never left the machine.
type scopeCheckedRunner struct {
	inventory.Runner
	refused map[string]bool
}

func (r *scopeCheckedRunner) RunQuery(ctx context.Context, dql string) (*inventory.RunResult, error) {
	if err := dqlScopePrecheck(ctx, dql); err != nil {
		if r.refused == nil {
			r.refused = map[string]bool{}
		}
		r.refused[dql] = true
		return nil, &sdkquery.QueryError{ErrorType: "NOT_AUTHORIZED_FOR_TABLE", Message: err.Error()}
	}
	return r.Runner.RunQuery(ctx, dql)
}

// printRepoScopeDiscoveryPlans is `discover --dry-run`: every query exactly
// as it would be sent and every value in it. The agent envelope carries the
// plans as its payload, one per unit and each with its own sent and dropped
// values, in a list also when there is one; the details are one flat map.
func printRepoScopeDiscoveryPlans(cmd *cobra.Command, plans []*reposcope.Plan) error {
	report := newDryRunReport(cmd).As("discover", "repo-scope")
	units := make([]string, len(plans))
	for i, plan := range plans {
		if i > 0 {
			report.Linef("")
		}
		writeDiscoveryPlan(report, plan)
		units[i] = plan.Unit
	}
	if plan := plans[0]; len(plans) == 1 {
		report.Detail("unit", "%s", plan.Unit)
		if len(plan.Queries) > 0 {
			report.Detail("sent", "%s", strings.Join(plan.Sent, ", "))
		}
		if len(plan.Dropped) > 0 {
			report.Detail("dropped", "%s", strings.Join(plan.Dropped, ", "))
		}
	} else {
		report.Detail("units", "%s", strings.Join(units, ", "))
	}
	payload, _ := json.Marshal(plans)
	return report.Payload(payload).Print()
}

// writeDiscoveryPlan writes one plan's preview: its values and its queries.
func writeDiscoveryPlan(report *dryRunReport, plan *reposcope.Plan) {
	noteDropped := func() {
		if len(plan.Dropped) > 0 {
			report.Linef("Names that look like test fixtures are not sent: %s; use --term to send one", quotedValues(plan.Dropped))
		}
		if note := plan.TruncationNote(); note != "" {
			report.Linef("%s", capitalize(note))
		}
	}
	if len(plan.Queries) == 0 {
		report.Linef("Dry run: no usable name found for unit %q; %s", plan.Unit, repoScopeNarrowHint)
		noteDropped()
		return
	}
	report.
		Linef("Dry run: discovery for unit %q would send at most %d of these %d queries to the current context's environment; nothing was sent",
			plan.Unit, plan.MostSent(), len(plan.Queries)).
		Linef("Values it would send: %s", quotedValues(plan.Sent))
	noteDropped()
	record := 0 // the number of the record query a rung retries
	for i, q := range plan.Queries {
		label := q.Purpose
		switch {
		case q.SampledRetry():
			label += fmt.Sprintf(" (only if the scan cap stops query %d)", record)
		case q.ProcessGroupCheck():
			label += " (only if a candidate's spans match and no logs do; the ids are the ones the environment returned)"
		case !slices.Contains(reposcope.DataObjects(), q.DataObject):
			label += " (only if the record queries find nothing)"
		default:
			record = i + 1
		}
		report.Linef("").Linef("%d. %s", i+1, label)
		for _, line := range strings.Split(q.DQL, "\n") {
			report.Linef("   %s", line)
		}
	}
}

// repoScopeSetCommand is the `repo-scope set` line that saves a candidate
// for dir, or "" when no binding selects it alone. Discovery never writes the
// file: running this line is the confirmation, so it must save exactly the
// candidate shown, in the environment discovery asked, for the directory it
// asked about.
//
// The workload and the service name lead because they are what the records
// carry by design, the workload first and the service name only for a
// candidate no workload binding describes; the entity ids are deprecated
// fields, listed process group first, and are proposed only for a candidate
// the other two cannot select,
// or whose logs they cannot reach: discovery found those logs by the
// candidate's process groups
// (LogsByProcessGroup), and without that binding the entry would return its
// spans and none of its logs. A service name another candidate also reports
// is left out: it would select that candidate's records as well, and the
// bindings are alternatives. So is one the candidate's records carry that
// discovery did not send: the queries compare service.name to the sent names
// only, so nothing looked for what else reports it.
//
// The workload binding carries the cluster the candidate's records name, so
// the same namespace and workload in another cluster stays out of the entry.
// That holds for a sampled candidate too: the cluster is a key its rows were
// grouped by, not an id list a sample or a cap can cut short, so the line
// still saves exactly the candidate shown. A candidate whose records name no
// cluster, while another candidate runs its namespace and workload in one
// (WorkloadShared), gets no workload binding: it would select both. A
// candidate some of whose records name the cluster and some none
// (WithoutCluster) is bound without it: with the cluster the entry would
// return its spans and none of its logs, or the reverse, and no other
// candidate runs that workload for the wider binding to select.
//
// When any record query was sampled, a candidate gets its workload binding
// only, whether its own records were sampled or read whole: another
// candidate sharing its service name could sit below the sample's floor, so
// ServiceNameShared cannot be trusted. The same holds for a partial report:
// a query that did not answer could have found that other candidate. An id
// binding is held to more still, see repoScopeIDRefusal.
func repoScopeSetCommand(r *reposcope.DiscoveryReport, c reposcope.Candidate, selectors []string) string {
	e, _ := repoScopeBindings(r, c)
	if !e.HasBindings() {
		return ""
	}
	return repoScopeSetLine(exec.ShellQuote(c.Name), repoScopeFlags(e), repoScopeDir(r, c), selectors)
}

// repoScopeBindings is what a line for c binds, as the entry `set` would
// build from it, and the bindings left out because a scope file cannot hold
// them: set would refuse the whole line for one such value. Records carry
// what their sender wrote, a namespace in capitals or an entity id of
// another kind, and a line that is refused saves nothing.
func repoScopeBindings(r *reposcope.DiscoveryReport, c reposcope.Candidate) (e reposcope.Entry, unheld []repoScopeUnheld) {
	// holds reports whether a scope file can hold b, the bindings of one
	// kind, and records them as left out when it cannot.
	holds := func(b reposcope.Entry) bool {
		b.Name = c.Name
		problem := b.Problem()
		if problem != "" {
			unheld = append(unheld, repoScopeUnheld{strings.Join(repoScopeFlags(b), " "), problem})
		}
		return problem == ""
	}
	if c.Namespace != "" && c.Workload != "" && (c.ClusterUID != "" || !c.WorkloadShared) {
		w := reposcope.Workload{Namespace: c.Namespace, Name: c.Workload}
		if len(c.WithoutCluster) == 0 {
			w.ClusterUID = c.ClusterUID
		}
		if holds(reposcope.Entry{Workloads: []reposcope.Workload{w}}) {
			e.Workloads = []reposcope.Workload{w}
		}
	}
	// The service name is for a candidate no workload binding describes.
	// Beside one it would widen the entry: the bindings are alternatives, so
	// the name selects its records in every cluster and namespace, whatever
	// the workload binding says.
	if len(e.Workloads) == 0 && c.ServiceName != "" && slices.Contains(r.Sent, c.ServiceName) &&
		!c.ServiceNameShared && !c.Sampled && !r.SampledRecords() && !r.Partial &&
		holds(reposcope.Entry{ServiceNames: []string{c.ServiceName}}) {
		e.ServiceNames = []string{c.ServiceName}
	}
	switch {
	case !e.HasBindings():
		ids := reposcope.Entry{ProcessGroups: c.ProcessGroups, Services: c.Services}
		if repoScopeIDRefusal(r, c, true) == nil && ids.HasBindings() && holds(ids) {
			e.ProcessGroups, e.Services = c.ProcessGroups, c.Services
		}
	case c.LogsByProcessGroup > 0 && repoScopeIDRefusal(r, c, false) == nil && holds(reposcope.Entry{ProcessGroups: c.ProcessGroups}):
		e.ProcessGroups = c.ProcessGroups
	}
	return e, unheld
}

// repoScopeUnheld is the bindings of one kind that a scope file cannot hold:
// the flags they would have been, and the schema's reason.
type repoScopeUnheld struct{ flags, problem string }

// repoScopeFlags spells an entry's bindings as `repo-scope set` flags,
// shell-quoted, in the order they are listed everywhere: the workload, the
// service name, then the two deprecated ids, process group first.
func repoScopeFlags(e reposcope.Entry) []string {
	var flags []string
	flag := func(name string, values ...string) {
		for _, v := range values {
			flags = append(flags, name, exec.ShellQuote(v))
		}
	}
	for _, w := range e.Workloads {
		if w.ClusterUID != "" {
			flag("--cluster-uid", w.ClusterUID)
		}
		flag("--namespace", w.Namespace)
		flag("--workload", w.Name)
	}
	flag("--service-name", e.ServiceNames...)
	flag("--process-group", e.ProcessGroups...)
	flag("--service", e.Services...)
	return flags
}

// repoScopeRefusal is why a line binds none of a candidate's entity ids:
// the cause, and what follows from it for a candidate left with no line.
type repoScopeRefusal struct{ cause, noLine string }

// repoScopeIDRefusal is why a line for c must not bind its entity ids, nil
// when it may. An id binding saves the ids discovery listed, so they must be
// all of the candidate's (not a sample's, not a list cut short), no other
// candidate's (the result whole, and no candidate shown carrying one), and,
// when alone is true and they are the whole entry, enough to reach the
// records the candidate was found in and more than a name matched inside a
// longer word. alone false asks about the process groups a line adds to a
// workload or service-name binding.
func repoScopeIDRefusal(r *reposcope.DiscoveryReport, c reposcope.Candidate, alone bool) *repoScopeRefusal {
	const workloadOnly = ", so only --namespace with --workload is proposed, and "
	switch {
	case c.Sampled:
		return &repoScopeRefusal{"it was found by sampling, which can miss another candidate sharing its service name or ids",
			workloadOnly + "its records name no workload"}
	case c.ProcessGroupsCapped || alone && c.ServicesCapped:
		return &repoScopeRefusal{"its records carry more entity ids than discovery lists",
			", so no --service or --process-group binding is proposed"}
	case r.Partial:
		return &repoScopeRefusal{"the result is partial, and a candidate it did not see could share its service name or ids",
			workloadOnly + "no workload binding selects it"}
	}
	ids := c.ProcessGroups
	if alone {
		ids = slices.Concat(ids, c.Services)
	}
	if len(ids) == 0 {
		return nil // nothing to bind, and nothing to explain
	}
	carries := func(o reposcope.Candidate) bool {
		return o.Rank != c.Rank && slices.ContainsFunc(ids, func(id string) bool {
			return slices.Contains(o.ProcessGroups, id) || slices.Contains(o.Services, id)
		})
	}
	if i := slices.IndexFunc(r.Candidates, carries); i >= 0 {
		return &repoScopeRefusal{cause: fmt.Sprintf("candidate %d carries one of its entity ids, so a binding by id would select both", r.Candidates[i].Rank)}
	}
	if !alone {
		return nil
	}
	// The entry would be the ids and nothing else. A scope filters each data
	// object by one kind of id, so an object the candidate was found in
	// needs its kind, or the entry binds nothing for it.
	unreached := func(object, field string, found int64, ids []string) *repoScopeRefusal {
		if found == 0 || len(ids) > 0 {
			return nil
		}
		return &repoScopeRefusal{cause: fmt.Sprintf("its %s carry no %s, the id a scope filters %s by, so a binding by id would leave them out", object, field, object)}
	}
	switch {
	case c.Tier == "substring":
		return &repoScopeRefusal{cause: "it matched only inside a longer name, which is too weak a tie to bind it by entity id"}
	case unreached("spans", "dt.entity.service", c.Spans, c.Services) != nil:
		return unreached("spans", "dt.entity.service", c.Spans, c.Services)
	}
	return unreached("logs", "dt.entity.process_group", c.Logs, c.ProcessGroups)
}

// repoScopeSetLine assembles a `repo-scope set` line. name, bindings and
// selectors arrive shell-quoted; dir does not, and is quoted here.
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
// would save the candidate for the wrong environment. The context is always
// named, also when it was the config's current one: that is the one a later
// `dtctl ctx` changes under a line still on the screen. (A session has no
// context a caller could name, and no working directory either, so discovery
// never runs in one.) The config is named only when --config named it: DTCTL_CONFIG is set for the shell, the set
// line included, while --config names it for one command.
func repoScopeSelectors(ctx context.Context, cfg *config.Config) []string {
	var args []string
	if path := cfgFile(ctx); path != "" {
		args = append(args, "--config", exec.ShellQuote(path))
	}
	if cfg.CurrentContext != "" {
		args = append(args, "--context", exec.ShellQuote(cfg.CurrentContext))
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
	// lineNotes explain each candidate that has no line because it was
	// sampled or lists only some of its ids, each line that leaves the
	// candidate's cluster out, and how to bind a workload several lines bind
	// one cluster of.
	lineNotes []string
}

type repoScopeSelectAll struct {
	serviceName string
	all         bool // every candidate reports it
	line        string
}

// notes is what the proposal says about the candidates' lines, for a report
// that is read without the table: the line that selects several candidates,
// which no candidate carries, and what the lines leave out.
func (p repoScopeProposal) notes() []string {
	var notes []string
	for _, a := range p.selectAll {
		notes = append(notes, a.sentence()+"; the line that does: "+a.line)
	}
	return slices.Concat(notes, p.sharedNotes, p.lineNotes)
}

// newRepoScopeProposal works out what r proposes beyond its candidates'
// lines, which must already be set. names are the entry names the run's
// lines have taken so far; a line proposed here takes its own.
func newRepoScopeProposal(r *reposcope.DiscoveryReport, selectors []string, names repoScopeNames) repoScopeProposal {
	p := repoScopeProposal{lineNotes: append(repoScopeNoLineNotes(r), repoScopeClusterNotes(r)...)}
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
		p.selectAll = repoScopeSelectAllLines(r, selectors, names)
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
		repoScopeSetLine("<name>", []string{"--process-group", "PROCESS_GROUP-…", "--service", "SERVICE-…"}, dir, selectors),
	}
	hint := ""
	if r.Unit == "." && r.Dir != "." {
		hint = fmt.Sprintf("each line covers the whole repository; add --path %s to cover only %s", exec.ShellQuote(r.Dir), r.Dir)
	}
	return links, hint
}

// repoScopeSelectAllLines is a line for each service name more than one
// candidate reports, in rank order. The line is an entry for the service
// name, not for one of the candidates: it is named after the service name,
// and it covers the directory discovery ran for only when that directory's
// name matched every candidate it selects (see repoScopeDir).
func repoScopeSelectAllLines(r *reposcope.DiscoveryReport, selectors []string, names repoScopeNames) []repoScopeSelectAll {
	var out []repoScopeSelectAll
	for _, c := range r.Candidates {
		if !c.ServiceNameShared || slices.ContainsFunc(out, func(s repoScopeSelectAll) bool { return s.serviceName == c.ServiceName }) {
			continue
		}
		reporting, matchedDir := 0, true
		for _, o := range r.Candidates {
			if o.ServiceName == c.ServiceName {
				reporting++
				matchedDir = matchedDir && o.MatchedDir
			}
		}
		// The service name of every candidate that reports it, and nothing
		// of this one. A sampled candidate's service name, or one of a
		// partial result, is never proposed alone.
		all := reposcope.Candidate{Rank: c.Rank, Name: reposcope.EntryName(c.ServiceName), ServiceName: c.ServiceName,
			Sampled: c.Sampled, MatchedDir: matchedDir}
		if repoScopeSetCommand(r, all, selectors) == "" {
			continue
		}
		all.Name, _ = names.claim(all.Name, r.Unit)
		out = append(out, repoScopeSelectAll{serviceName: c.ServiceName, all: reporting == len(r.Candidates), line: repoScopeSetCommand(r, all, selectors)})
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
		// A line that binds the workload carries no service name anyway.
		if e, _ := repoScopeBindings(r, c); len(e.Workloads) > 0 {
			continue
		}
		if c.ServiceNameShared && !slices.Contains(names, c.ServiceName) {
			names = append(names, c.ServiceName)
			notes = append(notes, fmt.Sprintf("--service-name %s is left out of the set lines: more than one candidate reports it, so it would select each of them",
				exec.ShellQuote(c.ServiceName)))
		}
	}
	return notes
}

// repoScopeNoLineNotes explains, in rank order, each candidate that has no
// line because of how it was found rather than because nothing describes it,
// and each line that binds less than the candidate's records name.
func repoScopeNoLineNotes(r *reposcope.DiscoveryReport) []string {
	var notes []string
	for _, c := range r.Candidates {
		_, unheld := repoScopeBindings(r, c)
		if c.SetCommand != "" {
			for _, u := range unheld {
				notes = append(notes, fmt.Sprintf("candidate %d's line leaves %s out, which a scope file cannot hold: %s", c.Rank, u.flags, u.problem))
			}
			if c.ClusterUID != "" && len(c.WithoutCluster) > 0 {
				notes = append(notes, fmt.Sprintf("candidate %d's line leaves --cluster-uid out: its %s carry no k8s.cluster.uid, so a binding with it would return none of them; without it the entry selects %s/%s in any cluster",
					c.Rank, strings.Join(c.WithoutCluster, " and "), c.Namespace, c.Workload))
			}
			if why := repoScopeIDRefusal(r, c, false); why != nil && c.LogsByProcessGroup > 0 {
				notes = append(notes, fmt.Sprintf("candidate %d's line leaves --process-group out, and with it the %d logs found by process group: %s",
					c.Rank, c.LogsByProcessGroup, why.cause))
			}
			continue
		}
		why := repoScopeIDRefusal(r, c, true)
		switch {
		case len(unheld) > 0:
			var all []string
			for _, u := range unheld {
				all = append(all, fmt.Sprintf("%s: %s", u.flags, u.problem))
			}
			notes = append(notes, fmt.Sprintf("candidate %d has no set line: a scope file cannot hold %s", c.Rank, strings.Join(all, "; nor ")))
		case c.WorkloadShared && c.ClusterRefused != "":
			notes = append(notes, fmt.Sprintf("candidate %d has no set line: its records carry k8s.cluster.uid %q, which is not a cluster uid a scope file can hold (a lowercase UUID), and another candidate runs the same namespace and workload, so a binding without --cluster-uid would select both",
				c.Rank, c.ClusterRefused))
		case c.WorkloadShared && c.ClusterUID == "":
			notes = append(notes, fmt.Sprintf("candidate %d has no set line: its records name no cluster, and another candidate runs the same namespace and workload in one, so a binding without --cluster-uid would select both; to bind the workload in every cluster, leave --cluster-uid out of that candidate's line",
				c.Rank))
		case why != nil:
			notes = append(notes, fmt.Sprintf("candidate %d has no set line: %s%s", c.Rank, why.cause, why.noLine))
		}
	}
	return notes
}

// repoScopeClusterNotes says, for each namespace and workload the candidates
// run in several clusters, how to bind more than the one cluster each line
// binds. No line does: --cluster-uid takes one cluster for the whole command.
func repoScopeClusterNotes(r *reposcope.DiscoveryReport) []string {
	var workloads []reposcope.Workload
	ranks := map[reposcope.Workload][]string{}
	for _, c := range r.Candidates {
		if !c.WorkloadShared || c.ClusterUID == "" || c.SetCommand == "" {
			continue
		}
		w := reposcope.Workload{Namespace: c.Namespace, Name: c.Workload}
		if ranks[w] == nil {
			workloads = append(workloads, w)
		}
		ranks[w] = append(ranks[w], strconv.Itoa(c.Rank))
	}
	var notes []string
	for _, w := range workloads {
		if n := len(ranks[w]); n > 1 {
			notes = append(notes, fmt.Sprintf("candidates %s and %s are %s in different clusters, and each line binds one; to bind the workload in every cluster, run either line without --cluster-uid; to bind some of several, list the workload once per cluster-uid under workloads in %s",
				strings.Join(ranks[w][:n-1], ", "), ranks[w][n-1], w, reposcope.FileName))
		}
	}
	return notes
}

// repoScopeDiscoverySuggestions are the next steps an agent reading a
// discovery report can take. Saving is the user's call, so a line is offered
// for confirmation rather than as something to run.
func repoScopeDiscoverySuggestions(r *reposcope.DiscoveryReport, p repoScopeProposal) []string {
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
	s = append(s, p.lineNotes...)
	// What a sample can have missed is the agent's to tell the user: it is
	// why one sampled candidate is not a match.
	s = append(s, r.SamplingNotes()...)
	if len(r.Candidates) > 0 {
		s = append(s, "nothing has been saved; running the set command is the confirmation")
	}
	if r.Partial {
		s = append(s, "the result is partial (see notes): a query failed, timed out, was capped or was interrupted, or a sample matched nothing, so a missing candidate is not evidence that nothing runs this code")
	}
	return s
}
