package workflow

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

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

func stringPtr(v string) *string { return &v }

func intPtr(v int) *int { return &v }

func TestList(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/platform/automation/v1/workflows", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		resp := WorkflowList{
			Count: 2,
			Results: []Workflow{
				{ID: "wf-1", Title: "Deploy", Owner: "user-1"},
				{ID: "wf-2", Title: "Remediation", Owner: "user-2"},
			},
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	})

	h := NewHandler(newTestClient(t, mux))
	result, err := h.List(context.Background(), WorkflowFilters{}, 0, 0)
	if err != nil {
		t.Fatalf("List() error: %v", err)
	}
	if result.Count != 2 {
		t.Errorf("Count = %d, want 2", result.Count)
	}
	if len(result.Results) != 2 {
		t.Errorf("got %d workflows, want 2", len(result.Results))
	}
}

func TestList_WithOwnerFilter(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/platform/automation/v1/workflows", func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.Query().Get("owner"); got != "user-1" {
			t.Fatalf("owner query = %q, want %q", got, "user-1")
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"count":0,"results":[]}`)
	})

	h := NewHandler(newTestClient(t, mux))
	_, err := h.List(context.Background(), WorkflowFilters{Owner: "user-1"}, 0, 0)
	if err != nil {
		t.Fatalf("List() error: %v", err)
	}
}

func TestList_ServerError(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/platform/automation/v1/workflows", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		fmt.Fprint(w, `{"error":{"message":"internal error"}}`)
	})

	h := NewHandler(newTestClient(t, mux))
	_, err := h.List(context.Background(), WorkflowFilters{}, 0, 0)
	if err == nil {
		t.Fatal("List() expected error for 500")
	}
}

func TestList_ParseError(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/platform/automation/v1/workflows", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{invalid-json`)
	})

	h := NewHandler(newTestClient(t, mux))
	_, err := h.List(context.Background(), WorkflowFilters{}, 0, 0)
	if err == nil {
		t.Fatal("List() expected parse error")
	}
}

// newPagingMux returns a mock /workflows endpoint backed by `total` synthetic
// workflows, honoring limit/offset like the real LimitOffsetPagination backend.
// Unlike the real backend it has no default page size, so a request without a
// limit returns all `total` rows in one response. It records every (limit,
// offset) request pair into reqs.
func newPagingMux(total int, reqs *[][2]string) *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("/platform/automation/v1/workflows", func(w http.ResponseWriter, r *http.Request) {
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

		var results []Workflow
		for i := offset; i < total && i < offset+limit; i++ {
			results = append(results, Workflow{ID: fmt.Sprintf("wf-%d", i)})
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(WorkflowList{Count: total, Results: results})
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
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var reqs [][2]string
			h := NewHandler(newTestClient(t, newPagingMux(tt.total, &reqs)))

			result, err := h.List(context.Background(), WorkflowFilters{}, tt.chunkSize, tt.limit)
			if err != nil {
				t.Fatalf("List() error: %v", err)
			}
			if len(result.Results) != tt.wantLen {
				t.Errorf("got %d results, want %d", len(result.Results), tt.wantLen)
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
	mux.HandleFunc("/platform/automation/v1/workflows", func(w http.ResponseWriter, r *http.Request) {
		results := make([]Workflow, 10)
		for i := range results {
			results[i] = Workflow{ID: fmt.Sprintf("wf-%d", i)}
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(WorkflowList{Count: 10, Results: results})
	})

	h := NewHandler(newTestClient(t, mux))
	result, err := h.List(context.Background(), WorkflowFilters{}, 0, 3)
	if err != nil {
		t.Fatalf("List() error: %v", err)
	}
	if len(result.Results) != 3 {
		t.Errorf("got %d results, want 3 (truncated)", len(result.Results))
	}
}

func TestList_LimitNarrowsLastPage(t *testing.T) {
	// chunkSize 4, limit 5: first page requests limit=4, second page requests
	// limit=1 (the remaining budget), not another full chunk.
	var reqs [][2]string
	h := NewHandler(newTestClient(t, newPagingMux(100, &reqs)))

	result, err := h.List(context.Background(), WorkflowFilters{}, 4, 5)
	if err != nil {
		t.Fatalf("List() error: %v", err)
	}
	if len(result.Results) != 5 {
		t.Fatalf("got %d results, want 5", len(result.Results))
	}
	want := [][2]string{{"4", ""}, {"1", "4"}}
	if fmt.Sprint(reqs) != fmt.Sprint(want) {
		t.Errorf("requests = %v, want %v", reqs, want)
	}
}

func TestGet(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/platform/automation/v1/workflows/wf-1", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		resp := Workflow{
			ID:                   "wf-1",
			Title:                "Deploy",
			IsDeployed:           true,
			Description:          "Workflow description",
			Actor:                "user-1",
			Owner:                "user-1",
			OwnerType:            "USER",
			Private:              false,
			SchemaVersion:        4,
			Trigger:              map[string]interface{}{"manual": true},
			Result:               stringPtr("{{ result('deploy') }}"),
			Type:                 "STANDARD",
			Input:                map[string]interface{}{"env": "prod"},
			HourlyExecutionLimit: intPtr(1000),
			Guide:                stringPtr("# Deploy\n\nRun after validation."),
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	})

	h := NewHandler(newTestClient(t, mux))
	result, err := h.Get(context.Background(), "wf-1")
	if err != nil {
		t.Fatalf("Get() error: %v", err)
	}
	if result.ID != "wf-1" {
		t.Errorf("ID = %q, want %q", result.ID, "wf-1")
	}
	if result.Title != "Deploy" {
		t.Errorf("Title = %q, want %q", result.Title, "Deploy")
	}
	if !result.IsDeployed {
		t.Errorf("IsDeployed = %v, want true", result.IsDeployed)
	}
	if result.Description != "Workflow description" {
		t.Errorf("Description = %q, want %q", result.Description, "Workflow description")
	}
	if result.Actor != "user-1" {
		t.Errorf("Actor = %q, want %q", result.Actor, "user-1")
	}
	if result.OwnerType != "USER" {
		t.Errorf("OwnerType = %q, want %q", result.OwnerType, "USER")
	}
	if result.Private {
		t.Errorf("Private = %v, want false", result.Private)
	}
	if result.SchemaVersion != 4 {
		t.Errorf("SchemaVersion = %d, want 4", result.SchemaVersion)
	}
	if got := result.Trigger["manual"]; got != true {
		t.Errorf("Trigger[manual] = %#v, want true", got)
	}
	if result.Type != "STANDARD" {
		t.Errorf("Type = %q, want %q", result.Type, "STANDARD")
	}
	if got := result.Input["env"]; got != "prod" {
		t.Errorf("Input[env] = %#v, want %q", got, "prod")
	}
	if result.Result == nil || *result.Result != "{{ result('deploy') }}" {
		t.Fatalf("Result = %#v, want %q", result.Result, "{{ result('deploy') }}")
	}
	if result.HourlyExecutionLimit == nil || *result.HourlyExecutionLimit != 1000 {
		t.Fatalf("HourlyExecutionLimit = %#v, want %d", result.HourlyExecutionLimit, 1000)
	}
	if result.Guide == nil || *result.Guide != "# Deploy\n\nRun after validation." {
		t.Fatalf("Guide = %#v, want %q", result.Guide, "# Deploy\n\nRun after validation.")
	}
}

func TestGet_NotFound(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/platform/automation/v1/workflows/missing", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		fmt.Fprintf(w, `{"error":{"message":"not found"}}`)
	})

	h := NewHandler(newTestClient(t, mux))
	_, err := h.Get(context.Background(), "missing")
	if err == nil {
		t.Fatal("Get() expected error for 404")
	}
}

func TestCreate(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/platform/automation/v1/workflows", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		resp := Workflow{ID: "wf-new", Title: "New Workflow", Owner: "user-1"}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		json.NewEncoder(w).Encode(resp)
	})

	h := NewHandler(newTestClient(t, mux))
	result, err := h.Create(context.Background(), []byte(`{"title":"New Workflow"}`))
	if err != nil {
		t.Fatalf("Create() error: %v", err)
	}
	if result.ID != "wf-new" {
		t.Errorf("ID = %q, want %q", result.ID, "wf-new")
	}
}

func TestCreate_ParseError(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/platform/automation/v1/workflows", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{invalid-json`)
	})

	h := NewHandler(newTestClient(t, mux))
	_, err := h.Create(context.Background(), []byte(`{"title":"New Workflow"}`))
	if err == nil {
		t.Fatal("Create() expected parse error")
	}
}

func TestDelete(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/platform/automation/v1/workflows/wf-1", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodDelete {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})

	h := NewHandler(newTestClient(t, mux))
	err := h.Delete(context.Background(), "wf-1")
	if err != nil {
		t.Fatalf("Delete() error: %v", err)
	}
}

func TestDelete_ServerError(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/platform/automation/v1/workflows/wf-1", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		fmt.Fprint(w, `{"error":{"message":"internal error"}}`)
	})

	h := NewHandler(newTestClient(t, mux))
	err := h.Delete(context.Background(), "wf-1")
	if err == nil {
		t.Fatal("Delete() expected error for 500")
	}
}

func TestGetRaw(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/platform/automation/v1/workflows/wf-raw", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"id":"wf-raw","title":"Raw"}`)
	})

	h := NewHandler(newTestClient(t, mux))
	body, err := h.GetRaw(context.Background(), "wf-raw")
	if err != nil {
		t.Fatalf("GetRaw() error: %v", err)
	}
	if got := string(body); got != `{"id":"wf-raw","title":"Raw"}` {
		t.Fatalf("GetRaw() = %q, want %q", got, `{"id":"wf-raw","title":"Raw"}`)
	}
}

func TestUpdate(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/platform/automation/v1/workflows/wf-1", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPut {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		if ct := r.Header.Get("Content-Type"); ct != "application/json" {
			t.Fatalf("Content-Type = %q, want application/json", ct)
		}
		resp := Workflow{ID: "wf-1", Title: "Updated Workflow", Owner: "user-1"}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	})

	h := NewHandler(newTestClient(t, mux))
	result, err := h.Update(context.Background(), "wf-1", []byte(`{"title":"Updated Workflow"}`))
	if err != nil {
		t.Fatalf("Update() error: %v", err)
	}
	if result.Title != "Updated Workflow" {
		t.Fatalf("Title = %q, want %q", result.Title, "Updated Workflow")
	}
}

func TestUpdate_ParseError(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/platform/automation/v1/workflows/wf-1", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPut {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{invalid-json`)
	})

	h := NewHandler(newTestClient(t, mux))
	_, err := h.Update(context.Background(), "wf-1", []byte(`{"title":"Updated Workflow"}`))
	if err == nil {
		t.Fatal("Update() expected parse error")
	}
}

func TestListHistory(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/platform/automation/v1/workflows/wf-1/history", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		resp := HistoryList{
			Count: 2,
			Results: []HistoryRecord{
				{Version: 1, User: "user-1", DateCreated: "2026-05-19T10:00:00Z"},
				{Version: 2, User: "user-1", DateCreated: "2026-05-19T11:00:00Z"},
			},
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	})

	h := NewHandler(newTestClient(t, mux))
	result, err := h.ListHistory(context.Background(), "wf-1")
	if err != nil {
		t.Fatalf("ListHistory() error: %v", err)
	}
	if result.Count != 2 || len(result.Results) != 2 {
		t.Fatalf("ListHistory() = %#v, want count=2 and two results", result)
	}
}

func TestListHistory_ParseError(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/platform/automation/v1/workflows/wf-1/history", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{invalid-json`)
	})

	h := NewHandler(newTestClient(t, mux))
	_, err := h.ListHistory(context.Background(), "wf-1")
	if err == nil {
		t.Fatal("ListHistory() expected parse error")
	}
}

func TestGetHistoryRecord(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/platform/automation/v1/workflows/wf-1/history/2", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		resp := Workflow{ID: "wf-1", Title: "Version 2", Owner: "user-1"}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	})

	h := NewHandler(newTestClient(t, mux))
	result, err := h.GetHistoryRecord(context.Background(), "wf-1", 2)
	if err != nil {
		t.Fatalf("GetHistoryRecord() error: %v", err)
	}
	if result.Title != "Version 2" {
		t.Fatalf("Title = %q, want %q", result.Title, "Version 2")
	}
}

func TestGetHistoryRecord_ParseError(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/platform/automation/v1/workflows/wf-1/history/2", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{invalid-json`)
	})

	h := NewHandler(newTestClient(t, mux))
	_, err := h.GetHistoryRecord(context.Background(), "wf-1", 2)
	if err == nil {
		t.Fatal("GetHistoryRecord() expected parse error")
	}
}

func TestRestoreHistory(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/platform/automation/v1/workflows/wf-1/history/2/restore", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		resp := Workflow{ID: "wf-1", Title: "Restored", Owner: "user-1"}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	})

	h := NewHandler(newTestClient(t, mux))
	result, err := h.RestoreHistory(context.Background(), "wf-1", 2)
	if err != nil {
		t.Fatalf("RestoreHistory() error: %v", err)
	}
	if result.Title != "Restored" {
		t.Fatalf("Title = %q, want %q", result.Title, "Restored")
	}
}

func TestRestoreHistory_ParseError(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/platform/automation/v1/workflows/wf-1/history/2/restore", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{invalid-json`)
	})

	h := NewHandler(newTestClient(t, mux))
	_, err := h.RestoreHistory(context.Background(), "wf-1", 2)
	if err == nil {
		t.Fatal("RestoreHistory() expected parse error")
	}
}

func TestGet_ServerError(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/platform/automation/v1/workflows/wf-1", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		fmt.Fprintf(w, `{"error":{"message":"internal error"}}`)
	})

	h := NewHandler(newTestClient(t, mux))
	_, err := h.Get(context.Background(), "wf-1")
	if err == nil {
		t.Fatal("Get() expected error for 500")
	}
}

// TestWithAdminAccess pins #700: a handler built WithAdminAccess(true) sends
// adminAccess=true on every call — the existence check apply makes before an
// update included, or another user's workflow is a 403 before the PUT is ever
// sent — and the handler it was copied from keeps sending nothing.
func TestWithAdminAccess(t *testing.T) {
	seen := map[string]string{}
	mux := http.NewServeMux()
	record := func(w http.ResponseWriter, r *http.Request) {
		key := r.Method + " " + r.URL.Path
		if v, ok := r.URL.Query()["adminAccess"]; ok {
			seen[key] = v[0]
		} else {
			seen[key] = "<absent>"
		}
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/platform/automation/v1/workflows" && r.Method == http.MethodGet:
			fmt.Fprint(w, `{"count":0,"results":[]}`)
		case r.URL.Path == "/platform/automation/v1/executions":
			fmt.Fprint(w, `{"count":0,"results":[]}`)
		default:
			fmt.Fprint(w, `{"id":"wf-1","title":"Alert"}`)
		}
	}
	mux.HandleFunc("/platform/automation/v1/workflows", record)
	mux.HandleFunc("/platform/automation/v1/workflows/wf-1", record)
	mux.HandleFunc("/platform/automation/v1/executions", record)
	mux.HandleFunc("/platform/automation/v1/executions/ex-1", record)

	c := newTestClient(t, mux)
	plain := NewHandler(c)
	admin := plain.WithAdminAccess(true)
	ctx := context.Background()

	if _, err := admin.Get(ctx, "wf-1"); err != nil {
		t.Fatalf("Get: %v", err)
	}
	if _, err := admin.Update(ctx, "wf-1", []byte(`{"title":"Alert"}`)); err != nil {
		t.Fatalf("Update: %v", err)
	}
	if _, err := admin.Create(ctx, []byte(`{"title":"Alert"}`)); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, err := admin.List(ctx, WorkflowFilters{}, 0, 0); err != nil {
		t.Fatalf("List: %v", err)
	}
	execAdmin := NewExecutionHandler(c).WithAdminAccess(true)
	if _, err := execAdmin.Get(ctx, "ex-1"); err != nil {
		t.Fatalf("execution Get: %v", err)
	}
	if _, err := execAdmin.List(ctx, ExecutionFilters{}, 10); err != nil {
		t.Fatalf("execution List: %v", err)
	}
	for _, key := range []string{
		"GET /platform/automation/v1/workflows/wf-1",
		"PUT /platform/automation/v1/workflows/wf-1",
		"POST /platform/automation/v1/workflows",
		"GET /platform/automation/v1/workflows",
		"GET /platform/automation/v1/executions/ex-1",
		"GET /platform/automation/v1/executions",
	} {
		if seen[key] != "true" {
			t.Errorf("%s: adminAccess = %q, want \"true\"", key, seen[key])
		}
	}

	// The original handler is untouched by the copy.
	if _, err := plain.Get(ctx, "wf-1"); err != nil {
		t.Fatalf("plain Get: %v", err)
	}
	if got := seen["GET /platform/automation/v1/workflows/wf-1"]; got != "<absent>" {
		t.Errorf("plain handler sent adminAccess=%q, want it absent", got)
	}
}
