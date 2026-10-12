package exec

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/dynatrace-oss/dtctl/pkg/client"
)

func TestReferencedFields(t *testing.T) {
	cases := []struct {
		name       string
		query      string
		wantObject string
		wantFields []string
	}{
		{"filter", `fetch logs | filter servce.name == "x" | limit 1`, "logs", []string{"servce.name"}},
		{"strings, functions, keywords and named params are not fields",
			`fetch logs | filter contains(content, "a.b", caseSensitive:false) and not isNull(k8s.pod.name) or loglevel == "ERROR" and true`,
			"logs", []string{"content", "k8s.pod.name", "loglevel"}},
		{"backtick field", "fetch logs | filter `odd name` == 1", "logs", []string{"odd name"}},
		{"durations and numbers are skipped", `fetch spans | filter duration > 5ms and timestamp > now()-1h`, "spans", []string{"duration", "timestamp"}},
		{"filterOut and sort pass through", `fetch logs, from:now()-1d | sort timestamp desc | filterOut status == 200`, "logs", []string{"status"}},
		{"summarize by: is read, then the walk stops",
			`fetch logs | filter a == 1 | summarize c = count(), by:{servce.name, bucket = bin(timestamp, 5m)} | filter c > 0`,
			"logs", []string{"a", "servce.name", "timestamp"}},
		{"summarize with a bare by:", `fetch logs | summarize count(), by: host.name`, "logs", []string{"host.name"}},
		{"a field-creating stage stops the walk", `fetch logs | fieldsAdd x = 1 | filter x == 1`, "logs", nil},
		{"pipe inside a string does not split", `fetch logs | filter content == "a | b" and svc == "y"`, "logs", []string{"content", "svc"}},
		{"subquery brackets are skipped", `fetch logs | filter in(dt.entity.host, [fetch dt.entity.host | fields id])`, "logs", []string{"dt.entity.host"}},
		{"duplicates collapse", `fetch logs | filter a == 1 or a == 2`, "logs", []string{"a"}},
		{"not a fetch", `timeseries avg(dt.host.cpu.usage)`, "", nil},
		{"line comments are ignored", "fetch logs // | filter nope == 1\n| filter real == 1", "logs", []string{"real"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			obj, fields := referencedFields(tc.query)
			if obj != tc.wantObject || !reflect.DeepEqual(fields, tc.wantFields) {
				t.Errorf("referencedFields(%q) = %q, %q; want %q, %q", tc.query, obj, fields, tc.wantObject, tc.wantFields)
			}
		})
	}
}

// fakeProbe records every probe and answers from a canned function.
type fakeProbe struct {
	calls   []string
	opts    []DQLExecuteOptions
	respond func(query string) (*DQLQueryResponse, error)
}

func (f *fakeProbe) run(_ context.Context, query string, opts DQLExecuteOptions) (*DQLQueryResponse, error) {
	f.calls = append(f.calls, query)
	f.opts = append(f.opts, opts)
	return f.respond(query)
}

func recordsResponse(records ...map[string]interface{}) *DQLQueryResponse {
	return &DQLQueryResponse{State: "SUCCEEDED", Result: &DQLResult{Records: records}}
}

func logSample() *DQLQueryResponse {
	return recordsResponse(
		map[string]interface{}{"timestamp": "t", "content": "c", "service.name": "a", "loglevel": "INFO"},
		map[string]interface{}{"timestamp": "t", "content": "c", "k8s.pod.name": "p"},
	)
}

func hasSuggestion(s []string, sub string) bool {
	for _, x := range s {
		if strings.Contains(x, sub) {
			return true
		}
	}
	return false
}

func TestEmptyResultAdvice_FieldTypo(t *testing.T) {
	p := &fakeProbe{respond: func(string) (*DQLQueryResponse, error) { return logSample(), nil }}
	e := &DQLExecutor{probe: p.run}
	query := `fetch logs | filter servce.name == "x" | limit 1`

	reason, sugg := e.emptyResultAdvice(query, recordsResponse(), nil, DQLExecuteOptions{})

	if reason == nil {
		t.Fatalf("want an empty_reason, got none; suggestions=%v", sugg)
	}
	if reason.Code != "field_not_in_sample" || reason.Field != "servce.name" || reason.DataObject != "logs" ||
		!reflect.DeepEqual(reason.DidYouMean, []string{"service.name"}) || reason.SampleSize != 2 {
		t.Errorf("reason = %+v", reason)
	}
	if !strings.Contains(reason.Evidence, "2 sampled") {
		t.Errorf("evidence must state the basis of the finding: %q", reason.Evidence)
	}
	if !hasSuggestion(sugg, "service.name") {
		t.Errorf("suggestions should name the near match: %v", sugg)
	}
	if hasSuggestion(sugg, "DEFAULT query window") {
		t.Errorf("widen-the-window advice must be withheld when a near-match typo explains the empty result: %v", sugg)
	}
	// One bounded probe: the user's own fetch stage plus a small limit.
	if len(p.calls) != 1 || p.calls[0] != "fetch logs | limit 100" {
		t.Errorf("probe calls = %q", p.calls)
	}
	o := p.opts[0]
	if o.MaxResultRecords != 100 || o.MaxResultBytes == 0 || o.DefaultScanLimitGbytes == 0 || o.FetchTimeoutSeconds == 0 {
		t.Errorf("probe must be bounded, got %+v", o)
	}
}

// A caller that rewrote the query (a repo scope inserts a filter) passes the
// text as typed: the diagnosis must judge only the fields the user wrote, and
// sample the user's own fetch stage.
func TestEmptyResultAdvice_DiagnosesTheTypedQuery(t *testing.T) {
	const typed = `fetch logs | filter servce.name == "x"`
	const sent = "fetch logs | filter (k8s.workload.name == \"checkout\")\n| filter servce.name == \"x\""

	p := &fakeProbe{respond: func(string) (*DQLQueryResponse, error) { return logSample(), nil }}
	e := &DQLExecutor{probe: p.run}
	reason, sugg := e.emptyResultAdvice(sent, recordsResponse(), nil, DQLExecuteOptions{TypedQuery: typed})
	if reason == nil || reason.Field != "servce.name" {
		t.Fatalf("reason = %+v, want the typed field servce.name", reason)
	}
	if hasSuggestion(sugg, "k8s.workload.name") {
		t.Errorf("the inserted filter's field must not be diagnosed: %v", sugg)
	}
	if len(p.calls) != 1 || p.calls[0] != "fetch logs | limit 100" {
		t.Errorf("probe calls = %q", p.calls)
	}

	// Positive control: without the typed text the inserted field is judged.
	_, sugg = e.emptyResultAdvice(sent, recordsResponse(), nil, DQLExecuteOptions{})
	if !hasSuggestion(sugg, "k8s.workload.name") {
		t.Errorf("control: the sent query's fields should be diagnosed: %v", sugg)
	}
}

func TestEmptyResultAdvice_FieldProbeKeepsUserWindow(t *testing.T) {
	p := &fakeProbe{respond: func(string) (*DQLQueryResponse, error) { return logSample(), nil }}
	e := &DQLExecutor{probe: p.run}
	opts := DQLExecuteOptions{DefaultTimeframeStart: "2026-01-01T00:00:00Z", DefaultTimeframeEnd: "2026-01-02T00:00:00Z"}

	e.emptyResultAdvice(`fetch logs, from:now()-7d, bucket:{"b"} | filter servce.name == "x"`, recordsResponse(), nil, opts)

	if len(p.calls) != 1 || p.calls[0] != `fetch logs, from:now()-7d, bucket:{"b"} | limit 100` {
		t.Errorf("probe must reuse the user's fetch stage verbatim, got %q", p.calls)
	}
	if p.opts[0].DefaultTimeframeStart != opts.DefaultTimeframeStart || p.opts[0].DefaultTimeframeEnd != opts.DefaultTimeframeEnd {
		t.Errorf("probe must sample the user's window, got %+v", p.opts[0])
	}
}

func TestEmptyResultAdvice_FieldPresentKeepsWindowAdvice(t *testing.T) {
	p := &fakeProbe{respond: func(string) (*DQLQueryResponse, error) { return logSample(), nil }}
	e := &DQLExecutor{probe: p.run}
	query := `fetch logs | filter service.name == "nope"`

	reason, sugg := e.emptyResultAdvice(query, recordsResponse(), nil, DQLExecuteOptions{})

	if reason != nil {
		t.Errorf("no finding expected, got %+v", reason)
	}
	if !reflect.DeepEqual(sugg, windowAdvice(query, nil, DQLExecuteOptions{})) {
		t.Errorf("suggestions = %v, want today's window advice", sugg)
	}
}

func TestEmptyResultAdvice_FieldAbsentWithoutNearMatchIsHedged(t *testing.T) {
	p := &fakeProbe{respond: func(string) (*DQLQueryResponse, error) { return logSample(), nil }}
	e := &DQLExecutor{probe: p.run}
	query := `fetch logs | filter trace_id == "abc"`

	reason, sugg := e.emptyResultAdvice(query, recordsResponse(), nil, DQLExecuteOptions{})

	if reason != nil {
		t.Errorf("a field with no near match must not become a structured finding: %+v", reason)
	}
	if !hasSuggestion(sugg, "`trace_id` did not occur in any of the 2 sampled") || !hasSuggestion(sugg, "rare field") {
		t.Errorf("want a hedged not-in-sample note, got %v", sugg)
	}
	if !hasSuggestion(sugg, "DEFAULT query window") {
		t.Errorf("window advice must stay when the diagnosis is inconclusive: %v", sugg)
	}
}

// A sample cut short by a scan, result, time or consumption limit may be
// skewed toward whatever was read first, so it is not used as evidence.
func TestEmptyResultAdvice_PartialFieldSampleClaimsNothing(t *testing.T) {
	query := `fetch logs | filter servce.name == "x"`
	for _, n := range []QueryNotification{
		{Severity: "WARNING", NotificationType: "SCAN_LIMIT_GBYTES", Message: "Your execution was stopped after 1 gigabytes of data were scanned."},
		{Severity: "WARNING", NotificationType: "RESULT_LIMIT_BYTES", Message: "The result has been limited to 4000000 bytes."},
		{Severity: "WARNING", NotificationType: "RESULT_LIMIT_RECORDS", Message: "The result has been limited to 100 records."},
		{Severity: "WARNING", NotificationType: "FETCH_TIMEOUT", Message: "The fetch timed out."},
		{Severity: "WARNING", NotificationType: "QUERY_CONSUMPTION_LIMIT", Message: "The query consumption limit was reached."},
	} {
		t.Run(n.NotificationType, func(t *testing.T) {
			p := &fakeProbe{respond: func(string) (*DQLQueryResponse, error) {
				r := logSample()
				r.Metadata = &DQLMetadata{Grail: &GrailMetadata{Notifications: []QueryNotification{n}}}
				return r, nil
			}}
			e := &DQLExecutor{probe: p.run}

			reason, sugg := e.emptyResultAdvice(query, recordsResponse(), nil, DQLExecuteOptions{})

			if reason != nil || !reflect.DeepEqual(sugg, windowAdvice(query, nil, DQLExecuteOptions{})) {
				t.Errorf("a partial sample is no evidence: reason=%+v sugg=%v", reason, sugg)
			}
		})
	}
}

// When the user's own query was cut short, the limit may be why it is empty,
// so no probe runs and the advice stays as it was without diagnosis.
func TestEmptyResultAdvice_PartialQueryResultIsNotDiagnosed(t *testing.T) {
	query := `fetch logs | filter servce.name == "x"`
	for _, n := range []QueryNotification{
		{Severity: "WARNING", NotificationType: "SCAN_LIMIT_GBYTES", Message: "Your execution was stopped after 500 gigabytes of data were scanned."},
		{Severity: "WARNING", NotificationType: "FETCH_EXEC_TIME_LIMIT", Message: "The data couldn't be read within the internal time limit."},
		{Severity: "WARNING", NotificationType: "QUERY_CONSUMPTION_LIMIT", Message: "The query consumption limit was reached."},
	} {
		t.Run(n.NotificationType, func(t *testing.T) {
			p := &fakeProbe{respond: func(string) (*DQLQueryResponse, error) { return logSample(), nil }}
			e := &DQLExecutor{probe: p.run}
			result := recordsResponse()
			result.Metadata = &DQLMetadata{Grail: &GrailMetadata{Notifications: []QueryNotification{n}}}

			reason, sugg := e.emptyResultAdvice(query, result, nil, DQLExecuteOptions{})

			if len(p.calls) != 0 || reason != nil || !reflect.DeepEqual(sugg, windowAdvice(query, nil, DQLExecuteOptions{})) {
				t.Errorf("calls=%q reason=%+v sugg=%v", p.calls, reason, sugg)
			}
		})
	}
}

func TestEmptyResultAdvice_EmptySampleClaimsNothing(t *testing.T) {
	p := &fakeProbe{respond: func(string) (*DQLQueryResponse, error) { return recordsResponse(), nil }}
	e := &DQLExecutor{probe: p.run}
	query := `fetch logs | filter servce.name == "x"`

	reason, sugg := e.emptyResultAdvice(query, recordsResponse(), nil, DQLExecuteOptions{})

	if reason != nil || !reflect.DeepEqual(sugg, windowAdvice(query, nil, DQLExecuteOptions{})) {
		t.Errorf("an empty sample is no evidence: reason=%+v sugg=%v", reason, sugg)
	}
}

func metricResult(key, start, end string) *DQLQueryResponse {
	return &DQLQueryResponse{
		State:  "SUCCEEDED",
		Result: &DQLResult{},
		Metadata: &DQLMetadata{
			Grail:   &GrailMetadata{AnalysisTimeframe: &AnalysisTimeframe{Start: start, End: end}},
			Metrics: []MetricInfo{{MetricKey: key}},
		},
	}
}

func metricCatalog(keys ...string) *DQLQueryResponse {
	var recs []map[string]interface{}
	for _, k := range keys {
		recs = append(recs, map[string]interface{}{"metric.key": k})
	}
	return recordsResponse(recs...)
}

func TestEmptyResultAdvice_MetricTypo(t *testing.T) {
	p := &fakeProbe{respond: func(string) (*DQLQueryResponse, error) {
		return metricCatalog("dt.host.cpu.usage", "dt.host.cpu.user", "dt.host.memory.usage"), nil
	}}
	e := &DQLExecutor{probe: p.run}
	query := `timeseries avg(dt.host.cpu.usge)`
	// A 24h window: the probe must clamp to its last 2h.
	result := metricResult("dt.host.cpu.usge", "2026-06-21T00:00:00Z", "2026-06-22T00:00:00Z")

	reason, sugg := e.emptyResultAdvice(query, result, nil, DQLExecuteOptions{})

	if reason == nil {
		t.Fatalf("want an empty_reason, got none; suggestions=%v", sugg)
	}
	if reason.Code != "metric_not_in_window" || reason.Metric != "dt.host.cpu.usge" ||
		!reflect.DeepEqual(reason.DidYouMean, []string{"dt.host.cpu.usage"}) {
		t.Errorf("reason = %+v", reason)
	}
	if !strings.Contains(reason.Evidence, "2026-06-21T22:00:00Z") || !strings.Contains(reason.Evidence, "2026-06-22T00:00:00Z") {
		t.Errorf("evidence must name the window that was checked: %q", reason.Evidence)
	}
	if hasSuggestion(sugg, "DEFAULT query window") {
		t.Errorf("window advice must be withheld on a near-match metric key: %v", sugg)
	}
	if len(p.calls) != 1 || !strings.HasPrefix(p.calls[0], "metrics") {
		t.Fatalf("probe calls = %q", p.calls)
	}
	o := p.opts[0]
	if o.DefaultTimeframeStart != "2026-06-21T22:00:00Z" || o.DefaultTimeframeEnd != "2026-06-22T00:00:00Z" {
		t.Errorf("metric probe window = %s..%s, want the last 2h of the query window", o.DefaultTimeframeStart, o.DefaultTimeframeEnd)
	}
	if o.MaxResultRecords == 0 || o.FetchTimeoutSeconds == 0 {
		t.Errorf("probe must be bounded, got %+v", o)
	}
}

func TestEmptyResultAdvice_MetricKnownKeepsWindowAdvice(t *testing.T) {
	p := &fakeProbe{respond: func(string) (*DQLQueryResponse, error) { return metricCatalog("dt.host.cpu.usage"), nil }}
	e := &DQLExecutor{probe: p.run}
	query := `timeseries avg(dt.host.cpu.usage)`

	reason, sugg := e.emptyResultAdvice(query, metricResult("dt.host.cpu.usage", "2026-06-21T23:00:00Z", "2026-06-22T00:00:00Z"), nil, DQLExecuteOptions{})

	if reason != nil || !reflect.DeepEqual(sugg, windowAdvice(query, nil, DQLExecuteOptions{})) {
		t.Errorf("reason=%+v sugg=%v", reason, sugg)
	}
}

func TestEmptyResultAdvice_PartialMetricCatalogClaimsNothing(t *testing.T) {
	p := &fakeProbe{respond: func(string) (*DQLQueryResponse, error) {
		r := metricCatalog("dt.host.cpu.usage")
		r.Metadata = &DQLMetadata{Grail: &GrailMetadata{Notifications: []QueryNotification{{
			Severity: "WARNING", NotificationType: "RESULT_LIMIT_RECORDS", Message: "The result has been limited to 50000 records.",
		}}}}
		return r, nil
	}}
	e := &DQLExecutor{probe: p.run}
	query := `timeseries avg(dt.host.cpu.usge)`

	reason, sugg := e.emptyResultAdvice(query, metricResult("dt.host.cpu.usge", "2026-06-21T23:00:00Z", "2026-06-22T00:00:00Z"), nil, DQLExecuteOptions{})

	if reason != nil || !reflect.DeepEqual(sugg, windowAdvice(query, nil, DQLExecuteOptions{})) {
		t.Errorf("a truncated catalog cannot prove absence: reason=%+v sugg=%v", reason, sugg)
	}
}

func TestEmptyResultAdvice_ProbeFailureDegradesSilently(t *testing.T) {
	p := &fakeProbe{respond: func(string) (*DQLQueryResponse, error) { return nil, errors.New("403 forbidden") }}
	e := &DQLExecutor{probe: p.run}
	for _, tc := range []struct {
		query  string
		result *DQLQueryResponse
	}{
		{`fetch logs | filter servce.name == "x"`, recordsResponse()},
		{`timeseries avg(dt.host.cpu.usge)`, metricResult("dt.host.cpu.usge", "2026-06-21T23:00:00Z", "2026-06-22T00:00:00Z")},
	} {
		reason, sugg := e.emptyResultAdvice(tc.query, tc.result, nil, DQLExecuteOptions{})
		if reason != nil || !reflect.DeepEqual(sugg, windowAdvice(tc.query, nil, DQLExecuteOptions{})) {
			t.Errorf("%s: reason=%+v sugg=%v", tc.query, reason, sugg)
		}
	}
}

func TestEmptyResultAdvice_NonEmptyResultNeverProbes(t *testing.T) {
	p := &fakeProbe{respond: func(string) (*DQLQueryResponse, error) { return logSample(), nil }}
	e := &DQLExecutor{probe: p.run}
	rows := []map[string]interface{}{{"servce.name": "x"}}

	reason, sugg := e.emptyResultAdvice(`fetch logs | filter servce.name == "x"`, recordsResponse(rows...), rows, DQLExecuteOptions{})

	if len(p.calls) != 0 || reason != nil || len(sugg) != 0 {
		t.Errorf("calls=%q reason=%+v sugg=%v", p.calls, reason, sugg)
	}
}

func TestEmptyResultAdvice_NoClientSkipsProbes(t *testing.T) {
	e := &DQLExecutor{}
	query := `fetch logs | filter servce.name == "x"`
	reason, sugg := e.emptyResultAdvice(query, recordsResponse(), nil, DQLExecuteOptions{})
	if reason != nil || !reflect.DeepEqual(sugg, windowAdvice(query, nil, DQLExecuteOptions{})) {
		t.Errorf("reason=%+v sugg=%v", reason, sugg)
	}
}

// TestE2E_EmptyResultCarriesEmptyReason runs the real executor against a fake
// Grail that answers the user's query with nothing and the probe with a sample,
// and checks the agent envelope end to end.
func TestE2E_EmptyResultCarriesEmptyReason(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var req struct {
			Query string `json:"query"`
		}
		_ = json.Unmarshal(body, &req)
		resp := DQLQueryResponse{State: "SUCCEEDED", Result: &DQLResult{}}
		if strings.HasSuffix(req.Query, "| limit 100") {
			resp.Result.Records = logSample().Result.Records
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	t.Cleanup(srv.Close)
	c, err := client.NewForTesting(srv.URL, "test-token")
	if err != nil {
		t.Fatalf("client: %v", err)
	}
	e := NewDQLExecutor(c)

	for _, tc := range []struct {
		name string
		opts DQLExecuteOptions
	}{
		{"inline envelope", DQLExecuteOptions{AgentMode: true, Spill: SpillOptions{Mode: SpillNever, Format: "json"}}},
		{"--jq envelope", DQLExecuteOptions{AgentMode: true, JQFilter: ".records", OutputFormat: "json"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out := runAndCapture(t, func() error {
				return e.ExecuteWithOptions(`fetch logs | filter servce.name == "x" | limit 1`, tc.opts)
			})
			var env struct {
				Context struct {
					EmptyReason *struct {
						Code       string   `json:"code"`
						Field      string   `json:"field"`
						DidYouMean []string `json:"did_you_mean"`
					} `json:"empty_reason"`
					Suggestions []string `json:"suggestions"`
				} `json:"context"`
			}
			if err := json.Unmarshal([]byte(out), &env); err != nil {
				t.Fatalf("envelope: %v\n%s", err, out)
			}
			er := env.Context.EmptyReason
			if er == nil || er.Code != "field_not_in_sample" || er.Field != "servce.name" || !reflect.DeepEqual(er.DidYouMean, []string{"service.name"}) {
				t.Errorf("empty_reason = %+v\n%s", er, out)
			}
			if hasSuggestion(env.Context.Suggestions, "DEFAULT query window") {
				t.Errorf("window advice should be withheld: %v", env.Context.Suggestions)
			}
		})
	}
}
