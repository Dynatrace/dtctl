package livedebugger

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Dynatrace/dtctl/pkg/client"
)

func newGraphQLTestHandler(t *testing.T, statusCode int, responder func(body map[string]interface{}) map[string]interface{}) *Handler {
	t.Helper()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer r.Body.Close()
		var body map[string]interface{}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode request body failed: %v", err)
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(statusCode)
		if responder != nil {
			_ = json.NewEncoder(w).Encode(responder(body))
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]interface{}{"data": map[string]interface{}{}})
	}))
	t.Cleanup(server.Close)

	c, err := client.NewForTesting(server.URL, "dt0c01.test")
	if err != nil {
		t.Fatalf("client.New failed: %v", err)
	}

	h, err := NewHandler(c, server.URL)
	if err != nil {
		t.Fatalf("NewHandler failed: %v", err)
	}
	return h
}

func TestNewHandler(t *testing.T) {
	t.Run("invalid environment", func(t *testing.T) {
		c, _ := client.New("https://example.invalid", "dt0c01.test")
		if _, err := NewHandler(c, "://bad"); err == nil {
			t.Fatalf("expected NewHandler error")
		}
	})

	t.Run("valid environment", func(t *testing.T) {
		c, _ := client.New("https://abc.example.invalid", "dt0c01.test")
		h, err := NewHandler(c, "https://abc.example.invalid")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if h == nil {
			t.Fatal("expected non-nil handler")
		}
		if h.sdk == nil {
			t.Fatal("expected non-nil sdk handler")
		}
	})
}

func TestExtractWorkspaceID(t *testing.T) {
	if _, err := ExtractWorkspaceID(map[string]interface{}{}); err == nil {
		t.Fatalf("expected missing data error")
	}

	resp := map[string]interface{}{
		"data": map[string]interface{}{
			"org": map[string]interface{}{
				"getOrCreateUserWorkspaceV2": map[string]interface{}{"id": "ws-1"},
			},
		},
	}
	id, err := ExtractWorkspaceID(resp)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if id != "ws-1" {
		t.Fatalf("unexpected workspace id: %q", id)
	}
}

func TestBuildFilterSets_UsesLabelsAndEmptyFilters(t *testing.T) {
	input := map[string][]string{
		"k8s.container.name":          {"credit-card-order-service"},
		"dt.kubernetes.workload.name": {"credit-card-order-service"},
	}

	sets := BuildFilterSets(input)
	if len(sets) != 1 {
		t.Fatalf("expected one filter set, got %d", len(sets))
	}

	set := sets[0]

	labels, ok := set["labels"].([]map[string]interface{})
	if !ok {
		t.Fatalf("expected labels to be []map[string]interface{}, got %T", set["labels"])
	}
	if len(labels) != 2 {
		t.Fatalf("expected 2 labels, got %d", len(labels))
	}

	filters, ok := set["filters"].([]interface{})
	if !ok {
		t.Fatalf("expected filters to be []interface{}, got %T", set["filters"])
	}
	if len(filters) != 0 {
		t.Fatalf("expected filters to be empty, got %d items", len(filters))
	}

	lookup := map[string][]string{}
	for _, label := range labels {
		field, _ := label["field"].(string)
		values, _ := label["values"].([]string)
		lookup[field] = values
	}

	if got := len(lookup["k8s.container.name"]); got != 1 || lookup["k8s.container.name"][0] != "credit-card-order-service" {
		t.Fatalf("unexpected values for k8s.container.name: %#v", lookup["k8s.container.name"])
	}

	if got := len(lookup["dt.kubernetes.workload.name"]); got != 1 || lookup["dt.kubernetes.workload.name"][0] != "credit-card-order-service" {
		t.Fatalf("unexpected values for dt.kubernetes.workload.name: %#v", lookup["dt.kubernetes.workload.name"])
	}
}

func TestBuildFilterSets_Empty(t *testing.T) {
	sets := BuildFilterSets(map[string][]string{})
	if len(sets) != 0 {
		t.Fatalf("expected empty filter sets, got %#v", sets)
	}
}

func TestHandlerMethods(t *testing.T) {
	h := newGraphQLTestHandler(t, http.StatusOK, func(body map[string]interface{}) map[string]interface{} {
		query, _ := body["query"].(string)
		switch {
		case strings.Contains(query, "GetOrCreateWorkspaceV2"):
			return map[string]interface{}{
				"data": map[string]interface{}{
					"org": map[string]interface{}{
						"getOrCreateUserWorkspaceV2": map[string]interface{}{"id": "ws-1"},
					},
				},
			}
		case strings.Contains(query, "UpdateWorkspaceV2"):
			return map[string]interface{}{"data": map[string]interface{}{"org": map[string]interface{}{"updateWorkspaceV2": map[string]interface{}{"id": "ws-1"}}}}
		case strings.Contains(query, "CreateRule"):
			return map[string]interface{}{"data": map[string]interface{}{"org": map[string]interface{}{"workspace": map[string]interface{}{"createRuleV2": map[string]interface{}{"id": "r1"}}}}}
		case strings.Contains(query, "GetWorkspaceRules"):
			return map[string]interface{}{"data": map[string]interface{}{"org": map[string]interface{}{"workspace": map[string]interface{}{"rules": []interface{}{}}}}}
		case strings.Contains(query, "DeleteRule"):
			return map[string]interface{}{"data": map[string]interface{}{"org": map[string]interface{}{"workspace": map[string]interface{}{"deleteRuleV2": true}}}}
		case strings.Contains(query, "GetRuleStatusBreakdown"):
			return map[string]interface{}{"data": map[string]interface{}{"org": map[string]interface{}{"ruleStatuses": []interface{}{}}}}
		case strings.Contains(query, "EditRuleV2"):
			return map[string]interface{}{"data": map[string]interface{}{"org": map[string]interface{}{"workspace": map[string]interface{}{"editRuleV2": map[string]interface{}{"id": "r1"}}}}}
		case strings.Contains(query, "EnableOrDisableRules"):
			return map[string]interface{}{"data": map[string]interface{}{"org": map[string]interface{}{"workspace": map[string]interface{}{"enableOrDisableRules": []interface{}{}}}}}
		case strings.Contains(query, "DeleteAllRulesFromWorkspace"):
			return map[string]interface{}{"data": map[string]interface{}{"org": map[string]interface{}{"workspace": map[string]interface{}{"deleteAllRulesFromWorkspaceV2": []interface{}{"r1"}}}}}
		default:
			return map[string]interface{}{"data": map[string]interface{}{}}
		}
	})

	workspaceResp, workspaceID, err := h.GetOrCreateWorkspace("proj")
	if err != nil || workspaceID != "ws-1" || workspaceResp == nil {
		t.Fatalf("GetOrCreateWorkspace failed: id=%q err=%v resp=%#v", workspaceID, err, workspaceResp)
	}

	if _, err := h.UpdateWorkspaceFilters("ws-1", BuildFilterSets(map[string][]string{"k": {"v"}})); err != nil {
		t.Fatalf("UpdateWorkspaceFilters failed: %v", err)
	}
	if _, err := h.CreateBreakpoint("ws-1", "A.java", 10); err != nil {
		t.Fatalf("CreateBreakpoint failed: %v", err)
	}
	if _, err := h.GetWorkspaceRules("ws-1"); err != nil {
		t.Fatalf("GetWorkspaceRules failed: %v", err)
	}
	if _, err := h.DeleteBreakpoint("ws-1", "bp-1"); err != nil {
		t.Fatalf("DeleteBreakpoint failed: %v", err)
	}
	if _, err := h.GetRuleStatusBreakdown("bp-1"); err != nil {
		t.Fatalf("GetRuleStatusBreakdown failed: %v", err)
	}
	if _, err := h.EditBreakpoint("ws-1", map[string]interface{}{"mutableRuleId": "bp-1"}); err != nil {
		t.Fatalf("EditBreakpoint failed: %v", err)
	}
	if _, err := h.EnableOrDisableBreakpoints("ws-1", []string{"bp-1"}, true); err != nil {
		t.Fatalf("EnableOrDisableBreakpoints failed: %v", err)
	}
	if _, err := h.DeleteAllBreakpoints("ws-1"); err != nil {
		t.Fatalf("DeleteAllBreakpoints failed: %v", err)
	}
}

func TestGetOrCreateWorkspace_MissingWorkspaceID(t *testing.T) {
	h := newGraphQLTestHandler(t, http.StatusOK, func(body map[string]interface{}) map[string]interface{} {
		return map[string]interface{}{"data": map[string]interface{}{"org": map[string]interface{}{"getOrCreateUserWorkspaceV2": map[string]interface{}{}}}}
	})

	resp, workspaceID, err := h.GetOrCreateWorkspace("proj")
	if err == nil {
		t.Fatalf("expected missing workspace id error")
	}
	if resp == nil || workspaceID != "" {
		t.Fatalf("unexpected response/id on error: resp=%#v id=%q", resp, workspaceID)
	}
}
