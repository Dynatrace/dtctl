package document

import (
	"context"
	"time"

	"github.com/Dynatrace/dtctl/pkg/client"
	sdkdocument "github.com/Dynatrace/dtctl/sdk/api/document"
	"github.com/Dynatrace/dtctl/sdk/httpclient"
)

// TrashedDocument is the CLI read model for a trashed document.
type TrashedDocument struct {
	ID               string           `json:"id" yaml:"id" table:"ID"`
	Name             string           `json:"name" yaml:"name" table:"NAME"`
	Type             string           `json:"type" yaml:"type" table:"TYPE"`
	Version          int              `json:"version" yaml:"version" table:"VERSION,wide"`
	Owner            string           `json:"owner" yaml:"owner" table:"OWNER,wide"`
	DeletionInfo     DeletionInfo     `json:"deletionInfo" yaml:"deletionInfo" table:"-"`
	ModificationInfo ModificationInfo `json:"modificationInfo,omitempty" yaml:"modificationInfo,omitempty" table:"-"`
	DeletedBy        string           `json:"-" yaml:"-" table:"DELETED BY"`
	DeletedAt        time.Time        `json:"-" yaml:"-" table:"DELETED AT"`
}

// fromSDKTrashedDocument converts an SDK TrashedDocument to the CLI TrashedDocument.
func fromSDKTrashedDocument(d *sdkdocument.TrashedDocument) *TrashedDocument {
	return &TrashedDocument{
		ID:               d.ID,
		Name:             d.Name,
		Type:             d.Type,
		Version:          d.Version,
		Owner:            d.Owner,
		DeletionInfo:     d.DeletionInfo,
		ModificationInfo: d.ModificationInfo,
		DeletedBy:        d.DeletedBy,
		DeletedAt:        d.DeletedAt,
	}
}

// TrashDocumentListEntry is the CLI read model for a trashed document list entry.
type TrashDocumentListEntry struct {
	ID           string       `json:"id" yaml:"id" table:"ID"`
	Name         string       `json:"name" yaml:"name" table:"NAME"`
	Type         string       `json:"type" yaml:"type" table:"TYPE"`
	DeletionInfo DeletionInfo `json:"deletionInfo" yaml:"deletionInfo" table:"-"`
	DeletedBy    string       `json:"-" yaml:"-" table:"DELETED BY"`
	DeletedAt    time.Time    `json:"-" yaml:"-" table:"DELETED AT"`
}

// fromSDKTrashDocumentListEntry converts an SDK TrashDocumentListEntry to the CLI type.
func fromSDKTrashDocumentListEntry(d *sdkdocument.TrashDocumentListEntry) TrashDocumentListEntry {
	return TrashDocumentListEntry{
		ID:           d.ID,
		Name:         d.Name,
		Type:         d.Type,
		DeletionInfo: d.DeletionInfo,
		DeletedBy:    d.DeletedBy,
		DeletedAt:    d.DeletedAt,
	}
}

// Re-export SDK trash types that don't have table tags.
type (
	DeletionInfo     = sdkdocument.DeletionInfo
	TrashListOptions = sdkdocument.TrashListOptions
	RestoreOptions   = sdkdocument.RestoreOptions
)

// TrashList represents a list of trashed documents.
type TrashList struct {
	Documents   []TrashDocumentListEntry `json:"documents"`
	TotalCount  int                      `json:"totalCount"`
	NextPageKey string                   `json:"nextPageKey,omitempty"`
}

// TrashHandler handles trash operations for documents.
// It delegates to the SDK trash handler.
type TrashHandler struct {
	sdk *sdkdocument.TrashHandler
}

// NewTrashHandler creates a new trash handler.
func NewTrashHandler(c *client.Client) *TrashHandler {
	return &TrashHandler{
		sdk: sdkdocument.NewTrashHandler(httpclient.Wrap(c.HTTP())),
	}
}

// List retrieves trashed documents matching the provided filters.
func (h *TrashHandler) List(opts TrashListOptions) ([]TrashDocumentListEntry, error) {
	sdkEntries, err := h.sdk.List(context.Background(), opts)
	if err != nil {
		return nil, err
	}
	entries := make([]TrashDocumentListEntry, len(sdkEntries))
	for i := range sdkEntries {
		entries[i] = fromSDKTrashDocumentListEntry(&sdkEntries[i])
	}
	return entries, nil
}

// Get retrieves a specific trashed document by ID.
func (h *TrashHandler) Get(id string) (*TrashedDocument, error) {
	d, err := h.sdk.Get(context.Background(), id)
	if err != nil {
		return nil, err
	}
	return fromSDKTrashedDocument(d), nil
}

// Restore restores a document from trash.
func (h *TrashHandler) Restore(id string, opts RestoreOptions) error {
	return h.sdk.Restore(context.Background(), id, opts)
}

// Delete permanently deletes a document from trash.
func (h *TrashHandler) Delete(id string) error {
	return h.sdk.Delete(context.Background(), id)
}
