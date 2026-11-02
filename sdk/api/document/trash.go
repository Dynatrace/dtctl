package document

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/Dynatrace/dtctl/sdk/httpclient"
)

// TrashedDocument represents a document in the trash (from GET /trash/documents/{id})
type TrashedDocument struct {
	ID               string           `json:"id" yaml:"id"`
	Name             string           `json:"name" yaml:"name"`
	Type             string           `json:"type" yaml:"type"`
	Version          int              `json:"version" yaml:"version"`
	Owner            string           `json:"owner" yaml:"owner"`
	DeletionInfo     DeletionInfo     `json:"deletionInfo" yaml:"deletionInfo"`
	ModificationInfo ModificationInfo `json:"modificationInfo,omitempty" yaml:"modificationInfo,omitempty"`
	// Computed fields for display
	DeletedBy string    `json:"-" yaml:"-"`
	DeletedAt time.Time `json:"-" yaml:"-"`
}

// UnmarshalJSON custom unmarshaler for TrashedDocument to handle version as string or int.
func (t *TrashedDocument) UnmarshalJSON(data []byte) error {
	type Alias TrashedDocument
	aux := &struct {
		Version json.RawMessage `json:"version"`
		*Alias
	}{
		Alias: (*Alias)(t),
	}
	if err := json.Unmarshal(data, aux); err != nil {
		return err
	}
	if len(aux.Version) > 0 {
		v, err := parseFlexibleInt(aux.Version)
		if err != nil {
			return fmt.Errorf("invalid version: %w", err)
		}
		t.Version = v
	}
	return nil
}

// TrashDocumentListEntry represents a document in the trash list (from GET /trash/documents)
type TrashDocumentListEntry struct {
	ID           string       `json:"id" yaml:"id"`
	Name         string       `json:"name" yaml:"name"`
	Type         string       `json:"type" yaml:"type"`
	DeletionInfo DeletionInfo `json:"deletionInfo" yaml:"deletionInfo"`
	// Computed fields for display
	DeletedBy string    `json:"-" yaml:"-"`
	DeletedAt time.Time `json:"-" yaml:"-"`
}

// DeletionInfo contains deletion metadata
type DeletionInfo struct {
	DeletedBy   string    `json:"deletedBy" yaml:"deletedBy"`
	DeletedTime time.Time `json:"deletedTime" yaml:"deletedTime"`
}

// TrashListOptions contains filter options for listing trashed documents
type TrashListOptions struct {
	Type          string    // Filter by type: "dashboard", "notebook"
	DeletedBy     string    // Filter by who deleted it
	DeletedAfter  time.Time // Show documents deleted after date
	DeletedBefore time.Time // Show documents deleted before date
	ChunkSize     int64     // Page size for pagination (0 = no chunking)
}

// RestoreOptions contains options for restoring a document
type RestoreOptions struct {
	Force   bool   // Restore even if name conflicts exist (not supported by API yet)
	NewName string // Restore with a new name (not supported by API yet)
}

// TrashList represents a list of trashed documents
type TrashList struct {
	Documents   []TrashDocumentListEntry `json:"documents"`
	TotalCount  int                      `json:"totalCount"`
	NextPageKey string                   `json:"nextPageKey,omitempty"`
}

// TrashHandler handles trash operations for documents
type TrashHandler struct {
	client *httpclient.Client
}

// NewTrashHandler creates a new trash handler
func NewTrashHandler(c *httpclient.Client) *TrashHandler {
	return &TrashHandler{client: c}
}

// List retrieves trashed documents matching the provided filters
func (h *TrashHandler) List(ctx context.Context, opts TrashListOptions) ([]TrashDocumentListEntry, error) {
	var allDocuments []TrashDocumentListEntry
	nextPageKey := ""

	// Build filter query parameter
	var filterStr string
	var conditions []string

	if opts.Type != "" {
		conditions = append(conditions, fmt.Sprintf("type=='%s'", escapeFilterValue(opts.Type)))
	}
	if opts.DeletedBy != "" {
		conditions = append(conditions, fmt.Sprintf("deletionInfo.deletedBy=='%s'", escapeFilterValue(opts.DeletedBy)))
	}
	if !opts.DeletedAfter.IsZero() {
		conditions = append(conditions, fmt.Sprintf("deletionInfo.deletedTime>='%s'", opts.DeletedAfter.Format(time.RFC3339)))
	}
	if !opts.DeletedBefore.IsZero() {
		conditions = append(conditions, fmt.Sprintf("deletionInfo.deletedTime<='%s'", opts.DeletedBefore.Format(time.RFC3339)))
	}

	if len(conditions) > 0 {
		filterStr = strings.Join(conditions, " and ")
	}

	for {
		var result TrashList
		req := h.client.HTTP().R().SetContext(ctx).SetResult(&result)

		req.SetQueryParamsFromValues(httpclient.PaginationParams{
			Style:         httpclient.PaginationDocumentAPI,
			PageKeyParam:  "page-key",
			PageSizeParam: "page-size",
			NextPageKey:   nextPageKey,
			PageSize:      int64(opts.ChunkSize),
			Filters:       map[string]string{"filter": filterStr},
		}.QueryParams())

		resp, err := req.Get("/platform/document/v1/trash/documents")
		if err != nil {
			return nil, fmt.Errorf("failed to list trash: %w", err)
		}

		if err := httpclient.CheckResponse(resp); err != nil {
			return nil, fmt.Errorf("failed to list trash: %w", err)
		}

		// Populate computed fields
		for i := range result.Documents {
			doc := &result.Documents[i]
			doc.DeletedBy = doc.DeletionInfo.DeletedBy
			doc.DeletedAt = doc.DeletionInfo.DeletedTime
		}

		allDocuments = append(allDocuments, result.Documents...)

		// If chunking is disabled, return first page only
		if opts.ChunkSize == 0 {
			return result.Documents, nil
		}

		// Check if there are more pages
		if result.NextPageKey == "" {
			break
		}
		nextPageKey = result.NextPageKey
	}

	return allDocuments, nil
}

// Get retrieves a specific trashed document by ID
func (h *TrashHandler) Get(ctx context.Context, id string) (*TrashedDocument, error) {
	var doc TrashedDocument

	resp, err := h.client.HTTP().R().SetContext(ctx).
		SetResult(&doc).
		Get(fmt.Sprintf("/platform/document/v1/trash/documents/%s", httpclient.PathSegment(id)))

	if err != nil {
		return nil, fmt.Errorf("failed to get trashed document: %w", err)
	}

	if err := httpclient.CheckResponse(resp); err != nil {
		return nil, fmt.Errorf("failed to get trashed document %q: %w", id, err)
	}

	// Populate computed fields
	doc.DeletedBy = doc.DeletionInfo.DeletedBy
	doc.DeletedAt = doc.DeletionInfo.DeletedTime

	return &doc, nil
}

// Restore restores a document from trash
func (h *TrashHandler) Restore(ctx context.Context, id string, opts RestoreOptions) error {
	req := h.client.HTTP().R().SetContext(ctx)

	// Note: The API doesn't support newName or force options in the spec
	// These are left here for potential future support
	if opts.NewName != "" {
		req.SetBody(map[string]interface{}{
			"name": opts.NewName,
		})
	}

	if opts.Force {
		req.SetQueryParam("force", "true")
	}

	resp, err := req.Post(fmt.Sprintf("/platform/document/v1/trash/documents/%s/restore", httpclient.PathSegment(id)))
	if err != nil {
		return fmt.Errorf("failed to restore document: %w", err)
	}

	if err := httpclient.CheckResponse(resp); err != nil {
		var apiErr *httpclient.APIError
		if errors.As(err, &apiErr) && apiErr.StatusCode == 409 {
			return fmt.Errorf("name conflict: document with same name exists")
		}
		return fmt.Errorf("failed to restore document %q: %w", id, err)
	}

	return nil
}

// Delete permanently deletes a document from trash
func (h *TrashHandler) Delete(ctx context.Context, id string) error {
	resp, err := h.client.HTTP().R().SetContext(ctx).
		Delete(fmt.Sprintf("/platform/document/v1/trash/documents/%s", httpclient.PathSegment(id)))

	if err != nil {
		return fmt.Errorf("failed to permanently delete document: %w", err)
	}

	if err := httpclient.CheckResponse(resp); err != nil {
		return fmt.Errorf("failed to permanently delete document %q: %w", id, err)
	}

	return nil
}
