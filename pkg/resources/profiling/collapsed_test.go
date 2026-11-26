package profiling

import (
	"fmt"
	"strings"
	"testing"
)

// node is a small helper to build a dataNodes entry.
func hnode(id string, cls, method string, app bool, running, lock int, children ...string) map[string]interface{} {
	kids := make([]interface{}, len(children))
	for i, c := range children {
		kids[i] = c
	}
	return map[string]interface{}{
		"id":         id,
		"classPath":  "",
		"className":  cls,
		"methodName": method,
		"apiInfo":    map[string]interface{}{"systemApi": app},
		"samples":    map[string]interface{}{"RUNNING": float64(running), "LOCK": float64(lock)},
		"childIds":   kids,
	}
}

func hotspotsResult(rootID string, nodes ...map[string]interface{}) map[string]interface{} {
	arr := make([]interface{}, len(nodes))
	for i, n := range nodes {
		arr[i] = n
	}
	return map[string]interface{}{
		"dataRootNodeId": rootID,
		"dataNodes":      arr,
	}
}

// toCollapsed is the stack view of a result with one line per row, capped at
// top rows (0 = all), in the form `dtctl exec profiling` prints a table: the
// fold tests read rows more easily as lines than as structs.
func toCollapsed(kind string, raw interface{}, top int, appOnly bool) string {
	var sb strings.Builder
	switch kind {
	case "memoryAllocation":
		rows := capRows(allocationStacks(raw, appOnly, false), top)
		if len(rows) > 0 {
			sb.WriteString("# ALLOCATIONS\n")
		}
		for _, r := range rows {
			fmt.Fprintf(&sb, "%s alloc=%d surv=%d alloc_count=%d surv_count=%d\n", r.Stack, r.AllocBytes, r.SurvivedBytes, r.AllocCount, r.SurvivedCount)
		}
	case "methodHotspots":
		for _, r := range capRows(hotspotStacks(raw, appOnly), top) {
			fmt.Fprintf(&sb, "%s running=%d lock=%d net_io=%d disk_io=%d wait=%d\n", r.Stack, r.Running, r.Lock, r.NetIO, r.DiskIO, r.Wait)
		}
	}
	return sb.String()
}

func capRows[T any](rows []T, top int) []T {
	if top > 0 && len(rows) > top {
		return rows[:top]
	}
	return rows
}

func runOf(t *testing.T, line string) int {
	t.Helper()
	i := strings.Index(line, "running=")
	if i < 0 {
		t.Fatalf("no running= in %q", line)
	}
	rest := line[i+len("running="):]
	if sp := strings.IndexByte(rest, ' '); sp >= 0 {
		rest = rest[:sp]
	}
	n := 0
	for _, c := range rest {
		n = n*10 + int(c-'0')
	}
	return n
}

// API counts are cumulative: A's running=10 already includes B's running=3, so
// A's own (self) cost is 10-3=7. Interior self time must be attributed (a
// leaf-only fold drops it entirely), and because self = node - Σ children the
// emitted RUNNING telescopes back to the root total (10), never double-counts.
func TestToCollapsed_EmitsInteriorSelfSamples(t *testing.T) {
	// root(0, cumulative=10) -> A(cumulative running=10, has child) -> B(leaf, running=3)
	raw := hotspotsResult("0",
		hnode("0", "", "", false, 0, 0, "A"),
		hnode("A", "com.ex.Foo", "a", true, 10, 0, "B"),
		hnode("B", "com.ex.Foo", "b", true, 3, 0),
	)
	out := toCollapsed("methodHotspots", raw, 0, false)

	var total int
	var sawInterior bool
	for _, l := range strings.Split(strings.TrimSpace(out), "\n") {
		if l == "" {
			continue
		}
		total += runOf(t, l)
		// Interior frame A must appear as its own stack carrying its SELF time
		// (10 cumulative - 3 in child B = 7), not its cumulative 10.
		if l == "com.ex.Foo.a running=7 lock=0 net_io=0 disk_io=0 wait=0" {
			sawInterior = true
		}
	}
	if total != 10 {
		t.Fatalf("emitted RUNNING = %d, want 10 (self counts telescope to the root total, no double-count)\n%s", total, out)
	}
	if !sawInterior {
		t.Fatalf("interior frame A's self samples (running=7) were not emitted as their own line:\n%s", out)
	}
}

// --app-only strips non-app frames, which can collapse distinct subtrees onto
// the same application stack. Those must be merged into one line with summed
// counts, not printed as duplicates.
func TestToCollapsed_AppOnlyMergesCollidingStacks(t *testing.T) {
	// root -> App(app) -> Lib1(non-app, running=5), Lib2(non-app, running=7)
	// Under --app-only both children collapse to the single path "App".
	raw := hotspotsResult("0",
		hnode("0", "", "", false, 0, 0, "App"),
		hnode("App", "com.ex.Svc", "handle", true, 0, 0, "L1", "L2"),
		hnode("L1", "jdk.internal", "x", false, 5, 0),
		hnode("L2", "jdk.internal", "y", false, 7, 0),
	)
	out := strings.TrimSpace(toCollapsed("methodHotspots", raw, 0, true))
	lines := strings.Split(out, "\n")
	if len(lines) != 1 {
		t.Fatalf("expected 1 merged line, got %d:\n%s", len(lines), out)
	}
	if got := runOf(t, lines[0]); got != 12 {
		t.Fatalf("merged RUNNING = %d, want 12 (5+7 summed)\n%s", got, out)
	}
	if !strings.HasPrefix(lines[0], "com.ex.Svc.handle running=") {
		t.Fatalf("merged line should be the app path only, got:\n%s", out)
	}
}

// Self is computed per thread state (self = node - Σ children, each state
// independently). An interior node with several children keeps only what it did
// not delegate, and a pure pass-through (all of its cumulative count lives in
// its children) contributes no line at all.
func TestToCollapsed_SelfIsPerStateAndSkipsPassThrough(t *testing.T) {
	// root -> P(run=20,lock=5) -> X(run=8), Y(run=5,lock=2)
	//      -> Q(run=4)         -> Z(run=4)              [Q is a pure pass-through]
	raw := hotspotsResult("0",
		hnode("0", "", "", false, 0, 0, "P", "Q"),
		hnode("P", "com.ex.T", "p", true, 20, 5, "X", "Y"),
		hnode("X", "com.ex.T", "x", true, 8, 0),
		hnode("Y", "com.ex.T", "y", true, 5, 2),
		hnode("Q", "com.ex.T", "q", true, 4, 0, "Z"),
		hnode("Z", "com.ex.T", "z", true, 4, 0),
	)
	out := strings.TrimSpace(toCollapsed("methodHotspots", raw, 0, false))

	var total int
	byLine := map[string]bool{}
	for _, l := range strings.Split(out, "\n") {
		total += runOf(t, l)
		byLine[l] = true
	}
	// P kept run=20-(8+5)=7, lock=5-(0+2)=3.
	if !byLine["com.ex.T.p running=7 lock=3 net_io=0 disk_io=0 wait=0"] {
		t.Fatalf("P self must be per-state (run=7, lock=3):\n%s", out)
	}
	// Q delegated everything (4-4=0) — it must not appear as its own stack.
	if byLine["com.ex.T.q running=0 lock=0 net_io=0 disk_io=0 wait=0"] {
		t.Fatalf("pure pass-through Q must not emit a line:\n%s", out)
	}
	// Emitted RUNNING telescopes to the true total (P 20 + Q 4 = 24).
	if total != 24 {
		t.Fatalf("emitted RUNNING = %d, want 24\n%s", total, out)
	}
}

// A child reporting more of a state than its parent (sampling jitter) must clamp
// that state's self to zero, never emit a negative count.
func TestToCollapsed_ClampsNegativeSelf(t *testing.T) {
	// A: run=3, lock=0 with child B: run=1, lock=5 -> A self lock = 0-5, clamp 0.
	raw := hotspotsResult("0",
		hnode("0", "", "", false, 0, 0, "A"),
		hnode("A", "com.ex.C", "a", true, 3, 0, "B"),
		hnode("B", "com.ex.C", "b", true, 1, 5),
	)
	out := strings.TrimSpace(toCollapsed("methodHotspots", raw, 0, false))
	if !strings.Contains(out, "com.ex.C.a running=2 lock=0 net_io=0 disk_io=0 wait=0") {
		t.Fatalf("A self must clamp negative lock to 0 (run=2, lock=0):\n%s", out)
	}
	if strings.Contains(out, "lock=-") {
		t.Fatalf("no negative counts allowed:\n%s", out)
	}
}

// The schema is one line per unique stack regardless of top; top only caps
// rows and never introduces per-state section headers.
func TestToCollapsed_TopCapsRowsNoSections(t *testing.T) {
	raw := hotspotsResult("0",
		hnode("0", "", "", false, 0, 0, "A", "B", "C"),
		hnode("A", "com.ex.C", "a", true, 9, 0),
		hnode("B", "com.ex.C", "b", true, 5, 1),
		hnode("C", "com.ex.C", "c", true, 1, 0),
	)
	out := strings.TrimSpace(toCollapsed("methodHotspots", raw, 2, false))
	lines := strings.Split(out, "\n")
	if len(lines) != 2 {
		t.Fatalf("top=2 should emit 2 rows, got %d:\n%s", len(lines), out)
	}
	for _, l := range lines {
		if strings.HasPrefix(l, "#") {
			t.Fatalf("output must not contain section headers:\n%s", out)
		}
	}
	// Sorted by RUNNING desc: A(9) then B(5).
	if !strings.HasPrefix(lines[0], "com.ex.C.a running=9") {
		t.Fatalf("rows not sorted by RUNNING desc:\n%s", out)
	}
}

// jsnode builds a Node.js frame the way the API returns it: no class, the function
// in methodName, and the file in fileName/filePath (the shape the API records).
func jsnode(id, method, filePath, fileName, ruleID string, running int, children ...string) map[string]interface{} {
	kids := make([]interface{}, len(children))
	for i, c := range children {
		kids[i] = c
	}
	return map[string]interface{}{
		"id":         id,
		"classPath":  "",
		"className":  "",
		"methodName": method,
		"fileName":   fileName,
		"filePath":   filePath,
		"apiInfo":    map[string]interface{}{"id": ruleID, "systemApi": true},
		"samples":    map[string]interface{}{"RUNNING": float64(running)},
		"childIds":   kids,
	}
}

// Node.js frames have an empty className. The renderer used to label them "" and
// skip them, so a Node.js profile printed "no stacks" while -o json had thousands
// of nodes. They must render as file:function.
func TestToCollapsed_NodeJSFramesRender(t *testing.T) {
	raw := hotspotsResult("0",
		hnode("0", "", "", false, 0, 0, "1"),
		jsnode("1", "onconnection", "", "node:net", "default-rule-id-nodejs", 10, "2"),
		jsnode("2", "Socket", "", "node:net", "default-rule-id-nodejs", 10),
	)
	out := toCollapsed("methodHotspots", raw, 0, false)
	want := "node:net:onconnection;node:net:Socket running=10 lock=0 net_io=0 disk_io=0 wait=0\n"
	if out != want {
		t.Fatalf("got %q, want %q", out, want)
	}
}

func TestNodeLabel_NodeJSFiles(t *testing.T) {
	cases := []struct{ path, file, want string }{
		{"node:internal/timers", "", "node:internal/timers:f"},
		{"node:internal/modules/cjs", "loader", "node:internal/modules/cjs/loader:f"},
		{"", "node:fs", "node:fs:f"},
		{"/app/node_modules/graphql/type", "definition.js", "graphql/type/definition.js:f"},
		{"/app/node_modules/@grpc/proto-loader/node_modules/protobufjs/src", "type.js", "protobufjs/src/type.js:f"},
		{"/app/build", "server.js", "/app/build/server.js:f"},
		{"", "", "f"},
	}
	for _, c := range cases {
		if got := nodeLabel(jsnode("x", "f", c.path, c.file, "", 0)); got != c.want {
			t.Errorf("path=%q file=%q: got %q, want %q", c.path, c.file, got, c.want)
		}
	}
}

// --app-only promises to strip OneAgent noise, but the API marks the agent's Java
// frames as the "Java" default rule (systemApi=true), like app code. On
// featureflagservice 18 of 26 app-only stacks had com.dynatrace.agent frames.
func TestToCollapsed_AppOnlyDropsAgentFrames(t *testing.T) {
	agent := func(id, cp, cls string, running int, children ...string) map[string]interface{} {
		n := hnode(id, cls, "m", true, running, 0, children...)
		n["classPath"] = cp
		return n
	}
	raw := hotspotsResult("0",
		hnode("0", "", "", false, 0, 0, "A"),
		agent("A", "com.dynatrace.easytrade.flags", "FlagController", 10, "B", "C"),
		agent("B", "com.dynatrace.agent.shaded.uem.impl", "AgentInjectingResponseWrapper", 4),
		agent("C", "jakarta.servlet.http", "DynatraceJakartaUemIntrospection", 3),
	)
	out := toCollapsed("methodHotspots", raw, 0, true)
	if strings.Contains(out, "agent") || strings.Contains(out, "Introspection") {
		t.Fatalf("--app-only kept OneAgent frames:\n%s", out)
	}
	// The app frame stays, and the agent frames' samples fold into it.
	want := "com.dynatrace.easytrade.flags.FlagController.m running=10 lock=0 net_io=0 disk_io=0 wait=0\n"
	if out != want {
		t.Fatalf("got %q, want %q", out, want)
	}
}

// On Node.js every frame is systemApi=true, so --app-only used to keep the
// runtime and every npm package. It keeps only the app's own files now.
func TestToCollapsed_AppOnlyNodeJSKeepsOwnFiles(t *testing.T) {
	raw := hotspotsResult("0",
		hnode("0", "", "", false, 0, 0, "1"),
		jsnode("1", "processTicksAndRejections", "node:internal/process", "task_queues", "default-rule-id-nodejs", 10, "2"),
		jsnode("2", "handler", "/app/build", "server.js", "default-rule-id-nodejs", 10, "3"),
		jsnode("3", "execute", "/app/node_modules/graphql/execution", "execute.js", "FilePathBasedApiRuleId-graphql", 6),
	)
	out := toCollapsed("methodHotspots", raw, 0, true)
	want := "/app/build/server.js:handler running=10 lock=0 net_io=0 disk_io=0 wait=0\n"
	if out != want {
		t.Fatalf("got %q, want %q", out, want)
	}
}

// memnode is a memory-tree node: the tree is inverted (the allocation site is
// the root, its callers below it) and allocationInfo is cumulative.
func memnode(id float64, name string, app bool, alloc, count float64, children ...interface{}) map[string]interface{} {
	return map[string]interface{}{
		"id": id, "name": name, "childIds": children,
		"apiInfo":        map[string]interface{}{"systemApi": app},
		"allocationInfo": map[string]interface{}{"allocationSize": alloc, "allocationCount": count},
	}
}

func allocationsResult(roots []interface{}, nodes ...interface{}) map[string]interface{} {
	return map[string]interface{}{"analysisResult": map[string]interface{}{
		"stacktreeRootIds": roots, "stacktreeNodes": nodes,
	}}
}

// Values live on every level: Site.alloc (1000) is reached through Svc.get
// (700, of which 100 is allocated with Svc.get as the outermost frame) and
// Job.run (300). Each line carries its own share, so the lines add up to the
// root's total, and the interior share is not lost.
func TestMemoryCollapsed_ValuesOnRootsAndDescendants(t *testing.T) {
	raw := allocationsResult([]interface{}{float64(1)},
		memnode(1, "Site.alloc", true, 1000, 10, float64(2), float64(3)),
		memnode(2, "Svc.get", true, 700, 7, float64(4)),
		memnode(3, "Job.run", true, 300, 3),
		memnode(4, "Thread.run", false, 600, 6),
	)
	out := toCollapsed("memoryAllocation", raw, 0, false)
	for _, want := range []string{
		"Site.alloc;Svc.get;Thread.run alloc=600 ",
		"Site.alloc;Job.run alloc=300 ",
		"Site.alloc;Svc.get alloc=100 ",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	sum := 0
	for _, l := range strings.Split(out, "\n") {
		if i := strings.Index(l, " alloc="); i >= 0 {
			var n int
			_, _ = fmt.Sscanf(l[i:], " alloc=%d", &n)
			sum += n
		}
	}
	if sum != 1000 {
		t.Errorf("lines add up to %d, want the root's 1000:\n%s", sum, out)
	}
}

// --app-only drops Thread.run, so its line and Svc.get's own share land on the
// same path and are merged into one line.
func TestMemoryCollapsed_AppOnlyMergesCollidingPaths(t *testing.T) {
	raw := allocationsResult([]interface{}{float64(1)},
		memnode(1, "Site.alloc", true, 1000, 10, float64(2), float64(3)),
		memnode(2, "Svc.get", true, 700, 7, float64(4)),
		memnode(3, "Job.run", true, 300, 3),
		memnode(4, "Thread.run", false, 600, 6),
	)
	out := toCollapsed("memoryAllocation", raw, 0, true)
	if n := strings.Count(out, "Site.alloc;Svc.get "); n != 1 {
		t.Errorf("Site.alloc;Svc.get printed %d times, want once:\n%s", n, out)
	}
	if !strings.Contains(out, "Site.alloc;Svc.get alloc=700 ") {
		t.Errorf("want the merged 600+100:\n%s", out)
	}
}

// --survivors-only ranks the rows by survived bytes, not allocated bytes.
func TestAllocationStacks_BySurvivors(t *testing.T) {
	churn := memnode(1, "Churn.alloc", true, 1000, 10)
	keep := memnode(2, "Keep.alloc", true, 100, 1)
	churn["allocationInfo"].(map[string]interface{})["survivorSize"] = float64(10)
	keep["allocationInfo"].(map[string]interface{})["survivorSize"] = float64(90)
	raw := allocationsResult([]interface{}{float64(1), float64(2)}, churn, keep)

	if rows := allocationStacks(raw, false, false); rows[0].Stack != "Churn.alloc" {
		t.Errorf("by allocated bytes, Churn.alloc first: %+v", rows)
	}
	rows := allocationStacks(raw, false, true)
	if rows[0].Stack != "Keep.alloc" || rows[0].SurvivedBytes != 90 {
		t.Errorf("by survived bytes, Keep.alloc first: %+v", rows)
	}
}

// Survived bytes are summed per GC survived, so a long-lived frame's survivors
// can exceed its allocations. The rows pass that through.
func TestAllocationStacks_SurvivedExceedsAllocated(t *testing.T) {
	cache := memnode(1, "Cache.put", true, 100, 1)
	cache["allocationInfo"].(map[string]interface{})["survivorSize"] = float64(500)
	raw := allocationsResult([]interface{}{float64(1)}, cache)

	rows := allocationStacks(raw, false, true)
	if len(rows) != 1 || rows[0].AllocBytes != 100 || rows[0].SurvivedBytes != 500 {
		t.Fatalf("want alloc 100, survived 500 as the API gives them: %+v", rows)
	}
}
