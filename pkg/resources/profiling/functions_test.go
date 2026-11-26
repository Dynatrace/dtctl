package profiling

import (
	"reflect"
	"strings"
	"testing"
)

// fnTree is a small tree with cumulative counts, the way the API reports them:
//
//	app.main (100)
//	├── lib.load (60)
//	│   └── lib.wrap (30)
//	└── app.handle (30)
//	    └── lib.wrap (20, lock 5)
//
// Self: main 10, load 30, wrap 30+20, handle 10. lib.wrap is the scattered
// function: the hottest by self time, split over two stacks.
func fnTree() map[string]interface{} {
	return hotspotsResult("0",
		hnode("0", "", "", false, 0, 0, "A"),
		hnode("A", "app", "main", true, 100, 5, "B", "D"),
		hnode("B", "lib", "load", false, 60, 0, "C1"),
		hnode("C1", "lib", "wrap", false, 30, 0),
		hnode("D", "app", "handle", true, 30, 5, "C2"),
		hnode("C2", "lib", "wrap", false, 20, 5),
	)
}

func TestHotspotFunctions_SelfAndTotal(t *testing.T) {
	got := hotspotFunctions(fnTree(), false, "running", "self")
	if got == nil || got.Samples != 100 {
		t.Fatalf("table = %+v", got)
	}
	want := []FunctionRow{
		{Self: 50, SelfPct: 50, Total: 50, TotalPct: 50, Paths: 2, Function: "lib.wrap"},
		{Self: 30, SelfPct: 30, Total: 60, TotalPct: 60, Paths: 1, Function: "lib.load"},
		{Self: 10, SelfPct: 10, Total: 100, TotalPct: 100, Paths: 1, Function: "app.main"},
		{Self: 10, SelfPct: 10, Total: 30, TotalPct: 30, Paths: 1, Function: "app.handle"},
	}
	if !reflect.DeepEqual(got.Rows, want) {
		t.Fatalf("rows:\n got %+v\nwant %+v", got.Rows, want)
	}
}

func TestHotspotFunctions_SortByTotal(t *testing.T) {
	got := hotspotFunctions(fnTree(), false, "running", "total")
	var order []string
	for _, r := range got.Rows {
		order = append(order, r.Function)
	}
	want := []string{"app.main", "lib.load", "lib.wrap", "app.handle"}
	if !reflect.DeepEqual(order, want) {
		t.Fatalf("order = %v, want %v", order, want)
	}
}

// The function view must agree with the stack view: summing the stack rows by
// their last frame gives each function's self time.
func TestHotspotFunctions_SelfMatchesStackView(t *testing.T) {
	bySelf := map[string]int{}
	for _, l := range strings.Split(strings.TrimSpace(toCollapsed("methodHotspots", fnTree(), 0, false)), "\n") {
		stack := strings.Fields(l)[0]
		frames := strings.Split(stack, ";")
		bySelf[frames[len(frames)-1]] += runOf(t, l)
	}
	for _, r := range hotspotFunctions(fnTree(), false, "running", "self").Rows {
		if bySelf[r.Function] != r.Self {
			t.Errorf("%s: stack view self %d, function view self %d", r.Function, bySelf[r.Function], r.Self)
		}
	}
}

// A recursive function is on the stack twice, but each sample counts once in its
// total: total is taken at the outermost occurrence only.
func TestHotspotFunctions_RecursionCountsOnce(t *testing.T) {
	raw := hotspotsResult("0",
		hnode("0", "", "", false, 0, 0, "A"),
		hnode("A", "x", "f", true, 10, 0, "B"),
		hnode("B", "x", "f", true, 6, 0),
	)
	got := hotspotFunctions(raw, false, "running", "self").Rows
	want := []FunctionRow{{Self: 10, SelfPct: 100, Total: 10, TotalPct: 100, Paths: 2, Function: "x.f"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v, want %+v", got, want)
	}
}

// With --app-only, hidden frames' self time goes to the nearest kept frame
// above them, as in the stack view.
func TestHotspotFunctions_AppOnly(t *testing.T) {
	got := hotspotFunctions(fnTree(), true, "running", "self").Rows
	want := []FunctionRow{
		{Self: 70, SelfPct: 70, Total: 100, TotalPct: 100, Paths: 1, Function: "app.main"},
		{Self: 30, SelfPct: 30, Total: 30, TotalPct: 30, Paths: 1, Function: "app.handle"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v, want %+v", got, want)
	}
}

func TestHotspotFunctions_State(t *testing.T) {
	lock := hotspotFunctions(fnTree(), false, "lock", "self")
	if lock == nil || lock.Samples != 5 || len(lock.Rows) != 3 {
		t.Fatalf("lock table = %+v", lock)
	}
	if r := lock.Rows[0]; r.Function != "lib.wrap" || r.Self != 5 || r.Total != 5 {
		t.Errorf("top lock row = %+v", r)
	}
	all := hotspotFunctions(fnTree(), false, "all", "self")
	if all.Samples != 105 {
		t.Errorf("all samples = %d, want 105", all.Samples)
	}
	if hotspotFunctions(fnTree(), false, "disk_io", "self") != nil {
		t.Error("no disk_io samples: want nil")
	}
}

func TestHotspotFocus_CallersAndCallees(t *testing.T) {
	rows, fn, nCallers, nCallees, err := hotspotFocus(fnTree(), false, "running", "wrap", 0)
	if err != nil {
		t.Fatal(err)
	}
	if fn != "lib.wrap" || nCallers != 2 || nCallees != 0 {
		t.Fatalf("fn=%q callers=%d callees=%d", fn, nCallers, nCallees)
	}
	want := []FocusRow{
		{Relation: "total", Samples: 50, Share: 100, Function: "lib.wrap"},
		{Relation: "self", Samples: 50, Share: 100, Function: "lib.wrap"},
		{Relation: "caller", Samples: 30, Share: 60, Function: "lib.load"},
		{Relation: "caller", Samples: 20, Share: 40, Function: "app.handle"},
	}
	if !reflect.DeepEqual(rows, want) {
		t.Fatalf("rows:\n got %+v\nwant %+v", rows, want)
	}

	rows, _, _, _, err = hotspotFocus(fnTree(), false, "running", "app.main", 1)
	if err != nil {
		t.Fatal(err)
	}
	want = []FocusRow{
		{Relation: "total", Samples: 100, Share: 100, Function: "app.main"},
		{Relation: "self", Samples: 10, Share: 10, Function: "app.main"},
		{Relation: "caller", Samples: 100, Share: 100, Function: RootCaller},
		{Relation: "callee", Samples: 60, Share: 60, Function: "lib.load"}, // limit 1 drops app.handle
	}
	if !reflect.DeepEqual(rows, want) {
		t.Fatalf("rows:\n got %+v\nwant %+v", rows, want)
	}
}

// A recursive function: callers are the entries into it (its outermost call),
// own time and callees come from its innermost call, so the own row equals its
// self time in the functions view and both splits add up to the total.
//
//	x.f (10) -> x.g (8) -> x.f (6)
func TestHotspotFocus_Recursion(t *testing.T) {
	raw := hotspotsResult("0",
		hnode("0", "", "", false, 0, 0, "A"),
		hnode("A", "x", "f", true, 10, 0, "B"),
		hnode("B", "x", "g", true, 8, 0, "C"),
		hnode("C", "x", "f", true, 6, 0),
	)
	rows, _, _, _, err := hotspotFocus(raw, false, "running", "x.f", 0)
	if err != nil {
		t.Fatal(err)
	}
	want := []FocusRow{
		{Relation: "total", Samples: 10, Share: 100, Function: "x.f"},
		{Relation: "self", Samples: 8, Share: 80, Function: "x.f"},
		{Relation: "caller", Samples: 10, Share: 100, Function: RootCaller},
		{Relation: "callee", Samples: 2, Share: 20, Function: "x.g"},
	}
	if !reflect.DeepEqual(rows, want) {
		t.Fatalf("rows:\n got %+v\nwant %+v", rows, want)
	}
	if self := hotspotFunctions(raw, false, "running", "self").Rows[0]; self.Function != "x.f" || self.Self != 8 {
		t.Errorf("functions view self = %+v, want x.f self 8", self)
	}
}

func TestHotspotFocus_Matching(t *testing.T) {
	if _, _, _, _, err := hotspotFocus(fnTree(), false, "running", "lib.", 0); err == nil ||
		!strings.Contains(err.Error(), "matches 2 functions") || !strings.Contains(err.Error(), "lib.wrap") {
		t.Errorf("ambiguous pattern: err = %v", err)
	}
	if _, _, _, _, err := hotspotFocus(fnTree(), false, "running", "nope", 0); err == nil ||
		!strings.Contains(err.Error(), "--view functions") {
		t.Errorf("no match: err = %v", err)
	}
	// An exact label wins over being a substring of others.
	raw := hotspotsResult("0",
		hnode("0", "", "", false, 0, 0, "A"),
		hnode("A", "x", "f", true, 10, 0, "B"),
		hnode("B", "x", "fg", true, 4, 0),
	)
	if _, fn, _, _, err := hotspotFocus(raw, false, "running", "x.f", 0); err != nil || fn != "x.f" {
		t.Errorf("exact match: fn=%q err=%v", fn, err)
	}
}
