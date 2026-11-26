package exec

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/Dynatrace/dtctl/pkg/client"
	"github.com/Dynatrace/dtctl/pkg/output"
)

// newTimeseriesExecutor serves one timeseries record with 60 full-precision
// points and a spike at index 30.
func newTimeseriesExecutor(t *testing.T) *DQLExecutor {
	t.Helper()
	values := make([]interface{}, 60)
	for i := range values {
		values[i] = 3.10276124773992 + float64(i%5)
	}
	values[30] = 409.123456789
	return newRecordsExecutor(t, []map[string]interface{}{{
		"host.name": "web-01",
		"cpu":       values,
		"interval":  "60000000000",
		"timeframe": map[string]interface{}{
			"start": "2026-01-01T12:00:00.000000000Z",
			"end":   "2026-01-01T13:00:00.000000000Z",
		},
	}})
}

// newRecordsExecutor serves the given records from a mock Grail server.
func newRecordsExecutor(t *testing.T, records []map[string]interface{}) *DQLExecutor {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := DQLQueryResponse{State: "SUCCEEDED", Result: &DQLResult{Records: records}}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	t.Cleanup(server.Close)
	c, err := client.NewForTesting(server.URL, "test-token")
	if err != nil {
		t.Fatalf("failed to create client: %v", err)
	}
	return NewDQLExecutor(c)
}

func TestDQLExecutor_SeriesModes(t *testing.T) {
	clearAIAgentEnvVars(t)
	executor := newTimeseriesExecutor(t)
	run := func(t *testing.T, opts DQLExecuteOptions) string {
		t.Helper()
		return string(captureStdout(t, func() {
			if err := executor.ExecuteWithContext(context.Background(), "timeseries cpu=avg(x)", opts); err != nil {
				t.Fatalf("execute: %v", err)
			}
		}))
	}

	t.Run("default output keeps every full-precision point", func(t *testing.T) {
		out := run(t, DQLExecuteOptions{OutputFormat: "json"})
		if !strings.Contains(out, "3.10276124773992") || strings.Contains(out, `"spark"`) {
			t.Errorf("default output must be unchanged:\n%s", out)
		}
	})

	t.Run("summary in json", func(t *testing.T) {
		out := run(t, DQLExecuteOptions{OutputFormat: "json", Series: output.SeriesMode{Kind: output.SeriesSummary}})
		for _, want := range []string{`"spark"`, `"max": 409`, `"max_at": "2026-01-01T12:30:00Z"`, `"n": 60`} {
			if !strings.Contains(out, want) {
				t.Errorf("summary output missing %s:\n%s", want, out)
			}
		}
		if strings.Contains(out, "3.10276124773992") {
			t.Errorf("summary should not carry raw points:\n%s", out)
		}
	})

	t.Run("summary inside the agent envelope", func(t *testing.T) {
		// Agent mode leaves -o at its "table" default; the summary must still
		// be an object in the JSON envelope, not a table cell string.
		out := run(t, DQLExecuteOptions{
			OutputFormat: "table",
			AgentMode:    true,
			Spill:        SpillOptions{Mode: SpillAuto, Threshold: 1 << 20, Dir: t.TempDir(), Format: "json"},
			Series:       output.SeriesMode{Kind: output.SeriesSummary},
		})
		var env struct {
			OK     bool `json:"ok"`
			Result struct {
				Records []map[string]interface{} `json:"records"`
			} `json:"result"`
		}
		if err := json.Unmarshal([]byte(out), &env); err != nil {
			t.Fatalf("not an envelope: %v\n%s", err, out)
		}
		cpu, ok := env.Result.Records[0]["cpu"].(map[string]interface{})
		if !env.OK || !ok || cpu["max"] != 409.0 {
			t.Errorf("agent envelope should carry the summary, got:\n%s", out)
		}
	})

	t.Run("summary is visible to --jq in agent mode", func(t *testing.T) {
		out := run(t, DQLExecuteOptions{
			OutputFormat: "json",
			AgentMode:    true,
			JQFilter:     ".records[0].cpu.max",
			Series:       output.SeriesMode{Kind: output.SeriesSummary},
		})
		if !strings.Contains(out, `"result":409`) {
			t.Errorf("jq should filter the summarized records, got:\n%s", out)
		}
	})

	t.Run("summary in a table is one readable cell", func(t *testing.T) {
		out := run(t, DQLExecuteOptions{OutputFormat: "table", Series: output.SeriesMode{Kind: output.SeriesSummary}})
		if strings.Contains(out, "<60 items>") || !strings.Contains(out, "max=409") {
			t.Errorf("table should render the summary instead of <60 items>:\n%s", out)
		}
	})

	t.Run("downsample keeps the spike and rescales the interval", func(t *testing.T) {
		out := run(t, DQLExecuteOptions{OutputFormat: "json", Series: output.SeriesMode{Kind: output.SeriesDownsample, Points: 10}})
		var doc struct {
			Records []map[string]interface{} `json:"records"`
		}
		if err := json.Unmarshal([]byte(out), &doc); err != nil {
			t.Fatalf("invalid json: %v\n%s", err, out)
		}
		cpu := doc.Records[0]["cpu"].([]interface{})
		if len(cpu) > 10 || !strings.Contains(out, "409.123456789") {
			t.Errorf("downsample:10 should keep the spike in at most 10 points, got %d:\n%s", len(cpu), out)
		}
		if doc.Records[0]["interval"] != "360000000000" {
			t.Errorf("interval = %v, want 6 minutes", doc.Records[0]["interval"])
		}
	})

	t.Run("precision rounds full output", func(t *testing.T) {
		out := run(t, DQLExecuteOptions{OutputFormat: "json", Precision: 3})
		if strings.Contains(out, "3.10276124773992") || !strings.Contains(out, "3.1") || !strings.Contains(out, "409") {
			t.Errorf("--precision 3 should round every point:\n%s", out)
		}
	})
}

// agentEnvelope decodes the parts of an agent envelope these tests assert on.
type agentEnvelope struct {
	OK     bool `json:"ok"`
	Result struct {
		Records []map[string]interface{} `json:"records"`
	} `json:"result"`
	Context struct {
		Suggestions []string `json:"suggestions"`
	} `json:"context"`
}

func seriesSuggestions(env agentEnvelope) []string {
	var out []string
	for _, s := range env.Context.Suggestions {
		if strings.Contains(s, "--series") || strings.Contains(s, "--precision") {
			out = append(out, s)
		}
	}
	return out
}

func TestDQLExecutor_AgentDefaultSeriesSuggestion(t *testing.T) {
	clearAIAgentEnvVars(t)
	executor := newTimeseriesExecutor(t)
	run := func(t *testing.T, opts DQLExecuteOptions) agentEnvelope {
		t.Helper()
		opts.OutputFormat = "table" // agent mode leaves -o at its default
		opts.AgentMode = true
		opts.Spill = SpillOptions{Mode: SpillAuto, Threshold: 1 << 20, Dir: t.TempDir(), Format: "json"}
		out := captureStdout(t, func() {
			if err := executor.ExecuteWithContext(context.Background(), "timeseries cpu=avg(x)", opts); err != nil {
				t.Fatalf("execute: %v", err)
			}
		})
		var env agentEnvelope
		if err := json.Unmarshal(out, &env); err != nil {
			t.Fatalf("not an envelope: %v\n%s", err, out)
		}
		return env
	}

	t.Run("defaulted summary and rounding name both opt-outs in one suggestion", func(t *testing.T) {
		env := run(t, DQLExecuteOptions{
			Series: output.SeriesMode{Kind: output.SeriesSummary}, SeriesDefaulted: true,
			Precision: AgentDefaultPrecision, PrecisionDefaulted: true,
		})
		got := seriesSuggestions(env)
		if len(got) != 1 || !strings.Contains(got[0], "--series=full") || !strings.Contains(got[0], "--precision 0") {
			t.Errorf("want one suggestion naming --series=full and --precision 0, got %q", got)
		}
	})

	t.Run("explicit summary adds no suggestion", func(t *testing.T) {
		env := run(t, DQLExecuteOptions{Series: output.SeriesMode{Kind: output.SeriesSummary}, Precision: AgentDefaultPrecision})
		if got := seriesSuggestions(env); len(got) != 0 {
			t.Errorf("an explicit choice needs no opt-out hint, got %q", got)
		}
	})

	t.Run("defaulted rounding alone names --precision 0", func(t *testing.T) {
		env := run(t, DQLExecuteOptions{Precision: AgentDefaultPrecision, PrecisionDefaulted: true})
		got := seriesSuggestions(env)
		if len(got) != 1 || !strings.Contains(got[0], "--precision 0") || strings.Contains(got[0], "--series") {
			t.Errorf("want one --precision 0 suggestion, got %q", got)
		}
	})

	t.Run("full and precision 0 restore the raw points with no suggestion", func(t *testing.T) {
		env := run(t, DQLExecuteOptions{})
		if _, ok := env.Result.Records[0]["cpu"].([]interface{}); !ok {
			t.Fatalf("cpu should be the raw array, got %#v", env.Result.Records[0]["cpu"])
		}
		if got := seriesSuggestions(env); len(got) != 0 {
			t.Errorf("unexpected suggestion %q", got)
		}
	})

	t.Run("defaults that change nothing add no suggestion", func(t *testing.T) {
		// No timeseries and a float already within 4 significant digits.
		plain := newRecordsExecutor(t, []map[string]interface{}{{"host": "web-01", "avg": 2.5, "count": "42"}})
		out := captureStdout(t, func() {
			if err := plain.ExecuteWithContext(context.Background(), "fetch logs | summarize avg(x)", DQLExecuteOptions{
				OutputFormat: "table", AgentMode: true,
				Spill:  SpillOptions{Mode: SpillAuto, Threshold: 1 << 20, Dir: t.TempDir(), Format: "json"},
				Series: output.SeriesMode{Kind: output.SeriesSummary}, SeriesDefaulted: true,
				Precision: AgentDefaultPrecision, PrecisionDefaulted: true,
			}); err != nil {
				t.Fatalf("execute: %v", err)
			}
		})
		var env agentEnvelope
		if err := json.Unmarshal(out, &env); err != nil {
			t.Fatalf("not an envelope: %v\n%s", err, out)
		}
		if got := seriesSuggestions(env); len(got) != 0 {
			t.Errorf("nothing was summarized or rounded, yet got %q", got)
		}
	})

	t.Run("defaulted summary without defaulted rounding names --series=full only", func(t *testing.T) {
		env := run(t, DQLExecuteOptions{Series: output.SeriesMode{Kind: output.SeriesSummary}, SeriesDefaulted: true})
		got := seriesSuggestions(env)
		if len(got) != 1 || !strings.Contains(got[0], "--series=full") || strings.Contains(got[0], "--precision") {
			t.Errorf("want a --series=full-only suggestion, got %q", got)
		}
	})

	t.Run("defaulted summary of exact values names --series=full only", func(t *testing.T) {
		// Integer points and exact statistics: nothing was rounded, so the
		// hint must not claim it, and --series=full alone restores the points.
		exact := newRecordsExecutor(t, []map[string]interface{}{{
			"cpu":      []interface{}{1.0, 2.0, 4.0, 8.0},
			"interval": "60000000000",
			"timeframe": map[string]interface{}{
				"start": "2026-01-01T12:00:00.000000000Z",
				"end":   "2026-01-01T12:04:00.000000000Z",
			},
		}})
		out := captureStdout(t, func() {
			if err := exact.ExecuteWithContext(context.Background(), "timeseries cpu=sum(x)", DQLExecuteOptions{
				OutputFormat: "table", AgentMode: true,
				Spill:  SpillOptions{Mode: SpillAuto, Threshold: 1 << 20, Dir: t.TempDir(), Format: "json"},
				Series: output.SeriesMode{Kind: output.SeriesSummary}, SeriesDefaulted: true,
				Precision: AgentDefaultPrecision, PrecisionDefaulted: true,
			}); err != nil {
				t.Fatalf("execute: %v", err)
			}
		})
		var env agentEnvelope
		if err := json.Unmarshal(out, &env); err != nil {
			t.Fatalf("not an envelope: %v\n%s", err, out)
		}
		got := seriesSuggestions(env)
		if len(got) != 1 || strings.Contains(got[0], "rounded") || strings.Contains(got[0], "--precision") {
			t.Errorf("want a --series=full-only suggestion, got %q", got)
		}
	})

	t.Run("defaulted suggestion rides the --jq envelope too", func(t *testing.T) {
		out := captureStdout(t, func() {
			if err := executor.ExecuteWithContext(context.Background(), "timeseries cpu=avg(x)", DQLExecuteOptions{
				OutputFormat: "table", AgentMode: true, JQFilter: ".records[0].cpu.max",
				Series: output.SeriesMode{Kind: output.SeriesSummary}, SeriesDefaulted: true,
			}); err != nil {
				t.Fatalf("execute: %v", err)
			}
		})
		if !strings.Contains(string(out), "--series=full") {
			t.Errorf("--jq envelope lost the opt-out hint:\n%s", out)
		}
	})
}

// TestDQLExecutor_AgentDefaultSeriesWithAutoFormat pins the combined agent-mode
// default for `query` with no -o: -o auto plus the series defaults. Both
// defaults apply, each lossy one names its own opt-out, and the series opt-out
// restores the -o auto output byte for byte.
func TestDQLExecutor_AgentDefaultSeriesWithAutoFormat(t *testing.T) {
	clearAIAgentEnvVars(t)
	spillDir := t.TempDir()
	run := func(series output.SeriesMode, precision int, defaulted bool) []byte {
		executor := newTimeseriesExecutor(t)
		return captureStdout(t, func() {
			if err := executor.ExecuteWithContext(context.Background(), "timeseries cpu=avg(x)", DQLExecuteOptions{
				OutputFormat: "auto", AutoFormatByDefault: true, AgentMode: true,
				Spill:  SpillOptions{Mode: SpillAuto, Threshold: 1 << 20, Dir: spillDir, Format: "json"},
				Series: series, SeriesDefaulted: defaulted,
				Precision: precision, PrecisionDefaulted: defaulted,
			}); err != nil {
				t.Fatalf("execute: %v", err)
			}
		})
	}
	type autoEnvelope struct {
		Result struct {
			Records string `json:"records"`
		} `json:"result"`
		Context struct {
			Format      string   `json:"format"`
			Suggestions []string `json:"suggestions"`
		} `json:"context"`
	}
	decode := func(out []byte) autoEnvelope {
		var env autoEnvelope
		if err := json.Unmarshal(out, &env); err != nil {
			t.Fatalf("not an auto envelope: %v\n%s", err, out)
		}
		return env
	}
	count := func(suggestions []string, needle string) int {
		n := 0
		for _, s := range suggestions {
			if strings.Contains(s, needle) {
				n++
			}
		}
		return n
	}

	t.Run("both defaults apply and each names its opt-out", func(t *testing.T) {
		env := decode(run(output.SeriesMode{Kind: output.SeriesSummary}, AgentDefaultPrecision, true))
		if env.Context.Format != "yaml" || !strings.Contains(env.Result.Records, "spark:") || strings.Contains(env.Result.Records, "3.10276124773992") {
			t.Errorf("auto should encode the summarized records, got format %q:\n%s", env.Context.Format, env.Result.Records)
		}
		if count(env.Context.Suggestions, "--series=full --precision 0") != 1 || count(env.Context.Suggestions, "-o json") != 1 {
			t.Errorf("want one series and one -o json suggestion, got %q", env.Context.Suggestions)
		}
	})

	t.Run("series opt-out restores the -o auto output exactly", func(t *testing.T) {
		optOut := run(output.SeriesMode{Kind: output.SeriesFull}, 0, false)
		autoOnly := captureStdout(t, func() {
			if err := newTimeseriesExecutor(t).ExecuteWithContext(context.Background(), "timeseries cpu=avg(x)", DQLExecuteOptions{
				OutputFormat: "auto", AutoFormatByDefault: true, AgentMode: true,
				Spill: SpillOptions{Mode: SpillAuto, Threshold: 1 << 20, Dir: spillDir, Format: "json"},
			}); err != nil {
				t.Fatalf("execute: %v", err)
			}
		})
		if string(optOut) != string(autoOnly) {
			t.Errorf("--series=full --precision 0 changed the output:\n%s\nvs\n%s", optOut, autoOnly)
		}
		if env := decode(optOut); count(env.Context.Suggestions, "--series") != 0 || strings.Contains(env.Result.Records, "spark:") {
			t.Errorf("opt-out should carry raw arrays and no series hint, got %q", env.Context.Suggestions)
		}
	})
}

// TestDQLExecutor_AgentDefaultSeriesWithCompact pins the agent-mode series
// defaults composing with the agent-mode --compact default. Series run first,
// so compaction hoists what a `by:` result shares (timeframe, interval) into
// result.constant and keeps each row's summary; a summary that is the same in
// every row is hoisted whole, and constant merged with a row is still that
// row's summary.
func TestDQLExecutor_AgentDefaultSeriesWithCompact(t *testing.T) {
	clearAIAgentEnvVars(t)
	series := func(offset float64) []interface{} {
		values := make([]interface{}, 60)
		for i := range values {
			values[i] = 3.10276124773992 + offset + float64(i%5)
		}
		return values
	}
	timeframe := map[string]interface{}{
		"start": "2026-01-01T12:00:00.000000000Z",
		"end":   "2026-01-01T13:00:00.000000000Z",
	}
	rows := func(second float64) []map[string]interface{} {
		return []map[string]interface{}{
			{"host.name": "web-01", "cpu": series(0), "interval": "60000000000", "timeframe": timeframe},
			{"host.name": "web-02", "cpu": series(second), "interval": "60000000000", "timeframe": timeframe},
		}
	}
	type envelope struct {
		Result struct {
			Constant map[string]interface{}   `json:"constant"`
			Records  []map[string]interface{} `json:"records"`
		} `json:"result"`
		Context struct {
			Suggestions []string `json:"suggestions"`
		} `json:"context"`
	}
	run := func(t *testing.T, records []map[string]interface{}) envelope {
		t.Helper()
		out := captureStdout(t, func() {
			if err := newRecordsExecutor(t, records).ExecuteWithContext(context.Background(), "timeseries cpu=avg(x), by:{host.name}", DQLExecuteOptions{
				OutputFormat: "json", AgentMode: true, Compact: true,
				Spill:  SpillOptions{Mode: SpillAuto, Threshold: 1 << 20, Dir: t.TempDir(), Format: "json"},
				Series: output.SeriesMode{Kind: output.SeriesSummary}, SeriesDefaulted: true,
				Precision: AgentDefaultPrecision, PrecisionDefaulted: true,
			}); err != nil {
				t.Fatalf("execute: %v", err)
			}
		})
		var env envelope
		if err := json.Unmarshal(out, &env); err != nil {
			t.Fatalf("not an envelope: %v\n%s", err, out)
		}
		return env
	}
	isSummary := func(v interface{}) bool {
		m, ok := v.(map[string]interface{})
		return ok && m["spark"] != nil && m["n"] != nil
	}

	t.Run("both defaults apply and each names its opt-out", func(t *testing.T) {
		env := run(t, rows(100))
		if env.Result.Constant["interval"] != "60000000000" || env.Result.Constant["timeframe"] == nil {
			t.Errorf("shared timeframe/interval should be hoisted, constant = %v", env.Result.Constant)
		}
		if _, ok := env.Result.Constant["cpu"]; ok {
			t.Errorf("differing summaries must stay in the rows, constant = %v", env.Result.Constant)
		}
		if len(env.Result.Records) != 2 {
			t.Fatalf("records = %v, want 2 rows", env.Result.Records)
		}
		for i, r := range env.Result.Records {
			if !isSummary(r["cpu"]) || r["timeframe"] != nil {
				t.Errorf("row %d = %v, want a cpu summary without the hoisted columns", i, r)
			}
		}
		if !strings.Contains(strings.Join(env.Context.Suggestions, "\n"), "--series=full --precision 0") {
			t.Errorf("series opt-out missing: %q", env.Context.Suggestions)
		}
	})

	t.Run("a summary equal in every row is hoisted whole", func(t *testing.T) {
		env := run(t, rows(0))
		if !isSummary(env.Result.Constant["cpu"]) {
			t.Fatalf("identical summaries should be hoisted, constant = %v", env.Result.Constant)
		}
		for i, r := range env.Result.Records {
			if len(r) != 1 || r["host.name"] == nil {
				t.Errorf("row %d = %v, want only host.name", i, r)
			}
		}
		// The hoisted value is exactly the per-row summary without --compact.
		full := captureStdout(t, func() {
			if err := newRecordsExecutor(t, rows(0)).ExecuteWithContext(context.Background(), "timeseries cpu=avg(x), by:{host.name}", DQLExecuteOptions{
				OutputFormat: "json", AgentMode: true,
				Spill:  SpillOptions{Mode: SpillAuto, Threshold: 1 << 20, Dir: t.TempDir(), Format: "json"},
				Series: output.SeriesMode{Kind: output.SeriesSummary}, SeriesDefaulted: true,
				Precision: AgentDefaultPrecision, PrecisionDefaulted: true,
			}); err != nil {
				t.Fatalf("execute: %v", err)
			}
		})
		var plain envelope
		if err := json.Unmarshal(full, &plain); err != nil {
			t.Fatalf("not an envelope: %v\n%s", err, full)
		}
		if !reflect.DeepEqual(plain.Result.Records[1]["cpu"], env.Result.Constant["cpu"]) {
			t.Errorf("hoisted summary %v differs from the row's %v", env.Result.Constant["cpu"], plain.Result.Records[1]["cpu"])
		}
	})
}

// TestDQLExecutor_AgentDefaultSeriesWithFieldCap runs the series defaults with
// the agent-mode field cap and compaction. Clipping comes last, so a summary
// (whose strings are a <=24-cell sparkline and timestamps) passes the default
// 500-char cap unchanged, in the rows and in result.constant, while a long
// string dimension next to it is clipped.
func TestDQLExecutor_AgentDefaultSeriesWithFieldCap(t *testing.T) {
	clearAIAgentEnvVars(t)
	values := func(offset float64) []interface{} {
		v := make([]interface{}, 60)
		for i := range v {
			v[i] = 3.10276124773992 + offset + float64(i%5)
		}
		return v
	}
	timeframe := map[string]interface{}{
		"start": "2026-01-01T12:00:00.000000000Z",
		"end":   "2026-01-01T13:00:00.000000000Z",
	}
	records := []map[string]interface{}{
		{"host.name": "web-01", "note": "a" + strings.Repeat("n", 800), "cpu": values(0), "mem": values(0), "interval": "60000000000", "timeframe": timeframe},
		{"host.name": "web-02", "note": "b" + strings.Repeat("n", 800), "cpu": values(100), "mem": values(0), "interval": "60000000000", "timeframe": timeframe},
	}
	type envelope struct {
		Result struct {
			Constant map[string]interface{}   `json:"constant"`
			Records  []map[string]interface{} `json:"records"`
		} `json:"result"`
		Context struct {
			Truncated       bool     `json:"truncated"`
			TruncatedFields []string `json:"truncated_fields"`
		} `json:"context"`
	}
	run := func(t *testing.T, maxChars int) envelope {
		t.Helper()
		out := captureStdout(t, func() {
			if err := newRecordsExecutor(t, records).ExecuteWithContext(context.Background(), "timeseries cpu=avg(x), mem=avg(y), by:{host.name, note}", DQLExecuteOptions{
				OutputFormat: "json", AgentMode: true, Compact: true, MaxFieldChars: maxChars,
				Spill:  SpillOptions{Mode: SpillAuto, Threshold: 1 << 20, Dir: t.TempDir(), Format: "json"},
				Series: output.SeriesMode{Kind: output.SeriesSummary}, SeriesDefaulted: true,
				Precision: AgentDefaultPrecision, PrecisionDefaulted: true,
			}); err != nil {
				t.Fatalf("execute: %v", err)
			}
		})
		var env envelope
		if err := json.Unmarshal(out, &env); err != nil {
			t.Fatalf("not an envelope: %v\n%s", err, out)
		}
		return env
	}

	capped, full := run(t, output.DefaultAgentMaxFieldChars), run(t, 0)
	if !reflect.DeepEqual(capped.Result.Constant["mem"], full.Result.Constant["mem"]) || capped.Result.Constant["mem"] == nil {
		t.Errorf("hoisted summary changed by the cap: %v vs %v", capped.Result.Constant["mem"], full.Result.Constant["mem"])
	}
	for i := range capped.Result.Records {
		if !reflect.DeepEqual(capped.Result.Records[i]["cpu"], full.Result.Records[i]["cpu"]) {
			t.Errorf("row %d summary changed by the cap: %v vs %v", i, capped.Result.Records[i]["cpu"], full.Result.Records[i]["cpu"])
		}
		if note, _ := capped.Result.Records[i]["note"].(string); !strings.HasSuffix(note, "…(+301 chars)") {
			t.Errorf("row %d note = %.40q…, want clipped", i, note)
		}
	}
	if !capped.Context.Truncated || !reflect.DeepEqual(capped.Context.TruncatedFields, []string{"note"}) {
		t.Errorf("context truncated=%v fields=%v, want only note", capped.Context.Truncated, capped.Context.TruncatedFields)
	}
	if full.Context.Truncated {
		t.Error("--max-field-chars 0 must not clip")
	}
}
