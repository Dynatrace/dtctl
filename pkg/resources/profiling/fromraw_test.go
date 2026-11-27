package profiling_test

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

	"gopkg.in/yaml.v3"

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

// decodeNumbers reads doc the way a program that wants exact integers does:
// every number a json.Number.
func decodeNumbers(t *testing.T, doc string) map[string]interface{} {
	t.Helper()
	dec := json.NewDecoder(strings.NewReader(doc))
	dec.UseNumber()
	var out map[string]interface{}
	if err := dec.Decode(&out); err != nil {
		t.Fatal(err)
	}
	return out
}

// A kept document reads the same whichever way its JSON was decoded. Read as
// they are, the json.Number values of Decoder.UseNumber would count for
// nothing: the document of a busy process would pass for one of an idle one.
func TestFromRaw_NumbersDecodedAsJSONNumber(t *testing.T) {
	hs, a := analyses(t)
	hdoc, err := json.Marshal(hs.Raw())
	if err != nil {
		t.Fatal(err)
	}
	hdoc = []byte(strings.Replace(string(hdoc), `"pollCount":0`, `"pollCount":2`, 1))
	plain, err := profiling.HotspotsFromRaw(decode(t, string(hdoc)))
	if err != nil {
		t.Fatal(err)
	}
	numbered, err := profiling.HotspotsFromRaw(decodeNumbers(t, string(hdoc)))
	if err != nil {
		t.Fatal(err)
	}
	if plain.PollCount != 2 || numbered.PollCount != 2 {
		t.Errorf("poll counts %d and %d, want 2 and 2", plain.PollCount, numbered.PollCount)
	}
	if want, got := plain.Stacks(false), numbered.Stacks(false); got.Samples != 10 || !reflect.DeepEqual(got, want) {
		t.Errorf("Stacks of the json.Number document = %+v\nwant %+v", got, want)
	}
	// Every number of the document handed out is a float64 again.
	if !reflect.DeepEqual(numbered.Raw(), plain.Raw()) {
		t.Errorf("Raw() of the json.Number document = %#v\nwant %#v", numbered.Raw(), plain.Raw())
	}

	adoc, err := json.Marshal(a.Raw())
	if err != nil {
		t.Fatal(err)
	}
	aPlain, err := profiling.AllocationsFromRaw(decode(t, string(adoc)))
	if err != nil {
		t.Fatal(err)
	}
	aNumbered, err := profiling.AllocationsFromRaw(decodeNumbers(t, string(adoc)))
	if err != nil {
		t.Fatal(err)
	}
	if want, got := aPlain.Stacks(false, false), aNumbered.Stacks(false, false); got.AllocatedBytes != 100 || !reflect.DeepEqual(got, want) {
		t.Errorf("allocation Stacks of the json.Number document = %+v\nwant %+v", got, want)
	}

	// A number no float64 holds is the largest count, not an error and not
	// zero; one that is no number is not a document Raw wrote.
	huge := decodeNumbers(t, `{"status":"completed","kind":"methodHotspots","entityId":"PROCESS-1","pollCount":0,"result":{"dataRootNodeId":"1","dataNodes":[
		{"id":"1","className":"C","methodName":"m","samples":{"RUNNING":1e999},"childIds":[]}]}}`)
	over, err := profiling.HotspotsFromRaw(huge)
	if err != nil {
		t.Fatal(err)
	}
	if got := over.Stacks(false); len(got.Rows) != 1 || got.Rows[0].Running != math.MaxInt {
		t.Errorf("Stacks of a count beyond float64 = %+v, want one row with the largest count", got)
	}
	if _, err := json.Marshal(over.Raw()); err != nil {
		t.Errorf("Raw() of a count beyond float64 cannot be kept as JSON: %v", err)
	}
	huge["pollCount"] = json.Number("many")
	if hs, err := profiling.HotspotsFromRaw(huge); hs != nil || !errors.Is(err, profiling.ErrMalformedResult) {
		t.Errorf("HotspotsFromRaw with a json.Number that is no number = %v, %v; want ErrMalformedResult", hs, err)
	}
}

// A document built in Go with types no JSON decoder produces is refused. Its
// typed slices and maps could be neither read (a []string of child ids names
// no children) nor copied, so the result would share them with the caller.
func TestFromRaw_RefusesTypesNoDecoderProduces(t *testing.T) {
	build := func(result map[string]interface{}) map[string]interface{} {
		return map[string]interface{}{"status": "completed", "kind": "methodHotspots", "entityId": "PROCESS-1", "pollCount": float64(0), "result": result}
	}
	cases := []struct {
		name string
		doc  map[string]interface{}
		want string
	}{
		{"a typed slice", build(map[string]interface{}{"dataRootNodeId": "1", "extra": []int{1, 2, 3}}),
			"profiling: malformed analysis result: result.extra has the type []int, which no JSON decoder produces"},
		{"typed child ids", build(map[string]interface{}{"dataRootNodeId": "1", "dataNodes": []interface{}{
			map[string]interface{}{"id": "1", "childIds": []string{"2"}}}}),
			"profiling: malformed analysis result: result.dataNodes[0].childIds has the type []string, which no JSON decoder produces"},
		{"a typed map", build(map[string]interface{}{"dataNodes": []interface{}{
			map[string]interface{}{"id": "1", "samples": map[string]int{"RUNNING": 7}}}}),
			"profiling: malformed analysis result: result.dataNodes[0].samples has the type map[string]int, which no JSON decoder produces"},
		{"an int", build(map[string]interface{}{"dataRootNodeId": 1}),
			"profiling: malformed analysis result: result.dataRootNodeId has the type int, which no JSON decoder produces"},
		{"an int poll count", map[string]interface{}{"status": "completed", "kind": "methodHotspots", "pollCount": 3, "result": map[string]interface{}{}},
			"profiling: malformed analysis result: pollCount has the type int, which no JSON decoder produces"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			hs, err := profiling.HotspotsFromRaw(tc.doc)
			if hs != nil || !errors.Is(err, profiling.ErrMalformedResult) || err.Error() != tc.want {
				t.Errorf("HotspotsFromRaw = %v, %v\nwant the error %q", hs, err, tc.want)
			}
		})
	}
}

// The result shares nothing with the document it was built from, and Raw
// shares nothing with the result: neither the caller's document nor one
// returned earlier can change what the next Raw returns.
func TestFromRaw_SharesNothing(t *testing.T) {
	doc := decode(t, `{"status":"completed","kind":"methodHotspots","entityId":"PROCESS-1","pollCount":0,"result":{"dataRootNodeId":"1","dataNodes":[
		{"id":"1","className":"C","methodName":"m","samples":{"RUNNING":3},"childIds":["2"],"tags":["a","b"]}]}}`)
	hs, err := profiling.HotspotsFromRaw(doc)
	if err != nil {
		t.Fatal(err)
	}
	want := throughJSON(t, hs.Raw())

	node := func(d map[string]interface{}) map[string]interface{} {
		return d["result"].(map[string]interface{})["dataNodes"].([]interface{})[0].(map[string]interface{})
	}
	node(doc)["tags"].([]interface{})[0] = "changed"
	node(doc)["samples"].(map[string]interface{})["RUNNING"] = float64(99)
	first := hs.Raw()
	if !reflect.DeepEqual(first, want) {
		t.Errorf("after the caller changed its document, Raw() = %#v\nwant %#v", first, want)
	}
	node(first)["tags"].([]interface{})[1] = "changed"
	node(first)["childIds"].([]interface{})[0] = "9"
	if got := hs.Raw(); !reflect.DeepEqual(got, want) {
		t.Errorf("after the caller changed an earlier Raw(), Raw() = %#v\nwant %#v", got, want)
	}
}

// --raw -o yaml prints the document too, and a YAML decoder gives its integers
// as int. Such a document is refused, not read as a process without samples:
// the constructors take the document as decoded from JSON.
func TestFromRaw_DocumentDecodedFromYAML(t *testing.T) {
	hs, _ := analyses(t)
	data, err := yaml.Marshal(hs.Raw())
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]interface{}
	if err := yaml.Unmarshal(data, &doc); err != nil {
		t.Fatal(err)
	}
	again, err := profiling.HotspotsFromRaw(doc)
	if again != nil || !errors.Is(err, profiling.ErrMalformedResult) {
		t.Fatalf("HotspotsFromRaw of a YAML-decoded document = %+v, %v; want ErrMalformedResult", again, err)
	}
	if want := "profiling: malformed analysis result: pollCount has the type int, which no JSON decoder produces"; err.Error() != want {
		t.Errorf("error %q\nwant  %q", err, want)
	}
}

// The envelope keys are read strictly: a value of the wrong type is not a
// document Raw wrote.
func TestFromRaw_EnvelopeOfTheWrongTypes(t *testing.T) {
	build := func(key string, value interface{}) map[string]interface{} {
		doc := map[string]interface{}{"status": "completed", "kind": "methodHotspots", "entityId": "PROCESS-1", "pollCount": float64(0), "result": map[string]interface{}{}}
		doc[key] = value
		return doc
	}
	cases := []struct {
		name string
		doc  map[string]interface{}
		want string
	}{
		{"an entity id that is a number", build("entityId", float64(5)),
			"profiling: malformed analysis result: entityId is a number, not a string"},
		{"a poll count that is a string", build("pollCount", "bad"),
			"profiling: malformed analysis result: pollCount is a string, not a count"},
		{"a negative poll count", build("pollCount", float64(-1)),
			"profiling: malformed analysis result: pollCount is -1, not a count"},
		{"a poll count with a fraction", build("pollCount", 1.5),
			"profiling: malformed analysis result: pollCount is 1.5, not a count"},
		{"a nil map as the result", build("result", map[string]interface{}(nil)),
			"profiling: malformed analysis result: the result of a completed analysis is a nil map, not an object"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			hs, err := profiling.HotspotsFromRaw(tc.doc)
			if hs != nil || !errors.Is(err, profiling.ErrMalformedResult) || err.Error() != tc.want {
				t.Errorf("HotspotsFromRaw = %v, %v\nwant the error %q", hs, err, tc.want)
			}
		})
	}
	// Absent, both keys are what a zero value holds.
	doc := build("entityId", "")
	delete(doc, "entityId")
	delete(doc, "pollCount")
	if hs, err := profiling.HotspotsFromRaw(doc); err != nil || hs.Entity != "" || hs.PollCount != 0 {
		t.Errorf("HotspotsFromRaw without entityId and pollCount = %+v, %v", hs, err)
	}
}
