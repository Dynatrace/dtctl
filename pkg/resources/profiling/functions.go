package profiling

import (
	"encoding/binary"
	"fmt"
	"hash/fnv"
	"math"
	"sort"
	"strings"
)

// Per-function views of a hotspots tree. The stack view (Hotspots.Stacks) prints one
// row per unique call stack, so a function reached through many paths is split
// over many rows: on a Node.js service, a module-loader function carried 5% of
// all CPU spread over 378 rows of about 12 samples each, the first at row 46.
// "Which function uses the most CPU" then needs the rows summed by hand, which
// every agent did with awk or a script. These views do the sum:
//
//   - Functions: one row per function, its self time (samples where it was the
//     running frame) and its total time (samples with it anywhere on the stack).
//   - Focus: one function's callers and callees, each with its share of the
//     function's total time. Callers are taken at its outermost call on each
//     stack, callees and own time at its innermost, so recursion adds up.
//
// Both walk the same tree as the stack view, once, in time linear in the node
// count. Self samples follow the stack view exactly (self = node − Σ children, per
// thread state, clamped at zero), and with --app-only a hidden frame's self time
// goes to the nearest kept frame above it, so the numbers match the stack view.

// States are the --state values: which thread-state samples the views count.
var States = []string{"running", "lock", "net_io", "disk_io", "wait", "offcpu", "all"}

// stateKeys maps a --state value to the API's sample keys it sums.
var stateKeys = map[string][]string{
	"running": {"RUNNING"},
	"lock":    {"LOCK"},
	"net_io":  {"NET_IO"},
	"disk_io": {"DISK_IO"},
	"wait":    {"WAIT"},
	"offcpu":  {"LOCK", "NET_IO", "DISK_IO", "WAIT"},
	"all":     {"RUNNING", "LOCK", "NET_IO", "DISK_IO", "WAIT"},
}

// ValidState reports whether s is a --state value.
func ValidState(s string) bool {
	_, ok := stateKeys[s]
	return ok
}

func stateValue(samples map[string]int, state string) int {
	v := 0
	for _, k := range stateKeys[state] {
		v += samples[k]
	}
	return v
}

// FunctionRow is one function in the functions view. Shares are percentages of
// all counted samples (in the chosen state) in the window.
type FunctionRow struct {
	Self     int     `json:"self" table:"SELF"`
	SelfPct  float64 `json:"selfPct" table:"SELF%"`
	Total    int     `json:"total" table:"TOTAL"`
	TotalPct float64 `json:"totalPct" table:"TOTAL%"`
	// Paths counts the distinct call paths that reach the function: how
	// scattered it is over the stack view's rows.
	Paths    int    `json:"paths" table:"PATHS"`
	Function string `json:"function" table:"FUNCTION"`
}

// FunctionTable is the functions view: all rows, sorted, before any limit.
type FunctionTable struct {
	Rows []FunctionRow
	// Samples is the number of counted samples in the window, the base of the
	// percentages.
	Samples int
	// Empty says why there are no rows, and is empty when there are some.
	Empty EmptyReason
}

// FocusRow is one line of the focus (callers/callees) view.
type FocusRow struct {
	// Relation is total, self, caller or callee.
	Relation string `json:"relation" table:"RELATION"`
	Samples  int    `json:"samples" table:"SAMPLES"`
	// Share is the percentage of the focused function's total time.
	Share    float64 `json:"share" table:"SHARE%"`
	Function string  `json:"function" table:"FUNCTION"`
}

// RootCaller names the caller of a function that is the outermost kept frame.
const RootCaller = "(root)"

// funcAcc accumulates one function while the tree is walked.
type funcAcc struct {
	self, total int
	paths       map[uint64]struct{}
}

// funcWalk is one pass over a hotspots tree, collecting per-function self and
// total samples and, when focus is set, that function's callers and callees.
type funcWalk struct {
	nodes   map[string]*treeNode
	appOnly bool
	state   string
	focus   string

	funcs   map[string]*funcAcc
	onPath  map[string]int
	visited map[string]bool // node ids walked; a repeat means a malformed (cyclic) result
	callers map[string]int
	callees map[string]int
	ownSelf int // samples with the focused function running (its innermost call on top)
}

// walk visits node id. nearest is the nearest kept frame above it ("" for none)
// and key identifies the kept-frame path to it. inFocus says the focused function
// is on that path, and callee names the kept frame right below its innermost
// occurrence ("" while there is none yet). It returns the counted self samples of
// the subtree, which for a kept frame is the time spent with that frame on the
// stack.
//
// For the focus view each sample is split two ways. Its caller is the frame
// above the focused function's outermost occurrence on the stack: how execution
// entered the function, recursion included. Its callee is the frame right below
// the innermost occurrence, or none when the function itself is running: what
// the function spends its time on. Both splits add up to the function's total.
func (w *funcWalk) walk(id, nearest string, key uint64, inFocus bool, callee string) int {
	n := w.nodes[id]
	if n == nil || w.visited[id] {
		return 0
	}
	w.visited[id] = true
	kept := n.label != "" && (!w.appOnly || n.isApp)
	label, caller := nearest, nearest
	outermost := false
	if kept {
		label = n.label
		key = pathKey(key, label)
		outermost = w.onPath[label] == 0
		w.onPath[label]++
		switch {
		case w.focus != "" && label == w.focus:
			inFocus, callee = true, ""
		case inFocus && callee == "":
			callee = label
		}
	}

	self := stateValue(n.self(w.nodes), w.state)
	if inFocus && self > 0 {
		if callee == "" {
			w.ownSelf += self
		} else {
			w.callees[callee] += self
		}
	}
	sub := self
	for _, cid := range n.children {
		sub += w.walk(cid, label, key, inFocus, callee)
	}

	if label != "" && self > 0 {
		w.acc(label).self += self
	}
	if kept {
		w.onPath[label]--
		a := w.acc(label)
		if outermost {
			// The outermost occurrence's subtree already holds every nested
			// (recursive) one, so total counts each sample once.
			a.total += sub
		}
		if sub > 0 {
			a.paths[key] = struct{}{}
		}
		if label == w.focus && outermost && sub > 0 {
			if caller == "" {
				caller = RootCaller
			}
			w.callers[caller] += sub
		}
	}
	return sub
}

func (w *funcWalk) acc(label string) *funcAcc {
	a := w.funcs[label]
	if a == nil {
		a = &funcAcc{paths: map[uint64]struct{}{}}
		w.funcs[label] = a
	}
	return a
}

// pathKey extends a kept-frame path hash by one frame. Paths that --app-only
// collapses onto the same kept frames get the same key, as they share one row
// in the stack view.
func pathKey(parent uint64, label string) uint64 {
	h := fnv.New64a()
	var b [8]byte
	binary.LittleEndian.PutUint64(b[:], parent)
	_, _ = h.Write(b[:])
	_, _ = h.Write([]byte(label))
	return h.Sum64()
}

func newFuncWalk(raw interface{}, appOnly bool, state, focus string) (*funcWalk, string) {
	nodes, root := hotspotTree(raw)
	if nodes == nil {
		return nil, ""
	}
	return &funcWalk{
		nodes:   nodes,
		appOnly: appOnly,
		state:   state,
		focus:   focus,
		funcs:   map[string]*funcAcc{},
		onPath:  map[string]int{},
		visited: map[string]bool{},
		callers: map[string]int{},
		callees: map[string]int{},
	}, root
}

// hotspotFunctions builds the functions view of a hotspots result, counting the
// samples of the given state. sortBy is "self" (default) or "total". The table
// has no rows when the result has no samples to show.
func hotspotFunctions(raw interface{}, appOnly bool, state, sortBy string) FunctionTable {
	var t FunctionTable
	w, root := newFuncWalk(raw, appOnly, state, "")
	if w == nil {
		return t
	}
	w.walk(root, "", 0, false, "")

	for _, a := range w.funcs {
		t.Samples += a.self
	}
	if t.Samples == 0 {
		return t
	}
	for label, a := range w.funcs {
		if a.total == 0 {
			continue
		}
		t.Rows = append(t.Rows, FunctionRow{
			Self:     a.self,
			SelfPct:  pct(a.self, t.Samples),
			Total:    a.total,
			TotalPct: pct(a.total, t.Samples),
			Paths:    len(a.paths),
			Function: label,
		})
	}
	sort.Slice(t.Rows, func(i, j int) bool {
		a, b := t.Rows[i], t.Rows[j]
		ka, kb := a.Self, b.Self
		if sortBy == "total" {
			ka, kb = a.Total, b.Total
		}
		if ka != kb {
			return ka > kb
		}
		if a.Total != b.Total {
			return a.Total > b.Total
		}
		return a.Function < b.Function
	})
	return t
}

// hotspotFocus builds the callers/callees view of one function. pattern names the
// function: an exact frame label, or a substring of exactly one label. limit caps
// the callers and the callees separately (0 = all). It also returns the matched
// label, and how many callers and callees there were before the limit.
func hotspotFocus(raw interface{}, appOnly bool, state, pattern string, limit int) (rows []FocusRow, function string, nCallers, nCallees int, err error) {
	t := hotspotFunctions(raw, appOnly, state, "total")
	if len(t.Rows) == 0 {
		return nil, "", 0, 0, fmt.Errorf("no %s samples in the window to focus on", state)
	}
	function, err = matchFunction(t.Rows, pattern)
	if err != nil {
		return nil, "", 0, 0, err
	}
	w, root := newFuncWalk(raw, appOnly, state, function)
	w.walk(root, "", 0, false, "")
	a := w.funcs[function]

	edges := func(m map[string]int) []FocusRow {
		var out []FocusRow
		for f, s := range m {
			out = append(out, FocusRow{Samples: s, Share: pct(s, a.total), Function: f})
		}
		sort.Slice(out, func(i, j int) bool {
			if out[i].Samples != out[j].Samples {
				return out[i].Samples > out[j].Samples
			}
			return out[i].Function < out[j].Function
		})
		return out
	}
	callers, callees := edges(w.callers), edges(w.callees)
	nCallers, nCallees = len(callers), len(callees)

	// The function's own time: samples with it running (or a frame --app-only
	// hid below it). With the callees' time it adds up to the total.
	own := w.ownSelf
	if limit > 0 && len(callers) > limit {
		callers = callers[:limit]
	}
	if limit > 0 && len(callees) > limit {
		callees = callees[:limit]
	}

	rows = append(rows,
		FocusRow{Relation: "total", Samples: a.total, Share: 100, Function: function},
		FocusRow{Relation: "self", Samples: own, Share: pct(own, a.total), Function: function},
	)
	for _, c := range callers {
		c.Relation = "caller"
		rows = append(rows, c)
	}
	for _, c := range callees {
		c.Relation = "callee"
		rows = append(rows, c)
	}
	return rows, function, nCallers, nCallees, nil
}

// matchFunction resolves a --focus pattern against the function labels: an exact
// label wins, otherwise the pattern must be a substring of exactly one label.
// rows must be sorted, so an ambiguous match lists the heaviest candidates.
func matchFunction(rows []FunctionRow, pattern string) (string, error) {
	var matches []string
	for _, r := range rows {
		if r.Function == pattern {
			return r.Function, nil
		}
		if strings.Contains(r.Function, pattern) {
			matches = append(matches, r.Function)
		}
	}
	switch len(matches) {
	case 0:
		return "", fmt.Errorf("no function in the profile matches %q — list them with --view functions", pattern)
	case 1:
		return matches[0], nil
	}
	shown := matches
	if len(shown) > 8 {
		shown = shown[:8]
	}
	return "", fmt.Errorf("%q matches %d functions; pass one exactly:\n  %s", pattern, len(matches), strings.Join(shown, "\n  "))
}

// pct is part as a percentage of whole, to one decimal.
func pct(part, whole int) float64 {
	if whole == 0 {
		return 0
	}
	return math.Round(float64(part)*1000/float64(whole)) / 10
}
