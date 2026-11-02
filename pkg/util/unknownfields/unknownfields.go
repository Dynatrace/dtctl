// Package unknownfields lets a typed struct carry the JSON object members it
// does not model through a decode/encode round trip.
//
// dtctl's typed resource structs are read-modify-write views: a command reads
// a document, changes one field and sends the whole document back. Any member
// the struct has no field for would otherwise be dropped from that update —
// silently resetting it on the server, or, for a never-modifiable property,
// getting the update rejected. Keeping unknown members in an Extra map lets a
// schema grow without a dtctl release.
//
// A type opts in with an `Extra map[string]json.RawMessage` field tagged
// `json:"-"` and two methods that delegate here through a method-less alias:
//
//	func (c *Config) UnmarshalJSON(data []byte) error {
//		type plain Config
//		extra, err := unknownfields.Unmarshal(data, (*plain)(c))
//		if err != nil {
//			return err
//		}
//		c.Extra = extra
//		return nil
//	}
//
//	func (c Config) MarshalJSON() ([]byte, error) {
//		type plain Config
//		return unknownfields.Marshal(plain(c), c.Extra)
//	}
package unknownfields

import (
	"bytes"
	"encoding/json"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"sync"

	"gopkg.in/yaml.v3"

	"github.com/Dynatrace/dtctl/pkg/util/format"
)

// YAML renders known (a method-less alias of the struct) the way yaml.v3
// reflection always has — same keys, order and empty values — and appends
// the extra members under their original API names, in key order, with
// numbers kept exactly as the server sent them. It is the MarshalYAML
// counterpart of Marshal, so `get -o yaml` → `apply -f` keeps unmodelled
// members: apply matches the reflected lowercase keys case-insensitively and
// reads the appended ones back into Extra.
//
// TEMPORARY: this exists only because `-o yaml` still prints reflected
// lowercase field names, which stable commands promise to keep. Once YAML
// output uses JSON field names everywhere (centrally, in the printer), Extra
// is emitted by MarshalJSON alone, and this function and every MarshalYAML
// that calls it should be deleted.
func YAML(known any, extra map[string]json.RawMessage) (any, error) {
	var node yaml.Node
	if err := node.Encode(known); err != nil {
		return nil, err
	}
	if len(extra) == 0 {
		return &node, nil
	}
	if node.Kind != yaml.MappingNode {
		return nil, fmt.Errorf("unknownfields: %T does not encode as a YAML mapping", known)
	}

	names := fieldNames(reflect.TypeOf(known))
	keys := make([]string, 0, len(extra))
	for key := range extra {
		if !names.has(key) {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)

	for _, key := range keys {
		raw := extra[key]
		if len(bytes.TrimSpace(raw)) == 0 {
			raw = json.RawMessage("null")
		}
		value, err := format.YAMLNodeFromJSON(raw)
		if err != nil {
			return nil, fmt.Errorf("unknownfields: invalid extra member %q in %T: %w", key, known, err)
		}
		var valueNode yaml.Node
		if err := valueNode.Encode(value); err != nil {
			return nil, err
		}
		node.Content = append(node.Content,
			&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: key},
			&valueNode)
	}
	return &node, nil
}

// Unmarshal decodes the JSON object in data into known, which must be a
// pointer to a struct without its own UnmarshalJSON (use a method-less alias),
// and returns the members no field of that struct consumes. A member is
// "consumed" under the same case-insensitive name matching encoding/json
// applies, so a key the typed field already absorbed is never kept twice.
//
// A JSON null is a no-op, as encoding/json expects from an Unmarshaler. The
// returned map is nil when every member is modelled.
func Unmarshal(data []byte, known any) (map[string]json.RawMessage, error) {
	if bytes.Equal(bytes.TrimSpace(data), []byte("null")) {
		return nil, nil
	}
	if err := json.Unmarshal(data, known); err != nil {
		return nil, err
	}

	var members map[string]json.RawMessage
	if err := json.Unmarshal(data, &members); err != nil {
		return nil, err
	}

	names := fieldNames(reflect.TypeOf(known))
	var extra map[string]json.RawMessage
	for key, raw := range members {
		if names.has(key) {
			continue
		}
		if extra == nil {
			extra = make(map[string]json.RawMessage)
		}
		extra[key] = raw
	}
	return extra, nil
}

// Marshal encodes known, which must be a struct (or pointer to one) without
// its own MarshalJSON, and appends the extra members after the modelled ones
// in key order. An extra member whose name matches a modelled field is
// skipped: the typed field is the source of truth for anything dtctl models.
func Marshal(known any, extra map[string]json.RawMessage) ([]byte, error) {
	data, err := json.Marshal(known)
	if err != nil {
		return nil, err
	}
	if len(extra) == 0 {
		return data, nil
	}

	trimmed := bytes.TrimSpace(data)
	if len(trimmed) < 2 || trimmed[0] != '{' || trimmed[len(trimmed)-1] != '}' {
		return nil, fmt.Errorf("unknownfields: %T does not encode as a JSON object", known)
	}

	names := fieldNames(reflect.TypeOf(known))
	keys := make([]string, 0, len(extra))
	for key := range extra {
		if !names.has(key) {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)

	var buf bytes.Buffer
	buf.Write(trimmed[:len(trimmed)-1])
	hasMembers := len(bytes.TrimSpace(trimmed[1:len(trimmed)-1])) > 0
	for _, key := range keys {
		raw := extra[key]
		if len(bytes.TrimSpace(raw)) == 0 {
			// An empty RawMessage is not valid JSON; encode it as null
			// rather than failing the whole document.
			raw = json.RawMessage("null")
		}
		if hasMembers {
			buf.WriteByte(',')
		}
		hasMembers = true
		encodedKey, err := json.Marshal(key)
		if err != nil {
			return nil, err
		}
		buf.Write(encodedKey)
		buf.WriteByte(':')
		buf.Write(raw)
	}
	buf.WriteByte('}')

	// Validate (and compact) the result so a malformed RawMessage surfaces
	// here instead of as a confusing 400 from the API.
	var out bytes.Buffer
	if err := json.Compact(&out, buf.Bytes()); err != nil {
		return nil, fmt.Errorf("unknownfields: invalid extra member in %T: %w", known, err)
	}
	return out.Bytes(), nil
}

// nameSet holds a struct's JSON member names, lowercased for the
// case-insensitive matching encoding/json performs on decode.
type nameSet map[string]struct{}

func (s nameSet) has(key string) bool {
	_, ok := s[strings.ToLower(key)]
	return ok
}

var namesCache sync.Map // reflect.Type -> nameSet

func fieldNames(t reflect.Type) nameSet {
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	if cached, ok := namesCache.Load(t); ok {
		return cached.(nameSet)
	}
	names := nameSet{}
	collectFieldNames(t, names)
	namesCache.Store(t, names)
	return names
}

func collectFieldNames(t reflect.Type, names nameSet) {
	if t.Kind() != reflect.Struct {
		return
	}
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		tag := f.Tag.Get("json")
		if tag == "-" {
			continue
		}
		name, _, _ := strings.Cut(tag, ",")

		if f.Anonymous && name == "" {
			ft := f.Type
			if ft.Kind() == reflect.Pointer {
				ft = ft.Elem()
			}
			if ft.Kind() == reflect.Struct {
				collectFieldNames(ft, names)
				continue
			}
		}
		if !f.IsExported() {
			continue
		}
		if name == "" {
			name = f.Name
		}
		names[strings.ToLower(name)] = struct{}{}
	}
}
