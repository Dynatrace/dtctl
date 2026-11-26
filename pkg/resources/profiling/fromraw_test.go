package profiling_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/Dynatrace/dtctl/pkg/client"
	"github.com/Dynatrace/dtctl/pkg/resources/profiling"
)

const (
	rawEntity = "PROCESS-0000000000000001"

	rawHotspots = `{"dataRootNodeId":"1","timeseries":{"dataPoints":[1,2,3]},"dataNodes":[
		{"id":"1","className":"app.Main","methodName":"main","apiInfo":{"systemApi":true},"samples":{"RUNNING":10,"LOCK":3},"childIds":["2"]},
		{"id":"2","className":"lib.Json","methodName":"parse","apiInfo":{"systemApi":false},"samples":{"RUNNING":8},"childIds":[]}]}`
	rawAllocations = `{"analysisResult":{"stacktreeRootIds":[1],"stacktreeNodes":[
		{"id":1,"name":"Cache.put","childIds":[2],"apiInfo":{"systemApi":true},"allocationInfo":{"allocationSize":100,"survivorSize":500,"allocationCount":4}},
		{"id":2,"name":"Svc.get","childIds":[],"apiInfo":{"systemApi":true},"allocationInfo":{"allocationSize":40,"survivorSize":10,"allocationCount":1}}]}}`
)

// analyses runs one analysis of each kind against a stand-in environment.
func analyses(t *testing.T) (*profiling.Hotspots, *profiling.Allocations) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.Contains(r.URL.Path, "/memoryallocation/") {
			fmt.Fprint(w, rawAllocations)
			return
		}
		fmt.Fprint(w, rawHotspots)
	}))
	t.Cleanup(srv.Close)
	c, err := client.NewForTesting(srv.URL, "dt0c01.test")
	if err != nil {
		t.Fatal(err)
	}
	h := profiling.NewHandler(c)
	window := profiling.Window{From: 1000, To: 2000}
	hs, err := h.Hotspots(context.Background(), profiling.HotspotsRequest{Entity: rawEntity, Window: window})
	if err != nil {
		t.Fatal(err)
	}
	a, err := h.Allocations(context.Background(), profiling.AllocationsRequest{Entity: rawEntity, Window: window})
	if err != nil {
		t.Fatal(err)
	}
	return hs, a
}

// throughJSON is doc as a program that kept it as JSON reads it back.
func throughJSON(t *testing.T, doc map[string]interface{}) map[string]interface{} {
	t.Helper()
	data, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	var out map[string]interface{}
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatal(err)
	}
	return out
}

// An analysis that was kept as the JSON of its Raw document reads the same
// when it is rebuilt from that document, without an environment to ask.
func TestFromRaw_RoundTrip(t *testing.T) {
	hs, a := analyses(t)

	doc := throughJSON(t, hs.Raw())
	again, err := profiling.HotspotsFromRaw(doc)
	if err != nil {
		t.Fatal(err)
	}
	if again.Entity != rawEntity || again.Status != "completed" || again.PollCount != 0 {
		t.Errorf("rebuilt hotspots: entity %q, status %q, %d polls; want %q, completed, 0", again.Entity, again.Status, again.PollCount, rawEntity)
	}
	wantStacks := profiling.StackView{Samples: 10, Rows: []profiling.StackRow{
		{Running: 8, Stack: "app.Main.main;lib.Json.parse"},
		{Running: 2, Lock: 3, Stack: "app.Main.main"},
	}}
	if got := again.Stacks(false); !reflect.DeepEqual(got, wantStacks) || !reflect.DeepEqual(hs.Stacks(false), wantStacks) {
		t.Errorf("rebuilt Stacks = %+v\nwant %+v", got, wantStacks)
	}
	fns, err := again.Functions(true, profiling.StateRunning, profiling.SortBySelf)
	wantFns := profiling.FunctionView{Samples: 10, Rows: []profiling.FunctionRow{
		{Self: 10, SelfPct: 100, Total: 10, TotalPct: 100, Paths: 1, Function: "app.Main.main"},
	}}
	if err != nil || !reflect.DeepEqual(fns, wantFns) {
		t.Errorf("rebuilt Functions = %+v, %v\nwant %+v", fns, err, wantFns)
	}
	if !reflect.DeepEqual(again.Raw(), doc) {
		t.Errorf("rebuilt Raw() = %#v\nwant %#v", again.Raw(), doc)
	}
	// Straight from Raw, without the detour through JSON.
	if direct, err := profiling.HotspotsFromRaw(hs.Raw()); err != nil || !reflect.DeepEqual(direct.Stacks(false), wantStacks) {
		t.Errorf("HotspotsFromRaw(Raw()): %v", err)
	}

	// The result holds a copy: the document stays the caller's to change.
	doc["result"].(map[string]interface{})["dataNodes"] = []interface{}{}
	doc["entityId"] = "PROCESS-CHANGED"
	if got := again.Stacks(false); !reflect.DeepEqual(got, wantStacks) || again.Entity != rawEntity {
		t.Errorf("after the caller changed its document: %+v for %q", got, again.Entity)
	}

	adoc := throughJSON(t, a.Raw())
	allocs, err := profiling.AllocationsFromRaw(adoc)
	if err != nil {
		t.Fatal(err)
	}
	wantAllocs := profiling.AllocationView{AllocatedBytes: 100, SurvivedBytes: 500, Rows: []profiling.AllocationRow{
		{AllocBytes: 60, SurvivedBytes: 490, AllocCount: 3, Stack: "Cache.put"},
		{AllocBytes: 40, SurvivedBytes: 10, AllocCount: 1, Stack: "Cache.put;Svc.get"},
	}}
	if got := allocs.Stacks(false, false); !reflect.DeepEqual(got, wantAllocs) || !reflect.DeepEqual(a.Stacks(false, false), wantAllocs) {
		t.Errorf("rebuilt allocation Stacks = %+v\nwant %+v", got, wantAllocs)
	}
	if !reflect.DeepEqual(allocs.Raw(), adoc) {
		t.Errorf("rebuilt allocation Raw() = %#v\nwant %#v", allocs.Raw(), adoc)
	}
}

func decode(t *testing.T, doc string) map[string]interface{} {
	t.Helper()
	var out map[string]interface{}
	if err := json.Unmarshal([]byte(doc), &out); err != nil {
		t.Fatal(err)
	}
	return out
}

// An analysis without data is a document too.
func TestFromRaw_NoData(t *testing.T) {
	hs, err := profiling.HotspotsFromRaw(decode(t, `{"status":"no-data","kind":"methodHotspots","entityId":"PROCESS-0000000000000001","pollCount":3,"result":null}`))
	if err != nil {
		t.Fatal(err)
	}
	if hs.Status != "no-data" || hs.PollCount != 3 || hs.Entity != rawEntity {
		t.Errorf("rebuilt: status %q, %d polls, entity %q; want no-data, 3, %q", hs.Status, hs.PollCount, hs.Entity, rawEntity)
	}
	if got := hs.Stacks(false); len(got.Rows) != 0 || got.Empty != profiling.EmptyNoSamples {
		t.Errorf("Stacks = %+v, want no rows and %q", got, profiling.EmptyNoSamples)
	}
	a, err := profiling.AllocationsFromRaw(decode(t, `{"status":"no-data","kind":"memoryAllocation","entityId":"PROCESS-0000000000000001","pollCount":0}`))
	if err != nil {
		t.Fatal(err)
	}
	if got := a.Stacks(false, false); len(got.Rows) != 0 || got.Empty != profiling.EmptyNoSamples {
		t.Errorf("allocation Stacks = %+v, want no rows and %q", got, profiling.EmptyNoSamples)
	}
}

// A document that is not the analysis asked for is refused, not read as an
// analysis without samples.
func TestFromRaw_Malformed(t *testing.T) {
	cases := []struct{ name, doc, want string }{
		{"the other kind", `{"status":"completed","kind":"memoryAllocation","entityId":"PROCESS-1","pollCount":0,"result":{}}`,
			`profiling: malformed analysis result: kind "memoryAllocation", want methodHotspots`},
		{"no kind: the API's bare result", `{"dataRootNodeId":"1","dataNodes":[]}`,
			`profiling: malformed analysis result: kind "", want methodHotspots`},
		{"a result that is an array", `{"status":"completed","kind":"methodHotspots","result":[1]}`,
			`profiling: malformed analysis result: the result of a completed analysis is an array, not an object`},
		{"completed without a result", `{"status":"completed","kind":"methodHotspots"}`,
			`profiling: malformed analysis result: the result of a completed analysis is null, not an object`},
		{"no data with a result", `{"status":"no-data","kind":"methodHotspots","result":{}}`,
			`profiling: malformed analysis result: an analysis without data carries a result`},
		{"an unknown status", `{"status":"running","kind":"methodHotspots","result":{}}`,
			`profiling: malformed analysis result: status "running", want completed or no-data`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			hs, err := profiling.HotspotsFromRaw(decode(t, tc.doc))
			if hs != nil || !errors.Is(err, profiling.ErrMalformedResult) || err.Error() != tc.want {
				t.Errorf("HotspotsFromRaw = %v, %v\nwant the error %q", hs, err, tc.want)
			}
		})
	}
	if a, err := profiling.AllocationsFromRaw(decode(t, `{"status":"completed","kind":"methodHotspots","result":{}}`)); a != nil || !errors.Is(err, profiling.ErrMalformedResult) {
		t.Errorf("AllocationsFromRaw of a hotspots document = %v, %v; want ErrMalformedResult", a, err)
	}
	if hs, err := profiling.HotspotsFromRaw(nil); hs != nil || !errors.Is(err, profiling.ErrMalformedResult) {
		t.Errorf("HotspotsFromRaw(nil) = %v, %v; want ErrMalformedResult", hs, err)
	}
}
