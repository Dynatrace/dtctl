// Package awsconnection implements CRUD operations for the Settings 2.0 schema
// builtin:hyperscaler-authentication.connections.aws (AWS authentication for
// Dynatrace data acquisition extensions, e.g. com.dynatrace.extension.da-aws).
package awsconnection

import (
	"encoding/json"
	"fmt"
	"net/url"
	"regexp"
	"strings"

	"github.com/Dynatrace/dtctl/pkg/client"
	"github.com/Dynatrace/dtctl/pkg/diagnostic"
	"github.com/Dynatrace/dtctl/sdk/httpclient"
)

const (
	SchemaID    = "builtin:hyperscaler-authentication.connections.aws"
	SettingsAPI = "/platform/classic/environment-api/v2/settings/objects"

	// listFields is the field projection a re-appliable export needs. The
	// Settings API list defaults to objectId + value only, which strips the
	// schemaId and scope that apply reads to tell a connection document from a
	// custom document. See https://github.com/Dynatrace/dtctl/issues/509
	listFields = "objectId,schemaId,schemaVersion,scope,value"

	// TypeRoleBased is the only auth type currently supported by dtctl.
	TypeRoleBased = "awsRoleBasedAuthentication"

	// DefaultConsumer matches the schema default for awsRoleBasedAuthentication.
	DefaultConsumer = "SVC:com.dynatrace.da"

	// Dynatrace AWS account IDs used as Principal in the IAM trust policy.
	// Sourced from the official monitoring role CloudFormation template
	// (da-aws-nested-monitoring-role.yaml) which switches based on the
	// 3rd segment of the tenant URL host (cProductionEnvironment condition).
	dynatraceAWSAccountIDProd    = "314146291599"
	dynatraceAWSAccountIDNonProd = "476114158034"
)

// roleArnRegex mirrors the constraint pattern from the schema for
// awsRoleBasedAuthentication.roleArn (allows empty string or AWS IAM role ARN).
var roleArnRegex = regexp.MustCompile(`^$|arn:aws:iam::(?:aws|\d+?):role\/.*$`)

// ValidateRoleArn returns nil if arn matches the schema pattern (or is empty).
func ValidateRoleArn(arn string) error {
	if roleArnRegex.MatchString(arn) {
		return nil
	}
	return fmt.Errorf("invalid AWS IAM role ARN %q (expected pattern: arn:aws:iam::<account-id>:role/<name>)", arn)
}

// DynatraceAWSAccountID returns the Dynatrace AWS account that should appear
// as the Principal in the IAM role trust policy for the given tenant base URL.
// The selection mirrors the cProductionEnvironment CloudFormation condition
// in da-aws-nested-monitoring-role.yaml (host segment[2] == "dynatrace").
func DynatraceAWSAccountID(baseURL string) string {
	if isProductionHost(baseURL) {
		return dynatraceAWSAccountIDProd
	}
	return dynatraceAWSAccountIDNonProd
}

func isProductionHost(baseURL string) bool {
	u, err := url.Parse(baseURL)
	if err != nil {
		return false
	}
	host := u.Host
	if host == "" {
		host = baseURL
	}
	parts := strings.Split(host, ".")
	if len(parts) < 3 {
		return false
	}
	return parts[2] == "dynatrace"
}

type Handler struct {
	client *client.Client
}

func NewHandler(c *client.Client) *Handler {
	return &Handler{client: c}
}

type AWSConnection struct {
	ObjectID      string `json:"objectId" table:"ID"`
	SchemaID      string `json:"schemaId,omitempty" table:"SCHEMA,wide"`
	SchemaVersion string `json:"schemaVersion,omitempty" table:"VERSION,wide"`
	Scope         string `json:"scope,omitempty" table:"-"`
	Author        string `json:"author,omitempty" table:"AUTHOR,wide"`
	Created       int64  `json:"created,omitempty" table:"-"`
	Modified      int64  `json:"modified,omitempty" table:"-"`
	Summary       string `json:"summary,omitempty" table:"SUMMARY,wide"`
	Value         Value  `json:"value" table:"-"`

	// Flattened fields for table view.
	Name    string `json:"name,omitempty" table:"NAME"`
	Type    string `json:"type,omitempty" table:"TYPE"`
	RoleArn string `json:"roleArn,omitempty" table:"ROLE_ARN"`
}

type Value struct {
	Name                       string                            `json:"name"`
	Type                       string                            `json:"type"`
	AwsRoleBasedAuthentication *AwsRoleBasedAuthenticationConfig `json:"awsRoleBasedAuthentication,omitempty"`
}

type AwsRoleBasedAuthenticationConfig struct {
	RoleArn   string   `json:"roleArn"`
	Consumers []string `json:"consumers"`
}

type ListResponse struct {
	Items       []AWSConnection `json:"items"`
	TotalCount  int             `json:"totalCount"`
	NextPageKey string          `json:"nextPageKey,omitempty"`
}

type AWSConnectionCreate struct {
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

func flattenConnection(item *AWSConnection) {
	item.Name = item.Value.Name
	item.Type = item.Value.Type
	if item.Value.AwsRoleBasedAuthentication != nil {
		item.RoleArn = item.Value.AwsRoleBasedAuthentication.RoleArn
	}
}

func (h *Handler) listBySchema(schemaID string) ([]AWSConnection, error) {
	var allItems []AWSConnection
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
					Operation:  "list aws_connections",
					Permission: "settings:objects:read",
					SchemaID:   schemaID,
					Body:       resp.String(),
				})
			}
			return nil, fmt.Errorf("failed to list aws_connections for schema %q: %s", schemaID, resp.String())
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

func (h *Handler) Get(id string) (*AWSConnection, error) {
	var result AWSConnection
	req := h.client.HTTP().R().SetResult(&result)
	resp, err := req.Get(fmt.Sprintf("%s/%s", SettingsAPI, httpclient.PathSegment(id)))
	if err != nil {
		return nil, err
	}
	if resp.IsError() {
		if resp.StatusCode() == 403 {
			return nil, diagnostic.SettingsForbidden(diagnostic.SettingsDenial{
				Operation:      fmt.Sprintf("get aws_connection %q", id),
				Permission:     "settings:objects:read",
				SchemaID:       SchemaID,
				Body:           resp.String(),
				ExistingObject: true,
			})
		}
		return nil, fmt.Errorf("failed to get aws_connection: %s", resp.String())
	}
	flattenConnection(&result)
	return &result, nil
}

func (h *Handler) List() ([]AWSConnection, error) {
	return h.listBySchema(SchemaID)
}

func (h *Handler) Delete(id string) error {
	resp, err := h.client.HTTP().R().Delete(fmt.Sprintf("%s/%s", SettingsAPI, httpclient.PathSegment(id)))
	if err != nil {
		return err
	}
	if resp.IsError() {
		if resp.StatusCode() == 403 {
			return diagnostic.SettingsForbidden(diagnostic.SettingsDenial{
				Operation:      fmt.Sprintf("delete aws_connection %q", id),
				Permission:     "settings:objects:write",
				SchemaID:       SchemaID,
				Body:           resp.String(),
				ExistingObject: true,
			})
		}
		return fmt.Errorf("failed to delete aws_connection: status %d: %s", resp.StatusCode(), resp.String())
	}
	return nil
}

func (h *Handler) FindByName(name string) (*AWSConnection, error) {
	items, err := h.List()
	if err != nil {
		return nil, err
	}
	for i := range items {
		if items[i].Name == name {
			return &items[i], nil
		}
	}
	return nil, fmt.Errorf("AWS connection with name %q not found", name)
}

func (h *Handler) Create(req AWSConnectionCreate) (*AWSConnection, error) {
	if req.SchemaID == "" {
		req.SchemaID = SchemaID
	}
	if req.Scope == "" {
		req.Scope = "environment"
	}
	if req.Value.Type == "" {
		req.Value.Type = TypeRoleBased
	}
	if req.Value.Type == TypeRoleBased {
		if req.Value.AwsRoleBasedAuthentication == nil {
			req.Value.AwsRoleBasedAuthentication = &AwsRoleBasedAuthenticationConfig{
				Consumers: []string{DefaultConsumer},
			}
		}
		if len(req.Value.AwsRoleBasedAuthentication.Consumers) == 0 {
			req.Value.AwsRoleBasedAuthentication.Consumers = []string{DefaultConsumer}
		}
		if err := ValidateRoleArn(req.Value.AwsRoleBasedAuthentication.RoleArn); err != nil {
			return nil, err
		}
	}

	body := []AWSConnectionCreate{req}
	resp, err := h.client.HTTP().R().SetBody(body).Post(SettingsAPI)
	if err != nil {
		return nil, fmt.Errorf("failed to create aws_connection: %w", err)
	}
	if resp.IsError() {
		switch resp.StatusCode() {
		case 400:
			return nil, fmt.Errorf("invalid aws_connection: %s", resp.String())
		case 403:
			return nil, diagnostic.SettingsForbidden(diagnostic.SettingsDenial{
				Operation:  "create aws_connection",
				Permission: "settings:objects:write",
				SchemaID:   req.SchemaID,
				Body:       resp.String(),
			})
		case 404:
			return nil, fmt.Errorf("schema %q not found", req.SchemaID)
		case 409:
			return nil, fmt.Errorf("aws_connection already exists or conflicts with existing connection")
		default:
			return nil, fmt.Errorf("failed to create aws_connection: status %d: %s", resp.StatusCode(), resp.String())
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

func (h *Handler) Update(objectID string, value Value) (*AWSConnection, error) {
	if value.Type == TypeRoleBased && value.AwsRoleBasedAuthentication != nil {
		if err := ValidateRoleArn(value.AwsRoleBasedAuthentication.RoleArn); err != nil {
			return nil, err
		}
	}

	// Verify object exists (and resolve any not-found errors) before PUT.
	if _, err := h.Get(objectID); err != nil {
		return nil, err
	}

	// Settings 2.0 does not use HTTP If-Match/ETag for optimistic locking;
	// schemaVersion is the schema's semver (e.g. "1.0.27"), not an object ETag.
	// Concurrency is enforced server-side and surfaces as 409/412 if it occurs.
	body := map[string]interface{}{"value": value}
	resp, err := h.client.HTTP().R().
		SetBody(body).
		Put(fmt.Sprintf("%s/%s", SettingsAPI, httpclient.PathSegment(objectID)))
	if err != nil {
		return nil, fmt.Errorf("failed to update aws_connection: %w", err)
	}
	if resp.IsError() {
		switch resp.StatusCode() {
		case 400:
			return nil, fmt.Errorf("invalid aws_connection: %s", resp.String())
		case 403:
			return nil, diagnostic.SettingsForbidden(diagnostic.SettingsDenial{
				Operation:      fmt.Sprintf("update aws_connection %q", objectID),
				Permission:     "settings:objects:write",
				SchemaID:       SchemaID,
				Body:           resp.String(),
				ExistingObject: true,
			})
		case 404:
			return nil, fmt.Errorf("aws_connection %q not found", objectID)
		case 409, 412:
			return nil, fmt.Errorf("aws_connection version conflict (connection was modified)")
		default:
			return nil, fmt.Errorf("failed to update aws_connection: status %d: %s", resp.StatusCode(), resp.String())
		}
	}
	return h.Get(objectID)
}
