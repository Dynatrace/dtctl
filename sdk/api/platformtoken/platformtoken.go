package platformtoken

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"

	"github.com/Dynatrace/dtctl/sdk/httpclient"
)

// Handler handles platform token resources via the Account Management API.
type Handler struct {
	client      *httpclient.Client
	accountUUID string
}

// NewHandler creates a new platform token handler.
func NewHandler(c *httpclient.Client, accountUUID string) *Handler {
	return &Handler{client: c, accountUUID: accountUUID}
}

// PlatformToken represents a Dynatrace platform token.
type PlatformToken struct {
	Name           string   `json:"name"`
	TokenID        string   `json:"tokenId"`
	Token          string   `json:"token,omitempty"` // secret, returned only on create
	AccountUUID    string   `json:"accountUuid,omitempty"`
	Scope          []string `json:"scope"`
	Resource       []string `json:"resource,omitempty"`
	Status         string   `json:"status,omitempty"`
	ExpirationDate string   `json:"expirationDate,omitempty"`
	CreatedAt      string   `json:"createdAt,omitempty"`
	Owner          string   `json:"owner,omitempty"`
}

// PlatformTokenListResponse wraps the list API page response.
type PlatformTokenListResponse struct {
	Total      int             `json:"total"`
	PageSize   int             `json:"pageSize"`
	PageNumber int             `json:"pageNumber"`
	Results    []PlatformToken `json:"results"`
}

// PlatformTokenCreate is the request body for creating a platform token.
type PlatformTokenCreate struct {
	Name           string   `json:"name"`
	UserUUID       string   `json:"userUuid"`
	Scope          []string `json:"scope"`
	Resource       []string `json:"resource,omitempty"`
	Tags           []string `json:"tags,omitempty"`
	ExpirationDate string   `json:"expirationDate"`
}

func (h *Handler) basePath() string {
	return fmt.Sprintf("/iam/v1/accounts/%s/platform-tokens", httpclient.PathSegment(h.accountUUID))
}

const listPageSize = 100

// List returns all platform tokens for the account.
func (h *Handler) List(ctx context.Context) ([]PlatformToken, error) {
	var all []PlatformToken
	for pageNum := 0; ; pageNum++ {
		req := h.client.HTTP().R().SetContext(ctx).
			SetQueryParam("pageSize", strconv.Itoa(listPageSize)).
			SetQueryParam("pageNumber", strconv.Itoa(pageNum))
		resp, err := req.Get(h.basePath())
		if err != nil {
			return nil, fmt.Errorf("list platform tokens: %w", err)
		}
		if err := httpclient.CheckResponse(resp); err != nil {
			return nil, fmt.Errorf("list platform tokens: %w", err)
		}
		var page PlatformTokenListResponse
		if err := json.Unmarshal(resp.Body(), &page); err != nil {
			return nil, fmt.Errorf("list platform tokens: parse response: %w", err)
		}
		all = append(all, page.Results...)
		if len(page.Results) == 0 || len(all) >= page.Total {
			break
		}
	}
	return all, nil
}

// Create creates a new platform token.
func (h *Handler) Create(ctx context.Context, req PlatformTokenCreate) (*PlatformToken, error) {
	resp, err := h.client.HTTP().R().SetContext(ctx).
		SetBody(req).
		Post(h.basePath())
	if err != nil {
		return nil, fmt.Errorf("create platform token: %w", err)
	}
	if err := httpclient.CheckResponse(resp); err != nil {
		return nil, fmt.Errorf("create platform token: %w", err)
	}
	var result PlatformToken
	if err := json.Unmarshal(resp.Body(), &result); err != nil {
		return nil, fmt.Errorf("create platform token: parse response: %w", err)
	}
	return &result, nil
}

// Revoke deletes (revokes) a platform token by ID.
func (h *Handler) Revoke(ctx context.Context, tokenID string) error {
	resp, err := h.client.HTTP().R().SetContext(ctx).
		Delete(fmt.Sprintf("%s/%s", h.basePath(), httpclient.PathSegment(tokenID)))
	if err != nil {
		return fmt.Errorf("revoke platform token: %w", err)
	}
	if err := httpclient.CheckResponse(resp); err != nil {
		return fmt.Errorf("revoke platform token %q: %w", tokenID, err)
	}
	return nil
}
