package profiling

import (
	"errors"
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
	if got.Samples != 100 {
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
	if lock.Samples != 5 || len(lock.Rows) != 3 {
		t.Fatalf("lock table = %+v", lock)
	}
	if r := lock.Rows[0]; r.Function != "lib.wrap" || r.Self != 5 || r.Total != 5 {
		t.Errorf("top lock row = %+v", r)
	}
	all := hotspotFunctions(fnTree(), false, "all", "self")
	if all.Samples != 105 {
		t.Errorf("all samples = %d, want 105", all.Samples)
	}
	if got := hotspotFunctions(fnTree(), false, "disk_io", "self"); got.Rows != nil || got.Samples != 0 {
		t.Errorf("no disk_io samples: want no rows, got %+v", got)
	}
}

func TestFocus_CallersAndCallees(t *testing.T) {
	h := hotspotsOf(fnTree())
	got, err := h.Focus(false, StateRunning, "wrap")
	if err != nil {
		t.Fatal(err)
	}
	want := FocusView{
		Function: "lib.wrap", Total: 50, Self: 50,
		Callers: []FocusRow{
			{Relation: "caller", Samples: 30, Share: 60, Function: "lib.load"},
			{Relation: "caller", Samples: 20, Share: 40, Function: "app.handle"},
		},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Focus(wrap):\n got %+v\nwant %+v", got, want)
	}
	wantRows := []FocusRow{
		{Relation: "total", Samples: 50, Share: 100, Function: "lib.wrap"},
		{Relation: "self", Samples: 50, Share: 100, Function: "lib.wrap"},
		{Relation: "caller", Samples: 30, Share: 60, Function: "lib.load"},
		{Relation: "caller", Samples: 20, Share: 40, Function: "app.handle"},
	}
	if rows := got.Rows(0); !reflect.DeepEqual(rows, wantRows) {
		t.Fatalf("Rows(0):\n got %+v\nwant %+v", rows, wantRows)
	}

	got, err = h.Focus(false, StateRunning, "app.main")
	if err != nil {
		t.Fatal(err)
	}
	want = FocusView{
		Function: "app.main", Total: 100, Self: 10,
		Callers: []FocusRow{{Relation: "caller", Samples: 100, Share: 100, Function: RootCaller}},
		Callees: []FocusRow{
			{Relation: "callee", Samples: 60, Share: 60, Function: "lib.load"},
			{Relation: "callee", Samples: 30, Share: 30, Function: "app.handle"},
		},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Focus(app.main):\n got %+v\nwant %+v", got, want)
	}
	// A limit caps the callers and the callees separately.
	wantRows = []FocusRow{
		{Relation: "total", Samples: 100, Share: 100, Function: "app.main"},
		{Relation: "self", Samples: 10, Share: 10, Function: "app.main"},
		{Relation: "caller", Samples: 100, Share: 100, Function: RootCaller},
		{Relation: "callee", Samples: 60, Share: 60, Function: "lib.load"}, // limit 1 drops app.handle
	}
	if rows := got.Rows(1); !reflect.DeepEqual(rows, wantRows) {
		t.Fatalf("Rows(1):\n got %+v\nwant %+v", rows, wantRows)
	}

	// Another state counts its own samples: the lock samples are all on
	// lib.wrap under app.handle.
	got, err = h.Focus(false, StateLock, "app.handle")
	if err != nil {
		t.Fatal(err)
	}
	want = FocusView{
		Function: "app.handle", Total: 5, Self: 0,
		Callers: []FocusRow{{Relation: "caller", Samples: 5, Share: 100, Function: "app.main"}},
		Callees: []FocusRow{{Relation: "callee", Samples: 5, Share: 100, Function: "lib.wrap"}},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Focus(app.handle, lock):\n got %+v\nwant %+v", got, want)
	}
}

// A recursive function: callers are the entries into it (its outermost call),
// own time and callees come from its innermost call, so the own row equals its
// self time in the functions view and both splits add up to the total.
//
//	x.f (10) -> x.g (8) -> x.f (6)
func TestFocus_Recursion(t *testing.T) {
	raw := hotspotsResult("0",
		hnode("0", "", "", false, 0, 0, "A"),
		hnode("A", "x", "f", true, 10, 0, "B"),
		hnode("B", "x", "g", true, 8, 0, "C"),
		hnode("C", "x", "f", true, 6, 0),
	)
	got, err := hotspotsOf(raw).Focus(false, StateRunning, "x.f")
	if err != nil {
		t.Fatal(err)
	}
	want := FocusView{
		Function: "x.f", Total: 10, Self: 8,
		Callers: []FocusRow{{Relation: "caller", Samples: 10, Share: 100, Function: RootCaller}},
		Callees: []FocusRow{{Relation: "callee", Samples: 2, Share: 20, Function: "x.g"}},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v\nwant %+v", got, want)
	}
	if self := hotspotFunctions(raw, false, "running", "self").Rows[0]; self.Function != "x.f" || self.Self != 8 {
		t.Errorf("functions view self = %+v, want x.f self 8", self)
	}
}

// A pattern that does not name one function is an error a caller can read:
// the pattern, and for an ambiguous one every candidate, heaviest first.
func TestFocus_Matching(t *testing.T) {
	h := hotspotsOf(fnTree())

	_, err := h.Focus(false, StateRunning, "lib.")
	var ambiguous *AmbiguousMatchError
	if !errors.As(err, &ambiguous) {
		t.Fatalf("ambiguous pattern: err = %v (%T), want an *AmbiguousMatchError", err, err)
	}
	if ambiguous.Pattern != "lib." || !reflect.DeepEqual(ambiguous.Candidates, []string{"lib.load", "lib.wrap"}) {
		t.Errorf("ambiguous = %+v, want lib. with lib.load (60) before lib.wrap (50)", ambiguous)
	}
	if got, want := err.Error(), `"lib." matches 2 functions`; got != want {
		t.Errorf("err = %q, want %q", got, want)
	}

	_, err = h.Focus(false, StateRunning, "nope")
	var noMatch *NoMatchError
	if !errors.As(err, &noMatch) || noMatch.Pattern != "nope" {
		t.Fatalf("no match: err = %v (%T), want a *NoMatchError for nope", err, err)
	}
	if got, want := err.Error(), `no function in the profile matches "nope"`; got != want {
		t.Errorf("err = %q, want %q", got, want)
	}

	// An exact label wins over being a substring of others.
	raw := hotspotsResult("0",
		hnode("0", "", "", false, 0, 0, "A"),
		hnode("A", "x", "f", true, 10, 0, "B"),
		hnode("B", "x", "fg", true, 4, 0),
	)
	if got, err := hotspotsOf(raw).Focus(false, StateRunning, "x.f"); err != nil || got.Function != "x.f" {
		t.Errorf("exact match: function %q, err %v", got.Function, err)
	}

	// Nothing to match against is not a failed match: the view says why.
	got, err := h.Focus(false, StateDiskIO, "nope")
	if err != nil || !reflect.DeepEqual(got, FocusView{Empty: EmptyState}) || got.Rows(0) != nil {
		t.Errorf("no samples in the state: %+v, %v; want an empty view saying %q", got, err, EmptyState)
	}
}

// The zero State and SortKey are the defaults, and a value that is no constant
// is refused rather than read as one.
func TestFunctions_StateAndSortKey(t *testing.T) {
	h := hotspotsOf(fnTree())
	order := func(v FunctionView) []string {
		var out []string
		for _, r := range v.Rows {
			out = append(out, r.Function)
		}
		return out
	}
	bySelf := []string{"lib.wrap", "lib.load", "app.main", "app.handle"}
	byTotal := []string{"app.main", "lib.load", "lib.wrap", "app.handle"}
	cases := []struct {
		state   State
		by      SortKey
		samples int
		order   []string
		err     error
	}{
		{state: "", by: "", samples: 100, order: bySelf},
		{state: StateRunning, by: SortBySelf, samples: 100, order: bySelf},
		{state: StateRunning, by: SortByTotal, samples: 100, order: byTotal},
		// Self ties are broken by total, then by name.
		{state: StateLock, by: SortBySelf, samples: 5, order: []string{"lib.wrap", "app.handle", "app.main"}},
		{state: StateOffCPU, by: "", samples: 5, order: []string{"lib.wrap", "app.handle", "app.main"}},
		{state: StateAll, by: SortByTotal, samples: 105, order: []string{"app.main", "lib.load", "lib.wrap", "app.handle"}},
		{state: "sleeping", by: SortBySelf, err: ErrUnknownState},
		{state: "RUNNING", by: SortBySelf, err: ErrUnknownState},
		{state: StateRunning, by: "name", err: ErrUnknownSortKey},
	}
	for _, tc := range cases {
		got, err := h.Functions(false, tc.state, tc.by)
		if !errors.Is(err, tc.err) {
			t.Errorf("Functions(%q, %q): err = %v, want %v", tc.state, tc.by, err, tc.err)
			continue
		}
		if tc.err != nil {
			if !reflect.DeepEqual(got, FunctionView{}) {
				t.Errorf("Functions(%q, %q): a refused view must be empty, got %+v", tc.state, tc.by, got)
			}
			continue
		}
		if got.Samples != tc.samples || !reflect.DeepEqual(order(got), tc.order) {
			t.Errorf("Functions(%q, %q) = %d samples, %v; want %d, %v", tc.state, tc.by, got.Samples, order(got), tc.samples, tc.order)
		}
	}

	if _, err := h.Focus(false, "sleeping", "wrap"); !errors.Is(err, ErrUnknownState) {
		t.Errorf("Focus(sleeping): err = %v, want ErrUnknownState", err)
	}
	if got, err := h.Focus(false, "", "wrap"); err != nil || got.Total != 50 {
		t.Errorf("Focus with the zero State: %+v, %v; want the running samples", got, err)
	}
}

func TestParseStateAndSortKey(t *testing.T) {
	want := []State{"running", "lock", "net_io", "disk_io", "wait", "offcpu", "all"}
	if got := States(); !reflect.DeepEqual(got, want) {
		t.Errorf("States() = %v, want %v", got, want)
	}
	for _, s := range want {
		if got, err := ParseState(string(s)); err != nil || got != s {
			t.Errorf("ParseState(%q) = %q, %v", s, got, err)
		}
	}
	if got, err := ParseState(""); err != nil || got != StateRunning {
		t.Errorf(`ParseState("") = %q, %v; want running`, got, err)
	}
	if _, err := ParseState("sleeping"); !errors.Is(err, ErrUnknownState) || err.Error() != `profiling: unknown thread state "sleeping"` {
		t.Errorf("ParseState(sleeping): err = %v", err)
	}
	for in, want := range map[string]SortKey{"": SortBySelf, "self": SortBySelf, "total": SortByTotal} {
		if got, err := ParseSortKey(in); err != nil || got != want {
			t.Errorf("ParseSortKey(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	if _, err := ParseSortKey("name"); !errors.Is(err, ErrUnknownSortKey) || err.Error() != `profiling: unknown sort key "name"` {
		t.Errorf("ParseSortKey(name): err = %v", err)
	}
	// The slice is the caller's: changing it does not change the next one.
	States()[0] = "changed"
	if States()[0] != StateRunning {
		t.Error("States() hands out shared storage")
	}
}

// An empty pattern names no function. As a substring it would match every
// one, and so focus whichever function a profile of one happens to hold.
func TestFocus_EmptyPattern(t *testing.T) {
	one := hotspotsOf(hotspotsResult("0", hnode("0", "C", "only", true, 3, 0)))
	for name, h := range map[string]*Hotspots{"one function": one, "several": hotspotsOf(fnTree()), "no samples": hotspotsOf(hotspotsResult("0"))} {
		if got, err := h.Focus(false, StateRunning, ""); !errors.Is(err, ErrEmptyPattern) || !reflect.DeepEqual(got, FocusView{}) {
			t.Errorf("%s: Focus with an empty pattern = %+v, %v; want ErrEmptyPattern", name, got, err)
		}
		if got, err := h.FocusExact(false, StateRunning, ""); !errors.Is(err, ErrEmptyPattern) || !reflect.DeepEqual(got, FocusView{}) {
			t.Errorf("%s: FocusExact with an empty pattern = %+v, %v; want ErrEmptyPattern", name, got, err)
		}
	}
}

// C.run has running samples only and C.runHandler the lock samples. A pattern
// is matched against the functions with samples in the state, so Focus on
// C.run in the lock state finds C.runHandler as its one substring match.
// FocusExact takes the label and nothing else.
func TestFocusExact(t *testing.T) {
	h := hotspotsOf(hotspotsResult("0",
		hnode("0", "", "", false, 4, 6, "A", "B"),
		hnode("A", "C", "run", true, 4, 0),
		hnode("B", "C", "runHandler", true, 0, 6),
	))

	if got, err := h.Focus(false, StateLock, "C.run"); err != nil || got.Function != "C.runHandler" {
		t.Errorf("Focus(lock, C.run) = %q, %v; want its one substring match, C.runHandler", got.Function, err)
	}
	_, err := h.FocusExact(false, StateLock, "C.run")
	var noMatch *NoMatchError
	if !errors.As(err, &noMatch) || noMatch.Pattern != "C.run" {
		t.Errorf("FocusExact(lock, C.run): err = %v, want a *NoMatchError for C.run", err)
	}
	if _, err := h.FocusExact(false, StateRunning, "run"); !errors.As(err, &noMatch) {
		t.Errorf("FocusExact(running, run): err = %v, want a *NoMatchError: a substring is not a label", err)
	}

	want := FocusView{Function: "C.run", Total: 4, Self: 4, Callers: []FocusRow{{Relation: "caller", Samples: 4, Share: 100, Function: RootCaller}}}
	if got, err := h.FocusExact(false, StateRunning, "C.run"); err != nil || !reflect.DeepEqual(got, want) {
		t.Errorf("FocusExact(running, C.run) = %+v, %v\nwant %+v", got, err, want)
	}
	// Like Focus, it checks its state and reports an empty analysis in the view.
	if _, err := h.FocusExact(false, "cpu", "C.run"); !errors.Is(err, ErrUnknownState) {
		t.Errorf("FocusExact with an unknown state: err = %v, want ErrUnknownState", err)
	}
	if got, err := h.FocusExact(false, StateDiskIO, "C.run"); err != nil || !reflect.DeepEqual(got, FocusView{Empty: EmptyState}) {
		t.Errorf("FocusExact(disk_io, C.run) = %+v, %v; want an empty view saying %q", got, err, EmptyState)
	}
}

// With appOnly the focus view counts as the functions view does: a hidden
// frame's samples go to the nearest application frame above it, so the hidden
// frames are neither callers nor callees, and the function's own time holds
// theirs.
func TestFocus_AppOnly(t *testing.T) {
	h := hotspotsOf(fnTree())

	want := FocusView{
		Function: "app.handle", Total: 30, Self: 30,
		Callers: []FocusRow{{Relation: "caller", Samples: 30, Share: 100, Function: "app.main"}},
	}
	if got, err := h.Focus(true, StateRunning, "handle"); err != nil || !reflect.DeepEqual(got, want) {
		t.Errorf("Focus(appOnly, handle) = %+v, %v\nwant %+v", got, err, want)
	}
	want = FocusView{
		Function: "app.main", Total: 100, Self: 70,
		Callers: []FocusRow{{Relation: "caller", Samples: 100, Share: 100, Function: RootCaller}},
		Callees: []FocusRow{{Relation: "callee", Samples: 30, Share: 30, Function: "app.handle"}},
	}
	if got, err := h.FocusExact(true, StateRunning, "app.main"); err != nil || !reflect.DeepEqual(got, want) {
		t.Errorf("FocusExact(appOnly, app.main) = %+v, %v\nwant %+v", got, err, want)
	}
	// A frame appOnly hides is no function of the view.
	var noMatch *NoMatchError
	if _, err := h.Focus(true, StateRunning, "lib.wrap"); !errors.As(err, &noMatch) {
		t.Errorf("Focus(appOnly, lib.wrap): err = %v, want a *NoMatchError", err)
	}
}
