// Package credentialvault wraps the Dynatrace Credential vault API
// (/platform/credential-vault/v1/entries).
//
// The read model (Credential) carries metadata only: the API never returns
// secret material (passwords, tokens, private keys) on GET, and this package
// does not add any. Secret fields exist only on the write model
// (CredentialRequest).
package credentialvault

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/Dynatrace/dtctl/sdk/httpclient"
)

const basePath = "/platform/credential-vault/v1/entries"

// Credential types accepted by the API (the `type` discriminator).
const (
	TypeAWSKeyBased       = "AWS_MONITORING_KEY_BASED"
	TypeAWSRoleBased      = "AWS_MONITORING_ROLE_BASED"
	TypeCertificate       = "CERTIFICATE"
	TypePublicCertificate = "PUBLIC_CERTIFICATE"
	TypeSNMPV3            = "SNMPV3"
	TypeToken             = "TOKEN"
	TypeUsernamePassword  = "USERNAME_PASSWORD"
)

// Credential scopes (the `scopes` array).
const (
	ScopeAppEngine               = "APP_ENGINE"
	ScopeExtension               = "EXTENSION"
	ScopeExtensionAuthentication = "EXTENSION_AUTHENTICATION"
	ScopeSynthetic               = "SYNTHETIC"
)

// Certificate formats (the `certificateFormat` field).
const (
	CertificateFormatPEM    = "PEM"
	CertificateFormatPKCS12 = "PKCS12"
)

// Handler handles Credential vault resources.
type Handler struct {
	client *httpclient.Client
}

// NewHandler creates a new Credential vault handler.
func NewHandler(c *httpclient.Client) *Handler {
	return &Handler{client: c}
}

// AccessData names an entity allowed to use a credential.
type AccessData struct {
	ID   string `json:"id,omitempty"`
	Type string `json:"type,omitempty"` // APPLICATION, USER, UNKNOWN
}

// Credential is the metadata of a credentials set as returned by GET.
// It never contains secret material.
type Credential struct {
	ID                       string           `json:"id,omitempty"`
	Name                     string           `json:"name"`
	Type                     string           `json:"type"`
	Description              string           `json:"description,omitempty"`
	Owner                    string           `json:"owner,omitempty"`
	OwnerAccessOnly          bool             `json:"ownerAccessOnly"`
	Scope                    string           `json:"scope,omitempty"` // deprecated by the API; prefer Scopes
	Scopes                   []string         `json:"scopes,omitempty"`
	AllowContextlessRequests bool             `json:"allowContextlessRequests,omitempty"`
	AllowedEntities          []AccessData     `json:"allowedEntities,omitempty"`
	CredentialUsageSummary   []map[string]any `json:"credentialUsageSummary,omitempty"`
}

// CredentialList is one page (or, after List, all pages) of credentials.
type CredentialList struct {
	Credentials []Credential `json:"credentials"`
	NextPageKey string       `json:"nextPageKey,omitempty"`
	PageSize    int          `json:"pageSize,omitempty"`
	TotalCount  int          `json:"totalCount"`
}

// CredentialRequest is the body for Create and Update. Which secret fields
// apply depends on Type:
//
//   - CERTIFICATE: Certificate (file bytes, base64), CertificateFormat,
//     Password (base64; empty string for PEM)
//   - PUBLIC_CERTIFICATE: Certificate (PEM text), CertificateFormat
//   - TOKEN: Token
//   - USERNAME_PASSWORD: User, Password
//
// Pointer fields are omitted when nil so that an empty string can still be
// sent deliberately (the API requires `password: ""` for PEM certificates).
type CredentialRequest struct {
	Name                     string       `json:"name"`
	Type                     string       `json:"type"`
	Description              string       `json:"description,omitempty"`
	OwnerAccessOnly          bool         `json:"ownerAccessOnly"`
	Scopes                   []string     `json:"scopes"`
	AllowContextlessRequests *bool        `json:"allowContextlessRequests,omitempty"`
	AllowedEntities          []AccessData `json:"allowedEntities,omitempty"`

	Certificate       *string `json:"certificate,omitempty"`
	CertificateFormat *string `json:"certificateFormat,omitempty"`
	Password          *string `json:"password,omitempty"`
	Token             *string `json:"token,omitempty"`
	User              *string `json:"user,omitempty"`
}

// ListOptions filters List. All fields are optional.
type ListOptions struct {
	// Type filters by credential type (e.g. PUBLIC_CERTIFICATE).
	Type string
	// Name filters by name (case-insensitive; quoted phrases match whole).
	Name string
	// Scope filters by credential scope (e.g. EXTENSION_AUTHENTICATION).
	Scope string
	// PageSize is the number of credentials per request (max 500). Zero uses
	// the API default.
	PageSize int64
}

// List lists credentials, following nextPageKey until all pages are read.
//
// Filters and page-size go on the first request only: the page token embeds
// them, and the API rejects any of them alongside page-key with HTTP 400
// (verified against the live API across pages). The server-side type filter does not accept PUBLIC_CERTIFICATE, so that
// type is filtered here instead.
func (h *Handler) List(ctx context.Context, opts ListOptions) (*CredentialList, error) {
	var all []Credential
	totalCount := 0
	nextPageKey := ""
	serverType, clientType := opts.Type, ""
	if opts.Type == TypePublicCertificate {
		serverType, clientType = "", opts.Type
	}

	for {
		params := httpclient.PaginationParams{
			Style:         httpclient.PaginationSettingsAPI,
			PageKeyParam:  "page-key",
			PageSizeParam: "page-size",
			NextPageKey:   nextPageKey,
			PageSize:      opts.PageSize,
			Filters:       map[string]string{"type": serverType, "name": opts.Name, "scope": opts.Scope},
		}.QueryParams()

		resp, err := h.client.HTTP().R().SetContext(ctx).
			SetQueryParamsFromValues(params).
			Get(basePath)
		if err != nil {
			return nil, fmt.Errorf("list credentials: %w", err)
		}
		if err := httpclient.CheckResponse(resp); err != nil {
			return nil, fmt.Errorf("list credentials: %w", err)
		}

		var page CredentialList
		if err := json.Unmarshal(resp.Body(), &page); err != nil {
			return nil, fmt.Errorf("list credentials: parse response: %w", err)
		}

		for _, c := range page.Credentials {
			if clientType == "" || c.Type == clientType {
				all = append(all, c)
			}
		}
		totalCount = page.TotalCount

		if page.NextPageKey == "" {
			break
		}
		nextPageKey = page.NextPageKey
	}

	if all == nil {
		all = []Credential{}
	}
	if clientType != "" {
		totalCount = len(all)
	}
	return &CredentialList{Credentials: all, TotalCount: totalCount}, nil
}

// Get gets the metadata of a credentials set by ID.
func (h *Handler) Get(ctx context.Context, id string) (*Credential, error) {
	resp, err := h.client.HTTP().R().SetContext(ctx).
		Get(basePath + "/" + httpclient.PathSegment(id))
	if err != nil {
		return nil, fmt.Errorf("get credential: %w", err)
	}
	if err := httpclient.CheckResponse(resp); err != nil {
		return nil, fmt.Errorf("get credential %q: %w", id, err)
	}

	var result Credential
	if err := json.Unmarshal(resp.Body(), &result); err != nil {
		return nil, fmt.Errorf("get credential: parse response: %w", err)
	}
	return &result, nil
}

// Create creates a credentials set and returns its ID. The API responds with
// the ID only, not the full object.
func (h *Handler) Create(ctx context.Context, req CredentialRequest) (string, error) {
	resp, err := h.client.HTTP().R().SetContext(ctx).
		SetBody(req).
		Post(basePath)
	if err != nil {
		return "", fmt.Errorf("create credential: %w", err)
	}
	if err := httpclient.CheckResponse(resp); err != nil {
		return "", fmt.Errorf("create credential: %w", err)
	}

	var result struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(resp.Body(), &result); err != nil {
		return "", fmt.Errorf("create credential: parse response: %w", err)
	}
	if result.ID == "" {
		return "", fmt.Errorf("create credential: response did not include an id")
	}
	return result.ID, nil
}

// Update replaces a credentials set. The request must carry the full secret
// material again; GET never returns it.
func (h *Handler) Update(ctx context.Context, id string, req CredentialRequest) error {
	resp, err := h.client.HTTP().R().SetContext(ctx).
		SetBody(req).
		Put(basePath + "/" + httpclient.PathSegment(id))
	if err != nil {
		return fmt.Errorf("update credential: %w", err)
	}
	if err := httpclient.CheckResponse(resp); err != nil {
		return fmt.Errorf("update credential %q: %w", id, err)
	}
	return nil
}

// Delete deletes a credentials set.
func (h *Handler) Delete(ctx context.Context, id string) error {
	resp, err := h.client.HTTP().R().SetContext(ctx).
		Delete(basePath + "/" + httpclient.PathSegment(id))
	if err != nil {
		return fmt.Errorf("delete credential: %w", err)
	}
	if err := httpclient.CheckResponse(resp); err != nil {
		return fmt.Errorf("delete credential %q: %w", id, err)
	}
	return nil
}
