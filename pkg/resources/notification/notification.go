package notification

import (
	"context"

	"github.com/Dynatrace/dtctl/pkg/client"
	sdknotification "github.com/Dynatrace/dtctl/sdk/api/notification"
	"github.com/Dynatrace/dtctl/sdk/httpclient"
)

// EventNotification represents an event notification (CLI version with table tags).
type EventNotification struct {
	ID               string         `json:"id" table:"ID"`
	NotificationType string         `json:"notificationType" table:"TYPE"`
	Enabled          bool           `json:"enabled" table:"ENABLED"`
	AppID            string         `json:"appId,omitempty" table:"APP_ID,wide"`
	Owner            string         `json:"owner,omitempty" table:"OWNER,wide"`
	TriggerConfig    map[string]any `json:"triggerConfig,omitempty" table:"-"`
	ActionConfig     map[string]any `json:"actionConfig,omitempty" table:"-"`
}

// EventNotificationList represents a list of event notifications.
type EventNotificationList struct {
	Results []EventNotification `json:"results"`
	Count   int                 `json:"count"`
}

// ResourceNotification represents a resource notification (CLI version with table tags).
type ResourceNotification struct {
	ID               string `json:"id" table:"ID"`
	NotificationType string `json:"notificationType" table:"TYPE"`
	ResourceID       string `json:"resourceId" table:"RESOURCE_ID"`
	AppID            string `json:"appId,omitempty" table:"APP_ID,wide"`
}

// ResourceNotificationList represents a list of resource notifications.
type ResourceNotificationList struct {
	Results []ResourceNotification `json:"results"`
	Count   int                    `json:"count"`
}

// fromSDKEventNotification converts an SDK EventNotification to CLI.
func fromSDKEventNotification(s *sdknotification.EventNotification) EventNotification {
	return EventNotification{
		ID:               s.ID,
		NotificationType: s.NotificationType,
		Enabled:          s.Enabled,
		AppID:            s.AppID,
		Owner:            s.Owner,
		TriggerConfig:    s.TriggerConfig,
		ActionConfig:     s.ActionConfig,
	}
}

// fromSDKResourceNotification converts an SDK ResourceNotification to CLI.
func fromSDKResourceNotification(s *sdknotification.ResourceNotification) ResourceNotification {
	return ResourceNotification{
		ID:               s.ID,
		NotificationType: s.NotificationType,
		ResourceID:       s.ResourceID,
		AppID:            s.AppID,
	}
}

// Handler handles notification resources.
// It delegates to the SDK handler.
type Handler struct {
	sdk *sdknotification.Handler
}

// NewHandler creates a new notification handler.
func NewHandler(c *client.Client) *Handler {
	return &Handler{
		sdk: sdknotification.NewHandler(httpclient.Wrap(c.HTTP())),
	}
}

// ListEventNotifications lists event notifications.
func (h *Handler) ListEventNotifications(notificationType string) (*EventNotificationList, error) {
	sdkResult, err := h.sdk.ListEventNotifications(context.Background(), notificationType)
	if err != nil {
		return nil, err
	}
	results := make([]EventNotification, len(sdkResult.Results))
	for i := range sdkResult.Results {
		results[i] = fromSDKEventNotification(&sdkResult.Results[i])
	}
	return &EventNotificationList{Results: results, Count: sdkResult.Count}, nil
}

// GetEventNotification gets a specific event notification by ID.
func (h *Handler) GetEventNotification(id string) (*EventNotification, error) {
	sdkResult, err := h.sdk.GetEventNotification(context.Background(), id)
	if err != nil {
		return nil, err
	}
	n := fromSDKEventNotification(sdkResult)
	return &n, nil
}

// CreateEventNotification creates a new event notification.
func (h *Handler) CreateEventNotification(data []byte) (*EventNotification, error) {
	sdkResult, err := h.sdk.CreateEventNotification(context.Background(), data)
	if err != nil {
		return nil, err
	}
	n := fromSDKEventNotification(sdkResult)
	return &n, nil
}

// DeleteEventNotification deletes an event notification.
func (h *Handler) DeleteEventNotification(id string) error {
	return h.sdk.DeleteEventNotification(context.Background(), id)
}

// ListResourceNotifications lists resource notifications.
func (h *Handler) ListResourceNotifications(notificationType, resourceID string) (*ResourceNotificationList, error) {
	sdkResult, err := h.sdk.ListResourceNotifications(context.Background(), notificationType, resourceID)
	if err != nil {
		return nil, err
	}
	results := make([]ResourceNotification, len(sdkResult.Results))
	for i := range sdkResult.Results {
		results[i] = fromSDKResourceNotification(&sdkResult.Results[i])
	}
	return &ResourceNotificationList{Results: results, Count: sdkResult.Count}, nil
}

// GetResourceNotification gets a specific resource notification by ID.
func (h *Handler) GetResourceNotification(id string) (*ResourceNotification, error) {
	sdkResult, err := h.sdk.GetResourceNotification(context.Background(), id)
	if err != nil {
		return nil, err
	}
	n := fromSDKResourceNotification(sdkResult)
	return &n, nil
}

// DeleteResourceNotification deletes a resource notification.
func (h *Handler) DeleteResourceNotification(id string) error {
	return h.sdk.DeleteResourceNotification(context.Background(), id)
}
