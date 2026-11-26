package unknownfields

import (
	"encoding/json"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/Dynatrace/dtctl/pkg/util/format"
)

type yamlInner struct {
	Enabled bool `json:"enabled"`

	Extra map[string]json.RawMessage `json:"-" yaml:"-"`
}

func (v *yamlInner) UnmarshalJSON(data []byte) error {
	type plain yamlInner
	extra, err := Unmarshal(data, (*plain)(v))
	if err != nil {
		return err
	}
	v.Extra = extra
	return nil
}

func (v yamlInner) MarshalYAML() (any, error) {
	type plain yamlInner
	return YAML(plain(v), v.Extra)
}

type yamlOuter struct {
	Name        string    `json:"name"`
	Optional    *bool     `json:"optional,omitempty"`
	DisplayName string    `json:"displayName"`
	Inner       yamlInner `json:"inner"`
	List        []string  `json:"list"`

	Extra map[string]json.RawMessage `json:"-" yaml:"-"`
}

func (v *yamlOuter) UnmarshalJSON(data []byte) error {
	type plain yamlOuter
	extra, err := Unmarshal(data, (*plain)(v))
	if err != nil {
		return err
	}
	v.Extra = extra
	return nil
}

func (v yamlOuter) MarshalYAML() (any, error) {
	type plain yamlOuter
	return YAML(plain(v), v.Extra)
}

func TestYAMLKeepsReflectedShapeAndAppendsExtra(t *testing.T) {
	var v yamlOuter
	doc := `{"name":"x","displayName":"d","inner":{"enabled":false,"newBlock":{"b":1,"a":2}},"list":[],"big":9007199254740993,"tenantInstanceId":null}`
	if err := json.Unmarshal([]byte(doc), &v); err != nil {
		t.Fatal(err)
	}
	out, err := yaml.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}

	// Modelled fields keep yaml.v3's reflected (lowercased) names, order and
	// empty values; unknown members follow under their original names, in
	// key order.
	want := `name: x
optional: null
displayname: d
inner:
    enabled: false
    newBlock:
        a: 2
        b: 1
list: []
big: 9007199254740993
tenantInstanceId: null
`
	if string(out) != want {
		t.Fatalf("yaml mismatch\n got:\n%s\nwant:\n%s", out, want)
	}

	// Reading it back the way apply does (YAML -> JSON -> typed) restores the
	// typed fields and the unknown members, and a lowercased modelled key
	// (displayname) fills its field instead of becoming an unknown member.
	back, err := format.YAMLToJSON(out)
	if err != nil {
		t.Fatal(err)
	}
	var again yamlOuter
	if err := json.Unmarshal(back, &again); err != nil {
		t.Fatal(err)
	}
	if again.Name != "x" || again.DisplayName != "d" || len(again.Extra) != 2 ||
		string(again.Extra["big"]) != "9007199254740993" ||
		string(again.Extra["tenantInstanceId"]) != "null" ||
		string(again.Inner.Extra["newBlock"]) != `{"a":2,"b":1}` {
		t.Fatalf("yaml round trip lost data: %+v / inner %+v", again, again.Inner)
	}
}

func TestYAMLWithoutExtraIsPlainReflection(t *testing.T) {
	v := yamlOuter{Name: "n", DisplayName: "d"}
	got, err := yaml.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	type plain yamlOuter
	want, err := yaml.Marshal(plain(v))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(want) {
		t.Fatalf("got:\n%s\nwant:\n%s", got, want)
	}
}

// yaml.Node.Encode returns the document's root content, not the DocumentNode
// wrapper, so YAML sees a MappingNode and can append the extra members.
func TestYAMLReturnsMappingNodeWithExtra(t *testing.T) {
	type plain yamlInner
	got, err := YAML(plain{Enabled: true}, map[string]json.RawMessage{"newBlock": json.RawMessage(`{"a":1}`)})
	if err != nil {
		t.Fatal(err)
	}
	node, ok := got.(*yaml.Node)
	if !ok || node.Kind != yaml.MappingNode {
		t.Fatalf("want *yaml.Node of kind MappingNode, got %#v", got)
	}
	if n := len(node.Content); n != 4 || node.Content[2].Value != "newBlock" {
		t.Fatalf("extra member not appended: %d nodes", n)
	}
}
