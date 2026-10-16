package cmd

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// analyzerServer answers every :execute with the given completed result body.
func analyzerServer(t *testing.T, body string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if !strings.HasSuffix(r.URL.Path, ":execute") {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	isolatedConfig(t, safetyLevelConfig(srv.URL, "readonly"))
	return srv
}

const forecastBody = `{"result":{"resultId":"r-1","resultStatus":"SUCCESSFUL","executionStatus":"COMPLETED",
 "input":{"timeSeriesData":"timeseries avg(dt.host.cpu.usage)"},
 "output":[{"analysisStatus":"OK","timeSeriesDataWithPredictions":{
   "metadata":{"metrics":[]},
   "records":[{"interval":"60000000000","timeframe":{"start":"2026-01-01T00:00:00Z","end":"2026-01-01T00:05:00Z"},
               "dt.davis.forecast:point":[1.123456,2.123456,3.123456,4.123456,5.123456]}],
   "types":[{"indexRange":[0,0],"mappings":{}}]}}]}}`

func execAnalyzerArgs(extra ...string) []string {
	return append([]string{"exec", "analyzer", "dt.statistics.GenericForecastAnalyzer", "--input", `{"timeSeriesData":"timeseries avg(dt.host.cpu.usage)"}`}, extra...)
}

func TestExecAnalyzerAgentShapesResult(t *testing.T) {
	analyzerServer(t, forecastBody)
	code, env := runAgentEnvelope(t, execAnalyzerArgs())
	require.Equal(t, 0, code)
	require.Equal(t, true, env["ok"], env)

	res := env["result"].(map[string]any)["result"].(map[string]any)
	_, hasInput := res["input"]
	assert.False(t, hasInput)
	rec := res["output"].([]any)[0].(map[string]any)["timeSeriesDataWithPredictions"].(map[string]any)["records"].([]any)[0].(map[string]any)
	_, summarized := rec["dt.davis.forecast:point"].(map[string]any)
	assert.True(t, summarized, "series summarized by default: %v", rec)

	hints, _ := env["context"].(map[string]any)["suggestions"].([]any)
	joined := anyStrings(hints)
	assert.Contains(t, joined, "--series=full")
	assert.NotContains(t, joined, "empty output")
}

func TestExecAnalyzerAgentSeriesFull(t *testing.T) {
	analyzerServer(t, forecastBody)
	code, env := runAgentEnvelope(t, execAnalyzerArgs("--series=full", "--precision", "0"))
	require.Equal(t, 0, code)
	res := env["result"].(map[string]any)["result"].(map[string]any)
	rec := res["output"].([]any)[0].(map[string]any)["timeSeriesDataWithPredictions"].(map[string]any)["records"].([]any)[0].(map[string]any)
	pts, ok := rec["dt.davis.forecast:point"].([]any)
	require.True(t, ok)
	assert.Equal(t, 1.123456, pts[0])
	hints, _ := env["context"].(map[string]any)["suggestions"].([]any)
	assert.NotContains(t, anyStrings(hints), "--series=full")
}

func TestExecAnalyzerAgentNoFindings(t *testing.T) {
	analyzerServer(t, `{"result":{"resultId":"r-2","resultStatus":"SUCCESSFUL","executionStatus":"COMPLETED","input":{"threshold":1},"output":[]}}`)
	code, env := runAgentEnvelope(t, execAnalyzerArgs())
	require.Equal(t, 0, code)
	hints, _ := env["context"].(map[string]any)["suggestions"].([]any)
	assert.Contains(t, anyStrings(hints), "empty output")
}

func TestExecAnalyzerPlainOutputUnchanged(t *testing.T) {
	analyzerServer(t, forecastBody)
	code, out := captureRun(t, execAnalyzerArgs(), RunOptions{})
	require.Equal(t, 0, code)
	assert.Contains(t, out, `"input"`, "non-agent output keeps the raw result")
	assert.Contains(t, out, `"types"`)
}

func anyStrings(in []any) string {
	var sb strings.Builder
	for _, v := range in {
		if s, ok := v.(string); ok {
			sb.WriteString(s)
			sb.WriteByte('\n')
		}
	}
	return sb.String()
}
