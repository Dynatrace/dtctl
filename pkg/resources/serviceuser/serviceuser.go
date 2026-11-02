package serviceuser

import (
	"context"

	sdksu "github.com/Dynatrace/dtctl/sdk/api/serviceuser"
	"github.com/Dynatrace/dtctl/sdk/httpclient"
)

// ServiceUserCreate is the request body for creating a service user.
type ServiceUserCreate = sdksu.ServiceUserCreate

// ServiceUser is the CLI output model for service-user identity metadata.
type ServiceUser struct {
	Name        string `json:"name" table:"NAME"`
	Email       string `json:"email" table:"EMAIL"`
	UID         string `json:"uid" table:"UID"`
	CreatedAt   string `json:"createdAt" table:"CREATED"`
	Surname     string `json:"surname" table:"SURNAME,wide"`
	Description string `json:"description" table:"DESCRIPTION,wide"`
	GroupUUID   string `json:"groupUuid,omitempty" table:"GROUP-UUID,wide"`
}

// Handler delegates service-user operations to the SDK.
type Handler struct {
	sdk *sdksu.Handler
}

// NewHandler creates a CLI service-user handler.
func NewHandler(accountClient *httpclient.Client, accountUUID string) *Handler {
	return &Handler{sdk: sdksu.NewHandler(accountClient, accountUUID)}
}

func fromSDK(user *sdksu.ServiceUser) ServiceUser {
	return ServiceUser{
		Name:        user.Name,
		Email:       user.Email,
		UID:         user.UID,
		CreatedAt:   user.CreatedAt,
		Surname:     user.Surname,
		Description: user.Description,
		GroupUUID:   user.GroupUUID,
	}
}

// List returns all service users.
func (h *Handler) List() ([]ServiceUser, error) {
	results, err := h.sdk.List(context.Background())
	if err != nil {
		return nil, err
	}

	users := make([]ServiceUser, len(results))
	for i := range results {
		users[i] = fromSDK(&results[i])
	}
	return users, nil
}

// Create creates a service user and returns its identity metadata.
func (h *Handler) Create(req ServiceUserCreate) (*ServiceUser, error) {
	result, err := h.sdk.Create(context.Background(), req)
	if err != nil {
		return nil, err
	}
	user := fromSDK(result)
	return &user, nil
}

// Delete deletes a service user by UUID.
func (h *Handler) Delete(userUUID string) error {
	return h.sdk.Delete(context.Background(), userUUID)
}
