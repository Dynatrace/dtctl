package cmd

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/dynatrace-oss/dtctl/pkg/output"
	"github.com/dynatrace-oss/dtctl/pkg/resources/analyzer"
)

// forecastFixture is a synthetic GenericForecastAnalyzer result: the echoed
// input, one analyzed series and one predicted series, each a DQL result.
func forecastFixture(t *testing.T) *analyzer.ExecuteResult {
	t.Helper()
	const raw = `{"result":{"resultId":"r-1","resultStatus":"SUCCESSFUL","executionStatus":"COMPLETED",
	 "input":{"timeSeriesData":"timeseries avg(dt.host.cpu.usage)","forecastHorizon":3},
	 "output":[{"analysisStatus":"OK","forecastQualityAssessment":"VALID",
	   "analyzedTimeSeriesQuery":{"expression":{
	     "metadata":{"metrics":[]},
	     "records":[{"interval":"60000000000","timeframe":{"start":"2026-01-01T00:00:00Z","end":"2026-01-01T00:06:00Z"},
	                 "avg(dt.host.cpu.usage)":[1.23456,2.34567,null,4.56789,5.67891,6.78912],"empty":null}],
	     "types":[{"indexRange":[0,0],"mappings":{}}]}},
	   "timeSeriesDataWithPredictions":{
	     "metadata":{"metrics":[{"fieldName":"dt.davis.forecast:point"}]},
	     "records":[{"interval":"60000000000","timeframe":{"start":"2026-01-01T00:06:00Z","end":"2026-01-01T00:09:00Z"},
	                 "dt.davis.forecast:point":[7.11111,8.22222,9.33333]}],
	     "types":[{"indexRange":[0,0],"mappings":{}}]}}]}}`
	var r analyzer.ExecuteResult
	require.NoError(t, json.Unmarshal([]byte(raw), &r))
	return &r
}

func TestShapeForAgentSummary(t *testing.T) {
	shaped, eff := shapeAnalyzerForAgent(forecastFixture(t), output.SeriesMode{Kind: output.SeriesSummary}, 4)
	assert.True(t, eff.Summarized)
	assert.False(t, eff.NoFindings)

	res := shaped["result"].(map[string]interface{})
	_, hasInput := res["input"]
	assert.False(t, hasInput, "echoed input is dropped")

	out := res["output"].([]interface{})[0].(map[string]interface{})
	expr := out["analyzedTimeSeriesQuery"].(map[string]interface{})["expression"].(map[string]interface{})
	_, hasTypes := expr["types"]
	assert.False(t, hasTypes, "DQL types are dropped")
	rec := expr["records"].([]interface{})[0].(map[string]interface{})
	_, hasEmpty := rec["empty"]
	assert.False(t, hasEmpty, "null record fields are dropped")
	sum, ok := rec["avg(dt.host.cpu.usage)"].(map[string]interface{})
	require.True(t, ok, "series replaced by a summary: %v", rec)
	assert.EqualValues(t, 6, sum["n"])

	pred := out["timeSeriesDataWithPredictions"].(map[string]interface{})["records"].([]interface{})[0].(map[string]interface{})
	_, ok = pred["dt.davis.forecast:point"].(map[string]interface{})
	assert.True(t, ok, "predicted series summarized too")

	// The source is untouched.
	assert.Len(t, forecastFixture(t).Result.Input, 2)
}

func TestShapeForAgentFullKeepsPoints(t *testing.T) {
	shaped, eff := shapeAnalyzerForAgent(forecastFixture(t), output.SeriesMode{Kind: output.SeriesFull}, 0)
	assert.False(t, eff.Summarized)
	assert.False(t, eff.Rounded)
	out := shaped["result"].(map[string]interface{})["output"].([]interface{})[0].(map[string]interface{})
	rec := out["analyzedTimeSeriesQuery"].(map[string]interface{})["expression"].(map[string]interface{})["records"].([]interface{})[0].(map[string]interface{})
	pts := rec["avg(dt.host.cpu.usage)"].([]interface{})
	assert.Len(t, pts, 6)
	assert.Nil(t, pts[2], "gaps inside a series stay")
	assert.Equal(t, 1.23456, pts[0])
}

func TestShapeForAgentNoFindings(t *testing.T) {
	r := &analyzer.ExecuteResult{Result: &analyzer.AnalyzerResult{ResultID: "r-2", ResultStatus: "SUCCESSFUL", ExecutionStatus: "COMPLETED",
		Input: map[string]interface{}{"threshold": 1}}}
	shaped, eff := shapeAnalyzerForAgent(r, output.SeriesMode{Kind: output.SeriesSummary}, 4)
	assert.True(t, eff.NoFindings)
	res := shaped["result"].(map[string]interface{})
	_, hasOutput := res["output"]
	assert.False(t, hasOutput)
	assert.Equal(t, "SUCCESSFUL", res["resultStatus"])
}

func TestShapeForAgentKeepsPollHandle(t *testing.T) {
	r := &analyzer.ExecuteResult{RequestToken: "tok-1", TTLInSeconds: 120,
		Result: &analyzer.AnalyzerResult{ResultStatus: "PENDING", ExecutionStatus: "RUNNING"}}
	shaped, _ := shapeAnalyzerForAgent(r, output.SeriesMode{Kind: output.SeriesSummary}, 4)
	assert.Equal(t, "tok-1", shaped["requestToken"])
	assert.EqualValues(t, 120, shaped["ttlInSeconds"])
}
