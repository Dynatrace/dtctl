package apply

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/dynatrace-oss/dtctl/pkg/config"
	"github.com/dynatrace-oss/dtctl/pkg/resources/anomalydetector"
	"github.com/dynatrace-oss/dtctl/pkg/resources/workflow"
	"github.com/dynatrace-oss/dtctl/pkg/safety"
)

func TestDetectResourceType(t *testing.T) {
	tests := []struct {
		name      string
		input     string
		expected  ResourceType
		wantArray bool
		wantErr   bool
	}{
		{
			name: "dashboard with tiles at root",
			input: `{
				"tiles": [{"name": "test", "tileType": "MARKDOWN"}],
				"version": "1"
			}`,
			expected: ResourceDashboard,
			wantErr:  false,
		},
		{
			name: "dashboard with content wrapper",
			input: `{
				"name": "My Dashboard",
				"content": {
					"tiles": [{"name": "test", "tileType": "MARKDOWN"}],
					"version": "1"
				}
			}`,
			expected: ResourceDashboard,
			wantErr:  false,
		},
		{
			name: "dashboard with type field",
			input: `{
				"type": "dashboard",
				"content": {"version": "1"}
			}`,
			expected: ResourceDashboard,
			wantErr:  false,
		},
		{
			name: "dashboard with metadata",
			input: `{
				"metadata": {"name": "test"},
				"type": "dashboard"
			}`,
			expected: ResourceDashboard,
			wantErr:  false,
		},
		{
			name: "notebook with sections at root",
			input: `{
				"sections": [{"title": "test"}]
			}`,
			expected: ResourceNotebook,
			wantErr:  false,
		},
		{
			name: "notebook with content wrapper",
			input: `{
				"name": "My Notebook",
				"content": {
					"sections": [{"title": "test"}]
				}
			}`,
			expected: ResourceNotebook,
			wantErr:  false,
		},
		{
			name: "workflow",
			input: `{
				"tasks": [{"name": "test"}],
				"trigger": {"type": "event"}
			}`,
			expected: ResourceWorkflow,
			wantErr:  false,
		},
		{
			name: "SLO",
			input: `{
				"name": "Test SLO",
				"criteria": {"threshold": 95},
				"customSli": {"enabled": true}
			}`,
			expected: ResourceSLO,
			wantErr:  false,
		},
		{
			name: "bucket",
			input: `{
				"bucketName": "my-bucket",
				"table": "logs"
			}`,
			expected: ResourceBucket,
			wantErr:  false,
		},
		{
			name: "gcp connection",
			input: `{
				"schemaId": "builtin:hyperscaler-authentication.connections.gcp",
				"scope": "environment",
				"value": {
					"name": "gcp-conn",
					"type": "serviceAccountImpersonation"
				}
			}`,
			expected: ResourceGCPConnection,
			wantErr:  false,
		},
		{
			name: "gcp monitoring config",
			input: `{
				"scope": "integration-gcp",
				"value": {
					"description": "gcp-monitoring",
					"googleCloud": {
						"credentials": []
					}
				}
			}`,
			expected: ResourceGCPMonitoringConfig,
			wantErr:  false,
		},
		{
			name: "aws monitoring config",
			input: `{
				"scope": "integration-aws",
				"value": {
					"description": "aws-monitoring",
					"enabled": true,
					"aws": {
						"credentials": []
					}
				}
			}`,
			expected: ResourceAWSMonitoringConfig,
			wantErr:  false,
		},
		{
			name: "unknown resource",
			input: `{
				"random": "field"
			}`,
			expected: ResourceUnknown,
			wantErr:  true,
		},
		// Array detection tests — regression for #180
		{
			name: "array of settings objects",
			input: `[
				{"schemaId": "builtin:rum.web.enablement", "scope": "APPLICATION-123", "value": {"enabled": true}},
				{"schemaId": "builtin:rum.web.enablement", "scope": "APPLICATION-456", "value": {"enabled": false}}
			]`,
			expected:  ResourceSettings,
			wantArray: true,
		},
		{
			name: "array of workflows",
			input: `[
				{"tasks": {"t1": {}}, "trigger": {"type": "event"}},
				{"tasks": {"t2": {}}, "trigger": {"type": "schedule"}}
			]`,
			expected:  ResourceWorkflow,
			wantArray: true,
		},
		{
			name: "array of SLOs",
			input: `[
				{"name": "SLO 1", "criteria": {"threshold": 95}, "customSli": {"enabled": true}},
				{"name": "SLO 2", "criteria": {"threshold": 99}, "customSli": {"enabled": true}}
			]`,
			expected:  ResourceSLO,
			wantArray: true,
		},
		{
			name:     "empty array",
			input:    `[]`,
			expected: ResourceUnknown,
			wantErr:  true,
		},
		{
			name:     "array of unknown objects",
			input:    `[{"random": "field"}]`,
			expected: ResourceUnknown,
			wantErr:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Validate JSON (accept both objects and arrays)
			if !json.Valid([]byte(tt.input)) && !tt.wantErr {
				t.Fatalf("test input is not valid JSON: %s", tt.input)
			}

			result, isArray, err := detectResourceType([]byte(tt.input))

			if tt.wantErr {
				if err == nil {
					t.Errorf("expected error, got nil")
				}
			} else {
				if err != nil {
					t.Errorf("unexpected error: %v", err)
				}
			}

			if result != tt.expected {
				t.Errorf("expected %s, got %s", tt.expected, result)
			}
			if isArray != tt.wantArray {
				t.Errorf("expected isArray=%v, got %v", tt.wantArray, isArray)
			}
		})
	}
}

func TestIsUUID(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected bool
	}{
		{
			name:     "valid UUID lowercase",
			input:    "550e8400-e29b-41d4-a716-446655440000",
			expected: true,
		},
		{
			name:     "valid UUID uppercase",
			input:    "550E8400-E29B-41D4-A716-446655440000",
			expected: true,
		},
		{
			name:     "valid UUID mixed case",
			input:    "550e8400-E29B-41d4-A716-446655440000",
			expected: true,
		},
		{
			name:     "empty string",
			input:    "",
			expected: false,
		},
		{
			name:     "too short",
			input:    "550e8400-e29b-41d4",
			expected: false,
		},
		{
			name:     "no dashes",
			input:    "550e8400e29b41d4a716446655440000",
			expected: false,
		},
		{
			name:     "wrong dash positions",
			input:    "550e-8400-e29b-41d4-a716-446655440000",
			expected: false,
		},
		{
			name:     "contains invalid characters",
			input:    "550e8400-e29b-41d4-a716-44665544000g",
			expected: false,
		},
		{
			name:     "simple string",
			input:    "my-dashboard-id",
			expected: false,
		},
		{
			name:     "document ID format (not UUID)",
			input:    "abc123def456",
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := isUUID(tt.input)
			if result != tt.expected {
				t.Errorf("isUUID(%q) = %v, want %v", tt.input, result, tt.expected)
			}
		})
	}
}

func TestExtractDocumentContent(t *testing.T) {
	tests := []struct {
		name            string
		doc             map[string]interface{}
		docType         string
		wantName        string
		wantDescription string
		wantWarnings    int
		wantTiles       bool // check if content has tiles
		wantSections    bool // check if content has sections
	}{
		{
			name: "dashboard with content wrapper",
			doc: map[string]interface{}{
				"name":        "My Dashboard",
				"description": "A test dashboard",
				"content": map[string]interface{}{
					"tiles":   []interface{}{map[string]interface{}{"name": "tile1"}},
					"version": "1",
				},
			},
			docType:         "dashboard",
			wantName:        "My Dashboard",
			wantDescription: "A test dashboard",
			wantWarnings:    0,
			wantTiles:       true,
		},
		{
			name: "dashboard with direct tiles",
			doc: map[string]interface{}{
				"name":  "Direct Dashboard",
				"tiles": []interface{}{map[string]interface{}{"name": "tile1"}},
			},
			docType:      "dashboard",
			wantName:     "Direct Dashboard",
			wantWarnings: 0,
			wantTiles:    true,
		},
		{
			name: "dashboard missing tiles warning",
			doc: map[string]interface{}{
				"name": "Empty Dashboard",
				"content": map[string]interface{}{
					"version": "1",
				},
			},
			docType:      "dashboard",
			wantName:     "Empty Dashboard",
			wantWarnings: 1, // missing tiles warning
		},
		{
			name: "dashboard missing version warning",
			doc: map[string]interface{}{
				"name": "Dashboard",
				"content": map[string]interface{}{
					"tiles": []interface{}{},
				},
			},
			docType:      "dashboard",
			wantName:     "Dashboard",
			wantWarnings: 1, // missing version warning
		},
		{
			name: "dashboard with double-nested content",
			doc: map[string]interface{}{
				"name": "Double Nested",
				"content": map[string]interface{}{
					"content": map[string]interface{}{
						"tiles":   []interface{}{},
						"version": "1",
					},
				},
			},
			docType:      "dashboard",
			wantName:     "Double Nested",
			wantWarnings: 1, // double-nested warning
		},
		{
			name: "notebook with sections",
			doc: map[string]interface{}{
				"name": "My Notebook",
				"content": map[string]interface{}{
					"sections": []interface{}{map[string]interface{}{"title": "section1"}},
				},
			},
			docType:      "notebook",
			wantName:     "My Notebook",
			wantWarnings: 0,
			wantSections: true,
		},
		{
			name: "notebook missing sections warning",
			doc: map[string]interface{}{
				"name": "Empty Notebook",
				"content": map[string]interface{}{
					"version": "1",
				},
			},
			docType:      "notebook",
			wantName:     "Empty Notebook",
			wantWarnings: 1, // missing sections warning
		},
		{
			name: "notebook with direct sections",
			doc: map[string]interface{}{
				"name":     "Direct Notebook",
				"sections": []interface{}{map[string]interface{}{"title": "section1"}},
			},
			docType:      "notebook",
			wantName:     "Direct Notebook",
			wantWarnings: 0,
			wantSections: true,
		},
		{
			name: "dashboard with no content or tiles",
			doc: map[string]interface{}{
				"name": "Broken Dashboard",
			},
			docType:      "dashboard",
			wantName:     "Broken Dashboard",
			wantWarnings: 1, // structure warning
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			contentData, name, description, warnings := extractDocumentContent(tt.doc, tt.docType)

			if name != tt.wantName {
				t.Errorf("name = %q, want %q", name, tt.wantName)
			}

			if description != tt.wantDescription {
				t.Errorf("description = %q, want %q", description, tt.wantDescription)
			}

			if len(warnings) != tt.wantWarnings {
				t.Errorf("got %d warnings, want %d: %v", len(warnings), tt.wantWarnings, warnings)
			}

			// Verify content is valid JSON
			var content map[string]interface{}
			if err := json.Unmarshal(contentData, &content); err != nil {
				t.Errorf("contentData is not valid JSON: %v", err)
			}

			// Check for expected content structure
			if tt.wantTiles {
				if _, ok := content["tiles"]; !ok {
					t.Error("expected tiles in content")
				}
			}
			if tt.wantSections {
				if _, ok := content["sections"]; !ok {
					t.Error("expected sections in content")
				}
			}
		})
	}
}

func TestExtractDocumentContent_NonMapNestedContent(t *testing.T) {
	tests := []struct {
		name  string
		inner interface{}
	}{
		{"string", "# Welcome to the team"},
		{"nil", nil},
		{"slice", []interface{}{"a"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			doc := map[string]interface{}{
				"name": "Launchpad",
				"content": map[string]interface{}{
					"content": tt.inner,
					"format":  "markdown",
				},
			}

			contentData, name, _, warnings := extractDocumentContent(doc, "launchpad")

			if name != "Launchpad" {
				t.Errorf("name = %q, want %q", name, "Launchpad")
			}
			if len(warnings) != 0 {
				t.Errorf("got %d warnings, want 0: %v", len(warnings), warnings)
			}

			var content map[string]interface{}
			if err := json.Unmarshal(contentData, &content); err != nil {
				t.Fatalf("contentData is not valid JSON: %v", err)
			}
			if content["format"] != "markdown" {
				t.Errorf("format = %v, want markdown - a non-map .content.content must leave the outer content in place", content["format"])
			}
		})
	}
}

func TestCountDocumentItems(t *testing.T) {
	tests := []struct {
		name     string
		content  string
		docType  string
		expected int
	}{
		{
			name:     "dashboard with 3 tiles",
			content:  `{"tiles": [{"name": "a"}, {"name": "b"}, {"name": "c"}], "version": "1"}`,
			docType:  "dashboard",
			expected: 3,
		},
		{
			name:     "dashboard with no tiles",
			content:  `{"version": "1"}`,
			docType:  "dashboard",
			expected: 0,
		},
		{
			name:     "dashboard with empty tiles",
			content:  `{"tiles": [], "version": "1"}`,
			docType:  "dashboard",
			expected: 0,
		},
		{
			name:     "notebook with 2 sections",
			content:  `{"sections": [{"title": "a"}, {"title": "b"}]}`,
			docType:  "notebook",
			expected: 2,
		},
		{
			name:     "notebook with no sections",
			content:  `{"version": "1"}`,
			docType:  "notebook",
			expected: 0,
		},
		{
			name:     "invalid JSON",
			content:  `{invalid}`,
			docType:  "dashboard",
			expected: 0,
		},
		{
			name:     "tiles is not an array",
			content:  `{"tiles": "not-an-array"}`,
			docType:  "dashboard",
			expected: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := countDocumentItems([]byte(tt.content), tt.docType)
			if result != tt.expected {
				t.Errorf("countDocumentItems() = %d, want %d", result, tt.expected)
			}
		})
	}
}

func TestItemName(t *testing.T) {
	tests := []struct {
		docType  string
		expected string
	}{
		{"dashboard", "tiles"},
		{"notebook", "sections"},
		{"acme:config", "items"}, // custom types get a generic item name
	}

	for _, tt := range tests {
		t.Run(tt.docType, func(t *testing.T) {
			result := itemName(tt.docType)
			if result != tt.expected {
				t.Errorf("itemName(%q) = %q, want %q", tt.docType, result, tt.expected)
			}
		})
	}
}

func TestCapitalize(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"dashboard", "Dashboard"},
		{"notebook", "Notebook"},
		{"tiles", "Tiles"},
		{"sections", "Sections"},
		{"a", "A"},
		{"", ""},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			result := capitalize(tt.input)
			if result != tt.expected {
				t.Errorf("capitalize(%q) = %q, want %q", tt.input, result, tt.expected)
			}
		})
	}
}

func TestShowJSONDiff(t *testing.T) {
	tests := []struct {
		name         string
		oldData      string
		newData      string
		resourceType string
		wantContains []string
	}{
		{
			name:         "simple change",
			oldData:      `{"name": "old"}`,
			newData:      `{"name": "new"}`,
			resourceType: "dashboard",
			wantContains: []string{"--- existing dashboard", "+++ new dashboard", "old", "new"},
		},
		{
			name:         "no changes",
			oldData:      `{"name": "same"}`,
			newData:      `{"name": "same"}`,
			resourceType: "notebook",
			wantContains: []string{"(no changes)"},
		},
		{
			name:         "addition",
			oldData:      `{}`,
			newData:      `{"key": "value"}`,
			resourceType: "dashboard",
			wantContains: []string{"+"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Capture stderr
			old := os.Stderr
			r, w, _ := os.Pipe()
			os.Stderr = w

			(&Applier{}).showJSONDiff([]byte(tt.oldData), []byte(tt.newData), tt.resourceType)

			_ = w.Close()
			os.Stderr = old

			var buf bytes.Buffer
			_, _ = io.Copy(&buf, r)
			output := buf.String()

			for _, want := range tt.wantContains {
				if !strings.Contains(output, want) {
					t.Errorf("output missing %q, got: %s", want, output)
				}
			}
		})
	}
}

func TestDocumentURL(t *testing.T) {
	tests := []struct {
		name     string
		baseURL  string
		docType  string
		id       string
		expected string
	}{
		{
			name:     "dashboard URL",
			baseURL:  "https://abc12345.apps.dynatrace.com",
			docType:  "dashboard",
			id:       "doc-123",
			expected: "https://abc12345.apps.dynatrace.com/ui/apps/dynatrace.dashboards/dashboard/doc-123",
		},
		{
			name:     "notebook URL",
			baseURL:  "https://abc12345.apps.dynatrace.com",
			docType:  "notebook",
			id:       "nb-456",
			expected: "https://abc12345.apps.dynatrace.com/ui/apps/dynatrace.notebooks/notebook/nb-456",
		},
		{
			name:     "launchpad URL uses the launcher app",
			baseURL:  "https://abc12345.apps.dynatrace.com",
			docType:  "launchpad",
			id:       "team-launchpad",
			expected: "https://abc12345.apps.dynatrace.com/ui/apps/dynatrace.launcher/launchpad/team-launchpad",
		},
		{
			name:     "custom document type has no derivable viewer app URL",
			baseURL:  "https://tenant.apps.dynatrace.com",
			docType:  "acme:config",
			id:       "rpt-789",
			expected: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Create applier with just the baseURL (no real client needed for this test)
			a := &Applier{baseURL: tt.baseURL}

			result := a.documentURL(tt.docType, tt.id)
			if result != tt.expected {
				t.Errorf("documentURL() = %q, want %q", result, tt.expected)
			}
		})
	}
}

func TestDetectResourceTypeEdgeCases(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected ResourceType
		wantErr  bool
	}{
		{
			name:     "invalid JSON",
			input:    `{not valid json}`,
			expected: ResourceUnknown,
			wantErr:  true,
		},
		{
			name:     "empty object",
			input:    `{}`,
			expected: ResourceUnknown,
			wantErr:  true,
		},
		{
			name: "notebook with type field",
			input: `{
				"type": "notebook",
				"content": {"sections": []}
			}`,
			expected: ResourceNotebook,
			wantErr:  false,
		},
		{
			name: "SLO with sliReference",
			input: `{
				"name": "Test SLO",
				"criteria": {"threshold": 95},
				"sliReference": {"id": "sli-123"}
			}`,
			expected: ResourceSLO,
			wantErr:  false,
		},
		{
			name: "SLO minimal (criteria and name only)",
			input: `{
				"name": "Minimal SLO",
				"criteria": {"threshold": 99}
			}`,
			expected: ResourceSLO,
			wantErr:  false,
		},
		{
			name: "settings with camelCase schemaId",
			input: `{
				"schemaId": "builtin:alerting.profile",
				"scope": "environment",
				"value": {"enabled": true}
			}`,
			expected: ResourceSettings,
			wantErr:  false,
		},
		{
			name: "settings with lowercase schemaid",
			input: `{
				"schemaid": "builtin:alerting.profile",
				"scope": "environment",
				"value": {"enabled": true}
			}`,
			expected: ResourceSettings,
			wantErr:  false,
		},
		{
			name: "metadata only defaults to dashboard",
			input: `{
				"metadata": {"name": "test"}
			}`,
			expected: ResourceDashboard,
			wantErr:  false,
		},
		{
			name: "metadata with notebook type",
			input: `{
				"metadata": {"name": "test"},
				"type": "notebook"
			}`,
			expected: ResourceNotebook,
			wantErr:  false,
		},
		{
			name: "tasks without trigger is a manual workflow",
			input: `{
				"tasks": [{"name": "test"}]
			}`,
			expected: ResourceWorkflow,
			wantErr:  false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, _, err := detectResourceType([]byte(tt.input))

			if tt.wantErr {
				if err == nil {
					t.Errorf("expected error, got nil")
				}
			} else {
				if err != nil {
					t.Errorf("unexpected error: %v", err)
				}
			}

			if result != tt.expected {
				t.Errorf("expected %s, got %s", tt.expected, result)
			}
		})
	}
}

// TestApplierSafetyCheckMethods tests the safety checking helper methods on the Applier
// This is a regression test for the bug where readwrite-mine would always fail because
// the safety check used OwnershipUnknown instead of determining actual ownership.
func TestApplierSafetyCheckMethods(t *testing.T) {
	t.Run("determineOwnership returns correct ownership", func(t *testing.T) {
		// Test with matching user IDs
		a := &Applier{currentUserID: "user-123"}
		ownership := a.determineOwnership("user-123")
		if ownership != safety.OwnershipOwn {
			t.Errorf("expected OwnershipOwn, got %v", ownership)
		}

		// Test with different user IDs
		ownership = a.determineOwnership("user-456")
		if ownership != safety.OwnershipShared {
			t.Errorf("expected OwnershipShared, got %v", ownership)
		}

		// Test with empty resource owner
		ownership = a.determineOwnership("")
		if ownership != safety.OwnershipUnknown {
			t.Errorf("expected OwnershipUnknown when resource owner is empty, got %v", ownership)
		}

		// Test with empty current user
		a2 := &Applier{currentUserID: ""}
		ownership = a2.determineOwnership("user-123")
		if ownership != safety.OwnershipUnknown {
			t.Errorf("expected OwnershipUnknown when current user is empty, got %v", ownership)
		}
	})

	t.Run("checkSafety with readwrite-mine allows own resource updates", func(t *testing.T) {
		checker := safety.NewChecker("test-context", &config.Context{
			SafetyLevel: config.SafetyLevelReadWriteMine,
		})

		a := &Applier{
			currentUserID: "user-123",
			safetyChecker: checker,
		}

		// Create operations should be allowed
		err := a.checkSafety(safety.OperationCreate, safety.OwnershipUnknown)
		if err != nil {
			t.Errorf("create should be allowed in readwrite-mine: %v", err)
		}

		// Update of own resource should be allowed
		err = a.checkSafety(safety.OperationUpdate, safety.OwnershipOwn)
		if err != nil {
			t.Errorf("update of own resource should be allowed: %v", err)
		}

		// Update of shared resource should be blocked
		err = a.checkSafety(safety.OperationUpdate, safety.OwnershipShared)
		if err == nil {
			t.Error("update of shared resource should be blocked in readwrite-mine")
		}

		// Update with unknown ownership should be blocked (this was the bug!)
		err = a.checkSafety(safety.OperationUpdate, safety.OwnershipUnknown)
		if err == nil {
			t.Error("update with unknown ownership should be blocked in readwrite-mine")
		}
	})

	t.Run("checkSafety without checker allows all operations", func(t *testing.T) {
		// Applier without safety checker should allow everything
		a := &Applier{currentUserID: "user-123"}

		err := a.checkSafety(safety.OperationUpdate, safety.OwnershipShared)
		if err != nil {
			t.Errorf("without checker, all operations should be allowed: %v", err)
		}
	})
}

// TestApplierOwnershipDeterminationRegression is a regression test that verifies
// the fix for the bug where apply/edit commands with readwrite-mine safety level
// would always fail because ownership was not properly determined from metadata.
//
// The bug was:
// 1. cmd/apply.go did safety check with OwnershipUnknown BEFORE knowing if it's create/update
// 2. readwrite-mine blocks updates with OwnershipUnknown
// 3. Therefore, apply would always fail with readwrite-mine even for your own resources
//
// The fix:
// 1. Safety checks now happen INSIDE the applier methods
// 2. For updates, ownership is determined from the resource metadata (which includes owner)
// 3. Create operations use OwnershipUnknown (which is allowed in readwrite-mine)
func TestApplierOwnershipDeterminationRegression(t *testing.T) {
	t.Run("readwrite-mine allows create with OwnershipUnknown", func(t *testing.T) {
		checker := safety.NewChecker("test", &config.Context{
			SafetyLevel: config.SafetyLevelReadWriteMine,
		})

		a := &Applier{safetyChecker: checker}

		// This is what happens for new resource creation
		err := a.checkSafety(safety.OperationCreate, safety.OwnershipUnknown)
		if err != nil {
			t.Errorf("BUG: readwrite-mine should allow creates: %v", err)
		}
	})

	t.Run("readwrite-mine blocks update with OwnershipUnknown", func(t *testing.T) {
		checker := safety.NewChecker("test", &config.Context{
			SafetyLevel: config.SafetyLevelReadWriteMine,
		})

		a := &Applier{safetyChecker: checker}

		// This is what the OLD buggy code did - it passed OwnershipUnknown for updates
		err := a.checkSafety(safety.OperationUpdate, safety.OwnershipUnknown)
		if err == nil {
			t.Error("readwrite-mine should block updates with unknown ownership")
		}
	})

	t.Run("readwrite-mine allows update when ownership is properly determined", func(t *testing.T) {
		checker := safety.NewChecker("test", &config.Context{
			SafetyLevel: config.SafetyLevelReadWriteMine,
		})

		// Simulate: current user is "user-123", resource owner is also "user-123"
		a := &Applier{
			currentUserID: "user-123",
			safetyChecker: checker,
		}

		// This is what the NEW fixed code does - it determines ownership from metadata
		resourceOwner := "user-123" // This comes from metadata.Owner or wf.Owner
		ownership := a.determineOwnership(resourceOwner)

		if ownership != safety.OwnershipOwn {
			t.Errorf("expected OwnershipOwn, got %v", ownership)
		}

		err := a.checkSafety(safety.OperationUpdate, ownership)
		if err != nil {
			t.Errorf("BUG: readwrite-mine should allow updating own resources: %v", err)
		}
	})
}

// TestAnomalyDetectorRoundTrip is the regression test for issue #216.
//
// It verifies that the JSON and YAML output of `dtctl get anomaly-detector` is
// directly consumable by `dtctl apply -f` — i.e., that detection identifies the
// resource type correctly. Before the fix, the AnomalyDetector struct serialized
// a hybrid shape (top-level display fields plus a nested `value` payload) that
// neither matched the raw Settings format nor the flattened authoring format,
// causing apply to fail with "could not detect resource type from file content".
//
// This test guards against regressions of that class: if anyone reverts the
// custom MarshalJSON/MarshalYAML or reintroduces display fields into the wire
// shape, this test fails immediately.
func TestAnomalyDetectorRoundTrip(t *testing.T) {
	// Build a fixture with the same shape the Settings API returns.
	// Mirror the public anomalydetector.AnomalyDetector exactly as `Get` would
	// populate it: derived display fields populated alongside the raw Value.
	ad := anomalydetector.AnomalyDetector{
		ObjectID:      "vu9U3hXa3q0AAAA",
		Title:         "Test Detector",
		Enabled:       true,
		AnalyzerShort: "static (>90)",
		EventType:     "PERFORMANCE_EVENT",
		Source:        "dtctl",
		Description:   "Round-trip test fixture",
		SchemaVersion: "1.0.42",
		Value: map[string]any{
			"title":       "Test Detector",
			"enabled":     true,
			"description": "Round-trip test fixture",
			"source":      "dtctl",
			"analyzer": map[string]any{
				"name": "dt.statistics.ui.anomaly_detection.StaticThresholdAnomalyDetectionAnalyzer",
				"input": []any{
					map[string]any{"key": "alertCondition", "value": "ABOVE"},
					map[string]any{"key": "threshold", "value": "90"},
				},
			},
			"eventTemplate": map[string]any{
				"properties": []any{
					map[string]any{"key": "event.type", "value": "PERFORMANCE_EVENT"},
					map[string]any{"key": "event.name", "value": "High CPU"},
				},
			},
		},
	}

	t.Run("JSON output is detected as anomaly detector", func(t *testing.T) {
		data, err := json.Marshal(ad)
		if err != nil {
			t.Fatalf("json.Marshal: %v", err)
		}

		// Sanity: the wire shape must include schemaId at the top level.
		var probe map[string]any
		if err := json.Unmarshal(data, &probe); err != nil {
			t.Fatalf("unmarshal probe: %v", err)
		}
		if probe["schemaId"] != anomalydetector.SchemaID {
			t.Errorf("JSON output missing schemaId at top level (got %q); display fields may be leaking into wire format", probe["schemaId"])
		}
		if _, hasAnalyzerLeak := probe["analyzer"]; hasAnalyzerLeak {
			t.Error("JSON output should not contain top-level 'analyzer' (display field leak)")
		}
		if _, hasEventTypeLeak := probe["eventType"]; hasEventTypeLeak {
			t.Error("JSON output should not contain top-level 'eventType' (display field leak)")
		}

		rt, isArray, err := detectResourceType(data)
		if err != nil {
			t.Fatalf("detectResourceType: %v\noutput was:\n%s", err, data)
		}
		if isArray {
			t.Error("expected single object, got array")
		}
		if rt != ResourceAnomalyDetector {
			t.Errorf("ResourceType = %v, want %v", rt, ResourceAnomalyDetector)
		}
	})

	t.Run("YAML output is detected as anomaly detector", func(t *testing.T) {
		yamlData, err := yaml.Marshal(ad)
		if err != nil {
			t.Fatalf("yaml.Marshal: %v", err)
		}

		// Apply accepts YAML by converting to JSON first; emulate that path.
		var doc map[string]any
		if err := yaml.Unmarshal(yamlData, &doc); err != nil {
			t.Fatalf("yaml.Unmarshal: %v\noutput was:\n%s", err, yamlData)
		}
		jsonData, err := json.Marshal(doc)
		if err != nil {
			t.Fatalf("json.Marshal: %v", err)
		}

		rt, _, err := detectResourceType(jsonData)
		if err != nil {
			t.Fatalf("detectResourceType after YAML round-trip: %v\nyaml was:\n%s", err, yamlData)
		}
		if rt != ResourceAnomalyDetector {
			t.Errorf("ResourceType = %v, want %v", rt, ResourceAnomalyDetector)
		}
	})
}

// TestWorkflowManualTriggerRoundTrip guards the workflow get→apply lifecycle for
// workflows that have no event or schedule trigger (manual trigger). Such a
// workflow serializes with no "trigger" field (Trigger is nil, omitempty), so
// detection must rely on the "tasks" field alone. Before the fix, detection
// required both "tasks" and "trigger", so `dtctl get workflow -o yaml|json`
// output for a manual workflow could not be re-applied: it was reported as an
// undetectable resource type. The bug affects both JSON and YAML output.
func TestWorkflowManualTriggerRoundTrip(t *testing.T) {
	// Mirror what `Get` produces for a manual-trigger workflow: no Trigger,
	// TriggerType derived as "Manual".
	wf := workflow.Workflow{
		ID:          "abc12345-6789-0abc-def0-123456789abc",
		Title:       "Manual Workflow",
		IsDeployed:  true,
		TriggerType: "Manual",
		Tasks: map[string]interface{}{
			"task1": map[string]interface{}{
				"name":   "task1",
				"action": "dynatrace.automations:run-javascript",
			},
		},
	}

	t.Run("JSON output is detected as workflow", func(t *testing.T) {
		data, err := json.Marshal(wf)
		if err != nil {
			t.Fatalf("json.Marshal: %v", err)
		}

		// Sanity: the wire shape must NOT contain a trigger field.
		var probe map[string]any
		if err := json.Unmarshal(data, &probe); err != nil {
			t.Fatalf("unmarshal probe: %v", err)
		}
		if _, hasTrigger := probe["trigger"]; hasTrigger {
			t.Fatal("fixture invalid: manual workflow output should not contain a 'trigger' field")
		}

		rt, isArray, err := detectResourceType(data)
		if err != nil {
			t.Fatalf("detectResourceType: %v\noutput was:\n%s", err, data)
		}
		if isArray {
			t.Error("expected single object, got array")
		}
		if rt != ResourceWorkflow {
			t.Errorf("ResourceType = %v, want %v", rt, ResourceWorkflow)
		}
	})

	t.Run("YAML output is detected as workflow", func(t *testing.T) {
		yamlData, err := yaml.Marshal(wf)
		if err != nil {
			t.Fatalf("yaml.Marshal: %v", err)
		}

		// Apply accepts YAML by converting to JSON first; emulate that path.
		var doc map[string]any
		if err := yaml.Unmarshal(yamlData, &doc); err != nil {
			t.Fatalf("yaml.Unmarshal: %v\noutput was:\n%s", err, yamlData)
		}
		jsonData, err := json.Marshal(doc)
		if err != nil {
			t.Fatalf("json.Marshal: %v", err)
		}

		rt, _, err := detectResourceType(jsonData)
		if err != nil {
			t.Fatalf("detectResourceType after YAML round-trip: %v\nyaml was:\n%s", err, yamlData)
		}
		if rt != ResourceWorkflow {
			t.Errorf("ResourceType = %v, want %v", rt, ResourceWorkflow)
		}
	})
}

func TestApplierWithStderrReceivesNotes(t *testing.T) {
	var buf bytes.Buffer
	a := (&Applier{}).WithStderr(&buf)

	var warnings []string
	a.stderrWarn(&warnings, "dropped %s", "field")

	if got := buf.String(); got != "Note: dropped field\n" {
		t.Errorf("note = %q, want it on the applier's own stderr", got)
	}
	if len(warnings) != 1 || warnings[0] != "dropped field" {
		t.Errorf("warnings = %v", warnings)
	}
}
