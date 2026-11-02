package appengine

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"

	"github.com/Dynatrace/dtctl/sdk/httpclient"
)

// Handler handles App Engine resources
type Handler struct {
	client *httpclient.Client
}

// NewHandler creates a new App Engine handler
func NewHandler(c *httpclient.Client) *Handler {
	return &Handler{client: c}
}

// App represents an installed app
type App struct {
	ID               string                 `json:"id"`
	Name             string                 `json:"name"`
	Version          string                 `json:"version"`
	Description      string                 `json:"description"`
	IsBuiltin        bool                   `json:"isBuiltin,omitempty"`
	ResourceStatus   *ResourceStatus        `json:"resourceStatus,omitempty"`
	SignatureInfo    *SignatureInfo         `json:"signatureInfo,omitempty"`
	Manifest         map[string]interface{} `json:"manifest,omitempty"`
	ModificationInfo *ModificationInfo      `json:"modificationInfo,omitempty"`
}

// ResourceStatus represents the status of an app's resources
type ResourceStatus struct {
	Status              string   `json:"status"`
	SubResourceTypes    []string `json:"subResourceTypes,omitempty"`
	SubResourceStatuses []string `json:"subResourceStatuses,omitempty"`
}

// SignatureInfo represents signature information for an app
type SignatureInfo struct {
	Signature string `json:"signature,omitempty"`
}

// ModificationInfo contains modification timestamps
type ModificationInfo struct {
	CreatedBy      string `json:"createdBy,omitempty"`
	CreatedAt      string `json:"createdAt,omitempty"`
	LastModifiedBy string `json:"lastModifiedBy,omitempty"`
	LastModifiedAt string `json:"lastModifiedAt,omitempty"`
}

// AppList represents a list of apps
type AppList struct {
	Apps []App `json:"apps"`
}

// ListApps lists all installed apps
func (h *Handler) ListApps(ctx context.Context) (*AppList, error) {
	resp, err := h.client.HTTP().R().SetContext(ctx).
		SetQueryParam("add-fields", "isBuiltin,manifest,resourceStatus.subResourceTypes").
		Get("/platform/app-engine/registry/v1/apps")

	if err != nil {
		return nil, fmt.Errorf("list apps: %w", err)
	}
	if err := httpclient.CheckResponse(resp); err != nil {
		return nil, fmt.Errorf("list apps: %w", err)
	}

	var result AppList
	if err := json.Unmarshal(resp.Body(), &result); err != nil {
		return nil, fmt.Errorf("parse apps response: %w", err)
	}

	return &result, nil
}

// GetApp gets a specific app by ID
func (h *Handler) GetApp(ctx context.Context, appID string) (*App, error) {
	resp, err := h.client.HTTP().R().SetContext(ctx).
		SetQueryParam("add-fields", "isBuiltin,manifest,resourceStatus.subResourceTypes").
		Get(fmt.Sprintf("/platform/app-engine/registry/v1/apps/%s", httpclient.PathSegment(appID)))

	if err != nil {
		return nil, fmt.Errorf("get app %q: %w", appID, err)
	}
	if err := httpclient.CheckResponse(resp); err != nil {
		return nil, fmt.Errorf("get app %q: %w", appID, err)
	}

	var result App
	if err := json.Unmarshal(resp.Body(), &result); err != nil {
		return nil, fmt.Errorf("parse app response: %w", err)
	}

	return &result, nil
}

// DeleteApp uninstalls an app
func (h *Handler) DeleteApp(ctx context.Context, appID string) error {
	resp, err := h.client.HTTP().R().SetContext(ctx).
		Delete(fmt.Sprintf("/platform/app-engine/registry/v1/apps/%s", httpclient.PathSegment(appID)))

	if err != nil {
		return fmt.Errorf("delete app %q: %w", appID, err)
	}
	if err := httpclient.CheckResponse(resp); err != nil {
		return fmt.Errorf("delete app %q: %w", appID, err)
	}

	return nil
}

// AppFunction represents a function within an app
type AppFunction struct {
	AppID        string `json:"appId"`
	AppName      string `json:"appName"`
	FunctionName string `json:"functionName"`
	Title        string `json:"title,omitempty"`
	Description  string `json:"description,omitempty"`
	Resumable    bool   `json:"resumable"`
	Stateful     bool   `json:"stateful,omitempty"`
	FullName     string `json:"fullName"`
}

// ListFunctions lists all functions across apps (or filtered by app ID)
func (h *Handler) ListFunctions(ctx context.Context, appIDFilter string) ([]AppFunction, error) {
	appList, err := h.ListApps(ctx)
	if err != nil {
		return nil, err
	}

	var functions []AppFunction

	for _, app := range appList.Apps {
		if appIDFilter != "" && app.ID != appIDFilter {
			continue
		}

		if app.ResourceStatus == nil || !slices.Contains(app.ResourceStatus.SubResourceTypes, "FUNCTIONS") {
			continue
		}

		if app.Manifest != nil {
			actionMetadata := make(map[string]struct {
				title       string
				description string
				stateful    bool
			})

			if actionsArray, ok := app.Manifest["actions"].([]interface{}); ok {
				for _, action := range actionsArray {
					if actionMap, ok := action.(map[string]interface{}); ok {
						name, _ := actionMap["name"].(string)
						title, _ := actionMap["title"].(string)
						description, _ := actionMap["description"].(string)
						stateful, _ := actionMap["stateful"].(bool)

						actionMetadata[name] = struct {
							title       string
							description string
							stateful    bool
						}{title, description, stateful}
					}
				}
			}

			if functionsMap, ok := app.Manifest["functions"].(map[string]interface{}); ok {
				for functionName, functionData := range functionsMap {
					resumable := false
					if functionDataMap, ok := functionData.(map[string]interface{}); ok {
						if res, ok := functionDataMap["resumable"].(bool); ok {
							resumable = res
						}
					}

					metadata := actionMetadata[functionName]

					functions = append(functions, AppFunction{
						AppID:        app.ID,
						AppName:      app.Name,
						FunctionName: functionName,
						Title:        metadata.title,
						Description:  metadata.description,
						Resumable:    resumable,
						Stateful:     metadata.stateful,
						FullName:     fmt.Sprintf("%s/%s", app.ID, functionName),
					})
				}
			}
		}
	}

	return functions, nil
}

// GetFunction gets details about a specific function
func (h *Handler) GetFunction(ctx context.Context, fullName string) (*AppFunction, error) {
	appID, functionName := parseFullFunctionName(fullName)
	if appID == "" || functionName == "" {
		return nil, fmt.Errorf("invalid function name format, expected 'app-id/function-name', got %q", fullName)
	}

	app, err := h.GetApp(ctx, appID)
	if err != nil {
		return nil, err
	}

	if app.ResourceStatus == nil || !slices.Contains(app.ResourceStatus.SubResourceTypes, "FUNCTIONS") {
		return nil, fmt.Errorf("app %q does not have functions", appID)
	}

	if app.Manifest != nil {
		var title, description string
		var stateful bool

		if actionsArray, ok := app.Manifest["actions"].([]interface{}); ok {
			for _, action := range actionsArray {
				if actionMap, ok := action.(map[string]interface{}); ok {
					if name, _ := actionMap["name"].(string); name == functionName {
						title, _ = actionMap["title"].(string)
						description, _ = actionMap["description"].(string)
						stateful, _ = actionMap["stateful"].(bool)
						break
					}
				}
			}
		}

		if functionsMap, ok := app.Manifest["functions"].(map[string]interface{}); ok {
			if functionData, ok := functionsMap[functionName]; ok {
				resumable := false
				if functionDataMap, ok := functionData.(map[string]interface{}); ok {
					if res, ok := functionDataMap["resumable"].(bool); ok {
						resumable = res
					}
				}

				return &AppFunction{
					AppID:        app.ID,
					AppName:      app.Name,
					FunctionName: functionName,
					Title:        title,
					Description:  description,
					Resumable:    resumable,
					Stateful:     stateful,
					FullName:     fullName,
				}, nil
			}
		}
	}

	return nil, fmt.Errorf("function %q not found in app %q", functionName, appID)
}

// Helper functions
func parseFullFunctionName(fullName string) (appID, functionName string) {
	for i := 0; i < len(fullName); i++ {
		if fullName[i] == '/' {
			return fullName[:i], fullName[i+1:]
		}
	}
	return "", ""
}
