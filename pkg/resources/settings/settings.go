package settings

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/Dynatrace/dtctl/pkg/client"
	sdksettings "github.com/Dynatrace/dtctl/sdk/api/settings"
	"github.com/Dynatrace/dtctl/sdk/httpclient"
)

// Re-export SDK types that are identical (no table tags needed in CLI).
type (
	SettingsObjectCreate   = sdksettings.SettingsObjectCreate
	SettingsObjectResponse = sdksettings.SettingsObjectResponse
	CreateResponse         = sdksettings.CreateResponse
	ModificationInfo       = sdksettings.ModificationInfo
)

// Schema represents a settings schema (CLI version with table tags).
type Schema struct {
	SchemaID    string         `json:"schemaId" table:"SCHEMA_ID"`
	DisplayName string         `json:"displayName" table:"DISPLAY_NAME"`
	Description string         `json:"description,omitempty" table:"-"`
	Version     string         `json:"version" table:"VERSION"`
	MultiObject bool           `json:"multiObject,omitempty" table:"MULTI,wide"`
	Ordered     bool           `json:"ordered,omitempty" table:"ORDERED,wide"`
	Properties  map[string]any `json:"properties,omitempty" table:"-"`
	Scopes      []string       `json:"scopes,omitempty" table:"-"`
}

// SchemaList represents a list of schemas.
type SchemaList struct {
	Items      []Schema `json:"items"`
	TotalCount int      `json:"totalCount"`
}

// SettingsObject represents a settings object (CLI version with display fields and table tags).
type SettingsObject struct {
	ObjectID         string            `json:"objectId" table:"OBJECT_ID,wide"`
	SchemaID         string            `json:"schemaId" table:"SCHEMA_ID"`
	SchemaVersion    string            `json:"schemaVersion,omitempty" table:"VERSION,wide"`
	Scope            string            `json:"scope" table:"SCOPE,wide"`
	ExternalID       string            `json:"externalId,omitempty" table:"-"`
	Summary          string            `json:"summary,omitempty" table:"SUMMARY"`
	Value            map[string]any    `json:"value,omitempty" table:"-"`
	ModificationInfo *ModificationInfo `json:"modificationInfo,omitempty" table:"-"`

	// Display fields (computed, not from API)
	ObjectIDShort string `json:"-" yaml:"-" table:"OBJECT_ID_SHORT"`
	ScopeType     string `json:"-" yaml:"-" table:"SCOPE_TYPE,wide"`
	ScopeID       string `json:"-" yaml:"-" table:"SCOPE_ID,wide"`
}

// populateDisplayFields computes ObjectIDShort from ObjectID and parses
// ScopeType / ScopeID from the Scope field.
// Scope format: "<TYPE>-<ID>" for entity scopes, bare type name for singletons.
func (s *SettingsObject) populateDisplayFields() {
	if len(s.ObjectID) > 23 {
		s.ObjectIDShort = s.ObjectID[:20] + "..."
	} else {
		s.ObjectIDShort = s.ObjectID
	}

	if s.Scope != "" {
		parts := strings.SplitN(s.Scope, "-", 2)
		s.ScopeType = parts[0]
		if len(parts) == 2 {
			s.ScopeID = parts[1]
		}
	}
}

// settingsObjectFromSDK converts an SDK SettingsObject to a CLI SettingsObject with display fields.
func settingsObjectFromSDK(obj *sdksettings.SettingsObject) *SettingsObject {
	result := &SettingsObject{
		ObjectID:         obj.ObjectID,
		SchemaID:         obj.SchemaID,
		SchemaVersion:    obj.SchemaVersion,
		Scope:            obj.Scope,
		ExternalID:       obj.ExternalID,
		Summary:          obj.Summary,
		Value:            obj.Value,
		ModificationInfo: obj.ModificationInfo,
	}
	result.populateDisplayFields()
	return result
}

// SettingsObjectsList represents a list of settings objects (CLI version using CLI SettingsObject).
type SettingsObjectsList struct {
	Items       []SettingsObject `json:"items"`
	TotalCount  int              `json:"totalCount"`
	NextPageKey string           `json:"nextPageKey,omitempty"`
}

// Handler handles settings resources.
// It delegates to the SDK handler and adds CLI-specific convenience methods.
type Handler struct {
	sdk *sdksettings.Handler
}

// NewHandler creates a new settings handler.
func NewHandler(c *client.Client) *Handler {
	return &Handler{
		sdk: sdksettings.NewHandler(httpclient.Wrap(c.HTTP())),
	}
}

// ListSchemas lists all available settings schemas.
func (h *Handler) ListSchemas() (*SchemaList, error) {
	sdkResult, err := h.sdk.ListSchemas(context.Background())
	if err != nil {
		return nil, err
	}

	// Convert SDK schemas to CLI schemas (with table tags).
	items := make([]Schema, len(sdkResult.Items))
	for i, s := range sdkResult.Items {
		items[i] = Schema{
			SchemaID:    s.SchemaID,
			DisplayName: s.DisplayName,
			Description: s.Description,
			Version:     s.Version,
			MultiObject: s.MultiObject,
			Ordered:     s.Ordered,
			Properties:  s.Properties,
			Scopes:      s.Scopes,
		}
	}

	return &SchemaList{
		Items:      items,
		TotalCount: sdkResult.TotalCount,
	}, nil
}

// GetSchema gets a specific schema definition.
func (h *Handler) GetSchema(schemaID string) (map[string]any, error) {
	return h.sdk.GetSchema(context.Background(), schemaID)
}

// ListObjects lists settings objects for a schema with automatic pagination.
func (h *Handler) ListObjects(schemaID, scope string, chunkSize int64) (*SettingsObjectsList, error) {
	sdkResult, err := h.sdk.ListObjects(context.Background(), schemaID, scope, chunkSize)
	if err != nil {
		return nil, err
	}

	items := make([]SettingsObject, len(sdkResult.Items))
	for i := range sdkResult.Items {
		obj := settingsObjectFromSDK(&sdkResult.Items[i])
		// API bug workaround: The API returns empty schemaId field
		if schemaID != "" && obj.SchemaID == "" {
			obj.SchemaID = schemaID
		}
		items[i] = *obj
	}

	return &SettingsObjectsList{
		Items:       items,
		TotalCount:  sdkResult.TotalCount,
		NextPageKey: sdkResult.NextPageKey,
	}, nil
}

// Get gets a specific settings object by objectId.
func (h *Handler) Get(objectID string) (*SettingsObject, error) {
	sdkObj, err := h.sdk.Get(context.Background(), objectID)
	if err != nil {
		return nil, err
	}
	return settingsObjectFromSDK(sdkObj), nil
}

// ValidateCreate validates a settings object without creating it.
func (h *Handler) ValidateCreate(req SettingsObjectCreate) error {
	return h.sdk.ValidateCreate(context.Background(), req)
}

// Create creates a new settings object.
func (h *Handler) Create(req SettingsObjectCreate) (*SettingsObjectResponse, error) {
	return h.sdk.Create(context.Background(), req)
}

// ValidateUpdate validates a settings object update without applying it.
// Auto-fetches the current schemaVersion for the If-Match header.
func (h *Handler) ValidateUpdate(objectID string, value map[string]any) error {
	obj, err := h.sdk.Get(context.Background(), objectID)
	if err != nil {
		return err
	}
	return h.sdk.ValidateUpdate(context.Background(), objectID, obj.SchemaVersion, value)
}

// Update updates an existing settings object.
// Auto-fetches the current schemaVersion for the If-Match header, then re-fetches.
func (h *Handler) Update(objectID string, value map[string]any) (*SettingsObject, error) {
	obj, err := h.sdk.Get(context.Background(), objectID)
	if err != nil {
		return nil, err
	}

	if err := h.sdk.Update(context.Background(), obj.ObjectID, obj.SchemaVersion, value); err != nil {
		return nil, err
	}

	return h.Get(obj.ObjectID)
}

// Delete deletes a settings object.
// Auto-fetches the current schemaVersion for the If-Match header.
func (h *Handler) Delete(objectID string) error {
	obj, err := h.sdk.Get(context.Background(), objectID)
	if err != nil {
		return err
	}
	return h.sdk.Delete(context.Background(), obj.ObjectID, obj.SchemaVersion)
}

// GetRaw gets a settings object as raw JSON bytes (for editing).
func (h *Handler) GetRaw(objectID string) ([]byte, error) {
	obj, err := h.Get(objectID)
	if err != nil {
		return nil, err
	}
	return json.MarshalIndent(obj.Value, "", "  ")
}
