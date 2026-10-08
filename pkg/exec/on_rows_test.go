package exec

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/dynatrace-oss/dtctl/pkg/client"
)

// OnRows reports the record count once, before the rows reach stdout on the
// buffered path, so a zero-rows hint can precede an empty table.
func TestExecuteWithContext_OnRowsBuffered(t *testing.T) {
	for _, records := range [][]map[string]interface{}{
		{},
		{{"content": "a"}, {"content": "b"}},
	} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(DQLQueryResponse{State: "SUCCEEDED", Result: &DQLResult{Records: records}})
		}))
		c, err := client.NewForTesting(server.URL, "test-token")
		require.NoError(t, err)

		var out, errOut bytes.Buffer
		var calls []int
		printedBefore := -1
		opts := DQLExecuteOptions{OutputFormat: "json", OnRows: func(n int) {
			calls = append(calls, n)
			printedBefore = out.Len()
		}}
		require.NoError(t, NewDQLExecutor(c).WithStreams(&out, &errOut).ExecuteWithContext(context.Background(), "fetch logs", opts))
		server.Close()

		assert.Equal(t, []int{len(records)}, calls, "called once with the record count")
		assert.Zero(t, printedBefore, "called before anything was printed")
		assert.NotZero(t, out.Len())
	}
}

// On the streaming path the rows are already written when the count is
// known; OnRows still fires once, with every row counted.
func TestStreamCollector_OnRowsStreamed(t *testing.T) {
	records := bigRecords(250)
	result, _ := sampleResult(false)
	result.Records = nil
	var calls []int
	opts := DQLExecuteOptions{
		AgentMode: true, Compact: true,
		Spill:  SpillOptions{Mode: SpillAlways, Threshold: 50 << 10, Dir: t.TempDir(), Format: "jsonl"},
		OnRows: func(n int) { calls = append(calls, n) },
	}
	var out, errOut bytes.Buffer
	e := (&DQLExecutor{}).WithStreams(&out, &errOut)
	plan, ok := e.planStream(opts)
	require.True(t, ok)
	col := feed(t, e, plan, opts, records)
	defer col.abort()

	require.NoError(t, col.finish("fetch logs", result, opts))
	assert.Equal(t, []int{len(records)}, calls)
}

// A result that stays small takes the buffered replay, which reports its
// rows the same way.
func TestStreamCollector_OnRowsBufferedReplay(t *testing.T) {
	result, _ := sampleResult(false)
	result.Records = nil
	var calls []int
	opts := DQLExecuteOptions{
		AgentMode: true, Compact: true,
		Spill:  SpillOptions{Mode: SpillAuto, Threshold: 50 << 10, Dir: t.TempDir(), Format: "jsonl"},
		OnRows: func(n int) { calls = append(calls, n) },
	}
	var out, errOut bytes.Buffer
	e := (&DQLExecutor{}).WithStreams(&out, &errOut)
	plan, ok := e.planStream(opts)
	require.True(t, ok)
	col := feed(t, e, plan, opts, bigRecords(3))
	defer col.abort()

	require.NoError(t, col.finish("fetch logs", result, opts))
	assert.Equal(t, []int{3}, calls)
}
