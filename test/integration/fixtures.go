//go:build integration
// +build integration

package integration

import (
	"encoding/json"
	"fmt"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/Dynatrace/dtctl/pkg/resources/awsconnection"
	"github.com/Dynatrace/dtctl/pkg/resources/awsmonitoringconfig"
	"github.com/Dynatrace/dtctl/pkg/resources/document"
	"github.com/Dynatrace/dtctl/pkg/resources/extension"
)

// WorkflowFixture returns a minimal workflow JSON for integration testing
func WorkflowFixture(prefix string) []byte {
	workflow := map[string]interface{}{
		"title":       fmt.Sprintf("%s-workflow", prefix),
		"description": "Integration test workflow",
		"tasks": map[string]interface{}{
			"test_task": map[string]interface{}{
				"name":   "test_task",
				"action": "dynatrace.automations:run-javascript",
				"input": map[string]interface{}{
					"script": "import { execution } from '@dynatrace-sdk/automation-utils';\nexport default async function() { const ex = await execution(); return { result: ex.input }; }",
				},
			},
		},
	}

	data, _ := json.Marshal(workflow)
	return data
}

// SchedulingRuleFixture returns a minimal scheduling rule for integration tests
func SchedulingRuleFixture(prefix string) []byte {
	rule := map[string]interface{}{
		"title":       fmt.Sprintf("%s-scheduling-rule", prefix),
		"description": "Integration test scheduling rule",
		"ruleType":    "rrule",
		"rrule": map[string]interface{}{
			"freq":      "WEEKLY",
			"datestart": "2026-01-05",
			"byday":     []string{"MO", "TU", "WE", "TH", "FR"},
		},
	}

	data, _ := json.Marshal(rule)
	return data
}

// SchedulingRuleFixtureModified returns a modified scheduling rule for update testing
func SchedulingRuleFixtureModified(prefix string) []byte {
	rule := map[string]interface{}{
		"title":       fmt.Sprintf("%s-scheduling-rule-modified", prefix),
		"description": "Modified integration test scheduling rule",
		"ruleType":    "rrule",
		"rrule": map[string]interface{}{
			"freq":      "DAILY",
			"datestart": "2026-01-05",
		},
	}

	data, _ := json.Marshal(rule)
	return data
}

// WorkflowFixtureModified returns a modified version of the workflow for edit testing
func WorkflowFixtureModified(prefix string) []byte {
	workflow := map[string]interface{}{
		"title":       fmt.Sprintf("%s-workflow-modified", prefix),
		"description": "Modified integration test workflow",
		"tasks": map[string]interface{}{
			"test_task": map[string]interface{}{
				"name":   "test_task",
				"action": "dynatrace.automations:run-javascript",
				"input": map[string]interface{}{
					"script": "export default async function() { return { result: 'Modified workflow' }; }",
				},
			},
			"second_task": map[string]interface{}{
				"name":   "second_task",
				"action": "dynatrace.automations:run-javascript",
				"input": map[string]interface{}{
					"script": "export default async function() { return { result: 'Second task added' }; }",
				},
			},
		},
	}

	data, _ := json.Marshal(workflow)
	return data
}

// DashboardFixture returns a minimal dashboard JSON for integration testing
func DashboardFixture(prefix string) []byte {
	dashboard := map[string]interface{}{
		"dashboardMetadata": map[string]interface{}{
			"name":   fmt.Sprintf("%s-dashboard", prefix),
			"shared": false,
		},
		"tiles": []map[string]interface{}{
			{
				"name":       "test-tile",
				"tileType":   "DATA_EXPLORER",
				"configured": true,
				"bounds": map[string]interface{}{
					"top":    0,
					"left":   0,
					"width":  400,
					"height": 200,
				},
			},
		},
	}

	data, _ := json.Marshal(dashboard)
	return data
}

// DashboardFixtureModified returns a modified dashboard for edit testing
func DashboardFixtureModified(prefix string) []byte {
	dashboard := map[string]interface{}{
		"dashboardMetadata": map[string]interface{}{
			"name":        fmt.Sprintf("%s-dashboard-modified", prefix),
			"description": "Modified dashboard",
			"shared":      false,
		},
		"tiles": []map[string]interface{}{
			{
				"name":       "test-tile",
				"tileType":   "DATA_EXPLORER",
				"configured": true,
				"bounds": map[string]interface{}{
					"top":    0,
					"left":   0,
					"width":  400,
					"height": 200,
				},
			},
			{
				"name":       "second-tile",
				"tileType":   "MARKDOWN",
				"configured": true,
				"bounds": map[string]interface{}{
					"top":    0,
					"left":   400,
					"width":  400,
					"height": 200,
				},
				"markdown": "# Modified Dashboard",
			},
		},
	}

	data, _ := json.Marshal(dashboard)
	return data
}

// DashboardFixtureLarge returns a large dashboard to test truncation issues
// This creates a dashboard with many tiles to ensure the response is > 10KB
func DashboardFixtureLarge(prefix string) []byte {
	tiles := make([]map[string]interface{}, 0)

	// Create 50 tiles to make a reasonably large dashboard (>10KB)
	for i := 0; i < 50; i++ {
		tile := map[string]interface{}{
			"name":       fmt.Sprintf("test-tile-%d", i),
			"tileType":   "DATA_EXPLORER",
			"configured": true,
			"bounds": map[string]interface{}{
				"top":    (i / 5) * 200,
				"left":   (i % 5) * 400,
				"width":  400,
				"height": 200,
			},
			"queries": []map[string]interface{}{
				{
					"id":    fmt.Sprintf("query-%d", i),
					"query": fmt.Sprintf("fetch logs | filter status == \"ERROR\" | filter tile == %d | summarize count = count(), by: {status}", i),
				},
			},
			// Add some padding to make each tile larger
			"customName": fmt.Sprintf("Custom Tile %d - This is a longer description to add more data", i),
		}
		tiles = append(tiles, tile)
	}

	dashboard := map[string]interface{}{
		"dashboardMetadata": map[string]interface{}{
			"name":        fmt.Sprintf("%s-dashboard-large", prefix),
			"description": "Large dashboard for testing content retrieval and truncation issues. This dashboard contains many tiles with queries.",
			"shared":      false,
		},
		"tiles": tiles,
	}

	data, _ := json.Marshal(dashboard)
	return data
}

// NotebookFixture returns a minimal notebook JSON for integration testing
func NotebookFixture(prefix string) []byte {
	notebook := map[string]interface{}{
		"name": fmt.Sprintf("%s-notebook", prefix),
		"sections": []map[string]interface{}{
			{
				"type":     "markdown",
				"title":    "Test Section",
				"state":    "default",
				"markdown": "# Integration Test Notebook\n\nThis is a test notebook.",
			},
		},
	}

	data, _ := json.Marshal(notebook)
	return data
}

// NotebookFixtureModified returns a modified notebook for edit testing
func NotebookFixtureModified(prefix string) []byte {
	notebook := map[string]interface{}{
		"name": fmt.Sprintf("%s-notebook-modified", prefix),
		"sections": []map[string]interface{}{
			{
				"type":     "markdown",
				"title":    "Test Section",
				"state":    "default",
				"markdown": "# Modified Integration Test Notebook\n\nThis notebook has been modified.",
			},
			{
				"type":     "markdown",
				"title":    "Second Section",
				"state":    "default",
				"markdown": "## New Section\n\nAdded during edit test.",
			},
		},
	}

	data, _ := json.Marshal(notebook)
	return data
}

// BucketName returns a unique bucket name for testing
func BucketName(prefix string) string {
	return fmt.Sprintf("%s_bucket", prefix)
}

// BucketCreateRequest returns a bucket creation request
func BucketCreateRequest(prefix string) map[string]interface{} {
	return map[string]interface{}{
		"bucketName":    BucketName(prefix),
		"table":         "logs",
		"displayName":   fmt.Sprintf("%s Integration Test Bucket", prefix),
		"retentionDays": 35,
	}
}

// BucketUpdateRequest returns a bucket update request
func BucketUpdateRequest(prefix string) map[string]interface{} {
	return map[string]interface{}{
		"displayName":   fmt.Sprintf("%s Modified Test Bucket", prefix),
		"retentionDays": 60,
	}
}

// WorkflowExecutionParams returns sample workflow execution parameters
func WorkflowExecutionParams() map[string]interface{} {
	return map[string]interface{}{
		"testParam": "testValue",
	}
}

// WorkflowExecutionInput returns sample workflow input for integration testing.
func WorkflowExecutionInput() map[string]interface{} {
	return map[string]interface{}{
		"message": "hello from dtctl",
		"count":   3,
		"options": map[string]interface{}{
			"dryRun": true,
		},
	}
}

// ToYAML converts a map to YAML bytes
func ToYAML(data map[string]interface{}) []byte {
	bytes, _ := yaml.Marshal(data)
	return bytes
}

// ToJSON converts a map to JSON bytes
func ToJSON(data map[string]interface{}) []byte {
	bytes, _ := json.Marshal(data)
	return bytes
}

// SettingsObjectFixture returns a minimal settings object for integration testing
// Uses builtin:loadtest.5k.owner-based schema which is a simple owner-based schema
func SettingsObjectFixture(prefix string) map[string]interface{} {
	return map[string]interface{}{
		"schemaId": "builtin:loadtest.5k.owner-based",
		"scope":    "environment",
		"value": map[string]interface{}{
			"text": fmt.Sprintf("%s-integration-test-settings", prefix),
		},
	}
}

// SettingsObjectFixtureModified returns a modified settings object for edit testing
func SettingsObjectFixtureModified(prefix string) map[string]interface{} {
	return map[string]interface{}{
		"text": fmt.Sprintf("%s-integration-test-settings-modified", prefix),
	}
}

// SLOFixture returns a minimal SLO for integration testing
func SLOFixture(prefix string) []byte {
	slo := map[string]interface{}{
		"name":        fmt.Sprintf("%s-slo", prefix),
		"description": "Integration test SLO",
		"criteria": []map[string]interface{}{
			{
				"timeframeFrom": "-1h",
				"target":        95.0,
				"warning":       98.0,
			},
		},
		"customSli": map[string]interface{}{
			"indicator": "fetch logs | filter status == \"INFO\" | summarize count = count()",
			"type":      "QUERY_RATIO",
		},
	}

	data, _ := json.Marshal(slo)
	return data
}

// SLOFixtureModified returns a modified SLO for edit testing
func SLOFixtureModified(prefix string) []byte {
	slo := map[string]interface{}{
		"name":        fmt.Sprintf("%s-slo-modified", prefix),
		"description": "Modified integration test SLO",
		"criteria": []map[string]interface{}{
			{
				"timeframeFrom": "-1h",
				"target":        90.0,
				"warning":       95.0,
			},
		},
		"customSli": map[string]interface{}{
			"indicator": "fetch logs | filter status == \"INFO\" | summarize count = count()",
			"type":      "QUERY_RATIO",
		},
	}

	data, _ := json.Marshal(slo)
	return data
}

// EdgeConnectFixture returns an EdgeConnect configuration for integration testing
func EdgeConnectFixture(prefix string) map[string]interface{} {
	return map[string]interface{}{
		"name": fmt.Sprintf("%s-edgeconnect", prefix),
		"hostPatterns": []string{
			fmt.Sprintf("*.%s.test.invalid", prefix), // Use .invalid TLD to ensure non-routable
		},
		"oauthClientId": fmt.Sprintf("%s-client-id", prefix),
	}
}

// EdgeConnectFixtureModified returns a modified EdgeConnect configuration for edit testing
func EdgeConnectFixtureModified(prefix string) map[string]interface{} {
	return map[string]interface{}{
		"name": fmt.Sprintf("%s-edgeconnect-modified", prefix),
		"hostPatterns": []string{
			fmt.Sprintf("*.%s.test.invalid", prefix),
			fmt.Sprintf("*.%s.modified.test.invalid", prefix),
		},
		"oauthClientId": fmt.Sprintf("%s-client-id-modified", prefix),
	}
}

// DashboardCreateRequest returns a CreateRequest for a dashboard
func DashboardCreateRequest(prefix string) document.CreateRequest {
	return document.CreateRequest{
		Name:    fmt.Sprintf("%s-dashboard", prefix),
		Type:    "dashboard",
		Content: DashboardFixture(prefix),
	}
}

// DashboardCreateRequestModified returns a modified CreateRequest for a dashboard
func DashboardCreateRequestModified(prefix string) document.CreateRequest {
	return document.CreateRequest{
		Name:    fmt.Sprintf("%s-dashboard-modified", prefix),
		Type:    "dashboard",
		Content: DashboardFixtureModified(prefix),
	}
}

// DashboardCreateRequestLarge returns a CreateRequest for a large dashboard
func DashboardCreateRequestLarge(prefix string) document.CreateRequest {
	return document.CreateRequest{
		Name:    fmt.Sprintf("%s-dashboard-large", prefix),
		Type:    "dashboard",
		Content: DashboardFixtureLarge(prefix),
	}
}

// NotebookCreateRequest returns a CreateRequest for a notebook
func NotebookCreateRequest(prefix string) document.CreateRequest {
	return document.CreateRequest{
		Name:    fmt.Sprintf("%s-notebook", prefix),
		Type:    "notebook",
		Content: NotebookFixture(prefix),
	}
}

// NotebookCreateRequestModified returns a modified CreateRequest for a notebook
func NotebookCreateRequestModified(prefix string) document.CreateRequest {
	return document.CreateRequest{
		Name:    fmt.Sprintf("%s-notebook-modified", prefix),
		Type:    "notebook",
		Content: NotebookFixtureModified(prefix),
	}
}

// MonitoringConfigFixture returns a monitoring configuration create request for integration testing.
// extensionName must be a pre-installed extension in the target environment.
// version is the extension version to configure monitoring for.
func MonitoringConfigFixture(prefix, extensionName, version string) extension.MonitoringConfigurationCreate {
	return extension.MonitoringConfigurationCreate{
		Scope: "environment",
		Value: map[string]any{
			"enabled":     true,
			"description": fmt.Sprintf("%s-monitoring-config", prefix),
			"version":     version,
		},
	}
}

// MonitoringConfigFixtureModified returns a modified monitoring configuration for update testing.
// version is the extension version to configure monitoring for.
func MonitoringConfigFixtureModified(prefix, version string) extension.MonitoringConfigurationCreate {
	return extension.MonitoringConfigurationCreate{
		Scope: "environment",
		Value: map[string]any{
			"enabled":     false,
			"description": fmt.Sprintf("%s-monitoring-config-modified", prefix),
			"version":     version,
		},
	}
}

// SegmentFixture returns a minimal filter segment JSON for integration testing
func SegmentFixture(prefix string) []byte {
	seg := map[string]interface{}{
		"name":        fmt.Sprintf("%s-segment", prefix),
		"description": "Integration test segment",
		"isPublic":    false,
		"includes": []map[string]interface{}{
			{
				"dataObject": "logs",
				"filter":     "status = \"ERROR\"",
			},
		},
	}

	data, _ := json.Marshal(seg)
	return data
}

// SegmentFixtureModified returns a modified filter segment for update testing
func SegmentFixtureModified(prefix string) []byte {
	seg := map[string]interface{}{
		"name":        fmt.Sprintf("%s-segment-modified", prefix),
		"description": "Modified integration test segment",
		"isPublic":    true,
		"includes": []map[string]interface{}{
			{
				"dataObject": "logs",
				"filter":     "status = \"ERROR\"",
			},
			{
				"dataObject": "spans",
				"filter":     "span.kind = \"SERVER\"",
			},
		},
	}

	data, _ := json.Marshal(seg)
	return data
}

// AnomalyDetectorFixture returns a minimal anomaly detector JSON for integration testing.
// Uses flattened format with a static threshold analyzer.
func AnomalyDetectorFixture(prefix string) []byte {
	ad := map[string]interface{}{
		"title":       fmt.Sprintf("%s-anomaly-detector", prefix),
		"description": "Integration test anomaly detector",
		"enabled":     true,
		"source":      "dtctl",
		"analyzer": map[string]interface{}{
			"name": "dt.statistics.ui.anomaly_detection.StaticThresholdAnomalyDetectionAnalyzer",
			"input": map[string]interface{}{
				"query":              "timeseries avg_cpu=avg(dt.host.cpu.usage), interval:5m",
				"threshold":          "95",
				"alertCondition":     "ABOVE",
				"violatingSamples":   "3",
				"slidingWindow":      "5",
				"dealertingSamples":  "5",
				"alertOnMissingData": "false",
			},
		},
		"eventTemplate": map[string]interface{}{
			"event.type":        "CUSTOM_ALERT",
			"event.name":        fmt.Sprintf("%s integration test alert", prefix),
			"event.description": "Integration test anomaly detector alert",
		},
		"executionSettings": map[string]interface{}{
			"queryOffset": 7,
		},
	}

	data, _ := json.Marshal(ad)
	return data
}

// AnomalyDetectorFixtureModified returns a modified anomaly detector for update testing
func AnomalyDetectorFixtureModified(prefix string) []byte {
	ad := map[string]interface{}{
		"title":       fmt.Sprintf("%s-anomaly-detector-modified", prefix),
		"description": "Modified integration test anomaly detector",
		"enabled":     false,
		"source":      "dtctl",
		"analyzer": map[string]interface{}{
			"name": "dt.statistics.ui.anomaly_detection.StaticThresholdAnomalyDetectionAnalyzer",
			"input": map[string]interface{}{
				"query":              "timeseries avg_cpu=avg(dt.host.cpu.usage), interval:5m",
				"threshold":          "90",
				"alertCondition":     "ABOVE",
				"violatingSamples":   "5",
				"slidingWindow":      "10",
				"dealertingSamples":  "10",
				"alertOnMissingData": "false",
			},
		},
		"eventTemplate": map[string]interface{}{
			"event.type":        "CUSTOM_ALERT",
			"event.name":        fmt.Sprintf("%s modified integration test alert", prefix),
			"event.description": "Modified integration test anomaly detector alert",
		},
		"executionSettings": map[string]interface{}{
			"queryOffset": 7,
		},
	}

	data, _ := json.Marshal(ad)
	return data
}

// SegmentFixtureMultiInclude returns a segment with multiple includes for query testing
func SegmentFixtureMultiInclude(prefix string) []byte {
	seg := map[string]interface{}{
		"name":        fmt.Sprintf("%s-segment-multi", prefix),
		"description": "Integration test segment with multiple includes",
		"isPublic":    false,
		"includes": []map[string]interface{}{
			{
				"dataObject": "logs",
				"filter":     "loglevel = \"ERROR\" OR loglevel = \"WARN\"",
			},
		},
	}

	data, _ := json.Marshal(seg)
	return data
}

// SegmentFixtureComplexFilter returns a segment with a complex filter
// expression that exercises parenthesized groups, mixed AND/OR, and implicit
// AND. This tests DQL→AST→DQL round-trip fidelity for non-trivial filters.
//
// Note: The Segments API only supports operators =, starts-with, contains,
// ends-with, and in. The DQL parser currently handles = and in (single-value
// only; tuple syntax like `in ("a","b")` is not supported). This fixture uses
// only the = operator to test structural complexity rather than operator variety.
func SegmentFixtureComplexFilter(prefix string) []byte {
	seg := map[string]interface{}{
		"name":        fmt.Sprintf("%s-segment-complex", prefix),
		"description": "Integration test segment with complex filter",
		"isPublic":    false,
		"includes": []map[string]interface{}{
			{
				"dataObject": "logs",
				"filter":     `(status = "ERROR" OR status = "WARN") AND dt.system.bucket = "custom-logs"`,
			},
			{
				"dataObject": "spans",
				"filter":     `span.kind = "CLIENT" http.method = "GET"`,
			},
		},
	}

	data, _ := json.Marshal(seg)
	return data
}

// AWSMonitoringConfigFixture returns a minimal AWS monitoring configuration in
// the disabled state, shaped the way `dtctl create aws monitoring` shapes it
// (see buildAWSMonitoringConfig in cmd/create_aws.go).
//
// credential must come from a connection that already exists in the
// environment: the extension validates the account id behind the connection's
// role ARN, so a synthetic credential is rejected.
func AWSMonitoringConfigFixture(prefix, version string, credential awsmonitoringconfig.Credential) awsmonitoringconfig.AWSMonitoringConfig {
	regions := []string{"us-east-1"}
	return awsmonitoringconfig.AWSMonitoringConfig{
		Scope: awsmonitoringconfig.DefaultScope,
		Value: awsmonitoringconfig.Value{
			Enabled:           false,
			Description:       fmt.Sprintf("%s-aws-monitoring", prefix),
			Version:           version,
			ActivationContext: awsmonitoringconfig.DefaultActivationContext,
			FeatureSets:       []string{"EC2_essential"},
			Aws: awsmonitoringconfig.AWSConfig{
				DeploymentRegion:        regions[0],
				Credentials:             []awsmonitoringconfig.Credential{credential},
				RegionFiltering:         regions,
				TagFiltering:            []awsmonitoringconfig.TagFilter{},
				TagEnrichment:           []string{},
				Namespaces:              []awsmonitoringconfig.CustomNamespace{},
				ConfigurationMode:       "QUICK_START",
				DeploymentMode:          "AUTOMATED",
				DeploymentScope:         "SINGLE_ACCOUNT",
				SmartscapeConfiguration: awsmonitoringconfig.FlagConfig{Enabled: true},
				MetricsConfiguration:    awsmonitoringconfig.RegionalFlagConfig{Enabled: true, Regions: regions},
			},
		},
	}
}

// AWSConnectionFixture returns an AWS connection creation request. The role ARN
// is deliberately empty: dtctl supports creating a connection first and
// patching the ARN once the IAM role exists with the connection's objectId as
// sts:ExternalId, and any non-empty ARN is validated by a real sts:AssumeRole.
func AWSConnectionFixture(prefix string) awsconnection.AWSConnectionCreate {
	return awsconnection.AWSConnectionCreate{
		Value: awsconnection.Value{
			Name: fmt.Sprintf("%s-aws-connection", prefix),
			Type: awsconnection.TypeRoleBased,
			AwsRoleBasedAuthentication: &awsconnection.AwsRoleBasedAuthenticationConfig{
				Consumers: []string{awsconnection.DefaultConsumer},
			},
		},
	}
}

// ToJSONBytes marshals v, failing the test instead of returning an error.
func ToJSONBytes(t *testing.T, v any) []byte {
	t.Helper()
	data, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("failed to marshal fixture: %v", err)
	}
	return data
}
