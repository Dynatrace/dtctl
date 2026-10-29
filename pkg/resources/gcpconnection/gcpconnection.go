package gcpconnection

import (
	"encoding/json"
	"errors"
	"fmt"

	"github.com/Dynatrace/dtctl/pkg/client"
	"github.com/Dynatrace/dtctl/pkg/diagnostic"
	"github.com/Dynatrace/dtctl/sdk/httpclient"
)

const (
	SchemaID          = "builtin:hyperscaler-authentication.connections.gcp"
	PrincipalSchemaID = "builtin:hyperscaler-authentication.connections.gcp-dynatrace-principal"

	SettingsAPI = "/platform/classic/environment-api/v2/settings/objects"

	// listFields is the field projection a re-appliable export needs. The
	// Settings API list defaults to objectId + value only, which strips the
	// schemaId and scope that apply reads to tell a connection document from a
	// custom document. See https://github.com/Dynatrace/dtctl/issues/509
	listFields = "objectId,schemaId,schemaVersion,scope,value"

	// TypeServiceAccountImpersonation is the only authentication type the GCP
	// connection schema accepts today, and the default dtctl fills in.
	TypeServiceAccountImpersonation = "serviceAccountImpersonation"
)

var ErrPrincipalNotFound = errors.New("gcp dynatrace principal not found")

type Handler struct {
	client *client.Client
}

func NewHandler(c *client.Client) *Handler {
	return &Handler{client: c}
}

type GCPConnection struct {
	ObjectID      string `json:"objectId" table:"ID"`
	SchemaID      string `json:"schemaId,omitempty" table:"SCHEMA,wide"`
	SchemaVersion string `json:"schemaVersion,omitempty" table:"VERSION,wide"`
	Scope         string `json:"scope,omitempty" table:"-"`
	Author        string `json:"author,omitempty" table:"AUTHOR,wide"`
	Created       int64  `json:"created,omitempty" table:"-"`
	Modified      int64  `json:"modified,omitempty" table:"-"`
	Summary       string `json:"summary,omitempty" table:"SUMMARY,wide"`
	Value         Value  `json:"value" table:"-"`

	Name             string `json:"name,omitempty" table:"NAME"`
	Type             string `json:"type,omitempty" table:"TYPE"`
	Principal        string `json:"principal,omitempty" table:"PRINCIPAL"`
	ServiceAccountID string `json:"serviceAccountId,omitempty" table:"SERVICE_ACCOUNT"`
}

type Value struct {
	Principal                   string                       `json:"principal,omitempty"`
	Name                        string                       `json:"name"`
	Type                        string                       `json:"type"`
	ServiceAccountImpersonation *ServiceAccountImpersonation `json:"serviceAccountImpersonation,omitempty"`
}

type ServiceAccountImpersonation struct {
	ServiceAccountID string   `json:"serviceAccountId,omitempty"`
	Consumers        []string `json:"consumers"`
}

type ListResponse struct {
	Items       []GCPConnection `json:"items"`
	TotalCount  int             `json:"totalCount"`
	NextPageKey string          `json:"nextPageKey,omitempty"`
}

type GCPConnectionCreate struct {
	SchemaID      string `json:"schemaId"`
	Scope         string `json:"scope"`
	Value         Value  `json:"value"`
	SchemaVersion string `json:"schemaVersion,omitempty"`
	ExternalID    string `json:"externalId,omitempty"`
}

type CreateResponse struct {
	ObjectID string `json:"objectId"`
	Code     int    `json:"code,omitempty"`
	Error    *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error,omitempty"`
}

func flattenConnection(item *GCPConnection) {
	item.Principal = item.Value.Principal
	item.Name = item.Value.Name
	if item.Name == "" && item.Value.Principal != "" {
		item.Name = item.Value.Principal
	}
	item.Type = item.Value.Type
	if item.Value.ServiceAccountImpersonation != nil {
		item.ServiceAccountID = item.Value.ServiceAccountImpersonation.ServiceAccountID
	}
}

func (h *Handler) listBySchema(schemaID string) ([]GCPConnection, error) {
	var allItems []GCPConnection
	nextPageKey := ""

	for {
		var result ListResponse
		req := h.client.HTTP().R().SetResult(&result)

		client.PaginationParams{
			Style:        client.PaginationSettingsAPI,
			PageKeyParam: "nextPageKey",
			NextPageKey:  nextPageKey,
			Filters:      map[string]string{"schemaIds": schemaID, "fields": listFields},
		}.Apply(req)

		resp, err := req.Get(SettingsAPI)
		if err != nil {
			return nil, err
		}
		if resp.IsError() {
			if resp.StatusCode() == 403 {
				return nil, diagnostic.SettingsForbidden(diagnostic.SettingsDenial{
					Operation:  "list gcp_connections",
					Permission: "settings:objects:read",
					SchemaID:   schemaID,
					Body:       resp.String(),
				})
			}
			return nil, fmt.Errorf("failed to list gcp_connections for schema %q: %s", schemaID, resp.String())
		}

		for i := range result.Items {
			flattenConnection(&result.Items[i])
		}
		allItems = append(allItems, result.Items...)

		if result.NextPageKey == "" {
			break
		}
		nextPageKey = result.NextPageKey
	}
	return allItems, nil
}

func (h *Handler) Get(id string) (*GCPConnection, error) {
	var result GCPConnection
	req := h.client.HTTP().R().SetResult(&result)
	resp, err := req.Get(fmt.Sprintf("%s/%s", SettingsAPI, httpclient.PathSegment(id)))
	if err != nil {
		return nil, err
	}
	if resp.IsError() {
		if resp.StatusCode() == 403 {
			return nil, diagnostic.SettingsForbidden(diagnostic.SettingsDenial{
				Operation:      fmt.Sprintf("get gcp_connection %q", id),
				Permission:     "settings:objects:read",
				SchemaID:       SchemaID,
				Body:           resp.String(),
				ExistingObject: true,
			})
		}
		return nil, fmt.Errorf("failed to get gcp_connection: %s", resp.String())
	}

	flattenConnection(&result)

	return &result, nil
}

func (h *Handler) List() ([]GCPConnection, error) {
	return h.listBySchema(SchemaID)
}

func (h *Handler) GetDynatracePrincipal() (*GCPConnection, error) {
	items, err := h.listBySchema(PrincipalSchemaID)
	if err != nil {
		return nil, fmt.Errorf("failed to get gcp dynatrace principal: %w", err)
	}
	if len(items) == 0 {
		return nil, ErrPrincipalNotFound
	}

	return &items[0], nil
}

func (h *Handler) Delete(id string) error {
	resp, err := h.client.HTTP().R().Delete(fmt.Sprintf("%s/%s", SettingsAPI, httpclient.PathSegment(id)))
	if err != nil {
		return err
	}
	if resp.IsError() {
		if resp.StatusCode() == 403 {
			return diagnostic.SettingsForbidden(diagnostic.SettingsDenial{
				Operation:      fmt.Sprintf("delete gcp_connection %q", id),
				Permission:     "settings:objects:write",
				SchemaID:       SchemaID,
				Body:           resp.String(),
				ExistingObject: true,
			})
		}
		return fmt.Errorf("failed to delete gcp_connection: status %d: %s", resp.StatusCode(), resp.String())
	}
	return nil
}

func (h *Handler) FindByName(name string) (*GCPConnection, error) {
	items, err := h.List()
	if err != nil {
		return nil, err
	}
	for i := range items {
		if items[i].Name == name {
			return &items[i], nil
		}
	}
	return nil, fmt.Errorf("GCP connection with name %q not found", name)
}

func (h *Handler) FindByNameAndType(name, typeVal string) (*GCPConnection, error) {
	items, err := h.List()
	if err != nil {
		return nil, err
	}
	for i := range items {
		if items[i].Name == name && items[i].Type == typeVal {
			return &items[i], nil
		}
	}
	return nil, nil
}

func (h *Handler) EnsureDynatracePrincipal() error {
	_, err := h.EnsureDynatracePrincipalWithResult()
	return err
}

func (h *Handler) EnsureDynatracePrincipalWithResult() (*GCPConnection, error) {
	principal, err := h.GetDynatracePrincipal()
	if err == nil {
		return principal, nil
	}
	if !errors.Is(err, ErrPrincipalNotFound) {
		return nil, err
	}

	body := []map[string]interface{}{
		{
			"schemaId": PrincipalSchemaID,
			"value":    map[string]interface{}{},
		},
	}

	createResp, err := h.client.HTTP().R().
		SetQueryParam("schemaIds", PrincipalSchemaID).
		SetBody(body).
		Post(SettingsAPI)
	if err != nil {
		return nil, fmt.Errorf("failed to create gcp dynatrace principal: %w", err)
	}
	if createResp.IsError() {
		return nil, fmt.Errorf("failed to create gcp dynatrace principal: status %d: %s", createResp.StatusCode(), createResp.String())
	}

	var created []CreateResponse
	if err := json.Unmarshal(createResp.Body(), &created); err == nil && len(created) > 0 && created[0].ObjectID != "" {
		item, getErr := h.Get(created[0].ObjectID)
		if getErr == nil {
			return item, nil
		}
	}

	createdPrincipal, err := h.GetDynatracePrincipal()
	if err != nil {
		return nil, err
	}

	return createdPrincipal, nil
}

func (h *Handler) Create(req GCPConnectionCreate) (*GCPConnection, error) {
	if req.SchemaID == "" {
		req.SchemaID = SchemaID
	}
	if req.Scope == "" {
		req.Scope = "environment"
	}
	if req.Value.Type == "" {
		req.Value.Type = "serviceAccountImpersonation"
	}

	body := []GCPConnectionCreate{req}
	resp, err := h.client.HTTP().R().SetBody(body).Post(SettingsAPI)
	if err != nil {
		return nil, fmt.Errorf("failed to create gcp_connection: %w", err)
	}
	if resp.IsError() {
		switch resp.StatusCode() {
		case 400:
			return nil, fmt.Errorf("invalid gcp_connection: %s", resp.String())
		case 403:
			return nil, diagnostic.SettingsForbidden(diagnostic.SettingsDenial{
				Operation:  "create gcp_connection",
				Permission: "settings:objects:write",
				SchemaID:   req.SchemaID,
				Body:       resp.String(),
			})
		case 404:
			return nil, fmt.Errorf("schema %q not found", req.SchemaID)
		case 409:
			return nil, fmt.Errorf("gcp_connection already exists or conflicts with existing connection")
		default:
			return nil, fmt.Errorf("failed to create gcp_connection: status %d: %s", resp.StatusCode(), resp.String())
		}
	}

	var createResp []CreateResponse
	if err := json.Unmarshal(resp.Body(), &createResp); err != nil {
		return nil, fmt.Errorf("failed to parse create response: %w", err)
	}
	if len(createResp) == 0 {
		return nil, fmt.Errorf("no items returned in create response")
	}
	if createResp[0].Error != nil {
		return nil, fmt.Errorf("create failed: %s", createResp[0].Error.Message)
	}

	return h.Get(createResp[0].ObjectID)
}

func (h *Handler) Update(objectID string, value Value) (*GCPConnection, error) {
	obj, err := h.Get(objectID)
	if err != nil {
		return nil, err
	}

	body := map[string]interface{}{"value": value}
	resp, err := h.client.HTTP().R().
		SetBody(body).
		SetHeader("If-Match", obj.SchemaVersion).
		Put(fmt.Sprintf("%s/%s", SettingsAPI, httpclient.PathSegment(objectID)))
	if err != nil {
		return nil, fmt.Errorf("failed to update gcp_connection: %w", err)
	}
	if resp.IsError() {
		switch resp.StatusCode() {
		case 400:
			return nil, fmt.Errorf("invalid gcp_connection: %s", resp.String())
		case 403:
			return nil, diagnostic.SettingsForbidden(diagnostic.SettingsDenial{
				Operation:      fmt.Sprintf("update gcp_connection %q", objectID),
				Permission:     "settings:objects:write",
				SchemaID:       obj.SchemaID,
				Body:           resp.String(),
				ExistingObject: true,
			})
		case 404:
			return nil, fmt.Errorf("gcp_connection %q not found", objectID)
		case 409, 412:
			return nil, fmt.Errorf("gcp_connection version conflict (connection was modified)")
		default:
			return nil, fmt.Errorf("failed to update gcp_connection: status %d: %s", resp.StatusCode(), resp.String())
		}
	}

	return h.Get(objectID)
}
