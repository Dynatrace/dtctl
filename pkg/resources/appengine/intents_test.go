package appengine

import (
	"encoding/json"
	"net/url"
	"reflect"
	"strings"
	"testing"

	"github.com/Dynatrace/dtctl/pkg/client"
)

func TestExtractIntentsFromManifest(t *testing.T) {
	tests := []struct {
		name     string
		app      App
		expected int
	}{
		{
			name: "app with intents",
			app: App{
				ID:   "test.app",
				Name: "Test App",
				Manifest: map[string]interface{}{
					"intents": map[string]interface{}{
						"view-trace": map[string]interface{}{
							"description": "View distributed trace",
							"properties": map[string]interface{}{
								"trace_id": map[string]interface{}{
									"required": true,
									"schema": map[string]interface{}{
										"type": "string",
									},
								},
								"timestamp": map[string]interface{}{
									"required": false,
									"schema": map[string]interface{}{
										"type":   "string",
										"format": "date-time",
									},
								},
							},
						},
					},
				},
			},
			expected: 1,
		},
		{
			name: "app without intents",
			app: App{
				ID:       "test.app",
				Name:     "Test App",
				Manifest: map[string]interface{}{},
			},
			expected: 0,
		},
		{
			name: "app with empty intents map",
			app: App{
				ID:   "test.app",
				Name: "Test App",
				Manifest: map[string]interface{}{
					"intents": map[string]interface{}{},
				},
			},
			expected: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			intents := extractIntentsFromManifest(tt.app)
			if len(intents) != tt.expected {
				t.Errorf("expected %d intents, got %d", tt.expected, len(intents))
			}

			// Verify intent structure if present
			if len(intents) > 0 {
				intent := intents[0]
				if intent.AppID != tt.app.ID {
					t.Errorf("expected AppID %q, got %q", tt.app.ID, intent.AppID)
				}
				if intent.AppName != tt.app.Name {
					t.Errorf("expected AppName %q, got %q", tt.app.Name, intent.AppName)
				}
				if intent.FullName == "" {
					t.Error("expected FullName to be set")
				}
			}
		})
	}
}

func TestParseIntentFromMap(t *testing.T) {
	tests := []struct {
		name         string
		appID        string
		appName      string
		intentID     string
		intentMap    map[string]interface{}
		expectedID   string
		expectedDesc string
		expectedReq  int
	}{
		{
			name:     "intent with required and optional properties",
			appID:    "test.app",
			appName:  "Test App",
			intentID: "view-trace",
			intentMap: map[string]interface{}{
				"name":        "View Trace",
				"description": "View distributed trace",
				"properties": map[string]interface{}{
					"trace_id": map[string]interface{}{
						"required": true,
						"schema": map[string]interface{}{
							"type": "string",
						},
					},
					"timestamp": map[string]interface{}{
						"required": false,
						"schema": map[string]interface{}{
							"type":   "string",
							"format": "date-time",
						},
					},
				},
			},
			expectedID:   "view-trace",
			expectedDesc: "View distributed trace",
			expectedReq:  1,
		},
		{
			name:     "intent with no properties",
			appID:    "test.app",
			appName:  "Test App",
			intentID: "simple-intent",
			intentMap: map[string]interface{}{
				"description": "Simple intent",
			},
			expectedID:   "simple-intent",
			expectedDesc: "Simple intent",
			expectedReq:  0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			intent := parseIntentFromMap(tt.appID, tt.appName, tt.intentID, tt.intentMap)

			if intent.IntentID != tt.expectedID {
				t.Errorf("expected IntentID %q, got %q", tt.expectedID, intent.IntentID)
			}
			if intent.Description != tt.expectedDesc {
				t.Errorf("expected Description %q, got %q", tt.expectedDesc, intent.Description)
			}
			if len(intent.RequiredProps) != tt.expectedReq {
				t.Errorf("expected %d required props, got %d", tt.expectedReq, len(intent.RequiredProps))
			}
			if intent.FullName != tt.appID+"/"+tt.expectedID {
				t.Errorf("expected FullName %q, got %q", tt.appID+"/"+tt.expectedID, intent.FullName)
			}
		})
	}
}

func TestMatchIntentToData(t *testing.T) {
	tests := []struct {
		name            string
		intent          Intent
		data            map[string]interface{}
		expectedQuality float64
		expectedMatched int
		expectedMissing int
	}{
		{
			name: "perfect match - all properties present",
			intent: Intent{
				IntentID: "view-trace",
				Properties: map[string]IntentProperty{
					"trace_id":  {Type: "string", Required: true},
					"timestamp": {Type: "string", Required: false},
				},
				RequiredProps: []string{"trace_id"},
			},
			data: map[string]interface{}{
				"trace_id":  "abc123",
				"timestamp": "2026-02-02T10:00:00Z",
			},
			expectedQuality: 100,
			expectedMatched: 2,
			expectedMissing: 0,
		},
		{
			name: "partial match - required present, optional missing",
			intent: Intent{
				IntentID: "view-trace",
				Properties: map[string]IntentProperty{
					"trace_id":  {Type: "string", Required: true},
					"timestamp": {Type: "string", Required: false},
				},
				RequiredProps: []string{"trace_id"},
			},
			data: map[string]interface{}{
				"trace_id": "abc123",
			},
			expectedQuality: 50,
			expectedMatched: 1,
			expectedMissing: 0,
		},
		{
			name: "no match - missing required property",
			intent: Intent{
				IntentID: "view-trace",
				Properties: map[string]IntentProperty{
					"trace_id": {Type: "string", Required: true},
				},
				RequiredProps: []string{"trace_id"},
			},
			data: map[string]interface{}{
				"log_id": "xyz789",
			},
			expectedQuality: 0,
			expectedMatched: 0,
			expectedMissing: 1,
		},
		{
			// An intent that declares nothing does not use the data, so the
			// data does not select it. It used to match everything at 100%
			// and crowd out the intents that do take the data (#517).
			name: "intent with no properties",
			intent: Intent{
				IntentID:      "simple-intent",
				Properties:    map[string]IntentProperty{},
				RequiredProps: []string{},
			},
			data: map[string]interface{}{
				"any_data": "value",
			},
			expectedQuality: 0,
			expectedMatched: 0,
			expectedMissing: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			match := matchIntentToData(tt.intent, tt.data)

			if match.MatchQuality != tt.expectedQuality {
				t.Errorf("expected MatchQuality %.2f, got %.2f", tt.expectedQuality, match.MatchQuality)
			}
			if len(match.MatchedProps) != tt.expectedMatched {
				t.Errorf("expected %d matched props, got %d", tt.expectedMatched, len(match.MatchedProps))
			}
			if len(match.MissingProps) != tt.expectedMissing {
				t.Errorf("expected %d missing props, got %d", tt.expectedMissing, len(match.MissingProps))
			}
		})
	}
}

func TestParseFullIntentName(t *testing.T) {
	tests := []struct {
		name           string
		fullName       string
		expectedAppID  string
		expectedIntent string
	}{
		{
			name:           "valid full name",
			fullName:       "dynatrace.distributedtracing/view-trace",
			expectedAppID:  "dynatrace.distributedtracing",
			expectedIntent: "view-trace",
		},
		{
			name:           "invalid - no slash",
			fullName:       "dynatrace.distributedtracing",
			expectedAppID:  "",
			expectedIntent: "",
		},
		{
			name:           "invalid - empty",
			fullName:       "",
			expectedAppID:  "",
			expectedIntent: "",
		},
		{
			name:           "multiple slashes",
			fullName:       "dynatrace.distributedtracing/view/trace",
			expectedAppID:  "dynatrace.distributedtracing",
			expectedIntent: "view/trace",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			appID, intentID := parseFullIntentName(tt.fullName)

			if appID != tt.expectedAppID {
				t.Errorf("expected AppID %q, got %q", tt.expectedAppID, appID)
			}
			if intentID != tt.expectedIntent {
				t.Errorf("expected IntentID %q, got %q", tt.expectedIntent, intentID)
			}
		})
	}
}

// TestGenerateIntentURLFragmentRoundTrip is a regression test for #439:
// form encoding (url.QueryEscape) turns spaces into "+" which the fragment
// treats as a literal plus, corrupting DQL payloads.
func TestGenerateIntentURLFragmentRoundTrip(t *testing.T) {
	tests := []struct {
		name    string
		payload map[string]interface{}
	}{
		{"simple payload", map[string]interface{}{"trace_id": "abc123"}},
		{"dql query with spaces", map[string]interface{}{"dt.query": "fetch logs | limit 10"}},
		{"literal plus sign", map[string]interface{}{"timestamp": "2026-02-02T16:04:19+01:00"}},
	}

	const baseURL = "https://example.apps.dynatrace.com"
	const prefix = baseURL + "/ui/intent/dynatrace.notebooks/view-query#"

	c, err := client.NewForTesting(baseURL, "fake-token")
	if err != nil {
		t.Fatalf("NewForTesting: %v", err)
	}
	handler := NewIntentHandler(c)

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := handler.GenerateIntentURL("dynatrace.notebooks", "view-query", tt.payload)
			if err != nil {
				t.Fatalf("GenerateIntentURL: %v", err)
			}
			if !strings.HasPrefix(got, prefix) {
				t.Fatalf("expected URL to start with %q, got %q", prefix, got)
			}

			// Use PathUnescape (not QueryUnescape): in a fragment "+" is a literal plus,
			// so QueryUnescape would hide exactly the bug under test.
			decoded, err := url.PathUnescape(strings.TrimPrefix(got, prefix))
			if err != nil {
				t.Fatalf("fragment is not valid percent-encoding: %v", err)
			}

			var roundTripped map[string]interface{}
			if err := json.Unmarshal([]byte(decoded), &roundTripped); err != nil {
				t.Fatalf("fragment is not valid JSON after decoding: %v\ndecoded: %q", err, decoded)
			}

			if !reflect.DeepEqual(roundTripped, tt.payload) {
				t.Errorf("round-trip mismatch\n  want: %v\n   got: %v", tt.payload, roundTripped)
			}
		})
	}
}

// --- #517: declarations are read in full, and nothing is invented ---------

// decodeManifestJSON decodes a manifest fragment the way the API client does
// (encoding/json into interface{}), so fixtures carry []interface{} and
// float64 exactly as production data does.
func decodeManifestJSON(t *testing.T, raw string) map[string]interface{} {
	t.Helper()
	var m map[string]interface{}
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		t.Fatalf("bad fixture: %v", err)
	}
	return m
}

// entityIntentsApp declares two entity intents that accept the same alias key
// (`dt.entity`) and tell the entity types apart only by pattern, which is how
// the plural `schemas` form is used in practice.
func entityIntentsApp(t *testing.T) App {
	return App{
		ID:   "example.app",
		Name: "Example App",
		Manifest: decodeManifestJSON(t, `{
		  "intents": {
		    "view-thing": {
		      "name": "View thing",
		      "properties": {
		        "thing": {
		          "required": true,
		          "description": "The thing to open",
		          "schemas": {
		            "thing":     {"type": "string", "pattern": "^THING-[0-9A-F]{16}$"},
		            "dt.entity": {"type": "string", "pattern": "^(?:THING-|WIDGET-)[0-9A-F]{16}$"},
		            "id":        {"type": "string", "pattern": "^(?:THING-|WIDGET-)[0-9A-F]{16}$"}
		          }
		        },
		        "timeframe": {
		          "schema": {"type": "object", "properties": {"from": {"type": "string"}}, "required": ["from"]}
		        }
		      }
		    },
		    "view-host": {
		      "description": "View host",
		      "properties": {
		        "host": {
		          "required": true,
		          "schemas": {
		            "dt.entity": {"type": "string", "pattern": "^HOST-[0-9A-F]{16}$"}
		          }
		        }
		      }
		    },
		    "open-anything": {
		      "description": "Opens without any data"
		    }
		  }
		}`),
	}
}

func TestParseIntentProperty_SingularSchema(t *testing.T) {
	propMap := decodeManifestJSON(t, `{
	  "required": true,
	  "description": "Trace to open",
	  "schema": {"type": "string", "format": "uuid", "pattern": "^[0-9a-f]{32}$", "enum": ["a", "b"], "items": {"type": "string"}}
	}`)

	prop := parseIntentProperty("trace_id", propMap)

	if prop.Type != "string" || !reflect.DeepEqual(prop.Types, []string{"string"}) {
		t.Errorf("type = %q, types = %v; want string", prop.Type, prop.Types)
	}
	if !prop.Required || prop.Format != "uuid" || prop.Description != "Trace to open" {
		t.Errorf("existing fields not kept: %+v", prop)
	}
	if !reflect.DeepEqual(prop.AcceptedKeys, []string{"trace_id"}) {
		t.Errorf("acceptedKeys = %v, want the property name", prop.AcceptedKeys)
	}
	if prop.Schemas != nil {
		t.Errorf("schemas = %v, want nil for the singular form", prop.Schemas)
	}
	// Constraints beyond format survive in the raw schema.
	for _, k := range []string{"pattern", "enum", "items"} {
		if _, ok := prop.Schema[k]; !ok {
			t.Errorf("schema lost %q: %v", k, prop.Schema)
		}
	}
	if got := prop.KeyPattern("trace_id", "trace_id"); got != "^[0-9a-f]{32}$" {
		t.Errorf("KeyPattern = %q", got)
	}
}

func TestParseIntentProperty_PluralSchemas(t *testing.T) {
	app := entityIntentsApp(t)
	intents := extractIntentsFromManifest(app)
	var viewThing Intent
	for _, in := range intents {
		if in.IntentID == "view-thing" {
			viewThing = in
		}
	}
	prop := viewThing.Properties["thing"]

	wantKeys := []string{"dt.entity", "id", "thing"}
	if !reflect.DeepEqual(prop.AcceptedKeys, wantKeys) {
		t.Errorf("acceptedKeys = %v, want %v", prop.AcceptedKeys, wantKeys)
	}
	if prop.Type != "string" {
		t.Errorf("type = %q, want string (declared by every key)", prop.Type)
	}
	if len(prop.Schemas) != 3 || prop.Schema != nil {
		t.Errorf("want the raw schemas map and no singular schema, got %+v", prop)
	}
	if got := prop.KeyPattern("thing", "dt.entity"); got != "^(?:THING-|WIDGET-)[0-9A-F]{16}$" {
		t.Errorf("KeyPattern(dt.entity) = %q", got)
	}

	// Nested properties/required of an object schema are kept.
	tf := viewThing.Properties["timeframe"]
	if tf.Type != "object" || tf.Schema["properties"] == nil || tf.Schema["required"] == nil {
		t.Errorf("timeframe lost its nested schema: %+v", tf)
	}
	if viewThing.Name != "View thing" || viewThing.Description != "View thing" {
		t.Errorf("name = %q, description = %q", viewThing.Name, viewThing.Description)
	}
}

func TestParseIntentProperty_TypeIsNeverInvented(t *testing.T) {
	tests := []struct {
		name      string
		raw       string
		wantType  string
		wantTypes []string
	}{
		{"no schema at all", `{"required": true}`, "", nil},
		{"schema without type", `{"schema": {"pattern": "^x$"}}`, "", nil},
		{"schemas without type", `{"schemas": {"a": {}, "b": {"pattern": "^b$"}}}`, "", nil},
		{"malformed object type", `{"schema": {"type": {"type": "string"}}}`, "", nil},
		{"list-form type", `{"schema": {"type": ["string", "array"]}}`, "string|array", []string{"string", "array"}},
		{"list-form with duplicates", `{"schema": {"type": ["string", "string"]}}`, "string", []string{"string"}},
		{"keys disagree", `{"schemas": {"id": {"type": "string"}, "ids": {"type": "array"}}}`, "string|array", []string{"string", "array"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			prop := parseIntentProperty("p", decodeManifestJSON(t, tt.raw))
			if prop.Type != tt.wantType {
				t.Errorf("type = %q, want %q", prop.Type, tt.wantType)
			}
			if !reflect.DeepEqual(prop.Types, tt.wantTypes) {
				t.Errorf("types = %v, want %v", prop.Types, tt.wantTypes)
			}
		})
	}
}

func TestParseIntentProperty_EmptySchemasIsUnsatisfiable(t *testing.T) {
	prop := parseIntentProperty("p", decodeManifestJSON(t, `{"required": true, "schemas": {}}`))

	out, err := json.Marshal(prop)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(out), `"acceptedKeys":[]`) {
		t.Errorf("empty schemas must show as acceptedKeys: [], got %s", out)
	}

	intent := Intent{
		Properties:    map[string]IntentProperty{"p": prop},
		RequiredProps: []string{"p"},
	}
	if m := matchIntentToData(intent, map[string]interface{}{"p": "x"}); m.MatchQuality != 0 {
		t.Errorf("a property no key can satisfy matched at %.0f%%", m.MatchQuality)
	}
}

func TestIntentPropertyJSONKeepsExistingKeys(t *testing.T) {
	prop := parseIntentProperty("p", decodeManifestJSON(t, `{"required": false}`))
	out, err := json.Marshal(prop)
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]interface{}
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatal(err)
	}
	// `type` and `required` are always emitted, as before; an undeclared
	// type is empty rather than a made-up "string".
	if v, ok := got["type"]; !ok || v != "" {
		t.Errorf("type = %v (present %v), want empty string", v, ok)
	}
	if _, ok := got["required"]; !ok {
		t.Errorf("required missing: %s", out)
	}
}

func TestParseIntentDeprecated(t *testing.T) {
	tests := []struct {
		raw     string
		want    bool
		wantMsg string
	}{
		{`{"deprecated": true}`, true, ""},
		{`{"deprecated": false}`, false, ""},
		{`{"deprecated": "Use open-thing instead"}`, true, "Use open-thing instead"},
		{`{}`, false, ""},
	}
	for _, tt := range tests {
		in := parseIntentFromMap("example.app", "Example", "i", decodeManifestJSON(t, tt.raw))
		if in.Deprecated != tt.want || in.DeprecationMessage != tt.wantMsg {
			t.Errorf("%s: deprecated = %v %q, want %v %q", tt.raw, in.Deprecated, in.DeprecationMessage, tt.want, tt.wantMsg)
		}
	}
}

func TestExtractIntentsFromManifestIsOrdered(t *testing.T) {
	app := entityIntentsApp(t)
	for run := 0; run < 5; run++ {
		var ids []string
		for _, in := range extractIntentsFromManifest(app) {
			ids = append(ids, in.IntentID)
		}
		want := []string{"open-anything", "view-host", "view-thing"}
		if !reflect.DeepEqual(ids, want) {
			t.Fatalf("run %d: order = %v, want %v", run, ids, want)
		}
	}
}

func TestFindIntentsMatchesAcceptedKeysAndPatterns(t *testing.T) {
	intents := extractIntentsFromManifest(entityIntentsApp(t))

	names := func(ms []IntentMatch) []string {
		var out []string
		for _, m := range ms {
			out = append(out, m.FullName)
		}
		return out
	}

	t.Run("alias key selects by pattern", func(t *testing.T) {
		ms := rankIntentMatches(intents, map[string]interface{}{"dt.entity": "WIDGET-0000000000000001"})
		if got := names(ms); !reflect.DeepEqual(got, []string{"example.app/view-thing"}) {
			t.Fatalf("matches = %v, want only view-thing", got)
		}
		m := ms[0]
		if m.MatchQuality != 50 {
			t.Errorf("quality = %.0f, want 50 (1 of 2 properties)", m.MatchQuality)
		}
		if !reflect.DeepEqual(m.MatchedProps, []string{"thing"}) || m.MatchedKeys["thing"] != "dt.entity" {
			t.Errorf("matchedProps = %v, matchedKeys = %v", m.MatchedProps, m.MatchedKeys)
		}
	})

	t.Run("same key, other entity type", func(t *testing.T) {
		ms := rankIntentMatches(intents, map[string]interface{}{"dt.entity": "HOST-0000000000000001"})
		if got := names(ms); !reflect.DeepEqual(got, []string{"example.app/view-host"}) {
			t.Fatalf("matches = %v, want only view-host", got)
		}
	})

	t.Run("pattern mismatch is not a match", func(t *testing.T) {
		ms := rankIntentMatches(intents, map[string]interface{}{"dt.entity": "not-an-entity"})
		if len(ms) != 0 {
			t.Fatalf("matches = %v, want none", names(ms))
		}
	})

	t.Run("intents declaring nothing do not match everything", func(t *testing.T) {
		ms := rankIntentMatches(intents, map[string]interface{}{"unrelated": "x"})
		if len(ms) != 0 {
			t.Fatalf("matches = %v, want none", names(ms))
		}
	})

	t.Run("property name that is not an accepted key", func(t *testing.T) {
		intent := Intent{Properties: map[string]IntentProperty{
			"frontend": parseIntentProperty("frontend", decodeManifestJSON(t, `{"required": true, "schemas": {"dt.entity": {}}}`)),
		}, RequiredProps: []string{"frontend"}}
		if m := matchIntentToData(intent, map[string]interface{}{"frontend": "x"}); m.MatchQuality != 0 {
			t.Errorf("matched by property name at %.0f%%", m.MatchQuality)
		}
	})
}

func TestMatchIntentToData_PatternEdgeCases(t *testing.T) {
	prop := func(raw string) IntentProperty { return parseIntentProperty("k", decodeManifestJSON(t, raw)) }
	tests := []struct {
		name string
		prop IntentProperty
		data map[string]interface{}
		want bool
	}{
		{"pattern matches", prop(`{"schema": {"pattern": "^A-[0-9]+$"}}`), map[string]interface{}{"k": "A-12"}, true},
		{"pattern rejects", prop(`{"schema": {"pattern": "^A-[0-9]+$"}}`), map[string]interface{}{"k": "B-12"}, false},
		// RE2 cannot compile a lookahead; the key alone decides then.
		{"uncompilable pattern", prop(`{"schema": {"pattern": "^(?=A)A$"}}`), map[string]interface{}{"k": "anything"}, true},
		// A pattern only constrains strings.
		{"non-string value", prop(`{"schema": {"pattern": "^A$"}}`), map[string]interface{}{"k": float64(3)}, true},
		{"wildcard key", prop(`{"schemas": {"example.entity.*": {"pattern": "^W-"}}}`), map[string]interface{}{"example.entity.widget": "W-1"}, true},
		{"wildcard key, pattern rejects", prop(`{"schemas": {"example.entity.*": {"pattern": "^W-"}}}`), map[string]interface{}{"example.entity.widget": "X-1"}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			intent := Intent{Properties: map[string]IntentProperty{"k": tt.prop}, RequiredProps: []string{"k"}}
			m := matchIntentToData(intent, tt.data)
			if got := m.MatchQuality == 100; got != tt.want {
				t.Errorf("matched = %v (%.0f%%), want %v", got, m.MatchQuality, tt.want)
			}
		})
	}
}

func TestRankIntentMatches_DeprecatedRanksBelow(t *testing.T) {
	singleKey := func(t *testing.T) map[string]IntentProperty {
		return map[string]IntentProperty{"k": parseIntentProperty("k", decodeManifestJSON(t, `{"required": true}`))}
	}
	intents := []Intent{
		{FullName: "example.app/a-old", Deprecated: true, Properties: singleKey(t), RequiredProps: []string{"k"}},
		{FullName: "example.app/z-new", Properties: singleKey(t), RequiredProps: []string{"k"}},
		{FullName: "example.app/m-new", Properties: singleKey(t), RequiredProps: []string{"k"}},
	}
	ms := rankIntentMatches(intents, map[string]interface{}{"k": "v"})
	var got []string
	for _, m := range ms {
		got = append(got, m.FullName)
	}
	want := []string{"example.app/m-new", "example.app/z-new", "example.app/a-old"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("order = %v, want %v", got, want)
	}
}
