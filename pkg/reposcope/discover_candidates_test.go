package reposcope

import (
	"context"
	"fmt"
	"testing"
	"testing/fstest"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	sdkquery "github.com/Dynatrace/dtctl/sdk/api/query"
	"github.com/Dynatrace/dtctl/sdk/inventory"
)

// Logs of a workload need not carry a service name while its spans do. The
// two rows are one workload: as two candidates the report said the first had
// no logs under its workload, beside a second candidate that was those logs.
func TestDiscover_RowWithoutServiceNameJoinsItsWorkload(t *testing.T) {
	row := func(uid, serviceName string, records float64) map[string]interface{} {
		r := map[string]interface{}{fieldNamespace: "payments", fieldWorkload: "checkout", fieldRecords: records,
			fieldProcessGroups: collected(pgID), fieldProcessGroupCount: float64(1)}
		if uid != "" {
			r[fieldClusterUID] = uid
		}
		if serviceName != "" {
			r[fieldServiceName] = serviceName
		}
		return r
	}
	for name, uid := range map[string]string{"in a cluster": clusterUID, "without a cluster": ""} {
		t.Run(name, func(t *testing.T) {
			runner := &fakeRunner{answers: map[string][]answer{
				"spans": {{records: []map[string]interface{}{row(uid, "checkout", 300)}}},
				"logs":  {{records: []map[string]interface{}{row(uid, "", 500)}}},
			}}
			r, err := Discover(context.Background(), runner, singleGo(t), prodHost)
			require.NoError(t, err)

			require.Len(t, r.Candidates, 1)
			c := r.Candidates[0]
			assert.Equal(t, []string{uid, "checkout", "checkout"}, []string{c.ClusterUID, c.Workload, c.ServiceName})
			assert.Equal(t, []int64{300, 500, 0}, []int64{c.Spans, c.Logs, c.LogsByProcessGroup})
			assert.Equal(t, VerdictMatch, r.Verdict)
			assert.Empty(t, r.Notes, "its logs were found")
			assert.Len(t, runner.sent, 2, "no lookup by process group")
		})
	}

	t.Run("then its cluster", func(t *testing.T) {
		// A row with neither joins the service name first, then the cluster.
		runner := &fakeRunner{answers: map[string][]answer{
			"spans": {{records: []map[string]interface{}{row(clusterUID, "checkout", 300), row(clusterUID, "", 20)}}},
			"logs":  {{records: []map[string]interface{}{row("", "", 500)}}},
		}}
		r, err := Discover(context.Background(), runner, singleGo(t), prodHost)
		require.NoError(t, err)
		require.Len(t, r.Candidates, 1)
		c := r.Candidates[0]
		assert.Equal(t, []int64{320, 500}, []int64{c.Spans, c.Logs})
		assert.Equal(t, []string{"logs"}, c.WithoutCluster)
	})

	t.Run("not beside two service names", func(t *testing.T) {
		// Which of the two the row belongs to is not known.
		runner := &fakeRunner{answers: map[string][]answer{
			"spans": {{records: []map[string]interface{}{row(clusterUID, "checkout", 300), row(clusterUID, "checkout-service", 100)}}},
			"logs":  {{records: []map[string]interface{}{row(clusterUID, "", 500)}}},
		}}
		r, err := Discover(context.Background(), runner, singleGo(t), prodHost)
		require.NoError(t, err)
		require.Len(t, r.Candidates, 3)
		assert.Equal(t, VerdictAmbiguous, r.Verdict)
	})
}

// Two candidates whose records name no workload are two services, not one
// workload: a process group both carry is neither's alone. The first was
// credited with the second's logs, and its line would have bound the group.
func TestDiscover_ProcessGroupSharedByServicesWithoutAWorkload(t *testing.T) {
	row := func(serviceName string, records float64) map[string]interface{} {
		return map[string]interface{}{fieldServiceName: serviceName, fieldRecords: records,
			fieldProcessGroups: collected(pgID), fieldProcessGroupCount: float64(1)}
	}
	runner := &fakeRunner{answers: map[string][]answer{
		"spans": {{records: []map[string]interface{}{row("checkout", 100)}}},
		"logs": {
			{records: []map[string]interface{}{row("checkout-service", 5000)}},
			{records: []map[string]interface{}{pgLogs(5000, pgID)}},
		},
	}}
	r, err := Discover(context.Background(), runner, singleGo(t), prodHost)
	require.NoError(t, err)

	require.Len(t, r.Candidates, 2)
	assert.Len(t, runner.sent, 2, "the group is not looked up for either")
	for _, c := range r.Candidates {
		assert.Zero(t, c.LogsByProcessGroup, c.ServiceName)
	}
	assert.Equal(t, []string{
		"candidate 1 has spans, but no logs carry its k8s.workload.name or service.name, so an entry bound by those reaches no logs; if its logs carry a process group, bind it with --process-group",
	}, r.Notes)
}

// The query the scan cap stopped still returned rows, and each names an
// identity that exists. When a sampled retry answers, the ones it did not
// return are kept as candidates, without the count a partial scan cannot
// give, and the result is partial. They were thrown away, so a second cluster
// the capped scan had already shown disappeared from the report.
func TestDiscover_SampledRetryKeepsWhatTheCappedScanSaw(t *testing.T) {
	const otherCluster = "99999999-2222-3333-4444-555555555555"
	row := func(uid string, records float64) map[string]interface{} {
		return map[string]interface{}{fieldClusterUID: uid, fieldNamespace: "payments", fieldWorkload: "checkout",
			fieldServiceName: "checkout", fieldRecords: records, fieldProcessGroups: collected(pgID), fieldProcessGroupCount: float64(1)}
	}
	runner := &fakeRunner{answers: map[string][]answer{
		"spans": {
			capped([]map[string]interface{}{row(clusterUID, 4000), row(otherCluster, 9000)}),
			sampledAnswer([]map[string]interface{}{row(clusterUID, 17)}, 100_000_000),
		},
	}}
	r, err := Discover(context.Background(), runner, singleGo(t), prodHost)
	require.NoError(t, err)

	require.Len(t, r.Candidates, 2)
	found, seen := r.Candidates[0], r.Candidates[1]
	assert.Equal(t, []string{clusterUID, otherCluster}, []string{found.ClusterUID, seen.ClusterUID})
	assert.Equal(t, int64(17), found.SpansSampled)
	assert.Empty(t, found.CappedScan)
	assert.Equal(t, []string{"spans"}, seen.CappedScan)
	assert.Equal(t, []int64{0, 0}, []int64{seen.Spans, seen.SpansSampled}, "a partial scan's count is not one")
	assert.Empty(t, seen.ProcessGroups, "nor are its ids all of them")
	assert.Equal(t, "seen in the spans scan the 5 GB cap stopped, not in the sample that replaced it", seen.Evidence[len(seen.Evidence)-1])
	assert.True(t, found.WorkloadShared)
	assert.True(t, r.Partial)
	assert.Equal(t, VerdictAmbiguous, r.Verdict)
	assert.Contains(t, r.Notes, "the spans scan the 5 GB cap stopped saw candidates the sample did not return; they are listed without a count, and the result is partial")

	// What the sample returned is not listed twice, and a row the capped
	// scan saw of a candidate another query counted joins it.
	runner = &fakeRunner{answers: map[string][]answer{
		"spans": {
			capped([]map[string]interface{}{row(clusterUID, 4000), row(otherCluster, 9000)}),
			sampledAnswer([]map[string]interface{}{row(clusterUID, 17)}, 100_000_000),
		},
		"logs": {{records: []map[string]interface{}{row(otherCluster, 50)}}},
	}}
	r, err = Discover(context.Background(), runner, singleGo(t), prodHost)
	require.NoError(t, err)
	require.Len(t, r.Candidates, 2)
	for _, c := range r.Candidates {
		if c.ClusterUID == otherCluster {
			assert.Equal(t, []string{"spans"}, c.CappedScan)
			assert.Equal(t, int64(50), c.Logs)
		}
	}

	runner = &fakeRunner{answers: map[string][]answer{
		"spans": {capped([]map[string]interface{}{row(clusterUID, 4000)}), sampledAnswer([]map[string]interface{}{row(clusterUID, 17)}, 100_000_000)},
	}}
	r, err = Discover(context.Background(), runner, singleGo(t), prodHost)
	require.NoError(t, err)
	require.Len(t, r.Candidates, 1)
	assert.Empty(t, r.Candidates[0].CappedScan)
	assert.False(t, r.Partial)
}

// Grail says a result was limited in two ways. A typed RESULT_LIMIT_BYTES or
// RESULT_LIMIT_RECORDS is a page that lost rows, however short it is. Only
// the message without a type, which is also how a collected id array cut at
// its maxLength is reported, leaves a short page whole.
func TestDiscover_TypedResultLimitIsACutPage(t *testing.T) {
	for name, tc := range map[string]struct {
		answer  answer
		partial bool
	}{
		"typed":                 {answer{truncations: []inventory.Truncation{{Cause: inventory.TruncationResultLimit, Typed: true}}}, true},
		"by its message alone":  {answer{truncations: []inventory.Truncation{{Cause: inventory.TruncationResultLimit}}}, false},
		"both":                  {answer{truncations: []inventory.Truncation{{Cause: inventory.TruncationResultLimit}, {Cause: inventory.TruncationResultLimit, Typed: true}}}, true},
		"a runner with a cause": {answer{truncation: inventory.TruncationResultLimit}, true},
	} {
		t.Run(name, func(t *testing.T) {
			tc.answer.records = rows(t, "spans-checkout")
			r, err := Discover(context.Background(), &fakeRunner{answers: map[string][]answer{
				"spans": {tc.answer},
				"logs":  {{records: rows(t, "logs-checkout")}},
			}}, singleGo(t), prodHost)
			require.NoError(t, err)
			require.Len(t, r.Candidates, 1)
			assert.Equal(t, tc.partial, r.Partial)
			assert.Equal(t, tc.partial, r.Queries[0].Truncated)
			if tc.partial {
				assert.Equal(t, VerdictAmbiguous, r.Verdict)
				assert.Equal(t, []string{"spans stopped at Grail's result limit, so the result is partial"}, r.Notes)
			} else {
				assert.Equal(t, VerdictMatch, r.Verdict)
				assert.Empty(t, r.Notes)
			}
		})
	}
}

// A response can report a result limit and the scan cap together. The scan
// cap is the one that says how little was read, and the one a sampled retry
// answers, so it decides whichever notification came first.
func TestDiscover_ScanCapWinsOverAnotherLimit(t *testing.T) {
	scan := inventory.Truncation{Cause: inventory.TruncationScanLimit, Typed: true}
	for name, limits := range map[string][]inventory.Truncation{
		"reported first": {scan, {Cause: inventory.TruncationResultLimit}},
		"reported last":  {{Cause: inventory.TruncationResultLimit}, scan},
		"after a typed":  {{Cause: inventory.TruncationResultLimit, Typed: true}, scan},
	} {
		t.Run(name, func(t *testing.T) {
			runner := &fakeRunner{answers: map[string][]answer{
				"spans": {{records: rows(t, "spans-checkout"), truncations: limits}, sampledAnswer(rows(t, "spans-checkout-sampled"), 100_000_000)},
				"logs":  {{records: rows(t, "logs-checkout")}},
			}}
			r, err := Discover(context.Background(), runner, singleGo(t), prodHost)
			require.NoError(t, err)
			assert.Len(t, runner.sent, 3, "the capped query was retried sampled")
			assert.Equal(t, inventory.TruncationScanLimit, r.Queries[0].truncation)
			require.Len(t, r.Candidates, 1)
			assert.True(t, r.Candidates[0].Sampled)
		})
	}
}

// A row the query kept that discovery cannot explain is not an exact match.
// in() on an array-valued service.name keeps the row of an unrelated
// workload, and the tier used to fall back to exact, which made it a match
// with no evidence line naming a file.
func TestDiscover_UnexplainedRowIsNotExact(t *testing.T) {
	r, err := Discover(context.Background(), &fakeRunner{answers: map[string][]answer{
		"spans": {{records: []map[string]interface{}{{
			fieldNamespace: "batch", fieldWorkload: "totally-unrelated",
			fieldServiceName: []interface{}{"checkout", "nightly"}, fieldRecords: float64(40),
		}}}},
	}}, singleGo(t), prodHost)
	require.NoError(t, err)

	require.Len(t, r.Candidates, 1)
	c := r.Candidates[0]
	assert.Equal(t, "substring", c.Tier)
	assert.Equal(t, VerdictAmbiguous, r.Verdict)
	assert.Equal(t, []string{
		"neither its k8s.workload.name nor its service.name is a name discovery sent",
		"40 spans in 24h",
	}, c.Evidence)
}

// A verdict per kind of binding is never better than the overall one: with
// two candidates there is no kind that is settled.
func TestDiscover_KindVerdictsFollowTheOverallOne(t *testing.T) {
	row := func(uid string) map[string]interface{} {
		return map[string]interface{}{fieldClusterUID: uid, fieldNamespace: "payments", fieldWorkload: "checkout",
			fieldServiceName: "checkout", fieldRecords: float64(10), fieldProcessGroups: collected(pgID), fieldProcessGroupCount: float64(1)}
	}
	// Two clusters a scope file cannot hold: the workload kind used to see
	// one value, since neither candidate has a cluster uid to tell them by.
	r, err := Discover(context.Background(), &fakeRunner{answers: map[string][]answer{
		"spans": {{records: []map[string]interface{}{row("PROD-A"), row("PROD-B")}}},
		"logs":  {{records: []map[string]interface{}{row("PROD-A"), row("PROD-B")}}},
	}}, singleGo(t), prodHost)
	require.NoError(t, err)
	require.Len(t, r.Candidates, 2)
	assert.Equal(t, VerdictAmbiguous, r.Verdict)
	assert.Equal(t, map[string]Verdict{
		"workload": VerdictAmbiguous, "serviceName": VerdictAmbiguous, "processGroup": VerdictAmbiguous, "service": VerdictNone,
	}, r.Verdicts)

	// Alone, the same candidate is a match for the kinds it has.
	r, err = Discover(context.Background(), &fakeRunner{answers: map[string][]answer{
		"spans": {{records: []map[string]interface{}{row("PROD-A")}}},
		"logs":  {{records: []map[string]interface{}{row("PROD-A")}}},
	}}, singleGo(t), prodHost)
	require.NoError(t, err)
	assert.Equal(t, VerdictMatch, r.Verdict)
	assert.Equal(t, map[string]Verdict{
		"workload": VerdictMatch, "serviceName": VerdictMatch, "processGroup": VerdictMatch, "service": VerdictNone,
	}, r.Verdicts)
}

// A workload is bound by namespace and name. Records that carry the workload
// name and no namespace give no workload to bind, so that kind has none.
func TestDiscover_WorkloadWithoutANamespaceIsNoWorkloadBinding(t *testing.T) {
	r, err := Discover(context.Background(), &fakeRunner{answers: map[string][]answer{
		"spans": {{records: []map[string]interface{}{{fieldWorkload: "checkout", fieldRecords: float64(10)}}}},
		"logs":  {{records: []map[string]interface{}{{fieldWorkload: "checkout", fieldRecords: float64(10)}}}},
	}}, singleGo(t), prodHost)
	require.NoError(t, err)
	require.Len(t, r.Candidates, 1)
	assert.Equal(t, map[string]Verdict{
		"workload": VerdictNone, "serviceName": VerdictNone, "processGroup": VerdictNone, "service": VerdictNone,
	}, r.Verdicts)
}

// An interruption after both record queries answered leaves nothing unread
// unless the lookup of logs by process group was still due. The report used
// to say "discovery was interrupted, so the result is incomplete" above a
// match it had read in full.
func TestDiscover_InterruptionAfterTheRecordQueries(t *testing.T) {
	interrupted := func(logs []map[string]interface{}) (*DiscoveryReport, *fakeRunner, error) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		runner := &fakeRunner{
			answers: map[string][]answer{"spans": {{records: rows(t, "spans-checkout")}}, "logs": {{records: logs}}},
			onRun: func(_ context.Context, object string) {
				if object == "logs" {
					cancel() // while the last record query runs; its answer still arrives
				}
			},
		}
		r, err := Discover(ctx, runner, singleGo(t), prodHost)
		return r, runner, err
	}

	r, runner, err := interrupted(rows(t, "logs-checkout"))
	require.ErrorIs(t, err, context.Canceled)
	assert.Len(t, runner.sent, 2)
	assert.Equal(t, VerdictMatch, r.Verdict)
	assert.False(t, r.Partial)
	assert.Empty(t, r.Notes, "no lookup was due, so nothing was left unread")
	assert.Empty(t, r.processGroupCheck().Cause)

	// With no logs under the workload, the lookup was due and did not run.
	// What it would have found is a binding, not a candidate.
	r, runner, err = interrupted(nil)
	require.ErrorIs(t, err, context.Canceled)
	assert.Len(t, runner.sent, 2)
	assert.Equal(t, VerdictMatch, r.Verdict)
	assert.False(t, r.Partial)
	q := r.processGroupCheck()
	assert.True(t, q.Skipped)
	assert.Equal(t, causeCancelled, q.Cause)
	assert.Equal(t, []string{
		"discovery was interrupted before it looked for logs by process group",
		"candidate 1 has spans, but no logs carry its k8s.workload.name or service.name, so an entry bound by those reaches no logs; if its logs carry a process group, bind it with --process-group",
	}, r.Notes)
}

// Whether a row's id list is whole is decided from the list: the query
// collects one id more than a binding takes, so a list that long was cut.
// The distinct count beside it is an estimate in DQL, and for an array field
// counts arrays, so it cannot tell, and is used only as how many ids a cut
// list stands for.
func TestDiscover_IDListCutIsReadFromTheList(t *testing.T) {
	services := func(n int) []interface{} {
		ids := make([]interface{}, n)
		for i := range ids {
			ids[i] = fmt.Sprintf("SERVICE-%016X", 0xC000+i)
		}
		return ids
	}
	for name, tc := range map[string]struct {
		count  string
		list   []interface{}
		listed int
		total  int64
		capped bool
	}{
		"a whole list the estimate overshoots":  {"60", services(50), 50, 50, false},
		"a whole list the estimate undershoots": {"3", services(5), 5, 5, false},
		"a cut list":                            {"200", services(51), 50, 200, true},
		"a cut list the estimate undershoots":   {"40", services(51), 50, 51, true},
		"overlapping arrays": {"3", []interface{}{
			[]interface{}{"SERVICE-000000000000000A", "SERVICE-000000000000000B"},
			[]interface{}{"SERVICE-000000000000000A"},
			[]interface{}{"SERVICE-000000000000000B"},
		}, 2, 2, false},
		"an id a file would refuse still counts": {"2", []interface{}{"SERVICE-000000000000000A", "SERVICE-1"}, 1, 2, false},
	} {
		t.Run(name, func(t *testing.T) {
			r, err := Discover(context.Background(), &fakeRunner{answers: map[string][]answer{
				"spans": {{records: []map[string]interface{}{
					{fieldServiceName: "checkout", fieldServiceCount: tc.count, fieldServices: tc.list, fieldRecords: float64(1)},
				}}},
			}}, singleGo(t), prodHost)
			require.NoError(t, err)
			require.Len(t, r.Candidates, 1)
			c := r.Candidates[0]
			assert.Len(t, c.Services, tc.listed)
			assert.Equal(t, tc.total, c.ServiceCount)
			assert.Equal(t, tc.capped, c.ServicesCapped)
		})
	}
	assert.Contains(t, singleGo(t).Queries[0].DQL, "services = collectDistinct(dt.entity.service, maxLength:51),")
}

// An entity is named exactly when its whole name is a sent name, in any
// spelling of its words. A name qualified with a path, a tag or a prefix
// only holds one: "retired/checkout" was read as the image name "checkout"
// and reported as an exact match.
func TestDiscover_QualifiedEntityNameIsNotExact(t *testing.T) {
	for name, want := range map[string]string{
		"checkout":         "exact",
		"Checkout":         "exact",
		"Checkout Service": "exact",
		"checkout_service": "exact",
		"retired/checkout": "substring",
		"checkout:v2":      "substring",
		"checkout@sha256":  "substring",
		"eu.checkout":      "segment",
	} {
		r, err := Discover(context.Background(), &fakeRunner{answers: map[string][]answer{
			"dt.entity.service": {{records: []map[string]interface{}{{fieldEntityID: svcID, fieldEntityName: name}}}},
		}}, singleGo(t), prodHost)
		require.NoError(t, err)
		require.Len(t, r.Candidates, 1, name)
		assert.Equal(t, want, r.Candidates[0].Tier, name)
	}
}

// A record query's table missing from the environment is said as that, not
// in the words meant for an entity table ("the record queries are what
// applies here" about a record query). It hides nothing, so the report is not
// partial for it.
func TestDiscover_MissingRecordTable(t *testing.T) {
	r, err := Discover(context.Background(), &fakeRunner{answers: map[string][]answer{
		"spans": {{err: &sdkquery.QueryError{ErrorType: "UNKNOWN_DATA_OBJECT"}}},
		"logs":  {{records: rows(t, "logs-checkout")}},
	}}, singleGo(t), prodHost)
	require.NoError(t, err)
	assert.False(t, r.Partial)
	assert.Equal(t, VerdictMatch, r.Verdict)
	assert.Equal(t, []string{"spans is not available on this environment, so nothing was looked for there"}, r.Notes)

	r, err = Discover(context.Background(), &fakeRunner{answers: map[string][]answer{
		"dt.entity.service": {{err: &sdkquery.QueryError{ErrorType: "UNKNOWN_DATA_OBJECT"}}},
	}}, singleGo(t), prodHost)
	require.NoError(t, err)
	assert.Equal(t, []string{"dt.entity.service is not available on this environment; the record queries are what applies here"}, r.Notes)
}

// Grail may report a table the token cannot read in the message alone, with
// no error type. It is the same denial, and gets the same note.
func TestDiscover_DenialReportedInTheMessage(t *testing.T) {
	for name, qe := range map[string]*sdkquery.QueryError{
		"typed":       {StatusCode: 403, ErrorType: "NOT_AUTHORIZED_FOR_TABLE"},
		"the message": {StatusCode: 403, Message: "NOT_AUTHORIZED_FOR_TABLE: missing permission for table logs"},
		"the detail":  {StatusCode: 403, ErrorType: "FORBIDDEN", Detail: "NOT_AUTHORIZED_FOR_TABLE"},
	} {
		r, err := Discover(context.Background(), &fakeRunner{answers: map[string][]answer{
			"spans": {{records: rows(t, "spans-checkout")}},
			"logs":  {{err: fmt.Errorf("query: %w", qe)}},
		}}, singleGo(t), prodHost)
		require.NoError(t, err, name)
		assert.Equal(t, causeAuth, r.Queries[4].Cause, name)
		assert.Contains(t, r.Notes, "the token cannot read logs", name)
		assert.True(t, r.Partial, name)
	}
}

// A rung that came back empty and one after it that the environment did not
// sample: the capped answer stands, and the notes say so. They used to say
// too that the data was sampled and matched nothing, which the next note
// contradicts.
func TestDiscover_RefusedRungAfterAnEmptyOne(t *testing.T) {
	runner := &fakeRunner{answers: map[string][]answer{
		"spans": {
			capped(rows(t, "spans-checkout")),
			sampledAnswer(nil, 1_000_000),
			{records: rows(t, "spans-checkout"), scannedBytes: 4_000_000_000},
		},
		"logs": {{records: rows(t, "logs-checkout")}},
	}}
	r, err := Discover(context.Background(), runner, singleGo(t), prodHost)
	require.NoError(t, err)
	assert.Len(t, runner.sent, 4)
	assert.Equal(t, causeSamplingRefused, r.Queries[2].Cause)
	assert.Equal(t, []string{
		"this environment did not sample spans, so there is no cheaper query than the capped one",
		"spans stopped at the 5 GB scan cap, so the result is partial",
	}, r.Notes)
	assert.True(t, r.Partial)
	assert.False(t, r.SampledRecords(), "nothing was read by a sample")
}

// A workload or entity that holds several of the unit's names is credited
// with every file that names it, not only the one behind the longest name.
// Credited with go.mod alone, a candidate the directory's own name also
// matched lost MatchedDir, and its line would have covered the whole
// repository instead of the directory.
func TestDiscover_PartialMatchCreditsEveryFile(t *testing.T) {
	fsys := fstest.MapFS{
		"go.mod":         {Data: []byte("module example.invalid/ledger-writer\n")},
		"ledger/main.go": {Data: []byte("package main\n")},
	}
	p, err := PlanDiscovery(fsys, "ledger", nil)
	require.NoError(t, err)
	require.Equal(t, ".", p.Unit)
	require.Contains(t, p.Sent, "ledger")
	require.Contains(t, p.Sent, "ledger-writer")

	r, err := Discover(context.Background(), &fakeRunner{answers: map[string][]answer{
		"spans": {{records: []map[string]interface{}{
			{fieldNamespace: "prod", fieldWorkload: "acme-ledger-writer", fieldRecords: float64(10)},
			{fieldNamespace: "prod", fieldWorkload: "acme-ledger-reader", fieldRecords: float64(900)},
		}}},
	}}, p, prodHost)
	require.NoError(t, err)
	require.Len(t, r.Candidates, 2)
	writer := r.Candidates[0]
	assert.Equal(t, "acme-ledger-writer", writer.Workload, "two files name it, so it ranks ahead of more records")
	assert.True(t, writer.MatchedDir)
	assert.Contains(t, writer.Evidence, `k8s.workload.name "acme-ledger-writer" contains module "ledger-writer" (go.mod:1) (segment match)`)
	assert.True(t, r.Candidates[1].MatchedDir)

	r, err = Discover(context.Background(), &fakeRunner{answers: map[string][]answer{
		"dt.entity.service": {{records: []map[string]interface{}{{fieldEntityID: svcID, fieldEntityName: "acme-ledger-writer"}}}},
	}}, p, prodHost)
	require.NoError(t, err)
	require.Len(t, r.Candidates, 1)
	assert.True(t, r.Candidates[0].MatchedDir)
	assert.Equal(t, []string{`entity.name "acme-ledger-writer" contains module "ledger-writer" (go.mod:1) (segment match)`}, r.Candidates[0].Evidence)
}

// A query the scan cap or a time limit stopped read part of the window, so
// what it counted is a lower bound, and the evidence says "at least". It
// used to print the count like one read whole.
func TestDiscover_CountsOfAStoppedQueryAreLowerBounds(t *testing.T) {
	typed := func(c inventory.TruncationCause) answer {
		return answer{records: rows(t, "spans-checkout"), truncations: []inventory.Truncation{{Cause: c, Typed: true}}}
	}
	for name, tc := range map[string]struct {
		spans answer
		want  string
	}{
		"the scan cap, no sample":  {capped(rows(t, "spans-checkout")), "at least 12400 spans, 88000 logs in 24h"},
		"a time limit":             {typed(inventory.TruncationTimeout), "at least 12400 spans, 88000 logs in 24h"},
		"the consumption limit":    {typed(inventory.TruncationConsumption), "at least 12400 spans, 88000 logs in 24h"},
		"a result limit cuts rows": {typed(inventory.TruncationResultLimit), "12400 spans, 88000 logs in 24h"},
		"read whole":               {answer{records: rows(t, "spans-checkout")}, "12400 spans, 88000 logs in 24h"},
	} {
		t.Run(name, func(t *testing.T) {
			r, err := Discover(context.Background(), &fakeRunner{answers: map[string][]answer{
				"spans": {tc.spans},
				"logs":  {{records: rows(t, "logs-checkout")}},
			}}, singleGo(t), prodHost)
			require.NoError(t, err)
			require.Len(t, r.Candidates, 1)
			e := r.Candidates[0].Evidence
			assert.Equal(t, tc.want, e[len(e)-1])
		})
	}
}

// A name in the entity table is not evidence that the code runs: an entity
// outlives its last record. One exact entity-name match with no span or log
// in the window is therefore a candidate to confirm, not a match.
func TestDiscover_EntityNameAloneIsNotAMatch(t *testing.T) {
	r, err := Discover(context.Background(), &fakeRunner{answers: map[string][]answer{
		"dt.entity.service": {{records: []map[string]interface{}{{fieldEntityID: svcID, fieldEntityName: "checkout"}}}},
	}}, singleGo(t), prodHost)
	require.NoError(t, err)
	require.Len(t, r.Candidates, 1)
	assert.Equal(t, "exact", r.Candidates[0].Tier)
	assert.Equal(t, VerdictAmbiguous, r.Verdict)
	assert.Equal(t, VerdictAmbiguous, r.Verdicts["service"])
	assert.False(t, r.Partial, "every query answered")
}
