package query

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dynatrace-oss/dtctl/sdk/httpclient"
)

func newTestClient(t *testing.T, handler http.Handler) *httpclient.Client {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	c, err := httpclient.New(srv.URL, httpclient.WithToken("dt0c01.test"))
	if err != nil {
		t.Fatalf("httpclient.New: %v", err)
	}
	return c
}

func TestExecute_Synchronous(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/platform/storage/query/v1/query:execute", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}

		var req ExecuteRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if req.Query != "fetch logs | limit 1" {
			t.Errorf("unexpected query: %s", req.Query)
		}

		resp := Response{
			State: "SUCCEEDED",
			Result: &Result{
				Records: []map[string]interface{}{{"message": "hello"}},
			},
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	})

	h := NewHandler(newTestClient(t, mux))
	result, err := h.Execute(context.Background(), ExecuteRequest{Query: "fetch logs | limit 1"})
	if err != nil {
		t.Fatalf("Execute() error: %v", err)
	}
	if result.State != "SUCCEEDED" {
		t.Errorf("State = %q, want SUCCEEDED", result.State)
	}
	if len(result.GetRecords()) != 1 {
		t.Errorf("got %d records, want 1", len(result.GetRecords()))
	}
}

func TestExecuteAndPoll_EnrichMetricMetadata(t *testing.T) {
	var execEnrich, pollEnrich string
	var polled atomic.Bool

	mux := http.NewServeMux()
	mux.HandleFunc("/platform/storage/query/v1/query:execute", func(w http.ResponseWriter, r *http.Request) {
		execEnrich = r.URL.Query().Get("enrich")
		resp := Response{State: "RUNNING", RequestToken: "tok-enrich"}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusAccepted)
		json.NewEncoder(w).Encode(resp)
	})
	mux.HandleFunc("/platform/storage/query/v1/query:poll", func(w http.ResponseWriter, r *http.Request) {
		pollEnrich = r.URL.Query().Get("enrich")
		polled.Store(true)
		resp := Response{
			State: "SUCCEEDED",
			Result: &Result{
				Metadata: &Metadata{Metrics: []MetricInfo{{MetricKey: "dt.host.cpu.user", Unit: "%"}}},
			},
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	})

	h := NewHandler(newTestClient(t, mux))
	_, err := h.ExecuteAndPoll(context.Background(), ExecuteRequest{Query: "timeseries avg(dt.host.cpu.user)", EnrichMetricMetadata: true}, nil)
	if err != nil {
		t.Fatalf("ExecuteAndPoll() error: %v", err)
	}
	if !polled.Load() {
		t.Fatal("expected poll to be invoked")
	}
	if execEnrich != "metric-metadata" {
		t.Errorf("execute enrich = %q, want metric-metadata", execEnrich)
	}
	if pollEnrich != "metric-metadata" {
		t.Errorf("poll enrich = %q, want metric-metadata", pollEnrich)
	}
}

func TestExecute_NoEnrichByDefault(t *testing.T) {
	var execEnrich string
	mux := http.NewServeMux()
	mux.HandleFunc("/platform/storage/query/v1/query:execute", func(w http.ResponseWriter, r *http.Request) {
		execEnrich = r.URL.Query().Get("enrich")
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(Response{State: "SUCCEEDED", Result: &Result{}})
	})

	h := NewHandler(newTestClient(t, mux))
	if _, err := h.Execute(context.Background(), ExecuteRequest{Query: "fetch logs | limit 1"}); err != nil {
		t.Fatalf("Execute() error: %v", err)
	}
	if execEnrich != "" {
		t.Errorf("enrich param = %q, want empty when EnrichMetricMetadata is false", execEnrich)
	}
}

func TestExecute_AsyncWithPoll(t *testing.T) {
	var pollCount atomic.Int32

	mux := http.NewServeMux()
	mux.HandleFunc("/platform/storage/query/v1/query:execute", func(w http.ResponseWriter, r *http.Request) {
		resp := Response{State: "RUNNING", RequestToken: "tok-123"}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusAccepted)
		json.NewEncoder(w).Encode(resp)
	})
	mux.HandleFunc("/platform/storage/query/v1/query:poll", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("request-token") != "tok-123" {
			t.Errorf("unexpected request-token: %s", r.URL.Query().Get("request-token"))
		}

		n := pollCount.Add(1)
		if n < 2 {
			resp := Response{State: "RUNNING", RequestToken: "tok-123", Progress: 50}
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(resp)
			return
		}

		resp := Response{
			State: "SUCCEEDED",
			Result: &Result{
				Records: []map[string]interface{}{{"key": "value"}},
			},
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	})

	h := NewHandler(newTestClient(t, mux))
	result, err := h.ExecuteAndPoll(context.Background(), ExecuteRequest{Query: "fetch logs"}, nil)
	if err != nil {
		t.Fatalf("ExecuteAndPoll() error: %v", err)
	}
	if result.State != "SUCCEEDED" {
		t.Errorf("State = %q, want SUCCEEDED", result.State)
	}
	if pollCount.Load() < 2 {
		t.Errorf("expected at least 2 polls, got %d", pollCount.Load())
	}
}

// TestExecute_NotStartedIsPolled locks the congested-tenant path: a queued
// query answers execute (and early polls) with state NOT_STARTED, which is
// in-flight, not terminal — returning it as success hands the caller the raw
// {"state":"NOT_STARTED","requestToken":...} envelope with rc 0 (observed
// live three times in a row on a busy dev tenant).
func TestExecute_NotStartedIsPolled(t *testing.T) {
	var pollCount atomic.Int32

	mux := http.NewServeMux()
	mux.HandleFunc("/platform/storage/query/v1/query:execute", func(w http.ResponseWriter, r *http.Request) {
		resp := Response{State: "NOT_STARTED", RequestToken: "tok-ns"}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusAccepted)
		json.NewEncoder(w).Encode(resp)
	})
	mux.HandleFunc("/platform/storage/query/v1/query:poll", func(w http.ResponseWriter, r *http.Request) {
		n := pollCount.Add(1)
		w.Header().Set("Content-Type", "application/json")
		if n == 1 {
			// still queued on the first poll
			json.NewEncoder(w).Encode(Response{State: "NOT_STARTED", RequestToken: "tok-ns"})
			return
		}
		json.NewEncoder(w).Encode(Response{
			State:  "SUCCEEDED",
			Result: &Result{Records: []map[string]interface{}{{"c": "1"}}},
		})
	})

	h := NewHandler(newTestClient(t, mux))
	result, err := h.ExecuteAndPoll(context.Background(), ExecuteRequest{Query: "fetch logs"}, nil)
	if err != nil {
		t.Fatalf("ExecuteAndPoll() error: %v", err)
	}
	if result.State != "SUCCEEDED" {
		t.Errorf("State = %q, want SUCCEEDED (NOT_STARTED must be polled, not returned)", result.State)
	}
	if pollCount.Load() < 2 {
		t.Errorf("expected at least 2 polls, got %d", pollCount.Load())
	}
}

func TestPoll_Success(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/platform/storage/query/v1/query:poll", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		resp := Response{
			State: "SUCCEEDED",
			Result: &Result{
				Records: []map[string]interface{}{{"a": 1}},
			},
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	})

	h := NewHandler(newTestClient(t, mux))
	result, err := h.Poll(context.Background(), "tok-abc", 5000, false)
	if err != nil {
		t.Fatalf("Poll() error: %v", err)
	}
	if result.State != "SUCCEEDED" {
		t.Errorf("State = %q, want SUCCEEDED", result.State)
	}
}

func TestPoll_ErrorReturnsAPIError(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/platform/storage/query/v1/query:poll", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		w.Write([]byte(`{"error":{"message":"jwt expired"}}`))
	})

	h := NewHandler(newTestClient(t, mux))
	_, err := h.Poll(context.Background(), "tok-abc", 5000, false)
	if err == nil {
		t.Fatal("expected error, got nil")
	}

	var apiErr *httpclient.APIError
	if !errorAsAPIError(err, &apiErr) {
		t.Fatalf("expected *httpclient.APIError, got %T: %v", err, err)
	}
	if apiErr.StatusCode != 401 {
		t.Errorf("StatusCode = %d, want 401", apiErr.StatusCode)
	}
}

func TestCancel(t *testing.T) {
	var called atomic.Bool
	mux := http.NewServeMux()
	mux.HandleFunc("/platform/storage/query/v1/query:cancel", func(w http.ResponseWriter, r *http.Request) {
		called.Store(true)
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		if r.URL.Query().Get("request-token") != "tok-cancel" {
			t.Errorf("unexpected request-token: %s", r.URL.Query().Get("request-token"))
		}
		w.WriteHeader(http.StatusOK)
	})

	h := NewHandler(newTestClient(t, mux))
	err := h.Cancel(context.Background(), "tok-cancel")
	if err != nil {
		t.Fatalf("Cancel() error: %v", err)
	}
	if !called.Load() {
		t.Error("cancel endpoint was not called")
	}
}

func TestVerify(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/platform/storage/query/v1/query:verify", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		resp := VerifyResponse{
			Valid:          true,
			CanonicalQuery: "fetch logs",
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	})

	h := NewHandler(newTestClient(t, mux))
	result, err := h.Verify(context.Background(), VerifyRequest{Query: "fetch logs", GenerateCanonicalQuery: true})
	if err != nil {
		t.Fatalf("Verify() error: %v", err)
	}
	if !result.Valid {
		t.Error("expected valid query")
	}
	if result.CanonicalQuery != "fetch logs" {
		t.Errorf("CanonicalQuery = %q, want %q", result.CanonicalQuery, "fetch logs")
	}
}

func TestExecuteAndPoll_Cancellation(t *testing.T) {
	var cancelCalled atomic.Bool

	mux := http.NewServeMux()
	mux.HandleFunc("/platform/storage/query/v1/query:execute", func(w http.ResponseWriter, r *http.Request) {
		resp := Response{State: "RUNNING", RequestToken: "tok-cancel-test"}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusAccepted)
		json.NewEncoder(w).Encode(resp)
	})
	mux.HandleFunc("/platform/storage/query/v1/query:poll", func(w http.ResponseWriter, r *http.Request) {
		// Simulate slow response — give the test time to cancel
		time.Sleep(200 * time.Millisecond)
		resp := Response{State: "RUNNING", RequestToken: "tok-cancel-test"}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	})
	mux.HandleFunc("/platform/storage/query/v1/query:cancel", func(w http.ResponseWriter, r *http.Request) {
		cancelCalled.Store(true)
		w.WriteHeader(http.StatusOK)
	})

	h := NewHandler(newTestClient(t, mux))
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	_, err := h.ExecuteAndPoll(ctx, ExecuteRequest{Query: "fetch logs"}, nil)
	if err == nil {
		t.Fatal("expected error from cancelled context")
	}

	// Give best-effort cancel a moment to complete
	time.Sleep(100 * time.Millisecond)
	if !cancelCalled.Load() {
		t.Error("expected cancel endpoint to be called")
	}
}

// A caller that has already gone away must not start a query at all.
func TestExecuteAndPoll_PreCancelledContextStartsNothing(t *testing.T) {
	var requests atomic.Int32
	h := NewHandler(newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		w.WriteHeader(http.StatusOK)
	})))

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := h.ExecuteAndPoll(ctx, ExecuteRequest{Query: "fetch logs"}, nil)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	if n := requests.Load(); n != 0 {
		t.Errorf("sent %d requests for a cancelled caller, want 0", n)
	}
}

// Cancelling the caller while the initial execute is in flight still lets the
// request token come back, so the query Grail already started is cancelled on
// the backend instead of running on unobserved (#502).
func TestExecuteAndPoll_CancelDuringExecuteCancelsBackendQuery(t *testing.T) {
	var cancelledToken atomic.Value
	var polls atomic.Int32

	mux := http.NewServeMux()
	mux.HandleFunc("/platform/storage/query/v1/query:execute", func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(200 * time.Millisecond) // server-side hold; the caller cancels meanwhile
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusAccepted)
		_ = json.NewEncoder(w).Encode(Response{State: "RUNNING", RequestToken: "tok-in-flight"})
	})
	mux.HandleFunc("/platform/storage/query/v1/query:poll", func(w http.ResponseWriter, r *http.Request) {
		polls.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
	})
	mux.HandleFunc("/platform/storage/query/v1/query:cancel", func(w http.ResponseWriter, r *http.Request) {
		cancelledToken.Store(r.URL.Query().Get("request-token"))
		w.WriteHeader(http.StatusOK)
	})

	h := NewHandler(newTestClient(t, mux))
	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(50*time.Millisecond, cancel)

	_, err := h.ExecuteAndPoll(ctx, ExecuteRequest{Query: "fetch logs"}, nil)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	if got, _ := cancelledToken.Load().(string); got != "tok-in-flight" {
		t.Errorf("backend cancel token = %q, want %q", got, "tok-in-flight")
	}
	if n := polls.Load(); n != 0 {
		t.Errorf("polled %d times after the caller cancelled, want 0", n)
	}
}

// An execute that stalls is abandoned executeCancelGrace after the caller
// cancels, rather than holding the caller for the full executeTimeout.
func TestExecuteAndPoll_CancelDuringStalledExecuteReturnsAfterGrace(t *testing.T) {
	prev := executeCancelGrace
	executeCancelGrace = 50 * time.Millisecond
	t.Cleanup(func() { executeCancelGrace = prev })

	release := make(chan struct{})
	mux := http.NewServeMux()
	mux.HandleFunc("/platform/storage/query/v1/query:execute", func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-release:
		case <-r.Context().Done():
		}
	})
	h := NewHandler(newTestClient(t, mux))
	t.Cleanup(func() { close(release) })

	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(50*time.Millisecond, cancel)

	start := time.Now()
	_, err := h.ExecuteAndPoll(ctx, ExecuteRequest{Query: "fetch logs"}, nil)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Errorf("returned after %s; a cancelled caller should be released within the grace", elapsed)
	}
}

func TestExecuteAndPoll_QueryFailed(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/platform/storage/query/v1/query:execute", func(w http.ResponseWriter, r *http.Request) {
		resp := Response{State: "RUNNING", RequestToken: "tok-fail"}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusAccepted)
		json.NewEncoder(w).Encode(resp)
	})
	mux.HandleFunc("/platform/storage/query/v1/query:poll", func(w http.ResponseWriter, r *http.Request) {
		resp := Response{State: "FAILED"}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	})

	h := NewHandler(newTestClient(t, mux))
	_, err := h.ExecuteAndPoll(context.Background(), ExecuteRequest{Query: "bad query"}, nil)
	if err == nil {
		t.Fatal("expected error for FAILED query")
	}
}

func TestExecute_ErrorResponse(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/platform/storage/query/v1/query:execute", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte(`{"error":{"message":"invalid query","details":{"errorType":"SYNTAX_ERROR","errorMessage":"parse error","arguments":[]}}}`))
	})

	h := NewHandler(newTestClient(t, mux))
	_, err := h.Execute(context.Background(), ExecuteRequest{Query: "bad"})
	if err == nil {
		t.Fatal("expected error")
	}

	var qErr *QueryError
	if !errorAsQueryError(err, &qErr) {
		t.Fatalf("expected *QueryError, got %T: %v", err, err)
	}
	if qErr.ErrorType != "SYNTAX_ERROR" {
		t.Errorf("ErrorType = %q, want SYNTAX_ERROR", qErr.ErrorType)
	}
}

func TestWithHeaders(t *testing.T) {
	var gotHeader string
	mux := http.NewServeMux()
	mux.HandleFunc("/platform/storage/query/v1/query:execute", func(w http.ResponseWriter, r *http.Request) {
		gotHeader = r.Header.Get("dt-client-context")
		resp := Response{State: "SUCCEEDED"}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	})

	h := NewHandler(newTestClient(t, mux)).WithHeaders(map[string]string{
		"dt-client-context": `{"app":"test"}`,
	})
	_, err := h.Execute(context.Background(), ExecuteRequest{Query: "fetch logs"})
	if err != nil {
		t.Fatalf("Execute() error: %v", err)
	}
	if gotHeader != `{"app":"test"}` {
		t.Errorf("dt-client-context = %q, want %q", gotHeader, `{"app":"test"}`)
	}
}

func TestResponse_GetRecords(t *testing.T) {
	t.Run("from Result", func(t *testing.T) {
		r := &Response{
			Result: &Result{Records: []map[string]interface{}{{"a": 1}}},
		}
		if len(r.GetRecords()) != 1 {
			t.Errorf("got %d records, want 1", len(r.GetRecords()))
		}
	})

	t.Run("from top-level Records", func(t *testing.T) {
		r := &Response{
			Records: []map[string]interface{}{{"b": 2}},
		}
		if len(r.GetRecords()) != 1 {
			t.Errorf("got %d records, want 1", len(r.GetRecords()))
		}
	})

	t.Run("empty", func(t *testing.T) {
		r := &Response{}
		if len(r.GetRecords()) != 0 {
			t.Errorf("got %d records, want 0", len(r.GetRecords()))
		}
	})
}

func TestResponse_GetTypes(t *testing.T) {
	t.Run("from Result", func(t *testing.T) {
		r := &Response{
			Result: &Result{
				Records: []map[string]interface{}{{"count()": "194414758"}},
				Types: []ColumnTypes{
					{IndexRange: []int{0, 0}, Mappings: map[string]ColumnType{"count()": {Type: "long"}}},
				},
			},
		}
		types := r.GetTypes()
		if len(types) != 1 {
			t.Fatalf("got %d type groups, want 1", len(types))
		}
		if got := types[0].Mappings["count()"].Type; got != "long" {
			t.Errorf("got type %q, want long", got)
		}
	})

	t.Run("nil when absent", func(t *testing.T) {
		r := &Response{Records: []map[string]interface{}{{"a": 1}}}
		if r.GetTypes() != nil {
			t.Errorf("expected nil types, got %v", r.GetTypes())
		}
	})

	t.Run("unmarshals from API JSON", func(t *testing.T) {
		body := `{"state":"SUCCEEDED","result":{"records":[{"count()":"194414758"}],` +
			`"types":[{"indexRange":[0,0],"mappings":{"count()":{"type":"long"}}}]}}`
		var r Response
		if err := json.Unmarshal([]byte(body), &r); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		types := r.GetTypes()
		if len(types) != 1 || types[0].Mappings["count()"].Type != "long" {
			t.Errorf("types not parsed correctly: %+v", types)
		}
		if len(types[0].IndexRange) != 2 || types[0].IndexRange[1] != 0 {
			t.Errorf("indexRange not parsed correctly: %+v", types[0].IndexRange)
		}
	})

	t.Run("keeps array element types", func(t *testing.T) {
		body := `{"state":"SUCCEEDED","result":{"records":[{"v":[1.5,2.5]}],` +
			`"types":[{"indexRange":[0,0],"mappings":{"v":{"type":"array","types":` +
			`[{"indexRange":[0,1],"mappings":{"element":{"type":"double"}}}]}}}]}}`
		var r Response
		if err := json.Unmarshal([]byte(body), &r); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		col := r.GetTypes()[0].Mappings["v"]
		if col.Type != "array" || len(col.Types) != 1 {
			t.Fatalf("array element types dropped: %+v", col)
		}
		if got := col.Types[0].Mappings["element"].Type; got != "double" {
			t.Errorf("element type = %q, want double", got)
		}
		out, err := json.Marshal(r.GetTypes())
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		want := `[{"indexRange":[0,0],"mappings":{"v":{"type":"array","types":` +
			`[{"indexRange":[0,1],"mappings":{"element":{"type":"double"}}}]}}}]`
		if string(out) != want {
			t.Errorf("round trip = %s, want %s", out, want)
		}
	})
}

func TestResponse_GetNotifications(t *testing.T) {
	t.Run("from top-level metadata", func(t *testing.T) {
		r := &Response{
			Metadata: &Metadata{
				Grail: &GrailMetadata{
					Notifications: []Notification{{Severity: "WARNING", Message: "scan limit"}},
				},
			},
		}
		if len(r.GetNotifications()) != 1 {
			t.Errorf("got %d notifications, want 1", len(r.GetNotifications()))
		}
	})

	t.Run("from result metadata", func(t *testing.T) {
		r := &Response{
			Result: &Result{
				Metadata: &Metadata{
					Grail: &GrailMetadata{
						Notifications: []Notification{{Severity: "INFO", Message: "ok"}},
					},
				},
			},
		}
		if len(r.GetNotifications()) != 1 {
			t.Errorf("got %d notifications, want 1", len(r.GetNotifications()))
		}
	})
}

func TestResponse_GetMetadata(t *testing.T) {
	r := &Response{
		Metadata: &Metadata{
			Grail: &GrailMetadata{
				QueryID:    "q-123",
				DQLVersion: "1.0",
			},
		},
	}
	m := r.GetMetadata()
	if m == nil {
		t.Fatal("expected metadata")
	}
	if m.QueryID != "q-123" {
		t.Errorf("QueryID = %q, want q-123", m.QueryID)
	}
}

func TestGrailMetadata_UnmarshalApproximationsAndNotifications(t *testing.T) {
	// Mirrors the metadata.grail shape the query API returns for a successful
	// but approximate result (#415). Every advisory field must survive decoding.
	const raw = `{
		"records": [{"n": "1"}],
		"metadata": {
			"grail": {
				"approximations": ["~ on content: substring match, not token match"],
				"canonicalQuery": "fetch logs | filter content ~ \"error\" | summarize n=count()",
				"notifications": [
					{"severity": "INFO", "notificationType": "EXAMPLE_NOTICE", "message": "an advisory note"}
				],
				"sampled": true,
				"scannedRecords": 1
			}
		}
	}`

	var r Response
	if err := json.Unmarshal([]byte(raw), &r); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	g := r.GetMetadata()
	if g == nil {
		t.Fatal("expected grail metadata")
	}
	want := []string{"~ on content: substring match, not token match"}
	if len(g.Approximations) != 1 || g.Approximations[0] != want[0] {
		t.Errorf("Approximations = %q, want %q", g.Approximations, want)
	}
	if got := r.GetApproximations(); len(got) != 1 || got[0] != want[0] {
		t.Errorf("GetApproximations() = %q, want %q", got, want)
	}
	if !g.Sampled {
		t.Error("Sampled = false, want true")
	}
	if n := r.GetNotifications(); len(n) != 1 || n[0].Severity != "INFO" || n[0].Message != "an advisory note" {
		t.Errorf("notifications = %+v, want the one INFO notification", n)
	}
}

func TestApproximations_LenientDecoding(t *testing.T) {
	// An unexpected entry shape must not fail the whole response: the list is
	// advisory, and the stream decoder aborts on any metadata decode error.
	tests := []struct {
		name string
		raw  string
		want []string
	}{
		{"strings", `["a", "b"]`, []string{"a", "b"}},
		{"empty list", `[]`, nil},
		{"null", `null`, nil},
		{"object with message", `[{"message": "approx"}]`, []string{"approx"}},
		{"object without message", `[{"kind": "x"}]`, []string{`{"kind":"x"}`}},
		{"mixed", `["a", {"message": "b"}, 3, null]`, []string{"a", "b", "3"}},
		{"single string", `"only"`, []string{"only"}},
		{"not a list", `{"message": "x"}`, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var g GrailMetadata
			if err := json.Unmarshal([]byte(`{"approximations": `+tt.raw+`, "scannedRecords": 7}`), &g); err != nil {
				t.Fatalf("unmarshal: %v", err)
			}
			if g.ScannedRecords != 7 {
				t.Errorf("ScannedRecords = %d, want 7 (sibling fields must still decode)", g.ScannedRecords)
			}
			if len(g.Approximations) != len(tt.want) {
				t.Fatalf("Approximations = %q, want %q", g.Approximations, tt.want)
			}
			for i := range tt.want {
				if g.Approximations[i] != tt.want[i] {
					t.Errorf("Approximations[%d] = %q, want %q", i, g.Approximations[i], tt.want[i])
				}
			}
		})
	}
}

func TestResponse_GetApproximations(t *testing.T) {
	t.Run("none", func(t *testing.T) {
		if got := (&Response{}).GetApproximations(); got != nil {
			t.Errorf("got %q, want nil", got)
		}
	})
	t.Run("from result metadata", func(t *testing.T) {
		r := &Response{
			Metadata: &Metadata{Grail: &GrailMetadata{}},
			Result: &Result{
				Metadata: &Metadata{
					Grail: &GrailMetadata{Approximations: Approximations{"approx"}},
				},
			},
		}
		if got := r.GetApproximations(); len(got) != 1 || got[0] != "approx" {
			t.Errorf("got %q, want [approx]", got)
		}
	})
}

func TestMetricInfo_UnmarshalFromAPI(t *testing.T) {
	// Mirrors the metadata.metrics[] shape the DQL API returns for timeseries
	// queries. All descriptor fields must survive unmarshalling.
	const raw = `{
		"records": [],
		"metadata": {
			"metrics": [
				{
					"metric.key": "dt.host.cpu.user",
					"displayName": "CPU user",
					"description": "Average CPU time when CPU was running in user mode",
					"unit": "%",
					"fieldName": "sum(dt.host.cpu.user)",
					"aggregation": "sum"
				}
			]
		}
	}`

	var r Response
	if err := json.Unmarshal([]byte(raw), &r); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	metrics := r.GetMetrics()
	if len(metrics) != 1 {
		t.Fatalf("expected 1 metric, got %d", len(metrics))
	}
	m := metrics[0]
	if m.MetricKey != "dt.host.cpu.user" {
		t.Errorf("MetricKey = %q, want dt.host.cpu.user", m.MetricKey)
	}
	if m.DisplayName != "CPU user" {
		t.Errorf("DisplayName = %q, want CPU user", m.DisplayName)
	}
	if m.Description != "Average CPU time when CPU was running in user mode" {
		t.Errorf("Description = %q, want the API description", m.Description)
	}
	if m.Unit != "%" {
		t.Errorf("Unit = %q, want %%", m.Unit)
	}
	if m.FieldName != "sum(dt.host.cpu.user)" {
		t.Errorf("FieldName = %q, want sum(dt.host.cpu.user)", m.FieldName)
	}
	if m.Aggregation != "sum" {
		t.Errorf("Aggregation = %q, want sum", m.Aggregation)
	}
}

func TestResponse_GetMetrics(t *testing.T) {
	t.Run("from top-level metadata", func(t *testing.T) {
		r := &Response{
			Metadata: &Metadata{
				Metrics: []MetricInfo{{MetricKey: "dt.host.cpu.usage", FieldName: "avg(dt.host.cpu.usage)", Aggregation: "avg"}},
			},
		}
		metrics := r.GetMetrics()
		if len(metrics) != 1 || metrics[0].MetricKey != "dt.host.cpu.usage" {
			t.Errorf("got %+v, want one metric for dt.host.cpu.usage", metrics)
		}
	})

	t.Run("from result metadata", func(t *testing.T) {
		r := &Response{
			Result: &Result{
				Metadata: &Metadata{
					Metrics: []MetricInfo{{MetricKey: "dt.host.mem.usage", FieldName: "sum(dt.host.mem.usage)", Aggregation: "sum"}},
				},
			},
		}
		metrics := r.GetMetrics()
		if len(metrics) != 1 || metrics[0].MetricKey != "dt.host.mem.usage" {
			t.Errorf("got %+v, want one metric for dt.host.mem.usage", metrics)
		}
	})

	t.Run("metrics without grail", func(t *testing.T) {
		// The DQL API may return metadata.metrics independently of metadata.grail
		// (they are unrelated siblings of Metadata) — GetMetrics must not depend
		// on Grail being present.
		r := &Response{
			Metadata: &Metadata{
				Metrics: []MetricInfo{{MetricKey: "dt.host.cpu.usage"}},
			},
		}
		if r.GetMetadata() != nil {
			t.Fatal("expected nil Grail metadata for this fixture")
		}
		if len(r.GetMetrics()) != 1 {
			t.Errorf("expected metrics to be returned even when Grail metadata is absent, got %+v", r.GetMetrics())
		}
	})

	t.Run("no metrics", func(t *testing.T) {
		r := &Response{Metadata: &Metadata{}}
		if metrics := r.GetMetrics(); metrics != nil {
			t.Errorf("expected nil metrics, got %+v", metrics)
		}
	})
}

// helpers to avoid importing errors in test
func errorAsAPIError(err error, target **httpclient.APIError) bool {
	for err != nil {
		if e, ok := err.(*httpclient.APIError); ok {
			*target = e
			return true
		}
		u, ok := err.(interface{ Unwrap() error })
		if !ok {
			return false
		}
		err = u.Unwrap()
	}
	return false
}

func errorAsQueryError(err error, target **QueryError) bool {
	for err != nil {
		if e, ok := err.(*QueryError); ok {
			*target = e
			return true
		}
		u, ok := err.(interface{ Unwrap() error })
		if !ok {
			return false
		}
		err = u.Unwrap()
	}
	return false
}

// TestExecuteAndPoll_OnUnauthorizedRetry verifies that a 401 during polling triggers
// the onUnauthorized callback and retries the poll successfully.
func TestExecuteAndPoll_OnUnauthorizedRetry(t *testing.T) {
	var pollCount atomic.Int32
	var refreshCount atomic.Int32

	mux := http.NewServeMux()
	mux.HandleFunc("/platform/storage/query/v1/query:execute", func(w http.ResponseWriter, r *http.Request) {
		resp := Response{State: "RUNNING", RequestToken: "tok-auth"}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusAccepted)
		json.NewEncoder(w).Encode(resp)
	})
	mux.HandleFunc("/platform/storage/query/v1/query:poll", func(w http.ResponseWriter, r *http.Request) {
		n := pollCount.Add(1)
		if n == 1 {
			// First poll: simulate expired token
			w.WriteHeader(http.StatusUnauthorized)
			w.Write([]byte(`{"error":{"message":"jwt expired"}}`))
			return
		}
		// Second poll: verify the refreshed token was applied to the client
		if got := r.Header.Get("Authorization"); got != "Bearer new-token" {
			t.Errorf("retry poll Authorization = %q, want %q", got, "Bearer new-token")
		}
		// Second poll: succeed
		resp := Response{
			State:  "SUCCEEDED",
			Result: &Result{Records: []map[string]interface{}{{"refreshed": true}}},
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	})

	h := NewHandler(newTestClient(t, mux))
	result, err := h.ExecuteAndPoll(context.Background(), ExecuteRequest{Query: "fetch logs"}, func() (string, error) {
		refreshCount.Add(1)
		return "new-token", nil
	})
	if err != nil {
		t.Fatalf("ExecuteAndPoll() error: %v", err)
	}
	if result.State != "SUCCEEDED" {
		t.Errorf("State = %q, want SUCCEEDED", result.State)
	}
	if refreshCount.Load() != 1 {
		t.Errorf("expected 1 token refresh, got %d", refreshCount.Load())
	}
	if pollCount.Load() != 2 {
		t.Errorf("expected 2 polls (1 failed + 1 retry), got %d", pollCount.Load())
	}
}

// TestExecuteAndPoll_OnUnauthorizedAbortOnConsecutive401 verifies that two consecutive
// 401 responses (even after a successful refresh) cause the poll to fail instead of
// looping infinitely.
func TestExecuteAndPoll_OnUnauthorizedAbortOnConsecutive401(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/platform/storage/query/v1/query:execute", func(w http.ResponseWriter, r *http.Request) {
		resp := Response{State: "RUNNING", RequestToken: "tok-double-401"}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusAccepted)
		json.NewEncoder(w).Encode(resp)
	})
	mux.HandleFunc("/platform/storage/query/v1/query:poll", func(w http.ResponseWriter, r *http.Request) {
		// Always return 401 — credentials are fundamentally broken
		w.WriteHeader(http.StatusUnauthorized)
		w.Write([]byte(`{"error":{"message":"jwt expired"}}`))
	})

	h := NewHandler(newTestClient(t, mux))
	_, err := h.ExecuteAndPoll(context.Background(), ExecuteRequest{Query: "fetch logs"}, func() (string, error) {
		return "still-bad-token", nil
	})
	if err == nil {
		t.Fatal("expected error after consecutive 401s")
	}
}

// TestExecuteAndPoll_ImmediateSuccess verifies that ExecuteAndPoll returns immediately
// when the execute response already contains the completed result (no polling needed).
func TestExecuteAndPoll_ImmediateSuccess(t *testing.T) {
	var pollCalled atomic.Bool

	mux := http.NewServeMux()
	mux.HandleFunc("/platform/storage/query/v1/query:execute", func(w http.ResponseWriter, r *http.Request) {
		resp := Response{
			State: "SUCCEEDED",
			Result: &Result{
				Records: []map[string]interface{}{{"instant": true}},
			},
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	})
	mux.HandleFunc("/platform/storage/query/v1/query:poll", func(w http.ResponseWriter, r *http.Request) {
		pollCalled.Store(true)
		t.Error("poll should not be called for immediate success")
	})

	h := NewHandler(newTestClient(t, mux))
	result, err := h.ExecuteAndPoll(context.Background(), ExecuteRequest{Query: "fetch logs | limit 1"}, nil)
	if err != nil {
		t.Fatalf("ExecuteAndPoll() error: %v", err)
	}
	if result.State != "SUCCEEDED" {
		t.Errorf("State = %q, want SUCCEEDED", result.State)
	}
	if pollCalled.Load() {
		t.Error("poll endpoint should not have been called")
	}
}

// TestExecute_RequestBodySerialization verifies that all ExecuteRequest fields are
// correctly serialized in the HTTP request body.
func TestExecute_RequestBodySerialization(t *testing.T) {
	var gotReq ExecuteRequest

	mux := http.NewServeMux()
	mux.HandleFunc("/platform/storage/query/v1/query:execute", func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&gotReq); err != nil {
			t.Errorf("decode request body: %v", err)
		}
		resp := Response{State: "SUCCEEDED"}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	})

	includeTypes := true
	includeContrib := true
	req := ExecuteRequest{
		Query:                        "fetch logs",
		RequestTimeoutMilliseconds:   5000,
		MaxResultRecords:             100,
		MaxResultBytes:               1024,
		DefaultScanLimitGbytes:       500,
		DefaultSamplingRatio:         0.1,
		FetchTimeoutSeconds:          30,
		PollingPromiseSeconds:        7,
		EnablePreview:                true,
		EnforceQueryConsumptionLimit: true,
		IncludeTypes:                 &includeTypes,
		IncludeContributions:         &includeContrib,
		DefaultTimeframeStart:        "2024-01-01T00:00:00Z",
		DefaultTimeframeEnd:          "2024-01-02T00:00:00Z",
		Locale:                       "en_US",
		Timezone:                     "Europe/Vienna",
		FilterSegments: []FilterSegmentRef{
			{ID: "seg-1", Variables: []FilterSegmentVariable{{Name: "host", Values: []string{"a", "b"}}}},
		},
	}

	h := NewHandler(newTestClient(t, mux))
	_, err := h.Execute(context.Background(), req)
	if err != nil {
		t.Fatalf("Execute() error: %v", err)
	}

	if gotReq.Query != "fetch logs" {
		t.Errorf("Query = %q", gotReq.Query)
	}
	if gotReq.MaxResultRecords != 100 {
		t.Errorf("MaxResultRecords = %d, want 100", gotReq.MaxResultRecords)
	}
	if gotReq.DefaultScanLimitGbytes != 500 {
		t.Errorf("DefaultScanLimitGbytes = %f, want 500", gotReq.DefaultScanLimitGbytes)
	}
	if gotReq.Timezone != "Europe/Vienna" {
		t.Errorf("Timezone = %q, want Europe/Vienna", gotReq.Timezone)
	}
	if gotReq.PollingPromiseSeconds != 7 {
		t.Errorf("PollingPromiseSeconds = %d, want 7", gotReq.PollingPromiseSeconds)
	}
	if gotReq.IncludeTypes == nil || !*gotReq.IncludeTypes {
		t.Error("IncludeTypes should be true")
	}
	if len(gotReq.FilterSegments) != 1 || gotReq.FilterSegments[0].ID != "seg-1" {
		t.Errorf("FilterSegments = %v, want [{seg-1 ...}]", gotReq.FilterSegments)
	}
	if len(gotReq.FilterSegments[0].Variables) != 1 || gotReq.FilterSegments[0].Variables[0].Name != "host" {
		t.Error("FilterSegments[0].Variables should contain host variable")
	}
}

// TestVerify_InvalidQuery verifies that a query with syntax errors returns proper
// notifications in the VerifyResponse.
func TestVerify_InvalidQuery(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/platform/storage/query/v1/query:verify", func(w http.ResponseWriter, r *http.Request) {
		resp := VerifyResponse{
			Valid: false,
			Notifications: []VerifyNotification{
				{
					Severity:         "ERROR",
					NotificationType: "SYNTAX_ERROR",
					Message:          "unexpected token 'bogus'",
					SyntaxPosition: &SyntaxPosition{
						Start: &Position{Line: 1, Column: 0},
						End:   &Position{Line: 1, Column: 5},
					},
				},
			},
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	})

	h := NewHandler(newTestClient(t, mux))
	result, err := h.Verify(context.Background(), VerifyRequest{Query: "bogus"})
	if err != nil {
		t.Fatalf("Verify() error: %v", err)
	}
	if result.Valid {
		t.Error("expected invalid query")
	}
	if len(result.Notifications) != 1 {
		t.Fatalf("expected 1 notification, got %d", len(result.Notifications))
	}
	n := result.Notifications[0]
	if n.NotificationType != "SYNTAX_ERROR" {
		t.Errorf("NotificationType = %q, want SYNTAX_ERROR", n.NotificationType)
	}
	if n.SyntaxPosition == nil || n.SyntaxPosition.Start.Line != 1 {
		t.Error("expected syntax position with line 1")
	}
}

// TestWithHeaders_PropagatedToAllEndpoints verifies that custom headers are sent
// on execute, poll, cancel, and verify requests.
func TestWithHeaders_PropagatedToAllEndpoints(t *testing.T) {
	var headers sync.Map // endpoint -> header value

	mux := http.NewServeMux()
	recordHeader := func(endpoint string) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			headers.Store(endpoint, r.Header.Get("x-custom"))
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(Response{State: "SUCCEEDED"})
		}
	}
	mux.HandleFunc("/platform/storage/query/v1/query:execute", recordHeader("execute"))
	mux.HandleFunc("/platform/storage/query/v1/query:poll", recordHeader("poll"))
	mux.HandleFunc("/platform/storage/query/v1/query:cancel", recordHeader("cancel"))
	mux.HandleFunc("/platform/storage/query/v1/query:verify", recordHeader("verify"))

	h := NewHandler(newTestClient(t, mux)).WithHeaders(map[string]string{"x-custom": "test-value"})

	h.Execute(context.Background(), ExecuteRequest{Query: "q"})
	h.Poll(context.Background(), "tok", 1000, false)
	h.Cancel(context.Background(), "tok")
	h.Verify(context.Background(), VerifyRequest{Query: "q"})

	for _, ep := range []string{"execute", "poll", "cancel", "verify"} {
		val, ok := headers.Load(ep)
		if !ok || val.(string) != "test-value" {
			t.Errorf("endpoint %q: x-custom = %q, want %q", ep, val, "test-value")
		}
	}
}

// TestCancel_ServerError verifies that Cancel returns a structured error
// when the server returns a non-2xx status.
func TestCancel_ServerError(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/platform/storage/query/v1/query:cancel", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte("oops"))
	})

	h := NewHandler(newTestClient(t, mux))
	err := h.Cancel(context.Background(), "tok")
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "500") {
		t.Errorf("error should mention status 500: %v", err)
	}
}

// TestQueryError_ErrorFormatting verifies the Error() method produces sensible messages.
func TestQueryError_ErrorFormatting(t *testing.T) {
	t.Run("with error type", func(t *testing.T) {
		e := &QueryError{StatusCode: 400, Message: "bad", ErrorType: "SYNTAX_ERROR"}
		want := "query failed (SYNTAX_ERROR): bad"
		if e.Error() != want {
			t.Errorf("Error() = %q, want %q", e.Error(), want)
		}
	})

	t.Run("without error type", func(t *testing.T) {
		e := &QueryError{StatusCode: 500, Message: "internal"}
		want := "query failed with status 500: internal"
		if e.Error() != want {
			t.Errorf("Error() = %q, want %q", e.Error(), want)
		}
	})

	// The API often repeats the error type as the message and puts the
	// actionable text in details.errorMessage — that detail must surface.
	t.Run("detail replaces a type-echo message", func(t *testing.T) {
		e := &QueryError{StatusCode: 400, Message: "UNKNOWN_COMMAND", ErrorType: "UNKNOWN_COMMAND",
			Detail: "There's no command `dqll`."}
		want := "query failed (UNKNOWN_COMMAND): There's no command `dqll`."
		if e.Error() != want {
			t.Errorf("Error() = %q, want %q", e.Error(), want)
		}
	})

	t.Run("detail appends to a distinct message", func(t *testing.T) {
		e := &QueryError{StatusCode: 400, Message: "invalid query", ErrorType: "SYNTAX_ERROR",
			Detail: "token `|` unexpected", Arguments: []string{"line 1"}}
		want := "query failed (SYNTAX_ERROR): invalid query — token `|` unexpected [line 1]"
		if e.Error() != want {
			t.Errorf("Error() = %q, want %q", e.Error(), want)
		}
	})

	t.Run("arguments skipped when already in message", func(t *testing.T) {
		e := &QueryError{StatusCode: 400, Message: "field `foo` missing", ErrorType: "FIELD_DOES_NOT_EXIST",
			Arguments: []string{"foo"}}
		want := "query failed (FIELD_DOES_NOT_EXIST): field `foo` missing"
		if e.Error() != want {
			t.Errorf("Error() = %q, want %q", e.Error(), want)
		}
	})
}

// TestExecuteAndPoll_SetsRequestTimeout verifies that ExecuteAndPoll sets the
// RequestTimeoutMilliseconds field if the caller omits it.
func TestExecuteAndPoll_SetsRequestTimeout(t *testing.T) {
	var gotTimeout int64

	mux := http.NewServeMux()
	mux.HandleFunc("/platform/storage/query/v1/query:execute", func(w http.ResponseWriter, r *http.Request) {
		var req ExecuteRequest
		json.NewDecoder(r.Body).Decode(&req)
		gotTimeout = req.RequestTimeoutMilliseconds

		resp := Response{State: "SUCCEEDED"}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	})

	h := NewHandler(newTestClient(t, mux))
	// Don't set RequestTimeoutMilliseconds — ExecuteAndPoll should default it
	_, err := h.ExecuteAndPoll(context.Background(), ExecuteRequest{Query: "fetch logs"}, nil)
	if err != nil {
		t.Fatalf("ExecuteAndPoll() error: %v", err)
	}
	if gotTimeout != pollRequestTimeoutMs {
		t.Errorf("RequestTimeoutMilliseconds = %d, want %d", gotTimeout, pollRequestTimeoutMs)
	}
}

// TestExecuteAndPoll_PreservesCallerTimeout verifies that ExecuteAndPoll does NOT
// overwrite a caller-supplied RequestTimeoutMilliseconds value.
func TestExecuteAndPoll_PreservesCallerTimeout(t *testing.T) {
	var gotTimeout int64

	mux := http.NewServeMux()
	mux.HandleFunc("/platform/storage/query/v1/query:execute", func(w http.ResponseWriter, r *http.Request) {
		var req ExecuteRequest
		json.NewDecoder(r.Body).Decode(&req)
		gotTimeout = req.RequestTimeoutMilliseconds

		resp := Response{State: "SUCCEEDED"}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	})

	h := NewHandler(newTestClient(t, mux))
	_, err := h.ExecuteAndPoll(context.Background(), ExecuteRequest{
		Query:                      "fetch logs",
		RequestTimeoutMilliseconds: 10000,
	}, nil)
	if err != nil {
		t.Fatalf("ExecuteAndPoll() error: %v", err)
	}
	if gotTimeout != 10000 {
		t.Errorf("RequestTimeoutMilliseconds = %d, want 10000 (caller value)", gotTimeout)
	}
}

// TestExecuteAndPoll_SetsPollingPromise verifies that ExecuteAndPoll sets the
// PollingPromiseSeconds field to the default if the caller omits it.
func TestExecuteAndPoll_SetsPollingPromise(t *testing.T) {
	var gotPromise int32

	mux := http.NewServeMux()
	mux.HandleFunc("/platform/storage/query/v1/query:execute", func(w http.ResponseWriter, r *http.Request) {
		var req ExecuteRequest
		json.NewDecoder(r.Body).Decode(&req)
		gotPromise = req.PollingPromiseSeconds

		resp := Response{State: "SUCCEEDED"}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	})

	h := NewHandler(newTestClient(t, mux))
	_, err := h.ExecuteAndPoll(context.Background(), ExecuteRequest{Query: "fetch logs"}, nil)
	if err != nil {
		t.Fatalf("ExecuteAndPoll() error: %v", err)
	}
	if gotPromise != defaultPollingPromiseSeconds {
		t.Errorf("PollingPromiseSeconds = %d, want %d", gotPromise, defaultPollingPromiseSeconds)
	}
}

// TestExecuteAndPoll_PreservesCallerPollingPromise verifies that ExecuteAndPoll
// does NOT overwrite a caller-supplied PollingPromiseSeconds value.
func TestExecuteAndPoll_PreservesCallerPollingPromise(t *testing.T) {
	var gotPromise int32

	mux := http.NewServeMux()
	mux.HandleFunc("/platform/storage/query/v1/query:execute", func(w http.ResponseWriter, r *http.Request) {
		var req ExecuteRequest
		json.NewDecoder(r.Body).Decode(&req)
		gotPromise = req.PollingPromiseSeconds

		resp := Response{State: "SUCCEEDED"}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	})

	h := NewHandler(newTestClient(t, mux))
	_, err := h.ExecuteAndPoll(context.Background(), ExecuteRequest{
		Query:                 "fetch logs",
		PollingPromiseSeconds: 42,
	}, nil)
	if err != nil {
		t.Fatalf("ExecuteAndPoll() error: %v", err)
	}
	if gotPromise != 42 {
		t.Errorf("PollingPromiseSeconds = %d, want 42 (caller value)", gotPromise)
	}
}

// TestExecuteAndPoll_PollGapWithinPromiseBudget guards against accidental sleeps
// or backoffs being introduced into the poll loop. The backend will auto-cancel
// a running query if the gap between successive polls exceeds
// pollingPromiseSeconds (currently 5s). Today the loop has zero client-side
// delay between polls; this test pins that property by asserting each
// poll-to-poll gap is well under the budget.
func TestExecuteAndPoll_PollGapWithinPromiseBudget(t *testing.T) {
	const maxGap = 1 * time.Second // ~5x under the 5s pollingPromiseSeconds budget

	var (
		mu        sync.Mutex
		pollTimes []time.Time
	)

	mux := http.NewServeMux()
	mux.HandleFunc("/platform/storage/query/v1/query:execute", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(Response{State: "RUNNING", RequestToken: "tok"})
	})
	mux.HandleFunc("/platform/storage/query/v1/query:poll", func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		pollTimes = append(pollTimes, time.Now())
		n := len(pollTimes)
		mu.Unlock()

		state := "RUNNING"
		if n >= 3 {
			state = "SUCCEEDED"
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(Response{State: state})
	})

	h := NewHandler(newTestClient(t, mux))
	_, err := h.ExecuteAndPoll(context.Background(), ExecuteRequest{Query: "fetch logs"}, nil)
	if err != nil {
		t.Fatalf("ExecuteAndPoll() error: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(pollTimes) < 2 {
		t.Fatalf("expected at least 2 poll calls to measure gaps, got %d", len(pollTimes))
	}
	for i := 1; i < len(pollTimes); i++ {
		gap := pollTimes[i].Sub(pollTimes[i-1])
		if gap >= maxGap {
			t.Errorf("poll-to-poll gap %d->%d = %v, want < %v (must stay well under pollingPromiseSeconds=%ds)",
				i-1, i, gap, maxGap, defaultPollingPromiseSeconds)
		}
	}
}

// TestExecuteAndPoll_NoRequestToken verifies that ExecuteAndPoll returns an error
// when the server returns RUNNING state without a request token.
func TestExecuteAndPoll_NoRequestToken(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/platform/storage/query/v1/query:execute", func(w http.ResponseWriter, r *http.Request) {
		// RUNNING but no request token — broken server response
		resp := Response{State: "RUNNING"}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusAccepted)
		json.NewEncoder(w).Encode(resp)
	})

	h := NewHandler(newTestClient(t, mux))
	_, err := h.ExecuteAndPoll(context.Background(), ExecuteRequest{Query: "fetch logs"}, nil)
	if err == nil {
		t.Fatal("expected error for missing request token")
	}
	if !strings.Contains(err.Error(), "no request token") {
		t.Errorf("error should mention missing token: %v", err)
	}
}

// TestExecuteAndPollWithOptions_OnUpdate verifies that the OnUpdate callback is
// invoked on the initial RUNNING response and on each subsequent RUNNING poll,
// carrying the reported progress and (when previews are enabled) preview rows.
func TestExecuteAndPollWithOptions_OnUpdate(t *testing.T) {
	var pollCount atomic.Int32

	mux := http.NewServeMux()
	mux.HandleFunc("/platform/storage/query/v1/query:execute", func(w http.ResponseWriter, r *http.Request) {
		// Initial async response: RUNNING at 10%, no preview yet.
		resp := Response{State: "RUNNING", RequestToken: "tok-upd", Progress: 10}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusAccepted)
		json.NewEncoder(w).Encode(resp)
	})
	mux.HandleFunc("/platform/storage/query/v1/query:poll", func(w http.ResponseWriter, r *http.Request) {
		n := pollCount.Add(1)
		w.Header().Set("Content-Type", "application/json")
		if n == 1 {
			// A RUNNING poll carrying a preview snapshot.
			json.NewEncoder(w).Encode(Response{
				State:        "RUNNING",
				RequestToken: "tok-upd",
				Progress:     60,
				Result:       &Result{Records: []map[string]interface{}{{"a": 1}, {"a": 2}}},
			})
			return
		}
		json.NewEncoder(w).Encode(Response{
			State:  "SUCCEEDED",
			Result: &Result{Records: []map[string]interface{}{{"a": 1}, {"a": 2}, {"a": 3}}},
		})
	})

	h := NewHandler(newTestClient(t, mux))

	var updates []PollUpdate
	result, err := h.ExecuteAndPollWithOptions(context.Background(),
		ExecuteRequest{Query: "fetch logs", EnablePreview: true},
		ExecuteAndPollOptions{OnUpdate: func(u PollUpdate) { updates = append(updates, u) }},
	)
	if err != nil {
		t.Fatalf("ExecuteAndPollWithOptions() error: %v", err)
	}
	if result.State != "SUCCEEDED" {
		t.Fatalf("State = %q, want SUCCEEDED", result.State)
	}

	// Expect: initial RUNNING (10%, no preview) + one RUNNING poll (60%, 2 rows).
	// The terminal SUCCEEDED poll must NOT produce an update.
	if len(updates) != 2 {
		t.Fatalf("got %d updates, want 2: %+v", len(updates), updates)
	}
	if updates[0].Progress != 10 || updates[0].Preview != nil {
		t.Errorf("first update = %+v, want progress 10 and no preview", updates[0])
	}
	if updates[1].Progress != 60 {
		t.Errorf("second update progress = %d, want 60", updates[1].Progress)
	}
	if updates[1].Preview == nil || len(updates[1].Preview.Records) != 2 {
		t.Errorf("second update should carry a 2-row preview, got %+v", updates[1].Preview)
	}
}

// TestExecuteAndPollWithOptions_NoPreviewWhenDisabled verifies that previews are
// never surfaced to OnUpdate when EnablePreview is false, even if the backend
// happens to include a result on a RUNNING poll.
func TestExecuteAndPollWithOptions_NoPreviewWhenDisabled(t *testing.T) {
	var pollCount atomic.Int32
	mux := http.NewServeMux()
	mux.HandleFunc("/platform/storage/query/v1/query:execute", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusAccepted)
		json.NewEncoder(w).Encode(Response{State: "RUNNING", RequestToken: "tok-np", Progress: 5})
	})
	mux.HandleFunc("/platform/storage/query/v1/query:poll", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if pollCount.Add(1) == 1 {
			json.NewEncoder(w).Encode(Response{
				State: "RUNNING", RequestToken: "tok-np", Progress: 50,
				Result: &Result{Records: []map[string]interface{}{{"a": 1}}},
			})
			return
		}
		json.NewEncoder(w).Encode(Response{State: "SUCCEEDED", Result: &Result{}})
	})

	h := NewHandler(newTestClient(t, mux))
	var updates []PollUpdate
	_, err := h.ExecuteAndPollWithOptions(context.Background(),
		ExecuteRequest{Query: "fetch logs"}, // EnablePreview false
		ExecuteAndPollOptions{OnUpdate: func(u PollUpdate) { updates = append(updates, u) }},
	)
	if err != nil {
		t.Fatalf("ExecuteAndPollWithOptions() error: %v", err)
	}
	for i, u := range updates {
		if u.Preview != nil {
			t.Errorf("update %d carried a preview despite EnablePreview=false: %+v", i, u.Preview)
		}
	}
}

// TestExecuteAndPoll_OnUnauthorizedRetryOnExecute verifies that a 401 on the
// INITIAL execute (not just on polls) triggers the onUnauthorized callback and
// retries the execute with the refreshed token — long-lived processes
// outlive the first bearer token, and their next query starts here.
func TestExecuteAndPoll_OnUnauthorizedRetryOnExecute(t *testing.T) {
	var execCount atomic.Int32
	var refreshCount atomic.Int32

	mux := http.NewServeMux()
	mux.HandleFunc("/platform/storage/query/v1/query:execute", func(w http.ResponseWriter, r *http.Request) {
		n := execCount.Add(1)
		if n == 1 {
			w.WriteHeader(http.StatusUnauthorized)
			w.Write([]byte(`{"error":{"message":"jwt expired"}}`))
			return
		}
		if got := r.Header.Get("Authorization"); got != "Bearer new-token" {
			t.Errorf("retry execute Authorization = %q, want %q", got, "Bearer new-token")
		}
		resp := Response{
			State:  "SUCCEEDED",
			Result: &Result{Records: []map[string]interface{}{{"refreshed": true}}},
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	})

	h := NewHandler(newTestClient(t, mux))
	result, err := h.ExecuteAndPoll(context.Background(), ExecuteRequest{Query: "fetch logs"}, func() (string, error) {
		refreshCount.Add(1)
		return "new-token", nil
	})
	if err != nil {
		t.Fatalf("ExecuteAndPoll() error: %v", err)
	}
	if result.State != "SUCCEEDED" {
		t.Errorf("State = %q, want SUCCEEDED", result.State)
	}
	if refreshCount.Load() != 1 {
		t.Errorf("expected 1 token refresh, got %d", refreshCount.Load())
	}
	if execCount.Load() != 2 {
		t.Errorf("expected 2 executes (1 failed + 1 retry), got %d", execCount.Load())
	}
}

// TestExecuteAndPoll_ExecuteUnauthorizedWithoutRefresher verifies a 401 on the
// initial execute still fails cleanly when no refresher is wired.
func TestExecuteAndPoll_ExecuteUnauthorizedWithoutRefresher(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/platform/storage/query/v1/query:execute", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		w.Write([]byte(`{"error":{"message":"jwt expired"}}`))
	})

	h := NewHandler(newTestClient(t, mux))
	_, err := h.ExecuteAndPoll(context.Background(), ExecuteRequest{Query: "fetch logs"}, nil)
	if err == nil {
		t.Fatal("expected an error without a refresher")
	}
}
