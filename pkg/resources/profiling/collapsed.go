package profiling

import (
	"slices"
	"sort"
	"strings"
)

// StackRow is one row of the hotspots stack view: a unique call stack with its
// own (self) samples in each thread state.
type StackRow struct {
	Running int    `json:"running" yaml:"running" table:"RUNNING"`
	Lock    int    `json:"lock" yaml:"lock" table:"LOCK"`
	NetIO   int    `json:"net_io" yaml:"net_io" table:"NET_IO"`
	DiskIO  int    `json:"disk_io" yaml:"disk_io" table:"DISK_IO"`
	Wait    int    `json:"wait" yaml:"wait" table:"WAIT"`
	Stack   string `json:"stack" yaml:"stack" table:"STACK"`
}

// AllocationRow is one row of the allocations stack view: an allocation path,
// allocation site first, with its own share of the window's allocations.
// SurvivedBytes and SurvivedCount are summed over every GC an object lived
// through until the agent's next report, about a minute (10 bytes surviving 3
// GCs are 30): they are not sizes, can exceed the allocated figures, and say
// nothing about the heap in use.
// The figures are int64, as the view's totals are: a byte count outgrows an
// int on a 32-bit platform.
type AllocationRow struct {
	AllocBytes    int64  `json:"alloc_bytes" yaml:"alloc_bytes" table:"ALLOC_BYTES"`
	SurvivedBytes int64  `json:"survived_bytes" yaml:"survived_bytes" table:"SURVIVED_BYTES"`
	AllocCount    int64  `json:"alloc_count" yaml:"alloc_count" table:"ALLOC_COUNT"`
	SurvivedCount int64  `json:"survived_count" yaml:"survived_count" table:"SURVIVED_COUNT"`
	Stack         string `json:"stack" yaml:"stack" table:"STACK"`
}

// hotspotStacks folds a hotspots result into one row per unique stack, sorted
// by running samples, descending. appOnly strips frames the profiler did not
// classify as application code (apiInfo.systemApi) from each path; see
// nodeIsApp. Returns nil for missing data.
func hotspotStacks(raw interface{}, appOnly bool) []StackRow {
	nodeMap, rootID := hotspotTree(raw)
	if nodeMap == nil {
		return nil
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
	agg := make(map[string]map[string]int)
	var order []string

	visited := make(map[string]bool, len(nodeMap))
	var dfs func(id string, stack []string)
	dfs = func(id string, stack []string) {
		n := nodeMap[id]
		if n == nil || visited[id] {
			// visited: the API's childIds form a tree, so a node reached twice
			// means a malformed result (a cycle would recurse until the
			// goroutine's stack overflows, which no recover catches).
			return
		}
		visited[id] = true
		path := stack
		if n.label != "" && (!appOnly || n.isApp) {
			// Clip before extending: append then copies, so sibling recursions
			// never alias one backing array (a later sibling's frame would
			// corrupt an earlier path).
			path = append(slices.Clip(stack), n.label)
		}
		self := n.self(nodeMap)
		if len(path) > 0 && hasSamples(self) {
			key := strings.Join(path, ";")
			m := agg[key]
			if m == nil {
				m = make(map[string]int, 5)
				agg[key] = m
				order = append(order, key)
			}
			for k, v := range self {
				m[k] = satAddInt(m[k], v)
			}
		}
		for _, cid := range n.children {
			dfs(cid, path)
		}
	}

	dfs(rootID, nil)
	if len(agg) == 0 {
		return nil
	}

	sort.SliceStable(order, func(i, j int) bool {
		return agg[order[i]]["RUNNING"] > agg[order[j]]["RUNNING"]
	})
	rows := make([]StackRow, len(order))
	for i, key := range order {
		c := agg[key]
		rows[i] = StackRow{Running: c["RUNNING"], Lock: c["LOCK"], NetIO: c["NET_IO"], DiskIO: c["DISK_IO"], Wait: c["WAIT"], Stack: key}
	}
	return rows
}

// hasSamples reports whether a node has samples of its own in any thread
// state; a node with none contributes no line to the fold.
func hasSamples(s map[string]int) bool {
	for _, v := range s {
		if v > 0 {
			return true
		}
	}
	return false
}

// allocationStacks folds a memory allocation result into one row per unique
// allocation path, weighted by bytes and sorted by allocated bytes (by survived
// bytes when bySurvivors), descending.
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
// (apiInfo.systemApi; see nodeIsApp) in each path. Returns nil for missing data.
func allocationStacks(raw interface{}, appOnly, bySurvivors bool) []AllocationRow {
	result, _ := raw.(map[string]interface{})
	ar, _ := result["analysisResult"].(map[string]interface{})
	if ar == nil {
		return nil
	}
	nodesRaw, _ := ar["stacktreeNodes"].([]interface{})
	rootIDs, _ := ar["stacktreeRootIds"].([]interface{})
	if len(nodesRaw) == 0 || len(rootIDs) == 0 {
		return nil
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
				if cid, ok := nodeIDStr(c); ok {
					md.children = append(md.children, cid)
				}
			}
		}
		if id, ok := nodeIDStr(nm["id"]); ok {
			nodeMap[id] = md
		}
	}

	type line struct {
		path                  string
		allocSize, survSize   int64
		allocCount, survCount int64
	}
	var all []*line
	byPath := map[string]*line{}

	// The values are cumulative: a node holds its whole subtree, so a root is
	// the allocation site's total. Each node therefore contributes its own
	// share, node minus children, clamped at zero, the same way the hotspots
	// fold does. Most of a tree's bytes sit on its leaves (one line per full
	// path), but an interior node can allocate in its own frame too, and a
	// leaf-only fold would drop that. Summed over the tree, the shares add
	// back up to the roots. Paths that coincide (recursion, or --app-only
	// dropping the frames that told them apart) are merged into one line.
	selfOf := func(n *mnode, key string) int64 {
		v := intVal(n.ai, key)
		for _, cid := range n.children {
			if c := nodeMap[cid]; c != nil {
				v = max(v-intVal(c.ai, key), 0) // at every step, as treeNode.self
			}
		}
		return v
	}

	visited := make(map[string]bool, len(nodeMap))
	var dfs func(id string, stack []string)
	dfs = func(id string, stack []string) {
		n := nodeMap[id]
		if n == nil || visited[id] {
			return // a node reached twice: malformed result, see hotspotStacks
		}
		visited[id] = true
		path := stack
		if n.name != "" && (!appOnly || n.isApp) {
			// Clipped, so append copies and siblings never share a backing array.
			path = append(slices.Clip(stack), n.name)
		}
		if len(path) > 0 {
			self := line{
				allocSize:  selfOf(n, "allocationSize"),
				survSize:   selfOf(n, "survivorSize"),
				allocCount: selfOf(n, "allocationCount"),
				survCount:  selfOf(n, "survivorCount"),
			}
			if self.allocSize > 0 || self.allocCount > 0 || self.survSize > 0 || self.survCount > 0 {
				key := strings.Join(path, ";")
				l := byPath[key]
				if l == nil {
					l = &line{path: key}
					byPath[key] = l
					all = append(all, l)
				}
				l.allocSize = satAdd(l.allocSize, self.allocSize)
				l.survSize = satAdd(l.survSize, self.survSize)
				l.allocCount = satAdd(l.allocCount, self.allocCount)
				l.survCount = satAdd(l.survCount, self.survCount)
			}
		}
		for _, cid := range n.children {
			dfs(cid, path)
		}
	}
	for _, rid := range rootIDs {
		if id, ok := nodeIDStr(rid); ok {
			dfs(id, nil)
		}
	}
	if len(all) == 0 {
		return nil
	}

	sort.SliceStable(all, func(i, j int) bool {
		if bySurvivors {
			return all[i].survSize > all[j].survSize
		}
		return all[i].allocSize > all[j].allocSize
	})
	rows := make([]AllocationRow, len(all))
	for i, l := range all {
		rows[i] = AllocationRow{AllocBytes: l.allocSize, SurvivedBytes: l.survSize, AllocCount: l.allocCount, SurvivedCount: l.survCount, Stack: l.path}
	}
	return rows
}
