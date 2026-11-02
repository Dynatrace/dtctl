package resolver

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Dynatrace/dtctl/pkg/client"
	"github.com/Dynatrace/dtctl/pkg/resources/document"
	"github.com/Dynatrace/dtctl/pkg/resources/segment"
	"github.com/Dynatrace/dtctl/pkg/resources/workflow"
)

func TestNewResolver(t *testing.T) {
	c := &client.Client{}
	r := NewResolver(c)

	if r == nil {
		t.Fatal("NewResolver returned nil")
	}

	if r.client != c {
		t.Error("NewResolver did not set client correctly")
	}
}

func TestLooksLikeID(t *testing.T) {
	tests := []struct {
		name         string
		identifier   string
		resourceType ResourceType
		want         bool
	}{
		{
			name:         "workflow UUID with dashes",
			identifier:   "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee",
			resourceType: TypeWorkflow,
			want:         true,
		},
		{
			name:         "dashboard UUID with dashes",
			identifier:   "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee",
			resourceType: TypeDashboard,
			want:         true,
		},
		{
			name:         "notebook UUID with dashes",
			identifier:   "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee",
			resourceType: TypeNotebook,
			want:         true,
		},
		{
			name:         "workflow name without dashes",
			identifier:   "my-workflow",
			resourceType: TypeWorkflow,
			want:         false, // Too short
		},
		{
			name:         "dashboard name without UUID format",
			identifier:   "MyDashboard",
			resourceType: TypeDashboard,
			want:         false,
		},
		{
			name:         "short string with dash",
			identifier:   "abc-def",
			resourceType: TypeWorkflow,
			want:         false, // Not long enough
		},
		{
			name:         "long string without dashes",
			identifier:   "abcdefghijklmnopqrstuvwxyz",
			resourceType: TypeDashboard,
			want:         false, // No dashes
		},
		{
			name:         "empty string",
			identifier:   "",
			resourceType: TypeWorkflow,
			want:         false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := &Resolver{}
			got := r.looksLikeID(tt.identifier, tt.resourceType)

			if got != tt.want {
				t.Errorf("looksLikeID() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestResolveID_AlreadyID(t *testing.T) {
	// Mock server should not be called when identifier looks like an ID
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("Server should not be called when identifier looks like an ID")
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()

	c, err := client.NewForTesting(server.URL, "test-token")
	if err != nil {
		t.Fatalf("failed to create client: %v", err)
	}
	resolver := NewResolver(c)

	tests := []struct {
		name         string
		resourceType ResourceType
		identifier   string
	}{
		{
			name:         "workflow with UUID",
			resourceType: TypeWorkflow,
			identifier:   "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee",
		},
		{
			name:         "dashboard with UUID",
			resourceType: TypeDashboard,
			identifier:   "12345678-1234-1234-1234-123456789012",
		},
		{
			name:         "notebook with UUID",
			resourceType: TypeNotebook,
			identifier:   "notebook-id-with-dashes-long-enough",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			id, err := resolver.ResolveID(tt.resourceType, tt.identifier)

			if err != nil {
				t.Errorf("ResolveID() error = %v, want nil", err)
			}

			if id != tt.identifier {
				t.Errorf("ResolveID() = %v, want %v", id, tt.identifier)
			}
		})
	}
}

func TestResolveID_WorkflowByName_SingleMatch(t *testing.T) {
	workflowList := workflow.WorkflowList{
		Count: 1,
		Results: []workflow.Workflow{
			{
				ID:    "workflow-id-1",
				Title: "My Test Workflow",
			},
		},
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/platform/automation/v1/workflows" {
			t.Errorf("Unexpected path: %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(workflowList)
	}))
	defer server.Close()

	c, err := client.NewForTesting(server.URL, "test-token")
	if err != nil {
		t.Fatalf("failed to create client: %v", err)
	}
	resolver := NewResolver(c)

	id, err := resolver.ResolveID(TypeWorkflow, "Test")

	if err != nil {
		t.Errorf("ResolveID() error = %v, want nil", err)
	}

	if id != "workflow-id-1" {
		t.Errorf("ResolveID() = %v, want workflow-id-1", id)
	}
}

func TestResolveID_WorkflowByName_MultipleMatches(t *testing.T) {
	workflowList := workflow.WorkflowList{
		Count: 2,
		Results: []workflow.Workflow{
			{
				ID:    "workflow-id-1",
				Title: "Test Workflow 1",
			},
			{
				ID:    "workflow-id-2",
				Title: "Test Workflow 2",
			},
		},
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(workflowList)
	}))
	defer server.Close()

	c, err := client.NewForTesting(server.URL, "test-token")
	if err != nil {
		t.Fatalf("failed to create client: %v", err)
	}
	resolver := NewResolver(c)

	id, err := resolver.ResolveID(TypeWorkflow, "Test")

	if err == nil {
		t.Error("ResolveID() should return error for ambiguous name")
	}

	if id != "" {
		t.Errorf("ResolveID() should return empty string on error, got %v", id)
	}

	// Check error message contains both workflow IDs
	errMsg := err.Error()
	if !strings.Contains(errMsg, "ambiguous") {
		t.Errorf("Error should mention 'ambiguous', got: %v", errMsg)
	}
	if !strings.Contains(errMsg, "workflow-id-1") {
		t.Errorf("Error should list workflow-id-1, got: %v", errMsg)
	}
	if !strings.Contains(errMsg, "workflow-id-2") {
		t.Errorf("Error should list workflow-id-2, got: %v", errMsg)
	}
}

func TestResolveID_WorkflowByName_NoMatches(t *testing.T) {
	workflowList := workflow.WorkflowList{
		Count:   0,
		Results: []workflow.Workflow{},
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(workflowList)
	}))
	defer server.Close()

	c, err := client.NewForTesting(server.URL, "test-token")
	if err != nil {
		t.Fatalf("failed to create client: %v", err)
	}
	resolver := NewResolver(c)

	id, err := resolver.ResolveID(TypeWorkflow, "NonExistent")

	if err == nil {
		t.Error("ResolveID() should return error when no matches found")
	}

	if id != "" {
		t.Errorf("ResolveID() should return empty string on error, got %v", id)
	}

	errMsg := err.Error()
	if !strings.Contains(errMsg, "no workflow found") {
		t.Errorf("Error should mention no workflow found, got: %v", errMsg)
	}
}

func TestResolveID_DashboardByName_SingleMatch(t *testing.T) {
	docList := document.DocumentList{
		TotalCount: 1,
		Documents: []document.DocumentMetadata{
			{
				ID:   "dashboard-id-1",
				Name: "My Test Dashboard",
				Type: "dashboard",
				ModificationInfo: document.ModificationInfo{
					CreatedTime:      time.Now(),
					LastModifiedTime: time.Now(),
				},
			},
		},
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// "Test" is not a document ID, so the ID probe finds nothing.
		if r.URL.Path == "/platform/document/v1/documents/Test/metadata" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if r.URL.Path != "/platform/document/v1/documents" {
			t.Errorf("Unexpected path: %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
			return
		}

		// Check filter parameter
		filter := r.URL.Query().Get("filter")
		if !strings.Contains(filter, "type=='dashboard'") {
			t.Errorf("Expected filter to contain type=='dashboard', got: %s", filter)
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(docList)
	}))
	defer server.Close()

	c, err := client.NewForTesting(server.URL, "test-token")
	if err != nil {
		t.Fatalf("failed to create client: %v", err)
	}
	resolver := NewResolver(c)

	id, err := resolver.ResolveID(TypeDashboard, "Test")

	if err != nil {
		t.Errorf("ResolveID() error = %v, want nil", err)
	}

	if id != "dashboard-id-1" {
		t.Errorf("ResolveID() = %v, want dashboard-id-1", id)
	}
}

func TestResolveID_NotebookByName_SingleMatch(t *testing.T) {
	docList := document.DocumentList{
		TotalCount: 1,
		Documents: []document.DocumentMetadata{
			{
				ID:   "notebook-id-1",
				Name: "My Test Notebook",
				Type: "notebook",
				ModificationInfo: document.ModificationInfo{
					CreatedTime:      time.Now(),
					LastModifiedTime: time.Now(),
				},
			},
		},
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// "Test" is not a document ID, so the ID probe finds nothing.
		if r.URL.Path == "/platform/document/v1/documents/Test/metadata" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if r.URL.Path != "/platform/document/v1/documents" {
			t.Errorf("Unexpected path: %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
			return
		}

		// Check filter parameter
		filter := r.URL.Query().Get("filter")
		if !strings.Contains(filter, "type=='notebook'") {
			t.Errorf("Expected filter to contain type=='notebook', got: %s", filter)
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(docList)
	}))
	defer server.Close()

	c, err := client.NewForTesting(server.URL, "test-token")
	if err != nil {
		t.Fatalf("failed to create client: %v", err)
	}
	resolver := NewResolver(c)

	id, err := resolver.ResolveID(TypeNotebook, "Test")

	if err != nil {
		t.Errorf("ResolveID() error = %v, want nil", err)
	}

	if id != "notebook-id-1" {
		t.Errorf("ResolveID() = %v, want notebook-id-1", id)
	}
}

func TestResolveID_UnsupportedResourceType(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("Server should not be called for unsupported resource type")
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()

	c, err := client.NewForTesting(server.URL, "test-token")
	if err != nil {
		t.Fatalf("failed to create client: %v", err)
	}
	resolver := NewResolver(c)

	id, err := resolver.ResolveID(ResourceType("unsupported"), "test")

	if err == nil {
		t.Error("ResolveID() should return error for unsupported resource type")
	}

	if id != "" {
		t.Errorf("ResolveID() should return empty string on error, got %v", id)
	}

	errMsg := err.Error()
	if !strings.Contains(errMsg, "unsupported resource type") {
		t.Errorf("Error should mention unsupported resource type, got: %v", errMsg)
	}
}

func TestResolveID_APIError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte("Internal Server Error"))
	}))
	defer server.Close()

	c, err := client.NewForTesting(server.URL, "test-token")
	if err != nil {
		t.Fatalf("failed to create client: %v", err)
	}
	resolver := NewResolver(c)

	id, err := resolver.ResolveID(TypeWorkflow, "test")

	if err == nil {
		t.Error("ResolveID() should return error on API error")
	}

	if id != "" {
		t.Errorf("ResolveID() should return empty string on error, got %v", id)
	}
}

func TestSearchWorkflows_CaseInsensitiveMatch(t *testing.T) {
	workflowList := workflow.WorkflowList{
		Count: 3,
		Results: []workflow.Workflow{
			{
				ID:    "workflow-1",
				Title: "Production Workflow",
			},
			{
				ID:    "workflow-2",
				Title: "Test Workflow",
			},
			{
				ID:    "workflow-3",
				Title: "Development Workflow",
			},
		},
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(workflowList)
	}))
	defer server.Close()

	c, err := client.NewForTesting(server.URL, "test-token")
	if err != nil {
		t.Fatalf("failed to create client: %v", err)
	}
	resolver := NewResolver(c)

	// Search with lowercase - should match "Production Workflow"
	matches, err := resolver.searchWorkflows("production")

	if err != nil {
		t.Errorf("searchWorkflows() error = %v, want nil", err)
	}

	if len(matches) != 1 {
		t.Fatalf("searchWorkflows() returned %d matches, want 1", len(matches))
	}

	if matches[0].ID != "workflow-1" {
		t.Errorf("searchWorkflows() returned ID %v, want workflow-1", matches[0].ID)
	}

	if matches[0].Name != "Production Workflow" {
		t.Errorf("searchWorkflows() returned Name %v, want Production Workflow", matches[0].Name)
	}

	if matches[0].Type != TypeWorkflow {
		t.Errorf("searchWorkflows() returned Type %v, want %v", matches[0].Type, TypeWorkflow)
	}
}

func TestSearchWorkflows_PartialMatch(t *testing.T) {
	workflowList := workflow.WorkflowList{
		Count: 2,
		Results: []workflow.Workflow{
			{
				ID:    "workflow-1",
				Title: "Deploy to Production",
			},
			{
				ID:    "workflow-2",
				Title: "Deploy to Staging",
			},
		},
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(workflowList)
	}))
	defer server.Close()

	c, err := client.NewForTesting(server.URL, "test-token")
	if err != nil {
		t.Fatalf("failed to create client: %v", err)
	}
	resolver := NewResolver(c)

	// Search for "deploy" should match both
	matches, err := resolver.searchWorkflows("deploy")

	if err != nil {
		t.Errorf("searchWorkflows() error = %v, want nil", err)
	}

	if len(matches) != 2 {
		t.Fatalf("searchWorkflows() returned %d matches, want 2", len(matches))
	}
}

func TestSearchDocuments_Dashboard(t *testing.T) {
	docList := document.DocumentList{
		TotalCount: 1,
		Documents: []document.DocumentMetadata{
			{
				ID:   "dash-1",
				Name: "Production Dashboard",
				Type: "dashboard",
				ModificationInfo: document.ModificationInfo{
					CreatedTime:      time.Now(),
					LastModifiedTime: time.Now(),
				},
			},
		},
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		filter := r.URL.Query().Get("filter")
		if !strings.Contains(filter, "type=='dashboard'") {
			t.Errorf("Expected filter to contain type=='dashboard', got: %s", filter)
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(docList)
	}))
	defer server.Close()

	c, err := client.NewForTesting(server.URL, "test-token")
	if err != nil {
		t.Fatalf("failed to create client: %v", err)
	}
	resolver := NewResolver(c)

	matches, err := resolver.searchDocuments("production", "dashboard")

	if err != nil {
		t.Errorf("searchDocuments() error = %v, want nil", err)
	}

	if len(matches) != 1 {
		t.Fatalf("searchDocuments() returned %d matches, want 1", len(matches))
	}

	if matches[0].Type != TypeDashboard {
		t.Errorf("searchDocuments() returned Type %v, want %v", matches[0].Type, TypeDashboard)
	}
}

func TestSearchDocuments_Notebook(t *testing.T) {
	docList := document.DocumentList{
		TotalCount: 1,
		Documents: []document.DocumentMetadata{
			{
				ID:   "note-1",
				Name: "Analysis Notebook",
				Type: "notebook",
				ModificationInfo: document.ModificationInfo{
					CreatedTime:      time.Now(),
					LastModifiedTime: time.Now(),
				},
			},
		},
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		filter := r.URL.Query().Get("filter")
		if !strings.Contains(filter, "type=='notebook'") {
			t.Errorf("Expected filter to contain type=='notebook', got: %s", filter)
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(docList)
	}))
	defer server.Close()

	c, err := client.NewForTesting(server.URL, "test-token")
	if err != nil {
		t.Fatalf("failed to create client: %v", err)
	}
	resolver := NewResolver(c)

	matches, err := resolver.searchDocuments("analysis", "notebook")

	if err != nil {
		t.Errorf("searchDocuments() error = %v, want nil", err)
	}

	if len(matches) != 1 {
		t.Fatalf("searchDocuments() returned %d matches, want 1", len(matches))
	}

	if matches[0].Type != TypeNotebook {
		t.Errorf("searchDocuments() returned Type %v, want %v", matches[0].Type, TypeNotebook)
	}
}

func TestAmbiguousNameError(t *testing.T) {
	resolver := &Resolver{}

	matches := []Resource{
		{
			ID:   "id-1",
			Name: "Resource One",
			Type: TypeWorkflow,
		},
		{
			ID:   "id-2",
			Name: "Resource Two",
			Type: TypeWorkflow,
		},
		{
			ID:   "id-3",
			Name: "Resource Three",
			Type: TypeWorkflow,
		},
	}

	err := resolver.ambiguousNameError(TypeWorkflow, "Resource", matches)

	if err == nil {
		t.Fatal("ambiguousNameError() should return an error")
	}

	errMsg := err.Error()

	// Check error message contains key information
	if !strings.Contains(errMsg, "ambiguous") {
		t.Errorf("Error should contain 'ambiguous', got: %v", errMsg)
	}

	if !strings.Contains(errMsg, "workflow") {
		t.Errorf("Error should contain resource type 'workflow', got: %v", errMsg)
	}

	if !strings.Contains(errMsg, "Resource") {
		t.Errorf("Error should contain the name 'Resource', got: %v", errMsg)
	}

	// Check all three matches are listed
	for _, match := range matches {
		if !strings.Contains(errMsg, match.ID) {
			t.Errorf("Error should contain ID %s, got: %v", match.ID, errMsg)
		}
		if !strings.Contains(errMsg, match.Name) {
			t.Errorf("Error should contain Name %s, got: %v", match.Name, errMsg)
		}
	}

	// Check helpful message
	if !strings.Contains(errMsg, "use the exact ID") {
		t.Errorf("Error should contain helpful message about using exact ID, got: %v", errMsg)
	}
}

func TestLooksLikeID_SegmentNeverMatchesAsID(t *testing.T) {
	r := &Resolver{}

	// Segment UIDs are short alphanumeric strings — should never be treated as IDs
	tests := []struct {
		name       string
		identifier string
	}{
		{"short alphanumeric UID", "4lpVjcpcsjd"},
		{"another segment UID", "VLSFwcHqVlB"},
		{"UUID-like string", "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"},
		{"segment name", "My Segment"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if r.looksLikeID(tt.identifier, TypeSegment) {
				t.Errorf("looksLikeID() should return false for segments (identifier=%q), but returned true", tt.identifier)
			}
		})
	}
}

func newSegmentMockServer(t *testing.T, segments []segment.FilterSegment) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/platform/storage/filter-segments/v1/filter-segments" {
			t.Errorf("Unexpected path: %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(segment.FilterSegmentList{
			FilterSegments: segments,
			TotalCount:     len(segments),
		})
	}))
}

func TestResolveID_SegmentByUID(t *testing.T) {
	server := newSegmentMockServer(t, []segment.FilterSegment{
		{UID: "4lpVjcpcsjd", Name: "Astroshop - Small"},
		{UID: "WyAl8Sapu4Z", Name: "Astroshop"},
	})
	defer server.Close()

	c, err := client.NewForTesting(server.URL, "test-token")
	if err != nil {
		t.Fatalf("failed to create client: %v", err)
	}
	res := NewResolver(c)

	id, err := res.ResolveID(TypeSegment, "4lpVjcpcsjd")
	if err != nil {
		t.Fatalf("ResolveID() error = %v", err)
	}
	if id != "4lpVjcpcsjd" {
		t.Errorf("ResolveID() = %q, want %q", id, "4lpVjcpcsjd")
	}
}

func TestResolveID_SegmentByName_SingleMatch(t *testing.T) {
	server := newSegmentMockServer(t, []segment.FilterSegment{
		{UID: "4lpVjcpcsjd", Name: "Astroshop - Small"},
		{UID: "I1cTXkHjZSL", Name: "Astroshop - Full"},
		{UID: "ZixojzxNvKY", Name: "Stocks"},
	})
	defer server.Close()

	c, err := client.NewForTesting(server.URL, "test-token")
	if err != nil {
		t.Fatalf("failed to create client: %v", err)
	}
	res := NewResolver(c)

	id, err := res.ResolveID(TypeSegment, "Stocks")
	if err != nil {
		t.Fatalf("ResolveID() error = %v", err)
	}
	if id != "ZixojzxNvKY" {
		t.Errorf("ResolveID() = %q, want %q", id, "ZixojzxNvKY")
	}
}

func TestResolveID_SegmentByName_AmbiguousMatch(t *testing.T) {
	server := newSegmentMockServer(t, []segment.FilterSegment{
		{UID: "4lpVjcpcsjd", Name: "Astroshop - Small"},
		{UID: "I1cTXkHjZSL", Name: "Astroshop - Full"},
	})
	defer server.Close()

	c, err := client.NewForTesting(server.URL, "test-token")
	if err != nil {
		t.Fatalf("failed to create client: %v", err)
	}
	res := NewResolver(c)

	_, err = res.ResolveID(TypeSegment, "Astroshop")
	if err == nil {
		t.Fatal("ResolveID() should return error for ambiguous name")
	}
	if !strings.Contains(err.Error(), "ambiguous") {
		t.Errorf("error should mention 'ambiguous', got: %v", err)
	}
}

func TestResolveID_SegmentByName_NoMatch(t *testing.T) {
	server := newSegmentMockServer(t, []segment.FilterSegment{
		{UID: "4lpVjcpcsjd", Name: "Astroshop - Small"},
	})
	defer server.Close()

	c, err := client.NewForTesting(server.URL, "test-token")
	if err != nil {
		t.Fatalf("failed to create client: %v", err)
	}
	res := NewResolver(c)

	_, err = res.ResolveID(TypeSegment, "NonExistent")
	if err == nil {
		t.Fatal("ResolveID() should return error when no matches found")
	}
	if !strings.Contains(err.Error(), "no segment found") {
		t.Errorf("error should mention 'no segment found', got: %v", err)
	}
}

func TestResolveID_SegmentUID_PriorityOverName(t *testing.T) {
	// If a segment's UID matches the input exactly, it should be returned
	// even if another segment's name also contains the input as a substring.
	server := newSegmentMockServer(t, []segment.FilterSegment{
		{UID: "Stocks", Name: "Some Other Segment"},    // UID happens to look like a name
		{UID: "ZixojzxNvKY", Name: "Stocks Portfolio"}, // Name contains "Stocks"
	})
	defer server.Close()

	c, err := client.NewForTesting(server.URL, "test-token")
	if err != nil {
		t.Fatalf("failed to create client: %v", err)
	}
	res := NewResolver(c)

	id, err := res.ResolveID(TypeSegment, "Stocks")
	if err != nil {
		t.Fatalf("ResolveID() error = %v", err)
	}
	// Should match the exact UID, not the name substring
	if id != "Stocks" {
		t.Errorf("ResolveID() = %q, want %q (exact UID match should take priority)", id, "Stocks")
	}
}

// newDocumentIDMockServer serves document metadata for the IDs in byID and the
// documents in listed from the list endpoint. An ID absent from byID answers
// probeStatus (404 when zero). listCalls counts list requests.
func newDocumentIDMockServer(t *testing.T, byID map[string]document.DocumentMetadata, listed []document.DocumentMetadata, probeStatus int, listCalls *int) *httptest.Server {
	t.Helper()
	const prefix = "/platform/document/v1/documents/"
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/platform/document/v1/documents":
			if listCalls != nil {
				*listCalls++
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(document.DocumentList{TotalCount: len(listed), Documents: listed})
		case strings.HasPrefix(r.URL.Path, prefix) && strings.HasSuffix(r.URL.Path, "/metadata"):
			id := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, prefix), "/metadata")
			meta, ok := byID[id]
			if !ok {
				status := probeStatus
				if status == 0 {
					status = http.StatusNotFound
				}
				w.WriteHeader(status)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(meta)
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusInternalServerError)
		}
	}))
}

func newTestResolver(t *testing.T, serverURL string) *Resolver {
	t.Helper()
	c, err := client.NewForTesting(serverURL, "test-token")
	if err != nil {
		t.Fatalf("failed to create client: %v", err)
	}
	return NewResolver(c)
}

// A caller-chosen, non-UUID document ID (a slug) resolves as an ID, the way
// `get document <slug>` fetches it, without a name search.
func TestResolveID_DocumentBySlugID(t *testing.T) {
	listCalls := 0
	server := newDocumentIDMockServer(t,
		map[string]document.DocumentMetadata{
			"team-launchpad": {ID: "team-launchpad", Name: "Team Launchpad", Type: "launchpad"},
		}, nil, 0, &listCalls)
	defer server.Close()

	id, err := newTestResolver(t, server.URL).ResolveID(TypeDocument, "team-launchpad")
	if err != nil {
		t.Fatalf("ResolveID() error = %v", err)
	}
	if id != "team-launchpad" {
		t.Errorf("ResolveID() = %q, want %q", id, "team-launchpad")
	}
	if listCalls != 0 {
		t.Errorf("name search ran %d times, want 0 for an existing ID", listCalls)
	}
}

// An argument that is one document's ID and another document's name resolves
// to the ID, deterministically. Delete relies on this: it must never act on a
// different document than the one whose exact ID was given.
func TestResolveID_DocumentIDWinsOverOtherDocumentsName(t *testing.T) {
	server := newDocumentIDMockServer(t,
		map[string]document.DocumentMetadata{
			"team-launchpad": {ID: "team-launchpad", Name: "Team Launchpad", Type: "launchpad"},
		},
		[]document.DocumentMetadata{
			{ID: "other-doc-id", Name: "team-launchpad", Type: "dashboard"},
		}, 0, nil)
	defer server.Close()

	id, err := newTestResolver(t, server.URL).ResolveID(TypeDocument, "team-launchpad")
	if err != nil {
		t.Fatalf("ResolveID() error = %v", err)
	}
	if id != "team-launchpad" {
		t.Errorf("ResolveID() = %q, want the exact ID %q", id, "team-launchpad")
	}
}

// When the argument is not a document ID (404, or 400 for a string the API
// rejects as an ID), resolution falls back to the name search.
func TestResolveID_DocumentFallsBackToName(t *testing.T) {
	for _, status := range []int{http.StatusNotFound, http.StatusBadRequest} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			server := newDocumentIDMockServer(t, nil,
				[]document.DocumentMetadata{
					{ID: "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee", Name: "Team Launchpad", Type: "launchpad"},
				}, status, nil)
			defer server.Close()

			id, err := newTestResolver(t, server.URL).ResolveID(TypeDocument, "Team Launchpad")
			if err != nil {
				t.Fatalf("ResolveID() error = %v", err)
			}
			if id != "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee" {
				t.Errorf("ResolveID() = %q, want the name match", id)
			}
		})
	}
}

// Neither an ID nor a name: the error still names the argument.
func TestResolveID_DocumentNeitherIDNorName(t *testing.T) {
	server := newDocumentIDMockServer(t, nil, nil, 0, nil)
	defer server.Close()

	_, err := newTestResolver(t, server.URL).ResolveID(TypeDocument, "missing-slug")
	if err == nil || !strings.Contains(err.Error(), `no document found with name "missing-slug"`) {
		t.Errorf("ResolveID() error = %v, want no-document-found error", err)
	}
}

// A probe failure other than "no such ID" is returned, not masked by a name
// search that could select a different document.
func TestResolveID_DocumentProbeErrorIsReturned(t *testing.T) {
	listCalls := 0
	server := newDocumentIDMockServer(t, nil,
		[]document.DocumentMetadata{
			{ID: "other-doc-id", Name: "team-launchpad", Type: "launchpad"},
		}, http.StatusForbidden, &listCalls)
	defer server.Close()

	id, err := newTestResolver(t, server.URL).ResolveID(TypeDocument, "team-launchpad")
	if err == nil {
		t.Fatalf("ResolveID() = %q, want an error for a forbidden ID probe", id)
	}
	if listCalls != 0 {
		t.Errorf("name search ran %d times after a forbidden ID probe, want 0", listCalls)
	}
}

// Typed resolution only accepts an ID of its own document type, so
// `delete dashboard <id>` cannot resolve to a notebook with that ID.
func TestResolveID_TypedDocumentIDMustMatchType(t *testing.T) {
	byID := map[string]document.DocumentMetadata{
		"ops-notes": {ID: "ops-notes", Name: "Ops Notes", Type: "notebook"},
	}

	t.Run("matching type resolves as ID", func(t *testing.T) {
		server := newDocumentIDMockServer(t, byID, nil, 0, nil)
		defer server.Close()
		id, err := newTestResolver(t, server.URL).ResolveID(TypeNotebook, "ops-notes")
		if err != nil || id != "ops-notes" {
			t.Errorf("ResolveID(notebook) = %q, %v; want %q", id, err, "ops-notes")
		}
	})

	t.Run("other type falls back to name", func(t *testing.T) {
		server := newDocumentIDMockServer(t, byID, nil, 0, nil)
		defer server.Close()
		id, err := newTestResolver(t, server.URL).ResolveID(TypeDashboard, "ops-notes")
		if err == nil {
			t.Fatalf("ResolveID(dashboard) = %q, want no-match error for a notebook ID", id)
		}
		if !strings.Contains(err.Error(), "no dashboard found") {
			t.Errorf("error = %v, want no dashboard found", err)
		}
	})
}
