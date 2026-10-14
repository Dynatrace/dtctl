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
// object the query fetches, and records every DQL text it was sent. Each
// object's answers are consumed in order, so a record query's first answer
// is its own and the next ones its sampled retries'; once they run out, a
// query gets an empty answer Grail did not sample.
type fakeRunner struct {
	answers map[string][]answer
	sent    []string
	onRun   func(ctx context.Context, object string) // runs before answering
}

type answer struct {
	records      []map[string]interface{}
	truncation   inventory.TruncationCause
	sampled      bool
	scannedBytes int64
	err          error
}

func (r *fakeRunner) RunQuery(ctx context.Context, dql string) (*inventory.RunResult, error) {
	r.sent = append(r.sent, dql)
	object := strings.TrimSuffix(strings.Fields(strings.TrimPrefix(dql, "fetch "))[0], ",")
	if r.onRun != nil {
		r.onRun(ctx, object)
	}
	var a answer
	if queue := r.answers[object]; len(queue) > 0 {
		a, r.answers[object] = queue[0], queue[1:]
	}
	if a.err != nil {
		return nil, a.err
	}
	return &inventory.RunResult{Records: a.records, Truncated: a.truncation != "", TruncationCause: a.truncation,
		Sampled: a.sampled, ScannedBytes: a.scannedBytes}, nil
}

// capped is a record query's answer when the scan cap stopped it.
func capped(records []map[string]interface{}) answer {
	return answer{records: records, truncation: inventory.TruncationScanLimit}
}

// sampledAnswer is a rung's answer: Grail sampled it and read scanned bytes.
func sampledAnswer(records []map[string]interface{}, scanned int64) answer {
	return answer{records: records, sampled: true, scannedBytes: scanned}
}

// collected is an id column as a record query collects it.
func collected(ids ...string) []interface{} {
	out := make([]interface{}, len(ids))
	for i, id := range ids {
		out[i] = id
	}
	return out
}

// serviceNames is one row per name, as many distinct identities return.
func serviceNames(n int) []map[string]interface{} {
	out := make([]map[string]interface{}, n)
	for i := range out {
		out[i] = map[string]interface{}{fieldServiceName: fmt.Sprintf("checkout-%03d", i), fieldRecords: float64(1)}
	}
	return out
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
	require.Len(t, p.Queries, 10)
	assert.Equal(t, ".", p.Unit)
	assert.Equal(t, []string{"checkout", "checkout-service", "checkout.service", "checkout_service", "checkoutservice"}, p.Sent)

	list := `"checkout", "checkout-service", "checkout.service", "checkout_service", "checkoutservice"`
	ids := "    serviceCount = countDistinct(dt.entity.service), services = collectDistinct(dt.entity.service, maxLength:50),\n" +
		"    processGroupCount = countDistinct(dt.entity.process_group), processGroups = collectDistinct(dt.entity.process_group, maxLength:50),\n" +
		"    by:{k8s.namespace.name, k8s.workload.name, service.name}\n"
	assert.Equal(t, Query{
		Purpose:    "spans by workload or service name",
		DataObject: "spans",
		DQL: "fetch spans, from:now()-24h, scanLimitGBytes:5\n" +
			"| filter in(k8s.workload.name, array(" + list + ")) or in(service.name, array(" + list + "))\n" +
			"| summarize records = count(),\n" + ids +
			"| sort records desc\n" +
			"| limit 200",
	}, p.Queries[0])
	assert.Equal(t, Query{
		Purpose:       "spans by workload or service name (sampled 1-in-100000 over 1h)",
		DataObject:    "spans",
		Window:        "1h",
		SamplingRatio: 100000,
		DQL: "fetch spans, from:now()-1h, scanLimitGBytes:5, samplingRatio:100000\n" +
			"| filter in(k8s.workload.name, array(" + list + ")) or in(service.name, array(" + list + "))\n" +
			"| summarize records = count(),\n" + ids +
			"| sort records desc\n" +
			"| limit 200",
	}, p.Queries[1])
	assert.NotContains(t, p.Queries[0].DQL, "by:{k8s.namespace.name, k8s.workload.name, service.name, dt.entity",
		"an id in by: would make one row per id")
	assert.Equal(t, "logs", p.Queries[4].DataObject)
	assert.Equal(t, strings.Replace(p.Queries[0].DQL, "fetch spans", "fetch logs", 1), p.Queries[4].DQL)
	assert.Equal(t, Query{
		Purpose:    "services by entity name",
		DataObject: "dt.entity.service",
		DQL: "fetch dt.entity.service, from:now()-24h\n" +
			`| filter matchesValue(entity.name, "*checkout*") or matchesValue(entity.name, "*checkout-service*") or matchesValue(entity.name, "*checkout.service*") or matchesValue(entity.name, "*checkout_service*") or matchesValue(entity.name, "*checkoutservice*")` + "\n" +
			"| fields id, entity.name\n" +
			"| fieldsAdd nameLength = stringLength(entity.name)\n" +
			"| sort nameLength asc\n" +
			"| limit 200",
	}, p.Queries[8])
	assert.Equal(t, "dt.entity.process_group", p.Queries[9].DataObject)
}

// Each record query is followed by its sampled retries, coarsest first. They
// are in the plan, so a dry run shows them, but a run sends them only when
// the scan cap stops the record query.
func TestPlan_IncludesSamplingRungs(t *testing.T) {
	p := singleGo(t)
	var got []string
	for _, q := range p.Queries {
		got = append(got, fmt.Sprintf("%s %s %d", q.DataObject, q.Window, q.SamplingRatio))
	}
	assert.Equal(t, []string{
		"spans  0", "spans 1h 100000", "spans 24h 100000", "spans 24h 1000",
		"logs  0", "logs 1h 100000", "logs 24h 100000", "logs 24h 1000",
		"dt.entity.service  0", "dt.entity.process_group  0",
	}, got)
	for i, first := range []string{
		"fetch spans, from:now()-1h, scanLimitGBytes:5, samplingRatio:100000",
		"fetch spans, from:now()-24h, scanLimitGBytes:5, samplingRatio:100000",
		"fetch spans, from:now()-24h, scanLimitGBytes:5, samplingRatio:1000",
	} {
		q := p.Queries[i+1]
		assert.True(t, strings.HasPrefix(q.DQL, first+"\n"), q.DQL)
		_, rest, _ := strings.Cut(q.DQL, "\n")
		_, unsampled, _ := strings.Cut(p.Queries[0].DQL, "\n")
		assert.Equal(t, unsampled, rest, "a rung differs from its record query only in what it reads")
	}

	runner := &fakeRunner{answers: map[string][]answer{
		"spans": {{records: rows(t, "spans-checkout")}},
		"logs":  {{records: rows(t, "logs-checkout")}},
	}}
	r, err := Discover(context.Background(), runner, p, prodHost)
	require.NoError(t, err)
	assert.Len(t, runner.sent, 2, "neither record query was capped")
	for _, i := range []int{1, 2, 3, 5, 6, 7} {
		assert.True(t, r.Queries[i].Skipped, r.Queries[i].Purpose)
		assert.Empty(t, r.Queries[i].Cause, "a retry nothing needed is no gap")
	}
	assert.False(t, r.Partial)
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
	require.Len(t, p.Queries, 10)
	assert.Equal(t, []string{"BillingEngine", "billing-engine", "billing.engine", "billing_engine", "billingengine"}, p.Sent,
		"a term is sent as given, lowercased, and in its variants")
}

// A name that looks like a test fixture is held back, but a --term never
// passes through that rule: a user who means it can send it.
func TestPlan_TermsBypassTestNames(t *testing.T) {
	p, err := PlanDiscovery(fixture(t, "test-noise"), ".", nil)
	require.NoError(t, err)
	assert.NotContains(t, p.Sent, "test-platform-service-smoke")
	assert.Contains(t, p.Dropped, "test-platform-service-smoke")

	p, err = PlanDiscovery(fixture(t, "test-noise"), ".", []string{"test-platform-service-smoke"})
	require.NoError(t, err)
	assert.Contains(t, p.Sent, "test-platform-service-smoke")
	assert.NotContains(t, p.Dropped, "test-platform-service-smoke", "a name a term sends is not reported as held back")
}

// From a Gradle library two deployables share, discovery names them and
// sends nothing itself; from a library one of them depends on, it plans for
// that deployable.
func TestPlan_GradleDeployables(t *testing.T) {
	p, err := PlanDiscovery(fixture(t, "gradle-multi"), "webproxy-core", nil)
	require.NoError(t, err)
	assert.Equal(t, ".", p.Unit)
	assert.Equal(t, []string{"admin", "webproxy"}, p.SharedBy)
	assert.Empty(t, p.Sent)

	p, err = PlanDiscovery(fixture(t, "gradle-multi"), "webproxy-api", nil)
	require.NoError(t, err)
	assert.Equal(t, "webproxy", p.Unit)
	assert.Empty(t, p.SharedBy)
}

// The names held back are named, so the drop is never silent.
func TestDiscover_NotesDroppedNames(t *testing.T) {
	p := singleGo(t)
	p.Dropped = []string{"mock-ledger", "test-platform-service-a"}
	r, err := Discover(context.Background(), &fakeRunner{}, p, prodHost)
	require.NoError(t, err)
	assert.Contains(t, r.Notes, "names that look like test fixtures are not sent: mock-ledger, test-platform-service-a; use --term to send one")
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
	for _, q := range p.Queries {
		if !q.fallback() {
			assert.Contains(t, q.DQL, `"BillingEngine"`, q.Purpose)
		}
	}

	r, err := Discover(context.Background(), &fakeRunner{answers: map[string][]answer{
		"spans": {{records: []map[string]interface{}{{fieldServiceName: "BillingEngine", fieldRecords: float64(3)}}}},
	}}, p, prodHost)
	require.NoError(t, err)
	require.Len(t, r.Candidates, 1)
	assert.Equal(t, `service.name "BillingEngine" from go.mod:1`, r.Candidates[0].Evidence[0])
}

func TestDiscover_Match(t *testing.T) {
	p := singleGo(t)
	runner := &fakeRunner{answers: map[string][]answer{
		"spans": {{records: rows(t, "spans-checkout")}},
		"logs":  {{records: rows(t, "logs-checkout")}},
	}}
	r, err := Discover(context.Background(), runner, p, prodHost)
	require.NoError(t, err)

	assert.Len(t, runner.sent, 2, "the entity-table fallbacks are not needed")
	assert.True(t, r.Queries[8].Skipped)
	assert.True(t, r.Queries[9].Skipped)
	assert.Equal(t, VerdictMatch, r.Verdict)
	assert.Equal(t, map[string]Verdict{
		"service": VerdictMatch, "processGroup": VerdictMatch, "workload": VerdictMatch, "serviceName": VerdictMatch,
	}, r.Verdicts)
	assert.False(t, r.Partial)
	assert.Equal(t, p.Sent, r.Sent)

	require.Len(t, r.Candidates, 1, "the logs row joins the spans row of the same workload")
	assert.Equal(t, Candidate{
		Rank:              1,
		Name:              "checkout",
		Services:          []string{svcID},
		ServiceCount:      1,
		ProcessGroups:     []string{pgID},
		ProcessGroupCount: 1,
		ServiceName:       "checkout",
		Namespace:         "payments",
		Workload:          "checkout",
		Tier:              "exact",
		Spans:             12400,
		Logs:              88000,
		Evidence: []string{
			`k8s.workload.name "checkout" from Dockerfile:4, deploy/k8s/deployment.yaml:4, deploy/k8s/deployment.yaml:12, deploy/k8s/deployment.yaml:15, go.mod:1`,
			`service.name "checkout" from Dockerfile:4, deploy/k8s/deployment.yaml:4, deploy/k8s/deployment.yaml:12, deploy/k8s/deployment.yaml:15, go.mod:1`,
			`k8s.namespace.name "payments" from deploy/k8s/deployment.yaml:5`,
			"12400 spans, 88000 logs in 24h",
		},
	}, r.Candidates[0])
}

func TestDiscover_FallbackTiers(t *testing.T) {
	runner := &fakeRunner{answers: map[string][]answer{
		"dt.entity.service": {{records: rows(t, "services-by-name")}},
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
	runner := &fakeRunner{answers: map[string][]answer{
		"dt.entity.service":       {{err: &sdkquery.QueryError{StatusCode: 403, ErrorType: "NOT_AUTHORIZED_FOR_TABLE", Message: "not authorized"}}},
		"dt.entity.process_group": {{err: fmt.Errorf("query: %w", &sdkquery.QueryError{StatusCode: 400, ErrorType: "UNKNOWN_DATA_OBJECT", Message: "unknown\nsecond line"})}},
	}}
	r, err := Discover(context.Background(), runner, singleGo(t), prodHost)
	require.NoError(t, err)

	assert.Equal(t, VerdictNone, r.Verdict)
	assert.Empty(t, r.Candidates)
	assert.True(t, r.Partial, "an unreadable table leaves a gap")
	assert.Equal(t, causeAuth, r.Queries[8].Cause)
	assert.Equal(t, causeUnknownObject, r.Queries[9].Cause)
	assert.Equal(t, "query: query failed (UNKNOWN_DATA_OBJECT): unknown", r.Queries[9].Error, "first line only")
	assert.Len(t, r.Notes, 2, "one note for each query that failed")
}

func TestDiscover_UnknownObjectAloneIsNotPartial(t *testing.T) {
	unknown := answer{err: &sdkquery.QueryError{ErrorType: "UNKNOWN_DATA_OBJECT"}}
	r, err := Discover(context.Background(), &fakeRunner{answers: map[string][]answer{
		"dt.entity.service": {unknown}, "dt.entity.process_group": {unknown},
	}}, singleGo(t), prodHost)
	require.NoError(t, err)
	assert.False(t, r.Partial)
	assert.Equal(t, VerdictNone, r.Verdict)
}

// A cap other than the scan cap is not a volume problem sampling solves: the
// capped answer stands, and no retry is sent.
func TestDiscover_TruncatedCapsTheVerdict(t *testing.T) {
	runner := &fakeRunner{answers: map[string][]answer{
		"spans": {{records: rows(t, "spans-checkout"), truncation: inventory.TruncationTimeout}},
	}}
	r, err := Discover(context.Background(), runner, singleGo(t), prodHost)
	require.NoError(t, err)
	assert.Len(t, runner.sent, 2)
	require.Len(t, r.Candidates, 1)
	assert.Equal(t, VerdictAmbiguous, r.Verdict)
	assert.Equal(t, VerdictAmbiguous, r.Verdicts["service"])
	assert.True(t, r.Partial)
	assert.True(t, r.Queries[0].Truncated)
	assert.Equal(t, causeTruncated, r.Queries[0].Cause)
	assert.Equal(t, inventory.TruncationTimeout, r.Queries[0].truncation, "the note names the limit Grail reported")
	assert.Len(t, r.Notes, 1)
}

// Grail does not flag a page cut by the query's own limit stage, so a page
// that long is read as capped. Rows are identities, so a full page is 200 of
// them.
func TestDiscover_FullPageCapsTheVerdict(t *testing.T) {
	r, err := Discover(context.Background(), &fakeRunner{answers: map[string][]answer{
		"spans": {{records: serviceNames(discoveryRowLimit)}},
	}}, singleGo(t), prodHost)
	require.NoError(t, err)
	assert.Len(t, r.Candidates, discoveryRowLimit)
	assert.True(t, r.Partial)
	assert.True(t, r.Queries[0].Truncated)
	assert.Equal(t, causeRowLimit, r.Queries[0].Cause)
	assert.Equal(t, VerdictAmbiguous, r.Verdict)
	assert.Len(t, r.Notes, 1)

	r, err = Discover(context.Background(), &fakeRunner{answers: map[string][]answer{
		"spans": {{records: []map[string]interface{}{
			{fieldServiceName: "checkout", fieldServiceCount: "1", fieldServices: collected("SERVICE-0000000000000001"), fieldRecords: float64(1)},
			{fieldServiceName: "checkout", fieldServiceCount: "1", fieldServices: collected("SERVICE-0000000000000002"), fieldRecords: float64(1)},
		}}},
	}}, singleGo(t), prodHost)
	require.NoError(t, err)
	assert.False(t, r.Partial, "a short page is the whole answer")
	require.Len(t, r.Candidates, 1)
	assert.Equal(t, int64(2), r.Candidates[0].ServiceCount, "the rows of one identity merge their ids")
	assert.Equal(t, VerdictMatch, r.Verdicts["serviceName"])
	assert.Equal(t, VerdictAmbiguous, r.Verdicts["service"], "two service ids on one candidate are two services")
}

// One workload can run as hundreds of service ids. It is one row, so one
// candidate, and the count says how many ids the collected list leaves out.
func TestDiscover_ManyIDsAreOneCandidate(t *testing.T) {
	r, err := Discover(context.Background(), &fakeRunner{answers: map[string][]answer{
		"spans": {{records: rows(t, "many-ids")}},
	}}, singleGo(t), prodHost)
	require.NoError(t, err)
	require.Len(t, r.Candidates, 1)
	c := r.Candidates[0]
	assert.False(t, r.Partial)
	assert.Len(t, c.Services, discoveryIDLimit)
	assert.Equal(t, int64(200), c.ServiceCount)
	assert.True(t, c.ServicesCapped)
	assert.False(t, c.ProcessGroupsCapped)
	assert.Equal(t, VerdictAmbiguous, r.Verdicts["service"], "200 services, whatever the list shows")
	assert.Equal(t, VerdictMatch, r.Verdicts["workload"])

	// The count, not the list, is the tally: one id listed of two is two.
	r, err = Discover(context.Background(), &fakeRunner{answers: map[string][]answer{
		"spans": {{records: []map[string]interface{}{
			{fieldServiceName: "checkout", fieldServiceCount: "2", fieldServices: collected(svcID), fieldRecords: float64(1)},
		}}},
	}}, singleGo(t), prodHost)
	require.NoError(t, err)
	assert.Equal(t, VerdictAmbiguous, r.Verdicts["service"])
}

// Grail may report a collected array cut at maxLength with a "limited to"
// message, which the runner maps to result_limit. A page shorter than the
// query's own limit lost no row, so that is not a cut page.
func TestDiscover_CollectedArrayCapIsNotACutPage(t *testing.T) {
	r, err := Discover(context.Background(), &fakeRunner{answers: map[string][]answer{
		"spans": {{records: serviceNames(3), truncation: inventory.TruncationResultLimit}},
	}}, singleGo(t), prodHost)
	require.NoError(t, err)
	assert.False(t, r.Partial)
	assert.False(t, r.Queries[0].Truncated)
	assert.Empty(t, r.Notes)

	r, err = Discover(context.Background(), &fakeRunner{answers: map[string][]answer{
		"spans": {{records: serviceNames(discoveryRowLimit), truncation: inventory.TruncationResultLimit}},
	}}, singleGo(t), prodHost)
	require.NoError(t, err)
	assert.True(t, r.Partial)
	assert.Equal(t, causeRowLimit, r.Queries[0].Cause, "a full page is still judged by its length")
}

// A record query that failed leaves its data object unseen, so what the other
// one found cannot be a match.
func TestDiscover_FailedRecordQueryCapsTheVerdict(t *testing.T) {
	r, err := Discover(context.Background(), &fakeRunner{answers: map[string][]answer{
		"spans": {{err: &sdkquery.QueryError{ErrorType: "INTERNAL"}}},
		"logs":  {{records: rows(t, "logs-checkout")}},
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
		answers: map[string][]answer{"spans": {{err: context.Canceled}}},
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
		answers: map[string][]answer{"spans": {{records: rows(t, "spans-checkout")}}},
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
	assert.Empty(t, r.Queries[1].Cause, "the spans query completed, so its retries were not needed")
	for _, q := range r.Queries[4:8] {
		assert.Equal(t, causeCancelled, q.Cause, q.Purpose)
	}
	assert.Empty(t, r.Queries[8].Cause, "a fallback the records made unnecessary is no gap")
	assert.Len(t, r.Notes, 1)
}

// Ctrl-C while a sampled retry runs: its answer still arrives, and the rungs
// it kept from running are a gap like any query the cancellation stopped.
func TestDiscover_CancellationMidLadder(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	calls := 0
	runner := &fakeRunner{
		answers: map[string][]answer{"spans": {capped(nil), {sampled: true}}},
		onRun: func(context.Context, string) {
			if calls++; calls == 2 {
				cancel() // while the first rung runs
			}
		},
	}
	r, err := Discover(ctx, runner, singleGo(t), prodHost)
	require.ErrorIs(t, err, context.Canceled)
	assert.Len(t, runner.sent, 2)
	assert.Equal(t, 0, r.Queries[1].Rows)
	assert.Empty(t, r.Queries[1].Cause, "the first rung completed")
	for _, q := range r.Queries[2:8] {
		assert.True(t, q.Skipped, q.Purpose)
		assert.Equal(t, causeCancelled, q.Cause, q.Purpose)
	}
	assert.True(t, r.Partial)
	assert.Equal(t, VerdictNone, r.Verdict)
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
	r, err := Discover(context.Background(), &fakeRunner{answers: map[string][]answer{
		"spans": {{records: rows(t, "spans-two-services")}},
		"logs":  {{records: rows(t, "logs-service-name-only")}},
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
	row := func(namespace string, records float64, services ...string) map[string]interface{} {
		return map[string]interface{}{
			fieldServices: collected(services...), fieldServiceCount: float64(len(services)), fieldServiceName: "checkout",
			fieldNamespace: namespace, fieldWorkload: "checkout", fieldRecords: records,
		}
	}
	spans := []map[string]interface{}{
		{fieldServiceName: "checkout", fieldRecords: float64(5)},
		row("a", 5, "SERVICE-0000000000000003", "SERVICE-0000000000000001"),
		row("b", 5, "SERVICE-0000000000000002"),
	}
	logs := []map[string]interface{}{
		{fieldProcessGroups: collected(pgID), fieldProcessGroupCount: "1", fieldServiceName: "checkout", fieldNamespace: "a", fieldWorkload: "checkout", fieldRecords: float64(3)},
	}
	discover := func(spans []map[string]interface{}) *DiscoveryReport {
		r, err := Discover(context.Background(), &fakeRunner{answers: map[string][]answer{
			"spans": {{records: spans}}, "logs": {{records: logs}},
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
// array, so collecting them yields arrays of arrays. Every valid id in them
// reaches the candidate, and the entry saved from it renders a filter that
// matches the array.
func TestDiscover_ProcessGroupArrays(t *testing.T) {
	const pg2 = "PROCESS_GROUP-0123456789ABCDEF"
	r, err := Discover(context.Background(), &fakeRunner{answers: map[string][]answer{
		"logs": {{records: []map[string]interface{}{
			{fieldServiceName: "checkout", fieldProcessGroupCount: "1",
				fieldProcessGroups: []interface{}{[]interface{}{pgID, pg2, "PROCESS_GROUP-1", nil}}, fieldRecords: "10"},
			{fieldServiceName: "checkout", fieldProcessGroupCount: "1", fieldProcessGroups: collected(pgID), fieldRecords: "4"},
		}}},
	}}, singleGo(t), prodHost)
	require.NoError(t, err)
	require.Len(t, r.Candidates, 1)
	c := r.Candidates[0]
	assert.Equal(t, []string{pg2, pgID}, c.ProcessGroups, "an id the scope file would refuse is dropped")
	assert.Equal(t, int64(2), c.ProcessGroupCount, "never fewer than the ids listed")
	assert.False(t, c.ProcessGroupsCapped, "one distinct array is not a cut list")
	assert.Equal(t, int64(14), c.Logs)
	assert.Equal(t, VerdictAmbiguous, r.Verdicts["processGroup"], "two process groups")

	f, ok, _ := Render(&Entry{Name: c.Name, ProcessGroups: c.ProcessGroups}, objectLogs)
	require.True(t, ok)
	assert.Equal(t, `in(dt.entity.process_group, array("PROCESS_GROUP-0123456789ABCDEF", "PROCESS_GROUP-FEDCBA9876543210"))`, f.Expr)
}

// For an array field Grail counts and collects distinct arrays. 60 arrays
// counted against 50 collected is a cut list, however many ids the 50
// flatten to.
func TestDiscover_ProcessGroupArraysCut(t *testing.T) {
	arrays, next := make([]interface{}, 50), 0
	for i := range arrays {
		size := 2
		if i < 20 {
			size = 3
		}
		ids := make([]interface{}, size)
		for j := range ids {
			ids[j], next = fmt.Sprintf("PROCESS_GROUP-%016X", next), next+1
		}
		arrays[i] = ids
	}
	r, err := Discover(context.Background(), &fakeRunner{answers: map[string][]answer{
		"logs": {{records: []map[string]interface{}{
			{fieldServiceName: "checkout", fieldProcessGroupCount: "60", fieldProcessGroups: arrays, fieldRecords: "10"},
		}}},
	}}, singleGo(t), prodHost)
	require.NoError(t, err)
	require.Len(t, r.Candidates, 1)
	c := r.Candidates[0]
	assert.Len(t, c.ProcessGroups, 120)
	assert.True(t, c.ProcessGroupsCapped, "10 arrays were cut, and their ids with them")
}

// Selecting prod's checkout must carry neither staging's nor the canary
// beside it: the candidate says its service name is shared, so the CLI can
// leave that binding out.
func TestDiscover_ServiceNameShared(t *testing.T) {
	record := func(namespace, workload string, records float64) map[string]interface{} {
		return map[string]interface{}{fieldServiceName: "checkout", fieldNamespace: namespace, fieldWorkload: workload, fieldRecords: records}
	}
	r, err := Discover(context.Background(), &fakeRunner{answers: map[string][]answer{
		"spans": {{records: []map[string]interface{}{record("prod", "checkout", 50), record("staging", "checkout", 5), record("prod", "checkout-canary", 2)}}},
		"logs":  {{records: []map[string]interface{}{{fieldServiceName: "ledger", fieldNamespace: "prod", fieldWorkload: "ledger", fieldRecords: float64(1)}}}},
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
	spans := []map[string]interface{}{
		{fieldNamespace: "prod", fieldWorkload: "ledger", fieldRecords: float64(10)},
		{fieldNamespace: "prod", fieldWorkload: "platform", fieldRecords: float64(10)},
		{fieldNamespace: "prod", fieldWorkload: "billing", fieldRecords: float64(10)},
	}
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
			r, err := Discover(context.Background(), &fakeRunner{answers: map[string][]answer{"spans": {{records: spans}}}}, p, prodHost)
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
	r, err := Discover(context.Background(), &fakeRunner{answers: map[string][]answer{
		"spans": {{records: []map[string]interface{}{
			{fieldServiceName: "ledger", fieldRecords: float64(1)},
			{fieldServiceName: "checkout", fieldRecords: float64(100)},
		}}},
	}}, p, prodHost)
	require.NoError(t, err)
	require.Len(t, r.Candidates, 2)
	assert.Equal(t, "ledger", r.Candidates[0].ServiceName)
	assert.Equal(t, `service.name "checkout" from deploy.yaml:4, deploy.yaml:10, deploy.yaml:13`, r.Candidates[1].Evidence[0],
		"evidence still cites every line")
}

// On a busy environment a day of spans scans past the cap. The first rung
// answers, so it is the only retry sent, and its candidate is a match at the
// floor its note states.
func TestDiscover_ScanCapFallsBackToSampling(t *testing.T) {
	runner := &fakeRunner{answers: map[string][]answer{
		"spans": {capped(rows(t, "spans-checkout")), sampledAnswer(rows(t, "spans-checkout-sampled"), 100_000_000)},
		"logs":  {{records: rows(t, "logs-checkout")}},
	}}
	r, err := Discover(context.Background(), runner, singleGo(t), prodHost)
	require.NoError(t, err)

	assert.Len(t, runner.sent, 3, "the first rung that completes ends the walk")
	assert.Equal(t, VerdictMatch, r.Verdict)
	assert.False(t, r.Partial)
	assert.True(t, r.Queries[0].Truncated)
	assert.Equal(t, causeTruncated, r.Queries[0].Cause)
	assert.Equal(t, int64(100000), r.Queries[1].SamplingRatio)
	assert.Equal(t, "1h", r.Queries[1].Window)
	assert.Equal(t, 1, r.Queries[1].Rows)
	for _, q := range r.Queries[2:4] {
		assert.True(t, q.Skipped, q.Purpose)
		assert.Empty(t, q.Cause, q.Purpose)
	}
	assert.Equal(t, []string{
		"spans scan more than the 5 GB cap over 24h, so they were sampled 1-in-100000 over 1h; a service with fewer than about 300000 spans over 1h may be missing",
	}, r.Notes, "the capped query's own note is not needed: the retry answered")

	require.Len(t, r.Candidates, 1)
	c := r.Candidates[0]
	assert.True(t, c.Sampled)
	assert.Equal(t, int64(17), c.SpansSampled, "the sample's hits, not the capped scan's count")
	assert.Zero(t, c.Spans, "no population is estimated from a sample")
	assert.Equal(t, int64(88000), c.Logs)
	assert.Equal(t, "17 sampled span hits in 1h (1 in 100000 requested), 88000 logs in 24h", c.Evidence[len(c.Evidence)-1])
	assert.Equal(t, r.Notes, r.samplingNotes())
}

// Before each rung is sent, the last scan is scaled by how much more the rung
// reads. An hour at 1-in-100000 that read 1 GB predicts 24 GB for the day,
// and the day at 1-in-1000 more still, so neither is sent.
func TestDiscover_SamplingSkipsAPredictedCap(t *testing.T) {
	runner := &fakeRunner{answers: map[string][]answer{
		"spans": {capped(nil), sampledAnswer(nil, 1_000_000_000)},
	}}
	r, err := Discover(context.Background(), runner, singleGo(t), prodHost)
	require.NoError(t, err)

	assert.Equal(t, 2, strings.Count(strings.Join(runner.sent, "\n"), "fetch spans"))
	for _, q := range r.Queries[2:4] {
		assert.True(t, q.Skipped, q.Purpose)
		assert.Equal(t, causePredictedCap, q.Cause, q.Purpose)
	}
	assert.True(t, r.Partial, "a sampled miss is a bound, not an absence")
	assert.Contains(t, r.Notes,
		"spans scan more than the 5 GB cap over 24h, so they were sampled 1-in-100000 over 1h and matched nothing; a service with fewer than about 300000 spans over 1h may be missing, so the result is partial")
}

// Every rung that completes empty passes the walk on to the next. The bound
// the report states is the finest rung's, and the result stays partial.
func TestDiscover_EmptySampledAnswerIsPartial(t *testing.T) {
	runner := &fakeRunner{answers: map[string][]answer{
		"spans": {capped(nil), sampledAnswer(nil, 1_000_000), sampledAnswer(nil, 10_000_000), sampledAnswer(nil, 0)},
	}}
	r, err := Discover(context.Background(), runner, singleGo(t), prodHost)
	require.NoError(t, err)

	assert.Len(t, runner.sent, 7, "every rung ran, then the logs query, then both fallbacks")
	assert.True(t, r.Partial)
	assert.Equal(t, VerdictNone, r.Verdict)
	assert.False(t, r.Queries[8].Skipped, "the records found nothing, so the fallbacks ran")
	assert.Equal(t, []string{
		"spans scan more than the 5 GB cap over 24h, so they were sampled 1-in-1000 over 24h and matched nothing; a service with fewer than about 3000 spans over 24h may be missing, so the result is partial",
	}, r.samplingNotes())
}

// A rung's collected id array cut at maxLength is no more a cut page than the
// record query's own: its rows are the answer.
func TestDiscover_SampledRungCollectedArrayCapIsNotACutPage(t *testing.T) {
	rung := sampledAnswer(rows(t, "spans-checkout-sampled"), 100_000_000)
	rung.truncation = inventory.TruncationResultLimit
	runner := &fakeRunner{answers: map[string][]answer{
		"spans": {capped(rows(t, "spans-checkout")), rung},
		"logs":  {{records: rows(t, "logs-checkout")}},
	}}
	r, err := Discover(context.Background(), runner, singleGo(t), prodHost)
	require.NoError(t, err)

	assert.Len(t, runner.sent, 3, "the rung answered, so the walk ended there")
	assert.False(t, r.Queries[1].Truncated)
	assert.Empty(t, r.Queries[1].Cause)
	assert.Equal(t, VerdictMatch, r.Verdict)
	assert.False(t, r.Partial)
	require.Len(t, r.Candidates, 1)
	assert.True(t, r.Candidates[0].Sampled)
	assert.Equal(t, int64(17), r.Candidates[0].SpansSampled, "the sample's rows, not the capped scan's")
}

// A rung that reports no scan size keeps the prediction scaled from the last
// one that did: 40 MB an hour is 960 MB a day, and a hundred times that at
// 1-in-1000 is over the cap whatever the second rung failed to report.
func TestDiscover_UnknownScanKeepsThePrediction(t *testing.T) {
	runner := &fakeRunner{answers: map[string][]answer{
		"spans": {capped(nil), sampledAnswer(nil, 40_000_000), sampledAnswer(nil, 0)},
	}}
	r, err := Discover(context.Background(), runner, singleGo(t), prodHost)
	require.NoError(t, err)

	assert.Equal(t, 3, strings.Count(strings.Join(runner.sent, "\n"), "fetch spans"))
	assert.True(t, r.Queries[3].Skipped)
	assert.Equal(t, causePredictedCap, r.Queries[3].Cause)
}

// A rung that fills the page is as cut short as a record query that does.
func TestDiscover_SampledRungFillsThePage(t *testing.T) {
	runner := &fakeRunner{answers: map[string][]answer{
		"spans": {capped(nil), sampledAnswer(serviceNames(discoveryRowLimit), 0)},
	}}
	r, err := Discover(context.Background(), runner, singleGo(t), prodHost)
	require.NoError(t, err)

	assert.Equal(t, causeRowLimit, r.Queries[1].Cause)
	assert.True(t, r.Queries[2].Skipped)
	assert.Empty(t, r.Queries[2].Cause)
	assert.True(t, r.Partial)
	assert.Len(t, r.Candidates, discoveryRowLimit)
	assert.True(t, r.Candidates[0].Sampled)
}

// Grail answers a refusal to sample with a warning on a 200, so the rung read
// what the capped query read. Every further rung would too: the walk stops,
// and the capped answer stands.
func TestDiscover_SamplingRefusedByGrail(t *testing.T) {
	runner := &fakeRunner{answers: map[string][]answer{
		"spans": {capped(rows(t, "spans-checkout")), {records: rows(t, "spans-checkout-sampled")}},
	}}
	r, err := Discover(context.Background(), runner, singleGo(t), prodHost)
	require.NoError(t, err)

	assert.Equal(t, causeSamplingRefused, r.Queries[1].Cause)
	for _, q := range r.Queries[2:4] {
		assert.True(t, q.Skipped, q.Purpose)
		assert.Empty(t, q.Cause, q.Purpose)
	}
	require.Len(t, r.Candidates, 1)
	assert.Equal(t, int64(12400), r.Candidates[0].Spans, "the capped scan's count")
	assert.False(t, r.Candidates[0].Sampled)
	assert.True(t, r.Partial)
	assert.Equal(t, VerdictAmbiguous, r.Verdict)
	assert.Equal(t, []string{
		"this environment did not sample spans, so there is no cheaper query than the capped one",
		"spans stopped at the 5 GB scan cap, so the verdict is capped at ambiguous",
	}, r.Notes)
	assert.Empty(t, r.samplingNotes())
}

// Sampled candidates rank by the hits the sample matched.
func TestDiscover_RanksSampledCandidatesByHits(t *testing.T) {
	p, err := PlanDiscovery(fstest.MapFS{}, ".", []string{"checkout", "ledger"})
	require.NoError(t, err)
	runner := &fakeRunner{answers: map[string][]answer{
		"spans": {capped(nil), sampledAnswer([]map[string]interface{}{
			{fieldServiceName: "checkout", fieldRecords: "12"},
			{fieldServiceName: "ledger", fieldRecords: "30"},
		}, 0)},
	}}
	r, err := Discover(context.Background(), runner, p, prodHost)
	require.NoError(t, err)
	require.Len(t, r.Candidates, 2)
	assert.Equal(t, "ledger", r.Candidates[0].ServiceName)
	assert.Equal(t, int64(30), r.Candidates[0].SpansSampled)
	assert.Equal(t, "checkout", r.Candidates[1].ServiceName)
	assert.Equal(t, "12 sampled span hits in 1h (1 in 100000 requested)", r.Candidates[1].Evidence[len(r.Candidates[1].Evidence)-1])
}
