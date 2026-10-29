package iam

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/Dynatrace/dtctl/sdk/httpclient"
)

// Handler handles IAM resources.
type Handler struct {
	client *httpclient.Client
}

// NewHandler creates a new IAM handler.
func NewHandler(c *httpclient.Client) *Handler {
	return &Handler{client: c}
}

// User represents a Dynatrace user.
type User struct {
	UID         string `json:"uid"`
	Email       string `json:"email"`
	Name        string `json:"name,omitempty"`
	Surname     string `json:"surname,omitempty"`
	Description string `json:"description,omitempty"`
}

// UserListResponse represents a list of users.
type UserListResponse struct {
	Results     []User `json:"results"`
	NextPageKey string `json:"nextPageKey,omitempty"`
	TotalCount  int64  `json:"totalCount"`
}

// Group represents a Dynatrace group.
type Group struct {
	UUID      string `json:"uuid"`
	GroupName string `json:"groupName"`
	Type      string `json:"type"`
}

// GroupListResponse represents a list of groups.
type GroupListResponse struct {
	Results     []Group `json:"results"`
	NextPageKey string  `json:"nextPageKey,omitempty"`
	TotalCount  int64   `json:"totalCount"`
}

// extractEnvironmentID extracts the environment ID from the base URL.
func extractEnvironmentID(baseURL string) (string, error) {
	return httpclient.ExtractSubdomain(baseURL)
}

// ListUsers lists all users in the current environment with automatic pagination.
func (h *Handler) ListUsers(ctx context.Context, partialString string, uuids []string, chunkSize int64) (*UserListResponse, error) {
	envID, err := extractEnvironmentID(h.client.BaseURL())
	if err != nil {
		return nil, err
	}

	var allUsers []User
	var totalCount int64
	nextPageKey := ""

	for {
		req := h.client.HTTP().R().SetContext(ctx)

		uuidFilter := ""
		if len(uuids) > 0 {
			uuidFilter = strings.Join(uuids, ",")
		}

		params := httpclient.PaginationParams{
			Style:         httpclient.PaginationDefault,
			PageKeyParam:  "page-key",
			PageSizeParam: "page-size",
			NextPageKey:   nextPageKey,
			PageSize:      chunkSize,
			Filters:       map[string]string{"partialString": partialString, "uuid": uuidFilter},
		}.QueryParams()

		req.SetQueryParamsFromValues(params)

		resp, err := req.Get(fmt.Sprintf("/platform/iam/v1/organizational-levels/environment/%s/users", httpclient.PathSegment(envID)))
		if err != nil {
			return nil, fmt.Errorf("list users: %w", err)
		}
		if err := httpclient.CheckResponse(resp); err != nil {
			return nil, fmt.Errorf("list users: %w", err)
		}

		var result UserListResponse
		if err := json.Unmarshal(resp.Body(), &result); err != nil {
			return nil, fmt.Errorf("list users: parse response: %w", err)
		}

		allUsers = append(allUsers, result.Results...)
		totalCount = result.TotalCount

		if chunkSize == 0 {
			return &result, nil
		}

		if result.NextPageKey == "" {
			break
		}
		nextPageKey = result.NextPageKey
	}

	return &UserListResponse{
		Results:    allUsers,
		TotalCount: totalCount,
	}, nil
}

// GetUser gets a specific user by UUID.
func (h *Handler) GetUser(ctx context.Context, uuid string) (*User, error) {
	envID, err := extractEnvironmentID(h.client.BaseURL())
	if err != nil {
		return nil, err
	}

	resp, err := h.client.HTTP().R().SetContext(ctx).
		Get(fmt.Sprintf("/platform/iam/v1/organizational-levels/environment/%s/users/%s", httpclient.PathSegment(envID), httpclient.PathSegment(uuid)))
	if err != nil {
		return nil, fmt.Errorf("get user: %w", err)
	}
	if err := httpclient.CheckResponse(resp); err != nil {
		return nil, fmt.Errorf("get user %q: %w", uuid, err)
	}

	var result User
	if err := json.Unmarshal(resp.Body(), &result); err != nil {
		return nil, fmt.Errorf("get user: parse response: %w", err)
	}

	return &result, nil
}

// ListGroups lists all groups in the current environment with automatic pagination.
func (h *Handler) ListGroups(ctx context.Context, partialGroupName string, uuids []string, chunkSize int64) (*GroupListResponse, error) {
	envID, err := extractEnvironmentID(h.client.BaseURL())
	if err != nil {
		return nil, err
	}

	var allGroups []Group
	var totalCount int64
	nextPageKey := ""

	for {
		req := h.client.HTTP().R().SetContext(ctx)

		uuidFilter := ""
		if len(uuids) > 0 {
			uuidFilter = strings.Join(uuids, ",")
		}

		params := httpclient.PaginationParams{
			Style:         httpclient.PaginationDefault,
			PageKeyParam:  "page-key",
			PageSizeParam: "page-size",
			NextPageKey:   nextPageKey,
			PageSize:      chunkSize,
			Filters:       map[string]string{"partialGroupName": partialGroupName, "uuid": uuidFilter},
		}.QueryParams()

		req.SetQueryParamsFromValues(params)

		resp, err := req.Get(fmt.Sprintf("/platform/iam/v1/organizational-levels/environment/%s/groups", httpclient.PathSegment(envID)))
		if err != nil {
			return nil, fmt.Errorf("list groups: %w", err)
		}
		if err := httpclient.CheckResponse(resp); err != nil {
			return nil, fmt.Errorf("list groups: %w", err)
		}

		var result GroupListResponse
		if err := json.Unmarshal(resp.Body(), &result); err != nil {
			return nil, fmt.Errorf("list groups: parse response: %w", err)
		}

		allGroups = append(allGroups, result.Results...)
		totalCount = result.TotalCount

		if chunkSize == 0 {
			return &result, nil
		}

		if result.NextPageKey == "" {
			break
		}
		nextPageKey = result.NextPageKey
	}

	return &GroupListResponse{
		Results:    allGroups,
		TotalCount: totalCount,
	}, nil
}
