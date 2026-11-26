package profiling

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/Dynatrace/dtctl/pkg/client"
	sdkcla "github.com/Dynatrace/dtctl/sdk/api/codelevelanalysis"
)

const testEntity = "PROCESS-0000000000000001"

// hotspotsOf wraps a hotspots envelope built by the fold tests' helpers.
func hotspotsOf(env map[string]interface{}) *Hotspots {
	return newHotspots(&sdkcla.Response{Status: "completed", Kind: sdkcla.KindHotspots, EntityID: testEntity, Result: env["result"]})
}

func allocationsOf(env map[string]interface{}) *Allocations {
	return newAllocations(&sdkcla.Response{Status: "completed", Kind: sdkcla.KindAllocations, EntityID: testEntity, Result: env["result"]})
}

func survnode(id float64, name string, app bool, alloc, surv int, children ...interface{}) map[string]interface{} {
	n := memnode(id, name, app, alloc, 1, children...)
	n["allocationInfo"].(map[string]interface{})["survivorSize"] = float64(surv)
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
			"timeseries":     map[string]interface{}{"dataPointsCount": 3},
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

	if got := hotspotsOf(envelope("0")).Stacks(false); got.Rows != nil || got.Samples != 0 {
		t.Errorf("empty analysis: %+v", got)
	}
}

func TestAllocations_Stacks(t *testing.T) {
	a := allocationsOf(memEnvelope([]interface{}{float64(1), float64(2)},
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

func TestWhyEmpty(t *testing.T) {
	// A tree whose frames are all library code.
	libOnly := envelope("0",
		hnode("0", "", "", false, 0, 0, "A"),
		hnode("A", "lib", "run", false, 10, 0),
	)
	noData := newHotspots(&sdkcla.Response{Status: "no-data", Kind: sdkcla.KindHotspots, EntityID: testEntity})
	const (
		noSamples = "no stacks for " + testEntity + ": no profiling samples in the window (status=%s) — the process may be idle, restarted, or not under load; try another window (at most 2h), or --raw for the API's whole result"
		appOnly   = "no stacks for " + testEntity + ": --app-only removed every frame (the profiler classified none as application code) — re-run without --app-only"
		noState   = "no %s samples for " + testEntity + " in the window — the stack view (without --view/--focus) shows every thread state; waiting states may need --show-waiting"
	)
	cases := []struct {
		name    string
		h       *Hotspots
		appOnly bool
		state   string
		reason  EmptyReason
		note    string
	}{
		{name: "no data at all", h: noData, reason: EmptyNoSamples, note: fmt.Sprintf(noSamples, "no-data")},
		{name: "no data, app-only", h: noData, appOnly: true, reason: EmptyNoSamples, note: fmt.Sprintf(noSamples, "no-data")},
		{name: "completed without nodes", h: hotspotsOf(envelope("0")), reason: EmptyNoSamples, note: fmt.Sprintf(noSamples, "completed")},
		{name: "app-only removed every frame", h: hotspotsOf(libOnly), appOnly: true, reason: EmptyAppOnly, note: appOnly},
		{name: "running state reads like the stack view", h: hotspotsOf(libOnly), appOnly: true, state: "running", reason: EmptyAppOnly, note: appOnly},
		{name: "a state without samples", h: hotspotsOf(fnTree()), state: "disk_io", reason: EmptyState, note: fmt.Sprintf(noState, "disk_io")},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			reason, note := tc.h.WhyEmpty(tc.appOnly, tc.state)
			if reason != tc.reason {
				t.Errorf("reason = %q, want %q", reason, tc.reason)
			}
			if note != tc.note {
				t.Errorf("note = %q\nwant   %q", note, tc.note)
			}
		})
	}

	t.Run("allocations, app-only removed every frame", func(t *testing.T) {
		a := allocationsOf(memEnvelope([]interface{}{float64(1)}, survnode(1, "Buf.grow", false, 10, 0)))
		if reason, note := a.WhyEmpty(true); reason != EmptyAppOnly || note != appOnly {
			t.Errorf("WhyEmpty = %q, %q", reason, note)
		}
	})
	t.Run("allocations, nothing allocated", func(t *testing.T) {
		a := allocationsOf(memEnvelope([]interface{}{}))
		if reason, note := a.WhyEmpty(false); reason != EmptyNoSamples || note != fmt.Sprintf(noSamples, "completed") {
			t.Errorf("WhyEmpty = %q, %q", reason, note)
		}
	})
}
