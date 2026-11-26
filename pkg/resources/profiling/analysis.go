package profiling

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strconv"

	sdkcla "github.com/Dynatrace/dtctl/sdk/api/codelevelanalysis"
)

// HotspotsRequest asks for a hotspots analysis: where a process spends its CPU
// time, and where its threads wait.
type HotspotsRequest struct {
	// Entity is the process to analyse; see EligibleEntity.
	Entity string
	// Window is the time to analyse, usually from ResolveWindow.
	Window Window
	// LeafType picks which CPU samples to attribute: LeafTypeTotal (also the
	// zero value), LeafTypeService or LeafTypeBackground.
	LeafType LeafType
	// ShowWaiting includes the samples of waiting (non-running) threads.
	ShowWaiting bool
}

// AllocationsRequest asks for an allocations analysis: where a process
// allocates memory, and what survived GC. The API calls it memoryAllocation.
type AllocationsRequest struct {
	// Entity is the process to analyse; see EligibleEntity.
	Entity string
	// Window is the time to analyse, usually from ResolveWindow.
	Window Window
	// SurvivorsOnly restricts the analysis to allocations that survived a GC.
	SurvivorsOnly bool
}

// Hotspots is the result of a hotspots analysis. Its methods read it as the
// views `dtctl exec profiling hotspots` prints; each returns every row,
// unlimited.
//
// A result is safe for concurrent use: no method changes it, and what a method
// returns belongs to the caller. That holds for its methods; the exported
// fields are plain values, set once when the result is built, and must not be
// written while another goroutine reads the result.
type Hotspots struct {
	// Entity is the id the analysis was asked for, as the caller passed it
	// (APIEntityID gives the id the API was asked about).
	Entity string
	// Status is the API's verdict: completed, or no-data.
	Status string
	// PollCount is how many times the result was polled for: 0 when the API
	// answered the request with it.
	PollCount int

	// result is the API's decoded JSON document. It is never changed and never
	// handed out, which is what makes the result safe for concurrent use.
	result interface{}
}

// Allocations is the result of an allocations analysis. Stacks reads it as the
// view `dtctl exec profiling allocations` prints. Like a Hotspots, it is safe
// for concurrent use.
type Allocations struct {
	// Entity is the id the analysis was asked for, as the caller passed it
	// (APIEntityID gives the id the API was asked about).
	Entity string
	// Status is the API's verdict: completed, or no-data.
	Status string
	// PollCount is how many times the result was polled for: 0 when the API
	// answered the request with it.
	PollCount int

	result interface{} // as Hotspots.result
}

// Hotspots runs a hotspots analysis and returns it ready to be read. A window
// that is empty or longer than MaxWindow is refused before a request is sent.
// An error is best passed to ClassifyError before it is shown.
func (h *Handler) Hotspots(ctx context.Context, r HotspotsRequest) (*Hotspots, error) {
	if err := r.Window.check(); err != nil {
		return nil, &sdkcla.Error{Phase: sdkcla.PhaseValidate, Err: err}
	}
	resp, err := h.sdk.Run(ctx, sdkcla.Payload{
		Kind:        sdkcla.KindHotspots,
		EntityID:    r.Entity,
		From:        r.Window.From,
		To:          r.Window.To,
		LeafType:    r.LeafType,
		ShowWaiting: r.ShowWaiting,
	})
	if err != nil {
		return nil, err
	}
	if err := checkResult(resp.Status, resp.Result); err != nil {
		return nil, err
	}
	return newHotspots(resp), nil
}

// Allocations runs an allocations analysis and returns it ready to be read. It
// refuses the windows Hotspots refuses. An error is best passed to
// ClassifyError before it is shown.
func (h *Handler) Allocations(ctx context.Context, r AllocationsRequest) (*Allocations, error) {
	if err := r.Window.check(); err != nil {
		return nil, &sdkcla.Error{Phase: sdkcla.PhaseValidate, Err: err}
	}
	resp, err := h.sdk.Run(ctx, sdkcla.Payload{
		Kind:          sdkcla.KindAllocations,
		EntityID:      r.Entity,
		From:          r.Window.From,
		To:            r.Window.To,
		SurvivorsOnly: r.SurvivorsOnly,
	})
	if err != nil {
		return nil, err
	}
	if err := checkResult(resp.Status, resp.Result); err != nil {
		return nil, err
	}
	return newAllocations(resp), nil
}

func newHotspots(resp *sdkcla.Response) *Hotspots {
	return &Hotspots{Entity: resp.EntityID, Status: resp.Status, PollCount: resp.PollCount, result: resp.Result}
}

func newAllocations(resp *sdkcla.Response) *Allocations {
	return &Allocations{Entity: resp.EntityID, Status: resp.Status, PollCount: resp.PollCount, result: resp.Result}
}

// The verdicts of the API a result carries in its Status.
const (
	statusCompleted = "completed"
	statusNoData    = "no-data"
)

// ErrMalformedResult is in the chain of the error for a document that is not
// an analysis result: what HotspotsFromRaw and AllocationsFromRaw return for a
// document Raw did not write, and what a Handler returns when the API answers
// a completed analysis with something other than a JSON object.
var ErrMalformedResult = errors.New("profiling: malformed analysis result")

// checkResult reports whether result is what an analysis with this status
// carries: a JSON object when it completed, nothing when there was no data.
// Read as it is, any other document would have no rows in any view and so
// pass for a process without samples.
func checkResult(status string, result interface{}) error {
	switch status {
	case statusCompleted:
		if _, ok := result.(map[string]interface{}); !ok {
			return fmt.Errorf("%w: the result of a completed analysis is %s, not an object", ErrMalformedResult, jsonKind(result))
		}
	case statusNoData:
		if result != nil {
			return fmt.Errorf("%w: an analysis without data carries a result", ErrMalformedResult)
		}
	default:
		return fmt.Errorf("%w: status %q, want %s or %s", ErrMalformedResult, status, statusCompleted, statusNoData)
	}
	return nil
}

// jsonKind names the JSON type of a decoded value.
func jsonKind(v interface{}) string {
	switch v.(type) {
	case nil:
		return "null"
	case []interface{}:
		return "an array"
	case string:
		return "a string"
	case float64:
		return "a number"
	case bool:
		return "a boolean"
	}
	return fmt.Sprintf("a %T", v)
}

// HotspotsFromRaw rebuilds a hotspots analysis from the document its Raw
// returned, so that an analysis kept as JSON can be read again without running
// it. Outside agent mode `dtctl exec profiling hotspots --raw -o json` prints
// that document; in agent mode it is the result of the envelope printed. raw is
// typically that JSON, decoded, with its numbers as float64 or, from a Decoder
// with UseNumber, as json.Number. The result keeps a copy: raw stays the
// caller's.
//
// A document of another kind of analysis, or one that Raw did not write, is an
// error that wraps ErrMalformedResult. That includes a document built in Go
// that holds a type no JSON decoder produces, such as an int or a []string of
// child ids: read as it is, it would count for nothing and pass for a process
// without samples.
func HotspotsFromRaw(raw map[string]interface{}) (*Hotspots, error) {
	e, err := openEnvelope(sdkcla.KindHotspots, raw)
	if err != nil {
		return nil, err
	}
	return &Hotspots{Entity: e.entity, Status: e.status, PollCount: e.pollCount, result: e.result}, nil
}

// AllocationsFromRaw rebuilds an allocations analysis from the document its
// Raw returned, as HotspotsFromRaw does for a hotspots analysis.
func AllocationsFromRaw(raw map[string]interface{}) (*Allocations, error) {
	e, err := openEnvelope(sdkcla.KindAllocations, raw)
	if err != nil {
		return nil, err
	}
	return &Allocations{Entity: e.entity, Status: e.status, PollCount: e.pollCount, result: e.result}, nil
}

// envelope is what the document of rawEnvelope holds.
type envelope struct {
	entity, status string
	pollCount      int
	result         interface{}
}

// openEnvelope reads a document rawEnvelope built for an analysis of kind. The
// result it returns is a copy that shares nothing with raw, in the types
// json.Unmarshal gives a document.
func openEnvelope(kind sdkcla.Kind, raw map[string]interface{}) (envelope, error) {
	if got, _ := raw["kind"].(string); got != string(kind) {
		return envelope{}, fmt.Errorf("%w: kind %q, want %s", ErrMalformedResult, got, kind)
	}
	e := envelope{}
	e.status, _ = raw["status"].(string)
	if err := checkResult(e.status, raw["result"]); err != nil {
		return envelope{}, err
	}
	e.entity, _ = raw["entityId"].(string)
	polls, err := decodedLeaf(raw["pollCount"])
	if err != nil {
		return envelope{}, fmt.Errorf("%w: pollCount%w", ErrMalformedResult, err)
	}
	if n, ok := polls.(float64); ok {
		e.pollCount = count(n)
	}
	if e.result, err = compact(raw["result"], decodedLeaf); err != nil {
		return envelope{}, fmt.Errorf("%w: result%w", ErrMalformedResult, err)
	}
	return e, nil
}

// notDecoded is a value in a document that no JSON decoder produces. Its text
// continues the path that leads to the value, which compact fills in on the
// way back up, so that a document without such a value costs no path.
type notDecoded struct {
	path string // from the walk's root, as ".dataNodes[0].childIds"
	what string
}

func (e *notDecoded) Error() string { return e.path + " " + e.what }

// under puts the step that led to a refused value in front of its path.
func under(step string, err error) error {
	var nd *notDecoded
	if errors.As(err, &nd) {
		nd.path = step + nd.path
	}
	return err
}

// decodedLeaf is a value of a document as json.Unmarshal gives it. A
// json.Number, which a Decoder with UseNumber gives, becomes the float64 the
// views read; one too large for a float64 becomes the largest, which every
// count holds at its own limit. Any other type is not a document Raw wrote:
// the views read none of them, and a slice or map among them could not be
// copied, so it would stay shared with the caller.
func decodedLeaf(v interface{}) (interface{}, error) {
	switch val := v.(type) {
	case nil, string, bool, float64:
		return v, nil
	case json.Number:
		f, err := val.Float64()
		switch {
		case err == nil:
			return f, nil
		case errors.Is(err, strconv.ErrRange) && math.IsInf(f, 0):
			return math.Copysign(math.MaxFloat64, f), nil
		}
		return nil, &notDecoded{what: fmt.Sprintf("is the json.Number %q, which is no number", string(val))}
	}
	return nil, &notDecoded{what: fmt.Sprintf("has the type %T, which no JSON decoder produces", v)}
}

// Raw is the API's whole result under the envelope keys status, kind (the
// API's name for the analysis), entityId, pollCount and result, without the
// timeseries data points and the fields that only carry chart rendering.
// Large: megabytes for a busy process. Every call builds a new document, which
// the caller may change. Every number in it is a float64, as in a document
// decoded from JSON. HotspotsFromRaw reads the document back.
func (h *Hotspots) Raw() map[string]interface{} {
	return rawEnvelope(sdkcla.KindHotspots, h.Entity, h.Status, h.PollCount, h.result)
}

// Raw is the API's whole result, as Hotspots.Raw gives it. AllocationsFromRaw
// reads the document back.
func (a *Allocations) Raw() map[string]interface{} {
	return rawEnvelope(sdkcla.KindAllocations, a.Entity, a.Status, a.PollCount, a.result)
}

// rawEnvelope builds the document Raw returns. compactResult copies every map
// and slice it walks, so the document shares nothing with result.
func rawEnvelope(kind sdkcla.Kind, entity, status string, pollCount int, result interface{}) map[string]interface{} {
	return map[string]interface{}{
		"status":   status,
		"kind":     string(kind),
		"entityId": entity,
		// A float64, as every other number in the decoded document is.
		"pollCount": float64(pollCount),
		"result":    compactResult(result),
	}
}

// StackView is the stack view of a hotspots analysis.
type StackView struct {
	// Rows has one row per unique call stack, by running samples, descending.
	Rows []StackRow
	// Samples is the number of running samples over all rows.
	Samples int
	// Empty says why there are no rows, and is empty when there are some.
	Empty EmptyReason
}

// Stacks reads the analysis as one row per unique call stack. appOnly keeps
// only application frames in each stack.
func (h *Hotspots) Stacks(appOnly bool) StackView {
	v := StackView{Rows: hotspotStacks(h.result, appOnly)}
	for _, r := range v.Rows {
		v.Samples = satAddInt(v.Samples, r.Running)
	}
	if len(v.Rows) == 0 {
		v.Empty = h.whyEmpty(appOnly, "")
	}
	return v
}

// AllocationView is the stack view of an allocations analysis.
type AllocationView struct {
	// Rows has one row per unique allocation path, allocation site first, by
	// allocated bytes (by survived bytes when asked for), descending.
	Rows []AllocationRow
	// AllocatedBytes and SurvivedBytes are the totals over all rows.
	// SurvivedBytes is summed per GC survived and is not a size (see
	// AllocationRow).
	AllocatedBytes int64
	SurvivedBytes  int64
	// Empty says why there are no rows, and is empty when there are some.
	Empty EmptyReason
}

// Stacks reads the analysis as one row per unique allocation path. appOnly
// keeps only application frames in each path; bySurvivors ranks the rows by
// survived bytes.
func (a *Allocations) Stacks(appOnly, bySurvivors bool) AllocationView {
	v := AllocationView{Rows: allocationStacks(a.result, appOnly, bySurvivors)}
	for _, r := range v.Rows {
		v.AllocatedBytes = satAdd(v.AllocatedBytes, r.AllocBytes)
		v.SurvivedBytes = satAdd(v.SurvivedBytes, r.SurvivedBytes)
	}
	switch {
	case len(v.Rows) > 0:
	case len(allocationStacks(a.result, false, false)) == 0:
		v.Empty = EmptyNoSamples
	default:
		v.Empty = EmptyAppOnly
	}
	return v
}

// Functions reads the analysis as one row per function: its self samples
// (with the function running), its total samples (with it anywhere on the
// stack) and the number of call paths that reach it. state selects the thread
// states counted and by the ranking; an unknown one is an error
// (ErrUnknownState, ErrUnknownSortKey). appOnly gives a hidden frame's samples
// to the nearest application frame above it, as in the stack view.
func (h *Hotspots) Functions(appOnly bool, state State, by SortKey) (FunctionView, error) {
	state, err := ParseState(string(state))
	if err != nil {
		return FunctionView{}, err
	}
	if by, err = ParseSortKey(string(by)); err != nil {
		return FunctionView{}, err
	}
	v := hotspotFunctions(h.result, appOnly, state, by)
	if len(v.Rows) == 0 {
		v.Empty = h.whyEmpty(appOnly, state)
	}
	return v, nil
}

// Focus reads the analysis as one function's callers and callees, counting
// the samples of state. pattern names the function as a person types it: an
// exact frame label, or a substring of exactly one. It is matched against the
// functions that have samples in state, so a label without any there is not
// found as itself and can match a longer one as a substring; the view's
// Function says which function was focused. FocusExact takes a label only.
//
// A pattern that matches no function is a *NoMatchError, one that matches
// several an *AmbiguousMatchError and an empty one ErrEmptyPattern; an unknown
// state is an error that wraps ErrUnknownState. An analysis with nothing to
// match against is not an error: the view's Empty says why.
func (h *Hotspots) Focus(appOnly bool, state State, pattern string) (FocusView, error) {
	return h.focus(appOnly, state, pattern, false)
}

// FocusExact is Focus for a caller that holds a frame label, such as the
// Function of a row: function names that frame and nothing else, so a label
// without samples in state is a *NoMatchError, never another function.
func (h *Hotspots) FocusExact(appOnly bool, state State, function string) (FocusView, error) {
	return h.focus(appOnly, state, function, true)
}

func (h *Hotspots) focus(appOnly bool, state State, pattern string, exact bool) (FocusView, error) {
	state, err := ParseState(string(state))
	if err != nil {
		return FocusView{}, err
	}
	v, err := hotspotFocus(h.result, appOnly, state, pattern, exact)
	if err != nil {
		return FocusView{}, err
	}
	if v.Function == "" {
		v.Empty = h.whyEmpty(appOnly, state)
	}
	return v, nil
}

// EmptyReason names why a view of an analysis has no rows. A view carries the
// reason for the arguments it was read with. Like the rest of the package the
// values are experimental: more may be added, so a caller needs a default for
// a reason it does not know.
type EmptyReason string

const (
	// EmptyNoSamples: the window holds no samples for the entity at all.
	EmptyNoSamples EmptyReason = "no_samples"
	// EmptyState: the window holds samples, but none in the thread state a
	// per-function view counted.
	EmptyState EmptyReason = "no_state_samples"
	// EmptyAppOnly: there are samples to show, but appOnly removed every
	// frame they are on.
	EmptyAppOnly EmptyReason = "app_only"
)

// whyEmpty says why a view that has no rows has none. appOnly is what the view
// was read with, and state the thread state a per-function view counted, empty
// for the stack view. The reason is read from the data, in a fixed order: no
// samples at all, then none in that state, and only then appOnly, which is all
// that is left to have removed them. A view read without appOnly never has
// that reason.
func (h *Hotspots) whyEmpty(appOnly bool, state State) EmptyReason {
	if len(hotspotStacks(h.result, false)) == 0 {
		return EmptyNoSamples
	}
	if state != "" && (!appOnly || hotspotFunctions(h.result, false, state, SortBySelf).Samples == 0) {
		return EmptyState
	}
	return EmptyAppOnly
}

// visualOnlyKeys are fields that carry UI rendering metadata with no
// analytical value.
var visualOnlyKeys = map[string]bool{
	"color": true, // hex colour for charts
	"uuid":  true, // internal render ID
}

// emptyOnlyKeys are dropped only when they carry nothing. fileName/filePath
// are null on Java frames, but on Node.js they are the only place a frame's
// file lives (its className is empty), and the folded renderer labels frames
// from them.
var emptyOnlyKeys = map[string]bool{
	"fileName": true,
	"filePath": true,
}

// compactResult copies an analysis result without its timeseries dataPoints
// and visual-only fields, so Raw carries signal without chart/render noise.
// The result is the API's document as json.Unmarshal decoded it, so every
// value that is no object or array is kept as it is.
func compactResult(v interface{}) interface{} {
	out, _ := compact(v, func(leaf interface{}) (interface{}, error) { return leaf, nil })
	return out
}

// compact is the walk of compactResult. It copies every object and array, and
// hands every other value to leaf, for the caller that cannot trust where the
// document came from.
func compact(v interface{}, leaf func(interface{}) (interface{}, error)) (interface{}, error) {
	switch val := v.(type) {
	case map[string]interface{}:
		out := make(map[string]interface{}, len(val))
		for k, child := range val {
			if visualOnlyKeys[k] {
				continue
			}
			if emptyOnlyKeys[k] && (child == nil || child == "") {
				continue
			}
			if k == "dataPoints" {
				if arr, ok := child.([]interface{}); ok {
					// A float64, as every other number in the document is.
					out["dataPointsCount"] = float64(len(arr))
					continue
				}
			}
			if k == "dataPointsCount" {
				if _, ok := val["dataPoints"].([]interface{}); ok {
					continue // the points are there to be counted
				}
			}
			copied, err := compact(child, leaf)
			if err != nil {
				return nil, under("."+k, err)
			}
			out[k] = copied
		}
		return out, nil
	case []interface{}:
		out := make([]interface{}, len(val))
		for i, item := range val {
			copied, err := compact(item, leaf)
			if err != nil {
				return nil, under(fmt.Sprintf("[%d]", i), err)
			}
			out[i] = copied
		}
		return out, nil
	}
	return leaf(v)
}
