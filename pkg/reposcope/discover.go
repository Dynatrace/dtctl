package reposcope

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"path"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/Dynatrace/dtctl/pkg/dql"
	sdkquery "github.com/Dynatrace/dtctl/sdk/api/query"
	"github.com/Dynatrace/dtctl/sdk/inventory"
)

// ScanLimitGB is a discovery query's scan cap. The record queries carry it in
// their own text, so a dry run shows it; the entity-table fallbacks need it
// as the request default.
const ScanLimitGB = 5

const (
	discoveryWindow   = "24h"
	discoveryRowLimit = 200

	// scanLimitBytes is ScanLimitGB as Grail counts scanned bytes, which the
	// sampling ladder predicts a rung's scan against.
	scanLimitBytes = ScanLimitGB * 1_000_000_000
)

// maxSentValues is the most values one discovery sends. Each is a clause of
// every query and a word of the disclosure the user is asked to read, and a
// unit one service is built from names a few dozen at most. A unit that names
// more is refused rather than cut down: which names to leave out is not
// discovery's to choose.
const maxSentValues = 200

// Why a query did not give a whole answer.
const (
	causeTimeout       = "timeout"
	causeAuth          = "auth"
	causeUnknownObject = "unknown_object"
	causeError         = "error"
	causeTruncated     = "truncated"
	causeRowLimit      = "row_limit"
	causeCancelled     = "cancelled"
	// causePredictedCap is a sampled retry skipped because the scan of the
	// one before it, scaled by how much more this one reads, exceeds the cap.
	causePredictedCap = "predicted_cap"
	// causeSamplingRefused is a sampled retry Grail answered without
	// sampling, so it read what the capped query read.
	causeSamplingRefused = "sampling_refused"
)

// samplingRung is one sampled retry of a record query.
type samplingRung struct {
	window string // from:now()-<window>
	ratio  int64  // samplingRatio; only decades are real, Grail rounds down
	step   int64  // how many times more this rung reads than the previous one
}

// recordSamplingRungs are the sampled retries of a record query the scan cap
// stopped, coarsest first, so the common case pays one near-free query. The
// walk stops at the first rung that completes: discovery needs the identities
// a sample reveals, not a precise count, which is why its rule differs from
// sdk/inventory's (that one keeps descending until the sample is thick
// enough to date). A rung is skipped when the previous rung's scanned bytes,
// scaled by the step, predict another cap.
//
// The ratios come from inventory's measurement on a large tenant (see
// sdk/inventory/sampling.go): 15 minutes of logs scanned 500 GB unsampled,
// 14.4 GB at 1-in-1000 and 0.18 GB at 1-in-100000.
var recordSamplingRungs = []samplingRung{
	{"1h", 100000, 0},   // the largest tenants: an hour is what fits even at 1-in-100000
	{"24h", 100000, 24}, // large tenants: the whole window at the coarsest ratio
	{"24h", 1000, 100},  // medium tenants: quiet services down to about 3k records a day
}

// Query is one DQL text discovery runs. The same value describes a plan and,
// filled in, a result.
type Query struct {
	Purpose    string `json:"purpose" yaml:"purpose"`
	DataObject string `json:"dataObject" yaml:"dataObject"`
	DQL        string `json:"dql" yaml:"dql"`
	Rows       int    `json:"rows,omitempty" yaml:"rows,omitempty"`
	Truncated  bool   `json:"truncated,omitempty" yaml:"truncated,omitempty"`
	// Skipped is a query that was not run: a fallback the record queries
	// made unnecessary, or one after a cancellation.
	Skipped bool   `json:"skipped,omitempty" yaml:"skipped,omitempty"`
	Error   string `json:"error,omitempty" yaml:"error,omitempty"` // first line
	Cause   string `json:"cause,omitempty" yaml:"cause,omitempty"`
	// Window and SamplingRatio are set only on a sampled retry of the
	// record query before it: the lookback it reads and the ratio it asks
	// for. Grail may sample more finely than asked, never more coarsely, so
	// the floor a sampled answer states is computed from this ratio.
	Window        string `json:"window,omitempty" yaml:"window,omitempty"`
	SamplingRatio int64  `json:"samplingRatio,omitempty" yaml:"samplingRatio,omitempty"`
	// truncation is the limit Grail reported for a truncated result, which
	// the note names; Cause stays "truncated" whichever it was.
	truncation inventory.TruncationCause
	// step is, on a sampled retry, how many times more it reads than the
	// retry before it, which scales that one's scan into a prediction.
	step int64
}

// purposeProcessGroupLogs names the one query whose text depends on what the
// record queries returned.
const purposeProcessGroupLogs = "logs by process group"

// processGroupPlaceholder stands in a plan for ids only a run knows.
const processGroupPlaceholder = "<process groups the candidate's spans carried>"

// ProcessGroupCheck reports the query that looks for a candidate's logs by
// process group, run only when its spans matched and no logs did.
func (q *Query) ProcessGroupCheck() bool {
	return q.Purpose == purposeProcessGroupLogs
}

// fallback reports an entity-table query, run only when the record queries
// found nothing.
func (q *Query) fallback() bool {
	return q.DataObject == objectServices || q.DataObject == objectProcessGroups
}

// SampledRetry reports a sampled retry, run only when the scan cap stopped
// the record query before it.
func (q *Query) SampledRetry() bool {
	return q.SamplingRatio > 0
}

// Plan is what discovery will send for one unit: each query with its exact
// DQL, and every value in them. It is built without a network, so a dry run
// shows precisely what a real run sends.
type Plan struct {
	// Dir is the directory discovery was asked about, relative to the
	// repository root; Unit is the build unit holding it. They differ in a
	// monorepo with one build file at the top, where Dir's own name is
	// among what was sent but Unit is the whole repository.
	Dir  string `json:"dir" yaml:"dir"`
	Unit string `json:"unit" yaml:"unit"`
	// SharedBy are the deployables that share Dir's module; the command
	// then runs discovery for each of them.
	SharedBy []string `json:"-" yaml:"-"`
	Sent     []string `json:"sent" yaml:"sent"`
	// Dropped are the unit's names held back because they look like test
	// fixtures; a --term sends one anyway.
	Dropped []string `json:"dropped,omitempty" yaml:"dropped,omitempty"`
	// Truncated reports that the repository holds more source files than
	// discovery reads. The unit is still the right one, but names in the
	// files left unread are missing from Sent, so a report from this plan
	// is partial. See TruncationNote.
	Truncated bool    `json:"truncated,omitempty" yaml:"truncated,omitempty"`
	Queries   []Query `json:"queries" yaml:"queries"`
	tokens    []token
}

// TruncationNote says what a Truncated plan leaves out, for whoever shows the
// plan or its report; "" for a plan that read the repository whole.
func (p *Plan) TruncationNote() string {
	if !p.Truncated {
		return ""
	}
	return fmt.Sprintf("the repository holds more than %d build files, Dockerfiles and manifests, and discovery reads that many, so names in the rest were not sent; add one with --term", maxFiles)
}

// PlanDiscovery reads the names repo gives the unit holding dir and plans the
// queries that look them up. Sent is every variant of those names and of each
// term, and each one's own spelling, sorted; namespaces are never sent, only
// matched against what the records say. With nothing to send there are no
// queries.
//
// The record queries aggregate 24 hours of spans and logs whose workload or
// service.name is one of the values, one row per cluster, namespace, workload
// and service name, counting and collecting the entity ids of each. Each is
// followed by its sampled retries, run only when the scan cap stops it. The
// entity-table queries search entity names and run only when the record
// queries find nothing. The last query looks for logs by the process groups a
// candidate's spans carried, and runs only when the records leave a candidate
// with spans and no logs; its ids come from the environment's own answer, so
// the plan shows it with a placeholder.
//
// dir is relative to the repository root, as an fs.FS names it; one that is
// not is an InvalidError. A term is held to what a name read from the
// repository is, and one that is not is an InvalidError too: it would
// otherwise be the one way a generic word, a two-letter name or a character
// with a meaning in DQL reaches a query.
func PlanDiscovery(repo fs.FS, dir string, terms []string) (*Plan, error) {
	terms, err := checkTerms(terms)
	if err != nil {
		return nil, err
	}
	if dir = path.Clean(dir); !fs.ValidPath(dir) {
		return nil, &InvalidError{Source: dir, Msg: "is not a directory inside the repository",
			Suggestions: []string{"name the directory relative to the repository root, e.g. services/checkout"}}
	}
	s, err := extract(repo, dir)
	if err != nil {
		return nil, err
	}
	p := &Plan{Dir: dir, Unit: s.unit, SharedBy: s.sharedBy, Truncated: s.capped,
		Sent: sendValues(s.tokens, terms), Queries: []Query{}, tokens: s.tokens}
	p.Dropped = heldBack(s.dropped, p.Sent)
	switch n := len(p.Sent); {
	case n == 0:
		return p, nil
	case n > maxSentValues:
		return nil, &InvalidError{Source: fmt.Sprintf("unit %q", p.Unit),
			Msg: fmt.Sprintf("its files name %d values to look for, more than the %d one discovery sends", n, maxSentValues),
			Suggestions: []string{
				"run discover for one service: --path <a directory with its own build file or Dockerfile>",
				"or link by hand: dtctl repo-scope set <name> --namespace <namespace> --workload <workload>",
			}}
	}
	// The stages after the fetch line are the same in all eight record
	// queries, so they are built once.
	stages := recordStages(p.Sent)
	for _, o := range []struct{ object, purpose string }{
		{objectSpans, "spans by workload or service name"},
		{objectLogs, "logs by workload or service name"},
	} {
		p.Queries = append(p.Queries, recordQuery(o.object, o.purpose, stages))
		for _, rung := range recordSamplingRungs {
			p.Queries = append(p.Queries, sampledRecordQuery(o.object, o.purpose, stages, rung))
		}
	}
	p.Queries = append(p.Queries,
		entityQuery(objectServices, "services by entity name", p.Sent),
		entityQuery(objectProcessGroups, "process groups by entity name", p.Sent),
		processGroupLogsQuery(nil),
	)
	return p, nil
}

// heldBack is the dropped names a term did not send after all, so the
// "not sent" note never names a value the queries carry.
func heldBack(dropped, sent []string) []string {
	var out []string
	for _, d := range dropped {
		if !slices.Contains(sent, d) {
			out = append(out, d)
		}
	}
	return out
}

// checkTerms trims the terms, drops the empty ones and refuses one that is
// not a name discovery would send had it read it from the repository: made of
// other characters than a name's, or with no variants, which is a generic
// word or fewer than minTokenRunes letters and digits.
func checkTerms(terms []string) ([]string, error) {
	var out []string
	for _, term := range terms {
		term = strings.TrimSpace(term)
		switch {
		case term == "":
			continue
		case !PlainName(term):
			return nil, &InvalidError{Source: "--term", Msg: fmt.Sprintf("%q may hold only letters, digits, '-', '_' and '.'", term)}
		case utf8.RuneCountInString(term) > maxTokenRunes:
			return nil, &InvalidError{Source: "--term",
				Msg: fmt.Sprintf("%q is longer than the %d characters a workload or service name has", term, maxTokenRunes)}
		case len(variants(term)) == 0:
			return nil, &InvalidError{Source: "--term",
				Msg: fmt.Sprintf("%q is not looked for: a name needs at least %d letters or digits and must not be a generic word such as api, server or web, which every environment holds many of",
					term, minTokenRunes),
				Suggestions: []string{"a service that is really named so is linked by hand: dtctl repo-scope set <name> --service-name " + term}}
		}
		out = append(out, term)
	}
	return out, nil
}

// sendValues is every value the queries carry: each token's variants, and
// each term as given, lowercased and in its variants. terms are checked.
func sendValues(tokens []token, terms []string) []string {
	set := map[string]bool{}
	for _, t := range tokens {
		if t.kind == kindNamespace {
			continue
		}
		for _, v := range t.variants {
			set[v] = true
		}
	}
	for _, term := range terms {
		set[term] = true
		set[strings.ToLower(term)] = true
		for _, v := range variants(term) {
			set[v] = true
		}
	}
	return sortedKeys(set)
}

func recordQuery(object, purpose, stages string) Query {
	return Query{Purpose: purpose, DataObject: object,
		DQL: fmt.Sprintf("fetch %s, from:now()-%s, scanLimitGBytes:%d\n", object, discoveryWindow, ScanLimitGB) + stages}
}

// sampledRecordQuery is a record query's retry at one rung. Its records
// column counts what the sample matched, the hits discovery reports, never
// the population Grail would extrapolate them to.
func sampledRecordQuery(object, purpose, stages string, rung samplingRung) Query {
	return Query{
		Purpose:       fmt.Sprintf("%s (sampled 1-in-%d over %s)", purpose, rung.ratio, rung.window),
		DataObject:    object,
		Window:        rung.window,
		SamplingRatio: rung.ratio,
		step:          rung.step,
		DQL: fmt.Sprintf("fetch %s, from:now()-%s, scanLimitGBytes:%d, samplingRatio:%d\n", object, rung.window, ScanLimitGB, rung.ratio) +
			stages,
	}
}

// recordStages is a record query after its fetch line. It groups by identity rather than by entity id: one workload can run
// as hundreds of service ids, and one row per id would fill the page with a
// single candidate. Each id field is collected up to one id more than
// discoveryIDLimit, which is what a binding can use: the extra one is how a
// cut list is told from a whole one. The distinct count beside it is an
// estimate in DQL, so it only says roughly how many ids a cut list stands
// for.
//
// The cluster uid is part of the identity: the same namespace and workload
// name in two clusters are two workloads, and one row for both would propose
// a binding that selects whichever the user did not mean. Its name is taken
// from any record of the group rather than grouped by, so a renamed cluster
// stays one row.
//
// A workload deployed under a longer name than the repository spells
// (acme-prod-checkout for checkout) is found in the same query, so it costs no
// further scan: the first filter also keeps records whose workload name holds
// a sent name, and a second one, after the aggregation, keeps of those only
// the rows where the name is a whole run of the workload name's segments. The
// service name is compared whole: it is free text, with no segments to trust.
//
// Those rows sort behind the ones named exactly, whatever their volume. The
// row limit cuts the end of the page, and a busy environment can hold more
// workloads that merely contain a name than the page has rows: sorted by
// records alone, they would push the workload that is named exactly off it.
func recordStages(values []string) string {
	list := quotedList(values)
	exact := fmt.Sprintf("in(%s, array(%s)) or in(%s, array(%s))", fieldWorkload, list, fieldServiceName, list)
	inside := ""
	page := []string{
		fmt.Sprintf("| sort %s desc", fieldRecords),
		fmt.Sprintf("| limit %d", discoveryRowLimit),
	}
	if patterns := segmentPatterns(values); len(patterns) > 0 {
		for _, n := range segmentNeedles(patterns) {
			inside += fmt.Sprintf(" or contains(%s, %s)", fieldWorkload, dql.Quote(n))
		}
		whole := fieldNameTier + " == 0"
		for _, p := range patterns {
			whole += fmt.Sprintf(" or contains(%s, %s)", fieldWorkloadSegments, dql.Quote("-"+p+"-"))
		}
		page = []string{
			// A workload name is lowercase letters, digits, "-" and ".":
			// with "." folded into "-" and a "-" at each end, a name is a
			// run of its segments exactly when "-name-" is in it.
			fmt.Sprintf(`| fieldsAdd %s = concat("-", replaceString(%s, ".", "-"), "-"),`, fieldWorkloadSegments, fieldWorkload),
			fmt.Sprintf("    %s = if(%s, 0, else:1)", fieldNameTier, exact),
			"| filter " + whole,
			fmt.Sprintf("| sort %s asc, %s desc", fieldNameTier, fieldRecords),
			fmt.Sprintf("| limit %d", discoveryRowLimit),
			fmt.Sprintf("| fieldsRemove %s, %s", fieldWorkloadSegments, fieldNameTier),
		}
	}
	ids := func(count, collected, field, end string) string {
		return fmt.Sprintf("    %s = countDistinct(%s), %s = collectDistinct(%s, maxLength:%d)%s", count, field, collected, field, discoveryIDLimit+1, end)
	}
	lines := []string{
		"| filter " + exact + inside,
		// Several aggregations are grouped in braces; ungrouped, Grail
		// answers with a PARAMETERS_SHOULD_BE_GROUPED notification.
		fmt.Sprintf("| summarize {%s = count(), %s = takeAny(%s),", fieldRecords, fieldAnyClusterName, fieldClusterName),
		ids(fieldServiceCount, fieldServices, fieldService, ","),
		ids(fieldProcessGroupCount, fieldProcessGroups, fieldProcessGroup, "},"),
		fmt.Sprintf("    by:{%s, %s, %s, %s}", fieldClusterUID, fieldNamespace, fieldWorkload, fieldServiceName),
	}
	return strings.Join(append(lines, page...), "\n")
}

// minSegmentRunes is the fewest letters and digits a name needs to be looked
// for inside a workload name. A shorter one (api, web, cart) is a segment of
// workloads all over a cluster, so it matches a workload only whole.
const minSegmentRunes = 5

// segmentable reports whether v is looked for as a run of a workload name's
// segments: it is spelled the way such a run is, lowercase letters and digits
// in words joined by single "-", and has at least minSegmentRunes of them.
// The other spellings of a name are not looked for there, and need not be:
// every name is sent in this spelling too (the first of its variants), so the
// queries carry no text that is not a sent value or, with "." for "-", one.
func segmentable(v string) bool {
	n, prev := 0, '-'
	for _, r := range v {
		switch {
		case r >= 'a' && r <= 'z' || r >= '0' && r <= '9':
			n++
		case r != '-' || prev == '-':
			return false
		}
		prev = r
	}
	return prev != '-' && n >= minSegmentRunes
}

// segmentPatterns are the names the record queries look for as a run of a
// workload name's segments: each sent value spelled that way and long enough.
// One that holds another as a run of its own segments is left out, since
// every workload name it is in holds the shorter one too.
func segmentPatterns(values []string) []string {
	var all []string
	for _, v := range values {
		if segmentable(v) {
			all = append(all, v)
		}
	}
	// Both are in the spelling segmentable accepts, so one holds the other
	// as a run of segments exactly when "-other-" is in "-one-".
	return slices.DeleteFunc(slices.Clone(all), func(p string) bool {
		return slices.ContainsFunc(all, func(o string) bool { return o != p && strings.Contains("-"+p+"-", "-"+o+"-") })
	})
}

// segmentNeedles are the texts the first filter looks for in a workload name
// so that every row the second filter keeps reaches it: each pattern as
// written with "-" and with ".", without those that hold another. A workload
// name that joins the words of one name with both delimiters is not found.
func segmentNeedles(patterns []string) []string {
	set := map[string]bool{}
	for _, p := range patterns {
		set[p] = true
		set[strings.ReplaceAll(p, "-", ".")] = true
	}
	all := sortedKeys(set)
	return slices.DeleteFunc(slices.Clone(all), func(n string) bool {
		return slices.ContainsFunc(all, func(o string) bool { return o != n && strings.Contains(n, o) })
	})
}

// processGroupLogsQuery counts the logs that carry one of ids. Its filter is
// the one a process-group binding renders, so what it counts is what a scoped
// fetch logs would reach. Without ids it is the plan's placeholder.
func processGroupLogsQuery(ids []string) Query {
	list := processGroupPlaceholder
	if len(ids) > 0 {
		list = quotedList(ids)
	}
	return Query{Purpose: purposeProcessGroupLogs, DataObject: objectLogs, DQL: strings.Join([]string{
		fmt.Sprintf("fetch %s, from:now()-%s, scanLimitGBytes:%d", objectLogs, discoveryWindow, ScanLimitGB),
		fmt.Sprintf("| filter in(%s, array(%s))", fieldProcessGroup, list),
		fmt.Sprintf("| summarize %s = count(), by:{%s}", fieldRecords, fieldProcessGroup),
		fmt.Sprintf("| limit %d", discoveryRowLimit),
	}, "\n")}
}

func entityQuery(object, purpose string, values []string) Query {
	clauses := make([]string, len(values))
	for i, v := range values {
		clauses[i] = fmt.Sprintf("matchesValue(%s, %s)", fieldEntityName, dql.Quote("*"+v+"*"))
	}
	return Query{Purpose: purpose, DataObject: object, DQL: strings.Join([]string{
		fmt.Sprintf("fetch %s, from:now()-%s", object, discoveryWindow),
		"| filter " + strings.Join(clauses, " or "),
		fmt.Sprintf("| fields %s, %s", fieldEntityID, fieldEntityName),
		fmt.Sprintf("| fieldsAdd %s = stringLength(%s)", fieldNameLength, fieldEntityName),
		fmt.Sprintf("| sort %s asc", fieldNameLength),
		fmt.Sprintf("| limit %d", discoveryRowLimit),
	}, "\n")}
}

// Verdict is how certain discovery is, per kind of binding and overall:
// match is exactly one exact-tier value, ambiguous is several (or a partial
// or sampled result), none is none.
type Verdict string

const (
	VerdictMatch     Verdict = "match"
	VerdictAmbiguous Verdict = "ambiguous"
	VerdictNone      Verdict = "none"
)

// DiscoveryReport is the outcome of Discover, and the agent envelope's
// result as is.
type DiscoveryReport struct {
	Environment string `json:"environment" yaml:"environment"`
	// Dir and Unit are the plan's.
	Dir     string  `json:"dir" yaml:"dir"`
	Unit    string  `json:"unit" yaml:"unit"`
	Verdict Verdict `json:"verdict" yaml:"verdict"`
	// Verdicts is keyed by service, processGroup, workload and serviceName.
	Verdicts   map[string]Verdict `json:"verdicts" yaml:"verdicts"`
	Candidates []Candidate        `json:"candidates" yaml:"candidates"`
	// Queries is every planned query, run or skipped.
	Queries []Query `json:"queries" yaml:"queries"`
	// Sent is the values that left the machine: the plan's, once a query ran.
	Sent  []string `json:"sent" yaml:"sent"`
	Notes []string `json:"notes,omitempty" yaml:"notes,omitempty"`
	// Partial is a query that failed, timed out, was truncated or never ran
	// because the run was cancelled, a sample that matched nothing or less
	// than the capped scan it replaced had seen, or a repository too large
	// to read whole (Plan.Truncated), so a "none" is
	// not evidence that nothing runs this code. A sample that matched is
	// complete at the floor its note states, so it is not partial, but it
	// is never a match either: see SampledRecords.
	Partial bool `json:"partial,omitempty" yaml:"partial,omitempty"`
}

// Discover runs p's queries through runner and ranks what came back. It only
// reads. A cancelled run returns the report so far together with ctx.Err(),
// so a Ctrl-C after the first query still shows what it found.
func Discover(ctx context.Context, runner inventory.Runner, p *Plan, host string) (*DiscoveryReport, error) {
	r := &DiscoveryReport{Environment: host, Dir: p.Dir, Unit: p.Unit,
		Queries: append([]Query{}, p.Queries...), Sent: []string{}}
	d := newDiscovery(p)
	if len(p.Queries) == 0 {
		r.note(fmt.Sprintf("no usable name found for unit %q", p.Unit))
	}
	if len(p.Dropped) > 0 {
		r.note(fmt.Sprintf("names that look like test fixtures are not sent: %s; use --term to send one", strings.Join(p.Dropped, ", ")))
	}
	if p.Truncated {
		// A name that was not sent could have found a second candidate.
		r.Partial = true
		r.note(p.TruncationNote())
	}
	for i := range r.Queries {
		q := &r.Queries[i]
		switch {
		case q.SampledRetry():
			// Decided together with the record query it retries.
			continue
		case q.ProcessGroupCheck():
			// Decided once the record queries have answered.
			q.Skipped = true
			continue
		case q.fallback() && len(d.records) > 0:
			q.Skipped = true
			continue
		case ctx.Err() != nil:
			// A query the cancellation kept from running is a gap in the
			// answer like one that failed: what it would have found could
			// be a second candidate.
			r.skip(ctx, q)
			for _, rung := range r.rungsAfter(i) {
				r.skip(ctx, rung)
			}
			continue
		}
		r.Sent = p.Sent
		if q.fallback() {
			r.runFallback(ctx, runner, q, d)
		} else {
			r.runRecordQuery(ctx, runner, q, r.rungsAfter(i), d)
		}
	}
	for _, n := range r.samplingNotes() {
		r.note(n)
	}
	d.foldServiceless()
	d.foldClusterless()
	r.checkLogs(ctx, runner, d)
	d.finish(r)
	return r, ctx.Err()
}

// rungsAfter is the sampled retries planned right after query i.
func (r *DiscoveryReport) rungsAfter(i int) []*Query {
	var rungs []*Query
	for j := i + 1; j < len(r.Queries) && r.Queries[j].SampledRetry(); j++ {
		rungs = append(rungs, &r.Queries[j])
	}
	return rungs
}

func (r *DiscoveryReport) runFallback(ctx context.Context, runner inventory.Runner, q *Query, d *discovery) {
	res, err := runner.RunQuery(ctx, q.DQL)
	if err != nil {
		r.fail(ctx, q, err)
		return
	}
	r.fold(q, res, d)
}

// checkLogs looks at the candidates whose spans matched and no logs did.
// The record queries find logs by workload and service name, and logs need
// not carry either: OneAgent may enrich them with the process group alone. A
// workload binding then reaches the candidate's spans and none of its logs,
// which reads exactly like a quiet service.
//
// It applies only when the logs query read the whole window, since a capped or
// sampled read has not shown the logs to be absent. A candidate whose process
// groups are known in full, and are no other workload's, is then looked up by
// them in one query; the others are only reported. A run cancelled by then
// says so only when there was something to look up.
func (r *DiscoveryReport) checkLogs(ctx context.Context, runner inventory.Runner, d *discovery) {
	q := r.processGroupCheck()
	if q == nil || !r.readWhole(objectLogs, d) {
		return
	}
	spansWhole := r.readWhole(objectSpans, d)
	var checked []*Candidate
	var ids []string
	for _, c := range d.records {
		if c.Spans == 0 || c.Logs > 0 {
			continue
		}
		c.logsMissed = true
		if spansWhole && d.ownsProcessGroups(c) {
			checked = append(checked, c)
			for _, id := range c.ProcessGroups {
				ids = addSorted(ids, id)
			}
		}
	}
	if len(ids) == 0 || len(ids) > discoveryIDLimit {
		return
	}
	if ctx.Err() != nil {
		// Only now is the lookup known to have been due. An interruption
		// that kept no query from running left nothing unread, and is not
		// worth a note.
		q.Cause = failureCause(ctx, ctx.Err())
		r.note(failureNote(q))
		return
	}
	*q = processGroupLogsQuery(ids)
	res, err := runner.RunQuery(ctx, q.DQL)
	if err != nil {
		// What this query misses is a binding, never a candidate, so its
		// failure does not make the report partial.
		q.Cause = failureCause(ctx, err)
		q.Error, _, _ = strings.Cut(err.Error(), "\n")
		r.note(failureNote(q))
		return
	}
	q.Rows = len(res.Records)
	limit, cut := cutShort(q, res)
	cut = cut || q.Rows >= discoveryRowLimit
	if cut {
		q.Truncated, q.Cause, q.truncation = true, causeTruncated, limit
	}
	for _, c := range checked {
		for _, rec := range res.Records {
			rowIDs, _, _ := idsField(rec, fieldProcessGroup, processGroupRe)
			if slices.ContainsFunc(rowIDs, func(id string) bool { return slices.Contains(c.ProcessGroups, id) }) {
				c.LogsByProcessGroup += countField(rec, fieldRecords)
			}
		}
		// A cut answer proves the logs it found, not the absence of others.
		c.logsChecked = !cut || c.LogsByProcessGroup > 0
	}
}

// processGroupCheck is the planned logs-by-process-group query.
func (r *DiscoveryReport) processGroupCheck() *Query {
	for i := range r.Queries {
		if r.Queries[i].ProcessGroupCheck() {
			return &r.Queries[i]
		}
	}
	return nil
}

// readWhole reports whether the record query for object answered for the
// whole window: it ran, nothing cut it short, and no sampled retry stood in.
func (r *DiscoveryReport) readWhole(object string, d *discovery) bool {
	for i := range r.Queries {
		q := &r.Queries[i]
		if q.DataObject == object && !q.SampledRetry() && !q.ProcessGroupCheck() {
			return !q.Skipped && q.Error == "" && q.Cause == "" && d.samples[object] == nil
		}
	}
	return false
}

// runRecordQuery runs a record query and, when the scan cap stopped it, its
// sampled retries. Only a scan cap is a volume problem, and only a volume
// problem is one sampling solves: a timeout or a consumption limit would
// stop the retry too. The capped rows are kept aside rather than folded,
// because counts from a partial scan are not comparable with a sample's;
// they stand only when no retry answers. When one does, the identities the
// capped rows name and the sample did not return are still candidates (see
// addCapped), and the result is partial: the capped scan saw something the
// sample missed, in a part of the window nobody knows the size of.
func (r *DiscoveryReport) runRecordQuery(ctx context.Context, runner inventory.Runner, q *Query, rungs []*Query, d *discovery) {
	res, err := runner.RunQuery(ctx, q.DQL)
	if err != nil {
		r.fail(ctx, q, err)
		skipUnneeded(rungs)
		return
	}
	if limit, _ := cutShort(q, res); limit == inventory.TruncationScanLimit {
		q.Rows, q.Truncated, q.Cause, q.truncation = len(res.Records), true, causeTruncated, limit
		if r.sample(ctx, runner, rungs, d) {
			if d.addCapped(q.DataObject, res.Records) {
				r.Partial = true
				r.note(fmt.Sprintf("the %s scan the %d GB cap stopped saw candidates the sample did not return; they are listed without a count, and the result is partial",
					q.DataObject, ScanLimitGB))
			}
			return
		}
	} else {
		skipUnneeded(rungs)
	}
	r.fold(q, res, d)
}

// sample walks the rungs of a record query the scan cap stopped and folds
// the first one that answers with candidates. It reports whether one did.
//
// A rung that completes empty does not end the walk, because a finer one
// may find a quieter service; but before each rung runs, the scan of the
// last one that ran is scaled by the steps in between, and a rung predicted
// to hit the cap again is skipped rather than sent. A rung Grail would not
// sample, or one cut short, ends it: the next one reads more. Not sampled
// means a result without the sampled flag that read something.
func (r *DiscoveryReport) sample(ctx context.Context, runner inventory.Runner, rungs []*Query, d *discovery) (answered bool) {
	var predicted int64 // what the next rung will scan; 0 when unknown
	done, capped := false, false
	for _, q := range rungs {
		switch {
		case done:
			q.Skipped = true
			continue
		case capped:
			q.Skipped, q.Cause = true, causePredictedCap
			continue
		case ctx.Err() != nil:
			r.skip(ctx, q)
			continue
		}
		if predicted *= q.step; predicted > scanLimitBytes {
			q.Skipped, q.Cause = true, causePredictedCap
			continue
		}
		res, err := runner.RunQuery(ctx, q.DQL)
		if err != nil {
			// A cancellation leaves the rest of the walk a gap; any other
			// failure ends it, with the capped answer standing.
			r.fail(ctx, q, err)
			done = ctx.Err() == nil
			continue
		}
		q.Rows = len(res.Records)
		limit, cut := cutShort(q, res)
		switch {
		case !res.Sampled && (q.Rows > 0 || res.ScannedBytes > 0):
			// As in inventory: every further rung would be the capped query
			// over again. A rung that read nothing is not one: Grail leaves
			// the sampled flag out of a result that scanned no record, so
			// there the flag says nothing and the rung is simply empty.
			q.Cause, done = causeSamplingRefused, true
			r.note(failureNote(q))
		case cut:
			// Even sampled this rung does not fit, and every rung after it
			// reads more.
			q.Truncated, q.Cause, q.truncation = true, causeTruncated, limit
			capped = limit == inventory.TruncationScanLimit
			done = !capped
			r.note(failureNote(q))
		case q.Rows == 0:
			// An unknown scan keeps the prediction scaled from the last
			// known one rather than resetting it.
			if res.ScannedBytes > 0 {
				predicted = res.ScannedBytes
			}
		default:
			d.samples[q.DataObject] = q
			r.fold(q, res, d)
			answered, done = true, true
		}
	}
	return answered
}

// skipUnneeded marks rungs the record query before them made unnecessary.
func skipUnneeded(rungs []*Query) {
	for _, q := range rungs {
		q.Skipped = true
	}
}

// skip marks q as kept from running by the cancellation of ctx.
func (r *DiscoveryReport) skip(ctx context.Context, q *Query) {
	q.Skipped, q.Cause, r.Partial = true, failureCause(ctx, ctx.Err()), true
	r.note(failureNote(q))
}

func (r *DiscoveryReport) fail(ctx context.Context, q *Query, err error) {
	q.Cause = failureCause(ctx, err)
	q.Error, _, _ = strings.Cut(err.Error(), "\n")
	// A table the environment does not have is a known absence, not a gap in
	// the answer: nothing is hidden in it.
	r.Partial = r.Partial || q.Cause != causeUnknownObject
	r.note(failureNote(q))
}

// fold records how complete q's result is and adds its rows to the
// candidates.
func (r *DiscoveryReport) fold(q *Query, res *inventory.RunResult, d *discovery) {
	q.Rows = len(res.Records)
	limit, cut := cutShort(q, res)
	switch {
	case cut:
		q.Truncated, q.Cause, q.truncation, r.Partial = true, causeTruncated, limit, true
		r.note(failureNote(q))
	case q.Rows >= discoveryRowLimit:
		// The limit stage is the query's own, not a cap Grail reports, so a
		// page as long as the limit is taken to have cut something off.
		q.Truncated, q.Cause, r.Partial = true, causeRowLimit, true
		r.note(failureNote(q))
	}
	for _, rec := range res.Records {
		if q.fallback() {
			d.addEntity(q.DataObject, rec)
		} else {
			d.addRecord(q.DataObject, rec, q.SampledRetry())
		}
	}
}

// cutShort reports whether a limit Grail reported cut res short, and which.
// A response can report several: the scan cap wins, since it says how little
// was read and is the one a sampled retry answers, then the first reported.
//
// One report is not taken as a cut: a result limit on a query that collects
// ids (a record query or one of its retries), recognised by its message
// alone. Grail may word an id array cut at its maxLength that way, and the
// row then says so itself by holding more ids than a binding takes. A page
// that lost rows is reported with a type, and one as long as the query's own
// limit is judged by its length whatever Grail says.
//
// A runner that reports only one cause is taken at its word, as if typed.
func cutShort(q *Query, res *inventory.RunResult) (limit inventory.TruncationCause, cut bool) {
	limits := res.Truncations
	if len(limits) == 0 && res.Truncated {
		limits = []inventory.Truncation{{Cause: res.TruncationCause, Typed: true}}
	}
	collectsIDs := !q.fallback() && !q.ProcessGroupCheck()
	for _, l := range limits {
		switch {
		case collectsIDs && l.Cause == inventory.TruncationResultLimit && !l.Typed:
		case l.Cause == inventory.TruncationScanLimit:
			return l.Cause, true
		case !cut:
			limit, cut = l.Cause, true
		}
	}
	return limit, cut
}

// samplingNotes states, for each data object discovery had to sample, what
// the sample can have missed. The floor comes from the ratio a rung asked
// for: Grail only ever samples more finely than asked, so it is the
// conservative bound, and it is defined when the sample matched nothing.
// A sampled match is complete at that floor; a sampled miss is a bound, not
// an absence, so the report stays partial. An object the environment turned
// out not to sample gets no such note: its capped answer stands, and an
// earlier rung that came back empty bounds nothing.
func (r *DiscoveryReport) samplingNotes() []string {
	var answered []*Query
	for i := range r.Queries {
		q := &r.Queries[i]
		if !q.SampledRetry() || q.Skipped || q.Error != "" || (q.Cause != "" && q.Cause != causeRowLimit) || r.samplingRefused(q.DataObject) {
			continue
		}
		// The last rung of an object that answered is the finest: it is
		// the one that found something, or the tightest bound on a miss.
		if n := len(answered); n > 0 && answered[n-1].DataObject == q.DataObject {
			answered[n-1] = q
		} else {
			answered = append(answered, q)
		}
	}
	notes := make([]string, len(answered))
	for i, q := range answered {
		notes[i] = samplingNote(q)
	}
	return notes
}

// SampledRecords reports whether a sampled retry read the records of a data
// object, whatever it matched. A candidate quieter than its floor may then
// be missing from the report, read whole or not, so a service name the
// report shows to be unshared may still be shared.
func (r *DiscoveryReport) SampledRecords() bool {
	return slices.ContainsFunc(r.Queries, func(q Query) bool {
		return q.SampledRetry() && !q.Skipped && q.Error == "" && !r.samplingRefused(q.DataObject)
	})
}

// samplingRefused reports whether a sampled retry over object came back
// unsampled: the environment does not sample it, whatever an earlier, empty
// retry seemed to say.
func (r *DiscoveryReport) samplingRefused(object string) bool {
	return slices.ContainsFunc(r.Queries, func(q Query) bool {
		return q.DataObject == object && q.Cause == causeSamplingRefused
	})
}

func samplingNote(q *Query) string {
	sampled := fmt.Sprintf("%s scan more than the %d GB cap over %s, so they were sampled 1-in-%d over %s",
		q.DataObject, ScanLimitGB, discoveryWindow, q.SamplingRatio, q.Window)
	floor := fmt.Sprintf("a service with fewer than about %d %s over %s may be missing",
		inventory.SampledMissBound(q.SamplingRatio), q.DataObject, q.Window)
	if q.Rows == 0 {
		return sampled + " and matched nothing; " + floor + ", so the result is partial"
	}
	return sampled + "; " + floor
}

func failureCause(ctx context.Context, err error) string {
	// A runner may report a lapsed deadline as a plain cancellation.
	switch {
	case errors.Is(err, context.DeadlineExceeded) || errors.Is(ctx.Err(), context.DeadlineExceeded):
		return causeTimeout
	case errors.Is(ctx.Err(), context.Canceled):
		return causeCancelled
	}
	var qe *sdkquery.QueryError
	if errors.As(err, &qe) {
		// Grail reports a table the token cannot read as the error type, or
		// only in the text beside another type.
		const denied = "NOT_AUTHORIZED_FOR_TABLE"
		switch {
		case qe.ErrorType == denied || strings.Contains(qe.Message, denied) || strings.Contains(qe.Detail, denied):
			return causeAuth
		case qe.ErrorType == "UNKNOWN_DATA_OBJECT" || qe.ErrorType == "DATA_OBJECT_NOT_SUPPORTED":
			return causeUnknownObject
		}
	}
	return causeError
}

func failureNote(q *Query) string {
	if q.ProcessGroupCheck() && q.Skipped {
		// What this lookup would have found is a binding, never a
		// candidate, so the note does not call the result incomplete.
		why := "was interrupted"
		if q.Cause == causeTimeout {
			why = "ran out of time"
		}
		return fmt.Sprintf("discovery %s before it looked for logs by process group", why)
	}
	switch q.Cause {
	case causeAuth:
		return fmt.Sprintf("the token cannot read %s", q.DataObject)
	case causeTimeout:
		return "discovery ran out of time; a narrower directory or an explicit name sends less"
	case causeCancelled:
		return "discovery was interrupted, so the result is incomplete"
	case causeTruncated:
		return fmt.Sprintf("%s stopped at %s, so the verdict is capped at ambiguous", q.DataObject, truncationLimit(q.truncation))
	case causeRowLimit:
		return fmt.Sprintf("%s returned %d rows, the most a discovery query reads, so the verdict is capped at ambiguous",
			q.DataObject, discoveryRowLimit)
	case causeUnknownObject:
		if !q.fallback() {
			return fmt.Sprintf("%s is not available on this environment, so nothing was looked for there", q.DataObject)
		}
		return fmt.Sprintf("%s is not available on this environment; the record queries are what applies here", q.DataObject)
	case causeSamplingRefused:
		return fmt.Sprintf("this environment did not sample %s, so there is no cheaper query than the capped one", q.DataObject)
	}
	return fmt.Sprintf("%s failed, so the result may be incomplete: %s", q.Purpose, q.Error)
}

// truncationLimit names the limit Grail reported for a cut-short result.
func truncationLimit(cause inventory.TruncationCause) string {
	switch cause {
	case inventory.TruncationScanLimit:
		return fmt.Sprintf("the %d GB scan cap", ScanLimitGB)
	case inventory.TruncationResultLimit:
		return "Grail's result limit"
	case inventory.TruncationTimeout:
		return "Grail's time limit"
	case inventory.TruncationConsumption:
		return "the query consumption limit"
	}
	return "a limit Grail reported"
}

func (r *DiscoveryReport) note(n string) {
	for _, existing := range r.Notes {
		if existing == n {
			return
		}
	}
	r.Notes = append(r.Notes, n)
}
