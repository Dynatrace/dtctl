package output

import (
	"bytes"
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/dynatrace-oss/dtctl/pkg/resources/analyzer"
	"github.com/dynatrace-oss/dtctl/pkg/resources/anomalydetector"
	"github.com/dynatrace-oss/dtctl/pkg/resources/api"
	"github.com/dynatrace-oss/dtctl/pkg/resources/appengine"
	"github.com/dynatrace-oss/dtctl/pkg/resources/azureconnection"
	"github.com/dynatrace-oss/dtctl/pkg/resources/azuremonitoringconfig"
	"github.com/dynatrace-oss/dtctl/pkg/resources/bucket"
	"github.com/dynatrace-oss/dtctl/pkg/resources/document"
	"github.com/dynatrace-oss/dtctl/pkg/resources/dqlprocessorverify"
	"github.com/dynatrace-oss/dtctl/pkg/resources/edgeconnect"
	"github.com/dynatrace-oss/dtctl/pkg/resources/extension"
	"github.com/dynatrace-oss/dtctl/pkg/resources/gcpconnection"
	"github.com/dynatrace-oss/dtctl/pkg/resources/gcpmonitoringconfig"
	"github.com/dynatrace-oss/dtctl/pkg/resources/hub"
	"github.com/dynatrace-oss/dtctl/pkg/resources/iam"
	"github.com/dynatrace-oss/dtctl/pkg/resources/matcherlqltodql"
	"github.com/dynatrace-oss/dtctl/pkg/resources/matcherverify"
	"github.com/dynatrace-oss/dtctl/pkg/resources/platform"
	"github.com/dynatrace-oss/dtctl/pkg/resources/platformtoken"
	"github.com/dynatrace-oss/dtctl/pkg/resources/previewprocessor"
	"github.com/dynatrace-oss/dtctl/pkg/resources/schedulingrule"
	"github.com/dynatrace-oss/dtctl/pkg/resources/segment"
	"github.com/dynatrace-oss/dtctl/pkg/resources/serviceuser"
	"github.com/dynatrace-oss/dtctl/pkg/resources/settings"
	"github.com/dynatrace-oss/dtctl/pkg/resources/slo"
	"github.com/dynatrace-oss/dtctl/pkg/resources/workflow"
	sdkmatcherverify "github.com/dynatrace-oss/dtctl/sdk/api/matcherverify"
)

// -update flag: regenerate golden files
// Run: go test ./pkg/output/ -update
var updateGolden = flag.Bool("update", false, "update golden files")

var ansiRegex = regexp.MustCompile(`\x1b\[[0-9;]*[a-zA-Z]`)

func stripANSI(s string) string {
	return ansiRegex.ReplaceAllString(s, "")
}

func assertGolden(t *testing.T, name string, actual string) {
	t.Helper()
	goldenPath := filepath.Join("testdata", "golden", name+".golden")

	if *updateGolden {
		dir := filepath.Dir(goldenPath)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("failed to create golden dir: %v", err)
		}
		if err := os.WriteFile(goldenPath, []byte(actual), 0o644); err != nil {
			t.Fatalf("failed to write golden file: %v", err)
		}
		t.Logf("updated golden file: %s", goldenPath)
		return
	}

	expected, err := os.ReadFile(goldenPath)
	if err != nil {
		t.Fatalf("golden file not found: %s\nRun with -update to create:\n  go test ./pkg/output/ -update\n\nActual output:\n%s", goldenPath, actual)
	}

	if string(expected) != actual {
		t.Errorf("output does not match golden file %s\n\n--- expected ---\n%s\n--- actual ---\n%s\n--- diff hint ---\nRun with -update to accept the new output:\n  go test ./pkg/output/ -update",
			goldenPath, string(expected), actual)
	}
}

// ---------------------------------------------------------------------------
// Test fixtures using REAL production structs with realistic synthetic data.
//
// All data is fictional — no real customer names, environment IDs, or tokens.
// IDs use realistic formats (UUIDs, base64-encoded settings object IDs).
// ---------------------------------------------------------------------------

var fixedTime = time.Date(2025, 3, 15, 10, 30, 0, 0, time.UTC)

// workflowFixtures mirrors a real `get workflows` (list) response, which the SDK
// restricts to listFields (no tasks/input/result/guide — see sdk/api/workflow).
func workflowFixtures() []workflow.Workflow {
	return []workflow.Workflow{
		{
			ID:            "a1b2c3d4-e5f6-4a7b-8c9d-0e1f2a3b4c5d",
			Title:         "Deploy to Production",
			IsDeployed:    true,
			Description:   "Deploys latest build to prod environment",
			Actor:         "7a8b9c0d-1e2f-4a3b-8c4d-5e6f7a8b9c0d",
			Owner:         "7a8b9c0d-1e2f-4a3b-8c4d-5e6f7a8b9c0d",
			OwnerType:     "USER",
			Private:       false,
			SchemaVersion: 4,
			Trigger: map[string]interface{}{
				"schedule": map[string]interface{}{
					"trigger": map[string]interface{}{"type": "cron", "cron": "0 9 * * 1-5"},
				},
			},
			TriggerType:          "Schedule",
			Type:                 "STANDARD",
			HourlyExecutionLimit: intPtr(1000),
		},
		{
			ID:          "b2c3d4e5-f6a7-4b8c-9d0e-1f2a3b4c5d6e",
			Title:       "Daily Cleanup",
			IsDeployed:  true,
			Description: "Removes stale resources older than 30 days",
			Type:        "STANDARD",
			TriggerType: "Manual",
			Owner:       "00000000-0000-0000-0000-000000000000",
			OwnerType:   "USER",
			Private:     false,
		},
		{
			ID:          "c3d4e5f6-a7b8-4c9d-0e1f-2a3b4c5d6e7f",
			Title:       "Incident Response",
			IsDeployed:  false,
			Description: "",
			Type:        "STANDARD",
			TriggerType: "Event",
			Owner:       "8b9c0d1e-2f3a-4b5c-6d7e-8f9a0b1c2d3e",
			OwnerType:   "USER",
			Private:     true,
		},
	}
}

// describeWorkflowFixture is the full single-workflow response returned by Get
// (used by `describe workflow`), including tasks/input/result/guide.
func describeWorkflowFixture() workflow.Workflow {
	wf := workflowFixtures()[0]
	wf.Result = stringPtr("{{ result('deploy') }}")
	wf.Input = map[string]interface{}{"environment": map[string]interface{}{"type": "string"}}
	wf.Guide = stringPtr("# Deploy to Production\n\nRun this workflow after validating the release candidate.")
	wf.Tasks = map[string]interface{}{
		"deploy": map[string]interface{}{
			"action": "dynatrace.automations:run-javascript",
			"input":  map[string]interface{}{"script": "// deploy logic"},
		},
	}
	return wf
}

func sloFixtures() []slo.SLO {
	return []slo.SLO{
		{
			ID:          "a1b2c3d4-0001-4000-8000-000000000001",
			Name:        "API Availability",
			Description: "99.9% availability for public API endpoints",
			Version:     "3",
			Criteria: []slo.Criteria{
				{TimeframeFrom: "-7d", Target: 99.9, Warning: float64Ptr(99.5)},
			},
			Tags: []string{"service:api", "tier:1"},
		},
		{
			ID:          "a1b2c3d4-0002-4000-8000-000000000002",
			Name:        "Checkout Latency",
			Description: "P95 response time under 500ms for checkout flow",
			Version:     "1",
			Criteria: []slo.Criteria{
				{TimeframeFrom: "-30d", Target: 95.0},
			},
			Tags: []string{"service:checkout"},
		},
		{
			ID:          "a1b2c3d4-0003-4000-8000-000000000003",
			Name:        "Error Rate",
			Description: "Error rate below 0.1% across all services",
			Version:     "5",
		},
	}
}

func float64Ptr(v float64) *float64 { return &v }
func int64Ptr(v int64) *int64       { return &v }
func intPtr(v int) *int             { return &v }
func boolPtr(v bool) *bool          { return &v }
func stringPtr(v string) *string    { return &v }

func bucketFixtures() []bucket.Bucket {
	return []bucket.Bucket{
		{
			BucketName:    "default_logs",
			Table:         "logs",
			DisplayName:   "Default Logs",
			Status:        "active",
			RetentionDays: 35,
			Updatable:     true,
			Version:       1,
			Records:       int64Ptr(1250000),
		},
		{
			BucketName:             "custom_metrics",
			Table:                  "metrics",
			DisplayName:            "Custom Metrics",
			Status:                 "active",
			RetentionDays:          90,
			IncludedQueryLimitDays: 30,
			MetricInterval:         "PT1M",
			Updatable:              true,
			Version:                3,
			Records:                int64Ptr(8750000),
		},
		{
			BucketName:    "security_events",
			Table:         "logs",
			DisplayName:   "Security Events",
			Status:        "active",
			RetentionDays: 365,
			Updatable:     false,
			Version:       2,
			Records:       int64Ptr(42000),
		},
	}
}

func documentFixtures() []document.Document {
	return []document.Document{
		{
			ID:          "b1c2d3e4-f5a6-4b7c-8d9e-0f1a2b3c4d5e",
			Name:        "Production Overview",
			Type:        "dashboard",
			Owner:       "7a8b9c0d-1e2f-4a3b-8c4d-5e6f7a8b9c0d",
			IsPrivate:   false,
			Created:     fixedTime,
			Description: "Main production monitoring dashboard",
			Version:     3,
			Modified:    fixedTime.Add(2 * time.Hour),
		},
		{
			ID:        "c2d3e4f5-a6b7-4c8d-9e0f-1a2b3c4d5e6f",
			Name:      "Runbook: Incident Response",
			Type:      "notebook",
			Owner:     "8b9c0d1e-2f3a-4b5c-6d7e-8f9a0b1c2d3e",
			IsPrivate: true,
			Created:   fixedTime.Add(-24 * time.Hour),
			Version:   1,
			Modified:  fixedTime.Add(-12 * time.Hour),
		},
		{
			ID:          "d3e4f5a6-b7c8-4d9e-0f1a-2b3c4d5e6f7a",
			Name:        "Performance Dashboard",
			Type:        "dashboard",
			Owner:       "9c0d1e2f-3a4b-5c6d-7e8f-9a0b1c2d3e4f",
			IsPrivate:   false,
			Created:     fixedTime.Add(-72 * time.Hour),
			Description: "Service performance metrics and SLOs",
			Version:     7,
			Modified:    fixedTime.Add(-1 * time.Hour),
		},
	}
}

func settingsFixtures() []settings.SettingsObject {
	return []settings.SettingsObject{
		{
			ObjectID:      "vu9U3hXa3q0AAAABABhidWlsdGluOmFsZXJ0aW5nLnByb2ZpbGUABnRlbmFudAAGdGVuYW50ACRhMWIyYzNkNC1lNWY2LTRhN2ItOGM5ZC0wZTFmMmEzYjRjNWQ",
			SchemaID:      "builtin:alerting.profile",
			SchemaVersion: "1.0.5",
			Scope:         "environment",
			Summary:       "Default Alerting Profile",
			Value: map[string]any{
				"name":            "Default",
				"severityRules":   []any{},
				"eventTypeFilter": []any{},
			},
			ObjectIDShort: "vu9U3hXa3q0AAAABAB...",
			ScopeType:     "tenant",
			ScopeID:       "tenant",
		},
		{
			ObjectID:      "vu9U3hXa3q0AAAABABxidWlsdGluOnByb2JsZW0ubm90aWZpY2F0aW9ucwAGdGVuYW50AAZ0ZW5hbnQAJGIyYzNkNGU1LWY2YTctNGI4Yy05ZDBlLTFmMmEzYjRjNWQ2ZQ",
			SchemaID:      "builtin:problem.notifications",
			SchemaVersion: "2.1.0",
			Scope:         "environment",
			Summary:       "Email Notification",
			Value: map[string]any{
				"enabled":    true,
				"type":       "EMAIL",
				"recipients": "oncall-team@example.invalid",
			},
			ObjectIDShort: "vu9U3hXa3q0AAAABAB...",
			ScopeType:     "tenant",
			ScopeID:       "tenant",
		},
		{
			ObjectID:      "vu9U3hXa3q0AAAABABlidWlsdGluOnRhZ3MuYXV0by10YWdnaW5nAAZ0ZW5hbnQABnRlbmFudAAkYzNkNGU1ZjYtYTdiOC00YzlkLTBlMWYtMmEzYjRjNWQ2ZTdm",
			SchemaID:      "builtin:tags.auto-tagging",
			SchemaVersion: "3.0.2",
			Scope:         "environment",
			Summary:       "Environment Tag Rule",
			Value: map[string]any{
				"name":  "environment",
				"rules": []any{},
			},
			ObjectIDShort: "vu9U3hXa3q0AAAABAB...",
			ScopeType:     "tenant",
			ScopeID:       "tenant",
		},
	}
}

func executionFixtures() []workflow.Execution {
	return []workflow.Execution{
		{
			ID:          "d4e5f6a7-b8c9-4d0e-1f2a-3b4c5d6e7f8a",
			Workflow:    "a1b2c3d4-e5f6-4a7b-8c9d-0e1f2a3b4c5d",
			Title:       "Deploy to Production",
			State:       "SUCCEEDED",
			StartedAt:   fixedTime,
			Runtime:     45,
			TriggerType: "Schedule",
		},
		{
			ID:          "e5f6a7b8-c9d0-4e1f-2a3b-4c5d6e7f8a9b",
			Workflow:    "a1b2c3d4-e5f6-4a7b-8c9d-0e1f2a3b4c5d",
			Title:       "Deploy to Production",
			State:       "FAILED",
			StartedAt:   fixedTime.Add(-time.Hour),
			Runtime:     12,
			TriggerType: "Manual",
		},
		{
			ID:          "f6a7b8c9-d0e1-4f2a-3b4c-5d6e7f8a9b0c",
			Workflow:    "b2c3d4e5-f6a7-4b8c-9d0e-1f2a3b4c5d6e",
			Title:       "Daily Cleanup",
			State:       "RUNNING",
			StartedAt:   fixedTime.Add(-5 * time.Minute),
			Runtime:     0,
			TriggerType: "Schedule",
		},
	}
}

func dqlRecordsFixture() []map[string]interface{} {
	return []map[string]interface{}{
		{"timestamp": "2025-03-15T10:30:00Z", "host.name": "web-server-01", "status": "ERROR", "content": "Connection timeout to database"},
		{"timestamp": "2025-03-15T10:29:55Z", "host.name": "web-server-02", "status": "WARN", "content": "High memory usage detected"},
		{"timestamp": "2025-03-15T10:29:50Z", "host.name": "api-gateway", "status": "INFO", "content": "Request processed successfully"},
	}
}

func dqlTimeseriesFixture() []map[string]interface{} {
	return []map[string]interface{}{
		{
			"timeframe": map[string]interface{}{
				"start": "2025-03-15T09:00:00Z",
				"end":   "2025-03-15T10:00:00Z",
			},
			"interval": 300000000000, // 5 min in nanoseconds
			"avg(dt.host.cpu.usage)": []interface{}{
				12.5, 15.3, 18.7, 22.1, 35.6,
				42.3, 38.9, 25.4, 19.8, 14.2,
				11.1, 13.7,
			},
		},
	}
}

// dqlMultiSeriesTimeseriesFixture returns two records with different dimension labels,
// producing a multi-series chart that exercises the PlotMany code path.
// interval must be a string (nanoseconds) to match the real DQL API response format.
func dqlMultiSeriesTimeseriesFixture() []map[string]interface{} {
	return []map[string]interface{}{
		{
			"timeframe": map[string]interface{}{
				"start": "2025-03-15T09:00:00Z",
				"end":   "2025-03-15T10:00:00Z",
			},
			"interval":               "300000000000",
			"dt.entity.host":         "HOST-A",
			"avg(dt.host.cpu.usage)": []interface{}{12.5, 15.3, 18.7, 22.1, 35.6, 42.3, 38.9, 25.4, 19.8, 14.2, 11.1, 13.7},
		},
		{
			"timeframe": map[string]interface{}{
				"start": "2025-03-15T09:00:00Z",
				"end":   "2025-03-15T10:00:00Z",
			},
			"interval":               "300000000000",
			"dt.entity.host":         "HOST-B",
			"avg(dt.host.cpu.usage)": []interface{}{55.0, 58.2, 61.4, 59.9, 57.3, 62.1, 65.8, 63.0, 60.5, 58.8, 56.2, 54.7},
		},
	}
}

func extensionFixtures() []extension.Extension {
	return []extension.Extension{
		{
			ExtensionName: "com.dynatrace.extension.host-monitoring",
			Version:       "1.2.3",
			ActiveVersion: "1.2.0",
		},
		{
			ExtensionName: "com.dynatrace.extension.jmx",
			Version:       "2.0.1",
		},
		{
			ExtensionName: "custom:my-custom-extension",
			Version:       "",
		},
	}
}

func extensionVersionFixtures() []extension.ExtensionVersion {
	return []extension.ExtensionVersion{
		{
			Version:       "1.2.3",
			ExtensionName: "com.dynatrace.extension.host-monitoring",
			Active:        true,
		},
		{
			Version:       "1.2.2",
			ExtensionName: "com.dynatrace.extension.host-monitoring",
			Active:        false,
		},
		{
			Version:       "1.1.0",
			ExtensionName: "com.dynatrace.extension.host-monitoring",
			Active:        false,
		},
	}
}

func segmentFixtures() []segment.FilterSegment {
	return []segment.FilterSegment{
		{
			UID:         "a1b2c3d4-e5f6-4a7b-8c9d-seg000000001",
			Name:        "Kubernetes Alpha",
			Description: "Filters data for Kubernetes cluster alpha",
			IsPublic:    true,
			Owner:       "admin@example.invalid",
			Version:     3,
			Includes: []segment.Include{
				{DataObject: "_all_data_object", Filter: `k8s.cluster.name = "alpha"`},
				{DataObject: "logs", Filter: `dt.system.bucket = "custom-logs"`},
			},
			Variables: &segment.Variables{
				Type:  "query",
				Value: `fetch logs | limit 1`,
			},
			VariablesDisplay:  "Yes",
			AllowedOperations: []string{"READ", "WRITE", "DELETE"},
		},
		{
			UID:         "b2c3d4e5-f6a7-4b8c-9d0e-seg000000002",
			Name:        "Production Services",
			Description: "All production microservices",
			IsPublic:    false,
			Owner:       "platform-team@example.invalid",
			Version:     1,
			Includes: []segment.Include{
				{DataObject: "spans", Filter: `environment = "production"`},
			},
			AllowedOperations: []string{"READ"},
		},
		{
			UID:      "c3d4e5f6-a7b8-4c9d-0e1f-seg000000003",
			Name:     "Error Logs",
			IsPublic: true,
			Owner:    "sre-team@example.invalid",
			Version:  7,
			Includes: []segment.Include{
				{DataObject: "logs", Filter: `status = "ERROR"`},
			},
			AllowedOperations: []string{"READ", "WRITE", "DELETE"},
		},
	}
}

func describeSegmentFixture() segment.FilterSegment {
	return segmentFixtures()[0]
}

func anomalyDetectorFixtures() []anomalydetector.AnomalyDetector {
	return []anomalydetector.AnomalyDetector{
		{
			ObjectID:      "vu9U3hXa3q0AAAABACdidWlsdGluOmRhdmlzLmFub21hbHktZGV0ZWN0b3JzAA",
			Title:         "Aurora cluster CPU utilization",
			Enabled:       true,
			AnalyzerShort: "static (>90)",
			EventType:     "PERFORMANCE_EVENT",
			Source:        "Clouds",
			Description:   "Monitors Aurora cluster CPU utilization to detect performance bottlenecks",
			Value: map[string]any{
				"title":       "Aurora cluster CPU utilization",
				"enabled":     true,
				"description": "Monitors Aurora cluster CPU utilization to detect performance bottlenecks",
				"source":      "Clouds",
				"analyzer": map[string]any{
					"name": "dt.statistics.ui.anomaly_detection.StaticThresholdAnomalyDetectionAnalyzer",
					"input": []any{
						map[string]any{"key": "alertCondition", "value": "ABOVE"},
						map[string]any{"key": "threshold", "value": "90"},
						map[string]any{"key": "slidingWindow", "value": "5"},
						map[string]any{"key": "violatingSamples", "value": "3"},
						map[string]any{"key": "dealertingSamples", "value": "5"},
						map[string]any{"key": "query", "value": "timeseries cpu=avg(cloud.aws.rds.CPUUtilization), interval:1m"},
					},
				},
				"eventTemplate": map[string]any{
					"properties": []any{
						map[string]any{"key": "event.type", "value": "PERFORMANCE_EVENT"},
						map[string]any{"key": "event.name", "value": "Aurora cluster high CPU"},
						map[string]any{"key": "event.description", "value": "CPU utilization is high"},
					},
				},
			},
			SchemaVersion: "1.0.15",
		},
		{
			ObjectID:      "xw0V4iYb4r1BBBBBAC1idWlsdGluOmRhdmlzLmFub21hbHktZGV0ZWN0b3JzAB",
			Title:         "VPC endpoint packet drops",
			Enabled:       true,
			AnalyzerShort: "auto-adaptive",
			EventType:     "AVAILABILITY_EVENT",
			Source:        "Clouds",
			Value: map[string]any{
				"title":   "VPC endpoint packet drops",
				"enabled": true,
				"source":  "Clouds",
				"analyzer": map[string]any{
					"name":  "dt.statistics.ui.anomaly_detection.AutoAdaptiveAnomalyDetectionAnalyzer",
					"input": []any{},
				},
				"eventTemplate": map[string]any{
					"properties": []any{
						map[string]any{"key": "event.type", "value": "AVAILABILITY_EVENT"},
						map[string]any{"key": "event.name", "value": "VPC packet drops detected"},
					},
				},
			},
			SchemaVersion: "1.0.15",
		},
		{
			ObjectID:      "yz1W5jZc5s2CCCCCAD2idWlsdGluOmRhdmlzLmFub21hbHktZGV0ZWN0b3JzAC",
			Title:         "EC2 instance CPU utilization",
			Enabled:       false,
			AnalyzerShort: "static (>85)",
			EventType:     "PERFORMANCE_EVENT",
			Source:        "dtctl",
			Description:   "Disabled detector for EC2 CPU monitoring",
			Value: map[string]any{
				"title":       "EC2 instance CPU utilization",
				"enabled":     false,
				"description": "Disabled detector for EC2 CPU monitoring",
				"source":      "dtctl",
				"analyzer": map[string]any{
					"name": "dt.statistics.ui.anomaly_detection.StaticThresholdAnomalyDetectionAnalyzer",
					"input": []any{
						map[string]any{"key": "alertCondition", "value": "ABOVE"},
						map[string]any{"key": "threshold", "value": "85"},
					},
				},
				"eventTemplate": map[string]any{
					"properties": []any{
						map[string]any{"key": "event.type", "value": "PERFORMANCE_EVENT"},
						map[string]any{"key": "event.name", "value": "EC2 high CPU utilization"},
					},
				},
			},
			SchemaVersion: "1.0.15",
		},
	}
}

func describeAnomalyDetectorFixture() anomalydetector.AnomalyDetector {
	return anomalyDetectorFixtures()[0]
}

func monitoringConfigFixtures() []extension.MonitoringConfiguration {
	return []extension.MonitoringConfiguration{
		{
			ExtensionName: "com.dynatrace.extension.host-monitoring",
			ObjectID:      "a1b2c3d4-e5f6-4a7b-8c9d-000000000001",
			Scope:         "environment",
		},
		{
			ExtensionName: "com.dynatrace.extension.host-monitoring",
			ObjectID:      "a1b2c3d4-e5f6-4a7b-8c9d-000000000002",
			Scope:         "HOST-ABC123",
		},
		{
			ExtensionName: "com.dynatrace.extension.jmx",
			ObjectID:      "b2c3d4e5-f6a7-4b8c-9d0e-000000000003",
			Scope:         "HOST_GROUP-XYZ789",
		},
	}
}

// ---------------------------------------------------------------------------
// Golden tests: text formats (table, wide, json, yaml, csv)
// ---------------------------------------------------------------------------

func TestGolden_GetWorkflows(t *testing.T) {
	workflows := workflowFixtures()

	formats := map[string]string{
		"table": "table",
		"wide":  "wide",
		"json":  "json",
		"yaml":  "yaml",
		"csv":   "csv",
		"toon":  "toon",
	}

	for name, format := range formats {
		t.Run(name, func(t *testing.T) {
			var buf bytes.Buffer
			printer := NewPrinterWithWriter(format, &buf)
			if err := printer.PrintList(workflows); err != nil {
				t.Fatalf("PrintList failed: %v", err)
			}
			assertGolden(t, "get/workflows-"+name, buf.String())
		})
	}
}

func TestGolden_GetSLOs(t *testing.T) {
	slos := sloFixtures()

	formats := map[string]string{
		"table": "table",
		"wide":  "wide",
		"json":  "json",
		"yaml":  "yaml",
		"csv":   "csv",
		"toon":  "toon",
	}

	for name, format := range formats {
		t.Run(name, func(t *testing.T) {
			var buf bytes.Buffer
			printer := NewPrinterWithWriter(format, &buf)
			if err := printer.PrintList(slos); err != nil {
				t.Fatalf("PrintList failed: %v", err)
			}
			assertGolden(t, "get/slos-"+name, buf.String())
		})
	}
}

func TestGolden_GetBuckets(t *testing.T) {
	buckets := bucketFixtures()

	formats := map[string]string{
		"table": "table",
		"wide":  "wide",
		"json":  "json",
		"yaml":  "yaml",
		"csv":   "csv",
		"toon":  "toon",
	}

	for name, format := range formats {
		t.Run(name, func(t *testing.T) {
			var buf bytes.Buffer
			printer := NewPrinterWithWriter(format, &buf)
			if err := printer.PrintList(buckets); err != nil {
				t.Fatalf("PrintList failed: %v", err)
			}
			assertGolden(t, "get/buckets-"+name, buf.String())
		})
	}
}

func TestGolden_GetDocuments(t *testing.T) {
	docs := documentFixtures()

	formats := map[string]string{
		"table": "table",
		"wide":  "wide",
		"json":  "json",
		"yaml":  "yaml",
		"csv":   "csv",
		"toon":  "toon",
	}

	for name, format := range formats {
		t.Run(name, func(t *testing.T) {
			var buf bytes.Buffer
			printer := NewPrinterWithWriter(format, &buf)
			if err := printer.PrintList(docs); err != nil {
				t.Fatalf("PrintList failed: %v", err)
			}
			assertGolden(t, "get/documents-"+name, buf.String())
		})
	}
}

func TestGolden_ClaimEnvironmentShare(t *testing.T) {
	claim := &document.EnvironmentShareClaim{
		DocumentID:   "doc-1234",
		Name:         "Prod overview",
		DocumentType: "dashboard",
		Access:       []string{"read"},
		AccessLevel:  "read",
		URL:          "https://abc.apps.dynatrace.com/ui/apps/dynatrace.dashboards/dashboard/doc-1234",
	}

	for _, format := range []string{"table", "wide", "json", "yaml"} {
		t.Run(format, func(t *testing.T) {
			var buf bytes.Buffer
			printer := NewPrinterWithWriter(format, &buf)
			if err := printer.Print(claim); err != nil {
				t.Fatalf("Print failed: %v", err)
			}
			assertGolden(t, "claim/environment-share-"+format, buf.String())
		})
	}
}

// dashboardWithContentFixture models a single dashboard as returned by
// `get dashboard <id>` — with a populated Content body. This is the path the
// content-as-raw-bytes regression broke; the list fixtures above never set
// Content, so it must be guarded explicitly here.
func dashboardWithContentFixture() document.Document {
	return document.Document{
		ID:        "c8e42bc8-a9bd-433f-85c7-343017c0836a",
		Name:      "Smartscape Overview",
		Type:      "dashboard",
		Owner:     "user-a@example.invalid",
		IsPrivate: false,
		Created:   fixedTime,
		Version:   784,
		Modified:  fixedTime.Add(2 * time.Hour),
		Content: []byte(`{"version":18,"tiles":{"0":{"type":"data",` +
			`"title":"Host count","query":"fetch dt.entity.host | summarize count()"}},` +
			`"layouts":{"0":{"x":0,"y":0,"w":12,"h":6}}}`),
	}
}

func TestGolden_GetDashboardWithContent(t *testing.T) {
	doc := dashboardWithContentFixture()

	formats := map[string]string{
		"json": "json",
		"yaml": "yaml",
	}

	for name, format := range formats {
		t.Run(name, func(t *testing.T) {
			var buf bytes.Buffer
			printer := NewPrinterWithWriter(format, &buf)
			if err := printer.Print(doc); err != nil {
				t.Fatalf("Print failed: %v", err)
			}
			assertGolden(t, "get/dashboard-content-"+name, buf.String())
		})
	}
}

// analyzerDefinitionFixture models a single analyzer as returned by
// `get analyzer <name>`. Input/Output are json.RawMessage ([]byte): without a
// MarshalYAML they render as a list of raw byte values in YAML, and the
// display-only CategoryName (json:"-") would leak in with reflection casing.
func analyzerDefinitionFixture() analyzer.AnalyzerDefinition {
	return analyzer.AnalyzerDefinition{
		Name:         "dt.statistics.GenericForecastAnalyzer",
		DisplayName:  "Generic Forecast Analyzer",
		Description:  "Forecasts a numeric time series",
		Type:         "DAVIS",
		CategoryName: "Forecast",
		BaseAnalyzer: "dt.statistics.BaseForecastAnalyzer",
		Input:        json.RawMessage(`{"fields":[{"name":"timeSeriesData","type":"timeseries","required":true}]}`),
		Output:       json.RawMessage(`{"fields":[{"name":"forecastValues","type":"timeseries"}]}`),
	}
}

func TestGolden_GetAnalyzerDefinition(t *testing.T) {
	def := analyzerDefinitionFixture()

	formats := map[string]string{
		"json": "json",
		"yaml": "yaml",
	}

	for name, format := range formats {
		t.Run(name, func(t *testing.T) {
			var buf bytes.Buffer
			printer := NewPrinterWithWriter(format, &buf)
			if err := printer.Print(def); err != nil {
				t.Fatalf("Print failed: %v", err)
			}
			assertGolden(t, "get/analyzer-definition-"+name, buf.String())
		})
	}
}

// analyzerDescriptionFixture models the enriched view returned by
// `describe analyzer <name>`, bundling resolved input/result JSON Schemas. Only
// structured formats flow through the printer (table uses custom rendering in
// cmd), so this covers the json/yaml serialization.
func analyzerDescriptionFixture() analyzer.AnalyzerDescription {
	return analyzer.AnalyzerDescription{
		Name:        "dt.statistics.GenericForecastAnalyzer",
		DisplayName: "Generic Forecast Analyzer",
		Description: "Forecasts a numeric time series",
		Category:    "Forecast",
		Type:        "DAVIS",
		Labels:      []string{"forecast", "timeseries"},
		InputSchema: map[string]interface{}{
			"type":     "object",
			"required": []interface{}{"timeSeriesData"},
			"properties": map[string]interface{}{
				"timeSeriesData":  map[string]interface{}{"type": "string", "description": "DQL timeseries query"},
				"forecastHorizon": map[string]interface{}{"type": "integer", "description": "points to forecast"},
			},
		},
		ResultSchema: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"forecastValues": map[string]interface{}{"type": "array", "description": "forecasted points"},
			},
		},
	}
}

func TestGolden_DescribeAnalyzer(t *testing.T) {
	desc := analyzerDescriptionFixture()

	formats := map[string]string{
		"json": "json",
		"yaml": "yaml",
	}

	for name, format := range formats {
		t.Run(name, func(t *testing.T) {
			var buf bytes.Buffer
			printer := NewPrinterWithWriter(format, &buf)
			if err := printer.Print(desc); err != nil {
				t.Fatalf("Print failed: %v", err)
			}
			assertGolden(t, "describe/analyzer-"+name, buf.String())
		})
	}
}

// snapshotFixture models a document snapshot as returned by `dtctl history`.
// CreatedBy/CreatedTime are json:"-" display duplicates of ModificationInfo and
// must not leak into json/yaml output.
func snapshotFixture() document.Snapshot {
	return document.Snapshot{
		SnapshotVersion: 3,
		DocumentVersion: 12,
		Description:     "before bulk edit",
		ModificationInfo: document.SnapshotModInfo{
			CreatedBy:   "user-a@example.invalid",
			CreatedTime: fixedTime,
		},
		CreatedBy:   "user-a@example.invalid",
		CreatedTime: fixedTime,
	}
}

func TestGolden_GetSnapshot(t *testing.T) {
	snap := snapshotFixture()

	formats := map[string]string{
		"json": "json",
		"yaml": "yaml",
	}

	for name, format := range formats {
		t.Run(name, func(t *testing.T) {
			var buf bytes.Buffer
			printer := NewPrinterWithWriter(format, &buf)
			if err := printer.Print(snap); err != nil {
				t.Fatalf("Print failed: %v", err)
			}
			assertGolden(t, "get/snapshot-"+name, buf.String())
		})
	}
}

func genericDocumentFixtures() []document.Document {
	return []document.Document{
		{
			ID:        "aaaaaaaa-1111-2222-3333-444444444444",
			Name:      "My Launchpad",
			Type:      "launchpad",
			Owner:     "user-a@example.invalid",
			IsPrivate: false,
			Created:   fixedTime,
			Version:   1,
			Modified:  fixedTime.Add(time.Hour),
		},
		{
			ID:          "bbbbbbbb-2222-3333-4444-555555555555",
			Name:        "App Config",
			Type:        "my-app:config",
			Owner:       "user-b@example.invalid",
			IsPrivate:   true,
			Created:     fixedTime.Add(-48 * time.Hour),
			Description: "Configuration for my custom app",
			Version:     2,
			Modified:    fixedTime.Add(-24 * time.Hour),
		},
		{
			ID:        "cccccccc-3333-4444-5555-666666666666",
			Name:      "Production Overview",
			Type:      "dashboard",
			Owner:     "user-c@example.invalid",
			IsPrivate: false,
			Created:   fixedTime.Add(-72 * time.Hour),
			Version:   5,
			Modified:  fixedTime.Add(-2 * time.Hour),
		},
	}
}

func TestGolden_GetDocumentsGeneric(t *testing.T) {
	docs := genericDocumentFixtures()

	formats := map[string]string{
		"table": "table",
		"wide":  "wide",
		"json":  "json",
		"yaml":  "yaml",
		"csv":   "csv",
		"toon":  "toon",
	}

	for name, format := range formats {
		t.Run(name, func(t *testing.T) {
			var buf bytes.Buffer
			printer := NewPrinterWithWriter(format, &buf)
			if err := printer.PrintList(docs); err != nil {
				t.Fatalf("PrintList failed: %v", err)
			}
			assertGolden(t, "get/documents-generic-"+name, buf.String())
		})
	}
}

// documentsWithAddFieldsFixtures models the output of
// `dtctl get dashboards --add-fields originExtensionId,labels,shareInfo,userContext`
// after ConvertToDocuments has carried the requested fields through.
// Used to lock in two guarantees: (1) JSON/YAML payloads include the
// requested fields, and (2) default table/wide output is unchanged because
// the optional fields are tagged `table:"-"`.
func documentsWithAddFieldsFixtures() []document.Document {
	return []document.Document{
		{
			ID:                "b1c2d3e4-f5a6-4b7c-8d9e-0f1a2b3c4d5e",
			Name:              "Production Overview",
			Type:              "dashboard",
			Owner:             "7a8b9c0d-1e2f-4a3b-8c4d-5e6f7a8b9c0d",
			IsPrivate:         false,
			Created:           fixedTime,
			Description:       "Main production monitoring dashboard",
			Version:           3,
			Modified:          fixedTime.Add(2 * time.Hour),
			OriginExtensionID: "com.dynatrace.example.extension",
			Labels:            []string{"prod", "team-a"},
			ShareInfo:         &document.ShareInfo{IsShared: true, IsSharedWithCurrentUser: true},
			UserContext:       &document.UserContext{LastAccessedTime: fixedTime.Add(time.Hour)},
		},
		{
			ID:        "c2d3e4f5-a6b7-4c8d-9e0f-1a2b3c4d5e6f",
			Name:      "Runbook: Incident Response",
			Type:      "notebook",
			Owner:     "8b9c0d1e-2f3a-4b5c-6d7e-8f9a0b1c2d3e",
			IsPrivate: true,
			Created:   fixedTime.Add(-24 * time.Hour),
			Version:   1,
			Modified:  fixedTime.Add(-12 * time.Hour),
			// Second row deliberately leaves AddFields empty to verify
			// `omitempty` keeps individual rows lean.
		},
	}
}

func TestGolden_GetDocumentsAddFields(t *testing.T) {
	docs := documentsWithAddFieldsFixtures()

	formats := map[string]string{
		"table": "table",
		"wide":  "wide",
		"json":  "json",
		"yaml":  "yaml",
	}

	for name, format := range formats {
		t.Run(name, func(t *testing.T) {
			var buf bytes.Buffer
			printer := NewPrinterWithWriter(format, &buf)
			if err := printer.PrintList(docs); err != nil {
				t.Fatalf("PrintList failed: %v", err)
			}
			assertGolden(t, "get/documents-addfields-"+name, buf.String())
		})
	}
}

func TestGolden_GetSettings(t *testing.T) {
	objs := settingsFixtures()

	formats := map[string]string{
		"table": "table",
		"wide":  "wide",
		"json":  "json",
		"yaml":  "yaml",
		"csv":   "csv",
		"toon":  "toon",
	}

	for name, format := range formats {
		t.Run(name, func(t *testing.T) {
			var buf bytes.Buffer
			printer := NewPrinterWithWriter(format, &buf)
			if err := printer.PrintList(objs); err != nil {
				t.Fatalf("PrintList failed: %v", err)
			}
			assertGolden(t, "get/settings-"+name, buf.String())
		})
	}
}

func TestGolden_GetExtensions(t *testing.T) {
	extensions := extensionFixtures()

	formats := map[string]string{
		"table": "table",
		"wide":  "wide",
		"json":  "json",
		"yaml":  "yaml",
		"csv":   "csv",
	}

	for name, format := range formats {
		t.Run(name, func(t *testing.T) {
			var buf bytes.Buffer
			printer := NewPrinterWithWriter(format, &buf)
			if err := printer.PrintList(extensions); err != nil {
				t.Fatalf("PrintList failed: %v", err)
			}
			assertGolden(t, "get/extensions-"+name, buf.String())
		})
	}
}

func TestGolden_GetExtensionVersions(t *testing.T) {
	versions := extensionVersionFixtures()

	formats := map[string]string{
		"table": "table",
		"wide":  "wide",
		"json":  "json",
		"yaml":  "yaml",
		"csv":   "csv",
	}

	for name, format := range formats {
		t.Run(name, func(t *testing.T) {
			var buf bytes.Buffer
			printer := NewPrinterWithWriter(format, &buf)
			if err := printer.PrintList(versions); err != nil {
				t.Fatalf("PrintList failed: %v", err)
			}
			assertGolden(t, "get/extension-versions-"+name, buf.String())
		})
	}
}

func TestGolden_GetExtensionConfigs(t *testing.T) {
	configs := monitoringConfigFixtures()

	formats := map[string]string{
		"table": "table",
		"wide":  "wide",
		"json":  "json",
		"yaml":  "yaml",
		"csv":   "csv",
	}

	for name, format := range formats {
		t.Run(name, func(t *testing.T) {
			var buf bytes.Buffer
			printer := NewPrinterWithWriter(format, &buf)
			if err := printer.PrintList(configs); err != nil {
				t.Fatalf("PrintList failed: %v", err)
			}
			assertGolden(t, "get/extension-configs-"+name, buf.String())
		})
	}
}

func TestGolden_GetExecutions(t *testing.T) {
	executions := executionFixtures()

	formats := map[string]string{
		"table": "table",
		"json":  "json",
		"yaml":  "yaml",
		"csv":   "csv",
		"toon":  "toon",
	}

	for name, format := range formats {
		t.Run(name, func(t *testing.T) {
			var buf bytes.Buffer
			printer := NewPrinterWithWriter(format, &buf)
			if err := printer.PrintList(executions); err != nil {
				t.Fatalf("PrintList failed: %v", err)
			}
			assertGolden(t, "get/executions-"+name, buf.String())
		})
	}
}

func TestGolden_GetSegments(t *testing.T) {
	segments := segmentFixtures()

	formats := map[string]string{
		"table": "table",
		"wide":  "wide",
		"json":  "json",
		"yaml":  "yaml",
		"csv":   "csv",
		"toon":  "toon",
	}

	for name, format := range formats {
		t.Run(name, func(t *testing.T) {
			var buf bytes.Buffer
			printer := NewPrinterWithWriter(format, &buf)
			if err := printer.PrintList(segments); err != nil {
				t.Fatalf("PrintList failed: %v", err)
			}
			assertGolden(t, "get/segments-"+name, buf.String())
		})
	}
}

func TestGolden_GetAnomalyDetectors(t *testing.T) {
	detectors := anomalyDetectorFixtures()

	formats := map[string]string{
		"table": "table",
		"wide":  "wide",
		"json":  "json",
		"yaml":  "yaml",
		"csv":   "csv",
		"toon":  "toon",
	}

	for name, format := range formats {
		t.Run(name, func(t *testing.T) {
			var buf bytes.Buffer
			printer := NewPrinterWithWriter(format, &buf)
			if err := printer.PrintList(detectors); err != nil {
				t.Fatalf("PrintList failed: %v", err)
			}
			assertGolden(t, "get/anomalydetectors-"+name, buf.String())
		})
	}
}

func taskExecutionFixtures() []workflow.TaskExecution {
	started1 := fixedTime
	ended1 := fixedTime.Add(5 * time.Second)
	started2 := fixedTime.Add(5 * time.Second)
	ended2 := fixedTime.Add(12 * time.Second)
	started3 := fixedTime.Add(12 * time.Second)
	ended3 := fixedTime.Add(15 * time.Second)
	return []workflow.TaskExecution{
		{
			ID:        "a1b2c3d4-task-0001",
			Name:      "fetch_active_events",
			State:     "SUCCESS",
			StartedAt: &started1,
			EndedAt:   &ended1,
			Runtime:   5,
			Result:    nil, // DQL task — no structured return value
		},
		{
			ID:        "a1b2c3d4-task-0002",
			Name:      "rca_analysis",
			State:     "SUCCESS",
			StartedAt: &started2,
			EndedAt:   &ended2,
			Runtime:   7,
			Result: map[string]any{
				"results": []any{
					map[string]any{
						"serviceId":  "SERVICE-BE4453718DDF0511",
						"eventStart": "2025-03-15T10:30:00.000Z",
					},
				},
			},
		},
		{
			ID:        "a1b2c3d4-task-0003",
			Name:      "send_notification",
			State:     "ERROR",
			StartedAt: &started3,
			EndedAt:   &ended3,
			Runtime:   3,
			StateInfo: func() *string { s := "HTTP 503 from notification endpoint"; return &s }(),
		},
	}
}

func taskResultFixture() any {
	return map[string]any{
		"results": []any{
			map[string]any{
				"serviceId":  "SERVICE-BE4453718DDF0511",
				"eventStart": "2025-03-15T10:30:00.000Z",
			},
			map[string]any{
				"serviceId":  "SERVICE-F19A3CC2E0B74E10",
				"eventStart": "2025-03-15T09:15:00.000Z",
			},
		},
	}
}

func TestGolden_GetTaskExecutions(t *testing.T) {
	tasks := taskExecutionFixtures()

	formats := map[string]string{
		"table": "table",
		"json":  "json",
		"yaml":  "yaml",
		"csv":   "csv",
	}

	for name, format := range formats {
		t.Run(name, func(t *testing.T) {
			var buf bytes.Buffer
			printer := NewPrinterWithWriter(format, &buf)
			if err := printer.PrintList(tasks); err != nil {
				t.Fatalf("PrintList failed: %v", err)
			}
			assertGolden(t, "get/task-executions-"+name, buf.String())
		})
	}
}

func TestGolden_GetTaskResult(t *testing.T) {
	result := taskResultFixture()

	formats := map[string]string{
		"json": "json",
		"yaml": "yaml",
	}

	for name, format := range formats {
		t.Run(name, func(t *testing.T) {
			var buf bytes.Buffer
			printer := NewPrinterWithWriter(format, &buf)
			if err := printer.Print(result); err != nil {
				t.Fatalf("Print failed: %v", err)
			}
			assertGolden(t, "get/task-result-"+name, buf.String())
		})
	}
}

// ---------------------------------------------------------------------------
// Golden tests: describe (single-object Print)
// ---------------------------------------------------------------------------

func TestGolden_DescribeWorkflow(t *testing.T) {
	wf := describeWorkflowFixture()

	formats := map[string]string{
		"table": "table",
		"json":  "json",
		"yaml":  "yaml",
		"toon":  "toon",
	}

	for name, format := range formats {
		t.Run(name, func(t *testing.T) {
			var buf bytes.Buffer
			printer := NewPrinterWithWriter(format, &buf)
			if err := printer.Print(wf); err != nil {
				t.Fatalf("Print failed: %v", err)
			}
			assertGolden(t, "describe/workflow-"+name, buf.String())
		})
	}
}

func TestGolden_DescribeBucket(t *testing.T) {
	b := bucketFixtures()[0]

	formats := map[string]string{
		"table": "table",
		"json":  "json",
		"yaml":  "yaml",
		"toon":  "toon",
	}

	for name, format := range formats {
		t.Run(name, func(t *testing.T) {
			var buf bytes.Buffer
			printer := NewPrinterWithWriter(format, &buf)
			if err := printer.Print(b); err != nil {
				t.Fatalf("Print failed: %v", err)
			}
			assertGolden(t, "describe/bucket-"+name, buf.String())
		})
	}
}

// ---------------------------------------------------------------------------
// Describe fixtures: additional resource types
// ---------------------------------------------------------------------------

func describeSLOFixture() slo.SLO {
	return slo.SLO{
		ID:          "a1b2c3d4-0001-4000-8000-000000000001",
		Name:        "API Availability",
		Description: "99.9% availability for public API endpoints",
		Version:     "3",
		Criteria: []slo.Criteria{
			{TimeframeFrom: "-7d", Target: 99.9, Warning: float64Ptr(99.5)},
			{TimeframeFrom: "-30d", Target: 99.0},
		},
		Tags: []string{"service:api", "tier:1"},
	}
}

func describeDocumentFixture() document.DocumentMetadata {
	return document.DocumentMetadata{
		ID:          "b1c2d3e4-f5a6-4b7c-8d9e-0f1a2b3c4d5e",
		Name:        "Production Overview",
		Type:        "dashboard",
		Owner:       "7a8b9c0d-1e2f-4a3b-8c4d-5e6f7a8b9c0d",
		IsPrivate:   false,
		Description: "Main production monitoring dashboard",
		Version:     3,
		ModificationInfo: document.ModificationInfo{
			CreatedBy:        "admin@example.invalid",
			CreatedTime:      fixedTime,
			LastModifiedBy:   "editor@example.invalid",
			LastModifiedTime: fixedTime.Add(2 * time.Hour),
		},
		Access: []string{"read", "write"},
	}
}

func describeAppFixture() appengine.App {
	return appengine.App{
		ID:          "my.custom-app",
		Name:        "Custom App",
		Version:     "1.4.2",
		Description: "A custom monitoring application",
		IsBuiltin:   false,
		ResourceStatus: &appengine.ResourceStatus{
			Status:           "OK",
			SubResourceTypes: []string{"function", "action"},
		},
		ModificationInfo: &appengine.ModificationInfo{
			CreatedBy:      "user-a@example.invalid",
			CreatedAt:      "2025-01-10T08:00:00Z",
			LastModifiedBy: "user-b@example.invalid",
			LastModifiedAt: "2025-03-15T10:30:00Z",
		},
	}
}

func describeSettingsFixture() settings.SettingsObject {
	return settings.SettingsObject{
		ObjectID:      "vu9U3hXa3q0AAAABABhidWlsdGluOmFsZXJ0aW5nLnByb2ZpbGUABnRlbmFudAAGdGVuYW50ACRhMWIyYzNkNC1lNWY2LTRhN2ItOGM5ZC0wZTFmMmEzYjRjNWQ",
		SchemaID:      "builtin:alerting.profile",
		SchemaVersion: "1.0.5",
		Scope:         "environment",
		Summary:       "Default Alerting Profile",
		Value: map[string]any{
			"name":            "Default",
			"severityRules":   []any{},
			"eventTypeFilter": []any{},
		},
		ObjectIDShort: "vu9U3hXa3q0AAAABAB...",
		ScopeType:     "tenant",
		ScopeID:       "tenant",
	}
}

func describeEdgeConnectFixture() edgeconnect.EdgeConnect {
	return edgeconnect.EdgeConnect{
		ID:           "ec-a1b2c3d4",
		Name:         "production-edge",
		HostPatterns: []string{"*.prod.example.invalid", "api.example.invalid"},
		ModificationInfo: &edgeconnect.ModificationInfo{
			CreatedBy:        "admin@example.invalid",
			CreatedTime:      "2025-01-15T09:00:00Z",
			LastModifiedBy:   "admin@example.invalid",
			LastModifiedTime: "2025-03-10T14:30:00Z",
		},
		ManagedByDynatraceOperator: true,
		Metadata: &edgeconnect.Metadata{
			Version: "5",
		},
	}
}

func describeUserFixture() iam.User {
	return iam.User{
		UID:         "a1b2c3d4-e5f6-4a7b-8c9d-0e1f2a3b4c5d",
		Email:       "jane.doe@example.invalid",
		Name:        "Jane",
		Surname:     "Doe",
		Description: "Platform engineering team lead",
	}
}

func describeGroupFixture() iam.Group {
	return iam.Group{
		UUID:      "g1h2i3j4-k5l6-4m7n-8o9p-0q1r2s3t4u5v",
		GroupName: "platform-admins",
		Type:      "LOCAL",
	}
}

// lookupDescribeFixture is an inline struct to avoid import cycle with lookup package.
type lookupDescribeFixture struct {
	Path        string                   `json:"path"`
	DisplayName string                   `json:"displayName,omitempty"`
	Description string                   `json:"description,omitempty"`
	FileSize    int64                    `json:"fileSize,omitempty"`
	Records     int                      `json:"records,omitempty"`
	LookupField string                   `json:"lookupField,omitempty"`
	Columns     []string                 `json:"columns,omitempty"`
	Modified    string                   `json:"modified,omitempty"`
	PreviewData []map[string]interface{} `json:"previewData"`
}

func describeLookupFixture() lookupDescribeFixture {
	return lookupDescribeFixture{
		Path:        "/lookups/grail/pm/error_codes",
		DisplayName: "Error Codes",
		Description: "Mapping of error codes to descriptions",
		FileSize:    2048,
		Records:     42,
		LookupField: "code",
		Columns:     []string{"code", "description", "severity"},
		Modified:    "2025-03-15T10:30:00Z",
		PreviewData: []map[string]interface{}{
			{"code": "E001", "description": "Connection timeout", "severity": "HIGH"},
			{"code": "E002", "description": "Invalid input", "severity": "LOW"},
		},
	}
}

func describeAzureConnectionFixture() azureconnection.AzureConnection {
	return azureconnection.AzureConnection{
		ObjectID: "azure-conn-a1b2c3d4",
		Value: azureconnection.Value{
			Name: "production-azure",
			Type: "CLIENT_SECRET",
			ClientSecret: &azureconnection.ClientSecretCredential{
				ApplicationID: "app-id-1234-5678",
				DirectoryID:   "dir-id-abcd-efgh",
				Consumers:     []string{"ME", "DA"},
			},
		},
		Name: "production-azure",
		Type: "CLIENT_SECRET",
	}
}

func describeAzureMonitoringConfigFixture() azuremonitoringconfig.AzureMonitoringConfig {
	return azuremonitoringconfig.AzureMonitoringConfig{
		ObjectID:    "azmon-a1b2c3d4",
		Description: "Production Azure monitoring",
		Enabled:     true,
		Version:     "1.0",
		Value: azuremonitoringconfig.Value{
			Enabled:     true,
			Description: "Production Azure monitoring",
			Version:     "1.0",
			Azure: azuremonitoringconfig.AzureConfig{
				DeploymentScope:           "MANAGEMENT_GROUP",
				SubscriptionFilteringMode: "ALL",
				ConfigurationMode:         "STANDARD",
				DeploymentMode:            "FULL",
				Credentials: []azuremonitoringconfig.Credential{
					{
						Enabled:      true,
						Description:  "Main credential",
						ConnectionId: "azure-conn-a1b2c3d4",
						Type:         "CLIENT_SECRET",
					},
				},
			},
			FeatureSets: []string{"default", "advanced"},
		},
	}
}

func describeGCPConnectionFixture() gcpconnection.GCPConnection {
	return gcpconnection.GCPConnection{
		ObjectID: "gcp-conn-x1y2z3",
		Value: gcpconnection.Value{
			Name: "production-gcp",
			Type: "SERVICE_ACCOUNT_IMPERSONATION",
			ServiceAccountImpersonation: &gcpconnection.ServiceAccountImpersonation{
				ServiceAccountID: "sa-monitor@project-id.iam.gserviceaccount.com",
				Consumers:        []string{"ME", "DA"},
			},
		},
		Name: "production-gcp",
		Type: "SERVICE_ACCOUNT_IMPERSONATION",
	}
}

func describeGCPMonitoringConfigFixture() gcpmonitoringconfig.GCPMonitoringConfig {
	return gcpmonitoringconfig.GCPMonitoringConfig{
		ObjectID:    "gcpmon-a1b2c3d4",
		Description: "Production GCP monitoring",
		Enabled:     true,
		Version:     "2.0",
		Value: gcpmonitoringconfig.Value{
			Enabled:     true,
			Description: "Production GCP monitoring",
			Version:     "2.0",
			GoogleCloud: gcpmonitoringconfig.GoogleCloudConfig{
				LocationFiltering: []string{"us-east1", "eu-west1"},
				ProjectFiltering:  []string{"my-project-123"},
				FolderFiltering:   []string{},
				Credentials: []gcpmonitoringconfig.Credential{
					{
						Description:    "Main SA credential",
						Enabled:        true,
						ConnectionID:   "gcp-conn-x1y2z3",
						ServiceAccount: "sa-monitor@project-id.iam.gserviceaccount.com",
					},
				},
				Resources: []gcpmonitoringconfig.MetricSource{
					{
						ResourceType:         "pubsub_topic",
						AutoDiscoveryEnabled: false,
						Metrics: []gcpmonitoringconfig.Metric{
							{
								Name:         "pubsub.googleapis.com/topic/send_request_count",
								MetricLabels: []string{"response_class"},
								Type:         "CUSTOM",
							},
						},
					},
					{
						ResourceType:                   "gce_instance",
						AutoDiscoveryEnabled:           true,
						AutodiscoveryExcludeMetricType: []string{"compute.googleapis.com/instance/disk/*"},
					},
				},
			},
			FeatureSets: []string{"default"},
		},
	}
}

func describeExecutionFixture() workflow.Execution {
	endedAt := fixedTime.Add(45 * time.Second)
	triggerStr := "schedule"
	return workflow.Execution{
		ID:          "d4e5f6a7-b8c9-4d0e-1f2a-3b4c5d6e7f8a",
		Workflow:    "a1b2c3d4-e5f6-4a7b-8c9d-0e1f2a3b4c5d",
		Title:       "Deploy to Production",
		State:       "SUCCEEDED",
		StartedAt:   fixedTime,
		EndedAt:     &endedAt,
		Runtime:     45,
		Trigger:     &triggerStr,
		TriggerType: "Schedule",
	}
}

// ---------------------------------------------------------------------------
// Golden tests: describe (additional resource types)
// ---------------------------------------------------------------------------

func TestGolden_DescribeSLO(t *testing.T) {
	s := describeSLOFixture()

	formats := map[string]string{
		"json": "json",
		"yaml": "yaml",
		"toon": "toon",
	}

	for name, format := range formats {
		t.Run(name, func(t *testing.T) {
			var buf bytes.Buffer
			printer := NewPrinterWithWriter(format, &buf)
			if err := printer.Print(s); err != nil {
				t.Fatalf("Print failed: %v", err)
			}
			assertGolden(t, "describe/slo-"+name, buf.String())
		})
	}
}

func TestGolden_DescribeDocument(t *testing.T) {
	d := describeDocumentFixture()

	formats := map[string]string{
		"json": "json",
		"yaml": "yaml",
		"toon": "toon",
	}

	for name, format := range formats {
		t.Run(name, func(t *testing.T) {
			var buf bytes.Buffer
			printer := NewPrinterWithWriter(format, &buf)
			if err := printer.Print(d); err != nil {
				t.Fatalf("Print failed: %v", err)
			}
			assertGolden(t, "describe/document-"+name, buf.String())
		})
	}
}

func TestGolden_DescribeApp(t *testing.T) {
	a := describeAppFixture()

	formats := map[string]string{
		"json": "json",
		"yaml": "yaml",
		"toon": "toon",
	}

	for name, format := range formats {
		t.Run(name, func(t *testing.T) {
			var buf bytes.Buffer
			printer := NewPrinterWithWriter(format, &buf)
			if err := printer.Print(a); err != nil {
				t.Fatalf("Print failed: %v", err)
			}
			assertGolden(t, "describe/app-"+name, buf.String())
		})
	}
}

// intentFixtures covers both declaration forms (#517): a property declared
// with singular `schema`, one with plural `schemas` (accepted alias keys with
// per-key patterns), a list-form type, an undeclared type (empty, never a
// made-up "string"), an empty `schemas` map, and a deprecated intent.
func intentFixtures() []appengine.Intent {
	thingPattern := "^(?:THING-|WIDGET-)[0-9A-F]{16}$"
	return []appengine.Intent{
		{
			AppID:       "example.app",
			AppName:     "Example App",
			IntentID:    "view-thing",
			Name:        "View thing",
			Description: "Open a thing",
			FullName:    "example.app/view-thing",
			Properties: map[string]appengine.IntentProperty{
				"thing": {
					Type:         "string",
					Types:        []string{"string"},
					Required:     true,
					Description:  "The thing to open",
					AcceptedKeys: []string{"dt.entity", "thing"},
					Schemas: map[string]interface{}{
						"dt.entity": map[string]interface{}{"type": "string", "pattern": thingPattern},
						"thing":     map[string]interface{}{"type": "string", "pattern": thingPattern},
					},
				},
				"tags": {
					Type:         "string|array",
					Types:        []string{"string", "array"},
					AcceptedKeys: []string{"tags"},
					Schema: map[string]interface{}{
						"type":  []interface{}{"string", "array"},
						"items": map[string]interface{}{"type": "string"},
					},
				},
				"note": {
					AcceptedKeys: []string{"note"},
				},
			},
			RequiredProps: []string{"thing"},
		},
		{
			AppID:              "example.app",
			AppName:            "Example App",
			IntentID:           "view-thing-legacy",
			Description:        "Open a thing (legacy)",
			FullName:           "example.app/view-thing-legacy",
			Deprecated:         true,
			DeprecationMessage: "Use view-thing instead",
			Properties: map[string]appengine.IntentProperty{
				"id": {
					Type:         "string",
					Types:        []string{"string"},
					Required:     true,
					Format:       "uuid",
					AcceptedKeys: []string{"id"},
					Schema:       map[string]interface{}{"type": "string", "format": "uuid"},
				},
				"unused": {
					Required:     true,
					AcceptedKeys: []string{},
					Schemas:      map[string]interface{}{},
				},
			},
			RequiredProps: []string{"id", "unused"},
		},
	}
}

func TestGolden_GetIntents(t *testing.T) {
	intents := intentFixtures()

	formats := map[string]string{
		"table": "table",
		"wide":  "wide",
		"json":  "json",
		"yaml":  "yaml",
		"csv":   "csv",
		"toon":  "toon",
	}

	for name, format := range formats {
		t.Run(name, func(t *testing.T) {
			var buf bytes.Buffer
			printer := NewPrinterWithWriter(format, &buf)
			if err := printer.PrintList(intents); err != nil {
				t.Fatalf("PrintList failed: %v", err)
			}
			assertGolden(t, "get/intents-"+name, buf.String())
		})
	}
}

func TestGolden_FindIntents(t *testing.T) {
	matches := []appengine.IntentMatch{
		{
			Intent:       intentFixtures()[0],
			MatchQuality: 33.33333333333333,
			MatchedProps: []string{"thing"},
			MatchedKeys:  map[string]string{"thing": "dt.entity"},
		},
	}

	formats := map[string]string{
		"table": "table",
		"json":  "json",
		"yaml":  "yaml",
	}

	for name, format := range formats {
		t.Run(name, func(t *testing.T) {
			var buf bytes.Buffer
			printer := NewPrinterWithWriter(format, &buf)
			if err := printer.PrintList(matches); err != nil {
				t.Fatalf("PrintList failed: %v", err)
			}
			assertGolden(t, "get/intent-matches-"+name, buf.String())
		})
	}
}

func TestGolden_DescribeSettings(t *testing.T) {
	s := describeSettingsFixture()

	formats := map[string]string{
		"json": "json",
		"yaml": "yaml",
		"toon": "toon",
	}

	for name, format := range formats {
		t.Run(name, func(t *testing.T) {
			var buf bytes.Buffer
			printer := NewPrinterWithWriter(format, &buf)
			if err := printer.Print(s); err != nil {
				t.Fatalf("Print failed: %v", err)
			}
			assertGolden(t, "describe/settings-"+name, buf.String())
		})
	}
}

func TestGolden_DescribeEdgeConnect(t *testing.T) {
	ec := describeEdgeConnectFixture()

	formats := map[string]string{
		"json": "json",
		"yaml": "yaml",
		"toon": "toon",
	}

	for name, format := range formats {
		t.Run(name, func(t *testing.T) {
			var buf bytes.Buffer
			printer := NewPrinterWithWriter(format, &buf)
			if err := printer.Print(ec); err != nil {
				t.Fatalf("Print failed: %v", err)
			}
			assertGolden(t, "describe/edgeconnect-"+name, buf.String())
		})
	}
}

func TestGolden_DescribeUser(t *testing.T) {
	u := describeUserFixture()

	formats := map[string]string{
		"json": "json",
		"yaml": "yaml",
		"toon": "toon",
	}

	for name, format := range formats {
		t.Run(name, func(t *testing.T) {
			var buf bytes.Buffer
			printer := NewPrinterWithWriter(format, &buf)
			if err := printer.Print(u); err != nil {
				t.Fatalf("Print failed: %v", err)
			}
			assertGolden(t, "describe/user-"+name, buf.String())
		})
	}
}

func TestGolden_DescribeGroup(t *testing.T) {
	g := describeGroupFixture()

	formats := map[string]string{
		"json": "json",
		"yaml": "yaml",
		"toon": "toon",
	}

	for name, format := range formats {
		t.Run(name, func(t *testing.T) {
			var buf bytes.Buffer
			printer := NewPrinterWithWriter(format, &buf)
			if err := printer.Print(g); err != nil {
				t.Fatalf("Print failed: %v", err)
			}
			assertGolden(t, "describe/group-"+name, buf.String())
		})
	}
}

func TestGolden_DescribeLookup(t *testing.T) {
	lu := describeLookupFixture()

	formats := map[string]string{
		"json": "json",
		"yaml": "yaml",
		"toon": "toon",
	}

	for name, format := range formats {
		t.Run(name, func(t *testing.T) {
			var buf bytes.Buffer
			printer := NewPrinterWithWriter(format, &buf)
			if err := printer.Print(lu); err != nil {
				t.Fatalf("Print failed: %v", err)
			}
			assertGolden(t, "describe/lookup-"+name, buf.String())
		})
	}
}

func TestGolden_DescribeAzureConnection(t *testing.T) {
	ac := describeAzureConnectionFixture()

	formats := map[string]string{
		"json": "json",
		"yaml": "yaml",
		"toon": "toon",
	}

	for name, format := range formats {
		t.Run(name, func(t *testing.T) {
			var buf bytes.Buffer
			printer := NewPrinterWithWriter(format, &buf)
			if err := printer.Print(ac); err != nil {
				t.Fatalf("Print failed: %v", err)
			}
			assertGolden(t, "describe/azure-connection-"+name, buf.String())
		})
	}
}

func TestGolden_DescribeAzureMonitoring(t *testing.T) {
	am := describeAzureMonitoringConfigFixture()

	formats := map[string]string{
		"json": "json",
		"yaml": "yaml",
		"toon": "toon",
	}

	for name, format := range formats {
		t.Run(name, func(t *testing.T) {
			var buf bytes.Buffer
			printer := NewPrinterWithWriter(format, &buf)
			if err := printer.Print(am); err != nil {
				t.Fatalf("Print failed: %v", err)
			}
			assertGolden(t, "describe/azure-monitoring-"+name, buf.String())
		})
	}
}

func TestGolden_DescribeGCPConnection(t *testing.T) {
	gc := describeGCPConnectionFixture()

	formats := map[string]string{
		"json": "json",
		"yaml": "yaml",
		"toon": "toon",
	}

	for name, format := range formats {
		t.Run(name, func(t *testing.T) {
			var buf bytes.Buffer
			printer := NewPrinterWithWriter(format, &buf)
			if err := printer.Print(gc); err != nil {
				t.Fatalf("Print failed: %v", err)
			}
			assertGolden(t, "describe/gcp-connection-"+name, buf.String())
		})
	}
}

func TestGolden_DescribeGCPMonitoring(t *testing.T) {
	gm := describeGCPMonitoringConfigFixture()

	formats := map[string]string{
		"json": "json",
		"yaml": "yaml",
		"toon": "toon",
	}

	for name, format := range formats {
		t.Run(name, func(t *testing.T) {
			var buf bytes.Buffer
			printer := NewPrinterWithWriter(format, &buf)
			if err := printer.Print(gm); err != nil {
				t.Fatalf("Print failed: %v", err)
			}
			assertGolden(t, "describe/gcp-monitoring-"+name, buf.String())
		})
	}
}

func TestGolden_DescribeExecution(t *testing.T) {
	e := describeExecutionFixture()

	formats := map[string]string{
		"json": "json",
		"yaml": "yaml",
		"toon": "toon",
	}

	for name, format := range formats {
		t.Run(name, func(t *testing.T) {
			var buf bytes.Buffer
			printer := NewPrinterWithWriter(format, &buf)
			if err := printer.Print(e); err != nil {
				t.Fatalf("Print failed: %v", err)
			}
			assertGolden(t, "describe/execution-"+name, buf.String())
		})
	}
}

func TestGolden_DescribeSegment(t *testing.T) {
	s := describeSegmentFixture()

	formats := map[string]string{
		"json": "json",
		"yaml": "yaml",
		"toon": "toon",
	}

	for name, format := range formats {
		t.Run(name, func(t *testing.T) {
			var buf bytes.Buffer
			printer := NewPrinterWithWriter(format, &buf)
			if err := printer.Print(s); err != nil {
				t.Fatalf("Print failed: %v", err)
			}
			assertGolden(t, "describe/segment-"+name, buf.String())
		})
	}
}

func TestGolden_DescribeAnomalyDetector(t *testing.T) {
	d := describeAnomalyDetectorFixture()

	formats := map[string]string{
		"json": "json",
		"yaml": "yaml",
		"toon": "toon",
	}

	for name, format := range formats {
		t.Run(name, func(t *testing.T) {
			var buf bytes.Buffer
			printer := NewPrinterWithWriter(format, &buf)
			if err := printer.Print(d); err != nil {
				t.Fatalf("Print failed: %v", err)
			}
			assertGolden(t, "describe/anomalydetector-"+name, buf.String())
		})
	}
}

// ---------------------------------------------------------------------------
// Golden tests: DQL query results (map-based records)
// ---------------------------------------------------------------------------

func TestGolden_QueryDQL(t *testing.T) {
	records := dqlRecordsFixture()

	// Parquet is intentionally excluded here: it is a binary format, so it is
	// covered by the round-trip assertions in parquet_test.go rather than a
	// golden byte file.
	formats := map[string]string{
		"table": "table",
		"wide":  "wide",
		"json":  "json",
		"csv":   "csv",
		"toon":  "toon",
		"jsonl": "jsonl",
	}

	for name, format := range formats {
		t.Run(name, func(t *testing.T) {
			var buf bytes.Buffer
			printer := NewPrinterWithWriter(format, &buf)
			if err := printer.PrintList(records); err != nil {
				t.Fatalf("PrintList failed: %v", err)
			}
			assertGolden(t, "query/dql-"+name, buf.String())
		})
	}
}

// ---------------------------------------------------------------------------
// Golden tests: DQL query results with metadata
// ---------------------------------------------------------------------------

func metadataFixture() *QueryMetadata {
	return &QueryMetadata{
		ExecutionTimeMilliseconds: 47,
		ScannedRecords:            42351,
		ScannedBytes:              2982690,
		ScannedDataPoints:         0,
		Sampled:                   false,
		QueryID:                   "27c4daf9-2619-4ba1-b1ad-9e276c75a351",
		DQLVersion:                "V1_0",
		Query:                     "fetch logs | limit 3 | fields timestamp, host.name, status, content",
		CanonicalQuery:            "fetch logs\n| limit 3\n| fields timestamp, host.name, status, content",
		Timezone:                  "Z",
		Locale:                    "und",
		AnalysisTimeframe: &MetadataTimeframe{
			Start: "2025-03-15T08:30:00.000000000Z",
			End:   "2025-03-15T10:30:00.000000000Z",
		},
		Contributions: &MetadataContribs{
			Buckets: []MetadataBucket{
				{
					Name:                "default_logs",
					Table:               "logs",
					ScannedBytes:        2982690,
					MatchedRecordsRatio: 1.0,
				},
			},
		},
		Metrics: []MetricInfo{
			{MetricKey: "process.cpu.utilization", FieldName: "avg(process.cpu.utilization)", Aggregation: "avg", Unit: "Percent", DisplayName: "Process CPU Utilization"},
			{MetricKey: "process.memory.usage", FieldName: "sum(process.memory.usage)", Aggregation: "sum", Unit: "Byte", DisplayName: "Process Memory Usage"},
		},
	}
}

func TestGolden_QueryDQL_Metadata_JSON(t *testing.T) {
	records := dqlRecordsFixture()
	meta := metadataFixture()

	t.Run("all", func(t *testing.T) {
		// "all" returns the struct (omitempty suppresses zeros)
		payload := map[string]interface{}{
			"records":  records,
			"metadata": MetadataToMap(meta, []string{"all"}),
		}
		var buf bytes.Buffer
		printer := NewPrinterWithWriter("json", &buf)
		if err := printer.Print(payload); err != nil {
			t.Fatalf("Print failed: %v", err)
		}
		assertGolden(t, "query/dql-metadata-json", buf.String())
	})

	t.Run("filtered", func(t *testing.T) {
		// Explicit field selection including a zero-value field (scannedDataPoints=0)
		fields := []string{"executionTimeMilliseconds", "scannedRecords", "scannedDataPoints", "sampled", "queryId"}
		payload := map[string]interface{}{
			"records":  records,
			"metadata": MetadataToMap(meta, fields),
		}
		var buf bytes.Buffer
		printer := NewPrinterWithWriter("json", &buf)
		if err := printer.Print(payload); err != nil {
			t.Fatalf("Print failed: %v", err)
		}
		assertGolden(t, "query/dql-metadata-filtered-json", buf.String())
	})
}

func TestGolden_QueryDQL_Metadata_YAML(t *testing.T) {
	records := dqlRecordsFixture()
	meta := metadataFixture()

	t.Run("all", func(t *testing.T) {
		payload := map[string]interface{}{
			"records":  records,
			"metadata": MetadataToMap(meta, []string{"all"}),
		}
		var buf bytes.Buffer
		printer := NewPrinterWithWriter("yaml", &buf)
		if err := printer.Print(payload); err != nil {
			t.Fatalf("Print failed: %v", err)
		}
		assertGolden(t, "query/dql-metadata-yaml", buf.String())
	})

	t.Run("filtered", func(t *testing.T) {
		fields := []string{"executionTimeMilliseconds", "scannedRecords", "scannedDataPoints", "sampled", "queryId"}
		payload := map[string]interface{}{
			"records":  records,
			"metadata": MetadataToMap(meta, fields),
		}
		var buf bytes.Buffer
		printer := NewPrinterWithWriter("yaml", &buf)
		if err := printer.Print(payload); err != nil {
			t.Fatalf("Print failed: %v", err)
		}
		assertGolden(t, "query/dql-metadata-filtered-yaml", buf.String())
	})
}

func TestGolden_QueryDQL_Metadata_Table(t *testing.T) {
	records := dqlRecordsFixture()
	meta := metadataFixture()

	// Disable color for deterministic output
	ResetColorCache()
	SetPlainMode(true)
	defer ResetColorCache()

	t.Run("all", func(t *testing.T) {
		var buf bytes.Buffer
		printer := NewPrinterWithWriter("table", &buf)
		if err := printer.PrintList(records); err != nil {
			t.Fatalf("PrintList failed: %v", err)
		}
		// Append metadata footer (same as printResults does)
		footer := FormatMetadataFooter(meta, nil)
		assertGolden(t, "query/dql-metadata-table", buf.String()+footer)
	})

	t.Run("filtered", func(t *testing.T) {
		fields := []string{"executionTimeMilliseconds", "scannedRecords", "queryId"}
		var buf bytes.Buffer
		printer := NewPrinterWithWriter("table", &buf)
		if err := printer.PrintList(records); err != nil {
			t.Fatalf("PrintList failed: %v", err)
		}
		footer := FormatMetadataFooter(meta, fields)
		assertGolden(t, "query/dql-metadata-filtered-table", buf.String()+footer)
	})
}

func TestGolden_QueryDQL_Metadata_Wide(t *testing.T) {
	records := dqlRecordsFixture()
	meta := metadataFixture()

	// Disable color for deterministic output
	ResetColorCache()
	SetPlainMode(true)
	defer ResetColorCache()

	t.Run("all", func(t *testing.T) {
		var buf bytes.Buffer
		printer := NewPrinterWithWriter("wide", &buf)
		if err := printer.PrintList(records); err != nil {
			t.Fatalf("PrintList failed: %v", err)
		}
		// Append metadata footer (same as printResults does for wide format)
		footer := FormatMetadataFooter(meta, nil)
		assertGolden(t, "query/dql-metadata-wide", buf.String()+footer)
	})

	t.Run("filtered", func(t *testing.T) {
		fields := []string{"executionTimeMilliseconds", "scannedRecords", "queryId"}
		var buf bytes.Buffer
		printer := NewPrinterWithWriter("wide", &buf)
		if err := printer.PrintList(records); err != nil {
			t.Fatalf("PrintList failed: %v", err)
		}
		footer := FormatMetadataFooter(meta, fields)
		assertGolden(t, "query/dql-metadata-filtered-wide", buf.String()+footer)
	})
}

func TestGolden_QueryDQL_Metadata_CSV(t *testing.T) {
	records := dqlRecordsFixture()
	meta := metadataFixture()

	t.Run("all", func(t *testing.T) {
		var buf bytes.Buffer
		printer := NewPrinterWithWriter("csv", &buf)
		if err := printer.PrintList(records); err != nil {
			t.Fatalf("PrintList failed: %v", err)
		}
		// Prepend metadata comments (same as printResults does)
		comments := FormatMetadataCSVComments(meta, nil)
		assertGolden(t, "query/dql-metadata-csv", comments+buf.String())
	})

	t.Run("filtered", func(t *testing.T) {
		fields := []string{"executionTimeMilliseconds", "scannedRecords", "queryId"}
		var buf bytes.Buffer
		printer := NewPrinterWithWriter("csv", &buf)
		if err := printer.PrintList(records); err != nil {
			t.Fatalf("PrintList failed: %v", err)
		}
		comments := FormatMetadataCSVComments(meta, fields)
		assertGolden(t, "query/dql-metadata-filtered-csv", comments+buf.String())
	})
}

func TestGolden_QueryDQL_Metadata_TOON(t *testing.T) {
	records := dqlRecordsFixture()
	meta := metadataFixture()

	t.Run("all", func(t *testing.T) {
		payload := map[string]interface{}{
			"records":  records,
			"metadata": MetadataToMap(meta, []string{"all"}),
		}
		var buf bytes.Buffer
		printer := NewPrinterWithWriter("toon", &buf)
		if err := printer.Print(payload); err != nil {
			t.Fatalf("Print failed: %v", err)
		}
		assertGolden(t, "query/dql-metadata-toon", buf.String())
	})

	t.Run("filtered", func(t *testing.T) {
		fields := []string{"executionTimeMilliseconds", "scannedRecords", "scannedDataPoints", "sampled", "queryId"}
		payload := map[string]interface{}{
			"records":  records,
			"metadata": MetadataToMap(meta, fields),
		}
		var buf bytes.Buffer
		printer := NewPrinterWithWriter("toon", &buf)
		if err := printer.Print(payload); err != nil {
			t.Fatalf("Print failed: %v", err)
		}
		assertGolden(t, "query/dql-metadata-filtered-toon", buf.String())
	})
}

// approximateMetadataFixture is a synthetic response Grail qualified: an
// approximation and an advisory notification on a successful result (#415).
func approximateMetadataFixture() *QueryMetadata {
	return &QueryMetadata{
		ExecutionTimeMilliseconds: 12,
		ScannedRecords:            1,
		Sampled:                   true,
		QueryID:                   "00000000-0000-4000-8000-000000000415",
		Approximations:            []string{"~ on content: substring match, not token match"},
		Notifications: []MetadataNotice{
			{Severity: "INFO", NotificationType: "EXAMPLE_NOTICE", Message: "an informational advisory"},
		},
	}
}

func TestGolden_QueryDQL_Metadata_Approximations(t *testing.T) {
	records := dqlRecordsFixture()
	meta := approximateMetadataFixture()

	ResetColorCache()
	SetPlainMode(true)
	defer ResetColorCache()

	for _, format := range []string{"json", "yaml"} {
		t.Run(format, func(t *testing.T) {
			payload := map[string]interface{}{
				"records":  records,
				"metadata": MetadataToMap(meta, []string{"all"}),
			}
			var buf bytes.Buffer
			if err := NewPrinterWithWriter(format, &buf).Print(payload); err != nil {
				t.Fatalf("Print failed: %v", err)
			}
			assertGolden(t, "query/dql-metadata-approximations-"+format, buf.String())
		})
	}

	t.Run("table", func(t *testing.T) {
		var buf bytes.Buffer
		if err := NewPrinterWithWriter("table", &buf).PrintList(records); err != nil {
			t.Fatalf("PrintList failed: %v", err)
		}
		assertGolden(t, "query/dql-metadata-approximations-table", buf.String()+FormatMetadataFooter(meta, nil))
	})

	t.Run("csv", func(t *testing.T) {
		var buf bytes.Buffer
		if err := NewPrinterWithWriter("csv", &buf).PrintList(records); err != nil {
			t.Fatalf("PrintList failed: %v", err)
		}
		assertGolden(t, "query/dql-metadata-approximations-csv", FormatMetadataCSVComments(meta, nil)+buf.String())
	})
}

// ---------------------------------------------------------------------------
// Golden tests: empty results
// ---------------------------------------------------------------------------

func TestGolden_EmptyResults(t *testing.T) {
	t.Run("table", func(t *testing.T) {
		var buf bytes.Buffer
		printer := NewPrinterWithWriter("table", &buf)
		if err := printer.PrintList([]workflow.Workflow{}); err != nil {
			t.Fatalf("PrintList failed: %v", err)
		}
		assertGolden(t, "empty/workflows-table", buf.String())
	})

	t.Run("json", func(t *testing.T) {
		var buf bytes.Buffer
		printer := NewPrinterWithWriter("json", &buf)
		if err := printer.PrintList([]workflow.Workflow{}); err != nil {
			t.Fatalf("PrintList failed: %v", err)
		}
		assertGolden(t, "empty/workflows-json", buf.String())
	})
}

// ---------------------------------------------------------------------------
// Golden tests: visual formats (chart, sparkline, barchart, braille)
// Use fixed dimensions for deterministic output.
// ---------------------------------------------------------------------------

func TestGolden_QueryDQL_Chart(t *testing.T) {
	records := dqlTimeseriesFixture()

	var buf bytes.Buffer
	printer := NewPrinterWithOpts(PrinterOptions{
		Format: "chart",
		Writer: &buf,
		Width:  80,
		Height: 15,
	})
	if err := printer.PrintList(records); err != nil {
		t.Fatalf("PrintList failed: %v", err)
	}
	// Strip ANSI for deterministic comparison
	assertGolden(t, "query/dql-chart", stripANSI(buf.String()))
}

// TestGolden_QueryDQL_Chart_MultiSeries exercises the PlotMany code path (2+ series).
// Previously, omitting SeriesColors when color was disabled caused a panic inside
// asciigraph.addLegends (legend.go:31), which accesses SeriesColors[i] for each legend.
func TestGolden_QueryDQL_Chart_MultiSeries(t *testing.T) {
	records := dqlMultiSeriesTimeseriesFixture()

	var buf bytes.Buffer
	printer := NewPrinterWithOpts(PrinterOptions{
		Format: "chart",
		Writer: &buf,
		Width:  80,
		Height: 15,
	})
	if err := printer.PrintList(records); err != nil {
		t.Fatalf("PrintList failed: %v", err)
	}
	// Chart output uses \r\n for raw terminal compatibility; normalize to \n so
	// the golden file is portable across platforms and survives git's CRLF handling.
	output := strings.ReplaceAll(stripANSI(buf.String()), "\r\n", "\n")
	assertGolden(t, "query/dql-chart-multi-series", output)
}

func TestGolden_QueryDQL_Sparkline(t *testing.T) {
	records := dqlTimeseriesFixture()

	var buf bytes.Buffer
	printer := NewPrinterWithOpts(PrinterOptions{
		Format: "sparkline",
		Writer: &buf,
		Width:  60,
	})
	if err := printer.PrintList(records); err != nil {
		t.Fatalf("PrintList failed: %v", err)
	}
	assertGolden(t, "query/dql-sparkline", stripANSI(buf.String()))
}

func TestGolden_QueryDQL_BarChart(t *testing.T) {
	records := dqlTimeseriesFixture()

	var buf bytes.Buffer
	printer := NewPrinterWithOpts(PrinterOptions{
		Format: "barchart",
		Writer: &buf,
		Width:  60,
	})
	if err := printer.PrintList(records); err != nil {
		t.Fatalf("PrintList failed: %v", err)
	}
	assertGolden(t, "query/dql-barchart", stripANSI(buf.String()))
}

func TestGolden_QueryDQL_Braille(t *testing.T) {
	records := dqlTimeseriesFixture()

	var buf bytes.Buffer
	printer := NewPrinterWithOpts(PrinterOptions{
		Format: "braille",
		Writer: &buf,
		Width:  40,
		Height: 10,
	})
	if err := printer.PrintList(records); err != nil {
		t.Fatalf("PrintList failed: %v", err)
	}
	assertGolden(t, "query/dql-braille", stripANSI(buf.String()))
}

// ---------------------------------------------------------------------------
// Golden tests: error output
// ---------------------------------------------------------------------------

func TestGolden_ErrorNotFound(t *testing.T) {
	// Simulate the error message a user would see
	errMsg := "Error: workflow \"my-workflow\" not found\n"
	assertGolden(t, "errors/not-found", errMsg)
}

func TestGolden_ErrorAuth(t *testing.T) {
	errMsg := "Error: authentication failed: token expired or invalid\n"
	assertGolden(t, "errors/auth-error", errMsg)
}

func TestGolden_ErrorPermission(t *testing.T) {
	errMsg := "Error: insufficient permissions: requires scope \"automation:workflows:read\"\n"
	assertGolden(t, "errors/permission-error", errMsg)
}

// ---------------------------------------------------------------------------
// Golden tests: agent mode error output (JSON envelope with ok: false)
// ---------------------------------------------------------------------------

func TestGolden_AgentErrorAuth(t *testing.T) {
	var buf bytes.Buffer
	detail := &ErrorDetail{
		Code:       "auth_required",
		Message:    "authentication failed",
		Operation:  "get workflows",
		StatusCode: 401,
		RequestID:  "req-abc-123",
		Suggestions: []string{
			"Run 'dtctl auth login' to refresh your token",
			"Verify your token is correct in the current context configuration",
		},
	}
	if err := PrintError(&buf, detail); err != nil {
		t.Fatalf("PrintError failed: %v", err)
	}
	assertGolden(t, "errors/auth-error-agent", buf.String())
}

func TestGolden_AgentErrorNotFound(t *testing.T) {
	var buf bytes.Buffer
	detail := &ErrorDetail{
		Code:       "not_found",
		Message:    "workflow not found",
		Operation:  "get workflows",
		StatusCode: 404,
		Suggestions: []string{
			"Verify the resource name or ID is correct",
			"List available resources: 'dtctl get workflows'",
		},
	}
	if err := PrintError(&buf, detail); err != nil {
		t.Fatalf("PrintError failed: %v", err)
	}
	assertGolden(t, "errors/not-found-agent", buf.String())
}

func TestGolden_AgentErrorSafetyBlocked(t *testing.T) {
	var buf bytes.Buffer
	detail := &ErrorDetail{
		Code:    "safety_blocked",
		Message: "Context 'production' (readonly) does not allow delete operations",
		Suggestions: []string{
			"Switch to a context with write permissions",
		},
	}
	if err := PrintError(&buf, detail); err != nil {
		t.Fatalf("PrintError failed: %v", err)
	}
	assertGolden(t, "errors/safety-blocked-agent", buf.String())
}

func TestGolden_AgentErrorGeneric(t *testing.T) {
	var buf bytes.Buffer
	detail := &ErrorDetail{
		Code:    "error",
		Message: "something unexpected happened",
	}
	if err := PrintError(&buf, detail); err != nil {
		t.Fatalf("PrintError failed: %v", err)
	}
	assertGolden(t, "errors/generic-error-agent", buf.String())
}

func TestGolden_AgentErrorQueryPosition(t *testing.T) {
	var buf bytes.Buffer
	detail := &ErrorDetail{
		Code:       "parse_error",
		Message:    "query failed (PARSE_ERROR): `by` isn't allowed here.",
		StatusCode: 400,
		Position:   &ErrorPosition{Line: 1, Column: 32, EndLine: 1, EndColumn: 33},
		Snippet:    "fetch logs | summarize count() by service.name\n                               ^^",
		Suggestions: []string{
			"group with the by: parameter, by:{field, …}, not a trailing by keyword: dtctl query 'fetch logs | summarize count(), by:{service.name}'",
		},
	}
	if err := PrintError(&buf, detail); err != nil {
		t.Fatalf("PrintError failed: %v", err)
	}
	assertGolden(t, "errors/query-position-agent", buf.String())
}

// ---------------------------------------------------------------------------
// Golden tests: agent mode output
// ---------------------------------------------------------------------------

func TestGolden_AgentMode(t *testing.T) {
	workflows := workflowFixtures()

	t.Run("list", func(t *testing.T) {
		var buf bytes.Buffer
		ctx := &ResponseContext{
			Verb:     "get",
			Resource: "workflow",
		}
		printer := NewAgentPrinter(&buf, ctx)
		printer.SetTotal(len(workflows))
		printer.SetSuggestions([]string{
			"Run 'dtctl describe workflow <id>' for details",
			"Run 'dtctl exec workflow <id>' to trigger a workflow",
		})
		if err := printer.PrintList(workflows); err != nil {
			t.Fatalf("PrintList failed: %v", err)
		}
		assertGolden(t, "get/workflows-agent", buf.String())
	})
}

// ---------------------------------------------------------------------------
// Golden tests: watch output (change prefixes)
// ---------------------------------------------------------------------------

func TestGolden_WatchChanges(t *testing.T) {
	wfs := workflowFixtures()

	changes := []Change{
		{Type: ChangeTypeAdded, Resource: wfs[0]},
		{Type: ChangeTypeModified, Resource: wfs[1]},
		{Type: ChangeTypeDeleted, Resource: wfs[2]},
	}

	var buf bytes.Buffer
	basePrinter := NewPrinterWithWriter("table", &buf)
	watchPrinter := NewWatchPrinterWithWriter(basePrinter, &buf, false) // no color for deterministic output

	if err := watchPrinter.PrintChanges(changes); err != nil {
		t.Fatalf("PrintChanges failed: %v", err)
	}

	assertGolden(t, "get/workflows-watch-changes", buf.String())
}

// ---------------------------------------------------------------------------
// Hub fixtures
// ---------------------------------------------------------------------------

func hubExtensionFixtures() []hub.HubExtension {
	return []hub.HubExtension{
		{
			ID:          "com.dynatrace.extension.host-monitoring",
			Name:        "Host Monitoring",
			Type:        "EXTENSION_2",
			Description: "Comprehensive host performance monitoring",
		},
		{
			ID:          "com.dynatrace.extension.jmx",
			Name:        "JMX Extension",
			Type:        "EXTENSION_2",
			Description: "Java management extensions monitoring",
		},
		{
			ID:   "custom:my-custom-extension",
			Name: "Custom Metrics Collector",
			Type: "EXTENSION_2",
		},
	}
}

// ---------------------------------------------------------------------------
// Golden tests: Hub extensions
// ---------------------------------------------------------------------------

func TestGolden_GetHubExtensions(t *testing.T) {
	exts := hubExtensionFixtures()

	formats := map[string]string{
		"table": "table",
		"wide":  "wide",
		"json":  "json",
		"yaml":  "yaml",
		"csv":   "csv",
	}

	for name, format := range formats {
		t.Run(name, func(t *testing.T) {
			var buf bytes.Buffer
			printer := NewPrinterWithWriter(format, &buf)
			if err := printer.PrintList(exts); err != nil {
				t.Fatalf("PrintList failed: %v", err)
			}
			assertGolden(t, "get/hub-extensions-"+name, buf.String())
		})
	}
}

func TestGolden_DescribeHubExtension(t *testing.T) {
	ext := hubExtensionFixtures()[0]

	formats := map[string]string{
		"table": "table",
		"json":  "json",
		"yaml":  "yaml",
	}

	for name, format := range formats {
		t.Run(name, func(t *testing.T) {
			var buf bytes.Buffer
			printer := NewPrinterWithWriter(format, &buf)
			if err := printer.Print(ext); err != nil {
				t.Fatalf("Print failed: %v", err)
			}
			assertGolden(t, "describe/hub-extension-"+name, buf.String())
		})
	}
}

func hubExtensionReleaseFixtures() []hub.HubExtensionRelease {
	return []hub.HubExtensionRelease{
		{Version: "3.1.0", ReleaseDate: "2025-03-01"},
		{Version: "3.0.2", ReleaseDate: "2025-01-15"},
		{Version: "3.0.0", ReleaseDate: "2024-11-20"},
	}
}

func TestGolden_GetHubExtensionReleases(t *testing.T) {
	releases := hubExtensionReleaseFixtures()

	formats := map[string]string{
		"table": "table",
		"wide":  "wide",
		"json":  "json",
		"yaml":  "yaml",
		"csv":   "csv",
	}

	for name, format := range formats {
		t.Run(name, func(t *testing.T) {
			var buf bytes.Buffer
			printer := NewPrinterWithWriter(format, &buf)
			if err := printer.PrintList(releases); err != nil {
				t.Fatalf("PrintList failed: %v", err)
			}
			assertGolden(t, "get/hub-extension-releases-"+name, buf.String())
		})
	}
}

// ---------------------------------------------------------------------------
// Golden tests: create extension (single ExtensionVersion)
// ---------------------------------------------------------------------------

func TestGolden_DescribeExtensionVersion(t *testing.T) {
	v := extensionVersionFixtures()[0]

	formats := map[string]string{
		"table": "table",
		"json":  "json",
		"yaml":  "yaml",
	}

	for name, format := range formats {
		t.Run(name, func(t *testing.T) {
			var buf bytes.Buffer
			printer := NewPrinterWithWriter(format, &buf)
			if err := printer.Print(v); err != nil {
				t.Fatalf("Print failed: %v", err)
			}
			assertGolden(t, "describe/extension-version-"+name, buf.String())
		})
	}
}

func activeGateGroupFixtures() []extension.ActiveGateGroupItem {
	return []extension.ActiveGateGroupItem{
		{
			GroupName:            "esx-linux-ag",
			AvailableActiveGates: 2,
			ActiveGates: []extension.ActiveGateEntry{
				{ID: 187309619},
				{ID: 1981204261},
			},
		},
		{
			GroupName:            "windows-prod",
			AvailableActiveGates: 1,
			ActiveGates: []extension.ActiveGateEntry{
				{ID: 305812744},
			},
		},
	}
}

func TestGolden_GetActiveGateGroups(t *testing.T) {
	groups := activeGateGroupFixtures()

	formats := map[string]string{
		"table": "table",
		"json":  "json",
		"yaml":  "yaml",
		"csv":   "csv",
	}

	for name, format := range formats {
		t.Run(name, func(t *testing.T) {
			var buf bytes.Buffer
			printer := NewPrinterWithWriter(format, &buf)
			if err := printer.PrintList(groups); err != nil {
				t.Fatalf("PrintList failed: %v", err)
			}
			assertGolden(t, "get/extension-active-gate-groups-"+name, buf.String())
		})
	}
}

// monitoringConfigSchemaFixture returns a synthetic JSON Schema document that
// mirrors the structure returned by the Extensions 2.0 schema endpoint.
// It intentionally includes documentation, customMessage, and displayName fields
// so that the --no-fluff stripping can be verified.
func monitoringConfigSchemaFixture() interface{} {
	return map[string]interface{}{
		"$schema":     "http://json-schema.org/draft-07/schema#",
		"displayName": "PostgreSQL Monitoring Configuration",
		"type":        "object",
		"properties": map[string]interface{}{
			"enabled": map[string]interface{}{
				"type":          "boolean",
				"displayName":   "Enable monitoring",
				"documentation": "When set to false, monitoring is suspended.",
				"customMessage": "Disabling will stop all metric collection.",
				"default":       true,
			},
			"host": map[string]interface{}{
				"type":        "string",
				"displayName": "Hostname",
				"description": "PostgreSQL server hostname or IP address.",
			},
			"port": map[string]interface{}{
				"type":        "integer",
				"displayName": "Port",
				"description": "PostgreSQL server port.",
				"default":     5432,
			},
			"credentials": map[string]interface{}{
				"type":          "object",
				"displayName":   "Credentials",
				"documentation": "Authentication settings for the PostgreSQL server.",
				"properties": map[string]interface{}{
					"username": map[string]interface{}{
						"type":          "string",
						"displayName":   "Username",
						"customMessage": "Use a read-only user for security.",
					},
					"password": map[string]interface{}{
						"type":        "string",
						"displayName": "Password",
					},
				},
			},
		},
	}
}

func TestGolden_DescribeExtensionSchema(t *testing.T) {
	schema := monitoringConfigSchemaFixture()

	formats := map[string]string{
		"json": "json",
		"yaml": "yaml",
	}

	for name, format := range formats {
		t.Run(name, func(t *testing.T) {
			var buf bytes.Buffer
			printer := NewPrinterWithWriter(format, &buf)
			if err := printer.Print(schema); err != nil {
				t.Fatalf("Print failed: %v", err)
			}
			assertGolden(t, "describe/extension-schema-"+name, buf.String())
		})
	}
}

func TestGolden_DescribeExtensionSchemaNoFluff(t *testing.T) {
	schema := monitoringConfigSchemaFixture()
	stripped := extension.StripSchemaFluff(schema)

	formats := map[string]string{
		"json": "json",
		"yaml": "yaml",
	}

	for name, format := range formats {
		t.Run(name, func(t *testing.T) {
			var buf bytes.Buffer
			printer := NewPrinterWithWriter(format, &buf)
			if err := printer.Print(stripped); err != nil {
				t.Fatalf("Print failed: %v", err)
			}
			assertGolden(t, "describe/extension-schema-no-fluff-"+name, buf.String())
		})
	}
}

func TestGolden_DescribeExtensionAssets(t *testing.T) {
	result := &extension.AssetResult{
		AlertTemplates: []extension.AlertAsset{
			{File: "alerts/cpu-saturation.json", Name: "CPU Saturation", EventType: "RESOURCE_CONTENTION", Enabled: boolPtr(true)},
			{File: "alerts/memory-usage.json", Name: "Memory Usage", EventType: "RESOURCE_CONTENTION", Enabled: boolPtr(false)},
		},
		Smartscape: &extension.SmartscapeAssetResult{
			Nodes: []extension.SmartscapeNode{
				{NodeType: "custom:my_service", NodeIDFieldName: "dt.entity.my_service", Description: "My Service node", Pipeline: "openpipeline/metrics.json"},
			},
			Edges: []extension.SmartscapeEdge{
				{SourceType: "custom:my_service", EdgeType: "runs_on", TargetType: "HOST"},
			},
		},
	}

	formats := map[string]string{
		"json": "json",
		"yaml": "yaml",
	}
	for name, format := range formats {
		t.Run(name, func(t *testing.T) {
			var buf bytes.Buffer
			printer := NewPrinterWithWriter(format, &buf)
			if err := printer.Print(result); err != nil {
				t.Fatalf("Print failed: %v", err)
			}
			assertGolden(t, "describe/extension-assets-"+name, buf.String())
		})
	}
}

// ---------------------------------------------------------------------------
// Translate: lql-to-dql
// ---------------------------------------------------------------------------

func TestGolden_TranslateLqlToDql(t *testing.T) {
	result := matcherlqltodql.TranslationResult{
		Query: `matchesValue(log.source, "snmptraps") and matchesValue(snmp.trap_oid, "F5-BIGIP-COMMON-MIB")`,
	}

	formats := map[string]string{
		"table": "table",
		"json":  "json",
		"yaml":  "yaml",
		"toon":  "toon",
	}

	for name, format := range formats {
		t.Run(name, func(t *testing.T) {
			var buf bytes.Buffer
			printer := NewPrinterWithWriter(format, &buf)
			if err := printer.Print(result); err != nil {
				t.Fatalf("Print failed: %v", err)
			}
			assertGolden(t, "translate/lql-to-dql-"+name, buf.String())
		})
	}
}

func platformTokenFixtures() []platformtoken.PlatformToken {
	return []platformtoken.PlatformToken{
		{
			Name:           "ci-pipeline",
			TokenID:        "a1b2c3d4-e5f6-4a7b-8c9d-000000000001",
			Status:         "ACTIVE",
			ExpirationDate: "2026-10-01T00:00:00.000Z",
			Scope:          "storage:events:read account-idm-read",
		},
		{
			Name:           "dev-automation",
			TokenID:        "b2c3d4e5-f6a7-4b8c-9d0e-000000000002",
			Status:         "ACTIVE",
			ExpirationDate: "2026-12-31T00:00:00.000Z",
			Scope:          "account-idm-write",
		},
		{
			Name:           "legacy-token",
			TokenID:        "c3d4e5f6-a7b8-4c9d-0e1f-000000000003",
			Status:         "REVOKED",
			ExpirationDate: "2025-01-01T00:00:00.000Z",
			Scope:          "storage:logs:read",
		},
	}
}

func TestGolden_GetPlatformTokens(t *testing.T) {
	tokens := platformTokenFixtures()

	formats := map[string]string{
		"table": "table",
		"wide":  "wide",
		"json":  "json",
		"yaml":  "yaml",
		"csv":   "csv",
		"toon":  "toon",
	}

	for name, format := range formats {
		t.Run(name, func(t *testing.T) {
			var buf bytes.Buffer
			printer := NewPrinterWithWriter(format, &buf)
			if err := printer.PrintList(tokens); err != nil {
				t.Fatalf("PrintList failed: %v", err)
			}
			assertGolden(t, "get/platform-tokens-"+name, buf.String())
		})
	}
}

func TestGolden_GetPlatformTokens_Empty(t *testing.T) {
	var buf bytes.Buffer
	printer := NewPrinterWithWriter("table", &buf)
	if err := printer.PrintList([]platformtoken.PlatformToken{}); err != nil {
		t.Fatalf("PrintList failed: %v", err)
	}
	assertGolden(t, "empty/platform-tokens", buf.String())
}

// ---------------------------------------------------------------------------
// OpenPipeline verify and preview golden tests
// ---------------------------------------------------------------------------

func matcherVerifyResultFixtures() []matcherverify.VerifyResult {
	return []matcherverify.VerifyResult{
		*matcherverify.FromSDKVerifyResponse(&sdkmatcherverify.VerifyResponse{Valid: true}),
		*matcherverify.FromSDKVerifyResponse(&sdkmatcherverify.VerifyResponse{
			Valid: false,
			Notifications: []sdkmatcherverify.MetadataNotification{
				{
					Severity: "ERROR",
					Message:  "unexpected token",
					SyntaxPosition: &sdkmatcherverify.SyntaxRange{
						Start: sdkmatcherverify.SyntaxPosition{Line: 1, Column: 1, Index: 0},
						End:   sdkmatcherverify.SyntaxPosition{Line: 1, Column: 6, Index: 5},
					},
				},
				{Severity: "WARN", Message: "expression is deprecated"},
			},
		}),
	}
}

func TestGolden_VerifyOpenpipelineMatcher(t *testing.T) {
	fixtures := matcherVerifyResultFixtures()

	formats := map[string]string{
		"table": "table",
		"json":  "json",
		"yaml":  "yaml",
	}

	for name, format := range formats {
		t.Run(name, func(t *testing.T) {
			var buf bytes.Buffer
			printer := NewPrinterWithWriter(format, &buf)
			if err := printer.PrintList(fixtures); err != nil {
				t.Fatalf("PrintList failed: %v", err)
			}
			assertGolden(t, "verify/openpipeline-matcher-"+name, buf.String())
		})
	}
}

func dqlProcessorVerifyResultFixtures() []dqlprocessorverify.VerifyResult {
	return []dqlprocessorverify.VerifyResult{
		*matcherverify.FromSDKVerifyResponse(&sdkmatcherverify.VerifyResponse{Valid: true}),
		*matcherverify.FromSDKVerifyResponse(&sdkmatcherverify.VerifyResponse{
			Valid: false,
			Notifications: []sdkmatcherverify.MetadataNotification{
				{
					Severity: "ERROR",
					Message:  "invalid field reference",
					SyntaxPosition: &sdkmatcherverify.SyntaxRange{
						Start: sdkmatcherverify.SyntaxPosition{Line: 2, Column: 5, Index: 0},
						End:   sdkmatcherverify.SyntaxPosition{Line: 2, Column: 12, Index: 0},
					},
				},
			},
		}),
	}
}

func TestGolden_VerifyOpenpipelineDQLProcessor(t *testing.T) {
	fixtures := dqlProcessorVerifyResultFixtures()

	formats := map[string]string{
		"table": "table",
		"json":  "json",
		"yaml":  "yaml",
	}

	for name, format := range formats {
		t.Run(name, func(t *testing.T) {
			var buf bytes.Buffer
			printer := NewPrinterWithWriter(format, &buf)
			if err := printer.PrintList(fixtures); err != nil {
				t.Fatalf("PrintList failed: %v", err)
			}
			assertGolden(t, "verify/openpipeline-dql-processor-"+name, buf.String())
		})
	}
}

func previewProcessorResultFixtures() []previewprocessor.PreviewResult {
	return []previewprocessor.PreviewResult{
		{
			Matched: true,
			Record: map[string]any{
				"content":  "error occurred in service-a",
				"severity": "ERROR",
				"foo":      float64(1),
			},
		},
		{
			Matched: false,
			Record: map[string]any{
				"content": "info log from service-b",
			},
		},
	}
}

func TestGolden_ExecPreviewProcessor(t *testing.T) {
	fixtures := previewProcessorResultFixtures()

	formats := map[string]string{
		"table": "table",
		"json":  "json",
		"yaml":  "yaml",
	}

	for name, format := range formats {
		t.Run(name, func(t *testing.T) {
			var buf bytes.Buffer
			printer := NewPrinterWithWriter(format, &buf)
			if err := printer.PrintList(fixtures); err != nil {
				t.Fatalf("PrintList failed: %v", err)
			}
			assertGolden(t, "exec/preview-processor-"+name, buf.String())
		})
	}
}

// apiInfoFixtures covers the three states a row of `dtctl get apis` can be in,
// because each renders differently and each has been got wrong: an API dtctl
// wraps natively, one it does not, and one documented at an absolute URL on
// another host (no base path, nothing dtctl can fetch).
//
// All of it is synthetic. A recording of a real environment's index would commit
// that environment's published API list to a public repository — and the list is a
// property of the environment, not of dtctl.
func apiInfoFixtures() []api.APIInfo {
	fourteen := 14
	three := 3
	return []api.APIInfo{
		{
			Name:       "Widget Service",
			BasePath:   "/platform/widget/v1",
			Category:   "Widgets",
			Operations: &fourteen,
			Dtctl:      "widget",
		},
		{
			Name:       "Sprocket Service",
			BasePath:   "/platform/sprocket/v1",
			Category:   "Sprockets",
			Operations: &three,
		},
		{
			// Operations is nil: the count is unknown rather than zero, and the OPS
			// cell must come out blank. A -1 sentinel used to print "-1" here.
			Name:     "Cog Service",
			BasePath: "/platform/cog/v1",
		},
		{
			Name:     "Elsewhere API",
			External: true,
		},
	}
}

func TestGolden_GetAPIs(t *testing.T) {
	apis := apiInfoFixtures()

	formats := map[string]string{
		"table": "table",
		"wide":  "wide",
		"json":  "json",
		"yaml":  "yaml",
		"csv":   "csv",
		"toon":  "toon",
	}

	for name, format := range formats {
		t.Run(name, func(t *testing.T) {
			var buf bytes.Buffer
			printer := NewPrinterWithWriter(format, &buf)
			if err := printer.PrintList(apis); err != nil {
				t.Fatalf("PrintList failed: %v", err)
			}
			assertGolden(t, "get/apis-"+name, buf.String())
		})
	}
}

// operationSummaryFixtures pins the operation index, including the two rows that
// carry meaning by being empty: an operation whose specification declares no scope
// (blank, and not the same as "needs nothing"), and a deprecated one, which is
// wide-only so the default view stays narrow.
func operationSummaryFixtures() []api.OperationSummary {
	return []api.OperationSummary{
		{Operation: "GET /widgets", Summary: "List all widgets.", Scope: "widget:widgets:read"},
		{Operation: "POST /widgets", Summary: "Create a widget.", Scope: "widget:widgets:write"},
		{Operation: "POST /widgets:search", Summary: "Search widgets.", Scope: "widget:widgets:read"},
		{Operation: "PUT /widgets/{id}", Summary: "Replace a widget."},
		{
			Operation:  "GET /widgets/{id}/legacy",
			Summary:    "Fetch the legacy representation.",
			Scope:      "widget:widgets:read",
			Deprecated: true,
		},
	}
}

func TestGolden_DescribeAPIOperations(t *testing.T) {
	ops := operationSummaryFixtures()

	formats := map[string]string{
		"table": "table",
		"wide":  "wide",
		"json":  "json",
		"yaml":  "yaml",
		"toon":  "toon",
	}

	for name, format := range formats {
		t.Run(name, func(t *testing.T) {
			var buf bytes.Buffer
			printer := NewPrinterWithWriter(format, &buf)
			if err := printer.PrintList(ops); err != nil {
				t.Fatalf("PrintList failed: %v", err)
			}
			assertGolden(t, "describe/api-operations-"+name, buf.String())
		})
	}
}

func schedulingRuleFixtures() []schedulingrule.SchedulingRule {
	return []schedulingrule.SchedulingRule{
		{
			ID:          "a1b2c3d4-e5f6-4a7b-8c9d-0e1f2a3b4c5d",
			Title:       "Business Hours",
			RuleType:    "rrule",
			Description: "Monday to Friday",
			Version:     3,
			RRule: map[string]interface{}{
				"freq":      "WEEKLY",
				"datestart": "2026-01-05",
				"byday":     []interface{}{"MO", "TU", "WE", "TH", "FR"},
			},
			ModificationInfo: &schedulingrule.ModificationInfo{
				CreatedBy:        "7a8b9c0d-1e2f-4a3b-8c4d-5e6f7a8b9c0d",
				CreatedTime:      "2026-01-05T08:00:00.000Z",
				LastModifiedBy:   "7a8b9c0d-1e2f-4a3b-8c4d-5e6f7a8b9c0d",
				LastModifiedTime: "2026-02-11T10:30:00.000Z",
			},
		},
		{
			ID:               "b2c3d4e5-f6a7-4b8c-9d0e-1f2a3b4c5d6e",
			Title:            "Maintenance Window",
			RuleType:         "grouping",
			BusinessCalendar: "c3d4e5f6-a7b8-4c9d-8e0f-2a3b4c5d6e7f",
			Version:          1,
			GroupingRule: map[string]interface{}{
				"combine": []interface{}{"a1b2c3d4-e5f6-4a7b-8c9d-0e1f2a3b4c5d"},
			},
			ModificationInfo: &schedulingrule.ModificationInfo{
				CreatedBy:        "8b9c0d1e-2f3a-4b5c-9d0e-1f2a3b4c5d6e",
				CreatedTime:      "2026-01-06T09:15:00.000Z",
				LastModifiedBy:   "8b9c0d1e-2f3a-4b5c-9d0e-1f2a3b4c5d6e",
				LastModifiedTime: "2026-01-06T09:15:00.000Z",
			},
		},
	}
}

func TestGolden_GetSchedulingRules(t *testing.T) {
	rules := schedulingRuleFixtures()

	formats := map[string]string{
		"table": "table",
		"wide":  "wide",
		"json":  "json",
		"yaml":  "yaml",
		"toon":  "toon",
	}

	for name, format := range formats {
		t.Run(name, func(t *testing.T) {
			var buf bytes.Buffer
			printer := NewPrinterWithWriter(format, &buf)
			if err := printer.PrintList(rules); err != nil {
				t.Fatalf("PrintList failed: %v", err)
			}
			assertGolden(t, "get/scheduling-rules-"+name, buf.String())
		})
	}
}

// ---------------------------------------------------------------------------
// Platform management: environment and license
// ---------------------------------------------------------------------------

func environmentFixture() platform.EnvironmentInfo {
	return platform.EnvironmentInfo{
		EnvironmentID: "abc12345",
		Type:          "INTERNAL",
		State:         "ACTIVE",
		CreateTime:    time.Date(2025, 3, 13, 13, 11, 4, 0, time.UTC),
		BlockTime:     time.Date(2031, 1, 1, 0, 0, 0, 0, time.UTC),
	}
}

func licenseFixture() platform.License {
	return platform.License{
		Trial:                false,
		PlatformSubscription: true,
	}
}

func TestGolden_GetEnvironment(t *testing.T) {
	info := environmentFixture()

	formats := map[string]string{
		"table": "table",
		"wide":  "wide",
		"json":  "json",
		"yaml":  "yaml",
		"toon":  "toon",
	}

	for name, format := range formats {
		t.Run(name, func(t *testing.T) {
			var buf bytes.Buffer
			printer := NewPrinterWithWriter(format, &buf)
			if err := printer.Print(info); err != nil {
				t.Fatalf("Print failed: %v", err)
			}
			assertGolden(t, "get/environment-"+name, buf.String())
		})
	}
}

func TestGolden_GetLicense(t *testing.T) {
	lic := licenseFixture()

	formats := map[string]string{
		"table": "table",
		"json":  "json",
		"yaml":  "yaml",
		"toon":  "toon",
	}

	for name, format := range formats {
		t.Run(name, func(t *testing.T) {
			var buf bytes.Buffer
			printer := NewPrinterWithWriter(format, &buf)
			if err := printer.Print(lic); err != nil {
				t.Fatalf("Print failed: %v", err)
			}
			assertGolden(t, "get/license-"+name, buf.String())
		})
	}
}

func TestGolden_DescribeEnvironment(t *testing.T) {
	info := environmentFixture()

	formats := map[string]string{
		"json": "json",
		"yaml": "yaml",
		"toon": "toon",
	}

	for name, format := range formats {
		t.Run(name, func(t *testing.T) {
			var buf bytes.Buffer
			printer := NewPrinterWithWriter(format, &buf)
			if err := printer.Print(info); err != nil {
				t.Fatalf("Print failed: %v", err)
			}
			assertGolden(t, "describe/environment-"+name, buf.String())
		})
	}
}

func TestGolden_DescribeLicense(t *testing.T) {
	lic := licenseFixture()

	formats := map[string]string{
		"json": "json",
		"yaml": "yaml",
		"toon": "toon",
	}

	for name, format := range formats {
		t.Run(name, func(t *testing.T) {
			var buf bytes.Buffer
			printer := NewPrinterWithWriter(format, &buf)
			if err := printer.Print(lic); err != nil {
				t.Fatalf("Print failed: %v", err)
			}
			assertGolden(t, "describe/license-"+name, buf.String())
		})
	}
}

func licenseSettingsFixture() []platform.LicenseSetting {
	return []platform.LicenseSetting{
		{Key: "AUTOMATION", Value: "true"},
		{Key: "AI_FUNCTIONS", Value: "true"},
		{Key: "LIVE_DEBUGGING", Value: "true"},
		{Key: "CONTAINER_APPLICATION_MONITORING", Value: "false"},
	}
}

func TestGolden_GetLicenseSettings(t *testing.T) {
	settings := licenseSettingsFixture()

	formats := map[string]string{
		"table": "table",
		"wide":  "wide",
		"json":  "json",
		"yaml":  "yaml",
		"toon":  "toon",
	}

	for name, format := range formats {
		t.Run(name, func(t *testing.T) {
			var buf bytes.Buffer
			printer := NewPrinterWithWriter(format, &buf)
			if err := printer.PrintList(settings); err != nil {
				t.Fatalf("PrintList failed: %v", err)
			}
			assertGolden(t, "get/license-settings-"+name, buf.String())
		})
	}
}

func TestGolden_GetLicenseSettings_Empty(t *testing.T) {
	formats := map[string]string{
		"table": "table",
		"wide":  "wide",
		"json":  "json",
		"yaml":  "yaml",
		"toon":  "toon",
	}

	for name, format := range formats {
		t.Run(name, func(t *testing.T) {
			var buf bytes.Buffer
			printer := NewPrinterWithWriter(format, &buf)
			if err := printer.PrintList([]platform.LicenseSetting{}); err != nil {
				t.Fatalf("PrintList failed: %v", err)
			}
			assertGolden(t, "empty/license-settings-"+name, buf.String())
		})
	}
}

func serviceUserFixtures() []serviceuser.ServiceUser {
	return []serviceuser.ServiceUser{
		{
			Name:        "automation-alpha",
			Email:       "automation-alpha@example.invalid",
			UID:         "00000000-0000-4000-8000-000000000101",
			CreatedAt:   "2026-03-01T12:00:00Z",
			Surname:     "ServiceUser",
			Description: "Synthetic integration identity",
			GroupUUID:   "00000000-0000-4000-8000-000000000201",
		},
		{
			Name:        "automation-beta",
			Email:       "automation-beta@example.invalid",
			UID:         "00000000-0000-4000-8000-000000000102",
			CreatedAt:   "2026-03-02T13:30:00Z",
			Surname:     "ServiceUser",
			Description: "Synthetic reporting identity",
			GroupUUID:   "00000000-0000-4000-8000-000000000202",
		},
	}
}

func TestGolden_GetServiceUsers(t *testing.T) {
	users := serviceUserFixtures()

	formats := map[string]string{
		"table": "table",
		"wide":  "wide",
		"json":  "json",
		"yaml":  "yaml",
		"csv":   "csv",
		"toon":  "toon",
	}

	for name, format := range formats {
		t.Run(name, func(t *testing.T) {
			var buf bytes.Buffer
			printer := NewPrinterWithWriter(format, &buf)
			if err := printer.PrintList(users); err != nil {
				t.Fatalf("PrintList failed: %v", err)
			}
			assertGolden(t, "get/service-users-"+name, buf.String())
		})
	}
}

func TestGolden_GetServiceUsers_Empty(t *testing.T) {
	var buf bytes.Buffer
	printer := NewPrinterWithWriter("table", &buf)
	if err := printer.PrintList([]serviceuser.ServiceUser{}); err != nil {
		t.Fatalf("PrintList failed: %v", err)
	}
	assertGolden(t, "empty/service-users", buf.String())
}

// actionFixtures is what `get actions` lists. Extra is deliberately absent
// from every entry: the flat /actions collection serves a fixed field set, so
// a list can never carry arbitrary keys. Only a single action has them --
// see actionWithExtraFixture.
func actionFixtures() []appengine.AppAction {
	return []appengine.AppAction{
		{
			AppID:       "example.app",
			ActionName:  "send-thing",
			Title:       "Send thing",
			Description: "Sends a thing",
			Stateful:    true,
			FullName:    "example.app:send-thing",
		},
		{
			AppID:       "example.app",
			ActionName:  "list-things",
			Title:       "List things",
			Description: "Lists the things",
			FullName:    "example.app:list-things",
		},
		{
			AppID:       "example.connector",
			ActionName:  "create-thing",
			Title:       "Create thing",
			Description: "Creates a thing",
			FullName:    "example.connector:create-thing",
		},
	}
}

// actionWithExtraFixture is a single action as `get action <app>/<name>`
// returns it: every manifest attribute the typed fields do not model lands in
// Extra, nested and scalar both. Hidden from tables, and emitted inline under
// its own name in the structured formats.
func actionWithExtraFixture() appengine.AppAction {
	return appengine.AppAction{
		AppID:       "example.connector",
		ActionName:  "create-thing",
		Title:       "Create thing",
		Description: "Creates a thing",
		FullName:    "example.connector:create-thing",
		Extra: map[string]json.RawMessage{
			"approval":             json.RawMessage(`true`),
			"widget":               json.RawMessage(`"/actions/create-thing"`),
			"expressionValidation": json.RawMessage(`{"owner":{"pattern":"^[A-Za-z0-9-]+$"}}`),
			"docs":                 json.RawMessage(`{"inputSchema":{"type":"object","required":["owner"]}}`),
		},
	}
}

func TestGolden_GetActions(t *testing.T) {
	actions := actionFixtures()

	formats := map[string]string{
		"table": "table",
		"wide":  "wide",
		"json":  "json",
		"yaml":  "yaml",
		"csv":   "csv",
		"toon":  "toon",
	}

	for name, format := range formats {
		t.Run(name, func(t *testing.T) {
			var buf bytes.Buffer
			printer := NewPrinterWithWriter(format, &buf)
			if err := printer.PrintList(actions); err != nil {
				t.Fatalf("PrintList failed: %v", err)
			}
			assertGolden(t, "get/actions-"+name, buf.String())
		})
	}
}

func TestGolden_GetAction(t *testing.T) {
	action := actionWithExtraFixture()

	formats := map[string]string{
		"table": "table",
		"wide":  "wide",
		"json":  "json",
		"yaml":  "yaml",
		"toon":  "toon",
	}

	for name, format := range formats {
		t.Run(name, func(t *testing.T) {
			var buf bytes.Buffer
			printer := NewPrinterWithWriter(format, &buf)
			if err := printer.Print(action); err != nil {
				t.Fatalf("Print failed: %v", err)
			}
			assertGolden(t, "get/action-single-"+name, buf.String())
		})
	}
}
