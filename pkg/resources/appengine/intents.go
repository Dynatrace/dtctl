package appengine

import (
	"encoding/json"
	"fmt"
	"net/url"
	"path"
	"regexp"
	"sort"
	"strings"

	"github.com/Dynatrace/dtctl/pkg/client"
	"github.com/Dynatrace/dtctl/sdk/httpclient"
)

// IntentHandler handles App Engine intent operations
type IntentHandler struct {
	client *client.Client
}

// NewIntentHandler creates a new intent handler
func NewIntentHandler(c *client.Client) *IntentHandler {
	return &IntentHandler{client: c}
}

// IntentProperty represents a property definition in an intent.
//
// An app declares a property in one of two forms:
//
//   - singular `schema`: the property name is the payload key, governed by
//     that one schema;
//   - plural `schemas`: a map whose keys are the payload keys the intent
//     accepts for this property (aliases such as `dt.entity` or `id`), each
//     with its own schema. The property name is then only a label: a payload
//     can carry it only when it is also one of the keys.
//
// Type, Required, Format and Description predate the plural form and keep
// their meaning. Everything else the declaration carries is exposed next to
// them rather than folded into them, so -o json/yaml show what the app
// declared.
type IntentProperty struct {
	// Type is the declared JSON-schema type. It is empty when the declaration
	// names none: dtctl does not guess one. A list-form type
	// (`type: ["string", "array"]`), or different types across the keys of a
	// plural `schemas` declaration, is joined with "|"; Types has the list.
	Type        string `json:"type"`
	Required    bool   `json:"required"`
	Format      string `json:"format,omitempty"`
	Description string `json:"description,omitempty"`

	// Types lists every declared type without duplicates, in declaration
	// order (for `schemas`, in accepted-key order).
	Types []string `json:"types,omitempty" yaml:",omitempty"`
	// AcceptedKeys are the payload keys that satisfy this property: the
	// property name for the singular form, the `schemas` keys (sorted) for
	// the plural one. Always present; an empty list means the app declared
	// `schemas: {}`, which no payload can satisfy.
	AcceptedKeys []string `json:"acceptedKeys"`
	// Schema is the singular `schema` exactly as declared (pattern, enum,
	// items, nested properties and required, ...).
	Schema map[string]interface{} `json:"schema,omitempty" yaml:",omitempty"`
	// Schemas is the plural `schemas` map exactly as declared, keyed by
	// accepted payload key.
	Schemas map[string]interface{} `json:"schemas,omitempty" yaml:",omitempty"`
}

// Intent represents an app intent from the manifest
type Intent struct {
	AppID         string                    `json:"appId" table:"APP_ID,wide"`
	AppName       string                    `json:"appName" table:"APP"`
	IntentID      string                    `json:"intentId" table:"INTENT_ID"`
	Description   string                    `json:"description" table:"DESCRIPTION"`
	Properties    map[string]IntentProperty `json:"properties,omitempty" table:"-"`
	FullName      string                    `json:"fullName" table:"FULL_NAME"`
	RequiredProps []string                  `json:"requiredProps,omitempty" table:"REQUIRED"`

	// Name is the declaration's human-readable label, as declared.
	Name string `json:"name,omitempty" yaml:",omitempty" table:"-"`
	// Deprecated is true when the app marks the intent deprecated.
	Deprecated bool `json:"deprecated,omitempty" yaml:",omitempty" table:"-"`
	// DeprecationMessage is the app's note when `deprecated` is a string.
	DeprecationMessage string `json:"deprecationMessage,omitempty" yaml:",omitempty" table:"-"`
}

// IntentMatch represents a matched intent with quality score
type IntentMatch struct {
	Intent
	MatchQuality float64  `json:"matchQuality" table:"MATCH%"`
	MatchedProps []string `json:"matchedProps,omitempty" table:"-"`
	MissingProps []string `json:"missingProps,omitempty" table:"-"`
	// MatchedKeys maps each matched property to the key in the data that
	// satisfied it: the key to send when opening the intent.
	MatchedKeys map[string]string `json:"matchedKeys,omitempty" yaml:",omitempty" table:"-"`
}

// ListIntents lists all intents across apps (or filtered by app ID)
func (h *IntentHandler) ListIntents(appIDFilter string) ([]Intent, error) {
	// Get all apps with manifest
	appHandler := NewHandler(h.client)
	appList, err := appHandler.ListApps()
	if err != nil {
		return nil, err
	}

	var intents []Intent

	// For each app, extract intents from manifest
	for _, app := range appList.Apps {
		// If filter is set, skip apps that don't match
		if appIDFilter != "" && app.ID != appIDFilter {
			continue
		}

		// Extract intents from manifest
		if app.Manifest != nil {
			appIntents := extractIntentsFromManifest(app)
			intents = append(intents, appIntents...)
		}
	}

	return intents, nil
}

// GetIntent gets details about a specific intent
func (h *IntentHandler) GetIntent(fullName string) (*Intent, error) {
	// Parse app-id/intent-id format
	appID, intentID := parseFullIntentName(fullName)
	if appID == "" || intentID == "" {
		return nil, fmt.Errorf("invalid intent name format, expected 'app-id/intent-id', got %q", fullName)
	}

	// Get app details
	appHandler := NewHandler(h.client)
	app, err := appHandler.GetApp(appID)
	if err != nil {
		return nil, err
	}

	// Find the intent in the manifest
	if app.Manifest != nil {
		intents := extractIntentsFromManifest(*app)
		for _, intent := range intents {
			if intent.IntentID == intentID {
				return &intent, nil
			}
		}
	}

	return nil, fmt.Errorf("intent %q not found in app %q", intentID, appID)
}

// FindIntentsForData finds intents that match the given data
func (h *IntentHandler) FindIntentsForData(data map[string]interface{}) ([]IntentMatch, error) {
	// Get all intents
	intents, err := h.ListIntents("")
	if err != nil {
		return nil, err
	}

	return rankIntentMatches(intents, data), nil
}

// rankIntentMatches matches every intent against the data and returns those
// with a non-zero match quality, best first.
func rankIntentMatches(intents []Intent, data map[string]interface{}) []IntentMatch {
	var matches []IntentMatch

	// Match each intent against the data
	for _, intent := range intents {
		match := matchIntentToData(intent, data)
		// Only include intents with non-zero match quality
		if match.MatchQuality > 0 {
			matches = append(matches, match)
		}
	}

	// Sort by match quality (descending). At equal quality a deprecated
	// intent ranks below the others, and the full name breaks the remaining
	// ties so that identical runs print identical output.
	sort.SliceStable(matches, func(i, j int) bool {
		a, b := matches[i], matches[j]
		if a.MatchQuality != b.MatchQuality {
			return a.MatchQuality > b.MatchQuality
		}
		if a.Deprecated != b.Deprecated {
			return !a.Deprecated
		}
		return a.FullName < b.FullName
	})

	return matches
}

// GenerateIntentURL generates an intent URL for the given app, intent, and payload
func (h *IntentHandler) GenerateIntentURL(appID, intentID string, payload map[string]interface{}) (string, error) {
	// Get base URL from client
	baseURL := h.client.BaseURL()

	// Marshal payload to JSON
	jsonPayload, err := json.Marshal(payload)
	if err != nil {
		return "", fmt.Errorf("failed to marshal payload: %w", err)
	}

	// Construct URL
	intentURL := fmt.Sprintf("%s/ui/intent/%s/%s#%s",
		baseURL, httpclient.PathSegment(appID), httpclient.PathSegment(intentID), escapeFragment(string(jsonPayload)))

	return intentURL, nil
}

// escapeFragment percent-encodes a string for use in a URL fragment (RFC 3986).
// url.QueryEscape is wrong here: it maps spaces to "+" (form encoding), but a
// fragment treats "+" as a literal plus. QueryEscape already emits "%2B" for a
// real "+", so replacing its space marker is exact and not a heuristic. (#439)
func escapeFragment(s string) string {
	return strings.ReplaceAll(url.QueryEscape(s), "+", "%20")
}

// extractIntentsFromManifest extracts intents from an app manifest, ordered
// by intent ID so that consecutive runs print identical output.
func extractIntentsFromManifest(app App) []Intent {
	var intents []Intent

	// Navigate manifest structure: manifest.intents (object/map at top level)
	if app.Manifest != nil {
		if intentsMap, ok := app.Manifest["intents"].(map[string]interface{}); ok {
			for _, intentID := range sortedKeys(intentsMap) {
				if intentMap, ok := intentsMap[intentID].(map[string]interface{}); ok {
					intent := parseIntentFromMap(app.ID, app.Name, intentID, intentMap)
					intents = append(intents, intent)
				}
			}
		}
	}

	return intents
}

// parseIntentFromMap parses an intent from a manifest map
func parseIntentFromMap(appID, appName, intentID string, intentMap map[string]interface{}) Intent {
	// Get intent metadata
	name, _ := intentMap["name"].(string)
	description, _ := intentMap["description"].(string)

	// Without an explicit description, fall back to the display name.
	if description == "" {
		description = name
	}

	var deprecated bool
	var deprecationMessage string
	switch d := intentMap["deprecated"].(type) {
	case bool:
		deprecated = d
	case string:
		deprecated = d != ""
		deprecationMessage = d
	}

	// Parse properties
	properties := make(map[string]IntentProperty)
	var requiredProps []string

	if propsMap, ok := intentMap["properties"].(map[string]interface{}); ok {
		for propName, propData := range propsMap {
			if propMap, ok := propData.(map[string]interface{}); ok {
				prop := parseIntentProperty(propName, propMap)
				properties[propName] = prop
				if prop.Required {
					requiredProps = append(requiredProps, propName)
				}
			}
		}
	}

	// Sort required props for consistent output
	sort.Strings(requiredProps)

	return Intent{
		AppID:              appID,
		AppName:            appName,
		IntentID:           intentID,
		Description:        description,
		Properties:         properties,
		FullName:           fmt.Sprintf("%s/%s", appID, intentID),
		RequiredProps:      requiredProps,
		Name:               name,
		Deprecated:         deprecated,
		DeprecationMessage: deprecationMessage,
	}
}

// parseIntentProperty parses one entry of an intent's `properties` map, in
// either the singular `schema` or the plural `schemas` form (see
// IntentProperty). Nothing is defaulted: a type the app did not declare is
// reported as empty.
func parseIntentProperty(propName string, propMap map[string]interface{}) IntentProperty {
	required, _ := propMap["required"].(bool)
	description, _ := propMap["description"].(string)

	prop := IntentProperty{
		Required:     required,
		Description:  description,
		AcceptedKeys: []string{},
	}

	if schema, ok := propMap["schema"].(map[string]interface{}); ok {
		prop.Schema = schema
	}
	if schemas, ok := propMap["schemas"].(map[string]interface{}); ok {
		prop.Schemas = schemas
	}

	var formats []string
	for _, key := range prop.acceptedKeys(propName) {
		schema := prop.keySchema(propName, key)
		for _, t := range schemaTypes(schema) {
			prop.Types = appendUnique(prop.Types, t)
		}
		if f, ok := schema["format"].(string); ok && f != "" {
			formats = appendUnique(formats, f)
		}
		prop.AcceptedKeys = append(prop.AcceptedKeys, key)
	}
	prop.Type = strings.Join(prop.Types, "|")
	// Format holds one value. When the accepted keys disagree it stays
	// empty, and each key's own schema (in Schemas) still carries its format.
	if len(formats) == 1 {
		prop.Format = formats[0]
	}

	return prop
}

// acceptedKeys returns the payload keys that satisfy the property: the
// `schemas` keys (sorted) when the plural form is declared, else the property
// name. A declared `schemas` map takes precedence over a singular `schema`.
func (p IntentProperty) acceptedKeys(propName string) []string {
	if p.Schemas != nil {
		return sortedKeys(p.Schemas)
	}
	return []string{propName}
}

// keySchema returns the schema governing one accepted key, or nil when none
// is declared (or the declaration is not an object).
func (p IntentProperty) keySchema(propName, key string) map[string]interface{} {
	if p.Schemas != nil {
		schema, _ := p.Schemas[key].(map[string]interface{})
		return schema
	}
	if key == propName {
		return p.Schema
	}
	return nil
}

// KeyPattern returns the `pattern` the schema of one accepted key declares,
// or "" when it declares none.
func (p IntentProperty) KeyPattern(propName, key string) string {
	pattern, _ := p.keySchema(propName, key)["pattern"].(string)
	return pattern
}

// schemaTypes returns the types a schema declares: `type: "string"` and
// `type: ["string", "array"]` are both valid JSON Schema. Anything else
// (absent, or malformed such as an object) declares no type.
func schemaTypes(schema map[string]interface{}) []string {
	switch t := schema["type"].(type) {
	case string:
		if t != "" {
			return []string{t}
		}
	case []interface{}:
		var types []string
		for _, v := range t {
			if s, ok := v.(string); ok && s != "" {
				types = appendUnique(types, s)
			}
		}
		return types
	}
	return nil
}

// matchIntentToData matches an intent against provided data.
//
// A property is satisfied when the data carries one of its accepted keys
// (see IntentProperty) with a value the key's schema admits: a declared
// `pattern` must match a string value. A pattern Go cannot compile (RE2
// lacks some ECMAScript constructs) is not evaluated, so the key alone
// decides.
//
// Match quality is the share of declared properties the data satisfies. It
// is 0 when a required property is unsatisfied, and also when nothing
// matched: an intent that declares no properties does not use the data, so
// the data does not select it.
func matchIntentToData(intent Intent, data map[string]interface{}) IntentMatch {
	var matchedProps []string
	var missingProps []string
	matchedKeys := make(map[string]string)

	satisfy := func(propName string) bool {
		if _, done := matchedKeys[propName]; done {
			return true
		}
		prop := intent.Properties[propName]
		key, ok := prop.satisfyingKey(propName, data)
		if !ok {
			return false
		}
		matchedKeys[propName] = key
		matchedProps = append(matchedProps, propName)
		return true
	}

	// Check all required properties
	for _, reqProp := range intent.RequiredProps {
		if !satisfy(reqProp) {
			missingProps = append(missingProps, reqProp)
		}
	}

	match := IntentMatch{Intent: intent, MissingProps: missingProps}

	// If any required property is missing, match quality is 0
	if len(missingProps) > 0 {
		match.MatchedProps = matchedProps
		return match
	}

	// Then the optional ones, in name order for stable output.
	for _, propName := range sortedPropertyNames(intent.Properties) {
		satisfy(propName)
	}

	match.MatchedProps = matchedProps
	if len(matchedKeys) > 0 {
		match.MatchedKeys = matchedKeys
	}

	totalProps := len(intent.Properties)
	if totalProps == 0 {
		return match
	}

	// Calculate coverage: (matched_properties / total_properties) * 100
	matched := 0
	for propName := range intent.Properties {
		if _, ok := matchedKeys[propName]; ok {
			matched++
		}
	}
	match.MatchQuality = (float64(matched) / float64(totalProps)) * 100

	return match
}

// satisfyingKey returns the first accepted key (in sorted order) that the
// data carries with an admitted value. An accepted key containing "*" is a
// wildcard (e.g. `dt.smartscape.*`) matched against the data's keys, and the
// data key it matched is returned.
func (p IntentProperty) satisfyingKey(propName string, data map[string]interface{}) (string, bool) {
	for _, key := range p.acceptedKeys(propName) {
		schema := p.keySchema(propName, key)
		if !strings.Contains(key, "*") {
			if value, ok := data[key]; ok && valueAdmitted(schema, value) {
				return key, true
			}
			continue
		}
		for _, dataKey := range sortedKeys(data) {
			if ok, err := path.Match(key, dataKey); err == nil && ok && valueAdmitted(schema, data[dataKey]) {
				return dataKey, true
			}
		}
	}
	return "", false
}

// valueAdmitted reports whether a schema's `pattern` admits a value. Only a
// string value is checked, and only against a pattern Go can compile.
func valueAdmitted(schema map[string]interface{}, value interface{}) bool {
	pattern, ok := schema["pattern"].(string)
	if !ok || pattern == "" {
		return true
	}
	s, ok := value.(string)
	if !ok {
		return true
	}
	re, err := regexp.Compile(pattern)
	if err != nil {
		return true
	}
	return re.MatchString(s)
}

// SortedPropertyNames returns the intent's property names in sorted order.
func (i Intent) SortedPropertyNames() []string {
	return sortedPropertyNames(i.Properties)
}

func sortedPropertyNames(props map[string]IntentProperty) []string {
	names := make([]string, 0, len(props))
	for name := range props {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func sortedKeys(m map[string]interface{}) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func appendUnique(list []string, item string) []string {
	if contains(list, item) {
		return list
	}
	return append(list, item)
}

// contains checks if a string slice contains a given item
func contains(slice []string, item string) bool {
	for _, s := range slice {
		if s == item {
			return true
		}
	}
	return false
}

// parseFullIntentName parses a full intent name (app-id/intent-id)
func parseFullIntentName(fullName string) (appID, intentID string) {
	parts := strings.SplitN(fullName, "/", 2)
	if len(parts) == 2 {
		return parts[0], parts[1]
	}
	return "", ""
}
