package apply

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/Dynatrace/dtctl/pkg/resources/anomalydetector"
)

// anomalyDetectorYAML is the definition from issue #369: raw Settings format,
// no description, empty executionSettings. A dry run reported success for it
// while the live apply failed against the schema.
const anomalyDetectorYAML = `
schemaId: builtin:davis.anomaly-detectors
scope: environment
value:
  analyzer:
    input:
      - key: query.expression
        value: timeseries val = avg(redis_connected_clients)
      - key: alertCondition
        value: ABOVE
    name: dt.statistics.ui.anomaly_detection.StaticThresholdAnomalyDetectionAnalyzer
  enabled: true
  eventTemplate:
    properties:
      - key: event.type
        value: CUSTOM_ALERT
      - key: event.name
        value: "Test alert"
  executionSettings: {}
  source: Custom Alerts
  title: "Test alert"
`

// settingsObjectsPath is the endpoint anomaly detectors live on — they are
// Settings objects, so apply and create both target it.
const settingsObjectsPath = "/platform/classic/environment-api/v2/settings/objects"

// newAnomalyDetectorApplier wires an applier against a Settings endpoint that
// runs validate, list, and create through the same handler.
func newAnomalyDetectorApplier(t *testing.T, settings http.HandlerFunc) (*Applier, func()) {
	t.Helper()
	srv, c := newApplyTestServer(t, map[string]http.HandlerFunc{
		"/platform/metadata/v1/user": func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]string{"userId": "e1a88cd0-bfe5-4fa1-b36f-9f85c9fac22e"})
		},
		settingsObjectsPath: settings,
	})
	return NewApplier(c), srv.Close
}

func TestDryRunAnomalyDetector_ReportsSchemaViolations(t *testing.T) {
	var validateOnly string
	a, closeSrv := newAnomalyDetectorApplier(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.Method {
		case http.MethodGet: // title lookup for create-vs-update
			json.NewEncoder(w).Encode(map[string]any{"items": []any{}, "totalCount": 0})
		case http.MethodPost:
			validateOnly = r.URL.Query().Get("validateOnly")
			w.WriteHeader(http.StatusBadRequest)
			fmt.Fprintf(w, `[{"code":400,"error":{"code":400,"message":"Validation failed for 1 Validators.",
				"constraintViolations":[{"path":%q,"message":"Must not be null"}]}}]`,
				anomalydetector.SchemaID+"/0/executionSettings.actor")
		}
	})
	defer closeSrv()

	_, err := a.Apply([]byte(anomalyDetectorYAML), ApplyOptions{DryRun: true})
	if err == nil {
		t.Fatal("Apply(dry-run) error = nil, want the schema violation to fail the dry run")
	}
	if !strings.Contains(err.Error(), "executionSettings.actor: Must not be null") {
		t.Errorf("error = %v, want the offending field and message", err)
	}
	if validateOnly != "true" {
		t.Errorf("validateOnly = %q, want the dry run to validate without persisting", validateOnly)
	}
}

func TestDryRunAnomalyDetector_ValidDefinition(t *testing.T) {
	var posted []map[string]any
	a, closeSrv := newAnomalyDetectorApplier(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.Method {
		case http.MethodGet:
			json.NewEncoder(w).Encode(map[string]any{"items": []any{}, "totalCount": 0})
		case http.MethodPost:
			if r.URL.Query().Get("validateOnly") != "true" {
				t.Errorf("dry run sent a persisting POST")
			}
			if err := json.NewDecoder(r.Body).Decode(&posted); err != nil {
				t.Fatalf("decode POST body: %v", err)
			}
			w.WriteHeader(http.StatusOK)
			w.Write([]byte(`[{"code":200}]`))
		}
	})
	defer closeSrv()

	results, err := a.Apply([]byte(anomalyDetectorYAML), ApplyOptions{DryRun: true})
	if err != nil {
		t.Fatalf("Apply(dry-run) error = %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("got %d results, want 1", len(results))
	}
	result, ok := results[0].(*DryRunResult)
	if !ok {
		t.Fatalf("result is %T, want *DryRunResult", results[0])
	}
	if result.Action != ActionCreated {
		t.Errorf("action = %v, want %v", result.Action, ActionCreated)
	}
	if result.Name != "Test alert" {
		t.Errorf("name = %q, want %q", result.Name, "Test alert")
	}
	if len(result.ValidationWarns) != 0 {
		t.Errorf("validation warnings = %v, want none", result.ValidationWarns)
	}

	// The validated payload must be the one apply would send, schema defaults
	// included — otherwise the dry run checks something else than the real call.
	value := posted[0]["value"].(map[string]any)
	if _, ok := value["description"].(string); !ok {
		t.Errorf("validated payload has no description: %#v", value)
	}
	es, ok := value["executionSettings"].(map[string]any)
	if !ok {
		t.Fatalf("executionSettings = %#v, want an object", value["executionSettings"])
	}
	if es["actor"] != "e1a88cd0-bfe5-4fa1-b36f-9f85c9fac22e" {
		t.Errorf("actor = %#v, want the authenticated identity", es["actor"])
	}
}

func TestDryRunAnomalyDetector_ExistingTitleIsAnUpdate(t *testing.T) {
	const objectID = "vu9U3hXa3q0AAAABAB9-existing"
	a, closeSrv := newAnomalyDetectorApplier(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.Method {
		case http.MethodGet:
			item := map[string]any{
				"objectId":      objectID,
				"schemaId":      anomalydetector.SchemaID,
				"schemaVersion": "1.0.15",
				"scope":         anomalydetector.Scope,
				"value":         map[string]any{"title": "Test alert", "enabled": true},
			}
			if strings.HasSuffix(r.URL.Path, objectID) {
				json.NewEncoder(w).Encode(item)
				return
			}
			json.NewEncoder(w).Encode(map[string]any{"items": []any{item}, "totalCount": 1})
		case http.MethodPut:
			if r.URL.Query().Get("validateOnly") != "true" {
				t.Errorf("dry run sent a persisting PUT")
			}
			w.WriteHeader(http.StatusOK)
			w.Write([]byte(`{"code":200}`))
		}
	})
	defer closeSrv()

	results, err := a.Apply([]byte(anomalyDetectorYAML), ApplyOptions{DryRun: true})
	if err != nil {
		t.Fatalf("Apply(dry-run) error = %v", err)
	}
	result := results[0].(*DryRunResult)
	if result.Action != ActionUpdated {
		t.Errorf("action = %v, want %v", result.Action, ActionUpdated)
	}
	if result.ID != objectID {
		t.Errorf("id = %q, want %q", result.ID, objectID)
	}
}

func TestDryRunAnomalyDetector_UnavailableValidationWarnsOnly(t *testing.T) {
	a, closeSrv := newAnomalyDetectorApplier(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.Method {
		case http.MethodGet:
			json.NewEncoder(w).Encode(map[string]any{"items": []any{}, "totalCount": 0})
		case http.MethodPost:
			w.WriteHeader(http.StatusForbidden)
			w.Write([]byte(`{"error":{"code":403,"message":"missing scope"}}`))
		}
	})
	defer closeSrv()

	results, err := a.Apply([]byte(anomalyDetectorYAML), ApplyOptions{DryRun: true})
	if err != nil {
		t.Fatalf("Apply(dry-run) error = %v, want a warning rather than a failure", err)
	}
	result := results[0].(*DryRunResult)
	if len(result.ValidationWarns) == 0 || !strings.Contains(result.ValidationWarns[0], "schema validation skipped") {
		t.Errorf("validation warnings = %v, want a skipped-validation warning", result.ValidationWarns)
	}
}
