package reposcope

import (
	"fmt"
	"math/rand/v2"
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

// The keys of the format, by where they sit. A key that narrows is one whose
// absence has a meaning of its own, and a wider one: an entry without a path
// covers the whole repository, a workload without a cluster is bound in every
// cluster, an entry without segments is narrowed by none, and a segment
// without variables is sent as it stands.
var (
	entryKeys    = []string{"name", "path", "workloads", "service-names", "process-groups", "services", "segments", "annotations"}
	workloadKeys = []string{"cluster-uid", "namespace", "name"}
	segmentKeys  = []string{"id", "variables"}
)

// failer is what the oracle needs of a test, so that the oracle can itself
// be tested on a read it must refuse.
type failer interface {
	require.TestingT
	Helper()
}

// refusal records that the oracle failed, and ends the check where a test
// would end.
type refusal struct{ failed bool }

func (r *refusal) Errorf(string, ...any) { r.failed = true }
func (r *refusal) FailNow()              { panic(r) }
func (r *refusal) Helper()               {}

// keysOf returns the mapping n as its keys' values, refusing a key outside
// known.
func keysOf(t failer, n *yaml.Node, known []string, doc string) map[string]*yaml.Node {
	t.Helper()
	require.Equal(t, yaml.MappingNode, n.Kind, "line %d\n%s", n.Line, doc)
	out := map[string]*yaml.Node{}
	for i := 0; i+1 < len(n.Content); i += 2 {
		key := n.Content[i].Value
		require.Contains(t, known, key, "a key outside the format was read past on line %d\n%s", n.Content[i].Line, doc)
		out[key] = n.Content[i+1]
	}
	return out
}

// requireNothingWider holds a file that loaded to the text it was loaded
// from, without the reader's help: the text is walked as plain YAML nodes.
// Every key written inside an entry is one of the format's, and every key
// that narrows is in the loaded entry with the value the line shows, so no
// entry is wider than the file says. The reverse holds too: the loaded entry
// has no path, cluster, segment or variable the text does not write.
func requireNothingWider(t failer, doc string, f *File) {
	t.Helper()
	var root yaml.Node
	if strings.TrimSpace(doc) != "" { // a blank file need not be YAML
		require.NoError(t, yaml.Unmarshal([]byte(doc), &root), doc)
	}
	if len(root.Content) == 0 || root.Content[0].Kind != yaml.MappingNode {
		require.Empty(t, f.Environments, doc)
		return
	}
	var environments *yaml.Node
	for i := 0; i+1 < len(root.Content[0].Content); i += 2 {
		if root.Content[0].Content[i].Value == "environments" {
			environments = root.Content[0].Content[i+1]
		}
	}
	if environments == nil {
		require.Empty(t, f.Environments, doc)
		return
	}
	require.Len(t, f.Environments, len(environments.Content)/2, doc)
	for i := 0; i+1 < len(environments.Content); i += 2 {
		host, written := environments.Content[i].Value, environments.Content[i+1]
		entries := f.Environments[host]
		require.Len(t, entries, len(written.Content), "environment %s\n%s", host, doc)
		for j, e := range entries {
			keys := keysOf(t, written.Content[j], entryKeys, doc)
			where := fmt.Sprintf("entry %q on line %d\n%s", e.Name, written.Content[j].Line, doc)

			path, hasPath := keys["path"]
			require.Equal(t, hasPath, e.Path != "", "a path is written exactly when the entry has one: %s", where)
			if hasPath {
				require.Equal(t, path.Value, e.Path, where)
			}

			workloads := keys["workloads"]
			require.Equal(t, workloads != nil && len(workloads.Content) > 0, len(e.Workloads) > 0, where)
			for k, w := range e.Workloads {
				wk := keysOf(t, workloads.Content[k], workloadKeys, doc)
				cluster, hasCluster := wk["cluster-uid"]
				require.Equal(t, hasCluster, w.ClusterUID != "", "a cluster is written exactly when the workload has one: %s", where)
				if hasCluster {
					require.Equal(t, cluster.Value, w.ClusterUID, where)
				}
			}

			segments, hasSegments := keys["segments"]
			require.Equal(t, hasSegments, len(e.Segments) > 0, "segments are written exactly when the entry has some: %s", where)
			for k, s := range e.Segments {
				require.Len(t, e.Segments, len(segments.Content), where)
				sk := keysOf(t, segments.Content[k], segmentKeys, doc)
				variables, hasVariables := sk["variables"]
				require.Equal(t, hasVariables, len(s.Variables) > 0, "variables are written exactly when the segment has some: %s", where)
				if !hasVariables {
					continue
				}
				require.Len(t, s.Variables, len(variables.Content)/2, where)
				for v := 0; v+1 < len(variables.Content); v += 2 {
					var values []string
					for _, item := range variables.Content[v+1].Content {
						values = append(values, item.Value)
					}
					require.NotEmpty(t, values, where)
					require.Equal(t, values, s.Variables[variables.Content[v].Value], where)
				}
			}
		}
	}
}

// requireWrittenLoads holds a file that loaded to what Save makes of it: the
// bytes written load again, to the same entries.
func requireWrittenLoads(t *testing.T, f *File) {
	t.Helper()
	written, err := encode(f)
	require.NoError(t, err)
	again, invalid := decode(written)
	require.Nil(t, invalid, "what Save writes does not load:\n%s", written)
	rewritten, err := encode(again)
	require.NoError(t, err)
	require.Equal(t, string(written), string(rewritten))
}

// The oracle itself must fail on a wider read, or it proves nothing: each
// row is a text and a value no reader may answer it with.
func TestRequireNothingWider_SeesAWiderRead(t *testing.T) {
	entry := func(e Entry) *File { return &File{Environments: map[string][]Entry{prodHost: {e}}} }
	checkout := []Workload{{Namespace: "payments", Name: "checkout"}}
	for name, tt := range map[string]struct {
		doc  string
		read *File
	}{
		"an empty path read as the whole repository": {
			scopeDoc("name: checkout", `path: ""`, "service-names: [checkout]"),
			entry(Entry{Name: "checkout", ServiceNames: []string{"checkout"}})},
		"an empty cluster read as every cluster": {
			scopeDoc("name: checkout", `workloads: [{namespace: payments, name: checkout, cluster-uid: ""}]`),
			entry(Entry{Name: "checkout", Workloads: checkout})},
		"a misspelt cluster key read past": {
			scopeDoc("name: checkout", "workloads: [{namespace: payments, name: checkout, cluster_uid: "+clusterUID+"}]"),
			entry(Entry{Name: "checkout", Workloads: checkout})},
		"an empty segment list read as no segments": {
			scopeDoc("name: checkout", "service-names: [checkout]", "segments: []"),
			entry(Entry{Name: "checkout", ServiceNames: []string{"checkout"}})},
		"empty variables read as none": {
			scopeDoc("name: checkout", "segments: [{id: Ab3dEf9hIjk, variables: {}}]"),
			entry(Entry{Name: "checkout", Segments: []Segment{{ID: "Ab3dEf9hIjk"}}})},
		"a dropped entry": {
			scopeDoc("name: checkout", "service-names: [checkout]"),
			&File{Environments: map[string][]Entry{prodHost: {}}}},
	} {
		t.Run(name, func(t *testing.T) {
			probe := &refusal{}
			func() {
				defer func() {
					if r := recover(); r != nil && r != any(probe) {
						panic(r)
					}
				}()
				requireNothingWider(probe, tt.doc, tt.read)
			}()
			assert.True(t, probe.failed, "the oracle accepted a read wider than the text")
		})
	}
}

// Every value of the format, written empty, fails the read: none has an
// empty spelling that means anything, and for a key that narrows the empty
// spelling would otherwise mean the widest thing the key can say.
func TestDecode_NoKeyIsReadEmpty(t *testing.T) {
	full := []string{
		"name: checkout", "path: services/checkout",
		"workloads: [{cluster-uid: " + clusterUID + ", namespace: payments, name: checkout}]",
		"service-names: [checkout]", "process-groups: [" + pgID + "]", "services: [" + svcID + "]",
		"segments: [{id: Ab3dEf9hIjk, variables: {env: [prod]}}]",
	}
	_, err := decode([]byte(scopeDoc(full...)))
	require.Nil(t, err, "the control loads")
	with := func(i int, line string) string {
		lines := slices.Clone(full)
		lines[i] = line
		return scopeDoc(lines...)
	}
	for name, doc := range map[string]string{
		"name":           with(0, `name: ""`),
		"path":           with(1, `path: ""`),
		"cluster-uid":    with(2, `workloads: [{cluster-uid: "", namespace: payments, name: checkout}]`),
		"namespace":      with(2, `workloads: [{namespace: "", name: checkout}]`),
		"workload name":  with(2, `workloads: [{namespace: payments, name: ""}]`),
		"workloads":      with(2, `workloads: []`),
		"service names":  with(3, `service-names: []`),
		"process groups": with(4, `process-groups: []`),
		"services":       with(5, `services: []`),
		// Beside segments an empty list would be the entry's last binding, and
		// read as left out it makes the entry "everything in the segment".
		"workloads, the only binding":      scopeDoc("name: checkout", "workloads: []", "segments: [{id: Ab3dEf9hIjk}]"),
		"service names, the only binding":  scopeDoc("name: checkout", "service-names: []", "segments: [{id: Ab3dEf9hIjk}]"),
		"process groups, the only binding": scopeDoc("name: checkout", "process-groups: []", "segments: [{id: Ab3dEf9hIjk}]"),
		"services, the only binding":       scopeDoc("name: checkout", "services: []", "segments: [{id: Ab3dEf9hIjk}]"),
		"service name":                     with(3, `service-names: [""]`),
		"process group":                    with(4, `process-groups: [""]`),
		"service":                          with(5, `services: [""]`),
		"segments":                         with(6, `segments: []`),
		"segment":                          with(6, `segments: [{}]`),
		"segment id":                       with(6, `segments: [{id: ""}]`),
		"variables":                        with(6, `segments: [{id: Ab3dEf9hIjk, variables: {}}]`),
		"variable name":                    with(6, `segments: [{id: Ab3dEf9hIjk, variables: {"": [prod]}}]`),
		"variable values":                  with(6, `segments: [{id: Ab3dEf9hIjk, variables: {env: []}}]`),
		"variable value":                   with(6, `segments: [{id: Ab3dEf9hIjk, variables: {env: [""]}}]`),
		"workload":                         with(2, `workloads: [{}]`),
		"entry":                            "environments:\n  " + prodHost + ":\n    - {}\n",
		"environment":                      "environments:\n  " + prodHost + ": []\n",
		"host":                             "environments:\n  \"\":\n    - name: checkout\n      service-names: [checkout]\n",
	} {
		t.Run(name, func(t *testing.T) {
			f, err := decode([]byte(doc))
			require.NotNil(t, err, "loaded as %+v", f)
		})
	}
}

// What Save writes is read back by the reader that refuses an empty value,
// so the writer must never spell one: an entry without a path, a workload
// without a cluster and a segment without variables are written without the
// key.
func TestEncode_WritesNoEmptyValue(t *testing.T) {
	f := &File{Environments: map[string][]Entry{prodHost: {
		{Name: "whole", Workloads: []Workload{{Namespace: "payments", Name: "checkout"}}, Segments: []Segment{{ID: "Ab3dEf9hIjk"}},
			ServiceNames: []string{}, Services: []string{}, ProcessGroups: []string{}, Annotations: map[string]string{}},
		{Name: "segments-only", Path: "services/ledger", Workloads: []Workload{}, Segments: []Segment{{ID: "Ab3dEf9hIjk", Variables: map[string][]string{}}}},
		{Name: "bindings-only", Path: "services/cart", ServiceNames: []string{"cart"}, Segments: []Segment{}},
	}}}
	require.Nil(t, f.check())
	requireWrittenLoads(t, f)
	written, err := encode(f)
	require.NoError(t, err)
	again, invalid := decode(written)
	require.Nil(t, invalid)
	requireNothingWider(t, string(written), again)
}

// Generated files, read whole and after a careless edit: whatever loads is
// no wider than its text, and is written back as a file that loads.
func TestDecode_NothingLoadsWiderThanWritten(t *testing.T) {
	rng := rand.New(rand.NewPCG(7, 8))
	loaded := 0
	for i := range 6000 {
		doc := randomScopeDoc(rng)
		if i%2 == 1 {
			doc = edit(rng, doc)
		}
		f, err := decode([]byte(doc))
		if err != nil {
			continue
		}
		loaded++
		requireNothingWider(t, doc, f)
		requireWrittenLoads(t, f)
	}
	assert.Greater(t, loaded, 2000, "most generated files load")
}
