package profiling

import (
	"fmt"
	"slices"
	"sort"
	"strings"
)

// StackRow is one row of the hotspots stack view: a unique call stack with its
// own (self) samples in each thread state.
type StackRow struct {
	Running int    `json:"running" table:"RUNNING"`
	Lock    int    `json:"lock" table:"LOCK"`
	NetIO   int    `json:"net_io" table:"NET_IO"`
	DiskIO  int    `json:"disk_io" table:"DISK_IO"`
	Wait    int    `json:"wait" table:"WAIT"`
	Stack   string `json:"stack" table:"STACK"`
}

// SurvivedNote says what the survived figures are, for every output that
// prints them: the API sums them over each GC an object lives through, so
// they read as a size (and as heap held) when they are not one.
const SurvivedNote = "survived_bytes/survived_count are summed over every GC an object lived through within about a minute (10 bytes surviving 3 GCs = 30): not a size, can exceed alloc_bytes, not heap used or retained; survived/allocated ~1 = dies young, higher tracks GC frequency, not a leak signal alone"

// AllocationRow is one row of the allocations stack view: an allocation path,
// allocation site first, with its own share of the window's allocations.
// SurvivedBytes and SurvivedCount are summed per GC survived (SurvivedNote).
type AllocationRow struct {
	AllocBytes    int    `json:"alloc_bytes" table:"ALLOC_BYTES"`
	SurvivedBytes int    `json:"survived_bytes" table:"SURVIVED_BYTES"`
	AllocCount    int    `json:"alloc_count" table:"ALLOC_COUNT"`
	SurvivedCount int    `json:"survived_count" table:"SURVIVED_COUNT"`
	Stack         string `json:"stack" table:"STACK"`
}

// FoldStacks renders stack rows as collapsed (folded) stacks, one line per row:
// `frame1;frame2;...;frameN running=.. lock=.. net_io=.. disk_io=.. wait=..`.
// Unlike single-metric folded output a line keeps all five thread-state
// counts, so one read shows whether a stack is CPU-bound or blocked without a
// separate flamegraph per state.
func FoldStacks(rows []StackRow) string {
	var sb strings.Builder
	for _, r := range rows {
		fmt.Fprintf(&sb, "%s running=%d lock=%d net_io=%d disk_io=%d wait=%d\n",
			r.Stack, r.Running, r.Lock, r.NetIO, r.DiskIO, r.Wait)
	}
	return sb.String()
}

// FoldAllocations renders allocation rows as folded lines weighted by bytes,
// under a header that names the sort key.
func FoldAllocations(rows []AllocationRow, bySurvivors bool) string {
	if len(rows) == 0 {
		return ""
	}
	key := "alloc"
	if bySurvivors {
		key = "surv"
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "# ALLOCATIONS (bytes, sorted by %s; surv = bytes x GCs survived: not a size, can exceed alloc, not live heap)\n", key)
	for _, l := range rows {
		fmt.Fprintf(&sb, "%s alloc=%d surv=%d alloc_count=%d surv_count=%d\n",
			l.Stack, l.AllocBytes, l.SurvivedBytes, l.AllocCount, l.SurvivedCount)
	}
	return sb.String()
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

// sampleTotal sums a node's per-state sample counts; a node with zero total
// contributes no line to the fold.
func sampleTotal(s map[string]int) int {
	t := 0
	for _, v := range s {
		t += v
	}
	return t
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
	selfOf := func(n *mnode, key string) int {
		v := intVal(n.ai, key)
		for _, cid := range n.children {
			if c := nodeMap[cid]; c != nil {
				v -= intVal(c.ai, key)
			}
		}
		return max(v, 0)
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
				l.allocSize = satAddInt(l.allocSize, self.allocSize)
				l.survSize = satAddInt(l.survSize, self.survSize)
				l.allocCount = satAddInt(l.allocCount, self.allocCount)
				l.survCount = satAddInt(l.survCount, self.survCount)
			}
		}
		for _, cid := range n.children {
			dfs(cid, path)
		}
	}
	for _, rid := range rootIDs {
		dfs(nodeIDStr(rid), nil)
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
