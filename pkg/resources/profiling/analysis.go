package profiling

import (
	"context"

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
	LeafType string
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
// returns belongs to the caller.
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

// Hotspots runs a hotspots analysis and returns it ready to be read. An error
// is best passed to ClassifyError before it is shown.
func (h *Handler) Hotspots(ctx context.Context, r HotspotsRequest) (*Hotspots, error) {
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
	return newHotspots(resp), nil
}

// Allocations runs an allocations analysis and returns it ready to be read. An
// error is best passed to ClassifyError before it is shown.
func (h *Handler) Allocations(ctx context.Context, r AllocationsRequest) (*Allocations, error) {
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
	return newAllocations(resp), nil
}

func newHotspots(resp *sdkcla.Response) *Hotspots {
	return &Hotspots{Entity: resp.EntityID, Status: resp.Status, PollCount: resp.PollCount, result: resp.Result}
}

func newAllocations(resp *sdkcla.Response) *Allocations {
	return &Allocations{Entity: resp.EntityID, Status: resp.Status, PollCount: resp.PollCount, result: resp.Result}
}

// Raw is the API's whole result under the envelope keys status, kind (the
// API's name for the analysis), entityId, pollCount and result, without the
// timeseries data points and the fields that only carry chart rendering.
// Large: megabytes for a busy process. Every call builds a new document, which
// the caller may change.
func (h *Hotspots) Raw() map[string]interface{} {
	return rawEnvelope(sdkcla.KindHotspots, h.Entity, h.Status, h.PollCount, h.result)
}

// Raw is the API's whole result, as Hotspots.Raw gives it.
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
		v.Samples += r.Running
	}
	if len(v.Rows) == 0 {
		v.Empty = h.whyEmpty("")
	}
	return v
}

// AllocationView is the stack view of an allocations analysis.
type AllocationView struct {
	// Rows has one row per unique allocation path, allocation site first, by
	// allocated bytes (by survived bytes when asked for), descending.
	Rows []AllocationRow
	// AllocatedBytes and SurvivedBytes are the totals over all rows.
	// SurvivedBytes is summed per GC survived and is not a size (SurvivedNote).
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
		v.AllocatedBytes += int64(r.AllocBytes)
		v.SurvivedBytes += int64(r.SurvivedBytes)
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
		v.Empty = h.whyEmpty(state)
	}
	return v, nil
}

// Focus reads the analysis as one function's callers and callees, counting
// the samples of state. pattern names the function: an exact frame label, or a
// substring of exactly one. A pattern that matches no function is a
// *NoMatchError and one that matches several an *AmbiguousMatchError; an
// unknown state is an error that wraps ErrUnknownState. An analysis with
// nothing to match against is not an error: the view's Empty says why.
func (h *Hotspots) Focus(appOnly bool, state State, pattern string) (FocusView, error) {
	state, err := ParseState(string(state))
	if err != nil {
		return FocusView{}, err
	}
	v, err := hotspotFocus(h.result, appOnly, state, pattern)
	if err != nil {
		return FocusView{}, err
	}
	if v.Function == "" {
		v.Empty = h.whyEmpty(state)
	}
	return v, nil
}

// EmptyReason names why a view of an analysis has no rows. A view carries the
// reason for the arguments it was read with. The values follow the tier of
// `dtctl exec profiling` (docs/STABILITY.md): while the command is
// experimental, they may change with it.
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

// whyEmpty says why a view that has no rows has none. state is the thread
// state a per-function view counted, and empty for the stack view. The reason
// is read from the data, in a fixed order: no samples at all, then none in
// that state, and only then appOnly, which is all that is left to have removed
// them.
func (h *Hotspots) whyEmpty(state State) EmptyReason {
	if len(hotspotStacks(h.result, false)) == 0 {
		return EmptyNoSamples
	}
	if state != "" && hotspotFunctions(h.result, false, state, SortBySelf).Samples == 0 {
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
func compactResult(v interface{}) interface{} {
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
					out["dataPointsCount"] = len(arr)
					continue
				}
			}
			out[k] = compactResult(child)
		}
		return out
	case []interface{}:
		out := make([]interface{}, len(val))
		for i, item := range val {
			out[i] = compactResult(item)
		}
		return out
	}
	return v
}
