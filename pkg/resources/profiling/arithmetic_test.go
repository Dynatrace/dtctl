package profiling

import (
	"math"
	"reflect"
	"testing"
)

// Counts no profile could hold still have to add up: a result is a document
// from the network, and a view that wraps around reports a busy process as an
// idle one, or samples on a frame that has none. Every test here reads one
// such document through the stack, function and focus views, which must agree.

// big is more than half of the largest count, so two of them do not fit.
const big = math.MaxInt/2 + 1

// snode is a hotspots frame with any sample counts and child ids.
func snode(id interface{}, cls, method string, app bool, samples map[string]interface{}, children ...interface{}) map[string]interface{} {
	if children == nil {
		children = []interface{}{}
	}
	return map[string]interface{}{
		"id":         id,
		"classPath":  "",
		"className":  cls,
		"methodName": method,
		"apiInfo":    map[string]interface{}{"systemApi": app},
		"samples":    samples,
		"childIds":   children,
	}
}

func counts(kv ...interface{}) map[string]interface{} {
	m := map[string]interface{}{}
	for i := 0; i < len(kv); i += 2 {
		m[kv[i].(string)] = kv[i+1]
	}
	return m
}

func mustFunctions(t *testing.T, h *Hotspots, appOnly bool, state State) FunctionView {
	t.Helper()
	v, err := h.Functions(appOnly, state, SortBySelf)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func mustFocus(t *testing.T, h *Hotspots, appOnly bool, state State, pattern string) FocusView {
	t.Helper()
	v, err := h.Focus(appOnly, state, pattern)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

// Two states of one frame that together exceed the largest count: the frame
// has samples, in every view.
func TestCounts_TwoStatesOverTheLimit(t *testing.T) {
	h := hotspotsOf(hotspotsResult("0",
		snode("0", "", "", false, counts("RUNNING", float64(big), "LOCK", float64(big)), "A"),
		snode("A", "C", "a", true, counts("RUNNING", float64(big), "LOCK", float64(big))),
	))

	wantStacks := StackView{Samples: big, Rows: []StackRow{{Running: big, Lock: big, Stack: "C.a"}}}
	if got := h.Stacks(false); !reflect.DeepEqual(got, wantStacks) {
		t.Errorf("Stacks(false) = %+v\nwant %+v", got, wantStacks)
	}
	wantFns := FunctionView{Samples: math.MaxInt, Rows: []FunctionRow{
		{Self: math.MaxInt, SelfPct: 100, Total: math.MaxInt, TotalPct: 100, Paths: 1, Function: "C.a"},
	}}
	if got := mustFunctions(t, h, false, StateAll); !reflect.DeepEqual(got, wantFns) {
		t.Errorf("Functions(false, all) = %+v\nwant %+v", got, wantFns)
	}
	wantFocus := FocusView{Function: "C.a", Total: math.MaxInt, Self: math.MaxInt, Callers: []FocusRow{
		{Relation: "caller", Samples: math.MaxInt, Share: 100, Function: RootCaller},
	}}
	if got := mustFocus(t, h, false, StateAll, "C.a"); !reflect.DeepEqual(got, wantFocus) {
		t.Errorf("Focus(false, all, C.a) = %+v\nwant %+v", got, wantFocus)
	}
}

// A recursive frame whose outer call is past the limit in two states and whose
// inner call waits: a view read without appOnly has rows, and so no reason to
// be empty, least of all appOnly.
func TestCounts_UnfilteredViewIsNeverEmptiedByAppOnly(t *testing.T) {
	h := hotspotsOf(hotspotsResult("0",
		snode("0", "", "", false, counts(), "A"),
		snode("A", "app", "f", true, counts("RUNNING", 1e300, "LOCK", 1e300, "WAIT", float64(0)), "B"),
		snode("B", "app", "f", true, counts("WAIT", float64(2))),
	))

	wantStacks := StackView{Samples: math.MaxInt, Rows: []StackRow{
		{Running: math.MaxInt, Lock: math.MaxInt, Stack: "app.f"},
		{Wait: 2, Stack: "app.f;app.f"},
	}}
	if got := h.Stacks(false); !reflect.DeepEqual(got, wantStacks) {
		t.Errorf("Stacks(false) = %+v\nwant %+v", got, wantStacks)
	}
	wantFns := FunctionView{Samples: math.MaxInt, Rows: []FunctionRow{
		{Self: math.MaxInt, SelfPct: 100, Total: math.MaxInt, TotalPct: 100, Paths: 2, Function: "app.f"},
	}}
	if got := mustFunctions(t, h, false, StateAll); !reflect.DeepEqual(got, wantFns) {
		t.Errorf("Functions(false, all) = %+v\nwant %+v", got, wantFns)
	}
	if got := mustFocus(t, h, false, StateAll, "app.f"); got.Total != math.MaxInt || got.Self != math.MaxInt || got.Empty != "" {
		t.Errorf("Focus(false, all, app.f) = %+v, want a total and self of %d", got, math.MaxInt)
	}

	// With no samples in a state, an unfiltered view says so: appOnly cannot
	// be the reason for a view that was read without it.
	if got := mustFunctions(t, h, false, StateNetIO); len(got.Rows) != 0 || got.Empty != EmptyState {
		t.Errorf("Functions(false, net_io) = %+v, want no rows and %q", got, EmptyState)
	}
}

// Four frames that together wrap around to exactly zero.
func TestCounts_SumWrappingToZero(t *testing.T) {
	h := hotspotsOf(hotspotsResult("0",
		snode("0", "", "", false, counts("RUNNING", 1e300), "A", "B", "C", "D"),
		snode("A", "C", "a", true, counts("RUNNING", float64(big))),
		snode("B", "C", "b", true, counts("RUNNING", float64(big))),
		snode("C", "C", "c", true, counts("RUNNING", float64(big))),
		snode("D", "C", "d", true, counts("RUNNING", float64(big))),
	))
	if got := h.Stacks(false); got.Samples != math.MaxInt || len(got.Rows) != 4 {
		t.Errorf("Stacks(false) = %+v, want 4 rows and %d samples", got, math.MaxInt)
	}
	want := FunctionView{Samples: math.MaxInt, Rows: []FunctionRow{
		{Self: big, SelfPct: 50, Total: big, TotalPct: 50, Paths: 1, Function: "C.a"},
		{Self: big, SelfPct: 50, Total: big, TotalPct: 50, Paths: 1, Function: "C.b"},
		{Self: big, SelfPct: 50, Total: big, TotalPct: 50, Paths: 1, Function: "C.c"},
		{Self: big, SelfPct: 50, Total: big, TotalPct: 50, Paths: 1, Function: "C.d"},
	}}
	if got := mustFunctions(t, h, false, StateRunning); !reflect.DeepEqual(got, want) {
		t.Errorf("Functions(false, running) = %+v\nwant %+v", got, want)
	}
}

// A negative count is no count. It must not turn into samples on the frame
// above, whose own time is its count minus its children's.
func TestCounts_NegativeChild(t *testing.T) {
	h := hotspotsOf(hotspotsResult("0",
		snode("0", "C", "p", true, counts("RUNNING", float64(0)), "A"),
		snode("A", "C", "c", true, counts("RUNNING", float64(-5))),
	))
	if got := h.Stacks(false); len(got.Rows) != 0 || got.Samples != 0 || got.Empty != EmptyNoSamples {
		t.Errorf("Stacks(false) = %+v, want no rows and %q", got, EmptyNoSamples)
	}
	if got := mustFunctions(t, h, false, StateRunning); len(got.Rows) != 0 || got.Samples != 0 || got.Empty != EmptyNoSamples {
		t.Errorf("Functions(false, running) = %+v, want no rows and %q", got, EmptyNoSamples)
	}
	if got := mustFocus(t, h, false, StateRunning, "C.p"); got.Function != "" || got.Empty != EmptyNoSamples {
		t.Errorf("Focus(false, running, C.p) = %+v, want no function and %q", got, EmptyNoSamples)
	}

	a := allocationsOf(allocationsResult([]interface{}{float64(1)},
		map[string]interface{}{"id": float64(1), "name": "A.a", "childIds": []interface{}{float64(2)},
			"allocationInfo": map[string]interface{}{"allocationSize": float64(0), "survivorSize": float64(0)}},
		map[string]interface{}{"id": float64(2), "name": "B.b", "childIds": []interface{}{},
			"allocationInfo": map[string]interface{}{"allocationSize": float64(-5), "survivorSize": float64(-5)}},
	))
	if got := a.Stacks(false, false); len(got.Rows) != 0 || got.AllocatedBytes != 0 || got.SurvivedBytes != 0 || got.Empty != EmptyNoSamples {
		t.Errorf("allocation Stacks = %+v, want no rows and %q", got, EmptyNoSamples)
	}
}

// A frame's own time is its count minus its children's, at least zero. Children
// that together exceed the largest count must not wrap that around into time
// of its own.
func TestCounts_ChildrenOverTheLimit(t *testing.T) {
	h := hotspotsOf(hotspotsResult("0",
		snode("0", "C", "p", true, counts("RUNNING", float64(0)), "A", "B"),
		snode("A", "C", "x", true, counts("RUNNING", float64(big))),
		snode("B", "C", "y", true, counts("RUNNING", float64(big))),
	))
	wantStacks := StackView{Samples: math.MaxInt, Rows: []StackRow{
		{Running: big, Stack: "C.p;C.x"},
		{Running: big, Stack: "C.p;C.y"},
	}}
	if got := h.Stacks(false); !reflect.DeepEqual(got, wantStacks) {
		t.Errorf("Stacks(false) = %+v\nwant %+v", got, wantStacks)
	}
	wantFns := FunctionView{Samples: math.MaxInt, Rows: []FunctionRow{
		{Self: big, SelfPct: 50, Total: big, TotalPct: 50, Paths: 1, Function: "C.x"},
		{Self: big, SelfPct: 50, Total: big, TotalPct: 50, Paths: 1, Function: "C.y"},
		{Self: 0, SelfPct: 0, Total: math.MaxInt, TotalPct: 100, Paths: 1, Function: "C.p"},
	}}
	if got := mustFunctions(t, h, false, StateRunning); !reflect.DeepEqual(got, wantFns) {
		t.Errorf("Functions(false, running) = %+v\nwant %+v", got, wantFns)
	}
	wantFocus := FocusView{Function: "C.p", Total: math.MaxInt, Self: 0,
		Callers: []FocusRow{{Relation: "caller", Samples: math.MaxInt, Share: 100, Function: RootCaller}},
		Callees: []FocusRow{
			{Relation: "callee", Samples: big, Share: 50, Function: "C.x"},
			{Relation: "callee", Samples: big, Share: 50, Function: "C.y"},
		}}
	if got := mustFocus(t, h, false, StateRunning, "C.p"); !reflect.DeepEqual(got, wantFocus) {
		t.Errorf("Focus(false, running, C.p) = %+v\nwant %+v", got, wantFocus)
	}

	// The same shape in an allocation tree.
	a := allocationsOf(allocationsResult([]interface{}{float64(1)},
		map[string]interface{}{"id": float64(1), "name": "A.a", "apiInfo": map[string]interface{}{"systemApi": true},
			"childIds": []interface{}{float64(2), float64(3)}, "allocationInfo": map[string]interface{}{"allocationSize": float64(0)}},
		map[string]interface{}{"id": float64(2), "name": "B.x", "apiInfo": map[string]interface{}{"systemApi": true},
			"childIds": []interface{}{}, "allocationInfo": map[string]interface{}{"allocationSize": 6e18}},
		map[string]interface{}{"id": float64(3), "name": "B.y", "apiInfo": map[string]interface{}{"systemApi": true},
			"childIds": []interface{}{}, "allocationInfo": map[string]interface{}{"allocationSize": 6e18}},
	))
	got := a.Stacks(false, false)
	if len(got.Rows) != 2 || got.Rows[0].Stack != "A.a;B.x" || got.Rows[1].Stack != "A.a;B.y" {
		t.Errorf("allocation rows = %+v, want A.a;B.x and A.a;B.y and none for A.a alone", got.Rows)
	}
}

// Two library frames that appOnly folds into the application frame above
// them, together over the limit: the merged row holds the largest count.
func TestCounts_MergedRowOverTheLimit(t *testing.T) {
	h := hotspotsOf(hotspotsResult("0",
		snode("0", "C", "a", true, counts("RUNNING", 1e300), "A", "B"),
		snode("A", "lib", "x", false, counts("RUNNING", float64(big))),
		snode("B", "lib", "y", false, counts("RUNNING", float64(big))),
	))
	wantStacks := StackView{Samples: math.MaxInt, Rows: []StackRow{{Running: math.MaxInt, Stack: "C.a"}}}
	if got := h.Stacks(true); !reflect.DeepEqual(got, wantStacks) {
		t.Errorf("Stacks(true) = %+v\nwant %+v", got, wantStacks)
	}
	wantFns := FunctionView{Samples: math.MaxInt, Rows: []FunctionRow{
		{Self: math.MaxInt, SelfPct: 100, Total: math.MaxInt, TotalPct: 100, Paths: 1, Function: "C.a"},
	}}
	if got := mustFunctions(t, h, true, StateRunning); !reflect.DeepEqual(got, wantFns) {
		t.Errorf("Functions(true, running) = %+v\nwant %+v", got, wantFns)
	}
	if got := mustFocus(t, h, true, StateRunning, "C.a"); got.Total != math.MaxInt || got.Self != math.MaxInt {
		t.Errorf("Focus(true, running, C.a) = %+v, want a total and self of %d", got, math.MaxInt)
	}
}

func TestSatAddInt(t *testing.T) {
	cases := []struct{ a, b, want int }{
		{1, 2, 3},
		{math.MaxInt, 1, math.MaxInt},
		{big, big, math.MaxInt},
		{math.MinInt, -1, math.MinInt},
	}
	for _, tc := range cases {
		if got := satAddInt(tc.a, tc.b); got != tc.want {
			t.Errorf("satAddInt(%d, %d) = %d, want %d", tc.a, tc.b, got, tc.want)
		}
	}
}

// The API writes ids as strings or as numbers, and a child id must be read the
// way a node's own id is: a child that is not found leaves its samples on the
// parent.
func TestHotspots_NumericChildIDs(t *testing.T) {
	h := hotspotsOf(map[string]interface{}{
		"dataRootNodeId": float64(1),
		"dataNodes": []interface{}{
			snode(float64(1), "C", "p", true, counts("RUNNING", float64(5)), float64(2)),
			snode(float64(2), "C", "c", true, counts("RUNNING", float64(5))),
		},
	})
	want := StackView{Samples: 5, Rows: []StackRow{{Running: 5, Stack: "C.p;C.c"}}}
	if got := h.Stacks(false); !reflect.DeepEqual(got, want) {
		t.Errorf("Stacks(false) = %+v\nwant %+v", got, want)
	}
	wantFns := FunctionView{Samples: 5, Rows: []FunctionRow{
		{Self: 5, SelfPct: 100, Total: 5, TotalPct: 100, Paths: 1, Function: "C.c"},
		{Self: 0, SelfPct: 0, Total: 5, TotalPct: 100, Paths: 1, Function: "C.p"},
	}}
	if got := mustFunctions(t, h, false, StateRunning); !reflect.DeepEqual(got, wantFns) {
		t.Errorf("Functions(false, running) = %+v\nwant %+v", got, wantFns)
	}
}

// A numeric id and the same id written as a string are one id, however large:
// a number of a million or more must not be spelled with an exponent.
func TestHotspots_LargeNumericIDs(t *testing.T) {
	h := hotspotsOf(map[string]interface{}{
		"dataRootNodeId": "1000000",
		"dataNodes": []interface{}{
			snode(float64(1000000), "C", "p", true, counts("RUNNING", float64(7)), "2000000"),
			snode(float64(2000000), "C", "c", true, counts("RUNNING", float64(5))),
		},
	})
	want := StackView{Samples: 7, Rows: []StackRow{{Running: 5, Stack: "C.p;C.c"}, {Running: 2, Stack: "C.p"}}}
	if got := h.Stacks(false); !reflect.DeepEqual(got, want) {
		t.Errorf("Stacks(false) = %+v\nwant %+v", got, want)
	}
	for id, want := range map[float64]string{999999: "999999", 1000000: "1000000", 1e21: "1000000000000000000000", 1.5: "1.5"} {
		if got, ok := nodeIDStr(id); got != want || !ok {
			t.Errorf("nodeIDStr(%v) = %q, %v, want %q", id, got, ok, want)
		}
	}
}

// A node without an id is no node anything can name. Stored under the empty
// id, it was what a null child id and a missing root id resolved to: an edge
// the document does not hold.
func TestTrees_NodeWithoutAnID(t *testing.T) {
	orphan := snode(nil, "Orphan", "ghost", true, counts("RUNNING", float64(7)))
	delete(orphan, "id")
	h := hotspotsOf(map[string]interface{}{
		"dataRootNodeId": "1",
		"dataNodes": []interface{}{
			snode("1", "Real", "main", true, counts("RUNNING", float64(10)), nil),
			orphan,
		},
	})
	want := StackView{Samples: 10, Rows: []StackRow{{Running: 10, Stack: "Real.main"}}}
	if got := h.Stacks(false); !reflect.DeepEqual(got, want) {
		t.Errorf("Stacks(false) = %+v\nwant %+v", got, want)
	}
	wantFns := FunctionView{Samples: 10, Rows: []FunctionRow{{Self: 10, SelfPct: 100, Total: 10, TotalPct: 100, Paths: 1, Function: "Real.main"}}}
	if got := mustFunctions(t, h, false, StateRunning); !reflect.DeepEqual(got, wantFns) {
		t.Errorf("Functions(false, running) = %+v\nwant %+v", got, wantFns)
	}

	// A document that names no root does not start at the node without an id.
	rootless := hotspotsOf(map[string]interface{}{"dataNodes": []interface{}{orphan}})
	if got := rootless.Stacks(false); len(got.Rows) != 0 || got.Empty != EmptyNoSamples {
		t.Errorf("Stacks of a document without a root = %+v, want no rows", got)
	}
	// An empty id is none either.
	unnamed := snode("", "Orphan", "ghost", true, counts("RUNNING", float64(7)))
	rootless = hotspotsOf(map[string]interface{}{"dataNodes": []interface{}{unnamed}})
	if got := rootless.Stacks(false); len(got.Rows) != 0 || got.Empty != EmptyNoSamples {
		t.Errorf("Stacks of a document whose node has an empty id = %+v, want no rows", got)
	}

	ghost := map[string]interface{}{"name": "Orphan.ghost", "childIds": []interface{}{},
		"allocationInfo": map[string]interface{}{"allocationSize": float64(70)}}
	a := allocationsOf(map[string]interface{}{"analysisResult": map[string]interface{}{
		"stacktreeRootIds": []interface{}{float64(1), nil},
		"stacktreeNodes": []interface{}{
			map[string]interface{}{"id": float64(1), "name": "Real.put", "childIds": []interface{}{nil},
				"allocationInfo": map[string]interface{}{"allocationSize": float64(100)}},
			ghost,
		},
	}})
	wantAllocs := AllocationView{AllocatedBytes: 100, Rows: []AllocationRow{{AllocBytes: 100, Stack: "Real.put"}}}
	if got := a.Stacks(false, false); !reflect.DeepEqual(got, wantAllocs) {
		t.Errorf("allocation Stacks = %+v\nwant %+v", got, wantAllocs)
	}
}

// Every figure of an allocation row is merged the same way when appOnly folds
// paths together: survived bytes and both counts as well as allocated bytes.
func TestCounts_MergedAllocationRowOverTheLimit(t *testing.T) {
	info := func(v float64) map[string]interface{} {
		return map[string]interface{}{"allocationSize": v, "survivorSize": v, "allocationCount": v, "survivorCount": v}
	}
	a := allocationsOf(allocationsResult([]interface{}{float64(1)},
		map[string]interface{}{"id": float64(1), "name": "A.a", "apiInfo": map[string]interface{}{"systemApi": true},
			"childIds": []interface{}{float64(2), float64(3)}, "allocationInfo": info(1e300)},
		map[string]interface{}{"id": float64(2), "name": "lib.x", "apiInfo": map[string]interface{}{"systemApi": false},
			"childIds": []interface{}{}, "allocationInfo": info(6e18)},
		map[string]interface{}{"id": float64(3), "name": "lib.y", "apiInfo": map[string]interface{}{"systemApi": false},
			"childIds": []interface{}{}, "allocationInfo": info(6e18)},
	))
	want := AllocationView{AllocatedBytes: math.MaxInt64, SurvivedBytes: math.MaxInt64, Rows: []AllocationRow{
		{AllocBytes: math.MaxInt64, SurvivedBytes: math.MaxInt64, AllocCount: math.MaxInt64, SurvivedCount: math.MaxInt64, Stack: "A.a"},
	}}
	if got := a.Stacks(true, false); !reflect.DeepEqual(got, want) {
		t.Errorf("Stacks(appOnly) = %+v\nwant %+v", got, want)
	}
	// Unmerged, the totals over the two rows saturate as well.
	got := a.Stacks(false, true)
	if len(got.Rows) != 2 || got.AllocatedBytes != math.MaxInt64 || got.SurvivedBytes != math.MaxInt64 {
		t.Errorf("Stacks = %+v, want two rows and totals of %d", got, int64(math.MaxInt64))
	}
}

// One function reached on two paths, each past half the largest count: its
// total, the caller it shares and the callee it shares are sums over both.
func TestCounts_FunctionOnTwoPathsOverTheLimit(t *testing.T) {
	h := hotspotsOf(hotspotsResult("0",
		snode("0", "", "", false, counts(), "P"),
		snode("P", "C", "p", true, counts("RUNNING", 1e300), "A", "B"),
		snode("A", "C", "x", true, counts("RUNNING", float64(big))),
		snode("B", "C", "x", true, counts("RUNNING", float64(big))),
	))

	wantFns := FunctionView{Samples: math.MaxInt, Rows: []FunctionRow{
		{Self: math.MaxInt, SelfPct: 100, Total: math.MaxInt, TotalPct: 100, Paths: 1, Function: "C.x"}, // one path: both frames fold to C.p;C.x
		{Self: 0, SelfPct: 0, Total: math.MaxInt, TotalPct: 100, Paths: 1, Function: "C.p"},
	}}
	if got := mustFunctions(t, h, false, StateRunning); !reflect.DeepEqual(got, wantFns) {
		t.Errorf("Functions = %+v\nwant %+v", got, wantFns)
	}
	want := FocusView{Function: "C.x", Total: math.MaxInt, Self: math.MaxInt, Callers: []FocusRow{
		{Relation: "caller", Samples: math.MaxInt, Share: 100, Function: "C.p"},
	}}
	if got := mustFocus(t, h, false, StateRunning, "C.x"); !reflect.DeepEqual(got, want) {
		t.Errorf("Focus(C.x) = %+v\nwant %+v", got, want)
	}
	want = FocusView{Function: "C.p", Total: math.MaxInt, Self: 0,
		Callers: []FocusRow{{Relation: "caller", Samples: math.MaxInt, Share: 100, Function: RootCaller}},
		Callees: []FocusRow{{Relation: "callee", Samples: math.MaxInt, Share: 100, Function: "C.x"}},
	}
	if got := mustFocus(t, h, false, StateRunning, "C.p"); !reflect.DeepEqual(got, want) {
		t.Errorf("Focus(C.p) = %+v\nwant %+v", got, want)
	}
}
