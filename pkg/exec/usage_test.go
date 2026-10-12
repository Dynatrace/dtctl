package exec

import (
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/dynatrace-oss/dtctl/pkg/client"
	"github.com/dynatrace-oss/dtctl/pkg/usage"
)

type fakeUsageRecorder struct {
	mu      sync.Mutex
	samples []usage.Sample
}

func (f *fakeUsageRecorder) RecordQuery(s usage.Sample) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.samples = append(f.samples, s)
}

func usageTestServer(t *testing.T, pollBody string) *client.Client {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/platform/storage/query/v1/query:execute":
			// A running query that has already scanned something.
			_, _ = w.Write([]byte(`{"state":"RUNNING","requestToken":"tok","progress":40,
				"result":{"records":[],"metadata":{"grail":{"scannedBytes":500,"scannedRecords":7}}}}`))
		case "/platform/storage/query/v1/query:poll":
			_, _ = w.Write([]byte(pollBody))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(server.Close)
	c, err := client.NewForTesting(server.URL, "test-token")
	if err != nil {
		t.Fatalf("client: %v", err)
	}
	return c
}

func TestDQLExecutor_RecordsUsageOnSuccess(t *testing.T) {
	c := usageTestServer(t, `{"state":"SUCCEEDED","result":{"records":[{"a":1}],
		"metadata":{"grail":{"scannedBytes":2048,"scannedRecords":30,"scannedDataPoints":9}}}}`)
	rec := &fakeUsageRecorder{}
	TrackUsage(c, rec)

	if _, err := NewDQLExecutor(c).ExecuteQuery("fetch logs"); err != nil {
		t.Fatalf("query: %v", err)
	}
	if len(rec.samples) != 1 {
		t.Fatalf("recorded %d samples, want 1", len(rec.samples))
	}
	want := usage.Sample{ScannedBytes: 2048, ScannedRecords: 30, ScannedDataPoints: 9}
	if rec.samples[0] != want {
		t.Errorf("sample = %+v, want %+v (the terminal metadata is authoritative)", rec.samples[0], want)
	}
}

func TestDQLExecutor_RecordsUsageOnFailure(t *testing.T) {
	c := usageTestServer(t, `{"state":"FAILED"}`)
	rec := &fakeUsageRecorder{}

	_, err := NewDQLExecutor(c).WithUsageRecorder(rec).ExecuteQuery("fetch logs")
	if err == nil {
		t.Fatal("expected the FAILED state to surface as an error")
	}
	if len(rec.samples) != 1 {
		t.Fatalf("recorded %d samples, want 1: a failed query still counts", len(rec.samples))
	}
	want := usage.Sample{Failed: true, ScannedBytes: 500, ScannedRecords: 7}
	if rec.samples[0] != want {
		t.Errorf("sample = %+v, want %+v (the last live totals)", rec.samples[0], want)
	}
}

func TestDQLExecutor_NoRecorderNoUsage(t *testing.T) {
	c := usageTestServer(t, `{"state":"SUCCEEDED","result":{"records":[]}}`)
	other, err := client.NewForTesting("http://example.invalid", "x")
	if err != nil {
		t.Fatal(err)
	}
	rec := &fakeUsageRecorder{}
	TrackUsage(other, rec) // bound to a different client

	if _, err := NewDQLExecutor(c).ExecuteQuery("fetch logs"); err != nil {
		t.Fatalf("query: %v", err)
	}
	if len(rec.samples) != 0 {
		t.Fatalf("a recorder bound to another client recorded %d samples", len(rec.samples))
	}
	if usageRecorderFor(c) != nil {
		t.Fatal("an unbound client must have no recorder")
	}
}
