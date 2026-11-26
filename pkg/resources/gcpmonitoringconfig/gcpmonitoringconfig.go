package gcpmonitoringconfig

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
	ExtensionName      = "com.dynatrace.extension.da-gcp"
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

type GCPMonitoringConfig struct {
	ObjectID string `json:"objectId,omitempty" table:"ID"`
	Scope    string `json:"scope,omitempty"`
	Value    Value  `json:"value" table:"-"`

	Description string `json:"description" table:"DESCRIPTION"`
	Enabled     bool   `json:"enabled" table:"ENABLED"`
	Version     string `json:"version" table:"VERSION"`
}

type Value struct {
	Enabled     bool              `json:"enabled"`
	Description string            `json:"description"`
	Version     string            `json:"version"`
	GoogleCloud GoogleCloudConfig `json:"googleCloud"`
	FeatureSets []string          `json:"featureSets"`

	// Extra keeps the members this struct does not model (unknown_fields.go).
	Extra map[string]json.RawMessage `json:"-" yaml:"-" table:"-"`
}

type GoogleCloudConfig struct {
	UseIngestEnrichmentConfig          *bool                     `json:"useIngestEnrichmentConfig,omitempty" yaml:"useIngestEnrichmentConfig,omitempty"`
	IngestEnrichmentMigrationProcessed *bool                     `json:"ingestEnrichmentMigrationProcessed,omitempty" yaml:"ingestEnrichmentMigrationProcessed,omitempty"`
	Credentials                        []Credential              `json:"credentials"`
	LocationFiltering                  []string                  `json:"locationFiltering,omitempty"`
	ProjectFiltering                   []string                  `json:"projectFiltering,omitempty"`
	FolderFiltering                    []string                  `json:"folderFiltering,omitempty"`
	TagFiltering                       []TagFilter               `json:"tagFiltering,omitempty"`
	LabelFiltering                     []TagFilter               `json:"labelFiltering,omitempty"`
	TagEnrichment                      []string                  `json:"tagEnrichment,omitempty"`
	LabelEnrichment                    []string                  `json:"labelEnrichment,omitempty"`
	DtLabelsEnrichment                 map[string]DtLabelMapping `json:"dtLabelsEnrichment,omitempty" yaml:"dtLabelsEnrichment,omitempty"`
	ObservabilityScopesEnabled         *bool                     `json:"observabilityScopesEnabled,omitempty"`
	SmartscapeConfiguration            FlagConfig                `json:"smartscapeConfiguration,omitempty"`
	Resources                          []MetricSource            `json:"resources,omitempty"`

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

type DtLabelMapping struct {
	Literal  string `json:"literal,omitempty"`
	TagKey   string `json:"tagKey,omitempty"`
	LabelKey string `json:"labelKey,omitempty"`

	// Extra keeps the members this struct does not model (unknown_fields.go).
	Extra map[string]json.RawMessage `json:"-" yaml:"-" table:"-"`
}

type FlagConfig struct {
	Enabled bool `json:"enabled"`

	// Extra keeps the members this struct does not model (unknown_fields.go).
	Extra map[string]json.RawMessage `json:"-" yaml:"-" table:"-"`
}

type MetricSource struct {
	ResourceType                   string   `json:"resourceType"`
	AutoDiscoveryEnabled           bool     `json:"autoDiscoveryEnabled"`
	AutodiscoveryExcludeMetricType []string `json:"autodiscoveryExcludeMetricType,omitempty"`
	Metrics                        []Metric `json:"metrics,omitempty"`

	// Extra keeps the members this struct does not model (unknown_fields.go).
	Extra map[string]json.RawMessage `json:"-" yaml:"-" table:"-"`
}

// Metric is a single Cloud Monitoring metric collected for a resource type,
// used when auto-discovery is off or to add metrics beyond the recommended set.
type Metric struct {
	Name         string   `json:"name"`
	MetricLabels []string `json:"metricLabels,omitempty"`
	Type         string   `json:"type,omitempty"`

	// Extra keeps the members this struct does not model (unknown_fields.go).
	Extra map[string]json.RawMessage `json:"-" yaml:"-" table:"-"`
}

type Credential struct {
	Description    string `json:"description"`
	Enabled        bool   `json:"enabled"`
	ConnectionID   string `json:"connectionId"`
	ServiceAccount string `json:"serviceAccount"`

	// Extra keeps the members this struct does not model (unknown_fields.go).
	Extra map[string]json.RawMessage `json:"-" yaml:"-" table:"-"`
}

type ListResponse struct {
	Items       []GCPMonitoringConfig `json:"items"`
	NextPageKey string                `json:"nextPageKey,omitempty"`
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

	locationEnum, ok := schema.Enums["dynatrace.datasource.gcp:location"]
	if !ok {
		return nil, fmt.Errorf("schema enum %q not found", "dynatrace.datasource.gcp:location")
	}

	locations := make([]Location, 0, len(locationEnum.Items))
	for _, item := range locationEnum.Items {
		if item.Value != "" {
			locations = append(locations, Location{Value: item.Value})
		}
	}
	if len(locations) == 0 {
		return nil, fmt.Errorf("no locations found in schema enum %q", "dynatrace.datasource.gcp:location")
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

func (h *Handler) Get(id string) (*GCPMonitoringConfig, error) {
	var result GCPMonitoringConfig
	resp, err := h.client.HTTP().R().SetResult(&result).Get(fmt.Sprintf("%s/%s", BaseAPI, httpclient.PathSegment(id)))
	if err != nil {
		return nil, err
	}
	if resp.IsError() {
		return nil, fmt.Errorf("failed to get gcp_monitoring_config: %s", resp.String())
	}

	result.Description = result.Value.Description
	result.Enabled = result.Value.Enabled
	result.Version = result.Value.Version

	return &result, nil
}

func (h *Handler) List() ([]GCPMonitoringConfig, error) {
	var allItems []GCPMonitoringConfig
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
			return nil, fmt.Errorf("failed to list gcp_monitoring_configs: %s", resp.String())
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

func (h *Handler) FindByName(name string) (*GCPMonitoringConfig, error) {
	items, err := h.List()
	if err != nil {
		return nil, err
	}
	for i := range items {
		if items[i].Description == name {
			return &items[i], nil
		}
	}
	return nil, fmt.Errorf("GCP monitoring config with description %q: %w", name, ErrNotFound)
}

func (h *Handler) Create(data []byte) (*GCPMonitoringConfig, error) {
	var result GCPMonitoringConfig
	resp, err := h.client.HTTP().R().
		SetHeader("Content-Type", "application/json").
		SetBody(data).
		SetResult(&result).
		Post(BaseAPI)
	if err != nil {
		return nil, fmt.Errorf("failed to create gcp_monitoring_config: %w", err)
	}
	if resp.IsError() {
		return nil, fmt.Errorf("failed to create gcp_monitoring_config: %s", resp.String())
	}

	return &result, nil
}

func (h *Handler) Update(id string, data []byte) (*GCPMonitoringConfig, error) {
	var result GCPMonitoringConfig
	resp, err := h.client.HTTP().R().
		SetHeader("Content-Type", "application/json").
		SetBody(data).
		SetResult(&result).
		Put(fmt.Sprintf("%s/%s", BaseAPI, httpclient.PathSegment(id)))
	if err != nil {
		return nil, fmt.Errorf("failed to update gcp_monitoring_config: %w", err)
	}
	if resp.IsError() {
		return nil, fmt.Errorf("failed to update gcp_monitoring_config: %s", resp.String())
	}

	return &result, nil
}

func (h *Handler) Delete(id string) error {
	resp, err := h.client.HTTP().R().Delete(fmt.Sprintf("%s/%s", BaseAPI, httpclient.PathSegment(id)))
	if err != nil {
		return err
	}
	if resp.IsError() {
		return fmt.Errorf("failed to delete gcp_monitoring_config: status %d: %s", resp.StatusCode(), resp.String())
	}
	return nil
}
