package azuremonitoringconfig

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/Dynatrace/dtctl/pkg/client"
	"github.com/Dynatrace/dtctl/sdk/httpclient"
)

// ErrNotFound reports that no monitoring configuration matches the name. It
// separates "absent" from "the lookup itself failed", which apply needs to
// decide between create and abort.
var ErrNotFound = errors.New("monitoring config not found")

const (
	ExtensionName      = "com.dynatrace.extension.da-azure"
	BaseAPI            = "/platform/extensions/v2/extensions/" + ExtensionName + "/monitoring-configurations"
	ExtensionAPI       = "/platform/extensions/v2/extensions/" + ExtensionName
	ExtensionSchemaAPI = ExtensionAPI + "/%s/schema"
)

type Handler struct {
	client *client.Client
}

func NewHandler(c *client.Client) *Handler {
	return &Handler{client: c}
}

type ExtensionResponse struct {
	// API currently returns "items" which are the versions of the extension or a list of metadata?
	// The Python snippet suggests: response JSON has 'items' -> each item has 'version'.
	Items []ExtensionItem `json:"items"`
}

type ExtensionItem struct {
	Version string `json:"version"`
}

type SchemaEnumItem struct {
	Value string `json:"value"`
}

type SchemaEnum struct {
	Items []SchemaEnumItem `json:"items"`
}

type ExtensionSchemaResponse struct {
	Enums map[string]SchemaEnum `json:"enums"`
}

type Location struct {
	Value string `json:"value" table:"LOCATION"`
}

type FeatureSet struct {
	Value string `json:"value" table:"FEATURE_SET"`
}

type AzureMonitoringConfig struct {
	ObjectID string `json:"objectId,omitempty" table:"ID"`
	Scope    string `json:"scope,omitempty"`
	Value    Value  `json:"value" table:"-"`

	// Flattened fields for table view
	Description string `json:"description" table:"DESCRIPTION"`
	Enabled     bool   `json:"enabled" table:"ENABLED"`
	Version     string `json:"version" table:"VERSION"`
}

type Value struct {
	Enabled     bool        `json:"enabled"`
	Description string      `json:"description"`
	Version     string      `json:"version"`
	Azure       AzureConfig `json:"azure"`
	FeatureSets []string    `json:"featureSets"`

	// Extra keeps the members this struct does not model (unknown_fields.go).
	Extra map[string]json.RawMessage `json:"-" yaml:"-" table:"-"`
}

type AzureConfig struct {
	UseIngestEnrichmentConfig          *bool             `json:"useIngestEnrichmentConfig,omitempty" yaml:"useIngestEnrichmentConfig,omitempty"`
	IngestEnrichmentMigrationProcessed *bool             `json:"ingestEnrichmentMigrationProcessed,omitempty" yaml:"ingestEnrichmentMigrationProcessed,omitempty"`
	DeploymentScope                    string            `json:"deploymentScope,omitempty"`
	SubscriptionFilteringMode          string            `json:"subscriptionFilteringMode,omitempty"`
	Credentials                        []Credential      `json:"credentials"`
	LocationFiltering                  []string          `json:"locationFiltering,omitempty"`
	ConfigurationMode                  string            `json:"configurationMode,omitempty"`
	DeploymentMode                     string            `json:"deploymentMode,omitempty"`
	TagFiltering                       []TagFilter       `json:"tagFiltering,omitempty"`
	TagEnrichment                      []string          `json:"tagEnrichment,omitempty"`
	DtLabelsEnrichment                 map[string]Labels `json:"dtLabelsEnrichment,omitempty"`

	// Extra keeps the members this struct does not model (unknown_fields.go).
	Extra map[string]json.RawMessage `json:"-" yaml:"-" table:"-"`
}

type TagFilter struct {
	Key       string `json:"key"`
	Value     string `json:"value"`
	Condition string `json:"condition"`

	// Extra keeps the members this struct does not model (unknown_fields.go).
	Extra map[string]json.RawMessage `json:"-" yaml:"-" table:"-"`
}

type Labels struct {
	Literal string `json:"literal,omitempty"`
	TagKey  string `json:"tagKey,omitempty"`

	// Extra keeps the members this struct does not model (unknown_fields.go).
	Extra map[string]json.RawMessage `json:"-" yaml:"-" table:"-"`
}

type Credential struct {
	Enabled            bool   `json:"enabled"`
	Description        string `json:"description"`
	ConnectionId       string `json:"connectionId"`
	ServicePrincipalId string `json:"servicePrincipalId"`
	Type               string `json:"type"`

	// Extra keeps the members this struct does not model (unknown_fields.go).
	Extra map[string]json.RawMessage `json:"-" yaml:"-" table:"-"`
}

type ListResponse struct {
	Items       []AzureMonitoringConfig `json:"items"`
	NextPageKey string                  `json:"nextPageKey,omitempty"`
}

func (h *Handler) GetLatestVersion() (string, error) {
	var result ExtensionResponse
	resp, err := h.client.HTTP().R().SetResult(&result).Get(ExtensionAPI)
	if err != nil {
		return "", fmt.Errorf("failed to fetch extension versions: %w", err)
	}
	if resp.IsError() {
		return "", fmt.Errorf("failed to fetch extension versions: %s", resp.String())
	}

	versions := make([]string, 0, len(result.Items))
	for _, item := range result.Items {
		if item.Version != "" {
			versions = append(versions, item.Version)
		}
	}

	if len(versions) == 0 {
		return "", fmt.Errorf("no versions found for extension %s", ExtensionName)
	}

	sort.Slice(versions, func(i, j int) bool {
		return compareVersion(versions[i], versions[j]) > 0
	})

	return versions[0], nil
}

func compareVersion(a, b string) int {
	aParts := strings.Split(a, ".")
	bParts := strings.Split(b, ".")
	maxLen := len(aParts)
	if len(bParts) > maxLen {
		maxLen = len(bParts)
	}

	for idx := 0; idx < maxLen; idx++ {
		aVal := 0
		if idx < len(aParts) {
			aVal, _ = strconv.Atoi(aParts[idx])
		}
		bVal := 0
		if idx < len(bParts) {
			bVal, _ = strconv.Atoi(bParts[idx])
		}
		if aVal > bVal {
			return 1
		}
		if aVal < bVal {
			return -1
		}
	}

	return 0
}

func (h *Handler) ListAvailableLocations() ([]Location, error) {
	latestVersion, err := h.GetLatestVersion()
	if err != nil {
		return nil, fmt.Errorf("failed to determine latest extension version: %w", err)
	}

	var schema ExtensionSchemaResponse
	schemaEndpoint := fmt.Sprintf(ExtensionSchemaAPI, httpclient.PathSegment(latestVersion))
	resp, err := h.client.HTTP().R().SetResult(&schema).Get(schemaEndpoint)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch extension schema: %w", err)
	}
	if resp.IsError() {
		return nil, fmt.Errorf("failed to fetch extension schema: %s", resp.String())
	}

	locationEnum, ok := schema.Enums["dynatrace.datasource.azure:location"]
	if !ok {
		return nil, fmt.Errorf("schema enum %q not found", "dynatrace.datasource.azure:location")
	}

	locations := make([]Location, 0, len(locationEnum.Items))
	for _, item := range locationEnum.Items {
		if item.Value != "" {
			locations = append(locations, Location{Value: item.Value})
		}
	}

	if len(locations) == 0 {
		return nil, fmt.Errorf("no locations found in schema enum %q", "dynatrace.datasource.azure:location")
	}

	return locations, nil
}

func (h *Handler) ListAvailableFeatureSets() ([]FeatureSet, error) {
	latestVersion, err := h.GetLatestVersion()
	if err != nil {
		return nil, fmt.Errorf("failed to determine latest extension version: %w", err)
	}

	var schema ExtensionSchemaResponse
	schemaEndpoint := fmt.Sprintf(ExtensionSchemaAPI, httpclient.PathSegment(latestVersion))
	resp, err := h.client.HTTP().R().SetResult(&schema).Get(schemaEndpoint)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch extension schema: %w", err)
	}
	if resp.IsError() {
		return nil, fmt.Errorf("failed to fetch extension schema: %s", resp.String())
	}

	featureSetEnum, ok := schema.Enums["FeatureSetsType"]
	if !ok {
		return nil, fmt.Errorf("schema enum %q not found", "FeatureSetsType")
	}

	featureSets := make([]FeatureSet, 0, len(featureSetEnum.Items))
	for _, item := range featureSetEnum.Items {
		if item.Value != "" {
			featureSets = append(featureSets, FeatureSet{Value: item.Value})
		}
	}

	if len(featureSets) == 0 {
		return nil, fmt.Errorf("no feature sets found in schema enum %q", "FeatureSetsType")
	}

	sort.Slice(featureSets, func(i, j int) bool {
		return featureSets[i].Value < featureSets[j].Value
	})

	return featureSets, nil
}

func (h *Handler) GetRaw(id string) ([]byte, error) {
	result, err := h.Get(id)
	if err != nil {
		return nil, err
	}
	return json.MarshalIndent(result.Value, "", "  ")
}

func (h *Handler) Get(id string) (*AzureMonitoringConfig, error) {
	var result AzureMonitoringConfig
	req := h.client.HTTP().R().SetResult(&result)
	resp, err := req.Get(fmt.Sprintf("%s/%s", BaseAPI, httpclient.PathSegment(id)))
	if err != nil {
		return nil, err
	}
	if resp.IsError() {
		return nil, fmt.Errorf("failed to get azure_monitoring_config: %s", resp.String())
	}

	result.Description = result.Value.Description
	result.Enabled = result.Value.Enabled
	result.Version = result.Value.Version

	return &result, nil
}

func (h *Handler) List() ([]AzureMonitoringConfig, error) {
	var allItems []AzureMonitoringConfig
	nextPageKey := ""

	for {
		var result ListResponse
		req := h.client.HTTP().R().SetResult(&result)

		client.PaginationParams{
			Style:        client.PaginationDefault,
			PageKeyParam: "next-page-key",
			NextPageKey:  nextPageKey,
		}.Apply(req)

		resp, err := req.Get(BaseAPI)
		if err != nil {
			return nil, err
		}
		if resp.IsError() {
			return nil, fmt.Errorf("failed to list azure_monitoring_configs: %s", resp.String())
		}

		for i := range result.Items {
			result.Items[i].Description = result.Items[i].Value.Description
			result.Items[i].Enabled = result.Items[i].Value.Enabled
			result.Items[i].Version = result.Items[i].Value.Version
		}
		allItems = append(allItems, result.Items...)

		if result.NextPageKey == "" {
			break
		}
		nextPageKey = result.NextPageKey
	}
	return allItems, nil
}

// FindByName finds an Azure monitoring config by description (name)
func (h *Handler) FindByName(name string) (*AzureMonitoringConfig, error) {
	items, err := h.List()
	if err != nil {
		return nil, err
	}
	for i := range items {
		// Matching by Description as it serves as the name
		if items[i].Description == name {
			return &items[i], nil
		}
	}
	return nil, fmt.Errorf("azure monitoring config with description %q: %w", name, ErrNotFound)
}

// Create creates a new Azure monitoring config
func (h *Handler) Create(data []byte) (*AzureMonitoringConfig, error) {
	var result AzureMonitoringConfig
	resp, err := h.client.HTTP().R().
		SetHeader("Content-Type", "application/json").
		SetBody(data).
		SetResult(&result).
		Post(BaseAPI)

	if err != nil {
		return nil, fmt.Errorf("failed to create azure_monitoring_config: %w", err)
	}
	if resp.IsError() {
		return nil, fmt.Errorf("failed to create azure_monitoring_config: %s", resp.String())
	}

	return &result, nil
}

// Update updates an existing Azure monitoring config
func (h *Handler) Update(id string, data []byte) (*AzureMonitoringConfig, error) {
	var result AzureMonitoringConfig
	resp, err := h.client.HTTP().R().
		SetHeader("Content-Type", "application/json").
		SetBody(data).
		SetResult(&result).
		Put(fmt.Sprintf("%s/%s", BaseAPI, httpclient.PathSegment(id)))

	if err != nil {
		return nil, fmt.Errorf("failed to update azure_monitoring_config: %w", err)
	}
	if resp.IsError() {
		return nil, fmt.Errorf("failed to update azure_monitoring_config: %s", resp.String())
	}

	return &result, nil
}

// Delete deletes an Azure monitoring config by ID
func (h *Handler) Delete(id string) error {
	resp, err := h.client.HTTP().R().Delete(fmt.Sprintf("%s/%s", BaseAPI, httpclient.PathSegment(id)))
	if err != nil {
		return err
	}
	if resp.IsError() {
		return fmt.Errorf("failed to delete azure_monitoring_config: status %d: %s", resp.StatusCode(), resp.String())
	}
	return nil
}
