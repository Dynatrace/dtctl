package serviceuser

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strconv"

	"github.com/Dynatrace/dtctl/sdk/httpclient"
)

const listPageSize = 100

// Handler handles service users through the Account Management API.
type Handler struct {
	client      *httpclient.Client
	accountUUID string
}

// NewHandler creates a service-user handler for an account.
func NewHandler(client *httpclient.Client, accountUUID string) *Handler {
	return &Handler{client: client, accountUUID: accountUUID}
}

// ServiceUser represents a Dynatrace account service user.
type ServiceUser struct {
	UID         string `json:"uid"`
	Email       string `json:"email"`
	Name        string `json:"name"`
	Surname     string `json:"surname"`
	Description string `json:"description"`
	CreatedAt   string `json:"createdAt"`
	GroupUUID   string `json:"groupUuid,omitempty"`
}

// ServiceUserCreate is the request body for creating a service user.
type ServiceUserCreate struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
}

// ServiceUserListResponse is one page of service users.
type ServiceUserListResponse struct {
	Results     []ServiceUser `json:"results"`
	NextPageKey string        `json:"nextPageKey"`
	TotalCount  int           `json:"totalCount"`
}

func (h *Handler) basePath() string {
	return fmt.Sprintf("/iam/v1/accounts/%s/service-users", httpclient.PathSegment(h.accountUUID))
}

// List returns all service users in the account.
func (h *Handler) List(ctx context.Context) ([]ServiceUser, error) {
	var all []ServiceUser
	var nextPageKey string

	for {
		req := h.client.HTTP().R().SetContext(ctx)
		if nextPageKey != "" {
			req.SetQueryParam("page-key", nextPageKey)
		} else {
			req.SetQueryParam("page-size", strconv.Itoa(listPageSize))
		}

		resp, err := req.Get(h.basePath())
		if err != nil {
			return nil, fmt.Errorf("list service users: %w", err)
		}
		if err := httpclient.CheckResponse(resp); err != nil {
			return nil, fmt.Errorf("list service users: %w", err)
		}

		var page *ServiceUserListResponse
		if err := json.Unmarshal(resp.Body(), &page); err != nil {
			return nil, fmt.Errorf("list service users: parse response: %w", err)
		}
		if page == nil {
			return nil, fmt.Errorf("list service users: parse response: expected list response object, got null")
		}
		all = append(all, page.Results...)
		if page.NextPageKey == "" {
			return all, nil
		}
		nextPageKey = page.NextPageKey
	}
}

// Create creates a service user and returns its identity metadata.
func (h *Handler) Create(ctx context.Context, req ServiceUserCreate) (*ServiceUser, error) {
	resp, err := h.client.HTTP().R().SetContext(ctx).
		SetBody(req).
		Post(h.basePath())
	if err != nil {
		return nil, fmt.Errorf("create service user: %w", err)
	}
	if err := httpclient.CheckResponse(resp); err != nil {
		return nil, fmt.Errorf("create service user: %w", err)
	}

	result, err := decodeCreateResponse(resp.Body())
	if err != nil {
		return nil, fmt.Errorf("create service user: parse response: %w", err)
	}
	return result, nil
}

func decodeCreateResponse(body []byte) (*ServiceUser, error) {
	trimmed := bytes.TrimSpace(body)
	if len(trimmed) == 0 {
		return nil, fmt.Errorf("empty response")
	}
	if trimmed[0] != '[' {
		var result *ServiceUser
		if err := json.Unmarshal(trimmed, &result); err != nil {
			return nil, err
		}
		if result == nil {
			return nil, fmt.Errorf("expected service-user object, got null")
		}
		return result, nil
	}

	var results []*ServiceUser
	if err := json.Unmarshal(trimmed, &results); err != nil {
		return nil, err
	}
	if len(results) != 1 {
		return nil, fmt.Errorf("expected one service user, got %d", len(results))
	}
	if results[0] == nil {
		return nil, fmt.Errorf("expected service-user object, got null")
	}
	return results[0], nil
}

// Delete deletes a service user by UUID.
func (h *Handler) Delete(ctx context.Context, userUUID string) error {
	resp, err := h.client.HTTP().R().SetContext(ctx).
		Delete(fmt.Sprintf("%s/%s", h.basePath(), httpclient.PathSegment(userUUID)))
	if err != nil {
		return fmt.Errorf("delete service user %q: %w", userUUID, err)
	}
	if err := httpclient.CheckResponse(resp); err != nil {
		return fmt.Errorf("delete service user %q: %w", userUUID, err)
	}
	return nil
}
