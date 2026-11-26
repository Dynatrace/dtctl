package profiling

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

// hotspotsAnalysis wraps a hotspots envelope built by the fold tests' helpers.
func hotspotsAnalysis(env map[string]interface{}) *Analysis {
	return NewAnalysis(&Response{Status: "completed", Kind: Kind["hotspots"], EntityID: "PROCESS-0000000000000001", Result: env["result"]})
}

func survnode(id float64, name string, app bool, alloc, surv int, children ...interface{}) map[string]interface{} {
	n := memnode(id, name, app, alloc, 1, children...)
	n["allocationInfo"].(map[string]interface{})["survivorSize"] = float64(surv)
	return n
}

func allocationsAnalysis(env map[string]interface{}) *Analysis {
	return NewAnalysis(&Response{Status: "completed", Kind: Kind["memory"], EntityID: "PROCESS-0000000000000001", Result: env["result"]})
}

func TestWhyEmpty_NoSamples(t *testing.T) {
	a := NewAnalysis(&Response{EntityID: "PROCESS_GROUP-1", Status: "no-data"})
	if _, got := a.WhyEmpty(false, ""); !strings.Contains(got, "no profiling samples in the window") {
		t.Errorf("note = %q", got)
	}
}

// Node.js frames carry their file in fileName/filePath (className is empty), so
// compactResult must keep those when set; it drops them only when empty (Java).
func TestCompactResult_KeepsFileOnlyWhenSet(t *testing.T) {
	in := map[string]interface{}{"dataNodes": []interface{}{
		map[string]interface{}{"methodName": "Socket", "fileName": "node:net", "filePath": "", "color": "#fff"},
		map[string]interface{}{"methodName": "run", "fileName": nil, "filePath": nil},
	}}
	nodes := compactResult(in).(map[string]interface{})["dataNodes"].([]interface{})
	js, java := nodes[0].(map[string]interface{}), nodes[1].(map[string]interface{})
	if js["fileName"] != "node:net" {
		t.Errorf("Node.js fileName dropped: %v", js)
	}
	if _, ok := js["filePath"]; ok {
		t.Errorf("empty filePath kept: %v", js)
	}
	if _, ok := js["color"]; ok {
		t.Errorf("visual-only key kept: %v", js)
	}
	if _, ok := java["fileName"]; ok {
		t.Errorf("null fileName kept: %v", java)
	}
}

// The envelope built from a Response is the document its JSON encoding decodes
// to, compacted: what --raw has always printed.
func TestNewAnalysis_RawEqualsTheEncodedResponse(t *testing.T) {
	var result interface{}
	body := `{"dataRootNodeId":"0","timeseries":{"dataPoints":[1,2,3]},"dataNodes":[
	 {"id":"0","methodName":"","color":"#fff","uuid":"u","fileName":null,"samples":{"RUNNING":0},"childIds":["A"]},
	 {"id":"A","className":"app.Main","methodName":"main","apiInfo":{"systemApi":true},"samples":{"RUNNING":7},"childIds":[]}]}`
	if err := json.Unmarshal([]byte(body), &result); err != nil {
		t.Fatal(err)
	}
	for _, resp := range []*Response{
		{Status: "completed", Kind: Kind["hotspots"], EntityID: "PROCESS-0000000000000001", PollCount: 3, Result: result},
		{Status: "no-data", Kind: Kind["memory"], EntityID: "PROCESS_GROUP-0000000000000001", PollCount: 1},
	} {
		b, err := json.Marshal(resp)
		if err != nil {
			t.Fatal(err)
		}
		var decoded interface{}
		if err := json.Unmarshal(b, &decoded); err != nil {
			t.Fatal(err)
		}
		a := NewAnalysis(resp)
		if want := compactResult(decoded); !reflect.DeepEqual(interface{}(a.Raw()), want) {
			t.Errorf("Raw() = %#v\nwant   %#v", a.Raw(), want)
		}
		if a.Kind != resp.Kind || a.Entity != resp.EntityID || a.Status != resp.Status {
			t.Errorf("fields = %q %q %q", a.Kind, a.Entity, a.Status)
		}
	}
	// The caller's result is copied, not compacted in place.
	root := result.(map[string]interface{})["dataNodes"].([]interface{})[0].(map[string]interface{})
	if _, ok := root["color"]; !ok {
		t.Error("NewAnalysis modified the response it was given")
	}
}

func TestAnalysis_StacksTotals(t *testing.T) {
	a := hotspotsAnalysis(fnTree())

	all := a.Stacks(false)
	if !reflect.DeepEqual(all.Rows, HotspotStacks(fnTree(), false)) {
		t.Errorf("rows differ from HotspotStacks: %+v", all.Rows)
	}
	if len(all.Rows) != 5 || all.Samples != 100 {
		t.Errorf("got %d rows, %d samples; want 5 rows, 100 samples", len(all.Rows), all.Samples)
	}

	// --app-only moves a hidden frame's samples to the kept frame above it,
	// so the total stays the analysis total.
	app := a.Stacks(true)
	if len(app.Rows) != 2 || app.Samples != 100 {
		t.Errorf("app-only: got %d rows, %d samples; want 2 rows, 100 samples", len(app.Rows), app.Samples)
	}

	empty := hotspotsAnalysis(envelope("0")).Stacks(false)
	if empty.Rows != nil || empty.Samples != 0 {
		t.Errorf("empty analysis: %+v", empty)
	}
}

func TestAnalysis_AllocationsTotals(t *testing.T) {
	a := allocationsAnalysis(memEnvelope([]interface{}{float64(1), float64(2)},
		survnode(1, "Cache.put", true, 1000, 5000, float64(3)),
		survnode(3, "Svc.get", true, 400, 100),
		survnode(2, "Buf.grow", false, 3000, 20),
	))

	byAlloc := a.Allocations(false, false)
	if byAlloc.AllocatedBytes != 4000 || byAlloc.SurvivedBytes != 5020 {
		t.Errorf("totals = %d allocated, %d survived; want 4000, 5020", byAlloc.AllocatedBytes, byAlloc.SurvivedBytes)
	}
	if len(byAlloc.Rows) != 3 || byAlloc.Rows[0].Stack != "Buf.grow" {
		t.Errorf("rows by allocated bytes = %+v", byAlloc.Rows)
	}

	// The ranking changes the order, not the totals.
	bySurv := a.Allocations(false, true)
	if bySurv.AllocatedBytes != 4000 || bySurv.SurvivedBytes != 5020 {
		t.Errorf("totals by survivors = %d, %d", bySurv.AllocatedBytes, bySurv.SurvivedBytes)
	}
	if bySurv.Rows[0].Stack != "Cache.put" {
		t.Errorf("rows by survived bytes = %+v", bySurv.Rows)
	}

	app := a.Allocations(true, false)
	if app.AllocatedBytes != 1000 || app.SurvivedBytes != 5000 {
		t.Errorf("app-only totals = %d, %d; want 1000, 5000", app.AllocatedBytes, app.SurvivedBytes)
	}
}

func TestAnalysis_FunctionsAndFocus(t *testing.T) {
	a := hotspotsAnalysis(fnTree())
	if got, want := a.Functions(false, "running", "self"), HotspotFunctions(fnTree(), false, "running", "self"); !reflect.DeepEqual(got, want) {
		t.Errorf("Functions = %+v, want %+v", got, want)
	}
	rows, fn, nCallers, nCallees, err := a.Focus(false, "running", "wrap", 0)
	if err != nil || fn != "lib.wrap" || nCallers != 2 || nCallees != 0 || len(rows) != 4 {
		t.Errorf("Focus = %+v, %q, %d, %d, %v", rows, fn, nCallers, nCallees, err)
	}
}

func TestWhyEmpty(t *testing.T) {
	const entity = "PROCESS-0000000000000001"
	// A tree whose frames are all library code.
	libOnly := envelope("0",
		hnode("0", "", "", false, 0, 0, "A"),
		hnode("A", "lib", "run", false, 10, 0),
	)
	noSamples := NewAnalysis(&Response{Status: "no-data", Kind: Kind["hotspots"], EntityID: entity})
	cases := []struct {
		name     string
		a        *Analysis
		appOnly  bool
		state    string
		reason   EmptyReason
		wantNote string
	}{
		{name: "no data at all", a: noSamples, reason: EmptyNoSamples,
			wantNote: "no stacks for " + entity + ": no profiling samples in the window (status=no-data) — the process may be idle, restarted, or not under load; try another window (at most 2h), or --raw for the API's whole result"},
		{name: "no data, app-only", a: noSamples, appOnly: true, reason: EmptyNoSamples,
			wantNote: "no profiling samples in the window (status=no-data)"},
		{name: "completed without nodes", a: hotspotsAnalysis(envelope("0")), reason: EmptyNoSamples,
			wantNote: "(status=completed)"},
		{name: "app-only removed every frame", a: hotspotsAnalysis(libOnly), appOnly: true, reason: EmptyAppOnly,
			wantNote: "no stacks for " + entity + ": --app-only removed every frame (the profiler classified none as application code) — re-run without --app-only"},
		{name: "running state reads like the stack view", a: hotspotsAnalysis(libOnly), appOnly: true, state: "running", reason: EmptyAppOnly,
			wantNote: "--app-only removed every frame"},
		{name: "a state without samples", a: hotspotsAnalysis(fnTree()), state: "disk_io", reason: EmptyState,
			wantNote: "no disk_io samples for " + entity + " in the window — the stack view (without --view/--focus) shows every thread state; waiting states may need --show-waiting"},
		{name: "a state without samples, app-only", a: hotspotsAnalysis(libOnly), appOnly: true, state: "offcpu", reason: EmptyState,
			wantNote: "no offcpu samples for " + entity},
		{name: "allocations, app-only removed every frame", appOnly: true, reason: EmptyAppOnly,
			a:        allocationsAnalysis(memEnvelope([]interface{}{float64(1)}, survnode(1, "Buf.grow", false, 10, 0))),
			wantNote: "--app-only removed every frame"},
		{name: "allocations, nothing allocated", reason: EmptyNoSamples,
			a:        allocationsAnalysis(memEnvelope([]interface{}{})),
			wantNote: "no profiling samples in the window (status=completed)"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			reason, note := tc.a.WhyEmpty(tc.appOnly, tc.state)
			if reason != tc.reason {
				t.Errorf("reason = %q, want %q", reason, tc.reason)
			}
			if !strings.Contains(note, tc.wantNote) {
				t.Errorf("note = %q, want it to contain %q", note, tc.wantNote)
			}
			if strings.HasPrefix(note, "#") || strings.HasSuffix(note, "\n") {
				t.Errorf("the note is one bare line, got %q", note)
			}
		})
	}
}
