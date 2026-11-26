package profiling

import (
	"fmt"
	"math"
	"strings"
)

// Node accessors for the API's render tree, shared by the folded-stack renderers.

// treeNode is one hotspots node: its frame label, whether --app-only keeps it,
// its per-state sample counts (cumulative, as the API reports them) and its
// children's ids.
type treeNode struct {
	label    string
	isApp    bool
	samples  map[string]int
	children []string
}

// hotspotTree indexes the dataNodes of a hotspots result (raw, the API's
// decoded document) by id and returns the root id. It returns a nil map when
// the result holds no nodes.
func hotspotTree(raw interface{}) (map[string]*treeNode, string) {
	result, _ := raw.(map[string]interface{})
	nodes := dataNodes(result)
	if len(nodes) == 0 {
		return nil, ""
	}
	nodeMap := make(map[string]*treeNode, len(nodes))
	for _, n := range nodes {
		nm, ok := n.(map[string]interface{})
		if !ok {
			continue
		}
		nd := &treeNode{
			label:   nodeLabel(nm),
			isApp:   nodeIsApp(nm),
			samples: nodeSamples(nm),
		}
		if cids, ok := nm["childIds"].([]interface{}); ok {
			for _, c := range cids {
				if s, ok := c.(string); ok {
					nd.children = append(nd.children, s)
				}
			}
		}
		nodeMap[nodeIDStr(nm["id"])] = nd
	}
	return nodeMap, nodeIDStr(result["dataRootNodeId"])
}

// self derives a node's own (exclusive) per-state counts from the cumulative API
// counts: self = node − Σ children, clamped at zero per state to absorb sampling
// jitter and synthetic roots whose own count is below their children's.
func (n *treeNode) self(nodeMap map[string]*treeNode) map[string]int {
	self := make(map[string]int, len(n.samples))
	for k, v := range n.samples {
		self[k] = v
	}
	for _, cid := range n.children {
		c := nodeMap[cid]
		if c == nil {
			continue
		}
		for k, v := range c.samples {
			self[k] -= v
		}
	}
	for k, v := range self {
		if v < 0 {
			self[k] = 0
		}
	}
	return self
}

func dataNodes(result map[string]interface{}) []interface{} {
	if result == nil {
		return nil
	}
	nodes, _ := result["dataNodes"].([]interface{})
	return nodes
}

// nodeIsApp reports whether a frame is the application's own code, the frames
// --app-only keeps.
//
// The API's flag is apiInfo.systemApi, and it is not an "is app code" flag: it is
// true for every frame no built-in library rule claims, i.e. the language's default
// rule ("Java", "Node.js"). On Java that is app code plus whatever has no rule of its
// own, the OneAgent's com.dynatrace.agent.* frames included (the built-in JRE,
// Spring and Apache rules are false). On Node.js every frame is true: the runtime
// (node:internal/...) falls under the "Node.js" default rule, and npm packages get a
// file-path rule per package that is also true. So the flag is the base, and three
// kinds of frames it lets through are dropped here:
//   - the OneAgent's own frames, on any runtime (see isAgentFrame);
//   - npm packages: rule ids FilePathBasedApiRuleId-<package> (from node_modules);
//   - the Node.js runtime: files under node: (node:internal/..., node:fs).
func nodeIsApp(n map[string]interface{}) bool {
	ai, _ := n["apiInfo"].(map[string]interface{})
	if ai == nil {
		return false
	}
	if b, _ := ai["systemApi"].(bool); !b {
		return false
	}
	if isAgentFrame(n) {
		return false
	}
	if id, _ := ai["id"].(string); strings.HasPrefix(id, "FilePathBasedApiRuleId-") {
		return false
	}
	return !strings.HasPrefix(nodeFile(n), "node:")
}

// agentFramePrefixes are the namespaces of the OneAgent's own code: its Java agent
// (com.dynatrace.agent.introspection, ...shaded.uem...) and its .NET agent.
var agentFramePrefixes = []string{"com.dynatrace.agent.", "Dynatrace.OneAgent."}

// isAgentFrame reports whether a frame is the OneAgent's own code. Matched on the
// class path with a trailing dot, so an application that lives under com.dynatrace
// (com.dynatrace.easytrade...) is not caught. The Java agent also injects bridge
// classes into the instrumented library's own package
// (jakarta.servlet.http.DynatraceJakartaUemIntrospection), caught by class name.
// Memory-tree nodes carry only a dotted name, which is matched the same way.
func isAgentFrame(n map[string]interface{}) bool {
	cp, _ := n["classPath"].(string)
	if cp == "" {
		cp, _ = n["name"].(string)
	}
	cp += "."
	for _, p := range agentFramePrefixes {
		if strings.HasPrefix(cp, p) {
			return true
		}
	}
	cn, _ := n["className"].(string)
	return strings.HasPrefix(cn, "Dynatrace") && strings.Contains(cn, "Introspection")
}

// nodeLabel names a frame: classPath.className.methodName for class-based runtimes
// (Java, .NET, Go). Node.js frames have no class: they carry the function name plus
// fileName/filePath, so they read file:function (see nodeFile). A frame with no
// method name has no label.
func nodeLabel(n map[string]interface{}) string {
	cp, _ := n["classPath"].(string)
	cn, _ := n["className"].(string)
	mn, _ := n["methodName"].(string)
	if mn == "" {
		return ""
	}
	if cn == "" {
		if f := nodeFile(n); f != "" {
			return f + ":" + mn
		}
		return mn
	}
	if cp != "" {
		return cp + "." + cn + "." + mn
	}
	return cn + "." + mn
}

// nodeFile is a class-less frame's file: filePath/fileName, with everything up to
// the last node_modules/ cut, so a package frame reads graphql/type/definition.js
// and not /app/node_modules/graphql/type/definition.js. Runtime files keep their
// node: prefix (node:internal/timers, node:fs). Empty for frames with no file.
func nodeFile(n map[string]interface{}) string {
	fp, _ := n["filePath"].(string)
	fn, _ := n["fileName"].(string)
	f := fn
	if fp != "" && fn != "" {
		f = strings.TrimSuffix(fp, "/") + "/" + fn
	} else if fp != "" {
		f = fp
	}
	if i := strings.LastIndex(f, "node_modules/"); i >= 0 {
		f = f[i+len("node_modules/"):]
	}
	return f
}

func nodeSamples(n map[string]interface{}) map[string]int {
	out := map[string]int{"RUNNING": 0, "LOCK": 0, "NET_IO": 0, "DISK_IO": 0, "WAIT": 0}
	s, ok := n["samples"].(map[string]interface{})
	if !ok {
		return out
	}
	for k := range out {
		if v, ok := s[k].(float64); ok {
			out[k] = floatInt(v)
		}
	}
	return out
}

func nodeIDStr(v interface{}) string {
	switch x := v.(type) {
	case string:
		return x
	case float64:
		return fmt.Sprintf("%g", x)
	}
	return ""
}

func strVal(m map[string]interface{}, key string) string {
	v, _ := m[key].(string)
	return v
}

func intVal(m map[string]interface{}, key string) int {
	v, _ := m[key].(float64)
	return floatInt(v)
}

// floatInt is the integer a JSON number stands for, held at the nearest int
// where it has none: converting a float64 out of range does not fail but gives
// an arbitrary value.
func floatInt(v float64) int {
	switch {
	case v >= float64(math.MaxInt): // 2^63, the first value an int cannot hold
		return math.MaxInt
	case v <= float64(math.MinInt):
		return math.MinInt
	case v != v: // NaN
		return 0
	}
	return int(v)
}
