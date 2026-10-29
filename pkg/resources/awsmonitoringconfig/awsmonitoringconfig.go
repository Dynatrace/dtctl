// Package awsmonitoringconfig manages monitoring configurations for the
// Dynatrace AWS data acquisition extension (com.dynatrace.extension.da-aws).
package awsmonitoringconfig

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
	ExtensionName      = "com.dynatrace.extension.da-aws"
	BaseAPI            = "/platform/extensions/v2/extensions/" + ExtensionName + "/monitoring-configurations"
	ExtensionAPI       = "/platform/extensions/v2/extensions/" + ExtensionName
	ExtensionSchemaAPI = ExtensionAPI + "/%s/schema"

	// HubAddToEnvironmentAPI bootstraps the extension for the tenant when no
	// versions are present yet. See:
	// https://docs.dynatrace.com/docs/ingest-from/amazon-web-services/create-an-aws-connection/aws-connection-api-cli
	HubAddToEnvironmentAPI = "/api/v2/hub/extensions2/" + ExtensionName + "/actions/addToEnvironment"

	// DefaultActivationContext mirrors the value used by the official UI flow
	// and visible in real monitoring configurations.
	DefaultActivationContext = "DATA_ACQUISITION"

	// Default scope for AWS monitoring configurations.
	DefaultScope = "integration-aws"

	// Region enum key in the extension schema.
	regionEnumKey = "dynatrace.datasource.aws:region"
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

type Region struct {
	Value string `json:"value" table:"REGION"`
}

type FeatureSet struct {
	Value string `json:"value" table:"FEATURE_SET"`
}

type AWSMonitoringConfig struct {
	ObjectID string `json:"objectId,omitempty" table:"ID"`
	Scope    string `json:"scope,omitempty"`
	Value    Value  `json:"value" table:"-"`

	// Flattened fields for table view.
	Description string `json:"description" table:"DESCRIPTION"`
	Enabled     bool   `json:"enabled" table:"ENABLED"`
	Version     string `json:"version" table:"VERSION"`
}

type Value struct {
	Enabled           bool      `json:"enabled"`
	Description       string    `json:"description"`
	Version           string    `json:"version"`
	ActivationContext string    `json:"activationContext,omitempty"`
	Aws               AWSConfig `json:"aws"`
	FeatureSets       []string  `json:"featureSets"`

	// Extra keeps the members this struct does not model (unknown_fields.go).
	Extra map[string]json.RawMessage `json:"-" yaml:"-" table:"-"`
}

type AWSConfig struct {
	UseIngestEnrichmentConfig          *bool                     `json:"useIngestEnrichmentConfig,omitempty" yaml:"useIngestEnrichmentConfig,omitempty"`
	IngestEnrichmentMigrationProcessed *bool                     `json:"ingestEnrichmentMigrationProcessed,omitempty" yaml:"ingestEnrichmentMigrationProcessed,omitempty"`
	DeploymentRegion                   string                    `json:"deploymentRegion,omitempty"`
	Credentials                        []Credential              `json:"credentials"`
	RegionFiltering                    []string                  `json:"regionFiltering"`
	TagFiltering                       []TagFilter               `json:"tagFiltering"`
	TagEnrichment                      []string                  `json:"tagEnrichment"`
	DtLabelsEnrichment                 map[string]DtLabelMapping `json:"dtLabelsEnrichment,omitempty"`
	SmartscapeConfiguration            FlagConfig                `json:"smartscapeConfiguration"`
	MetricsConfiguration               RegionalFlagConfig        `json:"metricsConfiguration"`
	CloudWatchLogsConfiguration        RegionalFlagConfig        `json:"cloudWatchLogsConfiguration"`
	Namespaces                         []CustomNamespace         `json:"namespaces"`
	ConfigurationMode                  string                    `json:"configurationMode,omitempty"`
	DeploymentMode                     string                    `json:"deploymentMode,omitempty"`
	DeploymentScope                    string                    `json:"deploymentScope,omitempty"`
	ManualDeploymentStatus             string                    `json:"manualDeploymentStatus,omitempty"`
	AutomatedDeploymentStatus          string                    `json:"automatedDeploymentStatus,omitempty"`

	// Extra keeps the members this struct does not model (unknown_fields.go).
	Extra map[string]json.RawMessage `json:"-" yaml:"-" table:"-"`
}

type Credential struct {
	Description  string `json:"description"`
	Enabled      bool   `json:"enabled"`
	ConnectionID string `json:"connectionId"`
	AccountID    string `json:"accountId,omitempty"`

	// Extra keeps the members this struct does not model (unknown_fields.go).
	Extra map[string]json.RawMessage `json:"-" yaml:"-" table:"-"`
}

type FlagConfig struct {
	Enabled bool `json:"enabled"`

	// Extra keeps the members this struct does not model (unknown_fields.go).
	Extra map[string]json.RawMessage `json:"-" yaml:"-" table:"-"`
}

type RegionalFlagConfig struct {
	Enabled bool     `json:"enabled"`
	Regions []string `json:"regions"`

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

// DtLabelMapping maps a Dynatrace label to either a literal value or an AWS
// tag key. Exactly one of Literal or TagKey should be set.
type DtLabelMapping struct {
	Literal string `json:"literal,omitempty"`
	TagKey  string `json:"tagKey,omitempty"`

	// Extra keeps the members this struct does not model (unknown_fields.go).
	Extra map[string]json.RawMessage `json:"-" yaml:"-" table:"-"`
}

// CustomNamespace defines a custom CloudWatch namespace with ad-hoc metrics
// that are not part of any built-in feature set.
type CustomNamespace struct {
	Namespace            string         `json:"namespace"`
	AutoDiscoveryEnabled bool           `json:"autoDiscoveryEnabled"`
	Metrics              []CustomMetric `json:"metrics"`

	// Extra keeps the members this struct does not model (unknown_fields.go).
	Extra map[string]json.RawMessage `json:"-" yaml:"-" table:"-"`
}

// CustomMetric defines a single CloudWatch metric within a CustomNamespace.
type CustomMetric struct {
	Name         string   `json:"name"`
	Unit         string   `json:"unit"`
	Dimensions   []string `json:"dimensions"`
	Aggregations []string `json:"aggregations"`
	Type         string   `json:"type,omitempty"` // e.g. "CUSTOM_AWS", "CUSTOM"

	// Extra keeps the members this struct does not model (unknown_fields.go).
	Extra map[string]json.RawMessage `json:"-" yaml:"-" table:"-"`
}

type ListResponse struct {
	Items       []AWSMonitoringConfig `json:"items"`
	NextPageKey string                `json:"nextPageKey,omitempty"`
}

// GetLatestVersion returns the highest semver version of the extension. Returns
// an error wrapping ErrExtensionNotInEnvironment when no versions exist yet.
var ErrExtensionNotInEnvironment = fmt.Errorf("extension %s is not yet added to this environment", ExtensionName)

func (h *Handler) GetLatestVersion() (string, error) {
	var result ExtensionResponse
	resp, err := h.client.HTTP().R().SetResult(&result).Get(ExtensionAPI)
	if err != nil {
		return "", fmt.Errorf("failed to fetch extension versions: %w", err)
	}
	if resp.StatusCode() == 404 {
		return "", ErrExtensionNotInEnvironment
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
		return "", ErrExtensionNotInEnvironment
	}

	sort.Slice(versions, func(i, j int) bool {
		return compareVersion(versions[i], versions[j]) > 0
	})
	return versions[0], nil
}

// AddToEnvironment registers the AWS extension in the tenant. This is required
// once before any monitoring configuration can be created.
func (h *Handler) AddToEnvironment() error {
	resp, err := h.client.HTTP().R().
		SetHeader("Accept", "application/json").
		Post(HubAddToEnvironmentAPI)
	if err != nil {
		return fmt.Errorf("failed to add extension %s to environment: %w", ExtensionName, err)
	}
	if resp.IsError() {
		return fmt.Errorf("failed to add extension %s to environment: status %d: %s", ExtensionName, resp.StatusCode(), resp.String())
	}
	return nil
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

func (h *Handler) ListAvailableRegions() ([]Region, error) {
	schema, err := h.fetchLatestSchema()
	if err != nil {
		return nil, err
	}
	enum, ok := schema.Enums[regionEnumKey]
	if !ok {
		return nil, fmt.Errorf("schema enum %q not found", regionEnumKey)
	}
	regions := make([]Region, 0, len(enum.Items))
	for _, item := range enum.Items {
		if item.Value != "" {
			regions = append(regions, Region{Value: item.Value})
		}
	}
	if len(regions) == 0 {
		return nil, fmt.Errorf("no regions found in schema enum %q", regionEnumKey)
	}
	return regions, nil
}

func (h *Handler) ListAvailableFeatureSets() ([]FeatureSet, error) {
	schema, err := h.fetchLatestSchema()
	if err != nil {
		return nil, err
	}
	enum, ok := schema.Enums["FeatureSetsType"]
	if !ok {
		return nil, fmt.Errorf("schema enum %q not found", "FeatureSetsType")
	}
	featureSets := make([]FeatureSet, 0, len(enum.Items))
	for _, item := range enum.Items {
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

func (h *Handler) fetchLatestSchema() (*ExtensionSchemaResponse, error) {
	latest, err := h.GetLatestVersion()
	if err != nil {
		return nil, fmt.Errorf("failed to determine latest extension version: %w", err)
	}
	var schema ExtensionSchemaResponse
	endpoint := fmt.Sprintf(ExtensionSchemaAPI, httpclient.PathSegment(latest))
	resp, err := h.client.HTTP().R().SetResult(&schema).Get(endpoint)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch extension schema: %w", err)
	}
	if resp.IsError() {
		return nil, fmt.Errorf("failed to fetch extension schema: %s", resp.String())
	}
	return &schema, nil
}

func (h *Handler) GetRaw(id string) ([]byte, error) {
	result, err := h.Get(id)
	if err != nil {
		return nil, err
	}
	return json.MarshalIndent(result.Value, "", "  ")
}

func (h *Handler) Get(id string) (*AWSMonitoringConfig, error) {
	var result AWSMonitoringConfig
	resp, err := h.client.HTTP().R().SetResult(&result).Get(fmt.Sprintf("%s/%s", BaseAPI, httpclient.PathSegment(id)))
	if err != nil {
		return nil, err
	}
	if resp.IsError() {
		return nil, fmt.Errorf("failed to get aws_monitoring_config: %s", resp.String())
	}
	flatten(&result)
	return &result, nil
}

func (h *Handler) List() ([]AWSMonitoringConfig, error) {
	var allItems []AWSMonitoringConfig
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
			return nil, fmt.Errorf("failed to list aws_monitoring_configs: %s", resp.String())
		}
		for i := range result.Items {
			flatten(&result.Items[i])
		}
		allItems = append(allItems, result.Items...)

		if result.NextPageKey == "" {
			break
		}
		nextPageKey = result.NextPageKey
	}
	return allItems, nil
}

func flatten(c *AWSMonitoringConfig) {
	c.Description = c.Value.Description
	c.Enabled = c.Value.Enabled
	c.Version = c.Value.Version
}

func (h *Handler) FindByName(name string) (*AWSMonitoringConfig, error) {
	items, err := h.List()
	if err != nil {
		return nil, err
	}
	for i := range items {
		if items[i].Description == name {
			return &items[i], nil
		}
	}
	return nil, fmt.Errorf("AWS monitoring config with description %q: %w", name, ErrNotFound)
}

func (h *Handler) Create(data []byte) (*AWSMonitoringConfig, error) {
	var result AWSMonitoringConfig
	resp, err := h.client.HTTP().R().
		SetHeader("Content-Type", "application/json").
		SetBody(data).
		SetResult(&result).
		Post(BaseAPI)
	if err != nil {
		return nil, fmt.Errorf("failed to create aws_monitoring_config: %w", err)
	}
	if resp.IsError() {
		return nil, fmt.Errorf("failed to create aws_monitoring_config: %s", resp.String())
	}
	return &result, nil
}

func (h *Handler) Update(id string, data []byte) (*AWSMonitoringConfig, error) {
	var result AWSMonitoringConfig
	resp, err := h.client.HTTP().R().
		SetHeader("Content-Type", "application/json").
		SetBody(data).
		SetResult(&result).
		Put(fmt.Sprintf("%s/%s", BaseAPI, httpclient.PathSegment(id)))
	if err != nil {
		return nil, fmt.Errorf("failed to update aws_monitoring_config: %w", err)
	}
	if resp.IsError() {
		return nil, fmt.Errorf("failed to update aws_monitoring_config: %s", resp.String())
	}
	return &result, nil
}

func (h *Handler) Delete(id string) error {
	resp, err := h.client.HTTP().R().Delete(fmt.Sprintf("%s/%s", BaseAPI, httpclient.PathSegment(id)))
	if err != nil {
		return err
	}
	if resp.IsError() {
		return fmt.Errorf("failed to delete aws_monitoring_config: status %d: %s", resp.StatusCode(), resp.String())
	}
	return nil
}
