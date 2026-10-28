//go:build integration
// +build integration

package integration

import (
	"context"
	"reflect"
	"testing"

	sdkquery "github.com/Dynatrace/dtctl/sdk/api/query"
	"github.com/Dynatrace/dtctl/sdk/httpclient"
)

// Live-tenant guard: runs the same query through ExecuteAndPoll and through ExecuteStream
// and requires them to agree on row count and content. Uses "data record(...)" instead of
// "fetch logs" so rows are deterministic and a value-level comparison isn't flaky.
func TestQueryExecuteStream_MatchesAccumulatedResult(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test")
	}

	env := SetupIntegration(t)
	handler := sdkquery.NewHandler(httpclient.Wrap(env.Client.HTTP()))

	const query = `data record(n=1, s="alpha", f=1.5), record(n=2, s="beta", f=2.5), record(n=3, s="gamma", f=3.5)`

	accumulated, err := handler.ExecuteAndPoll(context.Background(), sdkquery.ExecuteRequest{Query: query}, nil)
	if err != nil {
		t.Fatalf("ExecuteAndPoll() error: %v", err)
	}
	wantRecords := accumulated.GetRecords()

	var streamed []map[string]interface{}
	onRecord := func(row map[string]interface{}) error {
		streamed = append(streamed, row)
		return nil
	}

	execResp, err := handler.ExecuteStream(context.Background(), sdkquery.ExecuteRequest{Query: query}, onRecord)
	if err != nil {
		t.Fatalf("ExecuteStream() error: %v", err)
	}

	// A query this small (limit 25) may still complete asynchronously on a
	// busy tenant; poll to completion the same way ExecuteAndPoll would.
	result := execResp
	for result.RequestToken != "" && result.State != sdkquery.StateSucceeded {
		result, err = handler.PollStream(context.Background(), result.RequestToken, 5000, false, onRecord)
		if err != nil {
			t.Fatalf("PollStream() error: %v", err)
		}
	}

	if len(streamed) != len(wantRecords) {
		t.Fatalf("ExecuteStream/PollStream delivered %d rows via callback, ExecuteAndPoll accumulated %d",
			len(streamed), len(wantRecords))
	}
	// decodeResponseStream must never leave the streaming methods'
	// Records/Result.Records populated — every row must have come through
	// the callback instead.
	if result.Result != nil && result.Result.Records != nil {
		t.Errorf("ExecuteStream/PollStream Result.Records = %#v, want nil (rows must arrive via callback only)", result.Result.Records)
	}

	for i := range wantRecords {
		if !reflect.DeepEqual(streamed[i], wantRecords[i]) {
			t.Errorf("row %d: streamed = %#v, want %#v (accumulated)", i, streamed[i], wantRecords[i])
		}
	}
}
