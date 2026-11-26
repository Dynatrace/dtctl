package profiling

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/Dynatrace/dtctl/pkg/client"
	sdkcla "github.com/Dynatrace/dtctl/sdk/api/codelevelanalysis"
)

const testEntity = "PROCESS-0000000000000001"

// hotspotsOf wraps a hotspots result built by the fold tests' helpers.
func hotspotsOf(env map[string]interface{}) *Hotspots {
	return newHotspots(&sdkcla.Response{Status: "completed", Kind: sdkcla.KindHotspots, EntityID: testEntity, Result: env})
}

func allocationsOf(env map[string]interface{}) *Allocations {
	return newAllocations(&sdkcla.Response{Status: "completed", Kind: sdkcla.KindAllocations, EntityID: testEntity, Result: env})
}

func survnode(id float64, name string, app bool, alloc, surv float64, children ...interface{}) map[string]interface{} {
	n := memnode(id, name, app, alloc, 1, children...)
	n["allocationInfo"].(map[string]interface{})["survivorSize"] = surv
	return n
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

// analysisAPI is an environment that answers the submit of each kind with a
// progress token and the second poll with a result.
func analysisAPI(t *testing.T) (*Handler, *[]string) {
	t.Helper()
	const hotspots = `{"dataRootNodeId":"0","timeseries":{"dataPoints":[1,2,3]},"dataNodes":[
	 {"id":"0","methodName":"","color":"#fff","uuid":"u","fileName":null,"samples":{"RUNNING":0},"childIds":["A"]},
	 {"id":"A","className":"app.Main","methodName":"main","apiInfo":{"systemApi":true},"samples":{"RUNNING":7},"childIds":[]}]}`
	const allocations = `{"analysisResult":{"stacktreeRootIds":[1],"stacktreeNodes":[
	 {"id":1,"name":"Cache.put","childIds":[],"apiInfo":{"systemApi":true},"allocationInfo":{"allocationSize":100,"survivorSize":500}}]}}`
	var requests []string
	polls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests = append(requests, r.URL.RequestURI())
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.Contains(r.URL.Path, "/result"):
			if polls++; polls < 2 {
				w.WriteHeader(http.StatusAccepted)
				return
			}
			fmt.Fprint(w, hotspots)
		case strings.Contains(r.URL.Path, "/methodhotspots/"):
			w.WriteHeader(http.StatusAccepted)
			fmt.Fprint(w, `{"token":"t1"}`)
		case strings.Contains(r.URL.Path, "/memoryallocation/"):
			fmt.Fprint(w, allocations)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	c, err := client.NewForTesting(srv.URL, "dt0c01.test")
	if err != nil {
		t.Fatal(err)
	}
	return NewHandler(c), &requests
}

// Each kind has its own request and its own result, so a request cannot name a
// kind the API does not have and a result cannot be read as the other kind.
func TestHandler_Hotspots(t *testing.T) {
	h, requests := analysisAPI(t)
	got, err := h.Hotspots(context.Background(), HotspotsRequest{
		Entity: testEntity, Window: Window{From: 1000, To: 2000}, LeafType: LeafTypeService, ShowWaiting: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if got.Entity != testEntity || got.Status != "completed" || got.PollCount != 2 {
		t.Errorf("got entity %q, status %q, %d polls; want %q, completed, 2", got.Entity, got.Status, got.PollCount, testEntity)
	}
	const submit = "/platform-reserved/codelevelanalysis/v0.1/methodhotspots/PROCESS_GROUP_INSTANCE-0000000000000001?from=1000&servicefilter=42%111&showWaiting=true&to=2000"
	if len(*requests) != 3 || (*requests)[0] != submit {
		t.Errorf("requests = %q, want the submit %q and two polls", *requests, submit)
	}
	want := StackView{Rows: []StackRow{{Running: 7, Stack: "app.Main.main"}}, Samples: 7}
	if view := got.Stacks(false); !reflect.DeepEqual(view, want) {
		t.Errorf("Stacks = %+v, want %+v", view, want)
	}

	// Raw is the document the API sent under the envelope keys, without the
	// data points and the render-only fields.
	wantRaw := map[string]interface{}{
		"status": "completed", "kind": "methodHotspots", "entityId": testEntity, "pollCount": float64(2),
		"result": map[string]interface{}{
			"dataRootNodeId": "0",
			"timeseries":     map[string]interface{}{"dataPointsCount": float64(3)},
			"dataNodes": []interface{}{
				map[string]interface{}{"id": "0", "methodName": "", "samples": map[string]interface{}{"RUNNING": float64(0)}, "childIds": []interface{}{"A"}},
				map[string]interface{}{"id": "A", "className": "app.Main", "methodName": "main", "apiInfo": map[string]interface{}{"systemApi": true},
					"samples": map[string]interface{}{"RUNNING": float64(7)}, "childIds": []interface{}{}},
			},
		},
	}
	if raw := got.Raw(); !reflect.DeepEqual(raw, wantRaw) {
		t.Errorf("Raw() = %#v\nwant    %#v", raw, wantRaw)
	}
}

func TestHandler_Allocations(t *testing.T) {
	h, requests := analysisAPI(t)
	got, err := h.Allocations(context.Background(), AllocationsRequest{
		Entity: "PROCESS_GROUP-0000000000000001", Window: Window{From: 1000, To: 2000}, SurvivorsOnly: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if got.Entity != "PROCESS_GROUP-0000000000000001" || got.Status != "completed" || got.PollCount != 0 {
		t.Errorf("got entity %q, status %q, %d polls; want the group, completed, 0", got.Entity, got.Status, got.PollCount)
	}
	const submit = "/platform-reserved/codelevelanalysis/v0.1/memoryallocation/PROCESS_GROUP-0000000000000001?from=1000&survivorsOnly=true&to=2000"
	if len(*requests) != 1 || (*requests)[0] != submit {
		t.Errorf("requests = %q, want only %q", *requests, submit)
	}
	want := AllocationView{Rows: []AllocationRow{{AllocBytes: 100, SurvivedBytes: 500, Stack: "Cache.put"}}, AllocatedBytes: 100, SurvivedBytes: 500}
	if view := got.Stacks(false, false); !reflect.DeepEqual(view, want) {
		t.Errorf("Stacks = %+v, want %+v", view, want)
	}
	if kind := got.Raw()["kind"]; kind != "memoryAllocation" {
		t.Errorf(`Raw()["kind"] = %v, want the API's name, memoryAllocation`, kind)
	}
}

// A result without data keeps its envelope and has no rows.
func TestRaw_NoData(t *testing.T) {
	a := newAllocations(&sdkcla.Response{Status: "no-data", Kind: sdkcla.KindAllocations, EntityID: testEntity, PollCount: 1})
	want := map[string]interface{}{"status": "no-data", "kind": "memoryAllocation", "entityId": testEntity, "pollCount": float64(1), "result": nil}
	if raw := a.Raw(); !reflect.DeepEqual(raw, want) {
		t.Errorf("Raw() = %#v, want %#v", raw, want)
	}
	if view := a.Stacks(false, false); view.Rows != nil || view.AllocatedBytes != 0 || view.SurvivedBytes != 0 {
		t.Errorf("Stacks = %+v, want no rows", view)
	}
}

// The response a result is built from is copied, not compacted in place.
func TestNewHotspots_LeavesTheResponseAlone(t *testing.T) {
	var result interface{}
	if err := json.Unmarshal([]byte(`{"dataRootNodeId":"0","dataNodes":[{"id":"0","methodName":"m","color":"#fff","samples":{"RUNNING":1},"childIds":[]}]}`), &result); err != nil {
		t.Fatal(err)
	}
	newHotspots(&sdkcla.Response{Status: "completed", Kind: sdkcla.KindHotspots, EntityID: testEntity, Result: result})
	root := result.(map[string]interface{})["dataNodes"].([]interface{})[0].(map[string]interface{})
	if root["color"] != "#fff" {
		t.Errorf("the response was modified: %v", root)
	}
}

func TestHotspots_Stacks(t *testing.T) {
	h := hotspotsOf(fnTree())

	want := StackView{Samples: 100, Rows: []StackRow{
		{Running: 30, Stack: "app.main;lib.load"},
		{Running: 30, Stack: "app.main;lib.load;lib.wrap"},
		{Running: 20, Lock: 5, Stack: "app.main;app.handle;lib.wrap"},
		{Running: 10, Stack: "app.main"},
		{Running: 10, Stack: "app.main;app.handle"},
	}}
	if got := h.Stacks(false); !reflect.DeepEqual(got, want) {
		t.Errorf("Stacks(false) = %+v\nwant %+v", got, want)
	}

	// appOnly moves a hidden frame's samples to the kept frame above it, so
	// the total stays the analysis total.
	want = StackView{Samples: 100, Rows: []StackRow{
		{Running: 70, Stack: "app.main"},
		{Running: 30, Lock: 5, Stack: "app.main;app.handle"},
	}}
	if got := h.Stacks(true); !reflect.DeepEqual(got, want) {
		t.Errorf("Stacks(true) = %+v\nwant %+v", got, want)
	}

	want = StackView{Empty: EmptyNoSamples}
	if got := hotspotsOf(hotspotsResult("0")).Stacks(false); !reflect.DeepEqual(got, want) {
		t.Errorf("empty analysis: %+v, want %+v", got, want)
	}
}

func TestAllocations_Stacks(t *testing.T) {
	a := allocationsOf(allocationsResult([]interface{}{float64(1), float64(2)},
		survnode(1, "Cache.put", true, 1000, 5000, float64(3)),
		survnode(3, "Svc.get", true, 400, 100),
		survnode(2, "Buf.grow", false, 3000, 20),
	))

	// Cache.put's own share is its total less what Svc.get reached it with.
	want := AllocationView{AllocatedBytes: 4000, SurvivedBytes: 5020, Rows: []AllocationRow{
		{AllocBytes: 3000, SurvivedBytes: 20, AllocCount: 1, Stack: "Buf.grow"},
		{AllocBytes: 600, SurvivedBytes: 4900, Stack: "Cache.put"},
		{AllocBytes: 400, SurvivedBytes: 100, AllocCount: 1, Stack: "Cache.put;Svc.get"},
	}}
	if got := a.Stacks(false, false); !reflect.DeepEqual(got, want) {
		t.Errorf("Stacks(false, false) = %+v\nwant %+v", got, want)
	}

	// The ranking changes the order, not the totals.
	bySurvivors := a.Stacks(false, true)
	if bySurvivors.AllocatedBytes != 4000 || bySurvivors.SurvivedBytes != 5020 {
		t.Errorf("totals by survivors = %d, %d; want 4000, 5020", bySurvivors.AllocatedBytes, bySurvivors.SurvivedBytes)
	}
	if got := []string{bySurvivors.Rows[0].Stack, bySurvivors.Rows[1].Stack, bySurvivors.Rows[2].Stack}; !reflect.DeepEqual(got, []string{"Cache.put", "Cache.put;Svc.get", "Buf.grow"}) {
		t.Errorf("order by survived bytes = %v", got)
	}

	want = AllocationView{AllocatedBytes: 1000, SurvivedBytes: 5000, Rows: []AllocationRow{
		{AllocBytes: 600, SurvivedBytes: 4900, Stack: "Cache.put"},
		{AllocBytes: 400, SurvivedBytes: 100, AllocCount: 1, Stack: "Cache.put;Svc.get"},
	}}
	if got := a.Stacks(true, false); !reflect.DeepEqual(got, want) {
		t.Errorf("Stacks(true, false) = %+v\nwant %+v", got, want)
	}
}

// Totals over byte counts the API could not mean are held at the largest
// int64 instead of wrapping around into a negative or a small one.
func TestAllocations_TotalsDoNotWrap(t *testing.T) {
	a := allocationsOf(allocationsResult([]interface{}{float64(1), float64(2), float64(3)},
		survnode(1, "A.a", true, 6e18, 6e18),
		survnode(2, "B.b", true, 6e18, 1),
		map[string]interface{}{"id": float64(3), "name": "C.c", "childIds": []interface{}{},
			"allocationInfo": map[string]interface{}{"allocationSize": 1e300, "allocationCount": -1e300}},
	))
	got := a.Stacks(false, false)
	if got.AllocatedBytes != math.MaxInt64 || got.SurvivedBytes != 6e18+1 {
		t.Errorf("totals = %d allocated, %d survived; want %d, %d", got.AllocatedBytes, got.SurvivedBytes, int64(math.MaxInt64), int64(6e18+1))
	}
	want := []AllocationRow{
		{AllocBytes: math.MaxInt64, Stack: "C.c"},
		{AllocBytes: 6e18, SurvivedBytes: 6e18, AllocCount: 1, Stack: "A.a"},
		{AllocBytes: 6e18, SurvivedBytes: 1, AllocCount: 1, Stack: "B.b"},
	}
	if !reflect.DeepEqual(got.Rows, want) {
		t.Errorf("rows = %+v\nwant   %+v", got.Rows, want)
	}

	// Two paths that appOnly merges into one row add up the same way.
	merged := allocationsOf(allocationsResult([]interface{}{float64(1)},
		survnode(1, "A.a", true, math.MaxInt64, 0, float64(2), float64(3)),
		survnode(2, "lib.x", false, 6e18, 0),
		survnode(3, "lib.y", false, 6e18, 0),
	)).Stacks(true, false)
	if len(merged.Rows) != 1 || merged.Rows[0].AllocBytes != math.MaxInt64 || merged.AllocatedBytes != math.MaxInt64 {
		t.Errorf("merged rows = %+v, total %d; want one row and a total of %d", merged.Rows, merged.AllocatedBytes, int64(math.MaxInt64))
	}
}

// A view without rows says why, and the reason comes from the data in a fixed
// order: no samples at all, then none in the counted state, then removed by
// appOnly. The view carries it, so it cannot be asked for with other
// arguments than the view was read with.
func TestEmptyReason(t *testing.T) {
	// Ten LOCK samples on one library frame, and nothing running.
	lockedLib := hotspotsOf(hotspotsResult("0",
		hnode("0", "", "", false, 0, 0, "A"),
		hnode("A", "lib", "take", false, 0, 10),
	))
	// Running samples on library frames only.
	libOnly := hotspotsOf(hotspotsResult("0",
		hnode("0", "", "", false, 0, 0, "A"),
		hnode("A", "lib", "run", false, 10, 0),
	))
	noData := newHotspots(&sdkcla.Response{Status: "no-data", Kind: sdkcla.KindHotspots, EntityID: testEntity})
	noNodes := hotspotsOf(hotspotsResult("0"))
	full := hotspotsOf(fnTree())

	stacks := []struct {
		name    string
		h       *Hotspots
		appOnly bool
		want    EmptyReason
	}{
		{"rows", full, false, ""},
		{"rows, app-only", full, true, ""},
		{"no data", noData, false, EmptyNoSamples},
		{"no data, app-only", noData, true, EmptyNoSamples},
		{"completed without nodes, app-only", noNodes, true, EmptyNoSamples},
		{"only library frames", libOnly, false, ""},
		{"only library frames, app-only", libOnly, true, EmptyAppOnly},
		{"only lock samples", lockedLib, false, ""},
		{"only lock samples, app-only", lockedLib, true, EmptyAppOnly},
	}
	for _, tc := range stacks {
		t.Run("stacks: "+tc.name, func(t *testing.T) {
			view := tc.h.Stacks(tc.appOnly)
			if view.Empty != tc.want {
				t.Errorf("Empty = %q, want %q", view.Empty, tc.want)
			}
			if (len(view.Rows) == 0) != (tc.want != "") {
				t.Errorf("%d rows with Empty = %q", len(view.Rows), view.Empty)
			}
		})
	}

	functions := []struct {
		name    string
		h       *Hotspots
		appOnly bool
		state   State
		want    EmptyReason
	}{
		{"rows", full, false, "running", ""},
		{"rows in another state", full, true, "lock", ""},
		// The analysis has no samples at all: that, whatever the state.
		{"no data, lock", noData, false, "lock", EmptyNoSamples},
		{"no data, lock, app-only", noData, true, "lock", EmptyNoSamples},
		{"no data, running", noData, false, "running", EmptyNoSamples},
		{"completed without nodes, all", noNodes, false, "all", EmptyNoSamples},
		// There are samples, but none in the counted state.
		{"a state without samples", full, false, "disk_io", EmptyState},
		{"a state without samples, app-only", full, true, "disk_io", EmptyState},
		{"running, with only lock samples", lockedLib, false, "running", EmptyState},
		{"running, with only lock samples, app-only", lockedLib, true, "running", EmptyState},
		{"lock, with only running samples, app-only", libOnly, true, "lock", EmptyState},
		// The state has samples, and appOnly removed every frame under them.
		{"lock on a library frame, app-only", lockedLib, true, "lock", EmptyAppOnly},
		{"offcpu on a library frame, app-only", lockedLib, true, "offcpu", EmptyAppOnly},
		{"running on library frames, app-only", libOnly, true, "running", EmptyAppOnly},
		{"lock on a library frame", lockedLib, false, "lock", ""},
	}
	for _, tc := range functions {
		t.Run("functions: "+tc.name, func(t *testing.T) {
			view, err := tc.h.Functions(tc.appOnly, tc.state, SortBySelf)
			if err != nil {
				t.Fatal(err)
			}
			if view.Empty != tc.want {
				t.Errorf("Empty = %q, want %q", view.Empty, tc.want)
			}
			if (len(view.Rows) == 0) != (tc.want != "") {
				t.Errorf("%d rows with Empty = %q", len(view.Rows), view.Empty)
			}
			// The focus view of the same state and filter is empty for
			// the same reason.
			focus, err := tc.h.Focus(tc.appOnly, tc.state, "lib.take")
			if tc.want != "" && (err != nil || focus.Empty != tc.want) {
				t.Errorf("Focus: Empty = %q, err = %v; want %q", focus.Empty, err, tc.want)
			}
		})
	}

	libAllocations := allocationsOf(allocationsResult([]interface{}{float64(1)}, survnode(1, "Buf.grow", false, 10, 0)))
	allocations := []struct {
		name    string
		a       *Allocations
		appOnly bool
		want    EmptyReason
	}{
		{"rows", libAllocations, false, ""},
		{"only library frames, app-only", libAllocations, true, EmptyAppOnly},
		{"nothing allocated", allocationsOf(allocationsResult([]interface{}{})), false, EmptyNoSamples},
		{"nothing allocated, app-only", allocationsOf(allocationsResult([]interface{}{})), true, EmptyNoSamples},
		{"no data", newAllocations(&sdkcla.Response{Status: "no-data", Kind: sdkcla.KindAllocations, EntityID: testEntity}), true, EmptyNoSamples},
	}
	for _, tc := range allocations {
		t.Run("allocations: "+tc.name, func(t *testing.T) {
			view := tc.a.Stacks(tc.appOnly, false)
			if view.Empty != tc.want {
				t.Errorf("Empty = %q, want %q", view.Empty, tc.want)
			}
			if (len(view.Rows) == 0) != (tc.want != "") {
				t.Errorf("%d rows with Empty = %q", len(view.Rows), view.Empty)
			}
		})
	}
}

// Raw hands out a document of the caller's own: changing it does not reach the
// result's views or a later Raw.
func TestRaw_IsTheCallersToChange(t *testing.T) {
	h := hotspotsOf(fnTree())
	raw := h.Raw()
	nodes := raw["result"].(map[string]interface{})["dataNodes"].([]interface{})
	nodes[1].(map[string]interface{})["samples"].(map[string]interface{})["RUNNING"] = float64(0)
	delete(raw, "result")

	if got := h.Stacks(false); len(got.Rows) != 5 || got.Samples != 100 {
		t.Errorf("after the caller changed Raw(): %d rows, %d samples; want 5, 100", len(got.Rows), got.Samples)
	}
	again, _ := h.Raw()["result"].(map[string]interface{})
	if again == nil {
		t.Fatal("a later Raw() lost the result the caller deleted from an earlier one")
	}
	main := again["dataNodes"].([]interface{})[1].(map[string]interface{})["samples"].(map[string]interface{})
	if main["RUNNING"] != float64(100) {
		t.Errorf("a later Raw() carries the caller's change: %v", main)
	}

	a := allocationsOf(allocationsResult([]interface{}{float64(1)}, survnode(1, "Cache.put", true, 100, 500)))
	delete(a.Raw(), "result")
	if got := a.Stacks(false, false); got.AllocatedBytes != 100 {
		t.Errorf("after the caller changed Raw(): %+v", got)
	}
}

// A result is safe for concurrent readers, Raw included. Run with -race.
func TestResults_ConcurrentReaders(t *testing.T) {
	h := hotspotsOf(fnTree())
	a := allocationsOf(allocationsResult([]interface{}{float64(1)}, survnode(1, "Cache.put", true, 100, 500)))
	done := make(chan struct{})
	for i := 0; i < 8; i++ {
		go func() {
			defer func() { done <- struct{}{} }()
			for j := 0; j < 50; j++ {
				delete(h.Raw(), "result")
				delete(a.Raw()["result"].(map[string]interface{}), "analysisResult")
				// What a view returns is the caller's: every reader changes
				// its rows, and none sees another's change.
				stacks := h.Stacks(j%2 == 0)
				if stacks.Samples != 100 || stacks.Rows[0].Running == 0 {
					t.Errorf("Stacks: %+v, want 100 samples and a first row with some", stacks)
				}
				stacks.Rows[0] = StackRow{}
				fns, err := h.Functions(false, StateRunning, SortBySelf)
				if err != nil || fns.Samples != 100 || fns.Rows[0].Function != "lib.wrap" {
					t.Errorf("Functions: %+v, %v", fns, err)
				}
				fns.Rows[0].Function = "changed"
				focus, err := h.Focus(false, StateRunning, "wrap")
				if err != nil || focus.Function != "lib.wrap" || len(focus.Callers) != 2 || focus.Callers[0].Function != "lib.load" {
					t.Errorf("Focus: %+v, %v", focus, err)
				}
				focus.Callers[0].Function = "changed"
				focus.Rows(0)[0].Function = "changed"
				allocs := a.Stacks(false, false)
				if allocs.AllocatedBytes != 100 || allocs.Rows[0].Stack != "Cache.put" {
					t.Errorf("Allocations.Stacks: %+v", allocs)
				}
				allocs.Rows[0].Stack = "changed"
			}
		}()
	}
	for i := 0; i < 8; i++ {
		<-done
	}
}

// A handler takes the windows ResolveWindow gives: one that ends after its
// start and is at most MaxWindow long. Any other is refused with
// ResolveWindow's own errors before a request is sent, so a Window built by
// hand cannot ask for more than the command can.
func TestHandler_RefusesAWindowItDoesNotTake(t *testing.T) {
	h, requests := analysisAPI(t)
	ctx := context.Background()
	sixHours := Window{From: 1_700_000_000_000, To: 1_700_000_000_000 + 6*3600*1000}

	_, err := h.Hotspots(ctx, HotspotsRequest{Entity: testEntity, Window: sixHours})
	var tooLong *WindowTooLongError
	if !errors.As(err, &tooLong) || tooLong.Requested != sixHours || tooLong.Max != MaxWindow {
		t.Errorf("Hotspots over six hours: err = %v, want a *WindowTooLongError for %+v over %s", err, sixHours, MaxWindow)
	}
	_, err = h.Allocations(ctx, AllocationsRequest{Entity: testEntity, Window: sixHours})
	if !errors.As(err, &tooLong) {
		t.Errorf("Allocations over six hours: err = %v, want a *WindowTooLongError", err)
	}
	for _, w := range []Window{{}, {From: 5, To: 5}, {From: 2000, To: 1000}} {
		if _, err := h.Hotspots(ctx, HotspotsRequest{Entity: testEntity, Window: w}); !errors.Is(err, ErrWindowEmpty) {
			t.Errorf("Hotspots over %+v: err = %v, want ErrWindowEmpty", w, err)
		}
		if _, err := h.Allocations(ctx, AllocationsRequest{Entity: testEntity, Window: w}); !errors.Is(err, ErrWindowEmpty) {
			t.Errorf("Allocations over %+v: err = %v, want ErrWindowEmpty", w, err)
		}
	}
	if len(*requests) != 0 {
		t.Errorf("requests = %q, want none for a window the handler refuses", *requests)
	}

	// Exactly MaxWindow is taken, and so is a window that starts at the epoch.
	if _, err := h.Allocations(ctx, AllocationsRequest{Entity: testEntity, Window: Window{From: 0, To: MaxWindow.Milliseconds()}}); err != nil {
		t.Fatalf("Allocations over the first MaxWindow of the epoch: %v", err)
	}
	const submit = "/platform-reserved/codelevelanalysis/v0.1/memoryallocation/PROCESS_GROUP_INSTANCE-0000000000000001?from=0&to=7200000"
	if len(*requests) != 1 || (*requests)[0] != submit {
		t.Errorf("requests = %q, want only %q", *requests, submit)
	}
}

// Every number of the document Raw returns is a float64, as in any decoded
// JSON document, also the two it writes itself.
func TestRaw_NumbersAreFloat64(t *testing.T) {
	var result interface{}
	if err := json.Unmarshal([]byte(`{"timeseries":{"dataPoints":[1,2,3]},"dataNodes":[]}`), &result); err != nil {
		t.Fatal(err)
	}
	raw := rawEnvelope(sdkcla.KindHotspots, testEntity, "completed", 2, result)
	if _, ok := raw["pollCount"].(float64); !ok {
		t.Errorf("pollCount is a %T, want a float64", raw["pollCount"])
	}
	got := raw["result"].(map[string]interface{})["timeseries"].(map[string]interface{})["dataPointsCount"]
	if got != float64(3) {
		t.Errorf("dataPointsCount = %#v, want float64(3)", got)
	}
}

// A document that holds both the data points and a count of them (one that
// went through Raw once and had points added) gets the count of its points,
// every time.
func TestCompactResult_CountsTheDataPointsItDrops(t *testing.T) {
	for i := 0; i < 200; i++ {
		got := compactResult(map[string]interface{}{"dataPoints": []interface{}{1.0, 2.0}, "dataPointsCount": float64(9)})
		if want := map[string]interface{}{"dataPointsCount": float64(2)}; !reflect.DeepEqual(got, want) {
			t.Fatalf("compactResult = %#v, want %#v", got, want)
		}
	}
}

// A completed analysis whose result is not a JSON object is not an analysis.
// Read as one it would have no rows in any view, and so pass for a process
// that was idle.
func TestHandler_RefusesAResultThatIsNoObject(t *testing.T) {
	body := ""
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, body)
	}))
	defer srv.Close()
	c, err := client.NewForTesting(srv.URL, "dt0c01.test")
	if err != nil {
		t.Fatal(err)
	}
	h := NewHandler(c)
	window := Window{From: 1000, To: 2000}
	for in, want := range map[string]string{
		`[1,2]`:  "profiling: malformed analysis result: the result of a completed analysis is an array, not an object",
		`"gone"`: "profiling: malformed analysis result: the result of a completed analysis is a string, not an object",
		`null`:   "profiling: malformed analysis result: the result of a completed analysis is null, not an object",
		`7`:      "profiling: malformed analysis result: the result of a completed analysis is a number, not an object",
	} {
		body = in
		hs, err := h.Hotspots(context.Background(), HotspotsRequest{Entity: testEntity, Window: window})
		if hs != nil || !errors.Is(err, ErrMalformedResult) || err.Error() != want {
			t.Errorf("Hotspots answered with %s: %v, %v; want the error %q", in, hs, err, want)
		}
		a, err := h.Allocations(context.Background(), AllocationsRequest{Entity: testEntity, Window: window})
		if a != nil || !errors.Is(err, ErrMalformedResult) || err.Error() != want {
			t.Errorf("Allocations answered with %s: %v, %v; want the error %q", in, a, err, want)
		}
		if _, ok := ClassifyError(err); ok {
			t.Errorf("ClassifyError classifies %v, which is not a failure a caller can act on", err)
		}
	}
	body = `{}`
	if hs, err := h.Hotspots(context.Background(), HotspotsRequest{Entity: testEntity, Window: window}); err != nil || hs.Stacks(false).Empty != EmptyNoSamples {
		t.Errorf("Hotspots answered with an empty object: %v, want a result without samples", err)
	}
}
