package apply

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Dynatrace/dtctl/pkg/client"
	"github.com/Dynatrace/dtctl/pkg/config"
	"github.com/Dynatrace/dtctl/pkg/safety"
)

func TestDetectSchedulingRule(t *testing.T) {
	input := `{"title":"Business Hours","ruleType":"rrule","rrule":{"freq":"WEEKLY","datestart":"2026-01-05","byday":["MO","TU","WE","TH","FR"]}}`
	rt, isArray, err := detectResourceType([]byte(input))
	if err != nil {
		t.Fatalf("detectResourceType: %v", err)
	}
	if rt != ResourceSchedulingRule {
		t.Errorf("detected = %q, want %q", rt, ResourceSchedulingRule)
	}
	if isArray {
		t.Error("isArray = true, want false")
	}
}

func TestDetectSchedulingRuleWithID(t *testing.T) {
	input := `{"id":"sr-1","title":"Business Hours","ruleType":"rrule","rrule":{"freq":"DAILY","datestart":"2026-01-05"}}`
	rt, _, err := detectResourceType([]byte(input))
	if err != nil {
		t.Fatalf("detectResourceType: %v", err)
	}
	if rt != ResourceSchedulingRule {
		t.Errorf("detected = %q, want %q", rt, ResourceSchedulingRule)
	}
}

func makeSchedulingRuleTestServer(t *testing.T) (*httptest.Server, *client.Client) {
	t.Helper()

	mux := http.NewServeMux()
	mux.HandleFunc("/platform/automation/v1/scheduling-rules", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		w.Write([]byte(`{"id":"sr-new","title":"Business Hours","ruleType":"rrule","rrule":{"freq":"WEEKLY","datestart":"2026-01-05","byday":["MO","TU","WE","TH","FR"]}}`))
	})
	mux.HandleFunc("/platform/automation/v1/scheduling-rules/sr-existing", func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`{"id":"sr-existing","title":"Business Hours","ruleType":"rrule","rrule":{"freq":"WEEKLY","datestart":"2026-01-05","byday":["MO","TU","WE","TH","FR"]},"modificationInfo":{"createdBy":"user-1","createdTime":"2026-01-05T08:00:00Z","lastModifiedBy":"user-1","lastModifiedTime":"2026-01-05T08:00:00Z"}}`))
		case http.MethodPut:
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`{"id":"sr-existing","title":"Business Hours Updated","ruleType":"rrule","rrule":{"freq":"DAILY","datestart":"2026-01-05"},"modificationInfo":{"createdBy":"user-1","createdTime":"2026-01-05T08:00:00Z","lastModifiedBy":"user-1","lastModifiedTime":"2026-02-01T08:00:00Z"}}`))
		default:
			w.WriteHeader(http.StatusMethodNotAllowed)
		}
	})
	mux.HandleFunc("/platform/automation/v1/scheduling-rules/sr-missing", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		w.Write([]byte(`{"error":{"code":404,"message":"not found"}}`))
	})
	mux.HandleFunc("/platform/automation/v1/scheduling-rules/sr-forbidden", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		w.Write([]byte(`{"error":{"code":403,"message":"forbidden"}}`))
	})
	// Used by NewApplier to get current user ID (best-effort, ok to 401).
	mux.HandleFunc("/platform/metadata/v1/user", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	})

	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	c, err := client.NewForTesting(srv.URL, "test-token")
	if err != nil {
		t.Fatalf("client.NewForTesting: %v", err)
	}
	return srv, c
}

func makeTestApplierForSchedulingRule(t *testing.T, c *client.Client, level config.SafetyLevel) *Applier {
	t.Helper()
	checker := safety.NewChecker("test", &config.Context{
		SafetyLevel: level,
	})
	return NewApplier(c).WithSafetyChecker(checker)
}

func TestApplySchedulingRule_Create(t *testing.T) {
	_, c := makeSchedulingRuleTestServer(t)
	applier := makeTestApplierForSchedulingRule(t, c, config.SafetyLevelReadWriteAll)

	data := []byte(`{"title":"Business Hours","ruleType":"rrule","rrule":{"freq":"WEEKLY","datestart":"2026-01-05","byday":["MO","TU","WE","TH","FR"]}}`)
	results, err := applier.Apply(data, ApplyOptions{})
	if err != nil {
		t.Fatalf("Apply() error: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("len(results) = %d, want 1", len(results))
	}
	r, ok := results[0].(*SchedulingRuleApplyResult)
	if !ok {
		t.Fatalf("result type = %T, want *SchedulingRuleApplyResult", results[0])
	}
	if r.Action != ActionCreated {
		t.Errorf("Action = %q, want %q", r.Action, ActionCreated)
	}
	if r.ID != "sr-new" {
		t.Errorf("ID = %q, want %q", r.ID, "sr-new")
	}
}

func TestApplySchedulingRule_Update(t *testing.T) {
	_, c := makeSchedulingRuleTestServer(t)
	applier := makeTestApplierForSchedulingRule(t, c, config.SafetyLevelReadWriteAll)

	data := []byte(`{"id":"sr-existing","title":"Business Hours Updated","ruleType":"rrule","rrule":{"freq":"DAILY","datestart":"2026-01-05"},"modificationInfo":{"createdBy":"user-1","createdTime":"2026-01-05T08:00:00Z","lastModifiedBy":"user-1","lastModifiedTime":"2026-02-01T08:00:00Z"}}`)
	results, err := applier.Apply(data, ApplyOptions{})
	if err != nil {
		t.Fatalf("Apply() error: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("len(results) = %d, want 1", len(results))
	}
	r, ok := results[0].(*SchedulingRuleApplyResult)
	if !ok {
		t.Fatalf("result type = %T, want *SchedulingRuleApplyResult", results[0])
	}
	if r.Action != ActionUpdated {
		t.Errorf("Action = %q, want %q", r.Action, ActionUpdated)
	}
}

func TestApplySchedulingRule_CreateWithMissingID(t *testing.T) {
	_, c := makeSchedulingRuleTestServer(t)
	applier := makeTestApplierForSchedulingRule(t, c, config.SafetyLevelReadWriteAll)

	// Has an id field, but the resource doesn't exist — should create.
	data := []byte(`{"id":"sr-missing","title":"New Rule","ruleType":"rrule","rrule":{"freq":"DAILY","datestart":"2026-01-05"}}`)
	results, err := applier.Apply(data, ApplyOptions{})
	if err != nil {
		t.Fatalf("Apply() error: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("len(results) = %d, want 1", len(results))
	}
	r, ok := results[0].(*SchedulingRuleApplyResult)
	if !ok {
		t.Fatalf("result type = %T, want *SchedulingRuleApplyResult", results[0])
	}
	if r.Action != ActionCreated {
		t.Errorf("Action = %q, want %q", r.Action, ActionCreated)
	}
}

func TestApplySchedulingRule_ReadonlyBlocked(t *testing.T) {
	_, c := makeSchedulingRuleTestServer(t)
	applier := makeTestApplierForSchedulingRule(t, c, config.SafetyLevelReadOnly)

	data := []byte(`{"title":"Business Hours","ruleType":"rrule","rrule":{"freq":"WEEKLY","datestart":"2026-01-05","byday":["MO","TU","WE","TH","FR"]}}`)
	_, err := applier.Apply(data, ApplyOptions{})
	if err == nil {
		t.Fatal("Apply() expected error for readonly context")
	}
}

func TestApplySchedulingRule_DryRun(t *testing.T) {
	_, c := makeSchedulingRuleTestServer(t)
	applier := makeTestApplierForSchedulingRule(t, c, config.SafetyLevelReadWriteAll)

	data := []byte(`{"title":"Business Hours","ruleType":"rrule","rrule":{"freq":"WEEKLY","datestart":"2026-01-05","byday":["MO","TU","WE","TH","FR"]}}`)
	results, err := applier.Apply(data, ApplyOptions{DryRun: true})
	if err != nil {
		t.Fatalf("Apply(DryRun) error: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("len(results) = %d, want 1", len(results))
	}
	// Dry run result should be a DryRunResult, not a SchedulingRuleApplyResult.
	if _, ok := results[0].(*DryRunResult); !ok {
		t.Fatalf("result type = %T, want *DryRunResult", results[0])
	}
}

// TestSchedulingRuleApplyResultJSON verifies serialization.
func TestSchedulingRuleApplyResultJSON(t *testing.T) {
	r := &SchedulingRuleApplyResult{
		ApplyResultBase: ApplyResultBase{
			Action:       ActionCreated,
			ResourceType: "scheduling-rule",
			ID:           "sr-1",
			Name:         "Business Hours",
		},
	}
	b, err := json.Marshal(r)
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}
	var m map[string]interface{}
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("json.Unmarshal: %v", err)
	}
	if m["action"] != ActionCreated {
		t.Errorf("action = %v, want %q", m["action"], ActionCreated)
	}
}

func TestDetectSchedulingRule_AllRuleTypes(t *testing.T) {
	// ruleType is the API's own discriminator; every value must detect.
	for _, rt := range []string{"rrule", "grouping", "fixed_offset", "relative_offset"} {
		input := `{"title":"x","ruleType":"` + rt + `"}`
		got, _, err := detectResourceType([]byte(input))
		if err != nil {
			t.Fatalf("detectResourceType(%s): %v", rt, err)
		}
		if got != ResourceSchedulingRule {
			t.Errorf("ruleType %q detected = %q, want %q", rt, got, ResourceSchedulingRule)
		}
	}
}

func TestDetectSchedulingRule_WorkflowKeepsPrecedence(t *testing.T) {
	// A workflow that happens to carry a ruleType key must still detect as a workflow.
	input := `{"title":"wf","tasks":{},"ruleType":"rrule","rrule":{"freq":"DAILY","datestart":"2026-01-05"}}`
	rt, _, err := detectResourceType([]byte(input))
	if err != nil {
		t.Fatalf("detectResourceType: %v", err)
	}
	if rt != ResourceWorkflow {
		t.Errorf("detected = %q, want %q", rt, ResourceWorkflow)
	}
}

func TestApplySchedulingRule_LookupErrorIsNotACreate(t *testing.T) {
	// A 403 on the existence check must surface, not silently fall through to
	// create — that would both duplicate the rule and skip the ownership gate.
	_, c := makeSchedulingRuleTestServer(t)
	applier := makeTestApplierForSchedulingRule(t, c, config.SafetyLevelReadWriteAll)

	data := []byte(`{"id":"sr-forbidden","title":"Business Hours","ruleType":"rrule","rrule":{"freq":"DAILY","datestart":"2026-01-05"}}`)
	results, err := applier.Apply(data, ApplyOptions{})
	if err == nil {
		t.Fatalf("Apply() expected error for 403 on lookup, got results: %v", results)
	}
	if !strings.Contains(err.Error(), "sr-forbidden") {
		t.Errorf("error = %q, want it to name the rule being looked up", err.Error())
	}
}
