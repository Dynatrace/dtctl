package platform

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/Dynatrace/dtctl/sdk/httpclient"
)

// Handler handles Platform Management API resources.
type Handler struct {
	client *httpclient.Client
}

// NewHandler creates a new platform management handler.
func NewHandler(c *httpclient.Client) *Handler {
	return &Handler{client: c}
}

// EnvironmentInfo represents the Dynatrace environment details.
type EnvironmentInfo struct {
	EnvironmentID string    `json:"environmentId"`
	Type          string    `json:"type"`
	State         string    `json:"state"`
	CreateTime    time.Time `json:"createTime"`
	BlockTime     time.Time `json:"blockTime"`
}

// License represents the environment license details.
type License struct {
	Trial                bool `json:"trial"`
	PlatformSubscription bool `json:"platformSubscription"`
}

// LicenseSetting is a single feature-flag entry from the license settings list.
type LicenseSetting struct {
	Key   string `json:"key"`
	Value string `json:"value"`
}

// LicenseSettings is the response from the license settings endpoint.
type LicenseSettings struct {
	Settings []LicenseSetting `json:"settings"`
}

// GetEnvironment retrieves environment information.
func (h *Handler) GetEnvironment(ctx context.Context) (*EnvironmentInfo, error) {
	resp, err := h.client.HTTP().R().SetContext(ctx).
		Get("/platform/management/v1/environment")
	if err != nil {
		return nil, fmt.Errorf("get environment: %w", err)
	}
	if err := httpclient.CheckResponse(resp); err != nil {
		return nil, fmt.Errorf("get environment: %w", err)
	}
	var result EnvironmentInfo
	if err := json.Unmarshal(resp.Body(), &result); err != nil {
		return nil, fmt.Errorf("get environment: parse response: %w", err)
	}
	return &result, nil
}

// GetLicenseSettings retrieves environment license feature settings, optionally filtered by key.
func (h *Handler) GetLicenseSettings(ctx context.Context, keys ...string) (*LicenseSettings, error) {
	req := h.client.HTTP().R().SetContext(ctx)
	if len(keys) > 0 {
		req.SetQueryParamsFromValues(map[string][]string{"keys": keys})
	}
	resp, err := req.Get("/platform/management/v1/environment/license/settings")
	if err != nil {
		return nil, fmt.Errorf("get license settings: %w", err)
	}
	if err := httpclient.CheckResponse(resp); err != nil {
		return nil, fmt.Errorf("get license settings: %w", err)
	}
	var result LicenseSettings
	if err := json.Unmarshal(resp.Body(), &result); err != nil {
		return nil, fmt.Errorf("get license settings: parse response: %w", err)
	}
	return &result, nil
}

// GetLicense retrieves environment license information.
func (h *Handler) GetLicense(ctx context.Context) (*License, error) {
	resp, err := h.client.HTTP().R().SetContext(ctx).
		Get("/platform/management/v1/environment/license")
	if err != nil {
		return nil, fmt.Errorf("get license: %w", err)
	}
	if err := httpclient.CheckResponse(resp); err != nil {
		return nil, fmt.Errorf("get license: %w", err)
	}
	var result License
	if err := json.Unmarshal(resp.Body(), &result); err != nil {
		return nil, fmt.Errorf("get license: parse response: %w", err)
	}
	return &result, nil
}
