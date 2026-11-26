package extension

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/Dynatrace/dtctl/sdk/httpclient"
)

// Handler handles Extensions 2.0 resources
type Handler struct {
	client *httpclient.Client
}

// NewHandler creates a new Extension handler
func NewHandler(c *httpclient.Client) *Handler {
	return &Handler{client: c}
}

// Extension represents an Extensions 2.0 extension
type Extension struct {
	ExtensionName string `json:"extensionName"`
	Version       string `json:"version,omitempty"`
}

// ExtensionList represents a paginated list of extensions
type ExtensionList struct {
	Items       []Extension `json:"items"`
	TotalCount  int         `json:"totalCount"`
	NextPageKey string      `json:"nextPageKey,omitempty"`
}

// ExtensionVersion represents a specific version of an extension
type ExtensionVersion struct {
	Version       string `json:"version"`
	ExtensionName string `json:"extensionName"`
	Active        bool   `json:"active,omitempty"`
}

// ExtensionVersionList represents a list of extension versions
type ExtensionVersionList struct {
	Items       []ExtensionVersion `json:"items"`
	TotalCount  int                `json:"totalCount"`
	NextPageKey string             `json:"nextPageKey,omitempty"`
}

// ExtensionDetails represents detailed information about an extension version
type ExtensionDetails struct {
	ExtensionName       string                      `json:"extensionName"`
	Version             string                      `json:"version"`
	Author              ExtensionAuthor             `json:"author,omitempty"`
	DataSources         []string                    `json:"dataSources,omitempty"`
	FeatureSets         []string                    `json:"featureSets,omitempty"`
	FeatureSetDetails   map[string]FeatureSetDetail `json:"featureSetsDetails,omitempty"`
	FileHash            string                      `json:"fileHash,omitempty"`
	MinDynatraceVersion string                      `json:"minDynatraceVersion,omitempty"`
	MinEECVersion       string                      `json:"minEECVersion,omitempty"`
	Variables           []ExtensionVariable         `json:"vars,omitempty"`
}

// ExtensionAuthor represents the author of an extension
type ExtensionAuthor struct {
	Name string `json:"name"`
}

// FeatureSetDetail represents a feature set of an extension
type FeatureSetDetail struct {
	IsRecommended bool               `json:"isRecommended,omitempty"`
	Metrics       []FeatureSetMetric `json:"metrics,omitempty"`
}

// FeatureSetMetric represents a metric within a feature set
type FeatureSetMetric struct {
	Key string `json:"key"`
	// Metadata is a pointer so that omitempty actually drops the field for metrics
	// that carry no metadata. encoding/json does NOT treat a zero-valued struct as
	// empty, so a value type here would always serialize as "metadata": {}.
	Metadata *FeatureSetMetricMetadata `json:"metadata,omitempty"`
}

// FeatureSetMetricMetadata holds display information for a feature set metric
type FeatureSetMetricMetadata struct {
	DisplayName string `json:"displayName,omitempty"`
	Description string `json:"description,omitempty"`
	Unit        string `json:"unit,omitempty"`
}

// ExtensionVariable represents a variable defined in an extension
type ExtensionVariable struct {
	Name        string `json:"name"`
	Type        string `json:"type"`
	DisplayName string `json:"displayName,omitempty"`
}

// MonitoringConfiguration represents an extension monitoring configuration instance
type MonitoringConfiguration struct {
	Type          string          `json:"type,omitempty" yaml:"type,omitempty"`
	ExtensionName string          `json:"extensionName,omitempty"`
	ObjectID      string          `json:"objectId"`
	Scope         string          `json:"scope,omitempty"`
	Value         json.RawMessage `json:"value,omitempty"`
}

// MarshalYAML implements yaml.Marshaler to properly serialize json.RawMessage Value
// as a structured object instead of a byte array.
func (m MonitoringConfiguration) MarshalYAML() (interface{}, error) {
	var parsedValue interface{}
	if len(m.Value) > 0 {
		if err := json.Unmarshal(m.Value, &parsedValue); err != nil {
			// If we can't parse the JSON, fall back to string representation
			parsedValue = string(m.Value)
		}
	}

	return struct {
		Type          string      `yaml:"type,omitempty"`
		ExtensionName string      `yaml:"extensionName,omitempty"`
		ObjectID      string      `yaml:"objectId"`
		Scope         string      `yaml:"scope,omitempty"`
		Value         interface{} `yaml:"value,omitempty"`
	}{
		Type:          m.Type,
		ExtensionName: m.ExtensionName,
		ObjectID:      m.ObjectID,
		Scope:         m.Scope,
		Value:         parsedValue,
	}, nil
}

// MonitoringConfigurationList represents a list of monitoring configuration instances
type MonitoringConfigurationList struct {
	Items       []MonitoringConfiguration `json:"items"`
	TotalCount  int                       `json:"totalCount"`
	NextPageKey string                    `json:"nextPageKey,omitempty"`
}

// ExtensionEnvironmentConfig represents the environment-wide configuration for an extension
type ExtensionEnvironmentConfig struct {
	Version string `json:"version"`
}

// ExtensionStatus represents the monitoring status of a specific extension version
type ExtensionStatus struct {
	Status    string `json:"status"`
	Timestamp string `json:"timestamp,omitempty"`
}

// ActiveGateEntry represents a single ActiveGate instance within an active gate group
type ActiveGateEntry struct {
	ID     int64           `json:"id"`
	Errors json.RawMessage `json:"errors,omitempty"`
}

// ActiveGateGroupItem represents one active gate group available for an extension version
type ActiveGateGroupItem struct {
	GroupName            string            `json:"groupName"`
	AvailableActiveGates int               `json:"availableActiveGates"`
	ActiveGates          []ActiveGateEntry `json:"activeGates,omitempty"`
}

// ActiveGateGroupList represents the list of active gate groups for an extension version
type ActiveGateGroupList struct {
	Items []ActiveGateGroupItem `json:"items"`
}

// maxPageSize is the maximum page size accepted by the Extensions 2.0 API.
const maxPageSize = 100

// List lists all extensions with automatic pagination
func (h *Handler) List(ctx context.Context, name string, chunkSize int64) (*ExtensionList, error) {
	var allExtensions []Extension
	var totalCount int
	nextPageKey := ""

	// Cap page size to API maximum
	if chunkSize > maxPageSize {
		chunkSize = maxPageSize
	}

	for {
		var result ExtensionList
		req := h.client.HTTP().R().SetContext(ctx)

		req.SetQueryParamsFromValues(httpclient.PaginationParams{
			Style:         httpclient.PaginationDefault,
			PageKeyParam:  "next-page-key",
			PageSizeParam: "page-size",
			NextPageKey:   nextPageKey,
			PageSize:      chunkSize,
			Filters:       map[string]string{"name": name},
		}.QueryParams())

		resp, err := req.Get("/platform/extensions/v2/extensions")
		if err != nil {
			return nil, fmt.Errorf("failed to list extensions: %w", err)
		}
		if err := httpclient.CheckResponse(resp); err != nil {
			return nil, fmt.Errorf("failed to list extensions: %w", err)
		}

		if err := json.Unmarshal(resp.Body(), &result); err != nil {
			return nil, fmt.Errorf("list extensions: parse response: %w", err)
		}

		allExtensions = append(allExtensions, result.Items...)
		totalCount = result.TotalCount

		if chunkSize == 0 || result.NextPageKey == "" {
			break
		}

		nextPageKey = result.NextPageKey
	}

	// Client-side filtering: the API accepts the name parameter but ignores it,
	// so we filter locally using a case-insensitive substring match.
	if name != "" {
		nameLower := strings.ToLower(name)
		filtered := allExtensions[:0]
		for _, ext := range allExtensions {
			if strings.Contains(strings.ToLower(ext.ExtensionName), nameLower) {
				filtered = append(filtered, ext)
			}
		}
		allExtensions = filtered
		totalCount = len(filtered)
	}

	return &ExtensionList{
		Items:      allExtensions,
		TotalCount: totalCount,
	}, nil
}

// Get gets a specific extension by name (returns all versions)
func (h *Handler) Get(ctx context.Context, extensionName string) (*ExtensionVersionList, error) {
	var allVersions []ExtensionVersion
	var totalCount int
	nextPageKey := ""

	for {
		var result ExtensionVersionList
		req := h.client.HTTP().R().SetContext(ctx)

		req.SetQueryParamsFromValues(httpclient.PaginationParams{
			Style:        httpclient.PaginationDefault,
			PageKeyParam: "next-page-key",
			NextPageKey:  nextPageKey,
		}.QueryParams())

		resp, err := req.Get(fmt.Sprintf("/platform/extensions/v2/extensions/%s", httpclient.PathSegment(extensionName)))
		if err != nil {
			return nil, fmt.Errorf("failed to get extension: %w", err)
		}
		if err := httpclient.CheckResponse(resp); err != nil {
			var apiErr *httpclient.APIError
			if errors.As(err, &apiErr) {
				switch apiErr.StatusCode {
				case 404:
					return nil, fmt.Errorf("extension %q not found", extensionName)
				case 403:
					return nil, fmt.Errorf("access denied to extension %q", extensionName)
				}
			}
			return nil, fmt.Errorf("failed to get extension: %w", err)
		}

		if err := json.Unmarshal(resp.Body(), &result); err != nil {
			return nil, fmt.Errorf("get extension: parse response: %w", err)
		}

		allVersions = append(allVersions, result.Items...)
		totalCount = result.TotalCount

		if result.NextPageKey == "" {
			break
		}
		nextPageKey = result.NextPageKey
	}

	// Determine the active version from the environment configuration and mark it.
	// The version list endpoint does not include the active flag; the environment
	// configuration endpoint returns which version is currently active.
	//
	// This enrichment is best-effort: the version list was already fetched
	// successfully, so a failure of the secondary env-config lookup must not
	// fail the whole call. A 404 (no environment configuration) is treated as
	// "no active version"; any other error (403, 5xx, network) simply leaves
	// every version's Active flag unset. This mirrors List(), which leaves the
	// ACTIVE VERSION column empty rather than aborting the listing.
	if envCfg, err := h.GetActiveVersion(ctx, extensionName); err == nil && envCfg != "" {
		for i := range allVersions {
			if allVersions[i].Version == envCfg {
				allVersions[i].Active = true
				break
			}
		}
	}

	return &ExtensionVersionList{
		Items:      allVersions,
		TotalCount: totalCount,
	}, nil
}

// GetActiveVersion returns the currently active version of an extension
// by querying the environment configuration endpoint. Returns an empty string
// if the extension has no active version (HTTP 404). Other errors are propagated.
func (h *Handler) GetActiveVersion(ctx context.Context, extensionName string) (string, error) {
	resp, err := h.client.HTTP().R().SetContext(ctx).
		Get(fmt.Sprintf("/platform/extensions/v2/extensions/%s/environment-configuration", httpclient.PathSegment(extensionName)))
	if err != nil {
		return "", err
	}
	if err := httpclient.CheckResponse(resp); err != nil {
		var apiErr *httpclient.APIError
		if errors.As(err, &apiErr) && apiErr.StatusCode == http.StatusNotFound {
			return "", nil
		}
		return "", err
	}

	var cfg ExtensionEnvironmentConfig
	if err := json.Unmarshal(resp.Body(), &cfg); err != nil {
		return "", err
	}
	return cfg.Version, nil
}

// ActivateVersion sets the active version of an extension via the environment-configuration
// endpoint. PUT creates the environment configuration when the extension has never been
// activated as well as updating an existing one (verified against a live environment), and
// is safe to call when the version is already active.
func (h *Handler) ActivateVersion(ctx context.Context, extensionName, version string) (*ExtensionEnvironmentConfig, error) {
	resp, err := h.client.HTTP().R().SetContext(ctx).
		SetBody(map[string]string{"version": version}).
		Put(fmt.Sprintf("/platform/extensions/v2/extensions/%s/environment-configuration", httpclient.PathSegment(extensionName)))
	if err != nil {
		return nil, fmt.Errorf("activate extension version: %w", err)
	}
	if err := httpclient.CheckResponse(resp); err != nil {
		var apiErr *httpclient.APIError
		if errors.As(err, &apiErr) {
			switch apiErr.StatusCode {
			case http.StatusNotFound:
				return nil, fmt.Errorf("extension %q or version %q not found", extensionName, version)
			case http.StatusForbidden:
				return nil, fmt.Errorf("access denied to extension %q", extensionName)
			}
		}
		return nil, fmt.Errorf("activate extension version: %w", err)
	}
	var result ExtensionEnvironmentConfig
	if err := json.Unmarshal(resp.Body(), &result); err != nil {
		return nil, fmt.Errorf("activate extension version: parse response: %w", err)
	}
	return &result, nil
}

// GetVersion gets details for a specific extension version
func (h *Handler) GetVersion(ctx context.Context, extensionName, version string) (*ExtensionDetails, error) {
	resp, err := h.client.HTTP().R().SetContext(ctx).
		Get(fmt.Sprintf("/platform/extensions/v2/extensions/%s/%s", httpclient.PathSegment(extensionName), httpclient.PathSegment(version)))
	if err != nil {
		return nil, fmt.Errorf("failed to get extension version: %w", err)
	}
	if err := httpclient.CheckResponse(resp); err != nil {
		var apiErr *httpclient.APIError
		if errors.As(err, &apiErr) {
			switch apiErr.StatusCode {
			case 404:
				return nil, fmt.Errorf("extension %q version %q not found", extensionName, version)
			case 403:
				return nil, fmt.Errorf("access denied to extension %q", extensionName)
			}
		}
		return nil, fmt.Errorf("failed to get extension version: %w", err)
	}

	var result ExtensionDetails
	if err := json.Unmarshal(resp.Body(), &result); err != nil {
		return nil, fmt.Errorf("get extension version: parse response: %w", err)
	}
	return &result, nil
}

// GetEnvironmentConfig gets the environment configuration for a specific extension version.
// The version parameter is required by the Dynatrace Extensions 2.0 API.
func (h *Handler) GetEnvironmentConfig(ctx context.Context, extensionName, version string) (*ExtensionEnvironmentConfig, error) {
	resp, err := h.client.HTTP().R().SetContext(ctx).
		Get(fmt.Sprintf("/platform/extensions/v2/extensions/%s/%s/environment-configuration", httpclient.PathSegment(extensionName), httpclient.PathSegment(version)))
	if err != nil {
		return nil, fmt.Errorf("failed to get extension environment config: %w", err)
	}
	if err := httpclient.CheckResponse(resp); err != nil {
		var apiErr *httpclient.APIError
		if errors.As(err, &apiErr) {
			switch apiErr.StatusCode {
			case 404:
				return nil, fmt.Errorf("extension %q version %q has no environment configuration", extensionName, version)
			case 403:
				return nil, fmt.Errorf("access denied to extension %q", extensionName)
			}
		}
		return nil, fmt.Errorf("failed to get extension environment config: %w", err)
	}

	var result ExtensionEnvironmentConfig
	if err := json.Unmarshal(resp.Body(), &result); err != nil {
		return nil, fmt.Errorf("get extension environment config: parse response: %w", err)
	}
	return &result, nil
}

// ListMonitoringConfigurations lists monitoring configurations for an extension.
// If version is non-empty, results are filtered client-side by matching the
// "version" key inside each configuration's value JSON. Configurations whose
// value cannot be parsed or that lack a "version" key are excluded from the
// filtered result.
func (h *Handler) ListMonitoringConfigurations(ctx context.Context, extensionName, version string, chunkSize int64) (*MonitoringConfigurationList, error) {
	var allItems []MonitoringConfiguration
	var totalCount int
	nextPageKey := ""

	// Cap page size to API maximum
	if chunkSize > maxPageSize {
		chunkSize = maxPageSize
	}

	for {
		var result MonitoringConfigurationList
		req := h.client.HTTP().R().SetContext(ctx)

		req.SetQueryParamsFromValues(httpclient.PaginationParams{
			Style:         httpclient.PaginationDefault,
			PageKeyParam:  "next-page-key",
			PageSizeParam: "page-size",
			NextPageKey:   nextPageKey,
			PageSize:      chunkSize,
			Filters:       map[string]string{"version": version},
		}.QueryParams())

		resp, err := req.Get(fmt.Sprintf("/platform/extensions/v2/extensions/%s/monitoring-configurations", httpclient.PathSegment(extensionName)))
		if err != nil {
			return nil, fmt.Errorf("failed to list monitoring configurations: %w", err)
		}
		if err := httpclient.CheckResponse(resp); err != nil {
			var apiErr *httpclient.APIError
			if errors.As(err, &apiErr) {
				switch apiErr.StatusCode {
				case 404:
					return nil, fmt.Errorf("extension %q not found", extensionName)
				case 403:
					return nil, fmt.Errorf("access denied to extension %q", extensionName)
				}
			}
			return nil, fmt.Errorf("failed to list monitoring configurations: %w", err)
		}

		if err := json.Unmarshal(resp.Body(), &result); err != nil {
			return nil, fmt.Errorf("list monitoring configurations: parse response: %w", err)
		}

		for i := range result.Items {
			result.Items[i].Type = "extension_monitoring_config"
			result.Items[i].ExtensionName = extensionName
		}
		allItems = append(allItems, result.Items...)
		totalCount = result.TotalCount

		if chunkSize == 0 || result.NextPageKey == "" {
			break
		}
		nextPageKey = result.NextPageKey
	}

	// Client-side filtering: the API accepts the version parameter but ignores it,
	// so we filter locally by extracting the version from the config value JSON.
	if version != "" {
		filtered := allItems[:0]
		for _, item := range allItems {
			if len(item.Value) > 0 {
				var val map[string]interface{}
				if err := json.Unmarshal(item.Value, &val); err == nil {
					if v, ok := val["version"].(string); ok && v == version {
						filtered = append(filtered, item)
					}
				}
			}
		}
		allItems = filtered
		totalCount = len(filtered)
	}

	return &MonitoringConfigurationList{
		Items:      allItems,
		TotalCount: totalCount,
	}, nil
}

// GetMonitoringConfiguration gets a specific monitoring configuration
func (h *Handler) GetMonitoringConfiguration(ctx context.Context, extensionName, configID string) (*MonitoringConfiguration, error) {
	resp, err := h.client.HTTP().R().SetContext(ctx).
		Get(fmt.Sprintf("/platform/extensions/v2/extensions/%s/monitoring-configurations/%s", httpclient.PathSegment(extensionName), httpclient.PathSegment(configID)))
	if err != nil {
		return nil, fmt.Errorf("failed to get monitoring configuration: %w", err)
	}
	if err := httpclient.CheckResponse(resp); err != nil {
		var apiErr *httpclient.APIError
		if errors.As(err, &apiErr) {
			switch apiErr.StatusCode {
			case 404:
				return nil, fmt.Errorf("monitoring configuration %q not found for extension %q", configID, extensionName)
			case 403:
				return nil, fmt.Errorf("access denied to extension %q", extensionName)
			}
		}
		return nil, fmt.Errorf("failed to get monitoring configuration: %w", err)
	}

	var result MonitoringConfiguration
	if err := json.Unmarshal(resp.Body(), &result); err != nil {
		return nil, fmt.Errorf("get monitoring configuration: parse response: %w", err)
	}
	result.Type = "extension_monitoring_config"
	result.ExtensionName = extensionName
	return &result, nil
}

// MonitoringConfigurationCreate represents the body for creating/updating a monitoring configuration
type MonitoringConfigurationCreate struct {
	Scope string         `json:"scope,omitempty"`
	Value map[string]any `json:"value"`
}

// CreateMonitoringConfiguration creates a new monitoring configuration for an extension
func (h *Handler) CreateMonitoringConfiguration(ctx context.Context, extensionName string, body MonitoringConfigurationCreate) (*MonitoringConfiguration, error) {
	resp, err := h.client.HTTP().R().SetContext(ctx).
		SetBody(body).
		Post(fmt.Sprintf("/platform/extensions/v2/extensions/%s/monitoring-configurations", httpclient.PathSegment(extensionName)))
	if err != nil {
		return nil, fmt.Errorf("failed to create monitoring configuration: %w", err)
	}
	if err := httpclient.CheckResponse(resp); err != nil {
		var apiErr *httpclient.APIError
		if errors.As(err, &apiErr) {
			switch apiErr.StatusCode {
			case 404:
				return nil, fmt.Errorf("extension %q not found", extensionName)
			case 403:
				return nil, fmt.Errorf("access denied to extension %q", extensionName)
			}
		}
		return nil, fmt.Errorf("failed to create monitoring configuration: %w", err)
	}

	var result MonitoringConfiguration
	if err := json.Unmarshal(resp.Body(), &result); err != nil {
		return nil, fmt.Errorf("create monitoring configuration: parse response: %w", err)
	}
	return &result, nil
}

// UpdateMonitoringConfiguration updates an existing monitoring configuration for an extension
func (h *Handler) UpdateMonitoringConfiguration(ctx context.Context, extensionName, configID string, body MonitoringConfigurationCreate) (*MonitoringConfiguration, error) {
	resp, err := h.client.HTTP().R().SetContext(ctx).
		SetBody(body).
		Put(fmt.Sprintf("/platform/extensions/v2/extensions/%s/monitoring-configurations/%s", httpclient.PathSegment(extensionName), httpclient.PathSegment(configID)))
	if err != nil {
		return nil, fmt.Errorf("failed to update monitoring configuration: %w", err)
	}
	if err := httpclient.CheckResponse(resp); err != nil {
		var apiErr *httpclient.APIError
		if errors.As(err, &apiErr) {
			switch apiErr.StatusCode {
			case 404:
				return nil, fmt.Errorf("monitoring configuration %q not found for extension %q", configID, extensionName)
			case 403:
				return nil, fmt.Errorf("access denied to extension %q", extensionName)
			}
		}
		return nil, fmt.Errorf("failed to update monitoring configuration: %w", err)
	}

	var result MonitoringConfiguration
	if err := json.Unmarshal(resp.Body(), &result); err != nil {
		return nil, fmt.Errorf("update monitoring configuration: parse response: %w", err)
	}
	return &result, nil
}

// Upload uploads a custom extension zip file to the Dynatrace environment.
// The zipData should contain the raw bytes of the extension zip package.
// The fileName parameter is retained for API compatibility but is not used in the
// request; the bundle is sent as a raw application/octet-stream body as required
// by the DT Platform Extensions v2 API (multipart/form-data results in HTTP 415).
func (h *Handler) Upload(ctx context.Context, fileName string, zipData []byte) (*ExtensionVersion, error) {
	resp, err := h.client.HTTP().R().SetContext(ctx).
		SetHeader("Content-Type", "application/octet-stream").
		SetBody(zipData).
		Post("/platform/extensions/v2/extensions")
	if err != nil {
		return nil, fmt.Errorf("failed to upload extension: %w", err)
	}
	if err := httpclient.CheckResponse(resp); err != nil {
		var apiErr *httpclient.APIError
		if errors.As(err, &apiErr) {
			switch apiErr.StatusCode {
			case http.StatusBadRequest:
				return nil, fmt.Errorf("invalid extension package: %w", err)
			case http.StatusForbidden:
				return nil, fmt.Errorf("access denied: insufficient permissions to upload extensions")
			case http.StatusConflict:
				return nil, fmt.Errorf("extension version already exists: %w", err)
			}
		}
		return nil, fmt.Errorf("failed to upload extension: %w", err)
	}

	rawBody := resp.Body()
	var result ExtensionVersion
	if err := json.Unmarshal(rawBody, &result); err != nil {
		return nil, fmt.Errorf("upload extension: parse response: %w (body: %.512s)", err, rawBody)
	}
	return &result, nil
}

// InstallFromHub installs a Dynatrace Hub extension into the environment using the
// Extensions 2.0 API. extensionName is the hub extension catalog ID (path parameter).
// version is optional -- when provided it is sent as a query parameter to select a
// specific release; when empty the API resolves the latest available version.
func (h *Handler) InstallFromHub(ctx context.Context, extensionName, version string) (*ExtensionVersion, error) {
	req := h.client.HTTP().R().SetContext(ctx)
	if version != "" {
		req.SetQueryParam("version", version)
	}

	resp, err := req.Post(fmt.Sprintf("/platform/extensions/v2/extensions/%s", httpclient.PathSegment(extensionName)))
	if err != nil {
		return nil, fmt.Errorf("failed to install Hub extension %q: %w", extensionName, err)
	}
	if err := httpclient.CheckResponse(resp); err != nil {
		var apiErr *httpclient.APIError
		if errors.As(err, &apiErr) {
			switch apiErr.StatusCode {
			case http.StatusNotFound:
				return nil, fmt.Errorf("hub extension %q not found", extensionName)
			case http.StatusForbidden:
				return nil, fmt.Errorf("access denied: insufficient permissions to install extensions")
			case http.StatusConflict:
				if version == "" {
					return nil, fmt.Errorf("hub extension %q (latest version) is already installed", extensionName)
				}
				return nil, fmt.Errorf("hub extension %q version %q is already installed", extensionName, version)
			}
		}
		return nil, fmt.Errorf("failed to install Hub extension %q: %w", extensionName, err)
	}

	var result ExtensionVersion
	if err := json.Unmarshal(resp.Body(), &result); err != nil {
		return nil, fmt.Errorf("install hub extension: parse response: %w", err)
	}
	return &result, nil
}

// Download downloads the extension zip package for a specific version.
// The returned bytes are the raw zip file contents.
func (h *Handler) Download(ctx context.Context, extensionName, version string) ([]byte, error) {
	resp, err := h.client.HTTP().R().SetContext(ctx).
		SetHeader("Accept", "application/octet-stream").
		Get(fmt.Sprintf("/platform/extensions/v2/extensions/%s/%s", httpclient.PathSegment(extensionName), httpclient.PathSegment(version)))
	if err != nil {
		return nil, fmt.Errorf("failed to download extension: %w", err)
	}
	if err := httpclient.CheckResponse(resp); err != nil {
		var apiErr *httpclient.APIError
		if errors.As(err, &apiErr) {
			switch apiErr.StatusCode {
			case http.StatusNotFound:
				return nil, fmt.Errorf("extension %q version %q not found", extensionName, version)
			case http.StatusForbidden:
				return nil, fmt.Errorf("access denied to extension %q", extensionName)
			}
		}
		return nil, fmt.Errorf("failed to download extension: %w", err)
	}
	ct := resp.Header().Get("Content-Type")
	if ct != "" && !strings.HasPrefix(ct, "application/zip") && !strings.HasPrefix(ct, "application/octet-stream") {
		return nil, fmt.Errorf("unexpected Content-Type %q from extension download (expected zip/octet-stream)", ct)
	}
	return resp.Body(), nil
}

// DeleteMonitoringConfiguration deletes a monitoring configuration for an extension
func (h *Handler) DeleteMonitoringConfiguration(ctx context.Context, extensionName, configID string) error {
	resp, err := h.client.HTTP().R().SetContext(ctx).
		Delete(fmt.Sprintf("/platform/extensions/v2/extensions/%s/monitoring-configurations/%s", httpclient.PathSegment(extensionName), httpclient.PathSegment(configID)))
	if err != nil {
		return fmt.Errorf("failed to delete monitoring configuration: %w", err)
	}
	if err := httpclient.CheckResponse(resp); err != nil {
		var apiErr *httpclient.APIError
		if errors.As(err, &apiErr) {
			switch apiErr.StatusCode {
			case 404:
				return fmt.Errorf("monitoring configuration %q not found for extension %q", configID, extensionName)
			case 403:
				return fmt.Errorf("access denied to extension %q", extensionName)
			}
		}
		return fmt.Errorf("failed to delete monitoring configuration: %w", err)
	}
	return nil
}

// GetMonitoringConfigurationSchema retrieves the monitoring configuration schema for a specific
// extension version. The schema is an arbitrary JSON Schema document returned verbatim.
func (h *Handler) GetMonitoringConfigurationSchema(ctx context.Context, extensionName, version string) (json.RawMessage, error) {
	resp, err := h.client.HTTP().R().SetContext(ctx).
		Get(fmt.Sprintf("/platform/extensions/v2/extensions/%s/%s/schema", httpclient.PathSegment(extensionName), httpclient.PathSegment(version)))
	if err != nil {
		return nil, fmt.Errorf("failed to get monitoring configuration schema: %w", err)
	}
	if err := httpclient.CheckResponse(resp); err != nil {
		var apiErr *httpclient.APIError
		if errors.As(err, &apiErr) {
			switch apiErr.StatusCode {
			case http.StatusNotFound:
				return nil, fmt.Errorf("extension %q version %q not found", extensionName, version)
			case http.StatusForbidden:
				return nil, fmt.Errorf("access denied to extension %q", extensionName)
			}
		}
		return nil, fmt.Errorf("failed to get monitoring configuration schema: %w", err)
	}
	return json.RawMessage(resp.Body()), nil
}

// GetActiveGateGroups retrieves the active gate groups available for a specific extension version.
func (h *Handler) GetActiveGateGroups(ctx context.Context, extensionName, version string) (*ActiveGateGroupList, error) {
	resp, err := h.client.HTTP().R().SetContext(ctx).
		Get(fmt.Sprintf("/platform/extensions/v2/extensions/%s/%s/active-gate-groups", httpclient.PathSegment(extensionName), httpclient.PathSegment(version)))
	if err != nil {
		return nil, fmt.Errorf("failed to get active gate groups: %w", err)
	}
	if err := httpclient.CheckResponse(resp); err != nil {
		var apiErr *httpclient.APIError
		if errors.As(err, &apiErr) {
			switch apiErr.StatusCode {
			case http.StatusNotFound:
				return nil, fmt.Errorf("extension %q version %q not found", extensionName, version)
			case http.StatusForbidden:
				return nil, fmt.Errorf("access denied to extension %q", extensionName)
			}
		}
		return nil, fmt.Errorf("failed to get active gate groups: %w", err)
	}

	var result ActiveGateGroupList
	if err := json.Unmarshal(resp.Body(), &result); err != nil {
		return nil, fmt.Errorf("get active gate groups: parse response: %w", err)
	}
	return &result, nil
}
