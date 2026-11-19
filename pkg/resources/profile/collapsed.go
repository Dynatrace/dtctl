package profile

import (
	"fmt"
	"sort"
	"strings"
)

// ToCollapsed renders collapsed (folded) stacks: one line per unique stack,
// `frame1;frame2;...;frameN running=.. lock=.. net_io=.. disk_io=.. wait=..`,
// sorted by RUNNING descending. Unlike single-metric folded output it keeps all
// five thread-state counts on each line, so one read shows whether a stack is
// CPU-bound or blocked without emitting a separate flamegraph per state.
// top > 0 caps the number of rows; it does not change the shape. appOnly strips
// frames the profiler did not classify as application code (apiInfo.systemApi)
// from each path — see nodeIsApp. Returns empty string for unsupported kinds or
// missing data.
func ToCollapsed(kind string, raw interface{}, top int, appOnly bool) string {
	switch kind {
	case "memoryAllocation", "memoryAllocationDetails":
		return memoryCollapsed(raw, top, appOnly)
	case "methodHotspots", "threadAnalysis":
		// handled below
	default:
		return ""
	}
	nodeMap, rootID := hotspotTree(raw)
	if nodeMap == nil {
		return ""
	}

	// Emit one folded line per unique stack. Two corrections over a naive
	// leaf-only fold, both load-bearing:
	//
	//  1. Code-level-analysis samples are CUMULATIVE (inclusive): a node's count
	//     includes everything its children accrued, so a parent's RUNNING equals
	//     the sum of its children's RUNNING plus whatever the method spent in its
	//     own body. A method that is hot in its own body while also calling others
	//     is an interior node, and a leaf-only fold drops all of that interior self
	//     time (measured ~98% of RUNNING samples on a real tree). We recover each
	//     node's SELF cost — self = node − Σ children, per thread state — and emit
	//     the path root→node with those self counts. Summed across the tree the
	//     self counts telescope back to the root total, so emitted RUNNING equals
	//     the analysis total. A node whose self is zero (a pure pass-through that
	//     only forwards to children) contributes no line. Counts are clamped at
	//     zero to absorb sampling jitter that can make a difference slightly
	//     negative (and to skip synthetic roots whose own count is below their
	//     children's).
	//  2. Distinct nodes can share the same stack string — through recursion, or
	//     because --app-only collapses different subtrees onto one application path
	//     once library/agent frames are stripped out of it. We aggregate by path
	//     and sum, so each stack prints exactly once with correct totals instead of
	//     as several partial duplicates.
	//
	// The shape is one stable schema regardless of top/limit: one line per unique
	// stack carrying all five thread-state counters, sorted by RUNNING descending.
	// top only caps how many rows print — it never restructures the output.
	agg := make(map[string]map[string]int)
	var order []string

	var dfs func(id string, stack []string)
	dfs = func(id string, stack []string) {
		n := nodeMap[id]
		if n == nil {
			return
		}
		path := stack
		if n.label != "" && (!appOnly || n.isApp) {
			// Copy before extending: sibling recursions must not alias one
			// backing array, or a later sibling's frame corrupts an earlier path.
			path = append(append(make([]string, 0, len(stack)+1), stack...), n.label)
		}
		self := n.self(nodeMap)
		if len(path) > 0 && sampleTotal(self) > 0 {
			key := strings.Join(path, ";")
			m := agg[key]
			if m == nil {
				m = make(map[string]int, 5)
				agg[key] = m
				order = append(order, key)
			}
			for k, v := range self {
				m[k] += v
			}
		}
		for _, cid := range n.children {
			dfs(cid, path)
		}
	}

	dfs(rootID, nil)
	if len(agg) == 0 {
		return ""
	}

	sort.SliceStable(order, func(i, j int) bool {
		return agg[order[i]]["RUNNING"] > agg[order[j]]["RUNNING"]
	})
	if top > 0 && len(order) > top {
		order = order[:top]
	}

	var sb strings.Builder
	for _, key := range order {
		s := agg[key]
		fmt.Fprintf(&sb, "%s running=%d lock=%d net_io=%d disk_io=%d wait=%d\n",
			key, s["RUNNING"], s["LOCK"], s["NET_IO"], s["DISK_IO"], s["WAIT"],
		)
	}
	return sb.String()
}

// sampleTotal sums a node's per-state sample counts; a node with zero total
// contributes no line to the fold.
func sampleTotal(s map[string]int) int {
	t := 0
	for _, v := range s {
		t += v
	}
	return t
}

// memoryCollapsed folds a memory allocation result into Brendan-Gregg-style leaf
// stacks weighted by bytes, sorted by allocation size:
//
//	frame1;frame2;...;frameN alloc=<bytes> surv=<bytes> alloc_count=<n> surv_count=<n>
//
// It walks the flat call tree in analysisResult.stacktreeNodes (nodes keyed by id,
// linked via childIds, rooted at stacktreeRootIds). Note: analysisResult also carries
// an aggregated apiStacktreeNodes with no children — do NOT use that one.
//
// Orientation gotcha: this tree is INVERTED relative to the hotspots tree — the
// allocation-site method is the ROOT and its callers are the descendants. So the
// emitted path reads allocation-site-first (site;caller;...;outermost), and per-method
// totals live on the roots, not the leaves. Aggregating by leaf here gives Thread.run.
// alloc/surv are allocation pressure and GC-survivors within the window, not live
// retained heap. appOnly keeps only profiler-classified application frames
// (apiInfo.systemApi; see nodeIsApp) in each path; top limits rows.
func memoryCollapsed(raw interface{}, top int, appOnly bool) string {
	envelope, ok := raw.(map[string]interface{})
	if !ok {
		return ""
	}
	result, _ := envelope["result"].(map[string]interface{})
	ar, _ := result["analysisResult"].(map[string]interface{})
	if ar == nil {
		return ""
	}
	nodesRaw, _ := ar["stacktreeNodes"].([]interface{})
	rootIDs, _ := ar["stacktreeRootIds"].([]interface{})
	if len(nodesRaw) == 0 || len(rootIDs) == 0 {
		return ""
	}

	type mnode struct {
		name     string
		isApp    bool
		ai       map[string]interface{}
		children []string
	}
	nodeMap := make(map[string]*mnode, len(nodesRaw))
	for _, r := range nodesRaw {
		nm, ok := r.(map[string]interface{})
		if !ok {
			continue
		}
		md := &mnode{name: strVal(nm, "name"), isApp: nodeIsApp(nm)}
		md.ai, _ = nm["allocationInfo"].(map[string]interface{})
		if cids, ok := nm["childIds"].([]interface{}); ok {
			for _, c := range cids {
				md.children = append(md.children, nodeIDStr(c))
			}
		}
		nodeMap[nodeIDStr(nm["id"])] = md
	}

	type line struct {
		path                  string
		allocSize, survSize   int
		allocCount, survCount int
	}
	var all []line

	var dfs func(id string, stack []string)
	dfs = func(id string, stack []string) {
		n := nodeMap[id]
		if n == nil {
			return
		}
		path := stack
		if n.name != "" && (!appOnly || n.isApp) {
			// A fresh slice, so siblings never share (and overwrite) a backing array.
			path = make([]string, len(stack)+1)
			copy(path, stack)
			path[len(stack)] = n.name
		}
		if len(n.children) == 0 {
			if len(path) == 0 {
				return
			}
			all = append(all, line{
				path:       strings.Join(path, ";"),
				allocSize:  intVal(n.ai, "allocationSize"),
				survSize:   intVal(n.ai, "survivorSize"),
				allocCount: intVal(n.ai, "allocationCount"),
				survCount:  intVal(n.ai, "survivorCount"),
			})
			return
		}
		for _, cid := range n.children {
			dfs(cid, path)
		}
	}
	for _, rid := range rootIDs {
		dfs(nodeIDStr(rid), nil)
	}
	if len(all) == 0 {
		return ""
	}

	sort.SliceStable(all, func(i, j int) bool { return all[i].allocSize > all[j].allocSize })
	if top > 0 && len(all) > top {
		all = all[:top]
	}

	var sb strings.Builder
	sb.WriteString("# ALLOCATIONS (bytes, sorted by alloc; surv = survived >=1 GC in window, not live heap)\n")
	for _, l := range all {
		fmt.Fprintf(&sb, "%s alloc=%d surv=%d alloc_count=%d surv_count=%d\n",
			l.path, l.allocSize, l.survSize, l.allocCount, l.survCount)
	}
	return sb.String()
}
