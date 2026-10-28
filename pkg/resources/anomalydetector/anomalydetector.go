// Package anomalydetector provides a handler for custom anomaly detectors
// (Settings schema: builtin:davis.anomaly-detectors).
package anomalydetector

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/Dynatrace/dtctl/pkg/client"
	"github.com/Dynatrace/dtctl/pkg/diagnostic"
	"github.com/Dynatrace/dtctl/sdk/httpclient"
)

const (
	SchemaID    = "builtin:davis.anomaly-detectors"
	Scope       = "environment"
	SettingsAPI = "/platform/classic/environment-api/v2/settings/objects"

	// defaultSource is stamped on detectors whose definition omits "source".
	defaultSource = "dtctl"
)

// Handler handles anomaly detector resources.
type Handler struct {
	client *client.Client

	// defaultActor is used for executionSettings.actor when a definition omits
	// it. Callers that know the authenticated identity set it via
	// WithDefaultActor; it stays empty otherwise so the handler never issues an
	// identity lookup of its own.
	defaultActor string
}

// NewHandler creates a new anomaly detector handler.
func NewHandler(c *client.Client) *Handler {
	return &Handler{client: c}
}

// WithDefaultActor sets the UUID used for executionSettings.actor when the
// definition being created omits it. Some environments reject a detector
// without an actor ("executionSettings.actor: Must not be null"), others
// substitute the caller's identity server-side; filling it in with the
// authenticated identity makes the same definition work on both (issue #369).
func (h *Handler) WithDefaultActor(actor string) *Handler {
	h.defaultActor = actor
	return h
}

// AnomalyDetector represents a custom anomaly detector (builtin:davis.anomaly-detectors).
//
// The struct mixes table-display fields (Title, Enabled, AnalyzerShort, EventType,
// Source, Description — derived from Value) with the raw Settings payload (Value).
// Display fields are tagged with json:"-" so JSON/YAML output emits the raw Settings
// envelope ({schemaId, scope, value, objectId, schemaVersion}) — the same shape
// `apply` understands. This makes `dtctl get anomaly-detector -o json|yaml` round-
// trippable through `dtctl apply -f`, matching dashboard/notebook/SLO behavior.
//
// Custom MarshalJSON / MarshalYAML methods enforce the wire shape; struct field
// order and table tags are preserved for the table renderer.
type AnomalyDetector struct {
	ObjectID string `json:"-" table:"OBJECT ID,wide"`

	// Flattened fields for table display only — excluded from JSON/YAML
	// serialization (see MarshalJSON / MarshalYAML).
	Title         string `json:"-" table:"TITLE"`
	Enabled       bool   `json:"-" table:"ENABLED"`
	AnalyzerShort string `json:"-" table:"ANALYZER"`
	EventType     string `json:"-" table:"EVENT TYPE"`
	Source        string `json:"-" table:"SOURCE"`
	Description   string `json:"-" table:"DESCRIPTION,wide"`

	// Full value for describe and round-trippable JSON/YAML output.
	Value map[string]any `json:"-" table:"-"`

	// Raw settings metadata (not shown in table)
	SchemaVersion string `json:"-" table:"-"`
}

// rawSettingsEnvelope is the wire shape emitted by MarshalJSON / MarshalYAML.
// It matches the Settings API GET-by-id response and is accepted as-is by
// `dtctl apply -f` via the schemaId detection branch in pkg/apply/applier.go.
type rawSettingsEnvelope struct {
	SchemaID      string         `json:"schemaId" yaml:"schemaId"`
	SchemaVersion string         `json:"schemaVersion,omitempty" yaml:"schemaVersion,omitempty"`
	Scope         string         `json:"scope" yaml:"scope"`
	ObjectID      string         `json:"objectId,omitempty" yaml:"objectId,omitempty"`
	Value         map[string]any `json:"value" yaml:"value"`
}

// envelope builds the round-trippable wire shape from the receiver.
func (a AnomalyDetector) envelope() rawSettingsEnvelope {
	return rawSettingsEnvelope{
		SchemaID:      SchemaID,
		SchemaVersion: a.SchemaVersion,
		Scope:         Scope,
		ObjectID:      a.ObjectID,
		Value:         a.Value,
	}
}

// MarshalJSON emits the raw Settings envelope so `dtctl get -o json` output is
// directly consumable by `dtctl apply -f`. Display-only fields (AnalyzerShort,
// EventType, ...) are derived from Value and are intentionally omitted to avoid
// producing a hybrid shape that neither matches the Settings API nor the
// flattened authoring format.
func (a AnomalyDetector) MarshalJSON() ([]byte, error) {
	return json.Marshal(a.envelope())
}

// MarshalYAML mirrors MarshalJSON for `-o yaml` output.
func (a AnomalyDetector) MarshalYAML() (interface{}, error) {
	return a.envelope(), nil
}

// ListOptions configures listing behavior.
type ListOptions struct {
	Enabled *bool // nil = no filter, true = enabled only, false = disabled only
}

// listResponse is the raw Settings API response shape.
type listResponse struct {
	Items       []settingsItem `json:"items"`
	TotalCount  int            `json:"totalCount"`
	NextPageKey string         `json:"nextPageKey,omitempty"`
}

// settingsItem is the raw Settings API item shape.
type settingsItem struct {
	ObjectID      string         `json:"objectId"`
	SchemaID      string         `json:"schemaId"`
	SchemaVersion string         `json:"schemaVersion"`
	Scope         string         `json:"scope"`
	Value         map[string]any `json:"value"`
}

// createResponse is the response from Settings API create operations.
type createResponse struct {
	ObjectID string `json:"objectId"`
	Code     int    `json:"code,omitempty"`
	Error    *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error,omitempty"`
}

// flatten converts a raw settings item into an AnomalyDetector with flattened table fields.
func flatten(item settingsItem) AnomalyDetector {
	ad := AnomalyDetector{
		ObjectID:      item.ObjectID,
		Value:         item.Value,
		SchemaVersion: item.SchemaVersion,
	}

	if v, ok := item.Value["title"].(string); ok {
		ad.Title = v
	}
	if v, ok := item.Value["enabled"].(bool); ok {
		ad.Enabled = v
	}
	if v, ok := item.Value["description"].(string); ok {
		ad.Description = v
	}
	if v, ok := item.Value["source"].(string); ok {
		ad.Source = v
	}

	// Derive analyzer short form
	ad.AnalyzerShort = deriveAnalyzerShort(item.Value)

	// Derive event type
	ad.EventType = deriveEventType(item.Value)

	return ad
}

// deriveAnalyzerShort produces the compact analyzer display string.
// E.g., "static (>90)", "auto-adaptive"
func deriveAnalyzerShort(value map[string]any) string {
	analyzer, ok := value["analyzer"].(map[string]any)
	if !ok {
		return ""
	}

	name, _ := analyzer["name"].(string)
	input := ExtractKVMap(analyzer, "input")

	switch {
	case strings.Contains(name, "StaticThreshold"):
		condition := input["alertCondition"]
		threshold := input["threshold"]
		op := ">"
		if condition == "BELOW" {
			op = "<"
		}
		if threshold != "" {
			return fmt.Sprintf("static (%s%s)", op, threshold)
		}
		return "static"
	case strings.Contains(name, "AutoAdaptive"):
		return "auto-adaptive"
	default:
		// Unknown analyzer — show abbreviated name
		parts := strings.Split(name, ".")
		if len(parts) > 0 {
			return parts[len(parts)-1]
		}
		return name
	}
}

// deriveEventType extracts the event.type from eventTemplate properties.
func deriveEventType(value map[string]any) string {
	et, ok := value["eventTemplate"].(map[string]any)
	if !ok {
		return ""
	}

	props := extractKVSlice(et, "properties")
	for _, prop := range props {
		if prop["key"] == "event.type" {
			if v, ok := prop["value"].(string); ok {
				return v
			}
		}
	}
	return ""
}

// ExtractEventName extracts the event.name from eventTemplate properties.
func ExtractEventName(value map[string]any) string {
	et, ok := value["eventTemplate"].(map[string]any)
	if !ok {
		return ""
	}

	props := extractKVSlice(et, "properties")
	for _, prop := range props {
		if prop["key"] == "event.name" {
			if v, ok := prop["value"].(string); ok {
				return v
			}
		}
	}
	return ""
}

// ExtractKVMap reads the "input" or "properties" field which in the API is
// stored as [{key, value}] and returns it as a simple map.
// It also handles the case where the field is already a map (flattened format).
func ExtractKVMap(parent map[string]any, field string) map[string]string {
	result := make(map[string]string)

	raw, ok := parent[field]
	if !ok {
		return result
	}

	// Case 1: already a map (flattened format)
	if m, ok := raw.(map[string]any); ok {
		for k, v := range m {
			result[k] = fmt.Sprintf("%v", v)
		}
		return result
	}

	// Case 2: [{key, value}] array (API format)
	if arr, ok := raw.([]any); ok {
		for _, item := range arr {
			if obj, ok := item.(map[string]any); ok {
				k, _ := obj["key"].(string)
				v := fmt.Sprintf("%v", obj["value"])
				if k != "" {
					result[k] = v
				}
			}
		}
	}

	return result
}

// extractKVSlice reads a field that is stored as [{key, value}] and returns
// a slice of maps for iteration.
func extractKVSlice(parent map[string]any, field string) []map[string]any {
	raw, ok := parent[field]
	if !ok {
		return nil
	}

	arr, ok := raw.([]any)
	if !ok {
		return nil
	}

	var result []map[string]any
	for _, item := range arr {
		if obj, ok := item.(map[string]any); ok {
			result = append(result, obj)
		}
	}
	return result
}

// List returns all custom anomaly detectors, optionally filtered.
func (h *Handler) List(opts ListOptions) ([]AnomalyDetector, error) {
	var allItems []settingsItem
	nextPageKey := ""

	for {
		req := h.client.HTTP().R()

		// Settings API rejects ALL other params when nextPageKey is present.
		if nextPageKey != "" {
			req.SetQueryParam("nextPageKey", nextPageKey)
		} else {
			req.SetQueryParam("schemaIds", SchemaID)
			req.SetQueryParam("scopes", Scope)
			req.SetQueryParam("pageSize", "500")
		}

		var result listResponse
		req.SetResult(&result)

		resp, err := req.Get(SettingsAPI)
		if err != nil {
			return nil, fmt.Errorf("failed to list anomaly detectors: %w", err)
		}
		if resp.IsError() {
			if resp.StatusCode() == 403 {
				return nil, diagnostic.SettingsForbidden(diagnostic.SettingsDenial{
					Operation:  "list anomaly detectors",
					Permission: "settings:objects:read",
					SchemaID:   SchemaID,
					Body:       resp.String(),
				})
			}
			return nil, fmt.Errorf("failed to list anomaly detectors: status %d: %s", resp.StatusCode(), resp.String())
		}

		allItems = append(allItems, result.Items...)

		if result.NextPageKey == "" {
			break
		}
		nextPageKey = result.NextPageKey
	}

	// Flatten and filter
	var detectors []AnomalyDetector
	for _, item := range allItems {
		ad := flatten(item)

		// Apply enabled filter
		if opts.Enabled != nil && ad.Enabled != *opts.Enabled {
			continue
		}

		detectors = append(detectors, ad)
	}

	// Sort by title
	sort.Slice(detectors, func(i, j int) bool {
		return detectors[i].Title < detectors[j].Title
	})

	return detectors, nil
}

// Get retrieves a single anomaly detector by object ID.
func (h *Handler) Get(objectID string) (*AnomalyDetector, error) {
	var raw settingsItem
	req := h.client.HTTP().R().SetResult(&raw)

	resp, err := req.Get(fmt.Sprintf("%s/%s", SettingsAPI, httpclient.PathSegment(objectID)))
	if err != nil {
		return nil, fmt.Errorf("failed to get anomaly detector: %w", err)
	}
	if resp.IsError() {
		switch resp.StatusCode() {
		case 404:
			return nil, fmt.Errorf("anomaly detector %q not found", objectID)
		case 403:
			return nil, diagnostic.SettingsForbidden(diagnostic.SettingsDenial{
				Operation:      fmt.Sprintf("get anomaly detector %q", objectID),
				Permission:     "settings:objects:read",
				SchemaID:       SchemaID,
				Body:           resp.String(),
				ExistingObject: true,
			})
		default:
			return nil, fmt.Errorf("failed to get anomaly detector: status %d: %s", resp.StatusCode(), resp.String())
		}
	}

	ad := flatten(raw)
	return &ad, nil
}

// GetRaw returns the raw settings value as JSON bytes, suitable for editing.
func (h *Handler) GetRaw(objectID string) ([]byte, error) {
	ad, err := h.Get(objectID)
	if err != nil {
		return nil, err
	}
	return json.MarshalIndent(ad.Value, "", "  ")
}

// FindByName searches for an anomaly detector by title (case-insensitive prefix match).
// Returns the first match if found.
func (h *Handler) FindByName(name string) (*AnomalyDetector, error) {
	detectors, err := h.List(ListOptions{})
	if err != nil {
		return nil, err
	}

	nameLower := strings.ToLower(name)

	// Exact match first
	for i := range detectors {
		if strings.ToLower(detectors[i].Title) == nameLower {
			return &detectors[i], nil
		}
	}

	// Prefix match
	for i := range detectors {
		if strings.HasPrefix(strings.ToLower(detectors[i].Title), nameLower) {
			return &detectors[i], nil
		}
	}

	return nil, fmt.Errorf("anomaly detector with title %q not found", name)
}

// FindByExactTitle searches for an anomaly detector by exact title (case-insensitive).
// Returns (nil, nil) if not found — this distinguishes "not found" from actual errors,
// matching the pattern used by GCP/Azure connection handlers for apply idempotency.
func (h *Handler) FindByExactTitle(title string) (*AnomalyDetector, error) {
	detectors, err := h.List(ListOptions{})
	if err != nil {
		return nil, err
	}

	titleLower := strings.ToLower(title)
	for i := range detectors {
		if strings.ToLower(detectors[i].Title) == titleLower {
			return &detectors[i], nil
		}
	}

	return nil, nil
}

// ExtractTitle extracts the title from JSON data in either flattened or raw Settings format.
// Returns empty string if the title cannot be determined.
func ExtractTitle(data []byte) string {
	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		return ""
	}

	// Flattened format: top-level "title"
	if t, ok := raw["title"].(string); ok {
		return t
	}

	// Raw Settings format: "value"."title"
	if v, ok := raw["value"].(map[string]any); ok {
		if t, ok := v["title"].(string); ok {
			return t
		}
	}

	return ""
}

// Create creates a new anomaly detector from JSON data.
// Accepts both flattened format and raw Settings API format.
func (h *Handler) Create(data []byte) (*AnomalyDetector, error) {
	apiBody, err := h.PrepareCreateBody(data)
	if err != nil {
		return nil, err
	}

	// POST expects an array
	body := []map[string]any{apiBody}

	resp, err := h.client.HTTP().R().SetBody(body).Post(SettingsAPI)
	if err != nil {
		return nil, fmt.Errorf("failed to create anomaly detector: %w", err)
	}
	if resp.IsError() {
		switch resp.StatusCode() {
		case 400:
			return nil, settingsValidationError(resp.Body())
		case 403:
			return nil, diagnostic.SettingsForbidden(diagnostic.SettingsDenial{
				Operation:  "create anomaly detector",
				Permission: "settings:objects:write",
				SchemaID:   SchemaID,
				Body:       resp.String(),
			})
		case 404:
			return nil, fmt.Errorf("schema %q not found", SchemaID)
		default:
			return nil, fmt.Errorf("failed to create anomaly detector: status %d: %s", resp.StatusCode(), resp.String())
		}
	}

	var createResp []createResponse
	if err := json.Unmarshal(resp.Body(), &createResp); err != nil {
		return nil, fmt.Errorf("failed to parse create response: %w", err)
	}
	if len(createResp) == 0 {
		return nil, fmt.Errorf("no items returned in create response")
	}
	if createResp[0].Error != nil {
		return nil, fmt.Errorf("create failed: %s", createResp[0].Error.Message)
	}

	return h.Get(createResp[0].ObjectID)
}

// Update updates an existing anomaly detector.
func (h *Handler) Update(objectID string, data []byte) (*AnomalyDetector, error) {
	// Get current object for version
	existing, err := h.Get(objectID)
	if err != nil {
		return nil, err
	}

	// Parse the update data
	value, err := h.prepareUpdateValue(data, existing)
	if err != nil {
		return nil, err
	}

	body := map[string]any{"value": value}

	resp, err := h.client.HTTP().R().
		SetBody(body).
		SetHeader("If-Match", existing.SchemaVersion).
		Put(fmt.Sprintf("%s/%s", SettingsAPI, httpclient.PathSegment(objectID)))
	if err != nil {
		return nil, fmt.Errorf("failed to update anomaly detector: %w", err)
	}
	if resp.IsError() {
		switch resp.StatusCode() {
		case 400:
			return nil, settingsValidationError(resp.Body())
		case 403:
			return nil, diagnostic.SettingsForbidden(diagnostic.SettingsDenial{
				Operation:      fmt.Sprintf("update anomaly detector %q", objectID),
				Permission:     "settings:objects:write",
				SchemaID:       SchemaID,
				Body:           resp.String(),
				ExistingObject: true,
			})
		case 404:
			return nil, fmt.Errorf("anomaly detector %q not found", objectID)
		case 409, 412:
			return nil, fmt.Errorf("anomaly detector version conflict (object was modified)")
		default:
			return nil, fmt.Errorf("failed to update anomaly detector: status %d: %s", resp.StatusCode(), resp.String())
		}
	}

	return h.Get(objectID)
}

// Delete deletes an anomaly detector by object ID.
func (h *Handler) Delete(objectID string) error {
	resp, err := h.client.HTTP().R().Delete(fmt.Sprintf("%s/%s", SettingsAPI, httpclient.PathSegment(objectID)))
	if err != nil {
		return fmt.Errorf("failed to delete anomaly detector: %w", err)
	}
	if resp.IsError() {
		switch resp.StatusCode() {
		case 403:
			return diagnostic.SettingsForbidden(diagnostic.SettingsDenial{
				Operation:      fmt.Sprintf("delete anomaly detector %q", objectID),
				Permission:     "settings:objects:write",
				SchemaID:       SchemaID,
				Body:           resp.String(),
				ExistingObject: true,
			})
		case 404:
			return fmt.Errorf("anomaly detector %q not found", objectID)
		default:
			return fmt.Errorf("failed to delete anomaly detector: status %d: %s", resp.StatusCode(), resp.String())
		}
	}
	return nil
}

// toAPIFormat converts input data (flattened or raw Settings format) into the
// Settings API create body: {schemaId, scope, value}. Schema defaults are
// filled in and the result is validated locally before it is worth a round trip.
func toAPIFormat(data []byte, defaultActor string) (map[string]any, error) {
	value, err := toAPIValue(data, defaultActor)
	if err != nil {
		return nil, err
	}

	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("invalid JSON: %w", err)
	}

	// Raw Settings format: preserve the envelope the caller wrote (it may carry
	// objectId / schemaVersion) and swap in the normalized value.
	if schema, ok := raw["schemaId"].(string); ok && schema == SchemaID {
		if _, ok := raw["scope"]; !ok {
			raw["scope"] = Scope
		}
		raw["value"] = value
		return raw, nil
	}

	return map[string]any{
		"schemaId": SchemaID,
		"scope":    Scope,
		"value":    value,
	}, nil
}

// toAPIValue extracts the value portion suitable for PUT updates, with schema
// defaults applied and local validation run.
func toAPIValue(data []byte, defaultActor string) (map[string]any, error) {
	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("invalid JSON: %w", err)
	}

	var value map[string]any
	if _, ok := raw["schemaId"]; ok {
		// Raw Settings format
		v, ok := raw["value"].(map[string]any)
		if !ok {
			return nil, fmt.Errorf("raw Settings format missing 'value' field")
		}
		value = v
	} else {
		// Flattened format: convert to API value
		v, err := flattenedToAPIValue(raw)
		if err != nil {
			return nil, fmt.Errorf("invalid anomaly detector definition: %w", err)
		}
		value = v
	}

	normalizeValue(value, defaultActor)
	if err := validateValue(value); err != nil {
		return nil, err
	}
	return value, nil
}

// prepareUpdateValue builds the value for a PUT. An update replaces the whole
// value, so a definition that omits executionSettings.actor would otherwise
// silently drop the identity the detector already runs as — keep the existing
// actor in that case, and only fall back to the handler default.
func (h *Handler) prepareUpdateValue(data []byte, existing *AnomalyDetector) (map[string]any, error) {
	actor := h.defaultActor
	if existing != nil {
		if es, ok := existing.Value["executionSettings"].(map[string]any); ok {
			if current, _ := es["actor"].(string); current != "" {
				actor = current
			}
		}
	}
	return toAPIValue(data, actor)
}

// settingsValidationError turns a Settings API 400 body into an error that
// names the offending fields instead of echoing the whole payload back.
func settingsValidationError(body []byte) error {
	if detail := describeSettingsError(body); detail != "" {
		return fmt.Errorf("%s", detail)
	}
	return fmt.Errorf("invalid anomaly detector: %s", body)
}

// flattenedToAPIValue converts the human-friendly YAML format into the API's value shape.
// Converts analyzer.input from map to [{key, value}] and eventTemplate from map to
// {properties: [{key, value}]}.
func flattenedToAPIValue(raw map[string]any) (map[string]any, error) {
	value := make(map[string]any)

	// Required fields
	title, _ := raw["title"].(string)
	if title == "" {
		return nil, fmt.Errorf("'title' is required")
	}
	value["title"] = title

	// Optional fields
	if v, ok := raw["enabled"]; ok {
		value["enabled"] = v
	} else {
		value["enabled"] = true
	}

	if v, ok := raw["description"].(string); ok {
		value["description"] = v
	}

	// Source defaults to "dtctl" when omitted
	source, _ := raw["source"].(string)
	if source == "" {
		source = defaultSource
	}
	value["source"] = source

	// Analyzer
	analyzer, ok := raw["analyzer"].(map[string]any)
	if !ok {
		return nil, fmt.Errorf("'analyzer' is required and must be an object")
	}

	apiAnalyzer := make(map[string]any)
	if name, ok := analyzer["name"].(string); ok {
		apiAnalyzer["name"] = name
	} else {
		return nil, fmt.Errorf("'analyzer.name' is required")
	}

	// Convert analyzer input to [{key, value}] array.
	// Handle four cases:
	// 1. map[string]any (flattened format from JSON unmarshal) — convert to [{key, value}]
	// 2. map[string]string (flattened format from ToFlattenedYAML round-trip) — convert
	// 3. []any (already in API format) — pass through
	// 4. nil/missing — default to empty array (required by API for auto-adaptive analyzers)
	switch input := analyzer["input"].(type) {
	case map[string]any:
		apiAnalyzer["input"] = mapToKVArray(input)
	case map[string]string:
		m := make(map[string]any, len(input))
		for k, v := range input {
			m[k] = v
		}
		apiAnalyzer["input"] = mapToKVArray(m)
	case []any:
		apiAnalyzer["input"] = input
	default:
		apiAnalyzer["input"] = []map[string]any{}
	}

	value["analyzer"] = apiAnalyzer

	// Event template: convert from map to {properties: [{key, value}]}
	// Handle both map[string]any (JSON unmarshal) and map[string]string (ToFlattenedYAML round-trip)
	switch et := raw["eventTemplate"].(type) {
	case map[string]any:
		value["eventTemplate"] = map[string]any{
			"properties": mapToKVArray(et),
		}
	case map[string]string:
		m := make(map[string]any, len(et))
		for k, v := range et {
			m[k] = v
		}
		value["eventTemplate"] = map[string]any{
			"properties": mapToKVArray(m),
		}
	}

	// Execution settings (pass through as-is if present)
	if es, ok := raw["executionSettings"]; ok {
		value["executionSettings"] = es
	}

	return value, nil
}

// mapToKVArray converts a map to the [{key, value}] format used by the Settings API.
func mapToKVArray(m map[string]any) []map[string]any {
	var result []map[string]any
	for k, v := range m {
		result = append(result, map[string]any{
			"key":   k,
			"value": fmt.Sprintf("%v", v),
		})
	}
	// Sort for deterministic output
	sort.Slice(result, func(i, j int) bool {
		return result[i]["key"].(string) < result[j]["key"].(string)
	})
	return result
}

// ToFlattenedYAML converts an AnomalyDetector's Value into the human-friendly
// flattened format used for editing.
func ToFlattenedYAML(value map[string]any) map[string]any {
	flat := make(map[string]any)

	if v, ok := value["title"]; ok {
		flat["title"] = v
	}
	if v, ok := value["description"]; ok {
		flat["description"] = v
	}
	if v, ok := value["enabled"]; ok {
		flat["enabled"] = v
	}
	if v, ok := value["source"]; ok {
		flat["source"] = v
	}

	// Flatten analyzer
	if analyzer, ok := value["analyzer"].(map[string]any); ok {
		flatAnalyzer := make(map[string]any)
		if name, ok := analyzer["name"]; ok {
			flatAnalyzer["name"] = name
		}
		// Convert input from [{key, value}] to map
		flatAnalyzer["input"] = ExtractKVMap(analyzer, "input")
		flat["analyzer"] = flatAnalyzer
	}

	// Flatten eventTemplate
	if et, ok := value["eventTemplate"].(map[string]any); ok {
		flat["eventTemplate"] = ExtractKVMap(et, "properties")
	}

	// Pass through execution settings
	if es, ok := value["executionSettings"]; ok {
		flat["executionSettings"] = es
	}

	return flat
}

// IsRawSettingsFormat checks if JSON data is in raw Settings API format.
func IsRawSettingsFormat(data []byte) bool {
	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		return false
	}
	schema, ok := raw["schemaId"].(string)
	return ok && schema == SchemaID
}

// IsFlattenedFormat checks if JSON data is in the flattened anomaly detector format.
func IsFlattenedFormat(data []byte) bool {
	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		return false
	}
	_, hasAnalyzer := raw["analyzer"]
	_, hasEventTemplate := raw["eventTemplate"]
	return hasAnalyzer && hasEventTemplate
}
