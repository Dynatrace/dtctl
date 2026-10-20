package document

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/go-resty/resty/v2"

	"github.com/dynatrace-oss/dtctl/sdk/httpclient"
)

// parseFlexibleInt parses a JSON value that may be either a number or a
// quoted-string number (e.g. 1 or "1"). Some API versions return version
// fields as strings instead of integers.
func parseFlexibleInt(raw json.RawMessage) (int, error) {
	if len(raw) == 0 {
		return 0, nil
	}

	// Try as int first (most common case)
	var n int
	if err := json.Unmarshal(raw, &n); err == nil {
		return n, nil
	}

	// Fall back to quoted string
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return 0, fmt.Errorf("version field is neither a number nor a string: %s", string(raw))
	}
	return strconv.Atoi(s)
}

// escapeFilterValue escapes single quotes in filter values to prevent
// filter expression injection.
func escapeFilterValue(s string) string {
	return strings.ReplaceAll(s, "'", "\\'")
}

// Handler handles document resources (dashboards, notebooks, etc.)
type Handler struct {
	client *httpclient.Client
}

// NewHandler creates a new document handler
func NewHandler(c *httpclient.Client) *Handler {
	return &Handler{client: c}
}

// Document represents a document resource.
//
// The trailing optional fields (OriginAppID, OriginExtensionID, Labels,
// ShareInfo, UserContext) are populated only when requested via
// DocumentFilters.AddFields. They are tagged `omitempty` so default
// JSON/YAML output stays minimal.
type Document struct {
	ID          string    `json:"id"`
	Name        string    `json:"name"`
	Type        string    `json:"type"`
	Owner       string    `json:"owner"`
	IsPrivate   bool      `json:"isPrivate"`
	Created     time.Time `json:"-"`
	Description string    `json:"description,omitempty"`
	Version     int       `json:"version"`
	Modified    time.Time `json:"-"`
	Content     []byte    `json:"-"`

	OriginAppID       string       `json:"originAppId,omitempty" yaml:"originAppId,omitempty"`
	OriginExtensionID string       `json:"originExtensionId,omitempty" yaml:"originExtensionId,omitempty"`
	Labels            []string     `json:"labels,omitempty" yaml:"labels,omitempty"`
	ShareInfo         *ShareInfo   `json:"shareInfo,omitempty" yaml:"shareInfo,omitempty"`
	UserContext       *UserContext `json:"userContext,omitempty" yaml:"userContext,omitempty"`
}

// DocumentMetadata represents detailed document metadata.
// Fields after Access are only populated when requested via DocumentFilters.AddFields.
type DocumentMetadata struct {
	ID                string           `json:"id"`
	Name              string           `json:"name"`
	Type              string           `json:"type"`
	Description       string           `json:"description,omitempty"`
	Version           int              `json:"version"`
	Owner             string           `json:"owner"`
	IsPrivate         bool             `json:"isPrivate"`
	ModificationInfo  ModificationInfo `json:"modificationInfo"`
	Access            []string         `json:"access,omitempty"`
	OriginAppID       string           `json:"originAppId,omitempty" yaml:"originAppId,omitempty"`
	OriginExtensionID string           `json:"originExtensionId,omitempty" yaml:"originExtensionId,omitempty"`
	Labels            []string         `json:"labels,omitempty" yaml:"labels,omitempty"`
	ShareInfo         *ShareInfo       `json:"shareInfo,omitempty" yaml:"shareInfo,omitempty"`
	UserContext       *UserContext     `json:"userContext,omitempty" yaml:"userContext,omitempty"`
}

// ShareInfo describes share state for a document.
type ShareInfo struct {
	IsShared                bool `json:"isShared" yaml:"isShared"`
	IsSharedWithCurrentUser bool `json:"isSharedWithCurrentUser,omitempty" yaml:"isSharedWithCurrentUser,omitempty"`
}

// UserContext describes per-user metadata for a document.
type UserContext struct {
	LastAccessedTime time.Time `json:"lastAccessedTime" yaml:"lastAccessedTime"`
}

// UnmarshalJSON custom unmarshaler for DocumentMetadata to handle version as string or int.
func (m *DocumentMetadata) UnmarshalJSON(data []byte) error {
	type Alias DocumentMetadata
	aux := &struct {
		Version json.RawMessage `json:"version"`
		*Alias
	}{
		Alias: (*Alias)(m),
	}
	if err := json.Unmarshal(data, aux); err != nil {
		return err
	}
	if len(aux.Version) > 0 {
		v, err := parseFlexibleInt(aux.Version)
		if err != nil {
			return fmt.Errorf("invalid version: %w", err)
		}
		m.Version = v
	}
	return nil
}

// ModificationInfo contains creation and modification timestamps
type ModificationInfo struct {
	CreatedBy        string    `json:"createdBy"`
	CreatedTime      time.Time `json:"createdTime"`
	LastModifiedBy   string    `json:"lastModifiedBy"`
	LastModifiedTime time.Time `json:"lastModifiedTime"`
}

// DocumentList represents a list of documents
type DocumentList struct {
	Documents   []DocumentMetadata `json:"documents"`
	TotalCount  int                `json:"totalCount"`
	NextPageKey string             `json:"nextPageKey,omitempty"`
}

// DocumentFilters contains filter options for listing documents.
// When Filter is non-empty it is sent verbatim and overrides Type/Name/Owner.
type DocumentFilters struct {
	Type        string   // e.g., "dashboard", "notebook"
	Name        string   // Filter by name
	Owner       string   // Filter by owner ID
	Filter      string   // Raw filter string, sent verbatim (overrides Type/Name/Owner)
	ChunkSize   int64    // Page size for pagination (0 = no chunking, use API default)
	Sort        string   // Sort fields, comma-separated, prefix with "-" for descending
	AddFields   []string // Fields the API omits by default (e.g. "originExtensionId", "labels")
	AdminAccess bool     // List as effective owner; requires document:documents:admin
}

// List retrieves documents matching the provided filters with automatic pagination
func (h *Handler) List(ctx context.Context, filters DocumentFilters) (*DocumentList, error) {
	var allDocuments []DocumentMetadata
	var totalCount int
	nextPageKey := ""

	// Build filter query parameter
	var filterStr string
	if filters.Filter != "" {
		filterStr = filters.Filter
	} else {
		var conditions []string
		if filters.Type != "" {
			conditions = append(conditions, fmt.Sprintf("type=='%s'", escapeFilterValue(filters.Type)))
		}
		if filters.Name != "" {
			conditions = append(conditions, fmt.Sprintf("name contains '%s'", escapeFilterValue(filters.Name)))
		}
		if filters.Owner != "" {
			conditions = append(conditions, fmt.Sprintf("owner=='%s'", escapeFilterValue(filters.Owner)))
		}
		if len(conditions) > 0 {
			filterStr = strings.Join(conditions, " and ")
		}
	}

	queryFilters := map[string]string{}
	if filterStr != "" {
		queryFilters["filter"] = filterStr
	}
	if filters.Sort != "" {
		queryFilters["sort"] = filters.Sort
	}
	if len(filters.AddFields) > 0 {
		queryFilters["add-fields"] = strings.Join(filters.AddFields, ",")
	}
	if filters.AdminAccess {
		queryFilters["admin-access"] = "true"
	}

	for {
		var result DocumentList
		req := h.client.HTTP().R().SetContext(ctx)

		req.SetQueryParamsFromValues(httpclient.PaginationParams{
			Style:         httpclient.PaginationDocumentAPI,
			PageKeyParam:  "page-key",
			PageSizeParam: "page-size",
			NextPageKey:   nextPageKey,
			PageSize:      int64(filters.ChunkSize),
			Filters:       queryFilters,
		}.QueryParams())

		resp, err := req.Get("/platform/document/v1/documents")
		if err != nil {
			return nil, fmt.Errorf("failed to list documents: %w", err)
		}

		if err := httpclient.CheckResponse(resp); err != nil {
			return nil, fmt.Errorf("failed to list documents: %w", err)
		}

		if err := json.Unmarshal(resp.Body(), &result); err != nil {
			return nil, fmt.Errorf("list documents: parse response: %w", err)
		}

		allDocuments = append(allDocuments, result.Documents...)
		totalCount = result.TotalCount

		// If chunking is disabled (ChunkSize == 0), return first page only
		if filters.ChunkSize == 0 {
			return &result, nil
		}

		// Check if there are more pages
		if result.NextPageKey == "" {
			break
		}
		nextPageKey = result.NextPageKey
	}

	return &DocumentList{
		Documents:  allDocuments,
		TotalCount: totalCount,
	}, nil
}

// Get retrieves a specific document by ID
func (h *Handler) Get(ctx context.Context, id string) (*Document, error) {
	resp, err := h.client.HTTP().R().SetContext(ctx).
		Get(fmt.Sprintf("/platform/document/v1/documents/%s", httpclient.PathSegment(id)))

	if err != nil {
		return nil, fmt.Errorf("failed to get document: %w", err)
	}

	if err := httpclient.CheckResponse(resp); err != nil {
		return nil, fmt.Errorf("failed to get document %q: %w", id, err)
	}

	// Parse multipart response
	doc, err := ParseMultipartDocument(resp)
	if err != nil {
		return nil, fmt.Errorf("failed to parse document response: %w", err)
	}

	return doc, nil
}

// GetMetadata retrieves only the metadata for a document
func (h *Handler) GetMetadata(ctx context.Context, id string) (*DocumentMetadata, error) {
	resp, err := h.client.HTTP().R().SetContext(ctx).
		Get(fmt.Sprintf("/platform/document/v1/documents/%s/metadata", httpclient.PathSegment(id)))

	if err != nil {
		return nil, fmt.Errorf("failed to get document metadata: %w", err)
	}

	if err := httpclient.CheckResponse(resp); err != nil {
		return nil, fmt.Errorf("failed to get document metadata %q: %w", id, err)
	}

	var result DocumentMetadata
	if err := json.Unmarshal(resp.Body(), &result); err != nil {
		return nil, fmt.Errorf("get document metadata: parse response: %w", err)
	}

	return &result, nil
}

// Delete deletes a document
func (h *Handler) Delete(ctx context.Context, id string, version int) error {
	resp, err := h.client.HTTP().R().SetContext(ctx).
		SetQueryParam("optimistic-locking-version", fmt.Sprintf("%d", version)).
		Delete(fmt.Sprintf("/platform/document/v1/documents/%s", httpclient.PathSegment(id)))

	if err != nil {
		return fmt.Errorf("failed to delete document: %w", err)
	}

	if err := httpclient.CheckResponse(resp); err != nil {
		return fmt.Errorf("failed to delete document %q: %w", id, err)
	}

	return nil
}

// CreateRequest contains the data needed to create a new document
type CreateRequest struct {
	ID          string   // Optional - if not provided, system generates one
	Name        string   // Required
	Type        string   // Required - e.g., "dashboard", "notebook"
	Description string   // Optional
	Content     []byte   // Required - the document content
	Labels      []string // Optional - classification labels stored in document metadata
}

// addLabelParts appends each label as its own "labels" multipart form field.
// The Documents API models labels on write as a repeated form field — one part
// per label, value taken verbatim — the symmetric counterpart to the JSON array
// returned on read. Providing labels replaces the document's full label set;
// omitting them (nil/empty slice) leaves existing labels untouched.
//
// SetMultipartField (rather than FormData.Add) is used so that a labels-only
// update still switches the request into multipart mode.
func addLabelParts(r *resty.Request, labels []string) {
	for _, l := range labels {
		r.SetMultipartField("labels", "", "", strings.NewReader(l))
	}
}

// Create creates a new document
func (h *Handler) Create(ctx context.Context, req CreateRequest) (*Document, error) {
	if req.Name == "" {
		return nil, fmt.Errorf("document name is required")
	}
	if req.Type == "" {
		return nil, fmt.Errorf("document type is required")
	}
	if len(req.Content) == 0 {
		return nil, fmt.Errorf("document content is required")
	}

	// Build multipart form request
	// The API expects multipart/form-data with content as a file part
	r := h.client.HTTP().R().SetContext(ctx).
		SetMultipartFormData(map[string]string{
			"name": req.Name,
			"type": req.Type,
		}).
		SetMultipartField("content", "content.json", "application/json", bytes.NewReader(req.Content))

	if req.ID != "" {
		r.SetMultipartFormData(map[string]string{"id": req.ID})
	}
	if req.Description != "" {
		r.SetMultipartFormData(map[string]string{"description": req.Description})
	}

	resp, err := r.Post("/platform/document/v1/documents")

	if err != nil {
		return nil, fmt.Errorf("failed to create document: %w", err)
	}

	if err := httpclient.CheckResponse(resp); err != nil {
		return nil, fmt.Errorf("failed to create document: %w", err)
	}

	// Parse the response - determine fallback ID
	fallbackID := req.ID
	if fallbackID == "" {
		fallbackID = extractIDFromResponse(resp.Body())
	}

	doc, err := parseUpdateResponse(resp, fallbackID, 0, req.Name)
	if err != nil {
		return nil, err
	}
	if doc.Name == "" {
		doc.Name = req.Name
	}
	if doc.Type == "" {
		doc.Type = req.Type
	}

	// The create endpoint does not accept labels, so apply them with a follow-up
	// update once the document (and its version) exist. This is not atomic: on
	// failure the document is already created, so the error names it explicitly.
	if len(req.Labels) > 0 {
		labeled, err := h.UpdateDocument(ctx, doc.ID, doc.Version, UpdateRequest{Labels: req.Labels})
		if err != nil {
			return doc, fmt.Errorf("document %q created but setting labels failed: %w", doc.ID, err)
		}
		return labeled, nil
	}

	return doc, nil
}

// UpdateRequest contains the (all optional) fields that can be changed on an
// existing document via updateDocument. Any zero-valued field is omitted from
// the request and the API leaves the corresponding attribute unchanged. A
// non-empty Labels slice replaces the document's entire label set; an empty or
// nil slice sends no labels, which the API also treats as "no change" (this
// endpoint offers no way to clear labels).
type UpdateRequest struct {
	Content     []byte   // Document content; omitted when empty (content unchanged)
	ContentType string   // Content-Type for Content; defaults to application/json
	Name        string   // New name; omitted when empty
	Description string   // New description; omitted when empty
	Labels      []string // Replacement label set; omitted when empty

	// CreateSnapshot asks the API to snapshot the document's current state
	// before applying this update, so the pre-update content stays retrievable
	// via ListSnapshots / RestoreSnapshot. Defaults to false: updates overwrite
	// without keeping history unless this is set.
	//
	// The API caps snapshot creation at 5 per document per 60 seconds and keeps
	// at most 50 snapshots per document (creating the 51st deletes the oldest).
	CreateSnapshot bool

	// SnapshotDescription labels the snapshot created by CreateSnapshot (max
	// 128 characters). Ignored when CreateSnapshot is false.
	SnapshotDescription string
}

// Update updates a document's content.
func (h *Handler) Update(ctx context.Context, id string, version int, content []byte, contentType string) (*Document, error) {
	return h.UpdateDocument(ctx, id, version, UpdateRequest{Content: content, ContentType: contentType})
}

// UpdateWithMetadata updates a document's content and optionally its metadata (name, description).
func (h *Handler) UpdateWithMetadata(ctx context.Context, id string, version int, content []byte, contentType string, name string, description string) (*Document, error) {
	return h.UpdateDocument(ctx, id, version, UpdateRequest{
		Content:     content,
		ContentType: contentType,
		Name:        name,
		Description: description,
	})
}

// UpdateDocument performs a partial update of a document via the updateDocument
// endpoint. It is the general form behind Update / UpdateWithMetadata and is the
// only write path that can set labels (the create endpoint cannot).
func (h *Handler) UpdateDocument(ctx context.Context, id string, version int, req UpdateRequest) (*Document, error) {
	r := h.client.HTTP().R().SetContext(ctx).
		SetQueryParam("optimistic-locking-version", fmt.Sprintf("%d", version))

	if len(req.Content) > 0 {
		contentType := req.ContentType
		if contentType == "" {
			contentType = "application/json"
		}
		r.SetMultipartField("content", "content", contentType, bytes.NewReader(req.Content))
	}
	if req.Name != "" {
		r.SetMultipartFormData(map[string]string{"name": req.Name})
	}
	if req.Description != "" {
		r.SetMultipartFormData(map[string]string{"description": req.Description})
	}
	addLabelParts(r, req.Labels)

	// snapshotDescription is only meaningful alongside create-snapshot; the API
	// ignores it otherwise, so don't send it and keep the request minimal.
	if req.CreateSnapshot {
		r.SetQueryParam("create-snapshot", "true")
		if req.SnapshotDescription != "" {
			r.SetMultipartFormData(map[string]string{"snapshotDescription": req.SnapshotDescription})
		}
	}

	resp, err := r.Patch(fmt.Sprintf("/platform/document/v1/documents/%s", httpclient.PathSegment(id)))
	if err != nil {
		return nil, fmt.Errorf("failed to update document: %w", err)
	}

	if err := httpclient.CheckResponse(resp); err != nil {
		return nil, fmt.Errorf("failed to update document %q: %w", id, err)
	}

	return parseUpdateResponse(resp, id, version+1, req.Name)
}

// parseUpdateResponse parses an update/create response that may be either
// multipart or JSON. For JSON responses it expects the documentMetadata wrapper.
// On parse failure it returns a minimal Document with what we know, plus the
// given fallback fields.
func parseUpdateResponse(resp *resty.Response, fallbackID string, fallbackVersion int, fallbackName string) (*Document, error) {
	respContentType := resp.Header().Get("Content-Type")

	if strings.HasPrefix(respContentType, "multipart/") {
		doc, err := ParseMultipartDocument(resp)
		if err != nil {
			return documentFallback(fallbackID, fallbackVersion, fallbackName), nil
		}
		return doc, nil
	}

	// JSON response - try direct DocumentMetadata first, then wrapped version
	var metadata DocumentMetadata
	if err := json.Unmarshal(resp.Body(), &metadata); err == nil && metadata.ID != "" {
		return metadataToDocument(&metadata), nil
	}

	var wrapped struct {
		DocumentMetadata DocumentMetadata `json:"documentMetadata"`
	}
	if err := json.Unmarshal(resp.Body(), &wrapped); err == nil && wrapped.DocumentMetadata.ID != "" {
		return metadataToDocument(&wrapped.DocumentMetadata), nil
	}

	return documentFallback(fallbackID, fallbackVersion, fallbackName), nil
}

// metadataToDocument converts a DocumentMetadata to a Document.
func metadataToDocument(m *DocumentMetadata) *Document {
	return &Document{
		ID:          m.ID,
		Name:        m.Name,
		Type:        m.Type,
		Description: m.Description,
		Version:     m.Version,
		Owner:       m.Owner,
		IsPrivate:   m.IsPrivate,
		Created:     m.ModificationInfo.CreatedTime,
		Modified:    m.ModificationInfo.LastModifiedTime,
		Labels:      m.Labels,
	}
}

// documentFallback returns a minimal Document when response parsing fails
// but the operation succeeded (2xx).
func documentFallback(id string, version int, name string) *Document {
	doc := &Document{ID: id, Version: version}
	if name != "" {
		doc.Name = name
	}
	// Try to extract name from response if not provided
	return doc
}

// extractIDFromResponse attempts to extract an ID from a response body
// This is a fallback for when normal response parsing fails
func extractIDFromResponse(body []byte) string {
	// Try to find an ID in various JSON structures
	var raw map[string]interface{}
	if err := json.Unmarshal(body, &raw); err != nil {
		return ""
	}

	// Check common ID field locations
	if id, ok := raw["id"].(string); ok && id != "" {
		return id
	}
	if metadata, ok := raw["documentMetadata"].(map[string]interface{}); ok {
		if id, ok := metadata["id"].(string); ok && id != "" {
			return id
		}
	}
	return ""
}

// DirectShare represents a direct share for a document
type DirectShare struct {
	ID         string   `json:"id"`
	DocumentID string   `json:"documentId"`
	Access     []string `json:"access"`
}

// ExactAccess reports whether the share grants exactly the given access level.
func (s DirectShare) ExactAccess(level string) bool {
	want := accessToLevels(level)
	if len(s.Access) != len(want) {
		return false
	}
	for _, w := range want {
		found := false
		for _, a := range s.Access {
			if a == w {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}

// DirectShareList represents a list of direct shares
type DirectShareList struct {
	Shares      []DirectShare `json:"direct-shares"`
	TotalCount  int           `json:"totalCount"`
	NextPageKey string        `json:"nextPageKey,omitempty"`
}

// SsoEntity represents an SSO user or group
type SsoEntity struct {
	ID   string `json:"id"`
	Type string `json:"type"` // "user" or "group"
}

// CreateDirectShareRequest contains the data needed to create a direct share
type CreateDirectShareRequest struct {
	DocumentID string      `json:"documentId"`
	Access     string      `json:"access"` // "read" or "read-write"
	Recipients []SsoEntity `json:"recipients"`

	// SuppressNotification stops the API from notifying the recipients. It is
	// sent as a query parameter, not in the body. The API notifies by default,
	// and a group recipient means every member of the group.
	SuppressNotification bool `json:"-"`
}

// AddDirectShareRecipientsOptions controls side effects of adding recipients.
type AddDirectShareRecipientsOptions struct {
	// SuppressNotification stops the API from notifying the added recipients.
	SuppressNotification bool
}

// setSendNotification opts a direct-share request out of recipient
// notifications. The API default (send-notification=true) is left implicit
// otherwise, so callers that never ask get the server's behavior unchanged.
func setSendNotification(req *resty.Request, suppress bool) {
	if suppress {
		req.SetQueryParam("send-notification", "false")
	}
}

// CreateDirectShare creates a direct share for a document
func (h *Handler) CreateDirectShare(ctx context.Context, req CreateDirectShareRequest) (*DirectShare, error) {
	r := h.client.HTTP().R().SetContext(ctx).SetBody(req)
	setSendNotification(r, req.SuppressNotification)
	resp, err := r.Post("/platform/document/v1/direct-shares")

	if err != nil {
		return nil, fmt.Errorf("failed to create direct share: %w", err)
	}

	if err := httpclient.CheckResponse(resp); err != nil {
		return nil, fmt.Errorf("failed to create direct share for document %q: %w", req.DocumentID, err)
	}

	var result DirectShare
	if err := json.Unmarshal(resp.Body(), &result); err != nil {
		return nil, fmt.Errorf("create direct share: parse response: %w", err)
	}

	return &result, nil
}

// ListDirectShares lists direct shares for a document.
// Paginates automatically using the Document API style (page-size sent on every request).
func (h *Handler) ListDirectShares(ctx context.Context, documentID string) (*DirectShareList, error) {
	var allShares []DirectShare
	var totalCount int
	nextPageKey := ""

	filterStr := ""
	if documentID != "" {
		filterStr = fmt.Sprintf("documentId=='%s'", escapeFilterValue(documentID))
	}

	for {
		var result DirectShareList
		req := h.client.HTTP().R().SetContext(ctx)

		filters := map[string]string{}
		if filterStr != "" {
			filters["filter"] = filterStr
		}

		req.SetQueryParamsFromValues(httpclient.PaginationParams{
			Style:         httpclient.PaginationDocumentAPI,
			PageKeyParam:  "page-key",
			PageSizeParam: "page-size",
			NextPageKey:   nextPageKey,
			Filters:       filters,
		}.QueryParams())

		resp, err := req.Get("/platform/document/v1/direct-shares")
		if err != nil {
			return nil, fmt.Errorf("failed to list direct shares: %w", err)
		}

		if err := httpclient.CheckResponse(resp); err != nil {
			return nil, fmt.Errorf("failed to list direct shares: %w", err)
		}

		if err := json.Unmarshal(resp.Body(), &result); err != nil {
			return nil, fmt.Errorf("list direct shares: parse response: %w", err)
		}

		allShares = append(allShares, result.Shares...)
		totalCount = result.TotalCount

		if result.NextPageKey == "" {
			break
		}
		nextPageKey = result.NextPageKey
	}

	return &DirectShareList{
		Shares:     allShares,
		TotalCount: totalCount,
	}, nil
}

// DeleteDirectShare deletes a direct share
func (h *Handler) DeleteDirectShare(ctx context.Context, shareID string) error {
	resp, err := h.client.HTTP().R().SetContext(ctx).
		Delete(fmt.Sprintf("/platform/document/v1/direct-shares/%s", httpclient.PathSegment(shareID)))

	if err != nil {
		return fmt.Errorf("failed to delete direct share: %w", err)
	}

	if err := httpclient.CheckResponse(resp); err != nil {
		return fmt.Errorf("failed to delete direct share %q: %w", shareID, err)
	}

	return nil
}

// AddDirectShareRecipients adds recipients to a direct share
func (h *Handler) AddDirectShareRecipients(ctx context.Context, shareID string, recipients []SsoEntity) error {
	return h.AddDirectShareRecipientsWithOptions(ctx, shareID, recipients, AddDirectShareRecipientsOptions{})
}

// AddDirectShareRecipientsWithOptions adds recipients to a direct share,
// honoring opts (for example, suppressing recipient notifications).
func (h *Handler) AddDirectShareRecipientsWithOptions(ctx context.Context, shareID string, recipients []SsoEntity,
	opts AddDirectShareRecipientsOptions) error {
	body := map[string]interface{}{
		"recipients": recipients,
	}

	r := h.client.HTTP().R().SetContext(ctx).SetBody(body)
	setSendNotification(r, opts.SuppressNotification)
	resp, err := r.Post(fmt.Sprintf("/platform/document/v1/direct-shares/%s/recipients/add", httpclient.PathSegment(shareID)))

	if err != nil {
		return fmt.Errorf("failed to add recipients: %w", err)
	}

	if err := httpclient.CheckResponse(resp); err != nil {
		return fmt.Errorf("failed to add recipients to share %q: %w", shareID, err)
	}

	return nil
}

// RemoveDirectShareRecipients removes recipients from a direct share
func (h *Handler) RemoveDirectShareRecipients(ctx context.Context, shareID string, recipientIDs []string) error {
	body := map[string]interface{}{
		"ids": recipientIDs,
	}

	resp, err := h.client.HTTP().R().SetContext(ctx).
		SetBody(body).
		Post(fmt.Sprintf("/platform/document/v1/direct-shares/%s/recipients/remove", httpclient.PathSegment(shareID)))

	if err != nil {
		return fmt.Errorf("failed to remove recipients: %w", err)
	}

	if err := httpclient.CheckResponse(resp); err != nil {
		return fmt.Errorf("failed to remove recipients from share %q: %w", shareID, err)
	}

	return nil
}

// EnvironmentShare represents an environment-wide share for a document
// (a document shared with everyone in the environment, reflected as isPrivate=false on the document).
// The Document Service API returns `access` as a string array (e.g. ["read"] or
// ["read","write"]), not a single string. The create endpoint accepts either form
// and normalises server-side.
type EnvironmentShare struct {
	ID         string   `json:"id"`
	DocumentID string   `json:"documentId"`
	Access     []string `json:"access"`
	// ClaimCount > 0 means at least one user has claimed this share. A created-but-unclaimed
	// share exists on the server but doesn't flip the document's isPrivate flag.
	ClaimCount int `json:"claimCount"`
}

// HasAccess reports whether the share grants the given access level.
// Accepts "read" (present if "read" or "write" is in Access) or "read-write"
// (present if "write" is in Access).
func (s EnvironmentShare) HasAccess(level string) bool {
	want := accessToLevels(level)
	for _, w := range want {
		found := false
		for _, a := range s.Access {
			if a == w {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}

// ExactAccess reports whether the share grants exactly the given access level
// (no more, no less). Use this when deciding whether to replace a share: a
// "read-write" share is NOT an exact match for "read".
func (s EnvironmentShare) ExactAccess(level string) bool {
	want := accessToLevels(level)
	if len(s.Access) != len(want) {
		return false
	}
	for _, w := range want {
		found := false
		for _, a := range s.Access {
			if a == w {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}

func accessToLevels(level string) []string {
	switch level {
	case "read-write":
		return []string{"read", "write"}
	default:
		return []string{"read"}
	}
}

// EnvironmentShareList represents a list of environment shares.
// The API's JSON key is kebab-case: "environment-shares".
type EnvironmentShareList struct {
	Shares      []EnvironmentShare `json:"environment-shares"`
	TotalCount  int                `json:"totalCount"`
	NextPageKey string             `json:"nextPageKey,omitempty"`
}

// CreateEnvironmentShareRequest contains the data needed to create an environment share.
// The Document Service API schema is asymmetric: GET returns access as an array
// (e.g. ["read"] or ["read","write"]), but POST accepts a single level string
// ("read" or "read-write") and the server expands it server-side into the array.
type CreateEnvironmentShareRequest struct {
	DocumentID string `json:"documentId"`
	Access     string `json:"access"` // "read" or "read-write"
}

// ErrShareConflict is returned when creating an environment share fails because
// one already exists for the document (HTTP 409). Callers can use errors.Is to
// detect this case without fragile string matching.
var ErrShareConflict = fmt.Errorf("environment share conflict")

// ErrVersionConflict is returned when a document PATCH fails due to optimistic
// locking (HTTP 409). Callers can use errors.Is to detect this without string matching.
var ErrVersionConflict = fmt.Errorf("document version conflict")

// CreateEnvironmentShare creates an environment-wide share for a document
func (h *Handler) CreateEnvironmentShare(ctx context.Context, req CreateEnvironmentShareRequest) (*EnvironmentShare, error) {
	resp, err := h.client.HTTP().R().SetContext(ctx).
		SetBody(req).
		Post("/platform/document/v1/environment-shares")

	if err != nil {
		return nil, fmt.Errorf("failed to create environment share: %w", err)
	}

	if err := httpclient.CheckResponse(resp); err != nil {
		var apiErr *httpclient.APIError
		if errors.As(err, &apiErr) && apiErr.StatusCode == 409 {
			return nil, fmt.Errorf("an environment share already exists for document %q: %w", req.DocumentID, ErrShareConflict)
		}
		return nil, fmt.Errorf("failed to create environment share for document %q: %w", req.DocumentID, err)
	}

	var result EnvironmentShare
	if err := json.Unmarshal(resp.Body(), &result); err != nil {
		return nil, fmt.Errorf("create environment share: parse response: %w", err)
	}

	return &result, nil
}

// ListEnvironmentShares lists environment shares for a document (or all if documentID is empty).
// Paginates automatically using the Document API style (page-size sent on every request).
func (h *Handler) ListEnvironmentShares(ctx context.Context, documentID string) (*EnvironmentShareList, error) {
	var allShares []EnvironmentShare
	var totalCount int
	nextPageKey := ""

	filterStr := ""
	if documentID != "" {
		filterStr = fmt.Sprintf("documentId=='%s'", escapeFilterValue(documentID))
	}

	for {
		var result EnvironmentShareList
		req := h.client.HTTP().R().SetContext(ctx)

		filters := map[string]string{}
		if filterStr != "" {
			filters["filter"] = filterStr
		}

		req.SetQueryParamsFromValues(httpclient.PaginationParams{
			Style:         httpclient.PaginationDocumentAPI,
			PageKeyParam:  "page-key",
			PageSizeParam: "page-size",
			NextPageKey:   nextPageKey,
			Filters:       filters,
		}.QueryParams())

		resp, err := req.Get("/platform/document/v1/environment-shares")
		if err != nil {
			return nil, fmt.Errorf("failed to list environment shares: %w", err)
		}

		if err := httpclient.CheckResponse(resp); err != nil {
			return nil, fmt.Errorf("failed to list environment shares: %w", err)
		}

		if err := json.Unmarshal(resp.Body(), &result); err != nil {
			return nil, fmt.Errorf("list environment shares: parse response: %w", err)
		}

		allShares = append(allShares, result.Shares...)
		totalCount = result.TotalCount

		if result.NextPageKey == "" {
			break
		}
		nextPageKey = result.NextPageKey
	}

	return &EnvironmentShareList{
		Shares:     allShares,
		TotalCount: totalCount,
	}, nil
}

// DeleteEnvironmentShare deletes an environment share
func (h *Handler) DeleteEnvironmentShare(ctx context.Context, shareID string) error {
	resp, err := h.client.HTTP().R().SetContext(ctx).
		Delete(fmt.Sprintf("/platform/document/v1/environment-shares/%s", httpclient.PathSegment(shareID)))

	if err != nil {
		return fmt.Errorf("failed to delete environment share: %w", err)
	}

	if err := httpclient.CheckResponse(resp); err != nil {
		return fmt.Errorf("failed to delete environment share %q: %w", shareID, err)
	}

	return nil
}

// GetEnvironmentShare retrieves an environment share by ID, resolving it to the
// document it points to. Environment share URLs (`.../document/v0/#share=<id>`)
// carry only the share ID, not the document ID, so this is how a share link
// gets resolved.
func (h *Handler) GetEnvironmentShare(ctx context.Context, shareID string) (*EnvironmentShare, error) {
	resp, err := h.client.HTTP().R().SetContext(ctx).
		Get(fmt.Sprintf("/platform/document/v1/environment-shares/%s", httpclient.PathSegment(shareID)))

	if err != nil {
		return nil, fmt.Errorf("failed to get environment share: %w", err)
	}

	if err := httpclient.CheckResponse(resp); err != nil {
		return nil, fmt.Errorf("failed to get environment share %q: %w", shareID, err)
	}

	var result EnvironmentShare
	if err := json.Unmarshal(resp.Body(), &result); err != nil {
		return nil, fmt.Errorf("get environment share: parse response: %w", err)
	}

	return &result, nil
}

// EnvironmentShareClaimResult is what claiming an environment share returns: the
// document the share points to and the access the caller now holds on it.
type EnvironmentShareClaimResult struct {
	DocumentID   string   `json:"documentId"`
	DocumentType string   `json:"documentType"`
	Access       []string `json:"access"`
}

// ClaimEnvironmentShare claims an environment share, mirroring the browser's
// "Copy link to share" -> open flow. Claiming is how a recipient resolves a share
// link: GetEnvironmentShare only answers for the share's owner, so the claim
// response is the one place the recipient learns the document.
func (h *Handler) ClaimEnvironmentShare(ctx context.Context, shareID string) (*EnvironmentShareClaimResult, error) {
	resp, err := h.client.HTTP().R().SetContext(ctx).
		Put(fmt.Sprintf("/platform/document/v1/environment-shares/%s/claim", httpclient.PathSegment(shareID)))

	if err != nil {
		return nil, fmt.Errorf("failed to claim environment share: %w", err)
	}

	if err := httpclient.CheckResponse(resp); err != nil {
		return nil, fmt.Errorf("failed to claim environment share %q: %w", shareID, err)
	}

	var result EnvironmentShareClaimResult
	if err := json.Unmarshal(resp.Body(), &result); err != nil {
		return nil, fmt.Errorf("claim environment share: parse response: %w", err)
	}

	return &result, nil
}

// MakeDocumentPublic makes a document public (isPrivate=false): everyone in
// the environment can find and read it, with no share object involved. It is
// the half of the "Share with environment" UI action that an environment share
// does not cover — a share is a link users have to claim. Only the owner (or
// an admin, for ready-made documents) may call it; on a document that is
// already public it is a no-op.
func (h *Handler) MakeDocumentPublic(ctx context.Context, id string) error {
	return h.setVisibility(ctx, id, "makePublic")
}

// MakeDocumentPrivate makes a document private again (isPrivate=true),
// revoking the read access everyone in the environment had through it being
// public. The owner, direct-share recipients and environment-share claimers
// keep theirs. Owner-only like MakeDocumentPublic; a no-op on a private
// document.
func (h *Handler) MakeDocumentPrivate(ctx context.Context, id string) error {
	return h.setVisibility(ctx, id, "makePrivate")
}

// SetDocumentPublic makes a document public.
//
// Deprecated: use MakeDocumentPublic. The version is ignored: the Document
// API's :makePublic action takes none, and the isPrivate field of PATCH
// /documents/{id} this used to send is deprecated.
func (h *Handler) SetDocumentPublic(ctx context.Context, id string, version int) error {
	return h.MakeDocumentPublic(ctx, id)
}

// setVisibility calls the document's :makePublic or :makePrivate action.
func (h *Handler) setVisibility(ctx context.Context, id, action string) error {
	resp, err := h.client.HTTP().R().SetContext(ctx).
		Post(fmt.Sprintf("/platform/document/v1/documents/%s:%s", httpclient.PathSegment(id), httpclient.PathSegment(action)))
	if err != nil {
		return fmt.Errorf("failed to update document visibility: %w", err)
	}
	if err := httpclient.CheckResponse(resp); err != nil {
		return fmt.Errorf("failed to update document visibility for %q: %w", id, err)
	}
	return nil
}

// MarshalJSON custom marshaler for Document to include content when present
func (d Document) MarshalJSON() ([]byte, error) {
	type Alias Document

	// If content is present, try to parse it as JSON for cleaner output
	var contentJSON json.RawMessage
	if len(d.Content) > 0 {
		// Check if content is valid JSON
		if json.Valid(d.Content) {
			contentJSON = d.Content
		} else {
			// If not valid JSON, encode as base64 string
			contentJSON, _ = json.Marshal(string(d.Content))
		}
	}

	// Only include modificationInfo if timestamps are set
	var modInfo *ModificationInfo
	if !d.Created.IsZero() || !d.Modified.IsZero() {
		modInfo = &ModificationInfo{
			CreatedTime:      d.Created,
			LastModifiedTime: d.Modified,
		}
	}

	return json.Marshal(&struct {
		*Alias
		ModificationInfo *ModificationInfo `json:"modificationInfo,omitempty"`
		Content          json.RawMessage   `json:"content,omitempty"`
	}{
		Alias:            (*Alias)(&d),
		ModificationInfo: modInfo,
		Content:          contentJSON,
	})
}

// UnmarshalJSON custom unmarshaler for Document to handle nested modificationInfo
// and version as string or int.
func (d *Document) UnmarshalJSON(data []byte) error {
	type Alias Document
	aux := &struct {
		ModificationInfo *ModificationInfo `json:"modificationInfo"`
		Content          json.RawMessage   `json:"content"`
		Version          json.RawMessage   `json:"version"`
		*Alias
	}{
		Alias: (*Alias)(d),
	}

	if err := json.Unmarshal(data, &aux); err != nil {
		return err
	}

	if len(aux.Version) > 0 {
		v, err := parseFlexibleInt(aux.Version)
		if err != nil {
			return fmt.Errorf("invalid version: %w", err)
		}
		d.Version = v
	}

	if aux.ModificationInfo != nil {
		d.Created = aux.ModificationInfo.CreatedTime
		d.Modified = aux.ModificationInfo.LastModifiedTime
	}

	// Handle content field - it could be a JSON object or a string
	if len(aux.Content) > 0 {
		// If it's a JSON object/array, store as-is
		if aux.Content[0] == '{' || aux.Content[0] == '[' {
			d.Content = aux.Content
		} else {
			// It's a quoted string, unmarshal it
			var contentStr string
			if err := json.Unmarshal(aux.Content, &contentStr); err == nil {
				d.Content = []byte(contentStr)
			}
		}
	}

	return nil
}

// Snapshot represents a document snapshot (version)
type Snapshot struct {
	SnapshotVersion  int             `json:"snapshotVersion"`
	DocumentVersion  int             `json:"documentVersion"`
	Description      string          `json:"description,omitempty"`
	ModificationInfo SnapshotModInfo `json:"modificationInfo"`
	CreatedBy        string          `json:"-"`
	CreatedTime      time.Time       `json:"-"`
}

// SnapshotModInfo contains creation info for a snapshot
type SnapshotModInfo struct {
	CreatedBy   string    `json:"createdBy"`
	CreatedTime time.Time `json:"createdTime"`
}

// UnmarshalJSON custom unmarshaler for Snapshot to flatten modificationInfo
// and handle version fields as string or int.
func (s *Snapshot) UnmarshalJSON(data []byte) error {
	type Alias Snapshot
	aux := &struct {
		SnapshotVersion json.RawMessage `json:"snapshotVersion"`
		DocumentVersion json.RawMessage `json:"documentVersion"`
		*Alias
	}{
		Alias: (*Alias)(s),
	}
	if err := json.Unmarshal(data, &aux); err != nil {
		return err
	}
	if len(aux.SnapshotVersion) > 0 {
		v, err := parseFlexibleInt(aux.SnapshotVersion)
		if err != nil {
			return fmt.Errorf("invalid snapshotVersion: %w", err)
		}
		s.SnapshotVersion = v
	}
	if len(aux.DocumentVersion) > 0 {
		v, err := parseFlexibleInt(aux.DocumentVersion)
		if err != nil {
			return fmt.Errorf("invalid documentVersion: %w", err)
		}
		s.DocumentVersion = v
	}
	// Flatten modificationInfo fields for table display
	s.CreatedBy = s.ModificationInfo.CreatedBy
	s.CreatedTime = s.ModificationInfo.CreatedTime
	return nil
}

// SnapshotList represents a list of snapshots
type SnapshotList struct {
	Snapshots   []Snapshot `json:"snapshots"`
	TotalCount  int        `json:"totalCount"`
	NextPageKey string     `json:"nextPageKey,omitempty"`
}

// ListSnapshots retrieves all snapshots for a document
func (h *Handler) ListSnapshots(ctx context.Context, documentID string) (*SnapshotList, error) {
	var allSnapshots []Snapshot
	var totalCount int
	nextPageKey := ""

	for {
		var result SnapshotList
		req := h.client.HTTP().R().SetContext(ctx)

		req.SetQueryParamsFromValues(httpclient.PaginationParams{
			Style:        httpclient.PaginationDefault,
			PageKeyParam: "page-key",
			NextPageKey:  nextPageKey,
		}.QueryParams())

		resp, err := req.Get(fmt.Sprintf("/platform/document/v1/documents/%s/snapshots", httpclient.PathSegment(documentID)))
		if err != nil {
			return nil, fmt.Errorf("failed to list snapshots: %w", err)
		}

		if err := httpclient.CheckResponse(resp); err != nil {
			return nil, fmt.Errorf("failed to list snapshots for document %q: %w", documentID, err)
		}

		if err := json.Unmarshal(resp.Body(), &result); err != nil {
			return nil, fmt.Errorf("list snapshots: parse response: %w", err)
		}

		allSnapshots = append(allSnapshots, result.Snapshots...)
		totalCount = result.TotalCount

		if result.NextPageKey == "" {
			break
		}
		nextPageKey = result.NextPageKey
	}

	return &SnapshotList{
		Snapshots:  allSnapshots,
		TotalCount: totalCount,
	}, nil
}

// GetSnapshot retrieves metadata for a specific snapshot
func (h *Handler) GetSnapshot(ctx context.Context, documentID string, version int) (*Snapshot, error) {
	resp, err := h.client.HTTP().R().SetContext(ctx).
		Get(fmt.Sprintf("/platform/document/v1/documents/%s/snapshots/%d", httpclient.PathSegment(documentID), version))

	if err != nil {
		return nil, fmt.Errorf("failed to get snapshot: %w", err)
	}

	if err := httpclient.CheckResponse(resp); err != nil {
		return nil, fmt.Errorf("failed to get snapshot %d for document %q: %w", version, documentID, err)
	}

	var result Snapshot
	if err := json.Unmarshal(resp.Body(), &result); err != nil {
		return nil, fmt.Errorf("get snapshot: parse response: %w", err)
	}

	return &result, nil
}

// RestoreSnapshot restores a document to a specific snapshot version
func (h *Handler) RestoreSnapshot(ctx context.Context, documentID string, version int) (*DocumentMetadata, error) {
	resp, err := h.client.HTTP().R().SetContext(ctx).
		Post(fmt.Sprintf("/platform/document/v1/documents/%s/snapshots/%d:restore", httpclient.PathSegment(documentID), version))

	if err != nil {
		return nil, fmt.Errorf("failed to restore snapshot: %w", err)
	}

	if err := httpclient.CheckResponse(resp); err != nil {
		return nil, fmt.Errorf("failed to restore snapshot %d for document %q: %w", version, documentID, err)
	}

	var result struct {
		DocumentMetadata DocumentMetadata `json:"documentMetadata"`
	}
	if err := json.Unmarshal(resp.Body(), &result); err != nil {
		return nil, fmt.Errorf("restore snapshot: parse response: %w", err)
	}

	return &result.DocumentMetadata, nil
}

// DeleteSnapshot deletes a specific snapshot
func (h *Handler) DeleteSnapshot(ctx context.Context, documentID string, version int) error {
	resp, err := h.client.HTTP().R().SetContext(ctx).
		Delete(fmt.Sprintf("/platform/document/v1/documents/%s/snapshots/%d", httpclient.PathSegment(documentID), version))

	if err != nil {
		return fmt.Errorf("failed to delete snapshot: %w", err)
	}

	if err := httpclient.CheckResponse(resp); err != nil {
		return fmt.Errorf("failed to delete snapshot %d for document %q: %w", version, documentID, err)
	}

	return nil
}

// GetAtVersion retrieves a document's content at a specific snapshot version
func (h *Handler) GetAtVersion(ctx context.Context, id string, version int) (*Document, error) {
	resp, err := h.client.HTTP().R().SetContext(ctx).
		SetQueryParam("snapshot-version", fmt.Sprintf("%d", version)).
		Get(fmt.Sprintf("/platform/document/v1/documents/%s", httpclient.PathSegment(id)))

	if err != nil {
		return nil, fmt.Errorf("failed to get document at version: %w", err)
	}

	if err := httpclient.CheckResponse(resp); err != nil {
		return nil, fmt.Errorf("failed to get document %q at version %d: %w", id, version, err)
	}

	doc, err := ParseMultipartDocument(resp)
	if err != nil {
		return nil, fmt.Errorf("failed to parse document response: %w", err)
	}

	return doc, nil
}

// MarshalYAML custom marshaler for Document to include content for YAML output
func (d Document) MarshalYAML() (interface{}, error) {
	// Parse content as structured data if it's valid JSON
	var contentData interface{}
	if len(d.Content) > 0 && json.Valid(d.Content) {
		if err := json.Unmarshal(d.Content, &contentData); err != nil {
			// If unmarshal fails, use raw string
			contentData = string(d.Content)
		}
	} else if len(d.Content) > 0 {
		contentData = string(d.Content)
	}

	// Build the output map
	output := map[string]interface{}{
		"id":        d.ID,
		"name":      d.Name,
		"type":      d.Type,
		"version":   d.Version,
		"owner":     d.Owner,
		"isPrivate": d.IsPrivate,
	}

	if d.Description != "" {
		output["description"] = d.Description
	}

	if contentData != nil {
		output["content"] = contentData
	}

	if !d.Created.IsZero() || !d.Modified.IsZero() {
		output["modificationInfo"] = map[string]interface{}{
			"createdTime":      d.Created,
			"lastModifiedTime": d.Modified,
		}
	}

	if d.OriginAppID != "" {
		output["originAppId"] = d.OriginAppID
	}
	if d.OriginExtensionID != "" {
		output["originExtensionId"] = d.OriginExtensionID
	}
	if len(d.Labels) > 0 {
		output["labels"] = d.Labels
	}
	if d.ShareInfo != nil {
		output["shareInfo"] = d.ShareInfo
	}
	if d.UserContext != nil {
		output["userContext"] = d.UserContext
	}

	return output, nil
}
