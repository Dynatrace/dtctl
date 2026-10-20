package document

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/dynatrace-oss/dtctl/pkg/client"
	"github.com/dynatrace-oss/dtctl/pkg/util/format"
	sdkdocument "github.com/dynatrace-oss/dtctl/sdk/api/document"
	"github.com/dynatrace-oss/dtctl/sdk/httpclient"
)

// Document is the CLI read model for a document resource.
// It mirrors the SDK type but adds table tags for CLI table output.
type Document struct {
	ID          string    `json:"id" table:"ID"`
	Name        string    `json:"name" table:"NAME"`
	Type        string    `json:"type" table:"TYPE"`
	Owner       string    `json:"owner" table:"OWNER"`
	IsPrivate   bool      `json:"isPrivate" table:"PRIVATE"`
	Created     time.Time `json:"-" table:"CREATED"`
	Description string    `json:"description,omitempty" table:"DESCRIPTION,wide"`
	Version     int       `json:"version" table:"VERSION,wide"`
	Modified    time.Time `json:"-" table:"MODIFIED,wide"`
	Content     []byte    `json:"-" table:"-"`

	OriginAppID       string                   `json:"originAppId,omitempty" yaml:"originAppId,omitempty" table:"-"`
	OriginExtensionID string                   `json:"originExtensionId,omitempty" yaml:"originExtensionId,omitempty" table:"-"`
	Labels            []string                 `json:"labels,omitempty" yaml:"labels,omitempty" table:"-"`
	ShareInfo         *sdkdocument.ShareInfo   `json:"shareInfo,omitempty" yaml:"shareInfo,omitempty" table:"-"`
	UserContext       *sdkdocument.UserContext `json:"userContext,omitempty" yaml:"userContext,omitempty" table:"-"`
}

// UnmarshalJSON delegates to the SDK Document unmarshaler to handle flexible version fields.
func (d *Document) UnmarshalJSON(data []byte) error {
	var sdk sdkdocument.Document
	if err := json.Unmarshal(data, &sdk); err != nil {
		return err
	}
	*d = *fromSDKDocument(&sdk)
	return nil
}

// MarshalJSON delegates to the SDK Document marshaler so that the document
// Content (stored as raw []byte) is rendered as structured JSON rather than
// being dropped (the field is tagged json:"-") or, for default marshaling,
// emitted as a list of raw byte values.
func (d Document) MarshalJSON() ([]byte, error) {
	return json.Marshal(toSDKDocument(&d))
}

// MarshalYAML delegates to the SDK Document marshaler so that the document
// Content is rendered as structured YAML rather than a list of raw byte values.
func (d Document) MarshalYAML() (any, error) {
	return toSDKDocument(&d).MarshalYAML()
}

// fromSDKDocument converts an SDK Document to the CLI Document.
func fromSDKDocument(d *sdkdocument.Document) *Document {
	return &Document{
		ID:                d.ID,
		Name:              d.Name,
		Type:              d.Type,
		Owner:             d.Owner,
		IsPrivate:         d.IsPrivate,
		Created:           d.Created,
		Description:       d.Description,
		Version:           d.Version,
		Modified:          d.Modified,
		Content:           d.Content,
		OriginAppID:       d.OriginAppID,
		OriginExtensionID: d.OriginExtensionID,
		Labels:            d.Labels,
		ShareInfo:         d.ShareInfo,
		UserContext:       d.UserContext,
	}
}

// toSDKDocument converts the CLI Document back to an SDK Document so the SDK's
// custom (and tested) JSON/YAML marshalers can be reused for output rendering.
func toSDKDocument(d *Document) *sdkdocument.Document {
	return &sdkdocument.Document{
		ID:                d.ID,
		Name:              d.Name,
		Type:              d.Type,
		Owner:             d.Owner,
		IsPrivate:         d.IsPrivate,
		Created:           d.Created,
		Description:       d.Description,
		Version:           d.Version,
		Modified:          d.Modified,
		Content:           d.Content,
		OriginAppID:       d.OriginAppID,
		OriginExtensionID: d.OriginExtensionID,
		Labels:            d.Labels,
		ShareInfo:         d.ShareInfo,
		UserContext:       d.UserContext,
	}
}

// DirectShare is the CLI read model for a direct share.
type DirectShare struct {
	ID         string   `json:"id" table:"ID"`
	DocumentID string   `json:"documentId" table:"DOCUMENT_ID"`
	Access     []string `json:"access" table:"ACCESS"`
}

// ExactAccess reports whether the share grants exactly the given access level.
// Delegates to the SDK DirectShare.ExactAccess method.
func (s DirectShare) ExactAccess(level string) bool {
	sdkShare := sdkdocument.DirectShare{Access: s.Access}
	return sdkShare.ExactAccess(level)
}

// fromSDKDirectShare converts an SDK DirectShare to the CLI DirectShare.
func fromSDKDirectShare(d *sdkdocument.DirectShare) *DirectShare {
	return &DirectShare{
		ID:         d.ID,
		DocumentID: d.DocumentID,
		Access:     d.Access,
	}
}

// DirectShareList represents a list of direct shares.
type DirectShareList struct {
	Shares      []DirectShare `json:"directShares"`
	TotalCount  int           `json:"totalCount"`
	NextPageKey string        `json:"nextPageKey,omitempty"`
}

// fromSDKDirectShareList converts an SDK DirectShareList to the CLI DirectShareList.
func fromSDKDirectShareList(l *sdkdocument.DirectShareList) *DirectShareList {
	shares := make([]DirectShare, len(l.Shares))
	for i, s := range l.Shares {
		shares[i] = *fromSDKDirectShare(&s)
	}
	return &DirectShareList{
		Shares:      shares,
		TotalCount:  l.TotalCount,
		NextPageKey: l.NextPageKey,
	}
}

// EnvironmentShare is the CLI read model for an environment share.
type EnvironmentShare struct {
	ID         string   `json:"id" table:"ID"`
	DocumentID string   `json:"documentId" table:"DOCUMENT_ID"`
	Access     []string `json:"access" table:"ACCESS"`
	ClaimCount int      `json:"claimCount" table:"CLAIM_COUNT"`
}

// HasAccess reports whether the share grants the given access level.
// Delegates to the SDK EnvironmentShare.HasAccess method.
func (s EnvironmentShare) HasAccess(level string) bool {
	sdkShare := sdkdocument.EnvironmentShare{Access: s.Access}
	return sdkShare.HasAccess(level)
}

// Level returns the share's access level as --access spells it ("read" or
// "read-write"), or the raw access list for anything else.
func (s EnvironmentShare) Level() string {
	sdkShare := sdkdocument.EnvironmentShare{Access: s.Access}
	for _, level := range []string{"read", "read-write"} {
		if sdkShare.ExactAccess(level) {
			return level
		}
	}
	return strings.Join(s.Access, ",")
}

// fromSDKEnvironmentShare converts an SDK EnvironmentShare to the CLI EnvironmentShare.
func fromSDKEnvironmentShare(s *sdkdocument.EnvironmentShare) *EnvironmentShare {
	return &EnvironmentShare{
		ID:         s.ID,
		DocumentID: s.DocumentID,
		Access:     s.Access,
		ClaimCount: s.ClaimCount,
	}
}

// EnvironmentShareList represents a list of environment shares.
type EnvironmentShareList struct {
	Shares      []EnvironmentShare `json:"environment-shares"`
	TotalCount  int                `json:"totalCount"`
	NextPageKey string             `json:"nextPageKey,omitempty"`
}

// fromSDKEnvironmentShareList converts an SDK EnvironmentShareList to the CLI EnvironmentShareList.
func fromSDKEnvironmentShareList(l *sdkdocument.EnvironmentShareList) *EnvironmentShareList {
	shares := make([]EnvironmentShare, len(l.Shares))
	for i, s := range l.Shares {
		shares[i] = *fromSDKEnvironmentShare(&s)
	}
	return &EnvironmentShareList{
		Shares:      shares,
		TotalCount:  l.TotalCount,
		NextPageKey: l.NextPageKey,
	}
}

// Snapshot is the CLI read model for a document snapshot.
type Snapshot struct {
	SnapshotVersion  int                         `json:"snapshotVersion" table:"VERSION"`
	DocumentVersion  int                         `json:"documentVersion" table:"DOC_VERSION,wide"`
	Description      string                      `json:"description,omitempty" table:"DESCRIPTION"`
	ModificationInfo sdkdocument.SnapshotModInfo `json:"modificationInfo" table:"-"`
	CreatedBy        string                      `json:"-" table:"CREATED_BY"`
	CreatedTime      time.Time                   `json:"-" table:"CREATED"`
}

// UnmarshalJSON delegates to the SDK Snapshot unmarshaler to handle flexible int/string versions.
func (s *Snapshot) UnmarshalJSON(data []byte) error {
	var sdk sdkdocument.Snapshot
	if err := json.Unmarshal(data, &sdk); err != nil {
		return err
	}
	*s = fromSDKSnapshot(&sdk)
	return nil
}

// MarshalYAML renders the snapshot through its JSON shape so YAML output matches
// JSON: the display-only CreatedBy/CreatedTime (json:"-", duplicates of
// ModificationInfo) are excluded and keys keep their camelCase. Without it,
// yaml.v3 reflection would lowercase keys and leak createdby/createdtime.
func (s Snapshot) MarshalYAML() (any, error) {
	return format.YAMLNodeFromJSON(s)
}

// fromSDKSnapshot converts an SDK Snapshot to the CLI Snapshot.
func fromSDKSnapshot(s *sdkdocument.Snapshot) Snapshot {
	return Snapshot{
		SnapshotVersion:  s.SnapshotVersion,
		DocumentVersion:  s.DocumentVersion,
		Description:      s.Description,
		ModificationInfo: s.ModificationInfo,
		CreatedBy:        s.CreatedBy,
		CreatedTime:      s.CreatedTime,
	}
}

// SnapshotList represents a list of snapshots.
type SnapshotList struct {
	Snapshots   []Snapshot `json:"snapshots"`
	TotalCount  int        `json:"totalCount"`
	NextPageKey string     `json:"nextPageKey,omitempty"`
}

// fromSDKSnapshotList converts an SDK SnapshotList to the CLI SnapshotList.
func fromSDKSnapshotList(l *sdkdocument.SnapshotList) *SnapshotList {
	snapshots := make([]Snapshot, len(l.Snapshots))
	for i, s := range l.Snapshots {
		snapshots[i] = fromSDKSnapshot(&s)
	}
	return &SnapshotList{
		Snapshots:   snapshots,
		TotalCount:  l.TotalCount,
		NextPageKey: l.NextPageKey,
	}
}

// Re-export SDK types that don't have table tags (pure data types).
type (
	DocumentMetadata                = sdkdocument.DocumentMetadata
	DocumentList                    = sdkdocument.DocumentList
	DocumentFilters                 = sdkdocument.DocumentFilters
	ModificationInfo                = sdkdocument.ModificationInfo
	ShareInfo                       = sdkdocument.ShareInfo
	UserContext                     = sdkdocument.UserContext
	CreateRequest                   = sdkdocument.CreateRequest
	UpdateRequest                   = sdkdocument.UpdateRequest
	SsoEntity                       = sdkdocument.SsoEntity
	CreateDirectShareRequest        = sdkdocument.CreateDirectShareRequest
	AddDirectShareRecipientsOptions = sdkdocument.AddDirectShareRecipientsOptions
	CreateEnvironmentShareRequest   = sdkdocument.CreateEnvironmentShareRequest
	SnapshotModInfo                 = sdkdocument.SnapshotModInfo
)

// Re-export SDK sentinel errors.
var (
	ErrShareConflict   = sdkdocument.ErrShareConflict
	ErrVersionConflict = sdkdocument.ErrVersionConflict
)

// Re-export SDK functions.
var (
	ParseMultipartDocument = sdkdocument.ParseMultipartDocument
)

// ConvertToDocuments converts a list of DocumentMetadata to a list of Documents for table output.
func ConvertToDocuments(list *DocumentList) []Document {
	docs := make([]Document, len(list.Documents))
	for i, meta := range list.Documents {
		docs[i] = documentMetadataToDocument(meta)
	}
	return docs
}

// documentMetadataToDocument converts a DocumentMetadata to a CLI Document.
func documentMetadataToDocument(m sdkdocument.DocumentMetadata) Document {
	return Document{
		ID:                m.ID,
		Name:              m.Name,
		Type:              m.Type,
		Description:       m.Description,
		Version:           m.Version,
		Owner:             m.Owner,
		IsPrivate:         m.IsPrivate,
		Created:           m.ModificationInfo.CreatedTime,
		Modified:          m.ModificationInfo.LastModifiedTime,
		OriginAppID:       m.OriginAppID,
		OriginExtensionID: m.OriginExtensionID,
		Labels:            m.Labels,
		ShareInfo:         m.ShareInfo,
		UserContext:       m.UserContext,
	}
}

// Handler handles document resources (dashboards, notebooks, etc.)
// It delegates to the SDK handler and adds CLI-specific convenience methods.
type Handler struct {
	sdk *sdkdocument.Handler
}

// NewHandler creates a new document handler.
func NewHandler(c *client.Client) *Handler {
	return &Handler{
		sdk: sdkdocument.NewHandler(httpclient.Wrap(c.HTTP())),
	}
}

// List retrieves documents matching the provided filters with automatic pagination.
func (h *Handler) List(filters DocumentFilters) (*DocumentList, error) {
	return h.sdk.List(context.Background(), filters)
}

// Get retrieves a specific document by ID.
func (h *Handler) Get(id string) (*Document, error) {
	d, err := h.sdk.Get(context.Background(), id)
	if err != nil {
		return nil, err
	}
	return fromSDKDocument(d), nil
}

// GetMetadata retrieves only the metadata for a document.
func (h *Handler) GetMetadata(id string) (*DocumentMetadata, error) {
	return h.sdk.GetMetadata(context.Background(), id)
}

// IsNotFound reports whether err indicates the document does not exist (HTTP 404),
// as opposed to a transient, auth, or other failure.
func IsNotFound(err error) bool {
	return errors.Is(err, httpclient.ErrNotFound)
}

// GetRaw retrieves a document's content as raw bytes.
func (h *Handler) GetRaw(id string) ([]byte, error) {
	doc, err := h.sdk.Get(context.Background(), id)
	if err != nil {
		return nil, err
	}
	return doc.Content, nil
}

// Delete deletes a document.
func (h *Handler) Delete(id string, version int) error {
	return h.sdk.Delete(context.Background(), id, version)
}

// Create creates a new document.
func (h *Handler) Create(req CreateRequest) (*Document, error) {
	d, err := h.sdk.Create(context.Background(), req)
	if err != nil {
		return nil, err
	}
	return fromSDKDocument(d), nil
}

// Update updates a document's content.
func (h *Handler) Update(id string, version int, content []byte, contentType string) (*Document, error) {
	d, err := h.sdk.Update(context.Background(), id, version, content, contentType)
	if err != nil {
		return nil, err
	}
	return fromSDKDocument(d), nil
}

// UpdateWithMetadata updates a document's content and optionally its metadata (name, description).
func (h *Handler) UpdateWithMetadata(id string, version int, content []byte, contentType string, name string, description string) (*Document, error) {
	d, err := h.sdk.UpdateWithMetadata(context.Background(), id, version, content, contentType, name, description)
	if err != nil {
		return nil, err
	}
	return fromSDKDocument(d), nil
}

// UpdateDocument performs a partial update of a document (content, name,
// description, and/or labels). It is the general form behind Update /
// UpdateWithMetadata and the only write path that can set labels.
func (h *Handler) UpdateDocument(id string, version int, req UpdateRequest) (*Document, error) {
	d, err := h.sdk.UpdateDocument(context.Background(), id, version, req)
	if err != nil {
		return nil, err
	}
	return fromSDKDocument(d), nil
}

// CreateDirectShare creates a direct share for a document.
func (h *Handler) CreateDirectShare(req CreateDirectShareRequest) (*DirectShare, error) {
	d, err := h.sdk.CreateDirectShare(context.Background(), req)
	if err != nil {
		return nil, err
	}
	return fromSDKDirectShare(d), nil
}

// ListDirectShares lists direct shares for a document.
func (h *Handler) ListDirectShares(documentID string) (*DirectShareList, error) {
	l, err := h.sdk.ListDirectShares(context.Background(), documentID)
	if err != nil {
		return nil, err
	}
	return fromSDKDirectShareList(l), nil
}

// DeleteDirectShare deletes a direct share.
func (h *Handler) DeleteDirectShare(shareID string) error {
	return h.sdk.DeleteDirectShare(context.Background(), shareID)
}

// AddDirectShareRecipients adds recipients to a direct share.
func (h *Handler) AddDirectShareRecipients(shareID string, recipients []SsoEntity) error {
	return h.sdk.AddDirectShareRecipients(context.Background(), shareID, recipients)
}

// AddDirectShareRecipientsWithOptions adds recipients to a direct share, honoring opts.
func (h *Handler) AddDirectShareRecipientsWithOptions(shareID string, recipients []SsoEntity,
	opts AddDirectShareRecipientsOptions) error {
	return h.sdk.AddDirectShareRecipientsWithOptions(context.Background(), shareID, recipients, opts)
}

// RemoveDirectShareRecipients removes recipients from a direct share.
func (h *Handler) RemoveDirectShareRecipients(shareID string, recipientIDs []string) error {
	return h.sdk.RemoveDirectShareRecipients(context.Background(), shareID, recipientIDs)
}

// CreateEnvironmentShare creates an environment-wide share for a document.
func (h *Handler) CreateEnvironmentShare(req CreateEnvironmentShareRequest) (*EnvironmentShare, error) {
	s, err := h.sdk.CreateEnvironmentShare(context.Background(), req)
	if err != nil {
		return nil, err
	}
	return fromSDKEnvironmentShare(s), nil
}

// ListEnvironmentShares lists environment shares for a document (or all if documentID is empty).
func (h *Handler) ListEnvironmentShares(documentID string) (*EnvironmentShareList, error) {
	l, err := h.sdk.ListEnvironmentShares(context.Background(), documentID)
	if err != nil {
		return nil, err
	}
	return fromSDKEnvironmentShareList(l), nil
}

// GetEnvironmentShare retrieves an environment share by ID.
func (h *Handler) GetEnvironmentShare(shareID string) (*EnvironmentShare, error) {
	s, err := h.sdk.GetEnvironmentShare(context.Background(), shareID)
	if err != nil {
		return nil, err
	}
	return fromSDKEnvironmentShare(s), nil
}

// EnvironmentShareClaim is the CLI read model for a claimed environment share.
type EnvironmentShareClaim struct {
	DocumentID   string   `json:"documentId" yaml:"documentId" table:"DOCUMENT_ID"`
	Name         string   `json:"name,omitempty" yaml:"name,omitempty" table:"NAME"`
	DocumentType string   `json:"documentType" yaml:"documentType" table:"TYPE"`
	Access       []string `json:"access" yaml:"access" table:"-"`
	AccessLevel  string   `json:"-" yaml:"-" table:"ACCESS"`
	URL          string   `json:"url,omitempty" yaml:"url,omitempty" table:"URL,wide"`
}

// ClaimEnvironmentShare claims an environment share, granting the current user
// access to the shared document.
func (h *Handler) ClaimEnvironmentShare(shareID string) (*EnvironmentShareClaim, error) {
	r, err := h.sdk.ClaimEnvironmentShare(context.Background(), shareID)
	if err != nil {
		return nil, err
	}
	return &EnvironmentShareClaim{
		DocumentID:   r.DocumentID,
		DocumentType: r.DocumentType,
		Access:       r.Access,
		AccessLevel:  strings.Join(r.Access, ","),
	}, nil
}

// ParseShareRef accepts a bare share ID or a pasted share link
// (`https://<env>/.../#share=<id>`) and returns the share ID plus the link's
// lowercase hostname ("" for a bare ID).
func ParseShareRef(input string) (id, host string, err error) {
	input = strings.TrimSpace(input)
	if i := strings.Index(input, "#share="); i >= 0 {
		id = input[i+len("#share="):]
		if j := strings.IndexByte(id, '&'); j >= 0 {
			id = id[:j]
		}
		if u, perr := url.Parse(input[:i]); perr == nil {
			host = strings.ToLower(u.Hostname())
		}
	} else {
		id = input
	}
	if id == "" || strings.ContainsAny(id, "/?# ") {
		return "", "", fmt.Errorf("invalid share ID or URL %q", input)
	}
	return id, host, nil
}

// DeleteEnvironmentShare deletes an environment share.
func (h *Handler) DeleteEnvironmentShare(shareID string) error {
	return h.sdk.DeleteEnvironmentShare(context.Background(), shareID)
}

// ListSnapshots retrieves all snapshots for a document.
func (h *Handler) ListSnapshots(documentID string) (*SnapshotList, error) {
	l, err := h.sdk.ListSnapshots(context.Background(), documentID)
	if err != nil {
		return nil, err
	}
	return fromSDKSnapshotList(l), nil
}

// GetSnapshot retrieves metadata for a specific snapshot.
func (h *Handler) GetSnapshot(documentID string, version int) (*Snapshot, error) {
	s, err := h.sdk.GetSnapshot(context.Background(), documentID, version)
	if err != nil {
		return nil, err
	}
	snap := fromSDKSnapshot(s)
	return &snap, nil
}

// RestoreSnapshot restores a document to a specific snapshot version.
func (h *Handler) RestoreSnapshot(documentID string, version int) (*DocumentMetadata, error) {
	return h.sdk.RestoreSnapshot(context.Background(), documentID, version)
}

// DeleteSnapshot deletes a specific snapshot.
func (h *Handler) DeleteSnapshot(documentID string, version int) error {
	return h.sdk.DeleteSnapshot(context.Background(), documentID, version)
}

// GetAtVersion retrieves a document's content at a specific snapshot version.
func (h *Handler) GetAtVersion(id string, version int) (*Document, error) {
	d, err := h.sdk.GetAtVersion(context.Background(), id, version)
	if err != nil {
		return nil, err
	}
	return fromSDKDocument(d), nil
}

// EnvironmentLinkResult is what EnsureEnvironmentLink did.
type EnvironmentLinkResult struct {
	// Share is the environment share the document now has at the access level.
	Share *EnvironmentShare
	// Created is true when Share was created by this call.
	Created bool
}

// EnsureEnvironmentLink idempotently ensures the document has an environment
// share at exactly the access level: a link anyone in the environment can
// claim. It leaves the document's isPrivate flag alone, and never touches a
// share at another level — a document has at most one share per level, so a
// read and a read-write link coexist and neither has to be replaced.
func (h *Handler) EnsureEnvironmentLink(documentID, access string) (*EnvironmentLinkResult, error) {
	existing, err := h.sdk.ListEnvironmentShares(context.Background(), documentID)
	if err != nil {
		return nil, err
	}
	if match := findSDKShare(existing.Shares, access); match != nil {
		return &EnvironmentLinkResult{Share: fromSDKEnvironmentShare(match)}, nil
	}

	created, err := h.sdk.CreateEnvironmentShare(context.Background(), CreateEnvironmentShareRequest{
		DocumentID: documentID,
		Access:     access,
	})
	if err == nil {
		return &EnvironmentLinkResult{Share: fromSDKEnvironmentShare(created), Created: true}, nil
	}

	// A 409 means a share at this level exists: another process created it
	// between the list and the create. Reuse it.
	if !errors.Is(err, sdkdocument.ErrShareConflict) {
		return nil, err
	}
	reListed, reErr := h.sdk.ListEnvironmentShares(context.Background(), documentID)
	if reErr != nil {
		return nil, fmt.Errorf("create returned conflict and re-list failed: %w", reErr)
	}
	if match := findSDKShare(reListed.Shares, access); match != nil {
		return &EnvironmentLinkResult{Share: fromSDKEnvironmentShare(match)}, nil
	}
	return nil, err
}

// RemoveEnvironmentLinks deletes the document's environment shares. A
// non-empty access deletes only shares at exactly that level. The document's
// isPrivate flag and its direct (user/group) shares are left alone. It reports
// how many shares were deleted; a failure partway says how many were.
func (h *Handler) RemoveEnvironmentLinks(documentID, access string) (int, error) {
	existing, err := h.sdk.ListEnvironmentShares(context.Background(), documentID)
	if err != nil {
		return 0, err
	}

	var toDelete []string
	for _, s := range existing.Shares {
		if access == "" || s.ExactAccess(access) {
			toDelete = append(toDelete, s.ID)
		}
	}

	deleted := 0
	for _, id := range toDelete {
		if err := h.sdk.DeleteEnvironmentShare(context.Background(), id); err != nil {
			return deleted, fmt.Errorf("deleting environment share %s failed "+
				"(%d of %d environment share(s) deleted; re-run to remove the rest): %w",
				id, deleted, len(toDelete), err)
		}
		deleted++
	}
	return deleted, nil
}

// SetPrivate makes the document private (isPrivate=true) or public
// (isPrivate=false) through the Document API's :makePrivate/:makePublic
// actions. Neither takes a version, and both are a no-op when the document is
// already in that state.
func (h *Handler) SetPrivate(documentID string, private bool) error {
	if private {
		return h.sdk.MakeDocumentPrivate(context.Background(), documentID)
	}
	return h.sdk.MakeDocumentPublic(context.Background(), documentID)
}

// findSDKShare returns the share at exactly the access level, or nil.
func findSDKShare(shares []sdkdocument.EnvironmentShare, access string) *sdkdocument.EnvironmentShare {
	for i := range shares {
		if shares[i].ExactAccess(access) {
			return &shares[i]
		}
	}
	return nil
}
