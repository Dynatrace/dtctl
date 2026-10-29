package azureconnection

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/Dynatrace/dtctl/pkg/client"
	"github.com/Dynatrace/dtctl/pkg/diagnostic"
	"github.com/Dynatrace/dtctl/sdk/httpclient"
)

const (
	SchemaID    = "builtin:hyperscaler-authentication.connections.azure"
	SettingsAPI = "/platform/classic/environment-api/v2/settings/objects"

	// listFields is the field projection a re-appliable export needs. The
	// Settings API list defaults to objectId + value only, which strips the
	// schemaId and scope that apply reads to tell a connection document from a
	// custom document. See https://github.com/Dynatrace/dtctl/issues/509
	listFields = "objectId,schemaId,schemaVersion,scope,value"

	// The authentication types the Azure connection schema accepts.
	TypeFederatedIdentityCredential = "federatedIdentityCredential"
	TypeClientSecret                = "clientSecret"
)

// TokenIssuerForHost returns the Dynatrace token issuer URL for a given tenant host.
// Used to construct Azure federated identity credential configuration.
// For non-production environments not covered by auto-detection, pass an explicit
// issuer via the YAML top-level "issuer" field or --issuer flag.
func TokenIssuerForHost(host string) string {
	switch {
	case strings.Contains(host, "dev.apps.dynatracelabs.com"), strings.Contains(host, "dev.dynatracelabs.com"):
		return "https://dev.token.dynatracelabs.com"
	default:
		return "https://token.dynatrace.com"
	}
}

type Handler struct {
	client *client.Client
}

func NewHandler(c *client.Client) *Handler {
	return &Handler{client: c}
}

type AzureConnection struct {
	ObjectID      string `json:"objectId" table:"ID"`
	SchemaID      string `json:"schemaId,omitempty" table:"SCHEMA,wide"`
	SchemaVersion string `json:"schemaVersion,omitempty" table:"VERSION,wide"`
	Scope         string `json:"scope,omitempty" table:"-"`
	Author        string `json:"author,omitempty" table:"AUTHOR,wide"`
	Created       int64  `json:"created,omitempty" table:"-"`
	Modified      int64  `json:"modified,omitempty" table:"-"`
	Summary       string `json:"summary,omitempty" table:"SUMMARY,wide"`
	Value         Value  `json:"value" table:"-"`

	// Flattened fields for table view
	Name string `json:"name,omitempty" table:"NAME"`
	Type string `json:"type,omitempty" table:"TYPE"`
}

type Value struct {
	Name                        string                       `json:"name"`
	Type                        string                       `json:"type"`
	ClientSecret                *ClientSecretCredential      `json:"clientSecret,omitempty"`
	FederatedIdentityCredential *FederatedIdentityCredential `json:"federatedIdentityCredential,omitempty"`
}

type ClientSecretCredential struct {
	ApplicationID string   `json:"applicationId"`
	DirectoryID   string   `json:"directoryId"`
	ClientSecret  string   `json:"clientSecret,omitempty"` // Often masked in responses
	Consumers     []string `json:"consumers"`
}

type FederatedIdentityCredential struct {
	DirectoryID   string   `json:"directoryId,omitempty"`
	ApplicationID string   `json:"applicationId,omitempty"`
	Consumers     []string `json:"consumers"`
}

func (v Value) String() string {
	s := fmt.Sprintf("name=%s type=%s", v.Name, v.Type)

	if v.ClientSecret != nil {
		// Mask the secret to prevent leaking it in terminal/logs
		secret := ""
		if v.ClientSecret.ClientSecret != "" {
			secret = "[REDACTED]"
		}
		s += fmt.Sprintf(" dirId=%s appId=%s secret=%s consumers=%v",
			v.ClientSecret.DirectoryID,
			v.ClientSecret.ApplicationID,
			secret,
			v.ClientSecret.Consumers)
	}

	if v.FederatedIdentityCredential != nil {
		s += fmt.Sprintf(" consumers=%v", v.FederatedIdentityCredential.Consumers)
	}

	return s
}

type ListResponse struct {
	Items       []AzureConnection `json:"items"`
	TotalCount  int               `json:"totalCount"`
	NextPageKey string            `json:"nextPageKey,omitempty"`
}

func (h *Handler) Get(id string) (*AzureConnection, error) {
	var result AzureConnection
	req := h.client.HTTP().R().SetResult(&result)
	resp, err := req.Get(fmt.Sprintf("%s/%s", SettingsAPI, httpclient.PathSegment(id)))
	if err != nil {
		return nil, err
	}
	if resp.IsError() {
		if resp.StatusCode() == 403 {
			return nil, diagnostic.SettingsForbidden(diagnostic.SettingsDenial{
				Operation:      fmt.Sprintf("get azure_connection %q", id),
				Permission:     "settings:objects:read",
				SchemaID:       SchemaID,
				Body:           resp.String(),
				ExistingObject: true,
			})
		}
		return nil, fmt.Errorf("failed to get azure_connection: %s", resp.String())
	}

	// Populate flattened fields
	result.Name = result.Value.Name
	result.Type = result.Value.Type

	return &result, nil
}

func (h *Handler) List() ([]AzureConnection, error) {
	var allItems []AzureConnection
	nextPageKey := ""

	for {
		var result ListResponse
		req := h.client.HTTP().R().SetResult(&result)

		client.PaginationParams{
			Style:        client.PaginationSettingsAPI,
			PageKeyParam: "nextPageKey",
			NextPageKey:  nextPageKey,
			Filters:      map[string]string{"schemaIds": SchemaID, "fields": listFields},
		}.Apply(req)

		resp, err := req.Get(SettingsAPI)
		if err != nil {
			return nil, err
		}
		if resp.IsError() {
			if resp.StatusCode() == 403 {
				return nil, diagnostic.SettingsForbidden(diagnostic.SettingsDenial{
					Operation:  "list azure_connections",
					Permission: "settings:objects:read",
					SchemaID:   SchemaID,
					Body:       resp.String(),
				})
			}
			return nil, fmt.Errorf("failed to list azure_connections: %s", resp.String())
		}

		for i := range result.Items {
			result.Items[i].Name = result.Items[i].Value.Name
			result.Items[i].Type = result.Items[i].Value.Type
		}
		allItems = append(allItems, result.Items...)

		if result.NextPageKey == "" {
			break
		}
		nextPageKey = result.NextPageKey
	}
	return allItems, nil
}

// Delete deletes an Azure connection by ID
func (h *Handler) Delete(id string) error {
	resp, err := h.client.HTTP().R().Delete(fmt.Sprintf("%s/%s", SettingsAPI, httpclient.PathSegment(id)))
	if err != nil {
		return err
	}
	if resp.IsError() {
		if resp.StatusCode() == 403 {
			return diagnostic.SettingsForbidden(diagnostic.SettingsDenial{
				Operation:      fmt.Sprintf("delete azure_connection %q", id),
				Permission:     "settings:objects:write",
				SchemaID:       SchemaID,
				Body:           resp.String(),
				ExistingObject: true,
			})
		}
		return fmt.Errorf("failed to delete azure_connection: status %d: %s", resp.StatusCode(), resp.String())
	}
	return nil
}

// FindByName finds an Azure connection by name
func (h *Handler) FindByName(name string) (*AzureConnection, error) {
	items, err := h.List()
	if err != nil {
		return nil, err
	}
	for i := range items {
		if items[i].Name == name {
			return &items[i], nil
		}
	}
	return nil, fmt.Errorf("azure connection with name %q not found", name)
}

// FindByNameAndType finds an Azure connection by name and type
func (h *Handler) FindByNameAndType(name, typeVal string) (*AzureConnection, error) {
	items, err := h.List()
	if err != nil {
		return nil, err
	}
	for i := range items {
		if items[i].Name == name && items[i].Type == typeVal {
			return &items[i], nil
		}
	}
	return nil, nil // Return nil if not found, distinct from error
}

// AzureConnectionCreate represents the request body for created Azure connection
type AzureConnectionCreate struct {
	SchemaID      string `json:"schemaId"`
	Scope         string `json:"scope"`
	Value         Value  `json:"value"`
	SchemaVersion string `json:"schemaVersion,omitempty"`
	ExternalID    string `json:"externalId,omitempty"`
}

// CreateResponse represents the response from creating an Azure connection
type CreateResponse struct {
	ObjectID string `json:"objectId"`
	Code     int    `json:"code,omitempty"`
	Error    *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error,omitempty"`
}

// Create creates a new Azure connection
func (h *Handler) Create(req AzureConnectionCreate) (*AzureConnection, error) {
	// Ensure schemaId is set to the correct value
	if req.SchemaID == "" {
		req.SchemaID = SchemaID
	}

	// Default scope to environment if not provided
	if req.Scope == "" {
		req.Scope = "environment"
	}

	// Wrap in array for v2 API
	body := []AzureConnectionCreate{req}

	resp, err := h.client.HTTP().R().
		SetBody(body).
		Post(SettingsAPI)

	if err != nil {
		return nil, fmt.Errorf("failed to create azure_connection: %w", err)
	}

	if resp.IsError() {
		switch resp.StatusCode() {
		case 400:
			return nil, fmt.Errorf("invalid azure_connection: %s", resp.String())
		case 403:
			return nil, diagnostic.SettingsForbidden(diagnostic.SettingsDenial{
				Operation:  "create azure_connection",
				Permission: "settings:objects:write",
				SchemaID:   req.SchemaID,
				Body:       resp.String(),
			})
		case 404:
			return nil, fmt.Errorf("schema %q not found", req.SchemaID)
		case 409:
			return nil, fmt.Errorf("azure_connection already exists or conflicts with existing connection")
		default:
			return nil, fmt.Errorf("failed to create azure_connection: status %d: %s", resp.StatusCode(), resp.String())
		}
	}

	var createResp []CreateResponse
	if err := json.Unmarshal(resp.Body(), &createResp); err != nil {
		return nil, fmt.Errorf("failed to parse create response: %w", err)
	}

	if len(createResp) == 0 {
		return nil, fmt.Errorf("no items returned in create response")
	}

	result := &createResp[0]
	if result.Error != nil {
		return nil, fmt.Errorf("create failed: %s", result.Error.Message)
	}

	// Fetch and return the created connection
	return h.Get(result.ObjectID)
}

// Update updates an existing Azure connection
func (h *Handler) Update(objectID string, value Value) (*AzureConnection, error) {
	// First get current object to obtain version
	obj, err := h.Get(objectID)
	if err != nil {
		return nil, err
	}

	body := map[string]interface{}{
		"value": value,
	}

	resp, err := h.client.HTTP().R().
		SetBody(body).
		SetHeader("If-Match", obj.SchemaVersion).
		Put(fmt.Sprintf("%s/%s", SettingsAPI, httpclient.PathSegment(objectID)))

	if err != nil {
		return nil, fmt.Errorf("failed to update azure_connection: %w", err)
	}

	if resp.IsError() {
		switch resp.StatusCode() {
		case 400:
			return nil, fmt.Errorf("invalid azure_connection: %s", resp.String())
		case 403:
			return nil, diagnostic.SettingsForbidden(diagnostic.SettingsDenial{
				Operation:      fmt.Sprintf("update azure_connection %q", objectID),
				Permission:     "settings:objects:write",
				SchemaID:       SchemaID,
				Body:           resp.String(),
				ExistingObject: true,
			})
		case 404:
			return nil, fmt.Errorf("azure_connection %q not found", objectID)
		case 409, 412:
			return nil, fmt.Errorf("azure_connection version conflict (connection was modified)")
		default:
			return nil, fmt.Errorf("failed to update azure_connection: status %d: %s", resp.StatusCode(), resp.String())
		}
	}

	// Fetch and return the updated connection
	return h.Get(objectID)
}
