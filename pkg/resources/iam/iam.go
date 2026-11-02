package iam

import (
	"context"

	"github.com/Dynatrace/dtctl/pkg/client"
	sdkiam "github.com/Dynatrace/dtctl/sdk/api/iam"
	"github.com/Dynatrace/dtctl/sdk/httpclient"
)

// User represents a Dynatrace user (CLI version with table tags).
type User struct {
	UID         string `json:"uid" table:"UID"`
	Email       string `json:"email" table:"EMAIL"`
	Name        string `json:"name,omitempty" table:"NAME"`
	Surname     string `json:"surname,omitempty" table:"SURNAME"`
	Description string `json:"description,omitempty" table:"DESCRIPTION,wide"`
}

// UserListResponse represents a list of users.
type UserListResponse struct {
	Results     []User `json:"results"`
	NextPageKey string `json:"nextPageKey,omitempty"`
	TotalCount  int64  `json:"totalCount"`
}

// Group represents a Dynatrace group (CLI version with table tags).
type Group struct {
	UUID      string `json:"uuid" table:"UUID"`
	GroupName string `json:"groupName" table:"NAME"`
	Type      string `json:"type" table:"TYPE"`
}

// GroupListResponse represents a list of groups.
type GroupListResponse struct {
	Results     []Group `json:"results"`
	NextPageKey string  `json:"nextPageKey,omitempty"`
	TotalCount  int64   `json:"totalCount"`
}

// fromSDKUser converts an SDK User to a CLI User.
func fromSDKUser(s *sdkiam.User) User {
	return User{
		UID:         s.UID,
		Email:       s.Email,
		Name:        s.Name,
		Surname:     s.Surname,
		Description: s.Description,
	}
}

// fromSDKGroup converts an SDK Group to a CLI Group.
func fromSDKGroup(s *sdkiam.Group) Group {
	return Group{
		UUID:      s.UUID,
		GroupName: s.GroupName,
		Type:      s.Type,
	}
}

// Handler handles IAM resources.
// It delegates to the SDK handler.
type Handler struct {
	sdk *sdkiam.Handler
}

// NewHandler creates a new IAM handler.
func NewHandler(c *client.Client) *Handler {
	return &Handler{
		sdk: sdkiam.NewHandler(httpclient.Wrap(c.HTTP())),
	}
}

// ListUsers lists all users in the current environment with automatic pagination.
func (h *Handler) ListUsers(partialString string, uuids []string, chunkSize int64) (*UserListResponse, error) {
	sdkResult, err := h.sdk.ListUsers(context.Background(), partialString, uuids, chunkSize)
	if err != nil {
		return nil, err
	}
	users := make([]User, len(sdkResult.Results))
	for i := range sdkResult.Results {
		users[i] = fromSDKUser(&sdkResult.Results[i])
	}
	return &UserListResponse{
		Results:    users,
		TotalCount: sdkResult.TotalCount,
	}, nil
}

// GetUser gets a specific user by UUID.
func (h *Handler) GetUser(uuid string) (*User, error) {
	sdkResult, err := h.sdk.GetUser(context.Background(), uuid)
	if err != nil {
		return nil, err
	}
	u := fromSDKUser(sdkResult)
	return &u, nil
}

// ListGroups lists all groups in the current account with automatic pagination.
func (h *Handler) ListGroups(partialGroupName string, uuids []string, chunkSize int64) (*GroupListResponse, error) {
	sdkResult, err := h.sdk.ListGroups(context.Background(), partialGroupName, uuids, chunkSize)
	if err != nil {
		return nil, err
	}
	groups := make([]Group, len(sdkResult.Results))
	for i := range sdkResult.Results {
		groups[i] = fromSDKGroup(&sdkResult.Results[i])
	}
	return &GroupListResponse{
		Results:    groups,
		TotalCount: sdkResult.TotalCount,
	}, nil
}
