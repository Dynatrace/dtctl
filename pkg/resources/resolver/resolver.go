package resolver

import (
	"errors"
	"fmt"
	"strings"

	"github.com/dynatrace-oss/dtctl/pkg/client"
	"github.com/dynatrace-oss/dtctl/pkg/resources/document"
	"github.com/dynatrace-oss/dtctl/pkg/resources/segment"
	"github.com/dynatrace-oss/dtctl/pkg/resources/workflow"
	"github.com/dynatrace-oss/dtctl/sdk/httpclient"
)

// Resolver resolves resource names to IDs
type Resolver struct {
	client              *client.Client
	workflowAdminAccess bool
}

// NewResolver creates a new resolver
func NewResolver(c *client.Client) *Resolver {
	return &Resolver{client: c}
}

// WithWorkflowAdminAccess makes workflow name lookups search every workflow in
// the environment (adminAccess=true), so a name resolves to a workflow the
// caller does not own when the command itself runs with --admin-access.
func (r *Resolver) WithWorkflowAdminAccess(on bool) *Resolver {
	c := *r
	c.workflowAdminAccess = on
	return &c
}

// ResourceType represents the type of resource to resolve
type ResourceType string

const (
	TypeWorkflow  ResourceType = "workflow"
	TypeDashboard ResourceType = "dashboard"
	TypeNotebook  ResourceType = "notebook"
	TypeDocument  ResourceType = "document" // generic, searches all document types
	TypeSegment   ResourceType = "segment"
)

// ResolveID resolves a name or ID to a resource ID
// If identifier looks like an ID, returns it directly
// For document-backed types, an identifier that is an existing document's ID
// resolves to that ID even when it does not look like a UUID (see
// resolveDocumentID)
// If it's a name, searches for matching resources
// Returns error if multiple matches found (ambiguous)
func (r *Resolver) ResolveID(resourceType ResourceType, identifier string) (string, error) {
	// If identifier looks like an ID, return it directly
	if r.looksLikeID(identifier, resourceType) {
		return identifier, nil
	}

	// Documents may carry a caller-chosen, non-UUID ID (`create document --id
	// my-slug`). `get document <id>` fetches such an ID directly, so every other
	// verb must accept it too: try it as an ID first and only fall back to a name
	// search when no such document exists. An exact ID therefore always wins over
	// a different document whose name happens to match, which matters for delete.
	if isDocumentType(resourceType) {
		id, found, err := r.resolveDocumentID(resourceType, identifier)
		if err != nil {
			return "", err
		}
		if found {
			return id, nil
		}
	}

	// Search for resources by name
	matches, err := r.searchByName(resourceType, identifier)
	if err != nil {
		return "", err
	}

	if len(matches) == 0 {
		return "", fmt.Errorf("no %s found with name %q", resourceType, identifier)
	}

	if len(matches) == 1 {
		return matches[0].ID, nil
	}

	// Multiple matches - return error with suggestions
	return "", r.ambiguousNameError(resourceType, identifier, matches)
}

// looksLikeID checks if a string looks like a resource ID
func (r *Resolver) looksLikeID(str string, resourceType ResourceType) bool {
	// Segments use short alphanumeric UIDs (e.g. "4lpVjcpcsjd") that are
	// indistinguishable from names by format alone. The resolver handles
	// segments by checking both UID and name matches in searchSegments.
	if resourceType == TypeSegment {
		return false
	}

	// Other supported resource types use UUIDs (with dashes)
	if resourceType == TypeDashboard || resourceType == TypeNotebook ||
		resourceType == TypeWorkflow || resourceType == TypeDocument {
		// Simple heuristic: contains dashes and is long enough
		return strings.Contains(str, "-") && len(str) > 20
	}

	return false
}

// isDocumentType reports whether resourceType is backed by the Document API.
func isDocumentType(resourceType ResourceType) bool {
	return resourceType == TypeDocument || resourceType == TypeDashboard || resourceType == TypeNotebook
}

// resolveDocumentID looks identifier up as a document ID. found is false when no
// document with that ID exists (HTTP 404, or 400 for a string the API does not
// accept as an ID, e.g. a name with spaces), so the caller falls back to a name
// search. For TypeDashboard/TypeNotebook a document of another type is not a
// match either, so `delete dashboard <id>` cannot reach a notebook that owns
// that ID. Any other failure (auth, forbidden, server) is returned rather than
// masked by a name search that could pick a different document.
func (r *Resolver) resolveDocumentID(resourceType ResourceType, identifier string) (id string, found bool, err error) {
	if identifier == "" {
		return "", false, nil
	}
	metadata, err := document.NewHandler(r.client).GetMetadata(identifier)
	if err != nil {
		if errors.Is(err, httpclient.ErrNotFound) || errors.Is(err, httpclient.ErrBadRequest) {
			return "", false, nil
		}
		return "", false, err
	}
	if resourceType != TypeDocument && metadata.Type != string(resourceType) {
		return "", false, nil
	}
	return identifier, true, nil
}

// Resource represents a found resource
type Resource struct {
	ID   string
	Name string
	Type ResourceType
}

// searchByName searches for resources by name
func (r *Resolver) searchByName(resourceType ResourceType, name string) ([]Resource, error) {
	switch resourceType {
	case TypeWorkflow:
		return r.searchWorkflows(name)
	case TypeDashboard:
		return r.searchDashboards(name)
	case TypeNotebook:
		return r.searchNotebooks(name)
	case TypeDocument:
		return r.searchAllDocuments(name)
	case TypeSegment:
		return r.searchSegments(name)
	default:
		return nil, fmt.Errorf("unsupported resource type: %s", resourceType)
	}
}

// searchWorkflows searches for workflows by name
func (r *Resolver) searchWorkflows(name string) ([]Resource, error) {
	handler := workflow.NewHandler(r.client).WithAdminAccess(r.workflowAdminAccess)
	list, err := handler.List(workflow.WorkflowFilters{}, 500, 0)
	if err != nil {
		return nil, err
	}

	var matches []Resource
	nameLower := strings.ToLower(name)

	for _, wf := range list.Results {
		if strings.Contains(strings.ToLower(wf.Title), nameLower) {
			matches = append(matches, Resource{
				ID:   wf.ID,
				Name: wf.Title,
				Type: TypeWorkflow,
			})
		}
	}

	return matches, nil
}

// searchSegments searches for segments by UID or name.
// Segment UIDs are short alphanumeric strings (e.g. "4lpVjcpcsjd") that
// cannot be distinguished from names by format, so we check both.
// An exact UID match takes priority and is returned immediately.
func (r *Resolver) searchSegments(name string) ([]Resource, error) {
	handler := segment.NewHandler(r.client)
	list, err := handler.List()
	if err != nil {
		return nil, err
	}

	// First pass: check for exact UID match
	for _, seg := range list.FilterSegments {
		if seg.UID == name {
			return []Resource{{
				ID:   seg.UID,
				Name: seg.Name,
				Type: TypeSegment,
			}}, nil
		}
	}

	// Second pass: search by name (case-insensitive substring)
	var matches []Resource
	nameLower := strings.ToLower(name)

	for _, seg := range list.FilterSegments {
		if strings.Contains(strings.ToLower(seg.Name), nameLower) {
			matches = append(matches, Resource{
				ID:   seg.UID,
				Name: seg.Name,
				Type: TypeSegment,
			})
		}
	}

	return matches, nil
}

// searchDashboards searches for dashboards by name
func (r *Resolver) searchDashboards(name string) ([]Resource, error) {
	return r.searchDocuments(name, "dashboard")
}

// searchNotebooks searches for notebooks by name
func (r *Resolver) searchNotebooks(name string) ([]Resource, error) {
	return r.searchDocuments(name, "notebook")
}

// searchDocuments searches for documents by name and type
func (r *Resolver) searchDocuments(name, docType string) ([]Resource, error) {
	handler := document.NewHandler(r.client)

	filters := document.DocumentFilters{
		Type: docType,
		Name: name,
	}

	list, err := handler.List(filters)
	if err != nil {
		return nil, err
	}

	var matches []Resource
	nameLower := strings.ToLower(name)

	for _, doc := range list.Documents {
		if strings.Contains(strings.ToLower(doc.Name), nameLower) {
			resourceType := TypeDashboard
			if docType == "notebook" {
				resourceType = TypeNotebook
			}

			matches = append(matches, Resource{
				ID:   doc.ID,
				Name: doc.Name,
				Type: resourceType,
			})
		}
	}

	return matches, nil
}

// searchAllDocuments searches for documents across all types by name
func (r *Resolver) searchAllDocuments(name string) ([]Resource, error) {
	handler := document.NewHandler(r.client)

	// No type filter - search all document types
	filters := document.DocumentFilters{
		Name: name,
	}

	list, err := handler.List(filters)
	if err != nil {
		return nil, err
	}

	var matches []Resource
	nameLower := strings.ToLower(name)

	for _, doc := range list.Documents {
		if strings.Contains(strings.ToLower(doc.Name), nameLower) {
			// Store the actual document type (e.g. "dashboard", "launchpad") so the
			// disambiguation prompt can show it. We wrap it as a ResourceType string.
			matches = append(matches, Resource{
				ID:   doc.ID,
				Name: doc.Name,
				Type: ResourceType(doc.Type),
			})
		}
	}

	return matches, nil
}

// ambiguousNameError creates an error message for ambiguous names
func (r *Resolver) ambiguousNameError(resourceType ResourceType, name string, matches []Resource) error {
	msg := fmt.Sprintf("ambiguous %s name %q - multiple matches found:\n", resourceType, name)

	for i, match := range matches {
		if resourceType == TypeDocument {
			// For generic documents, show the actual document type to help users distinguish
			msg += fmt.Sprintf("  %d. %s (type: %s, ID: %s)\n", i+1, match.Name, match.Type, match.ID)
		} else {
			msg += fmt.Sprintf("  %d. %s (ID: %s)\n", i+1, match.Name, match.ID)
		}
	}

	msg += "\nPlease use the exact ID to specify which resource you want."

	return fmt.Errorf("%s", msg)
}
