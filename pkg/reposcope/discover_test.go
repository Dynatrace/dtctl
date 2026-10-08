package reposcope

import (
	"context"
	"encoding/json"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/dynatrace-oss/dtctl/pkg/dql"
	sdkquery "github.com/dynatrace-oss/dtctl/sdk/api/query"
	"github.com/dynatrace-oss/dtctl/sdk/inventory"
)

// fakeRunner answers discovery queries from fixtures, keyed by the data
// object the query fetches, and records every DQL text it was sent.
type fakeRunner struct {
	answers map[string]answer
	sent    []string
	onRun   func(ctx context.Context, object string) // runs before answering
}

type answer struct {
	records    []map[string]interface{}
	truncation inventory.TruncationCause
	err        error
}

func (r *fakeRunner) RunQuery(ctx context.Context, dql string) (*inventory.RunResult, error) {
	r.sent = append(r.sent, dql)
	object := strings.TrimSuffix(strings.Fields(strings.TrimPrefix(dql, "fetch "))[0], ",")
	if r.onRun != nil {
		r.onRun(ctx, object)
	}
	a := r.answers[object]
	if a.err != nil {
		return nil, a.err
	}
	return &inventory.RunResult{Records: a.records, Truncated: a.truncation != "", TruncationCause: a.truncation}, nil
}

// rows reads testdata/discovery/<name>.json.
func rows(t *testing.T, name string) []map[string]interface{} {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", "discovery", name+".json"))
	require.NoError(t, err)
	var out []map[string]interface{}
	require.NoError(t, json.Unmarshal(data, &out))
	return out
}

// singleGo is the plan for the single-go fixture at its root.
func singleGo(t *testing.T) *Plan {
	t.Helper()
	p, err := PlanDiscovery(fixture(t, "single-go"), ".", nil)
	require.NoError(t, err)
	return p
}

func TestPlan(t *testing.T) {
	p := singleGo(t)
	require.Len(t, p.Queries, 4)
	assert.Equal(t, ".", p.Unit)
	assert.Equal(t, []string{"checkout", "checkout-service", "checkout.service", "checkout_service", "checkoutservice"}, p.Sent)

	list := `"checkout", "checkout-service", "checkout.service", "checkout_service", "checkoutservice"`
	assert.Equal(t, Query{
		Purpose:    "spans by workload or service name",
		DataObject: "spans",
		DQL: "fetch spans, from:now()-24h, scanLimitGBytes:5\n" +
			"| filter in(k8s.workload.name, array(" + list + ")) or in(service.name, array(" + list + "))\n" +
			"| summarize records = count(), by:{k8s.namespace.name, k8s.workload.name, service.name, dt.entity.service, dt.entity.process_group}\n" +
			"| sort records desc\n" +
			"| limit 200",
	}, p.Queries[0])
	assert.Equal(t, "logs", p.Queries[1].DataObject)
	assert.Equal(t, strings.Replace(p.Queries[0].DQL, "fetch spans", "fetch logs", 1), p.Queries[1].DQL)
	assert.Equal(t, Query{
		Purpose:    "services by entity name",
		DataObject: "dt.entity.service",
		DQL: "fetch dt.entity.service, from:now()-24h\n" +
			`| filter matchesValue(entity.name, "*checkout*") or matchesValue(entity.name, "*checkout-service*") or matchesValue(entity.name, "*checkout.service*") or matchesValue(entity.name, "*checkout_service*") or matchesValue(entity.name, "*checkoutservice*")` + "\n" +
			"| fields id, entity.name\n" +
			"| fieldsAdd nameLength = stringLength(entity.name)\n" +
			"| sort nameLength asc\n" +
			"| limit 200",
	}, p.Queries[2])
	assert.Equal(t, "dt.entity.process_group", p.Queries[3].DataObject)
}

// Every dotted name a planned query uses is one of the names in grail.go, so
// renaming a Grail field there renames it everywhere.
func TestPlan_NamesOnlyGrailTableFields(t *testing.T) {
	known := map[string]bool{
		fieldService: true, fieldProcessGroup: true, fieldServiceName: true, fieldNamespace: true,
		fieldWorkload: true, fieldEntityName: true, // the entity-table objects share the id fields' names
	}
	dotted := regexp.MustCompile(`[a-z][a-z0-9_]*(\.[a-z0-9_]+)+`)
	for _, q := range singleGo(t).Queries {
		m, ok := dql.Scan(q.DQL) // literals are masked: only code is checked
		require.True(t, ok)
		for _, name := range dotted.FindAllString(m.Code, -1) {
			assert.True(t, known[name], "%s in %s", name, q.Purpose)
		}
	}
}

func TestPlan_NamespacesAreNeverSent(t *testing.T) {
	p := singleGo(t)
	assert.NotContains(t, p.Sent, "payments")
	for _, q := range p.Queries {
		assert.NotContains(t, q.DQL, "payments")
	}
}

func TestPlan_Terms(t *testing.T) {
	p, err := PlanDiscovery(fixture(t, "no-signals"), ".", nil)
	require.NoError(t, err)
	assert.Empty(t, p.Queries, "nothing worth sending")

	p, err = PlanDiscovery(fixture(t, "no-signals"), ".", []string{" BillingEngine ", ""})
	require.NoError(t, err)
	require.Len(t, p.Queries, 4)
	assert.Equal(t, []string{"BillingEngine", "billing-engine", "billing.engine", "billing_engine", "billingengine"}, p.Sent,
		"a term is sent as given, lowercased, and in its variants")
}

// Record fields compare case-sensitively, so a name the repository spells in
// capitals is sent that way too, and a record carrying it is credited to the
// file it came from.
func TestPlan_SendsTheNameAsWritten(t *testing.T) {
	fsys := fstest.MapFS{
		"go.mod":       {Data: []byte("module example.invalid/BillingEngine\n\ngo 1.22\n")},
		"package.json": {Data: []byte(`{"name": "@acme/Billing Engine"}`)},
	}
	p, err := PlanDiscovery(fsys, ".", nil)
	require.NoError(t, err)
	assert.Equal(t, []string{"BillingEngine", "billing-engine", "billing.engine", "billing_engine", "billingengine"}, p.Sent,
		"a spelling with a space is not sent; its variants are")
	for _, q := range p.Queries[:2] {
		assert.Contains(t, q.DQL, `"BillingEngine"`, q.Purpose)
	}

	r, err := Discover(context.Background(), &fakeRunner{answers: map[string]answer{
		"spans": {records: []map[string]interface{}{{fieldServiceName: "BillingEngine", "records": float64(3)}}},
	}}, p, prodHost)
	require.NoError(t, err)
	require.Len(t, r.Candidates, 1)
	assert.Equal(t, `service.name "BillingEngine" from go.mod:1`, r.Candidates[0].Evidence[0])
}

func TestDiscover_Match(t *testing.T) {
	p := singleGo(t)
	runner := &fakeRunner{answers: map[string]answer{
		"spans": {records: rows(t, "spans-checkout")},
		"logs":  {records: rows(t, "logs-checkout")},
	}}
	r, err := Discover(context.Background(), runner, p, prodHost)
	require.NoError(t, err)

	assert.Len(t, runner.sent, 2, "the entity-table fallbacks are not needed")
	assert.True(t, r.Queries[2].Skipped)
	assert.True(t, r.Queries[3].Skipped)
	assert.Equal(t, VerdictMatch, r.Verdict)
	assert.Equal(t, map[string]Verdict{
		"service": VerdictMatch, "processGroup": VerdictMatch, "workload": VerdictMatch, "serviceName": VerdictMatch,
	}, r.Verdicts)
	assert.False(t, r.Partial)
	assert.Equal(t, p.Sent, r.Sent)

	require.Len(t, r.Candidates, 1, "the logs row joins the spans row of the same workload")
	assert.Equal(t, Candidate{
		Rank:          1,
		Name:          "checkout",
		Services:      []string{svcID},
		ProcessGroups: []string{pgID},
		ServiceName:   "checkout",
		Namespace:     "payments",
		Workload:      "checkout",
		Tier:          "exact",
		Spans:         12400,
		Logs:          88000,
		Evidence: []string{
			`k8s.workload.name "checkout" from Dockerfile:4, deploy/k8s/deployment.yaml:4, deploy/k8s/deployment.yaml:12, deploy/k8s/deployment.yaml:15, go.mod:1`,
			`service.name "checkout" from Dockerfile:4, deploy/k8s/deployment.yaml:4, deploy/k8s/deployment.yaml:12, deploy/k8s/deployment.yaml:15, go.mod:1`,
			`k8s.namespace.name "payments" from deploy/k8s/deployment.yaml:5`,
			"12400 spans, 88000 logs in 24h",
		},
	}, r.Candidates[0])
}

func TestDiscover_FallbackTiers(t *testing.T) {
	runner := &fakeRunner{answers: map[string]answer{
		"dt.entity.service": {records: rows(t, "services-by-name")},
	}}
	r, err := Discover(context.Background(), runner, singleGo(t), prodHost)
	require.NoError(t, err)

	assert.Len(t, runner.sent, 4, "the record queries found nothing, so the fallbacks ran")
	var got []string
	for _, c := range r.Candidates {
		got = append(got, fmt.Sprintf("%d %s %s %s | %s", c.Rank, c.Name, c.Services, c.Tier, strings.Join(c.Evidence, "; ")))
	}
	assert.Equal(t, []string{
		`1 checkout [SERVICE-0000000000000C02] exact | entity.name "Checkout" = entrypoint "checkout" (Dockerfile:4) (exact match)`,
		`2 checkout [SERVICE-0000000000000C01] segment | entity.name "checkout-api-gateway" contains entrypoint "checkout" (Dockerfile:4) (segment match)`,
		`3 checkout [SERVICE-0000000000000C03] substring | entity.name "precheckouts" contains entrypoint "checkout" (Dockerfile:4) (substring match)`,
	}, got)
	assert.Equal(t, VerdictAmbiguous, r.Verdict)
	assert.Equal(t, VerdictAmbiguous, r.Verdicts["service"])
	assert.Equal(t, VerdictNone, r.Verdicts["workload"])
}

func TestDiscover_FallbackFailures(t *testing.T) {
	runner := &fakeRunner{answers: map[string]answer{
		"dt.entity.service":       {err: &sdkquery.QueryError{StatusCode: 403, ErrorType: "NOT_AUTHORIZED_FOR_TABLE", Message: "not authorized"}},
		"dt.entity.process_group": {err: fmt.Errorf("query: %w", &sdkquery.QueryError{StatusCode: 400, ErrorType: "UNKNOWN_DATA_OBJECT", Message: "unknown\nsecond line"})},
	}}
	r, err := Discover(context.Background(), runner, singleGo(t), prodHost)
	require.NoError(t, err)

	assert.Equal(t, VerdictNone, r.Verdict)
	assert.Empty(t, r.Candidates)
	assert.True(t, r.Partial, "an unreadable table leaves a gap")
	assert.Equal(t, causeAuth, r.Queries[2].Cause)
	assert.Equal(t, causeUnknownObject, r.Queries[3].Cause)
	assert.Equal(t, "query: query failed (UNKNOWN_DATA_OBJECT): unknown", r.Queries[3].Error, "first line only")
	assert.Len(t, r.Notes, 2, "one note for each query that failed")
}

func TestDiscover_UnknownObjectAloneIsNotPartial(t *testing.T) {
	unknown := answer{err: &sdkquery.QueryError{ErrorType: "UNKNOWN_DATA_OBJECT"}}
	r, err := Discover(context.Background(), &fakeRunner{answers: map[string]answer{
		"dt.entity.service": unknown, "dt.entity.process_group": unknown,
	}}, singleGo(t), prodHost)
	require.NoError(t, err)
	assert.False(t, r.Partial)
	assert.Equal(t, VerdictNone, r.Verdict)
}

func TestDiscover_TruncatedCapsTheVerdict(t *testing.T) {
	r, err := Discover(context.Background(), &fakeRunner{answers: map[string]answer{
		"spans": {records: rows(t, "spans-checkout"), truncation: inventory.TruncationScanLimit},
	}}, singleGo(t), prodHost)
	require.NoError(t, err)
	require.Len(t, r.Candidates, 1)
	assert.Equal(t, VerdictAmbiguous, r.Verdict)
	assert.Equal(t, VerdictAmbiguous, r.Verdicts["service"])
	assert.True(t, r.Partial)
	assert.True(t, r.Queries[0].Truncated)
	assert.Equal(t, causeTruncated, r.Queries[0].Cause)
	assert.Equal(t, inventory.TruncationScanLimit, r.Queries[0].truncation, "the note names the limit Grail reported")
	assert.Len(t, r.Notes, 1)
}

// Grail does not flag a page cut by the query's own limit stage, so a page
// that long is read as capped. Each row carries its own service id, so the
// one candidate they merge into is 200 services, not one.
func TestDiscover_FullPageCapsTheVerdict(t *testing.T) {
	page := func(n int) []map[string]interface{} {
		out := make([]map[string]interface{}, n)
		for i := range out {
			out[i] = map[string]interface{}{
				fieldServiceName: "checkout", fieldService: fmt.Sprintf("SERVICE-%016X", i), "records": float64(1),
			}
		}
		return out
	}
	r, err := Discover(context.Background(), &fakeRunner{answers: map[string]answer{
		"spans": {records: page(discoveryRowLimit)},
	}}, singleGo(t), prodHost)
	require.NoError(t, err)
	require.Len(t, r.Candidates, 1)
	assert.Len(t, r.Candidates[0].Services, discoveryRowLimit)
	assert.True(t, r.Partial)
	assert.True(t, r.Queries[0].Truncated)
	assert.Equal(t, causeRowLimit, r.Queries[0].Cause)
	assert.Equal(t, VerdictAmbiguous, r.Verdict)
	assert.Equal(t, VerdictAmbiguous, r.Verdicts["service"])
	assert.Len(t, r.Notes, 1)

	r, err = Discover(context.Background(), &fakeRunner{answers: map[string]answer{
		"spans": {records: page(2)},
	}}, singleGo(t), prodHost)
	require.NoError(t, err)
	assert.False(t, r.Partial, "a short page is the whole answer")
	assert.Equal(t, VerdictMatch, r.Verdicts["serviceName"])
	assert.Equal(t, VerdictAmbiguous, r.Verdicts["service"], "two service ids on one candidate are two services")
}

// A record query that failed leaves its data object unseen, so what the other
// one found cannot be a match.
func TestDiscover_FailedRecordQueryCapsTheVerdict(t *testing.T) {
	r, err := Discover(context.Background(), &fakeRunner{answers: map[string]answer{
		"spans": {err: &sdkquery.QueryError{ErrorType: "INTERNAL"}},
		"logs":  {records: rows(t, "logs-checkout")},
	}}, singleGo(t), prodHost)
	require.NoError(t, err)
	require.Len(t, r.Candidates, 1)
	assert.Equal(t, causeError, r.Queries[0].Cause)
	assert.True(t, r.Partial)
	assert.Equal(t, VerdictAmbiguous, r.Verdict)
}

// The CLI's runner reports a lapsed deadline as a cancellation; the cause is
// still a timeout.
func TestDiscover_Timeout(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	runner := &fakeRunner{
		answers: map[string]answer{"spans": {err: context.Canceled}},
		onRun: func(ctx context.Context, object string) {
			if object == "spans" {
				<-ctx.Done()
			}
		},
	}
	r, err := Discover(ctx, runner, singleGo(t), prodHost)
	require.ErrorIs(t, err, context.DeadlineExceeded)
	assert.Equal(t, causeTimeout, r.Queries[0].Cause)
	assert.True(t, r.Partial)
	assert.Len(t, r.Notes, 1, "every query the deadline stopped shares one note")
	for _, q := range r.Queries[1:] {
		assert.True(t, q.Skipped, q.Purpose)
	}
}

func TestDiscover_CancellationKeepsWhatWasFound(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	runner := &fakeRunner{
		answers: map[string]answer{"spans": {records: rows(t, "spans-checkout")}},
		onRun: func(_ context.Context, object string) {
			if object == "spans" {
				cancel() // Ctrl-C while the first query runs; its answer still arrives
			}
		},
	}
	p := singleGo(t)
	r, err := Discover(ctx, runner, p, prodHost)
	require.ErrorIs(t, err, context.Canceled)
	require.NotNil(t, r)
	assert.Len(t, runner.sent, 1)
	require.Len(t, r.Candidates, 1)
	assert.Equal(t, int64(12400), r.Candidates[0].Spans)
	for _, q := range r.Queries[1:] {
		assert.True(t, q.Skipped, q.Purpose)
	}
	assert.Equal(t, p.Sent, r.Sent)

	// The logs query never ran, so the one candidate the spans found is not
	// a match: the logs could have held a second one.
	assert.True(t, r.Partial)
	assert.Equal(t, VerdictAmbiguous, r.Verdict)
	assert.Equal(t, VerdictAmbiguous, r.Verdicts["service"])
	assert.Equal(t, causeCancelled, r.Queries[1].Cause)
	assert.Empty(t, r.Queries[2].Cause, "a fallback the records made unnecessary is no gap")
	assert.Len(t, r.Notes, 1)
}

// A deadline that lapses before the first query leaves nothing seen: the
// "none" that follows is partial, not evidence.
func TestDiscover_DeadlineBeforeTheFirstQuery(t *testing.T) {
	ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancel()
	runner := &fakeRunner{}
	r, err := Discover(ctx, runner, singleGo(t), prodHost)
	require.ErrorIs(t, err, context.DeadlineExceeded)
	assert.Empty(t, runner.sent)
	assert.Equal(t, VerdictNone, r.Verdict)
	assert.True(t, r.Partial)
	for _, q := range r.Queries {
		assert.True(t, q.Skipped, q.Purpose)
		assert.Equal(t, causeTimeout, q.Cause, q.Purpose)
	}
	assert.Len(t, r.Notes, 1, "every skipped query shares one note")
}

func TestDiscover_AmbiguousRows(t *testing.T) {
	r, err := Discover(context.Background(), &fakeRunner{answers: map[string]answer{
		"spans": {records: rows(t, "spans-two-services")},
		"logs":  {records: rows(t, "logs-service-name-only")},
	}}, singleGo(t), prodHost)
	require.NoError(t, err)

	// The logs row carries only service.name, which both spans rows share:
	// it is a candidate of its own.
	require.Len(t, r.Candidates, 3)
	assert.Equal(t, []string{svcID}, r.Candidates[0].Services, "more records rank first")
	assert.Equal(t, []string{"SERVICE-00000000000000AA"}, r.Candidates[1].Services)
	assert.Equal(t, int64(7), r.Candidates[2].Logs)
	assert.Equal(t, VerdictAmbiguous, r.Verdict)
	assert.Equal(t, VerdictAmbiguous, r.Verdicts["service"])
	assert.Equal(t, VerdictAmbiguous, r.Verdicts["workload"], "the same name in two namespaces")
	assert.Equal(t, VerdictMatch, r.Verdicts["serviceName"])
}

// Rows arrive sorted by record count, which changes with traffic. The result
// must not.
func TestDiscover_IndependentOfRowOrder(t *testing.T) {
	row := func(service, namespace string, records float64) map[string]interface{} {
		return map[string]interface{}{
			fieldService: service, fieldServiceName: "checkout", fieldNamespace: namespace,
			fieldWorkload: "checkout", "records": records,
		}
	}
	spans := []map[string]interface{}{
		{fieldServiceName: "checkout", "records": float64(5)},
		row("SERVICE-0000000000000001", "a", 5),
		row("SERVICE-0000000000000002", "b", 5),
		row("SERVICE-0000000000000003", "a", 1),
	}
	logs := []map[string]interface{}{
		{fieldProcessGroup: pgID, fieldServiceName: "checkout", fieldNamespace: "a", fieldWorkload: "checkout", "records": float64(3)},
	}
	discover := func(spans []map[string]interface{}) *DiscoveryReport {
		r, err := Discover(context.Background(), &fakeRunner{answers: map[string]answer{
			"spans": {records: spans}, "logs": {records: logs},
		}}, singleGo(t), prodHost)
		require.NoError(t, err)
		return r
	}
	want := discover(spans)
	require.Len(t, want.Candidates, 3, "one per (namespace, workload, service name)")
	assert.Equal(t, []string{"SERVICE-0000000000000001", "SERVICE-0000000000000003"}, want.Candidates[0].Services)
	assert.Equal(t, []string{pgID}, want.Candidates[0].ProcessGroups)

	rng := rand.New(rand.NewSource(1))
	for i := 0; i < 20; i++ {
		shuffled := append([]map[string]interface{}{}, spans...)
		rng.Shuffle(len(shuffled), func(a, b int) { shuffled[a], shuffled[b] = shuffled[b], shuffled[a] })
		assert.Equal(t, want, discover(shuffled))
	}
}

func TestDiscover_NoPlan(t *testing.T) {
	p, err := PlanDiscovery(fixture(t, "no-signals"), ".", nil)
	require.NoError(t, err)
	runner := &fakeRunner{}
	r, err := Discover(context.Background(), runner, p, prodHost)
	require.NoError(t, err)
	assert.Empty(t, runner.sent)
	assert.Equal(t, &DiscoveryReport{
		Environment: prodHost,
		Dir:         ".",
		Unit:        ".",
		Verdict:     VerdictNone,
		Verdicts:    map[string]Verdict{"service": VerdictNone, "processGroup": VerdictNone, "workload": VerdictNone, "serviceName": VerdictNone},
		Candidates:  []Candidate{},
		Queries:     []Query{},
		Sent:        []string{},
		Notes:       []string{`no usable name found for unit "."`},
	}, r)
}

func TestEntryName(t *testing.T) {
	assert.Equal(t, "checkout", entryName("Checkout"))
	assert.Equal(t, "payment-service", entryName("payment_service"))
	assert.Equal(t, "ledger-close", entryName("Ledger Close"))
	assert.Equal(t, "repo", entryName("___"))
	assert.Equal(t, strings.Repeat("a", 63), entryName(strings.Repeat("a", 70)))
	assert.Regexp(t, entryNameRe, entryName(strings.Repeat("a", 62)+"-b"))
}

// A log written by several processes carries its process groups as an
// array. Every valid id in it reaches the candidate, and the entry saved from
// it renders a filter that matches the array.
func TestDiscover_ProcessGroupArrays(t *testing.T) {
	const pg2 = "PROCESS_GROUP-0123456789ABCDEF"
	r, err := Discover(context.Background(), &fakeRunner{answers: map[string]answer{
		"logs": {records: []map[string]interface{}{
			{fieldServiceName: "checkout", fieldProcessGroup: []interface{}{pgID, pg2, "PROCESS_GROUP-1", nil}, "records": "10"},
			{fieldServiceName: "checkout", fieldProcessGroup: pgID, "records": "4"},
		}},
	}}, singleGo(t), prodHost)
	require.NoError(t, err)
	require.Len(t, r.Candidates, 1)
	c := r.Candidates[0]
	assert.Equal(t, []string{pg2, pgID}, c.ProcessGroups, "an id the scope file would refuse is dropped")
	assert.Equal(t, int64(14), c.Logs)
	assert.Equal(t, VerdictAmbiguous, r.Verdicts["processGroup"], "two process groups")

	f, ok, _ := Render(&Entry{Name: c.Name, ProcessGroups: c.ProcessGroups}, objectLogs)
	require.True(t, ok)
	assert.Equal(t, `in(dt.entity.process_group, array("PROCESS_GROUP-0123456789ABCDEF", "PROCESS_GROUP-FEDCBA9876543210"))`, f.Expr)
}

// Selecting prod's checkout must carry neither staging's nor the canary
// beside it: the candidate says its service name is shared, so the CLI can
// leave that binding out.
func TestDiscover_ServiceNameShared(t *testing.T) {
	record := func(namespace, workload string, records float64) map[string]interface{} {
		return map[string]interface{}{fieldServiceName: "checkout", fieldNamespace: namespace, fieldWorkload: workload, "records": records}
	}
	r, err := Discover(context.Background(), &fakeRunner{answers: map[string]answer{
		"spans": {records: []map[string]interface{}{record("prod", "checkout", 50), record("staging", "checkout", 5), record("prod", "checkout-canary", 2)}},
		"logs":  {records: []map[string]interface{}{{fieldServiceName: "ledger", fieldNamespace: "prod", fieldWorkload: "ledger", "records": float64(1)}}},
	}}, singleGo(t), prodHost)
	require.NoError(t, err)
	shared := map[string]bool{}
	for _, c := range r.Candidates {
		shared[c.Namespace+"/"+c.Workload] = c.ServiceNameShared
	}
	assert.Equal(t, map[string]bool{"prod/checkout": true, "staging/checkout": true, "prod/checkout-canary": true, "prod/ledger": false}, shared)
}

// With one build file at the top, the directory discovery ran for is tied to
// a candidate only by its own name; a candidate the build file matched is the
// whole repository's.
func TestDiscover_MatchedDir(t *testing.T) {
	fsys := fstest.MapFS{
		"go.mod":                   {Data: []byte("module example.invalid/platform\n")},
		"services/ledger/main.go":  {Data: []byte("package main\n")},
		"services/billing/go.mod":  {Data: []byte("module example.invalid/billing\n")},
		"services/billing/main.go": {Data: []byte("package main\n")},
	}
	spans := answer{records: []map[string]interface{}{
		{fieldNamespace: "prod", fieldWorkload: "ledger", "records": float64(10)},
		{fieldNamespace: "prod", fieldWorkload: "platform", "records": float64(10)},
		{fieldNamespace: "prod", fieldWorkload: "billing", "records": float64(10)},
	}}
	tests := []struct {
		name, dir string
		want      map[string]bool
	}{
		{"a directory below a root unit", "services/ledger", map[string]bool{"ledger": true, "platform": false, "billing": false}},
		{"the root itself", ".", map[string]bool{"ledger": false, "platform": false, "billing": false}},
		{"a unit of its own", "services/billing", map[string]bool{"ledger": false, "platform": false, "billing": false}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			p, err := PlanDiscovery(fsys, tc.dir, nil)
			require.NoError(t, err)
			r, err := Discover(context.Background(), &fakeRunner{answers: map[string]answer{"spans": spans}}, p, prodHost)
			require.NoError(t, err)
			got := map[string]bool{}
			for _, c := range r.Candidates {
				got[c.Workload] = c.MatchedDir
			}
			assert.Equal(t, tc.want, got)
		})
	}
}

// One manifest naming a service on three lines is one file. Two build files
// naming another are two, and rank it first.
func TestDiscover_RanksFilesNotLines(t *testing.T) {
	fsys := fstest.MapFS{
		"go.mod":       {Data: []byte("module example.invalid/ledger\n")},
		"package.json": {Data: []byte(`{"name":"ledger"}`)},
		"deploy.yaml": {Data: []byte("apiVersion: apps/v1\nkind: Deployment\nmetadata:\n  name: checkout\nspec:\n  template:\n    spec:\n" +
			"      containers:\n      - name: app\n        image: checkout\n        env:\n        - name: OTEL_SERVICE_NAME\n          value: checkout\n")},
	}
	p, err := PlanDiscovery(fsys, ".", nil)
	require.NoError(t, err)
	r, err := Discover(context.Background(), &fakeRunner{answers: map[string]answer{
		"spans": {records: []map[string]interface{}{
			{fieldServiceName: "ledger", "records": float64(1)},
			{fieldServiceName: "checkout", "records": float64(100)},
		}},
	}}, p, prodHost)
	require.NoError(t, err)
	require.Len(t, r.Candidates, 2)
	assert.Equal(t, "ledger", r.Candidates[0].ServiceName)
	assert.Equal(t, `service.name "checkout" from deploy.yaml:4, deploy.yaml:10, deploy.yaml:13`, r.Candidates[1].Evidence[0],
		"evidence still cites every line")
}
