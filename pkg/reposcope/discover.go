package reposcope

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"path"
	"strings"

	"github.com/dynatrace-oss/dtctl/pkg/dql"
	sdkquery "github.com/dynatrace-oss/dtctl/sdk/api/query"
	"github.com/dynatrace-oss/dtctl/sdk/inventory"
)

// ScanLimitGB is a discovery query's scan cap. The record queries carry it in
// their own text, so a dry run shows it; the entity-table fallbacks need it
// as the request default.
const ScanLimitGB = 5

const (
	discoveryWindow   = "24h"
	discoveryRowLimit = 200
)

// Why a query did not give a whole answer.
const (
	causeTimeout       = "timeout"
	causeAuth          = "auth"
	causeUnknownObject = "unknown_object"
	causeError         = "error"
	causeTruncated     = "truncated"
	causeRowLimit      = "row_limit"
	causeCancelled     = "cancelled"
)

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
	// truncation is the limit Grail reported for a truncated result, which
	// the note names; Cause stays "truncated" whichever it was.
	truncation inventory.TruncationCause
}

// fallback reports an entity-table query, run only when the record queries
// found nothing.
func (q *Query) fallback() bool {
	return q.DataObject == objectServices || q.DataObject == objectProcessGroups
}

// Plan is what discovery will send for one unit: each query with its exact
// DQL, and every value in them. It is built without a network, so a dry run
// shows precisely what a real run sends.
type Plan struct {
	// Dir is the directory discovery was asked about, relative to the
	// repository root; Unit is the build unit holding it. They differ in a
	// monorepo with one build file at the top, where Dir's own name is
	// among what was sent but Unit is the whole repository.
	Dir     string   `json:"dir" yaml:"dir"`
	Unit    string   `json:"unit" yaml:"unit"`
	Sent    []string `json:"sent" yaml:"sent"`
	Queries []Query  `json:"queries" yaml:"queries"`
	tokens  []token
}

// PlanDiscovery reads the names repo gives the unit holding dir and plans the
// queries that look them up. Sent is every variant of those names and of each
// term, and each one's own spelling, sorted; namespaces are never sent, only
// matched against what the records say. With nothing to send there are no
// queries.
//
// The record queries aggregate 24 hours of spans and logs whose workload or
// service.name is one of the values, by every identity a binding can use.
// The entity-table queries search entity names and run only when the record
// queries find nothing.
func PlanDiscovery(repo fs.FS, dir string, terms []string) (*Plan, error) {
	s, err := extract(repo, dir)
	if err != nil {
		return nil, err
	}
	p := &Plan{Dir: path.Clean(dir), Unit: s.unit, Sent: sendValues(s.tokens, terms), Queries: []Query{}, tokens: s.tokens}
	if len(p.Sent) > 0 {
		p.Queries = []Query{
			recordQuery(objectSpans, "spans by workload or service name", p.Sent),
			recordQuery(objectLogs, "logs by workload or service name", p.Sent),
			entityQuery(objectServices, "services by entity name", p.Sent),
			entityQuery(objectProcessGroups, "process groups by entity name", p.Sent),
		}
	}
	return p, nil
}

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
		if term = strings.TrimSpace(term); term != "" {
			set[term] = true
			set[strings.ToLower(term)] = true
			for _, v := range variants(term) {
				set[v] = true
			}
		}
	}
	return sortedKeys(set)
}

func recordQuery(object, purpose string, values []string) Query {
	list := quotedList(values)
	return Query{Purpose: purpose, DataObject: object, DQL: strings.Join([]string{
		fmt.Sprintf("fetch %s, from:now()-%s, scanLimitGBytes:%d", object, discoveryWindow, ScanLimitGB),
		fmt.Sprintf("| filter in(%s, array(%s)) or in(%s, array(%s))", fieldWorkload, list, fieldServiceName, list),
		fmt.Sprintf("| summarize records = count(), by:{%s, %s, %s, %s, %s}",
			fieldNamespace, fieldWorkload, fieldServiceName, fieldService, fieldProcessGroup),
		"| sort records desc",
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
// result), none is none.
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
	// because the run was cancelled, so a "none" is not evidence that
	// nothing runs this code.
	Partial bool `json:"partial,omitempty" yaml:"partial,omitempty"`
}

// Discover runs p's queries through runner and ranks what came back. It only
// reads. A cancelled run returns the report so far together with ctx.Err(),
// so a Ctrl-C after the first query still shows what it found.
func Discover(ctx context.Context, runner inventory.Runner, p *Plan, host string) (*DiscoveryReport, error) {
	r := &DiscoveryReport{Environment: host, Dir: p.Dir, Unit: p.Unit, Queries: append([]Query{}, p.Queries...), Sent: []string{}}
	d := newDiscovery(p)
	if len(p.Queries) == 0 {
		r.note(fmt.Sprintf("no usable name found for unit %q", p.Unit))
	}
	for i := range r.Queries {
		q := &r.Queries[i]
		if q.fallback() && len(d.records) > 0 {
			q.Skipped = true
			continue
		}
		// A query the cancellation kept from running is a gap in the
		// answer like one that failed: what it would have found could be a
		// second candidate.
		if ctx.Err() != nil {
			q.Skipped, q.Cause, r.Partial = true, failureCause(ctx, ctx.Err()), true
			r.note(failureNote(q))
			continue
		}
		r.Sent = p.Sent
		r.run(ctx, runner, q, d)
	}
	d.finish(r)
	return r, ctx.Err()
}

func (r *DiscoveryReport) run(ctx context.Context, runner inventory.Runner, q *Query, d *discovery) {
	res, err := runner.RunQuery(ctx, q.DQL)
	if err != nil {
		q.Cause = failureCause(ctx, err)
		q.Error, _, _ = strings.Cut(err.Error(), "\n")
		// A missing entity table is a known absence, not a gap in the
		// answer: only the fallbacks query one.
		r.Partial = r.Partial || q.Cause != causeUnknownObject
		r.note(failureNote(q))
		return
	}
	q.Rows = len(res.Records)
	switch {
	case res.Truncated:
		q.Truncated, q.Cause, q.truncation, r.Partial = true, causeTruncated, res.TruncationCause, true
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
			d.addRecord(q.DataObject, rec)
		}
	}
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
		switch qe.ErrorType {
		case "NOT_AUTHORIZED_FOR_TABLE":
			return causeAuth
		case "UNKNOWN_DATA_OBJECT", "DATA_OBJECT_NOT_SUPPORTED":
			return causeUnknownObject
		}
	}
	return causeError
}

func failureNote(q *Query) string {
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
		return fmt.Sprintf("%s is not available on this environment; the record queries are what applies here", q.DataObject)
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
