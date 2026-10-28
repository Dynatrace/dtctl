package platform

import (
	"context"
	"time"

	"github.com/Dynatrace/dtctl/pkg/client"
	sdkplatform "github.com/Dynatrace/dtctl/sdk/api/platform"
	"github.com/Dynatrace/dtctl/sdk/httpclient"
)

// EnvironmentInfo represents the Dynatrace environment details with CLI display fields.
type EnvironmentInfo struct {
	EnvironmentID string    `json:"environmentId" table:"ID"`
	Type          string    `json:"type" table:"TYPE"`
	State         string    `json:"state" table:"STATE"`
	CreateTime    time.Time `json:"createTime" table:"CREATED,wide"`
	BlockTime     time.Time `json:"blockTime" table:"BLOCK_TIME,wide"`
}

// License represents the environment license details with CLI display fields.
type License struct {
	Trial                bool `json:"trial" table:"TRIAL"`
	PlatformSubscription bool `json:"platformSubscription" table:"PLATFORM_SUBSCRIPTION"`
}

// LicenseSetting is a single feature-flag entry from the license settings list.
type LicenseSetting struct {
	Key string `json:"key" table:"KEY"`
	// Value is a JSON string from the API (e.g. "true"/"false"), not a boolean.
	Value string `json:"value" table:"ENABLED"`
}

// Handler handles Platform Management resources.
type Handler struct {
	sdk *sdkplatform.Handler
}

// NewHandler creates a new platform management handler.
func NewHandler(c *client.Client) *Handler {
	return &Handler{
		sdk: sdkplatform.NewHandler(httpclient.Wrap(c.HTTP())),
	}
}

// GetEnvironment retrieves environment information.
func (h *Handler) GetEnvironment() (*EnvironmentInfo, error) {
	sdkResult, err := h.sdk.GetEnvironment(context.Background())
	if err != nil {
		return nil, err
	}
	return &EnvironmentInfo{
		EnvironmentID: sdkResult.EnvironmentID,
		Type:          sdkResult.Type,
		State:         sdkResult.State,
		CreateTime:    sdkResult.CreateTime,
		BlockTime:     sdkResult.BlockTime,
	}, nil
}

// GetLicenseSettings retrieves license feature settings, optionally filtered to specific keys.
func (h *Handler) GetLicenseSettings(keys ...string) ([]LicenseSetting, error) {
	sdkResult, err := h.sdk.GetLicenseSettings(context.Background(), keys...)
	if err != nil {
		return nil, err
	}
	settings := make([]LicenseSetting, 0, len(sdkResult.Settings))
	for _, s := range sdkResult.Settings {
		settings = append(settings, LicenseSetting{Key: s.Key, Value: s.Value})
	}
	return settings, nil
}

// GetLicense retrieves environment license information.
func (h *Handler) GetLicense() (*License, error) {
	sdkResult, err := h.sdk.GetLicense(context.Background())
	if err != nil {
		return nil, err
	}
	return &License{
		Trial:                sdkResult.Trial,
		PlatformSubscription: sdkResult.PlatformSubscription,
	}, nil
}
