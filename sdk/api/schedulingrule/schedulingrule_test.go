package schedulingrule

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Dynatrace/dtctl/sdk/httpclient"
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

func TestList(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/platform/automation/v1/scheduling-rules", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		resp := SchedulingRuleList{
			Count: 2,
			Results: []SchedulingRule{
				{ID: "sr-1", Title: "Business Hours", RuleType: "rrule", RRule: map[string]interface{}{"freq": "WEEKLY"}},
				{ID: "sr-2", Title: "Maintenance Window", RuleType: "grouping", GroupingRule: map[string]interface{}{"combine": []interface{}{"sr-1"}}},
			},
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	})

	h := NewHandler(newTestClient(t, mux))
	result, err := h.List(context.Background(), 0, 0)
	if err != nil {
		t.Fatalf("List() error: %v", err)
	}
	if result.Count != 2 {
		t.Errorf("Count = %d, want 2", result.Count)
	}
	if len(result.Results) != 2 {
		t.Errorf("got %d rules, want 2", len(result.Results))
	}
}

func TestList_ServerError(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/platform/automation/v1/scheduling-rules", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		fmt.Fprint(w, `{"error":{"message":"internal error"}}`)
	})

	h := NewHandler(newTestClient(t, mux))
	_, err := h.List(context.Background(), 0, 0)
	if err == nil {
		t.Fatal("List() expected error for 500")
	}
}

func TestGet(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/platform/automation/v1/scheduling-rules/sr-1", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		rule := SchedulingRule{ID: "sr-1", Title: "Business Hours", RuleType: "rrule", RRule: map[string]interface{}{"freq": "WEEKLY"}}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(rule)
	})

	h := NewHandler(newTestClient(t, mux))
	rule, err := h.Get(context.Background(), "sr-1")
	if err != nil {
		t.Fatalf("Get() error: %v", err)
	}
	if rule.ID != "sr-1" {
		t.Errorf("ID = %q, want %q", rule.ID, "sr-1")
	}
}

func TestGet_NotFound(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/platform/automation/v1/scheduling-rules/missing", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		fmt.Fprint(w, `{"error":{"code":404,"message":"not found"}}`)
	})

	h := NewHandler(newTestClient(t, mux))
	_, err := h.Get(context.Background(), "missing")
	if err == nil {
		t.Fatal("Get() expected error for 404")
	}
}

func TestCreate(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/platform/automation/v1/scheduling-rules", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		rule := SchedulingRule{ID: "sr-new", Title: "New Rule", RuleType: "rrule", RRule: map[string]interface{}{"freq": "DAILY"}}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		json.NewEncoder(w).Encode(rule)
	})

	h := NewHandler(newTestClient(t, mux))
	data := []byte(`{"title":"New Rule","ruleType":"rrule","rrule":{"freq":"DAILY","datestart":"2026-01-05"}}`)
	result, err := h.Create(context.Background(), data)
	if err != nil {
		t.Fatalf("Create() error: %v", err)
	}
	if result.ID != "sr-new" {
		t.Errorf("ID = %q, want %q", result.ID, "sr-new")
	}
}

func TestUpdate(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/platform/automation/v1/scheduling-rules/sr-1", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPut {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		rule := SchedulingRule{ID: "sr-1", Title: "Updated Rule", RuleType: "rrule", RRule: map[string]interface{}{"freq": "WEEKLY"}}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(rule)
	})

	h := NewHandler(newTestClient(t, mux))
	data := []byte(`{"title":"Updated Rule","ruleType":"rrule","rrule":{"freq":"WEEKLY","byday":["MO"]}}`)
	result, err := h.Update(context.Background(), "sr-1", data)
	if err != nil {
		t.Fatalf("Update() error: %v", err)
	}
	if result.Title != "Updated Rule" {
		t.Errorf("Title = %q, want %q", result.Title, "Updated Rule")
	}
}

func TestDelete(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/platform/automation/v1/scheduling-rules/sr-1", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodDelete {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})

	h := NewHandler(newTestClient(t, mux))
	if err := h.Delete(context.Background(), "sr-1"); err != nil {
		t.Fatalf("Delete() error: %v", err)
	}
}

// newPagingMux returns a mock scheduling-rules endpoint backed by `total`
// synthetic rules, honoring limit/offset like the real LimitOffsetPagination
// backend. Unlike the real backend it has no default page size, so a request
// without a limit returns all `total` rows in one response. It records every
// (limit, offset) request pair into reqs.
func newPagingMux(total int, reqs *[][2]string) *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("/platform/automation/v1/scheduling-rules", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		*reqs = append(*reqs, [2]string{q.Get("limit"), q.Get("offset")})

		offset := 0
		if v := q.Get("offset"); v != "" {
			fmt.Sscanf(v, "%d", &offset)
		}
		limit := total
		if v := q.Get("limit"); v != "" {
			fmt.Sscanf(v, "%d", &limit)
		}

		var results []SchedulingRule
		for i := offset; i < total && i < offset+limit; i++ {
			results = append(results, SchedulingRule{ID: fmt.Sprintf("sr-%d", i), RuleType: "rrule"})
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(SchedulingRuleList{Count: total, Results: results})
	})
	return mux
}

func TestList_Pagination(t *testing.T) {
	tests := []struct {
		name      string
		total     int
		chunkSize int64
		limit     int64
		wantLen   int
		wantReqs  int // number of HTTP requests issued
	}{
		// Mock has no default page cap, so a no-limit single request returns all rows.
		{name: "chunkSize 0: single request, server returns all", total: 25, chunkSize: 0, limit: 0, wantLen: 25, wantReqs: 1},
		{name: "pages through all with chunkSize", total: 5, chunkSize: 2, limit: 0, wantLen: 5, wantReqs: 3},
		{name: "chunkSize 0 with limit: requests exactly limit in one GET", total: 100, chunkSize: 0, limit: 10, wantLen: 10, wantReqs: 1},
		{name: "limit caps and stops paging", total: 100, chunkSize: 2, limit: 5, wantLen: 5, wantReqs: 3},
		{name: "limit larger than total", total: 3, chunkSize: 2, limit: 50, wantLen: 3, wantReqs: 2},
		// The bug this pagination replaced: >100 rules must not be truncated.
		{name: "pages past the server default page size", total: 250, chunkSize: 100, limit: 0, wantLen: 250, wantReqs: 3},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var reqs [][2]string
			h := NewHandler(newTestClient(t, newPagingMux(tt.total, &reqs)))

			result, err := h.List(context.Background(), tt.chunkSize, tt.limit)
			if err != nil {
				t.Fatalf("List() error: %v", err)
			}
			if len(result.Results) != tt.wantLen {
				t.Errorf("got %d results, want %d", len(result.Results), tt.wantLen)
			}
			if result.Count != tt.total {
				t.Errorf("Count = %d, want %d", result.Count, tt.total)
			}
			if len(reqs) != tt.wantReqs {
				t.Errorf("issued %d requests, want %d (reqs=%v)", len(reqs), tt.wantReqs, reqs)
			}
		})
	}
}

func TestList_TruncatesOverReturn(t *testing.T) {
	// Server ignores the limit param and returns more than requested; the client
	// must still cap the result slice at the requested limit.
	mux := http.NewServeMux()
	mux.HandleFunc("/platform/automation/v1/scheduling-rules", func(w http.ResponseWriter, r *http.Request) {
		results := make([]SchedulingRule, 10)
		for i := range results {
			results[i] = SchedulingRule{ID: fmt.Sprintf("sr-%d", i), RuleType: "rrule"}
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(SchedulingRuleList{Count: 10, Results: results})
	})

	h := NewHandler(newTestClient(t, mux))
	result, err := h.List(context.Background(), 0, 3)
	if err != nil {
		t.Fatalf("List() error: %v", err)
	}
	if len(result.Results) != 3 {
		t.Errorf("got %d results, want 3 (truncated)", len(result.Results))
	}
}
