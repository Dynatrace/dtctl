package workflow

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"testing"
)

func TestExecutionList_Filters(t *testing.T) {
	var got map[string]string
	mux := http.NewServeMux()
	mux.HandleFunc("/platform/automation/v1/executions", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		got = map[string]string{
			"workflow":       q.Get("workflow"),
			"state":          q.Get("state"),
			"triggerType":    q.Get("triggerType"),
			"startedAt__gte": q.Get("startedAt__gte"),
			"startedAt__lte": q.Get("startedAt__lte"),
			"limit":          q.Get("limit"),
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(ExecutionList{Count: 1, Results: []Execution{{ID: "e-1", State: "SUCCESS"}}})
	})

	h := NewExecutionHandler(newTestClient(t, mux))
	_, err := h.List(context.Background(), ExecutionFilters{
		WorkflowID:   "wf-1",
		State:        "SUCCESS",
		TriggerType:  "Schedule",
		StartedSince: "2026-06-01T00:00:00Z",
		StartedUntil: "2026-06-10T23:59:59Z",
	}, 50)
	if err != nil {
		t.Fatalf("List() error: %v", err)
	}

	want := map[string]string{
		"workflow":       "wf-1",
		"state":          "SUCCESS",
		"triggerType":    "Schedule",
		"startedAt__gte": "2026-06-01T00:00:00Z",
		"startedAt__lte": "2026-06-10T23:59:59Z",
		"limit":          "50",
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("query param %q = %q, want %q", k, got[k], v)
		}
	}
}

func TestExecutionList_LimitCappedAtMax(t *testing.T) {
	var gotLimit string
	mux := http.NewServeMux()
	mux.HandleFunc("/platform/automation/v1/executions", func(w http.ResponseWriter, r *http.Request) {
		gotLimit = r.URL.Query().Get("limit")
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(ExecutionList{Count: 0, Results: []Execution{}})
	})

	h := NewExecutionHandler(newTestClient(t, mux))
	if _, err := h.List(context.Background(), ExecutionFilters{}, 2000); err != nil {
		t.Fatalf("List() error: %v", err)
	}
	if gotLimit != fmt.Sprint(MaxExecutionLimit) {
		t.Errorf("limit query param = %q, want %d (capped)", gotLimit, MaxExecutionLimit)
	}
}

func TestExecutionList_NoLimitWhenZero(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/platform/automation/v1/executions", func(w http.ResponseWriter, r *http.Request) {
		if _, ok := r.URL.Query()["limit"]; ok {
			t.Errorf("limit query param should be absent when limit is 0")
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(ExecutionList{Count: 0, Results: []Execution{}})
	})

	h := NewExecutionHandler(newTestClient(t, mux))
	if _, err := h.List(context.Background(), ExecutionFilters{}, 0); err != nil {
		t.Fatalf("List() error: %v", err)
	}
}

func TestExecutionList_ServerError(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/platform/automation/v1/executions", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		fmt.Fprint(w, `{"error":{"message":"boom"}}`)
	})

	h := NewExecutionHandler(newTestClient(t, mux))
	if _, err := h.List(context.Background(), ExecutionFilters{}, 10); err == nil {
		t.Fatal("List() expected error for 500")
	}
}

func TestExecutionList_ParseError(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/platform/automation/v1/executions", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{invalid-json`)
	})

	h := NewExecutionHandler(newTestClient(t, mux))
	if _, err := h.List(context.Background(), ExecutionFilters{}, 10); err == nil {
		t.Fatal("List() expected parse error")
	}
}
