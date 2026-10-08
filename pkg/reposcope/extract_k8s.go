package reposcope

import (
	"bytes"
	"path"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"
)

// Deployment descriptors name the workload exactly as Kubernetes, and so
// Dynatrace's k8s.workload.name, will. They are the strongest signal a
// repository carries.

// workloadKindList are the manifest kinds that run a workload.
var workloadKindList = []string{"Deployment", "StatefulSet", "DaemonSet", "Job", "CronJob", "Rollout"}

var (
	workloadKinds = keySet(workloadKindList...)
	// workloadKindRe is a cheap pre-check, so that only a YAML file that
	// mentions a workload kind is parsed.
	workloadKindRe    = regexp.MustCompile(`(?m)^kind:[ \t]*(` + strings.Join(workloadKindList, "|") + `)\b`)
	serviceNameAttrRe = regexp.MustCompile(`(?:^|,)\s*service\.name\s*=\s*([^,]+)`)
)

// manifestWorkloads reads every workload document in a Kubernetes manifest,
// each read on its own, claimed by the unit named like its workload or one of
// its images. A Helm template is not YAML; the documents before the first
// that fails to parse still count.
func manifestWorkloads(data []byte, file string) []sourceNames {
	if !workloadKindRe.Match(data) {
		return nil
	}
	var found []sourceNames
	for _, doc := range yamlDocuments(data) {
		if f, ok := workloadNames(doc, file); ok {
			found = append(found, f)
		}
	}
	return found
}

// yamlDocuments decodes the documents in data up to the first that fails to
// parse.
func yamlDocuments(data []byte) []*yaml.Node {
	dec := yaml.NewDecoder(bytes.NewReader(data))
	var docs []*yaml.Node
	for {
		var doc yaml.Node
		if err := dec.Decode(&doc); err != nil {
			return docs
		}
		if len(doc.Content) > 0 {
			docs = append(docs, doc.Content[0])
		}
	}
}

// workloadNames reads one workload document: its name, namespace, images
// and OpenTelemetry service names.
func workloadNames(doc *yaml.Node, file string) (sourceNames, bool) {
	if kind := mapValue(doc, "kind"); kind == nil || !workloadKinds[kind.Value] {
		return sourceNames{}, false
	}
	f := sourceNames{dir: path.Dir(file)}
	meta := mapValue(doc, "metadata")
	if name := mapValue(meta, "name"); name != nil {
		f.add(kindWorkload, name.Value, file, name.Line)
		f.claims = append(f.claims, name.Value)
	}
	if ns := mapValue(meta, "namespace"); ns != nil {
		f.add(kindNamespace, ns.Value, file, ns.Line)
	}
	for _, c := range containers(mapValue(doc, "spec")) {
		if image := mapValue(c, "image"); image != nil {
			f.add(kindImage, image.Value, file, image.Line)
			f.claims = append(f.claims, baseName(image.Value))
		}
		for _, env := range sequence(mapValue(c, "env")) {
			name, value := mapValue(env, "name"), mapValue(env, "value")
			if name == nil || value == nil {
				continue
			}
			if service := otelServiceName(name.Value, value.Value); service != "" {
				f.add(kindServiceName, service, file, value.Line)
			}
		}
	}
	return f, len(f.tokens) > 0
}

func otelServiceName(name, value string) string {
	switch name {
	case "OTEL_SERVICE_NAME":
		return value
	case "OTEL_RESOURCE_ATTRIBUTES":
		if m := serviceNameAttrRe.FindStringSubmatch(value); m != nil {
			return strings.TrimSpace(m[1])
		}
	}
	return ""
}

// containers finds every containers list under a workload spec, at whatever
// depth the kind nests its pod template (a CronJob's is three levels down).
func containers(n *yaml.Node) []*yaml.Node {
	if n == nil {
		return nil
	}
	var out []*yaml.Node
	switch n.Kind {
	case yaml.MappingNode:
		for i := 0; i+1 < len(n.Content); i += 2 {
			if n.Content[i].Value == "containers" {
				out = append(out, sequence(n.Content[i+1])...)
			} else {
				out = append(out, containers(n.Content[i+1])...)
			}
		}
	case yaml.SequenceNode:
		for _, item := range n.Content {
			out = append(out, containers(item)...)
		}
	}
	return out
}

// chartName reads a Helm chart's name. The chart is claimed by the unit of
// the same name; it does not make a unit of its own.
func chartName(data []byte, file string) []sourceNames {
	var chart struct {
		Name string `yaml:"name"`
	}
	if yaml.Unmarshal(data, &chart) != nil {
		return nil
	}
	t, ok := tokenAt(kindChart, chart.Name, file, string(data), "name:")
	if !ok {
		return nil
	}
	return []sourceNames{{dir: path.Dir(file), claims: []string{chart.Name}, tokens: []token{t}}}
}

func (f *sourceNames) add(kind, raw, file string, line int) {
	if t, ok := newToken(kind, raw, file, line); ok {
		f.tokens = append(f.tokens, t)
	}
}

func mapValue(n *yaml.Node, key string) *yaml.Node {
	if n == nil || n.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i+1 < len(n.Content); i += 2 {
		if n.Content[i].Value == key {
			return n.Content[i+1]
		}
	}
	return nil
}

func sequence(n *yaml.Node) []*yaml.Node {
	if n == nil || n.Kind != yaml.SequenceNode {
		return nil
	}
	return n.Content
}
