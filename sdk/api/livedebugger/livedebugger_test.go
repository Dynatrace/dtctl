package livedebugger

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/dynatrace-oss/dtctl/sdk/httpclient"
)

func TestBuildGraphQLURL(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		want    string
		wantErr bool
	}{
		{
			name:  "standard SaaS URL",
			input: "https://abc12345.apps.dynatrace.com",
			want:  "https://abc12345.apps.dynatrace.com/platform/dob/graphql",
		},
		{
			name:  "trailing slash stripped",
			input: "https://abc12345.apps.dynatrace.com/",
			want:  "https://abc12345.apps.dynatrace.com/platform/dob/graphql",
		},
		{
			name:    "empty string",
			input:   "",
			wantErr: true,
		},
		{
			name:    "no scheme",
			input:   "abc12345.apps.dynatrace.com",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := buildGraphQLURL(tt.input)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("expected error, got %q", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tt.want {
				t.Fatalf("got %q, want %q", got, tt.want)
			}
		})
	}
}

func TestExtractOrgID(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		want    string
		wantErr bool
	}{
		{
			name:  "standard SaaS URL",
			input: "https://abc12345.apps.dynatrace.com",
			want:  "abc12345",
		},
		{
			name:  "with path",
			input: "https://myorg.apps.dynatrace.com/some/path",
			want:  "myorg",
		},
		{
			name:    "empty string",
			input:   "",
			wantErr: true,
		},
		{
			name:    "no host",
			input:   "https://",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := extractOrgID(tt.input)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("expected error, got %q", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tt.want {
				t.Fatalf("got %q, want %q", got, tt.want)
			}
		})
	}
}

func TestGenerateMutableRuleID(t *testing.T) {
	id := generateMutableRuleID()
	if !strings.HasPrefix(id, "dtctl-rule-") {
		t.Fatalf("expected prefix 'dtctl-rule-', got %q", id)
	}
	// Should be "dtctl-rule-" + 16 hex chars (8 bytes)
	if len(id) != len("dtctl-rule-")+16 {
		t.Fatalf("unexpected length: %q (len=%d)", id, len(id))
	}

	// Two calls should produce different IDs
	id2 := generateMutableRuleID()
	if id == id2 {
		t.Fatalf("expected unique IDs, got same: %q", id)
	}
}

func TestExecuteGraphQL(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			defer r.Body.Close()
			var body map[string]interface{}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatalf("decode request body failed: %v", err)
			}
			if body["query"] == nil {
				t.Fatal("expected query in body")
			}
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]interface{}{
				"data": map[string]interface{}{"hello": "world"},
			})
		}))
		defer server.Close()

		c, err := httpclient.New(server.URL, httpclient.WithToken("dt0c01.test"))
		if err != nil {
			t.Fatalf("httpclient.New failed: %v", err)
		}

		h := &Handler{client: c, graphqlURL: server.URL + "/platform/dob/graphql", orgID: "test-org"}
		resp, err := h.executeGraphQL(context.Background(), "{ hello }", map[string]interface{}{})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if resp["data"] == nil {
			t.Fatal("expected data in response")
		}
	})

	t.Run("graphql errors", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]interface{}{
				"errors": []interface{}{map[string]interface{}{"message": "bad query"}},
			})
		}))
		defer server.Close()

		c, err := httpclient.New(server.URL, httpclient.WithToken("dt0c01.test"))
		if err != nil {
			t.Fatalf("httpclient.New failed: %v", err)
		}

		h := &Handler{client: c, graphqlURL: server.URL + "/platform/dob/graphql", orgID: "test-org"}
		_, err = h.executeGraphQL(context.Background(), "{ bad }", map[string]interface{}{})
		if err == nil {
			t.Fatal("expected error for graphql errors response")
		}
		if !strings.Contains(err.Error(), "graphql returned errors") {
			t.Fatalf("unexpected error message: %v", err)
		}
	})

	t.Run("http error", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusInternalServerError)
			w.Write([]byte(`{"error":"internal"}`))
		}))
		defer server.Close()

		c, err := httpclient.New(server.URL, httpclient.WithToken("dt0c01.test"), httpclient.WithRetry(0, time.Millisecond, time.Millisecond))
		if err != nil {
			t.Fatalf("httpclient.New failed: %v", err)
		}

		h := &Handler{client: c, graphqlURL: server.URL + "/platform/dob/graphql", orgID: "test-org"}
		_, err = h.executeGraphQL(context.Background(), "{ x }", map[string]interface{}{})
		if err == nil {
			t.Fatal("expected error for HTTP 500")
		}
	})
}

func TestWorkspaceHasFilters(t *testing.T) {
	workspaceResp := func(filterSets interface{}) map[string]interface{} {
		ws := map[string]interface{}{"id": "ws-1"}
		if filterSets != nil {
			ws["filterSets"] = filterSets
		}
		return map[string]interface{}{
			"data": map[string]interface{}{
				"org": map[string]interface{}{
					"getOrCreateUserWorkspaceV2": ws,
				},
			},
		}
	}

	t.Run("no filterSets field", func(t *testing.T) {
		got, err := WorkspaceHasFilters(workspaceResp(nil))
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got {
			t.Fatal("expected false when filterSets is absent")
		}
	})

	t.Run("empty filterSets slice", func(t *testing.T) {
		got, err := WorkspaceHasFilters(workspaceResp([]interface{}{}))
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got {
			t.Fatal("expected false when filterSets is empty")
		}
	})

	t.Run("filterSet with empty labels and filters", func(t *testing.T) {
		got, err := WorkspaceHasFilters(workspaceResp([]interface{}{
			map[string]interface{}{
				"labels":  []interface{}{},
				"filters": []interface{}{},
			},
		}))
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got {
			t.Fatal("expected false when all labels/filters are empty")
		}
	})

	t.Run("filterSet with label values", func(t *testing.T) {
		got, err := WorkspaceHasFilters(workspaceResp([]interface{}{
			map[string]interface{}{
				"labels": []interface{}{
					map[string]interface{}{"field": "k8s.container.name", "values": []interface{}{"my-service"}},
				},
				"filters": []interface{}{},
			},
		}))
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !got {
			t.Fatal("expected true when labels have values")
		}
	})

	t.Run("filterSet with filter values", func(t *testing.T) {
		got, err := WorkspaceHasFilters(workspaceResp([]interface{}{
			map[string]interface{}{
				"labels": []interface{}{},
				"filters": []interface{}{
					map[string]interface{}{"field": "dt.entity.host", "values": []interface{}{"HOST-123"}},
				},
			},
		}))
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !got {
			t.Fatal("expected true when filters have values")
		}
	})

	t.Run("invalid response", func(t *testing.T) {
		_, err := WorkspaceHasFilters(map[string]interface{}{"bad": "response"})
		if err != nil {
			t.Fatalf("unexpected error on empty response (missing workspace id is not checked here): %v", err)
		}
	})
}

func TestBuildFilterSets(t *testing.T) {
	labels := func(set map[string]interface{}) []map[string]interface{} {
		t.Helper()
		list, ok := set["labels"].([]map[string]interface{})
		if !ok {
			t.Fatalf("labels: got %T, want []map[string]interface{}", set["labels"])
		}
		return list
	}

	t.Run("fields are sorted", func(t *testing.T) {
		sets := BuildFilterSets(map[string][]string{
			"k8s.workload.name":  {"checkout"},
			"dt.entity.service":  {"SERVICE-0000000000000001"},
			"k8s.namespace.name": {"payments"},
		})
		if len(sets) != 1 {
			t.Fatalf("got %d filter sets, want 1", len(sets))
		}
		var fields []string
		for _, label := range labels(sets[0]) {
			fields = append(fields, label["field"].(string))
		}
		want := []string{"dt.entity.service", "k8s.namespace.name", "k8s.workload.name"}
		if strings.Join(fields, ",") != strings.Join(want, ",") {
			t.Fatalf("fields = %v, want %v", fields, want)
		}
	})

	t.Run("one filter set per map, in order", func(t *testing.T) {
		sets := BuildFilterSets(
			map[string][]string{"k8s.namespace.name": {"payments"}},
			map[string][]string{},
			map[string][]string{"service.name": {"checkout"}},
		)
		if len(sets) != 2 {
			t.Fatalf("got %d filter sets, want 2 (the empty map adds none)", len(sets))
		}
		for i, want := range []string{"k8s.namespace.name", "service.name"} {
			if got := labels(sets[i])[0]["field"]; got != want {
				t.Errorf("set %d field = %v, want %s", i, got, want)
			}
			if filters, ok := sets[i]["filters"].([]interface{}); !ok || len(filters) != 0 {
				t.Errorf("set %d filters = %#v, want an empty list", i, sets[i]["filters"])
			}
		}
	})

	t.Run("no labels gives an empty slice", func(t *testing.T) {
		for name, sets := range map[string][]map[string]interface{}{
			"empty map": BuildFilterSets(map[string][]string{}),
			"no maps":   BuildFilterSets(),
		} {
			if sets == nil || len(sets) != 0 {
				t.Errorf("%s: got %#v, want an empty non-nil slice", name, sets)
			}
		}
	})
}
