package appengine

import (
	"context"

	"github.com/Dynatrace/dtctl/pkg/client"
	sdkae "github.com/Dynatrace/dtctl/sdk/api/appengine"
	"github.com/Dynatrace/dtctl/sdk/httpclient"
)

// Re-export SDK types that have no table tags.
type (
	ResourceStatus   = sdkae.ResourceStatus
	SignatureInfo    = sdkae.SignatureInfo
	ModificationInfo = sdkae.ModificationInfo
)

// App represents an installed app (CLI version with table tags).
type App struct {
	ID               string                 `json:"id" table:"ID"`
	Name             string                 `json:"name" table:"NAME"`
	Version          string                 `json:"version" table:"VERSION"`
	Description      string                 `json:"description" table:"DESCRIPTION,wide"`
	IsBuiltin        bool                   `json:"isBuiltin,omitempty" table:"BUILTIN,wide"`
	ResourceStatus   *ResourceStatus        `json:"resourceStatus,omitempty" table:"-"`
	SignatureInfo    *SignatureInfo         `json:"signatureInfo,omitempty" table:"-"`
	Manifest         map[string]interface{} `json:"manifest,omitempty" table:"-"`
	ModificationInfo *ModificationInfo      `json:"modificationInfo,omitempty" table:"-"`
}

// AppList represents a list of apps.
type AppList struct {
	Apps []App `json:"apps"`
}

// AppFunction represents a function within an app (CLI version with table tags).
type AppFunction struct {
	AppID        string `json:"appId" table:"APP_ID,wide"`
	AppName      string `json:"appName" table:"APP"`
	FunctionName string `json:"functionName" table:"FUNCTION"`
	Title        string `json:"title,omitempty" table:"TITLE,wide"`
	Description  string `json:"description,omitempty" table:"DESCRIPTION,wide"`
	Resumable    bool   `json:"resumable" table:"RESUMABLE,wide"`
	Stateful     bool   `json:"stateful,omitempty" table:"STATEFUL,wide"`
	FullName     string `json:"fullName" table:"FULL_NAME"`
}

// fromSDKApp converts an SDK App to a CLI App.
func fromSDKApp(s *sdkae.App) App {
	return App{
		ID:               s.ID,
		Name:             s.Name,
		Version:          s.Version,
		Description:      s.Description,
		IsBuiltin:        s.IsBuiltin,
		ResourceStatus:   s.ResourceStatus,
		SignatureInfo:    s.SignatureInfo,
		Manifest:         s.Manifest,
		ModificationInfo: s.ModificationInfo,
	}
}

// fromSDKAppFunction converts an SDK AppFunction to a CLI AppFunction.
func fromSDKAppFunction(s *sdkae.AppFunction) AppFunction {
	return AppFunction{
		AppID:        s.AppID,
		AppName:      s.AppName,
		FunctionName: s.FunctionName,
		Title:        s.Title,
		Description:  s.Description,
		Resumable:    s.Resumable,
		Stateful:     s.Stateful,
		FullName:     s.FullName,
	}
}

// Handler handles App Engine resources.
type Handler struct {
	sdk *sdkae.Handler
}

// NewHandler creates a new App Engine handler
func NewHandler(c *client.Client) *Handler {
	return &Handler{
		sdk: sdkae.NewHandler(httpclient.Wrap(c.HTTP())),
	}
}

// ListApps lists all installed apps
func (h *Handler) ListApps() (*AppList, error) {
	sdkResult, err := h.sdk.ListApps(context.Background())
	if err != nil {
		return nil, err
	}
	apps := make([]App, len(sdkResult.Apps))
	for i := range sdkResult.Apps {
		apps[i] = fromSDKApp(&sdkResult.Apps[i])
	}
	return &AppList{Apps: apps}, nil
}

// GetApp gets a specific app by ID
func (h *Handler) GetApp(appID string) (*App, error) {
	sdkResult, err := h.sdk.GetApp(context.Background(), appID)
	if err != nil {
		return nil, err
	}
	app := fromSDKApp(sdkResult)
	return &app, nil
}

// DeleteApp uninstalls an app
func (h *Handler) DeleteApp(appID string) error {
	return h.sdk.DeleteApp(context.Background(), appID)
}

// ListFunctions lists all functions across apps (or filtered by app ID)
func (h *Handler) ListFunctions(appIDFilter string) ([]AppFunction, error) {
	sdkResult, err := h.sdk.ListFunctions(context.Background(), appIDFilter)
	if err != nil {
		return nil, err
	}
	functions := make([]AppFunction, len(sdkResult))
	for i := range sdkResult {
		functions[i] = fromSDKAppFunction(&sdkResult[i])
	}
	return functions, nil
}

// GetFunction gets details about a specific function
func (h *Handler) GetFunction(fullName string) (*AppFunction, error) {
	sdkResult, err := h.sdk.GetFunction(context.Background(), fullName)
	if err != nil {
		return nil, err
	}
	f := fromSDKAppFunction(sdkResult)
	return &f, nil
}
