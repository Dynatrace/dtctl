package reposcope

import (
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

// scopeDoc renders a one-entry document for prodHost from the entry's lines.
func scopeDoc(lines ...string) string {
	return "environments:\n  " + prodHost + ":\n    - " + strings.Join(lines, "\n      ") + "\n"
}

// A value somebody half wrote must never be read as "no constraint": a null
// path is not the whole repository, a blank cluster-uid is not every
// cluster, and a null list item is not an item to drop. Each is refused with
// the line it sits on.
func TestDecode_MalformedIsRefused(t *testing.T) {
	tests := []struct {
		name string
		doc  string
		msg  string
	}{
		{"a null path", scopeDoc("name: checkout", "path: ~", "service-names: [checkout]"),
			"path on line 4 has no value"},
		{"a blank path", scopeDoc("name: checkout", "path:", "service-names: [checkout]"),
			"path on line 4 has no value"},
		{"a blank cluster uid", scopeDoc("name: checkout", "workloads:", "  - cluster-uid:", "    namespace: payments", "    name: checkout"),
			"cluster-uid on line 5 has no value"},
		{"an empty path", scopeDoc("name: checkout", `path: ""`, "service-names: [checkout]"),
			"path on line 4 has no value"},
		{"an empty path under a tag", scopeDoc("name: checkout", `path: !!str ""`, "service-names: [checkout]"),
			"the tag !!str on line 4 is not accepted"},
		{"an empty block for a path", scopeDoc("name: checkout", "path: |", "service-names: [checkout]"),
			"path on line 4 has no value"},
		{"an empty cluster uid", scopeDoc("name: checkout", "workloads:", "  - cluster-uid: ''", "    namespace: payments", "    name: checkout"),
			"cluster-uid on line 5 has no value"},
		{"an empty cluster uid in a flow mapping", scopeDoc("name: checkout", `workloads: [{namespace: payments, name: checkout, cluster-uid: ""}]`),
			"cluster-uid on line 4 has no value"},
		{"an empty segment list", scopeDoc("name: checkout", "service-names: [checkout]", "segments: []"),
			"segments on line 5 has no value"},
		{"empty variables", scopeDoc("name: checkout", "segments:", "  - id: Ab3dEf9hIjk", "    variables: {}"),
			"variables on line 6 has no value"},
		{"a null segment", scopeDoc("name: checkout", "service-names: [checkout]", "segments: [null]"),
			"segments[0] on line 5 has no value"},
		{"a null service name", scopeDoc("name: checkout", "service-names:", "  - checkout", "  -"),
			"service-names[1] on line 6 has no value"},
		{"a null workload", scopeDoc("name: checkout", "workloads:", "  - ~"),
			"workloads[0] on line 5 has no value"},
		{"a null variable value", scopeDoc("name: checkout", "segments:", "  - id: Ab3dEf9hIjk", "    variables: {env: [~]}"),
			`variable "env"[0] on line 6 has no value`},
		{"null variables", scopeDoc("name: checkout", "segments:", "  - id: Ab3dEf9hIjk", "    variables:"),
			"variables on line 6 has no value"},
		{"a null binding list", scopeDoc("name: checkout", "service-names: [checkout]", "services:"),
			"services on line 5 has no value"},
		{"a null entry", "environments:\n  " + prodHost + ":\n    - ~\n",
			"entry on line 3 has no value"},
		{"an environment without a value", "environments:\n  " + prodHost + ":\n",
			`environment "` + prodHost + `" on line 2 has no value`},
		{"null environments", "environments:\n", "environments on line 1 has no value"},
		{"an empty apiVersion", "apiVersion: ''\n" + scopeDoc("name: checkout", "service-names: [checkout]"),
			"apiVersion on line 1 has no value"},
		{"a null apiVersion", "apiVersion:\n" + scopeDoc("name: checkout", "service-names: [checkout]"),
			"apiVersion on line 1 has no value"},

		{"an empty key in an entry", scopeDoc("name: checkout", "service-names: [checkout]", `"": 1`),
			`unknown key "" on line 5, in an entry`},
		{"a key of two lines in a workload", scopeDoc("name: checkout", "workloads:", "  - namespace: payments", "    name: checkout", `    "cluster\nuid": `+clusterUID),
			`unknown key "cluster\nuid" on line 7, in a workload`},
		{"a null key in an entry", scopeDoc("name: checkout", "service-names: [checkout]", "~: 1"),
			"the key on line 5 must be a plain string"},
		{"a list for a key", scopeDoc("name: checkout", "service-names: [checkout]", "? [a]\n      : 1"),
			"the key on line 5 must be a plain string"},
		{"a null environment", "environments:\n  ~:\n    - name: checkout\n      service-names: [checkout]\n",
			"the key on line 2 must be a plain string"},

		{"a key given twice", scopeDoc("name: checkout", "path: services/checkout", "service-names: [checkout]", "path: services/ledger"),
			`key "path" on line 6 is already given on line 4`},
		{"an environment given twice",
			scopeDoc("name: checkout", "service-names: [checkout]") + "  " + prodHost + ":\n    - name: ledger\n      service-names: [ledger]\n",
			`key "` + prodHost + `" on line 5 is already given on line 2`},
		{"an environment repeated through an alias",
			"environments:\n  &k " + prodHost + ":\n    - name: checkout\n      service-names: [checkout]\n  *k :\n    - name: ledger\n      service-names: [ledger]\n",
			"the anchor &k on line 2 is not accepted"},
		{"an alias for a value", "x-names: &n [checkout]\n" + scopeDoc("name: checkout", "service-names: *n"),
			"the anchor &n on line 1 is not accepted"},
		{"a merge key", scopeDoc("name: checkout", "service-names: [checkout]", "<<: {path: services/checkout}"),
			"the merge key << on line 5 is not accepted"},
		{"a merge key of the document", "<<: {environments: {" + prodHost + ": [{name: checkout, service-names: [checkout]}]}}\n",
			"the merge key << on line 1 is not accepted"},

		{"a list for the document", "- environments\n", "does not match the repo scope schema: the document on line 1 must be a mapping"},
		{"a list for environments", "environments: [" + prodHost + "]\n",
			"does not match the repo scope schema: environments on line 1 must be a mapping"},
		{"a mapping for an environment", "environments:\n  " + prodHost + ":\n    name: checkout\n",
			`does not match the repo scope schema: environment "` + prodHost + `" on line 2 must be a list`},
		{"a name for an entry", "environments:\n  " + prodHost + ":\n    - checkout\n",
			"does not match the repo scope schema: entry on line 3 must be a mapping"},
		{"a mapping for a name", scopeDoc("name: {a: b}", "service-names: [checkout]"),
			"does not match the repo scope schema: name on line 3 must be a single value"},
		{"one value for a list", scopeDoc("name: checkout", "services: "+svcID),
			"does not match the repo scope schema: services on line 4 must be a list"},
		{"one value for a variable", scopeDoc("name: checkout", "segments: [{id: Ab3dEf9hIjk, variables: {env: prod}}]"),
			`does not match the repo scope schema: variable "env" on line 4 must be a list`},
		{"a list in a list", scopeDoc("name: checkout", "service-names: [[checkout]]"),
			"does not match the repo scope schema: service-names[0] on line 4 must be a single value"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f, err := decode([]byte(tt.doc))
			require.NotNil(t, err, "loaded as %+v", f)
			assert.Nil(t, f)
			assert.Contains(t, err.Msg, tt.msg)
			assert.NotContains(t, err.Msg, "\n", "one line, wherever it is printed")
		})
	}
}

// A file a later format wrote is refused by its version, whatever else it
// holds: this dtctl cannot say what a key or a shape of that format means,
// so the only useful advice is to upgrade.
func TestDecode_AnotherVersionIsRefusedByName(t *testing.T) {
	for name, doc := range map[string]string{
		"with an entry key this dtctl does not know": "apiVersion: v2\n" + scopeDoc("name: checkout", "service-names: [checkout]", "hosts: [h1]"),
		"with environments of another shape":         "apiVersion: v2\nenvironments:\n  - host: " + prodHost + "\n",
		"with anchors":                               "apiVersion: v2\ndefaults: &d {a: b}\nenvironments: *d\n",
		"with a second document":                     "apiVersion: v2\n---\nmore: true\n",
		"after the environments":                     scopeDoc("name: checkout", "selector: {a: b}") + "apiVersion: v2\n",
		"as a number":                                "apiVersion: 2\nenvironments: []\n",
	} {
		t.Run(name, func(t *testing.T) {
			_, err := decode([]byte(doc))
			require.NotNil(t, err)
			assert.Regexp(t, `^apiVersion "(v2|2)" on line \d+ is not supported \(this dtctl reads "v1"\)$`, err.Msg)
			assert.Equal(t, []string{"upgrade dtctl to read this file"}, err.Suggestions)
		})
	}
}

// What follows the document is YAML too: text that does not parse is reported
// as that, not as a second document.
func TestDecode_TrailingText(t *testing.T) {
	doc := scopeDoc("name: checkout", "service-names: [checkout]")

	_, err := decode([]byte(doc + "---\n{unclosed\n"))
	require.NotNil(t, err)
	assert.Contains(t, err.Msg, "not valid YAML")
	assert.NotContains(t, err.Msg, "more than one")

	_, err = decode([]byte(doc + "---\n---\n"))
	require.NotNil(t, err)
	assert.Equal(t, "holds more than one '---' after its document", err.Msg)
}

// hostileFiles are inputs of the size Load still reads, each built to make a
// YAML reader do more work than its bytes suggest.
func hostileFiles() map[string][]byte {
	const size = 1_000_000
	wide := []byte("environments:\n")
	for i := 0; len(wide) < size; i++ {
		wide = fmt.Appendf(wide, " h%d: []\n", i)
	}
	keys := []byte(scopeDoc("name: checkout", "service-names: [checkout]"))
	for i := 0; len(keys) < size; i++ {
		keys = fmt.Appendf(keys, "k%d: 1\n", i)
	}
	aliases := []byte("x: &a [" + strings.TrimSuffix(strings.Repeat("checkout,", 20), ",") + "]\n" +
		"environments:\n  " + prodHost + ":\n    - name: checkout\n      service-names: [")
	for len(aliases) < size {
		aliases = append(aliases, "*a,"...)
	}
	aliases = append(aliases, "]\n"...)
	// YAML bounds an implicit key at 1,024 bytes and an explicit one ("? key")
	// not at all, so a key can be as long as the file allows, and whatever
	// the reader does once per item under it is done with all of it.
	longKey := strings.Repeat("k", size/2)
	values := "[" + strings.TrimSuffix(strings.Repeat("a,", size/5), ",") + "]"
	variable := "environments:\n  " + prodHost + ":\n    - name: checkout\n      segments:\n        - id: Ab3dEf9hIjk\n" +
		"          variables:\n            ? " + longKey + "\n            : " + values + "\n"
	host := "environments:\n  ? " + strings.Repeat("k", size/10) + "\n  :\n" + strings.Repeat("    - {}\n", size/50)
	return map[string][]byte{
		"a long variable name over many values": []byte(variable),
		"a long host over many entries":         []byte(host),
		"one wide mapping":                      wide,
		"unknown document keys":                 keys,
		"aliases of one anchor":                 aliases,
		"documents after the first":             []byte(scopeDoc("name: checkout", "service-names: [checkout]") + strings.Repeat("---\n", size/4)),
		"nested lists":                          []byte("environments: " + strings.Repeat("[", size/2) + strings.Repeat("]", size/2) + "\n"),
		"one long key":                          []byte(strings.Repeat("k", size) + ": 1\n"),
	}
}

// Load promises that a cloned repository cannot hang every query run inside
// it. The bound is loose on purpose: a reader that compares every key with
// every other takes more than a minute over the first of these.
func TestDecode_HostileFileIsAnsweredAtOnce(t *testing.T) {
	for name, data := range hostileFiles() {
		t.Run(name, func(t *testing.T) {
			require.LessOrEqual(t, len(data), maxFileBytes)
			start := time.Now()
			_, _ = decode(data)
			assert.Less(t, time.Since(start), 5*time.Second)
		})
	}
}

// No key of the format is longer than a host or an annotation key, and a
// longer one is refused by its line, without being quoted.
func TestDecode_RefusesAKeyLongerThanTheFormatHas(t *testing.T) {
	long := strings.Repeat("k", maxKeyBytes+1)
	for name, doc := range map[string]string{
		"a document key":  long + ": 1\n" + scopeDoc("name: checkout", "service-names: [checkout]"),
		"an environment":  "environments:\n  " + long + ":\n    - name: checkout\n      service-names: [checkout]\n",
		"an entry key":    scopeDoc("name: checkout", "service-names: [checkout]", long+": 1"),
		"a workload key":  scopeDoc("name: checkout", "workloads: [{namespace: payments, name: checkout, "+long+": 1}]"),
		"a variable name": scopeDoc("name: checkout", "segments: [{id: Ab3dEf9hIjk, variables: {"+long+": [a]}}]"),
		"an annotation":   scopeDoc("name: checkout", "service-names: [checkout]", "annotations: {"+long+": a}"),
	} {
		t.Run(name, func(t *testing.T) {
			_, err := decode([]byte(doc))
			require.NotNil(t, err)
			assert.Regexp(t, `^the key on line \d+ is longer than 253 bytes, which no key of the format is$`, err.Msg)
		})
	}
	_, err := decode([]byte(scopeDoc("name: checkout", "service-names: [checkout]", "annotations: {"+long[1:]+": a}")))
	require.Nil(t, err, "a key of the longest length loads")
}

// Reading an item costs nothing that grows with the key it sits under: where
// the item is, is put into words only for a message. The list here is read
// with the one allocation of its result.
func TestItems_DescribeAnItemOnlyForAMessage(t *testing.T) {
	var list yaml.Node
	require.NoError(t, yaml.Unmarshal([]byte("["+strings.TrimSuffix(strings.Repeat("a,", 1000), ",")+"]"), &list))
	at := place{what: "variable " + strings.Repeat("k", maxKeyBytes), line: 1}
	allocs := testing.AllocsPerRun(10, func() {
		values, err := items(list.Content[0], at, text)
		if err != nil || len(values) != 1000 {
			t.Fatalf("read %d values: %v", len(values), err)
		}
	})
	assert.LessOrEqual(t, allocs, 2.0)

	_, err := items(list.Content[0], at, func(n *yaml.Node, p place) (string, *InvalidError) { return "", noValue(p) })
	require.NotNil(t, err)
	assert.Equal(t, at.what+"[0] on line 1 has no value", err.Msg)
}

func BenchmarkDecode_Hostile(b *testing.B) {
	for name, data := range hostileFiles() {
		b.Run(name, func(b *testing.B) {
			b.SetBytes(int64(len(data)))
			for b.Loop() {
				_, _ = decode(data)
			}
		})
	}
}

// What the YAML parser says is put on one line, like every other reason.
func TestNotYAMLIsOneLine(t *testing.T) {
	assert.Equal(t, "not valid YAML: line 2: first second", notYAML(errors.New("yaml: line 2: first\n  second")).Msg)
}

// The file is written through types of its own, which spell its keys. A
// field added to an entry or a workload and not to them would be read, shown
// and then dropped by the next save, so the two are held field for field,
// and an entry with every field set is written and read back whole.
func TestFileTypesCarryEveryField(t *testing.T) {
	for _, pair := range [][2]reflect.Type{
		{reflect.TypeFor[Entry](), reflect.TypeFor[fileEntry]()},
		{reflect.TypeFor[Workload](), reflect.TypeFor[fileWorkload]()},
	} {
		model, file := pair[0], pair[1]
		require.Equal(t, model.NumField(), file.NumField(), "%s and %s", model, file)
		for i := range model.NumField() {
			assert.Equal(t, model.Field(i).Name, file.Field(i).Name, "%s field %d", file, i)
		}
	}

	full := Entry{
		Name: "checkout", Path: "services/checkout",
		Workloads:     []Workload{{ClusterUID: clusterUID, Namespace: "payments", Name: "checkout"}},
		ServiceNames:  []string{"checkout"},
		ProcessGroups: []string{pgID},
		Services:      []string{svcID},
		Segments:      []Segment{{ID: "Ab3dEf9hIjk", Variables: map[string][]string{"environment": {"production"}}}},
		Annotations:   map[string]string{"owner": "team-payments"},
	}
	for _, v := range []reflect.Value{reflect.ValueOf(full), reflect.ValueOf(full.Workloads[0]), reflect.ValueOf(full.Segments[0])} {
		for i := range v.NumField() {
			require.False(t, v.Field(i).IsZero(), "%s.%s is not set, so the test does not cover it", v.Type(), v.Type().Field(i).Name)
		}
	}
	f := &File{APIVersion: apiVersion, Environments: map[string][]Entry{prodHost: {full}}}
	data, err := encode(f)
	require.NoError(t, err)
	back, invalidErr := decode(data)
	require.Nil(t, invalidErr)
	assert.Equal(t, f, back)
	assert.Contains(t, string(data), "cluster-uid: ")
	assert.Contains(t, string(data), "service-names:")
	assert.Contains(t, string(data), "process-groups:")
	assert.NotRegexp(t, `(?m)^[ -]*[A-Za-z-]*[a-z][A-Z][A-Za-z]*:`, strings.SplitN(string(data), "apiVersion: v1\n", 2)[1], "a camelCase key in the file")
}

// A value is read as the text its line shows, and a tag is a way to say it is
// something else. None is accepted, on a value, on a key, on the document or
// on what follows it.
func TestDecode_RefusesAnExplicitTag(t *testing.T) {
	for name, tt := range map[string]struct{ doc, msg string }{
		"an item":                      {scopeDoc("name: checkout", "service-names: [!!int abc]"), "the tag !!int on line 4 is not accepted"},
		"a tag of its own":             {scopeDoc("name: checkout", "service-names: [!foo abc]"), "the tag !foo on line 4 is not accepted"},
		"a string said to be one":      {scopeDoc("name: checkout", "service-names: [!!str 5]"), "the tag !!str on line 4 is not accepted"},
		"a path":                       {scopeDoc("name: checkout", "path: !!float a", "service-names: [checkout]"), "the tag !!float on line 4 is not accepted"},
		"a key":                        {scopeDoc("!!binary bmFtZQ==: checkout", "service-names: [checkout]"), "the tag !!binary on line 3 is not accepted"},
		"a value in binary":            {scopeDoc("name: !!binary Y2hlY2tvdXQ=", "service-names: [checkout]"), "the tag !!binary on line 3 is not accepted"},
		"a version in binary":          {"apiVersion: !!binary djI=\n" + scopeDoc("name: checkout", "service-names: [checkout]"), "the tag !!binary on line 1 is not accepted"},
		"the document":                 {"!!null whatever\n", "the tag !!null on line 1 is not accepted"},
		"a list":                       {scopeDoc("name: checkout", "service-names: !!seq [checkout]"), "the tag !!seq on line 4 is not accepted"},
		"a second document":            {scopeDoc("name: checkout", "service-names: [checkout]") + "--- !!null \"environments: {}\"\n", "holds more than one YAML document"},
		"an anchor after the document": {scopeDoc("name: checkout", "service-names: [checkout]") + "--- &a\n", "holds more than one YAML document"},
	} {
		t.Run(name, func(t *testing.T) {
			f, err := decode([]byte(tt.doc))
			require.NotNil(t, err, "loaded as %+v", f)
			assert.Equal(t, tt.msg, err.Msg)
		})
	}
}

// What a file makes dtctl say is bounded like what it makes dtctl do: a
// warning is printed by every scoped query and travels in its envelope.
func TestDecode_HostileFileGetsAShortAnswer(t *testing.T) {
	const size = 1_000_000
	valid := scopeDoc("name: checkout", "service-names: [checkout]")
	keys := []byte(valid)
	for i := 0; len(keys) < size; i++ {
		keys = fmt.Appendf(keys, "%s%d: 1\n", strings.Repeat("k", 200), i)
	}
	f, err := decode(keys)
	require.Nil(t, err)
	assert.Less(t, len(f.Warning()), maxMessageBytes+1)
	assert.Regexp(t, `^unknown keys "k+0" on line 5, .* and \d+ more \(a typo, or written by a newer dtctl\)$`, f.Warning())

	entryKeys := []byte(valid)
	for i := 0; len(entryKeys) < size; i++ {
		entryKeys = fmt.Appendf(entryKeys, "      %s%d: 1\n", strings.Repeat("k", 200), i)
	}
	long := strings.Repeat("v", size)
	for name, data := range map[string][]byte{
		"unknown entry keys": entryKeys,
		"a long version":     []byte("apiVersion: " + long + "\n"),
		"a long anchor":      []byte("environments: &" + long + " {}\n"),
		"a long tag":         []byte("environments: !" + long + " {}\n"),
		"a long name":        []byte(scopeDoc("name: "+long, "service-names: [checkout]")),
		"a long service":     []byte(scopeDoc("name: checkout", "service-names: ["+long+"]")),
	} {
		t.Run(name, func(t *testing.T) {
			require.LessOrEqual(t, len(data), maxFileBytes)
			_, err := decode(data)
			require.NotNil(t, err)
			assert.Less(t, len(err.Msg), maxMessageBytes+1)
		})
	}
}
